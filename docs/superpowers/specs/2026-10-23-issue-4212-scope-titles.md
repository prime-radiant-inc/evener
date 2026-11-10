# Issue 4212: one cascade scope-title chain

## Scope and acceptance

Resolve [issue #4212](https://github.com/prime-radiant-inc/evener/issues/4212)
without changing cascade layout, navigation, activity demand, or transcript
admission. When a scope's thread model is unloaded but its exact navigation row
has a title, its column heading, spine name, spine button's accessible name, and
scope breadcrumb use that title instead of a session ID. A stale thread image
rejected by the existing identity fence cannot supply the column/spine name.

The requested behavior and boundaries in the issue are the approved product
requirements. This specification fixes their implementation choices. It is frozen
for two independent competing PAR reviews before implementation.

## Existing ownership and cause

`ScopeConversation` in `cmd/evener-hub/frontend/src/panes/zoom/Zoom.tsx`
derives activity for a requested ref, then replaces its title with an inline
model-name-or-cascade-fallback expression. That skips the navigation title.
`deriveCascadePath` in `panes/zoom/intent.ts` intentionally supplies identity
fallbacks, including activity ancestor titles that are session IDs.

`useScopeTitle` in `shell/statusbar/ScopeCrumbs.tsx` already owns display-title
resolution for scope crumbs and Zoom's selected mobile title. The raw-ID nav fix
was commit `739db2c3eddeb63ca8b3f0ca25990763a2b07e4d` (#4214).
Current main no longer renders the separate Agent path nav: the cascade footer
uses `ScopeCrumbs`, and compact spines provide the in-track breadcrumb. Do not
restore deleted navigation or change origin filtering to match older screenshots.

## One resolution chain

Extend `useScopeTitle(ref, fallback)` with an optional boolean
`allowModelName = true`. Resolve in this exact order, using the existing
non-empty/truthy title semantics:

1. The loaded thread model's name for the exact requested `ref`, only when
   `allowModelName` is true.
2. The title from `navigationSummaryFor(ref)`, which uses already-loaded bounded
   navigation resources for that exact ref. Never borrow a canonical ref's or
   another alias's title.
3. The caller's existing fallback, unchanged.

Keep the hook reactive to thread-name changes and navigation resource publication
or renames. A rejected model must not hide a useful navigation title. An eligible
model name must continue to outrank a different navigation title. Empty model
names fall through. Do not load a thread or expand navigation just to name it.

`ScopeConversation` calls this hook with `scope.requestedRef`, `scope.title`, and
its existing `imageMatches` value, then assigns the result to
`activity.leaf.title`. Remove its inline alternative resolution chain. The
existing selector of model name/identity may retain only identity if the shared
hook makes name selection redundant.

## Identity fence and preservation

Keep ScopeConversation's identity selection and admission rules unchanged:

- `sessionId = snapshot?.context?.sessionId ?? model?.imageSessionId ?? scope.sessionId`
- `imageMatches = sessionId === undefined || model === undefined || model.imageSessionId === sessionId`
- Transcript/composer admission remains `!rebinding && imageMatches`.

The title gate is `imageMatches`, not transcript readability or `admitted`.
A paused spine can still use its matching model name. An image belonging to a
different session falls through to the exact navigation title, then fallback.
When hydration supplies a matching model again, its name automatically regains
precedence without new recovery controls or title state.

Existing crumb and mobile-title callers have no ScopeConversation admission
fence and retain their default model eligibility. This change does not introduce
new identity subscriptions or alter their current navigation behavior. All callers
share the same precedence implementation; the column/spine caller supplies the
identity decision it already owns.

## Consumers

- `ScopeConversation` section `aria-label` consumes resolved `activity.leaf.title`.
- `CascadeColumn`'s desktop `h3` consumes that same value, including loading and
  fenced placeholder columns.
- `CascadeSpine`'s name and its existing Show action's `aria-label` consume that
  same value. Keep the action wording and pop target unchanged.
- `ScopeCrumbLabel` resolves every existing footer/sidebar path crumb through
  `useScopeTitle`. Buttons keep their requested navigation ref; the current crumb
  stays non-interactive.
- Zoom's selected mobile title keeps using `useScopeTitle` with its existing ref
  fallback. Do not add a second mobile title row or change tab-title ownership.

## Non-goals

No Zoom/StackHost redesign, path derivation or origin-collapse changes, navigation
placement changes, protocol/store lifecycle changes, transcript admission changes,
new loading state, labels, network reads, or native-client changes. Keep raw IDs
as the existing last resort when neither eligible name nor navigation title exists.
No unrelated fixes or documentation rewrites. File unrelated defects separately.

## Verification

Use real components and stores with the existing FakeClient network boundary.
Use test-supplied title sentinels, not hardcoded product copy, as data oracles.

1. Hold an ancestor's `thread/read` so its model remains unloaded. Publish its
   exact navigation row with a distinct title while cascade fallback remains an
   ID. Assert the readable heading and section name match the navigation title
   and crumb. Drill until that scope becomes a spine and assert its visible name,
   section name, and button accessible-name title agree. Pop through the spine
   and prove the original requested ref and selected branch behavior remain.
   Run this regression before the fix and record the raw-ID failures.
2. Exercise a scope with activity identity different from its retained model's
   image identity. Its column title must fall through to its navigation row,
   never the stale model name. Confirm stale transcript bytes remain unadmitted.
   Cover the hook's disabled-name path, empty model name, exact-ref fallback,
   eligible-model precedence, and reactive navigation/live renames without adding
   subscription demand. Restore a matching model and verify precedence recovers.
3. Run focused Vitest files `src/panes/zoom/Zoom.test.tsx`,
   `src/shell/statusbar/ScopeCrumbs.test.tsx`,
   `src/shell/statusbar/statusScope.test.ts`, and
   `src/panes/zoom/intent.test.ts` from `cmd/evener-hub/frontend`.
   Inspect scripts/config before execution and report actual failures and passes.
4. Run frontend `npm run typecheck`, Biome on touched TypeScript files, and root
   `make build`. Do not run the full local suite; CI owns it. Reuse only a
   lockfile-compatible dependency install and never run `npm ci` through a
   symlinked `node_modules`.
5. Run PAR on the frozen implementation diff, simplify-code, commit, and the
   local branch RoboRev route. Merge latest main before the final push if it
   moved, then push once per review round, open the PR against main, and shepherd
   exact-head checks/reviews. Admin squash-merge only when every required check
   is green and every RoboRev finding is low or conclusively refuted.
