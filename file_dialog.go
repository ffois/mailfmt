package internal

import (
	"context"

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

func ShowFileDialog(ctx context.Context) (string, error) {
	filePath, err := runtime.OpenFileDialog(ctx, EmailDialogOptions)
	if err != nil {
		return "", err
	}
	return filePath, nil
}
