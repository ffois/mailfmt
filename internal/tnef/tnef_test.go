package tnef

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/ffois/mailfmt/internal/email"
)

// buildTNEF assembles a minimal winmail.dat holding the given files. The
// format is a signature, a key, then a sequence of attributes; attachments
// are an attAttachRendData (which allocates the attachment record inside the
// upstream github.com/teamwork/tnef decoder), followed by an AttachTitle and
// an AttachData at the attachment level.
//
// The attribute layout (level, name/id, type, length, data, checksum) and
// the specific attribute IDs/types below are taken from decoding
// testdata/tnef/winmail.eml's embedded winmail.dat byte-for-byte: that
// fixture is a synthetic blob from an earlier task that is known to
// round-trip through the real library, so its encoding is the reference.
//
// Critically, the upstream decoder's Decode() only allocates a new
// *tnef.Attachment when it sees attAttachRendData (0x9002); any level-2
// attribute (attAttachTitle 0x8010 or attAttachData 0x800F) seen before that
// is applied to a nil *Attachment and panics. The brief's original
// buildTNEF omitted attAttachRendData, which is why it needed adjusting.
func buildTNEF(files map[string][]byte) []byte {
	var buf bytes.Buffer
	buf.Write(tnefMagic)
	binary.Write(&buf, binary.LittleEndian, uint16(0x0001)) // key

	writeAttr := func(level byte, id uint16, typ uint16, data []byte) {
		buf.WriteByte(level)
		binary.Write(&buf, binary.LittleEndian, id)
		binary.Write(&buf, binary.LittleEndian, typ)
		binary.Write(&buf, binary.LittleEndian, uint32(len(data)))
		buf.Write(data)
		var sum uint16
		for _, b := range data {
			sum += uint16(b)
		}
		binary.Write(&buf, binary.LittleEndian, sum)
	}

	for name, content := range files {
		writeAttr(2, 0x9002, 0x0006, make([]byte, 14))        // attAttachRendData: allocates the attachment
		writeAttr(2, 0x8010, 0x0002, append([]byte(name), 0)) // attAttachTitle
		writeAttr(2, 0x800F, 0x0006, content)                 // attAttachData
	}
	return buf.Bytes()
}

// The signature is what distinguishes a real winmail.dat from a file that
// merely has the name. If this ever regressed, filenames alone would decide
// what gets treated as a TNEF container, defeating the point of sniffing.
func TestIsTNEFData(t *testing.T) {
	if !IsTNEFData(buildTNEF(map[string][]byte{"a.txt": []byte("x")})) {
		t.Error("a generated TNEF blob was not recognised")
	}
	if IsTNEFData([]byte("From: a@example.com\r\n\r\nnope")) {
		t.Error("a plain message was recognised as TNEF")
	}
	if IsTNEFData(nil) {
		t.Error("nil was recognised as TNEF")
	}
	if IsTNEFData([]byte{0x78}) {
		t.Error("a one-byte input was recognised as TNEF")
	}
}

// A winmail.dat is a container: callers want the files inside it, not the
// container, so Expand replaces it with its contents. If this broke, callers
// would end up presenting an opaque, unusable winmail.dat to end users
// instead of the real attachments Outlook packed inside it.
func TestExpandReplacesContainer(t *testing.T) {
	blob := buildTNEF(map[string][]byte{
		"relazione.pdf": []byte("%PDF-1.4 finto"),
		"nota.txt":      []byte("appunti"),
	})

	got := Expand([]email.Attachment{
		{Filename: "documento.pdf", ContentType: "application/pdf", Data: []byte("%PDF altro")},
		{Filename: "winmail.dat", ContentType: "application/ms-tnef", Data: blob},
	})

	names := map[string]bool{}
	for _, a := range got {
		names[a.Filename] = true
	}
	if names["winmail.dat"] {
		t.Error("the winmail.dat container was left in the attachment list")
	}
	if !names["documento.pdf"] {
		t.Error("the unrelated attachment was dropped")
	}
	if !names["relazione.pdf"] || !names["nota.txt"] {
		t.Errorf("expected both extracted files, got %v", names)
	}
}

// An attachment named winmail.dat without the TNEF signature is just a file
// with an unlucky name and has to survive untouched; otherwise Expand would
// corrupt or drop a legitimate attachment purely because of its filename.
func TestExpandLeavesNonTNEFAlone(t *testing.T) {
	original := []email.Attachment{
		{Filename: "winmail.dat", ContentType: "application/octet-stream", Data: []byte("solo testo")},
	}

	got := Expand(original)

	if len(got) != 1 {
		t.Fatalf("expected the attachment to survive, got %d", len(got))
	}
	if got[0].Filename != "winmail.dat" || string(got[0].Data) != "solo testo" {
		t.Errorf("attachment was modified: %+v", got[0])
	}
}

// Extract on garbage must return an error rather than partial results, so
// callers can't be handed a nil or half-decoded attachment slice as if it
// were a genuine extraction.
func TestExtractRejectsGarbage(t *testing.T) {
	if _, err := Extract([]byte("non è un tnef")); err == nil {
		t.Fatal("expected an error for non-TNEF input, got nil")
	}
}

// appendAttr writes one TNEF attribute (level, id, type, data, checksum) to
// buf. It is the same layout as buildTNEF's private writeAttr closure above,
// duplicated here (rather than factored out of buildTNEF) so the existing
// tests and their helper stay untouched.
func appendAttr(buf *bytes.Buffer, level byte, id uint16, typ uint16, data []byte) {
	buf.WriteByte(level)
	binary.Write(buf, binary.LittleEndian, id)
	binary.Write(buf, binary.LittleEndian, typ)
	binary.Write(buf, binary.LittleEndian, uint32(len(data)))
	buf.Write(data)
	var sum uint16
	for _, b := range data {
		sum += uint16(b)
	}
	binary.Write(buf, binary.LittleEndian, sum)
}

// craftedTNEF reproduces, byte-for-byte, the crafted blob from the
// vulnerability report: a valid TNEF signature and key, followed by a
// single attAttachTitle (0x8010) attribute with no preceding
// attAttachRendData. The upstream decoder only allocates its *Attachment
// record on attAttachRendData (0x9002) and dereferences that pointer for
// any level-2 attribute that arrives first (see decodeTNEF's doc comment in
// tnef.go), so decoding this blob panics inside the upstream library.
func craftedTNEF() []byte {
	var buf bytes.Buffer
	buf.Write(tnefMagic)
	binary.Write(&buf, binary.LittleEndian, uint16(0x0001)) // key
	appendAttr(&buf, 2, 0x8010, 0x0006, append([]byte("evil.txt"), 0))
	return buf.Bytes()
}

// buildNestedCraftedTNEF wraps craftedTNEF inside a second, outer TNEF
// container so it is reachable only through extractTNEFRecursive's own
// recursive call, not through the top-level Extract entry point directly.
// The outer container is a normal, valid TNEF attachment (attAttachRendData
// then Title then Data, so the upstream decoder handles it fine) plus one
// attAttachment (0x9005) attribute whose value is a MAPI properties block
// exposing PR_ATTACH_DATA_OBJ (0x3701): a 16-byte GUID placeholder followed
// by the crafted inner TNEF stream. That is exactly the shape
// findEmbeddedTNEFStreamsFromRaw looks for when it digs into embedded MAPI
// messages, and mirrors how Outlook nests a forwarded message inside
// another.
func buildNestedCraftedTNEF() []byte {
	inner := craftedTNEF()

	// PR_ATTACH_DATA_OBJ's value, per findEmbeddedTNEFStreamsFromRaw's
	// `afterGuid := embedded[16:]`: a 16-byte GUID placeholder + the nested
	// TNEF stream.
	embeddedValue := append(make([]byte, 16), inner...)

	var mapi bytes.Buffer
	binary.Write(&mapi, binary.LittleEndian, uint32(1)) // property count
	propTag := uint32(0x3701)<<16 | uint32(0x000D)      // PR_ATTACH_DATA_OBJ, PT_OBJECT
	binary.Write(&mapi, binary.LittleEndian, propTag)
	binary.Write(&mapi, binary.LittleEndian, uint32(1)) // PT_OBJECT value count
	binary.Write(&mapi, binary.LittleEndian, uint32(len(embeddedValue)))
	mapi.Write(embeddedValue)
	// extractPRAttachDataObjDirect returns as soon as it reads the
	// PR_ATTACH_DATA_OBJ value above, so this padding is never parsed; it
	// only exists to push the attAttachment attribute's length past
	// findEmbeddedTNEFStreamsFromRaw's `attrLen > 100` threshold.
	mapi.Write(make([]byte, 64))

	var buf bytes.Buffer
	buf.Write(tnefMagic)
	binary.Write(&buf, binary.LittleEndian, uint16(0x0001))             // key
	appendAttr(&buf, 2, 0x9002, 0x0006, make([]byte, 14))               // attAttachRendData
	appendAttr(&buf, 2, 0x8010, 0x0002, append([]byte("outer.txt"), 0)) // attAttachTitle
	appendAttr(&buf, 2, 0x800F, 0x0006, []byte("outer content"))        // attAttachData
	appendAttr(&buf, 2, 0x9005, 0x0006, mapi.Bytes())                   // attAttachment (embeds crafted inner TNEF)
	return buf.Bytes()
}

// A crafted TNEF whose first level-2 attribute isn't attAttachRendData used
// to reach the upstream decoder's nil-pointer dereference directly. Extract
// must convert that panic into an ordinary error: a mail-parsing library
// that panics on attacker-controlled bytes takes its caller's process down
// with it, and every winmail.dat this package sees is attacker-controlled
// by default.
func TestExtractRecoversFromUpstreamPanic(t *testing.T) {
	_, err := Extract(craftedTNEF())
	if err == nil {
		t.Fatal("expected an error from the crafted blob, got nil")
	}
}

// Expand must neither let the crafted-blob panic escape nor silently drop
// the attachment: losing it without a trace would be a worse outcome than
// the panic it replaces, because the caller would never know something was
// there. A crafted winmail.dat must be preserved exactly like a
// non-TNEF-despite-the-name attachment already is (TestExpandLeavesNonTNEFAlone).
func TestExpandPreservesCraftedTNEFContainer(t *testing.T) {
	blob := craftedTNEF()
	original := []email.Attachment{
		{Filename: "winmail.dat", ContentType: "application/ms-tnef", Data: blob},
	}

	got := Expand(original)

	if len(got) != 1 {
		t.Fatalf("expected the crafted attachment to survive untouched, got %d entries: %+v", len(got), got)
	}
	if got[0].Filename != "winmail.dat" || !bytes.Equal(got[0].Data, blob) {
		t.Errorf("crafted attachment was modified: %+v", got[0])
	}
}

// extractTNEFRecursive re-enters the upstream decoder for embedded MAPI
// messages found via findEmbeddedTNEFStreamsFromRaw, and the recover
// boundary lives inside that same function. This proves the boundary also
// covers the recursive call, not just the top-level Extract entry: a nested
// crafted container must be contained exactly as an outer one is, and the
// still-valid outer attachment must survive alongside the containment.
func TestExtractRecoversFromNestedUpstreamPanic(t *testing.T) {
	attachments, err := Extract(buildNestedCraftedTNEF())
	if err != nil {
		t.Fatalf("the outer container is valid TNEF and should still extract, got error: %v", err)
	}
	if len(attachments) != 1 || attachments[0].Filename != "outer.txt" {
		t.Fatalf("expected only the outer attachment to survive, got %+v", attachments)
	}
}
