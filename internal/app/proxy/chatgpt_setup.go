package proxy

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/term-llm/term-llm/internal/oauth"
	"github.com/term-llm/term-llm/internal/passkeyauth"
)

const (
	chatGPTSetupLifetime  = 15 * time.Minute
	chatGPTSetupResultTTL = 5 * time.Minute
	chatGPTSetupLimit     = 32
)

const chatGPTSetupExpired = "ChatGPT sign-in expired; start again"

type chatGPTSetupInput struct {
	Name    string `json:"name"`
	Account string `json:"account"`
}

type chatGPTSetupChallenge struct {
	ID              string `json:"id"`
	VerificationURL string `json:"verification_url"`
	UserCode        string `json:"user_code"`
}

type chatGPTSetupStatus struct {
	Status string `json:"status"`
	Route  string `json:"route,omitempty"`
	Error  string `json:"error,omitempty"`
}

type chatGPTSetupOwner struct {
	tokenHash string
	principal passkeyauth.Principal
}

type chatGPTSetupAttempt struct {
	owner   chatGPTSetupOwner
	target  chatGPTSetupTarget
	cancel  context.CancelFunc
	expires time.Time
	result  chatGPTSetupStatus
}

type chatGPTSetups struct {
	mu       sync.Mutex
	attempts map[string]*chatGPTSetupAttempt
	closed   bool
	workers  sync.WaitGroup

	// Test seams, installed before serving. The zero value uses the real issuer.
	request func(context.Context) (*oauth.ChatGPTDeviceCode, error)
	wait    func(context.Context, *oauth.ChatGPTDeviceCode) (*oauth.ChatGPTCredentials, error)
}

func (s *Service) chatgptSetupAPI(w http.ResponseWriter, r *http.Request, sess session) {
	owner := chatGPTSetupOwner{principal: sess.principal}
	if s.passkey == nil {
		owner.tokenHash = hashToken(cookieValue(r, "tlp_admin"))
	}
	if r.Method == http.MethodPost {
		s.beginChatGPTSetup(w, r, sess, owner)
		return
	}
	result, problem := s.chatgptSetup.check(r.URL.Query().Get("id"), owner, r.Method == http.MethodDelete)
	switch {
	case problem != nil:
		fail(w, problem.code, problem.message)
	case r.Method == http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
	default:
		jsonReply(w, 200, result)
	}
}

func (d *chatGPTSetups) check(id string, owner chatGPTSetupOwner, cancel bool) (chatGPTSetupStatus, *chatGPTSetupError) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prune(time.Now())
	a := d.attempts[id]
	if a == nil || a.owner != owner {
		return chatGPTSetupStatus{}, &chatGPTSetupError{404, "ChatGPT sign-in attempt not found"}
	}
	if cancel {
		// This only discards an uncommitted attempt. It never signs out an account.
		if a.result.Status == "connected" {
			return chatGPTSetupStatus{}, &chatGPTSetupError{409, "ChatGPT sign-in already completed"}
		}
		a.cancel()
		delete(d.attempts, id)
	}
	return a.result, nil
}

func (s *Service) beginChatGPTSetup(w http.ResponseWriter, r *http.Request, sess session, owner chatGPTSetupOwner) {
	var in chatGPTSetupInput
	if !readJSON(w, r, &in) {
		return
	}
	target, problem := s.prepareChatGPTSetup(in)
	if problem != nil {
		fail(w, problem.code, problem.message)
		return
	}
	challenge, problem := s.startChatGPTDevice(r.Context(), sess.expires, owner, target)
	if problem != nil {
		fail(w, problem.code, problem.message)
		return
	}
	jsonReply(w, 201, challenge)
}

func (s *Service) startChatGPTDevice(requestCtx context.Context, sessionExpiry time.Time, owner chatGPTSetupOwner, target chatGPTSetupTarget) (chatGPTSetupChallenge, *chatGPTSetupError) {
	d := &s.chatgptSetup
	expires := time.Now().Add(chatGPTSetupLifetime)
	if !sessionExpiry.IsZero() && sessionExpiry.Before(expires) {
		expires = sessionExpiry
	}
	// Polling lives past the initiating HTTP request, but never past this deadline.
	ctx, cancel := context.WithDeadline(context.Background(), expires)
	a := &chatGPTSetupAttempt{owner: owner, target: target, cancel: cancel, expires: expires, result: chatGPTSetupStatus{Status: "pending"}}
	id := randomToken("cg_")
	if problem := d.reserve(id, a); problem != nil {
		cancel()
		return chatGPTSetupChallenge{}, problem
	}
	defer d.workers.Done()
	code, err := d.requestCode(ctx, requestCtx)
	if err != nil || code == nil || code.UserCode == "" || len(code.UserCode) > 128 || ctx.Err() != nil || requestCtx.Err() != nil {
		d.mu.Lock()
		cancel()
		delete(d.attempts, id)
		d.mu.Unlock()
		if errors.Is(err, oauth.ErrChatGPTDeviceCodeNotEnabled) {
			return chatGPTSetupChallenge{}, &chatGPTSetupError{409, "ChatGPT device sign-in is unavailable; check your ChatGPT device code settings"}
		}
		return chatGPTSetupChallenge{}, &chatGPTSetupError{502, "Could not start ChatGPT sign-in; try again"}
	}
	// reserve already registered this worker. Add while its count is still
	// positive, so shutdown can safely wait for both it and the poll worker.
	d.workers.Add(1)
	go s.pollChatGPTSetup(ctx, id, a, code)
	return chatGPTSetupChallenge{ID: id, VerificationURL: oauth.ChatGPTDeviceVerificationURL, UserCode: code.UserCode}, nil
}

func (d *chatGPTSetups) requestCode(ctx, requestCtx context.Context) (*oauth.ChatGPTDeviceCode, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	stop := context.AfterFunc(requestCtx, cancel)
	defer stop()
	request := d.request
	if request == nil {
		request = oauth.RequestChatGPTDeviceCode
	}
	return request(ctx)
}

func (d *chatGPTSetups) reserve(id string, a *chatGPTSetupAttempt) *chatGPTSetupError {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prune(time.Now())
	if d.closed {
		return &chatGPTSetupError{503, "Proxy is shutting down"}
	}
	for _, other := range d.attempts {
		if other.result.Status == "pending" && other.target.name == a.target.name {
			return &chatGPTSetupError{409, "ChatGPT sign-in is already pending for this name"}
		}
	}
	if len(d.attempts) >= chatGPTSetupLimit {
		return &chatGPTSetupError{429, "Too many ChatGPT sign-in attempts; try again later"}
	}
	if d.attempts == nil {
		d.attempts = make(map[string]*chatGPTSetupAttempt)
	}
	d.attempts[id] = a
	d.workers.Add(1)
	return nil
}

// prune is called under d.mu. Expired attempts retain only sanitized status for
// a short time; this also releases the name before the worker unwinds.
func (d *chatGPTSetups) prune(now time.Time) {
	for id, a := range d.attempts {
		if now.Before(a.expires) {
			continue
		}
		if a.result.Status == "pending" && now.Before(a.expires.Add(chatGPTSetupResultTTL)) {
			a.cancel()
			a.result = chatGPTSetupStatus{Status: "failed", Error: chatGPTSetupExpired}
			a.expires = a.expires.Add(chatGPTSetupResultTTL)
			continue
		}
		a.cancel()
		delete(d.attempts, id)
	}
}

func (d *chatGPTSetups) close() {
	d.mu.Lock()
	d.closed = true
	for _, a := range d.attempts {
		a.cancel()
	}
	clear(d.attempts)
	d.mu.Unlock()
	d.workers.Wait()
}

func (s *Service) pollChatGPTSetup(ctx context.Context, id string, a *chatGPTSetupAttempt, code *oauth.ChatGPTDeviceCode) {
	d := &s.chatgptSetup
	defer d.workers.Done()
	defer a.cancel()
	wait := d.wait
	if wait == nil {
		wait = oauth.AuthenticateChatGPTDevice
	}
	issued, err := wait(ctx, code)
	message := "ChatGPT sign-in did not complete; start again"
	if err == nil && issued != nil {
		creds := accountCredentials{AccessToken: issued.AccessToken, RefreshToken: issued.RefreshToken, AccountID: issued.AccountID, ExpiresAt: issued.ExpiresAt}
		message = s.completeChatGPTSetup(ctx, id, a, creds)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.attempts[id] == a && a.result.Status == "pending" {
		if ctx.Err() == context.DeadlineExceeded {
			message = chatGPTSetupExpired
		}
		a.result = chatGPTSetupStatus{Status: "failed", Error: message}
		a.expires = time.Now().Add(chatGPTSetupResultTTL)
	}
}

// Keep logout/revocation serialized with the commit, without retaining a raw
// browser cookie or refreshing the idle lifetime in the background.
func (s *Service) lockChatGPTSetupOwner(owner chatGPTSetupOwner) (unlock func(), valid bool) {
	if p := s.passkey; p != nil {
		p.mu.Lock()
		_, err := p.sessions.Info(owner.principal)
		return p.mu.Unlock, err == nil && p.store.HasCredential(owner.principal.CredentialRecordID)
	}
	s.authMu.Lock()
	sess, ok := s.sessions[owner.tokenHash]
	return s.authMu.Unlock, ok && time.Now().Before(sess.expires)
}
