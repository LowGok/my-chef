package billing

import (
	"context"
	"time"
)

const (
	playPullWake       = 15 * time.Second
	playQueueWake      = time.Minute
	playDrainBudget    = 9 * time.Minute
	playUnhealthyAfter = 3
)

type PlayStage string

const (
	PlayCatalogStage PlayStage = "play_sandbox_catalog"
	PlayPullStage    PlayStage = "play_notice_pull"
	PlayNoticeStage  PlayStage = "play_notice_queue"
	PlayKnownStage   PlayStage = "play_known_subscriptions"
)

// PlayWorkerHealth contains aggregate status only. Stage reports can be called
// from either lane, so the reporting function must be safe for concurrent use.
type PlayWorkerHealth struct {
	Stage       PlayStage
	Degraded    bool
	Outstanding int
}

type PlayService struct {
	pull   func(context.Context) (int, error)
	drain  func(context.Context) (ReconcileBatch, error)
	known  func(context.Context) (ReconcileBatch, error)
	report func(PlayWorkerHealth)
}

// NewPlayService keeps the worker disabled unless the deployment explicitly
// supplies both the Play connection and an authenticated Pub/Sub pull source.
func NewPlayService(pull *PlayPull, inbox *PlayInbox,
	withOwner func(context.Context, string, func() error) error, report func(PlayWorkerHealth)) (*PlayService, error) {
	if inbox == nil && pull == nil {
		return nil, nil
	}
	if inbox == nil || pull == nil || withOwner == nil {
		return nil, ErrStoreUnavailable
	}
	provider, ok := inbox.provider.(*PlayProvider)
	if !ok || provider == nil || inbox.env != provider.cfg.Environment || pull.cfg.Package != provider.cfg.Package {
		return nil, ErrStoreUnavailable
	}
	runner := PlayReconciler{Inbox: inbox, Acknowledge: provider.Acknowledge, AcknowledgeReplacement: provider.AcknowledgeReplacement, WithLiveOwner: withOwner}
	return &PlayService{
		pull: func(ctx context.Context) (int, error) {
			return pull.Poll(ctx, func(ctx context.Context, notice PlayNotice) error {
				return inbox.Retain(ctx, notice, time.Now())
			})
		},
		drain: runner.RunBatch, known: runner.PollKnown, report: report,
	}, nil
}

func (s *PlayService) Run(ctx context.Context) {
	if s == nil {
		return
	}
	pull, retry, known := time.NewTicker(playPullWake), time.NewTicker(playQueueWake), time.NewTicker(playQueueWake)
	defer pull.Stop()
	defer retry.Stop()
	defer known.Stop()
	s.run(ctx, pull.C, retry.C, known.C)
}

func (s *PlayService) run(ctx context.Context, pull, retry, known <-chan time.Time) {
	wake := make(chan struct{}, 1)
	pullDone := make(chan struct{})
	go func() {
		defer close(pullDone)
		var health playStageHealth
		for ctx.Err() == nil {
			n, err := s.pull(ctx)
			if n > 0 && ctx.Err() == nil {
				select {
				case wake <- struct{}{}:
				default:
				}
			}
			if ctx.Err() == nil {
				s.record(&health, PlayPullStage, 0, err)
			}
			select {
			case <-ctx.Done():
				return
			case <-pull:
			}
		}
	}()
	defer func() { <-pullDone }()
	knownDone := make(chan struct{})
	go func() {
		defer close(knownDone)
		var health playStageHealth
		for ctx.Err() == nil {
			s.work(ctx, PlayKnownStage, s.known, &health)
			select {
			case <-ctx.Done():
				return
			case <-known:
			}
		}
	}()
	defer func() { <-knownDone }()
	var health playStageHealth
	for ctx.Err() == nil {
		s.work(ctx, PlayNoticeStage, s.drain, &health)
		select {
		case <-ctx.Done():
			return
		case <-retry:
		case <-wake:
		}
	}
}

func (s *PlayService) work(ctx context.Context, stage PlayStage, run func(context.Context) (ReconcileBatch, error), health *playStageHealth) {
	work, cancel := context.WithTimeout(ctx, playDrainBudget)
	batch, err := run(work)
	if err == nil {
		err = work.Err()
	}
	cancel()
	if ctx.Err() == nil {
		s.record(health, stage, batch.Failed, err)
	}
}

type playStageHealth struct {
	consecutive int
	degraded    bool
}

func (s *PlayService) record(health *playStageHealth, stage PlayStage, failed int, err error) {
	if err != nil {
		health.consecutive = min(health.consecutive+1, playUnhealthyAfter)
	} else {
		health.consecutive = 0
	}
	degraded := failed > 0 || health.consecutive >= playUnhealthyAfter || err != nil && health.degraded
	if degraded != health.degraded && s.report != nil {
		s.report(PlayWorkerHealth{Stage: stage, Degraded: degraded, Outstanding: failed})
	}
	health.degraded = degraded
}
