package analyticsdataset

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

//go:embed schema.sql
var SchemaSQL string

var ErrDataset = errors.New("invalid synthetic analytics dataset")
var ErrInput = errors.New("invalid synthetic analytics input")
var ErrConflict = errors.New("analytics input conflicts with retained data")

type Dataset struct {
	db            *sql.DB
	path          string
	extractID     string
	reportingUnix int64
	readOnly      bool
}

func schemaDigest() string { return fmt.Sprintf("%x", sha256.Sum256([]byte(SchemaSQL))) }

func physicalFingerprint(ctx context.Context, db *sql.DB) (string, error) {
	rows, err := db.QueryContext(ctx, "SELECT type,name,sql FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name")
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	h := sha256.New()
	for rows.Next() {
		var kind, name, ddl string
		if err = rows.Scan(&kind, &name, &ddl); err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s\x00", kind, name, ddl)
	}
	return fmt.Sprintf("%x", h.Sum(nil)), rows.Err()
}

var expectedFingerprint = sync.OnceValues(func() (string, error) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		return "", err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(SchemaSQL); err != nil {
		return "", err
	}
	return physicalFingerprint(context.Background(), db)
})

func Create(ctx context.Context, path, extractID string, reporting int64) (*Dataset, error) {
	if !opaqueID(extractID) || !validTime(reporting) {
		return nil, ErrInput
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, errors.New("cannot create analytics directory")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, errors.New("analytics destination must be a fresh file")
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	d, err := connect(path, false)
	if err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err == nil {
		_, err = tx.ExecContext(ctx, SchemaSQL)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, "INSERT INTO analytics_schema VALUES(1,1,?,'synthetic',?,?)", schemaDigest(), extractID, reporting)
	}
	if err == nil {
		err = tx.Commit()
	} else if tx != nil {
		_ = tx.Rollback()
	}
	if err != nil {
		_ = d.Close()
		_ = os.Remove(path)
		return nil, ErrDataset
	}
	d.extractID, d.reportingUnix = extractID, reporting
	return d, nil
}

func connect(path string, readOnly bool) (*Dataset, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrDataset
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, ErrDataset
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := url.Values{"mode": {"rw"}, "_pragma": {"busy_timeout(5000)", "foreign_keys(1)"}}
	if readOnly {
		q.Set("mode", "ro")
		q.Add("_pragma", "query_only(1)")
	}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, ErrDataset
	}
	db.SetMaxOpenConns(1)
	return &Dataset{db: db, path: abs, readOnly: readOnly}, nil
}

func Open(ctx context.Context, path string, readOnly bool) (*Dataset, error) {
	// Inspect through a read-only connection before opening any writer.
	d, err := connect(path, true)
	if err != nil {
		return nil, err
	}
	if err = d.Validate(ctx); err != nil {
		_ = d.Close()
		return nil, err
	}
	if readOnly {
		return d, nil
	}
	_ = d.Close()
	d, err = connect(path, false)
	if err != nil {
		return nil, err
	}
	if err = d.Validate(ctx); err != nil {
		_ = d.Close()
		return nil, err
	}
	return d, nil
}

func (d *Dataset) Close() error { return d.db.Close() }

func (d *Dataset) Validate(ctx context.Context) error {
	var app, version, rows int
	var environment, digest, integrity string
	var extractID string
	var reportingUnix int64
	if d.db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&app) != nil || app != 1296253262 {
		return ErrDataset
	}
	if d.db.QueryRowContext(ctx, "SELECT count(*) FROM analytics_schema").Scan(&rows) != nil || rows != 1 {
		return ErrDataset
	}
	err := d.db.QueryRowContext(ctx, "SELECT version,checksum,environment,extract_id,reporting_unix FROM analytics_schema WHERE singleton=1").Scan(&version, &digest, &environment, &extractID, &reportingUnix)
	if err != nil || version != 1 || digest != schemaDigest() || environment != "synthetic" || !opaqueID(extractID) || !validTime(reportingUnix) {
		return ErrDataset
	}
	if d.extractID == "" {
		d.extractID, d.reportingUnix = extractID, reportingUnix
	} else if d.extractID != extractID || d.reportingUnix != reportingUnix {
		return ErrDataset
	}
	expected, err := expectedFingerprint()
	if err != nil {
		return ErrDataset
	}
	actual, err := physicalFingerprint(ctx, d.db)
	if err != nil || actual != expected {
		return ErrDataset
	}
	if d.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity) != nil || integrity != "ok" {
		return ErrDataset
	}
	fk, err := d.db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return ErrDataset
	}
	defer func() { _ = fk.Close() }()
	if fk.Next() {
		return ErrDataset
	}
	if fk.Err() != nil {
		return ErrDataset
	}
	return nil
}

func (d *Dataset) cutoff() int64 { return d.reportingUnix - 90*86400 }
func (d *Dataset) firstMonth() string {
	return monthStart(d.reportingUnix).AddDate(0, -12, 0).Format("2006-01")
}
func monthStart(r int64) time.Time {
	t := time.Unix(r, 0).UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}
func validTime(t int64) bool { return t >= 946684800 && t < 4133980800 }
