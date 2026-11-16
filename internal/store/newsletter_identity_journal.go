package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"time"

	"gorm.io/gorm"
)

const newsletterIdentityJournalSchema = `
DROP TRIGGER lifecycle_journal_binding_immutable;
DROP TRIGGER lifecycle_journal_binding_required;
CREATE TABLE lifecycle_journal_meta_v3(singleton INTEGER PRIMARY KEY CHECK(singleton=1),version INTEGER NOT NULL CHECK(version=3),binding TEXT NOT NULL CHECK(length(binding)=32)) STRICT;
INSERT INTO lifecycle_journal_meta_v3 SELECT singleton,3,binding FROM lifecycle_journal_meta;
DROP TABLE lifecycle_journal_meta;
ALTER TABLE lifecycle_journal_meta_v3 RENAME TO lifecycle_journal_meta;
CREATE TRIGGER lifecycle_journal_binding_immutable BEFORE UPDATE ON lifecycle_journal_meta BEGIN SELECT RAISE(ABORT,'immutable lifecycle binding'); END;
CREATE TRIGGER lifecycle_journal_binding_required BEFORE DELETE ON lifecycle_journal_meta BEGIN SELECT RAISE(ABORT,'required lifecycle binding'); END;
CREATE TABLE newsletter_identity_key(singleton INTEGER PRIMARY KEY CHECK(singleton=1),key_version INTEGER NOT NULL CHECK(key_version=1),key_id TEXT NOT NULL CHECK(length(key_id)=64),high_water INTEGER NOT NULL DEFAULT 0 CHECK(high_water>=0),next_sequence INTEGER NOT NULL DEFAULT 0 CHECK(next_sequence>=0),checkpoint INTEGER NOT NULL DEFAULT 0 CHECK(checkpoint>=0),complete INTEGER NOT NULL DEFAULT 0 CHECK(complete IN (0,1))) STRICT;
CREATE TABLE newsletter_identity_registry(user_id INTEGER PRIMARY KEY CHECK(user_id>0),incarnation TEXT NOT NULL UNIQUE CHECK(length(incarnation)=32),class TEXT NOT NULL CHECK(class IN ('contact','app')),key_version INTEGER,address_tag TEXT,ownership_epoch TEXT,status TEXT NOT NULL CHECK(status IN ('pending','confirmed','unsubscribed')),revision INTEGER NOT NULL CHECK(revision>=0),decision_order INTEGER NOT NULL CHECK(decision_order>=0),notice_version TEXT,decision_unix INTEGER,minimised INTEGER NOT NULL CHECK(minimised IN (0,1)),sequence INTEGER NOT NULL CHECK(sequence>0),CHECK((class='contact' AND key_version=1 AND length(address_tag)=64 AND ownership_epoch IS NULL) OR (class='app' AND key_version IS NULL AND address_tag IS NULL AND length(ownership_epoch)=32 AND minimised=0)),CHECK(minimised=0 OR status!='confirmed')) STRICT;
CREATE TABLE newsletter_identity_intents(operation TEXT PRIMARY KEY NOT NULL CHECK(length(operation)=32),sequence INTEGER NOT NULL UNIQUE CHECK(sequence>0),previous_sequence INTEGER NOT NULL CHECK(previous_sequence>=0),record_json TEXT NOT NULL CHECK(json_valid(record_json)),record_hash TEXT NOT NULL CHECK(length(record_hash)=64)) STRICT;
`

// NewsletterIdentityRecord contains no mailbox, profile, message or proof token.
// Sequence is the expected current journal position, not part of the state hash.
type NewsletterIdentityRecord struct {
	UserID         int64   `json:"userID"`
	Incarnation    string  `json:"incarnation"`
	Class          string  `json:"class"`
	KeyVersion     int     `json:"keyVersion,omitempty"`
	AddressTag     string  `json:"addressTag,omitempty"`
	OwnershipEpoch string  `json:"ownershipEpoch,omitempty"`
	Status         string  `json:"status,omitempty"`
	Revision       int64   `json:"revision,omitempty"`
	DecisionOrder  int64   `json:"decisionOrder,omitempty"`
	NoticeVersion  *string `json:"noticeVersion,omitempty"`
	DecisionUnix   *int64  `json:"decisionUnix,omitempty"`
	Minimised      bool    `json:"minimised,omitempty"`
	Sequence       int64   `json:"-"`
}

type NewsletterIdentityIntent struct {
	Operation        string
	Sequence         int64
	PreviousSequence int64
	Record           NewsletterIdentityRecord
	RecordHash       string
}

type newsletterIdentityJournalKey struct {
	Singleton    int `gorm:"primaryKey"`
	KeyVersion   int
	KeyID        string
	HighWater    int64
	NextSequence int64
	Checkpoint   int64
	Complete     bool
}

func (newsletterIdentityJournalKey) TableName() string { return "newsletter_identity_key" }

type newsletterIdentityIntentRow struct {
	Operation        string `gorm:"primaryKey"`
	Sequence         int64
	PreviousSequence int64
	RecordJSON       string
	RecordHash       string
}

func (newsletterIdentityIntentRow) TableName() string { return "newsletter_identity_intents" }

type newsletterIdentityCommit struct {
	Operation   string `gorm:"primaryKey"`
	Sequence    int64
	UserID      int64
	Incarnation string
	RecordHash  string
	Disposition string
}

func (newsletterIdentityCommit) TableName() string { return "newsletter_identity_commits" }

var newsletterNoticeVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,79}$`)

func newsletterRecordPayload(record NewsletterIdentityRecord) (string, string, error) {
	if record.UserID <= 0 || !opaqueLifecycleID(record.Incarnation) || record.Sequence < 0 || record.Revision < 0 || record.DecisionOrder < 0 || record.DecisionUnix != nil && *record.DecisionUnix < 0 || record.NoticeVersion != nil && !newsletterNoticeVersion.MatchString(*record.NoticeVersion) {
		return "", "", ErrLifecycleConflict
	}
	switch record.Class {
	case "contact":
		if record.KeyVersion != 1 || !validNewsletterDigest(record.AddressTag) || record.OwnershipEpoch != "" || record.Minimised && record.Status == "confirmed" {
			return "", "", ErrLifecycleConflict
		}
	case "app":
		if record.KeyVersion != 0 || record.AddressTag != "" || !opaqueLifecycleID(record.OwnershipEpoch) || record.Minimised {
			return "", "", ErrLifecycleConflict
		}
	case "retired":
		if record.KeyVersion != 0 || record.AddressTag != "" || record.OwnershipEpoch != "" || record.Status != "" || record.Revision != 0 || record.DecisionOrder != 0 || record.NoticeVersion != nil || record.DecisionUnix != nil || record.Minimised {
			return "", "", ErrLifecycleConflict
		}
	default:
		return "", "", ErrLifecycleConflict
	}
	if record.Class != "retired" && record.Status != "pending" && record.Status != "confirmed" && record.Status != "unsubscribed" {
		return "", "", ErrLifecycleConflict
	}
	body, err := json.Marshal(record)
	if err != nil {
		return "", "", ErrLifecycleConflict
	}
	hash := sha256.Sum256(body)
	return string(body), hex.EncodeToString(hash[:]), nil
}

func validNewsletterDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == value
}

func migrateNewsletterIdentityJournal(ctx context.Context, external *Store) error {
	return external.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var version int
		if err := tx.Raw("SELECT version FROM lifecycle_journal_meta WHERE singleton=1").Row().Scan(&version); err != nil {
			return ErrLifecycleJournal
		}
		if version == 3 {
			return nil
		}
		if version != 2 {
			return ErrLifecycleJournal
		}
		return tx.Exec(newsletterIdentityJournalSchema).Error
	})
}

func (j *LifecycleJournal) InitializeNewsletterIdentity(ctx context.Context, key *NewsletterIdentityKey) error {
	j.app.AccountWork.Lock()
	defer j.app.AccountWork.Unlock()
	if err := j.CheckAvailable(ctx); err != nil || !key.valid() {
		return ErrLifecycleJournal
	}
	var existing newsletterIdentityJournalKey
	err := j.store.DB.WithContext(ctx).Take(&existing, "singleton=1").Error
	known := err == nil
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrLifecycleJournal
	}
	if known && (existing.KeyVersion != key.Version() || existing.KeyID != key.ID()) {
		return ErrNewsletterIdentityUnavailable
	}
	if !known {
		var orphaned int64
		if err := j.store.DB.WithContext(ctx).Raw("SELECT (SELECT count(*) FROM newsletter_identity_registry)+(SELECT count(*) FROM newsletter_identity_intents)").Scan(&orphaned).Error; err != nil || orphaned != 0 {
			return ErrLifecycleUncertain
		}
		if err := j.app.DB.WithContext(ctx).Raw("SELECT (SELECT count(*) FROM users WHERE newsletter_minimised=1)+(SELECT count(*) FROM newsletter_identity_matches)").Scan(&orphaned).Error; err != nil || orphaned != 0 {
			return ErrLifecycleUncertain
		}
	}
	var binding NewsletterIdentityNamespace
	err = j.app.DB.WithContext(ctx).Take(&binding, "singleton=1").Error
	if err == nil && binding.JournalBound && !known {
		return ErrLifecycleUncertain
	}
	if errors.Is(err, gorm.ErrRecordNotFound) && known {
		// Explicit restore may recover a missing SQL binding from the already
		// authenticated current journal; runtime key installation cannot do so.
		err = j.app.DB.WithContext(ctx).Create(&NewsletterIdentityNamespace{Singleton: 1, KeyVersion: key.Version(), KeyID: key.ID(), LegacyVacuumPending: true}).Error
	} else if errors.Is(err, gorm.ErrRecordNotFound) {
		err = j.app.PrepareNewsletterIdentityNamespace(ctx, key)
	} else if err == nil && (binding.KeyVersion != key.Version() || binding.KeyID != key.ID()) {
		return ErrNewsletterIdentityUnavailable
	}
	if err != nil {
		return err
	}
	if !known {
		var floor int64
		if err := j.app.SQL.QueryRowContext(ctx, "SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name='users'),0)").Scan(&floor); err != nil {
			return err
		}
		if err := j.store.DB.WithContext(ctx).Create(&newsletterIdentityJournalKey{Singleton: 1, KeyVersion: key.Version(), KeyID: key.ID(), HighWater: floor}).Error; err != nil {
			return err
		}
	}
	if err := j.app.DB.WithContext(ctx).Model(&NewsletterIdentityNamespace{}).Where("singleton=1").Update("journal_bound", true).Error; err != nil {
		return err
	}
	return j.app.ConfigureNewsletterIdentity(ctx, key)
}

func (j *LifecycleJournal) newsletterIdentityBinding(ctx context.Context) (newsletterIdentityJournalKey, bool, error) {
	if err := j.CheckAvailable(ctx); err != nil {
		return newsletterIdentityJournalKey{}, false, err
	}
	var binding newsletterIdentityJournalKey
	err := j.store.DB.WithContext(ctx).Take(&binding, "singleton=1").Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if j.app.newsletterJournalRequired.Load() {
			return binding, false, ErrLifecycleUncertain
		}
		var orphaned int64
		if err := j.store.DB.WithContext(ctx).Raw("SELECT (SELECT count(*) FROM newsletter_identity_registry)+(SELECT count(*) FROM newsletter_identity_intents)").Scan(&orphaned).Error; err != nil || orphaned != 0 {
			return binding, false, ErrLifecycleJournal
		}
		return binding, false, nil
	}
	if err != nil {
		return binding, false, ErrLifecycleJournal
	}
	key := j.app.newsletterIdentity.Load()
	if !key.valid() || key.ID() != binding.KeyID || key.Version() != binding.KeyVersion {
		return binding, true, ErrNewsletterIdentityUnavailable
	}
	var saved NewsletterIdentityNamespace
	if err := j.app.DB.WithContext(ctx).Take(&saved, "singleton=1").Error; err != nil || saved.KeyID != key.ID() || saved.KeyVersion != key.Version() {
		return binding, true, ErrNewsletterIdentityUnavailable
	}
	return binding, true, nil
}

func (j *LifecycleJournal) ReserveNewsletterUserID(ctx context.Context, floor int64) (int64, error) {
	if _, bound, err := j.newsletterIdentityBinding(ctx); err != nil || !bound || floor < 0 || floor == math.MaxInt64 {
		return 0, ErrNewsletterIdentityUnavailable
	}
	var next int64
	err := j.store.DB.WithContext(ctx).Raw("UPDATE newsletter_identity_key SET high_water=MAX(high_water,?)+1 WHERE singleton=1 AND high_water<? RETURNING high_water", floor, int64(math.MaxInt64)).Row().Scan(&next)
	if err != nil || next <= floor {
		return 0, ErrLifecycleJournal
	}
	return next, nil
}

func (j *LifecycleJournal) NewsletterIdentity(ctx context.Context, userID int64) (NewsletterIdentityRecord, bool, error) {
	if _, bound, err := j.newsletterIdentityBinding(ctx); err != nil || !bound {
		return NewsletterIdentityRecord{}, false, err
	}
	return readNewsletterIdentity(j.store.DB.WithContext(ctx), userID)
}

func readNewsletterIdentity(tx *gorm.DB, userID int64) (NewsletterIdentityRecord, bool, error) {
	var record NewsletterIdentityRecord
	err := tx.Table("newsletter_identity_registry").Take(&record, "user_id=?", userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return record, false, nil
	}
	if err != nil {
		return record, false, ErrLifecycleJournal
	}
	if _, _, err := newsletterRecordPayload(record); err != nil || record.Class == "retired" {
		return record, false, ErrLifecycleJournal
	}
	return record, true, nil
}

func (j *LifecycleJournal) CompleteNewsletterIdentityBootstrap(ctx context.Context) error {
	j.app.AccountWork.Lock()
	defer j.app.AccountWork.Unlock()
	if _, bound, err := j.newsletterIdentityBinding(ctx); err != nil || !bound {
		return ErrNewsletterIdentityUnavailable
	}
	var pending int64
	if err := j.store.DB.WithContext(ctx).Table("newsletter_identity_intents").Count(&pending).Error; err != nil || pending != 0 {
		return ErrLifecycleUncertain
	}
	var contacts []User
	if err := j.app.DB.WithContext(ctx).Where("isNewsletterOnly=1").Find(&contacts).Error; err != nil {
		return err
	}
	for _, contact := range contacts {
		record, found, err := j.NewsletterIdentity(ctx, contact.ID)
		if err != nil || !found || record.Class != "contact" {
			return ErrLifecycleUncertain
		}
		if err := j.validateNewsletterIdentityPostState(ctx, j.app.DB.WithContext(ctx), record); err != nil {
			return err
		}
	}
	var floor int64
	if err := j.app.SQL.QueryRowContext(ctx, "SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name='users'),0)").Scan(&floor); err != nil {
		return err
	}
	return j.store.DB.WithContext(ctx).Exec("UPDATE newsletter_identity_key SET complete=1,high_water=MAX(high_water,?) WHERE singleton=1", floor).Error
}

func decodeNewsletterIntent(row newsletterIdentityIntentRow) (NewsletterIdentityIntent, error) {
	var record NewsletterIdentityRecord
	if err := json.Unmarshal([]byte(row.RecordJSON), &record); err != nil {
		return NewsletterIdentityIntent{}, ErrLifecycleJournal
	}
	body, hash, err := newsletterRecordPayload(record)
	if err != nil || body != row.RecordJSON || hash != row.RecordHash || !opaqueLifecycleID(row.Operation) || row.Sequence <= 0 || row.PreviousSequence < 0 {
		return NewsletterIdentityIntent{}, ErrLifecycleJournal
	}
	record.Sequence = row.PreviousSequence
	return NewsletterIdentityIntent{Operation: row.Operation, Sequence: row.Sequence, PreviousSequence: row.PreviousSequence, Record: record, RecordHash: hash}, nil
}

func (j *LifecycleJournal) PrepareNewsletterIdentity(ctx context.Context, operation string, record NewsletterIdentityRecord) (NewsletterIdentityIntent, error) {
	if _, bound, err := j.newsletterIdentityBinding(ctx); err != nil || !bound || !opaqueLifecycleID(operation) {
		return NewsletterIdentityIntent{}, ErrNewsletterIdentityUnavailable
	}
	body, hash, err := newsletterRecordPayload(record)
	if err != nil {
		return NewsletterIdentityIntent{}, err
	}
	var prepared newsletterIdentityIntentRow
	peek := j.store.DB.WithContext(ctx).Take(&prepared, "operation=?", operation).Error
	if peek == nil {
		if prepared.RecordHash != hash || prepared.PreviousSequence != record.Sequence {
			return NewsletterIdentityIntent{}, ErrLifecycleConflict
		}
		return decodeNewsletterIntent(prepared)
	}
	if !errors.Is(peek, gorm.ErrRecordNotFound) {
		return NewsletterIdentityIntent{}, peek
	}
	if _, exists, err := j.newsletterIdentityFate(ctx, operation); err != nil || exists {
		return NewsletterIdentityIntent{}, ErrLifecycleConflict
	}
	var result NewsletterIdentityIntent
	err = j.store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var old newsletterIdentityIntentRow
		if err := tx.Take(&old, "operation=?", operation).Error; err == nil {
			if old.RecordHash != hash || old.PreviousSequence != record.Sequence {
				return ErrLifecycleConflict
			}
			var err error
			result, err = decodeNewsletterIntent(old)
			return err
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var pending int64
		if err := tx.Raw("SELECT (SELECT count(*) FROM newsletter_identity_intents)+(SELECT count(*) FROM lifecycle_deletions WHERE state='prepared')").Scan(&pending).Error; err != nil || pending != 0 {
			return ErrLifecycleUncertain
		}
		current, found, err := readNewsletterIdentity(tx, record.UserID)
		if err != nil {
			return err
		}
		if found && (current.Incarnation != record.Incarnation || current.Sequence != record.Sequence || current.Class == "app" && record.Class == "contact" || current.Class == "contact" && record.Class == "contact" && (current.KeyVersion != record.KeyVersion || current.AddressTag != record.AddressTag) || record.Class != "retired" && (record.Revision < current.Revision || record.DecisionOrder < current.DecisionOrder)) || !found && record.Sequence != 0 {
			return ErrLifecycleConflict
		}
		var sequence int64
		if err := tx.Raw("UPDATE newsletter_identity_key SET next_sequence=next_sequence+1,high_water=MAX(high_water,?) WHERE singleton=1 AND next_sequence<? RETURNING next_sequence", record.UserID, int64(math.MaxInt64)).Row().Scan(&sequence); err != nil {
			return err
		}
		row := newsletterIdentityIntentRow{Operation: operation, Sequence: sequence, PreviousSequence: record.Sequence, RecordJSON: body, RecordHash: hash}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		result, err = decodeNewsletterIntent(row)
		return err
	})
	return result, err
}

func (j *LifecycleJournal) validateNewsletterIdentityPostState(ctx context.Context, tx *gorm.DB, record NewsletterIdentityRecord) error {
	tx = tx.WithContext(ctx).Session(&gorm.Session{NewDB: true})
	var user User
	err := tx.First(&user, record.UserID).Error
	if record.Class == "retired" {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrLifecycleConflict
		}
		var deleted int64
		if err := tx.Raw("SELECT count(*) FROM lifecycle_operations WHERE incarnation=?", record.Incarnation).Scan(&deleted).Error; err != nil || deleted != 1 {
			return ErrLifecycleUncertain
		}
		return nil
	}
	if err != nil {
		return ErrLifecycleConflict
	}
	var incarnation string
	if err := tx.Raw("SELECT incarnation FROM account_incarnations WHERE user_id=?", record.UserID).Row().Scan(&incarnation); err != nil || incarnation != record.Incarnation {
		return ErrLifecycleConflict
	}
	var matches []NewsletterIdentityMatch
	if err := tx.Where("user_id=?", record.UserID).Find(&matches).Error; err != nil {
		return err
	}
	if record.Class == "contact" {
		if !user.IsNewsletterOnly || user.NewsletterMinimised != record.Minimised || user.Password != "" || user.EmailVerifiedAt != nil || user.EmailChangedAt != nil || user.NewsletterOwnershipEpoch != nil || user.FirstName != "" || user.LastName != "" || user.PreferencesJSON != nil || user.Sex != nil || user.Birthdate != nil || user.Allergens != nil || user.Diet != nil || user.Dislikes != nil || user.LastMeal != nil || user.CookingEfforts != nil || user.CookingTechnique != nil || user.HasPref != nil && *user.HasPref {
			return ErrLifecycleConflict
		}
		if record.Minimised {
			if user.Email != "" || user.EmailKey != "" || user.Username != "" || len(matches) != 1 || matches[0].Incarnation != record.Incarnation || matches[0].KeyVersion != record.KeyVersion || matches[0].AddressTag != record.AddressTag {
				return ErrLifecycleConflict
			}
		} else {
			key := j.app.newsletterIdentity.Load()
			tag, err := key.Tag(user.Email)
			if err != nil || tag != record.AddressTag || len(matches) != 0 {
				return ErrLifecycleConflict
			}
		}
	} else if user.IsNewsletterOnly || user.NewsletterMinimised || user.NewsletterOwnershipEpoch == nil || *user.NewsletterOwnershipEpoch != record.OwnershipEpoch || len(matches) != 0 {
		return ErrLifecycleConflict
	}
	var sub struct {
		Status         string
		Revision       int64
		ProvedOrder    int64
		ConsentVersion string
		RequestedUnix  *int64
		ConfirmedUnix  *int64
		WithdrawnUnix  *int64
		DecisionUnix   *int64
	}
	err = tx.Table("newsletter_subscriptions").Take(&sub, "user_id=?", record.UserID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) && record.Status == "pending" && record.Revision == 0 && record.DecisionOrder == 0 && record.NoticeVersion == nil && record.DecisionUnix == nil {
		return nil
	}
	if err != nil || sub.Status != record.Status || sub.Revision != record.Revision || sub.ProvedOrder != record.DecisionOrder || record.NoticeVersion == nil && sub.ConsentVersion != "" || record.NoticeVersion != nil && *record.NoticeVersion != sub.ConsentVersion {
		return ErrLifecycleConflict
	}
	date := sub.RequestedUnix
	if user.NewsletterMinimised || date == nil && sub.Status == "pending" {
		date = sub.DecisionUnix
	} else {
		switch sub.Status {
		case "confirmed":
			date = sub.ConfirmedUnix
		case "unsubscribed":
			date = sub.WithdrawnUnix
		}
	}
	if date == nil != (record.DecisionUnix == nil) || date != nil && *date != *record.DecisionUnix {
		return ErrLifecycleConflict
	}
	return nil
}

func (j *LifecycleJournal) MarkNewsletterIdentityCommit(ctx context.Context, tx *gorm.DB, intent NewsletterIdentityIntent) error {
	if err := j.CheckAvailable(ctx); err != nil || tx == nil {
		return ErrLifecycleJournal
	}
	var saved newsletterIdentityIntentRow
	if err := j.store.DB.WithContext(ctx).Take(&saved, "operation=?", intent.Operation).Error; err != nil {
		return ErrLifecycleUncertain
	}
	body, hash, err := newsletterRecordPayload(intent.Record)
	if err != nil || hash != saved.RecordHash || body != saved.RecordJSON || intent.RecordHash != hash || intent.Sequence != saved.Sequence || intent.PreviousSequence != saved.PreviousSequence {
		return ErrLifecycleConflict
	}
	if err := j.validateNewsletterIdentityPostState(ctx, tx, intent.Record); err != nil {
		return err
	}
	marker := newsletterIdentityCommit{Operation: intent.Operation, Sequence: intent.Sequence, UserID: intent.Record.UserID, Incarnation: intent.Record.Incarnation, RecordHash: intent.RecordHash, Disposition: "committed"}
	if err := tx.WithContext(ctx).Session(&gorm.Session{NewDB: true}).Create(&marker).Error; err != nil {
		return err
	}
	return tx.WithContext(ctx).Session(&gorm.Session{NewDB: true}).Exec("UPDATE newsletter_identity_namespace SET journal_checkpoint=MAX(journal_checkpoint,?) WHERE singleton=1", intent.Sequence).Error
}

func (j *LifecycleJournal) newsletterIdentityFate(ctx context.Context, operation string) (newsletterIdentityCommit, bool, error) {
	var marker newsletterIdentityCommit
	err := j.app.DB.WithContext(ctx).Take(&marker, "operation=?", operation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return marker, false, nil
	}
	if err != nil {
		return marker, false, err
	}
	return marker, true, nil
}

func (j *LifecycleJournal) finishNewsletterIdentity(ctx context.Context, operation, disposition string) error {
	binding, bound, err := j.newsletterIdentityBinding(ctx)
	if err != nil || !bound {
		return ErrNewsletterIdentityUnavailable
	}
	marker, exists, err := j.newsletterIdentityFate(ctx, operation)
	if err != nil || !exists || marker.Disposition != disposition {
		return ErrLifecycleUncertain
	}
	return j.store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row newsletterIdentityIntentRow
		err := tx.Take(&row, "operation=?", operation).Error
		if errors.Is(err, gorm.ErrRecordNotFound) && marker.Sequence <= binding.Checkpoint {
			return nil // Acknowledgment only; never reapplies an old state.
		}
		if err != nil {
			return ErrLifecycleUncertain
		}
		intent, err := decodeNewsletterIntent(row)
		if err != nil || intent.Sequence <= binding.Checkpoint || marker.Sequence != intent.Sequence || marker.UserID != intent.Record.UserID || marker.Incarnation != intent.Record.Incarnation || marker.RecordHash != intent.RecordHash {
			return ErrLifecycleConflict
		}
		current, found, err := readNewsletterIdentity(tx, intent.Record.UserID)
		if err != nil || found && (current.Sequence != intent.PreviousSequence || current.Incarnation != intent.Record.Incarnation) || !found && intent.PreviousSequence != 0 {
			return ErrLifecycleConflict
		}
		if disposition == "committed" {
			if intent.Record.Class == "retired" {
				if err := tx.Exec("DELETE FROM newsletter_identity_registry WHERE user_id=? AND incarnation=?", intent.Record.UserID, intent.Record.Incarnation).Error; err != nil {
					return err
				}
			} else if err := saveNewsletterIdentity(tx, intent.Record, intent.Sequence); err != nil {
				return err
			}
		}
		if err := tx.Exec("UPDATE newsletter_identity_key SET checkpoint=MAX(checkpoint,?),high_water=MAX(high_water,?) WHERE singleton=1", intent.Sequence, intent.Record.UserID).Error; err != nil {
			return err
		}
		return tx.Delete(&newsletterIdentityIntentRow{}, "operation=?", operation).Error
	})
}

func nullableNewsletterText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func saveNewsletterIdentity(tx *gorm.DB, r NewsletterIdentityRecord, sequence int64) error {
	var version any
	if r.KeyVersion != 0 {
		version = r.KeyVersion
	}
	return tx.Exec(`INSERT INTO newsletter_identity_registry(user_id,incarnation,class,key_version,address_tag,ownership_epoch,status,revision,decision_order,notice_version,decision_unix,minimised,sequence)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(user_id) DO UPDATE SET incarnation=excluded.incarnation,class=excluded.class,key_version=excluded.key_version,address_tag=excluded.address_tag,ownership_epoch=excluded.ownership_epoch,status=excluded.status,revision=excluded.revision,decision_order=excluded.decision_order,notice_version=excluded.notice_version,decision_unix=excluded.decision_unix,minimised=excluded.minimised,sequence=excluded.sequence`,
		r.UserID, r.Incarnation, r.Class, version, nullableNewsletterText(r.AddressTag), nullableNewsletterText(r.OwnershipEpoch), r.Status, r.Revision, r.DecisionOrder, r.NoticeVersion, r.DecisionUnix, r.Minimised, sequence).Error
}

func (j *LifecycleJournal) CommitNewsletterIdentity(ctx context.Context, operation string) error {
	return j.finishNewsletterIdentity(ctx, operation, "committed")
}

// CancelNewsletterIdentity is for the owner of a known rolled-back SQL
// transaction. Startup reconciliation never guesses cancellation from absence.
func (j *LifecycleJournal) CancelNewsletterIdentity(ctx context.Context, operation string) error {
	if _, bound, err := j.newsletterIdentityBinding(ctx); err != nil || !bound {
		return ErrNewsletterIdentityUnavailable
	}
	var row newsletterIdentityIntentRow
	if err := j.store.DB.WithContext(ctx).Take(&row, "operation=?", operation).Error; err != nil {
		return ErrLifecycleUncertain
	}
	intent, err := decodeNewsletterIntent(row)
	if err != nil {
		return err
	}
	if marker, exists, err := j.newsletterIdentityFate(ctx, operation); err != nil || exists {
		if err == nil && marker.Disposition == "cancelled" {
			return j.finishNewsletterIdentity(ctx, operation, "cancelled")
		}
		return ErrLifecycleUncertain
	}
	if err := j.app.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		marker := newsletterIdentityCommit{Operation: operation, Sequence: intent.Sequence, UserID: intent.Record.UserID, Incarnation: intent.Record.Incarnation, RecordHash: intent.RecordHash, Disposition: "cancelled"}
		if err := tx.Create(&marker).Error; err != nil {
			return err
		}
		return tx.Exec("UPDATE newsletter_identity_namespace SET journal_checkpoint=MAX(journal_checkpoint,?) WHERE singleton=1", intent.Sequence).Error
	}); err != nil {
		return err
	}
	return j.finishNewsletterIdentity(ctx, operation, "cancelled")
}

func (j *LifecycleJournal) ReconcileNewsletterIdentityIntents(ctx context.Context) error {
	j.app.AccountWork.Lock()
	defer j.app.AccountWork.Unlock()
	binding, bound, err := j.newsletterIdentityBinding(ctx)
	if err != nil || !bound {
		if err == nil {
			return nil
		}
		return err
	}
	var rows []newsletterIdentityIntentRow
	if err := j.store.DB.WithContext(ctx).Order("sequence").Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		marker, found, err := j.newsletterIdentityFate(ctx, row.Operation)
		if err != nil || !found {
			return ErrLifecycleUncertain
		}
		if err := j.finishNewsletterIdentity(ctx, row.Operation, marker.Disposition); err != nil {
			return err
		}
	}
	if err := j.store.DB.WithContext(ctx).Take(&binding, "singleton=1").Error; err != nil {
		return err
	}
	var unknown int64
	if err := j.app.DB.WithContext(ctx).Model(&newsletterIdentityCommit{}).Where("sequence>?", binding.Checkpoint).Count(&unknown).Error; err != nil || unknown != 0 {
		return ErrLifecycleUncertain
	}
	var applied int64
	if err := j.app.SQL.QueryRowContext(ctx, "SELECT journal_checkpoint FROM newsletter_identity_namespace WHERE singleton=1").Scan(&applied); err != nil || applied > binding.Checkpoint {
		return ErrLifecycleUncertain
	}
	return nil
}

func (j *LifecycleJournal) CompactNewsletterIdentityCheckpoints(ctx context.Context) error {
	j.app.AccountWork.Lock()
	defer j.app.AccountWork.Unlock()
	binding, bound, err := j.newsletterIdentityBinding(ctx)
	if err != nil || !bound || !binding.Complete {
		return ErrNewsletterIdentityUnavailable
	}
	if err := j.CheckReady(ctx); err != nil {
		return err
	}
	return j.app.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE newsletter_identity_namespace SET journal_checkpoint=MAX(journal_checkpoint,?) WHERE singleton=1", binding.Checkpoint).Error; err != nil {
			return err
		}
		return tx.Where("sequence<=?", binding.Checkpoint).Delete(&newsletterIdentityCommit{}).Error
	})
}

// The ordinary readiness path must not acquire the application SQL pool: it can
// be called while the caller owns that pool's transaction. Pair/snapshot checks
// run separately under exclusive ownership before serving or restoring data.
func (j *LifecycleJournal) checkNewsletterIdentityJournalReady(ctx context.Context) error {
	var binding newsletterIdentityJournalKey
	err := j.store.DB.WithContext(ctx).Take(&binding, "singleton=1").Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if j.app.newsletterJournalRequired.Load() {
			return ErrLifecycleUncertain
		}
		var orphaned int64
		if err := j.store.DB.WithContext(ctx).Raw("SELECT (SELECT count(*) FROM newsletter_identity_registry)+(SELECT count(*) FROM newsletter_identity_intents)").Scan(&orphaned).Error; err != nil || orphaned != 0 {
			return ErrLifecycleJournal
		}
		return nil
	}
	key := j.app.newsletterIdentity.Load()
	if err != nil || !key.valid() || binding.KeyID != key.ID() || binding.KeyVersion != key.Version() || binding.Checkpoint > binding.NextSequence {
		return ErrLifecycleJournal
	}
	var pending int64
	if err := j.store.DB.WithContext(ctx).Table("newsletter_identity_intents").Count(&pending).Error; err != nil {
		return err
	}
	if !binding.Complete || pending != 0 {
		return ErrLifecycleUncertain
	}
	return nil
}

// TransitionNewsletterIdentity is called with the exclusive account-work fence
// already held. A failed SQL commit remains uncertain; only a proved rollback
// permits cancellation of the prepared journal operation.
func (j *LifecycleJournal) TransitionNewsletterIdentity(ctx context.Context, record NewsletterIdentityRecord, apply func(*gorm.DB) error) error {
	operation, err := randomLifecycleID()
	if err != nil {
		return err
	}
	intent, err := j.PrepareNewsletterIdentity(ctx, operation, record)
	if err != nil {
		return err
	}
	tx := j.app.DB.WithContext(ctx).Begin()
	if tx.Error != nil {
		return tx.Error
	}
	if err = apply(tx); err == nil {
		err = j.MarkNewsletterIdentityCommit(ctx, tx, intent)
	}
	if err != nil {
		if tx.Rollback().Error != nil {
			return ErrLifecycleUncertain
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if j.CancelNewsletterIdentity(cleanup, operation) != nil {
			return ErrLifecycleUncertain
		}
		return err
	}
	if err := tx.Commit().Error; err != nil {
		return ErrLifecycleUncertain
	}
	return j.CommitNewsletterIdentity(ctx, operation)
}
