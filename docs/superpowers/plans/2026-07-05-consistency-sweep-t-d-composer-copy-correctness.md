# Consistency Sweep — Track D: Composer-at-rest + Copy + Correctness residue + Investigate

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **DEPENDENCY — rebases on merged Track A.** This track runs **after Track A merges**. Track A rewrites attention rendering / needs-you icons in `renderer.js` (status regions) and the sidebar copy/vocabulary in `sidebar.js`, both of which Track D also touches (composer action-state in `renderer.js`; the row-menu + `working_dir` escaping in `sidebar.js`). The tasks below are written against **current** `consistency-sweep` code; before executing, rebase onto merged Track A and re-locate the anchors named in each task (they may have shifted by a few lines and the `processing`→`working` display rename may have landed — the wire enum values `active`/`awaiting` are untouched by Track A, so this plan's state-string comparisons stand). Nothing here depends on Track B or C.

**Goal:** A settled `awaiting` session presents plain **Send** (no Stop/steer/queue) on both surfaces; the task-status row is event-driven (no 5s poll, no eternal "loading…" on ended sessions); a scatter of copy, comment, and correctness residue is cleaned up with a red-first test for each; and two live-only questions (project ordering feel, spawn-failure UX) get explicit investigate-tasks. No behavior regresses; every mechanical fix ships behind its own reviewer gate.

**Architecture:** Composer affordance is decided in three places that currently collapse `awaiting`==`active` in one line — the TUI `sessionTurnActionState` (composer_panel.go), the web `turnAcceptsActions` (renderer.js), and the server-rendered initial markup (workspace.html). Each learns to distinguish a rested `awaiting` (plain Send) from a running `active` (Stop/steer/queue). The `serf/task/updated` notification — defined in the appwire catalog but emitted by nothing — gets an emit site at the task-store mutation point in the agent (projected through the existing `EventQueueChanged`-style path), the web client subscribes the task-status row to it, and the 5s `startTaskBadgePoller` is retired. The remaining items are localized fixes to hub HTTP handlers, JS, CSS, Go comments, and one flaky MCP test.

**Tech Stack:** Go (`cmd/serf-tui`, `cmd/serf-hub`, `agent`, `server`, `internal/appprojector`, `appwire`), JS (JSDOM jstest under `cmd/serf-hub/jstest`), CSS (`cmd/serf-hub/assets/style.css`), Go html/template (`cmd/serf-hub/templates`). Modules per `GO_MODULES` in Makefile (`.`, `agent`, `llm`, `auth`, `envvars`, `fuzz`, `invariant`) — run tests per-module; `agent` is its own module.

**Global Constraints (verbatim, apply to every task):**
- Run Go tests per-module: `cd <module> && go test ./<pkg>/ -run <Name> -count=1`. The web/hub/server code is in the **repo-root** module (`.`); the agent code is in `agent`.
- jstest: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh` (or `node test-<name>.js` for one file).
- `make lint` runs `serf-namingcheck`; per-task golangci misses it. Wire keys stay snake_case; do **not** rename any wire enum value (`active`, `awaiting` on the wire are the Codex-shaped contract). Deliberate camelCase takes a `// serf:naming-ignore:` line.
- Test output MUST be pristine. Any intentional error output must be captured and asserted, never left printing.
- Commit after every green task with the exact message given. NEVER `git add -A`; add the named files only.
- Read the anchor code before editing it — re-locate symbols after the Track A rebase.
- `docs/appwire-protocol.md` is generated (`make generate`); do not hand-edit it. New appwire **methods/notifications** need catalog + both routers in one commit; a notification that already exists in the catalog (like `serf/task/updated`) needs **no** catalog change (see Correctness task "emit serf/task/updated").
- **§12 (stdio probe `LookPath`-only): intentionally left as-is** — a documented limit, not in this track. No task.

## File Structure

```
cmd/serf-tui/
  composer_panel.go              # sessionTurnActionState (~99), sessionComposerMode (~58), queue mode (~141) — Composer T1
  composer_panel_test.go         # (new/extend) TUI composer-at-rest units — Composer T1
cmd/serf-hub/
  assets/renderer.js             # turnAcceptsActions (~436), syncTurnActionControls (~415), updateThreadState (~336),
                                 #   startTaskBadgePoller (~466), QUEUE_CHANGED handler (~1003) — Composer T2, Copy T3, Correctness T9
  assets/renderer-panels.js      # renderTasksInto (~285), updateTasksBadge (~358, "no tasks") — Copy T2
  assets/appwire.js              # eventsFromNotification (~810), tasks (~390) — Correctness T9
  assets/sidebar.js              # projectMenuItems (~171, R2 menu), working_dir encodeURIComponent (~176) — Correctness T5/T6
  assets/style.css               # .spawn-recent-row base (~3128) vs phone clamp (~4122); light-theme errored (~211) — Copy T4/T5
  templates/partials/workspace.html          # Stop/steer/send disabled gating (~88/91/93), tasks "loading…" (~65) — Composer T3, Copy T1
  templates/partials/settings/general.html   # settings copy staleness — Copy T6
  web_api_tree.go                # apiSessionDetail (~673) rename resolution — Correctness T1
  web_api_archive.go             # handleAPIArchive T8 comment/gate (~43) — Correctness T3
  web_api_rename.go              # ended-rename T18 (~53) — Correctness T4
  jstest/test-actions.js         # extend: web composer-at-rest — Composer T2
  jstest/test-sidebar-menu.js    # extend: R2 Unarchive menu — Correctness T5
  jstest/test-sidebar-workingdir-escape.js   # (new) encodeURIComponent test — Correctness T6
  jstest/test-task-updated-subscription.js   # (new) event-driven task row — Correctness T9
agent/
  session_tools_task.go          # task tool exec — emit serf/task/updated — Correctness T9
  session_init.go                # stale SourceMCP comments (~1343/1351) — Correctness T7
  internal/mcp/manager.go        # conn.reconnecting clear → defer (~537/560) — Correctness T8
  internal/mcp/cov_reconnect_test.go   # TestReconnect flake (~688) — Correctness T10
  internal/diagnostic/diagnostic.go    # reconnect-recovery hint — Correctness T2
internal/appprojector/
  appwire_projection.go          # project EventTaskUpdated → serf/task/updated (~588 EventQueueChanged pattern) — Correctness T9
appwire/
  types.go                       # NotifySerfTaskUpdated (~87, exists), TaskUpdatedParams (new type) — Correctness T9
  protocol.go                    # Notifications catalog — add serf/task/updated entry (remove "intentionally absent" note) — Correctness T9
docs/superpowers/plans/
  2026-07-05-consistency-sweep-t-d-composer-copy-correctness.md   # this plan
```

Task groups, in execution order: **Composer** (3 tasks) → **Copy/Display** (6 tasks) → **Correctness** (10 tasks) → **Investigate** (2 tasks). 21 tasks total.

---

## Composer

The three surfaces collapse `awaiting`==`active` into one predicate. A rested `awaiting` session (agent finished a turn, re-armed "your move", nothing actually running) must drop to plain **Send** — no Stop, no steer, no Queue routing — even when `Capabilities.Queue` is advertised. A genuinely `active` session is unchanged. The gating that already lets awaiting sessions send/steer (keyed on `!processing`) is untouched; this changes only the *presented affordance*. The `AwaitingQuestion` chip (driven by the pending-ask set, not by this state) is unaffected.

### Composer T1: TUI — rested `awaiting` shows Send, not Queue/steer

The TUI `sessionTurnActionState()` returns true for both `active` and `awaiting`, and `sessionComposerMode()` then routes an awaiting session into `hubComposerModeQueue` whenever `Capabilities.Queue` is set (composer_panel.go:58-75, 99-105). Split the composer-mode decision so only a genuinely running turn (`active`, or `session.processing`) takes the Queue/Stop path; a rested `awaiting` falls through to Send.

**Files:**
- Modify: `cmd/serf-tui/composer_panel.go`
- Test: `cmd/serf-tui/composer_panel_test.go` (new)

- [ ] **Step 1: Failing test — awaiting rests to Send even with Queue capability**

Create `cmd/serf-tui/composer_panel_test.go` (`package main`). Build a model whose detail State is `"awaiting"` with `Capabilities.Queue` and `Capabilities.Send` both true and `session.processing` false, and assert the composer mode is Send, not Queue:

```go
package main

import "testing"

func awaitingSendModel() hubModel {
	m := hubModel{}
	m.detail.State = "awaiting"
	m.detail.Capabilities.Queue = true
	m.detail.Capabilities.Send = true
	m.session.processing = false
	return m
}

func TestSessionComposerMode_RestedAwaitingShowsSend(t *testing.T) {
	m := awaitingSendModel()
	if got := m.sessionComposerMode(); got != hubComposerModeSend {
		t.Fatalf("rested awaiting composer mode = %v, want hubComposerModeSend (plain Send, no queue)", got)
	}
}

func TestSessionComposerMode_ActiveStillQueues(t *testing.T) {
	m := awaitingSendModel()
	m.detail.State = "active"
	m.session.processing = true
	if got := m.sessionComposerMode(); got != hubComposerModeQueue {
		t.Fatalf("active composer mode = %v, want hubComposerModeQueue (unchanged)", got)
	}
}
```

- [ ] **Step 2: Run — fails** (awaiting currently routes to Queue when Capabilities.Queue is set)

Run: `cd cmd/serf-tui && go test ./ -run TestSessionComposerMode -count=1`
Expected: FAIL — `rested awaiting composer mode = ... want hubComposerModeSend`.

- [ ] **Step 3: Implement — gate the Queue/Stop path on genuinely-running**

In `composer_panel.go`, add a predicate that is true only when a turn is actually in flight, and drive the composer mode's Queue branch off it instead of `sessionTurnActionState()`:

```go
// sessionTurnRunning reports a genuinely in-flight turn (the composer should
// offer Stop/steer/queue). A rested "awaiting" session — re-armed "your move"
// with nothing running — is NOT running: it drops to plain Send. This is
// narrower than sessionTurnActionState (which stays true for awaiting so the
// status line's "busy" affordances and the !processing send-gating are
// unchanged); it governs only the presented composer affordance.
func (m hubModel) sessionTurnRunning() bool {
	if stateLabel(m.detail.State) == "active" {
		return true
	}
	return m.session.processing
}
```

Then in `sessionComposerMode()` replace `if m.sessionTurnActionState() {` (the block that chooses Queue/Send/ReadOnly) with `if m.sessionTurnRunning() {`. Leave `sessionComposerReadOnlyReason()` and `sessionTurnActionState()` as-is — they still key on the wider predicate.

- [ ] **Step 4: Run — pass**

Run: `cd cmd/serf-tui && go test ./ -run TestSessionComposerMode -count=1`
Expected: PASS

- [ ] **Step 5: Lint + commit**

Run: `cd cmd/serf-tui && go vet ./ && go test ./ -count=1`
Commit:
- `git add cmd/serf-tui/composer_panel.go cmd/serf-tui/composer_panel_test.go`
- `git commit -m "fix(tui): rested awaiting composer shows Send, not Queue/steer"`

_~30 loc._

### Composer T2: Web — `turnAcceptsActions` distinguishes awaiting-rest from active

`turnAcceptsActions(state)` returns `state === "active" || state === "awaiting"` (renderer.js:436-438). It gates the interrupt/steer buttons (`syncTurnActionControls`, ~415-434) and the send/queue capability flip (`updateThreadState`, ~386-401). A rested awaiting session shows Stop+steer and routes Enter into Queue. Split so the Stop/steer/queue affordances key on `active` alone while awaiting keeps its send path.

**Files:**
- Modify: `cmd/serf-hub/assets/renderer.js`
- Test: `cmd/serf-hub/jstest/test-actions.js` (extend)

- [ ] **Step 1: Failing test — awaiting composer is plain Send**

Append to `cmd/serf-hub/jstest/test-actions.js` (before the final pass/exit block). Drive the renderer's state to `awaiting` and assert the send button advertises send (not queue) and the interrupt button is disabled:

```js
// ── Composer-at-rest: a rested "awaiting" session shows plain Send ──────────
// updateThreadState("awaiting") must NOT flip the send button into queue mode
// nor enable the Stop/steer controls — awaiting is a rest, not a running turn.
const R = window.SerfRenderer || window.serfRenderer;
if (R && typeof R.updateThreadState === "function") {
  R.sessionId = "01ACT001";
  R.conversation = window.document.getElementById("conversation");
  // Provide the composer send button + interrupt the sync functions read.
  window.document.body.insertAdjacentHTML("beforeend",
    '<form data-input-form><button class="send-btn" data-capability-send="true" data-capability-queue="false"></button></form>');
  R.activeTurnId = "";
  R.updateThreadState("awaiting");
  const sendBtn = window.document.querySelector("form[data-input-form] .send-btn");
  pass(sendBtn.getAttribute("data-capability-queue") !== "true",
    "awaiting rest must NOT put the send button in queue mode");
  pass(R.turnAcceptsSteer ? R.turnAcceptsSteer("awaiting") === false : true,
    "awaiting rest must not advertise steer/queue affordances");
}
```

(If the renderer instance is exposed under a different global in the sibling tests, match whatever `test-actions.js` already uses to reach it — the assertion contract is fixed; adjust only the accessor.)

- [ ] **Step 2: Run — fails** (`turnAcceptsActions("awaiting")` is true, so awaiting flips to queue mode)

Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-actions.js`
Expected: FAIL — awaiting put the send button in queue mode / advertised steer.

- [ ] **Step 3: Implement — narrow the running-affordance predicate**

In `renderer.js`, add a `turnIsRunning` predicate and use it where the affordance is Stop/steer/queue, keeping `turnAcceptsActions` for the send-path gating that awaiting legitimately shares. Concretely:

```js
    turnAcceptsActions(state) {
      // A turn "accepts actions" (send path stays open) while active OR
      // awaiting — awaiting sessions can still receive a fresh message.
      return state === "active" || state === "awaiting";
    },

    turnIsRunning(state) {
      // Only a genuinely in-flight turn (active) offers Stop/steer and routes
      // Enter into Queue. A rested "awaiting" is your-move, not running: it
      // shows plain Send.
      return state === "active";
    },
```

In `syncTurnActionControls()` replace the two `turnAcceptsActions` reads that gate `interrupt.disabled` and `steer.disabled` with `this.turnIsRunning(this.state)`. In `updateThreadState()`, the queue-flip branch already special-cases `state === "active"` for the `liveQueueCap` path (renderer.js:386-401); its trailing `else` (which sets `data-capability-send="true"`, `data-capability-queue="false"`) already yields plain Send for awaiting — verify no separate `awaiting`→queue branch exists and, if the Track A rebase added one, gate it on `turnIsRunning`. Leave `turnAcceptsActions` callers that guard the send path (e.g. the TURN_COMPLETED `updateThreadState("idle")` guard at ~951, `setActiveTurnId` clearing at ~361) unchanged.

- [ ] **Step 4: Run — pass**

Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-actions.js`
Expected: PASS

- [ ] **Step 5: Lint + commit**

Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh`
Commit:
- `git add cmd/serf-hub/assets/renderer.js cmd/serf-hub/jstest/test-actions.js`
- `git commit -m "fix(web): rested awaiting composer shows Send, not Stop/steer/queue"`

_~25 loc._

### Composer T3: Server-rendered markup — awaiting rest disables Stop/steer inline

The initial workspace markup gates Stop and steer with `(and (ne .State "awaiting") (ne .State "active"))` — i.e. **enabled** for awaiting (workspace.html:88, 91). So the very first paint of a rested awaiting session shows an enabled Stop and steer until JS re-syncs. Tighten the inline gate to `active` only, matching Composer T2.

**Files:**
- Modify: `cmd/serf-hub/templates/partials/workspace.html`
- Test: `cmd/serf-hub/web_test.go` (new test)

- [ ] **Step 1: Failing test — awaiting workspace renders Stop/steer disabled**

Add to `cmd/serf-hub/web_test.go`. Render the workspace partial for an `awaiting` thread (adapt the existing `TestWeb_Workspace...` fixtures around line 370 that use `scriptedAppSource`) and assert Stop and steer carry `disabled`:

```go
func TestWeb_WorkspaceAwaitingRestDisablesStopAndSteer(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Past: hubcore.NewPastIndex("")})
	web.sources.Add(&scriptedAppSource{
		id: "codex",
		thread: appwire.Thread{
			ID: "th_await", SessionID: "th_await", Source: "codex",
			Status:        appwire.ThreadStatus{Type: appwire.ThreadStatusAwaiting},
			ModelProvider: "gpt-5",
			Serf: appwire.SerfThread{
				Ref:          "codex:th_await",
				Capabilities: appwire.ThreadCapabilities{Send: true, Steer: true, Interrupt: true, Queue: true},
			},
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/_partials/s/"+url.PathEscape("codex:th_await")+"/workspace", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	stop := controlTag(t, body, `data-action-trigger="interrupt"`)
	if !strings.Contains(stop, "disabled") {
		t.Fatalf("rested awaiting Stop must be disabled at first paint: %s", stop)
	}
	steer := controlTag(t, body, `data-steer-trigger`)
	if !strings.Contains(steer, "disabled") {
		t.Fatalf("rested awaiting steer must be disabled at first paint: %s", steer)
	}
}
```

- [ ] **Step 2: Run — fails** (awaiting currently enables Stop/steer inline)

Run: `go test ./cmd/serf-hub/ -run TestWeb_WorkspaceAwaitingRestDisablesStopAndSteer -count=1`
Expected: FAIL — Stop/steer not disabled.

- [ ] **Step 3: Implement — drop awaiting from the enable set**

In `workspace.html`, change the Stop button's disabled condition (line ~88) from:

```
{{if or (not .Capabilities.Interrupt) (eq .ActiveTurnID "") (and (ne .State "awaiting") (ne .State "active"))}} disabled{{end}}
```

to gate purely on active:

```
{{if or (not .Capabilities.Interrupt) (eq .ActiveTurnID "") (ne .State "active")}} disabled{{end}}
```

Apply the identical change to the steer button (line ~91): replace its `(and (ne .State "awaiting") (ne .State "active"))` clause with `(ne .State "active")`. Leave the send button (line ~93) unchanged — it stays enabled for awaiting (plain Send is exactly what we want).

- [ ] **Step 4: Run — pass** (and confirm the active-session control test still green)

Run: `go test ./cmd/serf-hub/ -run 'TestWeb_WorkspaceAwaitingRestDisablesStopAndSteer|TestWeb_Workspace' -count=1`
Expected: PASS

- [ ] **Step 5: Lint + commit**

Run: `go vet ./cmd/serf-hub/ && go test ./cmd/serf-hub/ -count=1`
Commit:
- `git add cmd/serf-hub/templates/partials/workspace.html cmd/serf-hub/web_test.go`
- `git commit -m "fix(web): rested awaiting disables Stop/steer at first paint"`

_~15 loc._

---

## Copy/Display

### Copy T1: Task-status row starts at a neutral resting label, not eternal "loading…"

The workspace's task-status value is server-rendered as `loading…` (workspace.html:65). On an ended session with no tasks the poller resolves it to `no tasks`, but if the tasks fetch never returns (ended source, transport gone) it spins on "loading…" forever. Seed the initial markup with a neutral em-dash placeholder so a session that never resolves reads as "no data", not "still loading". (The live resolution to `N/M` or `no tasks` is Copy T2 / Correctness T9.)

**Files:**
- Modify: `cmd/serf-hub/templates/partials/workspace.html`
- Test: `cmd/serf-hub/web_test.go` (new)

- [ ] **Step 1: Failing test — initial task-status text is not "loading…"**

```go
func TestWeb_WorkspaceTaskStatusInitialIsNeutral(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Past: hubcore.NewPastIndex("")})
	web.sources.Add(&scriptedAppSource{
		id: "codex",
		thread: appwire.Thread{
			ID: "th_tasks", SessionID: "th_tasks", Source: "codex",
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Serf:   appwire.SerfThread{Ref: "codex:th_tasks", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/_partials/s/"+url.PathEscape("codex:th_tasks")+"/workspace", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, `data-task-status-text>loading…`) {
		t.Fatalf("task-status must not hard-code the spinning 'loading…' placeholder:\n%s", body)
	}
}
```

- [ ] **Step 2: Run — fails**

Run: `go test ./cmd/serf-hub/ -run TestWeb_WorkspaceTaskStatusInitialIsNeutral -count=1`
Expected: FAIL — body contains `data-task-status-text>loading…`.

- [ ] **Step 3: Implement — neutral placeholder**

In `workspace.html` line 65, change `<span class="status-value" data-task-status-text>loading…</span>` to a neutral resting glyph:

```
<span class="status-key">tasks</span><span class="status-value" data-task-status-text>—</span>
```

The em-dash reads as "no data yet" and is overwritten by `updateTasksBadge` the instant the first task list resolves (to `N/M …` or `no tasks`), so a session that resolves looks identical to today; only the never-resolving ended case stops implying work is loading.

- [ ] **Step 4: Run — pass**

Run: `go test ./cmd/serf-hub/ -run TestWeb_WorkspaceTaskStatusInitialIsNeutral -count=1`
Expected: PASS

- [ ] **Step 5: Lint + commit**

Run: `go vet ./cmd/serf-hub/ && go test ./cmd/serf-hub/ -count=1`
Commit:
- `git add cmd/serf-hub/templates/partials/workspace.html cmd/serf-hub/web_test.go`
- `git commit -m "fix(web): task-status starts neutral, not eternal loading…"`

_~10 loc._

### Copy T2: "no tasks" → "no tasks yet"

The task-badge resting copy is the bare `"no tasks"` (renderer-panels.js:375), which reads as a terminal fact even mid-session. Align it with the panel's own empty-state title ("No tasks yet", renderer-panels.js:312): lowercase-in-the-status-row `"no tasks yet"` reads as "none so far", consistent with the panel.

**Files:**
- Modify: `cmd/serf-hub/assets/renderer-panels.js`
- Test: `cmd/serf-hub/jstest/test-tasks-panel.js` (new, or extend an existing tasks jstest if one covers `updateTasksBadge`)

- [ ] **Step 1: Failing test — badge text for zero tasks is "no tasks yet"**

Create `cmd/serf-hub/jstest/test-tasks-panel.js` (JSDOM; mirror the bootstrap of a sibling renderer-panels jstest). Provide a `[data-tasks-trigger]` button containing a `[data-task-status-text]` span, call the exported `updateTasksBadge`/`renderTasksInto` accessor for an empty list, and assert:

```js
const textEl = window.document.querySelector("[data-task-status-text]");
// After rendering an empty task list, the status text must read "no tasks yet".
renderTasksInto(panel, []);
assert.strictEqual(textEl.textContent, "no tasks yet",
  "empty task list badge should read 'no tasks yet', not the terminal 'no tasks'");
```

(Reach `renderTasksInto`/`updateTasksBadge` through whatever accessor the sibling renderer-panels jstests use; if none is exported, drive it through the public renderer entry point the panel test already uses.)

- [ ] **Step 2: Run — fails** (current text is `"no tasks"`)

Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-tasks-panel.js`
Expected: FAIL — got `"no tasks"`, want `"no tasks yet"`.

- [ ] **Step 3: Implement**

In `renderer-panels.js` line 375, change `if (total === 0) textEl.textContent = "no tasks";` to:

```js
        if (total === 0) textEl.textContent = "no tasks yet";
```

- [ ] **Step 4: Run — pass**

Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-tasks-panel.js`
Expected: PASS

- [ ] **Step 5: Lint + commit**

Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh`
Commit:
- `git add cmd/serf-hub/assets/renderer-panels.js cmd/serf-hub/jstest/test-tasks-panel.js`
- `git commit -m "fix(web): task badge reads 'no tasks yet', matching the panel"`

_~5 loc._

### Copy T3: Retire the 5s task badge poller (client half)

`startTaskBadgePoller()` re-fetches `/tasks` every 5s (renderer.js:466-483) to keep the badge fresh — the transport straggler the design calls out. Correctness T9 makes the badge event-driven via `serf/task/updated`. This task removes the interval; the initial hydrate fetch (`hydrateDescriptions`, kept) plus the T9 subscription cover freshness. **Sequence this AFTER Correctness T9** so the badge is never stale between removing the poll and wiring the event.

**Files:**
- Modify: `cmd/serf-hub/assets/renderer.js`
- Test: `cmd/serf-hub/jstest/test-task-updated-subscription.js` (the T9 test also asserts no 5s interval is armed)

- [ ] **Step 1: Failing/guard test — no 5s poll interval after attach**

Extend the T9 subscription test (Correctness T9) with a guard: stub `setInterval` to record intervals, attach the renderer, and assert no interval of 5000ms is created for tasks:

```js
const intervals = [];
window.setInterval = (fn, ms) => { intervals.push(ms); return 0; };
// ... attach renderer to a session ...
assert.ok(!intervals.includes(5000),
  "task badge must be event-driven — no 5s poll interval after attach");
```

- [ ] **Step 2: Run — fails** (the 5s poller is still armed)

Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-task-updated-subscription.js`
Expected: FAIL — intervals include 5000.

- [ ] **Step 3: Implement — drop the interval, keep the one-shot hydrate**

In `renderer.js`, delete the `this.startTaskBadgePoller();` call (~241) and remove the `setInterval(tick, 5000)` line from `startTaskBadgePoller` — collapse the method to its one-shot `tick()` (rename to `refreshTaskBadgeOnce` for honesty) or delete it entirely if the initial `hydrateDescriptions().then(...)` already seeds the badge. Confirm `applyTasks` is still called on the initial hydrate so the badge paints once at attach; the T9 subscription keeps it fresh thereafter.

- [ ] **Step 4: Run — pass**

Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-task-updated-subscription.js`
Expected: PASS

- [ ] **Step 5: Lint + commit**

Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh`
Commit:
- `git add cmd/serf-hub/assets/renderer.js cmd/serf-hub/jstest/test-task-updated-subscription.js`
- `git commit -m "perf(web): retire the 5s task-badge poll (event-driven via serf/task/updated)"`

_~15 loc._

### Copy T4: Recent-prompts desktop truncation (2-line clamp on the base rule)

The 2-line `-webkit-line-clamp` for `.spawn-recent-row` lives only inside the phone media query (style.css:4122-4127, `@media (max-width:767px)` opened at :3782); the base rule (style.css:3128) has no clamp, so desktop renders full multi-line walls of prompt text. Move the clamp to the base rule so it applies on every width.

**Files:**
- Modify: `cmd/serf-hub/assets/style.css`
- Test: `cmd/serf-hub/jstest/test-spawn-recent-clamp.js` (new — asserts the base rule carries the clamp)

- [ ] **Step 1: Failing test — base .spawn-recent-row rule has the clamp**

Create `cmd/serf-hub/jstest/test-spawn-recent-clamp.js`. Read `style.css` as text, isolate the **base** `.spawn-recent-row {` rule (the first occurrence, before the `@media (max-width: 767px)` block opened at the `3782`-area marker), and assert it contains `-webkit-line-clamp: 2`:

```js
const fs = require("fs");
const css = fs.readFileSync(__dirname + "/../assets/style.css", "utf8");
const phoneAt = css.indexOf("@media (max-width: 767px)");
const base = css.slice(0, phoneAt);
if (!/\.spawn-recent-row\s*\{[^}]*-webkit-line-clamp:\s*2/.test(base)) {
  console.log("FAIL: base .spawn-recent-row must carry the 2-line clamp (desktop truncation)");
  process.exit(1);
}
console.log("PASS: recent-prompts clamp applies on desktop");
process.exit(0);
```

- [ ] **Step 2: Run — fails** (clamp lives only in the phone block)

Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-spawn-recent-clamp.js`
Expected: FAIL.

- [ ] **Step 3: Implement — clamp on the base rule**

In `style.css`, extend the base `.spawn-recent-row` rule (line 3128) with the clamp properties, and delete the now-redundant clamp block from the phone media query (lines 4122-4127):

```css
.spawn-recent-row { display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; padding: var(--space-3) var(--space-4); border-radius: var(--radius-md); color: var(--text); cursor: pointer; font-size: var(--text-sm); text-decoration: none; }
```

(Base `display: block` becomes `display: -webkit-box` to enable the clamp; the row is a full-width anchor either way. Remove the whole `.spawn-recent-row { display:-webkit-box; -webkit-line-clamp:2; ... }` block inside `@media (max-width:767px)` since the base now covers it.)

- [ ] **Step 4: Run — pass**

Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-spawn-recent-clamp.js`
Expected: PASS

- [ ] **Step 5: Lint + commit**

Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh`
Commit:
- `git add cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-spawn-recent-clamp.js`
- `git commit -m "fix(web): clamp recent-prompt rows to 2 lines on desktop too"`

_~8 loc._

### Copy T5: Light-theme errored sidebar tint (WS1 leftover)

The dark theme tints `.sb-row[data-state="awaiting|active|warning"]` with an explicit `[data-theme="light"]` override (style.css:211-214), but the **errored** row has no light-theme override — so a light-mode errored row falls through to the base 5%-error-mix (style.css:605-608) which reads too faint against the light `--bg`. Add the parallel light-theme errored tint.

**Files:**
- Modify: `cmd/serf-hub/assets/style.css`
- Test: `cmd/serf-hub/jstest/test-errored-light-tint.js` (new — asserts the light-theme errored override exists)

- [ ] **Step 1: Failing test — light-theme errored override present**

Create `cmd/serf-hub/jstest/test-errored-light-tint.js`:

```js
const fs = require("fs");
const css = fs.readFileSync(__dirname + "/../assets/style.css", "utf8");
if (!/\[data-theme="light"\]\s+\.sb-row\[data-state="errored"\]/.test(css)) {
  console.log("FAIL: light theme needs an explicit errored sidebar-row tint (parallel to awaiting/active/warning)");
  process.exit(1);
}
console.log("PASS: light-theme errored tint present");
process.exit(0);
```

- [ ] **Step 2: Run — fails**

Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-errored-light-tint.js`
Expected: FAIL.

- [ ] **Step 3: Implement — add the override**

In `style.css`, after the existing light-theme row overrides (line 214), add:

```css
[data-theme="light"] .sb-row[data-state="errored"] { background: color-mix(in srgb, var(--error) 12%, transparent); }
```

(12% matches the sibling awaiting/active/warning light-theme tints, which lift the base 5% mix to a legible level against the light background.)

- [ ] **Step 4: Run — pass**

Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-errored-light-tint.js`
Expected: PASS

- [ ] **Step 5: Lint + commit**

Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh`
Commit:
- `git add cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-errored-light-tint.js`
- `git commit -m "fix(web): light-theme errored sidebar-row tint (WS1 leftover)"`

_~5 loc._

### Copy T6: Settings copy staleness (literal stale strings only)

The `general.html` settings pane says the bearer token is "Rotated daily; ... regenerates the token on each daemon restart" (general.html:12), but the token is generated once and persisted at `$hub_state_root/auth-token`, invalidated only by deleting the file or `--rotate-auth-token` (auth_token.go:19-29, 43-77). It is **not** rotated daily, and it survives daemon restarts. Fix the literal stale copy. (The systemic vocabulary standardization is Track A; this is one wrong sentence.)

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings/general.html`
- Test: `cmd/serf-hub/web_test.go` (new — renders the general settings partial, asserts the corrected copy)

- [ ] **Step 1: Failing test — token help does not claim daily rotation**

```go
func TestWeb_SettingsGeneralBearerTokenCopyIsAccurate(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Past: hubcore.NewPastIndex("")})
	req := httptest.NewRequest(http.MethodGet, "/_partials/settings/general", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "Rotated daily") {
		t.Fatalf("bearer-token help still claims daily rotation (it is generated once and persisted):\n%s", body)
	}
	if strings.Contains(body, "regenerates the token on each daemon restart") {
		t.Fatalf("bearer-token help still claims per-restart regeneration (the token persists):\n%s", body)
	}
}
```

- [ ] **Step 2: Run — fails**

Run: `go test ./cmd/serf-hub/ -run TestWeb_SettingsGeneralBearerTokenCopyIsAccurate -count=1`
Expected: FAIL — body contains the stale strings.

- [ ] **Step 3: Implement — accurate token help**

In `general.html` line 12, replace:

```
<p class="help">Rotated daily; copy when authenticating the TUI or a remote browser. The hub regenerates the token on each daemon restart.</p>
```

with copy that matches `auth_token.go`:

```
<p class="help">Long-lived; copy when authenticating the TUI or a remote browser. Persisted at <code>auth-token</code> in the hub state dir and reused across restarts; delete that file (or run with <code>--rotate-auth-token</code>) to invalidate existing sessions.</p>
```

- [ ] **Step 4: Run — pass**

Run: `go test ./cmd/serf-hub/ -run TestWeb_SettingsGeneralBearerTokenCopyIsAccurate -count=1`
Expected: PASS

- [ ] **Step 5: Lint + commit**

Run: `go vet ./cmd/serf-hub/ && go test ./cmd/serf-hub/ -count=1`
Commit:
- `git add cmd/serf-hub/templates/partials/settings/general.html cmd/serf-hub/web_test.go`
- `git commit -m "fix(web/settings): bearer-token help matches actual token lifecycle"`

_~5 loc._

---

## Correctness

### Correctness T1: `GET /api/sessions/<id>` honors a live rename (WS3 T25 Bug 2)

`apiSessionDetail` seeds Title from `workspaceData` (web_api_tree.go:673-704). For a live serf session `workspaceData` calls `liveTitle(id, le, s.cfg.Past)` (web_workspace.go:280), which prefers the persisted meta name; but a **live rename** goes through `SetThreadName` on the daemon and `refreshRenamedMeta` bumps the past index — yet the live-branch replacement in `apiSessionDetail` (the `hubDetailFromAppThread` path, ~714) overwrites Title from `thread.Name`/`thread.Preview`/`thread.SessionID`, and if the daemon thread's Name is empty the detail falls back to the raw session id, ignoring the freshly-renamed meta. Give the endpoint the same name resolution `/api/tree` uses: when the live thread carries no name, fall back to the past-index meta name (via `liveTitle`) before the raw id.

**Files:**
- Modify: `cmd/serf-hub/web_api_tree.go` (`apiSessionDetail`)
- Test: `cmd/serf-hub/web_api_tree_test.go` (new)

- [ ] **Step 1: Failing test — renamed meta wins over the raw id in session-detail**

```go
func TestAPISessionDetailHonorsRenamedMetaForLiveThread(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID: "01RENAMED", UpdatedAt: time.Now(),
		Name: "my chosen name", NameSource: "user",
		Model: "gpt-5", EnvInfo: schema.EnvironmentInfo{WorkingDir: proj},
	}); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 88, Address: "127.0.0.1:4588", WorkingDir: proj, Model: "gpt-5"})
	r := hubcore.NewRoster(runDir, fakeProber{sessionID: "01RENAMED", status: appwire.ThreadStatusIdle})
	r.Refresh()
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: idx})
	// Live daemon thread reports NO name (the rename lives only in meta).
	web.sources.Add(&scriptedAppSource{
		id: "local",
		thread: appwire.Thread{
			ID: "01RENAMED", SessionID: "01RENAMED", Source: "local",
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}, CWD: proj,
			Serf: appwire.SerfThread{Ref: "local:01RENAMED", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
	})
	detail, ok := web.apiSessionDetail("01RENAMED")
	if !ok {
		t.Fatal("apiSessionDetail: not found")
	}
	if detail.Title != "my chosen name" {
		t.Fatalf("Title=%q, want the renamed meta name (not the raw id)", detail.Title)
	}
}
```

- [ ] **Step 2: Run — fails** (live branch overwrites Title with the raw id when the thread name is empty)

Run: `go test ./cmd/serf-hub/ -run TestAPISessionDetailHonorsRenamedMetaForLiveThread -count=1`
Expected: FAIL — `Title="01RENAMED"`.

- [ ] **Step 3: Implement — meta-name fallback after the live replacement**

In `apiSessionDetail`, immediately after the `detail = appDetail` live replacement block (web_api_tree.go ~722), reconcile the title against the resolved meta name before the raw-id fallback stands:

```go
			// A live rename lands in the persisted meta before the daemon thread
			// reports the new Name; if the live thread carried no name (detail.Title
			// fell back to the session id), prefer the resolved meta name so the
			// session-detail endpoint agrees with /api/tree (WS3 T25 Bug 2).
			if detail.Title == "" || detail.Title == detail.SessionID {
				if le, ok := liveEntryForRoute(s, id); ok {
					if resolved := liveTitle(id, le, s.cfg.Past); resolved != "" {
						detail.Title = resolved
					}
				}
			}
```

If a `liveEntryForRoute` helper does not already exist, inline the roster lookup (`s.cfg.Roster.Find(canonicalRouteID(id))`) guarded by `s.cfg.Roster != nil`; `liveTitle` itself only needs the `LiveEntry` for its non-past fallback and reads the past index for the name, so passing the found entry (or a zero `hubcore.LiveEntry{SessionID: id}`) is sufficient.

- [ ] **Step 4: Run — pass**

Run: `go test ./cmd/serf-hub/ -run 'TestAPISessionDetailHonorsRenamedMetaForLiveThread|TestAPISessionDetail' -count=1`
Expected: PASS

- [ ] **Step 5: Lint + commit**

Run: `go vet ./cmd/serf-hub/ && go test ./cmd/serf-hub/ -count=1`
Commit:
- `git add cmd/serf-hub/web_api_tree.go cmd/serf-hub/web_api_tree_test.go`
- `git commit -m "fix(hub): session-detail endpoint honors a live rename (WS3 T25 Bug 2)"`

_~20 loc._

### Correctness T2: Reconnect-recovery gets its own hint (WS5)

The diagnostic classifier derives Hint from Source alone (`FromFields` → `defaultForSource`, diagnostic.go:56-67, 89+): a `Source: "mcp"` warning always gets the failure-flavored `mcpFailure()` hint ("An MCP server failed to connect, authenticate, or complete a tool call…"). The `OnReconnect` recovery notice (session_init.go:1341-1347, Title "MCP server reconnected") is **good news** but inherits that failure hint. Give the recovery warning an explicit non-failure Hint at the emit site so enrichment preserves it (`FromFields` keeps a non-empty passed-in Hint, diagnostic.go:64-66).

**Files:**
- Modify: `agent/session_init.go` (the `OnReconnect` callback)
- Test: `agent/diagnostics_test.go` or a new `agent/cov_reconnect_hint_test.go`

- [ ] **Step 1: Failing test — recovery warning keeps a recovery hint, not the failure hint**

Create `agent/cov_reconnect_hint_test.go` (`package agent`). Enrich a reconnect-recovery warning through the same path `emitDiagnosticWarning` uses and assert the hint is not the failure text:

```go
package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
)

func TestReconnectRecoveryWarningKeepsRecoveryHint(t *testing.T) {
	in := reconnectRecoveryWarning("linear")
	out := enrichWarningData(in)
	if strings.Contains(strings.ToLower(out.Hint), "failed to connect") {
		t.Fatalf("recovery warning inherited the MCP-failure hint: %q", out.Hint)
	}
	if strings.TrimSpace(out.Hint) == "" {
		t.Fatalf("recovery warning must carry its own non-empty hint")
	}
	if !strings.Contains(out.Message, "linear") {
		t.Fatalf("recovery message should name the server: %q", out.Message)
	}
}
```

- [ ] **Step 2: Run — fails** (no `reconnectRecoveryWarning` helper; the inline warning carries no Hint, so enrichment fills the failure hint)

Run: `cd agent && go test ./ -run TestReconnectRecoveryWarningKeepsRecoveryHint -count=1`
Expected: FAIL to compile (undefined `reconnectRecoveryWarning`).

- [ ] **Step 3: Implement — a recovery-warning constructor with an explicit hint**

In `session_init.go`, extract the reconnect warning into a helper that stamps a recovery Hint, and call it from `OnReconnect`:

```go
// reconnectRecoveryWarning builds the good-news diagnostic emitted when a
// dropped MCP connection heals itself. It carries its OWN hint so the
// Source-derived classifier (which would otherwise stamp the MCP-FAILURE hint
// on any Source:"mcp" warning) does not make a recovery read like a failure.
func reconnectRecoveryWarning(name string) events.WarningData {
	return events.WarningData{
		Source:  "mcp",
		Title:   "MCP server reconnected",
		Hint:    "The connection dropped and was automatically re-established; the in-flight tool call was retried. No action needed.",
		Message: fmt.Sprintf("MCP server %q reconnected after a dropped connection", name),
	}
}
```

Replace the inline `OnReconnect` body (session_init.go:1341-1347) with `s.emitDiagnosticWarning(reconnectRecoveryWarning(name))`.

- [ ] **Step 4: Run — pass**

Run: `cd agent && go test ./ -run TestReconnectRecoveryWarningKeepsRecoveryHint -count=1`
Expected: PASS

- [ ] **Step 5: Lint + commit**

Run: `cd agent && golangci-lint run ./ ; cd agent && go test ./ -run 'Reconnect|Warning' -count=1`
Commit:
- `git add agent/session_init.go agent/cov_reconnect_hint_test.go`
- `git commit -m "fix(diagnostic): MCP reconnect recovery gets its own non-failure hint (WS5)"`

_~20 loc._

### Correctness T3: T8 un-archive Delete — gate on a real legacy key

`handleAPIArchive`, on un-archiving a project, fires `s.cfg.Archive.Delete("project", filepath.Base(body.ID))` to drop a legacy basename row (web_api_archive.go:43-48). When `body.ID` has no path separator (the common non-migration case), `filepath.Base(id) == id`, so this deletes the very row just written by `Set` above — behaviorally inert only because `Set(archived=false)` already cleared it, but the comment claims it targets a "legacy basename row". Gate the Delete on the id actually differing from its basename, so it only runs when there's a genuine distinct legacy key.

**Files:**
- Modify: `cmd/serf-hub/web_api_archive.go`
- Test: `cmd/serf-hub/web_api_archive_test.go` (new)

- [ ] **Step 1: Failing test — a basename-shaped id does not trigger the legacy Delete**

Extend `web_api_archive_test.go`. Un-archive a project whose id has no separator, and assert the decision store shows the row cleared exactly once (no redundant same-key Delete). The observable contract: after un-archiving `"proj"` (no slash), a subsequent `Set("project","proj",true)` then re-read must reflect archived=true — proving the earlier flow didn't leave a stale delete path. Simpler falsifiable form — assert the code path via a spy store recording Delete calls:

```go
func TestArchiveUnarchiveSkipsRedundantLegacyDeleteForBasenameID(t *testing.T) {
	dir := t.TempDir()
	store := hubcore.NewArchiveStore(filepath.Join(dir, "index.db"))
	web := NewWebServer(hubcore.WebConfig{Archive: store, Past: hubcore.NewPastIndex("")})
	// Pre-archive a basename-shaped project id (no path separator).
	if err := store.Set("project", "proj", true, time.Now()); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/archive", strings.NewReader(`{"kind":"project","id":"proj","archived":false}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	d, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if d[hubcore.ArchiveKey{Kind: "project", ID: "proj"}] {
		t.Fatalf("un-archive must clear the row; decisions=%v", d)
	}
}
```

(This pins the correct end-state; the gate is what makes the intent honest. A stricter spy-store variant asserting `Delete` is called zero times for a basename id is preferable if the suite already has an `ArchiveStore` interface seam — use it if present.)

- [ ] **Step 2: Run — fails or is inconclusive** — first confirm current behavior; if it already passes green, the fix is comment-honesty + the gate (still commit, since the gate removes the misleading no-op).

Run: `go test ./cmd/serf-hub/ -run TestArchiveUnarchiveSkipsRedundantLegacyDeleteForBasenameID -count=1`

- [ ] **Step 3: Implement — gate the legacy Delete on a distinct basename**

In `web_api_archive.go`, replace the unconditional legacy Delete (lines 43-48) with:

```go
	if body.Kind == "project" && !body.Archived {
		// Visible-wins: dropping the path row's archive also drops any legacy
		// basename row that could re-hide this (or a co-basename) project
		// (round-3 G3). Only meaningful when the id is a real path — a
		// basename-shaped id equals its own filepath.Base, so Set above already
		// cleared it and a Delete on the same key would be a no-op (T8).
		if base := filepath.Base(body.ID); base != body.ID {
			_ = s.cfg.Archive.Delete("project", base)
		}
	}
```

- [ ] **Step 4: Run — pass** (and the existing `TestArchiveEndpointProjectKind` path-shaped case still green)

Run: `go test ./cmd/serf-hub/ -run 'TestArchive' -count=1`
Expected: PASS

- [ ] **Step 5: Lint + commit**

Run: `go vet ./cmd/serf-hub/ && go test ./cmd/serf-hub/ -run TestArchive -count=1`
Commit:
- `git add cmd/serf-hub/web_api_archive.go cmd/serf-hub/web_api_archive_test.go`
- `git commit -m "fix(hub): gate un-archive legacy Delete on a real distinct basename (WS3 T8)"`

_~10 loc._

### Correctness T4: T18 ended-rename race — hard-fail instead of silent fallthrough

In the ended-rename path, a session that races back to live is handled by a `Roster.Find` re-check that routes through the daemon; but if the daemon `SetThreadName` fails, the code **silently falls through** to editing the persisted meta directly (web_api_rename.go:53-88), which the next autosave from the now-live session can revert — an atomic-but-lost write. Make the live-race branch hard-fail on daemon error instead of falling through to the meta edit.

**Files:**
- Modify: `cmd/serf-hub/web_api_rename.go`
- Test: `cmd/serf-hub/web_api_rename_test.go` (new or extend)

- [ ] **Step 1: Failing test — a live-race daemon failure returns an error, not a silent meta edit**

```go
func TestRenameLiveRaceDaemonFailureHardFails(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{ID: "01RACE", UpdatedAt: time.Now(), EnvInfo: schema.EnvironmentInfo{WorkingDir: proj}}); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(dir, "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 91, Address: "127.0.0.1:4591", WorkingDir: proj})
	r := hubcore.NewRoster(runDir, fakeProber{sessionID: "01RACE", status: appwire.ThreadStatusIdle})
	r.Refresh() // session reads as live
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: idx})
	// scriptedAppSource.SetThreadName returns Unavailable — the daemon rename fails.
	web.sources.Add(&scriptedAppSource{id: "local", thread: appwire.Thread{ID: "01RACE", SessionID: "01RACE", Source: "local", CWD: proj, Serf: appwire.SerfThread{Ref: "local:01RACE"}}})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/local:01RACE/rename", strings.NewReader(`{"name":"new"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusNoContent {
		t.Fatalf("a failed daemon rename on a live-racing session must NOT silently succeed via a meta edit; status=%d", rec.Code)
	}
	// The persisted meta must not have been edited behind the live session's back.
	meta, err := schema.LoadSessionMeta(proj, "01RACE")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Name == "new" {
		t.Fatalf("meta was edited despite the live-race daemon failure (T18): %+v", meta)
	}
}
```

- [ ] **Step 2: Run — fails** (current code falls through and edits the meta, returning 204)

Run: `go test ./cmd/serf-hub/ -run TestRenameLiveRaceDaemonFailureHardFails -count=1`
Expected: FAIL — status 204 and/or meta.Name == "new".

- [ ] **Step 3: Implement — hard-fail the live-race branch**

In `web_api_rename.go`, change the ended-path live-race block (lines 55-66) so a daemon error returns instead of falling through:

```go
	if s.cfg.Roster != nil {
		if _, live := s.cfg.Roster.Find(canonicalRouteID(id)); live {
			source, err := sourceForThread(s.sources, ref, "")
			if err != nil {
				writeAPIError(w, http.StatusNotFound, "session became live but its source is unavailable")
				return
			}
			if err := source.SetThreadName(r.Context(), appwire.ThreadNameSetParams{Ref: ref, Name: name}); err != nil {
				// The session raced back to live; editing the persisted meta now
				// would be silently reverted by the live session's next autosave
				// (T18). Fail loudly instead of writing a doomed meta edit.
				writeAPIWireError(w, http.StatusBadGateway, err)
				return
			}
			s.refreshRenamedMeta(id, name)
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
```

- [ ] **Step 4: Run — pass**

Run: `go test ./cmd/serf-hub/ -run 'TestRename' -count=1`
Expected: PASS

- [ ] **Step 5: Lint + commit**

Run: `go vet ./cmd/serf-hub/ && go test ./cmd/serf-hub/ -run TestRename -count=1`
Commit:
- `git add cmd/serf-hub/web_api_rename.go cmd/serf-hub/web_api_rename_test.go`
- `git commit -m "fix(hub): ended-rename hard-fails on a live-race daemon error (WS3 T18)"`

_~15 loc._

### Correctness T5: Row-menu edge — a test-run+archived project shows the right verb (WS3 R2)

`projectMenuItems` chooses Archive vs Unarchive from `p.__archived` (sidebar.js:171-183), a marker stamped only inside `pushArchivedSection` (sidebar.js:348-350). A project that is **both** test-run and archived is routed by the server into the Test-runs bucket (TestRuns wins, web_api_tree.go:54-68), where `pushTestRunsSection` passes `null` for the mark (sidebar.js:352) — so its menu offers "Archive" even though the project is archived. The existing `test-sidebar-testruns.js` actually *asserts* plain "Archive" for a test-run project (test-sidebar-testruns.js:137-142) on the rationale that a test-runs project "was never in the archived bucket". That rationale is the bug when the project is genuinely archived: archiving again is an idempotent no-op and hides that it can be un-archived. Fix: drive the menu verb off the server-supplied archived state carried on the wire node, not off which section stamped it.

**Files:**
- Modify: `cmd/serf-hub/assets/sidebar.js` (`projectMenuItems`), `hubapi/types.go` (add `IsArchived` to `TreeProject`), `cmd/serf-hub/web_api_tree.go` (populate it)
- Test: `cmd/serf-hub/jstest/test-sidebar-menu.js` (extend) + update `test-sidebar-testruns.js`'s stale assertion

- [ ] **Step 1: Failing test — an archived test-run project offers Unarchive**

Extend `test-sidebar-menu.js` with a project carrying both the test-run placement and an `is_archived: true` wire flag, and assert the menu offers Unarchive:

```js
// A project routed into Test runs but genuinely archived (is_archived) must
// still offer "Unarchive" — the verb tracks the server's archived state, not
// which section stamped the row (WS3 R2).
tree.test_runs = [{ key: "tr1", name: "e2e", working_dir: "/t/e2e", is_archived: true, default_expanded: true,
  sessions: [{ row_id: "project:tr1:local:0X", ref: "local:0X", session_id: "0X", title: "run", state: "ended", kind: "session", tier: "archived" }] }];
w.SerfSidebar.renderTree(tree);
// expand test-runs section, open the project menu, read items:
const items = /* ...open menu, map .sb-menu-item textContent... */;
if (!items.some((t) => /^Unarchive$/.test(t))) throw new Error("archived test-run project must offer Unarchive, got " + JSON.stringify(items));
```

- [ ] **Step 2: Run — fails** (menu offers Archive; `is_archived` is neither on the wire nor consulted)

Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-sidebar-menu.js`
Expected: FAIL.

- [ ] **Step 3: Implement — carry `is_archived` on the wire and read it in the menu**

In `hubapi/types.go`, add to `TreeProject`:

```go
	IsArchived      bool       `json:"is_archived,omitempty"`
```

In `cmd/serf-hub/web_api_tree.go` `apiTreeProject` (line 507-520), set it from the core project: `IsArchived: p.IsArchived,`. In `sidebar.js` `projectMenuItems`, replace `var archived = !!p.__archived;` with a read that prefers the server flag and falls back to the section stamp:

```js
    var archived = (typeof p.is_archived === "boolean") ? p.is_archived : !!p.__archived;
```

Then update the now-stale `test-sidebar-testruns.js` assertion (lines 137-142): a test-run project that is **not** archived (`is_archived` absent/false) still offers plain "Archive" — keep that case, but its comment (lines 8-10) should note the verb now follows `is_archived`, not bucket placement.

- [ ] **Step 4: Run — pass**

Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-sidebar-menu.js && node test-sidebar-testruns.js`
Expected: PASS

- [ ] **Step 5: Lint + commit**

Run: `go vet ./cmd/serf-hub/ && cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh`
Commit:
- `git add hubapi/types.go cmd/serf-hub/web_api_tree.go cmd/serf-hub/assets/sidebar.js cmd/serf-hub/jstest/test-sidebar-menu.js cmd/serf-hub/jstest/test-sidebar-testruns.js`
- `git commit -m "fix(hub): row-menu Unarchive tracks server archived state, not bucket (WS3 R2)"`

_~15 loc._

### Correctness T6: Test the `encodeURIComponent(working_dir)` escaping (WS3 T23)

The sidebar builds project-menu hrefs with `encodeURIComponent(p.working_dir)` for both New-session and Settings (sidebar.js:176-177). Correct but untested. Add a jstest pinning that a `working_dir` with characters needing escaping (spaces, `&`, `#`) is percent-encoded into the navigated URL.

**Files:**
- Test: `cmd/serf-hub/jstest/test-sidebar-workingdir-escape.js` (new)

- [ ] **Step 1: Failing test — special chars in working_dir are percent-encoded**

Create `cmd/serf-hub/jstest/test-sidebar-workingdir-escape.js` (JSDOM; mirror `test-sidebar-menu.js`'s bootstrap). Stub navigation by capturing `window.location.href` assignments, render a project whose `working_dir` is `/w/a b&c#d`, open the menu, click "New session", and assert the captured URL contains the encoded path:

```js
const w = boot();
let navigated = "";
Object.defineProperty(w.location, "href", { set(v) { navigated = v; }, get() { return navigated; }, configurable: true });
const tree = emptyTree();
tree.projects = [{ key: "p1", name: "p", working_dir: "/w/a b&c#d", default_expanded: true, sessions: [] }];
w.SerfSidebar.renderTree(tree);
// open the project menu, click "New session"
// ...
if (!/\/new\?dir=%2Fw%2Fa%20b%26c%23d/.test(navigated)) {
  throw new Error("working_dir must be percent-encoded in the New-session href, got " + navigated);
}
console.log("PASS: working_dir is encodeURIComponent-escaped");
```

- [ ] **Step 2: Run — pass immediately** (this pins existing-correct behavior against regression; the escaping already exists)

Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-sidebar-workingdir-escape.js`
Expected: PASS. If the harness plumbing (menu-open, location stub) needs adjusting, fix the harness until it passes — the assertion contract (encoded URL) is fixed.

- [ ] **Step 3: (no impl change) — regression pin only**

No source change: the test locks in the current correct escaping so a future refactor can't silently drop it.

- [ ] **Step 4: Run — pass**

Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh`
Expected: PASS (all jstests).

- [ ] **Step 5: Commit**

Commit:
- `git add cmd/serf-hub/jstest/test-sidebar-workingdir-escape.js`
- `git commit -m "test(hub): pin encodeURIComponent escaping for working_dir hrefs (WS3 T23)"`

_~40 loc (test only)._

### Correctness T7: Remove stale SourceMCP comments (shipped)

`session_init.go` carries two comments — `// diagnostic.SourceMCP doesn't exist yet (Task 10); literal string until then` (session_init.go:1343, 1351). `SourceMCP` shipped (it's in `agent/internal/diagnostic/diagnostic.go` — `normalizeSource`/`defaultForSource` both branch on it). Remove the stale comments; optionally switch the literal `"mcp"` to `string(diagnostic.SourceMCP)` for one source of truth. Recovery-hint code from Correctness T2 already removed one of the two inline sites — this cleans the surviving `pendingMCPWarnings` loop.

**Files:**
- Modify: `agent/session_init.go`
- Test: covered by existing `TestIntg_InitMCP*` (no behavior change); this is a comment/constant cleanup verified by compilation + lint.

- [ ] **Step 1: Implement — drop the stale comment, use the constant**

In `session_init.go`, the `pendingMCPWarnings` append (line ~1350-1354) becomes:

```go
	for _, o := range append(connectOutcomes, regOutcomes...) {
		s.pendingMCPWarnings = append(s.pendingMCPWarnings, events.WarningData{
			Source:  string(diagnostic.SourceMCP),
			Title:   "MCP server unavailable",
			Message: fmt.Sprintf("MCP server %q failed to %s: %v", o.Name, o.Stage, o.Err),
		})
	}
```

Add `"primeradiant.com/serf/agent/internal/diagnostic"` to the import block if not present. If Correctness T2's `reconnectRecoveryWarning` used the literal `"mcp"`, switch it to `string(diagnostic.SourceMCP)` too for consistency.

- [ ] **Step 2: Run — existing MCP init tests stay green**

Run: `cd agent && go test ./ -run 'TestIntg_InitMCP|MCPWarning' -count=1`
Expected: PASS (no behavior change; enrichment already recognizes `"mcp"`).

- [ ] **Step 3: Lint + commit**

Run: `cd agent && golangci-lint run ./ && go test ./ -run MCP -count=1`
Commit:
- `git add agent/session_init.go`
- `git commit -m "chore(agent): drop stale 'SourceMCP doesn't exist yet' comments (it shipped)"`

_~5 loc._

### Correctness T8: `conn.reconnecting` clear → `defer`

`reconnect()` clears `c.reconnecting` on the failure branch under a re-taken lock (manager.go:537-544), and `swap()` clears it on the success path (manager.go:557-560). Two clear sites for one flag is a maintenance hazard: a future early-return could skip the clear and wedge the conn (every subsequent call sees `reconnecting == true` and bails). Consolidate to a single `defer` in `reconnect`, so the flag is always cleared however the function exits.

**Files:**
- Modify: `agent/internal/mcp/manager.go`
- Test: `agent/internal/mcp/cov_reconnect_test.go` (existing reconnect matrix must stay green under `-race`)

- [ ] **Step 1: Failing/guard test — a reconnect leaves `reconnecting` false on every exit path**

Add to `cov_reconnect_test.go` a case that drives a reconnect whose dial fails, then asserts a *subsequent* reconnect is attempted after the backoff — i.e. the flag was cleared (not wedged). If the existing `TestReconnect_FailedReconnect_BackoffSuppressesImmediateRetry` already proves the flag clears on the failure path, add a narrower unit asserting the field directly after a failed reconnect:

```go
func TestReconnect_ClearsReconnectingFlagOnFailure(t *testing.T) {
	c := &conn{status: "connected", client: nil}
	c.dial = func(context.Context) (mcpsdk.Transport, error) { return nil, errors.New("dial refused") }
	_, ok := c.reconnect(context.Background())
	if ok {
		t.Fatal("reconnect should fail when dial fails")
	}
	c.mu.Lock()
	stuck := c.reconnecting
	c.mu.Unlock()
	if stuck {
		t.Fatal("reconnecting flag left set after a failed reconnect — a future call would wedge")
	}
}
```

- [ ] **Step 2: Run — passes today** (both current clear sites cover this) — this is the regression pin that must survive the refactor.

Run: `cd agent && go test ./internal/mcp/ -run TestReconnect_ClearsReconnectingFlagOnFailure -race -count=1`
Expected: PASS.

- [ ] **Step 3: Implement — single deferred clear**

In `reconnect()` (manager.go:498-546), after setting `c.reconnecting = true` and unlocking, add a deferred clear that re-takes the lock:

```go
	c.reconnecting = true
	dial := c.dial
	client := c.client
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.reconnecting = false
		c.mu.Unlock()
	}()
```

Remove the `c.reconnecting = false` assignment from the failure branch (manager.go:538) and from `swap()` (manager.go:560) — the `defer` now owns it. Verify `swap()`'s remaining body (commit/return old session) is unaffected and its doc comment is updated to drop the "clears reconnecting" clause. Take care the `defer`'s lock does not nest inside `swap`'s lock (they run sequentially — swap returns before the deferred clear fires).

- [ ] **Step 4: Run — pass, whole package under -race**

Run: `cd agent && go test ./internal/mcp/ -race -count=1`
Expected: PASS

- [ ] **Step 5: Lint + commit**

Run: `cd agent && golangci-lint run ./internal/mcp/... && go test ./internal/mcp/ -race -count=1`
Commit:
- `git add agent/internal/mcp/manager.go agent/internal/mcp/cov_reconnect_test.go`
- `git commit -m "refactor(mcp): single deferred clear of conn.reconnecting"`

_~15 loc._

### Correctness T9: Emit `serf/task/updated` on task-status change; subscribe the task row

`serf/task/updated` is in the appwire notification catalog constant (`NotifySerfTaskUpdated`, types.go:87) but **emitted by nothing** — `protocol.go:152-154` even documents it as "intentionally absent" from the `Notifications` catalog list. The web keeps the task badge fresh with a 5s poll (retired in Copy T3). Jesse's decision: emit the event on task-status change, subscribe the status row to it, delete the poll.

**Appwire router question (answered):** the topic constant already exists; this adds a **notification emit**, not a new request **method**. Notification emits do not go through the request routers, and the handoff rule ("new METHODS need catalog + both routers in one commit; new struct fields don't") does not cover notifications. The one catalog touch needed: `serf/task/updated` must appear in the `Notifications` slice in `protocol.go` (it currently does not — it's the "intentionally absent" case), so the generated-doc + `TestNotificationCatalogWellFormed` cross-check stays honest. That is a single-slice append in `appwire/protocol.go` plus a new `TaskUpdatedParams` payload type — **no router change**.

**Files:**
- Modify: `agent/session_tools_task.go` (emit an `EventTaskUpdated` after `store.Append`/`store.Update`)
- Modify: `agent/events/events.go` (+ `EventTaskUpdated` kind), `agent/events/payloads.go` (+ `TaskUpdatedData`)
- Modify: `internal/appprojector/appwire_projection.go` (project `EventTaskUpdated` → `NotifySerfTaskUpdated`, mirroring the `EventQueueChanged` case at ~588)
- Modify: `appwire/types.go` (+ `TaskUpdatedParams`), `appwire/protocol.go` (add the `Notifications` catalog entry; drop `NotifySerfTaskUpdated` from the "intentionally absent" prose)
- Modify: `cmd/serf-hub/assets/appwire.js` (`eventsFromNotification`: map `serf/task/updated` → a `TASKS_CHANGED` client event), `cmd/serf-hub/assets/renderer.js` (handle it → refresh the badge)
- Test: `agent/session_tools_task_test.go` (emit), `internal/appprojector/*_test.go` (projection), `cmd/serf-hub/jstest/test-task-updated-subscription.js` (client)

- [ ] **Step 1: Failing test (agent) — updating a task emits EventTaskUpdated**

In `agent/session_tools_task_test.go` (or a new `agent/cov_task_updated_test.go`), drive the task tool's `update` action on a session whose events are drained, and assert an `EventTaskUpdated` carrying the new progress lands on the stream:

```go
func TestTaskTool_UpdateEmitsTaskUpdated(t *testing.T) {
	sess := newTestSessionWithTaskStore(t) // reuse the suite's task-tool harness
	drain := collectEvents(sess)
	appendTasks(t, sess, []taskpkg.TaskInput{{Description: "a", Prompt: "p"}})
	updateTask(t, sess, 1, taskpkg.TaskInProgress)
	if !drain.sawKind(events.EventTaskUpdated) {
		t.Fatal("a task status update must emit EventTaskUpdated")
	}
}
```

(Match the existing task-tool test harness names in `session_tools_task_test.go`; the contract is: a status-changing tool call emits `EventTaskUpdated`.)

- [ ] **Step 2: Run — fails** (`undefined: events.EventTaskUpdated`)

Run: `cd agent && go test ./ -run TestTaskTool_UpdateEmitsTaskUpdated -count=1`
Expected: FAIL to compile.

- [ ] **Step 3: Implement (agent) — the event kind, payload, and emit**

In `agent/events/events.go` add `EventTaskUpdated EventKind = "TASK_UPDATED"`. In `agent/events/payloads.go` add:

```go
// TaskUpdatedData is the payload for an EventTaskUpdated event: the current
// task-list progress after an append or status change, so subscribers refresh
// the task-status row without re-polling.
type TaskUpdatedData struct {
	Total int `json:"total"`
	Done  int `json:"done"`
}
```

In `agent/session_tools_task.go`, after a successful `store.Append` (line ~114) and after a successful `store.Update` (line ~166), emit via `deps.emit`:

```go
	total, done := store.Progress()
	deps.emit(events.EventTaskUpdated, events.TaskUpdatedData{Total: total, Done: done})
```

(`deps.emit` is the toolDeps event hook — session_tool_registry.go:27. The append branch already computes `total, done` for its acknowledgement; reuse it. Emit once per tool call, after the store mutation is committed and any auto-advance re-Update has run, so `Progress()` reflects the final state.)

- [ ] **Step 4: Run — pass (agent emit)**

Run: `cd agent && go test ./ -run TestTaskTool_UpdateEmitsTaskUpdated -count=1`
Expected: PASS

- [ ] **Step 5: Failing test (projector) — EventTaskUpdated projects to serf/task/updated**

In `internal/appprojector/appwire_projection_test.go` (or the suite's projection test file), feed an `EventTaskUpdated` and assert one `NotifySerfTaskUpdated` notification with the mapped params:

```go
func TestProject_TaskUpdated(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{Data: events.TaskUpdatedData{Total: 3, Done: 1}})
	if len(out) != 1 || out[0].Method != appwire.NotifySerfTaskUpdated {
		t.Fatalf("want one serf/task/updated notification, got %+v", out)
	}
}
```

- [ ] **Step 6: Run — fails** (no projection case)

Run: `go test ./internal/appprojector/ -run TestProject_TaskUpdated -count=1`
Expected: FAIL.

- [ ] **Step 7: Implement (wire + projector)**

In `appwire/types.go` add:

```go
// TaskUpdatedParams is the params shape for serf/task/updated: the session's
// task-list progress after a change, so a client refreshes the status row
// event-driven instead of polling serf/tasks/list.
type TaskUpdatedParams struct {
	ThreadID string `json:"threadId,omitempty"`
	Ref      string `json:"ref,omitempty"`
	Total    int    `json:"total"`
	Done     int    `json:"done"`
}
```

In `appwire/protocol.go`, add to the `Notifications` slice (and edit the prose at lines 152-154 to drop `NotifySerfTaskUpdated` from the "intentionally absent" list, leaving only `NotifySerfContextPressure`):

```go
	{NotifySerfTaskUpdated, TaskUpdatedParams{}, "The session's task-list progress (total/done) changed."},
```

In `internal/appprojector/appwire_projection.go`, add a case alongside `EventQueueChanged` (~588):

```go
	case events.EventTaskUpdated:
		p.clearSkillCandidate()
		data := eventData[events.TaskUpdatedData](event.Data)
		return []AppNotification{p.notification(appwire.NotifySerfTaskUpdated, appwire.TaskUpdatedParams{
			ThreadID: p.threadID,
			Ref:      p.ref,
			Total:    data.Total,
			Done:     data.Done,
		})}
```

- [ ] **Step 8: Run — pass (projector), and regenerate the protocol doc**

Run: `go test ./internal/appprojector/ -run TestProject_TaskUpdated -count=1 && go test ./appwire/ -count=1 && make generate`
Expected: PASS; `make generate` updates `docs/appwire-protocol.md` to list `serf/task/updated`. Commit the regenerated doc.

- [ ] **Step 9: Failing test (client) — a serf/task/updated notification refreshes the badge, no 5s poll**

Create `cmd/serf-hub/jstest/test-task-updated-subscription.js` (JSDOM). Stub `SerfAppwire` with an `onNotification` hook and `eventsFromNotification`, attach the renderer to a session, deliver a `serf/task/updated` notification with `{total:3, done:1}`, and assert the badge text updates to `1/3` (and — the Copy T3 guard — no 5000ms interval was armed):

```js
const intervals = [];
window.setInterval = (fn, ms) => { intervals.push(ms); return 0; };
// attach renderer, deliver notification via the captured onNotification handler:
deliver("serf/task/updated", { threadId: "01S", total: 3, done: 1 });
const textEl = window.document.querySelector("[data-task-status-text]");
assert.ok(/1\/3/.test(textEl.textContent), "badge must reflect the pushed task progress");
assert.ok(!intervals.includes(5000), "no 5s task poll after wiring the event");
```

- [ ] **Step 10: Run — fails** (no client mapping/handler yet; and the 5s poll still armed until Copy T3)

Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-task-updated-subscription.js`
Expected: FAIL.

- [ ] **Step 11: Implement (client mapping + handler)**

In `appwire.js` `eventsFromNotification` (~810), add a branch:

```js
    if (method === "serf/task/updated") {
      return [["TASKS_CHANGED", {
        total: typeof params.total === "number" ? params.total : 0,
        done: typeof params.done === "number" ? params.done : 0,
      }]];
    }
```

In `renderer.js`'s `handleData` switch (alongside `QUEUE_CHANGED`, ~1003), add:

```js
        case "TASKS_CHANGED":
          // Event-driven task-status row (retires the 5s poll): the daemon
          // pushes total/done on every task mutation. Refetch the full list
          // once to refresh the panel's per-row detail, but update the badge
          // immediately from the pushed counts so the status row never lags.
          updateTasksBadge(data.done, data.total, "");
          if (window.SerfAppwire) {
            window.SerfAppwire.tasks(this.sessionId).then(tasks => this.applyTasks(tasks)).catch(() => {});
          }
          break;
```

(This lands **before** Copy T3 removes the poll, so between the two commits the badge is doubly-fresh, never stale.)

- [ ] **Step 12: Run — pass**

Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-task-updated-subscription.js`
Expected: PASS (the no-5s-interval assertion still fails until Copy T3 — sequence Copy T3 immediately after this task; if you want this task self-green, drop the interval assertion here and let Copy T3 add it).

- [ ] **Step 13: Full gates + commit**

Run: `cd agent && go test ./ -run 'Task' -count=1 && cd .. && go test ./internal/appprojector/ ./appwire/ ./cmd/serf-hub/ -count=1 && cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh`
Commit (one commit — wire+emit+projector+client together, since the notification threads all hops):
- `git add agent/events/events.go agent/events/payloads.go agent/session_tools_task.go agent/session_tools_task_test.go internal/appprojector/appwire_projection.go internal/appprojector/appwire_projection_test.go appwire/types.go appwire/protocol.go docs/appwire-protocol.md cmd/serf-hub/assets/appwire.js cmd/serf-hub/assets/renderer.js cmd/serf-hub/jstest/test-task-updated-subscription.js`
- `git commit -m "feat(tasks): emit serf/task/updated on task-status change; subscribe the status row"`

_~90 loc._

### Correctness T10: De-flake `TestReconnect_FailedReconnect_BackoffSuppressesImmediateRetry`

The test (cov_reconnect_test.go:688-745) is load-sensitive under full-suite parallelism: it primes a call, closes the server, and asserts the second post-drop call does not redial (dial count stays 2) because the failure-branch backoff (`reconnectBackoff`) suppresses it. Under heavy parallelism the second call can slip past a too-short backoff window. Make it deterministic with an injectable clock so the backoff window is controlled, not wall-clock-raced.

**Files:**
- Modify: `agent/internal/mcp/manager.go` (inject a `now func() time.Time` on `conn`/`Manager`, defaulting to `time.Now`, used for `backoffUntil` comparisons)
- Modify: `agent/internal/mcp/cov_reconnect_test.go` (drive the clock)

- [ ] **Step 1: Failing/flaky-prone test — make the backoff deterministic**

Rewrite `TestReconnect_FailedReconnect_BackoffSuppressesImmediateRetry` to install a fake clock on the manager/conn and advance it explicitly, so the "still within backoff" assertion no longer depends on real elapsed time:

```go
	fakeNow := time.Unix(0, 0)
	clock := func() time.Time { return fakeNow }
	mgr, outcomes := NewManagerWithClock(ctx, configs, dials, clock) // new test seam
	// ... prime, close server ...
	// first post-drop call: fails, sets backoffUntil = fakeNow + reconnectBackoff
	// (clock does NOT advance) → second call is strictly within the window:
	if _, isErr := execProbe(ctx, reg, t); !isErr { t.Fatal("expected failure") }
	if got := atomic.LoadInt32(&dialCalls); got != 2 {
		t.Fatalf("backoff must suppress the immediate 3rd redial, got %d dials", got)
	}
```

- [ ] **Step 2: Run — fails** (`NewManagerWithClock` undefined; `backoffUntil` uses `time.Now` directly)

Run: `cd agent && go test ./internal/mcp/ -run TestReconnect_FailedReconnect_BackoffSuppressesImmediateRetry -count=1`
Expected: FAIL to compile.

- [ ] **Step 3: Implement — injectable clock**

Add a `now func() time.Time` field to `conn` (and a `NewManagerWithClock` test-seam constructor that sets it on every conn; the production `NewManager` delegates with `time.Now`). Replace the three `time.Now()` reads in the reconnect/backoff path (manager.go:500 `time.Now().Before(c.backoffUntil)`, :541 `c.lastErrAt = time.Now()`, :542 `c.backoffUntil = time.Now().Add(reconnectBackoff)`) with `c.now()`. Keep `time.Now` as the default so production is unchanged; only the test drives the fake clock. Guard the new field with a `// serf:naming-ignore:` line only if namingcheck flags `now` (it should not — it's an unexported field).

- [ ] **Step 4: Run — pass, repeatedly and under -race**

Run: `cd agent && go test ./internal/mcp/ -run TestReconnect_FailedReconnect_BackoffSuppressesImmediateRetry -race -count=20`
Expected: PASS 20/20 (deterministic — no wall-clock dependence).

- [ ] **Step 5: Lint + commit**

Run: `cd agent && golangci-lint run ./internal/mcp/... && go test ./internal/mcp/ -race -count=1`
Commit:
- `git add agent/internal/mcp/manager.go agent/internal/mcp/cov_reconnect_test.go`
- `git commit -m "test(mcp): de-flake backoff-suppression reconnect test with an injectable clock"`

_~35 loc._

---

## Investigate

These two are **observation tasks**: run the built app live and gather evidence. Any code they produce is small and folds into this track; the expected outcome may be "reads right, no change" — record that verdict either way. Use the e2e-scenario-testing skill: build a fresh instance, isolated fake `$HOME` (real `~/.serf` untouched), dedicated Chrome profile, falsifiable observations.

### Investigate T1: Project last-touched ordering feel (LastActivity)

WS3 pinned project ordering to `LastActivity` (the max `OrderUpdatedAt` across a project's top-level sessions; tree.go:597-601 sorts active projects `LastActivity` desc). WS2 landed honest `CreatedAt`. The question: does the sidebar's project order *feel* right now — most-recently-touched project on top — or does a stale/quiet project float above a just-active one?

**Files:** none expected (investigation). If evidence shows a wrong-order case, the fix folds into `cmd/serf-hub/internal/hubcore/tree.go` ordering.

- [ ] **Step 1: Build + launch a fresh hub against an isolated fake `$HOME`**

Follow e2e-scenario-testing: `make build-hub`, run with `HOME=<tmp>` so a real index isn't touched. Spawn 3-4 sessions across 2-3 project dirs at staggered times; send a turn to the *oldest-created* project's session last (so its LastActivity is now newest).

- [ ] **Step 2: Observe the sidebar project order (falsifiable)**

Load the web UI; record the top-to-bottom project order and each project's rendered `Age` chip. Falsification: the project whose session was just touched must be at the top; a project with only old activity must not outrank it. Capture the `/api/tree` JSON (`GeneratedAt`, each project's implied order) as evidence.

- [ ] **Step 3: Verdict**

Write the verdict into the plan's execution notes (or a scenario card under `test/scenarios/`): "ordering reads right — no change" OR a specific mis-order with the two projects' `LastActivity` values. Only if wrong: open a follow-up red-first test in `tree_test.go` pinning the correct order and adjust the comparator. No speculative change.

- [ ] **Step 4: Commit (only if a card or fix was produced)**

Commit:
- `git add test/scenarios/<card>.md` (and any tree.go fix + test)
- `git commit -m "test(hub): project last-touched ordering scenario card (LastActivity feel-check)"`

_~0–40 loc._

### Investigate T2: Spawn-failure UX re-verify (post-WS5)

WS5 removed the main MCP-fatal cause of buried-stderr HTTP 500s on spawn (a dead MCP server no longer kills the session). Re-check what spawn-failure surfaces remain now: does a genuinely bad spawn (missing binary, un-writable cwd, bad model id) surface a *legible* error in the web spawn flow, or does a raw 500 / silent hang still leak through?

**Files:** none expected. Any fix folds into the spawn error path (`cmd/serf-hub/web_spawn.go` / the spawn handler) as a small follow-up.

- [ ] **Step 1: Provoke the failure classes live**

On a fresh hub (isolated `$HOME`), attempt spawns that must fail: (a) a non-existent model id, (b) a working dir that doesn't exist / isn't writable, (c) a harness binary not on PATH. Use the real web spawn form.

- [ ] **Step 2: Observe the surfaced error (falsifiable)**

For each, record what the user sees: a named, actionable error banner (good) vs. a bare `HTTP 500` / spinner-that-never-resolves / blank (bad). Capture the response body and any console/network error as evidence.

- [ ] **Step 3: Verdict**

Record per-class: "surfaces a legible error — no change" or the specific raw-500/hang with its response body. Only if a class leaks a raw failure: open a red-first test asserting the classified error text and thread it through the spawn error path (mirror WS5's diagnostic classifier approach). No speculative change.

- [ ] **Step 4: Commit (only if a card or fix was produced)**

Commit:
- `git add test/scenarios/<card>.md` (and any spawn-path fix + test)
- `git commit -m "test(hub): spawn-failure UX scenario card (post-WS5 re-verify)"`

_~0–40 loc._

---

## Out of scope for Track D

- **§12 stdio-probe `LookPath`-only limitation:** left as-is by design (a documented limit; a real MCP-handshake probe is explicitly not worth it now). No task.
- Anything Track A owns (attention vocabulary/icons, needs-you rendering, `processing`→`working` display rename, sidebar vocabulary copy). Track D rebases onto it and only touches the composer action-state and row-menu/escaping regions of the shared files.
- Model picker (Track B); metrics/cost + new settings controls (Track C).

## Final gate (before Track D merges)

- [ ] `make test-short` and `make test-race` green across `GO_MODULES`.
- [ ] `make lint` green (namingcheck included — verify any new field/const passes; the `TaskUpdatedParams`/`TaskUpdatedData` additions are snake_case on the wire, PascalCase in Go, no camelCase wire keys).
- [ ] `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh` green.
- [ ] `make generate` leaves `docs/appwire-protocol.md` unchanged (the T9 catalog entry already regenerated + committed).
- [ ] Re-grep the repo for conflict markers and `go vet` the touched packages after the Track A rebase.
- [ ] Both Investigate verdicts recorded (even if "no change").
