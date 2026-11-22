package responsesproviders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/term-llm/term-llm/internal/appdata"
	"github.com/term-llm/term-llm/internal/credentials"
	"github.com/term-llm/term-llm/internal/fileprivacy"
	"github.com/term-llm/term-llm/internal/proxyclient"
)

var errChatGPTCatalogBinding = errors.New("ChatGPT catalog requires an exact registered ChatGPT proxy binding")

type delegatedChatGPTCatalogScope struct {
	root string
	id   string
}

func delegatedChatGPTCatalog(baseURL string) (delegatedChatGPTCatalogScope, proxyclient.Binding, error) {
	binding, ok := delegatedCatalogBinding(baseURL, "chatgpt")
	if !ok {
		return delegatedChatGPTCatalogScope{}, proxyclient.Binding{}, errChatGPTCatalogBinding
	}
	return chatGPTCatalogScopeForBinding(binding), binding, nil
}

func chatGPTCatalogScopeForBinding(binding proxyclient.Binding) delegatedChatGPTCatalogScope {
	id := delegatedCatalogID(binding, binding.Root+"/models")
	return delegatedChatGPTCatalogScope{root: binding.Root, id: id}
}

func (p *ChatGPTProvider) delegatedCatalogScope() (delegatedChatGPTCatalogScope, error) {
	scope, binding, err := delegatedChatGPTCatalog(p.baseURL)
	if err != nil {
		return scope, err
	}
	if !binding.SamePrincipal(proxyclient.Binding{Kind: "chatgpt", Token: p.creds.AccessToken}) {
		return delegatedChatGPTCatalogScope{}, errChatGPTCatalogBinding
	}
	return scope, nil
}

// CachedDelegatedChatGPTModels reads backend model facts without network access
// or local OAuth. Missing data returns nil, false, nil. Stale data is returned
// with fresh=false; callers may still use it for completion.
func CachedDelegatedChatGPTModels(baseURL string) (models []ModelInfo, fresh bool, err error) {
	scope, _, err := delegatedChatGPTCatalog(baseURL)
	if err != nil {
		return nil, false, err
	}
	entry, err := scope.load()
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return entry.Models, err == nil && entry.fresh(), err
}

// RefreshDelegatedChatGPTModelsIfStale refreshes missing, stale, or unusable
// on-disk facts for the exact binding. The caller's context bounds the HTTP
// request; unlike interactive discovery, this does not launch a detached fetch.
// On success, the cache is ready for another process to read.
func RefreshDelegatedChatGPTModelsIfStale(ctx context.Context, baseURL string) error {
	ctx, cancel := context.WithTimeout(ctx, chatGPTModelsTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	scope, _, err := delegatedChatGPTCatalog(baseURL)
	if err != nil {
		return err
	}
	if entry, err := scope.load(); err == nil && entry.fresh() {
		return nil
	}
	creds, err := credentials.GetChatGPTCredentialsForEndpoint(scope.root)
	if err != nil {
		return err
	}
	models, err := scope.fetch(ctx, creds)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	entry := scope.entry(models)
	if err := scope.save(entry); err != nil {
		return fmt.Errorf("save delegated ChatGPT catalog: %w", err)
	}
	rememberProxyChatGPTCatalog(scope.id, entry)
	return nil
}

func (scope delegatedChatGPTCatalogScope) fetch(ctx context.Context, creds *credentials.ChatGPTCredentials) ([]ModelInfo, error) {
	// Bypass provider construction and all direct model policy/cache lookups.
	p := ChatGPTProvider{baseURL: scope.root, creds: creds}
	return p.refreshChatGPTModelFacts(ctx)
}

func (scope delegatedChatGPTCatalogScope) entry(models []ModelInfo) chatGPTModelsCache {
	return chatGPTModelsCache{
		AccountID:     scope.id,
		ClientVersion: chatGPTModelsClientVersion,
		FetchedAt:     time.Now(),
		Models:        models,
	}
}

func (entry chatGPTModelsCache) fresh() bool {
	return !entry.FetchedAt.IsZero() && time.Since(entry.FetchedAt) <= chatGPTModelsCacheTTL
}

func (scope delegatedChatGPTCatalogScope) path() (string, error) {
	dataDir, err := appdata.GetDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "chatgpt_delegated_models_"+scope.id+".json"), nil
}

func (scope delegatedChatGPTCatalogScope) load() (chatGPTModelsCache, error) {
	path, err := scope.path()
	if err != nil {
		return chatGPTModelsCache{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return chatGPTModelsCache{}, err
	}
	if !info.Mode().IsRegular() {
		return chatGPTModelsCache{}, errors.New("cached delegated ChatGPT catalog is not a regular file")
	}
	if err := fileprivacy.CheckFile(info); err != nil {
		return chatGPTModelsCache{}, fmt.Errorf("cached delegated ChatGPT catalog: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return chatGPTModelsCache{}, err
	}
	var entry chatGPTModelsCache
	if err := json.Unmarshal(data, &entry); err != nil {
		return chatGPTModelsCache{}, fmt.Errorf("decode delegated ChatGPT catalog: %w", err)
	}
	if entry.AccountID != scope.id || entry.ClientVersion != chatGPTModelsClientVersion || entry.FetchedAt.IsZero() || len(entry.Models) == 0 {
		return chatGPTModelsCache{}, errors.New("cached delegated ChatGPT catalog has invalid scope, version, timestamp, or models")
	}
	return entry, nil
}

func (scope delegatedChatGPTCatalogScope) save(entry chatGPTModelsCache) error {
	path, err := scope.path()
	if err != nil {
		return err
	}
	if err := fileprivacy.MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return fileprivacy.WriteFileAtomic(path, data)
}
