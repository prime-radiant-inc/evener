# Consistency sweep — Track 0: settings-pane split prep

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Split `cmd/serf-hub/assets/settings.js` — the monolithic event-delegation script behind the web settings pane — into per-section files, so that Track A (adds a `loudScope` control to the notifications section) and Track C (adds font-size, Enter-to-send, and Show-cost controls) edit disjoint files and never collide. **Pure refactor: no behavior change, no new controls, rendering byte-identical.** Lands on `consistency-sweep` before Tracks A/B/C fork.

**Architecture:** The settings pane's **HTML templates are already split** per section (`cmd/serf-hub/templates/partials/settings/*.html` — one file per nav entry: `general.html`, `theme.html`, `notifications.html`, `transcript.html`, etc.), wired by a `settingsSections` slice and a per-section `*template.Template` map in `cmd/serf-hub/web.go`. The **JS side is not split**: `settings.js` is one file containing six independent IIFEs covering theme/phone-density/sidebar-mode (a "Theme" section concern), the four notification toggles (a "Notifications" section concern), transcript-status toggles (a "Transcript" section concern), the settings-nav filter + phone back-button (shell chrome, no section owns it), and — piggybacked in for no principled reason — workspace model-chip abbreviation (not a settings concern at all). This is the real collision surface: the design spec's Track A note ("Settings control uses the existing `data-notif` commit pattern (settings.js:49,115)") and Track C's three new controls would otherwise all edit the same delegated-listener file. This track decomposes `settings.js` into one file per existing HTML section (mirroring the already-split templates), plus one file for the unowned shell chrome and one for the unrelated model-display code, then deletes the empty original.

**Tech Stack:** Vanilla JS (event delegation on `document.body`, no build step), html/template (untouched — zero `.html` edits in this track), jstest (JSDOM, agent-run, `cmd/serf-hub/jstest/run-all.sh`).

---

## The section-file contract (read this first)

Track 0 creates these JS files, one per **existing** settings section, matching the already-split HTML:

| Settings section (existing `.html`, untouched) | New JS file (created by this track) |
|---|---|
| `templates/partials/settings/theme.html` | `assets/settings-appearance.js` |
| `templates/partials/settings/notifications.html` | `assets/settings-notifications.js` |
| `templates/partials/settings/transcript.html` | `assets/settings-transcript.js` |
| *(none — shell chrome, no section owns it)* | `assets/settings-shell.js` |
| *(none — not a settings concern; workspace model chip)* | `assets/model-display.js` |

**Track A** (`loudScope` control) edits `templates/partials/settings/notifications.html` + `assets/settings-notifications.js`.

**Track C**'s three controls split across two homes:
- **Font-size** (a type-scale preset, same family as the existing Phone-density radio) goes into the existing **Theme** section: `templates/partials/settings/theme.html` + `assets/settings-appearance.js`.
- **Enter-to-send** and **Show-cost** (behavioral/display toggles, not appearance) need a section that doesn't exist yet. Track 0 does **not** create it — pre-creating an empty nav entry would add a visible, content-less nav link and violate this track's byte-identical-rendering rule. Track 0 only **reserves the name** as the contract: when Track C lands, it creates `templates/partials/settings/display.html` (nav label **"Display"**) + `assets/settings-display.js`, and wires it the same way every other section is wired (see "Handoff notes for Track C" at the end of this plan).

Since Track A only ever touches `notifications.html`/`settings-notifications.js` and Track C only ever touches `theme.html`/`settings-appearance.js` plus the net-new `display.html`/`settings-display.js`, **no file is edited by both tracks.**

---

## Conventions used throughout this plan

- All file paths are relative to `cmd/serf-hub/` unless given in full.
- jstest (not in CI/Makefile; agent-run): `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node <file>.js` for one test, or `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh` for the full suite (jsdom install per `jstest/README.md`). `run-all.sh` auto-discovers every `test-*.js` in the directory — a newly added test file needs no registration.
- Existing jstests load hand-written `assets/*.js` files into a JSDOM `window` via `fs.readFileSync` + `window.eval(src)`, then fire real DOM events and assert on the resulting DOM/localStorage — no framework, no mocking of the code under test. This plan follows the same pattern (see `test-settings.js`, `test-settings-dir-picker.js` for precedent).
- **This track touches zero Go source and zero settings-pane `.html` templates.** The only non-JS file touched is `cmd/serf-hub/templates/app.html` (the `<script>` tag list) — verified after every task by running the `cmd/serf-hub` Go test package (a broken `app.html` fails `template.Must(...ParseFS(...))` inside `NewWebServer`, which many existing Go tests construct).
- **Guardrail (copied from the design spec, §Tracks / §1):** JSON/TOML keys stay snake_case; `namingcheck` runs at `make lint` (per-task `golangci-lint` misses it — run `make lint-naming` before the final commit as a cheap sanity check even though this track adds no Go identifiers). Test output must be pristine — no unexplained console noise from a passing jstest run.
- Never `git add -A`. Stage only the exact paths listed in each task's commit step (after a `git status`).

---

## File structure

**Created:**
- `cmd/serf-hub/assets/settings-shell.js` — settings-nav filter + phone nav-as-page/back-button wiring. Shell chrome; not owned by Track A or C.
- `cmd/serf-hub/assets/settings-appearance.js` — theme / phone-density / sidebar-mode radios (commit + restore + on-load apply). Track C extends this for font-size.
- `cmd/serf-hub/assets/settings-notifications.js` — the four `data-notif` checkboxes (commit, OS-permission flow, restore). Track A extends this for `loudScope`.
- `cmd/serf-hub/assets/settings-transcript.js` — the four `data-transcript-status` checkboxes (commit + restore).
- `cmd/serf-hub/assets/model-display.js` — workspace model-chip abbreviation wiring. Unrelated to the settings pane; extracted so `settings.js` can be deleted cleanly.
- `cmd/serf-hub/jstest/test-settings-shell.js` — new characterization test (nav filter + back button had no prior coverage).
- `cmd/serf-hub/jstest/test-settings-appearance.js` — new characterization test (theme/phone-density/sidebar-mode had no prior coverage).
- `cmd/serf-hub/jstest/test-settings-notifications.js` — new characterization test (notif toggles + OS-permission flow had no prior coverage).
- `cmd/serf-hub/jstest/test-model-display.js` — new characterization test (model-chip abbreviation wiring had no prior coverage — `test-abbreviate-model.js` only covers the pure `abbreviateModel` function in `spawn.js`, not this DOM-wiring loop).

**Modified:**
- `cmd/serf-hub/templates/app.html` — replace the single `settings.js` `<script>` tag with five tags for the files above.
- `cmd/serf-hub/jstest/test-settings.js` — retarget its `SRC` require from `../assets/settings.js` to `../assets/settings-transcript.js` (its assertions are, and remain, transcript-status-only — this is the one true "port," everything else is new coverage).

**Deleted:**
- `cmd/serf-hub/assets/settings.js` — once every IIFE has moved out, nothing settings-pane-shaped remains in it.

**Verified unchanged (zero edits, confirmed by `git diff --stat` in the final gate):**
- `cmd/serf-hub/templates/partials/settings.html`, `cmd/serf-hub/templates/partials/settings/*.html`, `cmd/serf-hub/web.go`, `cmd/serf-hub/web_settings.go`, `cmd/serf-hub/embed.go` (its `assets/*` glob auto-covers new files; no glob edit needed).

---

## Task 1 — Extract `settings-shell.js` (nav filter + phone back-button)

Moves the two IIFEs (current `settings.js:169-192` nav-filter, `:194-243` phone-nav-as-page/back-button) that belong to neither Track A's nor Track C's sections. Untested today — this is new characterization coverage.

- [ ] **Failing test** — create `cmd/serf-hub/jstest/test-settings-shell.js`:
  ```js
  const fs = require("fs");
  const { JSDOM } = require("jsdom");

  const SRC = fs.readFileSync("../assets/settings-shell.js", "utf8");

  function assert(cond, msg) {
    if (!cond) {
      console.error("FAIL: " + msg);
      process.exit(1);
    }
  }

  function makeWindow() {
    const dom = new JSDOM(`<!DOCTYPE html><html><body>
      <header class="workspace-header">
        <div class="workspace-title">
          <button type="button" class="btn-ghost settings-nav-back" hidden aria-label="Back to settings">‹ Settings</button>
          <span class="title" data-settings-section="theme">theme</span>
        </div>
      </header>
      <nav class="settings-nav" aria-label="Settings sections">
        <div class="settings-nav-filter">
          <input type="search" class="val-input" data-settings-nav-filter placeholder="Filter settings…">
        </div>
        <a class="settings-nav-link" href="/settings/general">General</a>
        <a class="settings-nav-link" href="/settings/theme">Theme</a>
        <div class="settings-nav-section">Agents &amp; models</div>
        <a class="settings-nav-link" href="/settings/agents">Agents</a>
      </nav>
    </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "https://test.local/settings/theme" });
    return dom.window;
  }

  (function main() {
    const window = makeWindow();
    window.eval(SRC);
    window.document.dispatchEvent(new window.Event("DOMContentLoaded", { bubbles: true }));

    // --- nav filter ---
    const filterInput = window.document.querySelector("[data-settings-nav-filter]");
    const sectionHeader = window.document.querySelector(".settings-nav-section");
    assert(![...window.document.querySelectorAll(".settings-nav-link")].some(a => a.hidden), "no links hidden before filtering");

    filterInput.value = "agents";
    filterInput.dispatchEvent(new window.Event("input", { bubbles: true }));
    assert(window.document.querySelector('a[href="/settings/general"]').hidden, "General hides when filtering for 'agents'");
    assert(!window.document.querySelector('a[href="/settings/agents"]').hidden, "Agents stays visible when filtering for 'agents'");
    assert(!sectionHeader.hidden, "'Agents & models' header stays visible: it has a visible child link");

    filterInput.value = "nomatch";
    filterInput.dispatchEvent(new window.Event("input", { bubbles: true }));
    assert(sectionHeader.hidden, "'Agents & models' header hides when every child link is hidden");

    filterInput.value = "";
    filterInput.dispatchEvent(new window.Event("input", { bubbles: true }));
    assert(![...window.document.querySelectorAll(".settings-nav-link")].some(a => a.hidden), "clearing the filter re-shows every link");

    // --- back button / syncPane ---
    const back = window.document.querySelector(".settings-nav-back");
    assert(back.hasAttribute("hidden") === false, "back button becomes visible on load: an Active section title is present");

    back.click();
    assert(back.hasAttribute("hidden"), "clicking back hides the back button");
    assert(window.document.body.dataset.settingsPane === "nav", "clicking back flips settingsPane to nav");

    console.log("PASS — settings-nav filter and phone back-button wiring");
  })();
  ```
  Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-settings-shell.js` → expect **FAIL** (`ENOENT: no such file or directory, open '../assets/settings-shell.js'`).
- [ ] **Implement** — create `cmd/serf-hub/assets/settings-shell.js` with exactly the two IIFEs currently at `settings.js:169-192` and `:194-243` (verbatim, no logic changes):
  ```js
  // Settings-nav filter — delegated so it works after HTMX swaps the settings shell.
  (function () {
    document.body.addEventListener("input", (e) => {
      const input = e.target && e.target.closest("[data-settings-nav-filter]");
      if (!input) return;
      const q = input.value.trim().toLowerCase();
      const nav = input.closest(".settings-nav");
      if (!nav) return;
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
    });
  })();

  // Phone nav-as-page wiring — delegated so it works after HTMX swaps the settings shell.
  (function () {
    const body = document.body;

    function syncPane() {
      // If we're in a settings route, default to content; if at /settings (root)
      // with no Active section, show nav. The Active section is rendered into
      // the title — use its presence as the signal.
      const title = document.querySelector(".workspace-title .title[data-settings-section]");
      const isContent = !!(title && title.textContent.trim());
      body.dataset.settingsPane = isContent ? "content" : "nav";

      // Toggle the back button's hidden attribute — CSS display cannot override
      // the HTML hidden attribute, so we must manage it explicitly.
      const back = document.querySelector(".settings-nav-back");
      if (back) {
        if (isContent) {
          back.removeAttribute("hidden");
        } else {
          back.setAttribute("hidden", "");
        }
      }
    }

    // Delegated click handler for the back button — survives DOM swaps.
    document.body.addEventListener("click", (e) => {
      if (!e.target || !e.target.closest) return;
      const btn = e.target.closest(".settings-nav-back");
      if (!btn) return;
      body.dataset.settingsPane = "nav";
      const back = document.querySelector(".settings-nav-back");
      if (back) back.setAttribute("hidden", "");
      // Navigate to /settings root via history; HTMX is not used here because
      // the visibility-only flip is local.
      if (window.history && history.pushState) history.pushState({}, "", "/settings");
    });

    // Run syncPane on initial load and after any HTMX swap that brings in the
    // settings shell (#workspace) or updates the active content (#settings-content).
    function onAfterSwap(ev) {
      if (!ev.detail || !ev.detail.target) return;
      const id = ev.detail.target.id;
      if (id === "workspace" || id === "settings-content") {
        syncPane();
      }
    }

    document.addEventListener("DOMContentLoaded", syncPane);
    document.body.addEventListener("htmx:afterSwap", onAfterSwap);
  })();
  ```
  Delete these two IIFEs (`settings.js:169-192`, `:194-243`) from `settings.js`.
  In `cmd/serf-hub/templates/app.html`, insert `  <script src="/assets/settings-shell.js{{assetv}}"></script>` immediately **before** the existing `<script src="/assets/settings.js{{assetv}}"></script>` line (currently line 77).
- [ ] **Run** `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-settings-shell.js` → pass.
- [ ] **Run** the existing suite to confirm `settings.js`'s remaining IIFEs (appearance/notif/transcript/model-display) are unaffected: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh` → all `OK`. Also `go test ./cmd/serf-hub/... -count=1` (root module) → green — confirms `app.html` still parses.
- [ ] **Commit** — `git add cmd/serf-hub/assets/settings.js cmd/serf-hub/assets/settings-shell.js cmd/serf-hub/templates/app.html cmd/serf-hub/jstest/test-settings-shell.js` → `refactor(hub-web): extract settings-nav filter + phone back-button into settings-shell.js`.

## Task 2 — Extract `settings-appearance.js` (theme / phone-density / sidebar-mode)

Moves the theme/phone-density/sidebar-mode slice of the `change` listener and `applySettingsState` (from the combined IIFE at `settings.js:4-152`) plus the two standalone "apply stored value on load" IIFEs (`:154-159`, `:161-167`). This is the file Track C will extend with the font-size preset. Untested today.

**Deviation, called out explicitly:** the original `applySettingsState` registration is double: `document.addEventListener("DOMContentLoaded", applySettingsState)`, then an unconditional `document.body.addEventListener("htmx:afterSwap", applySettingsState)`, then a *third*, redundant `document.addEventListener("DOMContentLoaded", () => { document.body.addEventListener("htmx:afterSwap", applySettingsState); })` — registering the same `afterSwap` listener twice. Since every restore function here is idempotent (it only sets `.checked`/`.value` from `localStorage`), the duplicate call has no observable effect on rendered output — it is dead redundancy, not a behavior difference. This plan drops the redundant third registration in each of the three split files (one clean `DOMContentLoaded` + one clean `htmx:afterSwap` registration per file) rather than tripling the wart across three new files. Flagging this for review since it is the one non-mechanical change in an otherwise byte-for-byte move.

- [ ] **Failing test** — create `cmd/serf-hub/jstest/test-settings-appearance.js`:
  ```js
  const fs = require("fs");
  const { JSDOM } = require("jsdom");

  const THEME_SRC = fs.readFileSync("../assets/theme.js", "utf8");
  const SRC = fs.readFileSync("../assets/settings-appearance.js", "utf8");

  function assert(cond, msg) {
    if (!cond) {
      console.error("FAIL: " + msg);
      process.exit(1);
    }
  }

  function change(input) {
    input.dispatchEvent(new input.ownerDocument.defaultView.Event("change", { bubbles: true }));
  }

  function makeWindow() {
    const dom = new JSDOM(`<!DOCTYPE html><html><body>
      <div class="val-radio-group" data-theme-picker>
        <label class="val-radio"><input type="radio" name="theme" value="system"></label>
        <label class="val-radio"><input type="radio" name="theme" value="dark"></label>
        <label class="val-radio"><input type="radio" name="theme" value="light"></label>
      </div>
      <div class="val-radio-group" data-phone-density-picker>
        <label class="val-radio"><input type="radio" name="phone-density" value="compact"></label>
        <label class="val-radio"><input type="radio" name="phone-density" value="comfortable"></label>
      </div>
      <div class="val-radio-group" data-sidebar-mode-picker>
        <label class="val-radio"><input type="radio" name="sidebar-mode" value="pane"></label>
        <label class="val-radio"><input type="radio" name="sidebar-mode" value="rail"></label>
      </div>
    </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "https://test.local/" });
    const { window } = dom;
    window.SerfToast = { messages: [], show(message, kind) { this.messages.push({ message, kind }); } };
    return window;
  }

  (function main() {
    const window = makeWindow();
    window.eval(THEME_SRC);
    window.eval(SRC);

    // --- theme ---
    const dark = window.document.querySelector('input[name="theme"][value="dark"]');
    dark.checked = true;
    change(dark);
    assert(window.document.documentElement.getAttribute("data-theme") === "dark", "setting theme=dark applies data-theme");
    assert(window.localStorage.getItem("serf-hub.theme") === "dark", "theme choice persists to localStorage");
    assert(window.SerfToast.messages.some(m => m.message === "Theme: dark"), "theme change toasts the new value");

    const system = window.document.querySelector('input[name="theme"][value="system"]');
    system.checked = true;
    change(system);
    assert(window.document.documentElement.getAttribute("data-theme") === null, "theme=system removes data-theme");
    assert(window.localStorage.getItem("serf-hub.theme") === null, "theme=system clears the stored preference");

    // --- phone density ---
    const comfortable = window.document.querySelector('input[name="phone-density"][value="comfortable"]');
    comfortable.checked = true;
    change(comfortable);
    assert(window.localStorage.getItem("serf-hub.phone-density") === "comfortable", "phone-density choice persists");
    assert(window.document.body.dataset.phoneDensity === "comfortable", "phone-density applies to body dataset immediately");

    // --- sidebar mode ---
    const rail = window.document.querySelector('input[name="sidebar-mode"][value="rail"]');
    rail.checked = true;
    change(rail);
    assert(window.localStorage.getItem("serf-hub.sidebar.rail") === "true", "sidebar-mode=rail persists");
    assert(window.document.body.dataset.sidebarRail === "", "sidebar-mode=rail sets the rail dataset attribute");

    const pane = window.document.querySelector('input[name="sidebar-mode"][value="pane"]');
    pane.checked = true;
    change(pane);
    assert(window.localStorage.getItem("serf-hub.sidebar.rail") === "false", "sidebar-mode=pane persists");
    assert(!("sidebarRail" in window.document.body.dataset), "sidebar-mode=pane clears the rail dataset attribute");

    // --- restore on (re)load ---
    const restored = makeWindow();
    restored.localStorage.setItem("serf-hub.theme", "light");
    restored.localStorage.setItem("serf-hub.phone-density", "comfortable");
    restored.localStorage.setItem("serf-hub.sidebar.rail", "true");
    restored.eval(THEME_SRC);
    restored.eval(SRC);
    restored.document.dispatchEvent(new restored.Event("DOMContentLoaded", { bubbles: true }));
    assert(restored.document.querySelector('input[name="theme"][value="light"]').checked, "restore checks the stored theme radio");
    assert(restored.document.querySelector('input[name="phone-density"][value="comfortable"]').checked, "restore checks the stored phone-density radio");
    assert(restored.document.querySelector('input[name="sidebar-mode"][value="rail"]').checked, "restore checks the stored sidebar-mode radio");

    // --- the two "apply stored value on load" IIFEs run before DOMContentLoaded ---
    const preset = makeWindow();
    preset.localStorage.setItem("serf-hub.phone-density", "comfortable");
    preset.localStorage.setItem("serf-hub.sidebar.rail", "true");
    preset.eval(THEME_SRC);
    preset.eval(SRC);
    assert(preset.document.body.dataset.phoneDensity === "comfortable", "phone-density applies on script load, before DOMContentLoaded fires");
    assert(preset.document.body.dataset.sidebarRail === "", "sidebar-mode applies on script load, before DOMContentLoaded fires");

    console.log("PASS — settings-appearance theme/phone-density/sidebar-mode commit, restore, and on-load apply");
  })();
  ```
  Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-settings-appearance.js` → expect **FAIL** (ENOENT).
- [ ] **Implement** — create `cmd/serf-hub/assets/settings-appearance.js`:
  ```js
  // Settings page interactivity — appearance: theme, phone density, and
  // sidebar mode. Uses event delegation on document.body so it works even
  // when the settings partial is htmx-swapped in (inline scripts in swapped
  // content don't reliably execute across all htmx versions).
  (function () {
    "use strict";

    document.body.addEventListener("change", (e) => {
      const target = e.target;
      if (!target || !target.matches) return;

      if (target.matches('input[name="theme"]')) {
        const v = target.value;
        window.serfHub.setTheme(v === "system" ? null : v);
        if (window.SerfToast) window.SerfToast.show("Theme: " + v, "success");
        return;
      }

      if (target.matches('input[name="phone-density"]')) {
        const v = target.value;
        localStorage.setItem("serf-hub.phone-density", v);
        document.body.dataset.phoneDensity = v;
        return;
      }

      if (target.matches('input[name="sidebar-mode"]')) {
        const rail = target.value === "rail";
        localStorage.setItem("serf-hub.sidebar.rail", String(rail));
        if (rail) document.body.dataset.sidebarRail = "";
        else delete document.body.dataset.sidebarRail;
        return;
      }
    });

    // Reflect current theme/density/sidebar-mode prefs whenever a settings
    // pane is swapped in. htmx:afterSwap fires for the workspace swap; we
    // detect the panel's radios and check the right one.
    function applyAppearanceState() {
      const themeRadios = document.querySelectorAll('input[name="theme"]');
      if (themeRadios.length) {
        const current = localStorage.getItem("serf-hub.theme") || "system";
        themeRadios.forEach((r) => { r.checked = r.value === current; });
      }
      const phoneDensityRadios = document.querySelectorAll('input[name="phone-density"]');
      if (phoneDensityRadios.length) {
        const stored = localStorage.getItem("serf-hub.phone-density") || "compact";
        phoneDensityRadios.forEach((r) => { r.checked = r.value === stored; });
      }
      const sidebarModeRadios = document.querySelectorAll('input[name="sidebar-mode"]');
      if (sidebarModeRadios.length) {
        const stored = localStorage.getItem("serf-hub.sidebar.rail") === "true" ? "rail" : "pane";
        sidebarModeRadios.forEach((r) => { r.checked = r.value === stored; });
      }
    }

    document.addEventListener("DOMContentLoaded", applyAppearanceState);
    document.body.addEventListener("htmx:afterSwap", applyAppearanceState);
  })();

  // Phone density — apply stored value to body on every page load.
  (function () {
    const KEY = "serf-hub.phone-density";
    const stored = localStorage.getItem(KEY) || "compact";
    document.body.dataset.phoneDensity = stored;
  })();

  // Sidebar mode (pane / rail) — apply stored value to body on every page load.
  (function () {
    const KEY = "serf-hub.sidebar.rail";
    const rail = localStorage.getItem(KEY) === "true";
    if (rail) document.body.dataset.sidebarRail = "";
    else delete document.body.dataset.sidebarRail;
  })();
  ```
  Delete the theme/phone-density/sidebar-mode branches from the `change` listener, the theme/phone-density/sidebar-mode blocks from `applySettingsState`, and the two standalone IIFEs (`settings.js:154-159`, `:161-167`) from `settings.js`.
  In `cmd/serf-hub/templates/app.html`, insert `  <script src="/assets/settings-appearance.js{{assetv}}"></script>` immediately before the `settings.js` line.
- [ ] **Run** `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-settings-appearance.js` → pass.
- [ ] **Run** `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh` → all `OK`. `go test ./cmd/serf-hub/... -count=1` → green.
- [ ] **Commit** — `git add cmd/serf-hub/assets/settings.js cmd/serf-hub/assets/settings-appearance.js cmd/serf-hub/templates/app.html cmd/serf-hub/jstest/test-settings-appearance.js` → `refactor(hub-web): extract theme/phone-density/sidebar-mode into settings-appearance.js`.

## Task 3 — Extract `settings-notifications.js` (the four `data-notif` toggles)

Moves the `data-notif` branch of the `change` listener (including the async OS-permission flow) and the notif slice of `applySettingsState`, from the combined IIFE at `settings.js:4-152`. This is the file Track A will extend with `loudScope`. Untested today. Named to parallel `settings-appearance.js`/`settings-transcript.js` (mirrors the `notifications.html` section name) — distinct from the existing `assets/notifications.js`, which is unrelated runtime attention/badge/OS-alert logic that reads the same `serf-hub.notifications` localStorage key but is not part of the settings pane's DOM wiring and is untouched by this track.

- [ ] **Failing test** — create `cmd/serf-hub/jstest/test-settings-notifications.js`:
  ```js
  const fs = require("fs");
  const { JSDOM } = require("jsdom");

  const SRC = fs.readFileSync("../assets/settings-notifications.js", "utf8");

  function assert(cond, msg) {
    if (!cond) {
      console.error("FAIL: " + msg);
      process.exit(1);
    }
  }

  function change(input) {
    input.dispatchEvent(new input.ownerDocument.defaultView.Event("change", { bubbles: true }));
  }

  function makeWindow() {
    const dom = new JSDOM(`<!DOCTYPE html><html><body>
      <dl class="settings-table" data-notif-form>
        <div class="row editable">
          <dt id="lbl-notif-title">Title bar count</dt>
          <dd><label class="val-toggle"><input type="checkbox" data-notif="title" aria-labelledby="lbl-notif-title"><span class="state" aria-hidden="true">OFF</span></label></dd>
        </div>
        <div class="row editable">
          <dt id="lbl-notif-os">OS notification</dt>
          <dd><label class="val-toggle"><input type="checkbox" data-notif="os" aria-labelledby="lbl-notif-os"><span class="state" aria-hidden="true">OFF</span></label></dd>
        </div>
      </dl>
    </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "https://test.local/settings/notifications" });
    const { window } = dom;
    window.SerfToast = { messages: [], show(message, kind) { this.messages.push({ message, kind }); } };
    return window;
  }

  (async function main() {
    // --- simple (non-OS) toggle commits immediately ---
    const window = makeWindow();
    window.eval(SRC);
    const changedEvents = [];
    window.document.addEventListener("serf-hub:notifications-changed", (e) => changedEvents.push(e.detail));

    const title = window.document.querySelector('[data-notif="title"]');
    title.checked = true;
    change(title);
    assert(JSON.parse(window.localStorage.getItem("serf-hub.notifications")).title === true, "title toggle persists to localStorage");
    assert(title.parentElement.querySelector(".state").textContent === "ON", "title toggle updates the ON/OFF label");
    assert(changedEvents.some(d => d.key === "title" && d.value === true), "title toggle dispatches serf-hub:notifications-changed");
    assert(window.SerfToast.messages.some(m => m.kind === "success"), "committed toggle shows a success toast");

    // --- OS toggle: browser grants permission ---
    const grant = makeWindow();
    grant.eval(SRC);
    grant.Notification = { permission: "default", requestPermission: () => Promise.resolve("granted") };
    const os = grant.document.querySelector('[data-notif="os"]');
    os.checked = true;
    change(os);
    await new Promise((r) => setTimeout(r, 0));
    assert(JSON.parse(grant.localStorage.getItem("serf-hub.notifications")).os === true, "OS toggle persists once permission is granted");
    assert(os.checked === true, "OS checkbox stays checked once permission is granted");
    assert(os.parentElement.querySelector(".state").textContent === "ON", "OS toggle label reflects the granted permission");

    // --- OS toggle: browser denies permission ---
    const deny = makeWindow();
    deny.eval(SRC);
    deny.Notification = { permission: "default", requestPermission: () => Promise.resolve("denied") };
    const os2 = deny.document.querySelector('[data-notif="os"]');
    os2.checked = true;
    change(os2);
    await new Promise((r) => setTimeout(r, 0));
    assert(os2.checked === false, "OS checkbox reverts to unchecked when permission is denied");
    assert(!JSON.parse(deny.localStorage.getItem("serf-hub.notifications") || "{}").os, "denied OS toggle does not persist as on");
    assert(os2.parentElement.querySelector(".state").textContent === "OFF", "denied OS toggle label reverts to OFF");
    assert(deny.SerfToast.messages.some(m => m.kind === "warning"), "denied OS permission shows a warning toast");

    // --- restore on (re)load ---
    const restored = makeWindow();
    restored.localStorage.setItem("serf-hub.notifications", JSON.stringify({ title: true }));
    restored.eval(SRC);
    restored.document.dispatchEvent(new restored.Event("DOMContentLoaded", { bubbles: true }));
    assert(restored.document.querySelector('[data-notif="title"]').checked, "restore checks a previously-saved toggle");
    assert(restored.document.querySelector('[data-notif="os"]').checked === false, "restore leaves an unset toggle unchecked");

    console.log("PASS — settings-notifications toggle commit, OS-permission flow, and restore");
  })();
  ```
  Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-settings-notifications.js` → expect **FAIL** (ENOENT).
- [ ] **Implement** — create `cmd/serf-hub/assets/settings-notifications.js`:
  ```js
  // Settings page interactivity — notification toggles (title bar count,
  // favicon dot, OS notification, sound). Uses event delegation on
  // document.body so it works even when the settings partial is
  // htmx-swapped in (inline scripts in swapped content don't reliably
  // execute across all htmx versions).
  //
  // Not to be confused with assets/notifications.js, which drives the
  // runtime title/favicon/OS/sound alerting itself and reads the same
  // localStorage prefs this file writes.
  (function () {
    "use strict";

    document.body.addEventListener("change", (e) => {
      const target = e.target;
      if (!target || !target.matches) return;

      if (target.matches("input[type=checkbox][data-notif]")) {
        const key = target.dataset.notif;
        const desired = target.checked;

        // commit is the "yes the toggle stuck" finisher: persist prefs,
        // update the visible ON/OFF label, fire the change event, and toast.
        // It is split out so the OS-notification branch can defer it until
        // the browser permission prompt resolves (we don't want a success
        // toast or ON label for a setting the browser is about to deny).
        const commit = () => {
          const cur = readNotifPrefs();
          cur[key] = desired;
          writeNotifPrefs(cur);
          syncToggleState(target);
          document.dispatchEvent(new CustomEvent("serf-hub:notifications-changed", {
            detail: { key, value: desired },
          }));
          if (window.SerfToast) window.SerfToast.show("Settings saved", "success");
        };

        // revertToOff undoes a not-yet-committed OS toggle when the browser
        // denies the permission request. We use the same syncToggleState
        // path so the label stays in sync with the checkbox — the previous
        // code path left an "ON" label next to an unchecked box.
        const revertToOff = (reason) => {
          target.checked = false;
          const cur = readNotifPrefs();
          cur[key] = false;
          writeNotifPrefs(cur);
          syncToggleState(target);
          if (reason && window.SerfToast) window.SerfToast.show(reason, "warning");
        };

        if (key === "os" && desired && "Notification" in window && Notification.permission === "default") {
          Notification.requestPermission()
            .then((perm) => {
              if (perm === "granted") commit();
              else revertToOff("Browser denied notification permission");
            })
            .catch(() => revertToOff(""));
          return;
        }
        commit();
        return;
      }
    });

    // Reflect current notification prefs whenever a settings pane is swapped
    // in. htmx:afterSwap fires for the workspace swap; we detect the
    // panel's checkboxes and check the right boxes.
    function applyNotifState() {
      const notifBoxes = document.querySelectorAll("input[type=checkbox][data-notif]");
      if (notifBoxes.length) {
        const prefs = readNotifPrefs();
        notifBoxes.forEach((b) => { b.checked = !!prefs[b.dataset.notif]; syncToggleState(b); });
      }
    }

    function syncToggleState(input) {
      const span = input.parentElement.querySelector(".state");
      if (span) span.textContent = input.checked ? "ON" : "OFF";
    }

    function readNotifPrefs() {
      try { return JSON.parse(localStorage.getItem("serf-hub.notifications") || "{}"); }
      catch (e) { return {}; }
    }
    function writeNotifPrefs(prefs) {
      localStorage.setItem("serf-hub.notifications", JSON.stringify(prefs));
    }

    document.addEventListener("DOMContentLoaded", applyNotifState);
    document.body.addEventListener("htmx:afterSwap", applyNotifState);
  })();
  ```
  Delete the `data-notif` branch from the `change` listener, the notif block from `applySettingsState`, and the (now-unused-elsewhere) `readNotifPrefs`/`writeNotifPrefs` helpers from `settings.js`. Leave `syncToggleState` in `settings.js` for now — Task 4 still needs it; it becomes a duplicate small helper in each of the two files that use it (deliberate: keeps `settings-notifications.js` and `settings-transcript.js` fully self-contained, no inter-file load-order dependency for a 4-line helper).
  In `cmd/serf-hub/templates/app.html`, insert `  <script src="/assets/settings-notifications.js{{assetv}}"></script>` immediately before the `settings.js` line.
- [ ] **Run** `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-settings-notifications.js` → pass.
- [ ] **Run** `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh` → all `OK`. `go test ./cmd/serf-hub/... -count=1` → green.
- [ ] **Commit** — `git add cmd/serf-hub/assets/settings.js cmd/serf-hub/assets/settings-notifications.js cmd/serf-hub/templates/app.html cmd/serf-hub/jstest/test-settings-notifications.js` → `refactor(hub-web): extract notification toggles into settings-notifications.js`.

## Task 4 — Extract `settings-transcript.js`; port the existing `test-settings.js`

Moves the `data-transcript-status` branch and its `applySettingsState` slice — the last piece of the original combined IIFE — into its own file. Unlike Tasks 1-3, this behavior **is** already tested (`test-settings.js`), so this task is the one true "port": retarget the existing test's require path rather than writing new assertions.

- [ ] **Port the existing test first** — in `cmd/serf-hub/jstest/test-settings.js`, change line 4 from:
  ```js
  const SRC = fs.readFileSync("../assets/settings.js", "utf8");
  ```
  to:
  ```js
  const SRC = fs.readFileSync("../assets/settings-transcript.js", "utf8");
  ```
  (no other change to the file). Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-settings.js` → expect **FAIL** (ENOENT: `settings-transcript.js` doesn't exist yet).
- [ ] **Implement** — create `cmd/serf-hub/assets/settings-transcript.js`:
  ```js
  // Settings page interactivity — transcript system-status toggles. Uses
  // event delegation on document.body so it works even when the settings
  // partial is htmx-swapped in (inline scripts in swapped content don't
  // reliably execute across all htmx versions).
  (function () {
    "use strict";

    const transcriptStatusPrefsKey = "serf-hub.transcript.systemStatus";

    document.body.addEventListener("change", (e) => {
      const target = e.target;
      if (!target || !target.matches) return;

      if (target.matches("input[type=checkbox][data-transcript-status]")) {
        const key = target.dataset.transcriptStatus;
        const cur = readTranscriptStatusPrefs();
        cur[key] = target.checked;
        writeTranscriptStatusPrefs(cur);
        syncToggleState(target);
        document.dispatchEvent(new CustomEvent("serf-hub:transcript-system-status-changed", {
          detail: { key, value: target.checked },
        }));
        if (window.SerfToast) window.SerfToast.show("Settings saved", "success");
        return;
      }
    });

    // Reflect current transcript-status prefs whenever a settings pane is
    // swapped in. htmx:afterSwap fires for the workspace swap; we detect the
    // panel's checkboxes and check the right boxes.
    function applyTranscriptState() {
      const transcriptBoxes = document.querySelectorAll("input[type=checkbox][data-transcript-status]");
      if (transcriptBoxes.length) {
        const prefs = readTranscriptStatusPrefs();
        transcriptBoxes.forEach((b) => { b.checked = prefs[b.dataset.transcriptStatus] === true; syncToggleState(b); });
      }
    }

    function syncToggleState(input) {
      const span = input.parentElement.querySelector(".state");
      if (span) span.textContent = input.checked ? "ON" : "OFF";
    }

    function readTranscriptStatusPrefs() {
      try { return JSON.parse(localStorage.getItem(transcriptStatusPrefsKey) || "{}"); }
      catch (e) { return {}; }
    }
    function writeTranscriptStatusPrefs(prefs) {
      localStorage.setItem(transcriptStatusPrefsKey, JSON.stringify(prefs));
    }

    document.addEventListener("DOMContentLoaded", applyTranscriptState);
    document.body.addEventListener("htmx:afterSwap", applyTranscriptState);
  })();
  ```
  Delete the entire remaining combined IIFE (originally `settings.js:4-152`, now just the transcript-status branch/restore/helpers/`syncToggleState`/registration) from `settings.js` — after this, `settings.js` contains only the model-display IIFE (Task 5 handles that).
  In `cmd/serf-hub/templates/app.html`, insert `  <script src="/assets/settings-transcript.js{{assetv}}"></script>` immediately before the `settings.js` line.
- [ ] **Run** `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-settings.js` → pass.
- [ ] **Run** `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh` → all `OK`. `go test ./cmd/serf-hub/... -count=1` → green.
- [ ] **Commit** — `git add cmd/serf-hub/assets/settings.js cmd/serf-hub/assets/settings-transcript.js cmd/serf-hub/templates/app.html cmd/serf-hub/jstest/test-settings.js` → `refactor(hub-web): extract transcript-status toggles into settings-transcript.js; port test-settings.js to its new home`.

## Task 5 — Extract `model-display.js`; delete `settings.js`; finalize `app.html`

The last IIFE in `settings.js` (originally `:245-282`) has nothing to do with the settings pane — it abbreviates the model chip in the **workspace** header/composer (`[data-model-display]` in `templates/partials/workspace.html`). It only ever lived in `settings.js` by historical accident. Moving it out lets `settings.js` be deleted entirely, leaving no file whose name promises settings-pane behavior it doesn't have. Untested today (`test-abbreviate-model.js` only covers the pure `abbreviateModel` function in `spawn.js`, not this DOM-wiring loop).

- [ ] **Failing test** — create `cmd/serf-hub/jstest/test-model-display.js`:
  ```js
  const fs = require("fs");
  const { JSDOM } = require("jsdom");

  const SRC = fs.readFileSync("../assets/model-display.js", "utf8");

  function assert(cond, msg) {
    if (!cond) {
      console.error("FAIL: " + msg);
      process.exit(1);
    }
  }

  function makeWindow(bodyHTML) {
    const dom = new JSDOM(`<!DOCTYPE html><html><body>${bodyHTML}</body></html>`, {
      runScripts: "outside-only", pretendToBeVisual: true, url: "https://test.local/",
    });
    return dom.window;
  }

  (function main() {
    const window = makeWindow('<span data-model-display>anthropic/claude-haiku-4-5-20251001</span>');
    window.SerfSpawn = { abbreviateModel: (full) => full.replace(/^[^/]+\//, "").replace(/-\d{8}$/, "") };
    window.eval(SRC);
    window.document.dispatchEvent(new window.Event("DOMContentLoaded", { bubbles: true }));

    const el = window.document.querySelector("[data-model-display]");
    assert(el.textContent === "claude-haiku-4-5", "model chip abbreviates on load");
    assert(el.dataset.fullModel === "anthropic/claude-haiku-4-5-20251001", "full model id anchored in data-full-model");

    // A later htmx swap must not re-abbreviate an already-shortened string,
    // and must keep using the anchored full id (not the now-abbreviated textContent).
    window.document.body.dispatchEvent(new window.CustomEvent("htmx:afterSwap", { bubbles: true, detail: { target: window.document.body } }));
    assert(el.textContent === "claude-haiku-4-5", "re-swap keeps the abbreviated text stable");
    assert(el.dataset.fullModel === "anthropic/claude-haiku-4-5-20251001", "re-swap does not overwrite the anchored full id");

    // No SerfSpawn yet (script loaded before spawn.js resolves) — must no-op,
    // not throw.
    const early = makeWindow('<span data-model-display>anthropic/claude-opus-4-20250101</span>');
    early.eval(SRC);
    early.document.dispatchEvent(new early.Event("DOMContentLoaded", { bubbles: true }));
    assert(early.document.querySelector("[data-model-display]").textContent === "anthropic/claude-opus-4-20250101", "missing SerfSpawn leaves the raw id untouched, no throw");

    console.log("PASS — model-display abbreviation wiring survives extraction from settings.js");
  })();
  ```
  Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-model-display.js` → expect **FAIL** (ENOENT).
- [ ] **Implement** — create `cmd/serf-hub/assets/model-display.js`:
  ```js
  // Workspace model chip abbreviation — shorten server-rendered full model IDs
  // (e.g. "anthropic/claude-haiku-4-5-20251001") to compact display names
  // (e.g. "claude-haiku-4-5") while preserving the full ID in title for tooltip.
  // Uses data-full-model to anchor abbreviation to the original server value so
  // repeated swaps do not re-abbreviate an already-shortened string.
  //
  // Not settings-pane logic — it targets [data-model-display] chips in the
  // workspace header/composer (workspace.html), and lived in settings.js only
  // by historical accident. Moved here so settings.js could be split cleanly
  // into per-section files (2026-07 consistency sweep, Track 0).
  (function () {
    var modelAbbrevHandlerInstalled = false;

    function applyModelAbbreviations() {
      if (!window.SerfSpawn || !window.SerfSpawn.abbreviateModel) return;
      document.querySelectorAll("[data-model-display]").forEach(function (el) {
        // Populate the stable anchor attribute from the server-rendered value on
        // first encounter, before any abbreviation has been applied.
        if (!el.dataset.fullModel) {
          el.dataset.fullModel = el.textContent || "";
        }
        var full = el.dataset.fullModel;
        var abbr = window.SerfSpawn.abbreviateModel(full);
        if (abbr !== (el.textContent || "")) el.textContent = abbr;
      });
    }

    function installModelAbbrevHandler() {
      if (modelAbbrevHandlerInstalled) return;
      modelAbbrevHandlerInstalled = true;
      document.body.addEventListener("htmx:afterSwap", applyModelAbbreviations);
    }

    document.addEventListener("DOMContentLoaded", function () {
      applyModelAbbreviations();
      installModelAbbrevHandler();
    });
    // Guard for scripts that run after DOMContentLoaded has already fired.
    if (document.readyState !== "loading") {
      applyModelAbbreviations();
      installModelAbbrevHandler();
    }
  })();
  ```
  Delete `cmd/serf-hub/assets/settings.js` entirely (it is now empty of content — the model-display IIFE was its last occupant).
  In `cmd/serf-hub/templates/app.html`, replace the `<script src="/assets/settings.js{{assetv}}"></script>` line with `  <script src="/assets/model-display.js{{assetv}}"></script>`. The resulting five-line block (in order) is:
  ```html
  <script src="/assets/settings-shell.js{{assetv}}"></script>
  <script src="/assets/settings-appearance.js{{assetv}}"></script>
  <script src="/assets/settings-notifications.js{{assetv}}"></script>
  <script src="/assets/settings-transcript.js{{assetv}}"></script>
  <script src="/assets/model-display.js{{assetv}}"></script>
  ```
- [ ] **Run** `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-model-display.js` → pass.
- [ ] **Run** `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh` → all `OK`. `go test ./cmd/serf-hub/... -count=1` → green (confirms `app.html` still parses with `settings.js` gone).
- [ ] **Commit** — `git rm cmd/serf-hub/assets/settings.js && git add cmd/serf-hub/assets/model-display.js cmd/serf-hub/templates/app.html cmd/serf-hub/jstest/test-model-display.js` → `refactor(hub-web): extract model-display.js; delete now-empty settings.js`.

## Task 6 — Full verification gate

- [ ] **Run** the complete jstest suite: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh` → every test `OK`, output pristine (no unexplained stderr).
- [ ] **Run** `grep -rn "assets/settings.js" cmd/serf-hub/` → expect **no matches** (confirms every reference moved to a `settings-*.js` name).
- [ ] **Run** `git diff --stat main -- cmd/serf-hub/templates/partials/settings.html cmd/serf-hub/templates/partials/settings/ cmd/serf-hub/web.go cmd/serf-hub/web_settings.go cmd/serf-hub/embed.go` (against the branch point) → expect **empty** (zero changes — the settings-pane's rendered HTML and Go wiring are untouched, so the pane renders byte-identical to before this track).
- [ ] **Run** `go build ./... && go vet ./...` from repo root → green.
- [ ] **Run** `go test ./cmd/serf-hub/... -count=1` (root module) → green.
- [ ] **Run** `golangci-lint run ./cmd/serf-hub/...` → green.
- [ ] **Run** `make lint-naming` → green (this track adds no Go identifiers or wire fields, so this is a no-op confirmation, not a fix).
- [ ] **Review** `git status` for stray files, then confirm the final file list matches the contract table above.

---

## Handoff notes for Track A / Track C (not part of this track's tasks)

- **Track A**: your `loudScope` control's settings-pane wiring goes in `cmd/serf-hub/assets/settings-notifications.js` (the `data-notif` commit pattern this track just extracted); its persisted-pref logic (`DEFAULT_PREFS`, `migratePrefs`) stays in the existing, untouched `cmd/serf-hub/assets/notifications.js` per the design spec.
- **Track C** (font-size): add the fourth radio group to the existing `cmd/serf-hub/templates/partials/settings/theme.html` and its commit/restore branch to `cmd/serf-hub/assets/settings-appearance.js`.
- **Track C** (Enter-to-send, Show-cost): create the reserved-but-not-yet-existing "Display" section:
  1. `cmd/serf-hub/templates/partials/settings/display.html` (new `{{define "settings-content"}}` partial, same shape as `theme.html`/`notifications.html`).
  2. `cmd/serf-hub/assets/settings-display.js` (new file, same delegated-listener shape as `settings-appearance.js`/`settings-notifications.js`).
  3. Add `"display"` to the `settingsSections` slice at `cmd/serf-hub/web.go:84` (also see the `settingsTmpls` loop just below it — no special-casing needed, `display` isn't `credentials` or `project`).
  4. Add one `<a class="settings-nav-link" ...>` line to `cmd/serf-hub/templates/partials/settings.html`, labeled "Display", pointing at `/settings/display` / `/_partials/settings/display`.
  5. Add `"display"` to `SECTION_LABELS` in `cmd/serf-hub/assets/notifications.js` (the section-label map some copy reads — currently already missing `transcript`, `plugins-manager`, and `credentials` from the shipped section list; worth fixing while there, or file as its own quick-win if out of scope for Track C).
  6. Add the two `<script>` tags for `settings-display.js` (and any CSS) to `cmd/serf-hub/templates/app.html`, immediately after `settings-transcript.js` (or wherever Track C prefers in the by-then-current list).
