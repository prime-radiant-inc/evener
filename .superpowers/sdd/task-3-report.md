# Task 3 Report: Host bridge for nested open-beside from framed thread documents

## What implemented
- Added host-side pane bridge support in `cmd/serf-hub/assets/panes.js`:
  - `isKnownPaneSource(source)` validates that a message/source window belongs to an existing `.pane-frame` iframe.
  - `isPaneSafeHref(href)` normalizes pane hrefs and allows only same-origin `/thread/` or `/doc/` pane targets.
  - `openFromChild(sourceWindow, href, title)` validates source and href before delegating to `SerfPanes.open()`.
  - Registered a same-origin `message` listener for `{ type: "serf:open-beside", href, title }` requests.
  - Exported `SerfPanes.openFromChild` and `SerfPanes.isPaneSafeHref`.
- Added framed renderer fallback in `cmd/serf-hub/assets/renderer.js`:
  - `SerfRenderer.openBeside(spec)` opens locally when `window.SerfPanes` exists.
  - When framed and no local pane host exists, it posts `serf:open-beside` to the same-origin parent.
  - `makeOpenBesideButton` now permits framed renderers and preserves renderer context for click/key handlers.
- Added `cmd/serf-hub/jstest/test-thread-document-bridge.js` covering host bridge open, cross-origin rejection, and unknown-source rejection.

## Tests run and results
- `cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-thread-document-bridge.js` — PASS after implementation.
- `cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-renderer-open-beside.js` — PASS after implementation.
- `git diff --check` — PASS, no whitespace errors.

## TDD Evidence

### RED command/output
Command:
```bash
cd /Users/jesse/git/prime-radiant-inc/serf/.worktrees/subagent-side-view-chrome/cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-thread-document-bridge.js
```
Output:
```text
PASS — host opens initial pane
/Users/jesse/git/prime-radiant-inc/serf/.worktrees/subagent-side-view-chrome/cmd/serf-hub/jstest/test-thread-document-bridge.js:31
  const opened = host.SerfPanes.openFromChild(frame.contentWindow, "/thread/local%3Agrandchild", "grandchild");
                                ^

TypeError: host.SerfPanes.openFromChild is not a function
    at /Users/jesse/git/prime-radiant-inc/serf/.worktrees/subagent-side-view-chrome/cmd/serf-hub/jstest/test-thread-document-bridge.js:31:33
    at Object.<anonymous> (/Users/jesse/git/prime-radiant-inc/serf/.worktrees/subagent-side-view-chrome/cmd/serf-hub/jstest/test-thread-document-bridge.js:44:3)
    at Module._compile (node:internal/modules/cjs/loader:1829:14)
    at Module._extensions..js (node:internal/modules/cjs/loader:1969:10)
    at Module.load (node:internal/modules/cjs/loader:1552:32)
    at Module._load (node:internal/modules/cjs/loader:1354:12)
    at wrapModuleLoad (node:internal/modules/cjs/loader:255:19)
    at Module.executeUserEntryPoint [as runMain] (node:internal/modules/run_main:154:5)
    at node:internal/main/run_main_module:33:47

Node.js v26.0.0
```
Exit: 1.

### GREEN command/output
Command:
```bash
cd /Users/jesse/git/prime-radiant-inc/serf/.worktrees/subagent-side-view-chrome/cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-thread-document-bridge.js && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-renderer-open-beside.js
```
Output:
```text
PASS — host opens initial pane
PASS — known child frame can open a pane through host bridge
PASS — host bridge opens requested thread href
PASS — host bridge rejects cross-origin hrefs
PASS — host bridge rejects unknown source windows
OK	test-thread-document-bridge.js
PASS — open-beside button appears on a subagent row that has a transcriptRef
PASS — clicking open-beside calls SerfPanes.open with correct href
PASS — subagent open-beside uses thread document route for source-qualified refs
PASS — clicking open-beside does NOT trigger the row's hard navigation
PASS — open-beside button is absent when window.SerfPanes is not present
PASS — CSS defines .open-beside-btn (hover-revealed quiet button)
OK	test-renderer-open-beside.js
```
Exit: 0.

## Files changed
- `cmd/serf-hub/assets/panes.js`
  - Bridge validation and message handling at lines 62-96.
  - Exports and message listener at lines 306-307.
- `cmd/serf-hub/assets/renderer.js`
  - `openBeside(spec)` helper at lines 225-234.
  - Framed fallback button behavior at lines 2501-2521.
- `cmd/serf-hub/jstest/test-thread-document-bridge.js`
  - New bridge regression test.

## Self-review findings
- Scope is limited to Task 3 bridge behavior; no Task 4+ breadcrumb behavior or Task 5 loading/error UI was added.
- The iframe target remains `/thread/<encoded-ref>` through existing `threadHref`/`normalizePaneHref` paths.
- The host bridge validates both same-origin event origin and known child iframe source before opening.
- `isPaneSafeHref` rejects cross-origin URLs and restricts targets to `/thread/` and `/doc/`.
- Direct `/s/<id>` navigation and workspace partial routing were not modified.
- `git diff --check` passed.

## Issues/concerns
- Existing `test-renderer-open-beside.js` still asserts that open-beside is absent when `window.SerfPanes` is missing in an unframed harness. This remains valid; the new framed fallback path is enabled only when `isInPane()` is true.
- No concerns.

## Review fix: framed child SerfPanes fallback

### What changed
- Updated `cmd/serf-hub/assets/panes.js` so `SerfPanes.open(href, title)` posts `{ type: "serf:open-beside", href, title }` to the same-origin parent when the document is framed and has no local `#side-panes` host, then returns `null`.
- Extended `cmd/serf-hub/jstest/test-thread-document-bridge.js` with a framed child harness where `panes.js` is loaded but no local pane host exists; the child calls `SerfPanes.open()`, the host message listener receives the bridged request, and the host opens the requested thread pane.

### Tests run
Command:
```bash
cd /Users/jesse/git/prime-radiant-inc/serf/.worktrees/subagent-side-view-chrome/cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-thread-document-bridge.js && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-renderer-open-beside.js
```
Output summary:
```text
PASS — host opens initial pane
PASS — known child frame can open a pane through host bridge
PASS — host bridge opens requested thread href
PASS — host bridge rejects cross-origin hrefs
PASS — host bridge rejects unknown source windows
PASS — framed child with no local pane host returns no local pane
PASS — framed child SerfPanes.open posts bridge request to host
OK	test-thread-document-bridge.js
PASS — open-beside button appears on a subagent row that has a transcriptRef
PASS — clicking open-beside calls SerfPanes.open with correct href
PASS — subagent open-beside uses thread document route for source-qualified refs
PASS — clicking open-beside does NOT trigger the row's hard navigation
PASS — open-beside button is absent when window.SerfPanes is not present
PASS — CSS defines .open-beside-btn (hover-revealed quiet button)
OK	test-renderer-open-beside.js
```
Exit: 0.

### Files changed
- `cmd/serf-hub/assets/panes.js`
- `cmd/serf-hub/jstest/test-thread-document-bridge.js`
- `.superpowers/sdd/task-3-report.md`
