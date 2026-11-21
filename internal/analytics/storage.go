package analytics

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	"gorm.io/gorm"
)

const privacySchema = `PRAGMA application_id=1296253264;
CREATE TABLE privacy_meta(singleton INTEGER PRIMARY KEY CHECK(singleton=1),version INTEGER NOT NULL CHECK(version=2),binding TEXT NOT NULL,generation INTEGER NOT NULL,maintenance INTEGER NOT NULL) STRICT;
CREATE TABLE account_controls(incarnation TEXT PRIMARY KEY,revision INTEGER NOT NULL CHECK(revision>0),objection INTEGER NOT NULL CHECK(objection IN(0,1)),changed INTEGER NOT NULL) STRICT;
CREATE TABLE links(subject TEXT PRIMARY KEY,incarnation TEXT NOT NULL,epoch TEXT NOT NULL,state TEXT NOT NULL CHECK(state IN('prepared','active','revoked')),created INTEGER NOT NULL,revoked INTEGER,cleanup TEXT NOT NULL DEFAULT 'none') STRICT;
CREATE INDEX links_incarnation ON links(incarnation);
CREATE TABLE copies(path TEXT PRIMARY KEY,kind TEXT NOT NULL CHECK(kind IN('journal','staging','extract','backup')),reporting INTEGER NOT NULL,generation INTEGER NOT NULL,revision INTEGER NOT NULL DEFAULT 0,created INTEGER NOT NULL,deadline INTEGER NOT NULL DEFAULT 0,state TEXT NOT NULL CHECK(state IN('building','ready','quarantined','deleted'))) STRICT;
CREATE TABLE backup_subjects(path TEXT NOT NULL REFERENCES copies(path),subject TEXT NOT NULL,PRIMARY KEY(path,subject)) STRICT;`
const journalSchema = `PRAGMA application_id=1296253263;
CREATE TABLE journal_meta(singleton INTEGER PRIMARY KEY CHECK(singleton=1),version INTEGER NOT NULL CHECK(version=2),environment TEXT NOT NULL CHECK(environment='production'),binding TEXT NOT NULL,checksum TEXT NOT NULL,maintenance INTEGER NOT NULL,generation INTEGER NOT NULL,sequence INTEGER NOT NULL DEFAULT 0) STRICT;
CREATE TABLE processing_periods(id TEXT PRIMARY KEY,subject TEXT NOT NULL,start INTEGER NOT NULL,end INTEGER,basis TEXT NOT NULL,basis_version TEXT NOT NULL,notice TEXT NOT NULL,enrollment_source TEXT NOT NULL,revision INTEGER NOT NULL) STRICT;
CREATE TABLE facts(id TEXT PRIMARY KEY,subject TEXT NOT NULL,epoch TEXT NOT NULL,occurred INTEGER NOT NULL,name TEXT NOT NULL,platform TEXT NOT NULL,plan TEXT NOT NULL,sequence INTEGER NOT NULL,digest TEXT NOT NULL) STRICT;
CREATE INDEX facts_time ON facts(occurred);
CREATE TABLE delivery_receipts(id TEXT PRIMARY KEY,digest TEXT NOT NULL,occurred INTEGER NOT NULL) STRICT;
CREATE TABLE contributions(subject TEXT NOT NULL,month TEXT NOT NULL,metric TEXT NOT NULL,numerator INTEGER NOT NULL,denominator INTEGER NOT NULL,provenance TEXT NOT NULL,coverage TEXT NOT NULL,observation_status TEXT NOT NULL,PRIMARY KEY(subject,month,metric)) STRICT;
CREATE TABLE source_checkpoints(epoch TEXT PRIMARY KEY,sequence INTEGER NOT NULL,contiguous INTEGER NOT NULL CHECK(contiguous IN(0,1))) STRICT;
CREATE TABLE delivery_batches(id TEXT PRIMARY KEY,created INTEGER NOT NULL,status TEXT NOT NULL,accepted INTEGER NOT NULL DEFAULT 0) STRICT;
CREATE TABLE coverage(start INTEGER NOT NULL,end INTEGER NOT NULL,status TEXT NOT NULL,source TEXT NOT NULL,PRIMARY KEY(start,source)) STRICT;`

type OpenOptions struct {
	BackupKey                     []byte
	Root, PrivacyPath, AnchorPath string
	Create                        bool
	Config                        Config
	Now                           func() time.Time
}
type Service struct {
	unhealthy, degraded                    atomic.Bool
	lastHeartbeat                          int64
	appDirty                               atomic.Bool
	lockFile                               *os.File
	backupKey                              []byte
	app                                    *gorm.DB
	journal, privacy                       *sql.DB
	root, privacyPath, anchorPath, binding string
	config                                 Config
	now                                    func() time.Time
	mu                                     privacyMutex
	closed                                 bool
}
type anchor struct {
	ControlDigest string `json:"controlDigest"`
	Binding       string `json:"binding"`
	Generation    int64  `json:"generation"`
	Maintenance   int64  `json:"maintenance"`
}

func privatePath(path string, directory bool) error {
	a, e := filepath.Abs(path)
	if e != nil || a != filepath.Clean(path) {
		return ErrPrivacy
	}
	for p := a; ; p = filepath.Dir(p) {
		i, e := os.Lstat(p)
		if e != nil {
			return ErrPrivacy
		}
		if i.Mode()&os.ModeSymlink != 0 {
			return ErrPrivacy
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	i, e := os.Stat(a)
	if e != nil || i.IsDir() != directory || i.Mode().Perm()&0077 != 0 {
		return ErrPrivacy
	}
	return nil
}
func openSQL(path string, create bool) (*sql.DB, error) {
	if create {
		f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if e != nil {
			return nil, ErrPrivacy
		}
		if e = f.Close(); e != nil {
			return nil, e
		}
	}
	if e := privatePath(path, false); e != nil {
		return nil, e
	}
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{"mode": {"rw"}, "_pragma": {"foreign_keys(1)", "busy_timeout(5000)", "synchronous(FULL)", "secure_delete(1)"}, "_txlock": {"immediate"}}
	u.RawQuery = q.Encode()
	db, e := sql.Open("sqlite3", u.String())
	if e != nil {
		return nil, ErrPrivacy
	}
	db.SetMaxOpenConns(1)
	if e = db.Ping(); e != nil {
		_ = db.Close()
		return nil, ErrPrivacy
	}
	return db, nil
}
func Open(ctx context.Context, app *gorm.DB, o OpenOptions) (s *Service, err error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if !filepath.IsAbs(o.Root) || !filepath.IsAbs(o.PrivacyPath) || !filepath.IsAbs(o.AnchorPath) {
		return nil, ErrPrivacy
	}
	// Privacy controls and the high-water anchor are deliberately outside restorable data.
	for _, p := range []string{o.PrivacyPath, o.AnchorPath} {
		r, e := filepath.Rel(o.Root, p)
		if e != nil || r == "." || len(r) < 3 || r[:3] != "../" {
			return nil, ErrPrivacy
		}
	}
	if o.PrivacyPath == o.AnchorPath {
		return nil, ErrPrivacy
	}
	if o.Create {
		if e := os.Mkdir(o.Root, 0700); e != nil {
			return nil, ErrPrivacy
		}
	}
	if e := privatePath(o.Root, true); e != nil {
		return nil, e
	}
	for _, p := range []string{o.PrivacyPath, o.AnchorPath} {
		if e := privatePath(filepath.Dir(p), true); e != nil {
			return nil, e
		}
	}
	s = &Service{backupKey: append([]byte(nil), o.BackupKey...), app: app, root: o.Root, privacyPath: o.PrivacyPath, anchorPath: o.AnchorPath, config: o.Config, now: o.Now}
	if err = app.WithContext(ctx).Raw("SELECT binding FROM lifecycle_binding WHERE singleton=1").Row().Scan(&s.binding); err != nil {
		return nil, ErrPrivacy
	}
	defer func(opened *Service) {
		if err != nil {
			_ = opened.Close()
		}
	}(s)
	lockPath := o.PrivacyPath + ".lock"
	if _, e := os.Lstat(lockPath); e == nil {
		if e = privatePath(lockPath, false); e != nil {
			return nil, e
		}
	} else if !os.IsNotExist(e) {
		return nil, ErrPrivacy
	}
	s.lockFile, err = os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, ErrPrivacy
	}
	if err = flockContext(ctx, s.lockFile); err != nil {
		return nil, ErrPrivacy
	}
	if s.privacy, err = openSQL(o.PrivacyPath, o.Create); err != nil {
		return nil, err
	}
	if o.Create {
		if _, err = s.privacy.ExecContext(ctx, privacySchema); err != nil {
			return nil, ErrPrivacy
		}
		if _, err = s.privacy.ExecContext(ctx, "INSERT INTO privacy_meta VALUES(1,2,?,0,?)", s.binding, s.now().Unix()); err != nil {
			return nil, ErrPrivacy
		}
		if err = s.writeAnchor(ctx); err != nil {
			return nil, err
		}
		// Register the journal destination before its first byte is written.
		if _, err = s.privacy.ExecContext(ctx, "INSERT INTO copies(path,kind,reporting,generation,created,state) VALUES(?,'journal',0,0,?,'building')", filepath.Join(o.Root, "journal.sqlite"), s.now().Unix()); err != nil {
			return nil, err
		}
	}
	if err = s.checkAnchor(ctx); err != nil {
		return nil, err
	}
	if s.journal, err = openSQL(filepath.Join(o.Root, "journal.sqlite"), o.Create); err != nil {
		return nil, err
	}
	if o.Create {
		if _, err = s.journal.ExecContext(ctx, journalSchema); err != nil {
			return nil, err
		}
		if _, err = s.journal.ExecContext(ctx, "INSERT INTO journal_meta(singleton,version,environment,binding,checksum,maintenance,generation) VALUES(1,2,'production',?,?,?,0)", s.binding, Digest([]byte(journalSchema)), s.now().Unix()); err != nil {
			return nil, err
		}
		_, err = s.privacy.ExecContext(ctx, "UPDATE copies SET state='ready' WHERE path=?", filepath.Join(o.Root, "journal.sqlite"))
		if err != nil {
			return nil, err
		}
	}
	var v, id int
	var binding, checksum, environment string
	if err = s.journal.QueryRowContext(ctx, "PRAGMA application_id").Scan(&id); err != nil || id != 1296253263 {
		return nil, ErrPrivacy
	}
	if err = s.journal.QueryRowContext(ctx, "SELECT version,environment,binding,checksum FROM journal_meta WHERE singleton=1").Scan(&v, &environment, &binding, &checksum); err != nil || v != 2 || environment != "production" || binding != s.binding || checksum != Digest([]byte(journalSchema)) {
		return nil, ErrPrivacy
	}
	if err = s.validateSchema(ctx, s.privacy, privacySchema); err != nil {
		return nil, err
	}
	if err = s.validateSchema(ctx, s.journal, journalSchema); err != nil {
		return nil, err
	}
	if err = s.checkInventory(ctx); err != nil {
		return nil, err
	}
	if err = s.reconcilePrepared(ctx); err != nil {
		return nil, err
	}
	s.appDirty.Store(true)
	if err = app.WithContext(ctx).Exec("PRAGMA secure_delete=ON").Error; err != nil {
		return nil, err
	}
	if o.Create {
		if err = app.WithContext(ctx).Exec("UPDATE analytics_installation SET required=1 WHERE singleton=1").Error; err != nil {
			return nil, err
		}
	}
	var paused bool
	if err = app.WithContext(ctx).Raw("SELECT restore_paused FROM analytics_installation WHERE singleton=1").Row().Scan(&paused); err != nil {
		return nil, ErrPrivacy
	}
	if paused {
		if err = s.ReconcileAppRestore(ctx); err != nil {
			return nil, err
		}
	}
	s.mu.file = s.lockFile
	if err = syscall.Flock(int(s.lockFile.Fd()), syscall.LOCK_UN); err != nil {
		return nil, ErrPrivacy
	}
	return s, nil
}
func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	if e := s.mu.LockContext(context.Background()); e != nil {
		return e
	}
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.unhealthy.Store(true)
	var e, error2 error
	if s.journal != nil {
		e = s.journal.Close()
	}
	if s.privacy != nil {
		error2 = s.privacy.Close()
	}
	if s.lockFile != nil {
		error2 = errors.Join(error2, s.lockFile.Close())
	}
	clear(s.backupKey)
	return errors.Join(e, error2)
}
func (s *Service) readAnchor(ctx context.Context) (anchor, error) {
	var a anchor
	var version int
	e := s.privacy.QueryRowContext(ctx, "SELECT version,binding,generation,maintenance FROM privacy_meta WHERE singleton=1").Scan(&version, &a.Binding, &a.Generation, &a.Maintenance)
	if e != nil || version != 2 || a.Binding != s.binding {
		return a, ErrPrivacy
	}
	var values string
	for _, query := range []string{"SELECT incarnation,revision,objection,changed FROM account_controls ORDER BY incarnation", "SELECT subject,incarnation,epoch,state,created,coalesce(revoked,0) FROM links ORDER BY subject", "SELECT path,subject FROM backup_subjects ORDER BY path,subject"} {
		rows, e := s.privacy.QueryContext(ctx, query)
		if e != nil {
			return a, ErrPrivacy
		}
		cols, e := rows.Columns()
		if e != nil {
			_ = rows.Close()
			return a, ErrPrivacy
		}
		for rows.Next() {
			fields := make([]any, len(cols))
			pointers := make([]any, len(cols))
			for i := range fields {
				pointers[i] = &fields[i]
			}
			if e = rows.Scan(pointers...); e != nil {
				_ = rows.Close()
				return a, ErrPrivacy
			}
			b, _ := json.Marshal(fields)
			values += string(b) + "\n"
		}
		if e = rows.Close(); e != nil {
			return a, ErrPrivacy
		}
	}
	a.ControlDigest = Digest([]byte(values))
	return a, nil
}
func (s *Service) checkAnchor(ctx context.Context) error {
	if s.mu.failed.Load() {
		return ErrPrivacy
	}
	if s.lockFile != nil {
		opened, e := s.lockFile.Stat()
		current, e2 := os.Lstat(s.privacyPath + ".lock")
		if e != nil || e2 != nil || !os.SameFile(opened, current) {
			return ErrPrivacy
		}
	}

	a, e := s.readAnchor(ctx)
	if e != nil {
		return e
	}
	if e = privatePath(s.anchorPath, false); e != nil {
		return e
	}
	b, e := os.ReadFile(s.anchorPath)
	var saved anchor
	if e != nil || json.Unmarshal(b, &saved) != nil || saved != a || s.now().Unix() < a.Maintenance {
		return ErrPrivacy
	}
	return nil
}
func (s *Service) writeAnchor(ctx context.Context) error {
	a, e := s.readAnchor(ctx)
	if e != nil {
		return e
	}
	b, _ := json.Marshal(a)
	f, e := os.OpenFile(s.anchorPath+".pending", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return ErrPrivacy
	}
	_, e = f.Write(b)
	if e == nil {
		e = f.Sync()
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return ErrPrivacy
	}
	if e = os.Rename(s.anchorPath+".pending", s.anchorPath); e != nil {
		return ErrPrivacy
	}
	d, e := os.Open(filepath.Dir(s.anchorPath))
	if e != nil {
		return e
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
func fingerprint(ctx context.Context, db *sql.DB) (string, error) {
	rows, e := db.QueryContext(ctx, "SELECT type,name,sql FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name")
	if e != nil {
		return "", e
	}
	defer func() { _ = rows.Close() }()
	var text string
	for rows.Next() {
		var a, b, c string
		if e = rows.Scan(&a, &b, &c); e != nil {
			return "", e
		}
		text += fmt.Sprintf("%s\x00%s\x00%s\x00", a, b, c)
	}
	return Digest([]byte(text)), rows.Err()
}
func (s *Service) validateSchema(ctx context.Context, db *sql.DB, schema string) error {
	expected, e := sql.Open("sqlite3", ":memory:")
	if e != nil {
		return e
	}
	defer func() { _ = expected.Close() }()
	expected.SetMaxOpenConns(1)
	if _, e = expected.ExecContext(ctx, schema); e != nil {
		return e
	}
	a, e := fingerprint(ctx, expected)
	if e != nil {
		return e
	}
	b, e := fingerprint(ctx, db)
	if e != nil || a != b {
		return ErrPrivacy
	}
	return nil
}
