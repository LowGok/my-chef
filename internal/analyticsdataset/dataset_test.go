package analyticsdataset

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testContext = context.Background()

func stamp(value string) int64 {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return t.Unix()
}

func testBatch() Batch {
	r := stamp("2026-11-13T00:00:00Z")
	return Batch{Version: 1, Environment: "synthetic", ExtractID: "x_0000000000000001", ReportingUnix: r,
		ID:          "b_0000000000000001",
		Subjects:    []Subject{{ID: "s_0000000000000001", EligibleFrom: r - 30*86400, CohortStart: r - 30*86400, NoticeVersion: "v1"}},
		Consent:     []Consent{{ID: "c_0000000000000001", SubjectID: "s_0000000000000001", Purpose: "product", Start: r - 30*86400, NoticeVersion: "v1"}},
		Eligibility: []Eligibility{{ID: "f_0000000000000001", SubjectID: "s_0000000000000001", Feature: "meal_plan_saved", Start: r - 30*86400}},
		Activity:    []Activity{{ID: "a_0000000000000001", SubjectID: "s_0000000000000001", Occurred: r - 86400, Name: "meal_plan_saved", Platform: "ios", Plan: "free"}},
	}
}

func newTestDataset(t *testing.T) *Dataset {
	t.Helper()
	b := testBatch()
	d, err := Create(testContext, filepath.Join(t.TempDir(), "analytics.db"), b.ExtractID, b.ReportingUnix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func count(t *testing.T, d *Dataset, table string) int {
	t.Helper()
	var n int
	if err := d.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestIsolatedInitialization(t *testing.T) {
	d := newTestDataset(t)
	if err := d.Validate(testContext); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(d.path)
	if _, err := Create(testContext, d.path, d.extractID, d.reportingUnix); err == nil {
		t.Fatal("overwrote existing destination")
	}
	after, _ := os.ReadFile(d.path)
	if !bytes.Equal(before, after) {
		t.Fatal("refusal changed existing database")
	}
	app := filepath.Join(t.TempDir(), "app.db")
	if err := os.WriteFile(app, []byte("app-private-canary"), 0600); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte("app-private-canary"))
	if _, err := Open(testContext, app, false); err == nil {
		t.Fatal("accepted application file")
	}
	data, _ := os.ReadFile(app)
	if sha256.Sum256(data) != h {
		t.Fatal("changed refused file")
	}
}

func TestAtomicImportAndReplay(t *testing.T) {
	d := newTestDataset(t)
	b := testBatch()
	added, err := d.Import(testContext, b)
	if err != nil || !added {
		t.Fatalf("initial import: %v, %v", added, err)
	}
	added, err = d.Import(testContext, b)
	if err != nil || added {
		t.Fatalf("replay: %v, %v", added, err)
	}
	b.ID = "b_0000000000000002"
	b.Activity[0].Platform = "android"
	if _, err = d.Import(testContext, b); err == nil {
		t.Fatal("accepted conflicting fact")
	}
	if count(t, d, "import_batches") != 1 || count(t, d, "activity_events") != 1 {
		t.Fatal("partial conflicting import")
	}
	b = testBatch()
	b.ID = "b_0000000000000003"
	b.Activity = append(b.Activity, Activity{ID: "a_0000000000000002", SubjectID: b.Subjects[0].ID, Occurred: b.ReportingUnix - 40*86400, Name: "meal_plan_saved", Platform: "ios", Plan: "free"})
	if _, err = d.Import(testContext, b); err == nil {
		t.Fatal("accepted pre-consent activity")
	}
	if count(t, d, "import_batches") != 1 || count(t, d, "activity_events") != 1 {
		t.Fatal("partial out-of-consent import")
	}
}

func TestStrictManifestAndFixedDate(t *testing.T) {
	for _, input := range []string{`{"version":1,"email":"private-canary"}`, `{"version":1,"version":2}`, `{"version":1,"VERSION":1}`, `{"Version":1}`, `{} {}`, `{"activity":[{"private":"canary"}]}`} {
		if _, err := Decode(strings.NewReader(input)); err == nil {
			t.Fatal("accepted ambiguous or forbidden input")
		}
	}
	for _, change := range []func(*Batch){
		func(b *Batch) { b.Environment = "production" },
		func(b *Batch) { b.ReportingUnix++ },
		func(b *Batch) { b.Activity[0].Occurred = b.ReportingUnix - 90*86400 - 1 },
		func(b *Batch) { b.Activity[0].Occurred = b.ReportingUnix + 1 },
		func(b *Batch) { b.Activity[0].Name = "private-canary" },
		func(b *Batch) { b.Activity[0].SubjectID = "someone@example.com" },
	} {
		d := newTestDataset(t)
		b := testBatch()
		change(&b)
		if _, err := d.Import(testContext, b); err == nil {
			t.Fatal("accepted invalid input")
		}
		if count(t, d, "subjects") != 0 {
			t.Fatal("partial invalid import")
		}
	}
}

func TestPhysicalSchemaMismatch(t *testing.T) {
	d := newTestDataset(t)
	if _, err := d.db.Exec("ALTER TABLE subjects ADD COLUMN unexpected TEXT"); err != nil {
		t.Fatal(err)
	}
	if err := d.Validate(testContext); err == nil {
		t.Fatal("accepted altered physical schema")
	}
}
