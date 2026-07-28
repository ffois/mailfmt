package pec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeEML(t *testing.T, raw string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "envelope.eml")
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// envelopeWith wraps an inner message in a PEC envelope carrying it as
// postacert.eml, plus any extra parts given.
func envelopeWith(inner, extraParts string) string {
	return "From: posta-certificata@pec.it\r\n" +
		"To: dest@pec.it\r\n" +
		"Subject: POSTA CERTIFICATA: originale\r\n" +
		"Date: Mon, 02 Jun 2025 10:00:00 +0200\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"b\"\r\n" +
		"\r\n" +
		"--b\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"Messaggio di posta certificata\r\n" +
		"--b\r\n" +
		"Content-Type: message/rfc822\r\n" +
		"Content-Disposition: attachment; filename=\"postacert.eml\"\r\n" +
		"\r\n" +
		inner +
		extraParts +
		"--b--\r\n"
}

const innerMessage = "From: mittente <mittente@example.it>\r\n" +
	"To: destinatario@example.it\r\n" +
	"Subject: Oggetto reale\r\n" +
	"Date: Mon, 02 Jun 2025 09:59:00 +0200\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"contenuto reale\r\n"

// The envelope is packaging: what callers want is the message inside it, with
// the inner headers, not the "POSTA CERTIFICATA:" ones.
func TestReadInnerReturnsTheInnerMessage(t *testing.T) {
	data, err := ReadInner(writeEML(t, envelopeWith(innerMessage, "")))
	if err != nil {
		t.Fatalf("ReadInner: %v", err)
	}
	if data.Subject != "Oggetto reale" {
		t.Errorf("expected the inner subject %q, got %q", "Oggetto reale", data.Subject)
	}
	if !strings.Contains(data.From, "mittente@example.it") {
		t.Errorf("expected the inner sender, got %q", data.From)
	}
	if !strings.Contains(data.Body, "contenuto reale") {
		t.Errorf("expected the inner body, got %q", data.Body)
	}
	if data.Date != "Mon, 02 Jun 2025 09:59:00 +0200" {
		t.Errorf("expected the inner Date header, got %q", data.Date)
	}
}

// Without a postacert.eml part there is no inner message to return, and the
// caller has to be told so it can fall back to reading the file as plain EML.
func TestReadInnerFailsWithoutPostacert(t *testing.T) {
	raw := "From: a@example.it\r\n" +
		"To: b@example.it\r\n" +
		"Subject: Non una PEC\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"corpo\r\n"

	data, err := ReadInner(writeEML(t, raw))
	if err == nil {
		t.Fatal("expected an error when postacert.eml is missing, got nil")
	}
	if data != nil {
		t.Errorf("expected nil data on error, got %+v", data)
	}
}

// The part name is matched case-insensitively: senders are not consistent
// about it.
func TestReadInnerMatchesPostacertCaseInsensitively(t *testing.T) {
	envelope := strings.Replace(
		envelopeWith(innerMessage, ""),
		`filename="postacert.eml"`,
		`filename="PostaCert.EML"`,
		1,
	)

	data, err := ReadInner(writeEML(t, envelope))
	if err != nil {
		t.Fatalf("ReadInner: %v", err)
	}
	if data.Subject != "Oggetto reale" {
		t.Errorf("expected the inner subject %q, got %q", "Oggetto reale", data.Subject)
	}
}

// When the inner message itself carries an .eml attachment, HasInnerEmail
// flags it so callers know there is another level to descend.
func TestReadInnerFlagsNestedEmail(t *testing.T) {
	nested := "From: mittente <mittente@example.it>\r\n" +
		"To: destinatario@example.it\r\n" +
		"Subject: Con inoltro\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"inner\"\r\n" +
		"\r\n" +
		"--inner\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"in allegato l'originale\r\n" +
		"--inner\r\n" +
		"Content-Type: message/rfc822\r\n" +
		"Content-Disposition: attachment; filename=\"originale.eml\"\r\n" +
		"\r\n" +
		"From: terzo@example.it\r\n" +
		"Subject: Terzo livello\r\n" +
		"\r\n" +
		"testo\r\n" +
		"--inner--\r\n"

	data, err := ReadInner(writeEML(t, envelopeWith(nested, "")))
	if err != nil {
		t.Fatalf("ReadInner: %v", err)
	}
	if !data.HasInnerEmail {
		t.Error("expected HasInnerEmail to be set for a nested .eml attachment")
	}
}
