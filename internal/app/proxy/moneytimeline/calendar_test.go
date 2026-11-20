package moneytimeline

import (
	"testing"
	"time"
)

func TestPreviousWindowMatchesElapsedSpan(t *testing.T) {
	loc, err := time.LoadLocation("Australia/Sydney")
	if err != nil {
		t.Fatal(err)
	}
	at := func(month time.Month, day, hour, minute int) time.Time {
		return time.Date(2026, month, day, hour, minute, 0, 0, loc)
	}
	now := at(time.November, 20, 14, 30)
	for _, tc := range []struct {
		period    string
		wantStart time.Time
		wantEnd   time.Time
	}{
		{"today", at(time.November, 19, 0, 0), at(time.November, 19, 14, 30)},
		{"7d", at(time.November, 7, 0, 0), at(time.November, 13, 14, 30)},
		// The prior period crosses the October DST change, so the elapsed
		// span ends an hour later by the wall clock.
		{"30d", at(time.September, 22, 0, 0), at(time.October, 21, 15, 30)},
	} {
		t.Run(tc.period, func(t *testing.T) {
			chart, err := New(now, loc, tc.period)
			if err != nil {
				t.Fatal(err)
			}
			start, end := chart.PreviousWindow()
			if !start.Equal(tc.wantStart) || !end.Equal(tc.wantEnd) || end.Sub(start) != now.Sub(chart.Start) {
				t.Fatalf("previous window %v–%v, want %v–%v", start, end, tc.wantStart, tc.wantEnd)
			}
		})
	}
}

func TestTodayDSTBucketLabelsAndPriorEnd(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	at := func(month time.Month, day, hour, minute int) time.Time {
		return time.Date(2026, month, day, hour, minute, 0, 0, loc)
	}
	for _, tc := range []struct {
		name     string
		now      time.Time
		buckets  int
		labels   [2]string // Buckets after the midnight bucket.
		priorEnd time.Time
	}{
		{"spring forward", at(time.March, 8, 23, 30), 23,
			[2]string{"2026-03-08 01:00 -0500", "2026-03-08 03:00 -0400"}, at(time.March, 7, 22, 30)},
		{"fall back capped at current start", at(time.November, 1, 23, 30), 25,
			[2]string{"2026-11-01 01:00 -0400", "2026-11-01 01:00 -0500"}, at(time.November, 1, 0, 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chart, err := New(tc.now, loc, "today")
			if err != nil {
				t.Fatal(err)
			}
			if len(chart.Buckets) != tc.buckets || chart.Buckets[1].Label != tc.labels[0] || chart.Buckets[2].Label != tc.labels[1] {
				t.Fatalf("unexpected hourly buckets: %+v", chart.Buckets)
			}
			for i, b := range chart.Buckets {
				if got := chart.BucketIndex(b.Start); got != i {
					t.Fatalf("bucket start %v assigned to %d, want %d", b.Start, got, i)
				}
			}
			_, end := chart.PreviousWindow()
			if !end.Equal(tc.priorEnd) {
				t.Fatalf("prior end %v, want %v", end, tc.priorEnd)
			}
		})
	}
}

func TestBucketIndexIncludesCurrentEnd(t *testing.T) {
	start := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	now := start.Add(2 * time.Hour)
	chart, err := New(now, time.UTC, "today")
	if err != nil {
		t.Fatal(err)
	}
	if len(chart.Buckets) != 2 {
		t.Fatalf("unexpected buckets at an exact hour: %+v", chart.Buckets)
	}
	for _, tc := range []struct {
		stamp time.Time
		want  int
	}{
		{start.Add(-time.Nanosecond), -1}, {start, 0},
		{start.Add(time.Hour - time.Nanosecond), 0}, {start.Add(time.Hour), 1},
		{now, 1}, {now.Add(time.Nanosecond), -1},
	} {
		if got := chart.BucketIndex(tc.stamp); got != tc.want {
			t.Errorf("bucket for %v = %d, want %d", tc.stamp, got, tc.want)
		}
	}
}
