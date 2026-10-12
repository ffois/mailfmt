package msg

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/ffois/mailfmt/internal/email"
)

// msProps builds a top-level __properties_version1.0 stream: the 32-byte
// header, then one 16-byte entry (tag, flags, 8-byte value) per property.
func msProps(entries map[uint32]uint64) []byte {
	out := make([]byte, msTopLevelHeaderSize)
	for tag, value := range entries {
		entry := make([]byte, msPropertyEntrySize)
		binary.LittleEndian.PutUint32(entry[0:], tag)
		binary.LittleEndian.PutUint32(entry[4:], 0x6) // readable | writable
		binary.LittleEndian.PutUint64(entry[8:], value)
		out = append(out, entry...)
	}
	return out
}

func msReadFixture(t *testing.T, root *cfbEntry) *email.MSData {
	t.Helper()
	data, err := MS_Read(bytes.NewReader(buildCFB(root)))
	if err != nil {
		t.Fatalf("MS_Read: %v", err)
	}
	return data
}

// toFiletime is the inverse of msFiletime.
func toFiletime(tm time.Time) uint64 {
	return uint64(tm.Unix()+11_644_473_600)*10_000_000 + uint64(tm.Nanosecond()/100)
}

// The fixed-length properties Read ignores live in the property stream:
// importance, the unsent flag and the submit time come from there.
func TestMSReadFixedProperties(t *testing.T) {
	sent := time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC)
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagSubject, propTypeString), utf16LE("RE: Preventivo"))
	root.addStream(substgName(pidTagConversationTopic, propTypeString), utf16LE("Preventivo"))
	root.addStream(msPropertiesStream, msProps(map[uint32]uint64{
		msTagImportance:       uint64(email.MSImportanceHigh),
		msTagMessageFlags:     0x8 | 0x1, // MSGFLAG_UNSENT | MSGFLAG_READ
		msTagClientSubmitTime: toFiletime(sent),
	}))

	data := msReadFixture(t, root)

	if data.Importance != email.MSImportanceHigh {
		t.Errorf("expected High importance, got %v", data.Importance)
	}
	if !data.Unsent {
		t.Error("expected the unsent flag to be read from PR_MESSAGE_FLAGS")
	}
	if want := sent.Format(time.RFC1123Z); data.Date != want {
		t.Errorf("expected date %q, got %q", want, data.Date)
	}
	if data.ConversationTopic != "Preventivo" {
		t.Errorf("expected the stored conversation topic, got %q", data.ConversationTopic)
	}
	if data.Subject != "RE: Preventivo" {
		t.Errorf("expected the plain subject to be kept, got %q", data.Subject)
	}
}

// Without a submit time the delivery time is used.
func TestMSReadFallsBackToDeliveryTime(t *testing.T) {
	received := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(msPropertiesStream, msProps(map[uint32]uint64{
		msTagMessageDeliverTime: toFiletime(received),
	}))

	if got, want := msReadFixture(t, root).Date, received.Format(time.RFC1123Z); got != want {
		t.Errorf("expected date %q, got %q", want, got)
	}
}

// A message with no property stream, or an empty one, has no fixed-length
// properties: it is Normal, sent, and undated, not an error.
func TestMSReadWithoutPropertyStream(t *testing.T) {
	for _, stream := range [][]byte{nil, {}} {
		root := &cfbEntry{name: "Root Entry"}
		root.addStream(substgName(pidTagSubject, propTypeString), utf16LE("Senza proprietà"))
		if stream != nil {
			root.addStream(msPropertiesStream, stream)
		}

		data := msReadFixture(t, root)

		if data.Importance != email.MSImportanceNormal || data.Unsent || data.Date != "" {
			t.Errorf("expected defaults, got importance=%v unsent=%v date=%q",
				data.Importance, data.Unsent, data.Date)
		}
	}
}

// A property stream too short for its header is what OLMAPI32 rejects with
// MAPI_E_CORRUPT_DATA; the whole read fails, as opening it in Outlook does.
func TestMSReadRejectsTruncatedHeader(t *testing.T) {
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(msPropertiesStream, make([]byte, msTopLevelHeaderSize-1))

	_, err := MS_Read(bytes.NewReader(buildCFB(root)))

	if !errors.Is(err, errMSCorruptProperties) {
		t.Fatalf("expected errMSCorruptProperties, got %v", err)
	}
}

// A trailing partial entry is ignored, as OLMAPI32 sizes the read in whole
// 16-byte entries.
func TestMSReadIgnoresPartialEntry(t *testing.T) {
	stream := msProps(map[uint32]uint64{msTagImportance: uint64(email.MSImportanceLow)})
	stream = append(stream, 0x03, 0x00, 0x17, 0x00) // first 4 bytes of another entry

	root := &cfbEntry{name: "Root Entry"}
	root.addStream(msPropertiesStream, stream)

	if got := msReadFixture(t, root).Importance; got != email.MSImportanceLow {
		t.Errorf("expected Low importance, got %v", got)
	}
}

// MS_Read keeps everything Read reports: the Outlook fields are additions.
func TestMSReadKeepsPlainFields(t *testing.T) {
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagSubject, propTypeString), utf16LE("Con allegato"))
	root.addStream(substgName(pidTagSenderName, propTypeString), utf16LE("Anna Ferri"))
	att := root.addStorage("__attach_version1.0_#00000000")
	att.addStream(substgName(pidTagAttachLongFilename, propTypeString), utf16LE("nota.txt"))
	att.addStream(substgName(pidTagAttachData, propTypeBinary), []byte("contenuto"))
	raw := buildCFB(root)

	plain, err := Read(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	ms, err := MS_Read(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("MS_Read: %v", err)
	}
	if ms.Subject != plain.Subject || ms.From != plain.From || len(ms.Attachments) != len(plain.Attachments) {
		t.Errorf("plain fields differ:\n plain=%+v\n    ms=%+v", *plain, ms.Data)
	}
}

// msAttachProps builds an attachment's property stream: the 8-byte header,
// then 16-byte entries.
func msAttachProps(entries map[uint32]uint64) []byte {
	out := make([]byte, msSubObjectHeaderSize)
	for tag, value := range entries {
		entry := make([]byte, msPropertyEntrySize)
		binary.LittleEndian.PutUint32(entry[0:], tag)
		binary.LittleEndian.PutUint64(entry[8:], value)
		out = append(out, entry...)
	}
	return out
}

// An attachment flagged ATT_INVISIBLE_IN_HTML is hidden when the body is
// HTML, one with PR_ATTACHMENT_HIDDEN always; an attached message is a
// storage and comes back Embedded.
func TestMSReadAttachmentVisibility(t *testing.T) {
	build := func(withHTML bool) *email.MSData {
		root := &cfbEntry{name: "Root Entry"}
		if withHTML {
			root.addStream(substgName(pidTagBodyHTML, propTypeString), utf16LE(`<img src="cid:logo">`))
		} else {
			root.addStream(substgName(pidTagBody, propTypeString), utf16LE("testo"))
		}
		inline := root.addStorage("__attach_version1.0_#00000000")
		inline.addStream(substgName(pidTagAttachLongFilename, propTypeString), utf16LE("logo.png"))
		inline.addStream(substgName(pidTagAttachContentId, propTypeString), utf16LE("<logo>"))
		inline.addStream(substgName(pidTagAttachData, propTypeBinary), []byte("PNG"))
		inline.addStream(msPropertiesStream, msAttachProps(map[uint32]uint64{msTagAttachFlags: 4 | 1}))

		hidden := root.addStorage("__attach_version1.0_#00000001")
		hidden.addStream(substgName(pidTagAttachLongFilename, propTypeString), utf16LE("nascosto.bin"))
		hidden.addStream(substgName(pidTagAttachData, propTypeBinary), []byte("x"))
		hidden.addStream(msPropertiesStream, msAttachProps(map[uint32]uint64{msTagAttachmentHidden: 1}))

		embedded := root.addStorage("__attach_version1.0_#00000002")
		embedded.addStream(substgName(msPidTagDisplayName, propTypeString), utf16LE("Re: Preventivo"))
		inner := embedded.addStorage("__substg1.0_3701000D")
		inner.addStream(substgName(pidTagSubject, propTypeString), utf16LE("Re: Preventivo"))
		return msReadFixture(t, root)
	}

	html := build(true)
	if len(html.MSAttachments) != 3 {
		t.Fatalf("expected 3 attachments, got %+v", html.MSAttachments)
	}
	if a := html.MSAttachments[0]; !a.Hidden || a.ContentID != "logo" {
		t.Errorf("expected the inline image hidden, got %+v", a)
	}
	if !html.MSAttachments[1].Hidden {
		t.Error("expected PR_ATTACHMENT_HIDDEN to hide the attachment")
	}
	if a := html.MSAttachments[2]; !a.Embedded || a.Filename != "Re: Preventivo" || a.Hidden {
		t.Errorf("expected a visible embedded message, got %+v", a)
	}
	if len(html.Attachments) != 1 || !html.HasInnerEmail {
		t.Errorf("expected only the embedded message listed, got %+v", html.Attachments)
	}

	text := build(false)
	if text.MSAttachments[0].Hidden {
		t.Error("ATT_INVISIBLE_IN_HTML must not hide an attachment of a plain text message")
	}
}

// A multipart attachment is one attachment for Outlook, not the parts EMLy
// extracts from it.
func TestMSReadKeepsMultipartAttachmentWhole(t *testing.T) {
	root := &cfbEntry{name: "Root Entry"}
	att := root.addStorage("__attach_version1.0_#00000000")
	att.addStream(substgName(pidTagAttachLongFilename, propTypeString), utf16LE("busta.eml"))
	att.addStream(substgName(pidTagAttachMimeTag, propTypeString), utf16LE("multipart/mixed"))
	att.addStream(substgName(pidTagAttachData, propTypeBinary),
		[]byte("Content-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain; name=a.txt\r\n\r\na\r\n--b--\r\n"))

	data := msReadFixture(t, root)

	if len(data.Attachments) != 1 || data.Attachments[0].Filename != "busta.eml" {
		t.Errorf("expected the attachment kept whole, got %+v", data.Attachments)
	}
}

func TestMSFiletime(t *testing.T) {
	if got := msFiletime(116_444_736_000_000_000); !got.Equal(time.Unix(0, 0)) {
		t.Errorf("expected the Unix epoch, got %v", got)
	}
}
