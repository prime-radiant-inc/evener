# Serf TUI Deep UX Pass — Design

**Status:** Draft 1 · 2026-05-24
**Target branch:** `tui-ux-pass`
**Companion artifacts:**
- `docs/superpowers/specs/2026-05-22-serf-hub-design-language.md` — the workshop-log identity established for the web side; this spec ports it to the terminal.
- `docs/superpowers/specs/2026-05-23-serf-hub-design-system.md` — post-implementation reference for web tokens/components; this spec adapts them where lipgloss differs.

## 1 · Why

The TUI is functional but visually flat. Dashboard rows are space-joined token streams. The session header is three lines of `key: value  key: value` pairs. Tool calls render as one-line summaries with no diff coloring, no expandable body, no per-tool richness. The composer is bare. The chrome competes with the content because every glyph carries the same visual weight.

The web side just finished a workshop-log identity pass — paper-grain texture, two-voice typography, state-colored row accents, typographic status badges, rich per-tool renderers, diff color bars, mobile responsive density. The TUI should feel like the same product: same color tokens, same voice, same patterns adapted to terminal capabilities.

This spec covers everything: identity, tool renderers, session view, dashboard, composer, overlays, focus traps. Behavior changes are in scope where they improve the UX (mode chips, focus traps, ghost-dim chrome). RPC contracts, transcript schema, hub protocol — out of scope.

## 2 · Goals & non-goals

**Goals**
- Workshop-log identity in the terminal: state-colored left bars, typographic status badges, section dividers, ghost-dim chrome, right-floated duration.
- A per-tool renderer registry mirroring the web side's structure — 17+ named renderers, MCP fallback, unknown-tool fallback.
- Rich tool-call bodies: diff coloring, file preview with chroma highlighting, per-task task-list rendering, subagent body.
- Focus traps on overlays; consistent escape discipline.
- Composer chip strip, mode chip, persistent statusbar.
- Token-driven themes (configurable, even if hardcoded).
- Light + dark theme parity.

**Non-goals**
- No RPC, transcript, or hub-protocol changes.
- No mouse support enablement.
- No multi-pane workspace.
- No plugin-installed renderers (use MCP fallback).
- No search-across-past-sessions feature work.

## 3 · Foundations

### 3.1 · Theme registry

Replace ad-hoc style globals in `styles.go` with a `Theme` struct that holds every token, plus a `themes` registry keyed by name. Themes are first-class: `setTheme(name)` swaps the active struct; adding a new theme requires only a struct literal.

```go
type Theme struct {
    Name string

    // Surface colors
    Bg, BgRaised, SurfaceSecondary lipgloss.Color
    Rule, RuleSoft                 lipgloss.Color

    // Text tier (each step quieter than the last)
    Text, TextMuted, TextDim, TextGhost lipgloss.Color

    // Brand + state
    Accent, AccentSecondary                    lipgloss.Color
    StateAwaiting, StateProcessing             lipgloss.Color
    StateWarning, StateIdle, StateEnded        lipgloss.Color
    StateSubagent                              lipgloss.Color
    BtnPrimaryText                             lipgloss.Color // contrast-correct for the theme

    // Layout
    IndentToolBody, IndentSubagent int
    GapTurn, GapSection            int  // blank-line counts
    ColumnDur                      int  // right-aligned duration column width
    LeftBarGlyph, RuleGlyph        string
}
```

Registry seeded with two hardcoded entries: `dark` (the existing dark palette, augmented with the new `TextGhost` tier and missing state tokens) and `light` (same shape, light palette).

`tokens.go` (new) holds the struct definition + the registry + `setTheme(name string)` + `activeTheme() Theme`. The existing `applyTheme(t colorTheme)` function in `styles.go` becomes a thin wrapper that pulls from `activeTheme()`.

**Text tier semantics:**

| Token       | Use                                                                  |
|-------------|----------------------------------------------------------------------|
| `Text`      | Primary content: titles, prose body, verb names, state badge text.   |
| `TextMuted` | Secondary content: keys in key/value, captions, picker dim items.    |
| `TextDim`   | Chrome the user reads on demand: section labels, kbd hints, age.     |
| `TextGhost` | Chrome the eye should skip: durations, dot-leaders, separator dots.  |

**State color usage:**

| State       | Color           | Use                                                         |
|-------------|-----------------|-------------------------------------------------------------|
| `Awaiting`  | pink (`#f7768e`)| Session awaiting user input; error severity.                |
| `Processing`| blue (`#7aa2f7`)| Session actively running; in-flight work.                   |
| `Warning`   | amber           | Diff hunk headers, attention banners, read-only access mode.|
| `Idle`      | green           | Completed turns, healthy connection, successful tool calls. |
| `Ended`     | dim gray        | Closed sessions, completed agents.                          |
| `Subagent`  | violet          | Subagent rows, subagent-spawned tool calls.                 |

**Tint tokens (precomputed bg fills).** lipgloss has no `color-mix` equivalent and runtime alpha-blending isn't reliable across terminals. Tint values are precomputed and stored as separate theme tokens:

```go
// Within Theme struct, per state:
StateAwaitingTint   lipgloss.Color  // hex of state-awaiting mixed onto Bg at 12%
StateProcessingTint lipgloss.Color  // ditto
StateWarningTint    lipgloss.Color
StateIdleTint       lipgloss.Color
AccentTint          lipgloss.Color  // hex of accent mixed onto Bg at 6% — used for selected-row backgrounds
```

These are hardcoded hex values per theme. Computed once when the theme struct is defined. On terminals without truecolor support (lipgloss auto-detects), the tints degrade to the nearest 256-color cell or are dropped entirely — selection then reverts to the `Reverse` attribute on the state-bar glyph only.

**Migration scope acknowledgement.** Wave 1 replaces ~20 package-level `lipgloss.Style` globals in `styles.go` (`statusBarStyle`, `userBlockStyle`, `thinkingStyle`, etc.) with getter functions reading from `activeTheme()`. ~50 callsites across `hub_model.go`, `message.go`, `composer_panel.go`, `picker_panel.go`, `credentials_panel.go`, `details_drawer.go`, `notice_panel.go`, `launch_settings_panel.go`, `launch_overrides_modal.go`, `text_input_modal.go` need their references updated. This is the deepest wave-1 work; budget accordingly. The migration preserves global accessibility — callers still write `styles().UserBlock` (one extra parenthesis) rather than `userBlockStyle`.

**Thread safety.** `setTheme(name)` mutates package-level state. Bubbletea's `Update`/`View` run on a single goroutine; theme changes are dispatched as `tea.Msg` values handled in `Update`, so reads in `View` see a consistent value. We document this single-goroutine constraint inline and add a `// not safe for concurrent use` comment on `setTheme`. No mutex; we don't pay for one we don't need.

**Markdown renderer invalidation.** Glamour-based `markdownRenderer` (in `message.go`) caches its theme at creation. `setTheme` must call `resetMarkdownRenderer()` so the next prose render rebuilds it with the new style. Cheap (one nil-out). Adds one line to the `setTheme` path.

### 3.2 · Primitives

New file `primitives.go` exports small rendering helpers used across surfaces.

**`StateBar(state lipgloss.Color) string`** — returns the 1-column `▍` glyph foreground-colored to `state`. Default for all session and dashboard rows.

**`FocusedStateBar(state lipgloss.Color) string`** — returns `▍▍` (two 1-column glyphs concatenated, total visual width 2) for selected/focused rows. Layout math in the caller must account for the extra column when measuring right-aligned content. We do NOT use the wider single-character glyphs (`▌▋▊`) — their width-1 monospace cells render inconsistently across emulators.

**`StatusBadge(state lipgloss.Color, label string) string`** — reverse-pill rendering. Example: `● AWAITING` in `StateAwaiting` bg with `Bg` fg, bold, mono uppercase. Falls back to non-reverse colored text on terminals that report no truecolor support.

**`SectionDivider(width int, left, right string) string`** — renders `─ left ─────…────── right ┄` where `left` is bold dim-uppercase and `right` is ghost-dim. The `─` is `RuleSoft`; trailing `┄` is `Rule`.

**`KbdHint(key, action string) string`** — renders `<reverse-key> action` with `key` in reverse and `action` in `TextDim`. Used in footers and overlay action rows.

**`Overlay(opts OverlayOpts) string`** — unified overlay frame with title baked into the top border, body, and footer (kbd hints). Used by all modals/pickers/panels.

**`DotLeader(target, result string, width int) string`** — renders `target ······· result` filling middle with `TextGhost` dots. Used by tool-call rows.

### 3.3 · Voice (limited terminal typography)

The terminal vocabulary is small: bold, italic, dim, underline, reverse. We commit to:

- **Bold** = titles, primary action labels, verb names in tool calls, section header labels.
- **Dim/Faint** = chrome tier; combined with color tokens (`TextDim`, `TextGhost`).
- **Italic** = reserved for assistant-prose subtleties (e.g. inline emphasis from markdown rendering); not used for chrome.
- **Reverse** = status badge pill background, focused row selection, kbd-key chips.
- **Underline** = NOT used (too easy to mistake for hyperlinks).

No simulated letter-spacing. Section labels are bold + dim + uppercase, single-spaced.

## 4 · Tool renderer registry

### 4.1 · Architecture

Replace `tool_summary.go`'s `switch` with a registry of `ToolRenderer` structs keyed by canonical tool name.

```go
type ToolRenderer struct {
    Verb              func(args ToolArgs) string
    Target            func(args ToolArgs) string
    Result            func(args ToolArgs, output string, dur time.Duration, err string) string
    Body              func(args ToolArgs, output string, w int) string  // nil = no body
    ExpandedByDefault bool                                              // edit/write/patch only
}
```

`renderToolCall` in `message.go` consumes the registry:

1. Look up `toolRenderers[tool]` (or alias map).
2. If missing and name contains `__`, use MCP fallback renderer.
3. If still missing, use unknown-tool fallback.
4. Compose the row: `▍ ✓ <verb> <target> ················· <result> · <dur>`.
5. If `Body != nil` and (`focused` or `ExpandedByDefault`), append the indented body.

### 4.2 · Canonical renderers

| Tool name      | Aliases                                       | Verb        | Target source         | Result               |
|----------------|-----------------------------------------------|-------------|------------------------|----------------------|
| `read_file`    |                                               | `read`      | `file_path`/`path`     | `<N> lines`          |
| `write_file`   |                                               | `write`     | `file_path`            | diff stats           |
| `edit_file`    |                                               | `edit`      | `file_path`            | diff stats           |
| `apply_patch`  |                                               | `patch`     | files from patch hunk  | diff stats           |
| `shell`        | `exec_command`, `run_shell_command`           | `shell`     | first 80 chars of cmd  | exit code or `ok`    |
| `grep`         | `grep_files`, `grep_search`                   | `grep`      | pattern + path         | `<N> hits`           |
| `glob`         |                                               | `glob`      | pattern                | `<N> matches`        |
| `list_dir`     | `list_directory`                              | `ls`        | path                   | `<N> entries`        |
| `web_fetch`    |                                               | `fetch`     | url                    | `<N> bytes`          |
| `web_search`   |                                               | `search`    | first 80 chars of query| `<N> results`        |
| `spawn_agent`  |                                               | `spawn`     | task summary 80 chars  | turns + status       |
| `resume_agent` |                                               | `resume`    | agent id (short)       | `ok` / `error`       |
| `wait`         |                                               | `wait`      | agent id (short)       | `ok` / `error`       |
| `close_agent`  |                                               | `close`     | agent id (short)       | `ok` / `error`       |
| `task_list`    |                                               | `tasks`     | step count summary     | progress fraction    |
| `use_skill`    |                                               | `skill`     | skill name             | `ok` / `error`       |
| `communicate`  |                                               | (hidden — message body absorbed into assistant prose; tool row suppressed)        |

Plus default-MCP and default-unknown:

- **Default MCP** (name contains `__`): split `provider__operation`, verb = `provider`, target = `operation` + first 1-3 string args, result = `ok`/`error`, body = pretty-printed JSON output (chroma-highlighted as JSON).
- **Default unknown** (no `__`, no registry hit): verb = full tool name, target = first 80 chars of args JSON, result = `ok`/`error`, body = same JSON dump.

### 4.3 · Body renderers

**`diffBody`** (edit, write, apply_patch — `ExpandedByDefault = true`):

Today, `tool_summary.go` already produces a chroma-syntax-highlighted unified diff for `edit_file` via `unifiedDiff()` + `highlightDiff()`. The new `diffBody` REPLACES that path with a two-layer renderer:

1. **Layer 1 (line classification + background tint, the new behavior)**: split output on `\n`, classify each line by its first character, set a line-wide background:
   - `+`-prefixed: bg = `StateIdleTint` (precomputed token; see §3.1).
   - `-`-prefixed: bg = `StateAwaitingTint`.
   - `@@`-prefixed: bold + fg `StateWarning`, no bg.
   - context (space-prefixed or no prefix): fg `Text`, no bg.
2. **Layer 2 (chroma syntax highlight, preserved from existing code)**: still runs over the per-line content AFTER stripping the prefix; the bg tint from layer 1 wraps the highlighted content. The existing `highlightDiff()` function gets refactored: extract a `highlightLine(content string, lang string) string` helper used by the new diff renderer; keep `unifiedDiff` as the input-builder.

Net effect: green/pink row backgrounds on +/- lines AND syntax-highlighted code content within them. On terminals without truecolor (lipgloss auto-detects), bg tints fall back to ANSI background blocks of nearest-color or drop entirely; the chroma fg highlighting still works.

Indent: render the whole body under the tool-call row by `IndentToolBody`.

**`fileBody`** (read_file):
- Chroma-highlight the output by extension (e.g. `.go` → `go` lexer). Falls back to plain text.
- Show first 5 lines + `▸ show N more lines` (in `TextDim`) if longer.
- Expanded via row-focus + `enter` toggles full render.

**`taskListBody`** (task_list — `ExpandedByDefault = true`; the agent's coordination state is always relevant):
- Render each task as its own indented line.
- Per-task glyph based on state: `[✓]` in `StateIdle`, `[⠋]` (steady; pick frame from existing spinner table) in `StateProcessing`, `[ ]` in `TextDim`.
- Task description after the glyph in `Text`.
- Per-task duration right-aligned in `TextGhost` if available.

**`subagentBody`** (spawn_agent):

- One-line summary above body: `subagent <kind> (<turns> turns, <status>) ▸ N tool calls`.
- Expanded: render child turns indented under the parent's left bar.

**Data source.** Child turns live in a separate transcript file (`<state_dir>/sessions/<child-session-id>.transcript.jsonl`) — they are NOT in the current session's `messages` slice. The renderer loads them on demand the first time the subagent body is expanded (cache hit on subsequent renders). Loading is sync today via `loadTranscriptMessages` (already used by transcript view); we extend it for subagents. No RPC or schema change — files exist on disk for any spawned subagent.

**Recursion depth cap.** Subagents can themselves spawn subagents. Render up to depth 2 inline (parent → child → grandchild). Past depth 2, replace the body with `▸ N nested turns (open subagent to view)` — the user navigates into the subagent session for deeper inspection.

**Width budget.** Each nesting level reduces effective width by `IndentSubagent` (2 chars). If `width - depth*IndentSubagent < 30`, suppress nested body for that branch and show `(N turns)` summary only. Prevents lipgloss panic on zero/negative widths.

**Failure.** If the child transcript file can't be loaded, render `(transcript unavailable: <err>)` in `StateWarning` instead of the body.

**`shellBody`** (shell):
- Render output chroma-highlighted as `bash` (or per-shell detection).
- First 10 lines + collapse hint, expandable.

**`webSearchBody`** (web_search):
- Render results as ordered list: `1. <title> — <url>`. URL in `TextGhost`.

### 4.4 · Expand/collapse interaction

Existing `toolCallInfo.Expanded bool` field carries state. Default `false` except for `ExpandedByDefault` renderers.

**Tool focus lives inside scroll-browse mode — no separate "tool-focus" mode.** The existing scroll-browse already cycles through messages (including tool messages) via `↑`/`↓`. We extend the existing model.

Keyboard:
- `Esc` from compose enters scroll-browse (existing behavior, unchanged).
- `↑`/`↓` cycle through visible messages, including tool messages. Already implemented.
- `Enter` on a focused tool message toggles `Expanded`. New behavior. On non-tool messages, `Enter` is a no-op.
- `Esc` from scroll-browse returns to compose. Existing.
- `Tab` in compose mode keeps its existing behavior (toggle the most recent tool call's `Expanded` flag — see `model.go:406-415`). Power-user shortcut.
- Focused tool row carries `FocusedStateBar(Accent)` (the `▍▍` double-bar).

Footer hint updates in scroll-browse:
- `↑↓ select · enter expand · f fork · c copy · esc compose · ⌘O dashboard`

Footer hint in compose (unchanged):
- `enter send · shift+enter newline · tab toggle last tool · ⌘P palette · esc browse · /help`

The `tab toggle last tool` is added to the existing compose footer so the power-user keybinding is discoverable.

## 5 · Session view

### 5.1 · Header

Three rendered lines replace the current five.

**Line 1 — top rule (workshop-log voice):**
```
─ SERF / SESSION ────────────────────────────────────────── 12 turns ┄
```
- Left: breadcrumb `SERF / SESSION`, bold dim uppercase via `SectionDivider`.
- Right: turn count in `TextGhost`.
- `─` segments in `RuleSoft`, trailing `┄` in `Rule`.

**Line 2 — title + state badge:**
```
  Restore hub TUI widgets   ● AWAITING
```
- Title in `Text` bold, left-aligned with 2-char indent.
- `StatusBadge` to the right of the title (within the same line; gap of 2 chars between title and badge).
- Badge color = `StateX` for current session state.

**Line 3 — meta strip:**
```
  src serf · branch feat/widget · model opus-4.7 · ~/git/serf
```
- Keys (`src`, `branch`, `model`) in `TextDim`.
- Values in `Text`.
- `·` separators in `RuleSoft`.
- Model name abbreviated via the same helper as web (drop provider prefix, drop date suffix).
- Path middle-truncated when too long.
- Hidden cells when their value is absent (e.g. branch when no git context).

### 5.2 · Conversation rendering

**Turn separators**: one-line `┄` divider in `RuleSoft` between turn clusters. No divider inside a cluster (assistant message immediately followed by its tool calls is one cluster).

**User turn**:
- Bar `┃` (heavier) in `Accent` on the left.
- 2-char indent.
- Prefix `>` in `Accent`.
- Body in `Text`.

**Assistant prose turn**:
- Bar `▍` in the current session state color.
- 2-char indent.
- Markdown rendered via existing renderer.

**Tool call cluster** (sequence of `msgTool` after an assistant turn):
- Each row: `▍ ✓ verb target ······································· result · dur`.
- `▍` in session state color (or `StateIdle` per tool when completed and the session is still active).
- `✓` (or `✗` for error) in tool's success color: `StateIdle` for success, `StateAwaiting` for error.
- Verb in `Accent`, bold.
- Target in `Text`.
- Dot-leaders in `TextGhost`.
- Result text in `TextDim`.
- Duration right-floated in `TextGhost`.

**Subagent nested rows**: indent by `IndentSubagent`, use `StateSubagent` color on the bar.

**Communicate messages**: rendered as plain assistant prose (kind `msgCommunicate`), no special chrome — the message body IS the content; no need for a separate visual category.

**System messages** (`msgSystem`): single line, `TextDim` italic, no left bar. Used for `↻ steering injected` etc.

**Notice placement**: diagnostic notices (`notice_panel`, see §8.2) render between the header (§5.1) and the conversation. One blank line separates them from each side. They participate in the conversation scroll flow — when the user scrolls back through history, old notices scroll with the content.

### 5.3 · Scroll-browse mode

Entered via `Esc` in compose. Existing behavior; new visuals:

- Selected turn: bar widens to `FocusedStateBar(Accent)` (the `▍▍` glyph), optional bg tint via the `AccentTint` token on truecolor terminals.
- Composer collapses to a one-line dim banner:
  ```
  ─ browse mode · ↑↓ select · enter expand · f fork · c copy · esc compose ┄
  ```
- Textarea hidden.

### 5.4 · Fork mode

Entered via `f` on a user turn in scroll-browse. Existing behavior; new visuals:

- Composer label becomes a section rule:
  ```
  ─ fork draft from turn 1 ─────────────────────────── feat/widget@diverge ┄
  ```
- Diverge point + branch right-aligned in `StateWarning`.
- Enter forks; Esc cancels (clears draft).

## 6 · Dashboard

### 6.1 · Top rule + actions

```
─ SERF LIVE ────────────────────────────────────── http://hub.test · 3 live ┄

  + new session  ⌘N
  / filter sessions
```

- `SERF LIVE` left, hub URL + live count right (URL in `TextGhost`, count in `TextDim`).
- Two compose-style action rows below the rule (one blank line gap). Each row is a `KbdHint` for its binding.

### 6.2 · Project sections

```
  ▾ ● SERF                                                  2 live · 1 recent
```

- `▾` / `▸` chevron in `TextDim`.
- Rollup-state dot `●` colored by highest-urgency child (awaiting > processing > warning > idle > ended).
- Project name in `TextDim` bold uppercase.
- Right-aligned summary: `<N> live · <M> recent` (omit `recent` when zero).

### 6.3 · Session rows

```
  ▍ ● Stream markdown without flicker            serf  opus-4.7  active   4m
```

- 2-char indent.
- `StateBar(row.state)` (`▍`).
- `●` colored by row state.
- Title fills available width (clamps with ellipsis).
- Right-aligned tail group: `source · model-short · state-label · age`.
  - Each in `TextDim`, except `state-label` which is in its state color when notable (`active`, `awaiting`).
- Subagent rows: indent + 2, bar uses `StateSubagent`, glyph stays `●`.
- Fork rows: glyph `⎇` in place of `●`, color by row state.
- Selected row: `FocusedStateBar(Accent)`, background tint via the `AccentTint` token where truecolor available.
- Drop the `├─` / `└─` tree connectors — indent + bar carries the hierarchy.

### 6.4 · Wide-mode details drawer

Existing wide layout (>120 cols) splits dashboard / details drawer. Drawer adopts:
- Section labels in `TextDim` bold uppercase.
- Status badge at the top for current row's state.
- Key/value pairs with keys in `TextMuted`, values in `Text`.
- Durations + ages in `TextGhost`.

### 6.5 · Footer

```
↑↓ select · enter open · n new · / filter · ⌘O dashboard · q quit
```

Each kbd token via `KbdHint`. Wraps onto multiple lines on narrow widths via existing `actionBarForWidth`.

## 7 · Composer + statusbar

### 7.1 · Composer chip strip

One line above the textarea, doubling as a section divider:

```
─ harness serf · model opus-4.7 · branch feat/widget · cwd ~/serf ┄────────
```

- Keys in `TextDim`, values in `Text`.
- `·` separators in `RuleSoft`.
- Model abbreviated via shared helper.
- Path middle-truncated when long.
- Display-only — chip pickers (`⌘M` model, `⌘D` dir, etc.) remain overlays.

### 7.2 · Mode chip (right-aligned)

When the composer is in any non-default mode, a `StatusBadge`-styled chip appears right-aligned on the chip strip:

| Mode           | Trigger                                   | Chip       | Chip color       |
|----------------|-------------------------------------------|------------|------------------|
| compose        | default                                   | (none)     | —                |
| queue          | turn busy + queueable                     | `QUEUE N`  | `StateProcessing`|
| steer          | brief flash on `⇧↵` while active          | `STEER`    | `Accent`         |
| fork draft     | `f` on user turn in scroll-browse         | `FORK DRAFT` | `StateWarning` |
| awaiting input | session in `awaiting`                     | `AWAITING` | `StateAwaiting`  |

Mode-chip placement:
```
─ harness serf · model opus-4.7 · cwd ~/serf       FORK DRAFT ┄────────
```

### 7.3 · Textarea

- 1–6 rows auto-grow.
- `>` prefix in `Accent`.
- Cursor `█` in `Accent`.
- Body in `Text`.
- Placeholder in `TextDim` only when empty.

### 7.4 · Kbd hint footer

Below the textarea. Mode-dependent set rendered via `KbdHint`:

| Mode           | Hints                                                                          |
|----------------|--------------------------------------------------------------------------------|
| compose        | `enter send · shift+enter newline · ⌘P palette · esc browse · /help`           |
| queue          | `enter queue · ctrl+s steer · esc browse · ⌘P palette · ⌘O dashboard`          |
| fork draft     | `enter fork · esc cancel · ⌘O dashboard`                                       |
| scroll-browse  | `↑↓ select · enter expand · f fork · c copy · esc compose · ⌘O dashboard`      |
| tool-focus     | `tab next · enter toggle · esc back · ⌘O dashboard`                            |

### 7.5 · Statusbar

New persistent line below the kbd footer:

```
● connected · hub.test · openai 12 · ctx 14k/200k · $0.04           serf-tui 0.1.0
```

- `●` health dot in `StateIdle` (connected) or `StateAwaiting` (disconnected).
- Hub address in `TextGhost`.
- Provider + inflight LLM request count in `TextDim`.
- Context window usage `ctx 14k/200k` — color thresholds:
  - `≥ compactThreshold` (currently `0.90` from `agent/context_manager.go`): `StateAwaiting`. This is the existing "compact soon" trigger; we use the same threshold to avoid a second source of truth.
  - `0.75 ≤ usage < 0.90`: `StateWarning`. New soft-warning band, purely visual.
  - `< 0.75`: `TextDim`.
- Cost-so-far in `TextDim` (only when cost tracking is on).
- TUI version right-aligned in `TextGhost`.

The existing `statusbar.go` already renders context usage as `N to compact` text. The new design preserves that exact text but adds the color thresholds on top of it. The phrase `N to compact` stays as the value content; only the foreground color changes per band.

Collapse-on-narrow priority: drop cost first, then version, then provider+queue, then hub address. Always retain `● connected` / `● disconnected`.

## 8 · Overlays

### 8.1 · Unified primitive

```go
type OverlayOpts struct {
    Title  string
    Width  int                  // explicit or auto
    Body   string               // pre-rendered
    Footer string               // pre-rendered kbd hints
    Accent lipgloss.Color       // border + title color; default = activeTheme.Accent
}
```

Render shape:
```
╭─ Select model ──────────────────────────────────────────────╮
│                                                             │
│  [body]                                                     │
│                                                             │
│  [footer]                                                   │
╰─────────────────────────────────────────────────────────────╯
```

- Title baked into the top border.
- Body in `Text` with `BgRaised` background where supported.
- Footer (kbd hints) below the body, gap of one blank line.
- Border in `Accent` (or per-overlay color override).

### 8.2 · Per-overlay treatment

| Overlay                 | Notes                                                                        |
|-------------------------|------------------------------------------------------------------------------|
| `model_picker`          | Adopts primitive. Add harness filter row `harness all · serf · codex` (←/→). Abbreviate model names via shared helper. |
| `theme_picker`          | Adopts primitive. Add right-side preview pane showing a 2-line sample of state colors. |
| `command_palette`       | Adopts primitive. Items as `>` cursor + slash-command syntax (`/spawn`, `/search`, `/theme`). |
| `credentials_panel`     | Adopts primitive. Body = 3-col table provider · status · actions; status uses `StatusBadge`. |
| `launch_settings_panel` | Adopts primitive. Internal left-rail tab list (Serf / Codex / Per-project). |
| `launch_overrides_modal`| Adopts primitive. Same field-list voice as launch_settings_panel.            |
| `text_input_modal`      | Adopts primitive. Smaller width (60 cols). Single input + confirm/cancel.    |
| `followupModal`         | NOT a direct adoption. Already wraps `textInputModal` internally (`hub_model.go:130`); it inherits the new `Overlay` look automatically via that composition. No code change in `followupModal` itself unless we need to override the title/footer for confirmation prompts. |

`notice_panel` is non-modal (renders inline above conversation) and **does not participate in the focus-trap system** (§8.3). Notices coexist with normal session input: the user can press `⌘P` to open the palette, type into the composer, etc., while a notice is visible. To dismiss a notice, the user presses `n` in compose mode (existing binding) or it auto-dismisses on next state update.

Notices adopt the workshop-log diagnostic voice instead of the overlay frame:
```
▍ ● spawn failed: model provider not reported by harness
  source serf · cause selected provider openai not in discovery
  next  refresh spawn options or choose a reported harness model
```
Wide left-bar in `StateAwaiting`, three-line indented body with key/value pairs.

`details_drawer` (wide-mode dashboard sidecar) is also non-modal; adopts section labels + status badge + ghost chrome internally.

### 8.3 · Focus traps

All overlays use focus traps. While any overlay is open, all input is consumed by the topmost overlay first. Two hard-coded escape hatches:

- `esc` — closes the topmost overlay. Always. Overlays cannot suppress.
- `⌘O` / `ctrl+o` — hard escape to dashboard. Closes all open overlays + current session.

Everything else (chord keys `⌘P`, `⌘N`, `⌘M`, `/`) is overlay-scoped. If the overlay handler doesn't recognize the key, it's rejected — no passthrough to the hub model.

**Stacking**: overlays can stack (max 2 in practice). `esc` pops the topmost only, returning focus to the parent.

**Implementation**: `topmostOverlay(m hubModel) overlayController` helper. `hubModel.Update()` dispatches to it first. Existing per-overlay Update functions remain; only the routing changes.

### 8.4 · Animation

Overlays appear and disappear instantly. No slide-in or fade — terminal animation is unreliable across emulators and stability matters more than transitions.

## 9 · Testing

Reuse the existing golden-sample corpus (`tui_samples.go`, ~26 renders). Extend:

- **Theme coverage**: every golden render runs against `dark` + `light`. Adds `Theme string` field to `tuiSampleRender`. Doubles the corpus to ~52.
- **Width coverage**: dashboard, session header, composer chip strip, overlays — each at widths 60, 100, 140.
- **State coverage for tool renderers**: each renderer gets unit tests for `pending`, `done`, `error`, plus wide-output body variant.
- **Token-isolation tests**: walk every theme, assert no token is empty + no value collides (`Bg != Text` etc.).
- **Focus-trap tests** (see below for the measurable assertion shape).

**Per-theme test isolation.** The active theme lives in a package-level global (`activeTheme`). Tests that render under a specific theme must:

```go
func runWithTheme(t *testing.T, name string, body func()) {
    t.Helper()
    prev := currentThemeName()
    setTheme(name)
    t.Cleanup(func() { setTheme(prev) })
    body()
}
```

Goldens loop over `[]string{"dark", "light"}` and call `runWithTheme` so each rendered `View` reflects the named theme. Tests are not `t.Parallel()` while the theme global is in play — a comment in the helper makes this explicit.

**Focus-trap test shape.** The trivial `m == m` check is insufficient. Each focus-trap test:

1. Constructs a `hubModel` with the target overlay open.
2. Captures a snapshot of the mutable session state that the trapped key WOULD normally mutate (e.g. for `⌘P`: `m.commandPalette == nil`; for `/`: `m.dashboardFilter.Value()`; for `↑`/`↓`: `m.selected`).
3. Sends the key via `m.Update(tea.KeyMsg{...})`.
4. Asserts the snapshot is unchanged (`m.commandPalette == nil` still, `m.dashboardFilter.Value()` still empty, `m.selected` unchanged).
5. Asserts the overlay's own state IS allowed to change (e.g. if `↑` is the overlay's own selection key, the overlay's internal index moves — that's expected; only the underlying model is what's frozen).

The trap-test helper makes this a one-liner per (overlay, key) pair:

```go
assertTrapped(t, modelWithOverlay, tea.KeyMsg{Type: tea.KeyCtrlP}, "m.commandPalette stays nil")
```

Golden file format: `tuiSampleRender{Name, Theme, Width, View, Contains}`. `View` is raw ANSI-bearing output so theme changes show as visible diffs.

## 10 · Migration strategy

| Wave | Scope                                                                     | Files                                                                                | User-visible |
|------|---------------------------------------------------------------------------|--------------------------------------------------------------------------------------|--------------|
| 1    | Theme registry + token tier (`TextGhost`, tints). Existing styles rewired. `setTheme` invalidates markdown renderer. Tint tokens precomputed. ~50 callsite migration. | `tokens.go` (new), `styles.go`, `message.go` (glamour cache invalidation hook)        | No           |
| 1.5  | Golden corpus extended with `Theme string` field; `runWithTheme` test helper added; existing renders rerun under dark+light to establish baseline.                  | `tui_samples.go`, `tui_samples_test.go`                                              | No (test-only)|
| 2    | Primitives.                                                               | `primitives.go` (new)                                                                | No (unused)  |
| 3    | Dashboard rewrite using primitives.                                       | `hub_model.go` (dashboard funcs), goldens                                            | Yes          |
| 4    | Session header rewrite + meta strip + statusbar (existing `statusbar.go` is rewritten in place, not added). | `hub_model.go` (session funcs), `statusbar.go` (modify), goldens                              | Yes          |
| 5    | Tool renderer registry replaces switch; existing 14+ tools migrated.      | `tool_renderers.go` (new), `tool_summary.go` (thin), `message.go`                    | Yes          |
| 6    | Per-tool body renderers (diff coloring, file preview, task list, subagent).| `tool_renderers.go`                                                                 | Yes          |
| 7    | Composer chip strip + mode chip.                                          | `composer_panel.go`, `hub_model.go`                                                  | Yes          |
| 8    | Overlay primitive adoption — one overlay per commit.                      | `model_picker.go`, `theme_picker.go`, `command_palette.go`, `credentials_panel.go`, `launch_settings_panel.go`, `launch_overrides_modal.go`, `text_input_modal.go` | Yes (per overlay) |
| 9    | Focus-trap discipline. `topmostOverlay` helper + Update reroute.          | `hub_model.go`                                                                       | Behavior change |
| 10   | MCP fallback + unknown-tool fallback renderers.                           | `tool_renderers.go`                                                                  | Yes          |

Each wave: builds + tests green, golden snapshots updated in the same commit, no half-states.

## 11 · Compatibility & open questions

**Terminal compatibility**: targeting truecolor + ANSI 256. lipgloss auto-degrades. Theme detection via existing `termenv.HasDarkBackground`.

**No protocol changes**: RPC, transcript schema, hub binary protocol all untouched.

**Out of scope (file follow-ups as observed)**:
- Search across past sessions.
- Multi-pane workspace.
- Mouse support.
- Plugin-installed renderers (MCP fallback path until/unless a renderer-plugin API is designed).

## 12 · Done means

- All 10 migration waves shipped on `tui-ux-pass` and merged to `main`.
- Golden corpus covers dark + light + 3 widths for every surface (≥120 golden renders total).
- Every state has a visible color; no surface communicates state solely through text.
- Tool-call rows scan: verb anchored left, duration right-floated, dot-leader fades.
- Overlays trap focus; `esc` and `⌘O` are the only escapes.
- Theme registry holds at least `dark` + `light`; adding a third is a struct literal.
- User can run `serf-tui` in light mode and read every surface without squinting.
