package billing

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAppleWorkerRunsAllPathsAndReportsOnlyAggregateChanges(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	providerFault := errors.New("store details never belong in a health event")
	var attempts []AppleStage
	var records []AppleWorkerHealth
	historyFails := true
	pollFailures := 3
	w := &AppleWorker{
		history: func(ctx context.Context) (ReconcileBatch, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("unbounded history request")
			}
			attempts = append(attempts, AppleHistory)
			if historyFails {
				return ReconcileBatch{}, providerFault
			}
			return ReconcileBatch{}, nil
		},
		poll: func(context.Context) (ReconcileBatch, error) {
			attempts = append(attempts, AppleKnownSubscriptions)
			return ReconcileBatch{Failed: pollFailures}, nil
		},
		drain: func(context.Context) (ReconcileBatch, error) {
			attempts = append(attempts, AppleNoticeQueue)
			return ReconcileBatch{}, nil
		},
		report: func(event AppleWorkerHealth) { records = append(records, event) },
	}
	health := make(map[AppleStage]appleStageHealth)
	for range 5 {
		w.refresh(ctx, health)
	}
	if len(attempts) != 10 || attempts[0] != AppleHistory || attempts[1] != AppleKnownSubscriptions {
		t.Fatal("history failure prevented independent poll", attempts)
	}
	if len(records) != 2 || records[0] != (AppleWorkerHealth{Stage: AppleKnownSubscriptions, Degraded: true, Outstanding: 3}) ||
		records[1] != (AppleWorkerHealth{Stage: AppleHistory, Degraded: true}) {
		t.Fatalf("did not deduplicate alert or report a sustained failure as counts only: %+v", records)
	}
	historyFails = false
	pollFailures = 0
	w.refresh(ctx, health)
	if len(records) != 4 || records[2] != (AppleWorkerHealth{Stage: AppleHistory}) || records[3] != (AppleWorkerHealth{Stage: AppleKnownSubscriptions}) {
		t.Fatalf("worker did not report recovery: %+v", records)
	}
	cancel()
	w.refresh(ctx, health)
	if len(attempts) != 12 {
		t.Fatal("cancelled service started store I/O", attempts)
	}
}

func TestAppleWorkerDrainsAtStartupAndOnRetryTickThenJoinsShutdown(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	refresh := make(chan time.Time, 1)
	retry := make(chan time.Time, 1)
	observed := make(chan AppleStage, 10)
	callback := func(stage AppleStage) func(context.Context) (ReconcileBatch, error) {
		return func(c context.Context) (ReconcileBatch, error) { observed <- stage; return ReconcileBatch{}, c.Err() }
	}
	w := AppleWorker{history: callback(AppleHistory), poll: callback(AppleKnownSubscriptions), drain: callback(AppleNoticeQueue)}
	done := make(chan struct{})
	go func() { defer close(done); w.run(ctx, refresh, retry) }()
	expect := func(stage AppleStage) {
		t.Helper()
		select {
		case got := <-observed:
			if got != stage {
				t.Fatalf("wanted %q, saw %q", stage, got)
			}
		case <-ctx.Done():
			t.Fatal("worker did not progress", ctx.Err())
		}
	}
	startup := map[AppleStage]int{}
	for range 3 {
		select {
		case stage := <-observed:
			startup[stage]++
		case <-ctx.Done():
			t.Fatal("worker did not start", ctx.Err())
		}
	}
	if startup[AppleHistory] != 1 || startup[AppleKnownSubscriptions] != 1 || startup[AppleNoticeQueue] != 1 {
		t.Fatal("worker did not run all three startup paths", startup)
	}
	retry <- time.Now()
	expect(AppleNoticeQueue)
	refresh <- time.Now()
	expect(AppleHistory)
	expect(AppleKnownSubscriptions)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("billing worker did not stop")
	}
	if len(observed) != 0 {
		t.Fatal("cancelled worker started another store call")
	}
}

func TestAppleWorkerDefaultsOffAndCancelsBothLanes(t *testing.T) {
	disabled, err := NewAppleWorker(nil, nil, nil, nil, nil)
	if err != nil || disabled != nil {
		t.Fatal("billing worker started without store configuration", err)
	}
	disabled.Run(t.Context())
	_, journal, _ := fixture(t)
	if _, err := NewAppleWorker(&AppleProvider{}, journal, []Offer{{Store: Apple}}, nil, nil); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatal("worker was configured without the account fence", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	started, exited := make(chan struct{}, 2), make(chan struct{}, 2)
	block := func(ctx context.Context) (ReconcileBatch, error) {
		started <- struct{}{}
		<-ctx.Done()
		exited <- struct{}{}
		return ReconcileBatch{}, ctx.Err()
	}
	w := AppleWorker{history: block, drain: block, poll: func(context.Context) (ReconcileBatch, error) {
		t.Error("shutdown started the next scheduled call")
		return ReconcileBatch{}, nil
	}}
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("both lanes did not start independently", ctx.Err())
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not cancel active calls")
	}
	if len(exited) != 2 {
		t.Fatal("worker returned before its calls completed")
	}
}
