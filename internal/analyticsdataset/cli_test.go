package analyticsdataset

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIWorkflow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "analytics.sqlite")
	manifest := filepath.Join(dir, "fixture.json")
	var out, stderr bytes.Buffer
	if err := Run(testContext, []string{"fixture", "--db", path, "--manifest", manifest}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	d, err := Open(testContext, path, true)
	if err != nil {
		t.Fatal(err)
	}
	if count(t, d, "subjects") != 1000 {
		t.Fatal("fixture population differs")
	}
	_ = d.Close()
	for _, args := range [][]string{{"validate", "--db", path}, {"report", "--db", path}, {"report", "--db", path, "--monthly"}, {"expire", "--db", path}} {
		out.Reset()
		if err := Run(testContext, args, &out, &stderr); err != nil {
			t.Fatal(err)
		}
		if !json.Valid(out.Bytes()) {
			t.Fatalf("invalid JSON output for %s", args[0])
		}
	}
	out.Reset()
	if err := Run(testContext, []string{"report", "--db", path}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	var r Report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	var expected struct {
		Metrics map[string][2]int `json:"metrics"`
	}
	expectedJSON, err := os.ReadFile("../../tests/fixtures/analytics/expected.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(expectedJSON, &expected); err != nil {
		t.Fatal(err)
	}
	// The published fixture lists the earlier activation cohort explicitly.
	out.Reset()
	if err = Run(testContext, []string{"report", "--db", path, "--start", "2026-10-26", "--end", "2026-11-02"}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	for name, counts := range expected.Metrics {
		assertCounts(t, findMetric(t, r, name, "all"), counts[0], counts[1])
	}
	if strings.Contains(out.String(), "s_0000") || strings.Contains(out.String(), "subject_id") {
		t.Fatal("report exposed subjects")
	}
	if err := Run(testContext, []string{"delete-subject", "--db", path, "--subject", key("s", 1)}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	if err := Run(testContext, []string{"import", "--db", path, "--manifest", manifest}, &out, &stderr); err == nil {
		t.Fatal("manifest replay resurrected deleted subject")
	}
	if err := Run(testContext, []string{"fixture", "--db", path}, &out, &stderr); err == nil {
		t.Fatal("fixture overwrote destination")
	}
}

func TestAllCommandsRefuseAppAndRealEnvironment(t *testing.T) {
	for _, kind := range []string{"app", "real", "unknown_version"} {
		d := newTestDataset(t)
		path := d.path
		if kind == "app" {
			if _, err := d.db.Exec("PRAGMA application_id=1234"); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := d.db.Exec("PRAGMA ignore_check_constraints=ON"); err != nil {
				t.Fatal(err)
			}
			query := "UPDATE analytics_schema SET environment='production'"
			if kind == "unknown_version" {
				query = "UPDATE analytics_schema SET version=2"
			}
			if _, err := d.db.Exec(query); err != nil {
				t.Fatal(err)
			}
		}
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, op := range []string{"validate", "report", "expire", "delete-subject", "import", "fixture"} {
			args := []string{op, "--db", path}
			if op == "delete-subject" {
				args = append(args, "--subject", key("s", 1))
			}
			var out, stderr bytes.Buffer
			if err := Run(testContext, args, &out, &stderr); err == nil {
				t.Fatalf("%s accepted %s", op, kind)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatalf("%s mutated %s", op, kind)
			}
		}
	}
}

func TestDictionaryAndSQLContract(t *testing.T) {
	d := newTestDataset(t)
	schema, err := os.ReadFile("../../docs/reference/analytics-dataset-schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if string(schema) != SchemaSQL {
		t.Fatal("documented schema drift")
	}
	dictionary, err := os.ReadFile("../../docs/reference/analytics-dataset.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, metric := range metricNames() {
		if !strings.Contains(string(dictionary), "`"+metric+"`") {
			t.Fatalf("undocumented monthly metric %s", metric)
		}
	}
	tables, err := d.db.Query("SELECT name FROM sqlite_schema WHERE type='table' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	_ = tables.Close()
	for _, name := range names {
		if !strings.Contains(string(dictionary), "### "+name+"\n") {
			t.Fatalf("missing table %s", name)
		}
		_, section, _ := strings.Cut(string(dictionary), "### "+name+"\n")
		section, _, _ = strings.Cut(section, "\n### ")
		columns, err := d.db.Query("PRAGMA table_info(" + name + ")")
		if err != nil {
			t.Fatal(err)
		}
		for columns.Next() {
			var id, notnull, pk int
			var column, kind string
			var defaultValue any
			if err := columns.Scan(&id, &column, &kind, &notnull, &defaultValue, &pk); err != nil {
				t.Fatal(err)
			}
			required := "no"
			if notnull != 0 || pk != 0 {
				required = "yes"
			}
			if !strings.Contains(section, "| `"+column+"` | "+kind+" | "+required+" |") {
				t.Fatalf("undocumented field %s.%s", name, column)
			}
		}
		_ = columns.Close()
	}
	queries, err := os.ReadFile("../../docs/reference/analytics-queries.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(string(queries)); err != nil {
		t.Fatalf("documented SQL is not executable: %v", err)
	}
}

func TestMonthlyTextFormat(t *testing.T) {
	d, _ := importedPopulation(t, 20)
	var out, stderr bytes.Buffer
	if err := Run(testContext, []string{"report", "--db", d.path, "--monthly", "--format", "text"}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	if json.Valid(out.Bytes()) || !strings.Contains(out.String(), "2026-10 activity") {
		t.Fatal("monthly text format was ignored")
	}
}
