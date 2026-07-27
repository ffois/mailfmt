package mailfmt

import (
	"encoding/binary"
	"os"
	"strings"
	"testing"

	"github.com/ffois/mailfmt/internal/rtf"
)

// Genuine (non-HTML) RTF must yield no HTML so the caller falls back to the
// plain-text body instead of dumping RTF control words on screen.
func TestHTMLFromCompressedRTFNonHTML(t *testing.T) {
	payload := []byte(`{\rtf1\ansi\pard Just plain RTF, not from HTML.\par}`)
	stream := make([]byte, 16+len(payload))
	binary.LittleEndian.PutUint32(stream[0:], uint32(len(payload)+12))
	binary.LittleEndian.PutUint32(stream[4:], uint32(len(payload)))
	binary.LittleEndian.PutUint32(stream[8:], rtf.RTFUncompressedMagic)
	copy(stream[16:], payload)

	props := map[uint32][]byte{(pidTagRtfCompressed << 16) | propTypeBinary: stream}
	if got := htmlFromCompressedRTF(props); got != "" {
		t.Errorf("expected empty HTML for non-encapsulated RTF, got %q", got)
	}
}

// End-to-end check against a real .msg whose body is compressed RTF with inline
// images. Gated on MSG_DEBUG_PATH so it never runs (or fails) in CI.
//
//	MSG_DEBUG_PATH="C:\path\to\file.msg" go test . -run TestReadMSGInlineImagesE2E -v
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
