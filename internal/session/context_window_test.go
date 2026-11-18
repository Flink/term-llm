package session

import (
	"context"
	"testing"
)

func TestContextWindowPersistence(t *testing.T) {
	ctx := context.Background()
	store := newContextStateTestStore(t)
	sess := &Session{ID: NewID(), Provider: "provider", Model: "model", LastTotalTokens: 11, LastMessageCount: 2, LastContextWindow: 100}
	if err := store.Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	check := func(total, count, window int) {
		t.Helper()
		got, err := store.Get(ctx, sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.LastTotalTokens != total || got.LastMessageCount != count || got.LastContextWindow != window {
			t.Fatalf("estimate = (%d, %d, %d), want (%d, %d, %d)", got.LastTotalTokens, got.LastMessageCount, got.LastContextWindow, total, count, window)
		}
	}
	check(11, 2, 100)
	wrapped := NewLoggingStore(store, nil)
	for _, change := range []func(){func() { sess.Provider = "changed" }, func() { sess.ProviderKey = "alias" }, func() { sess.Model = "new-model" }} {
		if err := wrapped.UpdateContextEstimate(ctx, sess.ID, 14, 3, 200); err != nil {
			t.Fatal(err)
		}
		// Ordinary metadata writes must not replace a newer measurement with a stale snapshot.
		sess.Summary = "updated"
		if err := store.Update(ctx, sess); err != nil {
			t.Fatal(err)
		}
		check(14, 3, 200)
		change()
		if err := store.Update(ctx, sess); err != nil {
			t.Fatal(err)
		}
		check(14, 3, 0)
	}
	if err := store.UpdateContextEstimate(ctx, sess.ID, 15, 4, 300); err != nil {
		t.Fatal(err)
	}
	// A model swap can replace the identity and its reading in one write.
	sess.Model = "target"
	sess.LastTotalTokens, sess.LastMessageCount, sess.LastContextWindow = 16, 5, 400
	if err := wrapped.UpdateWithContextEstimate(ctx, sess); err != nil {
		t.Fatal(err)
	}
	check(16, 5, 400)
}
