package eml

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// addFixtureSeeds adds the repository fixtures matching the patterns, under
// testdata/, to the fuzz corpus.
func addFixtureSeeds(f *testing.F, patterns ...string) {
	for _, p := range patterns {
		paths, _ := filepath.Glob(filepath.Join("..", "..", "testdata", p))
		for _, path := range paths {
			if strings.HasSuffix(path, ".json") {
				continue
			}
			if b, err := os.ReadFile(path); err == nil {
				f.Add(b)
			}
		}
	}
}

// MS_Parse and MS_ParseAttached read untrusted messages: whatever the bytes,
// they return, with a result or an error, and do not panic.
func FuzzMSParse(f *testing.F) {
	addFixtureSeeds(f, "eml/*.eml", "pec/*.eml", "tnef/*.eml")
	f.Fuzz(func(t *testing.T, raw []byte) {
		d, err := MS_Parse(raw)
		if err == nil && d == nil {
			t.Fatal("no result and no error")
		}
		if _, err := MS_ParseAttached(raw, raw); err == nil && d == nil {
			t.Fatal("MS_ParseAttached read what MS_Parse refused")
		}
	})
}

// The BER walker splits within its input and gives up on what it cannot
// read; the signature it checks comes from the message.
func FuzzMSBER(f *testing.F) {
	f.Add(msTestSignedData)
	f.Add(msTestSignedDataBER)
	f.Add([]byte{0x30, 0x80, 0x30, 0x80, 0x30, 0x80, 0x00, 0x00})
	f.Add([]byte{0x1F, 0x81, 0x80, 0x01, 0x00})
	f.Fuzz(func(t *testing.T, b []byte) {
		if _, content, rest, err := msBERElement(b, 0); err == nil && len(content)+len(rest) > len(b) {
			t.Fatalf("contents %d + rest %d longer than the %d-byte input", len(content), len(rest), len(b))
		}
		_ = msIsSignedData(b)
	})
}
