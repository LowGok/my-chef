package store

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/domain"
	"gorm.io/gorm"
)

func lifecyclePersonalJPEG(t *testing.T, value uint8) []byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 4, 4))
	im.Set(0, 0, color.RGBA{R: value, G: 20, B: 30, A: 255})
	var out bytes.Buffer
	if err := jpeg.Encode(&out, im, nil); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func lifecyclePersonalPhoto(t *testing.T, j *LifecycleJournal, actor PersonalActor, reader PersonalEntitlementReader, input PersonalSave, data []byte, now time.Time) (PersonalRecipe, PersonalPhotoReservation) {
	t.Helper()
	hash := sha256.Sum256(data)
	input.PhotoAction = "replace"
	input.PhotoDigest = hex.EncodeToString(hash[:])
	upload := PersonalPhotoUpload{Binding: "fixture", Digest: input.PhotoDigest, Width: 4, Height: 4, Size: len(data)}
	r, err := j.ReservePersonalPhoto(t.Context(), actor, reader, input, upload, "192.0.2.20", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.BeginPersonalPhoto(t.Context(), actor, reader, input, r, now); err != nil {
		t.Fatal(err)
	}
	recipe, err := j.FinishPersonalPhoto(t.Context(), actor, reader, input, r, true, now)
	if err != nil {
		t.Fatal(err)
	}
	return recipe, r
}

func personalLifecycleExportFixture(t *testing.T) (*Store, *LifecycleJournal, AccountActor, map[string][]byte, PersonalRecipe, PersonalRecipe) {
	t.Helper()
	s, j, actor, other, reader, now := personalPhotoStore(t)
	firstData, secondData, foreignData := lifecyclePersonalJPEG(t, 40), lifecyclePersonalJPEG(t, 100), lifecyclePersonalJPEG(t, 200)
	first, r1 := lifecyclePersonalPhoto(t, j, actor, reader, PersonalSave{RequestKey: strings.Repeat("1", 32), Content: personalDocument("OWNER-PERSONAL-FIRST-CANARY")}, firstData, now)
	second, err := s.SavePersonalRecipe(t.Context(), actor, reader, PersonalSave{ID: first.ID, ExpectedVersion: first.VersionID, RequestKey: strings.Repeat("2", 32), Content: personalDocument("OWNER-PERSONAL-SECOND-CANARY")}, now)
	if err != nil {
		t.Fatal(err)
	}
	third, r2 := lifecyclePersonalPhoto(t, j, actor, reader, PersonalSave{ID: first.ID, ExpectedVersion: second.VersionID, RequestKey: strings.Repeat("3", 32), Content: personalDocument("OWNER-PERSONAL-THIRD-CANARY")}, secondData, now)
	foreign, r3 := lifecyclePersonalPhoto(t, j, other, reader, PersonalSave{RequestKey: strings.Repeat("1", 32), Content: personalDocument("OTHER-PERSONAL-OWNER-CANARY")}, foreignData, now)
	week, err := s.QueuePersonalRecipe(t.Context(), actor, reader, third.ID, third.VersionID, "2026-11-09", strings.Repeat("4", 32), now)
	if err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(domain.GenerationRequest{MenuInput: domain.MenuInput{Dates: []string{"2026-11-09"}, Adults: 2, MaxMinutes: 30, Budget: "low", Region: "BE", Creativity: 3}, Favorites: []domain.PlannedFavorite{{PersonalEntryID: Ptr(week.Entries[0].ID), Date: "2026-11-09", Recipe: week.Recipes[0]}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Create(&Generation{UserID: actor.UserID, Key: "personal-export-op", RequestHash: "hash", RequestJSON: string(request), Status: "queued", Stage: "queued"}).Error; err != nil {
		t.Fatal(err)
	}
	return s, j, AccountActor{UserID: actor.UserID, SessionHash: actor.SessionHash, PasswordHash: "hash"}, map[string][]byte{r1.Work.Target: firstData, r2.Work.Target: secondData, r3.Work.Target: foreignData}, third, foreign
}

func TestPersonalAccountExportIncludesAllVersionsAndNormalizedPhotoFiles(t *testing.T) {
	s, _, actor, photos, recipe, foreign := personalLifecycleExportFixture(t)
	var archive bytes.Buffer
	calls := 0
	read := func(_ context.Context, asset PersonalPhotoAsset) ([]byte, error) {
		calls++
		if asset.UserID != actor.UserID {
			return nil, errors.New("foreign photo requested")
		}
		return bytes.Clone(photos[asset.Target]), nil
	}
	if err := s.WriteAccountExport(t.Context(), actor, &archive, time.Now(), AccountExportOptions{ReadPhoto: read}); err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	photoCount := 0
	for _, file := range r.File {
		body, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(body)
		_ = body.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[file.Name] = data
		if strings.HasPrefix(file.Name, "photos/") {
			photoCount++
			if !strings.HasSuffix(file.Name, ".jpg") {
				t.Fatal("export retained original photo format/name")
			}
			if _, err := jpeg.Decode(bytes.NewReader(data)); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if bytes.Contains(data, []byte("OTHER-PERSONAL-OWNER-CANARY")) || bytes.Contains(data, []byte("mychef/")) || bytes.Contains(data, []byte(recipe.ID)) || bytes.Contains(data, []byte(foreign.ID)) || bytes.Contains(data, []byte("personal-photos/")) {
			t.Fatal("export exposed a foreign record or storage identity")
		}
	}
	if photoCount != 2 || calls != 4 {
		t.Fatal("retained photos missing or not preflighted", photoCount, calls)
	}
	var versions []map[string]any
	if json.Unmarshal(files["personal-versions.json"], &versions) != nil || len(versions) != 3 {
		t.Fatal("not every immutable version was exported")
	}
	for _, name := range []string{"personal-recipes.json", "personal-queue.json", "personal-photos.json"} {
		if !json.Valid(files[name]) {
			t.Fatal("missing personal export category", name)
		}
	}
	var roots, photoManifest, queue []map[string]any
	if json.Unmarshal(files["personal-recipes.json"], &roots) != nil || len(roots) != 1 || json.Unmarshal(files["personal-photos.json"], &photoManifest) != nil || len(photoManifest) != 2 || json.Unmarshal(files["personal-queue.json"], &queue) != nil || len(queue) != 1 {
		t.Fatal("export relationships unavailable")
	}
	byVersion := map[string]map[string]any{}
	for _, version := range versions {
		byVersion[version["id"].(string)] = version
		if version["recipeId"] != roots[0]["id"] {
			t.Fatal("version lost root relationship")
		}
	}
	current := byVersion[roots[0]["currentVersion"].(string)]
	previous := byVersion[current["previousVersion"].(string)]
	original := byVersion[previous["previousVersion"].(string)]
	if original["previousVersion"] != nil || previous["photoId"] != original["photoId"] || current["photoId"] == previous["photoId"] {
		t.Fatal("export changed version/photo history")
	}
	for _, photo := range photoManifest {
		data := files[photo["file"].(string)]
		hash := sha256.Sum256(data)
		if len(data) == 0 || photo["sha256"] != hex.EncodeToString(hash[:]) {
			t.Fatal("photo manifest does not match file")
		}
	}
	queuedRecipe := queue[0]["recipe"].(map[string]any)
	source := queuedRecipe["source"].(map[string]any)
	if queue[0]["recipeId"] != roots[0]["id"] || queue[0]["versionId"] != current["id"] || source["recipeId"] != roots[0]["id"] || source["versionId"] != current["id"] || source["photoId"] != current["photoId"] {
		t.Fatal("queued source links not export-local/consistent")
	}
	if bytes.Contains(files["personal-versions.json"], []byte("request_key")) || bytes.Contains(files["personal-photos.json"], []byte("target")) || bytes.Contains(files["personal-photos.json"], []byte("binding")) {
		t.Fatal("security receipts/provider identity exported")
	}
}

func TestPersonalAccountExportFailsEntireArchiveOnMissingOrChangedPhoto(t *testing.T) {
	s, _, actor, photos, _, _ := personalLifecycleExportFixture(t)
	for _, mode := range []string{"absent_reader", "failed_preflight", "failed_second_pass", "wrong_bytes"} {
		t.Run(mode, func(t *testing.T) {
			var out bytes.Buffer
			calls := 0
			var read func(context.Context, PersonalPhotoAsset) ([]byte, error)
			if mode != "absent_reader" {
				read = func(_ context.Context, asset PersonalPhotoAsset) ([]byte, error) {
					calls++
					if mode == "failed_preflight" || mode == "failed_second_pass" && calls > 2 {
						return nil, errors.New("private-photo-provider-credential-canary")
					}
					if mode == "wrong_bytes" {
						return []byte("wrong private image"), nil
					}
					return bytes.Clone(photos[asset.Target]), nil
				}
			}
			err := s.WriteAccountExport(t.Context(), actor, &out, time.Now(), AccountExportOptions{ReadPhoto: read})
			if err == nil || strings.Contains(err.Error(), "credential-canary") {
				t.Fatal("failed photo produced successful/leaking export", err)
			}
			if mode != "failed_second_pass" && out.Len() != 0 {
				t.Fatal("export started before photo preflight passed")
			}
			if _, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len())); err == nil {
				t.Fatal("failed archive finalized")
			}
		})
	}
}

func TestPersonalAccountDeletionAndMatchedRestoreCannotBringDataBack(t *testing.T) {
	s, j, actor, _, owned, foreign := personalLifecycleExportFixture(t)
	ctx := t.Context()
	now := time.Now()
	backup := filepath.Join(t.TempDir(), "before-personal-deletion.db")
	var incarnation string
	if err := s.SQL.QueryRow("SELECT incarnation FROM account_incarnations WHERE user_id=?", actor.UserID).Scan(&incarnation); err != nil {
		t.Fatal(err)
	}
	var ownedTargets []string
	if err := s.DB.Model(&PersonalPhotoAsset{}).Where("user_id=?", actor.UserID).Pluck("target", &ownedTargets).Error; err != nil || len(ownedTargets) != 2 {
		t.Fatal("owned target fixture incomplete", err)
	}
	expectedTargets := map[string]bool{}
	for _, target := range ownedTargets {
		expectedTargets[target] = true
	}
	if err := s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	if _, err := j.DeleteAccount(ctx, actor, strings.Repeat("d", 32), "DELETE", now, AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"personal_recipes", "personal_recipe_operations", "personal_photo_assets", "personal_photo_attempts", "personal_photo_requests", "personal_recipe_queue", "personal_queue_receipts"} {
		var count int
		if err := s.SQL.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE user_id=?", actor.UserID).Scan(&count); err != nil || count != 0 {
			t.Fatal("personal account row survived", table, err)
		}
	}
	var versions int
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM personal_recipe_versions WHERE recipe_id=?", owned.ID).Scan(&versions); err != nil || versions != 0 {
		t.Fatal("owned versions survived", err)
	}
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM personal_photo_limits WHERE key=?", "account:"+incarnation).Scan(&versions); err != nil || versions != 0 {
		t.Error("deleted account retained its upload counter", err)
	}
	var other int
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM personal_recipes WHERE id=?", foreign.ID).Scan(&other); err != nil || other != 1 {
		t.Fatal("other owner's library changed", err)
	}
	cleaner := &personalTestCleaner{fakeAssetCleaner: fakeAssetCleaner{binding: "fixture"}}
	for i := 0; i < 5; i++ {
		if _, err := j.CleanupStep(ctx, cleaner, now.Add(2*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if len(cleaner.private) != 2 {
		t.Fatal("deleted account's photos not cleaned exactly", len(cleaner.private))
	}
	for _, target := range cleaner.private {
		if !expectedTargets[target] {
			t.Fatal("cleanup targeted another owner's photo")
		}
	}
	destination := filepath.Join(t.TempDir(), "restored.db")
	if err := Restore(ctx, backup, destination, AccountRestoreOptions{JournalPath: j.Path}); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(destination, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	if err := restored.SQL.QueryRow("SELECT COUNT(*) FROM personal_recipes WHERE id=?", owned.ID).Scan(&versions); err != nil || versions != 0 {
		t.Fatal("restore resurrected deleted library", err)
	}
	if err := restored.SQL.QueryRow("SELECT COUNT(*) FROM personal_photo_assets WHERE user_id=?", actor.UserID).Scan(&versions); err != nil || versions != 0 {
		t.Fatal("restore resurrected photo access", err)
	}
	if err := restored.SQL.QueryRow("SELECT COUNT(*) FROM personal_recipes WHERE id=?", foreign.ID).Scan(&other); err != nil || other != 1 {
		t.Fatal("restore removed another owner's library", err)
	}
	tracked, err := j.WorkFor(ctx, incarnation)
	if err != nil || len(tracked) != 2 {
		t.Fatal("restored private targets were not captured before purge", err)
	}
	for _, work := range tracked {
		if work.DeliveryType != "authenticated" || work.Purpose != "personal" || !expectedTargets[work.Target] {
			t.Fatal("restore lost private delivery identity")
		}
	}
}

func TestPersonalRestoreUpgradesPriorSnapshotBeforeReconcilingPrivateAssets(t *testing.T) {
	s := versionTwelvePersonalFixture(t)
	ctx := t.Context()
	now := time.Now()
	if err := s.migratePersonalRecipes(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.migratePersonalPhotos(ctx); err != nil {
		t.Fatal(err)
	}
	var binding, incarnation string
	if err := s.SQL.QueryRow("SELECT binding FROM lifecycle_binding").Scan(&binding); err != nil {
		t.Fatal(err)
	}
	if err := s.SQL.QueryRow("SELECT incarnation FROM account_incarnations WHERE user_id=1").Scan(&incarnation); err != nil {
		t.Fatal(err)
	}
	journalPath := s.Path + ".lifecycle"
	external, err := Open(journalPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := external.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(lifecycleJournalSchema).Error; err != nil {
			return err
		}
		return tx.Exec("INSERT INTO lifecycle_journal_meta VALUES(1,1,?)", binding).Error
	}); err != nil {
		t.Fatal(err)
	}
	if err := migratePersonalPhotoJournal(ctx, external); err != nil {
		t.Fatal(err)
	}
	_ = external.Close()
	if err := s.DB.Exec("UPDATE lifecycle_binding SET stage='prepared';UPDATE lifecycle_binding SET stage='ready'").Error; err != nil {
		t.Fatal(err)
	}
	data := lifecyclePersonalJPEG(t, 70)
	sum := sha256.Sum256(data)
	asset := PersonalPhotoAsset{ID: strings.Repeat("c", 32), UserID: 1, Incarnation: incarnation, Target: "mychef/" + strings.Repeat("f", 32), Binding: "fixture", DeliveryType: "authenticated", Digest: hex.EncodeToString(sum[:]), Width: 4, Height: 4, Size: len(data), CreatedNS: now.UnixNano()}
	content, err := personalDocument("OLDER-SNAPSHOT-PERSONAL-CANARY").Normalize()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(content)
	root := personalRoot{ID: strings.Repeat("a", 32), UserID: 1, Incarnation: incarnation, CurrentVersion: strings.Repeat("b", 32), Title: content.Title, SearchTitle: strings.ToLower(content.Title), SourceYield: "[]", CreatedNS: now.UnixNano(), UpdatedNS: now.UnixNano()}
	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&asset).Error; err != nil {
			return err
		}
		if err := tx.Create(&root).Error; err != nil {
			return err
		}
		return tx.Create(&personalVersion{ID: root.CurrentVersion, RecipeID: root.ID, Body: string(body), PhotoID: &asset.ID, CreatedNS: now.UnixNano()}).Error
	}); err != nil {
		t.Fatal(err)
	}
	var user User
	if err := s.DB.First(&user, 1).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Create(&Session{Hash: "prior-snapshot-session", UserID: Ptr(int64(1)), ExpiresUnix: now.Add(time.Hour).Unix()}).Error; err != nil {
		t.Fatal(err)
	}
	// This is an untouched schema-14 snapshot, as produced by the previous binary.
	backup := filepath.Join(t.TempDir(), "prior-schema-14.db")
	if _, err := s.SQL.Exec("VACUUM INTO ?", backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(backup, 0600); err != nil {
		t.Fatal(err)
	}
	probe, err := OpenReadOnly(backup)
	if err != nil {
		t.Fatal(err)
	}
	if opened, err := probe.OpenLifecycleJournal(ctx, journalPath); err == nil {
		_ = opened.Close()
		t.Fatal("normal runtime accepted a prior application schema")
	}
	_ = probe.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	j, err := s.OpenLifecycleJournal(ctx, journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	if _, err := j.DeleteAccount(ctx, AccountActor{UserID: 1, SessionHash: "prior-snapshot-session", PasswordHash: user.Password}, strings.Repeat("d", 32), "DELETE", now, AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	cleaner := &personalTestCleaner{fakeAssetCleaner: fakeAssetCleaner{binding: "fixture"}}
	if _, err := j.CleanupStep(ctx, cleaner, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "upgraded-and-reconciled.db")
	if err := Restore(ctx, backup, destination, AccountRestoreOptions{JournalPath: journalPath}); err != nil {
		t.Fatal("prior snapshot was rejected before explicit restore upgrade", err)
	}
	restored, err := Open(destination, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	if err := restored.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
	probe, err = OpenReadOnly(backup)
	if err != nil {
		t.Fatal(err)
	}
	var sourceVersion int
	if err := probe.SQL.QueryRow("SELECT version FROM schema_migrations").Scan(&sourceVersion); err != nil || sourceVersion != 14 {
		t.Fatal("restore changed its source snapshot", err)
	}
	_ = probe.Close()
	var count int
	if err := restored.SQL.QueryRow("SELECT COUNT(*) FROM personal_recipes").Scan(&count); err != nil || count != 0 {
		t.Fatal("older snapshot brought back deleted private content", err)
	}
	work, err := j.WorkFor(ctx, incarnation)
	if err != nil || len(work) != 1 || work[0].DeliveryType != "authenticated" || work[0].Target != asset.Target {
		t.Fatal("older snapshot lost exact private cleanup identity", err)
	}
}

func TestPersonalExportScopesUnscheduledIngredientIdentitiesPerSavedVersion(t *testing.T) {
	s, actor, _, reader, now := personalFixture(t)
	for i := 0; i < 2; i++ {
		key := strings.Repeat([]string{"a", "b"}[i], 32)
		recipe, err := s.SavePersonalRecipe(t.Context(), actor, reader, PersonalSave{RequestKey: key, Content: personalDocument("Separate private recipe")}, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.QueuePersonalRecipe(t.Context(), actor, reader, recipe.ID, recipe.VersionID, "2026-11-09", strings.Repeat([]string{"c", "d"}[i], 32), now); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := s.WriteAccountExport(t.Context(), AccountActor{UserID: actor.UserID, SessionHash: actor.SessionHash, PasswordHash: "hash"}, &out, now); err != nil {
		t.Fatal(err)
	}
	files := readLifecycleExport(t, out.Bytes())
	var queue []struct {
		Recipe domain.Recipe `json:"recipe"`
	}
	if json.Unmarshal(files["personal-queue.json"], &queue) != nil || len(queue) != 2 {
		t.Fatal("missing queued recipes")
	}
	if queue[0].Recipe.Ingredients[0].ID == queue[1].Recipe.Ingredients[0].ID {
		t.Fatal("different personal recipes share an export ingredient identity")
	}
}

func TestPersonalAccountActionsStayAvailableWhileUnverifiedAndPurgeFaultRollsBack(t *testing.T) {
	s, j, actor, photos, _, _ := personalLifecycleExportFixture(t)
	ctx := t.Context()
	now := time.Now()
	if err := s.DB.Model(&User{}).Where("user_id=?", actor.UserID).Update("email_verified_at", nil).Error; err != nil {
		t.Fatal(err)
	}
	read := func(_ context.Context, asset PersonalPhotoAsset) ([]byte, error) {
		return bytes.Clone(photos[asset.Target]), nil
	}
	var out bytes.Buffer
	if err := s.WriteAccountExport(ctx, actor, &out, now, AccountExportOptions{ReadPhoto: read}); err != nil {
		t.Fatal("unverified owner could not export own photos", err)
	}
	if _, err := j.DeleteAccount(ctx, actor, strings.Repeat("d", 32), "DELETE", now, AccountDeletionOptions{Fault: func(stage string) error {
		if stage == "feedback" {
			return errors.New("injected account purge fault")
		}
		return nil
	}}); err == nil {
		t.Fatal("failed purge accepted")
	}
	var roots, assets, counters int
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM personal_recipes WHERE user_id=?", actor.UserID).Scan(&roots); err != nil || roots != 1 {
		t.Fatal("failed purge removed private roots", err)
	}
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM personal_photo_assets WHERE user_id=?", actor.UserID).Scan(&assets); err != nil || assets != 2 {
		t.Fatal("failed purge removed photo identities", err)
	}
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM personal_photo_limits WHERE key IN (SELECT 'account:'||incarnation FROM account_incarnations WHERE user_id=?)", actor.UserID).Scan(&counters); err != nil || counters != 1 {
		t.Fatal("failed purge did not restore upload budget", err)
	}
	if _, err := j.DeleteAccount(ctx, actor, strings.Repeat("e", 32), "DELETE", now, AccountDeletionOptions{}); err != nil {
		t.Fatal("unverified owner could not delete own data", err)
	}
}

func TestPersonalExportKeepsCapturedPhotoAfterLibraryDeletionAndOmitsOrphan(t *testing.T) {
	s, _, actor, photos, recipe, _ := personalLifecycleExportFixture(t)
	ctx := t.Context()
	now := time.Now()
	if err := s.DeletePersonalRecipe(ctx, PersonalActor{UserID: actor.UserID, SessionHash: actor.SessionHash}, recipe.ID, recipe.VersionID, strings.Repeat("5", 32), now); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	reads := 0
	read := func(_ context.Context, asset PersonalPhotoAsset) ([]byte, error) {
		reads++
		if asset.ID != *recipe.PhotoID {
			return nil, errors.New("orphan photo should not be exported")
		}
		return bytes.Clone(photos[asset.Target]), nil
	}
	if err := s.WriteAccountExport(ctx, actor, &out, now, AccountExportOptions{ReadPhoto: read}); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatal(err)
	}
	photoFiles := 0
	for _, file := range archive.File {
		if strings.HasPrefix(file.Name, "photos/") {
			photoFiles++
		}
		if file.Name == "personal-versions.json" || file.Name == "personal-recipes.json" {
			f, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(f)
			_ = f.Close()
			if err != nil {
				t.Fatal(err)
			}
			var records []any
			if json.Unmarshal(data, &records) != nil || len(records) != 0 {
				t.Fatal("export revived deleted library content")
			}
		}
	}
	if reads != 2 || photoFiles != 1 {
		t.Fatal("captured photo missing or an orphan was included")
	}
}
