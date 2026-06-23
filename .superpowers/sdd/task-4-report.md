# Task 4 Report: Pane-safe parent breadcrumb behavior

## What implemented
- Updated `cmd/serf-hub/templates/partials/workspace.html` so parent breadcrumbs in `ThreadDocumentMode` render pane-safe `/thread/<encoded-parent>` hrefs.
- Added `data-open-parent-beside` to thread-document parent breadcrumb links so clicks can be intercepted inside framed panes.
- Preserved normal full-app breadcrumb behavior outside thread-document mode: `/s/<parent>` remains unchanged.
- Added `SerfRenderer.bindPaneParentLinks()` in `cmd/serf-hub/assets/renderer.js` and call it from renderer init. It intercepts `[data-open-parent-beside]` clicks, prevents iframe navigation, and calls `openBeside({ href, title })`.
- Extended tests for server markup and JS bridge click behavior.

## Tests run and results
- `go test ./cmd/serf-hub -run 'TestWeb_ThreadDocument_SubagentBreadcrumbUsesPaneSafeAttributes|TestWeb_ThreadDocument_DirectGet_ServesChromeLessThreadDocument' -count=1 -v` — PASS
- `cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-thread-document-bridge.js` — PASS
- `git diff --check` — PASS

## TDD Evidence

### RED command/output
Command:
```bash
go test ./cmd/serf-hub -run 'TestWeb_ThreadDocument_SubagentBreadcrumbUsesPaneSafeAttributes' -count=1 -v
cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-thread-document-bridge.js
```

Output excerpts:
```text
--- go RED ---
=== RUN   TestWeb_ThreadDocument_SubagentBreadcrumbUsesPaneSafeAttributes
    web_test.go:2538: breadcrumb missing pane-safe parent target: ...
--- FAIL: TestWeb_ThreadDocument_SubagentBreadcrumbUsesPaneSafeAttributes (0.00s)
FAIL
FAIL	primeradiant.com/serf/cmd/serf-hub	0.464s
FAIL

--- js RED ---
PASS — host opens initial pane
PASS — known child frame can open a pane through host bridge
PASS — host bridge opens requested thread href
PASS — host bridge rejects cross-origin hrefs
PASS — host bridge rejects unknown source windows
PASS — framed child with no local pane host returns no local pane
PASS — framed child SerfPanes.open posts bridge request to host
FAIL — framed thread breadcrumb opens parent through host bridge
go_status=1 js_status=1
```

### GREEN command/output
Command:
```bash
go test ./cmd/serf-hub -run 'TestWeb_ThreadDocument_SubagentBreadcrumbUsesPaneSafeAttributes|TestWeb_ThreadDocument_DirectGet_ServesChromeLessThreadDocument' -count=1 -v
cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-thread-document-bridge.js
```

Output:
```text
--- go GREEN ---
=== RUN   TestWeb_ThreadDocument_DirectGet_ServesChromeLessThreadDocument
--- PASS: TestWeb_ThreadDocument_DirectGet_ServesChromeLessThreadDocument (0.01s)
=== RUN   TestWeb_ThreadDocument_SubagentBreadcrumbUsesPaneSafeAttributes
--- PASS: TestWeb_ThreadDocument_SubagentBreadcrumbUsesPaneSafeAttributes (0.00s)
PASS
ok  	primeradiant.com/serf/cmd/serf-hub	0.420s
--- js GREEN ---
PASS — host opens initial pane
PASS — known child frame can open a pane through host bridge
PASS — host bridge opens requested thread href
PASS — host bridge rejects cross-origin hrefs
PASS — host bridge rejects unknown source windows
PASS — framed child with no local pane host returns no local pane
PASS — framed child SerfPanes.open posts bridge request to host
PASS — framed thread breadcrumb posts parent open request to host bridge
OK	test-thread-document-bridge.js
go_status=0 js_status=0
```

## Files changed
- `cmd/serf-hub/templates/partials/workspace.html`
- `cmd/serf-hub/assets/renderer.js`
- `cmd/serf-hub/web_test.go`
- `cmd/serf-hub/jstest/test-thread-document-bridge.js`

## Self-review findings
- Scope is limited to Task 4: breadcrumb markup and click handling only.
- Direct `/s/<id>` behavior is preserved because non-thread-document breadcrumbs still render `/s/{{.ParentRouteID}}` without `data-open-parent-beside`.
- Thread document route remains `/thread/<encoded-ref>`; workspace partial route was not changed.
- JS test initially hung because renderer init starts timers in the harness; fixed by explicitly exiting after successful assertions, matching existing jstest patterns.
- `git diff --check` passed.

## Issues/concerns
- None.

## Review fix: standalone thread breadcrumb fallback

### What changed
- Updated `cmd/serf-hub/assets/renderer.js` so `SerfRenderer.openBeside(spec)` returns `true` only when a local pane host or framed parent bridge handles the open request, and `false` otherwise.
- Updated `bindPaneParentLinks()` to call `preventDefault()` only after `openBeside()` reports that pane/bridge handling is available. Standalone `/thread/<ref>` documents without `panes.js` now leave breadcrumb clicks uncanceled so normal anchor navigation to `/thread/<parent>` can proceed.
- Extended `cmd/serf-hub/jstest/test-thread-document-bridge.js` coverage to assert both framed-positive behavior (click canceled and bridge message posted) and standalone fallback behavior (click not canceled when `isInPane()` is false and no `SerfPanes` exists).

### Tests run and results
- `cd /Users/jesse/git/prime-radiant-inc/serf/.worktrees/subagent-side-view-chrome/cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-thread-document-bridge.js` — PASS. Output included framed bridge assertions and `PASS — standalone thread breadcrumb click is not canceled so href navigation can proceed`.
- `cd /Users/jesse/git/prime-radiant-inc/serf/.worktrees/subagent-side-view-chrome/cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-renderer-open-beside.js` — PASS. Existing open-beside row behavior remains unchanged.
- `cd /Users/jesse/git/prime-radiant-inc/serf/.worktrees/subagent-side-view-chrome && go test ./cmd/serf-hub -run 'TestWeb_ThreadDocument_SubagentBreadcrumbUsesPaneSafeAttributes|TestWeb_ThreadDocument_DirectGet_ServesChromeLessThreadDocument' -count=1 -v` — PASS. Both targeted thread document server/template tests passed.

### Files changed
- `cmd/serf-hub/assets/renderer.js`
- `cmd/serf-hub/jstest/test-thread-document-bridge.js`
- `.superpowers/sdd/task-4-report.md`
