package store

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/imports"
)

func readyImportSave(t *testing.T, s *Store, actor PersonalActor, premium PersonalEntitlementReader, vault *imports.DraftVault, key string, now time.Time) (RecipeImport, PersonalSave) {
	t.Helper()
	op, lease, err := s.BeginRecipeImport(t.Context(), actor, premium, vault, importInput(key, now), now)
	if err != nil {
		t.Fatal(err)
	}
	draft := importDraft(t, op.Private.Source)
	op, err = s.CompleteRecipeImport(t.Context(), actor, premium, vault, lease, imports.PrivateDraft{Source: op.Private.Source, Draft: &draft}, now)
	if err != nil {
		t.Fatal(err)
	}
	review, err := draft.Review(draft.Content)
	if err != nil {
		t.Fatal(err)
	}
	resolved := []string{}
	for _, issue := range draft.Issues {
		resolved = append(resolved, issue.Field)
	}
	return op, PersonalSave{RequestKey: strings.Repeat("b", 32), Content: review.Content, Import: &PersonalImportSave{OperationID: op.ID, Vault: vault, Confirmation: imports.ReviewConfirmation{DraftRevision: draft.Revision, ContentRevision: review.ContentRevision, ReviewedFields: review.RequiredFields, ResolvedFields: resolved}}}
}
func TestRecipeImportSaveAtomicReviewAndSingleVersion(t *testing.T) {
	s, actor, other, premium, now := personalFixture(t)
	vault := importVault(t)
	op, input := readyImportSave(t, s, actor, premium, vault, strings.Repeat("a", 32), now)
	bad := input
	empty := *input.Import
	empty.Confirmation = imports.ReviewConfirmation{}
	bad.Import = &empty
	if _, err := s.SavePersonalRecipe(t.Context(), actor, premium, bad, now); !errors.Is(err, imports.ErrImportReviewRequired) {
		t.Fatal("unreviewed import saved", err)
	}
	if _, err := s.SavePersonalRecipe(t.Context(), other, premium, input, now); !errors.Is(err, ErrRecipeImportNotFound) {
		t.Fatal("foreign import saved", err)
	}
	if _, err := s.SavePersonalRecipe(t.Context(), actor, nil, input, now); !errors.Is(err, ErrPersonalPermission) {
		t.Fatal("Premium bypassed", err)
	}
	for _, stage := range []string{"root", "version", "import-source", "import-saved", "current", "receipt"} {
		failure := errors.New("synthetic rollback")
		_, err := s.SavePersonalRecipe(t.Context(), actor, premium, input, now, PersonalWriteOptions{Fault: func(point string) error {
			if point == stage {
				return failure
			}
			return nil
		}})
		if !errors.Is(err, failure) {
			t.Fatal("fault not exercised", stage, err)
		}
		for _, table := range []string{"personal_recipes", "personal_recipe_versions", "personal_recipe_import_sources"} {
			var count int64
			if err := s.DB.Table(table).Count(&count).Error; err != nil || count != 0 {
				t.Fatal("partial import save survived", stage, table, err)
			}
		}
		retained, err := s.ReadRecipeImport(t.Context(), actor, vault, op.ID, now)
		if err != nil || retained.Stage != "ready" || retained.Draft == nil {
			t.Fatal("failed save destroyed draft", stage, err)
		}
	}
	saved, err := s.SavePersonalRecipe(t.Context(), actor, premium, input, now)
	if err != nil || saved.ImportSource == nil || saved.ImportSource.URL != "https://recipes.example.com/dinner?recipe_id=7" || saved.ImageURL != nil || !strings.HasPrefix(saved.Content.AllergenNote, imports.ImportedAllergenWarning) {
		t.Fatal("reviewed source not saved safely", err)
	}
	replay, err := s.SavePersonalRecipe(t.Context(), actor, premium, input, now)
	if err != nil || replay.VersionID != saved.VersionID {
		t.Fatal("save replay duplicated version", err)
	}
	input.RequestKey = strings.Repeat("c", 32)
	if _, err := s.SavePersonalRecipe(t.Context(), actor, premium, input, now); !errors.Is(err, ErrRecipeImportConflict) {
		t.Fatal("new save key reused consumed import", err)
	}
	var private int
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM recipe_import_operations WHERE id=? AND stage='saved' AND cipher IS NULL AND lease_hash='' AND recipe_id=? AND version_id=?", op.ID, saved.ID, saved.VersionID).Scan(&private); err != nil || private != 1 {
		t.Fatal("save did not purge private draft atomically", err)
	}
	update := PersonalSave{ID: saved.ID, ExpectedVersion: saved.VersionID, RequestKey: strings.Repeat("d", 32), Content: saved.Content}
	update.Content.Title = "Edited imported recipe"
	update.Content.AllergenNote = ""
	edited, err := s.SavePersonalRecipe(t.Context(), actor, premium, update, now)
	if err != nil || edited.ImportSource == nil || *edited.ImportSource != *saved.ImportSource || !strings.HasPrefix(edited.Content.AllergenNote, imports.ImportedAllergenWarning) {
		t.Fatal("edit lost source or unverified warning", err)
	}
	old, err := s.ReadPersonalRecipe(t.Context(), actor, saved.ID, saved.VersionID, now)
	if err != nil || old.Content.Title == edited.Content.Title {
		t.Fatal("edit changed frozen imported version", err)
	}
	queued, err := s.QueuePersonalRecipe(t.Context(), actor, premium, edited.ID, edited.VersionID, "2026-11-09", strings.Repeat("e", 32), now)
	if err != nil || len(queued.Recipes) != 1 || queued.Recipes[0].Source == nil || queued.Recipes[0].Source.Import == nil || queued.Recipes[0].Source.Import.URL != saved.ImportSource.URL {
		t.Fatal("planned snapshot lost source", err)
	}
	if _, err := s.ReadPersonalRecipe(t.Context(), actor, saved.ID, "", now); err != nil {
		t.Fatal("read needs Premium", err)
	}
}
func TestRecipeImportExportRetainedContentAndNoDispatchSecrets(t *testing.T) {
	s, actor, other, premium, now := personalFixture(t)
	vault := importVault(t)
	_, input := readyImportSave(t, s, actor, premium, vault, strings.Repeat("a", 32), now)
	if _, err := s.SavePersonalRecipe(t.Context(), actor, premium, input, now); err != nil {
		t.Fatal(err)
	}
	draft, _ := readyImportSave(t, s, actor, premium, vault, strings.Repeat("c", 32), now)
	foreign, _ := readyImportSave(t, s, other, premium, vault, strings.Repeat("d", 32), now)
	var password string
	if err := s.SQL.QueryRow("SELECT password FROM users WHERE user_id=?", actor.UserID).Scan(&password); err != nil {
		t.Fatal(err)
	}
	account := AccountActor{UserID: actor.UserID, SessionHash: actor.SessionHash, PasswordHash: password}
	var rejected bytes.Buffer
	if err := s.WriteAccountExport(t.Context(), account, &rejected, now); err == nil || rejected.Len() != 0 {
		t.Fatal("missing draft key emitted incomplete archive", err)
	}
	if err := s.DB.Model(&User{}).Where("user_id=?", actor.UserID).Update("email_verified_at", nil).Error; err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := s.WriteAccountExport(t.Context(), account, &out, now, AccountExportOptions{ImportVault: vault}); err != nil {
		t.Fatal(err)
	}
	files := readLifecycleExport(t, out.Bytes())
	if !bytes.Contains(files["recipe-import-drafts.json"], []byte("private-import-title-canary")) || !bytes.Contains(files["personal-import-sources.json"], []byte("https://recipes.example.com/dinner?recipe_id=7")) {
		t.Fatal("owned imports omitted from export")
	}
	for _, body := range files {
		for _, forbidden := range []string{draft.ID, foreign.ID, "leaseHash", "lease_hash", "sourceFingerprint", "source_fingerprint", "requestHash", "request_hash", "cipher"} {
			if bytes.Contains(body, []byte(forbidden)) {
				t.Fatal("export leaked dispatch or storage identifiers")
			}
		}
	}
}
func TestRecipeImportDeletionAndMatchedRestoreFenceLateCompletion(t *testing.T) {
	s, j, actor, other, premium, now := personalPhotoStore(t)
	vault := importVault(t)
	_, input := readyImportSave(t, s, actor, premium, vault, strings.Repeat("a", 32), now)
	if _, err := s.SavePersonalRecipe(t.Context(), actor, premium, input, now); err != nil {
		t.Fatal(err)
	}
	late, lease, err := s.BeginRecipeImport(t.Context(), actor, premium, vault, importInput(strings.Repeat("c", 32), now), now)
	if err != nil {
		t.Fatal(err)
	}
	_, foreignInput := readyImportSave(t, s, other, premium, vault, strings.Repeat("d", 32), now)
	foreignSaved, err := s.SavePersonalRecipe(t.Context(), other, premium, foreignInput, now)
	if err != nil {
		t.Fatal(err)
	}
	foreign, _ := readyImportSave(t, s, other, premium, vault, strings.Repeat("f", 32), now)
	backup := filepath.Join(t.TempDir(), "imports-before-delete.db")
	if err := s.Backup(t.Context(), backup); err != nil {
		t.Fatal(err)
	}
	var password string
	if err := s.SQL.QueryRow("SELECT password FROM users WHERE user_id=?", actor.UserID).Scan(&password); err != nil {
		t.Fatal(err)
	}
	if _, err := j.DeleteAccount(t.Context(), AccountActor{UserID: actor.UserID, SessionHash: actor.SessionHash, PasswordHash: password}, strings.Repeat("e", 32), "DELETE", now, AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	draft := importDraft(t, late.Private.Source)
	if _, err := s.CompleteRecipeImport(t.Context(), actor, premium, vault, lease, imports.PrivateDraft{Source: late.Private.Source, Draft: &draft}, now); err == nil {
		t.Fatal("deleted account accepted late content")
	}
	destination := filepath.Join(t.TempDir(), "restored-imports.db")
	if err := Restore(t.Context(), backup, destination, AccountRestoreOptions{JournalPath: j.Path}); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(destination, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	for _, db := range []*Store{s, restored} {
		for _, table := range []string{"recipe_import_operations", "personal_recipes"} {
			var n int
			if err := db.SQL.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE user_id=?", actor.UserID).Scan(&n); err != nil || n != 0 {
				t.Fatal("deletion or restore revived owned import", table, err)
			}
		}
		var n int
		if err := db.SQL.QueryRow("SELECT COUNT(*) FROM personal_recipe_import_sources").Scan(&n); err != nil || n != 1 {
			t.Fatal("deletion removed foreign provenance or retained deleted provenance", err)
		}
		if got, err := db.ReadPersonalRecipe(t.Context(), other, foreignSaved.ID, "", now); err != nil || got.ImportSource == nil {
			t.Fatal("foreign saved attribution was removed", err)
		}
		if got, err := db.ReadRecipeImport(t.Context(), other, vault, foreign.ID, now); err != nil || got.Draft == nil {
			t.Fatal("foreign import was removed", err)
		}
	}
}

func TestRecipeImportPhotoSaveRequiresReviewBeforeAdmission(t *testing.T) {
	s, j, actor, _, premium, now := personalPhotoStore(t)
	vault := importVault(t)
	_, input := readyImportSave(t, s, actor, premium, vault, strings.Repeat("a", 32), now)
	bad := input
	badProof := *input.Import
	badProof.Confirmation = imports.ReviewConfirmation{}
	bad.Import = &badProof
	bad.PhotoAction = "replace"
	bad.PhotoDigest = strings.Repeat("f", 64)
	if _, err := j.AdmitPersonalPhoto(t.Context(), actor, premium, bad, "192.0.2.1", now); !errors.Is(err, imports.ErrImportReviewRequired) {
		t.Fatal("unreviewed import admitted a dish-photo upload", err)
	}
	saved, _ := lifecyclePersonalPhoto(t, j, actor, premium, input, lifecyclePersonalJPEG(t, 42), now)
	if saved.ImportSource == nil || saved.PhotoID == nil || saved.ImageURL == nil {
		t.Fatal("deliberate dish photo lost import linkage")
	}
}
