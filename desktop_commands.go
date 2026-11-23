package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/sptask/sptask_desktop_runner/internal/desktopbot"
)

const (
	actionDesktopReplay = "desktop.replay"
	actionDesktopStop   = "desktop.stop"
)

// dataDir is the folder of device.json: approvals, the browser profile and
// the outbox live next to it.
func dataDir() string { return filepath.Dir(getConfigPath()) }

// handleDesktopCommand answers desktop.* commands on a tracked goroutine.
func (a *runnerApp) handleDesktopCommand(msgID, action string, params map[string]any) {
	a.jobs.Add(1)
	go func() {
		defer a.jobs.Done()
		if a.desk == nil {
			a.reply(msgID, map[string]any{"success": false, "error_code": desktopbot.CodeDesktopError,
				"error": fmt.Sprintf("desktop automation is not available on this runner: %v", a.deskErr)})
			return
		}
		switch action {
		case actionDesktopReplay:
			a.startDesktopReplay(msgID, params)
		case actionDesktopStop:
			id, _ := params["job_id"].(string)
			a.reply(msgID, map[string]any{"success": true, "stopped": a.desk.Stop(id)})
		default:
			a.reply(msgID, map[string]any{"success": false, "error": "unknown desktop action " + action})
		}
	}()
}

func (a *runnerApp) startDesktopReplay(msgID string, params map[string]any) {
	var rec desktopbot.Recording
	if err := decodeParam(params, "recording", &rec); err != nil {
		a.reply(msgID, map[string]any{"success": false, "error": err.Error(), "error_code": desktopbot.CodeInvalidRecording})
		return
	}
	runID, _ := params["run_id"].(string)
	if runID == "" {
		runID = msgID
	}
	opts := desktopbot.ReplayOptions{Timeout: time.Duration(intParam(params, "timeout_sec")) * time.Second}
	if _, ok := params["operator"]; ok {
		if err := decodeParam(params, "operator", &opts.Operator); err != nil {
			a.reply(msgID, map[string]any{"success": false, "error": err.Error(), "error_code": desktopbot.CodeInvalidRecording})
			return
		}
	}
	err := a.desk.StartReplay(runID, rec, opts, func(res desktopbot.ReplayResult) {
		log.Printf("🖥️ Masaüstü oynatması bitti: %s başarı=%v %s", runID, res.Success, res.ErrorCode)
		payload, err := toPayload(res)
		if err != nil {
			a.reply(msgID, map[string]any{"success": false, "error": err.Error(), "error_code": desktopbot.CodeDesktopError})
			return
		}
		a.reply(msgID, payload)
	})
	if err != nil {
		a.reply(msgID, map[string]any{"success": false, "error": err.Error(), "error_code": desktopbot.ErrorCode(err, desktopbot.CodeDesktopError)})
	}
}

func toPayload(v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode result: %w", err)
	}
	payload := map[string]any{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("encode result: %w", err)
	}
	return payload, nil
}

// runAppApprovals manages, on this computer only, the programs the desktop
// bot may act in.
func runAppApprovals(approve, revoke string, list bool) error {
	policy, err := desktopbot.LoadPolicy(dataDir())
	if err != nil {
		return err
	}
	if approve != "" {
		if err := policy.Approve(approve); err != nil {
			return fmt.Errorf("approve %s: %w", approve, err)
		}
		fmt.Printf("✅ Onaylandı: %s\n", approve)
	}
	if revoke != "" {
		ok, err := policy.Revoke(revoke)
		if err != nil {
			return fmt.Errorf("revoke %s: %w", revoke, err)
		}
		if ok {
			fmt.Printf("🗑️ Onay kaldırıldı: %s\n", revoke)
		} else {
			fmt.Printf("ℹ️ Zaten onaylı değildi: %s\n", revoke)
		}
	}
	if list {
		apps := policy.Approved()
		if len(apps) == 0 {
			fmt.Println("Masaüstü botu için onaylı program yok.")
		}
		for _, p := range apps {
			fmt.Println(p)
		}
	}
	return nil
}

// runDevDesktopReplay replays a desktop recording file without pairing.
func runDevDesktopReplay(ctx context.Context, file string) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read %s: %w", file, err)
	}
	var rec desktopbot.Recording
	if err := json.Unmarshal(raw, &rec); err != nil {
		return fmt.Errorf("decode %s: %w", file, err)
	}
	policy, err := desktopbot.LoadPolicy(dataDir())
	if err != nil {
		return err
	}
	bot := desktopbot.NewManager(ctx, policy, runnerJobs)
	done := make(chan desktopbot.ReplayResult, 1)
	if err := bot.StartReplay("dev", rec, desktopbot.ReplayOptions{}, func(r desktopbot.ReplayResult) { done <- r }); err != nil {
		return fmt.Errorf("start replay: %w", err)
	}
	res := <-done
	bot.Wait()
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
