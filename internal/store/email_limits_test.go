package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestEmailLimitsSurviveRestartAndExpireAtExactBoundary(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "limits.db")
	s, err := Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	source := EmailLimit{Bucket: "fixture-source", Maximum: 2, Window: time.Minute}
	email := EmailLimit{Bucket: "fixture-email", Maximum: 1, Window: 30 * time.Second}
	first, err := s.ReserveEmailLimits(ctx, nil, now, source, email)
	if err != nil || first.Reservation == "" || first.RetryAfter != 0 {
		t.Fatalf("initial reservation failed: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := s.ReserveEmailLimits(ctx, nil, now.Add(time.Second), source, email)
	if err != nil || blocked.Reservation != "" || blocked.RetryAfter != 29 {
		t.Fatalf("reopened store did not enforce the address window: %+v, %v", blocked, err)
	}
	other := EmailLimit{Bucket: "other-email", Maximum: 1, Window: time.Second}
	if result, err := s.ReserveEmailLimits(ctx, nil, now.Add(time.Second), source, other); err != nil || result.Reservation == "" {
		t.Fatalf("independent address was charged for a rejected request: %+v, %v", result, err)
	}
	if result, err := s.ReserveEmailLimits(ctx, nil, now.Add(30*time.Second), source, email); err != nil || result.RetryAfter != 30 {
		t.Fatalf("longest active window not reported: %+v, %v", result, err)
	}
	if result, err := s.ReserveEmailLimits(ctx, nil, now.Add(time.Minute), source, email); err != nil || result.Reservation == "" {
		t.Fatalf("expired window still denied request: %+v, %v", result, err)
	}
}

func TestEmailLimitsReserveConcurrentlyAndReleaseOnlyRequestedBucket(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "limits.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	source := EmailLimit{Bucket: "fixture-source", Maximum: 4, Window: time.Minute}
	token := EmailLimit{Bucket: "fixture-token", Maximum: 3, Window: time.Minute}
	type answer struct {
		result EmailLimitResult
		err    error
	}
	results := make(chan answer, 8)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			result, err := s.ReserveEmailLimits(ctx, nil, now, source, token)
			results <- answer{result, err}
		})
	}
	workers.Wait()
	close(results)
	var accepted []string
	for got := range results {
		if got.err != nil || (got.result.Reservation == "" && got.result.RetryAfter != 60) {
			t.Fatalf("concurrent reservation failed: %+v", got)
		}
		if got.result.Reservation != "" {
			accepted = append(accepted, got.result.Reservation)
		}
	}
	if len(accepted) != 3 {
		t.Fatalf("expected three reservations, got %d", len(accepted))
	}
	if err := s.ReleaseEmailLimit(ctx, nil, accepted[0], token.Bucket); err != nil {
		t.Fatal(err)
	}
	if result, err := s.ReserveEmailLimits(ctx, nil, now, source, token); err != nil || result.Reservation == "" {
		t.Fatalf("released token reservation could not be used again: %+v, %v", result, err)
	}
	if err := s.ClearEmailLimit(ctx, nil, token.Bucket); err != nil {
		t.Fatal(err)
	}
	if result, err := s.ReserveEmailLimits(ctx, nil, now, source, token); err != nil || result.RetryAfter != 60 {
		t.Fatalf("clearing the token incorrectly cleared the source allowance: %+v, %v", result, err)
	}
}

func TestRecoveryCompletionReportsLongestWaitAfterRestartAndReopensAtBoundary(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "limits.db")
	s, err := Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	check := func(stamp time.Time, source, proof string, want int) {
		t.Helper()
		wait, err := s.ReserveRecoveryCompletion(ctx, stamp, source, proof)
		if err != nil || wait != want {
			t.Fatalf("reservation: got %d, want %d (%v)", wait, want, err)
		}
	}
	for range 50 {
		check(now, "source-a", "", 0)
	}
	for range 10 {
		check(now.Add(time.Minute), "source-b", "proof", 0)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	check(now.Add(2*time.Minute), "source-a", "proof", 14*60)
	check(now.Add(15*time.Minute-time.Nanosecond), "source-a", "", 1)
	check(now.Add(15*time.Minute), "source-a", "", 0)
	check(now.Add(16*time.Minute-time.Nanosecond), "source-b", "proof", 1)
	check(now.Add(16*time.Minute), "source-b", "proof", 0)
}

func TestRecoveryCompletionConcurrentAdmissionsChargeExactlyOneSourceEvent(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "limits.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := t.Context()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	var workers sync.WaitGroup
	var got [60]int
	var errs [60]error
	for i := range got {
		workers.Go(func() { got[i], errs[i] = s.ReserveRecoveryCompletion(ctx, now, "source", "proof") })
	}
	workers.Wait()
	var allowed int
	for i, wait := range got {
		if errs[i] != nil || (wait != 0 && wait != 15*60) {
			t.Fatal("concurrent reservation did not return a valid result")
		}
		if wait == 0 {
			allowed++
		}
	}
	if allowed != 10 {
		t.Fatalf("concurrent work admitted %d proof attempts, want 10", allowed)
	}
	for bucket, want := range map[string]int{"source": 50, "proof": 10} {
		var count int
		if err := s.SQL.QueryRowContext(ctx, "SELECT count(*) FROM email_rate_events WHERE bucket=?", bucket).Scan(&count); err != nil || count != want {
			t.Fatalf("concurrent work admitted %d %s events, want %d", count, bucket, want)
		}
	}
}
