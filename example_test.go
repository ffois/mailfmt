package mailfmt_test

import (
	"io"
	"strings"
	"testing"

	"github.com/ffois/mailfmt"
)

// The package split moved every reader into internal packages. This test
// touches the whole public surface from outside the package, so any rename
// or signature change breaks the build here instead of breaking callers.
func TestPublicAPISurface(t *testing.T) {
	var (
		_ func(string) (*mailfmt.EmailData, mailfmt.EmailFormat, error) = mailfmt.ParseFile
		_ func(string) (mailfmt.EmailFormat, error)                     = mailfmt.DetectEmailFormat
		_ func(string) (*mailfmt.EmailData, error)                      = mailfmt.ReadEmlFile
		_ func(string) (*mailfmt.EmailData, error)                      = mailfmt.ReadPecInnerEml
		_ func(string) (*mailfmt.EmailData, error)                      = mailfmt.ReadMsgFile
		_ func(io.ReaderAt) (*mailfmt.EmailData, error)                 = mailfmt.Read
		_ func(io.Reader) (mailfmt.Email, error)                        = mailfmt.Parse
		_ func(string) string                                           = mailfmt.ConvertToUTF8
	)

	var (
		_ mailfmt.EmailFormat = mailfmt.FormatEML
		_ mailfmt.EmailFormat = mailfmt.FormatMSG
		_ mailfmt.EmailFormat = mailfmt.FormatUnknown
	)

	// The struct fields are part of the surface too.
	data := mailfmt.EmailData{
		From:          "a@example.com",
		To:            []string{"b@example.com"},
		Cc:            nil,
		Bcc:           nil,
		Subject:       "s",
		Body:          "b",
		Attachments:   []mailfmt.EmailAttachment{{Filename: "f", ContentType: "text/plain", Data: []byte("x")}},
		IsPec:         false,
		HasInnerEmail: false,
		Date:          "",
	}
	if data.Attachments[0].Filename != "f" {
		t.Fatalf("unexpected attachment filename %q", data.Attachments[0].Filename)
	}

	parsed, err := mailfmt.Parse(strings.NewReader("Subject: hi\r\n\r\nbody\r\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if parsed.Subject != "hi" {
		t.Errorf("expected subject %q, got %q", "hi", parsed.Subject)
	}

	var (
		_ mailfmt.Attachment   = mailfmt.Attachment{Filename: "f", ContentType: "text/plain"}
		_ mailfmt.EmbeddedFile = mailfmt.EmbeddedFile{CID: "c", Filename: "f"}
	)
}
