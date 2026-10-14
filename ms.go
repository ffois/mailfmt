package mailfmt

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ffois/mailfmt/internal/email"
	"github.com/ffois/mailfmt/internal/eml"
	"github.com/ffois/mailfmt/internal/msg"
	"github.com/ffois/mailfmt/internal/pec"
	"github.com/ffois/mailfmt/internal/tnef"
)

// The MS_ functions mirror the readers above with the behaviour of Microsoft
// Outlook 2010 (Office 14.0.7268.5000), as observed by reverse engineering
// OUTLOOK.EXE, OLMAPI32.DLL and OUTLMIME.DLL. They return MSEmailData, which
// embeds EmailData, so everything the plain readers report is still there.

// MSEmailData is EmailData plus the properties Outlook derives on import.
type MSEmailData = email.MSData

// MSAttachment is an attachment as Outlook holds it, hidden ones included.
type MSAttachment = email.MSAttachment

// MSImportance is PR_IMPORTANCE: Low, Normal or High.
type MSImportance = email.MSImportance

const (
	MSImportanceLow    = email.MSImportanceLow
	MSImportanceNormal = email.MSImportanceNormal
	MSImportanceHigh   = email.MSImportanceHigh
)

// MSBodyFormat is the format Outlook shows the body in: plain, HTML or RTF.
type MSBodyFormat = email.MSBodyFormat

const (
	MSBodyPlain = email.MSBodyPlain
	MSBodyHTML  = email.MSBodyHTML
	MSBodyRTF   = email.MSBodyRTF
)

// MS_DetectEmailFormat identifies the format the way Windows hands a file to
// Outlook: by extension, through the Outlook.File.msg.14 (OUTLOOK.EXE /f) and
// Outlook.File.eml.14 (OUTLOOK.EXE /eml) associations. The content is not
// inspected, so a .msg that is really MIME is still FormatMSG and fails to
// parse, as it does in Outlook. Any other extension is FormatUnknown.
//
// It fails only when the file cannot be found.
func MS_DetectEmailFormat(filePath string) (EmailFormat, error) {
	if _, err := os.Stat(filePath); err != nil {
		return FormatUnknown, err
	}
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".msg":
		return FormatMSG, nil
	case ".eml":
		return FormatEML, nil
	}
	return FormatUnknown, nil
}

// MS_ParseFile detects the format with MS_DetectEmailFormat and parses the
// file with the matching MS_ reader. Unlike ParseFile it does not look for a
// PEC envelope, which Outlook knows nothing about, and it refuses a file
// whose extension Outlook has no association for.
//
// The returned format is the detected one even when parsing fails, except
// when detection itself fails: then it is FormatUnknown.
func MS_ParseFile(path string) (*MSEmailData, EmailFormat, error) {
	format, err := MS_DetectEmailFormat(path)
	if err != nil {
		return nil, FormatUnknown, err
	}

	var data *MSEmailData
	switch format {
	case FormatMSG:
		data, err = MS_ReadMsgFile(path)
	case FormatEML:
		data, err = MS_ReadEmlFile(path)
	default:
		return nil, format, fmt.Errorf("mailfmt: Outlook has no reader for %q", filepath.Ext(path))
	}
	if err != nil {
		return nil, format, err
	}
	return data, format, nil
}

// MS_ReadEmlFile parses the MIME message at path as Outlook imports a .eml.
func MS_ReadEmlFile(path string) (*MSEmailData, error) { return eml.MS_ReadFile(path) }

// MS_ReadPecInnerEml reads the message inside a PEC envelope's postacert.eml
// as Outlook shows it when the attached message is opened. It fails when the
// part is missing.
func MS_ReadPecInnerEml(path string) (*MSEmailData, error) { return pec.MS_ReadInner(path) }

// MS_ReadEmlAttachedMessage reads att, an attached message among the
// MSAttachments MS_ReadEmlFile returns for the .eml at path, as Outlook shows
// it when it is opened from the attachment list. Inside a clear-signed
// message, one whose winmail.dat is decoded keeps only its attached messages.
//
// It fails for an attachment that is not an attached message in MIME form:
// one carried in a winmail.dat is a TNEF stream.
func MS_ReadEmlAttachedMessage(path string, att MSAttachment) (*MSEmailData, error) {
	if !att.Embedded || tnef.IsTNEFData(att.Data) {
		return nil, fmt.Errorf("mailfmt: %q is not an attached MIME message", att.Filename)
	}
	parent, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	return eml.MS_ParseAttached(parent, att.Data)
}

// MS_ReadAttachedMessage opens an attached message of the .eml or .msg at
// path the way Outlook shows it when it is opened from the attachment list.
// indexes is the way down: the first indexes the file's MSAttachments, the
// next that message's MSAttachments, and so on; each must be an Embedded
// attachment.
//
// The attached message is read from what carries it: a MIME part with
// eml.MS_ParseAttached (inside a clear-signed message, one whose winmail.dat
// is decoded keeps only its attached messages), a storage of the .msg with
// the rules Outlook applies to attached messages, a winmail.dat stream with
// tnef.MS_ReadMessage.
func MS_ReadAttachedMessage(path string, indexes ...int) (*MSEmailData, error) {
	format, err := MS_DetectEmailFormat(path)
	if err != nil {
		return nil, err
	}
	var node *msNode
	switch format {
	case FormatEML:
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("failed to open file: %w", err)
		}
		d, err := eml.MS_Parse(raw)
		if err != nil {
			return nil, err
		}
		node = &msNode{data: d, mime: raw}
	case FormatMSG:
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()
		m, err := msg.MS_Open(f)
		if err != nil {
			return nil, err
		}
		node = &msNode{data: m.Data, mime: m.Entity, msg: m}
	default:
		return nil, fmt.Errorf("mailfmt: Outlook has no reader for %q", filepath.Ext(path))
	}
	for depth, i := range indexes {
		if node, err = node.attached(i); err != nil {
			return nil, fmt.Errorf("mailfmt: attached message %v: %w", indexes[:depth+1], err)
		}
	}
	return node.data, nil
}

// msNode is a message on the way down MS_ReadAttachedMessage walks.
type msNode struct {
	data *email.MSData
	mime []byte         // the MIME message this is, or the S/MIME entity a .msg shows
	msg  *msg.MSMessage // set for a message stored in a .msg
}

func (n *msNode) attached(i int) (*msNode, error) {
	if i < 0 || i >= len(n.data.MSAttachments) {
		return nil, fmt.Errorf("no attachment %d", i)
	}
	a := n.data.MSAttachments[i]
	if !a.Embedded {
		return nil, fmt.Errorf("%q is not an attached message", a.Filename)
	}
	switch {
	case n.msg != nil && n.msg.Entity == nil:
		m, err := n.msg.AttachedMessage(i)
		if err != nil {
			return nil, err
		}
		return &msNode{data: m.Data, mime: m.Entity, msg: m}, nil
	case tnef.IsTNEFData(a.Data):
		d, err := tnef.MS_ReadMessage(a.Data)
		if err != nil {
			return nil, err
		}
		return &msNode{data: d}, nil
	case len(a.Data) > 0:
		d, err := eml.MS_ParseAttached(n.mime, a.Data)
		if err != nil {
			return nil, err
		}
		return &msNode{data: d, mime: a.Data}, nil
	}
	return nil, fmt.Errorf("%q has no content", a.Filename)
}

// MS_ReadMsgFile parses the Outlook MSG file at path, fixed-length
// properties included.
func MS_ReadMsgFile(path string) (*MSEmailData, error) { return msg.MS_ReadFile(path) }

// MS_Read parses an Outlook MSG message from r, fixed-length properties
// included.
func MS_Read(r io.ReaderAt) (*MSEmailData, error) { return msg.MS_Read(r) }
