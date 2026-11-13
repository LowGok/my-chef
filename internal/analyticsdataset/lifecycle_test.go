package analyticsdataset

import "testing"

func historyBatch(n int) Batch {
	b := populationBatch(n)
	s := stamp("2025-11-01T00:00:00Z")
	for i := range b.Subjects {
		b.Subjects[i].EligibleFrom = s
		b.Subjects[i].CohortStart = s
	}
	for i := range b.Consent {
		b.Consent[i].Start = s
	}
	for i := 1; i <= n; i++ {
		b.Contributions = append(b.Contributions, Contribution{ID: key("h", i), SubjectID: key("s", i), Month: "2026-07", Metric: "activity", Dimension: "all", Segment: "all", Numerator: 1, Denominator: 1, QueryVersion: 1, Permission: true, Coverage: "complete"})
	}
	return b
}
func monthlyMetric(t *testing.T, d *Dataset, month, metric string) MonthlyMetric {
	t.Helper()
	r, err := d.Monthly(testContext, "all")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range r.Rows {
		if m.Month == month && m.Name == metric {
			return m
		}
	}
	t.Fatalf("missing monthly metric %s/%s", month, metric)
	return MonthlyMetric{}
}

func TestMonthlyHistoryErasureAndReplay(t *testing.T) {
	d := newTestDataset(t)
	b := historyBatch(20)
	if _, err := d.Import(testContext, b); err != nil {
		t.Fatal(err)
	}
	assertCounts(t, monthlyMetric(t, d, "2026-07", "activity").Metric, 20, 20)
	if err := d.DeleteSubject(testContext, key("s", 1)); err != nil {
		t.Fatal(err)
	}
	assertCounts(t, monthlyMetric(t, d, "2026-07", "activity").Metric, 19, 19)
	for _, table := range []string{"subjects", "consent_periods", "feature_eligibility", "activity_events", "monthly_contributions"} {
		column := "subject_id"
		if table == "subjects" {
			column = "id"
		}
		var n int
		if err := d.db.QueryRow("SELECT count(*) FROM "+table+" WHERE "+column+"=?", key("s", 1)).Scan(&n); err != nil || n != 0 {
			t.Fatalf("retained erased data in %s", table)
		}
	}
	if _, err := d.Import(testContext, b); err == nil {
		t.Fatal("old batch resurrected erased subject")
	}
	b.ID = key("b", 100)
	if _, err := d.Import(testContext, b); err == nil {
		t.Fatal("new batch resurrected erased subject")
	}
	if err := d.DeleteSubject(testContext, key("s", 1)); err != nil {
		t.Fatal("deletion retry failed")
	}
}

func TestMonthlyThresholdAndAtomicDeletion(t *testing.T) {
	d := newTestDataset(t)
	b := historyBatch(10)
	if _, err := d.Import(testContext, b); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`CREATE TEMP TRIGGER stop_metrics BEFORE DELETE ON monthly_metrics BEGIN SELECT RAISE(ABORT,'fault'); END`); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteSubject(testContext, key("s", 1)); err == nil {
		t.Fatal("ignored aggregate failure")
	}
	if count(t, d, "subjects") != 10 || count(t, d, "subject_tombstones") != 0 {
		t.Fatal("partial erasure")
	}
	if _, err := d.db.Exec("DROP TRIGGER stop_metrics"); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteSubject(testContext, key("s", 1)); err != nil {
		t.Fatal(err)
	}
	m := monthlyMetric(t, d, "2026-07", "activity")
	if m.Status != "suppressed" || m.Value != nil {
		t.Fatal("revealed group after deletion")
	}
}

func TestFrozenExpiryBoundaries(t *testing.T) {
	d := newTestDataset(t)
	b := historyBatch(20)
	b.Activity = append(b.Activity, Activity{ID: key("a", 90000), SubjectID: key("s", 1), Occurred: b.ReportingUnix - 90*86400, Name: "app_active", Platform: "web", Plan: "free"})
	if _, err := d.Import(testContext, b); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec("INSERT INTO activity_events VALUES(?,?,?,'app_active','web','free',NULL,'stale')", key("a", 90001), key("s", 1), d.cutoff()-1); err != nil {
		t.Fatal(err)
	}
	if err := d.Expire(testContext); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.db.QueryRow("SELECT count(*) FROM activity_events WHERE occurred_unix<?", d.cutoff()).Scan(&n); err != nil || n != 0 {
		t.Fatal("expired row remains")
	}
	if err := d.db.QueryRow("SELECT count(*) FROM activity_events WHERE occurred_unix=?", d.cutoff()).Scan(&n); err != nil || n != 1 {
		t.Fatal("removed inclusive boundary")
	}
	if d.reportingUnix != b.ReportingUnix {
		t.Fatal("moved frozen date")
	}
	bad := testBatch()
	bad.ID = key("b", 90001)
	bad.Activity[0].Occurred = d.cutoff() - 1
	if _, err := d.Import(testContext, bad); err == nil {
		t.Fatal("accepted unseen expired event")
	}
	for _, month := range []string{"2025-10", "2026-09", "2026-11"} {
		bad = historyBatch(1)
		bad.ID = key("b", 90002)
		bad.Contributions[0].Month = month
		if _, err := d.Import(testContext, bad); err == nil {
			t.Fatal("accepted out-of-window or overlapping monthly source")
		}
	}
}

func TestFirstRetainedMonthAndConcurrentReplay(t *testing.T) {
	d := newTestDataset(t)
	b := historyBatch(20)
	for i := range b.Contributions {
		b.Contributions[i].Month = d.firstMonth()
	}
	errors := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := d.Import(testContext, b); errors <- err }()
	}
	for i := 0; i < 2; i++ {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	if count(t, d, "import_batches") != 1 {
		t.Fatal("concurrent replay duplicated receipt")
	}
	assertCounts(t, monthlyMetric(t, d, d.firstMonth(), "activity").Metric, 20, 20)
}

func TestUnchangedFrozenExpiryPreservesPublishedCache(t *testing.T) {
	d := newTestDataset(t)
	b := historyBatch(20)
	if _, err := d.Import(testContext, b); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`CREATE TEMP TRIGGER preserve_metrics BEFORE DELETE ON monthly_metrics BEGIN SELECT RAISE(ABORT,'unexpected rebuild'); END`); err != nil {
		t.Fatal(err)
	}
	if err := d.Expire(testContext); err != nil {
		t.Fatal("unchanged frozen expiry rebuilt the cache", err)
	}
}

func TestHistoricalPopulationComponents(t *testing.T) {
	for _, metric := range []string{"activity", "opens", "audience"} {
		d := newTestDataset(t)
		b := historyBatch(20)
		b.Contributions[0].Metric = metric
		b.Contributions[0].Numerator = 0
		if _, err := d.Import(testContext, b); err == nil {
			t.Fatalf("accepted a population member without its count for %s", metric)
		}
	}
}
