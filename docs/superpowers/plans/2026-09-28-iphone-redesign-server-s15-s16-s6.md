# iPhone redesign, Phase 7: direct subagent stop, session access and transcript history (Implementation Plan)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Every task carries its code, so every task's implementer is Sonnet; the reviewer checks it against this plan and the code as it is on main.

**Goal:** The phone stops a runaway subagent itself, without asking its coordinator (S6); the Session sheet shows a session's sandbox mode and network setting (S15); and a reloaded transcript still says "Queued" on a message that waited, and still shows what you allowed or denied (S16).

**Architecture:**
- **S6a (PR 30).** The daemon learns `evener/delegate/stop`. It cancels the named subagent's current run the way a user's stop of that run always has (the subagent's `cancelRequested` path, which today only a test drives), so the run settles as `cancelled` and its coordinator reads "Stopped by the user." Nothing durable is written for the stop itself: no subtree stop fence, so the subagents the stopped one started keep running under their own contexts and report to it, and it takes their results the next time it runs (Jesse's answer 4). The method targets the root session, the way every turn mutation does.
- **S6b (PR 31).** The hub relays the method to the root's daemon (local or remote), and root threads advertise `stopSubagent` in their capabilities, so the phone shows "Stop subagent" where it now shows "Ask coordinator to stop it".
- **S15 (PR 32).** `EvenerThread.access` carries `{sandbox, network}`. The daemon reads it from the sandbox request the session's meta persists (the root through its envelope's meta facet, each subagent from its own `SessionStart`), and the hub reads it from a past session's meta. The shared TypeScript model carries it for the Session sheet.
- **S16 (PR 33).** Two transcript records. A `USER_INPUT` entry whose message came through `turn/queue` is marked `queued`, and its `userMessage` item carries `queued: true`. A human's Allow or Deny on a sandbox escalation is recorded as a `NOTICE` of a new kind, `approval_decision`, and projects as a `systemMessage` whose `eventKind` is `approval_decision` and whose `raw` carries the decision. Both go through the transcript read model's one projection (`apptranscript.ProjectEntryParts`), so live history and a reload agree.

**Tech Stack:** Go 1.27 workspace (root module and `agent/`), AppWire over WebSocket (`ProtocolVersion` `"evener-appwire-v6"`), TypeScript 6 in `appwire-client/typescript` (tested with vitest from `cmd/evener-hub/frontend`), the Bubble Tea TUI (`cmd/evener-tui`).

**Spec:** `docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md`: 8.2 (the Transcript table: "Queued" on a delivered message, and Approval (history)), 8.6 (the Session sheet's Access section), 9 (Stop subagent), 17 and 18 (S6, S15, S16). The server plan `docs/superpowers/plans/2026-09-26-iphone-redesign-server-additions.md` holds the design-level S6 section this plan replaces (PRs 30 and 31), its Global Constraints and Jesse's answers (answer 4: stop that agent, not its tree). The handoff `docs/superpowers/handoffs/2026-09-28-iphone-redesign/HANDOFF.md` records ruling 19 (S6 is stop only; messaging a running subagent is tabled). The phone side is the phase 3 plan's rulings 21, 23 and 24 and the phase 4 plan's ruling 10. Written and dry-run against main at `274afd6ff` (see Self-review).

## What was measured

Everything below was read on main at `274afd6ff`.

**A session's sandbox (S15).**
- A session's sandbox request is two config fields: `SessionConfig.Sandbox`, the mode name (`off`, `read-only`, `workspace-write` or `restricted`; empty means off), and `SandboxNet`, the network decision (nil means the default, on) (`agent/session_config.go:236-240`).
- `configureSandbox` writes both only for a non-off mode (`cmd/evener/sandbox.go:62`). `provisionSandboxWithHost` resolves the request against the host and refuses to start a session it cannot enforce (`cmd/evener/sandbox.go:106`), and a resume re-resolves the persisted request the same way. So the persisted request is what a running session enforces. `reconcileClearSandbox` keeps a cleared session's config equal to its environment (`cmd/evener/sandbox.go:133`).
- The request persists in the session's meta: `SessionMeta.Config` (`agent/schema/snapshot.go:151`) round-trips `Sandbox` and `SandboxNet` (`agent/session_config.go:1083`).
- A subagent can run in a narrower sandbox than its coordinator: `childConfig.Sandbox` is set from the delegate's own resolved box (`agent/delegate_runtime.go:1930-1937`).
- Nothing puts it on the wire. `EvenerThread` has no sandbox field (`appwire/types.go:1004` is its last field, `Subagents`); only `LaunchConfigLayer` names a sandbox (`appwire/types.go:3822`, `:3831`), which describes a launch, not a running session.
- Where a thread read is built:
  - the live root: `appThreadWithDiagnosticsLocked` (`server/appwire_runtime.go:2344`) from the envelope, whose meta facet samples `SessionMeta` (`server/thread_envelope.go:424`, `:497`). `SESSION_START` samples every facet (`server/thread_envelope.go:218`), and a session's sandbox is fixed from that moment;
  - a live subagent: `RecordDescendantAppEvent` (`server/appwire_runtime.go:608`) keeps the thread the projector's `thread/started` built from `SessionStartData` (`:655`, `:671`); `SessionStartData` is emitted in one place, `emitSessionStartEnvelope` (`agent/session_events.go:144`);
  - a past session: `pastEntryThreadForList` (`cmd/evener-hub/app_threadread.go`, ending at `:949`) from the meta the past index holds.
- The shared client copies thread facts onto `ThreadModel` in `threadFields` (`appwire-client/typescript/reducer.ts:1222` for its neighbor `failedToolCalls`); the phone's Session sheet reads the model, as it reads `diagnostics.plugins` today (phase 3 ruling 21).

**A queued message's delivery (S16).**
- A message sent with `turn/start` runs through `ProcessClientMutationStart` (`agent/session_client_mutation.go:744`); one sent with `turn/queue` waits in the input queue and runs through `ProcessPendingUserInput` (`agent/session_client_mutation_queue.go:244`) when a turn ends. Both write a `USER_INPUT` entry with the message's `ClientMutationID` (`agent/session_lifecycle.go:2733`), and at that point the pending execution's `Method` says which path it took (`:2739`).
- A queued message steered in (`turn/promoteQueuedAsSteer`, `turn/drainAsSteer`) becomes a `STEERING` entry with `SteeringSource: "user"`, which the phone already captions "Steered in mid-turn".
- `USER_INPUT` projects to a `userMessage` item in `apptranscript.projectTurn` (`internal/apptranscript/apptranscript.go:602`), which carries no delivery mark.

**An approval's decision (S16).**
- An escalation blocks the tool goroutine in `escalateOnSandboxDenial` (`agent/session_escalation.go:162`) until `ResolveSandboxEscalation` sends the human's decision (`:214`), the turn's context ends, or `cancelAllEscalations` (`:280`) denies it on close. The close path sends the same `{Approve: false}` a human's Deny sends, so the waiter cannot tell them apart today.
- By design the escalation never enters the model's history (`:39`), and `evener/sandbox/escalation/resolved` carries no decision (`appwire/types.go:1229`). The decision is recorded nowhere; only the re-run call's result, or the typed denial, is.
- The transcript already records presentational history as transcript-only `NOTICE` entries: `recordNotice` (`agent/session_execution.go:437`) refuses a notice that fails `NoticeInfo.Validate` (`agent/schema/turn.go:178`), and `NoticeItem`/`noticeAnnouncement` (`internal/apptranscript/notice.go:67`) project each kind as a `systemMessage` with an `eventKind` and a structured `raw`.

**Live and reload share one projection.** Each served thread projects history from its own transcript's recorded entries (`server/appwire_histories.go:49`), through `apptranscript.ProjectEntryParts` (`internal/apptranscript/apptranscript.go:420`, called from `internal/transcriptindex/build.go:175`). A past read goes through the same index. So a field set on the entry reaches live history, a reload and search alike, and no change to `internal/appprojector` is needed.

**The transcript's strict decoding.** `decodeStrictJSON` rejects an unknown field (`agent/transcript/transcript.go:247`), and one such record makes a whole transcript unreadable to a build that does not know it (`:254`, kata wf7e, closed wontfix; pinned by `TestPastThreadReadFailsWholeSessionOnOneUnknownTurnField`). Every transcript schema addition so far has taken that one-way door. S16 takes it too: see question 1.

**How the shared client treats a new `eventKind`.** `transcriptProjector.systemDecision` shows any kind it does not list as an item at every level (`appwire-client/typescript/transcriptProjector.ts:265`), so an `approval_decision` item shows on today's web and phone as a plain system line until their lanes style it.

**Stopping a subagent today (S6).**
- The only delegate stop is the model's `job_stop`, which calls `StopSubtreeAndDrive` (`agent/delegate_tree_stop.go:117`): a durable `subtree_stop_requested` event over `subtreeMembersLocked(targetID)`, the target and every descendant (`:241`; the fold marks them all stopping, `agent/internal/delegatestore/fold.go:351`).
- A cascading stop cannot back Jesse's answer 4, and a target-only variant of the durable stop would break the controller's invariant that a stop covers whole subtrees: `admitLeaseLocked` refuses every lease under an ancestor with a pending stop (`agent/delegate_tree_controller.go:899`, `ancestorFenceLocked` at `:923`), so the stopped subagent's own children would fail their model requests with `target_busy` for as long as the stop was pending.
- A run's context is its own: every run's context derives from `context.Background()` (`agent/delegate_tree_start.go:154`, `:344`, `:631`), never from its parent's run. Cancelling one run's context touches no other run.
- A user stop of one run already exists: `Session.cancelAgent` (`agent/subagents.go:1687`) sets the subagent's `cancelRequested` (`:103`) and cancels its run. Settlement then classifies the end as the user's stop (`classifyRunEnd`, `:2091`) and records outcome `cancelled` (`stableDelegateFinishFromRun`, `:2282`). The delegate stays resumable and idle, and its coordinator gets the ordinary terminal delivery (`applyRunFinished`, `fold.go:255`, `:309`). `TestCancelAgent_RunningChildBecomesCancelledAndResumable` (`agent/subagents_test.go:339`) pins it. Nothing in production calls `cancelAgent`; `job_stop` replaced it.
- The packet the coordinator reads for a cancelled run is the run error's text, "context canceled", which does not say who stopped it.
- A subagent struct is tracked by the session that started it, under its child session id (`agent/delegate_runtime.go:2443`, `subagentManager.get` at `agent/subagent_manager.go:204`), so a nested subagent is found through its parent's session (`subagentManager.sessions`, `:227`). An idle parent's runtime is not reclaimed while a subagent below it runs: reclamation claims whole idle subtrees only (`agent/delegate_tree_reclaim.go:531`).
- The phone names a subagent by its delegate id, which the coordinator's tree carries (`EvenerDelegateInfo.DelegateID`, `appwire/types.go:1459`).
- The daemon takes mutations for its root only (`requireRootMutationTarget`, `server/appwire_runtime.go:2193`); a running subagent's thread on the hub is a read-only alias with no capabilities (`cmd/evener-hub/app_rpc.go:171`). The closest existing method is `evener/sandbox/escalation/resolve`: a root-targeted, UI-only request with a handler (`server/appwire_runtime.go:1599`), a retirement classification (`server/appwire_retirement_admission.go:36`), serve wiring (`cmd/evener/serve.go:1425`), a hub relay (`cmd/evener-hub/app_rpc.go:1775`) and a recovery-admission case (`cmd/evener-hub/app_sources.go:342`). S6 follows it step for step.
- Remote sessions: `maskRemoteThreadCapabilities` masks every thread action but Shutdown until the host capability probe lands (`cmd/evener-hub/internal/appsource/remote_hub_refs.go:107`), so a remote root will not advertise `stopSubagent` either; the relay is built so it turns on with the others.

## Global Constraints

- **Wire.** Every change is additive and stays on `ProtocolVersion = "evener-appwire-v6"` (`appwire/types.go:30`). New keys are optional (`omitempty` or a pointer), and an old peer reads a missing key as no information:
  - `EvenerThread.access` is a pointer, absent from an older daemon or hub. Inside it `network` is always present, because `false` is a fact.
  - `ThreadItem.queued` is `omitempty`; an older client ignores it.
  - `approval_decision` is a new `eventKind` value on an existing item type; an older client shows the item as a plain system line (What was measured).
  - `ThreadCapabilities.stopSubagent` is `omitempty`; an older phone ignores it.
  - `evener/delegate/stop` is a new method. An older daemon or hub answers MethodNotFound, which is the phone's cue to keep its fallback.
- **Never add a `FeatureSet` key.** The TS `initialize` decoder refuses unknown feature keys.
- **Casing.** `appwire` JSON is camelCase (`stopSubagent`, `delegateId`); `agent/schema` and `agent/events` JSON is snake_case (`approval_decision`, `sandbox_net`). The tagliatelle lint enforces both (`.golangci.yml`). A camelCase `raw` payload built in `internal/apptranscript` carries `//nolint:tagliatelle` per field, as `pluginLoadedRaw` does.
- **Transcript schema.** `schema.Turn` fields stay in alphabetical JSON-key order (its doc comment). A new `NoticeInfo` payload is added to `NoticeInfo.Validate`'s map, or `recordNotice` refuses it (found on the dry run: the refusal is a warning and nothing else).
- **Generated files.** After any `appwire` type or catalog change, doc comments included: `make generate`, then `go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1`. Commit `appwire-client/typescript/types.gen.ts`, and `docs/appwire-protocol.md` when it changes. The protocol doc lists methods, so PRs 30 and 31 change it and PRs 32 and 33 do not (checked on the dry run: `make generate` leaves it unchanged there).
- **The TUI rule.** No PR changes `internal/appprojector`. PR 33 changes what the transcript projection emits (a `queued` user message, a new system-message kind), so it adds a `cmd/evener-tui` case for each. No PR adds a notification.
- **A new hub method** gets a catalog row (`appwire/protocol.go`), a decision in `TestHostAdminAllowListMatchesCatalog` (`cmd/evener-hub/app_host_admin_test.go`), and an entry in `TestHubRPCRegistersExpectedHandlerSet` (`cmd/evener-hub/app_rpc_test.go`). A new daemon method gets a row in `daemonRetirementAccessKinds` and in `TestRetirementAdmissionCatalogCoverage`.
- **A new capability** is decided in every projection `TestCapabilityProjectionsMatchTheDaemonOracle` walks, wired in `hubtest.WireCapabilitySeams`, and named in `maskRemoteThreadCapabilities` and `TestRemoteHubCapabilitiesMatchForwardedMethods`.
- **Go floors,** per module touched (root and `agent`):
  - `go vet ./...`, `go vet -tags evenerfuzz ./...` and `GOOS=windows go vet -tags evenerfuzz ./...`;
  - format with `$(go env GOROOT)/bin/gofmt`, never the one on PATH;
  - run the pinned `golangci-lint` (2.13.1, `.tool-versions`) on each touched package.
- **TypeScript floor.** From `cmd/evener-hub/frontend`: `npm run typecheck`, `npx vitest run ../../../appwire-client/typescript/`, and `npx biome check <touched files>` on touched files in `appwire-client/typescript`. PR 33 also runs `make test-native` (it adds a key to every `ItemModel`). Never run Biome from the repo root or in `mobile-native`, and never `npm ci` through a symlinked `node_modules`.
- **Targeted tests only.** Run each task's tests and the gates it names; CI runs the full matrix. Several `agent` and `cmd/evener` tests fail on macOS only, because `/var` is a symlink to `/private/var` (#2497), and the `agent` package runs at its 10-minute cap there. CI (Linux) is the judge.
- **Deterministic tests.** No network, no provider credentials, no sleeps: scripted providers at the LLM boundary, channels for ordering, and the existing `waitForCondition` tripwires. Every new test is shown failing before its code lands, except the two TUI cases in PR 33, which pin behavior that must not change (their steps say so).
- **Line numbers** are on main at `274afd6ff`. A merge from another lane moves them, so every step also names its anchor: find it by the name.
- **Size,** measured on the dry run (production lines, tests and generated output excluded): PR 30 about 190, PR 31 about 75, PR 32 about 65, PR 33 about 100. Landing follows the handoff: a regular PR, CI green on the merged head, RoboRev's comment read, /simplify run and its fixes pushed, then an admin squash merge with `--match-head-commit <full sha>`.

## Rulings

Decisions the spec and the server plan leave open, with the reason for each.

**S6, direct subagent stop**

1. **The stop cancels the target's current run; it writes no durable stop.**
   - The server plan's sketch named a non-cascading counterpart of `StopSubtreeAndDrive`. Measured, a target-only durable stop breaks the controller's whole-subtree invariant: the target's children would be refused every lease while it was pending (What was measured).
   - Cancelling the run is the existing user stop (`cancelRequested`), and every run's context is its own, so the children are untouched by construction.
   - Durability comes from the run's own settlement, which records `cancelled` like any run end. A daemon that crashes mid-stop resumes the run as interrupted, which is the same end.
2. **Stopping is idempotent, so the method takes no `clientMutationId`.** A second stop finds the run ended or ending and answers `notRunning`, including one that arrives while the first stop's cancellation is still unwinding the run (`cancelRequested` already set; found by RoboRev on the plan PR). A run already past its final pre-settlement check (`settlementClaimed`) also answers `notRunning`: it is finishing on its own.
3. **The method targets the root.** Params are `{threadId, ref, delegateId}` with the root's ref, as every turn mutation's are. The phone has the root ref and the delegate id from the coordinator's tree. A subagent's own ref is refused by `requireRootMutationTarget`.
4. **Any subagent in the tree can be stopped by the user.** `job_stop`'s authorization (a parent stops only its own children) governs the model, not the human who owns the whole tree.
5. **The coordinator reads "Stopped by the user."** A cancelled run with nothing to report gets that sentence as its packet message instead of "context canceled", so the coordinator knows a person ended it rather than the run failing. Outcome and reason stay `cancelled`, which the tree and the tallies already count as done.
6. **An unknown delegate is `resourceNotFound`,** so the phone can tell a stale row from a daemon without the method (MethodNotFound).
7. **The capability is `stopSubagent` on the root thread.** It is true while the daemon wires the stop and the session is open, like Interrupt. It is absent from a past session (no daemon runs its subagents) and from a subagent's alias. It lands with the hub relay (PR 31), so it is never advertised by a hub that cannot route it.
8. **No new notification.** `evener/delegate/updated` and the jobs tree already report the run's end.

**S15, a session's access**

9. **Access is the persisted sandbox request, read where the thread is built.** Root: the meta facet. Subagent: its own `SessionStart`. Past: its meta. One helper, `appwire.SessionAccess(sandbox, network)`, maps the request to the wire for all three.
10. **`off` is stated, not omitted.** An unsandboxed session reports `{sandbox: "off", network: true}`: it may use the network. Only an older daemon or hub leaves `access` out, and the phone then leaves the section out (phase 3 ruling 21's fallback).
11. **The mode is the session's own name for it** (`read-only`, `workspace-write`, `restricted`). Copy belongs to the phone lane.

**S16, transcript history**

12. **"Queued" means the message came through `turn/queue` and ran as its own turn.** A message sent with `turn/start` is not queued. One steered in is a `STEERING` entry and keeps its "Steered in mid-turn" caption. One queued while idle runs at once and is still marked queued; the phone sends with `turn/queue` only while the agent works (spec 8.5), so that case is rare.
13. **Only a human's Allow or Deny is recorded.** A stopped turn or a closing session withdraws the escalation without a decision, and records nothing. To tell them apart, `cancelAllEscalations` now closes each waiter's channel instead of sending a deny; the tool call still gets its typed denial.
14. **The decision is recorded before the re-run,** so in the transcript it sits right before the call's result.
15. **The record carries what the card showed:** the escalation id, allowed or not, the denied tool, the card's kind and the path. Never file contents. Text reads "Allowed write_file to access <path>" or "Denied write_file access to <path>", worded like the web's card; the phone draws its row from `raw`.

## Questions for Jesse

Each has a recommendation; the plan is written to the recommendation, so none blocks work.

1. **S16 takes the transcript's one-way door. Accept it?** A hub or daemon older than the build that writes a `queued` entry or an `approval_decision` notice cannot read that session's transcript at all until it is updated (kata wf7e: strict decoding fails the whole file). It only affects sessions that have such an entry, and the file on disk is untouched. Every transcript schema addition so far has taken the same door. **Recommendation:** accept it, as wf7e did. The alternative is keeping these facts outside the transcript, which S16 exists to avoid.
2. **Should approval history show at every detail level?** The shared projector shows an unknown `eventKind` at every level, so the history row appears at Chat through Full until the phone and web lanes decide. **Recommendation:** every level, like a question's history, since an approval is a decision you made. The alternative is gating it with system events.
3. **Does "Stop subagent" leave that subagent's background commands running?** The stop ends its run: a command running in the foreground ends with it, as when you stop a coordinator's turn. Background jobs it started (`job_start` shells) and its own subagents keep running. **Recommendation:** yes, keep that, to match a coordinator's Stop and your answer 4. The alternative (also stopping its background jobs) is a small addition if you want it.
4. **Should the coordinator hear about the stop at once?** The coordinator gets the stopped run's report ("Stopped by the user.") the way it gets any subagent's end: at its next step if it is working, and as a wake if it is idle. **Recommendation:** keep it. A separate steer to the coordinator would say the same thing twice.
5. **Should a stop name the run it means?** A stop confirmed on a run that ended and restarted in between (a subagent woken by its own child) stops the new run. **Recommendation:** no fence. The window is the length of a confirmation tap, and stopping the restarted run is usually still what the user meant. A fence would add the run's start time to the params.

## Review Focus

1. **A stop that takes the tree with it.**
   - Stopping a subagent must leave the subagents it started running, and must write no durable subtree stop.
   - Pinned by `TestUserStopCancelsOnlyTheTargetsRun` (Task 30.1: the nested subagent's run context stays live) and `TestStopDelegateRunEndsTheRunAsCancelledByTheUser` (Task 30.1: no `subtree_stop_requested` event, the delegate idle and resumable).
2. **A withdrawn approval recorded as a Deny.**
   - A turn stopped or a session closed while an approval waits must not leave "Denied: …" in history.
   - Pinned by `TestEscalation_RecordsNoDecisionWithoutAHuman` (Task 33.2) and, for the Allow and Deny that must be recorded, `TestEscalation_RecordsTheHumansDecision`.
3. **"Queued" on a message that never waited.**
   - A message sent with `turn/start` must not be marked queued.
   - Pinned by `TestQueuedDeliveryIsRecordedOnTheUserEntry` (Task 33.1: the start path's entry reads false).
4. **Access that reads as unknown when it is known.**
   - An unsandboxed session must say `off`, and a sandbox with the network off must say `network: false`, never drop the key.
   - A subagent must show its own sandbox, not its coordinator's.
   - Pinned by `TestThreadAccessAlwaysCarriesBothKeys` (Task 32.1), `TestThreadSnapshotsSayAnUnsandboxedSessionIsOff` and `TestDescendantThreadsCarryTheirOwnAccess` (Task 32.2).
5. **History that differs live and after a reload.**
   - "Queued" and approval history must read the same from a live session and from its transcript on disk, in both entry formats.
   - Pinned by `TestQueuedUserInputProjectsAQueuedUserMessage` and `TestApprovalDecisionNoticeProjectsItsDecision` (both through `ProjectEntryParts`, the one projection live history and reloads share), and by the reducer tests that keep `queued` through the client's merge rules (Task 33.1).

---

## PRs and lanes

| PR | Item | Tasks | Production lines (dry run) | Depends on |
|---|---|---|---|---|
| 30 | S6a: the daemon stops one subagent's run | 30.1-30.2 | about 190 | none |
| 31 | S6b: the hub relays the stop, and roots advertise it | 31.1-31.2 | about 75 | PR 30 |
| 32 | S15: a session's access on the thread read | 32.1-32.3 | about 65 | none |
| 33 | S16: queued delivery and approval decisions in the transcript | 33.1-33.2 | about 100 | none; question 1 |

- **Merge order.** Three lanes: PR 30 then PR 31; PR 32; PR 33. Each branches from main once the PR it depends on has merged.
- **Where the lanes meet.**
  - `appwire/types.go`, `appwire/protocol.go` and the generated files (every PR). The additions sit apart; when `types.gen.ts` or `docs/appwire-protocol.md` conflicts, take either side and run `make generate` again.
  - `server/appwire_runtime.go` (PRs 30, 31 and 32): different functions.
  - `appwire-client/typescript/model.ts`, `reducer.ts` and `reducer.test.ts` (PRs 32 and 33): different fields.
  - `agent/schema/turn.go` (PR 33 only).
- **The server plan's PR map changes with this plan:** PRs 30 and 31 are planned in full here, and S15 and S16 get PRs 32 and 33.

## Phone lane handoff

This lane changes `mobile-native/` only through the shared package. When each PR is on the hub the phone talks to, the phone switches off its fallback as follows.

**S6 (after PR 31).** Phase 4's `SubagentBar` and `StopSubagentSheet` (its ruling 10 and Task 7's point 7).
- When the coordinator's root thread advertises `capabilities.stopSubagent`, a running subagent's bar offers "Stop subagent" in place of "Ask coordinator to stop it". It opens a confirmation, with no message (spec 9).
- Confirm sends `evener/delegate/stop {ref: <coordinator ref>, delegateId}`. `stopping` shows "Stop requested" until the tree reports the run's end, then "Stopped at your request" with the toast, as today. `notRunning` needs no message: the subagent is already finishing.
- The offer follows the subagent's own running state (phase 4 ruling 10's S6 note): a subagent whose own run ended but whose children still run has nothing to stop.
- `resourceNotFound`: the row is stale; refresh the tree. MethodNotFound or a missing capability: keep "Ask coordinator to stop it".
- A remote session never advertises the capability yet (remote thread actions are masked until the host capability probe lands), so remote subagents keep the fallback.

**S15 (after PR 32).** Phase 3's Session sheet (ruling 21).
- Show the Access section when `ThreadModel.access` is present: the mode and whether the network is allowed, read-only. Leave the section out when it is absent.

**S16 (after PR 33).** Phase 3's transcript rows (rulings 23 and 24).
- A `userMessage` item with `queued: true` carries the "Queued" caption (spec 8.2). Steered messages keep "Steered in mid-turn".
- A `systemMessage` with `eventKind: "approval_decision"` is the Approval (history) row: "Allowed: …" or "Denied: …" from `raw.approvalDecision` (`approved`, `tool`, `kind`, `deniedPath`). It is not shown while the dock is open, which cannot happen for the same escalation: the record is written when the dock's decision lands.
- Until the phone styles it, the item shows as a plain system line (What was measured).

**Known limits.**
- A read-only delegate scope on a host with no sandbox backend runs with its file tools confined and its shell unconfined, and still reports `read-only` (the persisted request). The session already tells the agent so; the Access row does not.
- A transcript written before PR 33 has no "Queued" and no approval history, and never will.

---

## PR 30: the daemon stops one subagent's run, S6a (Tasks 30.1-30.2)

**Branch:** `git fetch origin && git switch -c claude/s6a-delegate-stop origin/main`

**What it adds.** `Session.StopDelegateRun`, the daemon method `evener/delegate/stop` (daemon scope; PR 31 opens it to the hub), and the coordinator's "Stopped by the user." Nothing on the phone changes yet.

### Task 30.1: A user stop ends one subagent's run and nothing else

**Implementer:** Sonnet.

**Files:**
- Modify: `appwire/types.go` (the method constant after `MethodEvenerSandboxEscalationResolve`; `DelegateStopParams`, `DelegateStopOutcome` and `DelegateStopResponse` before `SandboxEscalationResolveParams`)
- Create: `agent/delegate_user_stop.go`
- Modify: `agent/subagents.go` (`cancelAgent` shares the new `requestUserStop`; `cancelRequested`'s comment; `stableDelegateFinish` and `delegateTerminalRunInputs` carry `stoppedByUser`; `stableDelegateFinishFromRun`'s cancelled branch)
- Create: `agent/delegate_user_stop_test.go`

**Interfaces:**
- Produces:
  - `appwire.MethodEvenerDelegateStop = "evener/delegate/stop"`, `appwire.DelegateStopParams{ThreadID, Ref, DelegateID}`, `appwire.DelegateStopOutcome` with `DelegateStopStopping = "stopping"` and `DelegateStopNotRunning = "notRunning"`, `appwire.DelegateStopResponse{Outcome}`.
  - `func (s *Session) StopDelegateRun(delegateID string) (appwire.DelegateStopOutcome, error)` and `agent.ErrUnknownDelegate`.
  - Unexported: `(*subagent).requestUserStop() error` (`errSubagentNotRunning`, `errSubagentSettling`), `(*Session).subagentForChild`, `(*delegateTreeController).childSessionIDFor`, `delegateUserStopMessage`.

- [ ] **Step 1: Write the failing tests**

`agent/delegate_user_stop_test.go`:

```go
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/appwire"
)

// Stopping a subagent ends that subagent's own run and nothing else (S6,
// Jesse's ruling): its run context is cancelled, while a subagent it started
// keeps running under a context of its own.
func TestUserStopCancelsOnlyTheTargetsRun(t *testing.T) {
	root := newTestSession(t)
	parentSession := newTestSession(t)
	childSession := newTestSession(t)
	parentCtx, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	childCtx, cancelChild := context.WithCancel(context.Background())
	defer cancelChild()
	parent := &subagent{id: "child-session-a", sess: parentSession, running: true, cancel: cancelParent}
	child := &subagent{id: "child-session-b", sess: childSession, running: true, cancel: cancelChild}
	root.subagents.track(parent)
	parentSession.subagents.track(child)

	if found := root.subagentForChild("child-session-b"); found != child {
		t.Fatalf("subagentForChild found %p, want the nested subagent %p", found, child)
	}
	if got := root.subagentForChild("child-session-a").requestUserStop(); got != nil {
		t.Fatalf("requestUserStop = %v, want the run stopping", got)
	}
	if parentCtx.Err() == nil {
		t.Fatal("the target's run context is still live")
	}
	if childCtx.Err() != nil {
		t.Fatal("stopping the target cancelled the subagent it started")
	}
	parent.mu.Lock()
	requested := parent.cancelRequested
	parent.mu.Unlock()
	child.mu.Lock()
	childRequested := child.cancelRequested
	child.mu.Unlock()
	if !requested || childRequested {
		t.Fatalf("cancelRequested target=%v child=%v, want only the target", requested, childRequested)
	}
}

// A run that is not running, or is already settling, has nothing to stop.
func TestUserStopRefusesARunThatIsNotRunning(t *testing.T) {
	idle := &subagent{id: "idle"}
	if err := idle.requestUserStop(); !errors.Is(err, errSubagentNotRunning) {
		t.Fatalf("idle requestUserStop = %v, want errSubagentNotRunning", err)
	}
	settling := &subagent{id: "settling", running: true, settlementClaimed: true}
	if err := settling.requestUserStop(); !errors.Is(err, errSubagentSettling) {
		t.Fatalf("settling requestUserStop = %v, want errSubagentSettling", err)
	}
	// A second stop while the first is still unwinding the run finds it
	// already stopping, so a retry answers notRunning rather than stopping.
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopping := &subagent{id: "stopping", running: true, cancel: cancel}
	if err := stopping.requestUserStop(); err != nil {
		t.Fatalf("first requestUserStop = %v", err)
	}
	if err := stopping.requestUserStop(); !errors.Is(err, errSubagentSettling) {
		t.Fatalf("repeated requestUserStop = %v, want errSubagentSettling", err)
	}
}

// End to end on a stable delegate: the stop ends its run as cancelled, with
// no durable subtree stop, the delegate idle and still resumable, and the
// coordinator told the user stopped it. A second stop finds nothing running,
// and an unknown delegate is an error.
func TestStopDelegateRunEndsTheRunAsCancelledByTheUser(t *testing.T) {
	harness := newStableStopRuntimeHarness(t)
	root, delegateID := harness.root, harness.fixture.delegateID

	got, err := root.StopDelegateRun(delegateID)
	if err != nil || got != appwire.DelegateStopStopping {
		t.Fatalf("StopDelegateRun = %q, %v; want stopping", got, err)
	}
	harness.release()
	// TRIPWIRE: an in-process scripted adapter; settling takes milliseconds.
	waitForCondition(t, 30*time.Second, "the stopped run to settle", func() bool {
		root.delegateController.mu.Lock()
		defer root.delegateController.mu.Unlock()
		aggregate := root.delegateController.durable[delegateID]
		return aggregate != nil && !aggregate.CurrentRunOpen && aggregate.LatestOutcome != nil
	})

	root.delegateController.mu.Lock()
	aggregate := *root.delegateController.durable[delegateID]
	root.delegateController.mu.Unlock()
	if aggregate.LatestOutcome.Status != delegatestore.OutcomeCancelled || aggregate.Phase != delegatestore.PhaseIdle || !aggregate.Resumable || aggregate.PendingStopSeq != 0 {
		t.Fatalf("stopped delegate = outcome %+v phase %q resumable %v pending stop %d; want cancelled, idle, resumable, no stop fence",
			aggregate.LatestOutcome, aggregate.Phase, aggregate.Resumable, aggregate.PendingStopSeq)
	}
	var message string
	if aggregate.LatestPacket == nil || json.Unmarshal(aggregate.LatestPacket.Message, &message) != nil || message != delegateUserStopMessage {
		t.Fatalf("the coordinator's packet = %+v, want %q", aggregate.LatestPacket, delegateUserStopMessage)
	}
	events, err := root.delegateController.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.SubtreeStopRequested != nil {
			t.Fatalf("a user stop wrote a durable subtree stop: %+v", event)
		}
	}

	if again, err := root.StopDelegateRun(delegateID); err != nil || again != appwire.DelegateStopNotRunning {
		t.Fatalf("second StopDelegateRun = %q, %v; want notRunning", again, err)
	}
	if _, err := root.StopDelegateRun("dlg_unknown"); !errors.Is(err, ErrUnknownDelegate) {
		t.Fatalf("unknown delegate = %v, want ErrUnknownDelegate", err)
	}
}
```

The end-to-end test uses `newStableStopRuntimeHarness` (`agent/delegate_resource_runtime_test.go`): a root restored with one stable delegate whose provider call blocks until `release`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./agent -run 'TestUserStop|TestStopDelegateRun' -count=1`
Expected: FAIL to compile (`root.subagentForChild undefined`, `idle.requestUserStop undefined`, `undefined: errSubagentNotRunning`, and `appwire.DelegateStopStopping` undefined).

- [ ] **Step 3: Implement**

`appwire/types.go`:

```diff
diff --git a/appwire/types.go b/appwire/types.go
--- a/appwire/types.go
+++ b/appwire/types.go
@@ -150,6 +150,12 @@ const (
 	// (daemon serves it; hub relays). It is a UI-only request, never advertised to
 	// the model.
 	MethodEvenerSandboxEscalationResolve = "evener/sandbox/escalation/resolve"
+	// MethodEvenerDelegateStop ends one subagent's current run at the user's
+	// request (S6): that subagent alone, never the subagents it started. It
+	// targets the root session (ref/threadId) and names the delegate. ScopeBoth
+	// (the root's daemon serves it; the hub relays). A UI-only request, never
+	// advertised to the model.
+	MethodEvenerDelegateStop = "evener/delegate/stop"
 	// MethodEvenerHostRequest forwards one hub-scoped admin RPC to a named
 	// remote host's hub (component 07a). Host is the component-03 source ID;
 	// Method must be in the proxy's exact allow-list. See HostRequestParams.
@@ -1250,6 +1256,32 @@ type SandboxEscalationResolved struct {
 	EscalationID string `json:"escalationId"`
 }
 
+// DelegateStopParams is the request shape for evener/delegate/stop: the root
+// session that owns the delegate tree (ThreadID/Ref, as every turn mutation
+// names it) and the delegate to stop (EvenerDelegateInfo.DelegateID).
+type DelegateStopParams struct {
+	ThreadID   string `json:"threadId,omitempty"`
+	Ref        string `json:"ref,omitempty"`
+	DelegateID string `json:"delegateId"`
+}
+
+// DelegateStopOutcome is what evener/delegate/stop did.
+type DelegateStopOutcome string
+
+const (
+	// DelegateStopStopping: the subagent's run was cancelled. It ends as
+	// cancelled, which evener/delegate/updated reports.
+	DelegateStopStopping DelegateStopOutcome = "stopping"
+	// DelegateStopNotRunning: the subagent had no run to stop, because it was
+	// idle, finished, or already finishing. A repeated stop answers this.
+	DelegateStopNotRunning DelegateStopOutcome = "notRunning"
+)
+
+// DelegateStopResponse is the result of evener/delegate/stop.
+type DelegateStopResponse struct {
+	Outcome DelegateStopOutcome `json:"outcome"`
+}
+
 // SandboxEscalationResolveParams is the request shape for
 // evener/sandbox/escalation/resolve (M7): the human's approve/deny decision for a
 // pending escalation. Approve re-runs the single denied invocation with the one
```

`agent/delegate_user_stop.go`:

```go
package agent

import (
	"errors"
	"fmt"
	"strings"

	"primeradiant.com/evener/appwire"
)

// ErrUnknownDelegate is StopDelegateRun's answer for an id this session's
// delegate tree does not hold.
var ErrUnknownDelegate = errors.New("unknown delegate")

// delegateUserStopMessage is what a coordinator reads in place of a stopped
// run's report, so it knows the user ended the run rather than the run
// failing.
const delegateUserStopMessage = "Stopped by the user."

var (
	errSubagentNotRunning = errors.New("not running")
	errSubagentSettling   = errors.New("completing its current run")
)

// StopDelegateRun ends delegateID's current run at the user's request (S6).
// Only that delegate's run ends: subagents it started keep running and report
// to it, and it takes their results the next time it runs. Nothing durable is
// written here; the run's own settlement records it as cancelled, and its
// coordinator reads delegateUserStopMessage. It is safe to repeat: a second
// call finds nothing running and answers DelegateStopNotRunning.
func (s *Session) StopDelegateRun(delegateID string) (appwire.DelegateStopOutcome, error) {
	controller := s.delegateController
	if controller == nil || s.isSubagentSession() {
		return "", errors.New("this session has no delegate tree to stop")
	}
	childSessionID, ok := controller.childSessionIDFor(strings.TrimSpace(delegateID))
	if !ok {
		return "", fmt.Errorf("%w %q", ErrUnknownDelegate, delegateID)
	}
	sub := s.subagentForChild(childSessionID)
	if sub == nil {
		return appwire.DelegateStopNotRunning, nil
	}
	switch err := sub.requestUserStop(); {
	case errors.Is(err, errSubagentNotRunning), errors.Is(err, errSubagentSettling):
		return appwire.DelegateStopNotRunning, nil
	case err != nil:
		return "", err
	}
	return appwire.DelegateStopStopping, nil
}

// childSessionIDFor names the session delegateID runs as.
func (c *delegateTreeController) childSessionIDFor(delegateID string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate := c.durable[delegateID]
	if aggregate == nil {
		return "", false
	}
	return aggregate.Descriptor.ChildSessionID, true
}

// subagentForChild finds the subagent whose session is childSessionID
// anywhere below s. Each session tracks only the subagents it started, so a
// nested delegate is found through its parent's session.
func (s *Session) subagentForChild(childSessionID string) *subagent {
	if sub := s.subagents.get(childSessionID); sub != nil {
		return sub
	}
	for _, child := range s.subagents.sessions() {
		if sub := child.subagentForChild(childSessionID); sub != nil {
			return sub
		}
	}
	return nil
}

// requestUserStop cancels the subagent's current run and marks it stopped at
// the user's request, so settlement maps the cancellation to a cancelled
// outcome. It refuses a subagent that is not running, one whose run has
// passed its last pre-settlement check, and one already stopping, so a
// repeated stop answers notRunning.
func (a *subagent) requestUserStop() error {
	a.mu.Lock()
	if !a.running {
		a.mu.Unlock()
		return errSubagentNotRunning
	}
	if a.settlementClaimed || a.cancelRequested {
		a.mu.Unlock()
		return errSubagentSettling
	}
	a.cancelRequested = true
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}
```

`agent/subagents.go`:

```diff
diff --git a/agent/subagents.go b/agent/subagents.go
--- a/agent/subagents.go
+++ b/agent/subagents.go
@@ -100,7 +100,7 @@ type subagent struct {
 	runStructuredCaptured bool                      // runStructured was captured, including an authoritative nil result
 	nudgeEnabled          bool                      // true for default subagents that should be nudged to communicate
 	cancel                context.CancelFunc        // cancels the current run's context
-	cancelRequested       bool                      // set by parent stop so finalize maps a context.Canceled run to cancelled
+	cancelRequested       bool                      // set when the user stops this run (requestUserStop), so finalize maps a context.Canceled run to cancelled
 	settlementClaimed     bool                      // cancellation admission closes after the run's final pre-settlement check
 	agentType             string                    // plugin agent type name; empty for default subagents
 	createdAt             time.Time                 // set once at spawn; never reset on resume
@@ -1690,20 +1690,10 @@ func (s *Session) cancelAgent(agentID string) (any, error) {
 		return "", fmt.Errorf("unknown agent_id: %s", agentID)
 	}
 	sub.mu.Lock()
-	if !sub.running {
-		sub.mu.Unlock()
-		return "", fmt.Errorf("agent %s is not running", agentID)
-	}
-	if sub.settlementClaimed {
-		sub.mu.Unlock()
-		return "", fmt.Errorf("agent %s is completing its current run", agentID)
-	}
-	sub.cancelRequested = true
-	cancel := sub.cancel
 	done := sub.done
 	sub.mu.Unlock()
-	if cancel != nil {
-		cancel()
+	if err := sub.requestUserStop(); err != nil {
+		return "", fmt.Errorf("agent %s is %w", agentID, err)
 	}
 	select {
 	case <-done:
@@ -2139,6 +2129,7 @@ func (a *subagent) stableDelegateFinish(result string, runErr error) delegateFin
 	endedAt := a.sess.sclock().Now()
 	a.mu.Lock()
 	startedAt := a.startedAt
+	stoppedByUser := a.cancelRequested
 	var descriptor delegatestore.Descriptor
 	if a.stableDescriptor != nil {
 		descriptor = cloneDelegateStartDescriptor(*a.stableDescriptor)
@@ -2157,6 +2148,7 @@ func (a *subagent) stableDelegateFinish(result string, runErr error) delegateFin
 		endedAt:                 endedAt,
 		latestActivityAt:        endedAt,
 		usage:                   cumulativeUsageSnapshot(a.sess.CumulativeUsageSnapshot()),
+		stoppedByUser:           stoppedByUser,
 	}
 	reporter := a.sess
 	if controller := a.sess.delegateController; controller != nil {
@@ -2204,6 +2196,9 @@ type delegateTerminalRunInputs struct {
 	warnings                []string
 	worktree                *delegateWorktreeReport
 	scratchPath             string
+	// stoppedByUser: the user stopped this run (requestUserStop), so a
+	// cancelled run with nothing to report tells its coordinator so.
+	stoppedByUser bool
 }
 
 type delegateTerminalPacketMetadata struct {
@@ -2282,6 +2277,9 @@ func stableDelegateFinishFromRun(inputs delegateTerminalRunInputs) delegateFinis
 	} else if errors.Is(inputs.runErr, context.Canceled) {
 		finish.outcome = delegatestore.OutcomeCancelled
 		finish.reason = "cancelled"
+		if inputs.stoppedByUser && strings.TrimSpace(inputs.result) == "" {
+			packet.Message, _ = json.Marshal(delegateUserStopMessage)
+		}
 	}
 	metadata.Outcome = finish.outcome
 	metadata.Reason = finish.reason
```

`cancelAgent`'s errors read as before ("agent X is not running", "agent X is completing its current run"), because the sentinels carry those words.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./agent -run 'TestUserStop|TestStopDelegateRun|TestCancelAgent' -count=1`
Expected: PASS. `TestCancelAgent_RunningChildBecomesCancelledAndResumable` and `TestCancelAgent_GenuineFailureRacingCancelStaysFailed` keep passing through the shared `requestUserStop`.

- [ ] **Step 5: Gates and commit**

```bash
$(go env GOROOT)/bin/gofmt -l agent appwire
(cd agent && go vet ./... && go vet -tags evenerfuzz ./... && GOOS=windows go vet -tags evenerfuzz ./... && golangci-lint run ./)
go vet ./appwire && golangci-lint run ./appwire/
make generate && go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1
git add appwire/types.go agent/delegate_user_stop.go agent/delegate_user_stop_test.go agent/subagents.go appwire-client/typescript/types.gen.ts docs/appwire-protocol.md
git commit -m "feat(agent): the user stops one subagent's run, never its tree (S6a)"
```

`golangci-lint`'s `nilerr` is why `StopDelegateRun` switches on the two sentinels instead of dropping any error.

### Task 30.2: The daemon serves evener/delegate/stop

**Implementer:** Sonnet.

**Files:**
- Modify: `server/server.go` (`delegateStopFunc` after `sandboxEscalationResolveFunc`; `SetDelegateStopFunc` after `SetSandboxEscalationResolveFunc`)
- Modify: `server/appwire_runtime.go` (register the handler after the escalation resolve's; `handleAppDelegateStop` before `handleAppTurnInterrupt`)
- Modify: `server/appwire_retirement_admission.go` and `server/appwire_retirement_admission_test.go` (classify it `mutation`)
- Modify: `appwire/protocol.go` (the catalog row after the escalation resolve's, `ScopeDaemon`)
- Modify: `cmd/evener/serve.go` (`serveServer` gains `SetDelegateStopFunc`; wire it after `SetSandboxEscalationResolveFunc`)
- Create: `server/appwire_delegate_stop_test.go`, `cmd/evener/serve_delegate_stop_test.go`

**Interfaces:**
- Consumes: Task 30.1's `Session.StopDelegateRun`, `agent.ErrUnknownDelegate` and the appwire types.
- Produces: `(*server.Server).SetDelegateStopFunc(func(delegateID string) (appwire.DelegateStopOutcome, error))` and the daemon method. PR 31 adds `stopSubagent` beside it.

- [ ] **Step 1: Write the failing tests**

`server/appwire_delegate_stop_test.go`:

```go
package server

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
)

func TestHandleDelegateStop(t *testing.T) {
	t.Parallel()

	t.Run("no callback is unavailable", func(t *testing.T) {
		s := NewServer(ServerConfig{})
		s.SetAppIdentity("local", "root")
		if _, err := s.handleAppDelegateStop(context.Background(), appwire.DelegateStopParams{Ref: "local:root", DelegateID: "dlg_1"}); err == nil {
			t.Fatal("want an error when no session callback is attached")
		}
	})

	t.Run("an empty delegate id is invalid", func(t *testing.T) {
		s := NewServer(ServerConfig{})
		s.SetAppIdentity("local", "root")
		s.SetDelegateStopFunc(func(string) (appwire.DelegateStopOutcome, error) { return appwire.DelegateStopStopping, nil })
		_, err := s.handleAppDelegateStop(context.Background(), appwire.DelegateStopParams{Ref: "local:root", DelegateID: "  "})
		if wireErrorInfo(err) != appwire.ErrorInvalidParams {
			t.Fatalf("empty delegateId = %v, want invalid params", err)
		}
	})

	t.Run("only the root that owns the tree is a target", func(t *testing.T) {
		s := NewServer(ServerConfig{})
		s.SetAppIdentity("local", "root")
		s.SetDelegateStopFunc(func(string) (appwire.DelegateStopOutcome, error) {
			t.Fatal("a stop aimed at another thread reached the session")
			return "", nil
		})
		if _, err := s.handleAppDelegateStop(context.Background(), appwire.DelegateStopParams{Ref: "local:child", DelegateID: "dlg_1"}); err == nil {
			t.Fatal("want a stop aimed at a subagent's own thread refused")
		}
	})

	t.Run("forwards the delegate and answers its outcome", func(t *testing.T) {
		s := NewServer(ServerConfig{})
		s.SetAppIdentity("local", "root")
		var got string
		s.SetDelegateStopFunc(func(delegateID string) (appwire.DelegateStopOutcome, error) {
			got = delegateID
			return appwire.DelegateStopNotRunning, nil
		})
		resp, err := s.handleAppDelegateStop(context.Background(), appwire.DelegateStopParams{Ref: "local:root", DelegateID: " dlg_1 "})
		if err != nil || resp.Outcome != appwire.DelegateStopNotRunning || got != "dlg_1" {
			t.Fatalf("stop = %+v, %v with delegate %q; want notRunning for dlg_1", resp, err, got)
		}
	})

	t.Run("an unknown delegate is not found", func(t *testing.T) {
		s := NewServer(ServerConfig{})
		s.SetAppIdentity("local", "root")
		s.SetDelegateStopFunc(func(delegateID string) (appwire.DelegateStopOutcome, error) {
			return "", fmt.Errorf("%w %q", agent.ErrUnknownDelegate, delegateID)
		})
		_, err := s.handleAppDelegateStop(context.Background(), appwire.DelegateStopParams{Ref: "local:root", DelegateID: "dlg_gone"})
		if wireErrorInfo(err) != appwire.ErrorResourceNotFound {
			t.Fatalf("unknown delegate = %v, want resourceNotFound", err)
		}
	})
}

// wireErrorInfo is the evener error kind a handler's error carries, "" when
// it is not a wire error.
func wireErrorInfo(err error) appwire.ErrorInfo {
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		return ""
	}
	data, _ := wire.Data.(appwire.ErrorData)
	return data.EvenerErrorInfo
}
```

`cmd/evener/serve_delegate_stop_test.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// A served root routes the direct subagent stop to its own session's delegate
// tree (S6): an id the tree does not hold is not found.
func TestServeDelegateStopReachesTheSessionsTree(t *testing.T) {
	workDir, stateDir, runDir := t.TempDir(), t.TempDir(), t.TempDir()
	installServeScriptedProvider(t, &scriptedProvider{name: "openai", steps: []func(llm.Request) llm.Response{}})
	done := make(chan error, 1)
	go func() {
		done <- runServe([]string{"--model", "openai/gpt-test", "--addr", "127.0.0.1:0", "--dir", workDir, "--state-dir", stateDir, "--run-dir", runDir})
	}()
	entry := waitForServeTestRendezvous(t, runDir)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	transport, err := appwire.DialWebSocket(ctx, "ws://"+entry.Address+"/rpc", http.DefaultClient)
	if err != nil {
		t.Fatalf("DialWebSocket: %v", err)
	}
	client := appwire.NewClient(transport)
	client.Start(context.WithoutCancel(ctx))
	defer client.Close()
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ClientInfo: appwire.ClientInfo{Name: "serve-delegate-stop-test", Version: "test"}}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	ref := appwire.Ref{SourceID: "local", ThreadID: entry.SessionID}.String()

	var resp appwire.DelegateStopResponse
	err = client.Request(ctx, appwire.MethodEvenerDelegateStop, appwire.DelegateStopParams{Ref: ref, DelegateID: "dlg_not_in_this_tree"}, &resp)
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("stop of an unknown delegate = %+v, %v; want a wire error", resp, err)
	}
	// A client decodes the error's data as plain JSON; read it back as ErrorData.
	raw, _ := json.Marshal(wire.Data)
	var data appwire.ErrorData
	if json.Unmarshal(raw, &data) != nil || data.EvenerErrorInfo != appwire.ErrorResourceNotFound {
		t.Fatalf("stop of an unknown delegate = %v (%+v), want resourceNotFound", err, wire.Data)
	}

	if err := shutdownServeTestDaemon(context.Background(), entry.Address, entry.SessionID); err != nil {
		t.Fatalf("thread/shutdown: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServe: %v", err)
		}
	case <-time.After(10 * time.Second): // TRIPWIRE: an idle daemon exits at once after shutdown.
		t.Fatal("runServe did not exit after shutdown")
	}
}
```

In `server/appwire_retirement_admission_test.go`, `TestRetirementAdmissionCatalogCoverage`'s `expected` map gains the method right after the escalation resolve:

`server/appwire_retirement_admission_test.go`:

```diff
diff --git a/server/appwire_retirement_admission_test.go b/server/appwire_retirement_admission_test.go
--- a/server/appwire_retirement_admission_test.go
+++ b/server/appwire_retirement_admission_test.go
@@ -83,7 +83,7 @@ func TestRetirementAdmissionFailsClosedForUnclassifiedMethod(t *testing.T) {
 func TestRetirementAdmissionCatalogCoverage(t *testing.T) {
 	expected := map[string]string{
 		appwire.MethodThreadList: "read", appwire.MethodThreadRead: "read", appwire.MethodThreadUnsubscribe: "read", appwire.MethodThreadTurnsList: "read", appwire.MethodEvenerTasksList: "read", appwire.MethodEvenerJobsList: "read", appwire.MethodEvenerJobsOutput: "read", appwire.MethodModelList: "read",
-		appwire.MethodThreadClear: "mutation", appwire.MethodThreadModelSet: "mutation", appwire.MethodEvenerThreadNameSet: "mutation", appwire.MethodThreadReasoningEffortSet: "mutation", appwire.MethodThreadVisionModelSet: "mutation", appwire.MethodThreadCompactStart: "mutation", appwire.MethodTurnStart: "mutation", appwire.MethodTurnSteer: "mutation", appwire.MethodTurnInterrupt: "mutation", appwire.MethodTurnQueue: "mutation", appwire.MethodTurnDrainAsSteer: "mutation", appwire.MethodTurnPromoteQueuedAsSteer: "mutation", appwire.MethodTurnCancelQueued: "mutation", appwire.MethodGoalSet: "mutation", appwire.MethodEvenerSandboxEscalationResolve: "mutation", appwire.MethodNotesHumanSet: "mutation", appwire.MethodUrlsRemove: "mutation", appwire.MethodThreadShutdown: "control", appwire.MethodEvenerDaemonStatus: "control", appwire.MethodEvenerDaemonRetire: "control", appwire.MethodEvenerDaemonIdleTimeoutSet: "control",
+		appwire.MethodThreadClear: "mutation", appwire.MethodThreadModelSet: "mutation", appwire.MethodEvenerThreadNameSet: "mutation", appwire.MethodThreadReasoningEffortSet: "mutation", appwire.MethodThreadVisionModelSet: "mutation", appwire.MethodThreadCompactStart: "mutation", appwire.MethodTurnStart: "mutation", appwire.MethodTurnSteer: "mutation", appwire.MethodTurnInterrupt: "mutation", appwire.MethodTurnQueue: "mutation", appwire.MethodTurnDrainAsSteer: "mutation", appwire.MethodTurnPromoteQueuedAsSteer: "mutation", appwire.MethodTurnCancelQueued: "mutation", appwire.MethodGoalSet: "mutation", appwire.MethodEvenerSandboxEscalationResolve: "mutation", appwire.MethodEvenerDelegateStop: "mutation", appwire.MethodNotesHumanSet: "mutation", appwire.MethodUrlsRemove: "mutation", appwire.MethodThreadShutdown: "control", appwire.MethodEvenerDaemonStatus: "control", appwire.MethodEvenerDaemonRetire: "control", appwire.MethodEvenerDaemonIdleTimeoutSet: "control",
 	}
 	catalog := appwire.CatalogMethodNames(appwire.ScopeDaemon)
 	if len(daemonRetirementAccessKinds) != len(catalog) {
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./server -run 'TestHandleDelegateStop|TestRetirementAdmissionCatalogCoverage' -count=1`
Expected: FAIL to compile (`s.handleAppDelegateStop undefined`, `s.SetDelegateStopFunc undefined`).

Run: `go test ./cmd/evener -run TestServeDelegateStopReachesTheSessionsTree -count=1`
Expected: FAIL: the daemon answers `method not found: evener/delegate/stop`, not `resourceNotFound`.

- [ ] **Step 3: Implement**

`server/server.go`:

```diff
diff --git a/server/server.go b/server/server.go
--- a/server/server.go
+++ b/server/server.go
@@ -444,7 +444,10 @@ type Server struct {
 	// pending sandbox-exemption escalation (M7) to the session, unblocking the
 	// waiting tool-exec goroutine. nil when no session is attached.
 	sandboxEscalationResolveFunc func(escalationID string, approve bool) error
-	processing                   bool
+	// delegateStopFunc ends one delegate's current run at the user's request
+	// (S6). nil when no session is attached.
+	delegateStopFunc func(delegateID string) (appwire.DelegateStopOutcome, error)
+	processing       bool
 	// appMutationGate serializes retry-safe turn mutations with thread/clear.
 	// A clear must not rotate the live instance while an old-generation turn
 	// callback is still admitted, and a delayed turn must not enter after clear
@@ -653,6 +656,14 @@ func (s *Server) SetSandboxEscalationResolveFunc(fn func(escalationID string, ap
 	s.mu.Unlock()
 }
 
+// SetDelegateStopFunc sets the callback evener/delegate/stop hands a delegate
+// id to (S6).
+func (s *Server) SetDelegateStopFunc(fn func(delegateID string) (appwire.DelegateStopOutcome, error)) {
+	s.mu.Lock()
+	s.delegateStopFunc = fn
+	s.mu.Unlock()
+}
+
 // SetSteerFunc sets the function called by turn/steer. It is invoked
 // regardless of whether the session is currently processing.
 func (s *Server) SetSteerFunc(fn func(string) error) {
```

`server/appwire_runtime.go`:

```diff
diff --git a/server/appwire_runtime.go b/server/appwire_runtime.go
--- a/server/appwire_runtime.go
+++ b/server/appwire_runtime.go
@@ -1132,6 +1132,7 @@ func (s *Server) registerAppWireHandlers() {
 	appserver.HandleTyped(router, appwire.MethodTurnStart, s.handleAppTurnStart)
 	appserver.HandleTyped(router, appwire.MethodTurnSteer, s.handleAppTurnSteer)
 	appserver.HandleTyped(router, appwire.MethodEvenerSandboxEscalationResolve, s.handleAppSandboxEscalationResolve)
+	appserver.HandleTyped(router, appwire.MethodEvenerDelegateStop, s.handleAppDelegateStop)
 	appserver.HandleTyped(router, appwire.MethodTurnInterrupt, s.handleAppTurnInterrupt)
 	appserver.HandleTyped(router, appwire.MethodTurnQueue, s.handleAppTurnQueue)
 	appserver.HandleTyped(router, appwire.MethodTurnDrainAsSteer, s.handleAppTurnDrainAsSteer)
@@ -1620,6 +1621,34 @@ func (s *Server) handleAppSandboxEscalationResolve(_ context.Context, params app
 	return appwire.EmptyResponse{}, nil
 }
 
+// handleAppDelegateStop ends one subagent's run at the user's request (S6).
+// It targets the root that owns the tree, the way every turn mutation does.
+// Stopping is idempotent, so it needs no client mutation id: a retry finds the
+// run already ending and answers notRunning.
+func (s *Server) handleAppDelegateStop(_ context.Context, params appwire.DelegateStopParams) (appwire.DelegateStopResponse, error) {
+	if err := s.requireRootMutationTarget(params.Ref, params.ThreadID); err != nil {
+		return appwire.DelegateStopResponse{}, err
+	}
+	delegateID := strings.TrimSpace(params.DelegateID)
+	if delegateID == "" {
+		return appwire.DelegateStopResponse{}, appwire.InvalidParams("delegateId is required")
+	}
+	s.mu.RLock()
+	fn := s.delegateStopFunc
+	s.mu.RUnlock()
+	if fn == nil {
+		return appwire.DelegateStopResponse{}, appwire.Unavailable("delegate stop not available")
+	}
+	outcome, err := fn(delegateID)
+	if errors.Is(err, agent.ErrUnknownDelegate) {
+		return appwire.DelegateStopResponse{}, appwire.ResourceNotFound(err.Error())
+	}
+	if err != nil {
+		return appwire.DelegateStopResponse{}, err
+	}
+	return appwire.DelegateStopResponse{Outcome: outcome}, nil
+}
+
 func (s *Server) handleAppTurnInterrupt(ctx context.Context, params appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
 	params.ClientMutationID = strings.TrimSpace(params.ClientMutationID)
 	params.ExpectedInstanceID = strings.TrimSpace(params.ExpectedInstanceID)
```

`server/appwire_retirement_admission.go`:

```diff
diff --git a/server/appwire_retirement_admission.go b/server/appwire_retirement_admission.go
--- a/server/appwire_retirement_admission.go
+++ b/server/appwire_retirement_admission.go
@@ -34,6 +34,7 @@ var daemonRetirementAccessKinds = map[string]string{
 	appwire.MethodTurnCancelQueued:               "mutation",
 	appwire.MethodGoalSet:                        "mutation",
 	appwire.MethodEvenerSandboxEscalationResolve: "mutation",
+	appwire.MethodEvenerDelegateStop:             "mutation",
 	appwire.MethodNotesHumanSet:                  "mutation",
 	appwire.MethodUrlsRemove:                     "mutation",
 	appwire.MethodThreadShutdown:                 "control",
```

`appwire/protocol.go`:

```diff
diff --git a/appwire/protocol.go b/appwire/protocol.go
--- a/appwire/protocol.go
+++ b/appwire/protocol.go
@@ -257,6 +257,7 @@ var Methods = []MethodSpec{
 	{MethodEvenerSettingsAgentsDocGet, EmptyParams{}, AgentsDocResponse{}, ScopeHub, "Reads the personal AGENTS.md under the user config root: its path, whether it exists, and its content."},
 	{MethodEvenerSettingsAgentsDocSet, AgentsDocSetParams{}, AgentsDocResponse{}, ScopeHub, "Replaces the personal AGENTS.md whole (no precondition); broadcasts evener/settings/agentsDoc/changed."},
 	{MethodEvenerSandboxEscalationResolve, SandboxEscalationResolveParams{}, EmptyResponse{}, ScopeBoth, "Delivers a human's approve/deny decision for a pending sandbox-exemption escalation (M7); the daemon unblocks the waiting tool-exec goroutine, the hub relays."},
+	{MethodEvenerDelegateStop, DelegateStopParams{}, DelegateStopResponse{}, ScopeDaemon, "Ends one subagent's current run at the user's request (S6): that subagent alone, while the subagents it started keep running; the root's daemon serves it. Answers stopping or notRunning."},
 	{MethodEvenerHostRequest, HostRequestParams{}, HostForwardedResult{}, ScopeHub, "Forwards one hub-scoped admin RPC to a named remote host's hub through the allow-listed proxy (component 07a); the result is the forwarded method's own result, verbatim — an opaque JSON object, not a wrapper, so a typed client must treat the result as unknown and cast it to the forwarded method's own result type (see HostForwardedResult)."},
 	{MethodEvenerHostAttach, HostAttachParams{}, HostAttachResponse{}, ScopeHub, "Explicitly attaches one configured remote host by name through the Ensure-backed dialing seam (component 06's Connect action); a mutation and the only browser-reachable attach trigger, idempotent while attached, returning the host's post-attach state."},
 	{MethodEvenerHostAdd, HostAddParams{}, HostMutationResult{}, ScopeHub, "Registers one host entry (its full entry: name, ssh address, user, key path, and the host's paths and roots) into the machine-managed hub.toml; validates like hub.toml loading and refuses a name the live set already holds. Result is the mutation-result union: committed, committed-with-teardown-failure, collision-dropped, or the keyless-add ambiguous arm."},
```

`cmd/evener/serve.go`:

```diff
diff --git a/cmd/evener/serve.go b/cmd/evener/serve.go
--- a/cmd/evener/serve.go
+++ b/cmd/evener/serve.go
@@ -104,6 +104,7 @@ type serveServer interface {
 	// fallible step has already happened by the time anything is announced.
 	ReplaceAppIdentity(server.PreparedAppIdentity, func())
 	SetSandboxEscalationResolveFunc(func(string, bool) error)
+	SetDelegateStopFunc(func(string) (appwire.DelegateStopOutcome, error))
 	SetCompactFunc(func(context.Context) error)
 	SetSteerFunc(func(string) error)
 	SetSteerWithImagesFunc(func(string, []server.ImageAttachment) error)
@@ -1425,6 +1426,9 @@ func runServeWithDeps(args []string, deps serveDeps) error {
 	srv.SetSandboxEscalationResolveFunc(func(id string, approve bool) error {
 		return getSession().ResolveSandboxEscalation(id, approve)
 	})
+	srv.SetDelegateStopFunc(func(delegateID string) (appwire.DelegateStopOutcome, error) {
+		return getSession().StopDelegateRun(delegateID)
+	})
 	srv.SetCompactFunc(func(ctx context.Context) error { return getSession().Compact(ctx) })
 	// The steer RPC carries human-sent steering, so it takes the user-sourced
 	// entry points: UIs render it as a user message, not a system steering
```

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./server ./appwire -count=1 && go test ./cmd/evener -run TestServeDelegateStop -count=1`
Expected: PASS.

- [ ] **Step 5: Gates and commit**

```bash
make generate && go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1
$(go env GOROOT)/bin/gofmt -l server appwire cmd/evener
go vet ./server ./appwire ./cmd/evener && go vet -tags evenerfuzz ./server ./appwire ./cmd/evener && GOOS=windows go vet -tags evenerfuzz ./server ./appwire ./cmd/evener
golangci-lint run ./server/ ./appwire/ ./cmd/evener/
go test ./cmd/evener-hub -count=1   # the hub's catalog tests: a ScopeDaemon row needs no hub handler
git add server appwire/protocol.go cmd/evener/serve.go cmd/evener/serve_delegate_stop_test.go appwire-client/typescript/types.gen.ts docs/appwire-protocol.md
git commit -m "feat(server): evener/delegate/stop stops one subagent's run (S6a)"
```

**PR.** Title "feat(server): stop one subagent without its tree (S6a, phase 7 PR 30)". The body says the stop cancels one run and writes no durable stop (ruling 1), why a target-only subtree stop was not built (the ancestor fence), that it is idempotent (ruling 2), that nothing on the phone reads it until PR 31, and that no projector change means no TUI case.

---

## PR 31: the hub relays the stop, and roots advertise it, S6b (Tasks 31.1-31.2)

**Branch:** `git fetch origin && git switch -c claude/s6b-delegate-stop-hub origin/main` (after PR 30 merges)

**What it adds.** `evener/delegate/stop` through the hub to the root's daemon, and `stopSubagent` on root threads. The phone can switch off its fallback (Phone lane handoff).

### Task 31.1: The hub relays evener/delegate/stop to the root's source

**Implementer:** Sonnet.

**Files:**
- Modify: `appwire/protocol.go` (the row becomes `ScopeBoth`)
- Modify: `cmd/evener-hub/internal/appsource/source.go` (`DelegateStopSource` before `ItemCandidateSource`)
- Modify: `cmd/evener-hub/internal/appsource/local_daemon.go` (`StopDelegate` before `InterruptTurn`)
- Modify: `cmd/evener-hub/internal/appsource/remote_hub_mutations.go` (`StopDelegate` before `InterruptTurn`)
- Modify: `cmd/evener-hub/app_rpc.go` (the handler before `MethodTurnQueue`'s)
- Modify: `cmd/evener-hub/app_sources.go` (`admitSessionRecovery` reads the stop's target like the escalation resolve's)
- Create: `cmd/evener-hub/app_delegate_stop_test.go`
- Modify tests: `cmd/evener-hub/app_rpc_test.go` (`TestHubRPCRegistersExpectedHandlerSet`), `cmd/evener-hub/app_force_stop_test.go` (`TestRecoveryAdmissionUsesNativeTargetAndPreservesRetryEpoch`), `cmd/evener-hub/app_restart_required_test.go`, `cmd/evener-hub/app_host_admin_test.go` (`TestHostAdminAllowListMatchesCatalog`)

**Interfaces:**
- Consumes: PR 30's method, params and response.
- Produces: `appsource.DelegateStopSource{ StopDelegate(context.Context, appwire.DelegateStopParams) (appwire.DelegateStopResponse, error) }`, implemented by `LocalDaemonSource` and `RemoteHubSource`. It is an optional interface, like `ItemCandidateSource`, so the test fakes that implement `Source` need no new method.

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/app_delegate_stop_test.go`:

```go
package hub

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

// stoppingAppSource is a scripted source that can stop a subagent, recording
// what it was asked.
type stoppingAppSource struct {
	*scriptedAppSource
	got     []appwire.DelegateStopParams
	outcome appwire.DelegateStopOutcome
}

func (s *stoppingAppSource) StopDelegate(_ context.Context, params appwire.DelegateStopParams) (appwire.DelegateStopResponse, error) {
	s.got = append(s.got, params)
	return appwire.DelegateStopResponse{Outcome: s.outcome}, nil
}

// The hub routes a stop to the source that owns the root session, and hands
// back its outcome (S6).
func TestHubRoutesDelegateStopToTheRootsSource(t *testing.T) {
	source := &stoppingAppSource{scriptedAppSource: &scriptedAppSource{id: "local"}, outcome: appwire.DelegateStopStopping}
	registry := appsource.NewRegistry()
	registry.Add(source)
	server := newHubAppServer(hubcore.WebConfig{}, registry)

	params := appwire.DelegateStopParams{Ref: "local:root", DelegateID: "dlg_1"}
	got, err := exactDispatch(t.Context(), t, server, appwire.MethodEvenerDelegateStop, params)
	if err != nil {
		t.Fatalf("evener/delegate/stop: %v", err)
	}
	if resp, ok := got.(appwire.DelegateStopResponse); !ok || resp.Outcome != appwire.DelegateStopStopping {
		t.Fatalf("response = %#v, want stopping", got)
	}
	if len(source.got) != 1 || source.got[0] != params {
		t.Fatalf("source received %+v, want exactly %+v", source.got, params)
	}
}

// A source that cannot stop a subagent answers Unavailable, which is the
// phone's cue to keep asking the coordinator instead.
func TestHubDelegateStopIsUnavailableWhereTheSourceCannotStop(t *testing.T) {
	registry := appsource.NewRegistry()
	registry.Add(&scriptedAppSource{id: "local"})
	server := newHubAppServer(hubcore.WebConfig{}, registry)

	_, err := exactDispatch(t.Context(), t, server, appwire.MethodEvenerDelegateStop, appwire.DelegateStopParams{Ref: "local:root", DelegateID: "dlg_1"})
	if wire := appserver.WireError(err); wire.Code != appwire.CodeUnavailable {
		t.Fatalf("stop through a source without it = %v, want unavailable", err)
	}
}
```

Add the method to the tables that must name every routed method:

`cmd/evener-hub/app_rpc_test.go`:

```diff
diff --git a/cmd/evener-hub/app_rpc_test.go b/cmd/evener-hub/app_rpc_test.go
--- a/cmd/evener-hub/app_rpc_test.go
+++ b/cmd/evener-hub/app_rpc_test.go
@@ -12698,6 +12698,7 @@ func TestHubRPCRegistersExpectedHandlerSet(t *testing.T) {
 		appwire.MethodTurnSteer,
 		appwire.MethodTurnInterrupt,
 		appwire.MethodEvenerSandboxEscalationResolve,
+		appwire.MethodEvenerDelegateStop,
 		appwire.MethodTurnQueue,
 		appwire.MethodTurnDrainAsSteer,
 		appwire.MethodTurnPromoteQueuedAsSteer,
```

`cmd/evener-hub/app_force_stop_test.go`:

```diff
diff --git a/cmd/evener-hub/app_force_stop_test.go b/cmd/evener-hub/app_force_stop_test.go
--- a/cmd/evener-hub/app_force_stop_test.go
+++ b/cmd/evener-hub/app_force_stop_test.go
@@ -1187,6 +1187,8 @@ func TestRecoveryAdmissionUsesNativeTargetAndPreservesRetryEpoch(t *testing.T) {
 		{"sandbox threadId", appwire.MethodEvenerSandboxEscalationResolve, "B", map[string]any{"threadId": "B"}},
 		{"sandbox ignores mutation ID", appwire.MethodEvenerSandboxEscalationResolve, "B", map[string]any{"ref": "local:B", "clientMutationId": []string{"ignored"}}},
 		{"sandbox invalid target", appwire.MethodEvenerSandboxEscalationResolve, "", map[string]any{"threadId": []string{"invalid"}}},
+		{"delegate stop ref precedence", appwire.MethodEvenerDelegateStop, "B", map[string]any{"ref": "local:B", "threadId": "A", "delegateId": "dlg_1"}},
+		{"delegate stop threadId", appwire.MethodEvenerDelegateStop, "B", map[string]any{"threadId": "B", "delegateId": "dlg_1"}},
 		{"model ignores unknown field types", appwire.MethodThreadModelSet, "B", map[string]any{"ref": "local:B", "threadId": []string{"ignored"}}},
 		{"unknown method", "unknown", "", map[string]any{"ref": "local:B"}},
 		{"foreign ref", appwire.MethodThreadResume, "", map[string]any{"ref": "remote:B", "sessionId": "A"}},
```

`cmd/evener-hub/app_restart_required_test.go`:

```diff
diff --git a/cmd/evener-hub/app_restart_required_test.go b/cmd/evener-hub/app_restart_required_test.go
--- a/cmd/evener-hub/app_restart_required_test.go
+++ b/cmd/evener-hub/app_restart_required_test.go
@@ -107,6 +107,7 @@ func testHubProtocolUpgrade(t *testing.T, protocol string, cleared, cached bool)
 	}{
 		{appwire.MethodThreadReasoningEffortSet, appwire.ThreadReasoningEffortSetParams{Ref: ref, ReasoningEffort: "high"}},
 		{appwire.MethodEvenerSandboxEscalationResolve, appwire.SandboxEscalationResolveParams{Ref: ref, EscalationID: "escalation", Approve: true}},
+		{appwire.MethodEvenerDelegateStop, appwire.DelegateStopParams{Ref: ref, DelegateID: "dlg_1"}},
 	} {
 		t.Run(request.method, func(t *testing.T) {
 			var response any
```

`cmd/evener-hub/app_host_admin_test.go`:

```diff
diff --git a/cmd/evener-hub/app_host_admin_test.go b/cmd/evener-hub/app_host_admin_test.go
--- a/cmd/evener-hub/app_host_admin_test.go
+++ b/cmd/evener-hub/app_host_admin_test.go
@@ -389,9 +389,10 @@ func TestHostAdminAllowListMatchesCatalog(t *testing.T) {
 	// refused with appwire.InvalidParams without reaching the remote. The denied
 	// families are controller-local state or UI (navigation, jobs, tasks,
 	// thread/turn, keybindings, overview, transcript display), local-process
-	// control (upgrade, update, mobile pairing, sandbox escalation), other
-	// mutating local surfaces (archive, pin, favorite, project delete, URLs,
-	// search, subagent preview), and the proxy method itself (no chaining).
+	// control (upgrade, update, mobile pairing, sandbox escalation, subagent
+	// stop), other mutating local surfaces (archive, pin, favorite, project
+	// delete, URLs, search, subagent preview), and the proxy method itself (no
+	// chaining).
 	// Rows are added one method at a time: a catalog method with no row fails the
 	// coverage check below, so a future addition still forces a decision.
 	//
@@ -428,6 +429,7 @@ func TestHostAdminAllowListMatchesCatalog(t *testing.T) {
 		// and never forwarded.
 		"evener/daemon/list":    false,
 		"evener/daemon/retire":  false,
+		"evener/delegate/stop":  false,
 		"evener/dirs/create":    true, // discovery: create the host directory the spawn form asked for
 		"evener/favorite/set":   false,
 		"evener/git/head":       true, // discovery: read-only branch metadata for a remote path
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub -run 'TestHubRoutesDelegateStop|TestHubDelegateStopIsUnavailable|TestHubRPCRegistersExpectedHandlerSet|TestRecoveryAdmissionUsesNativeTarget|TestHostAdminAllowListMatchesCatalog' -count=1`
Expected: FAIL, five ways:
- the two new tests: `method not found: evener/delegate/stop`;
- `TestRecoveryAdmissionUsesNativeTargetAndPreservesRetryEpoch/delegate_stop_*`: `admission={sessionID: epoch:0} present=false`;
- `TestHostAdminAllowListMatchesCatalog`: `policy names "evener/delegate/stop", which is not a ScopeHub catalog method`;
- `TestHubRPCRegistersExpectedHandlerSet`: `named but NOT registered: [evener/delegate/stop]`.

- [ ] **Step 3: Implement**

`appwire/protocol.go`:

```diff
diff --git a/appwire/protocol.go b/appwire/protocol.go
--- a/appwire/protocol.go
+++ b/appwire/protocol.go
@@ -257,7 +257,7 @@ var Methods = []MethodSpec{
 	{MethodEvenerSettingsAgentsDocGet, EmptyParams{}, AgentsDocResponse{}, ScopeHub, "Reads the personal AGENTS.md under the user config root: its path, whether it exists, and its content."},
 	{MethodEvenerSettingsAgentsDocSet, AgentsDocSetParams{}, AgentsDocResponse{}, ScopeHub, "Replaces the personal AGENTS.md whole (no precondition); broadcasts evener/settings/agentsDoc/changed."},
 	{MethodEvenerSandboxEscalationResolve, SandboxEscalationResolveParams{}, EmptyResponse{}, ScopeBoth, "Delivers a human's approve/deny decision for a pending sandbox-exemption escalation (M7); the daemon unblocks the waiting tool-exec goroutine, the hub relays."},
-	{MethodEvenerDelegateStop, DelegateStopParams{}, DelegateStopResponse{}, ScopeDaemon, "Ends one subagent's current run at the user's request (S6): that subagent alone, while the subagents it started keep running; the root's daemon serves it. Answers stopping or notRunning."},
+	{MethodEvenerDelegateStop, DelegateStopParams{}, DelegateStopResponse{}, ScopeBoth, "Ends one subagent's current run at the user's request (S6): that subagent alone, while the subagents it started keep running; the root's daemon serves it, the hub relays. Answers stopping or notRunning."},
 	{MethodEvenerHostRequest, HostRequestParams{}, HostForwardedResult{}, ScopeHub, "Forwards one hub-scoped admin RPC to a named remote host's hub through the allow-listed proxy (component 07a); the result is the forwarded method's own result, verbatim — an opaque JSON object, not a wrapper, so a typed client must treat the result as unknown and cast it to the forwarded method's own result type (see HostForwardedResult)."},
 	{MethodEvenerHostAttach, HostAttachParams{}, HostAttachResponse{}, ScopeHub, "Explicitly attaches one configured remote host by name through the Ensure-backed dialing seam (component 06's Connect action); a mutation and the only browser-reachable attach trigger, idempotent while attached, returning the host's post-attach state."},
 	{MethodEvenerHostAdd, HostAddParams{}, HostMutationResult{}, ScopeHub, "Registers one host entry (its full entry: name, ssh address, user, key path, and the host's paths and roots) into the machine-managed hub.toml; validates like hub.toml loading and refuses a name the live set already holds. Result is the mutation-result union: committed, committed-with-teardown-failure, collision-dropped, or the keyless-add ambiguous arm."},
```

`cmd/evener-hub/internal/appsource/source.go`:

```diff
diff --git a/cmd/evener-hub/internal/appsource/source.go b/cmd/evener-hub/internal/appsource/source.go
--- a/cmd/evener-hub/internal/appsource/source.go
+++ b/cmd/evener-hub/internal/appsource/source.go
@@ -85,6 +85,14 @@ func (h HistoryIdentity) StampPage(response appwire.ThreadTurnsListResponse) app
 	return response
 }
 
+// DelegateStopSource is a source that can end one subagent's run at the
+// user's request (evener/delegate/stop, S6). The local daemon source and the
+// remote hub source implement it; a source without it answers Unavailable,
+// and a client keeps asking the coordinator instead.
+type DelegateStopSource interface {
+	StopDelegate(context.Context, appwire.DelegateStopParams) (appwire.DelegateStopResponse, error)
+}
+
 // ItemCandidateSource exposes positioned item candidates alongside the source
 // methods used by ordinary callers.
 type ItemCandidateSource interface {
```

`cmd/evener-hub/internal/appsource/local_daemon.go`:

```diff
diff --git a/cmd/evener-hub/internal/appsource/local_daemon.go b/cmd/evener-hub/internal/appsource/local_daemon.go
--- a/cmd/evener-hub/internal/appsource/local_daemon.go
+++ b/cmd/evener-hub/internal/appsource/local_daemon.go
@@ -508,6 +508,20 @@ func (s *LocalDaemonSource) ResolveSandboxEscalation(ctx context.Context, params
 	})
 }
 
+// StopDelegate forwards evener/delegate/stop to the root's daemon. Like every
+// root mutation it targets the root's own entry, never a subagent's alias.
+func (s *LocalDaemonSource) StopDelegate(ctx context.Context, params appwire.DelegateStopParams) (appwire.DelegateStopResponse, error) {
+	entry, err := s.entryForRef(params.Ref, params.ThreadID)
+	if err != nil {
+		return appwire.DelegateStopResponse{}, err
+	}
+	var out appwire.DelegateStopResponse
+	err = s.withClient(ctx, entry, func(ctx context.Context, client *appwire.Client) error {
+		return client.Request(ctx, appwire.MethodEvenerDelegateStop, params, &out)
+	})
+	return out, err
+}
+
 func (s *LocalDaemonSource) InterruptTurn(ctx context.Context, params appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
 	entry, err := s.entryForRef(params.Ref, params.ThreadID)
 	if err != nil {
```

`cmd/evener-hub/internal/appsource/remote_hub_mutations.go`:

```diff
diff --git a/cmd/evener-hub/internal/appsource/remote_hub_mutations.go b/cmd/evener-hub/internal/appsource/remote_hub_mutations.go
--- a/cmd/evener-hub/internal/appsource/remote_hub_mutations.go
+++ b/cmd/evener-hub/internal/appsource/remote_hub_mutations.go
@@ -187,6 +187,21 @@ func (s *RemoteHubSource) ResolveSandboxEscalation(ctx context.Context, params a
 	return s.call(ctx, appwire.MethodEvenerSandboxEscalationResolve, remote, nil)
 }
 
+// StopDelegate forwards evener/delegate/stop to the host hub that owns the
+// root, which relays it to the root's daemon.
+func (s *RemoteHubSource) StopDelegate(ctx context.Context, params appwire.DelegateStopParams) (appwire.DelegateStopResponse, error) {
+	ref, err := s.toRemoteRef(params.Ref, params.ThreadID)
+	if err != nil {
+		return appwire.DelegateStopResponse{}, err
+	}
+	remote := params
+	remote.Ref = ref.String()
+	remote.ThreadID = ref.ThreadID
+	var out appwire.DelegateStopResponse
+	err = s.call(ctx, appwire.MethodEvenerDelegateStop, remote, &out)
+	return out, err
+}
+
 func (s *RemoteHubSource) InterruptTurn(ctx context.Context, params appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
 	ref, err := s.toRemoteRef(params.Ref, params.ThreadID)
 	if err != nil {
```

`cmd/evener-hub/app_rpc.go`:

```diff
diff --git a/cmd/evener-hub/app_rpc.go b/cmd/evener-hub/app_rpc.go
--- a/cmd/evener-hub/app_rpc.go
+++ b/cmd/evener-hub/app_rpc.go
@@ -1784,6 +1784,22 @@ func registerThreadHandlers(
 			return appwire.EmptyResponse{}, source.ResolveSandboxEscalation(ctx, params)
 		})
 	})
+	appserver.HandleTyped(server.Router(), appwire.MethodEvenerDelegateStop, func(ctx context.Context, params appwire.DelegateStopParams) (appwire.DelegateStopResponse, error) {
+		return withSessionActionOwnership(ctx, cfg, params.Ref, params.ThreadID, func() (appwire.DelegateStopResponse, error) {
+			if err := refreshDaemonRestartRequiredError(ctx, cfg, params.Ref, params.ThreadID, ""); err != nil {
+				return appwire.DelegateStopResponse{}, err
+			}
+			source, err := sourceForThread(sources, params.Ref, params.ThreadID)
+			if err != nil {
+				return appwire.DelegateStopResponse{}, err
+			}
+			stopper, ok := source.(appsource.DelegateStopSource)
+			if !ok {
+				return appwire.DelegateStopResponse{}, appwire.Unavailable("this session's source cannot stop a subagent")
+			}
+			return stopper.StopDelegate(ctx, params)
+		})
+	})
 	appserver.HandleTyped(server.Router(), appwire.MethodTurnQueue, func(ctx context.Context, params appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
 		if err := validateAppWireInputItems(params.Input); err != nil {
 			return appwire.TurnQueueResponse{}, appwire.InvalidParams(err.Error())
```

`cmd/evener-hub/app_sources.go`:

```diff
diff --git a/cmd/evener-hub/app_sources.go b/cmd/evener-hub/app_sources.go
--- a/cmd/evener-hub/app_sources.go
+++ b/cmd/evener-hub/app_sources.go
@@ -339,7 +339,7 @@ func admitSessionRecovery(ctx context.Context, cfg hubcore.WebConfig, message ap
 			return ctx
 		}
 		rawRef, id = params.Ref, strings.TrimSpace(params.ThreadID)
-	case appwire.MethodEvenerSandboxEscalationResolve:
+	case appwire.MethodEvenerSandboxEscalationResolve, appwire.MethodEvenerDelegateStop:
 		var params appwire.SandboxEscalationResolveParams
 		if json.Unmarshal(message.Request.Params, &params) != nil {
 			return ctx
```

The recovery-admission case reuses `SandboxEscalationResolveParams` to read `ref` and `threadId`: both requests carry exactly those two target keys, and the extra keys are ignored.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./cmd/evener-hub -run 'TestHubRoutesDelegateStop|TestHubDelegateStopIsUnavailable|TestHubRPCRegistersExpectedHandlerSet|TestRecoveryAdmissionUsesNativeTarget|TestHostAdminAllowListMatchesCatalog|TestHubRouterMatchesCatalog|RestartRequired' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make generate && go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1
git add appwire/protocol.go cmd/evener-hub appwire-client/typescript/types.gen.ts docs/appwire-protocol.md
git commit -m "feat(hub): relay evener/delegate/stop to the root's daemon (S6b)"
```

### Task 31.2: Root threads advertise stopSubagent

**Implementer:** Sonnet.

**Files:**
- Modify: `appwire/types.go` (`ThreadCapabilities.StopSubagent` after `SkillInput`)
- Modify: `server/appwire_runtime.go` (`appCapabilitiesLocked`, after `SharedNotes`); `server/server.go` (`SetDelegateStopFunc`'s doc gains its second sentence)
- Modify: `cmd/evener-hub/internal/hubtest/capability_seams.go` (wire the stop)
- Modify: `cmd/evener-hub/internal/appsource/local_daemon.go` (`listRowCapabilities`' unprobed fallback)
- Modify: `cmd/evener-hub/internal/appsource/remote_hub_refs.go` (`maskRemoteThreadCapabilities`)
- Modify tests: `server/appwire_delegate_stop_test.go`, `cmd/evener-hub/app_capability_parity_test.go`, `cmd/evener-hub/internal/appsource/remote_hub_refs_test.go`, `cmd/evener-hub/internal/appsource/local_daemon_test.go`, `cmd/evener-hub/internal/hubcore/prober_wire_test.go`

**Interfaces:**
- Consumes: Task 31.1's `RemoteHubSource.StopDelegate` (the remote capability test calls it).
- Produces: `ThreadCapabilities.StopSubagent bool` (`json:"stopSubagent,omitempty"`).

- [ ] **Step 1: Write the failing tests**

`server/appwire_delegate_stop_test.go`:

```diff
diff --git a/server/appwire_delegate_stop_test.go b/server/appwire_delegate_stop_test.go
--- a/server/appwire_delegate_stop_test.go
+++ b/server/appwire_delegate_stop_test.go
@@ -70,6 +70,20 @@ func TestHandleDelegateStop(t *testing.T) {
 	})
 }
 
+// The root advertises the stop while its daemon wires it, so a client offers
+// "Stop subagent" instead of asking the coordinator (S6).
+func TestRootCapabilitiesAdvertiseStopSubagent(t *testing.T) {
+	s := NewServer(ServerConfig{})
+	s.SetAppIdentity("local", "root")
+	if s.appThread().Evener.Capabilities.StopSubagent {
+		t.Fatal("a daemon without the stop wired advertises it")
+	}
+	s.SetDelegateStopFunc(func(string) (appwire.DelegateStopOutcome, error) { return appwire.DelegateStopStopping, nil })
+	if !s.appThread().Evener.Capabilities.StopSubagent {
+		t.Fatal("a daemon with the stop wired does not advertise it")
+	}
+}
+
 // wireErrorInfo is the evener error kind a handler's error carries, "" when
 // it is not a wire error.
 func wireErrorInfo(err error) appwire.ErrorInfo {
```

The capability ledgers and fixtures that must decide every field:

`cmd/evener-hub/app_capability_parity_test.go`:

```diff
diff --git a/cmd/evener-hub/app_capability_parity_test.go b/cmd/evener-hub/app_capability_parity_test.go
--- a/cmd/evener-hub/app_capability_parity_test.go
+++ b/cmd/evener-hub/app_capability_parity_test.go
@@ -112,6 +112,7 @@ func TestCapabilityProjectionsMatchTheDaemonOracle(t *testing.T) {
 		"Steer":        "no daemon is running to carry out a steer",
 		"Interrupt":    "no daemon is running to interrupt",
 		"ForkFromTurn": "the hub's own operation; applyHubForkCapability owns the bit on every path that serves it",
+		"StopSubagent": "a session with no daemon running runs no subagents to stop",
 	}
 	assertCapabilityParity(t, "pastThreadCapabilities", daemon, pastThreadCapabilities(), pastDiffer, nil)
 
```

`cmd/evener-hub/internal/appsource/remote_hub_refs_test.go`:

```diff
diff --git a/cmd/evener-hub/internal/appsource/remote_hub_refs_test.go b/cmd/evener-hub/internal/appsource/remote_hub_refs_test.go
--- a/cmd/evener-hub/internal/appsource/remote_hub_refs_test.go
+++ b/cmd/evener-hub/internal/appsource/remote_hub_refs_test.go
@@ -30,6 +30,7 @@ func allRemoteThreadCapabilities() appwire.ThreadCapabilities {
 		SharedNotes:       true,
 		Rename:            true,
 		SkillInput:        true,
+		StopSubagent:      true,
 	}
 }
 
@@ -155,6 +156,9 @@ func TestRemoteHubCapabilitiesMatchForwardedMethods(t *testing.T) {
 		{"Rename", map[string]func() error{
 			"SetThreadName": func() error { return source.SetThreadName(ctx, appwire.ThreadNameSetParams{}) },
 		}},
+		{"StopSubagent", map[string]func() error{
+			"StopDelegate": func() error { _, err := source.StopDelegate(ctx, appwire.DelegateStopParams{}); return err },
+		}},
 		{"SkillInput", map[string]func() error{
 			"StartTurn": func() error { _, err := source.StartTurn(ctx, appwire.TurnStartParams{}); return err },
 			"QueueTurn": func() error { _, err := source.QueueTurn(ctx, appwire.TurnQueueParams{}); return err },
```

`cmd/evener-hub/internal/appsource/local_daemon_test.go`:

```diff
diff --git a/cmd/evener-hub/internal/appsource/local_daemon_test.go b/cmd/evener-hub/internal/appsource/local_daemon_test.go
--- a/cmd/evener-hub/internal/appsource/local_daemon_test.go
+++ b/cmd/evener-hub/internal/appsource/local_daemon_test.go
@@ -734,11 +734,13 @@ func TestLocalDaemonSourceListFallbackFoldsDaemonStatus(t *testing.T) {
 		Steer: true, Interrupt: true, Compact: true, Shutdown: true,
 		ChangeModel: true, ChangeVisionModel: true, Queue: true,
 		Goal: true, SharedNotes: true, Rename: true, SkillInput: true,
+		StopSubagent: true,
 	}
 	wantClearWithheld := appwire.ThreadCapabilities{
 		Send: true, Steer: true, Interrupt: true, Compact: true, Shutdown: true,
 		ChangeModel: true, ChangeVisionModel: true, Queue: true,
 		Goal: true, SharedNotes: true, Rename: true, SkillInput: true,
+		StopSubagent: true,
 	}
 	if got := capsByID["th_processing"]; got != wantActive {
 		t.Fatalf("active row = %+v, want the daemon's active answer (Send and Clear folded): %+v", got, wantActive)
```

`cmd/evener-hub/internal/hubcore/prober_wire_test.go`:

```diff
diff --git a/cmd/evener-hub/internal/hubcore/prober_wire_test.go b/cmd/evener-hub/internal/hubcore/prober_wire_test.go
--- a/cmd/evener-hub/internal/hubcore/prober_wire_test.go
+++ b/cmd/evener-hub/internal/hubcore/prober_wire_test.go
@@ -277,7 +277,7 @@ func TestStatusProberCarriesDaemonCapabilities(t *testing.T) {
 		Send: true, Steer: true, Interrupt: true, Queue: true,
 		Compact: true, Clear: true, Shutdown: true, ChangeModel: true,
 		ChangeVisionModel: true, Rename: true, Goal: true, SharedNotes: true,
-		SkillInput: true, // ForkFromTurn stays the daemon's hardwired false.
+		SkillInput: true, StopSubagent: true, // ForkFromTurn stays the daemon's hardwired false.
 	}
 	if got.Capabilities != want {
 		t.Fatalf("capabilities = %+v, want the daemon's idle set %+v", got.Capabilities, want)
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./server -run TestRootCapabilitiesAdvertiseStopSubagent -count=1`
Expected: FAIL to compile (`StopSubagent` is not a field of `appwire.ThreadCapabilities`). The hub tests fail to compile the same way.

- [ ] **Step 3: Implement**

`appwire/types.go`:

```diff
diff --git a/appwire/types.go b/appwire/types.go
--- a/appwire/types.go
+++ b/appwire/types.go
@@ -1334,6 +1334,12 @@ type ThreadCapabilities struct {
 	// against the live daemon. ValidateSkillInputSupport keeps skill items
 	// rejected wherever this capability is false.
 	SkillInput bool `json:"skillInput,omitempty"`
+	// StopSubagent advertises evener/delegate/stop on a root session (S6):
+	// true while its daemon wires the stop and the session is open. Absent
+	// from an older daemon, from a session with no daemon running (it runs no
+	// subagents), and from a subagent's own thread: the stop targets the root
+	// that owns the tree.
+	StopSubagent bool `json:"stopSubagent,omitempty"`
 }
 
 // EvenerHookEventStatus describes a single hook event's registration state.
```

`server/appwire_runtime.go`:

```diff
diff --git a/server/appwire_runtime.go b/server/appwire_runtime.go
--- a/server/appwire_runtime.go
+++ b/server/appwire_runtime.go
@@ -2856,6 +2856,10 @@ func (s *Server) appCapabilitiesLocked(state string, processing bool) appwire.Th
 		// the session is open. Like Goal it is NOT gated on !active: a human
 		// save may land mid-turn (it steers the running turn), unlike Send.
 		SharedNotes: s.notesHumanSetFunc != nil && s.urlsRemoveFunc != nil && !closed,
+		// StopSubagent is available whenever the stop is wired and the
+		// session is open. Like Interrupt, whether a run is there to stop is
+		// the handler's answer (S6).
+		StopSubagent: s.delegateStopFunc != nil && !closed,
 		// SkillInput advertises that this thread's input-bearing turn mutations
 		// (turn/start, turn/steer, turn/queue, turn/drainAsSteer) consume skill
 		// selections at the input's actual claim. It is true exactly when every
```

`server/server.go`:

```diff
diff --git a/server/server.go b/server/server.go
--- a/server/server.go
+++ b/server/server.go
@@ -657,7 +657,7 @@ func (s *Server) SetSandboxEscalationResolveFunc(fn func(escalationID string, ap
 }
 
 // SetDelegateStopFunc sets the callback evener/delegate/stop hands a delegate
-// id to (S6).
+// id to (S6). Wiring it advertises the stopSubagent capability.
 func (s *Server) SetDelegateStopFunc(fn func(delegateID string) (appwire.DelegateStopOutcome, error)) {
 	s.mu.Lock()
 	s.delegateStopFunc = fn
```

`cmd/evener-hub/internal/hubtest/capability_seams.go`:

```diff
diff --git a/cmd/evener-hub/internal/hubtest/capability_seams.go b/cmd/evener-hub/internal/hubtest/capability_seams.go
--- a/cmd/evener-hub/internal/hubtest/capability_seams.go
+++ b/cmd/evener-hub/internal/hubtest/capability_seams.go
@@ -49,4 +49,7 @@ func WireCapabilitySeams(srv *daemonserver.Server) {
 		return appwire.NotesHumanSetResponse{}, nil
 	})
 	srv.SetUrlsRemoveFunc(func(outerID, id string) (bool, error) { return false, nil })
+	srv.SetDelegateStopFunc(func(string) (appwire.DelegateStopOutcome, error) {
+		return appwire.DelegateStopNotRunning, nil
+	})
 }
```

`cmd/evener-hub/internal/appsource/local_daemon.go`:

```diff
diff --git a/cmd/evener-hub/internal/appsource/local_daemon.go b/cmd/evener-hub/internal/appsource/local_daemon.go
--- a/cmd/evener-hub/internal/appsource/local_daemon.go
+++ b/cmd/evener-hub/internal/appsource/local_daemon.go
@@ -1293,6 +1307,10 @@ func listRowCapabilities(item LocalDaemonEntry, status string) appwire.ThreadCap
 		// genuinely lacks the support still refuses each selection
 		// honestly.
 		SkillInput: true,
+		// The daemon advertises the subagent stop whenever it is wired and
+		// the session is open, like Interrupt. A descendant alias never gets
+		// it: the stop targets the root that owns the tree.
+		StopSubagent: !item.ReadOnlyAlias && !closed,
 	}
 }
 
```

`cmd/evener-hub/internal/appsource/remote_hub_refs.go`:

```diff
diff --git a/cmd/evener-hub/internal/appsource/remote_hub_refs.go b/cmd/evener-hub/internal/appsource/remote_hub_refs.go
--- a/cmd/evener-hub/internal/appsource/remote_hub_refs.go
+++ b/cmd/evener-hub/internal/appsource/remote_hub_refs.go
@@ -139,6 +139,7 @@ func maskRemoteThreadCapabilities(remote appwire.ThreadCapabilities) appwire.Thr
 		SharedNotes:       remote.SharedNotes && forwarded.SharedNotes,
 		Rename:            remote.Rename && forwarded.Rename,
 		SkillInput:        remote.SkillInput && forwarded.SkillInput,
+		StopSubagent:      remote.StopSubagent && forwarded.StopSubagent,
 	}
 }
 
```

The unprobed list-row fallback advertises the stop the way it advertises Interrupt: every daemon from PR 30 on wires it, and a row's probe, or the thread read the phone acts on, replaces the fallback with the daemon's own answer. A daemon from before PR 30 answers the stop with MethodNotFound, which the phone treats as the fallback.

A remote root never advertises the stop yet: `remoteForwardedThreadCapabilities` still names Shutdown alone, and the stop turns on with the other thread actions when the host capability probe lands.

- [ ] **Step 4: Run them to verify they pass**

Run:
```bash
go test ./server ./appwire -count=1
go test ./cmd/evener-hub/... -count=1
go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1
```
Expected: PASS.

- [ ] **Step 5: Gates and commit**

```bash
make generate && go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1
cd cmd/evener-hub/frontend && npm run typecheck && npx vitest run ../../../appwire-client/typescript/reducer.test.ts ../../../appwire-client/typescript/testing/fakeClient.test.ts && cd -
$(go env GOROOT)/bin/gofmt -l appwire server cmd/evener-hub
go vet ./appwire ./server ./cmd/evener-hub/... && go vet -tags evenerfuzz ./appwire ./server ./cmd/evener-hub/... && GOOS=windows go vet -tags evenerfuzz ./appwire ./server ./cmd/evener-hub/...
golangci-lint run ./appwire/ ./server/ ./cmd/evener-hub/ ./cmd/evener-hub/internal/appsource/ ./cmd/evener-hub/internal/hubtest/ ./cmd/evener-hub/internal/hubcore/
go test ./cmd/evener-tui/... -count=1
git add appwire server cmd/evener-hub appwire-client/typescript/types.gen.ts docs/appwire-protocol.md
git commit -m "feat(hub): root threads advertise stopSubagent (S6b)"
```

**PR.** Title "feat(hub): the phone can stop a subagent directly (S6b, phase 7 PR 31)". The body says the hub relays to the root's source, remote roots stay masked with the other thread actions, the capability is absent from past and alias threads (ruling 7), and no projector change means no TUI case (the TUI suite still runs, since it reads capabilities).

---

## PR 32: a session's access on the thread read, S15 (Tasks 32.1-32.3)

**Branch:** `git fetch origin && git switch -c claude/s15-session-access origin/main`

**What it adds.** `EvenerThread.access` on every thread read: the live root, a live subagent and a past session. The shared model carries it for the Session sheet.

### Task 32.1: The wire shape and the one mapping

**Implementer:** Sonnet.

**Files:**
- Modify: `appwire/types.go` (`EvenerThread.Access` after `Subagents`; `ThreadAccess` after `EvenerThread`)
- Create: `appwire/access.go`
- Modify: `appwire/clone.go` (`cloneEvenerThread`)
- Create: `appwire/access_test.go`

**Interfaces:**
- Produces: `appwire.ThreadAccess{Sandbox string; Network bool}` (`json:"sandbox"`, `json:"network"`), `EvenerThread.Access *ThreadAccess` (`json:"access,omitempty"`), `appwire.SandboxOff = "off"`, and `func SessionAccess(sandbox string, network *bool) *ThreadAccess`.

- [ ] **Step 1: Write the failing tests**

`appwire/access_test.go`:

```go
package appwire

import (
	"encoding/json"
	"testing"
)

func TestSessionAccessReadsThePersistedSandboxRequest(t *testing.T) {
	off, on := false, true
	for _, tc := range []struct {
		name    string
		sandbox string
		network *bool
		want    ThreadAccess
	}{
		{"an unsandboxed session persists no mode", "", nil, ThreadAccess{Sandbox: "off", Network: true}},
		{"off is off whatever the network flag says", "off", &off, ThreadAccess{Sandbox: "off", Network: true}},
		{"a sandboxed session defaults to the network on", "workspace-write", nil, ThreadAccess{Sandbox: "workspace-write", Network: true}},
		{"the network on", "read-only", &on, ThreadAccess{Sandbox: "read-only", Network: true}},
		{"the network off", "restricted", &off, ThreadAccess{Sandbox: "restricted", Network: false}},
		{"surrounding space is not a mode", " workspace-write ", &off, ThreadAccess{Sandbox: "workspace-write", Network: false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := SessionAccess(tc.sandbox, tc.network)
			if got == nil || *got != tc.want {
				t.Fatalf("SessionAccess(%q, %v) = %+v, want %+v", tc.sandbox, tc.network, got, tc.want)
			}
		})
	}
}

// The network key is always present: false is the fact a restricted session
// reports, never an absent key a client could read as "not known".
func TestThreadAccessAlwaysCarriesBothKeys(t *testing.T) {
	raw, err := json.Marshal(EvenerThread{Access: &ThreadAccess{Sandbox: "restricted", Network: false}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Access map[string]any `json:"access"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Access["sandbox"] != "restricted" || decoded.Access["network"] != false {
		t.Fatalf("access = %v, want sandbox restricted and network false", decoded.Access)
	}
	raw, err = json.Marshal(EvenerThread{})
	if err != nil {
		t.Fatal(err)
	}
	var bare map[string]any
	if json.Unmarshal(raw, &bare) != nil || bare["access"] != nil {
		t.Fatalf("a thread without access encoded %s, want no access key", raw)
	}
}

func TestCloneThreadOwnsAccess(t *testing.T) {
	original := Thread{Evener: EvenerThread{Access: &ThreadAccess{Sandbox: "read-only", Network: true}}}
	clone := CloneThread(original)
	clone.Evener.Access.Sandbox = "changed"
	if original.Evener.Access.Sandbox != "read-only" {
		t.Fatal("the clone shares the original's access")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./appwire -run 'TestSessionAccess|TestThreadAccess|TestCloneThreadOwnsAccess' -count=1`
Expected: FAIL to compile (`undefined: ThreadAccess`, `undefined: SessionAccess`).

- [ ] **Step 3: Implement**

`appwire/types.go`:

```diff
diff --git a/appwire/types.go b/appwire/types.go
--- a/appwire/types.go
+++ b/appwire/types.go
@@ -1002,6 +1002,22 @@ type EvenerThread struct {
 	// thread/list root rows only, when the tree has at least one subagent, and
 	// never a thread/read snapshot: no notification announces its changes.
 	Subagents *SubagentTally `json:"subagents,omitempty"`
+	// Access is what the session's sandbox lets it reach (S15): the sandbox
+	// mode it started under and whether that sandbox allows the network. Every
+	// current producer sets it; it is absent from an older daemon or hub, which
+	// a client reads as "not known". Snapshot-only: a session's sandbox is
+	// fixed when it starts, so no notification carries it.
+	Access *ThreadAccess `json:"access,omitempty"`
+}
+
+// ThreadAccess is a session's sandbox mode and network setting (spec 8.6,
+// S15). Sandbox is the mode name a session starts with ("off", "read-only",
+// "workspace-write" or "restricted"). Network is true when the session may use
+// the network: always for "off", and for a sandboxed session unless it started
+// with the network turned off.
+type ThreadAccess struct {
+	Sandbox string `json:"sandbox"`
+	Network bool   `json:"network"`
 }
 
 // ThreadActivity is one pulse meter sample. Minutes holds seven one-minute
```

`appwire/access.go`:

```go
package appwire

import "strings"

// SandboxOff is the sandbox mode of an unsandboxed session.
const SandboxOff = "off"

// SessionAccess is the Access a session reports for the sandbox request its
// configuration persists: the mode name (empty means off) and the network
// decision (nil means the default, on). A session that could not enforce its
// mode never starts, so the persisted request is what the session enforces.
func SessionAccess(sandbox string, network *bool) *ThreadAccess {
	mode := strings.TrimSpace(sandbox)
	if mode == "" || mode == SandboxOff {
		return &ThreadAccess{Sandbox: SandboxOff, Network: true}
	}
	return &ThreadAccess{Sandbox: mode, Network: network == nil || *network}
}
```

`appwire/clone.go`:

```diff
diff --git a/appwire/clone.go b/appwire/clone.go
--- a/appwire/clone.go
+++ b/appwire/clone.go
@@ -113,6 +113,7 @@ func cloneEvenerThread(e EvenerThread) EvenerThread {
 	e.Subagents = clonePointer(e.Subagents)
 	e.PendingQuestion = ClonePendingQuestion(e.PendingQuestion)
 	e.Failure = CloneThreadFailure(e.Failure)
+	e.Access = clonePointer(e.Access)
 	// Capabilities is all bools (value type) — no copy needed.
 	return e
 }
```

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./appwire -count=1`
Expected: PASS. (Before the `clone.go` line, `TestCloneThreadOwnsAccess` fails with "the clone shares the original's access"; the dry run checked it.)

- [ ] **Step 5: Commit**

```bash
make generate && go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1
git add appwire/types.go appwire/access.go appwire/access_test.go appwire/clone.go appwire-client/typescript/types.gen.ts
git commit -m "feat(appwire): a thread can say what its session's sandbox allows (S15)"
```

### Task 32.2: The daemon reports the root's and each subagent's access

**Implementer:** Sonnet.

**Files:**
- Modify: `agent/events/payloads.go` (`SessionStartData.Sandbox` and `SandboxNet` after `TaskStoreOwnerSessionID`)
- Modify: `agent/session_events.go` (`emitSessionStartEnvelope`)
- Create: `agent/session_start_access_test.go`
- Modify: `server/thread_envelope.go` (`threadEnvelope.Access`; the meta facet's sample and `assign`)
- Modify: `server/appwire_runtime.go` (`appThreadWithDiagnosticsLocked`; `RecordDescendantAppEvent`'s `ThreadStartedParams` case)
- Create: `server/thread_envelope_access_test.go`

**Interfaces:**
- Consumes: Task 32.1's `appwire.SessionAccess`.
- Produces: `events.SessionStartData.Sandbox string` (`json:"sandbox,omitempty"`) and `SandboxNet *bool` (`json:"sandbox_net,omitempty"`).

- [ ] **Step 1: Write the failing tests**

`agent/session_start_access_test.go`:

```go
package agent

import (
	"testing"
)

// SessionStart names the sandbox request the session started under, so a
// subagent's thread can report its own access (S15).
func TestSessionStartNamesTheSandboxRequest(t *testing.T) {
	networkOff := false
	dir := t.TempDir()
	session := newSession(t, withDir(dir), withConfig(SessionConfig{
		NonInteractive:   true,
		StateDir:         dir,
		NoProjectPrompts: true,
		Sandbox:          "read-only",
		SandboxNet:       &networkOff,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	start := queuedSessionStart(t, session)
	if start.Sandbox != "read-only" || start.SandboxNet == nil || *start.SandboxNet {
		t.Fatalf("SessionStart sandbox = %q network %v, want read-only with the network off", start.Sandbox, start.SandboxNet)
	}
}
```

`server/thread_envelope_access_test.go`:

```go
package server

import (
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// The root's thread snapshot carries the sandbox mode and network setting its
// meta persists (S15). A session is sandboxed once, when it starts, so the
// seed at identity install is the sample that matters.
func TestThreadSnapshotsCarryTheSessionsAccess(t *testing.T) {
	networkOff := false
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	publishEnvelope(srv, &stubThreadEnvelopeSource{meta: schema.SessionMeta{
		ID:     "root",
		Config: schema.ConfigSnapshot{Sandbox: "workspace-write", SandboxNet: &networkOff},
	}})
	want := appwire.ThreadAccess{Sandbox: "workspace-write", Network: false}
	if got := readThreadOverWire(t, srv, "local:root").Evener.Access; got == nil || *got != want {
		t.Fatalf("read access = %+v, want %+v", got, want)
	}
}

// An unsandboxed session persists no mode, and still says so: "off", with the
// network on.
func TestThreadSnapshotsSayAnUnsandboxedSessionIsOff(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	publishEnvelope(srv, &stubThreadEnvelopeSource{meta: schema.SessionMeta{ID: "root"}})
	want := appwire.ThreadAccess{Sandbox: "off", Network: true}
	if got := readThreadOverWire(t, srv, "local:root").Evener.Access; got == nil || *got != want {
		t.Fatalf("read access = %+v, want %+v", got, want)
	}
}

// A subagent can run in a narrower sandbox than its coordinator, so its own
// thread carries its own access, from its session's start (S15).
func TestDescendantThreadsCarryTheirOwnAccess(t *testing.T) {
	networkOff := false
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	publishEnvelope(srv, &stubThreadEnvelopeSource{meta: schema.SessionMeta{ID: "root", Config: schema.ConfigSnapshot{Sandbox: "workspace-write"}}})
	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventSessionStart, SessionID: "child", Data: events.SessionStartData{
		Sandbox: "read-only", SandboxNet: &networkOff,
	}})
	want := appwire.ThreadAccess{Sandbox: "read-only", Network: false}
	if got := readThreadOverWire(t, srv, "local:child").Evener.Access; got == nil || *got != want {
		t.Fatalf("descendant access = %+v, want %+v", got, want)
	}
	if got := readThreadOverWire(t, srv, "local:root").Evener.Access; got == nil || got.Sandbox != "workspace-write" {
		t.Fatalf("root access = %+v, want the root's own workspace-write", got)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./agent -run TestSessionStartNamesTheSandboxRequest -count=1`
Expected: FAIL to compile (`start.Sandbox undefined`).

Run: `go test ./server -run 'TestThreadSnapshots|TestDescendantThreadsCarryTheirOwnAccess' -count=1`
Expected: FAIL to compile (`unknown field Sandbox in struct literal of type events.SessionStartData`). With only the payload fields added, the three tests fail with `read access = <nil>`.

- [ ] **Step 3: Implement**

`agent/events/payloads.go`:

```diff
diff --git a/agent/events/payloads.go b/agent/events/payloads.go
--- a/agent/events/payloads.go
+++ b/agent/events/payloads.go
@@ -36,6 +36,12 @@ type SessionStartData struct {
 	State                   string               `json:"state,omitempty"`
 	CurrentWork             *CurrentWorkSeedData `json:"current_work,omitempty"`
 	TaskStoreOwnerSessionID string               `json:"task_store_owner_session_id,omitempty"`
+	// Sandbox and SandboxNet are the sandbox request the session started
+	// under, as its configuration persists it: the mode name (empty is off)
+	// and the network decision (nil is the default, on). A subagent's thread
+	// reports them as its access (S15).
+	Sandbox    string `json:"sandbox,omitempty"`
+	SandboxNet *bool  `json:"sandbox_net,omitempty"`
 	// TaskPublicationEpoch identifies the process-local TaskStore incarnation;
 	// TaskPublicationRevision orders its snapshots. Both are in-process routing
 	// metadata and never enter event JSON.
```

`agent/session_events.go`:

```diff
diff --git a/agent/session_events.go b/agent/session_events.go
--- a/agent/session_events.go
+++ b/agent/session_events.go
@@ -142,6 +142,13 @@ func (s *Session) ConsumeEventsLossless(consume func(events.SessionEvent), onDra
 // buffering them here means they now fire it, for the first time, once
 // hookRunner exists.
 func (s *Session) emitSessionStartEnvelope(start events.SessionStartData, promptSources []promptSource) {
+	// The sandbox request is fixed for the session's life; SessionStart
+	// carries it so a subagent's thread reports its own access (S15).
+	start.Sandbox = s.cfg.Sandbox
+	if s.cfg.SandboxNet != nil {
+		network := *s.cfg.SandboxNet
+		start.SandboxNet = &network
+	}
 	store := s.getOrCreateTaskStore()
 	_ = store.MutateAndPublish(func(epoch, revision uint64) error {
 		// Sample current work only after entering the shared store's publication
```

`server/thread_envelope.go`:

```diff
diff --git a/server/thread_envelope.go b/server/thread_envelope.go
--- a/server/thread_envelope.go
+++ b/server/thread_envelope.go
@@ -105,6 +105,10 @@ type threadEnvelope struct {
 	Preview         string
 	LastTurnEndedAt int64
 	LastMessage     string
+	// Access is the session's sandbox mode and network setting (S15), from
+	// the sandbox request its meta persists. A session's sandbox is fixed when
+	// it starts, so the seed at identity install is the sample that matters.
+	Access *appwire.ThreadAccess
 }
 
 // ThreadEnvelopeSource supplies the live session values the thread envelope
@@ -422,6 +426,7 @@ func (s *Server) refreshFacets(facets envelopeFacet) {
 				next.LastTurnEndedAt = meta.LastTurnEndedAt.UnixMilli()
 			}
 			next.LastMessage = meta.LastMessage
+			next.Access = appwire.SessionAccess(meta.Config.Sandbox, meta.Config.SandboxNet)
 		}
 		if facets&facetGoal != 0 {
 			if meta.Goal != nil {
@@ -550,5 +555,6 @@ func (e *threadEnvelope) assign(facets envelopeFacet, next threadEnvelope, notes
 		e.Preview = next.Preview
 		e.LastTurnEndedAt = next.LastTurnEndedAt
 		e.LastMessage = next.LastMessage
+		e.Access = next.Access
 	}
 }
```

`server/appwire_runtime.go`:

```diff
diff --git a/server/appwire_runtime.go b/server/appwire_runtime.go
--- a/server/appwire_runtime.go
+++ b/server/appwire_runtime.go
@@ -669,6 +669,7 @@ func (s *Server) RecordDescendantAppEvent(ownerThreadID string, event events.Ses
 				params.Thread.Evener.ParentRef = parentRef
 				projection.thread = params.Thread
 				projection.thread.Evener.Kind = "subagent"
+				projection.thread.Evener.Access = appwire.SessionAccess(start.Sandbox, start.SandboxNet)
 				projection.thread.Evener.Tasks = appwire.CloneTaskAggregate(params.Thread.Evener.Tasks)
 				projection.thread.Evener.Goal = cloneGoalState(params.Thread.Evener.Goal)
 				params.Thread = projection.thread
@@ -2394,6 +2395,7 @@ func (s *Server) appThreadWithDiagnosticsLocked(diagnostics func(DetailedStatus)
 	visionModel := envelope.VisionModel
 	lastTurnEndedAt := envelope.LastTurnEndedAt
 	lastMessage := envelope.LastMessage
+	access := envelope.Access
 	threadName := envelope.Name
 	threadPreview := envelope.Preview
 	if threadPreview == "" {
@@ -2443,6 +2445,7 @@ func (s *Server) appThreadWithDiagnosticsLocked(diagnostics func(DetailedStatus)
 			VisionModel:           visionModel,
 			LastTurnEndedAt:       lastTurnEndedAt,
 			LastMessage:           lastMessage,
+			Access:                access,
 		},
 	}
 }
```

The root samples its access in the meta facet, which `SESSION_START`, `SESSION_END` and `TURN_ENDED` all sample, and the pre-bridge seed samples too. A subagent's access is set where its thread is installed, not in the projector, so no projector output changes.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./agent -run TestSessionStartNamesTheSandboxRequest -count=1 && go test ./server -count=1`
Expected: PASS.

- [ ] **Step 5: Gates and commit**

```bash
$(go env GOROOT)/bin/gofmt -l agent server
(cd agent && go vet ./... && go vet -tags evenerfuzz ./... && GOOS=windows go vet -tags evenerfuzz ./... && golangci-lint run ./ ./events/)
go vet ./server && go vet -tags evenerfuzz ./server && GOOS=windows go vet -tags evenerfuzz ./server && golangci-lint run ./server/
git add agent/events/payloads.go agent/session_events.go agent/session_start_access_test.go server/thread_envelope.go server/appwire_runtime.go server/thread_envelope_access_test.go
git commit -m "feat(server): the root's and each subagent's thread carry its access (S15)"
```

### Task 32.3: Past sessions carry access, and the shared model keeps it

**Implementer:** Sonnet.

**Files:**
- Modify: `cmd/evener-hub/app_threadread.go` (`pastEntryThreadForList`, after `VisionModel`)
- Create: `cmd/evener-hub/app_threadread_access_test.go`
- Modify: `appwire-client/typescript/model.ts` (`ThreadModel.access` after `failedToolCalls`), `appwire-client/typescript/reducer.ts` (`threadFields`), `appwire-client/typescript/reducer.test.ts`

**Interfaces:**
- Consumes: `appwire.SessionAccess`; the generated `ThreadAccess` TS type.
- Produces: `ThreadModel.access?: ThreadAccess` for the phone's Session sheet.

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/app_threadread_access_test.go`:

```go
package hub

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
)

// A past session's read carries the sandbox mode and network setting its meta
// persisted (S15), so the Session sheet's Access section reads the same after
// the session ends as while it ran.
func TestPastThreadReadCarriesThePersistedAccess(t *testing.T) {
	cfg, sessionID, _ := seedPastSessionWithTasks(t, nil)
	entry, ok := cfg.Past.Find(sessionID)
	if !ok {
		t.Fatal("past entry not found")
	}
	networkOff := false
	entry.Meta.Config.Sandbox = "restricted"
	entry.Meta.Config.SandboxNet = &networkOff

	thread, err := pastEntryThread(context.Background(), cfg, entry, false)
	if err != nil {
		t.Fatalf("pastEntryThread: %v", err)
	}
	want := appwire.ThreadAccess{Sandbox: "restricted", Network: false}
	if got := thread.Evener.Access; got == nil || *got != want {
		t.Fatalf("past access = %+v, want %+v", got, want)
	}

	entry.Meta.Config.Sandbox = ""
	entry.Meta.Config.SandboxNet = nil
	thread, err = pastEntryThread(context.Background(), cfg, entry, false)
	if err != nil {
		t.Fatalf("pastEntryThread: %v", err)
	}
	if got := thread.Evener.Access; got == nil || *got != (appwire.ThreadAccess{Sandbox: "off", Network: true}) {
		t.Fatalf("unsandboxed past access = %+v, want off with the network on", got)
	}
}
```

`appwire-client/typescript/reducer.test.ts`:

```diff
diff --git a/appwire-client/typescript/reducer.test.ts b/appwire-client/typescript/reducer.test.ts
--- a/appwire-client/typescript/reducer.test.ts
+++ b/appwire-client/typescript/reducer.test.ts
@@ -5465,6 +5465,23 @@ test("hydrateThread carries visionModel and defaults an absent wire value", () =
   ).toBe("anthropic/claude-haiku-4-5");
 });
 
+// S15: the session's sandbox mode and network setting ride the read, for the
+// Session sheet's Access section. Absent (an older daemon or hub) stays
+// absent: the phone leaves the section out rather than guess.
+test("hydrateThread carries the session's access and leaves an absent one out", () => {
+  expect(testHydrate().access).toBeUndefined();
+  expect(
+    testHydrate({
+      evener: {
+        ref: "ref_t",
+        capabilities: CAPABILITIES,
+        queue: { revision: 0 },
+        access: { sandbox: "workspace-write", network: false },
+      },
+    }).access,
+  ).toEqual({ sandbox: "workspace-write", network: false });
+});
+
 test("thread/vision-model/changed updates visionModel", () => {
   let model = testHydrate();
   expect(model.visionModel).toBe("");
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub -run TestPastThreadReadCarriesThePersistedAccess -count=1`
Expected: FAIL: `past access = <nil>`.

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/reducer.test.ts -t "access"`
Expected: FAIL: the hydrated model has no `access`.

- [ ] **Step 3: Implement**

`cmd/evener-hub/app_threadread.go`:

```diff
diff --git a/cmd/evener-hub/app_threadread.go b/cmd/evener-hub/app_threadread.go
--- a/cmd/evener-hub/app_threadread.go
+++ b/cmd/evener-hub/app_threadread.go
@@ -947,6 +947,7 @@ func pastEntryThreadForList(ctx context.Context, cfg hubcore.WebConfig, entry hu
 		thread = applyHubForkCapability(cfg, thread)
 	}
 	thread.Evener.VisionModel = entry.Meta.VisionModel
+	thread.Evener.Access = appwire.SessionAccess(entry.Meta.Config.Sandbox, entry.Meta.Config.SandboxNet)
 	return thread, nil
 }
 
```

`appwire-client/typescript/model.ts`:

```diff
diff --git a/appwire-client/typescript/model.ts b/appwire-client/typescript/model.ts
--- a/appwire-client/typescript/model.ts
+++ b/appwire-client/typescript/model.ts
@@ -15,6 +15,7 @@ import type {
   SandboxEscalationRequested,
   SessionURL,
   TaskAggregate,
+  ThreadAccess,
   ThreadCapabilities,
   ThreadItemPosition,
   ThreadStatus,
@@ -451,6 +452,11 @@ export interface ThreadModel {
   // nothing: absent is unknown, and zero is not news.
   // Snapshot-only like usage/cost/workMillis; no live push.
   failedToolCalls?: number;
+  // The session's sandbox mode and network setting (wire EvenerThread.access,
+  // S15), for the Session sheet's Access section. Undefined when the daemon or
+  // hub predates it: not known, so the section is left out. Snapshot-only: a
+  // session's sandbox is fixed when it starts.
+  access?: ThreadAccess;
   workMillis: number;
   // activeTurnStartedAt is undefined when no turn is active (an ISO string,
   // like every other timestamp on this model, converted from the wire's
```

`appwire-client/typescript/reducer.ts`:

```diff
diff --git a/appwire-client/typescript/reducer.ts b/appwire-client/typescript/reducer.ts
--- a/appwire-client/typescript/reducer.ts
+++ b/appwire-client/typescript/reducer.ts
@@ -1220,6 +1220,7 @@ function threadFields(resp: ThreadReadResponse, ref: string, now: number): Omit<
     // Passed straight through, undefined and all: absent is "nobody counted"
     // and must not become a 0 that reads as "nothing failed".
     failedToolCalls: thread.evener.failedToolCalls,
+    ...(thread.evener.access ? { access: { ...thread.evener.access } } : {}),
     workMillis: thread.evener.workMillis ?? 0,
     activeTurnStartedAt: epochMsToISO(thread.evener.activeTurnStartedAt),
     lastTurnEndedAt: epochMsToISO(thread.evener.lastTurnEndedAt),
```

- [ ] **Step 4: Run them to verify they pass**

Run:
```bash
go test ./cmd/evener-hub -run TestPastThreadRead -count=1
cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/ && npm run typecheck && npx biome check ../../../appwire-client/typescript/model.ts ../../../appwire-client/typescript/reducer.ts ../../../appwire-client/typescript/reducer.test.ts
```
Expected: PASS.

- [ ] **Step 5: Gates and commit**

```bash
$(go env GOROOT)/bin/gofmt -l cmd/evener-hub
go vet ./cmd/evener-hub && golangci-lint run ./cmd/evener-hub/
go test ./cmd/evener-hub/... ./cmd/evener-tui/... -count=1
git add cmd/evener-hub/app_threadread.go cmd/evener-hub/app_threadread_access_test.go appwire-client/typescript/model.ts appwire-client/typescript/reducer.ts appwire-client/typescript/reducer.test.ts
git commit -m "feat(hub): past sessions carry their access, and the shared model keeps it (S15)"
```

**PR.** Title "feat: a session's sandbox mode and network on the thread read (S15, phase 7 PR 32)". The body names the three producers and the one mapping (ruling 9), that `off` is stated rather than omitted (ruling 10), the read-only-scope known limit, and that no projector change means no TUI case.

---

## PR 33: queued delivery and approval decisions in the transcript, S16 (Tasks 33.1-33.2)

**Branch:** `git fetch origin && git switch -c claude/s16-transcript-history origin/main`

**Before merging:** question 1 (the transcript's one-way door) needs Jesse's answer. The PR can be built and reviewed before it.

**What it adds.** "Queued" on a delivered message, and approval history, both in the transcript so a reload shows them.

### Task 33.1: A message that waited in the queue says so

**Implementer:** Sonnet.

**Files:**
- Modify: `agent/schema/turn.go` (`Turn.Queued` after `OwningTurnID`, keeping the alphabetical key order)
- Modify: `agent/session_lifecycle.go` (the client-mutation branch of the `USER_INPUT` append, right after `pending` is read)
- Create: `agent/session_queued_delivery_test.go`
- Modify: `appwire/types.go` (`ThreadItem.Queued` after `ClientMutationID`)
- Modify: `internal/apptranscript/apptranscript.go` (`projectTurn`'s `TurnUserInput` case)
- Create: `internal/apptranscript/queued_delivery_test.go`
- Modify: `appwire-client/typescript/model.ts` (`ItemModel.queued` after `steeringKind`), `appwire-client/typescript/reducer.ts` (`wireItemToModel`, `mergePageItem`, `itemNullishMergedFields`, `ITEM_OMISSION_TOLERANT_FIELDS`), `appwire-client/typescript/reducer.test.ts`, `appwire-client/typescript/__snapshots__/reducer.test.ts.snap`
- Create: `cmd/evener-tui/internal/transcript/queued_user_message_test.go`

**Interfaces:**
- Produces: `schema.Turn.Queued bool` (`json:"queued,omitempty"`), `appwire.ThreadItem.Queued bool` (`json:"queued,omitempty"`), `ItemModel.queued?: boolean`.

- [ ] **Step 1: Write the failing tests**

`agent/session_queued_delivery_test.go`:

```go
package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// A message that waited in the queue says so on its USER_INPUT entry, so a
// reader can mark it "Queued" once it is delivered (S16); a message sent with
// turn/start does not.
func TestQueuedDeliveryIsRecordedOnTheUserEntry(t *testing.T) {
	s := newIdentitySession(t)
	ctx := context.Background()
	if _, err := s.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "sent",
		Input:            []appwire.InputItem{{Type: "text", Text: "sent at once"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	if _, ran, err := s.ProcessClientMutationStart(ctx, nil); err != nil || !ran {
		t.Fatalf("ProcessClientMutationStart: ran=%v err=%v", ran, err)
	}
	if _, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: "waited",
		Input:            []appwire.InputItem{{Type: "text", Text: "sent while it worked"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationQueue: %v", err)
	}
	if _, ran, err := s.ProcessPendingUserInput(ctx, nil); err != nil || !ran {
		t.Fatalf("ProcessPendingUserInput: ran=%v err=%v", ran, err)
	}

	queued := map[string]bool{}
	for _, entry := range transcriptEntries(t, s) {
		if entry.Turn.Kind == schema.TurnUserInput {
			queued[entry.Turn.ClientMutationID] = entry.Turn.Queued
		}
	}
	if len(queued) != 2 || queued["sent"] || !queued["waited"] {
		t.Fatalf("USER_INPUT queued marks = %v, want sent=false waited=true", queued)
	}
}
```

`internal/apptranscript/queued_delivery_test.go`:

```go
package apptranscript

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// A USER_INPUT entry that waited in the queue projects a userMessage marked
// queued, in the legacy and the identity formats alike, so live history and a
// reload say "Queued" on the same message (S16).
func TestQueuedUserInputProjectsAQueuedUserMessage(t *testing.T) {
	for _, format := range []int{0, schema.TurnFormatIdentity} {
		for _, queued := range []bool{false, true} {
			turn := schema.Turn{Kind: schema.TurnUserInput, Message: llm.User("run the tests"), Format: format, Queued: queued}
			items, _ := ProjectEntryParts("turn_1", 3, turn, nil, nil, nil)
			if len(items) != 1 || items[0].Type != "userMessage" {
				t.Fatalf("format %d projected %+v, want one userMessage", format, items)
			}
			if items[0].Queued != queued {
				t.Fatalf("format %d queued entry %v projected queued=%v", format, queued, items[0].Queued)
			}
		}
	}
}
```

`appwire-client/typescript/reducer.test.ts`:

```diff
diff --git a/appwire-client/typescript/reducer.test.ts b/appwire-client/typescript/reducer.test.ts
--- a/appwire-client/typescript/reducer.test.ts
+++ b/appwire-client/typescript/reducer.test.ts
@@ -5329,6 +5329,27 @@ test("a reloaded steering item carries steeringKind from the snapshot", () => {
   expect(itemAt(turnAt(model, 0), 0).steeringKind).toBe("tasks-done");
 });
 
+// S16: a message that waited in the queue says so on its userMessage item,
+// and the model keeps the mark so the phone can caption it "Queued".
+test("a queued user message carries queued from the snapshot", () => {
+  const thread = testThread({
+    turns: [
+      {
+        id: "turn_0",
+        status: "completed",
+        itemsView: "full",
+        items: [
+          { id: "item_user_0", type: "userMessage", text: "sent now", status: "completed" },
+          { id: "item_user_1", type: "userMessage", text: "sent while it worked", queued: true, status: "completed" },
+        ],
+      },
+    ],
+  });
+  const model = hydrateThread({ thread }, thread.evener.ref, 1000);
+  expect(itemAt(turnAt(model, 0), 0).queued).toBeUndefined();
+  expect(itemAt(turnAt(model, 0), 1).queued).toBe(true);
+});
+
 // On reload, apptranscript.TurnsFromFile mints one wire turn per transcript
 // entry, so a tool CALL (assistant entry) and its RESULT (tool-results entry)
 // arrive as two items sharing a callId, with different ids, in separate turns.
```

The TUI case pins behavior that must not change: the TUI shows a queued message as the user's message, once. It passes before and after this task; it is here so a later change to how the TUI treats `queued` has to break it.

`cmd/evener-tui/internal/transcript/queued_user_message_test.go`:

```go
package transcript

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// A delivered queued message now carries queued=true (S16). The TUI shows it
// as the user's message, once, exactly as before: it reconciles with the
// composer's echo and keeps its transcript position.
func TestQueuedUserMessageRendersAsTheUsersMessage(t *testing.T) {
	queued := appwire.ThreadItem{
		Type:                 "userMessage",
		ID:                   "item_user_3",
		TurnID:               "turn_2",
		TranscriptEntryIndex: 3,
		Text:                 "sent while it worked",
		Queued:               true,
	}
	reducer := NewTranscriptReducer(nil, nil, nil)
	reducer.ApplyUserMessageEcho("sent while it worked")
	reducer.ApplyThreadItem(queued, TurnIndexFromID(queued.TurnID), true)
	assertUserEntry(t, reducer.Messages(), 3, 2)
	if got := reducer.Messages()[0].Text; got != "sent while it worked" {
		t.Fatalf("queued message text = %q", got)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./agent -run TestQueuedDeliveryIsRecordedOnTheUserEntry -count=1`
Expected: FAIL to compile (`entry.Turn.Queued undefined`). With the schema field alone: FAIL, `USER_INPUT queued marks = map[sent:false waited:false], want sent=false waited=true`.

Run: `go test ./internal/apptranscript -run TestQueuedUserInputProjectsAQueuedUserMessage -count=1`
Expected: FAIL to compile (`items[0].Queued undefined`).

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/reducer.test.ts -t "queued from the snapshot"`
Expected: FAIL: `expected undefined to be true`.

- [ ] **Step 3: Implement**

`agent/schema/turn.go`:

```diff
diff --git a/agent/schema/turn.go b/agent/schema/turn.go
--- a/agent/schema/turn.go
+++ b/agent/schema/turn.go
@@ -407,7 +407,13 @@ type Turn struct {
 	OriginalOrdinal *uint64 `json:"original_ordinal,omitempty"`
 	// OwningTurnID identifies the logical turn that owns an ordinary steering
 	// entry. It differs from StableTurnID, which identifies the client mutation.
-	OwningTurnID           string `json:"owning_turn_id,omitempty"`
+	OwningTurnID string `json:"owning_turn_id,omitempty"`
+	// Queued marks a USER_INPUT entry whose message waited in the session's
+	// queue (turn/queue) and was delivered when a turn ended, so a reader can
+	// say "Queued" on it (S16). False for a message sent with turn/start and
+	// on every other kind; a queued message steered in instead is a STEERING
+	// entry and says so there.
+	Queued                 bool   `json:"queued,omitempty"`
 	ResponseContextMarker  string `json:"response_context_marker,omitempty"`
 	ResponseEndpoint       string `json:"response_endpoint,omitempty"`
 	ResponseEndpointFamily string `json:"response_endpoint_family,omitempty"`
```

`gofmt` realigns `OwningTurnID`'s line because the new comment splits the alignment block; that is the formatter's change, not a hand edit.

`agent/session_lifecycle.go`:

```diff
diff --git a/agent/session_lifecycle.go b/agent/session_lifecycle.go
--- a/agent/session_lifecycle.go
+++ b/agent/session_lifecycle.go
@@ -2736,6 +2736,9 @@ func (s *Session) acceptUserInputWithSkillSelection(ctx context.Context, input s
 			turn.SkillState = &schema.SkillTurnState{Input: skillInput}
 		}
 		pending := s.clientMutations.snapshot().PendingExecutions[queuedIdentity.ClientMutationID]
+		// A turn/queue message waited for a turn to end; the entry says so
+		// (S16). A queued message steered in is a STEERING entry instead.
+		turn.Queued = pending.Method == clientMutationMethodQueue
 		if pending.Method == clientMutationMethodStart && s.clientMutationPreAppendFailure != nil {
 			if failure := s.clientMutationPreAppendFailure(turn); failure != nil {
 				if err := s.beginClientMutationFailure(queuedIdentity.ClientMutationID, failure); err != nil {
```

`appwire/types.go`:

```diff
diff --git a/appwire/types.go b/appwire/types.go
--- a/appwire/types.go
+++ b/appwire/types.go
@@ -1782,6 +1782,12 @@ type ThreadItem struct {
 	// non-steering items and on steering items the daemon didn't classify.
 	SteeringKind     string `json:"steeringKind,omitempty"`
 	ClientMutationID string `json:"clientMutationId,omitempty"`
+	// Queued is true on a userMessage the user sent while the agent worked:
+	// it waited in the session's queue and was delivered when that turn
+	// ended (S16), so a client captions it "Queued". Absent on every other
+	// item, on a message sent to an idle session, and from an older daemon.
+	// A queued message steered in arrives as a steering item instead.
+	Queued bool `json:"queued,omitempty"`
 	// Version is the highest entry ordinal (below) among the entries that
 	// contributed to this item, stored and sent as ordinal + 1 so 0 means "no
 	// history version" (an overlay item). The higher version wins.
```

`internal/apptranscript/apptranscript.go`:

```diff
diff --git a/internal/apptranscript/apptranscript.go b/internal/apptranscript/apptranscript.go
--- a/internal/apptranscript/apptranscript.go
+++ b/internal/apptranscript/apptranscript.go
@@ -610,6 +610,7 @@ func projectTurn(turnID string, turnIndex int, turn schema.Turn, reg *ToolCallRe
 			Images:               images,
 			Status:               appwire.TurnStatusCompleted,
 			ClientMutationID:     turn.ClientMutationID,
+			Queued:               turn.Queued,
 		}}
 	case schema.TurnSteering:
 		if turn.GoalContinuation != nil {
```

`appwire-client/typescript/model.ts`:

```diff
diff --git a/appwire-client/typescript/model.ts b/appwire-client/typescript/model.ts
--- a/appwire-client/typescript/model.ts
+++ b/appwire-client/typescript/model.ts
@@ -82,6 +82,11 @@ export interface ItemModel {
   // non-steering items, for user-sourced steering, and for a steer projected
   // by a daemon predating the field — in which case the UI shows no kind.
   steeringKind?: string;
+  // Wire ThreadItem.queued (S16): true on a userMessage that waited in the
+  // session's queue and was delivered when a turn ended, so the transcript
+  // captions it "Queued". Undefined on every other item and from a daemon
+  // that predates the field.
+  queued?: boolean;
   // Structured detail behind a system item's prose text (wire ThreadItem.raw),
   // e.g. `{roundTimings: {...}}` for a round_timings item - lets a renderer
   // read real numbers instead of re-parsing the human-readable text.
```

`appwire-client/typescript/reducer.ts`:

```diff
diff --git a/appwire-client/typescript/reducer.ts b/appwire-client/typescript/reducer.ts
--- a/appwire-client/typescript/reducer.ts
+++ b/appwire-client/typescript/reducer.ts
@@ -329,6 +329,7 @@ function wireItemToModel(item: ThreadItem, imageSessionRoute?: string): ItemMode
     status: item.status,
     source: item.source,
     steeringKind: item.steeringKind,
+    queued: item.queued,
     startedAt: epochMsToISO(item.startedAt),
     completedAt: epochMsToISO(item.completedAt),
   };
@@ -816,6 +817,7 @@ function mergePageItem(older: ItemModel, newer: ItemModel): ItemModel {
       description: newer.description ?? older.description,
       eventKind: newer.eventKind ?? older.eventKind,
       steeringKind: newer.steeringKind ?? older.steeringKind,
+      queued: newer.queued ?? older.queued,
       raw: newer.raw ?? older.raw,
       output: newer.output ?? older.output,
       error: newer.error ?? older.error,
@@ -1541,6 +1543,7 @@ const itemNullishMergedFields = new Set([
   "description",
   "eventKind",
   "steeringKind",
+  "queued",
   "raw",
   "output",
   "error",
@@ -2195,6 +2198,7 @@ const ITEM_OMISSION_TOLERANT_FIELDS = [
   "callId",
   "eventKind",
   "steeringKind",
+  "queued",
   "source",
 ] as const;
 
```

`queued` follows `steeringKind` through every merge list, so a later page or a version-superseding replacement that omits it keeps the mark.

Then refresh the reducer's four fixture snapshots, which now list the `queued` key on every item (16 lines, all `"queued": undefined`):

```bash
cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/reducer.test.ts -u && cd -
git diff --stat appwire-client/typescript/__snapshots__/
```

- [ ] **Step 4: Run them to verify they pass**

Run:
```bash
go test ./agent -run 'TestQueuedDeliveryIsRecordedOnTheUserEntry|TestClientMutation_Queue' -count=1
go test ./internal/apptranscript ./cmd/evener-tui/... -count=1
make generate && go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1
cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/ && npm run typecheck && cd -
```
Expected: PASS.

- [ ] **Step 5: Gates and commit**

```bash
make test-native   # ItemModel gains a key; the phone's own suites must still pass
cd cmd/evener-hub/frontend && npx vitest run --exclude "scripts/*.test.mjs" && npx biome check ../../../appwire-client/typescript/model.ts ../../../appwire-client/typescript/reducer.ts ../../../appwire-client/typescript/reducer.test.ts && cd -
$(go env GOROOT)/bin/gofmt -l agent internal appwire cmd/evener-tui
(cd agent && go vet ./... && go vet -tags evenerfuzz ./... && GOOS=windows go vet -tags evenerfuzz ./... && golangci-lint run ./ ./schema/)
go vet ./appwire ./internal/apptranscript ./cmd/evener-tui/... && golangci-lint run ./appwire/ ./internal/apptranscript/ ./cmd/evener-tui/internal/transcript/
git add agent/schema/turn.go agent/session_lifecycle.go agent/session_queued_delivery_test.go appwire/types.go internal/apptranscript appwire-client/typescript cmd/evener-tui/internal/transcript/queued_user_message_test.go
git commit -m "feat(agent): a delivered queued message says it waited (S16)"
```

### Task 33.2: A human's Allow or Deny is history

**Implementer:** Sonnet.

**Files:**
- Modify: `agent/schema/turn.go` (`NoticeApprovalDecision`; `NoticeInfo.ApprovalDecision`; `Validate`'s map; `ApprovalDecisionNotice` before `SkillActivatedNotice`)
- Modify: `agent/schema/turn_identity_test.go` (`TestNoticeInfoValidate`)
- Modify: `agent/session_escalation.go` (`escalateOnSandboxDenial`'s wait; `cancelAllEscalations`)
- Create: `agent/session_escalation_record_test.go`
- Modify: `appwire/types.go` (`ThreadItemEventKindApprovalDecision` and its `AllThreadItemEventKinds` entry)
- Modify: `internal/apptranscript/notice.go` (`noticeAnnouncement`; `ApprovalDecisionAnnouncement`)
- Create: `internal/apptranscript/approval_decision_test.go`, `cmd/evener-tui/internal/transcript/approval_decision_test.go`

**Interfaces:**
- Produces: `schema.NoticeApprovalDecision = "approval_decision"`, `schema.ApprovalDecisionNotice{EscalationID, Approved, Tool, Kind, DeniedPath}` (snake_case JSON), `appwire.ThreadItemEventKindApprovalDecision = "approval_decision"`, `apptranscript.ApprovalDecisionAnnouncement`. On the wire: a `systemMessage` with `description: "Approval"`, the text, and `raw: {"approvalDecision": {escalationId, approved, tool, kind, deniedPath}}`.

- [ ] **Step 1: Write the failing tests**

`agent/session_escalation_record_test.go`:

```go
package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
)

// recordingEscalationSession is escalatableSession with a transcript, so the
// approval history NOTICE has somewhere to land.
func recordingEscalationSession(t *testing.T) *Session {
	t.Helper()
	s := newSession(t, withConfig(SessionConfig{
		StateDir:         t.TempDir(),
		NoProjectPrompts: true,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	s.SetSubscriberCountFunc(func() int { return 1 })
	return s
}

// approvalDecisions lists the approval NOTICE entries the transcript holds.
func approvalDecisions(t *testing.T, s *Session) []schema.ApprovalDecisionNotice {
	t.Helper()
	var decisions []schema.ApprovalDecisionNotice
	for _, entry := range transcriptEntries(t, s) {
		if notice := entry.Turn.Notice; entry.Turn.Kind == schema.TurnNotice && notice != nil && notice.Kind == schema.NoticeApprovalDecision {
			if notice.ApprovalDecision == nil {
				t.Fatalf("approval NOTICE without its payload: %+v", notice)
			}
			decisions = append(decisions, *notice.ApprovalDecision)
		}
	}
	return decisions
}

// A human's Allow or Deny is history (S16): the transcript records the
// decision, naming the tool, the card's kind and the path, so approval
// history reads the same after a reload as it did live.
func TestEscalation_RecordsTheHumansDecision(t *testing.T) {
	for _, approve := range []bool{true, false} {
		s := recordingEscalationSession(t)
		res, denied := deniedResult("/etc/hosts")
		done := make(chan tool.ExecResult, 1)
		go func() {
			done <- s.escalateOnSandboxDenial(context.Background(), "write_file", res, func(context.Context) tool.ExecResult { return succeededResult() })
		}()
		ids := awaitPending(t, s, 1)
		if err := s.ResolveSandboxEscalation(ids[0], approve); err != nil {
			t.Fatalf("resolve: %v", err)
		}
		<-done
		want := schema.ApprovalDecisionNotice{EscalationID: ids[0], Approved: approve, Tool: denied.Tool, Kind: "file_tool", DeniedPath: denied.Path}
		if got := approvalDecisions(t, s); len(got) != 1 || got[0] != want {
			t.Fatalf("approve=%v recorded %+v, want exactly %+v", approve, got, want)
		}
	}
}

// Stopping the turn or closing the session ends the wait without a human
// decision, so nothing is recorded as allowed or denied.
func TestEscalation_RecordsNoDecisionWithoutAHuman(t *testing.T) {
	t.Run("turn interrupted", func(t *testing.T) {
		s := recordingEscalationSession(t)
		res, _ := deniedResult("/etc/hosts")
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan tool.ExecResult, 1)
		go func() { done <- s.escalateOnSandboxDenial(ctx, "write_file", res, noRerun(t)) }()
		awaitPending(t, s, 1)
		cancel()
		<-done
		if got := approvalDecisions(t, s); len(got) != 0 {
			t.Fatalf("an interrupted escalation recorded %+v", got)
		}
	})
	t.Run("session closed", func(t *testing.T) {
		s := recordingEscalationSession(t)
		res, _ := deniedResult("/etc/hosts")
		done := make(chan tool.ExecResult, 1)
		go func() { done <- s.escalateOnSandboxDenial(context.Background(), "write_file", res, noRerun(t)) }()
		awaitPending(t, s, 1)
		s.Close()
		select {
		case got := <-done:
			if !got.IsError {
				t.Fatal("Close must still resolve a blocked escalation to the typed denial")
			}
		case <-time.After(30 * time.Second): // TRIPWIRE: in-process; fires only if Close fails to unblock the escalation.
			t.Fatal("Close did not unblock the escalation")
		}
		if got := approvalDecisions(t, s); len(got) != 0 {
			t.Fatalf("a closed session recorded %+v as a decision", got)
		}
	})
}
```

`agent/schema/turn_identity_test.go`:

```diff
diff --git a/agent/schema/turn_identity_test.go b/agent/schema/turn_identity_test.go
--- a/agent/schema/turn_identity_test.go
+++ b/agent/schema/turn_identity_test.go
@@ -83,6 +83,7 @@ func TestNoticeInfoValidate(t *testing.T) {
 		{Kind: NoticeGoalEnded, GoalEnded: &GoalEndedNotice{Status: "complete"}},
 		{Kind: NoticeTurnLimit, TurnLimit: &TurnLimitNotice{MaxTurns: 4}},
 		{Kind: NoticeSkillActivated, SkillActivated: &SkillActivatedNotice{Name: "tdd"}},
+		{Kind: NoticeApprovalDecision, ApprovalDecision: &ApprovalDecisionNotice{EscalationID: "esc_1", Approved: true}},
 	}
 	for _, notice := range valid {
 		if err := notice.Validate(); err != nil {
@@ -96,6 +97,8 @@ func TestNoticeInfoValidate(t *testing.T) {
 		{"zero payloads", NoticeInfo{Kind: NoticeGoalEnded}},
 		{"two payloads", NoticeInfo{Kind: NoticeGoalEnded, GoalEnded: &GoalEndedNotice{Status: "complete"}, TurnLimit: &TurnLimitNotice{MaxTurns: 4}}},
 		{"mismatched kind", NoticeInfo{Kind: NoticeGoalEnded, TurnLimit: &TurnLimitNotice{MaxTurns: 4}}},
+		{"approval without its payload", NoticeInfo{Kind: NoticeApprovalDecision}},
+		{"approval payload under another kind", NoticeInfo{Kind: NoticeGoalEnded, ApprovalDecision: &ApprovalDecisionNotice{EscalationID: "esc_1"}}},
 	}
 	for _, tc := range invalid {
 		if err := tc.notice.Validate(); err == nil {
```

`internal/apptranscript/approval_decision_test.go`:

```go
package apptranscript

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// An approval NOTICE projects as a systemMessage of its own kind, whose text
// says what was decided and whose raw detail carries the decision, so a client
// draws "Allowed: …" or "Denied: …" without parsing prose (S16).
func TestApprovalDecisionNoticeProjectsItsDecision(t *testing.T) {
	for _, tc := range []struct {
		approved bool
		text     string
	}{
		{true, "Allowed write_file to access /Users/j/sites/docs"},
		{false, "Denied write_file access to /Users/j/sites/docs"},
	} {
		entry := schema.Turn{Kind: schema.TurnNotice, Format: schema.TurnFormatIdentity, Notice: &schema.NoticeInfo{
			Kind: schema.NoticeApprovalDecision,
			ApprovalDecision: &schema.ApprovalDecisionNotice{
				EscalationID: "esc_1", Approved: tc.approved, Tool: "write_file", Kind: "file_tool", DeniedPath: "/Users/j/sites/docs",
			},
		}}
		items, parts := ProjectEntryParts("turn_4", 9, entry, nil, nil, nil)
		if len(items) != 1 || len(parts) != 1 {
			t.Fatalf("approved=%v projected %d items, want one", tc.approved, len(items))
		}
		item := items[0]
		if item.Type != "systemMessage" || item.EventKind != appwire.ThreadItemEventKindApprovalDecision || item.Text != tc.text || item.Description != "Approval" {
			t.Fatalf("approved=%v item = %+v", tc.approved, item)
		}
		var raw map[string]map[string]any
		if err := json.Unmarshal(item.Raw, &raw); err != nil {
			t.Fatalf("raw %s: %v", item.Raw, err)
		}
		got := raw["approvalDecision"]
		if got["escalationId"] != "esc_1" || got["approved"] != tc.approved || got["tool"] != "write_file" || got["kind"] != "file_tool" || got["deniedPath"] != "/Users/j/sites/docs" {
			t.Fatalf("approved=%v raw = %s", tc.approved, item.Raw)
		}
	}
}

// A notice whose kind says approval but carries no payload shows nothing,
// like every other mismatched notice.
func TestApprovalDecisionNoticeWithoutPayloadShowsNothing(t *testing.T) {
	entry := schema.Turn{Kind: schema.TurnNotice, Format: schema.TurnFormatIdentity, Notice: &schema.NoticeInfo{Kind: schema.NoticeApprovalDecision}}
	if items, _ := ProjectEntryParts("turn_4", 9, entry, nil, nil, nil); len(items) != 0 {
		t.Fatalf("payload-less approval notice projected %+v", items)
	}
}
```

The TUI case, like Task 33.1's, pins behavior that must not change: the TUI shows the new kind as it shows every system message, its description over its text.

`cmd/evener-tui/internal/transcript/approval_decision_test.go`:

```go
package transcript

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// A human's Allow or Deny now leaves a systemMessage of its own kind in
// history (S16). The TUI shows it as it shows every system message: its
// description over its text.
func TestApprovalDecisionRendersAsASystemLine(t *testing.T) {
	r := NewTranscriptReducer(nil, map[string]int{}, map[string]int{})
	r.ApplyThreadItem(appwire.ThreadItem{
		Type:        "systemMessage",
		ID:          "item_approval_decision_9",
		EventKind:   appwire.ThreadItemEventKindApprovalDecision,
		Description: "Approval",
		Text:        "Allowed write_file to access /Users/j/sites/docs",
		Raw:         []byte(`{"approvalDecision":{"escalationId":"esc_1","approved":true,"tool":"write_file","kind":"file_tool","deniedPath":"/Users/j/sites/docs"}}`),
	}, 4, true)
	messages := r.Messages()
	if len(messages) != 1 || messages[0].Kind != MsgSystem {
		t.Fatalf("messages = %+v, want one system line", messages)
	}
	if messages[0].Text != "Approval\nAllowed write_file to access /Users/j/sites/docs" {
		t.Fatalf("system line = %q", messages[0].Text)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./agent -run TestEscalation_Records -count=1`
Expected: FAIL to compile (`undefined: schema.ApprovalDecisionNotice`). With the schema alone: FAIL, `approve=true recorded [], want exactly {...}`. With the escalation change but not the `Validate` line: the same failure, and the session emits the warning `invalid notice not recorded: notice kind "approval_decision" has no payload`.

Run: `(cd agent && go test ./schema -run TestNoticeInfoValidate -count=1)`
Expected: FAIL to compile, then FAIL on the valid approval notice until `Validate` names it.

Run: `go test ./internal/apptranscript -run TestApprovalDecision -count=1`
Expected: FAIL to compile (`undefined: appwire.ThreadItemEventKindApprovalDecision`).

- [ ] **Step 3: Implement**

`agent/schema/turn.go`:

```diff
diff --git a/agent/schema/turn.go b/agent/schema/turn.go
--- a/agent/schema/turn.go
+++ b/agent/schema/turn.go
@@ -157,6 +157,10 @@ const (
 	NoticeGoalEnded      NoticeKind = "goal_ended"
 	NoticeTurnLimit      NoticeKind = "turn_limit"
 	NoticeSkillActivated NoticeKind = "skill_activated"
+	// NoticeApprovalDecision records a human's Allow or Deny on a sandbox
+	// escalation (S16). It has no live notice of its own: the escalation's
+	// card is the live form, and this is its history.
+	NoticeApprovalDecision NoticeKind = "approval_decision"
 )
 
 // NoticeInfo is a persisted presentational notice: its kind and exactly one
@@ -168,6 +172,8 @@ type NoticeInfo struct {
 	GoalEnded      *GoalEndedNotice      `json:"goal_ended,omitempty"`
 	TurnLimit      *TurnLimitNotice      `json:"turn_limit,omitempty"`
 	SkillActivated *SkillActivatedNotice `json:"skill_activated,omitempty"`
+	// ApprovalDecision rides a NoticeApprovalDecision notice.
+	ApprovalDecision *ApprovalDecisionNotice `json:"approval_decision,omitempty"`
 }
 
 // Validate enforces NoticeInfo's own doc contract: exactly one of the payload
@@ -179,10 +185,11 @@ func (n NoticeInfo) Validate() error {
 	matched := false
 	count := 0
 	for kind, present := range map[NoticeKind]bool{
-		NoticeToolRepair:     n.ToolRepair != nil,
-		NoticeGoalEnded:      n.GoalEnded != nil,
-		NoticeTurnLimit:      n.TurnLimit != nil,
-		NoticeSkillActivated: n.SkillActivated != nil,
+		NoticeToolRepair:       n.ToolRepair != nil,
+		NoticeGoalEnded:        n.GoalEnded != nil,
+		NoticeTurnLimit:        n.TurnLimit != nil,
+		NoticeSkillActivated:   n.SkillActivated != nil,
+		NoticeApprovalDecision: n.ApprovalDecision != nil,
 	} {
 		if !present {
 			continue
@@ -223,6 +230,18 @@ type TurnLimitNotice struct {
 	MaxToolRoundsPerInput int `json:"max_tool_rounds_per_input,omitempty"`
 }
 
+// ApprovalDecisionNotice is a human's decision on one sandbox escalation: the
+// escalation it answered, whether it was allowed, and what the card asked
+// about (the denied tool, the card's kind and the path). It carries what the
+// card showed and nothing else, never file contents.
+type ApprovalDecisionNotice struct {
+	EscalationID string `json:"escalation_id"`
+	Approved     bool   `json:"approved"`
+	Tool         string `json:"tool"`
+	Kind         string `json:"kind"`
+	DeniedPath   string `json:"denied_path"`
+}
+
 // SkillActivatedNotice mirrors events.SkillActivatedData.
 type SkillActivatedNotice struct {
 	Name string `json:"name"`
```

`gofmt` realigns `Validate`'s map for the longer key.

`agent/session_escalation.go`:

```diff
diff --git a/agent/session_escalation.go b/agent/session_escalation.go
--- a/agent/session_escalation.go
+++ b/agent/session_escalation.go
@@ -12,6 +12,7 @@ import (
 	"primeradiant.com/evener/agent/execenv"
 	"primeradiant.com/evener/agent/internal/tool"
 	"primeradiant.com/evener/agent/sandbox"
+	"primeradiant.com/evener/agent/schema"
 )
 
 // sandboxGranter is the execution environment's ability to produce a short-lived
@@ -211,7 +212,21 @@ func (s *Session) escalateOnSandboxDenial(ctx context.Context, callName string,
 	s.emit(events.EventSandboxEscalationRequested, data)
 
 	select {
-	case d := <-ch:
+	case d, decided := <-ch:
+		if !decided {
+			// cancelAllEscalations closed the channel: the session is closing,
+			// and no human answered.
+			return res
+		}
+		// A human answered; the answer is history (S16). It is recorded
+		// before the re-run, so it precedes the call's result.
+		s.recordNotice(schema.NoticeInfo{Kind: schema.NoticeApprovalDecision, ApprovalDecision: &schema.ApprovalDecisionNotice{
+			EscalationID: id,
+			Approved:     d.Approve,
+			Tool:         data.Tool,
+			Kind:         data.Kind,
+			DeniedPath:   data.DeniedPath,
+		}})
 		if d.Approve {
 			return rerun(withInvocationGrant(ctx, denied.Path))
 		}
@@ -273,7 +288,7 @@ func (s *Session) PendingEscalations() []events.SandboxEscalationRequestedData {
 	return out
 }
 
-// cancelAllEscalations denies every pending escalation. Called from Close so a
+// cancelAllEscalations withdraws every pending escalation. Called from Close so a
 // blocked tool-exec goroutine unblocks (returning the typed denial) rather than
 // leaking. Turn-interrupt cancellation is handled by the ctx.Done() arm of the
 // select; this covers teardown, where the ctx may outlive the decision to stop.
@@ -282,8 +297,11 @@ func (s *Session) cancelAllEscalations() {
 	pending := s.pendingEscalations
 	s.pendingEscalations = nil
 	s.mu.Unlock()
+	// Closing, not sending a deny: the waiter tells a human's decision from
+	// the session going away by whether the channel delivered one, and only a
+	// human's decision is recorded.
 	for _, w := range pending {
-		w.ch <- sandbox.EscalationDecision{Approve: false}
+		close(w.ch)
 	}
 }
 
```

`ResolveSandboxEscalation` and `cancelAllEscalations` each remove a waiter from the map under `s.mu` before touching its channel, so exactly one of them reaches any channel: a send and a close never race.

`appwire/types.go`:

```diff
diff --git a/appwire/types.go b/appwire/types.go
--- a/appwire/types.go
+++ b/appwire/types.go
@@ -1688,6 +1688,11 @@ const (
 	// round collapses into when it ended with streamed content or running
 	// tools that were never recorded.
 	ThreadItemEventKindInterrupted ThreadItemEventKind = "interrupted"
+	// ThreadItemEventKindApprovalDecision marks the systemMessage item a
+	// human's Allow or Deny on a sandbox escalation leaves in history (S16).
+	// Its Raw carries {"approvalDecision": {escalationId, approved, tool,
+	// kind, deniedPath}}; a client draws the approval history row from that.
+	ThreadItemEventKindApprovalDecision ThreadItemEventKind = "approval_decision"
 )
 
 // AllThreadItemEventKinds is every ThreadItem.EventKind value emitted for
@@ -1712,6 +1717,7 @@ var AllThreadItemEventKinds = []string{
 	string(ThreadItemEventKindNotesContext),
 	string(ThreadItemEventKindWarning),
 	string(ThreadItemEventKindInterrupted),
+	string(ThreadItemEventKindApprovalDecision),
 }
 
 type ThreadItem struct {
```

`internal/apptranscript/notice.go`:

```diff
diff --git a/internal/apptranscript/notice.go b/internal/apptranscript/notice.go
--- a/internal/apptranscript/notice.go
+++ b/internal/apptranscript/notice.go
@@ -74,6 +74,8 @@ func noticeAnnouncement(notice schema.NoticeInfo) (NoticeAnnouncement, bool) {
 		return TurnLimitAnnouncement(*notice.TurnLimit), true
 	case notice.Kind == schema.NoticeSkillActivated && notice.SkillActivated != nil:
 		return SkillActivatedAnnouncement(*notice.SkillActivated), true
+	case notice.Kind == schema.NoticeApprovalDecision && notice.ApprovalDecision != nil:
+		return ApprovalDecisionAnnouncement(*notice.ApprovalDecision), true
 	default:
 		return NoticeAnnouncement{}, false
 	}
@@ -202,3 +204,24 @@ func TurnLimitAnnouncement(notice schema.TurnLimitNotice) NoticeAnnouncement {
 func SkillActivatedAnnouncement(notice schema.SkillActivatedNotice) NoticeAnnouncement {
 	return NoticeAnnouncement{EventKind: appwire.ThreadItemEventKindSkillActivated, Description: "Skill activated", Text: "Activated skill: " + notice.Name}
 }
+
+// ApprovalDecisionAnnouncement is the history line a human's Allow or Deny on
+// a sandbox escalation leaves (S16), worded like the approval card that asked.
+// Its Raw carries the decision under "approvalDecision", so a client draws the
+// approval history row without parsing the text.
+func ApprovalDecisionAnnouncement(notice schema.ApprovalDecisionNotice) NoticeAnnouncement {
+	text := fmt.Sprintf("Denied %s access to %s", notice.Tool, notice.DeniedPath)
+	if notice.Approved {
+		text = fmt.Sprintf("Allowed %s to access %s", notice.Tool, notice.DeniedPath)
+	}
+	raw, _ := json.Marshal(map[string]any{
+		"approvalDecision": struct {
+			EscalationID string `json:"escalationId"` //nolint:tagliatelle // AppWire Raw payload the clients read (camelCase wire).
+			Approved     bool   `json:"approved"`
+			Tool         string `json:"tool"`
+			Kind         string `json:"kind"`
+			DeniedPath   string `json:"deniedPath"` //nolint:tagliatelle // AppWire Raw payload the clients read (camelCase wire).
+		}(notice),
+	})
+	return NoticeAnnouncement{EventKind: appwire.ThreadItemEventKindApprovalDecision, Description: "Approval", Text: text, Raw: raw}
+}
```

- [ ] **Step 4: Run them to verify they pass**

Run:
```bash
go test ./agent -run 'TestEscalation' -count=1
(cd agent && go test ./schema -count=1)
go test ./internal/apptranscript ./cmd/evener-tui/... ./appwire -count=1
make generate && go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1
cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/ && npm run typecheck && cd -
```
Expected: PASS. The existing escalation tests (`TestEscalation_CloseCancels`, `TestEscalation_CloseEmitsAtMostOneResolvedEvent`) keep passing: a closed channel still returns the typed denial.

- [ ] **Step 5: Gates and commit**

```bash
make test-native
$(go env GOROOT)/bin/gofmt -l agent internal appwire cmd/evener-tui
(cd agent && go vet ./... && go vet -tags evenerfuzz ./... && GOOS=windows go vet -tags evenerfuzz ./... && golangci-lint run ./ ./schema/)
go vet ./appwire ./internal/apptranscript ./cmd/evener-tui/... && golangci-lint run ./appwire/ ./internal/apptranscript/ ./cmd/evener-tui/internal/transcript/
git add agent/schema/turn.go agent/schema/turn_identity_test.go agent/session_escalation.go agent/session_escalation_record_test.go appwire/types.go internal/apptranscript cmd/evener-tui/internal/transcript/approval_decision_test.go appwire-client/typescript/types.gen.ts
git commit -m "feat(agent): a human's Allow or Deny is recorded as history (S16)"
```

**PR.** Title "feat: queued delivery and approval decisions in the transcript (S16, phase 7 PR 33)". The body says both ride the transcript read model's one projection, what "queued" means (ruling 12), that only a human's decision is recorded (ruling 13), the one-way door and question 1, and the two TUI cases.

---

## Self-review

- **Spec coverage.**
  - 8.2: "Queued" on a delivered queued message (Task 33.1; "Steered in mid-turn" unchanged); Approval (history) as "Allowed: …" or "Denied: …" with the mark (Task 33.2 and the handoff); not repeated while the dock is open (the record is written when the dock's decision lands).
  - 8.6: Access, sandbox mode and network, read-only (PR 32 and the handoff).
  - 9: "Stop subagent" with a confirmation and no message, in place of "Ask coordinator to stop it" (PRs 30 and 31 and the handoff); it stops that agent, not its tree (Jesse's answer 4, ruling 1); messaging a running subagent stays tabled (handoff ruling 19), and nothing here adds it.
  - 18: S6, S15 and S16, each with its fallback in the handoff section.
- **Checked by running.**
  - Every task was implemented on a local scratch branch from main at `274afd6ff` (never pushed), and the code blocks above are rendered from its commits. Each PR's commits were also cherry-picked alone onto main to confirm the lanes are independent: `make generate` left no diff, and each PR's tests passed.
  - These passed on the full stack: `go build ./...`; `go vet` plain, with `evenerfuzz` and for Windows on every touched package in both modules; `golangci-lint` 2.13.1 on every touched package; the `server`, `appwire`, `internal/...`, `cmd/evener-hub/...` and `cmd/evener-tui/...` suites; the hubcore fuzz seeds; `TestGeneratedFileCurrent`; the frontend typecheck and its full vitest run; the package's vitest suite; `make test-native`'s four steps; `npm run qualification`; and Biome on the touched package files.
  - `agent` (with `-timeout 45m`) and `cmd/evener`: the full suites ran. Their only failures are tests that compare `/var` with `/private/var` paths on macOS (#2497); the ten `agent` ones were re-run on main at `274afd6ff` and fail there identically. The `internal/selfupdate` failures are the same macOS path class.
  - Each red step named above was run and failed as described.
  - Rebasing the dry run from `18db0e9c3` onto `274afd6ff` caught one thing the plan now carries: #2706 added `NoticeInfo.Validate`, which refuses a notice kind it does not list.
- **Not run.** `make test-web-browser` (nothing here renders), `make lint` as a whole (the pinned linter ran per package), and anything on Linux.
- **Placeholders.** None: every task carries its code or the exact edit.
- **Names.** One spelling across tasks:
  - S6: `MethodEvenerDelegateStop`, `DelegateStopParams`, `DelegateStopOutcome`, `DelegateStopStopping`, `DelegateStopNotRunning`, `DelegateStopResponse`; `StopDelegateRun`, `ErrUnknownDelegate`, `requestUserStop`, `errSubagentNotRunning`, `errSubagentSettling`, `subagentForChild`, `childSessionIDFor`, `delegateUserStopMessage`, `stoppedByUser`; `SetDelegateStopFunc`, `delegateStopFunc`, `handleAppDelegateStop`; `DelegateStopSource`, `StopDelegate`; `ThreadCapabilities.StopSubagent`.
  - S15: `ThreadAccess`, `EvenerThread.Access`, `SessionAccess`, `SandboxOff`; `SessionStartData.Sandbox`, `SandboxNet`; `ThreadModel.access`.
  - S16: `Turn.Queued`, `ThreadItem.Queued`, `ItemModel.queued`; `NoticeApprovalDecision`, `ApprovalDecisionNotice`, `NoticeInfo.ApprovalDecision`, `ThreadItemEventKindApprovalDecision`, `ApprovalDecisionAnnouncement`.
- **Review Focus.** Each line names the tests that pin it, in the task that owns the code.
- **Departures from the server plan's S6 sketch.**
  - No non-cascading durable stop: the stop cancels the run (ruling 1, with the measured reason).
  - No `clientMutationId`: the stop is idempotent (ruling 2).
  - The capability is `stopSubagent`, as sketched, landing with the hub relay in PR 31 rather than beside the daemon method.
