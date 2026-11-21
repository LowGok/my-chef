package store

import (
	"path/filepath"
	"testing"

	"github.com/LowGok/my-chef/internal/billing"
)

func TestBillingJournalExplicitlyProvisionedAndBoundToApp(t *testing.T) {
	s, actor, _, _, now := personalFixture(t)
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "app.lifecycle")
	lifecycle, err := s.InitializeLifecycleJournal(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := lifecycle.Billing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := entitlementIdentity(t, s, actor.UserID)
	attempt, err := journal.Reserve(ctx, owner, billing.Sandbox, billing.Apple, billing.Monthly, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Close(); err != nil {
		t.Fatal(err)
	}
	lifecycle, err = s.OpenLifecycleJournal(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lifecycle.Close() }()
	journal, err = lifecycle.Billing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Launch(ctx, attempt, now); err != nil {
		t.Fatalf("checkout not durable across journal reopen: %v", err)
	}
	var tables int
	if err := s.SQL.QueryRow("SELECT count(*) FROM sqlite_master WHERE name LIKE 'billing_%'").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("billing tables leaked into ordinary application snapshots: %d %v", tables, err)
	}
}
