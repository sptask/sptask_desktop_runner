package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sptask/sptask_desktop_runner/internal/actions"
	"github.com/sptask/sptask_desktop_runner/internal/autostart"
	"github.com/sptask/sptask_desktop_runner/internal/notify"
	"github.com/sptask/sptask_desktop_runner/internal/protocol"
	"github.com/sptask/sptask_desktop_runner/internal/watcher"

	"github.com/gorilla/websocket"
)

var Version = "v1.0.0"

type DeviceConfig struct {
	DeviceID    string `json:"device_id,omitempty"`
	DeviceName  string `json:"device_name"`
	DeviceToken string `json:"device_token"`
	ServerURL   string `json:"server_url"`
	WatchDir    string `json:"watch_dir,omitempty"`
}

type SafeWebSocket struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (s *SafeWebSocket) SetConn(c *websocket.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conn = c
}

func (s *SafeWebSocket) WriteJSON(v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return errors.New("bağlantı aktif değil")
	}
	return s.conn.WriteJSON(v)
}

func main() {
	serverFlag := flag.String("server", "http://localhost:8080", "Spartask API Sunucu Adresi")
	frontendFlag := flag.String("frontend", "http://localhost:5173", "Spartask Web Panel Adresi")
	watchDirFlag := flag.String("watch-dir", "", "İzlenecek yerel klasör yolu")
	autostartFlag := flag.String("autostart", "", "Otomatik başlatma ayarı: 'enable' veya 'disable'")
	resetFlag := flag.Bool("reset", false, "Kayıtlı cihaz bilgilerini sıfırla ve yeniden eşleştir")
	noBrowserFlag := flag.Bool("no-browser", false, "Tarayıcıyı otomatik açma, adresi konsola yaz")
	versionFlag := flag.Bool("version", false, "Versiyon bilgisini göster")
	flag.BoolVar(versionFlag, "v", false, "Versiyon bilgisini göster (kısa)")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("Spartask Desktop Runner %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
		return
	}

	// Autostart komut satırı yönetimi
	switch *autostartFlag {
	case "enable":
		if err := autostart.Enable(); err != nil {
			log.Fatalf("❌ Otomatik başlatma etkinleştirilemedi: %v", err)
		}
		log.Println("✅ Bilgisayar açılışında otomatik başlatma etkinleştirildi.")
		return
	case "disable":
		if err := autostart.Disable(); err != nil {
			log.Fatalf("❌ Otomatik başlatma kaldırılamadı: %v", err)
		}
		log.Println("✅ Bilgisayar açılışında otomatik başlatma devre dışı bırakıldı.")
		return
	}

	configPath := getConfigPath()

	if *resetFlag {
		_ = os.Remove(configPath)
		log.Println("🔄 Kayıtlı cihaz bilgileri sıfırlandı.")
	}

	config, err := loadConfig(configPath)
	isFirstPair := false

	if err != nil || config.DeviceToken == "" {
		isFirstPair = true
		log.Println("🔍 Cihaz eşleştirilmemiş. Eşleştirme süreci başlatılıyor...")
		token, err := startPairingFlow(*serverFlag, *frontendFlag, *noBrowserFlag)
		if err != nil {
			log.Fatalf("❌ Eşleştirme başarısız: %v", err)
		}

		hostname, _ := os.Hostname()
		config = &DeviceConfig{
			DeviceName:  hostname,
			DeviceToken: token,
			ServerURL:   *serverFlag,
		}

		// İlk eşleştirmede otomatik başlangıcı aç
		if runtime.GOOS == "windows" {
			if autoErr := autostart.Enable(); autoErr == nil {
				log.Println("🚀 Windows başlangıcına otomatik kayıt yapıldı.")
			}
		}

		notify.ShowNotification("Spartask Asistanı", "Bilgisayarınız başarıyla bağlandı ve kullanıma hazır!")
	} else {
		log.Printf("✅ Kayıtlı cihaz bulundu: %s", config.DeviceName)
	}

	// İzlenecek klasörü belirle
	watchDir := *watchDirFlag
	if watchDir == "" && config.WatchDir != "" {
		watchDir = config.WatchDir
	}
	if watchDir == "" {
		watchDir = watcher.GetDefaultWatchDir()
	}

	config.WatchDir = watchDir
	_ = saveConfig(configPath, config)

	if isFirstPair {
		log.Printf("📁 İzleme klasörü oluşturuldu: %s", watchDir)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	safeWS := &SafeWebSocket{}

	// 1. Folder Watcher Kurulumu
	folderWatcher, err := watcher.NewFolderWatcher(watchDir, func(filePath, fileName string, fileSize int64, ext string) {
		log.Printf("📄 Klasörde yeni dosya algılandı: %s (%d bytes)", fileName, fileSize)

		eventMsg := protocol.DesktopMessage{
			Type:      protocol.MsgTypeEvent,
			ID:        fmt.Sprintf("evt_%d", time.Now().UnixNano()),
			Timestamp: time.Now().Unix(),
			Payload: map[string]any{
				"event_name": "desktop.file_created",
				"data": map[string]any{
					"file_name":   fileName,
					"file_path":   filePath,
					"file_size":   fileSize,
					"extension":   ext,
					"device_name": config.DeviceName,
				},
			},
		}

		if err := safeWS.WriteJSON(eventMsg); err != nil {
			log.Printf("⚠️ Olay sunucuya iletilemedi (bağlantı çevrimdışı olabilir): %v", err)
		} else {
			log.Printf("📤 Olay sunucuya iletildi: %s -> desktop.file_created", fileName)
			notify.ShowNotification("Spartask Asistanı", fmt.Sprintf("📄 %s algılandı, bulut sürecine iletildi.", fileName))
		}
	})

	if err != nil {
		log.Printf("⚠️ Klasör izleyici başlatılamadı: %v", err)
	} else {
		_ = folderWatcher.Start(ctx)
		defer folderWatcher.Stop()
	}

	// 2. WebSocket bağlantısını başlat ve canlı tut
	runWebSocketClient(ctx, config, safeWS, watchDir)
}

// 1. Eşleştirme Akışı (Browser-Based Single Click Pairing)
func startPairingFlow(serverURL, frontendURL string, noBrowser bool) (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "Ofis-Bilgisayari"
	}

	initURL := strings.TrimRight(serverURL, "/") + "/api/v1/desktop-runners/pair/init"
	reqBody, _ := json.Marshal(map[string]string{
		"device_name":       hostname,
		"platform":          runtime.GOOS,
		"frontend_base_url": frontendURL,
	})

	resp, err := http.Post(initURL, "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		return "", fmt.Errorf("sunucuya bağlanılamadı (%s): %w", initURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("sunucu hata döndürdü (%d): %s", resp.StatusCode, string(body))
	}

	var initRes struct {
		SessionID string `json:"session_id"`
		PairURL   string `json:"pair_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&initRes); err != nil {
		return "", err
	}

	fmt.Println("\n==================================================================")
	fmt.Println("🤖 SPARTASK MASAÜSTÜ ASİSTANI (DESKTOP RUNNER)")
	fmt.Println("==================================================================")
	fmt.Println("Cihazınızı hesabınızla eşleştirmek için tarayıcınız açılıyor...")
	fmt.Println("Eğer açılmazsa aşağıdaki bağlantıyı tarayıcınızda açınız:")
	fmt.Println(initRes.PairURL)
	fmt.Println("==================================================================")

	if !noBrowser {
		openBrowser(initRes.PairURL)
	}

	// Onay durumunu yokla (Polling)
	statusURL := fmt.Sprintf("%s/api/v1/desktop-runners/pair/status?session_id=%s",
		strings.TrimRight(serverURL, "/"),
		initRes.SessionID,
	)

	fmt.Print("⏳ Web panelinden onay bekleniyor")
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	timeout := time.After(10 * time.Minute)

	for {
		select {
		case <-timeout:
			return "", fmt.Errorf("eşleştirme süresi doldu (10 dakika)")
		case <-ticker.C:
			fmt.Print(".")
			pollResp, err := http.Get(statusURL)
			if err != nil {
				continue
			}

			var statusRes struct {
				Status      string `json:"status"`
				DeviceToken string `json:"device_token"`
			}
			_ = json.NewDecoder(pollResp.Body).Decode(&statusRes)
			pollResp.Body.Close()

			if statusRes.Status == "APPROVED" && statusRes.DeviceToken != "" {
				fmt.Println("\n🎉 Cihaz web panelinden başarıyla onaylandı!")
				return statusRes.DeviceToken, nil
			} else if statusRes.Status == "EXPIRED" || statusRes.Status == "REJECTED" {
				return "", fmt.Errorf("eşleştirme reddedildi veya süresi doldu")
			}
		}
	}
}

// 2. WebSocket Bağlantısı ve Mesaj Döngüsü
func runWebSocketClient(ctx context.Context, config *DeviceConfig, safeWS *SafeWebSocket, watchDir string) {
	wsURL := getWebSocketURL(config.ServerURL, config.DeviceToken)

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)

	for {
		log.Printf("🔌 Spartask Sunucusuna bağlanılıyor: %s", config.ServerURL)

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			log.Printf("⚠️ Bağlantı kurulamadı: %v. 5 saniye sonra tekrar denenecek...", err)
			time.Sleep(5 * time.Second)
			continue
		}

		safeWS.SetConn(conn)

		log.Println("🟢 BAĞLANTI BAŞARILI: Spartask Asistanı arka planda aktif ve hazır!")
		printBanner(config.DeviceName, watchDir)

		done := make(chan struct{})

		// Okuma Döngüsü
		go func() {
			defer close(done)
			for {
				var msg protocol.DesktopMessage
				err := conn.ReadJSON(&msg)
				if err != nil {
					log.Printf("⚠️ Bağlantı kesildi: %v", err)
					safeWS.SetConn(nil)
					return
				}

				handleIncomingCommand(safeWS, msg)
			}
		}()

		// Heartbeat Ping Döngüsü (15 saniyede bir)
		ticker := time.NewTicker(15 * time.Second)
		var exitLoop bool

		for !exitLoop {
			select {
			case <-done:
				ticker.Stop()
				exitLoop = true
			case <-ticker.C:
				pingMsg := protocol.DesktopMessage{
					Type:      protocol.MsgTypePing,
					ID:        fmt.Sprintf("ping_%d", time.Now().Unix()),
					Timestamp: time.Now().Unix(),
				}
				if err := safeWS.WriteJSON(pingMsg); err != nil {
					log.Printf("⚠️ Ping gönderilemedi: %v", err)
					safeWS.SetConn(nil)
					_ = conn.Close()
					exitLoop = true
				}
			case <-interrupt:
				log.Println("🛑 Kapatma sinyali alındı, bağlantı sonlandırılıyor...")
				_ = safeWS.WriteJSON(protocol.DesktopMessage{
					Type:      protocol.MsgTypeRegistered,
					ID:        "close",
					Timestamp: time.Now().Unix(),
				})
				safeWS.SetConn(nil)
				_ = conn.Close()
				return
			case <-ctx.Done():
				return
			}
		}

		time.Sleep(3 * time.Second)
	}
}

func handleIncomingCommand(safeWS *SafeWebSocket, msg protocol.DesktopMessage) {
	switch msg.Type {
	case protocol.MsgTypeRegistered:
		log.Println("🎉 Sunucu kaydı doğrulandı. Asistan görevleri dinliyor.")

	case protocol.MsgTypePong:
		// Heartbeat başarılı

	case protocol.MsgTypeCommand:
		action, _ := msg.Payload["action"].(string)
		params, _ := msg.Payload["parameters"].(map[string]any)

		log.Printf("⚡ Görev Alındı: %s", action)

		var res protocol.DesktopMessage
		res.Type = protocol.MsgTypeCommandResult
		res.ID = msg.ID
		res.Timestamp = time.Now().Unix()

		switch action {
		case "notify":
			title, _ := params["title"].(string)
			message, _ := params["message"].(string)
			if title == "" {
				title = "Spartask Asistanı"
			}
			notify.ShowNotification(title, message)
			res.Payload = map[string]any{"success": true}

		case "file.move":
			sourcePath, _ := params["source_path"].(string)
			destPath, _ := params["destination_path"].(string)
			createDirs := true
			if cd, ok := params["create_dirs"].(bool); ok {
				createDirs = cd
			}

			err := actions.MoveFile(sourcePath, destPath, createDirs)
			if err != nil {
				res.Payload = map[string]any{"success": false, "error": err.Error()}
			} else {
				log.Printf("📦 Dosya Taşındı: %s -> %s", sourcePath, destPath)
				res.Payload = map[string]any{
					"success":          true,
					"source_path":      sourcePath,
					"destination_path": destPath,
				}
			}

		case "file.read":
			filePath, _ := params["file_path"].(string)
			encoding, _ := params["encoding"].(string)

			content, size, err := actions.ReadFile(filePath, encoding)
			if err != nil {
				res.Payload = map[string]any{"success": false, "error": err.Error()}
			} else {
				res.Payload = map[string]any{
					"success":   true,
					"content":   content,
					"file_size": size,
					"file_path": filePath,
				}
			}

		case "file.write":
			filePath, _ := params["file_path"].(string)
			content, _ := params["content"].(string)
			mode, _ := params["mode"].(string)

			err := actions.WriteFile(filePath, content, mode)
			if err != nil {
				res.Payload = map[string]any{"success": false, "error": err.Error()}
			} else {
				log.Printf("✍️ Dosya Yazıldı: %s", filePath)
				res.Payload = map[string]any{
					"success":   true,
					"file_path": filePath,
				}
			}

		default:
			res.Payload = map[string]any{
				"success": false,
				"error":   fmt.Sprintf("Bilinmeyen veya henüz desteklenmeyen eylem: %s", action),
			}
		}

		_ = safeWS.WriteJSON(res)
	}
}

// 3. Yardımcı Fonksiyonlar
func getWebSocketURL(serverURL, token string) string {
	u, err := url.Parse(serverURL)
	if err != nil {
		u, _ = url.Parse("http://localhost:8080")
	}

	wsScheme := "ws"
	if u.Scheme == "https" {
		wsScheme = "wss"
	}

	return fmt.Sprintf("%s://%s/api/v1/desktop-runners/ws?token=%s", wsScheme, u.Host, token)
}

func getConfigPath() string {
	var dir string
	if runtime.GOOS == "windows" {
		dir = os.Getenv("APPDATA")
		if dir == "" {
			dir = os.Getenv("USERPROFILE")
		}
		dir = filepath.Join(dir, "Spartask")
	} else {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".spartask")
	}

	_ = os.MkdirAll(dir, 0755)
	return filepath.Join(dir, "device.json")
}

func loadConfig(path string) (*DeviceConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var config DeviceConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

func saveConfig(path string, config *DeviceConfig) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func openBrowser(url string) {
	var err error
	switch runtime.GOOS {
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	case "linux":
		err = exec.Command("xdg-open", url).Start()
	default:
		err = fmt.Errorf("desteklenmeyen platform")
	}
	if err != nil {
		log.Printf("⚠️ Tarayıcı otomatik açılamadı: %v", err)
	}
}

func printBanner(deviceName, watchDir string) {
	fmt.Println("==================================================================")
	fmt.Printf("🖥️  SPARTASK ASİSTANI AKTİF | Cihaz: %s\n", deviceName)
	fmt.Printf("📁  İzlenen Klasör: %s\n", watchDir)
	fmt.Printf("🚀  Açılışta Başlatma: %v\n", autostart.IsEnabled())
	fmt.Println("Arka planda iş akışları ve ofis görevleri bekleniyor...")
	fmt.Println("Durdurmak için Ctrl+C tuşlarına basabilirsiniz.")
	fmt.Println("==================================================================")
}
