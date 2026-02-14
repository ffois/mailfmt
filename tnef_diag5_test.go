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

func TestTNEFMapiProps(t *testing.T) {
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

		// Navigate to the first attachment's attAttachment (0x9005) block
		// From the raw scan: [011] offset=12082 + header(9bytes) = 12091 for data
		// Actually let's re-scan to find it properly
		offset := 6
		for offset < len(rawData) {
			if offset+9 > len(rawData) {
				break
			}
			level := rawData[offset]
			attrID := binary.LittleEndian.Uint32(rawData[offset+1 : offset+5])
			attrLen := int(binary.LittleEndian.Uint32(rawData[offset+5 : offset+9]))
			dataStart := offset + 9

			// attAttachment = 0x00069005, we want the FIRST one (for attachment group 1)
			if level == 0x02 && attrID == 0x00069005 && attrLen > 1000 {
				fmt.Printf("Found attAttachment at offset %d, len=%d\n", offset, attrLen)
				parseMapiProps(rawData[dataStart:dataStart+attrLen], t)
				break
			}

			offset += 9 + attrLen + 2
		}
	}
}

func parseMapiProps(data []byte, t *testing.T) {
	if len(data) < 4 {
		t.Fatal("too short for MAPI props")
	}

	count := binary.LittleEndian.Uint32(data[0:4])
	fmt.Printf("MAPI property count: %d\n", count)

	offset := 4
	for i := 0; i < int(count) && offset+4 <= len(data); i++ {
		propTag := binary.LittleEndian.Uint32(data[offset : offset+4])
		propType := propTag & 0xFFFF
		propID := (propTag >> 16) & 0xFFFF
		offset += 4

		// Handle named properties (ID >= 0x8000)
		if propID >= 0x8000 {
			// Skip GUID (16 bytes) + kind (4 bytes)
			if offset+20 > len(data) {
				break
			}
			kind := binary.LittleEndian.Uint32(data[offset+16 : offset+20])
			offset += 20
			if kind == 0 { // MNID_ID
				offset += 4 // skip NamedID
			} else { // MNID_STRING
				if offset+4 > len(data) {
					break
				}
				nameLen := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
				offset += 4 + nameLen
				// Pad to 4-byte boundary
				if nameLen%4 != 0 {
					offset += 4 - nameLen%4
				}
			}
		}

		var valueSize int
		switch propType {
		case 0x0002: // PT_SHORT
			valueSize = 4 // padded to 4
		case 0x0003: // PT_LONG
			valueSize = 4
		case 0x000B: // PT_BOOLEAN
			valueSize = 4
		case 0x0040: // PT_SYSTIME
			valueSize = 8
		case 0x001E: // PT_STRING8
			if offset+4 > len(data) {
				return
			}
			// count=1, then length, then data padded
			cnt := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
			offset += 4
			for j := 0; j < cnt; j++ {
				if offset+4 > len(data) {
					return
				}
				slen := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
				offset += 4
				strData := ""
				if offset+slen <= len(data) && slen < 200 {
					strData = string(data[offset : offset+slen])
				}
				fmt.Printf("  [%03d] PropID=0x%04X Type=0x%04X STRING8 len=%d val=%q\n", i, propID, propType, slen, strData)
				offset += slen
				if slen%4 != 0 {
					offset += 4 - slen%4
				}
			}
			continue
		case 0x001F: // PT_UNICODE
			if offset+4 > len(data) {
				return
			}
			cnt := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
			offset += 4
			for j := 0; j < cnt; j++ {
				if offset+4 > len(data) {
					return
				}
				slen := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
				offset += 4
				fmt.Printf("  [%03d] PropID=0x%04X Type=0x%04X UNICODE len=%d\n", i, propID, propType, slen)
				offset += slen
				if slen%4 != 0 {
					offset += 4 - slen%4
				}
			}
			continue
		case 0x0102: // PT_BINARY
			if offset+4 > len(data) {
				return
			}
			cnt := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
			offset += 4
			for j := 0; j < cnt; j++ {
				if offset+4 > len(data) {
					return
				}
				blen := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
				offset += 4
				fmt.Printf("  [%03d] PropID=0x%04X Type=0x%04X BINARY len=%d\n", i, propID, propType, blen)
				offset += blen
				if blen%4 != 0 {
					offset += 4 - blen%4
				}
			}
			continue
		case 0x000D: // PT_OBJECT
			if offset+4 > len(data) {
				return
			}
			cnt := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
			offset += 4
			for j := 0; j < cnt; j++ {
				if offset+4 > len(data) {
					return
				}
				olen := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
				offset += 4
				fmt.Printf("  [%03d] PropID=0x%04X Type=0x%04X OBJECT len=%d\n", i, propID, propType, olen)
				// Peek at first 16 bytes (GUID)
				if offset+16 <= len(data) {
					fmt.Printf("        GUID: %x\n", data[offset:offset+16])
				}
				offset += olen
				if olen%4 != 0 {
					offset += 4 - olen%4
				}
			}
			continue
		case 0x1003: // PT_MV_LONG
			if offset+4 > len(data) {
				return
			}
			cnt := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
			offset += 4
			fmt.Printf("  [%03d] PropID=0x%04X Type=0x%04X MV_LONG count=%d\n", i, propID, propType, cnt)
			offset += cnt * 4
			continue
		case 0x1102: // PT_MV_BINARY
			if offset+4 > len(data) {
				return
			}
			cnt := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
			offset += 4
			totalSize := 0
			for j := 0; j < cnt; j++ {
				if offset+4 > len(data) {
					return
				}
				blen := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
				offset += 4
				totalSize += blen
				offset += blen
				if blen%4 != 0 {
					offset += 4 - blen%4
				}
			}
			fmt.Printf("  [%03d] PropID=0x%04X Type=0x%04X MV_BINARY count=%d totalSize=%d\n", i, propID, propType, cnt, totalSize)
			continue
		default:
			fmt.Printf("  [%03d] PropID=0x%04X Type=0x%04X (unknown type)\n", i, propID, propType)
			return
		}

		if valueSize > 0 {
			if propType == 0x0003 && offset+4 <= len(data) {
				val := binary.LittleEndian.Uint32(data[offset : offset+4])
				fmt.Printf("  [%03d] PropID=0x%04X Type=0x%04X LONG val=%d (0x%X)\n", i, propID, propType, val, val)
			} else {
				fmt.Printf("  [%03d] PropID=0x%04X Type=0x%04X size=%d\n", i, propID, propType, valueSize)
			}
			offset += valueSize
		}
	}
}
