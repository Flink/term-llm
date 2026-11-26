package chat

import (
	"cmp"
	"context"
	"strings"

	"github.com/term-llm/term-llm/internal/session"
)

type sessionContextIdentity struct {
	savedProvider string
	savedModel    string
	provider      string
	model         string
}

func (m *Model) sessionContextIdentity() sessionContextIdentity {
	savedProvider := cmp.Or(strings.TrimSpace(m.sess.ProviderKey), strings.TrimSpace(m.sess.Provider))
	return sessionContextIdentity{
		savedProvider: savedProvider,
		savedModel:    strings.TrimSpace(m.sess.Model),
		provider:      cmp.Or(strings.TrimSpace(m.providerKey), savedProvider),
		model:         strings.TrimSpace(m.modelName), // A blank selector can mean the CLI default, not the saved model.
	}
}

func (m *Model) managesOwnContext() bool {
	return m != nil && m.provider != nil && m.provider.Capabilities().ManagesOwnContext
}

func (m *Model) configureContextManagementForSession() {
	if m == nil || m.engine == nil || m.provider == nil || m.config == nil || m.sess == nil {
		return
	}

	id := m.sessionContextIdentity()
	managed := m.managesOwnContext()
	if id.model == "" && !managed {
		return
	}
	m.engine.ConfigureContextManagement(m.provider, id.provider, id.model, m.config.AutoCompact)
	if m.contextSessionID == m.sess.ID {
		return
	}
	// A different session may use the exact same provider and model. Discard
	// the outgoing reading, but never replace a live one on per-turn configure.
	previous := m.contextSessionID
	m.contextSessionID = m.sess.ID
	if previous != "" {
		m.engine.SetContextEstimateBaseline(0, 0)
	} else if total, _, _ := m.engine.ContextEstimateSnapshot(); total > 0 {
		// Initial attachment can receive an engine that is already live.
		return
	}
	if !managed || id.savedProvider == id.provider {
		m.engine.SetContextEstimateBaseline(m.sess.LastTotalTokens, m.sess.LastMessageCount)
		m.engine.RestoreContextWindow(id.savedProvider, id.savedModel, m.sess.LastContextWindow)
	}
}

func (m *Model) persistContextEstimate(ctx context.Context) {
	if m == nil || m.store == nil || m.sess == nil || m.engine == nil {
		return
	}
	total, count, window := m.engine.ContextEstimateSnapshot()
	if total == 0 && m.sess.LastTotalTokens == 0 && m.sess.LastMessageCount == 0 && m.sess.LastContextWindow == 0 {
		return
	}
	// Totals follow the transcript, but the window belongs to the active model.
	id := m.sessionContextIdentity()
	if id.savedProvider != id.provider || id.savedModel != id.model {
		window = 0
	}
	_ = session.PersistContextEstimate(ctx, m.store, m.sess, total, count, window)
}

func (m *Model) resetContextEstimateBaseline(ctx context.Context) {
	if m == nil {
		return
	}
	if m.engine != nil {
		m.engine.SetContextEstimateBaseline(0, 0)
	}
	if m.sess == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if m.store != nil && (m.sess.LastTotalTokens != 0 || m.sess.LastMessageCount != 0 || m.sess.LastContextWindow != 0) {
		_ = session.ResetContextEstimate(ctx, m.store, m.sess)
	}
	m.sess.LastTotalTokens = 0
	m.sess.LastMessageCount = 0
	m.sess.LastContextWindow = 0
}
