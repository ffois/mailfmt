package eml

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/ffois/mailfmt/internal/email"
)

// msMessage assembles a multipart message from already formatted parts.
func msMessage(headers, contentType string, parts ...string) []byte {
	var b strings.Builder
	b.WriteString("From: a@example.it\r\nTo: b@example.it\r\nSubject: Prova\r\nMIME-Version: 1.0\r\n")
	b.WriteString(headers)
	b.WriteString("Content-Type: " + contentType + "; boundary=\"B\"\r\n\r\n")
	for _, p := range parts {
		b.WriteString("--B\r\n" + p + "\r\n")
	}
	b.WriteString("--B--\r\n")
	return []byte(b.String())
}

// msNested wraps parts in a multipart part with its own boundary.
func msNested(contentType, boundary string, parts ...string) string {
	var b strings.Builder
	b.WriteString("Content-Type: " + contentType + "; boundary=\"" + boundary + "\"\r\n\r\n")
	for _, p := range parts {
		b.WriteString("--" + boundary + "\r\n" + p + "\r\n")
	}
	b.WriteString("--" + boundary + "--")
	return b.String()
}

func msParse(t *testing.T, raw []byte) *email.MSData {
	t.Helper()
	d, err := MS_Parse(raw)
	if err != nil {
		t.Fatalf("MS_Parse: %v", err)
	}
	return d
}

func msNames(atts []email.MSAttachment) []string {
	var out []string
	for _, a := range atts {
		out = append(out, a.Filename)
	}
	return out
}

const (
	msText = "Content-Type: text/plain; charset=utf-8\r\n\r\ncorpo del messaggio"
	msPDF  = "Content-Type: application/pdf; name=\"relazione.pdf\"\r\n" +
		"Content-Disposition: attachment; filename=\"relazione.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\nJVBERi0xLjQ="
)

// In multipart/mixed only the first text part is the body; a second one is
// an attachment, and one without a name gets the numbered untitled name with
// the extension Windows registers for its type.
func TestMSMixedSecondTextIsAttachment(t *testing.T) {
	raw := msMessage("", "multipart/mixed",
		msText,
		"Content-Type: text/plain\r\n\r\nfirma aggiunta dal server",
		msPDF)

	d := msParse(t, raw)

	if d.Body != "corpo del messaggio" {
		t.Errorf("unexpected body %q", d.Body)
	}
	got := msNames(d.MSAttachments)
	want := []string{"Allegato senza titolo 00000.txt", "relazione.pdf"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("expected %v, got %v", want, got)
	}
}

// The text and HTML alternatives are both body; an image the HTML refers to
// by Content-ID is hidden from the list and inlined, a real attachment is not.
func TestMSAlternativeRelatedInline(t *testing.T) {
	html := "Content-Type: text/html; charset=utf-8\r\n\r\n<p>ciao</p><img src=\"cid:logo1\">"
	img := "Content-Type: image/png\r\nContent-ID: <logo1>\r\nContent-Transfer-Encoding: base64\r\n\r\niVBORw0KGgo="
	raw := msMessage("", "multipart/mixed",
		msNested("multipart/alternative", "ALT",
			msText,
			msNested("multipart/related", "REL", html, img)),
		msPDF)

	d := msParse(t, raw)

	if !strings.Contains(d.Body, "<p>ciao</p>") || !strings.Contains(d.Body, "data:image/png;base64,") {
		t.Errorf("expected the HTML body with the image inlined, got %q", d.Body)
	}
	if len(d.MSAttachments) != 2 {
		t.Fatalf("expected the image and the PDF, got %v", msNames(d.MSAttachments))
	}
	if img := d.MSAttachments[0]; !img.Hidden || img.ContentID != "logo1" || img.Filename != "Allegato senza titolo 00000.png" {
		t.Errorf("unexpected inline image: %+v", img)
	}
	if len(d.Attachments) != 1 || d.Attachments[0].Filename != "relazione.pdf" {
		t.Errorf("expected only the PDF in the visible list, got %+v", d.Attachments)
	}
}

// Without an HTML body nothing is hidden: a Content-ID alone does not make an
// attachment inline.
func TestMSContentIDWithoutHTMLIsVisible(t *testing.T) {
	img := "Content-Type: image/png; name=\"logo.png\"\r\nContent-ID: <logo1>\r\n\r\nPNG"
	d := msParse(t, msMessage("", "multipart/mixed", msText, img))

	if len(d.Attachments) != 1 || d.MSAttachments[0].Hidden {
		t.Errorf("expected a visible attachment, got %+v", d.MSAttachments)
	}
}

// A part explicitly disposed as an attachment stays visible even when the HTML
// body could refer to it.
func TestMSDispositionAttachmentStaysVisible(t *testing.T) {
	html := "Content-Type: text/html\r\n\r\n<img src=\"cid:x\">"
	img := "Content-Type: image/png\r\nContent-ID: <x>\r\nContent-Disposition: attachment; filename=\"x.png\"\r\n\r\nPNG"
	d := msParse(t, msMessage("", "multipart/related", html, img))

	if len(d.MSAttachments) != 1 || d.MSAttachments[0].Hidden {
		t.Errorf("expected a visible attachment, got %+v", d.MSAttachments)
	}
}

// Names: a name without extension gets one from the type; without a name the
// part's Content-Description is used; numbering only counts untitled parts.
func TestMSGeneratedNames(t *testing.T) {
	raw := msMessage("", "multipart/mixed",
		msText,
		"Content-Type: application/pdf; name=\"relazione\"\r\n\r\n%PDF",
		"Content-Type: image/jpeg\r\nContent-Description: Foto ufficio\r\n\r\nJPEG",
		"Content-Type: application/x-sconosciuto\r\n\r\nDATI",
		"Content-Type: text/html\r\n\r\n<p>secondo</p>")

	got := msNames(msParse(t, raw).MSAttachments)
	want := []string{"relazione.pdf", "Foto ufficio.jpg", "Allegato senza titolo 00000.dat", "Allegato senza titolo 00001.htm"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("expected %v, got %v", want, got)
	}
}

// Read receipts are not attached, delivery reports become details.txt and an
// attached message is Embedded, named after its subject.
func TestMSMessageParts(t *testing.T) {
	raw := msMessage("", "multipart/report",
		msText,
		"Content-Type: message/delivery-status\r\n\r\nReporting-MTA: dns; mx.example.it",
		"Content-Type: message/disposition-notification\r\n\r\nDisposition: manual-action/MDN-sent-manually; displayed",
		"Content-Type: message/rfc822\r\n\r\nFrom: c@example.it\r\nSubject: Messaggio originale\r\n\r\ntesto")

	d := msParse(t, raw)

	got := msNames(d.MSAttachments)
	want := []string{"details.txt", "Messaggio originale.eml"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("expected %v, got %v", want, got)
	}
	if !d.MSAttachments[1].Embedded || !d.HasInnerEmail {
		t.Errorf("expected an embedded message, got %+v", d.MSAttachments[1])
	}
}

// A single text part is the body unless disposed as something other than
// inline; then it is an attachment and there is no body.
func TestMSSinglePart(t *testing.T) {
	inline := "From: a@example.it\r\nSubject: x\r\nContent-Type: text/plain\r\n" +
		"Content-Disposition: inline; filename=\"nota.txt\"\r\n\r\ntesto"
	if d := msParse(t, []byte(inline)); d.Body != "testo" || len(d.MSAttachments) != 0 {
		t.Errorf("inline: expected the body, got body=%q atts=%v", d.Body, msNames(d.MSAttachments))
	}

	attached := "From: a@example.it\r\nSubject: x\r\nContent-Type: text/plain\r\n" +
		"Content-Disposition: attachment; filename=\"nota.txt\"\r\n\r\ntesto"
	if d := msParse(t, []byte(attached)); d.Body != "" || len(d.Attachments) != 1 || d.Attachments[0].Filename != "nota.txt" {
		t.Errorf("attachment: expected no body and nota.txt, got body=%q atts=%v", d.Body, msNames(d.MSAttachments))
	}
}

// A multipart/related root names its body by the start parameter.
func TestMSRelatedStart(t *testing.T) {
	raw := []byte("From: a@example.it\r\nSubject: x\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/related; boundary=\"B\"; start=\"<corpo@x>\"\r\n\r\n" +
		"--B\r\nContent-Type: text/html\r\nContent-ID: <altro@x>\r\n\r\n<p>non io</p>\r\n" +
		"--B\r\nContent-Type: text/html\r\nContent-ID: <corpo@x>\r\n\r\n<p>io</p>\r\n" +
		"--B--\r\n")

	if d := msParse(t, raw); !strings.Contains(d.Body, "io</p>") || strings.Contains(d.Body, "non io") {
		t.Errorf("expected the start part as body, got %q", d.Body)
	}
}

// msWinmail builds a minimal winmail.dat: optional message properties, then
// one attachment with a title and data.
func msWinmail(msgProps []byte, title, data string) []byte {
	var b bytes.Buffer
	b.Write([]byte{0x78, 0x9F, 0x3E, 0x22, 0x01, 0x00})
	attr := func(level byte, id uint16, payload []byte) {
		b.WriteByte(level)
		binary.Write(&b, binary.LittleEndian, id)
		binary.Write(&b, binary.LittleEndian, uint16(6))
		binary.Write(&b, binary.LittleEndian, uint32(len(payload)))
		b.Write(payload)
		b.Write([]byte{0, 0})
	}
	if msgProps != nil {
		attr(1, 0x9003, msgProps)
	}
	attr(2, 0x9002, make([]byte, 14))
	attr(2, 0x8010, append([]byte(title), 0))
	attr(2, 0x800F, []byte(data))
	return b.Bytes()
}

func msWinmailPart(blob []byte) string {
	return "Content-Type: application/ms-tnef; name=\"winmail.dat\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString(blob)
}

// Without a correlator the attachment tags OpenTaggedBody reads are stripped
// from a plain text body, and winmail.dat is replaced by its files.
func TestMSWinmailTaggedBody(t *testing.T) {
	body := "Content-Type: text/plain\r\n\r\nvedi allegato [[ nota.txt : 1 in winmail.dat ]] grazie"
	raw := msMessage("", "multipart/mixed", body, msWinmailPart(msWinmail(nil, "nota.txt", "appunti")))

	d := msParse(t, raw)

	if strings.Contains(d.Body, "[[") || !strings.Contains(d.Body, "vedi allegato") {
		t.Errorf("expected the tag stripped, got %q", d.Body)
	}
	if got := msNames(d.MSAttachments); len(got) != 1 || got[0] != "nota.txt" {
		t.Errorf("expected nota.txt, got %v", got)
	}
}

// For a report, the recipients come from the winmail.dat recipient table.
func TestMSWinmailReportRecipients(t *testing.T) {
	name := []byte("Anna Ferri\x00")
	row := binary.LittleEndian.AppendUint32(nil, 2)
	row = binary.LittleEndian.AppendUint32(row, 0x3001001E)
	row = binary.LittleEndian.AppendUint32(row, 1)
	row = binary.LittleEndian.AppendUint32(row, uint32(len(name)))
	row = append(row, name...)
	row = append(row, make([]byte, (4-len(name)%4)%4)...)
	row = binary.LittleEndian.AppendUint32(row, 0x0C150003)
	row = binary.LittleEndian.AppendUint32(row, 2)
	table := append(binary.LittleEndian.AppendUint32(nil, 1), row...)

	blob := msWinmail(nil, "nota.txt", "x")
	var withTable bytes.Buffer
	withTable.Write(blob[:6])
	withTable.WriteByte(1)
	binary.Write(&withTable, binary.LittleEndian, uint16(0x9004))
	binary.Write(&withTable, binary.LittleEndian, uint16(6))
	binary.Write(&withTable, binary.LittleEndian, uint32(len(table)))
	withTable.Write(table)
	withTable.Write([]byte{0, 0})
	withTable.Write(blob[6:])

	raw := msMessage("", "multipart/report", msText, msWinmailPart(withTable.Bytes()))
	d := msParse(t, raw)

	if len(d.Cc) != 1 || d.Cc[0] != "Anna Ferri" || len(d.To) != 0 {
		t.Errorf("expected the TNEF recipients, got To=%v Cc=%v", d.To, d.Cc)
	}
}
