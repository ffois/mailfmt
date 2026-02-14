package internal

import (
	"fmt"
	"os"
	"testing"
)

func TestReadEmlWithTNEF(t *testing.T) {
	testFile := `H:\Dev\Gits\EMLy\EML_TNEF.eml`
	if _, err := os.Stat(testFile); os.IsNotExist(err) {
		t.Skip("test EML file not present")
	}

	// First try the PEC reader (this is a PEC email)
	email, err := ReadPecInnerEml(testFile)
	if err != nil {
		t.Fatalf("ReadPecInnerEml failed: %v", err)
	}

	fmt.Printf("Subject: %s\n", email.Subject)
	fmt.Printf("From: %s\n", email.From)
	fmt.Printf("Attachment count: %d\n", len(email.Attachments))

	hasWinmailDat := false
	for i, att := range email.Attachments {
		fmt.Printf("  [%d] %s (%s, %d bytes)\n", i, att.Filename, att.ContentType, len(att.Data))
		if att.Filename == "winmail.dat" {
			hasWinmailDat = true
		}
	}

	if hasWinmailDat {
		t.Error("winmail.dat should have been expanded into its contained attachments")
	}

	if len(email.Attachments) == 0 {
		t.Error("expected at least one attachment after TNEF expansion")
	}
}

func TestReadEmlFallback(t *testing.T) {
	testFile := `H:\Dev\Gits\EMLy\EML_TNEF.eml`
	if _, err := os.Stat(testFile); os.IsNotExist(err) {
		t.Skip("test EML file not present")
	}

	// Also verify the plain EML reader path
	email, err := ReadEmlFile(testFile)
	if err != nil {
		t.Fatalf("ReadEmlFile failed: %v", err)
	}

	fmt.Printf("[EML] Subject: %s\n", email.Subject)
	fmt.Printf("[EML] Attachment count: %d\n", len(email.Attachments))
	for i, att := range email.Attachments {
		fmt.Printf("  [%d] %s (%s, %d bytes)\n", i, att.Filename, att.ContentType, len(att.Data))
	}
}
