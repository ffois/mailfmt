package rtf

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
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
	RTFCompressedMagic   = 0x75465A4C // "LZFu" – compressed payload
	RTFUncompressedMagic = 0x414C454D // "MELA" – raw RTF follows the header
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

// Decompress inflates a PidTagRtfCompressed stream into raw RTF bytes.
//
// A stream cut short in the middle of a token is not reported as an error.
// The control-byte loop carries no length of its own, so running out of source
// bytes mid-literal or mid-back-reference is the same code path as a clean end
// of stream, and the only field that could tell them apart is the sender's own
// rawSize. Callers get the prefix that was decoded and a nil error; a caller
// that needs to know whether the stream was whole must check it themselves.
func Decompress(data []byte) ([]byte, error) {
	if len(data) < 16 {
		return nil, errors.New("rtf: stream too short")
	}

	compSize := binary.LittleEndian.Uint32(data[0:4])
	rawSize := binary.LittleEndian.Uint32(data[4:8])
	compType := binary.LittleEndian.Uint32(data[8:12])

	switch compType {
	case RTFUncompressedMagic:
		end := 16 + int(rawSize)
		if end > len(data) || rawSize == 0 {
			end = len(data)
		}
		return data[16:end], nil
	case RTFCompressedMagic:
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
	// rawSize is a 4-byte field read straight out of the stream, so a ~20-byte
	// PidTagRtfCompressed payload can declare 0xFFFFFFFF and reserve 4 GiB
	// before decompression starts. Cap the reservation at what the payload could
	// physically expand to - a control byte followed by eight back-references of
	// 17 bytes is 136 output bytes per 17 input bytes, so 17x is the ceiling -
	// and let append grow past it if the stream really is that large.
	capHint := uint64(rawSize)
	if maxExpansion := uint64(len(src)) * 17; capHint > maxExpansion {
		capHint = maxExpansion
	}
	out := make([]byte, 0, capHint)

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

// IsEncapsulatedHTML reports whether decompressed RTF carries HTML produced by
// Outlook (\fromhtml1 in the header).
func IsEncapsulatedHTML(rtf []byte) bool {
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
	font    int  // current \fN
}

// DeEncapsulateHTML extracts the original HTML from \fromhtml RTF.
func DeEncapsulateHTML(rtf []byte) string { return deEncapsulate(rtf, false) }

// MS_DeEncapsulateHTML extracts the original HTML from \fromhtml RTF the way
// Outlook 2010 does, decoding each \'XX byte in the code page of the current
// font's \fcharset, as [MS-OXRTFEX] prescribes, instead of always in the
// document's \ansicpg: a run in an Arabic font (\fcharset178) holds
// Windows-1256 bytes, where 0x9D is U+200C, not an undefined Windows-1252
// byte. Consecutive bytes in one code page are decoded together, so
// double-byte code pages work.
func MS_DeEncapsulateHTML(rtf []byte) string { return deEncapsulate(rtf, true) }

// rtfWriter collects the output. In font-charset mode, bytes are held until
// something else is written or the code page changes, then decoded together.
type rtfWriter struct {
	buf  bytes.Buffer
	pend []byte
	cp   int
}

func (w *rtfWriter) flush() {
	if len(w.pend) > 0 {
		w.buf.WriteString(decodeCodePage(w.pend, w.cp))
		w.pend = w.pend[:0]
	}
}

func (w *rtfWriter) WriteByte(b byte) error            { w.flush(); return w.buf.WriteByte(b) }
func (w *rtfWriter) WriteRune(r rune) (int, error)     { w.flush(); return w.buf.WriteRune(r) }
func (w *rtfWriter) WriteString(s string) (int, error) { w.flush(); return w.buf.WriteString(s) }

func (w *rtfWriter) addByte(b byte, cp int) {
	if len(w.pend) > 0 && cp != w.cp {
		w.flush()
	}
	w.cp = cp
	w.pend = append(w.pend, b)
}

func (w *rtfWriter) String() string { w.flush(); return w.buf.String() }

func deEncapsulate(rtf []byte, fontCharsets bool) string {
	cpg := detectCodePage(rtf)

	var out rtfWriter
	st := rtfState{uc: 1}
	var fonts map[int]int
	if fontCharsets {
		fonts, st.font = fontCodePages(rtf)
	}
	stack := make([]rtfState, 0, 16)
	pendingStar := false
	skipChars := 0 // pending unicode fallback characters to drop

	shouldEmit := func() bool { return !st.skip && (st.inHTML || !st.htmlrtf) }

	emitByte := func(b byte) {
		if fontCharsets {
			cp := cpg
			if fcp, ok := fonts[st.font]; ok && fcp != 0 {
				cp = fcp
			}
			out.addByte(b, cp)
			return
		}
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
				case "f":
					if pendingStar {
						st.skip = true // as for any other \* destination
						pendingStar = false
					} else if hasParam {
						st.font = param
					}
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

// rtfCharsetCodePage maps \fcharsetN to a Windows code page. 0 (ANSI), 1
// (default) and 2 (symbol) map to 0: the document's \ansicpg applies.
var rtfCharsetCodePage = map[int]int{
	77: 10000, 128: 932, 129: 949, 130: 1361, 134: 936, 136: 950,
	161: 1253, 162: 1254, 163: 1258, 177: 1255, 178: 1256, 186: 1257,
	204: 1251, 222: 874, 238: 1250, 254: 437, 255: 850,
}

var (
	reFontEntry = regexp.MustCompile(`\{\\f(\d+)[^{}]*?\\fcharset(\d+)`)
	reDefFont   = regexp.MustCompile(`\\deff(\d+)`)
)

// fontCodePages reads the \fonttbl group: the code page of every font that
// declares a \fcharset other than ANSI, and the default font (\deffN).
func fontCodePages(rtf []byte) (map[int]int, int) {
	fonts := map[int]int{}
	deff := 0
	if m := reDefFont.FindSubmatch(rtf); m != nil {
		deff, _ = strconv.Atoi(string(m[1]))
	}
	start := bytes.Index(rtf, []byte(`{\fonttbl`))
	if start < 0 {
		return fonts, deff
	}
	depth, end := 0, len(rtf)
	for i := start; i < len(rtf); i++ {
		switch rtf[i] {
		case '\\':
			i++ // an escaped brace does not count
		case '{':
			depth++
		case '}':
			depth--
		}
		if depth == 0 {
			end = i
			break
		}
	}
	for _, m := range reFontEntry.FindAllSubmatch(rtf[start:end], -1) {
		n, _ := strconv.Atoi(string(m[1]))
		cs, _ := strconv.Atoi(string(m[2]))
		fonts[n] = rtfCharsetCodePage[cs]
	}
	return fonts, deff
}

// DecodeCodePage decodes text stored in a Windows code page (PR_INTERNET_CPID,
// \ansicpg, \fcharset), with the same table MS_DeEncapsulateHTML uses.
func DecodeCodePage(b []byte, cp int) string { return decodeCodePage(b, cp) }

// decodeCodePage decodes bytes of one Windows code page; an unknown code page
// is read as Windows-1252, and 65001 is taken as UTF-8 as it is.
func decodeCodePage(b []byte, cp int) string {
	var enc encoding.Encoding
	switch cp {
	case 65001:
		return string(b)
	case 1250:
		enc = charmap.Windows1250
	case 1251:
		enc = charmap.Windows1251
	case 1253:
		enc = charmap.Windows1253
	case 1254:
		enc = charmap.Windows1254
	case 1255:
		enc = charmap.Windows1255
	case 1256:
		enc = charmap.Windows1256
	case 1257:
		enc = charmap.Windows1257
	case 1258:
		enc = charmap.Windows1258
	case 874:
		enc = charmap.Windows874
	case 437:
		enc = charmap.CodePage437
	case 850:
		enc = charmap.CodePage850
	case 10000:
		enc = charmap.Macintosh
	case 932:
		enc = japanese.ShiftJIS
	case 936:
		enc = simplifiedchinese.GBK
	case 949:
		enc = korean.EUCKR
	case 950:
		enc = traditionalchinese.Big5
	}
	if enc != nil {
		if s, _, err := transform.String(enc.NewDecoder(), string(b)); err == nil {
			return s
		}
	}
	var sb strings.Builder
	for _, c := range b {
		sb.WriteRune(cp1252Table[c])
	}
	return sb.String()
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
