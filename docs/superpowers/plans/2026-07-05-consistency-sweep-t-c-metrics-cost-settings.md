# Consistency sweep — Track C: metrics & cost + new web settings

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give tokens a dollar-cost estimate everywhere they show (session status row, web
details panel, and a new per-turn transcript badge), routed through one pricing source of truth
(`llm/pricing.go` `GetPrice`, its first real caller); fill the two WS2-shipped-but-unwired display
gaps (`pastEntryThread` and the TUI details drawer); complete the web details panel's context-
pressure display; and add three new client-side web settings (font size, Enter-to-send, Show-cost)
in the per-section settings files Track 0 creates.

**Architecture:** Session-level cost is computed server-side in Go from already-wired
`appwire.SerfThread.Usage`/`WorkspaceData.Usage`/`hubapi.SessionDetail.Usage` plus the thread's
model id, via one new shared helper (`appwire.EstimateCost`) that both `cmd/serf-hub` and
`internal/appprojector` call — no new pricing arithmetic duplicated per surface. Per-turn cost/
tokens require one small piece of NEW wire plumbing the design spec undersold (flagged below):
`appwire.Turn` gains `Usage`/`Cost` fields, populated (a) live, by accumulating
`EventAssistantTextEnd`'s already-carried `Usage`/`Model` in the projector across a turn's rounds,
stamped at the same four completion sites that already stamp `CompletedAt`/`DurationMS`, and (b)
for ended sessions, by reading `schema.Turn.Usage` (already persisted per round) off the transcript
file. The per-turn badge itself is a brand-new UI element (no existing "turn" chip exists in the
web transcript today — see Scope note 1) built with the same hover/focus-reveal CSS technique as
the shipped tool-call timing chip. All `~$` display is gated client-side by a new `Show-cost`
`body[data-show-cost]` attribute + CSS rule (the server always computes/renders cost; the setting
only hides it), because a browser localStorage preference cannot gate server-side template
rendering. Show-cost/Enter-to-send/font-size all follow the existing `phone-density`/`sidebar-mode`
pattern: a radio/checkbox input, a `localStorage` write, a `body.dataset.*` mirror, a page-load IIFE
that reapplies it, and (for settings-pane re-sync) a hook in `applySettingsState()`.

**Tech Stack:** Go (root module: `appwire`, `internal/appprojector`, `internal/apptranscript`,
`cmd/serf-hub`, `cmd/serf-tui`, `cmd/serf`; `llm` module for pricing), htmx + vanilla JS (status
row, details panel, per-turn badge, settings), jstest (JSDOM), `docs/web-ui/design-system.md`.

## Global Constraints

- JSON/TOML keys stay snake_case (`hubapi`/`schema` convention); **appwire's own convention is
  camelCase** (`workMillis`, `activeTurnStartedAt`, `durationMs`) — new `appwire.Turn` fields
  (`usage`, `cost`) follow appwire's existing camelCase, not the snake_case rule that applies to
  `hubapi`/`schema`/TOML keys. `make lint` runs `serf-namingcheck`; verify it passes for both
  conventions on the files you touch.
- No new `SessionMeta` field. Confirmed during research: `WorkMillis`/`CumulativeUsage` already
  exist (WS2); cost is computed on read, not persisted. If any task in this plan seems to need a
  new persisted field, STOP — that is scope creep, not this plan.
- No new appwire RPC method/catalog entry. Every wire change here is a **struct field** addition
  (`appwire.Turn.Usage`/`.Cost`) — `TestDaemonRouterMatchesCatalog` enumerates `Method*`/`Notify*`
  constants only, so this needs no catalog touch (WS2 A7 precedent, re-confirmed).
- Track boundaries: **do not touch** Track A's files (sidebar.js, style.css state/glyph regions,
  renderer.js status regions, notifications.js, TUI hub_dashboard*/hub_session_view/composer
  question chip, hubcore tree/attention/prober/roster, hubapi/types state fields, server
  StatusInfo, appwire `AttentionEntry`) or Track B's files (app_models.go, web_spawn.go models
  path, spawn.js model picker, settings-pickers.js, TUI tuipick/model_picker.go, hub_commands.go
  model-list path, past.go). `appwire/types.go` is shared with Track A (different structs,
  `SerfThread`/`Turn` here vs `AttentionEntry` there) — expect a trivial merge, not a real
  conflict.
- TDD red-first for every behavioral change; test output must be pristine. Per-module test
  commands (`GO_MODULES` in Makefile) — this track's Go changes are entirely in the **root**
  module and the **llm** module (no `agent` module changes needed — see Scope note 2).
- `make lint` (namingcheck) before each commit that adds JSON-tagged fields.
- Never `git add -A`; stage only the exact paths listed per task, after `git status`.

## Scope notes (flag for Jesse — read before executing)

1. **The design spec's §5 claim "the wire already carries `Turn.CompletedAt`/`DurationMS`"
   undersells the per-turn badge's actual gap.** Verified: `appwire.Turn` (appwire/types.go:346-
   354) has no `Usage` field at all, and there is no existing "turn" UI element in the web
   transcript to "extend" — the shipped `2026-06-25-hover-only-turn-timing-metadata` feature only
   ever touched `.tool-call .tool-meta` (tool-invocation timing), never a conversation-turn-level
   chip. Phases R/S below build this from scratch: new `appwire.Turn.Usage`/`.Cost` fields, new
   projector accumulation logic, and a brand-new hover-reveal badge on the assistant-message
   element that closes each turn. This is real, justified new work, not a shortcut — flagging it
   because it pushes this track's loc past the spec's ~600-850 estimate (see Estimate section).
2. **No `agent` module changes needed.** `agent/schema/turn.go:37-39`'s `Turn.Usage llm.Usage` is
   already a **per-round** value (set at `agent/session.go:808` inside `appendAssistantTurn`, one
   call per model response) — the ended-session path (Phase R3) reads it directly, no new
   plumbing. For the **live** path, `agent/events/payloads.go:83-89`'s
   `AssistantTextEndData{Usage llm.Usage, Model string}` already carries per-round usage+model on
   the event the projector already receives — Phase R2 accumulates it in
   `internal/appprojector` (root module) alone. Neither touches `agent/session_lifecycle.go`,
   `agent/session_state.go`, or `agent/events/*` — the WS2-style "new `EventTurnEnded`" plumbing
   this might suggest by analogy is **not needed**.
3. **`llm/pricing.go` `GetPrice` needs strengthening, confirmed via trace.** Real stored model ids
   (`SessionMeta.Model`/`le.Model`/`thread.ModelProvider`) can be provider-qualified
   (`"anthropic/claude-3-opus"`, `"openrouter/anthropic/claude-3-opus"`, `"minimax/m2.7"` —
   `agent/provider/profile.go:505-528`'s `decidePrefixAction` keeps the namespace for meta-provider
   upstreams) or carry a literal `"[1m]"` suffix (`agent/provider/profile.go:743`,
   `llm/providers/anthropic/models.go:90`). `GetPrice`'s current exact-match-or-catalog-id-is-a-
   prefix-of-modelID lookup (`llm/pricing.go:30-64`) resolves none of these; `LookupModelInfo`
   (`llm/model_catalog.go:81-110`) already handles all three cases and is what
   `cmd/serf-hub/web_spawn.go:422-446`'s `catalogModelInfo` already uses for the picker. Phase P1
   fixes `GetPrice` to try `LookupModelInfo` first — a pure strengthening, zero regression risk
   (traced against every existing `pricing_test.go` case).
4. **Cost is web-only.** Decision #9/§6 frame Show-cost as a **web setting**; there is no TUI
   settings system to gate it and no design ask for TUI `$`. TUI gap-fill (Phase T2) adds only the
   already-scoped work-time/token line (mirroring the shipped `hub_status.go` lines), no `$`. Flag
   for Jesse if TUI cost is wanted later — small follow-up, not in this plan.
5. **Settings-file dependency on Track 0 (grounded in Track 0's committed plan
   `docs/superpowers/plans/2026-07-05-consistency-sweep-t0-settings-split.md`).** Track 0 lands
   first (merge order: Track 0 → A → B/C → D) and splits the monolithic `assets/settings.js` into
   per-section files, **deleting `assets/settings.js` entirely**. The HTML templates were already
   per-section (`templates/partials/settings/*.html`); Track 0 mirrors that on the JS side and
   **creates** `settings-appearance.js` (theme/phone-density/sidebar-mode — where font-size lands),
   `settings-notifications.js`, `settings-transcript.js`, `settings-shell.js`, `model-display.js`.
   It **reserves the name `settings-display.js` for this track but does NOT create it**, and does
   NOT create a "Display" HTML section (an empty nav entry would break its byte-identical-rendering
   rule). So this track: (a) font-size → Track 0's `theme.html` + `settings-appearance.js` (Task
   W1); (b) Enter-to-send + Show-cost → a **new "Display" section this track creates** —
   `templates/partials/settings/display.html` + `assets/settings-display.js` + a `settingsSections`
   registration in `web.go` + a nav link + an `app.html` `<script>` tag (Task W0 builds the
   scaffold; W2/W3 add the two controls). **No task in Phase W touches `assets/settings.js` — it
   will not exist post-Track-0.** The three controls share the `serf-hub.composer` JSON-blob pref
   (`{enterToSend, showCost}`) that W0's scaffold establishes, plus `serf-hub.appearance.fontSize`
   for font-size.

---

## File Structure

- `llm/pricing.go` — strengthen `GetPrice` resolution; add `EstimateCost` pure arithmetic.
- `llm/pricing_test.go` — new resolution + arithmetic tests.
- `appwire/cost.go` (new) — `SerfUsageFromLLM`, `EstimateCost` (wire-level, calls `llm`).
- `appwire/cost_test.go` (new) — marshal/omit + arithmetic tests.
- `appwire/types.go` — `Turn.Usage *SerfUsage`, `Turn.Cost string`.
- `appwire/types_test.go` — `Turn` marshal/omit tests.
- `internal/appprojector/appwire_projection.go` — `activeTurnUsage`/`activeTurnModel` accumulation
  + stamp at the four turn-completion sites.
- `internal/appprojector/appwire_projection_test.go` — projector usage-accumulation tests.
- `internal/apptranscript/apptranscript.go` — per-round `Usage` stamp in `TurnsFromFile`.
- `internal/apptranscript/apptranscript_test.go` — usage-stamp test.
- `cmd/serf/serve.go` — dedup `serfUsageFromLLM` onto `appwire.SerfUsageFromLLM`.
- `cmd/serf-hub/app_threadread.go` — `pastEntryThread` gains Usage/WorkMillis/ActiveTurnStartedAt;
  `pastEntryTurns` gains a per-turn Cost post-pass.
- `cmd/serf-hub/app_threadread_test.go` — new tests for both.
- `cmd/serf-hub/web_workspace.go` — session-level Cost wired at both `workspaceData` branches, at
  `renderInputStrip`'s map, and at `renderDetailsPanel` (context/work/tokens/cost rows, live+ended).
- `cmd/serf-hub/web_format.go` — session-level Cost wired at `workspaceDataFromAppThread`.
- `cmd/serf-hub/web_format_test.go`, `cmd/serf-hub/web_test.go` — new coverage.
- `cmd/serf-hub/templates/partials/input_strip.html` — `Cost` already has a dead `{{if .Cost}}`
  block (line 12); no template change needed, only the Go-side value.
- `cmd/serf-hub/assets/renderer.js` — stamp `turnId` on assistant-message elements; on
  `turn/completed`, attach/update the hover-reveal per-turn badge.
- `cmd/serf-hub/assets/renderer-format.js` — `turnMetaParts`/`formatTurnMetaText` display helpers.
- `cmd/serf-hub/assets/style.css` — `.assistant-message .turn-meta` hover/focus rules;
  `body[data-show-cost="false"]` gating rules; font-size preset `--text-*` overrides.
- `cmd/serf-hub/jstest/test-turn-meta-badge.js` (new), `test-show-cost-gating.js` (new),
  `test-font-size-presets.js` (new), `test-composer-shortcuts.js` (extended).
- `cmd/serf-hub/templates/partials/settings/theme.html` (Track 0's, font-size row),
  `templates/partials/settings/display.html` (NEW "Display" section, W0 creates it).
- `cmd/serf-hub/assets/settings-appearance.js` (Track 0's — font-size handler),
  `cmd/serf-hub/assets/settings-display.js` (NEW — Enter-to-send + Show-cost handlers, W0 creates it).
- `cmd/serf-hub/web.go` (register `"display"` in settingsSections),
  `cmd/serf-hub/templates/partials/settings.html` (Display nav link),
  `cmd/serf-hub/templates/app.html` (settings-display.js `<script>` tag) — W0.
- `cmd/serf-tui/details_drawer.go` — Work/Tokens summary line.
- `cmd/serf-tui/details_drawer_test.go` — new test.
- `docs/web-ui/design-system.md` — font-size preset documentation.

---

## Phase P — Pricing foundation (`llm` module)

### Task P1 — Strengthen `GetPrice` to resolve provider-qualified / `[1m]`-suffixed model ids

**Files:** Modify `llm/pricing.go`. Test: `llm/pricing_test.go`.

**Interfaces:**
- Consumes: existing `(c *ModelCatalog) LookupModelInfo(modelID string) *ModelInfo`
  (`llm/model_catalog.go:81-110`, unchanged).
- Produces: `(c *ModelCatalog) GetPrice(modelID string) (Price, bool)` — same signature, stronger
  resolution.

- [ ] **Failing test** — in `llm/pricing_test.go`, add:
  ```go
  func TestGetPrice_ProviderQualifiedRef(t *testing.T) {
  	cat := &ModelCatalog{
  		Models: []ModelInfo{
  			{ID: "claude-opus-4-5", InputCostPerMillion: f64(5.0), OutputCostPerMillion: f64(25.0)},
  		},
  	}
  	// Real stored session model ids can carry a provider namespace the
  	// catalog's bare key never sees (agent/provider/profile.go:505-528 keeps
  	// the namespace for meta-provider upstreams like openrouter/minimax).
  	p, ok := cat.GetPrice("anthropic/claude-opus-4-5")
  	if !ok {
  		t.Fatal("expected provider-qualified ref to resolve via LookupModelInfo")
  	}
  	if p.InputPerM != 5.0 || p.OutputPerM != 25.0 {
  		t.Errorf("got in=%v out=%v, want 5/25", p.InputPerM, p.OutputPerM)
  	}
  }

  func TestGetPrice_OneMillionContextSuffix(t *testing.T) {
  	cat := &ModelCatalog{
  		Models: []ModelInfo{
  			{ID: "claude-opus-4-5", InputCostPerMillion: f64(5.0), OutputCostPerMillion: f64(25.0)},
  		},
  	}
  	// The "[1m]" suffix (agent/provider/profile.go:743,
  	// llm/providers/anthropic/models.go:90) selects the 1M-context beta but
  	// carries no separate pricing entry — it must resolve to the base model.
  	p, ok := cat.GetPrice("claude-opus-4-5[1m]")
  	if !ok {
  		t.Fatal("expected [1m]-suffixed ref to resolve via LookupModelInfo")
  	}
  	if p.InputPerM != 5.0 {
  		t.Errorf("got in=%v, want 5.0", p.InputPerM)
  	}
  }
  ```
  Run: `cd llm && go test ./... -run 'TestGetPrice_ProviderQualifiedRef|TestGetPrice_OneMillionContextSuffix' -count=1` →
  expect FAIL (both return `ok=false` today: `GetModelInfo` exact-match fails, and the
  longest-prefix loop's `strings.HasPrefix(id, m.ID)` never matches a provider-prefixed or
  suffixed `id`).
- [ ] **Implement** — in `llm/pricing.go`, replace the primary lookup in `GetPrice` (currently
  `if mi := c.GetModelInfo(id); mi != nil { ... }`) with:
  ```go
  	if mi := c.LookupModelInfo(id); mi != nil {
  		if p, ok := priceFromModelInfo(mi); ok {
  			return p, true
  		}
  	}
  ```
  Leave the longest-prefix fallback loop below it unchanged (it stays as a safety net for the
  dated-snapshot case `LookupModelInfo`'s family-fallback doesn't cover, and
  `TestGetPrice_LongestPrefix`/`TestGetPrice_MissingBaseRates` already pin its exact behavior).
- [ ] **Run** `cd llm && go test ./... -run 'TestGetPrice|TestDefaultPrice' -count=1` → all green,
  including the two new tests and all five pre-existing `GetPrice`/`DefaultPrice` tests (traced
  by hand above: `LookupModelInfo` resolves the same bare/dated-snapshot ids `GetModelInfo` did,
  so no regression).
- [ ] **Run** `cd llm && golangci-lint run ./...` → green.
- [ ] **Commit** — `git add llm/pricing.go llm/pricing_test.go` →
  `fix(llm): GetPrice resolves provider-qualified and [1m]-suffixed model ids via LookupModelInfo`.

### Task P2 — `llm.EstimateCost` pure arithmetic

**Files:** Modify `llm/pricing.go`. Test: `llm/pricing_test.go`.

**Interfaces:**
- Produces: `func EstimateCost(inputTokens, cacheReadTokens, outputTokens int64, price Price) float64`.

- [ ] **Failing test** — in `llm/pricing_test.go`:
  ```go
  func TestEstimateCost_BlendsCacheReadAtItsOwnRate(t *testing.T) {
  	price := Price{InputPerM: 5.0, OutputPerM: 25.0, CacheReadPerM: f64(0.5)}
  	got := EstimateCost(1_000_000, 1_000_000, 1_000_000, price)
  	want := 5.0 + 0.5 + 25.0 // one million tokens of each tier
  	if !approxF(got, want) {
  		t.Errorf("got %v, want %v", got, want)
  	}
  }

  func TestEstimateCost_CacheReadFallsBackToInputRateWhenUncataloged(t *testing.T) {
  	price := Price{InputPerM: 5.0, OutputPerM: 25.0} // no CacheReadPerM
  	got := EstimateCost(0, 1_000_000, 0, price)
  	if !approxF(got, 5.0) {
  		t.Errorf("got %v, want 5.0 (cache-read priced at input rate)", got)
  	}
  }
  ```
  Run: `cd llm && go test ./... -run 'TestEstimateCost' -count=1` → FAIL (undefined `EstimateCost`).
- [ ] **Implement** — in `llm/pricing.go`, beside `priceFromModelInfo`:
  ```go
  // EstimateCost returns the blended dollar cost of the given token counts at
  // price's rates. Cache-read tokens price at CacheReadPerM when the catalog
  // has one, else at the input rate (an accepted approximation, not a hard
  // guarantee). Cache-creation cost is not counted: no caller here tracks a
  // cache-creation token count (cache-read/write cost breakout is explicitly
  // out of scope for the consistency-sweep cost feature).
  func EstimateCost(inputTokens, cacheReadTokens, outputTokens int64, price Price) float64 {
  	cacheReadRate := price.InputPerM
  	if price.CacheReadPerM != nil {
  		cacheReadRate = *price.CacheReadPerM
  	}
  	return float64(inputTokens)*price.InputPerM/1e6 +
  		float64(cacheReadTokens)*cacheReadRate/1e6 +
  		float64(outputTokens)*price.OutputPerM/1e6
  }
  ```
- [ ] **Run** `cd llm && go test ./... -run 'TestEstimateCost' -count=1` → pass. `golangci-lint run ./...` → green.
- [ ] **Commit** — `git add llm/pricing.go llm/pricing_test.go` →
  `feat(llm): EstimateCost blends input/cache-read/output token costs at price's rates`.

---

## Phase Q — Wire-level cost + usage conversion (`appwire`)

### Task Q1 — `appwire.SerfUsageFromLLM` + `appwire.EstimateCost`

**Files:** New `appwire/cost.go`. Test: new `appwire/cost_test.go`.

**Interfaces:**
- Produces: `func SerfUsageFromLLM(u llm.Usage) *SerfUsage`; `func EstimateCost(model string, usage *SerfUsage) string`.
- Consumes: `llm.DefaultPrice`, `llm.EstimateCost` (Task P2), existing `appwire.SerfUsage`.

- [ ] **Failing test** — new `appwire/cost_test.go`:
  ```go
  package appwire

  import (
  	"testing"

  	"primeradiant.com/serf/llm"
  )

  func TestSerfUsageFromLLM_NilWhenAllZero(t *testing.T) {
  	if got := SerfUsageFromLLM(llm.Usage{}); got != nil {
  		t.Errorf("got %+v, want nil for all-zero usage", got)
  	}
  }

  func TestSerfUsageFromLLM_MapsFields(t *testing.T) {
  	cacheRead := 7
  	got := SerfUsageFromLLM(llm.Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 10, CacheReadTokens: &cacheRead})
  	if got == nil || got.InputTokens != 1 || got.OutputTokens != 2 || got.TotalTokens != 10 || got.CacheReadTokens != 7 {
  		t.Errorf("got %+v, want {1 2 7 10}", got)
  	}
  }

  func TestEstimateCost_NilUsageReturnsEmpty(t *testing.T) {
  	if got := EstimateCost("claude-opus-4-5", nil); got != "" {
  		t.Errorf("got %q, want empty for nil usage", got)
  	}
  }

  func TestEstimateCost_UnpricedModelReturnsEmpty(t *testing.T) {
  	got := EstimateCost("totally-unknown-model-xyz", &SerfUsage{InputTokens: 100})
  	if got != "" {
  		t.Errorf("got %q, want empty for an unpriced model (not a misleading ~$0.00)", got)
  	}
  }

  func TestEstimateCost_FormatsToCents(t *testing.T) {
  	// claude-opus-4-5: $5/$25 per million (llm/pricing_test.go's known fixture values
  	// also hold for the embedded catalog per TestDefaultPrice_WellKnownModels).
  	got := EstimateCost("claude-opus-4-5", &SerfUsage{InputTokens: 100_000, OutputTokens: 20_000})
  	// 100_000/1e6*5 + 20_000/1e6*25 = 0.5 + 0.5 = 1.00
  	if got != "~$1.00" {
  		t.Errorf("got %q, want ~$1.00", got)
  	}
  }
  ```
  Run: `go test ./appwire/... -run 'TestSerfUsageFromLLM|TestEstimateCost' -count=1` → FAIL
  (undefined symbols).
- [ ] **Implement** — new `appwire/cost.go`:
  ```go
  package appwire

  import (
  	"fmt"

  	"primeradiant.com/serf/llm"
  )

  // SerfUsageFromLLM converts a raw llm.Usage into the wire SerfUsage shape,
  // returning nil when every total (including CacheReadTokens) is zero so
  // callers hide the usage cluster rather than render ↑0 ↓0 — the established
  // WS2 convention (mirrors cmd/serf/serve.go's serfUsageFromLLM and
  // cmd/serf-hub's serfUsageFromCumulative; this is the appwire-level home the
  // other two should eventually delegate to).
  func SerfUsageFromLLM(u llm.Usage) *SerfUsage {
  	cacheRead := int64(0)
  	if u.CacheReadTokens != nil {
  		cacheRead = int64(*u.CacheReadTokens)
  	}
  	if u.InputTokens == 0 && u.OutputTokens == 0 && cacheRead == 0 && u.TotalTokens == 0 {
  		return nil
  	}
  	return &SerfUsage{
  		InputTokens:     int64(u.InputTokens),
  		OutputTokens:    int64(u.OutputTokens),
  		CacheReadTokens: cacheRead,
  		TotalTokens:     int64(u.TotalTokens),
  	}
  }

  // EstimateCost returns a "~$X.XX" estimate of model's total cost for usage,
  // via llm's embedded-catalog pricing (llm.DefaultPrice — GetPrice's first
  // real caller). Returns "" when usage is nil or the model has no catalog
  // pricing, so callers render nothing rather than a misleading "~$0.00" for
  // an uncataloged model. The "~" marks every non-empty result as an
  // estimate, not a billing-exact figure.
  func EstimateCost(model string, usage *SerfUsage) string {
  	if usage == nil {
  		return ""
  	}
  	price, ok := llm.DefaultPrice(model)
  	if !ok {
  		return ""
  	}
  	dollars := llm.EstimateCost(usage.InputTokens, usage.CacheReadTokens, usage.OutputTokens, price)
  	return fmt.Sprintf("~$%.2f", dollars)
  }
  ```
- [ ] **Run** `go test ./appwire/... -run 'TestSerfUsageFromLLM|TestEstimateCost' -count=1` → pass.
  `golangci-lint run ./...` (root) → green.
- [ ] **Commit** — `git add appwire/cost.go appwire/cost_test.go` →
  `feat(appwire): SerfUsageFromLLM + EstimateCost — one cost path for session- and turn-level usage`.

### Task Q2 — Dedup `cmd/serf/serve.go`'s `serfUsageFromLLM` onto the shared helper

**Files:** Modify `cmd/serf/serve.go`.

- [ ] **Failing test** — none needed (pure refactor of a private function to delegate; existing
  callers/tests of `serfUsageFromLLM` in `cmd/serf` already cover its behavior). Run the existing
  suite first to confirm current green baseline: `go test ./cmd/serf/... -run 'ServeStatus|Usage' -count=1`.
- [ ] **Implement** — in `cmd/serf/serve.go:604`, replace the body of
  `func serfUsageFromLLM(u llm.Usage) *appwire.SerfUsage { ... }` with
  `return appwire.SerfUsageFromLLM(u)` (keep the function — its call sites stay unchanged — just
  delegate the body).
- [ ] **Run** `go test ./cmd/serf/... -count=1` → green (byte-identical behavior, same nil-when-
  all-zero rule). `golangci-lint run ./...` → green.
- [ ] **Commit** — `git add cmd/serf/serve.go` →
  `refactor(serve): serfUsageFromLLM delegates to appwire.SerfUsageFromLLM (dedup)`.

### Task Q3 — Pricing parity: cost path (GetPrice→LookupModelInfo) equals the picker's direct ModelInfo field reads

Spec Testing §5 requires a parity check between this track's cost path and Track B's picker, which
reads `ModelInfo.InputCostPerMillion`/`OutputCostPerMillion` directly (`web_spawn.go`'s
`catalogModelInfo` → `ModelInfo` fields). This test pins that the two agree — including on the exact
model-id shapes Task P1 found `GetPrice` had mishandled — so a future divergence (e.g. someone
"optimizes" one path) fails loudly. It lives in `llm` so it can read `ModelInfo` fields directly
(the way the picker does) without importing `cmd/serf-hub`.

**Files:** Test-only: `llm/pricing_test.go`.

**Interfaces:**
- Consumes: `EmbeddedModelCatalog()`, `(*ModelCatalog).GetPrice` (P1), `(*ModelCatalog).LookupModelInfo`,
  `EstimateCost` (P2), `ModelInfo.InputCostPerMillion`/`OutputCostPerMillion`/`CacheReadInputCostPerMillion`.

- [ ] **Failing-then-green test** — in `llm/pricing_test.go`, add
  `TestCostParity_GetPriceMatchesDirectModelInfoFieldReads`:
  ```go
  func TestCostParity_GetPriceMatchesDirectModelInfoFieldReads(t *testing.T) {
  	cat := EmbeddedModelCatalog()
  	// Representative ids INCLUDING the two shapes P1 fixed: a provider-qualified
  	// ref and a "[1m]"-suffixed ref. These must resolve identically whether cost
  	// comes from GetPrice (this track's path) or from a direct ModelInfo field
  	// read (Track B's picker path).
  	ids := []string{
  		"claude-opus-4-5",
  		"anthropic/claude-opus-4-5", // provider-qualified (P1)
  		"claude-opus-4-5[1m]",       // 1M-context suffix (P1)
  		"gpt-5-codex",
  	}
  	const inTok, cacheTok, outTok = int64(123_456), int64(0), int64(7_890)
  	for _, id := range ids {
  		t.Run(id, func(t *testing.T) {
  			// This track's path.
  			price, ok := cat.GetPrice(id)
  			if !ok {
  				t.Fatalf("GetPrice(%q) returned !ok — id should resolve after P1", id)
  			}
  			viaGetPrice := EstimateCost(inTok, cacheTok, outTok, price)

  			// The picker's path: resolve ModelInfo the same way catalogModelInfo
  			// does (LookupModelInfo), then read its cost fields directly.
  			mi := cat.LookupModelInfo(id)
  			if mi == nil || mi.InputCostPerMillion == nil || mi.OutputCostPerMillion == nil {
  				t.Fatalf("LookupModelInfo(%q) missing base cost fields", id)
  			}
  			viaDirectFields := float64(inTok)*(*mi.InputCostPerMillion)/1e6 +
  				float64(outTok)*(*mi.OutputCostPerMillion)/1e6
  			// cacheTok is 0 here, so cache-rate differences don't enter — this
  			// isolates the base-rate parity that both paths must agree on.

  			if !approxF(viaGetPrice, viaDirectFields) {
  				t.Errorf("cost parity mismatch for %q: GetPrice path=%v, direct-field path=%v", id, viaGetPrice, viaDirectFields)
  			}
  		})
  	}
  }
  ```
  This is a characterization/parity test: it should pass immediately once P1+P2 land (GetPrice and
  the direct read both funnel through the same `ModelInfo` fields via `priceFromModelInfo`/
  `LookupModelInfo`). Run: `cd llm && go test ./... -run 'TestCostParity' -count=1` → PASS. If it
  FAILS, the two paths genuinely diverge (e.g. `GetPrice`'s longest-prefix fallback resolved a
  *different* catalog entry than `LookupModelInfo` for one id) — that is a real bug this test exists
  to catch; report it rather than loosening the assertion.
  - **Note on the represented ids:** if a given id is absent from the embedded catalog at execution
    time (catalog contents drift), the test's `t.Fatalf` will flag it — swap in a currently-present
    id of the same shape (a real provider-qualified id and a real `[1m]`-capable id) rather than
    deleting the case; the point is to keep at least one provider-qualified and one `[1m]` id under
    parity coverage.
- [ ] **Run** `cd llm && go test ./... -run 'TestCostParity|TestGetPrice|TestEstimateCost' -count=1` → green.
  `golangci-lint run ./...` → green.
- [ ] **Commit** — `git add llm/pricing_test.go` →
  `test(llm): pin cost-path/picker pricing parity (incl. provider-qualified + [1m] ids)`.

---

## Phase R — Session-level cost display (the dead `Cost` field)

`cmd/serf-hub/web_types.go:181`'s `WorkspaceData.Cost string` and
`templates/partials/input_strip.html:12`'s `{{if .Cost}}<span class="status-item cost">…</span>{{end}}`
already exist — a stub, always set to `""` (`web_workspace.go:500`). This phase computes the real
value; **no template change is needed**, only the four Go call sites that build `WorkspaceData`/
`hubapi.SessionDetail`.

### Task R1 — Wire `Cost` at both `workspaceData` branches (roster-live + past-meta)

**Files:** Modify `cmd/serf-hub/web_workspace.go`. Test: `cmd/serf-hub/web_test.go`.

**Interfaces:**
- Consumes: `appwire.EstimateCost(model string, usage *appwire.SerfUsage) string` (Task Q1).

- [ ] **Failing test** — in `cmd/serf-hub/web_test.go`, add `TestWorkspaceData_LiveSessionCarriesCostEstimate`:
  build a roster live entry + `fetchStatus` stub (or a fake daemon status server, matching the
  existing pattern other roster-branch tests use — grep `s.fetchStatus` test doubles in
  `web_test.go` first) reporting `Model: "claude-opus-4-5"`, `Usage: &appwire.SerfUsage{InputTokens: 100_000, OutputTokens: 20_000}`;
  call `workspaceData(id)`; assert `data.Cost == "~$1.00"`. Add
  `TestWorkspaceData_PastSessionCarriesCostEstimate`: save a `schema.SessionMeta{Model: "claude-opus-4-5", CumulativeUsage: schema.CumulativeUsage{InputTokens: 100_000, OutputTokens: 20_000}}`,
  rebuild the past index, call `workspaceData(id)` for the now-ended session; assert
  `data.Cost == "~$1.00"`. Add `TestWorkspaceData_NoCostWhenUsageNil`: a fresh past-meta session
  with zero `CumulativeUsage`; assert `data.Cost == ""`. Run:
  `go test ./cmd/serf-hub/... -run 'TestWorkspaceData_.*Cost' -count=1` → FAIL (`Cost` unset/empty
  where a value is expected).
- [ ] **Implement** — in `cmd/serf-hub/web_workspace.go`:
  - Roster-live branch (~line 296, right after `data.ActiveTurnStartedAt = status.ActiveTurnStartedAt`):
    add `data.Cost = appwire.EstimateCost(data.Model, data.Usage)`. Place it AFTER `data.Model` is
    finalized (the block already updates `data.Model` from `status.Model` a few lines above) and
    after `data.Usage` is set, so both inputs are final.
  - Past-meta branch (~line 347, in the `WorkspaceData{...}` literal alongside `Usage: serfUsageFromCumulative(pe.Meta.CumulativeUsage)`):
    add a line right after the literal: `data.Cost = appwire.EstimateCost(data.Model, data.Usage)`
    (the literal already sets `Model: pe.Meta.Model`).
- [ ] **Run** `go test ./cmd/serf-hub/... -run 'TestWorkspaceData_.*Cost' -count=1` → pass.
  `golangci-lint run ./...` → green.
- [ ] **Commit** — `git add cmd/serf-hub/web_workspace.go cmd/serf-hub/web_test.go` →
  `feat(hub-web): compute session-level cost estimate for live and ended workspaceData`.

### Task R2 — Wire `Cost` at `workspaceDataFromAppThread` (remote/appwire path) and `renderInputStrip`

**Files:** Modify `cmd/serf-hub/web_format.go`, `cmd/serf-hub/web_workspace.go`. Test:
`cmd/serf-hub/web_format_test.go`, `cmd/serf-hub/web_test.go`.

- [ ] **Failing test 1** — in `cmd/serf-hub/web_format_test.go`, add
  `TestWorkspaceDataFromAppThread_CarriesCostEstimate`: build an `appwire.Thread{ModelProvider: "claude-opus-4-5", Serf: appwire.SerfThread{Usage: &appwire.SerfUsage{InputTokens: 100_000, OutputTokens: 20_000}}}`;
  call `workspaceDataFromAppThread(thread)`; assert `data.Cost == "~$1.00"`. Run:
  `go test ./cmd/serf-hub/... -run 'TestWorkspaceDataFromAppThread_CarriesCostEstimate' -count=1` → FAIL.
- [ ] **Implement 1** — in `cmd/serf-hub/web_format.go`'s `workspaceDataFromAppThread`, in the
  `WorkspaceData{...}` literal (after `ActiveTurnStartedAt: thread.Serf.ActiveTurnStartedAt,`), add
  a line after the literal: `data.Cost = appwire.EstimateCost(data.Model, data.Usage)` (the literal
  already sets `Model: thread.ModelProvider`).
- [ ] **Failing test 2** — in `cmd/serf-hub/web_test.go`, extend `TestWeb_State_RendersInputStatusPartial`
  (or add a sibling `TestWeb_State_RendersCostEstimate`): save a session meta with `Model` +
  non-zero `CumulativeUsage`; fetch `/_partials/s/<id>/state`; assert the body contains
  `class="status-item cost"` and the `~$X.XX` text. Add a negative case (zero usage) asserting the
  cost span is absent. Run → FAIL (renderInputStrip's map still hardcodes `"Cost": ""`).
- [ ] **Implement 2** — in `cmd/serf-hub/web_workspace.go`'s `renderInputStrip`, replace
  `"Cost": "",` in the `data := map[string]any{...}` literal with
  `"Cost": appwire.EstimateCost(detail.Model, detail.Usage),` (both already populated on `detail`,
  a `hubapi.SessionDetail`, by `apiSessionState`).
- [ ] **Run** `go test ./cmd/serf-hub/... -run 'Cost|InputStatus|State' -count=1` → all green.
  `golangci-lint run ./...` → green.
- [ ] **Commit** — `git add cmd/serf-hub/web_format.go cmd/serf-hub/web_workspace.go cmd/serf-hub/web_format_test.go cmd/serf-hub/web_test.go` →
  `feat(hub-web): cost estimate on the remote/appwire workspace path and the polled status row`.

---

## Phase S — Per-turn usage/cost wire plumbing

### Task S1 — `appwire.Turn` gains `Usage`/`Cost`

**Files:** Modify `appwire/types.go`. Test: `appwire/types_test.go`.

- [ ] **Failing test** — in `appwire/types_test.go`, add `TestTurnUsageCostJSONRoundTrip` (marshal
  a `Turn{Usage: &SerfUsage{InputTokens: 1}, Cost: "~$0.01"}`, assert `"usage":{"inputTokens":1}`
  and `"cost":"~$0.01"` present) and `TestTurnUsageCostOmitEmpty` (marshal a zero-value `Turn{}`,
  assert neither `"usage"` nor `"cost"` appears — mirrors `TestSerfThreadMetricsOmitEmpty`'s
  pattern at appwire/types_test.go:249). Run: `go test ./appwire/... -run 'TestTurnUsageCost' -count=1` → FAIL.
- [ ] **Implement** — in `appwire/types.go`'s `Turn` struct (after `DurationMS`):
  ```go
  	// Usage and Cost are the turn's own (not cumulative-session) token totals
  	// and estimated dollar cost — nil/empty when not computable (no usage
  	// data for this turn, or an uncataloged model). Populated live by
  	// summing EventAssistantTextEnd's per-round usage across the turn
  	// (internal/appprojector), and for ended sessions by reading the
  	// persisted per-round schema.Turn.Usage (internal/apptranscript).
  	Usage *SerfUsage `json:"usage,omitempty"`
  	Cost  string      `json:"cost,omitempty"`
  ```
- [ ] **Run** `go test ./appwire/... -run 'TestTurnUsageCost' -count=1` → pass. `golangci-lint run ./...` → green.
- [ ] **Run** `make fuzz` — confirm the appwire decode goldens still hold (new struct fields, no
  new methods; if `Test*Golden` drifts, `make fuzz-goldens` then re-verify).
- [ ] **Commit** — `git add appwire/types.go appwire/types_test.go` →
  `feat(appwire): Turn gains Usage/Cost — per-turn (not cumulative) token totals and cost estimate`.

### Task S2 — Projector: accumulate per-turn usage+model, stamp at completion

**Files:** Modify `internal/appprojector/appwire_projection.go`. Test:
`internal/appprojector/appwire_projection_test.go`.

**Interfaces:**
- Consumes: `events.AssistantTextEndData{Usage llm.Usage, Model string}` (agent/events/payloads.go:83-89,
  unchanged), `llm.Usage.Add` (llm/types.go:469-479, unchanged).
- Produces: `Turn.Usage`/`Turn.Cost` stamped at all four completion sites.

- [ ] **Failing test** — in `internal/appprojector/appwire_projection_test.go`, add
  `TestProjectorAccumulatesPerTurnUsageAcrossRounds`: drive
  `EventUserInput` → `EventAssistantTextStart` → `EventAssistantTextEnd{Usage: llm.Usage{InputTokens: 100, OutputTokens: 50}, Model: "claude-opus-4-5"}`
  → (a second round) `EventAssistantTextStart` → `EventAssistantTextEnd{Usage: llm.Usage{InputTokens: 20, OutputTokens: 10}, Model: "claude-opus-4-5"}`
  → `EventSessionEnd{State: "idle"}`; assert the completed `Turn.Usage` sums both rounds
  (`InputTokens == 120`, `OutputTokens == 60`) and `Turn.Cost` is a non-empty `"~$…"` string
  (exact value not asserted — cost's exact cents depend on the embedded catalog's live rates for
  `claude-opus-4-5`; assert format via a `strings.HasPrefix(turn.Cost, "~$")` check instead of a
  hardcoded number, to avoid coupling this test to catalog price changes).
  Add `TestProjectorNewTurnResetsUsageAccumulator`: run one turn with usage, complete it, start a
  SECOND turn with no `EventAssistantTextEnd` at all, complete it via `EventSessionEnd`; assert the
  second turn's `Usage == nil` (the accumulator reset in `startTurn()`, not leaked from turn one).
  Run: `go test ./internal/appprojector/... -run 'TestProjectorAccumulatesPerTurnUsage|TestProjectorNewTurnResetsUsageAccumulator' -count=1` → FAIL.
- [ ] **Implement**:
  - Add `"primeradiant.com/serf/llm"` to `internal/appprojector/appwire_projection.go`'s imports.
  - Add fields to `AppEventProjector` (beside `pendingDurationMS`):
    ```go
    	// activeTurnUsage/activeTurnModel accumulate the current turn's own
    	// (not cumulative-session) usage across every EventAssistantTextEnd
    	// since startTurn(), stamped onto the completing Turn at each of the
    	// four completion sites. Unlike pendingTurnID/pendingDurationMS, no
    	// stash-vs-completion-ordering race exists here: EventAssistantTextEnd
    	// always fires chronologically before the turn's own completion event.
    	activeTurnUsage llm.Usage
    	activeTurnModel string
    ```
  - In `startTurn()` (line ~966), reset both: add `p.activeTurnUsage = llm.Usage{}` and
    `p.activeTurnModel = ""` beside the existing `p.assistantItem = ""`/`p.assistantText = ""` reset.
  - In the `case events.EventAssistantTextEnd:` handler (line ~260), after
    `data := eventData[events.AssistantTextEndData](event.Data)`, add:
    ```go
    		p.activeTurnUsage = p.activeTurnUsage.Add(data.Usage)
    		if data.Model != "" {
    			p.activeTurnModel = data.Model
    		}
    ```
  - Add a helper beside `applyPendingTiming`:
    ```go
    // stampTurnUsage sets turn.Usage/Cost from the projector's per-turn
    // accumulator (see activeTurnUsage doc). No turnID match is needed — by
    // construction the accumulator always holds the completing turn's own
    // totals at the moment each of the four completion sites reads it (the
    // accumulator resets only in startTurn(), which the wrap-up sites call
    // AFTER building the completing Turn).
    func (p *AppEventProjector) stampTurnUsage(turn *appwire.Turn) {
    	usage := appwire.SerfUsageFromLLM(p.activeTurnUsage)
    	if usage == nil {
    		return
    	}
    	turn.Usage = usage
    	turn.Cost = appwire.EstimateCost(p.activeTurnModel, usage)
    }
    ```
  - Call `p.stampTurnUsage(&turn)` immediately after each of the four existing
    `p.applyPendingTiming(turnID, &turn)` calls (lines ~125, ~175, ~496, ~677 — re-grep exact
    lines at execution, they may have shifted from earlier tasks in this phase).
- [ ] **Run** the two new tests → pass. Run the full existing suite to confirm no regression:
  `go test ./internal/appprojector/... -count=1` (the interrupt/failed-turn timing tests at
  `TestProjectorTurnEndedPreservesInterruptStatus` etc. MUST stay green — usage stamping is
  additive, independent of status/timing).
- [ ] **Run** `golangci-lint run ./...` (root) → green.
- [ ] **Commit** — `git add internal/appprojector/appwire_projection.go internal/appprojector/appwire_projection_test.go` →
  `feat(appprojector): accumulate per-turn usage across rounds; stamp Turn.Usage/Cost at completion`.

### Task S3 — Ended-session path: per-round `Usage` from the transcript + per-turn `Cost` post-pass

**Files:** Modify `internal/apptranscript/apptranscript.go`, `cmd/serf-hub/app_threadread.go`.
Test: `internal/apptranscript/apptranscript_test.go`, `cmd/serf-hub/app_threadread_test.go`.

- [ ] **Failing test 1** — in `internal/apptranscript/apptranscript_test.go`, add
  `TestTurnsFromFile_StampsUsageFromEntry`: write a transcript JSONL fixture whose `"entry"` line's
  `"turn"` object carries `"usage":{"input_tokens":100,"output_tokens":50}` (matching
  `schema.Turn.Usage llm.Usage`'s JSON shape — check `llm.Usage`'s json tags at `llm/types.go:454-465`
  for the exact key names); call `TurnsFromFile(path, ..., project)`; assert the returned turn's
  `Usage != nil` with `InputTokens == 100`, `OutputTokens == 50`. Run:
  `go test ./internal/apptranscript/... -run 'TestTurnsFromFile_StampsUsageFromEntry' -count=1` → FAIL.
- [ ] **Implement 1** — in `internal/apptranscript/apptranscript.go`'s `TurnsFromFile`, in the
  `case "entry":` branch (~line 559-567, right after the `StartedAt` stamping block), add:
  ```go
  				if usage := appwire.SerfUsageFromLLM(entry.Turn.Usage); usage != nil {
  					turn.Usage = usage
  				}
  ```
  (reuse the same already-decoded `entry transcript.Entry` value the `StartedAt` block just
  unmarshaled — do not re-decode).
- [ ] **Run** `go test ./internal/apptranscript/... -count=1` → green (full package, confirm no
  regression in `TestTurnsFromFile` etc.). `golangci-lint run ./...` → green.
- [ ] **Failing test 2** — in `cmd/serf-hub/app_threadread_test.go`, add
  `TestPastEntryTurns_StampsCostFromSessionModel`: write a transcript fixture (same shape as test 1)
  under a `hubcore.PastEntry{Meta: schema.SessionMeta{Model: "claude-opus-4-5"}, ...}`; call
  `pastEntryTurns(entry)`; assert the turn with usage carries a non-empty `Cost` starting `"~$"`.
  Run: `go test ./cmd/serf-hub/... -run 'TestPastEntryTurns_StampsCostFromSessionModel' -count=1` → FAIL.
- [ ] **Implement 2** — in `cmd/serf-hub/app_threadread.go`'s `pastEntryTurns` (line 189), after
  the `pastTranscriptCache.TurnsFromFile(...)` call returns `turns`, add a post-pass before
  returning:
  ```go
  	for i := range turns {
  		if turns[i].Usage != nil {
  			turns[i].Cost = appwire.EstimateCost(entry.Meta.Model, turns[i].Usage)
  		}
  	}
  	return turns
  ```
  (adjust the existing function's control flow minimally — re-grep the current body first since it
  also does the jobstore-reconciliation dance; insert this loop right before its final `return`.)
- [ ] **Run** `go test ./cmd/serf-hub/... -run 'PastEntryTurns|ThreadRead' -count=1` → green.
  `golangci-lint run ./...` → green.
- [ ] **Commit** — `git add internal/apptranscript/apptranscript.go internal/apptranscript/apptranscript_test.go cmd/serf-hub/app_threadread.go cmd/serf-hub/app_threadread_test.go` →
  `feat(hub-web): ended-session transcript turns carry per-round usage + cost estimate`.

---

## Phase T — Per-turn hover badge (new web UI)

No existing "turn" chip exists to extend (Scope note 1) — this builds one, reusing the exact CSS
hover/focus-reveal technique the shipped tool-call timing chip established
(`2026-06-25-hover-only-turn-timing-metadata`), scoped to the assistant-message element that closes
each turn.

### Task T1 — Stamp `turnId` on assistant-message elements; attach the badge on `turn/completed`

**Files:** Modify `cmd/serf-hub/assets/renderer.js`, `cmd/serf-hub/assets/renderer-format.js`. Test:
new `cmd/serf-hub/jstest/test-turn-meta-badge.js`.

**Interfaces:**
- Consumes: `turn.usage {inputTokens,outputTokens,cacheReadTokens,totalTokens}`, `turn.cost` (string,
  may be absent), `turn.durationMs` (already wired), delivered on the existing `turn/completed`
  notification (`renderer.js:580,682`; `appwire.js:905`).
- Produces: `renderer-format.js` `turnMetaParts(turn)`/`formatTurnMetaText(turn)` → display parts/
  summary string; `renderer.js` DOM attachment of `.turn-meta` (with a nested `.cost` child span)
  onto `.assistant-message[data-turn-id="<id>"]`.

- [ ] **Failing test** — new `cmd/serf-hub/jstest/test-turn-meta-badge.js` (mirror
  `test-composer-shortcuts.js`'s JSDOM harness shape): build a DOM with a conversation container,
  drive the renderer through a user input + one assistant response with a mocked
  `turn/completed` notification carrying
  `{id: "turn_1", status: "completed", durationMs: 4200, usage: {inputTokens: 100, outputTokens: 50, totalTokens: 150}, cost: "~$0.01"}`;
  assert the resulting `.assistant-message` element contains a child `.turn-meta` span whose text
  includes `"4s"` (or the project's existing duration-format convention — reuse
  `formatToolDuration`/an equivalent turn-duration formatter, do not invent new duration text),
  `"150"` or `"↑100 ↓50"` (tokens), and `"~$0.01"` (cost); assert the `.turn-meta` span has
  `tabindex="0"` (the deliberate, narrowly-scoped tab stop — see Task T2's accessibility note).
  Run: `node cmd/serf-hub/jstest/test-turn-meta-badge.js` → FAIL (no such element exists yet).
- [ ] **Implement — stamp turnId** — in `cmd/serf-hub/assets/renderer.js`'s `beginAssistantMessage()`
  (~line 1909) and `appendAssistantBlock(text)` (~line 1922), after each `el.className = "assistant-message";` line, add
  `el.dataset.turnId = this.activeTurnId || "";`.
- [ ] **Implement — display helper** — in `cmd/serf-hub/assets/renderer-format.js`, add beside
  `formatToolDuration`. This returns a **plain-text summary** (for the hover `title=` tooltip) and
  the individual formatted parts (for DOM construction) rather than an HTML string — renderer.js
  builds real child nodes with `textContent`, matching this codebase's general preference for DOM
  APIs over `innerHTML` + string concatenation when a value could vary (no `escapeHtml` helper
  exists in `renderer-format.js`/`renderer.js` today; introducing one just for this would be new
  surface area this feature doesn't need):
  ```js
  function turnMetaParts(turn) {
    const parts = { duration: "", tokens: "", cost: "" };
    if (turn && typeof turn.durationMs === "number") parts.duration = formatToolDuration(turn.durationMs);
    if (turn && turn.usage) {
      const u = turn.usage;
      parts.tokens = "↑" + (u.inputTokens || 0) + " ↓" + (u.outputTokens || 0);
    }
    if (turn && turn.cost) parts.cost = turn.cost;
    return parts;
  }
  function formatTurnMetaText(turn) {
    const p = turnMetaParts(turn);
    return [p.duration, p.tokens, p.cost].filter(Boolean).join(" · ");
  }
  ```
  Add `turnMetaParts` and `formatTurnMetaText` to the module's exports object (alongside
  `formatToolDuration` etc., ~line 699).
- [ ] **Implement — attach on turn/completed** — in `cmd/serf-hub/assets/renderer.js`, at the
  `turn/completed` handling site (~line 580 inside `handleData`/the notification switch, and
  wherever the full `turn` object is available — re-grep for the exact `case "turn/completed":`
  block), after existing handling, add:
  ```js
        {
          const turn = data && data.turn;
          if (turn && turn.id) {
            const els = this.conversation.querySelectorAll('.assistant-message[data-turn-id="' + CSS.escape(turn.id) + '"]');
            const el = els.length ? els[els.length - 1] : null;
            const parts = window.SerfRendererFormat.turnMetaParts(turn);
            if (el && (parts.duration || parts.tokens || parts.cost)) {
              let meta = el.querySelector(".turn-meta");
              if (!meta) {
                meta = document.createElement("span");
                meta.className = "turn-meta";
                meta.tabIndex = 0;
                el.appendChild(meta);
              }
              meta.textContent = "";
              const segs = [parts.duration, parts.tokens].filter(Boolean);
              if (segs.length) meta.appendChild(document.createTextNode(segs.join(" · ")));
              if (parts.cost) {
                if (segs.length) meta.appendChild(document.createTextNode(" · "));
                const costEl = document.createElement("span");
                costEl.className = "cost";
                costEl.textContent = parts.cost;
                meta.appendChild(costEl);
              }
              meta.title = window.SerfRendererFormat.formatTurnMetaText(turn);
            }
          }
        }
  ```
  (Confirm the exact existing variable names for `data`/`turn` at this site during implementation —
  the notification payload shape is `{turnId, turn: {...}}` per `appwire.js:905`; adapt the
  destructuring to match what's already in scope rather than introducing a parallel read path. The
  `.cost` child span is built here — Task W3 depends on it existing for Show-cost CSS gating; no
  further DOM change needed there.)
- [ ] **Run** `node cmd/serf-hub/jstest/test-turn-meta-badge.js` → pass. Run the full jstest suite:
  `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh` → green
  (no regression in existing renderer tests).
- [ ] **Commit** — `git add cmd/serf-hub/assets/renderer.js cmd/serf-hub/assets/renderer-format.js cmd/serf-hub/jstest/test-turn-meta-badge.js` →
  `feat(hub-web): per-turn duration/tokens/cost hover badge on the assistant message that closes each turn`.

### Task T2 — CSS: hover/focus reveal for `.turn-meta` (mirrors `.tool-call .tool-meta`)

**Files:** Modify `cmd/serf-hub/assets/style.css`. Test: extend
`cmd/serf-hub/jstest/test-pane-and-sidebar-css.js`.

**Accessibility note:** `.assistant-message` has no other focusable descendant, so `:focus-within`
alone would leave keyboard users with no way to see the badge at all — the narrow exception the
shipped feature's own design doc carves out ("do not add unnecessary tab stops … unless testing
shows keyboard access is otherwise impossible"). `.turn-meta` itself gets `tabindex="0"` (set in
Task T1) so it is directly focusable; CSS reveals on `:focus` of itself, not `:focus-within` of the
parent.

- [ ] **Failing test** — in `cmd/serf-hub/jstest/test-pane-and-sidebar-css.js`, add (mirroring the
  shipped `.tool-call .tool-meta` assertions exactly):
  ```js
  pass(
    ruleContains(".assistant-message .turn-meta", /opacity:\s*0\b/) &&
      !ruleContains(".assistant-message .turn-meta", /visibility:\s*hidden\b/),
    "turn-meta badge should be visually hidden by default without visibility:hidden"
  );
  pass(
    ruleContains(".assistant-message:hover .turn-meta", /opacity:\s*1\b/),
    "turn-meta badge should reveal on message hover"
  );
  pass(
    ruleContains(".turn-meta:focus", /opacity:\s*1\b/),
    "turn-meta badge should reveal on its own keyboard focus"
  );
  ```
  Run: `node cmd/serf-hub/jstest/test-pane-and-sidebar-css.js` → FAIL.
- [ ] **Implement** — in `cmd/serf-hub/assets/style.css`, add near `.tool-call .tool-meta`:
  ```css
  .assistant-message { position: relative; }
  .assistant-message .turn-meta {
    display: inline-block;
    margin-left: var(--s2);
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: var(--text-xs);
    white-space: nowrap;
    opacity: 0;
    transition: opacity var(--motion-fast);
  }
  .assistant-message:hover .turn-meta,
  .turn-meta:focus {
    opacity: 1;
  }
  ```
  (`--s2` per the design system's spacing scale; if `--s2` is not defined in this file, grep for
  the actual spacing-token names in use — e.g. `var(--space-2)` — and match the existing
  convention rather than inventing a new token name.)
- [ ] **Run** `node cmd/serf-hub/jstest/test-pane-and-sidebar-css.js` → pass. `go test ./cmd/serf-hub/... -count=1` → green.
- [ ] **Commit** — `git add cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-pane-and-sidebar-css.js` →
  `feat(hub-web): hover/focus-reveal CSS for the per-turn metrics badge`.

---

## Phase U — WS2 gap-fill: `pastEntryThread` + TUI details drawer

### Task U1 — `pastEntryThread` carries Usage/WorkMillis/ActiveTurnStartedAt

**Files:** Modify `cmd/serf-hub/app_threadread.go`. Test: `cmd/serf-hub/app_threadread_test.go`.

- [ ] **Failing test** — in `cmd/serf-hub/app_threadread_test.go`, add
  `TestPastEntryThread_CarriesWorkMetrics`: build a `hubcore.PastEntry{Meta: schema.SessionMeta{WorkMillis: 5000, CumulativeUsage: schema.CumulativeUsage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150}}}`;
  call `pastEntryThread(entry, false)`; assert `thread.Serf.WorkMillis == 5000`,
  `thread.Serf.Usage != nil` with matching fields, and `thread.Serf.ActiveTurnStartedAt == 0`
  (ended — no turn in flight). Run:
  `go test ./cmd/serf-hub/... -run 'TestPastEntryThread_CarriesWorkMetrics' -count=1` → FAIL.
- [ ] **Implement** — in `cmd/serf-hub/app_threadread.go`'s `pastEntryThread` (line 121), in the
  `Serf: appwire.SerfThread{...}` literal (~line 152-163), add after `Capabilities: ...`:
  ```go
  			WorkMillis: entry.Meta.WorkMillis,
  			Usage:      serfUsageFromCumulative(entry.Meta.CumulativeUsage),
  			// ActiveTurnStartedAt stays 0 — an ended session has no turn in flight.
  ```
  (`serfUsageFromCumulative` already exists in `cmd/serf-hub/web_workspace.go:364`, same package —
  no new helper needed, reuse it verbatim.)
- [ ] **Run** `go test ./cmd/serf-hub/... -run 'PastEntryThread' -count=1` → pass. `golangci-lint run ./...` → green.
- [ ] **Commit** — `git add cmd/serf-hub/app_threadread.go cmd/serf-hub/app_threadread_test.go` →
  `fix(hub-web): pastEntryThread carries WorkMillis/Usage so a TUI user viewing an ended session sees metrics`.

### Task U2 — TUI details drawer: Work/Tokens summary line

**Files:** Modify `cmd/serf-tui/details_drawer.go`. Test: `cmd/serf-tui/details_drawer_test.go`.

- [ ] **Failing test** — in `cmd/serf-tui/details_drawer_test.go`, add
  `TestDetailsDrawerShowsWorkTimeAndTokens`: build
  `detailsDrawer{Detail: hubSessionDetail{WorkMillis: 4200, Usage: &appwire.SerfUsage{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 10, TotalTokens: 160}}}`;
  assert `d.View()` contains `"Work:"` and a token line containing `"↑100"`, `"↓50"`. Add
  `TestDetailsDrawerHidesWorkTimeAndTokensWhenAbsent`: a `hubSessionDetail{}` zero value; assert
  neither `"Work:"` nor a tokens line appears. Run:
  `go test ./cmd/serf-tui/... -run 'TestDetailsDrawerShowsWorkTimeAndTokens|TestDetailsDrawerHidesWorkTimeAndTokensWhenAbsent' -count=1` → FAIL.
- [ ] **Implement** — in `cmd/serf-tui/details_drawer.go`'s `View()` (after the `Context:` line at
  ~line 61-63), add (mirroring `hub_status.go:30-37`'s exact formatting so the drawer and the
  status chip strip agree):
  ```go
  	if detail.WorkMillis > 0 {
  		fmt.Fprintf(&b, "Work:     %s\n", ghostText(formatWorkMillis(detail.WorkMillis)))
  	}
  	if detail.Usage != nil {
  		u := detail.Usage
  		fmt.Fprintf(&b, "Tokens:   %s\n", ghostText(fmt.Sprintf("↑%s ↓%s · cache-read %s · total %s",
  			formatTokens(int(u.InputTokens)), formatTokens(int(u.OutputTokens)),
  			formatTokens(int(u.CacheReadTokens)), formatTokens(int(u.TotalTokens)))))
  	}
  ```
  (`formatWorkMillis`/`formatTokens` already exist in the same `main` package —
  `cmd/serf-tui/hub_status.go:101`, `cmd/serf-tui/statusbar.go:133` — no new helpers.)
- [ ] **Run** `go test ./cmd/serf-tui/... -count=1` → green. `golangci-lint run ./...` → green.
- [ ] **Commit** — `git add cmd/serf-tui/details_drawer.go cmd/serf-tui/details_drawer_test.go` →
  `feat(tui): details drawer shows work time + token totals (WS2 gap-fill)`.

---

## Phase V — Web details panel: context pressure + work/tokens/cost

`cmd/serf-hub/web_workspace.go:167-224`'s `renderDetailsPanel` renders a hand-built `<dl>` via a
`detailsRow{Label, Value string}` slice — today it has **no** context/work/tokens/cost rows at all
(not partial — fully absent) for either live or ended sessions.

### Task V1 — `detailsRow` gains an optional CSS class hook (for Show-cost gating)

**Files:** Modify `cmd/serf-hub/web_workspace.go`. Test: `cmd/serf-hub/web_test.go`.

- [ ] **Failing test** — in `cmd/serf-hub/web_test.go`, add
  `TestDetailsPanel_CostRowCarriesDataAttributeForGating`: render `/_partials/s/<id>/details` for a
  session with a computable cost; assert the body contains `data-row="cost"` on both the `<dt>`
  and `<dd>` for that row. Run → FAIL (no such attribute exists; `detailsRow` has no class/attr
  field yet).
- [ ] **Implement** — in `cmd/serf-hub/web_workspace.go`'s `renderDetailsPanel`:
  - Change the local `type detailsRow struct{ Label, Value string }` to
    `type detailsRow struct{ Label, Value, DataRow string }`.
  - Change the render loop from
    `fmt.Fprintf(w, `<dt>%s</dt><dd>%s</dd>`, htmlEscape(row.Label), htmlEscape(row.Value))` to:
    ```go
    		attr := ""
    		if row.DataRow != "" {
    			attr = ` data-row="` + htmlEscape(row.DataRow) + `"`
    		}
    		fmt.Fprintf(w, `<dt%s>%s</dt><dd%s>%s</dd>`, attr, htmlEscape(row.Label), attr, htmlEscape(row.Value))
    ```
- [ ] **Run** `go test ./cmd/serf-hub/... -run 'DetailsPanel' -count=1` → pass (no visible rows use
  `DataRow` yet — this task alone doesn't add the cost row, Task V2/V3 do; if the test written
  above needs the cost row to exist to assert on it, sequence this task's test AFTER V2/V3 land, or
  write a narrower unit test directly against a `detailsRow{DataRow: "cost"}` literal + a small
  render-loop helper extracted for testability — prefer extracting the row-rendering line into a
  small `func renderDetailsRow(w io.Writer, row detailsRow)` so this task's test can call it
  directly without needing a full HTTP round trip).
- [ ] **Commit** — `git add cmd/serf-hub/web_workspace.go cmd/serf-hub/web_test.go` →
  `refactor(hub-web): detailsRow carries an optional data-row attribute for CSS-gated rows`.

### Task V2 — Live-session rows: context pressure, work time, tokens, cost

**Files:** Modify `cmd/serf-hub/web_workspace.go`. Test: `cmd/serf-hub/web_test.go`.

- [ ] **Failing test** — in `cmd/serf-hub/web_test.go`, add
  `TestDetailsPanel_LiveSessionShowsContextWorkTokensCost`: build a roster live entry + a fake
  daemon `/status` response (matching the existing `fetchStatus` test-double pattern — grep for
  how other tests stub the daemon HTTP status endpoint, e.g. via `httptest.NewServer` registered
  on the roster entry's address) reporting `ContextPressure: 0.42`, `ContextUsed`/`ContextWindow`/
  `ContextRemaining`, `Model: "claude-opus-4-5"`, `WorkMillis: 4200`,
  `Usage: &appwire.SerfUsage{InputTokens: 100_000, OutputTokens: 20_000}`; fetch
  `/_partials/s/<id>/details`; assert the body contains a context row (`"42%"` and the formatted
  context numbers via `formatContextNumbers`), a work-time row (`formatWorkMillis(4200)`'s output),
  a tokens row (`"↑100"`/`"↓20"` or the full token-count formatting), and a cost row containing
  `"~$1.00"` with `data-row="cost"`. Run → FAIL (none of these rows exist).
- [ ] **Implement** — in `cmd/serf-hub/web_workspace.go`'s `renderDetailsPanel`, in the
  `if s.cfg.Roster != nil { if le, ok := ...; ok { ... } }` branch (the roster-live block, ~line
  201-206 where `daemon`/`pid` rows are already appended), fetch status the same way
  `workspaceData` does and append rows:
  ```go
  			if status := s.fetchStatus(le); status != nil {
  				if status.ContextWindow > 0 {
  					rows = append(rows, detailsRow{"context", fmt.Sprintf("%.0f%% used (%s)",
  						status.ContextPressure*100,
  						formatContextNumbers(status.ContextUsed, status.ContextWindow, status.ContextRemaining))})
  				}
  				if status.WorkMillis > 0 {
  					rows = append(rows, detailsRow{"work time", formatWorkMillis(status.WorkMillis)})
  				}
  				if status.Usage != nil {
  					u := status.Usage
  					rows = append(rows, detailsRow{"tokens", fmt.Sprintf("↑%s ↓%s · cache-read %s · total %s",
  						formatTokenCount(int(u.InputTokens)), formatTokenCount(int(u.OutputTokens)),
  						formatTokenCount(int(u.CacheReadTokens)), formatTokenCount(int(u.TotalTokens)))})
  				}
  				if cost := appwire.EstimateCost(status.Model, status.Usage); cost != "" {
  					rows = append(rows, detailsRow{Label: "cost", Value: cost, DataRow: "cost"})
  				}
  			}
  ```
  (Place this after the existing `daemon`/`pid` row append so ordering stays
  identity-then-runtime-then-metrics, matching the drawer's own row order in Task U2.)
- [ ] **Run** `go test ./cmd/serf-hub/... -run 'DetailsPanel' -count=1` → pass. `golangci-lint run ./...` → green.
- [ ] **Commit** — `git add cmd/serf-hub/web_workspace.go cmd/serf-hub/web_test.go` →
  `feat(hub-web): live-session details panel shows context pressure, work time, tokens, cost`.

### Task V3 — Ended-session rows: work time, tokens, cost

**Files:** Modify `cmd/serf-hub/web_workspace.go`. Test: `cmd/serf-hub/web_test.go`.

- [ ] **Failing test** — in `cmd/serf-hub/web_test.go`, add
  `TestDetailsPanel_EndedSessionShowsWorkTokensCostNoContext`: save a
  `schema.SessionMeta{Model: "claude-opus-4-5", WorkMillis: 4200, CumulativeUsage: schema.CumulativeUsage{InputTokens: 100_000, OutputTokens: 20_000}}`;
  fetch `/_partials/s/<id>/details`; assert work-time/tokens/cost rows are present (same content
  shape as V2) and NO context row is present (ended sessions have no live context pressure —
  mirrors the TUI drawer's `detail.ContextPressure > 0` guard, which is 0 for ended sessions since
  `pastEntryThread` never sets it, Scope note re-confirmed in Task U1's research). Run → FAIL.
- [ ] **Implement** — in `cmd/serf-hub/web_workspace.go`'s `renderDetailsPanel`'s `addMeta(m schema.SessionMeta)`
  closure (~line 176-200), after the existing `last input tokens` row, add:
  ```go
  		if m.WorkMillis > 0 {
  			rows = append(rows, detailsRow{"work time", formatWorkMillis(m.WorkMillis)})
  		}
  		if usage := serfUsageFromCumulative(m.CumulativeUsage); usage != nil {
  			rows = append(rows, detailsRow{"tokens", fmt.Sprintf("↑%s ↓%s · cache-read %s · total %s",
  				formatTokenCount(int(usage.InputTokens)), formatTokenCount(int(usage.OutputTokens)),
  				formatTokenCount(int(usage.CacheReadTokens)), formatTokenCount(int(usage.TotalTokens)))})
  			if cost := appwire.EstimateCost(m.Model, usage); cost != "" {
  				rows = append(rows, detailsRow{Label: "cost", Value: cost, DataRow: "cost"})
  			}
  		}
  ```
- [ ] **Run** `go test ./cmd/serf-hub/... -run 'DetailsPanel' -count=1` → pass. `golangci-lint run ./...` → green.
- [ ] **Commit** — `git add cmd/serf-hub/web_workspace.go cmd/serf-hub/web_test.go` →
  `feat(hub-web): ended-session details panel shows work time, tokens, cost`.

---

## Phase W — New web settings

### Task W0 — Create the "Display" settings section (Enter-to-send + Show-cost home) + confirm Track 0's contract

**Track 0 contract (from `docs/superpowers/plans/2026-07-05-consistency-sweep-t0-settings-split.md`,
confirmed):** Track 0 lands first and **creates** `assets/settings-shell.js`,
`assets/settings-appearance.js`, `assets/settings-notifications.js`,
`assets/settings-transcript.js`, `assets/model-display.js`, and **DELETES**
`assets/settings.js`. It **reserves the name `assets/settings-display.js` for this track but does
NOT create it**, and does NOT create a "Display" HTML section either (creating an empty nav entry
would violate its byte-identical-rendering rule). So:
- **Font-size** (Task W1) lands in the existing **Theme** section — HTML in
  `templates/partials/settings/theme.html`, behavior JS in `assets/settings-appearance.js`. Both
  files exist after Track 0 merges.
- **Enter-to-send + Show-cost** (Tasks W2/W3) form a **new "Display" section** this track creates:
  `templates/partials/settings/display.html` + `assets/settings-display.js` + a `settingsSections`
  registration + a nav link + an `app.html` `<script>` tag. This task creates the section scaffold
  (empty controls) so W2/W3 only add controls into a section that already routes and renders.

**Files:** Create `cmd/serf-hub/templates/partials/settings/display.html`,
`cmd/serf-hub/assets/settings-display.js`; modify `cmd/serf-hub/web.go`,
`cmd/serf-hub/templates/partials/settings.html`, `cmd/serf-hub/templates/app.html`. Test:
`cmd/serf-hub/web_settings_test.go`.

- [ ] **Confirm Track 0 landed** — run `ls cmd/serf-hub/assets/settings-appearance.js cmd/serf-hub/assets/settings-display.js`
  and `grep -n 'settingsSections' cmd/serf-hub/web.go`. Expect `settings-appearance.js` to EXIST
  (Track 0 created it — W1's target) and `settings-display.js` to NOT exist (this track's to
  create). If `settings-appearance.js` is missing, Track 0 has not merged into this worktree's base
  yet — STOP and report BLOCKED (the whole Phase W depends on the split landing first); do not
  re-derive Track 0's work here.
- [ ] **Failing test** — in `cmd/serf-hub/web_settings_test.go`, add
  `TestSettings_DisplaySectionRoutes`: `GET /_partials/settings/display` with the `HX-Request: true`
  header; assert `rec.Code == http.StatusOK` and the body contains the section's `<h2>` heading
  text (`"Display"`). (Mirror an existing settings-route test in this file for the exact
  `NewWebServer`/`httptest` setup; if none tests a section route directly, model it on
  `TestSettingsMCPStatus_PopulatedAndEmpty`'s server construction.) Run:
  `go test ./cmd/serf-hub/... -run 'TestSettings_DisplaySectionRoutes' -count=1` → FAIL (404: the
  `display` section isn't registered, so `settingsTmpls["display"]` is absent and `renderSettingsPartial`
  returns `http.NotFound`).
- [ ] **Implement — register the section** — in `cmd/serf-hub/web.go`, add `"display"` to the
  `settingsSections := []string{...}` slice literal (~line 84 — re-grep for the exact line; place
  `"display"` beside `"transcript"` so the display-toggle sections group together). The
  `settingsTmpls` loop just below it needs no special-casing (`display` is neither `credentials`
  nor `project`, so it takes the default `templates/partials/settings/display.html` branch).
- [ ] **Implement — the section template** — create
  `cmd/serf-hub/templates/partials/settings/display.html` (scaffold only; W2/W3 add the controls):
  ```html
  {{define "settings-content"}}
  <h2 class="settings-h2">Display</h2>
  <p class="settings-help">Composer and cost-display preferences. Saved per-browser.</p>
  <dl class="settings-table" data-display-form>
  </dl>
  {{end}}
  ```
- [ ] **Implement — nav link** — in `cmd/serf-hub/templates/partials/settings.html`, add a
  `<a class="settings-nav-link {{if eq .Active "display"}}active{{end}}" href="/settings/display" hx-get="/_partials/settings/display" hx-target="#settings-content" hx-swap="innerHTML" hx-push-url="/settings/display">Display</a>`
  line immediately after the existing `transcript` nav link (line ~15 — re-grep to confirm),
  matching that entry's attribute shape exactly.
- [ ] **Implement — settings-display.js scaffold + app.html script tag** — create
  `cmd/serf-hub/assets/settings-display.js` with the standard delegated-listener skeleton (W2/W3
  fill in the handlers; the file must exist and load so their tests can `readFileSync` it):
  ```js
  // Settings page interactivity — Display section: Enter-to-send and Show-cost
  // toggles. Uses event delegation on document.body so it works even when the
  // settings partial is htmx-swapped in (inline scripts in swapped content
  // don't reliably execute across all htmx versions). Mirrors the
  // settings-appearance.js / settings-notifications.js shape (2026-07
  // consistency sweep, Track C).
  (function () {
    "use strict";

    function readComposerPrefs() {
      let parsed = {};
      try { parsed = JSON.parse(localStorage.getItem("serf-hub.composer") || "{}") || {}; }
      catch (e) { parsed = {}; }
      // showCost defaults ON; enterToSend defaults OFF.
      return {
        enterToSend: parsed.enterToSend === true,
        showCost: parsed.showCost !== false,
      };
    }
    function writeComposerPrefs(prefs) {
      localStorage.setItem("serf-hub.composer", JSON.stringify(prefs));
    }
    function syncToggleState(input) {
      const span = input.parentElement.querySelector(".state");
      if (span) span.textContent = input.checked ? "ON" : "OFF";
    }

    // W2/W3 add: the change-listener branches for data-composer="enterToSend"
    // and data-composer="showCost", the applyDisplayState() restore function,
    // and the composer keybind-hint sync. Expose the pref helpers so those
    // additions and the page-load IIFEs below share one source of truth.
    window.SerfSettingsDisplay = { readComposerPrefs, writeComposerPrefs, syncToggleState };

    // Show-cost applies to <body> on every page load so the CSS gate
    // (body[data-show-cost="false"]) is correct before any settings pane opens.
    (function () {
      document.body.dataset.showCost = readComposerPrefs().showCost ? "true" : "false";
    })();
  })();
  ```
  In `cmd/serf-hub/templates/app.html`, add `  <script src="/assets/settings-display.js{{assetv}}"></script>`
  immediately after the `settings-transcript.js` line (the by-then-current script block Track 0
  established — re-grep for `settings-transcript.js` to place it).
- [ ] **Run** `go test ./cmd/serf-hub/... -run 'TestSettings_DisplaySectionRoutes' -count=1` → pass.
  Run `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh` →
  green (the scaffold JS is inert; no test regression). `go test ./cmd/serf-hub/... -count=1` →
  green (app.html + the new template parse). `golangci-lint run ./...` → green.
- [ ] **Commit** — `git add cmd/serf-hub/web.go cmd/serf-hub/templates/partials/settings.html cmd/serf-hub/templates/partials/settings/display.html cmd/serf-hub/assets/settings-display.js cmd/serf-hub/templates/app.html cmd/serf-hub/web_settings_test.go` →
  `feat(hub-web): add the Display settings section (Enter-to-send + Show-cost home) with a scaffold + pref helpers`.

### Task W1 — Font-size presets (S/M/L/XL)

**Files:** Modify `cmd/serf-hub/templates/partials/settings/theme.html`,
`cmd/serf-hub/assets/settings-appearance.js` (both created/owned by Track 0 — font-size is an
appearance/type-scale concern, same family as the existing phone-density radio, so it lands in the
Theme section per Track 0's contract), `cmd/serf-hub/assets/style.css`. Test:
new `cmd/serf-hub/jstest/test-font-size-presets.js`.

- [ ] **Failing test** — new `cmd/serf-hub/jstest/test-font-size-presets.js` (CSS-contract style,
  mirroring `test-pane-and-sidebar-css.js`'s `parseTopLevelBlocks`/`ruleContains` helpers — import
  or duplicate the same small parser): assert `body[data-font-size="s"]`, `="m"`, `="l"`, `="xl"`
  blocks each define all 8 `--text-*` tokens, and that within each block the values are strictly
  ascending in the same order as the base tokens (2xs < xs < sm < base < md < lg < xl < 2xl) —
  write this as a real assertion (parse each block's declarations, extract the px numbers, assert
  monotonic order) rather than hardcoding exact numbers, so a later manual tuning pass doesn't
  break the test. Run: `node cmd/serf-hub/jstest/test-font-size-presets.js` → FAIL (no such rules
  exist).
- [ ] **Implement — CSS** — in `cmd/serf-hub/assets/style.css`, near the `:root` token block
  (~line 69-76), add (values are a first-pass computed scale — 90/100/115/130% of the base tokens,
  rounded to the nearest px, kept strictly ascending; treat as provisional, adjust to taste on
  visual QA, not a hard requirement):
  ```css
  body[data-font-size="s"] {
    --text-2xs: 9px; --text-xs: 10px; --text-sm: 11px; --text-base: 12px;
    --text-md: 13px; --text-lg: 14px; --text-xl: 16px; --text-2xl: 20px;
  }
  body[data-font-size="m"] {
    --text-2xs: 10px; --text-xs: 11px; --text-sm: 12px; --text-base: 13px;
    --text-md: 14px; --text-lg: 16px; --text-xl: 18px; --text-2xl: 22px;
  }
  body[data-font-size="l"] {
    --text-2xs: 12px; --text-xs: 13px; --text-sm: 14px; --text-base: 15px;
    --text-md: 16px; --text-lg: 18px; --text-xl: 21px; --text-2xl: 25px;
  }
  body[data-font-size="xl"] {
    --text-2xs: 13px; --text-xs: 14px; --text-sm: 16px; --text-base: 17px;
    --text-md: 18px; --text-lg: 21px; --text-xl: 23px; --text-2xl: 29px;
  }
  ```
- [ ] **Implement — settings control** — in
  `cmd/serf-hub/templates/partials/settings/theme.html`, add a fourth `.row` (mirroring the
  existing phone-density/sidebar-mode radio-group markup in that same file exactly, inside the
  `<dl ... data-theme-form>`):
  ```html
  <div class="row">
    <dt>Font size</dt>
    <dd>
      <div class="val-radio-group" data-font-size-picker>
        <label class="val-radio"><input type="radio" name="font-size" value="s"> S</label>
        <label class="val-radio"><input type="radio" name="font-size" value="m"> M</label>
        <label class="val-radio"><input type="radio" name="font-size" value="l"> L</label>
        <label class="val-radio"><input type="radio" name="font-size" value="xl"> XL</label>
      </div>
    </dd>
    <p class="help">Scales all UI text. M is the default.</p>
  </div>
  ```
- [ ] **Implement — JS** — in `cmd/serf-hub/assets/settings-appearance.js` (Track 0's appearance
  file — the same one that holds theme/phone-density/sidebar-mode; NOT `settings.js`, which Track 0
  deleted):
  - In its `document.body.addEventListener("change", ...)` handler, add a branch (beside the
    existing `phone-density`/`sidebar-mode` branches):
    ```js
    if (target.matches('input[name="font-size"]')) {
      const v = target.value;
      localStorage.setItem("serf-hub.appearance.fontSize", v);
      document.body.dataset.fontSize = v;
      return;
    }
    ```
  - In its `applyAppearanceState()` restore function (Track 0's rename of the old
    `applySettingsState`'s appearance slice), add:
    ```js
    const fontSizeRadios = document.querySelectorAll('input[name="font-size"]');
    if (fontSizeRadios.length) {
      const stored = localStorage.getItem("serf-hub.appearance.fontSize") || "m";
      fontSizeRadios.forEach((r) => { r.checked = r.value === stored; });
    }
    ```
  - Add a page-load IIFE mirroring the phone-density/sidebar-mode "apply stored value on load"
    IIFEs already at the bottom of `settings-appearance.js`:
    ```js
    (function () {
      const KEY = "serf-hub.appearance.fontSize";
      document.body.dataset.fontSize = localStorage.getItem(KEY) || "m";
    })();
    ```
- [ ] **Run** `node cmd/serf-hub/jstest/test-font-size-presets.js` → pass. Run
  `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh` → green.
  `go test ./cmd/serf-hub/... -count=1` → green (templates still parse).
- [ ] **Commit** — `git add cmd/serf-hub/templates/partials/settings/theme.html cmd/serf-hub/assets/settings-appearance.js cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-font-size-presets.js` →
  `feat(hub-web): font-size presets (S/M/L/XL) scaling all --text-* tokens`.

### Task W2 — Enter-to-send toggle (web-only)

Lands in the **Display** section Task W0 created (`display.html` + `settings-display.js`) — NOT the
Track-0-deleted `settings.js`. Uses the `serf-hub.composer` JSON-blob pref
(`{enterToSend, showCost}`) and its `window.SerfSettingsDisplay.readComposerPrefs()` accessor that
W0's scaffold established, so Enter-to-send and Show-cost share one pref object.

**Files:** Modify `cmd/serf-hub/templates/partials/settings/display.html`,
`cmd/serf-hub/assets/settings-display.js`, `cmd/serf-hub/assets/renderer.js`. Test: extend
`cmd/serf-hub/jstest/test-composer-shortcuts.js`.

**Flagged conflict (not in the design spec — found during verification):** Shift+Enter is
*already* bound to "steer" (`renderer.js:5230-5238` — drains the queue as a steering injection,
matching the `⇧↵` hint already rendered at `workspace.html:91`). The design's "Enter-to-send ON ⇒
Shift+Enter = newline" would silently remove the existing Shift+Enter steer shortcut for anyone who
enables it. **Resolution:** when Enter-to-send is ON, Shift+Enter reverts to plain newline (steer
keybind unavailable) — the steer **button** stays fully clickable, only the keybind changes. This
is called out explicitly so a reviewer doesn't think the collision was missed.

- [ ] **Failing test** — extend `cmd/serf-hub/jstest/test-composer-shortcuts.js`: add
  `testEnterToSendModeSubmitsOnBareEnterAndNewlinesOnShiftEnter`: set
  `window.localStorage.setItem("serf-hub.composer", JSON.stringify({enterToSend: true}))` before
  `makeDOM`; dispatch
  a bare `Enter` keydown (no modifiers) — assert `submitCount === 1`; dispatch a `Shift+Enter`
  keydown — assert `steerCount === 0` (steer did NOT fire) and that `preventDefault` was NOT called
  on that event (so the browser's native newline insertion still happens — assert via
  `event.defaultPrevented === false` on a manually dispatched, non-`cancelable`-tracked event, or
  by checking the textarea's value grew a `\n` if the JSDOM environment supports native
  contentEditable-less textarea newline insertion — if JSDOM does not simulate default textarea
  keydown behavior, assert `defaultPrevented === false` instead, which is the actually-testable
  contract). Add `testEnterToSendModeOffPreservesExistingBehavior`: with the pref unset (or
  `"false"`), re-run the ORIGINAL `testMainWorkspaceShortcutsStillSubmitAndSteer` assertions
  unchanged (regression guard). Run: `node cmd/serf-hub/jstest/test-composer-shortcuts.js` → FAIL.
- [ ] **Implement — JS behavior** — in `cmd/serf-hub/assets/renderer.js`'s `bindKeyboard()`
  (~line 5213-5245):
  ```js
      bindKeyboard() {
        this.bindSubagentEscapeToParent();
        const ta = document.querySelector(".message-input");
        if (!ta) return;
        const suppressSubmitShortcuts = this.isInPane && this.isInPane();
        // Reads the same serf-hub.composer JSON blob the Display settings write
        // (via settings-display.js); enterToSend defaults OFF (absent/false).
        const enterToSend = () => {
          try { return (JSON.parse(localStorage.getItem("serf-hub.composer") || "{}") || {}).enterToSend === true; }
          catch (e) { return false; }
        };
        ta.addEventListener("keydown", (e) => {
          if (!suppressSubmitShortcuts && e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
            e.preventDefault();
            const form = ta.closest("form");
            if (form) form.requestSubmit();
            return;
          }
          if (!suppressSubmitShortcuts && enterToSend() && e.key === "Enter" && !e.shiftKey && !e.metaKey && !e.ctrlKey && !e.altKey) {
            e.preventDefault();
            const form = ta.closest("form");
            if (form) form.requestSubmit();
            return;
          }
          // Shift+Enter is the keybind equivalent of the "steer" button (kata
          // 0bq1) EXCEPT when Enter-to-send is on, where Shift+Enter must
          // revert to plain newline (Enter itself now sends) — the steer
          // BUTTON remains clickable either way, only this keybind changes.
          if (!suppressSubmitShortcuts && !enterToSend() && e.key === "Enter" && e.shiftKey && !e.metaKey && !e.ctrlKey && !e.altKey) {
            const steer = document.querySelector("[data-steer-trigger]");
            if (steer && !steer.disabled) {
              e.preventDefault();
              steer.click();
              return;
            }
          }
          if (e.key === "/" && !e.metaKey && !e.ctrlKey && !e.altKey && ta.value === "") {
            if (window.SerfSearch && typeof window.SerfSearch.openWith === "function") {
              e.preventDefault();
              window.SerfSearch.openWith("/");
            }
          }
        });
      },
  ```
- [ ] **Implement — settings-display.js: change handler + restore + kbd hint sync** — in
  `cmd/serf-hub/assets/settings-display.js` (the file W0 created), extend the IIFE:
  - Add a `document.body.addEventListener("change", ...)` branch for the `enterToSend` checkbox
    (following the `data-notif`/`data-transcript-status` commit pattern, but reading/writing the
    shared `serf-hub.composer` blob via the `readComposerPrefs`/`writeComposerPrefs` helpers W0
    defined):
    ```js
    document.body.addEventListener("change", (e) => {
      const target = e.target;
      if (!target || !target.matches) return;
      if (target.matches('input[type=checkbox][data-composer="enterToSend"]')) {
        const prefs = readComposerPrefs();
        prefs.enterToSend = target.checked;
        writeComposerPrefs(prefs);
        syncToggleState(target);
        applyComposerKeybindHints();
        if (window.SerfToast) window.SerfToast.show("Settings saved", "success");
        return;
      }
    });
    ```
  - Add an `applyDisplayState()` restore function (called on `DOMContentLoaded` + `htmx:afterSwap`,
    mirroring `applyAppearanceState`) that checks the `enterToSend` box from
    `readComposerPrefs().enterToSend` and calls `syncToggleState` on it.
  - Add `applyComposerKeybindHints()` inside the same IIFE (updates the composer's `<kbd>` hints to
    match the current mode), registered on `DOMContentLoaded` + `htmx:afterSwap`:
    ```js
    function applyComposerKeybindHints() {
      const sendKbd = document.querySelector(".send-btn kbd");
      const steerBtn = document.querySelector("[data-steer-trigger]");
      const steerKbd = steerBtn && steerBtn.querySelector("kbd");
      const on = readComposerPrefs().enterToSend;
      if (sendKbd) sendKbd.textContent = on ? "↵" : "⌘↵";
      if (steerKbd) steerKbd.textContent = on ? "" : "⇧↵";
    }
    document.addEventListener("DOMContentLoaded", applyComposerKeybindHints);
    document.body.addEventListener("htmx:afterSwap", applyComposerKeybindHints);
    ```
- [ ] **Implement — settings control** — in
  `cmd/serf-hub/templates/partials/settings/display.html`, add a `.row` inside the
  `<dl ... data-display-form>` W0 scaffolded:
  ```html
  <div class="row editable">
    <dt id="lbl-composer-enter-to-send">Enter sends</dt>
    <dd>
      <label class="val-toggle">
        <input type="checkbox" data-composer="enterToSend" aria-labelledby="lbl-composer-enter-to-send">
        <span class="state" aria-hidden="true">OFF</span>
      </label>
    </dd>
    <p class="help">Default off: ⌘/Ctrl-Enter sends, Enter inserts a newline. On: Enter sends, Shift-Enter inserts a newline (the steer keyboard shortcut is unavailable in this mode — the steer button still works).</p>
  </div>
  ```
- [ ] **Run** `node cmd/serf-hub/jstest/test-composer-shortcuts.js` → pass. Run
  `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh` → green.
  `go test ./cmd/serf-hub/... -count=1` → green.
- [ ] **Commit** — `git add cmd/serf-hub/templates/partials/settings/display.html cmd/serf-hub/assets/settings-display.js cmd/serf-hub/assets/renderer.js cmd/serf-hub/jstest/test-composer-shortcuts.js` →
  `feat(hub-web): Enter-to-send toggle (default off), resolving the Shift+Enter/steer keybind collision`.

### Task W3 — Show-cost toggle + CSS gating across all three cost surfaces

Lands in the same **Display** section (`display.html` + `settings-display.js`) as W2, sharing the
`serf-hub.composer` JSON blob and its `readComposerPrefs`/`writeComposerPrefs` helpers (W0). The
page-load body-attribute apply (`body[data-show-cost]`) already lives in W0's scaffold; this task
adds the toggle control and the CSS gate.

**Files:** Modify `cmd/serf-hub/templates/partials/settings/display.html`,
`cmd/serf-hub/assets/settings-display.js`, `cmd/serf-hub/assets/style.css`. Test: new
`cmd/serf-hub/jstest/test-show-cost-gating.js`.

- [ ] **Failing test** — new `cmd/serf-hub/jstest/test-show-cost-gating.js` (CSS-contract style):
  assert a rule exists hiding, under `body[data-show-cost="false"]`, all three cost-bearing
  selectors added in this plan: `.status-item.cost` (Phase R, input strip), `[data-row="cost"]`
  (Phase V, details panel), and `.turn-meta .cost` (Task T1 already built the per-turn badge with
  the cost portion in its own child `<span class="cost">` via real DOM nodes, specifically so
  Show-cost can hide just the `$` part without hiding duration/tokens too — no rework needed here,
  only the CSS rule). Run: `node cmd/serf-hub/jstest/test-show-cost-gating.js` → FAIL.
- [ ] **Implement — CSS gating** — in `cmd/serf-hub/assets/style.css`:
  ```css
  body[data-show-cost="false"] .status-item.cost,
  body[data-show-cost="false"] [data-row="cost"],
  body[data-show-cost="false"] .turn-meta .cost {
    display: none;
  }
  ```
- [ ] **Implement — settings control** — in
  `cmd/serf-hub/templates/partials/settings/display.html`, add a `.row` inside the
  `<dl ... data-display-form>` (after the Enter-sends row from W2):
  ```html
  <div class="row editable">
    <dt id="lbl-composer-show-cost">Show estimated cost</dt>
    <dd>
      <label class="val-toggle">
        <input type="checkbox" data-composer="showCost" aria-labelledby="lbl-composer-show-cost">
        <span class="state" aria-hidden="true">ON</span>
      </label>
    </dd>
    <p class="help">Default on. Shows an estimated ~$ cost next to token counts, from catalog pricing — an estimate, not a billing-exact figure.</p>
  </div>
  ```
- [ ] **Implement — JS** — in `cmd/serf-hub/assets/settings-display.js` (the same file W0/W2 build;
  `readComposerPrefs`/`writeComposerPrefs`/`syncToggleState` and the `body[data-show-cost]`
  page-load apply already exist from W0's scaffold — `showCost` defaults ON there via
  `parsed.showCost !== false`):
  - Add a `change`-handler branch for the `showCost` checkbox (in the same delegated listener W2
    added its `enterToSend` branch to), mirroring the `enterToSend` branch but toggling the CSS
    gate live:
    ```js
    if (target.matches('input[type=checkbox][data-composer="showCost"]')) {
      const prefs = readComposerPrefs();
      prefs.showCost = target.checked;
      writeComposerPrefs(prefs);
      syncToggleState(target);
      document.body.dataset.showCost = target.checked ? "true" : "false";
      if (window.SerfToast) window.SerfToast.show("Settings saved", "success");
      return;
    }
    ```
  - In the `applyDisplayState()` restore function (added in W2), also check the `showCost` box from
    `readComposerPrefs().showCost` and `syncToggleState` it (so its `ON`/`OFF` label is correct
    when the Display pane is swapped in).
- [ ] **Run** `node cmd/serf-hub/jstest/test-show-cost-gating.js` → pass. Run
  `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh` → green
  (including the T1/T2 tests — confirm the per-turn badge's `.cost` child span still renders and
  the duration/token assertions still hold). `go test ./cmd/serf-hub/... -count=1` → green.
- [ ] **Commit** — `git add cmd/serf-hub/templates/partials/settings/display.html cmd/serf-hub/assets/settings-display.js cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-show-cost-gating.js` →
  `feat(hub-web): Show-cost toggle (default on) gates ~$ display across the status row, details panel, and per-turn badge`.

---

## Phase X — Documentation

### Task X1 — Document font-size presets in the design system

**Files:** Modify `docs/web-ui/design-system.md`.

- [ ] **Implement** — in `docs/web-ui/design-system.md`, under `## 3. Tokens` → `### Type`
  (line 73-77), add a new subsection immediately after (the existing `--fs-*` names there are
  already stale versus the shipped `--text-2xs`…`--text-2xl` tokens in `style.css:69-76` — leave
  that pre-existing drift alone, out of scope for this task, but do NOT perpetuate it: name the
  new subsection's tokens after the real shipped ones):
  ```markdown
  ### Font-size presets

  Four user-selectable presets scale every `--text-*` token (base values: `--text-2xs 10` /
  `--text-xs 11` / `--text-sm 12` / `--text-base 13` / `--text-md 14` / `--text-lg 16` /
  `--text-xl 18` / `--text-2xl 22`, all px) via `body[data-font-size]`:

  | Preset | Scale | Setting |
  |---|---|---|
  | S | ~90% | Settings → Appearance → Font size |
  | M | 100% (default) | " |
  | L | ~115% | " |
  | XL | ~130% | " |

  Persisted per-browser in `localStorage` (`serf-hub.appearance.fontSize`), applied via a
  `body[data-font-size="…"]` attribute redefining the `--text-*` custom properties (they cascade
  to every descendant) — no per-element JS resize logic.
  ```
- [ ] **Run** — no automated test for prose docs; visually confirm the table renders sensibly in
  a markdown preview.
- [ ] **Commit** — `git add docs/web-ui/design-system.md` →
  `docs(web-ui): document the font-size preset system`.

---

## Phase Y — Full-repo gates + e2e

### Task Y1 — Full-repo gates

- [ ] `cd llm && golangci-lint run ./... && go test ./...` → green.
- [ ] From repo root: `golangci-lint run ./...` and
  `go test ./appwire/... ./internal/... ./cmd/... ./llm/... -count=1` → green.
- [ ] `make fuzz` (confirm appwire decode goldens hold after `Turn.Usage`/`.Cost`; if `Test*Golden`
  drifts, `make fuzz-goldens` then re-verify).
- [ ] `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh` →
  green (every new/extended jstest file from this plan, plus no regression in the existing suite).
- [ ] `make lint` (naming, internal, docs, golangci, generated, secret-scan) → green — this is
  where `Turn.Usage`/`.Cost`'s camelCase tags and the new settings keys get checked against
  `serf-namingcheck`.
- [ ] **Commit** any gate-driven fixups with a focused message; do not `git add -A`.

### Task Y2 — End-to-end scenario cards

Use the e2e-scenario-testing skill. Build fresh binaries
(`go build -o /tmp/serf ./cmd/serf`, `serf-hub`, `serf-tui`), run a live model per
`reference_serf_live_run`, author falsifiable scenario cards:

- [ ] **Card: cost estimate appears and is gateable.** Start a session, send one prompt to
  completion; assert the web status row shows `~$` next to tokens; toggle Show-cost off in
  Settings → confirm the `~$` (and the details-panel cost row, and the per-turn badge's cost
  portion) all disappear WITHOUT the page reloading (CSS-only); toggle back on → all reappear.
- [ ] **Card: per-turn badge reveals on hover/focus.** After a completed turn, confirm the
  duration/tokens/cost badge is present in the DOM but visually hidden by default, reveals on
  mouse hover over the assistant message, and reveals on Tab-focusing the badge directly.
- [ ] **Card: ended session shows metrics via the hub (TUI + web details panel).** Let a session
  end; open it from the Past index in the TUI (details drawer) and in the web (details panel);
  assert both show work time + tokens (+ cost where applicable) — the WS2 gap this plan fills.
- [ ] **Card: Enter-to-send toggle changes composer behavior live.** With the toggle off, confirm
  Enter inserts a newline and Shift+Enter steers; toggle it on; confirm Enter sends and
  Shift+Enter inserts a newline (steer button still works by click).
- [ ] **Card: font-size presets visibly change text size.** Cycle S/M/L/XL in Settings → Appearance;
  screenshot or visually confirm text size changes across the sidebar, transcript, and settings
  pane itself.
- [ ] **Record** the card outcomes; do not commit binaries. If a card fails, fix the root cause.

---

## Self-review

**Spec coverage:** §5 dollar cost (Phases P/Q/R/S), per-turn badge (Phase T — flagged as new work,
not an extension, per Scope note 1), the two WS2-skipped surfaces (Phase U), web details-panel
context pressure (Phase V, bundled with work/tokens/cost since the panel had none of the four),
§6 font-size/Enter-to-send/Show-cost (Phase W, gated on Track 0 via W0). No new `SessionMeta`
field. `GetPrice` is cost's one source of truth (Phase P1/Q1), and its resolution gap against real
stored model ids was found and fixed rather than assumed away.

**Drifted anchors found and corrected during verification (old → corrected):**
- "the wire already carries Turn.CompletedAt/DurationMS [implying per-turn tokens need only
  display work]" → `appwire.Turn` has no `Usage` field; new wire plumbing required (Scope note 1).
- "extend the shipped hover-only turn-timing chip" → no such per-logical-turn chip exists in the
  web transcript; only a per-tool-call chip (`.tool-call .tool-meta`) shipped. Built new, reusing
  its CSS technique (Phase T).
- "renderer.js `handleComposerKeydown`" → no such function name exists; the real site is
  `bindKeyboard()`'s keydown listener (`renderer.js:5213-5245`) (Task W2).
- "Cost display… gated by Show-cost" implicitly assumed a fresh field → `WorkspaceData.Cost
  string` and `input_strip.html`'s `{{if .Cost}}` block already exist as a dead stub (always `""`)
  — wired up, not created (Phase Q).
- "`llm/pricing.go` GetPrice, its first real caller" → confirmed zero non-test callers today;
  ALSO confirmed (not assumed) that its existing resolution would fail on real stored model ids
  (provider-qualified, `[1m]`-suffixed) — strengthened before adoption, not adopted as-is (P1).
- Track 0's settings-pane split → grounded in Track 0's committed plan, not guessed: Track 0
  **deletes `assets/settings.js`** and creates per-section JS files (`settings-appearance.js` etc.),
  reserving `settings-display.js` for this track without creating it. W0 therefore CREATES the new
  "Display" section (Enter-to-send + Show-cost home) and W1 targets Track 0's `theme.html` +
  `settings-appearance.js` for font-size — no Phase-W task references the deleted `settings.js`.
- Cost-path/picker pricing parity (spec Testing §5) → added as Task Q3, covering the exact
  provider-qualified and `[1m]`-suffixed ids P1 fixed, asserting `GetPrice` cost equals a direct
  `ModelInfo`-field read.

**Type/name consistency:** `appwire.SerfUsage` (existing) reused for `Turn.Usage` (no new usage
shape); `appwire.EstimateCost`/`llm.EstimateCost` (two layers: wire-level convenience wrapping the
pure arithmetic) both named `EstimateCost` deliberately (same concept, different layer, mirrors the
`Price`/`GetPrice` naming already established); `formatWorkMillis`/`formatTokens` reused verbatim
in the TUI drawer (Task U2) rather than reinvented; `serfUsageFromCumulative` (existing) reused
verbatim in `pastEntryThread` (U1) and the details panel (V3) rather than duplicated.

**Estimate check:** the design spec estimated Track C at ~600-850 loc. This plan's per-turn wire
plumbing (Phases S/T) — not foreseen at that granularity by the design spec (Scope note 1) — adds
roughly 250-350 loc beyond that estimate on its own. Rough loc incl. tests by phase: P ~90-120,
Q ~130-180 (Q1's new file + 3 call-site wirings + tests), R/S ~280-380 (wire fields + projector
accumulation + transcript stamping + all tests), T ~180-240 (new JS/CSS UI + jstest), U ~90-130,
V ~200-260 (four tasks, mostly test-heavy given zero prior coverage), W ~260-340 (three settings
controls + the W0 fallback-creation path + jstest), X ~20, Y ~20 (gates/cards, no loc). Total
~1,250-1,660, roughly 450-800 over the design spec's estimate — driven entirely by the per-turn
usage/cost wire plumbing the spec's own text implied was already done.
