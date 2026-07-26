package internal

import (
	"strings"
	"testing"
)

// Yahoo-style email: real attachments carry both a Content-Id and a
// Content-Disposition: attachment header. The declared filename must win
// over the Content-Id.
const yahooStyleEml = "From: elisabetta <test@yahoo.it>\r\n" +
	"To: sinistri@example.it\r\n" +
	"Subject: Test allegati\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"outer\"\r\n" +
	"\r\n" +
	"--outer\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"Content-Transfer-Encoding: 7bit\r\n" +
	"\r\n" +
	"<html><body>Si allega quanto richiesto.</body></html>\r\n" +
	"--outer\r\n" +
	"Content-Type: application/vnd.openxmlformats-officedocument.wordprocessingml.document; name=\"Dichiarazione.docx\"\r\n" +
	"Content-Disposition: attachment; filename=\"Dichiarazione.docx\"\r\n" +
	"Content-Id: <c181c7cd-3f95-4027-c17f-6547c60c5335@yahoo.com>\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n" +
	"UEsDBA==\r\n" +
	"--outer\r\n" +
	"Content-Type: application/pdf; name=\"Postewelfareservizi.pdf\"\r\n" +
	"Content-Disposition: attachment; filename=\"Postewelfareservizi.pdf\"\r\n" +
	"Content-Id: <d292d8de-4a06-5138-d28f-7658d71d6446@yahoo.com>\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n" +
	"JVBERg==\r\n" +
	"--outer--\r\n"

func TestParseAttachmentWithContentIdKeepsFilename(t *testing.T) {
	email, err := Parse(strings.NewReader(yahooStyleEml))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if len(email.EmbeddedFiles) != 0 {
		t.Errorf("expected 0 embedded files, got %d", len(email.EmbeddedFiles))
	}
	if len(email.Attachments) != 2 {
		t.Fatalf("expected 2 attachments, got %d", len(email.Attachments))
	}

	want := []string{"Dichiarazione.docx", "Postewelfareservizi.pdf"}
	for i, at := range email.Attachments {
		if at.Filename != want[i] {
			t.Errorf("attachment %d: expected filename %q, got %q", i, want[i], at.Filename)
		}
	}
}

// Inline image referenced via cid: must stay an embedded file (so the body
// reference can be resolved) but keep its declared filename when present.
const inlineImageEml = "From: a@example.com\r\n" +
	"To: b@example.com\r\n" +
	"Subject: Inline\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/related; boundary=\"rel\"\r\n" +
	"\r\n" +
	"--rel\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"Content-Transfer-Encoding: 7bit\r\n" +
	"\r\n" +
	"<html><body><img src=\"cid:logo123\"></body></html>\r\n" +
	"--rel\r\n" +
	"Content-Type: image/png; name=\"logo.png\"\r\n" +
	"Content-Disposition: inline; filename=\"logo.png\"\r\n" +
	"Content-Id: <logo123>\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n" +
	"iVBORw0KGgo=\r\n" +
	"--rel--\r\n"

func TestParseInlineImageStaysEmbeddedWithFilename(t *testing.T) {
	email, err := Parse(strings.NewReader(inlineImageEml))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if len(email.Attachments) != 0 {
		t.Errorf("expected 0 attachments, got %d", len(email.Attachments))
	}
	if len(email.EmbeddedFiles) != 1 {
		t.Fatalf("expected 1 embedded file, got %d", len(email.EmbeddedFiles))
	}

	ef := email.EmbeddedFiles[0]
	if ef.CID != "logo123" {
		t.Errorf("expected CID %q, got %q", "logo123", ef.CID)
	}
	if ef.Filename != "logo.png" {
		t.Errorf("expected filename %q, got %q", "logo.png", ef.Filename)
	}
}

func TestEmbeddedFilename(t *testing.T) {
	cases := []struct {
		name     string
		ef       EmbeddedFile
		mimeType string
		want     string
	}{
		{
			name:     "declared filename wins",
			ef:       EmbeddedFile{CID: "c181c7cd@yahoo.com", Filename: "logo.png"},
			mimeType: "image/png",
			want:     "logo.png",
		},
		{
			name:     "cid with domain-like suffix gets mime extension",
			ef:       EmbeddedFile{CID: "c181c7cd@yahoo.com"},
			mimeType: "image/png",
			want:     "c181c7cd@yahoo.com.png",
		},
		{
			name:     "cid without extension gets mime extension",
			ef:       EmbeddedFile{CID: "image001"},
			mimeType: "image/jpeg",
			want:     "image001.jpg",
		},
		{
			name:     "empty cid falls back to placeholder",
			ef:       EmbeddedFile{},
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
