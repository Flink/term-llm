package session

import (
	"context"
	"time"
)

// PersistContextEstimate saves one engine snapshot and keeps the live session
// metadata in sync with the successful write.
func PersistContextEstimate(ctx context.Context, store Store, sess *Session, total, count, window int) error {
	if err := store.UpdateContextEstimate(ctx, sess.ID, total, count, window); err != nil {
		return err
	}
	sess.LastTotalTokens, sess.LastMessageCount, sess.LastContextWindow = total, count, window
	return nil
}

func (s *LoggingStore) UpdateWithContextEstimate(ctx context.Context, sess *Session) error {
	err := s.Store.UpdateWithContextEstimate(ctx, sess)
	s.logOnce("UpdateWithContextEstimate", err)
	return err
}

func (s *SQLiteStore) UpdateWithContextEstimate(ctx context.Context, sess *Session) error {
	return s.update(ctx, sess, true)
}

func (s *SQLiteStore) contextWindowColumn() string {
	if s.hasLastContextWindow {
		return "last_context_window"
	}
	return "0 AS last_context_window"
}

// UpdateContextEstimate replaces the token count and matching window together.
func (s *SQLiteStore) UpdateContextEstimate(ctx context.Context, id string, lastTotalTokens, lastMessageCount, window int) error {
	if !s.hasLastTotalTokens || !s.hasLastMessageCount {
		return nil
	}
	return retryOnBusy(ctx, 5, func() error {
		_, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_total_tokens = ?, last_message_count = ?, last_context_window = ?, updated_at = ? WHERE id = ?`, max(0, lastTotalTokens), max(0, lastMessageCount), max(0, window), time.Now(), id)
		return err
	})
}
