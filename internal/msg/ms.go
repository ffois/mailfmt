package msg

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ffois/mailfmt/internal/email"
)

const (
	// msPropertiesStream holds the fixed-length properties of a message:
	// a header, then one 16-byte entry per property.
	msPropertiesStream = "__properties_version1.0"

	// msTopLevelHeaderSize is the header of a top-level message's property
	// stream; embedded messages use 24 bytes, recipients and attachments 8.
	msTopLevelHeaderSize = 32
	msPropertyEntrySize  = 16

	msTagImportance         = 0x00170003
	msTagMessageFlags       = 0x0E070003
	msTagClientSubmitTime   = 0x00390040
	msTagMessageDeliverTime = 0x0E060040

	msMsgFlagUnsent = 0x8

	// msSubObjectHeaderSize is the property stream header of an attachment
	// or recipient.
	msSubObjectHeaderSize = 8

	msTagAttachFlags      = 0x37140003
	msTagAttachmentHidden = 0x7FFE000B
	msAttInvisibleInHTML  = 0x1

	msPidTagAttachContentLocation = 0x3713
	msPidTagDisplayName           = 0x3001
)

// errMSCorruptProperties is what OLMAPI32.DLL reports as MAPI_E_CORRUPT_DATA
// for a property stream it cannot hold a header in.
var errMSCorruptProperties = errors.New("msg: corrupt property stream (MAPI_E_CORRUPT_DATA)")

// MS_ReadFile parses the Outlook MSG file at path. See MS_Read.
func MS_ReadFile(path string) (*email.MSData, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func(f *os.File) {
		_ = f.Close()
	}(f)
	return MS_Read(f)
}

// MS_Read parses an Outlook MSG message from r like Read, and also reads the
// fixed-length properties that Read ignores, from __properties_version1.0,
// the way Outlook 2010 (OLMAPI32.DLL) loads them:
//
//   - an empty or missing stream means no fixed-length properties;
//   - a stream shorter than its 32-byte header is MAPI_E_CORRUPT_DATA;
//   - the rest is read as 16-byte entries, a trailing partial entry ignored.
//
// From them it fills Importance (PR_IMPORTANCE), Unsent (MSGFLAG_UNSENT in
// PR_MESSAGE_FLAGS) and, when the message has none, Date from
// PR_CLIENT_SUBMIT_TIME or else PR_MESSAGE_DELIVERY_TIME. ConversationTopic
// is the stored PR_CONVERSATION_TOPIC.
func MS_Read(r io.ReaderAt) (*email.MSData, error) {
	cfb, err := newCFBReader(r)
	if err != nil {
		return nil, err
	}
	if cfb.root == nil {
		return nil, errors.New("no root directory")
	}

	fixed, err := msReadFixedProps(cfb)
	if err != nil {
		return nil, err
	}

	props := rootProps(cfb)
	result := &email.MSData{
		Data:              *buildMessage(cfb, props),
		Importance:        email.MSImportanceNormal,
		ConversationTopic: getPropString(props, pidTagConversationTopic),
	}

	// Outlook keeps one attachment per attachment object; it does not open
	// multipart attachments the way buildMessage does for EMLy.
	isHTML := getPropString(props, pidTagBodyHTML) != "" || getPropBinary(props, pidTagBodyHTML) != "" ||
		htmlFromCompressedRTF(props) != ""
	result.MSAttachments = msReadAttachments(cfb, isHTML)
	result.Attachments = email.MSVisible(result.MSAttachments)
	result.HasInnerEmail = false
	for _, a := range result.MSAttachments {
		result.HasInnerEmail = result.HasInnerEmail || a.Embedded
	}

	if v, ok := fixed[msTagImportance]; ok {
		result.Importance = email.MSImportance(int32(v))
	}
	if v, ok := fixed[msTagMessageFlags]; ok {
		result.Unsent = uint32(v)&msMsgFlagUnsent != 0
	}
	if result.Date == "" {
		for _, tag := range []uint32{msTagClientSubmitTime, msTagMessageDeliverTime} {
			if v, ok := fixed[tag]; ok && v != 0 {
				result.Date = msFiletime(v).Format(time.RFC1123Z)
				break
			}
		}
	}
	return result, nil
}

// msReadFixedProps returns the 8-byte value of every entry in the root's
// property stream, keyed by property tag.
func msReadFixedProps(cfb *cfbReader) (map[uint32]uint64, error) {
	var stream *dirNode
	for _, child := range cfb.root.Children {
		if child.Entry.ObjectType == 2 && child.Name == msPropertiesStream {
			stream = child
			break
		}
	}
	props := make(map[uint32]uint64)
	if stream == nil {
		return props, nil
	}

	data, err := cfb.readNodeStream(stream)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return props, nil
	}
	if len(data) < msTopLevelHeaderSize {
		return nil, errMSCorruptProperties
	}

	for off := msTopLevelHeaderSize; off+msPropertyEntrySize <= len(data); off += msPropertyEntrySize {
		tag := binary.LittleEndian.Uint32(data[off:])
		props[tag] = binary.LittleEndian.Uint64(data[off+8:])
	}
	return props, nil
}

// msReadAttachments reads every __attach_version1.0_# storage, in the order
// of their PR_ATTACH_NUM, into the attachment Outlook holds.
//
// An attachment is hidden when PR_ATTACHMENT_HIDDEN is set, or when its
// PR_ATTACH_FLAGS carry ATT_INVISIBLE_IN_HTML and the body is HTML: the flag
// OUTLMIME.DLL sets on parts an HTML body refers to. An attachment whose
// PR_ATTACH_DATA_OBJ is a storage is an attached message (Embedded, no Data).
func msReadAttachments(cfb *cfbReader, isHTML bool) []email.MSAttachment {
	var out []email.MSAttachment
	for _, node := range cfb.root.Children {
		if node.Entry.ObjectType != 1 || !strings.HasPrefix(node.Name, "__attach_version1.0_#") {
			continue
		}
		att := email.MSAttachment{}
		var flags uint32
		var hidden bool

		for _, child := range node.Children {
			switch {
			case child.Entry.ObjectType == 1 && strings.HasPrefix(child.Name, "__substg1.0_3701"):
				att.Embedded = true
			case child.Entry.ObjectType == 2 && child.Name == msPropertiesStream:
				fixed, _ := cfb.readNodeStream(child)
				for off := msSubObjectHeaderSize; off+msPropertyEntrySize <= len(fixed); off += msPropertyEntrySize {
					switch binary.LittleEndian.Uint32(fixed[off:]) {
					case msTagAttachFlags:
						flags = binary.LittleEndian.Uint32(fixed[off+8:])
					case msTagAttachmentHidden:
						hidden = fixed[off+8] != 0
					}
				}
			case child.Entry.ObjectType == 2 && strings.HasPrefix(child.Name, "__substg1.0_"):
				propID, propType := parsePropertyName(child.Name)
				data, _ := cfb.readNodeStream(child)
				switch propID {
				case pidTagAttachLongFilename:
					if s := decodePropertyString(data, propType); s != "" {
						att.Filename = s
					}
				case pidTagAttachFilename:
					if att.Filename == "" {
						att.Filename = decodePropertyString(data, propType)
					}
				case pidTagAttachMimeTag:
					att.ContentType = decodePropertyString(data, propType)
				case pidTagAttachContentId:
					att.ContentID = strings.Trim(decodePropertyString(data, propType), "<>")
				case msPidTagAttachContentLocation:
					att.ContentLocation = decodePropertyString(data, propType)
				case msPidTagDisplayName:
					att.DisplayName = decodePropertyString(data, propType)
				case pidTagAttachData:
					if propType == propTypeBinary {
						att.Data = data
					}
				}
			}
		}

		if att.DisplayName == "" {
			att.DisplayName = att.Filename
		}
		if att.Filename == "" {
			att.Filename = att.DisplayName
		}
		att.Hidden = hidden || (isHTML && flags&msAttInvisibleInHTML != 0)
		out = append(out, att)
	}
	return out
}

// msFiletime converts a Windows FILETIME (100 ns ticks since 1601) to UTC.
func msFiletime(ft uint64) time.Time {
	const ticksPerSecond = 10_000_000
	const unixEpochOffset = 11_644_473_600 // seconds from 1601-01-01 to 1970-01-01
	secs := int64(ft/ticksPerSecond) - unixEpochOffset
	nsec := int64(ft%ticksPerSecond) * 100
	return time.Unix(secs, nsec).UTC()
}
