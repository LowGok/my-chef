package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/imports"
)

func importVault(t *testing.T) *imports.DraftVault {
	t.Helper()
	vault, err := imports.NewDraftVault(bytes.Repeat([]byte{77}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return vault
}
func importInput(key string, now time.Time) RecipeImportStart {
	return RecipeImportStart{RequestKey: key, Source: imports.ImportSource{Kind: "url", URL: "https://recipes.example.com/dinner?recipe_id=7", ImportedAt: now}}
}
func importDraft(t *testing.T, source imports.ImportSource) imports.Draft {
	t.Helper()
	servings, prep, cook, total := 4, 5, 10, 20
	draft, err := imports.NewDraft(imports.ExtractedRecipe{Title: "private-import-title-canary", Servings: &servings, PrepMinutes: &prep, CookMinutes: &cook, TotalMinutes: &total, Ingredients: []imports.ExtractedIngredient{{Name: "Carrot", SourceText: "A handful"}}, Instructions: []string{"Cook gently."}}, source)
	if err != nil {
		t.Fatal(err)
	}
	return draft
}
func TestRecipeImportStoreOwnershipIdempotencyEncryptionAndRestart(t *testing.T) {
	s, actor, other, premium, now := personalFixture(t)
	vault := importVault(t)
	ctx := t.Context()
	input := importInput(strings.Repeat("a", 32), now)
	first, lease, err := s.BeginRecipeImport(ctx, actor, premium, vault, input, now)
	if err != nil || !lease.Dispatch || first.Stage != "checking" {
		t.Fatal("import did not start", err)
	}
	replay, again, err := s.BeginRecipeImport(ctx, actor, premium, vault, input, now.Add(time.Second))
	if err != nil || again.Dispatch || replay.ID != first.ID {
		t.Fatal("duplicate restarted source", err)
	}
	input.Source.URL = "https://recipes.example.com/another"
	if _, _, err := s.BeginRecipeImport(ctx, actor, premium, vault, input, now); !errors.Is(err, ErrRecipeImportConflict) {
		t.Fatal("same key accepted different source", err)
	}
	input = importInput(strings.Repeat("a", 32), now)
	if _, err := s.ReadRecipeImport(ctx, other, vault, first.ID, now); !errors.Is(err, ErrRecipeImportNotFound) {
		t.Fatal("foreign draft readable", err)
	}
	source := input.Source
	draft := importDraft(t, source)
	result, err := s.CompleteRecipeImport(ctx, actor, premium, vault, lease, imports.PrivateDraft{Source: source, Draft: &draft}, now)
	if err != nil || result.Stage != "ready" || result.Draft == nil {
		t.Fatal("private draft unavailable", err)
	}
	var ciphertext []byte
	if err := s.SQL.QueryRow("SELECT cipher FROM recipe_import_operations WHERE id=?", first.ID).Scan(&ciphertext); err != nil || bytes.Contains(ciphertext, []byte("canary")) || bytes.Contains(ciphertext, []byte("recipes.example.com")) {
		t.Fatal("private source persisted in plaintext", err)
	}
	for _, table := range []string{"personal_recipes", "meals", "favorites", "shopping_lists"} {
		var count int64
		if err := s.DB.Table(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("import mutated saved records", table, err)
		}
	}
	reopened, err := Open(s.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	got, err := reopened.ReadRecipeImport(ctx, actor, vault, first.ID, now)
	if err != nil || got.Draft == nil || got.Draft.Content.Title != draft.Content.Title {
		t.Fatal("draft lost across restart", err)
	}
	if _, err := reopened.CompleteRecipeImport(ctx, actor, premium, vault, lease, imports.PrivateDraft{Source: source, Draft: &draft}, now); !errors.Is(err, ErrRecipeImportConflict) {
		t.Fatal("completed source replay accepted", err)
	}
}
func TestRecipeImportStorePermissionConcurrencyCancelAndExpiry(t *testing.T) {
	s, actor, _, premium, now := personalFixture(t)
	vault := importVault(t)
	ctx := t.Context()
	input := importInput(strings.Repeat("b", 32), now)
	if _, _, err := s.BeginRecipeImport(ctx, actor, nil, vault, input, now); !errors.Is(err, ErrPersonalPermission) {
		t.Fatal("missing Premium enabled import", err)
	}
	if _, _, err := s.BeginRecipeImport(ctx, actor, premium, nil, input, now); err == nil {
		t.Fatal("missing encryption enabled import")
	}
	var wg sync.WaitGroup
	wg.Add(4)
	var mu sync.Mutex
	dispatches := 0
	var id string
	var lease RecipeImportLease
	for range 4 {
		go func() {
			defer wg.Done()
			op, l, err := s.BeginRecipeImport(context.Background(), actor, premium, vault, input, now)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Error(err)
				return
			}
			id = op.ID
			if l.Dispatch {
				dispatches++
				lease = l
			}
		}()
	}
	wg.Wait()
	if dispatches != 1 {
		t.Fatal("concurrent duplicate dispatch", dispatches)
	}
	next := importInput(strings.Repeat("c", 32), now)
	if _, _, err := s.BeginRecipeImport(ctx, actor, premium, vault, next, now); !errors.Is(err, ErrRecipeImportBusy) {
		t.Fatal("per-owner concurrency unbounded", err)
	}
	if err := s.CancelRecipeImport(ctx, actor, id, now); err != nil {
		t.Fatal(err)
	}
	draft := importDraft(t, input.Source)
	if _, err := s.CompleteRecipeImport(ctx, actor, premium, vault, lease, imports.PrivateDraft{Source: input.Source, Draft: &draft}, now); err == nil {
		t.Fatal("late completion recreated cancelled data")
	}
	if _, _, err := s.BeginRecipeImport(ctx, actor, premium, vault, next, now); err != nil {
		t.Fatal("cancel did not release active slot", err)
	}
	if err := s.SweepRecipeImports(ctx, now.Add(24*time.Hour), false); err != nil {
		t.Fatal(err)
	}
	var retained int64
	if err := s.DB.Table("recipe_import_operations").Where("cipher IS NOT NULL").Count(&retained).Error; err != nil || retained != 0 {
		t.Fatal("expired secrets retained", err)
	}
	if _, err := s.ReadRecipeImport(ctx, actor, vault, id, now.Add(24*time.Hour)); err == nil {
		t.Fatal("expired operation readable")
	}
}
func TestRecipeImportStoreExplicitAIAndRestartFences(t *testing.T) {
	s, actor, _, premium, now := personalFixture(t)
	vault := importVault(t)
	ctx := t.Context()
	input := importInput(strings.Repeat("d", 32), now)
	op, lease, err := s.BeginRecipeImport(ctx, actor, premium, vault, input, now)
	if err != nil {
		t.Fatal(err)
	}
	inspection := imports.PageInspection{Candidates: []imports.StructuredCandidate{}, FallbackText: "private-visible-recipe-canary"}
	op, err = s.CompleteRecipeImport(ctx, actor, premium, vault, lease, imports.PrivateDraft{Source: input.Source, Inspection: &inspection}, now)
	if err != nil || !op.CanReadWithAI || op.Stage != "awaiting-ai" {
		t.Fatal("explicit fallback not available", err)
	}
	op, ai, err := s.BeginRecipeImportAI(ctx, actor, premium, vault, op.ID, now)
	if err != nil || !ai.Dispatch || op.Stage != "reading" {
		t.Fatal("AI intent missing", err)
	}
	if _, _, err := s.BeginRecipeImportAI(ctx, actor, premium, vault, op.ID, now); !errors.Is(err, ErrRecipeImportConflict) {
		t.Fatal("duplicate provider intent allowed", err)
	}
	if err := s.SweepRecipeImports(ctx, now, true); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadRecipeImport(ctx, actor, vault, op.ID, now)
	if err != nil || got.Stage != "failed" || got.Code != "interrupted" || got.Draft != nil {
		t.Fatal("restart did not fence ambiguous dispatch", err)
	}
	draft := importDraft(t, input.Source)
	if _, err := s.CompleteRecipeImport(ctx, actor, premium, vault, ai, imports.PrivateDraft{Source: input.Source, Draft: &draft}, now); err == nil {
		t.Fatal("stale provider result accepted after restart")
	}
	replay, again, err := s.BeginRecipeImport(ctx, actor, premium, vault, input, now)
	if err != nil || again.Dispatch || replay.Stage != "failed" {
		t.Fatal("restart replayed source", err)
	}
}

func TestRecipeImportStoreSelectionPhotosAndRevocation(t *testing.T) {
	s, actor, other, premium, now := personalFixture(t)
	vault := importVault(t)
	ctx := t.Context()
	input := importInput(strings.Repeat("e", 32), now)
	op, lease, err := s.BeginRecipeImport(ctx, actor, premium, vault, input, now)
	if err != nil {
		t.Fatal(err)
	}
	first := importDraft(t, input.Source)
	extracted := imports.ExtractedRecipe{Title: "Another recipe", Instructions: []string{"Stir."}}
	second, err := imports.NewDraft(extracted, input.Source)
	if err != nil {
		t.Fatal(err)
	}
	inspection := imports.PageInspection{Candidates: []imports.StructuredCandidate{{ID: strings.Repeat("1", 64), Draft: first, Complete: false}, {ID: strings.Repeat("2", 64), Draft: second, Complete: false}}}
	op, err = s.CompleteRecipeImport(ctx, actor, premium, vault, lease, imports.PrivateDraft{Source: input.Source, Inspection: &inspection}, now)
	if err != nil || op.Stage != "selection" || len(op.Candidates) != 2 || op.Draft != nil {
		t.Fatal("multiple recipes silently selected", err)
	}
	if _, err := s.SelectRecipeImport(ctx, other, premium, vault, op.ID, strings.Repeat("2", 64), now); !errors.Is(err, ErrRecipeImportNotFound) {
		t.Fatal("foreign selection allowed", err)
	}
	if _, err := s.SelectRecipeImport(ctx, actor, nil, vault, op.ID, strings.Repeat("2", 64), now); !errors.Is(err, ErrPersonalPermission) {
		t.Fatal("selection bypassed permission", err)
	}
	chosen, err := s.SelectRecipeImport(ctx, actor, premium, vault, op.ID, strings.Repeat("2", 64), now)
	if err != nil || chosen.Draft == nil || chosen.Draft.Content.Title != "Another recipe" || chosen.Private.Inspection != nil {
		t.Fatal("selection retained another recipe", err)
	}
	photo := RecipeImportStart{RequestKey: strings.Repeat("f", 32), Source: imports.ImportSource{Kind: "photos", ImportedAt: now}, ImageDigests: []string{strings.Repeat("a", 64)}}
	op, lease, err = s.BeginRecipeImport(ctx, actor, premium, vault, photo, now)
	if err != nil || op.Stage != "reading" || !lease.Dispatch {
		t.Fatal("photo intent missing", err)
	}
	if _, _, err := s.BeginRecipeImportAI(ctx, actor, premium, vault, op.ID, now); !errors.Is(err, ErrRecipeImportConflict) {
		t.Fatal("photo second attempt permitted", err)
	}
	if err := s.DB.Where("hash=?", actor.SessionHash).Delete(&Session{}).Error; err != nil {
		t.Fatal(err)
	}
	draft := importDraft(t, photo.Source)
	if _, err := s.CompleteRecipeImport(ctx, actor, premium, vault, lease, imports.PrivateDraft{Source: photo.Source, Draft: &draft}, now); !errors.Is(err, ErrPersonalSession) {
		t.Fatal("revoked session completed import", err)
	}
	if err := s.DB.Delete(&User{}, actor.UserID).Error; err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := s.DB.Table("recipe_import_operations").Where("user_id=?", actor.UserID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("account removal retained import owner mapping", err)
	}
}
func TestRecipeImportMigrationRollbackAndPreservation(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			s := versionTwelvePersonalFixture(t)
			for _, step := range []func(context.Context) error{s.migratePersonalRecipes, s.migratePersonalPhotos, s.migratePersonalPlanning} {
				if err := step(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			var before string
			if err := s.SQL.QueryRow("SELECT sql FROM sqlite_schema WHERE name='users'").Scan(&before); err != nil {
				t.Fatal(err)
			}
			if fail {
				if _, err := s.SQL.Exec("CREATE TABLE personal_recipe_import_sources(content TEXT); INSERT INTO personal_recipe_import_sources VALUES('must survive')"); err != nil {
					t.Fatal(err)
				}
			}
			err := s.Migrate(t.Context())
			if fail {
				if err == nil {
					t.Fatal("conflicting migration accepted")
				}
				var version, count int
				var value string
				if err := s.SQL.QueryRow("SELECT version FROM schema_migrations").Scan(&version); err != nil || version != 15 {
					t.Fatal("failed migration advanced schema", err)
				}
				if err := s.SQL.QueryRow("SELECT COUNT(*) FROM sqlite_schema WHERE name='recipe_import_operations'").Scan(&count); err != nil || count != 0 {
					t.Fatal("partial import schema retained", err)
				}
				if err := s.SQL.QueryRow("SELECT content FROM personal_recipe_import_sources").Scan(&value); err != nil || value != "must survive" {
					t.Fatal("foreign schema overwritten", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if err := s.Migrate(t.Context()); err != nil {
					t.Fatal("migration replay failed", err)
				}
				var after string
				if err := s.SQL.QueryRow("SELECT sql FROM sqlite_schema WHERE name='users'").Scan(&after); err != nil || after != before {
					t.Fatal("migration changed existing identity schema", err)
				}
				if err := s.Validate(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestRecipeImportExpiryRunsWithoutWorker(t *testing.T) {
	s, actor, _, premium, now := personalFixture(t)
	vault := importVault(t)
	op, lease, err := s.BeginRecipeImport(t.Context(), actor, premium, vault, importInput(strings.Repeat("e", 32), now), now)
	if err != nil {
		t.Fatal(err)
	}
	draft := importDraft(t, op.Private.Source)
	if _, err := s.CompleteRecipeImport(t.Context(), actor, premium, vault, lease, imports.PrivateDraft{Source: op.Private.Source, Draft: &draft}, now); err != nil {
		t.Fatal(err)
	}
	ticks := make(chan time.Time, 1)
	ticks <- now.Add(24 * time.Hour)
	close(ticks)
	if err := s.runRecipeImportExpiry(t.Context(), ticks); err != nil {
		t.Fatal(err)
	}
	var row recipeImportOperation
	if err := s.DB.Where("id=?", op.ID).Take(&row).Error; err != nil || row.Stage != "expired" || len(row.Cipher) != 0 {
		t.Fatal("worker-disabled expiry retained private draft", err)
	}
}

func TestRecipeImportCompletionRequiresCurrentPremium(t *testing.T) {
	s, actor, _, _, now := personalFixture(t)
	ctx := t.Context()
	if err := s.DB.Exec("UPDATE users SET email='import-entitlement@example.test', email_key='import-entitlement@example.test' WHERE user_id=?", actor.UserID).Error; err != nil {
		t.Fatal(err)
	}
	owner := entitlementIdentity(t, s, actor.UserID)
	at := now
	reader, revoke := testEntitlementSwitch(t, s, actor, owner, func() time.Time { return at })
	vault := importVault(t)
	input := importInput(strings.Repeat("d", 32), now)
	op, lease, err := s.BeginRecipeImport(ctx, actor, reader, vault, input, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CheckRecipeImportDispatch(ctx, actor, reader, vault, lease, false, true, now); err != nil {
		t.Fatal(err)
	}
	revoke()
	draft := importDraft(t, op.Private.Source)
	if _, err := s.CompleteRecipeImport(ctx, actor, reader, vault, lease, imports.PrivateDraft{Source: op.Private.Source, Draft: &draft}, now); !errors.Is(err, ErrPersonalPermission) {
		t.Fatal("late extraction committed after revocation", err)
	}
	retained, err := s.ReadRecipeImport(ctx, actor, vault, op.ID, now)
	if err != nil || retained.Stage != "checking" || retained.Draft != nil {
		t.Fatal("rejected extraction changed stored draft", err)
	}
	if err := s.AbandonRecipeImport(ctx, actor.UserID, owner, lease, now); err != nil {
		t.Fatal("cleanup required Premium", err)
	}
}
