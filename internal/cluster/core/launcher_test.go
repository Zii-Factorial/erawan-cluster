package core

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLauncherCapsConcurrency(t *testing.T) {
	const limit = 2
	l := NewLauncher(limit)

	var current, peak int64
	var mu sync.Mutex
	release := make(chan struct{})

	for i := 0; i < 6; i++ {
		l.Go(func() {
			n := atomic.AddInt64(&current, 1)
			mu.Lock()
			if n > peak {
				peak = n
			}
			mu.Unlock()
			<-release // hold the slot until released
			atomic.AddInt64(&current, -1)
		})
	}

	// Give queued goroutines time to acquire whatever slots they can.
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt64(&current); got > limit {
		t.Fatalf("expected at most %d concurrent jobs, got %d", limit, got)
	}

	close(release)
	l.Wait(context.Background())

	if peak > limit {
		t.Fatalf("peak concurrency %d exceeded limit %d", peak, limit)
	}
	if peak == 0 {
		t.Fatal("expected jobs to run")
	}
}

func TestLauncherWaitRespectsContext(t *testing.T) {
	l := NewLauncher(1)
	block := make(chan struct{})
	l.Go(func() { <-block }) // never finishes during the test

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	l.Wait(ctx) // must return when ctx expires, not block forever
	if time.Since(start) > time.Second {
		t.Fatal("Wait did not return when context expired")
	}
	close(block)
}
