package internal

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/richardlehane/mscfb"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// MAPI Property Tags
const (
	prSubject            = "0037"
	prBody               = "1000"
	prBodyHTML           = "1013"
	prSenderName         = "0C1A"
	prSenderEmail        = "0C1F"
	prDisplayTo          = "0E04" // Display list of To recipients
	prDisplayCc          = "0E03"
	prDisplayBcc         = "0E02"
	prMessageHeaders     = "007D"
	prClientSubmitTime   = "0039" // Date
	prAttachLongFilename = "3707"
	prAttachFilename     = "3704"
	prAttachData         = "3701"
	prAttachMimeTag      = "370E"
)

// MAPI Property Types
const (
	ptUnicode = "001F"
	ptString8 = "001E"
	ptBinary  = "0102"
)

type msgParser struct {
	reader *mscfb.Reader
	props  map[string][]byte
}

func parseMsgFile(filePath string) (*EmailData, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	doc, err := mscfb.New(f)
	if err != nil {
		return nil, err
	}

	email := &EmailData{
		To:  []string{},
		Cc:  []string{},
		Bcc: []string{},
	}

	// We need to iterate through the entries to find properties and attachments
	// Since mscfb is a sequential reader, we might need to be careful.
	// However, usually properties are in streams.

	// Strategy:
	// 1. Read all streams into a map keyed by their path/name for easier access?
	//    MSG files can be large (attachments), so maybe not all.
	// 2. Identify properties from their stream names directly.

	// Simplified approach: scan for stream names matching our patterns.

	// Better approach:
	// The Root Entry has "properties".
	// We need to detect if we are in an attachment storage.

	// Since mscfb iterates flat (Post-Order?), we can track context?
	// mscfb File struct provides Name and path.

	attachmentsMap := make(map[string]*EmailAttachment)

	for entry, err := doc.Next(); err == nil; entry, err = doc.Next() {
		name := entry.Name

		// Check if it's a property stream
		if strings.HasPrefix(name, "__substg1.0_") {
			path := entry.Path // Path is array of directory names

			// Root properties
			if len(path) == 0 { // In root
				val, err := io.ReadAll(doc)
				if err != nil {
					continue
				}
				processRootProperty(name, val, email)
			} else if strings.HasPrefix(path[len(path)-1], "__attach_version1.0_") {
				// Attachment property
				attachStorageName := path[len(path)-1]
				if _, exists := attachmentsMap[attachStorageName]; !exists {
					attachmentsMap[attachStorageName] = &EmailAttachment{}
				}

				val, err := io.ReadAll(doc)
				if err != nil {
					continue
				}
				processAttachProperty(name, val, attachmentsMap[attachStorageName])
			}
		}
	}

	// Finalize attachments
	for _, att := range attachmentsMap {
		if strings.Contains(strings.ToLower(att.ContentType), "multipart/signed") {
			dataStr := string(att.Data)
			// Check if it already looks like a plain text EML (contains typical headers)
			if strings.Contains(dataStr, "Content-Type:") || strings.Contains(dataStr, "MIME-Version:") || strings.Contains(dataStr, "From:") {
				if !strings.HasSuffix(strings.ToLower(att.Filename), ".eml") {
					att.Filename += ".eml"
				}
			} else {
				// Try to decode as Base64
				// Clean up the base64 string: remove newlines and spaces
				base64Str := strings.Map(func(r rune) rune {
					if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
						return -1
					}
					return r
				}, dataStr)

				// Try standard decoding
				decoded, err := base64.StdEncoding.DecodeString(base64Str)
				if err != nil {
					// Try raw decoding (no padding)
					decoded, err = base64.RawStdEncoding.DecodeString(base64Str)
				}

				if err == nil {
					att.Data = decoded
					if !strings.HasSuffix(strings.ToLower(att.Filename), ".eml") {
						att.Filename += ".eml"
					}
				} else {
					fmt.Println("Failed to decode multipart/signed attachment:", err)
				}
			}
		}

		if att.Filename == "" {
			att.Filename = "attachment"
		}
		// Only add if we have data
		if len(att.Data) > 0 {
			email.Attachments = append(email.Attachments, *att)
		}
	}

	return email, nil
}

func processRootProperty(name string, data []byte, email *EmailData) {
	tag := name[12:16]
	typ := name[16:20]

	strVal := ""
	if typ == ptUnicode {
		strVal = decodeUTF16(data)
	} else if typ == ptString8 {
		strVal = string(data)
	}

	switch tag {
	case prSubject:
		email.Subject = strVal
	case prBody:
		if email.Body == "" { // Prefer body if not set
			email.Body = strVal
		}
	case prBodyHTML:
		email.Body = strVal // Prefer HTML
	case prSenderName:
		if email.From == "" {
			email.From = strVal
		} else {
			email.From = fmt.Sprintf("%s <%s>", strVal, email.From)
		}
	case prSenderEmail:
		if email.From == "" {
			email.From = strVal
		} else if !strings.Contains(email.From, "<") {
			email.From = fmt.Sprintf("%s <%s>", email.From, strVal)
		}
	case prDisplayTo:
		// Split by ; or similar if needed, but display string is usually one line
		email.To = splitAndTrim(strVal)
	case prDisplayCc:
		email.Cc = splitAndTrim(strVal)
	case prDisplayBcc:
		email.Bcc = splitAndTrim(strVal)
	case prClientSubmitTime:
		// Date logic to be added if struct supports it
	}

	/*
		if tag == prClientSubmitTime && typ == "0040" {
			if len(data) >= 8 {
				ft := binary.LittleEndian.Uint64(data)
				t := time.Date(1601, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(ft) * 100 * time.Nanosecond)
				email.Date = t.Format(time.RFC1123Z)
			}
		}
	*/
}

func processAttachProperty(name string, data []byte, att *EmailAttachment) {
	tag := name[12:16]
	typ := name[16:20]

	strVal := ""
	if typ == ptUnicode {
		strVal = decodeUTF16(data)
	} else if typ == ptString8 {
		strVal = string(data)
	}

	switch tag {
	case prAttachLongFilename:
		att.Filename = strVal
	case prAttachFilename:
		if att.Filename == "" {
			att.Filename = strVal
		}
	case prAttachMimeTag:
		att.ContentType = strVal
	case prAttachData:
		att.Data = data
	}
}

func decodeUTF16(b []byte) string {
	decoder := unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewDecoder()
	decoded, _, _ := transform.Bytes(decoder, b)
	// Remove null terminators if present
	return strings.TrimRight(string(decoded), "\x00")
}

func splitAndTrim(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ";")
	var res []string
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t != "" {
			res = append(res, t)
		}
	}
	return res
}
