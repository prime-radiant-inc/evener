# Serf Hub UI Pass 7 — Settings Sub-Page Consolidation

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Collapse all 14 settings sub-pages (plus credentials) onto two canonical primitives — `settings-table` and `settings-collection` — defined in §4.2 of the design language. After this pass every "label → value" page is a `<dl class="settings-table">` and every dynamic add/remove list is a `<section class="settings-collection">`, with one documented one-off (in-repo trust controls). `LaunchConfigControls.render()` is reworked to emit `settings-table` row markup; the JS API contract is preserved.

**Architecture:** CSS + templates + one JS file. No Go changes. No new endpoints. All sub-pages keep their existing `hx-get` swap targets and `data-launch-*` attributes; only the visual markup changes. The 14 templates under `cmd/serf-hub/templates/partials/settings/`, plus `credentials.html`, are rewritten. `assets/launchconfig.js` is updated to emit the new row primitive. `assets/style.css` gets ~250 lines of new primitive CSS (and ~120 lines of legacy settings rules deleted after migration; the actual delete happens in Pass 8's CSS sweep, not here).

**Tech Stack:** Go html/template, HTMX, vanilla browser JavaScript, CSS custom properties + grid.

**Design language anchor:** Every CSS rule in this plan implements §4.2 of `docs/superpowers/specs/2026-05-22-serf-hub-design-language.md`. Re-read that section before starting. The sub-page taxonomy table in §4.2 is the contract this pass delivers.

**Dependencies (must ship first):** Passes 1–3 (tokens + fonts + typography + spacing/radius/motion/z), Pass 4 (button variants — `.btn`, `.btn-primary`, `.btn-secondary`, `.btn-icon`, focus rings, `.status-badge`), and Pass 5 (sidebar restructure — provides nothing this pass strictly depends on but is the agreed ship order).

---

## File Touch Map

- Modify: `cmd/serf-hub/assets/style.css` — add `.settings-table`, `.settings-collection`, value-cell variants, section-header row variant, phone nav-as-page rules, settings-nav search input.
- Modify: `cmd/serf-hub/assets/launchconfig.js` — rewrite the markup emitted by `render()` to produce `settings-table .row` (or `section-header` row) elements; rewrite list/env/mcp control wrappers to render as `settings-collection` blocks inside a row. Preserve every `data-launch-*` attribute name and every internal selector (validate/collect read `[data-launch-wire-field]`, `[data-launch-list]`, `[data-launch-env-list]`, `[data-launch-mcp-list]`, `[data-launch-explicit-empty]`, `[data-launch-validation-error]`, `[data-launch-invalid]`, `[data-launch-kind]`, `[data-launch-path-kind]`, `[data-launch-option]`, `[data-launch-mcp-command]`).
- Modify: `cmd/serf-hub/templates/partials/settings.html` — add search filter `<input>` at top of `<nav class="settings-nav">` and a `<a class="settings-nav-back" hidden>` element used by phone nav-as-page.
- Modify (rewrite content): every file in `cmd/serf-hub/templates/partials/settings/` (14 files).
- Modify: `cmd/serf-hub/templates/partials/credentials.html` — rewrite JS to emit `settings-collection` markup.
- Modify: `cmd/serf-hub/assets/settings.js` — add settings-nav search filter handler; add phone nav-as-page back-chevron handler; add `data-phone-density` body-attribute wiring for the new theme row.
- Modify: `cmd/serf-hub/jstest/test-launchconfig-controls.js` — update the two `.settings-add-row` selectors (line 254–255) to `.settings-collection-add`. Existing `data-launch-*` selectors remain untouched.
- Optional: scan `cmd/serf-hub/web_test.go` for any literal `.settings-list`, `.settings-form`, `.settings-rows`, `.settings-row-title`, `.settings-toggle`, `.settings-radio`, `.settings-launch-form`, `.settings-launch-row`, `.settings-launch-group`, `.spawn-advanced-row` references. None expected (typography + class names changed in Pass 2; Pass 7 finalizes the markup shape) but a `grep -n` is part of every template-touching task's verification.

---

## Design-Language Reference: §4.2 Settings-Table CSS

This is the authoritative CSS for the row primitive. Every rule below is copied verbatim from the design language §4.2 (see `docs/superpowers/specs/2026-05-22-serf-hub-design-language.md`). Tasks 1–3 install this; later tasks consume it.

```css
.settings-table {
  margin: 0;
  max-width: 760px;
  border-top: 1px solid var(--rule);
}
.settings-table .row {
  display: grid;
  grid-template-columns: 160px 1fr;
  column-gap: var(--space-4);
  row-gap: var(--space-2);
  align-items: baseline;
  padding: var(--space-4) 0;
  border-bottom: 1px solid var(--rule);
}
.settings-table dt {
  margin: 0;
  font-family: var(--font-sans);
  font-size: var(--text-base);
  font-weight: 500;
  color: var(--text);
}
.settings-table dd {
  margin: 0;
  font-family: var(--font-mono);
  font-size: var(--text-sm);
  color: var(--text);
}
.settings-table .help {
  grid-column: 1 / -1;
  margin: 0;
  font-family: var(--font-sans);
  font-size: var(--text-xs);
  color: var(--text-muted);
  font-weight: 350;
  line-height: 1.55;
  max-width: 620px;
}
.settings-table .row.editable { cursor: pointer; }
.settings-table .row.editable:hover {
  background: color-mix(in srgb, var(--text) 2%, transparent);
}
```

Value cells:

| Variant | Use |
| --- | --- |
| Plain mono in `<dd>` | Read-only values (paths, addresses, durations) |
| `.val-text` (with `.dim` subspans) | Read-only with annotation (size, age) |
| `.status-badge` | Stateful read-only (provider availability) |
| `.val-input` | Text/number input (mono) |
| `.val-select` | Dropdown |
| `.val-radio-group` + `.val-radio` | Inline segmented radios |
| `.val-toggle` (with `.state` ON/OFF mono pill) | Checkbox |
| `.row.section-header` (new variant) | Nested fieldset header — `<dt>` empty, `<dd>` spans both columns, uppercase mono |

---

## Task 1: Add settings-table primitive CSS

**Files:**
- Modify: `cmd/serf-hub/assets/style.css`

- [ ] **Step 1:** Locate the existing `/* ── Settings page ──── */` block in `style.css` (around line 729). Leave the `.settings-pane`, `.settings-nav`, `.settings-nav-section`, `.settings-nav-link`, `.settings-content`, `.settings-h2`, `.settings-help`, `.settings-h3` rules in place — they describe the shell. Append the new primitive immediately after `.settings-h3` and before the legacy `.settings-form` / `.settings-list` / `.settings-rows` block. The legacy block stays during the pass; Pass 8's CSS sweep removes it.

- [ ] **Step 2:** Insert the following block. Each rule is taken from §4.2 of the design language; the value-cell variants extend it.

```css
/* ── settings-table primitive (design language §4.2) ───────────────────── */

.settings-table {
  margin: 0;
  max-width: 760px;
  border-top: 1px solid var(--rule);
}
.settings-table .row {
  display: grid;
  grid-template-columns: 160px 1fr;
  column-gap: var(--space-4);
  row-gap: var(--space-2);
  align-items: baseline;
  padding: var(--space-4) 0;
  border-bottom: 1px solid var(--rule);
}
.settings-table dt {
  margin: 0;
  font-family: var(--font-sans);
  font-size: var(--text-base);
  font-weight: 500;
  color: var(--text);
}
.settings-table dd {
  margin: 0;
  font-family: var(--font-mono);
  font-size: var(--text-sm);
  color: var(--text);
}
.settings-table .help {
  grid-column: 1 / -1;
  margin: 0;
  font-family: var(--font-sans);
  font-size: var(--text-xs);
  color: var(--text-muted);
  font-weight: 350;
  line-height: 1.55;
  max-width: 620px;
}
.settings-table .row.editable { cursor: pointer; }
.settings-table .row.editable:hover {
  background: color-mix(in srgb, var(--text) 2%, transparent);
}

/* Value cells (variants of <dd>) */
.settings-table .val-text {
  font-family: var(--font-mono);
  font-size: var(--text-sm);
  color: var(--text);
}
.settings-table .val-text .dim {
  color: var(--text-muted);
  margin-left: var(--space-2);
}
.settings-table .val-input,
.settings-table .val-select {
  width: 100%;
  max-width: 320px;
  padding: var(--space-2) var(--space-3);
  font-family: var(--font-mono);
  font-size: var(--text-sm);
  color: var(--text);
  background: var(--bg-raised);
  border: 1px solid var(--rule);
  border-radius: var(--radius-md);
  outline: none;
  transition: border-color var(--motion-fast);
}
.settings-table textarea.val-input {
  min-height: 132px;
  line-height: 1.45;
  resize: vertical;
  max-width: 100%;
}
.settings-table .val-input:focus-visible,
.settings-table .val-select:focus-visible { border-color: var(--accent); }
.settings-table .val-input[aria-invalid="true"],
.settings-table .val-input[data-launch-invalid="true"] { border-color: var(--state-awaiting); }

.settings-table .val-radio-group {
  display: inline-flex;
  flex-wrap: wrap;
  gap: var(--space-2);
}
.settings-table .val-radio {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-2) var(--space-3);
  background: var(--bg-raised);
  border: 1px solid var(--rule);
  border-radius: var(--radius-pill);
  font-family: var(--font-sans);
  font-size: var(--text-sm);
  color: var(--text);
  cursor: pointer;
  transition: border-color var(--motion-fast), background var(--motion-fast);
}
.settings-table .val-radio input[type="radio"] {
  appearance: none;
  width: 10px;
  height: 10px;
  border-radius: 50%;
  border: 1px solid var(--text-dim);
  margin: 0;
}
.settings-table .val-radio input[type="radio"]:checked {
  background: var(--accent);
  border-color: var(--accent);
}
.settings-table .val-radio[data-checked],
.settings-table .val-radio:has(input[type="radio"]:checked) {
  border-color: var(--accent);
  background: color-mix(in srgb, var(--accent) 6%, transparent);
}

.settings-table .val-toggle {
  display: inline-flex;
  align-items: center;
  gap: var(--space-3);
  cursor: pointer;
  font-family: var(--font-sans);
  font-size: var(--text-sm);
  color: var(--text);
}
.settings-table .val-toggle input[type="checkbox"] {
  appearance: none;
  width: 28px;
  height: 16px;
  background: var(--surface-secondary);
  border: 1px solid var(--rule);
  border-radius: var(--radius-pill);
  position: relative;
  cursor: pointer;
  transition: background var(--motion-fast), border-color var(--motion-fast);
}
.settings-table .val-toggle input[type="checkbox"]::after {
  content: "";
  position: absolute;
  top: 1px;
  left: 1px;
  width: 12px;
  height: 12px;
  background: var(--text-muted);
  border-radius: 50%;
  transition: transform var(--motion-fast), background var(--motion-fast);
}
.settings-table .val-toggle input[type="checkbox"]:checked {
  background: color-mix(in srgb, var(--accent) 30%, transparent);
  border-color: var(--accent);
}
.settings-table .val-toggle input[type="checkbox"]:checked::after {
  transform: translateX(12px);
  background: var(--accent);
}
.settings-table .val-toggle .state {
  font-family: var(--font-mono);
  font-size: var(--text-2xs);
  font-weight: 500;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--text-muted);
  min-width: 26px;
}
.settings-table .val-toggle:has(input[type="checkbox"]:checked) .state { color: var(--accent); }
```

- [ ] **Step 3:** Verify the existing token names (`--text-sm`, `--text-base`, `--text-xs`, `--text-2xs`, `--space-2`, `--space-3`, `--space-4`, `--motion-fast`, `--radius-pill`, `--radius-md`, `--bg-raised`, `--surface-secondary`, `--rule`, `--text`, `--text-muted`, `--text-dim`, `--accent`, `--state-awaiting`, `--font-sans`, `--font-mono`) all resolve. They are introduced in Pass 1. If any are missing, fail loudly — do NOT add fallbacks; the pass dependency is firm.

---

## Task 2: Add settings-collection primitive CSS

**Files:**
- Modify: `cmd/serf-hub/assets/style.css`

- [ ] **Step 1:** Immediately after the value-cell variants from Task 1, append:

```css
/* ── settings-collection primitive (design language §4.2) ──────────────── */

.settings-collection {
  margin: 0 0 var(--space-6);
  max-width: 760px;
}
.settings-collection-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  padding: var(--space-3) 0;
  border-bottom: 1px solid var(--rule);
  margin-bottom: var(--space-2);
}
.settings-collection-head h3 {
  margin: 0;
  font-family: var(--font-sans);
  font-size: var(--text-base);
  font-weight: 500;
  color: var(--text);
}
.settings-collection-count {
  font-family: var(--font-mono);
  font-size: var(--text-2xs);
  color: var(--text-muted);
  letter-spacing: 0.04em;
}
.settings-collection-list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.settings-collection-row {
  display: grid;
  grid-template-columns: 1fr auto;
  column-gap: var(--space-3);
  align-items: baseline;
  padding: var(--space-3) 0;
  border-bottom: 1px solid var(--rule-soft);
}
.settings-collection-row:last-child { border-bottom: none; }
.settings-collection-row .row-text {
  font-family: var(--font-mono);
  font-size: var(--text-sm);
  color: var(--text);
  word-break: break-all;
}
.settings-collection-row .row-meta {
  margin-top: 2px;
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  color: var(--text-muted);
}
.settings-collection-row .row-actions {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
}
.settings-collection-empty {
  padding: var(--space-4) 0;
  color: var(--text-muted);
  font-family: var(--font-sans);
  font-size: var(--text-sm);
  font-style: italic;
}
.settings-collection-add {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-3) 0;
  border-top: 1px solid var(--rule);
  flex-wrap: wrap;
}
.settings-collection-add .val-input {
  flex: 1;
  min-width: 200px;
}
.settings-collection-add .val-input + .val-input { flex: 0 0 auto; min-width: 140px; }
.settings-collection-add .row-error {
  flex-basis: 100%;
  margin: 0;
  font-family: var(--font-sans);
  font-size: var(--text-xs);
  color: var(--state-awaiting);
}
.settings-collection-add .row-error[hidden] { display: none; }
```

- [ ] **Step 2:** Confirm `--rule-soft` is defined (Pass 1 adds it). If not present, file a foundation-pass bug; the collection-row hairline depends on the soft variant for inner divider distinction from the surrounding `--rule` borders.

---

## Task 3: Add `.row.section-header` variant for nested groups

**Why:** `LaunchConfigControls.render()` currently emits `<fieldset><legend>` groupings; flat `settings-table` destroys this. The known-issues section of the responsive spec resolves this by permitting a row variant whose `<dd>` spans both columns and renders the fieldset header.

**Files:**
- Modify: `cmd/serf-hub/assets/style.css`

- [ ] **Step 1:** Append immediately after the value-cell variants:

```css
/* Section-header row — nested group header inside a settings-table.
   Used by LaunchConfigControls to render fieldset legends in the flat
   row primitive (design-language known-issues resolution, Pass 7). */
.settings-table .row.section-header {
  grid-template-columns: 1fr;
  padding: var(--space-5) 0 var(--space-2);
  border-bottom: none;
}
.settings-table .row.section-header dt { display: none; }
.settings-table .row.section-header dd {
  grid-column: 1 / -1;
  font-family: var(--font-mono);
  font-size: var(--text-2xs);
  font-weight: 500;
  letter-spacing: 0.14em;
  text-transform: uppercase;
  color: var(--text-muted);
}
.settings-table .row.section-header + .row { border-top: 1px solid var(--rule); }
```

- [ ] **Step 2:** Verify the cascade: a `.row.section-header` row sits **above** the next regular `.row`. The next regular row paints its own `border-bottom`; the explicit `border-top` rule restores the top hairline that would otherwise be eaten by the unbordered section-header. Document this in a 1-line comment alongside the rule.

---

## Task 4: Migrate `general.html` (read-only definition list)

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings/general.html`

- [ ] **Step 1:** Replace the entire file content with:

```html
{{define "settings-content"}}
<h2 class="settings-h2">General</h2>
<dl class="settings-table">
  <div class="row">
    <dt>Hub address</dt>
    <dd>{{.HubAddr}}</dd>
  </div>
  <div class="row">
    <dt>Run dir</dt>
    <dd>{{.RunDir}}</dd>
  </div>
  <div class="row">
    <dt>State dir</dt>
    <dd>{{.StateDir}}</dd>
  </div>
  <div class="row">
    <dt>Spawn timeout</dt>
    <dd>{{.SpawnTimeout}}</dd>
  </div>
  <div class="row">
    <dt>Past results per page</dt>
    <dd>{{.PastPerPage}}</dd>
  </div>
  <div class="row">
    <dt>Hub config</dt>
    <dd>~/.serf/hub.toml <span class="val-text"><span class="dim">edit to change</span></span></dd>
  </div>
</dl>
{{end}}
```

- [ ] **Step 2:** Remove the `<dl class="settings-list">` / `<dt>` / `<dd>` literal `<code>` wrapping — mono comes from `.settings-table dd` automatically. The footer `<p class="settings-help">` collapses into the last row's `.val-text .dim` annotation. Build the hub and load `/settings/general` in both themes.

---

## Task 5: Migrate `hub.html` (read-only definition list)

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings/hub.html`

- [ ] **Step 1:** Replace the entire file content with:

```html
{{define "settings-content"}}
<h2 class="settings-h2">Hub</h2>
<dl class="settings-table">
  <div class="row">
    <dt>Listen address</dt>
    <dd>{{.HubAddr}}</dd>
  </div>
  <div class="row">
    <dt>Run dir</dt>
    <dd>{{.RunDir}}</dd>
  </div>
  <div class="row">
    <dt>Spawn timeout</dt>
    <dd>{{.SpawnTimeout}}</dd>
  </div>
</dl>
{{end}}
```

---

## Task 6: Migrate `storage.html` (read-only definition list)

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings/storage.html`

- [ ] **Step 1:** Replace the entire file content with:

```html
{{define "settings-content"}}
<h2 class="settings-h2">Storage</h2>
<dl class="settings-table">
  <div class="row">
    <dt>State dir</dt>
    <dd>{{.StateDir}}</dd>
  </div>
  <div class="row">
    <dt>Run dir</dt>
    <dd>{{.RunDir}}</dd>
  </div>
  <div class="row">
    <dt>Hub config</dt>
    <dd>~/.serf/hub.toml</dd>
  </div>
  <div class="row">
    <dt>Past index</dt>
    <dd>{{.PastCount}} session{{if ne .PastCount 1}}s{{end}}</dd>
  </div>
</dl>
{{end}}
```

---

## Task 7: Migrate `agents.html` (row-per-agent)

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings/agents.html`

The agents page enumerates agents at runtime — each agent gets its own row in the table. `<dt>` is the agent name; `<dd>` is either a link to the agent's source file or a "built-in" annotation.

- [ ] **Step 1:** Replace the entire file content with:

```html
{{define "settings-content"}}
<h2 class="settings-h2">Agents</h2>
{{if .Agents}}
<dl class="settings-table">
  {{range .Agents}}
  <div class="row">
    <dt>{{.Name}}</dt>
    <dd>
      {{if .EditPath}}<a class="settings-open-link" href="{{.EditPath}}" target="_blank" rel="noopener">open in editor ↗</a>
      {{else}}<span class="val-text"><span class="dim">built-in</span></span>{{end}}
    </dd>
  </div>
  {{end}}
</dl>
{{else}}
<p class="settings-help">No agents discovered.</p>
{{end}}
{{end}}
```

- [ ] **Step 2:** Keep `.settings-open-link` as-is — it's a link, not a button. Pass 4's `:focus-visible` rule covers it. If `.settings-open-link` styling is gone after Pass 4 (it might have been folded into a generic `a` rule), no change needed; otherwise it still resolves to a muted-blue link in both themes.

---

## Task 8: Migrate `theme.html` (radios + new phone-density + sidebar-mode rows)

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings/theme.html`
- Modify: `cmd/serf-hub/assets/settings.js` (wire `data-phone-density` + sidebar-mode handlers)

The theme page picks up two new rows from §1.7 (Phone density) and §2.1 / Pass 5 (Sidebar mode). Both are radios, both are persisted to localStorage and applied as body data-attributes.

- [ ] **Step 1:** Replace the entire `theme.html` content with:

```html
{{define "settings-content"}}
<h2 class="settings-h2">Theme</h2>
<p class="settings-help">Theme, density, and sidebar mode are saved per-browser.</p>
<dl class="settings-table" data-theme-form>
  <div class="row">
    <dt>Color theme</dt>
    <dd>
      <div class="val-radio-group" data-theme-picker>
        <label class="val-radio"><input type="radio" name="theme" value="system"> System</label>
        <label class="val-radio"><input type="radio" name="theme" value="dark"> Dark</label>
        <label class="val-radio"><input type="radio" name="theme" value="light"> Light</label>
      </div>
    </dd>
    <p class="help">Both palettes ship; default follows your OS preference.</p>
  </div>
  <div class="row">
    <dt>Phone density</dt>
    <dd>
      <div class="val-radio-group" data-phone-density-picker>
        <label class="val-radio"><input type="radio" name="phone-density" value="compact"> Compact</label>
        <label class="val-radio"><input type="radio" name="phone-density" value="comfortable"> Comfortable</label>
      </div>
    </dd>
    <p class="help">Type-scale variant on phone (≤767px). Compact is the default.</p>
  </div>
  <div class="row">
    <dt>Sidebar mode</dt>
    <dd>
      <div class="val-radio-group" data-sidebar-mode-picker>
        <label class="val-radio"><input type="radio" name="sidebar-mode" value="pane"> Pane</label>
        <label class="val-radio"><input type="radio" name="sidebar-mode" value="rail"> Rail</label>
      </div>
    </dd>
    <p class="help">Desktop only. Rail mode collapses the sidebar to 56px (icons + dots). <code>⌘B</code> toggles.</p>
  </div>
</dl>
{{end}}
```

- [ ] **Step 2:** In `cmd/serf-hub/assets/settings.js`, locate the `data-theme-picker` initializer (it reads `localStorage["serf-hub.theme"]` and applies to `document.documentElement.dataset.theme`). Add two parallel handlers below it:

```js
// Phone density
(function () {
  const picker = document.querySelector("[data-phone-density-picker]");
  if (!picker) return;
  const KEY = "serf-hub.phone-density";
  const stored = localStorage.getItem(KEY) || "compact";
  const current = picker.querySelector(`input[value="${stored}"]`);
  if (current) current.checked = true;
  document.body.dataset.phoneDensity = stored;
  picker.addEventListener("change", (e) => {
    if (e.target.matches("input[type=radio]")) {
      const v = e.target.value;
      localStorage.setItem(KEY, v);
      document.body.dataset.phoneDensity = v;
    }
  });
})();

// Sidebar mode (pane / rail) — desktop only
(function () {
  const picker = document.querySelector("[data-sidebar-mode-picker]");
  if (!picker) return;
  const KEY = "serf-hub.sidebar.rail";
  const stored = localStorage.getItem(KEY) === "true" ? "rail" : "pane";
  const current = picker.querySelector(`input[value="${stored}"]`);
  if (current) current.checked = true;
  picker.addEventListener("change", (e) => {
    if (e.target.matches("input[type=radio]")) {
      const rail = e.target.value === "rail";
      localStorage.setItem(KEY, String(rail));
      if (rail) document.body.dataset.sidebarRail = "";
      else delete document.body.dataset.sidebarRail;
    }
  });
})();
```

- [ ] **Step 3:** Confirm the existing `data-theme-picker` handler reads radios correctly. It does today (`<input type="radio" name="theme">`); the new markup uses the same shape inside `<label class="val-radio">`, so no changes needed beyond the wrap.

---

## Task 9: Migrate `notifications.html` (toggles)

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings/notifications.html`

- [ ] **Step 1:** Replace the entire file content with:

```html
{{define "settings-content"}}
<h2 class="settings-h2">Notifications</h2>
<p class="settings-help">All notifications default off. Saved per-browser.</p>
<dl class="settings-table" data-notif-form>
  <div class="row editable">
    <dt>Title bar count</dt>
    <dd>
      <label class="val-toggle">
        <input type="checkbox" data-notif="title">
        <span class="state">OFF</span>
      </label>
    </dd>
    <p class="help">Show the count of awaiting sessions in the browser tab title.</p>
  </div>
  <div class="row editable">
    <dt>Favicon dot</dt>
    <dd>
      <label class="val-toggle">
        <input type="checkbox" data-notif="favicon">
        <span class="state">OFF</span>
      </label>
    </dd>
    <p class="help">Tint the favicon with the highest-attention session state.</p>
  </div>
  <div class="row editable">
    <dt>OS notification</dt>
    <dd>
      <label class="val-toggle">
        <input type="checkbox" data-notif="os">
        <span class="state">OFF</span>
      </label>
    </dd>
    <p class="help">Native notification on idle → awaiting and processing → errored.</p>
  </div>
  <div class="row editable">
    <dt>Sound</dt>
    <dd>
      <label class="val-toggle">
        <input type="checkbox" data-notif="sound">
        <span class="state">OFF</span>
      </label>
    </dd>
    <p class="help">Short tone on the same transitions.</p>
  </div>
</dl>
{{end}}
```

- [ ] **Step 2:** In `cmd/serf-hub/assets/settings.js`, find the notifications form handler (it queries `[data-notif-form] input[data-notif]`). Extend it so that on change it updates the sibling `.state` span text:

```js
function syncToggleState(input) {
  const span = input.parentElement.querySelector(".state");
  if (span) span.textContent = input.checked ? "ON" : "OFF";
}
```

Call `syncToggleState(input)` on initial load (after restoring stored value) and on every change event. This is purely cosmetic — the `data-notif` semantics don't change.

---

## Task 10: Migrate `providers.html` (status badges)

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings/providers.html`

The page is rendered client-side from `launchconfig.authList()`. The script emits rows; replace the loop's HTML with `settings-table .row` markup, swapping `.status-pill` for `.status-badge` (the Pass 4 token-driven typographic badge — no background).

- [ ] **Step 1:** Replace the entire file content with:

```html
{{define "settings-content"}}
<h2 class="settings-h2">Providers</h2>
<p class="settings-help">
  Credentials for each model provider. Edit on the
  <a href="/credentials" hx-get="/_partials/credentials" hx-target="#workspace" hx-push-url="/credentials">credentials</a>
  page.
</p>
<dl id="providers-rows" class="settings-table" data-loaded="false">
  <div class="row"><dt>Loading…</dt><dd></dd></div>
</dl>
<script>
  (async function () {
    const rows = document.getElementById("providers-rows");
    function escapeHtml(s) { return String(s).replace(/[&<>"]/g, c => ({"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;"}[c])); }
    function badgeState(source) {
      // map activeSource → data-state for the typographic badge
      switch (source) {
        case "file": case "oauth": case "env": return "idle";
        case "absent": return "ended";
        case "none": return "ended";
        case "error": case "missing": return "awaiting";
        case "unreachable": return "warning";
        default: return "ended";
      }
    }
    try {
      const data = await launchconfig.authList();
      rows.innerHTML = (data.providers || []).map(p => `
        <div class="row">
          <dt>${escapeHtml(p.provider)}</dt>
          <dd>
            <span class="status-badge" data-state="${badgeState(p.activeSource)}">
              <span class="status-dot" data-state="${badgeState(p.activeSource)}"></span>
              ${escapeHtml(p.activeSource)}
            </span>
            <span class="val-text"><span class="dim">${(p.authModes || []).join(" · ") || "—"}</span></span>
          </dd>
        </div>
      `).join("");
      rows.dataset.loaded = "true";
    } catch (err) {
      rows.innerHTML = `<div class="row"><dt>Error</dt><dd><span class="val-text">Failed to load: ${escapeHtml(err && err.message ? err.message : String(err))}</span></dd></div>`;
    }
  })();
</script>
{{end}}
```

- [ ] **Step 2:** The status badge uses Pass 4's typographic `.status-badge` (no background, mono small-caps in state color). The status dot inside the badge inherits state color from Pass 4 rules. No new CSS needed.

---

## Task 11: Migrate `launch-codex.html` (read-only display)

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings/launch-codex.html`

The Codex launch page is read-only — one `<dl class="settings-table">` per configured launch entry, preceded by an `<h3>` for the entry's ID. The legacy `<table>` markup is dropped.

- [ ] **Step 1:** Replace the entire file content with:

```html
{{define "settings-content"}}
<h2 class="settings-h2">Codex launch config</h2>
<p class="settings-help">
  Codex launch entries are defined in <code>hub.toml</code> under
  <code>[[codex_launches]]</code>. This page shows the current configuration
  (read-only).
</p>
{{if .CodexLaunches}}
  {{range .CodexLaunches}}
  <h3 class="settings-h3">{{.ID}}</h3>
  <dl class="settings-table">
    <div class="row">
      <dt>Binary</dt>
      <dd>{{if .Binary}}{{.Binary}}{{else}}codex{{end}}</dd>
    </div>
    <div class="row">
      <dt>Working dir</dt>
      <dd>{{if .WorkingDir}}{{.WorkingDir}}{{else}}<span class="val-text"><span class="dim">(inherited)</span></span>{{end}}</dd>
    </div>
    <div class="row">
      <dt>Listen</dt>
      <dd>{{if .Listen}}{{.Listen}}{{else}}ws://127.0.0.1:0{{end}}</dd>
    </div>
    <div class="row">
      <dt>Timeout</dt>
      <dd>{{if .Timeout}}{{.Timeout}}{{else}}30s{{end}}</dd>
    </div>
    {{if .Env}}
    <div class="row">
      <dt>Env</dt>
      <dd>
        {{range $k, $v := .Env}}<span class="val-text">{{$k}}=…</span> {{end}}
      </dd>
    </div>
    {{end}}
  </dl>
  {{end}}
{{else}}
<div class="settings-empty">
  <p>No codex launch entries configured.</p>
  <p class="settings-help">
    To add one, edit <code>hub.toml</code> and add a
    <code>[[codex_launches]]</code> section, for example:
  </p>
  <pre class="settings-pre">[[codex_launches]]
id          = "my-codex"
binary      = "/usr/local/bin/codex"
working_dir = "/path/to/project"
listen      = "ws://127.0.0.1:9190"
timeout     = "30s"</pre>
  <p class="settings-help">Restart the hub after editing hub.toml.</p>
</div>
{{end}}
{{end}}
```

- [ ] **Step 2:** Confirm `.settings-empty`, `.settings-pre`, and `.settings-h3` rules still exist in `style.css` (they're shared with other pages and stay during the pass). If `.settings-pre` is gone, add a minimal fallback rule alongside the new primitives. Do not remove the legacy `.settings-card` rule from the CSS in this pass — Pass 8 sweeps.

---

## Task 12: Update `LaunchConfigControls.render()` to emit settings-table row markup

**Files:**
- Modify: `cmd/serf-hub/assets/launchconfig.js`
- Modify: `cmd/serf-hub/jstest/test-launchconfig-controls.js`

**Mandate:** The JS contract (`render(form, opts)`, `populate(form, current)`, `collect(form)`, `validate(form)`, `showBackendError(form, err)`) stays intact. The `data-launch-*` attribute names stay intact. The internal class names (`spawn-advanced-*`, `settings-launch-*`) change to settings-table primitive markup when `mode === "settings"`. Spawn mode (`mode === "spawn"`) keeps its current class names — Pass 6 already finished the composer/spawn pass, so we do not touch spawn output here.

The migration is bounded: only the `"settings"` branch of `controlClass()` and the `"settings"` branches of all the title/wrap/textarea class assignments switch to settings-table markup. The `"spawn"` branches are unchanged.

- [ ] **Step 1:** Walk through every site in `launchconfig.js` that branches on `ctx.mode === "spawn"` and emits a `settings-launch-*` className. Each one becomes a settings-table primitive instead. The full set of changes:

  - Line 70 `controlClass()`: returns `"settings-table-" + name` when mode is settings (or, simpler, the function only matters for spawn mode now — refactor calls below to bypass it in settings).
  - Line 75 `addControlsWrap()`: in settings mode, wrap is `<div class="settings-collection-add">` instead of `settings-launch-add-controls settings-add-row`.
  - Line 133 textarea `className`: `"val-input"` in settings mode.
  - Line 153 model picker wrap: keep `"sp-model-wrap"` (picker widgets stay as their own classes — that's the "picker variant deferral" decision from the known-issues). Drop the `settings-launch-model` half.
  - Line 192 envFallback hint: only emitted when `ctx.envFallbacks` is set, which `mode === "settings"` never sets (`includeEnvFallbacks: false` in both launch-serf.html and project.html). Leave this branch alone.
  - Line 227, 387, 463, 513 field-label `className`: `"settings-table-field-label"` (a new utility class — see Step 3 below) in settings mode.
  - Line 383, 460, 510 list-control wrap: `"settings-collection"` in settings mode (with the wrap now being a `<section>`-shaped container, but as a `<div>` for simplicity — semantics-only difference).
  - Line 390, 466, 516 list `<ul>` `className`: `"settings-collection-list"` in settings mode.
  - Line 572 row `className`: `"row"` in settings mode (the `<dl class="settings-table">` wrapper is the form root).
  - Line 614 fieldset `className`: keep `"settings-launch-group"` for the spawn variant only; in settings mode, **do not emit a fieldset**. Instead emit a `.row.section-header` row immediately followed by the group's rows.

- [ ] **Step 2:** Rewrite `renderSchemaOption(group, opt, ctx)` so that in settings mode, each scalar / select / radio option emits:

```html
<div class="row" data-launch-option="<field>">
  <dt>{{ opt.label }}</dt>
  <dd> ... control ... </dd>
  <p class="help" hidden></p>
</div>
```

The `<dt>` carries the label text (sans, weight 500); the `<dd>` carries the input (`.val-input`, `.val-select`, `.val-radio-group`, or `.val-toggle`). The `<p class="help">` slot is reserved for `opt.helpText` (if present in schema) or for `[data-launch-validation-error]` to live in (re-purposed from today's `<div data-launch-validation-error class="settings-error">`).

- [ ] **Step 3:** Replace `renderScalarControl`'s `<label>` wrapping with a settings-table cell:
  - Replace the outer `<label>` (which today wraps both label text and the control) with two siblings: a `<dt>` for the label text and a `<dd>` containing the control. The control gets `class="val-input"` (text/number), `class="val-select"` (select / boolean dropdown), `class="val-radio-group"` (radio set), or `class="sp-model-wrap"` (modelPicker — unchanged from today).
  - For the `radio` kind, the wrap `<div class="settings-launch-radio">` becomes `<div class="val-radio-group">`; each option `<label>` gets `class="val-radio"` and contains `<input type="radio">` + label text.
  - For the `boolean` kind, the `<select>` gets `class="val-select"`.
  - For text-input kinds, the `<input>` gets `class="val-input"`. For multiline (`opt.kind === "multilineText"`), the `<textarea>` gets `class="val-input"` (the textarea CSS rule in Task 1 sets min-height + line-height).

- [ ] **Step 4:** Replace `renderPromptCompositeControl` similarly. The composite is a `<dd>` containing a `<div class="val-radio-group launch-radio-composite">` wrapping the radio options. The "use default" / "from file" / "inline text" options each become a `<label class="val-radio launch-radio-option">` with the appropriate `<input>` + nested `<input class="val-input">` or `<textarea class="val-input">`.

- [ ] **Step 5:** Replace `renderListControl` / `renderEnvControl` / `renderMCPControl` so each one emits a section-header row plus a `settings-collection`-shaped block:

```html
<!-- list-control settings shape -->
<div class="row section-header">
  <dt></dt>
  <dd>{{ opt.label }}</dd>
</div>
<div class="row" data-launch-option="<field>">
  <dt></dt>
  <dd>
    <div class="settings-collection" data-launch-wire-field="<wire>" data-launch-kind="<kind>" data-launch-path-kind="<path>">
      <ul class="settings-collection-list" data-launch-list role="list"></ul>
      <form class="settings-collection-add">
        <input class="val-input" type="text" ...>
        <button class="btn btn-secondary" type="button">Add</button>
        <p class="row-error" data-launch-validation-error hidden></p>
      </form>
      <label class="val-toggle" data-launch-explicit-empty-wrap hidden>
        <input type="checkbox" data-launch-explicit-empty>
        <span class="state">OFF</span>
        <span>No model fallbacks</span>
      </label>
    </div>
  </dd>
</div>
```

  The `data-launch-wire-field`, `data-launch-kind`, `data-launch-path-kind`, `data-launch-list`, `data-launch-env-list`, `data-launch-mcp-list`, `data-launch-explicit-empty`, `data-launch-validation-error`, `data-launch-invalid`, `data-launch-mcp-command`, `data-launch-option` attributes ALL stay — internal validate/collect functions read them. The classes the validate/collect functions read are `.spawn-advanced-list-control` (line 646, 730, 786): rename internally to `.settings-collection[data-launch-wire-field]` in settings mode while keeping the spawn class for spawn mode. Concretely: change `root.querySelectorAll(".spawn-advanced-list-control[data-launch-wire-field]")` to `root.querySelectorAll(".spawn-advanced-list-control[data-launch-wire-field], .settings-collection[data-launch-wire-field]")` in three places.

- [ ] **Step 6:** The remove `<button>` inside each list `<li>` becomes `<button class="btn-icon" type="button" aria-label="Remove">×</button>` in settings mode (text-only on spawn-mode `<li>` preserved). Each settings-mode `<li>` carries `class="settings-collection-row"`. The `appendListRow`, `appendMCPRow`, env-row append helpers each take an extra `mode` parameter (or branch on the list's class) to choose between the two markup shapes.

- [ ] **Step 7:** Add a top-level utility selector — replace any `font-weight: 500; color: var(--text)` on field labels with the existing `.row dt` rule (cascade handles it). For the `.row.section-header` + immediate-following `.row` case where the section-header `<dt>` is empty, the section header text lives in the `<dd>`.

- [ ] **Step 8:** Update `cmd/serf-hub/jstest/test-launchconfig-controls.js` lines 254 and 255:

```js
const pendingPath = pluginWrap.querySelector(".settings-collection-add input");
const addPath = pluginWrap.querySelector(".settings-collection-add button");
```

  These are the only two literal class-name references in the test file. The rest of the test uses `data-launch-*` attribute selectors, which are preserved.

- [ ] **Step 9:** Run `cmd/serf-hub/jstest/run-all.sh` (or the equivalent harness). All launchconfig-controls assertions must still pass: schema render coverage, prompt composite radio behavior, modelFallbacks explicit-empty checkbox, path-list validation, env-credential validation, spawn-mode exclusion of agent/model/reasoningEffort + explicit-empty. If any test fails, the cause is one of: a `data-launch-*` attribute was accidentally renamed; the wrap class branch was applied in spawn mode too; the explicit-empty checkbox got moved out of the `data-launch-wire-field` wrap.

- [ ] **Step 10:** Verify spawn mode renders identically pre- and post-pass. Open `/new` in the browser. The Advanced section should look exactly the same — confirm by comparing screenshots.

---

## Task 13: Migrate `launch-serf.html` (mixed inputs, schema-driven)

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings/launch-serf.html`

The page's JS body stays unchanged — it calls `launchconfig.schema()`, then `LaunchConfigControls.render(form, {mode: "settings", layer: "global", ...})`. Task 12 makes `render()` emit settings-table markup, so the only template change here is: the form root becomes a `<dl>` (semantics) wrapped with the form-action footer; the inline `style="margin:0"` is removed (deferred until Task 21).

- [ ] **Step 1:** Replace the entire file content with:

```html
{{define "settings-content"}}
<h2 class="settings-h2">Serf launch defaults</h2>
<p class="settings-help">
  These values are applied to every serf spawn unless overridden by a project
  layer or per-launch.
</p>
<form id="launch-form" class="settings-launch-form" data-loaded="false" data-launch-settings-root data-launch-settings-layer="global">
  <div data-launch-schema-loading class="settings-help">Loading launch settings…</div>
  <dl class="settings-table" data-launch-settings-groups></dl>
  <div class="form-actions">
    <button type="submit" class="btn btn-primary">Save launch defaults</button>
    <p id="launch-form-status" class="settings-form-status" aria-live="polite"></p>
  </div>
</form>
<script>
  (async function () {
    const form = document.getElementById("launch-form");
    const status = document.getElementById("launch-form-status");
    const loading = form.querySelector("[data-launch-schema-loading]");
    let statusClearTimer = 0;

    function setStatus(text) {
      if (statusClearTimer) {
        clearTimeout(statusClearTimer);
        statusClearTimer = 0;
      }
      status.textContent = text || "";
      if (text && !text.startsWith("Error:")) {
        statusClearTimer = setTimeout(() => {
          if (status.textContent === text) status.textContent = "";
          statusClearTimer = 0;
        }, 5000);
      }
    }

    try {
      const schema = await launchconfig.schema();
      const current = await launchconfig.getLayer("/", "global");
      if (!window.LaunchConfigControls) throw new Error("launch settings controls unavailable");
      window.LaunchConfigControls.render(form, {
        mode: "settings",
        layer: "global",
        options: (schema && schema.options) || [],
        current,
        includeEnvFallbacks: false,
      });
      if (loading) loading.hidden = true;
      form.dataset.loaded = "true";

      form.addEventListener("submit", async (e) => {
        e.preventDefault();
        if (!(await window.LaunchConfigControls.validate(form))) return;
        try {
          await launchconfig.setLayer("/", "global", window.LaunchConfigControls.collect(form));
          setStatus("Saved at " + new Date().toLocaleTimeString());
        } catch (err) {
          window.LaunchConfigControls.showBackendError(form, err);
          setStatus("Error: " + (err && err.message ? err.message : err));
        }
      });
    } catch (err) {
      if (loading) loading.textContent = "Failed to load launch settings.";
      setStatus("Error: " + (err && err.message ? err.message : err));
    }
  })();
</script>
{{end}}
```

- [ ] **Step 2:** Notes on the changes from today:
  - `<div data-launch-settings-groups>` → `<dl class="settings-table" data-launch-settings-groups>`. The `data-launch-settings-groups` attribute is preserved.
  - `<button type="submit">` → `<button type="submit" class="btn btn-primary">` (Pass 4 button variant).
  - `<p id="launch-form-status" class="settings-help" style="margin:0">` → `<p id="launch-form-status" class="settings-form-status" aria-live="polite">`. The inline `style="margin:0"` is removed and replaced by the `.settings-form-status` utility class (Task 21).
  - `aria-live="polite"` is added per design-language §6.2.

- [ ] **Step 3:** Add the `.settings-form-status` and `.form-actions` rules to `style.css` if they aren't already defined (Task 21 finalizes; for now, ensure they exist):

```css
.form-actions {
  margin-top: var(--space-4);
  display: flex;
  align-items: center;
  gap: var(--space-3);
}
.settings-form-status {
  margin: 0;
  font-family: var(--font-sans);
  font-size: var(--text-xs);
  color: var(--text-muted);
}
```

---

## Task 14: Migrate `project.html` (mixed inputs, schema-driven)

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings/project.html`

Same shape as launch-serf.html — schema-driven form via `LaunchConfigControls.render()` plus a project picker fallback when no CWD is selected.

- [ ] **Step 1:** Replace the entire file content with:

```html
{{define "settings-content"}}
<h2 class="settings-h2">Project settings</h2>
{{if .ProjectCWD}}
<p class="settings-help">{{.ProjectCWD}}</p>
<div id="project-settings-root" data-cwd="{{.ProjectCWD}}" data-loaded="false" hx-disable hx-disinherit="*">
  <h3 class="settings-h3">Launch defaults</h3>
  <form id="proj-launch-form" class="settings-launch-form" data-launch-settings-root data-launch-settings-layer="project">
    <div data-launch-schema-loading class="settings-help">Loading project launch settings…</div>
    <dl class="settings-table" data-launch-settings-groups></dl>
    <div class="form-actions">
      <button type="submit" id="proj-launch-save" class="btn btn-primary">Save launch defaults</button>
      <p id="proj-status" class="settings-form-status" aria-live="polite"></p>
    </div>
  </form>
</div>
<script>
  (async function () {
    const root = document.getElementById("project-settings-root");
    const cwd = root.dataset.cwd;
    const form = document.getElementById("proj-launch-form");
    const status = document.getElementById("proj-status");
    const loading = form.querySelector("[data-launch-schema-loading]");
    let statusClearTimer = 0;

    function setStatus(text) {
      if (statusClearTimer) {
        clearTimeout(statusClearTimer);
        statusClearTimer = 0;
      }
      status.textContent = text || "";
      if (text && !text.startsWith("Error:")) {
        statusClearTimer = setTimeout(() => {
          if (status.textContent === text) status.textContent = "";
          statusClearTimer = 0;
        }, 5000);
      }
    }

    try {
      const schema = await launchconfig.schema();
      const current = await launchconfig.getLayer(cwd, "project");
      if (!window.LaunchConfigControls) throw new Error("launch settings controls unavailable");
      window.LaunchConfigControls.render(form, {
        mode: "settings",
        layer: "project",
        options: (schema && schema.options) || [],
        current,
        includeEnvFallbacks: false,
      });
      if (loading) loading.hidden = true;
      root.dataset.loaded = "true";

      form.addEventListener("submit", async (e) => {
        e.preventDefault();
        if (!(await window.LaunchConfigControls.validate(form))) return;
        try {
          await launchconfig.setLayer(cwd, "project", window.LaunchConfigControls.collect(form));
          setStatus("Saved at " + new Date().toLocaleTimeString());
        } catch (err) {
          window.LaunchConfigControls.showBackendError(form, err);
          setStatus("Error: " + (err && err.message ? err.message : err));
        }
      });
    } catch (err) {
      if (loading) loading.textContent = "Failed to load project launch settings.";
      const message = "Error: " + (err && err.message ? err.message : err);
      status.textContent = message;
      status.replaceChildren(document.createTextNode(message));
      root.dataset.loaded = "error";
    }
  })();
</script>
{{else}}
<p class="settings-help">No project selected. Open this page via the ⚙ icon next to a project in the sidebar, or choose one below.</p>
{{if .AvailableProjects}}
<ul class="settings-project-list" role="list">
  {{range .AvailableProjects}}
  <li class="settings-project-list-item">
    <a href="/settings/project?cwd={{.CWD | urlquery}}" hx-get="/_partials/settings/project?cwd={{.CWD | urlquery}}" hx-target="#settings-content" hx-push-url="/settings/project?cwd={{.CWD | urlquery}}">
      <span class="settings-project-name">{{.Name}}</span>
      <span class="settings-project-cwd">{{.CWD}}</span>
    </a>
  </li>
  {{end}}
</ul>
{{else}}
<p class="settings-help">No known projects yet. Spawn a session to register a project.</p>
{{end}}
{{end}}
{{end}}
```

- [ ] **Step 2:** Notes on changes from today:
  - The header `<p class="settings-help"><code>{{.ProjectCWD}}</code></p>` drops the `<code>` — mono comes from `.settings-help` if needed via a typography rule, otherwise the CWD displays in sans (the help text is sans by design). If the CWD specifically needs mono treatment in this header, wrap it in `<span class="val-text">{{.ProjectCWD}}</span>` to inherit mono. Decide during implementation; default is no `<code>`.
  - Form root unchanged in structure but the inner `<div data-launch-settings-groups>` → `<dl class="settings-table" data-launch-settings-groups>`.
  - `<button type="submit" id="proj-launch-save">` gains `class="btn btn-primary"`.
  - The status `<p>` gains `class="settings-form-status" aria-live="polite"` and **drops `style="margin:0"`** (Task 21).
  - `.settings-project-list` and `.settings-project-list-item` rules stay as-is — they're a presentational link-list, not a settings primitive.

---

## Task 15: Migrate `inrepo.html` (settings-table + custom trust controls)

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings/inrepo.html`

In-repo trust is the documented one-off. The page consists of:
1. A working-dir `<input>` (rendered in a `settings-table` `.row.editable`).
2. A status panel that renders dynamically based on the resolve result.

The custom trust UI (preview block + trust button + status messaging) is kept as-is but adopts Pass 4 button + typography. The preview pre block becomes `surface-inset`.

- [ ] **Step 1:** Replace the entire file content with:

```html
{{define "settings-content"}}
<h2 class="settings-h2">In-repo config (.serf/launch.toml)</h2>
<p class="settings-help">
  Per-project launch config shipped inside the working directory. Hub
  only applies it after you confirm trust.
</p>
<dl class="settings-table">
  <div class="row editable">
    <dt>Working dir</dt>
    <dd><input type="text" id="inrepo-cwd" class="val-input" placeholder="/path/to/project"></dd>
    <p class="help">Enter an absolute path. The hub reads <code>.serf/launch.toml</code> from this directory.</p>
  </div>
</dl>
<div id="inrepo-status" class="inrepo-status" aria-live="polite">
  <p class="settings-help">Enter a working directory above.</p>
</div>
<script>
  (async function () {
    const cwdInput = document.getElementById("inrepo-cwd");
    const status = document.getElementById("inrepo-status");
    cwdInput.value = localStorage.getItem("lastCwd") || "";
    function escapeHtml(s) { return s.replace(/[&<>"]/g, c => ({"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;"}[c])); }
    async function refresh() {
      const cwd = cwdInput.value.trim();
      if (!cwd) { status.innerHTML = '<p class="settings-help">Enter a working directory.</p>'; return; }
      let r;
      try {
        r = await launchconfig.resolve(cwd);
      } catch (err) {
        const p = document.createElement("p");
        p.className = "settings-error";
        p.textContent = "Failed to load: " + (err && err.message ? err.message : err);
        status.replaceChildren(p);
        return;
      }
      const repo = r.repo || { trust: "absent" };
      if (repo.trust === "absent") {
        status.innerHTML = `<p class="settings-help">No <code>.serf/launch.toml</code> in <code>${escapeHtml(cwd)}</code>.</p>`;
        return;
      }
      const preview = repo.preview ? `<pre class="surface-inset inrepo-preview">${escapeHtml(repo.preview)}</pre>` : "";
      const noteByTrust = {
        trusted:   `<p class="settings-help">Trusted. Hash <span class="val-text">${escapeHtml(repo.hash || "")}</span>.</p>`,
        untrusted: `<p class="settings-help">Untrusted — review and approve below.</p>`,
        changed:   `<p class="settings-help">Trusted before, but the file has changed. Review and approve again.</p>`,
        rejected:  `<p class="settings-help">Previously rejected. Trust to apply.</p>`,
      };
      const showApprove = repo.trust !== "trusted";
      status.innerHTML = `
        ${noteByTrust[repo.trust] || ""}
        ${preview}
        ${showApprove ? '<button type="button" id="approve" class="btn btn-primary">Trust this file</button>' : ""}
      `;
      if (showApprove) {
        document.getElementById("approve").addEventListener("click", async () => {
          try {
            await launchconfig.trustRepo(cwd, repo.hash);
            refresh();
          } catch (err) {
            const msg = err && err.message ? err.message : String(err);
            const errEl = document.createElement("p");
            errEl.className = "settings-help settings-error";
            errEl.textContent = "Trust failed: " + msg;
            status.appendChild(errEl);
          }
        });
      }
    }
    cwdInput.addEventListener("change", refresh);
    refresh();
  })();
</script>
{{end}}
```

- [ ] **Step 2:** Add the `.inrepo-preview` + `.surface-inset` rule to `style.css` if not already present (Pass 2/3 may have defined `.surface-inset` as the inset variant):

```css
.inrepo-preview {
  margin: var(--space-3) 0;
  padding: var(--space-3) var(--space-4);
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  color: var(--text);
  white-space: pre;
  overflow-x: auto;
  border-radius: var(--radius-md);
}
.inrepo-status { margin-top: var(--space-4); }
```

If `.surface-inset` is already defined (Pass 4 + Pass 6 should have done so), `inrepo-preview` only adds margin + scroll; the surface variant handles background and radius.

- [ ] **Step 3:** Confirm the one-off documentation: in-repo is **the only sub-page that does not fully consume `settings-table`** — the trust-approval block is custom. The CWD input row sits in a one-row `settings-table` for visual consistency.

---

## Task 16: Migrate `plugins.html` (settings-collection)

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings/plugins.html`

- [ ] **Step 1:** Replace the entire file content with:

```html
{{define "settings-content"}}
<h2 class="settings-h2">Plugin directories</h2>
<p class="settings-help">
  Directories serf scans for plugins at launch. Applied to every spawn.
</p>
<div id="plugins-form" data-loaded="false">
  <p class="settings-help">Loading…</p>
</div>
<script>
  (async function () {
    const root = document.getElementById("plugins-form");
    function escapeHtml(s) { return String(s).replace(/[&<>"]/g, c => ({"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;"}[c])); }
    try {
      const current = await launchconfig.getLayer("/", "global");
      const dirs = current.pluginDirs || [];
      function render() {
        root.replaceChildren();
        const section = document.createElement("section");
        section.className = "settings-collection";

        const head = document.createElement("header");
        head.className = "settings-collection-head";
        const h3 = document.createElement("h3");
        h3.textContent = "Plugin directories";
        const count = document.createElement("span");
        count.className = "settings-collection-count";
        count.textContent = `${dirs.length} ${dirs.length === 1 ? "entry" : "entries"}`;
        head.append(h3, count);

        const list = document.createElement("ul");
        list.className = "settings-collection-list";
        list.setAttribute("role", "list");
        if (dirs.length === 0) {
          const empty = document.createElement("li");
          empty.className = "settings-collection-empty";
          empty.textContent = "No plugin directories. Add one below.";
          list.appendChild(empty);
        } else {
          dirs.forEach((d, i) => {
            const item = document.createElement("li");
            item.className = "settings-collection-row";
            const main = document.createElement("div");
            const text = document.createElement("div");
            text.className = "row-text";
            text.textContent = d;
            main.appendChild(text);
            const actions = document.createElement("div");
            actions.className = "row-actions";
            const button = document.createElement("button");
            button.type = "button";
            button.className = "btn-icon";
            button.dataset.i = String(i);
            button.dataset.action = "rm";
            button.setAttribute("aria-label", "Remove " + d);
            button.textContent = "×";
            actions.appendChild(button);
            item.append(main, actions);
            list.appendChild(item);
          });
        }

        const form = document.createElement("form");
        form.id = "plugins-add";
        form.className = "settings-collection-add sp-dir-wrap";
        const input = document.createElement("input");
        input.type = "text";
        input.name = "dir";
        input.className = "val-input";
        input.placeholder = "/absolute/path";
        input.required = true;
        input.setAttribute("data-settings-dir-input", "");
        const pick = document.createElement("button");
        pick.type = "button";
        pick.className = "btn btn-secondary";
        pick.setAttribute("data-settings-dir-picker", "");
        pick.textContent = "Browse";
        const add = document.createElement("button");
        add.type = "submit";
        add.className = "btn btn-primary";
        add.textContent = "＋ Add";
        const error = document.createElement("p");
        error.className = "row-error";
        error.hidden = true;
        form.append(input, pick, add, error);
        section.append(head, list, form);
        root.append(section);

        if (window.SettingsPickers) window.SettingsPickers.init(form);
        root.querySelectorAll("button[data-action=rm]").forEach(b => {
          b.addEventListener("click", async () => {
            dirs.splice(+b.dataset.i, 1);
            await launchconfig.setLayer("/", "global", { ...current, pluginDirs: dirs });
            render();
          });
        });
        root.querySelector("#plugins-add").addEventListener("submit", async (e) => {
          e.preventDefault();
          const v = e.target.dir.value.trim();
          if (!v) return;
          const valid = await launchconfig.validatePath(v, "dir");
          if (!valid || !valid.valid) {
            error.textContent = (valid && valid.error) ? valid.error : "path does not exist";
            error.hidden = false;
            return;
          }
          error.hidden = true;
          dirs.push(valid.path || v);
          await launchconfig.setLayer("/", "global", { ...current, pluginDirs: dirs });
          e.target.dir.value = "";
          render();
        });
      }
      render();
      root.dataset.loaded = "true";
    } catch (err) {
      const p = document.createElement("p");
      p.className = "settings-error";
      p.textContent = "Failed to load: " + (err && err.message ? err.message : err);
      root.replaceChildren(p);
    }
  })();
</script>
{{end}}
```

- [ ] **Step 2:** Confirm the remove button is **persistent** — no hover-only opacity. The `.btn-icon` class from Pass 4 already enforces persistence. The "×" character renders cleanly at 14px in JetBrains Mono; if a more accessible glyph is needed swap to "✕" (U+2715) or a `<svg>` icon. Keep "×" for now.

- [ ] **Step 3:** The `data-settings-dir-input` and `data-settings-dir-picker` attributes are read by `assets/settings-pickers.js`. They are preserved verbatim.

---

## Task 17: Migrate `skills.html` (settings-collection)

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings/skills.html`

The skills page is structurally identical to plugins — only the field name (`skillsDirs`), the page title, and the help text differ.

- [ ] **Step 1:** Replace the entire file content with the same shape as plugins.html, swapping each occurrence:
  - "Plugin directories" → "Skill directories"
  - "plugin" → "skill"
  - `pluginDirs` → `skillsDirs`
  - `plugins-form` → `skills-form`
  - `plugins-add` → `skills-add`

```html
{{define "settings-content"}}
<h2 class="settings-h2">Skill directories</h2>
<p class="settings-help">
  Directories serf scans for skills at launch. Applied to every spawn.
</p>
<div id="skills-form" data-loaded="false">
  <p class="settings-help">Loading…</p>
</div>
<script>
  (async function () {
    const root = document.getElementById("skills-form");
    function escapeHtml(s) { return String(s).replace(/[&<>"]/g, c => ({"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;"}[c])); }
    try {
      const current = await launchconfig.getLayer("/", "global");
      const dirs = current.skillsDirs || [];
      function render() {
        root.replaceChildren();
        const section = document.createElement("section");
        section.className = "settings-collection";

        const head = document.createElement("header");
        head.className = "settings-collection-head";
        const h3 = document.createElement("h3");
        h3.textContent = "Skill directories";
        const count = document.createElement("span");
        count.className = "settings-collection-count";
        count.textContent = `${dirs.length} ${dirs.length === 1 ? "entry" : "entries"}`;
        head.append(h3, count);

        const list = document.createElement("ul");
        list.className = "settings-collection-list";
        list.setAttribute("role", "list");
        if (dirs.length === 0) {
          const empty = document.createElement("li");
          empty.className = "settings-collection-empty";
          empty.textContent = "No skill directories. Add one below.";
          list.appendChild(empty);
        } else {
          dirs.forEach((d, i) => {
            const item = document.createElement("li");
            item.className = "settings-collection-row";
            const main = document.createElement("div");
            const text = document.createElement("div");
            text.className = "row-text";
            text.textContent = d;
            main.appendChild(text);
            const actions = document.createElement("div");
            actions.className = "row-actions";
            const button = document.createElement("button");
            button.type = "button";
            button.className = "btn-icon";
            button.dataset.i = String(i);
            button.dataset.action = "rm";
            button.setAttribute("aria-label", "Remove " + d);
            button.textContent = "×";
            actions.appendChild(button);
            item.append(main, actions);
            list.appendChild(item);
          });
        }

        const form = document.createElement("form");
        form.id = "skills-add";
        form.className = "settings-collection-add sp-dir-wrap";
        const input = document.createElement("input");
        input.type = "text";
        input.name = "dir";
        input.className = "val-input";
        input.placeholder = "/absolute/path";
        input.required = true;
        input.setAttribute("data-settings-dir-input", "");
        const pick = document.createElement("button");
        pick.type = "button";
        pick.className = "btn btn-secondary";
        pick.setAttribute("data-settings-dir-picker", "");
        pick.textContent = "Browse";
        const add = document.createElement("button");
        add.type = "submit";
        add.className = "btn btn-primary";
        add.textContent = "＋ Add";
        const error = document.createElement("p");
        error.className = "row-error";
        error.hidden = true;
        form.append(input, pick, add, error);
        section.append(head, list, form);
        root.append(section);

        if (window.SettingsPickers) window.SettingsPickers.init(form);
        root.querySelectorAll("button[data-action=rm]").forEach(b => {
          b.addEventListener("click", async () => {
            dirs.splice(+b.dataset.i, 1);
            await launchconfig.setLayer("/", "global", { ...current, skillsDirs: dirs });
            render();
          });
        });
        root.querySelector("#skills-add").addEventListener("submit", async (e) => {
          e.preventDefault();
          const v = e.target.dir.value.trim();
          if (!v) return;
          const valid = await launchconfig.validatePath(v, "dir");
          if (!valid || !valid.valid) {
            error.textContent = (valid && valid.error) ? valid.error : "path does not exist";
            error.hidden = false;
            return;
          }
          error.hidden = true;
          dirs.push(valid.path || v);
          await launchconfig.setLayer("/", "global", { ...current, skillsDirs: dirs });
          e.target.dir.value = "";
          render();
        });
      }
      render();
      root.dataset.loaded = "true";
    } catch (err) {
      const p = document.createElement("p");
      p.className = "settings-error";
      p.textContent = "Failed to load: " + (err && err.message ? err.message : err);
      root.replaceChildren(p);
    }
  })();
</script>
{{end}}
```

---

## Task 18: Migrate `mcp.html` (two settings-collections)

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings/mcp.html`

The MCP page hosts two collections: "MCP config files" (paths) and "Inline MCP servers" (name + command + args).

- [ ] **Step 1:** Replace the entire file content with:

```html
{{define "settings-content"}}
<h2 class="settings-h2">MCP servers</h2>
<p class="settings-help">
  MCP servers serf spawns alongside each session. Stored in the global launch layer.
</p>
<div id="mcps-form" data-loaded="false"><p class="settings-help">Loading…</p></div>
<script>
  (async function () {
    const root = document.getElementById("mcps-form");
    function escapeHtml(s) { return String(s).replace(/[&<>"]/g, c => ({"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;"}[c])); }

    function makeCollectionShell(title, count) {
      const section = document.createElement("section");
      section.className = "settings-collection";
      const head = document.createElement("header");
      head.className = "settings-collection-head";
      const h3 = document.createElement("h3");
      h3.textContent = title;
      const countSpan = document.createElement("span");
      countSpan.className = "settings-collection-count";
      countSpan.textContent = `${count} ${count === 1 ? "entry" : "entries"}`;
      head.append(h3, countSpan);
      const list = document.createElement("ul");
      list.className = "settings-collection-list";
      list.setAttribute("role", "list");
      section.append(head, list);
      return { section, list };
    }

    function makeRemoveButton(label, onClick) {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "btn-icon";
      button.setAttribute("aria-label", label);
      button.textContent = "×";
      button.addEventListener("click", onClick);
      return button;
    }

    function emptyRow(text) {
      const li = document.createElement("li");
      li.className = "settings-collection-empty";
      li.textContent = text;
      return li;
    }

    try {
      const current = await launchconfig.getLayer("/", "global");
      const mcps = current.mcps || [];
      const mcpConfigs = current.mcpConfigs || [];

      function render() {
        root.replaceChildren();

        // --- MCP config files ---
        const { section: configsSection, list: configList } = makeCollectionShell("MCP config files", mcpConfigs.length);
        if (mcpConfigs.length === 0) {
          configList.appendChild(emptyRow("No MCP config files. Add one below."));
        } else {
          mcpConfigs.forEach((p, i) => {
            const item = document.createElement("li");
            item.className = "settings-collection-row";
            const main = document.createElement("div");
            const text = document.createElement("div");
            text.className = "row-text";
            text.textContent = p;
            main.appendChild(text);
            const actions = document.createElement("div");
            actions.className = "row-actions";
            actions.appendChild(makeRemoveButton("Remove " + p, async () => {
              mcpConfigs.splice(i, 1);
              await launchconfig.setLayer("/", "global", { ...current, mcpConfigs });
              render();
            }));
            item.append(main, actions);
            configList.appendChild(item);
          });
        }
        const configForm = document.createElement("form");
        configForm.id = "mcp-configs-add";
        configForm.className = "settings-collection-add";
        const configInput = document.createElement("input");
        configInput.type = "text";
        configInput.name = "path";
        configInput.className = "val-input";
        configInput.placeholder = "/absolute/path/to/mcp.json";
        configInput.required = true;
        configInput.setAttribute("data-settings-dir-input", "");
        const configAdd = document.createElement("button");
        configAdd.type = "submit";
        configAdd.className = "btn btn-primary";
        configAdd.textContent = "＋ Add";
        const configError = document.createElement("p");
        configError.className = "row-error";
        configError.hidden = true;
        configForm.append(configInput, configAdd, configError);
        configsSection.appendChild(configForm);
        root.append(configsSection);

        // --- Inline MCP servers ---
        const { section: inlineSection, list: inlineList } = makeCollectionShell("Inline MCP servers", mcps.length);
        if (mcps.length === 0) {
          inlineList.appendChild(emptyRow("No inline MCP servers. Add one below."));
        } else {
          mcps.forEach((m, i) => {
            const item = document.createElement("li");
            item.className = "settings-collection-row";
            const main = document.createElement("div");
            const text = document.createElement("div");
            text.className = "row-text";
            text.textContent = (m.name || "") + " → " + [m.command || "", ...((m.args || []))].filter(Boolean).join(" ");
            main.appendChild(text);
            const actions = document.createElement("div");
            actions.className = "row-actions";
            actions.appendChild(makeRemoveButton("Remove " + (m.name || ""), async () => {
              mcps.splice(i, 1);
              await launchconfig.setLayer("/", "global", { ...current, mcps });
              render();
            }));
            item.append(main, actions);
            inlineList.appendChild(item);
          });
        }
        const inlineForm = document.createElement("form");
        inlineForm.id = "mcps-add";
        inlineForm.className = "settings-collection-add";
        const nameInput = document.createElement("input");
        nameInput.type = "text";
        nameInput.name = "name";
        nameInput.className = "val-input";
        nameInput.placeholder = "name";
        nameInput.required = true;
        const commandInput = document.createElement("input");
        commandInput.type = "text";
        commandInput.name = "command";
        commandInput.className = "val-input";
        commandInput.placeholder = "command";
        commandInput.required = true;
        const argsInput = document.createElement("input");
        argsInput.type = "text";
        argsInput.name = "args";
        argsInput.className = "val-input";
        argsInput.placeholder = "args (space-separated)";
        const inlineAdd = document.createElement("button");
        inlineAdd.type = "submit";
        inlineAdd.className = "btn btn-primary";
        inlineAdd.textContent = "＋ Add";
        const inlineError = document.createElement("p");
        inlineError.className = "row-error";
        inlineError.hidden = true;
        inlineForm.append(nameInput, commandInput, argsInput, inlineAdd, inlineError);
        inlineSection.appendChild(inlineForm);
        root.append(inlineSection);

        if (window.SettingsPickers) window.SettingsPickers.init(configForm);

        configForm.addEventListener("submit", async (e) => {
          e.preventDefault();
          const v = e.target.path.value.trim();
          if (!v) return;
          const valid = await launchconfig.validatePath(v, "file");
          if (!valid || !valid.valid) {
            configError.textContent = (valid && valid.error) ? valid.error : "path does not exist";
            configError.hidden = false;
            return;
          }
          configError.hidden = true;
          mcpConfigs.push(valid.path || v);
          await launchconfig.setLayer("/", "global", { ...current, mcpConfigs });
          e.target.reset();
          render();
        });

        inlineForm.addEventListener("submit", async (e) => {
          e.preventDefault();
          const name = e.target.name.value.trim();
          const command = e.target.command.value.trim();
          const args = e.target.args.value.trim().split(/\s+/).filter(Boolean);
          const valid = await launchconfig.validatePath(command, "command");
          if (!valid || !valid.valid) {
            inlineError.textContent = (valid && valid.error) ? valid.error : "command not found";
            inlineError.hidden = false;
            return;
          }
          inlineError.hidden = true;
          mcps.push({ name, command, args });
          await launchconfig.setLayer("/", "global", { ...current, mcps });
          e.target.reset();
          render();
        });
      }
      render();
      root.dataset.loaded = "true";
    } catch (err) {
      const p = document.createElement("p");
      p.className = "settings-error";
      p.textContent = "Failed to load: " + (err && err.message ? err.message : err);
      root.replaceChildren(p);
    }
  })();
</script>
{{end}}
```

- [ ] **Step 2:** The two `settings-collection-add` forms wrap their inputs naturally; the second form has 3 text inputs + 1 button, which the flexbox layout handles via `flex-wrap`. If width is constrained on phone, the inputs wrap to multiple rows — that's fine.

---

## Task 19: Migrate `credentials.html` (settings-collection with action buttons)

**Files:**
- Modify: `cmd/serf-hub/templates/partials/credentials.html`

Credentials is a settings-collection with per-row action buttons (Set/Replace, OAuth, Clear) and an inline editor form that opens below a row. The two source-layer presentation (file/env effective/shadowed) stays — it's domain-specific and informative.

- [ ] **Step 1:** Replace the entire file content with:

```html
{{define "credentials"}}
<header class="workspace-header">
  <div class="workspace-title">
    <span class="title">credentials</span>
  </div>
</header>
<div class="credentials-pane">
  <p class="credentials-help">
    Provider credentials. Keys are stored in <code>~/.serf/credentials.toml</code>
    (chmod 600). Env vars in the hub process take precedence only when no file
    entry exists. The UI never displays stored values.
  </p>
  <section id="credentials-rows" class="settings-collection" data-loaded="false">
    <header class="settings-collection-head">
      <h3>Providers</h3>
      <span class="settings-collection-count" data-count></span>
    </header>
    <ul class="settings-collection-list" role="list">
      <li class="settings-collection-empty">Loading…</li>
    </ul>
  </section>
</div>
<script>
  (async function () {
    const section = document.getElementById("credentials-rows");
    const list = section.querySelector(".settings-collection-list");
    const countEl = section.querySelector("[data-count]");
    let openEditor = null;

    function escapeHtml(s) {
      return String(s).replace(/[&<>"]/g, c => ({"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;"}[c]));
    }

    function sourceLabel(p) {
      return {
        file: "Configured via stored API key",
        env:  "Configured via environment variable",
        oauth: "Configured via OAuth",
        absent: "Not configured",
        none: "No credentials required",
      }[p.activeSource] || escapeHtml(p.activeSource);
    }

    function badgeState(source) {
      switch (source) {
        case "file": case "oauth": case "env": return "idle";
        case "absent": case "none": return "ended";
        default: return "ended";
      }
    }

    function sourceLayers(p) {
      const bothPresent = p.hasStoredFile && p.envVar;
      if (bothPresent) {
        const fileEffective = p.activeSource === "file";
        const fileLine = `<div class="credentials-source-layer">↳ file: stored key${fileEffective ? ' <span class="status-badge" data-state="idle">effective</span>' : ' <span class="status-badge" data-state="ended">shadowed by env</span>'}</div>`;
        const envLine = `<div class="credentials-source-layer">↳ env: ${escapeHtml(p.envVar)}${!fileEffective ? ' <span class="status-badge" data-state="idle">effective</span>' : ' <span class="status-badge" data-state="ended">shadowed by file</span>'}</div>`;
        return fileLine + envLine;
      }
      return `<div class="row-meta">${sourceLabel(p)}${p.email ? " — " + escapeHtml(p.email) : ""}</div>`;
    }

    function renderRow(p) {
      const supportsApiKey = (p.authModes || []).includes("apiKey");
      const supportsOAuth = (p.authModes || []).includes("oauth");
      const showClear = p.activeSource === "file" || p.activeSource === "oauth";
      const editor = openEditor && openEditor.provider === p.provider ? renderEditor(p, openEditor) : "";
      return `
        <li class="settings-collection-row credentials-row" data-provider="${escapeHtml(p.provider)}">
          <div>
            <div class="row-text">
              ${escapeHtml(p.provider)}
              <span class="status-badge" data-state="${badgeState(p.activeSource)}">
                <span class="status-dot" data-state="${badgeState(p.activeSource)}"></span>
                ${escapeHtml(p.activeSource)}
              </span>
            </div>
            ${sourceLayers(p)}
            ${editor}
          </div>
          <div class="row-actions">
            ${supportsApiKey ? `<button type="button" class="btn btn-secondary" data-action="set">${p.activeSource === "file" ? "Replace key" : "Set API key"}</button>` : ""}
            ${supportsOAuth ? `<button type="button" class="btn btn-secondary" data-action="oauth">${p.activeSource === "oauth" ? "Refresh OAuth" : "Sign in…"}</button>` : ""}
            ${showClear ? `<button type="button" class="btn btn-danger" data-action="clear">Clear</button>` : ""}
          </div>
        </li>
      `;
    }

    function renderEditor(p, e) {
      if (e.kind === "set") {
        return `
          <form class="credentials-editor surface-inset" data-editor="set">
            <label class="credentials-editor-label" for="cred-key-${escapeHtml(p.provider)}">API key for ${escapeHtml(p.provider)}</label>
            <input id="cred-key-${escapeHtml(p.provider)}" type="password" autocomplete="off" placeholder="paste key" class="val-input credentials-editor-input">
            <div class="credentials-editor-actions">
              <button type="submit" class="btn btn-primary">Save</button>
              <button type="button" class="btn btn-ghost" data-action="cancel-edit">Cancel</button>
            </div>
          </form>`;
      }
      if (e.kind === "oauth-redirect") {
        return `
          <form class="credentials-editor surface-inset" data-editor="oauth-redirect" data-flow-id="${escapeHtml(e.flowId)}">
            <label class="credentials-editor-label" for="cred-redirect-${escapeHtml(p.provider)}">
              Authorize in browser, then paste the full redirect URL back here.
              <a href="${escapeHtml(e.authUrl)}" target="_blank" rel="noopener">Re-open authorize URL</a>
            </label>
            <input id="cred-redirect-${escapeHtml(p.provider)}" type="text" autocomplete="off" placeholder="https://…" class="val-input credentials-editor-input">
            <div class="credentials-editor-actions">
              <button type="submit" class="btn btn-primary">Finish</button>
              <button type="button" class="btn btn-ghost" data-action="cancel-edit">Cancel</button>
            </div>
          </form>`;
      }
      return "";
    }

    function render(data) {
      const providers = data.providers || [];
      countEl.textContent = `${providers.length} ${providers.length === 1 ? "provider" : "providers"}`;
      if (!providers.length) {
        list.innerHTML = `<li class="settings-collection-empty">No providers reported.</li>`;
      } else {
        list.innerHTML = providers.map(renderRow).join("");
      }
      section.dataset.loaded = "true";
      if (openEditor) {
        const sel = openEditor.kind === "set"
          ? `[data-provider="${openEditor.provider}"] .credentials-editor input[type=password]`
          : `[data-provider="${openEditor.provider}"] .credentials-editor input[type=text]`;
        const input = list.querySelector(sel);
        if (input) input.focus();
      }
    }

    async function refresh() {
      render(await launchconfig.authList());
    }

    list.addEventListener("click", async (ev) => {
      const btn = ev.target.closest("button[data-action]");
      if (!btn) return;
      const row = btn.closest(".credentials-row");
      const provider = row.dataset.provider;
      const action = btn.dataset.action;
      if (action === "set") {
        openEditor = { provider, kind: "set" };
        await refresh();
      } else if (action === "cancel-edit") {
        openEditor = null;
        await refresh();
      } else if (action === "oauth") {
        try {
          const r = await launchconfig.authLoginStart(provider);
          window.open(r.url, "_blank", "noopener");
          openEditor = { provider, kind: "oauth-redirect", flowId: r.flowId, authUrl: r.url };
          await refresh();
        } catch (err) {
          alert("Sign-in failed: " + (err && err.message ? err.message : err));
        }
      } else if (action === "clear") {
        if (!confirm(`Clear stored credentials for ${provider}?`)) return;
        try {
          await launchconfig.authLogout(provider);
          openEditor = null;
          await refresh();
        } catch (err) {
          alert("Clear failed: " + (err && err.message ? err.message : err));
        }
      }
    });

    list.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      const form = ev.target.closest("form[data-editor]");
      if (!form) return;
      const row = form.closest(".credentials-row");
      const provider = row.dataset.provider;
      const kind = form.dataset.editor;
      function showInlineError(msg) {
        let errEl = form.querySelector(".credentials-editor-error");
        if (!errEl) {
          errEl = document.createElement("p");
          errEl.className = "credentials-editor-error";
          form.querySelector(".credentials-editor-label").after(errEl);
        }
        errEl.textContent = msg;
      }
      if (kind === "set") {
        const value = form.querySelector("input[type=password]").value.trim();
        if (!value) {
          openEditor = null;
          await refresh();
          return;
        }
        try {
          await launchconfig.authApiKeySet(provider, value);
          openEditor = null;
          await refresh();
        } catch (err) {
          showInlineError(err && err.message ? err.message : String(err));
        }
      } else if (kind === "oauth-redirect") {
        const flowId = form.dataset.flowId;
        const redirectUrl = form.querySelector("input[type=text]").value.trim();
        if (!redirectUrl) {
          openEditor = null;
          await refresh();
          return;
        }
        try {
          await launchconfig.authLoginComplete(provider, flowId, redirectUrl);
          openEditor = null;
          await refresh();
        } catch (err) {
          showInlineError(err && err.message ? err.message : String(err));
        }
      }
    });

    list.addEventListener("credentials-reload", (ev) => {
      render(ev.detail);
    });

    refresh();
  })();
</script>
{{end}}
```

- [ ] **Step 2:** Notes on the migration:
  - The outer `<div id="credentials-rows">` becomes `<section class="settings-collection">` carrying both the head (count) and the list.
  - Per-row markup becomes a `settings-collection-row` `<li>` — but with a wider main column to accommodate the source-layer detail lines, the open editor form, and the status badge.
  - Buttons gain `class="btn btn-secondary"` / `class="btn btn-danger"`.
  - The editor form gains `class="surface-inset"` (Pass 4 surface variant).
  - The status pill is replaced by `.status-badge` + `.status-dot` per Pass 4.
  - The `credentials-reload` custom event listener is preserved (it's part of the contract — search-palette dispatches it).

- [ ] **Step 3:** Add minor CSS to keep the editor + source layers laid out under the row main column:

```css
.credentials-row .row-text {
  display: flex;
  align-items: baseline;
  gap: var(--space-3);
  flex-wrap: wrap;
}
.credentials-source-layer {
  margin-top: 2px;
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  color: var(--text-muted);
}
.credentials-editor {
  margin-top: var(--space-3);
  padding: var(--space-3);
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
.credentials-editor-label {
  font-family: var(--font-sans);
  font-size: var(--text-xs);
  color: var(--text-muted);
}
.credentials-editor-actions {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}
.credentials-editor-error {
  margin: 0;
  font-family: var(--font-sans);
  font-size: var(--text-xs);
  color: var(--state-awaiting);
}
```

  Delete the legacy `.credentials-row`, `.credentials-row-main`, `.credentials-row-name`, `.credentials-row-status`, `.credentials-row-actions`, `.credentials-source-badge`, `.credentials-source-effective`, `.credentials-source-shadowed`, `.credentials-clear-btn`, `.credentials-editor-input` rules in Pass 8's CSS sweep, not now.

---

## Task 20: Settings-nav search filter + phone nav-as-page

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings.html`
- Modify: `cmd/serf-hub/assets/settings.js`
- Modify: `cmd/serf-hub/assets/style.css`

- [ ] **Step 1:** Update `settings.html` to add a search input at the top of `<nav class="settings-nav">` and a back link slot for phone nav-as-page:

```html
{{define "settings"}}
<header class="workspace-header">
  <div class="workspace-title">
    <button type="button" class="btn-ghost settings-nav-back" hidden aria-label="Back to settings">‹ Settings</button>
    <span class="title" data-settings-section="{{.Active}}">{{.Active}}</span>
  </div>
</header>
<div class="settings-pane">
  <nav class="settings-nav" aria-label="Settings sections">
    <div class="settings-nav-filter">
      <input type="search" class="val-input" placeholder="Filter settings…" data-settings-nav-filter aria-label="Filter settings">
    </div>
    <a class="settings-nav-link {{if eq .Active "general"}}active{{end}}" href="/settings/general" hx-get="/_partials/settings/general" hx-target="#settings-content" hx-swap="innerHTML" hx-push-url="/settings/general">General</a>
    <a class="settings-nav-link {{if eq .Active "theme"}}active{{end}}" href="/settings/theme" hx-get="/_partials/settings/theme" hx-target="#settings-content" hx-swap="innerHTML" hx-push-url="/settings/theme">Theme</a>
    <a class="settings-nav-link {{if eq .Active "notifications"}}active{{end}}" href="/settings/notifications" hx-get="/_partials/settings/notifications" hx-target="#settings-content" hx-swap="innerHTML" hx-push-url="/settings/notifications">Notifications</a>
    <div class="settings-nav-section">Agents &amp; models</div>
    <a class="settings-nav-link {{if eq .Active "providers"}}active{{end}}" href="/settings/providers" hx-get="/_partials/settings/providers" hx-target="#settings-content" hx-swap="innerHTML" hx-push-url="/settings/providers">Providers</a>
    <a class="settings-nav-link" href="/credentials" hx-get="/_partials/credentials" hx-target="#workspace" hx-swap="innerHTML" hx-push-url="/credentials">Credentials</a>
    <a class="settings-nav-link {{if eq .Active "agents"}}active{{end}}" href="/settings/agents" hx-get="/_partials/settings/agents" hx-target="#settings-content" hx-swap="innerHTML" hx-push-url="/settings/agents">Agents</a>
    <a class="settings-nav-link {{if eq .Active "launch-serf"}}active{{end}}" href="/settings/launch-serf" hx-get="/_partials/settings/launch-serf" hx-target="#settings-content" hx-swap="innerHTML" hx-push-url="/settings/launch-serf">Serf launch</a>
    <a class="settings-nav-link {{if eq .Active "launch-codex"}}active{{end}}" href="/settings/launch-codex" hx-get="/_partials/settings/launch-codex" hx-target="#settings-content" hx-swap="innerHTML" hx-push-url="/settings/launch-codex">Codex launch</a>
    <a class="settings-nav-link {{if eq .Active "inrepo"}}active{{end}}" href="/settings/inrepo" hx-get="/_partials/settings/inrepo" hx-target="#settings-content" hx-swap="innerHTML" hx-push-url="/settings/inrepo">In-repo config</a>
    <div class="settings-nav-section">Extensions</div>
    <a class="settings-nav-link {{if eq .Active "plugins"}}active{{end}}" href="/settings/plugins" hx-get="/_partials/settings/plugins" hx-target="#settings-content" hx-swap="innerHTML" hx-push-url="/settings/plugins">Plugins</a>
    <a class="settings-nav-link {{if eq .Active "skills"}}active{{end}}" href="/settings/skills" hx-get="/_partials/settings/skills" hx-target="#settings-content" hx-swap="innerHTML" hx-push-url="/settings/skills">Skills</a>
    <a class="settings-nav-link {{if eq .Active "mcp"}}active{{end}}" href="/settings/mcp" hx-get="/_partials/settings/mcp" hx-target="#settings-content" hx-swap="innerHTML" hx-push-url="/settings/mcp">MCP servers</a>
    <div class="settings-nav-section">Daemon</div>
    <a class="settings-nav-link {{if eq .Active "hub"}}active{{end}}" href="/settings/hub" hx-get="/_partials/settings/hub" hx-target="#settings-content" hx-swap="innerHTML" hx-push-url="/settings/hub">Hub</a>
    <a class="settings-nav-link {{if eq .Active "storage"}}active{{end}}" href="/settings/storage" hx-get="/_partials/settings/storage" hx-target="#settings-content" hx-swap="innerHTML" hx-push-url="/settings/storage">Storage</a>
  </nav>
  <div id="settings-content" class="settings-content">
    {{template "settings-content" .}}
  </div>
</div>
{{end}}
```

- [ ] **Step 2:** Append the following CSS to `style.css` (right after the settings-collection block from Task 2):

```css
/* Settings-nav search filter (≥12 entries → always shown) */
.settings-nav-filter {
  padding: var(--space-2) var(--space-4) var(--space-3);
}
.settings-nav-filter .val-input {
  width: 100%;
  font-size: var(--text-xs);
}
.settings-nav-link[hidden] { display: none; }
.settings-nav-section[hidden] { display: none; }

/* Phone: nav-as-page navigation
   By default, both the nav and the content are visible side-by-side. On phone,
   we show ONE at a time. The body carries data-settings-pane="nav" (index) or
   data-settings-pane="content" (sub-page detail). The back chevron returns to
   the nav. URL routing is unchanged — only visibility flips. */
@media (max-width: 767px) {
  .settings-pane {
    display: block;
    overflow: visible;
  }
  .settings-nav {
    width: 100%;
    border-right: none;
    border-bottom: 1px solid var(--rule);
  }
  .settings-content { padding: var(--space-5) var(--space-4); }

  /* When pane=content, hide nav and show content (with back chevron) */
  body[data-settings-pane="content"] .settings-nav { display: none; }
  body[data-settings-pane="content"] .settings-nav-back { display: inline-flex; }

  /* When pane=nav (default), hide the content area entirely on phone */
  body[data-settings-pane="nav"] #settings-content { display: none; }
  body:not([data-settings-pane="content"]) .settings-nav-back[hidden] { display: none; }
}
```

- [ ] **Step 3:** Append to `cmd/serf-hub/assets/settings.js`:

```js
// Settings-nav filter
(function () {
  const input = document.querySelector("[data-settings-nav-filter]");
  if (!input) return;
  function applyFilter() {
    const q = input.value.trim().toLowerCase();
    const nav = input.closest(".settings-nav");
    nav.querySelectorAll(".settings-nav-link").forEach(a => {
      const visible = !q || a.textContent.toLowerCase().includes(q);
      a.hidden = !visible;
    });
    // Hide section headers whose children are all hidden
    nav.querySelectorAll(".settings-nav-section").forEach(h => {
      let nxt = h.nextElementSibling;
      let anyVisible = false;
      while (nxt && !nxt.classList.contains("settings-nav-section")) {
        if (nxt.classList.contains("settings-nav-link") && !nxt.hidden) { anyVisible = true; break; }
        nxt = nxt.nextElementSibling;
      }
      h.hidden = !anyVisible;
    });
  }
  input.addEventListener("input", applyFilter);
})();

// Phone nav-as-page wiring
(function () {
  const body = document.body;
  const back = document.querySelector(".settings-nav-back");
  function syncPane() {
    // If we're in a settings route, default to content; if at /settings (root)
    // with no Active section, show nav. The Active section is rendered into
    // the title — use its presence as the signal.
    const title = document.querySelector(".workspace-title .title[data-settings-section]");
    body.dataset.settingsPane = title && title.textContent.trim() ? "content" : "nav";
  }
  syncPane();
  if (back) {
    back.addEventListener("click", () => {
      body.dataset.settingsPane = "nav";
      // Navigate to /settings root via history; HTMX is not used here because
      // the visibility-only flip is local.
      if (window.history && history.pushState) history.pushState({}, "", "/settings");
    });
  }
  // After every htmx swap that targets #settings-content, update the pane.
  document.body.addEventListener("htmx:afterSwap", (ev) => {
    if (ev.detail && ev.detail.target && ev.detail.target.id === "settings-content") {
      syncPane();
    }
  });
})();
```

- [ ] **Step 4:** Verify the filter feels right. Type "launch" — only the two launch entries plus the in-repo entry (which contains "config" not "launch") should remain, with the right section headers also hiding when empty. Re-type empty — everything reappears.

- [ ] **Step 5:** Verify phone behavior in dev tools mobile mode (390 × 844). At `/settings` you see the index page (nav only). Tap "Theme" — workspace innerHTML swaps to the theme partial with `< Settings` back chevron in the header. Tap the chevron — the workspace returns to nav.

---

## Task 21: Remove inline `style="margin:0"` overrides

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings/launch-serf.html` (already done in Task 13)
- Modify: `cmd/serf-hub/templates/partials/settings/project.html` (already done in Task 14)
- Modify: `cmd/serf-hub/assets/style.css`

- [ ] **Step 1:** Confirm Tasks 13 and 14 replaced `style="margin:0"` with `class="settings-form-status"`. Run `grep -rn 'style="margin:0"' cmd/serf-hub/templates/` — output should be empty.

- [ ] **Step 2:** Confirm `.settings-form-status` is defined in `style.css` (added in Task 13 Step 3). The rule sets `margin: 0; font-family: var(--font-sans); font-size: var(--text-xs); color: var(--text-muted);` — combining the margin-zero override + the consistent help-text typography.

- [ ] **Step 3:** Run `grep -rn 'style="' cmd/serf-hub/templates/` and confirm only data-driven inline styles remain (e.g., `style="width:{{.Pct}}%"` on context-fill or similar). Pre-existing data-driven inline styles are permitted per the spec; static-value inline styles are not.

---

## Task 22: Build + manual verification

**Files:** none

- [ ] **Step 1:** `make build-hub && ./serf-hub` (or your local equivalent). Browse to `http://127.0.0.1:9180/settings/general` — verify the page loads, the `<dl class="settings-table">` renders with 160px / 1fr columns, labels are sans + weight 500, values are mono. Toggle the theme via Theme settings — every row repaints with the new palette without flash.

- [ ] **Step 2:** Walk every sub-page in dark theme: General, Theme, Notifications, Providers, Agents, Serf launch, Codex launch, In-repo config, Plugins, Skills, MCP servers, Hub, Storage, Project (with a CWD set + with no CWD), Credentials. Each one should render in the new primitive with no visible glitches.

- [ ] **Step 3:** Switch to light theme. Repeat the walk. Confirm light-mode contrast — toggle ON/OFF pills should be clearly readable; status badges in Providers/Credentials should carry state color visibly against the cream background.

- [ ] **Step 4:** On Serf launch settings, fill in a value (e.g., set `agent` to a non-default), click Save, confirm "Saved at HH:MM:SS" appears. Reload the page; confirm the value persists. Repeat on Project settings.

- [ ] **Step 5:** On Plugins / Skills / MCP servers, add a directory + remove it. Confirm the collection re-renders, the count badge updates, and the empty-state row appears when zero entries remain.

- [ ] **Step 6:** On Credentials, click "Set API key" — the inline editor opens below the row inside a `surface-inset` block; the password input focuses; Save / Cancel buttons use the new variants. Cancel returns to clean row state.

- [ ] **Step 7:** In dev tools mobile mode (390 × 844), navigate to `/settings`. Confirm the nav-as-page index. Tap "Theme". Confirm the back chevron appears in the workspace header, the content area shows the Theme partial, the nav is hidden. Tap the back chevron. Nav returns. URL is `/settings`.

- [ ] **Step 8:** Type "launch" into the settings-nav filter. Confirm only the matching entries remain visible and the relevant section headers stay; non-matching section headers (e.g., "Daemon") hide.

- [ ] **Step 9:** Run `cmd/serf-hub/jstest/run-all.sh`. All tests pass — particularly `test-launchconfig-controls.js` (verifies render + collect + validate end-to-end).

- [ ] **Step 10:** Run `make test` (Go) and `make lint-naming`. Both pass.

- [ ] **Step 11:** Grep for any remaining legacy class names that the new primitives should have replaced. Expected output is empty in templates:
  - `grep -rn 'settings-list\|settings-form\b\|settings-rows\|settings-row-title\|settings-row-meta\|settings-toggle\|settings-radio\|status-pill' cmd/serf-hub/templates/`
  - The settings-row* rules in `style.css` itself stay until Pass 8's sweep.

- [ ] **Step 12:** A `prefers-reduced-motion: reduce` smoke test — set `chrome://flags/#prefers-reduced-motion` to `reduce` (or `system-ui` simulation), reload, confirm none of the new transitions runs visibly (they cap at 1ms per Pass 3's rule).

---

## Notes for implementers

- **Picker widgets stay unchanged.** `.sp-model-wrap`, `.sp-model-btn`, `.sp-dir-wrap`, `.sp-dir-btn`, `.sp-clear-btn` keep their current CSS — they sit inside `<dd>` cells and inside `.settings-collection-add` forms. Don't migrate them; the spec defers their consolidation to a follow-up pass.
- **The `data-launch-*` attribute set is sacrosanct.** Tasks 12–14 rely on every attribute (`data-launch-wire-field`, `data-launch-kind`, `data-launch-path-kind`, `data-launch-list`, `data-launch-env-list`, `data-launch-mcp-list`, `data-launch-explicit-empty`, `data-launch-validation-error`, `data-launch-invalid`, `data-launch-mcp-command`, `data-launch-option`, `data-launch-settings-root`, `data-launch-settings-layer`, `data-launch-settings-groups`, `data-launch-schema-loading`, `data-launch-advanced-root`, `data-launch-advanced-groups`) being preserved. If you find yourself wanting to rename one, stop — that's outside Pass 7's scope.
- **Spawn-mode markup is unchanged.** Pass 6 already shipped the spawn/composer pass; Pass 7 only changes the `mode === "settings"` branch of `LaunchConfigControls.render()`.
- **`settings-error` is a legacy class.** Several scripts (plugins, skills, mcp, inrepo, credentials) use `<p class="settings-error">` for inline errors. Keep using it for now; Pass 8 may consolidate it onto a `.banner-error` or `.row-error` token. The rule's existing `color: var(--state-awaiting)` carries the meaning.
- **`.settings-help` stays.** It's the prose helper paragraph used outside the table (e.g., the intro paragraph above the table). Inside the table, `.help` is grid-column-spanned. They are not interchangeable.
- **One-off documentation:** in-repo trust + LaunchConfigControls schema-driven form are the two documented one-offs. Both adopt the primitive markup where they can (CWD row in a `settings-table`, schema groups in a `settings-table` via `LaunchConfigControls`), but their unique controls (trust button, model picker, list/env/mcp collections inside a row's `<dd>`) live alongside.

## Success criteria

The pass is done when:

1. All 14 settings sub-pages + credentials.html render via the new primitives.
2. `LaunchConfigControls.render(form, {mode: "settings"})` produces `<div class="row">…</div>` markup inside a `<dl class="settings-table">` parent.
3. Spawn-mode rendering (`/new` Advanced) is visually pixel-equivalent to pre-pass.
4. `cmd/serf-hub/jstest/run-all.sh` and `make test` and `make lint-naming` all pass.
5. No `style="margin:0"` (or any other static inline style) appears in the settings or credentials templates.
6. Phone nav-as-page works: tap a settings entry, content swaps in with back chevron; tap back chevron, nav returns.
7. Settings-nav search filter shows/hides links + their section headers based on the query.
8. Phone density and sidebar mode radios on Theme settings persist to localStorage and apply to body data-attributes.
9. Light theme has visible contrast across all migrated surfaces.
10. The legacy CSS rules (`.settings-list`, `.settings-form`, `.settings-rows`, `.settings-launch-form`, `.settings-launch-row`, `.settings-launch-group`, `.spawn-advanced-row` for settings mode, `.settings-add-row`, `.settings-row-title`, `.settings-row-meta`, `.settings-toggle`, `.settings-radio`, `.status-pill`) remain defined in `style.css` for now — Pass 8 sweeps them.
