package actions

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// MoveFile: Kaynak dosyayı hedef konuma taşır veya yeniden adlandırır.
func MoveFile(sourcePath, destPath string, createDirs bool) error {
	if sourcePath == "" || destPath == "" {
		return errors.New("kaynak veya hedef dosya yolu boş olamaz")
	}

	sourcePath = filepath.Clean(sourcePath)
	destPath = filepath.Clean(destPath)

	info, err := os.Stat(sourcePath)
	if err != nil {
		return fmt.Errorf("kaynak dosya bulunamadı (%s): %w", sourcePath, err)
	}
	if info.IsDir() {
		return errors.New("kaynak yol bir dizindir, sadece dosya taşınabilir")
	}

	if createDirs {
		destDir := filepath.Dir(destPath)
		if err := os.MkdirAll(destDir, 0755); err != nil {
			return fmt.Errorf("hedef klasör oluşturulamadı (%s): %w", destDir, err)
		}
	}

	// 1. Önce os.Rename dene (Aynı disk/bölüm içindeyse anında taşır)
	err = os.Rename(sourcePath, destPath)
	if err == nil {
		return nil
	}

	// 2. Farklı diskler arasındaysa (Cross-device link hatası) kopyala + sil yöntemi
	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("kaynak dosya açılamadı: %w", err)
	}
	defer sourceFile.Close()

	destFile, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return fmt.Errorf("hedef dosya oluşturulamadı: %w", err)
	}
	defer destFile.Close()

	if _, err := io.Copy(destFile, sourceFile); err != nil {
		return fmt.Errorf("dosya kopyalanamadı: %w", err)
	}

	_ = sourceFile.Close()
	_ = destFile.Close()

	_ = os.Remove(sourcePath)
	return nil
}

// ReadFile: Belirtilen yerel dosyayı metin veya base64 formatında okur.
func ReadFile(filePath string, encoding string) (string, int64, error) {
	if filePath == "" {
		return "", 0, errors.New("dosya yolu boş olamaz")
	}

	filePath = filepath.Clean(filePath)
	info, err := os.Stat(filePath)
	if err != nil {
		return "", 0, fmt.Errorf("dosya bulunamadı (%s): %w", filePath, err)
	}
	if info.IsDir() {
		return "", 0, errors.New("belirtilen yol bir dizindir")
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", 0, fmt.Errorf("dosya okunamadı: %w", err)
	}

	if strings.ToLower(encoding) == "base64" {
		return base64.StdEncoding.EncodeToString(data), info.Size(), nil
	}

	return string(data), info.Size(), nil
}

// WriteFile: Belirtilen konuma dosya yazar (overwrite veya append).
func WriteFile(filePath string, content string, mode string) error {
	if filePath == "" {
		return errors.New("dosya yolu boş olamaz")
	}

	filePath = filepath.Clean(filePath)
	destDir := filepath.Dir(filePath)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("klasör oluşturulamadı (%s): %w", destDir, err)
	}

	flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if strings.ToLower(mode) == "append" {
		flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}

	file, err := os.OpenFile(filePath, flag, 0644)
	if err != nil {
		return fmt.Errorf("dosya açılamadı: %w", err)
	}
	defer file.Close()

	if _, err := file.WriteString(content); err != nil {
		return fmt.Errorf("dosyaya yazılamadı: %w", err)
	}

	return nil
}
