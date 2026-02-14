package internal

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/teamwork/tnef"
)

func TestTNEFDiag(t *testing.T) {
	testFile := `H:\Dev\Gits\EMLy\EML_TNEF.eml`
	if _, err := os.Stat(testFile); os.IsNotExist(err) {
		t.Skip("test EML file not present")
	}

	f, _ := os.Open(testFile)
	defer f.Close()

	// Parse the PEC outer envelope
	outerEmail, err := Parse(f)
	if err != nil {
		t.Fatalf("parse outer: %v", err)
	}

	// Find postacert.eml
	var innerData []byte
	for _, att := range outerEmail.Attachments {
		if strings.Contains(strings.ToLower(att.Filename), "postacert.eml") {
			innerData, _ = io.ReadAll(att.Data)
			break
		}
	}
	if innerData == nil {
		t.Fatal("no postacert.eml found")
	}

	// Parse inner email
	innerEmail, err := Parse(bytes.NewReader(innerData))
	if err != nil {
		t.Fatalf("parse inner: %v", err)
	}

	fmt.Printf("Inner attachments: %d\n", len(innerEmail.Attachments))
	for i, att := range innerEmail.Attachments {
		data, _ := io.ReadAll(att.Data)
		fmt.Printf("  [%d] filename=%q contentType=%q size=%d\n", i, att.Filename, att.ContentType, len(data))

		if strings.ToLower(att.Filename) == "winmail.dat" ||
			strings.Contains(strings.ToLower(att.ContentType), "ms-tnef") {

			fmt.Printf("  Found TNEF! First 20 bytes: %x\n", data[:min(20, len(data))])
			fmt.Printf("  isTNEFData: %v\n", isTNEFData(data))

			decoded, err := tnef.Decode(data)
			if err != nil {
				fmt.Printf("  TNEF decode error: %v\n", err)
				continue
			}
			fmt.Printf("  TNEF Body len: %d\n", len(decoded.Body))
			fmt.Printf("  TNEF BodyHTML len: %d\n", len(decoded.BodyHTML))
			fmt.Printf("  TNEF Attachments: %d\n", len(decoded.Attachments))
			for j, ta := range decoded.Attachments {
				fmt.Printf("    [%d] title=%q size=%d\n", j, ta.Title, len(ta.Data))
			}
			fmt.Printf("  TNEF Attributes: %d\n", len(decoded.Attributes))
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
