package mailfmt

import (
	"testing"
)

func TestEmbeddedFilename(t *testing.T) {
	cases := []struct {
		name     string
		ef       EmbeddedFile
		mimeType string
		want     string
	}{
		{
			name:     "declared filename wins",
			ef:       EmbeddedFile{CID: "c181c7cd@yahoo.com", Filename: "logo.png"},
			mimeType: "image/png",
			want:     "logo.png",
		},
		{
			name:     "cid with domain-like suffix gets mime extension",
			ef:       EmbeddedFile{CID: "c181c7cd@yahoo.com"},
			mimeType: "image/png",
			want:     "c181c7cd@yahoo.com.png",
		},
		{
			name:     "cid without extension gets mime extension",
			ef:       EmbeddedFile{CID: "image001"},
			mimeType: "image/jpeg",
			want:     "image001.jpg",
		},
		{
			name:     "empty cid falls back to placeholder",
			ef:       EmbeddedFile{},
			mimeType: "image/gif",
			want:     "embedded_image.gif",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := embeddedFilename(tc.ef, tc.mimeType); got != tc.want {
				t.Errorf("expected %q, got %q", tc.want, got)
			}
		})
	}
}
