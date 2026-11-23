package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sptask/sptask_desktop_runner/internal/browserbot"
	"github.com/sptask/sptask_desktop_runner/internal/notify"
	"github.com/sptask/sptask_desktop_runner/internal/protocol"
)

const (
	actionBrowserRecordStart = "browser.record.start"
	actionBrowserReplay      = "browser.replay"
	actionBrowserStop        = "browser.stop"

	eventRecordingCompleted = "browser.recording_completed"
)

// runnerOptions are command-line settings shared by the runner's parts.
type runnerOptions struct {
	// dataDir replaces the user's Spartask folder (device.json, outbox,
	// browser profile), so a test runner never touches the real one.
	dataDir string
	// browserDebugPort opens the runner browser to local tools (tests only).
	browserDebugPort int
}

var runnerOpts runnerOptions

// browserConfig is the browser setup of this runner: the profile lives next
// to device.json.
func browserConfig(browserPath string) browserbot.Config {
	return browserbot.Config{
		ExecPath:   browserPath,
		ProfileDir: filepath.Join(filepath.Dir(getConfigPath()), "browser-profile"),
		DebugPort:  runnerOpts.browserDebugPort,
	}
}

// runnerApp holds what the command handlers share. Browser commands run on
// tracked goroutines so a long replay never blocks the WebSocket read loop.
type runnerApp struct {
	ws        *SafeWebSocket
	bot       *browserbot.Manager
	jobs      sync.WaitGroup
	outboxDir string
	outboxMu  sync.Mutex
}

func newRunnerApp(ctx context.Context, ws *SafeWebSocket, browserPath string) (*runnerApp, error) {
	outbox := filepath.Join(filepath.Dir(getConfigPath()), "outbox")
	if err := os.MkdirAll(outbox, 0o700); err != nil {
		return nil, fmt.Errorf("create outbox %s: %w", outbox, err)
	}
	return &runnerApp{
		ws:        ws,
		bot:       browserbot.NewManager(ctx, browserConfig(browserPath)),
		outboxDir: outbox,
	}, nil
}

// Wait returns after every browser job and command goroutine has ended.
func (a *runnerApp) Wait() {
	a.bot.Wait()
	a.jobs.Wait()
}

func (a *runnerApp) reply(id string, payload map[string]any) {
	msg := protocol.DesktopMessage{Type: protocol.MsgTypeCommandResult, ID: id, Timestamp: time.Now().Unix(), Payload: payload}
	if err := a.ws.WriteJSON(msg); err != nil {
		log.Printf("⚠️ Komut sonucu gönderilemedi (%s): %v", id, err)
	}
}

func failurePayload(err error, fallback string) map[string]any {
	return map[string]any{"success": false, "error": err.Error(), "error_code": browserbot.ErrorCode(err, fallback)}
}

func decodeParam(params map[string]any, key string, out any) error {
	raw, err := json.Marshal(params[key])
	if err != nil {
		return fmt.Errorf("encode %s: %w", key, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode %s: %w", key, err)
	}
	return nil
}

func intParam(params map[string]any, key string) int {
	if v, ok := params[key].(float64); ok {
		return int(v)
	}
	return 0
}

// handleBrowserCommand answers browser.* commands. It returns at once; the
// work and the reply happen on a tracked goroutine.
func (a *runnerApp) handleBrowserCommand(msgID, action string, params map[string]any) {
	a.jobs.Add(1)
	go func() {
		defer a.jobs.Done()
		switch action {
		case actionBrowserRecordStart:
			a.startRecording(msgID, params)
		case actionBrowserReplay:
			a.startReplay(msgID, params)
		case actionBrowserStop:
			id, _ := params["job_id"].(string)
			a.reply(msgID, map[string]any{"success": true, "stopped": a.bot.Stop(id)})
		default:
			a.reply(msgID, map[string]any{"success": false, "error": "unknown browser action " + action})
		}
	}()
}

func (a *runnerApp) startRecording(msgID string, params map[string]any) {
	id, _ := params["recording_id"].(string)
	startURL, _ := params["start_url"].(string)
	if strings.TrimSpace(id) == "" {
		a.reply(msgID, map[string]any{"success": false, "error": "recording_id is required", "error_code": browserbot.CodeInvalidRecording})
		return
	}
	maxDuration := time.Duration(intParam(params, "max_duration_sec")) * time.Second
	err := a.bot.StartRecording(id, startURL, maxDuration, func(rec browserbot.Recording) {
		log.Printf("🎬 Tarayıcı kaydı tamamlandı: %s (%d adım)", rec.ID, len(rec.Steps))
		a.deliverEvent(eventRecordingCompleted, rec.ID, map[string]any{"recording": rec})
		notify.ShowNotification("Spartask Asistanı", "Tarayıcı kaydınız Spartask'a gönderildi.")
	})
	if err != nil {
		log.Printf("❌ Tarayıcı kaydı başlatılamadı: %v", err)
		a.reply(msgID, failurePayload(err, browserbot.CodeBrowserError))
		return
	}
	log.Printf("🎬 Tarayıcı kaydı başladı: %s → %s", id, startURL)
	notify.ShowNotification("Spartask Asistanı", "Kayıt başladı. İşinizi yapın, bitince tarayıcı penceresini kapatın.")
	a.reply(msgID, map[string]any{"success": true, "recording_id": id})
}

func (a *runnerApp) startReplay(msgID string, params map[string]any) {
	var rec browserbot.Recording
	if err := decodeParam(params, "recording", &rec); err != nil {
		a.reply(msgID, map[string]any{"success": false, "error": err.Error(), "error_code": browserbot.CodeInvalidRecording})
		return
	}
	runID, _ := params["run_id"].(string)
	if runID == "" {
		runID = msgID
	}
	headless, _ := params["headless"].(bool)
	opts := browserbot.ReplayOptions{Headless: headless, Timeout: time.Duration(intParam(params, "timeout_sec")) * time.Second}
	if _, ok := params["operator"]; ok {
		if err := decodeParam(params, "operator", &opts.Operator); err != nil {
			a.reply(msgID, map[string]any{"success": false, "error": err.Error(), "error_code": browserbot.CodeInvalidRecording})
			return
		}
	}
	err := a.bot.StartReplay(runID, rec, opts, func(res browserbot.ReplayResult) {
		log.Printf("🤖 Tarayıcı oynatması bitti: %s başarı=%v %s", runID, res.Success, res.ErrorCode)
		raw, err := json.Marshal(res)
		if err != nil {
			a.reply(msgID, map[string]any{"success": false, "error": fmt.Sprintf("encode replay result: %v", err), "error_code": browserbot.CodeBrowserError})
			return
		}
		payload := map[string]any{}
		if err := json.Unmarshal(raw, &payload); err != nil {
			a.reply(msgID, map[string]any{"success": false, "error": fmt.Sprintf("encode replay result: %v", err), "error_code": browserbot.CodeBrowserError})
			return
		}
		a.reply(msgID, payload)
	})
	if err != nil {
		a.reply(msgID, failurePayload(err, browserbot.CodeBrowserError))
	}
}

// deliverEvent writes the event to the outbox first, so a recording made while
// the connection is down reaches the server after the next reconnect.
func (a *runnerApp) deliverEvent(name, key string, data map[string]any) {
	msg := protocol.DesktopMessage{
		Type:      protocol.MsgTypeEvent,
		ID:        fmt.Sprintf("evt_%d", time.Now().UnixNano()),
		Timestamp: time.Now().Unix(),
		Payload:   map[string]any{"event_name": name, "data": data},
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		log.Printf("❌ Olay kodlanamadı (%s): %v", name, err)
		return
	}
	path := filepath.Join(a.outboxDir, fmt.Sprintf("%d_%s.json", time.Now().UnixNano(), safeFileName(key)))
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		log.Printf("❌ Olay kutusuna yazılamadı (%s): %v", path, err)
		return
	}
	a.flushOutbox()
}

func safeFileName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, s)
}

// flushOutbox sends stored events in order and deletes each one that was sent.
func (a *runnerApp) flushOutbox() {
	a.outboxMu.Lock()
	defer a.outboxMu.Unlock()
	entries, err := os.ReadDir(a.outboxDir)
	if err != nil {
		log.Printf("⚠️ Olay kutusu okunamadı: %v", err)
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(a.outboxDir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			log.Printf("⚠️ Bekleyen olay okunamadı (%s): %v", name, err)
			continue
		}
		var msg protocol.DesktopMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.Printf("⚠️ Bozuk bekleyen olay atlandı (%s): %v", name, err)
			continue
		}
		if err := a.ws.WriteJSON(msg); err != nil {
			log.Printf("⏳ Olay bağlantı gelince gönderilecek (%s): %v", name, err)
			return
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("⚠️ Gönderilen olay silinemedi (%s): %v", name, err)
		}
	}
}

// runDevRecord records without pairing and writes the recording to out.
func runDevRecord(ctx context.Context, browserPath, startURL, out string) error {
	bot := browserbot.NewManager(ctx, browserConfig(browserPath))
	done := make(chan browserbot.Recording, 1)
	if err := bot.StartRecording("dev", startURL, 0, func(r browserbot.Recording) { done <- r }); err != nil {
		return fmt.Errorf("start recording: %w", err)
	}
	fmt.Println("🎬 Kayıt başladı. İşinizi yapın ve bitince tarayıcı penceresini kapatın.")
	var rec browserbot.Recording
	select {
	case rec = <-done:
	case <-ctx.Done():
		bot.Stop("")
		rec = <-done
	}
	bot.Wait()
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("encode recording: %w", err)
	}
	if err := os.WriteFile(out, raw, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", out, err)
	}
	fmt.Printf("✅ %d adım kaydedildi: %s\n", len(rec.Steps), out)
	return nil
}

// runDevReplay replays a recording file without pairing and prints the result.
func runDevReplay(ctx context.Context, browserPath, file string, headless bool) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read %s: %w", file, err)
	}
	var rec browserbot.Recording
	if err := json.Unmarshal(raw, &rec); err != nil {
		return fmt.Errorf("decode %s: %w", file, err)
	}
	bot := browserbot.NewManager(ctx, browserConfig(browserPath))
	done := make(chan browserbot.ReplayResult, 1)
	if err := bot.StartReplay("dev", rec, browserbot.ReplayOptions{Headless: headless}, func(r browserbot.ReplayResult) { done <- r }); err != nil {
		return fmt.Errorf("start replay: %w", err)
	}
	res := <-done
	bot.Wait()
	res.Screenshot = fmt.Sprintf("<%d base64 karakter>", len(res.Screenshot))
	out, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	fmt.Println(string(out))
	if !res.Success {
		return fmt.Errorf("replay failed: %s", res.ErrorCode)
	}
	return nil
}
