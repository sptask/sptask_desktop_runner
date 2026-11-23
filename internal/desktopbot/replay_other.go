//go:build !windows || !(amd64 || arm64)

package desktopbot

import "context"

// Desktop automation needs Windows UI Automation.
func runReplay(context.Context, *Policy, *Recording, ReplayOptions) ReplayResult {
	return ReplayResult{ErrorCode: CodeUnsupportedPlatform, Error: "desktop automation runs only on 64-bit Windows"}
}
