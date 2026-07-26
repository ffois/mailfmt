package internal

import (
	"encoding/binary"
	"os"
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

	got := deEncapsulateHTML([]byte(rtf))
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

	got := deEncapsulateHTML([]byte(rtf))
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
	binary.LittleEndian.PutUint32(stream[8:], rtfUncompressedMagic)    // "MELA"
	copy(stream[16:], payload)

	got, err := decompressRTF(stream)
	if err != nil {
		t.Fatalf("decompressRTF: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("uncompressed passthrough mismatch:\n got=%q\nwant=%q", got, payload)
	}
}

// Genuine (non-HTML) RTF must yield no HTML so the caller falls back to the
// plain-text body instead of dumping RTF control words on screen.
func TestHTMLFromCompressedRTFNonHTML(t *testing.T) {
	payload := []byte(`{\rtf1\ansi\pard Just plain RTF, not from HTML.\par}`)
	stream := make([]byte, 16+len(payload))
	binary.LittleEndian.PutUint32(stream[0:], uint32(len(payload)+12))
	binary.LittleEndian.PutUint32(stream[4:], uint32(len(payload)))
	binary.LittleEndian.PutUint32(stream[8:], rtfUncompressedMagic)
	copy(stream[16:], payload)

	props := map[uint32][]byte{(pidTagRtfCompressed << 16) | propTypeBinary: stream}
	if got := htmlFromCompressedRTF(props); got != "" {
		t.Errorf("expected empty HTML for non-encapsulated RTF, got %q", got)
	}
}

// End-to-end check against a real .msg whose body is compressed RTF with inline
// images. Gated on MSG_DEBUG_PATH so it never runs (or fails) in CI.
//
//	MSG_DEBUG_PATH="C:\path\to\file.msg" go test ./backend/utils/mail -run TestReadMSGInlineImages -v
func TestReadMSGInlineImagesE2E(t *testing.T) {
	path := os.Getenv("MSG_DEBUG_PATH")
	if path == "" {
		t.Skip("set MSG_DEBUG_PATH to run the end-to-end MSG check")
	}

	data, err := ReadMsgFile(path)
	if err != nil {
		t.Fatalf("ReadMsgFile: %v", err)
	}
	if !strings.Contains(data.Body, "<img") {
		t.Fatal("body has no <img> tags – RTF de-encapsulation did not run")
	}
	if strings.Contains(data.Body, "cid:") {
		t.Errorf("body still contains unresolved cid: references")
	}
	if !strings.Contains(data.Body, "data:image/") {
		t.Errorf("inline images were not substituted with data URIs")
	}
}
