package store

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFeedbackMigrationPreservesAppSchemaAndData(t *testing.T) {
	ctx := t.Context()
	source := versionFourEmailStore(t, "feedback-fixture@example.test")
	for _, step := range []func(context.Context) error{
		source.migrateEmailIdentity, source.migrateEmailLimits, source.migrateSignUp,
		source.migrateEmailVerification, source.migrateEmailChange, source.migratePasswordRecovery,
	} {
		if err := step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := source.SQL.ExecContext(ctx, `
INSERT INTO menus(id,user_id,created_unix) VALUES(1,1,1234);
INSERT INTO meals(meal_id,creator_id,menu_id,creationdate,recipetitle)
VALUES(1,1,1,'2026-11-06','Original recipe');
INSERT INTO favorites(user_id,recipe_id,body,created_unix)
VALUES(1,1,'{"id":"1","title":"Original recipe"}',1234);
INSERT INTO shopping_lists(user_id,week,revision,body)
VALUES(1,'2026-11-02',3,'{"items":[]}');
`); err != nil {
		t.Fatal(err)
	}
	type snapshot struct {
		objects map[string]string
		rows    [5]string
	}
	read := func(db *Store) snapshot {
		t.Helper()
		out := snapshot{objects: map[string]string{}}
		rows, err := db.SQL.QueryContext(ctx, "SELECT name,sql FROM sqlite_schema WHERE type IN ('table','index') AND sql IS NOT NULL AND name NOT LIKE 'sqlite_%'")
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var name, sql string
			if err := rows.Scan(&name, &sql); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			out.objects[name] = sql
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		for i, query := range []string{
			"SELECT email||':'||password FROM users WHERE user_id=1",
			"SELECT user_id||':'||created_unix FROM menus WHERE id=1",
			"SELECT creator_id||':'||menu_id||':'||creationdate||':'||recipetitle FROM meals WHERE meal_id=1",
			"SELECT recipe_id||':'||body FROM favorites WHERE user_id=1",
			"SELECT revision||':'||body FROM shopping_lists WHERE user_id=1",
		} {
			if err := db.SQL.QueryRowContext(ctx, query).Scan(&out.rows[i]); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	before := read(source)
	destination := filepath.Join(t.TempDir(), "feedback-restored.db")
	if err := source.publishSnapshot(ctx, destination, true); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(destination, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	if err := upgraded.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := upgraded.SQL.QueryRowContext(ctx, "SELECT version FROM schema_migrations").Scan(&version); err != nil || version != len(schemaVersions) {
		t.Fatalf("expected current schema after feedback upgrade, got %d (%v)", version, err)
	}
	after := read(upgraded)
	for name, definition := range before.objects {
		if name == "meals" {
			definition = strings.TrimSuffix(definition, ")") + ", personal_source TEXT CHECK(personal_source IS NULL OR json_valid(personal_source)))"
		}
		if after.objects[name] != definition {
			t.Fatalf("migration changed existing schema object %s", name)
		}
		delete(after.objects, name)
	}
	for _, name := range []string{"feedback_reports", "feedback_ratings", "feedback_rating_receipts", "feedback_reports_by_owner_time", "feedback_rating_receipts_by_owner_time", "account_incarnations", "lifecycle_binding", "lifecycle_operations", "account_mail_owners", "account_mail_owner", "account_signup_owners", "account_incarnation_pair", "personal_recipes", "personal_recipes_page", "personal_recipe_copy", "personal_recipe_versions", "personal_recipe_operations", "personal_recipe_operation_source", "personal_photo_assets", "personal_photo_attempts", "personal_photo_attempt_leases", "personal_photo_limits", "personal_photo_limit_key", "personal_photo_requests", "meals_personal_photo", "personal_recipe_queue", "personal_recipe_queue_pending", "personal_queue_receipts", "recipe_import_operations", "recipe_import_owner", "recipe_import_expiry", "personal_recipe_import_sources"} {
		if after.objects[name] == "" {
			t.Fatalf("missing feature schema object %s", name)
		}
		delete(after.objects, name)
	}
	if len(after.objects) != 0 || after.rows != before.rows || !reflect.DeepEqual(read(source), before) {
		t.Fatal("migration changed unrelated schema, representative data, or the v10 snapshot")
	}
}
