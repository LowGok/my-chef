package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrLifecycleJournal   = errors.New("account lifecycle journal unavailable")
	ErrLifecycleConflict  = errors.New("account lifecycle operation conflicts")
	ErrLifecycleUncertain = errors.New("account deletion intent needs reconciliation")
)

// LifecycleJournal is independent of application snapshots. Startup opens an
// existing binding; only an explicit migration may initialize it.
type LifecycleJournal struct {
	identity  os.FileInfo
	binding   string
	cleanupMu sync.Mutex
	workMu    sync.Mutex
	active    map[string]map[string]context.CancelFunc
	store     *Store
	app       *Store
	Path      string
}

type lifecycleDeletion struct {
	Operation   string `gorm:"primaryKey"`
	Incarnation string
	State       string
	CreatedNS   int64
}

func (lifecycleDeletion) TableName() string { return "lifecycle_deletions" }

type LifecycleWork struct {
	DeliveryType string
	Purpose      string
	Provider     string
	CleanupNS    int64
	Root         string
	StartedNS    int64
	Finished     bool
	Confirmed    bool
	ID           string `gorm:"primaryKey"`
	Incarnation  string
	Kind         string
	Target       string
	State        string
	Attempts     int
	NextNS       int64
	Code         string
}

func (LifecycleWork) TableName() string { return "lifecycle_work" }

const lifecycleJournalSchema = `
CREATE TABLE lifecycle_journal_meta(singleton INTEGER PRIMARY KEY CHECK(singleton=1), version INTEGER NOT NULL CHECK(version=1), binding TEXT NOT NULL CHECK(length(binding)=32)) STRICT;
CREATE TRIGGER lifecycle_journal_binding_immutable BEFORE UPDATE ON lifecycle_journal_meta BEGIN SELECT RAISE(ABORT,'immutable lifecycle binding'); END;
CREATE TRIGGER lifecycle_journal_binding_required BEFORE DELETE ON lifecycle_journal_meta BEGIN SELECT RAISE(ABORT,'required lifecycle binding'); END;
CREATE TABLE lifecycle_deletions(operation TEXT PRIMARY KEY NOT NULL CHECK(length(operation)=32),incarnation TEXT NOT NULL CHECK(length(incarnation)=32),state TEXT NOT NULL CHECK(state IN ('prepared','committed','cancelled')),created_ns INTEGER NOT NULL) STRICT;
CREATE UNIQUE INDEX lifecycle_active_incarnation ON lifecycle_deletions(incarnation) WHERE state!='cancelled';
CREATE TRIGGER lifecycle_marker_immutable BEFORE UPDATE ON lifecycle_deletions WHEN OLD.state='committed' BEGIN SELECT RAISE(ABORT,'immutable deletion marker'); END;
CREATE TRIGGER lifecycle_marker_required BEFORE DELETE ON lifecycle_deletions BEGIN SELECT RAISE(ABORT,'required deletion record'); END;
CREATE TABLE lifecycle_work(id TEXT PRIMARY KEY NOT NULL CHECK(length(id)=32),incarnation TEXT NOT NULL CHECK(length(incarnation)=32),kind TEXT NOT NULL CHECK(kind IN ('image','trace')),target TEXT NOT NULL CHECK(length(target) BETWEEN 1 AND 4096),state TEXT NOT NULL CHECK(state IN ('reserved','uploading','live','deleting','review')),attempts INTEGER NOT NULL DEFAULT 0,next_ns INTEGER NOT NULL DEFAULT 0,code TEXT NOT NULL DEFAULT '',root TEXT NOT NULL DEFAULT '',started_ns INTEGER NOT NULL DEFAULT 0,finished INTEGER NOT NULL DEFAULT 0,confirmed INTEGER NOT NULL DEFAULT 0,provider TEXT NOT NULL DEFAULT 'fixture',cleanup_ns INTEGER NOT NULL DEFAULT 0,UNIQUE(kind,provider,target)) STRICT;
CREATE INDEX lifecycle_work_owner ON lifecycle_work(incarnation,state);
`

func opaqueLifecycleID(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16 && hex.EncodeToString(decoded) == value
}

func (s *Store) InitializeLifecycleJournal(ctx context.Context, path string) (*LifecycleJournal, error) {
	return s.lifecycleJournal(ctx, path, true, false, false)
}
func (s *Store) OpenLifecycleJournal(ctx context.Context, path string) (*LifecycleJournal, error) {
	return s.lifecycleJournal(ctx, path, false, false, false)
}
func (s *Store) OpenLifecycleJournalReadOnly(ctx context.Context, path string) (*LifecycleJournal, error) {
	return s.lifecycleJournal(ctx, path, false, true, false)
}
func validateLifecycleJournal(ctx context.Context, external *Store, binding string, allowLegacy bool) error {
	var saved string
	var version int
	if err := external.SQL.QueryRowContext(ctx, "SELECT version,binding FROM lifecycle_journal_meta WHERE singleton=1").Scan(&version, &saved); err != nil || version != 2 && (!allowLegacy || version != 1) || saved != binding {
		return ErrLifecycleJournal
	}
	rows, err := external.SQL.QueryContext(ctx, "SELECT id,incarnation,kind,target,root,provider,started_ns,finished,confirmed,cleanup_ns FROM lifecycle_work LIMIT 0")
	if err != nil {
		return ErrLifecycleJournal
	}
	_ = rows.Close()
	if version == 2 {
		rows, err := external.SQL.QueryContext(ctx, "SELECT delivery_type,purpose FROM lifecycle_work LIMIT 0")
		if err != nil {
			return ErrLifecycleJournal
		}
		_ = rows.Close()
	}
	if err := external.validateIntegrity(ctx); err != nil {
		return ErrLifecycleJournal
	}
	return nil
}
func (s *Store) lifecycleJournal(ctx context.Context, path string, initialize, readOnly, priorSnapshot bool) (*LifecycleJournal, error) {
	if err := s.checkSchema(ctx, priorSnapshot); err != nil {
		return nil, ErrLifecycleJournal
	}
	absolute, err := filepath.Abs(path)
	if err != nil || absolute == s.Path {
		return nil, ErrLifecycleJournal
	}
	var binding, stage string
	if err = s.SQL.QueryRowContext(ctx, "SELECT binding,stage FROM lifecycle_binding WHERE singleton=1").Scan(&binding, &stage); err != nil || !opaqueLifecycleID(binding) {
		return nil, ErrLifecycleJournal
	}
	if !initialize && stage != "ready" {
		return nil, ErrLifecycleJournal
	}
	info, statErr := os.Lstat(absolute)
	fresh := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !fresh {
		return nil, ErrLifecycleJournal
	}
	if fresh && (!initialize || stage != "new") {
		return nil, ErrLifecycleJournal
	}
	var external *Store
	if !fresh {
		appInfo, appErr := os.Stat(s.Path)
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || appErr != nil || os.SameFile(info, appInfo) {
			return nil, ErrLifecycleJournal
		}
		// Inspect an existing candidate read-only before changing its journal mode
		// or pragmas. A wrong file must remain byte-identical.
		probe, e := OpenReadOnly(absolute)
		if e != nil {
			return nil, ErrLifecycleJournal
		}
		if e = validateLifecycleJournal(ctx, probe, binding, initialize); e != nil {
			_ = probe.Close()
			return nil, e
		}
		if readOnly {
			external = probe
		} else {
			_ = probe.Close()
			external, err = Open(absolute, false)
		}
	} else {
		result, e := s.SQL.ExecContext(ctx, "UPDATE lifecycle_binding SET stage='prepared' WHERE singleton=1 AND stage='new'")
		if e != nil {
			return nil, ErrLifecycleJournal
		}
		affected, e := result.RowsAffected()
		if e != nil || affected != 1 {
			return nil, ErrLifecycleJournal
		}
		stage = "prepared"
		file, e := os.OpenFile(absolute, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return nil, ErrLifecycleJournal
		}
		if e = file.Close(); e != nil {
			return nil, ErrLifecycleJournal
		}
		external, err = Open(absolute, false)
	}
	if err != nil || external == nil {
		return nil, ErrLifecycleJournal
	}
	ok := false
	defer func() {
		if !ok {
			_ = external.Close()
		}
	}()
	if !readOnly {
		if _, err = external.SQL.ExecContext(ctx, "PRAGMA synchronous=FULL; PRAGMA secure_delete=ON"); err != nil {
			return nil, ErrLifecycleJournal
		}
	}
	if fresh {
		if err = external.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec(lifecycleJournalSchema).Error; err != nil {
				return err
			}
			return tx.Exec("INSERT INTO lifecycle_journal_meta VALUES(1,1,?)", binding).Error
		}); err != nil {
			return nil, ErrLifecycleJournal
		}
	}
	if initialize {
		if err = migratePersonalPhotoJournal(ctx, external); err != nil {
			return nil, ErrLifecycleJournal
		}
	}
	if err = validateLifecycleJournal(ctx, external, binding, false); err != nil {
		return nil, err
	}
	if fresh {
		directory, err := os.Open(filepath.Dir(absolute))
		if err != nil {
			return nil, ErrLifecycleJournal
		}
		err = directory.Sync()
		_ = directory.Close()
		if err != nil {
			return nil, ErrLifecycleJournal
		}
	}
	if initialize && stage != "ready" {
		if stage == "new" {
			if _, err = s.SQL.ExecContext(ctx, "UPDATE lifecycle_binding SET stage='prepared' WHERE singleton=1 AND stage='new'"); err != nil {
				return nil, ErrLifecycleJournal
			}
		}
		if _, err = s.SQL.ExecContext(ctx, "UPDATE lifecycle_binding SET stage='ready' WHERE singleton=1 AND stage='prepared'"); err != nil {
			return nil, ErrLifecycleJournal
		}
	}
	identity, err := os.Lstat(absolute)
	if err != nil {
		return nil, ErrLifecycleJournal
	}
	ok = true
	return &LifecycleJournal{store: external, app: s, Path: absolute, identity: identity, binding: binding}, nil
}
func (j *LifecycleJournal) CheckAvailable(ctx context.Context) error {
	current, err := os.Lstat(j.Path)
	if err != nil || !current.Mode().IsRegular() || current.Mode().Perm()&0077 != 0 || j.identity == nil || !os.SameFile(current, j.identity) {
		return ErrLifecycleJournal
	}
	var binding string
	if err := j.store.SQL.QueryRowContext(ctx, "SELECT binding FROM lifecycle_journal_meta WHERE singleton=1").Scan(&binding); err != nil || binding != j.binding {
		return ErrLifecycleJournal
	}
	return nil
}
func (j *LifecycleJournal) Close() error {
	j.workMu.Lock()
	var cancellations []context.CancelFunc
	for _, group := range j.active {
		for _, cancel := range group {
			cancellations = append(cancellations, cancel)
		}
	}
	j.active = nil
	j.workMu.Unlock()
	for _, cancel := range cancellations {
		cancel()
	}
	return j.store.Close()
}

func (j *LifecycleJournal) PrepareDeletion(ctx context.Context, operation, incarnation string) error {
	if err := j.CheckAvailable(ctx); err != nil {
		return err
	}
	if !opaqueLifecycleID(operation) || !opaqueLifecycleID(incarnation) {
		return ErrLifecycleConflict
	}
	return j.store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var old lifecycleDeletion
		err := tx.Where("operation = ?", operation).Take(&old).Error
		if err == nil {
			if old.Incarnation != incarnation || old.State == "cancelled" {
				return ErrLifecycleConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrLifecycleJournal
		}
		row := lifecycleDeletion{Operation: operation, Incarnation: incarnation, State: "prepared", CreatedNS: time.Now().UnixNano()}
		if err = tx.Create(&row).Error; err != nil {
			return ErrLifecycleConflict
		}
		return nil
	})
}

func (j *LifecycleJournal) deletionProof(ctx context.Context, operation, incarnation string) (bool, error) {
	var saved string
	err := j.app.SQL.QueryRowContext(ctx, "SELECT incarnation FROM lifecycle_operations WHERE operation=?", operation).Scan(&saved)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, ErrLifecycleJournal
	}
	return saved == incarnation, nil
}
func (j *LifecycleJournal) finishDeletion(ctx context.Context, operation, state string) error {
	if err := j.CheckAvailable(ctx); err != nil {
		return err
	}
	var row lifecycleDeletion
	if err := j.store.DB.WithContext(ctx).Where("operation = ?", operation).Take(&row).Error; err != nil {
		return ErrLifecycleJournal
	}
	if row.State == state {
		return nil
	}
	if row.State != "prepared" {
		return ErrLifecycleConflict
	}
	proof, err := j.deletionProof(ctx, operation, row.Incarnation)
	if err != nil {
		return err
	}
	if state == "committed" && !proof || state == "cancelled" && proof {
		return ErrLifecycleUncertain
	}
	result := j.store.DB.WithContext(ctx).Model(&lifecycleDeletion{}).Where("operation = ? AND state = 'prepared'", operation).Update("state", state)
	if result.Error != nil {
		return ErrLifecycleJournal
	}
	if result.RowsAffected != 1 {
		return ErrLifecycleConflict
	}
	return nil
}
func (j *LifecycleJournal) CommitDeletion(ctx context.Context, operation string) error {
	return j.finishDeletion(ctx, operation, "committed")
}
func (j *LifecycleJournal) CancelDeletion(ctx context.Context, operation string) error {
	return j.finishDeletion(ctx, operation, "cancelled")
}
func (j *LifecycleJournal) ReconcileIntents(ctx context.Context) error {
	var rows []lifecycleDeletion
	if err := j.store.DB.WithContext(ctx).Where("state = 'prepared'").Find(&rows).Error; err != nil {
		return ErrLifecycleJournal
	}
	for _, row := range rows {
		proof, err := j.deletionProof(ctx, row.Operation, row.Incarnation)
		if err != nil {
			return err
		}
		if !proof {
			return ErrLifecycleUncertain
		}
		if err = j.CommitDeletion(ctx, row.Operation); err != nil {
			return err
		}
	}
	return nil
}
func (j *LifecycleJournal) IsDeleted(ctx context.Context, incarnation string) (bool, error) {
	var count int64
	if err := j.store.DB.WithContext(ctx).Model(&lifecycleDeletion{}).Where("incarnation = ? AND state = 'committed'", incarnation).Count(&count).Error; err != nil {
		return false, ErrLifecycleJournal
	}
	return count != 0, nil
}

func (j *LifecycleJournal) ReserveWork(ctx context.Context, id, incarnation, kind, target string) error {
	if kind != "image" {
		return ErrLifecycleConflict
	}
	return j.reserveLifecycleWork(ctx, LifecycleWork{ID: id, Incarnation: incarnation, Kind: kind, Target: target, Provider: "fixture", State: "reserved"})
}
func (j *LifecycleJournal) reserveLifecycleWork(ctx context.Context, work LifecycleWork, replay ...bool) error {
	if work.DeliveryType == "" {
		work.DeliveryType = "upload"
	}
	if work.Purpose == "" {
		work.Purpose = "account"
	}
	if err := j.CheckAvailable(ctx); err != nil {
		return err
	}
	if !opaqueLifecycleID(work.ID) || !opaqueLifecycleID(work.Incarnation) || (work.Kind != "image" && work.Kind != "trace") || len(work.Target) == 0 || len(work.Target) > 4096 {
		return ErrLifecycleConflict
	}
	if work.Kind == "trace" && !traceTargetShape(work.Root, work.Target) {
		return ErrLifecycleConflict
	}
	if work.DeliveryType != "upload" && work.DeliveryType != "authenticated" || work.Purpose != "account" && work.Purpose != "personal" || work.Purpose == "personal" && (work.Kind != "image" || work.DeliveryType != "authenticated" || !validPersonalTarget(work.Target)) {
		return ErrLifecycleConflict
	}
	return j.store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var deleted int64
		if err := tx.Model(&lifecycleDeletion{}).Where("incarnation=? AND state!='cancelled'", work.Incarnation).Count(&deleted).Error; err != nil {
			return ErrLifecycleJournal
		}
		if deleted != 0 {
			var committed int64
			if len(replay) == 0 || !replay[0] {
				return ErrAccountAccess
			}
			if err := tx.Model(&lifecycleDeletion{}).Where("incarnation=? AND state='committed'", work.Incarnation).Count(&committed).Error; err != nil || committed != 1 {
				return ErrAccountAccess
			}
		}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&work)
		if result.Error != nil {
			return ErrLifecycleJournal
		}
		if result.RowsAffected == 1 {
			return nil
		}
		var old LifecycleWork
		if err := tx.Where("id=?", work.ID).Take(&old).Error; err != nil {
			return ErrLifecycleConflict
		}
		if old.Incarnation != work.Incarnation || old.Kind != work.Kind || old.Target != work.Target || old.Root != work.Root || old.Provider != work.Provider || old.DeliveryType != work.DeliveryType || old.Purpose != work.Purpose {
			return ErrLifecycleConflict
		}
		return nil
	})
}

func (j *LifecycleJournal) WorkFor(ctx context.Context, incarnation string) ([]LifecycleWork, error) {
	var work []LifecycleWork
	if err := j.store.DB.WithContext(ctx).Where("incarnation = ?", incarnation).Order("id").Find(&work).Error; err != nil {
		return nil, ErrLifecycleJournal
	}
	return work, nil
}
func (j *LifecycleJournal) CompleteWork(ctx context.Context, id string) error {
	var work LifecycleWork
	if err := j.store.DB.WithContext(ctx).Where("id=?", id).Take(&work).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return ErrLifecycleJournal
	}
	if work.Purpose == "personal" {
		if err := j.app.DB.WithContext(ctx).Where(`target=? AND binding=? AND delivery_type=? AND NOT (`+personalPhotoReferences+`)`, work.Target, work.Provider, work.DeliveryType).Delete(&PersonalPhotoAsset{}).Error; err != nil {
			return ErrLifecycleJournal
		}
	}
	if err := j.store.DB.WithContext(ctx).Where("id = ?", id).Delete(&LifecycleWork{}).Error; err != nil {
		return ErrLifecycleJournal
	}
	var busy, log, done int
	if err := j.store.SQL.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &done); err != nil || busy != 0 {
		return ErrLifecycleJournal
	}
	return nil
}
