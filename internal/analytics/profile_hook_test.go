package analytics_test

import (
	"context"
	"testing"

	"github.com/LowGok/my-chef/internal/analytics"
	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/domain"
)

func TestProfilePreferencesHook(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p, e := f.s.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: true, NoticeVersion: "core-v1"}, "web")
	if e != nil {
		t.Fatal(e)
	}
	ctx = f.s.Request(ctx, f.user, p.Revision, "web")
	m := auth.New(f.app)
	in := auth.ProfileUpdate{Username: "fixture", FirstName: "Fixture", LastName: "Only", Preferences: domain.EmptyPreferences()}
	if _, e = m.Update(ctx, f.user, in); e != nil {
		t.Fatal(e)
	}
	in.FirstName = "Changed"
	if _, e = m.Update(ctx, f.user, in); e != nil {
		t.Fatal(e)
	}
	var n int
	_ = f.app.SQL.QueryRow("SELECT count(*) FROM analytics_outbox WHERE name='preferences_saved'").Scan(&n)
	if n != 1 {
		t.Fatalf("profile-only update captured: %d", n)
	}
}
