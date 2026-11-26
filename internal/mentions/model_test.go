package mentions

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func modelTestProviders() []ModelProvider {
	efforts := []string{"low", "medium", "high", "xhigh", "max"}
	return []ModelProvider{
		{ID: "bedrock", Models: []ModelOption{{ID: "claude-opus-4-8", Name: "Opus 4.8"}}},
		{ID: "claude-bin", Models: []ModelOption{
			{ID: "opus", Name: "Opus 5.5", Alias: true, Efforts: efforts},
			{ID: "haiku", Name: "Haiku 5.5", Alias: true, Efforts: efforts},
			{ID: "claude-opus-5-5", Name: "Opus 5.5", Efforts: efforts},
			{ID: "claude-opus-4-8", Name: "Opus 4.8", Efforts: efforts},
			{ID: "claude-haiku-4-5-20251001", Name: "Haiku 4.5"},
		}},
		{ID: "chatgpt", Models: []ModelOption{{ID: "gpt-6.2-sol", Name: "gpt-6.2-sol", Efforts: efforts, DefaultEffort: "low"}}},
		{ID: "unavailable"},
	}
}

func TestActiveModelToken(t *testing.T) {
	providers := modelTestProviders()
	for _, tt := range []struct {
		input, query, reference, effort string
		active, scoped, step            bool
	}{
		{input: "@model:opus", query: "opus", active: true, scoped: true},
		{input: "@M:", active: true, scoped: true},
		{input: "@m:bogus", query: "bogus", active: true, scoped: true},
		{input: "@CL", query: "CL", active: true},
		{input: "@chatgpt:gpt", query: "chatgpt:gpt", active: true},
		{input: "@unavailable:", query: "unavailable:", active: true},
		{input: "@m:claude-bin:opus#me", query: "claude-bin:opus#me", reference: "claude-bin:opus", effort: "me", active: true, scoped: true, step: true},
		{input: "@claude-bin:opus#", query: "claude-bin:opus#", reference: "claude-bin:opus", active: true, step: true},
		{input: "@m:#", query: "#", active: true, scoped: true, step: true},
		{input: "@agent:opus"},
		{input: "@src/main.ts"},
		{input: "@docs:intro"},
		{input: "@c"},
		{input: "@"},
		{input: `@"model:opus`},
		{input: "@./model:opus"},
		{input: "email@m:opus"},
		{input: "@m:opus\u00a0other"},
	} {
		t.Run(tt.input, func(t *testing.T) {
			got, ok := ActiveModelToken(tt.input, len(tt.input), providers)
			if ok != tt.active {
				t.Fatalf("ActiveModelToken = %+v, %t; want active = %t", got, ok, tt.active)
			}
			if !ok {
				return
			}
			want := ModelToken{
				ActiveToken: ActiveToken{End: len(tt.input), Query: tt.query},
				Scoped:      tt.scoped, Reference: tt.reference, EffortQuery: tt.effort, EffortStep: tt.step,
			}
			if got != want || got.Owns() != (tt.scoped || tt.step) {
				t.Errorf("token = %+v (owns %t); want %+v (owns %t)", got, got.Owns(), want, want.Owns())
			}
		})
	}
}

func TestActiveModelTokenReplacement(t *testing.T) {
	providers := modelTestProviders()
	const before = "猫🙂。"
	const after = "\u00a0研究"
	for _, tt := range []struct {
		left, right, insert string
	}{
		{"@m:opus", "-stale", "@model:claude-bin:opus#"},
		{"@model:claude-bin:opus#ME", "xtra", "claude-bin:opus-medium"},
	} {
		text := before + tt.left + tt.right + after
		token, ok := ActiveModelToken(text, len(before+tt.left), providers)
		if !ok || token.Start != len(before) || token.End != len(before+tt.left+tt.right) {
			t.Fatalf("unexpected byte span for %q: %+v, %t", text, token, ok)
		}
		rows := CompleteModels(token, providers)
		if len(rows) == 0 {
			t.Fatalf("no rows for %+v", token)
		}
		if got, want := text[:token.Start]+rows[0].InsertText+text[token.End:], before+tt.insert+after; got != want {
			t.Errorf("replacement = %q; want %q", got, want)
		}
	}
	for _, cursor := range []int{-1, len("@m:") + 1, len("@m:猫") + 1} {
		if token, ok := ActiveModelToken("@m:猫", cursor, providers); ok {
			t.Errorf("accepted invalid byte cursor %d: %+v", cursor, token)
		}
	}
}

func TestIsScopedModelQuery(t *testing.T) {
	for _, query := range []string{"model:", "MoDeL:opus", "M:claude-bin:opus#"} {
		if !IsScopedModelQuery(query) {
			t.Errorf("%q should be reserved", query)
		}
	}
	for _, query := range []string{"model", "agent:opus", "claude-bin:opus", `"model:opus"`, "./model:opus", "../m:opus", "x/model:opus"} {
		if IsScopedModelQuery(query) {
			t.Errorf("%q should not be reserved", query)
		}
	}
}

func TestCompleteModelsRanking(t *testing.T) {
	providers := modelTestProviders()
	for _, tt := range []struct {
		input string
		want  []string
	}{
		{"@m:opus", []string{"claude-bin:opus", "claude-bin:claude-opus-5-5", "claude-bin:claude-opus-4-8", "bedrock:claude-opus-4-8"}},
		{"@m:4.8", []string{"bedrock:claude-opus-4-8", "claude-bin:claude-opus-4-8"}},
		{"@m:CLAUDE-BIN:claude-opus-4-8", []string{"claude-bin:claude-opus-4-8"}},
		{"@cl", []string{"claude-bin:opus", "claude-bin:haiku", "claude-bin:claude-opus-5-5", "claude-bin:claude-opus-4-8", "claude-bin:claude-haiku-4-5-20251001", "bedrock:claude-opus-4-8"}},
		{"@chatgpt:", []string{"chatgpt:gpt-6.2-sol"}},
		{"@m:absent", nil},
	} {
		t.Run(tt.input, func(t *testing.T) {
			got := modelRowLabels(modelTestRows(t, tt.input, providers, providers))
			if !slices.Equal(got, tt.want) {
				t.Errorf("labels = %v; want %v", got, tt.want)
			}
		})
	}
	alias := modelTestRows(t, "@m:opus", providers, providers)[0]
	wantAlias := ModelRow{
		Label: "claude-bin:opus", InsertText: "@model:claude-bin:opus#", Continues: true,
		Description: "Opus 5.5 · effort: low, medium, high, xhigh, max", Badge: "latest",
	}
	if alias != wantAlias {
		t.Errorf("alias row = %+v; want %+v", alias, wantAlias)
	}
	wantPinned := ModelRow{
		Label: "claude-bin:claude-haiku-4-5-20251001", InsertText: "claude-bin:claude-haiku-4-5-20251001", Description: "Haiku 4.5",
	}
	if rows := modelTestRows(t, "@m:haiku-4", providers, providers); len(rows) != 1 || rows[0] != wantPinned {
		t.Errorf("pinned rows = %+v; want %+v", rows, wantPinned)
	}
}

func TestCompleteModelsCapsAndExactMatch(t *testing.T) {
	providers := []ModelProvider{{ID: "other"}, {ID: "chatgpt"}}
	for i := range 20 {
		providers[0].Models = append(providers[0].Models, ModelOption{ID: fmt.Sprintf("chatgpt:%d", i)})
		providers[1].Models = append(providers[1].Models, ModelOption{ID: fmt.Sprintf("target-%d", i), Alias: true})
	}
	providers[1].Models = append(providers[1].Models, ModelOption{ID: "target"})
	for _, tt := range []struct {
		input string
		limit int
	}{
		{"@chatgpt:target", 6},
		{"@model:chatgpt:target", 12},
		{"@m:target", 12},
	} {
		rows := modelTestRows(t, tt.input, providers, providers)
		if len(rows) != tt.limit || rows[0].Label != "chatgpt:target" {
			t.Errorf("%q returned %d rows: %v; want %d with exact match first", tt.input, len(rows), modelRowLabels(rows), tt.limit)
		}
	}
}

func TestCompleteModelsEfforts(t *testing.T) {
	providers := modelTestProviders()
	const ref = "chatgpt:gpt-6.2-sol"
	const marker = "@m:" + ref + "#"
	for _, tt := range []struct {
		input string
		want  []string
	}{
		{marker, []string{ref, ref + "-low", ref + "-medium", ref + "-high", ref + "-xhigh", ref + "-max"}},
		{marker + "dEF", []string{ref}},
		{marker + "ME", []string{ref + "-medium"}},
		{marker + "x", []string{ref + "-xhigh"}},
		{marker + "nope", nil},
		{"@m:chatgpt:missing#", nil},
		{"@m:opus#", nil},
	} {
		t.Run(tt.input, func(t *testing.T) {
			rows := modelTestRows(t, tt.input, providers, providers)
			if got := modelRowLabels(rows); !slices.Equal(got, tt.want) {
				t.Errorf("efforts = %v; want %v", got, tt.want)
			}
			for _, row := range rows {
				wantBadge := ""
				if row.Label == ref+"-low" {
					wantBadge = "default"
				}
				if row.Continues || row.InsertText != row.Label || row.Badge != wantBadge {
					t.Errorf("unexpected effort row: %+v", row)
				}
			}
			if tt.input == marker && len(rows) > 0 && !strings.Contains(rows[0].Description, "default applies") {
				t.Errorf("missing explanation of the no-suffix row: %+v", rows[0])
			}
		})
	}
}

func TestCompleteModelsRespectsCurrentCatalog(t *testing.T) {
	providers := modelTestProviders()
	offered := []ModelProvider{{ID: "claude-bin", Models: []ModelOption{
		{ID: "opus-medium", Name: "Opus 5.5"},
		{ID: "other-high", Efforts: []string{"low"}},
	}}}
	rows := modelTestRows(t, "@m:", providers, offered)
	if want := []string{"claude-bin:opus-medium", "claude-bin:other-high"}; !slices.Equal(modelRowLabels(rows), want) {
		t.Fatalf("offered rows = %v; want %v", modelRowLabels(rows), want)
	}
	if row := rows[0]; row.Continues || row.InsertText != "claude-bin:opus-medium" {
		t.Errorf("normalized pre-suffixed ID should insert verbatim: %+v", row)
	}
	if row := rows[1]; !row.Continues || row.InsertText != "@model:claude-bin:other-high#" {
		t.Errorf("do not assume a suffix-like ID is an effort variant: %+v", row)
	}
	for _, input := range []string{"@chatgpt:gpt-6.2-sol#", "@model:claude-bin:opus#", "@m:claude-bin:opus-medium#"} {
		if rows := modelTestRows(t, input, providers, offered); len(rows) != 0 {
			t.Errorf("unexpected effort rows for %q: %+v", input, rows)
		}
	}
}

func modelTestRows(t *testing.T, input string, recognized, offered []ModelProvider) []ModelRow {
	t.Helper()
	token, ok := ActiveModelToken(input, len(input), recognized)
	if !ok {
		t.Fatalf("no model token for %q", input)
	}
	return CompleteModels(token, offered)
}

func modelRowLabels(rows []ModelRow) []string {
	labels := make([]string, 0, len(rows))
	for _, row := range rows {
		labels = append(labels, row.Label)
	}
	return labels
}

func TestRankAgentMentionNames(t *testing.T) {
	names := []string{"my-code-helper", "coder", "xcode", "code", "c-o-d-e", "codebase", "unrelated"}
	for _, tt := range []struct {
		query string
		limit int
		want  []string
	}{
		{query: "code", want: []string{"code", "coder", "codebase", "xcode", "my-code-helper", "c-o-d-e"}},
		{query: "code", limit: 2, want: []string{"code", "coder"}},
		{query: "xde", want: []string{"xcode"}},
		{query: ""},
		{query: "absent"},
	} {
		t.Run(tt.query, func(t *testing.T) {
			if got := RankAgentMentionNames(names, tt.query, tt.limit); !slices.Equal(got, tt.want) {
				t.Fatalf("RankAgentMentionNames(%q) = %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}
