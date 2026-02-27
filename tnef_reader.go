package internal

import (
	"encoding/binary"
	"mime"
	"path/filepath"
	"strings"

	"github.com/teamwork/tnef"
)

// tnefMagic is the TNEF file signature (little-endian 0x223E9F78).
var tnefMagic = []byte{0x78, 0x9F, 0x3E, 0x22}

const maxTNEFDepth = 10

// isTNEFData returns true if the given byte slice starts with the TNEF magic number.
func isTNEFData(data []byte) bool {
	return len(data) >= 4 &&
		data[0] == tnefMagic[0] &&
		data[1] == tnefMagic[1] &&
		data[2] == tnefMagic[2] &&
		data[3] == tnefMagic[3]
}

// isTNEFAttachment returns true if an attachment is a TNEF-encoded winmail.dat.
// Detection is based on filename, content-type, or the TNEF magic bytes.
func isTNEFAttachment(att EmailAttachment) bool {
	filenameLower := strings.ToLower(att.Filename)
	if filenameLower == "winmail.dat" {
		return true
	}
	ctLower := strings.ToLower(att.ContentType)
	if strings.Contains(ctLower, "application/ms-tnef") ||
		strings.Contains(ctLower, "application/vnd.ms-tnef") {
		return true
	}
	return isTNEFData(att.Data)
}

// extractTNEFAttachments decodes a TNEF blob and returns the files embedded
// inside it, recursively following nested embedded MAPI messages.
func extractTNEFAttachments(data []byte) ([]EmailAttachment, error) {
	return extractTNEFRecursive(data, 0)
}

func extractTNEFRecursive(data []byte, depth int) ([]EmailAttachment, error) {
	if depth > maxTNEFDepth {
		return nil, nil
	}

	decoded, err := tnef.Decode(data)
	if err != nil {
		return nil, err
	}

	var attachments []EmailAttachment

	// Collect non-placeholder file attachments from the library output.
	for _, att := range decoded.Attachments {
		if len(att.Data) == 0 {
			continue
		}
		// Skip the small MAPI placeholder text ("L'allegato è un messaggio
		// incorporato MAPI 1.0...") that Outlook inserts for embedded messages.
		if isEmbeddedMsgPlaceholder(att) {
			continue
		}

		filename := att.Title
		if filename == "" || filename == "Untitled Attachment" {
			filename = inferFilename(att.Data)
		}

		attachments = append(attachments, EmailAttachment{
			Filename:    filename,
			ContentType: mimeTypeFromFilename(filename),
			Data:        att.Data,
		})
	}

	// Recursively dig into embedded MAPI messages stored in
	// attAttachment (0x9005) → PR_ATTACH_DATA_OBJ (0x3701).
	for _, stream := range findEmbeddedTNEFStreamsFromRaw(data) {
		subAtts, _ := extractTNEFRecursive(stream, depth+1)
		attachments = append(attachments, subAtts...)
	}

	return attachments, nil
}

// isEmbeddedMsgPlaceholder returns true if the attachment is a tiny placeholder
// that Outlook generates for embedded MAPI messages ("L'allegato è un messaggio
// incorporato MAPI 1.0" or equivalent in other languages).
func isEmbeddedMsgPlaceholder(att *tnef.Attachment) bool {
	if len(att.Data) > 300 {
		return false
	}
	lower := strings.ToLower(string(att.Data))
	return strings.Contains(lower, "mapi 1.0") ||
		strings.Contains(lower, "embedded message") ||
		strings.Contains(lower, "messaggio incorporato")
}

// inferFilename picks a reasonable filename based on the data's magic bytes.
func inferFilename(data []byte) string {
	if looksLikeEML(data) {
		return "embedded_message.eml"
	}
	if isTNEFData(data) {
		return "embedded.dat"
	}
	if len(data) >= 8 {
		if data[0] == 0xD0 && data[1] == 0xCF && data[2] == 0x11 && data[3] == 0xE0 {
			return "embedded_message.msg"
		}
	}
	return "attachment.dat"
}

// looksLikeEML returns true if data starts with typical RFC 5322 headers.
func looksLikeEML(data []byte) bool {
	if len(data) < 20 {
		return false
	}
	// Quick check: must start with printable ASCII
	if data[0] < 32 || data[0] > 126 {
		return false
	}
	prefix := strings.ToLower(string(data[:min(200, len(data))]))
	return strings.HasPrefix(prefix, "mime-version:") ||
		strings.HasPrefix(prefix, "from:") ||
		strings.HasPrefix(prefix, "received:") ||
		strings.HasPrefix(prefix, "date:") ||
		strings.HasPrefix(prefix, "content-type:") ||
		strings.HasPrefix(prefix, "return-path:")
}

// expandTNEFAttachments iterates over the attachment list and replaces any
// TNEF-encoded winmail.dat entries with the files they contain. Attachments
// that are not TNEF are passed through unchanged.
func expandTNEFAttachments(attachments []EmailAttachment) []EmailAttachment {
	var result []EmailAttachment
	for _, att := range attachments {
		if isTNEFAttachment(att) {
			extracted, err := extractTNEFAttachments(att.Data)
			if err == nil && len(extracted) > 0 {
				result = append(result, extracted...)
				continue
			}
			// If extraction fails, keep the original blob.
		}
		result = append(result, att)
	}
	return result
}

// ---------------------------------------------------------------------------
// Raw TNEF attribute scanner — extracts nested TNEF streams from embedded
// MAPI messages that the teamwork/tnef library does not handle.
// ---------------------------------------------------------------------------

// findEmbeddedTNEFStreamsFromRaw scans the raw TNEF byte stream for
// attAttachment (0x00069005) attribute blocks, parses their MAPI properties,
// and extracts any PR_ATTACH_DATA_OBJ (0x3701) values that begin with a
// TNEF signature.
func findEmbeddedTNEFStreamsFromRaw(tnefData []byte) [][]byte {
	if len(tnefData) < 6 || !isTNEFData(tnefData) {
		return nil
	}

	var streams [][]byte
	offset := 6 // skip TNEF signature (4) + key (2)

	for offset+9 < len(tnefData) {
		level := tnefData[offset]
		attrID := binary.LittleEndian.Uint32(tnefData[offset+1 : offset+5])
		attrLen := int(binary.LittleEndian.Uint32(tnefData[offset+5 : offset+9]))
		dataStart := offset + 9

		if dataStart+attrLen > len(tnefData) || attrLen < 0 {
			break
		}

		// attAttachment (0x00069005) at attachment level (0x02)
		if level == 0x02 && attrID == 0x00069005 && attrLen > 100 {
			mapiData := tnefData[dataStart : dataStart+attrLen]
			embedded := extractPRAttachDataObjFromMAPI(mapiData)
			if len(embedded) > 22 {
				// Skip the 16-byte IID_IMessage GUID
				afterGuid := embedded[16:]
				if isTNEFData(afterGuid) {
					streams = append(streams, afterGuid)
				}
			}
		}

		// level(1) + id(4) + len(4) + data(attrLen) + checksum(2)
		offset += 9 + attrLen + 2
	}
	return streams
}

// extractPRAttachDataObjFromMAPI parses a MAPI properties block (from an
// attAttachment attribute) and returns the raw value of PR_ATTACH_DATA_OBJ
// (property ID 0x3701, type PT_OBJECT 0x000D).
func extractPRAttachDataObjFromMAPI(data []byte) []byte {
	if len(data) < 4 {
		return nil
	}
	count := int(binary.LittleEndian.Uint32(data[0:4]))
	off := 4

	for i := 0; i < count && off+4 <= len(data); i++ {
		propTag := binary.LittleEndian.Uint32(data[off : off+4])
		propType := propTag & 0xFFFF
		propID := (propTag >> 16) & 0xFFFF
		off += 4

		// Named properties (ID >= 0x8000) have extra GUID + kind fields.
		if propID >= 0x8000 {
			if off+20 > len(data) {
				return nil
			}
			kind := binary.LittleEndian.Uint32(data[off+16 : off+20])
			off += 20
			if kind == 0 { // MNID_ID
				off += 4
			} else { // MNID_STRING
				if off+4 > len(data) {
					return nil
				}
				nameLen := int(binary.LittleEndian.Uint32(data[off : off+4]))
				off += 4 + nameLen
				off += padTo4(nameLen)
			}
		}

		off = skipMAPIPropValue(data, off, propType, propID)
		if off < 0 {
			return nil // parse error
		}
		// If skipMAPIPropValue returned a special sentinel, extract it.
		// We use a hack: skipMAPIPropValue can't return the data directly,
		// so we handle PT_OBJECT / 0x3701 inline below.
	}

	// Simpler approach: re-scan specifically for 0x3701.
	return extractPRAttachDataObjDirect(data)
}

// extractPRAttachDataObjDirect re-scans the MAPI property block and
// returns the raw value of PR_ATTACH_DATA_OBJ (0x3701, PT_OBJECT).
func extractPRAttachDataObjDirect(data []byte) []byte {
	if len(data) < 4 {
		return nil
	}
	count := int(binary.LittleEndian.Uint32(data[0:4]))
	off := 4

	for i := 0; i < count && off+4 <= len(data); i++ {
		propTag := binary.LittleEndian.Uint32(data[off : off+4])
		propType := propTag & 0xFFFF
		propID := (propTag >> 16) & 0xFFFF
		off += 4

		// Skip named property headers.
		if propID >= 0x8000 {
			if off+20 > len(data) {
				return nil
			}
			kind := binary.LittleEndian.Uint32(data[off+16 : off+20])
			off += 20
			if kind == 0 {
				off += 4
			} else {
				if off+4 > len(data) {
					return nil
				}
				nameLen := int(binary.LittleEndian.Uint32(data[off : off+4]))
				off += 4 + nameLen
				off += padTo4(nameLen)
			}
		}

		switch propType {
		case 0x0002: // PT_SHORT (padded to 4)
			off += 4
		case 0x0003, 0x000A: // PT_LONG, PT_ERROR
			off += 4
		case 0x000B: // PT_BOOLEAN (padded to 4)
			off += 4
		case 0x0004: // PT_FLOAT
			off += 4
		case 0x0005: // PT_DOUBLE
			off += 8
		case 0x0006: // PT_CURRENCY
			off += 8
		case 0x0007: // PT_APPTIME
			off += 8
		case 0x0014: // PT_I8
			off += 8
		case 0x0040: // PT_SYSTIME
			off += 8
		case 0x0048: // PT_CLSID
			off += 16
		case 0x001E, 0x001F: // PT_STRING8, PT_UNICODE
			off = skipCountedBlobs(data, off)
		case 0x0102: // PT_BINARY
			off = skipCountedBlobs(data, off)
		case 0x000D: // PT_OBJECT
			if off+4 > len(data) {
				return nil
			}
			cnt := int(binary.LittleEndian.Uint32(data[off : off+4]))
			off += 4
			for j := 0; j < cnt; j++ {
				if off+4 > len(data) {
					return nil
				}
				olen := int(binary.LittleEndian.Uint32(data[off : off+4]))
				off += 4
				if propID == 0x3701 && off+olen <= len(data) {
					return data[off : off+olen]
				}
				off += olen
				off += padTo4(olen)
			}
		case 0x1002: // PT_MV_SHORT
			off = skipMVFixed(data, off, 4)
		case 0x1003: // PT_MV_LONG
			off = skipMVFixed(data, off, 4)
		case 0x1005: // PT_MV_DOUBLE
			off = skipMVFixed(data, off, 8)
		case 0x1014: // PT_MV_I8
			off = skipMVFixed(data, off, 8)
		case 0x1040: // PT_MV_SYSTIME
			off = skipMVFixed(data, off, 8)
		case 0x101E, 0x101F: // PT_MV_STRING8, PT_MV_UNICODE
			off = skipCountedBlobs(data, off)
		case 0x1048: // PT_MV_CLSID
			off = skipMVFixed(data, off, 16)
		case 0x1102: // PT_MV_BINARY
			off = skipCountedBlobs(data, off)
		default:
			// Unknown type, can't continue
			return nil
		}

		if off < 0 || off > len(data) {
			return nil
		}
	}
	return nil
}

// skipCountedBlobs advances past a MAPI value that stores count + N
// length-prefixed blobs (used by PT_STRING8, PT_UNICODE, PT_BINARY, and
// their multi-valued variants).
func skipCountedBlobs(data []byte, off int) int {
	if off+4 > len(data) {
		return -1
	}
	cnt := int(binary.LittleEndian.Uint32(data[off : off+4]))
	off += 4
	for j := 0; j < cnt; j++ {
		if off+4 > len(data) {
			return -1
		}
		blen := int(binary.LittleEndian.Uint32(data[off : off+4]))
		off += 4 + blen
		off += padTo4(blen)
	}
	return off
}

// skipMVFixed advances past a multi-valued fixed-size property
// (count followed by count*elemSize bytes).
func skipMVFixed(data []byte, off int, elemSize int) int {
	if off+4 > len(data) {
		return -1
	}
	cnt := int(binary.LittleEndian.Uint32(data[off : off+4]))
	off += 4 + cnt*elemSize
	return off
}

// skipMAPIPropValue is a generic value skipper (unused in the current flow
// but kept for completeness).
func skipMAPIPropValue(data []byte, off int, propType uint32, _ uint32) int {
	switch propType {
	case 0x0002:
		return off + 4
	case 0x0003, 0x000A, 0x000B, 0x0004:
		return off + 4
	case 0x0005, 0x0006, 0x0007, 0x0014, 0x0040:
		return off + 8
	case 0x0048:
		return off + 16
	case 0x001E, 0x001F, 0x0102, 0x000D:
		return skipCountedBlobs(data, off)
	case 0x1002, 0x1003:
		return skipMVFixed(data, off, 4)
	case 0x1005, 0x1014, 0x1040:
		return skipMVFixed(data, off, 8)
	case 0x1048:
		return skipMVFixed(data, off, 16)
	case 0x101E, 0x101F, 0x1102:
		return skipCountedBlobs(data, off)
	default:
		return -1
	}
}

// padTo4 returns the number of padding bytes needed to reach a 4-byte boundary.
func padTo4(n int) int {
	r := n % 4
	if r == 0 {
		return 0
	}
	return 4 - r
}

// ---------------------------------------------------------------------------
// MIME type helper
// ---------------------------------------------------------------------------

// mimeTypeFromFilename guesses the MIME type from a file extension.
// Falls back to "application/octet-stream" when the type is unknown.
func mimeTypeFromFilename(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		return "application/octet-stream"
	}
	t := mime.TypeByExtension(ext)
	if t == "" {
		return "application/octet-stream"
	}
	// Strip any parameters (e.g. "; charset=utf-8")
	if idx := strings.Index(t, ";"); idx != -1 {
		t = strings.TrimSpace(t[:idx])
	}
	return t
}
