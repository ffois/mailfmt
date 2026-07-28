package eml

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ffois/mailfmt/internal/mimeparse"
)

func TestEmbeddedFilename(t *testing.T) {
	cases := []struct {
		name     string
		ef       mimeparse.EmbeddedFile
		mimeType string
		want     string
	}{
		{
			name:     "declared filename wins",
			ef:       mimeparse.EmbeddedFile{CID: "c181c7cd@yahoo.com", Filename: "logo.png"},
			mimeType: "image/png",
			want:     "logo.png",
		},
		{
			name:     "cid with domain-like suffix gets mime extension",
			ef:       mimeparse.EmbeddedFile{CID: "c181c7cd@yahoo.com"},
			mimeType: "image/png",
			want:     "c181c7cd@yahoo.com.png",
		},
		{
			name:     "cid without extension gets mime extension",
			ef:       mimeparse.EmbeddedFile{CID: "image001"},
			mimeType: "image/jpeg",
			want:     "image001.jpg",
		},
		{
			name:     "empty cid falls back to placeholder",
			ef:       mimeparse.EmbeddedFile{},
			mimeType: "image/gif",
			want:     "embedded_image.gif",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := embeddedFilename(tc.ef, tc.mimeType); got != tc.want {
				t.Errorf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

// writeEML writes a message to a temp file and returns its path.
func writeEML(t *testing.T, raw string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "message.eml")
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// With both a text and an HTML alternative, the HTML one is what callers get:
// it is the richer rendering, and the text part is a fallback for clients
// that cannot display it.
func TestBuildPrefersHTMLBody(t *testing.T) {
	raw := "From: a@example.com\r\n" +
		"To: b@example.com\r\n" +
		"Subject: Alternative\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=\"alt\"\r\n" +
		"\r\n" +
		"--alt\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"versione testo\r\n" +
		"--alt\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<p>versione html</p>\r\n" +
		"--alt--\r\n"

	data, err := ReadFile(writeEML(t, raw))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(data.Body, "versione html") {
		t.Errorf("expected the HTML alternative in the body, got %q", data.Body)
	}
	if strings.Contains(data.Body, "versione testo") {
		t.Errorf("text alternative leaked into the body: %q", data.Body)
	}
}

// An inline image is inlined into the body as a data URI so the message
// renders standalone, and is also listed as an attachment so callers that
// only walk Attachments still see it.
func TestBuildInlinesCIDReferences(t *testing.T) {
	raw := "From: a@example.com\r\n" +
		"To: b@example.com\r\n" +
		"Subject: Inline\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/related; boundary=\"rel\"\r\n" +
		"\r\n" +
		"--rel\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<img src=\"cid:logo123\">\r\n" +
		"--rel\r\n" +
		"Content-Type: image/png; name=\"logo.png\"\r\n" +
		"Content-Disposition: inline; filename=\"logo.png\"\r\n" +
		"Content-Id: <logo123>\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"iVBORw0KGgo=\r\n" +
		"--rel--\r\n"

	data, err := ReadFile(writeEML(t, raw))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(data.Body, "cid:logo123") {
		t.Errorf("cid: reference left unresolved in the body: %q", data.Body)
	}
	if !strings.Contains(data.Body, "data:image/png;base64,") {
		t.Errorf("inline image was not substituted with a data URI: %q", data.Body)
	}
	if len(data.Attachments) != 1 {
		t.Fatalf("expected the inline image to be listed as an attachment, got %d", len(data.Attachments))
	}
	if data.Attachments[0].Filename != "logo.png" {
		t.Errorf("expected filename %q, got %q", "logo.png", data.Attachments[0].Filename)
	}
}

// A message is a PEC envelope only when it carries both daticert.xml and
// smime.p7s. Either one alone is not enough.
func TestBuildFlagsPecOnlyWithBothParts(t *testing.T) {
	envelope := func(extraPart string) string {
		return "From: posta-certificata@pec.it\r\n" +
			"To: dest@pec.it\r\n" +
			"Subject: POSTA CERTIFICATA\r\n" +
			"MIME-Version: 1.0\r\n" +
			"Content-Type: multipart/mixed; boundary=\"b\"\r\n" +
			"\r\n" +
			"--b\r\n" +
			"Content-Type: text/plain; charset=utf-8\r\n" +
			"\r\n" +
			"ricevuta\r\n" +
			extraPart +
			"--b--\r\n"
	}
	datiCert := "--b\r\n" +
		"Content-Type: application/xml\r\n" +
		"Content-Disposition: attachment; filename=\"daticert.xml\"\r\n" +
		"\r\n" +
		"<postacert/>\r\n"
	smime := "--b\r\n" +
		"Content-Type: application/pkcs7-signature\r\n" +
		"Content-Disposition: attachment; filename=\"smime.p7s\"\r\n" +
		"\r\n" +
		"sig\r\n"

	cases := []struct {
		name  string
		parts string
		want  bool
	}{
		{"both parts", datiCert + smime, true},
		{"only daticert", datiCert, false},
		{"only smime", smime, false},
		{"neither", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := ReadFile(writeEML(t, envelope(tc.parts)))
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if data.IsPec != tc.want {
				t.Errorf("expected IsPec=%v, got %v", tc.want, data.IsPec)
			}
		})
	}
}

// Headers encoded per RFC 2047 must be decoded, whatever the charset and
// whichever of the two encodings is used.
func TestReadFileDecodesEncodedHeaders(t *testing.T) {
	raw := "From: =?utf-8?B?TWFyaW8gUm9zc2k=?= <mario@example.it>\r\n" +
		"To: b@example.it\r\n" +
		"Subject: =?iso-8859-1?Q?Perch=E9_no=3F?=\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"corpo\r\n"

	data, err := ReadFile(writeEML(t, raw))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if data.Subject != "Perché no?" {
		t.Errorf("expected subject %q, got %q", "Perché no?", data.Subject)
	}
	if !strings.Contains(data.From, "Mario Rossi") {
		t.Errorf("expected the decoded display name in From, got %q", data.From)
	}
}

// A body declared as iso-8859-1 has to come back as valid UTF-8.
func TestReadFileDecodesLatin1Body(t *testing.T) {
	raw := "From: a@example.it\r\n" +
		"To: b@example.it\r\n" +
		"Subject: Latin1\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=iso-8859-1\r\n" +
		"Content-Transfer-Encoding: 8bit\r\n" +
		"\r\n" +
		"Caff\xe8 corretto\r\n"

	data, err := ReadFile(writeEML(t, raw))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !utf8.ValidString(data.Body) {
		t.Fatalf("body is not valid UTF-8: %q", data.Body)
	}
	if !strings.Contains(data.Body, "Caffè") {
		t.Errorf("expected %q in the body, got %q", "Caffè", data.Body)
	}
}

// A missing file is an error, not a panic or an empty result.
func TestReadFileMissing(t *testing.T) {
	data, err := ReadFile(filepath.Join(t.TempDir(), "nope.eml"))
	if err == nil {
		t.Fatal("expected an error for a missing file, got nil")
	}
	if data != nil {
		t.Errorf("expected nil data on error, got %+v", data)
	}
}
