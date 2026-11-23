// Package joblock lets one automation job run on this computer at a time.
// The browser bot and the desktop bot share it: both act on the screen the
// person at the computer sees, and two bots at once would act on each other's
// windows.
package joblock

import "sync"

type Lock struct {
	mu     sync.Mutex
	holder string
}

// TryAcquire takes the lock for job. It returns the current holder when the
// lock is taken.
func (l *Lock) TryAcquire(job string) (release func(), holder string, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.holder != "" {
		return nil, l.holder, false
	}
	l.holder = job
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.holder = ""
		})
	}, "", true
}
