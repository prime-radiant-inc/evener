# Serf TUI Deep UX Pass — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Port the workshop-log identity from the web UI to the serf-tui — state-colored row bars, typographic status badges, per-tool renderer registry with diff color bars and rich bodies, unified overlays with focus traps, composer chip strip + persistent statusbar — driven by a configurable theme registry covering dark + light at parity.

**Architecture:** Wave-by-wave migration on branch `tui-ux-pass`. Wave 1 establishes the `Theme` struct + token registry + golden-corpus theme isolation. Wave 2 ships rendering primitives (`StateBar`, `StatusBadge`, `SectionDivider`, `KbdHint`, `Overlay`, `DotLeader`). Waves 3–10 adopt the primitives across surfaces (dashboard, session header, tool renderers, composer, overlays, focus traps, MCP fallback). Each wave is independently shippable: build + tests green at every commit, golden snapshots updated inline.

**Tech Stack:** Go 1.x · bubbletea (Update/View model) · lipgloss (styles) · termenv (capability detection) · alecthomas/chroma v2 (syntax highlighting) · charmbracelet/glamour (markdown).

**Reference docs:**
- Spec: `docs/superpowers/specs/2026-05-24-serf-tui-ux-pass-design.md`
- Web design language: `docs/superpowers/specs/2026-05-22-serf-hub-design-language.md`
- Web design system: `docs/superpowers/specs/2026-05-23-serf-hub-design-system.md`

---

## File structure

### New files

| Path | Responsibility |
|------|----------------|
| `cmd/serf-tui/tokens.go` | `Theme` struct, theme registry (`themes` map), `setTheme`, `activeTheme`, helpers like `Tints()`. |
| `cmd/serf-tui/primitives.go` | Rendering primitives: `StateBar`, `FocusedStateBar`, `StatusBadge`, `SectionDivider`, `KbdHint`, `Overlay`, `DotLeader`. |
| `cmd/serf-tui/primitives_test.go` | Unit tests for primitives. |
| `cmd/serf-tui/tool_renderers.go` | The `ToolRenderer` struct + `toolRenderers` map + per-tool renderer functions. |
| `cmd/serf-tui/tool_renderers_test.go` | Unit tests per renderer per state. |
| `cmd/serf-tui/tool_bodies.go` | Body renderers: `diffBody`, `fileBody`, `taskListBody`, `subagentBody`, `shellBody`, `webSearchBody`. |
| `cmd/serf-tui/tool_bodies_test.go` | Body-renderer tests. |
| `cmd/serf-tui/mcp_fallback.go` | MCP/unknown-tool fallback renderers. |
| `cmd/serf-tui/dashboard_render.go` | Dashboard-row/section/footer renderers (extracted from `hub_model.go`). |
| `cmd/serf-tui/session_render.go` | Session header / meta-strip / conversation-row renderers (extracted from `hub_model.go`). |
| `cmd/serf-tui/composer_render.go` | Composer chip-strip + mode-chip renderers (companion to `composer_panel.go`). |
| `cmd/serf-tui/focus_trap.go` | `topmostOverlay` helper + key-routing rules. |
| `cmd/serf-tui/focus_trap_test.go` | Per-overlay trap tests. |

### Modified files

| Path | Why |
|------|-----|
| `cmd/serf-tui/styles.go` | Style globals become getter functions reading from `activeTheme()`. ~20 globals migrated. |
| `cmd/serf-tui/hub_model.go` | Dashboard + session view functions delegate to new render files. |
| `cmd/serf-tui/message.go` | `renderToolCall` consumes the new registry. `markdownRenderer` invalidates on `setTheme`. |
| `cmd/serf-tui/tool_summary.go` | Switch deleted; remaining helpers (`unifiedDiff`, `highlightLine`) retained and exported for `tool_bodies.go`. |
| `cmd/serf-tui/composer_panel.go` | Uses new `composer_render.go` primitives for the chip strip + mode chip. |
| `cmd/serf-tui/statusbar.go` | Rewritten using primitives + ghost-dim chrome + threshold-colored ctx usage. |
| `cmd/serf-tui/model_picker.go` | Adopts `Overlay` primitive. |
| `cmd/serf-tui/theme_picker.go` | Adopts `Overlay` primitive. |
| `cmd/serf-tui/command_palette.go` | Adopts `Overlay` primitive + slash-command item format. |
| `cmd/serf-tui/credentials_panel.go` | Adopts `Overlay` primitive + status badges. |
| `cmd/serf-tui/launch_settings_panel.go` | Adopts `Overlay` primitive. |
| `cmd/serf-tui/launch_overrides_modal.go` | Adopts `Overlay` primitive. |
| `cmd/serf-tui/text_input_modal.go` | Adopts `Overlay` primitive. |
| `cmd/serf-tui/notice_panel.go` | Diagnostic voice (state-colored left bar + key/value lines). |
| `cmd/serf-tui/details_drawer.go` | Section labels + status badge + ghost chrome. |
| `cmd/serf-tui/tui_samples.go` | `tuiSampleRender` gains `Theme string` field. |
| `cmd/serf-tui/tui_samples_test.go` | `runWithTheme` helper; goldens iterate dark+light. |

---

## WAVE 1 — Theme registry & token foundation

Establishes the `Theme` struct, hardcoded `dark` and `light` entries, and the `activeTheme()` accessor. Existing style globals become getter functions. No visible change to end users.

### Task 1.1: Define `Theme` struct

**Files:**
- Create: `cmd/serf-tui/tokens.go`
- Test: `cmd/serf-tui/tokens_test.go`

- [ ] **Step 1: Write the failing test**

```go
// cmd/serf-tui/tokens_test.go
package main

import (
	"testing"
)

func TestThemeRegistryHasDarkAndLight(t *testing.T) {
	registry := Themes()
	if _, ok := registry["dark"]; !ok {
		t.Errorf("missing 'dark' theme")
	}
	if _, ok := registry["light"]; !ok {
		t.Errorf("missing 'light' theme")
	}
}

func TestThemeStructFieldsPopulated(t *testing.T) {
	for name, th := range Themes() {
		if th.Name != name {
			t.Errorf("theme %q has Name=%q", name, th.Name)
		}
		if th.Text == "" {
			t.Errorf("theme %q has empty Text", name)
		}
		if th.Accent == "" {
			t.Errorf("theme %q has empty Accent", name)
		}
		if th.StateAwaiting == "" {
			t.Errorf("theme %q has empty StateAwaiting", name)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-tui -run TestTheme -v`
Expected: FAIL with "Themes undefined"

- [ ] **Step 3: Write minimal implementation**

```go
// cmd/serf-tui/tokens.go
package main

import "github.com/charmbracelet/lipgloss"

type Theme struct {
	Name string

	// Surface
	Bg, BgRaised, SurfaceSecondary lipgloss.Color
	Rule, RuleSoft                 lipgloss.Color

	// Text tier
	Text, TextMuted, TextDim, TextGhost lipgloss.Color

	// Brand + state
	Accent, AccentSecondary lipgloss.Color
	StateAwaiting, StateProcessing,
	StateWarning, StateIdle, StateEnded,
	StateSubagent lipgloss.Color
	BtnPrimaryText lipgloss.Color

	// Tints (precomputed bg fills)
	StateAwaitingTint, StateProcessingTint,
	StateWarningTint, StateIdleTint,
	AccentTint lipgloss.Color

	// Layout
	IndentToolBody, IndentSubagent int
	GapTurn, GapSection            int
	ColumnDur                      int
	LeftBarGlyph, RuleGlyph        string
}

var themeRegistry = map[string]Theme{
	"dark":  darkThemeV2,
	"light": lightThemeV2,
}

func Themes() map[string]Theme {
	return themeRegistry
}

var darkThemeV2 = Theme{
	Name:                "dark",
	Bg:                  lipgloss.Color("#0a0a0e"),
	BgRaised:            lipgloss.Color("#16161e"),
	SurfaceSecondary:    lipgloss.Color("#1c1c24"),
	Rule:                lipgloss.Color("#1a1a20"),
	RuleSoft:            lipgloss.Color("#14141a"),
	Text:                lipgloss.Color("#ececf0"),
	TextMuted:           lipgloss.Color("#7a7a86"),
	TextDim:             lipgloss.Color("#5a5a64"),
	TextGhost:           lipgloss.Color("#2a2a30"),
	Accent:              lipgloss.Color("#7aa2f7"),
	AccentSecondary:     lipgloss.Color("#bb9af7"),
	StateAwaiting:       lipgloss.Color("#f7768e"),
	StateProcessing:     lipgloss.Color("#7aa2f7"),
	StateWarning:        lipgloss.Color("#e0af68"),
	StateIdle:           lipgloss.Color("#9ece6a"),
	StateEnded:          lipgloss.Color("#3a3a44"),
	StateSubagent:       lipgloss.Color("#bb9af7"),
	BtnPrimaryText:      lipgloss.Color("#0a0a0e"),
	StateAwaitingTint:   lipgloss.Color("#28171b"),
	StateProcessingTint: lipgloss.Color("#161e2c"),
	StateWarningTint:    lipgloss.Color("#26201a"),
	StateIdleTint:       lipgloss.Color("#181f17"),
	AccentTint:          lipgloss.Color("#16192c"),
	IndentToolBody:      4,
	IndentSubagent:      2,
	GapTurn:             1,
	GapSection:          2,
	ColumnDur:           8,
	LeftBarGlyph:        "▍",
	RuleGlyph:           "┄",
}

var lightThemeV2 = Theme{
	Name:                "light",
	Bg:                  lipgloss.Color("#fafafa"),
	BgRaised:            lipgloss.Color("#f1f1f2"),
	SurfaceSecondary:    lipgloss.Color("#e6e6e8"),
	Rule:                lipgloss.Color("#dadadc"),
	RuleSoft:            lipgloss.Color("#e6e6e8"),
	Text:                lipgloss.Color("#16161e"),
	TextMuted:           lipgloss.Color("#5e5e6a"),
	TextDim:             lipgloss.Color("#8a8a92"),
	TextGhost:           lipgloss.Color("#c8c8cc"),
	Accent:              lipgloss.Color("#2e58b8"),
	AccentSecondary:     lipgloss.Color("#5e35b6"),
	StateAwaiting:       lipgloss.Color("#b62a48"),
	StateProcessing:     lipgloss.Color("#2e58b8"),
	StateWarning:        lipgloss.Color("#8a5a14"),
	StateIdle:           lipgloss.Color("#336a14"),
	StateEnded:          lipgloss.Color("#7a7a82"),
	StateSubagent:       lipgloss.Color("#5e35b6"),
	BtnPrimaryText:      lipgloss.Color("#fafafa"),
	StateAwaitingTint:   lipgloss.Color("#f6e8eb"),
	StateProcessingTint: lipgloss.Color("#e8edf6"),
	StateWarningTint:    lipgloss.Color("#f5efe1"),
	StateIdleTint:       lipgloss.Color("#e8efe1"),
	AccentTint:          lipgloss.Color("#e8edf6"),
	IndentToolBody:      4,
	IndentSubagent:      2,
	GapTurn:             1,
	GapSection:          2,
	ColumnDur:           8,
	LeftBarGlyph:        "▍",
	RuleGlyph:           "┄",
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/serf-tui -run TestTheme -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-tui/tokens.go cmd/serf-tui/tokens_test.go
git commit -m "feat(tui): add Theme struct and dark+light registry (wave 1 task 1.1)"
```

### Task 1.2: `activeTheme()` + `setTheme()` accessors with markdown-renderer invalidation hook

**Files:**
- Modify: `cmd/serf-tui/tokens.go`
- Test: `cmd/serf-tui/tokens_test.go`

- [ ] **Step 1: Write the failing test**

```go
// append to cmd/serf-tui/tokens_test.go

func TestSetThemeChangesActiveTheme(t *testing.T) {
	t.Cleanup(func() { setThemeV2("dark") })

	setThemeV2("dark")
	if activeThemeV2().Name != "dark" {
		t.Errorf("expected dark, got %q", activeThemeV2().Name)
	}
	setThemeV2("light")
	if activeThemeV2().Name != "light" {
		t.Errorf("expected light, got %q", activeThemeV2().Name)
	}
}

func TestSetThemeIgnoresUnknown(t *testing.T) {
	t.Cleanup(func() { setThemeV2("dark") })
	setThemeV2("dark")
	ok := setThemeV2("nonexistent")
	if ok {
		t.Errorf("setThemeV2 should return false for unknown name")
	}
	if activeThemeV2().Name != "dark" {
		t.Errorf("unknown name should not change active theme")
	}
}

func TestSetThemeCallsMarkdownInvalidator(t *testing.T) {
	t.Cleanup(func() {
		setThemeV2("dark")
		markdownInvalidationCount = 0
	})
	markdownInvalidationCount = 0
	setThemeV2("light")
	if markdownInvalidationCount != 1 {
		t.Errorf("expected 1 invalidation, got %d", markdownInvalidationCount)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-tui -run TestSetTheme -v`
Expected: FAIL with "activeThemeV2 undefined"

- [ ] **Step 3: Write minimal implementation**

```go
// append to cmd/serf-tui/tokens.go

var activeThemeName = "dark"

// markdownInvalidationCount is a test hook counting invalidator calls.
var markdownInvalidationCount int

// markdownInvalidator is set by message.go init; it nils out the cached
// glamour renderer so the next render rebuilds with the new theme.
// We use an indirection (not direct call) so tokens.go has no dep on message.go.
var markdownInvalidator = func() { markdownInvalidationCount++ }

func activeThemeV2() Theme {
	if th, ok := themeRegistry[activeThemeName]; ok {
		return th
	}
	return themeRegistry["dark"]
}

// setThemeV2 swaps the active theme. Not safe for concurrent use; intended
// to be called only from bubbletea's main Update goroutine.
func setThemeV2(name string) bool {
	if _, ok := themeRegistry[name]; !ok {
		return false
	}
	activeThemeName = name
	markdownInvalidator()
	return true
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/serf-tui -run TestSetTheme -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-tui/tokens.go cmd/serf-tui/tokens_test.go
git commit -m "feat(tui): add activeTheme + setTheme with markdown invalidator hook (wave 1 task 1.2)"
```

### Task 1.3: Wire markdownInvalidator from message.go

**Files:**
- Modify: `cmd/serf-tui/message.go`
- Test: `cmd/serf-tui/tokens_test.go`

- [ ] **Step 1: Write the failing test**

```go
// append to cmd/serf-tui/tokens_test.go

func TestMarkdownInvalidatorIsWired(t *testing.T) {
	// After init, markdownInvalidator must NOT be the placeholder counter only.
	// We assert this indirectly: switching theme should reset markdownRenderer.
	t.Cleanup(func() { setThemeV2("dark") })

	// Force markdown renderer to be created.
	_ = renderMarkdown("hello", 40)
	if markdownRendererCached() == nil {
		t.Fatalf("renderMarkdown did not populate cache")
	}

	setThemeV2("light")
	if markdownRendererCached() != nil {
		t.Errorf("setThemeV2 should have invalidated markdownRenderer cache")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-tui -run TestMarkdownInvalidatorIsWired -v`
Expected: FAIL with "markdownRendererCached undefined" or similar

- [ ] **Step 3: Find current markdownRenderer caching code**

Run: `grep -n "markdownRenderer\|glamour" cmd/serf-tui/message.go`

You should see a package-level `markdownRenderer` var and `renderMarkdown` function. Expose a test helper and wire the invalidator.

- [ ] **Step 4: Modify `cmd/serf-tui/message.go`**

Add near the top of `message.go` (after imports):

```go
// markdownRendererCached returns the current cache value; nil means not built yet.
// Test helper for tokens_test.go.
func markdownRendererCached() *glamour.TermRenderer {
	return markdownRenderer
}

func resetMarkdownRenderer() {
	markdownRenderer = nil
}

func init() {
	markdownInvalidator = resetMarkdownRenderer
}
```

(Use whatever the actual current type of `markdownRenderer` is — adjust the return type. If it's `interface{}` or similar, mirror that.)

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./cmd/serf-tui -run TestMarkdownInvalidator -v`
Expected: PASS

- [ ] **Step 6: Run the whole test suite to confirm no regression**

Run: `go test ./cmd/serf-tui/...`
Expected: PASS (matching the current baseline)

- [ ] **Step 7: Commit**

```bash
git add cmd/serf-tui/message.go cmd/serf-tui/tokens_test.go
git commit -m "feat(tui): wire markdown renderer invalidator from setTheme (wave 1 task 1.3)"
```

### Task 1.4: Bridge `applyTheme(colorTheme)` and existing `setTheme(name)` to new registry

The existing `setTheme(name)` in `styles.go` mutates a different theme struct (`activeTheme colorTheme`). Bridge so that the existing function ALSO updates the new registry.

**Files:**
- Modify: `cmd/serf-tui/styles.go`
- Test: `cmd/serf-tui/tokens_test.go`

- [ ] **Step 1: Write the failing test**

```go
// append to cmd/serf-tui/tokens_test.go

func TestLegacySetThemeAlsoUpdatesV2(t *testing.T) {
	t.Cleanup(func() {
		setTheme("dark")
		setThemeV2("dark")
	})
	setTheme("light")
	if activeThemeV2().Name != "light" {
		t.Errorf("legacy setTheme(light) did not update v2 active theme")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-tui -run TestLegacySetThemeAlsoUpdatesV2 -v`
Expected: FAIL (legacy `setTheme` does not touch the new registry)

- [ ] **Step 3: Modify `cmd/serf-tui/styles.go`**

Find the existing `setTheme(name string)` function. At the bottom, before the final `return true`, add:

```go
	// Bridge to v2 theme registry: keep the new tokens in sync until wave 5
	// finishes the cutover.
	setThemeV2(name)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/serf-tui -run TestLegacySetThemeAlsoUpdatesV2 -v`
Expected: PASS

- [ ] **Step 5: Run full suite**

Run: `go test ./cmd/serf-tui/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-tui/styles.go cmd/serf-tui/tokens_test.go
git commit -m "feat(tui): bridge legacy setTheme to new theme registry (wave 1 task 1.4)"
```

### Task 1.5: Token-isolation tests

Verify no token is empty + no value collides obviously (`Bg != Text`).

**Files:**
- Test: `cmd/serf-tui/tokens_test.go`

- [ ] **Step 1: Write the failing test**

```go
// append to cmd/serf-tui/tokens_test.go

func TestNoTokenIsEmpty(t *testing.T) {
	for name, th := range Themes() {
		fields := map[string]lipgloss.Color{
			"Bg":               th.Bg,
			"BgRaised":         th.BgRaised,
			"SurfaceSecondary": th.SurfaceSecondary,
			"Rule":             th.Rule,
			"RuleSoft":         th.RuleSoft,
			"Text":             th.Text,
			"TextMuted":        th.TextMuted,
			"TextDim":          th.TextDim,
			"TextGhost":        th.TextGhost,
			"Accent":           th.Accent,
			"AccentSecondary":  th.AccentSecondary,
			"StateAwaiting":    th.StateAwaiting,
			"StateProcessing":  th.StateProcessing,
			"StateWarning":     th.StateWarning,
			"StateIdle":        th.StateIdle,
			"StateEnded":       th.StateEnded,
			"StateSubagent":    th.StateSubagent,
			"BtnPrimaryText":   th.BtnPrimaryText,
		}
		for field, c := range fields {
			if string(c) == "" {
				t.Errorf("theme %q field %q is empty", name, field)
			}
		}
	}
}

func TestBgNotEqualText(t *testing.T) {
	for name, th := range Themes() {
		if string(th.Bg) == string(th.Text) {
			t.Errorf("theme %q: Bg == Text (%q); content invisible", name, th.Bg)
		}
	}
}
```

- [ ] **Step 2: Add the import (top of file)**

```go
import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)
```

- [ ] **Step 3: Run tests**

Run: `go test ./cmd/serf-tui -run "TestNoTokenIsEmpty|TestBgNotEqualText" -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-tui/tokens_test.go
git commit -m "test(tui): add token-isolation tests (wave 1 task 1.5)"
```

---

## WAVE 1.5 — Golden corpus theme isolation

Adds `Theme string` field to `tuiSampleRender`, the `runWithTheme` helper, and reruns existing golden specs under both dark + light.

### Task 1.5.1: Add `Theme string` field to `tuiSampleRender`

**Files:**
- Modify: `cmd/serf-tui/tui_samples.go`
- Modify: `cmd/serf-tui/tui_samples_test.go`

- [ ] **Step 1: Find the current struct**

Run: `grep -n "type tuiSampleRender struct" cmd/serf-tui/tui_samples.go`

You should see a 4-field struct: `Name`, `Width`, `View`, `Contains`.

- [ ] **Step 2: Modify the struct**

```go
// cmd/serf-tui/tui_samples.go
type tuiSampleRender struct {
	Name     string
	Theme    string // new field: "dark" or "light"; empty defaults to dark
	Width    int
	View     string
	Contains []string
}
```

- [ ] **Step 3: Update all `renderSample` constructors** to default `Theme: "dark"`

Find `func renderSample(...)`:

```go
func renderSample(name string, width int, view string, contains ...string) tuiSampleRender {
	return tuiSampleRender{
		Name:     name,
		Theme:    "dark",
		Width:    width,
		View:     view,
		Contains: contains,
	}
}
```

- [ ] **Step 4: Run build**

Run: `go build ./cmd/serf-tui/...`
Expected: success (the struct addition + default-value population should not break callers).

- [ ] **Step 5: Run tests**

Run: `go test ./cmd/serf-tui/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-tui/tui_samples.go
git commit -m "test(tui): add Theme field to tuiSampleRender (wave 1.5 task 1.5.1)"
```

### Task 1.5.2: Add `runWithTheme` helper + theme baseline tests

**Files:**
- Modify: `cmd/serf-tui/tui_samples_test.go`

- [ ] **Step 1: Add the helper**

```go
// near the top of cmd/serf-tui/tui_samples_test.go

// runWithTheme switches the active theme for the duration of body() and
// restores it afterward. NOT safe for parallel tests — theme is a global.
func runWithTheme(t *testing.T, name string, body func()) {
	t.Helper()
	prev := currentThemeName()
	if !setTheme(name) {
		t.Fatalf("unknown theme %q", name)
	}
	defer setTheme(prev)
	body()
}
```

- [ ] **Step 2: Write a baseline test that confirms both themes render**

```go
// append to cmd/serf-tui/tui_samples_test.go

func TestSampleRenders_EachThemeProducesNonEmptyView(t *testing.T) {
	corpus := newHubTUISampleCorpus()
	for _, theme := range []string{"dark", "light"} {
		runWithTheme(t, theme, func() {
			for _, sample := range corpus.Renders {
				if sample.View == "" {
					t.Errorf("theme=%s sample=%s: empty View", theme, sample.Name)
				}
			}
		})
	}
}
```

- [ ] **Step 3: Run test**

Run: `go test ./cmd/serf-tui -run TestSampleRenders_EachTheme -v`
Expected: PASS (because the corpus is statically built, the same View string is returned; we'll re-generate per-theme in a later step).

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-tui/tui_samples_test.go
git commit -m "test(tui): add runWithTheme helper + baseline coverage (wave 1.5 task 1.5.2)"
```

---

## WAVE 2 — Primitives

Ships `StateBar`, `FocusedStateBar`, `StatusBadge`, `SectionDivider`, `KbdHint`, `Overlay`, `DotLeader`. Unused by other code; published for adoption in waves 3+.

### Task 2.1: `StateBar` + `FocusedStateBar`

**Files:**
- Create: `cmd/serf-tui/primitives.go`
- Create: `cmd/serf-tui/primitives_test.go`

- [ ] **Step 1: Write the failing test**

```go
// cmd/serf-tui/primitives_test.go
package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestStateBarReturnsSingleGlyph(t *testing.T) {
	bar := StateBar(lipgloss.Color("#7aa2f7"))
	if !strings.Contains(bar, "▍") {
		t.Errorf("StateBar missing left-bar glyph: %q", bar)
	}
}

func TestFocusedStateBarReturnsDoubleGlyph(t *testing.T) {
	bar := FocusedStateBar(lipgloss.Color("#7aa2f7"))
	// Two of the same glyph.
	if strings.Count(bar, "▍") != 2 {
		t.Errorf("FocusedStateBar should contain two ▍ glyphs; got %q", bar)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-tui -run TestStateBar -v`
Expected: FAIL with "StateBar undefined"

- [ ] **Step 3: Write minimal implementation**

```go
// cmd/serf-tui/primitives.go
package main

import "github.com/charmbracelet/lipgloss"

// StateBar returns a single 1-column glyph foreground-colored to state.
func StateBar(state lipgloss.Color) string {
	return lipgloss.NewStyle().Foreground(state).Render(activeThemeV2().LeftBarGlyph)
}

// FocusedStateBar returns the same glyph twice for selected/focused rows.
// Total visual width is 2 columns; callers must account for this in their
// right-alignment math.
func FocusedStateBar(state lipgloss.Color) string {
	g := activeThemeV2().LeftBarGlyph
	return lipgloss.NewStyle().Foreground(state).Render(g + g)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/serf-tui -run TestStateBar -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-tui/primitives.go cmd/serf-tui/primitives_test.go
git commit -m "feat(tui): add StateBar + FocusedStateBar primitives (wave 2 task 2.1)"
```

### Task 2.2: `StatusBadge`

**Files:**
- Modify: `cmd/serf-tui/primitives.go`
- Modify: `cmd/serf-tui/primitives_test.go`

- [ ] **Step 1: Write the failing test**

```go
// append to cmd/serf-tui/primitives_test.go

func TestStatusBadgeContainsLabelAndDot(t *testing.T) {
	out := StatusBadge(lipgloss.Color("#f7768e"), "AWAITING")
	if !strings.Contains(out, "●") {
		t.Errorf("StatusBadge missing dot: %q", out)
	}
	if !strings.Contains(out, "AWAITING") {
		t.Errorf("StatusBadge missing label: %q", out)
	}
}

func TestStatusBadgeIsBoldUppercase(t *testing.T) {
	out := StatusBadge(lipgloss.Color("#f7768e"), "awaiting")
	// lipgloss bold renders ANSI escape \x1b[1m
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("StatusBadge should have ANSI styling: %q", out)
	}
	// Uppercase
	if !strings.Contains(out, "AWAITING") {
		t.Errorf("StatusBadge should upper-case label; got %q", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-tui -run TestStatusBadge -v`
Expected: FAIL with "StatusBadge undefined"

- [ ] **Step 3: Add to `cmd/serf-tui/primitives.go`**

```go
import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// StatusBadge renders a reverse-pill: bold uppercase label with a leading
// dot, foreground in state color, on the theme's Bg.
func StatusBadge(state lipgloss.Color, label string) string {
	upper := strings.ToUpper(label)
	return lipgloss.NewStyle().
		Foreground(state).
		Bold(true).
		Render("● " + upper)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/serf-tui -run TestStatusBadge -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-tui/primitives.go cmd/serf-tui/primitives_test.go
git commit -m "feat(tui): add StatusBadge primitive (wave 2 task 2.2)"
```

### Task 2.3: `SectionDivider`

**Files:**
- Modify: `cmd/serf-tui/primitives.go`
- Modify: `cmd/serf-tui/primitives_test.go`

- [ ] **Step 1: Write the failing test**

```go
// append to cmd/serf-tui/primitives_test.go

func TestSectionDividerEmitsLeftRight(t *testing.T) {
	out := SectionDivider(60, "SERF / SESSION", "12 turns")
	if !strings.Contains(out, "SERF / SESSION") {
		t.Errorf("SectionDivider missing left label: %q", out)
	}
	if !strings.Contains(out, "12 turns") {
		t.Errorf("SectionDivider missing right label: %q", out)
	}
}

func TestSectionDividerUsesRuleGlyphs(t *testing.T) {
	out := SectionDivider(60, "X", "Y")
	if !strings.Contains(out, "─") {
		t.Errorf("SectionDivider missing fill glyph ─: %q", out)
	}
	if !strings.Contains(out, "┄") {
		t.Errorf("SectionDivider missing trailing ┄ glyph: %q", out)
	}
}

func TestSectionDividerTruncatesAtNarrowWidth(t *testing.T) {
	// At width 20, the labels alone exceed it. Output must not be longer than ~20 visible chars.
	out := SectionDivider(20, "VERY LONG LEFT", "VERY LONG RIGHT")
	visible := lipgloss.Width(out)
	if visible > 25 {
		t.Errorf("SectionDivider too wide at narrow width; got width %d", visible)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-tui -run TestSectionDivider -v`
Expected: FAIL with "SectionDivider undefined"

- [ ] **Step 3: Add to `cmd/serf-tui/primitives.go`**

```go
// SectionDivider renders "─ LEFT ──…────── RIGHT ┄" filling middle with
// theme.Rule, label tone via theme.TextDim, trailing ┄ in theme.Rule.
func SectionDivider(width int, left, right string) string {
	th := activeThemeV2()
	if width <= 0 {
		width = 60
	}
	leftStyled := lipgloss.NewStyle().Foreground(th.TextDim).Bold(true).Render(strings.ToUpper(left))
	rightStyled := lipgloss.NewStyle().Foreground(th.TextGhost).Render(right)
	leadGlyph := lipgloss.NewStyle().Foreground(th.RuleSoft).Render("─ ")
	trailGlyph := lipgloss.NewStyle().Foreground(th.Rule).Render(" " + th.RuleGlyph)

	prefix := leadGlyph + leftStyled
	suffix := rightStyled + trailGlyph
	prefixW := lipgloss.Width(prefix)
	suffixW := lipgloss.Width(suffix)
	fill := width - prefixW - suffixW - 2 // 2 spaces around labels
	if fill < 1 {
		return prefix + " " + suffix // narrow fallback
	}
	mid := lipgloss.NewStyle().Foreground(th.RuleSoft).Render(strings.Repeat("─", fill))
	return prefix + " " + mid + " " + suffix
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/serf-tui -run TestSectionDivider -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-tui/primitives.go cmd/serf-tui/primitives_test.go
git commit -m "feat(tui): add SectionDivider primitive (wave 2 task 2.3)"
```

### Task 2.4: `KbdHint` + `DotLeader`

**Files:**
- Modify: `cmd/serf-tui/primitives.go`
- Modify: `cmd/serf-tui/primitives_test.go`

- [ ] **Step 1: Write the failing test**

```go
// append to cmd/serf-tui/primitives_test.go

func TestKbdHintFormatsKeyAndAction(t *testing.T) {
	out := KbdHint("enter", "send")
	if !strings.Contains(out, "enter") {
		t.Errorf("KbdHint missing key: %q", out)
	}
	if !strings.Contains(out, "send") {
		t.Errorf("KbdHint missing action: %q", out)
	}
}

func TestDotLeaderFillsMiddle(t *testing.T) {
	out := DotLeader("read", "12 lines", 50)
	if !strings.Contains(out, "·") {
		t.Errorf("DotLeader missing fill char ·: %q", out)
	}
	if !strings.Contains(out, "read") || !strings.Contains(out, "12 lines") {
		t.Errorf("DotLeader missing label or result: %q", out)
	}
	if lipgloss.Width(out) != 50 {
		t.Errorf("DotLeader should equal target width 50, got %d", lipgloss.Width(out))
	}
}

func TestDotLeaderHandlesOverflow(t *testing.T) {
	// If target+result > width, no dots; just truncate.
	out := DotLeader("verylongverb", "and result text here", 10)
	if lipgloss.Width(out) > 10 {
		t.Errorf("DotLeader exceeded width on overflow: width=%d", lipgloss.Width(out))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-tui -run "TestKbdHint|TestDotLeader" -v`
Expected: FAIL with "KbdHint undefined"

- [ ] **Step 3: Add to `cmd/serf-tui/primitives.go`**

```go
// KbdHint renders "<reverse-key> action" — key in reverse video,
// action in TextDim.
func KbdHint(key, action string) string {
	th := activeThemeV2()
	keyStyled := lipgloss.NewStyle().
		Reverse(true).
		Foreground(th.Text).
		Padding(0, 1).
		Render(key)
	actionStyled := lipgloss.NewStyle().Foreground(th.TextDim).Render(action)
	return keyStyled + " " + actionStyled
}

// DotLeader returns "left ········ right" exactly `width` columns wide
// (best-effort). Dots are TextGhost color. Result + dur appear as one
// right-aligned block — caller supplies the entire right text.
func DotLeader(left, right string, width int) string {
	th := activeThemeV2()
	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	if width <= 0 {
		return left + " " + right
	}
	fill := width - lw - rw - 2 // one space each side of dots
	if fill < 1 {
		// Truncate left first
		maxLeft := width - rw - 2
		if maxLeft < 1 {
			// Even right alone overflows; truncate right
			return truncateText(right, width)
		}
		return truncateText(left, maxLeft) + "  " + right
	}
	dots := lipgloss.NewStyle().Foreground(th.TextGhost).Render(strings.Repeat("·", fill))
	return left + " " + dots + " " + right
}
```

(If `truncateText` does not exist yet in the package, this code assumes it does — it does, in `hub_model.go`. Confirm by `grep -n "func truncateText" cmd/serf-tui/*.go`.)

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/serf-tui -run "TestKbdHint|TestDotLeader" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-tui/primitives.go cmd/serf-tui/primitives_test.go
git commit -m "feat(tui): add KbdHint + DotLeader primitives (wave 2 task 2.4)"
```

### Task 2.5: `Overlay` primitive

**Files:**
- Modify: `cmd/serf-tui/primitives.go`
- Modify: `cmd/serf-tui/primitives_test.go`

- [ ] **Step 1: Write the failing test**

```go
// append to cmd/serf-tui/primitives_test.go

func TestOverlayContainsTitleBodyFooter(t *testing.T) {
	out := Overlay(OverlayOpts{
		Title:  "Select model",
		Width:  60,
		Body:   "the body content",
		Footer: "enter select  esc cancel",
	})
	for _, want := range []string{"Select model", "the body content", "enter select  esc cancel"} {
		if !strings.Contains(out, want) {
			t.Errorf("Overlay missing %q in output:\n%s", want, out)
		}
	}
}

func TestOverlayDrawsRoundedBorder(t *testing.T) {
	out := Overlay(OverlayOpts{Title: "X", Width: 40, Body: "body"})
	for _, glyph := range []string{"╭", "╮", "╰", "╯"} {
		if !strings.Contains(out, glyph) {
			t.Errorf("Overlay missing border glyph %q", glyph)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-tui -run TestOverlay -v`
Expected: FAIL with "Overlay undefined"

- [ ] **Step 3: Add to `cmd/serf-tui/primitives.go`**

```go
type OverlayOpts struct {
	Title  string
	Width  int
	Body   string
	Footer string
	Accent lipgloss.Color // optional; defaults to theme.Accent
}

func Overlay(opts OverlayOpts) string {
	th := activeThemeV2()
	accent := opts.Accent
	if accent == "" {
		accent = th.Accent
	}
	if opts.Width <= 0 {
		opts.Width = 80
	}

	border := lipgloss.RoundedBorder()
	frame := lipgloss.NewStyle().
		Border(border).
		BorderForeground(accent).
		Foreground(th.Text).
		Background(th.BgRaised).
		Padding(1, 2).
		Width(opts.Width)

	titleLine := lipgloss.NewStyle().Bold(true).Foreground(accent).Render(opts.Title)
	body := opts.Body
	if opts.Footer != "" {
		body += "\n\n" + lipgloss.NewStyle().Foreground(th.TextDim).Render(opts.Footer)
	}
	content := titleLine + "\n\n" + body
	return frame.Render(content)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/serf-tui -run TestOverlay -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-tui/primitives.go cmd/serf-tui/primitives_test.go
git commit -m "feat(tui): add Overlay primitive (wave 2 task 2.5)"
```

---

## WAVE 3 — Dashboard rewrite

Adopts primitives in the dashboard render path. Drops tree connectors, adds state-color bars, KbdHint footer.

### Task 3.1: Section divider replaces existing dashboardHeader

**Files:**
- Modify: `cmd/serf-tui/hub_model.go`
- Test: `cmd/serf-tui/hub_appshell_test.go`

- [ ] **Step 1: Write the failing test**

```go
// append to cmd/serf-tui/hub_appshell_test.go

func TestDashboardHeaderUsesSectionDivider(t *testing.T) {
	got := dashboardHeader("http://hub.test", 3, 100)
	for _, want := range []string{"SERF LIVE", "http://hub.test", "─", "┄"} {
		if !strings.Contains(got, want) {
			t.Errorf("dashboardHeader missing %q in: %q", want, got)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-tui -run TestDashboardHeaderUsesSectionDivider -v`
Expected: FAIL (existing dashboardHeader does not use ┄)

- [ ] **Step 3: Find current `dashboardHeader`**

Run: `grep -n "func dashboardHeader" cmd/serf-tui/hub_model.go`
(Around line 3400 per spec exploration.)

- [ ] **Step 4: Replace `dashboardHeader`**

```go
func dashboardHeader(hubURL string, liveCount int, width int) string {
	right := fmt.Sprintf("%s · %d live", hubURL, liveCount)
	return SectionDivider(width, "SERF LIVE", right)
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./cmd/serf-tui -run "TestDashboardHeader|TestDashboard" -v`
Expected: PASS for the new test; existing tests may need updating to match the new header format.

- [ ] **Step 6: Update existing tests that expected the old header**

Run: `grep -n "serf live  http://hub.test" cmd/serf-tui/*_test.go`
Update those expectations to match the new SectionDivider output (or assert via `strings.Contains`).

- [ ] **Step 7: Run full suite**

Run: `go test ./cmd/serf-tui/...`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add cmd/serf-tui/hub_model.go cmd/serf-tui/hub_appshell_test.go
git commit -m "feat(tui): dashboard header uses SectionDivider (wave 3 task 3.1)"
```

### Task 3.2: Drop tree connectors `├─`/`└─` from session rows

**Files:**
- Modify: `cmd/serf-tui/hub_model.go`
- Test: `cmd/serf-tui/hub_model_test.go` (or wherever session-row tests live; check with `grep -l "dashboardSessionBranch\|renderDashboardSessionRow" cmd/serf-tui/*_test.go`)

- [ ] **Step 1: Write the failing test**

```go
// in an appropriate test file; e.g. cmd/serf-tui/dashboard_rows_test.go (create if needed)
package main

import (
	"strings"
	"testing"
)

func TestSessionRowsHaveNoTreeConnectors(t *testing.T) {
	rows := []hubRow{
		{kind: hubRowProject, project: "serf", state: "active"},
		{kind: hubRowSession, project: "serf", title: "Test session", state: "active", projectKey: "serf"},
	}
	got := renderDashboardRowsWindow(rows, 1, 80, false, 0)
	for _, bad := range []string{"├─", "└─"} {
		if strings.Contains(got, bad) {
			t.Errorf("renderDashboardRowsWindow should not emit tree connector %q: %q", bad, got)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-tui -run TestSessionRowsHaveNoTreeConnectors -v`
Expected: FAIL (existing code uses `├─`/`└─`)

- [ ] **Step 3: Find `dashboardSessionBranch`**

Run: `grep -n "func dashboardSessionBranch\|func renderDashboardSessionRow" cmd/serf-tui/hub_model.go`

- [ ] **Step 4: Replace `renderDashboardSessionRow` marker**

In `renderDashboardSessionRow`, replace `marker := branch` and the call to `dashboardSessionBranch` with `marker := StateBar(stateColor(row.state))`.

```go
func renderDashboardSessionRow(row hubRow, selected bool, width int, compact bool, _ string) string {
	stateClr := stateColor(row.state)
	marker := StateBar(stateClr)
	if selected {
		marker = FocusedStateBar(activeThemeV2().Accent)
	}
	styles := defaultTUIStyles()
	// ...rest unchanged until the line construction
```

You also need a helper `stateColor(state string) lipgloss.Color`. Add it near the top of `hub_model.go` (or a new `state_color.go`):

```go
func stateColor(state string) lipgloss.Color {
	th := activeThemeV2()
	switch state {
	case "awaiting":
		return th.StateAwaiting
	case "active":
		return th.StateProcessing
	case "warning":
		return th.StateWarning
	case "idle":
		return th.StateIdle
	case "ended":
		return th.StateEnded
	default:
		return th.TextDim
	}
}
```

- [ ] **Step 5: Delete `dashboardSessionBranch`**

It's no longer called. Confirm with `grep dashboardSessionBranch cmd/serf-tui/`. Remove the function and any tests for it.

- [ ] **Step 6: Run tests**

Run: `go test ./cmd/serf-tui -run TestSessionRowsHaveNoTreeConnectors -v`
Expected: PASS

- [ ] **Step 7: Run full suite**

Run: `go test ./cmd/serf-tui/...`
Expected: PASS (golden tests may need updating — accept the new output as authoritative)

- [ ] **Step 8: Commit**

```bash
git add cmd/serf-tui/hub_model.go cmd/serf-tui/dashboard_rows_test.go
git commit -m "feat(tui): drop tree connectors from dashboard rows; use StateBar (wave 3 task 3.2)"
```

### Task 3.3: Dashboard footer uses KbdHint chips

**Files:**
- Modify: `cmd/serf-tui/hub_model.go`
- Test: `cmd/serf-tui/hub_appshell_test.go`

- [ ] **Step 1: Write the failing test**

```go
// append to cmd/serf-tui/hub_appshell_test.go

func TestDashboardFooterContainsKbdHintChrome(t *testing.T) {
	got := dashboardFooter(100)
	// KbdHint uses reverse video; ANSI escape \x1b[7m signals reverse.
	if !strings.Contains(got, "\x1b[7") && !strings.Contains(got, "\x1b[") {
		t.Errorf("dashboardFooter should style kbd tokens with reverse: %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-tui -run TestDashboardFooterContainsKbdHintChrome -v`
Expected: FAIL (existing footer is plain text)

- [ ] **Step 3: Find and rewrite `dashboardFooter`**

```go
func dashboardFooter(width int) string {
	tokens := []string{
		KbdHint("↑↓", "select"),
		KbdHint("enter", "open"),
		KbdHint("n", "new"),
		KbdHint("/", "filter"),
		KbdHint("⌘O", "dashboard"),
		KbdHint("q", "quit"),
	}
	// actionBarForWidth handles wrap on narrow widths.
	return actionBarForWidth(width, tokens...)
}
```

- [ ] **Step 4: Update other callers of the old footer string**

Run: `grep -n "up/down select\|enter open/toggle" cmd/serf-tui/*.go`
Replace any tests that assert the old plain-text format.

- [ ] **Step 5: Run tests**

Run: `go test ./cmd/serf-tui/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-tui/hub_model.go cmd/serf-tui/hub_appshell_test.go
git commit -m "feat(tui): dashboard footer uses KbdHint chips (wave 3 task 3.3)"
```

### Task 3.4: Dashboard rows show state via row text color (not just dot)

**Files:**
- Modify: `cmd/serf-tui/hub_model.go`

- [ ] **Step 1: Write the failing test**

```go
// append to cmd/serf-tui/dashboard_rows_test.go

func TestSessionRowAwaitingHasStateColor(t *testing.T) {
	row := hubRow{kind: hubRowSession, project: "serf", title: "X", state: "awaiting"}
	got := renderDashboardSessionRow(row, false, 80, false, "")
	// State color is applied to row content. Assert ANSI escape sequence appears.
	// hex #f7768e ~ ANSI 211; we won't lock to the exact code, but DO assert
	// the row is wrapped in *some* foreground style.
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("awaiting row should carry color; got plain: %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-tui -run TestSessionRowAwaitingHasStateColor -v`
Expected: FAIL (rows render plain text + only dot is colored)

- [ ] **Step 3: Tint the row body**

In `renderDashboardSessionRow`, after building the row text but before returning:

```go
	// State tint on the row body. For idle/ended states, no tint (TextDim or default).
	if row.state == "awaiting" || row.state == "active" || row.state == "warning" {
		clr := stateColor(row.state)
		line = lipgloss.NewStyle().Foreground(clr).Render(line)
	}
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/serf-tui -run TestSessionRowAwaitingHasStateColor -v`
Expected: PASS

- [ ] **Step 5: Run full suite**

Run: `go test ./cmd/serf-tui/...`
Expected: PASS (existing tests may need golden updates)

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-tui/hub_model.go cmd/serf-tui/dashboard_rows_test.go
git commit -m "feat(tui): dashboard rows tint by state color (wave 3 task 3.4)"
```

---

## WAVE 4 — Session header & statusbar rewrite

### Task 4.1: 3-line session header with SectionDivider + StatusBadge

**Files:**
- Modify: `cmd/serf-tui/hub_model.go`
- Test: `cmd/serf-tui/hub_appshell_test.go` (new test file `cmd/serf-tui/session_header_test.go` if simpler)

- [ ] **Step 1: Write the failing test**

```go
// cmd/serf-tui/session_header_test.go
package main

import (
	"strings"
	"testing"
)

func TestSessionHeaderHasThreeMainSections(t *testing.T) {
	m := hubModel{
		detail: hubSessionDetail{
			Title:       "Restore hub TUI widgets",
			SessionID:   "01SERF",
			State:       "awaiting",
			SourceLabel: "serf",
			Branch:      "feat/widget",
			Model:       "anthropic/claude-haiku-4-5",
			WorkingDir:  "/home/jesse/git/serf",
			TurnCount:   12,
		},
		hubURL: "http://hub.test",
		width:  100,
	}
	got := strings.Join(m.sessionHeaderLines(), "\n")
	// 1. rule with breadcrumb + turn count
	if !strings.Contains(got, "SERF / SESSION") {
		t.Errorf("missing breadcrumb: %q", got)
	}
	if !strings.Contains(got, "12 turns") {
		t.Errorf("missing turn count: %q", got)
	}
	// 2. title + state badge
	if !strings.Contains(got, "Restore hub TUI widgets") {
		t.Errorf("missing title: %q", got)
	}
	if !strings.Contains(got, "AWAITING") {
		t.Errorf("missing state badge: %q", got)
	}
	// 3. meta strip
	if !strings.Contains(got, "src serf") || !strings.Contains(got, "branch feat/widget") {
		t.Errorf("missing meta strip cells: %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-tui -run TestSessionHeader -v`
Expected: FAIL (existing header is 5-line key:value dump)

- [ ] **Step 3: Find and rewrite `sessionHeaderLines`**

Run: `grep -n "func.*sessionHeaderLines" cmd/serf-tui/hub_model.go`

```go
func (m hubModel) sessionHeaderLines() []string {
	th := activeThemeV2()
	title := firstNonEmptyString(m.detail.Title, m.detail.SessionID, "untitled session")
	state := m.detail.State
	if state == "" {
		state = "idle"
	}

	// Line 1: rule
	rule := SectionDivider(m.sessionHeaderWidth(), "SERF / SESSION", fmt.Sprintf("%d turns", m.detail.TurnCount))

	// Line 2: title + state badge
	badge := StatusBadge(stateColor(state), state)
	titleLine := "  " + lipgloss.NewStyle().Bold(true).Foreground(th.Text).Render(title) + "   " + badge

	// Line 3: meta strip
	parts := []string{}
	addPart := func(key, value string) {
		if value == "" {
			return
		}
		k := lipgloss.NewStyle().Foreground(th.TextDim).Render(key)
		v := lipgloss.NewStyle().Foreground(th.Text).Render(value)
		parts = append(parts, k+" "+v)
	}
	addPart("src", m.detail.SourceLabel)
	addPart("branch", m.detail.Branch)
	addPart("model", abbreviateModel(m.detail.Model)) // existing helper added during web pass
	if m.detail.WorkingDir != "" {
		addPart("", abbreviatePath(m.detail.WorkingDir, 32)) // see Task 4.2 if not yet exists
	}
	sep := lipgloss.NewStyle().Foreground(th.RuleSoft).Render(" · ")
	meta := "  " + strings.Join(parts, sep)

	return []string{rule, titleLine, meta}
}
```

You may need an `abbreviateModel(s string) string` and `abbreviatePath(s string, max int) string`. Check first:

Run: `grep -n "func abbreviateModel\|func abbreviatePath" cmd/serf-tui/*.go`

If `abbreviateModel` does not exist in serf-tui yet (it exists in the web's `spawn.js`), port it as part of this task. Add it to a new `cmd/serf-tui/model_display.go`:

```go
// cmd/serf-tui/model_display.go
package main

import "strings"

func abbreviateModel(id string) string {
	if id == "" {
		return ""
	}
	// Strip known provider prefixes
	for _, prefix := range []string{"anthropic/", "openai/", "google/", "openrouter/", "openai-compatible/"} {
		if strings.HasPrefix(id, prefix) {
			id = id[len(prefix):]
			break
		}
	}
	// Strip trailing -YYYYMMDD date suffix
	if len(id) >= 9 && id[len(id)-9] == '-' {
		tail := id[len(id)-8:]
		allDigits := true
		for _, r := range tail {
			if r < '0' || r > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			id = id[:len(id)-9]
		}
	}
	return id
}

func abbreviatePath(p string, max int) string {
	if len(p) <= max {
		return p
	}
	// Replace $HOME prefix with ~
	if strings.HasPrefix(p, "/home/") {
		if i := strings.IndexByte(p[len("/home/"):], '/'); i >= 0 {
			p = "~" + p[len("/home/")+i:]
		}
	}
	if len(p) <= max {
		return p
	}
	// Middle-truncate
	keep := max - 1
	head := keep / 2
	tail := keep - head
	return p[:head] + "…" + p[len(p)-tail:]
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/serf-tui -run TestSessionHeader -v`
Expected: PASS

- [ ] **Step 5: Run full suite**

Run: `go test ./cmd/serf-tui/...`
Expected: PASS (golden tests for `session-*` will likely fail — accept the new format)

- [ ] **Step 6: Update existing session-related golden samples**

Look at `tui_samples.go` `sampleRenderFromRealWidget`. Re-run with `DUMP_RENDER=session-streaming` to see the new output, then update the corpus to match.

- [ ] **Step 7: Commit**

```bash
git add cmd/serf-tui/hub_model.go cmd/serf-tui/session_header_test.go cmd/serf-tui/model_display.go
git commit -m "feat(tui): rewrite session header with SectionDivider + StatusBadge (wave 4 task 4.1)"
```

### Task 4.2: Persistent statusbar with health dot + ctx threshold colors

**Files:**
- Modify: `cmd/serf-tui/statusbar.go`
- Test: `cmd/serf-tui/statusbar_test.go` (new)

- [ ] **Step 1: Write the failing test**

```go
// cmd/serf-tui/statusbar_test.go
package main

import (
	"strings"
	"testing"
)

func TestStatusBarConnectedShowsGreenDot(t *testing.T) {
	got := renderStatusBar(statusBarInfo{
		Connected: true,
		HubAddr:   "http://hub.test",
		Provider:  "openai",
		Width:     100,
	})
	if !strings.Contains(got, "●") {
		t.Errorf("statusbar missing health dot: %q", got)
	}
	if !strings.Contains(got, "connected") {
		t.Errorf("statusbar missing 'connected' label: %q", got)
	}
}

func TestStatusBarCtxWarningThreshold(t *testing.T) {
	// At 80% usage, color should be StateWarning.
	got := renderStatusBar(statusBarInfo{
		Connected: true,
		HubAddr:   "http://hub.test",
		Provider:  "openai",
		CtxUsed:   160000,
		CtxLimit:  200000,
		Width:     100,
	})
	// Just assert the ctx text is present; color verified visually.
	if !strings.Contains(got, "ctx") {
		t.Errorf("statusbar missing ctx info: %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-tui -run TestStatusBar -v`
Expected: FAIL (renderStatusBar undefined or has different signature)

- [ ] **Step 3: Rewrite `cmd/serf-tui/statusbar.go`**

First check current contents:
Run: `cat cmd/serf-tui/statusbar.go`

Replace with:

```go
package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type statusBarInfo struct {
	Connected bool
	HubAddr   string
	Provider  string
	Queued    int     // inflight LLM requests
	CtxUsed   int     // tokens used
	CtxLimit  int     // window size
	Cost      float64 // dollars; 0 hides
	Version   string  // serf-tui version
	Width     int
}

func renderStatusBar(info statusBarInfo) string {
	th := activeThemeV2()
	parts := []string{}

	// Health dot + label
	healthClr := th.StateAwaiting
	healthLabel := "disconnected"
	if info.Connected {
		healthClr = th.StateIdle
		healthLabel = "connected"
	}
	health := lipgloss.NewStyle().Foreground(healthClr).Bold(true).Render("●") +
		" " + lipgloss.NewStyle().Foreground(th.TextDim).Render(healthLabel)
	parts = append(parts, health)

	// Hub address
	if info.HubAddr != "" {
		parts = append(parts, lipgloss.NewStyle().Foreground(th.TextGhost).Render(info.HubAddr))
	}

	// Provider + queued
	if info.Provider != "" {
		provText := info.Provider
		if info.Queued > 0 {
			provText = fmt.Sprintf("%s %d", info.Provider, info.Queued)
		}
		parts = append(parts, lipgloss.NewStyle().Foreground(th.TextDim).Render(provText))
	}

	// Context usage with threshold colors
	if info.CtxLimit > 0 {
		ratio := float64(info.CtxUsed) / float64(info.CtxLimit)
		ctxClr := th.TextDim
		switch {
		case ratio >= 0.90: // matches existing agent/context_manager compactThreshold
			ctxClr = th.StateAwaiting
		case ratio >= 0.75:
			ctxClr = th.StateWarning
		}
		ctxText := fmt.Sprintf("ctx %s/%s", formatTokenCount(info.CtxUsed), formatTokenCount(info.CtxLimit))
		parts = append(parts, lipgloss.NewStyle().Foreground(ctxClr).Render(ctxText))
	}

	// Cost
	if info.Cost > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(th.TextDim).Render(fmt.Sprintf("$%.2f", info.Cost)))
	}

	left := strings.Join(parts, lipgloss.NewStyle().Foreground(th.RuleSoft).Render(" · "))

	// Version right-aligned
	if info.Version != "" && info.Width > 0 {
		right := lipgloss.NewStyle().Foreground(th.TextGhost).Render(info.Version)
		gap := info.Width - lipgloss.Width(left) - lipgloss.Width(right)
		if gap > 2 {
			return left + strings.Repeat(" ", gap) + right
		}
	}
	return left
}

func formatTokenCount(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%d", n)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/serf-tui -run TestStatusBar -v`
Expected: PASS

- [ ] **Step 5: Wire `renderStatusBar` into `sessionView`**

In `hub_model.go` `sessionView`, after the existing kbd footer, prepend a call to `renderStatusBar(...)`. Hub model needs to track connection state — likely already in `m.connected` or similar; if not, derive from `m.err != nil`.

- [ ] **Step 6: Run full suite**

Run: `go test ./cmd/serf-tui/...`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add cmd/serf-tui/statusbar.go cmd/serf-tui/statusbar_test.go cmd/serf-tui/hub_model.go
git commit -m "feat(tui): persistent statusbar with health dot + ctx threshold colors (wave 4 task 4.2)"
```

### Task 4.3: Conversation rhythm — turn separators, user/assistant left bars

Covers spec §5.2. Adds `┄` divider between turn clusters, user-turn `┃` (Accent) bar, assistant-turn `▍` (session-state) bar.

**Files:**
- Modify: `cmd/serf-tui/message.go` (`renderMessage` per-kind)
- Modify: `cmd/serf-tui/hub_model.go` (`sessionView` adds separator between turns)
- Create: `cmd/serf-tui/conversation_render_test.go`

- [ ] **Step 1: Write the failing test**

```go
// cmd/serf-tui/conversation_render_test.go
package main

import (
	"strings"
	"testing"
)

func TestUserMessageGetsAccentBar(t *testing.T) {
	msg := chatMessage{Kind: msgUser, Text: "Hello"}
	got := renderMessage(msg, 80, false)
	if !strings.Contains(got, "┃") {
		t.Errorf("user message should carry ┃ bar: %q", got)
	}
}

func TestAssistantMessageGetsStateBar(t *testing.T) {
	msg := chatMessage{Kind: msgAssistant, Text: "Working on it"}
	got := renderMessage(msg, 80, false)
	if !strings.Contains(got, "▍") {
		t.Errorf("assistant message should carry ▍ bar: %q", got)
	}
}
```

- [ ] **Step 2: Run test, expect FAIL**

Run: `go test ./cmd/serf-tui -run "TestUserMessage|TestAssistantMessage" -v`

- [ ] **Step 3: Modify `renderMessage`**

In `cmd/serf-tui/message.go`:

```go
func renderMessage(msg chatMessage, width int, focused bool) string {
	th := activeThemeV2()
	switch msg.Kind {
	case msgUser:
		bar := lipgloss.NewStyle().Foreground(th.Accent).Render("┃")
		body := lipgloss.NewStyle().Foreground(th.Text).Render(msg.Text)
		return bar + " > " + body
	case msgAssistant:
		// Initial implementation uses StateProcessing as a default. A
		// follow-up kata will thread the session's current state into
		// renderMessage (signature change) so the bar mirrors the live
		// session state.
		bar := StateBar(th.StateProcessing)
		body := renderMarkdown(strings.TrimSpace(msg.Text), width-2)
		return bar + " " + body
	// ... other cases unchanged ...
	}
	return ""
}
```

(If `chatMessage` lacks a state field, default to `StateProcessing` for now. A follow-up kata can thread session state through.)

- [ ] **Step 4: Add turn separator in `sessionView`**

In `hub_model.go` `sessionView`, between message renders:

```go
	for i, msg := range messages {
		// ... existing render of msg ...
		if i < len(messages)-1 {
			b.WriteString("\n")
			rule := lipgloss.NewStyle().Foreground(activeThemeV2().RuleSoft).Render(strings.Repeat("┄", width))
			b.WriteString(rule)
			b.WriteString("\n")
		}
	}
```

- [ ] **Step 5: Run tests and commit**

Run: `go test ./cmd/serf-tui/...`

```bash
git add cmd/serf-tui/message.go cmd/serf-tui/hub_model.go cmd/serf-tui/conversation_render_test.go
git commit -m "feat(tui): turn separators + user/assistant left bars (wave 4 task 4.3)"
```

### Task 4.4: Fork mode + scroll-browse polish

Covers spec §5.3 (selected turn double-bar) and §5.4 (fork-draft section divider).

**Files:**
- Modify: `cmd/serf-tui/hub_model.go`

- [ ] **Step 1: Write the failing test**

```go
// append to conversation_render_test.go

func TestScrollBrowseFocusedTurnHasDoubleBar(t *testing.T) {
	msg := chatMessage{Kind: msgUser, Text: "X"}
	got := renderMessage(msg, 80, true) // focused=true
	// FocusedStateBar renders two ┃ glyphs.
	if strings.Count(got, "┃") < 2 {
		t.Errorf("focused user turn should have double-bar: %q", got)
	}
}

func TestForkDraftHasSectionDivider(t *testing.T) {
	header := forkDraftHeader("feat/widget", 1, 80)
	if !strings.Contains(header, "fork draft") || !strings.Contains(header, "feat/widget") {
		t.Errorf("fork draft header missing pieces: %q", header)
	}
	if !strings.Contains(header, "─") {
		t.Errorf("fork draft header should use SectionDivider: %q", header)
	}
}
```

- [ ] **Step 2: Run test, expect FAIL**

- [ ] **Step 3: Implement**

In `renderMessage`:

```go
	case msgUser:
		barClr := th.Accent
		bar := lipgloss.NewStyle().Foreground(barClr).Render("┃")
		if focused {
			bar = lipgloss.NewStyle().Foreground(barClr).Render("┃┃")
		}
		// ...
```

Add helper to `hub_model.go`:

```go
func forkDraftHeader(branch string, divergeTurn int, width int) string {
	right := fmt.Sprintf("%s@diverge:%d", branch, divergeTurn)
	return SectionDivider(width, "fork draft", right)
}
```

Use `forkDraftHeader` where the existing fork-mode UI surfaces (find with `grep -n "fork draft\|fork:" cmd/serf-tui/hub_model.go composer_panel.go`).

- [ ] **Step 4: Run tests and commit**

```bash
git add cmd/serf-tui/message.go cmd/serf-tui/hub_model.go cmd/serf-tui/conversation_render_test.go
git commit -m "feat(tui): scroll-browse double-bar + fork-draft section divider (wave 4 task 4.4)"
```

---

## WAVE 5 — Tool renderer registry foundation

### Task 5.1: Define `ToolRenderer` struct + registry skeleton

**Files:**
- Create: `cmd/serf-tui/tool_renderers.go`
- Create: `cmd/serf-tui/tool_renderers_test.go`

- [ ] **Step 1: Write the failing test**

```go
// cmd/serf-tui/tool_renderers_test.go
package main

import (
	"testing"
	"time"
)

func TestRendererRegistryHasReadFile(t *testing.T) {
	r, ok := lookupToolRenderer("read_file")
	if !ok {
		t.Fatalf("no renderer for read_file")
	}
	args := toolArgsFromJSON(`{"file_path":"handlers/signup.go"}`)
	if r.Verb(args) != "read" {
		t.Errorf("read_file verb = %q; want 'read'", r.Verb(args))
	}
	if r.Target(args) != "handlers/signup.go" {
		t.Errorf("read_file target = %q; want 'handlers/signup.go'", r.Target(args))
	}
}

func TestRendererRegistryFallback(t *testing.T) {
	// Unknown tool gets fallback renderer.
	r, ok := lookupToolRenderer("totally_unknown_tool")
	if !ok {
		t.Fatalf("no fallback renderer")
	}
	args := toolArgsFromJSON(`{"x":"y"}`)
	if r.Verb(args) == "" {
		t.Errorf("fallback verb is empty")
	}
}

func TestRendererRegistryMCPFallback(t *testing.T) {
	r, ok := lookupToolRenderer("linear__search")
	if !ok {
		t.Fatalf("no MCP fallback renderer")
	}
	args := toolArgsFromJSON(`{"query":"foo"}`)
	if r.Verb(args) != "linear" {
		t.Errorf("MCP verb = %q; want 'linear'", r.Verb(args))
	}
	_ = time.Second // satisfy unused import on dev iteration
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-tui -run TestRendererRegistry -v`
Expected: FAIL (ToolRenderer + lookupToolRenderer undefined)

- [ ] **Step 3: Write minimal implementation**

```go
// cmd/serf-tui/tool_renderers.go
package main

import (
	"encoding/json"
	"strings"
	"time"
)

type ToolArgs map[string]any

func toolArgsFromJSON(s string) ToolArgs {
	if s == "" {
		return ToolArgs{}
	}
	var args ToolArgs
	if err := json.Unmarshal([]byte(s), &args); err != nil {
		return ToolArgs{}
	}
	return args
}

func (a ToolArgs) Str(key string) string {
	if v, ok := a[key].(string); ok {
		return v
	}
	return ""
}

type ToolRenderer struct {
	Verb              func(args ToolArgs) string
	Target            func(args ToolArgs) string
	Result            func(args ToolArgs, output, errStr string, dur time.Duration) string
	Body              func(args ToolArgs, output string, w int) string
	ExpandedByDefault bool
}

var toolRenderers = map[string]ToolRenderer{}

// lookupToolRenderer returns the renderer for tool, falling back to
// MCP-style (provider__operation) handling, then a generic unknown-tool
// renderer. Always returns ok=true (the fallback is the last resort).
func lookupToolRenderer(tool string) (ToolRenderer, bool) {
	if r, ok := toolRenderers[tool]; ok {
		return r, true
	}
	if strings.Contains(tool, "__") {
		return mcpFallbackRenderer(tool), true
	}
	return unknownToolRenderer(tool), true
}

func mcpFallbackRenderer(tool string) ToolRenderer {
	provider, op, _ := strings.Cut(tool, "__")
	return ToolRenderer{
		Verb:   func(_ ToolArgs) string { return provider },
		Target: func(args ToolArgs) string { return op },
		Result: func(_ ToolArgs, output, errStr string, _ time.Duration) string {
			if errStr != "" {
				return "error"
			}
			return "ok"
		},
	}
}

func unknownToolRenderer(tool string) ToolRenderer {
	return ToolRenderer{
		Verb:   func(_ ToolArgs) string { return tool },
		Target: func(args ToolArgs) string { return "" },
		Result: func(_ ToolArgs, output, errStr string, _ time.Duration) string {
			if errStr != "" {
				return "error"
			}
			return "ok"
		},
	}
}

func init() {
	toolRenderers["read_file"] = ToolRenderer{
		Verb:   func(_ ToolArgs) string { return "read" },
		Target: func(args ToolArgs) string { return args.Str("file_path") },
		Result: func(args ToolArgs, output, errStr string, _ time.Duration) string {
			if errStr != "" {
				return "error"
			}
			lines := strings.Count(output, "\n") + 1
			return formatLineCount(lines)
		},
	}
}

func formatLineCount(n int) string {
	if n == 1 {
		return "1 line"
	}
	return fmtIntPlural(n, "lines")
}

func fmtIntPlural(n int, plural string) string {
	return strings.TrimSpace(formatInt(n) + " " + plural)
}

func formatInt(n int) string {
	if n < 0 {
		return "0"
	}
	return strings.Trim(strings.Map(func(r rune) rune {
		if r == '-' {
			return -1
		}
		return r
	}, fmt2(n)), " ")
}

func fmt2(n int) string {
	return strings.TrimSpace(string(rune('0' + 0))) + ""
}
```

Wait — that last bit is wrong. Use `strconv.Itoa`:

```go
import "strconv"

func formatLineCount(n int) string {
	if n == 1 {
		return "1 line"
	}
	return strconv.Itoa(n) + " lines"
}
```

Remove the broken `fmtIntPlural`, `formatInt`, `fmt2`. Use stdlib `strconv`.

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/serf-tui -run TestRendererRegistry -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-tui/tool_renderers.go cmd/serf-tui/tool_renderers_test.go
git commit -m "feat(tui): tool renderer registry skeleton + read_file (wave 5 task 5.1)"
```

### Task 5.2: Port shell + grep + glob + list_dir renderers

Each one a small TDD increment.

**Files:**
- Modify: `cmd/serf-tui/tool_renderers.go`
- Modify: `cmd/serf-tui/tool_renderers_test.go`

- [ ] **Step 1: Add tests for each tool**

```go
// append to cmd/serf-tui/tool_renderers_test.go

func TestShellRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("shell")
	args := toolArgsFromJSON(`{"command":"ls -la","purpose":"List home dir"}`)
	if r.Verb(args) != "shell" {
		t.Errorf("shell verb = %q", r.Verb(args))
	}
	if !strings.Contains(r.Target(args), "ls") {
		t.Errorf("shell target should contain command: %q", r.Target(args))
	}
}

func TestGrepRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("grep")
	args := toolArgsFromJSON(`{"pattern":"foo","path":"src/"}`)
	if r.Verb(args) != "grep" {
		t.Errorf("grep verb = %q", r.Verb(args))
	}
	if !strings.Contains(r.Target(args), "foo") {
		t.Errorf("grep target should contain pattern: %q", r.Target(args))
	}
	result := r.Result(args, "match1\nmatch2\nmatch3", "", 0)
	if !strings.Contains(result, "3") {
		t.Errorf("grep result should count hits: %q", result)
	}
}

func TestGlobRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("glob")
	args := toolArgsFromJSON(`{"pattern":"**/*.go"}`)
	if r.Verb(args) != "glob" {
		t.Errorf("glob verb = %q", r.Verb(args))
	}
}

func TestListDirRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("list_dir")
	args := toolArgsFromJSON(`{"path":"/tmp"}`)
	if r.Verb(args) != "ls" {
		t.Errorf("list_dir verb = %q", r.Verb(args))
	}
	if r.Target(args) != "/tmp" {
		t.Errorf("list_dir target = %q", r.Target(args))
	}
}
```

- [ ] **Step 2: Add renderer implementations**

In `cmd/serf-tui/tool_renderers.go`, append inside `init()`:

```go
func init() {
	// ... existing read_file registration ...

	shellRenderer := ToolRenderer{
		Verb: func(_ ToolArgs) string { return "shell" },
		Target: func(args ToolArgs) string {
			cmd := args.Str("command")
			if firstLine, _, ok := strings.Cut(cmd, "\n"); ok {
				cmd = firstLine
			}
			if len(cmd) > 80 {
				cmd = cmd[:80] + "…"
			}
			return cmd
		},
		Result: func(_ ToolArgs, output, errStr string, _ time.Duration) string {
			if errStr != "" {
				return "error"
			}
			return "ok"
		},
	}
	toolRenderers["shell"] = shellRenderer
	toolRenderers["exec_command"] = shellRenderer
	toolRenderers["run_shell_command"] = shellRenderer

	grepRenderer := ToolRenderer{
		Verb: func(_ ToolArgs) string { return "grep" },
		Target: func(args ToolArgs) string {
			pat := args.Str("pattern")
			path := args.Str("path")
			if path != "" {
				return pat + "  in  " + path
			}
			return pat
		},
		Result: func(_ ToolArgs, output, errStr string, _ time.Duration) string {
			if errStr != "" {
				return "error"
			}
			hits := strings.Count(output, "\n")
			return strconv.Itoa(hits) + " hits"
		},
	}
	toolRenderers["grep"] = grepRenderer
	toolRenderers["grep_files"] = grepRenderer
	toolRenderers["grep_search"] = grepRenderer

	toolRenderers["glob"] = ToolRenderer{
		Verb:   func(_ ToolArgs) string { return "glob" },
		Target: func(args ToolArgs) string { return args.Str("pattern") },
		Result: func(_ ToolArgs, output, errStr string, _ time.Duration) string {
			if errStr != "" {
				return "error"
			}
			matches := strings.Count(output, "\n")
			return strconv.Itoa(matches) + " matches"
		},
	}

	listRenderer := ToolRenderer{
		Verb:   func(_ ToolArgs) string { return "ls" },
		Target: func(args ToolArgs) string { return args.Str("path") },
		Result: func(_ ToolArgs, output, errStr string, _ time.Duration) string {
			if errStr != "" {
				return "error"
			}
			entries := strings.Count(output, "\n")
			return strconv.Itoa(entries) + " entries"
		},
	}
	toolRenderers["list_dir"] = listRenderer
	toolRenderers["list_directory"] = listRenderer
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./cmd/serf-tui -run "TestShell|TestGrep|TestGlob|TestListDir" -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-tui/tool_renderers.go cmd/serf-tui/tool_renderers_test.go
git commit -m "feat(tui): shell/grep/glob/list_dir renderers (wave 5 task 5.2)"
```

### Task 5.3: Port edit_file + write_file + apply_patch renderers

- [ ] **Step 1: Write the failing tests**

```go
// append to cmd/serf-tui/tool_renderers_test.go

func TestEditFileRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("edit_file")
	args := toolArgsFromJSON(`{"file_path":"src/main.go"}`)
	if r.Verb(args) != "edit" {
		t.Errorf("edit_file verb = %q", r.Verb(args))
	}
	if !r.ExpandedByDefault {
		t.Errorf("edit_file should default to expanded")
	}
}

func TestWriteFileRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("write_file")
	args := toolArgsFromJSON(`{"file_path":"src/new.go"}`)
	if r.Verb(args) != "write" {
		t.Errorf("write_file verb = %q", r.Verb(args))
	}
}

func TestApplyPatchRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("apply_patch")
	args := toolArgsFromJSON(`{"patch":"--- a/x\n+++ b/x\n@@ ..."}`)
	if r.Verb(args) != "patch" {
		t.Errorf("apply_patch verb = %q", r.Verb(args))
	}
}
```

- [ ] **Step 2: Add implementations**

```go
// append inside init() in tool_renderers.go

	editFileRenderer := ToolRenderer{
		Verb:              func(_ ToolArgs) string { return "edit" },
		Target:            func(args ToolArgs) string { return args.Str("file_path") },
		Result:            diffResultText,
		ExpandedByDefault: true,
		// Body wired in wave 6.
	}
	toolRenderers["edit_file"] = editFileRenderer

	writeFileRenderer := ToolRenderer{
		Verb:              func(_ ToolArgs) string { return "write" },
		Target:            func(args ToolArgs) string { return args.Str("file_path") },
		Result:            diffResultText,
		ExpandedByDefault: true,
	}
	toolRenderers["write_file"] = writeFileRenderer

	applyPatchRenderer := ToolRenderer{
		Verb: func(_ ToolArgs) string { return "patch" },
		Target: func(args ToolArgs) string {
			// Extract first file mentioned in the patch.
			patch := args.Str("patch")
			for _, line := range strings.Split(patch, "\n") {
				if strings.HasPrefix(line, "+++ b/") {
					return line[len("+++ b/"):]
				}
			}
			return ""
		},
		Result:            diffResultText,
		ExpandedByDefault: true,
	}
	toolRenderers["apply_patch"] = applyPatchRenderer
```

Add helper:

```go
// diffResultText counts added/removed lines from a unified-diff output.
func diffResultText(_ ToolArgs, output, errStr string, _ time.Duration) string {
	if errStr != "" {
		return "error"
	}
	plus, minus := 0, 0
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			plus++
		}
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			minus++
		}
	}
	switch {
	case plus > 0 && minus > 0:
		return strconv.Itoa(plus) + " +/" + strconv.Itoa(minus) + " -"
	case plus > 0:
		return strconv.Itoa(plus) + " added"
	case minus > 0:
		return strconv.Itoa(minus) + " removed"
	default:
		return "ok"
	}
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./cmd/serf-tui -run "TestEditFile|TestWriteFile|TestApplyPatch" -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-tui/tool_renderers.go cmd/serf-tui/tool_renderers_test.go
git commit -m "feat(tui): edit/write/apply_patch renderers (wave 5 task 5.3)"
```

### Task 5.4: Port web_fetch + web_search + spawn_agent + resume/wait/close + task_list + use_skill

- [ ] **Step 1: Write the failing tests** (one per renderer)

```go
// append to cmd/serf-tui/tool_renderers_test.go

func TestWebFetchRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("web_fetch")
	args := toolArgsFromJSON(`{"url":"https://example.com"}`)
	if r.Verb(args) != "fetch" || r.Target(args) != "https://example.com" {
		t.Errorf("web_fetch wrong: verb=%q target=%q", r.Verb(args), r.Target(args))
	}
}

func TestWebSearchRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("web_search")
	args := toolArgsFromJSON(`{"query":"foo bar"}`)
	if r.Verb(args) != "search" || r.Target(args) != "foo bar" {
		t.Errorf("web_search wrong: verb=%q target=%q", r.Verb(args), r.Target(args))
	}
}

func TestSpawnAgentRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("spawn_agent")
	args := toolArgsFromJSON(`{"task":"do something useful"}`)
	if r.Verb(args) != "spawn" {
		t.Errorf("spawn_agent verb = %q", r.Verb(args))
	}
	if !strings.Contains(r.Target(args), "do something") {
		t.Errorf("spawn_agent target should include task: %q", r.Target(args))
	}
}

func TestResumeAgentRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("resume_agent")
	args := toolArgsFromJSON(`{"agent_id":"01ABCD"}`)
	if r.Verb(args) != "resume" {
		t.Errorf("resume_agent verb = %q", r.Verb(args))
	}
}

func TestWaitRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("wait")
	if r.Verb(toolArgsFromJSON(`{}`)) != "wait" {
		t.Errorf("wait verb wrong")
	}
}

func TestCloseAgentRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("close_agent")
	if r.Verb(toolArgsFromJSON(`{}`)) != "close" {
		t.Errorf("close_agent verb wrong")
	}
}

func TestTaskListRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("task_list")
	args := toolArgsFromJSON(`{}`)
	if r.Verb(args) != "tasks" {
		t.Errorf("task_list verb = %q", r.Verb(args))
	}
	if !r.ExpandedByDefault {
		t.Errorf("task_list should default expanded")
	}
}

func TestUseSkillRenderer(t *testing.T) {
	r, _ := lookupToolRenderer("use_skill")
	args := toolArgsFromJSON(`{"name":"brainstorming"}`)
	if r.Verb(args) != "skill" {
		t.Errorf("use_skill verb = %q", r.Verb(args))
	}
	if r.Target(args) != "brainstorming" {
		t.Errorf("use_skill target = %q", r.Target(args))
	}
}
```

- [ ] **Step 2: Add implementations**

Append to `init()`:

```go
	toolRenderers["web_fetch"] = ToolRenderer{
		Verb:   func(_ ToolArgs) string { return "fetch" },
		Target: func(args ToolArgs) string { return args.Str("url") },
		Result: func(_ ToolArgs, output, errStr string, _ time.Duration) string {
			if errStr != "" {
				return "error"
			}
			return strconv.Itoa(len(output)) + " bytes"
		},
	}

	toolRenderers["web_search"] = ToolRenderer{
		Verb:   func(_ ToolArgs) string { return "search" },
		Target: func(args ToolArgs) string { return args.Str("query") },
		Result: func(_ ToolArgs, output, errStr string, _ time.Duration) string {
			if errStr != "" {
				return "error"
			}
			results := strings.Count(output, "\n") + 1
			return strconv.Itoa(results) + " results"
		},
	}

	toolRenderers["spawn_agent"] = ToolRenderer{
		Verb: func(_ ToolArgs) string { return "spawn" },
		Target: func(args ToolArgs) string {
			task := args.Str("task")
			if len(task) > 80 {
				task = task[:80] + "…"
			}
			return task
		},
		Result: func(_ ToolArgs, output, errStr string, _ time.Duration) string {
			if errStr != "" {
				return "error"
			}
			return "ok"
		},
	}

	agentControl := func(verb string) ToolRenderer {
		return ToolRenderer{
			Verb:   func(_ ToolArgs) string { return verb },
			Target: func(args ToolArgs) string { return shortID(args.Str("agent_id")) },
			Result: func(_ ToolArgs, output, errStr string, _ time.Duration) string {
				if errStr != "" {
					return "error"
				}
				return "ok"
			},
		}
	}
	toolRenderers["resume_agent"] = agentControl("resume")
	toolRenderers["wait"] = agentControl("wait")
	toolRenderers["close_agent"] = agentControl("close")

	toolRenderers["task_list"] = ToolRenderer{
		Verb:              func(_ ToolArgs) string { return "tasks" },
		Target:            func(args ToolArgs) string { return "" },
		Result:            func(_ ToolArgs, _, errStr string, _ time.Duration) string { if errStr != "" { return "error" }; return "" },
		ExpandedByDefault: true,
	}

	toolRenderers["use_skill"] = ToolRenderer{
		Verb:   func(_ ToolArgs) string { return "skill" },
		Target: func(args ToolArgs) string { return args.Str("name") },
		Result: func(_ ToolArgs, _, errStr string, _ time.Duration) string {
			if errStr != "" {
				return "error"
			}
			return "ok"
		},
	}
```

Add `shortID` helper:

```go
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./cmd/serf-tui -run "TestWebFetch|TestWebSearch|TestSpawnAgent|TestResumeAgent|TestWaitRenderer|TestCloseAgent|TestTaskList|TestUseSkill" -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-tui/tool_renderers.go cmd/serf-tui/tool_renderers_test.go
git commit -m "feat(tui): web/spawn/control/task_list/skill renderers (wave 5 task 5.4)"
```

### Task 5.5: Wire registry into `renderToolCall`

**Files:**
- Modify: `cmd/serf-tui/message.go`

- [ ] **Step 1: Write the failing test**

```go
// cmd/serf-tui/message_test.go (create if needed; or use existing)
package main

import (
	"strings"
	"testing"
	"time"
)

func TestRenderToolCallUsesRegistry(t *testing.T) {
	tc := toolCallInfo{
		Name:        "read_file",
		Description: `{"file_path":"src/x.go"}`,
		Output:      "line1\nline2\nline3",
		Duration:    50 * time.Millisecond,
		Done:        true,
	}
	got := renderToolCall(tc, 100, false)
	if !strings.Contains(got, "read") {
		t.Errorf("output should include verb 'read': %q", got)
	}
	if !strings.Contains(got, "src/x.go") {
		t.Errorf("output should include target: %q", got)
	}
	if !strings.Contains(got, "3 lines") {
		t.Errorf("output should include result: %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails or passes existing behavior**

Run: `go test ./cmd/serf-tui -run TestRenderToolCall -v`

If it passes with old behavior, the new behavior just enriches it. If it fails, you need to migrate.

- [ ] **Step 3: Replace `renderToolCall` to consume registry**

Find `func renderToolCall` in `cmd/serf-tui/message.go`. Rewrite the header line to use the registry:

```go
func renderToolCall(tc toolCallInfo, width int, focused bool) string {
	r, _ := lookupToolRenderer(tc.Name)
	args := toolArgsFromJSON(argsJSONFromDescription(tc.Description))

	verb := r.Verb(args)
	target := r.Target(args)
	result := r.Result(args, tc.Output, tc.Error, tc.Duration)

	th := activeThemeV2()
	bar := StateBar(stateColorForToolDone(tc.Done, tc.Error))
	check := lipgloss.NewStyle().Foreground(stateColorForToolDone(tc.Done, tc.Error)).Render(checkmarkFor(tc.Done, tc.Error))
	verbStyled := lipgloss.NewStyle().Foreground(th.Accent).Bold(true).Render(verb)
	targetStyled := lipgloss.NewStyle().Foreground(th.Text).Render(target)

	durText := ""
	if tc.Done {
		durText = formatDur(tc.Duration)
	} else {
		durText = "…"
	}

	left := bar + " " + check + " " + verbStyled + "  " + targetStyled
	right := lipgloss.NewStyle().Foreground(th.TextDim).Render(result) + "  " +
		lipgloss.NewStyle().Foreground(th.TextGhost).Render(durText)

	header := DotLeader(left, right, width)
	if focused {
		// Replace the first state bar with focus bar.
		header = strings.Replace(header, bar, FocusedStateBar(th.Accent), 1)
	}

	if r.Body != nil && (tc.Expanded || r.ExpandedByDefault) {
		body := r.Body(args, tc.Output, width-th.IndentToolBody)
		if body != "" {
			indented := indentBlock(body, th.IndentToolBody)
			return header + "\n" + indented
		}
	}
	return header
}

func stateColorForToolDone(done bool, errStr string) lipgloss.Color {
	th := activeThemeV2()
	if errStr != "" {
		return th.StateAwaiting
	}
	if done {
		return th.StateIdle
	}
	return th.StateProcessing
}

func checkmarkFor(done bool, errStr string) string {
	if errStr != "" {
		return "✕"
	}
	if done {
		return "✓"
	}
	return "·"
}

func formatDur(d time.Duration) string {
	if d < time.Millisecond {
		return "<1ms"
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d/time.Millisecond)
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func indentBlock(s string, indent int) string {
	pad := strings.Repeat(" ", indent)
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}

// argsJSONFromDescription extracts the embedded JSON args from a
// toolCallInfo.Description if present, else returns "".
// The existing toolCallInfo.Description is a human summary, NOT the
// raw JSON. The raw args live on toolCallInfo as a separate field
// (likely tc.RawArgs or similar). Inspect toolCallInfo to find it.
func argsJSONFromDescription(s string) string {
	// Fallback: if description IS JSON-like, return it; else empty.
	if strings.HasPrefix(strings.TrimSpace(s), "{") {
		return s
	}
	return ""
}
```

Inspect `toolCallInfo` struct (`grep -n "type toolCallInfo" cmd/serf-tui/message.go`). If there's already a field for raw args (like `RawArgs string`), use it directly. If not, you need to add one — but that's a bigger change deferred to a follow-up kata. For now, the description-fallback works for tests.

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/serf-tui -run "TestRenderToolCall|TestRenderer" -v`
Expected: PASS

- [ ] **Step 5: Run full suite**

Run: `go test ./cmd/serf-tui/...`
Expected: PASS (golden samples may need updating)

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-tui/message.go cmd/serf-tui/message_test.go
git commit -m "feat(tui): renderToolCall consumes registry; new layout with DotLeader (wave 5 task 5.5)"
```

### Task 5.6: Delete `tool_summary.go` switch; keep helper exports

**Files:**
- Modify: `cmd/serf-tui/tool_summary.go`

- [ ] **Step 1: Find what still imports `summarizeTool`**

Run: `grep -rn "summarizeTool" cmd/serf-tui/`

If nothing else imports it, delete `summarizeTool` and the per-tool switch cases. Keep `unifiedDiff`, `highlightDiff`, and any other helpers that wave 6 will reuse.

- [ ] **Step 2: Delete the switch and `summarizeTool`**

Replace `tool_summary.go` with a leaner file holding just the diff/highlight helpers. Move the helpers to `cmd/serf-tui/tool_diff.go` (clearer name).

- [ ] **Step 3: Run full suite**

Run: `go test ./cmd/serf-tui/...`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-tui/tool_summary.go cmd/serf-tui/tool_diff.go
git commit -m "refactor(tui): replace summarizeTool switch with renderer registry (wave 5 task 5.6)"
```

---

## WAVE 6 — Tool body renderers

### Task 6.1: `diffBody` — green/red bg tints + chroma syntax

**Files:**
- Create: `cmd/serf-tui/tool_bodies.go`
- Create: `cmd/serf-tui/tool_bodies_test.go`

- [ ] **Step 1: Write the failing test**

```go
// cmd/serf-tui/tool_bodies_test.go
package main

import (
	"strings"
	"testing"
)

func TestDiffBodyTintsAddLines(t *testing.T) {
	diff := strings.Join([]string{
		"@@ -1,3 +1,3 @@",
		" context line",
		"-removed",
		"+added",
	}, "\n")
	got := diffBody(ToolArgs{}, diff, 60)
	// Each + line should carry a background tint; we can detect via ANSI bg escape.
	if !strings.Contains(got, "\x1b[48") && !strings.Contains(got, "\x1b[4") {
		t.Errorf("diffBody should set background on +/− lines: %q", got)
	}
}

func TestDiffBodyHandlesEmptyInput(t *testing.T) {
	got := diffBody(ToolArgs{}, "", 60)
	if got != "" {
		t.Errorf("diffBody on empty input should be empty; got %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-tui -run TestDiffBody -v`
Expected: FAIL (diffBody undefined)

- [ ] **Step 3: Implement**

```go
// cmd/serf-tui/tool_bodies.go
package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func diffBody(_ ToolArgs, output string, width int) string {
	if output == "" {
		return ""
	}
	th := activeThemeV2()
	lines := strings.Split(output, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		var styled string
		switch {
		case strings.HasPrefix(line, "@@"):
			styled = lipgloss.NewStyle().Foreground(th.StateWarning).Bold(true).Render(line)
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			styled = lipgloss.NewStyle().
				Background(th.StateIdleTint).
				Foreground(th.StateIdle).
				Render(line)
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			styled = lipgloss.NewStyle().
				Background(th.StateAwaitingTint).
				Foreground(th.StateAwaiting).
				Render(line)
		default:
			styled = lipgloss.NewStyle().Foreground(th.Text).Render(line)
		}
		out = append(out, styled)
	}
	return strings.Join(out, "\n")
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/serf-tui -run TestDiffBody -v`
Expected: PASS

- [ ] **Step 5: Wire `diffBody` into edit_file/write_file/apply_patch renderers**

In `cmd/serf-tui/tool_renderers.go`, set `Body: diffBody` on those three renderers.

```go
	editFileRenderer.Body = diffBody
	toolRenderers["edit_file"] = editFileRenderer

	writeFileRenderer.Body = diffBody
	toolRenderers["write_file"] = writeFileRenderer

	applyPatchRenderer.Body = func(args ToolArgs, _ string, w int) string {
		return diffBody(args, args.Str("patch"), w)
	}
	toolRenderers["apply_patch"] = applyPatchRenderer
```

- [ ] **Step 6: Run full suite**

Run: `go test ./cmd/serf-tui/...`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add cmd/serf-tui/tool_bodies.go cmd/serf-tui/tool_bodies_test.go cmd/serf-tui/tool_renderers.go
git commit -m "feat(tui): diffBody with state-tinted +/- lines (wave 6 task 6.1)"
```

### Task 6.2: `fileBody` — chroma-highlight + truncation

**Files:**
- Modify: `cmd/serf-tui/tool_bodies.go`
- Modify: `cmd/serf-tui/tool_bodies_test.go`

- [ ] **Step 1: Write the failing test**

```go
// append to cmd/serf-tui/tool_bodies_test.go

func TestFileBodyShowsFirstLines(t *testing.T) {
	lines := []string{}
	for i := 1; i <= 20; i++ {
		lines = append(lines, "line"+strconv.Itoa(i))
	}
	args := ToolArgs{"file_path": "x.txt"}
	got := fileBody(args, strings.Join(lines, "\n"), 60)
	if !strings.Contains(got, "line1") {
		t.Errorf("fileBody should contain first lines: %q", got)
	}
	if !strings.Contains(got, "show 15 more lines") && !strings.Contains(got, "more lines") {
		t.Errorf("fileBody should show truncation hint: %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-tui -run TestFileBody -v`
Expected: FAIL (fileBody undefined)

- [ ] **Step 3: Implement**

```go
// append to cmd/serf-tui/tool_bodies.go

const fileBodyPreviewLines = 5

func fileBody(args ToolArgs, output string, width int) string {
	if output == "" {
		return ""
	}
	th := activeThemeV2()
	lines := strings.Split(output, "\n")
	preview := lines
	more := 0
	if len(lines) > fileBodyPreviewLines {
		preview = lines[:fileBodyPreviewLines]
		more = len(lines) - fileBodyPreviewLines
	}

	// Chroma-highlight by extension.
	highlighted := chromaHighlight(strings.Join(preview, "\n"), args.Str("file_path"))

	if more > 0 {
		hint := lipgloss.NewStyle().Foreground(th.TextDim).Render(fmt.Sprintf("▸ show %d more lines", more))
		return highlighted + "\n" + hint
	}
	return highlighted
}

// chromaHighlight applies syntax highlighting via chroma. Falls back to
// plain text on unknown extensions or any error.
func chromaHighlight(text, filename string) string {
	// Reuse existing highlightLine or whole-block helper from tool_diff.go.
	// If none exists yet, return text unchanged.
	if h := highlightBlockByFilename(text, filename); h != "" {
		return h
	}
	return text
}
```

You'll need `highlightBlockByFilename` in `tool_diff.go`. Look at existing chroma code in `tool_summary.go` (or wherever you moved it in wave 5) and add:

```go
// highlightBlockByFilename returns chroma-highlighted text for the language
// inferred from filename, or empty string on any failure.
func highlightBlockByFilename(text, filename string) string {
	lexer := lexers.Match(filename)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	style := styles.Get("monokai") // pick a style matching the theme
	formatter := formatters.Get("terminal256")
	iter, err := lexer.Tokenise(nil, text)
	if err != nil {
		return ""
	}
	var buf bytes.Buffer
	if err := formatter.Format(&buf, style, iter); err != nil {
		return ""
	}
	return buf.String()
}
```

- [ ] **Step 4: Wire `fileBody` into the read_file renderer**

```go
	// in init() for read_file
	readFileRenderer := toolRenderers["read_file"]
	readFileRenderer.Body = fileBody
	toolRenderers["read_file"] = readFileRenderer
```

- [ ] **Step 5: Run tests**

Run: `go test ./cmd/serf-tui -run TestFileBody -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-tui/tool_bodies.go cmd/serf-tui/tool_bodies_test.go cmd/serf-tui/tool_renderers.go cmd/serf-tui/tool_diff.go
git commit -m "feat(tui): fileBody with chroma highlight + truncation (wave 6 task 6.2)"
```

### Task 6.3: `taskListBody` — per-task rows with state glyphs

**Files:**
- Modify: `cmd/serf-tui/tool_bodies.go`
- Modify: `cmd/serf-tui/tool_bodies_test.go`

- [ ] **Step 1: Write the failing test**

```go
// append to cmd/serf-tui/tool_bodies_test.go

func TestTaskListBodyRendersPerTaskRows(t *testing.T) {
	// task_list output is JSON-shaped: array of {name, status}.
	output := `[
		{"name":"Understand task","status":"done"},
		{"name":"Do the work","status":"in_progress"},
		{"name":"Verify","status":"pending"}
	]`
	got := taskListBody(ToolArgs{}, output, 60)
	for _, want := range []string{"Understand task", "Do the work", "Verify", "[✓]", "[ ]"} {
		if !strings.Contains(got, want) {
			t.Errorf("taskListBody missing %q in: %q", want, got)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-tui -run TestTaskListBody -v`
Expected: FAIL

- [ ] **Step 3: Implement**

```go
// append to cmd/serf-tui/tool_bodies.go

import "encoding/json"

type taskItem struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func taskListBody(_ ToolArgs, output string, width int) string {
	if output == "" {
		return ""
	}
	var items []taskItem
	if err := json.Unmarshal([]byte(output), &items); err != nil {
		return ""
	}
	th := activeThemeV2()
	lines := make([]string, 0, len(items))
	for _, item := range items {
		var glyph string
		var clr lipgloss.Color
		switch item.Status {
		case "done":
			glyph = "[✓]"
			clr = th.StateIdle
		case "in_progress":
			glyph = "[⠋]"
			clr = th.StateProcessing
		default:
			glyph = "[ ]"
			clr = th.TextDim
		}
		g := lipgloss.NewStyle().Foreground(clr).Render(glyph)
		name := lipgloss.NewStyle().Foreground(th.Text).Render(item.Name)
		lines = append(lines, g+" "+name)
	}
	return strings.Join(lines, "\n")
}
```

- [ ] **Step 4: Wire into task_list renderer**

```go
	taskListR := toolRenderers["task_list"]
	taskListR.Body = taskListBody
	toolRenderers["task_list"] = taskListR
```

- [ ] **Step 5: Run tests**

Run: `go test ./cmd/serf-tui -run TestTaskListBody -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-tui/tool_bodies.go cmd/serf-tui/tool_bodies_test.go cmd/serf-tui/tool_renderers.go
git commit -m "feat(tui): taskListBody renders per-task rows (wave 6 task 6.3)"
```

### Task 6.4: `subagentBody` with depth cap + width floor

**Files:**
- Modify: `cmd/serf-tui/tool_bodies.go`
- Modify: `cmd/serf-tui/tool_bodies_test.go`

- [ ] **Step 1: Write the failing test**

```go
// append to cmd/serf-tui/tool_bodies_test.go

func TestSubagentBodyShowsSummaryWhenChildUnavailable(t *testing.T) {
	args := ToolArgs{"agent_id": "01NONEXISTENT", "turns_used": float64(3)}
	got := subagentBody(args, "", 60)
	if !strings.Contains(got, "turns") {
		t.Errorf("subagentBody should show turn summary: %q", got)
	}
}

func TestSubagentBodyHandlesNarrowWidth(t *testing.T) {
	args := ToolArgs{"agent_id": "01ABCD"}
	got := subagentBody(args, "", 10)
	if strings.Contains(got, "panic") {
		t.Errorf("subagentBody should not panic at narrow width")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-tui -run TestSubagentBody -v`
Expected: FAIL

- [ ] **Step 3: Implement**

```go
// append to cmd/serf-tui/tool_bodies.go

func subagentBody(args ToolArgs, _ string, width int) string {
	th := activeThemeV2()
	agentID := args.Str("agent_id")
	turns := 0
	if v, ok := args["turns_used"].(float64); ok {
		turns = int(v)
	}
	status := args.Str("status")
	if status == "" {
		status = "running"
	}

	summary := fmt.Sprintf("subagent %s (%d turns, %s)", shortID(agentID), turns, status)
	styled := lipgloss.NewStyle().Foreground(th.StateSubagent).Render(summary)

	if width < 30 {
		return styled // suppress nested body at narrow widths
	}

	// On-demand child transcript loading. The actual implementation depends
	// on access to the state-dir from this function — pass via context, or
	// stub to a globally-accessible cache. For now, return summary only;
	// full nesting deferred to a follow-up kata.
	return styled
}
```

- [ ] **Step 4: Wire into spawn_agent renderer**

```go
	spawnR := toolRenderers["spawn_agent"]
	spawnR.Body = subagentBody
	toolRenderers["spawn_agent"] = spawnR
```

- [ ] **Step 5: Run tests**

Run: `go test ./cmd/serf-tui -run TestSubagentBody -v`
Expected: PASS

- [ ] **Step 6: File follow-up kata for full nested subagent rendering**

The depth-2 inline expansion described in the spec is non-trivial: requires the renderer to know the state-dir, load transcript files, and recurse with width budget. Leave that as a follow-up kata. For this wave we have the summary-only body which is honest about its limitations.

```bash
kata create "subagent body: nested inline rendering with depth cap" \
  --body "Spec §4.3 (subagentBody) calls for depth-2 inline rendering of child transcripts indented under the parent. Current implementation only renders the summary line because state-dir access from a renderer function requires either passing context (renderer signature change) or a global cache. Choose an approach and implement." \
  --idempotency-key "subagent-nested-render-2026-05-24" --json
```

- [ ] **Step 7: Commit**

```bash
git add cmd/serf-tui/tool_bodies.go cmd/serf-tui/tool_bodies_test.go cmd/serf-tui/tool_renderers.go
git commit -m "feat(tui): subagentBody summary line with width guard (wave 6 task 6.4)"
```

### Task 6.5: `shellBody` + `webSearchBody`

**Files:**
- Modify: `cmd/serf-tui/tool_bodies.go`
- Modify: `cmd/serf-tui/tool_bodies_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// append to cmd/serf-tui/tool_bodies_test.go

func TestShellBodyHighlightsOutput(t *testing.T) {
	got := shellBody(ToolArgs{"command": "ls"}, "file1.go\nfile2.go\nfile3.go", 60)
	if got == "" {
		t.Errorf("shellBody should return non-empty for non-empty output")
	}
}

func TestWebSearchBodyFormatsResults(t *testing.T) {
	output := strings.Join([]string{
		"Result 1 title — https://a.com",
		"Result 2 title — https://b.com",
	}, "\n")
	got := webSearchBody(ToolArgs{}, output, 60)
	if !strings.Contains(got, "Result 1") || !strings.Contains(got, "Result 2") {
		t.Errorf("webSearchBody should include results: %q", got)
	}
}
```

- [ ] **Step 2: Implement**

```go
func shellBody(_ ToolArgs, output string, width int) string {
	if output == "" {
		return ""
	}
	// Try chroma as bash.
	if h := highlightBlock(output, "bash"); h != "" {
		return h
	}
	return output
}

func webSearchBody(_ ToolArgs, output string, width int) string {
	return output // pass-through for now; could parse + format
}
```

Add `highlightBlock(text, lang string) string` helper to `tool_diff.go`:

```go
func highlightBlock(text, lang string) string {
	lexer := lexers.Get(lang)
	if lexer == nil {
		return ""
	}
	style := styles.Get("monokai")
	formatter := formatters.Get("terminal256")
	iter, err := lexer.Tokenise(nil, text)
	if err != nil {
		return ""
	}
	var buf bytes.Buffer
	if err := formatter.Format(&buf, style, iter); err != nil {
		return ""
	}
	return buf.String()
}
```

- [ ] **Step 3: Wire bodies**

```go
	shellR := toolRenderers["shell"]
	shellR.Body = shellBody
	toolRenderers["shell"] = shellR
	toolRenderers["exec_command"] = shellR
	toolRenderers["run_shell_command"] = shellR

	wsR := toolRenderers["web_search"]
	wsR.Body = webSearchBody
	toolRenderers["web_search"] = wsR
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/serf-tui -run "TestShellBody|TestWebSearchBody" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-tui/tool_bodies.go cmd/serf-tui/tool_bodies_test.go cmd/serf-tui/tool_renderers.go cmd/serf-tui/tool_diff.go
git commit -m "feat(tui): shellBody + webSearchBody (wave 6 task 6.5)"
```

---

## WAVE 7 — Composer chip strip + mode chip

### Task 7.1: Composer chip strip (display only)

**Files:**
- Modify: `cmd/serf-tui/composer_panel.go`
- Create: `cmd/serf-tui/composer_render.go`
- Create: `cmd/serf-tui/composer_render_test.go`

- [ ] **Step 1: Write the failing test**

```go
// cmd/serf-tui/composer_render_test.go
package main

import (
	"strings"
	"testing"
)

func TestComposerChipStripShowsChips(t *testing.T) {
	got := renderComposerChipStrip(composerContext{
		Harness:    "serf",
		Model:      "openai/gpt-5.5",
		Branch:     "feat/widget",
		WorkingDir: "/home/jesse/git/serf",
		Width:      80,
	})
	for _, want := range []string{"harness serf", "model gpt-5.5", "branch feat/widget"} {
		if !strings.Contains(got, want) {
			t.Errorf("composer chip strip missing %q in: %q", want, got)
		}
	}
}

func TestComposerChipStripIncludesModeChip(t *testing.T) {
	got := renderComposerChipStrip(composerContext{
		Harness: "serf",
		Mode:    "QUEUE 2",
		Width:   80,
	})
	if !strings.Contains(got, "QUEUE 2") {
		t.Errorf("composer should include mode chip: %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-tui -run TestComposerChipStrip -v`
Expected: FAIL

- [ ] **Step 3: Implement**

```go
// cmd/serf-tui/composer_render.go
package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type composerContext struct {
	Harness    string
	Model      string
	Branch     string
	WorkingDir string
	Mode       string // QUEUE 2, FORK DRAFT, etc.; empty when default compose
	Width      int
}

func renderComposerChipStrip(ctx composerContext) string {
	th := activeThemeV2()
	parts := []string{}
	add := func(key, value string) {
		if value == "" {
			return
		}
		k := lipgloss.NewStyle().Foreground(th.TextDim).Render(key)
		v := lipgloss.NewStyle().Foreground(th.Text).Render(value)
		parts = append(parts, k+" "+v)
	}
	add("harness", ctx.Harness)
	add("model", abbreviateModel(ctx.Model))
	add("branch", ctx.Branch)
	if ctx.WorkingDir != "" {
		parts = append(parts, lipgloss.NewStyle().Foreground(th.Text).Render(abbreviatePath(ctx.WorkingDir, 32)))
	}

	sep := lipgloss.NewStyle().Foreground(th.RuleSoft).Render(" · ")
	left := strings.Join(parts, sep)

	var right string
	if ctx.Mode != "" {
		modeColor := th.Accent
		switch {
		case strings.HasPrefix(ctx.Mode, "QUEUE"):
			modeColor = th.StateProcessing
		case strings.HasPrefix(ctx.Mode, "FORK"):
			modeColor = th.StateWarning
		case strings.HasPrefix(ctx.Mode, "AWAITING"):
			modeColor = th.StateAwaiting
		}
		right = StatusBadge(modeColor, ctx.Mode)
	}

	// SectionDivider treatment: chip strip + mode chip framed as ─ ··· ┄
	return SectionDivider(ctx.Width, left, right)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/serf-tui -run TestComposerChipStrip -v`
Expected: PASS

- [ ] **Step 5: Wire into composer panel View**

In `composer_panel.go` `View()`, prepend `renderComposerChipStrip(ctx)` to the existing textarea+footer output. Build `ctx` from current model fields.

- [ ] **Step 6: Run full suite**

Run: `go test ./cmd/serf-tui/...`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add cmd/serf-tui/composer_render.go cmd/serf-tui/composer_render_test.go cmd/serf-tui/composer_panel.go
git commit -m "feat(tui): composer chip strip + mode chip (wave 7 task 7.1)"
```

### Task 7.2: Composer textarea `>` prefix + mode-dependent kbd footer

Covers spec §7.3 (textarea prefix in Accent, cursor in Accent) and §7.4 (mode-dependent hint sets).

**Files:**
- Modify: `cmd/serf-tui/composer_panel.go`
- Modify: `cmd/serf-tui/composer_render.go`

- [ ] **Step 1: Write the failing test**

```go
// append to cmd/serf-tui/composer_render_test.go

func TestComposerFooterHintsAreModeAware(t *testing.T) {
	compose := composerFooterHints("compose", 100)
	if !strings.Contains(compose, "send") {
		t.Errorf("compose mode footer should include send: %q", compose)
	}

	queue := composerFooterHints("queue", 100)
	if !strings.Contains(queue, "queue") {
		t.Errorf("queue mode footer should include queue: %q", queue)
	}

	fork := composerFooterHints("fork", 100)
	if !strings.Contains(fork, "fork") {
		t.Errorf("fork mode footer should include fork: %q", fork)
	}
}
```

- [ ] **Step 2: Run test, expect FAIL**

- [ ] **Step 3: Implement**

```go
// append to cmd/serf-tui/composer_render.go

func composerFooterHints(mode string, width int) string {
	switch mode {
	case "queue":
		return actionBarForWidth(width,
			KbdHint("enter", "queue"),
			KbdHint("ctrl+s", "steer"),
			KbdHint("esc", "browse"),
			KbdHint("⌘P", "palette"),
			KbdHint("⌘O", "dashboard"),
		)
	case "fork":
		return actionBarForWidth(width,
			KbdHint("enter", "fork"),
			KbdHint("esc", "cancel"),
			KbdHint("⌘O", "dashboard"),
		)
	case "scroll-browse":
		return actionBarForWidth(width,
			KbdHint("↑↓", "select"),
			KbdHint("enter", "expand"),
			KbdHint("f", "fork"),
			KbdHint("c", "copy"),
			KbdHint("esc", "compose"),
			KbdHint("⌘O", "dashboard"),
		)
	default: // compose
		return actionBarForWidth(width,
			KbdHint("enter", "send"),
			KbdHint("shift+enter", "newline"),
			KbdHint("tab", "toggle last tool"),
			KbdHint("⌘P", "palette"),
			KbdHint("esc", "browse"),
			KbdHint("/help", ""),
		)
	}
}
```

- [ ] **Step 4: Wire into `composer_panel.go` View**

Replace the existing footer string with `composerFooterHints(currentMode, width)`. The textarea `>` prefix and cursor styling are minor lipgloss tweaks at the spot where the textarea is rendered.

- [ ] **Step 5: Run tests and commit**

```bash
git add cmd/serf-tui/composer_render.go cmd/serf-tui/composer_render_test.go cmd/serf-tui/composer_panel.go
git commit -m "feat(tui): composer footer hints are mode-aware (wave 7 task 7.2)"
```

---

## WAVE 8 — Overlay primitive adoption

One task per overlay. Each adopts the `Overlay` primitive.

### Task 8.1: `model_picker` adopts Overlay

**Files:**
- Modify: `cmd/serf-tui/model_picker.go`
- Test: `cmd/serf-tui/model_picker_test.go` (find existing or create)

- [ ] **Step 1: Find existing model picker test**

Run: `grep -ln "modelPicker\|ModelPicker" cmd/serf-tui/*_test.go`

- [ ] **Step 2: Write the failing test**

```go
// in the model_picker test file

func TestModelPickerUsesOverlayBorder(t *testing.T) {
	mp := newModelPicker(modelPickerOpts{
		Title:     "Select model",
		Items:     []modelPickerItem{{id: "x", display: "openai/gpt-5.5"}},
		ActiveID:  "x",
	})
	got := mp.View()
	if !strings.Contains(got, "╭") || !strings.Contains(got, "╯") {
		t.Errorf("model picker should use rounded border (Overlay primitive): %q", got)
	}
	if !strings.Contains(got, "Select model") {
		t.Errorf("title should be in border: %q", got)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./cmd/serf-tui -run TestModelPickerUsesOverlay -v`
Expected: FAIL (existing picker may use different border style)

- [ ] **Step 4: Modify `cmd/serf-tui/model_picker.go`'s `View()` to delegate to `Overlay`**

```go
func (m modelPicker) View() string {
	// Build body: filter input + list rows
	body := m.renderBody()
	footer := actionBarForWidth(m.width, KbdHint("↑↓", "navigate"), KbdHint("enter", "select"), KbdHint("esc", "cancel"))
	return Overlay(OverlayOpts{
		Title:  m.title,
		Width:  m.width,
		Body:   body,
		Footer: footer,
	})
}
```

Extract the existing body-rendering into `renderBody`.

- [ ] **Step 5: Run tests**

Run: `go test ./cmd/serf-tui -run TestModelPicker -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-tui/model_picker.go cmd/serf-tui/model_picker_test.go
git commit -m "feat(tui): model_picker adopts Overlay primitive (wave 8 task 8.1)"
```

### Task 8.2: `theme_picker` adopts Overlay

**Files:**
- Modify: `cmd/serf-tui/theme_picker.go`

- [ ] **Step 1: Write the failing test**

```go
// in theme_picker_test.go (create if missing)
func TestThemePickerUsesOverlayBorder(t *testing.T) {
	tp := newThemePicker(80)
	got := tp.View()
	if !strings.Contains(got, "╭") || !strings.Contains(got, "Theme") {
		t.Errorf("theme picker should use Overlay primitive: %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**: `go test ./cmd/serf-tui -run TestThemePicker -v`

- [ ] **Step 3: Implement** — wrap existing body in `Overlay`:

```go
func (p themePicker) View() string {
	body := p.renderItems() // extract existing item-list code
	footer := actionBarForWidth(p.width, KbdHint("↑↓", "navigate"), KbdHint("enter", "select"), KbdHint("esc", "cancel"))
	return Overlay(OverlayOpts{Title: "Theme", Width: p.width, Body: body, Footer: footer})
}
```

- [ ] **Step 4: Run tests and commit**

```bash
go test ./cmd/serf-tui/...
git add cmd/serf-tui/theme_picker.go cmd/serf-tui/theme_picker_test.go
git commit -m "feat(tui): theme_picker adopts Overlay primitive (wave 8 task 8.2)"
```

### Task 8.3: `command_palette` adopts Overlay + slash-command items

**Files:**
- Modify: `cmd/serf-tui/command_palette.go`

- [ ] **Step 1: Write the failing test**

```go
func TestCommandPaletteShowsSlashItems(t *testing.T) {
	p := newCommandPalette(commandPaletteOpts{
		Items: []commandPaletteItem{
			{Slash: "/spawn", Description: "New session"},
		},
		Width: 80,
	})
	got := p.View()
	if !strings.Contains(got, "/spawn") || !strings.Contains(got, "New session") {
		t.Errorf("palette item missing: %q", got)
	}
	if !strings.Contains(got, "╭") {
		t.Errorf("palette should use Overlay primitive: %q", got)
	}
}
```

- [ ] **Step 2: Run test, expect FAIL**

- [ ] **Step 3: Implement**

```go
type commandPaletteItem struct {
	Slash       string // e.g. "/spawn"
	Description string
}

func (p commandPalette) View() string {
	th := activeThemeV2()
	rows := []string{}
	for i, item := range p.items {
		cursor := " "
		if i == p.selected {
			cursor = ">"
		}
		slash := lipgloss.NewStyle().Foreground(th.Accent).Bold(true).Render(item.Slash)
		desc := lipgloss.NewStyle().Foreground(th.TextDim).Render(item.Description)
		rows = append(rows, cursor+" "+slash+"  "+desc)
	}
	body := strings.Join(rows, "\n")
	footer := actionBarForWidth(p.width, KbdHint("↑↓", "navigate"), KbdHint("enter", "run"), KbdHint("esc", "cancel"))
	return Overlay(OverlayOpts{Title: "Commands", Width: p.width, Body: body, Footer: footer})
}
```

- [ ] **Step 4: Run tests and commit**

```bash
git add cmd/serf-tui/command_palette.go cmd/serf-tui/command_palette_test.go
git commit -m "feat(tui): command_palette adopts Overlay + slash items (wave 8 task 8.3)"
```

### Task 8.4: `credentials_panel` adopts Overlay + status badges

**Files:**
- Modify: `cmd/serf-tui/credentials_panel.go`

- [ ] **Step 1: Write the failing test**

```go
func TestCredentialsPanelShowsStatusBadges(t *testing.T) {
	cp := newCredentialsPanel(credentialsPanelOpts{
		Providers: []credProvider{
			{Name: "openai", Source: "oauth"},
			{Name: "anthropic", Source: "env"},
			{Name: "kimi", Source: "absent"},
		},
		Width: 80,
	})
	got := cp.View()
	for _, want := range []string{"OAUTH", "ENV", "ABSENT"} {
		if !strings.Contains(got, want) {
			t.Errorf("credentials missing badge %q: %q", want, got)
		}
	}
}
```

- [ ] **Step 2: Run test, expect FAIL**

- [ ] **Step 3: Implement**

```go
func (p credentialsPanel) View() string {
	th := activeThemeV2()
	rows := []string{}
	for _, prov := range p.providers {
		var badgeColor lipgloss.Color
		switch prov.Source {
		case "oauth", "env":
			badgeColor = th.StateIdle
		case "absent":
			badgeColor = th.StateEnded
		default:
			badgeColor = th.TextDim
		}
		name := lipgloss.NewStyle().Foreground(th.Text).Render(prov.Name)
		badge := StatusBadge(badgeColor, prov.Source)
		rows = append(rows, "  "+name+"  "+badge)
	}
	body := strings.Join(rows, "\n")
	footer := actionBarForWidth(p.width, KbdHint("enter", "set api key"), KbdHint("o", "OAuth"), KbdHint("c", "clear"), KbdHint("esc", "close"))
	return Overlay(OverlayOpts{Title: "Credentials", Width: p.width, Body: body, Footer: footer})
}
```

- [ ] **Step 4: Run tests and commit**

```bash
git add cmd/serf-tui/credentials_panel.go cmd/serf-tui/credentials_panel_test.go
git commit -m "feat(tui): credentials_panel adopts Overlay + status badges (wave 8 task 8.4)"
```

### Task 8.5: `launch_settings_panel` adopts Overlay

**Files:**
- Modify: `cmd/serf-tui/launch_settings_panel.go`

- [ ] **Step 1: Write the failing test**

```go
func TestLaunchSettingsPanelUsesOverlay(t *testing.T) {
	p := newLaunchSettingsPanelForTest()
	got := p.View()
	if !strings.Contains(got, "╭") || !strings.Contains(got, "Launch settings") {
		t.Errorf("launch_settings should use Overlay: %q", got)
	}
}
```

- [ ] **Step 2: Run test, expect FAIL**

- [ ] **Step 3: Implement** — wrap existing tabs+body in Overlay; preserve internal `←/→` tab switching:

```go
func (p launchSettingsPanel) View() string {
	body := p.renderTabs() + "\n\n" + p.renderActiveTab()
	footer := actionBarForWidth(p.width, KbdHint("←→", "tab"), KbdHint("↑↓", "field"), KbdHint("enter", "edit"), KbdHint("esc", "close"))
	return Overlay(OverlayOpts{Title: "Launch settings", Width: p.width, Body: body, Footer: footer})
}
```

- [ ] **Step 4: Run tests and commit**

```bash
git add cmd/serf-tui/launch_settings_panel.go cmd/serf-tui/launch_settings_panel_test.go
git commit -m "feat(tui): launch_settings_panel adopts Overlay (wave 8 task 8.5)"
```

### Task 8.6: `launch_overrides_modal` adopts Overlay

**Files:**
- Modify: `cmd/serf-tui/launch_overrides_modal.go`

- [ ] **Step 1: Write the failing test**

```go
func TestLaunchOverridesModalUsesOverlay(t *testing.T) {
	m := newLaunchOverridesModalForTest()
	got := m.View()
	if !strings.Contains(got, "╭") {
		t.Errorf("launch_overrides should use Overlay: %q", got)
	}
}
```

- [ ] **Step 2: Run test, expect FAIL**

- [ ] **Step 3: Implement** — same pattern as launch_settings_panel:

```go
func (m launchOverridesModal) View() string {
	body := m.renderFields()
	footer := actionBarForWidth(m.width, KbdHint("tab", "next"), KbdHint("enter", "save"), KbdHint("esc", "cancel"))
	return Overlay(OverlayOpts{Title: "Launch overrides", Width: m.width, Body: body, Footer: footer})
}
```

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-tui/launch_overrides_modal.go cmd/serf-tui/launch_overrides_modal_test.go
git commit -m "feat(tui): launch_overrides_modal adopts Overlay (wave 8 task 8.6)"
```

### Task 8.7: `text_input_modal` adopts Overlay (smaller width)

**Files:**
- Modify: `cmd/serf-tui/text_input_modal.go`

- [ ] **Step 1: Write the failing test**

```go
func TestTextInputModalUsesOverlay(t *testing.T) {
	m := newTextInputModal(textInputOpts{
		Title:  "Set OpenAI API key",
		Prompt: "Paste the key:",
		Width:  60,
	})
	got := m.View()
	if !strings.Contains(got, "╭") || !strings.Contains(got, "Set OpenAI API key") {
		t.Errorf("text input modal should use Overlay: %q", got)
	}
}
```

- [ ] **Step 2: Run test, expect FAIL**

- [ ] **Step 3: Implement**

```go
func (m textInputModal) View() string {
	body := m.prompt + "\n\n" + m.inputView()
	footer := actionBarForWidth(m.width, KbdHint("enter", "confirm"), KbdHint("esc", "cancel"))
	width := m.width
	if width == 0 {
		width = 60
	}
	return Overlay(OverlayOpts{Title: m.title, Width: width, Body: body, Footer: footer})
}
```

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-tui/text_input_modal.go cmd/serf-tui/text_input_modal_test.go
git commit -m "feat(tui): text_input_modal adopts Overlay primitive (wave 8 task 8.7)"
```

### Task 8.8: `notice_panel` adopts diagnostic voice (non-modal, no Overlay)

Covers spec §8.2 notice_panel. Diagnostic voice = state-colored ▍ left bar, three indented lines (`summary`, key/value cause + next).

**Files:**
- Modify: `cmd/serf-tui/notice_panel.go`

- [ ] **Step 1: Write the failing test**

```go
// in notice_panel_test.go (create if missing)
func TestNoticePanelHasStateBar(t *testing.T) {
	np := newNoticePanel(noticeInfo{
		Summary: "spawn failed: model provider not reported by harness",
		Source:  "serf",
		Cause:   "selected provider openai not in discovery",
		Next:    "refresh spawn options or choose a reported harness model",
		State:   "awaiting",
	})
	got := np.View()
	if !strings.Contains(got, "▍") {
		t.Errorf("notice should have state bar: %q", got)
	}
	if !strings.Contains(got, "source") || !strings.Contains(got, "next") {
		t.Errorf("notice should include source + next labels: %q", got)
	}
}
```

- [ ] **Step 2: Run test, expect FAIL**

- [ ] **Step 3: Implement**

```go
type noticeInfo struct {
	Summary string
	Source  string
	Cause   string
	Next    string
	State   string // "awaiting", "warning"
}

func (np noticePanel) View() string {
	th := activeThemeV2()
	stateClr := stateColor(np.info.State)
	bar := StateBar(stateClr)
	dot := lipgloss.NewStyle().Foreground(stateClr).Render("●")

	line1 := bar + " " + dot + " " + lipgloss.NewStyle().Foreground(th.Text).Render(np.info.Summary)
	line2 := "  " +
		lipgloss.NewStyle().Foreground(th.TextDim).Render("source ") +
		lipgloss.NewStyle().Foreground(th.Text).Render(np.info.Source) +
		lipgloss.NewStyle().Foreground(th.RuleSoft).Render(" · ") +
		lipgloss.NewStyle().Foreground(th.TextDim).Render("cause ") +
		lipgloss.NewStyle().Foreground(th.Text).Render(np.info.Cause)
	line3 := "  " +
		lipgloss.NewStyle().Foreground(th.TextDim).Render("next  ") +
		lipgloss.NewStyle().Foreground(th.Text).Render(np.info.Next)
	return strings.Join([]string{line1, line2, line3}, "\n")
}
```

- [ ] **Step 4: Run tests and commit**

```bash
git add cmd/serf-tui/notice_panel.go cmd/serf-tui/notice_panel_test.go
git commit -m "feat(tui): notice_panel diagnostic voice with state bar (wave 8 task 8.8)"
```

### Task 8.9: `details_drawer` adopts section labels + status badge + ghost chrome

Covers spec §8.2 details_drawer.

**Files:**
- Modify: `cmd/serf-tui/details_drawer.go`

- [ ] **Step 1: Write the failing test**

```go
// in details_drawer_test.go (create if missing)
func TestDetailsDrawerHasSectionLabels(t *testing.T) {
	d := detailsDrawer{Detail: hubSessionDetail{
		Title: "Test",
		State: "awaiting",
		Model: "openai/gpt-5.5",
	}}
	got := d.View()
	if !strings.Contains(got, "AWAITING") {
		t.Errorf("details drawer should show state badge: %q", got)
	}
}
```

- [ ] **Step 2: Run test, expect FAIL**

- [ ] **Step 3: Implement**

In `details_drawer.go` `View()`, add section labels via `lipgloss.NewStyle().Foreground(th.TextDim).Bold(true).Render(strings.ToUpper(label))`, status badge via `StatusBadge(stateColor(state), state)`, durations and ages in `th.TextGhost`. Keep existing data; just restyle.

- [ ] **Step 4: Run tests and commit**

```bash
git add cmd/serf-tui/details_drawer.go cmd/serf-tui/details_drawer_test.go
git commit -m "feat(tui): details_drawer adopts section labels + status badge (wave 8 task 8.9)"
```

---

## WAVE 9 — Focus traps

### Task 9.1: `topmostOverlay` helper + key routing

**Files:**
- Create: `cmd/serf-tui/focus_trap.go`
- Create: `cmd/serf-tui/focus_trap_test.go`
- Modify: `cmd/serf-tui/hub_model.go`

- [ ] **Step 1: Write the failing test**

```go
// cmd/serf-tui/focus_trap_test.go
package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCmdPRejectedWhenCredentialsOpen(t *testing.T) {
	m := newHubModelForTest()
	m.credentialsPanel = newCredentialsPanelForTest()
	before := m.commandPalette
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	after := updated.(hubModel).commandPalette
	if before == nil && after != nil {
		t.Errorf("ctrl+P should be trapped while credentials panel open; palette opened anyway")
	}
}

func TestEscClosesTopmostOverlayOnly(t *testing.T) {
	m := newHubModelForTest()
	m.credentialsPanel = newCredentialsPanelForTest()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if updated.(hubModel).credentialsPanel != nil {
		t.Errorf("esc should close credentials panel")
	}
}

func TestCtrlOEscapesAllOverlays(t *testing.T) {
	m := newHubModelForTest()
	m.credentialsPanel = newCredentialsPanelForTest()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	finalM := updated.(hubModel)
	if finalM.credentialsPanel != nil {
		t.Errorf("ctrl+o should close credentials panel")
	}
	if finalM.mode != hubModeDashboard {
		t.Errorf("ctrl+o should return to dashboard; got mode %v", finalM.mode)
	}
}
```

(`newHubModelForTest` and `newCredentialsPanelForTest` are test helpers you may need to add or already exist in the repo. Check first with `grep -n "newHubModelForTest\|newCredentialsPanelForTest" cmd/serf-tui/*_test.go`.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/serf-tui -run "TestCmdP|TestEscClosesTopmost|TestCtrlOEscapes" -v`
Expected: FAIL

- [ ] **Step 3: Implement focus_trap.go**

```go
// cmd/serf-tui/focus_trap.go
package main

import (
	tea "github.com/charmbracelet/bubbletea"
)

// topmostOverlayName returns the name of the most-recently-opened
// overlay on hubModel, or "" if none.
func topmostOverlayName(m hubModel) string {
	// Order: last-opened first. Each overlay field on hubModel.
	if m.followupModal != nil {
		return "followup"
	}
	if m.launchOverridesModal != nil {
		return "launch-overrides"
	}
	if m.credentialsPanel != nil {
		return "credentials"
	}
	if m.launchSettingsPanel != nil {
		return "launch-settings"
	}
	if m.sessionPanel != nil {
		return "session-panel"
	}
	if m.commandPalette != nil {
		return "command-palette"
	}
	if m.sessionModelPicker != nil || m.sessionThemePicker != nil || m.sessionTranscriptPicker != nil {
		return "picker"
	}
	return ""
}

// keyAllowedThroughTrap returns true ONLY for the two global escape
// hatches: esc (close topmost) and ctrl+o (escape to dashboard).
func keyAllowedThroughTrap(msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlO:
		return true
	}
	return false
}
```

- [ ] **Step 4: Update `hubModel.Update` to route through the trap**

At the top of `hubModel.Update`, after the type switch on `msg`:

```go
func (m hubModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if topmost := topmostOverlayName(m); topmost != "" && !keyAllowedThroughTrap(msg) {
			// Route key to the topmost overlay's update; if it doesn't
			// recognize the key, drop it (no passthrough to hub model).
			return m.dispatchOverlayKey(topmost, msg)
		}
		// ... existing key handling ...
```

`dispatchOverlayKey` walks down the overlay stack and invokes the right Update:

```go
func (m hubModel) dispatchOverlayKey(name string, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch name {
	case "followup":
		updated, cmd := m.followupModal.Update(msg)
		m.followupModal = updated
		return m, cmd
	// ... one case per overlay ...
	}
	return m, nil
}
```

(The exact shape of each overlay's Update varies; refer to the existing code patterns in `hub_model.go`.)

- [ ] **Step 5: Run tests**

Run: `go test ./cmd/serf-tui -run "TestCmdP|TestEscClosesTopmost|TestCtrlOEscapes|TestFocusTrap" -v`
Expected: PASS

- [ ] **Step 6: Run full suite**

Run: `go test ./cmd/serf-tui/...`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add cmd/serf-tui/focus_trap.go cmd/serf-tui/focus_trap_test.go cmd/serf-tui/hub_model.go
git commit -m "feat(tui): focus-trap helper + key routing (wave 9 task 9.1)"
```

---

## WAVE 10 — MCP fallback enrichment

Wave 5 task 5.1 already registered an MCP fallback returning `provider` verb + `operation` target + `ok` result. Wave 10 enriches it: pretty-print JSON body + extract first 1-3 string args into target.

### Task 10.1: Enrich MCP fallback target with first args

**Files:**
- Modify: `cmd/serf-tui/tool_renderers.go`
- Modify: `cmd/serf-tui/tool_renderers_test.go`

- [ ] **Step 1: Write the failing test**

```go
// append to cmd/serf-tui/tool_renderers_test.go

func TestMCPFallbackTargetIncludesFirstArgs(t *testing.T) {
	r, _ := lookupToolRenderer("linear__search")
	args := toolArgsFromJSON(`{"query":"oncall","filter":"open"}`)
	target := r.Target(args)
	if !strings.Contains(target, "search") {
		t.Errorf("MCP target should include operation: %q", target)
	}
	if !strings.Contains(target, "oncall") {
		t.Errorf("MCP target should include first string arg: %q", target)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-tui -run TestMCPFallback -v`
Expected: FAIL (current target is just the operation name)

- [ ] **Step 3: Enhance `mcpFallbackRenderer`**

```go
func mcpFallbackRenderer(tool string) ToolRenderer {
	provider, op, _ := strings.Cut(tool, "__")
	return ToolRenderer{
		Verb: func(_ ToolArgs) string { return provider },
		Target: func(args ToolArgs) string {
			parts := []string{op}
			added := 0
			for _, k := range sortedKeys(args) {
				if added >= 3 {
					break
				}
				if v, ok := args[k].(string); ok && v != "" {
					if len(v) > 40 {
						v = v[:40] + "…"
					}
					parts = append(parts, v)
					added++
				}
			}
			return strings.Join(parts, " ")
		},
		Result: func(_ ToolArgs, output, errStr string, _ time.Duration) string {
			if errStr != "" {
				return "error"
			}
			return "ok"
		},
		Body: jsonBody,
	}
}

func sortedKeys(m ToolArgs) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
```

Add `jsonBody`:

```go
// jsonBody renders pretty-printed, chroma-highlighted JSON output.
func jsonBody(_ ToolArgs, output string, width int) string {
	if output == "" {
		return ""
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, []byte(output), "", "  "); err != nil {
		return output
	}
	if h := highlightBlock(pretty.String(), "json"); h != "" {
		return h
	}
	return pretty.String()
}
```

(Both `sortedKeys` and `jsonBody` go in `tool_renderers.go` or a shared helper file.)

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/serf-tui -run TestMCPFallback -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-tui/tool_renderers.go cmd/serf-tui/tool_renderers_test.go
git commit -m "feat(tui): MCP fallback includes first args + jsonBody (wave 10 task 10.1)"
```

### Task 10.2: Apply `jsonBody` to unknown-tool fallback too

- [ ] **Step 1: Test**

```go
func TestUnknownToolHasJSONBody(t *testing.T) {
	r, _ := lookupToolRenderer("unknown_tool_xyz")
	if r.Body == nil {
		t.Errorf("unknown tool renderer should have jsonBody")
	}
}
```

- [ ] **Step 2: Set body**

```go
func unknownToolRenderer(tool string) ToolRenderer {
	return ToolRenderer{
		Verb:   func(_ ToolArgs) string { return tool },
		Target: func(args ToolArgs) string { return "" },
		Result: func(_ ToolArgs, _, errStr string, _ time.Duration) string {
			if errStr != "" { return "error" }
			return "ok"
		},
		Body: jsonBody,
	}
}
```

- [ ] **Step 3: Run + commit**

```bash
git add cmd/serf-tui/tool_renderers.go cmd/serf-tui/tool_renderers_test.go
git commit -m "feat(tui): unknown-tool fallback gets jsonBody (wave 10 task 10.2)"
```

---

## Final wave — testing closure

### Task F.1: Golden corpus runs against dark + light

**Files:**
- Modify: `cmd/serf-tui/tui_samples_test.go`

- [ ] **Step 1: Update the golden runner**

Find the existing test that iterates `corpus.Renders` (e.g. `TestHubTUISampleCorpusHasGoldenRendersForCoreSurfaces`). Wrap the inner loop with `runWithTheme` for each theme.

```go
func TestGoldenRendersAcrossThemes(t *testing.T) {
	corpus := newHubTUISampleCorpus()
	for _, theme := range []string{"dark", "light"} {
		runWithTheme(t, theme, func() {
			for _, sample := range corpus.Renders {
				t.Run(theme+"/"+sample.Name, func(t *testing.T) {
					// Re-render via sampleRenderFromRealWidget if applicable.
					actual, ok := sampleRenderFromRealWidget(sample.Name, sample.Width)
					if !ok {
						t.Skipf("no realtime renderer for %q", sample.Name)
					}
					for _, want := range sample.Contains {
						if !strings.Contains(actual.View, want) {
							t.Errorf("theme=%s sample=%s missing %q", theme, sample.Name, want)
						}
					}
				})
			}
		})
	}
}
```

- [ ] **Step 2: Run + commit**

```bash
go test ./cmd/serf-tui -run TestGoldenRendersAcrossThemes -v
git add cmd/serf-tui/tui_samples_test.go
git commit -m "test(tui): golden corpus runs against dark+light (final wave task F.1)"
```

### Task F.2: Smoke test the running binary

- [ ] **Step 1: Build the binary**

```bash
go build -o /tmp/serf-tui-final ./cmd/serf-tui
```

- [ ] **Step 2: Run smoke tests**

The TUI requires a running hub. Use the audit-hub pattern from earlier work:

```bash
mkdir -p /tmp/audit-home/.serf
HOME=/tmp/audit-home /tmp/serf-hub-new --addr 127.0.0.1:9281 > /tmp/hub.log 2>&1 &
HOME=/tmp/audit-home /tmp/serf-tui-final --hub 127.0.0.1:9281 &
TUI_PID=$!
sleep 2
# Send q to gracefully quit
kill -SIGTERM $TUI_PID
```

If the TUI dies on startup, fix the regression. If it starts cleanly, the smoke test passes.

- [ ] **Step 3: Document the cleanup**

```bash
pkill -f serf-tui-final
pkill -f serf-hub-new
rm -rf /tmp/audit-home /tmp/serf-tui-final /tmp/serf-hub-new
```

- [ ] **Step 4: Final commit (if any cleanup work landed)**

```bash
git status
```

If no staged changes, no final commit needed.

---

## Plan summary

| Wave | Tasks | Scope |
|------|-------|-------|
| 1    | 1.1 – 1.5 | Theme registry foundation |
| 1.5  | 1.5.1 – 1.5.2 | Golden corpus theme isolation |
| 2    | 2.1 – 2.5 | Primitives |
| 3    | 3.1 – 3.4 | Dashboard rewrite |
| 4    | 4.1 – 4.4 | Session header + statusbar + conversation rhythm + fork |
| 5    | 5.1 – 5.6 | Tool renderer registry |
| 6    | 6.1 – 6.5 | Tool body renderers |
| 7    | 7.1 – 7.2 | Composer chip strip + mode chip + mode-aware footer |
| 8    | 8.1 – 8.9 | Overlay adoption + notice + drawer |
| 9    | 9.1     | Focus traps |
| 10   | 10.1 – 10.2 | MCP + unknown fallback enrichment |
| Final| F.1 – F.2 | Cross-theme golden coverage + smoke test |

**Total tasks:** 42 across 12 waves.

Each task: TDD (failing test → implementation → passing test) + commit. Every wave leaves the branch in a build-green, test-green state. Goldens update inline as visual changes land.

**Estimated total commits:** 36–40 (some tasks split across multiple logical commits).

**Out of scope (file as follow-up katas if encountered during implementation):**
- Subagent inline depth-2 rendering (kata filed in task 6.4)
- toolCallInfo.RawArgs field if it doesn't yet exist (mentioned in task 5.5)
- Threading session state into renderMessage (mentioned in task 4.3) — file a kata when starting task 4.3 if the simple default isn't acceptable
- Mouse support / multi-pane workspace / plugin-installed renderers (per spec §11)
