package proxy

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/term-llm/term-llm/internal/app/proxy/moneytimeline"
)

// timelinePrevious totals the prior window without grouping. It returns nil
// unless retained history reaches its start, so an unrecorded past isn't
// treated as a period with zero usage.
func (s *Service) timelinePrevious(ctx context.Context, chart moneytimeline.Response) (*moneytimeline.Series, error) {
	start, end := chart.PreviousWindow()
	if !end.After(start) {
		return nil, nil
	}
	// The prior window is half-open. Convert its exclusive end to the last
	// stored millisecond before it, including when end falls between ticks.
	from, through := start.UnixMilli(), end.Add(-time.Nanosecond).UnixMilli()
	if err := s.store.reconcileMoneyHistory(ctx, from, through); err != nil {
		return nil, fmt.Errorf("reconcile previous timeline: %w", err)
	}
	var earliest sql.NullInt64
	if err := s.store.db.QueryRowContext(ctx, "SELECT MIN(started) FROM money_history").Scan(&earliest); err != nil {
		return nil, fmt.Errorf("read timeline history coverage: %w", err)
	}
	if !earliest.Valid || earliest.Int64 > from {
		return nil, nil
	}
	total := newTimelineAggregate("Total")
	currencySet := map[string]bool{}
	if err := s.store.streamTimelineRows(ctx, from, through, func(_ int64, _, _ string, rec MoneyRecord) {
		accumulateTimelineRecord(total, rec, currencySet)
	}); err != nil {
		return nil, fmt.Errorf("read previous timeline: %w", err)
	}
	series := timelineSeriesFromAggregate("total", total, sortedTimelineCurrencies(currencySet))
	return &series, nil
}
