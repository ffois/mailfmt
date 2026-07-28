package mailfmt_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ffois/mailfmt"
)

var update = flag.Bool("update", false, "rewrite the .golden.json files")

// Every fixture under testdata/ is parsed through ParseFile and compared
// against a committed snapshot of the result. This covers format detection,
// the dispatch in ParseFile and each reader end to end, so a change in any of
// them shows up as a diff rather than as silence.
func TestGoldenFixtures(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "*", "*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no fixtures found under testdata/")
	}

	for _, path := range paths {
		if strings.HasSuffix(path, ".golden.json") {
			continue
		}

		t.Run(filepath.ToSlash(path), func(t *testing.T) {
			data, format, err := mailfmt.ParseFile(path)

			snapshot := map[string]any{"format": string(format)}
			if err != nil {
				snapshot["error"] = err.Error()
			} else {
				snapshot["email"] = summarise(data)
			}

			got, mErr := json.MarshalIndent(snapshot, "", "  ")
			if mErr != nil {
				t.Fatalf("marshal: %v", mErr)
			}
			got = append(got, '\n')

			goldenPath := path + ".golden.json"
			if *update {
				if wErr := os.WriteFile(goldenPath, got, 0o644); wErr != nil {
					t.Fatalf("write golden: %v", wErr)
				}
				return
			}

			want, rErr := os.ReadFile(goldenPath)
			if rErr != nil {
				t.Fatalf("read golden: %v (run with -update to create it)", rErr)
			}
			if string(got) != string(want) {
				t.Errorf("golden mismatch for %s\n got=%s\nwant=%s", path, got, want)
			}
		})
	}
}

// summarise keeps the fields that describe the parse and drops attachment
// bytes, which would make the snapshots huge and unreadable. Their length is
// kept so a change in decoding still shows up.
func summarise(d *mailfmt.EmailData) map[string]any {
	atts := make([]map[string]any, 0, len(d.Attachments))
	for _, a := range d.Attachments {
		atts = append(atts, map[string]any{
			"filename":    a.Filename,
			"contentType": a.ContentType,
			"size":        len(a.Data),
		})
	}
	return map[string]any{
		"from":          d.From,
		"to":            d.To,
		"cc":            d.Cc,
		"bcc":           d.Bcc,
		"subject":       d.Subject,
		"bodyLen":       len(d.Body),
		"bodyPrefix":    firstRunes(d.Body, 120),
		"attachments":   atts,
		"isPec":         d.IsPec,
		"hasInnerEmail": d.HasInnerEmail,
		"date":          d.Date,
	}
}

func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
