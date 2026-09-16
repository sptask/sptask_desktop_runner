//go:build !windows

package autostart

// Enable: Windows dışı platformlarda autostart taslağı
func Enable() error {
	return nil
}

// Disable: Windows dışı platformlarda autostart taslağı
func Disable() error {
	return nil
}

// IsEnabled: Windows dışı platformlarda kontrol
func IsEnabled() bool {
	return false
}
