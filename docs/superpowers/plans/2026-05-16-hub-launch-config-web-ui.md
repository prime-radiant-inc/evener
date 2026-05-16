# Hub Launch Config — Web UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire the hub's embedded web UI to the launch-config and credentials RPCs introduced by the Backend plan. Users gain (a) form-based editing of global and per-project layers under `/settings`, (b) a credentials page with one row per provider, (c) an Advanced disclosure on `/new` for per-launch overrides, and (d) live updates via `serf/auth/updated` and `serf/launch/updated` SSE events.

**Architecture:** Reuses the existing HTMX-driven `/settings/<tab>` machinery (each tab is a partial template). Adds three new tabs (Launch, In-Repo) and adapts four existing tabs (Providers, Plugins, Skills, MCP). New `/credentials` route uses the same partial pattern. `assets/launchconfig.js` is a tiny RPC helper; existing `appwire.js` carries SSE.

**Tech Stack:** Go html/template (existing), HTMX 1.x (existing), vanilla JS (existing). No new dependencies.

**Prerequisite:** Backend plan landed — `serf/launch/*`, `serf/auth/list`, `serf/auth/apiKey/set`, and the two notifications are wired in `cmd/serf-hub/app_rpc.go`.

**Spec:** `docs/superpowers/specs/2026-05-16-hub-serf-launch-config-design.md`

---

## File Structure

**New files**

- `cmd/serf-hub/assets/launchconfig.js` — RPC helper: `resolve`, `getLayer`, `setLayer`, `trustRepo`, `authList`, `authApiKeySet`, `authLogout`
- `cmd/serf-hub/templates/partials/settings/launch.html` — global / project / in-repo tab combined
- `cmd/serf-hub/templates/partials/credentials.html` — credentials route
- `cmd/serf-hub/web_launchconfig.go` — HTTP handlers for the new partials and JSON endpoints (`/api/launch/*`, `/api/credentials/*` thin wrappers around RPCs)
- `cmd/serf-hub/web_launchconfig_test.go` — Go-side HTTP tests
- `cmd/serf-hub/jstest/launchconfig.spec.js` — JS form-render tests (if jstest harness is present)

**Modified files**

- `cmd/serf-hub/web.go` — add routes, extend `spawnRequest` with `launch_overrides`, register new partial handlers
- `cmd/serf-hub/templates/partials/spawn.html` — add Advanced disclosure
- `cmd/serf-hub/templates/partials/settings.html` — add Launch and In-Repo nav entries; mark Plugins/Skills/MCP entries as launch-config sub-views
- `cmd/serf-hub/templates/partials/settings/plugins.html` — replace stub with editable plugin_dirs form
- `cmd/serf-hub/templates/partials/settings/skills.html` — replace stub with editable skills_dirs form
- `cmd/serf-hub/templates/partials/settings/mcp.html` — replace stub with editable mcps + mcp_configs form
- `cmd/serf-hub/templates/partials/settings/providers.html` — show provider rows with auth modes, add `/credentials` link
- `cmd/serf-hub/assets/spawn.js` — extend new-thread form to read Advanced fields and submit them as `launch_overrides`
- `cmd/serf-hub/assets/style.css` — minor additions for the new forms
- `cmd/serf-hub/assets/notifications.js` — handle `serf/auth/updated` and `serf/launch/updated` to refresh open partials

---

## Task 1 — JS RPC helper module

**Files:**
- Create: `cmd/serf-hub/assets/launchconfig.js`

- [ ] **Step 1: Inspect the existing appwire RPC pattern**

```bash
grep -n "appwireRequest\|callRPC\|fetchRPC\|function call" cmd/serf-hub/assets/appwire.js | head -10
```

You should find an exported `appwire.request(method, params)` or similar JSON-RPC sender. Use it.

- [ ] **Step 2: Write the module**

`cmd/serf-hub/assets/launchconfig.js`:

```js
// launchconfig.js — thin wrappers around serf/launch/* and serf/auth/*
// RPCs. Re-uses appwire.request from appwire.js.
(function (global) {
  const { request } = global.appwire;

  global.launchconfig = {
    resolve: (cwd, overrides) =>
      request("serf/launch/resolve", { cwd, launchOverrides: overrides || undefined }),
    getLayer: (cwd, layer) => request("serf/launch/getLayer", { cwd, layer }),
    setLayer: (cwd, layer, config) => request("serf/launch/setLayer", { cwd, layer, config }),
    trustRepo: (cwd, hash) => request("serf/launch/trustRepo", { cwd, hash }),

    authList: () => request("serf/auth/list", {}),
    authStatus: (provider) => request("serf/auth/status", { provider }),
    authApiKeySet: (provider, value) => request("serf/auth/apiKey/set", { provider, value }),
    authLoginStart: (provider) => request("serf/auth/login/start", { provider }),
    authLoginComplete: (provider, flowId, redirectUrl) =>
      request("serf/auth/login/complete", { provider, flowId, redirectUrl }),
    authLogout: (provider) => request("serf/auth/logout", { provider }),
  };
})(window);
```

- [ ] **Step 3: Add `<script src="/assets/launchconfig.js">` to `app.html`**

`cmd/serf-hub/templates/app.html`: after the existing `<script src="/assets/appwire.js"></script>` line, add:

```html
<script src="/assets/launchconfig.js"></script>
```

- [ ] **Step 4: Smoke test**

Start hub, load `/`, open browser console:
```js
launchconfig.authList()
```
Expected: a promise resolving to `{providers: [...]}`.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/launchconfig.js cmd/serf-hub/templates/app.html
git commit -m "web: launchconfig.js RPC helper"
```

---

## Task 2 — `/credentials` route

**Files:**
- Modify: `cmd/serf-hub/web.go`
- Create: `cmd/serf-hub/templates/partials/credentials.html`

- [ ] **Step 1: Add the route**

In `cmd/serf-hub/web.go` (next to the existing `/settings/` route registration):

```go
mux.HandleFunc("/credentials", s.handleCredentials)
mux.HandleFunc("/_partials/credentials", s.handleCredentialsPartial)
```

Implement the handlers (new file `cmd/serf-hub/web_launchconfig.go`):

```go
package main

import (
	"net/http"
)

func (s *WebServer) handleCredentials(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.appTmpl.ExecuteTemplate(w, "app", map[string]string{"WorkspaceURL": "/_partials/credentials"}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *WebServer) handleCredentialsPartial(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.credsTmpl.ExecuteTemplate(w, "credentials", nil); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
```

Wire `credsTmpl` into the `WebServer` struct similarly to `spawnTmpl`. Look at how `appTmpl` is initialized in the existing constructor and follow the same template-parse pattern, referencing `templates/partials/credentials.html`.

- [ ] **Step 2: Write the partial template**

`cmd/serf-hub/templates/partials/credentials.html`:

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
  <div id="credentials-rows" data-loaded="false">
    <p class="credentials-loading">Loading…</p>
  </div>
</div>
<script>
  (async function () {
    const rows = document.getElementById("credentials-rows");
    function render(list) {
      rows.innerHTML = list.providers.map(renderRow).join("");
      rows.dataset.loaded = "true";
    }
    function renderRow(p) {
      const sourceLabel = {
        file: "Configured via stored API key",
        env:  "Configured via environment variable",
        oauth: "Configured via OAuth",
        absent: "Not configured",
        none: "No credentials required",
      }[p.activeSource] || p.activeSource;
      const supportsApiKey = (p.authModes || []).includes("apiKey");
      const supportsOAuth = (p.authModes || []).includes("oauth");
      const showClear = p.activeSource === "file" || p.activeSource === "oauth";
      return `
        <div class="credentials-row" data-provider="${p.provider}">
          <div class="credentials-row-name">${p.provider}</div>
          <div class="credentials-row-status">${sourceLabel}${p.email ? " — " + p.email : ""}</div>
          <div class="credentials-row-actions">
            ${supportsApiKey ? `<button type="button" data-action="set">${p.activeSource === "file" ? "Replace key" : "Set API key"}</button>` : ""}
            ${supportsOAuth ? `<button type="button" data-action="oauth">${p.activeSource === "oauth" ? "Refresh" : "Sign in…"}</button>` : ""}
            ${showClear ? `<button type="button" data-action="clear">Clear</button>` : ""}
          </div>
        </div>
      `;
    }
    rows.addEventListener("click", async (ev) => {
      const btn = ev.target.closest("button[data-action]");
      if (!btn) return;
      const row = btn.closest(".credentials-row");
      const provider = row.dataset.provider;
      const action = btn.dataset.action;
      if (action === "set") {
        const value = prompt(`Paste the API key for ${provider} (not echoed):`);
        if (!value) return;
        await launchconfig.authApiKeySet(provider, value);
      } else if (action === "oauth") {
        const r = await launchconfig.authLoginStart(provider);
        window.open(r.url, "_blank");
        const redirectUrl = prompt("Paste the full redirect URL after sign-in:");
        if (redirectUrl) {
          await launchconfig.authLoginComplete(provider, r.flowId, redirectUrl);
        }
      } else if (action === "clear") {
        if (!confirm(`Clear credentials for ${provider}?`)) return;
        await launchconfig.authLogout(provider);
      }
      render(await launchconfig.authList());
    });
    render(await launchconfig.authList());
  })();
</script>
{{end}}
```

- [ ] **Step 3: Add CSS hooks (minimal)**

Append to `cmd/serf-hub/assets/style.css`:

```css
.credentials-pane { padding: 1rem 1.25rem; }
.credentials-help { color: var(--muted-fg, #888); }
.credentials-row { display: grid; grid-template-columns: 8rem 1fr auto; align-items: center; gap: 1rem; padding: .5rem 0; border-bottom: 1px solid var(--border-subtle, #2a2a2a); }
.credentials-row-name { font-weight: 600; }
.credentials-row-actions button { margin-left: .25rem; }
```

- [ ] **Step 4: Write a smoke test**

`cmd/serf-hub/web_launchconfig_test.go`:

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWeb_CredentialsRoute(t *testing.T) {
	s := newTestWebServer(t)
	req := httptest.NewRequest(http.MethodGet, "/credentials", nil)
	rec := httptest.NewRecorder()
	s.handleCredentials(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "/_partials/credentials") {
		t.Errorf("body did not reference the partial: %s", rec.Body.String())
	}
}

func TestWeb_CredentialsPartial(t *testing.T) {
	s := newTestWebServer(t)
	req := httptest.NewRequest(http.MethodGet, "/_partials/credentials", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialsPartial(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "credentials-rows") {
		t.Errorf("partial missing root div")
	}
}
```

(`newTestWebServer` is an existing helper. If absent, add a minimal one following the pattern in existing `web_test.go`.)

- [ ] **Step 5: Run and commit**

```bash
go test ./cmd/serf-hub/ -run TestWeb_Credentials -v
git add cmd/serf-hub/web.go cmd/serf-hub/web_launchconfig.go cmd/serf-hub/templates/partials/credentials.html cmd/serf-hub/assets/style.css cmd/serf-hub/web_launchconfig_test.go
git commit -m "web: /credentials route and partial"
```

---

## Task 3 — Replace the Providers settings tab content

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings/providers.html`

The existing partial reads from server-side `.Providers` data. We replace it with a JS-driven view that calls `launchconfig.authList()` directly so the panel reflects credential state, not just configured model lists.

- [ ] **Step 1: Replace the partial**

```html
{{define "settings-content"}}
<h2 class="settings-h2">Providers</h2>
<p class="settings-help">
  Credentials for each model provider. Edit on the
  <a href="/credentials" hx-get="/_partials/credentials" hx-target="#workspace" hx-push-url="/credentials">credentials</a>
  page.
</p>
<div id="providers-rows" data-loaded="false">
  <p>Loading…</p>
</div>
<script>
  (async function () {
    const rows = document.getElementById("providers-rows");
    const data = await launchconfig.authList();
    rows.innerHTML = data.providers.map(p => `
      <div class="settings-row">
        <div class="settings-row-title">${p.provider}
          <span class="status-pill status-${p.activeSource}">${p.activeSource}</span>
        </div>
        <div class="settings-row-meta">auth modes: ${(p.authModes || []).join(", ") || "—"}</div>
      </div>
    `).join("");
    rows.dataset.loaded = "true";
  })();
</script>
{{end}}
```

- [ ] **Step 2: Verify in a running hub**

Start hub, navigate to `/settings/providers`, confirm rows render with status pills.

- [ ] **Step 3: Commit**

```bash
git add cmd/serf-hub/templates/partials/settings/providers.html
git commit -m "web: providers settings tab reads auth/list"
```

---

## Task 4 — Plugins, Skills, MCP settings tabs read+write launch config

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings/plugins.html`
- Modify: `cmd/serf-hub/templates/partials/settings/skills.html`
- Modify: `cmd/serf-hub/templates/partials/settings/mcp.html`

All three share the same form pattern: read `serf/launch/getLayer` for the global layer, render a list of values with add/remove controls, persist with `serf/launch/setLayer`. The plan repeats the structure for each so the implementer doesn't have to cross-reference.

### Plugins

- [ ] **Step 1: Replace the partial**

```html
{{define "settings-content"}}
<h2 class="settings-h2">Plugin directories</h2>
<p class="settings-help">
  Directories serf scans for plugins at launch. Applied to every spawn.
  Per-project additions live in <a href="/settings/launch">Launch config → Project</a>.
</p>
<div id="plugins-form" data-loaded="false">
  <p>Loading…</p>
</div>
<script>
  (async function () {
    const root = document.getElementById("plugins-form");
    const cwd = window.location.pathname; // synthetic, server doesn't filter by cwd for global layer
    const current = await launchconfig.getLayer("/", "global");
    const dirs = current.pluginDirs || [];
    function render() {
      root.innerHTML = `
        <ul class="settings-list">
          ${dirs.map((d, i) => `
            <li><code>${d}</code> <button data-i="${i}" data-action="rm">remove</button></li>
          `).join("")}
        </ul>
        <form id="plugins-add">
          <input type="text" name="dir" placeholder="/absolute/path" required>
          <button type="submit">Add</button>
        </form>
      `;
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
        dirs.push(v);
        await launchconfig.setLayer("/", "global", { ...current, pluginDirs: dirs });
        e.target.dir.value = "";
        render();
      });
    }
    render();
    root.dataset.loaded = "true";
  })();
</script>
{{end}}
```

- [ ] **Step 2: Repeat for Skills (`skills.html`)**

Same structure, replace `pluginDirs` with `skillsDirs` and adjust the heading/help.

- [ ] **Step 3: MCP tab (`mcp.html`)**

```html
{{define "settings-content"}}
<h2 class="settings-h2">MCP servers</h2>
<p class="settings-help">
  MCP servers serf spawns alongside each session. Stored in the global launch layer.
</p>
<div id="mcps-form" data-loaded="false"><p>Loading…</p></div>
<script>
  (async function () {
    const root = document.getElementById("mcps-form");
    const current = await launchconfig.getLayer("/", "global");
    const mcps = current.mcps || [];
    function render() {
      root.innerHTML = `
        <ul class="settings-list">
          ${mcps.map((m, i) => `
            <li>
              <code>${m.name}</code> →
              <code>${m.command} ${(m.args || []).join(" ")}</code>
              <button data-i="${i}" data-action="rm">remove</button>
            </li>
          `).join("")}
        </ul>
        <form id="mcps-add">
          <input type="text" name="name" placeholder="name" required>
          <input type="text" name="command" placeholder="command" required>
          <input type="text" name="args" placeholder="args (space-separated)">
          <button type="submit">Add</button>
        </form>
      `;
      root.querySelectorAll("button[data-action=rm]").forEach(b => {
        b.addEventListener("click", async () => {
          mcps.splice(+b.dataset.i, 1);
          await launchconfig.setLayer("/", "global", { ...current, mcps });
          render();
        });
      });
      root.querySelector("#mcps-add").addEventListener("submit", async (e) => {
        e.preventDefault();
        const name = e.target.name.value.trim();
        const command = e.target.command.value.trim();
        const args = e.target.args.value.trim().split(/\s+/).filter(Boolean);
        mcps.push({ name, command, args });
        await launchconfig.setLayer("/", "global", { ...current, mcps });
        e.target.reset();
        render();
      });
    }
    render();
    root.dataset.loaded = "true";
  })();
</script>
{{end}}
```

- [ ] **Step 4: Verify each tab**

Open `/settings/plugins`, `/settings/skills`, `/settings/mcp`. Add, remove, refresh; values persist across reload.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/templates/partials/settings/{plugins,skills,mcp}.html
git commit -m "web: plugins/skills/mcp tabs edit global launch config"
```

---

## Task 5 — New "Launch" settings tab for scalars (model defaults, max-rounds, etc.)

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings.html`
- Create: `cmd/serf-hub/templates/partials/settings/launch.html`
- Modify: `cmd/serf-hub/web.go` (to register the new section in `validSettingsSections`)

- [ ] **Step 1: Add the new nav entry**

In `cmd/serf-hub/templates/partials/settings.html`, after the existing `Theme` entry and before `Notifications`:

```html
<a class="settings-nav-link {{if eq .Active "launch"}}active{{end}}" href="/settings/launch" hx-get="/_partials/settings/launch" hx-target="#settings-content" hx-swap="innerHTML" hx-push-url="/settings/launch">Launch defaults</a>
```

- [ ] **Step 2: Register the section**

```bash
grep -n "validSettingsSections\|knownSections\|\"general\"" cmd/serf-hub/web.go
```

Add `"launch"` to the list/map.

- [ ] **Step 3: Write the partial**

`cmd/serf-hub/templates/partials/settings/launch.html`:

```html
{{define "settings-content"}}
<h2 class="settings-h2">Launch defaults</h2>
<p class="settings-help">
  These values are applied to every spawn unless overridden by a project
  layer or per-launch.
</p>
<form id="launch-form" data-loaded="false">
  <label>Model <input type="text" name="model" placeholder="openai/gpt-5"></label>
  <label>Agent <input type="text" name="agent" placeholder="default"></label>
  <label>Reasoning effort
    <select name="reasoningEffort">
      <option value="">(default)</option>
      <option>low</option><option>medium</option><option>high</option><option>xhigh</option><option>none</option>
    </select>
  </label>
  <label>Context strategy
    <select name="contextStrategy">
      <option value="">(default)</option>
      <option>compact</option><option>recall</option><option>session-log</option><option>ooda</option>
    </select>
  </label>
  <label>Max rounds <input type="number" name="maxRounds" min="0" step="1"></label>
  <label>Max subagent depth <input type="number" name="maxSubagentDepth" min="0" step="1"></label>
  <label><input type="checkbox" name="noProjectPrompts"> Suppress .serf/prompts loading</label>
  <button type="submit">Save</button>
  <p id="launch-form-status" class="settings-help"></p>
</form>
<script>
  (async function () {
    const form = document.getElementById("launch-form");
    const status = document.getElementById("launch-form-status");
    const current = await launchconfig.getLayer("/", "global");
    form.model.value = current.model || "";
    form.agent.value = current.agent || "";
    form.reasoningEffort.value = current.reasoningEffort || "";
    form.contextStrategy.value = current.contextStrategy || "";
    form.maxRounds.value = current.maxRounds ?? "";
    form.maxSubagentDepth.value = current.maxSubagentDepth ?? "";
    form.noProjectPrompts.checked = !!current.noProjectPrompts;
    form.dataset.loaded = "true";

    form.addEventListener("submit", async (e) => {
      e.preventDefault();
      const out = { ...current,
        model: form.model.value.trim(),
        agent: form.agent.value.trim(),
        reasoningEffort: form.reasoningEffort.value || undefined,
        contextStrategy: form.contextStrategy.value || undefined,
        maxRounds: form.maxRounds.value === "" ? undefined : +form.maxRounds.value,
        maxSubagentDepth: form.maxSubagentDepth.value === "" ? undefined : +form.maxSubagentDepth.value,
        noProjectPrompts: form.noProjectPrompts.checked || undefined,
      };
      try {
        await launchconfig.setLayer("/", "global", out);
        status.textContent = "Saved at " + new Date().toLocaleTimeString();
      } catch (err) {
        status.textContent = "Error: " + err.message;
      }
    });
  })();
</script>
{{end}}
```

- [ ] **Step 4: Test in browser**

Open `/settings/launch`, edit values, submit, reload — values persist.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/templates/partials/settings.html cmd/serf-hub/templates/partials/settings/launch.html cmd/serf-hub/web.go
git commit -m "web: Launch defaults settings tab"
```

---

## Task 6 — In-Repo tab with trust prompt

**Files:**
- Create: `cmd/serf-hub/templates/partials/settings/inrepo.html`
- Modify: `cmd/serf-hub/templates/partials/settings.html`
- Modify: `cmd/serf-hub/web.go`

The In-Repo tab needs a `cwd` parameter — the user picks a project to inspect. For v1, we read the most-recently-viewed cwd from `localStorage` (set by the workspace and spawn flows).

- [ ] **Step 1: Add the nav entry**

In `settings.html`, after "Launch defaults":

```html
<a class="settings-nav-link {{if eq .Active "inrepo"}}active{{end}}" href="/settings/inrepo" hx-get="/_partials/settings/inrepo" hx-target="#settings-content" hx-swap="innerHTML" hx-push-url="/settings/inrepo">In-repo config</a>
```

Register `"inrepo"` in the valid-sections list in `web.go`.

- [ ] **Step 2: Write the partial**

`cmd/serf-hub/templates/partials/settings/inrepo.html`:

```html
{{define "settings-content"}}
<h2 class="settings-h2">In-repo config (.serf/launch.toml)</h2>
<p class="settings-help">
  Per-project launch config shipped inside the working directory. Hub
  only applies it after you confirm trust.
</p>
<label>Working dir: <input type="text" id="inrepo-cwd" placeholder="/path/to/project"></label>
<div id="inrepo-status"><p>Enter a working directory above.</p></div>
<script>
  (async function () {
    const cwdInput = document.getElementById("inrepo-cwd");
    const status = document.getElementById("inrepo-status");
    cwdInput.value = localStorage.getItem("lastCwd") || "";
    async function refresh() {
      const cwd = cwdInput.value.trim();
      if (!cwd) { status.innerHTML = "<p>Enter a working directory.</p>"; return; }
      const r = await launchconfig.resolve(cwd);
      const repo = r.repo || { trust: "absent" };
      if (repo.trust === "absent") {
        status.innerHTML = `<p>No <code>.serf/launch.toml</code> in <code>${cwd}</code>.</p>`;
        return;
      }
      const preview = repo.preview ? `<pre class="settings-code">${escapeHtml(repo.preview)}</pre>` : "";
      const noteByTrust = {
        trusted:   `<p class="settings-help">Trusted. Hash <code>${repo.hash}</code>.</p>`,
        untrusted: `<p class="settings-help">Untrusted — review and approve below.</p>`,
        changed:   `<p class="settings-help">Trusted before, but the file has changed. Review and approve again.</p>`,
        rejected:  `<p class="settings-help">Previously rejected. Trust to apply.</p>`,
      };
      const showApprove = repo.trust !== "trusted";
      status.innerHTML = `
        ${noteByTrust[repo.trust] || ""}
        ${preview}
        ${showApprove ? `<button type="button" id="approve">Trust this file</button>` : ""}
      `;
      if (showApprove) {
        document.getElementById("approve").addEventListener("click", async () => {
          await launchconfig.trustRepo(cwd, repo.hash);
          refresh();
        });
      }
    }
    function escapeHtml(s) { return s.replace(/[&<>"]/g, c => ({"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;"}[c])); }
    cwdInput.addEventListener("change", refresh);
    refresh();
  })();
</script>
{{end}}
```

- [ ] **Step 3: Test**

Create a `.serf/launch.toml` in some directory, point the cwd field at it, observe the trust prompt and approval flow.

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-hub/templates/partials/settings/inrepo.html cmd/serf-hub/templates/partials/settings.html cmd/serf-hub/web.go
git commit -m "web: in-repo trust tab"
```

---

## Task 7 — Advanced disclosure on `/new`

**Files:**
- Modify: `cmd/serf-hub/templates/partials/spawn.html`
- Modify: `cmd/serf-hub/assets/spawn.js`
- Modify: `cmd/serf-hub/web.go`

- [ ] **Step 1: Add the disclosure block to the template**

In `cmd/serf-hub/templates/partials/spawn.html` (somewhere near the bottom of the existing form, before the submit button):

```html
<details class="spawn-advanced">
  <summary>Advanced (per-launch overrides)</summary>
  <p class="settings-help">
    Tweaks applied to this launch only. Defaults come from
    <a href="/settings/launch">Launch defaults</a> and any per-project layers.
  </p>
  <label>Add skill dir <input type="text" id="ovr-skill"></label>
  <button type="button" id="ovr-skill-add">Add</button>
  <ul id="ovr-skill-list"></ul>

  <label>Add plugin dir <input type="text" id="ovr-plugin"></label>
  <button type="button" id="ovr-plugin-add">Add</button>
  <ul id="ovr-plugin-list"></ul>

  <label>Max rounds <input type="number" id="ovr-max-rounds" min="0" step="1"></label>
  <label>Context strategy
    <select id="ovr-ctx">
      <option value="">(use defaults)</option>
      <option>compact</option><option>recall</option><option>session-log</option><option>ooda</option>
    </select>
  </label>
  <button type="button" id="ovr-show-resolved">Show resolved config</button>
  <pre id="ovr-resolved-out" class="settings-code"></pre>
</details>
```

- [ ] **Step 2: Extend `spawn.js`**

Find the existing submit handler that collects form values and sends a POST to `/api/spawn`. Add Advanced collection:

```js
function collectAdvancedOverrides() {
  const skillsDirs = Array.from(document.querySelectorAll("#ovr-skill-list li")).map(li => li.dataset.value);
  const pluginDirs = Array.from(document.querySelectorAll("#ovr-plugin-list li")).map(li => li.dataset.value);
  const maxRoundsRaw = document.getElementById("ovr-max-rounds").value;
  const ctx = document.getElementById("ovr-ctx").value;
  const overrides = {};
  if (skillsDirs.length) overrides.skillsDirs = skillsDirs;
  if (pluginDirs.length) overrides.pluginDirs = pluginDirs;
  if (maxRoundsRaw !== "") overrides.maxRounds = +maxRoundsRaw;
  if (ctx) overrides.contextStrategy = ctx;
  return Object.keys(overrides).length ? overrides : undefined;
}

// In the existing submit body, where the POST body is built:
const body = {
  prompt, harness, model, working_dir, branch, access_mode, agent, reasoning_effort,
  launch_overrides: collectAdvancedOverrides(),
};
```

(Adapt the variable names to whatever the existing handler uses. The point: pass a `launch_overrides` key with the JS structure mirroring `appwire.LaunchConfigLayer`.)

Add the +Add and resolved-preview handlers right after the existing init:

```js
function attachListAdd(inputId, addBtnId, listId) {
  document.getElementById(addBtnId).addEventListener("click", () => {
    const input = document.getElementById(inputId);
    const v = input.value.trim();
    if (!v) return;
    const li = document.createElement("li");
    li.dataset.value = v;
    li.textContent = v;
    const rm = document.createElement("button");
    rm.type = "button";
    rm.textContent = "remove";
    rm.addEventListener("click", () => li.remove());
    li.appendChild(rm);
    document.getElementById(listId).appendChild(li);
    input.value = "";
  });
}
attachListAdd("ovr-skill", "ovr-skill-add", "ovr-skill-list");
attachListAdd("ovr-plugin", "ovr-plugin-add", "ovr-plugin-list");

document.getElementById("ovr-show-resolved").addEventListener("click", async () => {
  const cwd = document.querySelector("[name=working_dir]").value;
  const overrides = collectAdvancedOverrides();
  const r = await launchconfig.resolve(cwd, overrides);
  document.getElementById("ovr-resolved-out").textContent = JSON.stringify(r, null, 2);
});
```

- [ ] **Step 3: Extend `spawnRequest` server-side**

In `cmd/serf-hub/web.go`, extend `spawnRequest`:

```go
type spawnRequest struct {
	Prompt          string                     `json:"prompt"`
	Harness         string                     `json:"harness"`
	Model           string                     `json:"model"`
	WorkingDir      string                     `json:"working_dir"`
	Branch          string                     `json:"branch"`
	AccessMode      string                     `json:"access_mode"`
	Agent           string                     `json:"agent"`
	ReasoningEffort string                     `json:"reasoning_effort"`
	LaunchOverrides *appwire.LaunchConfigLayer `json:"launch_overrides,omitempty"`
}
```

In `handleApiSpawn`, pass the overrides through:

```go
resp, err := hubThreadStart(r.Context(), s.cfg, s.sources, appwire.ThreadStartParams{
	Harness:         req.Harness,
	CWD:             req.WorkingDir,
	Prompt:          req.Prompt,
	Model:           req.Model,
	Profile:         req.Agent,
	ReasoningEffort: req.ReasoningEffort,
	LaunchOverrides: req.LaunchOverrides,
})
```

- [ ] **Step 4: Test**

Open `/new`, expand Advanced, add a skill dir, click "Show resolved config" — JSON appears showing the override in the `layers.launch` block and the merged value in `effective`.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/templates/partials/spawn.html cmd/serf-hub/assets/spawn.js cmd/serf-hub/web.go
git commit -m "web: Advanced disclosure for per-launch overrides on /new"
```

---

## Task 8 — Wire SSE notifications

**Files:**
- Modify: `cmd/serf-hub/assets/notifications.js`

- [ ] **Step 1: Inspect existing notification handling**

```bash
grep -n "notification\|onNotify\|EventSource\|onmessage" cmd/serf-hub/assets/notifications.js
```

- [ ] **Step 2: Add handlers for the two new methods**

Append to `notifications.js`:

```js
appwire.subscribe("serf/auth/updated", () => {
  // Reload credentials page if open.
  const credsRows = document.getElementById("credentials-rows");
  if (credsRows) {
    launchconfig.authList().then(list => {
      const evt = new CustomEvent("credentials-reload", { detail: list });
      credsRows.dispatchEvent(evt);
    });
  }
  // Refresh providers settings tab if visible.
  const provRows = document.getElementById("providers-rows");
  if (provRows && provRows.dataset.loaded === "true") {
    provRows.dataset.loaded = "false";
    htmx.ajax("GET", "/_partials/settings/providers", "#settings-content");
  }
});

appwire.subscribe("serf/launch/updated", () => {
  // Reload whatever settings tab is open under /settings/.
  const path = window.location.pathname;
  if (path.startsWith("/settings/")) {
    htmx.ajax("GET", "/_partials" + path, "#settings-content");
  }
});
```

(Reference the credentials partial's render function so it picks up the event — easiest fix is to register the `credentials-reload` listener inside the existing IIFE there.)

- [ ] **Step 3: Test**

Open `/credentials` in tab A, `/credentials` in tab B; set an API key in A; verify B updates without manual reload.

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-hub/assets/notifications.js cmd/serf-hub/templates/partials/credentials.html
git commit -m "web: SSE handlers for serf/auth/updated and serf/launch/updated"
```

---

## Task 9 — Per-project (hub-side) layer editor

The settings tabs in tasks 4–5 edit the **global** layer. We now add a per-project section to each tab.

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings/{plugins,skills,mcp,launch}.html`

- [ ] **Step 1: Add a cwd selector + project section to each tab**

For each tab, wrap the existing global form in a section, and add a second section below labeled "Project overrides" with the same form pattern but calling `launchconfig.getLayer(cwd, "project")` and `setLayer(cwd, "project", ...)`. The `cwd` comes from `localStorage.getItem("lastCwd")` (set by the workspace flow) and a text input lets the user change it.

Template for the project section (drop into each of the four tabs):

```html
<h3 class="settings-h3">Project overrides</h3>
<label>Working dir <input type="text" id="proj-cwd" placeholder="/path/to/project"></label>
<div id="project-section">…</div>
<script>
  (function () {
    const cwdIn = document.getElementById("proj-cwd");
    const section = document.getElementById("project-section");
    cwdIn.value = localStorage.getItem("lastCwd") || "";
    async function refresh() {
      const cwd = cwdIn.value.trim();
      if (!cwd) { section.innerHTML = "<p>Enter a working dir above.</p>"; return; }
      const layer = await launchconfig.getLayer(cwd, "project");
      // render the same controls but writing back to the "project" layer
      // ... (mirror the global form rendering inside this section) ...
    }
    cwdIn.addEventListener("change", refresh);
    refresh();
  })();
</script>
```

(The exact form repeated inside is the same pattern as the global form; copy and adjust the `setLayer(cwd, "project", ...)` call.)

- [ ] **Step 2: Test**

Open `/settings/plugins`, enter a cwd, add a project-only plugin dir, switch to `/new`, click "Show resolved config" — the project plugin dir appears in `layers.project` and contributes to the effective list.

- [ ] **Step 3: Commit**

```bash
git add cmd/serf-hub/templates/partials/settings/
git commit -m "web: per-project layer editor in plugins/skills/mcp/launch tabs"
```

---

## Task 10 — Verification pass

- [ ] **Step 1: Run all Go tests**

```bash
go test ./cmd/serf-hub/ -v
```

- [ ] **Step 2: Manual smoke through every route**

Open in a browser:
- `/credentials` — set, clear, see SSE update across tabs
- `/settings/launch` — edit scalars, save, reload, confirm persist
- `/settings/plugins`, `/settings/skills`, `/settings/mcp` — add and remove entries, switch to a project cwd, do the same in the project section
- `/settings/inrepo` — point at a directory with `.serf/launch.toml`, see the preview, click Trust, observe state → trusted
- `/settings/providers` — confirm rows render with `authModes`
- `/new` — expand Advanced, add per-launch overrides, click Show resolved config, then spawn a thread and confirm the new daemon has the overrides reflected (e.g., via `ps -eo args | grep serf` showing the flag presence)

- [ ] **Step 3: Commit any tweaks**

```bash
git add -A
git commit -m "web: launch-config UI verification fixes"
```

(If no changes were needed, skip.)

---

## Implementation Checklist Summary

- [ ] Task 1 — JS RPC helper
- [ ] Task 2 — `/credentials` route
- [ ] Task 3 — Providers tab uses `auth/list`
- [ ] Task 4 — Plugins / Skills / MCP edit global layer
- [ ] Task 5 — Launch defaults tab (scalars)
- [ ] Task 6 — In-Repo tab with TOFU prompt
- [ ] Task 7 — Advanced disclosure on `/new`
- [ ] Task 8 — SSE handlers
- [ ] Task 9 — Per-project layer editor in all tabs
- [ ] Task 10 — Verification pass
