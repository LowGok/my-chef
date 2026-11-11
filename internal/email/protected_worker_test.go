package email

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/email/mailqueue"
	httpapi "github.com/LowGok/my-chef/internal/http"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

type proofSender struct {
	bodies [][]byte
	result deliveryResult
}

func (*proofSender) bindingID() string { return "protected-test" }
func (s *proofSender) send(_ context.Context, _ string, body []byte) deliveryResult {
	s.bodies = append(s.bodies, bytes.Clone(body))
	return s.result
}

func protectedFixture(t *testing.T) (*store.Store, mailqueue.Request, time.Time, *mailqueue.ProofKeys) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "protected.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	keys, err := mailqueue.NewProofKeys("first", map[string][]byte{"first": bytes.Repeat([]byte{1}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	return s, mailqueue.Request{
		Key: "signup/fixture", Recipient: "recipient@example.test", Template: mailqueue.SignUpProofTemplate,
		Fields: map[string]string{"link": "https://mychef.example.test/app/signup#N1ngJYuGtWdZT7czVq0pcc7bcLSeTog-L9VQq9YR73Q"},
	}, now, keys
}

func TestProtectedProofRetriesWithOriginalCiphertextAndErasesOnAcceptance(t *testing.T) {
	ctx := context.Background()
	s, req, now, keys := protectedFixture(t)
	until := now.Add(time.Hour)
	if _, err := (Queue{Store: s}).Enqueue(ctx, nil, req); err == nil {
		t.Fatal("ordinary queue accepted a protected template")
	}
	id, err := mailqueue.EnqueueProtected(ctx, s, nil, now, until, keys, req)
	if err != nil {
		t.Fatal(err)
	}
	read := func(s *store.Store) outbox {
		t.Helper()
		var row outbox
		if err := s.DB.First(&row, id).Error; err != nil {
			t.Fatal(err)
		}
		return row
	}
	initial := read(s)
	if strings.Contains(initial.Data, req.Fields["link"]) || strings.Contains(initial.Data, strings.Split(req.Fields["link"], "#")[1]) {
		t.Fatal("stored data contains the proof URL")
	}
	second, err := mailqueue.NewProofKeys("second", map[string][]byte{
		"first": bytes.Repeat([]byte{1}, 32), "second": bytes.Repeat([]byte{2}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	if again, err := mailqueue.EnqueueProtected(ctx, s, nil, now, until, second, req); err != nil || again != id || read(s).Data != initial.Data {
		t.Fatal("replay did not reuse the first committed ciphertext")
	}
	p := &proofSender{result: deliveryResult{temporary: true}}
	newWorker := func(db *store.Store, ring *mailqueue.ProofKeys) *Worker {
		return &Worker{Queue: Queue{Store: db, Now: func() time.Time { return now }, ProofKeys: ring},
			sender: p, From: "MyChef <hello@example.test>", FingerprintKey: bytes.Repeat([]byte{3}, 32),
			Random: func() float64 { return .5 }}
	}
	if worked, err := newWorker(s, keys).Step(ctx); err != nil || !worked || len(p.bodies) != 1 {
		t.Fatalf("first protected send: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(s.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	now = now.Add(time.Minute)
	p.result = deliveryResult{id: "accepted-fixture"}
	if worked, err := newWorker(reopened, second).Step(ctx); err != nil || !worked || len(p.bodies) != 2 ||
		!bytes.Equal(p.bodies[0], p.bodies[1]) || !bytes.Contains(p.bodies[1], []byte(req.Fields["link"])) {
		t.Fatalf("protected retry did not preserve the rendered request: %v", err)
	}
	accepted := read(reopened)
	if accepted.State != "accepted" || strings.Contains(accepted.Data, "ciphertext") || accepted.Data == initial.Data {
		t.Fatal("provider acceptance did not erase the protected material")
	}
	if again, err := mailqueue.EnqueueProtected(ctx, reopened, nil, now, until, second, req); err != nil || again != id || read(reopened).Data != accepted.Data {
		t.Fatal("replaying an accepted message restored its secret")
	}
	req.Fields["link"] = strings.Replace(req.Fields["link"], "#N", "#X", 1)
	if _, err := mailqueue.EnqueueProtected(ctx, reopened, nil, now, until, second, req); !errors.Is(err, ErrKeyConflict) {
		t.Fatal("a different proof reused a terminal identity")
	}
}

func TestProtectedProofErasesOnExpiryOrMissingKeyWithoutSending(t *testing.T) {
	for _, scenario := range []string{"expired", "missing key", "invalidated", "rejected", "no loopback exception"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			s, req, now, keys := protectedFixture(t)
			if scenario == "no loopback exception" {
				req.Fields["link"] = strings.Replace(req.Fields["link"], "https://mychef.example.test", "http://127.0.0.1:18138", 1)
			}
			until := now.Add(time.Minute)
			id, err := mailqueue.EnqueueProtected(ctx, s, nil, now, until, keys, req)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "expired":
				now = until
			case "missing key":
				keys, err = mailqueue.NewProofKeys("second", map[string][]byte{"second": bytes.Repeat([]byte{2}, 32)})
				if err != nil {
					t.Fatal(err)
				}
			case "invalidated":
				if err := mailqueue.InvalidateProtected(ctx, s.DB, now, req.Key); err != nil {
					t.Fatal(err)
				}
			}
			p := &proofSender{}
			w := Worker{Queue: Queue{Store: s, Now: func() time.Time { return now }, ProofKeys: keys},
				sender: p, FingerprintKey: bytes.Repeat([]byte{3}, 32)}
			if _, err := w.Step(ctx); err != nil {
				t.Fatal(err)
			}
			var row outbox
			wantSends := 0
			if scenario == "rejected" {
				wantSends = 1
			}
			if err := s.DB.First(&row, id).Error; err != nil || row.State != "failed" ||
				strings.Contains(row.Data, "ciphertext") || len(p.bodies) != wantSends {
				t.Fatalf("terminal protected row was not handled safely: %v", err)
			}
		})
	}
}

type stalledProofSender struct {
	started chan struct{}
	release chan struct{}
}

func (*stalledProofSender) bindingID() string { return "stalled-proof-test" }
func (s *stalledProofSender) send(ctx context.Context, _ string, _ []byte) deliveryResult {
	close(s.started)
	select {
	case <-s.release:
		return deliveryResult{id: "accepted-after-expiry"}
	case <-ctx.Done():
		return deliveryResult{temporary: true}
	}
}

func TestRecoverySweepErasesExpiredMailWhileProviderIsStalled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, req, start, keys := protectedFixture(t)
	start = start.Add(100 * time.Millisecond)
	expires := start.Add(800 * time.Millisecond) // Expiry falls within the same second.
	user := store.User{FirstName: "Recovery", LastName: "Fixture", Email: req.Recipient, Username: "recovery-fixture", Password: "fixture-hash"}
	if err := s.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	req.Template = mailqueue.RecoveryProofTemplate
	req.Fields["link"] = strings.Replace(req.Fields["link"], "/signup#", "/reset-password#", 1)
	var ids []int64
	tag := strings.Repeat("a", 64)
	for i, hash := range []string{strings.Repeat("1", 64), strings.Repeat("2", 64)} {
		order := int64(i + 1)
		req.Key = mailqueue.RecoveryMailKey(order)
		id, err := mailqueue.EnqueueProtected(ctx, s, nil, start, expires, keys, req)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.DB.Create(&store.RecoveryProof{Hash: hash, AcceptedOrder: order, UserID: user.ID, AddressTag: tag,
			IssuedNS: start.UnixNano(), ExpiresNS: expires.UnixNano()}).Error; err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := s.DB.Create(&store.RecoveryJob{AcceptedOrder: 3, AddressTag: tag, ExpiresNS: expires.UnixNano(), Draft: "fixture-ciphertext"}).Error; err != nil {
		t.Fatal(err)
	}

	var clock atomic.Int64
	clock.Store(start.UnixNano())
	provider := &stalledProofSender{started: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	unblock := func() { release.Do(func() { close(provider.release) }) }
	defer unblock()
	w := Worker{Queue: Queue{Store: s, Now: func() time.Time { return time.Unix(0, clock.Load()) }, ProofKeys: keys},
		sender: provider, From: "MyChef <hello@example.test>", FingerprintKey: bytes.Repeat([]byte{3}, 32)}
	done := make(chan error, 1)
	go func() {
		_, err := w.Step(ctx)
		done <- err
	}()
	select {
	case <-provider.started:
	case err := <-done:
		t.Fatalf("provider was never called: %v", err)
	case <-ctx.Done():
		t.Fatal("provider did not receive the first queued message")
	}
	for i, id := range ids {
		var row outbox
		want := []string{"sending", "queued"}[i]
		if err := s.DB.First(&row, id).Error; err != nil || row.State != want || !strings.Contains(row.Data, "ciphertext") ||
			strings.Contains(row.Data, req.Fields["link"]) {
			t.Fatal("fixture did not have one stalled send and one unselected encrypted message")
		}
	}
	var unselected outbox
	if err := s.DB.First(&unselected, ids[1]).Error; err != nil {
		t.Fatal(err)
	}
	fields, _, err := mailqueue.OpenProtected(keys, unselected, expires.Add(-time.Nanosecond), false)
	if err != nil || fields["link"] != req.Fields["link"] {
		t.Fatal("the recovery link became unavailable before its precise expiry")
	}
	if _, _, err := mailqueue.OpenProtected(keys, unselected, expires, false); !errors.Is(err, mailqueue.ErrProofExpired) {
		t.Fatal("the recovery link remained deliverable at its precise expiry")
	}
	clock.Store(expires.UnixNano())
	if err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, err := mailqueue.SweepRecoverySlice(ctx, tx, expires)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, model := range []any{&store.RecoveryJob{}, &store.RecoveryProof{}} {
		var count int64
		if err := s.DB.Model(model).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("expired recovery material remained while provider was stalled")
		}
	}
	checkErased := func() {
		t.Helper()
		for _, id := range ids {
			var row outbox
			if err := s.DB.First(&row, id).Error; err != nil || row.State != "failed" || row.Reason != "proof_expired" ||
				strings.Contains(row.Data, "ciphertext") || row.ProviderID != nil {
				t.Fatal("expired recovery email retained usable material or was reactivated")
			}
		}
	}
	checkErased()
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("provider did not finish after release")
	}
	checkErased()
}

func TestRecoveryAcknowledgementIsIndependentOfStalledProvider(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	s, req, now, keys := protectedFixture(t)
	if _, err := mailqueue.EnqueueProtected(ctx, s, nil, now, now.Add(time.Hour), keys, req); err != nil {
		t.Fatal("could not queue protected fixture mail")
	}
	verifiedAt := now.Unix()
	if err := s.DB.Create(&[]store.User{
		{FirstName: "Test", LastName: "Cook", Email: "verified@example.test", EmailKey: "verified@example.test", Username: "fixture-verified", Password: "fixture", EmailVerifiedAt: &verifiedAt},
		{FirstName: "Test", LastName: "Cook", Email: "restricted@example.test", EmailKey: "restricted@example.test", Username: "fixture-restricted", Password: "fixture"},
	}).Error; err != nil {
		t.Fatal("could not seed the account states")
	}
	work := &store.RecoveryWork{}
	origin := "https://mychef.example.test"
	r := &auth.Recovery{SignUp: &auth.SignUp{Store: s, Keys: keys, RateKey: bytes.Repeat([]byte{4}, 32),
		Origin: origin, Now: func() time.Time { return now }}, Work: work}
	a := httpapi.New(s, nil, httpapi.Config{Origin: origin, Recovery: r}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cookie, err := a.Auth.Rotate(ctx, nil, "")
	if err != nil {
		t.Fatal("could not create anonymous fixture session")
	}
	h := a.Handler(nil)
	provider := &stalledProofSender{started: make(chan struct{}), release: make(chan struct{})}
	defer close(provider.release)
	w := Worker{Queue: Queue{Store: s, Now: func() time.Time { return now }, ProofKeys: keys, RecoveryWork: work},
		sender: provider, From: "MyChef <hello@example.test>", FingerprintKey: bytes.Repeat([]byte{3}, 32)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = w.Step(ctx)
	}()
	defer func() { cancel(); <-done }()
	select {
	case <-provider.started:
	case <-ctx.Done():
		t.Fatal("fake provider never received the mail")
	}
	for _, address := range []string{"verified@example.test", "restricted@example.test", "unknown@example.test"} {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/password-recovery/request", strings.NewReader(`{"email":"`+address+`"}`))
		request.AddCookie(&http.Cookie{Name: "mychef_session", Value: cookie})
		request.Header.Set("Origin", origin)
		request.Header.Set("X-CSRF-Token", auth.CSRF(cookie))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)
		if response.Code != 202 || response.Body.String() != "{\"status\":\"accepted\"}\n" ||
			response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("stalled provider changed the uniform acknowledgement")
		}
	}
	var pending int64
	if err := s.DB.Model(&store.RecoveryJob{}).Count(&pending).Error; err != nil || pending != 3 {
		t.Fatal("the intake path did not commit all three jobs during a stalled send")
	}
}
