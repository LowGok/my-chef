package analyticsdataset

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixtureProductionID(p string, i int) string { return fmt.Sprintf("%s_%032x", p, i) }
func TestProductionIdentityFrozenRetentionAndSyntheticRefusal(t *testing.T) {
	ctx := context.Background()
	r := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC).Unix()
	root := t.TempDir()
	_ = os.Chmod(root, 0700)
	path := filepath.Join(root, "real.sqlite")
	in := ProductionSnapshot{Binding: "fixture-binding", ExtractID: fixtureProductionID("ext", 1), Reporting: r, Generation: 0}
	for i := 1; i <= 10; i++ {
		sub := fixtureProductionID("sub", i)
		in.Periods = append(in.Periods, ProductionPeriod{ID: fixtureProductionID("per", i), Subject: sub, Start: r - 100, Notice: "fixture", Basis: "legitimate_interest", BasisVersion: "fixture", Source: "registration_default", Revision: 1})
		in.Activity = append(in.Activity, Activity{ID: fixtureProductionID("evt", i), SubjectID: sub, Occurred: r - 90*86400, Name: "meal_plan_saved", Platform: "web", Plan: "free"})
	}
	// Facts before their processing period are rejected without creating a destination.
	if d, e := CreateProduction(ctx, path, in); e == nil {
		_ = d.Close()
		t.Fatal("pre-enrollment facts accepted")
	}
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("refusal modified destination")
	}
	for i := range in.Periods {
		in.Periods[i].Start = r - 90*86400
	}
	d, e := CreateProduction(ctx, path, in)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = d.Close() }()
	if _, e := Open(ctx, path, true); e == nil {
		t.Fatal("production opened as synthetic")
	}
	if e = d.MaintainProduction(ctx, r+1, 1, nil); e != nil {
		t.Fatal(e)
	}
	var n int
	_ = d.db.QueryRow("SELECT count(*) FROM activity_events").Scan(&n)
	if n != 0 {
		t.Fatal("inclusive boundary not expired")
	}
	if d.reportingUnix != r {
		t.Fatal("report date moved")
	}
	if e = d.MaintainProduction(ctx, r, 1, nil); e == nil {
		t.Fatal("retention clock reversed")
	}
	if e = d.Validate(ctx); e != nil {
		t.Fatal(e)
	}
	report, e := d.Report(ctx, ReportOptions{Start: r - 100, End: r + 1, Dimension: "all"})
	if e != nil || report.Environment != "production" || report.SchemaVersion != 2 {
		t.Fatalf("report %+v %v", report, e)
	}
}

func TestProductionMonthlyBucketsAndErasureSuppression(t *testing.T) {
	ctx := context.Background()
	r := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC).Unix()
	in := ProductionSnapshot{Binding: "fixture", ExtractID: fixtureProductionID("ext", 9), Reporting: r}
	for i := 1; i <= 10; i++ {
		sub := fixtureProductionID("sub", i)
		in.Periods = append(in.Periods, ProductionPeriod{ID: fixtureProductionID("per", i), Subject: sub, Start: time.Date(2025, 11, 1, 0, 0, 0, 0, time.UTC).Unix(), Notice: "fixture", Basis: "legitimate_interest", BasisVersion: "fixture", Source: "explicit_enable", Revision: 1})
		in.Contributions = append(in.Contributions, Contribution{ID: fixtureProductionID("con", i), SubjectID: sub, Month: "2025-11", Metric: "activity", Dimension: "all", Segment: "all", Numerator: 1, Denominator: 1, Permission: true, Coverage: "complete", ObservationStatus: "eligible"})
	}
	d, e := CreateProduction(ctx, filepath.Join(t.TempDir(), "monthly.sqlite"), in)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = d.Close() }()
	report, e := d.Monthly(ctx, "all")
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, m := range report.Rows {
		if m.Month == "2025-11" && m.Name == "activity" {
			found = true
			if m.Value == nil || *m.Value != 10 {
				t.Fatalf("ten participants missing: %+v", m)
			}
		}
	}
	if !found {
		t.Fatal("retained month missing")
	}
	if e = d.MaintainProduction(ctx, r, 1, []string{fixtureProductionID("sub", 1)}); e != nil {
		t.Fatal(e)
	}
	report, e = d.Monthly(ctx, "all")
	if e != nil {
		t.Fatal(e)
	}
	for _, m := range report.Rows {
		if m.Month == "2025-11" && m.Name == "activity" && (m.Status != "suppressed" || m.Value != nil) {
			t.Fatal("nine-person group disclosed", m)
		}
	}
	now := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC).Unix()
	if e = d.MaintainProduction(ctx, now, 1, nil); e != nil {
		t.Fatal(e)
	}
	var n int
	_ = d.db.QueryRow("SELECT count(*) FROM monthly_contributions WHERE month='2025-11'").Scan(&n)
	if n != 0 || d.reportingUnix != r {
		t.Fatal("thirteenth bucket or R changed incorrectly")
	}
	report, e = d.Monthly(ctx, "all")
	if e != nil || report.StartMonth != "2025-12" || report.EndMonth != "2026-11" {
		t.Fatalf("frozen/monthly intersection %+v %v", report, e)
	}
}

func TestProductionMonthlyAuthorityFollowsActualPeriods(t *testing.T) {
	ctx := context.Background()
	r := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC).Unix()
	at := func(month, day int) int64 {
		return time.Date(2026, time.Month(month), day, 0, 0, 0, 0, time.UTC).Unix()
	}
	sub := fixtureProductionID("sub", 77)
	june, july := at(6, 1), at(7, 15)
	in := ProductionSnapshot{Binding: "authority-fixture", ExtractID: fixtureProductionID("ext", 77), Reporting: r,
		Periods: []ProductionPeriod{
			{ID: fixtureProductionID("per", 1), Subject: sub, Start: at(5, 1), End: &june, Notice: "v1", Basis: "legitimate_interest", BasisVersion: "basis-v1", Source: "explicit_enable", Revision: 1},
			{ID: fixtureProductionID("per", 2), Subject: sub, Start: june, End: &july, Notice: "v2", Basis: "legitimate_interest", BasisVersion: "basis-v2", Source: "explicit_enable", Revision: 2},
			{ID: fixtureProductionID("per", 3), Subject: sub, Start: july, Notice: "v3", Basis: "legitimate_interest", BasisVersion: "basis-v3", Source: "explicit_enable", Revision: 3}}}
	for i, m := range []string{"2026-05", "2026-07", "2026-08"} {
		in.Contributions = append(in.Contributions, Contribution{ID: fixtureProductionID("con", i+1), SubjectID: sub, Month: m, Metric: "activity", Dimension: "all", Segment: "all", Numerator: 1, Denominator: 1, Permission: true, Coverage: "partial", ObservationStatus: "eligible"})
	}
	d, err := CreateProduction(ctx, filepath.Join(t.TempDir(), "authority.sqlite"), in)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	rows, err := d.db.QueryContext(ctx, "SELECT month,basis_version,enrollment_revision FROM contribution_provenance ORDER BY month,enrollment_revision")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var m, b string
		var rev int
		if err = rows.Scan(&m, &b, &rev); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s:%s:%d", m, b, rev))
	}
	expected := "2026-05:basis-v1:1,2026-07:basis-v2:2,2026-07:basis-v3:3,2026-08:basis-v3:3,2026-09:basis-v3:3,2026-10:basis-v3:3,2026-11:basis-v3:3"
	if strings.Join(got, ",") != expected {
		t.Fatalf("wrong monthly authority: %v", got)
	}
	_ = rows.Close()
	// Preserved historical provenance must match every overlapping revision.
	in.Authorities = []ProductionAuthority{{sub, "2026-07", "basis-v1", 1}}
	wrong := filepath.Join(t.TempDir(), "wrong-authority.sqlite")
	if bad, err := CreateProduction(ctx, wrong, in); err == nil {
		_ = bad.Close()
		t.Fatal("wrong retained authority accepted")
	}
	if _, err := os.Stat(wrong); !os.IsNotExist(err) {
		t.Fatal("provenance refusal created destination")
	}
	// Other later monthly history must not keep an expired earlier period alive.
	if err = d.MaintainProduction(ctx, time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC).Unix(), 1, nil); err != nil {
		t.Fatal(err)
	}
	var obsolete int
	if err = d.db.QueryRow("SELECT count(*) FROM processing_periods WHERE enrollment_revision=1").Scan(&obsolete); err != nil || obsolete != 0 {
		t.Fatalf("expired authority retained %d %v", obsolete, err)
	}
}
