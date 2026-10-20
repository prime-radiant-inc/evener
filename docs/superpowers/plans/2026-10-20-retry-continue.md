# Retry Continuation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make failed-turn Retry send `continue` in browser and native without replaying prior input or consuming a draft.

**Architecture:** Keep the current client submission, durable mutation, and recovery owners. Replace originating-input reconstruction with a shared continuation message. Test the action at each client and the recovery journey through the real hub and session engine.

**Tech Stack:** TypeScript, React, React Native, Vitest, Go, AppWire, scripted external LLM provider.

**Spec:** `docs/superpowers/specs/2026-10-20-retry-continue-design.md`

## Global constraints

- Both clients send exactly `continue`, as an ordinary new text message.
- Preserve transcript, completed work, composer text, selections, and staged images.
- Preserve native latest-failure eligibility, Resume/Sign in priority, and send/queue routing.
- Do not change queued-message retry, provider retry timing, protocol, or server behavior.
- No new widgets or widget props. No live-provider requirement in default tests.
- Prove copy with a complete golden, behavior with structured state and real side effects.
- Isolated lane only. Leave the unrelated main-checkout script untouched.
- Run targeted local checks; full gates belong to CI. Never install through symlinked dependencies.

## Review focus

- An image-only failure with unavailable bytes still offers continuation (Task 1).
- A failure with no loaded earlier user item still offers continuation (Task 1).
- A current draft with skills, commands, and images never leaks into Retry (Task 1).
- A refusal preserves empty, whitespace-only, and occupied drafts and permits later recovery (Tasks 1 and 2).
- A lost acceptance reply reconciles the same mutation instead of duplicating continuation (Task 2).

## Task 1: Switch both client actions with regression coverage

**Files:**
- Create `appwire-client/typescript/turnContinuation.ts`.
- Modify `appwire-client/typescript/index.ts`, `tsconfig.build.json`, and `README.md`.
- Modify `cmd/evener-hub/frontend/src/panes/session/transcript/TurnFailureEndCap.tsx`, `TurnFailureEndCap.test.tsx`, `TurnBlock.tsx`, and the Retry case in `cmd/evener-hub/frontend/src/panes/session/Session.test.tsx`.
- Modify `cmd/evener-hub/frontend/src/stores/threads.ts`, `mutationOutboxIndexedDB.ts`, their focused tests, `cmd/evener-hub/frontend/src/panes/session/composer/Composer.tsx` and `Composer.test.tsx`, and `appwire-client/typescript/state/mutation/records.ts` for the non-consuming recovery marker.
- Modify `mobile-native/src/screens.tsx`, `mobile-native/src/session/errorAction.ts`, `mobile-native/src/session/errorAction.test.ts`, and `mobile-native/src/ConversationScreen.send.test.tsx`.
- Remove `appwire-client/typescript/turnRetryInput.ts`, its `.test.ts`, and `mobile-native/src/session/turnRetryInput.ts` and its `.test.ts` only after confirming no remaining consumer.
- Modify `docs/daily-work.md` and `docs/product/subsystems.md`.

**Interfaces:**
- Produce `export const TURN_CONTINUATION_TEXT = "continue"` from `turnContinuation.ts`, exported by the package root.
- Browser adds `continueTurn(ref: string): Promise<void>` to the thread store. Reuse ordinary send admission, enqueue and resume handling, factoring the existing send tail rather than duplicating it. The continuation intent carries local-only `preserveComposer?: boolean` set to `true`; persist it into rejected recovery records. Composer automatic restoration excludes those records, while explicit queue recovery stays available. This marker never enters the RPC payload and does not change ordinary sends. Remove the unused `turn` prop from `TurnFailureEndCap` and its callers.
- Native consumes existing `document.submitText(text, operation)` and `deliver(service, kind, text, images)` without supplying reissued attachments. Keep `liveSendKind()` as admission/routing owner.

- [ ] Add failing browser tests using the real thread store and dispatcher, with the fake boundary at AppWire transport rather than a stubbed `send`. Click Retry for text, images, unavailable image-only, and absent prior input. Snapshot the complete outbound input and assert one text item, original transcript retention, unchanged draft and no selected skill/command/image payloads. For missing session ref, assert no request.
- [ ] Add refusal tests with `""`, `"  "`, and occupied composer inputs. Assert `expect(currentDraft).toEqual(beforeDraft)` and `expect(recoveryRecords).toHaveLength(1)` after rejection and remount, then use the existing explicit recovery action and assert one accepted continuation. Keep an ordinary-send refusal test proving its automatic draft restoration still works.
- [ ] Change native replay expectations to whole outbound-input snapshots and preservation assertions. Cover latest-failure/action precedence, text/image/absent-input cases, send/queue selection, and refusal followed by recovery. Use the existing actual screen and mutation runtime, not a replacement Retry callback.
- [ ] Run the targeted tests and record the expected old-input or unavailable-action failures before editing product code.
- [ ] Add the shared constant, replace both payload builders, remove Retry-specific dropped-image warnings and input-availability gates, and retain unrelated error precedence. Delete now-unused reconstruction modules only after a repository-wide reference check.
- [ ] Replace obsolete replay-only tests with the continuation cases; keep unrelated diagnostic and disclosure coverage. Update the two owning guides.
- [ ] Run from `cmd/evener-hub/frontend`: `./node_modules/.bin/vitest run --maxWorkers=2 src/panes/session/transcript/TurnFailureEndCap.test.tsx src/panes/session/Session.test.tsx`, then `npm run typecheck`.
- [ ] Run from `mobile-native`: `./node_modules/.bin/vitest run --maxWorkers=2 src/session/errorAction.test.ts src/ConversationScreen.send.test.tsx`, then `npm run check`.
- [ ] Format touched frontend/shared paths with frontend Biome and touched native paths with native Biome. Review exact staged paths and commit this behavior change with the spec and plan.

## Task 2: Prove actual failed-turn recovery and ship

**Files:**
- Add focused journey coverage under `cmd/evener-hub/retry_continuation_browser_test.go` and `cmd/evener-hub/frontend/scripts/retryguard/`, reusing `startHubStack`, `test/e2e/fakellm`, and existing browser-driver support.
- Add native integration coverage under `mobile-native/src/ConversationScreen.retryJourney.test.tsx`; extract shared native test setup only where needed to avoid copying `ConversationScreen.send.test.tsx` scaffolding.
- Add reviewed full-output input snapshots alongside their owning tests.

**Interfaces:**
- Reuse Task 1's production actions; a journey must never create its own substitute Retry implementation.
- The Go fixture owns the real hub, daemon, scripted provider, private connection fixture and process cleanup. Browser and native drivers consume that fixture without printing credentials.
- Native UI qualification uses the existing React Native test renderer and platform adapters on this Linux host; the AppWire transport, conversation service/store, draft document and mutation machinery remain real. This does not claim a physical-device or simulator test.

- [ ] Drive the production browser Retry and mounted native error-row action through a real hub after the scripted provider produces a terminal failure. Observe failed turn, continuation request with preserved previous context, and completed turn in the same conversation.
- [ ] Assert the original request appears once in the authoritative transcript, the continuation appears once, and the new request contains no copied old images or current draft data. Keep a distinct unsent draft throughout.
- [ ] Drop an acceptance reply at the transport boundary, reconnect and reconcile, and assert one accepted mutation/continuation. Separately exercise a known refusal followed by successful retry through the existing recovery owner. Await events or conditions; do not use fixed sleeps.
- [ ] Run the journey with the payload still replaying old input to demonstrate failure, restore only the task-owned change, and rerun to completion. Record which native boundaries are platform adapters.
- [ ] Build web through the repository's build target and run `go test -tags browserguard ./cmd/evener-hub -run '^TestRetryContinuationJourney$' -count=1 -v`; the Go test must run both client drivers and fail on missing prerequisites. Inspect actual runner source and output before reporting.
- [ ] Run PAR on the frozen base/head diff, approved spec, and evidence. Resolve significant findings red-test-first; run simplify-code and repeat affected checks.
- [ ] Commit intended changes, use shepherd-pr, batch fixes into one push per review round, read matching-head CI and RoboRev findings, and satisfy base policy before merge.
- [ ] Admin squash-merge only after settled green checks and acceptable review. Query merged SHA/content, stop owned processes, inspect retention notes, and remove only owned disposable worktree/scratch. Do not deploy.
