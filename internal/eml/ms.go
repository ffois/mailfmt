package eml

import (
	"bytes"
	"errors"
	"fmt"
	"mime"
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
//     in RTFBody, and with a correlator HTML encapsulated in it is the body;
//     a Report.* message takes its recipients from it, and without a
//     correlator its attachment tags are stripped from a plain text body;
//   - Importance, X-Priority and X-MSMail-Priority become Importance, the
//     subject a ConversationTopic, and a non-zero X-Unsent marks a draft.
func MS_Parse(raw []byte) (*email.MSData, error) {
	p, err := msParseMessage(raw)
	if err != nil {
		return nil, err
	}
	return p.data, nil
}

// MS_ParseAttached reads data, an attached message of the MIME message
// parent, the way Outlook shows it when it is opened from the attachment
// list: as MS_Parse reads it, except that inside a clear-signed (S/MIME
// multipart/signed) parent a message whose winmail.dat was decoded keeps
// only its attached messages. Its other attachments, MIME parts and TNEF
// records alike, are not there.
//
// Measured on Outlook 2010: the same message attached to an unsigned parent
// keeps every attachment, and one without a winmail.dat keeps them in a
// signed parent too.
func MS_ParseAttached(parent, data []byte) (*email.MSData, error) {
	p, err := msParseMessage(data)
	if err != nil {
		return nil, err
	}
	if !p.tnef || !msClearSignedMessage(parent) {
		return p.data, nil
	}
	var kept []email.MSAttachment
	for _, a := range p.data.MSAttachments {
		if a.Embedded {
			kept = append(kept, a)
		}
	}
	p.data.MSAttachments = kept
	p.data.Attachments = email.MSVisible(kept)
	p.data.IsPec, p.data.HasInnerEmail = email.MSPecFlags(kept, p.signed)
	return p.data, nil
}

// msClearSignedMessage reports whether the message in raw is an S/MIME
// clear-signed one, from its header alone.
func msClearSignedMessage(raw []byte) bool {
	parsed, err := mimeparse.ParseHeader(bytes.NewReader(raw))
	if err != nil {
		return false
	}
	mediaType, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	return err == nil && strings.EqualFold(mediaType, "multipart/signed") && msPKCS7Protocol(params["protocol"])
}

func msPKCS7Protocol(protocol string) bool {
	p := strings.ToLower(protocol)
	return p == "application/pkcs7-signature" || p == "application/x-pkcs7-signature"
}

// msParsed is a message read by msParseMessage, with what MS_ParseAttached needs.
type msParsed struct {
	data   *email.MSData
	tnef   bool // a winmail.dat was decoded
	signed bool // the message is multipart/signed
}

func msParseMessage(raw []byte) (*msParsed, error) {
	parsed, err := mimeparse.ParseHeader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("failed to parse email: %w", err)
	}
	tree, err := msParseTree(raw)
	if err != nil {
		return nil, fmt.Errorf("failed to parse email: %w", err)
	}
	if err := msCheckSignature(tree); err != nil {
		return nil, err
	}

	// Addresses, subject and date as Build reads them; body and attachments
	// are replaced below, and encoded words are decoded again with every
	// charset, as MimeOLE decodes them.
	data := build(parsed, func(a []email.Attachment) []email.Attachment { return a })
	msDecodeHeaders(data, tree.header)

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
		result.Body = email.MSResolveCIDs(msStripCharsetMeta(msBodyText(htmlPart)), atts)
	} else {
		result.Body = msBodyText(textPart)
	}

	if decoded != nil {
		result.DroppedAttachments = decoded.Dropped
		if len(decoded.RTFCompressed) > 0 {
			if out, err := rtf.Decompress(decoded.RTFCompressed); err == nil {
				result.RTFBody = string(out)
				// With a correlator the container's properties become the
				// message's: HTML encapsulated in its RTF is the body.
				if correlator != "" && rtf.IsEncapsulatedHTML(out) {
					result.Body = email.MSResolveCIDs(rtf.MS_DeEncapsulateHTML(out), atts)
				}
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
	result.IsPec, result.HasInnerEmail = email.MSPecFlags(atts, tree.is("multipart/signed"))
	return &msParsed{data: result, tnef: decoded != nil, signed: tree.is("multipart/signed")}, nil
}

// msTNEFTag is the attachment position tag ITnef::OpenTaggedBody reads and
// strips from a text body: [[ name : n in container ]].
var msTNEFTag = regexp.MustCompile(`\[\[ [^\]\r\n]*? : \d+ in [^\]\r\n]*? \]\]`)

// errMSSignature is what Outlook reports, "Il sistema di sicurezza
// sottostante non è in grado di trovare il nome ID digitale", for a signed
// message whose signature is not a PKCS #7 structure; the message is not
// opened at all.
var errMSSignature = errors.New("eml: S/MIME signature is not a PKCS #7 SignedData (Outlook cannot open the message)")

// msCheckSignature refuses a multipart/signed message whose signature part
// does not hold a BER ContentInfo of type signedData, as Outlook refuses it.
// Only the structure is checked: Outlook opens messages whose signature does
// not verify, so the certificates and the digest are not looked at.
//
// BER, not DER: the signatures PEC providers send use indefinite lengths
// (30 80 … 00 00), which encoding/asn1 does not accept.
func msCheckSignature(root *msPart) error {
	if !root.is("multipart/signed") {
		return nil
	}
	if !msPKCS7Protocol(root.params["protocol"]) {
		return nil
	}
	for _, c := range root.children {
		if !msIsSignature(root, c) {
			continue
		}
		if !msIsSignedData(c.data) {
			return errMSSignature
		}
		return nil
	}
	return errMSSignature
}

// msSignedDataOID is the DER content of OID 1.2.840.113549.1.7.2.
var msSignedDataOID = []byte{0x2A, 0x86, 0x48, 0x86, 0xF7, 0x0D, 0x01, 0x07, 0x02}

// msIsSignedData reports whether b starts with a well-formed BER
// ContentInfo { signedData, [0] SignedData }, the SignedData holding a
// version, a SET of digest algorithms, an encapsulated ContentInfo,
// optional [0] certificates and [1] CRLs, and a SET of signer infos.
//
// Measured: Outlook opens a message whose SignedData has no certificate and
// no signer at all, but not one that stops after the version.
func msIsSignedData(b []byte) bool {
	tag, info, _, err := msBERElement(b, 0)
	if err != nil || tag != 0x30 {
		return false
	}
	tag, oid, rest, err := msBERElement(info, 0)
	if err != nil || tag != 0x06 || !bytes.Equal(oid, msSignedDataOID) {
		return false
	}
	tag, explicit, _, err := msBERElement(rest, 0)
	if err != nil || tag != 0xA0 {
		return false
	}
	tag, signed, _, err := msBERElement(explicit, 0)
	if err != nil || tag != 0x30 {
		return false
	}
	var tags []byte
	var encap []byte
	for len(signed) > 0 {
		t, content, next, err := msBERElement(signed, 0)
		if err != nil {
			return false
		}
		if len(tags) == 2 {
			encap = content
		}
		tags = append(tags, t)
		signed = next
	}
	if len(tags) < 4 || tags[0] != 0x02 || tags[1] != 0x31 || tags[2] != 0x30 || tags[len(tags)-1] != 0x31 {
		return false
	}
	for _, t := range tags[3 : len(tags)-1] {
		if t != 0xA0 && t != 0xA1 {
			return false
		}
	}
	t, _, _, err := msBERElement(encap, 0)
	return err == nil && t == 0x06
}

// msMaxBERDepth bounds the nesting msBERElement walks for an indefinite
// length; the signature comes from the message, so it is untrusted.
const msMaxBERDepth = 64

var errMSBER = errors.New("eml: malformed BER")

// msBERElement splits the BER element at the start of b into its first
// identifier octet, its contents and the bytes after it. An indefinite length
// (constructed elements only) runs to the end-of-contents octets, which are
// found by walking the nested elements; they are not part of the contents.
func msBERElement(b []byte, depth int) (tag byte, content, rest []byte, err error) {
	if depth > msMaxBERDepth || len(b) < 2 {
		return 0, nil, nil, errMSBER
	}
	tag, i := b[0], 1
	if tag&0x1F == 0x1F { // high tag number: base-128 digits, last one < 0x80
		for i < len(b) && b[i]&0x80 != 0 {
			i++
		}
		i++
	}
	if i >= len(b) {
		return 0, nil, nil, errMSBER
	}
	l := int(b[i])
	i++
	switch {
	case l == 0x80:
		if tag&0x20 == 0 {
			return 0, nil, nil, errMSBER
		}
		start := i
		for {
			if i+1 < len(b) && b[i] == 0 && b[i+1] == 0 {
				return tag, b[start:i], b[i+2:], nil
			}
			_, _, next, err := msBERElement(b[i:], depth+1)
			if err != nil {
				return 0, nil, nil, err
			}
			i = len(b) - len(next)
		}
	case l > 0x80:
		n := l - 0x80
		if n > 4 || i+n > len(b) {
			return 0, nil, nil, errMSBER
		}
		l = 0
		for _, d := range b[i : i+n] {
			l = l<<8 | int(d)
		}
		i += n
	}
	if l < 0 || l > len(b)-i {
		return 0, nil, nil, errMSBER
	}
	return tag, b[i : i+l], b[i+l:], nil
}

// msDecodeHeaders decodes the subject and the addresses from the raw header
// with msWordDecoder. A value Build already decoded comes out the same; one
// in a charset Build does not know is now decoded, and display names stay
// UTF-8 instead of being encoded again.
func msDecodeHeaders(d *email.Data, h textproto.MIMEHeader) {
	if s := h.Get("Subject"); s != "" {
		d.Subject = email.ConvertToUTF8(msDecodeWord(s))
	}
	parser := mail.AddressParser{WordDecoder: msWordDecoder}
	list := func(name string, fallback []string) []string {
		v := h.Get(name)
		if v == "" {
			return fallback
		}
		addrs, err := parser.ParseList(v)
		if err != nil {
			return fallback
		}
		out := make([]string, 0, len(addrs))
		for _, a := range addrs {
			out = append(out, msFormatAddress(a))
		}
		return out
	}
	if from := list("From", nil); len(from) > 0 {
		d.From = from[0]
	}
	d.To = list("To", d.To)
	d.Cc = list("Cc", d.Cc)
	d.Bcc = list("Bcc", d.Bcc)
}

// msFormatAddress writes an address as net/mail does for ASCII names,
// `"Name" <addr>` or `<addr>`, but keeps a non-ASCII name as UTF-8.
func msFormatAddress(a *mail.Address) string {
	return email.MSFormatAddress(email.ConvertToUTF8(a.Name), a.Address)
}

// msCharsetMeta matches the <meta http-equiv="Content-Type"> element, which
// Outlook removes from an imported HTML body: the body is stored in its own
// code page, so the declared charset no longer applies. Nothing around the
// element is touched.
var msCharsetMeta = regexp.MustCompile(`(?is)<meta\b[^>]*\bhttp-equiv\s*=\s*["']?content-type\b[^>]*>`)

func msStripCharsetMeta(html string) string {
	return msCharsetMeta.ReplaceAllString(html, "")
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
