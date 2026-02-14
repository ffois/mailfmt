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

func TestTNEFRecursiveExtract(t *testing.T) {
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

		fmt.Println("=== Level 0 (top TNEF) ===")
		atts, body := recursiveExtract(rawData, 0)
		fmt.Printf("\nTotal extracted attachments: %d\n", len(atts))
		for i, a := range atts {
			fmt.Printf("  [%d] %q (%d bytes)\n", i, a.Title, len(a.Data))
		}
		fmt.Printf("Body HTML len: %d\n", len(body))
		if len(body) > 0 && len(body) < 500 {
			fmt.Printf("Body: %s\n", body)
		}
	}
}

func recursiveExtract(tnefData []byte, depth int) ([]*tnef.Attachment, string) {
	prefix := strings.Repeat("  ", depth)

	decoded, err := tnef.Decode(tnefData)
	if err != nil {
		fmt.Printf("%sDecode error: %v\n", prefix, err)
		return nil, ""
	}

	// Collect body
	bodyHTML := string(decoded.BodyHTML)
	bodyText := string(decoded.Body)

	// Check for RTF body in attributes
	for _, attr := range decoded.Attributes {
		if attr.Name == 0x1009 {
			fmt.Printf("%sFound PR_RTF_COMPRESSED: %d bytes\n", prefix, len(attr.Data))
		}
		if attr.Name == 0x1000 {
			fmt.Printf("%sFound PR_BODY: %d bytes\n", prefix, len(attr.Data))
			if bodyText == "" {
				bodyText = string(attr.Data)
			}
		}
		if attr.Name == 0x1013 || attr.Name == 0x1035 {
			fmt.Printf("%sFound PR_BODY_HTML/PR_HTML: %d bytes\n", prefix, len(attr.Data))
			if bodyHTML == "" {
				bodyHTML = string(attr.Data)
			}
		}
	}

	fmt.Printf("%sAttachments: %d, Body: %d, BodyHTML: %d\n",
		prefix, len(decoded.Attachments), len(bodyText), len(bodyHTML))

	var allAttachments []*tnef.Attachment

	// Collect real attachments (skip placeholders)
	for _, a := range decoded.Attachments {
		if a.Title == "Untitled Attachment" && len(a.Data) < 200 {
			fmt.Printf("%sSkipping placeholder: %q (%d bytes)\n", prefix, a.Title, len(a.Data))
			continue
		}
		allAttachments = append(allAttachments, a)
	}

	// Now scan for embedded messages in raw TNEF
	embeddedStreams := findEmbeddedTNEFStreams(tnefData)
	for i, stream := range embeddedStreams {
		fmt.Printf("%s--- Recursing into embedded message %d (%d bytes) ---\n", prefix, i, len(stream))
		subAtts, subBody := recursiveExtract(stream, depth+1)
		allAttachments = append(allAttachments, subAtts...)
		if bodyHTML == "" && subBody != "" {
			bodyHTML = subBody
		}
	}

	if bodyHTML != "" {
		return allAttachments, bodyHTML
	}
	return allAttachments, bodyText
}

func findEmbeddedTNEFStreams(tnefData []byte) [][]byte {
	var streams [][]byte

	// Navigate through TNEF attributes
	offset := 6
	for offset+9 < len(tnefData) {
		level := tnefData[offset]
		attrID := binary.LittleEndian.Uint32(tnefData[offset+1 : offset+5])
		attrLen := int(binary.LittleEndian.Uint32(tnefData[offset+5 : offset+9]))
		dataStart := offset + 9

		if dataStart+attrLen > len(tnefData) {
			break
		}

		// attAttachment (0x9005) at attachment level
		if level == 0x02 && attrID == 0x00069005 && attrLen > 100 {
			mapiData := tnefData[dataStart : dataStart+attrLen]
			embedded := extractPRAttachDataObj2(mapiData)
			if embedded != nil && len(embedded) > 22 {
				// Skip 16-byte GUID, check for TNEF signature
				afterGuid := embedded[16:]
				if len(afterGuid) >= 4 {
					sig := binary.LittleEndian.Uint32(afterGuid[0:4])
					if sig == 0x223E9F78 {
						streams = append(streams, afterGuid)
					}
				}
			}
		}

		offset += 9 + attrLen + 2
	}
	return streams
}

func extractPRAttachDataObj2(mapiData []byte) []byte {
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
		case 0x0002:
			offset += 4
		case 0x0003:
			offset += 4
		case 0x000B:
			offset += 4
		case 0x0040:
			offset += 8
		case 0x001E, 0x001F:
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
		case 0x0102:
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
		case 0x000D:
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
					return mapiData[offset : offset+olen]
				}
				offset += olen
				if olen%4 != 0 {
					offset += 4 - olen%4
				}
			}
		case 0x1003:
			if offset+4 > len(mapiData) {
				return nil
			}
			cnt := int(binary.LittleEndian.Uint32(mapiData[offset : offset+4]))
			offset += 4 + cnt*4
		case 0x1102:
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
		default:
			return nil
		}
	}
	return nil
}
