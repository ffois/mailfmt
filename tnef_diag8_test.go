package internal

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/teamwork/tnef"
)

func TestTNEFDeepAttachment(t *testing.T) {
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
		rawData, _ := io.ReadAll(att.Data)
		if strings.ToLower(att.Filename) != "winmail.dat" {
			continue
		}

		// Dig to level 2: top → embedded[0] → embedded[0]
		streams0 := findEmbeddedTNEFStreams(rawData)
		if len(streams0) == 0 {
			t.Fatal("no embedded streams at level 0")
		}
		streams1 := findEmbeddedTNEFStreams(streams0[0])
		if len(streams1) == 0 {
			t.Fatal("no embedded streams at level 1")
		}

		// Decode level 2
		decoded2, err := tnef.Decode(streams1[0])
		if err != nil {
			t.Fatalf("level 2 decode: %v", err)
		}

		fmt.Printf("Level 2 attachments: %d\n", len(decoded2.Attachments))
		for i, a := range decoded2.Attachments {
			fmt.Printf("  [%d] title=%q size=%d\n", i, a.Title, len(a.Data))
			if len(a.Data) > 20 {
				fmt.Printf("      first 20 bytes: %x\n", a.Data[:20])
				// Check for EML, MSG, TNEF signatures
				if len(a.Data) >= 4 {
					sig := binary.LittleEndian.Uint32(a.Data[0:4])
					if sig == 0x223E9F78 {
						fmt.Println("      -> TNEF stream!")
					}
				}
				if len(a.Data) >= 8 && bytes.Equal(a.Data[:8], []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}) {
					fmt.Println("      -> MSG (OLE2) file!")
				}
				// Check if text/EML
				if a.Data[0] < 128 && a.Data[0] >= 32 {
					preview := string(a.Data[:min2(200, len(a.Data))])
					if strings.Contains(preview, "From:") || strings.Contains(preview, "Content-Type") || strings.Contains(preview, "MIME") || strings.Contains(preview, "Received:") {
						fmt.Printf("      -> Looks like an EML file! First 200 chars: %s\n", preview)
					} else {
						fmt.Printf("      -> Text data: %.200s\n", preview)
					}
				}
			}
		}

		// Also check level 2's attAttachment for embedded msgs
		streams2 := findEmbeddedTNEFStreams(streams1[0])
		fmt.Printf("\nLevel 2 embedded TNEF streams: %d\n", len(streams2))

		// Check all MAPI attributes at level 2
		fmt.Println("\nLevel 2 MAPI attributes:")
		for _, attr := range decoded2.Attributes {
			fmt.Printf("  0x%04X: %d bytes\n", attr.Name, len(attr.Data))
			// PR_BODY
			if attr.Name == 0x1000 && len(attr.Data) < 500 {
				fmt.Printf("    PR_BODY: %s\n", string(attr.Data))
			}
		}
	}
}
