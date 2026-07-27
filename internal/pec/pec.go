package pec

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ffois/mailfmt/internal/email"
	"github.com/ffois/mailfmt/internal/eml"
	"github.com/ffois/mailfmt/internal/mimeparse"
)

func ReadInner(filePath string) (*email.Data, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	outerEmail, err := mimeparse.Parse(file)
	if err != nil {
		return nil, fmt.Errorf("failed to parse outer email: %w", err)
	}

	// A signed PEC carries the real message in a postacert.eml part.
	var innerEmailData []byte
	foundPec := false
	for _, att := range outerEmail.Attachments {
		if strings.Contains(strings.ToLower(att.Filename), "postacert.eml") {
			data, err := io.ReadAll(att.Data)
			if err != nil {
				return nil, fmt.Errorf("failed to read inner email content: %w", err)
			}
			innerEmailData = data
			foundPec = true
			break
		}
	}
	if !foundPec {
		return nil, fmt.Errorf("not a signed PEC or 'postacert.eml' attachment is missing")
	}

	innerEmail, err := mimeparse.Parse(bytes.NewReader(innerEmailData))
	if err != nil {
		return nil, fmt.Errorf("failed to parse inner email structure: %w", err)
	}

	return eml.Build(innerEmail), nil
}
