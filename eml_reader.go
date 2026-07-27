package mailfmt

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/mail"
	"os"
	"regexp"
	"strings"

	"github.com/ffois/mailfmt/internal/tnef"
)

func ReadEmlFile(filePath string) (*EmailData, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	parsed, err := Parse(file)
	if err != nil {
		return nil, fmt.Errorf("failed to parse email: %w", err)
	}

	return buildEmailData(parsed), nil
}

// buildEmailData flattens a parsed MIME message into EmailData: it resolves
// cid: references in the body to inline data URIs, lists inline images as
// attachments as well, expands any winmail.dat container, and flags PEC
// envelopes.
func buildEmailData(parsed Email) *EmailData {
	formatAddress := func(addr []*mail.Address) []string {
		var result []string
		for _, a := range addr {
			result = append(result, ConvertToUTF8(a.String()))
		}
		return result
	}

	body := parsed.HTMLBody
	if body == "" {
		body = parsed.TextBody
	}

	var attachments []EmailAttachment
	var hasDatiCert, hasSmime, hasInnerEmail bool

	for _, ef := range parsed.EmbeddedFiles {
		data, err := io.ReadAll(ef.Data)
		if err != nil {
			continue
		}

		b64 := base64.StdEncoding.EncodeToString(data)
		mimeType := ef.ContentType
		if parts := strings.Split(mimeType, ";"); len(parts) > 0 {
			mimeType = strings.TrimSpace(parts[0])
		}
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}

		dataURI := fmt.Sprintf("data:%s;base64,%s", mimeType, b64)
		re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta("cid:"+ef.CID))
		body = re.ReplaceAllLiteralString(body, dataURI)

		attachments = append(attachments, EmailAttachment{
			Filename:    embeddedFilename(ef, mimeType),
			ContentType: mimeType,
			Data:        data,
		})
	}

	for _, att := range parsed.Attachments {
		data, err := io.ReadAll(att.Data)
		if err != nil {
			continue
		}

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

	attachments = tnef.Expand(attachments)

	var from string
	if len(parsed.From) > 0 {
		from = parsed.From[0].String()
	}

	return &EmailData{
		From:          ConvertToUTF8(from),
		To:            formatAddress(parsed.To),
		Cc:            formatAddress(parsed.Cc),
		Bcc:           formatAddress(parsed.Bcc),
		Subject:       ConvertToUTF8(parsed.Subject),
		Body:          ConvertToUTF8(body),
		Attachments:   attachments,
		IsPec:         hasDatiCert && hasSmime,
		HasInnerEmail: hasInnerEmail,
		Date:          parsed.Header.Get("Date"),
	}
}

func ReadPecInnerEml(filePath string) (*EmailData, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	outerEmail, err := Parse(file)
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

	innerEmail, err := Parse(bytes.NewReader(innerEmailData))
	if err != nil {
		return nil, fmt.Errorf("failed to parse inner email structure: %w", err)
	}

	return buildEmailData(innerEmail), nil
}

// embeddedFilename returns the best display filename for an embedded file:
// the filename declared by the MIME part when present, otherwise a name
// derived from the Content-ID. Content-IDs are not real filenames (e.g.
// "c181c7cd@yahoo.com"), so in that case an extension matching the MIME type
// is always appended unless the name already ends with it.
func embeddedFilename(ef EmbeddedFile, mimeType string) string {
	if ef.Filename != "" {
		return ef.Filename
	}

	base := ef.CID
	if base == "" {
		base = "embedded_image"
	}

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

	if strings.HasSuffix(strings.ToLower(base), "."+ext) {
		return base
	}
	return fmt.Sprintf("%s.%s", base, ext)
}
