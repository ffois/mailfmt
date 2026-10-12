package mailfmt_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ffois/mailfmt"
)

// copyAs copies a fixture to a temp file with another name, to test what the
// extension alone decides.
func copyAs(t *testing.T, src, name string) string {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	dst := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return dst
}

// Outlook is handed a file by its extension, not by its content.
func TestMSDetectEmailFormatUsesExtension(t *testing.T) {
	msgFixture := filepath.Join("testdata", "msg", "with-attachment.msg")
	emlFixture := filepath.Join("testdata", "eml", "alternative.eml")

	cases := []struct {
		path string
		want mailfmt.EmailFormat
	}{
		{msgFixture, mailfmt.FormatMSG},
		{emlFixture, mailfmt.FormatEML},
		{copyAs(t, msgFixture, "MESSAGGIO.MSG"), mailfmt.FormatMSG},
		{copyAs(t, msgFixture, "messaggio.eml"), mailfmt.FormatEML},
		{copyAs(t, emlFixture, "messaggio.txt"), mailfmt.FormatUnknown},
	}
	for _, tc := range cases {
		got, err := mailfmt.MS_DetectEmailFormat(tc.path)
		if err != nil {
			t.Errorf("%s: %v", tc.path, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: expected %q, got %q", tc.path, tc.want, got)
		}
	}
}

func TestMSDetectEmailFormatMissingFile(t *testing.T) {
	got, err := mailfmt.MS_DetectEmailFormat(filepath.Join(t.TempDir(), "nope.msg"))
	if err == nil || got != mailfmt.FormatUnknown {
		t.Errorf("expected (unknown, error), got (%q, %v)", got, err)
	}
}

// MS_ParseFile reads both formats and reports the same headers as ParseFile
// for a message that is not a PEC envelope; body and attachments follow
// Outlook's rules and may differ.
func TestMSParseFileMatchesParseFile(t *testing.T) {
	for _, path := range []string{
		filepath.Join("testdata", "msg", "with-attachment.msg"),
		filepath.Join("testdata", "eml", "mixed-attachment.eml"),
	} {
		plain, _, err := mailfmt.ParseFile(path)
		if err != nil {
			t.Fatalf("%s: ParseFile: %v", path, err)
		}
		ms, _, err := mailfmt.MS_ParseFile(path)
		if err != nil {
			t.Fatalf("%s: MS_ParseFile: %v", path, err)
		}
		if ms.Subject != plain.Subject || ms.From != plain.From {
			t.Errorf("%s: headers differ:\n plain=%+v\n    ms=%+v", path, *plain, ms.Data)
		}
	}
}

// The inner message of a PEC is the postacert.eml Outlook attaches to the
// envelope, read with the same rules.
func TestMSReadPecInnerEml(t *testing.T) {
	path := filepath.Join("testdata", "pec", "envelope.eml")

	plain, err := mailfmt.ReadPecInnerEml(path)
	if err != nil {
		t.Fatalf("ReadPecInnerEml: %v", err)
	}
	ms, err := mailfmt.MS_ReadPecInnerEml(path)
	if err != nil {
		t.Fatalf("MS_ReadPecInnerEml: %v", err)
	}
	if ms.Subject != plain.Subject || ms.From != plain.From {
		t.Errorf("expected the inner message, got subject %q from %q", ms.Subject, ms.From)
	}

	if _, err := mailfmt.MS_ReadPecInnerEml(filepath.Join("testdata", "pec", "no-postacert.eml")); err == nil {
		t.Error("expected an error for an envelope without postacert.eml")
	}
	// signed.eml carries a placeholder instead of a PKCS #7 signature, and
	// Outlook 2010 refuses to open it.
	if _, err := mailfmt.MS_ReadPecInnerEml(filepath.Join("testdata", "pec", "signed.eml")); err == nil {
		t.Error("expected an error for an envelope whose signature is not PKCS #7")
	}
}

// A file whose extension Outlook has no association for is refused, and a
// .msg that is really MIME fails to parse instead of being sniffed as EML.
func TestMSParseFileTrustsExtension(t *testing.T) {
	emlFixture := filepath.Join("testdata", "eml", "alternative.eml")

	if _, format, err := mailfmt.MS_ParseFile(copyAs(t, emlFixture, "messaggio.txt")); err == nil || format != mailfmt.FormatUnknown {
		t.Errorf("expected an error for .txt, got format %q, err %v", format, err)
	}
	if _, format, err := mailfmt.MS_ParseFile(copyAs(t, emlFixture, "messaggio.msg")); err == nil || format != mailfmt.FormatMSG {
		t.Errorf("expected a MSG parse error, got format %q, err %v", format, err)
	}
}
