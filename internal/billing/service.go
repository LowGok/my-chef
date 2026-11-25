package billing

import (
	"context"
	"time"
)

type AppleStage string

const (
	AppleHistory            AppleStage = "notification_history"
	AppleKnownSubscriptions AppleStage = "known_subscriptions"
	AppleNoticeQueue        AppleStage = "notification_queue"
	appleQueueWake                     = time.Minute
	appleHistoryBudget                 = 75 * time.Second
	applePollBudget                    = 9 * time.Minute
	appleDrainBudget                   = 9 * time.Minute
	appleUnhealthyAfter                = 3
)

// AppleWorkerHealth is safe for service logs and metrics. Provider messages,
// purchase IDs, account bindings and proof never leave the worker.
type AppleWorkerHealth struct {
	Stage       AppleStage
	Degraded    bool
	Outstanding int
}

type appleStageHealth struct {
	failures int
	degraded bool
}

type appleStageRun func(context.Context) (ReconcileBatch, error)

type AppleWorker struct {
	history, poll, drain appleStageRun
	report               func(AppleWorkerHealth)
}

// NewAppleWorker creates the service loop only from a configured store
// adapter and a restore-safe lifecycle fence. Nil provider keeps it disabled.
// The health callback must be safe to call from both worker lanes.
func NewAppleWorker(provider *AppleProvider, j *Journal, offers []Offer, withOwner func(context.Context, string, func() error) error, report func(AppleWorkerHealth)) (*AppleWorker, error) {
	if provider == nil {
		return nil, nil
	}
	if j == nil || withOwner == nil || len(offers) == 0 {
		return nil, ErrStoreUnavailable
	}
	r := AppleReconciler{Journal: j, Environment: provider.cfg.Environment, Provider: provider,
		Offers: append([]Offer(nil), offers...), WithLiveOwner: withOwner}
	return &AppleWorker{
		history: func(ctx context.Context) (ReconcileBatch, error) {
			return ReconcileBatch{}, provider.SyncNotificationHistory(ctx, j)
		},
		poll: r.PollKnown, drain: r.RunBatch, report: report,
	}, nil
}

// Run performs an immediate reconciliation after restart, then polls every
// 15 minutes. The independent queue lane handles due retries without waiting
// for Apple history requests. Both lanes exit before Run returns.
func (w *AppleWorker) Run(ctx context.Context) {
	if w == nil {
		return
	}
	refresh, retry := time.NewTicker(applePollInterval), time.NewTicker(appleQueueWake)
	defer refresh.Stop()
	defer retry.Stop()
	w.run(ctx, refresh.C, retry.C)
}

func (w *AppleWorker) run(ctx context.Context, refresh, retry <-chan time.Time) {
	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		health := make(map[AppleStage]appleStageHealth)
		w.refresh(ctx, health)
		for {
			select {
			case <-ctx.Done():
				return
			case <-refresh:
				w.refresh(ctx, health)
			}
		}
	}()
	defer func() { <-refreshDone }()
	health := make(map[AppleStage]appleStageHealth)
	w.runStage(ctx, health, AppleNoticeQueue, appleDrainBudget, w.drain)
	for {
		select {
		case <-ctx.Done():
			return
		case <-retry:
			w.runStage(ctx, health, AppleNoticeQueue, appleDrainBudget, w.drain)
		}
	}
}

func (w *AppleWorker) refresh(ctx context.Context, health map[AppleStage]appleStageHealth) {
	w.runStage(ctx, health, AppleHistory, appleHistoryBudget, w.history)
	w.runStage(ctx, health, AppleKnownSubscriptions, applePollBudget, w.poll)
}

func (w *AppleWorker) runStage(ctx context.Context, health map[AppleStage]appleStageHealth, stage AppleStage, budget time.Duration, run appleStageRun) {
	if ctx.Err() != nil {
		return
	}
	stageCtx, cancel := context.WithTimeout(ctx, budget)
	batch, err := run(stageCtx)
	if err == nil {
		err = stageCtx.Err()
	}
	cancel()
	if ctx.Err() != nil {
		return
	}
	previous := health[stage]
	current := appleStageHealth{}
	if err != nil {
		current.failures = min(previous.failures+1, appleUnhealthyAfter)
	}
	current.degraded = batch.Failed > 0 || current.failures >= appleUnhealthyAfter || err != nil && previous.degraded
	health[stage] = current
	if w.report != nil && current.degraded != previous.degraded {
		w.report(AppleWorkerHealth{Stage: stage, Degraded: current.degraded, Outstanding: batch.Failed})
	}
}
