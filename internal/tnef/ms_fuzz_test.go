package tnef

import (
	"testing"

	"github.com/ffois/mailfmt/internal/email"
)

// msDecode itself, not MS_Decode: MS_Decode turns a panic into an error at
// the trust boundary, which would hide one from the fuzzer. Any panic found
// here is a bug to fix, not one to rely on the recover for.
func FuzzMSDecode(f *testing.F) {
	f.Add(newMSTNEF().attachment("nota.txt", []byte("contenuto")).bytes())
	f.Add(newMSTNEF().
		msgProps(msProp{0x007F0102, []byte("chiave\x00")}, msProp{0x0037001F, msUnicode("Oggetto")}).
		recipients([]msProp{{0x3001001F, msUnicode("Anna")}, {0x0C150003, msLong(1)}}).
		attachment("a.txt", []byte("a")).
		rawAttachment("senza metodo", 0x0001, []byte("b")).
		attachment("", nil, msProp{0x37050003, msLong(5)},
			msProp{0x3701000D, append(make([]byte, 16), newMSTNEF().attachment("x", []byte("y")).bytes()...)}).
		bytes())
	f.Fuzz(func(t *testing.T, blob []byte) {
		d, err := msDecode(blob)
		if err == nil && d == nil {
			t.Fatal("no result and no error")
		}
		if err == nil {
			_, _ = MS_Expand([]email.MSAttachment{winmail(blob)}, "")
			_, _ = MS_ReadMessage(blob)
		}
	})
}
