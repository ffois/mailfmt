package rtf

import "testing"

// The case met in a real message: a byte in a run whose font is Arabic
// (\fcharset178) is Windows-1256, where 0x9D is U+200C ZERO WIDTH NON-JOINER.
// DeEncapsulateHTML reads it as Windows-1252, where 0x9D is undefined.
func TestMSDeEncapsulateHTMLFontCharset(t *testing.T) {
	rtf := []byte(`{\rtf1\ansi\ansicpg1252\fromhtml1 \deff0{\fonttbl` +
		`{\f0\fswiss\fcharset0 Arial;}{\f6\fswiss\fcharset178 Arial;}{\f7\fmodern\fcharset128 MS Gothic;}}` +
		"\r\n" + `{\*\htmltag84 &nbsp;}\htmlrtf \'a0\htmlrtf0 ` +
		`\htmlrtf{\f6\fs24\htmlrtf0 \'9d\htmlrtf\f0}\htmlrtf0 ` +
		`{\f7 \'82\'a0}\'e8}`)

	want := "&nbsp;‌あè"
	if got := MS_DeEncapsulateHTML(rtf); got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
	// The plain function keeps its behaviour.
	if got := DeEncapsulateHTML(rtf); got == want {
		t.Errorf("expected DeEncapsulateHTML to stay on Windows-1252, got %q", got)
	}
}
