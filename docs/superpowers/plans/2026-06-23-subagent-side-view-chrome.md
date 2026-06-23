# Subagent Side-View Chrome Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a pane-safe, standalone thread document for subagent side views so panes show an interactive thread without duplicating the global app shell/sidebar, while preserving nested open-beside behavior.

**Architecture:** Keep `/s/<id>` as the full app shell and keep `/_partials/s/<id>/workspace` as the HX-gated workspace fragment. Add a standalone authenticated thread document route (use `/thread/<encoded-ref>`) that renders a minimal HTML document including the shared workspace fragment and the required thread assets. Centralize pane-thread URL construction, normalize legacy `/s/<id>` pane hrefs, and bridge framed open-beside requests to the top-level pane host with validated `postMessage`.

**Tech Stack:** Go `net/http` + `html/template`, embedded templates/assets, HTMX, plain JavaScript modules under `cmd/serf-hub/assets`, JSDOM tests, Go `httptest` tests.

## Global Constraints

- Normal direct `/s/<id>` navigation must continue to render the full app shell with global sidebar.
- `/_partials/s/<id>/workspace` must remain HX-gated and must not become an iframe target.
- The side-pane iframe target must be a normal authenticated GET document route; use `/thread/<encoded-ref>` for this implementation.
- The standalone thread document must include transcript, live renderer, parent breadcrumb, composer/input, input status, and open-beside controls.
- The standalone thread document must exclude `#sidebar`, global search/settings/new-session chrome, and mobile sidebar hamburger/drawer controls.
- Nested open-beside from inside a framed thread document must open a pane in the top-level host, not inside the iframe.
- Legacy pane hrefs that point at `/s/<id>` must be normalized to `/thread/<id>` on open/restore/persistence paths.
- Preserve same-origin framing protection and existing auth guard behavior.
- Default tests must remain deterministic and must not require provider credentials or network access.

---

## File Structure

Modify/create these files:

- Modify `cmd/serf-hub/web.go`
  - Add `threadTmpl *template.Template` to `WebServer`.
  - Parse the new thread document template plus shared workspace/input templates.
  - Register `mux.HandleFunc("/thread/", s.handleThreadDocument)`.

- Modify `cmd/serf-hub/web_workspace.go`
  - Add `WorkspaceData` pane/thread-context fields if the type is in this file; otherwise modify its defining file.
  - Add `handleThreadDocument` and `renderThreadDocument` helpers.
  - Factor shared workspace-data preparation so workspace partial and thread document both set `HomeDir` consistently.

- Create `cmd/serf-hub/templates/thread.html`
  - Minimal standalone thread document shell.
  - Include shared head assets and thread-required scripts.
  - Render `{{template "workspace" .}}`.

- Modify `cmd/serf-hub/templates/app.html`
  - Optionally extract common asset tags to a template if that reduces duplication.
  - No behavior change for the full app shell.

- Modify `cmd/serf-hub/templates/partials/workspace.html`
  - Guard mobile sidebar hamburger behind `{{if .ShowSidebarToggle}}`.
  - In thread-document context, make parent breadcrumb pane-safe via data attributes consumed by JS.

- Modify `cmd/serf-hub/assets/panes.js`
  - Add `threadHref(ref)`, `normalizePaneHref(href)`, host message bridge, loading/error UI, and export helpers.
  - Normalize hrefs before dedupe, open, close, persist, restore, and suppression checks.

- Modify `cmd/serf-hub/assets/renderer.js`
  - Use `SerfPanes.threadHref()` or a fallback thread href builder for subagent and observer pane URLs.
  - Make open-beside controls available in framed thread documents through the bridge.
  - Intercept pane-context parent breadcrumb clicks.

- Modify `cmd/serf-hub/assets/sidebar.js`
  - Use centralized thread href builder for sidebar subagent open-beside.

- Modify tests:
  - `cmd/serf-hub/web_test.go`
  - `cmd/serf-hub/jstest/test-renderer-open-beside.js`
  - `cmd/serf-hub/jstest/test-panes-url.js`
  - Add `cmd/serf-hub/jstest/test-thread-document-bridge.js`

---

### Task 1: Standalone thread document route and template

**Files:**
- Create: `cmd/serf-hub/templates/thread.html`
- Modify: `cmd/serf-hub/web.go`
- Modify: `cmd/serf-hub/web_workspace.go`
- Modify: `cmd/serf-hub/templates/partials/workspace.html`
- Test: `cmd/serf-hub/web_test.go`

**Interfaces:**
- Consumes: existing `workspaceData(id string) WorkspaceData`, `workspaceTmpl`, `canonicalRouteID`, `appRefFromRouteID`.
- Produces:
  - Route: `GET /thread/<encoded-ref>` returns a standalone thread document without `HX-Request`.
  - Template data field: `ShowSidebarToggle bool`.
  - Template data field: `ThreadDocumentMode bool`.

- [ ] **Step 1: Add failing server tests for thread document route**

Append tests to `cmd/serf-hub/web_test.go` near existing session route tests. Use exact assertions that prove `/thread/<id>` is a full document, not an app shell and not an HX partial.

```go
func TestWeb_ThreadDocument_DirectGet_ServesChromeLessThreadDocument(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRoster(t.TempDir(), nil),
		Past:    hubcore.NewPastIndex(""),
	})

	req := httptest.NewRequest(http.MethodGet, "/thread/anysession", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<!DOCTYPE html>`,
		`<body class="thread-document"`,
		`id="conversation"`,
		`data-input-form`,
		`/assets/renderer.js`,
		`/assets/appwire.js`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("thread document missing %q in %q", want, body)
		}
	}
	for _, forbidden := range []string{
		`id="sidebar"`,
		`id="search-dialog"`,
		`data-sidebar-toggle`,
		`/_partials/sidebar`,
		`settings-link`,
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("thread document should not contain %q in %q", forbidden, body)
		}
	}
}

func TestWeb_WorkspacePartial_RemainsHXGated(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRoster(t.TempDir(), nil),
		Past:    hubcore.NewPastIndex(""),
	})

	req := httptest.NewRequest(http.MethodGet, "/_partials/s/anysession/workspace", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("workspace partial without HX should remain hidden: status=%d body=%q", rec.Code, rec.Body.String())
	}
}
```

- [ ] **Step 2: Run the new tests and verify they fail**

Run:

```bash
go test ./cmd/serf-hub -run 'TestWeb_ThreadDocument_DirectGet_ServesChromeLessThreadDocument|TestWeb_WorkspacePartial_RemainsHXGated' -count=1 -v
```

Expected:

- `TestWeb_ThreadDocument_DirectGet_ServesChromeLessThreadDocument` fails with `status: 404` because `/thread/` does not exist.
- `TestWeb_WorkspacePartial_RemainsHXGated` passes.

- [ ] **Step 3: Add thread document template**

Create `cmd/serf-hub/templates/thread.html`:

```html
{{define "thread_document"}}
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{if .Title}}{{.Title}} · {{end}}serf thread</title>
  <link rel="icon" href="data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 100'><circle cx='50' cy='50' r='40' fill='%237aa2f7'/></svg>">
  <script>
    (function () {
      var pref = localStorage.getItem("serf-hub.theme");
      if (pref === "light" || pref === "dark") {
        document.documentElement.setAttribute("data-theme", pref);
      }
    })();
  </script>
  <link rel="preconnect" href="https://fonts.googleapis.com">
  <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
  <link href="https://fonts.googleapis.com/css2?family=Hanken+Grotesk:ital,wght@0,100..900;1,100..900&family=JetBrains+Mono:ital,wght@0,100..800;1,100..800&display=swap" rel="stylesheet">
  <link rel="stylesheet" href="/assets/style.css">
</head>
<body class="thread-document" data-thread-document="true">
  <main id="workspace" class="thread-document-workspace">
    {{template "workspace" .}}
  </main>
  <div id="toast-region" aria-live="polite" aria-relevant="additions"></div>
  <script src="/assets/htmx.min.js"></script>
  <script src="/assets/toast.js"></script>
  <script src="/assets/skeleton.js"></script>
  <script src="/assets/appwire.js"></script>
  <script src="/assets/focus-trap.js"></script>
  <script src="/assets/theme.js"></script>
  <script src="/assets/composer-attachments.js"></script>
  <script src="/assets/marked.min.js"></script>
  <script src="/assets/diagnostics.js"></script>
  <script src="/assets/pending.js"></script>
  <script src="/assets/renderer-format.js"></script>
  <script src="/assets/renderer-tools.js"></script>
  <script src="/assets/renderer-panels.js"></script>
  <script src="/assets/renderer.js"></script>
  <script>
    document.body.addEventListener("htmx:responseError", function (e) {
      if (!window.SerfToast) return;
      var status = (e && e.detail && e.detail.xhr && e.detail.xhr.status) || 0;
      window.SerfToast.show(status ? ("Request failed (" + status + ")") : "Request failed", "error");
    });
    document.body.addEventListener("htmx:sendError", function () {
      if (window.SerfToast) window.SerfToast.show("Network error", "error");
    });
  </script>
</body>
</html>
{{end}}
```

- [ ] **Step 4: Parse and register the thread document template**

Modify `cmd/serf-hub/web.go`:

```go
type WebServer struct {
	cfg                 hubcore.WebConfig
	appTmpl             *template.Template
	sidebarTmpl         *template.Template
	workspaceTmpl       *template.Template
	threadTmpl          *template.Template
	spawnTmpl           *template.Template
	inputStripTmpl      *template.Template
	projectSettingsTmpl *template.Template
	settingsTmpls       map[string]*template.Template
	appRPC              *appserver.Server
	sources             *appsource.Registry
	startedAt           time.Time

	resumeMu    sync.Mutex
	resumeLocks map[string]*sync.Mutex // sessionID -> per-session lock
}
```

In `NewWebServer` after `workspaceTmpl`:

```go
threadTmpl := template.Must(template.ParseFS(templatesFS,
	"templates/thread.html",
	"templates/partials/workspace.html",
	"templates/partials/input_strip.html",
))
```

In the `WebServer` initializer:

```go
web := &WebServer{
	cfg: cfg, appTmpl: appTmpl, sidebarTmpl: sidebarTmpl,
	workspaceTmpl: workspaceTmpl, threadTmpl: threadTmpl, spawnTmpl: spawnTmpl, inputStripTmpl: inputStripTmpl,
	projectSettingsTmpl: projectSettingsTmpl,
	settingsTmpls:       settingsTmpls,
	sources:             sources,
	startedAt:           time.Now().UTC(),
	resumeLocks:         map[string]*sync.Mutex{},
}
```

In `Handler`, register before `/_partials/` is fine because `/thread/` is distinct:

```go
mux.HandleFunc("/thread/", s.handleThreadDocument)
```

- [ ] **Step 5: Add thread-context fields to workspace data**

Find the `WorkspaceData` type with:

```bash
rg -n "type WorkspaceData" cmd/serf-hub
```

Add these fields:

```go
ShowSidebarToggle bool
ThreadDocumentMode bool
```

Do not change existing JSON/API structs unless `WorkspaceData` already doubles as an API type; these fields are template-only.

- [ ] **Step 6: Render workspace partial and thread document with explicit context**

In `cmd/serf-hub/web_workspace.go`, add a helper:

```go
func (s *WebServer) workspaceDataForRender(id string) WorkspaceData {
	data := s.workspaceData(id)
	if data.ID == "" {
		return data
	}
	if data.HomeDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			data.HomeDir = home
		}
	}
	return data
}
```

Update `renderWorkspacePartial`:

```go
func (s *WebServer) renderWorkspacePartial(w http.ResponseWriter, r *http.Request, id string) {
	data := s.workspaceDataForRender(id)
	if data.ID == "" {
		http.NotFound(w, r)
		return
	}
	data.ShowSidebarToggle = true
	data.ThreadDocumentMode = false
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.workspaceTmpl.ExecuteTemplate(w, "workspace", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
```

Add route handler:

```go
func (s *WebServer) handleThreadDocument(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/thread/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	id = canonicalRouteID(id)
	s.renderThreadDocument(w, r, id)
}

func (s *WebServer) renderThreadDocument(w http.ResponseWriter, r *http.Request, id string) {
	data := s.workspaceDataForRender(id)
	if data.ID == "" {
		http.NotFound(w, r)
		return
	}
	data.ShowSidebarToggle = false
	data.ThreadDocumentMode = true
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.threadTmpl.ExecuteTemplate(w, "thread_document", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
```

- [ ] **Step 7: Guard sidebar hamburger in the workspace fragment**

Modify `cmd/serf-hub/templates/partials/workspace.html` around the hamburger:

```html
{{if .ShowSidebarToggle}}
<button type="button" class="header-hamburger" data-sidebar-toggle title="open sidebar" aria-label="open sidebar">☰</button>
{{end}}
```

- [ ] **Step 8: Run targeted tests**

Run:

```bash
go test ./cmd/serf-hub -run 'TestWeb_ThreadDocument_DirectGet_ServesChromeLessThreadDocument|TestWeb_WorkspacePartial_RemainsHXGated|TestWeb_SessionRoute_FullPage_ServesAppShell' -count=1 -v
```

Expected: all pass.

- [ ] **Step 9: Commit Task 1**

```bash
git add cmd/serf-hub/web.go cmd/serf-hub/web_workspace.go cmd/serf-hub/templates/thread.html cmd/serf-hub/templates/partials/workspace.html cmd/serf-hub/web_test.go
git commit -m "feat(hub): add standalone thread document"
```

---

### Task 2: Pane-safe thread URL builder and legacy href normalization

**Files:**
- Modify: `cmd/serf-hub/assets/panes.js`
- Modify: `cmd/serf-hub/assets/renderer.js`
- Modify: `cmd/serf-hub/assets/sidebar.js`
- Test: `cmd/serf-hub/jstest/test-renderer-open-beside.js`
- Test: `cmd/serf-hub/jstest/test-panes-url.js`

**Interfaces:**
- Consumes: Task 1 route `/thread/<encoded-ref>`.
- Produces JS API:
  - `SerfPanes.threadHref(ref: string): string`
  - `SerfPanes.normalizePaneHref(href: string): string`

- [ ] **Step 1: Add failing tests for thread href construction and restore normalization**

In `cmd/serf-hub/jstest/test-renderer-open-beside.js`, change the expected URL for subagent open-beside from `/s/<ref>` to `/thread/<ref>`. Add a source-qualified ref case:

```js
await scenario("subagent open-beside uses thread document route", { panes: true }, [
  ["JOB_FINISHED", { jobId: "job_A", jobType: "delegate", status: "completed", transcriptRef: "local:child-A", outputBytes: 10 }],
], ({ window, conv }) => {
  const btn = conv.querySelector(".open-beside-btn");
  if (!btn) return { ok: false, detail: "missing open-beside button" };
  btn.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
  const calls = window.__paneOpenCalls || [];
  if (calls.length !== 1) return { ok: false, detail: "expected one pane open, got " + calls.length };
  if (calls[0].href !== "/thread/local%3Achild-A") return { ok: false, detail: "wrong href: " + calls[0].href };
  return { ok: true };
});
```

If the harness currently stubs `SerfPanes.open` without `threadHref`, update the stub:

```js
window.SerfPanes = {
  open: (href, title) => { window.__paneOpenCalls.push({ href, title }); },
  threadHref: ref => "/thread/" + encodeURIComponent(ref),
};
```

In `cmd/serf-hub/jstest/test-panes-url.js`, add a restore normalization scenario:

```js
await scenario("restore normalizes legacy /s session pane hrefs to thread documents", ({ window, document }) => {
  window.history.replaceState(null, "", "/s/parent?pane=%2Fs%2Flocal%253Achild-A");
  window.SerfPanes.restore();
  const hrefs = window.SerfPanes.openHrefs();
  if (hrefs.length !== 1) return { ok: false, detail: "expected one restored pane" };
  if (hrefs[0] !== "/thread/local%3Achild-A") return { ok: false, detail: "wrong restored href: " + hrefs[0] };
  return { ok: true };
});
```

Adapt to the existing `test-panes-url.js` helper style rather than adding a second incompatible harness.

- [ ] **Step 2: Run JS tests and verify they fail**

Run:

```bash
cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-renderer-open-beside.js
cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-panes-url.js
```

Expected: failures show old `/s/...` hrefs or missing `threadHref`/normalization.

- [ ] **Step 3: Implement URL helpers in `panes.js`**

Add near the top of `cmd/serf-hub/assets/panes.js`:

```js
function threadHref(ref) {
  ref = String(ref || "").trim();
  return ref ? "/thread/" + encodeURIComponent(ref) : "";
}

function normalizePaneHref(href) {
  href = String(href || "").trim();
  if (!href) return "";
  try {
    var u = new URL(href, window.location.origin);
    if (u.origin !== window.location.origin) return href;
    if (u.pathname.indexOf("/thread/") === 0) return u.pathname + u.search + u.hash;
    if (u.pathname.indexOf("/s/") === 0) {
      var rest = u.pathname.slice(3);
      if (rest && rest.indexOf("/") === -1) {
        return threadHref(decodeURIComponent(rest));
      }
    }
  } catch (e) {
    if (href.indexOf("/thread/") === 0) return href;
    if (href.indexOf("/s/") === 0) {
      var id = href.slice(3);
      if (id && id.indexOf("/") === -1 && id.indexOf("?") === -1 && id.indexOf("#") === -1) {
        return threadHref(decodeURIComponent(id));
      }
    }
  }
  return href;
}
```

Normalize at the start of `open`, `close`, `isSuppressed`, `suppress`, and `unsuppress`:

```js
function open(href, title) {
  href = normalizePaneHref(href);
  if (!href) return null;
  // existing body...
}
```

Apply the same first line in `close`, `isSuppressed`, `suppress`, `unsuppress`.

Normalize stored and URL-restored pane hrefs:

```js
return { href: normalizePaneHref(f.getAttribute("src")), title: t ? t.textContent : "" };
```

```js
urlPanes.forEach(function (href) {
  href = normalizePaneHref(href);
  if (!href) return;
  unsuppress(href);
  open(href, href);
});
```

```js
data.forEach(function (p) {
  if (p && p.href) open(normalizePaneHref(p.href), p.title);
});
```

Export helpers:

```js
window.SerfPanes = {
  open: open,
  close: close,
  openHrefs: openHrefs,
  restore: restore,
  isSuppressed: isSuppressed,
  setSidePanesWidth: setSidePanesWidth,
  threadHref: threadHref,
  normalizePaneHref: normalizePaneHref,
  MAX_SIDE_PANES: MAX_SIDE_PANES,
  PANE_MIN: PANE_MIN,
  _persist: persist
};
```

- [ ] **Step 4: Use thread hrefs from renderer and sidebar open-beside**

In `cmd/serf-hub/assets/renderer.js`, add a helper method near navigation helpers:

```js
threadHref(ref) {
  ref = String(ref || "").trim();
  if (!ref) return "";
  if (window.SerfPanes && window.SerfPanes.threadHref) return window.SerfPanes.threadHref(ref);
  return "/thread/" + encodeURIComponent(ref);
},
```

Replace subagent open-beside href construction in `applyJobRefTarget`:

```js
return { href: this.threadHref(ref), title: label };
```

Replace observer auto-open href construction:

```js
var href = this.threadHref(refs[i]);
```

In `cmd/serf-hub/assets/sidebar.js`, change:

```js
window.SerfPanes.open("/s/" + encodeURIComponent(ref), title);
```

to:

```js
var href = window.SerfPanes.threadHref ? window.SerfPanes.threadHref(ref) : ("/thread/" + encodeURIComponent(ref));
window.SerfPanes.open(href, title);
```

- [ ] **Step 5: Run targeted JS tests**

Run:

```bash
cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-renderer-open-beside.js
cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-panes-url.js
```

Expected: both pass.

- [ ] **Step 6: Commit Task 2**

```bash
git add cmd/serf-hub/assets/panes.js cmd/serf-hub/assets/renderer.js cmd/serf-hub/assets/sidebar.js cmd/serf-hub/jstest/test-renderer-open-beside.js cmd/serf-hub/jstest/test-panes-url.js
git commit -m "fix(hub): route side panes to thread documents"
```

---

### Task 3: Host bridge for nested open-beside from framed thread documents

**Files:**
- Modify: `cmd/serf-hub/assets/panes.js`
- Modify: `cmd/serf-hub/assets/renderer.js`
- Create: `cmd/serf-hub/jstest/test-thread-document-bridge.js`

**Interfaces:**
- Consumes: Task 2 `SerfPanes.threadHref(ref)` and `SerfPanes.open(href, title)`.
- Produces:
  - `SerfPanes.openFromChild(sourceWindow: Window, href: string, title: string): Element|null`
  - `SerfPanes.isPaneSafeHref(href: string): boolean`
  - Framed child fallback `window.SerfPanes.open(href, title)` that posts to parent when no local pane host exists.

- [ ] **Step 1: Add failing bridge tests**

Create `cmd/serf-hub/jstest/test-thread-document-bridge.js`:

```js
const fs = require("fs");
const { JSDOM } = require("jsdom");

function loadScript(window, path) {
  window.eval(fs.readFileSync(path, "utf8"));
}

function newHostHarness() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <main id="workspace"></main>
    <div id="pane-splitter" hidden></div>
    <aside id="side-panes" hidden></aside>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://127.0.0.1:9180/s/parent" });
  const { window } = dom;
  loadScript(window, "../assets/panes.js");
  return window;
}

let allPass = true;
function pass(ok, msg) {
  console.log((ok ? "PASS" : "FAIL") + " — " + msg);
  if (!ok) allPass = false;
}

(function () {
  const host = newHostHarness();
  const pane = host.SerfPanes.open("/thread/local%3Achild", "child");
  const frame = pane && pane.querySelector("iframe");
  pass(!!frame, "host opens initial pane");

  const opened = host.SerfPanes.openFromChild(frame.contentWindow, "/thread/local%3Agrandchild", "grandchild");
  pass(!!opened, "known child frame can open a pane through host bridge");
  pass(host.SerfPanes.openHrefs().includes("/thread/local%3Agrandchild"), "host bridge opens requested thread href");

  const rejectedExternal = host.SerfPanes.openFromChild(frame.contentWindow, "https://example.com/thread/x", "bad");
  pass(!rejectedExternal, "host bridge rejects cross-origin hrefs");

  const unknown = { closed: false };
  const rejectedUnknown = host.SerfPanes.openFromChild(unknown, "/thread/local%3Aintruder", "intruder");
  pass(!rejectedUnknown, "host bridge rejects unknown source windows");

  if (!allPass) process.exit(1);
  console.log("OK\ttest-thread-document-bridge.js");
})();
```

- [ ] **Step 2: Run bridge test and verify it fails**

Run:

```bash
cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-thread-document-bridge.js
```

Expected: fails because `openFromChild` does not exist.

- [ ] **Step 3: Implement host validation and message bridge in `panes.js`**

Add helpers:

```js
function isKnownPaneSource(source) {
  var r = region();
  if (!r || !source) return false;
  var frames = r.querySelectorAll(".pane-frame");
  for (var i = 0; i < frames.length; i++) {
    if (frames[i].contentWindow === source) return true;
  }
  return false;
}

function isPaneSafeHref(href) {
  href = normalizePaneHref(href);
  if (!href) return false;
  try {
    var u = new URL(href, window.location.origin);
    if (u.origin !== window.location.origin) return false;
    return u.pathname.indexOf("/thread/") === 0 || u.pathname.indexOf("/doc/") === 0;
  } catch (e) {
    return href.indexOf("/thread/") === 0 || href.indexOf("/doc/") === 0;
  }
}

function openFromChild(source, href, title) {
  href = normalizePaneHref(href);
  if (!isKnownPaneSource(source)) return null;
  if (!isPaneSafeHref(href)) return null;
  return open(href, String(title || href));
}

function onMessage(e) {
  if (!e || e.origin !== window.location.origin) return;
  var data = e.data || {};
  if (data.type !== "serf:open-beside") return;
  openFromChild(e.source, data.href, data.title);
}
```

Bind the listener in `onLoad` or module initialization:

```js
window.addEventListener("message", onMessage);
```

Export:

```js
openFromChild: openFromChild,
isPaneSafeHref: isPaneSafeHref,
```

- [ ] **Step 4: Add framed fallback in `renderer.js`**

In `makeOpenBesideButton`, replace the early `return null` when `window.SerfPanes` is absent with a framed bridge fallback.

Add a method near `isFramed`/navigation helpers if not already present:

```js
openBeside(spec) {
  if (!spec || !spec.href) return;
  if (window.SerfPanes && window.SerfPanes.open) {
    window.SerfPanes.open(spec.href, spec.title);
    return;
  }
  if (this.isFramed && this.isFramed() && window.parent) {
    window.parent.postMessage({ type: "serf:open-beside", href: spec.href, title: spec.title || spec.href }, window.location.origin);
  }
},
```

Change button creation to allow framed documents:

```js
if (!window.SerfPanes && !(this.isFramed && this.isFramed())) return null;
```

Change click handler:

```js
if (spec && spec.href) this.openBeside(spec);
```

Because `openBeside` uses `this`, preserve renderer context:

```js
var self = this;
function openBeside(e) {
  e.preventDefault();
  e.stopPropagation();
  var spec = resolve();
  if (spec && spec.href) self.openBeside(spec);
}
```

- [ ] **Step 5: Run bridge and renderer open-beside tests**

Run:

```bash
cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-thread-document-bridge.js
cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-renderer-open-beside.js
```

Expected: both pass.

- [ ] **Step 6: Commit Task 3**

```bash
git add cmd/serf-hub/assets/panes.js cmd/serf-hub/assets/renderer.js cmd/serf-hub/jstest/test-thread-document-bridge.js
git commit -m "feat(hub): bridge nested pane opens from thread documents"
```

---

### Task 4: Pane-safe parent breadcrumb behavior

**Files:**
- Modify: `cmd/serf-hub/templates/partials/workspace.html`
- Modify: `cmd/serf-hub/assets/renderer.js`
- Test: `cmd/serf-hub/web_test.go`
- Test: `cmd/serf-hub/jstest/test-thread-document-bridge.js`

**Interfaces:**
- Consumes: `ThreadDocumentMode bool`, `ParentRouteID`, `SerfRenderer.openBeside(spec)` from Task 3.
- Produces: parent breadcrumb in thread-document mode opens/focuses parent as `/thread/<parent>` via host bridge rather than navigating iframe to `/s/<parent>`.

- [ ] **Step 1: Add failing server assertion for pane-safe breadcrumb markup**

In `TestWeb_ThreadDocument_DirectGet_ServesChromeLessThreadDocument`, if constructing a real subagent fixture is difficult, add a new unit test around template rendering with a `WorkspaceData` value. If `WorkspaceData` is accessible in `cmd/serf-hub`, render `threadTmpl` directly:

```go
func TestWeb_ThreadDocument_SubagentBreadcrumbUsesPaneSafeAttributes(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180"})
	data := WorkspaceData{
		ID: "local:child",
		Title: "child",
		ParentRouteID: "local:parent",
		ParentTitle: "parent",
		ThreadDocumentMode: true,
		ShowSidebarToggle: false,
	}
	rec := httptest.NewRecorder()
	if err := web.threadTmpl.ExecuteTemplate(rec, "thread_document", data); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-open-parent-beside="/thread/local%3Aparent"`) {
		t.Fatalf("breadcrumb missing pane-safe parent target: %q", body)
	}
	if strings.Contains(body, `href="/s/local:parent"`) {
		t.Fatalf("thread document breadcrumb must not target full app shell: %q", body)
	}
}
```

- [ ] **Step 2: Add failing JS assertion for breadcrumb bridge**

Extend `test-thread-document-bridge.js` with a child-document style harness that loads `renderer.js` and clicks an element with `data-open-parent-beside`:

```js
// Add after host bridge checks; adapt if helper names differ.
const childDom = new JSDOM(`<!DOCTYPE html><html><body>
  <a class="subagent-parent-up" href="/thread/local%3Aparent" data-open-parent-beside="/thread/local%3Aparent">↑ Parent</a>
  <div id="conversation" data-session-id="local:child" data-state="active"></div>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://127.0.0.1:9180/thread/local%3Achild" });
const child = childDom.window;
let posted = null;
child.parent = { postMessage: (msg, origin) => { posted = { msg, origin }; } };
child.SerfRendererInternal = host.SerfRendererInternal || {};
// If loading full renderer dependencies is too heavy, test a small exported handler added in renderer.js.
```

If the full renderer dependencies make this brittle, instead add and test an exported small helper `SerfRenderer.handlePaneParentClick(e)` through the existing renderer harness.

- [ ] **Step 3: Run tests and verify they fail**

Run:

```bash
go test ./cmd/serf-hub -run 'TestWeb_ThreadDocument_SubagentBreadcrumbUsesPaneSafeAttributes' -count=1 -v
cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-thread-document-bridge.js
```

Expected: server test fails because breadcrumb lacks `data-open-parent-beside`; JS test fails until click handling exists.

- [ ] **Step 4: Update breadcrumb template**

In `workspace.html`, change parent breadcrumb links:

```html
{{if .ThreadDocumentMode}}
<a class="subagent-parent-up" href="/thread/{{.ParentRouteID | urlquery}}" data-open-parent-beside="/thread/{{.ParentRouteID | urlquery}}">↑ Parent</a>
<a class="subagent-parent-crumb" href="/thread/{{.ParentRouteID | urlquery}}" data-open-parent-beside="/thread/{{.ParentRouteID | urlquery}}">{{.ParentTitle}}</a>
{{else}}
<a class="subagent-parent-up" href="/s/{{.ParentRouteID}}">↑ Parent</a>
<a class="subagent-parent-crumb" href="/s/{{.ParentRouteID}}">{{.ParentTitle}}</a>
{{end}}
```

If `urlquery` is not available in this template, add a template func to the template parser or use precomputed `ParentThreadHref` in `WorkspaceData`. Prefer precomputed `ParentThreadHref string` if template funcs would affect multiple templates.

- [ ] **Step 5: Add click handling in renderer**

In `renderer.js`, add a document click listener during bootstrap or `init`:

```js
bindPaneParentLinks() {
  if (this.__paneParentLinksBound) return;
  this.__paneParentLinksBound = true;
  document.addEventListener("click", (e) => {
    const a = e.target && e.target.closest && e.target.closest("[data-open-parent-beside]");
    if (!a) return;
    e.preventDefault();
    const href = a.getAttribute("data-open-parent-beside") || a.getAttribute("href") || "";
    const title = a.textContent || href;
    this.openBeside({ href, title });
  });
},
```

Call it from `init` after basic state setup:

```js
this.bindPaneParentLinks();
```

- [ ] **Step 6: Run targeted tests**

Run:

```bash
go test ./cmd/serf-hub -run 'TestWeb_ThreadDocument_SubagentBreadcrumbUsesPaneSafeAttributes|TestWeb_ThreadDocument_DirectGet_ServesChromeLessThreadDocument' -count=1 -v
cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-thread-document-bridge.js
```

Expected: all pass.

- [ ] **Step 7: Commit Task 4**

```bash
git add cmd/serf-hub/templates/partials/workspace.html cmd/serf-hub/assets/renderer.js cmd/serf-hub/web_test.go cmd/serf-hub/jstest/test-thread-document-bridge.js
git commit -m "fix(hub): make pane breadcrumbs thread-safe"
```

---

### Task 5: Pane loading and error states

**Files:**
- Modify: `cmd/serf-hub/assets/panes.js`
- Modify: `cmd/serf-hub/assets/style.css`
- Test: `cmd/serf-hub/jstest/test-panes-url.js` or create `cmd/serf-hub/jstest/test-panes-error.js`

**Interfaces:**
- Consumes: existing `SerfPanes.open(href, title)`.
- Produces DOM contract:
  - `.pane[data-state="loading"]`
  - `.pane[data-state="ready"]`
  - `.pane[data-state="error"]`
  - `.pane-error` with retry and close controls.

- [ ] **Step 1: Add failing pane error/loading tests**

Create `cmd/serf-hub/jstest/test-panes-error.js`:

```js
const fs = require("fs");
const { JSDOM } = require("jsdom");

function load(window, file) { window.eval(fs.readFileSync(file, "utf8")); }

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <main id="workspace"></main>
  <div id="pane-splitter" hidden></div>
  <aside id="side-panes" hidden></aside>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://127.0.0.1:9180/s/parent" });
const { window } = dom;
load(window, "../assets/panes.js");

let allPass = true;
function check(ok, msg) { console.log((ok ? "PASS" : "FAIL") + " — " + msg); if (!ok) allPass = false; }

const pane = window.SerfPanes.open("/thread/local%3Achild", "child");
check(pane && pane.dataset.state === "loading", "pane starts loading");
const frame = pane.querySelector("iframe");
frame.dispatchEvent(new window.Event("load"));
check(pane.dataset.state === "ready", "pane becomes ready after iframe load");

const bad = window.SerfPanes.open("/thread/local%3Amissing", "missing");
window.SerfPanes.markError("/thread/local%3Amissing", "Thread failed to load");
check(bad.dataset.state === "error", "pane can enter error state");
check(!!bad.querySelector(".pane-error"), "error state renders pane error UI");
check(!!bad.querySelector("[data-pane-retry]"), "error state renders retry control");

if (!allPass) process.exit(1);
console.log("OK\ttest-panes-error.js");
```

- [ ] **Step 2: Run and verify failure**

Run:

```bash
cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-panes-error.js
```

Expected: fails because pane state and `markError` do not exist.

- [ ] **Step 3: Implement pane loading/ready/error state**

In `panes.js`, after creating `pane`:

```js
pane.dataset.state = "loading";
```

After creating `frame`:

```js
var loadTimer = window.setTimeout(function () {
  markError(href, "Pane did not finish loading");
}, 15000);
frame.addEventListener("load", function () {
  window.clearTimeout(loadTimer);
  if (pane.dataset.state !== "error") pane.dataset.state = "ready";
});
frame.addEventListener("error", function () {
  window.clearTimeout(loadTimer);
  markError(href, "Pane failed to load");
});
```

Add:

```js
function markError(href, message) {
  href = normalizePaneHref(href);
  var pane = paneFor(href);
  if (!pane) return null;
  pane.dataset.state = "error";
  var existing = pane.querySelector(".pane-error");
  if (existing) existing.remove();
  var err = document.createElement("div");
  err.className = "pane-error";
  var text = document.createElement("div");
  text.className = "pane-error-text";
  text.textContent = message || "Pane failed to load";
  var retry = document.createElement("button");
  retry.type = "button";
  retry.className = "btn btn-secondary";
  retry.dataset.paneRetry = "";
  retry.textContent = "retry";
  retry.addEventListener("click", function () {
    var frame = pane.querySelector(".pane-frame");
    if (frame) {
      pane.dataset.state = "loading";
      err.remove();
      frame.setAttribute("src", href);
    }
  });
  err.appendChild(text);
  err.appendChild(retry);
  pane.appendChild(err);
  return pane;
}
```

Export `markError`.

- [ ] **Step 4: Add basic CSS**

In `style.css`, near pane styles:

```css
.pane[data-state="loading"] .pane-frame {
  opacity: 0.65;
}

.pane-error {
  padding: 16px;
  color: var(--text-primary);
  border-top: 1px solid var(--border-subtle);
  background: var(--surface-raised);
}

.pane-error-text {
  margin-bottom: 10px;
  color: var(--text-muted);
}
```

Use existing CSS variables; if names differ, inspect nearby pane styles and use the established variables.

- [ ] **Step 5: Run pane error test**

Run:

```bash
cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-panes-error.js
```

Expected: pass.

- [ ] **Step 6: Commit Task 5**

```bash
git add cmd/serf-hub/assets/panes.js cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-panes-error.js
git commit -m "feat(hub): show pane loading and error states"
```

---

### Task 6: Security, route encoding, and integration coverage

**Files:**
- Modify: `cmd/serf-hub/web_test.go`
- Modify: `cmd/serf-hub/jstest/run-all.sh` only if needed to include new tests automatically; it already runs `test-*.js`.
- No production code unless tests reveal gaps from earlier tasks.

**Interfaces:**
- Consumes: all prior tasks.
- Produces: deterministic regression coverage for route auth/security/encoding and full JS suite behavior.

- [ ] **Step 1: Add route encoding and security tests**

Add to `cmd/serf-hub/web_test.go`:

```go
func TestWeb_ThreadDocument_RouteEncoding(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRoster(t.TempDir(), nil),
		Past:    hubcore.NewPastIndex(""),
	})

	cases := []string{
		"local%3Achild-A",
		"codex%3Ath_active",
		"bare-session",
	}
	for _, encoded := range cases {
		t.Run(encoded, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/thread/"+encoded, nil)
			req.Host = "127.0.0.1:9180"
			rec := httptest.NewRecorder()
			web.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK && rec.Code != http.StatusNotFound {
				t.Fatalf("unexpected status for encoded route %s: %d", encoded, rec.Code)
			}
		})
	}
}

func TestWeb_ThreadDocument_SecurityHeaders(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRoster(t.TempDir(), nil),
		Past:    hubcore.NewPastIndex(""),
	})

	req := httptest.NewRequest(http.MethodGet, "/thread/anysession", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'self'") {
		t.Fatalf("thread document should preserve same-origin frame policy, CSP=%q", csp)
	}
}
```

If `anysession` can 404 in a way that skips headers, use an existing fixture/helper from nearby tests or assert headers on a known route. Do not make this test depend on live provider state.

- [ ] **Step 2: Run new Go tests**

Run:

```bash
go test ./cmd/serf-hub -run 'TestWeb_ThreadDocument' -count=1 -v
```

Expected: pass after adjusting fixture setup if needed.

- [ ] **Step 3: Run all serf-hub JS tests**

Run:

```bash
cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} ./run-all.sh
```

Expected: `jstest: all tests passed`.

If `test-renderer-compaction.js` fails once and passes alone, rerun it alone and rerun the full suite once, as observed previously. If it fails repeatedly, stop and diagnose; do not ignore repeated failures.

- [ ] **Step 4: Run focused Go package tests**

Run:

```bash
go test ./cmd/serf-hub -count=1
```

Expected: pass.

- [ ] **Step 5: Commit Task 6**

```bash
git add cmd/serf-hub/web_test.go cmd/serf-hub/jstest
git commit -m "test(hub): cover thread document pane behavior"
```

If no files changed in this task because earlier tasks already added all tests, skip the commit and record that in the handoff.

---

### Task 7: Final verification and cleanup

**Files:**
- No planned production changes.
- Update docs only if implementation decisions differ from `docs/superpowers/specs/2026-06-23-subagent-side-view-chrome-design.md`.

**Interfaces:**
- Consumes: all prior tasks.
- Produces: final verified branch ready for review.

- [ ] **Step 1: Inspect worktree**

Run:

```bash
git status --short
```

Expected: no unstaged tracked changes. Pre-existing untracked `.private-journal/...` and `docs/superpowers/plans/2026-05-07-serf-daemon-prereqs.md` may remain; do not stage them.

- [ ] **Step 2: Run full targeted verification**

Run:

```bash
go test ./cmd/serf-hub -count=1
cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} ./run-all.sh
```

Expected:

- `go test ./cmd/serf-hub -count=1` exits 0.
- `./run-all.sh` prints `jstest: all tests passed`.

- [ ] **Step 3: Inspect final diff from branch point**

Run:

```bash
git log --oneline --decorate -8
git diff --stat HEAD~7..HEAD
```

If the number of implementation commits differs, adjust `HEAD~7` to cover only this feature’s commits.

Expected: changes are limited to `cmd/serf-hub` route/templates/assets/tests and this plan/spec if updated.

- [ ] **Step 4: Final report**

Report:

- commits created;
- tests run and exact pass/fail results;
- any known limitations;
- whether pre-existing untracked files remain.

Do not claim success unless Step 2 passed in the current execution.

---

## Self-Review Against Spec

Spec coverage:

- App shell remains separate: Task 1 tests `/s/<id>` and keeps `app.html` behavior.
- HX-gated partial remains non-iframe: Task 1 tests partial without HX returns 404.
- Standalone authenticated thread document: Task 1 creates `/thread/<ref>` and Task 6 checks security headers.
- Shared thread fragment and app-shell chrome flags: Task 1 adds `ShowSidebarToggle` / `ThreadDocumentMode` and guards hamburger.
- Thread document assets: Task 1 template includes required CSS/JS and tests check key scripts.
- Nested open-beside host bridge: Task 3.
- URL construction and legacy migration: Task 2.
- Breadcrumb policy: Task 4.
- Pane error handling: Task 5.
- Security/CSP: Task 3 bridge validation and Task 6 headers.
- Tests: Tasks 1 through 6 add server, JS, and integration-style coverage.

Placeholder scan:

- No `TBD`, `TODO`, or “implement later” placeholders remain.
- Where implementation choices are allowed by the spec, this plan fixes concrete choices: route `/thread/<encoded-ref>`, template fields `ShowSidebarToggle` and `ThreadDocumentMode`, and bridge message type `serf:open-beside`.

Type/interface consistency:

- JS helper names are consistent: `threadHref`, `normalizePaneHref`, `isPaneSafeHref`, `openFromChild`, `markError`.
- Go template fields are consistent: `ShowSidebarToggle`, `ThreadDocumentMode`.
- Route path is consistently `/thread/<encoded-ref>`.
