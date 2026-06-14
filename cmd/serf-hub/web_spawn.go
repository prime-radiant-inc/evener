package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/fspaths"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/hubapi"
	"primeradiant.com/serf/internal/diagnostic"
	"primeradiant.com/serf/llm"
)

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

func launchHarnessIDs(cfg hubcore.WebConfig) []string {
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
	r.Body = http.MaxBytesReader(w, r.Body, hubcore.SendMaxRequestBytes)
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
		if len(it.Data) > hubcore.SendMaxImageBytes {
			http.Error(w, fmt.Sprintf("items[%d] %q exceeds %d-byte limit", i, it.Name, hubcore.SendMaxImageBytes), http.StatusRequestEntityTooLarge)
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
	var wire appwire.WireError
	if errors.As(err, &wire) {
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
	// With ?diagnostics=1 the response carries both models and the launch-check
	// diagnostics so the picker can show why a configured provider is missing
	// instead of silently dropping it. The default response stays a bare array
	// for the settings and command-palette consumers.
	includeDiagnostics := r.URL.Query().Get("diagnostics") == "1"
	if harness != "" && harness != "serf" && harness != "local" {
		resp, err := hubModelList(r.Context(), s.cfg, s.sources, appwire.ModelListParams{Harness: harness, CWD: workingDir})
		if err != nil {
			writeAPIWireError(w, http.StatusBadGateway, err)
			return
		}
		writeModelsResponse(w, modelDescriptorsToAPIModels(resp.Data), resp.Diagnostics, includeDiagnostics)
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
	writeModelsResponse(w, models, launchResp.Diagnostics, includeDiagnostics)
}

// writeModelsResponse writes the model list as a bare JSON array by default, or
// as {"models": [...], "diagnostics": [...]} when the caller opted into
// diagnostics. Diagnostics serialize as an empty array (never null) so clients
// can iterate unconditionally.
func writeModelsResponse(w http.ResponseWriter, models []map[string]any, diagnostics []appwire.ModelListDiagnostic, includeDiagnostics bool) {
	w.Header().Set("Content-Type", "application/json")
	if !includeDiagnostics {
		json.NewEncoder(w).Encode(models) //nolint:errcheck
		return
	}
	if models == nil {
		models = []map[string]any{}
	}
	if diagnostics == nil {
		diagnostics = []appwire.ModelListDiagnostic{}
	}
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"models":      models,
		"diagnostics": diagnostics,
	})
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
			if mi := catalogModelInfo(cat, m.Model); mi != nil {
				entry["display_name"] = mi.DisplayName
				entry["context_window"] = mi.ContextWindow
				entry["supports_tools"] = mi.SupportsTools
				entry["supports_reasoning"] = mi.SupportsReasoning
				entry["input_cost_per_million"] = mi.InputCostPerMillion
				entry["output_cost_per_million"] = mi.OutputCostPerMillion
				if len(mi.ReasoningEffortLevels) > 0 {
					entry["reasoning_effort_levels"] = mi.ReasoningEffortLevels
				}
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
			// Prefer effort levels the provider advertised live; fall back to the
			// catalog below.
			if len(m.ReasoningEffortLevels) > 0 {
				entry["reasoning_effort_levels"] = m.ReasoningEffortLevels
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
				if _, ok := entry["reasoning_effort_levels"]; !ok && len(mi.ReasoningEffortLevels) > 0 {
					entry["reasoning_effort_levels"] = mi.ReasoningEffortLevels
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
	// LookupModelInfo canonicalizes the "[1m]" suffix, a provider namespace
	// (e.g. "anthropic/claude-opus-4-6" served by an openrouter-anthropic
	// instance), and dated snapshots so per-model metadata (context window,
	// effort levels) is found for qualified/dated/1M model refs.
	return cat.LookupModelInfo(modelID)
}
