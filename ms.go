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

// MS_ReadMsgFile parses the Outlook MSG file at path, fixed-length
// properties included.
func MS_ReadMsgFile(path string) (*MSEmailData, error) { return msg.MS_ReadFile(path) }

// MS_Read parses an Outlook MSG message from r, fixed-length properties
// included.
func MS_Read(r io.ReaderAt) (*MSEmailData, error) { return msg.MS_Read(r) }
