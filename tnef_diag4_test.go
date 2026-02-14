package internal

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

func TestTNEFRawScan(t *testing.T) {
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

		fmt.Printf("TNEF raw size: %d bytes\n", len(data))

		// Verify signature
		if len(data) < 6 {
			t.Fatal("too short")
		}
		sig := binary.LittleEndian.Uint32(data[0:4])
		key := binary.LittleEndian.Uint16(data[4:6])
		fmt.Printf("Signature: 0x%08X  Key: 0x%04X\n", sig, key)

		offset := 6
		attrNum := 0
		for offset < len(data) {
			if offset+9 > len(data) {
				fmt.Printf("  Truncated at offset %d\n", offset)
				break
			}

			level := data[offset]
			attrID := binary.LittleEndian.Uint32(data[offset+1 : offset+5])
			attrLen := binary.LittleEndian.Uint32(data[offset+5 : offset+9])

			levelStr := "MSG"
			if level == 0x02 {
				levelStr = "ATT"
			}

			fmt.Printf("  [%03d] offset=%-8d level=%s id=0x%08X len=%d\n",
				attrNum, offset, levelStr, attrID, attrLen)

			// Move past: level(1) + id(4) + len(4) + data(attrLen) + checksum(2)
			offset += 1 + 4 + 4 + int(attrLen) + 2

			attrNum++
			if attrNum > 200 {
				fmt.Println("  ... stopping at 200 attributes")
				break
			}
		}
	}
}
