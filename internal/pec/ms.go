package pec

import (
	"fmt"
	"os"
	"strings"

	"github.com/ffois/mailfmt/internal/email"
	"github.com/ffois/mailfmt/internal/eml"
)

// MS_ReadInner reads the message carried inside a PEC envelope's
// postacert.eml part the way Outlook 2010 shows it: the envelope is imported
// with eml.MS_Parse, where postacert.eml is an attached message, and that
// attached message is then read with eml.MS_ParseAttached, as opening it
// from Outlook's attachment list does. It fails when the part is missing.
func MS_ReadInner(filePath string) (*email.MSData, error) {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	envelope, err := eml.MS_Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("failed to parse outer email: %w", err)
	}

	for _, att := range envelope.MSAttachments {
		if strings.Contains(strings.ToLower(att.Filename), "postacert.eml") {
			inner, err := eml.MS_ParseAttached(raw, att.Data)
			if err != nil {
				return nil, fmt.Errorf("failed to parse inner email structure: %w", err)
			}
			return inner, nil
		}
	}
	return nil, fmt.Errorf("not a signed PEC or 'postacert.eml' attachment is missing")
}
