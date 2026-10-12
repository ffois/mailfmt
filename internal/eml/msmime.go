package eml

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"path"
	"strings"

	"golang.org/x/text/encoding/htmlindex"

	"github.com/ffois/mailfmt/internal/email"
)

// This file reproduces how Outlook 2010's MIME engine (the MimeOLE copy
// inside OUTLMIME.DLL) splits a message into a body and attachments, and how
// IConverterSession::MIMEToMAPI then turns each attachment part into a MAPI
// attachment. Each rule names the function it was observed in.

// msMaxDepth bounds the part tree; MimeOLE's own recursive search stops at
// ten levels, and an attacker can nest multiparts as deep as they like.
const msMaxDepth = 20

// msUntitledFormat is the template OUTLLIBR.DLL string 0x106 holds for a part
// without any name, as installed here (Italian, LCID 1040). The %s is a
// five-digit counter.
const msUntitledFormat = "Allegato senza titolo %s"

// msPart is one node of the MIME tree.
type msPart struct {
	header     textproto.MIMEHeader
	mediaType  string // lower case, "text/plain" when absent
	params     map[string]string
	disp       string // lower case, "" when absent
	dispParams map[string]string
	children   []*msPart
	parent     *msPart
	data       []byte // decoded content of a leaf
	rendered   bool   // att:rendered, set on the parts chosen as body
}

func (p *msPart) multipart() bool { return strings.HasPrefix(p.mediaType, "multipart/") }

func (p *msPart) is(mediaType string) bool { return p.mediaType == mediaType }

func (p *msPart) typ() (string, string) {
	t, s, _ := strings.Cut(p.mediaType, "/")
	return t, s
}

// msParseTree reads a whole message into a part tree.
func msParseTree(raw []byte) (*msPart, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(msg.Body)
	if err != nil {
		return nil, err
	}
	return msNewPart(textproto.MIMEHeader(msg.Header), body, nil, 0), nil
}

func msNewPart(h textproto.MIMEHeader, body []byte, parent *msPart, depth int) *msPart {
	p := &msPart{header: h, parent: parent, mediaType: "text/plain", params: map[string]string{}}
	if ct := h.Get("Content-Type"); ct != "" {
		if mt, params, err := mime.ParseMediaType(ct); err == nil {
			p.mediaType, p.params = strings.ToLower(mt), params
		} else if mt := strings.TrimSpace(strings.Split(ct, ";")[0]); strings.Contains(mt, "/") {
			p.mediaType = strings.ToLower(mt)
		}
	}
	if cd := h.Get("Content-Disposition"); cd != "" {
		if d, params, err := mime.ParseMediaType(cd); err == nil {
			p.disp, p.dispParams = strings.ToLower(d), params
		} else {
			p.disp = strings.ToLower(strings.TrimSpace(strings.Split(cd, ";")[0]))
		}
	}

	if p.multipart() && depth < msMaxDepth && p.params["boundary"] != "" {
		mr := multipart.NewReader(bytes.NewReader(body), p.params["boundary"])
		for {
			part, err := mr.NextRawPart()
			if err != nil {
				break
			}
			content, err := io.ReadAll(part)
			if err != nil {
				break
			}
			p.children = append(p.children, msNewPart(part.Header, content, p, depth+1))
		}
		return p
	}
	if p.multipart() {
		// A multipart Outlook cannot open is kept as one opaque leaf.
		p.mediaType = "application/octet-stream"
	}
	p.data = msDecodeTransfer(body, h.Get("Content-Transfer-Encoding"))
	return p
}

func msDecodeTransfer(body []byte, encoding string) []byte {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		clean := bytes.Map(func(r rune) rune {
			if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
				return -1
			}
			return r
		}, body)
		if out, err := base64.StdEncoding.DecodeString(string(clean)); err == nil {
			return out
		}
		if out, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(string(clean), "=")); err == nil {
			return out
		}
	case "quoted-printable":
		if out, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(body))); err == nil {
			return out
		}
	}
	return body
}

// msWalk visits leaves and containers in document order.
func msWalk(p *msPart, fn func(*msPart)) {
	fn(p)
	for _, c := range p.children {
		msWalk(c, fn)
	}
}

// msIsAttachmentType is MimeOLE's IBT_ATTACHMENT test (FUN_3fe31a11):
// everything is an attachment except containers, vCards, S/MIME payloads and
// signatures, and plain/HTML/enriched text that is neither disposed as an
// attachment nor named.
func msIsAttachmentType(p *msPart) bool {
	if p.multipart() {
		return false
	}
	t, s := p.typ()
	switch {
	case t == "text" && s == "x-vcard":
		return false
	case t == "application" && (s == "pkcs7-mime" || s == "x-pkcs7-mime"):
		return false
	case s == "pkcs7-signature" || s == "x-pkcs7-signature":
		return false
	case t == "text" && (s == "plain" || s == "html" || s == "enriched"):
		return p.disp == "attachment" || p.dispParams["filename"] != "" || p.params["name"] != ""
	}
	return true
}

// msFindTextBody is IMimeMessage::GetTextBody (FUN_3fe2880c): a
// multipart/related root with a start parameter names its body by
// Content-ID; otherwise the tree is searched for text/<sub>, and for HTML
// text/enriched is accepted when no HTML is found. The chosen part must not
// be empty.
func msFindTextBody(root *msPart, sub string) *msPart {
	var found *msPart
	if root.is("multipart/related") && root.params["start"] != "" {
		start := strings.Trim(root.params["start"], "<>")
		msWalk(root, func(p *msPart) {
			if found == nil && strings.EqualFold(strings.Trim(p.header.Get("Content-ID"), "<> "), start) {
				found = p
			}
		})
	}
	if found == nil {
		found, _ = msSearchText(root, root, sub, 0)
	}
	if found == nil && sub == "html" {
		found, _ = msSearchText(root, root, "enriched", 0)
	}
	if found == nil || found.multipart() || len(found.data) == 0 {
		return nil
	}
	return found
}

// msSearchText is FUN_3fe285e4. A leaf text/<sub> is the body unless it is an
// attachment, except when it is the whole message and not disposed other than
// inline. Children are searched in order; inside multipart/mixed the search
// stops after the first child that holds a plain or HTML part of the other
// kind, so a later text part cannot become the body. sawText reports such a
// part, as the original does for the last child it visited.
func msSearchText(root, p *msPart, sub string, depth int) (found *msPart, sawText bool) {
	if !p.multipart() {
		t, s := p.typ()
		if t != "text" {
			return nil, false
		}
		if s == sub {
			if !msIsAttachmentType(p) || (p == root && (p.disp == "" || p.disp == "inline")) {
				return p, false
			}
			return nil, false
		}
		return nil, s == "plain" || s == "html"
	}
	for _, c := range p.children {
		f, saw := msSearchText(root, c, sub, depth+1)
		if f != nil {
			return f, saw
		}
		sawText = saw
		if saw && p.is("multipart/mixed") && depth < 10 {
			break
		}
	}
	return nil, sawText
}

// msMarkRendered flags the body part, and every alternative to it, so that
// GetAttachments leaves them out (FUN_3fe2880c).
func msMarkRendered(body *msPart) {
	if body == nil {
		return
	}
	body.rendered = true
	for p := body.parent; p != nil; p = p.parent {
		if p.is("multipart/alternative") {
			for _, c := range p.children {
				c.rendered = true
			}
			return
		}
	}
}

// msNamer generates attachment names. Outlook's counter is per process and
// starts at zero; here it starts at zero for every message, which is what a
// fresh OUTLOOK.EXE /eml produces.
type msNamer struct{ counter int }

// filename is att:generated-filename (FUN_3fe3bcf0, FUN_3fe3bae7,
// FUN_3fe35b13, FUN_3fe3ecbb): the declared filename, else the Content-Type
// name, given an extension when it has none; without either, the part's
// subject, else its Content-Description, else the numbered untitled
// template, plus the extension for the content type.
func (n *msNamer) filename(p *msPart, innerSubject string) string {
	name := msDecodeWord(p.dispParams["filename"])
	if name == "" {
		name = msDecodeWord(p.params["name"])
	}
	if name != "" {
		if path.Ext(strings.ReplaceAll(name, `\`, "/")) != "" {
			return name
		}
		return name + msExtension(p.mediaType)
	}

	base := innerSubject
	if base == "" {
		base = msDecodeWord(p.header.Get("Subject"))
	}
	if base == "" {
		base = msDecodeWord(p.header.Get("Content-Description"))
	}
	if base == "" {
		base = fmt.Sprintf(msUntitledFormat, fmt.Sprintf("%05d", n.counter))
		n.counter++
	}
	return base + msExtension(p.mediaType)
}

// msExtension is the extension FUN_3fe3ecbb appends: the one Windows
// registers for the content type, else .eml for a message, .txt for text and
// disposition notifications, .dat for anything else.
func msExtension(mediaType string) string {
	if mediaType == "message/rfc822" {
		return ".eml"
	}
	if ext, ok := msRegisteredExtensions[mediaType]; ok {
		return ext
	}
	if strings.HasPrefix(mediaType, "text/") || mediaType == "message/disposition-notification" {
		return ".txt"
	}
	return ".dat"
}

func msDecodeWord(s string) string {
	if s == "" {
		return ""
	}
	if out, err := new(mime.WordDecoder).DecodeHeader(s); err == nil {
		return out
	}
	return s
}

// msCollect is GetAttachments (FUN_3fe23d06) followed by the per-part
// conversion (FUN_3fe703d3, FUN_3fe6fc0d, FUN_3fe6dac8): every non-empty
// leaf not chosen as body becomes an attachment, in document order, except
// read receipts.
func msCollect(root *msPart, hasHTML bool) []email.MSAttachment {
	var namer msNamer
	var out []email.MSAttachment

	msWalk(root, func(p *msPart) {
		if p.multipart() || p.rendered || len(p.data) == 0 {
			return
		}
		if p.is("message/disposition-notification") {
			return // handled as a read receipt, not attached
		}

		att := email.MSAttachment{}
		att.ContentType = p.mediaType
		att.Data = p.data

		switch {
		case p.is("message/delivery-status"):
			att.Filename = "details.txt"
		case p.is("message/rfc822"):
			att.Embedded = true
			att.Filename = namer.filename(p, msInnerSubject(p.data))
		default:
			att.Filename = namer.filename(p, "")
		}
		att.DisplayName = att.Filename

		att.ContentID, att.ContentLocation = msReferences(p)
		if hasHTML && p.disp != "attachment" && (att.ContentID != "" || att.ContentLocation != "") {
			att.Hidden = true // PR_ATTACH_FLAGS = ATT_INVISIBLE_IN_HTML | ATT_MHTML_REF
		}
		out = append(out, att)
	})
	return out
}

// msReferences returns the Content-ID and Content-Location that apply to a
// part: its own, or the nearest ancestor's (FUN_3fe6dac8).
func msReferences(p *msPart) (cid, location string) {
	for q := p; q != nil && cid == ""; q = q.parent {
		cid = strings.TrimSpace(strings.Trim(strings.TrimSpace(q.header.Get("Content-ID")), "<>"))
	}
	for q := p; q != nil && location == ""; q = q.parent {
		location = strings.TrimSpace(q.header.Get("Content-Location"))
	}
	return cid, location
}

func msInnerSubject(raw []byte) string {
	tp := textproto.NewReader(bufio.NewReader(bytes.NewReader(raw)))
	h, err := tp.ReadMIMEHeader()
	if err != nil && len(h) == 0 {
		return ""
	}
	return msDecodeWord(h.Get("Subject"))
}

// msBodyText decodes a body part to UTF-8 according to its charset.
func msBodyText(p *msPart) string {
	if p == nil {
		return ""
	}
	if cs := p.params["charset"]; cs != "" {
		if enc, err := htmlindex.Get(cs); err == nil {
			if out, err := enc.NewDecoder().Bytes(p.data); err == nil {
				return string(out)
			}
		}
	}
	return email.ConvertToUTF8(string(p.data))
}
