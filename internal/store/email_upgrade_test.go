package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/LowGok/my-chef/migrations"
)

func TestRestoreRejectsPreIncarnationSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source, err := Open(filepath.Join(dir, "v3.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = source.SQL.ExecContext(ctx, migrations.Initial+migrations.Shopping+migrations.Favorites+
		"CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,checksum TEXT NOT NULL,dirty BOOLEAN NOT NULL);"); err != nil {
		_ = source.Close()
		t.Fatal(err)
	}
	if _, err = source.SQL.ExecContext(ctx, "INSERT INTO schema_migrations VALUES(3,?,0)", favoritesSchemaChecksum()); err != nil {
		_ = source.Close()
		t.Fatal(err)
	}
	if err = source.Close(); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "restored.db")
	if err = Restore(ctx, source.Path, dest); err == nil {
		t.Fatal("pre-incarnation backup restored without a verified identity mapping")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("unsafe restore was published")
	}
	original, err := OpenReadOnly(source.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = original.Close() }()
	var version int
	if err = original.SQL.QueryRowContext(ctx, "SELECT version FROM schema_migrations").Scan(&version); err != nil || version != 3 {
		t.Fatalf("source changed: version=%d err=%v", version, err)
	}
}
