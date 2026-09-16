package watcher

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type FileEventCallback func(filePath, fileName string, fileSize int64, ext string)

type FolderWatcher struct {
	watchDir    string
	onFileReady FileEventCallback
	watcher     *fsnotify.Watcher
	mu          sync.Mutex
	pending     map[string]*time.Timer
}

func GetDefaultWatchDir() string {
	var desktopDir string
	if runtime.GOOS == "windows" {
		userProfile := os.Getenv("USERPROFILE")
		if userProfile == "" {
			userProfile = os.Getenv("HOME")
		}
		desktopDir = filepath.Join(userProfile, "Desktop")
	} else {
		home, _ := os.UserHomeDir()
		desktopDir = filepath.Join(home, "Desktop")
	}

	return filepath.Join(desktopDir, "Spartask_Gelen_Kutusu")
}

func NewFolderWatcher(watchDir string, onFileReady FileEventCallback) (*FolderWatcher, error) {
	if watchDir == "" {
		watchDir = GetDefaultWatchDir()
	}

	// Klasör yoksa otomatik oluştur
	if err := os.MkdirAll(watchDir, 0755); err != nil {
		return nil, fmt.Errorf("izleme klasörü oluşturulamadı (%s): %w", watchDir, err)
	}

	fsWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("fsnotify ilklendirilemedi: %w", err)
	}

	return &FolderWatcher{
		watchDir:    watchDir,
		onFileReady: onFileReady,
		watcher:     fsWatcher,
		pending:     make(map[string]*time.Timer),
	}, nil
}

func (w *FolderWatcher) WatchDir() string {
	return w.watchDir
}

func (w *FolderWatcher) Start(ctx context.Context) error {
	if err := w.watcher.Add(w.watchDir); err != nil {
		return fmt.Errorf("klasör izlemeye eklenemedi: %w", err)
	}

	log.Printf("👀 Klasör İzleyici Aktif: %s", w.watchDir)

	go func() {
		for {
			select {
			case <-ctx.Done():
				_ = w.watcher.Close()
				return

			case event, ok := <-w.watcher.Events:
				if !ok {
					return
				}

				// Sadece dosya oluşturma veya yazma olaylarını takip et
				if event.Has(fsnotify.Create) || event.Has(fsnotify.Write) {
					w.handleFileEvent(event.Name)
				}

			case err, ok := <-w.watcher.Errors:
				if !ok {
					return
				}
				log.Printf("⚠️ Klasör izleme hatası: %v", err)
			}
		}
	}()

	return nil
}

func (w *FolderWatcher) Stop() {
	if w.watcher != nil {
		_ = w.watcher.Close()
	}
	w.mu.Lock()
	for _, timer := range w.pending {
		timer.Stop()
	}
	w.pending = make(map[string]*time.Timer)
	w.mu.Unlock()
}

func (w *FolderWatcher) handleFileEvent(filePath string) {
	fileName := filepath.Base(filePath)

	// Geçici veya gizli dosyaları yoksay
	if shouldIgnore(fileName) {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	// Eğer dosya için zaten bir zamanlayıcı varsa sıfırla (Debounce)
	if timer, exists := w.pending[filePath]; exists {
		timer.Stop()
	}

	// Dosyanın yazılmasının bitmesi için 1 saniye bekle
	w.pending[filePath] = time.AfterFunc(1200*time.Millisecond, func() {
		w.processReadyFile(filePath)
	})
}

func (w *FolderWatcher) processReadyFile(filePath string) {
	w.mu.Lock()
	delete(w.pending, filePath)
	w.mu.Unlock()

	info, err := os.Stat(filePath)
	if err != nil {
		// Dosya silinmiş veya geçici olabilir
		return
	}

	if info.IsDir() {
		return
	}

	// Boyut 0 ise veya dosya kilitliyse kısa bir kontrol daha yap
	if !isFileStableAndReadable(filePath, info.Size()) {
		return
	}

	ext := strings.ToLower(filepath.Ext(filePath))
	fileName := filepath.Base(filePath)

	log.Printf("📥 Dosya hazırlandı ve yakalandı: %s (%d bytes)", fileName, info.Size())

	if w.onFileReady != nil {
		w.onFileReady(filePath, fileName, info.Size(), ext)
	}
}

func isFileStableAndReadable(filePath string, expectedSize int64) bool {
	// Dosyayı okuma modunda açmayı dene (Eğer başka bir process yazıyorsa kilitli olabilir)
	file, err := os.OpenFile(filePath, os.O_RDONLY, 0)
	if err != nil {
		return false
	}
	_ = file.Close()

	// 200ms sonra boyutun değişmediğinden emin ol
	time.Sleep(200 * time.Millisecond)
	info, err := os.Stat(filePath)
	if err != nil {
		return false
	}

	return info.Size() == expectedSize && info.Size() > 0
}

func shouldIgnore(fileName string) bool {
	// Gizli ve sistem dosyaları
	if strings.HasPrefix(fileName, ".") || strings.HasPrefix(fileName, "~$") || strings.HasPrefix(fileName, "tmp_") {
		return true
	}

	lower := strings.ToLower(fileName)
	// İndirme ve geçici uzantılar
	if strings.HasSuffix(lower, ".tmp") ||
		strings.HasSuffix(lower, ".crdownload") ||
		strings.HasSuffix(lower, ".part") ||
		strings.HasSuffix(lower, ".download") {
		return true
	}

	return false
}
