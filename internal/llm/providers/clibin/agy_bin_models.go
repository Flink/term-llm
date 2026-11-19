package clibin

import (
	"context"
	"errors"
	"fmt"
	"github.com/term-llm/term-llm/internal/llm/core"
	"os"
	"path/filepath"
	"strings"

	"github.com/term-llm/term-llm/internal/cache"
)

const agyBinModelCacheKey = "agy-bin"

// RefreshAgyBinModelsIfStale refreshes the on-disk model catalog when it is
// missing or older than the shared model-cache TTL. A failed refresh leaves any
// stale cache available as a completion fallback.
func RefreshAgyBinModelsIfStale(ctx context.Context, model string, env map[string]string) error {
	cached, err := cache.ReadModelCache(agyBinModelCacheKey)
	if !cliProxyRequested(env, agyProxyRootEnv) && err == nil && cache.IsCacheValid(cached) && (len(cached.ModelInfos) > 0 || len(cached.Models) > 0) {
		return nil
	}
	_, err = NewAgyBinProvider(model, env).ListModels(ctx)
	return err
}

// GetCachedAgyBinModels returns the last live model list fetched from the agy
// CLI. It never spawns agy, so it is safe for shell completion and provider
// pickers. Run `term-llm models --provider agy-bin` to refresh it.
func GetCachedAgyBinModels() []string {
	models := GetCachedAgyBinModelInfos()
	ids := make([]string, 0, len(models))
	for _, model := range models {
		if model.ID != "" {
			ids = append(ids, model.ID)
		}
	}
	return ids
}

// agyEffortLevels lists the reasoning-effort levels supported by `agy --effort`
// and encoded as model-ID suffixes in `agy models`, in canonical display order.
var agyEffortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// parseAgyEffort splits a trailing effort suffix from an agy model ID:
// "gemini-4-neon-xhigh" -> ("gemini-4-neon", "xhigh"),
// "claude-opus-5-5-high" -> ("claude-opus-5-5", "high"),
// "gemini-pro-orchestrator-neon" -> ("gemini-pro-orchestrator-neon", "").
func parseAgyEffort(model string) (string, string) {
	model = strings.TrimSpace(model)
	lower := strings.ToLower(model)
	for _, effort := range agyEffortLevels {
		suffix := "-" + effort
		if strings.HasSuffix(lower, suffix) && len(model) > len(suffix) {
			return model[:len(model)-len(suffix)], effort
		}
	}
	return model, ""
}

func normalizeAgyEffort(effort string) string {
	effort = strings.ToLower(strings.TrimSpace(effort))
	switch effort {
	case "", "default", "auto":
		return ""
	default:
		return effort
	}
}

func sortAgyEfforts(efforts []string) []string {
	if len(efforts) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(efforts))
	for _, e := range efforts {
		if norm := normalizeAgyEffort(e); norm != "" {
			seen[norm] = true
		}
	}
	ordered := make([]string, 0, len(seen))
	for _, level := range agyEffortLevels {
		if seen[level] {
			ordered = append(ordered, level)
			delete(seen, level)
		}
	}
	for _, e := range efforts {
		norm := normalizeAgyEffort(e)
		if norm != "" && seen[norm] {
			ordered = append(ordered, norm)
			delete(seen, norm)
		}
	}
	if len(ordered) == 0 {
		return nil
	}
	return ordered
}

func stripAgyEffortDisplaySuffix(displayName, effort string) string {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" || effort == "" {
		return displayName
	}
	suffix := " (" + effort + ")"
	if len(displayName) > len(suffix) && strings.EqualFold(displayName[len(displayName)-len(suffix):], suffix) {
		return strings.TrimSpace(displayName[:len(displayName)-len(suffix)])
	}
	return displayName
}

// groupAgyModelInfos collapses agy's flat "<base>-<effort>" catalog rows into
// one ModelInfo per base model with its exact supported ReasoningEfforts.
// Running it on already-grouped entries is idempotent, so older flat disk
// caches are upgraded transparently when read.
func groupAgyModelInfos(models []core.ModelInfo) []core.ModelInfo {
	grouped := make([]core.ModelInfo, 0, len(models))
	indexByID := make(map[string]int, len(models))
	for _, m := range models {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		base, suffixEffort := parseAgyEffort(id)
		displayName := stripAgyEffortDisplaySuffix(m.DisplayName, suffixEffort)
		efforts := cloneEfforts(m.ReasoningEfforts)
		if suffixEffort != "" {
			efforts = append(efforts, suffixEffort)
		}
		if idx, exists := indexByID[base]; exists {
			if grouped[idx].DisplayName == "" && displayName != "" {
				grouped[idx].DisplayName = displayName
			}
			if len(efforts) > 0 {
				grouped[idx].ReasoningEfforts = sortAgyEfforts(append(grouped[idx].ReasoningEfforts, efforts...))
			}
			continue
		}
		entry := m
		entry.ID = base
		entry.DisplayName = displayName
		entry.ReasoningEfforts = sortAgyEfforts(efforts)
		indexByID[base] = len(grouped)
		grouped = append(grouped, entry)
	}
	return grouped
}

// GetCachedAgyBinModelInfos returns cached live agy CLI model metadata. Stale
// entries remain available because they are preferable to the curated fallback
// and keep non-subprocess callers fast.
func GetCachedAgyBinModelInfos() []core.ModelInfo {
	cached, err := cache.ReadModelCache(agyBinModelCacheKey)
	if err != nil || cached == nil || (len(cached.ModelInfos) == 0 && len(cached.Models) == 0) {
		return nil
	}
	return groupAgyModelInfos(modelInfosFromCache(cached))
}

// RefreshAgyBinCacheSync stores a freshly fetched agy CLI model list for
// completions and offline provider/model pickers.
func RefreshAgyBinCacheSync(models []core.ModelInfo) {
	grouped := groupAgyModelInfos(models)
	if len(grouped) == 0 {
		return
	}
	_ = cache.WriteModelInfoCache(agyBinModelCacheKey, modelInfosToCache(grouped))
}

func defaultAgyModelsCommandOutput(ctx context.Context, p *AgyBinProvider) ([]byte, error) {
	binding, err := p.nativeProxyBinding()
	if err != nil {
		return nil, err
	}
	if binding != nil {
		return p.boundAgyModels(ctx, binding)
	}
	cmd, err := newCLICommand(ctx, "agy", []string{"models"}, "")
	if err != nil {
		return nil, err
	}
	cmd.Env = p.extraEnvList()
	auditedEnv, wireAudit, err := startCLIWireAudit("agy-bin-models", cmd.Env)
	if err != nil {
		return nil, err
	}
	cmd.Env = auditedEnv
	defer stopCLIWireAudit(wireAudit)
	return cmd.Output()
}

// Use a scratch home rather than the live generation session (which may be
// streaming). Each process independently seeds aliases from discovery.
func (p *AgyBinProvider) boundAgyModels(ctx context.Context, binding *agyBoundRoute) (out []byte, retErr error) {
	worker := NewAgyBinProvider(p.model, p.extraEnv)
	if err := worker.resolveBinary(); err != nil {
		return nil, err
	}
	if err := worker.prepareHome(true, true); err != nil {
		return nil, err
	}
	defer os.RemoveAll(worker.agyHome)
	defer worker.cleanupRuntime()
	finish, err := worker.startNativeProxyWorker(ctx, binding)
	if err != nil {
		return nil, err
	}
	defer func() { retErr = errors.Join(retErr, finish()) }()
	cmd, err := newCLICommand(ctx, worker.agyBinary, []string{"models"}, filepath.Join(worker.agyHome, "cwd"))
	if err != nil {
		return nil, err
	}
	cmd.Env = worker.proxyEnv
	return cmd.Output()
}

var agyModelsCommandOutput = defaultAgyModelsCommandOutput

// ListModels parses the account-specific tab-separated model catalog exposed by
// `agy models` and saves it in term-llm's shared model cache.
func (p *AgyBinProvider) ListModels(ctx context.Context) ([]core.ModelInfo, error) {
	out, err := agyModelsCommandOutput(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("list agy models: %w", err)
	}
	models := parseAgyModelsOutput(string(out))
	if len(models) == 0 {
		return nil, fmt.Errorf("list agy models: no models found")
	}
	// The local account catalog is not the bound account catalog.
	if !cliProxyRequested(p.extraEnv, agyProxyRootEnv) {
		RefreshAgyBinCacheSync(models)
	}
	return models, nil
}

func parseAgyModelsOutput(output string) []core.ModelInfo {
	raw := make([]core.ModelInfo, 0)
	seen := make(map[string]bool)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 || seen[fields[0]] {
			continue
		}
		seen[fields[0]] = true
		model := core.ModelInfo{ID: fields[0]}
		if len(fields) > 1 {
			model.DisplayName = strings.Join(fields[1:], " ")
		}
		raw = append(raw, model)
	}
	return groupAgyModelInfos(raw)
}
