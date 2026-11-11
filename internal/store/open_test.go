package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ncruces/go-sqlite3/driver"
)

func TestDriverForeignKeysAndWAL(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "driver.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var mode string
	if err = s.SQL.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("WAL: %s %v", mode, err)
	}
	for _, ddl := range []string{"CREATE TABLE parent(id INTEGER PRIMARY KEY)", "CREATE TABLE child(id INTEGER PRIMARY KEY,parent_id INTEGER REFERENCES parent(id))"} {
		if err = s.DB.Exec(ddl).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err = s.DB.Table("child").Create(map[string]any{"id": 1, "parent_id": 99}).Error; err == nil {
		t.Fatal("orphan insert succeeded")
	}
	if err = s.DB.Table("parent").Create(map[string]any{"id": 42}).Error; err != nil {
		t.Fatal(err)
	}
	conn, err := s.SQL.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	err = conn.Raw(func(raw any) error { return raw.(driver.Conn).Raw().Backup("main", backup) })
	_ = conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := Open(backup, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	var count int64
	if err = restored.DB.Table("parent").Where("id = ?", 42).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("backup data: %d %v", count, err)
	}
	_ = s.SQL.Close()
	s2, err := Open(s.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	var fk int
	if err = s2.SQL.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("reopened foreign keys: %d %v", fk, err)
	}
}
