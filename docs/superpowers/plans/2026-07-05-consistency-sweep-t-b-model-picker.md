# Consistency Sweep — Track B: Model Picker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn all three model pickers (web spawn `openModelPicker`, web settings `buildModelPicker`, TUI `tuipick.ModelPicker`) from a raw alphabetical id-dump into a `Recent` group (last 5 distinct models, global recency, derived from the Past index) atop a browsable, provider-grouped full catalog with auto-prettified display names and catalog-metadata badges (tools/vision/reasoning+effort/web-search/context-window/max-output/price), with dated snapshots sorted last and uncatalogued live models still rendering (name+provider+id, no badges).

**Architecture:** The hub server (`cmd/serf-hub`) is the single place that joins the live/launch model list against the embedded `llm` catalog; this plan extends that join with three cross-cutting pieces of enrichment — capability badges, auto-prettified display names, and dated-snapshot-last ordering — applied uniformly wherever a `[]map[string]any` model entry list is built (`modelDescriptorsToAPIModels`, `fetchLiveModels`). A new read-only `PastIndex.RecentModels` query (global recency, deduped) feeds a new `Recent` field carried on `appwire.ModelListResponse` (a struct-field addition, no new wire method) and on the web's `/api/models?diagnostics=1` JSON envelope. Both consumers — the appwire-only TUI and the REST-only web JS — read the same server-computed enrichment; the TUI additionally does its own lightweight catalog lookup (`llm.EmbeddedModelCatalog()`, imported directly since `cmd/serf-tui` lives in the root Go module) for its compact per-row metadata tail, since the appwire wire type carries only bare `{provider, model}` pairs. All three picker UIs (spawn.js, settings-pickers.js, `tuipick.ModelPicker`) render the same shape: a `Recent` group, then provider groups (dated snapshots last within each), each row showing a prettified name plus a raw-id secondary line and capability badges.

**Tech Stack:** Go (root module: `appwire`, `cmd/serf-hub`, `cmd/serf-hub/internal/hubcore`, `cmd/serf-tui`, `cmd/serf-tui/internal/tuipick`; `llm` package consumed read-only, not modified), vanilla JS + JSDOM jstest (`cmd/serf-hub/assets/spawn.js`, `settings-pickers.js`), CSS (`cmd/serf-hub/assets/style.css`).

## Global Constraints

- JSON/TOML keys stay snake_case (the sweep's guardrail); Go struct fields for the TUI's in-memory `ModelPickerItem` are not wire types and are exempt, but every new appwire/JSON field in this track (`recent`) must be snake_case.
- `make lint` (namingcheck runs only here) must pass before merge; any deliberate camelCase wire field needs a `// serf:naming-ignore:` line (none is expected in this track — all new fields are snake_case or Go-internal).
- New appwire **struct fields** (e.g. `ModelListResponse.Recent`) do not require the dual-router catalog change; only new **methods** do. This track adds no new appwire method.
- Per-repo `GO_MODULES` (root, `agent`, `llm`, `auth`, `envvars`, `fuzz`, `invariant`): this track touches only the **root** module (`appwire`, `cmd/serf-hub`, `cmd/serf-tui`) plus read-only imports of the already-built `llm` module; run tests with `go test ./<pkg>/... -run '<Name>' -count=1` from the repo root, and `golangci-lint run ./...` from the repo root.
- **Out of scope (verbatim from spec):** "A hand-maintained curated model manifest (explicitly rejected in favor of Recent)." / "Model `pricing.go` becoming the picker's pricing path (§4 keeps the direct field read; only §5 cost adopts `pricing.go`)." This plan never imports `llm/pricing.go` and never introduces a manifest file; `Recent` is derived purely from `PastIndex`.
- `llm/model_catalog.go` and `llm/model_catalog_embedded.go` are **not owned by this track** (no other track claims them either) — this plan deliberately does not modify them, even though a private `datedModelSuffix` regex already exists there. Each consumer (`cmd/serf-hub`, `cmd/serf-tui`) defines its own small, duplicated dated-snapshot regex and prettify helper rather than exporting from `llm`, to avoid an un-owned cross-track edit.
- jstest is agent-run, not part of `make`/CI: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node <file>.js` (or `sh run-all.sh` for the full suite; one-time jsdom install per `jstest/README.md`).
- Never `git add -A`; stage only the exact paths listed in each task's commit step (after a `git status`).

---

## File Structure

New/changed responsibilities, in dependency order:

- `appwire/types.go` — `ModelListResponse` gains `Recent []ModelDescriptor` (wire shape only; no behavior).
- `cmd/serf-hub/internal/hubcore/past.go` — `PastIndex.RecentModels(limit int) []appwire.ModelDescriptor`: the global-recency, deduped query over the already-sorted `PastIndex.all`, keyed on `(Meta.ProfileID, Meta.Model)`.
- `cmd/serf-hub/app_models.go` — `hubModelList` (the single appwire-dispatch entry point used by every `ModelList` RPC, including the TUI's) attaches `Recent` via a new `attachRecentModels` wrapper, regardless of the harness selected (Recent is harness-independent per the spec).
- `cmd/serf-hub/web_spawn.go` — `modelDescriptorsToAPIModels` and `fetchLiveModels` (the two builders of the web's `/api/models` entries) gain: auto-prettified `display_name` (replacing the raw-id passthrough), three new badge fields (`supports_vision`, `supports_web_search`, `max_output_tokens`), and dated-snapshot-last ordering within each provider. `handleApiModels`/`writeModelsResponse` gain a `recent` field on the diagnostics-envelope JSON response, resolved from the Past index (default harness path) or from `hubModelList`'s `Recent` (non-serf harness path).
- `cmd/serf-hub/assets/spawn.js` — `openModelPicker`'s `renderList` renders a `Recent` group above the provider groups; each row shows the prettified `display_name` primary with the raw id as a dim secondary line, plus a capability-badge row. A `buildModelRow`/`modelBadges` helper pair is factored out of the existing inline row-building code.
- `cmd/serf-hub/assets/settings-pickers.js` — `buildModelPicker`'s two-column layout gains a pinned-first `Recent` pseudo-provider tab; `renderModels` gets the same display_name/badges treatment as spawn.js (small, deliberate duplication — the two files already duplicate `formatCtx`).
- `cmd/serf-hub/assets/style.css` — a small, new, self-contained CSS block for `.chip-picker-model-id` (secondary raw-id line) and `.chip-picker-model-badges`/`.chip-picker-badge` (capability pills), appended next to the existing `.chip-picker-model-meta` rule; does not touch Track A's state/glyph regions.
- `cmd/serf-tui/internal/tuipick/model_picker.go` — `ModelPickerItem` gains `Group string` (rendered as a sticky-less header line on a group transition) and `Meta string` (a compact caps/ctx/price tail appended to the row); zero-value for both preserves today's rendering for `NewTranscriptPicker`/`NewActionPicker`.
- `cmd/serf-tui/hub_commands.go` — `modelPickerItems` sets `Group` (provider), prettifies `Display`, computes `Meta` from a direct `llm.EmbeddedModelCatalog()` lookup (no providers.toml/behaviorTag resolution available to the TUI process — a documented, accepted simplification), and dated-snapshot-last sorts the result; `modelPickerItemsFromResponse` prepends a `Recent`-grouped set of items built from `resp.Recent`.

---

## Task 1 — `appwire.ModelListResponse.Recent` wire field

**Files:**
- Modify: `appwire/types.go:768-771` (`ModelListResponse`)
- Test: `appwire/types_test.go`

**Interfaces:**
- Produces: `appwire.ModelListResponse.Recent []appwire.ModelDescriptor` (json tag `recent,omitempty`), consumed by Task 3 (`hubModelList`), Task 6 (`handleApiModels`), and Task 12 (TUI `modelPickerItemsFromResponse`).

- [ ] **Failing test** — add to `appwire/types_test.go`:
```go
// TestModelListResponseRecentJSONRoundTrip verifies the model picker's
// Recent group rides ModelListResponse as an ordinary struct field (no new
// appwire method), snake_case on the wire, and round-trips.
func TestModelListResponseRecentJSONRoundTrip(t *testing.T) {
	in := ModelListResponse{
		Data:   []ModelDescriptor{{Provider: "anthropic", Model: "claude-opus-4-6"}},
		Recent: []ModelDescriptor{{Provider: "openai", Model: "gpt-5.2"}},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	if !strings.Contains(got, `"recent":[{"provider":"openai","model":"gpt-5.2"}]`) {
		t.Fatalf("marshal=%s missing recent", got)
	}
	var out ModelListResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Recent) != 1 || out.Recent[0] != in.Recent[0] {
		t.Fatalf("roundtrip recent=%+v, want %+v", out.Recent, in.Recent)
	}
}

// TestModelListResponseRecentOmitEmpty verifies a response with no recent
// models (fresh install, no history) omits the field entirely rather than
// rendering an empty array.
func TestModelListResponseRecentOmitEmpty(t *testing.T) {
	raw, err := json.Marshal(ModelListResponse{Data: []ModelDescriptor{{Provider: "a", Model: "b"}}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), `"recent"`) {
		t.Fatalf("marshal=%s should have omitted recent", raw)
	}
}
```
Run: `go test ./appwire/... -run 'TestModelListResponseRecent' -count=1` → expect FAIL (compile error: `Recent` undefined on `ModelListResponse`).

- [ ] **Implement** — in `appwire/types.go`, change:
```go
type ModelListResponse struct {
	Data        []ModelDescriptor     `json:"data"`
	Diagnostics []ModelListDiagnostic `json:"diagnostics,omitempty"`
}
```
to:
```go
type ModelListResponse struct {
	Data        []ModelDescriptor     `json:"data"`
	Diagnostics []ModelListDiagnostic `json:"diagnostics,omitempty"`
	// Recent carries the model picker's "Recent" group: the last N distinct
	// models across all sessions, globally by recency (not scoped to the
	// currently selected harness/project), derived from the Past index. Empty
	// on a fresh install with no session history. A struct field, not a new
	// appwire method — no dual-router catalog change required.
	Recent []ModelDescriptor `json:"recent,omitempty"`
}
```

- [ ] **Run** `go test ./appwire/... -run 'TestModelListResponseRecent' -count=1` → PASS.
- [ ] **Run** `golangci-lint run ./appwire/...` → green.
- [ ] **Commit** — `git add appwire/types.go appwire/types_test.go` → `feat(appwire): add ModelListResponse.Recent for the model picker's Recent group`.

## Task 2 — `PastIndex.RecentModels`: global-recency deduped query

**Files:**
- Modify: `cmd/serf-hub/internal/hubcore/past.go` (add method near `AllMetas`, ~line 480)
- Test: `cmd/serf-hub/internal/hubcore/past_test.go`

**Interfaces:**
- Consumes: `PastIndex.all []PastEntry` (already sorted most-recent-first by `sessionMetaLess`, `session_order.go:83`), `schema.SessionMeta.ProfileID`/`.Model`.
- Produces: `func (i *PastIndex) RecentModels(limit int) []appwire.ModelDescriptor`, consumed by Task 3 (`attachRecentModels`) and Task 6 (`handleApiModels`'s default-harness branch).

- [ ] **Failing test** — add to `cmd/serf-hub/internal/hubcore/past_test.go`:
```go
func TestPastIndex_RecentModels_DedupesGlobalRecencyLastN(t *testing.T) {
	idx := NewPastIndex("")
	now := time.Now().UTC()
	idx.SeedForTest([]schema.SessionMeta{
		{ID: "a", ProfileID: "anthropic", Model: "claude-opus-4-6", UpdatedAt: now.Add(-1 * time.Minute)},
		{ID: "b", ProfileID: "openai", Model: "gpt-5.2", UpdatedAt: now.Add(-2 * time.Minute)},
		{ID: "c", ProfileID: "anthropic", Model: "claude-opus-4-6", UpdatedAt: now.Add(-3 * time.Minute)}, // dup of a, older — dropped
		{ID: "d", ProfileID: "openai", Model: "gpt-5-mini", UpdatedAt: now.Add(-4 * time.Minute)},
		{ID: "e", ProfileID: "google", Model: "gemini-3-pro", UpdatedAt: now.Add(-5 * time.Minute)},
		{ID: "f", ProfileID: "zai", Model: "glm-5.2", UpdatedAt: now.Add(-6 * time.Minute)},
		{ID: "g", ProfileID: "mistral", Model: "mistral-large", UpdatedAt: now.Add(-7 * time.Minute)}, // 6th distinct — excluded by limit=5
	})
	got := idx.RecentModels(5)
	want := []appwire.ModelDescriptor{
		{Provider: "anthropic", Model: "claude-opus-4-6"},
		{Provider: "openai", Model: "gpt-5.2"},
		{Provider: "openai", Model: "gpt-5-mini"},
		{Provider: "google", Model: "gemini-3-pro"},
		{Provider: "zai", Model: "glm-5.2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RecentModels(5) = %+v, want %+v", got, want)
	}
}

func TestPastIndex_RecentModels_EmptyIndexReturnsNil(t *testing.T) {
	idx := NewPastIndex("")
	if got := idx.RecentModels(5); got != nil {
		t.Fatalf("RecentModels on empty index = %+v, want nil", got)
	}
}

func TestPastIndex_RecentModels_SkipsBlankProviderOrModel(t *testing.T) {
	idx := NewPastIndex("")
	idx.SeedForTest([]schema.SessionMeta{
		{ID: "a", ProfileID: "", Model: "gpt-5.2", UpdatedAt: time.Now()},
		{ID: "b", ProfileID: "openai", Model: "", UpdatedAt: time.Now()},
		{ID: "c", ProfileID: "openai", Model: "gpt-5.2", UpdatedAt: time.Now()},
	})
	got := idx.RecentModels(5)
	want := []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.2"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RecentModels = %+v, want %+v (blank provider/model entries skipped)", got, want)
	}
}
```
Add `"reflect"` and `"primeradiant.com/serf/appwire"` to the file's import block.
Run: `go test ./cmd/serf-hub/internal/hubcore/... -run 'TestPastIndex_RecentModels' -count=1` → expect FAIL (compile error: `RecentModels` undefined).

- [ ] **Implement** — in `cmd/serf-hub/internal/hubcore/past.go`, add `"primeradiant.com/serf/appwire"` to imports and add near `AllMetas` (~line 480):
```go
// RecentModels returns up to limit distinct (provider, model) pairs for the
// model picker's "Recent" group, ordered by global recency — the same
// most-recently-updated-first order the rest of the index uses
// (session_order.go's sessionMetaLess) — not scoped to any one project or
// harness. ProfileID is the provider instance name (mirrors
// appwire.ModelDescriptor.Provider); entries with a blank ProfileID or Model
// are skipped. Deduped on the pair's first (most recent) occurrence.
func (i *PastIndex) RecentModels(limit int) []appwire.ModelDescriptor {
	if limit <= 0 {
		return nil
	}
	i.mu.RLock()
	defer i.mu.RUnlock()
	seen := make(map[string]bool, limit)
	var out []appwire.ModelDescriptor
	for _, e := range i.all {
		provider := strings.TrimSpace(e.Meta.ProfileID)
		model := strings.TrimSpace(e.Meta.Model)
		if provider == "" || model == "" {
			continue
		}
		key := provider + "/" + model
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, appwire.ModelDescriptor{Provider: provider, Model: model})
		if len(out) >= limit {
			break
		}
	}
	return out
}
```

- [ ] **Run** `go test ./cmd/serf-hub/internal/hubcore/... -run 'TestPastIndex_RecentModels' -count=1` → PASS.
- [ ] **Run** `go test ./cmd/serf-hub/internal/hubcore/... -count=1` (full package, no regression) and `golangci-lint run ./cmd/serf-hub/...`.
- [ ] **Commit** — `git add cmd/serf-hub/internal/hubcore/past.go cmd/serf-hub/internal/hubcore/past_test.go` → `feat(hubcore): PastIndex.RecentModels — global-recency deduped model query`.

## Task 3 — `hubModelList` attaches `Recent` for every harness

**Files:**
- Modify: `cmd/serf-hub/app_models.go` (rename current `hubModelList` body to `hubModelListInner`, add wrapper + `attachRecentModels`)
- Test: `cmd/serf-hub/app_models_test.go` (new file)

**Interfaces:**
- Consumes: `hubcore.PastIndex.RecentModels` (Task 2), `hubcore.WebConfig.Past *PastIndex`.
- Produces: `hubModelList(...)` now always returns `resp.Recent` populated (when `cfg.Past != nil` and it has matches), used by every `ModelList` RPC dispatch (`app_rpc.go:749-750`) — this is the path the TUI's `client.ModelList()` hits, and (for non-serf harnesses) the path `handleApiModels` hits too.

- [ ] **Failing test** — create `cmd/serf-hub/app_models_test.go`:
```go
package main

import (
	"context"
	"reflect"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

// TestHubModelList_AttachesRecentFromPastIndex verifies every ModelList
// response (the path both the TUI's appwire RPC and the web's non-serf-harness
// REST branch use) carries Recent, filtered to models actually present in
// resp.Data — a recent ref no longer offered isn't rendered as unselectable.
func TestHubModelList_AttachesRecentFromPastIndex(t *testing.T) {
	past := hubcore.NewPastIndex("")
	past.SeedForTest([]schema.SessionMeta{
		{ID: "a", ProfileID: "local", Model: "still-live-model"},
		{ID: "b", ProfileID: "local", Model: "retired-model"}, // not in the live source below
	})
	cfg := hubcore.WebConfig{Past: past}
	sources := appsource.NewRegistry()
	// No Spawner/live source configured: hubModelList's serf/local branch
	// returns an empty ModelListResponse (its early-return path), which is
	// enough to exercise attachRecentModels' filtering against resp.Data.
	resp, err := hubModelList(context.Background(), cfg, sources, appwire.ModelListParams{})
	if err != nil {
		t.Fatalf("hubModelList: %v", err)
	}
	if resp.Recent != nil {
		t.Fatalf("Recent = %+v, want nil (no models in resp.Data to match against)", resp.Recent)
	}
}

// TestHubModelList_NilPastIndexOmitsRecent guards the nil-Past config path
// (tests/sandboxes that construct WebConfig without a Past index).
func TestHubModelList_NilPastIndexOmitsRecent(t *testing.T) {
	cfg := hubcore.WebConfig{}
	sources := appsource.NewRegistry()
	resp, err := hubModelList(context.Background(), cfg, sources, appwire.ModelListParams{})
	if err != nil {
		t.Fatalf("hubModelList: %v", err)
	}
	if resp.Recent != nil {
		t.Fatalf("Recent = %+v, want nil with no Past index configured", resp.Recent)
	}
}

// TestAttachRecentModels_FiltersToAvailableModels is the direct unit test for
// the filtering rule: a recent ref present in resp.Data survives; one absent
// (retired/reconfigured) is dropped, in most-recent-first order.
func TestAttachRecentModels_FiltersToAvailableModels(t *testing.T) {
	past := hubcore.NewPastIndex("")
	past.SeedForTest([]schema.SessionMeta{
		{ID: "a", ProfileID: "openai", Model: "gpt-5.2"},
		{ID: "b", ProfileID: "openai", Model: "retired-model"},
	})
	cfg := hubcore.WebConfig{Past: past}
	resp := appwire.ModelListResponse{Data: []appwire.ModelDescriptor{
		{Provider: "openai", Model: "gpt-5.2"},
	}}
	got := attachRecentModels(cfg, resp)
	want := []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.2"}}
	if !reflect.DeepEqual(got.Recent, want) {
		t.Fatalf("Recent = %+v, want %+v (retired-model absent from resp.Data must be dropped)", got.Recent, want)
	}
}
```
Run: `go test ./cmd/serf-hub/... -run 'TestHubModelList_.*Recent|TestAttachRecentModels' -count=1` → expect FAIL (compile error: `attachRecentModels` undefined; `resp.Recent` field access is fine since Task 1 already added it).

- [ ] **Implement** — in `cmd/serf-hub/app_models.go`:
  - Rename the existing `func hubModelList(...)` (lines 14-47) to `func hubModelListInner(...)` (same signature, same body, unchanged).
  - Add the new wrapper and helper immediately above it:
```go
// hubModelList is the single server-side entry point for every ModelList
// RPC — the appwire dispatch (app_rpc.go) routes every harness's call here,
// which is also the path the TUI's client.ModelList() hits. It always
// attaches Recent (the model picker's global-recency group), regardless of
// which harness was requested: Recent is harness-independent by design (the
// picker shows the same top-5 list no matter which harness tab is active).
func hubModelList(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
	resp, err := hubModelListInner(ctx, cfg, sources, params)
	if err != nil {
		return resp, err
	}
	return attachRecentModels(cfg, resp), nil
}

// recentModelsLimit is the model picker's Recent group size (Decision #8:
// "the last 5 distinct models").
const recentModelsLimit = 5

// attachRecentModels resolves cfg.Past's globally-recent model refs and
// filters them to ones actually present in resp.Data — a recent model the
// current config no longer offers (retired, provider reconfigured) is
// dropped rather than rendered as an unselectable entry.
func attachRecentModels(cfg hubcore.WebConfig, resp appwire.ModelListResponse) appwire.ModelListResponse {
	if cfg.Past == nil {
		return resp
	}
	refs := cfg.Past.RecentModels(recentModelsLimit)
	if len(refs) == 0 {
		return resp
	}
	available := make(map[string]bool, len(resp.Data))
	for _, d := range resp.Data {
		available[d.Provider+"/"+d.Model] = true
	}
	var recent []appwire.ModelDescriptor
	for _, ref := range refs {
		if available[ref.Provider+"/"+ref.Model] {
			recent = append(recent, ref)
		}
	}
	resp.Recent = recent
	return resp
}
```

- [ ] **Run** `go test ./cmd/serf-hub/... -run 'TestHubModelList_.*Recent|TestAttachRecentModels' -count=1` → PASS.
- [ ] **Run** `go test ./cmd/serf-hub/... -run 'ModelList' -count=1` (existing model-list tests, no regression) and `golangci-lint run ./cmd/serf-hub/...`.
- [ ] **Commit** — `git add cmd/serf-hub/app_models.go cmd/serf-hub/app_models_test.go` → `feat(hub): attach Recent to every ModelList response via the Past index`.

## Task 4 — Auto-prettified display names + dated-snapshot-last sort (catalog path)

**Files:**
- Modify: `cmd/serf-hub/web_spawn.go` (`modelDescriptorsToAPIModels`, ~line 222)
- Test: `cmd/serf-hub/effort_models_test.go` (extend) and new `cmd/serf-hub/app_models_test.go` cases

**Interfaces:**
- Produces: `prettifyModelDisplayName(id string) string`, `isDatedSnapshotModelID(ref string) bool`, `sortModelEntriesDatedLast(entries []map[string]any)` — all package-private in `cmd/serf-hub`, used by Task 5 (`fetchLiveModels`) too.
- Consumes: nothing new (pure string helpers).

- [ ] **Failing test** — add to `cmd/serf-hub/app_models_test.go`:
```go
func TestPrettifyModelDisplayName(t *testing.T) {
	cases := map[string]string{
		"claude-opus-4-6":            "Claude Opus 4 6",
		"claude-opus-4-6-20251101":   "Claude Opus 4 6", // dated snapshot suffix stripped first
		"gpt-5.1":                    "Gpt 5.1",
		"o3-deep-research":           "O3 Deep Research",
		"glm-5.2":                    "Glm 5.2",
		"bare":                       "Bare",
	}
	for id, want := range cases {
		if got := prettifyModelDisplayName(id); got != want {
			t.Errorf("prettifyModelDisplayName(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestIsDatedSnapshotModelID(t *testing.T) {
	if !isDatedSnapshotModelID("claude-opus-4-6-20251101") {
		t.Error("dated snapshot suffix should be detected")
	}
	if !isDatedSnapshotModelID("anthropic/claude-opus-4-6-20251101") {
		t.Error("dated snapshot suffix should be detected through a provider-qualified ref")
	}
	if isDatedSnapshotModelID("claude-opus-4-6") {
		t.Error("bare family id must not be treated as dated")
	}
	if isDatedSnapshotModelID("gpt-5.1") {
		t.Error("non-dated id must not be treated as dated")
	}
}

func TestModelDescriptorsToAPIModels_UsesPrettifiedDisplayNameAndSortsDatedLast(t *testing.T) {
	models := modelDescriptorsToAPIModels([]appwire.ModelDescriptor{
		{Provider: "anthropic", Model: "claude-opus-4-6-20251101"},
		{Provider: "anthropic", Model: "claude-opus-4-6"},
		{Provider: "openai", Model: "gpt-5.2"},
	}, nil)
	if len(models) != 3 {
		t.Fatalf("got %d models, want 3", len(models))
	}
	if got := models[0]["display_name"]; got != "Claude Opus 4 6" {
		t.Errorf("models[0].display_name = %v, want %q", got, "Claude Opus 4 6")
	}
	// Within the anthropic group, the dated snapshot must sort after the bare
	// family id, regardless of input order.
	var anthropicOrder []string
	for _, m := range models {
		if m["provider"] == "anthropic" {
			anthropicOrder = append(anthropicOrder, m["model"].(string))
		}
	}
	want := []string{"claude-opus-4-6", "claude-opus-4-6-20251101"}
	if !reflect.DeepEqual(anthropicOrder, want) {
		t.Errorf("anthropic model order = %v, want %v (dated snapshot last)", anthropicOrder, want)
	}
}
```
Run: `go test ./cmd/serf-hub/... -run 'TestPrettifyModelDisplayName|TestIsDatedSnapshotModelID|TestModelDescriptorsToAPIModels_UsesPrettified' -count=1` → expect FAIL (compile error: helpers undefined).

- [ ] **Implement** — in `cmd/serf-hub/web_spawn.go`:
  - Add `"regexp"`, `"sort"`, and `"unicode"` to the import block.
  - Add near `modelDescriptorsToAPIModels`:
```go
// datedSnapshotSuffix matches a trailing dated-snapshot suffix on a bare
// model id (e.g. "-20251101"). Duplicated (not exported from llm) because
// llm/model_catalog_embedded.go isn't owned by this track — see the plan's
// Global Constraints.
var datedSnapshotSuffix = regexp.MustCompile(`-\d{8}$`)

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
```
  - In `modelDescriptorsToAPIModels`, change the entry construction from:
```go
		entry := map[string]any{
			"provider": m.Provider,
			"model":    m.Model,
		}
		if cat != nil {
			if mi := catalogModelInfo(cat, behaviorTagFor(providerCfg, m.Provider), m.Model); mi != nil {
				entry["display_name"] = mi.DisplayName
```
    to:
```go
		entry := map[string]any{
			"provider":     m.Provider,
			"model":        m.Model,
			"display_name": prettifyModelDisplayName(m.Model),
		}
		if cat != nil {
			if mi := catalogModelInfo(cat, behaviorTagFor(providerCfg, m.Provider), m.Model); mi != nil {
```
    (delete the old `entry["display_name"] = mi.DisplayName` line entirely — display_name is now always the prettified id, catalogued or not).
  - Immediately before `return out`, add `sortModelEntriesDatedLast(out)`.

- [ ] **Run** `go test ./cmd/serf-hub/... -run 'TestPrettifyModelDisplayName|TestIsDatedSnapshotModelID|TestModelDescriptorsToAPIModels' -count=1` → PASS (including the pre-existing `TestModelDescriptorsToAPIModels_*` tests in `effort_models_test.go`, which don't assert display_name and must stay green).
- [ ] **Run** `go test ./cmd/serf-hub/... -count=1` (package-wide, no regression) and `golangci-lint run ./cmd/serf-hub/...`.
- [ ] **Commit** — `git add cmd/serf-hub/web_spawn.go cmd/serf-hub/app_models_test.go` → `feat(hub): auto-prettify model display names and sort dated snapshots last`.

## Task 5 — Capability badges + prettify/sort on the live-list path

**Files:**
- Modify: `cmd/serf-hub/web_spawn.go` (`modelDescriptorsToAPIModels` gains 3 new badge fields; `fetchLiveModels`, ~line 297, gets the same three plus prettify+sort)

**Interfaces:**
- Consumes: `prettifyModelDisplayName`, `sortModelEntriesDatedLast` (Task 4); `llm.ModelInfo.SupportsVision/.SupportsWebSearch/.MaxOutputTokens` (existing fields, `llm/model_catalog.go:24/34/22`).
- Produces: three new JSON keys on every `/api/models` entry — `supports_vision` (bool), `supports_web_search` (bool, present only when the catalog is non-nil on it), `max_output_tokens` (int, present only when non-nil) — plus an uncatalogued-model test proving graceful degradation.

- [ ] **Failing test** — add to `cmd/serf-hub/app_models_test.go`:
```go
func TestModelDescriptorsToAPIModels_IncludesCapabilityBadges(t *testing.T) {
	models := modelDescriptorsToAPIModels([]appwire.ModelDescriptor{
		{Provider: "anthropic", Model: "claude-opus-4-6"},
	}, nil)
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	m := models[0]
	if got, _ := m["supports_vision"].(bool); !got {
		t.Errorf("supports_vision = %v, want true", m["supports_vision"])
	}
	if got, _ := m["supports_web_search"].(bool); !got {
		t.Errorf("supports_web_search = %v, want true", m["supports_web_search"])
	}
	if got, _ := m["max_output_tokens"].(int); got != 128000 {
		t.Errorf("max_output_tokens = %v, want 128000", m["max_output_tokens"])
	}
	if got, _ := m["context_window"].(int); got != 1_000_000 {
		t.Errorf("context_window = %v, want 1000000", m["context_window"])
	}
}

// TestModelDescriptorsToAPIModels_UncataloguedModelStillRendersWithoutBadges
// pins the graceful-degradation rule: a live model absent from the embedded
// catalog (catalogModelInfo returns nil) must still render name+provider+id
// — not be dropped — just without any badge fields.
func TestModelDescriptorsToAPIModels_UncataloguedModelStillRendersWithoutBadges(t *testing.T) {
	models := modelDescriptorsToAPIModels([]appwire.ModelDescriptor{
		{Provider: "mycompany", Model: "totally-unknown-model-xyz"},
	}, nil)
	if len(models) != 1 {
		t.Fatalf("uncatalogued model was dropped: got %d entries, want 1", len(models))
	}
	m := models[0]
	if m["provider"] != "mycompany" || m["model"] != "totally-unknown-model-xyz" {
		t.Fatalf("uncatalogued entry missing provider/model: %+v", m)
	}
	if m["display_name"] != "Totally Unknown Model Xyz" {
		t.Errorf("display_name = %v, want prettified id even when uncatalogued", m["display_name"])
	}
	for _, badge := range []string{"supports_tools", "supports_vision", "supports_reasoning", "supports_web_search", "context_window", "max_output_tokens", "input_cost_per_million", "output_cost_per_million"} {
		if _, ok := m[badge]; ok {
			t.Errorf("uncatalogued entry should omit %q, got %v", badge, m[badge])
		}
	}
}
```
Run: `go test ./cmd/serf-hub/... -run 'TestModelDescriptorsToAPIModels_IncludesCapabilityBadges|TestModelDescriptorsToAPIModels_Uncatalogued' -count=1` → expect FAIL (missing badge keys).

- [ ] **Implement** — in `cmd/serf-hub/web_spawn.go`, in `modelDescriptorsToAPIModels`'s `if mi := catalogModelInfo(...); mi != nil {` block, add after the existing `entry["supports_tools"] = mi.SupportsTools`:
```go
				entry["supports_vision"] = mi.SupportsVision
				if mi.MaxOutputTokens != nil {
					entry["max_output_tokens"] = *mi.MaxOutputTokens
				}
				if mi.SupportsWebSearch != nil {
					entry["supports_web_search"] = *mi.SupportsWebSearch
				}
```
  - In `fetchLiveModels`, in the initial `entry := map[string]any{...}` literal, change `"display_name": m.DisplayName,` to `"display_name": prettifyModelDisplayName(m.ID),`.
  - In `fetchLiveModels`'s `if mi != nil {` block (the catalog-enrichment fallback), add after the existing `reasoning_effort_levels` fallback:
```go
			if _, ok := entry["supports_vision"]; !ok {
				entry["supports_vision"] = mi.SupportsVision
			}
			if mi.MaxOutputTokens != nil {
				if _, ok := entry["max_output_tokens"]; !ok {
					entry["max_output_tokens"] = *mi.MaxOutputTokens
				}
			}
			if mi.SupportsWebSearch != nil {
				if _, ok := entry["supports_web_search"]; !ok {
					entry["supports_web_search"] = *mi.SupportsWebSearch
				}
			}
```
  - Immediately before `s.liveModels.mu.Lock()` near the end of `fetchLiveModels` (where `out` is about to be cached), add `sortModelEntriesDatedLast(out)`.

- [ ] **Run** `go test ./cmd/serf-hub/... -run 'TestModelDescriptorsToAPIModels|TestPrettifyModelDisplayName' -count=1` → PASS.
- [ ] **Run** `go test ./cmd/serf-hub/... -count=1` (full package) and `golangci-lint run ./cmd/serf-hub/...`.
- [ ] **Commit** — `git add cmd/serf-hub/web_spawn.go cmd/serf-hub/app_models_test.go` → `feat(hub): surface vision/web-search/max-output badges; uncatalogued models degrade gracefully`.

## Task 6 — `recent` in the `/api/models` JSON envelope

**Files:**
- Modify: `cmd/serf-hub/web_spawn.go` (`writeModelsResponse`, ~line 188; `handleApiModels`, ~line 150)
- Test: `cmd/serf-hub/web_test.go` (extend)

**Interfaces:**
- Consumes: `hubModelList`'s `resp.Recent` (Task 3, non-serf-harness branch), `hubcore.PastIndex.RecentModels` directly (Task 2, default serf/local branch).
- Produces: `recentModelEntriesFromDescriptors(models []map[string]any, refs []appwire.ModelDescriptor) []map[string]any`; the `/api/models?diagnostics=1` response gains a `"recent"` array (always present, never null, when the envelope form is requested) — the bare-array default response (used by `search.js`, unaffected) is untouched.

- [ ] **Failing test** — add to `cmd/serf-hub/web_test.go`:
```go
// TestHandleApiModels_DiagnosticsEnvelopeIncludesRecent (kata model-picker
// Recent) verifies /api/models?diagnostics=1 carries a "recent" array
// resolved from the Past index, restricted to models the response actually
// offers, in most-recent-first order; the bare-array default response is
// unaffected. LiveModels is stubbed to keep the test hermetic (fetchLiveModels
// would otherwise call cmdutil.LoadClient() and touch the real host config).
func TestHandleApiModels_DiagnosticsEnvelopeIncludesRecent(t *testing.T) {
	s := NewWebServer(hubcore.WebConfig{
		HubAddr:    "127.0.0.1:9180",
		LiveModels: func(context.Context) []map[string]any { return nil },
	})
	s.injectMetasForTest([]schema.SessionMeta{
		{ID: "a", ProfileID: "local", Model: "test-model-one", UpdatedAt: time.Now()},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/models?diagnostics=1", nil)
	rec := httptest.NewRecorder()
	s.handleApiModels(rec, req)

	var body struct {
		Models      []map[string]any `json:"models"`
		Diagnostics []map[string]any `json:"diagnostics"`
		Recent      []map[string]any `json:"recent"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	if body.Recent == nil {
		t.Fatal("recent should be an empty array, not null, when the envelope is requested")
	}
}
```
(`injectMetasForTest`, `web_test.go:76-80`, replaces `s.cfg.Past` with a freshly seeded index — the established test-only seam other `web_test.go` tests already use.)
Run: `go test ./cmd/serf-hub/... -run 'TestHandleApiModels_DiagnosticsEnvelopeIncludesRecent' -count=1` → expect FAIL (`recent` key absent/null).

- [ ] **Implement** — in `cmd/serf-hub/web_spawn.go`:
  - Add near `sortModelEntriesDatedLast`:
```go
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
```
  - Change `writeModelsResponse`'s signature and body:
```go
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
```
  - In `handleApiModels`, in the non-serf-harness branch, change:
```go
		writeModelsResponse(w, modelDescriptorsToAPIModels(resp.Data, s.cfg.ProviderConfig), resp.Diagnostics, includeDiagnostics)
```
    to:
```go
		models := modelDescriptorsToAPIModels(resp.Data, s.cfg.ProviderConfig)
		writeModelsResponse(w, models, resp.Diagnostics, recentModelEntriesFromDescriptors(models, resp.Recent), includeDiagnostics)
```
  - In `handleApiModels`'s default (serf/local) branch, immediately before the final `writeModelsResponse(...)` call, add:
```go
	var recentRefs []appwire.ModelDescriptor
	if s.cfg.Past != nil {
		recentRefs = s.cfg.Past.RecentModels(recentModelsLimit)
	}
```
    and change the call to:
```go
	writeModelsResponse(w, models, launchResp.Diagnostics, recentModelEntriesFromDescriptors(models, recentRefs), includeDiagnostics)
```

- [ ] **Run** `go test ./cmd/serf-hub/... -run 'TestHandleApiModels|TestModelDescriptorsToAPIModels' -count=1` → PASS.
- [ ] **Run** `go test ./cmd/serf-hub/... -count=1` (full package — `writeModelsResponse`'s signature change touches every call site; confirm none were missed) and `golangci-lint run ./cmd/serf-hub/...`.
- [ ] **Commit** — `git add cmd/serf-hub/web_spawn.go cmd/serf-hub/web_test.go` → `feat(hub): surface the Recent group on /api/models?diagnostics=1`.

## Task 7 — spawn.js: prettified name, secondary id, capability badges

**Files:**
- Modify: `cmd/serf-hub/assets/spawn.js` (`openModelPicker`'s `renderList`, ~line 1449-1488)
- Modify: `cmd/serf-hub/assets/style.css` (new badge/id CSS block)
- Test: `cmd/serf-hub/jstest/test-spawn-model-picker-badges.js` (new)

**Interfaces:**
- Consumes: `/api/models?diagnostics=1` entries' `display_name`, `model`, `supports_tools`, `supports_vision`, `supports_reasoning`, `reasoning_effort_levels`, `supports_web_search`, `context_window`, `max_output_tokens`, `input_cost_per_million`, `output_cost_per_million` (Tasks 4-6).
- Produces: `buildModelRow(m)` and `modelBadges(m)` helpers (module-private, not exported on `window.SerfSpawn` — internal refactor of the existing inline row-building code).

- [ ] **Failing test** — create `cmd/serf-hub/jstest/test-spawn-model-picker-badges.js`:
```js
// Model picker: prettified display name + raw id secondary line + capability
// badges (kata model-picker-badges). Loads spawn.js in JSDOM, stubs
// SerfAppwire.listModelsWithDiagnostics to return one catalogued and one
// uncatalogued model, opens the model chip picker, asserts on the rendered
// DOM.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const spawnSrc = fs.readFileSync(path.resolve(__dirname, "../assets/spawn.js"), "utf8");

const failures = [];
const pass = (c, m) => { if (!c) failures.push("FAIL: " + m); };
const flush = () => new Promise((r) => setTimeout(r, 0));

(async () => {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <form data-spawn-form>
      <div id="spawn-chips">
        <button class="btn btn-chip" type="button" data-chip="model">
          <span class="chip-value" data-chip-value-model>(pick a model)</span>
        </button>
      </div>
      <textarea name="prompt"></textarea>
      <input type="hidden" name="harness" value="serf">
      <input type="hidden" name="model" value="">
      <input type="hidden" name="working_dir" value="">
      <input type="hidden" name="branch" value="">
      <input type="hidden" name="access_mode" value="full">
      <input type="hidden" name="agent" value="default">
      <input type="hidden" name="reasoning_effort" value="">
    </form>
  </body></html>`, {
    runScripts: "outside-only",
    pretendToBeVisual: true,
    url: "http://127.0.0.1:9180/new",
  });
  const { window } = dom;

  window.SerfAppwire = {
    listModelsWithDiagnostics() {
      return Promise.resolve({
        models: [
          {
            provider: "anthropic", model: "claude-opus-4-6", display_name: "Claude Opus 4 6",
            supports_tools: true, supports_vision: true, supports_reasoning: true,
            reasoning_effort_levels: ["low", "medium", "high", "max"], supports_web_search: true,
            context_window: 1000000, max_output_tokens: 128000,
            input_cost_per_million: 5, output_cost_per_million: 25,
          },
          { provider: "mycompany", model: "unknown-model", display_name: "Unknown Model" },
        ],
        diagnostics: [],
      });
    },
  };

  window.eval(spawnSrc);
  window.document.dispatchEvent(new window.Event("DOMContentLoaded", { bubbles: true }));
  await flush();

  window.document.querySelector('button[data-chip="model"]').click();
  await flush();

  const rows = window.document.querySelectorAll(".chip-picker-model");
  pass(rows.length === 2, "both models rendered, got " + rows.length);

  const catalogued = Array.from(rows).find(r => r.textContent.includes("Claude Opus 4 6"));
  pass(catalogued, "catalogued model row rendered with prettified name");
  pass(catalogued && catalogued.querySelector(".chip-picker-model-id").textContent === "claude-opus-4-6",
    "secondary line shows the raw id");
  const badges = catalogued ? Array.from(catalogued.querySelectorAll(".chip-picker-badge")).map(b => b.textContent) : [];
  pass(badges.includes("tools"), "tools badge present: " + badges.join(","));
  pass(badges.includes("vision"), "vision badge present: " + badges.join(","));
  pass(badges.some(b => b.startsWith("reasoning")), "reasoning badge present: " + badges.join(","));
  pass(badges.includes("web search"), "web search badge present: " + badges.join(","));
  pass(catalogued && catalogued.textContent.includes("1M ctx"), "context window meta present");
  pass(catalogued && catalogued.textContent.includes("$5.00/M in"), "input cost meta present");

  const uncatalogued = Array.from(rows).find(r => r.textContent.includes("Unknown Model"));
  pass(uncatalogued, "uncatalogued model still renders");
  pass(uncatalogued && uncatalogued.querySelectorAll(".chip-picker-badge").length === 0,
    "uncatalogued model has no badges");

  if (failures.length === 0) {
    console.log("PASS: spawn.js model picker badges");
    process.exit(0);
  }
  for (const f of failures) console.log(" " + f);
  process.exit(1);
})().catch((e) => { console.error(e && e.stack ? e.stack : e); process.exit(1); });
```
Run: `cd cmd/serf-hub/jstest && node test-spawn-model-picker-badges.js` → expect FAIL (no `.chip-picker-model-id`/`.chip-picker-badge` elements exist yet; `name.textContent` is still the raw `m.model`).

- [ ] **Implement** — in `cmd/serf-hub/assets/spawn.js`, replace the inline row-building code inside `renderList`'s `matches.forEach(m => { ... })` (the block building `el`/`name`/`meta`) with calls to two new module-level functions, and update `renderList` to use them:
```js
      function modelBadges(m) {
        const badges = [];
        if (m.supports_tools) badges.push("tools");
        if (m.supports_vision) badges.push("vision");
        if (m.supports_reasoning) {
          const levels = m.reasoning_effort_levels;
          badges.push(Array.isArray(levels) && levels.length ? "reasoning (" + levels.join("/") + ")" : "reasoning");
        }
        if (m.supports_web_search) badges.push("web search");
        return badges;
      }

      function buildModelRow(m) {
        const el = document.createElement("button");
        el.type = "button";
        el.className = "chip-picker-model";
        const name = document.createElement("div");
        name.className = "chip-picker-model-name";
        name.textContent = m.display_name || m.model;
        el.appendChild(name);
        if (m.display_name && m.display_name !== m.model) {
          const idLine = document.createElement("div");
          idLine.className = "chip-picker-model-id";
          idLine.textContent = m.model;
          el.appendChild(idLine);
        }
        const badges = modelBadges(m);
        if (badges.length) {
          const badgeRow = document.createElement("div");
          badgeRow.className = "chip-picker-model-badges";
          badges.forEach(b => {
            const span = document.createElement("span");
            span.className = "chip-picker-badge";
            span.textContent = b;
            badgeRow.appendChild(span);
          });
          el.appendChild(badgeRow);
        }
        const meta = document.createElement("div");
        meta.className = "chip-picker-model-meta";
        const parts = [];
        if (m.context_window) parts.push(formatCtx(m.context_window) + " ctx");
        if (m.max_output_tokens) parts.push(formatCtx(m.max_output_tokens) + " out");
        if (m.input_cost_per_million != null) parts.push("$" + m.input_cost_per_million.toFixed(2) + "/M in");
        if (m.output_cost_per_million != null) parts.push("$" + m.output_cost_per_million.toFixed(2) + "/M out");
        if (parts.length) {
          meta.textContent = parts.join(" · ");
          el.appendChild(meta);
        }
        el.addEventListener("click", () => selectModel(m));
        return el;
      }
```
  Place these two functions right after the existing `function formatCtx(n) {...}` definition (still inside `modelsPromise.then(result => {...})`, so they close over `selectModel`).
  Then, inside `renderList`'s `matches.forEach(m => { ... })`, replace the whole per-model body with:
```js
          matches.forEach(m => {
            shown++;
            list.appendChild(buildModelRow(m));
          });
```
  (Delete the now-unused inline `el`/`name`/`meta`/`parts` construction that used to live there.)
- [ ] **Implement CSS** — in `cmd/serf-hub/assets/style.css`, immediately after the existing `.chip-picker-model-meta { ... }` rule (~line 3159), add:
```css
.chip-picker-model-id { color: var(--text-muted); font-family: var(--font-mono); font-size: var(--text-xs); }
.chip-picker-model-badges { display: flex; flex-wrap: wrap; gap: var(--space-1); margin-top: var(--space-1); }
.chip-picker-badge { color: var(--text-muted); background: var(--bg); border-radius: var(--radius-pill); padding: 0 var(--space-2); font-size: var(--text-2xs); font-family: var(--font-mono); }
```

- [ ] **Run** `cd cmd/serf-hub/jstest && node test-spawn-model-picker-badges.js` → PASS.
- [ ] **Run** `cd cmd/serf-hub/jstest && node test-abbreviate-model.js && node test-spawn.js` (existing spawn.js tests, no regression).
- [ ] **Commit** — `git add cmd/serf-hub/assets/spawn.js cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-spawn-model-picker-badges.js` → `feat(spawn-ui): prettified model names, raw-id secondary line, capability badges`.

## Task 8 — spawn.js: `Recent` group above the provider list

**Files:**
- Modify: `cmd/serf-hub/assets/spawn.js` (`openModelPicker`, ~line 1385-1488)
- Test: `cmd/serf-hub/jstest/test-spawn-model-picker-recent.js` (new)

**Interfaces:**
- Consumes: `listModelsWithDiagnosticsForHarness`'s resolved `{models, diagnostics, recent}` (the server already returns `recent` per Task 6; this task threads it through the JS fetch wrapper, which today discards anything beyond `models`/`diagnostics`).
- Produces: a `Recent` group rendered first, reusing `buildModelRow` (Task 7); filtered by the same search predicate as provider groups.

- [ ] **Failing test** — create `cmd/serf-hub/jstest/test-spawn-model-picker-recent.js`:
```js
// Model picker: Recent group renders above the provider-grouped catalog and
// is filtered by the same search box (kata model-picker-recent).
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const spawnSrc = fs.readFileSync(path.resolve(__dirname, "../assets/spawn.js"), "utf8");

const failures = [];
const pass = (c, m) => { if (!c) failures.push("FAIL: " + m); };
const flush = () => new Promise((r) => setTimeout(r, 0));

(async () => {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <form data-spawn-form>
      <div id="spawn-chips">
        <button class="btn btn-chip" type="button" data-chip="model">
          <span class="chip-value" data-chip-value-model>(pick a model)</span>
        </button>
      </div>
      <textarea name="prompt"></textarea>
      <input type="hidden" name="harness" value="serf">
      <input type="hidden" name="model" value="">
      <input type="hidden" name="working_dir" value="">
      <input type="hidden" name="branch" value="">
      <input type="hidden" name="access_mode" value="full">
      <input type="hidden" name="agent" value="default">
      <input type="hidden" name="reasoning_effort" value="">
    </form>
  </body></html>`, {
    runScripts: "outside-only",
    pretendToBeVisual: true,
    url: "http://127.0.0.1:9180/new",
  });
  const { window } = dom;

  const recentModel = { provider: "anthropic", model: "claude-opus-4-6", display_name: "Claude Opus 4 6" };
  const otherModel = { provider: "openai", model: "gpt-5.2", display_name: "Gpt 5.2" };
  window.SerfAppwire = {
    listModelsWithDiagnostics() {
      return Promise.resolve({ models: [recentModel, otherModel], diagnostics: [], recent: [recentModel] });
    },
  };

  window.eval(spawnSrc);
  window.document.dispatchEvent(new window.Event("DOMContentLoaded", { bubbles: true }));
  await flush();

  window.document.querySelector('button[data-chip="model"]').click();
  await flush();

  const groups = Array.from(window.document.querySelectorAll(".chip-picker-group")).map(g => g.textContent);
  pass(groups[0] === "Recent", "Recent group renders first, got groups=" + JSON.stringify(groups));
  pass(groups.includes("anthropic") && groups.includes("openai"), "provider groups still render: " + JSON.stringify(groups));

  const recentGroup = window.document.querySelectorAll(".chip-picker-group")[0];
  const rowAfterRecentHeader = recentGroup.nextElementSibling;
  pass(rowAfterRecentHeader && rowAfterRecentHeader.textContent.includes("Claude Opus 4 6"),
    "the row directly after the Recent header is the recent model");

  // Filtering narrows Recent too.
  const search = window.document.querySelector(".chip-picker-search");
  search.value = "gpt";
  search.dispatchEvent(new window.Event("input", { bubbles: true }));
  const groupsAfterFilter = Array.from(window.document.querySelectorAll(".chip-picker-group")).map(g => g.textContent);
  pass(!groupsAfterFilter.includes("Recent"), "Recent group hides when its only entry doesn't match the filter: " + JSON.stringify(groupsAfterFilter));

  if (failures.length === 0) {
    console.log("PASS: spawn.js model picker Recent group");
    process.exit(0);
  }
  for (const f of failures) console.log(" " + f);
  process.exit(1);
})().catch((e) => { console.error(e && e.stack ? e.stack : e); process.exit(1); });
```
Run: `cd cmd/serf-hub/jstest && node test-spawn-model-picker-recent.js` → expect FAIL (no Recent group rendered; `result.recent` is discarded today).

- [ ] **Implement** — in `cmd/serf-hub/assets/spawn.js`'s `openModelPicker`:
  - Change the top of the `modelsPromise.then(result => {...})` callback from:
```js
      const models = Array.isArray(result && result.models) ? result.models : [];
      const diagnostics = Array.isArray(result && result.diagnostics) ? result.diagnostics : [];
```
    to:
```js
      const models = Array.isArray(result && result.models) ? result.models : [];
      const diagnostics = Array.isArray(result && result.diagnostics) ? result.diagnostics : [];
      const recentModels = Array.isArray(result && result.recent) ? result.recent : [];
```
  - In `renderList(filter)`, before the `providers.forEach(p => {...})` loop, add:
```js
        const recentMatches = recentModels.filter(m =>
          !filter || (m.model + " " + (m.display_name || "")).toLowerCase().includes(filter)
        );
        if (recentMatches.length > 0) {
          const header = document.createElement("div");
          header.className = "chip-picker-group";
          header.textContent = "Recent";
          list.appendChild(header);
          recentMatches.forEach(m => {
            shown++;
            list.appendChild(buildModelRow(m));
          });
        }
```
  (`shown` is already declared as `let shown = 0;` at the top of `renderList`.)

- [ ] **Run** `cd cmd/serf-hub/jstest && node test-spawn-model-picker-recent.js` → PASS.
- [ ] **Run** `cd cmd/serf-hub/jstest && node test-spawn-model-picker-badges.js && node test-spawn.js` (no regression from Task 7).
- [ ] **Commit** — `git add cmd/serf-hub/assets/spawn.js cmd/serf-hub/jstest/test-spawn-model-picker-recent.js` → `feat(spawn-ui): render the Recent group above the provider-grouped catalog`.

## Task 9 — settings-pickers.js: envelope fetch, Recent tab, badges

**Files:**
- Modify: `cmd/serf-hub/assets/settings-pickers.js` (`fetchModels`, `buildModelPicker`, ~lines 12-148)
- Test: `cmd/serf-hub/jstest/test-settings-model-picker.js` (new)

**Interfaces:**
- Consumes: `/api/models?diagnostics=1` envelope (Task 6); reuses the design (not the code — separate IIFE, established duplication convention with `formatCtx`) of `modelBadges`/badge rendering from Task 7.
- Produces: `buildModelPicker` renders a pinned-first `Recent` provider-column entry (when non-empty) plus prettified names/badges in `renderModels`.

- [ ] **Failing test** — create `cmd/serf-hub/jstest/test-settings-model-picker.js`:
```js
// Settings model picker: envelope fetch (diagnostics=1), Recent pinned-first
// provider tab, prettified names + badges (kata settings-model-picker).
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const settingsPickersSrc = fs.readFileSync(path.resolve(__dirname, "../assets/settings-pickers.js"), "utf8");

const failures = [];
const pass = (c, m) => { if (!c) failures.push("FAIL: " + m); };
const flush = () => new Promise((r) => setTimeout(r, 0));

(async () => {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <div class="sp-model-wrap">
      <button data-settings-model-picker type="button">choose</button>
      <input type="hidden" name="cheap_model" value="">
      <span class="sp-model-display"></span>
    </div>
  </body></html>`, {
    runScripts: "outside-only",
    pretendToBeVisual: true,
    url: "http://127.0.0.1:9180/settings",
  });
  const { window } = dom;

  const recentModel = { provider: "anthropic", model: "claude-opus-4-6", display_name: "Claude Opus 4 6", supports_tools: true };
  const otherModel = { provider: "openai", model: "gpt-5.2", display_name: "Gpt 5.2" };
  let requestedURL = null;
  window.fetch = (url) => {
    requestedURL = url;
    return Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ models: [recentModel, otherModel], diagnostics: [], recent: [recentModel] }),
    });
  };

  window.eval(settingsPickersSrc);
  window.document.dispatchEvent(new window.Event("DOMContentLoaded", { bubbles: true }));
  await flush();

  pass(requestedURL && requestedURL.includes("diagnostics=1"), "settings picker fetches the diagnostics envelope, got " + requestedURL);

  window.document.querySelector("button[data-settings-model-picker]").click();
  await flush();

  const providerTabs = Array.from(window.document.querySelectorAll(".chip-picker-provider")).map(p => p.textContent);
  pass(providerTabs[0] === "Recent", "Recent is the first provider tab, got " + JSON.stringify(providerTabs));

  const modelRows = window.document.querySelectorAll(".chip-picker-model");
  pass(modelRows.length === 1 && modelRows[0].textContent.includes("Claude Opus 4 6"),
    "Recent tab is active by default and shows the recent model");
  pass(modelRows[0].querySelector(".chip-picker-badge") && modelRows[0].textContent.includes("tools"),
    "badges render in the settings picker too");

  if (failures.length === 0) {
    console.log("PASS: settings-pickers.js model picker");
    process.exit(0);
  }
  for (const f of failures) console.log(" " + f);
  process.exit(1);
})().catch((e) => { console.error(e && e.stack ? e.stack : e); process.exit(1); });
```
Run: `cd cmd/serf-hub/jstest && node test-settings-model-picker.js` → expect FAIL (`fetchModels` still hits the bare `/api/models`; no Recent tab).

- [ ] **Implement** — in `cmd/serf-hub/assets/settings-pickers.js`:
  - Change `fetchModels`:
```js
  function fetchModels() {
    if (_modelsCache) return _modelsCache;
    _modelsCache = fetch("/api/models?diagnostics=1", { credentials: "same-origin" })
      .then(r => r.ok ? r.json() : { models: [], diagnostics: [], recent: [] })
      .catch(() => ({ models: [], diagnostics: [], recent: [] }));
    return _modelsCache;
  }
```
  - In `buildModelPicker`, change the `fetchModels().then(models => {...})` opening to destructure the envelope and build the pinned-first providers list:
```js
    fetchModels().then(result => {
      const models = Array.isArray(result && result.models) ? result.models : [];
      const recent = Array.isArray(result && result.recent) ? result.recent : [];

      // Group by provider; "Recent" is a pinned-first pseudo-provider.
      const byProvider = {};
      if (recent.length > 0) byProvider["Recent"] = recent;
      models.forEach(m => {
        if (!byProvider[m.provider]) byProvider[m.provider] = [];
        byProvider[m.provider].push(m);
      });
      const providers = Object.keys(byProvider).filter(p => p !== "Recent").sort();
      if (byProvider["Recent"]) providers.unshift("Recent");
```
  - Add the same `modelBadges(m)` helper as Task 7 (duplicated per the file's existing `formatCtx` duplication convention), placed right after `formatCtx`:
```js
      function modelBadges(m) {
        const badges = [];
        if (m.supports_tools) badges.push("tools");
        if (m.supports_vision) badges.push("vision");
        if (m.supports_reasoning) {
          const levels = m.reasoning_effort_levels;
          badges.push(Array.isArray(levels) && levels.length ? "reasoning (" + levels.join("/") + ")" : "reasoning");
        }
        if (m.supports_web_search) badges.push("web search");
        return badges;
      }
```
  - In `renderModels(filter)`, change the row-building body from:
```js
          const el = document.createElement("div");
          el.className = "chip-picker-model";
          const name = document.createElement("div");
          name.className = "chip-picker-model-name";
          name.textContent = m.model;
          const meta = document.createElement("div");
          meta.className = "chip-picker-model-meta";
          const parts = [];
          if (m.context_window) parts.push(formatCtx(m.context_window) + " ctx");
          if (m.input_cost_per_million != null) parts.push("$" + m.input_cost_per_million.toFixed(2) + "/M in");
          meta.textContent = parts.join(" · ");
          el.appendChild(name);
          el.appendChild(meta);
```
    to:
```js
          const el = document.createElement("div");
          el.className = "chip-picker-model";
          const name = document.createElement("div");
          name.className = "chip-picker-model-name";
          name.textContent = m.display_name || m.model;
          el.appendChild(name);
          const badges = modelBadges(m);
          if (badges.length) {
            const badgeRow = document.createElement("div");
            badgeRow.className = "chip-picker-model-badges";
            badges.forEach(b => {
              const span = document.createElement("span");
              span.className = "chip-picker-badge";
              span.textContent = b;
              badgeRow.appendChild(span);
            });
            el.appendChild(badgeRow);
          }
          const meta = document.createElement("div");
          meta.className = "chip-picker-model-meta";
          const parts = [];
          if (m.context_window) parts.push(formatCtx(m.context_window) + " ctx");
          if (m.input_cost_per_million != null) parts.push("$" + m.input_cost_per_million.toFixed(2) + "/M in");
          meta.textContent = parts.join(" · ");
          el.appendChild(meta);
```

- [ ] **Run** `cd cmd/serf-hub/jstest && node test-settings-model-picker.js` → PASS.
- [ ] **Run** `cd cmd/serf-hub/jstest && node test-settings-dir-picker.js && node test-settings.js` (existing settings-pickers.js/settings.js tests, no regression).
- [ ] **Commit** — `git add cmd/serf-hub/assets/settings-pickers.js cmd/serf-hub/jstest/test-settings-model-picker.js` → `feat(settings-ui): Recent tab, prettified names, capability badges in the model picker`.

## Task 10 — `tuipick.ModelPickerItem` gains `Group`/`Meta`; grouped rendering

**Files:**
- Modify: `cmd/serf-tui/internal/tuipick/model_picker.go` (`ModelPickerItem`, ~line 12-16; `renderBody`, ~line 124-191)
- Test: `cmd/serf-tui/internal/tuipick/model_picker_test.go` (extend)

**Interfaces:**
- Produces: `ModelPickerItem.Group string` (renders as a header line on a group transition, matching the web's sticky group header semantics) and `ModelPickerItem.Meta string` (a trailing dim tail on the row). Zero-value for both leaves `NewTranscriptPicker`/`NewActionPicker` rendering unchanged.

- [ ] **Failing test** — add to `cmd/serf-tui/internal/tuipick/model_picker_test.go`:
```go
func TestModelPicker_RendersGroupHeadersOnTransition(t *testing.T) {
	withTestColorProfile(t)
	items := []ModelPickerItem{
		{ID: "anthropic/claude-opus-4-6", Display: "Claude Opus 4 6", Group: "Recent"},
		{ID: "anthropic/claude-opus-4-6", Display: "Claude Opus 4 6", Group: "anthropic"},
		{ID: "openai/gpt-5.2", Display: "Gpt 5.2", Group: "openai"},
	}
	p := NewModelPicker(items, "", 80)
	plain := ansiPattern.ReplaceAllString(p.View(), "")
	lines := strings.Split(plain, "\n")

	recentIdx, anthropicIdx, openaiIdx := -1, -1, -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "RECENT" {
			recentIdx = i
		}
		if trimmed == "ANTHROPIC" {
			anthropicIdx = i
		}
		if trimmed == "OPENAI" {
			openaiIdx = i
		}
	}
	if recentIdx == -1 || anthropicIdx == -1 || openaiIdx == -1 {
		t.Fatalf("expected RECENT, ANTHROPIC, and OPENAI group headers, view:\n%s", plain)
	}
	if !(recentIdx < anthropicIdx && anthropicIdx < openaiIdx) {
		t.Fatalf("group headers out of order: recent=%d anthropic=%d openai=%d", recentIdx, anthropicIdx, openaiIdx)
	}
}

func TestModelPicker_RendersMetaTail(t *testing.T) {
	withTestColorProfile(t)
	items := []ModelPickerItem{
		{ID: "anthropic/claude-opus-4-6", Display: "Claude Opus 4 6", Meta: "1M ctx · $5.00/$25.00 · tools,vision"},
	}
	p := NewModelPicker(items, "", 80)
	plain := ansiPattern.ReplaceAllString(p.View(), "")
	if !strings.Contains(plain, "1M ctx · $5.00/$25.00 · tools,vision") {
		t.Fatalf("expected meta tail in view:\n%s", plain)
	}
}

func TestModelPicker_ZeroValueGroupMetaUnchangedForActionPicker(t *testing.T) {
	// NewActionPicker/NewTranscriptPicker items never set Group/Meta; their
	// rendering must be byte-for-byte unaffected by this change.
	items := []ModelPickerItem{{ID: "restart", Display: "Restart session"}}
	p := NewActionPicker("Actions", "enter select", items, 80)
	plain := ansiPattern.ReplaceAllString(p.View(), "")
	if strings.Contains(plain, "\n\n\n") {
		t.Fatalf("zero-value Group must not introduce a spurious blank header line:\n%s", plain)
	}
	if !strings.Contains(plain, "Restart session") {
		t.Fatalf("action item should still render:\n%s", plain)
	}
}
```
Run: `go test ./cmd/serf-tui/internal/tuipick/... -run 'TestModelPicker_RendersGroupHeadersOnTransition|TestModelPicker_RendersMetaTail|TestModelPicker_ZeroValueGroupMetaUnchangedForActionPicker' -count=1` → expect FAIL (compile error: `Group`/`Meta` undefined on `ModelPickerItem`).

- [ ] **Implement** — in `cmd/serf-tui/internal/tuipick/model_picker.go`:
  - Extend `ModelPickerItem`:
```go
type ModelPickerItem struct {
	ID             string
	Display        string
	DisabledReason string
	// Group labels the item's section for a browsable, provider-grouped
	// picker ("Recent", a provider name, ...); "" renders no header. Set only
	// by the model-picker path (hub_commands.go); zero-value for
	// NewTranscriptPicker/NewActionPicker leaves their rendering unchanged.
	Group string
	// Meta is a compact trailing tail (context window, price, capability
	// flags) appended dim after the row. "" renders nothing extra.
	Meta string
}
```
  - In `renderBody`, inside the `for i := start; i < end; i++ {` loop, add the group-header check immediately before the `cursor := "  "` line:
```go
		for i := start; i < end; i++ {
			item := filtered[i]
			if item.Group != "" && (i == 0 || filtered[i-1].Group != item.Group) {
				b.WriteString(tuitheme.MpDimStyle.Render(strings.ToUpper(item.Group)))
				b.WriteString("\n")
			}
			cursor := "  "
```
  - After the existing `if item.ID != item.Display && item.Display != "" { line += ... }` block, add:
```go
			if item.Meta != "" {
				line += "  " + tuitheme.MpDimStyle.Render(item.Meta)
			}
```

- [ ] **Run** `go test ./cmd/serf-tui/internal/tuipick/... -run 'TestModelPicker' -count=1` → PASS (all pre-existing `TestModelPicker_*` cases stay green).
- [ ] **Run** `go test ./cmd/serf-tui/internal/tuipick/... -count=1` (full package) and `golangci-lint run ./cmd/serf-tui/...`.
- [ ] **Commit** — `git add cmd/serf-tui/internal/tuipick/model_picker.go cmd/serf-tui/internal/tuipick/model_picker_test.go` → `feat(tuipick): ModelPickerItem gains Group/Meta for a browsable, badged picker`.

## Task 11 — TUI `modelPickerItems`: provider groups, prettify, catalog meta tail, dated-last sort

**Files:**
- Modify: `cmd/serf-tui/hub_commands.go` (`modelPickerItems`, ~line 323-342)
- Test: `cmd/serf-tui/hub_model_picker_items_test.go` (new)

**Interfaces:**
- Consumes: `llm.EmbeddedModelCatalog().LookupModelInfo` (direct import — `cmd/serf-tui` is part of the root Go module, so this needs no wire round trip; a documented simplification versus the web's `catalogModelInfo`, since the TUI has no `providers.toml`/behaviorTag context to resolve the tag-qualified fallback).
- Produces: `modelPickerItems` sets `Group` (provider) and `Meta` (catalog tail) on each `tuipick.ModelPickerItem`, prettifies `Display`, and sorts dated-snapshots last within each provider group.

- [ ] **Failing test** — create `cmd/serf-tui/hub_model_picker_items_test.go`:
```go
package main

import (
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuipick"
)

func TestModelPickerItems_SetsGroupAndPrettifiedDisplay(t *testing.T) {
	items := modelPickerItems([]appwire.ModelDescriptor{
		{Provider: "anthropic", Model: "claude-opus-4-6"},
	}, false)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0].Group != "anthropic" {
		t.Errorf("Group = %q, want %q", items[0].Group, "anthropic")
	}
	if items[0].Display != "Claude Opus 4 6" {
		t.Errorf("Display = %q, want %q", items[0].Display, "Claude Opus 4 6")
	}
	if items[0].ID != "anthropic/claude-opus-4-6" {
		t.Errorf("ID = %q, want the qualified ref unchanged", items[0].ID)
	}
}

func TestModelPickerItems_MetaTailFromCatalog(t *testing.T) {
	items := modelPickerItems([]appwire.ModelDescriptor{
		{Provider: "anthropic", Model: "claude-opus-4-6"},
	}, false)
	if !strings.Contains(items[0].Meta, "1M ctx") {
		t.Errorf("Meta = %q, want it to contain %q", items[0].Meta, "1M ctx")
	}
	if !strings.Contains(items[0].Meta, "$5.00/$25.00") {
		t.Errorf("Meta = %q, want it to contain price", items[0].Meta)
	}
	if !strings.Contains(items[0].Meta, "tools") || !strings.Contains(items[0].Meta, "vision") || !strings.Contains(items[0].Meta, "reasoning") {
		t.Errorf("Meta = %q, want tools/vision/reasoning capability flags", items[0].Meta)
	}
}

func TestModelPickerItems_UncataloguedModelStillRendersEmptyMeta(t *testing.T) {
	items := modelPickerItems([]appwire.ModelDescriptor{
		{Provider: "mycompany", Model: "totally-unknown-model-xyz"},
	}, false)
	if len(items) != 1 {
		t.Fatalf("uncatalogued model was dropped: got %d items, want 1", len(items))
	}
	if items[0].Meta != "" {
		t.Errorf("Meta = %q, want empty for an uncatalogued model", items[0].Meta)
	}
	if items[0].Display != "Totally Unknown Model Xyz" {
		t.Errorf("Display = %q, want the prettified id even when uncatalogued", items[0].Display)
	}
}

func TestModelPickerItems_SortsDatedSnapshotLastWithinProvider(t *testing.T) {
	items := modelPickerItems([]appwire.ModelDescriptor{
		{Provider: "anthropic", Model: "claude-opus-4-6-20251101"},
		{Provider: "anthropic", Model: "claude-opus-4-6"},
	}, false)
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].ID != "anthropic/claude-opus-4-6" || items[1].ID != "anthropic/claude-opus-4-6-20251101" {
		t.Fatalf("order = [%s, %s], want bare family id before its dated snapshot", items[0].ID, items[1].ID)
	}
}

// TestModelPickerItemProvider_ReadsGroupNotDisplay pins a regression this
// task would otherwise introduce: modelPickerItemProvider used to parse the
// provider out of item.Display (which used to be "provider/model"). Now that
// Display is the prettified bare name (no "/"), the diagnostics
// disabled-reason overlay in modelPickerItemsFromResponse would silently stop
// matching any provider. Group is now the authoritative source.
func TestModelPickerItemProvider_ReadsGroupNotDisplay(t *testing.T) {
	item := tuipick.ModelPickerItem{ID: "anthropic/claude-opus-4-6", Display: "Claude Opus 4 6", Group: "anthropic"}
	if got := modelPickerItemProvider(item); got != "anthropic" {
		t.Fatalf("modelPickerItemProvider = %q, want %q (from Group, not the prettified Display)", got, "anthropic")
	}
}
```
Run: `go test ./cmd/serf-tui/... -run 'TestModelPickerItems|TestModelPickerItemProvider' -count=1` → expect FAIL (`items[0].Group`/`.Meta` empty, order not yet sorted; `modelPickerItemProvider` returns "" because prettified Display has no "/").

- [ ] **Implement** — in `cmd/serf-tui/hub_commands.go`:
  - Add `"regexp"`, `"sort"`, `"unicode"`, and `"primeradiant.com/serf/llm"` to the import block.
  - Add, near `modelPickerItems`:
```go
// datedSnapshotSuffix and prettifyModelDisplayName are duplicated from
// cmd/serf-hub/web_spawn.go (a different binary/package; llm/model_catalog.go
// isn't owned by this track — see the plan's Global Constraints).
var datedSnapshotSuffix = regexp.MustCompile(`-\d{8}$`)

func isDatedSnapshotModelID(ref string) bool {
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		ref = ref[i+1:]
	}
	return datedSnapshotSuffix.MatchString(ref)
}

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

// formatModelContextWindow renders a token count compactly ("1M", "128K").
func formatModelContextWindow(n int) string {
	switch {
	case n >= 1_000_000:
		return strconv.Itoa(n/1_000_000) + "M"
	case n >= 1000:
		return strconv.Itoa(n/1000) + "K"
	default:
		return strconv.Itoa(n)
	}
}

// modelInfoMetaTail builds the model picker row's compact caps/ctx/price
// tail from a direct llm.EmbeddedModelCatalog() lookup. Unlike the web's
// catalogModelInfo, this has no providers.toml/behaviorTag to resolve the
// tag-qualified fallback — the TUI process has no such config — so it only
// tries the canonicalized bare lookup (LookupModelInfo). A nil mi (model not
// in the embedded catalog) yields "": the uncatalogued-model rule (still
// render name+provider+id, no badges) applies.
func modelInfoMetaTail(mi *llm.ModelInfo) string {
	if mi == nil {
		return ""
	}
	var parts []string
	if mi.ContextWindow > 0 {
		parts = append(parts, formatModelContextWindow(mi.ContextWindow)+" ctx")
	}
	if mi.InputCostPerMillion != nil && mi.OutputCostPerMillion != nil {
		parts = append(parts, fmt.Sprintf("$%.2f/$%.2f", *mi.InputCostPerMillion, *mi.OutputCostPerMillion))
	}
	var caps []string
	if mi.SupportsTools {
		caps = append(caps, "tools")
	}
	if mi.SupportsVision {
		caps = append(caps, "vision")
	}
	if mi.SupportsReasoning {
		caps = append(caps, "reasoning")
	}
	if len(caps) > 0 {
		parts = append(parts, strings.Join(caps, ","))
	}
	return strings.Join(parts, " · ")
}
```
  - Change `modelPickerItems`:
```go
func modelPickerItems(models []appwire.ModelDescriptor, rawModelID bool) []tuipick.ModelPickerItem {
	cat := llm.EmbeddedModelCatalog()
	items := make([]tuipick.ModelPickerItem, 0, len(models))
	for _, option := range models {
		model := strings.TrimSpace(option.Model)
		provider := strings.TrimSpace(option.Provider)
		if model == "" || (!rawModelID && provider == "") {
			continue
		}
		display := prettifyModelDisplayName(model)
		id := provider + "/" + model
		if rawModelID {
			id = model
		} else if provider == "" {
			id = model
		}
		var meta string
		if cat != nil {
			meta = modelInfoMetaTail(cat.LookupModelInfo(model))
		}
		items = append(items, tuipick.ModelPickerItem{ID: id, Display: display, Group: provider, Meta: meta})
	}
	sort.SliceStable(items, func(a, b int) bool {
		if items[a].Group != items[b].Group {
			return items[a].Group < items[b].Group
		}
		da, db := isDatedSnapshotModelID(items[a].ID), isDatedSnapshotModelID(items[b].ID)
		if da != db {
			return !da
		}
		return false
	})
	return items
}
```
  (Note: the original `id := display; if provider != "" { display = provider + "/" + model }` construction is replaced — `id` now always carries the qualified ref when a provider is present, matching the original behavior for `rawModelID == false`, and the original bare-model behavior for `rawModelID == true`.)
  - Fix `modelPickerItemProvider` (~line 375), which used to extract the provider by parsing `item.Display` as `"provider/model"` — that assumption breaks now that `Display` is the prettified bare name. `Group` is now the authoritative provider source:
```go
func modelPickerItemProvider(item tuipick.ModelPickerItem) string {
	if g := strings.TrimSpace(item.Group); g != "" {
		return g
	}
	display := strings.TrimSpace(item.Display)
	if display == "" {
		display = strings.TrimSpace(item.ID)
	}
	provider, _, ok := strings.Cut(display, "/")
	if !ok {
		return ""
	}
	return strings.TrimSpace(provider)
}
```
  (The Display/ID-parsing fallback is kept for defensiveness/tests that construct a bare `ModelPickerItem{}` without `Group`; every real caller through `modelPickerItems` always sets `Group` now.)

- [ ] **Run** `go test ./cmd/serf-tui/... -run 'TestModelPickerItems|TestModelPickerItemProvider' -count=1` → PASS.
- [ ] **Run** `go test ./cmd/serf-tui/... -run 'TestHubModel|TestModelPicker' -count=1` (existing model-list/picker tests, no regression).
- [ ] **Run** `go test ./cmd/serf-tui/... -count=1` (full package) and `golangci-lint run ./cmd/serf-tui/...`.
- [ ] **Commit** — `git add cmd/serf-tui/hub_commands.go cmd/serf-tui/hub_model_picker_items_test.go` → `feat(tui): provider-grouped, prettified, badged model picker rows`.

## Task 12 — TUI: prepend the `Recent` group from `resp.Recent`

**Files:**
- Modify: `cmd/serf-tui/hub_commands.go` (`modelPickerItemsFromResponse`, ~line 344-373)
- Test: `cmd/serf-tui/hub_model_picker_items_test.go` (extend)

**Interfaces:**
- Consumes: `appwire.ModelListResponse.Recent` (Task 1), already filtered/deduped/limited server-side (Task 3).
- Produces: `modelPickerItemsFromResponse` prepends a `Group: "Recent"` set of items ahead of the provider-grouped catalog, using the same enrichment (`modelPickerItems`) so Recent rows carry the same prettified name and Meta tail.

- [ ] **Failing test** — add to `cmd/serf-tui/hub_model_picker_items_test.go`:
```go
func TestModelPickerItemsFromResponse_PrependsRecentGroup(t *testing.T) {
	resp := appwire.ModelListResponse{
		Data: []appwire.ModelDescriptor{
			{Provider: "anthropic", Model: "claude-opus-4-6"},
			{Provider: "openai", Model: "gpt-5.2"},
		},
		Recent: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.2"}},
	}
	items := modelPickerItemsFromResponse(resp, false)
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3 (1 recent + 2 catalog, recent duplicated per design)", len(items))
	}
	if items[0].Group != "Recent" || items[0].ID != "openai/gpt-5.2" {
		t.Fatalf("items[0] = %+v, want the Recent-grouped gpt-5.2 first", items[0])
	}
	// The catalog copy of gpt-5.2 (under its provider group) still exists —
	// Recent is a shortcut, not a removal from the browsable catalog.
	var providerCopyFound bool
	for _, it := range items[1:] {
		if it.ID == "openai/gpt-5.2" && it.Group == "openai" {
			providerCopyFound = true
		}
	}
	if !providerCopyFound {
		t.Fatal("gpt-5.2 should still appear under its provider group, not just under Recent")
	}
}

func TestModelPickerItemsFromResponse_NoRecentOmitsGroup(t *testing.T) {
	resp := appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.2"}}}
	items := modelPickerItemsFromResponse(resp, false)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1 (fresh install, no Recent)", len(items))
	}
	if items[0].Group == "Recent" {
		t.Fatal("no Recent group should render when resp.Recent is empty")
	}
}
```
Run: `go test ./cmd/serf-tui/... -run 'TestModelPickerItemsFromResponse_PrependsRecentGroup|TestModelPickerItemsFromResponse_NoRecentOmitsGroup' -count=1` → expect FAIL (Recent not prepended).

- [ ] **Implement** — in `cmd/serf-tui/hub_commands.go`, change `modelPickerItemsFromResponse`:
```go
func modelPickerItemsFromResponse(resp appwire.ModelListResponse, rawModelID bool) []tuipick.ModelPickerItem {
	items := modelPickerItems(resp.Data, rawModelID)
	if len(resp.Diagnostics) > 0 {
		reasons := map[string]string{}
		for _, diagnostic := range resp.Diagnostics {
			provider := strings.TrimSpace(diagnostic.Provider)
			if provider == "" {
				continue
			}
			if _, exists := reasons[provider]; exists {
				continue
			}
			reasons[provider] = modelDiagnosticDisabledReason(diagnostic)
		}
		if len(reasons) > 0 {
			for i := range items {
				provider := modelPickerItemProvider(items[i])
				if provider == "" {
					continue
				}
				if reason := reasons[provider]; reason != "" {
					items[i].DisabledReason = reason
				}
			}
		}
	}
	if len(resp.Recent) == 0 {
		return items
	}
	recentItems := modelPickerItems(resp.Recent, rawModelID)
	for i := range recentItems {
		recentItems[i].Group = "Recent"
	}
	return append(recentItems, items...)
}
```
  (This restructures the existing diagnostics-overlay block from an early-return shape into a straight-line `if` so the Recent-prepend can share the same `items` — same behavior, just no `return items` in the middle. Diagnostics disabled-reasons are computed from `resp.Diagnostics` and applied to `items` before prepending `recentItems`, matching the diagnostics-overlay's existing scope of "the browsable catalog", not the Recent shortcut — a disabled provider's model still shows once in Recent without a disabled tag, which is acceptable since Recent only ever contains previously-launchable models.)

- [ ] **Run** `go test ./cmd/serf-tui/... -run 'TestModelPickerItemsFromResponse' -count=1` → PASS.
- [ ] **Run** `go test ./cmd/serf-tui/... -count=1` (full package, confirms `fetchHubModelsForHarness`/`fetchHubSessionModels`/`fetchHubSpawnOptions` callers still compile and their existing tests pass) and `golangci-lint run ./cmd/serf-tui/...`.
- [ ] **Commit** — `git add cmd/serf-tui/hub_commands.go cmd/serf-tui/hub_model_picker_items_test.go` → `feat(tui): prepend the Recent group to the model picker from resp.Recent`.

## Task 13 — End-to-end scenario cards + full-repo gates

Use the e2e-scenario-testing skill. Build fresh binaries (`go build -o /tmp/serf-hub ./cmd/serf-hub`, `go build -o /tmp/serf-tui ./cmd/serf-tui`, `go build -o /tmp/serf ./cmd/serf`) and author falsifiable scenario cards against a hermetic `$HOME`/dedicated Chrome profile:

- [ ] **Card: fresh install shows no Recent group.** With an empty `~/.serf` (no session history), open the web spawn model picker and the settings model picker; assert neither shows a "Recent" group/tab — only the provider-grouped catalog. Open the TUI's `n` (new session) model picker; assert the same.
- [ ] **Card: Recent reflects the last 5 distinct models across sessions, globally.** Launch and complete (or just spawn+idle) 6 sessions across at least 3 different models/providers from different working directories; assert the web spawn picker's Recent group shows exactly the 5 most-recently-touched distinct models (not the 6th, oldest), in most-recent-first order, and that it's the SAME 5 regardless of which working directory the spawn form is currently scoped to. Repeat the read against the TUI's model picker and the settings picker — all three must agree.
- [ ] **Card: uncatalogued live model still renders.** Configure a provider instance whose live-listed model id is absent from the embedded catalog (or stub one via `providers.toml` if the live provider can't be forced to report an unknown id); assert all three pickers still render a row for it (provider/model/name visible) with no badges/price/context shown, and that it's selectable and launches successfully.
- [ ] **Card: dated snapshot sorts last within its provider.** Pick a provider with both a bare family id and a dated snapshot in the live/embedded catalog (e.g. an Anthropic model); assert all three pickers list the bare id before its dated snapshot within that provider's group.
- [ ] **Card: badges match catalog data.** For a known catalogued model (e.g. `anthropic/claude-opus-4-6`), assert the web spawn picker's rendered badges/meta line include context window, price, and the tools/vision/reasoning flags that match the embedded catalog's actual values (cross-check against the `/api/models?diagnostics=1` JSON directly); assert the TUI's compact meta tail agrees on context window and price.
- [ ] **Record** the card outcomes; do not commit binaries. If a card fails, fix the root cause (not the card).

Full-repo gates (root module only — this track touches no other `GO_MODULES` entry):
- [ ] `go build ./...` and `go vet ./...` from the repo root (go build does not compile test files; vet catches what it misses).
- [ ] `go test ./appwire/... ./cmd/serf-hub/... ./cmd/serf-tui/... -count=1` → green.
- [ ] `golangci-lint run ./...` from the repo root → green.
- [ ] `make fuzz` — confirms no appwire decode golden references `ModelListResponse` in a way this field addition could break (Task 1's research found none, but this is the authoritative check); if `Test*Golden` drifts, `make fuzz-goldens` and re-verify.
- [ ] jstest: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh` → all green, including the four new files from Tasks 7-9.
- [ ] `make lint` (naming, internal, docs, golangci, generated, secret-scan) → green — confirms the new `recent` JSON field and all other new fields pass namingcheck without needing a `// serf:naming-ignore:` line.
- [ ] **Commit** any gate-driven fixups with a focused message; do not `git add -A`.

---

## Self-review — spec coverage

| Spec item (§4) | Task |
| --- | --- |
| `Recent` group, last 5 distinct models, global recency, derived from Past index, no manifest | Tasks 1-3 (wire field + `PastIndex.RecentModels` + `hubModelList` attachment) |
| Fresh install → no Recent group | Task 13 card; `RecentModels`/`attachRecentModels` both return nil/empty on no history (Tasks 2-3 tests) |
| Browsable (not search-gated) full catalog, provider-grouped | Preserved from existing `renderList("")`/`renderModels("")`/TUI's always-visible list — no behavior change needed there, confirmed during research |
| Dated snapshots sort last within a provider | Tasks 4 (web) and 11 (TUI) |
| Auto-prettified display names, no hand-maintained map | Tasks 4 and 11 (`prettifyModelDisplayName`, duplicated per file-ownership constraint) |
| Capability badges: tools/vision/reasoning+effort/web-search/context-window/max-output/price | Tasks 5, 7, 9 (web); Task 11 (TUI `modelInfoMetaTail`) |
| Graceful degradation: uncatalogued live model still renders, no badges | Task 5 (`TestModelDescriptorsToAPIModels_UncataloguedModelStillRendersWithoutBadges`), Task 7 (jstest), Task 11 (`TestModelPickerItems_UncataloguedModelStillRendersEmptyMeta`), Task 13 card |
| TUI: Recent group + compact caps/ctx/price tail per row, keep 15-visible/"…N total" | Tasks 10-12; `maxVisible`/"… N total" untouched |
| `llm/pricing.go` NOT adopted here | Confirmed: no task imports `llm/pricing.go`; badges read `ModelInfo.InputCostPerMillion`/`OutputCostPerMillion` directly, matching the existing web pattern |
| All three pickers (web spawn, web settings, TUI) | Tasks 7-8 (spawn.js), Task 9 (settings-pickers.js), Tasks 10-12 (TUI) |

**Type/name consistency check:** `prettifyModelDisplayName`/`isDatedSnapshotModelID`/`datedSnapshotSuffix` are each defined twice (once in `cmd/serf-hub/web_spawn.go`, once in `cmd/serf-tui/hub_commands.go`) with identical bodies and a comment cross-referencing the duplication and its reason — verified no accidental signature drift between the two copies while drafting Tasks 4 and 11. `appwire.ModelListResponse.Recent`, `hubcore.PastIndex.RecentModels`, and `recentModelsLimit` (=5) are the single source of the "last 5" constant, referenced from both `hubModelList`/`attachRecentModels` (Task 3) and `handleApiModels`'s default branch (Task 6) — not re-declared.

**Estimate check:** ~12 implementation tasks + 1 e2e/gates task. Rough loc incl. tests — Task set 1-3 (wire+hubcore+app_models) ~180-230; Task set 4-6 (web_spawn.go enrichment) ~220-280; Task set 7-9 (spawn.js/settings-pickers.js/CSS/jstest) ~260-330; Task set 10-12 (TUI) ~180-230. Total ~840-1,070, above the spec's ~500-750 track estimate — driven mostly by the jstest fixtures (Tasks 7-9, each a full JSDOM harness) and the deliberate duplication of the prettify/sort helpers across the two binaries; flagged, not a scope change.
