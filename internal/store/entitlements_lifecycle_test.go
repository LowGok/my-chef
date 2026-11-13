package store

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEntitlementRestoreRetiresGenerationWithSameLease(t *testing.T) {
	s, journal, account := accountDeletionFixture(t)
	ctx := t.Context()
	now := time.Now().UTC()
	owner := entitlementIdentity(t, s, account.UserID)
	if err := s.DB.Exec("UPDATE users SET email_verified_at=? WHERE user_id=?", now.Unix(), account.UserID).Error; err != nil {
		t.Fatal(err)
	}
	lease := FixtureEntitlementLease{ID: "same-still-valid-lease", Expires: now.Add(time.Hour)}
	generation, err := s.FixtureEntitlementGeneration(ctx, lease, fixedEntitlementClock(now))
	if err != nil {
		t.Fatal(err)
	}
	op := FixtureEntitlementOperation{ID: strings.Repeat("a", 32), Generation: generation, Incarnation: owner, UserID: account.UserID, State: "premium"}
	if err := s.SetFixtureEntitlement(ctx, lease, op, fixedEntitlementClock(now)); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "pre-revocation.db")
	if err := s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	revoke := op
	revoke.ID, revoke.State = strings.Repeat("b", 32), "free"
	if err := s.SetFixtureEntitlement(ctx, lease, revoke, fixedEntitlementClock(now.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored.db")
	if err := Restore(ctx, backup, destination, AccountRestoreOptions{JournalPath: journal.Path}); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(destination, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	if entitlementIdentity(t, restored, account.UserID) != owner {
		t.Fatal("restore changed a never-deleted account")
	}
	newGeneration, err := restored.FixtureEntitlementGeneration(ctx, lease, fixedEntitlementClock(now.Add(time.Second)))
	if err != nil || newGeneration == generation {
		t.Fatalf("restore retained an active fixture generation: %v", err)
	}
	state, err := EntitlementReader(lease, func() time.Time { return now.Add(time.Second) })(ctx, restored.DB, account.UserID, owner)
	if err != nil || state.Premium {
		t.Fatal("pre-revocation backup reactivated Premium", err)
	}
	if err := restored.SetFixtureEntitlement(ctx, lease, op, fixedEntitlementClock(now.Add(time.Second))); err == nil {
		t.Fatal("pre-restore request was accepted with the same lease")
	}
	for _, table := range []string{"entitlement_grants", "entitlement_fixture_operations"} {
		var count int
		if err := restored.SQL.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("restored fixture retained old grants or receipts", table, count, err)
		}
	}
	var sourceCount int
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM entitlement_grants WHERE revoked_ns IS NOT NULL").Scan(&sourceCount); err != nil || sourceCount != 1 {
		t.Fatal("restore changed its source database", sourceCount, err)
	}
}

func TestEntitlementAccountExportDeletionAndFreshSignup(t *testing.T) {
	s, journal, actor := accountDeletionFixture(t)
	ctx := t.Context()
	now := time.Now().UTC()
	if err := s.DB.Exec("UPDATE users SET email_verified_at=? WHERE user_id IN (1,2)", now.Unix()).Error; err != nil {
		t.Fatal(err)
	}
	lease := FixtureEntitlementLease{ID: "lease-id-never-export", Expires: now.Add(time.Hour)}
	generation, err := s.FixtureEntitlementGeneration(ctx, lease, fixedEntitlementClock(now))
	if err != nil {
		t.Fatal(err)
	}
	owner := entitlementIdentity(t, s, actor.UserID)
	other := entitlementIdentity(t, s, 2)
	grant := entitlementGrant{id: "grant-id-never-export", incarnation: owner, source: "fixture", generation: generation, lease: lease.ID, start: now.UnixNano(), end: now.Add(time.Minute).UnixNano()}
	if err := insertEntitlementGrant(s.DB, grant); err != nil {
		t.Fatal(err)
	}
	foreign := grant
	foreign.id, foreign.incarnation, foreign.end = "foreign-grant-id", other, now.Add(10*time.Minute).UnixNano()
	if err := insertEntitlementGrant(s.DB, foreign); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec("UPDATE entitlement_grants SET revoked_ns=? WHERE id=?", now.UnixNano(), grant.id).Error; err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := s.WriteAccountExport(ctx, actor, &archive, now); err != nil {
		t.Fatal("Free account could not export", err)
	}
	files := readLifecycleExport(t, archive.Bytes())
	var facts []map[string]json.RawMessage
	if err := json.Unmarshal(files["entitlements.json"], &facts); err != nil || len(facts) != 1 ||
		string(facts[0]["source"]) != `"fixture"` || len(facts[0]) != 4 || len(facts[0]["revokedNanoseconds"]) == 0 {
		t.Fatal("missing, foreign or unsafe export facts", err)
	}
	for _, data := range files {
		for _, secret := range []string{grant.id, lease.ID, generation, foreign.id, owner, other} {
			if bytes.Contains(data, []byte(secret)) {
				t.Fatal("account export included raw entitlement authority")
			}
		}
	}
	backup := filepath.Join(t.TempDir(), "pre-delete.db")
	if err := s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	var original User
	if err := s.DB.First(&original, actor.UserID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := journal.DeleteAccount(ctx, actor, strings.Repeat("c", 32), "DELETE", now, AccountDeletionOptions{}); err != nil {
		t.Fatal("Free account could not delete", err)
	}
	var rows int
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM entitlement_grants WHERE incarnation=?", owner).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("deleted grants survived", rows, err)
	}
	destination := filepath.Join(t.TempDir(), "restored.db")
	if err := Restore(ctx, backup, destination, AccountRestoreOptions{JournalPath: journal.Path}); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(destination, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	if err := restored.SQL.QueryRow("SELECT COUNT(*) FROM users WHERE email_key=?", original.EmailKey).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("restore returned the deleted account", rows, err)
	}
	if err := s.DB.Create(&original).Error; err != nil {
		t.Fatal(err)
	}
	fresh := entitlementIdentity(t, s, original.ID)
	decision, err := EntitlementReader(lease, func() time.Time { return now })(ctx, s.DB, original.ID, fresh)
	if err != nil || fresh == owner || decision.Premium {
		t.Fatal("fresh signup inherited entitlement", err)
	}
}

func TestEntitlementUpgradeFrom17LeavesAccountsFree(t *testing.T) {
	s := versionTwelvePersonalFixture(t)
	ctx := t.Context()
	for _, step := range []func(context.Context) error{s.migratePersonalRecipes, s.migratePersonalPhotos, s.migratePersonalPlanning, s.migrateRecipeImports, s.migrateNativeSessions} {
		if err := step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	owner := entitlementIdentity(t, s, 1)
	var schema string
	if err := s.SQL.QueryRow("SELECT sql FROM sqlite_schema WHERE name='users'").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckSchema(ctx); err == nil {
		t.Fatal("startup accepted schema 17 without an explicit upgrade")
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM entitlement_grants").Scan(&count); err != nil || count != 0 {
		t.Fatal("migration backfilled grants", count, err)
	}
	var currentSchema string
	if err := s.SQL.QueryRow("SELECT sql FROM sqlite_schema WHERE name='users'").Scan(&currentSchema); err != nil || currentSchema != schema || entitlementIdentity(t, s, 1) != owner {
		t.Fatal("upgrade rewrote account data", err)
	}
	if err := s.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
}
