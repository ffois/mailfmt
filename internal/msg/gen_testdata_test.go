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
// compound file cannot be anonymised by substituting text. outlookMSG makes
// each one a message Outlook 2010 opens. This test writes them when -update
// is passed and otherwise checks they are still in sync, so a change to the
// writer cannot silently diverge from what is committed.
func TestGenerateMSGTestdata(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "msg")

	cases := map[string]func() []byte{
		"unicode-props.msg":        fixtureUnicodeProps,
		"string8-props.msg":        fixtureString8Props,
		"with-attachment.msg":      fixtureWithAttachment,
		"large-body.msg":           fixtureLargeBody,
		"multipart-attachment.msg": fixtureMultipartAttachment,
		"attached-message.msg":     fixtureAttachedMessage,
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
	root.addStream(substgName(pidTagBodyHTML, propTypeBinary), []byte("<html><body>In allegato il preventivo.</body></html>"))
	return buildCFB(outlookMSG(root, nil))
}

// PT_STRING8 properties hold single-byte text; accents come back through the
// Windows-1252 fallback in ConvertToUTF8.
func fixtureString8Props() []byte {
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagSubject, propTypeString8), []byte{
		'P', 'e', 'r', 'c', 'h', 0xE9, ' ', 'n', 'o', // "Perché no" in cp1252
	})
	root.addStream(substgName(pidTagBody, propTypeString8), []byte("Testo semplice\r\ncon due righe"))
	return buildCFB(outlookMSG(root, nil))
}

// An attachment is a storage under the root, holding its own property streams.
func fixtureWithAttachment() []byte {
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagSubject, propTypeString), utf16LE("Con allegato"))
	root.addStream(substgName(pidTagBodyHTML, propTypeBinary), []byte("<html><body>Vedi allegato.</body></html>"))

	att := root.addStorage("__attach_version1.0_#00000000")
	att.addStream(substgName(pidTagAttachLongFilename, propTypeString), utf16LE("nota.txt"))
	att.addStream(substgName(pidTagAttachMimeTag, propTypeString), utf16LE("text/plain"))
	att.addStream(substgName(pidTagAttachData, propTypeBinary), []byte("contenuto della nota"))
	return buildCFB(outlookMSG(root, nil))
}

// An attachment can be a whole MIME tree rather than a single file: Outlook
// stores a certified-mail envelope this way, as one attachment whose MIME tag
// is multipart/mixed and whose data is the raw envelope. The reader then has to
// walk that tree itself - descend the parts, pull the message/rfc822 payload
// out, and walk that in turn - which is the only path that sets HasInnerEmail
// for a .msg.
func fixtureMultipartAttachment() []byte {
	inner := "From: Studio Medico <studio@example.it>\r\n" +
		"To: paziente@example.it\r\n" +
		"Subject: Referto\r\n" +
		"Content-Type: multipart/mixed; boundary=\"interno\"\r\n" +
		"\r\n" +
		"--interno\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"In allegato il referto.\r\n" +
		"--interno\r\n" +
		"Content-Type: application/pdf; name=\"referto.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"JVBERi0xLjQK\r\n" +
		"--interno--\r\n"

	envelope := "Content-Type: multipart/mixed; boundary=\"esterno\"\r\n" +
		"\r\n" +
		"--esterno\r\n" +
		"Content-Type: message/rfc822\r\n" +
		"Content-Disposition: attachment; filename=\"postacert.eml\"\r\n" +
		"\r\n" +
		inner +
		"--esterno\r\n" +
		"Content-Type: application/pkcs7-signature; name=\"smime.p7s\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"AAECAwQ=\r\n" +
		"--esterno--\r\n"

	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagSubject, propTypeString), utf16LE("POSTA CERTIFICATA: Referto"))
	root.addStream(substgName(pidTagBodyHTML, propTypeBinary), []byte("<html><body>Messaggio certificato.</body></html>"))

	att := root.addStorage("__attach_version1.0_#00000000")
	att.addStream(substgName(pidTagAttachLongFilename, propTypeString), utf16LE("envelope.eml"))
	att.addStream(substgName(pidTagAttachMimeTag, propTypeString), utf16LE("multipart/mixed; boundary=\"esterno\""))
	att.addStream(substgName(pidTagAttachData, propTypeBinary), []byte(envelope))
	return buildCFB(outlookMSG(root, nil))
}

// A body above the 4096-byte cutoff exercises the regular-sector read path.
func fixtureLargeBody() []byte {
	body := "<html><body>" + strings.Repeat("riga di testo<br>", 400) + "</body></html>"
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagSubject, propTypeString), utf16LE("Corpo lungo"))
	root.addStream(substgName(pidTagBodyHTML, propTypeBinary), []byte(body))
	return buildCFB(outlookMSG(root, nil))
}

// An attached message is a storage inside the attachment's storage, a whole
// message with its own recipients and attachments: a forwarded mail with the
// original attached, as Outlook stores it.
func fixtureAttachedMessage() []byte {
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagSubject, propTypeString), utf16LE("I: Preventivo n. 4471"))
	root.addStream(substgName(pidTagSenderName, propTypeString), utf16LE("Ufficio Acquisti"))
	root.addStream(substgName(pidTagSenderEmailAddress, propTypeString), utf16LE("acquisti@example.it"))
	root.addStream(substgName(pidTagBodyHTML, propTypeBinary), []byte("<html><body>Inoltro il preventivo ricevuto.</body></html>"))
	r := root.addStorage("__recip_version1.0_#00000000")
	r.addStream(substgName(msPidTagDisplayName, propTypeString), utf16LE("Direzione"))
	r.addStream(substgName(msPidTagEmailAddress, propTypeString), utf16LE("direzione@example.it"))

	att := root.addStorage("__attach_version1.0_#00000000")
	att.addStream(substgName(msPidTagDisplayName, propTypeString), utf16LE("Preventivo n. 4471"))
	inner := att.addStorage("__substg1.0_3701000D")
	inner.addStream(substgName(pidTagSubject, propTypeString), utf16LE("Preventivo n. 4471"))
	inner.addStream(substgName(pidTagSenderName, propTypeString), utf16LE("Anna Ferri"))
	inner.addStream(substgName(pidTagSenderEmailAddress, propTypeString), utf16LE("anna.ferri@example.it"))
	inner.addStream(substgName(pidTagBody, propTypeString), utf16LE("In allegato il preventivo.\r\nCordiali saluti"))
	ir := inner.addStorage("__recip_version1.0_#00000000")
	ir.addStream(substgName(msPidTagDisplayName, propTypeString), utf16LE("Ufficio Acquisti"))
	ir.addStream(substgName(msPidTagEmailAddress, propTypeString), utf16LE("acquisti@example.it"))
	ia := inner.addStorage("__attach_version1.0_#00000000")
	ia.addStream(substgName(pidTagAttachLongFilename, propTypeString), utf16LE("preventivo.pdf"))
	ia.addStream(substgName(pidTagAttachMimeTag, propTypeString), utf16LE("application/pdf"))
	ia.addStream(substgName(pidTagAttachData, propTypeBinary), []byte("%PDF-1.4 FAKE\n%%EOF"))
	return buildCFB(outlookMSG(root, nil))
}
