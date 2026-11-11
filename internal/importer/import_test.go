package importer

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/store"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

//go:embed testdata/postgres.sql
var sourceDDL string

const legacyMealID int64 = 9223372036854775000
const exactQuantity = "12345678901234567890.123456789012345678901234567890"
const oldInstructions = `{"First, chop","Then say \"ready\"","Back\\slash","NULL",""}`

func fixture(t *testing.T) (context.Context, *pgx.Conn, string, string) {
	t.Helper()
	dsn := os.Getenv("MYCHEF_TEST_POSTGRES_URL")
	if os.Getenv("MYCHEF_TEST_POSTGRES") != "1" || dsn == "" {
		t.Skip("explicit disposable PostgreSQL fixture required")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid fixture configuration")
	}
	ip := net.ParseIP(cfg.Host)
	if cfg.Database != "mychef_test" || (cfg.Host != "localhost" && (ip == nil || !ip.IsLoopback())) {
		t.Fatal("integration tests require the local disposable mychef_test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal("fixture database unavailable")
	}
	schema := fmt.Sprintf("rewrite_%d", time.Now().UnixNano())
	if _, err = conn.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		_ = conn.Close(context.Background())
	})
	if _, err = conn.Exec(ctx, "SET search_path TO "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, sourceDDL); err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("LegacyPass2026!"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	legacyHash := strings.Replace(string(hash), "$2a$", "$2b$", 1)
	if _, err = conn.Exec(ctx, `INSERT INTO users(user_id,firstname,lastname,sex,birthdate,email,password,username,allergens,diet,dislikes,lastmeal,cooking_efforts,cooking_technique,haspref) VALUES(1,'Ada','Chéf',NULL,NULL,'CaseSensitive@Example.Test',$1,'ada','Milk, Nuts','Vegetarian, Gluten-Free','Mushrooms',NULL,'Quick weekday meals','Roasting, Steaming',NULL),(2,'Grace','Cook','F','1990-02-03','other@example.test',$1,'grace',NULL,NULL,NULL,'1970-01-01',NULL,NULL,FALSE)`, legacyHash); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `INSERT INTO meals(meal_id,creator_id,creationdate,recipetitle,yield,preptime,cooktime,totaltime,mychefnotes,equipment,instructions,allergens,ismeatorfish,recipeimg,cloudinary_public_id,allergenssafetynote) VALUES($1,1,'2026-12-31','Duplicate title',$2,'5 minutes',NULL,'20 min','A note with a comma, and a quote "','Pan, oven',$3,'{}',FALSE,'https://res.cloudinary.com/test/source.png','test/source','Read labels.'),($4,2,'2027-01-01','Duplicate title',NULL,NULL,'',NULL,NULL,NULL,'Old instructions without array encoding',NULL,NULL,NULL,NULL,NULL)`, legacyMealID, `{"2 adults","NULL",""}`, oldInstructions, legacyMealID+1); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `INSERT INTO ingredients(pk_ingredients,recipe_id,quantity,unit,ingredient_name,date) VALUES(10,$1,$2::text::numeric,'','Crème fraîche',NULL),(11,$3,NULL,NULL,'Leek','2027-01-01')`, legacyMealID, exactQuantity, legacyMealID+1); err != nil {
		t.Fatal(err)
	}
	return ctx, conn, schema, dsn
}
func sourceHash(t *testing.T, ctx context.Context, conn *pgx.Conn, schema string) string {
	t.Helper()
	hash := sha256.New()
	for _, table := range []string{"users", "meals", "ingredients"} {
		rows, err := conn.Query(ctx, "SELECT to_jsonb(t)::text FROM "+pgx.Identifier{schema, table}.Sanitize()+" t ORDER BY 1")
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var value string
			if err = rows.Scan(&value); err != nil {
				t.Fatal(err)
			}
			_, _ = hash.Write([]byte(value))
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	return hex.EncodeToString(hash.Sum(nil))
}
func normal(value any) any {
	switch v := value.(type) {
	case int32:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case time.Time:
		return v.Format(time.DateOnly)
	default:
		return value
	}
}
func compareEveryColumn(t *testing.T, ctx context.Context, conn *pgx.Conn, schema string, target *store.Store) {
	t.Helper()
	for table, fields := range expectedColumns {
		columns := make([]string, 0, len(fields))
		for column := range fields {
			columns = append(columns, column)
		}
		sort.Strings(columns)
		sourceColumns := append([]string{}, columns...)
		for i, column := range columns {
			if fields[column] == "numeric" {
				sourceColumns[i] = column + "::text"
			}
		}
		primary := map[string]string{"users": "user_id", "meals": "meal_id", "ingredients": "pk_ingredients"}[table]
		source, err := conn.Query(ctx, "SELECT "+strings.Join(sourceColumns, ",")+" FROM "+pgx.Identifier{schema, table}.Sanitize()+" ORDER BY "+primary)
		if err != nil {
			t.Fatal(err)
		}
		dest, err := target.SQL.QueryContext(ctx, "SELECT "+strings.Join(columns, ",")+" FROM "+table+" ORDER BY "+primary)
		if err != nil {
			t.Fatal(err)
		}
		for source.Next() {
			if !dest.Next() {
				t.Fatal("destination row missing")
			}
			original, err := source.Values()
			if err != nil {
				t.Fatal(err)
			}
			values := make([]any, len(columns))
			pointers := make([]any, len(values))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err = dest.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			for i, column := range columns {
				if table == "meals" && (column == "yield" || column == "instructions" || column == "allergens") {
					continue
				}
				if !reflect.DeepEqual(normal(original[i]), normal(values[i])) {
					t.Fatalf("%s.%s was not preserved", table, column)
				}
			}
		}
		if source.Err() != nil || dest.Err() != nil || dest.Next() {
			t.Fatal("source/destination row counts differ")
		}
		source.Close()
		_ = dest.Close()
	}
}
func TestPostgresSnapshotPreservesDataAndLogin(t *testing.T) {
	ctx, conn, schema, dsn := fixture(t)
	before := sourceHash(t, ctx, conn, schema)
	snapshot, err := openSnapshot(ctx, dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	var readonly, isolation string
	if err = snapshot.tx.QueryRow(ctx, "SHOW transaction_read_only").Scan(&readonly); err != nil {
		t.Fatal(err)
	}
	if err = snapshot.tx.QueryRow(ctx, "SHOW transaction_isolation").Scan(&isolation); err != nil {
		t.Fatal(err)
	}
	if readonly != "on" || isolation != "repeatable read" {
		t.Fatal("snapshot is not read-only and repeatable")
	}
	snapshot.close()
	destination := filepath.Join(t.TempDir(), "import.db")
	report, err := Import(ctx, dsn, destination, schema)
	if err != nil {
		t.Fatal(err)
	}
	if report.Users != 2 || report.Meals != 2 || report.Ingredients != 2 || len(report.Warnings) != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if sourceHash(t, ctx, conn, schema) != before {
		t.Fatal("source data changed")
	}
	s, err := store.Open(destination, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	compareEveryColumn(t, ctx, conn, schema, s)
	user, err := auth.New(s).Login(ctx, "CaseSensitive@Example.Test", "LegacyPass2026!")
	if err != nil {
		t.Fatal("legacy bcrypt login failed")
	}
	if !reflect.DeepEqual(user.Preferences().Diet, []string{"Vegetarian", "Gluten-Free"}) {
		t.Fatal("legacy preferences changed")
	}
	recipe, err := s.GetRecipe(ctx, user.ID, legacyMealID)
	if err != nil {
		t.Fatal(err)
	}
	if recipe.ID != strconv.FormatInt(legacyMealID, 10) || recipe.Equipment == nil || *recipe.Equipment != "Pan, oven" || *recipe.Ingredients[0].Quantity != exactQuantity || !reflect.DeepEqual(recipe.Yield, []string{"2 adults", "NULL", ""}) || !reflect.DeepEqual(recipe.Instructions, []string{"First, chop", `Then say "ready"`, `Back\slash`, "NULL", ""}) {
		t.Fatal("recipe fields changed")
	}
	if recipe.LegacyRaw["instructions"] == nil || *recipe.LegacyRaw["instructions"] != oldInstructions {
		t.Fatal("raw legacy text not preserved")
	}
	if _, err = s.GetRecipe(ctx, user.ID, legacyMealID+1); err == nil {
		t.Fatal("ownership changed")
	}
	newMeal := store.Meal{CreatorID: user.ID, CreationDate: "2027-01-02", RecipeTitle: "New recipe", ImageStatus: "unavailable"}
	if err = s.DB.Create(&newMeal).Error; err != nil || newMeal.ID != legacyMealID+2 {
		t.Fatalf("new meal ID collided: %d %v", newMeal.ID, err)
	}
	if _, err = Import(ctx, dsn, destination, schema); err == nil {
		t.Fatal("existing destination was overwritten")
	}
	var row store.Meal
	if err = s.DB.First(&row, newMeal.ID).Error; err != nil {
		t.Fatal("existing destination changed")
	}
}
func TestPostgresImportRejectsUnknownAndInvalidDataAtomically(t *testing.T) {
	for _, tc := range []struct{ name, change string }{{"unknown_column", "ALTER TABLE users ADD COLUMN unexpected TEXT"}, {"duplicate_identity", "ALTER TABLE users DROP CONSTRAINT users_email_key; UPDATE users SET email='CaseSensitive@Example.Test' WHERE user_id=2"}, {"orphan", "ALTER TABLE ingredients DROP CONSTRAINT ingredients_recipe_id_fkey; UPDATE ingredients SET recipe_id=1234 WHERE pk_ingredients=10"}, {"unsupported_numeric", "UPDATE ingredients SET quantity='NaN'::numeric WHERE pk_ingredients=10"}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, conn, schema, dsn := fixture(t)
			if _, err := conn.Exec(ctx, tc.change); err != nil {
				t.Fatal(err)
			}
			before := sourceHash(t, ctx, conn, schema)
			destination := filepath.Join(t.TempDir(), "rejected.db")
			if _, err := Import(ctx, dsn, destination, schema); err == nil {
				t.Fatal("invalid import accepted")
			}
			if _, err := os.Lstat(destination); !os.IsNotExist(err) {
				t.Fatal("failed import published a destination")
			}
			if before != sourceHash(t, ctx, conn, schema) {
				t.Fatal("failed import changed source")
			}
		})
	}
}

func TestImportReportContainsNoAccountFields(t *testing.T) {
	b, err := json.Marshal(Report{Users: 2, Warnings: []Warning{{Table: "meals", Column: "instructions", ID: "1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "password") || strings.Contains(string(b), "email") {
		t.Fatal("sensitive fields in import report")
	}
}
