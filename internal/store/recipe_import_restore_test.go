package store

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/imports"
)

func TestRecipeImportWorkerLedgerSurvivesMatchedAppRestore(t *testing.T) {
	s, j, actor, _, premium, now := personalPhotoStore(t)
	vault := importVault(t)
	key, channel := bytes.Repeat([]byte{51}, 32), bytes.Repeat([]byte{52}, 32)
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(directory, "admission.db")
	identity, err := imports.InitializeAdmissionLedger(ledgerPath, key, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := imports.OpenAdmissionLedger(ledgerPath, key, identity, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ledger.Close() }()
	binding, policy := imports.FixtureExtractionPolicy(now.Add(30 * time.Minute))
	handler, err := imports.NewWorkerHandler(imports.WorkerOptions{Ledger: ledger, Binding: binding, Policy: func() (imports.ExtractionPolicy, error) { return policy, nil }, Engine: imports.FixtureExtractionEngine{}, Fetcher: imports.FixturePageFetcher{}, ChannelKey: channel, Fixture: true})
	if err != nil {
		t.Fatal(err)
	}
	rpc := func(path string, request imports.WorkerRequest, want int) imports.WorkerResult {
		raw, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		call := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw)).WithContext(t.Context())
		call.Header.Set("Content-Type", "application/json")
		call.Header.Set("Authorization", imports.WorkerAuthorization(channel))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, call)
		if response.Code != want {
			t.Fatal("unexpected private worker outcome", response.Code, want)
		}
		var result imports.WorkerResult
		if want == 200 && json.Unmarshal(response.Body.Bytes(), &result) != nil {
			t.Fatal("invalid worker response")
		}
		return result
	}
	source := func(db *Store, key string) (RecipeImport, imports.WorkerRequest) {
		input := importInput(key, now)
		input.Source.URL = "https://recipes.example.test/plain"
		op, lease, err := db.BeginRecipeImport(t.Context(), actor, premium, vault, input, now)
		if err != nil || !lease.Dispatch {
			t.Fatal("source did not start", err)
		}
		request := imports.WorkerRequest{OperationID: op.ID, AccountID: op.OwnerInstance, Source: op.Private.Source}
		fetched := rpc("/v1/check-page", request, 200)
		op, err = db.CompleteRecipeImport(t.Context(), actor, premium, vault, lease, fetched.Private, now)
		if err != nil || op.Stage != "awaiting-ai" || op.Private.Inspection == nil {
			t.Fatal("fallback source unavailable", err)
		}
		request.Text = op.Private.Inspection.FallbackText
		return op, request
	}
	op, request := source(s, strings.Repeat("a", 32))
	backup := filepath.Join(t.TempDir(), "before-inference.db")
	if err := s.Backup(t.Context(), backup); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRecipeImportAI(t.Context(), actor, premium, vault, op.ID, now); err != nil {
		t.Fatal(err)
	}
	rpc("/v1/extract", request, 200) // The web app loses this result.
	restoredPath := filepath.Join(t.TempDir(), "restored.db")
	if err := Restore(t.Context(), backup, restoredPath, AccountRestoreOptions{JournalPath: j.Path}); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(restoredPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	again, lease, err := restored.BeginRecipeImportAI(t.Context(), actor, premium, vault, op.ID, now)
	if err != nil || !lease.Dispatch || again.ID != op.ID || again.OwnerInstance != op.OwnerInstance {
		t.Fatal("restored dispatch identity changed", err)
	}
	rpc("/v1/extract", request, 409)
	if err := restored.AbandonRecipeImport(t.Context(), actor.UserID, again.OwnerInstance, lease, now); err != nil {
		t.Fatal(err)
	}
	// A genuinely new, deliberate attempt still fits the second fleet slot.
	next, nextRequest := source(restored, strings.Repeat("b", 32))
	if _, _, err := restored.BeginRecipeImportAI(t.Context(), actor, premium, vault, next.ID, now); err != nil {
		t.Fatal(err)
	}
	rpc("/v1/extract", nextRequest, 200)
}

func TestRecipeImportAbandonLeaseCannotPurgeForeignOrReadyDraft(t *testing.T) {
	s, actor, other, premium, now := personalFixture(t)
	vault := importVault(t)
	op, lease, err := s.BeginRecipeImport(t.Context(), actor, premium, vault, importInput(strings.Repeat("a", 32), now), now)
	if err != nil {
		t.Fatal(err)
	}
	foreign, _, err := s.BeginRecipeImport(t.Context(), other, premium, vault, importInput(strings.Repeat("b", 32), now), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AbandonRecipeImport(t.Context(), other.UserID, foreign.OwnerInstance, lease, now); err != nil {
		t.Fatal(err)
	}
	wrong := lease
	wrong.Token = strings.Repeat("0", 64)
	if err := s.AbandonRecipeImport(t.Context(), actor.UserID, op.OwnerInstance, wrong, now); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadRecipeImport(t.Context(), actor, vault, op.ID, now)
	if err != nil || got.Stage != "checking" {
		t.Fatal("foreign or guessed lease erased source", err)
	}
	draft := importDraft(t, op.Private.Source)
	if _, err := s.CompleteRecipeImport(t.Context(), actor, premium, vault, lease, imports.PrivateDraft{Source: op.Private.Source, Draft: &draft}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.AbandonRecipeImport(t.Context(), actor.UserID, op.OwnerInstance, lease, now); err != nil {
		t.Fatal(err)
	}
	got, err = s.ReadRecipeImport(t.Context(), actor, vault, op.ID, now)
	if err != nil || got.Stage != "ready" || got.Draft == nil {
		t.Fatal("stale cleanup erased a ready result", err)
	}
}
