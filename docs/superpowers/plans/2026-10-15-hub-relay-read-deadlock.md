# Hub Relay Read Deadlock Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reproduce the hub relay deadlock through real components, repair its confirmed cause, and verify ordered delivery and recovery before merge.

**Architecture:** Keep the canonical upstream relay and per-target downstream routes. Locate the circular wait with a deterministic real-daemon test before choosing the source edit. Preserve the snapshot acknowledgement barrier and the separate lifetime needed to deliver daemon-departure recovery to a client whose subscription is being installed.

**Tech Stack:** Go, AppWire over loopback WebSockets, the existing real-daemon/browser guard, TypeScript production browser entry route.

**Spec:** `docs/superpowers/specs/2026-10-15-hub-relay-read-deadlock-design.md`, approved by Jesse at commit `e85f5987acc968987c1e901f50ddcba59de2ee01`. The implementation base is `c0dd57709950cba1e5d54eac22dcfd6302c1e190`.

## Global Constraints

- Deployment and a production hub restart require separate authorization.
- Use real hub and source relay components, not a scripted `RelaySessionLease`.
- A test ceiling is a deadlock tripwire, not the ordering mechanism.
- A read must not launch or stop a daemon.
- Do not introduce silent event dropping, broad acknowledgement-barrier removal, client-side deduplication, deadline widening, or repeated retries as substitutes for fixing the cause.
- No client layout, cache format, background budget, subscription capacity accounting, or mutation behavior change is planned.
- Preserve unrelated checkout changes. Heavy local gates run sequentially; full repository checks run in CI.
- Assert typed behavior and independently produced item identity/content, not error-message wording or a test driver's self-reported success.

## Review Focus

1. A second read already charged as pending waits behind the first source command: both must finish without requiring either to cancel.
2. More than `hubRelayPendingDeliveryLimit` (64) retired-child updates cross the response cut: bounded buffering must not recreate the deadlock.
3. A legitimate child's authoritative ref is learned after its update parks: publication must wake the retained update without unrelated traffic.
4. A daemon departure occurs after route publication but before downstream subscription installation: the new subscriber must still learn it needs to re-read.
5. Cancellation or an epoch change arrives while routing is pending: a later read must work, while old deliveries must not enter the replacement route.

---

## Task 1: Prove and repair the read-progress cycle

**Files:**
- Create: `cmd/evener-hub/app_relay_read_progress_test.go`.
- Modify only as confirmed necessary: `cmd/evener-hub/app_relay.go`, `cmd/evener-hub/internal/appsource/relay_session.go` and their existing relay tests.
- Reference: `cmd/evener-hub/app_session_activity_relay_test.go`, `server/appwire_runtime.go`, `internal/appserver/server.go`.

**Interfaces:**
- Exercise `LocalDaemonSource`, `newHubAppServer`, real `server.Server`, and ordinary `appwire.Client.ThreadRead`/`ThreadUnsubscribe` calls.
- The production source read boundary is `(*relaySession).readWithRoutePublication(ctx context.Context, params appwire.ThreadReadParams, publishRoutes func(context.Context, appwire.Thread) error) (RelayReadResult, error)`.
- The downstream completion boundary is `(*hubThreadReadResult).finish(commit bool) bool`.
- Reuse `SetBeforeSubscriptionGate(func())`, `RelayLifecycleHooks.AfterIdleDelete`, and `RecordDescendantAppEvent` to order real producer work. Install the gate before serving; arm it only for the target read. The idle hook names the canonical root even when it retires a child.
- No public API change is planned. Do not add a second reader or mutation path.

- [ ] **Write `TestHubRelayRetiredChildDoesNotBlockRootRead`.** Build the real daemon/appsource/hub/WebSocket chain using the real-session fixture pattern in `app_session_activity_relay_test.go`. Subscribe root and child with replacement disabled. Unsubscribe the child, await its hub-route retirement, and prove the upstream child subscriber remains. Gate a root read before the daemon subscription capture. Produce a real typed child status event, then release the gate. Require successful root read, a subsequent successful read, and delivery of a root sentinel exactly once. Fence negative checks with a later known notification and reject child content on the root client. All failure paths close clients and release gates.
- [ ] **Run the regression red on unchanged production code.** Run `go test ./cmd/evener-hub -run '^TestHubRelayRetiredChildDoesNotBlockRootRead$' -count=1 -timeout=30s`. Expected: a bounded read-progress assertion fails after the real child event and response ordering has been established. A compile error or malformed fixture is not the red result. Capture the blocked wait chain from the test, not only the elapsed time.
- [ ] **Check the cause against the evidence.** Confirm the parked child delivery, source response-cut barrier, pending route lifetime and command-gate ownership form the same cycle. Compare the source at deployed `26960d52c0`. Distinguish this reproduced bug from unproved live subscription history. If the regression does not fail for the hypothesized cause, investigate the next spec hypothesis before editing production code.
- [ ] **Repair the confirmed ownership dependency in the existing relay boundary.** Route-discovery progress must not depend on downstream handoff completion or a later command blocked behind the current read. Preserve known-route acknowledgement ordering and retain daemon-departure recovery until downstream installation. Re-evaluate parked deliveries when authoritative routing changes. Choose the smallest edit justified by the red case; do not merely decrement a global counter while queued reads still hold it, acknowledge and forget updates, or remove the source barrier. A public protocol or wider ownership redesign returns for a spec amendment.
- [ ] **Run green and focused regression checks.** Run the red command again, then `go test ./cmd/evener-hub/internal/appsource ./cmd/evener-hub -run 'Test(Relay|HubRelay|HubAtomicRejoin)' -count=1 -timeout=180s`. Both must complete with exit 0 and no failures.
- [ ] **Commit the proven repair and its test.** Stage only the new regression and production files actually changed, inspect the staged diff, and commit with hooks enabled. Record the red/green commands and confirmed mechanism in the PR evidence, not an evergreen guide's history.

## Task 2: Pin queued reads, routing and recovery

**Files:**
- Extend: `cmd/evener-hub/app_relay_read_progress_test.go`.
- Extend existing real-boundary tests only where useful: `cmd/evener-hub/app_relay_test.go`, `cmd/evener-hub/relay_subscription_lifecycle_test.go`, `cmd/evener-hub/internal/appsource/relay_session_test.go`.
- Update owning behavior: `docs/product/subsystems.md` S06 and `docs/evener-hub-web-routing.md`.

**Interfaces:** Consume the same real fixture and unchanged read/handoff interfaces from Task 1. Preserve authoritative ref precedence and the source's ordinary cancellation/epoch fences.

- [ ] **Add `TestHubRelayQueuedReadDoesNotHoldPreCutAcknowledgement`.** Admit a second read before releasing the first gated response. Assert both results and a following known-target update; neither caller may need cancellation to unblock the other.
- [ ] **Add `TestHubRelayPendingTargetBurstMakesProgress`.** Send `hubRelayPendingDeliveryLimit+1` real child updates before the root response. Assert read completion, bounded state under the existing limit, correct root delivery, and no child leak. Do not enlarge the limit to pass.
- [ ] **Add `TestHubRelayRoutePublicationUnblocksPendingDelivery`.** Park a legitimate child's event before its authoritative route is bound. Complete publication with no unrelated traffic and require ordered delivery to the correct existing consumer, plus coherent snapshot/live state for the new consumer.
- [ ] **Add cancellation/reconnect variants through the same boundary.** For `TestHubRelayReadCancellationAllowsNextRead` and `TestHubRelayReconnectAfterPendingRead`, place cancellation or disconnect at the owned barrier, settle the failed attempt, and require a fresh read plus ordered live output. Assert that the old generation cannot publish into its replacement.
- [ ] **Recheck departure and handoff preservation.** Run existing `TestHubRelayDaemonGoneReachesARouteBoundDuringTheFanOut`, `TestHubRelayDaemonGoneAnnouncedBetweenPublishAndSubscribeStillArrives`, and `TestHubAtomicRejoinWritesResponseBeforePostCutNotification`; extend a real-boundary case if the repair changes an uncovered transition. Each joining client must observe the departure and rehydrate, and healthy sibling clients must continue.
- [ ] **Check alias, failed-publication and forwarding controls.** Preserve the existing authoritative-ref/remap cases in `app_relay_test.go` and source handoff abort tests in `relay_session_test.go`. Add a real-boundary failed-publication case if the changed ownership is not exercised by those controls. Run `go test ./appwire -count=1` for the Go client transport; the complete appsource/hub package run below includes remote forwarding and noncanonical-source controls. Run the shared TypeScript client gate `make test-api-package` after reading its preflight and target. Do not claim native or TUI visual testing from wire-level checks.
- [ ] **Run new cases red against the pre-fix production revision where they expose the same bug, then green against the fix.** Use a separate disposable test lane or exact file-scoped restoration, not shared stashes. Preserve every case's strict behavior assertions; record any case that is a passing preservation control rather than claiming it was red.
- [ ] **Run affected Go and race checks.** Run `go test ./cmd/evener-hub/internal/appsource ./cmd/evener-hub -count=1`, then `go test -race ./cmd/evener-hub/internal/appsource ./cmd/evener-hub -run 'Test(Relay|HubRelay|HubAtomicRejoin)' -count=1`, then `go vet ./cmd/evener-hub/internal/appsource ./cmd/evener-hub`. Each must exit 0. Run the pinned lint tooling on the touched packages after reading its documented invocation.
- [ ] **Document current ownership and commit.** Describe which lifetime owns unresolved routing versus downstream recovery retention and how normal operation resumes. Stage only changed tests and owning docs, inspect the diff, and commit with hooks enabled.

## Task 3: Verify the real foreground/background user journey

**Files:**
- Inspect and extend only as needed: `cmd/evener-hub/background_transcripts_browser_test.go`, `cmd/evener-hub/frontend/scripts/backgroundtranscriptguard/run.mjs` and its README.
- Existing fixture setup/provider: `cmd/evener-hub/skill_composer_browser_test.go` and `cmd/evener/skill_browser_helper_test.go`. Read these before changing their shared fixture contract.

**Interfaces:** Use the existing real daemon provider phase protocol and `TestBackgroundTranscriptsEntrySliceBrowser` entry route. Do not mock the relay, cache, client store or browser renderer.

- [ ] **Read the Go owner, browser driver and setup scripts.** Check missing dependencies and symlinked `node_modules` before using the repository preflight. Do not run `npm ci` through a shared symlink.
- [ ] **Exercise the cross-tab journey and, where feasible, the legitimate child-route retirement sequence.** Open the same root in a second tab while output continues. Verify the tab loads and output continues in both tabs. Add the regression trigger if the real scripted-provider fixture can produce and retire a child through production code; use no fabricated foreign frames. If that trigger cannot be expressed without changing production behavior, retain the real Go producer regression from Task 1 and state the browser attribution limit explicitly.
- [ ] **Verify preservation and recovery independently.** Produce tagged items while the root is backgrounded, open via sidebar, page back, reopen, disconnect, then reconnect. Compare rendered item identity/content with the real daemon's durable producer evidence. Assert one copy of each represented item, order, retained older content, and useful new output after recovery.
- [ ] **Compare background subscription schedules.** Reuse the same event/read sequence with and without an extra background consumer. Report whether background streaming is required for the reproduction; do not infer that from a green browser guard alone.
- [ ] **Run the real guard.** After required preflight, run `go test -tags browserguard ./cmd/evener-hub -run '^TestBackgroundTranscriptsEntrySliceBrowser$' -count=1`. Require actual browser execution and assertions, not skips. Run frontend typecheck/Biome/test gates if frontend files change; run heavy gates sequentially.
- [ ] **Commit only necessary journey changes and evidence references.** Keep no new standalone harness when the existing guard can express the scenario.

## Task 4: Review, simplify, merge and verify

**Files:** All task-owned changes, the approved spec and this plan; include new files in the review diff.

- [ ] **Freeze base/head and run implementation PAR.** Supply both independent competing reviewers identical spec, diff and red/green/journey evidence. Resolve legitimate findings with failing-then-passing tests. Any consequential scope change requires approval and affected re-review.
- [ ] **Run `simplify-code:simplify-code`.** Preserve behavior and rerun the affected checks after simplification.
- [ ] **Run `shepherd-pr:shepherd-pr`.** Commit intended changes, push one batch per round, inspect exact-head CI and both combined and own per-commit reviews. Follow repository base-update and approval policy.
- [ ] **Admin squash merge only the verified head.** Use `gh pr merge <PR-URL> --admin --squash --match-head-commit <SHA>` after current-head checks and findings meet policy.
- [ ] **Update the reported issue.** Add the reproduced mechanism, red/green evidence, merge reference and any unresolved incident-attribution limit to https://github.com/prime-radiant-inc/evener/issues/4666.
- [ ] **Verify remote merge and clean up.** Query `MERGED` and merge SHA, verify intended content, preserve unknown-owner and designated scratch, then remove only this merged lane and disposable files. Report cause, evidence limits, checks, PR/SHA and production-not-deployed status. Mark the goal complete only when the approved delivery is verified.
