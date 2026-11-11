package imports

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ncruces/go-sqlite3/gormlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const admissionSchema = `
CREATE TABLE import_admission_meta(singleton INTEGER PRIMARY KEY CHECK(singleton=1),version INTEGER NOT NULL CHECK(version=1),id TEXT NOT NULL CHECK(length(id)=32),key_hash TEXT NOT NULL CHECK(length(key_hash)=64),created_ns INTEGER NOT NULL CHECK(created_ns>0),high_ns INTEGER NOT NULL CHECK(high_ns>=created_ns)) STRICT;
CREATE TRIGGER import_admission_meta_immutable BEFORE UPDATE ON import_admission_meta WHEN NEW.version!=OLD.version OR NEW.id!=OLD.id OR NEW.key_hash!=OLD.key_hash OR NEW.created_ns!=OLD.created_ns OR NEW.high_ns<OLD.high_ns BEGIN SELECT RAISE(ABORT,'immutable admission identity'); END;
CREATE TRIGGER import_admission_meta_required BEFORE DELETE ON import_admission_meta BEGIN SELECT RAISE(ABORT,'required admission identity'); END;
CREATE TABLE import_admissions(operation_key TEXT PRIMARY KEY NOT NULL CHECK(length(operation_key)=64),kind TEXT NOT NULL CHECK(kind IN ('extract','fetch')),account_hash TEXT NOT NULL CHECK(length(account_hash)=64),source_hash TEXT NOT NULL CHECK(length(source_hash)=64),request_hash TEXT NOT NULL CHECK(length(request_hash)=64),receipt_hash TEXT NOT NULL CHECK(length(receipt_hash)=64),tokens INTEGER NOT NULL CHECK(tokens>=0),state TEXT NOT NULL CHECK(state IN ('intent','completed','failed','ambiguous')),created_ns INTEGER NOT NULL CHECK(created_ns>0),finished_ns INTEGER NOT NULL DEFAULT 0) STRICT;
CREATE INDEX import_admission_window ON import_admissions(kind,created_ns);
CREATE INDEX import_admission_account ON import_admissions(kind,account_hash,created_ns);
CREATE INDEX import_admission_source ON import_admissions(kind,source_hash,created_ns);
`

// AdmissionLedger is a worker-owned database, never an application snapshot.
// One worker owns it at a time; its concurrent handlers reserve atomically.
// Opening never creates a missing ledger or resets usage.
type AdmissionLedger struct {
	mu                sync.Mutex
	db                *gorm.DB
	sql               *sql.DB
	lock              *os.File
	path, id, keyHash string
	identity          os.FileInfo
	vault             *DraftVault
	closed            bool
}

func (*AdmissionLedger) String() string   { return "private import admission ledger" }
func (*AdmissionLedger) GoString() string { return "private import admission ledger" }

type AdmissionRequest struct{ OperationID, AccountID, SourceGroup, RequestHash string }
type AdmissionReservation struct {
	Dispatch           bool
	Outcome            string
	operation, receipt string
	request            AdmissionRequest
}

func (AdmissionReservation) String() string   { return "private import reservation" }
func (AdmissionReservation) GoString() string { return "private import reservation" }

type admissionRecord struct {
	OperationKey                                                   string `gorm:"primaryKey"`
	Kind, AccountHash, SourceHash, RequestHash, ReceiptHash, State string
	Tokens, CreatedNS, FinishedNS                                  int64
}

func (admissionRecord) TableName() string { return "import_admissions" }

func admissionHex(value string, bytes int) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == bytes && hex.EncodeToString(decoded) == value
}
func privateAdmissionParent(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Lstat(filepath.Dir(path))
	return err == nil && info.IsDir() && info.Mode().Perm() == 0700
}
func admissionConnection(path string) (*gorm.DB, *sql.DB, error) {
	address := url.URL{Scheme: "file", Path: path}
	query := url.Values{"mode": {"rw"}, "_pragma": {"busy_timeout(5000)", "foreign_keys(1)", "journal_mode(WAL)", "synchronous(FULL)"}, "_txlock": {"immediate"}}
	address.RawQuery = query.Encode()
	db, err := gorm.Open(gormlite.Open(address.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, nil, ErrExtractionUnavailable
	}
	pool, err := db.DB()
	if err != nil {
		return nil, nil, ErrExtractionUnavailable
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	return db, pool, nil
}
func admissionLock(path string) (*os.File, error) {
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, ErrExtractionUnavailable
	}
	info, err := lock.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		_ = lock.Close()
		return nil, ErrExtractionUnavailable
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, ErrExtractionUnavailable
	}
	return lock, nil
}

// InitializeAdmissionLedger is an explicit operator action. An existing file,
// including an uncertain/partial initialization, cannot be replaced by it.
func InitializeAdmissionLedger(path string, key []byte, now time.Time) (string, error) {
	if !privateAdmissionParent(path) || now.IsZero() || now.UnixNano() <= 0 {
		return "", ErrExtractionUnavailable
	}
	if _, err := NewDraftVault(key); err != nil {
		return "", ErrExtractionUnavailable
	}
	lock, err := admissionLock(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = lock.Close() }()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return "", ErrExtractionUnavailable
	}
	if err := file.Close(); err != nil {
		return "", ErrExtractionUnavailable
	}
	_, pool, err := admissionConnection(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = pool.Close() }()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", ErrExtractionUnavailable
	}
	id := hex.EncodeToString(random[:])
	keySum := sha256.Sum256(key)
	tx, err := pool.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return "", ErrExtractionUnavailable
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(admissionSchema); err != nil {
		return "", ErrExtractionUnavailable
	}
	if _, err := tx.Exec("INSERT INTO import_admission_meta VALUES(1,1,?,?,?,?)", id, hex.EncodeToString(keySum[:]), now.UnixNano(), now.UnixNano()); err != nil {
		return "", ErrExtractionUnavailable
	}
	if err := tx.Commit(); err != nil {
		return "", ErrExtractionUnavailable
	}
	return id, nil
}
func OpenAdmissionLedger(path string, key []byte, id string, now time.Time) (ledger *AdmissionLedger, err error) {
	if !privateAdmissionParent(path) || !admissionHex(id, 16) || now.IsZero() || now.UnixNano() <= 0 {
		return nil, ErrExtractionUnavailable
	}
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, ErrExtractionUnavailable
	}
	vault, e := NewDraftVault(key)
	if e != nil {
		return nil, ErrExtractionUnavailable
	}
	lock, e := admissionLock(path)
	if e != nil {
		return nil, e
	}
	defer func() {
		if err != nil {
			_ = lock.Close()
		}
	}()
	db, pool, e := admissionConnection(path)
	if e != nil {
		return nil, e
	}
	defer func() {
		if err != nil {
			_ = pool.Close()
		}
	}()
	keySum := sha256.Sum256(key)
	ledger = &AdmissionLedger{db: db, sql: pool, lock: lock, path: path, id: id, keyHash: hex.EncodeToString(keySum[:]), identity: info, vault: vault}
	err = db.Transaction(func(tx *gorm.DB) error {
		if e := ledger.check(tx, now); e != nil {
			return e
		}
		// The exclusive process lock proves no earlier worker is still dispatching.
		// Release orphaned concurrency slots, but keep every call/token reservation.
		if e := tx.Model(&admissionRecord{}).Where("state='intent'").Updates(map[string]any{"state": "ambiguous", "finished_ns": now.UnixNano()}).Error; e != nil {
			return ErrExtractionUnavailable
		}
		return ledger.advance(tx, now)
	})
	if err != nil {
		return nil, ErrExtractionUnavailable
	}
	return ledger, nil
}
func (l *AdmissionLedger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	a := l.sql.Close()
	b := l.lock.Close()
	l.vault = nil
	if a != nil || b != nil {
		return ErrExtractionUnavailable
	}
	return nil
}
func (l *AdmissionLedger) check(tx *gorm.DB, now time.Time) error {
	if l.closed || now.IsZero() || now.UnixNano() <= 0 {
		return ErrExtractionUnavailable
	}
	info, err := os.Lstat(l.path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !os.SameFile(info, l.identity) || !privateAdmissionParent(l.path) {
		return ErrExtractionUnavailable
	}
	var version int
	var id, keyHash string
	var high int64
	if err := tx.Raw("SELECT version,id,key_hash,high_ns FROM import_admission_meta WHERE singleton=1").Row().Scan(&version, &id, &keyHash, &high); err != nil || version != 1 || id != l.id || keyHash != l.keyHash || high > now.UnixNano() {
		return ErrExtractionUnavailable
	}
	return nil
}
func (l *AdmissionLedger) advance(tx *gorm.DB, now time.Time) error {
	if err := tx.Exec("UPDATE import_admission_meta SET high_ns=? WHERE singleton=1", now.UnixNano()).Error; err != nil {
		return ErrExtractionUnavailable
	}
	return nil
}

type admissionCaps struct{ minute, day, account, source, tokens, concurrent, reserve int64 }

func (l *AdmissionLedger) Admit(ctx context.Context, policy ExtractionPolicy, binding ExtractionBinding, request AdmissionRequest, now time.Time) (AdmissionReservation, error) {
	if err := policy.Check(now, binding); err != nil {
		return AdmissionReservation{}, err
	}
	limits := policy.Limits
	keySum := sha256.Sum256([]byte(binding.APIKey))
	provider := binding.ProjectID + ":" + binding.Model + ":" + hex.EncodeToString(keySum[:])
	return l.admit(ctx, "extract", provider, request, now, admissionCaps{minute: limits.FleetCallsPerMinute, day: limits.FleetCallsPerDay, account: limits.AccountCallsPerDay, source: limits.SourceCallsPerDay, tokens: limits.TokensPerMinute, concurrent: limits.MaxConcurrent, reserve: limits.MaxInputTokens + limits.MaxOutputTokens + limits.MaxThinkingTokens})
}

// AdmitFetch records possible dispatch before the public fetch too. It needs no
// Gemini eligibility and cannot use or reset the inference budget. Fixed local
// ceilings bound this separate non-provider work, including after app restore.
func (l *AdmissionLedger) AdmitFetch(ctx context.Context, request AdmissionRequest, now time.Time) (AdmissionReservation, error) {
	return l.admit(ctx, "fetch", "public-https-v1", request, now, admissionCaps{minute: 30, day: 500, account: 20, source: 100, concurrent: 2})
}
func (l *AdmissionLedger) admit(ctx context.Context, kind, provider string, request AdmissionRequest, now time.Time, caps admissionCaps) (AdmissionReservation, error) {
	if l == nil || !admissionHex(request.OperationID, 16) || !admissionHex(request.AccountID, 16) || !admissionHex(request.RequestHash, 32) || len(request.SourceGroup) == 0 || len(request.SourceGroup) > 256 || !utf8.ValidString(request.SourceGroup) || strings.IndexFunc(request.SourceGroup, unicode.IsControl) >= 0 {
		return AdmissionReservation{}, ErrExtractionUnavailable
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return AdmissionReservation{}, ErrExtractionUnavailable
	}
	var out AdmissionReservation
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := l.check(tx, now); err != nil {
			return err
		}
		operation := l.vault.Fingerprint("admission-operation:"+kind, []byte(request.OperationID))
		account := l.vault.Fingerprint("admission-account", []byte(request.AccountID))
		source := l.vault.Fingerprint("admission-source:"+kind, []byte(strings.TrimSuffix(strings.ToLower(request.SourceGroup), ".")))
		encoded, err := json.Marshal([]string{account, source, request.RequestHash, provider})
		if err != nil {
			return ErrExtractionUnavailable
		}
		hash := l.vault.Fingerprint("admission-request:"+kind, encoded)
		clear(encoded)
		var prior admissionRecord
		err = tx.Where("operation_key=?", operation).Take(&prior).Error
		if err == nil {
			if prior.RequestHash != hash || prior.AccountHash != account || prior.SourceHash != source || prior.Kind != kind {
				return ErrExtractionUnavailable
			}
			out = AdmissionReservation{Dispatch: false, Outcome: prior.State, operation: operation, request: request}
			return l.advance(tx, now)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrExtractionUnavailable
		}
		var minuteCalls, minuteTokens, dayCalls, accountCalls, sourceCalls, active int64
		if err := tx.Raw("SELECT COUNT(*),COALESCE(SUM(tokens),0) FROM import_admissions WHERE kind=? AND created_ns>?", kind, now.Add(-time.Minute).UnixNano()).Row().Scan(&minuteCalls, &minuteTokens); err != nil {
			return ErrExtractionUnavailable
		}
		for _, check := range []struct {
			query  string
			args   []any
			target *int64
		}{
			{"kind=? AND created_ns>?", []any{kind, now.Add(-24 * time.Hour).UnixNano()}, &dayCalls},
			{"kind=? AND account_hash=? AND created_ns>?", []any{kind, account, now.Add(-24 * time.Hour).UnixNano()}, &accountCalls},
			{"kind=? AND source_hash=? AND created_ns>?", []any{kind, source, now.Add(-24 * time.Hour).UnixNano()}, &sourceCalls},
			{"kind=? AND state='intent'", []any{kind}, &active},
		} {
			if err := tx.Model(&admissionRecord{}).Where(check.query, check.args...).Count(check.target).Error; err != nil {
				return ErrExtractionUnavailable
			}
		}
		if minuteCalls >= caps.minute || dayCalls >= caps.day || accountCalls >= caps.account || sourceCalls >= caps.source || active >= caps.concurrent || caps.reserve > 0 && minuteTokens > caps.tokens-caps.reserve {
			return ErrExtractionUnavailable
		}
		var secret [32]byte
		if _, err := rand.Read(secret[:]); err != nil {
			return ErrExtractionUnavailable
		}
		receipt := hex.EncodeToString(secret[:])
		receiptSum := sha256.Sum256([]byte(receipt))
		record := admissionRecord{OperationKey: operation, Kind: kind, AccountHash: account, SourceHash: source, RequestHash: hash, ReceiptHash: hex.EncodeToString(receiptSum[:]), Tokens: caps.reserve, State: "intent", CreatedNS: now.UnixNano()}
		if err := tx.Create(&record).Error; err != nil {
			return ErrExtractionUnavailable
		}
		if err := l.advance(tx, now); err != nil {
			return err
		}
		out = AdmissionReservation{Dispatch: true, Outcome: "intent", operation: operation, receipt: receipt, request: request}
		return nil
	})
	if err != nil {
		return AdmissionReservation{}, ErrExtractionUnavailable
	}
	return out, nil
}
func (l *AdmissionLedger) Finish(ctx context.Context, reservation AdmissionReservation, outcome string, now time.Time) error {
	if l == nil || !reservation.Dispatch || !admissionHex(reservation.operation, 32) || !admissionHex(reservation.receipt, 32) || (outcome != "completed" && outcome != "failed" && outcome != "ambiguous") {
		return ErrExtractionUnavailable
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := l.check(tx, now); err != nil {
			return err
		}
		var row admissionRecord
		if err := tx.Where("operation_key=?", reservation.operation).Take(&row).Error; err != nil {
			return ErrExtractionUnavailable
		}
		receiptSum := sha256.Sum256([]byte(reservation.receipt))
		if subtle.ConstantTimeCompare([]byte(row.ReceiptHash), []byte(hex.EncodeToString(receiptSum[:]))) != 1 {
			return ErrExtractionUnavailable
		}
		if row.State != "intent" {
			return nil
		}
		if err := tx.Model(&row).Updates(map[string]any{"state": outcome, "finished_ns": now.UnixNano()}).Error; err != nil {
			return ErrExtractionUnavailable
		}
		return l.advance(tx, now)
	})
}

func (l *AdmissionLedger) CheckAvailable(ctx context.Context, now time.Time) error {
	if l == nil {
		return ErrExtractionUnavailable
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return ErrExtractionUnavailable
	}
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return l.check(tx, now) })
}
