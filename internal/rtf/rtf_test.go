package rtf

import (
	"bytes"
	"encoding/binary"
	"runtime"
	"strings"
	"testing"
)

// De-encapsulation must recover the original HTML from \fromhtml RTF: emit
// \*\htmltag contents and unsuppressed document text, decode \'XX via the
// declared code page, drop \htmlrtf-suppressed runs, and never leak the font
// table or other RTF-only destinations.
func TestDeEncapsulateHTML(t *testing.T) {
	rtf := `{\rtf1\ansi\ansicpg1252\fromhtml1\deff0` +
		`{\fonttbl{\f0\fswiss Arial;}}` +
		`{\*\htmltag <p>}Caff\'e8 \htmlrtf SUPPRESSED\htmlrtf0 ok{\*\htmltag </p>}` +
		`{\*\htmltag <img src="cid:x@1">}}`

	got := DeEncapsulateHTML([]byte(rtf))
	want := `<p>Caffè ok</p><img src="cid:x@1">`
	if got != want {
		t.Fatalf("de-encapsulated HTML mismatch:\n got=%q\nwant=%q", got, want)
	}
	if strings.Contains(got, "Arial") {
		t.Error("font table leaked into output")
	}
	if strings.Contains(got, "SUPPRESSED") {
		t.Error("\\htmlrtf-suppressed text leaked into output")
	}
}

// \*\mhtmltag carries the MHTML variant of a reference and must be discarded in
// favour of the matching \*\htmltag.
func TestDeEncapsulateHTMLSkipsMhtmltag(t *testing.T) {
	rtf := `{\rtf1\fromhtml1` +
		`{\*\mhtmltag <img src="http://example/mhtml">}` +
		`{\*\htmltag <img src="cid:logo@1">}}`

	got := DeEncapsulateHTML([]byte(rtf))
	if strings.Contains(got, "mhtml") {
		t.Errorf("mhtmltag content leaked: %q", got)
	}
	if !strings.Contains(got, `src="cid:logo@1"`) {
		t.Errorf("htmltag content missing: %q", got)
	}
}

func TestDecompressRTFUncompressed(t *testing.T) {
	payload := []byte(`{\rtf1\fromhtml1{\*\htmltag <b>hi</b>}}`)
	stream := make([]byte, 16+len(payload))
	binary.LittleEndian.PutUint32(stream[0:], uint32(len(payload)+12)) // compSize
	binary.LittleEndian.PutUint32(stream[4:], uint32(len(payload)))    // rawSize
	binary.LittleEndian.PutUint32(stream[8:], RTFUncompressedMagic)    // "MELA"
	copy(stream[16:], payload)

	got, err := Decompress(stream)
	if err != nil {
		t.Fatalf("Decompress: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("uncompressed passthrough mismatch:\n got=%q\nwant=%q", got, payload)
	}
}

// compressRTF produces a valid LZFu stream that uses literals only. The format
// allows it: a control byte with a zero bit means "the next byte is a literal",
// so a stream of zero control bytes is a correct, if useless, encoding. That is
// enough to exercise the decompressor's framing and dictionary handling.
func compressRTF(payload []byte) []byte {
	var body []byte
	for i := 0; i < len(payload); i += 8 {
		body = append(body, 0x00) // eight literals follow
		end := i + 8
		if end > len(payload) {
			end = len(payload)
		}
		body = append(body, payload[i:end]...)
	}

	out := make([]byte, 16+len(body))
	binary.LittleEndian.PutUint32(out[0:], uint32(len(body)+12)) // compSize
	binary.LittleEndian.PutUint32(out[4:], uint32(len(payload))) // rawSize
	binary.LittleEndian.PutUint32(out[8:], RTFCompressedMagic)
	binary.LittleEndian.PutUint32(out[12:], 0) // CRC, not checked
	copy(out[16:], body)
	return out
}

// The compressed path is the one Outlook actually uses. A round trip through
// it must give back exactly the original RTF.
func TestDecompressRTFCompressed(t *testing.T) {
	payload := []byte(`{\rtf1\ansi\fromhtml1{\*\htmltag <p>ciao</p>}}`)

	got, err := Decompress(compressRTF(payload))
	if err != nil {
		t.Fatalf("Decompress: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("round trip mismatch:\n got=%q\nwant=%q", got, payload)
	}
}

// A stream shorter than the 16-byte header cannot even be parsed for
// compSize/rawSize/compType, so it must be rejected outright rather than
// panicking on an out-of-range slice.
func TestDecompressRTFTruncated(t *testing.T) {
	full := compressRTF([]byte(`{\rtf1\fromhtml1 testo}`))

	if _, err := Decompress(full[:8]); err == nil {
		t.Error("expected an error for a stream cut inside the header, got nil")
	}
}

// Unlike a truncated header, a body cut mid-token gives Decompress no way to
// tell "truncated" apart from "legitimately ends here" (the control-byte loop
// carries no separate length field of its own, only compSize/rawSize in the
// header, and running out of source bytes mid-literal or mid-back-reference
// is the same code path as a clean end of stream). So this must not panic or
// fabricate trailing bytes: it has to return exactly the prefix it managed to
// decode, with a nil error, rather than the full original payload.
func TestDecompressRTFBodyCutShort(t *testing.T) {
	payload := []byte(`{\rtf1\fromhtml1 testo}`)
	full := compressRTF(payload)

	got, err := Decompress(full[:len(full)-4])
	if err != nil {
		t.Fatalf("Decompress: %v", err)
	}
	if len(got) == 0 || len(got) >= len(payload) {
		t.Fatalf("expected a non-empty partial result shorter than the original payload (%d bytes), got %d bytes: %q", len(payload), len(got), got)
	}
	if !bytes.Equal(payload[:len(got)], got) {
		t.Fatalf("partial result is not a prefix of the original payload:\n got=%q\nwant prefix of=%q", got, payload)
	}
}

// rawSize is a 4-byte field the sender writes, and nothing downstream of it is
// checked against the payload that follows. A tiny stream claiming to expand to
// 4 GiB must not be taken at its word: the decompressed size the caller gets
// has to come from the bytes actually decoded, and the memory reserved has to
// stay proportional to the input rather than to the claim.
func TestDecompressRTFIgnoresInflatedRawSize(t *testing.T) {
	stream := compressRTF([]byte(`{\rtf1\fromhtml1 x}`))
	binary.LittleEndian.PutUint32(stream[4:8], 0xFFFFFFFF)

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	got, err := Decompress(stream)
	if err != nil {
		t.Fatalf("Decompress: %v", err)
	}

	runtime.ReadMemStats(&after)

	if len(got) > len(stream)*17 {
		t.Fatalf("decoded %d bytes from a %d-byte stream, more than LZFu can expand to", len(got), len(stream))
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 16<<20 {
		t.Fatalf("allocated %d bytes decompressing a %d-byte stream: the declared rawSize is being trusted", allocated, len(stream))
	}
}

// Only RTF marked \fromhtml carries HTML. Genuine RTF must be reported as
// such so the caller falls back to the plain-text body.
func TestIsEncapsulatedHTML(t *testing.T) {
	if !IsEncapsulatedHTML([]byte(`{\rtf1\ansi\fromhtml1{\*\htmltag <p>}}`)) {
		t.Error("encapsulated HTML was not recognised")
	}
	if IsEncapsulatedHTML([]byte(`{\rtf1\ansi\pard Testo normale.\par}`)) {
		t.Error("genuine RTF was recognised as encapsulated HTML")
	}
}
