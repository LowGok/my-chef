package notifications

import (
	"testing"
	"time"
)

func TestCurrentWeekNudgeAtMonday1700Local(t *testing.T) {
	for _, tc := range []struct {
		zone, before, due, last, late, monday string
	}{
		{"Europe/Brussels", "2027-03-29T14:59:59Z", "2027-03-29T15:00:00Z", "2027-03-30T14:59:59Z", "2027-03-30T15:00:00Z", "2027-03-29"},
		{"Asia/Kolkata", "2026-12-28T11:29:59Z", "2026-12-28T11:30:00Z", "2026-12-29T11:29:59Z", "2026-12-29T11:30:00Z", "2026-12-28"},
		{"Pacific/Honolulu", "2026-12-29T02:59:59Z", "2026-12-29T03:00:00Z", "2026-12-30T02:59:59Z", "2026-12-30T03:00:00Z", "2026-12-28"},
	} {
		t.Run(tc.zone, func(t *testing.T) {
			zone, err := time.LoadLocation(tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			due := testInstant(t, tc.due)
			got := CurrentWeek(due, zone)
			if !got.Scheduled.Equal(due) || got.Week != tc.monday || !got.Deadline.Equal(due.Add(24*time.Hour)) {
				t.Fatalf("wrong local occurrence: %+v", got)
			}
			for _, point := range []struct {
				at   string
				want bool
			}{{tc.before, false}, {tc.due, true}, {tc.last, true}, {tc.late, false}} {
				if got.Due(testInstant(t, point.at)) != point.want {
					t.Fatalf("wrong eligibility at %s", point.at)
				}
			}
		})
	}
}

func TestAdvanceCivilTimeUsesFirstValidMinuteAndEarlierFold(t *testing.T) {
	zone, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		at, scheduled, week string
	}{
		{"2027-03-28T01:00:00Z", "2027-03-28T01:00:00Z", "2027-03-29"},
		{"2027-10-31T00:30:00Z", "2027-10-31T00:30:00Z", "2027-11-01"},
		{"2026-12-27T01:30:00Z", "2026-12-27T01:30:00Z", "2026-12-28"},
	} {
		at := testInstant(t, tc.at)
		got := AdvanceWeek(at, zone, time.Sunday, 2*60+30)
		if got.Week != tc.week || !got.Scheduled.Equal(testInstant(t, tc.scheduled)) ||
			got.Deadline.In(zone).Format("2006-01-02 15:04") != tc.week+" 00:00" || got.Due(got.Deadline) {
			t.Fatalf("wrong advance occurrence: %+v", got)
		}
	}
}

func testInstant(t *testing.T, input string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, input)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func TestTargetWeekUsesCurrentConfirmedZone(t *testing.T) {
	east := time.FixedZone("east", 14*3600)
	west := time.FixedZone("west", -12*3600)
	now := testInstant(t, "2026-12-28T03:00:01Z")
	sent := CurrentWeek(now, east)
	if !sent.Due(now) {
		t.Fatal("fixture did not create a due nudge")
	}
	if !OpenWeekAllowed(now, east, sent.Week, false, false) {
		t.Fatal("valid current-week nudge refused")
	}
	if OpenWeekAllowed(now, west, sent.Week, false, false) {
		t.Fatal("timezone change allowed a future week from a current-week nudge")
	}
	next := AdvanceWeek(now, east, time.Monday, 17*60)
	if OpenWeekAllowed(now, east, next.Week, true, false) || !OpenWeekAllowed(now, east, next.Week, true, true) {
		t.Fatal("advance authorization did not follow current entitlement")
	}
}
