//go:build windows && (amd64 || arm64)

package desktopbot

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
)

func runReplay(ctx context.Context, policy *Policy, rec *Recording, opts ReplayOptions) ReplayResult {
	allowed := make(map[string]bool, len(rec.AllowedApps))
	for _, a := range rec.AllowedApps {
		n, err := NormalizeApp(a)
		if err != nil {
			return ReplayResult{ErrorCode: CodeInvalidRecording, Error: err.Error()}
		}
		allowed[n] = true
	}
	if workstationLocked() {
		return ReplayResult{ErrorCode: CodeWorkstationLocked, Error: "the computer is locked; the bot cannot see the program"}
	}
	u, err := startUIA()
	if err != nil {
		return ReplayResult{ErrorCode: CodeDesktopError, Error: fmt.Sprintf("start UI Automation: %v", err)}
	}
	defer u.close()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var stoppedByUser atomic.Bool
	show, err := startOverlay(opts.Operator, func() {
		stoppedByUser.Store(true)
		cancel()
	})
	if err != nil {
		// Without the overlay nobody at the computer would know or could stop it.
		return ReplayResult{ErrorCode: ErrorCode(err, CodeDesktopError), Error: fmt.Sprintf("show the operator on screen: %v", err)}
	}
	p := &player{u: u, policy: policy, allowed: allowed, show: show, outputs: map[string]string{}}
	res := p.replay(runCtx, rec)
	show.close()
	res.Warnings = append(res.Warnings, show.takeWarnings()...)
	if stoppedByUser.Load() && !res.Success {
		res.ErrorCode = CodeStoppedByUser
		res.Error = "the person at the computer took over (a click, a key or Shift+Esc)"
	} else if !res.Success && res.ErrorCode == CodeCancelled && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		res.ErrorCode = CodeTimeout
	}
	return res
}
