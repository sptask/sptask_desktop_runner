//go:build !windows || !(amd64 || arm64)

package desktopbot

import "context"

type recorder struct{}

// Teaching needs Windows UI Automation.
func startRecorder(context.Context, context.CancelFunc, *Policy, string, []string) (*recorder, error) {
	return nil, botErr(CodeUnsupportedPlatform, "desktop recording runs only on 64-bit Windows")
}

func (*recorder) wait() Recording { return Recording{} }
