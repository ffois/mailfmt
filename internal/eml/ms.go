package eml

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/mail"
	"net/textproto"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/ffois/mailfmt/internal/email"
	"github.com/ffois/mailfmt/internal/mimeparse"
	"github.com/ffois/mailfmt/internal/rtf"
	"github.com/ffois/mailfmt/internal/tnef"
)

// MS_ReadFile parses the MIME message at path the way Outlook 2010 imports a
// .eml through IConverterSession::MIMEToMAPI. See MS_Parse.
func MS_ReadFile(filePath string) (*email.MSData, error) {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	return MS_Parse(raw)
}

// MS_Parse reads a MIME message with Outlook 2010's rules:
//
//   - the HTML body is GetTextBody(HTML), else the plain one, chosen as
//     MimeOLE chooses them (see msFindTextBody); a later text part in a
//     multipart/mixed is an attachment, not more body;
//   - every other non-empty part is an attachment, named as Outlook names it
//     (msNamer), with parts referenced from an HTML body by Content-ID or
//     Content-Location hidden from the attachment list but resolved in the
//     body; read receipts are dropped, delivery reports become details.txt
//     and attached messages are Embedded;
//   - winmail.dat is expanded by tnef.MS_Expand: its compressed RTF is kept
//     in RTFBody, a Report.* message takes its recipients from it, and
//     without a correlator its attachment tags are stripped from a plain
//     text body;
//   - Importance, X-Priority and X-MSMail-Priority become Importance, the
//     subject a ConversationTopic, and a non-zero X-Unsent marks a draft.
func MS_Parse(raw []byte) (*email.MSData, error) {
	parsed, err := mimeparse.ParseHeader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("failed to parse email: %w", err)
	}
	tree, err := msParseTree(raw)
	if err != nil {
		return nil, fmt.Errorf("failed to parse email: %w", err)
	}

	// Addresses, subject and date as Build reads them; body and attachments
	// are replaced below.
	data := build(parsed, func(a []email.Attachment) []email.Attachment { return a })

	htmlPart := msFindTextBody(tree, "html")
	textPart := msFindTextBody(tree, "plain")
	msMarkRendered(htmlPart)
	msMarkRendered(textPart)

	atts := msCollect(tree, htmlPart != nil)
	correlator := parsed.Header.Get("X-MS-TNEF-Correlator")
	atts, decoded := tnef.MS_Expand(atts, correlator)

	result := &email.MSData{
		Data:              *data,
		Importance:        msImportance(parsed.Header),
		ConversationTopic: msConversationTopic(data.Subject),
		Unsent:            msUnsent(parsed.Header),
		MSAttachments:     atts,
	}

	if htmlPart != nil {
		result.Body = msResolveCIDs(msBodyText(htmlPart), atts)
	} else {
		result.Body = msBodyText(textPart)
	}

	if decoded != nil {
		if len(decoded.RTFCompressed) > 0 {
			if out, err := rtf.Decompress(decoded.RTFCompressed); err == nil {
				result.RTFBody = string(out)
			}
		}
		if tree.is("multipart/report") && len(decoded.Recipients) > 0 {
			msApplyRecipients(&result.Data, decoded.Recipients)
		}
		if correlator == "" && htmlPart == nil {
			result.Body = msTNEFTag.ReplaceAllString(result.Body, "")
		}
	}

	result.Attachments = email.MSVisible(atts)
	result.IsPec, result.HasInnerEmail = msPecFlags(atts)
	return result, nil
}

// msTNEFTag is the attachment position tag ITnef::OpenTaggedBody reads and
// strips from a text body: [[ name : n in container ]].
var msTNEFTag = regexp.MustCompile(`\[\[ [^\]\r\n]*? : \d+ in [^\]\r\n]*? \]\]`)

// msResolveCIDs inlines every attachment an HTML body refers to by cid:, as
// Outlook's HTML view resolves them against the message's attachments.
func msResolveCIDs(body string, atts []email.MSAttachment) string {
	for _, a := range atts {
		if a.ContentID == "" {
			continue
		}
		mediaType := strings.TrimSpace(strings.Split(a.ContentType, ";")[0])
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}
		uri := fmt.Sprintf("data:%s;base64,%s", mediaType, base64.StdEncoding.EncodeToString(a.Data))
		re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta("cid:"+a.ContentID))
		body = re.ReplaceAllLiteralString(body, uri)
	}
	return body
}

// msApplyRecipients takes To/Cc/Bcc from a winmail.dat recipient table, which
// Outlook extracts for Report.* messages only.
func msApplyRecipients(d *email.Data, recips []tnef.MSRecipient) {
	d.To, d.Cc, d.Bcc = nil, nil, nil
	for _, r := range recips {
		entry := r.Name
		if r.Address != "" {
			entry = (&mail.Address{Name: r.Name, Address: r.Address}).String()
		}
		switch r.Type {
		case 2:
			d.Cc = append(d.Cc, entry)
		case 3:
			d.Bcc = append(d.Bcc, entry)
		default:
			d.To = append(d.To, entry)
		}
	}
}

// msPecFlags applies Build's PEC test to Outlook's attachment list.
func msPecFlags(atts []email.MSAttachment) (isPec, hasInner bool) {
	var datiCert, smime bool
	for _, a := range atts {
		name := strings.ToLower(a.Filename)
		datiCert = datiCert || name == "daticert.xml"
		smime = smime || name == "smime.p7s"
		hasInner = hasInner || strings.HasSuffix(name, ".eml") || a.Embedded
	}
	return datiCert && smime, hasInner
}

// msImportance resolves the three priority headers into PR_IMPORTANCE.
//
// Outlook lets the first valid priority header set the value and afterwards
// lets only Importance override it, so a valid Importance always wins. Among
// the other two it is the first valid one in message order; the parsed
// header keeps no order across names, so X-Priority is preferred here, which
// is what Outlook itself writes alongside X-MSMail-Priority. A message with
// no valid priority header is Normal.
func msImportance(h mail.Header) email.MSImportance {
	importance, found := email.MSImportanceNormal, false
	for _, v := range headerValues(h, "Importance") {
		if imp, ok := msParseImportance(v); ok {
			importance, found = imp, true
		}
	}
	if found {
		return importance
	}
	for _, name := range []string{"X-Priority", "X-MSMail-Priority"} {
		for _, v := range headerValues(h, name) {
			if imp, ok := msParseImportance(v); ok {
				return imp
			}
		}
	}
	return email.MSImportanceNormal
}

// msParseImportance maps one priority header value. A value starting with a
// letter must be High, Normal or Low; any other value is cut at its first
// non-digit and must then be 1 to 5, so "1 (Highest)" reads as 1 and "10"
// is rejected.
func msParseImportance(v string) (email.MSImportance, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if c := v[0]; ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') {
		switch {
		case strings.EqualFold(v, "High"):
			return email.MSImportanceHigh, true
		case strings.EqualFold(v, "Normal"):
			return email.MSImportanceNormal, true
		case strings.EqualFold(v, "Low"):
			return email.MSImportanceLow, true
		}
		return 0, false
	}
	digits := v
	if i := strings.IndexFunc(v, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
		digits = v[:i]
	}
	switch digits {
	case "1", "2":
		return email.MSImportanceHigh, true
	case "3":
		return email.MSImportanceNormal, true
	case "4", "5":
		return email.MSImportanceLow, true
	}
	return 0, false
}

// msConversationTopic strips a reply/forward prefix from subject the way
// Outlook derives PR_CONVERSATION_TOPIC: after leading white space, at most
// three characters ending in a colon, none of them a digit and no letter
// after a space ("RE:", "Fw :", "AW:"), followed by any spaces. A subject
// that starts with the colon, or has no such prefix, is kept whole.
func msConversationTopic(subject string) string {
	s := strings.TrimLeftFunc(subject, unicode.IsSpace)
	runes := []rune(s)

	sawLetter, sawSpace := false, false
	for i, r := range runes {
		if r == ':' {
			if i == 0 {
				return s
			}
			return strings.TrimLeft(string(runes[i+1:]), " ")
		}
		if i == 3 {
			return s
		}
		switch {
		case r == ' ':
			if !sawLetter {
				return s
			}
			sawSpace = true
		case '0' <= r && r <= '9':
			return s
		default:
			if sawSpace {
				return s
			}
			sawLetter = true
		}
	}
	return s
}

// msUnsent reads X-Unsent as an integer, as Outlook does; the last header
// wins and a value that is not a number leaves the flag clear.
func msUnsent(h mail.Header) bool {
	values := headerValues(h, "X-Unsent")
	if len(values) == 0 {
		return false
	}
	n, err := strconv.Atoi(strings.TrimSpace(values[len(values)-1]))
	return err == nil && n != 0
}

// headerValues returns every value of the header name, in message order.
func headerValues(h mail.Header, name string) []string {
	return h[textproto.CanonicalMIMEHeaderKey(name)]
}
