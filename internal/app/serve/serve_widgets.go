package serve

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/term-llm/term-llm/internal/widgets"
)

// widgetsRoute returns the widgets sub-route, e.g. "/ui/widgets/".
func (c serveServerConfig) widgetsRoute() string { return c.basePath + "/widgets/" }

// registerWidgetRoutes mounts widget proxy and admin routes on inner mux.
// inner must be an http.ServeMux with basePath already stripped.
func (s *serveServer) registerWidgetRoutes(inner *http.ServeMux) {
	inner.HandleFunc("/widgets/", s.auth(s.handleWidgetProxy))
	inner.HandleFunc("/admin/widgets/reload", s.auth(s.cors(s.handleAdminWidgetsReload)))
	inner.HandleFunc("/admin/widgets/status", s.auth(s.cors(s.handleAdminWidgetsStatus)))
	inner.HandleFunc("/admin/widgets/stop", s.auth(s.cors(s.handleAdminWidgetsStop)))
	inner.HandleFunc("/admin/widgets/", s.auth(s.cors(s.handleAdminWidgetAction)))
	inner.HandleFunc("/widgets", s.auth(s.handleWidgetIndex))
}

// handleWidgetIndex serves a simple HTML index page listing all widgets.
func (s *serveServer) handleWidgetIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/widgets" && r.URL.Path != "/widgets/" {
		http.NotFound(w, r)
		return
	}
	s.widgetsMgr.ServeIndex(w)
}

// handleWidgetProxy proxies requests to the appropriate widget process.
// Path: /widgets/<mount>/...
func (s *serveServer) handleWidgetProxy(w http.ResponseWriter, r *http.Request) {
	// Strip leading /widgets/ to get "mount/rest..."
	rest := strings.TrimPrefix(r.URL.Path, "/widgets/")
	if rest == "" {
		// /widgets/ with no mount - redirect to index
		http.Redirect(w, r, s.cfg.basePath+"/widgets", http.StatusTemporaryRedirect)
		return
	}

	parts := strings.SplitN(rest, "/", 2)
	mount := parts[0]
	if mount == "" {
		http.NotFound(w, r)
		return
	}

	// /widgets/<mount> (no trailing slash) → redirect so relative asset URLs work.
	if len(parts) == 1 {
		http.Redirect(w, r, s.cfg.basePath+"/widgets/"+mount+"/", http.StatusTemporaryRedirect)
		return
	}

	// Build the widget-relative path: "/" + everything after "mount/"
	widgetPath := "/" + parts[1]

	// Rewrite request path to the widget-relative path before proxying.
	r2 := r.Clone(r.Context())
	r2.URL.Path = widgetPath
	if r2.URL.RawPath != "" {
		rawRest := strings.TrimPrefix(r.URL.RawPath, "/widgets/"+mount)
		if rawRest == "" || rawRest == "/" {
			rawRest = "/"
		} else if !strings.HasPrefix(rawRest, "/") {
			rawRest = "/" + rawRest
		}
		r2.URL.RawPath = rawRest
	}

	s.widgetsMgr.Proxy(mount, w, r2)
}

// handleAdminWidgetsReload handles POST /admin/widgets/reload.
func (s *serveServer) handleAdminWidgetsReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	errs := s.widgetsMgr.Reload()
	type result struct {
		OK     bool     `json:"ok"`
		Errors []string `json:"errors,omitempty"`
	}
	res := result{OK: len(errs) == 0}
	for _, e := range errs {
		res.Errors = append(res.Errors, e.Error())
	}
	writeJSON(w, http.StatusOK, res)
}

type widgetStatusResponse struct {
	Widgets    []widgets.WidgetStatus `json:"widgets"`
	LoadErrors []string               `json:"load_errors,omitempty"`
}

func (s *serveServer) currentWidgetStatus() widgetStatusResponse {
	resp := widgetStatusResponse{Widgets: make([]widgets.WidgetStatus, 0)}
	if s.widgetsMgr == nil {
		return resp
	}
	resp.Widgets = s.widgetsMgr.Status()
	for _, err := range s.widgetsMgr.LoadErrors() {
		resp.LoadErrors = append(resp.LoadErrors, err.Error())
	}
	return resp
}

// handleAdminWidgetsStatus handles GET /admin/widgets/status.
func (s *serveServer) handleAdminWidgetsStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.currentWidgetStatus())
}

// handleAdminWidgetsStop handles POST /admin/widgets/stop.
func (s *serveServer) handleAdminWidgetsStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.widgetsMgr.StopAll()
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"ok":true}`)
}

// handleAdminWidgetAction handles POST /admin/widgets/<mount>/stop, /disable
// and /enable.
func (s *serveServer) handleAdminWidgetAction(w http.ResponseWriter, r *http.Request) {
	mount, action, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/admin/widgets/"), "/")
	if mount == "" || (action != "stop" && action != "enable" && action != "disable") {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var err error
	if action == "stop" {
		err = s.widgetsMgr.StopMount(mount)
	} else {
		err = s.widgetsMgr.SetDisabled(mount, action == "disable")
	}
	var warning string
	var stopErr *widgets.DisableStopError
	switch {
	case err == nil:
	case errors.As(err, &stopErr):
		warning = stopErr.Error()
	case errors.Is(err, widgets.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		widgetStatusResponse
		OK      bool   `json:"ok"`
		Warning string `json:"warning,omitempty"`
	}{widgetStatusResponse: s.currentWidgetStatus(), OK: true, Warning: warning})
}
