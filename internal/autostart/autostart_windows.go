//go:build windows

package autostart

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

const (
	runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
	appName    = "SpartaskDesktopRunner"
)

// Enable: Uygulamayı Windows başlangıcına (Registry Run Key) kaydeder.
func Enable() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("çalıştırılabilir dosya yolu alınamadı: %w", err)
	}

	exePath = filepath.Clean(exePath)

	key, _, err := registry.CreateKey(
		registry.CURRENT_USER,
		runKeyPath,
		registry.SET_VALUE,
	)
	if err != nil {
		return fmt.Errorf("registry anahtarı açılamadı: %w", err)
	}
	defer key.Close()

	// Tırnak içine alarak argümansız kaydet
	cmdValue := fmt.Sprintf("\"%s\"", exePath)
	if err := key.SetStringValue(appName, cmdValue); err != nil {
		return fmt.Errorf("registry değeri yazılamadı: %w", err)
	}

	return nil
}

// Disable: Uygulamayı Windows başlangıcından kaldırır.
func Disable() error {
	key, err := registry.OpenKey(
		registry.CURRENT_USER,
		runKeyPath,
		registry.SET_VALUE,
	)
	if err != nil {
		return nil // Anahtar yoksa zaten devre dışıdır
	}
	defer key.Close()

	_ = key.DeleteValue(appName)
	return nil
}

// IsEnabled: Uygulamanın başlangıçta çalışıp çalışmadığını kontrol eder.
func IsEnabled() bool {
	key, err := registry.OpenKey(
		registry.CURRENT_USER,
		runKeyPath,
		registry.QUERY_VALUE,
	)
	if err != nil {
		return false
	}
	defer key.Close()

	val, _, err := key.GetStringValue(appName)
	return err == nil && val != ""
}
