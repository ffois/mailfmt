package mailfmt_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ffois/mailfmt"
)

// Every fixture under testdata/ is parsed through MS_ParseFile and compared
// against a committed snapshot, <fixture>.ms.golden.json, as
// TestGoldenFixtures does for the plain readers. Attached messages are opened
// too, two levels deep, with MS_ReadAttachedMessage. The snapshot keeps a
// hash of the body and of every attachment, so a one-byte change shows up;
// rerun with -update after a deliberate change and review the diff.
func TestMSGoldenFixtures(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "*", "*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, ".golden.json") {
			continue
		}
		t.Run(filepath.ToSlash(path), func(t *testing.T) {
			data, format, err := mailfmt.MS_ParseFile(path)
			snapshot := map[string]any{"format": string(format)}
			if err != nil {
				snapshot["error"] = err.Error()
			} else {
				snapshot["email"] = msSummarise(data)
				if opened := msOpenAttached(path, data, nil); opened != nil {
					snapshot["attachedMessages"] = opened
				}
			}

			got, mErr := json.MarshalIndent(snapshot, "", "  ")
			if mErr != nil {
				t.Fatalf("marshal: %v", mErr)
			}
			got = append(got, '\n')

			goldenPath := path + ".ms.golden.json"
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

// msOpenAttached opens each attached message of d, two levels deep, with
// MS_ReadAttachedMessage.
func msOpenAttached(path string, d *mailfmt.MSEmailData, at []int) []any {
	if len(at) >= 2 {
		return nil
	}
	var opened []any
	for i, a := range d.MSAttachments {
		if !a.Embedded {
			continue
		}
		sub := append(append([]int(nil), at...), i)
		entry := map[string]any{"attachment": sub}
		if inner, err := mailfmt.MS_ReadAttachedMessage(path, sub...); err != nil {
			entry["error"] = err.Error()
		} else {
			entry["email"] = msSummarise(inner)
			if deeper := msOpenAttached(path, inner, sub); deeper != nil {
				entry["attachedMessages"] = deeper
			}
		}
		opened = append(opened, entry)
	}
	return opened
}

// msSummarise keeps everything an MS_ reader reports, with hashes in place
// of the body and attachment bytes.
func msSummarise(d *mailfmt.MSEmailData) map[string]any {
	atts := make([]map[string]any, 0, len(d.MSAttachments))
	for _, a := range d.MSAttachments {
		atts = append(atts, map[string]any{
			"filename":        a.Filename,
			"displayName":     a.DisplayName,
			"contentType":     a.ContentType,
			"contentId":       a.ContentID,
			"contentLocation": a.ContentLocation,
			"hidden":          a.Hidden,
			"embedded":        a.Embedded,
			"size":            len(a.Data),
			"sha256":          shortHash([]byte(a.Data)),
		})
	}
	var visible []string
	for _, a := range d.Attachments {
		visible = append(visible, a.Filename)
	}
	return map[string]any{
		"from":               d.From,
		"to":                 d.To,
		"cc":                 d.Cc,
		"bcc":                d.Bcc,
		"subject":            d.Subject,
		"conversationTopic":  d.ConversationTopic,
		"date":               d.Date,
		"importance":         int(d.Importance),
		"unsent":             d.Unsent,
		"bodyLen":            len(d.Body),
		"bodyPrefix":         firstRunes(d.Body, 120),
		"bodySha256":         shortHash([]byte(d.Body)),
		"bodyFormat":         int(d.BodyFormat),
		"rtfBodyLen":         len(d.RTFBody),
		"msAttachments":      atts,
		"visibleAttachments": visible,
		"droppedAttachments": d.DroppedAttachments,
		"isPec":              d.IsPec,
		"hasInnerEmail":      d.HasInnerEmail,
	}
}

func shortHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}
