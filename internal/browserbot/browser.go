package browserbot

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// Config controls how the runner starts its own browser.
type Config struct {
	// ExecPath is an explicit Chrome/Edge executable. Empty means auto-detect.
	ExecPath string
	// ProfileDir is the runner-owned browser profile shared by recording and
	// replay, so a login made while recording stays valid for replays.
	ProfileDir string

	// headlessRecording lets package tests drive a recording without a window.
	headlessRecording bool
}

// DefaultProfileDir returns the runner-owned profile next to device.json.
func DefaultProfileDir() (string, error) {
	var base string
	if runtime.GOOS == "windows" {
		base = os.Getenv("APPDATA")
		if base == "" {
			base = os.Getenv("USERPROFILE")
		}
		base = filepath.Join(base, "Spartask")
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		base = filepath.Join(home, ".spartask")
	}
	return filepath.Join(base, "browser-profile"), nil
}

func browserCandidates() []string {
	switch runtime.GOOS {
	case "windows":
		var out []string
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LOCALAPPDATA"} {
			root := os.Getenv(env)
			if root == "" {
				continue
			}
			out = append(out, filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe"))
		}
		for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles"} {
			root := os.Getenv(env)
			if root == "" {
				continue
			}
			out = append(out, filepath.Join(root, "Microsoft", "Edge", "Application", "msedge.exe"))
		}
		return out
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
	default:
		return []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge"}
	}
}

// FindBrowser locates an installed Chromium-based browser.
func FindBrowser(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", botErr(CodeBrowserUnavailable, "browser path %s: %w", explicit, err)
		}
		return explicit, nil
	}
	for _, c := range browserCandidates() {
		if filepath.IsAbs(c) {
			if _, err := os.Stat(c); err == nil {
				return c, nil
			}
			continue
		}
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	return "", botErr(CodeBrowserUnavailable, "no Chrome or Edge installation was found")
}

const gracefulCloseTimeout = 10 * time.Second

// newBrowser starts a browser with the runner profile and returns a tab context
// and the function that closes the browser.
func newBrowser(parent context.Context, cfg Config, headless bool) (context.Context, func() error, error) {
	path, err := FindBrowser(cfg.ExecPath)
	if err != nil {
		return nil, nil, err
	}
	profile := cfg.ProfileDir
	if profile == "" {
		if profile, err = DefaultProfileDir(); err != nil {
			return nil, nil, err
		}
	}
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return nil, nil, fmt.Errorf("create browser profile %s: %w", profile, err)
	}
	opts := []chromedp.ExecAllocatorOption{
		chromedp.ExecPath(path),
		chromedp.UserDataDir(profile),
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.WindowSize(1280, 900),
		// One tab only: pop-ups and target=_blank cannot escape the origin guard.
		chromedp.Flag("block-new-web-contents", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("disable-features", "Translate,PasswordLeakDetection"),
		chromedp.Flag("password-store", "basic"),
		chromedp.Flag("use-mock-keychain", true),
	}
	if headless {
		opts = append(opts, chromedp.Headless)
	}
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(parent, opts...)
	tabCtx, cancelTab := chromedp.NewContext(allocCtx)
	if err := chromedp.Do(tabCtx); err != nil {
		cancelTab()
		cancelAlloc()
		return nil, nil, botErr(CodeBrowserUnavailable, "start browser %s: %w", path, err)
	}
	return tabCtx, closeGracefully(tabCtx, cancelTab, cancelAlloc), nil
}

// closeGracefully asks the browser to quit so it writes its cookies to the
// profile; a killed browser loses the login the next replay depends on. If the
// browser does not quit in time it is stopped.
func closeGracefully(tabCtx context.Context, cancelTab, cancelAlloc context.CancelFunc) func() error {
	return func() error {
		done := make(chan error, 1)
		go func() { done <- chromedp.Cancel(tabCtx) }()
		var err error
		select {
		case err = <-done:
			cancelTab()
			cancelAlloc()
		case <-time.After(gracefulCloseTimeout):
			cancelTab()
			cancelAlloc()
			<-done // Cancel returns once the contexts above end.
			err = fmt.Errorf("browser did not quit within %s and was stopped", gracefulCloseTimeout)
		}
		if err != nil {
			return fmt.Errorf("close browser: %w", err)
		}
		return nil
	}
}

// originGuard intercepts every document request of the tab. Main-frame
// navigations, including redirects, must stay on allowed origins; any frame
// must use http or https.
type originGuard struct {
	allowed     map[string]bool // nil allows every http/https origin
	mainFrameID string

	mu           sync.Mutex
	blocked      string
	interceptErr error
	visited      []string
	seen         map[string]bool
	inflight     sync.WaitGroup
}

func newOriginGuard(allowed []string) (*originGuard, error) {
	g := &originGuard{seen: map[string]bool{}}
	if allowed != nil {
		g.allowed = map[string]bool{}
		for _, o := range allowed {
			n, err := NormalizeOrigin(o)
			if err != nil {
				return nil, fmt.Errorf("allowed origin: %w", err)
			}
			g.allowed[n] = true
		}
	}
	return g, nil
}

func (g *originGuard) decide(rawURL string, mainFrame bool) bool {
	origin, err := NormalizeOrigin(rawURL)
	if err != nil {
		g.noteBlocked(rawURL)
		return false
	}
	if mainFrame && g.allowed != nil && !g.allowed[origin] {
		g.noteBlocked(origin)
		return false
	}
	if mainFrame {
		g.mu.Lock()
		if !g.seen[origin] {
			g.seen[origin] = true
			g.visited = append(g.visited, origin)
		}
		g.mu.Unlock()
	}
	return true
}

func (g *originGuard) noteBlocked(what string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.blocked == "" {
		g.blocked = what
	}
}

func (g *originGuard) blockedOrigin() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.blocked
}

func (g *originGuard) interceptionError() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.interceptErr
}

func (g *originGuard) visitedOrigins() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.visited...)
}

// install enables interception on the tab. The returned wait function blocks
// until every in-flight decision has been sent to the browser.
func (g *originGuard) install(ctx context.Context) error {
	c := chromedp.FromContext(ctx)
	if c == nil || c.Target == nil {
		return botErr(CodeBrowserError, "no browser target in context")
	}
	// A page target's main frame shares the target's id.
	g.mainFrameID = string(c.Target.TargetID)
	err := chromedp.Listen(ctx, func(ev any) {
		paused, ok := ev.(*fetch.EventRequestPaused)
		if !ok {
			return
		}
		allow := g.decide(paused.Request.URL, string(paused.FrameID) == g.mainFrameID)
		id := paused.RequestID
		// CDP calls must not run inside the listener; it would deadlock.
		g.inflight.Add(1)
		go func() {
			defer g.inflight.Done()
			var err error
			if allow {
				_, err = chromedp.Call(ctx, fetch.ContinueRequest, fetch.ContinueRequestParams{RequestID: id})
			} else {
				_, err = chromedp.Call(ctx, fetch.FailRequest, fetch.FailRequestParams{RequestID: id, ErrorReason: network.ErrorReasonBlockedByClient})
			}
			if err != nil && ctx.Err() == nil {
				g.mu.Lock()
				if g.interceptErr == nil {
					g.interceptErr = fmt.Errorf("resolve intercepted request: %w", err)
				}
				g.mu.Unlock()
			}
		}()
	})
	if err != nil {
		return botErr(CodeBrowserError, "listen for navigations: %w", err)
	}
	pattern := &fetch.RequestPattern{URLPattern: "*", ResourceType: network.ResourceTypeDocument, RequestStage: fetch.RequestStageRequest}
	if _, err := chromedp.Call(ctx, fetch.Enable, fetch.EnableParams{Patterns: []*fetch.RequestPattern{pattern}}); err != nil {
		return botErr(CodeBrowserError, "enable navigation guard: %w", err)
	}
	return nil
}

func (g *originGuard) wait() { g.inflight.Wait() }
