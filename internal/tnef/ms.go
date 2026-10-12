package tnef

import (
	"encoding/binary"
	"fmt"
	"mime"
	"strings"
	"unicode/utf16"

	"github.com/ffois/mailfmt/internal/email"
)

// MSRecipient is one row of a winmail.dat recipient table.
type MSRecipient struct {
	Name    string
	Address string
	// Type is PR_RECIPIENT_TYPE: 1 To, 2 Cc, 3 Bcc.
	Type int
}

// MSDecoded is what Outlook takes out of a winmail.dat.
type MSDecoded struct {
	// Attachments in container order. An attached message stays one
	// attachment, Embedded, whose Data is its own TNEF stream: Outlook
	// shows it as a message, it does not spill its contents.
	Attachments []email.MSAttachment

	// CorrelationKey is PR_TNEF_CORRELATION_KEY, without its terminator.
	CorrelationKey string

	// RTFCompressed is PR_RTF_COMPRESSED. Outlook's exclusion list keeps
	// the MIME subject, sender, recipients, dates and plain/HTML bodies,
	// but not this one, so it is copied onto the message.
	RTFCompressed []byte

	// MessageClass is PR_MESSAGE_CLASS.
	MessageClass string

	// Recipients is the recipient table (attRecipTable). Outlook takes it
	// only for Report.* messages.
	Recipients []MSRecipient
}

// msUntitledAttachment is the name OLMAPI32.DLL gives a TNEF attachment
// without a title.
const msUntitledAttachment = "Untitled Attachment"

// MS_Decode reads a winmail.dat the way Outlook does. Like Extract it is a
// trust boundary: the bytes come from an unvetted message, so every offset
// is checked and a panic is returned as an error.
func MS_Decode(blob []byte) (d *MSDecoded, err error) {
	defer func() {
		if r := recover(); r != nil {
			d = nil
			err = fmt.Errorf("tnef: panic decoding %d-byte blob: %v", len(blob), r)
		}
	}()
	return msDecode(blob)
}

type msRawAttachment struct {
	title string
	data  []byte
	props map[uint32][]byte
}

func msDecode(blob []byte) (*MSDecoded, error) {
	if len(blob) < 6 || !IsTNEFData(blob) {
		return nil, fmt.Errorf("tnef: missing signature")
	}

	d := &MSDecoded{}
	var atts []*msRawAttachment
	var cur *msRawAttachment

	// level(1) id(4) length(4) data(length) checksum(2), after signature(4)
	// and key(2).
	for off := 6; off+9 <= len(blob); {
		level := blob[off]
		name := binary.LittleEndian.Uint32(blob[off+1:]) & 0xFFFF
		length := int64(binary.LittleEndian.Uint32(blob[off+5:]))
		start := int64(off + 9)
		if start+length > int64(len(blob)) {
			break
		}
		data := blob[start : start+length]
		off = int(start+length) + 2

		switch {
		case level == 2 && name == 0x9002: // attAttachRendData opens an attachment
			cur = &msRawAttachment{}
			atts = append(atts, cur)
		case level == 2 && cur == nil:
			// An attachment attribute before any attAttachRendData belongs to
			// nothing; the upstream decoder dereferences nil here.
		case level == 2 && name == 0x8010: // attAttachTitle
			cur.title = email.DecodeString8(data)
		case level == 2 && name == 0x800F: // attAttachData
			cur.data = data
		case level == 2 && name == 0x9005: // attAttachment
			cur.props = msScanProps(data)
		case level == 1 && name == 0x9003: // attMsgProps
			props := msScanProps(data)
			d.CorrelationKey = strings.TrimRight(string(props[0x007F0102]), "\x00")
			d.RTFCompressed = props[0x10090102]
			d.MessageClass = msPropString(props, 0x001A)
		case level == 1 && name == 0x9004: // attRecipTable
			d.Recipients = msScanRecipients(data)
		}
	}

	for _, raw := range atts {
		if att, ok := msBuildAttachment(raw); ok {
			d.Attachments = append(d.Attachments, att)
		}
	}
	return d, nil
}

// msBuildAttachment turns one container record into what Outlook lists.
func msBuildAttachment(raw *msRawAttachment) (email.MSAttachment, bool) {
	p := raw.props
	att := email.MSAttachment{
		ContentID: strings.Trim(msPropString(p, 0x3712), "<>"),
		Hidden:    len(p[0x7FFE000B]) > 0 && p[0x7FFE000B][0] != 0,
	}
	method := uint32(0)
	if v := p[0x37050003]; len(v) >= 4 {
		method = binary.LittleEndian.Uint32(v)
	}

	// ATTACH_EMBEDDED_MSG: PR_ATTACH_DATA_OBJ is IID_IMessage, then the
	// message as its own TNEF stream.
	if obj := p[0x3701000D]; method == 5 && len(obj) > 16 && IsTNEFData(obj[16:]) {
		att.Embedded = true
		att.Data = obj[16:]
		att.ContentType = "application/ms-tnef"
		att.DisplayName = firstNonEmpty(msPropString(p, 0x3001), raw.title, msUntitledAttachment)
		att.Filename = att.DisplayName
		return att, true
	}

	att.Data = raw.data
	if att.Data == nil {
		att.Data = p[0x37010102]
	}
	if att.Data == nil {
		return att, false
	}
	att.Filename = firstNonEmpty(msPropString(p, 0x3707), raw.title, msUntitledAttachment)
	att.DisplayName = firstNonEmpty(msPropString(p, 0x3001), att.Filename)
	att.ContentType = msPropString(p, 0x370E)
	if att.ContentType == "" {
		att.ContentType = mimeTypeFromFilename(att.Filename)
	}
	return att, true
}

// msScanProps reads a counted MAPI property list and returns the first value
// of every property, keyed by its full tag.
func msScanProps(data []byte) map[uint32][]byte {
	props := map[uint32][]byte{}
	if len(data) < 4 {
		return props
	}
	msScanPropList(data, 4, int(binary.LittleEndian.Uint32(data)), props)
	return props
}

// msScanPropList reads count properties starting at off into props and
// returns the offset after them, or -1 when the data runs out.
func msScanPropList(data []byte, off, count int, props map[uint32][]byte) int {
	need := func(n int) bool { return off >= 0 && n >= 0 && off+n <= len(data) && off+n >= off }
	u32 := func() int {
		v := int(binary.LittleEndian.Uint32(data[off:]))
		off += 4
		return v
	}

	for i := 0; i < count; i++ {
		if !need(4) {
			return -1
		}
		tag := binary.LittleEndian.Uint32(data[off:])
		off += 4
		propType := tag & 0xFFFF
		propID := tag >> 16

		if propID >= 0x8000 { // named property: GUID, kind, then id or name
			if !need(20) {
				return -1
			}
			off += 16
			kind := u32()
			if kind == 0 {
				if !need(4) {
					return -1
				}
				off += 4
			} else {
				if !need(4) {
					return -1
				}
				n := u32()
				if !need(n) {
					return -1
				}
				off += n + padTo4(n)
			}
		}

		multi := propType&0x1000 != 0
		base := propType &^ 0x1000
		values := 1
		variable := base == 0x001E || base == 0x001F || base == 0x0102 || base == 0x000D
		if multi || variable {
			if !need(4) {
				return -1
			}
			values = u32()
			if values < 0 || values > len(data) {
				return -1
			}
		}

		for v := 0; v < values; v++ {
			var size int
			switch base {
			case 0x0002, 0x0003, 0x0004, 0x000A, 0x000B:
				size = 4
			case 0x0005, 0x0006, 0x0007, 0x0014, 0x0040:
				size = 8
			case 0x0048:
				size = 16
			case 0x001E, 0x001F, 0x0102, 0x000D:
				if !need(4) {
					return -1
				}
				size = u32()
			default:
				return -1 // unknown type: the rest cannot be located
			}
			if !need(size) {
				return -1
			}
			if v == 0 {
				if _, seen := props[tag]; !seen {
					props[tag] = data[off : off+size]
				}
			}
			off += size + padTo4(size)
		}
	}
	return off
}

// msScanRecipients reads attRecipTable: a row count, then per row a counted
// property list.
func msScanRecipients(data []byte) []MSRecipient {
	if len(data) < 4 {
		return nil
	}
	rows := int(binary.LittleEndian.Uint32(data))
	var out []MSRecipient
	off := 4
	for r := 0; r < rows && off >= 0 && off+4 <= len(data); r++ {
		count := int(binary.LittleEndian.Uint32(data[off:]))
		props := map[uint32][]byte{}
		off = msScanPropList(data, off+4, count, props)
		rec := MSRecipient{
			Name:    msPropString(props, 0x3001),
			Address: msPropString(props, 0x3003),
		}
		if v := props[0x0C150003]; len(v) >= 4 {
			rec.Type = int(binary.LittleEndian.Uint32(v))
		}
		out = append(out, rec)
	}
	return out
}

// msPropString reads a string property stored as PT_UNICODE or PT_STRING8.
func msPropString(props map[uint32][]byte, id uint32) string {
	if v, ok := props[id<<16|0x001F]; ok {
		u := make([]uint16, len(v)/2)
		for i := range u {
			u[i] = binary.LittleEndian.Uint16(v[i*2:])
		}
		return strings.TrimRight(string(utf16.Decode(u)), "\x00")
	}
	if v, ok := props[id<<16|0x001E]; ok {
		return email.DecodeString8(v)
	}
	return ""
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// MS_Expand replaces a winmail.dat with what it carries, following the rules
// Outlook 2010's MIME reader (OUTLMIME.DLL) applies, which are narrower than
// Expand's:
//
//   - a part is TNEF only when its Content-Type is application/ms-tnef or its
//     filename is winmail.dat; the vnd.ms-tnef alias and the magic bytes alone
//     do not count;
//   - only one part is decoded: each candidate overwrites the previous one, so
//     the last candidate wins and any earlier one stays a plain attachment;
//   - when the message carries an X-MS-TNEF-Correlator header, the container
//     must carry the same PR_TNEF_CORRELATION_KEY, compared case-insensitively;
//     a mismatch, or a container with no key, leaves winmail.dat untouched;
//   - an attached message stays one attachment instead of being flattened;
//   - once decoding succeeds the container is removed even if it held no files.
//
// correlator is the X-MS-TNEF-Correlator header value, empty when absent. The
// decoded container is returned too, nil when nothing was decoded.
func MS_Expand(atts []email.MSAttachment, correlator string) ([]email.MSAttachment, *MSDecoded) {
	idx := -1
	for i, att := range atts {
		if msIsTNEFAttachment(att.Attachment) {
			idx = i
		}
	}
	if idx < 0 {
		return atts, nil
	}

	d, err := MS_Decode(atts[idx].Data)
	if err != nil {
		return atts, nil
	}
	if correlator != "" && !strings.EqualFold(d.CorrelationKey, strings.TrimSpace(correlator)) {
		return atts, nil
	}

	result := make([]email.MSAttachment, 0, len(atts)-1+len(d.Attachments))
	result = append(result, atts[:idx]...)
	result = append(result, d.Attachments...)
	return append(result, atts[idx+1:]...), d
}

// msIsTNEFAttachment is the test OUTLMIME.DLL runs on each attachment part.
func msIsTNEFAttachment(att email.Attachment) bool {
	mediaType, _, err := mime.ParseMediaType(att.ContentType)
	if err != nil {
		mediaType = strings.TrimSpace(strings.Split(att.ContentType, ";")[0])
	}
	return strings.EqualFold(mediaType, "application/ms-tnef") ||
		strings.EqualFold(att.Filename, "winmail.dat")
}
