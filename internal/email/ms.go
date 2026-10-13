package email

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
)

// MSImportance is PR_IMPORTANCE as Outlook stores it on a message.
type MSImportance int

const (
	MSImportanceLow    MSImportance = 0
	MSImportanceNormal MSImportance = 1
	MSImportanceHigh   MSImportance = 2
)

// MSAttachment is an attachment as Outlook 2010 holds it after import: one
// per attachment object, in Outlook's order, with the properties that decide
// how it is shown.
type MSAttachment struct {
	Attachment

	// DisplayName is the name Outlook shows (PR_DISPLAY_NAME).
	DisplayName string `json:"displayName"`

	// ContentID and ContentLocation are what an HTML body refers to the
	// attachment by, without angle brackets.
	ContentID       string `json:"contentId,omitempty"`
	ContentLocation string `json:"contentLocation,omitempty"`

	// Hidden attachments are rendered inside the HTML body and left out of
	// the attachment list (ATT_INVISIBLE_IN_HTML, PR_ATTACHMENT_HIDDEN).
	Hidden bool `json:"hidden"`

	// Embedded is an attached message (ATTACH_EMBEDDED_MSG). For a .eml its
	// Data is the RFC 822 message, for a winmail.dat the nested TNEF stream,
	// for a .msg nil: the message is a storage, not a byte stream.
	Embedded bool `json:"embedded"`
}

// MSData is Data plus the properties Outlook 2010 derives while it reads a
// message and that the plain readers do not surface. The MS_ readers fill it
// following the rules observed in OUTLOOK.EXE, OLMAPI32.DLL and OUTLMIME.DLL
// (Office 14.0.7268.5000).
//
// Data.Attachments lists what Outlook shows in the attachment list, that is
// MSAttachments without the hidden ones.
type MSData struct {
	Data

	// Importance is Normal unless the message says otherwise.
	Importance MSImportance `json:"importance"`

	// ConversationTopic is the subject without its reply/forward prefix.
	ConversationTopic string `json:"conversationTopic"`

	// Unsent is MSGFLAG_UNSENT: Outlook opens such a message as a draft.
	Unsent bool `json:"unsent"`

	// MSAttachments is every attachment Outlook creates, hidden ones too.
	MSAttachments []MSAttachment `json:"msAttachments"`

	// RTFBody is the decompressed PR_RTF_COMPRESSED a winmail.dat carried,
	// which Outlook copies onto the message. Empty when there was none.
	RTFBody string `json:"rtfBody,omitempty"`

	// DroppedAttachments counts the winmail.dat records Outlook could not
	// open and left out; when it is not zero Outlook warns "Impossibile
	// aprire uno o più allegati" ("Cannot open one or more attachments").
	DroppedAttachments int `json:"droppedAttachments,omitempty"`
}

// MSResolveCIDs inlines every attachment an HTML body refers to by cid:, as
// Outlook's HTML view resolves them against the message's attachments.
func MSResolveCIDs(body string, atts []MSAttachment) string {
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

// MSPecFlags applies Build's PEC test to Outlook's attachment list. Outlook
// does not list the signature of a signed message, so a signed message with
// daticert.xml counts as a PEC as well as one listing smime.p7s.
func MSPecFlags(atts []MSAttachment, signed bool) (isPec, hasInner bool) {
	var datiCert, smime bool
	for _, a := range atts {
		name := strings.ToLower(a.Filename)
		datiCert = datiCert || name == "daticert.xml"
		smime = smime || name == "smime.p7s"
		hasInner = hasInner || strings.HasSuffix(name, ".eml") || a.Embedded
	}
	return datiCert && (smime || signed), hasInner
}

// MSFormatAddress writes a name and an address as the MS_ readers report
// them: `"Name" <addr>`, `<addr>` without a name, the bare name without an
// address. Unlike net/mail, a non-ASCII name stays UTF-8.
func MSFormatAddress(name, address string) string {
	switch {
	case address == "":
		return name
	case name == "":
		return "<" + address + ">"
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(name) + `" <` + address + ">"
}

// MSVisible returns the attachments Outlook lists: those not hidden.
func MSVisible(atts []MSAttachment) []Attachment {
	var out []Attachment
	for _, a := range atts {
		if !a.Hidden {
			out = append(out, a.Attachment)
		}
	}
	return out
}
