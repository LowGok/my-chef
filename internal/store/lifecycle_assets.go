package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"gorm.io/gorm"
)

func (j *LifecycleJournal) ReserveRecipeImage(ctx context.Context, user, recipe int64, provider ...string) (LifecycleWork, error) {
	j.app.AccountWork.RLock()
	defer j.app.AccountWork.RUnlock()
	binding := "fixture"
	if len(provider) > 0 {
		binding = provider[0]
	}
	if binding != "fixture" && !cloudinaryBindingPattern.MatchString(binding) {
		return LifecycleWork{}, ErrLifecycleConflict
	}
	var work LifecycleWork
	err := j.app.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var incarnation string
		if err := tx.Raw(`SELECT a.incarnation FROM account_incarnations a JOIN meals m ON m.creator_id=a.user_id WHERE a.user_id=? AND m.meal_id=? AND m.image_status='pending'`, user, recipe).Row().Scan(&incarnation); err != nil {
			return ErrAccountAccess
		}
		id, err := randomLifecycleID()
		if err != nil {
			return err
		}
		objectID, err := randomLifecycleID()
		if err != nil {
			return err
		}
		work = LifecycleWork{ID: id, Incarnation: incarnation, Kind: "image", Target: "mychef/" + objectID, Provider: binding, State: "reserved"}
		return j.reserveLifecycleWork(ctx, work)
	})
	return work, err
}
func (j *LifecycleJournal) BeginImageUpload(ctx context.Context, id, target string) error {
	if err := j.CheckAvailable(ctx); err != nil {
		return err
	}
	return j.store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var work LifecycleWork
		if err := tx.Where("id=? AND target=? AND kind='image'", id, target).Take(&work).Error; err != nil {
			return ErrLifecycleJournal
		}
		if work.StartedNS != 0 || work.Finished {
			return ErrLifecycleConflict
		}
		var deleted int64
		if err := tx.Model(&lifecycleDeletion{}).Where("incarnation=? AND state!='cancelled'", work.Incarnation).Count(&deleted).Error; err != nil {
			return ErrLifecycleJournal
		}
		if deleted != 0 {
			return ErrAccountAccess
		}
		return tx.Model(&work).Updates(map[string]any{"started_ns": time.Now().UnixNano(), "state": "uploading"}).Error
	})
}
func (j *LifecycleJournal) FinishImageUpload(ctx context.Context, id string, confirmed bool) error {
	var work LifecycleWork
	if err := j.store.DB.WithContext(ctx).Where("id=? AND kind='image'", id).Take(&work).Error; err != nil {
		return ErrLifecycleJournal
	}
	if work.StartedNS == 0 && !confirmed {
		return j.CompleteWork(ctx, id)
	}
	state, code := "review", "upload_uncertain"
	if confirmed && work.StartedNS != 0 {
		state, code = "live", "upload_confirmed"
	}
	return j.store.DB.WithContext(ctx).Model(&work).Updates(map[string]any{"state": state, "code": code, "finished": true, "confirmed": confirmed && work.StartedNS != 0, "next_ns": int64(0), "cleanup_ns": int64(0), "attempts": 0}).Error
}

var traceDirectoryName = regexp.MustCompile(`^generation-([0-9]+)-[0-9]+$`)

func traceTargetShape(root, target string) bool {
	absolute, err := filepath.Abs(root)
	if err != nil || absolute != root {
		return false
	}
	resolved, err := filepath.Abs(target)
	if err != nil || resolved != target || filepath.Dir(target) != root || !traceDirectoryName.MatchString(filepath.Base(target)) {
		return false
	}
	return true
}
func validTraceTarget(root, target string) bool {
	if !traceTargetShape(root, target) {
		return false
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return false
	}
	info, err = os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	for _, name := range []string{"request.json", "response.json"} {
		info, err = os.Lstat(filepath.Join(target, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}
func (j *LifecycleJournal) captureTrace(ctx context.Context, incarnation, root, target string) error {
	if !traceTargetShape(root, target) {
		return ErrLifecycleConflict
	}
	var old LifecycleWork
	err := j.store.DB.WithContext(ctx).Where("kind='trace' AND target=?", target).Take(&old).Error
	if err == nil {
		if old.Root != root {
			return ErrLifecycleConflict
		}
		// The independent registry is authoritative after a snapshot rollback;
		// reusing a numeric generation ID cannot reassign another owner's trace.
		if old.Incarnation != incarnation {
			return ErrLifecycleConflict
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrLifecycleJournal
	}
	id, err := randomLifecycleID()
	if err != nil {
		return err
	}
	return j.reserveLifecycleWork(ctx, LifecycleWork{ID: id, Incarnation: incarnation, Kind: "trace", Target: target, Root: root, State: "live", Finished: true, Confirmed: true}, true)
}

// GuardAccountTrace protects each filesystem write, not the remote request.
// Account deletion can finish while a provider is slow; its late trace writer
// cannot recreate files after the account has gone.
func (j *LifecycleJournal) GuardAccountTrace(ctx context.Context, user int64, root, target string, write func() error) error {
	j.app.AccountWork.RLock()
	defer j.app.AccountWork.RUnlock()
	if !validTraceTarget(root, target) {
		return ErrLifecycleConflict
	}
	var incarnation string
	matches := traceDirectoryName.FindStringSubmatch(filepath.Base(target))
	if len(matches) != 2 {
		return ErrLifecycleConflict
	}
	if err := j.app.SQL.QueryRowContext(ctx, "SELECT a.incarnation FROM account_incarnations a JOIN generations g ON g.user_id=a.user_id WHERE a.user_id=? AND g.id=?", user, matches[1]).Scan(&incarnation); err != nil {
		return ErrAccountAccess
	}
	if err := j.captureTrace(ctx, incarnation, root, target); err != nil {
		return err
	}
	var deleting int64
	if err := j.store.DB.WithContext(ctx).Model(&lifecycleDeletion{}).Where("incarnation=? AND state!='cancelled'", incarnation).Count(&deleting).Error; err != nil {
		return ErrLifecycleJournal
	}
	if deleting != 0 {
		return ErrAccountAccess
	}
	return write()
}
func (j *LifecycleJournal) CaptureAccountTraces(ctx context.Context, tx *gorm.DB, user int64, incarnation, root string) error {
	if root == "" {
		return nil
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return ErrAccountDeletion
	}
	entries, err := os.ReadDir(absolute)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ErrAccountDeletion
	}
	var ids []int64
	if err := tx.Model(&Generation{}).Where("user_id=?", user).Pluck("id", &ids).Error; err != nil {
		return ErrAccountDeletion
	}
	owned := map[string]bool{}
	for _, id := range ids {
		owned[strconv.FormatInt(id, 10)] = true
	}
	for _, entry := range entries {
		matches := traceDirectoryName.FindStringSubmatch(entry.Name())
		if len(matches) != 2 || !owned[matches[1]] {
			continue
		}
		target := filepath.Join(absolute, entry.Name())
		var registered LifecycleWork
		lookup := j.store.DB.WithContext(ctx).Where("kind='trace' AND target=?", target).Take(&registered).Error
		if lookup == nil && registered.Incarnation != incarnation {
			continue
		}
		if lookup != nil && !errors.Is(lookup, gorm.ErrRecordNotFound) {
			return ErrLifecycleJournal
		}
		if err := j.captureTrace(ctx, incarnation, absolute, target); err != nil {
			return err
		}
	}
	return nil
}

// TrackAccountWork binds in-flight provider contexts to an account incarnation.
// Deletion holds AccountWork exclusively while committing and cancelling them.
func (j *LifecycleJournal) TrackAccountWork(ctx context.Context, user int64) (context.Context, func(), error) {
	j.app.AccountWork.RLock()
	defer j.app.AccountWork.RUnlock()
	var incarnation string
	if err := j.app.SQL.QueryRowContext(ctx, "SELECT incarnation FROM account_incarnations WHERE user_id=?", user).Scan(&incarnation); err != nil {
		return nil, nil, ErrAccountAccess
	}
	var deleting int64
	if err := j.store.DB.WithContext(ctx).Model(&lifecycleDeletion{}).Where("incarnation=? AND state!='cancelled'", incarnation).Count(&deleting).Error; err != nil || deleting != 0 {
		return nil, nil, ErrAccountAccess
	}
	id, err := randomLifecycleID()
	if err != nil {
		return nil, nil, err
	}
	tracked, cancel := context.WithCancel(ctx)
	j.workMu.Lock()
	if j.active == nil {
		j.active = map[string]map[string]context.CancelFunc{}
	}
	if j.active[incarnation] == nil {
		j.active[incarnation] = map[string]context.CancelFunc{}
	}
	j.active[incarnation][id] = cancel
	j.workMu.Unlock()
	release := func() {
		cancel()
		j.workMu.Lock()
		defer j.workMu.Unlock()
		delete(j.active[incarnation], id)
		if len(j.active[incarnation]) == 0 {
			delete(j.active, incarnation)
		}
	}
	return tracked, release, nil
}
func (j *LifecycleJournal) CancelAccountWork(incarnation string) {
	j.workMu.Lock()
	defer j.workMu.Unlock()
	for _, cancel := range j.active[incarnation] {
		cancel()
	}
	delete(j.active, incarnation)
}
