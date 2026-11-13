package analyticsdataset

import (
	"fmt"
	"time"
)

const DefaultReportingDate = "2026-11-13T00:00:00Z"

func fixtureID(kind string, n int) string { return fmt.Sprintf("%s_%016x", kind, n) }

func monday(t time.Time) time.Time {
	t = t.UTC()
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return day.AddDate(0, 0, -(int(day.Weekday())+6)%7)
}

// simulatedActions supplies the same synthetic population for raw and old monthly data.
func simulatedActions(subject string, index int, month time.Time, eligible, until int64) []Activity {
	if (index+int(month.Month()))%4 == 0 {
		return nil
	}
	var events []Activity
	for j, day := range []int{3, 10, 17, 24} {
		t := month.AddDate(0, 0, day-1).Add(10 * time.Hour).Unix()
		if t < eligible || t > until {
			continue
		}
		name := "meal_plan_saved"
		if j%2 == 1 {
			name = "shopping_list_edited"
		}
		events = append(events, Activity{ID: derivedID(fmt.Sprintf("%s/%s/%d", subject, name, t)), SubjectID: subject, Occurred: t, Name: name, Platform: "web", Plan: "free"})
	}
	return events
}

func BuildFixture(reference int64) (Batch, error) {
	if !validTime(reference) || reference < 1009843200 {
		return Batch{}, ErrInput
	}
	r := time.Unix(reference, 0).UTC()
	first := monthStart(reference).AddDate(0, -12, 0)
	cohort := monday(r).AddDate(0, 0, -14).Add(time.Hour).Unix()
	b := Batch{Version: 1, Environment: "synthetic", ExtractID: fmt.Sprintf("x_%016x", reference), ReportingUnix: reference, ID: fmt.Sprintf("b_%016x", reference)}
	for i := 1; i <= 1000; i++ {
		id := fixtureID("s", i)
		start := cohort
		if i <= 500 {
			start = first.Unix()
		}
		b.Subjects = append(b.Subjects, Subject{ID: id, EligibleFrom: start, CohortStart: start, NoticeVersion: "v1"})
		b.Consent = append(b.Consent, Consent{ID: fixtureID("c", i), SubjectID: id, Purpose: "product", Start: start, NoticeVersion: "v1"})
		for j, feature := range coreActions {
			b.Eligibility = append(b.Eligibility, Eligibility{ID: fixtureID("f", i*10+j), SubjectID: id, Feature: feature, Start: start})
		}
		if i <= 500 {
			for month := first; !month.After(monthStart(reference)); month = month.AddDate(0, 1, 0) {
				events := simulatedActions(id, i, month, start, min(reference, month.AddDate(0, 1, 0).Unix()-1))
				if month.Unix() < reference-90*86400 && month.Before(monthStart(reference)) && len(events) > 0 {
					b.Contributions = append(b.Contributions, Contribution{ID: derivedID(id + "/historical/" + month.Format("2006-01")), SubjectID: id, Month: month.Format("2006-01"), Metric: "activity", Dimension: "all", Segment: "all", Numerator: 1, Denominator: 1, QueryVersion: 1, Permission: true, Coverage: "complete", ObservationStatus: "eligible"})
				}
				for _, event := range events {
					if event.Occurred >= reference-90*86400 {
						b.Activity = append(b.Activity, event)
					}
				}
			}
		} else {
			b.Activity = append(b.Activity, Activity{ID: fixtureID("a", i*10), SubjectID: id, Occurred: cohort + 86400, Name: "meal_plan_saved", Platform: "ios", Plan: "free"})
			if i%2 == 0 {
				b.Activity = append(b.Activity, Activity{ID: fixtureID("a", i*10+1), SubjectID: id, Occurred: cohort + 8*86400, Name: "meal_plan_saved", Platform: "ios", Plan: "free"})
			}
		}
		b.Activity = append(b.Activity, Activity{ID: fixtureID("a", i*10+2), SubjectID: id, Occurred: cohort + 8*86400, Name: "app_active", Platform: "ios", Plan: "free"})
		invited := cohort + 2*86400 + 11*3600
		end := invited + 2*86400
		inv := fixtureID("i", i)
		b.Invitations = append(b.Invitations, Invitation{ID: inv, SubjectID: id, Scope: "app", SurveyVersion: "v1", Invited: invited, Start: invited, End: end})
		if i%5 != 0 {
			b.Responses = append(b.Responses, Response{ID: fixtureID("r", i), SubjectID: id, Scope: "app", SurveyVersion: "v1", Occurred: invited + 60, Score: 1 + (i/5)%5, InvitationID: &inv})
		}
		if i%3 == 0 {
			b.Responses = append(b.Responses, Response{ID: fixtureID("r", 1000+i), SubjectID: id, Scope: "recipe", SurveyVersion: "v1", Occurred: invited + 120, Score: 1 + (i/3)%5})
		}
		if i <= 19 || i%10 != 0 {
			b.Consent = append(b.Consent, Consent{ID: fixtureID("c", 1000+i), SubjectID: id, Purpose: "demographics", Start: start, NoticeVersion: "v1"})
			country := "BE"
			if i <= 9 {
				country = "DE"
			} else if i <= 19 {
				country = "GB"
			}
			age := ageBands[i%6]
			if i%13 == 0 {
				age = "prefer_not_to_answer"
			}
			b.Demographics = append(b.Demographics, Demographic{ID: fixtureID("d", i), SubjectID: id, Start: start, AgeBand: age, Country: country, Language: languages[i%3], HouseholdBand: householdBands[i%len(householdBands)]})
		}
		addFixtureCommunications(&b, i, id, cohort)
	}
	for i, source := range sources {
		b.Coverage = append(b.Coverage, Coverage{ID: fixtureID("v", i), Source: source, Start: reference - 90*86400, End: reference + 1, Status: "complete"})
	}
	return b, nil
}

func addFixtureCommunications(b *Batch, index int, subject string, cohort int64) {
	tap := cohort + 2*86400 - 3600
	occurrence := fixtureID("o", index)
	channel := "push"
	if index%2 == 0 {
		channel = "email"
	}
	for j, status := range []string{"queued", "accepted", "delivered"} {
		if status == "delivered" && index%10 == 0 {
			continue
		}
		b.Communications = append(b.Communications, Communication{ID: fixtureID("m", index*10+j), SubjectID: subject, OccurrenceID: occurrence, Channel: channel, Category: "planning", Status: status, Occurred: tap - int64(100-j)})
	}
	if index%3 != 0 {
		b.Communications = append(b.Communications, Communication{ID: fixtureID("m", index*10+3), SubjectID: subject, OccurrenceID: occurrence, Channel: channel, Category: "planning", Status: "tapped", Occurred: tap})
	}
	if index%4 < 2 {
		b.Activity = append(b.Activity, Activity{ID: fixtureID("a", index*10+3), SubjectID: subject, Occurred: tap + 3600, Name: "favorite_saved", Platform: "ios", Plan: "free"})
	}
}
