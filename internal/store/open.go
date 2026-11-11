package store

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"github.com/ncruces/go-sqlite3/gormlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type Store struct {
	AccountWork sync.RWMutex
	DB          *gorm.DB
	SQL         *sql.DB
	Path        string
}

func Open(path string, create bool) (*Store, error) {
	return open(path, create, false)
}

// OpenReadOnly never creates a database or changes its journal mode.
func OpenReadOnly(path string) (*Store, error) { return open(path, false, true) }

func open(path string, create, readOnly bool) (*Store, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if create {
		f, e := os.OpenFile(absolute, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if e != nil && !os.IsExist(e) {
			return nil, e
		}
		if f != nil {
			if e = f.Close(); e != nil {
				return nil, e
			}
		}
	}
	if !create {
		if _, err = os.Stat(absolute); err != nil {
			return nil, fmt.Errorf("database unavailable: %w", err)
		}
	}
	u := url.URL{Scheme: "file", Path: absolute}
	q := url.Values{"_pragma": {"busy_timeout(5000)", "foreign_keys(1)", "journal_mode(WAL)", "synchronous(FULL)"}, "_txlock": {"immediate"}}
	if !create {
		q.Set("mode", "rw")
	}
	if readOnly {
		q = url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout(5000)", "foreign_keys(1)"}}
	}
	u.RawQuery = q.Encode()
	db, err := gorm.Open(gormlite.Open(u.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	pool, err := db.DB()
	if err != nil {
		return nil, err
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	var fk int
	if err = pool.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		_ = pool.Close()
		return nil, fmt.Errorf("database foreign keys unavailable")
	}
	return &Store{DB: db, SQL: pool, Path: absolute}, nil
}

func (s *Store) Close() error { return s.SQL.Close() }
