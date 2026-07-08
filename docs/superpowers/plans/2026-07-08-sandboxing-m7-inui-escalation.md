# Serf Sandboxing — M7: In-UI Sandbox-Exemption Escalation

> **For agentic workers:** Implement with superpowers:subagent-driven-development,
> task-by-task, red→green→adversarial-verify→commit. Follow the SDD protocol in
> `2026-07-08-sandboxing-m0-master.md`. Design source:
> `docs/superpowers/specs/2026-07-08-sandboxing-design.md` (v4) — the whole
> "M7 — In-UI escalation" section plus the denial-surfacing seam (#6).

**Goal:** Build a **new human-gated approval primitive over AppWire**, for
interactive sessions only. When a sandboxed file/shell tool call is denied in an
interactive root session, the harness raises a one-shot escalation card to the
human ("Agent tried to write `/etc/hosts` — allow this one action?"). **Approve**
re-runs *that single invocation* with *that one path* added **for that invocation
only** (gemini-cli per-invocation expansion — never session-wide, never a
persistent allowlist). **Deny** returns the typed sandbox-denied error to the
model, exactly as a non-interactive session already does. The model can never
trigger, approve, observe, or replay an escalation.

**Why last:** M7 is the terminal milestone. It hooks into the **typed
sandbox-denied errors** produced by M2 (in-process file tools) and M3 (kernel
wrap), and it exercises a *live* sandboxed session end-to-end, so it needs M1–M6
merged and validated first. Nothing in M7 changes what gets denied — it only adds
a human-in-the-loop *re-run* seam on top of an already-final denial. This is
deliberately the narrowest possible approval engine (single action, single
invocation, human-gated); it is the same primitive a future general
tool-approval/allowlist engine would sit on, but **that rich policy engine is
explicitly out of scope** (spec Non-goals; §M7 "the deferred policy engine sits
here later").

**Architecture:** Three planes, one round-trip.

1. **Session plane (`agent/`)** owns the primitive. After `execTool` dispatches a
   call and gets back a result carrying a *typed sandbox-denied error*, a new
   `escalateOnSandboxDenial` step decides: non-interactive / subagent / no live
   human → return the denial unchanged (final); interactive root with a live
   subscriber → register a pending escalation, emit a UI-only event, and **block
   the tool-exec goroutine** on a per-escalation channel. On **approve** it
   re-dispatches the *same* `llm.ToolCallData` with a **per-invocation grant**
   threaded on the context (`ctxSandboxInvocationGrant`), which the execenv/kernel
   layer consults for *this one call*; on **deny** (or turn-interrupt / session
   close) it returns the typed error. The escalation is **never** appended to
   history — only the final tool result (approved re-run output *or* typed denial)
   enters the model's context.
2. **Wire plane (`appwire/`)** adds one server→client notification
   (`serf/sandbox/escalation/requested`) carrying the card payload, and one
   client→server request (`serf/sandbox/escalation/resolve`) carrying the
   decision. Both are `ScopeBoth` (daemon serves; hub relays). Params/response
   types + method/notification name constants + catalog rows; docs regenerate via
   `make generate`; `protocol_test.go` cross-checks the catalog against both
   routers.
3. **Transport plane (`server/`, `cmd/serf/serve.go`, `cmd/serf-hub/`,
   `assets/`)** wires the round-trip: the daemon projects the escalation event to
   the notification (`internal/appprojector`), registers a resolve handler that
   calls a new session callback (`SetSandboxEscalationResolveFunc`), the hub
   relays both directions through the `appsource.Source` interface, and the web
   client maps the notification to a card and posts the decision.

**The closest existing pattern — and where it diverges.** `ask_user`
(`agent/session_tools_ask.go`) is the nearest thing serf has to a
human-in-the-loop prompt, and M7 adapts its *surfacing* (a card projected from a
session event, gated by the same `NonInteractive || isSubagentSession()` check,
rendered by `renderer.js`). But its **control flow is the opposite** and must not
be reused:

| | `ask_user` | M7 escalation |
|---|---|---|
| Initiator | **model** (a tool the model calls) | **harness** (reaction to a denial; no tool) |
| Turn | **ends** the turn; answer arrives in the *next* user turn | **blocks** mid-tool; answer unblocks the same call |
| Model visibility | **visible** — the `ask_user` call + its questions are a transcript turn the model reads | **invisible** — never a `schema.Turn`; only the final tool result enters history |
| Answer channel | user's next `turn/start` input | a **new** `escalation/resolve` request that targets a blocked waiter |

So serf has **no** request/response harness-prompt today: `ask_user` is
fire-and-forget and model-initiated (wrong trust direction), and PreToolUse hooks
can only *deny* — `hooks.go:614-635` recognizes `permissionDecision: "ask"`/
`"defer"` but explicitly **does not honor** them ("serf has no interactive
permission prompt; the tool will proceed"). M7 builds that missing primitive.

**Tech Stack:** Go 1.25, stdlib. No new external dep. The blocking wait is a
`select` over a per-escalation `chan escalationDecision` and `ctx.Done()`. Wire
types are plain structs with snake_case JSON tags. Web is vanilla JS in
`cmd/serf-hub/assets/` (no build step). Table-driven `testing`; a real
`serf serve` daemon + `appwire.Client` for the round-trip e2e; a live bwrap
sandbox for the file/shell approve/deny e2e (Linux; the branch is Linux-only —
M6/Seatbelt parity for escalation is a documented follow-up, not an M7 task).

**Anchors** (verified 2026-07-08 against the live tree; **re-verify** before
editing — M2–M6 will have shifted line numbers, and M7 branches off *after* they
land):

- `agent/session_tools.go:296` `execTool`; **`:431` `res = s.reg.ExecuteCall(ctx,
  s.currentEnv(), call)`** — the single dispatch point M7 wraps. `:326`/`:333`
  the PreToolUse deny-only path (proves denies exist but re-runs do not).
- `agent/internal/hooks/hooks.go:601` `RunPreToolUse`; `:614-635` the `ask`/
  `defer` "not honored — no interactive permission prompt" branch.
- `agent/session_tools_ask.go:45` `isSubagentSession`; `:128` `registerAskTool`;
  **`:136` the gate `s.cfg.NonInteractive || s.isSubagentSession()`** — mirror it.
- `agent/events/events.go` `EventKind` taxonomy (add
  `EventSandboxEscalationRequested`; there are 35 kinds today).
- `internal/appprojector/appwire_projection.go:87` `Project` — add a case mapping
  the new event to the notification.
- `appwire/protocol.go:100-146` `Methods`, `:155-179` `Notifications`;
  `appwire/types.go:24` method-name consts, `:89` notification-name consts.
- `server/appwire_runtime.go:44` `RecordAppEvent` (event→project→broadcast path);
  `:81` `registerAppWireHandlers` (add the daemon resolve handler).
- `server/server.go:154` `steerFunc` field / `:302` `SetSteerFunc` (add the
  escalation-resolve callback beside it). `internal/appserver/server.go:94`
  `SubscriberCount(threadID)` — the "is a human actually watching" probe.
- `cmd/serf/serve.go:326` `srv.SetSteerFunc(...)` — wire
  `srv.SetSandboxEscalationResolveFunc(...)` alongside it.
- `cmd/serf-hub/app_rpc.go:429` the `MethodTurnSteer` hub relay (copy its shape);
  `cmd/serf-hub/internal/appsource/source.go:9` `Source` interface, `:18`
  `SteerTurn`; impls `local_daemon.go:127`, `codex_source.go:292`.
- `cmd/serf-hub/assets/appwire.js:811` `eventsFromNotification`;
  `cmd/serf-hub/assets/renderer.js:~4372` the `ask_user` card (surfacing template
  to adapt — **not** the control flow).

## Global Constraints

- **M7 changes nothing about *what* is denied.** It only adds a re-run seam on an
  already-typed, already-final denial. If you find yourself editing the
  masking/openat2/bwrap policy, you've left M7 — stop. The only new *decision* is
  approve-vs-deny, and it is made by a human over the wire.
- **The model is fully walled off from the primitive.** No tool exposes it (not
  triggerable). The resolve method is a UI request never advertised to the model
  (not approvable). The escalation is never written as a `schema.Turn` and never
  projected into the transcript the model reads (not observable). Because it is
  not in history, a crashed/resumed session has nothing to replay — a mid-wait
  crash leaves an interrupted tool call that orphan-repair fills with an `IsError`
  placeholder, exactly like an interrupted `ask_user` (spec §6 "an interrupted
  ack-less ask is never pending"). **Every task must defend one of these four
  walls; Task 8 asserts all four.**
- **Per-invocation only.** The grant lives on the *context* of a single
  re-dispatch. It is never stored on the session, never mutates `ResolvedPolicy`,
  never survives the call. Two denials in one round → two independent
  escalations. Approving one path never widens any other call.
- **Immutability floor is untouched (spec Goal).** No approval can *relax the
  session policy*; it can only re-run one action. Subagents/delegates never
  escalate (they have no attached human) — their sandbox denials stay final,
  which also preserves M4's re-rooted-policy guarantees.
- **snake_case** for every JSON/wire key (`make lint` / `serf-namingcheck` gate).
  Regenerate `docs/appwire-protocol.md` with `make generate` after touching the
  catalog; `protocol_test.go` must stay green (catalog ↔ router cross-check).
- Never `git add -A` without a prior `git status`. Stage exact paths.

## The consumed contract (denial-surfacing seam #6, from M2/M3)

M7 reads a typed error the file/kernel layers already return. Before Task 1,
**locate the concrete type** M2/M3 shipped (spec seam #6: "typed, legible tool
error; in-process denials know the path, kernel denials attributed
best-effort"). M7 needs, from a denial:

- `Mode` (the active sandbox mode) and `Tool` (which tool was denied);
- `DeniedPath` — the precise path (file tools) or the best-effort heuristic path
  (shell);
- a `Kind` discriminator: **file-tool** (precise path, *no partial side effects*)
  vs **shell** (*may have partially executed*);
- for the shell kind: the `Command` and the **output-so-far** captured before the
  denial (M3's kernel wrap streams output; the partial buffer is what the card
  shows under the "already partially ran; approving re-runs start-to-finish"
  caveat).

**If M2/M3's error does not already carry `Command` + output-so-far for the shell
kind, adding those fields is an in-scope M7 prerequisite** — do it as the first
commit of Task 1 and coordinate the small M2/M3 edit, because the card cannot be
honest without them. Expose a single predicate `sandbox.AsDenied(err) (*Denied,
bool)` so the session layer never type-switches on internals.

## File Structure

- `agent/sandbox/escalation.go` (new) — the wire-agnostic value types the session
  and projector share: `EscalationRequest{ID, Mode, Tool, Kind, DeniedPath,
  Command, OutputSoFar, PartiallyRan}` and `EscalationDecision` (`approve`/
  `deny`). No AppWire import here (keeps `agent/sandbox` leaf-level).
- `agent/session_escalation.go` (new) — the primitive: a per-session
  `map[string]chan EscalationDecision` guarded by `s.mu`; `escalateOnSandboxDenial`
  (the gate + emit + block); `ResolveSandboxEscalation(id string, approve bool)
  error` (the resolve entry point the daemon callback calls); cancel-all-pending
  on turn interrupt and on `Close`. The `ctxSandboxInvocationGrant` context key +
  `withInvocationGrant(ctx, path)` helper live here.
- `agent/session_escalation_test.go` (new) — unit tests with a fake `sandbox.Denied`
  and a fake env: gate matrix, block/resolve, per-invocation-grant threading,
  interrupt/close cancellation, model-invisibility (nothing appended to history).
- `agent/session_tools.go` (modify, small) — after `:431`, route the result
  through `escalateOnSandboxDenial`; on approve re-dispatch `s.reg.ExecuteCall`
  with the grant ctx. This is the *only* edit to the hot tool path.
- `agent/events/events.go` (modify) — add `EventSandboxEscalationRequested` +
  its payload struct (`SandboxEscalationRequestedData`).
- `appwire/types.go` (modify) — `MethodSerfSandboxEscalationResolve`
  (`"serf/sandbox/escalation/resolve"`) and `NotifySerfSandboxEscalationRequested`
  (`"serf/sandbox/escalation/requested"`) constants; `SandboxEscalationRequested`
  (notification payload) and `SandboxEscalationResolveParams` request type.
- `appwire/protocol.go` (modify) — one `Methods` row (ScopeBoth) + one
  `Notifications` row.
- `internal/appprojector/appwire_projection.go` (modify) — a `case
  events.EventSandboxEscalationRequested` returning the notification (payload
  redacted per seam #6: basename/`<denied>` for denylisted paths, truncated
  command — never file contents).
- `server/appwire_runtime.go` (modify) — register
  `MethodSerfSandboxEscalationResolve` → `handleAppSandboxEscalationResolve`,
  which calls the session callback.
- `server/server.go` (modify) — `sandboxEscalationResolveFunc` field +
  `SetSandboxEscalationResolveFunc`.
- `cmd/serf/serve.go` (modify) — `srv.SetSandboxEscalationResolveFunc(func(id
  string, approve bool) error { return getSession().ResolveSandboxEscalation(id,
  approve) })` next to `SetSteerFunc`.
- `cmd/serf-hub/internal/appsource/source.go` + `local_daemon.go` +
  `codex_source.go` (modify) — add `ResolveSandboxEscalation(ctx,
  SandboxEscalationResolveParams) error` to `Source`; LocalDaemon forwards via
  `client.Request`; Codex returns method-not-supported.
- `cmd/serf-hub/app_rpc.go` (modify) — hub relay handler for the resolve method
  (copy the `MethodTurnSteer` block at `:429`).
- `cmd/serf-hub/assets/appwire.js` (modify) — map the notification in
  `eventsFromNotification`.
- `cmd/serf-hub/assets/renderer.js` (+ `renderer-tools.js`/`style.css` as needed)
  (modify) — render the escalation card (two shapes) and post the decision via the
  resolve request.
- New tests colocated per package: `appwire/…_test.go` (round-trip + catalog),
  `internal/appprojector/…_test.go` (projection + redaction),
  `server/…_test.go` (daemon handler), `cmd/serf-hub/…_test.go` (hub relay + web
  mapping), and the two live e2e tests (Tasks 6–7).

---

## Task 1 — Escalation primitive: gate, block, resolve, per-invocation grant

**Files:** `agent/sandbox/escalation.go` (new), `agent/session_escalation.go`
(new), `agent/session_escalation_test.go` (new); the seam-#6 field addition if
needed (see "consumed contract").

- [ ] **Failing test** (`session_escalation_test.go`, no mocks — a real `Session`
  + a fake env whose file op returns a `sandbox.Denied`):
  - `TestEscalation_GateMatrix`: non-interactive → no escalation (denial
    returned); subagent (`isSubagentSession()`) → no escalation; interactive root
    **with zero subscribers** → no escalation (denial final); interactive root
    **with a live subscriber** → escalation raised. (Mirror the exact predicate
    at `session_tools_ask.go:136`, plus the `SubscriberCount>0` add.)
  - `TestEscalation_ApproveThreadsInvocationGrant`: on approve, the re-dispatched
    call runs with `ctx` carrying `ctxSandboxInvocationGrant == DeniedPath`, and
    nothing on the session is mutated (grep-assert `s` has no post-call grant
    field set).
  - `TestEscalation_DenyReturnsTypedError`: on deny, the result is the original
    typed denial, `IsError`, byte-for-byte.
  - `TestEscalation_InterruptAndCloseCancel`: a blocked escalation resolves to
    deny when `ctx` cancels (turn interrupt) and when `Close()` runs — no
    goroutine leak.
  - `TestEscalation_NeverAppendsHistory`: after approve *and* after deny,
    `len(s.history)` gained exactly the tool round it always would — no extra
    escalation turn.
- [ ] Implement `EscalationRequest`/`EscalationDecision`, the pending-map registry,
  `escalateOnSandboxDenial`, `ResolveSandboxEscalation`, the ctx grant key/helper,
  and interrupt/close cancellation. Block with `select { case d := <-ch: …; case
  <-ctx.Done(): deny }`.
- [ ] **Adversarial verify** (Opus): is the grant *provably* per-invocation (no
  session field, no policy mutation)? Can two concurrent denials in one round
  cross-wire their channels (assert distinct IDs route to distinct waiters)? Does
  a resolve for an unknown/already-resolved ID no-op cleanly rather than panic or
  block? Fix, commit.

## Task 2 — Wrap the dispatch point in `execTool`

**Files:** `agent/session_tools.go` (modify), extend
`session_escalation_test.go`.

- [ ] **Failing test:** `TestExecTool_SandboxDenialEscalatesThenReruns` drives a
  real `execTool` whose env denies the first attempt and succeeds when the grant
  ctx is present; assert the returned `tool.ExecResult` is the *successful* re-run
  output on approve and the typed denial on deny. A regression test asserts a
  **non-sandbox** error path (any other `IsError`) is untouched — escalation fires
  *only* for `sandbox.AsDenied`.
- [ ] Implement: immediately after `res = s.reg.ExecuteCall(ctx, s.currentEnv(),
  call)` (`:431`), `res = s.escalateOnSandboxDenial(ctx, call, res)`. Keep the
  emit/`toolEventsWG` bookkeeping around it correct — the escalation blocks
  *between* dispatch and the `EventToolCallEnd` emit, so the tool item stays "in
  progress" in the UI while the human decides (verify no double-close of the tool
  event).
- [ ] **Adversarial verify:** does a blocked escalation interact badly with
  `abortIfClosing`/`errIfClosing` (the block must be cancellable and must not
  emit a spurious canceled-end before resolving)? Is the re-run's duration/usage
  accounting sane? Fix, commit.

## Task 3 — AppWire types + catalog (notification + resolve method)

**Files:** `appwire/types.go`, `appwire/protocol.go` (modify);
`appwire/protocol_test.go` (extend); regenerate `docs/appwire-protocol.md`.

- [ ] **Failing test:** extend the catalog cross-check so the new method appears
  on **both** routers and the new notification is in the catalog; a
  round-trip/marshal test asserts snake_case JSON for every field of
  `SandboxEscalationRequested` (`escalation_id`, `mode`, `tool`, `kind`,
  `denied_path`, `command`, `output_so_far`, `partially_ran`) and
  `SandboxEscalationResolveParams` (`ref`/`thread_id`, `escalation_id`, `approve`).
- [ ] Implement the two name constants, the two types, the `Methods` row (ScopeBoth,
  `SandboxEscalationResolveParams` → `EmptyResponse`), and the `Notifications`
  row. Run `make generate`; commit the regenerated doc.
- [ ] **Adversarial verify:** is `output_so_far` bounded (a partial-run buffer can
  be large — the type or the projector must truncate)? Does `make lint`
  (namingcheck) pass on every new key? Fix, commit.

## Task 4 — Daemon: project the event out, handle the resolve in

**Files:** `agent/events/events.go`,
`internal/appprojector/appwire_projection.go`, `server/appwire_runtime.go`,
`server/server.go`, `cmd/serf/serve.go` (modify); package tests.

- [ ] **Failing test:**
  - `internal/appprojector`: `TestProject_SandboxEscalationRequested` asserts the
    event projects to exactly the notification, **redacted** per seam #6 (a
    denylisted/secret path becomes basename or `<denied>`; the command is
    truncated; file *contents* never appear).
  - `server`: `TestHandleSandboxEscalationResolve` drives the daemon handler and
    asserts it calls the registered callback with `(id, approve)` and surfaces a
    clean error for an unknown id.
- [ ] Implement: the event kind + payload; the projector case; the daemon
  `HandleTyped(router, MethodSerfSandboxEscalationResolve,
  s.handleAppSandboxEscalationResolve)`; the `server.Server` callback field +
  setter; the `serve.go` wiring to `getSession().ResolveSandboxEscalation`. The
  session emits `EventSandboxEscalationRequested` from `escalateOnSandboxDenial`
  (Task 1) *before* it blocks — so `RecordAppEvent`→`Broadcast` pushes the card,
  then the goroutine waits.
- [ ] **Adversarial verify:** confirm the escalation event is **not** persisted to
  the transcript (it must ride the event stream only, never `appendTurn`). Confirm
  the reconnect-replay buffer (`appNotifier.Record`) carries the pending card so a
  human who reattaches mid-wait still sees it. Fix, commit.

## Task 5 — Hub relay + web card

**Files:** `cmd/serf-hub/internal/appsource/{source.go,local_daemon.go,codex_source.go}`,
`cmd/serf-hub/app_rpc.go`, `cmd/serf-hub/assets/appwire.js`,
`cmd/serf-hub/assets/renderer.js` (+ `renderer-tools.js`/`style.css` as needed);
package + jstest tests.

- [ ] **Failing test:** a hub-relay test asserts the resolve request routes daemon-
  ward through `LocalDaemonSource` and that Codex returns method-not-supported; a
  `cmd/serf-hub/jstest` case asserts `eventsFromNotification` maps
  `serf/sandbox/escalation/requested` to a card event, and that the file-tool
  shape vs the shell shape render distinctly (the shell card shows the
  output-so-far + the "already partially ran; approving re-runs start-to-finish"
  caveat; the file card does not).
- [ ] Implement: add `ResolveSandboxEscalation` to the `Source` interface + both
  impls; the hub handler in `app_rpc.go` (copy the `:429` `MethodTurnSteer`
  block, swapping in the new method + `source.ResolveSandboxEscalation`); the
  `appwire.js` mapping; the two-shape card in `renderer.js` with Allow/Deny
  controls that post `MethodSerfSandboxEscalationResolve`. Adapt the `ask_user`
  card's *structure* (a projected, human-answered card) but not its lifecycle —
  this card resolves in place and vanishes; it is never a transcript turn.
- [ ] **Adversarial verify:** is the card unmistakably a *harness* prompt, not a
  model message (so a human can't be socially-engineered by model text into the
  approve button)? Does Deny (or dismiss) send `approve:false` rather than
  silently dropping — leaving the daemon blocked forever? Fix, commit.

## Task 6 — E2E: file-tool escalation (approve expands one invocation)

**Files:** `cmd/serf-hub/…_test.go` (new e2e), real `serf serve` daemon + real
bwrap, no mocks.

- [ ] **Failing test** `TestE2E_FileToolEscalation_Approve/Deny` (Linux, bwrap):
  start a **live `--sandbox read-only`** interactive session; drive a `write_file`
  to a denied path; over AppWire, assert the `escalation/requested` notification
  arrives with the precise path; send `escalation/resolve{approve:true}` and
  assert the write **succeeds** and the tool result is the success output; in the
  Deny variant assert the typed denial reaches the model as the tool result.
  **Per-invocation proof:** a *second* `write_file` to the *same* path in the same
  session raises a *fresh* escalation (approving once did not create a standing
  allowance).
- [ ] Make it green (implementation should already exist from Tasks 1–5; this task
  is the live wiring proof — fix integration gaps only).
- [ ] **Adversarial verify:** does the approved re-run go through the *same*
  race-safe openat2 path with the grant, not a path-string bypass (the grant must
  widen the allowed set for one call, not disable the resolver)? Fix, commit.

## Task 7 — E2E: shell escalation (partial-run honesty)

**Files:** `cmd/serf-hub/…_test.go` (new e2e), real daemon + bwrap.

- [ ] **Failing test** `TestE2E_ShellEscalation_PartialRun`: start a live sandboxed
  session; drive a `shell` command that writes some output **then** hits a denied
  path (so output-so-far is non-empty and the command partially executed); assert
  the escalation card carries `command` + `output_so_far` + `partially_ran:true`;
  on approve assert the command **re-runs start-to-finish** with the grant (assert
  the full effect, not just the tail); on deny assert the typed denial reaches the
  model.
- [ ] Make it green; fix integration gaps only.
- [ ] **Adversarial verify:** is the "approving re-runs start-to-finish" claim
  actually true (no attempt to resume mid-command)? Is `output_so_far` truncated/
  bounded so a runaway command can't bloat the card? Fix, commit.

## Task 8 — Invariants: the four walls + non-interactive parity

**Files:** consolidate into `agent/session_escalation_test.go` +
`cmd/serf-hub/…_test.go`.

- [ ] **Failing test** — one assertion per wall, plus parity:
  - **Not triggerable:** grep-style test that no entry in the tool registry can
    raise an escalation (only `escalateOnSandboxDenial` does), and no model-facing
    tool named for escalation exists.
  - **Not approvable by the model:** the resolve method is absent from the model's
    advertised tool set; the only caller is the human UI path.
  - **Not observable:** after a full approve→re-run round, the model-visible
    history/transcript contains **no** escalation request or decision — only the
    tool result. (Assert against the projected transcript *and* `s.history`.)
  - **Not replayable:** a session interrupted mid-wait, then resumed, has **no**
    pending escalation and the interrupted call reads as an `IsError` orphan-repair
    placeholder (mirror the `ask_user` interrupted-ack invariant).
  - **Non-interactive parity:** `TestE2E_NonInteractiveDenialUnchanged` — a
    `--sandbox` **non-interactive** session's denial is byte-identical to the
    pre-M7 behavior (no notification emitted, denial final). This is the
    regression guard that M7 added *zero* new behavior to the headless path.
- [ ] Make green.
- [ ] **Adversarial verify:** try to find a fifth hole — e.g. can a subagent's
  denial bubble to the *parent's* human (it must not; subagents don't escalate)?
  Can a queued/steered message race the blocked call into an inconsistent state?
  Fix, commit.

## Design decision to confirm before Task 1 — "interactive but no human watching"

An interactive root session can have **no browser attached** at the instant of a
denial (the `serf serve` daemon is up, the tab is closed). Blocking a turn on a
card no one can answer would hang the session. **Recommendation (encoded in Task 1's
gate):** escalate **only when `SubscriberCount(threadID) > 0`**; otherwise treat
the denial as final (same as non-interactive). A subscriber that drops *mid-wait*
is handled by the existing turn-interrupt / session-close cancellation (deny). A
bounded wait timeout is a deliberate **non-goal** for M7 (the spec asks for
human-gated, not time-gated); note it as a possible follow-up. If Jesse prefers
"block and wait for a reattach" over "deny when unwatched," that is a one-line
change to the gate + the reconnect-replay already carries the pending card — flag
it and get his call before building, since it changes the hang/￼safety trade-off.

## Done criteria

- `cd <worktree> && make test-short && make vet && make lint` clean;
  `make generate` produced no uncommitted diff (the protocol doc is regenerated
  and committed).
- The full round-trip works against a **live bwrap-sandboxed `serf serve`**:
  file-tool approve expands exactly one invocation, deny returns the typed error,
  shell approve re-runs start-to-finish with an honest partial-run caveat.
- All four model-isolation walls have a passing assertion; the non-interactive
  path is proven byte-unchanged.
- No goroutine leak on interrupt/close with a pending escalation.
- Merge `wip/sandbox-m7` → `wip/sandboxing`; tick the M0 status ledger's final
  box; report. (`wip/sandbox-m7` is cut from `wip/sandboxing` **after** M1–M6 have
  merged and validated e2e — M7 needs a live sandbox to escalate over.)
