package tnef

import (
	"bytes"
	"encoding/binary"
	"testing"
	"unicode/utf16"

	"github.com/ffois/mailfmt/internal/email"
)

// msProp is one MAPI property for msPropBlock: full tag and raw value.
type msProp struct {
	tag   uint32
	value []byte
}

// msPropList encodes properties the way a TNEF property block holds them:
// tag, then for variable-length types a value count and a length, every
// value padded to four bytes.
func msPropList(props ...msProp) []byte {
	var b bytes.Buffer
	for _, p := range props {
		binary.Write(&b, binary.LittleEndian, p.tag)
		switch p.tag & 0xFFFF {
		case 0x001E, 0x001F, 0x0102, 0x000D:
			binary.Write(&b, binary.LittleEndian, uint32(1))
			binary.Write(&b, binary.LittleEndian, uint32(len(p.value)))
		}
		b.Write(p.value)
		b.Write(make([]byte, padTo4(len(p.value))))
	}
	return b.Bytes()
}

// msPropBlock is a counted property list (attMsgProps, attAttachment).
func msPropBlock(props ...msProp) []byte {
	out := binary.LittleEndian.AppendUint32(nil, uint32(len(props)))
	return append(out, msPropList(props...)...)
}

func msUnicode(s string) []byte {
	var b []byte
	for _, u := range utf16.Encode([]rune(s + "\x00")) {
		b = binary.LittleEndian.AppendUint16(b, u)
	}
	return b
}

func msLong(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

// msTNEF assembles a container from message-level and attachment records.
type msTNEF struct{ buf bytes.Buffer }

func newMSTNEF() *msTNEF {
	t := &msTNEF{}
	t.buf.Write(tnefMagic)
	binary.Write(&t.buf, binary.LittleEndian, uint16(0x0001))
	return t
}

func (t *msTNEF) msgProps(props ...msProp) *msTNEF {
	appendAttr(&t.buf, 1, 0x9003, 0x0006, msPropBlock(props...))
	return t
}

func (t *msTNEF) recipients(rows ...[]msProp) *msTNEF {
	out := binary.LittleEndian.AppendUint32(nil, uint32(len(rows)))
	for _, r := range rows {
		out = append(out, msPropBlock(r...)...)
	}
	appendAttr(&t.buf, 1, 0x9004, 0x0006, out)
	return t
}

func (t *msTNEF) attachment(title string, data []byte, props ...msProp) *msTNEF {
	appendAttr(&t.buf, 2, 0x9002, 0x0006, make([]byte, 14))
	if title != "" {
		appendAttr(&t.buf, 2, 0x8010, 0x0002, append([]byte(title), 0))
	}
	if data != nil {
		appendAttr(&t.buf, 2, 0x800F, 0x0006, data)
	}
	if len(props) > 0 {
		appendAttr(&t.buf, 2, 0x9005, 0x0006, msPropBlock(props...))
	}
	return t
}

func (t *msTNEF) bytes() []byte { return t.buf.Bytes() }

func winmail(blob []byte) email.MSAttachment {
	return email.MSAttachment{Attachment: email.Attachment{
		Filename: "winmail.dat", ContentType: "application/ms-tnef", Data: blob,
	}}
}

func names(atts []email.MSAttachment) map[string]bool {
	got := map[string]bool{}
	for _, a := range atts {
		got[a.Filename] = true
	}
	return got
}

// Outlook recognises a container by its declared type or its name only. A
// TNEF blob under the vnd.ms-tnef alias and another name is an ordinary file
// to it, even though Expand would open it.
func TestMSExpandIgnoresMagicAndAlias(t *testing.T) {
	blob := newMSTNEF().attachment("nota.txt", []byte("appunti")).bytes()
	original := []email.MSAttachment{{Attachment: email.Attachment{
		Filename: "dati.bin", ContentType: "application/vnd.ms-tnef", Data: blob,
	}}}

	got, decoded := MS_Expand(original, "")

	if decoded != nil || len(got) != 1 || got[0].Filename != "dati.bin" {
		t.Errorf("expected the container to be left alone, got %v", names(got))
	}
}

// The declared type is matched without its parameters and without regard to
// case, the filename without regard to case.
func TestMSExpandRecognisesTypeOrName(t *testing.T) {
	blob := newMSTNEF().attachment("nota.txt", []byte("appunti")).bytes()
	cases := []email.Attachment{
		{Filename: "x.bin", ContentType: "Application/MS-TNEF; name=x.bin", Data: blob},
		{Filename: "WINMAIL.DAT", ContentType: "application/octet-stream", Data: blob},
	}
	for _, att := range cases {
		got, _ := MS_Expand([]email.MSAttachment{{Attachment: att}}, "")
		if n := names(got); !n["nota.txt"] || n[att.Filename] {
			t.Errorf("%s / %s: expected the container replaced by its file, got %v",
				att.Filename, att.ContentType, n)
		}
	}
}

// With a correlator on the message, only a container carrying the same key is
// decoded; the comparison ignores case. A different key, or none at all,
// leaves winmail.dat as a plain attachment.
func TestMSExpandChecksCorrelator(t *testing.T) {
	withKey := newMSTNEF().
		msgProps(msProp{0x007F0102, []byte("<00000000ABCDEF@example.it>\x00")}).
		attachment("relazione.pdf", []byte("%PDF finto")).bytes()
	withoutKey := newMSTNEF().attachment("relazione.pdf", []byte("%PDF finto")).bytes()

	cases := []struct {
		name       string
		blob       []byte
		correlator string
		expanded   bool
	}{
		{"matching key", withKey, "<00000000abcdef@EXAMPLE.it>", true},
		{"different key", withKey, "<altro@example.it>", false},
		{"no key in the container", withoutKey, "<00000000ABCDEF@example.it>", false},
		{"no correlator on the message", withoutKey, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := MS_Expand([]email.MSAttachment{winmail(tc.blob)}, tc.correlator)
			n := names(got)
			if n["relazione.pdf"] != tc.expanded || n["winmail.dat"] == tc.expanded {
				t.Errorf("expected expanded=%v, got %v", tc.expanded, n)
			}
		})
	}
}

// Outlook keeps a single container to decode, the last one it meets; an
// earlier winmail.dat stays in the list as it was.
func TestMSExpandDecodesOnlyLastContainer(t *testing.T) {
	first := newMSTNEF().attachment("primo.txt", []byte("1")).bytes()
	last := newMSTNEF().attachment("ultimo.txt", []byte("2")).bytes()

	got, _ := MS_Expand([]email.MSAttachment{winmail(first), winmail(last)}, "")

	if len(got) != 2 {
		t.Fatalf("expected the first container and the last one's file, got %v", names(got))
	}
	if got[0].Filename != "winmail.dat" || !bytes.Equal(got[0].Data, first) {
		t.Errorf("the first container was modified: %+v", got[0])
	}
	if got[1].Filename != "ultimo.txt" {
		t.Errorf("expected the last container's file, got %q", got[1].Filename)
	}
}

// A container that decodes but holds no files is still removed: Outlook
// deletes winmail.dat once decoding succeeds, whatever it found inside.
func TestMSExpandRemovesEmptyContainer(t *testing.T) {
	got, decoded := MS_Expand([]email.MSAttachment{winmail(newMSTNEF().bytes())}, "")

	if decoded == nil || len(got) != 0 {
		t.Errorf("expected the empty container to be removed, got %v", names(got))
	}
}

// A container that fails to decode is kept.
func TestMSExpandKeepsUndecodableContainer(t *testing.T) {
	got, decoded := MS_Expand([]email.MSAttachment{winmail([]byte("non è un tnef"))}, "")

	if decoded != nil || len(got) != 1 || string(got[0].Data) != "non è un tnef" {
		t.Errorf("expected the container to survive untouched, got %+v", got)
	}
}

// An attached message stays one attachment, named by its display name, with
// its own TNEF stream as data; its contents are not spilled into the list.
func TestMSDecodeKeepsEmbeddedMessage(t *testing.T) {
	inner := newMSTNEF().attachment("dentro.pdf", []byte("%PDF interno")).bytes()
	obj := append(make([]byte, 16), inner...) // IID_IMessage placeholder, then the stream

	blob := newMSTNEF().attachment("", []byte("L'allegato è un messaggio incorporato MAPI 1.0"),
		msProp{0x37050003, msLong(5)},
		msProp{0x3001001F, msUnicode("Re: Preventivo")},
		msProp{0x3701000D, obj},
	).bytes()

	d, err := MS_Decode(blob)
	if err != nil {
		t.Fatalf("MS_Decode: %v", err)
	}
	if len(d.Attachments) != 1 {
		t.Fatalf("expected one attachment, got %d: %+v", len(d.Attachments), d.Attachments)
	}
	got := d.Attachments[0]
	if !got.Embedded || got.Filename != "Re: Preventivo" || !bytes.Equal(got.Data, inner) {
		t.Errorf("unexpected embedded attachment: %+v", got)
	}
}

// The MAPI long filename and MIME tag win over the short title; a record
// without any name is Untitled Attachment, as OLMAPI32 names it.
func TestMSDecodeAttachmentNames(t *testing.T) {
	blob := newMSTNEF().
		attachment("RELAZI~1.PDF", []byte("%PDF"),
			msProp{0x3707001F, msUnicode("relazione annuale.pdf")},
			msProp{0x370E001F, msUnicode("application/pdf")}).
		attachment("", []byte("dati")).
		bytes()

	d, err := MS_Decode(blob)
	if err != nil {
		t.Fatalf("MS_Decode: %v", err)
	}
	if len(d.Attachments) != 2 {
		t.Fatalf("expected two attachments, got %+v", d.Attachments)
	}
	if a := d.Attachments[0]; a.Filename != "relazione annuale.pdf" || a.ContentType != "application/pdf" {
		t.Errorf("unexpected first attachment: %+v", a)
	}
	if a := d.Attachments[1]; a.Filename != "Untitled Attachment" {
		t.Errorf("expected Untitled Attachment, got %q", a.Filename)
	}
}

// The message-level properties Outlook copies: the compressed RTF body, the
// message class and, for reports, the recipient table.
func TestMSDecodeMessageProperties(t *testing.T) {
	blob := newMSTNEF().
		msgProps(
			msProp{0x10090102, []byte{1, 2, 3, 4}},
			msProp{0x001A001E, []byte("REPORT.IPM.Note.NDR\x00")}).
		recipients(
			[]msProp{{0x3001001F, msUnicode("Anna Ferri")}, {0x3003001F, msUnicode("anna@example.it")}, {0x0C150003, msLong(1)}},
			[]msProp{{0x3001001E, []byte("Ufficio\x00")}, {0x0C150003, msLong(2)}}).
		bytes()

	d, err := MS_Decode(blob)
	if err != nil {
		t.Fatalf("MS_Decode: %v", err)
	}
	if !bytes.Equal(d.RTFCompressed, []byte{1, 2, 3, 4}) {
		t.Errorf("unexpected RTF: %v", d.RTFCompressed)
	}
	if d.MessageClass != "REPORT.IPM.Note.NDR" {
		t.Errorf("unexpected message class %q", d.MessageClass)
	}
	want := []MSRecipient{{"Anna Ferri", "anna@example.it", 1}, {"Ufficio", "", 2}}
	if len(d.Recipients) != 2 || d.Recipients[0] != want[0] || d.Recipients[1] != want[1] {
		t.Errorf("expected %+v, got %+v", want, d.Recipients)
	}
}

// Attachment attributes before any attAttachRendData belong to nothing: the
// upstream decoder panics on them, MS_Decode ignores them.
func TestMSDecodeIgnoresOrphanAttributes(t *testing.T) {
	d, err := MS_Decode(craftedTNEF())
	if err != nil {
		t.Fatalf("expected the crafted blob to decode, got %v", err)
	}
	if len(d.Attachments) != 0 {
		t.Errorf("expected no attachments, got %+v", d.Attachments)
	}
}

// Truncated and lying lengths end the walk instead of reading past the data.
func TestMSDecodeSurvivesTruncation(t *testing.T) {
	blob := newMSTNEF().
		msgProps(msProp{0x10090102, bytes.Repeat([]byte{7}, 64)}).
		attachment("a.txt", []byte("contenuto"), msProp{0x3707001F, msUnicode("lungo.txt")}).
		bytes()
	for n := 0; n < len(blob); n++ {
		if _, err := MS_Decode(blob[:n]); err != nil && n >= 6 {
			t.Fatalf("prefix of %d bytes: %v", n, err)
		}
	}
	lying := append([]byte(nil), msPropBlock(msProp{0x3707001F, msUnicode("x")})...)
	binary.LittleEndian.PutUint32(lying[12:], 0x7FFFFFFF) // length of the value
	if got := msScanProps(lying); len(got) != 0 {
		t.Errorf("expected nothing from a lying length, got %v", got)
	}
}
