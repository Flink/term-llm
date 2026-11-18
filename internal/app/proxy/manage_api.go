package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const managePrefix = "/_proxy/manage/"

var externalIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,128}$`)

type manageAction string

const (
	manageRoutes  manageAction = "routes"
	manageList    manageAction = "list"
	manageGet     manageAction = "get"
	manageCreate  manageAction = "create"
	manageRegrant manageAction = "regrant"
	manageDelete  manageAction = "delete"
)

// Match only the public management API; don't delegate to browser routing.
func manageEndpoint(path, method string) (manageAction, string, int) {
	switch path {
	case "v1/routes":
		if method == http.MethodGet {
			return manageRoutes, "", 0
		}
		return "", "", http.StatusMethodNotAllowed
	case "v1/clients":
		switch method {
		case http.MethodGet:
			return manageList, "", 0
		case http.MethodPost:
			return manageCreate, "", 0
		}
		return "", "", http.StatusMethodNotAllowed
	}
	relative, ok := strings.CutPrefix(path, "v1/clients/")
	if !ok {
		return "", "", http.StatusNotFound
	}
	id, suffix, nested := strings.Cut(relative, "/")
	switch {
	case id == "" || nested && suffix != "grants":
		return "", "", http.StatusNotFound
	case nested && method == http.MethodPut:
		return manageRegrant, id, 0
	case !nested && method == http.MethodGet:
		return manageGet, id, 0
	case !nested && method == http.MethodDelete:
		return manageDelete, id, 0
	}
	return "", "", http.StatusMethodNotAllowed
}

func (s *Service) manageAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	for name := range r.Header {
		if strings.EqualFold(name, "Origin") {
			fail(w, 403, "Origin is not allowed on the management API")
			return
		}
	}
	if r.Method == http.MethodOptions {
		fail(w, 405, "Method not allowed")
		return
	}
	action, id, status := manageEndpoint(strings.TrimPrefix(r.URL.Path, managePrefix), r.Method)
	if status != 0 {
		fail(w, status, http.StatusText(status))
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		fail(w, 503, "Proxy concurrency limit")
		return
	}
	rc := http.NewResponseController(w)
	now := time.Now()
	_ = rc.SetReadDeadline(now.Add(15 * time.Second))
	defer rc.SetReadDeadline(time.Time{})
	// Leave time to respond after reading the body, but don't let a slow
	// response reader hold an admission slot indefinitely.
	_ = rc.SetWriteDeadline(now.Add(30 * time.Second))
	defer rc.SetWriteDeadline(time.Time{})
	auth := newManageCredential(r)
	if action == manageRoutes || action == manageList || action == manageGet {
		result, ok := s.readManage(w, r, auth, action, id)
		if ok {
			writeManageResult(w, 200, result)
		}
		return
	}
	s.mutateManage(w, r, auth, action, id)
}

type manageGrantInput struct {
	Grants   []string `json:"grants"`
	Revision *int64   `json:"revision"`
}

type manageCreateInput struct {
	manageGrantInput
	Name       string          `json:"name"`
	ExternalID json.RawMessage `json:"external_id"`
}

func (s *Service) readManage(w http.ResponseWriter, r *http.Request, auth manageCredential, action manageAction, id string) (manageResult, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.validateManage(w, r, auth, true); !ok {
		return manageResult{}, false
	}
	ctx := r.Context()
	switch action {
	case manageRoutes:
		routes, err := s.projectManageRoutes(ctx)
		return manageResult{body: map[string]any{"revision": s.snap.revision, "routes": routes}, err: err}, true
	case manageGet:
		c, err := managedCallerWithGrants(ctx, s.store.db, id)
		return manageResult{body: map[string]any{"client": c}, err: err}, true
	default:
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			return manageResult{err: inputError("Invalid query string")}, true
		}
		external := q.Get("external_id")
		if q.Has("external_id") && (len(q["external_id"]) != 1 || !externalIDPattern.MatchString(external)) {
			return manageResult{err: inputError("Invalid external_id")}, true
		}
		cs, err := s.store.managedCallers(ctx, external)
		return manageResult{body: map[string]any{"clients": cs}, err: err}, true
	}
}

func (s *Service) mutateManage(w http.ResponseWriter, r *http.Request, auth manageCredential, action manageAction, id string) {
	// Do not read a body from an unauthenticated peer or hold the write lock
	// across untrusted I/O. This first check cannot authorize the mutation.
	if _, ok := s.validateManage(w, r, auth, false); !ok {
		return
	}
	var in manageCreateInput
	switch action {
	case manageCreate:
		if !readJSON(w, r, &in) {
			return
		}
	case manageRegrant:
		if !readJSON(w, r, &in.manageGrantInput) {
			return
		}
	}
	result, ok := s.commitManage(w, r, auth, action, id, in)
	if ok {
		status := http.StatusOK
		if action == manageCreate {
			status = http.StatusCreated
		}
		writeManageResult(w, status, result)
	}
}

type manageResult struct {
	body any
	err  error
}

func (s *Service) commitManage(w http.ResponseWriter, r *http.Request, auth manageCredential, action manageAction, id string, in manageCreateInput) (manageResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.validateManage(w, r, auth, true)
	if !ok {
		return manageResult{}, false
	}
	if action == manageDelete {
		return s.deleteManageClient(r, key, id), true
	}
	snap := s.snap
	if in.Revision == nil {
		return manageResult{err: inputError("revision is required")}, true
	}
	if in.Grants == nil {
		return manageResult{err: inputError("grants must be an array; use [] to grant no routes")}, true
	}
	if *in.Revision != snap.revision {
		return manageResult{err: errManageRevision}, true
	}
	if action == manageCreate {
		return s.createManageClient(r, key, in, snap), true
	}
	c, err := s.store.regrantManagedCaller(r.Context(), id, in.Grants, snap)
	if err == nil {
		s.cancelCallerSockets(c.ID)
		auditManage("regrant", key, c, len(in.Grants))
	}
	return manageResult{body: map[string]any{"client": c}, err: err}, true
}

func (s *Service) createManageClient(r *http.Request, key string, in manageCreateInput, snap *snapshot) manageResult {
	name := strings.TrimSpace(in.Name)
	if len(name) < 1 || len(name) > 80 {
		return manageResult{err: inputError("Enter a client name (1–80 characters)")}
	}
	external := ""
	if in.ExternalID != nil {
		if err := json.Unmarshal(in.ExternalID, &external); err != nil || !externalIDPattern.MatchString(external) {
			return manageResult{err: inputError("Invalid external_id")}
		}
	}
	c, token, err := s.store.insertCaller(r.Context(), name, in.Grants, snap, key, external)
	if err == nil {
		auditManage("create", key, c, len(in.Grants))
	}
	return manageResult{body: map[string]any{"client": c, "token": token}, err: err}
}

func (s *Service) deleteManageClient(r *http.Request, key, id string) manageResult {
	ctx := r.Context()
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return manageResult{err: inputError("Invalid query string")}
	}
	if q.Has("purge") {
		if len(q["purge"]) != 1 || q.Get("purge") != "1" {
			return manageResult{err: inputError("Use purge=1 to remove a revoked client")}
		}
		c, err := s.store.purgeCaller(ctx, id)
		if err == nil {
			auditManage("purge", key, c, 0)
		}
		return manageResult{body: map[string]bool{"ok": true}, err: err}
	}
	c, err := s.store.revokeManagedCaller(ctx, id)
	if err == nil {
		s.cancelCallerSockets(id)
		auditManage("revoke", key, c, 0)
	}
	return manageResult{body: map[string]bool{"ok": true}, err: err}
}

var errManageRevision = errors.New("Route configuration changed; refresh before granting access")

func writeManageResult(w http.ResponseWriter, status int, result manageResult) {
	var conflict externalIDConflict
	var invalid inputError
	switch err := result.err; {
	case err == nil:
		jsonReply(w, status, result.body)
	case errors.As(err, &conflict):
		jsonReply(w, 409, map[string]string{"error": conflict.Error(), "client_id": conflict.clientID})
	case errors.As(err, &invalid):
		fail(w, 400, invalid.Error())
	case errors.Is(err, errCallerMissing):
		fail(w, 404, errCallerMissing.Error())
	case errors.Is(err, errManageRevision), errors.Is(err, errCallerRevoked), errors.Is(err, errCallerActive):
		fail(w, 409, err.Error())
	default:
		fail(w, 503, "Management storage unavailable")
	}
}

func auditManage(action, key string, c Caller, grants int) {
	log.Printf("proxy: manage_client_%s key=%s client=%s name=%q grants=%d", action, key, c.ID, adminAuditName(c.Name), grants)
}

// These types are an allowlist, not a serialized Route, Rule, or account.
type manageAuthInfo struct {
	Header string `json:"header"`
	Prefix string `json:"prefix"`
}

type manageRuleInfo struct {
	PathPrefix string         `json:"path_prefix"`
	CallerAuth manageAuthInfo `json:"caller_auth"`
}

type manageRouteInfo struct {
	Name         string           `json:"name"`
	LocalPrefix  string           `json:"local_prefix"`
	Enabled      bool             `json:"enabled"`
	WebSocket    bool             `json:"websocket"`
	AccountState string           `json:"account_state,omitempty"`
	CallerAuth   manageAuthInfo   `json:"caller_auth"`
	Rules        []manageRuleInfo `json:"rules"`
}

func (s *Service) projectManageRoutes(ctx context.Context) ([]manageRouteInfo, error) {
	accountStates, err := s.manageRouteAccountStates(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]manageRouteInfo, 0, len(s.snap.routes))
	for _, route := range s.snap.routes {
		entry := manageRouteInfo{
			Name: route.Name, LocalPrefix: route.LocalPrefix, Enabled: route.Enabled, WebSocket: route.WebSocket,
			AccountState: accountStates[route.Account],
			CallerAuth:   manageAuthInfo(route.CallerAuth), Rules: make([]manageRuleInfo, 0, len(route.rules)),
		}
		for _, rule := range route.rules {
			entry.Rules = append(entry.Rules, manageRuleInfo{rule.pathPrefix, manageAuthInfo(rule.CallerAuth)})
		}
		out = append(out, entry)
	}
	return out, nil
}

func (s *Service) manageRouteAccountStates(ctx context.Context) (map[string]string, error) {
	states := make(map[string]string)
	var names []any
	for _, route := range s.snap.routes {
		if _, seen := states[route.Account]; route.Account != "" && !seen {
			names = append(names, route.Account)
			states[route.Account] = "unavailable" // Keep this state for missing accounts.
		}
	}
	if len(names) == 0 {
		return states, nil
	}
	// The route limit bounds the parameter count. Read only state, not custody.
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(names)), ",")
	rows, err := s.store.db.QueryContext(ctx, "SELECT name,state FROM accounts WHERE name IN ("+placeholders+")", names...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, state string
		if err := rows.Scan(&name, &state); err != nil {
			return nil, err
		}
		states[name] = state
	}
	return states, rows.Err()
}
