package mailfmt

import (
	"io"

	"github.com/ffois/mailfmt/internal/email"
	"github.com/ffois/mailfmt/internal/eml"
	"github.com/ffois/mailfmt/internal/mimeparse"
	"github.com/ffois/mailfmt/internal/msg"
	"github.com/ffois/mailfmt/internal/pec"
)

// The readers live in internal packages, one per format. This file is the
// facade that keeps them reachable under the names this module has always
// exported: type aliases are type identities, not new types, so callers can
// pass a mailfmt.EmailData wherever an email.Data is expected and back.

// EmailData is a message flattened into the shape callers consume.
type EmailData = email.Data

// EmailAttachment is a single decoded attachment.
type EmailAttachment = email.Attachment

// Email is a parsed MIME message with all RFC5322 headers.
type Email = mimeparse.Email

// Attachment is an attachment part of a parsed MIME message.
type Attachment = mimeparse.Attachment

// EmbeddedFile is an inline part of a parsed MIME message, referenced from
// the body via its Content-ID.
type EmbeddedFile = mimeparse.EmbeddedFile

// ReadEmlFile parses the MIME message at path.
func ReadEmlFile(path string) (*EmailData, error) { return eml.ReadFile(path) }

// ReadPecInnerEml parses the message carried inside a PEC envelope's
// postacert.eml part. It fails when the part is missing.
func ReadPecInnerEml(path string) (*EmailData, error) { return pec.ReadInner(path) }

// ReadMsgFile parses the Outlook MSG file at path.
func ReadMsgFile(path string) (*EmailData, error) { return msg.ReadFile(path) }

// Read parses an Outlook MSG message from r.
func Read(r io.ReaderAt) (*EmailData, error) { return msg.Read(r) }

// Parse reads a MIME message from r.
func Parse(r io.Reader) (Email, error) { return mimeparse.Parse(r) }

// ConvertToUTF8 returns s unchanged if it is already valid UTF-8, otherwise
// decodes it from Windows-1252.
func ConvertToUTF8(s string) string { return email.ConvertToUTF8(s) }
