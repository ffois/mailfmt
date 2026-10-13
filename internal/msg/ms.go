package msg

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ffois/mailfmt/internal/email"
	"github.com/ffois/mailfmt/internal/eml"
	"github.com/ffois/mailfmt/internal/rtf"
)

const (
	// msPropertiesStream lists the properties of a message, attachment or
	// recipient: a header, then one 16-byte entry per property.
	msPropertiesStream = "__properties_version1.0"
	msNameIDStorage    = "__nameid_version1.0"
	msAttachPrefix     = "__attach_version1.0_#"
	msRecipPrefix      = "__recip_version1.0_#"

	// The property stream header is 32 bytes for the top-level message, 24
	// for an attached message and 8 for an attachment or recipient. A
	// message header holds the next recipient ID at offset 8 and the next
	// attachment ID at offset 12.
	msTopLevelHeaderSize  = 32
	msEmbeddedHeaderSize  = 24
	msSubObjectHeaderSize = 8
	msPropertyEntrySize   = 16

	msTagImportance         = 0x00170003
	msTagMessageFlags       = 0x0E070003
	msTagHasAttach          = 0x0E1B000B
	msTagClientSubmitTime   = 0x00390040
	msTagMessageDeliverTime = 0x0E060040
	msTagInternetCPID       = 0x3FDE0003
	msTagStoreSupportMask   = 0x340D0003

	msMsgFlagUnsent    = 0x8
	msMsgFlagHasAttach = 0x10
	msStoreUnicodeOK   = 0x00040000

	msTagAttachMethod     = 0x37050003
	msTagAttachFlags      = 0x37140003
	msTagAttachmentHidden = 0x7FFE000B
	msTagAttachDataObject = 0x3701000D
	msAttInvisibleInHTML  = 0x1
	msAttachOLE           = 6

	msTagRecipientType = 0x0C150003
	msRecipCc          = 2
	msRecipBcc         = 3

	msPidTagAttachContentLocation = 0x3713
	msPidTagDisplayName           = 0x3001
	msPidTagEmailAddress          = 0x3003
	msPidTagSMTPAddress           = 0x39FE

	msClassMultipartSigned = "IPM.Note.SMIME.MultipartSigned"
)

// The errors below are the ways Outlook 2010 refuses a .msg; each names the
// message Outlook shows.
var (
	// errMSDirectorySectors: StgOpenStorage fails with STG_E_DOCFILECORRUPT
	// ("Impossibile aprire il file").
	errMSDirectorySectors = errors.New("msg: a version 3 compound file must declare 0 directory sectors (STG_E_DOCFILECORRUPT)")

	// errMSNoProperties: "Impossibile leggere l'elemento".
	errMSNoProperties = errors.New("msg: the message has no property stream (Outlook cannot read the item)")

	// errMSCorruptProperties is MAPI_E_CORRUPT_DATA: a property stream too
	// short for its header.
	errMSCorruptProperties = errors.New("msg: corrupt property stream (MAPI_E_CORRUPT_DATA)")

	// errMSAttachmentProperties: "Autorizzazione non idonea per eseguire
	// l'operazione".
	errMSAttachmentProperties = errors.New("msg: an attachment has no property stream (Outlook: access denied)")

	// errMSNoNameID: "Operazione non riuscita. Impossibile trovare un
	// oggetto".
	errMSNoNameID = errors.New("msg: no __nameid_version1.0 storage (Outlook cannot find an object)")

	// errMSNoMessageClass: "Impossibile leggere l'elemento".
	errMSNoMessageClass = errors.New("msg: PR_MESSAGE_CLASS is not in the property stream (Outlook cannot read the item)")

	// errMSStringType: "Impossibile leggere l'elemento", for a string
	// property of the message or of an attachment that is PT_STRING8 in a
	// Unicode message or PT_UNICODE in an ANSI one.
	errMSStringType = errors.New("msg: a string property does not match the message's ANSI or Unicode mode (Outlook cannot read the item)")
)

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

// MS_Read parses an Outlook MSG message from r the way Outlook 2010 opens a
// .msg (OUTLOOK.EXE /f: StgOpenStorage, then OLMAPI32 OpenIMsgOnIStg).
//
// A message is Unicode when its PR_STORE_SUPPORT_MASK has STORE_UNICODE_OK,
// ANSI otherwise, and its string properties must all be of that kind.
//
// The file is refused, as Outlook refuses it, when the compound file is
// version 3 with a non-zero directory sector count in its header, when the
// message has no property stream (or one shorter than its header), when an
// attachment it loads has no property stream or a string of the other kind,
// when there is no __nameid_version1.0 storage, when PR_MESSAGE_CLASS is not
// listed, or when the message has a string of the other kind.
//
// A storage has exactly the properties its property stream lists: a
// __substg1.0_ stream that is not listed is ignored. Attachments and
// recipients are the storages numbered below the next IDs of the message's
// header; the counts beside them are not used. If one recipient has no
// property stream, or a string of the other kind, the message has no
// recipients. Attachments are only shown when the message says it has
// some: PR_HASATTACH, or MSGFLAG_HASATTACH in PR_MESSAGE_FLAGS.
//
// From the result:
//
//   - To, Cc and Bcc come from the recipient table, each `"Name" <address>`
//     with PR_SMTP_ADDRESS, else PR_EMAIL_ADDRESS; From is formatted the same;
//   - Body is PR_HTML (PT_BINARY only, decoded in PR_INTERNET_CPID), else the
//     HTML encapsulated in PR_RTF_COMPRESSED decoded with each font's
//     charset, with cid: references resolved, else the PR_BODY text;
//   - an attached message is Embedded, named PR_DISPLAY_NAME, with the file
//     name Outlook derives from that (".msg" when it has no name);
//   - an IPM.Note.SMIME.MultipartSigned message takes body and attachments
//     from the multipart/signed entity its attachment carries, read as
//     eml.MS_Parse reads a .eml;
//   - Importance, Unsent (MSGFLAG_UNSENT) and Date, from PR_CLIENT_SUBMIT_TIME
//     or else PR_MESSAGE_DELIVERY_TIME, come from the fixed properties;
//     Subject is PR_SUBJECT alone and ConversationTopic the stored
//     PR_CONVERSATION_TOPIC.
func MS_Read(r io.ReaderAt) (*email.MSData, error) {
	cfb, err := newCFBReader(r)
	if err != nil {
		return nil, err
	}
	if cfb.header.MajorVersion == 3 && cfb.header.DirectorySectors != 0 {
		return nil, errMSDirectorySectors
	}
	if cfb.root == nil {
		return nil, errors.New("no root directory")
	}

	m, err := msLoadMessage(cfb, cfb.root, msTopLevelHeaderSize)
	if err != nil {
		return nil, err
	}
	if msChild(cfb.root, msNameIDStorage, 1) == nil {
		return nil, errMSNoNameID
	}
	_, unicodeClass := m.props[pidTagMessageClass<<16|propTypeString]
	_, ansiClass := m.props[pidTagMessageClass<<16|propTypeString8]
	if !unicodeClass && !ansiClass {
		return nil, errMSNoMessageClass
	}
	if m.mixed(m.unicodeMode()) {
		return nil, errMSStringType
	}

	p := m.props
	result := &email.MSData{
		Importance:        email.MSImportanceNormal,
		ConversationTopic: getPropString(p, pidTagConversationTopic),
	}
	result.Subject = getPropString(p, pidTagSubject)
	result.From = msSender(p)
	result.To, result.Cc, result.Bcc = msRecipients(m.recipients)

	signed := strings.EqualFold(getPropString(p, pidTagMessageClass), msClassMultipartSigned)
	inner, err := msMultipartSigned(m, signed)
	if err != nil {
		return nil, err
	}
	var atts []email.MSAttachment
	if inner != nil {
		result.Body, result.RTFBody = inner.Body, inner.RTFBody
		atts = inner.MSAttachments
	} else {
		var isHTML bool
		result.Body, isHTML = msBody(p, m.fixed)
		if m.hasAttachments() {
			atts = msAttachments(m.attachments, isHTML)
		}
		if isHTML {
			result.Body = email.MSResolveCIDs(result.Body, atts)
		}
	}
	result.MSAttachments = atts
	result.Attachments = email.MSVisible(atts)
	result.IsPec, result.HasInnerEmail = email.MSPecFlags(atts, signed)

	if v, ok := m.fixed[msTagImportance]; ok {
		result.Importance = email.MSImportance(int32(v))
	}
	if v, ok := m.fixed[msTagMessageFlags]; ok {
		result.Unsent = uint32(v)&msMsgFlagUnsent != 0
	}
	for _, tag := range []uint32{msTagClientSubmitTime, msTagMessageDeliverTime} {
		if v, ok := m.fixed[tag]; ok && v != 0 {
			result.Date = msFiletime(v).Format(time.RFC1123Z)
			break
		}
	}
	return result, nil
}

// msObject is a message, attachment or recipient storage as OLMAPI32 loads
// it: it has the properties its property stream lists, and no others. A
// listed property whose stream is missing is empty.
type msObject struct {
	fixed   map[uint32]uint64   // fixed-length values, held in the entry
	props   map[uint32][]byte   // variable-length values, from __substg1.0_ streams
	objects map[uint32]*dirNode // PT_OBJECT values: storages, nil when missing

	ansi, unicode bool // whether PT_STRING8 / PT_UNICODE properties are listed

	nextRecip, nextAttach uint32 // messages only
}

// mixed reports whether o lists a string of the kind a message in the given
// mode cannot hold.
func (o *msObject) mixed(unicode bool) bool {
	if unicode {
		return o.ansi
	}
	return o.unicode
}

// msLoadObject reads the property stream of node, whose header is
// headerSize bytes. A trailing partial entry is ignored, as OLMAPI32 sizes
// the read in whole entries.
func msLoadObject(cfb *cfbReader, node *dirNode, headerSize int) (*msObject, error) {
	stream := msChild(node, msPropertiesStream, 2)
	if stream == nil {
		return nil, errMSNoProperties
	}
	data, err := cfb.readNodeStream(stream)
	if err != nil {
		return nil, err
	}
	if len(data) < headerSize {
		return nil, errMSCorruptProperties
	}

	o := &msObject{
		fixed:   make(map[uint32]uint64),
		props:   make(map[uint32][]byte),
		objects: make(map[uint32]*dirNode),
	}
	if headerSize >= msEmbeddedHeaderSize {
		o.nextRecip = binary.LittleEndian.Uint32(data[8:])
		o.nextAttach = binary.LittleEndian.Uint32(data[12:])
	}
	for off := headerSize; off+msPropertyEntrySize <= len(data); off += msPropertyEntrySize {
		tag := binary.LittleEndian.Uint32(data[off:])
		name := fmt.Sprintf("__substg1.0_%08X", tag)
		switch tag & 0xEFFF { // single- and multi-valued alike
		case propTypeString8:
			o.ansi = true
		case propTypeString:
			o.unicode = true
		}
		switch typ := tag & 0xFFFF; {
		case msFixedLength(typ):
			o.fixed[tag] = binary.LittleEndian.Uint64(data[off+8:])
		case typ == 0x000D: // PT_OBJECT
			o.objects[tag] = msChild(node, name, 1)
		case typ&0x1000 != 0:
			// Multi-valued: no property read here is one.
		default:
			var v []byte
			if s := msChild(node, name, 2); s != nil {
				v, _ = cfb.readNodeStream(s)
			}
			o.props[tag] = v
		}
	}
	return o, nil
}

// msFixedLength reports whether a property type is stored in its 16-byte
// entry rather than in a stream of its own.
func msFixedLength(typ uint32) bool {
	switch typ {
	case 0x0002, 0x0003, 0x0004, 0x0005, 0x0006, 0x0007, 0x000A, 0x000B, 0x0014, 0x0040:
		return true
	}
	return false
}

// msMessage is a loaded message with its attachments and recipients.
type msMessage struct {
	*msObject
	attachments []*msObject
	recipients  []*msObject
}

// msLoadMessage loads the message stored in node, then the attachments and
// recipients numbered below the next IDs in its header. An attachment
// without a property stream, or with a string of the wrong kind, fails the
// whole message; such a recipient leaves the message with no recipients.
func msLoadMessage(cfb *cfbReader, node *dirNode, headerSize int) (*msMessage, error) {
	obj, err := msLoadObject(cfb, node, headerSize)
	if err != nil {
		return nil, err
	}
	m := &msMessage{msObject: obj}
	unicode := m.unicodeMode()
	for _, n := range msNumbered(node, msAttachPrefix, obj.nextAttach) {
		a, err := msLoadObject(cfb, n, msSubObjectHeaderSize)
		if errors.Is(err, errMSNoProperties) {
			return nil, errMSAttachmentProperties
		}
		if err != nil {
			return nil, err
		}
		if a.mixed(unicode) {
			return nil, errMSStringType
		}
		m.attachments = append(m.attachments, a)
	}
	for _, n := range msNumbered(node, msRecipPrefix, obj.nextRecip) {
		r, err := msLoadObject(cfb, n, msSubObjectHeaderSize)
		if err != nil || r.mixed(unicode) {
			m.recipients = nil
			break
		}
		m.recipients = append(m.recipients, r)
	}
	return m, nil
}

// unicodeMode reports whether the message is a Unicode one.
func (m *msMessage) unicodeMode() bool {
	return uint32(m.fixed[msTagStoreSupportMask])&msStoreUnicodeOK != 0
}

// hasAttachments reports whether the message says it has attachments; the
// object model shows none otherwise, though they are loaded.
func (m *msMessage) hasAttachments() bool {
	return byte(m.fixed[msTagHasAttach]) != 0 || uint32(m.fixed[msTagMessageFlags])&msMsgFlagHasAttach != 0
}

// msChild finds a child of node by name, case-insensitively as IStorage
// does, and by object type (1 storage, 2 stream).
func msChild(node *dirNode, name string, objectType uint8) *dirNode {
	for _, c := range node.Children {
		if c.Entry.ObjectType == objectType && strings.EqualFold(c.Name, name) {
			return c
		}
	}
	return nil
}

// msNumbered returns the storages of node named prefix plus an 8-digit hex
// number below next, in number order.
func msNumbered(node *dirNode, prefix string, next uint32) []*dirNode {
	type numbered struct {
		n    uint64
		node *dirNode
	}
	var found []numbered
	for _, c := range node.Children {
		if c.Entry.ObjectType != 1 || len(c.Name) != len(prefix)+8 || !strings.EqualFold(c.Name[:len(prefix)], prefix) {
			continue
		}
		n, err := strconv.ParseUint(c.Name[len(prefix):], 16, 32)
		if err == nil && n < uint64(next) {
			found = append(found, numbered{n, c})
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].n < found[j].n })
	out := make([]*dirNode, len(found))
	for i, f := range found {
		out[i] = f.node
	}
	return out
}

// msSender is the sender, else the represented sender, as an address.
func msSender(p map[uint32][]byte) string {
	name := getPropString(p, pidTagSenderName)
	if name == "" {
		name = getPropString(p, pidTagSentRepresentingName)
	}
	addr := getPropString(p, pidTagSenderEmailAddress)
	if addr == "" {
		addr = getPropString(p, pidTagSentRepresentingAddr)
	}
	return email.MSFormatAddress(name, addr)
}

// msRecipients splits the recipient table by PR_RECIPIENT_TYPE; a type other
// than Cc or Bcc counts as To.
func msRecipients(recips []*msObject) (to, cc, bcc []string) {
	for _, r := range recips {
		addr := getPropString(r.props, msPidTagSMTPAddress)
		if addr == "" {
			addr = getPropString(r.props, msPidTagEmailAddress)
		}
		entry := email.MSFormatAddress(getPropString(r.props, msPidTagDisplayName), addr)
		if entry == "" {
			continue
		}
		// MAPI_P1 and MAPI_SUBMITTED are flags on the type.
		switch uint32(r.fixed[msTagRecipientType]) &^ 0x90000000 {
		case msRecipCc:
			cc = append(cc, entry)
		case msRecipBcc:
			bcc = append(bcc, entry)
		default:
			to = append(to, entry)
		}
	}
	return to, cc, bcc
}

// msBody returns the body Outlook shows and whether it is HTML; a plain text
// body is returned as text, as eml.MS_Parse returns it. PR_HTML is read as
// PT_BINARY only: Outlook ignores a PR_HTML stored as a string.
func msBody(p map[uint32][]byte, fixed map[uint32]uint64) (string, bool) {
	if b := p[pidTagBodyHTML<<16|propTypeBinary]; len(b) > 0 {
		if cp, ok := fixed[msTagInternetCPID]; ok {
			return rtf.DecodeCodePage(b, int(int32(cp))), true
		}
		return email.ConvertToUTF8(string(b)), true
	}
	if data, ok := p[pidTagRtfCompressed<<16|propTypeBinary]; ok {
		if out, err := rtf.Decompress(data); err == nil && rtf.IsEncapsulatedHTML(out) {
			return rtf.MS_DeEncapsulateHTML(out), true
		}
	}
	return getPropString(p, pidTagBody), false
}

// msMultipartSigned reads the entity a clear-signed S/MIME message keeps in
// its attachment: the whole multipart/signed MIME message, headers included,
// which Outlook opens in place of the message's own body and attachments.
// It returns nil for a message that is not signed, or has no such data.
func msMultipartSigned(m *msMessage, signed bool) (*email.MSData, error) {
	if !signed || len(m.attachments) == 0 {
		return nil, nil
	}
	data := m.attachments[0].props[pidTagAttachData<<16|propTypeBinary]
	if len(data) == 0 {
		return nil, nil
	}
	inner, err := eml.MS_Parse(data)
	if err != nil {
		return nil, fmt.Errorf("msg: S/MIME content: %w", err)
	}
	return inner, nil
}

// msAttachments builds the attachments Outlook holds, in number order.
//
// An attachment is hidden when PR_ATTACHMENT_HIDDEN is set, or when its
// PR_ATTACH_FLAGS carry ATT_INVISIBLE_IN_HTML and the body is HTML: the flag
// OUTLMIME.DLL sets on parts an HTML body refers to. One whose
// PR_ATTACH_DATA_OBJ is a storage, and not an OLE object, is an attached
// message: Embedded, with no Data, named PR_DISPLAY_NAME alone (its subject
// is not used), and with the file name Outlook derives from that name unless
// it stores one. Any other attachment without a display name shows its file
// name.
func msAttachments(atts []*msObject, isHTML bool) []email.MSAttachment {
	out := make([]email.MSAttachment, 0, len(atts))
	for _, o := range atts {
		p := o.props
		a := email.MSAttachment{
			DisplayName:     getPropString(p, msPidTagDisplayName),
			ContentID:       strings.Trim(getPropString(p, pidTagAttachContentId), "<>"),
			ContentLocation: getPropString(p, msPidTagAttachContentLocation),
		}
		a.Filename = getPropString(p, pidTagAttachLongFilename)
		if a.Filename == "" {
			a.Filename = getPropString(p, pidTagAttachFilename)
		}
		a.ContentType = getPropString(p, pidTagAttachMimeTag)
		a.Data = p[pidTagAttachData<<16|propTypeBinary]

		if o.objects[msTagAttachDataObject] != nil && o.fixed[msTagAttachMethod] != msAttachOLE {
			a.Embedded = true
			if a.Filename == "" {
				a.Filename = email.MSAttachedMessageFileName(a.DisplayName)
			}
		} else {
			if a.DisplayName == "" {
				a.DisplayName = a.Filename
			}
			if a.Filename == "" {
				a.Filename = a.DisplayName
			}
		}
		hidden := byte(o.fixed[msTagAttachmentHidden]) != 0
		a.Hidden = hidden || (isHTML && uint32(o.fixed[msTagAttachFlags])&msAttInvisibleInHTML != 0)
		out = append(out, a)
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
