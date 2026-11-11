package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigrationStateAndLargeIDs(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "store.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if s.CheckSchema(ctx) == nil {
		t.Fatal("unmigrated database accepted")
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	u := User{ID: 9007199254740993, FirstName: "A", LastName: "B", Email: "a@test.invalid", Username: "a", Password: "hash"}
	if err = s.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	next := User{FirstName: "C", LastName: "D", Email: "c@test.invalid", Username: "c", Password: "hash"}
	if err = s.DB.Create(&next).Error; err != nil || next.ID != u.ID+1 {
		t.Fatalf("large ID allocation: %d %v", next.ID, err)
	}
	if err = s.DB.Exec("UPDATE schema_migrations SET dirty=1").Error; err != nil {
		t.Fatal(err)
	}
	if s.Migrate(ctx) == nil {
		t.Fatal("dirty database accepted")
	}
	if err = s.DB.Exec("UPDATE schema_migrations SET dirty=0,version=99").Error; err != nil {
		t.Fatal(err)
	}
	if s.CheckSchema(ctx) == nil {
		t.Fatal("future schema accepted")
	}
}

func TestMigrationRefusesAnUnversionedForeignDatabase(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "foreign.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err = s.DB.Exec("CREATE TABLE valuable_existing_data(id INTEGER)").Error; err != nil {
		t.Fatal(err)
	}
	if s.Migrate(context.Background()) == nil {
		t.Fatal("unversioned foreign database accepted")
	}
	var n int
	if err = s.SQL.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='schema_migrations'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("foreign database changed: %d %v", n, err)
	}
}
