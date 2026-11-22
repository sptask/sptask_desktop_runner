package browserbot

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
)

const (
	JobRecord = "record"
	JobReplay = "replay"

	startNavigationTimeout = 30 * time.Second
	DefaultMaxRecording    = 30 * time.Minute
	DefaultReplayTimeout   = 5 * time.Minute
	MaxReplayTimeout       = 15 * time.Minute
)

// ReplayOptions tune one replay.
type ReplayOptions struct {
	Headless bool
	Timeout  time.Duration
}

type activeJob struct {
	id     string
	kind   string
	cancel context.CancelFunc // ends a replay
	stop   chan struct{}      // finishes a recording and keeps its steps
	tabCtx context.Context    // the recording tab, read by package tests
}

// Manager runs at most one browser job at a time, because recording and
// replay share one browser profile. Every job goroutine is tracked, so Wait
// returns only after the browser has closed.
type Manager struct {
	cfg  Config
	base context.Context

	mu     sync.Mutex
	active *activeJob
	wg     sync.WaitGroup
}

func NewManager(base context.Context, cfg Config) *Manager {
	return &Manager{cfg: cfg, base: base}
}

func (m *Manager) claim(id, kind string) (*activeJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.base.Err() != nil {
		return nil, botErr(CodeCancelled, "the runner is shutting down")
	}
	if m.active != nil {
		return nil, botErr(CodeBusy, "another browser job (%s %s) is running", m.active.kind, m.active.id)
	}
	job := &activeJob{id: id, kind: kind, stop: make(chan struct{})}
	m.active = job
	return job, nil
}

func (m *Manager) release(job *activeJob) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == job {
		m.active = nil
	}
}

// Stop ends the running job when its id matches, or any job when id is empty.
// A recording keeps the steps made so far; a replay fails with CANCELLED.
func (m *Manager) Stop(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.active
	if job == nil || (id != "" && job.id != id) {
		return false
	}
	if job.cancel != nil {
		job.cancel()
	}
	select {
	case <-job.stop:
	default:
		close(job.stop)
	}
	return true
}

// Wait blocks until every job goroutine has returned.
func (m *Manager) Wait() { m.wg.Wait() }

// StartRecording opens a visible browser on startURL and returns once the page
// has loaded. The recording continues in the background; onDone receives it
// when the user closes the browser, Stop is called, maxDuration passes or the
// runner shuts down.
func (m *Manager) StartRecording(id, startURL string, maxDuration time.Duration, onDone func(Recording)) error {
	if _, err := NormalizeOrigin(startURL); err != nil {
		return botErr(CodeInvalidRecording, "start url: %w", err)
	}
	if maxDuration <= 0 {
		maxDuration = DefaultMaxRecording
	}
	job, err := m.claim(id, JobRecord)
	if err != nil {
		return err
	}
	jobCtx, cancelJob := context.WithCancel(m.base)
	tabCtx, closeBrowser, err := newBrowser(jobCtx, m.cfg, m.cfg.headlessRecording)
	if err != nil {
		cancelJob()
		m.release(job)
		return err
	}
	m.mu.Lock()
	job.tabCtx = tabCtx
	m.mu.Unlock()
	guard, rec := &originGuard{seen: map[string]bool{}}, &recorder{skipNav: true}
	fail := func(err error) error {
		err = errors.Join(err, closeBrowser())
		guard.wait()
		cancelJob()
		m.release(job)
		return err
	}
	if err := guard.install(tabCtx); err != nil {
		return fail(err)
	}
	if err := rec.install(tabCtx); err != nil {
		return fail(err)
	}
	navCtx, cancelNav := context.WithTimeout(tabCtx, startNavigationTimeout)
	err = chromedp.Do(navCtx, chromedp.Navigate(startURL))
	cancelNav()
	if err != nil {
		return fail(botErr(CodeBrowserError, "open %s: %w", startURL, err))
	}
	detached, err := chromedp.Detached(tabCtx)
	if err != nil {
		return fail(botErr(CodeBrowserError, "watch browser window: %w", err))
	}

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		timer := time.NewTimer(maxDuration)
		defer timer.Stop()
		userClosed := false
		select {
		case <-detached:
			userClosed = true
		case <-job.stop:
		case <-timer.C:
			rec.warn(fmt.Sprintf("recording stopped after the %s limit", maxDuration))
		case <-tabCtx.Done():
		}
		startOrigin, _ := NormalizeOrigin(startURL)
		origins := []string{startOrigin}
		for _, o := range guard.visitedOrigins() {
			if o != startOrigin {
				origins = append(origins, o)
			}
		}
		if len(origins) > MaxAllowedOrigins {
			origins = origins[:MaxAllowedOrigins]
			rec.warn("only the first allowed origins were kept")
		}
		result := rec.result(id, startURL, origins)
		// A window the user closed has nothing left to close.
		if err := closeBrowser(); err != nil && !userClosed {
			result.Warnings = append(result.Warnings, err.Error())
		}
		guard.wait()
		cancelJob()
		m.release(job)
		onDone(result)
	}()
	return nil
}

// StartReplay validates the recording and replays it in the background.
// onDone always receives a result, also for failures.
func (m *Manager) StartReplay(id string, rec Recording, opts ReplayOptions, onDone func(ReplayResult)) error {
	if err := rec.Validate(); err != nil {
		return botErr(CodeInvalidRecording, "%w", err)
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultReplayTimeout
	}
	if opts.Timeout > MaxReplayTimeout {
		opts.Timeout = MaxReplayTimeout
	}
	job, err := m.claim(id, JobReplay)
	if err != nil {
		return err
	}
	jobCtx, cancelJob := context.WithCancel(m.base)
	m.mu.Lock()
	job.cancel = cancelJob
	m.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer m.release(job)
		defer cancelJob()
		onDone(m.runReplay(jobCtx, &rec, opts))
	}()
	return nil
}

func (m *Manager) runReplay(jobCtx context.Context, rec *Recording, opts ReplayOptions) ReplayResult {
	fail := func(err error, fallback string) ReplayResult {
		return ReplayResult{ErrorCode: ErrorCode(err, fallback), Error: err.Error()}
	}
	guard, err := newOriginGuard(rec.AllowedOrigins)
	if err != nil {
		return fail(err, CodeInvalidRecording)
	}
	tabCtx, closeBrowser, err := newBrowser(jobCtx, m.cfg, opts.Headless)
	if err != nil {
		return fail(err, CodeBrowserUnavailable)
	}
	finish := func(res ReplayResult) ReplayResult {
		// Closing writes the session cookies; a failure here does not undo the
		// replay, so it is reported next to the result.
		if err := closeBrowser(); err != nil {
			res.Warnings = append(res.Warnings, err.Error())
		}
		guard.wait()
		return res
	}
	if err := guard.install(tabCtx); err != nil {
		return finish(fail(err, CodeBrowserError))
	}
	nav := newNavWatcher()
	if err := nav.install(tabCtx); err != nil {
		return finish(fail(err, CodeBrowserError))
	}
	runCtx, cancelRun := context.WithTimeout(tabCtx, opts.Timeout)
	res := replay(runCtx, rec, guard, nav)
	cancelRun()
	if !res.Success && jobCtx.Err() != nil {
		res.ErrorCode = CodeCancelled
	}
	return finish(res)
}
