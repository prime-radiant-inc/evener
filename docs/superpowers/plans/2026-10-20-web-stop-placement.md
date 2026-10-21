# Web Stop Placement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Adopt native-style Stop/Send placement at every web width, with a
500 ms Send-to-Stop activation delay to prevent accidental double-tap stops.

**Architecture:** Keep existing draft, capability, submission and interrupt
owners. Replace the primary slot's button, expose Stop in the existing menu at
all widths, and keep the short activation timer local to the slot's session.

**Tech Stack:** React, TypeScript, Vitest, existing Chrome CDP browser guards.

**Spec:** `docs/superpowers/specs/2026-10-20-web-stop-placement-design.md`

## Global constraints

- Every web width; document review and native code are excluded.
- No widget API, protocol, cancellation or durable-input changes.
- Existing `hasContent` counts images and skill/command-only drafts.
- Stop delay is 500 ms on a Send-to-Stop transition, not an initially displayed
  Stop. Menu Stop retains its existing gates, without the new delay.
- Preserve Steer's responsive placement and the pending-question surface.
- Use behavior assertions, external-boundary fakes and deterministic clocks.
- Run targeted checks locally; CI owns the full gates.

## Review focus

1. Session replacement or unmount during the delay cannot enable another slot.
2. A stale timer cannot shorten a newer Send-to-Stop delay.
3. Pending requests remain gated after the delay expires.
4. Restored image/skill/command drafts must retain Send.
5. A physical press spanning replacement cannot activate Stop or lose editor focus.

## Task 1: Composer slot and action behavior

**Files:** `cmd/evener-hub/frontend/src/panes/session/composer/Composer.tsx`,
its `Composer*.test.tsx` suites, a focused delay helper/test if needed,
`narrowComposer.ts`, `narrowComposer.test.tsx`, and directly affected
`src/shell/sessionMenu` / `src/dev/surface-sections/composer` examples/tests.

**Interfaces:** Preserve `ComposerProps`, `SessionMenuTurnVerbs` and the existing
submission/interrupt handlers. The timer returns inline Stop eligibility only;
it must not delay the menu or modify session capabilities.

- [ ] Add failing real-composer tests for mutually exclusive Stop/Send at narrow
  and wide widths, including whitespace, images, skill/command-only content,
  typing/clearing, pending questions, menu Stop, held/rejected interrupts and
  empty Enter/form submission. Preserve existing behavior assertions when
  changing their control location.
- [ ] Run the changed tests and record the expected placement failures.
- [ ] Implement the slot swap with distinct button identities, reuse
  `hasContent` and `controlsFor`, and make menu Stop independent of width.
- [ ] Add failing timer tests: initial Stop enabled, swap disabled through
  499 ms, enabled at 500 ms, pending still disabled, repeated transitions and
  session/unmount cancellation. Implement the smallest session-scoped guard.
- [ ] Run `./node_modules/.bin/vitest run src/panes/session/composer
  --maxWorkers=1` from the frontend, then `npm run typecheck` and touched-file
  `./node_modules/.bin/biome check --write <paths>`.
- [ ] Review and commit only the named implementation/test paths after passing
  checks and reading the staged diff. Do not bypass hooks.

## Task 2: Browser proof and owning contracts

**Files:** `cmd/evener-hub/frontend/scripts/overflowguard/run.mjs`,
`cmd/evener-hub/frontend/src/dev/overflowharness-entry.tsx` and directly affected
browser fixtures, `docs/web-ui/parity/contracts-composer-queue-pending.md`,
`docs/product/subsystems.md`, and affected current composer documentation.

**Interfaces:** Use Task 1's real composer through the existing overflow harness.
Do not substitute handwritten markup or mock the composer/interrupt owner.

- [ ] Add trusted coordinate-driven Send double-tap and split-press checks.
  Observe exactly one submission and zero interrupts during the delay; after
  it expires, deliberate Stop must interrupt once. Check keyboard/focus behavior.
- [ ] Measure composer content widths of 399 and 400px, plus a narrow pane
  inside a desktop viewport, with expectations independent of the production
  breakpoint. Check both themes, draft transitions and no horizontal overflow.
- [ ] Run `node scripts/overflowguard/run.mjs --footer-only 390 1400` from the
  frontend after reading the runner; adjust only if its documented modes require
  a narrower focused command. Require exit zero and actual assertion results.
- [ ] Update current product/web contracts and examples; leave historical
  decision documents and document-review behavior alone.
- [ ] Format touched frontend files, rerun affected tests/typecheck, review the
  complete diff and commit the exact owned paths.

## Delivery

- [ ] Run two independent implementation reviews against immutable base/head and
  the amended spec, then simplify-code and rerun affected checks.
- [ ] Push one complete round, shepherd matching-head CI/reviews, satisfy current
  base policy and merge only under repository policy.
- [ ] Verify merged content, remove only owned disposable files/worktree/branches,
  and report behavior evidence and unverified limits. Do not deploy the hub.
