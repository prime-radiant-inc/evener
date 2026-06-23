# Compact Thread Pane Chrome Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make side-pane thread documents tighter by removing redundant parent chrome, compressing footer metadata, and moving composer actions into the input box.

**Architecture:** The existing `/thread/<ref>` document is the reusable side-pane surface and already has `body.thread-document`; use that boundary for compact presentation while preserving full `/s/<id>` workspace behavior. Keep server data flow unchanged unless tests require a semantic template split; favor scoped template/CSS changes and focused route assertions.

**Tech Stack:** Go `net/http` server/templates, HTML templates under `cmd/serf-hub/templates`, CSS in `cmd/serf-hub/assets/style.css`, existing JS tests under `cmd/serf-hub/jstest`, Go tests in `cmd/serf-hub/web_test.go`.

## Global Constraints

- Preserve full `/s/<id>` workspace behavior unless a task explicitly targets `/thread/<ref>`.
- Default tests must be deterministic and must not depend on provider credentials, network access, quota, current model behavior, or ambient developer machine state.
- Keep the composer usable in both full workspaces and thread documents.
- Do not reintroduce `?pane=1`; the side-pane surface remains the standalone thread document endpoint.
- Keep nested open-beside and existing renderer/composer data attributes intact.

---

### Task 1: Compact thread-document header and footer metadata

**Files:**
- Modify: `cmd/serf-hub/templates/partials/workspace.html`
- Modify: `cmd/serf-hub/templates/partials/input_strip.html`
- Modify: `cmd/serf-hub/web_test.go`

**Interfaces:**
- Consumes: `WorkspaceData.ThreadDocumentMode bool` already used in `workspace.html`.
- Produces: `/thread/<ref>` omits the `.subagent-parent-banner`; `/s/<id>` keeps it. `input_status` can render a compact variant when `.ThreadDocumentMode` is true.

- [ ] **Step 1: Add failing Go assertions for thread compacting**

In `cmd/serf-hub/web_test.go`, extend `TestWeb_ThreadDocument_DirectGet_ServesChromeLessThreadDocument` or add a focused test using existing `newWebTestServer` helpers. Assert the thread document does not render:

```go
for _, forbidden := range []string{
	`class="subagent-parent-banner"`,
	`subagent-parent-esc`,
	`<span class="status-key">src</span>`,
	`<span class="status-key">cwd</span>`,
	`<span class="status-key">branch</span>`,
} {
	if strings.Contains(body, forbidden) {
		t.Fatalf("thread document contains compact-forbidden markup %q in:\n%s", forbidden, body)
	}
}
for _, required := range []string{
	`class="workspace-title-row"`,
	`class="message-input"`,
	`data-task-status-text`,
	`class="status-badge"`,
	`class="status-item turns"`,
} {
	if !strings.Contains(body, required) {
		t.Fatalf("thread document missing compact-required markup %q in:\n%s", required, body)
	}
}
```

Add or preserve a companion assertion for full `/s/<subagent>` behavior that confirms the parent banner still renders outside `ThreadDocumentMode`:

```go
if !strings.Contains(fullBody, `class="subagent-parent-banner"`) {
	t.Fatalf("full workspace should keep subagent parent banner; body:\n%s", fullBody)
}
```

- [ ] **Step 2: Run the focused Go test to verify it fails**

Run:

```bash
go test ./cmd/serf-hub -run 'TestWeb_ThreadDocument|TestWeb_Workspace' -count=1 -v
```

Expected: FAIL because `/thread/...` still includes the subagent parent banner and footer metadata.

- [ ] **Step 3: Hide parent banner in thread documents**

In `cmd/serf-hub/templates/partials/workspace.html`, change the parent banner condition from:

```gotemplate
{{if .ParentRouteID}}
```

to:

```gotemplate
{{if and .ParentRouteID (not .ThreadDocumentMode)}}
```

This keeps full workspace parent navigation and removes the redundant side-pane header block.

- [ ] **Step 4: Compact footer metadata in thread documents**

In `cmd/serf-hub/templates/partials/input_strip.html`, wrap the noisy metadata with `{{if not .ThreadDocumentMode}}` while keeping status and turns always visible:

```gotemplate
{{define "input_status"}}
{{if and .SourceLabel (not .ThreadDocumentMode)}}<span class="status-item source"><span class="status-key">src</span> <span class="status-value source-label">{{.SourceLabel}}</span></span>{{end}}
<span class="status-badge" data-state="{{.State}}"><span class="status-dot" data-state="{{.State}}"></span>{{.StateLabel}}</span>
<span class="status-item turns"><span class="status-value turn-count">{{.TurnCount}} turn{{if ne .TurnCount 1}}s{{end}}</span></span>
{{if not .ThreadDocumentMode}}
{{if .WorkingDir}}<span class="status-item cwd" title="{{.WorkingDir}}"><span class="status-key">cwd</span> <span class="status-value">{{.WorkingDir}}</span></span>{{end}}
{{if .Branch}}<span class="status-item branch"><span class="status-key">branch</span> <span class="status-value">{{.Branch}}</span></span>{{end}}
{{if .ContextWindow}}<span class="status-item context{{if ge .ContextPercent 80}} context-warn{{end}}"><span class="status-key">ctx</span>{{if ge .ContextPercent 80}} <span class="context-warn-glyph" title="Near the context limit" aria-hidden="true">⚠</span>{{end}} <span class="status-value context-numbers">{{.ContextNumbers}}</span><span class="context-bar"><span class="context-fill{{if ge .ContextPercent 80}} context-warn{{end}}" style="width:{{.ContextPercent}}%"></span></span></span>{{end}}
{{if .Cost}}<span class="status-item cost"><span class="status-key">cost</span> <span class="status-value">{{.Cost}}</span></span>{{end}}
{{if .GoalStatus}}<span class="status-item goal"><span class="status-key">goal</span> <span class="status-value">{{.GoalStatus}} · {{.GoalIterations}} turns</span></span>{{end}}
{{end}}
{{end}}
```

- [ ] **Step 5: Run focused Go tests to verify pass**

Run:

```bash
go test ./cmd/serf-hub -run 'TestWeb_ThreadDocument|TestWeb_Workspace' -count=1 -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/templates/partials/workspace.html cmd/serf-hub/templates/partials/input_strip.html cmd/serf-hub/web_test.go
git commit -m "fix(hub): compact thread document metadata"
```

---

### Task 2: Move composer controls into the input card

**Files:**
- Modify: `cmd/serf-hub/templates/partials/workspace.html`
- Modify: `cmd/serf-hub/assets/style.css`
- Modify: `cmd/serf-hub/jstest/test-input-area.js` or add a focused JS test under `cmd/serf-hub/jstest/`
- Modify: `cmd/serf-hub/web_test.go` if template structure assertions are clearer in Go

**Interfaces:**
- Consumes: existing composer attributes: `data-attach-trigger`, `data-model-trigger`, `data-steer-trigger`, `data-capability-*`, `data-drop-zone`, `data-file-picker`.
- Produces: `.input-card` contains the textarea plus an in-card `.input-controls`; model display is outside the button line, read-only when model changes are unavailable and still clickable when `.Capabilities.ChangeModel` is true.

- [ ] **Step 1: Add failing structure test for in-card controls**

Add a deterministic test that renders `/thread/anysession` and asserts ordering/containment by string positions:

```go
inputCard := strings.Index(body, `class="input-card"`)
messageInput := strings.Index(body, `class="message-input"`)
inputControls := strings.Index(body, `class="input-controls"`)
inputStatus := strings.Index(body, `id="input-status"`)
if inputCard < 0 || messageInput < 0 || inputControls < 0 || inputStatus < 0 {
	t.Fatalf("missing composer structure: inputCard=%d messageInput=%d inputControls=%d inputStatus=%d", inputCard, messageInput, inputControls, inputStatus)
}
if !(inputCard < messageInput && messageInput < inputControls && inputControls < inputStatus) {
	t.Fatalf("composer controls should be inside input card before input status")
}
if !strings.Contains(body, `class="composer-model"`) {
	t.Fatalf("composer should render model outside the button row")
}
```

- [ ] **Step 2: Run the focused test to verify it fails**

Run:

```bash
go test ./cmd/serf-hub -run 'TestWeb_ThreadDocument' -count=1 -v
```

Expected: FAIL because `.input-controls` is currently outside `.input-card` and there is no `.composer-model` row.

- [ ] **Step 3: Restructure composer markup**

In `cmd/serf-hub/templates/partials/workspace.html`, move `.input-controls` inside `.input-card` after the textarea. Add a model line above the input card or at the top of the card:

```gotemplate
<div class="input-card" data-drop-zone>
  <div class="composer-model" title="{{if .Model}}{{.Model}}{{else}}model unavailable{{end}}">
    <span class="composer-model-key">model</span>
    {{if .Capabilities.ChangeModel}}
    <button type="button" class="composer-model-value" data-model-trigger{{if .Model}} title="{{.Model}}"{{end}}>
      <span data-model-display>{{if .Model}}{{.Model}}{{else}}—{{end}}</span><span class="caret">▾</span>
    </button>
    {{else}}
    <span class="composer-model-value" data-model-display>{{if .Model}}{{.Model}}{{else}}—{{end}}</span>
    {{end}}
  </div>
  <textarea class="message-input" placeholder="message the agent…" autofocus rows="1"></textarea>
  <div class="input-controls">
    <div class="controls-left">
      <button type="button" class="btn btn-secondary" data-attach-trigger title="attach image" aria-label="attach image">＋</button>
    </div>
    <div class="controls-center"></div>
    <div class="controls-right">
      {{if and (ne .State "ended") (ne .State "closed") (or .Capabilities.Interrupt .Capabilities.Steer .Capabilities.Send .Capabilities.Queue)}}
      <button type="button" class="btn btn-danger stop-btn" data-action-trigger="interrupt" data-capability-interrupt="{{.Capabilities.Interrupt}}" title="stop the in-flight turn"{{if or (not .Capabilities.Interrupt) (eq .ActiveTurnID "") (and (ne .State "awaiting") (ne .State "active"))}} disabled{{end}}>Stop</button>
      {{end}}
      {{if or .Capabilities.Steer .Capabilities.Send .Capabilities.Queue}}
      <button type="button" class="btn btn-ghost" data-steer-trigger data-capability-steer="{{.Capabilities.Steer}}" title="drain the queue as a steering message — or steer with the textarea text when the queue is empty"{{if or (not .Capabilities.Steer) (eq .ActiveTurnID "") (and (ne .State "awaiting") (ne .State "active"))}} disabled{{end}}>send as steer <kbd>⇧↵</kbd></button>
      {{end}}
      <button type="submit" class="btn btn-primary send-btn" data-capability-send="{{.Capabilities.Send}}" data-capability-queue="{{.Capabilities.Queue}}"{{if and (not .Capabilities.Send) (not .Capabilities.Queue)}} disabled title="send unavailable"{{end}}>send <kbd>⌘↵</kbd></button>
    </div>
  </div>
</div>
```

Remove the old model chip from `.controls-left` so model no longer consumes button-row space.

- [ ] **Step 4: Style the compact composer**

In `cmd/serf-hub/assets/style.css`, update composer styling:

```css
.input-card {
  background: var(--bg-raised);
  border: 1px solid var(--rule);
  border-radius: var(--radius-md);
  padding: var(--space-3) var(--space-4);
  min-height: 0;
  transition: border-color var(--motion-fast);
}
.composer-model {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  margin-bottom: var(--space-2);
  color: var(--text-muted);
  font-family: var(--font-mono);
  font-size: var(--text-xs);
}
.composer-model-key { color: var(--text-muted); }
.composer-model-value {
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text);
  font: inherit;
  border: 0;
  background: transparent;
  padding: 0;
}
button.composer-model-value { cursor: pointer; color: var(--accent); }
button.composer-model-value:hover { text-decoration: underline; }
.input-card .input-controls {
  padding: var(--space-3) 0 0;
  margin-top: var(--space-2);
  border-top: 0;
}
.thread-document .workspace-input {
  padding: var(--space-3) var(--space-4);
}
.thread-document .input-status {
  gap: var(--space-3);
  padding-top: var(--space-2);
  margin-top: var(--space-1);
  border-top: 0;
}
.thread-document .conversation {
  padding: var(--space-4);
}
```

Keep selectors additive and avoid breaking full-page layouts.

- [ ] **Step 5: Run focused Go and JS tests**

Run:

```bash
go test ./cmd/serf-hub -run 'TestWeb_ThreadDocument' -count=1 -v
cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} node test-input-area.js
```

Expected: PASS. If `test-input-area.js` asserts the old model chip location, update it to require the same data attributes in the new `.composer-model` element and controls inside `.input-card`.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/templates/partials/workspace.html cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-input-area.js cmd/serf-hub/web_test.go
git commit -m "feat(hub): tighten thread pane composer"
```

---

### Task 3: Verification and manual smoke

**Files:**
- Modify only if failures expose necessary focused fixes.

**Interfaces:**
- Consumes: Task 1 and Task 2 commits.
- Produces: verified branch with compact thread pane chrome and composer layout.

- [ ] **Step 1: Run formatting/whitespace check**

```bash
git diff --check
```

Expected: no output and exit 0.

- [ ] **Step 2: Run focused Go suite**

```bash
go test ./cmd/serf-hub -count=1
```

Expected: PASS.

- [ ] **Step 3: Run JS suite**

```bash
cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} ./run-all.sh
```

Expected: `jstest: all tests passed`.

- [ ] **Step 4: Manual HTTP smoke test**

Start a local isolated `serf-hub` binary with temporary config and fetch `/s/manual` and `/thread/manual` using the auth token. Assert:

```text
/s/manual includes id="sidebar"
/thread/manual includes body class="thread-document"
/thread/manual does not include class="subagent-parent-banner"
/thread/manual includes class="composer-model"
/thread/manual includes class="input-card" before class="input-controls" before id="input-status"
/thread/manual does not include footer status-key src/cwd/branch
/thread/manual includes class="status-badge" and class="status-item turns"
```

Expected: all assertions pass; stop the server and remove temp files.

- [ ] **Step 5: Commit any verification fixes**

If Step 1-4 required fixes, commit them with a focused message. If no fixes were needed, do not create an empty commit.

---

### Task 4: Push update

**Files:**
- No source changes expected.

**Interfaces:**
- Consumes: verified compact pane commits.
- Produces: pushed branch `origin/subagent-side-view-chrome`.

- [ ] **Step 1: Check status**

```bash
git status --short --branch
```

Expected: clean worktree on `subagent-side-view-chrome` tracking `origin/subagent-side-view-chrome`.

- [ ] **Step 2: Push**

```bash
git push
```

Expected: push succeeds and remote branch updates.

- [ ] **Step 3: Final state check**

```bash
git status --short --branch
git log --oneline --decorate -3
```

Expected: clean worktree; HEAD includes compact pane commits and matches upstream.
