package internal

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"
)

// This file recovers the HTML body that Outlook stores inside a .msg file as
// compressed RTF (PidTagRtfCompressed, 0x1009) when no plain HTML body
// (PidTagBodyHtml, 0x1013) is present. Two steps are involved:
//
//  1. Decompress the stream per [MS-OXRTFCP] (the "LZFu" dictionary scheme).
//  2. De-encapsulate the HTML per [MS-OXRTFEX] when the RTF was produced from
//     HTML (\fromhtml1). The original markup lives in \*\htmltag destinations
//     and the document text lives in runs that are not suppressed by \htmlrtf.
//
// The recovered HTML keeps its original cid: image references intact, so the
// caller's Content-Id substitution can turn them into inline data URIs.

const (
	rtfCompressedMagic   = 0x75465A4C // "LZFu" – compressed payload
	rtfUncompressedMagic = 0x414C454D // "MELA" – raw RTF follows the header
)

// rtfDictInit is the 207-byte dictionary preload mandated by [MS-OXRTFCP].
// It must be byte-exact or decompression fails.
var rtfDictInit = []byte("{\\rtf1\\ansi\\mac\\deff0\\deftab720{\\fonttbl;}" +
	"{\\f0\\fnil \\froman \\fswiss \\fmodern \\fscript \\fdecor MS Sans SerifSymbolArial" +
	"Times New RomanCourier{\\colortbl\\red0\\green0\\blue0" +
	"\r\n" +
	"\\par \\pard\\plain\\f0\\fs20\\b\\i\\u\\tab\\tx")

// cp1252Table maps every single byte to its Windows-1252 rune, used to decode
// \'XX escapes when the RTF code page is not UTF-8.
var cp1252Table [256]rune

func init() {
	for b := range 256 {
		dec := charmap.Windows1252.NewDecoder()
		r, _, err := transform.String(dec, string([]byte{byte(b)}))
		if err != nil || len(r) == 0 {
			cp1252Table[b] = rune(b)
			continue
		}
		cp1252Table[b] = []rune(r)[0]
	}
}

// htmlFromCompressedRTF recovers an HTML body from a message's
// PidTagRtfCompressed stream. It only returns HTML when the RTF was produced
// from HTML (\fromhtml1); genuine RTF documents yield "" so the caller can fall
// back to the plain-text body.
func htmlFromCompressedRTF(props map[uint32][]byte) string {
	data, ok := props[(pidTagRtfCompressed<<16)|propTypeBinary]
	if !ok {
		return ""
	}
	rtf, err := decompressRTF(data)
	if err != nil || !isEncapsulatedHTML(rtf) {
		return ""
	}
	return deEncapsulateHTML(rtf)
}

// decompressRTF inflates a PidTagRtfCompressed stream into raw RTF bytes.
func decompressRTF(data []byte) ([]byte, error) {
	if len(data) < 16 {
		return nil, errors.New("rtf: stream too short")
	}

	compSize := binary.LittleEndian.Uint32(data[0:4])
	rawSize := binary.LittleEndian.Uint32(data[4:8])
	compType := binary.LittleEndian.Uint32(data[8:12])

	switch compType {
	case rtfUncompressedMagic:
		end := 16 + int(rawSize)
		if end > len(data) || rawSize == 0 {
			end = len(data)
		}
		return data[16:end], nil
	case rtfCompressedMagic:
		// fall through to decompression below
	default:
		return nil, fmt.Errorf("rtf: unknown compression type 0x%08X", compType)
	}

	// compSize counts the rawSize+compType+crc fields (12 bytes) plus payload.
	end := int(compSize) + 4
	if end > len(data) || end < 16 {
		end = len(data)
	}
	src := data[16:end]

	dict := make([]byte, 4096)
	wp := copy(dict, rtfDictInit) // write pointer starts past the preload (207)
	out := make([]byte, 0, rawSize)

	i := 0
	for i < len(src) {
		control := src[i]
		i++
		for bit := range 8 {
			if control&(1<<uint(bit)) == 0 {
				// literal byte
				if i >= len(src) {
					return out, nil
				}
				c := src[i]
				i++
				out = append(out, c)
				dict[wp] = c
				wp = (wp + 1) & 4095
			} else {
				// back-reference into the dictionary
				if i+1 >= len(src) {
					return out, nil
				}
				hi, lo := src[i], src[i+1]
				i += 2
				offset := (int(hi) << 4) | (int(lo) >> 4)
				length := (int(lo) & 0x0F) + 2
				if offset == wp {
					return out, nil // end-of-stream marker
				}
				for j := range length {
					c := dict[(offset+j)&4095]
					out = append(out, c)
					dict[wp] = c
					wp = (wp + 1) & 4095
				}
			}
			if rawSize > 0 && uint32(len(out)) >= rawSize {
				return out, nil
			}
		}
	}
	return out, nil
}

// isEncapsulatedHTML reports whether decompressed RTF carries HTML produced by
// Outlook (\fromhtml1 in the header).
func isEncapsulatedHTML(rtf []byte) bool {
	head := rtf
	if len(head) > 1024 {
		head = head[:1024]
	}
	return bytes.Contains(head, []byte("\\fromhtml"))
}

// rtfDestSkip lists RTF destinations whose contents must never appear in the
// recovered HTML (font/colour tables, list bookkeeping, generator metadata…).
var rtfDestSkip = map[string]bool{
	"fonttbl": true, "colortbl": true, "stylesheet": true, "listtable": true,
	"listoverridetable": true, "revtbl": true, "rsidtbl": true, "mmath": true,
	"generator": true, "latentstyles": true, "datastore": true, "themedata": true,
	"colorschememapping": true, "pgptbl": true, "wgrffmtfilter": true,
	"template": true, "xmlnstbl": true, "pntext": true, "pntxta": true,
	"pntxtb": true, "listtext": true, "fldinst": true, "mhtmltag": true,
	"object": true, "info": true, "pict": true,
}

// rtfSymbolWord maps character-producing control words to their text.
var rtfSymbolWord = map[string]string{
	"lquote": "‘", "rquote": "’",
	"ldblquote": "“", "rdblquote": "”",
	"bullet": "•", "endash": "–", "emdash": "—",
	"emspace": " ", "enspace": " ",
}

type rtfState struct {
	htmlrtf bool // suppress RTF-only content
	inHTML  bool // current destination is \htmltag/\htmlbase – emit literally
	skip    bool // current destination must be discarded entirely
	uc      int  // \uc unicode fallback skip count
}

// deEncapsulateHTML extracts the original HTML from \fromhtml RTF.
func deEncapsulateHTML(rtf []byte) string {
	cpg := detectCodePage(rtf)

	var out bytes.Buffer
	st := rtfState{uc: 1}
	stack := make([]rtfState, 0, 16)
	pendingStar := false
	skipChars := 0 // pending unicode fallback characters to drop

	shouldEmit := func() bool { return !st.skip && (st.inHTML || !st.htmlrtf) }

	emitByte := func(b byte) {
		if cpg == 65001 || b < 0x80 {
			out.WriteByte(b)
		} else {
			out.WriteRune(cp1252Table[b])
		}
	}

	n := len(rtf)
	i := 0
	for i < n {
		c := rtf[i]

		switch c {
		case '{':
			stack = append(stack, st)
			i++
		case '}':
			if len(stack) > 0 {
				st = stack[len(stack)-1]
				stack = stack[:len(stack)-1]
			}
			i++
		case '\r', '\n':
			i++ // RTF source line breaks are not content
		case '\\':
			if i+1 >= n {
				i++
				break
			}
			next := rtf[i+1]
			switch {
			case isAlpha(next):
				// control word, optional signed parameter, optional space delimiter
				j := i + 1
				for j < n && isAlpha(rtf[j]) {
					j++
				}
				word := string(rtf[i+1 : j])
				neg := false
				if j < n && rtf[j] == '-' {
					neg = true
					j++
				}
				ps := j
				param := 0
				for j < n && rtf[j] >= '0' && rtf[j] <= '9' {
					param = param*10 + int(rtf[j]-'0')
					j++
				}
				hasParam := j > ps
				if neg {
					param = -param
				}
				if j < n && rtf[j] == ' ' {
					j++ // a single trailing space delimits the word
				}
				i = j

				switch word {
				case "htmlrtf":
					st.htmlrtf = !(hasParam && param == 0)
				case "htmltag", "htmlbase":
					st.inHTML = true
					pendingStar = false
				case "uc":
					if hasParam {
						st.uc = param
					}
					pendingStar = false
				case "u":
					if hasParam {
						if skipChars > 0 {
							skipChars--
						} else if shouldEmit() {
							out.WriteRune(rune(uint16(param)))
						}
						skipChars = st.uc
					}
					pendingStar = false
				case "par", "line", "sect", "softline":
					if skipChars > 0 {
						skipChars--
					} else if shouldEmit() {
						out.WriteByte('\n')
					}
				case "tab":
					if skipChars > 0 {
						skipChars--
					} else if shouldEmit() {
						out.WriteByte('\t')
					}
				default:
					if pendingStar {
						st.skip = true
						pendingStar = false
					} else if rtfDestSkip[word] {
						st.skip = true
					} else if s, ok := rtfSymbolWord[word]; ok {
						if skipChars > 0 {
							skipChars--
						} else if shouldEmit() {
							out.WriteString(s)
						}
					}
					// any other control word produces no output
				}
			case next == '*':
				pendingStar = true
				i += 2
			case next == '\'':
				// \'XX hex escape -> one raw byte
				if i+3 < n {
					b := (hexVal(rtf[i+2]) << 4) | hexVal(rtf[i+3])
					i += 4
					if skipChars > 0 {
						skipChars--
					} else if shouldEmit() {
						emitByte(byte(b))
					}
				} else {
					i = n
				}
			case next == '\\' || next == '{' || next == '}':
				i += 2
				if skipChars > 0 {
					skipChars--
				} else if shouldEmit() {
					out.WriteByte(next)
				}
			case next == '~':
				i += 2
				if shouldEmit() {
					out.WriteRune(' ')
				}
			case next == '_':
				i += 2
				if shouldEmit() {
					out.WriteByte('-')
				}
			case next == '-':
				i += 2 // optional hyphen – no output
			case next == '\r' || next == '\n':
				i += 2
				if shouldEmit() {
					out.WriteByte('\n')
				}
			default:
				i += 2 // unknown control symbol
			}
		default:
			i++
			if skipChars > 0 {
				skipChars--
			} else if shouldEmit() {
				emitByte(c)
			}
		}
	}

	return out.String()
}

// detectCodePage reads the \ansicpgN value from the RTF header (default 1252).
func detectCodePage(rtf []byte) int {
	idx := bytes.Index(rtf, []byte("\\ansicpg"))
	if idx < 0 {
		return 1252
	}
	j := idx + len("\\ansicpg")
	n := 0
	got := false
	for j < len(rtf) && rtf[j] >= '0' && rtf[j] <= '9' {
		n = n*10 + int(rtf[j]-'0')
		j++
		got = true
	}
	if !got {
		return 1252
	}
	return n
}

func isAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func hexVal(b byte) int {
	switch {
	case b >= '0' && b <= '9':
		return int(b - '0')
	case b >= 'a' && b <= 'f':
		return int(b-'a') + 10
	case b >= 'A' && b <= 'F':
		return int(b-'A') + 10
	}
	return 0
}
