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

func TestTNEFAllSizes(t *testing.T) {
	testFile := `H:\Dev\Gits\EMLy\EML_TNEF.eml`
	if _, err := os.Stat(testFile); os.IsNotExist(err) {
		t.Skip("test EML file not present")
	}

	f, _ := os.Open(testFile)
	defer f.Close()

	outerEmail, _ := Parse(f)
	var innerData []byte
	for _, att := range outerEmail.Attachments {
		if strings.Contains(strings.ToLower(att.Filename), "postacert.eml") {
			innerData, _ = io.ReadAll(att.Data)
			break
		}
	}

	innerEmail, _ := Parse(bytes.NewReader(innerData))
	for _, att := range innerEmail.Attachments {
		data, _ := io.ReadAll(att.Data)
		if strings.ToLower(att.Filename) != "winmail.dat" {
			continue
		}

		decoded, _ := tnef.Decode(data)

		totalAttrSize := 0
		for _, attr := range decoded.Attributes {
			totalAttrSize += len(attr.Data)
			fmt.Printf("  Attr 0x%04X: %d bytes\n", attr.Name, len(attr.Data))
		}

		totalAttSize := 0
		for _, ta := range decoded.Attachments {
			totalAttSize += len(ta.Data)
		}

		fmt.Printf("\nTotal TNEF data: %d bytes\n", len(data))
		fmt.Printf("Total attribute data: %d bytes\n", totalAttrSize)
		fmt.Printf("Total attachment data: %d bytes\n", totalAttSize)
		fmt.Printf("Accounted: %d bytes\n", totalAttrSize+totalAttSize)
		fmt.Printf("Missing: %d bytes\n", len(data)-totalAttrSize-totalAttSize)

		// Try raw decode to check for nested message/attachment objects
		fmt.Printf("\nBody: %d, BodyHTML: %d\n", len(decoded.Body), len(decoded.BodyHTML))

		// Check attachment[0] content
		if len(decoded.Attachments) > 0 {
			a0 := decoded.Attachments[0]
			fmt.Printf("\nAttachment[0] Title=%q Data (hex): %x\n", a0.Title, a0.Data)
		}
	}
}
