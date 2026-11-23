package desktopbot

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/sptask/sptask_desktop_runner/internal/joblock"
	"github.com/sptask/sptask_desktop_runner/internal/operator"
)

const (
	DefaultReplayTimeout = 5 * time.Minute
	MaxReplayTimeout     = 15 * time.Minute
)

type ReplayOptions struct {
	Timeout  time.Duration
	Operator operator.Operator
}

// Manager runs desktop replays and recordings one at a time, sharing the
// runner's job lock with the browser bot. Every job goroutine is tracked;
// Wait returns after the last one has ended.
type Manager struct {
	base   context.Context
	policy *Policy
	jobs   *joblock.Lock

	mu     sync.Mutex
	cancel map[string]context.CancelFunc
	wg     sync.WaitGroup
}

func NewManager(base context.Context, policy *Policy, jobs *joblock.Lock) *Manager {
	return &Manager{base: base, policy: policy, jobs: jobs, cancel: map[string]context.CancelFunc{}}
}

// Wait blocks until every replay and recording goroutine has returned.
func (m *Manager) Wait() { m.wg.Wait() }

// Stop ends the replay or recording id, or every one when id is empty. A
// stopped recording still delivers what was recorded.
func (m *Manager) Stop(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	stopped := false
	for jobID, cancel := range m.cancel {
		if id == "" || jobID == id {
			cancel()
			stopped = true
		}
	}
	return stopped
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
	if m.base.Err() != nil {
		return botErr(CodeCancelled, "the runner is shutting down")
	}
	release, holder, ok := m.jobs.TryAcquire("desktop replay " + id)
	if !ok {
		return botErr(CodeBusy, "another job (%s) is running", holder)
	}
	ctx, cancel := context.WithTimeout(m.base, opts.Timeout)
	m.mu.Lock()
	m.cancel[id] = cancel
	m.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer release()
		defer func() {
			m.mu.Lock()
			delete(m.cancel, id)
			m.mu.Unlock()
			cancel()
		}()
		res := runReplay(ctx, m.policy, &rec, opts)
		if !res.Success && m.base.Err() != nil {
			res.ErrorCode = CodeCancelled
		}
		onDone(res)
	}()
	return nil
}

// StartRecording records, until Stop, Shift+Esc or the time limit, what the
// person at this computer does in the programs of apps that are also
// approved here. onDone always receives the recording, with what was done up
// to the end.
func (m *Manager) StartRecording(id string, apps []string, opts RecordOptions, onDone func(Recording)) error {
	if strings.TrimSpace(id) == "" {
		return botErr(CodeInvalidRecording, "recording_id is required")
	}
	list, err := normalizeApps(apps)
	if err != nil {
		return botErr(CodeInvalidRecording, "%w", err)
	}
	approved := false
	for _, app := range list {
		if deniedApps[app] {
			return botErr(CodeAppBlocked, "%s can never be automated", app)
		}
		if m.policy.approvesName(app) {
			approved = true
		}
	}
	if !approved {
		return botErr(CodeAppNotApproved, "none of %s is approved on this computer (sptask-runner --approve-app)", strings.Join(list, ", "))
	}
	if opts.MaxDuration <= 0 {
		opts.MaxDuration = DefaultMaxRecording
	}
	if opts.MaxDuration > MaxRecordingDuration {
		opts.MaxDuration = MaxRecordingDuration
	}
	if m.base.Err() != nil {
		return botErr(CodeCancelled, "the runner is shutting down")
	}
	release, holder, ok := m.jobs.TryAcquire("desktop recording " + id)
	if !ok {
		return botErr(CodeBusy, "another job (%s) is running", holder)
	}
	ctx, cancel := context.WithTimeout(m.base, opts.MaxDuration)
	rec, err := startRecorder(ctx, cancel, m.policy, id, list)
	if err != nil {
		cancel()
		release()
		return err
	}
	m.mu.Lock()
	m.cancel[id] = cancel
	m.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer release()
		defer func() {
			m.mu.Lock()
			delete(m.cancel, id)
			m.mu.Unlock()
			cancel()
		}()
		onDone(rec.wait())
	}()
	return nil
}
