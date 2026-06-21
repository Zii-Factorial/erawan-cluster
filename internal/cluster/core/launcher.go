package core

import (
	"context"
	"sync"
)

// Launcher runs background cluster jobs with a bounded concurrency limit and
// tracks them so they can be drained on shutdown.
//
// Each cluster job spawns a heavyweight ansible-playbook process; without a cap,
// a burst of API calls would launch an unbounded number of them and exhaust CPU
// and memory. The concurrency slot is acquired *inside* the spawned goroutine,
// so callers (HTTP handlers) return immediately with a 202 while excess jobs
// queue cheaply on the semaphore. A limit <= 0 means unbounded.
type Launcher struct {
	sem chan struct{}
	wg  sync.WaitGroup
}

/**
 * NewLauncher returns a Launcher that runs at most maxConcurrent jobs at once
 * (<= 0 for unbounded).
 *
 * Params:
 *   maxConcurrent int - the maxConcurrent value
 *
 * Returns:
 *   *Launcher - the resulting *Launcher
 */
func NewLauncher(maxConcurrent int) *Launcher {
	var sem chan struct{}
	if maxConcurrent > 0 {
		sem = make(chan struct{}, maxConcurrent)
	}
	return &Launcher{sem: sem}
}

/**
 * Go runs fn in a tracked background goroutine, waiting (in that goroutine) for
 * a free concurrency slot first. Safe for concurrent use.
 *
 * Receiver:
 *   l *Launcher - pointer receiver; the method may mutate this Launcher instance
 *
 * Params:
 *   fn func() - the fn (func())
 */
func (l *Launcher) Go(fn func()) {
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		if l.sem != nil {
			l.sem <- struct{}{}
			defer func() { <-l.sem }()
		}
		fn()
	}()
}

/**
 * Wait blocks until all launched jobs finish or ctx is done, whichever comes
 * first. Used during graceful shutdown to drain in-flight jobs.
 *
 * Receiver:
 *   l *Launcher - pointer receiver; the method may mutate this Launcher instance
 *
 * Params:
 *   ctx context.Context - context carrying cancellation signals and deadlines
 */
func (l *Launcher) Wait(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		l.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
