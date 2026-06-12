package internal

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

var EmailDialogOptions = runtime.OpenDialogOptions{
	Title: "Select Email file",
	Filters: []runtime.FileFilter{
		{DisplayName: "Email Files (*.eml;*.msg)", Pattern: "*.eml;*.msg"},
		{DisplayName: "EML Files (*.eml)", Pattern: "*.eml"},
		{DisplayName: "MSG Files (*.msg)", Pattern: "*.msg"},
	},
	ShowHiddenFiles: false,
}

var FolderDialogOptions = runtime.OpenDialogOptions{
	Title:           "Select Folder",
	ShowHiddenFiles: false,
}

func ShowFileDialog(ctx context.Context) (string, error) {
	filePath, err := runtime.OpenFileDialog(ctx, EmailDialogOptions)
	if err != nil {
		return "", err
	}
	return filePath, nil
}

// ShowFolderDialog displays the native directory picker and returns the
// selected folder path, or an empty string if the user cancelled.
func ShowFolderDialog(ctx context.Context) (string, error) {
	folderPath, err := runtime.OpenDirectoryDialog(ctx, FolderDialogOptions)
	if err != nil {
		return "", err
	}
	return folderPath, nil
}

// windowsEnvVarRe matches %%VAR%% or %VAR% style environment variable references.
var windowsEnvVarRe = regexp.MustCompile(`%%([^%]+)%%|%([^%]+)%`)

// ExpandWindowsEnvVars replaces %%VAR%% or %VAR% references with their values.
func ExpandWindowsEnvVars(path string) string {
	return windowsEnvVarRe.ReplaceAllStringFunc(path, func(match string) string {
		varName := strings.Trim(match, "%")
		return os.Getenv(varName)
	})
}

// invalidFilenameChars matches characters not allowed in Windows file names.
var invalidFilenameChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

// sanitizeFilename strips path components and characters that are invalid in
// Windows file names. Attachment names come from untrusted email content, so
// they must never be able to escape the target folder.
func sanitizeFilename(filename string) string {
	name := filepath.Base(strings.ReplaceAll(filename, "\\", "/"))
	name = invalidFilenameChars.ReplaceAllString(name, "_")
	name = strings.Trim(name, " .")
	if name == "" || name == "." || name == ".." {
		name = "attachment"
	}
	return name
}

// uniquePath returns fullPath if it does not exist yet, otherwise appends
// " (1)", " (2)", ... before the extension until a free name is found.
func uniquePath(fullPath string) string {
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		return fullPath
	}
	ext := filepath.Ext(fullPath)
	base := strings.TrimSuffix(fullPath, ext)
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

// SaveAttachmentToFolder saves a base64-encoded attachment to the specified folder.
// If folderPath is empty, the user's Downloads folder is used as default.
// Environment variables in the %%VAR%% or %VAR% format are expanded.
// Existing files are never overwritten: a " (n)" suffix is appended instead.
//
// Parameters:
//   - filename: The name to save the file as
//   - base64Data: The base64-encoded file content
//   - folderPath: Optional custom folder path (uses Downloads if empty)
//
// Returns:
//   - string: The full path where the file was saved
//   - error: Any file system or decoding errors
func SaveAttachmentToFolder(filename string, base64Data string, folderPath string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return "", fmt.Errorf("failed to decode attachment data: %w", err)
	}

	targetFolder := strings.TrimSpace(folderPath)
	if targetFolder == "" {
		targetFolder = filepath.Join(os.Getenv("USERPROFILE"), "Downloads")
	} else {
		targetFolder = ExpandWindowsEnvVars(targetFolder)
	}

	if err := os.MkdirAll(targetFolder, 0755); err != nil {
		return "", fmt.Errorf("failed to create target folder: %w", err)
	}

	fullPath := uniquePath(filepath.Join(targetFolder, sanitizeFilename(filename)))

	if err := os.WriteFile(fullPath, data, 0644); err != nil {
		return "", fmt.Errorf("failed to save attachment: %w", err)
	}

	return fullPath, nil
}

// OpenFileExplorer opens Windows Explorer and selects the specified file.
// Uses the /select parameter to highlight the file in Explorer.
// If the path is a directory, opens the directory without selecting anything.
//
// Parameters:
//   - filePath: The full path to the file or directory to open in Explorer
//
// Returns:
//   - error: Any execution errors
func OpenFileExplorer(filePath string) error {
	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("failed to stat path: %w", err)
	}

	if info.IsDir() {
		cmd := exec.Command("explorer.exe", filePath)
		return cmd.Start()
	}

	cmd := exec.Command("explorer.exe", "/select,", filePath)
	return cmd.Start()
}
