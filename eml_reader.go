package internal

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/mail"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"
)

type EmailAttachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Data        []byte `json:"data"`
}

type EmailData struct {
	From          string            `json:"from"`
	To            []string          `json:"to"`
	Cc            []string          `json:"cc"`
	Bcc           []string          `json:"bcc"`
	Subject       string            `json:"subject"`
	Body          string            `json:"body"`
	Attachments   []EmailAttachment `json:"attachments"`
	IsPec         bool              `json:"isPec"`
	HasInnerEmail bool              `json:"hasInnerEmail"`
}

func ReadEmlFile(filePath string) (*EmailData, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	email, err := Parse(file)
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

	// Process attachments list and PEC detection
	var attachments []EmailAttachment
	var hasDatiCert, hasSmime, hasInnerEmail bool

	// Process embedded files (inline images) -> add to body AND add as attachments
	for _, ef := range email.EmbeddedFiles {
		data, err := io.ReadAll(ef.Data)
		if err != nil {
			continue
		}

		// Convert to base64
		b64 := base64.StdEncoding.EncodeToString(data)
		mimeType := ef.ContentType
		if parts := strings.Split(mimeType, ";"); len(parts) > 0 {
			mimeType = strings.TrimSpace(parts[0])
		}
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}

		// Create data URI
		dataURI := fmt.Sprintf("data:%s;base64,%s", mimeType, b64)

		// Replace cid:reference with data URI in HTML body (case-insensitive).
		// ef.CID is already trimmed of <>.
		re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta("cid:"+ef.CID))
		body = re.ReplaceAllLiteralString(body, dataURI)

		// ALSO ADD AS ATTACHMENTS for the viewer
		filename := ef.CID
		if filename == "" {
			filename = "embedded_image"
		}
		// If no extension, try to infer from mimetype
		if !strings.Contains(filename, ".") {
			ext := "dat"
			switch mimeType {
			case "image/jpeg":
				ext = "jpg"
			case "image/png":
				ext = "png"
			case "image/gif":
				ext = "gif"
			case "application/pdf":
				ext = "pdf"
			default:
				if parts := strings.Split(mimeType, "/"); len(parts) > 1 {
					ext = parts[1]
				}
			}
			filename = fmt.Sprintf("%s.%s", filename, ext)
		}

		attachments = append(attachments, EmailAttachment{
			Filename:    filename,
			ContentType: mimeType,
			Data:        data,
		})
	}

	// Process standard attachments
	for _, att := range email.Attachments {
		data, err := io.ReadAll(att.Data)
		if err != nil {
			continue // Handle error or skip? Skipping for now.
		}

		// PEC Detection Logic
		filenameLower := strings.ToLower(att.Filename)
		if filenameLower == "daticert.xml" {
			hasDatiCert = true
		}
		if filenameLower == "smime.p7s" {
			hasSmime = true
		}
		if strings.HasSuffix(filenameLower, ".eml") {
			hasInnerEmail = true
		}

		attachments = append(attachments, EmailAttachment{
			Filename:    att.Filename,
			ContentType: att.ContentType,
			Data:        data,
		})
	}

	// Expand any TNEF (winmail.dat) attachments into their contained files.
	attachments = expandTNEFAttachments(attachments)

	isPec := hasDatiCert && hasSmime

	// Format From
	var from string
	if len(email.From) > 0 {
		from = email.From[0].String()
	}

	return &EmailData{
		From:          convertToUTF8(from),
		To:            formatAddress(email.To),
		Cc:            formatAddress(email.Cc),
		Bcc:           formatAddress(email.Bcc),
		Subject:       convertToUTF8(email.Subject),
		Body:          convertToUTF8(body),
		Attachments:   attachments,
		IsPec:         isPec,
		HasInnerEmail: hasInnerEmail,
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

func ReadPecInnerEml(filePath string) (*EmailData, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// 1. Parse outer "Envelope"
	outerEmail, err := Parse(file)
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
	innerEmail, err := Parse(bytes.NewReader(innerEmailData))
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

	// Expand any TNEF (winmail.dat) attachments into their contained files.
	attachments = expandTNEFAttachments(attachments)

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
