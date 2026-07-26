package mailfmt

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFixture writes raw bytes to a temp file and returns its path.
func writeFixture(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write fixture %s: %v", name, err)
	}
	return path
}

// A plain EML has no postacert.eml part, so the PEC attempt must fail
// silently and ParseFile must fall back to the plain EML reader.
func TestParseFilePlainEML(t *testing.T) {
	const raw = "From: mario <mario@example.it>\r\n" +
		"To: luigi@example.it\r\n" +
		"Subject: Ciao\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"corpo del messaggio\r\n"

	path := writeFixture(t, "plain.eml", raw)

	email, format, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile returned error: %v", err)
	}
	if format != FormatEML {
		t.Errorf("expected format %q, got %q", FormatEML, format)
	}
	if email.Subject != "Ciao" {
		t.Errorf("expected subject %q, got %q", "Ciao", email.Subject)
	}
	if email.IsPec {
		t.Error("plain EML must not be flagged as PEC")
	}
}

// A PEC envelope carries the real message inside postacert.eml. ParseFile
// must return the inner message, not the envelope.
func TestParseFilePecEnvelope(t *testing.T) {
	const inner = "From: mittente <mittente@pec.it>\r\n" +
		"To: destinatario@pec.it\r\n" +
		"Subject: Messaggio interno\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"contenuto reale\r\n"

	raw := "From: posta-certificata@pec.it\r\n" +
		"To: destinatario@pec.it\r\n" +
		"Subject: POSTA CERTIFICATA: Messaggio interno\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"pecbound\"\r\n" +
		"\r\n" +
		"--pecbound\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"Messaggio di posta certificata\r\n" +
		"--pecbound\r\n" +
		"Content-Type: message/rfc822\r\n" +
		"Content-Disposition: attachment; filename=\"postacert.eml\"\r\n" +
		"\r\n" +
		inner +
		"--pecbound--\r\n"

	path := writeFixture(t, "pec.eml", raw)

	email, format, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile returned error: %v", err)
	}
	if format != FormatEML {
		t.Errorf("expected format %q, got %q", FormatEML, format)
	}
	if email.Subject != "Messaggio interno" {
		t.Errorf("expected inner subject %q, got %q", "Messaggio interno", email.Subject)
	}
}

// A missing file must surface the detect error, not panic.
func TestParseFileMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.eml")

	email, format, err := ParseFile(path)
	if err == nil {
		t.Fatal("expected an error for a missing file, got nil")
	}
	if email != nil {
		t.Errorf("expected nil email on error, got %+v", email)
	}
	if format != FormatUnknown {
		t.Errorf("expected format %q on error, got %q", FormatUnknown, format)
	}
}
