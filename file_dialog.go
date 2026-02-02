package internal

import (
	"context"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

var EMLDialogOptions = runtime.OpenDialogOptions{
	Title:           "Select EML file",
	Filters:         []runtime.FileFilter{{DisplayName: "EML Files (*.eml)", Pattern: "*.eml"}},
	ShowHiddenFiles: false,
}

func ShowFileDialog(ctx context.Context) (string, error) {
	filePath, err := runtime.OpenFileDialog(ctx, EMLDialogOptions)
	if err != nil {
		return "", err
	}
	return filePath, nil
}
