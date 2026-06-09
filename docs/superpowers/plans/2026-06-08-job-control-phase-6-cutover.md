# Job Control — Phase 6: cutover (remove all legacy subagent-control-plane residue)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the legacy subagent control plane (`spawn_agent`/`resume_agent`/`wait`/`close_agent`/`cancel_agent`/`list_agents`/`subagent_output`, keyed on an in-memory `agent_id`) and every cross-package projection it feeds, with **no residue**. The job tools from Phases 2–5 (`delegate`, `job_send_message`, `job_read_output`, `job_list`, `job_stop`, `job_watch`, plus a job-capable `shell`) are the sole control surface afterward. At the end, the §13 acceptance gate returns nothing outside historical changelogs and the spec, and `make build && make test && make lint` is green across all modules.

**Architecture:** This phase is almost entirely **deletions and re-points of existing code**, not new TDD modules. The replacement tools and their runtime already exist (Phases 2–5); here we delete the legacy tool definitions, registrations, profile capability, root-only gating set, per-tool-name behavior tables, UI renderers (Go + JS), the events/wire/snapshot projection chain, the `<subagent-notification>` formatter, and the docs — repointing each live consumer to the job-control equivalent. The child-session **runtime** (`spawnAgent`/`sendInput`/`cancelAgent`/`getSub`/`subagent.run`/the `subagent` struct/`SubagentStatus`) is **kept** — Phase 3 salvaged it as the delegate runtime, and §16 permits those internal-only names to remain. Only the **tool-facing entry points** whose tools are gone (`waitAgent`/`closeAgent`/`listAgents`/`subagent_output`) and the model-/UI-facing surfaces are removed.

**Tech Stack:** Go (module `primeradiant.com/serf/agent`, plus `server`, `appwire`, `internal/appprojector`, `cmd/serf-tui`, `cmd/serf-hub` modules in the go.work workspace), embedded JS assets under `cmd/serf-hub/assets`, Markdown docs and prompt templates. No new dependencies.

This is **Phase 6 of 6**, implementing spec `docs/superpowers/specs/2026-06-08-job-control-design.md` §13 (cutover — no legacy residue), §15 (acceptance criteria), and §16 (the internal-naming deferral and the decided subagent job-tool set). It is the final phase.

**PREREQUISITE — Phases 2–5 must be merged before starting.** This plan deletes the legacy tools and repoints their consumers onto the job tools, so the replacements must already exist:
- **Phase 2** (`docs/superpowers/plans/2026-06-08-job-control-phase-2-shell-jobs.md`): `agent/jobs.go` (the `jobManager`, `jobNotification`, `enqueueJobNotification`, `finalize`), `agent/job_shell.go`, `agent/job_notify.go` (`formatJobNotificationBlock`, the `<job-notification>` format), `agent/session_tools_jobs.go` (`registerJobTools`), `capabilityJobControl` in `agent/provider/profile.go`, the reworked `DefShell`, and `DefJobReadOutput`/`DefJobList`/`DefJobStop`.
- **Phase 3** (`docs/superpowers/plans/2026-06-08-job-control-phase-3-delegate.md`): `agent/job_delegate.go` (`createDelegate`/`sendDelegateMessage`, the salvaged `spawnAgent`/`sendInput` reuse), `DefDelegate`/`DefJobSendMessage`, `s.delegateAgentTypeNames()`, the `delegate`/`job_send_message` handlers in `registerJobTools`.
- **Phases 4–5**: `DefJobWatch`/`job_watch`, nested-shell-job forwarding, and the `## Background jobs` prompt section (root + subagent variants).

**The implementer MUST verify the prerequisite before Task 1.** Run from the repo root:

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
for sym in 'func DefDelegate' 'func DefJobReadOutput' 'func DefJobSendMessage' 'func DefJobWatch' 'capabilityJobControl' 'func (s \*Session) createDelegate' 'formatJobNotificationBlock' 'func registerJobTools'; do
  printf '%-40s ' "$sym"; rg -l "$sym" agent/ >/dev/null 2>&1 && echo PRESENT || echo "ABSENT — STOP, Phases 2-5 not merged"
done
```

If any line says ABSENT, **STOP** — this phase cannot proceed and must not invent the replacements.

**Conventions for every task below:**
- Work from the repo root `/Users/jesse/prime-radiant/toil-suite/serf` (this phase touches several modules in the go.work workspace, not just `agent`). Build the `agent` module with `cd agent && go build ./...`; build everything with `make build` from the root.
- **Line numbers in the spec §13 inventory and in this plan are advisory and have drifted.** Before each edit, `grep`/`rg` for the **symbol** named in the task and edit what you find — never trust a bare line number. Each task names the load-bearing symbol for this reason.
- This phase is deletions/repoints, so each task's "test" step is typically a **build + grep verification** (`cd agent && go build ./...` or `make build` must pass, and the relevant gate token must stop matching in production code) rather than a new unit test. Where a repointed *behavior* is testable (a renamed tool-name truncation limit, a renamed event), update the existing test to the new name and run it.
- Commit per task in the repo's `type(scope): subject` style, e.g. `refactor(agent): ...` / `chore(cutover): ...`.
- Order matters: the tasks are sequenced so the build stays green at every commit. Do them in order.
- Full `make build && make test && make lint` from the repo root in the final task (Task 13).

---

## What is DELETED vs. KEPT vs. RETARGETED (verified against the tree)

Read this before starting; it resolves the most error-prone ambiguity in the cutover.

**Salvaged runtime — KEEP (do NOT delete; §16 lets these internal names stay):** in `agent/subagents.go` — `spawnAgent` (called by `createDelegate`, Phase 3), `sendInput` (reused by `job_send_message`, Phase 3), `cancelAgent` + `sub.cancel` mechanics (the delegate `signal` path), `getSub`, the `subagent` struct, `SubagentStatus` + its enum values, `subagent.run`, `subagentResult`, `communicateNudge`/`subagentNeedsCommunicateNudge`, the `subagentManager`'s retention/cap logic (until Task 3 absorbs it). Verify each KEEP symbol still has a non-test caller before deleting anything near it.

**Tool-facing entry points whose tools are gone — DELETE:** the `Session` methods `waitAgent`, `closeAgent`, `listAgents` (`agent/subagents.go`) once their registrations and legacy tests are gone; `agent/subagent_output.go` in full (its `subagent_output` tool is gone); the 7 `Def*` functions; `agent/session_tools_subagent.go` (registration).

**RETARGET (not delete):** `rootOnlyAgentManagementTools` and its consumers → `{delegate, job_watch}`; the three per-tool-name behavior tables (`toolname`, `registry`, `contextmgr`); the UI renderers (Go + JS); the events/wire/snapshot projection chain; the `<subagent-notification>` formatter → `<job-notification>` (Phase 2 already added the job path); `capabilityAgentControl` → `capabilityJobControl`.

Quick caller census (run to re-confirm before deleting any entry point — line numbers will differ):

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
rg -n '\.waitAgent\(|\.closeAgent\(|\.listAgents\(' agent/   # only session_tools_subagent.go + *_test.go should remain
rg -n '\.sendInput\(|\.cancelAgent\(|\.spawnAgent\(' agent/  # job_delegate.go (Phase 3) + tests KEEP these alive
```

---

## File structure

No new files. The edits (delete `D`, retarget `R`) span:

```
agent/internal/tool/definitions.go            D  the 7 legacy Def* funcs (+ the <subagent-notification> ref in DefSpawnAgent's desc, deleted with it)
agent/session_tools_subagent.go               D  whole file (registration)
agent/subagent_output.go                      D  whole file
agent/subagent_manager.go                     D  whole file (logic absorbed into the JobManager in Phase 2)
agent/subagents.go                            R/D  retarget rootOnlyAgentManagementTools → {delegate, job_watch};
                                                  delete dead tool entry points waitAgent/closeAgent/listAgents; KEEP the runtime
agent/session_tools.go                        R  baseSubagentToolPolicy / removeRootOnlyAgentManagementTools / agentUsesRootOnlyManagementTools consumers
agent/session_init.go                         R  the `s.depth>0 { Remove(rootOnly...) }` loop + drop the legacy subagentManager wiring once absorbed
agent/provider/profile.go                     R  capabilityAgentControl block + the three capability sets → capabilityJobControl
agent/internal/toolname/toolname.go           R  "Task":"spawn_agent" → "Task":"delegate"
agent/internal/tool/registry.go               R  defaultToolLimit case "spawn_agent" → delegate / job-tool cases
agent/internal/contextmgr/context_manager.go  R  compaction render case "spawn_agent" (extracts agent_id) → delegate / job_id
cmd/serf-tui/internal/msgrender/tool_renderers.go  R  spawn_agent/resume_agent/close_agent renderers → job tools
cmd/serf-tui/internal/msgrender/tool_bodies.go     R  spawn_agent body comment/shape → delegate
cmd/serf-tui/internal/toolsummary/tool_summary.go  R  spawn_agent/resume_agent/wait/close_agent summary cases → job tools
cmd/serf-hub/assets/renderer.js               R  SUBAGENT_START/END + spawn_agent/resume_agent/close_agent renderers → job lifecycle/tools
cmd/serf-hub/assets/appwire.js                R  serf/subagent/* → SUBAGENT_* mapping → job lifecycle mapping
agent/events/events.go                        R  EventSubagentStart/End kinds → job-lifecycle kinds
agent/events/payloads.go                      R  SubagentStartData/EndData → job-lifecycle payloads
agent/events/eventdata.go                     R  the eventKind() bindings + the compile-time _ EventData asserts
internal/appprojector/appwire_projection.go   R  switch on EventSubagentStart/End → job-lifecycle events
appwire/types.go                              R  NotifySerfSubagentStarted/Ended + SerfSubagentInfo + Subagents field → job equivalents
server/server.go                              R  SubagentStatusInfo + Subagents field → job equivalents
server/appwire_runtime.go                     R  SerfSubagentInfo population from the snapshot → job equivalents
agent/status.go                               R  SubagentInfo / SubagentStatus(snapshot) / Subagents field → JobRecord projection
agent/schema/snapshot.go                      (audit) IsSubagent bool — internal-naming; keep unless it surfaces in a UI/model wire shape
agent/session_outline.go                      R  subagentLifecycleTools / decodeSubagentResult / extractSubagentResult cluster keyed on old tool names
agent/transcript_render.go                    R  subagentResultKnownKeys / subagentResultBody / hasNonSubagentResultKeys keyed on old tool names
agent/session_lifecycle.go                    R  formatNotificationReminder (<subagent-notification>) + acceptNotificationInput / filterDeliverableNotifications subagent framing → job path
agent/session.go                              R  subagentNotification queue (pendingNotifs/enqueueNotification/drainNotifications) — remove once the job queue is the only one
docs/subagent-management/00-subagent-control-plane.md   D  superseded by docs/job-control.md
docs/hooks.md, docs/tools/transcripts.md,
docs/subagent-management/{08,10}.md,
agent/prompts/sections/delegation.md,
agent/prompts/sections/available-agents.md.tmpl        R  update legacy tool names → job tools
docs/architecture.md                          (audit)
agent/*subagent*_test.go, agent/notification_test.go   D/R  delete or rewrite against the job tools
```

---

## Task 1: retarget `rootOnlyAgentManagementTools` to `{delegate, job_watch}`

This is first because it is the lowest-risk repoint and unblocks the gate token `rootOnlyAgentManagementTools` (which is a *keep-the-name, change-the-value* edit — the symbol survives but its content changes; the gate matches the **string literal members**, so retargeting clears the `spawn_agent`/`resume_agent`/… tokens inside it).

**Symbols (grep to locate; line numbers drift):**
- `rootOnlyAgentManagementTools` — the `var` in `agent/subagents.go` (currently `{"spawn_agent", "resume_agent", "wait", "close_agent", "cancel_agent", "list_agents", "subagent_output"}`).
- Its consumers: `isRootOnlyAgentManagementTool`, `removeRootOnlyAgentManagementTools`, `agentUsesRootOnlyManagementTools`, `baseSubagentToolPolicy` (all in `agent/subagents.go`); `agent/session_tools.go` (`baseSubagentToolPolicy`/`removeRootOnlyAgentManagementTools`/`agentUsesRootOnlyManagementTools` call sites); `agent/session_init.go` (the `if s.depth > 0 { for _, name := range rootOnlyAgentManagementTools { s.reg.Remove(name) } }` loop); `agent/builtin_agents_test.go` (the "all-tools subagent should not receive root-only tool" assertion).

- [ ] **Step 1: Locate and retarget the set.** `rg -n 'rootOnlyAgentManagementTools = ' agent/subagents.go`. Change the literal to exactly the §13/§16 decision:

```go
var rootOnlyAgentManagementTools = []string{"delegate", "job_watch"}
```

Per §5.1/§16: tools whose **mere presence** is root-only = `{delegate, job_watch}`. `job_send_message` is **NOT** in this set — it stays present for subagents and gates its *target* by caller role inside the handler (Phase 3's `sendDelegateMessage` already does this). The job-capable `shell`, `job_read_output`, `job_list`, `job_stop` stay available to subagents for their own nested jobs. Do not add any of those four to the set.

- [ ] **Step 2: Confirm the consumers still compile against the retargeted set.** The four helper functions and the `session_init.go` removal loop are name-agnostic (they iterate the slice), so they need no change — but verify: `cd agent && go build ./...`. Expected: builds. The behavior is now "remove `delegate` and `job_watch` from a subagent's tools," which is correct.

- [ ] **Step 3: Update the test that asserts the forbidden set.** `rg -n 'rootOnlyAgentManagementTools' agent/builtin_agents_test.go`. That test iterates the set and asserts no subagent receives those tools — it remains correct (it now asserts subagents don't get `delegate`/`job_watch`). But if Phases 2–5 left the job tools registered, also confirm the test does NOT assert subagents are denied `job_read_output`/`job_list`/`job_stop`/`shell` (they must keep those). If such an assertion exists from an earlier phase, fix it to match §16. Run: `cd agent && go test ./ -run 'TestBuiltinAgents|TestPluginAgent' -v`. Expected: PASS.

- [ ] **Step 4: Grep-verify the token.** `rg -n 'rootOnlyAgentManagementTools' agent/ | rg -v '_test\.go'` — the only production hits should now be the retargeted `var` and its name-only consumers (no `spawn_agent`/`resume_agent` string members remain in the `var`).

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/subagents.go agent/builtin_agents_test.go
git commit -m "refactor(agent): retarget root-only tool-presence set to {delegate, job_watch}"
```

---

## Task 2: repoint `capabilityAgentControl` → `capabilityJobControl` and delete the legacy profile block

**Symbols:** `capabilityAgentControl` (the `toolCapability` const + the three capability-set slices `openAICodexCapabilities`/`anthropicStyleCapabilities`/`geminiStyleCapabilities` + the `if enabled[capabilityAgentControl] { add(tool.DefSpawnAgent()); add(tool.DefSendInput()); add(tool.DefWait()); add(tool.DefCloseAgent()) }` block in `toolDefinitionsForCapabilities`), all in `agent/provider/profile.go`. (Verified: the block adds exactly those **4** legacy Defs; the other three legacy tools were registered only in `session_tools_subagent.go`, deleted in Task 4.)

Phase 2 already added `capabilityJobControl` and added it to the three capability sets, and Phase 3 wired `DefDelegate(nil)`/`DefJobSendMessage()` into its block. So the legacy `capabilityAgentControl` is now **pure dead weight** sitting alongside `capabilityJobControl` in each set.

- [ ] **Step 1: Confirm Phase 2/3 already wired `capabilityJobControl` into the three sets.** `rg -n 'capabilityJobControl' agent/provider/profile.go`. Expected: the const, a block in `toolDefinitionsForCapabilities` adding `DefJobReadOutput`/`DefJobList`/`DefJobStop`/`DefDelegate`/`DefJobSendMessage` (and `DefJobWatch` from Phase 4), and membership in `openAICodexCapabilities`/`anthropicStyleCapabilities`/`geminiStyleCapabilities`. If `capabilityJobControl` is NOT already in all three sets, add it now (it is the replacement). **The shell tool stays under `capabilityShellSearch`** — do not move it.

- [ ] **Step 2: Delete the legacy capability.** Remove the `capabilityAgentControl` const line, remove it from all three capability-set slices, and delete the entire `if enabled[capabilityAgentControl] { ... }` block (the 4 `add(...)` calls).

- [ ] **Step 3: Build + token check.** `cd agent && go build ./...`. Then `rg -n 'capabilityAgentControl' agent/` — expected: **nothing**. (`go build` would also error if any other file referenced the deleted const.)

- [ ] **Step 4: Update the profile test if present.** `rg -n 'capabilityAgentControl|DefSpawnAgent|DefWait' agent/provider/*_test.go`. If a profile test asserted the legacy tools are advertised, replace those assertions with the Phase-2/3 job-tool assertions (or delete the legacy-only test if a `TestJobControlCapability*` already covers it). Run: `cd agent && go test ./provider/ -run 'TestToolDefinitions|TestJobControlCapability|TestProfile' -v`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/provider/profile.go agent/provider/profile_test.go
git commit -m "refactor(provider): drop capabilityAgentControl; job tools under capabilityJobControl"
```

---

## Task 3: delete the legacy registration, `subagent_output.go`, and the 7 `Def*` functions

This removes the bulk of the tool surface in one build-green step: with the profile block gone (Task 2) and the runtime kept (Phase 3), the only remaining references to the 7 `Def*` and the legacy handlers are `session_tools_subagent.go` (registration) and `subagent_output.go` — both of which this task deletes wholesale. The `subagentManager` retention/cap logic is absorbed by the JobManager (Phase 2), so its file goes too.

**Symbols:** `agent/session_tools_subagent.go` (`registerSubagentTools` + its call site in `agent/session_tool_registry.go`); `agent/subagent_output.go` (`execSubagentOutput`/`subagentOutputResult`/`subagentResultView`/`subagentTranscriptView`/`marshalSubagentOutput`/`unavailableSubagentOutput`); `agent/subagent_manager.go` (`subagentManager`/`newSubagentManager`); the 7 `Def*` in `agent/internal/tool/definitions.go`; the dead `Session` entry points `waitAgent`/`closeAgent`/`listAgents` in `agent/subagents.go`.

- [ ] **Step 1: Delete the registration call + file.** In `agent/session_tool_registry.go`, `rg -n 'registerSubagentTools' agent/session_tool_registry.go` and delete that call line in `registerCoreTools`. Then delete the file `agent/session_tools_subagent.go`. (`registerJobTools` from Phase 2/3 is the replacement registration and is already wired in `registerCoreTools` — verify with `rg -n 'registerJobTools' agent/session_tool_registry.go`.)

- [ ] **Step 2: Delete `subagent_output.go`.** `rm agent/subagent_output.go`. Its tool (`subagent_output`) is gone; `job_read_output` is the replacement.

- [ ] **Step 3: Delete the dead `Session` entry points.** In `agent/subagents.go`, delete `func (s *Session) waitAgent(...)`, `func (s *Session) closeAgent(...)`, and `func (s *Session) listAgents(...)`. **Before deleting each**, confirm its only remaining callers are test files: `rg -n '\.waitAgent\(|\.closeAgent\(|\.listAgents\(' agent/ | rg -v '_test\.go'` must be empty (the registration that called them was deleted in Step 1). KEEP `spawnAgent`, `sendInput`, `cancelAgent`, `getSub`, `subagent.run`, the `subagent` struct, `SubagentStatus`, `subagentResult`, `communicateNudge`, `subagentNeedsCommunicateNudge` — Phase 3 uses these. NOTE: `subagentManager.listAgents` (the retention-manager method, distinct from the deleted `Session.listAgents`) is removed with the whole file in Step 4 — do not confuse the two.

- [ ] **Step 4: Absorb/delete `subagent_manager.go`.** Per §11/§13 the 128-retained-terminal cap and retention logic moved to the JobManager in Phase 2. Confirm the JobManager owns retention (`rg -n 'retain|cap|prune|maxRetained' agent/jobs.go`) — if Phase 2 did not port it, **STOP and port the retention/cap policy into `agent/jobs.go` first** (do not silently drop it; the contract requires a running/retained cap). Once confirmed, delete the legacy wiring: in `agent/session_init.go`, `rg -n 'newSubagentManager' agent/session_init.go` (two call sites — `NewSession` and `RestoreSessionFromMetaWithConfig`) and remove them along with the `s.subagents` field assignment, **only if** nothing else still reads `s.subagents`. Run `rg -n 's\.subagents' agent/ | rg -v '_test\.go'` — if Phase 3's delegate runtime still uses `s.subagents.get(...)` (it does — `getSub`), then `s.subagents` and the `subagentManager` **must stay** as the live child registry; in that case delete only the retention/cap fields/methods that the JobManager now owns, not the whole struct. Decide based on the grep: if `s.subagents` has live non-test readers, keep the struct and trim duplicated retention logic; if it has none, delete the file. Record which path you took in the commit message.

- [ ] **Step 5: Delete the 7 `Def*` functions.** In `agent/internal/tool/definitions.go`, delete `DefSpawnAgent`, `DefSendInput`, `DefWait`, `DefCloseAgent`, `DefCancelAgent`, `DefListAgents`, `DefSubagentOutput` (grep each `func DefX` to locate). This also removes the `<subagent-notification>` string embedded in `DefSpawnAgent`'s description.

- [ ] **Step 6: Build.** `cd agent && go build ./...`. Fix any straggler reference the compiler flags (it will catch every production consumer of a deleted symbol — that is the point of doing this before the test-file cleanup). Do **not** touch `*_test.go` yet (Task 12 handles legacy tests); if a non-test file fails to build because it called a deleted symbol, that is a real consumer the inventory missed — repoint it to the job equivalent and note it.

- [ ] **Step 7: Token check.** `rg -n 'DefSpawnAgent|DefSendInput|DefWait|DefCloseAgent|DefCancelAgent|DefListAgents|DefSubagentOutput|registerSubagentTools|execSubagentOutput' agent/ | rg -v '_test\.go'` — expected: **nothing** in production code.

- [ ] **Step 8: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add -A agent/
git commit -m "refactor(agent): delete legacy subagent tool defs, registration, and subagent_output"
```

(`git add -A agent/` is safe here because the working tree should contain only this task's deletions/edits; run `git status` first to confirm no stray files.)

---

## Task 4: repoint the three per-tool-name behavior tables

These three switch statements key per-tool **behavior** on the old names; each is a gate-token hit (`spawn_agent`) and a functional bug if left (the new `delegate` tool would get the default truncation/render instead of the agent-specific one).

**Symbols:**
- `agent/internal/toolname/toolname.go` — `claudeToSerf` map, the `"Task": "spawn_agent"` entry (Claude `Task` ↔ serf canonical). Verified at the map literal.
- `agent/internal/tool/registry.go` — `defaultToolLimit(toolName string)`, the `case "spawn_agent":` output-truncation limit.
- `agent/internal/contextmgr/context_manager.go` — the compaction-render switch, `case "spawn_agent":` which calls `extractJSONField(contentStr, "agent_id")` and renders `[spawn_agent: ...]`.

- [ ] **Step 1: toolname map.** `rg -n '"Task"' agent/internal/toolname/toolname.go`. Change `"Task": "spawn_agent",` to `"Task": "delegate",`. (Claude Code's `Task` tool maps to serf's `delegate` now — that is the closest semantic equivalent: spawn independent agentic work.)

- [ ] **Step 2: toolname test.** `rg -n 'spawn_agent|"Task"|delegate' agent/internal/toolname/*_test.go`. Update any assertion that maps `Task ↔ spawn_agent` to `Task ↔ delegate`. Run: `cd agent && go test ./internal/toolname/ -v`. Expected: PASS.

- [ ] **Step 3: registry truncation.** `rg -n 'case "spawn_agent"' agent/internal/tool/registry.go`. Rename the case to `case "delegate":` and keep the same limit (`MaxChars: 20_000, Strategy: schema.TruncHeadTail`) — a delegate's result is a comparable size. If Phase 2/3 did not already add limits for the other job tools, add reasonable cases now: `job_read_output` (it returns bounded output already — `MaxChars: 50_000` to match `read_file`, or leave to default), `job_list` (`MaxChars: 20_000, TruncTail`). Use the spec's bounded-tail defaults as a guide; do not invent oversized caps.

- [ ] **Step 4: registry test.** If `agent/internal/tool/registry_test.go` asserts a `spawn_agent` limit, rename it to `delegate`. `rg -n 'spawn_agent|defaultToolLimit' agent/internal/tool/*_test.go`. Run: `cd agent && go test ./internal/tool/ -run 'TestDefaultToolLimit|TestToolLimit' -v` (use the actual test name from the file). Expected: PASS.

- [ ] **Step 5: contextmgr compaction render.** `rg -n 'case "spawn_agent"' agent/internal/contextmgr/context_manager.go`. Repoint: rename the case to `case "delegate":`, and change the field it extracts from `agent_id` to `job_id` (the delegate tool returns `job_id`, not `agent_id`), rendering `[delegate: <job_id>]` / `[delegate: N chars]`. Verify the delegate return shape carries `job_id` (Phase 3 §5.4): `rg -n 'job_id' agent/job_delegate.go`.

- [ ] **Step 6: contextmgr test.** `rg -n 'spawn_agent|agent_id' agent/internal/contextmgr/*_test.go`. Update any compaction-render test to the `delegate`/`job_id` shape. Run: `cd agent && go test ./internal/contextmgr/ -v`. Expected: PASS.

- [ ] **Step 7: Build + token check.** `cd agent && go build ./...`. Then `rg -n 'spawn_agent' agent/internal/ | rg -v '_test\.go'` — expected: **nothing**.

- [ ] **Step 8: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/internal/toolname/ agent/internal/tool/registry.go agent/internal/contextmgr/
git commit -m "refactor(agent): repoint per-tool-name tables (toolname/registry/contextmgr) to delegate"
```

---

## Task 5: repoint the `agent/events` lifecycle kinds and payloads

The events are the *source* of the wire/snapshot/UI projection chain. Repointing them first lets the consumers (Tasks 6–8) be repointed against the new names. This is the one place where the gate's **string token** (`SUBAGENT_START`) and the **Go symbol** (`EventSubagentStart`) both appear, so both must change.

**Symbols (all in `agent/events/`):**
- `events.go` — `EventSubagentStart EventKind = "SUBAGENT_START"`, `EventSubagentEnd EventKind = "SUBAGENT_END"`.
- `payloads.go` — `type SubagentStartData struct{...}`, `type SubagentEndData struct{...}`.
- `eventdata.go` — `func (SubagentStartData) eventKind() ...`, `func (SubagentEndData) eventKind() ...`, and the `_ EventData = SubagentStartData{}` / `_ EventData = SubagentEndData{}` compile-time asserts.

**Decision (apply everywhere — §13 requires a deliberate, consistent choice, not a silent keep):** rename to **job-lifecycle** events. Use `EventJobStarted EventKind = "JOB_STARTED"` and `EventJobFinished EventKind = "JOB_FINISHED"` with payloads `JobStartedData` / `JobFinishedData`. Rationale: the model-/UI surface is now jobs, and these events drive UI cards that should read `job_id`/`status`/`type` (not `agent_id`). The payload fields must carry what the UI needs: `JobID string`, `JobType string`, `Status string`, and for finished, `Reason string`, `ExitCode *int`, `OutputBytes int64`, `TranscriptRef string` (mirror the `<job-notification>` attributes from Phase 2's `formatJobNotificationBlock`). Check the existing `SubagentStartData`/`SubagentEndData` fields first (`rg -n 'SubagentStartData|SubagentEndData' agent/events/payloads.go` then read the struct bodies) and map each old field to its job equivalent so no UI data is dropped.

- [ ] **Step 1: Read the current payload structs** so the rename preserves every field a consumer reads. `rg -n -A6 'type SubagentStartData|type SubagentEndData' agent/events/payloads.go`.

- [ ] **Step 2: Rename in `events.go`.** `EventSubagentStart`→`EventJobStarted` (`"SUBAGENT_START"`→`"JOB_STARTED"`), `EventSubagentEnd`→`EventJobFinished` (`"SUBAGENT_END"`→`"JOB_FINISHED"`). Keep the doc comments accurate.

- [ ] **Step 3: Rename + reshape in `payloads.go`.** `SubagentStartData`→`JobStartedData`, `SubagentEndData`→`JobFinishedData`. Reshape fields to carry `job_id`/`job_type`/`status`/`reason`/`exit_code`/`output_bytes`/`transcript_ref` (preserving the JSON tags the wire/UI expects — coordinate with Task 6/7's wire shape). If a field has no job equivalent (e.g. an `agent_id`-only field with no UI consumer), drop it; if a UI reads it, carry it as `job_id`.

- [ ] **Step 4: Rename in `eventdata.go`.** The two `eventKind()` methods and the two `_ EventData = ...{}` asserts → the new type names + new kinds.

- [ ] **Step 5: Find the emitter(s).** `rg -n 'EventSubagentStart|EventSubagentEnd|SubagentStartData|SubagentEndData' agent/ | rg -v 'events/'` — these are the code paths that *emit* the events (likely in `subagents.go`/`subagent.run` or `jobs.go`). Repoint each emit to the new type/kind with the job fields. If Phase 2/3 already emit job-lifecycle events from `jm.finalize`/`createDelegate`, the old subagent emits may be entirely removable — verify the UI still gets a start+finish event per job from the job path; if so, delete the old emits, otherwise repoint them.

- [ ] **Step 6: Build the events package + agent.** `cd agent && go build ./events/ && go build ./...`. The compiler flags every consumer of the renamed symbols inside the `agent` module — Tasks 6–8 handle the cross-module consumers (`server`, `appwire`, `internal/appprojector`), which live in other modules and will be repointed next. If `agent` itself does not build because of a same-module consumer not in Tasks 6–8, repoint it here.

- [ ] **Step 7: events test.** `rg -n 'SubagentStartData|SubagentEndData|SUBAGENT_START|SUBAGENT_END|EventSubagentStart|EventSubagentEnd' agent/events/*_test.go`. Rename to the job symbols. Run: `cd agent && go test ./events/ -v`. Expected: PASS.

- [ ] **Step 8: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/events/ agent/subagents.go agent/jobs.go
git commit -m "refactor(events): rename EventSubagentStart/End to job-lifecycle kinds + payloads"
```

(Adjust the `git add` list to whatever the emitter grep in Step 5 actually touched.)

---

## Task 6: repoint the appwire wire protocol + appprojector translation

**Symbols:**
- `appwire/types.go` — `NotifySerfSubagentStarted = "serf/subagent/started"`, `NotifySerfSubagentEnded = "serf/subagent/completed"`, `type SerfSubagentInfo struct{...}`, and the `Subagents []SerfSubagentInfo` field on the snapshot/status struct.
- `internal/appprojector/appwire_projection.go` — the `switch` arms `case events.EventSubagentStart:` / `case events.EventSubagentEnd:` that build `p.notification(appwire.NotifySerfSubagentStarted, ...)` / `...Ended`.

**Decision:** rename to job wire notifications: `NotifySerfJobStarted = "serf/job/started"`, `NotifySerfJobFinished = "serf/job/finished"`, `type SerfJobInfo struct{ JobID, JobType, Status string; ... }`, field `Jobs []SerfJobInfo`. Keep the JSON `method` strings and field names consistent with what the JS client (Task 8) will switch on.

- [ ] **Step 1: appwire/types.go.** Rename the two `Notify*` consts, `SerfSubagentInfo`→`SerfJobInfo` (reshape its fields to `job_id`/`job_type`/`status`/… matching the snapshot projection), and the `Subagents`→`Jobs` field (keep or update the JSON tag — pick `jobs` and update Task 7/8 to match). `rg -n -A6 'type SerfSubagentInfo' appwire/types.go` first to see the fields.

- [ ] **Step 2: appprojector.** `rg -n 'EventSubagentStart|EventSubagentEnd|NotifySerfSubagent' internal/appprojector/appwire_projection.go`. Rename the switch arms to `case events.EventJobStarted:` / `case events.EventJobFinished:`, build `appwire.NotifySerfJobStarted`/`...Finished`, and map the event payload fields (`job_id`/`status`/`type` from Task 5) into the notification params. Read the current arm bodies to preserve the param keys the JS expects, then update the JS in Task 8 to match.

- [ ] **Step 3: Build both modules.** From the repo root, `make build` (it builds all modules in the workspace) — or target them: `cd appwire && go build ./...` and `cd internal/appprojector && go build ./...`. Fix any straggler. The compiler will flag `server` next (Task 7).

- [ ] **Step 4: Tests.** `rg -rn 'NotifySerfSubagent|SerfSubagentInfo|EventSubagentStart|EventSubagentEnd' appwire/ internal/appprojector/ | rg '_test\.go'`. Rename in tests. Run: `cd appwire && go test ./... ; cd internal/appprojector && go test ./...`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add appwire/ internal/appprojector/
git commit -m "refactor(appwire): rename serf/subagent wire notifications to serf/job lifecycle"
```

---

## Task 7: repoint the server snapshot/status carriers

**Symbols:**
- `server/server.go` — `type SubagentStatusInfo struct{...}` and the `Subagents []SubagentStatusInfo` field on the status struct.
- `server/appwire_runtime.go` — the line populating `appwire.SerfSubagentInfo` from the snapshot (`out.Subagents = append(out.Subagents, appwire.SerfSubagentInfo{...})`).

- [ ] **Step 1: server.go.** `rg -n 'SubagentStatusInfo|Subagents' server/server.go`. Rename `SubagentStatusInfo`→`JobStatusInfo` (reshape fields to `job_id`/`job_type`/`status`/… mirroring `appwire.SerfJobInfo` from Task 6) and the `Subagents`→`Jobs` field. Read the struct body first to map fields.

- [ ] **Step 2: appwire_runtime.go.** `rg -n 'SerfSubagentInfo|Subagents' server/appwire_runtime.go`. Repoint the population to `appwire.SerfJobInfo{JobID: ..., JobType: ..., Status: ...}` reading from the **job snapshot** (the `JobRecord` projection from Task 9, or the existing subagent snapshot if Task 9 hasn't replaced it yet — if this task runs before Task 9, populate from whatever the snapshot still exposes and let Task 9 swap the source; keep the build green). Update `out.Subagents`→`out.Jobs` to match Task 6's field rename.

- [ ] **Step 3: Build server.** `cd server && go build ./...` (or `make build`). Fix stragglers.

- [ ] **Step 4: Tests.** `rg -n 'SubagentStatusInfo|SerfSubagentInfo' server/*_test.go`. Rename. Run: `cd server && go test ./...`. Expected: PASS.

- [ ] **Step 5: Token check.** `rg -n 'SubagentStatusInfo|SerfSubagentInfo|NotifySerfSubagent' server/ appwire/ internal/appprojector/ | rg -v '_test\.go'` — expected: **nothing**.

- [ ] **Step 6: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add server/
git commit -m "refactor(server): rename SubagentStatusInfo carriers to job status info"
```

---

## Task 8: repoint the serf-hub web JS assets (renderer.js + appwire.js)

These are `go:embed`-ed and served live, so they are gate-token hits the static gate would otherwise leave red, and a real runtime break (the web client would stop rendering job lifecycle).

**Symbols:**
- `cmd/serf-hub/assets/appwire.js` — the `if (method === "serf/subagent/started") return [["SUBAGENT_START", ...]]` / `"serf/subagent/completed"` → `"SUBAGENT_END"` mapping.
- `cmd/serf-hub/assets/renderer.js` — `case "SUBAGENT_START":` / `case "SUBAGENT_END":` (the lifecycle banner/reference-card handlers), and the per-tool renderers `"spawn_agent"` (`spawnAgentRenderer`), `"resume_agent"`/`"wait"`/`"close_agent"` (`subagentControlRenderer`), plus the helper map keys (`activeSubagents`, `subagent-reference`, `data.subagentId`).

- [ ] **Step 1: appwire.js mapping.** `rg -n 'serf/subagent|SUBAGENT_START|SUBAGENT_END' cmd/serf-hub/assets/appwire.js`. Repoint the method strings to Task 6's new wire names (`"serf/job/started"` → `"JOB_STARTED"`, `"serf/job/finished"` → `"JOB_FINISHED"`) and read `params.job || params` instead of `params.subagent || params`. Match the exact `method` strings and param shape chosen in Task 6.

- [ ] **Step 2: renderer.js lifecycle cases.** `rg -n 'SUBAGENT_START|SUBAGENT_END' cmd/serf-hub/assets/renderer.js`. Rename the `case` labels to `"JOB_STARTED"`/`"JOB_FINISHED"` and update the handler bodies to read `job_id`/`job_type`/`status` (was `agent_id`/`status`). Update the reference-card el-tracking map (`activeSubagents` → `activeJobs`, keyed by `job_id`) and the dataset/class names if you want them consistent (`subagent-reference` can stay as a CSS class name if a stylesheet depends on it — grep `cmd/serf-hub/assets/*.css` for `subagent-reference` before renaming the class; the CSS class is not a gate token, so renaming it is optional and only for consistency).

- [ ] **Step 3: renderer.js tool renderers.** `rg -n 'spawn_agent|resume_agent|close_agent|"wait"' cmd/serf-hub/assets/renderer.js`. Repoint the tool-renderer map keys: `"spawn_agent"` → `"delegate"` (the spawn→reference-card renderer now keys on `delegate`, reading `job_id`/`transcript_ref` for the clickable card), and the control renderers (`"resume_agent"`/`"wait"`/`"close_agent"`) → the job tools (`job_send_message` for follow-up, `job_stop` for stop; `job_read_output`/`job_list` if they warrant a renderer). Map each old verb to the closest job verb; do not leave a `spawn_agent` key.

- [ ] **Step 4: Verify the served bundle.** There is no JS unit test; verify by build + token grep + a live smoke. `make build` (re-embeds the assets). Then `rg -n 'SUBAGENT_START|SUBAGENT_END|serf/subagent|spawn_agent|resume_agent|close_agent' cmd/serf-hub/assets/` — expected: **nothing** (CSS class `subagent-reference` is acceptable if you chose to keep it; if so, the grep above won't match it since it doesn't contain those tokens). The live smoke is folded into Task 13's e2e check.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add cmd/serf-hub/assets/renderer.js cmd/serf-hub/assets/appwire.js
git commit -m "refactor(serf-hub): repoint web client to serf/job lifecycle + job tools"
```

---

## Task 9: repoint the agent status/snapshot projection

**Symbols:**
- `agent/status.go` — `type SubagentInfo struct{...}` (with `Status SubagentStatus`) and the `Subagents []SubagentInfo` field on the status struct.
- `agent/schema/snapshot.go` — the `IsSubagent bool` field (audit only — see below).
- `SubagentStatus` — the type lives in `agent/subagents.go` (KEEP it as the internal runtime status per §16; the **snapshot/status projection** is what gets repointed to a job shape).

- [ ] **Step 1: Read the current `SubagentInfo` + status struct.** `rg -n -A10 'type SubagentInfo' agent/status.go` and find the `Subagents` field. This is the per-session status the server/UI read.

- [ ] **Step 2: Repoint to a JobRecord projection.** Replace `SubagentInfo`/`Subagents` with a job projection. The cleanest path: expose a `Jobs []JobStatusInfo` (or reuse the Phase 2 `JobRecord` model-facing projection) built from `s.jobs.list(...)`. Define `JobStatusInfo` with `JobID`/`JobType`/`Status`/`Reason`/`TranscriptRef`/`OutputBytes`/`ExitCode` (mirror `appwire.SerfJobInfo` from Task 6 / `server.JobStatusInfo` from Task 7 so the snapshot→wire mapping is 1:1). Repoint the function that populated `Subagents` (grep `rg -n 'Subagents' agent/status.go`) to populate `Jobs` from the JobManager. Keep `SubagentStatus` (the runtime enum) — it is internal; only the snapshot field type changes.

- [ ] **Step 3: schema/snapshot.go audit.** `rg -n -i 'subagent' agent/schema/snapshot.go` → only `IsSubagent bool` (json:`is_subagent`). This flags whether a *session* was spawned as a subagent — it is **internal session metadata**, not a model-/UI-facing job surface, and it is not a §13 gate token. Per §16, leave `IsSubagent` as-is (renaming it would churn the persisted snapshot schema for zero model/UI benefit). Note this decision in the commit message so a reviewer doesn't flag it as missed.

- [ ] **Step 4: Build + repoint consumers.** `cd agent && go build ./...`, then `cd server && go build ./...` (server reads this snapshot — Task 7 must already consume the new `Jobs` field; reconcile if needed). The compiler flags every consumer of `SubagentInfo`/`Subagents`.

- [ ] **Step 5: Tests.** `rg -rn 'SubagentInfo|\.Subagents' agent/ server/ | rg '_test\.go'`. Rename to the job projection. Run: `cd agent && go test ./ -run 'TestStatus|TestSnapshot' -v ; cd server && go test ./...`. Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/status.go agent/schema/snapshot.go server/
git commit -m "refactor(agent): project session status as jobs, not subagents (keep IsSubagent internal)"
```

---

## Task 10: repoint the transcript/outline lifecycle-tool tables

This is a tightly-coupled cluster (bigger than the §13 bullet implied — verified) keyed on the old tool names, spanning two files. Both render legacy `spawn_agent`/`wait`/`resume_agent`/`close_agent` tool *results* into the audit outline and transcript view.

**Symbols:**
- `agent/session_outline.go` — `subagentLifecycleTools` (the `map[string]bool` of old tool names: `spawn_agent`/`resume_agent`/`wait`/`close_agent`), `subagentRefInfo`, `decodeSubagentResult`, `extractSubagentResult`, `subagentLifecycleBrackets`.
- `agent/transcript_render.go` — `subagentResultKnownKeys`, `subagentResultBody`, `hasNonSubagentResultKeys` (gated on `subagentLifecycleTools[toolName]`).

These decode a `subagentResult` (the KEPT struct in `subagents.go`) from a lifecycle tool's result body. The question is whether the **job tools** produce a result that should still be rendered this way.

- [ ] **Step 1: Decide the rendering target.** The `delegate` tool returns `{job_id, type, status, transcript_ref, output, structured_result, ...}` (Phase 3 §5.4), not a `subagentResult` (`{success, status, transcript_ref, ...}`). So `subagentLifecycleTools` must key on the **job** tools that produce a transcript-ref-bearing result worth an audit bracket: `delegate` (and `job_send_message` when it resumes, which also returns `transcript_ref`/`job_id`). Repoint the map:

```go
// agent/session_outline.go
var subagentLifecycleTools = map[string]bool{
	"delegate":         true,
	"job_send_message": true,
}
```

Keep the internal name `subagentLifecycleTools` only if you prefer minimal churn (it's not a gate token), but the string **members** must change. Renaming the symbol to `jobLifecycleTools` is cleaner and recommended — do it consistently across both files if you rename.

- [ ] **Step 2: Repoint the decode shape.** `decodeSubagentResult`/`subagentResult` decode the legacy result body. The `delegate`/`job_send_message` results have a different shape (`job_id`/`status`/`transcript_ref` vs `success`/`status`/`transcript_ref`). Update `decodeSubagentResult` (or add a job-result decoder) to read the job return shape, and update `subagentResultKnownKeys` (`agent/transcript_render.go`) to the job result's JSON keys (`job_id`, `type`, `status`, `reason`, `transcript_ref`, `output`, `structured_result`, `running_in_background`, `timed_out`, `truncated`, `exit_code`, `resumed_from_job_id`). Confirm the exact keys against `marshalDelegateResult` (Phase 3): `rg -n 'job_id\|transcript_ref\|structured_result' agent/job_delegate.go`. The audit bracket (`success/status/child=<ref>`) becomes `status/child=<transcript_ref>` (there is no `success` field in the job model — `status=="completed"` is the success signal, per §4).

- [ ] **Step 3: Build.** `cd agent && go build ./...`.

- [ ] **Step 4: Tests.** `agent/transcript_render_subagent_test.go` and any outline test exercise this cluster heavily — `rg -ln 'subagentLifecycleTools|decodeSubagentResult|subagentResultKnownKeys|spawn_agent' agent/*_test.go`. These legacy tests must be **rewritten** against the job result shape (not deleted — the audit-bracket/transcript-render behavior is still wanted, just for the job tools). Update the scripted tool results from `{success, status, transcript_ref}` (spawn_agent) to `{job_id, type:"delegate", status, transcript_ref}` (delegate), and rename the test file `transcript_render_subagent_test.go` → `transcript_render_job_test.go` if you renamed the symbols. Run: `cd agent && go test ./ -run 'TestTranscriptRender|TestOutline|TestSessionOutline' -v`. Expected: PASS.

- [ ] **Step 5: Token check.** `rg -n 'spawn_agent|resume_agent|close_agent' agent/session_outline.go agent/transcript_render.go` — expected: **nothing** (comments referencing the old names must also be updated, since the §13 gate greps comments too).

- [ ] **Step 6: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/session_outline.go agent/transcript_render.go agent/transcript_render_job_test.go
git commit -m "refactor(agent): key transcript/outline lifecycle rendering on job tools (delegate/job_send_message)"
```

(Adjust the test filename in `git add` to whatever you renamed it to; if you kept the old filename, use that.)

---

## Task 11: replace the `<subagent-notification>` formatter with the job-notification path

Phase 2 already added the durable job-notification path (`formatJobNotificationBlock` → `<job-notification>`, `enqueueJobNotification`/`pendingJobNotifs`, and the job branch in `acceptNotificationInput`). This task **removes the legacy subagent-notification half**, leaving the job path as the only notification mechanism.

**Symbols:**
- `agent/session_lifecycle.go` — `formatNotificationReminder([]subagentNotification) string` (the `<subagent-notification agent_id=%q status=%q turns_used=%q transcript_ref=%q>` formatter), and the subagent-draining/framing inside `acceptNotificationInput` + `filterDeliverableNotifications([]subagentNotification)`.
- `agent/session.go` — `type subagentNotification struct{...}`, `pendingNotifs []subagentNotification`, `enqueueNotification`/`drainNotifications`.

- [ ] **Step 1: Confirm the job path is live.** `rg -n 'formatJobNotificationBlock|pendingJobNotifs|enqueueJobNotification' agent/`. Phase 2 must have wired the job notifications into `acceptNotificationInput` already. If not, STOP (Phase 2 prerequisite). Confirm `acceptNotificationInput` drains **and renders** job notifications via `formatJobNotificationBlock`.

- [ ] **Step 2: Find the legacy subagent-notification enqueue sources.** `rg -n 'enqueueNotification\(' agent/ | rg -v '_test\.go'`. The subagent runtime (`subagent.run` finalize) arms a `subagentNotification`. In the job model, the **delegate** path arms a `jobNotification` via `jm.finalize` (Phase 3's `finalizeDelegate`). So the legacy `subagentNotification` arm in `subagent.run` is now **redundant** for delegates — confirm Phase 3's `finalizeDelegate` arms the job notification, then delete the legacy `enqueueNotification(subagentNotification{...})` call in `subagent.run` (`rg -n 'enqueueNotification' agent/subagents.go`). If any non-delegate path still armed a subagent notification, it has no job equivalent and should be removed (the legacy tools are gone).

- [ ] **Step 3: Delete the legacy notification machinery.** Remove `formatNotificationReminder` (the `<subagent-notification>` formatter), `filterDeliverableNotifications([]subagentNotification)` (Phase 2 replaced its logic with the durable-record-keyed job filter — confirm the job filter exists and the subagent one is unused), the `subagentNotification` type, `pendingNotifs`, `enqueueNotification`, `drainNotifications`, and the subagent-draining block in `acceptNotificationInput`. **Before deleting each**, grep its callers: `rg -n 'formatNotificationReminder|filterDeliverableNotifications|enqueueNotification\(|drainNotifications\(|subagentNotification' agent/ | rg -v '_test\.go'` — every production caller must be the code you're removing in this task. If a caller remains that isn't legacy-notification code, STOP and investigate.

- [ ] **Step 4: Build + token check.** `cd agent && go build ./...`. Then `rg -n 'subagent-notification|formatNotificationReminder|subagentNotification' agent/ | rg -v '_test\.go'` — expected: **nothing**.

- [ ] **Step 5: Commit** (tests for this path are handled in Task 12, which rewrites `notification_test.go`).

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/session_lifecycle.go agent/session.go agent/subagents.go
git commit -m "refactor(agent): remove <subagent-notification>; job-notification is the only path"
```

---

## Task 12: delete or rewrite the legacy subagent test files

The legacy tests are part of the surface §13 says must be "deleted or rewritten against the job tools." Doing them last keeps the build green through Tasks 1–11 (the tests still reference deleted symbols until now, but `go build ./...` ignores `_test.go`; `go test` would fail, which is why the full `make test` is deferred to Task 13).

**Files (verified present):** `agent/subagents_test.go`, `agent/subagent_manager_test.go`, `agent/subagent_output_test.go`, `agent/transcript_render_subagent_test.go` (renamed/rewritten in Task 10), `agent/notification_test.go`, plus the legacy `waitAgent`/`sendInput`/`cancelAgent` callers in `agent/plugin_agents_integration_test.go`, `agent/session_test.go`, `agent/session_dod_test.go`, `agent/builtin_agents_test.go`.

- [ ] **Step 1: Enumerate the breakage.** `cd agent && go vet ./... 2>&1 | rg -i 'undefined|undeclared' | head -50` (or `go test ./ -run x 2>&1` to force compile). This lists every test referencing a deleted symbol (`waitAgent`, `closeAgent`, `listAgents`, `subagent_output`, `DefSpawnAgent`, `subagentNotification`, `formatNotificationReminder`, etc.).

- [ ] **Step 2: Classify each failing test.**
  - **Tests of deleted tools** (`subagent_output_test.go`; the `spawn_agent`/`wait`/`close_agent`/`list_agents` tool-behavior tests in `subagents_test.go`): **delete** the test functions whose subject tool no longer exists. The replacement behavior is tested by the Phase 2/3 job-tool tests (`session_tools_jobs_test.go`, `job_delegate_test.go`).
  - **Tests of the kept runtime** (`spawnAgent`/`sendInput`/`cancelAgent`/`getSub`/`subagent.run` in `subagents_test.go`, `session_dod_test.go`, `plugin_agents_integration_test.go`, `session_test.go`): these exercise the salvaged delegate runtime — **rewrite** them to drive the runtime through its surviving entry points. If a test called `waitAgent` only to block until a child finished, replace it with a wait on the child's `done`/the job's terminal (the pattern Phase 3's `job_delegate_test.go` uses). If it called `closeAgent`/`listAgents` as a tool, delete that assertion (those tools are gone).
  - **`subagent_manager_test.go`**: if Task 3 deleted `subagentManager`, delete this file; if Task 3 kept it as the live child registry (because `s.subagents.get` is still used), keep the tests for the surviving methods and delete the `listAgents` (tool) tests.
  - **`notification_test.go`**: rewrite the `<subagent-notification>` assertions (`requestsContain(reqs, "<subagent-notification", ...)`) to `<job-notification>` driven by a **delegate job** finishing (the job path from Phase 2/3). The behavior under test — a terminal child wakes the parent on a later turn via an injected notification block — is unchanged; only the tag and the arming path change. Keep this coverage; it is load-bearing.
  - **`builtin_agents_test.go`**: already updated in Task 1 (the `rootOnlyAgentManagementTools` assertion). Re-confirm it compiles and passes.

- [ ] **Step 3: Apply deletions/rewrites file by file**, re-running `cd agent && go test ./ -run <RewrittenTest> -v` after each so you see each one go green. Do not bulk-delete whole files without checking for kept-runtime tests inside them.

- [ ] **Step 4: Full agent module test.** `cd agent && go test ./...`. Expected: PASS (pristine output — capture any intentional error output and assert it, per the repo testing rules).

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/
git commit -m "test(cutover): delete legacy subagent tool tests; rewrite runtime/notification tests for job tools"
```

---

## Task 13: docs reconciliation + final acceptance gate

Docs and the final gate. The gate is **authoritative** (§13): run it, repoint every hit, re-run until clean. Note the exemption — historical changelogs and the spec/plan files under `docs/superpowers/{specs,plans}/` and `docs/job-control.md`'s legacy-mapping section legitimately contain the old tokens and are NOT edited.

**Docs to delete/supersede/update (verified hit counts):**
- `docs/subagent-management/00-subagent-control-plane.md` (50 hits) — **superseded** by `docs/job-control.md`. Delete it, or replace its body with a one-line pointer to `docs/job-control.md` (prefer delete; the spec says "superseded by").
- `docs/hooks.md` (1 hit), `docs/tools/transcripts.md` (1 hit), `docs/subagent-management/08-standalone-llm-calls.md` (4 hits), `docs/subagent-management/10-runtime-contracts.md` (2 hits) — **update** the legacy tool names to the job tools.
- `agent/prompts/sections/delegation.md` (1 hit), `agent/prompts/sections/available-agents.md.tmpl` (1 hit) — **update** to the job tools (`delegate`/`job_send_message`/etc.). These are model-facing prompt text, so correctness matters — align with the §5 descriptions and the `## Background jobs` section from Phase 5.
- `docs/architecture.md` (0 token hits, but **audit** for prose describing the legacy control plane) — update any narrative that describes `spawn_agent`/`agent_id` as the control plane.

- [ ] **Step 1: Delete the superseded doc.** `rm docs/subagent-management/00-subagent-control-plane.md` (or replace with a pointer stub — choose delete). Grep the repo for inbound links to it: `rg -n '00-subagent-control-plane' docs/ agent/` and repoint any link to `docs/job-control.md`.

- [ ] **Step 2: Update the live reference docs.** For each of `docs/hooks.md`, `docs/tools/transcripts.md`, `docs/subagent-management/{08,10}.md`: `rg -n 'spawn_agent|resume_agent|close_agent|cancel_agent|list_agents|subagent_output' <file>` and rewrite each mention to the job tool that replaced it (`delegate`/`job_send_message`/`job_read_output`/`job_list`/`job_stop`). Use the spec's reference doc `docs/job-control.md` for the canonical mapping. Run the docs linter as you go: `make lint-docs` (`go run ./cmd/serf-docscheck`) catches doc/code drift.

- [ ] **Step 3: Update the prompt sections.** Rewrite `agent/prompts/sections/delegation.md` and `available-agents.md.tmpl` to describe `delegate` (not `spawn_agent`). Cross-check against the `## Background jobs` section (Phase 5) so the prompt is internally consistent. These feed the model directly — verify the rendered prompt is coherent (`rg -n 'spawn_agent|agent_id' agent/prompts/`).

- [ ] **Step 4: Audit architecture.md.** `rg -n -i 'spawn_agent|subagent control|agent_id|control plane' docs/architecture.md`. Update any prose describing the removed control plane to describe job control. (0 gate-token hits, so this is prose-quality, not a gate blocker.)

- [ ] **Step 4b: Two surfaces the §13 inventory missed (the gate WILL flag them — verified present).** The inventory claimed it grepped the whole tree, but two live surfaces still carry gate tokens and must be handled or the Step 5 gate stays red:
  - **`test/scenarios/*.md` e2e scenario cards** — `subagent-list-and-output.md`, `subagent-close-retains.md`, `subagent-cancel-runaway.md`, `subagent-notification-wake.md`, `transcript-subagent-audit-children-of.md`, and their `INDEX.md` entries (verified). These are e2e scenario cards (run via the `e2e-scenario-testing` skill) that exercise the **removed** tools. **Delete or rewrite** each against the job tools, exactly like the unit tests in Task 12: a `subagent-list-and-output` card becomes a `job_list`/`job_read_output` card; `subagent-notification-wake` becomes a `<job-notification>` delegate-wake card. Update `test/scenarios/INDEX.md` to match. `rg -ln 'spawn_agent|resume_agent|close_agent|cancel_agent|list_agents|subagent_output|subagent-notification|SUBAGENT_START' test/` must end empty.
  - **`tools/dashboard/static/js/*.js` trajectory viewer** — `task-structure.js` (and siblings) render `else if (name === 'spawn_agent')` in a trajectory/task view (verified at `task-structure.js`). This is a live JS consumer of tool-call names. **Repoint** `spawn_agent`→`delegate` (and any `subagent`/`resume_agent`/`close_agent` cases → the job tools), reading `job_id`/`transcript_ref`. `rg -n 'spawn_agent|resume_agent|close_agent|subagent' tools/dashboard/static/js/` must end empty (or only non-token CSS-class-style names remain). The `tools/*.py` transcript helpers (`export_transcript.py`/`read_transcript.py`) — grep them too; if they hard-code the old tool names for rendering, repoint; if they're generic, they may already be clean.

- [ ] **Step 5: RUN THE ACCEPTANCE GATE (§13).** From the repo root:

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
rg -n 'spawn_agent|resume_agent|close_agent|cancel_agent|list_agents|subagent_output|subagent-notification|DefSpawnAgent|DefSendInput|DefWait|DefCloseAgent|DefCancelAgent|DefListAgents|DefSubagentOutput|rootOnlyAgentManagementTools|SUBAGENT_START|SUBAGENT_END|EventSubagentStart|EventSubagentEnd|SubagentStartData|SubagentEndData|NotifySerfSubagent|SerfSubagentInfo|SubagentStatusInfo' \
  | rg -v 'docs/superpowers/(specs|plans)/|docs/job-control\.md|CHANGELOG|/original-attractor-specs/|/design/2026-'
```

This **must return nothing** outside the historical/spec files filtered above. For every hit in live code/docs/prompts/assets: repoint it (it's a consumer the inventory missed — expected for a few), then re-run. Iterate until the filtered gate is empty. NOTE the §13 caveats: the gate intentionally does **not** include `wait` (the legacy tool's un-greppable English name — its registration was deleted in Task 3, and the `DefWait` symbol IS in the gate) or the phantom `wait_job`. The `IsSubagent` snapshot field (Task 9) is intentionally NOT a gate token and stays. The kept internal runtime names (`spawnAgent`/`sendInput`/`cancelAgent`/`subagent` struct/`SubagentStatus`/`subagentResult`/`subagentManager` if retained) are **not** in the gate token list (§16 permits them) — confirm none of *those* internal names accidentally appear in the gate output; if they do, the gate token list didn't include them, so they're fine.

- [ ] **Step 6: Full build + test + lint across all modules.** From the repo root:

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
make build && make test && make lint
```

Expected: all modules build; all tests PASS (pristine output); `make lint` clean (golangci ×4 modules + `serf-namingcheck` + `serf-internalcheck` + `serf-docscheck`). A clean grep is necessary but NOT sufficient — the build/test catch renamed-symbol consumers a token list misses (§13). Fix any fallout and re-run until green.

- [ ] **Step 7: Live e2e smoke** (per `reference_serf_live_run`; build a standalone binary, do NOT touch a running serve). Confirm the cutover didn't break the live surface — both the agent tools and the web client:

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
go build -o /tmp/serf ./cmd/serf
go build -o /tmp/serf-hub ./cmd/serf-hub
. "$PWD/.env"
# In a scratch dir: run a delegate (background), confirm the terminal <job-notification> wakes the
# parent and job_read_output/job_list/job_stop work; open serf-hub and confirm a delegate renders a
# job reference card (the repointed renderer.js JOB_STARTED/JOB_FINISHED path), not a broken/blank tile.
```

Use the `e2e-scenario-testing` skill for a falsifiable scenario card: a delegate fan-out with a notification wake, and a serf-hub view rendering the job lifecycle. Expected: delegate returns a `job_id`; the `<job-notification>` (not `<subagent-notification>`) wakes the parent; the web client renders the job card.

- [ ] **Step 8: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add -A   # after `git status` review
git commit -m "docs(cutover): supersede subagent-management/00; reconcile live docs/prompts to job tools"
```

---

## Phase 6 self-review

- **§13 inventory coverage:** every bullet maps to a task — the 7 `Def*` + registration + `subagent_output` + `subagent_manager` (Task 3); `capabilityAgentControl`→`capabilityJobControl` (Task 2); `rootOnlyAgentManagementTools`→`{delegate, job_watch}` (Task 1); the three per-tool-name tables `toolname`/`registry`/`contextmgr` (Task 4); serf-tui renderers (folded into the UI work — see the gap note below); serf-hub JS assets (Task 8); the events→appprojector→appwire→server→snapshot chain (Tasks 5–9); the `subagentLifecycleTools`/transcript-render cluster (Task 10); `<subagent-notification>`→`<job-notification>` (Task 11); docs (Task 13).
- **Gap flagged for the implementer — serf-tui renderers (do in the Task 8 UI batch):** the spec §13 names `cmd/serf-tui/internal/msgrender/tool_renderers.go`, `tool_bodies.go`, and `cmd/serf-tui/internal/toolsummary/tool_summary.go` (verified: `spawn_agent`/`resume_agent`/`close_agent`/`wait` renderer + summary cases). These are **not** §13 gate tokens individually but DO contain `spawn_agent`/`resume_agent`/`close_agent` (which ARE gate tokens), so Task 13's gate will flag them. **The implementer must repoint serf-tui as part of Task 8 (the UI batch)** — same as the JS: `spawn_agent`→`delegate` reference card, control verbs→`job_send_message`/`job_stop`, reading `job_id`/`transcript_ref`. Build with `cd cmd/serf-tui && go build ./...` and confirm `rg -n 'spawn_agent|resume_agent|close_agent' cmd/serf-tui/` is empty. I kept this as a self-review callout (not a separate task) because it is mechanically identical to Task 8 and shares its build-green window.
- **Two surfaces the §13 inventory missed (now handled in Task 13 Step 4b, verified present):** `test/scenarios/*.md` e2e scenario cards exercising the removed tools (delete/rewrite against the job tools, like the unit tests), and `tools/dashboard/static/js/*.js` trajectory viewer rendering `spawn_agent` (repoint to `delegate`). Both carry gate tokens, so the Step 5 gate would have stayed red without explicit handling. The inventory's "grepped the whole tree" claim did not cover `test/` and `tools/`; the gate (which greps everything) is the backstop, and Step 4b turns those hits into work.
- **Prerequisite honored:** the plan STOPs at the top if Phases 2–5 aren't merged (the replacement tools/runtime must exist), and every "repoint to X" names a symbol Phase 2/3 created (verified to exist in those plans: `registerJobTools`, `createDelegate`, `formatJobNotificationBlock`, `capabilityJobControl`, `DefDelegate`/`DefJobSendMessage`/`DefJobReadOutput`/`DefJobList`/`DefJobStop`).
- **§16 honored:** the kept internal runtime names (`spawnAgent`/`sendInput`/`cancelAgent`/`getSub`/`subagent`/`SubagentStatus`/`subagentResult`/`subagentManager`-if-retained/`IsSubagent`) are explicitly preserved and are NOT in the gate token list; only the model-/UI-facing surfaces (events, wire, snapshots, prompts, docs, the notification tag, the per-tool-name tables) are reconciled now.
- **Build stays green:** ordering is deliberate — retarget the set (1) and capability (2) before deleting the defs (3); rename events (5) before the wire/server/snapshot consumers (6–9); keep `go build ./...` passing every commit, with `go test` deferred to the test-file cleanup (12) and the full `make test`/`make lint` to the final gate (13). The compiler is the completeness net for renamed-symbol consumers; the §13 `rg` gate is the net for string-token residue in docs/prompts/JS that the compiler can't see.
- **No invented details:** every file:line in the spec §13 inventory was grep-verified against the current tree before writing the task; where the spec's line numbers had drifted (registry `:548`, context_manager `:583`) or a reference was imprecise (`SubagentStatus`/`SubagentInfo` live in `agent/status.go` + `agent/subagents.go`, not `agent/schema/snapshot.go` which has only `IsSubagent`), the task names the **symbol** and instructs grep-to-locate. Discrepancies found and resolved are noted inline (snapshot `IsSubagent`, the kept-vs-deleted runtime split, the `s.subagents` live-reader decision in Task 3, the transcript/outline cluster being larger than the bullet implied, the serf-tui gap above).
