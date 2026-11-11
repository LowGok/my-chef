package httpapi

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPasswordLimitRollingWindowsAndReset(t *testing.T) {
	now := time.Unix(1000, 0)
	limits := passwordLimiter{now: func() time.Time { return now }}
	allow := func(account int64, source string, outcome passwordAttemptOutcome) {
		t.Helper()
		finish, retry := limits.begin(account, source)
		if finish == nil || retry != 0 {
			t.Fatalf("expected a slot, retry %d", retry)
		}
		finish(outcome)
	}
	block := func(account int64, source string, wait int) {
		t.Helper()
		finish, retry := limits.begin(account, source)
		if finish != nil || retry != wait {
			t.Fatalf("expected retry %d, got %d", wait, retry)
		}
	}
	for range 5 {
		allow(1, "first", passwordWrong)
	}
	block(1, "first", 900)
	allow(3, "first", passwordUncounted)
	now = now.Add(2 * time.Minute)
	for range 15 {
		allow(2, "second", passwordUncounted)
	}
	block(1, "second", 900)
	block(1, "third", 780)
	now = now.Add(13*time.Minute - time.Millisecond)
	block(1, "third", 1)
	now = now.Add(time.Millisecond)
	for range 4 {
		allow(1, "first", passwordWrong)
	}
	allow(1, "first", passwordChanged)
	for range 5 {
		allow(1, "first", passwordWrong)
	}
	block(1, "first", 900)
	// The account-blocked request still counted against the source.
	for range 4 {
		allow(4, "first", passwordUncounted)
	}
	block(4, "first", 900)
	now = now.Add(15 * time.Minute)
	allow(4, "first", passwordUncounted)
}

func TestPasswordLimitReservesSlotsBeforeConcurrentVerification(t *testing.T) {
	limits := passwordLimiter{now: func() time.Time { return time.Unix(1000, 0) }}
	var attempts sync.WaitGroup
	var rejected atomic.Int32
	held := make(chan func(passwordAttemptOutcome), 12)
	for range 12 {
		attempts.Go(func() {
			finish, retry := limits.begin(1, "same-source")
			if finish != nil {
				held <- finish
			} else if retry == 1 {
				rejected.Add(1)
			}
		})
	}
	attempts.Wait()
	if len(held) != 5 || rejected.Load() != 7 {
		t.Fatal("concurrent verification exceeded the account budget")
	}
	finish := <-held
	finish(passwordUncounted) // Context cancellation releases a reservation.
	reopened, retry := limits.begin(1, "same-source")
	if reopened == nil || retry != 0 {
		t.Fatal("canceled verification did not release its slot")
	}
	reopened(passwordWrong)
	close(held)
	for finish := range held {
		finish(passwordWrong)
	}
	if finish, retry := limits.begin(1, "other-source"); finish != nil || retry != 900 {
		t.Fatal("five verified failures did not block the account across sources")
	}
}

func TestPasswordLimitBoundsConcurrentSources(t *testing.T) {
	limits := passwordLimiter{now: func() time.Time { return time.Unix(1000, 0) }}
	var attempts sync.WaitGroup
	var accepted, rejected atomic.Int32
	for i := range 30 {
		attempts.Go(func() {
			finish, retry := limits.begin(int64(i+1), "shared-source")
			switch {
			case finish != nil:
				accepted.Add(1)
				finish(passwordUncounted)
			case retry == 900:
				rejected.Add(1)
			}
		})
	}
	attempts.Wait()
	if accepted.Load() != 15 || rejected.Load() != 15 {
		t.Fatal("parallel accounts bypassed the shared source budget")
	}
}
