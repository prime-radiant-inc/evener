package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"primeradiant.com/serf/buildinfo"
	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/internal/diagnostic"
	"primeradiant.com/serf/internal/hubapi"
)

func (s *WebServer) handleApiSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	var resp searchResponse
	if s.cfg.Roster != nil {
		live := s.cfg.Roster.List()
		sort.SliceStable(live, func(i, j int) bool {
			return liveEntryWithPastLess(live[i], live[j], s.cfg.Past)
		})
		for _, le := range live {
			if le.SessionID == "" {
				continue
			}
			title := liveTitle(le.SessionID, le, s.cfg.Past)
			if q == "" || strings.Contains(strings.ToLower(le.SessionID), q) || strings.Contains(strings.ToLower(title), q) {
				resp.Live = append(resp.Live, searchResult{
					ID:      le.SessionID,
					Title:   title,
					State:   normalizeState(le.Status),
					Project: filepath.Base(le.WorkingDir),
					Age:     "now",
				})
			}
		}
	}
	if s.cfg.Past != nil {
		// Empty query → most-recent N. Substring match otherwise.
		results := s.cfg.Past.Search(q, 20, 0)
		for _, e := range results {
			resp.Past = append(resp.Past, searchResult{
				ID:      e.Meta.ID,
				Title:   searchPastTitle(e),
				State:   "ended",
				Project: filepath.Base(e.Meta.EnvInfo.WorkingDir),
				Age:     ageString(e.Meta.UpdatedAt),
			})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

func writeAPIJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAPIError(w http.ResponseWriter, status int, msg string) {
	writeAPIJSON(w, status, hubapi.ErrorResponse{Error: msg})
}

func writeAPIWireError(w http.ResponseWriter, fallbackStatus int, err error) {
	wire, ok := wireErrorFromError(err)
	if !ok {
		writeAPIError(w, fallbackStatus, err.Error())
		return
	}
	writeAPIJSON(w, statusForWireError(wire, fallbackStatus), hubapi.ErrorResponse{
		Error:         wire.Message,
		Code:          wire.Code,
		SerfErrorInfo: serfErrorInfoFromData(wire.Data),
	})
}

func wireErrorFromError(err error) (appwire.WireError, bool) {
	var wire appwire.WireError
	if errors.As(err, &wire) {
		return wire, true
	}
	return appwire.WireError{}, false
}

func statusForWireError(wire appwire.WireError, fallback int) int {
	switch wire.Code {
	case appwire.CodeInvalidParams, appwire.CodeInvalidRequest:
		return http.StatusBadRequest
	case appwire.CodeMethodNotFound:
		return http.StatusNotFound
	case appwire.CodeConflict:
		return http.StatusConflict
	case appwire.CodeUnavailable:
		return http.StatusServiceUnavailable
	case appwire.CodeInternalError:
		return http.StatusInternalServerError
	default:
		return fallback
	}
}

func serfErrorInfoFromData(data any) string {
	switch v := data.(type) {
	case appwire.ErrorData:
		return string(v.SerfErrorInfo)
	case map[string]any:
		if info, ok := v["serfErrorInfo"].(string); ok {
			return info
		}
	}
	return ""
}

func (s *WebServer) handleAPIHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	writeAPIJSON(w, http.StatusOK, hubapi.HealthResponse{
		Version:   buildinfo.Version(),
		StartedAt: s.startedAt,
		HubAddr:   s.cfg.HubAddr,
		RunDir:    s.cfg.RunDir,
		StateGlob: s.apiStateGlob(),
		Capabilities: hubapi.HealthCapabilities{
			Tree:             true,
			TranscriptFollow: true,
			SpawnSchema:      true,
			Spawn:            s.cfg.Spawner != nil || len(s.cfg.CodexSources) > 0 || len(s.cfg.CodexLaunches) > 0 || s.cfg.CodexLauncher != nil,
			Fork:             true,
			RemoteSources:    len(s.cfg.CodexSources) > 0,
		},
	})
}

func (s *WebServer) apiStateGlob() string {
	if s.cfg.Past == nil {
		return ""
	}
	return s.cfg.Past.stateGlob
}

func (s *WebServer) handleAPISpawnSchema(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	writeAPIJSON(w, http.StatusOK, hubapi.SpawnSchema{Fields: []hubapi.SpawnField{
		{Name: "prompt", Type: "text"},
		{Name: "harness", Type: "enum", Values: launchHarnessIDs(s.cfg)},
		{Name: "working_dir", Type: "path"},
		{Name: "model", Type: "model"},
		{Name: "agent", Type: "string"},
		{Name: "reasoning_effort", Type: "enum", Values: []string{"low", "medium", "high"}},
	}})
}

func warningPayload(raw json.RawMessage) map[string]any {
	message := warningMessage(raw)
	payload := map[string]any{"message": message}
	var params struct {
		Source string `json:"source"`
		Title  string `json:"title"`
		Hint   string `json:"hint"`
	}
	if json.Unmarshal(raw, &params) == nil {
		if params.Source != "" {
			payload["source"] = params.Source
		}
		if params.Title != "" {
			payload["title"] = params.Title
		}
		if params.Hint != "" {
			payload["hint"] = params.Hint
		}
	}
	addDiagnosticDefaults(payload, message)
	return payload
}

func addDiagnosticDefaults(payload map[string]any, message string) {
	info := diagnostic.Classify(message)
	if _, ok := payload["source"]; !ok {
		payload["source"] = string(info.Source)
	}
	if _, ok := payload["title"]; !ok {
		payload["title"] = info.Title
	}
	if _, ok := payload["hint"]; !ok {
		payload["hint"] = info.Hint
	}
}

func warningMessage(raw json.RawMessage) string {
	var params struct {
		Message string `json:"message"`
		Warning any    `json:"warning"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return string(raw)
	}
	if strings.TrimSpace(params.Message) != "" {
		return params.Message
	}
	switch warning := params.Warning.(type) {
	case string:
		return warning
	case map[string]any:
		if message, ok := warning["message"].(string); ok {
			return message
		}
	}
	return string(raw)
}

func (s *WebServer) handleAPIClear(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if !s.isLive(id) {
		writeAPIError(w, http.StatusNotFound, "session not live")
		return
	}
	if err := s.ensureSessionActionAvailable(id, "clear"); err != nil {
		writeAPIWireError(w, http.StatusBadGateway, err)
		return
	}
	refText := appRefFromRouteID(id)
	source, err := sourceForThread(s.sources, refText, "")
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "session not live")
		return
	}
	resp, err := source.ClearThread(r.Context(), appwire.ThreadClearParams{Ref: refText})
	if err != nil {
		writeAPIWireError(w, http.StatusBadGateway, err)
		return
	}
	outRefText := resp.Ref
	if outRefText == "" {
		outRefText = resp.Thread.Serf.Ref
	}
	ref, err := hubapi.ParseRef(outRefText)
	if err != nil {
		ref = hubapi.LocalRef(resp.Thread.ID)
	}
	writeAPIJSON(w, http.StatusOK, hubapi.RefResponse{
		Ref:       ref.String(),
		HostID:    ref.HostID,
		SessionID: ref.SessionID,
	})
}

func (s *WebServer) handleAPIModel(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if !s.isLive(id) {
		writeAPIError(w, http.StatusNotFound, "session not live")
		return
	}
	ref := appRefFromRouteID(id)
	source, err := sourceForThread(s.sources, ref, "")
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "session not live")
		return
	}
	var body struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	provider, model := splitProviderModel(body.Model)
	if model == "" {
		model = body.Model
	}
	if err := s.ensureSessionActionAvailable(id, "model"); err != nil {
		writeAPIWireError(w, http.StatusBadGateway, err)
		return
	}
	if err := source.SetThreadModel(r.Context(), appwire.ThreadModelSetParams{Ref: ref, ModelProvider: provider, Model: model}); err != nil {
		writeAPIWireError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleApiDirs returns directories matching a path prefix for the directory autocomplete.
func (s *WebServer) handleApiDirs(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	if prefix == "" {
		prefix = os.Getenv("HOME")
	}
	// Expand ~ to home.
	if strings.HasPrefix(prefix, "~/") || prefix == "~" {
		prefix = filepath.Join(os.Getenv("HOME"), strings.TrimPrefix(prefix, "~"))
	}
	// Reject traversal; preserve trailing slash so the listDir/filter logic
	// below still distinguishes "list dir contents" from "filter siblings".
	cleaned, err := sanitizeDirPrefix(prefix)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[]}`)) //nolint:errcheck
		return
	}
	prefix = cleaned

	// If prefix ends with "/", list contents of that directory.
	// Otherwise, list contents of the parent and filter by basename prefix.
	var listDir, filter string
	if strings.HasSuffix(prefix, "/") || prefix == "" {
		listDir = prefix
		if listDir == "" {
			listDir = "/"
		}
		filter = ""
	} else {
		listDir = filepath.Dir(prefix)
		filter = filepath.Base(prefix)
	}

	entries, err := os.ReadDir(listDir)
	if err != nil {
		// Return empty list rather than error — UI shows no matches.
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[]}`)) //nolint:errcheck
		return
	}

	type result struct {
		Path  string `json:"path"`
		Name  string `json:"name"`
		IsGit bool   `json:"is_git"`
	}
	var results []result
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") && filter == "" {
			continue // hide dotfiles unless user typed a dot
		}
		if filter != "" && !strings.HasPrefix(strings.ToLower(name), strings.ToLower(filter)) {
			continue
		}
		full := filepath.Join(listDir, name)
		isGit := false
		if _, gerr := os.Stat(filepath.Join(full, ".git")); gerr == nil {
			isGit = true
		}
		results = append(results, result{Path: full, Name: name, IsGit: isGit})
		if len(results) >= 30 {
			break
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"results": results}) //nolint:errcheck
}

func (s *WebServer) handleAPIPathValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	resp := validateLaunchPath(appwire.PathValidateParams{
		Path: r.URL.Query().Get("path"),
		Kind: r.URL.Query().Get("kind"),
	})
	writeAPIJSON(w, http.StatusOK, resp)
}

// gitHeadBranch runs `git rev-parse --abbrev-ref HEAD` in dir and returns
// the branch name. In detached HEAD state it falls back to the short SHA.
func gitHeadBranch(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "", err
	}
	branch := strings.TrimSpace(string(out))
	if branch == "HEAD" {
		// Detached HEAD — return short SHA instead.
		out2, err2 := exec.Command("git", "-C", dir, "rev-parse", "--short", "HEAD").Output()
		if err2 != nil {
			return branch, nil
		}
		branch = strings.TrimSpace(string(out2))
	}
	return branch, nil
}

// handleApiGitHead returns the current git HEAD branch name for a given cwd.
// Query param: cwd=<absolute path>. Returns {"branch": "<name>"} or {"branch": ""} on error.
func (s *WebServer) handleApiGitHead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	cwd := strings.TrimSpace(r.URL.Query().Get("cwd"))
	branch := ""
	if cwd != "" {
		if abs, err := filepath.Abs(cwd); err == nil {
			cwd = abs
		}
		if _, err := os.Stat(cwd); err == nil {
			if out, err := gitHeadBranch(cwd); err == nil {
				branch = out
			}
		}
	}
	writeAPIJSON(w, http.StatusOK, map[string]string{"branch": branch})
}
