package notifications

import (
	"time"
	_ "time/tzdata" // The container must resolve the same IANA zones as development.
)

// Occurrence identifies one local target week and its half-open send window.
type Occurrence struct {
	Week      string
	Scheduled time.Time
	Deadline  time.Time
}

func (o Occurrence) Due(now time.Time) bool {
	return !now.Before(o.Scheduled) && now.Before(o.Deadline)
}

func CurrentWeek(now time.Time, zone *time.Location) Occurrence {
	monday := mondayDate(now.In(zone))
	start := localMinute(monday, 17*60, zone)
	return occurrence(monday, start, localMinute(monday.AddDate(0, 0, 7), 0, zone))
}

func AdvanceWeek(now time.Time, zone *time.Location, day time.Weekday, minute int) Occurrence {
	monday := mondayDate(now.In(zone))
	next := monday.AddDate(0, 0, 7)
	scheduled := monday.AddDate(0, 0, (int(day)+6)%7)
	return occurrence(next, localMinute(scheduled, minute, zone), localMinute(next, 0, zone))
}

func occurrence(week, start, limit time.Time) Occurrence {
	end := start.Add(24 * time.Hour)
	if limit.Before(end) {
		end = limit
	}
	return Occurrence{Week: week.Format(time.DateOnly), Scheduled: start.UTC(), Deadline: end.UTC()}
}

// Dates in UTC here are civil date labels, not the real start of the local day.
func mondayDate(local time.Time) time.Time {
	y, m, d := local.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -((int(local.Weekday()) + 6) % 7))
}

// Check each zone interval near the civil date. When the same wall minute is
// in two intervals choose the earlier instant; for a gap use the next
// representable wall minute, including a date skipped by a zone transition.
func localMinute(date time.Time, minute int, zone *time.Location) time.Time {
	wall := date.Add(time.Duration(minute) * time.Minute)
	from, through := wall.Add(-36*time.Hour), wall.Add(36*time.Hour)
	var best, bestWall time.Time
	for probe := from; probe.Before(through); {
		local := probe.In(zone)
		_, offset := local.Zone()
		_, stop := local.ZoneBounds()
		if stop.IsZero() || stop.After(through) {
			stop = through
		}
		candidate := wall.Add(-time.Duration(offset) * time.Second)
		if candidate.Before(probe) {
			candidate = probe.Truncate(time.Minute)
			if candidate.Before(probe) {
				candidate = candidate.Add(time.Minute)
			}
		}
		if candidate.Before(stop) {
			label := candidate.Add(time.Duration(offset) * time.Second)
			if !label.Before(wall) && (best.IsZero() || label.Before(bestWall) || label.Equal(bestWall) && candidate.Before(best)) {
				best, bestWall = candidate, label
			}
		}
		probe = stop
	}
	return best
}

// OpenWeekAllowed checks a notification's target against current authority,
// never the zone or entitlement recorded when the notification was queued.
func OpenWeekAllowed(now time.Time, zone *time.Location, target string, advance, premium bool) bool {
	if zone == nil {
		return false
	}
	monday := mondayDate(now.In(zone))
	if advance {
		return premium && target == monday.AddDate(0, 0, 7).Format(time.DateOnly)
	}
	return target == monday.Format(time.DateOnly)
}
