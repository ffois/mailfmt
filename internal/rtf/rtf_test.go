package rtf

import (
	"encoding/binary"
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
