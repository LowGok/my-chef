package email

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/LowGok/my-chef/internal/config"
)

type fakeSender struct{ inbox *FixtureInbox }

func (fakeSender) bindingID() string { return "local-fake" }
func (p fakeSender) send(_ context.Context, key string, body []byte) deliveryResult {
	p.inbox.record(key, body)
	id := sha256.Sum256([]byte(key))
	return deliveryResult{id: hex.EncodeToString(id[:])}
}

type Service struct {
	disabled bool
	worker   Worker
	webhook  http.Handler
	failed   atomic.Bool
	inbox    *FixtureInbox
}

func New(q Queue, cfg config.Email) *Service {
	return NewWithFixtureInbox(q, cfg, nil)
}

func NewWithFixtureInbox(q Queue, cfg config.Email, inbox *FixtureInbox) *Service {
	if cfg.Disabled {
		inbox.close()
		return &Service{disabled: true, worker: Worker{Queue: q}}
	}
	var p sender
	if cfg.Fake {
		p = fakeSender{inbox: inbox}
		cfg.FingerprintKey = []byte("local-fake-fingerprint-key-only-000")
		cfg.WebhookKey = []byte("local-fake-webhook-key-only")
	} else {
		inbox = nil
		p = resend{endpoint: resendEndpoint, key: cfg.APIKey, client: &http.Client{
			Timeout:       sendTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}}
	}
	q.AllowLoopbackProofLinks = inbox != nil && inbox.settings.AllowLoopbackLinks
	return &Service{worker: Worker{Queue: q, sender: p, From: cfg.From, ReplyTo: cfg.ReplyTo, FingerprintKey: cfg.FingerprintKey}, webhook: q.Webhook(cfg.WebhookKey), inbox: inbox}
}

func (s *Service) Webhook() http.Handler { return s.webhook }

func (s *Service) FixtureInbox() http.Handler {
	if s.inbox == nil {
		return nil
	}
	return s.inbox
}

func (s *Service) Ready(ctx context.Context) bool {
	if s.failed.Load() {
		return false
	}
	return s.worker.Queue.Store.SQL.QueryRowContext(ctx, "SELECT count(*) FROM (SELECT id FROM email_outbox LIMIT 1)").Scan(new(int)) == nil
}

func (s *Service) Run(ctx context.Context) {
	if s.disabled {
		<-ctx.Done()
		return
	}
	defer s.inbox.close()
	defer func() {
		if recover() != nil || ctx.Err() == nil {
			s.failed.Store(true)
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		s.inbox.prune()
		worked, err := s.worker.Step(ctx)
		if err != nil {
			return
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Smoke submits only the requested row. The command holds the app lock.
func (s *Service) Smoke(ctx context.Context, req Request) error {
	if s.disabled {
		return errStorage
	}
	id, err := s.worker.Queue.Enqueue(ctx, nil, req)
	if err != nil {
		return err
	}
	s.worker.onlyID = id
	if _, err := s.worker.Step(ctx); err != nil {
		return err
	}
	var row outbox
	if err := s.worker.Queue.Store.DB.WithContext(ctx).First(&row, id).Error; err != nil || row.State != "accepted" {
		return errStorage
	}
	return nil
}
