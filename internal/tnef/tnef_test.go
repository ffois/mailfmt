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
