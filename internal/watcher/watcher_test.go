package watcher

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestFolderWatcher_FileDetection(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "spartask_watcher_test_*")
	if err != nil {
		t.Fatalf("TempDir oluşturulamadı: %v", err)
	}
	defer os.RemoveAll(tempDir)

	var detectedFileName string
	var detectedSize int64
	var detectedExt string
	var wg sync.WaitGroup
	wg.Add(1)

	fw, err := NewFolderWatcher(tempDir, func(filePath, fileName string, fileSize int64, ext string) {
		detectedFileName = fileName
		detectedSize = fileSize
		detectedExt = ext
		wg.Done()
	})
	if err != nil {
		t.Fatalf("NewFolderWatcher failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := fw.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer fw.Stop()

	// 1. Geçici dosyayı yoksayma testi (~$ gecici dosya)
	tempIgnoredFile := filepath.Join(tempDir, "~$gecici.docx")
	_ = os.WriteFile(tempIgnoredFile, []byte("gecici icerik"), 0644)

	time.Sleep(200 * time.Millisecond)

	// 2. Gerçek bir PDF dosyası yaz
	realFile := filepath.Join(tempDir, "fatura_enerjisa.pdf")
	fileContent := []byte("%PDF-1.4 test fatura icerigi 123456789")
	if err := os.WriteFile(realFile, fileContent, 0644); err != nil {
		t.Fatalf("Dosya yazılamadı: %v", err)
	}

	// Wait for debounce and callback
	doneChan := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneChan)
	}()

	select {
	case <-doneChan:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("Watcher dosya algılama zaman aşımına uğradı")
	}

	if detectedFileName != "fatura_enerjisa.pdf" {
		t.Errorf("Expected fileName fatura_enerjisa.pdf, got %s", detectedFileName)
	}
	if detectedExt != ".pdf" {
		t.Errorf("Expected ext .pdf, got %s", detectedExt)
	}
	if detectedSize != int64(len(fileContent)) {
		t.Errorf("Expected size %d, got %d", len(fileContent), detectedSize)
	}
}
