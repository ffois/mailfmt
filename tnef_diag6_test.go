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

func TestTNEFNestedMessage(t *testing.T) {
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

		// Navigate to attAttachment (0x9005) for first attachment
		offset := 6
		for offset < len(rawData) {
			if offset+9 > len(rawData) {
				break
			}
			level := rawData[offset]
			attrID := binary.LittleEndian.Uint32(rawData[offset+1 : offset+5])
			attrLen := int(binary.LittleEndian.Uint32(rawData[offset+5 : offset+9]))
			dataStart := offset + 9

			if level == 0x02 && attrID == 0x00069005 && attrLen > 1000 {
				mapiData := rawData[dataStart : dataStart+attrLen]

				// Parse MAPI props to find PR_ATTACH_DATA_OBJ (0x3701)
				embeddedData := extractPRAttachDataObj(mapiData)
				if embeddedData == nil {
					t.Fatal("could not find PR_ATTACH_DATA_OBJ")
				}

				fmt.Printf("PR_ATTACH_DATA_OBJ total: %d bytes\n", len(embeddedData))
				fmt.Printf("First 32 bytes after GUID: %x\n", embeddedData[16:min2(48, len(embeddedData))])

				// Check if after the 16-byte GUID there's a TNEF signature
				afterGuid := embeddedData[16:]
				if len(afterGuid) >= 4 {
					sig := binary.LittleEndian.Uint32(afterGuid[0:4])
					fmt.Printf("Signature after GUID: 0x%08X (TNEF=0x223E9F78)\n", sig)

					if sig == 0x223E9F78 {
						fmt.Println("It's a nested TNEF stream!")
						decoded, err := tnef.Decode(afterGuid)
						if err != nil {
							fmt.Printf("Nested TNEF decode error: %v\n", err)
						} else {
							fmt.Printf("Nested Body: %d bytes\n", len(decoded.Body))
							fmt.Printf("Nested BodyHTML: %d bytes\n", len(decoded.BodyHTML))
							fmt.Printf("Nested Attachments: %d\n", len(decoded.Attachments))
							for i, na := range decoded.Attachments {
								fmt.Printf("  [%d] %q (%d bytes)\n", i, na.Title, len(na.Data))
							}
							fmt.Printf("Nested Attributes: %d\n", len(decoded.Attributes))
						}
					} else {
						// Try as raw MAPI attributes (no TNEF wrapper)
						fmt.Printf("Not a TNEF stream. First byte: 0x%02X\n", afterGuid[0])
						// Check if it's a count of MAPI properties
						if len(afterGuid) >= 4 {
							propCount := binary.LittleEndian.Uint32(afterGuid[0:4])
							fmt.Printf("First uint32 (possible prop count): %d\n", propCount)
						}
					}
				}
				break
			}

			offset += 9 + attrLen + 2
		}
	}
}

func extractPRAttachDataObj(mapiData []byte) []byte {
	if len(mapiData) < 4 {
		return nil
	}
	count := int(binary.LittleEndian.Uint32(mapiData[0:4]))
	offset := 4

	for i := 0; i < count && offset+4 <= len(mapiData); i++ {
		propTag := binary.LittleEndian.Uint32(mapiData[offset : offset+4])
		propType := propTag & 0xFFFF
		propID := (propTag >> 16) & 0xFFFF
		offset += 4

		// Handle named props
		if propID >= 0x8000 {
			if offset+20 > len(mapiData) {
				return nil
			}
			kind := binary.LittleEndian.Uint32(mapiData[offset+16 : offset+20])
			offset += 20
			if kind == 0 {
				offset += 4
			} else {
				if offset+4 > len(mapiData) {
					return nil
				}
				nameLen := int(binary.LittleEndian.Uint32(mapiData[offset : offset+4]))
				offset += 4 + nameLen
				if nameLen%4 != 0 {
					offset += 4 - nameLen%4
				}
			}
		}

		switch propType {
		case 0x0002: // PT_SHORT
			offset += 4
		case 0x0003: // PT_LONG
			offset += 4
		case 0x000B: // PT_BOOLEAN
			offset += 4
		case 0x0040: // PT_SYSTIME
			offset += 8
		case 0x001E, 0x001F: // PT_STRING8, PT_UNICODE
			if offset+4 > len(mapiData) {
				return nil
			}
			cnt := int(binary.LittleEndian.Uint32(mapiData[offset : offset+4]))
			offset += 4
			for j := 0; j < cnt; j++ {
				if offset+4 > len(mapiData) {
					return nil
				}
				slen := int(binary.LittleEndian.Uint32(mapiData[offset : offset+4]))
				offset += 4 + slen
				if slen%4 != 0 {
					offset += 4 - slen%4
				}
			}
		case 0x0102: // PT_BINARY
			if offset+4 > len(mapiData) {
				return nil
			}
			cnt := int(binary.LittleEndian.Uint32(mapiData[offset : offset+4]))
			offset += 4
			for j := 0; j < cnt; j++ {
				if offset+4 > len(mapiData) {
					return nil
				}
				blen := int(binary.LittleEndian.Uint32(mapiData[offset : offset+4]))
				offset += 4 + blen
				if blen%4 != 0 {
					offset += 4 - blen%4
				}
			}
		case 0x000D: // PT_OBJECT
			if offset+4 > len(mapiData) {
				return nil
			}
			cnt := int(binary.LittleEndian.Uint32(mapiData[offset : offset+4]))
			offset += 4
			for j := 0; j < cnt; j++ {
				if offset+4 > len(mapiData) {
					return nil
				}
				olen := int(binary.LittleEndian.Uint32(mapiData[offset : offset+4]))
				offset += 4
				if propID == 0x3701 {
					// This is PR_ATTACH_DATA_OBJ!
					return mapiData[offset : offset+olen]
				}
				offset += olen
				if olen%4 != 0 {
					offset += 4 - olen%4
				}
			}
		default:
			return nil
		}
	}
	return nil
}

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}
