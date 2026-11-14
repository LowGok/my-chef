package newsletter

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"
)

// The first journal query runs inside Step's read fence. Queue a writer there
// to exercise the interleaving without sleeps or production instrumentation.
type retirementContext struct {
	context.Context
	once  sync.Once
	queue func()
}

func (c *retirementContext) Done() <-chan struct{} {
	c.once.Do(c.queue)
	return c.Context.Done()
}

func TestCaptureCompletesWithRetirementWaitingAtReadiness(t *testing.T) {
	s, _ := fixture(t)
	j, err := s.Store.InitializeLifecycleJournal(t.Context(), s.Store.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	s.Lifecycle = j
	request(t, s, "queued@example.test", "en")
	writer := make(chan struct{})
	queued := make(chan struct{})
	ctx := &retirementContext{Context: t.Context()}
	ctx.queue = func() {
		go func() {
			s.Store.AccountWork.Lock()
			defer s.Store.AccountWork.Unlock()
			close(writer)
		}()
		deadline := time.Now().Add(2 * time.Second)
		for s.Store.AccountWork.TryRLock() {
			s.Store.AccountWork.RUnlock()
			if time.Now().After(deadline) {
				return
			}
			runtime.Gosched()
		}
		close(queued)
	}
	done := make(chan error, 1)
	go func() {
		worked, err := s.Step(ctx)
		if !worked && err == nil {
			done <- context.Canceled
			return
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("capture deadlocked while retirement waited for its read fence")
	}
	select {
	case <-writer:
	case <-time.After(time.Second):
		t.Fatal("retirement remained blocked after capture")
	}
	select {
	case <-queued:
	default:
		t.Fatal("the retirement writer did not queue during readiness")
	}
}
