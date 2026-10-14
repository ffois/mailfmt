package tnef

import (
	"encoding/binary"
	"strings"
	"time"

	"github.com/ffois/mailfmt/internal/email"
	"github.com/ffois/mailfmt/internal/rtf"
)

// Message-level attributes MS_ReadMessage reads when attMsgProps does not
// carry the property, matched with their type as the decoder matches them.
const (
	msAttFrom         = 0x00008000 // atpTriples
	msAttDateSent     = 0x00038005 // atpDate
	msAttDateReceived = 0x00038006 // atpDate
	msAttBody         = 0x0002800C // atpText
	msAttPriority     = 0x0004800D // atpShort
	msAttOemCodepage  = 0x00069007 // atpLong
)

// MS_ReadMessage reads an attached message carried in a winmail.dat, the
// TNEF stream of an Embedded attachment's Data, the way Outlook shows it when
// it is opened from the attachment list. Its properties are those of
// attMsgProps, and an attribute stands in for a property attMsgProps does not
// carry:
//
//   - Subject from PR_SUBJECT, else attSubject; ConversationTopic from
//     PR_CONVERSATION_TOPIC, else derived from the subject, as for an
//     attached message stored in a .msg;
//   - From from PR_SENDER_NAME and PR_SENDER_EMAIL_ADDRESS (else the
//     represented sender), else attFrom;
//   - To, Cc and Bcc from attRecipTable, `"Name" <address>` with
//     PR_SMTP_ADDRESS, else PR_EMAIL_ADDRESS;
//   - Date from PR_CLIENT_SUBMIT_TIME, else attDateSent, else the delivery
//     time; Importance from PR_IMPORTANCE, else attPriority; Unsent from
//     PR_MESSAGE_FLAGS;
//   - the body as for a .msg: PR_HTML (binary, in PR_INTERNET_CPID), else HTML
//     encapsulated in PR_RTF_COMPRESSED, else PR_BODY, else attBody in the
//     container's OEM code page; the attachments as MS_Decode reads them,
//     hidden as in a .msg: flagged ATT_MHTML_REF, or referred to by an HTML
//     body.
func MS_ReadMessage(blob []byte) (*email.MSData, error) {
	d, err := MS_Decode(blob)
	if err != nil {
		return nil, err
	}
	p := d.Props
	result := &email.MSData{
		Importance:         email.MSImportanceNormal,
		ConversationTopic:  msPropString(p, 0x0070),
		DroppedAttachments: d.Dropped,
	}
	result.Subject = d.Subject
	_, unicodeTopic := p[0x0070001F]
	_, ansiTopic := p[0x0070001E]
	if !unicodeTopic && !ansiTopic {
		result.ConversationTopic = email.MSConversationTopic(result.Subject)
	}
	result.From = d.msSender()
	result.To, result.Cc, result.Bcc = msSplitRecipients(d.Recipients)
	result.Date = d.msDate()

	if v := p[0x00170003]; len(v) >= 4 {
		result.Importance = email.MSImportance(int32(binary.LittleEndian.Uint32(v)))
	} else if v := d.Attributes[msAttPriority]; len(v) >= 2 {
		switch binary.LittleEndian.Uint16(v) {
		case 1:
			result.Importance = email.MSImportanceHigh
		case 3:
			result.Importance = email.MSImportanceLow
		}
	}
	if v := p[0x0E070003]; len(v) >= 4 {
		result.Unsent = binary.LittleEndian.Uint32(v)&0x8 != 0
	}

	result.Body, result.BodyFormat, result.RTFBody = d.msBody()
	isHTML := result.BodyFormat == email.MSBodyHTML
	atts := append([]email.MSAttachment(nil), d.Attachments...)
	for i := range atts {
		if i < len(d.attachFlags) && d.attachFlags[i]&0x4 != 0 { // ATT_MHTML_REF, whatever the body
			atts[i].Hidden = true
		}
	}
	if isHTML {
		email.MSHideReferenced(result.Body, atts)
		result.Body = email.MSResolveCIDs(result.Body, atts)
	}
	result.MSAttachments = atts
	result.Attachments = email.MSVisible(atts)
	result.IsPec, result.HasInnerEmail = email.MSPecFlags(atts, false)
	return result, nil
}

// msSender is the sender, else the represented sender, else attFrom.
func (d *MSDecoded) msSender() string {
	p := d.Props
	name := firstNonEmpty(msPropString(p, 0x0C1A), msPropString(p, 0x0042))
	addr := firstNonEmpty(msPropString(p, 0x0C1F), msPropString(p, 0x0065))
	if name == "" && addr == "" {
		name, addr = msTriple(d.Attributes[msAttFrom])
	}
	return email.MSFormatAddress(name, addr)
}

// msTriple reads an atpTriples value (a TRP structure): the display name,
// then the address, written TYPE:ADDRESS.
func msTriple(b []byte) (name, addr string) {
	if len(b) < 8 {
		return "", ""
	}
	cch := int(binary.LittleEndian.Uint16(b[4:]))
	cb := int(binary.LittleEndian.Uint16(b[6:]))
	if 8+cch > len(b) {
		return "", ""
	}
	name = strings.TrimRight(email.DecodeString8(b[8:8+cch]), "\x00")
	if end := 8 + cch + cb; cb > 0 && end <= len(b) {
		addr = strings.TrimRight(email.DecodeString8(b[8+cch:end]), "\x00")
		if i := strings.IndexByte(addr, ':'); i >= 0 {
			addr = addr[i+1:]
		}
	}
	return name, addr
}

// msSplitRecipients splits the recipient table by PR_RECIPIENT_TYPE; a type
// other than Cc or Bcc counts as To.
func msSplitRecipients(recips []MSRecipient) (to, cc, bcc []string) {
	for _, r := range recips {
		entry := email.MSFormatAddress(r.Name, firstNonEmpty(r.SMTPAddress, r.Address))
		if entry == "" {
			continue
		}
		switch uint32(r.Type) &^ 0x90000000 { // MAPI_P1, MAPI_SUBMITTED
		case 2:
			cc = append(cc, entry)
		case 3:
			bcc = append(bcc, entry)
		default:
			to = append(to, entry)
		}
	}
	return to, cc, bcc
}

// msDate is the submit time, else attDateSent, else the delivery time, else
// attDateReceived.
func (d *MSDecoded) msDate() string {
	if t, ok := msFiletime(d.Props[0x00390040]); ok {
		return t.Format(time.RFC1123Z)
	}
	if t, ok := msAtpDate(d.Attributes[msAttDateSent]); ok {
		return t.Format(time.RFC1123Z)
	}
	if t, ok := msFiletime(d.Props[0x0E060040]); ok {
		return t.Format(time.RFC1123Z)
	}
	if t, ok := msAtpDate(d.Attributes[msAttDateReceived]); ok {
		return t.Format(time.RFC1123Z)
	}
	return ""
}

// msFiletime reads a PT_SYSTIME value.
func msFiletime(v []byte) (time.Time, bool) {
	if len(v) < 8 {
		return time.Time{}, false
	}
	ft := binary.LittleEndian.Uint64(v)
	if ft == 0 {
		return time.Time{}, false
	}
	const ticksPerSecond = 10_000_000
	const unixEpochOffset = 11_644_473_600
	return time.Unix(int64(ft/ticksPerSecond)-unixEpochOffset, int64(ft%ticksPerSecond)*100).UTC(), true
}

// msAtpDate reads an atpDate value: year, month, day, hour, minute, second
// and day of week, two bytes each, taken as UTC.
func msAtpDate(v []byte) (time.Time, bool) {
	if len(v) < 12 {
		return time.Time{}, false
	}
	w := func(i int) int { return int(binary.LittleEndian.Uint16(v[i*2:])) }
	if w(0) == 0 {
		return time.Time{}, false
	}
	return time.Date(w(0), time.Month(w(1)), w(2), w(3), w(4), w(5), 0, time.UTC), true
}

// msBody returns the body, its format and the decompressed RTF, with the
// precedence of a .msg: PR_HTML, else the RTF when there is a plain body
// (PR_BODY or attBody), else that plain body.
func (d *MSDecoded) msBody() (string, email.MSBodyFormat, string) {
	p := d.Props
	var rtfBody []byte
	if len(d.RTFCompressed) > 0 {
		if out, err := rtf.Decompress(d.RTFCompressed); err == nil {
			rtfBody = out
		}
	}
	if b := p[0x10130102]; len(b) > 0 {
		if v := p[0x3FDE0003]; len(v) >= 4 {
			return rtf.DecodeCodePage(b, int(int32(binary.LittleEndian.Uint32(v)))), email.MSBodyHTML, string(rtfBody)
		}
		return email.ConvertToUTF8(string(b)), email.MSBodyHTML, string(rtfBody)
	}
	_, unicodeBody := p[0x1000001F]
	_, ansiBody := p[0x1000001E]
	attBody, hasAttBody := d.Attributes[msAttBody]
	if rtfBody != nil && (unicodeBody || ansiBody || hasAttBody) {
		if rtf.IsEncapsulatedHTML(rtfBody) {
			return rtf.MS_DeEncapsulateHTML(rtfBody), email.MSBodyHTML, string(rtfBody)
		}
		return rtf.MS_RTFToText(rtfBody), email.MSBodyRTF, string(rtfBody)
	}
	if unicodeBody || ansiBody {
		return msPropString(p, 0x1000), email.MSBodyPlain, string(rtfBody)
	}
	if hasAttBody {
		b := []byte(strings.TrimRight(string(attBody), "\x00"))
		if v := d.Attributes[msAttOemCodepage]; len(v) >= 4 {
			return rtf.DecodeCodePage(b, int(binary.LittleEndian.Uint32(v))), email.MSBodyPlain, string(rtfBody)
		}
		return email.DecodeString8(b), email.MSBodyPlain, string(rtfBody)
	}
	return "", email.MSBodyPlain, string(rtfBody)
}
