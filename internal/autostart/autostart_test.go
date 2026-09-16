package autostart

import (
	"runtime"
	"testing"
)

func TestAutostart_EnableDisable(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Autostart registry test sadece Windows üzerinde çalışır")
	}

	// 1. Enable
	if err := Enable(); err != nil {
		t.Fatalf("Enable failed: %v", err)
	}

	// 2. IsEnabled check
	if !IsEnabled() {
		t.Error("Expected IsEnabled to return true after Enable()")
	}

	// 3. Disable
	if err := Disable(); err != nil {
		t.Fatalf("Disable failed: %v", err)
	}

	// 4. IsEnabled check after disable
	if IsEnabled() {
		t.Error("Expected IsEnabled to return false after Disable()")
	}
}
