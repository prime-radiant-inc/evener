package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"primeradiant.com/serf/cmd/serf-hub/internal/fspaths"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/internal/diagnostic"
	"primeradiant.com/serf/internal/hubapi"
	"primeradiant.com/serf/llm"
)

// findNewSession polls the roster up to 3s for a daemon with the given pid.
// Returns the resolved session_id when found, or empty string on timeout.
func (s *WebServer) findNewSession(pid int) string {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.cfg.Roster != nil {
			s.cfg.Roster.Refresh()
			for _, le := range s.cfg.Roster.List() {
				if le.PID == pid && le.SessionID != "" {
					return le.SessionID
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return ""
}

// handleWorkspaceSpawn renders the prompt-first spawn surface partial.
// Accepts an optional ?dir=<absolute path> query param. When present and the
// path is absolute and exists, it pre-fills the working_dir chip — used by
// the sidebar's per-project "+" button to open spawn already scoped to a
// project.
func (s *WebServer) handleWorkspaceSpawn(w http.ResponseWriter, r *http.Request) {
	defaultWorkingDir := "(pick a directory)"
	defaultWorkingDirValue := ""
	if dir := strings.TrimSpace(r.URL.Query().Get("dir")); dir != "" {
		if resolved, err := fspaths.CanonicalizeDir(dir); err == nil {
			defaultWorkingDir = resolved
			defaultWorkingDirValue = resolved
		}
	}
	data := spawnViewData{
		DefaultModel:           "(pick a model)",
		DefaultHarness:         "serf",
		DefaultWorkingDir:      defaultWorkingDir,
		DefaultWorkingDirValue: defaultWorkingDirValue,
		DefaultBranch:          "(default)",
		DefaultAccessMode:      "full",
		DefaultPrompt:          r.URL.Query().Get("prompt"),
		SafeEnv:                safeSpawnEnv(),
	}
	for _, descriptor := range launchHarnessDescriptors(s.cfg) {
		data.Harnesses = append(data.Harnesses, launchHarness{ID: descriptor.ID, Label: descriptor.Label})
	}
	if s.cfg.Past != nil {
		results := s.cfg.Past.Search("", 5, 0)
		for _, e := range results {
			if e.Meta.OriginalPrompt != "" {
				data.RecentPrompts = append(data.RecentPrompts, e.Meta.OriginalPrompt)
			}
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.spawnTmpl.ExecuteTemplate(w, "spawn", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func safeSpawnEnv() map[string]string {
	out := map[string]string{}
	for _, name := range []string{"SERF_MODEL", "SERF_REASONING_EFFORT"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			out[name] = value
		}
	}
	return out
}

func launchHarnessIDs(cfg WebConfig) []string {
	descriptors := launchHarnessDescriptors(cfg)
	out := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		out = append(out, descriptor.ID)
	}
	return out
}

// handleApiSpawn spawns a new daemon and optionally sends the initial prompt.
func (s *WebServer) handleApiSpawn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, sendMaxRequestBytes)
	var req spawnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateAppWireInputItems(req.Items); err != nil {
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	}
	for i, it := range req.Items {
		if len(it.Data) > sendMaxImageBytes {
			http.Error(w, fmt.Sprintf("items[%d] %q exceeds %d-byte limit", i, it.Name, sendMaxImageBytes), http.StatusRequestEntityTooLarge)
			return
		}
	}
	if s.cfg.Spawner == nil && len(s.cfg.CodexSources) == 0 && len(s.cfg.CodexLaunches) == 0 {
		writeSpawnError(w, appwire.Unavailable("spawner not configured"))
		return
	}
	resp, err := hubThreadStart(r.Context(), s.cfg, s.sources, appwire.ThreadStartParams{
		Harness:         req.Harness,
		CWD:             req.WorkingDir,
		Input:           append(inputItemsForText(req.Prompt), req.Items...),
		Model:           req.Model,
		Profile:         req.Agent,
		ReasoningEffort: req.ReasoningEffort,
		NonInteractive:  req.NonInteractive,
		LaunchOverrides: req.LaunchOverrides,
	})
	if err != nil {
		writeSpawnError(w, err)
		return
	}
	ref := hubRefFromAppThread(resp.Thread)
	writeAPIJSON(w, http.StatusOK, hubapi.SpawnResponse{
		Ref:       ref.String(),
		HostID:    ref.HostID,
		SessionID: ref.SessionID,
	})
}

func writeSpawnError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if wire, ok := err.(appwire.WireError); ok {
		switch wire.Code {
		case appwire.CodeInvalidParams:
			status = http.StatusBadRequest
		case appwire.CodeUnavailable:
			status = http.StatusServiceUnavailable
		}
	}
	writeAPIWireError(w, status, err)
}

// handleApiModels returns the models the hub can spawn for. Hub-owned Serf
// launches report their model choices through the Serf launch harness contract;
// the direct live provider query remains a fallback for tests and non-spawning
// server configurations. Pricing and context-window metadata come from the
// embedded catalog where provider APIs don't carry it.
func (s *WebServer) handleApiModels(w http.ResponseWriter, r *http.Request) {
	harness := strings.TrimSpace(r.URL.Query().Get("harness"))
	workingDir := strings.TrimSpace(r.URL.Query().Get("cwd"))
	if harness != "" && harness != "serf" && harness != "local" {
		resp, err := hubModelList(r.Context(), s.cfg, s.sources, appwire.ModelListParams{Harness: harness, CWD: workingDir})
		if err != nil {
			writeAPIWireError(w, http.StatusBadGateway, err)
			return
		}
		writeAPIJSON(w, http.StatusOK, modelDescriptorsToAPIModels(resp.Data))
		return
	}

	launchResp, err := serfLaunchModelList(r.Context(), s.cfg, workingDir)
	if err != nil && hasSerfLaunchModelLister(s.cfg) {
		writeAPIWireError(w, http.StatusBadGateway, err)
		return
	}
	models := modelDescriptorsToAPIModels(launchResp.Data)
	if len(models) == 0 && !hasSerfLaunchModelLister(s.cfg) {
		models = s.fetchLiveModels(r.Context())
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(models) //nolint:errcheck
}

func serfLaunchModelsOrEmpty(ctx context.Context, cfg WebConfig) []appwire.ModelDescriptor {
	return serfLaunchModelListOrEmpty(ctx, cfg).Data
}

func serfLaunchModelListOrEmpty(ctx context.Context, cfg WebConfig) appwire.ModelListResponse {
	resp, err := serfLaunchModelList(ctx, cfg, "")
	if err != nil {
		return appwire.ModelListResponse{}
	}
	return resp
}

func launchModelListErrorDiagnostic(err error) appwire.ModelListDiagnostic {
	info := diagnostic.FromFields(string(diagnostic.SourceHub), "Model list unavailable", "", err.Error())
	return appwire.ModelListDiagnostic{
		Source:  string(info.Source),
		Title:   info.Title,
		Message: err.Error(),
		Hint:    info.Hint,
	}
}

func modelDescriptorsToAPIModels(models []appwire.ModelDescriptor) []map[string]any {
	if len(models) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(models))
	cat := llm.EmbeddedModelCatalog()
	for _, m := range models {
		if m.Provider == "" || m.Model == "" {
			continue
		}
		entry := map[string]any{
			"provider": m.Provider,
			"model":    m.Model,
		}
		if cat != nil {
			if mi := cat.GetModelInfo(m.Model); mi != nil {
				entry["display_name"] = mi.DisplayName
				entry["context_window"] = mi.ContextWindow
				entry["supports_tools"] = mi.SupportsTools
				entry["supports_reasoning"] = mi.SupportsReasoning
				entry["input_cost_per_million"] = mi.InputCostPerMillion
				entry["output_cost_per_million"] = mi.OutputCostPerMillion
			}
		}
		out = append(out, entry)
	}
	return out
}

func (s *WebServer) fetchLiveModels(ctx context.Context) []map[string]any {
	liveModelsCache.mu.Lock()
	if time.Now().Before(liveModelsCache.expires) && liveModelsCache.models != nil {
		out := liveModelsCache.models
		liveModelsCache.mu.Unlock()
		return out
	}
	liveModelsCache.mu.Unlock()

	c, _, _, err := cmdutil.LoadClient()
	if err != nil || c == nil {
		return nil
	}
	cat := llm.EmbeddedModelCatalog()

	var out []map[string]any
	for _, prov := range c.ProviderNames() {
		tag := c.BehaviorTagOf(prov)
		// Skip dual-route variants that surface the same models as their
		// primary route. openrouter-anthropic instances exist for specific
		// models whose tool-calling format requires the Anthropic-Messages
		// endpoint, but they list the same /models response as plain
		// openrouter. The daemon picks the correct route based on the model
		// name when spawning; the picker doesn't need to expose both.
		if tag == "openrouter-anthropic" {
			continue
		}
		listCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		models, lerr := c.ListModels(listCtx, prov)
		cancel()
		if lerr != nil {
			// Provider doesn't support live listing or call failed —
			// skip this provider rather than fall back to a stale catalog.
			// User sees only providers we can confidently report on.
			continue
		}
		for _, m := range models {
			lower := strings.ToLower(m.ID)
			// Skip non-chat / non-completion models from the live list.
			if strings.Contains(lower, "embedding") ||
				strings.Contains(lower, "whisper") ||
				strings.Contains(lower, "tts") ||
				strings.Contains(lower, "dall-e") ||
				strings.Contains(lower, "moderation") ||
				strings.Contains(lower, "audio") ||
				strings.Contains(lower, "transcribe") ||
				strings.Contains(lower, "image") {
				continue
			}
			mi := catalogModelInfo(cat, m.ID)
			if tag == "openrouter" && (mi == nil || !mi.SupportsTools) {
				continue
			}
			// Use the registered provider name (prov), not m.Provider — wrapper
			// adapters like openrouter forward to openaicompat which reports
			// itself as "openai-compatible". The hub's spawn flow needs the
			// registered name (openrouter, openrouter-anthropic, etc.) so the
			// daemon spawns with the right adapter.
			entry := map[string]any{
				"provider":     prov,
				"model":        m.ID,
				"display_name": m.DisplayName,
			}
			if m.ContextWindow > 0 {
				entry["context_window"] = m.ContextWindow
			}
			if m.SupportsTools {
				entry["supports_tools"] = true
			}
			if m.SupportsReasoning {
				entry["supports_reasoning"] = true
			}
			// Keep catalog enrichment for static pricing/capability hints, but
			// do not replace live token limits with catalog values.
			if mi != nil {
				entry["input_cost_per_million"] = mi.InputCostPerMillion
				entry["output_cost_per_million"] = mi.OutputCostPerMillion
				if _, ok := entry["supports_tools"]; !ok {
					entry["supports_tools"] = mi.SupportsTools
				}
				if _, ok := entry["supports_reasoning"]; !ok {
					entry["supports_reasoning"] = mi.SupportsReasoning
				}
			}
			out = append(out, entry)
		}
	}

	liveModelsCache.mu.Lock()
	liveModelsCache.models = out
	liveModelsCache.expires = time.Now().Add(liveModelsTTL)
	liveModelsCache.mu.Unlock()
	return out
}

func catalogModelInfo(cat *llm.ModelCatalog, modelID string) *llm.ModelInfo {
	if cat == nil {
		return nil
	}
	return cat.GetModelInfo(modelID)
}
