# Activity View Retention Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans to implement this plan inline. The coordinating agent owns independent review and live persona confirmation.

**Goal:** Reloading or revisiting a desktop session preserves the Activity inspection the reader chose.

**Architecture:** The existing sidebar UI store retains open/category intent and per-category semantic scroll anchors by public session ref in best-effort browser storage. The browser disclosure binding retains explicit choices only inside an opt-in Activity context. The existing ActivityPageBoundary remains the sole view paging owner; anchor restoration positions its viewport and never calls the transport or starts a retry loop.

**Tech Stack:** React, Zustand, TypeScript, Vitest/Testing Library, browser localStorage.

**Spec:** Riley's reload persona evidence and the approved coordinator design; the current domain-read contract is [session activity](../../product/session-activity.md).

## Global Constraints

- Preserve the mobile branch; work from integrated UI commit `060b2a74116da8f7a7d0016242afa995cfb05af2` on `codex/session-activity-view-retention`.
- Persist only lightweight UI intent, never authoritative activity/task rows, cursors, transports or retry state.
- Keep the selected public ref as the scope; retain live retargeting into an unseen child while Activity is already open, with fresh child folds and anchor.
- Cancel a pending restore on close, category/scope change, or user scrolling. Missing rows are proven absent only by an authoritative complete collection.
- Use fake transport only. No live providers, push or PR. Root owns independent review and live browser checks.
- Do not edit Composer, JobLog, DockHost, title-channel code, or shared SessionActivityStore.

## Review Focus

- Cold collection pages: restoration reaches a retained old row through fresh boundary visibility, one existing page admission at a time.
- Partial/error pages: missing-anchor evidence cannot discard retained intent.
- User navigation: close/category/scope/user scrolling cancels pending work and cannot leak old-scope position or row demand.
- Storage: malformed or unavailable storage must leave the live controls usable; a closed saved view starts no activity read.
- Disclosure ownership: Jobs, inactive Agents, Watches and Tasks retain explicit choices without enabling persistence globally or changing task grouping.

## Task 1: Retain sidebar and disclosure intent

**Files:** `activitybar/activitySidebarStore.ts`, `ActivitySidebar.tsx`, `AgentsTab.tsx`, browser `widgets/disclosure/disclosureStore.ts`, and focused tests under those owners.

**Interfaces:** Sidebar UI state gains per-ref/per-category view intent and best-effort storage hydration. An Activity-only disclosure context opts existing qualified IDs into storage while the existing disclosure store remains the in-memory owner.

- [x] Add real-store/component tests for reload, explicit close, category retention, scope isolation, disclosure restoration, malformed storage and storage failures.
- [x] Run focused Vitest tests and observe the missing retention behavior fail.
- [x] Implement the smallest scoped UI-state persistence and replace Agents' local inactive fold with the existing disclosure owner; retain only its local shown-count intent.
- [x] Run focused tests and confirm GREEN; commit with the ordinary hooks.

## Task 2: Restore semantic position through existing paging

**Files:** Activity sidebar scroll helper, tabs, page boundary, stable identity attributes in `activityRows.tsx` and `TasksPanel.tsx`, and real-component tests with fake transport/geometry observers.

**Interfaces:** Per-category anchor is `{ id: string, offset: number }`. Tabs expose their current read-completeness to the sidebar scroll helper without acquiring another reader. The helper scrolls only; ActivityPageBoundary owns page demand.

- [x] Add reload-after-cold-pages tests, including moved row positions, fresh visibility for each page, authoritative missing-row completion, partial results and cancelled restores.
- [x] Observe RED with the current scroll behavior.
- [x] Implement anchor capture/restoration and cancellation; do not call `loadMore` outside ActivityPageBoundary.
- [x] Confirm focused GREEN, including existing activity paging and task behavior suites.
- [x] Update the owning product guide and subsystem map, format touched paths, run `make test-web` from the repository root, inspect the final diff, and commit normally.

## Evidence and status

- Design approved by the coordinating agent before source edits. Task 1: four real-component regressions RED, then 41 focused tests and 214 script tests GREEN; TypeScript check GREEN. UI retention follows the rail's bounded-blob precedent: 100 recent session views and 2,000 explicit disclosure choices. Hydration does not write storage.
- Task 2: cold-page, disclosure-hydration, expanded-watch, cancellation, efficient repeated-event and immediate-child-reload cases observed RED then GREEN. Existing empty-page advancement remains owned by the shared store. Hidden/mobile focus changes cannot write inherited desktop open intent; the committed desktop view records it.
- Final `make test-web`: PASS typecheck (23.4s), full test suite (119.1s), Biome (1.9s). Impeccable detector found no issues in the changed sidebar/viewport/row surfaces. A stale pre-existing ActivityPanel test locator was aligned with the existing description-first job label; its ownership assertions remain intact.
- Completed: implementation, deterministic verification and owning documentation. Remaining: coordinator integration, independent final review and live persona/browser confirmation. No push or PR from this branch.
