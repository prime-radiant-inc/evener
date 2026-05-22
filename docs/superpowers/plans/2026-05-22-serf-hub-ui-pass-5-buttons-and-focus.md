# Serf Hub UI Pass 5 — Buttons, Status Badges, Focus Rings Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Collapse the 8+ legacy button variant classes to a canonical `.btn` base + 6 modifiers (`-primary`, `-secondary`, `-ghost`, `-danger`, `-icon`, `-chip`), replace the rgba-tinted `.status-pill` with a typographic `.status-badge`, add universal `:focus-visible` rings, drive status-dot pulse via `[data-pulse]`, and migrate sub-tab state from ad-hoc `.active` classes to `[data-active]`.

**Architecture:** Add new CSS rules alongside the legacy ones so the templates can be migrated one surface at a time. Each new-variant task verifies parity on a live element by co-applying old + new classes. Once every template that uses a legacy class is migrated, delete the legacy CSS in a single sweep. JS that today sets `.active` on toggles is changed to set `data-active`. JS that renders the workspace `data-state` on status-dots is extended to also set `data-pulse` for `{active, awaiting, errored}`.

**Tech Stack:** Plain CSS (custom properties + attribute selectors), Go html/template, vanilla JS DOM API. No new dependencies.

**Pre-conditions (must hold before this plan starts):**
- Pass 4 in the migration order (sidebar restructure) has shipped — `.sb-row` exists in `sidebar.html` and `style.css`.
- Foundation tokens from earlier passes are defined in `:root`: `--bg`, `--bg-raised`, `--surface-secondary`, `--text`, `--text-muted`, `--text-dim`, `--rule`, `--accent`, `--state-awaiting`, `--state-processing`, `--state-warning`, `--state-idle`, `--state-ended`, `--state-subagent`, `--space-2`, `--space-3`, `--space-4`, `--space-7`, `--font-sans`, `--font-mono`, `--text-2xs`, `--text-xs`, `--text-sm`, `--text-base`, `--radius-md`, `--radius-pill`, `--motion-fast`, `--tap-min`.
- The `--pulse-cycle` motion token is defined (it's added by the motion pass).

If any of those tokens are not present when this plan starts, stop and confirm with the user before fabricating fallbacks.

---

## Reference — Design Language §3.1 Button Variants

All variants inherit:

```css
.btn {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-2) var(--space-4);
  min-height: var(--tap-min);
  font: inherit;
  font-family: var(--font-sans);
  font-size: var(--text-sm);
  font-weight: 500;
  border-radius: var(--radius-md);
  border: 1px solid transparent;
  cursor: pointer;
  transition: background var(--motion-fast), color var(--motion-fast), border-color var(--motion-fast);
}
.btn[disabled] { opacity: 0.45; cursor: not-allowed; }
```

| Variant | Background | Color | Border | Hover | Use |
| --- | --- | --- | --- | --- | --- |
| `.btn-primary` | `--accent` | `--btn-primary-text` | `--accent` | `filter: brightness(1.08)` | Send · spawn · fork confirm · save |
| `.btn-secondary` | `--bg-raised` | `--text` | `--rule` | bg → `--surface-secondary` | Common controls |
| `.btn-ghost` | transparent | `--text-muted` | transparent | bg → `--bg-raised`; color → `--text` | Header actions · cancel · ⋯ |
| `.btn-danger` | `rgba(awaiting, 0.05)` (`color-mix`) | `--state-awaiting` | `color-mix(awaiting, 40%)` | bg + border brighten | Stop · shutdown · destructive |
| `.btn-icon` | transparent | `--text-muted` | transparent | bg → `--bg-raised`; border → `--rule` | Single-glyph buttons |
| `.btn-chip` | `--bg-raised` | `--text` | `--rule` | border → `--accent` | Spawn picker triggers (pill-shaped) |

`.btn-primary` resolves its text color via the surface-specific token:

```css
:root,
[data-theme="dark"]  { --btn-primary-text: var(--bg);    /* near-black on light blue accent */ }
[data-theme="light"] { --btn-primary-text: #fafafa;      /* cream on dark blue accent */ }
```

### Legacy class → new variant mapping

| Today | New |
| --- | --- |
| `.input-btn` | `.btn .btn-secondary` |
| `.input-btn-primary` | `.btn .btn-primary` |
| `.input-btn-stop` | `.btn .btn-danger` |
| `.input-btn-ghost` | `.btn .btn-ghost` |
| `.header-action` | `.btn .btn-ghost` |
| `.header-action-danger` | `.btn .btn-danger` |
| `.spawn-btn` | `.btn .btn-primary` |
| `.spawn-attach-btn` | `.btn .btn-secondary` |
| `.fork-confirm` | `.btn .btn-primary` |
| `.fork-cancel` | `.btn .btn-ghost` |
| `.title-action` | `.btn .btn-icon` |
| `.panel-toggle` | `.btn .btn-ghost` + `[data-active]` |
| `.chip` (spawn) | `.btn .btn-chip` |

---

## File Structure

**Modified files:**

- `cmd/serf-hub/assets/style.css` — add `.btn`, `.btn-*` variants; add `--btn-primary-text` token; add `.status-badge` rules + state colors; add `[data-pulse]` keyframes; add universal `:focus-visible` rule + per-component overrides. Delete legacy `.input-btn*`, `.header-action*`, `.spawn-btn`, `.spawn-attach-btn`, `.fork-confirm`, `.fork-cancel`, `.title-action`, `.panel-toggle`, `.chip`, `.status-pill*` blocks after migrations.
- `cmd/serf-hub/templates/partials/workspace.html` — composer buttons + header actions + meta line status badge.
- `cmd/serf-hub/templates/partials/spawn.html` — spawn chips + attach + spawn submit.
- `cmd/serf-hub/templates/partials/sidebar.html` — `.project-new-btn`, `.project-gear-btn`, project chevron.
- `cmd/serf-hub/templates/partials/credentials.html` — Set/OAuth/Clear action buttons, editor Save/Cancel/Finish.
- `cmd/serf-hub/templates/partials/settings/providers.html` — `.status-pill` → `.status-badge`.
- `cmd/serf-hub/templates/partials/settings/project.html` — Save button.
- `cmd/serf-hub/templates/partials/settings/launch-serf.html` — Save button.
- `cmd/serf-hub/templates/partials/settings/inrepo.html` — Trust button.
- `cmd/serf-hub/templates/partials/settings/plugins.html` — Add/Remove (JS-rendered).
- `cmd/serf-hub/templates/partials/settings/skills.html` — Add/Remove (JS-rendered).
- `cmd/serf-hub/templates/partials/settings/mcp.html` — Add/Remove (JS-rendered).
- `cmd/serf-hub/assets/renderer.js` — fork dialog `.fork-cancel`/`.fork-confirm` → `.btn`; `setPanelToggleActive` switches from `classList.toggle("active", …)` to `toggleAttribute("data-active", …)`; new `applyStatusDotPulse` helper sets `data-pulse` for `active|awaiting|errored`.
- `cmd/serf-hub/assets/sidebar.js` — call `applyStatusDotPulse` after sidebar swap (mirroring the sb-row pass).
- `cmd/serf-hub/assets/search.js` — set `data-pulse` on dots emitted in search results.
- `cmd/serf-hub/web_test.go` — update three assertions referencing the legacy class strings.
- `cmd/serf-hub/jstest/*.js` — update jsdom HTML fixtures that hard-code legacy class names.

**No new files.**

---

## Task 1: Add `.btn` base class + `.btn-primary` variant

**Files:**
- Modify: `cmd/serf-hub/assets/style.css` (insert a new section after the legacy `.input-btn*` block ending around line 417)
- Modify: `cmd/serf-hub/templates/partials/workspace.html:67` (co-apply new classes to the existing send button for visual diff)

- [ ] **Step 1: Add `--btn-primary-text` token to `:root` and the explicit theme overrides**

Open `cmd/serf-hub/assets/style.css`. Inside the default `:root` block (the dark-default one starting at line 4), add the line below at the end of the block (right before the closing `}`):

```css
  --btn-primary-text: var(--bg);
```

Inside `:root[data-theme="dark"] { ... }`, add at the end:

```css
  --btn-primary-text: var(--bg);
```

Inside the `@media (prefers-color-scheme: light) { :root { ... } }`, add at the end of the inner `:root` block:

```css
    --btn-primary-text: #fafafa;
```

Inside `:root[data-theme="light"] { ... }`, add at the end:

```css
  --btn-primary-text: #fafafa;
```

- [ ] **Step 2: Add the `.btn` base + `.btn-primary` rules**

Find an unused horizontal section near the bottom of `style.css` (above the `Optimistic rendering` block around line 1004 is a good spot — group all six new variants together so they're one readable section). Insert this exact block:

```css
/* ── Button system (Pass 5) ───────────────────────────────────────────────
   Base class + six variants. Replaces .input-btn*, .header-action*, .spawn-btn,
   .spawn-attach-btn, .fork-confirm, .fork-cancel, .title-action, .panel-toggle,
   and the spawn .chip. See design language §3.1. */
.btn {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-2) var(--space-4);
  min-height: var(--tap-min);
  font: inherit;
  font-family: var(--font-sans);
  font-size: var(--text-sm);
  font-weight: 500;
  color: var(--text);
  background: transparent;
  border: 1px solid transparent;
  border-radius: var(--radius-md);
  cursor: pointer;
  transition: background var(--motion-fast),
              color var(--motion-fast),
              border-color var(--motion-fast),
              filter var(--motion-fast);
}
.btn[disabled],
.btn:disabled { opacity: 0.45; cursor: not-allowed; }

.btn-primary {
  background: var(--accent);
  color: var(--btn-primary-text);
  border-color: var(--accent);
}
.btn-primary:hover:not([disabled]):not(:disabled) { filter: brightness(1.08); }
.btn-primary:active:not([disabled]):not(:disabled) { filter: brightness(0.95); }
.btn-primary kbd {
  background: color-mix(in srgb, var(--btn-primary-text) 20%, transparent);
  border: 1px solid color-mix(in srgb, var(--btn-primary-text) 30%, transparent);
  color: inherit;
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  padding: 0 var(--space-2);
  border-radius: var(--radius-sm, 3px);
  opacity: 0.85;
  margin-left: var(--space-2);
}
```

- [ ] **Step 3: Co-apply the new classes to the workspace Send button to compare**

Open `cmd/serf-hub/templates/partials/workspace.html`. Find line 67:

```html
    <button type="submit" class="input-btn input-btn-primary send-btn" data-capability-send="{{.Capabilities.Send}}" data-capability-queue="{{.Capabilities.Queue}}"{{if and (not .Capabilities.Send) (not .Capabilities.Queue)}} disabled title="send unavailable"{{end}}>send <kbd>⌘↵</kbd></button>
```

Edit it to add `btn btn-primary` to the class list, keeping the old classes:

```html
    <button type="submit" class="input-btn input-btn-primary send-btn btn btn-primary" data-capability-send="{{.Capabilities.Send}}" data-capability-queue="{{.Capabilities.Queue}}"{{if and (not .Capabilities.Send) (not .Capabilities.Queue)}} disabled title="send unavailable"{{end}}>send <kbd>⌘↵</kbd></button>
```

- [ ] **Step 4: Build and visually verify**

Run:

```bash
cd /home/jesse/git/prime-radiant/serf && go build ./cmd/serf-hub && ./cmd/serf-hub/serf-hub &
```

Open a session in the web UI in both light and dark theme. Confirm the Send button:
- Reads at the same size as before (legacy + new co-applied → the cascade picks whichever paints last; new wins via specificity-equal source order).
- Has the accent background in both themes.
- The keyboard hint inside the button is legible.

Expected: no visible change vs the previous build (parity).

- [ ] **Step 5: Revert the co-applied class on Send (keep only the new classes)**

In `cmd/serf-hub/templates/partials/workspace.html:67`, replace `class="input-btn input-btn-primary send-btn btn btn-primary"` with the final form:

```html
    <button type="submit" class="btn btn-primary send-btn" data-capability-send="{{.Capabilities.Send}}" data-capability-queue="{{.Capabilities.Queue}}"{{if and (not .Capabilities.Send) (not .Capabilities.Queue)}} disabled title="send unavailable"{{end}}>send <kbd>⌘↵</kbd></button>
```

(Keep `send-btn` because `appwire.js`/`renderer.js` may target it via `.send-btn`.)

Re-build and re-verify the button still looks correct on its own classes.

- [ ] **Step 6: Update `web_test.go` send-button assertion**

In `cmd/serf-hub/web_test.go`, find line 139:

```go
	for _, supported := range []string{`class="input-btn input-btn-primary send-btn"`} {
```

Change to:

```go
	for _, supported := range []string{`class="btn btn-primary send-btn"`} {
```

Run the test:

```bash
cd /home/jesse/git/prime-radiant/serf && go test ./cmd/serf-hub -run TestWeb_WorkspaceRendersSendControl -count=1
```

Expected: PASS. If a test of that name doesn't exist, search for the test containing line 139 by `grep -n 'input-btn input-btn-primary' cmd/serf-hub/web_test.go` and run that test's name.

Also run the full hub tests:

```bash
go test ./cmd/serf-hub -count=1
```

Expected: PASS (one other web_test.go assertion about `input-btn-stop` may still pass since we haven't migrated that yet).

- [ ] **Step 7: Commit**

```bash
cd /home/jesse/git/prime-radiant/serf && git add cmd/serf-hub/assets/style.css cmd/serf-hub/templates/partials/workspace.html cmd/serf-hub/web_test.go
git commit -m "$(cat <<'EOF'
ui: add .btn + .btn-primary, migrate workspace Send button

First step of Pass 5 button consolidation. Adds the .btn base class,
the .btn-primary variant, and the --btn-primary-text token (resolves
to --bg in dark, #fafafa in light per design language §3.1). Migrates
the workspace Send button as the parity check.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Add `.btn-secondary` variant

**Files:**
- Modify: `cmd/serf-hub/assets/style.css` (append to the button-system section from Task 1)

- [ ] **Step 1: Append `.btn-secondary` rules**

Immediately after the `.btn-primary` block added in Task 1, append:

```css
.btn-secondary {
  background: var(--bg-raised);
  color: var(--text);
  border-color: var(--rule);
}
.btn-secondary:hover:not([disabled]):not(:disabled) {
  background: var(--surface-secondary);
}
.btn-secondary:active:not([disabled]):not(:disabled) {
  background: var(--surface-secondary);
  filter: brightness(0.96);
}
```

- [ ] **Step 2: Build and confirm no parse errors**

```bash
cd /home/jesse/git/prime-radiant/serf && go build ./cmd/serf-hub
```

Expected: clean build. No template touches yet — visual smoke verification deferred to the migration tasks (Tasks 7–11).

- [ ] **Step 3: Commit**

```bash
git add cmd/serf-hub/assets/style.css
git commit -m "$(cat <<'EOF'
ui: add .btn-secondary variant

Neutral surface-on-bg-raised button used for common controls
(attach, secondary actions). Pass 5 of the button consolidation.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Add `.btn-ghost` variant

**Files:**
- Modify: `cmd/serf-hub/assets/style.css` (append)

- [ ] **Step 1: Append `.btn-ghost` rules**

After the `.btn-secondary` block, append:

```css
.btn-ghost {
  background: transparent;
  color: var(--text-muted);
  border-color: transparent;
}
.btn-ghost:hover:not([disabled]):not(:disabled) {
  background: var(--bg-raised);
  color: var(--text);
}
.btn-ghost:active:not([disabled]):not(:disabled) {
  background: var(--surface-secondary);
}
.btn-ghost[data-active] {
  background: var(--bg-raised);
  color: var(--text);
  border-color: var(--rule);
}
```

- [ ] **Step 2: Build**

```bash
cd /home/jesse/git/prime-radiant/serf && go build ./cmd/serf-hub
```

Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add cmd/serf-hub/assets/style.css
git commit -m "$(cat <<'EOF'
ui: add .btn-ghost variant with [data-active] state

Transparent-by-default button used for header actions, cancel
controls, and the workspace tasks/details toggles. [data-active]
replaces ad-hoc .active classes for sub-tab pressed state.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Add `.btn-danger` variant

**Files:**
- Modify: `cmd/serf-hub/assets/style.css` (append)

- [ ] **Step 1: Append `.btn-danger` rules**

After the `.btn-ghost` block, append:

```css
.btn-danger {
  background: color-mix(in srgb, var(--state-awaiting) 5%, transparent);
  color: var(--state-awaiting);
  border-color: color-mix(in srgb, var(--state-awaiting) 40%, transparent);
}
.btn-danger:hover:not([disabled]):not(:disabled) {
  background: color-mix(in srgb, var(--state-awaiting) 14%, transparent);
  border-color: color-mix(in srgb, var(--state-awaiting) 65%, transparent);
}
.btn-danger:active:not([disabled]):not(:disabled) {
  background: color-mix(in srgb, var(--state-awaiting) 20%, transparent);
}
/* Disabled danger should not look "active red" — neutral muted instead. */
.btn-danger[disabled],
.btn-danger:disabled {
  color: var(--text-muted);
  background: transparent;
  border-color: var(--rule);
}
```

- [ ] **Step 2: Build**

```bash
cd /home/jesse/git/prime-radiant/serf && go build ./cmd/serf-hub
```

Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add cmd/serf-hub/assets/style.css
git commit -m "$(cat <<'EOF'
ui: add .btn-danger variant

Tinted danger button using color-mix on --state-awaiting. Disabled
state drops the red tint to neutral so an ended session's Stop
button doesn't read as inviting. Pass 5.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Add `.btn-icon` variant

**Files:**
- Modify: `cmd/serf-hub/assets/style.css` (append)

- [ ] **Step 1: Append `.btn-icon` rules**

After the `.btn-danger` block, append:

```css
.btn-icon {
  background: transparent;
  color: var(--text-muted);
  border-color: transparent;
  padding: var(--space-2);
  min-width: var(--tap-min);
  justify-content: center;
}
.btn-icon:hover:not([disabled]):not(:disabled) {
  background: var(--bg-raised);
  border-color: var(--rule);
  color: var(--text);
}
.btn-icon:active:not([disabled]):not(:disabled) {
  background: var(--surface-secondary);
}
```

- [ ] **Step 2: Build**

```bash
cd /home/jesse/git/prime-radiant/serf && go build ./cmd/serf-hub
```

Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add cmd/serf-hub/assets/style.css
git commit -m "$(cat <<'EOF'
ui: add .btn-icon variant

Square single-glyph button (≥tap-min square) used for title-bar
copy, sidebar project gear/new, and other one-character actions. Pass 5.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Add `.btn-chip` variant

**Files:**
- Modify: `cmd/serf-hub/assets/style.css` (append)

- [ ] **Step 1: Append `.btn-chip` rules**

After the `.btn-icon` block, append:

```css
.btn-chip {
  background: var(--bg-raised);
  color: var(--text);
  border-color: var(--rule);
  border-radius: var(--radius-pill);
  padding: var(--space-2) var(--space-4);
  align-items: baseline;
}
.btn-chip:hover:not([disabled]):not(:disabled) {
  border-color: var(--accent);
}
.btn-chip:active:not([disabled]):not(:disabled) {
  background: var(--surface-secondary);
}
.btn-chip .key {
  font-family: var(--font-mono);
  font-size: var(--text-2xs);
  color: var(--text-muted);
}
.btn-chip .val {
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  color: var(--text);
  max-width: 280px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.btn-chip .caret {
  color: var(--text-muted);
  margin-left: var(--space-2);
  font-size: 9px;
}
/* Mode-specific chip tint (e.g., access_mode chip on spawn). */
.btn-chip[data-mode] {
  background: color-mix(in srgb, var(--state-warning) 12%, var(--bg-raised));
  color: var(--state-warning);
}
.btn-chip[data-mode] .val { color: var(--state-warning); }
```

- [ ] **Step 2: Build**

```bash
cd /home/jesse/git/prime-radiant/serf && go build ./cmd/serf-hub
```

Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add cmd/serf-hub/assets/style.css
git commit -m "$(cat <<'EOF'
ui: add .btn-chip variant

Pill-shaped picker trigger with .key/.val/.caret children. Replaces
the spawn .chip class. [data-mode] reproduces the access-mode warning
tint. Pass 5.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Migrate `workspace.html` composer + header

**Files:**
- Modify: `cmd/serf-hub/templates/partials/workspace.html`
- Modify: `cmd/serf-hub/web_test.go` (Stop button assertions)
- Modify: `cmd/serf-hub/jstest/test-input-area.js`
- Modify: `cmd/serf-hub/jstest/test-queue-and-drain.js`

- [ ] **Step 1: Replace the title-action copy button**

In `cmd/serf-hub/templates/partials/workspace.html:9`, change:

```html
      <button class="title-action" type="button" data-copy-id="{{.ID}}" title="copy session ID">⧉</button>
```

to:

```html
      <button class="btn btn-icon title-action" type="button" data-copy-id="{{.ID}}" title="copy session ID">⧉</button>
```

(Keep `title-action` as a JS hook — `data-copy-id` is the actual selector but renaming would risk breaking unseen callers. The class is harmless if not styled.)

- [ ] **Step 2: Replace the two panel-toggle buttons (tasks + details)**

In `workspace.html`, find lines 12–17:

```html
      <button type="button" class="panel-toggle" data-tasks-trigger title="task list">
        <span class="panel-toggle-icon">☑</span><span class="panel-toggle-label">tasks</span>
      </button>
      <button type="button" class="panel-toggle" data-details-trigger title="session details">
        <span class="panel-toggle-icon">ⓘ</span><span class="panel-toggle-label">details</span>
      </button>
```

Change to:

```html
      <button type="button" class="btn btn-ghost panel-toggle" data-tasks-trigger title="task list">
        <span class="panel-toggle-icon">☑</span><span class="panel-toggle-label">tasks</span>
      </button>
      <button type="button" class="btn btn-ghost panel-toggle" data-details-trigger title="session details">
        <span class="panel-toggle-icon">ⓘ</span><span class="panel-toggle-label">details</span>
      </button>
```

(Keep `panel-toggle` class — JS targets it via `setPanelToggleActive` and tests query it. Task 15 changes the `.active` → `data-active` mechanism.)

- [ ] **Step 3: Replace the composer attach button**

In `workspace.html:51`, change:

```html
    <button type="button" class="input-btn" data-attach-trigger title="attach image">＋</button>
```

to:

```html
    <button type="button" class="btn btn-secondary" data-attach-trigger title="attach image">＋</button>
```

- [ ] **Step 4: Replace the model chip**

In `workspace.html` lines 52–56:

```html
    {{if .Capabilities.ChangeModel}}
    <button type="button" class="input-chip model-chip" data-model-trigger>{{if .Model}}{{.Model}}{{else}}—{{end}} <span class="chip-caret">▾</span></button>
    {{else}}
    <span class="input-chip model-chip" title="model changes unavailable">{{if .Model}}{{.Model}}{{else}}—{{end}}</span>
    {{end}}
```

Change to:

```html
    {{if .Capabilities.ChangeModel}}
    <button type="button" class="btn btn-chip model-chip" data-model-trigger>
      <span class="val">{{if .Model}}{{.Model}}{{else}}—{{end}}</span>
      <span class="caret">▾</span>
    </button>
    {{else}}
    <span class="btn btn-chip model-chip" title="model changes unavailable">
      <span class="val">{{if .Model}}{{.Model}}{{else}}—{{end}}</span>
    </span>
    {{end}}
```

(`.model-chip` is kept as a JS hook for any model-trigger callers — pure marker class, no CSS rule.)

- [ ] **Step 5: Replace the Stop button**

In `workspace.html:62`, change:

```html
    <button type="button" class="input-btn input-btn-stop stop-btn" data-action-trigger="interrupt" data-capability-interrupt="{{.Capabilities.Interrupt}}" title="stop the in-flight turn"{{if and (ne .State "awaiting") (ne .State "active")}} disabled{{end}}>Stop</button>
```

to:

```html
    <button type="button" class="btn btn-danger stop-btn" data-action-trigger="interrupt" data-capability-interrupt="{{.Capabilities.Interrupt}}" title="stop the in-flight turn"{{if and (ne .State "awaiting") (ne .State "active")}} disabled{{end}}>Stop</button>
```

- [ ] **Step 6: Replace the send-as-steer button**

In `workspace.html:65`, change:

```html
    <button type="button" class="input-btn input-btn-ghost" data-steer-trigger data-capability-steer="{{.Capabilities.Steer}}" title="drain the queue as a steering message — or steer with the textarea text when the queue is empty"{{if or (not .Capabilities.Steer) (eq .ActiveTurnID "") (and (ne .State "awaiting") (ne .State "active"))}} disabled{{end}}>send as steer <kbd>⇧↵</kbd></button>
```

to:

```html
    <button type="button" class="btn btn-ghost" data-steer-trigger data-capability-steer="{{.Capabilities.Steer}}" title="drain the queue as a steering message — or steer with the textarea text when the queue is empty"{{if or (not .Capabilities.Steer) (eq .ActiveTurnID "") (and (ne .State "awaiting") (ne .State "active"))}} disabled{{end}}>send as steer <kbd>⇧↵</kbd></button>
```

- [ ] **Step 7: Update the web_test.go Stop button assertions**

In `cmd/serf-hub/web_test.go` lines 209–219, change every occurrence of `class="input-btn input-btn-stop stop-btn"` to `class="btn btn-danger stop-btn"`. Specifically:

Line 214 — change:

```go
		if !strings.Contains(body, `class="input-btn input-btn-stop stop-btn" data-action-trigger="interrupt"`) ||
```

to:

```go
		if !strings.Contains(body, `class="btn btn-danger stop-btn" data-action-trigger="interrupt"`) ||
```

Line 218 — change:

```go
		if strings.Contains(body, `class="input-btn input-btn-stop stop-btn" data-action-trigger="interrupt" title="stop the in-flight turn" disabled`) {
```

to:

```go
		if strings.Contains(body, `class="btn btn-danger stop-btn" data-action-trigger="interrupt" title="stop the in-flight turn" disabled`) {
```

Line 209 (the negative assertion checking that the old `header-action` class is no longer present) — this is still correct as-is; leave it alone. It asserts the legacy header-action class doesn't appear, which becomes vacuously true after the migration.

- [ ] **Step 8: Update jsdom HTML fixtures**

In `cmd/serf-hub/jstest/test-input-area.js` lines 36–38, change:

```html
      <button type="button" class="input-btn" data-attach-trigger>＋</button>
      <button type="button" class="input-btn input-btn-ghost" data-steer-trigger>steer</button>
      <button type="submit" class="send-btn input-btn input-btn-primary">send</button>
```

to:

```html
      <button type="button" class="btn btn-secondary" data-attach-trigger>＋</button>
      <button type="button" class="btn btn-ghost" data-steer-trigger>steer</button>
      <button type="submit" class="send-btn btn btn-primary">send</button>
```

In `cmd/serf-hub/jstest/test-queue-and-drain.js` lines 36–38, apply the same replacement.

- [ ] **Step 9: Build and test**

```bash
cd /home/jesse/git/prime-radiant/serf && go build ./cmd/serf-hub && go test ./cmd/serf-hub -count=1
```

Expected: PASS.

```bash
cd /home/jesse/git/prime-radiant/serf/cmd/serf-hub/jstest && node test-input-area.js && node test-queue-and-drain.js
```

Expected: both exit 0.

- [ ] **Step 10: Visually verify the composer**

Launch the hub and open an active session. Confirm:
- Send button accent in both themes.
- Stop button red-tinted while a turn is active, neutral when disabled.
- Send-as-steer ghost-style.
- Attach `＋` looks like a secondary button.
- Model chip pill shape with caret.
- Tasks/details ghost buttons in header.
- Title-bar copy `⧉` is icon-square.

- [ ] **Step 11: Commit**

```bash
git add cmd/serf-hub/templates/partials/workspace.html cmd/serf-hub/web_test.go cmd/serf-hub/jstest/test-input-area.js cmd/serf-hub/jstest/test-queue-and-drain.js
git commit -m "$(cat <<'EOF'
ui: migrate workspace composer + header to .btn variants

workspace.html now uses .btn .btn-primary/secondary/ghost/danger/chip/icon
in place of .input-btn*, .title-action, .panel-toggle, .input-chip. Stop
button test assertions in web_test.go and jsdom fixtures updated to match.
Pass 5.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Migrate `spawn.html`

**Files:**
- Modify: `cmd/serf-hub/templates/partials/spawn.html`
- Modify: `cmd/serf-hub/jstest/test-spawn.js`

- [ ] **Step 1: Migrate the five spawn chips**

In `cmd/serf-hub/templates/partials/spawn.html` lines 12–37, the existing block is:

```html
    <div class="spawn-chips" id="spawn-chips">
      <button class="chip" type="button" data-chip="harness">
        <span class="chip-label">harness</span>
        <span class="chip-value" data-chip-value-harness>{{.DefaultHarness}}</span>
        <span class="chip-caret">▾</span>
      </button>
      <button class="chip" type="button" data-chip="model">
        <span class="chip-label">model</span>
        <span class="chip-value" data-chip-value-model>{{.DefaultModel}}</span>
        <span class="chip-caret">▾</span>
      </button>
      <button class="chip" type="button" data-chip="working_dir">
        <span class="chip-label">📁</span>
        <span class="chip-value" data-chip-value-working_dir>{{.DefaultWorkingDir}}</span>
        <span class="chip-caret">▾</span>
      </button>
      <button class="chip" type="button" data-chip="branch" title="branch / worktree">
        <span class="chip-label">branch</span>
        <span class="chip-value" data-chip-value-branch>{{.DefaultBranch}}</span>
        <span class="chip-caret">▾</span>
      </button>
      <button class="chip chip-mode" type="button" data-chip="access_mode">
        <span class="chip-value" data-chip-value-access_mode>{{.DefaultAccessMode}}</span>
        <span class="chip-caret">▾</span>
      </button>
    </div>
```

Replace with (note `.btn .btn-chip` on the buttons, `.key`/`.val`/`.caret` on the spans, and `data-mode` on the access-mode chip):

```html
    <div class="spawn-chips" id="spawn-chips">
      <button class="btn btn-chip" type="button" data-chip="harness">
        <span class="key chip-label">harness</span>
        <span class="val chip-value" data-chip-value-harness>{{.DefaultHarness}}</span>
        <span class="caret chip-caret">▾</span>
      </button>
      <button class="btn btn-chip" type="button" data-chip="model">
        <span class="key chip-label">model</span>
        <span class="val chip-value" data-chip-value-model>{{.DefaultModel}}</span>
        <span class="caret chip-caret">▾</span>
      </button>
      <button class="btn btn-chip" type="button" data-chip="working_dir">
        <span class="key chip-label">📁</span>
        <span class="val chip-value" data-chip-value-working_dir>{{.DefaultWorkingDir}}</span>
        <span class="caret chip-caret">▾</span>
      </button>
      <button class="btn btn-chip" type="button" data-chip="branch" title="branch / worktree">
        <span class="key chip-label">branch</span>
        <span class="val chip-value" data-chip-value-branch>{{.DefaultBranch}}</span>
        <span class="caret chip-caret">▾</span>
      </button>
      <button class="btn btn-chip" type="button" data-chip="access_mode" data-mode="access_mode">
        <span class="val chip-value" data-chip-value-access_mode>{{.DefaultAccessMode}}</span>
        <span class="caret chip-caret">▾</span>
      </button>
    </div>
```

(`.chip-label` / `.chip-value` / `.chip-caret` are kept as JS hooks. `spawn.js` line 1136/1149/1453 queries `.chip-value` and would break if we drop it. The double-class on each span is intentional.)

- [ ] **Step 2: Migrate the spawn attach button**

In `spawn.html:42`, change:

```html
      <button type="button" class="spawn-attach-btn" data-attach-trigger aria-label="attach image" title="attach image">📎</button>
```

to:

```html
      <button type="button" class="btn btn-secondary spawn-attach-btn" data-attach-trigger aria-label="attach image" title="attach image">📎</button>
```

(Keep `spawn-attach-btn` as a marker; it has no remaining CSS once Task 12 deletes the legacy block, but downstream code may select by it.)

- [ ] **Step 3: Migrate the spawn submit button**

In `spawn.html:76`, change:

```html
      <button class="spawn-btn" type="submit">spawn <kbd>⌘↵</kbd></button>
```

to:

```html
      <button class="btn btn-primary spawn-btn" type="submit">spawn <kbd>⌘↵</kbd></button>
```

(`spawn.js:1088` selects `.spawn-btn`; keep it as a hook.)

- [ ] **Step 4: Migrate the "show resolved config" button + the advanced add controls**

In `spawn.html:70`, change:

```html
          <button type="button" id="ovr-show-resolved">show resolved config</button>
```

to:

```html
          <button type="button" class="btn btn-secondary" id="ovr-show-resolved">show resolved config</button>
```

- [ ] **Step 5: Update jstest fixtures**

In `cmd/serf-hub/jstest/test-spawn.js` lines 39–46 and 163–164, replace `class="chip"` with `class="btn btn-chip"` and `class="spawn-btn"` with `class="btn btn-primary spawn-btn"` wherever they appear.

Concretely, lines 39, 42, 45 currently read:

```html
      <button class="chip" type="button" data-chip="harness">
```

Change to:

```html
      <button class="btn btn-chip" type="button" data-chip="harness">
```

Repeat for `data-chip="model"` (line 42) and `data-chip="branch"` (line 45).

Lines 163–164:

```html
    <button class="chip" type="button" data-chip="harness"><span class="chip-value" data-chip-value-harness>serf</span></button>
    <button class="chip" type="button" data-chip="model"><span class="chip-value" data-chip-value-model>(pick a model)</span></button>
```

Change to:

```html
    <button class="btn btn-chip" type="button" data-chip="harness"><span class="chip-value" data-chip-value-harness>serf</span></button>
    <button class="btn btn-chip" type="button" data-chip="model"><span class="chip-value" data-chip-value-model>(pick a model)</span></button>
```

Lines 59 and 174 contain `<button class="spawn-btn" type="submit">spawn</button>`. Change both to:

```html
    <button class="btn btn-primary spawn-btn" type="submit">spawn</button>
```

Line 373 — `formDom.window.document.querySelector(".spawn-btn")` — leave unchanged; the marker class is preserved.

- [ ] **Step 6: Build and test**

```bash
cd /home/jesse/git/prime-radiant/serf && go build ./cmd/serf-hub && go test ./cmd/serf-hub -count=1
cd cmd/serf-hub/jstest && node test-spawn.js
```

Expected: both PASS / exit 0.

- [ ] **Step 7: Visually verify spawn**

Open `/new`. Confirm:
- Chips pill-shaped, key+val styling, caret on right.
- Access-mode chip carries warning tint via `[data-mode]`.
- 📎 attach button reads as secondary.
- spawn submit accent.

- [ ] **Step 8: Commit**

```bash
git add cmd/serf-hub/templates/partials/spawn.html cmd/serf-hub/jstest/test-spawn.js
git commit -m "$(cat <<'EOF'
ui: migrate spawn.html to .btn variants

Spawn chips become .btn .btn-chip with [data-mode] for access-mode
tint. Attach + submit become .btn-secondary / .btn-primary. JS hooks
(.chip-value, .spawn-btn, .spawn-attach-btn) preserved as marker
classes. Pass 5.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Migrate `sidebar.html` — rail toggle, project new/gear

**Files:**
- Modify: `cmd/serf-hub/templates/partials/sidebar.html`
- Modify: `cmd/serf-hub/assets/style.css` (drop legacy `.project-new-btn` / `.project-gear-btn` block once template uses `.btn-icon`)

- [ ] **Step 1: Migrate the project gear button**

In `cmd/serf-hub/templates/partials/sidebar.html` lines 41–47:

```html
      <a class="project-gear-btn"
         title="project settings for {{.Name}}"
         href="/settings/project?cwd={{.WorkingDir | urlquery}}"
         hx-get="/_partials/settings/project?cwd={{.WorkingDir | urlquery}}"
         hx-target="#workspace"
         hx-swap="innerHTML"
         hx-push-url="/settings/project?cwd={{.WorkingDir | urlquery}}">⚙</a>
```

Change `class="project-gear-btn"` to `class="btn btn-icon project-gear-btn"`:

```html
      <a class="btn btn-icon project-gear-btn"
         title="project settings for {{.Name}}"
         href="/settings/project?cwd={{.WorkingDir | urlquery}}"
         hx-get="/_partials/settings/project?cwd={{.WorkingDir | urlquery}}"
         hx-target="#workspace"
         hx-swap="innerHTML"
         hx-push-url="/settings/project?cwd={{.WorkingDir | urlquery}}">⚙</a>
```

- [ ] **Step 2: Migrate the project new button**

In `sidebar.html` lines 48–54:

```html
      <a class="project-new-btn"
         title="new session in {{.Name}}"
         href="/new?dir={{.WorkingDir | urlquery}}"
         hx-get="/_partials/workspace/spawn?dir={{.WorkingDir | urlquery}}"
         hx-target="#workspace"
         hx-swap="innerHTML"
         hx-push-url="/new?dir={{.WorkingDir | urlquery}}">＋</a>
```

Change `class="project-new-btn"` to `class="btn btn-icon project-new-btn"`:

```html
      <a class="btn btn-icon project-new-btn"
         title="new session in {{.Name}}"
         href="/new?dir={{.WorkingDir | urlquery}}"
         hx-get="/_partials/workspace/spawn?dir={{.WorkingDir | urlquery}}"
         hx-target="#workspace"
         hx-swap="innerHTML"
         hx-push-url="/new?dir={{.WorkingDir | urlquery}}">＋</a>
```

- [ ] **Step 3: Drop the hover-only opacity on `.project-gear-btn`**

In `cmd/serf-hub/assets/style.css` line 323, the rule `.project-gear-btn { … opacity: 0; }` is what made the gear hide until hover. Design language §4.8 says these are persistent at `--text-dim`. Edit the rule to remove the `opacity: 0` and the `:hover { opacity: 1 }` overrides:

Find the block:

```css
.project-new-btn { color: var(--text-dim); text-decoration: none; padding: 4px 6px; font-size: 14px; line-height: 1; cursor: pointer; transition: color 0.1s, background 0.1s; border-radius: 4px; }
.project-new-btn:hover { color: var(--text); background: var(--bg-raised); }
.project-section:hover .project-new-btn { color: var(--text-muted); }
.project-gear-btn { color: var(--text-dim); text-decoration: none; padding: 4px 5px; font-size: 11px; line-height: 1; cursor: pointer; transition: color 0.1s, background 0.1s; border-radius: 4px; opacity: 0; }
.project-gear-btn:hover { color: var(--text); background: var(--bg-raised); opacity: 1; }
.project-section:hover .project-gear-btn { opacity: 1; color: var(--text-muted); }
```

Replace with:

```css
/* Project header icons (gear, new) inherit .btn-icon. Keep them dim by
   default and brighten on row hover; the .btn-icon hover state handles
   per-button hover. No hover-only-reveal (touch users see them too). */
.project-new-btn,
.project-gear-btn { color: var(--text-dim); text-decoration: none; }
.project-section:hover .project-new-btn,
.project-section:hover .project-gear-btn { color: var(--text-muted); }
```

(`.btn-icon` provides padding, radius, transitions, and per-button hover. Per-class rules now only carry the row-hover-brightens behavior.)

- [ ] **Step 4: Migrate the project chevron to a `<button>`**

Design language §6.3 (Keyboard navigation) calls out: "Project chevrons today are click-only (a11y gap). They become `<button>` elements with keyboard activation."

In `sidebar.html:35`:

```html
      <span class="project-chevron" role="button" aria-label="expand project">▸</span>
```

Change to:

```html
      <button type="button" class="btn btn-icon project-chevron" aria-label="expand project">▸</button>
```

If existing JS in `sidebar.js` selects `.project-chevron` and handles `click` events, it will keep working — buttons fire `click` too. Confirm by running the sidebar jstest after the build step.

- [ ] **Step 5: Build and test**

```bash
cd /home/jesse/git/prime-radiant/serf && go build ./cmd/serf-hub && go test ./cmd/serf-hub -count=1
cd cmd/serf-hub/jstest && node test-sidebar.js
```

Expected: PASS / exit 0.

- [ ] **Step 6: Visually verify sidebar**

Open the hub and confirm: gear and ＋ icons render at `--text-dim`, brighten on project-row hover, and brighten further on direct button hover. Chevron focusable via Tab.

- [ ] **Step 7: Commit**

```bash
git add cmd/serf-hub/templates/partials/sidebar.html cmd/serf-hub/assets/style.css
git commit -m "$(cat <<'EOF'
ui: migrate sidebar project + chevron buttons to .btn-icon

Project gear/new/chevron now use .btn .btn-icon. Drops hover-only
opacity:0 on gear (persistent at --text-dim per design language §4.8).
Chevron promoted from <span role=button> to <button> for keyboard
activation. Pass 5.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: Migrate `credentials.html` + the fork dialog

**Files:**
- Modify: `cmd/serf-hub/templates/partials/credentials.html`
- Modify: `cmd/serf-hub/assets/renderer.js`

- [ ] **Step 1: Migrate the credentials action buttons**

In `cmd/serf-hub/templates/partials/credentials.html` lines 62–64:

```html
              ${supportsApiKey ? `<button type="button" data-action="set">${p.activeSource === "file" ? "Replace key" : "Set API key"}</button>` : ""}
              ${supportsOAuth ? `<button type="button" data-action="oauth">${p.activeSource === "oauth" ? "Refresh OAuth" : "Sign in…"}</button>` : ""}
              ${showClear ? `<button type="button" data-action="clear" class="credentials-clear-btn">Clear</button>` : ""}
```

Change to:

```html
              ${supportsApiKey ? `<button type="button" class="btn btn-secondary" data-action="set">${p.activeSource === "file" ? "Replace key" : "Set API key"}</button>` : ""}
              ${supportsOAuth ? `<button type="button" class="btn btn-secondary" data-action="oauth">${p.activeSource === "oauth" ? "Refresh OAuth" : "Sign in…"}</button>` : ""}
              ${showClear ? `<button type="button" class="btn btn-danger credentials-clear-btn" data-action="clear">Clear</button>` : ""}
```

- [ ] **Step 2: Migrate the credentials-editor Save/Cancel/Finish buttons**

In `credentials.html` lines 78–80 and 92–94, change:

```html
            <div class="credentials-editor-actions">
              <button type="submit">Save</button>
              <button type="button" data-action="cancel-edit">Cancel</button>
            </div>
```

to:

```html
            <div class="credentials-editor-actions">
              <button type="submit" class="btn btn-primary">Save</button>
              <button type="button" class="btn btn-ghost" data-action="cancel-edit">Cancel</button>
            </div>
```

And for the OAuth redirect editor lines 92–94, change:

```html
            <div class="credentials-editor-actions">
              <button type="submit">Finish</button>
              <button type="button" data-action="cancel-edit">Cancel</button>
            </div>
```

to:

```html
            <div class="credentials-editor-actions">
              <button type="submit" class="btn btn-primary">Finish</button>
              <button type="button" class="btn btn-ghost" data-action="cancel-edit">Cancel</button>
            </div>
```

- [ ] **Step 3: Migrate the fork dialog buttons in `renderer.js`**

In `cmd/serf-hub/assets/renderer.js` lines 988–991:

```javascript
      const cancel = document.createElement("button");
      cancel.className = "fork-cancel"; cancel.textContent = "cancel"; cancel.type = "button";
      const confirm = document.createElement("button");
      confirm.className = "fork-confirm"; confirm.type = "button";
      confirm.innerHTML = "fork <kbd>⌘↩</kbd>";
```

Change to:

```javascript
      const cancel = document.createElement("button");
      cancel.className = "btn btn-ghost fork-cancel"; cancel.textContent = "cancel"; cancel.type = "button";
      const confirm = document.createElement("button");
      confirm.className = "btn btn-primary fork-confirm"; confirm.type = "button";
      confirm.innerHTML = "fork <kbd>⌘↩</kbd>";
```

- [ ] **Step 4: Build and verify**

```bash
cd /home/jesse/git/prime-radiant/serf && go build ./cmd/serf-hub && go test ./cmd/serf-hub -count=1
```

Expected: PASS.

Manually: open `/credentials`. Set/Replace, Sign in, Clear, and the Save/Cancel pair when editing a key. Then edit a user message in a session and check the fork-confirm modal — confirm/cancel buttons should match the design language.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/templates/partials/credentials.html cmd/serf-hub/assets/renderer.js
git commit -m "$(cat <<'EOF'
ui: migrate credentials + fork dialog to .btn variants

Credentials action buttons: Set/OAuth → .btn-secondary, Clear →
.btn-danger, Save/Finish → .btn-primary, Cancel → .btn-ghost.
Fork dialog cancel/confirm in renderer.js follow the same mapping.
Pass 5.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 11: Migrate settings sub-pages with buttons

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings/project.html`
- Modify: `cmd/serf-hub/templates/partials/settings/launch-serf.html`
- Modify: `cmd/serf-hub/templates/partials/settings/inrepo.html`
- Modify: `cmd/serf-hub/templates/partials/settings/plugins.html`
- Modify: `cmd/serf-hub/templates/partials/settings/skills.html`
- Modify: `cmd/serf-hub/templates/partials/settings/mcp.html`

- [ ] **Step 1: project.html — Save button**

In `cmd/serf-hub/templates/partials/settings/project.html:11`, change:

```html
      <button type="submit" id="proj-launch-save">Save launch defaults</button>
```

to:

```html
      <button type="submit" class="btn btn-primary" id="proj-launch-save">Save launch defaults</button>
```

- [ ] **Step 2: launch-serf.html — Save button**

In `cmd/serf-hub/templates/partials/settings/launch-serf.html:11`, change:

```html
    <button type="submit">Save launch defaults</button>
```

to:

```html
    <button type="submit" class="btn btn-primary">Save launch defaults</button>
```

- [ ] **Step 3: inrepo.html — Trust button**

In `cmd/serf-hub/templates/partials/settings/inrepo.html:44`, change:

```html
        ${showApprove ? `<button type="button" id="approve">Trust this file</button>` : ""}
```

to:

```html
        ${showApprove ? `<button type="button" class="btn btn-primary" id="approve">Trust this file</button>` : ""}
```

- [ ] **Step 4: plugins.html — JS-rendered Add + Pick + Remove**

In `cmd/serf-hub/templates/partials/settings/plugins.html`, the script creates buttons via `document.createElement("button")` and sets `textContent`. Find lines 23–28:

```javascript
          const button = document.createElement("button");
          button.type = "button";
          button.dataset.i = String(i);
          button.dataset.action = "rm";
          button.textContent = "remove";
          item.append(code, " ", button);
```

Add a `className` assignment right after `button.type = "button";`:

```javascript
          const button = document.createElement("button");
          button.type = "button";
          button.className = "btn btn-ghost";
          button.dataset.i = String(i);
          button.dataset.action = "rm";
          button.textContent = "remove";
          item.append(code, " ", button);
```

Find lines 40–44 (the "pick" + "add" buttons):

```javascript
        const pick = document.createElement("button");
        pick.type = "button";
        pick.textContent = "Pick directory…";
        pick.dataset.action = "pick";
        const add = document.createElement("button");
```

Insert `className` lines:

```javascript
        const pick = document.createElement("button");
        pick.type = "button";
        pick.className = "btn btn-secondary";
        pick.textContent = "Pick directory…";
        pick.dataset.action = "pick";
        const add = document.createElement("button");
```

For the `add` button (continue reading the file at line 44+ and inspect context — the `add` button is the green-action one; mark as primary). Edit the line creating it:

```javascript
        const add = document.createElement("button");
        add.type = "button";
        add.className = "btn btn-primary";
        add.textContent = "Add";
```

(If the original textContent or other attributes differ, preserve them — only add the className. Read the file first if uncertain.)

- [ ] **Step 5: skills.html — JS-rendered Add + Pick + Remove**

In `cmd/serf-hub/templates/partials/settings/skills.html`, apply the same edits as plugins.html (the file mirrors plugins.html structure):
- Line ~24 onwards: add `button.className = "btn btn-ghost";` to the `rm` remove button.
- Line ~40 onwards: add `pick.className = "btn btn-secondary";` to the pick button.
- Line ~44 onwards: add `add.className = "btn btn-primary";` to the add button.

- [ ] **Step 6: mcp.html — JS-rendered Add + Remove (two sections: config files + inline servers)**

In `cmd/serf-hub/templates/partials/settings/mcp.html`:
- Line ~26 (first `rm-config` remove button): add `button.className = "btn btn-ghost";`.
- Line ~41 (configAdd button): add `configAdd.className = "btn btn-primary";`.
- Line ~62 (second `rm` remove button in the inline-servers section): add `button.className = "btn btn-ghost";`.
- Line ~85 (final add button in inline servers): add `add.className = "btn btn-primary";`.

- [ ] **Step 7: Build and smoke-test**

```bash
cd /home/jesse/git/prime-radiant/serf && go build ./cmd/serf-hub && go test ./cmd/serf-hub -count=1
```

Expected: PASS.

Visually: navigate to /settings/project, /settings/launch, /settings/inrepo, /settings/plugins, /settings/skills, /settings/mcp. Confirm each surface's Save / Add / Remove buttons render as primary or ghost as appropriate.

- [ ] **Step 8: Commit**

```bash
git add cmd/serf-hub/templates/partials/settings/project.html cmd/serf-hub/templates/partials/settings/launch-serf.html cmd/serf-hub/templates/partials/settings/inrepo.html cmd/serf-hub/templates/partials/settings/plugins.html cmd/serf-hub/templates/partials/settings/skills.html cmd/serf-hub/templates/partials/settings/mcp.html
git commit -m "$(cat <<'EOF'
ui: migrate settings sub-pages to .btn variants

Save buttons (project, launch-serf, inrepo Trust) become .btn-primary.
JS-rendered Add buttons in plugins/skills/mcp become .btn-primary;
their Remove and Pick buttons become .btn-ghost / .btn-secondary.
Pass 5.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 12: Delete legacy button CSS

**Files:**
- Modify: `cmd/serf-hub/assets/style.css` (delete lines for the legacy button classes)

By this point no template or JS string emits the legacy button classes for visible elements, so the rules can be removed. Marker classes that remain on elements (e.g., `send-btn`, `stop-btn`, `model-chip`, `panel-toggle`, `title-action`, `spawn-btn`, `spawn-attach-btn`, `fork-cancel`, `fork-confirm`, `project-new-btn`, `project-gear-btn`) have no CSS rules of their own once these blocks are deleted — they're pure JS hooks.

- [ ] **Step 1: Verify the legacy classes are truly unused in templates / JS**

Run:

```bash
cd /home/jesse/git/prime-radiant/serf && grep -rn 'class="[^"]*\(input-btn\|spawn-btn\|spawn-attach-btn\|fork-confirm\|fork-cancel\|header-action\|title-action\|panel-toggle\|chip-mode\|input-chip\)' cmd/serf-hub/templates/ cmd/serf-hub/assets/ --include='*.html' --include='*.js' | grep -v "\.btn"
```

Expected: only marker-class uses remain (the new `class="btn btn-primary spawn-btn"` form is excluded by the grep). If any element is still styled solely by a legacy class, fix it before continuing.

- [ ] **Step 2: Delete legacy button CSS rules**

In `cmd/serf-hub/assets/style.css`, delete these blocks entirely:

1. Lines ~339–352 (the `.panel-toggle*` + `.header-action*` rules). Specifically delete every line whose selector starts with `.panel-toggle`, `.header-action`, `.header-action-danger`.

2. Lines ~409–417 — the `.input-btn*` family:

```css
.input-btn { display: inline-flex; align-items: center; gap: 5px; padding: 4px 12px; background: var(--bg); border: 1px solid var(--rule); border-radius: 4px; color: var(--text); font: inherit; font-size: 11.5px; cursor: pointer; }
.input-btn:hover { background: var(--bg-raised); }
.input-btn-ghost { color: var(--text-muted); }
.input-btn-stop { color: var(--state-awaiting); border-color: rgba(247,118,142,0.45); background: rgba(247,118,142,0.08); margin-right: 10px; }
.input-btn-stop:hover:not([disabled]) { background: rgba(247,118,142,0.14); border-color: rgba(247,118,142,0.65); }
.input-btn-stop[disabled] { opacity: 0.45; cursor: not-allowed; }
.input-btn-primary { background: var(--state-processing); color: var(--bg); border-color: transparent; font-weight: 500; }
.input-btn-primary:hover { background: var(--state-processing); filter: brightness(1.1); }
.input-btn-primary kbd { background: rgba(0,0,0,0.2); border: 1px solid rgba(0,0,0,0.3); color: inherit; font-family: ui-monospace, "SFMono-Regular", monospace; padding: 0 4px; border-radius: 3px; }
```

3. Line 419 — `.input-chip { font-family: ... font-size: 11px; }` — delete.

4. Lines ~595–596 — `.fork-cancel` and `.fork-confirm` rules — delete.

5. Lines ~604–608 — the spawn-page `.chip` rules:

```css
.chip { display: inline-flex; align-items: baseline; gap: 8px; padding: 5px 11px; background: var(--bg-raised); border: 1px solid var(--rule); border-radius: 14px; color: var(--text); font-size: 12px; cursor: pointer; font: inherit; }
.chip-label { color: var(--text-muted); }
.chip-value { color: var(--text); font-family: ui-monospace, "SFMono-Regular", monospace; max-width: 280px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.chip-caret { color: var(--text-muted); font-size: 9px; }
.chip-mode { background: rgba(224, 175, 104, 0.12); color: var(--state-warning); }
```

6. Lines ~619–621 — `.spawn-attach-btn` rules — delete.

7. Lines ~657–658 — `.spawn-btn` rules — delete. `.fork-confirm kbd` (line 663) — delete too (the `.btn-primary kbd` rule in Task 1 covers it).

8. Lines ~813–814 — `.title-action` rules — delete.

9. In the `@media (max-width: 767px)` block around line 921, find the override:

```css
  .header-action,
  .panel-toggle { padding: 4px 8px; font-size: 11px; }
```

The classes are no longer used. Delete the rule (or, if a phone-specific `.btn-ghost` size adjustment is needed, replace it with: `.workspace-header .btn-ghost { padding: var(--space-2) var(--space-3); }` — but only if visual review at phone width shows the standard size is too cramped).

Also delete:

```css
  .input-btn-primary kbd { display: none; }
```

(Replace with `.btn-primary kbd { display: none; }` if you still want to hide the kbd hint on phone, which is the current behavior.)

- [ ] **Step 3: Build and full regression**

```bash
cd /home/jesse/git/prime-radiant/serf && go build ./cmd/serf-hub && go test ./cmd/serf-hub -count=1
cd cmd/serf-hub/jstest && for f in *.js; do node "$f" || { echo "FAIL: $f"; break; }; done
```

Expected: PASS / exit 0 across the board.

- [ ] **Step 4: Visual regression on every migrated surface**

Walk through: `/new` (spawn), an active session, an awaiting session, a closed session, `/credentials`, `/settings/general`, `/settings/launch`, `/settings/plugins`, `/settings/skills`, `/settings/mcp`, `/settings/project`, `/settings/providers`, `/settings/inrepo`. Confirm:
- Every button renders. None is unstyled / browser-default.
- Send / spawn / Save buttons accent-colored.
- Stop button red-tinted when active, neutral when disabled.
- Tasks / details ghost-style.
- Both light and dark themes.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/style.css
git commit -m "$(cat <<'EOF'
ui: delete legacy button CSS

Every template + JS site now uses .btn variants. Remove the old
.input-btn*, .header-action*, .panel-toggle, .title-action, .chip,
.spawn-btn, .spawn-attach-btn, .fork-cancel, .fork-confirm,
.input-chip rules. Marker classes (send-btn, stop-btn, model-chip,
etc.) remain as JS hooks with no CSS rules of their own. Pass 5.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 13: Add `.status-badge` CSS

**Files:**
- Modify: `cmd/serf-hub/assets/style.css` (add `.status-badge` rules + state colors)

- [ ] **Step 1: Append the `.status-badge` block**

Below the button-system block, append:

```css
/* ── Status badge (Pass 5) ────────────────────────────────────────────────
   Typographic small-caps mono in state color. No background fill — replaces
   the rgba-tinted .status-pill which read as generic SaaS chrome. See
   design language §3.3. */
.status-badge {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  font-weight: 500;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--text-muted);
}
.status-badge[data-state="active"]    { color: var(--state-processing); }
.status-badge[data-state="awaiting"]  { color: var(--state-awaiting); }
.status-badge[data-state="errored"]   { color: var(--state-awaiting); }
.status-badge[data-state="warning"]   { color: var(--state-warning); }
.status-badge[data-state="idle"]      { color: var(--state-idle); }
.status-badge[data-state="ended"]     { color: var(--text-muted); }
.status-badge[data-state="closed"]    { color: var(--text-muted); }
.status-badge[data-state="notLoaded"] { color: var(--text-muted); }

/* Status badge variants used in settings rows: alias the legacy
   .status-running / .status-available / .status-stopped / .status-error
   / .status-missing / .status-unreachable / .status-unknown markers
   onto the same color logic so settings/providers.html's
   "status-${activeSource}" form keeps working. */
.status-badge.status-running,
.status-badge.status-available { color: var(--state-idle); }
.status-badge.status-error,
.status-badge.status-missing   { color: var(--state-awaiting); }
.status-badge.status-unreachable { color: var(--state-warning); }
.status-badge.status-stopped,
.status-badge.status-unknown,
.status-badge.status-absent,
.status-badge.status-file,
.status-badge.status-env,
.status-badge.status-oauth,
.status-badge.status-none      { color: var(--text-muted); }

/* Add coverage for status dots in [data-state="errored|closed|notLoaded"]
   so dots in templates that don't get a normalized state still render. */
.status-dot[data-state="errored"]   { background: var(--state-awaiting); }
.status-dot[data-state="closed"],
.status-dot[data-state="notLoaded"] { background: var(--state-ended); }
```

- [ ] **Step 2: Build**

```bash
cd /home/jesse/git/prime-radiant/serf && go build ./cmd/serf-hub
```

Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add cmd/serf-hub/assets/style.css
git commit -m "$(cat <<'EOF'
ui: add .status-badge CSS

Typographic small-caps mono in state color, no background fill.
Replaces .status-pill (still in tree until templates migrate in next
task). Covers errored, closed, notLoaded state values + legacy
.status-{running,available,stopped,error,missing,unreachable,unknown,
absent,file,env,oauth,none} marker classes. Adds .status-dot rules
for the three additional state values. Pass 5.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 14: Migrate `.status-pill` templates to `.status-badge`

**Files:**
- Modify: `cmd/serf-hub/templates/partials/workspace.html`
- Modify: `cmd/serf-hub/templates/partials/settings/providers.html`
- Modify: `cmd/serf-hub/assets/style.css` (delete the legacy `.status-pill*` rules)

- [ ] **Step 1: Migrate the workspace meta status-pill**

In `cmd/serf-hub/templates/partials/workspace.html:80`, the inline `workspace_meta` template currently reads:

```html
{{define "workspace_meta"}}{{if .SourceLabel}}<span class="source-label" data-source-label="{{.SourceLabel}}">{{.SourceLabel}}</span><span class="rule-dot">·</span>{{end}}{{if .Branch}}<span class="branch">{{.Branch}}</span><span class="rule-dot">·</span>{{end}}<span class="status-pill" data-state="{{.State}}"><span class="status-dot" data-state="{{.State}}"></span> {{.StateLabel}}</span><span class="rule-dot">·</span><span class="turn-count">{{.TurnCount}} turn{{if ne .TurnCount 1}}s{{end}}</span>{{end}}
```

Change the `<span class="status-pill" …>` portion to `<span class="status-badge" …>`:

```html
{{define "workspace_meta"}}{{if .SourceLabel}}<span class="source-label" data-source-label="{{.SourceLabel}}">{{.SourceLabel}}</span><span class="rule-dot">·</span>{{end}}{{if .Branch}}<span class="branch">{{.Branch}}</span><span class="rule-dot">·</span>{{end}}<span class="status-badge" data-state="{{.State}}"><span class="status-dot" data-state="{{.State}}"></span> {{.StateLabel}}</span><span class="rule-dot">·</span><span class="turn-count">{{.TurnCount}} turn{{if ne .TurnCount 1}}s{{end}}</span>{{end}}
```

- [ ] **Step 2: Migrate the providers settings status-pill**

In `cmd/serf-hub/templates/partials/settings/providers.html:19`, change:

```html
            <span class="status-pill status-${p.activeSource}">${p.activeSource}</span>
```

to:

```html
            <span class="status-badge status-${p.activeSource}" data-state="${p.activeSource === 'absent' || p.activeSource === 'none' ? 'closed' : 'idle'}">${p.activeSource}</span>
```

(The `data-state` attribute drives state color; the legacy `status-${activeSource}` marker class still picks up the secondary color rules added in Task 13. This belt-and-braces lets settings/providers render correctly whether the activeSource maps to a known state or not.)

- [ ] **Step 3: Delete the legacy `.status-pill*` rules from style.css**

In `cmd/serf-hub/assets/style.css`:

- Line ~356 — delete `.status-pill { display: inline-flex; align-items: baseline; gap: 6px; }`.
- Lines ~759–766 — delete the entire `.status-pill` family:

```css
.settings-row-title .status-pill { margin-left: 8px; padding: 1px 8px; border-radius: 10px; font-size: 11px; font-family: ui-monospace, "SFMono-Regular", monospace; font-weight: 400; }
.status-pill.status-running { background: rgba(158, 206, 106, 0.15); color: var(--state-idle); }
.status-pill.status-available { background: rgba(158, 206, 106, 0.15); color: var(--state-idle); }
.status-pill.status-stopped { background: var(--bg-raised); color: var(--text-muted); }
.status-pill.status-error { background: rgba(247, 118, 142, 0.15); color: var(--state-awaiting); }
.status-pill.status-missing { background: rgba(247, 118, 142, 0.15); color: var(--state-awaiting); }
.status-pill.status-unreachable { background: rgba(224, 175, 104, 0.15); color: var(--state-warning); }
.status-pill.status-unknown { background: var(--bg-raised); color: var(--text-muted); }
```

Add a small replacement so the badge keeps a left margin inside settings rows:

```css
.settings-row-title .status-badge { margin-left: var(--space-3); }
```

- [ ] **Step 4: Build and test**

```bash
cd /home/jesse/git/prime-radiant/serf && go build ./cmd/serf-hub && go test ./cmd/serf-hub -count=1
```

Expected: PASS.

Visually: open an active session — confirm the header status badge reads as mono small-caps `ACTIVE` in `--state-processing` color with a dot to its left. Navigate to `/settings/providers` — confirm the per-provider activeSource label is mono small-caps and colored.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/templates/partials/workspace.html cmd/serf-hub/templates/partials/settings/providers.html cmd/serf-hub/assets/style.css
git commit -m "$(cat <<'EOF'
ui: replace .status-pill with .status-badge in templates

workspace meta and settings/providers now render the typographic
status badge instead of the rgba-tinted pill. Legacy .status-pill
CSS rules removed. Pass 5.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 15: Drive `[data-pulse]` from JS

**Files:**
- Modify: `cmd/serf-hub/assets/renderer.js`
- Modify: `cmd/serf-hub/assets/sidebar.js`
- Modify: `cmd/serf-hub/assets/search.js`
- Modify: `cmd/serf-hub/assets/style.css` (pulse keyframes + `[data-pulse]` selector)

- [ ] **Step 1: Add the keyframes + selector to style.css**

Append to `cmd/serf-hub/assets/style.css`:

```css
/* ── Status-dot pulse (Pass 5) ────────────────────────────────────────────
   JS sets [data-pulse] on .status-dot when [data-state] ∈
   {active, awaiting, errored}. Reduced-motion override in the global
   media query already neutralizes the animation. */
.status-dot[data-pulse] {
  animation: status-dot-pulse var(--pulse-cycle) ease-in-out infinite;
}
@keyframes status-dot-pulse {
  0%, 100% { opacity: 1; }
  50%      { opacity: 0.5; }
}
```

- [ ] **Step 2: Add the `applyStatusDotPulse` helper in renderer.js**

Open `cmd/serf-hub/assets/renderer.js`. At the bottom of the file (or in the same scope as `setPanelToggleActive` around line 2843), add:

```javascript
  // applyStatusDotPulse sets [data-pulse] on every .status-dot under root
  // whose data-state is in the "should breathe" set. Idempotent. Called
  // after any DOM change that may have introduced status dots.
  function applyStatusDotPulse(root) {
    const scope = root || document;
    const dots = scope.querySelectorAll(".status-dot[data-state]");
    dots.forEach(dot => {
      const state = dot.getAttribute("data-state");
      const shouldPulse = state === "active" || state === "awaiting" || state === "errored";
      if (shouldPulse) {
        dot.setAttribute("data-pulse", "");
      } else {
        dot.removeAttribute("data-pulse");
      }
    });
  }
  // Expose so sidebar.js / search.js can call it after their own swaps.
  if (typeof window !== "undefined") {
    window.SerfRenderer = window.SerfRenderer || {};
    window.SerfRenderer.applyStatusDotPulse = applyStatusDotPulse;
  }
```

(If a `window.SerfRenderer` namespace is already attached in this file, hang the helper off that. If not, the `||=` form above creates it.)

- [ ] **Step 3: Call `applyStatusDotPulse` after htmx swaps in renderer.js**

Find the existing `htmx:afterSwap` listener in renderer.js (search the file for `afterSwap`). After the swap is processed, invoke:

```javascript
        if (window.SerfRenderer && window.SerfRenderer.applyStatusDotPulse) {
          window.SerfRenderer.applyStatusDotPulse(document);
        }
```

If renderer.js has no `afterSwap` listener already, add a small handler at module init:

```javascript
  document.addEventListener("htmx:afterSwap", () => {
    if (window.SerfRenderer && window.SerfRenderer.applyStatusDotPulse) {
      window.SerfRenderer.applyStatusDotPulse(document);
    }
  });
```

Also call it once at module load (in case the initial render had dots already on the page):

```javascript
  if (typeof window !== "undefined" && window.SerfRenderer && window.SerfRenderer.applyStatusDotPulse) {
    window.SerfRenderer.applyStatusDotPulse(document);
  }
```

- [ ] **Step 4: Call `applyStatusDotPulse` from sidebar.js after its own renders**

Open `cmd/serf-hub/assets/sidebar.js`. Find the function that runs after the sidebar partial is swapped in (look for `htmx:afterSwap` listener scoped to `#sidebar`, or any function that processes a fresh sidebar DOM). At the end of that function, add:

```javascript
  if (window.SerfRenderer && window.SerfRenderer.applyStatusDotPulse) {
    window.SerfRenderer.applyStatusDotPulse(document.getElementById("sidebar") || document);
  }
```

If no such hook exists, register one at the top level of sidebar.js:

```javascript
  document.addEventListener("htmx:afterSwap", (ev) => {
    if (!ev.target || ev.target.id !== "sidebar") return;
    if (window.SerfRenderer && window.SerfRenderer.applyStatusDotPulse) {
      window.SerfRenderer.applyStatusDotPulse(ev.target);
    }
  });
```

- [ ] **Step 5: Set `data-pulse` directly in search.js when rendering result rows**

In `cmd/serf-hub/assets/search.js:821`, the rendered dot string is:

```javascript
           '<span class="status-dot" data-state="' + escapeHtml(r.state || "ended") + '"></span>' +
```

Change to (inline the pulse logic — search rows render in a tight loop, simpler than a post-render walk):

```javascript
           '<span class="status-dot" data-state="' + escapeHtml(r.state || "ended") + '"' +
           ((r.state === "active" || r.state === "awaiting" || r.state === "errored") ? ' data-pulse=""' : '') +
           '></span>' +
```

- [ ] **Step 6: Build + test**

```bash
cd /home/jesse/git/prime-radiant/serf && go build ./cmd/serf-hub && go test ./cmd/serf-hub -count=1
cd cmd/serf-hub/jstest && node test-sidebar.js && node test-search.js 2>/dev/null || true
```

Visually: open a session that's active. Confirm the sidebar's status dot for that row breathes (opacity oscillates over ~2s). Confirm the workspace header's status dot also breathes. Open the search palette — a live active session in the results breathes.

- [ ] **Step 7: Verify reduced-motion**

In the browser's devtools, toggle `prefers-reduced-motion: reduce`. Confirm the dot stops breathing (the global `*` animation override neutralizes it).

- [ ] **Step 8: Commit**

```bash
git add cmd/serf-hub/assets/renderer.js cmd/serf-hub/assets/sidebar.js cmd/serf-hub/assets/search.js cmd/serf-hub/assets/style.css
git commit -m "$(cat <<'EOF'
ui: drive [data-pulse] on status dots from JS

renderer.js owns applyStatusDotPulse(root); sidebar + search call
it after their own renders. CSS adds the 2s keyframes animation on
.status-dot[data-pulse]. Reduced-motion already neutralizes via the
global * override. Pass 5.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 16: Universal `:focus-visible` rings + per-component overrides

**Files:**
- Modify: `cmd/serf-hub/assets/style.css`

- [ ] **Step 1: Add the universal `:where(:focus-visible)` rule**

Near the top of the components section in `style.css` (after the `:root` blocks and before any component rules — a comment marks a good location, but inserting just before the `* { box-sizing: border-box; }` line at line 112 is safest), add:

```css
/* ── Universal focus ring (Pass 5) ────────────────────────────────────────
   :where() gives this rule zero specificity so per-component overrides
   with negative offsets win. See design language §6.1. */
:where(:focus-visible) {
  outline: 2px solid var(--accent);
  outline-offset: 1px;
}
```

- [ ] **Step 2: Add per-component negative-offset overrides**

For controls where the +1px offset escapes the radius (the ring would be visibly inside the next row's padding), use `outline-offset: -2px`. Append to style.css near the end of the file:

```css
/* Per-component focus offset overrides — rounded rows where the +1px
   ring would visually escape the radius. Specificity beats the
   zero-specificity :where() rule above. */
.search-row:focus-visible,
.sb-row:focus-visible,
.settings-nav-link:focus-visible,
.row-icon-btn:focus-visible,
.spawn-recent-row:focus-visible,
.tasks-list .task-row-details > summary:focus-visible,
.search-cmd-pill-back:focus-visible,
.composer-attachment-remove:focus-visible,
.attachment-chip .att-remove:focus-visible {
  outline-offset: -2px;
}

/* Chip picker option needs a snug inset to match other rounded rows. */
.chip-picker-option:focus-visible,
.chip-picker-model:focus-visible,
.chip-picker-provider:focus-visible,
.chip-picker-dir-row:focus-visible {
  outline-offset: -2px;
}
```

- [ ] **Step 3: Remove the now-redundant per-button `:focus-visible` rule**

In `cmd/serf-hub/assets/style.css`, locate the existing `.spawn-attach-btn:focus-visible { outline: ...; outline-offset: 1px; }` rule at line 621 (or wherever it survived Task 12). It's been superseded by the universal rule. If still present, delete it. Also remove any other one-off `:focus-visible` rule that exactly matches the universal `outline: 2px solid var(--accent); outline-offset: 1px;` pattern.

- [ ] **Step 4: Build**

```bash
cd /home/jesse/git/prime-radiant/serf && go build ./cmd/serf-hub
```

- [ ] **Step 5: Verify focus rings with the keyboard**

Launch the hub. Press Tab from the URL bar into the body. Confirm the focus ring appears on:
- Sidebar's `+ new`, `search`, `settings` links.
- Each sidebar row (with `outline-offset: -2px`, hugging the rounded shape).
- Project chevron + gear + ＋ icons.
- Workspace header copy, tasks, details buttons.
- Composer textarea, attach, model chip, Stop, send-as-steer, send.
- `/new` form chips and the spawn textarea + submit.
- Search palette input + each result row (`outline-offset: -2px`).
- Settings nav links (`outline-offset: -2px`).
- Settings sub-page Save buttons.

Both light and dark themes. Confirm the ring color resolves to `--accent` in both.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/assets/style.css
git commit -m "$(cat <<'EOF'
ui: add universal :where(:focus-visible) ring + per-component offsets

Zero-specificity universal rule applies to every focusable element.
Per-component overrides for rounded rows (.sb-row, .search-row,
.settings-nav-link, chip-picker rows, etc.) use outline-offset: -2px
to keep the ring inside the radius. Design language §6.1. Pass 5.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 17: `[data-active]` for sub-tab pressed state

**Files:**
- Modify: `cmd/serf-hub/assets/renderer.js` (`setPanelToggleActive`)

The `.btn-ghost[data-active]` rule from Task 3 already styles the pressed state. This task switches the JS that toggles `.active` to instead toggle `data-active`.

- [ ] **Step 1: Edit `setPanelToggleActive`**

In `cmd/serf-hub/assets/renderer.js` lines 2843–2846:

```javascript
  function setPanelToggleActive(selector, active) {
    const btn = document.querySelector(selector);
    if (btn) btn.classList.toggle("active", !!active);
  }
```

Change to:

```javascript
  function setPanelToggleActive(selector, active) {
    const btn = document.querySelector(selector);
    if (!btn) return;
    if (active) {
      btn.setAttribute("data-active", "");
    } else {
      btn.removeAttribute("data-active");
    }
  }
```

- [ ] **Step 2: Build and test**

```bash
cd /home/jesse/git/prime-radiant/serf && go build ./cmd/serf-hub && go test ./cmd/serf-hub -count=1
cd cmd/serf-hub/jstest && node test-panels.js && node test-renderer.js
```

Expected: PASS / exit 0. If a panel-toggle test specifically asserts `.active` class, update that assertion to `getAttribute("data-active")` instead. Search:

```bash
cd /home/jesse/git/prime-radiant/serf && grep -rn "panel-toggle.*\.active\|classList.contains..active.*panel" cmd/serf-hub/jstest/
```

If any matches, fix them as part of this commit.

- [ ] **Step 3: Visually verify**

Open a session. Click the `tasks` button — confirm it gets the pressed surface state via `[data-active]` (`background: var(--bg-raised); color: var(--text); border-color: var(--rule);` from the `.btn-ghost[data-active]` rule). Click again to close; the state clears.

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-hub/assets/renderer.js
git commit -m "$(cat <<'EOF'
ui: switch panel-toggle from .active class to [data-active]

setPanelToggleActive now toggles the data-active attribute. The
.btn-ghost[data-active] CSS rule (added with .btn-ghost in Task 3)
handles the pressed surface state. Matches the convention sidebar.html
uses for the [data-active] currently-selected row. Pass 5.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 18: Final verification

**Files:** none (verification only).

- [ ] **Step 1: Full build + full test pass**

```bash
cd /home/jesse/git/prime-radiant/serf && go build ./... && go test ./cmd/serf-hub -count=1
cd cmd/serf-hub/jstest && for f in *.js; do echo "=== $f ==="; node "$f" || { echo "FAIL: $f"; exit 1; }; done
```

Expected: every test PASS / exit 0.

- [ ] **Step 2: Hub walk-through, dark theme**

Launch the hub. Walk every surface and verify all buttons render correctly:

- `/new` (spawn): 5 chips + 📎 attach + spawn submit.
- Session with state `active`: workspace title copy ⧉, tasks, details, attach ＋, model chip, send-as-steer, send, status badge `ACTIVE` (mono small-caps in `--state-processing`), status-dot pulse.
- Session with state `awaiting`: status badge `AWAITING` in `--state-awaiting`, dot pulse.
- Session with state `errored`: badge in `--state-awaiting`, dot pulse.
- Session with state `closed`: badge in `--text-muted`, no pulse.
- Session with state `idle`: badge in `--state-idle`, no pulse.
- Stop button enabled while active, disabled when idle.
- Fork dialog (edit a user message → confirm/cancel buttons).
- `/credentials`: provider rows render Set/OAuth/Clear, and editor Save/Cancel.
- `/settings/general`: read-only.
- `/settings/launch`: Save button accent.
- `/settings/providers`: per-provider activeSource label as mono small-caps badge.
- `/settings/inrepo`: Trust button accent when shown.
- `/settings/plugins`, `/settings/skills`, `/settings/mcp`: Add accent, Remove ghost.
- `/settings/project`: Save accent.

- [ ] **Step 3: Repeat in light theme**

Toggle theme. Walk the same surfaces. Confirm:
- Accent on primary buttons reads `#fafafa` text on `--accent` (`#3b6fc9`), legible.
- Danger buttons render with the light-theme `--state-awaiting` (`#c43755`).
- Status badge colors all shift to their light-theme variants.

- [ ] **Step 4: Keyboard tour**

Reload, press Tab repeatedly from the URL bar. Confirm a visible focus ring appears on every interactive element, with `--accent` color in both themes. Esc closes modal/drawer surfaces and restores focus to the trigger.

- [ ] **Step 5: prefers-reduced-motion**

In devtools, enable `prefers-reduced-motion: reduce`. Confirm status-dot pulse stops (the global `*::after` reduce rule already neutralizes animations).

- [ ] **Step 6: Lint check — no legacy class remnants**

```bash
cd /home/jesse/git/prime-radiant/serf && grep -rn 'class="[^"]*\(input-btn\b\|input-btn-primary\|input-btn-stop\|input-btn-ghost\|header-action\|spawn-attach-btn\|fork-confirm\|fork-cancel\|title-action\|panel-toggle\b\|chip-mode\|input-chip\|status-pill\)' cmd/serf-hub/templates/ cmd/serf-hub/assets/*.js | grep -v "// "
```

Expected: no output. Marker classes like `send-btn`, `stop-btn`, `model-chip`, `spawn-btn`, `spawn-attach-btn`, `fork-confirm`, `fork-cancel`, `title-action`, `panel-toggle`, `project-new-btn`, `project-gear-btn` may still appear, but each must be co-applied with `btn`. Spot-check.

- [ ] **Step 7: No commit (verification only)**

If verifications pass, the migration is complete. If any verification fails, file the gap as a follow-up task (or fix inline and commit).

---

## Self-Review Notes (from writer)

- Spec coverage: every legacy → new button mapping in §3.1 has a migration task (7–11) and a CSS-delete task (12). `.status-badge` from §3.3 is added (13) and migrated (14). `[data-pulse]` from §3.3 is wired (15). Universal `:focus-visible` from §6.1 is added (16). `[data-active]` switch from §3.1 is done (17). Project chevron a11y from §6.3 is addressed in Task 9 Step 4.
- Tokens used (`--btn-primary-text`, `--space-*`, `--font-sans`, `--font-mono`, `--text-*`, `--radius-md`, `--radius-pill`, `--tap-min`, `--motion-fast`, `--pulse-cycle`, `--accent`, `--state-*`, `--bg-raised`, `--surface-secondary`, `--text-muted`, `--text`, `--rule`) are listed in the pre-conditions; if any are missing when this plan starts, the implementer is told to stop and confirm.
- JS hooks: marker classes (`send-btn`, `stop-btn`, `model-chip`, `spawn-btn`, `spawn-attach-btn`, `fork-cancel`, `fork-confirm`, `title-action`, `panel-toggle`, `project-new-btn`, `project-gear-btn`) are preserved on the elements so existing selectors keep working. The legacy CSS rules for those marker classes are removed in Task 12 — the marker classes have no styles of their own after this plan.
- The spec uses both `errored` and `awaiting` for the same color — both have explicit rules in Tasks 13 and 15.
- `closed` and `notLoaded` map to `--state-ended` for dots and `--text-muted` for badges — both handled in Task 13.

---

**Plan complete and saved to `docs/superpowers/plans/2026-05-22-serf-hub-ui-pass-5-buttons-and-focus.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

**Which approach?**