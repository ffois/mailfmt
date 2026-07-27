// Package email holds the flat, JSON-friendly representation that every
// format reader in this module produces, plus the text helpers shared
// between them.
package email

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"
)

// Attachment is a single file carried by a message, already decoded.
type Attachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Data        []byte `json:"data"`
}

// Data is a message flattened into the shape callers consume: addresses as
// strings, a single body, attachments already decoded.
type Data struct {
	From          string       `json:"from"`
	To            []string     `json:"to"`
	Cc            []string     `json:"cc"`
	Bcc           []string     `json:"bcc"`
	Subject       string       `json:"subject"`
	Body          string       `json:"body"`
	Attachments   []Attachment `json:"attachments"`
	IsPec         bool         `json:"isPec"`
	HasInnerEmail bool         `json:"hasInnerEmail"`
	Date          string       `json:"date"`
}

// ConvertToUTF8 returns s unchanged if it is already valid UTF-8, otherwise
// decodes it from Windows-1252. On decode failure it returns s unchanged.
func ConvertToUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	decoded, _, err := transform.String(charmap.Windows1252.NewDecoder(), s)
	if err != nil {
		return s
	}
	return decoded
}

// DecodeString8 decodes a NUL-terminated single-byte string, as stored in
// MSG PT_STRING8 properties.
func DecodeString8(data []byte) string {
	return ConvertToUTF8(strings.TrimRight(string(data), "\x00"))
}
