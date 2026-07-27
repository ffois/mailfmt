package msg

import (
	"github.com/ffois/mailfmt/internal/rtf"
)

// htmlFromCompressedRTF recovers an HTML body from a message's
// PidTagRtfCompressed stream. It only returns HTML when the RTF was produced
// from HTML (\fromhtml1); genuine RTF documents yield "" so the caller can fall
// back to the plain-text body.
func htmlFromCompressedRTF(props map[uint32][]byte) string {
	data, ok := props[(pidTagRtfCompressed<<16)|propTypeBinary]
	if !ok {
		return ""
	}
	decompressed, err := rtf.Decompress(data)
	if err != nil {
		return ""
	}
	if !rtf.IsEncapsulatedHTML(decompressed) {
		return ""
	}
	return rtf.DeEncapsulateHTML(decompressed)
}
