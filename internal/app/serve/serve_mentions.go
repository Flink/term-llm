package serve

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/term-llm/term-llm/internal/app/serve/wire"
	"github.com/term-llm/term-llm/internal/llm"
	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/mentions"
	"github.com/term-llm/term-llm/internal/session"
	"github.com/term-llm/term-llm/internal/worktree"
)

const (
	serveMentionSearchLimit     = 10
	serveMentionMaxTextBytes    = 256 << 10
	serveMentionCacheTTL        = 10 * time.Second
	serveMentionCacheErrorTTL   = time.Second
	serveMentionCacheMaxEntries = 32
	serveMentionBuildTimeout    = 30 * time.Second
)

type serveMentionCacheEntry struct {
	snapshot *mentions.Snapshot
	builtAt  time.Time
	lastUsed time.Time
	building chan struct{}
	err      error
	errorAt  time.Time
}

type serveMentionSearchRequest struct {
	Text        string `json:"text"`
	CursorUTF16 int    `json:"cursor_utf16"`
	Limit       int    `json:"limit,omitempty"`
	ProjectID   string `json:"project_id,omitempty"`
	NoProject   bool   `json:"no_project,omitempty"`
	WorktreeDir string `json:"worktree_dir,omitempty"`
}

type serveMentionToken struct {
	StartUTF16 int    `json:"start_utf16"`
	EndUTF16   int    `json:"end_utf16"`
	Query      string `json:"query"`
	Quoted     bool   `json:"quoted"`
}

type serveMentionSegment struct {
	Text    string `json:"text"`
	Matched bool   `json:"matched"`
}

type serveMentionSearchItem struct {
	Path       string                `json:"path"`
	Kind       string                `json:"kind"`
	InsertText string                `json:"insert_text"`
	Segments   []serveMentionSegment `json:"segments"`
}

type serveMentionModelItem struct {
	Label       string `json:"label"`
	Description string `json:"description"`
	Badge       string `json:"badge"`
	InsertText  string `json:"insert_text"`
	Continues   bool   `json:"continues"`
}

type serveMentionSearchResponse struct {
	Active         bool                     `json:"active"`
	Token          *serveMentionToken       `json:"token,omitempty"`
	Items          []serveMentionSearchItem `json:"items"`
	IndexTruncated bool                     `json:"index_truncated,omitempty"`
	ModelToken     *serveMentionToken       `json:"model_token,omitempty"`
	ModelItems     []serveMentionModelItem  `json:"model_items,omitempty"`
	ModelOnly      bool                     `json:"model_only,omitempty"`
	ModelsPending  bool                     `json:"models_pending,omitempty"`
}

func (s *serveServer) handleMentionSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	if !mentions.EnabledFromEnv() {
		writeJSON(w, http.StatusOK, serveMentionSearchResponse{Items: []serveMentionSearchItem{}})
		return
	}
	if err := requireJSONContentType(r); err != nil {
		writeOpenAIError(w, http.StatusUnsupportedMediaType, "invalid_request_error", err.Error())
		return
	}
	var req serveMentionSearchRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if len(req.Text) > serveMentionMaxTextBytes {
		writeOpenAIError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "composer text is too large")
		return
	}
	cursor, err := wire.UTF16OffsetToByteOffset(req.Text, req.CursorUTF16)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	response := serveMentionSearchResponse{Items: []serveMentionSearchItem{}}
	token, ok := mentions.ActiveTokenAt(req.Text, cursor)
	if ok {
		s.populateModelMentions(&response, req.Text, cursor, token)
		if !response.ModelOnly {
			status, message := s.populateFileMentions(&response, r, req, token)
			// A project or index error must not hide usable model rows, or
			// prevent a loose model lookup from revalidating pending catalogs.
			if status != http.StatusOK && len(response.ModelItems) == 0 && !response.ModelsPending {
				kind := "invalid_request_error"
				if status == http.StatusInternalServerError {
					kind = "server_error"
				}
				writeOpenAIError(w, status, kind, message)
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *serveServer) populateFileMentions(response *serveMentionSearchResponse, r *http.Request, req serveMentionSearchRequest, token mentions.ActiveToken) (int, string) {
	root, err := s.resolveMentionSearchRootForProject(r.Context(), resolveRequestSessionID(r), req.ProjectID, req.WorktreeDir, req.NoProject, isFirstPartyUIResponseRequest(r))
	if err != nil {
		return http.StatusBadRequest, err.Error()
	}
	snapshot, err := s.mentionSnapshot(r.Context(), root)
	if err != nil {
		log.Printf("[serve] build project mention index for %s: %v", root, err)
		return http.StatusInternalServerError, "failed to build project mention index"
	}
	limit := req.Limit
	if limit <= 0 || limit > serveMentionSearchLimit {
		limit = serveMentionSearchLimit
	}
	matches := snapshot.Search(r.Context(), token.Query, limit)
	response.Items = mentionFileItems(snapshot, matches)
	response.Active = true
	response.Token = mentionWireToken(req.Text, token)
	response.IndexTruncated = snapshot.Truncated
	return http.StatusOK, ""
}

func mentionFileItems(snapshot *mentions.Snapshot, matches []mentions.Match) []serveMentionSearchItem {
	items := make([]serveMentionSearchItem, 0, len(matches))
	for _, match := range matches {
		if match.Candidate < 0 || match.Candidate >= len(snapshot.Candidates) {
			continue
		}
		candidate := snapshot.Candidates[match.Candidate]
		kind := "file"
		if candidate.Kind == mentions.KindDirectory {
			kind = "directory"
		}
		items = append(items, serveMentionSearchItem{
			Path:       candidate.Path,
			Kind:       kind,
			InsertText: mentions.InsertText(candidate.Path, candidate.Kind == mentions.KindDirectory),
			Segments:   mentionMatchSegments(candidate.Path, match.Positions),
		})
	}
	return items
}

func mentionWireToken(text string, token mentions.ActiveToken) *serveMentionToken {
	start, _ := wire.ByteOffsetToUTF16Offset(text, token.Start)
	end, _ := wire.ByteOffsetToUTF16Offset(text, token.End)
	return &serveMentionToken{StartUTF16: start, EndUTF16: end, Query: token.Query, Quoted: token.Quoted}
}

func mentionMatchSegments(path string, positions []int) []serveMentionSegment {
	hits := make(map[int]bool, len(positions))
	for _, position := range positions {
		hits[position] = true
	}
	segments := make([]serveMentionSegment, 0, len(positions)*2+1)
	var run strings.Builder
	matched := false
	first := true
	flush := func() {
		if run.Len() == 0 {
			return
		}
		segments = append(segments, serveMentionSegment{Text: run.String(), Matched: matched})
		run.Reset()
	}
	for offset, r := range path {
		hit := false
		for i := 0; i < utf8.RuneLen(r); i++ {
			if hits[offset+i] {
				hit = true
				break
			}
		}
		if !first && hit != matched {
			flush()
		}
		first = false
		matched = hit
		run.WriteRune(r)
	}
	flush()
	return segments
}

func (s *serveServer) trustedMentionBaseDir() (string, error) {
	cwd, err := os.Getwd()
	if s.worktreeRootFn != nil {
		cwd, err = s.worktreeRootFn()
	}
	if err != nil {
		return "", fmt.Errorf("resolve server project root: %w", err)
	}
	return canonicalizeWorktreeBoundary(cwd)
}

func (s *serveServer) resolveMentionSearchRootForProject(ctx context.Context, sessionID, projectID, requestedWorktree string, noProject, firstParty bool) (string, error) {
	if s.projectsEnabled && (firstParty || strings.TrimSpace(projectID) != "") {
		binding, err := s.resolveWorkspace(ctx, serveWorkspaceRequest{
			SessionID: sessionID, ProjectID: projectID, WorktreeDir: requestedWorktree,
			FirstPartyUI: firstParty, FreshConversation: sessionID == "", AllowNoProject: noProject,
		})
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(binding.RuntimeDir) == "" {
			return "", errors.New("choose a project before searching mentions")
		}
		return canonicalizeWorktreeBoundary(binding.RuntimeDir)
	}
	return s.resolveMentionSearchRoot(ctx, sessionID, requestedWorktree)
}

func (s *serveServer) resolveMentionSearchRoot(ctx context.Context, sessionID, requestedWorktree string) (string, error) {
	base, err := s.trustedMentionBaseDir()
	if err != nil {
		return "", err
	}
	gitRoot, gitErr := s.currentGitRoot()
	candidate := ""
	if sessionID != "" && s.store != nil {
		sess, getErr := s.store.Get(ctx, sessionID)
		if getErr == nil && sess != nil {
			candidate = strings.TrimSpace(sess.WorktreeDir)
			if candidate == "" {
				candidate = strings.TrimSpace(sess.CWD)
			}
		} else if getErr != nil && !errors.Is(getErr, session.ErrNotFound) {
			return "", fmt.Errorf("load session: %w", getErr)
		}
	}
	if candidate == "" {
		candidate = strings.TrimSpace(requestedWorktree)
		if candidate != "" {
			if gitErr != nil {
				return "", errors.New("worktree selection requires a Git project")
			}
			wt, wtErr := managedWorktreeForRoot(gitRoot, candidate)
			if wtErr != nil {
				return "", wtErr
			}
			return canonicalizeWorktreeBoundary(wt.Dir)
		}
		if gitErr == nil {
			return canonicalizeWorktreeBoundary(gitRoot)
		}
		return base, nil
	}
	resolved, err := canonicalizeWorktreeBoundary(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve session project root: %w", err)
	}
	if gitErr == nil {
		wt, wtErr := worktree.Get(resolved)
		if wtErr != nil || !sameServePath(wt.RepoRoot, gitRoot) {
			return "", errors.New("session project root does not belong to the served repository")
		}
		return canonicalizeWorktreeBoundary(wt.Dir)
	}
	if !sameServePath(resolved, base) {
		return "", errors.New("session project root does not match the served project")
	}
	return resolved, nil
}

func (s *serveServer) mentionSnapshot(ctx context.Context, root string) (*mentions.Snapshot, error) {
	for {
		now := time.Now()
		s.mentionsCacheMu.Lock()
		if s.mentionsByRoot == nil {
			s.mentionsByRoot = make(map[string]*serveMentionCacheEntry)
		}
		entry := s.mentionsByRoot[root]
		if entry == nil {
			if !s.evictMentionCacheLocked() {
				s.mentionsCacheMu.Unlock()
				return nil, errors.New("project mention index cache is busy")
			}
			entry = &serveMentionCacheEntry{lastUsed: now}
			s.mentionsByRoot[root] = entry
		}
		entry.lastUsed = now
		if entry.snapshot != nil {
			snapshot := entry.snapshot
			if now.Sub(entry.builtAt) >= serveMentionCacheTTL && entry.building == nil {
				s.startMentionBuildLocked(root, entry)
			}
			s.mentionsCacheMu.Unlock()
			return snapshot, nil
		}
		if entry.err != nil && now.Sub(entry.errorAt) < serveMentionCacheErrorTTL {
			err := entry.err
			s.mentionsCacheMu.Unlock()
			return nil, err
		}
		if entry.building == nil {
			s.startMentionBuildLocked(root, entry)
		}
		building := entry.building
		s.mentionsCacheMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-building:
		}
	}
}

func (s *serveServer) startMentionBuildLocked(root string, entry *serveMentionCacheEntry) {
	building := make(chan struct{})
	entry.building = building
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), serveMentionBuildTimeout)
		defer cancel()
		build := s.mentionBuildFn
		if build == nil {
			build = mentions.Build
		}
		snapshot, err := build(ctx, root, mentions.DefaultBuildOptions())
		s.mentionsCacheMu.Lock()
		defer s.mentionsCacheMu.Unlock()
		current := s.mentionsByRoot[root]
		if current != entry || entry.building != building {
			close(building)
			return
		}
		entry.building = nil
		entry.err = err
		if err != nil {
			entry.errorAt = time.Now()
			if entry.snapshot != nil {
				// A stale snapshot remains usable; delay the next refresh attempt so
				// transient Git failures do not spawn a subprocess on every keystroke.
				entry.builtAt = entry.errorAt
			}
		} else {
			entry.snapshot = snapshot
			entry.builtAt = time.Now()
		}
		close(building)
	}()
}

func (s *serveServer) evictMentionCacheLocked() bool {
	if len(s.mentionsByRoot) < serveMentionCacheMaxEntries {
		return true
	}
	oldestRoot := ""
	var oldest time.Time
	for root, entry := range s.mentionsByRoot {
		if entry.building != nil {
			continue
		}
		if oldestRoot == "" || entry.lastUsed.Before(oldest) {
			oldestRoot, oldest = root, entry.lastUsed
		}
	}
	if oldestRoot == "" {
		return false
	}
	delete(s.mentionsByRoot, oldestRoot)
	return true
}

func (s *serveServer) augmentMessagesWithMentions(ctx context.Context, runtime *serveRuntime, sessionID, requestedWorktree string, messages []core.Message) {
	if !mentions.EnabledFromEnv() || runtime == nil {
		return
	}
	root := ""
	if runtime.toolMgr != nil {
		root = strings.TrimSpace(runtime.toolMgr.BaseDir())
	}
	if root == "" {
		resolved, err := s.resolveMentionSearchRoot(ctx, sessionID, requestedWorktree)
		if err != nil {
			return
		}
		root = resolved
	}
	if root == "" {
		return
	}
	// A first-party @ mention is explicit authorization to attach that path.
	// LoadEagerAttachments still confines resolution to root, rejects symlink
	// escapes, and applies all file, token, binary, and directory limits.
	allowed := func(string) bool { return true }
	for i := range messages {
		if messages[i].Role != core.RoleUser || messages[i].DisplayText != "" {
			continue
		}
		text := core.MessageText(messages[i])
		visibleText := llm.StripEmbeddedFileText(text)
		if visibleText == "" {
			continue
		}
		attachments := mentions.LoadEagerAttachments(ctx, root, visibleText, allowed)
		if suffix := mentions.FormatEagerAttachments(attachments); suffix != "" {
			messages[i].DisplayText = visibleText
			messages[i].Parts = append(messages[i].Parts, core.Part{Type: core.PartText, Text: suffix})
		}
	}
}
