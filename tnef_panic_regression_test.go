package mailfmt_test

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/ffois/mailfmt"
)

// craftedWinmailDat reproduces, byte-for-byte, the crafted TNEF blob from
// the vulnerability report: the four-byte TNEF signature, a key, and a
// single attAttachTitle (0x8010) attribute with no preceding
// attAttachRendData (0x9002) attribute. The upstream
// github.com/teamwork/tnef decoder (vendored transitively via
// internal/tnef, pinned to a 2020 commit) only allocates its *Attachment
// record when it sees attAttachRendData and dereferences that pointer
// unconditionally for any level-2 attribute that arrives first, so this
// blob panics with a nil-pointer dereference deep inside a third-party
// dependency this repository does not control.
func craftedWinmailDat() []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0x78, 0x9F, 0x3E, 0x22})               // TNEF signature
	binary.Write(&buf, binary.LittleEndian, uint16(0x0001)) // key

	payload := append([]byte("evil.txt"), 0)
	buf.WriteByte(2)                                              // attachment level
	binary.Write(&buf, binary.LittleEndian, uint16(0x8010))       // attAttachTitle
	binary.Write(&buf, binary.LittleEndian, uint16(0x0006))       // type
	binary.Write(&buf, binary.LittleEndian, uint32(len(payload))) // length
	buf.Write(payload)
	var checksum uint16
	for _, b := range payload {
		checksum += uint16(b)
	}
	binary.Write(&buf, binary.LittleEndian, checksum)

	return buf.Bytes()
}

// craftedWinmailEml wraps craftedWinmailDat in an otherwise ordinary
// multipart/mixed .eml, the same shape as testdata/tnef/winmail.eml, so the
// crafted attachment reaches mailfmt.ParseFile the way a real inbound
// message would.
func craftedWinmailEml() []byte {
	encoded := base64.StdEncoding.EncodeToString(craftedWinmailDat())

	var b bytes.Buffer
	b.WriteString("From: mittente@example.it\r\n")
	b.WriteString("To: destinatario@example.it\r\n")
	b.WriteString("Subject: crafted winmail.dat\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: multipart/mixed; boundary=\"----=_crafted_tnef_0001\"\r\n")
	b.WriteString("\r\n")
	b.WriteString("------=_crafted_tnef_0001\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString("body\r\n")
	b.WriteString("------=_crafted_tnef_0001\r\n")
	b.WriteString("Content-Type: application/ms-tnef; name=\"winmail.dat\"\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	b.WriteString("Content-Disposition: attachment; filename=\"winmail.dat\"\r\n")
	b.WriteString("\r\n")
	b.WriteString(encoded)
	b.WriteString("\r\n")
	b.WriteString("------=_crafted_tnef_0001--\r\n")
	return b.Bytes()
}

// TestParseFileDoesNotPanicOnCraftedWinmailDat pins the user-visible defect
// this task fixes: mailfmt.ParseFile must never panic on attacker-supplied
// input, and a crafted winmail.dat attachment inside an otherwise ordinary
// .eml is exactly that. internal/tnef's tests pin the containment
// mechanism (a recover boundary around every call into the upstream
// decoder); this test pins that the mechanism is actually reachable from
// the public API mail arrives through, end to end.
func TestParseFileDoesNotPanicOnCraftedWinmailDat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "crafted.eml")
	if err := os.WriteFile(path, craftedWinmailEml(), 0o644); err != nil {
		t.Fatalf("failed to write fixture: %v", err)
	}

	data, _, err := mailfmt.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile returned an error: %v", err)
	}
	if data == nil {
		t.Fatal("ParseFile returned a nil result with a nil error")
	}

	// The crafted attachment can't be expanded (that's the whole point), so
	// it must survive unchanged rather than vanish silently.
	found := false
	for _, att := range data.Attachments {
		if att.Filename == "winmail.dat" {
			found = true
		}
	}
	if !found {
		t.Error("the crafted winmail.dat attachment was dropped instead of preserved")
	}
}
