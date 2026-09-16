package notify

import (
	"fmt"
	"log"
	"os/exec"
	"runtime"
	"strings"
)

// ShowNotification: İşletim sistemi yerel masaüstü bildirimi (Toast / Balon) gösterir.
func ShowNotification(title, message string) {
	switch runtime.GOOS {
	case "windows":
		// Windows PowerShell tek satırlık balon bildirim (Hiçbir harici bağımlılık gerektirmez)
		script := fmt.Sprintf(`
[void] [System.Reflection.Assembly]::LoadWithPartialName("System.Windows.Forms")
$objNotifyIcon = New-Object System.Windows.Forms.NotifyIcon
$objNotifyIcon.Icon = [System.Drawing.SystemIcons]::Information
$objNotifyIcon.BalloonTipIcon = "Info"
$objNotifyIcon.BalloonTipTitle = "%s"
$objNotifyIcon.BalloonTipText = "%s"
$objNotifyIcon.Visible = $True
$objNotifyIcon.ShowBalloonTip(4000)
Start-Sleep -Seconds 1
$objNotifyIcon.Dispose()
`, escapeQuotes(title), escapeQuotes(message))

		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
		_ = cmd.Start()

	case "darwin":
		// macOS AppleScript bildirimi
		script := fmt.Sprintf(`display notification "%s" with title "%s"`, escapeQuotes(message), escapeQuotes(title))
		_ = exec.Command("osascript", "-e", script).Run()

	case "linux":
		// Linux notify-send
		_ = exec.Command("notify-send", title, message).Run()

	default:
		log.Printf("📢 [%s] %s", title, message)
	}
}

func escapeQuotes(s string) string {
	return strings.ReplaceAll(s, `"`, `'`)
}
