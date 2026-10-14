package msg

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// MS_Read reads untrusted files: whatever the bytes, it returns, with a
// result or an error, and does not panic. Read is held to the same.
func FuzzMSRead(f *testing.F) {
	paths, _ := filepath.Glob(filepath.Join("..", "..", "testdata", "msg", "*.msg"))
	for _, path := range paths {
		if b, err := os.ReadFile(path); err == nil {
			f.Add(b)
		}
	}
	// A message with recipients, an attached message and fixed properties,
	// which the fixtures do not have.
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagSubject, propTypeString), utf16LE("Oggetto"))
	recip := root.addStorage("__recip_version1.0_#00000000")
	recip.addStream(substgName(msPidTagEmailAddress, propTypeString), utf16LE("anna@example.it"))
	att := root.addStorage("__attach_version1.0_#00000000")
	att.addStream(substgName(msPidTagDisplayName, propTypeString), utf16LE("Interno"))
	att.addStorage("__substg1.0_3701000D").addStream(substgName(pidTagSubject, propTypeString), utf16LE("Interno"))
	f.Add(buildCFB(outlookMSG(root, msgFixed{root: {msTagImportance: 2, msTagMessageFlags: 0x19}})))

	f.Fuzz(func(t *testing.T, raw []byte) {
		if d, err := MS_Read(bytes.NewReader(raw)); err == nil && d == nil {
			t.Fatal("no result and no error")
		}
		_, _ = Read(bytes.NewReader(raw))
	})
}
