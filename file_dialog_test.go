package internal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckFolderWritable(t *testing.T) {
	writable := t.TempDir()
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	tests := []struct {
		name      string
		folder    string
		wantError bool
	}{
		// An empty path means "use the Downloads default", which is the
		// value we fall back to — testing it would be circular.
		{name: "empty path is always valid", folder: "", wantError: false},
		{name: "blank path is always valid", folder: "   ", wantError: false},
		{name: "writable folder", folder: writable, wantError: false},
		{name: "missing folder", folder: missing, wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckFolderWritable(tt.folder)
			if tt.wantError && err == nil {
				t.Fatalf("CheckFolderWritable(%q) = nil, want error", tt.folder)
			}
			if !tt.wantError && err != nil {
				t.Fatalf("CheckFolderWritable(%q) = %v, want nil", tt.folder, err)
			}
		})
	}
}

// The probe file must never survive the check, otherwise every startup
// would litter the user's attachment folder.
func TestCheckFolderWritableLeavesNoResidue(t *testing.T) {
	folder := t.TempDir()

	if err := CheckFolderWritable(folder); err != nil {
		t.Fatalf("CheckFolderWritable(%q) = %v, want nil", folder, err)
	}

	entries, err := os.ReadDir(folder)
	if err != nil {
		t.Fatalf("failed to read folder: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("folder contains %d leftover entries, want 0", len(entries))
	}
}

// The check must not create the folder: SaveAttachmentToFolder does that on
// purpose when saving, but here a missing folder is a failure to report,
// not something to silently provision.
func TestCheckFolderWritableDoesNotCreateFolder(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	if err := CheckFolderWritable(missing); err == nil {
		t.Fatal("CheckFolderWritable(missing) = nil, want error")
	}

	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("folder was created by the check (stat err = %v)", err)
	}
}
