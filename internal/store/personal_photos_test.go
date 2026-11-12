package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
)

func personalPhotoStore(t *testing.T) (*Store, *LifecycleJournal, PersonalActor, PersonalActor, PersonalEntitlementReader, time.Time) {
	t.Helper()
	s, actor, other, premium, now := personalFixture(t)
	j, err := s.InitializeLifecycleJournal(t.Context(), s.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return s, j, actor, other, premium, now
}

func personalUpload() PersonalPhotoUpload {
	return PersonalPhotoUpload{Binding: "fixture", Digest: strings.Repeat("a", 64), Width: 40, Height: 20, Size: 100}
}

func TestPersonalPhotoReservationReplayAndAtomicAttachment(t *testing.T) {
	s, j, actor, other, premium, now := personalPhotoStore(t)
	ctx := t.Context()
	input := PersonalSave{RequestKey: strings.Repeat("1", 32), Content: personalDocument("Private photo"), PhotoAction: "replace", PhotoDigest: personalUpload().Digest}
	reservation, err := j.ReservePersonalPhoto(ctx, actor, premium, input, personalUpload(), "192.0.2.1", now)
	if err != nil || reservation.AssetID == "" || reservation.Work.DeliveryType != "authenticated" || reservation.Work.Purpose != "personal" {
		t.Fatal("missing private reservation", err)
	}
	replay, err := j.ReservePersonalPhoto(ctx, actor, premium, input, personalUpload(), "192.0.2.1", now)
	if err != nil || replay.AssetID != reservation.AssetID || replay.Dispatch {
		t.Fatal("equal retry dispatched a new upload", err)
	}
	if _, err := s.ReadPersonalPhoto(ctx, other, reservation.AssetID, now); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatal("foreign photo identity leaked", err)
	}
	if err := j.BeginPersonalPhoto(ctx, actor, premium, input, reservation, now); err != nil {
		t.Fatal(err)
	}
	if err := j.BeginPersonalPhoto(ctx, actor, premium, input, reservation, now); err == nil {
		t.Fatal("duplicate upload began")
	}
	first, err := j.FinishPersonalPhoto(ctx, actor, premium, input, reservation, true, now)
	if err != nil || first.PhotoID == nil || *first.PhotoID != reservation.AssetID || first.ImageURL == nil || strings.Contains(*first.ImageURL, "mychef/") {
		t.Fatal("photo was not atomically attached", err)
	}
	metadata, err := s.ReadPersonalPhoto(ctx, actor, reservation.AssetID, now)
	if err != nil || metadata.Target != reservation.Work.Target || metadata.Digest != personalUpload().Digest {
		t.Fatal("owned saved photo unreadable", err)
	}
	again, err := j.FinishPersonalPhoto(ctx, actor, premium, input, reservation, true, now)
	if err != nil || again.VersionID != first.VersionID {
		t.Fatal("late duplicate result created a new version", err)
	}
	// A text edit inherits the saved image; removing it creates a new version,
	// while historical versions retain their image.
	edit := PersonalSave{ID: first.ID, ExpectedVersion: first.VersionID, RequestKey: strings.Repeat("2", 32), Content: first.Content}
	second, err := s.SavePersonalRecipe(ctx, actor, premium, edit, now)
	if err != nil || second.PhotoID == nil || *second.PhotoID != reservation.AssetID {
		t.Fatal("edit lost prior image", err)
	}
	edit.ExpectedVersion, edit.RequestKey, edit.PhotoAction = second.VersionID, strings.Repeat("3", 32), "remove"
	third, err := s.SavePersonalRecipe(ctx, actor, premium, edit, now)
	if err != nil || third.PhotoID != nil || third.ImageURL != nil {
		t.Fatal("photo removal failed", err)
	}
	old, err := s.ReadPersonalRecipe(ctx, actor, first.ID, first.VersionID, now)
	if err != nil || old.PhotoID == nil {
		t.Fatal("image history changed", err)
	}
	cleaner := &personalTestCleaner{fakeAssetCleaner: fakeAssetCleaner{binding: "fixture"}}
	if _, err := j.CleanupStep(ctx, cleaner, now.Add(2*time.Minute)); err != nil || len(cleaner.private) != 0 {
		t.Fatal("retained version image deleted", err)
	}
	if err := s.DeletePersonalRecipe(ctx, actor, first.ID, third.VersionID, strings.Repeat("4", 32), now); err != nil {
		t.Fatal(err)
	}
	if _, err := j.CleanupStep(ctx, cleaner, now.Add(26*time.Hour)); err != nil || len(cleaner.private) != 1 || len(cleaner.calls) != 0 {
		t.Fatal("live-account orphan did not use typed cleanup", err)
	}
	if _, err := j.FinishPersonalPhoto(ctx, actor, premium, input, reservation, true, now); err == nil {
		t.Fatal("old upload recreated a deleted recipe")
	}
}

type personalTestCleaner struct {
	fakeAssetCleaner
	private []string
}

func (f *personalTestCleaner) DeletePersonalPhoto(_ context.Context, target string) (AssetDeletionResult, error) {
	f.private = append(f.private, target)
	return AssetDeletionResult{Deleted: true}, nil
}

func TestPersonalPhotoLimitsPersistAndRetriesDoNotConsumeBudget(t *testing.T) {
	s, j, actor, _, _, now := personalPhotoStore(t)
	ctx := t.Context()
	if err := s.DB.Model(&Session{}).Where("hash=?", actor.SessionHash).Update("expires_unix", now.Add(3*time.Hour).Unix()).Error; err != nil {
		t.Fatal(err)
	}
	premium := func(context.Context, *gorm.DB, int64, string) (PersonalEntitlement, error) {
		return PersonalEntitlement{Premium: true, ExpiresUnix: now.Add(3 * time.Hour).Unix()}, nil
	}
	for i := 0; i < 10; i++ {
		in := PersonalSave{RequestKey: fmt.Sprintf("%032x", i+1), Content: personalDocument("Photo attempt"), PhotoAction: "replace", PhotoDigest: personalUpload().Digest}
		r, err := j.ReservePersonalPhoto(ctx, actor, premium, in, personalUpload(), "192.0.2.5", now)
		if err != nil || !r.Dispatch {
			t.Fatal("valid attempt blocked", i, err)
		}
		if _, err := j.FinishPersonalPhoto(ctx, actor, premium, in, r, false, now); err == nil {
			t.Fatal("failed upload saved text")
		}
		if retry, err := j.ReservePersonalPhoto(ctx, actor, premium, in, personalUpload(), "192.0.2.5", now); err != nil || retry.Dispatch {
			t.Fatal("exact failed retry consumed budget", err)
		}
	}
	in := PersonalSave{RequestKey: strings.Repeat("f", 32), Content: personalDocument("Limit"), PhotoAction: "replace", PhotoDigest: personalUpload().Digest}
	if _, err := j.ReservePersonalPhoto(ctx, actor, premium, in, personalUpload(), "192.0.2.5", now); !errors.Is(err, ErrPersonalPhotoLimit) {
		t.Fatal("account hourly limit absent", err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	j, err = s.OpenLifecycleJournal(ctx, s.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	if _, err := j.ReservePersonalPhoto(ctx, actor, premium, in, personalUpload(), "192.0.2.6", now); !errors.Is(err, ErrPersonalPhotoLimit) {
		t.Fatal("limit lost on restart", err)
	}
	if _, err := j.ReservePersonalPhoto(ctx, actor, premium, in, personalUpload(), "192.0.2.6", now.Add(time.Hour)); err != nil {
		t.Fatal("hourly limit never expired", err)
	}
}

func TestPersonalPhotoJournalUpgradeIsExplicitAndPreservesRegistry(t *testing.T) {
	s, actor, _, _, _ := personalFixture(t)
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "legacy-journal.db")
	external, err := Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	var binding, incarnation string
	if err := s.SQL.QueryRow("SELECT binding FROM lifecycle_binding").Scan(&binding); err != nil {
		t.Fatal(err)
	}
	if err := s.SQL.QueryRow("SELECT incarnation FROM account_incarnations WHERE user_id=?", actor.UserID).Scan(&incarnation); err != nil {
		t.Fatal(err)
	}
	if err := external.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(lifecycleJournalSchema).Error; err != nil {
			return err
		}
		if err := tx.Exec("INSERT INTO lifecycle_journal_meta VALUES(1,1,?)", binding).Error; err != nil {
			return err
		}
		return tx.Exec("INSERT INTO lifecycle_work(id,incarnation,kind,target,state) VALUES(?,?,'image','preserve-old-target','live')", strings.Repeat("a", 32), incarnation).Error
	}); err != nil {
		t.Fatal(err)
	}
	_ = external.Close()
	if _, err := s.SQL.Exec("UPDATE lifecycle_binding SET stage='prepared'; UPDATE lifecycle_binding SET stage='ready'"); err != nil {
		t.Fatal(err)
	}
	if j, err := s.OpenLifecycleJournal(ctx, path); err == nil {
		_ = j.Close()
		t.Fatal("runtime silently upgraded old journal")
	}
	j, err := s.InitializeLifecycleJournal(ctx, path)
	if err != nil {
		t.Fatal("explicit journal upgrade failed", err)
	}
	defer func() { _ = j.Close() }()
	var version int
	if err := j.store.SQL.QueryRow("SELECT version FROM lifecycle_journal_meta").Scan(&version); err != nil || version != 2 {
		t.Fatal("journal type extension not versioned", err)
	}
	work, err := j.WorkFor(ctx, incarnation)
	if err != nil || len(work) != 1 || work[0].Target != "preserve-old-target" || work[0].DeliveryType != "upload" || work[0].Purpose != "account" {
		t.Fatal("upgrade changed old work", err)
	}
}

func TestPersonalPhotoConcurrentBoundsAndSourceBudget(t *testing.T) {
	s, j, actor, other, premium, now := personalPhotoStore(t)
	ctx := t.Context()
	third := User{FirstName: "Third", LastName: "Chef", Email: "third@example.test", Username: "third", Password: "hash", EmailVerifiedAt: Ptr(now.Unix())}
	if err := s.DB.Create(&third).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Create(&Session{Hash: "third-session", UserID: &third.ID, ExpiresUnix: now.Add(time.Hour).Unix()}).Error; err != nil {
		t.Fatal(err)
	}
	thirdActor := PersonalActor{UserID: third.ID, SessionHash: "third-session"}
	in := PersonalSave{RequestKey: fmt.Sprintf("%032x", 1), Content: personalDocument("Bounded photo"), PhotoAction: "replace", PhotoDigest: personalUpload().Digest}
	first, err := j.ReservePersonalPhoto(ctx, actor, premium, in, personalUpload(), "192.0.2.7", now)
	if err != nil {
		t.Fatal(err)
	}
	in.RequestKey = fmt.Sprintf("%032x", 2)
	if _, err := j.ReservePersonalPhoto(ctx, actor, premium, in, personalUpload(), "192.0.2.7", now); !errors.Is(err, ErrPersonalPhotoBusy) {
		t.Fatal("per-account concurrency missing", err)
	}
	second, err := j.ReservePersonalPhoto(ctx, other, premium, in, personalUpload(), "192.0.2.7", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.ReservePersonalPhoto(ctx, thirdActor, premium, in, personalUpload(), "192.0.2.7", now); !errors.Is(err, ErrPersonalPhotoBusy) {
		t.Fatal("global concurrency missing", err)
	}
	in.RequestKey = fmt.Sprintf("%032x", 1)
	_, _ = j.FinishPersonalPhoto(ctx, actor, premium, in, first, false, now)
	in.RequestKey = fmt.Sprintf("%032x", 2)
	_, _ = j.FinishPersonalPhoto(ctx, other, premium, in, second, false, now)
	for _, owner := range []PersonalActor{actor, other} {
		for i := 0; i < 9; i++ {
			in.RequestKey = fmt.Sprintf("%032x", 100+i)
			r, err := j.ReservePersonalPhoto(ctx, owner, premium, in, personalUpload(), "192.0.2.7", now)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = j.FinishPersonalPhoto(ctx, owner, premium, in, r, false, now)
		}
	}
	in.RequestKey = fmt.Sprintf("%032x", 999)
	if _, err := j.ReservePersonalPhoto(ctx, thirdActor, premium, in, personalUpload(), "192.0.2.7", now); !errors.Is(err, ErrPersonalPhotoLimit) {
		t.Fatal("source budget absent", err)
	}
	if _, err := j.ReservePersonalPhoto(ctx, thirdActor, premium, in, personalUpload(), "192.0.2.8", now); err != nil {
		t.Fatal("source budget leaked across sources", err)
	}
	var raw int
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM personal_photo_limits WHERE key LIKE '%192.0.2.%'").Scan(&raw); err != nil || raw != 0 {
		t.Fatal("raw source persisted", err)
	}
}

func TestPersonalPhotoFailedAttachKeepsSavedStateAndTracksOrphan(t *testing.T) {
	for _, failure := range []string{"root", "version", "current", "receipt", "conflict", "expiry", "revoked", "deleted", "lease"} {
		t.Run(failure, func(t *testing.T) {
			s, j, actor, _, premium, now := personalPhotoStore(t)
			ctx := t.Context()
			base, err := s.SavePersonalRecipe(ctx, actor, premium, PersonalSave{RequestKey: strings.Repeat("a", 32), Content: personalDocument("Saved before photo")}, now)
			if err != nil {
				t.Fatal(err)
			}
			in := PersonalSave{ID: base.ID, ExpectedVersion: base.VersionID, RequestKey: strings.Repeat("b", 32), Content: personalDocument("Unsaved changes"), PhotoAction: "replace", PhotoDigest: personalUpload().Digest}
			r, err := j.ReservePersonalPhoto(ctx, actor, premium, in, personalUpload(), "192.0.2.10", now)
			if err != nil {
				t.Fatal(err)
			}
			if err := j.BeginPersonalPhoto(ctx, actor, premium, in, r, now); err != nil {
				t.Fatal(err)
			}
			finishTime := now
			switch failure {
			case "conflict":
				_, err = s.SavePersonalRecipe(ctx, actor, premium, PersonalSave{ID: base.ID, ExpectedVersion: base.VersionID, RequestKey: strings.Repeat("c", 32), Content: personalDocument("Other tab")}, now)
			case "expiry":
				premium = nil
			case "revoked":
				err = s.DB.Where("hash=?", actor.SessionHash).Delete(&Session{}).Error
			case "deleted":
				err = s.DeletePersonalRecipe(ctx, actor, base.ID, base.VersionID, strings.Repeat("d", 32), now)
			case "lease":
				finishTime = now.Add(2 * time.Minute)
			}
			if err != nil {
				t.Fatal(err)
			}
			fault := PersonalWriteOptions{Fault: func(stage string) error {
				if stage == failure {
					return errors.New("injected attach fault")
				}
				return nil
			}}
			if _, err := j.FinishPersonalPhoto(ctx, actor, premium, in, r, true, finishTime, fault); err == nil {
				t.Fatal("failed attachment accepted")
			}
			var changed int
			if err := s.SQL.QueryRow("SELECT COUNT(*) FROM personal_recipe_versions WHERE json_extract(body,'$.title')='Unsaved changes'").Scan(&changed); err != nil || changed != 0 {
				t.Fatal("partial recipe survived", err)
			}
			cleaner := &personalTestCleaner{fakeAssetCleaner: fakeAssetCleaner{binding: "fixture"}}
			if _, err := j.CleanupStep(ctx, cleaner, now.Add(3*time.Minute)); err != nil || len(cleaner.private) != 1 {
				t.Fatal("confirmed orphan not cleaned", err)
			}
			if failure != "deleted" && failure != "conflict" {
				var current string
				if err := s.SQL.QueryRow("SELECT current_version FROM personal_recipes WHERE id=?", base.ID).Scan(&current); err != nil || current != base.VersionID {
					t.Fatal("failed upload rewrote saved state", err)
				}
			}
		})
	}
}

func TestPersonalPhotoAdmissionCountsInvalidAttemptsBeforeDecoding(t *testing.T) {
	s, j, actor, _, premium, now := personalPhotoStore(t)
	ctx := t.Context()
	in := PersonalSave{RequestKey: strings.Repeat("a", 32), Content: personalDocument("Private input"), PhotoAction: "replace", PhotoDigest: strings.Repeat("c", 64)}
	admission, err := j.AdmitPersonalPhoto(ctx, actor, premium, in, "192.0.2.9", now)
	if err != nil || !admission.Dispatch {
		t.Fatal("admission failed", err)
	}
	if same, err := j.AdmitPersonalPhoto(ctx, actor, premium, in, "192.0.2.9", now); err != nil || same.Dispatch {
		t.Fatal("equal retry admitted another decoder", err)
	}
	textOnly := in
	textOnly.PhotoAction = ""
	textOnly.PhotoDigest = ""
	if _, err := s.SavePersonalRecipe(ctx, actor, premium, textOnly, now); !errors.Is(err, ErrPersonalKeyConflict) {
		t.Fatal("photo request key was reused for a text save", err)
	}
	if err := j.FailPersonalPhotoAdmission(ctx, actor, admission); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 10; i++ {
		in.RequestKey = fmt.Sprintf("%032x", i)
		a, err := j.AdmitPersonalPhoto(ctx, actor, premium, in, "192.0.2.9", now)
		if err != nil {
			t.Fatal(err)
		}
		if err := j.FailPersonalPhotoAdmission(ctx, actor, a); err != nil {
			t.Fatal(err)
		}
	}
	in.RequestKey = strings.Repeat("f", 32)
	if _, err := j.AdmitPersonalPhoto(ctx, actor, premium, in, "192.0.2.9", now); !errors.Is(err, ErrPersonalPhotoLimit) {
		t.Fatal("invalid decode attempts bypassed budget", err)
	}
	var leaked int
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM personal_photo_requests WHERE request_hash LIKE '%Private input%'").Scan(&leaked); err != nil || leaked != 0 {
		t.Fatal("admission retained recipe text", err)
	}
}

func TestPersonalPhotoLostAndLateResultsStayTrackedWithLiveOrDeletedAccount(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(fmt.Sprint(deleted), func(t *testing.T) {
			s, j, actor, _, premium, now := personalPhotoStore(t)
			ctx := t.Context()
			in := PersonalSave{RequestKey: strings.Repeat("a", 32), Content: personalDocument("Late upload"), PhotoAction: "replace", PhotoDigest: personalUpload().Digest}
			r, err := j.ReservePersonalPhoto(ctx, actor, premium, in, personalUpload(), "192.0.2.15", now)
			if err != nil {
				t.Fatal(err)
			}
			if err := j.BeginPersonalPhoto(ctx, actor, premium, in, r, now); err != nil {
				t.Fatal(err)
			}
			if deleted {
				account := AccountActor{UserID: actor.UserID, SessionHash: actor.SessionHash, PasswordHash: "hash"}
				if _, err := j.DeleteAccount(ctx, account, strings.Repeat("d", 32), "DELETE", now, AccountDeletionOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			cleaner := &personalTestCleaner{fakeAssetCleaner: fakeAssetCleaner{binding: "fixture"}}
			if did, err := j.CleanupStep(ctx, cleaner, now.Add(2*time.Minute)); err != nil || !did {
				t.Fatal(err)
			}
			pending, err := j.WorkFor(ctx, r.Work.Incarnation)
			if err != nil || len(pending) != 1 || pending[0].Code != "upload_outcome_unconfirmed" {
				t.Fatal("early absence lost a potentially late upload", err)
			}
			if _, err := j.FinishPersonalPhoto(ctx, actor, premium, in, r, true, now.Add(2*time.Minute)); err == nil {
				t.Fatal("late result saved a recipe")
			}
			if did, err := j.CleanupStep(ctx, cleaner, now.Add(3*time.Minute)); err != nil || !did {
				t.Fatal(err)
			}
			pending, err = j.WorkFor(ctx, r.Work.Incarnation)
			if err != nil || len(pending) != 0 {
				t.Fatal("late confirmed orphan not cleaned", err)
			}
			var recipes int
			if err := s.SQL.QueryRow("SELECT COUNT(*) FROM personal_recipes").Scan(&recipes); err != nil || recipes != 0 {
				t.Fatal("late result restored private content", err)
			}
		})
	}
}

func TestPersonalPhotoAdmissionSerializesAcrossDatabaseHandles(t *testing.T) {
	s, j, actor, _, premium, now := personalPhotoStore(t)
	other, err := Open(s.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	journal, err := other.OpenLifecycleJournal(t.Context(), j.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	var group sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		group.Go(func() {
			owner := j
			if i%2 == 1 {
				owner = journal
			}
			in := PersonalSave{RequestKey: fmt.Sprintf("%032x", i+1), Content: personalDocument("Concurrent photo"), PhotoAction: "replace", PhotoDigest: personalUpload().Digest}
			_, err := owner.AdmitPersonalPhoto(t.Context(), actor, premium, in, "192.0.2.16", now)
			results <- err
		})
	}
	group.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrPersonalPhotoBusy) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatal("multiple in-flight admissions", winners)
	}
}

func TestPersonalPhotoUsesFreshEntitlementAtAttachment(t *testing.T) {
	s, journal, actor, _, _, now := personalPhotoStore(t)
	ctx := t.Context()
	if err := s.DB.Exec("UPDATE users SET email='photo-entitlement@example.test',email_key='photo-entitlement@example.test' WHERE user_id=?", actor.UserID).Error; err != nil {
		t.Fatal(err)
	}
	at := now
	reader, _ := testEntitlementSwitch(t, s, actor, entitlementIdentity(t, s, actor.UserID), func() time.Time { return at })
	first := PersonalSave{RequestKey: strings.Repeat("a", 32), Content: personalDocument("Kept photo"), PhotoAction: "replace", PhotoDigest: personalUpload().Digest}
	prepare := func(input PersonalSave) PersonalPhotoReservation {
		t.Helper()
		r, err := journal.ReservePersonalPhoto(ctx, actor, reader, input, personalUpload(), "192.0.2.9", now)
		if err != nil {
			t.Fatal(err)
		}
		if err := journal.BeginPersonalPhoto(ctx, actor, reader, input, r, now); err != nil {
			t.Fatal(err)
		}
		return r
	}
	firstReservation := prepare(first)
	recipe, err := journal.FinishPersonalPhoto(ctx, actor, reader, first, firstReservation, true, now)
	if err != nil || recipe.PhotoID == nil {
		t.Fatal("initial photo save failed", err)
	}
	edit := PersonalSave{ID: recipe.ID, ExpectedVersion: recipe.VersionID, RequestKey: strings.Repeat("b", 32), Content: personalDocument("Denied attachment"), PhotoAction: "replace", PhotoDigest: personalUpload().Digest}
	reservation := prepare(edit)
	at = now.Add(time.Minute)
	if _, err := journal.FinishPersonalPhoto(ctx, actor, reader, edit, reservation, true, now); !errors.Is(err, ErrPersonalPermission) {
		t.Fatal("photo attachment used admission time after grant expiry", err)
	}
	got, err := s.ReadPersonalRecipe(ctx, actor, recipe.ID, "", now)
	if err != nil || got.VersionID != recipe.VersionID || got.PhotoID == nil || *got.PhotoID != *recipe.PhotoID {
		t.Fatal("denied edit changed retained recipe", err)
	}
	if _, err := s.ReadPersonalPhoto(ctx, actor, *recipe.PhotoID, now); err != nil {
		t.Fatal("retained photo needed Premium", err)
	}
	cleaner := &personalTestCleaner{fakeAssetCleaner: fakeAssetCleaner{binding: "fixture"}}
	// The retained original and rejected upload may both be due. Cleanup
	// examines one item per call, and their opaque IDs do not define order.
	for range 2 {
		if _, err := journal.CleanupStep(ctx, cleaner, now.Add(3*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if len(cleaner.private) != 1 || cleaner.private[0] != reservation.Work.Target {
		t.Fatal("cleanup did not remove only the denied upload")
	}
}
