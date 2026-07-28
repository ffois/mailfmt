package msg

import (
	"bytes"
	"maps"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ffois/mailfmt/internal/email"
)

// readFixture builds a message and parses it, failing the test on error.
func readFixture(t *testing.T, root *cfbEntry) *email.Data {
	t.Helper()
	data, err := Read(bytes.NewReader(buildCFB(root)))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return data
}

// PT_UNICODE properties are UTF-16LE; recipients arrive as one
// semicolon-separated string and have to be split.
func TestReadUnicodeProperties(t *testing.T) {
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagSubject, propTypeString), utf16LE("Preventivo"))
	root.addStream(substgName(pidTagSenderName, propTypeString), utf16LE("Anna Ferri"))
	root.addStream(substgName(pidTagSenderEmailAddress, propTypeString), utf16LE("anna@example.it"))
	root.addStream(substgName(pidTagDisplayTo, propTypeString), utf16LE("uno@example.it; due@example.it"))

	data := readFixture(t, root)

	if data.Subject != "Preventivo" {
		t.Errorf("expected subject %q, got %q", "Preventivo", data.Subject)
	}
	if data.From != "Anna Ferri <anna@example.it>" {
		t.Errorf("expected From %q, got %q", "Anna Ferri <anna@example.it>", data.From)
	}
	if len(data.To) != 2 {
		t.Fatalf("expected 2 recipients, got %d: %v", len(data.To), data.To)
	}
}

// PT_STRING8 properties hold single-byte text. Outlook writes them in the
// sender's ANSI code page, so a byte above 0x7F has to come back as UTF-8.
func TestReadString8Properties(t *testing.T) {
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagSubject, propTypeString8),
		[]byte{'P', 'e', 'r', 'c', 'h', 0xE9})

	data := readFixture(t, root)

	if !utf8.ValidString(data.Subject) {
		t.Fatalf("subject is not valid UTF-8: %q", data.Subject)
	}
	if data.Subject != "Perché" {
		t.Errorf("expected subject %q, got %q", "Perché", data.Subject)
	}
}

// With no subject of its own, a message falls back to the conversation topic.
func TestReadFallsBackToConversationTopic(t *testing.T) {
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagConversationTopic, propTypeString), utf16LE("Discussione"))

	if got := readFixture(t, root).Subject; got != "Discussione" {
		t.Errorf("expected subject %q, got %q", "Discussione", got)
	}
}

// With no HTML body, the plain-text body is escaped and its newlines turned
// into <br>, so callers always get HTML.
func TestReadConvertsTextBodyToHTML(t *testing.T) {
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagBody, propTypeString), utf16LE("prima riga\r\nsecondo <b>"))

	body := readFixture(t, root).Body

	if !strings.Contains(body, "<br>") {
		t.Errorf("expected newlines to become <br>, got %q", body)
	}
	if !strings.Contains(body, "&lt;b&gt;") {
		t.Errorf("expected the literal tag to be escaped, got %q", body)
	}
}

// An attachment lives in its own storage under the root, with its filename,
// MIME type and data in separate property streams.
func TestReadAttachment(t *testing.T) {
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagSubject, propTypeString), utf16LE("Con allegato"))
	att := root.addStorage("__attach_version1.0_#00000000")
	att.addStream(substgName(pidTagAttachLongFilename, propTypeString), utf16LE("nota.txt"))
	att.addStream(substgName(pidTagAttachMimeTag, propTypeString), utf16LE("text/plain"))
	att.addStream(substgName(pidTagAttachData, propTypeBinary), []byte("contenuto"))

	data := readFixture(t, root)

	if len(data.Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(data.Attachments))
	}
	got := data.Attachments[0]
	if got.Filename != "nota.txt" {
		t.Errorf("expected filename %q, got %q", "nota.txt", got.Filename)
	}
	if got.ContentType != "text/plain" {
		t.Errorf("expected content type %q, got %q", "text/plain", got.ContentType)
	}
	if string(got.Data) != "contenuto" {
		t.Errorf("expected data %q, got %q", "contenuto", got.Data)
	}
}

// The long filename wins over the short one when both are present.
func TestReadAttachmentPrefersLongFilename(t *testing.T) {
	root := &cfbEntry{name: "Root Entry"}
	att := root.addStorage("__attach_version1.0_#00000000")
	att.addStream(substgName(pidTagAttachFilename, propTypeString), utf16LE("NOTA~1.TXT"))
	att.addStream(substgName(pidTagAttachLongFilename, propTypeString), utf16LE("nota lunga.txt"))
	att.addStream(substgName(pidTagAttachData, propTypeBinary), []byte("x"))

	data := readFixture(t, root)

	if len(data.Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(data.Attachments))
	}
	if data.Attachments[0].Filename != "nota lunga.txt" {
		t.Errorf("expected the long filename, got %q", data.Attachments[0].Filename)
	}
}

// An attachment whose MIME tag is multipart/* is not a file but a MIME tree,
// and the reader has to walk it: the parts come out as attachments in their
// own right, a message/rfc822 part is kept whole and also descended into, and
// base64 payloads are decoded on the way. This is how a certified-mail envelope
// arrives inside a .msg, and the only path that sets HasInnerEmail for one.
func TestReadMultipartAttachment(t *testing.T) {
	data, err := Read(bytes.NewReader(fixtureMultipartAttachment()))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if !data.HasInnerEmail {
		t.Error("expected HasInnerEmail to be set for an envelope attachment")
	}

	byName := map[string]email.Attachment{}
	for _, att := range data.Attachments {
		byName[att.Filename] = att
	}

	// The envelope itself is replaced by its contents, not kept alongside them.
	if _, ok := byName["envelope.eml"]; ok {
		t.Error("the multipart container should not survive as an attachment of its own")
	}

	eml, ok := byName["postacert.eml"]
	if !ok {
		t.Fatalf("expected the inner message among %v", slices.Sorted(maps.Keys(byName)))
	}
	if eml.ContentType != "message/rfc822" {
		t.Errorf("inner message: expected content type message/rfc822, got %q", eml.ContentType)
	}
	if !bytes.Contains(eml.Data, []byte("Subject: Referto")) {
		t.Error("inner message: expected the raw rfc822 payload to be kept whole")
	}

	pdf, ok := byName["referto.pdf"]
	if !ok {
		t.Fatalf("expected the attachment nested inside the inner message, got %v", slices.Sorted(maps.Keys(byName)))
	}
	if !bytes.HasPrefix(pdf.Data, []byte("%PDF-1.4")) {
		t.Errorf("referto.pdf: expected the base64 payload decoded, got %q", pdf.Data)
	}

	sig, ok := byName["smime.p7s"]
	if !ok {
		t.Fatalf("expected the signature part, got %v", slices.Sorted(maps.Keys(byName)))
	}
	if !bytes.Equal(sig.Data, []byte{0, 1, 2, 3, 4}) {
		t.Errorf("smime.p7s: expected the base64 payload decoded, got %v", sig.Data)
	}
}

// Anything that is not a compound file is an error, not a panic. The input is
// full-length on purpose: a short one fails at the header read, which proves
// nothing about the signature check that is the thing under test here.
func TestReadRejectsNonMSG(t *testing.T) {
	notMSG := make([]byte, 512)
	copy(notMSG, "From: a@example.com\r\n\r\nnot a msg")

	_, err := Read(bytes.NewReader(notMSG))
	if err == nil {
		t.Fatal("expected an error for a non-MSG input, got nil")
	}
	if !strings.Contains(err.Error(), "invalid MSG file") {
		t.Errorf("expected the signature check to reject it, got %v", err)
	}
}
