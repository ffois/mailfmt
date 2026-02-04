package internal

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ReadMsgFile reads a .msg file using the native Go parser.
func ReadMsgFile(filePath string) (*EmailData, error) {
	return ReadMsgPecFile(filePath)
}

func OSSReadMsgFile(filePath string) (*EmailData, error) {
	return parseMsgFile(filePath)
}

// parseSignedMsgExec executes 'signed_msg.exe' via cmd to convert a MSG to JSON,
// then processes the output to reconstruct the PEC email data.
func ReadMsgPecFile(filePath string) (*EmailData, error) {
	fmt.Println("Called!")
	// 1. Locate signed_msg.exe
	exePath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("failed to get executable path: %w", err)
	}
	baseDir := filepath.Dir(exePath)
	helperExe := filepath.Join(baseDir, "signed_msg.exe")

	fmt.Println(helperExe)

	// 2. Create temp file for JSON output
	// Using generic temp file naming with timestamp
	timestamp := time.Now().Format("20060102_150405")
	tempFile, err := os.CreateTemp("", fmt.Sprintf("pec_output_%s_*.json", timestamp))
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	tempPath := tempFile.Name()
	tempFile.Close() // Close immediately, exe will write to it
	// defer os.Remove(tempPath) // Cleanup

	// 3. Run signed_msg.exe <msgPath> <jsonPath>
	// Use exec.Command
	// Note: Command might need to be "cmd", "/C", ... but usually direct execution works on Windows
	fmt.Println(helperExe, filePath, tempPath)
	cmd := exec.Command(helperExe, filePath, tempPath)
	// Hide window?
	// cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} // Requires syscall import

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("signed_msg.exe failed: %s, output: %s", err, string(output))
	}

	// 4. Read JSON output
	jsonData, err := os.ReadFile(tempPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read json output: %w", err)
	}

	// 5. Parse JSON
	var pecJson struct {
		Subject     string   `json:"subject"`
		From        string   `json:"from"`
		To          []string `json:"to"`
		Cc          []string `json:"cc"`
		Bcc         []string `json:"bcc"`
		Body        string   `json:"body"`
		HtmlBody    string   `json:"htmlBody"`
		Attachments []struct {
			Filename    string `json:"filename"`
			ContentType string `json:"contentType"`
			Data        string `json:"data"`       // Base64
			DataFormat  string `json:"dataFormat"` // "base64" (optional)
		} `json:"attachments"`
	}

	if err := json.Unmarshal(jsonData, &pecJson); err != nil {
		return nil, fmt.Errorf("failed to parse json output: %w", err)
	}

	// 6. Check for postacert.eml to determine if it is a PEC
	var foundPostacert bool
	var hasDatiCert, hasSmime bool

	// We'll prepare attachments listing at the same time
	var attachments []EmailAttachment

	for _, att := range pecJson.Attachments {
		attData, err := base64.StdEncoding.DecodeString(att.Data)
		if err != nil {
			fmt.Printf("Failed to decode attachment %s: %v\n", att.Filename, err)
			continue
		}

		filenameLower := strings.ToLower(att.Filename)
		if filenameLower == "postacert.eml" {
			foundPostacert = true
		}
		if filenameLower == "daticert.xml" {
			hasDatiCert = true
		}
		if filenameLower == "smime.p7s" {
			hasSmime = true
		}

		attachments = append(attachments, EmailAttachment{
			Filename:    att.Filename,
			ContentType: att.ContentType,
			Data:        attData,
		})
	}

	if !foundPostacert {
		// Maybe its a normal MSG, continue to try to parse it as a regular email
		normalMsgEmail, err := parseMsgFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("failed to find postacert.eml and also failed to parse as normal MSG: %w", err)
		}
		return normalMsgEmail, nil
	}

	// 7. It is a PEC. Return the outer message (wrapper)
	// so the user can see the PEC envelope and attachments (postacert.eml, etc.)

	body := pecJson.HtmlBody
	if body == "" {
		body = pecJson.Body
	}

	return &EmailData{
		From:          convertToUTF8(pecJson.From),
		To:            pecJson.To, // Assuming format is already correct or compatible
		Cc:            pecJson.Cc,
		Bcc:           pecJson.Bcc,
		Subject:       convertToUTF8(pecJson.Subject),
		Body:          convertToUTF8(body),
		Attachments:   attachments,
		IsPec:         hasDatiCert || hasSmime, // Typical PEC indicators
		HasInnerEmail: foundPostacert,
	}, nil
}
