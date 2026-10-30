# Issue #4807: ordinary mobile Back history

Status: frozen for spec PAR. Jesse authorized implementation after spec PAR.

## Scope and intended behavior

Fix ordinary route reconciliation in the browser shell. From a top-level session
in the real mobile AppShell and StackHost, pressing toolbar Back opens Welcome
and makes one deliberate route transition to `/`. Reconciliation must not reopen
the session just left or publish intermediate duplicate entries.

The reported Vitest control on ff0d014d06db374192e8a06d415116683a813b29,
without an attention hop, observed `push /` → `push /s/local%3Aorigin` →
`push /`, history length 9 → 12, settling at `/`. This is a component-environment
observation, not a real-browser reproduction.

AppShell owns route placement and StackHost publishes focused-pane URLs.
Reconciliation must distinguish the newly published route from the previous
render's route, rather than fighting a workspace transition with stale intent.
Keep URL-driven navigation authoritative when it is genuinely pending.

## Preservation and recovery

Attention Back retains browser history length and restores the exact reader.
Keep that integration and its existing test intact. Preserve top-level/nested
session placement, child and transcript context, document companions, settings,
spawn, browser Back/Forward, deferred location recovery and desktop restoration.
No new state persistence, navigation subsystem or attention-history ownership.
A pending session location retains its route until existing location recovery
can place it; this fix does not introduce a new retry owner.

## Non-goals

Do not redesign mobile local Back stacks, change attention navigation, alter
browser-history semantics to replacement for ordinary Back, or add a browser
guard. No changes outside shell route reconciliation, its tests and this spec.
Existing product ownership and recovery contracts remain unchanged.

## Verification

Add an AppShell.test.tsx regression with the existing mobile viewport fixture,
real AppShell and StackHost, a scripted network client and a resolved top-level
session. Record actual pushState and replaceState route events during toolbar
Back. Require the settled Welcome pane and `/`, and exactly the deliberate
`push /`, with no original-session rebound or extra replacement. Do not rely
on history.length alone. Prove it fails before the production fix and passes
after it. Keep the existing attention Back exact-reader/history test passing.

Run only `npx vitest run src/shell/AppShell.test.tsx` and `npm run typecheck`
from cmd/evener-hub/frontend, plus `make build` at the repository root.
Run spec and implementation PAR with two independent competing reviewers,
simplify, commit, branch RoboRev, then push and shepherd a PR against main.
CI owns full-suite verification. Hold the green current-head reviewed PR for
Jesse's merge signal; do not merge.
