# Track A — Unified Vocabulary/Icons + Ask-Aware Attention Tiering Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Consolidate the three duplicated attention-rank implementations into one shared `hubapi` source of truth; replace the web's text glyphs (`⟳ ◆ ✕`) with real line icons under a green/blue/amber/red/gray palette (needs-you moves amber→blue, warning gets its own amber identity, `processing`→`working` is a display-only rename); and land the ask-tiering remainder (§3–§7 of the folded-in spec) — an additive `pending_ask` wire bit, a three-band NeedsYou sort (errored → ask-pending → your-move), a `loudScope` notification preference, and blue `?`/`!` row markers — as one reviewed unit built on the consolidated rank.

**Architecture:** A new file `hubapi/attention.go` becomes the single shared home for `AttentionRank`, `RollupRank`, `NeedsYouBand`, and `StateWord` — pure functions with no dependencies, importable by both `cmd/serf-hub/internal/hubcore` and `cmd/serf-tui` (both live in the root Go module `primeradiant.com/serf`; `hubapi` is already a dependency-free package `cmd/serf-tui` imports elsewhere, so this needs no new module wiring). The web renders icon+dot with the state word as a hover tooltip via a small vendored Lucide SVG set (`cmd/serf-hub/assets/icons.js`); the TUI keeps the word, now unified via `hubapi.StateWord`. The `pending_ask` bit rides two parallel wire chains that both terminate at the same daemon-side `Server.pendingAskFn` callback: the HTTP-polling chain (`StatusInfo` → hub prober → `roster.LiveEntry` → `hubcore.TreeNode`/`AttentionEntry` → `hubapi.TreeNode`, consumed by the web) and the appwire JSON-RPC chain (`appwire.SerfThread` → `cmd/serf-tui`'s local `hubTreeNode`/`hubRow`, consumed by the TUI).

**Tech Stack:** Go (root module `primeradiant.com/serf` — `cmd/serf-hub/internal/hubcore`, `cmd/serf-tui`, `hubapi`, `server`, `cmd/serf`, `appwire`; all one module, no `go.work` cross-module wiring needed), vanilla JS (`cmd/serf-hub/assets/*.js`, JSDOM `jstest`), CSS custom properties (`cmd/serf-hub/assets/style.css`), inline SVG icons vendored from the Lucide project (ISC license, `github.com/lucide-icons/lucide`).

## Global Constraints

- JSON/TOML keys stay snake_case; wire enum values `active`/`awaiting`/`warning`/`errored` are the Codex-shaped contract and are **never** renamed. `processing`→`working` touches only CSS custom-property names, TUI theme-token identifiers (`StateProcessing`→`StateWorking`), and Go/JS display-word strings — never the wire state string `"active"`.
- `appwire`/`hubcore` parallel-type camelCase JSON fields (e.g. `askPending`) require a `// serf:naming-ignore` comment on the line immediately above the field (bare comment, no suffix — confirmed against the existing `AttentionEntry.ID`/`AttentionSummary.NeedsYou` precedent and `cmd/serf-namingcheck/main.go`'s `ignoreMarker`).
- New struct **fields** (e.g. `PendingAsk` on `StatusInfo`, `AskPending` on `TreeNode`/`AttentionEntry`/`SerfThread`) need no appwire dual-router catalog change — only new/renamed **methods** do (confirmed: `server/appwire_catalog_test.go`'s `TestDaemonRouterMatchesCatalog` and `appwire/cov_rhub_appwire_test.go` diff only registered method names against `appwire.Methods`/`Notifications`, never struct shape).
- `make lint` runs `lint-naming` (`go run ./cmd/serf-namingcheck`) in addition to `golangci-lint`; per-task `golangci-lint run ./...` misses the naming check, so run `make lint` before any merge-readiness claim.
- `GO_MODULES := . agent llm auth envvars fuzz invariant` (root Makefile) — run `go test ./...` from the relevant module root; everything this track touches (`hubapi`, `hubcore`, `cmd/serf-tui`, `server`, `cmd/serf`, `appwire`) is in the root module `.`, so `go test ./...` from the repo root covers it (no `cd agent`/`cd llm` needed for this track).
- jstest: `sh cmd/serf-hub/jstest/run-all.sh` (auto-detects `NODE_PATH`, falling back to `/tmp/serf-jstest-jsdom/node_modules`).
- Never `git add -A`; every commit lists exact paths.
- **Corrected module-boundary fact** (the design spec's phrasing is imprecise): `cmd/serf-tui` and `cmd/serf-hub/internal/hubcore` are **not** separate Go modules — there is no `cmd/serf-tui/go.mod`; both live in the root module `primeradiant.com/serf`. The reason `cmd/serf-tui` cannot import `hubcore` directly is Go's `internal/` visibility rule (an `internal` package is importable only by code rooted at the parent of `internal/`, i.e. anything under `cmd/serf-hub/`), not a module boundary. `hubapi` has no such restriction and is already imported by `cmd/serf-tui/internal/hubstart/hub_start.go`, making it the correct shared home.
- Icon SVGs are vendored verbatim from `https://raw.githubusercontent.com/lucide-icons/lucide/main/icons/<name>.svg` (ISC license) — fetched and verified during planning (2026-07-05); do not hand-author path data.

---

## File Structure

**Created:**
- `hubapi/attention.go` — `AttentionRank`, `RollupRank`, `NeedsYouBand`, `StateWord` (the shared source of truth).
- `hubapi/attention_test.go` — table tests for all four.
- `cmd/serf-hub/assets/icons.js` — vendored Lucide SVG markup, keyed by unified-vocabulary state name.
- `cmd/serf-hub/jstest/test-icons.js` — sanity test that every `SerfIcons` entry parses as an SVG element with the expected role.
- `cmd/serf-hub/jstest/test-style-palette.js` — palette recolor + `processing`→`working` rename assertions against `style.css`.
- `cmd/serf-hub/jstest/test-style-colorblind-shapes.js` — warning's distinct dot shape.
- `cmd/serf-hub/jstest/test-sidebar-icons.js` — status-dot icon+tooltip rendering, rollup-badge icons, `data-ask` marker.
- `cmd/serf-hub/jstest/test-notifications-palette.js` — `STATE_COLORS` recolor.
- `cmd/serf-hub/jstest/test-renderer-subagent-glyphs.js` — subagent glyph/tally icon swap.
- `cmd/serf-hub/jstest/test-renderer-format-plan-glyphs.js` — plan/task glyph icon swap.
- `cmd/serf-hub/jstest/test-renderer-needsyou-affordances.js` — needs-you affordance + ask-chip icon swap.
- `cmd/serf-hub/jstest/test-notifications-loudscope.js` — `loudScope` migration + gating.
- `cmd/serf-hub/jstest/test-settings-loudscope.js` — `loudScope` settings-pane radio control.
- `test/scenarios/status-vocabulary-roundtrip.md` — new e2e scenario card.

**Modified (Go):**
- `cmd/serf-hub/internal/hubcore/tree.go` (`AttentionRank`/`rollupRank` removed in favor of `hubapi`; `TreeNode.AskPending`; NeedsYou band sort).
- `cmd/serf-hub/internal/hubcore/tree_test.go` (rank tests retargeted to `hubapi`; band-sort test extended).
- `cmd/serf-hub/internal/hubcore/attention.go` (`AttentionEntry.AskPending`; `DeriveAttention`; `AttentionWatcher.Tick` diffs on ask-flip too).
- `cmd/serf-hub/internal/hubcore/attention_test.go` (new cases).
- `cmd/serf-hub/internal/hubcore/prober.go` (`statusInfo.PendingAsk`; `Prober.Probe` gains a 4th return).
- `cmd/serf-hub/internal/hubcore/prober_test.go` (new case).
- `cmd/serf-hub/internal/hubcore/roster.go` (`LiveEntry.PendingAsk`; `probeResult`; `rosterFingerprint` hashes it too).
- `cmd/serf-hub/internal/hubcore/roster_test.go` (new case).
- `cmd/serf-hub/web_api_tree.go` (`apiTreeNode` copies `AskPending`).
- `cmd/serf-hub/web_api_tree_test.go` (new case).
- `cmd/serf-hub/web_format.go` (`stateLabel` delegates to `hubapi.StateWord`).
- `cmd/serf-hub/web_format_test.go` (new cases).
- `hubapi/types.go` (`TreeNode.AskPending` — `ask_pending,omitempty`).
- `hubapi/types_test.go` (round-trip case).
- `server/server.go` (`StatusInfo.PendingAsk`; `pendingAskFn` field + `SetPendingAskFunc`).
- `server/server_handlers.go` (`handleStatus` overlays `pendingAskFn`).
- `server/server_test.go` / `server/awaiting_status_test.go` (new cases).
- `server/appwire_runtime.go` (`appThread()` overlays `pendingAskFn` into `SerfThread.AskPending`).
- `server/appwire_runtime_test.go` (new case).
- `appwire/types.go` (`SerfThread.AskPending`).
- `appwire/types_test.go` (round-trip case).
- `cmd/serf/serve.go` (`srv.SetPendingAskFunc(func() bool { return getSession().HasPendingAsk() })`).
- `cmd/serf-tui/hub_types.go` (`hubTreeNode.AskPending`; `hubSessionDetail.AskPending`; `hubNodeFromThread`/`hubDetailFromThread` read it).
- `cmd/serf-tui/hub_model.go` (`hubRow.askPending`).
- `cmd/serf-tui/hub_dashboard.go` (`addSession` copies `askPending`; `dashboardRowLess` gains the band).
- `cmd/serf-tui/hub_dashboard_view.go` (`attentionRankLabel` delegates to `hubapi.AttentionRank`; new `displayWord`; `projectSummary` rewritten; row marker for ask-pending).
- `cmd/serf-tui/hub_dashboard_view_test.go` / `dashboard_rows_test.go` (new cases).
- `cmd/serf-tui/hub_session_view.go` (`StatusBadge` label uses `displayWord`).
- `cmd/serf-tui/internal/tuitheme/tokens.go` (`StateProcessing`→`StateWorking` rename + recolor; `StateAwaiting` recolor; `StateIdle` recolor).
- `cmd/serf-tui/internal/tuitheme/tokens_test.go` (updated).
- `cmd/serf-tui/internal/tuitheme/styles.go`, `cmd/serf-tui/composer_render.go`, `cmd/serf-tui/internal/msgrender/message.go`, `cmd/serf-tui/internal/msgrender/tool_bodies.go` (token rename call sites only).
- `cmd/serf-hub/assets/style.css` (palette recolor across all 4 theme blocks; colorblind shapes; glyph legend).
- `cmd/serf-hub/assets/sidebar.js` (status-dot → icon+dot+tooltip; rollup badge icons; `data-ask` marker).
- `cmd/serf-hub/assets/notifications.js` (`STATE_COLORS` recolor; `DEFAULT_PREFS.loudScope`; `migratePrefs` version bump; `onAttentionChanged` gating).
- `cmd/serf-hub/assets/settings.js` (radio-commit handler for `loudScope`).
- `cmd/serf-hub/templates/partials/settings/notifications.html` (loud-scope control markup).
- `cmd/serf-hub/assets/renderer.js` (connection banner, subagent glyphs, plan-adjacent needs-you affordances, ask-question chip — icon swap).
- `cmd/serf-hub/assets/renderer-format.js` (plan/task glyphs — icon swap).
- `cmd/serf-hub/templates/app.html`, `cmd/serf-hub/templates/thread.html` (load `icons.js`).
- `cmd/serf-hub/jstest/test-renderer-connection-banner.js` (glyph assertion updated to expect an svg icon).
- `test/scenarios/ask-cross-session-notify.md` (HX-Request header + NeedsYou-count + client-rendered-sidebar fixes + loud-scope default assertion).
- `test/scenarios/ask-noninteractive-invisible.md`, `ask-subagent-invisible.md`, `ask-tui-answer.md`, and any idle-poll cards (hermetics batch, docs-only).

---

# Phase 1 — Rank & vocabulary consolidation (`hubapi` as shared source of truth)

## Task 1: `hubapi.AttentionRank` + `hubapi.RollupRank`

**Files:**
- Create: `hubapi/attention.go`
- Create: `hubapi/attention_test.go`

**Interfaces:**
- Produces: `hubapi.AttentionRank(state string) int`, `hubapi.RollupRank(state string) int` — used by Task 2 (hubcore) and Task 3 (TUI).

- [ ] **Step 1: Write the failing test**

Create `hubapi/attention_test.go`:

```go
package hubapi

import "testing"

func TestAttentionRank(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"errored", 5},
		{"awaiting", 4},
		{"active", 3},
		{"warning", 2},
		{"idle", 1},
		{"ended", 0},
		{"unknown", 0},
	}
	for _, c := range cases {
		if got := AttentionRank(c.in); got != c.want {
			t.Errorf("AttentionRank(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestRollupRank(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"errored", 5},
		{"awaiting", 4},
		{"warning", 3},
		{"active", 2},
		{"idle", 1},
		{"ended", 0},
		{"unknown", 0},
	}
	for _, c := range cases {
		if got := RollupRank(c.in); got != c.want {
			t.Errorf("RollupRank(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestAttentionRank_ErroredOutranksAwaiting(t *testing.T) {
	if AttentionRank("errored") <= AttentionRank("awaiting") {
		t.Fatal("errored must outrank awaiting")
	}
	if RollupRank("errored") <= RollupRank("awaiting") {
		t.Fatal("RollupRank: errored must outrank awaiting")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./hubapi/ -run 'TestAttentionRank|TestRollupRank' -v`
Expected: FAIL — `AttentionRank`/`RollupRank` undefined in package `hubapi`.

- [ ] **Step 3: Write the implementation**

Create `hubapi/attention.go`:

```go
// Package hubapi's attention.go is the single shared source of truth for
// attention-state ranking and display words, imported by both the hub
// (cmd/serf-hub/internal/hubcore, which cannot be imported directly by the
// TUI because it is an `internal` package scoped to cmd/serf-hub) and the
// TUI (cmd/serf-tui). Previously AttentionRank and rollupRank were
// duplicated in hubcore, and the TUI carried a third copy
// (attentionRankLabel) — this file is the one place that ordering logic
// lives now.
package hubapi

// AttentionRank maps a normalized state to a sort key for live-session
// ordering. Higher rank sorts first (most attention-needing first).
func AttentionRank(state string) int {
	switch state {
	case "errored":
		return 5
	case "awaiting":
		return 4
	case "active":
		return 3
	case "warning":
		return 2
	case "idle":
		return 1
	default: // "ended" and unknown
		return 0
	}
}

// RollupRank maps a normalized state to a sort key for a project's rollup
// dot, where a warning outranks a merely-active child (a stuck warning
// surfaces before routine activity). Deliberately different ordering from
// AttentionRank — kept in the same file so the two rank tables never drift
// apart without a reviewer noticing.
func RollupRank(state string) int {
	switch state {
	case "errored":
		return 5
	case "awaiting":
		return 4
	case "warning":
		return 3
	case "active":
		return 2
	case "idle":
		return 1
	default:
		return 0
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./hubapi/ -run 'TestAttentionRank|TestRollupRank' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add hubapi/attention.go hubapi/attention_test.go
git commit -m "feat(hubapi): shared AttentionRank/RollupRank source of truth"
```

---

## Task 2: hubcore adopts `hubapi.AttentionRank`/`RollupRank`, dropping its own copies

**Files:**
- Modify: `cmd/serf-hub/internal/hubcore/tree.go`
- Modify: `cmd/serf-hub/internal/hubcore/tree_test.go`

**Interfaces:**
- Consumes: `hubapi.AttentionRank(state string) int`, `hubapi.RollupRank(state string) int` (Task 1).

- [ ] **Step 1: Update the existing tests to call `hubapi` instead of the local functions**

In `cmd/serf-hub/internal/hubcore/tree_test.go`, add `"primeradiant.com/serf/hubapi"` to the imports, then replace the bodies of `TestAttentionRank`, `TestRollupRank`, and `TestAttentionRanks_Errored` so they call `hubapi.AttentionRank`/`hubapi.RollupRank` instead of the local, soon-to-be-removed functions:

```go
func TestAttentionRank(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"awaiting", 4}, {"active", 3}, {"warning", 2}, {"idle", 1}, {"ended", 0}, {"unknown", 0},
	}
	for _, c := range cases {
		if got := hubapi.AttentionRank(c.in); got != c.want {
			t.Errorf("hubapi.AttentionRank(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestRollupRank(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"awaiting", 4}, {"warning", 3}, {"active", 2}, {"idle", 1}, {"ended", 0}, {"unknown", 0},
	}
	for _, c := range cases {
		if got := hubapi.RollupRank(c.in); got != c.want {
			t.Errorf("hubapi.RollupRank(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestAttentionRanks_Errored(t *testing.T) {
	if hubapi.AttentionRank("errored") <= hubapi.AttentionRank("awaiting") {
		t.Fatal("errored must outrank awaiting")
	}
	if hubapi.RollupRank("errored") <= hubapi.RollupRank("awaiting") {
		t.Fatal("RollupRank: errored must outrank awaiting")
	}
}
```

- [ ] **Step 2: Run test to verify it fails to compile**

Run: `go test ./cmd/serf-hub/internal/hubcore/ -run 'TestAttentionRank|TestRollupRank' -v`
Expected: FAIL to compile — `tree.go` still defines local `AttentionRank`/`rollupRank`, so this is a harmless double-definition at this step only if names collided; since the test now qualifies every call with `hubapi.`, it actually compiles fine against the still-present local functions too. This step's real purpose is the next one: deleting the local functions and updating their two call sites, which is what makes the local names disappear for good — run the full package test now as a baseline:

Run: `go test ./cmd/serf-hub/internal/hubcore/ -v 2>&1 | tail -20`
Expected: PASS (baseline, before removing the local functions).

- [ ] **Step 3: Remove the local rank functions and their call sites**

In `cmd/serf-hub/internal/hubcore/tree.go`, add `"primeradiant.com/serf/hubapi"` to the imports, then delete the `AttentionRank`/`rollupRank` function definitions (currently at lines 148-191, immediately after the `TreeNode` struct):

```go
// (delete AttentionRank and rollupRank entirely)
```

Update the two call sites. The rollup-computation loop (around line 508-511):

```go
		for _, s := range sessions {
			if hubapi.RollupRank(s.State) > hubapi.RollupRank(rollup) {
				rollup = s.State
			}
```

The live-nodes sort (around line 642-647):

```go
	sort.SliceStable(liveNodes, func(i, j int) bool {
		ri, rj := hubapi.AttentionRank(liveNodes[i].State), hubapi.AttentionRank(liveNodes[j].State)
		if ri != rj {
			return ri > rj
		}
		return treeNodeLess(liveNodes[i], liveNodes[j], metaMap, liveMap)
	})
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/serf-hub/... -v 2>&1 | tail -40`
Expected: PASS — every hubcore and web test green, including `TestAttentionRank`, `TestRollupRank`, `TestAttentionRanks_Errored`, `TestNeedsYou_AdmitsErroredAndWarning_RanksErroredFirst`.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/internal/hubcore/tree.go cmd/serf-hub/internal/hubcore/tree_test.go
git commit -m "refactor(hub): delete duplicated rank functions, delegate to hubapi"
```

---

## Task 3: TUI adopts `hubapi.AttentionRank`

**Files:**
- Modify: `cmd/serf-tui/hub_dashboard_view.go`

**Interfaces:**
- Consumes: `hubapi.AttentionRank(state string) int` (Task 1), the existing local `stateLabel(state string) string` (hub_dashboard_view.go:556-578, unchanged by this task).
- Produces: `attentionRankLabel(state string) int` keeps its existing name/signature so `dashboardRowLess` (hub_dashboard.go:221-234) and `TestErroredLane_TUI` (hub_dashboard_view_test.go) need no changes.

- [ ] **Step 1: Run the existing TUI rank test as a baseline**

Run: `go test ./cmd/serf-tui/... -run TestErroredLane_TUI -v`
Expected: PASS (baseline before the refactor — this pins the observable behavior that must not change).

- [ ] **Step 2: Replace `attentionRankLabel`'s body with a delegation**

In `cmd/serf-tui/hub_dashboard_view.go`, add `"primeradiant.com/serf/hubapi"` to the imports, then replace the `attentionRankLabel` function (currently lines 597-612):

```go
// attentionRankLabel normalizes state via stateLabel, then delegates to the
// shared hubapi.AttentionRank table so the TUI and the hub can never drift
// on ordering (Track A rank consolidation).
func attentionRankLabel(state string) int {
	return hubapi.AttentionRank(stateLabel(state))
}
```

- [ ] **Step 3: Run tests to verify they pass**

Run: `go test ./cmd/serf-tui/... -v 2>&1 | tail -40`
Expected: PASS — `TestErroredLane_TUI` and every dashboard-ordering test unchanged in behavior.

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-tui/hub_dashboard_view.go
git commit -m "refactor(tui): delegate attentionRankLabel to hubapi.AttentionRank"
```

---

## Task 4: `hubapi.StateWord` — the shared display-word table

**Files:**
- Modify: `hubapi/attention.go`
- Modify: `hubapi/attention_test.go`

**Interfaces:**
- Produces: `hubapi.StateWord(state string, askPending bool) string` — returns `"Working"`/`"Warning"`/`"Error"`/`"Idle"`/`"Ended"`/`"Not loaded"` for their respective normalized states, and for `"awaiting"` returns `"Question waiting"` when `askPending` else `"Your move"`. Consumed by Task 10 (web `stateLabel`) and Task 17 (TUI `displayWord`).

- [ ] **Step 1: Write the failing test**

Append to `hubapi/attention_test.go`:

```go
func TestStateWord(t *testing.T) {
	cases := []struct {
		state      string
		askPending bool
		want       string
	}{
		{"active", false, "Working"},
		{"awaiting", true, "Question waiting"},
		{"awaiting", false, "Your move"},
		{"warning", false, "Warning"},
		{"warning", true, "Warning"}, // askPending is meaningless outside "awaiting"
		{"errored", false, "Error"},
		{"idle", false, "Idle"},
		{"ended", false, "Ended"},
		{"closed", false, "Ended"},
		{"notLoaded", false, "Not loaded"},
	}
	for _, c := range cases {
		if got := StateWord(c.state, c.askPending); got != c.want {
			t.Errorf("StateWord(%q, %v) = %q, want %q", c.state, c.askPending, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./hubapi/ -run TestStateWord -v`
Expected: FAIL — `StateWord` undefined.

- [ ] **Step 3: Write the implementation**

Append to `hubapi/attention.go`:

```go
// StateWord returns the unified display word for a normalized attention
// state — one word, shared verbatim by the web (cmd/serf-hub's stateLabel)
// and the TUI (displayWord) so the two surfaces can never independently
// drift on vocabulary (Track A §1). askPending selects between the two
// needs-you bands (Track A §2 ask-tiering) and is ignored for every other
// state.
func StateWord(state string, askPending bool) string {
	switch state {
	case "errored":
		return "Error"
	case "awaiting":
		if askPending {
			return "Question waiting"
		}
		return "Your move"
	case "active":
		return "Working"
	case "warning":
		return "Warning"
	case "idle":
		return "Idle"
	case "ended", "closed":
		return "Ended"
	case "notLoaded":
		return "Not loaded"
	default:
		return state
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./hubapi/ -run TestStateWord -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add hubapi/attention.go hubapi/attention_test.go
git commit -m "feat(hubapi): shared StateWord display vocabulary"
```

---

# Phase 2 — Web unified vocabulary & icons

## Task 5: Palette recolor in `style.css` — green Working, blue Needs-you, amber Warning stays, red Error, gray Idle/Ended

**Files:**
- Modify: `cmd/serf-hub/assets/style.css`

There are four theme blocks carrying the same six `--state-*`/`--error` tokens: `:root` (default/dark, lines 4-21), `@media (prefers-color-scheme: light)` (lines 123-143), `:root[data-theme="dark"]` (lines 154-181), `:root[data-theme="light"]` (lines 183-210). `--state-processing` (blue) renames to `--state-working` and becomes green; `--state-awaiting` (was amber) becomes blue, reusing the exact blue `--state-processing` vacates (no new color invented for that swap); `--state-warning` keeps its current amber (no collision remains once `--state-awaiting` isn't amber too); `--state-idle`, `--state-ended`, `--error`, `--state-subagent` are unchanged.

- [ ] **Step 1: Write the failing jstest**

Create `cmd/serf-hub/jstest/test-style-palette.js` (Node script reading `style.css` as text — follow the pattern of the sibling `test-context-pressure-css.js`):

```js
"use strict";
const fs = require("fs");
const path = require("path");
const assert = require("assert");

const css = fs.readFileSync(path.join(__dirname, "..", "assets", "style.css"), "utf8");

// The processing→working rename must leave zero references to the old name.
assert.ok(!/--state-processing/.test(css), "style.css must not reference --state-processing after the rename");
assert.ok(/--state-working:\s*#/.test(css), "style.css must define --state-working");

// Four theme blocks must all define --state-working and --state-awaiting.
const working = css.match(/--state-working:\s*(#[0-9a-fA-F]{6})/g) || [];
const awaiting = css.match(/--state-awaiting:\s*(#[0-9a-fA-F]{6})/g) || [];
assert.strictEqual(working.length, 4, "expected 4 --state-working definitions (default/light-media/dark-forced/light-forced), got " + working.length);
assert.strictEqual(awaiting.length, 4, "expected 4 --state-awaiting definitions, got " + awaiting.length);

console.log("test-style-palette.js: OK");
```

- [ ] **Step 2: Run test to verify it fails**

Run: `node cmd/serf-hub/jstest/test-style-palette.js`
Expected: FAIL (assertion) — `--state-processing` still present.

- [ ] **Step 3: Recolor all four theme blocks**

In `cmd/serf-hub/assets/style.css`, the default `:root` block (lines 4-21):

```css
  --state-awaiting: #7aa2f7;
  --state-working: #7dc98f;
  --state-warning: #e0af68;
  --state-idle: #7a7a86;
  --state-ended: #3a3a44;
```

(replacing the existing `--state-awaiting: #e2b06a;` / `--state-processing: #7aa2f7;` / `--state-warning: #e0af68;` / `--state-idle: #7a7a86;` / `--state-ended: #3a3a44;` lines; the comment above them at lines 13-16 should also update to describe the new mapping:)

```css
  /* Four meanings, each exactly one (design-system §3, palette v2):
     green=working, blue=needs-you, amber=warning, red=error, neutral=done/settled.
     --state-awaiting is BLUE (needs your eye, no longer amber — accepted churn),
     --state-working is GREEN, --state-warning keeps its own AMBER identity
     (no longer folded into needs-you), --state-idle is NEUTRAL. */
```

The light-media block (lines 123-143):

```css
    --state-awaiting: #2e58b8;
    --state-working: #2e7d4f;
    --state-warning: #8a5a14;
    --state-idle: #5e5e6a;
    --state-ended: #7a7a82;
```

The `:root[data-theme="dark"]` forced block (lines 154-181) — same values as the default block:

```css
  --state-awaiting: #7aa2f7;
  --state-working: #7dc98f;
  --state-warning: #e0af68;
  --state-idle: #7a7a86;
  --state-ended: #3a3a44;
```

The `:root[data-theme="light"]` forced block (lines 183-210) — same values as the light-media block:

```css
  --state-awaiting: #2e58b8;
  --state-working: #2e7d4f;
  --state-warning: #8a5a14;
  --state-idle: #5e5e6a;
  --state-ended: #7a7a82;
```

Update every remaining `var(--state-processing)` reference in the file to `var(--state-working)` (there are ~17 usage sites plus the `--diagnostic-hub: var(--state-processing);` alias at line 43 — becomes `--diagnostic-hub: var(--state-working);`). Search-and-replace `--state-processing` → `--state-working` across the whole file (both the definitions above and every `var(--state-processing)` call site); do not touch `--state-warning`/`--tool` (which correctly stays `var(--state-warning)`, unrelated to this rename).

- [ ] **Step 4: Run tests to verify they pass**

Run: `node cmd/serf-hub/jstest/test-style-palette.js && grep -c "state-processing" cmd/serf-hub/assets/style.css`
Expected: `test-style-palette.js: OK` and the grep count is `0`.

Run: `sh cmd/serf-hub/jstest/run-all.sh 2>&1 | tail -30`
Expected: PASS — no existing jstest (`test-context-pressure-css.js` etc.) asserted the literal old hex values, only that the rules/vars exist, so they stay green; if any test fails because it string-matched `--state-processing`, update that test's string to `--state-working` (grep the failure output for the exact assertion).

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-style-palette.js
git commit -m "feat(web): recolor state palette — working green, needs-you blue, processing rename"
```

---

## Task 6: Colorblind shape channel + glyph legend rewrite

**Files:**
- Modify: `cmd/serf-hub/assets/style.css`

The dot shape channel (currently at lines 1048-1071) pairs `awaiting`+`warning` on the same rotated-diamond shape because both were amber; now that `awaiting` (needs-you) is blue and `warning` keeps amber, give `warning` its own shape so the "un-folded" identity is visible in shape too, not just color.

- [ ] **Step 1: Write the failing jstest**

Create `cmd/serf-hub/jstest/test-style-colorblind-shapes.js`:

```js
"use strict";
const fs = require("fs");
const path = require("path");
const assert = require("assert");

const css = fs.readFileSync(path.join(__dirname, "..", "assets", "style.css"), "utf8");

// warning must no longer share the diamond transform with awaiting.
const awaitingBlock = css.match(/\.status-dot\[data-state="awaiting"\]\s*\{([^}]*)\}/);
const warningBlock = css.match(/\.status-dot\[data-state="warning"\]\s*\{([^}]*)\}/);
assert.ok(awaitingBlock && warningBlock, "expected both awaiting and warning status-dot shape rules");
assert.notStrictEqual(awaitingBlock[1].trim(), warningBlock[1].trim(), "warning must have its own distinct shape now that it is no longer amber-paired with awaiting");

console.log("test-style-colorblind-shapes.js: OK");
```

- [ ] **Step 2: Run test to verify it fails**

Run: `node cmd/serf-hub/jstest/test-style-colorblind-shapes.js`
Expected: FAIL — both rules are still identical `border-radius: 1px; transform: rotate(45deg);`.

- [ ] **Step 3: Give warning its own shape + rewrite the legend**

In `cmd/serf-hub/assets/style.css`, replace the colorblind-shapes block (currently lines 1048-1071):

```css
/* ============================================================
   Colorblind-safe status dots (mockup #10, palette v2)
   The session dot is shape-coded as well as color-coded so it
   survives colorblindness: working = green DISC, needs-you =
   blue DIAMOND, warning = amber SQUIRCLE (its own shape now that
   it is no longer amber-paired with needs-you), idle/ended =
   neutral SQUARE, errored = red TRIANGLE. Shape + color is the
   double channel; renderer.js's [data-pulse] still drives the
   live breathing on the same element.
   ============================================================ */
.status-dot[data-state="awaiting"] {
  border-radius: 1px;
  transform: rotate(45deg); /* diamond */
}
.status-dot[data-state="warning"] {
  border-radius: 30%; /* squircle — its own shape, distinct from the needs-you diamond */
  transform: none;
}
.status-dot[data-state="active"] { border-radius: var(--radius-pill); } /* disc */
.status-dot[data-state="idle"],
.status-dot[data-state="ended"] { border-radius: 1px; }                 /* square */
.status-dot[data-state="errored"] {
  border-radius: 0;
  clip-path: polygon(50% 0, 100% 100%, 0 100%); /* triangle — red alone must not be the only channel */
  transform: none;
}
```

Update the "Needs-you triage tier" comment block immediately below (currently referencing "Amber is reserved strictly for needs-you") and the glyph legend (currently lines 939-944):

```css
/* Icon pairing (mockup #1, palette v2): refresh-cw green working, speech-
   bubble blue needs-you (? question-waiting / ! your-move), triangle-alert
   amber warning, circle-x red error. Color is never the sole channel — the
   shape carries it too (see the colorblind-safe status-dots block below). */
.subagent-row[data-state="active"]   .subagent-glyph { color: var(--state-working); }
.subagent-row[data-state="awaiting"] .subagent-glyph { color: var(--state-awaiting); }
.subagent-row[data-state="warning"]  .subagent-glyph { color: var(--state-warning); }
.subagent-row[data-state="errored"]  .subagent-glyph { color: var(--error); }
```

(Note: `data-state="warning"` on `.subagent-glyph` previously colored via `var(--error)`, an existing inconsistency the legend's own comment flagged — Task 13 fixes this call site when it revisits subagent glyphs; this task only rewrites the comment and the two shape/color lines named above.)

Also update the "Needs-you triage tier" heading comment (formerly citing amber) to:

```css
.needs-you-header { color: var(--state-awaiting); }
.needs-you-header .needs-you-glyph { color: var(--state-awaiting); font-size: var(--text-xs); }
.needs-you-header .count { color: var(--state-awaiting); }
```

(values unchanged — `--state-awaiting` is now blue, so this block recolors automatically via the token; only the "Amber is reserved strictly for needs-you" prose comment above it needs correcting to "Blue is reserved strictly for needs-you".)

- [ ] **Step 4: Run tests to verify they pass**

Run: `node cmd/serf-hub/jstest/test-style-colorblind-shapes.js && sh cmd/serf-hub/jstest/run-all.sh 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-style-colorblind-shapes.js
git commit -m "feat(web): give warning its own colorblind-safe shape; rewrite glyph legend"
```

---

## Task 7: Vendor the Lucide icon set (`icons.js`)

**Files:**
- Create: `cmd/serf-hub/assets/icons.js`
- Create: `cmd/serf-hub/jstest/test-icons.js`

Seven icons, fetched verbatim (2026-07-05) from `https://raw.githubusercontent.com/lucide-icons/lucide/main/icons/<name>.svg` (ISC license): `refresh-cw` (Working), `message-circle-question-mark` (Question waiting), `message-circle-warning` (Your move), `triangle-alert` (Warning), `circle-x` (Error), `pause` (Idle), `check` (Ended). `width`/`height` are set to `1em` (not the fetched `24`) so each icon scales with the containing element's font-size via CSS, and `stroke="currentColor"` is preserved so color follows the element's CSS `color`.

**Interfaces:**
- Produces: `window.SerfIcons = { working, questionWaiting, yourMove, warning, error, idle, ended }`, each an SVG markup string, consumed by Task 8 (sidebar), Task 9 (rollup badge), Task 12-15 (renderer.js), Task 14 (renderer-format.js).

- [ ] **Step 1: Write the failing jstest**

Create `cmd/serf-hub/jstest/test-icons.js` (JSDOM harness — copy the bootstrap pattern from a sibling test like `test-sidebar-aria.js`):

```js
"use strict";
const { JSDOM } = require("jsdom");
const fs = require("fs");
const path = require("path");
const assert = require("assert");

const dom = new JSDOM("<!doctype html><html><body></body></html>", { runScripts: "outside-only" });
const script = fs.readFileSync(path.join(__dirname, "..", "assets", "icons.js"), "utf8");
dom.window.eval(script);

const EXPECTED_KEYS = ["working", "questionWaiting", "yourMove", "warning", "error", "idle", "ended"];
const icons = dom.window.SerfIcons;
assert.ok(icons, "window.SerfIcons must be defined");
for (const key of EXPECTED_KEYS) {
  const markup = icons[key];
  assert.ok(typeof markup === "string" && markup.length > 0, `SerfIcons.${key} must be a non-empty string`);
  const div = dom.window.document.createElement("div");
  div.innerHTML = markup;
  const svg = div.querySelector("svg");
  assert.ok(svg, `SerfIcons.${key} must parse to an <svg> element`);
  assert.strictEqual(svg.getAttribute("stroke"), "currentColor", `SerfIcons.${key} must use stroke="currentColor" to inherit color`);
}

console.log("test-icons.js: OK");
```

- [ ] **Step 2: Run test to verify it fails**

Run: `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-icons.js`
Expected: FAIL — `cmd/serf-hub/assets/icons.js` does not exist.

- [ ] **Step 3: Write the implementation**

Create `cmd/serf-hub/assets/icons.js`:

```js
// Vendored Lucide line icons (ISC license, github.com/lucide-icons/lucide),
// one per unified-vocabulary state (Track A §1). width/height="1em" scales
// with the containing element's font-size; stroke="currentColor" inherits
// the element's CSS color. Consumed by sidebar.js, renderer.js, and
// renderer-format.js wherever a text glyph (⟳ ◆ ✕) used to render.
(function () {
  "use strict";

  window.SerfIcons = {
    // refresh-cw — Working (green)
    working:
      '<svg xmlns="http://www.w3.org/2000/svg" width="1em" height="1em" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
      '<path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/>' +
      '<path d="M21 3v5h-5"/>' +
      '<path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/>' +
      '<path d="M8 16H3v5"/>' +
      "</svg>",
    // message-circle-question-mark — Needs you · Question waiting (blue)
    questionWaiting:
      '<svg xmlns="http://www.w3.org/2000/svg" width="1em" height="1em" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
      '<path d="M2.992 16.342a2 2 0 0 1 .094 1.167l-1.065 3.29a1 1 0 0 0 1.236 1.168l3.413-.998a2 2 0 0 1 1.099.092 10 10 0 1 0-4.777-4.719"/>' +
      '<path d="M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3"/>' +
      '<path d="M12 17h.01"/>' +
      "</svg>",
    // message-circle-warning — Needs you · Your move (blue)
    yourMove:
      '<svg xmlns="http://www.w3.org/2000/svg" width="1em" height="1em" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
      '<path d="M2.992 16.342a2 2 0 0 1 .094 1.167l-1.065 3.29a1 1 0 0 0 1.236 1.168l3.413-.998a2 2 0 0 1 1.099.092 10 10 0 1 0-4.777-4.719"/>' +
      '<path d="M12 8v4"/>' +
      '<path d="M12 16h.01"/>' +
      "</svg>",
    // triangle-alert — Warning (amber)
    warning:
      '<svg xmlns="http://www.w3.org/2000/svg" width="1em" height="1em" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
      '<path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3"/>' +
      '<path d="M12 9v4"/>' +
      '<path d="M12 17h.01"/>' +
      "</svg>",
    // circle-x — Error (red)
    error:
      '<svg xmlns="http://www.w3.org/2000/svg" width="1em" height="1em" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
      '<circle cx="12" cy="12" r="10"/>' +
      '<path d="m15 9-6 6"/>' +
      '<path d="m9 9 6 6"/>' +
      "</svg>",
    // pause — Idle (gray)
    idle:
      '<svg xmlns="http://www.w3.org/2000/svg" width="1em" height="1em" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
      '<rect x="14" y="3" width="5" height="18" rx="1"/>' +
      '<rect x="5" y="3" width="5" height="18" rx="1"/>' +
      "</svg>",
    // check — Ended (dim gray)
    ended:
      '<svg xmlns="http://www.w3.org/2000/svg" width="1em" height="1em" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
      '<path d="M20 6 9 17l-5-5"/>' +
      "</svg>",
  };
})();
```

Add `<script src="/assets/icons.js"></script>` to `cmd/serf-hub/templates/app.html` immediately before the existing `<script src="/assets/sidebar.js"></script>` tag (grep `sidebar.js` in `app.html` to find the exact line), and the same in `cmd/serf-hub/templates/thread.html` if it independently loads `renderer.js` (grep `renderer.js` there).

- [ ] **Step 4: Run test to verify it passes**

Run: `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-icons.js`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/icons.js cmd/serf-hub/jstest/test-icons.js cmd/serf-hub/templates/app.html cmd/serf-hub/templates/thread.html
git commit -m "feat(web): vendor Lucide icon set for the unified status vocabulary"
```

---

## Task 8: `sidebar.js` status-dot becomes icon + dot, word as hover tooltip

**Files:**
- Modify: `cmd/serf-hub/assets/sidebar.js`
- Create: `cmd/serf-hub/jstest/test-sidebar-icons.js`

**Interfaces:**
- Consumes: `window.SerfIcons` (Task 7), `hubapi.TreeNode.State` (already on the wire; the `ask_pending` field itself lands in Phase 4 Task 21 — this task wires the icon-choice function to accept an `askPending` parameter now so Phase 4 only needs to pass the real value, not restructure the call).

- [ ] **Step 1: Write the failing test**

Create `cmd/serf-hub/jstest/test-sidebar-icons.js` (copy the JSDOM + sidebar-model bootstrap from `test-sidebar-reconcile.js`):

```js
"use strict";
require("./load-renderer.js"); // or the sidebar-specific loader the sibling tests use
const assert = require("assert");

// buildRow must render an icon (svg) inside .status-dot's container and set
// a title attribute carrying the unified word as the hover tooltip.
const node = { row_id: "project:x:local:01A", ref: "local:01A", state: "active", title: "t", session_id: "01A" };
const row = window.SerfSidebarInternal.buildRow(node); // exposed for tests, see step 3
const iconWrap = row.querySelector(".status-icon");
assert.ok(iconWrap, "row must render a .status-icon element");
assert.ok(iconWrap.querySelector("svg"), "status-icon must contain an svg icon");
assert.strictEqual(iconWrap.getAttribute("title"), "Working", "status-icon title must be the unified word (hover tooltip)");

const askNode = Object.assign({}, node, { state: "awaiting", ask_pending: true });
const askRow = window.SerfSidebarInternal.buildRow(askNode);
assert.strictEqual(askRow.querySelector(".status-icon").getAttribute("title"), "Question waiting");

const moveNode = Object.assign({}, node, { state: "awaiting", ask_pending: false });
const moveRow = window.SerfSidebarInternal.buildRow(moveNode);
assert.strictEqual(moveRow.querySelector(".status-icon").getAttribute("title"), "Your move");

console.log("test-sidebar-icons.js: OK");
```

- [ ] **Step 2: Run test to verify it fails**

Run: `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-sidebar-icons.js`
Expected: FAIL — no `.status-icon` element, `window.SerfSidebarInternal` undefined.

- [ ] **Step 3: Add the icon element + tooltip word to `buildRow`/`patchRow`**

In `cmd/serf-hub/assets/sidebar.js`, add a small word/icon-key resolver near the top (after `function rowKey(n) { ... }`, line 24):

```js
  // stateIconKey maps a tree-node state (+ optional ask_pending) to the
  // SerfIcons key and the hubapi.StateWord-equivalent tooltip text. Mirrors
  // hubapi.StateWord verbatim so the web tooltip and the TUI word agree.
  var STATE_WORDS = {
    active: "Working", warning: "Warning", errored: "Error",
    idle: "Idle", ended: "Ended", closed: "Ended", notLoaded: "Not loaded",
  };
  function stateIconKey(state, askPending) {
    if (state === "awaiting") return askPending ? "questionWaiting" : "yourMove";
    if (state === "active") return "working";
    if (state === "warning") return "warning";
    if (state === "errored") return "error";
    if (state === "idle") return "idle";
    return "ended";
  }
  function stateWord(state, askPending) {
    if (state === "awaiting") return askPending ? "Question waiting" : "Your move";
    return STATE_WORDS[state] || state;
  }
```

Replace `buildRow`'s dot markup (currently the `a.innerHTML = '<div class="dot-col">...` line):

```js
    a.innerHTML =
      '<div class="dot-col"><span class="status-dot" data-state="' + n.state + '"></span>' +
      '<span class="status-icon" data-state="' + n.state + '"></span></div>' +
      '<div class="text-col"><div class="title"></div><div class="meta"></div></div>';
    var icon = a.querySelector(".status-icon");
    icon.innerHTML = window.SerfIcons[stateIconKey(n.state, n.ask_pending)];
    icon.setAttribute("title", stateWord(n.state, n.ask_pending));
```

(inserted immediately after the existing `a.querySelector(".title").textContent = n.title;` line so `icon` is in scope alongside the other row-building statements). Update `patchRow` to keep the icon in sync when state or ask_pending changes:

```js
  function patchRow(a, n) {
    if (a.getAttribute("data-state") !== n.state) {
      a.setAttribute("data-state", n.state);
      var dot = a.querySelector(".status-dot");
      if (dot) dot.setAttribute("data-state", n.state);
    }
    var icon = a.querySelector(".status-icon");
    if (icon) {
      var key = stateIconKey(n.state, n.ask_pending);
      var word = stateWord(n.state, n.ask_pending);
      if (icon.getAttribute("title") !== word) {
        icon.setAttribute("data-state", n.state);
        icon.innerHTML = window.SerfIcons[key];
        icon.setAttribute("title", word);
      }
    }
    var title = a.querySelector(".title");
    if (title && title.textContent !== n.title) title.textContent = n.title;
    if (n.favorite) a.setAttribute("data-favorite", ""); else a.removeAttribute("data-favorite");
    patchChildrenToggle(a, n);
  }
```

Expose the internals the test needs, near the top where `window.SerfSidebarModel = model;` already exists:

```js
  window.SerfSidebarInternal = { buildRow: buildRow, stateIconKey: stateIconKey, stateWord: stateWord }; // test/inspection surface
```

Add the `.status-icon` sizing rule to `cmd/serf-hub/assets/style.css` next to the existing `.status-dot` rule (line 637):

```css
.status-icon { display: inline-flex; width: 12px; height: 12px; font-size: 12px; color: var(--state-ended); flex-shrink: 0; }
.status-icon[data-state="active"] { color: var(--state-working); }
.status-icon[data-state="awaiting"] { color: var(--state-awaiting); }
.status-icon[data-state="warning"] { color: var(--state-warning); }
.status-icon[data-state="idle"] { color: var(--state-idle); }
.status-icon[data-state="errored"] { color: var(--error); }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-sidebar-icons.js && sh cmd/serf-hub/jstest/run-all.sh 2>&1 | tail -30`
Expected: PASS — including every pre-existing `test-sidebar-*.js` (row structure gained a sibling element, which reconciliation-keyed tests should tolerate; if a pre-existing test snapshot-matches `a.innerHTML` exactly, update its expected markup to include the new `.status-icon` span).

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/sidebar.js cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-sidebar-icons.js
git commit -m "feat(web): sidebar status-dot renders icon + word tooltip"
```

---

## Task 9: `sidebar.js` rollup badge (`⟳N · ◆M`) adopts the icon set

**Files:**
- Modify: `cmd/serf-hub/assets/sidebar.js`

**Interfaces:**
- Consumes: `window.SerfIcons.working`, `window.SerfIcons.yourMove` (Task 7).

- [ ] **Step 1: Write the failing test**

Append to `cmd/serf-hub/jstest/test-sidebar-icons.js`:

```js
const badge = window.SerfSidebarInternal.buildRollupBadge("rollup-live", "working", 3);
assert.ok(badge.querySelector("svg"), "rollup badge must render an svg icon, not a text glyph");
assert.strictEqual(badge.textContent.trim(), "3", "rollup badge count text must still read as a plain number");
```

- [ ] **Step 2: Run test to verify it fails**

Run: `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-sidebar-icons.js`
Expected: FAIL — `buildRollupBadge` not exposed / still sets `textContent` to a glyph character, no `<svg>`.

- [ ] **Step 3: Implement**

In `cmd/serf-hub/assets/sidebar.js`, replace `setProjectRollup`/`buildRollupBadge` (currently lines 482-507):

```js
  function setProjectRollup(el, p) {
    var r = el.querySelector(".project-rollup");
    if (!r) return;
    r.setAttribute("data-state", p.rollup_state || "");
    r.textContent = ""; // clear prior badges/separator (not innerHTML)
    var live = p.rollup_live || 0;
    var attn = p.rollup_attn || 0;
    if (live > 0) r.appendChild(buildRollupBadge("rollup-live", "working", live));
    if (live > 0 && attn > 0) {
      var sep = document.createElement("span");
      sep.className = "rollup-sep";
      sep.textContent = "·";
      r.appendChild(sep);
    }
    if (attn > 0) r.appendChild(buildRollupBadge("rollup-attn", "yourMove", attn));
  }
  function buildRollupBadge(cls, iconKey, count) {
    var b = document.createElement("span");
    b.className = "rollup-badge " + cls;
    var g = document.createElement("span");
    g.className = "rollup-glyph";
    g.innerHTML = window.SerfIcons[iconKey];
    b.appendChild(g);
    b.appendChild(document.createTextNode(String(count)));
    return b;
  }
```

Add `buildRollupBadge` to the `window.SerfSidebarInternal` export from Task 8:

```js
  window.SerfSidebarInternal = { buildRow: buildRow, stateIconKey: stateIconKey, stateWord: stateWord, buildRollupBadge: buildRollupBadge };
```

Update `.rollup-glyph`'s CSS (style.css, currently `font-family: var(--font-mono); font-size: 9px;` on the rule at line 1110) to size the inline svg instead:

```css
.rollup-badge .rollup-glyph { display: inline-flex; width: 9px; height: 9px; font-size: 9px; }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-sidebar-icons.js && sh cmd/serf-hub/jstest/run-all.sh 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/sidebar.js cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-sidebar-icons.js
git commit -m "feat(web): rollup badge adopts icon set instead of text glyphs"
```

---

## Task 10: `web_format.go` `stateLabel` delegates to `hubapi.StateWord`

**Files:**
- Modify: `cmd/serf-hub/web_format.go`
- Modify: `cmd/serf-hub/web_format_test.go`
- Modify: every call site of `stateLabel` in `cmd/serf-hub` (grep `stateLabel(` — signature gains an `askPending bool` parameter)

**Interfaces:**
- Consumes: `hubapi.StateWord(state string, askPending bool) string` (Task 4).
- Produces: `stateLabel(state string, askPending bool) string` — callers without ask-pending information (most, until Phase 4) pass `false`.

- [ ] **Step 1: Write the failing test**

Append to `cmd/serf-hub/web_format_test.go`:

```go
func TestStateLabel_UnifiedVocabulary(t *testing.T) {
	cases := []struct {
		state      string
		askPending bool
		want       string
	}{
		{"active", false, "Working"},
		{"awaiting", false, "Your move"},
		{"awaiting", true, "Question waiting"},
		{"warning", false, "Warning"},
		{"errored", false, "Error"},
		{"idle", false, "Idle"},
	}
	for _, c := range cases {
		if got := stateLabel(c.state, c.askPending); got != c.want {
			t.Errorf("stateLabel(%q, %v) = %q, want %q", c.state, c.askPending, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-hub/ -run TestStateLabel_UnifiedVocabulary -v`
Expected: FAIL to compile — `stateLabel` takes one argument today.

- [ ] **Step 3: Implement + fix call sites**

In `cmd/serf-hub/web_format.go`, replace `stateLabel` (currently lines 191-210):

```go
// stateLabel returns the unified display word (Track A §1) for a normalized
// state. Delegates to hubapi.StateWord so the web and the TUI can never
// independently drift on vocabulary. askPending selects the needs-you band
// (Track A §2); pass false where the caller has no ask-pending information.
func stateLabel(state string, askPending bool) string {
	return hubapi.StateWord(state, askPending)
}
```

Add `"primeradiant.com/serf/hubapi"` to the file's imports. Run `grep -rn "stateLabel(" cmd/serf-hub/*.go` (excluding `_test.go`) to find every call site outside this file; update each to pass `false` for now (e.g. `stateLabel(state)` → `stateLabel(state, false)`) — Phase 4 Task 26's TUI-analogue threads the real bit through once `ask_pending` exists on the relevant response type; this task keeps every call site compiling and behaviorally identical for non-awaiting states (only `"awaiting"` differs, and it now reads "Your move" instead of the old "Needs you" — an intentional, spec-mandated wording change, not a regression).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/serf-hub/... -v 2>&1 | tail -40`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/web_format.go cmd/serf-hub/web_format_test.go
git commit -m "refactor(web): stateLabel delegates to hubapi.StateWord"
```

(Grep the full call-site diff and `git add` any additional files the grep in Step 3 turned up before committing.)

---

## Task 11: `notifications.js` `STATE_COLORS` recolor (favicon/dot)

**Files:**
- Modify: `cmd/serf-hub/assets/notifications.js`

- [ ] **Step 1: Write the failing jstest**

Create `cmd/serf-hub/jstest/test-notifications-palette.js`:

```js
"use strict";
const fs = require("fs");
const path = require("path");
const assert = require("assert");

const src = fs.readFileSync(path.join(__dirname, "..", "assets", "notifications.js"), "utf8");
const match = src.match(/const STATE_COLORS = \{([^}]*)\}/);
assert.ok(match, "STATE_COLORS block must exist");
assert.ok(/working:\s*"#7dc98f"/.test(match[1]), "working must be the new green, matching --state-working");
assert.ok(/needs_you:\s*"#7aa2f7"/.test(match[1]), "needs_you must be the new blue, matching --state-awaiting");
assert.ok(/error:\s*"#f7768e"/.test(match[1]), "error stays unchanged");

console.log("test-notifications-palette.js: OK");
```

- [ ] **Step 2: Run test to verify it fails**

Run: `node cmd/serf-hub/jstest/test-notifications-palette.js`
Expected: FAIL — `working: "#7aa2f7"` (old blue), `needs_you: "#e0af68"` (old amber).

- [ ] **Step 3: Implement**

In `cmd/serf-hub/assets/notifications.js`, replace `STATE_COLORS` (currently lines 32-36):

```js
  // Dot color by attention level, mirroring style.css's --state-working /
  // --state-awaiting (dark-theme values — the favicon/badge always renders
  // against the dark default regardless of the page's active theme).
  // No "idle" entry: idle never sets a dot.
  const STATE_COLORS = {
    error: "#f7768e",
    needs_you: "#7aa2f7",
    working: "#7dc98f",
  };
```

- [ ] **Step 4: Run test to verify it passes**

Run: `node cmd/serf-hub/jstest/test-notifications-palette.js && sh cmd/serf-hub/jstest/run-all.sh 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/notifications.js cmd/serf-hub/jstest/test-notifications-palette.js
git commit -m "feat(web): recolor notification favicon/badge dots to the new palette"
```

---

## Task 12: `renderer.js` connection banner icon swap

**Files:**
- Modify: `cmd/serf-hub/assets/renderer.js`

- [ ] **Step 1: Write the failing jstest**

Check the existing `cmd/serf-hub/jstest/test-renderer-connection-banner.js` (it currently asserts the banner text matches `/[⟳↻⤬✕⚠◐]/`, per the "glyph-paired (colorblind-safe)" comment at its line ~72). Update that assertion to expect an `<svg>` element instead of a Unicode glyph character:

```js
// (edit the existing assertion block, around line 70-75, from a regex glyph
// match to an svg-element check)
const glyphEl = banner.querySelector(".connection-banner-glyph");
assert.ok(glyphEl.querySelector("svg"), "reconnecting banner must render the working icon, not a text glyph");
```

- [ ] **Step 2: Run test to verify it fails**

Run: `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-renderer-connection-banner.js`
Expected: FAIL — the banner glyph span still has `textContent = "⟳"`, no child `<svg>`.

- [ ] **Step 3: Implement**

In `cmd/serf-hub/assets/renderer.js`, replace `showConnectionBanner` (currently lines 809-823):

```js
    showConnectionBanner(level) {
      const banner = this.connectionBannerEl();
      banner.classList.remove("reconnecting", "lost");
      if (level === "lost") {
        banner.classList.add("lost");
        banner.innerHTML = '<span class="connection-banner-glyph" aria-hidden="true">' + window.SerfIcons.warning + '</span>' +
          '<span class="connection-banner-msg">Connection lost</span>' +
          '<span class="connection-banner-sub">retrying… — the agent keeps running on the daemon</span>';
      } else {
        banner.classList.add("reconnecting");
        banner.innerHTML = '<span class="connection-banner-glyph" aria-hidden="true">' + window.SerfIcons.working + '</span>' +
          '<span class="connection-banner-msg">Reconnecting…</span>' +
          '<span class="connection-banner-sub">the agent keeps running on the daemon</span>';
      }
    },
```

(The "lost" banner previously used a bare `⚠` in its regex-matched set but no code reference was found emitting it distinctly from the reconnecting `⟳` — re-check at implementation time whether "lost" already had its own glyph line; if the current code shares one glyph span pattern across both branches as shown by the sub-agent research, this diff is exact. `warning`'s triangle-alert icon is the right semantic pairing for "lost" — connection loss is a warning-severity event, not a working/reconnecting one.)

Add sizing CSS next to `.connection-banner-glyph`'s existing rule in `style.css` (grep `connection-banner-glyph` for the exact selector) so the inline svg matches the old glyph's footprint:

```css
.connection-banner-glyph svg { width: 1.1em; height: 1.1em; vertical-align: -0.15em; }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-renderer-connection-banner.js && sh cmd/serf-hub/jstest/run-all.sh 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/renderer.js cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-renderer-connection-banner.js
git commit -m "feat(web): connection banner adopts icon set"
```

---

## Task 13: `renderer.js` subagent glyphs + tally adopt the icon set

**Files:**
- Modify: `cmd/serf-hub/assets/renderer.js`

**Interfaces:**
- Consumes: `window.SerfIcons.working`, `.error`, `.ended` (Task 7). Note: `subagentGlyph("unknown")` returns `"?"` today — there is no unified-vocabulary state named "unknown" (subagent tool-status kinds are `running`/`done`/`failed`/`unknown`, a different axis than session attention state); this task maps `done`→`ended` icon, `failed`→`error` icon, `running`→`working` icon, and leaves `unknown` as a literal `"?"` character (out of scope — it is not one of the five/six unified states).

- [ ] **Step 1: Write the failing test**

Create `cmd/serf-hub/jstest/test-renderer-subagent-glyphs.js` (copy the JSDOM + `SerfRendererInternal` bootstrap from a sibling renderer test):

```js
"use strict";
require("./load-renderer.js");
const assert = require("assert");

const r = window.SerfRendererInternal;
assert.ok(r.subagentGlyph("done").includes("<svg"), "done glyph must be an svg icon");
assert.ok(r.subagentGlyph("failed").includes("<svg"), "failed glyph must be an svg icon");
assert.ok(r.subagentGlyph("running").includes("<svg"), "running glyph must be an svg icon");
assert.strictEqual(r.subagentGlyph("unknown"), "?", "unknown stays a literal ? — not a unified-vocabulary state");

console.log("test-renderer-subagent-glyphs.js: OK");
```

- [ ] **Step 2: Run test to verify it fails**

Run: `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-renderer-subagent-glyphs.js`
Expected: FAIL — `subagentGlyph("done")` returns `"✓"`, not svg markup; `SerfRendererInternal` may not yet expose `subagentGlyph` (if not already exposed, add it to whatever internal-test-surface object the sibling tests use).

- [ ] **Step 3: Implement**

In `cmd/serf-hub/assets/renderer.js`, replace `subagentGlyph` (currently lines 2546-2551):

```js
    subagentGlyph(kind) {
      if (kind === "done") return window.SerfIcons.ended;
      if (kind === "failed") return window.SerfIcons.error;
      if (kind === "unknown") return "?";
      return window.SerfIcons.working;
    },
```

`subagentGlyphClass` (lines 2557-2562) is unchanged — it still returns CSS class suffixes (`done`/`err`/`unk`/`run`), only the glyph *content* changed. Update every call site that assigns `subagentGlyph(...)`'s return value via `textContent` to use `innerHTML` instead (the value is now markup, not a character) — grep `subagentGlyph(` for call sites (`makeSubagentRow`'s initial placeholder at line 2619 currently sets `glyph.textContent = "⟳";` directly rather than calling the function; change it to `glyph.innerHTML = window.SerfIcons.working;`), plus any `refreshSubagentModule`/row-update function that later calls `subagentGlyph(kind)` and assigns the result — change `.textContent =` to `.innerHTML =` at each.

Update the subagent tally block (currently lines 3168-3171):

```js
        if (failed) parts.push(["f", window.SerfIcons.error + " " + failed + " failed"]);
        if (running) parts.push(["r", window.SerfIcons.working + " " + running + " running"]);
        if (done) parts.push(["o", window.SerfIcons.ended + " " + done + " done"]);
```

Check how `parts` is later joined into DOM (grep the variable a few lines below its `push` calls) — if it is assigned via `.textContent`, change the assignment to build DOM nodes (`.innerHTML =` the joined string, since each part is `[cssHook, htmlString]`) so the icon markup renders instead of appearing as literal `<svg>` text.

- [ ] **Step 4: Run tests to verify they pass**

Run: `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-renderer-subagent-glyphs.js && sh cmd/serf-hub/jstest/run-all.sh 2>&1 | tail -30`
Expected: PASS — fix any sibling test that string-matched the old `"✓"`/`"✕"`/`"⟳"` glyph characters by updating its assertion to check for `<svg` presence instead (grep the failure output for the exact assertion to update).

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/renderer.js cmd/serf-hub/jstest/test-renderer-subagent-glyphs.js
git commit -m "feat(web): subagent glyphs + tally adopt icon set"
```

---

## Task 14: `renderer-format.js` plan/task glyphs adopt the icon set

**Files:**
- Modify: `cmd/serf-hub/assets/renderer-format.js`

**Interfaces:**
- Consumes: `window.SerfIcons.ended`, `.working`, `.error` (Task 7).

- [ ] **Step 1: Write the failing test**

Create `cmd/serf-hub/jstest/test-renderer-format-plan-glyphs.js`:

```js
"use strict";
require("./load-renderer.js");
const assert = require("assert");

const r = window.SerfRendererFormatInternal || window.SerfRendererInternal;
assert.ok(r.planGlyphForStatus("done").includes("<svg"), "done plan glyph must be an svg icon");
assert.ok(r.planGlyphForStatus("in_progress").includes("<svg"), "in_progress plan glyph must be an svg icon");
assert.ok(r.planGlyphForStatus("cancelled").includes("<svg"), "cancelled plan glyph must be an svg icon");
assert.strictEqual(r.planGlyphForStatus("pending"), "○", "pending (default) stays a neutral literal circle — not a unified-vocabulary state");

console.log("test-renderer-format-plan-glyphs.js: OK");
```

(If `renderer-format.js` does not already expose an internal test surface, add the minimal one following whatever pattern the sibling `renderer-format` tests use — check `test-renderer-format-*.js` for the exact accessor name before assuming `SerfRendererFormatInternal`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-renderer-format-plan-glyphs.js`
Expected: FAIL — `planGlyphForStatus("done")` returns `"✓"`.

- [ ] **Step 3: Implement**

In `cmd/serf-hub/assets/renderer-format.js`, replace `planGlyphForStatus` (currently lines 486-493):

```js
  // planGlyphForStatus returns the glyph-paired status marker for a plan
  // item. cancelled maps to the error icon (a plan item that will not
  // happen reads the same as a failure, distinct from the neutral pending
  // circle); "pending" is not a unified-vocabulary state and keeps its
  // neutral literal circle.
  function planGlyphForStatus(status) {
    switch (status) {
      case "done": return window.SerfIcons.ended;
      case "in_progress": return window.SerfIcons.working;
      case "cancelled": return window.SerfIcons.error;
      default: return "○";
    }
  }
```

Update every call site assigning the return value via `.textContent` to `.innerHTML` instead (grep `planGlyphForStatus(` for callers).

- [ ] **Step 4: Run tests to verify they pass**

Run: `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-renderer-format-plan-glyphs.js && sh cmd/serf-hub/jstest/run-all.sh 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/renderer-format.js cmd/serf-hub/jstest/test-renderer-format-plan-glyphs.js
git commit -m "feat(web): plan/task glyphs adopt icon set"
```

---

## Task 15: `renderer.js` needs-you affordances + ask-question chip adopt the blue bubble icons

**Files:**
- Modify: `cmd/serf-hub/assets/renderer.js`

**Interfaces:**
- Consumes: `window.SerfIcons.yourMove`, `.error`, `.questionWaiting` (Task 7).

- [ ] **Step 1: Write the failing test**

Create `cmd/serf-hub/jstest/test-renderer-needsyou-affordances.js` (drive the four affordance builders the way the sibling renderer tests drive DOM-building functions — check `test-renderer-liveness.js` for the harness pattern):

```js
"use strict";
require("./load-renderer.js");
const assert = require("assert");

const r = window.SerfRendererInternal;
// The scroll-nudge pill (formerly "↓ ◆ needs you")
const pill = r.buildScrollNudgePill({ dir: "down", level: "needs_you" });
assert.ok(pill.innerHTML.includes("<svg"), "needs-you scroll pill must render an icon, not ◆");
// The off-screen dock hint (formerly "◆ The agent is waiting on your answer…")
const dock = r.buildNeedsYouDock();
assert.ok(dock.innerHTML.includes("<svg"), "needs-you dock must render an icon, not ◆");
// The Esc-collapsed ask chip (formerly "◆ question waiting")
const chip = r.buildAskCollapsedChip();
assert.ok(chip.innerHTML.includes("<svg"), "ask-collapsed chip must render the question-waiting icon, not ◆");
// The settled-ask summary line (formerly "◆ asked …")
const line = r.buildAskSettledLine("what should I do?", "do X");
assert.ok(line.innerHTML.includes("<svg"), "ask-settled line must render an icon, not ◆");

console.log("test-renderer-needsyou-affordances.js: OK");
```

(Function names above are illustrative — at implementation time, open `renderer.js` at the four cited line numbers, find the actual enclosing function names, and name the test calls to match exactly; do not invent function names that don't exist.)

- [ ] **Step 2: Run test to verify it fails**

Run: `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-renderer-needsyou-affordances.js`
Expected: FAIL — each site still sets `.textContent` to a string containing `"◆"`.

- [ ] **Step 3: Implement**

In `cmd/serf-hub/assets/renderer.js`, the four sites (verify current line numbers before editing — the design doc's citations were confirmed exact at 4213/4321/4451/4787 as of branch base `6647b744`, re-check for drift from this track's own earlier commits in this same plan):

Line ~4213 (scroll-nudge pill, `pill.textContent = "↓ ◆ needs you";`):

```js
        pill.innerHTML = (urgent.dir === "up" ? "↑ " : "↓ ") + window.SerfIcons.yourMove + " needs you";
```

(the urgent-error branch a few lines above, `pill.textContent = (urgent.dir === "up" ? "↑" : "↓") + " ✕ error";`, becomes:)

```js
        pill.innerHTML = (urgent.dir === "up" ? "↑ " : "↓ ") + window.SerfIcons.error + " error";
```

Line ~4321 (off-screen dock, `dock.textContent = "◆ The agent is waiting on your answer — jump to it";`):

```js
      dock.innerHTML = window.SerfIcons.questionWaiting + " The agent is waiting on your answer — jump to it";
```

Line ~4451 (Esc-collapsed ask chip, `chip.textContent = "◆ question waiting";`):

```js
      chip.innerHTML = window.SerfIcons.questionWaiting + " question waiting";
```

Line ~4787 (settled-ask summary, `line.textContent = "◆ asked " + askedSummary + (echo ? " — answered: " + echo : " — answered");`):

```js
      line.innerHTML = window.SerfIcons.questionWaiting + " asked " + askedSummary + (echo ? " — answered: " + echo : " — answered");
```

Also update the ask-header glyph near line 2011 (`glyph.textContent = "◆";`, the mockup #16 header comment at line 1997):

```js
      glyph.innerHTML = window.SerfIcons.questionWaiting;
```

Every string concatenated with `askedSummary`/user-provided text **must** go through `.innerHTML` carefully — confirm at implementation time that `askedSummary`/`echo` are already HTML-escaped upstream (check how the pre-existing `.textContent` assignment sourced them); if they are raw user text, build the icon and text as separate DOM nodes instead of string-concatenating into `innerHTML` to avoid an XSS regression (e.g. `line.textContent = ""; line.appendChild(iconSpan); line.appendChild(document.createTextNode(" asked " + askedSummary + ...));` where `iconSpan.innerHTML = window.SerfIcons.questionWaiting`).

- [ ] **Step 4: Run tests to verify they pass**

Run: `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-renderer-needsyou-affordances.js && sh cmd/serf-hub/jstest/run-all.sh 2>&1 | tail -30`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/renderer.js cmd/serf-hub/jstest/test-renderer-needsyou-affordances.js
git commit -m "feat(web): needs-you affordances + ask chip adopt the blue bubble icons"
```

---

# Phase 3 — TUI unified vocabulary

## Task 16: `tuitheme` token rename + recolor (`StateProcessing`→`StateWorking`, green/blue/gray reassignment)

**Files:**
- Modify: `cmd/serf-tui/internal/tuitheme/tokens.go`
- Modify: `cmd/serf-tui/internal/tuitheme/tokens_test.go`
- Modify: `cmd/serf-tui/internal/tuitheme/styles.go` (rename call site)
- Modify: `cmd/serf-tui/hub_dashboard_view.go` (rename call site, `stateColor` line ~308)
- Modify: `cmd/serf-tui/composer_render.go` (rename call site, line ~74)
- Modify: `cmd/serf-tui/internal/msgrender/message.go` (rename call sites, lines ~200, ~361)
- Modify: `cmd/serf-tui/internal/msgrender/tool_bodies.go` (rename call site, line ~182)

The TUI's existing palette is a distinct "calm terracotta/slate/gold/sage" design language (`tokens.go`'s own comment), not the web's blue/amber/red scheme — so this is not a literal hex copy from `style.css`. It reassigns the SAME five semantic hues (green=working, blue=needs-you, amber=warning, red=error, gray=idle) onto the TUI's existing muted tones, reusing the TUI's own vacated values wherever possible: the old `StateProcessing` slate-blue becomes `StateAwaiting`'s new color (needs-you moves onto it, exactly mirroring the web's swap), and `StateIdle`'s old sage-green becomes `StateWorking`'s color (idle moves off green, working moves onto it) — zero new hex values needed for those two. `StateIdle` itself gets a new neutral gray (the one genuinely new value this task introduces, flagged as a first-cut for Jesse to tune). `StateWarning`, `StateEnded`, `StateError`, `StateSubagent` are unchanged.

- [ ] **Step 1: Update the existing token test**

Read `cmd/serf-tui/internal/tuitheme/tokens_test.go` around its current lines 125, 132 (per prior research, referencing `StateProcessing`) and update every reference from `StateProcessing`/`StateProcessingTint` to `StateWorking`/`StateWorkingTint`. If the test asserts specific hex values for `StateProcessing`/`StateAwaiting`/`StateIdle`, update the expected values to match Step 3 below.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-tui/internal/tuitheme/... -v`
Expected: FAIL to compile — `StateWorking`/`StateWorkingTint` undefined until Step 3.

- [ ] **Step 3: Rename + recolor in `tokens.go`**

In `cmd/serf-tui/internal/tuitheme/tokens.go`, rename the struct fields (currently lines 23-29):

```go
	Accent, AccentSecondary lipgloss.Color
	StateAwaiting, StateWorking,
	StateWarning, StateIdle, StateEnded,
	StateSubagent, StateError lipgloss.Color
	BtnPrimaryText lipgloss.Color

	StateAwaitingTint, StateWorkingTint,
	StateWarningTint, StateIdleTint,
	AccentTint lipgloss.Color
```

`darkTheme` (currently lines 117-129):

```go
	// Calm state palette (palette v2): needs-you moves onto the old
	// processing slate-blue; working moves onto the old idle sage-green;
	// idle gets a fresh neutral gray. Warning/ended/error/subagent unchanged.
	StateAwaiting:   lipgloss.Color("#6b9ec8"),
	StateWorking:    lipgloss.Color("#88a878"),
	StateWarning:    lipgloss.Color("#c4a06a"),
	StateIdle:       lipgloss.Color("#767c82"),
	StateEnded:      lipgloss.Color("#5e5e64"),
	StateSubagent:   lipgloss.Color("#a8927a"),
	StateError:      lipgloss.Color("#d16969"),
	BtnPrimaryText:  lipgloss.Color("#0f0f11"),
	// Tints — faint elevated darks, used as backgrounds on tinted rows.
	StateAwaitingTint:   lipgloss.Color("#13181f"),
	StateWorkingTint:    lipgloss.Color("#161c14"),
	StateWarningTint:    lipgloss.Color("#1d1a14"),
	StateIdleTint:       lipgloss.Color("#1a1c1e"),
	AccentTint:          lipgloss.Color("#13181f"),
```

`lightTheme` (currently lines 160-172):

```go
	StateAwaiting:   lipgloss.Color("#3d6790"),
	StateWorking:    lipgloss.Color("#4a6a35"),
	StateWarning:    lipgloss.Color("#8a6420"),
	StateIdle:       lipgloss.Color("#8a8f94"),
	StateEnded:      lipgloss.Color("#76746e"),
	StateSubagent:   lipgloss.Color("#7a6850"),
	StateError:      lipgloss.Color("#8a2a2a"),
	BtnPrimaryText:  lipgloss.Color("#f8f7f3"),
	// Tints — barely-perceptible washes.
	StateAwaitingTint:   lipgloss.Color("#dfe5ec"),
	StateWorkingTint:    lipgloss.Color("#e1e6d6"),
	StateWarningTint:    lipgloss.Color("#ebe5d3"),
	StateIdleTint:       lipgloss.Color("#e8e9ea"),
	AccentTint:          lipgloss.Color("#dfe5ec"),
```

- [ ] **Step 4: Fix every `StateProcessing`/`StateProcessingTint` call site**

`cmd/serf-tui/internal/tuitheme/styles.go:41` (and the `Processing` struct field name at line 24, which is a separate cosmetic identifier — rename it to `Working` too for consistency, it has no outside callers per the prior research):

```go
	Working: lipgloss.NewStyle().Foreground(th.StateWorking),
```

(and its declaration line 24: `Working lipgloss.Style` instead of `Processing lipgloss.Style`.)

`cmd/serf-tui/hub_dashboard_view.go` (`stateColor`, case `"active"`, line ~308):

```go
	case "active":
		return th.StateWorking
```

`cmd/serf-tui/composer_render.go:74`:

```go
		modeColor = th.StateWorking
```

`cmd/serf-tui/internal/msgrender/message.go:200` and `:361`:

```go
		bar := tuiprim.StateBar(th.StateWorking)
```
```go
		return th.StateWorking
```

`cmd/serf-tui/internal/msgrender/tool_bodies.go:182`:

```go
		clr = th.StateWorking
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/serf-tui/... -v 2>&1 | tail -60`
Expected: PASS. If any test asserted a specific old hex value for `StateAwaiting`/`StateIdle`/`StateProcessing` (beyond the ones already fixed in Step 1), fix it now — grep `go test` failure output for the exact hex string and update.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-tui/internal/tuitheme/tokens.go cmd/serf-tui/internal/tuitheme/tokens_test.go cmd/serf-tui/internal/tuitheme/styles.go cmd/serf-tui/hub_dashboard_view.go cmd/serf-tui/composer_render.go cmd/serf-tui/internal/msgrender/message.go cmd/serf-tui/internal/msgrender/tool_bodies.go
git commit -m "feat(tui): rename StateProcessing->StateWorking, reassign palette hues"
```

---

## Task 17: TUI `displayWord` — unified vocabulary words on the status badge and project summary

**Files:**
- Modify: `cmd/serf-tui/hub_dashboard_view.go`
- Modify: `cmd/serf-tui/hub_session_view.go`
- Modify: `cmd/serf-tui/hub_types.go` (`hubSessionDetail.AskPending` field — populated with a hardcoded `false` in this task; Phase 4 Task 26 threads the real bit through)
- Modify: `cmd/serf-tui/hub_dashboard_view_test.go`

**Interfaces:**
- Consumes: `hubapi.StateWord(state string, askPending bool) string` (Task 4), `stateLabel(state string) string` (unchanged — still the internal normalized-key function).
- Produces: `displayWord(state string, askPending bool) string`.

**Verification note (no code change):** the composer's question chip (`composer_panel.go:220-223`, `"◆ question waiting — ctrl+q to answer"`) and the subagent tally in `msgrender/tool_bodies.go` (lines 304/307/310/322, using `⟳`/`✓`/`✕`) already use the unicode-fallback vocabulary the spec explicitly permits for the TUI ("adopt the icon vocabulary where the terminal can render it (unicode fallback where it can't)") and already match the target wording ("question waiting" is verbatim the target's "Question waiting" band). Confirm this by reading both files at the cited lines during this task; do not change them.

- [ ] **Step 1: Write the failing test**

Append to `cmd/serf-tui/hub_dashboard_view_test.go`:

```go
func TestDisplayWord_UnifiedVocabulary(t *testing.T) {
	cases := []struct {
		state      string
		askPending bool
		want       string
	}{
		{"active", false, "Working"},
		{"awaiting", false, "Your move"},
		{"awaiting", true, "Question waiting"},
		{"warning", false, "Warning"},
		{"systemerror", false, "Error"},
		{"idle", false, "Idle"},
		{"closed", false, "Ended"},
	}
	for _, c := range cases {
		if got := displayWord(c.state, c.askPending); got != c.want {
			t.Errorf("displayWord(%q, %v) = %q, want %q", c.state, c.askPending, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-tui/... -run TestDisplayWord_UnifiedVocabulary -v`
Expected: FAIL — `displayWord` undefined.

- [ ] **Step 3: Implement `displayWord` + rewrite `projectSummary`**

In `cmd/serf-tui/hub_dashboard_view.go`, add near `stateLabel` (after its closing brace, line 578):

```go
// displayWord returns the unified display word (Track A §1/§2) for a raw
// wire state, normalizing via stateLabel first and then delegating to
// hubapi.StateWord — the same table cmd/serf-hub's stateLabel uses, so the
// TUI and the web can never independently drift on vocabulary.
func displayWord(state string, askPending bool) string {
	return hubapi.StateWord(stateLabel(state), askPending)
}
```

Replace `projectSummary` (currently lines 580-595) so it tracks the worst-ranked row's normalized key separately from its display word (the rank comparison needs the raw key; only the final text needs the word):

```go
func projectSummary(project hubRow, rows []hubRow) string {
	liveCount, recentCount := projectSessionCounts(project, rows)
	worstState := project.state
	worstAsk := project.askPending
	for _, row := range rows {
		if row.kind != hubRowSession || row.projectKey != project.projectKey {
			continue
		}
		if attentionRankLabel(row.state) > attentionRankLabel(worstState) {
			worstState = row.state
			worstAsk = row.askPending
		}
	}
	attention := displayWord(worstState, worstAsk)
	if recentCount > 0 {
		return fmt.Sprintf("%d live · %d recent · %s", liveCount, recentCount, attention)
	}
	return fmt.Sprintf("%d live · %s", liveCount, attention)
}
```

(`hubRow.askPending` does not exist until Phase 4 Task 26 — for this task, add a **temporary** unexported field stub so the file compiles: in `cmd/serf-tui/hub_model.go`, add `askPending bool` to the `hubRow` struct now, defaulted to its zero value everywhere it's constructed. Phase 4 Task 26 is then only a matter of *populating* this already-declared field from the wire, not adding it — reducing that later task's surface. Add `"primeradiant.com/serf/hubapi"` to `hub_dashboard_view.go`'s imports.)

Add the field to `hubRow` in `cmd/serf-tui/hub_model.go` now:

```go
type hubRow struct {
	kind        hubRowKind
	ref         appwire.Ref
	sourceLabel string
	title       string
	project     string
	projectKey  string
	state       string
	askPending  bool
	live        bool
	model       string
	age         string
	rowID       string
	createdAt   int64
	updatedAt   int64
	liveCount   int
	recentCount int
}
```

Update `cmd/serf-tui/hub_session_view.go`'s badge line (currently line 29):

```go
	badge := tuiprim.StatusBadge(stateColor(normalizedState), displayWord(state, m.detail.AskPending))
```

Add `AskPending bool` to `hubSessionDetail` in `cmd/serf-tui/hub_types.go` (next to the existing `State string` field), and set it to `false` in `hubDetailFromThread` for now (Phase 4 Task 26 reads the real `thread.Serf.AskPending`):

```go
type hubSessionDetail struct {
	Ref              string
	SessionID        string
	SourceLabel      string
	Title            string
	State            string
	AskPending       bool
	Model            string
	// ... (rest unchanged)
}
```

In `hubDetailFromThread` (`hub_types.go`, currently building the `hubSessionDetail{...}` literal), add `AskPending: false, // Phase 4 Task 26 wires the real bit` alongside the existing `State: node.State,` line.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/serf-tui/... -v 2>&1 | tail -60`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-tui/hub_dashboard_view.go cmd/serf-tui/hub_dashboard_view_test.go cmd/serf-tui/hub_session_view.go cmd/serf-tui/hub_model.go cmd/serf-tui/hub_types.go
git commit -m "feat(tui): displayWord unified vocabulary on status badge + project summary"
```

---

# Phase 4 — Ask-aware attention tiering (§2 of the design; §3-§7 of the folded-in ask-tiering spec)

## Task 18: `hubapi.NeedsYouBand` — the three-band ranking function

**Files:**
- Modify: `hubapi/attention.go`
- Modify: `hubapi/attention_test.go`

**Interfaces:**
- Produces: `hubapi.NeedsYouBand(state string, askPending bool) int` — `2` for `errored`, `1` when `askPending` (any state, though only meaningful for `awaiting`), `0` otherwise. Consumed by Task 23 (hub `tree.go` sort) and Task 25 (TUI `dashboardRowLess`).

- [ ] **Step 1: Write the failing test**

Append to `hubapi/attention_test.go`:

```go
func TestNeedsYouBand(t *testing.T) {
	cases := []struct {
		state      string
		askPending bool
		want       int
	}{
		{"errored", false, 2},
		{"errored", true, 2}, // errored always outranks ask-pending
		{"awaiting", true, 1},
		{"awaiting", false, 0},
		{"warning", false, 0},
	}
	for _, c := range cases {
		if got := NeedsYouBand(c.state, c.askPending); got != c.want {
			t.Errorf("NeedsYouBand(%q, %v) = %d, want %d", c.state, c.askPending, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./hubapi/ -run TestNeedsYouBand -v`
Expected: FAIL — `NeedsYouBand` undefined.

- [ ] **Step 3: Write the implementation**

Append to `hubapi/attention.go`:

```go
// NeedsYouBand ranks a needs-you row into one of three ordering bands:
// errored (2, "broken beats blocked"), ask-pending (1, "blocked beats
// your-move"), or your-move (0, a generic settle). Callers sort NeedsYou
// rows by this band descending, then by recency within a band. Meaningful
// only for the needs-you tier (errored/awaiting/warning states); callers
// outside that tier should not invoke it. askPending is ignored when state
// is "errored" (errored always wins regardless).
func NeedsYouBand(state string, askPending bool) int {
	switch {
	case state == "errored":
		return 2
	case askPending:
		return 1
	default:
		return 0
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./hubapi/ -run TestNeedsYouBand -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add hubapi/attention.go hubapi/attention_test.go
git commit -m "feat(hubapi): NeedsYouBand three-band ranking function"
```

---

## Task 19: Daemon wire — `StatusInfo.PendingAsk` + `Server.pendingAskFn`

**Files:**
- Modify: `server/server.go`
- Modify: `server/server_handlers.go`
- Modify: `server/server_test.go`
- Modify: `cmd/serf/serve.go`

**Interfaces:**
- Consumes: `func (s *Session) HasPendingAsk() bool` (`agent/session_tools_ask.go:67`, already shipped).
- Produces: `func (s *Server) SetPendingAskFunc(fn func() bool)`, `StatusInfo.PendingAsk bool` (`json:"pending_ask,omitempty"`). Consumed by Task 20 (prober is a separate HTTP client, doesn't call this directly, but decodes the JSON this produces) and Task 24 (`appThread()` reuses the same `pendingAskFn`).

- [ ] **Step 1: Write the failing test**

Append to `server/server_test.go`:

```go
func TestHandleStatus_PendingAskOverlaysLiveFunc(t *testing.T) {
	srv := NewServer(nil)
	srv.SetStatus(StatusInfo{SessionID: "s1", State: "awaiting"})
	pending := true
	srv.SetPendingAskFunc(func() bool { return pending })

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	srv.handleStatus(rec, req)
	var got StatusInfo
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !got.PendingAsk {
		t.Fatal("expected pending_ask=true while pendingAskFn returns true")
	}

	pending = false
	rec = httptest.NewRecorder()
	srv.handleStatus(rec, req)
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.PendingAsk {
		t.Fatal("expected pending_ask=false once pendingAskFn flips false")
	}
}
```

(Match the exact `NewServer(...)` constructor signature used by sibling tests in this file — copy it from an adjacent test rather than guessing.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./server/ -run TestHandleStatus_PendingAskOverlaysLiveFunc -v`
Expected: FAIL to compile — `StatusInfo.PendingAsk`, `SetPendingAskFunc` undefined.

- [ ] **Step 3: Add the field, the callback, and the `handleStatus` overlay**

In `server/server.go`, add to `StatusInfo` (currently lines 82-97, after `ActiveTurnStartedAt int64`):

```go
	// PendingAsk mirrors the session's HasPendingAsk() — true while an
	// ask_user question is unanswered (Track A §2 ask-tiering). Additive,
	// daemon-truth: Codex-sourced threads and old daemons never set it, so
	// absence decodes as false everywhere downstream.
	PendingAsk bool `json:"pending_ask,omitempty"`
```

Add the field + setter near `pressureFn` (currently line 160-167 and its setter at 385-390):

```go
	pendingAskFn         func() bool
```

```go
// SetPendingAskFunc sets a callback to retrieve the live pending-ask bit
// (Track A §2). Read by both /status (handleStatus) and the appwire thread
// projection (appThread).
func (s *Server) SetPendingAskFunc(fn func() bool) {
	s.mu.Lock()
	s.pendingAskFn = fn
	s.mu.Unlock()
}
```

In `server/server_handlers.go`'s `handleStatus` (currently lines 260-309), capture the callback alongside the others (after `wmfn := s.workMetricsFn`, line 270):

```go
	pafn := s.pendingAskFn
```

and overlay it after the existing `wmfn` block (after line 304's closing brace):

```go
	if pafn != nil {
		status.PendingAsk = pafn()
	}
```

In `cmd/serf/serve.go`, add the wiring next to the other `Set*Func` calls (after `srv.SetWorkMetricsFunc(...)`, line 353-356):

```go
	srv.SetPendingAskFunc(func() bool { return getSession().HasPendingAsk() })
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./server/... ./cmd/serf/... -v 2>&1 | tail -40`
Expected: PASS.

- [ ] **Step 5: Regenerate + check the appwire doc**

Run: `make generate && make lint-generated`
Expected: PASS or a regenerated `docs/appwire-protocol.md` diff (new field is additive, no catalog entry needed per Global Constraints — `make lint-generated` only fails on a stale generated file, not a missing catalog entry). If it regenerates a diff, include it in this commit.

- [ ] **Step 6: Commit**

```bash
git add server/server.go server/server_handlers.go server/server_test.go cmd/serf/serve.go docs/appwire-protocol.md
git commit -m "feat(server): additive pending_ask bit on StatusInfo, daemon-truth via HasPendingAsk"
```

---

## Task 20: Hub wire — prober decodes `pending_ask`, roster carries `PendingAsk`

**Files:**
- Modify: `cmd/serf-hub/internal/hubcore/prober.go`
- Modify: `cmd/serf-hub/internal/hubcore/prober_test.go`
- Modify: `cmd/serf-hub/internal/hubcore/roster.go`
- Modify: `cmd/serf-hub/internal/hubcore/roster_test.go`

The `Prober` interface's `Probe` method returns a plain 3-tuple `(sessionID, status string, ok bool)` today (not a struct), so this task's signature change touches every implementation and every call site — the production `StatusProber`, the roster's `probeResult`/`Refresh` loop, and any test fakes implementing `Prober`.

**Interfaces:**
- Consumes: the `pending_ask` JSON field from `/status` (Task 19).
- Produces: `Prober.Probe(entry rendezvous.Entry) (sessionID, status string, pendingAsk, ok bool)`, `LiveEntry.PendingAsk bool`. Consumed by Task 21 (`hubcore.TreeNode.AskPending`) and Task 22 (`hubcore.AttentionEntry.askPending`).

- [ ] **Step 1: Write the failing tests**

Add to `cmd/serf-hub/internal/hubcore/prober_test.go` (create the file if it doesn't exist, following the package's existing test-server pattern — check for an existing `prober_test.go` first):

```go
func TestStatusProber_DecodesPendingAsk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"session_id": "01A", "state": "awaiting", "pending_ask": true})
	}))
	defer srv.Close()
	p := &StatusProber{}
	entry := rendezvous.Entry{Address: strings.TrimPrefix(srv.URL, "http://")}
	sessID, status, pendingAsk, ok := p.Probe(entry)
	if !ok || sessID != "01A" || status != "awaiting" || !pendingAsk {
		t.Fatalf("Probe() = %q, %q, %v, %v; want 01A, awaiting, true, true", sessID, status, pendingAsk, ok)
	}
}

func TestStatusProber_AbsentPendingAskDecodesFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"session_id": "01A", "state": "active"})
	}))
	defer srv.Close()
	p := &StatusProber{}
	entry := rendezvous.Entry{Address: strings.TrimPrefix(srv.URL, "http://")}
	_, _, pendingAsk, _ := p.Probe(entry)
	if pendingAsk {
		t.Fatal("absent pending_ask (old daemon / Codex thread) must decode as false")
	}
}
```

Append to `cmd/serf-hub/internal/hubcore/roster_test.go`:

```go
func TestRoster_CarriesPendingAskFromProber(t *testing.T) {
	prober := stubProber{sessionID: "01A", status: "awaiting", pendingAsk: true, ok: true} // adapt to this file's existing fake-prober type/pattern
	r := NewRoster(t.TempDir(), prober)
	// ... seed a rendezvous entry the way the file's existing Refresh tests do, then:
	r.Refresh()
	entries := r.List()
	if len(entries) != 1 || !entries[0].PendingAsk {
		t.Fatalf("expected one live entry with PendingAsk=true, got %+v", entries)
	}
}
```

(Match whatever fake-`Prober` type this test file already uses for its `Refresh`/`List` tests — do not invent a new one if `stubProber`-equivalent already exists; extend its fields instead. Give it a `pendingAsk bool` field and thread it through its `Probe` method's new 4th return value.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/serf-hub/internal/hubcore/ -run 'TestStatusProber|TestRoster_CarriesPendingAsk' -v`
Expected: FAIL to compile — `Probe` returns 3 values today; `LiveEntry.PendingAsk` undefined.

- [ ] **Step 3: Implement**

In `cmd/serf-hub/internal/hubcore/prober.go`, extend `statusInfo` and `Probe`:

```go
// statusInfo is a partial mirror of server.StatusInfo (we only need
// session_id, state, and pending_ask).
type statusInfo struct {
	SessionID  string `json:"session_id"`
	State      string `json:"state"`
	PendingAsk bool   `json:"pending_ask"`
}

// Probe implements Prober.
func (p *StatusProber) Probe(entry rendezvous.Entry) (sessionID, status string, pendingAsk, ok bool) {
	timeout := p.Timeout
	if timeout == 0 {
		timeout = 500 * time.Millisecond
	}
	client := &http.Client{Timeout: timeout}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+entry.Address+"/status", nil)
	if err != nil {
		return "", "", false, false
	}
	SetDaemonAuthorization(req.Header, entry.HubToken)
	resp, err := client.Do(req)
	if err != nil {
		return "", "", false, false
	}
	defer resp.Body.Close() //nolint:errcheck // response body close on read path; error is not actionable
	if resp.StatusCode != http.StatusOK {
		return "", "", false, false
	}
	var s statusInfo
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return "", "", false, false
	}
	return s.SessionID, s.State, s.PendingAsk, true
}
```

In `cmd/serf-hub/internal/hubcore/roster.go`, extend `LiveEntry` (currently lines 23-27):

```go
type LiveEntry struct {
	rendezvous.Entry
	SessionID  string
	Status     string // most-recent daemon state ("active", "idle", "awaiting", etc.)
	PendingAsk bool   // true while the daemon reports an unanswered ask_user question
}
```

The `Prober` interface (currently lines 34-36):

```go
type Prober interface {
	Probe(entry rendezvous.Entry) (sessionID, status string, pendingAsk, ok bool)
}
```

`Refresh`'s `probeResult` struct and its two use sites (currently lines 164-169, 172-183, 188-209):

```go
	type probeResult struct {
		entry      rendezvous.Entry
		sessID     string
		status     string
		pendingAsk bool
		ok         bool
	}
	results := make([]probeResult, len(entries))
	var wg sync.WaitGroup
	for i, e := range entries {
		if r.prober == nil {
			results[i] = probeResult{entry: e, ok: true}
			continue
		}
		wg.Add(1)
		go func(i int, e rendezvous.Entry) {
			defer wg.Done()
			sessID, status, pendingAsk, ok := r.prober.Probe(e)
			results[i] = probeResult{entry: e, sessID: sessID, status: status, pendingAsk: pendingAsk, ok: ok}
		}(i, e)
	}
	wg.Wait()
```

and the `LiveEntry` construction inside the results loop (currently line 202):

```go
		live := LiveEntry{Entry: e, SessionID: res.sessID, Status: res.status, PendingAsk: res.pendingAsk}
```

`rosterFingerprint` (currently lines 127-141) must hash `PendingAsk` too — otherwise a session whose ask bit flips while `Status` stays `"awaiting"` produces the same fingerprint and `onChange` never fires, so the ask marker would silently go stale until an unrelated state change:

```go
func rosterFingerprint(bySess map[string]LiveEntry) uint64 {
	ids := make([]string, 0, len(bySess))
	for id := range bySess {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	h := fnv.New64a()
	for _, id := range ids {
		_, _ = h.Write([]byte(id))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(bySess[id].Status))
		_, _ = h.Write([]byte{0})
		if bySess[id].PendingAsk {
			_, _ = h.Write([]byte{1})
		}
		_, _ = h.Write([]byte{0})
	}
	return h.Sum64()
}
```

Fix every other `Prober` implementation in the package's tests (grep `func.*Probe(entry rendezvous.Entry)` across `_test.go` files) to add the 4th return value.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/serf-hub/... -v 2>&1 | tail -60`
Expected: PASS — including a new assertion in `rosterFingerprint`'s own test (if one exists) or a new case added to it confirming an ask-only flip changes the fingerprint: grep `TestRosterFingerprint` or equivalent and add `{Status: "awaiting", PendingAsk: true}` vs `{Status: "awaiting", PendingAsk: false}` producing different hashes, following that test's existing style.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/internal/hubcore/prober.go cmd/serf-hub/internal/hubcore/prober_test.go cmd/serf-hub/internal/hubcore/roster.go cmd/serf-hub/internal/hubcore/roster_test.go
git commit -m "feat(hub): prober decodes pending_ask; roster carries PendingAsk end to end"
```

---

## Task 21: `hubcore.TreeNode.AskPending` → `hubapi.TreeNode.AskPending` (`ask_pending`)

**Files:**
- Modify: `cmd/serf-hub/internal/hubcore/tree.go`
- Modify: `cmd/serf-hub/internal/hubcore/tree_test.go`
- Modify: `hubapi/types.go`
- Modify: `hubapi/types_test.go`
- Modify: `cmd/serf-hub/web_api_tree.go`
- Modify: `cmd/serf-hub/web_api_tree_test.go`

**Interfaces:**
- Consumes: `LiveEntry.PendingAsk` (Task 20).
- Produces: `hubcore.TreeNode.AskPending bool`, `hubapi.TreeNode.AskPending bool` (`json:"ask_pending,omitempty"`). Consumed by Task 23 (band sort) and Task 28 (row markers).

- [ ] **Step 1: Write the failing tests**

Append to `cmd/serf-hub/internal/hubcore/tree_test.go`:

```go
func TestNeedsYou_CarriesAskPendingFromLiveEntry(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{{ID: "01A", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}}}
	live := []LiveEntry{{Entry: rendezvous.Entry{PID: 1}, SessionID: "01A", Status: appwire.ThreadStatusAwaiting, PendingAsk: true}}
	tree := buildTree(metas, live)
	if len(tree.NeedsYou) != 1 || !tree.NeedsYou[0].AskPending {
		t.Fatalf("NeedsYou node must carry AskPending=true, got %+v", tree.NeedsYou)
	}
}
```

Append to `hubapi/types_test.go`:

```go
func TestTreeNode_AskPendingRoundTrips(t *testing.T) {
	n := TreeNode{ID: "01A", State: "awaiting", AskPending: true}
	data, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"ask_pending":true`) {
		t.Fatalf("expected ask_pending:true in wire JSON, got %s", data)
	}
	var got TreeNode
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !got.AskPending {
		t.Fatal("round-trip must preserve AskPending")
	}
}
```

Append to `cmd/serf-hub/web_api_tree_test.go`:

```go
func TestAPITree_NeedsYouCarriesAskPending(t *testing.T) {
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry: rendezvous.Entry{SessionID: "01A", PID: 1}, SessionID: "01A", Status: "awaiting", PendingAsk: true,
	})
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), Roster: roster})
	req := httptest.NewRequest(http.MethodGet, "/api/tree", nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	var resp hubapi.TreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range resp.NeedsYou {
		if n.SessionID == "01A" && n.AskPending {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected NeedsYou[?].AskPending=true for 01A, got %+v", resp.NeedsYou)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/serf-hub/... ./hubapi/... -run 'AskPending' -v`
Expected: FAIL to compile — `AskPending` undefined on all three types.

- [ ] **Step 3: Add the field to all three types + thread it through**

In `hubapi/types.go`, add to `TreeNode` (next to `Favorite`/`Rename`/`Live`):

```go
	AskPending bool `json:"ask_pending,omitempty"`
```

In `cmd/serf-hub/internal/hubcore/tree.go`, add to `TreeNode` (currently lines 134-146, next to `State`):

```go
	AskPending bool // true while the daemon reports an unanswered ask_user question
```

In `BuildTreeAt`'s NeedsYou construction (currently lines 684-688), add `AskPending: le.PendingAsk` to the node literal:

```go
		node := TreeNode{
			ID:         le.SessionID,
			State:      st,
			Kind:       "session",
			AskPending: le.PendingAsk,
		}
```

In `cmd/serf-hub/web_api_tree.go`'s `apiTreeNode` (currently lines 570-597), add `AskPending: n.AskPending,` to the `hubapi.TreeNode{...}` literal (next to `State`/`Kind`).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/serf-hub/... ./hubapi/... -v 2>&1 | tail -60`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/internal/hubcore/tree.go cmd/serf-hub/internal/hubcore/tree_test.go hubapi/types.go hubapi/types_test.go cmd/serf-hub/web_api_tree.go cmd/serf-hub/web_api_tree_test.go
git commit -m "feat(hub): ask_pending flows hubcore.TreeNode -> hubapi.TreeNode wire"
```

---

## Task 22: `hubcore.AttentionEntry.askPending` + `Tick` diffs on an ask-only flip

**Files:**
- Modify: `cmd/serf-hub/internal/hubcore/attention.go`
- Modify: `cmd/serf-hub/internal/hubcore/attention_test.go`

Without this task's `Tick` change, a session whose `askPending` flips while its `Level` stays `"needs_you"` throughout (e.g. a second question arrives right after the first is answered, or vice versa) produces no `serf/attention/changed` diff — the client's loud-scope gating (Task 26) and row marker (Task 28) would then only refresh on the next unrelated level transition or a full tree refetch. `Tick` must treat an ask-only flip as a change too.

**Interfaces:**
- Consumes: `LiveEntry.PendingAsk` (Task 20).
- Produces: `AttentionEntry.AskPending bool` (`json:"askPending,omitempty"`, needs its own `// serf:naming-ignore` line). Consumed by Task 26 (loud-scope gating reads `ch.askPending`).

- [ ] **Step 1: Write the failing tests**

Append to `cmd/serf-hub/internal/hubcore/attention_test.go`:

```go
func TestDeriveAttention_CarriesAskPending(t *testing.T) {
	metas := []schema.SessionMeta{{ID: "01A", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}}}
	live := []LiveEntry{{SessionID: "01A", Status: "awaiting", PendingAsk: true}}
	entries, _ := DeriveAttention(metas, live, nil)
	if !entries["01A"].AskPending {
		t.Fatalf("expected AttentionEntry.AskPending=true, got %+v", entries["01A"])
	}
}

func TestAttentionWatcher_TicksOnAskOnlyFlip(t *testing.T) {
	var got []AttentionChangedPayload
	w := NewAttentionWatcher(func(p AttentionChangedPayload) { got = append(got, p) })
	base := map[string]AttentionEntry{"01A": {ID: "01A", Level: "needs_you", AskPending: false}}
	w.Tick(base, AttentionSummary{}) // seed, silent

	flipped := map[string]AttentionEntry{"01A": {ID: "01A", Level: "needs_you", AskPending: true}}
	w.Tick(flipped, AttentionSummary{})
	if len(got) != 1 {
		t.Fatalf("expected one emitted payload for an ask-only flip (Level unchanged), got %d", len(got))
	}
	if !got[0].Changed[0].AskPending {
		t.Fatalf("changed entry must carry the new AskPending=true, got %+v", got[0].Changed[0])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/serf-hub/internal/hubcore/ -run 'TestDeriveAttention_CarriesAskPending|TestAttentionWatcher_TicksOnAskOnlyFlip' -v`
Expected: FAIL — `AskPending` undefined; the second test also fails logically (0 payloads emitted) even once it compiles, since `Tick` only diffs on `Level`.

- [ ] **Step 3: Implement**

In `cmd/serf-hub/internal/hubcore/attention.go`, add to `AttentionEntry` (currently lines 17-23):

```go
type AttentionEntry struct {
	// serf:naming-ignore
	ID      string `json:"threadId"`
	Title   string `json:"title"`
	Project string `json:"project"`
	Level   string `json:"level"`
	// serf:naming-ignore
	AskPending bool `json:"askPending,omitempty"`
}
```

In `DeriveAttention` (currently lines 73-113), set it on the entry literal (currently `e := AttentionEntry{ID: le.SessionID, Level: level}`):

```go
		e := AttentionEntry{ID: le.SessionID, Level: level, AskPending: le.PendingAsk}
```

In `Tick` (currently lines 132-161), change the transition test to also fire on an ask-only flip:

```go
func (w *AttentionWatcher) Tick(cur map[string]AttentionEntry, sum AttentionSummary) {
	if !w.seeded {
		w.prev = cur
		w.seeded = true
		return
	}
	var changed []AttentionChanged
	for id, e := range cur {
		prev, had := w.prev[id]
		if !had || prev.Level != e.Level || prev.AskPending != e.AskPending {
			pl := "idle"
			if had {
				pl = prev.Level
			}
			changed = append(changed, AttentionChanged{AttentionEntry: e, PrevLevel: pl})
		}
	}
	for id, prev := range w.prev {
		if _, still := cur[id]; !still {
			gone := prev
			gone.Level = "idle"
			gone.AskPending = false
			changed = append(changed, AttentionChanged{AttentionEntry: gone, PrevLevel: prev.Level})
		}
	}
	w.prev = cur
	if len(changed) == 0 {
		return
	}
	w.emit(AttentionChangedPayload{Changed: changed, Summary: sum})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/serf-hub/internal/hubcore/ -v 2>&1 | tail -60`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/internal/hubcore/attention.go cmd/serf-hub/internal/hubcore/attention_test.go
git commit -m "feat(hub): AttentionEntry.askPending; Tick fires on an ask-only flip too"
```

---

## Task 23: NeedsYou sort adopts `hubapi.NeedsYouBand`

**Files:**
- Modify: `cmd/serf-hub/internal/hubcore/tree.go`
- Modify: `cmd/serf-hub/internal/hubcore/tree_test.go`

**Interfaces:**
- Consumes: `hubapi.NeedsYouBand(state string, askPending bool) int` (Task 18), `TreeNode.AskPending` (Task 21).

- [ ] **Step 1: Write the failing test**

Append to `cmd/serf-hub/internal/hubcore/tree_test.go`:

```go
func TestNeedsYou_AskPendingBandsBetweenErroredAndYourMove(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01OLD_YOURMOVE", UpdatedAt: now.Add(-3 * time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
		{ID: "01ASK", UpdatedAt: now.Add(-1 * time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
		{ID: "01ERR", UpdatedAt: now.Add(-2 * time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01OLD_YOURMOVE", Status: appwire.ThreadStatusAwaiting, PendingAsk: false},
		{Entry: rendezvous.Entry{PID: 2}, SessionID: "01ASK", Status: appwire.ThreadStatusAwaiting, PendingAsk: true},
		{Entry: rendezvous.Entry{PID: 3}, SessionID: "01ERR", Status: appwire.ThreadStatusSystemError},
	}
	tree := buildTree(metas, live)
	if len(tree.NeedsYou) != 3 {
		t.Fatalf("NeedsYou len = %d, want 3", len(tree.NeedsYou))
	}
	// errored first, then ask-pending (even though it is newer than the
	// your-move row), then your-move last — despite 01OLD_YOURMOVE being the
	// oldest-updated of all three.
	got := []string{tree.NeedsYou[0].ID, tree.NeedsYou[1].ID, tree.NeedsYou[2].ID}
	want := []string{"01ERR", "01ASK", "01OLD_YOURMOVE"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("band order = %v, want %v", got, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-hub/internal/hubcore/ -run TestNeedsYou_AskPendingBandsBetweenErroredAndYourMove -v`
Expected: FAIL — today's sort is errored-first then pure recency, so `01OLD_YOURMOVE` (oldest) sorts ahead of `01ASK`.

- [ ] **Step 3: Replace the sort with the band function**

In `cmd/serf-hub/internal/hubcore/tree.go`, add `"primeradiant.com/serf/hubapi"` to the imports (if not already added by Task 2), and replace the NeedsYou sort (currently lines 704-717):

```go
	// Three bands, oldest-first inside each band (Track A §2 ask-tiering):
	// errored (broken beats blocked) > ask-pending (blocked beats your-move) >
	// your-move (a generic amber settle). AttentionRank isn't used here — it
	// would also separate plain awaiting from warning, which both belong in
	// the your-move band unless ask-pending.
	sort.SliceStable(needsYou, func(i, j int) bool {
		bi, bj := hubapi.NeedsYouBand(needsYou[i].State, needsYou[i].AskPending), hubapi.NeedsYouBand(needsYou[j].State, needsYou[j].AskPending)
		if bi != bj {
			return bi > bj
		}
		return needsYou[i].UpdatedAt.Before(needsYou[j].UpdatedAt)
	})
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/serf-hub/... -v 2>&1 | tail -60`
Expected: PASS — including the pre-existing `TestNeedsYou_AdmitsErroredAndWarning_RanksErroredFirst` (unaffected: with `AskPending` false on both its fixtures, warning and awaiting both band at `0`, so ordering among them still falls through to recency exactly as before).

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/internal/hubcore/tree.go cmd/serf-hub/internal/hubcore/tree_test.go
git commit -m "feat(hub): NeedsYou sort adopts the three-band hubapi.NeedsYouBand"
```

---

## Task 24: Appwire wire — `SerfThread.AskPending` for the TUI's JSON-RPC path

**Files:**
- Modify: `appwire/types.go`
- Modify: `appwire/types_test.go`
- Modify: `server/appwire_runtime.go`
- Modify: `server/appwire_runtime_test.go`

The web path (Tasks 19-23) travels through `/status` HTTP polling; the TUI reads `appwire.Thread` over JSON-RPC instead (`cmd/serf-tui/hub_types.go`'s `hubTreeFromThreads`/`hubNodeFromThread` build the TUI's local mirror directly from `[]appwire.Thread]`, never touching `hubapi.TreeNode`). This is a second, independent wire chain that needs its own field, sourced from the same `Server.pendingAskFn` Task 19 already wired up.

**Interfaces:**
- Consumes: `Server.pendingAskFn` (Task 19, already wired to `getSession().HasPendingAsk()` in `cmd/serf/serve.go`).
- Produces: `appwire.SerfThread.AskPending bool` (`json:"askPending,omitempty"`). Consumed by Task 25 (TUI reads it off `thread.Serf.AskPending`).

- [ ] **Step 1: Write the failing tests**

Append to `appwire/types_test.go`:

```go
func TestSerfThread_AskPendingRoundTrips(t *testing.T) {
	th := SerfThread{Ref: "local:01A", AskPending: true}
	data, err := json.Marshal(th)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"askPending":true`) {
		t.Fatalf("expected askPending:true in wire JSON, got %s", data)
	}
}
```

Append to `server/appwire_runtime_test.go`:

```go
func TestAppThread_OverlaysPendingAskFunc(t *testing.T) {
	srv := NewServer(nil) // match this file's existing constructor pattern
	srv.SetStatus(StatusInfo{SessionID: "s1", State: "awaiting"})
	srv.SetPendingAskFunc(func() bool { return true })
	thread := srv.appThread()
	if !thread.Serf.AskPending {
		t.Fatal("expected appThread().Serf.AskPending=true")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./appwire/... ./server/... -run 'AskPending' -v`
Expected: FAIL to compile — `SerfThread.AskPending` undefined.

- [ ] **Step 3: Implement**

In `appwire/types.go`, add to `SerfThread` (currently lines 191-223, next to `ActiveTurnStartedAt`):

```go
	// AskPending mirrors StatusInfo.PendingAsk (Track A §2 ask-tiering) —
	// true while an ask_user question is unanswered. Additive: absent on old
	// daemons and Codex threads, decoding as false.
	AskPending bool `json:"askPending,omitempty"`
```

In `server/appwire_runtime.go`'s `appThread()` (currently lines 440-531), capture the callback alongside `wmfn` (after line 452):

```go
	pafn := s.pendingAskFn
```

compute the overlay after the existing `workMillis`/`usage`/`activeTurnStartedAt` block (after line 504):

```go
	askPending := status.PendingAsk
	if pafn != nil {
		askPending = pafn()
	}
```

and add it to the `appwire.SerfThread{...}` literal (currently lines 514-529, next to `ActiveTurnStartedAt: activeTurnStartedAt,`):

```go
			AskPending:          askPending,
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./appwire/... ./server/... -v 2>&1 | tail -40`
Expected: PASS.

- [ ] **Step 5: Regenerate the appwire doc**

Run: `make generate && make lint-generated`
Expected: PASS, possibly with a regenerated `docs/appwire-protocol.md` diff to include in this commit.

- [ ] **Step 6: Commit**

```bash
git add appwire/types.go appwire/types_test.go server/appwire_runtime.go server/appwire_runtime_test.go docs/appwire-protocol.md
git commit -m "feat(appwire): SerfThread.AskPending for the TUI's JSON-RPC wire chain"
```

---

## Task 25: TUI reads `AskPending` end to end — `hubRow`, `dashboardRowLess` band, real `displayWord` wiring

**Files:**
- Modify: `cmd/serf-tui/hub_types.go`
- Modify: `cmd/serf-tui/hub_dashboard.go`
- Modify: `cmd/serf-tui/hub_session_view.go`
- Modify: `cmd/serf-tui/hub_dashboard_view_test.go`
- Modify: `cmd/serf-tui/dashboard_rows_test.go`

`hubRow.askPending` and `hubSessionDetail.AskPending` were already declared as stub fields in Task 17 (defaulted false everywhere); this task populates them from the real wire value and wires the three-band sort.

**Interfaces:**
- Consumes: `thread.Serf.AskPending` (Task 24), `hubapi.NeedsYouBand` (Task 18).

- [ ] **Step 1: Write the failing tests**

Append to `cmd/serf-tui/dashboard_rows_test.go`:

```go
func TestDashboardRowLess_AskPendingBandsAboveYourMove(t *testing.T) {
	yourMove := hubRow{kind: hubRowSession, state: "awaiting", askPending: false, updatedAt: 3000}
	askPending := hubRow{kind: hubRowSession, state: "awaiting", askPending: true, updatedAt: 1000}
	if !dashboardRowLess(askPending, yourMove) {
		t.Fatal("an ask-pending row must sort above a your-move row even though it is older")
	}
}
```

Append to `cmd/serf-tui/hub_dashboard_view_test.go` (or wherever `hubNodeFromThread`/`hubDetailFromThread` are already tested — grep for their existing test coverage first):

```go
func TestHubNodeFromThread_CarriesAskPending(t *testing.T) {
	thread := appwire.Thread{SessionID: "01A", Serf: appwire.SerfThread{AskPending: true}}
	node := hubNodeFromThread(thread)
	if !node.AskPending {
		t.Fatal("expected hubTreeNode.AskPending=true from thread.Serf.AskPending")
	}
}

func TestHubDetailFromThread_CarriesAskPending(t *testing.T) {
	thread := appwire.Thread{SessionID: "01A", Serf: appwire.SerfThread{AskPending: true}}
	detail := hubDetailFromThread(thread)
	if !detail.AskPending {
		t.Fatal("expected hubSessionDetail.AskPending=true from thread.Serf.AskPending")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/serf-tui/... -run 'AskPending' -v`
Expected: FAIL — `dashboardRowLess` has no band term; `hubTreeNode.AskPending` undefined; `hubDetailFromThread` still hardcodes `AskPending: false`.

- [ ] **Step 3: Implement**

In `cmd/serf-tui/hub_types.go`, add `AskPending bool` to `hubTreeNode` (currently lines 24-38, next to `State`). In `hubNodeFromThread` (currently lines 159-185), add to the returned literal:

```go
		AskPending:  thread.Serf.AskPending,
```

In `hubDetailFromThread` (currently building the `hubSessionDetail{...}` literal), replace the Task-17 placeholder:

```go
		AskPending:          thread.Serf.AskPending,
```

In `cmd/serf-tui/hub_dashboard.go`'s `addSession` closure (currently lines 90-140), add to the `hubRow{...}` literal (next to `state: n.State,`):

```go
			askPending:  n.AskPending,
```

Add `"primeradiant.com/serf/hubapi"` to `hub_dashboard.go`'s imports, then extend `dashboardRowLess` (currently lines 221-234) with the band term between rank and recency:

```go
func dashboardRowLess(a, b hubRow) bool {
	ar, br := attentionRankLabel(a.state), attentionRankLabel(b.state)
	if ar != br {
		return ar > br
	}
	aBand, bBand := hubapi.NeedsYouBand(stateLabel(a.state), a.askPending), hubapi.NeedsYouBand(stateLabel(b.state), b.askPending)
	if aBand != bBand {
		return aBand > bBand
	}
	au, bu := rowRecency(a), rowRecency(b)
	if au != bu {
		return au > bu
	}
	if a.project != b.project {
		return strings.ToLower(a.project) < strings.ToLower(b.project)
	}
	return strings.ToLower(a.title) < strings.ToLower(b.title)
}
```

In `cmd/serf-tui/hub_session_view.go`, the badge line already reads `m.detail.AskPending` (Task 17) — no further change needed here now that `hubDetailFromThread` populates the real value.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/serf-tui/... -v 2>&1 | tail -60`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-tui/hub_types.go cmd/serf-tui/hub_dashboard.go cmd/serf-tui/hub_session_view.go cmd/serf-tui/hub_dashboard_view_test.go cmd/serf-tui/dashboard_rows_test.go
git commit -m "feat(tui): AskPending flows thread -> hubRow -> dashboardRowLess band"
```

---

## Task 26: `loudScope` preference — default, migration, gating

**Files:**
- Modify: `cmd/serf-hub/assets/notifications.js`
- Create: `cmd/serf-hub/jstest/test-notifications-loudscope.js`

**Interfaces:**
- Consumes: `ch.askPending` (the `AttentionChanged` payload field from Task 22, camelCase on the wire, read as-is in JS — no transform needed).
- Produces: `DEFAULT_PREFS.loudScope` (`"asks"` default), version-bumped `migratePrefs`, `onAttentionChanged` gating on `prefs.loudScope`.

- [ ] **Step 1: Write the failing test**

Create `cmd/serf-hub/jstest/test-notifications-loudscope.js` (copy the JSDOM + `notifications.js` load pattern from `test-notifications-migration.js`):

```js
"use strict";
require("./load-renderer.js"); // or whatever bootstrap test-notifications-migration.js uses
const assert = require("assert");

// Migration backfills loudScope: "asks" for an existing v2 blob with no
// loudScope key, and bumps the version so migration doesn't re-run.
localStorage.setItem("serf-hub.notifications", JSON.stringify({ title: true, favicon: true, os: false, sound: false }));
localStorage.setItem("serf-hub.notifications.v", "2");
window.SerfNotificationsInternal.migratePrefs();
let prefs = JSON.parse(localStorage.getItem("serf-hub.notifications"));
assert.strictEqual(prefs.loudScope, "asks", "existing v2 users must backfill to the asks default, not off");
assert.strictEqual(localStorage.getItem("serf-hub.notifications.v"), "3", "migration must bump the version so it does not re-run");

// A fresh install gets loudScope: "asks" outright.
localStorage.clear();
window.SerfNotificationsInternal.migratePrefs();
prefs = JSON.parse(localStorage.getItem("serf-hub.notifications"));
assert.strictEqual(prefs.loudScope, "asks");

// Gating: under "asks" (default), a generic needs_you transition (no error,
// no askPending) must NOT fire OS/sound; an askPending or error transition
// must.
let osNotified = 0, soundPlayed = 0;
window.SerfNotificationsInternal.setTestHooks({
  fireOsNotification: () => { osNotified++; },
  playTone: () => { soundPlayed++; },
});
localStorage.setItem("serf-hub.notifications", JSON.stringify({ title: true, favicon: true, os: true, sound: true, loudScope: "asks" }));
window.SerfNotificationsInternal.setLeaderForTest(true);
window.SerfNotificationsInternal.setBaselineForTest({ needsYou: 0, error: 0, working: 0 });
window.SerfNotificationsInternal.onAttentionChanged({
  summary: { needsYou: 1, error: 0, working: 0 },
  changed: [{ id: "01A", level: "needs_you", prevLevel: "idle", askPending: false }],
});
assert.strictEqual(osNotified, 0, `askScope "asks" must suppress a generic needs_you settle, got ${osNotified} OS fires`);

window.SerfNotificationsInternal.onAttentionChanged({
  summary: { needsYou: 2, error: 0, working: 0 },
  changed: [{ id: "01B", level: "needs_you", prevLevel: "idle", askPending: true }],
});
assert.strictEqual(osNotified, 1, "askScope \"asks\" must still fire for an askPending transition");

console.log("test-notifications-loudscope.js: OK");
```

(Match whichever test-hook injection pattern the sibling `test-notifications-*.js` files already use for `leader`/`fireOsNotification`/`playTone`/the baseline `summary` — if no such seam exists yet, add the minimal one following that file's existing style, exposed via a `window.SerfNotificationsInternal` object alongside `migratePrefs`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-notifications-loudscope.js`
Expected: FAIL — `loudScope` absent from `DEFAULT_PREFS`; version stays `"2"`; gating fires for every needs_you transition regardless of `askPending`.

- [ ] **Step 3: Implement**

In `cmd/serf-hub/assets/notifications.js`, extend `DEFAULT_PREFS` (currently line 27):

```js
  const DEFAULT_PREFS = { title: true, favicon: true, os: false, sound: false, loudScope: "asks" };
```

Bump `migratePrefs` (currently lines 63-77) to version `"3"`, backfilling `loudScope` to its default (not `false` — the asymmetry with the boolean channel keys is deliberate, per the folded-in spec):

```js
  function migratePrefs() {
    if (localStorage.getItem(PREFS_VERSION_KEY) === "3") return;
    const raw = localStorage.getItem(PREFS_KEY);
    if (!raw) {
      writePrefs(Object.assign({}, DEFAULT_PREFS));
    } else {
      let cur = {};
      try { cur = JSON.parse(raw) || {}; } catch (e) { cur = {}; }
      for (const k of ["title", "favicon", "os", "sound"]) {
        if (typeof cur[k] !== "boolean") cur[k] = false;
      }
      if (cur.loudScope !== "asks" && cur.loudScope !== "all") cur.loudScope = "asks";
      writePrefs(cur);
    }
    localStorage.setItem(PREFS_VERSION_KEY, "3");
  }
```

Update the gating in `onAttentionChanged` (currently lines 246-253):

```js
    for (const ch of (params && params.changed) || []) {
      const into = ch.level === "needs_you" || ch.level === "error";
      const was = ch.prevLevel === "needs_you" || ch.prevLevel === "error";
      if (into && !was) {
        const loud = prefs.loudScope === "all" || ch.askPending || ch.level === "error";
        if (loud) {
          if (prefs.os) fireOsNotification(ch);
          if (prefs.sound) playTone();
        }
      }
    }
```

Add the test-hook seam (near the module's existing `leader`/`summary` module-scope variables):

```js
  window.SerfNotificationsInternal = {
    migratePrefs: migratePrefs,
    onAttentionChanged: onAttentionChanged,
    setTestHooks: function (hooks) {
      if (hooks.fireOsNotification) fireOsNotification = hooks.fireOsNotification;
      if (hooks.playTone) playTone = hooks.playTone;
    },
    setLeaderForTest: function (v) { leader = v; },
    setBaselineForTest: function (s) { summary = s; },
  };
```

(`fireOsNotification`/`playTone` must be declared with `let`, not `const function`, for the hook-override assignment to work — check their current declaration style and adjust only if necessary.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-notifications-loudscope.js && sh cmd/serf-hub/jstest/run-all.sh 2>&1 | tail -30`
Expected: PASS — fix any pre-existing `test-notifications-migration.js`/`test-notifications-attention.js` case that hardcoded version `"2"` or the old unconditional gating, updating it to version `"3"` / the new loud-scope-aware behavior.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/notifications.js cmd/serf-hub/jstest/test-notifications-loudscope.js
git commit -m "feat(web): loudScope preference — asks-by-default gating, versioned migration"
```

---

## Task 27: `loudScope` settings control

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings/notifications.html`
- Modify: `cmd/serf-hub/assets/settings.js`

The notifications settings section already lives in its own template file (`cmd/serf-hub/templates/partials/settings/notifications.html`) — the HTML-side per-section split some call this "Track 0 prep" already exists on this branch. The JS behavior file `cmd/serf-hub/assets/settings.js` is still monolithic (Track 0's JS-side split has not landed as of this branch's base commit `a2c5a0f7`); add the `loudScope` handler directly to `settings.js`'s existing `data-notif`-adjacent block for now. **If a per-section settings JS file (e.g. `settings-notifications.js`) exists by the time this task executes** (check `ls cmd/serf-hub/assets/settings*.js` first), add the handler there instead and skip editing the monolithic `settings.js`.

- [ ] **Step 1: Write the failing jstest**

Create `cmd/serf-hub/jstest/test-settings-loudscope.js` (copy the JSDOM bootstrap from a sibling `test-settings-*.js`, loading both `settings.js` and a DOM fragment containing the new radio markup from Step 3):

```js
"use strict";
require("./load-renderer.js");
const assert = require("assert");

document.body.innerHTML =
  '<dl class="settings-table" data-notif-form>' +
  '<input type="radio" name="loud-scope" data-notif-radio="loudScope" value="asks" checked>' +
  '<input type="radio" name="loud-scope" data-notif-radio="loudScope" value="all">' +
  "</dl>";

const allRadio = document.querySelector('input[value="all"]');
allRadio.checked = true;
allRadio.dispatchEvent(new window.Event("change", { bubbles: true }));

const stored = JSON.parse(localStorage.getItem("serf-hub.notifications") || "{}");
assert.strictEqual(stored.loudScope, "all", "selecting the 'all' radio must persist loudScope=all");

console.log("test-settings-loudscope.js: OK");
```

- [ ] **Step 2: Run test to verify it fails**

Run: `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-settings-loudscope.js`
Expected: FAIL — no handler matches `input[data-notif-radio]`; `stored.loudScope` is undefined.

- [ ] **Step 3: Implement**

Add the control to `cmd/serf-hub/templates/partials/settings/notifications.html` (inside the existing `<dl class="settings-table" data-notif-form>`, before or after the four existing `.row.editable` blocks):

```html
  <div class="row editable">
    <dt id="lbl-notif-loudscope">Loud for</dt>
    <dd>
      <label class="val-radio">
        <input type="radio" name="loud-scope" data-notif-radio="loudScope" value="asks" aria-labelledby="lbl-notif-loudscope">
        Questions &amp; errors
      </label>
      <label class="val-radio">
        <input type="radio" name="loud-scope" data-notif-radio="loudScope" value="all" aria-labelledby="lbl-notif-loudscope">
        Everything needing me
      </label>
    </dd>
    <p class="help">OS notification and sound fire for this scope only; the title/favicon count always reflects everything needing you.</p>
  </div>
```

Add the radio-commit handler to `cmd/serf-hub/assets/settings.js`, in the same `document.body.addEventListener("change", ...)` delegate (after the `data-notif` checkbox branch, before its closing `});`):

```js
    if (target.matches("input[type=radio][data-notif-radio]")) {
      const key = target.dataset.notifRadio;
      const desired = target.value;
      const cur = readNotifPrefs();
      cur[key] = desired;
      writeNotifPrefs(cur);
      document.dispatchEvent(new CustomEvent("serf-hub:notifications-changed", {
        detail: { key, value: desired },
      }));
      if (window.SerfToast) window.SerfToast.show("Settings saved", "success");
      return;
    }
```

Reflect the stored value on settings-pane load, in `applySettingsState` (next to the existing `notifBoxes` block):

```js
    const loudScopeRadios = document.querySelectorAll('input[type=radio][data-notif-radio="loudScope"]');
    if (loudScopeRadios.length) {
      const stored = readNotifPrefs().loudScope || "asks";
      loudScopeRadios.forEach((r) => { r.checked = r.value === stored; });
    }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-settings-loudscope.js && sh cmd/serf-hub/jstest/run-all.sh 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/templates/partials/settings/notifications.html cmd/serf-hub/assets/settings.js cmd/serf-hub/jstest/test-settings-loudscope.js
git commit -m "feat(web): loudScope settings control (questions & errors / everything)"
```

---

## Task 28: Web row markers — `data-ask="true"` plumbed on the sidebar row

**Files:**
- Modify: `cmd/serf-hub/assets/sidebar.js`

Task 8 already made `buildRow`/`patchRow` choose the `questionWaiting` vs `yourMove` icon from `n.ask_pending` (with a stub reading `undefined`/falsy until now, since the wire field didn't exist yet — Task 21 added it). This task adds the explicit `data-ask` attribute the folded-in spec requires as its own hard requirement (independent of which icon rendered), so e2e/CSS/other consumers can select ask-pending rows without inspecting the icon.

- [ ] **Step 1: Write the failing test**

Append to `cmd/serf-hub/jstest/test-sidebar-icons.js`:

```js
const askOnRow = window.SerfSidebarInternal.buildRow({ row_id: "x", ref: "local:01A", state: "awaiting", ask_pending: true, title: "t", session_id: "01A" });
assert.strictEqual(askOnRow.getAttribute("data-ask"), "true", "an ask-pending row must carry data-ask=true");

const askOffRow = window.SerfSidebarInternal.buildRow({ row_id: "y", ref: "local:01B", state: "awaiting", ask_pending: false, title: "t", session_id: "01B" });
assert.ok(!askOffRow.hasAttribute("data-ask"), "a your-move row must not carry data-ask");
```

- [ ] **Step 2: Run test to verify it fails**

Run: `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-sidebar-icons.js`
Expected: FAIL — `data-ask` never set.

- [ ] **Step 3: Implement**

In `cmd/serf-hub/assets/sidebar.js`'s `buildRow` (after the `if (n.favorite) a.setAttribute("data-favorite", "");` line):

```js
    if (n.ask_pending) a.setAttribute("data-ask", "true");
```

In `patchRow` (after the existing `if (n.favorite) ... else ...` line):

```js
    if (n.ask_pending) a.setAttribute("data-ask", "true"); else a.removeAttribute("data-ask");
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-sidebar-icons.js && sh cmd/serf-hub/jstest/run-all.sh 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/sidebar.js cmd/serf-hub/jstest/test-sidebar-icons.js
git commit -m "feat(web): data-ask row marker on ask-pending sidebar rows"
```

---

## Task 29: TUI row marker — dashboard row shows the ask-pending bubble

**Files:**
- Modify: `cmd/serf-tui/hub_dashboard_view.go`
- Modify: `cmd/serf-tui/hub_dashboard_view_test.go`

The `◆N` header badge (`needsYouBadge`, lines 173-182) already counts all of needs-you unconditionally and needs no change (folded-in spec: "keeps counting all needs-you"). This task adds a per-row marker to the dashboard's session-row rendering so an ask-pending row is visually distinguishable from a your-move row in the list, not just via band ordering.

- [ ] **Step 1: Write the failing test**

Append to `cmd/serf-tui/hub_dashboard_view_test.go`:

```go
func TestDashboardSessionRow_AskPendingMarker(t *testing.T) {
	row := hubRow{kind: hubRowSession, state: "awaiting", askPending: true, title: "x"}
	rendered := renderDashboardSessionRow(row, 80) // match this file's existing row-render function name/signature
	if !strings.Contains(rendered, "◆") {
		t.Fatalf("an ask-pending row must show the ◆ question-waiting marker, got %q", rendered)
	}
}
```

(Confirm the actual row-rendering function name/signature by reading `hub_dashboard_view.go` around where `stateColor`/`needsYouBadge` are used to render a single dashboard row — do not invent `renderDashboardSessionRow` if the real name differs; use the real one.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-tui/... -run TestDashboardSessionRow_AskPendingMarker -v`
Expected: FAIL — no `◆` present for an ask-pending row today (only the header badge shows one, not per-row).

- [ ] **Step 3: Implement**

At the row-rendering site identified in Step 1, add a small marker immediately before or after the state bar/badge, reusing the same `◆` glyph the composer's question chip and header badge already use (one vocabulary for "question waiting" — folded-in spec §6):

```go
	marker := ""
	if row.askPending {
		marker = lipgloss.NewStyle().Foreground(th.StateAwaiting).Render("◆ ")
	}
```

and prepend `marker` to the row's rendered title/text at the point the function assembles its output string. Match the existing indentation/column-budget conventions in that function (the marker consumes 2 display columns; if the function does explicit width budgeting for truncation, account for it there too).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/serf-tui/... -v 2>&1 | tail -40`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-tui/hub_dashboard_view.go cmd/serf-tui/hub_dashboard_view_test.go
git commit -m "feat(tui): per-row ask-pending marker in the dashboard list"
```

---

## Task 30: `pending_ask` truthful-after-restart wire test

**Files:**
- Modify: `server/awaiting_status_test.go`

The agent-side restore rebuild (`§2` of the folded-in spec, main commit `d331ccaf`) already makes `HasPendingAsk()` truthful across daemon restarts. This task pins that truthfulness **at the wire** — the property this whole track's wire plumbing exists to expose — with a serve-level test asserting `pending_ask` true → false → true-after-restart.

- [ ] **Step 1: Write the failing test**

Append to `server/awaiting_status_test.go` (matching its existing `handleStatus`-driving pattern from Task 19's test):

```go
func TestHandleStatus_PendingAskTrueFalseTrueAfterRestart(t *testing.T) {
	srv := NewServer(nil)
	srv.SetStatus(StatusInfo{SessionID: "s1", State: "awaiting"})

	asked := true
	srv.SetPendingAskFunc(func() bool { return asked })
	if got := statusPendingAsk(t, srv); !got {
		t.Fatal("expected pending_ask=true while the question is unanswered")
	}

	asked = false
	if got := statusPendingAsk(t, srv); got {
		t.Fatal("expected pending_ask=false once answered")
	}

	// Simulate a daemon restart: a fresh Server, a fresh pendingAskFn backed
	// by a new session whose restore rebuilt HasPendingAsk()=true (the
	// already-shipped §2 restore fix) — the wire must reflect it immediately,
	// with no post-restart grace period where it reads stale-false.
	restarted := NewServer(nil)
	restarted.SetStatus(StatusInfo{SessionID: "s1", State: "awaiting"})
	restarted.SetPendingAskFunc(func() bool { return true })
	if got := statusPendingAsk(t, restarted); !got {
		t.Fatal("expected pending_ask=true immediately after restart, mirroring HasPendingAsk()'s restore rebuild")
	}
}

func statusPendingAsk(t *testing.T, srv *Server) bool {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	srv.handleStatus(rec, req)
	var got StatusInfo
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	return got.PendingAsk
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./server/ -run TestHandleStatus_PendingAskTrueFalseTrueAfterRestart -v`
Expected: PASS already, in fact — Task 19 already implements the overlay this test exercises. Run it to confirm it's green as a regression pin, not to drive new implementation (this task is a pure test-addition task; if it somehow fails, that means Task 19's overlay has a bug — stop and fix Task 19, do not add new production code here).

- [ ] **Step 3: Run test to verify it passes**

Run: `go test ./server/ -run TestHandleStatus_PendingAskTrueFalseTrueAfterRestart -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add server/awaiting_status_test.go
git commit -m "test(server): pin pending_ask true/false/true-after-restart at the wire"
```

---

# Phase 5 — e2e scenario cards + hermetics batch

These tasks produce `test/scenarios/*.md` runbooks, not automated Go/JS tests — they follow this repo's `test/scenarios/_template.md` format (`# <area>-<slug>: <title>` / `**What this covers**` / `## Pre-state` / `## Steps` / `## Expected` with a `Falsification:` line / `## Cleanup` / `## Sharp edges`) and are executed live by an agent per the `e2e-scenario-testing` skill against a freshly built hub with an isolated `$HOME` and a dedicated Chrome profile — evidence over assertion, no mocks. The "red/green" cycle for these tasks is: write the card, then actually run it once live and fix any step whose commands don't match the real, current system before checking it in.

## Task 31: New e2e card — unified status vocabulary round-trip

**Files:**
- Create: `test/scenarios/status-vocabulary-roundtrip.md`

- [ ] **Step 1: Write the card**

Create `test/scenarios/status-vocabulary-roundtrip.md`:

```markdown
# status-vocabulary-roundtrip: unified icon/word/color agree across sidebar, thread page, and TUI

**What this covers**: Track A §1 (unified status vocabulary & icons) — the same state must
render the same icon+tooltip on the web sidebar, the same word on the TUI dashboard/session
header, and the same word from `hubapi.StateWord` in both, for the palette v2 mapping (green
Working, blue Needs-you split into Question-waiting/Your-move, amber Warning, red Error, gray
Idle/Ended).

## Pre-state

- Hub running with a fresh `$HOME` (isolated `~/.serf`), real credentials, no prior sessions.
- A TUI (`serf-tui`) pointed at the same hub, in a tmux session for scriptable interaction.
- `superpowers-chrome:browsing` available for the web assertions.

## Steps

1. Spawn a session and let it settle to a generic `awaiting` (your-move) state — a prompt with
   no `ask_user` call, e.g. "Say hello and stop." Wait for `state=="awaiting"` via
   `GET /api/sessions/local:<id>`.
2. In the browser, open the sidebar and inspect the row for this session:
   ```javascript
   (() => {
     const row = document.querySelector('[data-ref="local:<id>"]') || document.querySelector('a.sb-row[href*="<id>"]');
     const icon = row.querySelector('.status-icon');
     return { title: icon.getAttribute('title'), hasSvg: !!icon.querySelector('svg') };
   })()
   ```
3. In the TUI, navigate to the hub dashboard and locate the same session's row; capture the
   rendered state word (tmux capture-pane).
4. Open the session's detail view in the TUI; capture the status badge text.
5. Force the same session into `errored` (a bad prompt / injected failure), re-check the same
   three surfaces.
6. Force a genuine `ask_user` question, re-check the same three surfaces plus the sidebar's
   `data-ask` attribute.

## Expected

- Step 2: `title === "Your move"`, `hasSvg === true`.
- Step 3/4: TUI shows `Your move` (dashboard row) / `YOUR MOVE` (uppercased status badge).
- Step 5: sidebar tooltip `"Error"`, TUI shows `Error`/`ERROR`.
- Step 6: sidebar tooltip `"Question waiting"`, `data-ask="true"` present on the row; TUI shows
  `Question waiting`/`QUESTION WAITING` and the per-row `◆` marker (Task 29).
- Falsification: if the sidebar tooltip word and the TUI status-badge word ever disagree for
  the same underlying state (e.g. sidebar says "Working" while the TUI still says "ACTIVE"),
  the shared `hubapi.StateWord` delegation is broken or one surface bypassed it.

## Cleanup

- Shut down the spawned session; kill the TUI tmux session; remove the isolated `$HOME`.

## Sharp edges

- The TUI's `StatusBadge` uppercases whatever word it's given (`tuiprim.StatusBadge`); the web
  never uppercases — this is expected surface-specific styling, not a vocabulary mismatch.
- The `data-ask` attribute is only present for the Question-waiting band, never for Your-move —
  confirm its *absence* on a your-move row as part of falsification, not just its presence on
  an ask-pending one.
```

- [ ] **Step 2: Run it live once and fix any drift**

Execute the card exactly as written against a real hub + TUI + browser session (per the `e2e-scenario-testing` skill). Fix any step whose selector, endpoint, or expected string doesn't match the actual running system (e.g. the sidebar row selector, or the TUI capture-pane text) — every falsification line must still hold after the fix.

- [ ] **Step 3: Commit**

```bash
git add test/scenarios/status-vocabulary-roundtrip.md
git commit -m "test(e2e): unified status vocabulary round-trip scenario card"
```

---

## Task 32: Fix + extend `ask-cross-session-notify` (client-rendered sidebar, `/api/tree`, loud-scope default)

**Files:**
- Modify: `test/scenarios/ask-cross-session-notify.md`

The card's step 5 and several "Sharp edges" bullets predate the WS3 sidebar rebuild (merge `f0030dbf`): `templates/partials/sidebar.html` no longer exists, there is no `/_partials/sidebar` route, and the sidebar is client-rendered from `GET /api/tree` JSON by `cmd/serf-hub/assets/sidebar.js`. Per the folded-in ask-tiering spec's hermetics batch item 4, this card needs its NeedsYou-tier check re-pointed at the real endpoint (no `HX-Request` header — `/api/tree` is a plain JSON API route, not an htmx partial) and its expected count corrected to account for **both** sessions now present in NeedsYou (this track's Task 23 band change doesn't add a session to the tier, only reorders it — the `(1)` vs `(2)` question from the handoff note is about Session B's own state, not this track's change; verify the actual live count at execution time rather than assuming). Also fold in the `loudScope` default assertion this track adds (Task 26): under the default `"asks"` scope, a *generic* your-move settle must not fire OS/sound, only an ask-pending or errored one does.

- [ ] **Step 1: Rewrite step 5 and the affected "Sharp edges" bullets**

Replace step 5 (currently the `/_partials/sidebar` `DOMParser` fetch) with a direct `/api/tree` check matching the real client-rendered architecture:

```javascript
fetch('/api/tree', { headers: { Authorization: 'Bearer ' + '<TOKEN>' } }).then(r => r.json()).then(tree => {
  const row = tree.needs_you.find(n => n.session_id === '<SIDA>');
  window.__sidebarCheck = { present: !!row, count: tree.needs_you.length, askPending: row ? row.ask_pending : null };
  return window.__sidebarCheck;
})
```

Update the "Expected" step-5 bullet: `present` is `true`; `askPending` is `true` (Session A asked a question — this is the band-ordering wire bit this track adds, Task 21); `count` reflects the live NeedsYou population at execution time (assert it is `>= 1` and specifically includes Session A, rather than hardcoding a stale `(1)`/`(2)` expectation that predates this track).

Update the "Sharp edges" bullet currently describing `templates/partials/sidebar.html`/`hx-trigger="load, sidebar:refresh from:body"` to describe the actual current mechanism: `sidebar.js` refetches `/api/tree` on `serf/attention/changed` (and other refresh triggers already wired in `sidebar.js`), reconciling the DOM via keyed `RowID`s rather than re-rendering an htmx partial.

Add a new step 6 + Expected bullet for the `loudScope` default:

```javascript
// Re-arm with a fresh baseline, then trigger a generic (non-ask, non-error)
// needs_you transition on a third throwaway session and confirm it stays quiet
// under the default loudScope="asks".
```

Add to "Expected": a generic your-move transition on a third session produces no new entry in `window.__asked` (the array captured in step 4's stub) under the default `loudScope: "asks"`, while Session A's ask-pending transition (already asserted in step 4) did fire — demonstrating the default scope distinguishes the two.

- [ ] **Step 2: Run the updated card live**

Execute the full card end to end against a real hub (per `e2e-scenario-testing`). Confirm every falsification line holds; the falsification prose itself stays byte-identical to before this task except for the sentence naming the new loud-scope check, per the "mechanics only" hermetics-batch rule.

- [ ] **Step 3: Commit**

```bash
git add test/scenarios/ask-cross-session-notify.md
git commit -m "fix(e2e): ask-cross-session-notify targets /api/tree post-sidebar-rebuild; add loudScope default check"
```

---

## Task 33: Hermetics batch — remaining scenario-card mechanics fixes (docs-only, byte-identical falsification lines)

**Files:**
- Modify: `test/scenarios/ask-noninteractive-invisible.md`
- Modify: `test/scenarios/ask-subagent-invisible.md`
- Modify: `test/scenarios/ask-tui-answer.md`
- Modify: any scenario card still polling for `idle` after a reply instead of `awaiting`-at-rest (grep `test/scenarios/*.md` for `"idle"` polling loops in ask-adjacent cards)

Per the folded-in ask-tiering spec §9 / the remaining-work handoff §6: fix mechanics only, every falsification line stays byte-identical to today's.

- [ ] **Step 1: Idle-poll → awaiting-poll pattern**

Grep `test/scenarios/*.md` for a polling loop like `[ "$st" = "idle" ] && break` in a card that waits for a reply to an answered question. Replace `idle` with `awaiting` in the poll condition (post-merge, an answered ask-then-settle session rests `awaiting`, not `idle` — the inbox-semantics behavior this whole track's tiering builds on). Leave every other line, including the card's own falsification prose, untouched.

- [ ] **Step 2: `ask-noninteractive-invisible.md` — locate by session id, not `working_dir` grep**

Open `test/scenarios/ask-noninteractive-invisible.md`. Find the step that greps a transcript file by `working_dir` (fails on macOS because `/tmp` resolves to `/private/tmp` inside the daemon but the test script's `$tmpdir` variable holds the unresolved `/tmp/...` path). Change the grep/find to key on the session id instead (e.g. `find ~/.serf -name '*<SID>*'` or whatever this card's existing transcript-location convention is elsewhere in the same file — match it, don't invent a new one).

- [ ] **Step 3: `ask-subagent-invisible.md` — assert the delegate's `communicate` call argument, not a raw grep**

Open `test/scenarios/ask-subagent-invisible.md`. Find the step that greps the transcript for marker strings (polluted by the task prompt echoing the same strings). Replace it with an assertion on the delegate subagent's actual `communicate` tool-call argument (locate the specific JSON event/field this card's transcript format already exposes for tool calls, matching the pattern used elsewhere in the same card or a sibling subagent-focused card). Add a one-line note (in "Sharp edges", not altering any Expected/Falsification line) that the parent legitimately rests `active` while the delegate child session lingers as live autonomy — this is expected, not a bug to falsify on.

- [ ] **Step 4: `ask-tui-answer.md` — note the tmux em-dash round-trip hazard**

Open `test/scenarios/ask-tui-answer.md`. Add a "Sharp edges" bullet (not touching any Expected/Falsification line) noting that asserting the chip text via tmux `capture-pane` can round-trip an em-dash (`—`) as a different byte sequence depending on terminal encoding, so a byte-exact string match on a line containing `—` is fragile — match on a substring that excludes the em-dash, or normalize encoding before comparing.

- [ ] **Step 5: Run every touched card live once**

Execute each of the four/five touched cards against a real hub + TUI (per `e2e-scenario-testing`), confirming every Expected/Falsification line still holds exactly as worded before this task (only the mechanics — commands, greps, poll conditions — changed).

- [ ] **Step 6: Commit**

```bash
git add test/scenarios/ask-noninteractive-invisible.md test/scenarios/ask-subagent-invisible.md test/scenarios/ask-tui-answer.md
git commit -m "fix(e2e): scenario-card hermetics batch — mechanics only, falsification lines unchanged"
```

(Add any additional idle-poll card file discovered in Step 1 to this same commit.)

---

## Task 34: Full gate — lint, tests, jstest, appwire doc

**Files:**
- None (verification-only task; no production code changes expected)

- [ ] **Step 1: Run the full Go test suite**

Run: `go test ./... 2>&1 | tail -80`
Expected: PASS across every package this track touched (`hubapi`, `cmd/serf-hub/...`, `cmd/serf-tui/...`, `server`, `cmd/serf`, `appwire`).

- [ ] **Step 2: Run the full lint gate (includes `serf-namingcheck`, which per-task `golangci-lint` misses)**

Run: `make lint`
Expected: PASS. If `serf-namingcheck` flags a camelCase JSON tag this track introduced (`askPending` on `AttentionEntry`, `AskPending` json tags elsewhere), confirm the `// serf:naming-ignore` comment sits on the line immediately above the flagged field (added in Task 22) — it should already pass; if it doesn't, the marker is misplaced relative to what the checker expects, not a design flaw — fix the comment placement.

- [ ] **Step 3: Run the appwire doc regen gate**

Run: `make generate && make lint-generated`
Expected: PASS with no stale-doc diff (Tasks 19 and 24 already regenerated `docs/appwire-protocol.md` at the time each field landed).

- [ ] **Step 4: Run the full jstest suite**

Run: `sh cmd/serf-hub/jstest/run-all.sh 2>&1 | tail -60`
Expected: every `test-*.js` exits 0, including every new/updated file this track added (`test-icons.js`, `test-sidebar-icons.js`, `test-style-palette.js`, `test-style-colorblind-shapes.js`, `test-notifications-palette.js`, `test-renderer-connection-banner.js`, `test-renderer-subagent-glyphs.js`, `test-renderer-format-plan-glyphs.js`, `test-renderer-needsyou-affordances.js`, `test-notifications-loudscope.js`, `test-settings-loudscope.js`).

- [ ] **Step 5: Re-grep for conflict markers (repo process, post-any-merge hygiene)**

Run: `grep -rn '^<<<<<<<\|^=======$\|^>>>>>>>' --include='*.go' --include='*.js' --include='*.css' --include='*.html' --include='*.md' . 2>/dev/null`
Expected: no output.

- [ ] **Step 6: `go vet` every touched package (catches what `go build` alone misses in test files)**

Run: `go vet ./hubapi/... ./cmd/serf-hub/... ./cmd/serf-tui/... ./server/... ./cmd/serf/... ./appwire/...`
Expected: no output.

This task has no commit of its own — it is the readiness gate before merging Track A per the design spec's merge order (Track 0 → **Track A** → B/C → D).
