package tnef

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"github.com/ffois/mailfmt/internal/email"
)

// msUncompressedRTF wraps rtf as a PR_RTF_COMPRESSED value in uncompressed
// ("MELA") form.
func msUncompressedRTF(rtf string) []byte {
	out := binary.LittleEndian.AppendUint32(nil, uint32(len(rtf)+12))
	out = binary.LittleEndian.AppendUint32(out, uint32(len(rtf)))
	out = binary.LittleEndian.AppendUint32(out, 0x414C454D)
	out = binary.LittleEndian.AppendUint32(out, 0)
	return append(out, rtf...)
}

// An attached message carried in a winmail.dat reads from attMsgProps: the
// HTML encapsulated in its RTF is the body, the recipient table gives To and
// Cc, and an attachment referred to by the HTML is hidden.
func TestMSReadMessage(t *testing.T) {
	sent := time.Date(2026, 1, 29, 10, 27, 0, 0, time.UTC)
	ft := binary.LittleEndian.AppendUint64(nil, uint64(sent.Unix()+11_644_473_600)*10_000_000)
	blob := newMSTNEF().
		msgProps(
			msProp{0x0037001F, msUnicode("RE: Reclamo")},
			msProp{0x0C1A001F, msUnicode("Anna Ferri")},
			msProp{0x0C1F001F, msUnicode("anna@example.it")},
			msProp{0x00390040, ft},
			msProp{0x00170003, msLong(2)},
			msProp{0x1000001F, msUnicode("Vedi")}, // with no plain body the RTF would not count
			msProp{0x10090102, msUncompressedRTF(`{\rtf1\ansi\fromhtml1 \deff0{\fonttbl{\f0 Arial;}}` +
				`{\*\htmltag64 <p>}Vedi{\*\htmltag84 <img src="cid:logo">}{\*\htmltag72 </p>}}`)},
		).
		recipients(
			[]msProp{{0x3001001F, msUnicode("Bruno")}, {0x3003001F, msUnicode("/o=Esempio/cn=bruno")}, {0x39FE001F, msUnicode("bruno@example.it")}, {0x0C150003, msLong(1)}},
			[]msProp{{0x3003001F, msUnicode("copia@example.it")}, {0x0C150003, msLong(2)}},
		).
		attachment("logo.png", []byte("PNG"), msProp{0x3712001F, msUnicode("logo")}, msProp{0x37140003, msLong(4 | 1)}).
		attachment("nota.txt", []byte("testo")).
		bytes()

	d, err := MS_ReadMessage(blob)
	if err != nil {
		t.Fatalf("MS_ReadMessage: %v", err)
	}
	if d.Subject != "RE: Reclamo" || d.ConversationTopic != "Reclamo" || d.From != `"Anna Ferri" <anna@example.it>` {
		t.Errorf("unexpected header: subject %q, topic %q, from %q", d.Subject, d.ConversationTopic, d.From)
	}
	if len(d.To) != 1 || d.To[0] != `"Bruno" <bruno@example.it>` || len(d.Cc) != 1 || d.Cc[0] != "<copia@example.it>" {
		t.Errorf("unexpected recipients: To %q, Cc %q", d.To, d.Cc)
	}
	if d.Date != sent.Format(time.RFC1123Z) || d.Importance != email.MSImportanceHigh {
		t.Errorf("unexpected date %q or importance %v", d.Date, d.Importance)
	}
	if d.Body != `<p>Vedi<img src="data:application/octet-stream;base64,UE5H"></p>` {
		t.Errorf("unexpected body %q", d.Body)
	}
	if len(d.MSAttachments) != 2 || !d.MSAttachments[0].Hidden || len(d.Attachments) != 1 || d.Attachments[0].Filename != "nota.txt" {
		t.Errorf("expected logo.png hidden and nota.txt listed, got %+v", d.MSAttachments)
	}
}

// Without attMsgProps the attributes stand in: attFrom, attDateSent,
// attPriority and attBody in the container's OEM code page.
func TestMSReadMessageFromAttributes(t *testing.T) {
	var b bytes.Buffer
	b.Write(newMSTNEF().bytes())
	appendAttr(&b, 1, 0x9007, 0x0006, []byte{0xE4, 0x04, 0, 0, 0, 0, 0, 0}) // attOemCodepage 1252
	appendAttr(&b, 1, 0x8004, 0x0001, []byte("Oggetto\x00"))
	name, addr := "Anna Ferri\x00", "SMTP:anna@example.it\x00"
	trp := binary.LittleEndian.AppendUint16(nil, 0x0004)
	trp = binary.LittleEndian.AppendUint16(trp, uint16(8+len(name)+len(addr)))
	trp = binary.LittleEndian.AppendUint16(trp, uint16(len(name)))
	trp = binary.LittleEndian.AppendUint16(trp, uint16(len(addr)))
	trp = append(append(append(trp, name...), addr...), make([]byte, 8)...)
	appendAttr(&b, 1, 0x8000, 0x0000, trp)
	date := []byte{0xEA, 0x07, 1, 0, 29, 0, 10, 0, 27, 0, 0, 0, 4, 0} // 2026-01-29 10:27:00
	appendAttr(&b, 1, 0x8005, 0x0003, date)
	appendAttr(&b, 1, 0x800D, 0x0004, []byte{3, 0}) // low
	appendAttr(&b, 1, 0x800C, 0x0002, []byte("caff\xe8\x00"))

	d, err := MS_ReadMessage(b.Bytes())
	if err != nil {
		t.Fatalf("MS_ReadMessage: %v", err)
	}
	if d.Subject != "Oggetto" || d.From != `"Anna Ferri" <anna@example.it>` || d.Body != "caffè" {
		t.Errorf("unexpected message: subject %q, from %q, body %q", d.Subject, d.From, d.Body)
	}
	if d.Date != time.Date(2026, 1, 29, 10, 27, 0, 0, time.UTC).Format(time.RFC1123Z) || d.Importance != email.MSImportanceLow {
		t.Errorf("unexpected date %q or importance %v", d.Date, d.Importance)
	}
}
