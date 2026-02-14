package internal

import (
	"bytes"
	"os"
)

// EmailFormat represents the detected format of an email file.
type EmailFormat string

const (
	FormatEML     EmailFormat = "eml"
	FormatMSG     EmailFormat = "msg"
	FormatUnknown EmailFormat = "unknown"
)

// msgMagic is the OLE2/CFB compound file header signature used by .msg files.
var msgMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

// DetectEmailFormat identifies the email file format by inspecting the file's
// binary magic bytes, regardless of the file extension.
//
// Supported formats:
//   - "msg": Microsoft Outlook MSG (OLE2/CFB compound file)
//   - "eml": Standard MIME email (RFC 5322)
//   - "unknown": Could not determine format
func DetectEmailFormat(filePath string) (EmailFormat, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return FormatUnknown, err
	}
	defer f.Close()

	buf := make([]byte, 8)
	n, err := f.Read(buf)
	if err != nil || n < 1 {
		return FormatUnknown, nil
	}

	// MSG files start with the OLE2 Compound File Binary magic bytes.
	if n >= 8 && bytes.Equal(buf[:8], msgMagic) {
		return FormatMSG, nil
	}

	// EML files are plain-text MIME messages; assume EML for anything else.
	return FormatEML, nil
}
