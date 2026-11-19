package proxy

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func chatGPTAccountIdentityHash(a accountRecord) string {
	if a.credentials.AccountID != "" {
		return chatGPTIdentityHash(a.credentials.AccountID)
	}
	return a.chatGPTIdentityHash
}

func chatGPTIdentityHash(accountID string) string {
	return hashToken("proxy-chatgpt-identity:" + accountID)
}

type chatGPTSetupTarget struct {
	name            string
	reconnect       bool
	identityVersion int64
}

type chatGPTSetupError struct {
	code    int
	message string
}

func (s *Service) prepareChatGPTSetup(in chatGPTSetupInput) (chatGPTSetupTarget, *chatGPTSetupError) {
	if in.Name != "" && in.Account != "" {
		return chatGPTSetupTarget{}, &chatGPTSetupError{400, "Specify a name for a new route or an account to reconnect, not both"}
	}
	target := chatGPTSetupTarget{name: in.Name, reconnect: in.Account != ""}
	if target.reconnect {
		target.name = in.Account
	} else if target.name == "" {
		target.name = "chatgpt"
	}
	if !slug.MatchString(target.name) {
		return chatGPTSetupTarget{}, &chatGPTSetupError{400, "Name must be a slug of at most 64 characters"}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	old, problem := s.checkChatGPTSetupTarget(target)
	target.identityVersion = old.identityVersion
	return target, problem
}

// Check at start and again under the commit lock. The admin may change routes
// or accounts while the user is entering the device code.
func (s *Service) checkChatGPTSetupTarget(target chatGPTSetupTarget) (accountRecord, *chatGPTSetupError) {
	if !target.reconnect && s.opts.ConfigFile != "" {
		return accountRecord{}, &chatGPTSetupError{409, "Routes are operator-managed; edit the mounted file and Reload"}
	}
	old, err := s.store.account(target.name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return accountRecord{}, &chatGPTSetupError{503, "Could not check available accounts"}
	}
	if target.reconnect {
		switch {
		case err != nil:
			return old, &chatGPTSetupError{404, "ChatGPT account not found"}
		case old.Kind != "chatgpt":
			return old, &chatGPTSetupError{409, "This account is not a ChatGPT account"}
		case old.operator.Issuer != "" || old.operator.ClientID != "" || len(old.operator.Origins) != 0:
			// Production credentials must never go to custom issuers or origins.
			return old, &chatGPTSetupError{409, "Accounts with custom OAuth settings cannot use ChatGPT device sign-in"}
		case chatGPTAccountIdentityHash(old) == "":
			return old, &chatGPTSetupError{409, "The original ChatGPT account identity is unavailable; create a new route and grant access explicitly"}
		}
		return old, nil
	}
	if err == nil {
		return old, &chatGPTSetupError{409, "An account named " + target.name + " already exists; remove it under Routes › Unused accounts, or choose a different route name"}
	}
	return old, s.checkNewChatGPTRoute(target.name)
}

func (s *Service) checkNewChatGPTRoute(name string) *chatGPTSetupError {
	if len(s.snap.routes) >= 128 {
		return &chatGPTSetupError{409, "Route limit reached"}
	}
	for _, route := range s.snap.routes {
		if route.Name == name {
			return &chatGPTSetupError{409, "A route with this name already exists; choose a different name"}
		}
	}
	if validatePrefix(Route{Name: name, LocalPrefix: "/" + name + "/"}, s.snap.routes) != nil {
		return &chatGPTSetupError{409, "This route prefix is reserved or overlaps an existing route; choose a different name"}
	}
	return nil
}

func (s *Service) completeChatGPTSetup(ctx context.Context, id string, a *chatGPTSetupAttempt, creds accountCredentials) string {
	if validateAccountCredentials("chatgpt", creds) != nil || creds.ExpiresAt <= time.Now().Unix() {
		return "ChatGPT did not issue usable credentials; start again"
	}
	unlock, err := s.accountLock(ctx, a.target.name)
	if err != nil {
		return "ChatGPT sign-in did not complete; start again"
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	unlockOwner, valid := s.lockChatGPTSetupOwner(a.owner)
	defer unlockOwner()
	if !valid {
		return "Admin session expired or was revoked; sign in and try again"
	}
	d := &s.chatgptSetup
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.attempts[id] != a || a.result.Status != "pending" || ctx.Err() != nil || s.closing {
		return "ChatGPT sign-in did not complete; start again"
	}
	route, problem := s.commitChatGPTSetup(ctx, a.target, creds)
	if problem != nil {
		return problem.message
	}
	a.result = chatGPTSetupStatus{Status: "connected", Route: route}
	a.expires = time.Now().Add(chatGPTSetupResultTTL)
	return ""
}

func (s *Service) commitChatGPTSetup(ctx context.Context, target chatGPTSetupTarget, creds accountCredentials) (string, *chatGPTSetupError) {
	old, problem := s.checkChatGPTSetupTarget(target)
	if problem != nil {
		return "", problem
	}
	a := accountRecord{AccountInfo: AccountInfo{Name: target.name, Kind: "chatgpt", State: "ready", Generation: 1, ExpiresAt: creds.ExpiresAt, Updated: time.Now().UnixMilli()}, credentials: creds}
	if target.reconnect {
		// Routine refresh may change the generation, but sign-out/replacement
		// changes the identity epoch and makes this pending authorization stale.
		if old.identityVersion != target.identityVersion {
			return "", &chatGPTSetupError{409, "The ChatGPT account changed while signing in; start again"}
		}
		if chatGPTAccountIdentityHash(old) != chatGPTIdentityHash(creds.AccountID) {
			return "", &chatGPTSetupError{409, "Sign in to the original ChatGPT account and workspace, or create a separate route"}
		}
		a.Generation, a.identityVersion, a.operator = old.Generation+1, old.identityVersion+1, old.operator
		if err := s.store.replaceAccount(ctx, a, old.Generation); err != nil {
			return "", &chatGPTSetupError{503, "Could not update the ChatGPT account; existing access is unchanged"}
		}
		s.rememberAccount(a.Name, a.credentials)
		s.cancelAccountSockets(a.Name)
		return s.chatGPTRouteForAccount(a.Name), nil
	}
	return s.provisionChatGPTRoute(ctx, a)
}

// Reconnect preserves every route, including custom and disabled routes. The
// first attached route is useful to the UI; prefer an enabled route if any.
func (s *Service) chatGPTRouteForAccount(name string) string {
	var first string
	for _, route := range s.snap.routes {
		if route.Account == name {
			if route.Enabled {
				return route.Name
			}
			if first == "" {
				first = route.Name
			}
		}
	}
	return first
}

func (s *Service) provisionChatGPTRoute(ctx context.Context, a accountRecord) (string, *chatGPTSetupError) {
	var preset routePreset
	for _, p := range routePresets {
		if p.ID == "chatgpt" && p.AccountKind == a.Kind {
			preset = p
			break
		}
	}
	c := s.snap.config
	money, err := s.currentMoneySettingsLocked(ctx)
	if err != nil {
		return "", &chatGPTSetupError{503, "Could not read current proxy configuration"}
	}
	c.Money = money
	route := chatGPTPresetRoute(preset, a.Name, a.Name)
	c.Routes = append(append([]Route(nil), c.Routes...), route)
	companions := chatGPTCompanionRoutes(preset, a.Name, c.Routes)
	c.Routes = append(c.Routes, companions...)
	added := 1 + len(companions)
	next, err := compile(c, s.snap.revision+1, s.snap.secrets)
	if err != nil {
		return "", &chatGPTSetupError{503, "Could not prepare the standard ChatGPT route"}
	}
	for _, r := range next.routes[len(next.routes)-added:] {
		if !accountOriginAllowed(a, r.origin) {
			return "", &chatGPTSetupError{503, "Could not prepare the standard ChatGPT route"}
		}
	}
	err = s.store.accountWrite(ctx, func(tx *sql.Tx) error {
		if err := insertAccount(ctx, tx, a); err != nil {
			return err
		}
		return saveSetupConfig(ctx, tx, next)
	})
	if err != nil {
		return "", &chatGPTSetupError{503, "Could not save the ChatGPT route and account; existing configuration is unchanged"}
	}
	s.rememberAccount(a.Name, a.credentials)
	s.reconcileSockets(next)
	s.snap = next
	return route.Name, nil
}
