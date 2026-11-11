package store

import (
	"encoding/json"
	"strings"
	"testing"

	"gorm.io/gorm"
)

func TestPersonalPhotoSchemaUpgradePreservesSavedVersionAndRollsBackConflicts(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "upgrade", true: "conflict"}[conflict], func(t *testing.T) {
			s := versionTwelvePersonalFixture(t)
			ctx := t.Context()
			if err := s.migratePersonalRecipes(ctx); err != nil {
				t.Fatal(err)
			}
			var incarnation string
			if err := s.SQL.QueryRow("SELECT incarnation FROM account_incarnations WHERE user_id=1").Scan(&incarnation); err != nil {
				t.Fatal(err)
			}
			content, err := personalDocument("Keep original version").Normalize()
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(content)
			if err != nil {
				t.Fatal(err)
			}
			id, version := strings.Repeat("a", 32), strings.Repeat("b", 32)
			if err := s.DB.Transaction(func(tx *gorm.DB) error {
				if err := tx.Exec("INSERT INTO personal_recipes(id,user_id,incarnation,current_version,title,search_title,created_ns,updated_ns) VALUES(?,1,?,?,'Keep original version','keep original version',1,1)", id, incarnation, version).Error; err != nil {
					return err
				}
				return tx.Exec("INSERT INTO personal_recipe_versions(id,recipe_id,body,created_ns) VALUES(?,?,?,1)", version, id, string(body)).Error
			}); err != nil {
				t.Fatal(err)
			}
			if conflict {
				if err := s.DB.Exec("CREATE TABLE personal_photo_assets(sentinel TEXT);INSERT INTO personal_photo_assets VALUES('keep-this')").Error; err != nil {
					t.Fatal(err)
				}
			}
			err = s.Migrate(ctx)
			if conflict && err == nil || !conflict && err != nil {
				t.Fatal("unexpected migration result", err)
			}
			var actual, storedVersion, storedBody string
			if err := s.SQL.QueryRow("SELECT current_version FROM personal_recipes WHERE id=?", id).Scan(&storedVersion); err != nil || storedVersion != version {
				t.Fatal("upgrade changed version pointer", err)
			}
			if err := s.SQL.QueryRow("SELECT body FROM personal_recipe_versions WHERE id=?", version).Scan(&storedBody); err != nil || storedBody != string(body) {
				t.Fatal("upgrade rewrote content", err)
			}
			var current int
			if err := s.SQL.QueryRow("SELECT version FROM schema_migrations").Scan(&current); err != nil {
				t.Fatal(err)
			}
			if conflict {
				if current != 13 {
					t.Fatal("partial migration changed schema version")
				}
				if err := s.SQL.QueryRow("SELECT sentinel FROM personal_photo_assets").Scan(&actual); err != nil || actual != "keep-this" {
					t.Fatal("conflicting content overwritten", err)
				}
				var columns int
				if err := s.SQL.QueryRow("SELECT COUNT(*) FROM pragma_table_info('personal_recipe_versions') WHERE name='photo_id'").Scan(&columns); err != nil || columns != 0 {
					t.Fatal("partial photo column survived", err)
				}
			} else if err := s.CheckSchema(ctx); err != nil || current != len(schemaVersions) {
				t.Fatal("photo schema absent", err)
			}
		})
	}
}

func TestPersonalPhotoJournalUpgradeRollsBackAllChangesOnConflict(t *testing.T) {
	s, _, _, _, _ := personalFixture(t)
	ctx := t.Context()
	external, err := Open(s.Path+".legacy", true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = external.Close() }()
	var binding string
	if err := s.SQL.QueryRow("SELECT binding FROM lifecycle_binding").Scan(&binding); err != nil {
		t.Fatal(err)
	}
	if err := external.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(lifecycleJournalSchema).Error; err != nil {
			return err
		}
		if err := tx.Exec("INSERT INTO lifecycle_journal_meta VALUES(1,1,?)", binding).Error; err != nil {
			return err
		}
		return tx.Exec("ALTER TABLE lifecycle_work ADD COLUMN purpose TEXT NOT NULL DEFAULT 'conflicting-purpose'").Error
	}); err != nil {
		t.Fatal(err)
	}
	if err := migratePersonalPhotoJournal(ctx, external); err == nil {
		t.Fatal("conflicting journal replaced")
	}
	var version, columns, triggers int
	if err := external.SQL.QueryRow("SELECT version FROM lifecycle_journal_meta").Scan(&version); err != nil || version != 1 {
		t.Fatal("failed journal upgrade changed version", err)
	}
	if err := external.SQL.QueryRow("SELECT COUNT(*) FROM pragma_table_info('lifecycle_work') WHERE name='delivery_type'").Scan(&columns); err != nil || columns != 0 {
		t.Fatal("partial private type survived", err)
	}
	if err := external.SQL.QueryRow("SELECT COUNT(*) FROM sqlite_schema WHERE type='trigger' AND name IN ('lifecycle_journal_binding_immutable','lifecycle_journal_binding_required')").Scan(&triggers); err != nil || triggers != 2 {
		t.Fatal("journal protection lost", err)
	}
}
