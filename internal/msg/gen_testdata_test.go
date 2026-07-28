package msg

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateTestdata = flag.Bool("update", false, "rewrite the generated .msg fixtures")

// The .msg fixtures are generated rather than derived from real mail: a
// compound file cannot be anonymised by substituting text. This test writes
// them when -update is passed and otherwise checks they are still in sync,
// so a change to the writer cannot silently diverge from what is committed.
func TestGenerateMSGTestdata(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "msg")

	cases := map[string]func() []byte{
		"unicode-props.msg":   fixtureUnicodeProps,
		"string8-props.msg":   fixtureString8Props,
		"with-attachment.msg": fixtureWithAttachment,
		"large-body.msg":      fixtureLargeBody,
	}

	for name, build := range cases {
		path := filepath.Join(dir, name)
		want := build()

		if *updateTestdata {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", dir, err)
			}
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
			continue
		}

		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v (run with -update to generate)", path, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is out of date; rerun with -update", name)
		}
	}
}

// A message with PT_UNICODE properties, the common case.
func fixtureUnicodeProps() []byte {
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagSubject, propTypeString), utf16LE("Preventivo n. 4471"))
	root.addStream(substgName(pidTagSenderName, propTypeString), utf16LE("Anna Ferri"))
	root.addStream(substgName(pidTagSenderEmailAddress, propTypeString), utf16LE("anna.ferri@example.it"))
	root.addStream(substgName(pidTagDisplayTo, propTypeString), utf16LE("ufficio@example.it"))
	root.addStream(substgName(pidTagDisplayCc, propTypeString), utf16LE("copia@example.it; altra@example.it"))
	root.addStream(substgName(pidTagBodyHTML, propTypeString), utf16LE("<html><body>In allegato il preventivo.</body></html>"))
	return buildCFB(root)
}

// PT_STRING8 properties hold single-byte text; accents come back through the
// Windows-1252 fallback in ConvertToUTF8.
func fixtureString8Props() []byte {
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagSubject, propTypeString8), []byte{
		'P', 'e', 'r', 'c', 'h', 0xE9, ' ', 'n', 'o', // "Perché no" in cp1252
	})
	root.addStream(substgName(pidTagBody, propTypeString8), []byte("Testo semplice\r\ncon due righe"))
	return buildCFB(root)
}

// An attachment is a storage under the root, holding its own property streams.
func fixtureWithAttachment() []byte {
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagSubject, propTypeString), utf16LE("Con allegato"))
	root.addStream(substgName(pidTagBodyHTML, propTypeString), utf16LE("<html><body>Vedi allegato.</body></html>"))

	att := root.addStorage("__attach_version1.0_#00000000")
	att.addStream(substgName(pidTagAttachLongFilename, propTypeString), utf16LE("nota.txt"))
	att.addStream(substgName(pidTagAttachMimeTag, propTypeString), utf16LE("text/plain"))
	att.addStream(substgName(pidTagAttachData, propTypeBinary), []byte("contenuto della nota"))
	return buildCFB(root)
}

// A body above the 4096-byte cutoff exercises the regular-sector read path.
func fixtureLargeBody() []byte {
	body := "<html><body>" + strings.Repeat("riga di testo<br>", 400) + "</body></html>"
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagSubject, propTypeString), utf16LE("Corpo lungo"))
	root.addStream(substgName(pidTagBodyHTML, propTypeString8), []byte(body))
	return buildCFB(root)
}
