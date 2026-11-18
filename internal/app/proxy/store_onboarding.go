package proxy

import (
	"context"
	"fmt"
	"time"
)

// hasRequests reports whether an accepted request has ever been persisted. The
// AUTOINCREMENT high-water mark survives retention deleting every request row.
func (s *store) hasRequests(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var hasRequests bool
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_sequence WHERE name='requests' AND seq>0)").Scan(&hasRequests); err != nil {
		return false, fmt.Errorf("read request completion evidence: %w", err)
	}
	return hasRequests, nil
}
