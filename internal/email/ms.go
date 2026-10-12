package email

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
