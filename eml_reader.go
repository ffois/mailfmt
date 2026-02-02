package internal

import (
	"fmt"
	"io"
	"net/mail"
	"os"
	"unicode/utf8"

	"github.com/DusanKasan/parsemail"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"
)

type EmailAttachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Data        []byte `json:"data"`
}

type EmailData struct {
	From        string            `json:"from"`
	To          []string          `json:"to"`
	Cc          []string          `json:"cc"`
	Bcc         []string          `json:"bcc"`
	Subject     string            `json:"subject"`
	Body        string            `json:"body"`
	Attachments []EmailAttachment `json:"attachments"`
}

func ReadEmlFile(filePath string) (*EmailData, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	email, err := parsemail.Parse(file)
	if err != nil {
		return nil, fmt.Errorf("failed to parse email: %w", err)
	}

	// Format addresses
	formatAddress := func(addr []*mail.Address) []string {
		var result []string
		for _, a := range addr {
			result = append(result, convertToUTF8(a.String()))
		}
		return result
	}

	// Determine body (prefer HTML)
	body := email.HTMLBody
	if body == "" {
		body = email.TextBody
	}

	// Process attachments
	var attachments []EmailAttachment
	for _, att := range email.Attachments {
		data, err := io.ReadAll(att.Data)
		if err != nil {
			continue // Handle error or skip? Skipping for now.
		}
		attachments = append(attachments, EmailAttachment{
			Filename:    att.Filename,
			ContentType: att.ContentType,
			Data:        data,
		})
	}

	// Format From
	var from string
	if len(email.From) > 0 {
		from = email.From[0].String()
	}

	return &EmailData{
		From:        convertToUTF8(from),
		To:          formatAddress(email.To),
		Cc:          formatAddress(email.Cc),
		Bcc:         formatAddress(email.Bcc),
		Subject:     convertToUTF8(email.Subject),
		Body:        convertToUTF8(body),
		Attachments: attachments,
	}, nil
}

func convertToUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}

	// If invalid UTF-8, assume Windows-1252 (superset of ISO-8859-1)
	decoder := charmap.Windows1252.NewDecoder()
	decoded, _, err := transform.String(decoder, s)
	if err != nil {
		return s // Return as-is if decoding fails
	}
	return decoded
}
