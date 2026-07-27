package mailfmt

import (
	"github.com/ffois/mailfmt/internal/email"
)

// EmailData is the flat representation returned by every reader.
type EmailData = email.Data

// EmailAttachment is a single decoded attachment.
type EmailAttachment = email.Attachment

// ConvertToUTF8 returns s unchanged if it is already valid UTF-8, otherwise
// decodes it from Windows-1252.
func ConvertToUTF8(s string) string { return email.ConvertToUTF8(s) }
