package msg

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ffois/mailfmt/internal/email"
	"github.com/ffois/mailfmt/internal/rtf"
)

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

// replaceStream swaps the data of e's stream called name.
func replaceStream(e *cfbEntry, name string, data []byte) {
	e.removeChild(name)
	e.addStream(name, data)
}

// setNextIDs rewrites the next recipient and attachment IDs of a message's
// property stream.
func setNextIDs(e *cfbEntry, recip, attach uint32) {
	stream := findChild(e, msPropertiesStream)
	binary.LittleEndian.PutUint32(stream.data[8:], recip)
	binary.LittleEndian.PutUint32(stream.data[12:], attach)
}

// The fixed-length properties Read ignores live in the property stream:
// importance, the unsent flag and the submit time come from there.
func TestMSReadFixedProperties(t *testing.T) {
	sent := time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC)
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagSubject, propTypeString), utf16LE("RE: Preventivo"))
	root.addStream(substgName(pidTagConversationTopic, propTypeString), utf16LE("Preventivo"))
	outlookMSG(root, msgFixed{root: {
		msTagImportance:       uint64(email.MSImportanceHigh),
		msTagMessageFlags:     0x8 | 0x1, // MSGFLAG_UNSENT | MSGFLAG_READ
		msTagClientSubmitTime: toFiletime(sent),
	}})

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
		t.Errorf("expected the subject, got %q", data.Subject)
	}
}

// Without a submit time the delivery time is used; with neither the message
// is Normal, sent and undated.
func TestMSReadDates(t *testing.T) {
	received := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	root := &cfbEntry{name: "Root Entry"}
	outlookMSG(root, msgFixed{root: {msTagMessageDeliverTime: toFiletime(received)}})

	if got, want := msReadFixture(t, root).Date, received.Format(time.RFC1123Z); got != want {
		t.Errorf("expected date %q, got %q", want, got)
	}

	data := msReadFixture(t, outlookMSG(&cfbEntry{name: "Root Entry"}, nil))
	if data.Importance != email.MSImportanceNormal || data.Unsent || data.Date != "" {
		t.Errorf("expected defaults, got importance=%v unsent=%v date=%q", data.Importance, data.Unsent, data.Date)
	}
}

// Each structure Outlook 2010 needs, removed in turn, makes it refuse the
// file; MS_Read refuses it the same way.
func TestMSReadRefusesWhatOutlookRefuses(t *testing.T) {
	build := func(edit func(root, att *cfbEntry)) []byte {
		root := &cfbEntry{name: "Root Entry"}
		root.addStream(substgName(pidTagSubject, propTypeString), utf16LE("Oggetto"))
		att := root.addStorage("__attach_version1.0_#00000000")
		att.addStream(substgName(pidTagAttachLongFilename, propTypeString), utf16LE("nota.txt"))
		att.addStream(substgName(pidTagAttachData, propTypeBinary), []byte("contenuto"))
		outlookMSG(root, nil)
		if edit != nil {
			edit(root, att)
		}
		return buildCFB(root)
	}
	// A Unicode message's property stream that lists nothing else.
	header := func(attach uint32) []byte {
		return propertyStream(&cfbEntry{}, msTopLevelHeaderSize, 0, attach,
			map[uint32]uint64{msTagStoreSupportMask: msStoreUnicodeOK})
	}

	cases := []struct {
		name string
		raw  []byte
		want error
	}{
		{"complete", build(nil), nil},
		{"directory sectors in a version 3 header", func() []byte {
			raw := build(nil)
			binary.LittleEndian.PutUint32(raw[40:], 1)
			return raw
		}(), errMSDirectorySectors},
		{"no property stream", build(func(root, _ *cfbEntry) { root.removeChild(msPropertiesStream) }), errMSNoProperties},
		{"property stream shorter than its header", build(func(root, _ *cfbEntry) {
			replaceStream(root, msPropertiesStream, make([]byte, msTopLevelHeaderSize-1))
		}), errMSCorruptProperties},
		{"attachment without a property stream", build(func(_, att *cfbEntry) { att.removeChild(msPropertiesStream) }), errMSAttachmentProperties},
		{"no named-property storage", build(func(root, _ *cfbEntry) { root.removeChild(msNameIDStorage) }), errMSNoNameID},
		{"message class not listed", build(func(root, _ *cfbEntry) { replaceStream(root, msPropertiesStream, header(1)) }), errMSNoMessageClass},
	}
	for _, tc := range cases {
		_, err := MS_Read(bytes.NewReader(tc.raw))
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: expected %v, got %v", tc.name, tc.want, err)
		}
	}
}

// A trailing partial entry is ignored, as OLMAPI32 sizes the read in whole
// 16-byte entries.
func TestMSReadIgnoresPartialEntry(t *testing.T) {
	root := &cfbEntry{name: "Root Entry"}
	outlookMSG(root, msgFixed{root: {msTagImportance: uint64(email.MSImportanceLow)}})
	stream := findChild(root, msPropertiesStream)
	stream.data = append(stream.data, 0x03, 0x00, 0x17, 0x00) // first 4 bytes of another entry

	if got := msReadFixture(t, root).Importance; got != email.MSImportanceLow {
		t.Errorf("expected Low importance, got %v", got)
	}
}

// A property is what the property stream lists: a __substg1.0_ stream it
// does not list is not seen, as Outlook does not see it, while Read takes it.
func TestMSReadListedPropertiesOnly(t *testing.T) {
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagConversationTopic, propTypeString), utf16LE("Preventivo"))
	outlookMSG(root, nil)
	root.addStream(substgName(pidTagSubject, propTypeString), utf16LE("Non elencato"))
	raw := buildCFB(root)

	ms, err := MS_Read(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("MS_Read: %v", err)
	}
	if ms.Subject != "" || ms.ConversationTopic != "Preventivo" {
		t.Errorf("expected only the listed topic, got subject %q, topic %q", ms.Subject, ms.ConversationTopic)
	}
	plain, err := Read(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if plain.Subject != "Non elencato" {
		t.Errorf("expected Read to keep the stream, got %q", plain.Subject)
	}
}

// Attachments are those numbered below the next attachment ID of the header,
// in number order, whatever the count beside it says; a gap is skipped.
func TestMSReadAttachmentNumbering(t *testing.T) {
	root := &cfbEntry{name: "Root Entry"}
	for _, n := range []string{"00000002", "00000000", "00000003"} {
		att := root.addStorage("__attach_version1.0_#" + n)
		att.addStream(substgName(pidTagAttachLongFilename, propTypeString), utf16LE(n+".txt"))
	}
	outlookMSG(root, nil)

	names := func(d *email.MSData) string {
		var out []string
		for _, a := range d.MSAttachments {
			out = append(out, a.Filename)
		}
		return strings.Join(out, " ")
	}
	if got := names(msReadFixture(t, root)); got != "00000000.txt 00000002.txt 00000003.txt" {
		t.Errorf("expected the attachments in number order, got %q", got)
	}
	setNextIDs(root, 0, 3)
	if got := names(msReadFixture(t, root)); got != "00000000.txt 00000002.txt" {
		t.Errorf("expected the attachments below the next ID, got %q", got)
	}
}

// To, Cc and Bcc come from the recipient table, not from PR_DISPLAY_TO; the
// SMTP address is preferred. One recipient without a property stream and the
// message has none.
func TestMSReadRecipients(t *testing.T) {
	build := func() (*cfbEntry, *cfbEntry) {
		root := &cfbEntry{name: "Root Entry"}
		root.addStream(substgName(pidTagDisplayTo, propTypeString), utf16LE("Anna Ferri"))
		r0 := root.addStorage("__recip_version1.0_#00000000")
		r0.addStream(substgName(msPidTagDisplayName, propTypeString), utf16LE("Anna Ferri"))
		r0.addStream(substgName(msPidTagEmailAddress, propTypeString), utf16LE("/o=Esempio/cn=Recipients/cn=anna"))
		r0.addStream(substgName(msPidTagSMTPAddress, propTypeString), utf16LE("anna.ferri@example.it"))
		r1 := root.addStorage("__recip_version1.0_#00000001")
		r1.addStream(substgName(msPidTagDisplayName, propTypeString), utf16LE("Ufficio \"Acquisti\""))
		r1.addStream(substgName(msPidTagEmailAddress, propTypeString), utf16LE("acquisti@example.it"))
		r2 := root.addStorage("__recip_version1.0_#00000002")
		r2.addStream(substgName(msPidTagEmailAddress, propTypeString), utf16LE("archivio@example.it"))
		return outlookMSG(root, msgFixed{
			r1: {msTagRecipientType: msRecipCc},
			r2: {msTagRecipientType: msRecipBcc | 0x10000000}, // MAPI_P1 flag
		}), r1
	}

	root, _ := build()
	data := msReadFixture(t, root)
	if got := strings.Join(data.To, "; "); got != `"Anna Ferri" <anna.ferri@example.it>` {
		t.Errorf("To: got %q", got)
	}
	if got := strings.Join(data.Cc, "; "); got != `"Ufficio \"Acquisti\"" <acquisti@example.it>` {
		t.Errorf("Cc: got %q", got)
	}
	if got := strings.Join(data.Bcc, "; "); got != "<archivio@example.it>" {
		t.Errorf("Bcc: got %q", got)
	}

	root, r1 := build()
	r1.removeChild(msPropertiesStream)
	if data := msReadFixture(t, root); data.To != nil || data.Cc != nil || data.Bcc != nil {
		t.Errorf("expected no recipients, got %q %q %q", data.To, data.Cc, data.Bcc)
	}
}

// An attachment flagged ATT_INVISIBLE_IN_HTML is hidden when the body is
// HTML, one with PR_ATTACHMENT_HIDDEN always. An attached message is a
// storage and comes back Embedded, named after PR_DISPLAY_NAME alone, with
// the file name Outlook derives from that.
func TestMSReadAttachmentVisibility(t *testing.T) {
	build := func(withHTML bool) *email.MSData {
		root := &cfbEntry{name: "Root Entry"}
		if withHTML {
			root.addStream(substgName(pidTagBodyHTML, propTypeBinary), []byte(`<img src="cid:logo">`))
		} else {
			root.addStream(substgName(pidTagBody, propTypeString), utf16LE("testo"))
		}
		inline := root.addStorage("__attach_version1.0_#00000000")
		inline.addStream(substgName(pidTagAttachLongFilename, propTypeString), utf16LE("logo.png"))
		inline.addStream(substgName(pidTagAttachContentId, propTypeString), utf16LE("<logo>"))
		inline.addStream(substgName(pidTagAttachMimeTag, propTypeString), utf16LE("image/png"))
		inline.addStream(substgName(pidTagAttachData, propTypeBinary), []byte("PNG"))

		hidden := root.addStorage("__attach_version1.0_#00000001")
		hidden.addStream(substgName(pidTagAttachLongFilename, propTypeString), utf16LE("nascosto.bin"))
		hidden.addStream(substgName(pidTagAttachData, propTypeBinary), []byte("x"))

		named := root.addStorage("__attach_version1.0_#00000002")
		named.addStream(substgName(msPidTagDisplayName, propTypeString), utf16LE("Re: Preventivo"))
		named.addStorage("__substg1.0_3701000D").addStream(substgName(pidTagSubject, propTypeString), utf16LE("Re: Preventivo"))

		unnamed := root.addStorage("__attach_version1.0_#00000003")
		unnamed.addStorage("__substg1.0_3701000D").addStream(substgName(pidTagSubject, propTypeString), utf16LE("Offerta"))

		return msReadFixture(t, outlookMSG(root, msgFixed{
			inline: {msTagAttachFlags: 4 | 1},
			hidden: {msTagAttachmentHidden: 1},
		}))
	}

	html := build(true)
	if len(html.MSAttachments) != 4 {
		t.Fatalf("expected 4 attachments, got %+v", html.MSAttachments)
	}
	if a := html.MSAttachments[0]; !a.Hidden || a.ContentID != "logo" {
		t.Errorf("expected the inline image hidden, got %+v", a)
	}
	if html.Body != `<img src="data:image/png;base64,UE5H">` {
		t.Errorf("expected the cid: reference resolved, got %q", html.Body)
	}
	if !html.MSAttachments[1].Hidden {
		t.Error("expected PR_ATTACHMENT_HIDDEN to hide the attachment")
	}
	if a := html.MSAttachments[2]; !a.Embedded || a.Hidden || a.DisplayName != "Re: Preventivo" || a.Filename != "Re Preventivo.msg" {
		t.Errorf("expected a visible attached message named after its display name, got %+v", a)
	}
	if a := html.MSAttachments[3]; !a.Embedded || a.DisplayName != "" || a.Filename != ".msg" {
		t.Errorf("expected an unnamed attached message, its subject unused, got %+v", a)
	}
	if len(html.Attachments) != 2 || !html.HasInnerEmail {
		t.Errorf("expected only the attached messages listed, got %+v", html.Attachments)
	}

	text := build(false)
	if text.MSAttachments[0].Hidden {
		t.Error("ATT_INVISIBLE_IN_HTML must not hide an attachment of a plain text message")
	}
}

// Attachments are shown only when the message says it has some, through
// PR_HASATTACH or MSGFLAG_HASATTACH; they are loaded either way.
func TestMSReadHasAttach(t *testing.T) {
	build := func(values map[uint32]uint64) *email.MSData {
		root := &cfbEntry{name: "Root Entry"}
		att := root.addStorage("__attach_version1.0_#00000000")
		att.addStream(substgName(pidTagAttachLongFilename, propTypeString), utf16LE("nota.txt"))
		return msReadFixture(t, outlookMSG(root, msgFixed{root: values}))
	}
	if d := build(map[uint32]uint64{msTagHasAttach: 0}); len(d.MSAttachments) != 0 || len(d.Attachments) != 0 {
		t.Errorf("expected no attachments without PR_HASATTACH, got %+v", d.MSAttachments)
	}
	if d := build(map[uint32]uint64{msTagHasAttach: 0, msTagMessageFlags: msMsgFlagHasAttach}); len(d.MSAttachments) != 1 {
		t.Errorf("expected MSGFLAG_HASATTACH to show the attachment, got %+v", d.MSAttachments)
	}
}

// A message is ANSI or Unicode, after STORE_UNICODE_OK, and a string of the
// other kind makes Outlook refuse it, unless it is in a recipient: then the
// message has no recipients.
func TestMSReadStringModes(t *testing.T) {
	unicodeOK := map[uint32]uint64{msTagStoreSupportMask: msStoreUnicodeOK}

	ansiSubject := &cfbEntry{name: "Root Entry"}
	ansiSubject.addStream(substgName(pidTagSubject, propTypeString8), []byte("Perch\xe9 no"))
	if d := msReadFixture(t, outlookMSG(ansiSubject, nil)); d.Subject != "Perché no" {
		t.Errorf("expected an ANSI message read, got subject %q", d.Subject)
	}

	inUnicode := &cfbEntry{name: "Root Entry"}
	inUnicode.addStream(substgName(pidTagSubject, propTypeString8), []byte("ANSI"))
	inANSI := &cfbEntry{name: "Root Entry"}
	inANSI.addStream(substgName(pidTagMessageClass, propTypeString8), []byte("IPM.Note"))
	inANSI.addStream(substgName(pidTagSubject, propTypeString), utf16LE("Unicode"))
	attachment := &cfbEntry{name: "Root Entry"}
	attachment.addStream(substgName(pidTagMessageClass, propTypeString), utf16LE("IPM.Note"))
	attachment.addStorage("__attach_version1.0_#00000000").addStream(substgName(pidTagAttachFilename, propTypeString8), []byte("NOTA.TXT"))
	for name, root := range map[string]*cfbEntry{
		"ANSI subject in a Unicode message":    outlookMSG(inUnicode, msgFixed{inUnicode: unicodeOK}),
		"Unicode subject in an ANSI message":   outlookMSG(inANSI, nil),
		"ANSI attachment in a Unicode message": outlookMSG(attachment, msgFixed{attachment: unicodeOK}),
	} {
		if _, err := MS_Read(bytes.NewReader(buildCFB(root))); !errors.Is(err, errMSStringType) {
			t.Errorf("%s: expected errMSStringType, got %v", name, err)
		}
	}

	recipient := &cfbEntry{name: "Root Entry"}
	recipient.addStream(substgName(pidTagMessageClass, propTypeString), utf16LE("IPM.Note"))
	recipient.addStorage("__recip_version1.0_#00000000").addStream(substgName(msPidTagEmailAddress, propTypeString8), []byte("anna@example.it"))
	if d := msReadFixture(t, outlookMSG(recipient, msgFixed{recipient: unicodeOK})); d.To != nil {
		t.Errorf("expected no recipients, got %q", d.To)
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

	data := msReadFixture(t, outlookMSG(root, nil))

	if len(data.Attachments) != 1 || data.Attachments[0].Filename != "busta.eml" {
		t.Errorf("expected the attachment kept whole, got %+v", data.Attachments)
	}
}

// A binary PR_HTML is in the code page PR_INTERNET_CPID names.
func TestMSReadHTMLCodePage(t *testing.T) {
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagBodyHTML, propTypeBinary), []byte("<p>\xcf\xf0\xe8\xe2\xe5\xf2</p>"))
	data := msReadFixture(t, outlookMSG(root, msgFixed{root: {msTagInternetCPID: 1251}}))

	if data.Body != "<p>Привет</p>" {
		t.Errorf("expected the body decoded as Windows-1251, got %q", data.Body)
	}
}

// Outlook reads PR_HTML as PT_BINARY only, so one stored as a string is not
// a body; a plain text body is returned as text.
func TestMSReadBodyTypes(t *testing.T) {
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagBodyHTML, propTypeString), utf16LE("<p>ignorato</p>"))
	root.addStream(substgName(pidTagBody, propTypeString), utf16LE("riga 1\r\nriga 2 & <3"))

	if d := msReadFixture(t, outlookMSG(root, nil)); d.Body != "riga 1\r\nriga 2 & <3" {
		t.Errorf("expected the text body as text, got %q", d.Body)
	}
}

// HTML encapsulated in RTF is decoded with each font's charset: a byte in an
// Arabic font is Windows-1256.
func TestMSReadRTFFontCharset(t *testing.T) {
	payload := []byte(`{\rtf1\ansi\ansicpg1252\fromhtml1 \deff0{\fonttbl` +
		`{\f0\fswiss\fcharset0 Arial;}{\f6\fswiss\fcharset178 Arial;}}` +
		"\r\n" + `{\*\htmltag64 <p>}a{\f6 \'9d}b{\*\htmltag72 </p>}}`)
	stream := make([]byte, 16+len(payload))
	binary.LittleEndian.PutUint32(stream[0:], uint32(len(payload)+12))
	binary.LittleEndian.PutUint32(stream[4:], uint32(len(payload)))
	binary.LittleEndian.PutUint32(stream[8:], rtf.RTFUncompressedMagic)
	copy(stream[16:], payload)

	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagRtfCompressed, propTypeBinary), stream)
	data := msReadFixture(t, outlookMSG(root, nil))

	if data.Body != "<p>a‌b</p>" {
		t.Errorf("expected U+200C from the Arabic font, got %q", data.Body)
	}
}

// A clear-signed S/MIME message keeps the whole multipart/signed entity in
// its attachment; Outlook shows that entity's body and attachments instead.
func TestMSReadMultipartSigned(t *testing.T) {
	// A BER SignedData with no certificate and no signer, which Outlook opens.
	sig := []byte{0x30, 0x80, 0x06, 0x09, 0x2A, 0x86, 0x48, 0x86, 0xF7, 0x0D, 0x01, 0x07, 0x02,
		0xA0, 0x80, 0x30, 0x80, 0x02, 0x01, 0x01, 0x31, 0x00,
		0x30, 0x80, 0x06, 0x09, 0x2A, 0x86, 0x48, 0x86, 0xF7, 0x0D, 0x01, 0x07, 0x01, 0x00, 0x00,
		0x31, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	inner := "From: mittente@example.it\r\nSubject: Referto\r\n\r\nIn allegato il referto.\r\n"
	entity := "Content-Type: multipart/signed; protocol=\"application/pkcs7-signature\"; micalg=sha-256; boundary=\"S\"\r\n\r\n" +
		"--S\r\nContent-Type: multipart/mixed; boundary=\"M\"\r\n\r\n" +
		"--M\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>Messaggio di posta certificata</p>\r\n" +
		"--M\r\nContent-Type: message/rfc822\r\nContent-Disposition: attachment; filename=\"postacert.eml\"\r\n\r\n" + inner +
		"--M\r\nContent-Type: application/xml; name=\"daticert.xml\"\r\n\r\n<postacert/>\r\n--M--\r\n" +
		"--S\r\nContent-Type: application/pkcs7-signature; name=\"smime.p7s\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		base64.StdEncoding.EncodeToString(sig) + "\r\n--S--\r\n"

	build := func(entity string) []byte {
		root := &cfbEntry{name: "Root Entry"}
		root.addStream(substgName(pidTagMessageClass, propTypeString), utf16LE("IPM.Note.SMIME.MultipartSigned"))
		root.addStream(substgName(pidTagSubject, propTypeString), utf16LE("POSTA CERTIFICATA: Referto"))
		root.addStream(substgName(pidTagBody, propTypeString), utf16LE("testo della busta"))
		att := root.addStorage("__attach_version1.0_#00000000")
		att.addStream(substgName(pidTagAttachLongFilename, propTypeString), utf16LE("smime.p7m"))
		att.addStream(substgName(pidTagAttachMimeTag, propTypeString), utf16LE("multipart/signed"))
		att.addStream(substgName(pidTagAttachData, propTypeBinary), []byte(entity))
		return buildCFB(outlookMSG(root, nil))
	}

	data, err := MS_Read(bytes.NewReader(build(entity)))
	if err != nil {
		t.Fatalf("MS_Read: %v", err)
	}
	if data.Subject != "POSTA CERTIFICATA: Referto" || !strings.Contains(data.Body, "Messaggio di posta certificata") {
		t.Errorf("expected the message's subject and the entity's body, got %q / %q", data.Subject, data.Body)
	}
	var names []string
	for _, a := range data.MSAttachments {
		names = append(names, a.Filename)
	}
	if got := strings.Join(names, " | "); !strings.HasPrefix(got, "postacert.eml (") || !strings.HasSuffix(got, ").msg | daticert.xml") {
		t.Errorf("expected the entity's attachments, got %q", got)
	}
	if !data.IsPec || !data.HasInnerEmail {
		t.Errorf("expected a PEC with an inner message, got IsPec=%v HasInnerEmail=%v", data.IsPec, data.HasInnerEmail)
	}

	// A signature that is not PKCS #7 is refused, as for a .eml.
	fake := strings.Replace(entity, base64.StdEncoding.EncodeToString(sig), "RkFLRQ==", 1)
	if _, err := MS_Read(bytes.NewReader(build(fake))); err == nil {
		t.Error("expected an error for a signature that is not PKCS #7")
	}
}

// MS_Read reports what Read does for an ordinary message; the Outlook fields
// are additions, and the sender is written as the MS_ readers write it.
func TestMSReadKeepsPlainFields(t *testing.T) {
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagSubject, propTypeString), utf16LE("Con allegato"))
	root.addStream(substgName(pidTagSenderName, propTypeString), utf16LE("Anna Ferri"))
	root.addStream(substgName(pidTagSenderEmailAddress, propTypeString), utf16LE("anna.ferri@example.it"))
	root.addStream(substgName(pidTagBodyHTML, propTypeBinary), []byte("<p>Vedi allegato.</p>"))
	att := root.addStorage("__attach_version1.0_#00000000")
	att.addStream(substgName(pidTagAttachLongFilename, propTypeString), utf16LE("nota.txt"))
	att.addStream(substgName(pidTagAttachData, propTypeBinary), []byte("contenuto"))
	raw := buildCFB(outlookMSG(root, nil))

	plain, err := Read(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	ms, err := MS_Read(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("MS_Read: %v", err)
	}
	if ms.Subject != plain.Subject || ms.Body != plain.Body || len(ms.Attachments) != len(plain.Attachments) {
		t.Errorf("plain fields differ:\n plain=%+v\n    ms=%+v", *plain, ms.Data)
	}
	if plain.From != "Anna Ferri <anna.ferri@example.it>" || ms.From != `"Anna Ferri" <anna.ferri@example.it>` {
		t.Errorf("unexpected senders: plain %q, ms %q", plain.From, ms.From)
	}
}

func TestMSFiletime(t *testing.T) {
	if got := msFiletime(116_444_736_000_000_000); !got.Equal(time.Unix(0, 0)) {
		t.Errorf("expected the Unix epoch, got %v", got)
	}
}
