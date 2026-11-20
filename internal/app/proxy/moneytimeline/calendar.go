package moneytimeline

import (
	"fmt"
	"sort"
	"time"
)

// New builds the current chart's local calendar and empty response.
func New(now time.Time, loc *time.Location, period string) (Response, error) {
	days := rangeDays(period)
	if days == 0 {
		return Response{}, fmt.Errorf("invalid range")
	}
	local := now.In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	if days > 1 {
		start = start.AddDate(0, 0, 1-days)
	}
	result := Response{Timezone: loc.String(), Range: period, Start: start, End: now, Currencies: []string{}}
	for from := start; ; {
		var to time.Time
		var label string
		if days == 1 {
			to = from.Add(time.Hour)
			// The offset distinguishes the repeated hour on a fall-back day.
			label = from.Format("2006-01-02 15:00 -0700")
		} else {
			to = from.AddDate(0, 0, 1)
			label = from.Format("2006-01-02")
		}
		last := !to.Before(now)
		if last {
			to = now
			if to.Before(from) {
				to = from
			}
		}
		result.Buckets = append(result.Buckets, Bucket{Start: from, End: to, Label: label, Series: []Series{}})
		if last {
			return result, nil
		}
		from = to
	}
}

// BucketIndex assigns a timestamp to a half-open bucket, including the chart's
// end in its final bucket. It returns -1 outside the chart.
func (r Response) BucketIndex(t time.Time) int {
	if t.After(r.End) {
		return -1
	}
	return sort.Search(len(r.Buckets), func(i int) bool { return r.Buckets[i].Start.After(t) }) - 1
}

// PreviousWindow is the comparable elapsed span of the preceding local
// calendar period. Its end is exclusive and capped at the current start so
// that a shorter preceding period (e.g. due to DST) cannot overlap the chart.
func (r Response) PreviousWindow() (start, end time.Time) {
	start = r.Start.AddDate(0, 0, -rangeDays(r.Range))
	end = start.Add(r.End.Sub(r.Start))
	if end.After(r.Start) {
		end = r.Start
	}
	return start, end
}

func ValidRange(period string) bool { return rangeDays(period) != 0 }

func rangeDays(period string) int {
	switch period {
	case "today":
		return 1
	case "7d":
		return 7
	case "30d":
		return 30
	default:
		return 0
	}
}
