package actions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileActions_WriteReadMove(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "spartask_actions_test_*")
	if err != nil {
		t.Fatalf("Temp dir oluşturulamadı: %v", err)
	}
	defer os.RemoveAll(tempDir)

	sourceFile := filepath.Join(tempDir, "gelen", "fatura.txt")
	destFile := filepath.Join(tempDir, "arsiv", "2024", "fatura_islenmis.txt")
	sampleContent := "Fatura No: GIB20240001\nTutar: 1500 TL\nFirma: Enerjisa"

	// 1. WriteFile Test (create dirs automatically)
	if err := WriteFile(sourceFile, sampleContent, "overwrite"); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// 2. ReadFile Test
	content, size, err := ReadFile(sourceFile, "utf-8")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if content != sampleContent {
		t.Errorf("Expected content %s, got %s", sampleContent, content)
	}
	if size != int64(len(sampleContent)) {
		t.Errorf("Expected size %d, got %d", len(sampleContent), size)
	}

	// 3. MoveFile Test
	if err := MoveFile(sourceFile, destFile, true); err != nil {
		t.Fatalf("MoveFile failed: %v", err)
	}

	// Check source deleted
	if _, err := os.Stat(sourceFile); !os.IsNotExist(err) {
		t.Error("Expected source file to be removed after MoveFile")
	}

	// Check dest exists and has correct content
	destContent, _, err := ReadFile(destFile, "utf-8")
	if err != nil {
		t.Fatalf("ReadFile on dest failed: %v", err)
	}
	if destContent != sampleContent {
		t.Errorf("Expected dest content %s, got %s", sampleContent, destContent)
	}

	// 4. Base64 Read Test
	b64, _, err := ReadFile(destFile, "base64")
	if err != nil {
		t.Fatalf("ReadFile base64 failed: %v", err)
	}
	if len(b64) == 0 {
		t.Error("Expected non-empty base64 string")
	}
}
