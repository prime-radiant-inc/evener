# iPhone redesign: implementation roadmap

> **For agentic workers:** this roadmap splits the redesign into phases. Each phase gets its own detailed plan (`2026-09-25-iphone-redesign-phase<N>-<name>.md`), written just before the phase starts, and each plan is executed with superpowers:subagent-driven-development. Do not implement from this roadmap directly.

**Goal:** Rebuild the iPhone app's interface (`mobile-native/`) to the redesign spec, on the app's existing data layer, landing on main phase by phase so every merge leaves a working app.

**Spec:** `docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md`. The clickable prototype (`docs/design/mobile/redesign/prototype/`) and its usability record (`docs/design/mobile/redesign/usability/findings.md`) show the intended behavior screen by screen; when the spec and the prototype disagree, the spec wins.

## Approach

- **Replace in place, screen by screen.** The app is pre-v1 with internal testers, so each phase swaps a screen (or a family of sheets) for its redesigned version on main. No parallel old UI, no feature flag.
- **Keep the data layer.** The existing services, stores and adapters stay: roster paging (`navigationPages.ts`, `projectBrowser.ts`, `rosterSearch.ts`), the conversation service and stores (`mobile/src/services/conversation.ts`, `mobile/src/state/*`), transcript projection (`projectedRows.ts` over `@evener/appwire-client`'s `projectThread`), questions and approvals (`questionAnswers.ts`, `questionBatches.ts`, `approvalControls.ts`), send/steer/queue/stop (`composerSteering.ts`, `sessionControls.ts`), the durable mutation outbox (`nativeMutationHost.ts`, `mutationOutboxStorage.ts`), drafts (`draftRepository.ts`, `creationDraftRepository.ts`), reader position (`readerPosition.ts`), tasks and activity (`tasksRead.ts`, `ActivitySheet.tsx`'s data), providers, plugins and launch settings. A phase that moves UI out of a mixed file (`screens.tsx` holds three whole screens and their wiring) moves the wiring with it unchanged: never change storage keys, route params that feed storage keys (`hubId`, `ref`), or row identity used by reader position.
- **Fallbacks first, server additions after.** The spec's server additions (section 18) each have a fallback. The phone phases ship on the fallbacks; the server phase then lights up the richer rows and marks.
- **Tests pin behavior, not pixels.** Pure models (attention, ordering, copy selection) get table tests; screens get `react-test-renderer` tests through `src/renderNative.testkit.tsx` that assert what a person sees (text, accessibility labels, which actions fire). When a redesign changes copy that an existing test asserts, update that test in the same task and say so in the commit.

## Phases

Each phase is one plan and usually two to four PRs. A phase starts only after the previous phase's PRs are on main.

| Phase | Plan | Delivers |
|---|---|---|
| 0 | (none) | The spec, the prototype, the usability record and this roadmap on main. |
| 1 | Foundations | Design tokens (spec 16.1-16.3) behind `useColors()` so every existing screen adopts the palette at once; Source Serif 4 embedded, with agent prose and your messages in the serif (spec 16.2, 8.2). |
| 2 | Board | The attention model (spec 13.1-13.2) as pure functions, SF Symbols and the shared primitives (state marks, pulse meter, subagent strip, chips, section headers, grouped rows), which land with the Board as their first screen so nothing ships unused. The Board (spec 7) replaces the project-first Sessions screen as home: section chips, notices, Continue reading, the Live summary line, the four Live bands, pinned-category sections, Projects/Hosts, Test runs, Archived, search, select mode, row swipes and the long-press menu, all on the fallbacks. |
| 3 | Session | The workbench (spec 8): nav and context chips, the notes bar and Notes & links sheet (8.8), the restyled transcript items, the status tray, the Next capsule, the ask dock for questions and approvals, the composer, the session sheet and ⋯ menu, detail level. |
| 4 | Subagents and Reader | Subagents list and read-only subagent transcript (spec 9); the document Reader with comments and review (10.1-10.2), on `docContent` / `nativeDocPort.ts`. The artifact viewer (10.3) waits for the shared-artifacts work to reach main. |
| 5 | New session and Hub | The launch sheet (spec 11) and the Hub sheet with hosts, providers and sign-in, plugins, recipes, display, alerts, hubs (spec 12), as native form sheets. |
| 6 | Attention and resilience | In-app alerts (13.3): banners below the nav, coalescing, held while reading or typing, Next serving held sessions first; connection states and outbox presentation (14). |
| 7 | Server additions | In value order, each its own small PR with hub tests: S2 pending-approval flag, S1 row "why" payload, S13 task progress, S5 activity buckets, S4 seen-through marker, S3 subagent tallies, S12 scoped approvals. The phone switches from each fallback as its addition lands. |

Out of scope here, as in the spec: push notifications and Live Activities (S10, phase 2 of the product), iPad, Android, the decision inbox.

## Landing rules

- One task list per PR; keep PRs reviewable (target under about 800 changed lines of production code).
- Open regular PRs, never drafts. CI must be green at the head.
- Read the RoboRev comment at the head. With no Medium-or-higher finding, admin squash merge (`gh pr merge <n> --squash --admin --match-head-commit <full sha>`), then fix any Lows in a fast-follow PR. A Medium or higher finding is fixed on the PR and re-reviewed.
- Before merging a code PR, run /simplify on it and push its fixes.
- Gates to run locally per task: the task's own tests, `npm run check` in `mobile-native`, and `make test-native-bundle` when imports, `metro.config.js` or `app.json` change. CI runs the full matrix (`make test-native` and the rest).
- Native dependencies are installed per worktree with `cd mobile-native && npm ci` only when `node_modules` is absent or a real directory; never through a symlinked `node_modules`.

## Acceptance per phase

A phase is done when its PRs are merged, `make test-native` passes on main, and the phase's screens match the spec's copy and behavior in a simulator Release build (screenshots attached to the last PR of the phase). Physical-device and TestFlight qualification follow the existing process in `docs/design/mobile/ios-v1-remaining.md`.
