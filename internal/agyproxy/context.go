package agyproxy

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

const maxCatalogResponse = 8 << 20

type availableModelsCatalog struct {
	Models map[string]struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"maxTokens"`
	} `json:"models"`
}

func isAvailableModelsPath(path string) bool {
	return strings.HasSuffix(path, ":fetchAvailableModels")
}

// parseAvailableModelWindows extracts exact context-window sizes from Cloud
// Code's fetchAvailableModels payload. Malformed or oversized payloads are
// ignored so observation never affects proxied traffic.
func parseAvailableModelWindows(raw []byte, contentEncoding string) (map[string]int, bool) {
	data := raw
	if strings.EqualFold(strings.TrimSpace(contentEncoding), "gzip") {
		gz, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, false
		}
		defer gz.Close()
		decompressed, err := io.ReadAll(io.LimitReader(gz, maxCatalogResponse+1))
		if err != nil || len(decompressed) > maxCatalogResponse {
			return nil, false
		}
		data = decompressed
	}
	var catalog availableModelsCatalog
	if err := json.Unmarshal(data, &catalog); err != nil || len(catalog.Models) == 0 {
		return nil, false
	}
	windows := make(map[string]int, len(catalog.Models))
	placeholders := make(map[string]int, len(catalog.Models))
	conflictingPlaceholders := make(map[string]bool)
	for id, entry := range catalog.Models {
		if entry.MaxTokens <= 0 {
			continue
		}
		if key := strings.ToLower(strings.TrimSpace(id)); key != "" {
			windows[key] = entry.MaxTokens
		}
		if ph := strings.ToLower(strings.TrimSpace(entry.Model)); ph != "" {
			if prev, exists := placeholders[ph]; exists && prev != entry.MaxTokens {
				conflictingPlaceholders[ph] = true
			} else {
				placeholders[ph] = entry.MaxTokens
			}
		}
	}
	for ph, limit := range placeholders {
		if conflictingPlaceholders[ph] {
			continue
		}
		if _, exists := windows[ph]; !exists {
			windows[ph] = limit
		}
	}
	return windows, len(windows) > 0
}

func (s *Server) observeAvailableModels(raw []byte, contentEncoding string) {
	windows, ok := parseAvailableModelWindows(raw, contentEncoding)
	if !ok {
		return
	}
	s.mu.Lock()
	s.modelWindows = windows
	s.mu.Unlock()
}

func (s *Server) recordGenerationModel(model string) {
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	s.mu.Lock()
	s.lastGenerationModel = model
	s.mu.Unlock()
}

// LastGenerationModel returns the model ID sent in the most recent verified
// main generation request of the current turn.
func (s *Server) LastGenerationModel() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastGenerationModel
}

// ModelContextWindow returns the window observed in Cloud Code's catalog for
// an exact, case-insensitive model ID, or zero if it was not observed.
func (s *Server) ModelContextWindow(model string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.modelWindows[strings.ToLower(model)]
}

func (s *Server) streamResponse(w http.ResponseWriter, resp *http.Response, captureCatalog bool) {
	flusher, _ := w.(http.Flusher)
	var catalogBuf bytes.Buffer
	catalogOverflow := false
	buffer := make([]byte, 32<<10)
	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			chunk := buffer[:n]
			if captureCatalog && !catalogOverflow {
				if catalogBuf.Len()+len(chunk) > maxCatalogResponse {
					catalogOverflow = true
					catalogBuf.Reset()
				} else {
					_, _ = catalogBuf.Write(chunk)
				}
			}
			if _, writeErr := w.Write(chunk); writeErr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			// Parse before returning from the handler so the catalog map is
			// populated by the time the turn completes its first generation step.
			if readErr == io.EOF && captureCatalog && !catalogOverflow && catalogBuf.Len() > 0 {
				s.observeAvailableModels(catalogBuf.Bytes(), resp.Header.Get("Content-Encoding"))
			}
			return
		}
	}
}
