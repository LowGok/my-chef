package analyticsdataset

import (
	"fmt"
	"testing"
)

func key(prefix string, n int) string { return fmt.Sprintf("%s_%016x", prefix, n) }
func populationBatch(n int) Batch {
	b := testBatch()
	b.Subjects = nil
	b.Consent = nil
	b.Eligibility = nil
	b.Activity = nil
	s := stamp("2026-10-26T01:00:00Z")
	for i := 1; i <= n; i++ {
		id := key("s", i)
		b.Subjects = append(b.Subjects, Subject{ID: id, EligibleFrom: s, CohortStart: s, NoticeVersion: "v1"})
		b.Consent = append(b.Consent, Consent{ID: key("c", i), SubjectID: id, Purpose: "product", Start: s, NoticeVersion: "v1"})
		for j, feature := range coreActions {
			b.Eligibility = append(b.Eligibility, Eligibility{ID: key("f", i*10+j), SubjectID: id, Feature: feature, Start: s})
		}
		b.Activity = append(b.Activity, Activity{ID: key("a", i*10), SubjectID: id, Occurred: s + 86400, Name: "meal_plan_saved", Platform: "ios", Plan: "free"})
		b.Activity = append(b.Activity, Activity{ID: key("a", i*10+1), SubjectID: id, Occurred: s + 8*86400, Name: "app_active", Platform: "ios", Plan: "free"})
		if i <= n/2 {
			b.Activity = append(b.Activity, Activity{ID: key("a", i*10+2), SubjectID: id, Occurred: s + 8*86400, Name: "meal_plan_saved", Platform: "ios", Plan: "free"})
		}
	}
	for i, source := range sources {
		b.Coverage = append(b.Coverage, Coverage{ID: key("v", i), Source: source, Start: b.ReportingUnix - 90*86400, End: b.ReportingUnix + 1, Status: "complete"})
	}
	return b
}
func importedPopulation(t *testing.T, n int) (*Dataset, Batch) {
	t.Helper()
	d := newTestDataset(t)
	b := populationBatch(n)
	if _, err := d.Import(testContext, b); err != nil {
		t.Fatal(err)
	}
	return d, b
}
func findMetric(t *testing.T, r Report, name, segment string) Metric {
	t.Helper()
	for _, m := range r.Rows {
		if m.Name == name && m.Segment == segment {
			return m
		}
	}
	t.Fatalf("missing metric %s/%s", name, segment)
	return Metric{}
}
func assertCounts(t *testing.T, m Metric, n, d int) {
	t.Helper()
	if m.Value == nil || m.Denominator == nil || *m.Value != n || *m.Denominator != d {
		t.Fatalf("unexpected metric %+v", m)
	}
}
func weekOptions() ReportOptions {
	return ReportOptions{Start: stamp("2026-10-26T00:00:00Z"), End: stamp("2026-11-02T00:00:00Z"), Dimension: "all"}
}

func TestPlanningMetrics(t *testing.T) {
	d, _ := importedPopulation(t, 20)
	r, err := d.Report(testContext, weekOptions())
	if err != nil {
		t.Fatal(err)
	}
	assertCounts(t, findMetric(t, r, "activity", "all"), 20, 20)
	assertCounts(t, findMetric(t, r, "activation", "all"), 20, 20)
	assertCounts(t, findMetric(t, r, "retention", "all"), 10, 20)
	assertCounts(t, findMetric(t, r, "adoption_meal_plan_saved", "all"), 20, 20)
}

func TestSatisfactionInvitationDenominator(t *testing.T) {
	d := newTestDataset(t)
	b := populationBatch(20)
	s := stamp("2026-10-28T12:00:00Z")
	for i := 1; i <= 20; i++ {
		b.Invitations = append(b.Invitations, Invitation{ID: key("i", i), SubjectID: key("s", i), Scope: "app", SurveyVersion: "v1", Invited: s, Start: s, End: s + 86400})
		if i <= 10 {
			inv := key("i", i)
			b.Responses = append(b.Responses, Response{ID: key("r", i), SubjectID: key("s", i), Scope: "app", SurveyVersion: "v1", Occurred: s + 1, Score: 5, InvitationID: &inv})
		}
	}
	// A repeated invitation and response cannot increase the respondent denominator.
	b.Invitations = append(b.Invitations, Invitation{ID: key("i", 21), SubjectID: key("s", 1), Scope: "app", SurveyVersion: "v1", Invited: s, Start: s, End: s + 86400})
	inv := key("i", 21)
	b.Responses = append(b.Responses, Response{ID: key("r", 21), SubjectID: key("s", 1), Scope: "app", SurveyVersion: "v1", Occurred: s + 2, Score: 5, InvitationID: &inv})
	if _, err := d.Import(testContext, b); err != nil {
		t.Fatal(err)
	}
	r, err := d.Report(testContext, weekOptions())
	if err != nil {
		t.Fatal(err)
	}
	assertCounts(t, findMetric(t, r, "satisfaction_app_response_rate", "all"), 10, 20)
	assertCounts(t, findMetric(t, r, "satisfaction_app_score_5", "all"), 10, 10)
}

func TestCommunicationAttributionBoundary(t *testing.T) {
	d := newTestDataset(t)
	b := populationBatch(20)
	tap := stamp("2026-10-28T00:00:00Z")
	for i := 1; i <= 20; i++ {
		for j := 0; j < 2; j++ {
			b.Communications = append(b.Communications, Communication{ID: key("m", i*10+j), SubjectID: key("s", i), OccurrenceID: key("o", i*10+j), Channel: "push", Category: "planning", Status: "tapped", Occurred: tap})
		}
		if i <= 10 {
			b.Activity = append(b.Activity, Activity{ID: key("a", 10000+i), SubjectID: key("s", i), Occurred: tap + 86400, Name: "favorite_saved", Platform: "ios", Plan: "free"})
		}
		if i > 10 {
			b.Activity = append(b.Activity, Activity{ID: key("a", 10000+i), SubjectID: key("s", i), Occurred: tap + 86401, Name: "favorite_saved", Platform: "ios", Plan: "free"})
		}
	}
	if _, err := d.Import(testContext, b); err != nil {
		t.Fatal(err)
	}
	r, err := d.Report(testContext, weekOptions())
	if err != nil {
		t.Fatal(err)
	}
	assertCounts(t, findMetric(t, r, "communication_push_conversion", "all"), 10, 20)
	var occurrence string
	if err := d.db.QueryRow(`SELECT occurrence_id FROM attributed_actions WHERE id=?`, key("a", 10001)).Scan(&occurrence); err != nil || occurrence != key("o", 11) {
		t.Fatalf("tie attribution %s, %v", occurrence, err)
	}
}

func TestSuppressionAndMissingCoverage(t *testing.T) {
	d, _ := importedPopulation(t, 9)
	r, err := d.Report(testContext, weekOptions())
	if err != nil {
		t.Fatal(err)
	}
	m := findMetric(t, r, "activity", "all")
	if m.Status != "suppressed" || m.Value != nil {
		t.Fatalf("leaked small cohort %+v", m)
	}
	d, b := importedPopulation(t, 20)
	b.ID = key("b", 2)
	b.Subjects = nil
	b.Consent = nil
	b.Eligibility = nil
	b.Activity = nil
	b.Coverage = []Coverage{{ID: key("v", 100), Source: "activity", Start: stamp("2026-10-27T00:00:00Z"), End: stamp("2026-10-28T00:00:00Z"), Status: "partial"}}
	if _, err := d.Import(testContext, b); err != nil {
		t.Fatal(err)
	}
	r, err = d.Report(testContext, weekOptions())
	if err != nil {
		t.Fatal(err)
	}
	m = findMetric(t, r, "activation", "all")
	if m.Rate != nil || m.Status == "complete" {
		t.Fatal("invented complete observation")
	}
}

func TestDemographicEffectiveTimeAndComplements(t *testing.T) {
	for _, known := range []int{10, 11} {
		d := newTestDataset(t)
		b := populationBatch(20)
		s := b.Subjects[0].CohortStart
		change := s + 3*86400
		for i := 1; i <= known; i++ {
			b.Consent = append(b.Consent, Consent{ID: key("c", 1000+i), SubjectID: key("s", i), Purpose: "demographics", Start: s, NoticeVersion: "v1"})
			b.Demographics = append(b.Demographics, Demographic{ID: key("d", i*2), SubjectID: key("s", i), Start: s, End: &change, AgeBand: "25_34", Country: "BE", Language: "nl", HouseholdBand: "3_4"})
			b.Demographics = append(b.Demographics, Demographic{ID: key("d", i*2+1), SubjectID: key("s", i), Start: change, AgeBand: "45_54", Country: "BE", Language: "nl", HouseholdBand: "3_4"})
		}
		if _, err := d.Import(testContext, b); err != nil {
			t.Fatal(err)
		}
		o := weekOptions()
		o.Dimension = "age_band"
		r, err := d.Report(testContext, o)
		if err != nil {
			t.Fatal(err)
		}
		activity := findMetric(t, r, "activity", "25_34")
		audience := findMetric(t, r, "audience", "45_54")
		if known == 10 {
			assertCounts(t, activity, 10, 10)
			assertCounts(t, audience, 10, 20)
		} else if activity.Status != "suppressed" || audience.Status != "suppressed" {
			t.Fatal("revealed complementary nine-person group")
		}
		if r.TargetFit != nil {
			t.Fatal("invented target audience")
		}
	}
}

func TestActivationBoundaryEligibilityAndPending(t *testing.T) {
	d := newTestDataset(t)
	b := populationBatch(20)
	s := b.Subjects[0].CohortStart
	for i := range b.Activity {
		if b.Activity[i].Name == "meal_plan_saved" && b.Activity[i].Occurred == s+86400 && b.Activity[i].SubjectID > key("s", 10) {
			b.Activity[i].Occurred = s + 7*86400
		}
	}
	for i := range b.Eligibility {
		if b.Eligibility[i].Feature == "favorite_saved" {
			b.Eligibility[i].Start = s + 2*86400
		}
	}
	for i := 1; i <= 10; i++ {
		b.Activity = append(b.Activity, Activity{ID: key("a", 20000+i), SubjectID: key("s", i), Occurred: s + 3*86400, Name: "favorite_saved", Platform: "web", Plan: "free"})
	}
	if _, err := d.Import(testContext, b); err != nil {
		t.Fatal(err)
	}
	r, err := d.Report(testContext, weekOptions())
	if err != nil {
		t.Fatal(err)
	}
	assertCounts(t, findMetric(t, r, "activation", "all"), 10, 20)
	// Users with a later eligible planning action also belong in the feature denominator.
	assertCounts(t, findMetric(t, r, "adoption_favorite_saved", "all"), 10, 10)
	d = newTestDataset(t)
	b = populationBatch(20)
	for i := range b.Subjects {
		b.Subjects[i].EligibleFrom += 7 * 86400
		b.Subjects[i].CohortStart += 7 * 86400
	}
	for i := range b.Consent {
		b.Consent[i].Start += 7 * 86400
	}
	for i := range b.Eligibility {
		b.Eligibility[i].Start += 7 * 86400
	}
	for i := range b.Activity {
		b.Activity[i].Occurred += 7 * 86400
	}
	// All following-week plans must still be inside the frozen extract.
	if _, err := d.Import(testContext, b); err != nil {
		t.Fatal(err)
	}
	o := weekOptions()
	o.Start += 7 * 86400
	o.End += 7 * 86400
	r, err = d.Report(testContext, o)
	if err != nil {
		t.Fatal(err)
	}
	m := findMetric(t, r, "retention", "all")
	if m.Rate != nil || m.Pending == nil || *m.Pending != 20 {
		t.Fatalf("counted incomplete return window %+v", m)
	}
}

func TestScoreComplementarySuppression(t *testing.T) {
	d := newTestDataset(t)
	b := populationBatch(84)
	at := stamp("2026-10-28T12:00:00Z")
	for i := 1; i <= 84; i++ {
		score := 1
		if i > 4 {
			score = 2 + (i-5)/20
		}
		b.Responses = append(b.Responses, Response{ID: key("r", i), SubjectID: key("s", i), Scope: "app", SurveyVersion: "v1", Occurred: at, Score: score})
	}
	if _, err := d.Import(testContext, b); err != nil {
		t.Fatal(err)
	}
	r, err := d.Report(testContext, weekOptions())
	if err != nil {
		t.Fatal(err)
	}
	for score := 1; score <= 5; score++ {
		m := findMetric(t, r, fmt.Sprintf("satisfaction_app_score_%d", score), "all")
		if m.Value != nil {
			t.Fatal("other score bins reveal the four-person complement")
		}
	}
}

func TestPopulationCountsHaveNoSelfRate(t *testing.T) {
	d, _ := importedPopulation(t, 20)
	r, err := d.Report(testContext, weekOptions())
	if err != nil {
		t.Fatal(err)
	}
	if findMetric(t, r, "activity", "all").Rate != nil {
		t.Fatal("invented a rate for a population count")
	}
	if monthlyMetric(t, d, "2026-10", "activity").Rate != nil {
		t.Fatal("invented a monthly population rate")
	}
}

func TestPlanningReturnMondayBoundaries(t *testing.T) {
	d := newTestDataset(t)
	b := populationBatch(20)
	s := b.Subjects[0].CohortStart
	for i := range b.Activity {
		if b.Activity[i].Name != "meal_plan_saved" {
			continue
		}
		if b.Activity[i].Occurred == s+86400 {
			b.Activity[i].Occurred = stamp("2026-11-01T23:59:59Z")
		} else {
			b.Activity[i].Occurred = stamp("2026-11-02T00:00:00Z")
		}
	}
	for i := 11; i <= 20; i++ {
		b.Activity = append(b.Activity, Activity{ID: key("a", 30000+i), SubjectID: key("s", i), Name: "meal_plan_saved", Occurred: stamp("2026-11-09T00:00:00Z"), Platform: "ios", Plan: "free"})
	}
	// Adjacent grants preserve continuous permission across the observation window.
	join := s + 2*86400
	for i := range b.Consent {
		b.Consent[i].End = &join
		b.Consent = append(b.Consent, Consent{ID: key("c", 2000+i), SubjectID: b.Subjects[i].ID, Purpose: "product", Start: join, NoticeVersion: "v2"})
		b.Subjects[i].NoticeVersion = "v2"
	}
	if _, err := d.Import(testContext, b); err != nil {
		t.Fatal(err)
	}
	r, err := d.Report(testContext, weekOptions())
	if err != nil {
		t.Fatal(err)
	}
	assertCounts(t, findMetric(t, r, "retention", "all"), 10, 20)
}

func TestSatisfactionInvalidInvitationBinding(t *testing.T) {
	for _, fault := range []string{"subject", "version", "scope", "deadline"} {
		d := newTestDataset(t)
		b := populationBatch(20)
		at := stamp("2026-10-28T12:00:00Z")
		inv := key("i", 1)
		b.Invitations = []Invitation{{ID: inv, SubjectID: key("s", 1), Scope: "app", SurveyVersion: "v1", Invited: at, Start: at, End: at + 86400}}
		response := Response{ID: key("r", 1), SubjectID: key("s", 1), Scope: "app", SurveyVersion: "v1", Occurred: at + 1, Score: 5, InvitationID: &inv}
		switch fault {
		case "subject":
			response.SubjectID = key("s", 2)
		case "version":
			response.SurveyVersion = "v2"
		case "scope":
			response.Scope = "recipe"
		case "deadline":
			response.Occurred = at + 86400
		}
		b.Responses = []Response{response}
		if _, err := d.Import(testContext, b); err == nil {
			t.Fatalf("accepted invalid invitation %s", fault)
		}
		if count(t, d, "subjects") != 0 {
			t.Fatal("partial invalid survey import")
		}
	}
}

func TestInvitationCohortCoverageGaps(t *testing.T) {
	for _, edge := range []string{"before", "after"} {
		t.Run(edge, func(t *testing.T) {
			d := newTestDataset(t)
			b := populationBatch(20)
			at := stamp("2026-10-28T12:00:00Z")
			for i := range b.Coverage {
				if b.Coverage[i].Source == "satisfaction_invitations" {
					if edge == "before" {
						b.Coverage[i].Start = at
					} else {
						b.Coverage[i].End = at + 86400
					}
				}
			}
			for i := 1; i <= 20; i++ {
				inv := key("i", i)
				b.Invitations = append(b.Invitations, Invitation{ID: inv, SubjectID: key("s", i), Scope: "app", SurveyVersion: "v1", Invited: at, Start: at, End: at + 86400})
				if i <= 10 {
					b.Responses = append(b.Responses, Response{ID: key("r", i), SubjectID: key("s", i), Scope: "app", SurveyVersion: "v1", Occurred: at + 1, Score: 5, InvitationID: &inv})
				}
			}
			if _, err := d.Import(testContext, b); err != nil {
				t.Fatal(err)
			}
			r, err := d.Report(testContext, weekOptions())
			if err != nil {
				t.Fatal(err)
			}
			m := findMetric(t, r, "satisfaction_app_response_rate", "all")
			if m.Status == "complete" || m.Rate != nil {
				t.Fatal("published a rate with incomplete invitation cohort coverage")
			}
			m = monthlyMetric(t, d, "2026-10", "satisfaction_app_response_rate").Metric
			if m.Status == "complete" || m.Rate != nil {
				t.Fatal("published a monthly rate with incomplete invitation cohort coverage")
			}
		})
	}
}

func TestSameOccurrenceRetapAcrossReportPeriods(t *testing.T) {
	d := newTestDataset(t)
	b := populationBatch(20)
	b.Activity = nil
	first := stamp("2026-10-28T00:00:00Z")
	later := stamp("2026-11-03T00:00:00Z")
	for i := 1; i <= 20; i++ {
		for j, at := range []int64{first, later} {
			b.Communications = append(b.Communications, Communication{ID: key("m", i*10+j), SubjectID: key("s", i), OccurrenceID: key("o", i), Channel: "push", Category: "planning", Status: "tapped", Occurred: at})
		}
		if i <= 10 {
			b.Activity = append(b.Activity, Activity{ID: key("a", i), SubjectID: key("s", i), Occurred: later + 1, Name: "favorite_saved", Platform: "ios", Plan: "free"})
		}
	}
	if _, err := d.Import(testContext, b); err != nil {
		t.Fatal(err)
	}
	r, err := d.Report(testContext, weekOptions())
	if err != nil {
		t.Fatal(err)
	}
	m := findMetric(t, r, "communication_push_conversion", "all")
	if m.Value != nil && *m.Value != 0 {
		t.Fatal("reassigned a later selected tap to an earlier reporting period")
	}
	m = monthlyMetric(t, d, "2026-10", "communication_push_conversion").Metric
	if m.Value != nil && *m.Value != 0 {
		t.Fatal("reassigned a later selected tap to an earlier month")
	}
	o := weekOptions()
	o.Start += 7 * 86400
	o.End += 7 * 86400
	r, err = d.Report(testContext, o)
	if err != nil {
		t.Fatal(err)
	}
	assertCounts(t, findMetric(t, r, "communication_push_conversion", "all"), 10, 20)
	assertCounts(t, monthlyMetric(t, d, "2026-11", "communication_push_conversion").Metric, 10, 20)
}
