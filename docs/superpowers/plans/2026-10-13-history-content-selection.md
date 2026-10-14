# History Content Selection Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans to implement task-by-task.

**Goal:** Ship selectable history content and use it for compact Playdate reads.

**Architecture:** A shared history selection adapter surrounds existing Hub and
daemon history handlers. It fills selected pages from indexed source windows,
binds cursors/snapshots to the selection, and filters subscription output per
downstream owner while preserving the shared complete upstream stream.

**Tech Stack:** Go AppWire/Hub/daemon, generated TypeScript, native C Playdate.

**Spec:** `docs/superpowers/specs/2026-10-13-history-content-selection.md`

## Global Constraints

- Six optional flags default true; tool arguments and results stay together.
- No new endpoint or device-specific Hub behavior.
- Preserve mutation receipts, identity, subscriptions, saved work and recovery.
- Use focused local checks and CI for heavy gates; qualify hardware end to end.

## Review Focus

- Long runs of excluded items must not yield empty intermediate pages or skip chat.
- Changing selection cannot reuse cursors or incremental history from another view.
- Interleaved filtered and full readers must retain independent live subscriptions.
- A failed/replaced subscription capture restores the old selection with ownership.
- Intent-only overlays must never append tool output or omit later conversation deltas.

### Task 1: Protocol and selected history reads

**Files:** `appwire/types.go`, new `appwire/history_content.go`, new
`internal/apphistory/`, generated protocol types/docs.

**Interfaces:** `ThreadContentParams.Selection()`, `apphistory.Read` and
`apphistory.List` wrap typed full-content read/list functions.

- [ ] Write behavioral tests for optional/default booleans, every category,
  intent-only payloads, long excluded runs, byte limits, cursor identity,
  snapshot changes, cancellation and non-progressing source pages.
- [ ] Run focused tests and observe failure before implementing.
- [ ] Implement immutable projection and selected-page accumulation with bounded
  page memory; retain source cursors and snapshot authority.
- [ ] Run `go test ./appwire ./internal/apphistory -count=1` and commit.

### Task 2: Subscription and producer integration

**Files:** `internal/appserver/`, `server/appwire_runtime.go`,
`cmd/evener-hub/app_rpc.go`, focused real producer/transport tests.

**Interfaces:** `appserver.HandleThreadHistory` installs the shared read/list
adapter and a per-subscription content filter through the capture context.

- [ ] Add tests for independent live readers, initial overlays, tool output
  suppression, rollback/replacement, paging refresh and reconnect.
- [ ] Observe failures; integrate both Hub and daemon using the existing
  subscription lifetime and response-before-notification gates.
- [ ] Run focused AppServer/Hub/daemon tests, generate contracts and commit.

### Task 3: Playdate consumer and qualification

**Files:** Playdate repository `src/hub.c` and owning client docs; Evener
`docs/product/subsystems.md` and an evergreen history-selection contract.

**Interfaces:** Conversation-only requests send all six explicit booleans;
opaque older cursors remain owned by the server.

- [ ] Update the native requests and documents; build device and Simulator.
- [ ] Measure full/filtered reads through real AppWire, then install and verify
  physical conversation load, paging, voice review and one deliberate send.
- [ ] Simplify and independently review the complete change; run targeted
  checks, push once, inspect CI and RoboRev, and land only under existing approval.
