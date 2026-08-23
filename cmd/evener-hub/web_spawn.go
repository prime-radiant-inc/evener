package hub

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providercfg"
)

var webSpawnLoadClient = cmdutil.LoadClient

func sandboxForAccessMode(mode string) string {
	mode = strings.TrimSpace(mode)
	switch mode {
	case "full":
		return "off"
	case "read-only", "workspace-write", "restricted":
		return mode
	default:
		return ""
	}
}

func launchOverridesWithAccessMode(overrides *appwire.LaunchConfigLayer, accessMode string) *appwire.LaunchConfigLayer {
	sandbox := sandboxForAccessMode(accessMode)
	if sandbox == "" {
		return overrides
	}
	if overrides == nil {
		return &appwire.LaunchConfigLayer{Sandbox: sandbox}
	}
	if strings.TrimSpace(overrides.Sandbox) != "" {
		return overrides
	}
	next := *overrides
	next.Sandbox = sandbox
	return &next
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
		LaunchOverrides: launchOverridesWithAccessMode(req.LaunchOverrides, req.AccessMode),
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

// handleApiModels returns the models the hub can spawn for. Hub-owned Evener
// launches report their model choices through the Evener launch harness contract;
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
	if harness != "" && harness != "evener" && harness != "local" {
		resp, err := hubModelList(r.Context(), s.cfg, s.sources, appwire.ModelListParams{Harness: harness, CWD: workingDir})
		if err != nil {
			writeAPIWireError(w, http.StatusBadGateway, err)
			return
		}
		models := modelDescriptorsToAPIModels(resp.Data, s.cfg.ProviderConfig)
		writeModelsResponse(w, models, resp.Diagnostics, recentModelEntriesFromDescriptors(models, resp.Recent), includeDiagnostics)
		return
	}

	launchResp, err := evenerLaunchModelList(r.Context(), s.cfg, workingDir)
	if err != nil && hasEvenerLaunchModelLister(s.cfg) {
		writeAPIWireError(w, http.StatusBadGateway, err)
		return
	}
	models := modelDescriptorsToAPIModels(launchResp.Data, s.cfg.ProviderConfig)
	if len(models) == 0 && !hasEvenerLaunchModelLister(s.cfg) {
		liveModels := s.fetchLiveModels
		if s.cfg.LiveModels != nil {
			liveModels = s.cfg.LiveModels
		}
		models = liveModels(r.Context())
	}
	var recentRefs []appwire.ModelDescriptor
	if s.cfg.Past != nil {
		recentRefs = s.cfg.Past.RecentModels(recentModelsLimit)
	}
	writeModelsResponse(w, models, launchResp.Diagnostics, recentModelEntriesFromDescriptors(models, recentRefs), includeDiagnostics)
}

// writeModelsResponse writes the model list as a bare JSON array by default, or
// as {"models": [...], "diagnostics": [...], "recent": [...]} when the caller
// opted into diagnostics. Diagnostics and recent serialize as empty arrays
// (never null) so clients can iterate unconditionally.
func writeModelsResponse(w http.ResponseWriter, models []map[string]any, diagnostics []appwire.ModelListDiagnostic, recent []map[string]any, includeDiagnostics bool) {
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
	if recent == nil {
		recent = []map[string]any{}
	}
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"models":      models,
		"diagnostics": diagnostics,
		"recent":      recent,
	})
}

// datedSnapshotSuffix matches a trailing dated-snapshot suffix on a bare
// model id (e.g. "-20251101"), plus an optional trailing LiteLLM version tag
// ("-v1") — mirrors llm/model_catalog_embedded.go's datedModelSuffix.
// Duplicated (not exported from llm) because llm/model_catalog_embedded.go
// isn't owned by this track — see the plan's Global Constraints.
var datedSnapshotSuffix = regexp.MustCompile(`-\d{8}(-v\d+)?$`)

// isDatedSnapshotModelID reports whether ref's model segment (the part after
// the last "/", if any) carries a dated-snapshot suffix.
func isDatedSnapshotModelID(ref string) bool {
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		ref = ref[i+1:]
	}
	return datedSnapshotSuffix.MatchString(ref)
}

// prettifyModelDisplayName turns a raw model id into a human-readable label
// with no hand-maintained per-model name table (Decision #8): it strips a
// trailing dated-snapshot suffix, splits on '-', and capitalizes each
// segment's first rune, leaving the rest (numbers, "4.5", "70b", ...)
// untouched. Deliberately simple.
func prettifyModelDisplayName(id string) string {
	base := datedSnapshotSuffix.ReplaceAllString(id, "")
	segments := strings.Split(base, "-")
	for idx, seg := range segments {
		if seg == "" {
			continue
		}
		r := []rune(seg)
		r[0] = unicode.ToUpper(r[0])
		segments[idx] = string(r)
	}
	return strings.Join(segments, " ")
}

// sortModelEntriesDatedLast stable-sorts model entries by provider, then
// pushes dated-snapshot ids to the end of their provider's group; order is
// otherwise preserved (whatever order the source returned).
func sortModelEntriesDatedLast(entries []map[string]any) {
	sort.SliceStable(entries, func(i, j int) bool {
		pi, _ := entries[i]["provider"].(string)
		pj, _ := entries[j]["provider"].(string)
		if pi != pj {
			return pi < pj
		}
		mi, _ := entries[i]["model"].(string)
		mj, _ := entries[j]["model"].(string)
		di, dj := isDatedSnapshotModelID(mi), isDatedSnapshotModelID(mj)
		if di != dj {
			return !di
		}
		return false
	})
}

// recentModelEntriesFromDescriptors resolves Recent model refs (Provider,
// Model pairs, already deduped/limited/most-recent-first) against the
// already-built, already-enriched models list, returning the matching
// entries (same maps, so they carry the same badges) in refs' order. A ref
// with no match in models is silently dropped.
func recentModelEntriesFromDescriptors(models []map[string]any, refs []appwire.ModelDescriptor) []map[string]any {
	if len(refs) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(refs))
	for _, ref := range refs {
		for _, m := range models {
			p, _ := m["provider"].(string)
			mod, _ := m["model"].(string)
			if p == ref.Provider && mod == ref.Model {
				out = append(out, m)
				break
			}
		}
	}
	return out
}

// modelDescriptorsToAPIModels builds the /api/models response entries. The
// embedded catalog supplies default metadata (display name, context window,
// cost, effort levels); when providerCfg carries a providers.toml instance
// that defines the model, its ModelConfig wins — an instance author who sets
// custom ThinkingLevels or a context_window knows their deployment better
// than the catalog's guess for the bare model id.
func modelDescriptorsToAPIModels(models []appwire.ModelDescriptor, providerCfg *providercfg.Config) []map[string]any {
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
			"provider":     m.Provider,
			"model":        m.Model,
			"display_name": prettifyModelDisplayName(m.Model),
		}
		if cat != nil {
			if mi := catalogModelInfo(cat, behaviorTagFor(providerCfg, m.Provider), m.Model); mi != nil {
				entry["context_window"] = mi.ContextWindow
				entry["supports_tools"] = mi.SupportsTools
				entry["supports_vision"] = mi.SupportsVision
				if mi.MaxOutputTokens != nil {
					entry["max_output_tokens"] = *mi.MaxOutputTokens
				}
				if mi.SupportsWebSearch != nil {
					entry["supports_web_search"] = *mi.SupportsWebSearch
				}
				entry["supports_reasoning"] = mi.SupportsReasoning
				entry["input_cost_per_million"] = mi.InputCostPerMillion
				entry["output_cost_per_million"] = mi.OutputCostPerMillion
				if len(mi.ReasoningEffortLevels) > 0 {
					entry["reasoning_effort_levels"] = mi.ReasoningEffortLevels
				}
			}
		}
		applyInstanceModelOverride(entry, providerCfg, m.Provider, m.Model)
		out = append(out, entry)
	}
	sortModelEntriesDatedLast(out)
	return out
}

// applyInstanceModelOverride overlays a providers.toml instance's ModelConfig
// (when one is defined for m.Provider/m.Model) onto an /api/models entry
// already populated from the embedded catalog. Mirrors the precedence
// newOpenAICompatProfile applies at session-build time (agent/provider) so the
// spawn-form effort chip matches the levels the session will actually honor.
func applyInstanceModelOverride(entry map[string]any, providerCfg *providercfg.Config, provider, model string) {
	if providerCfg == nil {
		return
	}
	var inst *providercfg.InstanceConfig
	for i := range providerCfg.Instances {
		if providerCfg.Instances[i].Name == provider {
			inst = &providerCfg.Instances[i]
			break
		}
	}
	if inst == nil {
		return
	}
	mc, ok := inst.Models[model]
	if !ok {
		return
	}
	switch {
	case mc.Reasoning != nil && !*mc.Reasoning:
		// A declared non-reasoning model advertises no effort levels at all.
		entry["reasoning_effort_levels"] = []string{}
		entry["supports_reasoning"] = false
	case len(mc.ThinkingLevels) > 0:
		entry["reasoning_effort_levels"] = llm.OrderedEffortLevels(mc.ThinkingLevels)
		entry["supports_reasoning"] = true
	case mc.Reasoning != nil && *mc.Reasoning:
		// reasoning = true without custom levels: the model IS
		// reasoning-capable even if the live/catalog data says otherwise;
		// levels stay as derived (the UI falls back to the default ladder).
		entry["supports_reasoning"] = true
	}
	if mc.ContextWindow > 0 {
		entry["context_window"] = mc.ContextWindow
	}
}

func (s *WebServer) fetchLiveModels(ctx context.Context) []map[string]any {
	s.liveModels.mu.Lock()
	if time.Now().Before(s.liveModels.expires) && s.liveModels.models != nil {
		out := s.overlayLiveEntries(s.liveModels.models)
		s.liveModels.mu.Unlock()
		return out
	}
	s.liveModels.mu.Unlock()

	c, _, _, err := webSpawnLoadClient()
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
			// VisibleLiveModel applies the shared non-chat skip rule and, for
			// the openrouter tag, the live/catalog tool-capability filter.
			// This is the same rule the launch-check path uses
			// (llm.VisibleLiveModel), so the two live-model paths cannot drift.
			if !cat.VisibleLiveModel(tag, m) {
				continue
			}
			mi := catalogModelInfo(cat, tag, m.ID)
			// Use the registered provider name (prov), not m.Provider — wrapper
			// adapters like openrouter forward to openaicompat which reports
			// itself as "openai-compatible". The hub's spawn flow needs the
			// registered name (openrouter, openrouter-anthropic, etc.) so the
			// daemon spawns with the right adapter.
			entry := map[string]any{
				"provider":     prov,
				"model":        m.ID,
				"display_name": prettifyModelDisplayName(m.ID),
			}
			// --- Live-first population: values parsed from the provider's
			// /models response are authoritative when the provider reported
			// them. The catalog only fills gaps the live response left open.
			if m.ContextWindow > 0 {
				entry["context_window"] = m.ContextWindow
			}
			if m.CapabilitiesAdvertised {
				// The provider explicitly reported capabilities — emit them
				// even when false (e.g. SupportsTools=false means the provider
				// says no tools, which is different from "unknown").
				entry["supports_tools"] = m.SupportsTools
				entry["supports_vision"] = m.SupportsVision
				entry["supports_reasoning"] = m.SupportsReasoning
			} else {
				// Provider didn't report capabilities — emit only the true
				// values (zero-value false is not authoritative).
				if m.SupportsTools {
					entry["supports_tools"] = true
				}
				if m.SupportsReasoning {
					entry["supports_reasoning"] = true
				}
			}
			if len(m.ReasoningEffortLevels) > 0 {
				entry["reasoning_effort_levels"] = m.ReasoningEffortLevels
			}
			if m.InputCostPerMillion != nil {
				entry["input_cost_per_million"] = *m.InputCostPerMillion
			}
			if m.OutputCostPerMillion != nil {
				entry["output_cost_per_million"] = *m.OutputCostPerMillion
			}
			if m.MaxOutputTokens != nil {
				entry["max_output_tokens"] = *m.MaxOutputTokens
			}
			// --- Catalog enrichment: fill fields the live response didn't
			// provide. Never overwrite a live value with a catalog value.
			if mi != nil {
				if _, ok := entry["context_window"]; !ok && mi.ContextWindow > 0 {
					entry["context_window"] = mi.ContextWindow
				}
				if _, ok := entry["input_cost_per_million"]; !ok && mi.InputCostPerMillion != nil {
					entry["input_cost_per_million"] = *mi.InputCostPerMillion
				}
				if _, ok := entry["output_cost_per_million"]; !ok && mi.OutputCostPerMillion != nil {
					entry["output_cost_per_million"] = *mi.OutputCostPerMillion
				}
				if _, ok := entry["supports_tools"]; !ok && mi.SupportsTools {
					entry["supports_tools"] = mi.SupportsTools
				}
				if _, ok := entry["supports_reasoning"]; !ok && mi.SupportsReasoning {
					entry["supports_reasoning"] = mi.SupportsReasoning
				}
				if _, ok := entry["reasoning_effort_levels"]; !ok && len(mi.ReasoningEffortLevels) > 0 {
					entry["reasoning_effort_levels"] = mi.ReasoningEffortLevels
				}
				if _, ok := entry["supports_vision"]; !ok && mi.SupportsVision {
					entry["supports_vision"] = mi.SupportsVision
				}
				if _, ok := entry["max_output_tokens"]; !ok && mi.MaxOutputTokens != nil {
					entry["max_output_tokens"] = *mi.MaxOutputTokens
				}
				if mi.SupportsWebSearch != nil {
					if _, ok := entry["supports_web_search"]; !ok {
						entry["supports_web_search"] = *mi.SupportsWebSearch
					}
				}
			}
			out = append(out, entry)
		}
	}

	// The cache is per-WebServer (each server's provider set/config owns its
	// own entries) and holds RAW live values; providers.toml overrides are
	// applied per request on fresh copies (overlayLiveEntries).
	sortModelEntriesDatedLast(out)
	s.liveModels.mu.Lock()
	s.liveModels.models = out
	s.liveModels.expires = time.Now().Add(liveModelsTTL)
	s.liveModels.mu.Unlock()
	return s.overlayLiveEntries(out)
}

// overlayLiveEntries returns fresh copies of raw live-model entries with this
// server's providers.toml instance overrides applied. The copies keep the
// server's live-models cache config-agnostic: overlays never mutate cached
// state.
func (s *WebServer) overlayLiveEntries(entries []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		clone := make(map[string]any, len(entry))
		maps.Copy(clone, entry)
		prov, _ := clone["provider"].(string)
		model, _ := clone["model"].(string)
		applyInstanceModelOverride(clone, s.cfg.ProviderConfig, prov, model)
		out = append(out, clone)
	}
	return out
}

func catalogModelInfo(cat *llm.ModelCatalog, behaviorTag, modelID string) *llm.ModelInfo {
	// Delegates to the shared llm.ModelCatalog.ResolveLiveModelInfo rule so the
	// hub /api/models path and the launch-check path share one resolution: bare
	// LookupModelInfo first (handles "[1m]", provider namespaces, dated
	// snapshots, curated overrides), then the exact tag-qualified key as a
	// fallback so openrouter-only listings (keyed "openrouter/<model>") resolve.
	return cat.ResolveLiveModelInfo(behaviorTag, modelID)
}

// behaviorTagFor resolves an instance name to its behavior tag via the loaded
// providers.toml; env-seeded instances are named after their type, so the
// name doubles as the tag when no config entry matches.
func behaviorTagFor(providerCfg *providercfg.Config, name string) string {
	if providerCfg != nil {
		for i := range providerCfg.Instances {
			if providerCfg.Instances[i].Name == name {
				return providercfg.BehaviorTag(string(providerCfg.Instances[i].Type), string(providerCfg.Instances[i].APIStyle))
			}
		}
	}
	return name
}
