package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDeletionNoticeSweepsAtStartupAndMinuteWithoutSender(t *testing.T) {
	s, _ := lifecycleJournalFixture(t)
	now := time.Unix(1800000000, 0)
	query := "INSERT INTO email_outbox(key,recipient,template,data,state,next_at,created_at,updated_at) VALUES(?,?,?,'{}','queued',1,?,1)"
	for _, row := range []struct {
		key, template string
		created       int64
	}{
		{"deleted-expired", AccountDeletedTemplate, now.Add(-24 * time.Hour).Unix()},
		{"deleted-later", AccountDeletedTemplate, now.Unix()},
		{"unrelated", "password-changed/v1", now.Add(-24 * time.Hour).Unix()},
	} {
		if _, err := s.SQL.ExecContext(t.Context(), query, row.key, "sweep-fixture@example.test", row.template, row.created); err != nil {
			t.Fatal(err)
		}
	}
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.runDeletionMailSweep(ctx, now, ticks) }()
	defer func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	}()
	recipient := func(key string) string {
		t.Helper()
		var value string
		if err := s.SQL.QueryRowContext(t.Context(), "SELECT recipient FROM email_outbox WHERE key=?", key).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	waitErased := func(key string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for recipient(key) != "" {
			if time.Now().After(deadline) {
				t.Fatal("scheduled deletion notice was not erased")
			}
			time.Sleep(time.Millisecond)
		}
	}
	waitErased("deleted-expired")
	if recipient("deleted-later") == "" {
		t.Fatal("fresh notice expired before its horizon")
	}
	ticks <- now.Add(24 * time.Hour)
	waitErased("deleted-later")
	if recipient("unrelated") == "" {
		t.Fatal("dedicated notice sweep erased unrelated mail")
	}
}
