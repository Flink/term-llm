package mentions

import (
	"cmp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/term-llm/term-llm/internal/llm/core"
)

const (
	modelQueryPrefix      = "model:"
	shortModelQueryPrefix = "m:"
	modelEffortSeparator  = "#"
	scopedModelLimit      = 12
	looseModelLimit       = 6
)

// ModelProvider is an offered provider and its currently available models.
// An empty catalog still allows recognition of bare provider queries.
type ModelProvider struct {
	ID     string
	Models []ModelOption
}

// ModelOption is a normalized catalog entry. Already-suffixed IDs have no
// Efforts and are inserted verbatim; the completer does not interpret IDs.
type ModelOption = core.ModelMentionOption

// ModelRow describes a suggestion. Continues keeps the token open for an
// effort choice; InsertText does not include any trailing space.
type ModelRow struct {
	Label, Description, Badge, InsertText string
	Continues                             bool
}

// ModelToken has a full byte replacement span and a query up to the caret,
// excluding the @ and any explicit model namespace.
type ModelToken struct {
	ActiveToken
	Scoped      bool
	Reference   string
	EffortQuery string
	EffortStep  bool
}

// Owns reports whether the model token should suppress other mention sources.
func (token ModelToken) Owns() bool {
	return token.Scoped || token.EffortStep
}

// IsScopedModelQuery reports whether an unquoted mention payload (without @)
// starts with a reserved model namespace. Callers should check quoting before
// passing a decoded path, so a quoted file named model:… is not suppressed.
func IsScopedModelQuery(query string) bool {
	return modelQueryPrefixLen(query) != 0
}

func modelQueryPrefixLen(query string) int {
	for _, prefix := range [...]string{modelQueryPrefix, shortModelQueryPrefix} {
		if len(query) >= len(prefix) && strings.EqualFold(query[:len(prefix)], prefix) {
			return len(prefix)
		}
	}
	return 0
}

// ActiveModelToken recognizes scoped mentions and bare provider queries at a
// byte cursor. It matches only the part before the cursor but replaces the
// whole token, including any untyped query suffix to the right of the cursor.
func ActiveModelToken(text string, cursor int, providers []ModelProvider) (ModelToken, bool) {
	active, ok := ActiveTokenAt(text, cursor)
	if !ok || active.Quoted || active.Agent || strings.ContainsFunc(active.Query, unicode.IsSpace) {
		return ModelToken{}, false
	}
	if cursor < len(text) && !utf8.RuneStart(text[cursor]) {
		return ModelToken{}, false
	}
	prefix := modelQueryPrefixLen(active.Query)
	active.Query = active.Query[prefix:]
	if prefix == 0 && !looksLikeModelQuery(active.Query, providers) {
		return ModelToken{}, false
	}
	active.End = len(text)
	if end := strings.IndexFunc(text[cursor:], func(r rune) bool { return unicode.IsSpace(r) || r == '"' }); end >= 0 {
		active.End = cursor + end
	}
	token := ModelToken{ActiveToken: active, Scoped: prefix != 0}
	if reference, effort, ok := strings.Cut(active.Query, modelEffortSeparator); ok {
		token.Reference, token.EffortQuery, token.EffortStep = reference, effort, true
	}
	return token, true
}

func looksLikeModelQuery(query string, providers []ModelProvider) bool {
	providerID, _, qualified := strings.Cut(strings.ToLower(query), ":")
	for _, provider := range providers {
		id := strings.ToLower(provider.ID)
		if qualified && id == providerID || !qualified && utf8.RuneCountInString(providerID) >= 2 && strings.HasPrefix(id, providerID) {
			return true
		}
	}
	return false
}

type rankedModel struct {
	provider  string
	reference string
	option    ModelOption
	rank      int
}

// CompleteModels ranks suggestions from the offered catalogs or lists the
// valid effort choices for an exact reference in an open effort step.
func CompleteModels(token ModelToken, providers []ModelProvider) []ModelRow {
	if token.EffortStep {
		return completeModelEfforts(token, providers)
	}
	query := strings.ToLower(token.Query)
	var ranked []rankedModel
	bestRank := make(map[string]int, len(providers))
	for _, provider := range providers {
		for _, model := range provider.Models {
			reference := provider.ID + ":" + model.ID
			rank := modelMatchRank(query, reference, model)
			if rank < 0 {
				continue
			}
			if best, found := bestRank[provider.ID]; !found || rank < best {
				bestRank[provider.ID] = rank
			}
			ranked = append(ranked, rankedModel{provider.ID, reference, model, rank})
		}
	}
	// Keep the web ordering: best provider rank, then individual rank,
	// then catalog order. Exact matches sort before other rows and the cap.
	slices.SortStableFunc(ranked, func(a, b rankedModel) int {
		return cmp.Or(cmp.Compare(bestRank[a.provider], bestRank[b.provider]), cmp.Compare(a.rank, b.rank))
	})
	limit := looseModelLimit
	if token.Scoped {
		limit = scopedModelLimit
	}
	limit = min(limit, len(ranked))
	rows := make([]ModelRow, 0, limit)
	for _, result := range ranked[:limit] {
		rows = append(rows, modelSuggestion(result.reference, result.option))
	}
	return rows
}

func modelMatchRank(query, reference string, model ModelOption) int {
	id, ref := strings.ToLower(model.ID), strings.ToLower(reference)
	aliasOffset := 0
	if !model.Alias {
		aliasOffset = 1
	}
	switch {
	case query == "":
		return aliasOffset
	case id == query || ref == query:
		return 0
	case strings.HasPrefix(id, query) || strings.HasPrefix(ref, query):
		return 1 + aliasOffset
	case strings.Contains(ref, query):
		return 3 + aliasOffset
	case strings.Contains(strings.ToLower(model.Name), query):
		return 5
	default:
		return -1
	}
}

func modelSuggestion(reference string, model ModelOption) ModelRow {
	row := ModelRow{Label: reference, InsertText: reference}
	if model.Alias {
		row.Badge = "latest"
	}
	if model.Name != model.ID {
		row.Description = model.Name
	}
	if len(model.Efforts) != 0 {
		if row.Description != "" {
			row.Description += " · "
		}
		row.Description += "effort: " + strings.Join(model.Efforts, ", ")
		row.InsertText = "@" + modelQueryPrefix + reference + modelEffortSeparator
		row.Continues = true
	}
	return row
}

func completeModelEfforts(token ModelToken, providers []ModelProvider) []ModelRow {
	providerID, modelID, qualified := strings.Cut(token.Reference, ":")
	if !qualified {
		return nil
	}
	for _, provider := range providers {
		if !strings.EqualFold(provider.ID, providerID) {
			continue
		}
		for _, model := range provider.Models {
			if strings.EqualFold(model.ID, modelID) && len(model.Efforts) != 0 {
				return modelEffortRows(provider.ID+":"+model.ID, strings.ToLower(token.EffortQuery), model)
			}
		}
	}
	return nil
}

func modelEffortRows(reference, query string, model ModelOption) []ModelRow {
	var rows []ModelRow
	if strings.HasPrefix("default", query) {
		rows = append(rows, ModelRow{
			Label: reference, InsertText: reference,
			Description: "No effort suffix: the agent or provider default applies",
		})
	}
	for _, effort := range model.Efforts {
		if !strings.HasPrefix(strings.ToLower(effort), query) {
			continue
		}
		value := reference + "-" + effort
		row := ModelRow{Label: value, InsertText: value, Description: effort + " effort"}
		if effort == model.DefaultEffort {
			row.Badge = "default"
		}
		rows = append(rows, row)
	}
	return rows
}
