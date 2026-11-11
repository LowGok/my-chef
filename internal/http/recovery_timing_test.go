package httpapi

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

type timingContextKey int

const (
	timingIntake timingContextKey = iota
	timingWorker
)

type recoveryTimingSample struct {
	written        chan struct{}
	sessionStarted chan struct{}
	sessionWait    atomic.Int64
	guardDeadline  atomic.Int64
	accountReads   atomic.Int32
	overBudget     atomic.Bool
	jobWrites      atomic.Int32
	sessionReads   atomic.Int32
	sessionStart   time.Time // only accessed by the request goroutine
	positive       bool
}

type recoveryTimingResult struct {
	elapsed time.Duration
	err     error
}

type recoveryTimingWorker struct {
	account chan struct{}
	release chan struct{}
}

type timingResponse struct {
	http.ResponseWriter
	delay time.Duration
}

func (w timingResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w timingResponse) WriteHeader(status int) {
	if status == http.StatusAccepted && w.delay != 0 {
		time.Sleep(w.delay)
	}
	w.ResponseWriter.WriteHeader(status)
}

type recoveryTimingHarness struct {
	api    *API
	client *http.Client
	origin string
	cookie string
	active sync.Map
	next   atomic.Uint32
}

func newRecoveryTimingHarness(t *testing.T, count int) *recoveryTimingHarness {
	t.Helper()
	ctx := t.Context()
	a, _, cookie := recoveryAPI(t)
	a.Config.TrustedProxies = []netip.Addr{netip.MustParseAddr("127.0.0.1")}
	h := &recoveryTimingHarness{api: a, cookie: cookie}
	verified := time.Now().Unix()
	// One address per sample keeps the rolling windows independent of ordering.
	for state := range 2 {
		err := a.Store.DB.Transaction(func(tx *gorm.DB) error {
			for i := range count {
				email := recoveryTimingEmail(state, i)
				u := store.User{Email: email, EmailKey: email, Username: fmt.Sprintf("timing-%d-%d", state, i), FirstName: "Test", LastName: "Cook", Password: "fixture-does-not-authenticate"}
				if state == 0 {
					u.EmailVerifiedAt = &verified
				}
				if err := tx.Create(&u).Error; err != nil {
					return err
				}
				if state == 0 {
					if _, err := store.AdvanceRecoveryFence(ctx, tx, u.ID); err != nil {
						return err
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal("cannot seed timing accounts")
		}
	}
	onQuery := func(db *gorm.DB) {
		if sample, ok := db.Statement.Context.Value(timingIntake).(*recoveryTimingSample); ok {
			switch db.Statement.Table {
			case "users":
				sample.accountReads.Add(1)
			case "sessions":
				if sample.sessionReads.Add(1) == 1 {
					sample.sessionStart = time.Now()
					if deadline, ok := db.Statement.Context.Deadline(); ok {
						sample.guardDeadline.Store(int64(time.Until(deadline)))
					}
					close(sample.sessionStarted)
				}
			}
		}
		if gate, ok := db.Statement.Context.Value(timingWorker).(*recoveryTimingWorker); ok && db.Statement.Table == "users" {
			close(gate.account)
			select {
			case <-gate.release:
			case <-db.Statement.Context.Done():
				_ = db.AddError(db.Statement.Context.Err())
			}
		}
	}
	db := a.Store.DB.Callback()
	if err := db.Query().Before("gorm:query").Register("recovery-timing/query", onQuery); err != nil {
		t.Fatal(err)
	}
	if err := db.Query().After("gorm:query").Register("recovery-timing/session-result", func(db *gorm.DB) {
		if sample, ok := db.Statement.Context.Value(timingIntake).(*recoveryTimingSample); ok && db.Statement.Table == "sessions" && !sample.sessionStart.IsZero() {
			sample.sessionWait.Store(int64(time.Since(sample.sessionStart)))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Row().Before("gorm:row").Register("recovery-timing/raw-query", func(db *gorm.DB) {
		if sample, ok := db.Statement.Context.Value(timingIntake).(*recoveryTimingSample); ok && strings.Contains(strings.ToLower(db.Statement.SQL.String()), "users") {
			sample.accountReads.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create().After("gorm:create").Register("recovery-timing/job", func(db *gorm.DB) {
		if sample, ok := db.Statement.Context.Value(timingIntake).(*recoveryTimingSample); ok && db.Statement.Table == "recovery_jobs" {
			if sample.jobWrites.Add(1) == 1 {
				sample.overBudget.Store(db.Statement.Context.Err() != nil)
				close(sample.written)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	app := a.Handler(nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, exists := h.active.Load(r.Header.Get("X-Fixture-Sample"))
		if !exists {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		sample := v.(*recoveryTimingSample)
		if sample.positive {
			w = timingResponse{ResponseWriter: w, delay: 100 * time.Millisecond}
		}
		app.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), timingIntake, sample)))
	}))
	t.Cleanup(server.Close)
	h.origin = server.URL
	h.client = server.Client()
	h.client.Timeout = 3 * time.Second
	return h
}

func recoveryTimingEmail(state, n int) string {
	return fmt.Sprintf("timing-%d-%04d@example.test", state, n)
}

func (h *recoveryTimingHarness) start(ctx context.Context, address string, positive bool) (*recoveryTimingSample, <-chan recoveryTimingResult) {
	sample := &recoveryTimingSample{written: make(chan struct{}), sessionStarted: make(chan struct{}), positive: positive}
	id := h.next.Add(1)
	key := strconv.FormatUint(uint64(id), 10)
	h.active.Store(key, sample)
	done := make(chan recoveryTimingResult, 1)
	go func() {
		defer h.active.Delete(key)
		elapsed, err := h.call(ctx, key, id, address, sample)
		done <- recoveryTimingResult{elapsed, err}
	}()
	return sample, done
}

func (h *recoveryTimingHarness) call(ctx context.Context, key string, id uint32, address string, sample *recoveryTimingSample) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.origin+requestRecoveryPath, strings.NewReader(`{"email":"`+address+`"}`))
	if err != nil {
		return 0, fmt.Errorf("could not construct intake request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Fixture-Sample", key)
	req.Header.Set("Origin", h.api.Config.Origin)
	req.Header.Set("X-CSRF-Token", auth.CSRF(h.cookie))
	req.Header.Set("X-Forwarded-For", fmt.Sprintf("2001:db8::%x", id))
	req.AddCookie(&http.Cookie{Name: "mychef_session", Value: h.cookie})
	start := time.Now()
	resp, err := h.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("loopback acknowledgement transport failed")
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	elapsed := time.Since(start)
	if err != nil || resp.StatusCode != 202 || string(body) != "{\"status\":\"accepted\"}\n" {
		return 0, fmt.Errorf("unexpected acknowledgement status or body: %d", resp.StatusCode)
	}
	for name, value := range map[string]string{
		"Cache-Control": "no-store", "Referrer-Policy": "no-referrer", "Content-Type": "application/json",
		"X-Content-Type-Options": "nosniff", "X-MyChef-User": "", "Set-Cookie": "", "Retry-After": "",
	} {
		if resp.Header.Get(name) != value {
			return 0, fmt.Errorf("acknowledgement changed invariant header %s", name)
		}
	}
	csp := "default-src 'self'; img-src 'self' https: data:; style-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
	requestID := resp.Header.Get("X-Request-ID")
	if len(requestID) != 32 || strings.Trim(requestID, "0123456789abcdef") != "" || resp.Header.Get("Content-Security-Policy") != csp {
		return 0, fmt.Errorf("acknowledgement missing standard headers")
	}
	budget := time.Duration(sample.guardDeadline.Load())
	if elapsed < recoveryResponseFloor || budget <= 0 || budget > recoveryIntakeBudget ||
		sample.overBudget.Load() || sample.accountReads.Load() != 0 || sample.jobWrites.Load() != 1 || sample.sessionReads.Load() != 1 {
		return 0, fmt.Errorf("intake exceeded its budget, skipped the floor or changed operations")
	}
	return elapsed, nil
}

type recoveryTimingGroups [3][]time.Duration

func timingAUC(first, second []time.Duration) float64 {
	var score float64
	for _, a := range first {
		for _, b := range second {
			if a > b {
				score++
			} else if a == b {
				score += 0.5
			}
		}
	}
	return score / (float64(len(first)) * float64(len(second)))
}

func timingSeparated(groups recoveryTimingGroups) ([3]float64, bool) {
	values := [3]float64{timingAUC(groups[0], groups[1]), timingAUC(groups[0], groups[2]), timingAUC(groups[1], groups[2])}
	for _, value := range values {
		if value < 0.40 || value > 0.60 {
			return values, true
		}
	}
	return values, false
}

func timingOrder(n int) []int {
	states := make([]int, 3*n)
	// Randomize balanced blocks so slow drift cannot align with address state.
	const block = 8
	r := rand.New(rand.NewPCG(3119, 927))
	for start := 0; start < n; start += block {
		size := min(block, n-start)
		partition := states[3*start : 3*(start+size)]
		for i := range partition {
			partition[i] = i % 3
		}
		r.Shuffle(len(partition), func(i, j int) { partition[i], partition[j] = partition[j], partition[i] })
	}
	return states
}

func waitRecoveryTiming(t *testing.T, label string, ready <-chan struct{}) {
	t.Helper()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatalf("timing fixture did not reach %s", label)
	}
}

func recoveryTimingElapsed(t *testing.T, done <-chan recoveryTimingResult) time.Duration {
	t.Helper()
	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	return result.elapsed
}

func TestRecoveryTimingGate(t *testing.T) {
	if os.Getenv("MYCHEF_RECOVERY_TIMING") != "1" {
		t.Skip("set MYCHEF_RECOVERY_TIMING=1 for the full loopback timing gate")
	}
	const n = 200
	for _, mode := range []string{"idle", "eight concurrent", "worker active", "positive control"} {
		t.Run(mode, func(t *testing.T) {
			count, batch := n, 1
			if mode == "eight concurrent" || mode == "positive control" {
				batch = 8
			}
			if mode == "positive control" {
				count = 24
			}
			h := newRecoveryTimingHarness(t, count)
			var target, overlap recoveryTimingGroups
			var next [3]int
			order := timingOrder(count)
			for start := 0; start < len(order); start += batch {
				states := order[start:min(start+batch, len(order))]
				pending := make([]<-chan recoveryTimingResult, 0, len(states))
				for _, state := range states {
					index := next[state]
					next[state]++
					sample, done := h.start(t.Context(), recoveryTimingEmail(state, index), mode == "positive control" && state == 0)
					pending = append(pending, done)
					if mode == "worker active" {
						overlap[state] = append(overlap[state], h.overlap(t, start, sample))
					}
				}
				for i, state := range states {
					target[state] = append(target[state], recoveryTimingElapsed(t, pending[i]))
				}
			}
			auc, separated := timingSeparated(target)
			t.Logf("target: %d per state; pairwise AUC %.3f", count, auc)
			if separated != (mode == "positive control") || (mode == "positive control" && (auc[0] < 0.95 || auc[1] < 0.95)) {
				t.Fatal("target AUC did not satisfy the declared timing gate")
			}
			if mode == "worker active" {
				auc, separated := timingSeparated(overlap)
				t.Logf("overlapping probe: %d per target state; pairwise AUC %.3f", count, auc)
				if separated {
					t.Fatal("overlapping probe AUC did not satisfy the declared timing gate")
				}
			}
		})
	}
}

func (h *recoveryTimingHarness) overlap(t *testing.T, index int, target *recoveryTimingSample) time.Duration {
	t.Helper()
	waitRecoveryTiming(t, "target's durable job write", target.written)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	gate := &recoveryTimingWorker{account: make(chan struct{}), release: make(chan struct{})}
	step := make(chan bool, 1)
	go func() {
		worked, err := h.api.Config.Recovery.Step(context.WithValue(ctx, timingWorker, gate))
		step <- worked && err == nil
	}()
	waitRecoveryTiming(t, "worker account query inside SQLite write", gate.account)
	probe, done := h.start(t.Context(), recoveryTimingEmail(3, index), false)
	waitRecoveryTiming(t, "overlapping session lookup", probe.sessionStarted)
	const held = 15 * time.Millisecond
	time.Sleep(held)
	close(gate.release)
	if !<-step {
		t.Fatal("target worker failed")
	}
	waitRecoveryTiming(t, "overlapping job write", probe.written)
	if time.Duration(probe.sessionWait.Load()) < held {
		t.Fatal("the probe did not wait behind the target's transaction")
	}
	// Drain the unknown probe's job so the next worker always sees its target.
	if worked, err := h.api.Config.Recovery.Step(ctx); !worked || err != nil {
		t.Fatal("could not drain overlapping probe job")
	}
	return recoveryTimingElapsed(t, done)
}
