package internal

import (
	"bytes"
	"fmt"
	"io"
	"net/mail"
	"os"
	"strings"

	"emly/backend/utils"
)

// ReadPecInnerEml reads the inner email (postacert.eml) from a PEC EML file.
// It opens the outer file, looks for the specific attachment, and parses it.
func ReadPecInnerEml(filePath string) (*EmailData, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// 1. Parse outer "Envelope"
	outerEmail, err := utils.Parse(file)
	if err != nil {
		return nil, fmt.Errorf("failed to parse outer email: %w", err)
	}

	// 2. Look for the real content inside postacert.eml
	var innerEmailData []byte
	foundPec := false

	for _, att := range outerEmail.Attachments {
		// Standard PEC puts the real message in postacert.eml
		// Using case-insensitive check and substring as per example
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

	// 3. Parse the inner EML content
	innerEmail, err := utils.Parse(bytes.NewReader(innerEmailData))
	if err != nil {
		return nil, fmt.Errorf("failed to parse inner email structure: %w", err)
	}

	// Helper to format addresses (reused logic pattern from eml_reader.go)
	formatAddress := func(addr []*mail.Address) []string {
		var result []string
		for _, a := range addr {
			// convertToUTF8 is defined in eml_reader.go (same package)
			result = append(result, convertToUTF8(a.String()))
		}
		return result
	}

	// Determine body (prefer HTML)
	body := innerEmail.HTMLBody
	if body == "" {
		body = innerEmail.TextBody
	}

	// Process attachments of the inner email
	var attachments []EmailAttachment
	var hasDatiCert, hasSmime, hasInnerPecEmail bool

	for _, att := range innerEmail.Attachments {
		data, err := io.ReadAll(att.Data)
		if err != nil {
			continue
		}

		// Check internal flags for the inner email (recursive PEC check?)
		filenameLower := strings.ToLower(att.Filename)
		if filenameLower == "daticert.xml" {
			hasDatiCert = true
		}
		if filenameLower == "smime.p7s" {
			hasSmime = true
		}
		if strings.HasSuffix(filenameLower, ".eml") {
			hasInnerPecEmail = true
		}

		attachments = append(attachments, EmailAttachment{
			Filename:    att.Filename,
			ContentType: att.ContentType,
			Data:        data,
		})
	}

	isPec := hasDatiCert && hasSmime

	// Format From
	var from string
	if len(innerEmail.From) > 0 {
		from = innerEmail.From[0].String()
	}

	return &EmailData{
		From:          convertToUTF8(from),
		To:            formatAddress(innerEmail.To),
		Cc:            formatAddress(innerEmail.Cc),
		Bcc:           formatAddress(innerEmail.Bcc),
		Subject:       convertToUTF8(innerEmail.Subject),
		Body:          convertToUTF8(body),
		Attachments:   attachments,
		IsPec:         isPec,
		HasInnerEmail: hasInnerPecEmail,
	}, nil
}
