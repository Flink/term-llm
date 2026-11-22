package proxy

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/term-llm/term-llm/internal/app/proxy/deviceflow"
	"github.com/term-llm/term-llm/internal/oauth"
	"github.com/term-llm/term-llm/internal/passkeyauth"
)

const (
	deviceSetupLifetime  = 15 * time.Minute
	deviceSetupResultTTL = 5 * time.Minute
	deviceSetupLimit     = 32
)

type deviceSetupInput struct {
	Name    string `json:"name"`
	Account string `json:"account"`
}

type deviceSetupChallenge = deviceflow.Challenge

type deviceSetupStatus struct {
	Status string `json:"status"`
	Route  string `json:"route,omitempty"`
	Error  string `json:"error,omitempty"`
}

type deviceSetupOwner struct {
	tokenHash string
	principal passkeyauth.Principal
}

type deviceSetupAttempt struct {
	owner   deviceSetupOwner
	target  deviceSetupTarget
	cancel  context.CancelFunc
	expires time.Time
	result  deviceSetupStatus
}

type deviceSetups struct {
	mu       sync.Mutex
	attempts map[string]*deviceSetupAttempt
	closed   bool
	workers  sync.WaitGroup

	start deviceflow.Start // optional issuer fixture installed before serving
}

func (s *Service) deviceSetupAPI(w http.ResponseWriter, r *http.Request, sess session, kind string) {
	owner := deviceSetupOwner{principal: sess.principal}
	if s.passkey == nil {
		owner.tokenHash = hashToken(cookieValue(r, "tlp_admin"))
	}
	if r.Method == http.MethodPost {
		s.beginDeviceSetup(w, r, sess, owner, kind)
		return
	}
	result, problem := s.deviceSetup.check(r.URL.Query().Get("id"), kind, owner, r.Method == http.MethodDelete)
	switch {
	case problem != nil:
		fail(w, problem.code, problem.message)
	case r.Method == http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
	default:
		jsonReply(w, 200, result)
	}
}

func (d *deviceSetups) check(id, kind string, owner deviceSetupOwner, cancel bool) (deviceSetupStatus, *deviceSetupError) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prune(time.Now())
	a := d.attempts[id]
	if a == nil || a.owner != owner || a.target.kind != kind {
		return deviceSetupStatus{}, &deviceSetupError{404, deviceflow.Label(kind) + " sign-in attempt not found"}
	}
	if cancel {
		// This only discards an uncommitted attempt. It never signs out an account.
		if a.result.Status == "connected" {
			return deviceSetupStatus{}, &deviceSetupError{409, deviceflow.Label(kind) + " sign-in already completed"}
		}
		a.cancel()
		delete(d.attempts, id)
	}
	return a.result, nil
}

func (s *Service) beginDeviceSetup(w http.ResponseWriter, r *http.Request, sess session, owner deviceSetupOwner, kind string) {
	var in deviceSetupInput
	if !readJSON(w, r, &in) {
		return
	}
	target, problem := s.prepareDeviceSetup(in, kind)
	if problem != nil {
		fail(w, problem.code, problem.message)
		return
	}
	challenge, problem := s.startDeviceSetup(r.Context(), sess.expires, owner, target)
	if problem != nil {
		fail(w, problem.code, problem.message)
		return
	}
	jsonReply(w, 201, challenge)
}

func (s *Service) startDeviceSetup(requestCtx context.Context, sessionExpiry time.Time, owner deviceSetupOwner, target deviceSetupTarget) (deviceSetupChallenge, *deviceSetupError) {
	d := &s.deviceSetup
	expires := time.Now().Add(deviceSetupLifetime)
	if !sessionExpiry.IsZero() && sessionExpiry.Before(expires) {
		expires = sessionExpiry
	}
	// Polling lives past the initiating HTTP request, but never past this deadline.
	ctx, cancel := context.WithDeadline(context.Background(), expires)
	a := &deviceSetupAttempt{owner: owner, target: target, cancel: cancel, expires: expires, result: deviceSetupStatus{Status: "pending"}}
	id := randomToken("ds_")
	if problem := d.reserve(id, a); problem != nil {
		cancel()
		return deviceSetupChallenge{}, problem
	}
	defer d.workers.Done()
	challenge, poll, err := d.start.Request(ctx, requestCtx, target.kind, s.transport)
	if err != nil || poll == nil || challenge.VerificationURL == "" || challenge.UserCode == "" || len(challenge.UserCode) > 128 || ctx.Err() != nil || requestCtx.Err() != nil {
		d.mu.Lock()
		cancel()
		delete(d.attempts, id)
		d.mu.Unlock()
		if errors.Is(err, oauth.ErrChatGPTDeviceCodeNotEnabled) {
			return deviceSetupChallenge{}, &deviceSetupError{409, "ChatGPT device sign-in is unavailable; check your ChatGPT device code settings"}
		}
		return deviceSetupChallenge{}, &deviceSetupError{502, "Could not start " + deviceflow.Label(target.kind) + " sign-in; try again"}
	}
	// reserve already registered this worker. Add while its count is still
	// positive, so shutdown can safely wait for both it and the poll worker.
	d.workers.Add(1)
	go s.pollDeviceSetup(ctx, id, a, poll)
	challenge.ID = id
	return challenge, nil
}

func (d *deviceSetups) reserve(id string, a *deviceSetupAttempt) *deviceSetupError {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prune(time.Now())
	if d.closed {
		return &deviceSetupError{503, "Proxy is shutting down"}
	}
	for _, other := range d.attempts {
		if other.result.Status == "pending" && other.target.name == a.target.name {
			return &deviceSetupError{409, "A sign-in is already pending for this name"}
		}
	}
	if len(d.attempts) >= deviceSetupLimit {
		return &deviceSetupError{429, "Too many " + deviceflow.Label(a.target.kind) + " sign-in attempts; try again later"}
	}
	if d.attempts == nil {
		d.attempts = make(map[string]*deviceSetupAttempt)
	}
	d.attempts[id] = a
	d.workers.Add(1)
	return nil
}

// prune is called under d.mu. Expired attempts retain only sanitized status for
// a short time; this also releases the name before the worker unwinds.
func (d *deviceSetups) prune(now time.Time) {
	for id, a := range d.attempts {
		if now.Before(a.expires) {
			continue
		}
		if a.result.Status == "pending" && now.Before(a.expires.Add(deviceSetupResultTTL)) {
			a.cancel()
			a.result = deviceSetupStatus{Status: "failed", Error: deviceflow.Label(a.target.kind) + " sign-in expired; start again"}
			a.expires = a.expires.Add(deviceSetupResultTTL)
			continue
		}
		a.cancel()
		delete(d.attempts, id)
	}
}

func (d *deviceSetups) close() {
	d.mu.Lock()
	d.closed = true
	for _, a := range d.attempts {
		a.cancel()
	}
	clear(d.attempts)
	d.mu.Unlock()
	d.workers.Wait()
}

func (s *Service) pollDeviceSetup(ctx context.Context, id string, a *deviceSetupAttempt, poll deviceflow.Poll) {
	d := &s.deviceSetup
	defer d.workers.Done()
	defer a.cancel()
	creds, err := poll(ctx)
	message := deviceflow.Label(a.target.kind) + " sign-in did not complete; start again"
	if err == nil {
		message = s.completeDeviceSetup(ctx, id, a, accountCredentials{AccessToken: creds.AccessToken, RefreshToken: creds.RefreshToken, AccountID: creds.AccountID, ExpiresAt: creds.ExpiresAt})
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.attempts[id] == a && a.result.Status == "pending" {
		if ctx.Err() == context.DeadlineExceeded {
			message = deviceflow.Label(a.target.kind) + " sign-in expired; start again"
		}
		a.result = deviceSetupStatus{Status: "failed", Error: message}
		a.expires = time.Now().Add(deviceSetupResultTTL)
	}
}

// Keep logout/revocation serialized with the commit, without retaining a raw
// browser cookie or refreshing the idle lifetime in the background.
func (s *Service) lockDeviceSetupOwner(owner deviceSetupOwner) (unlock func(), valid bool) {
	if p := s.passkey; p != nil {
		p.mu.Lock()
		_, err := p.sessions.Info(owner.principal)
		return p.mu.Unlock, err == nil && p.store.HasCredential(owner.principal.CredentialRecordID)
	}
	s.authMu.Lock()
	sess, ok := s.sessions[owner.tokenHash]
	return s.authMu.Unlock, ok && time.Now().Before(sess.expires)
}
