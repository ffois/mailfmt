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

func TestTNEFAttributes(t *testing.T) {
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
		fmt.Printf("MAPI Attributes (%d):\n", len(decoded.Attributes))
		for _, attr := range decoded.Attributes {
			dataPreview := fmt.Sprintf("%d bytes", len(attr.Data))
			if len(attr.Data) < 200 {
				dataPreview = fmt.Sprintf("%q", attr.Data)
			}
			fmt.Printf("  Name=0x%04X  Data=%s\n", attr.Name, dataPreview)
		}

		// Check Body/BodyHTML from TNEF data struct fields
		fmt.Printf("\nBody len: %d\n", len(decoded.Body))
		fmt.Printf("BodyHTML len: %d\n", len(decoded.BodyHTML))

		// Check attachment details
		for i, ta := range decoded.Attachments {
			fmt.Printf("Attachment[%d]: title=%q dataLen=%d\n", i, ta.Title, len(ta.Data))
		}
	}
}
