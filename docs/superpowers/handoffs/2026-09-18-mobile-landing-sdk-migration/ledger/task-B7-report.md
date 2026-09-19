# B7 — One liveness line and thread title: NEEDS_CONTEXT (no code written)

Status: NEEDS_CONTEXT. No commit, no SHA; worktree `sdk-b7-liveness` is clean
(only `npm ci` ran, in the three package dirs).

## The premise is false: there is no native copy

Plan row B7 and inventory line 167 both say the twin lives in
`mobile/src/conversation/project.ts`. It does not. Neither web rule has an
equivalent anywhere under `mobile/src` or `mobile-native/src`.

### `cadenceStateForStatus` (`.../frontend/src/panes/session/liveness.ts:33-48`)
Maps raw wire `ThreadStatus.type` onto Cadence's five-value space
(`working`/`needs-you`/`failed`/`ended`/`idle`). `grep -rn '"needs-you"' mobile
mobile-native` returns nothing — the Cadence vocabulary does not exist natively.
`project.ts:715` is `status: thread.status.type`, straight passthrough, pinned
as passthrough by `mobile/src/conversation/project.test.ts:1483,1493` (feeds
`{type:"running"}`, asserts `"running"`). Web side is pinned by
`panes/session/liveness.test.ts:12-33` (5 cases).
Nearest native relatives are different rules, not copies:
- `mobile/src/services/roster.ts:48-53` `classifyAttention` →
  `needsYou`/`running`/`recent`, also reads `evener.askPending`, has no
  `systemError`/`closed`/`warning`/`restartRequired` cases. Pinned by
  `mobile/src/services/roster.test.ts:324,329,353`.
- `shell/rail/sessionState.ts:24` `humanizeState` → human words; native already
  imports it directly (`mobile-native/src/screens.tsx:38`), so it is plan row
  C14, not B7.

### `resolveThreadName` (`panes/session/threadTitle.ts:6-18`)
Live `ThreadModel.name` → navigation-summary `title` → undefined. Pinned by
`threadTitle.test.ts:20-29`. Native has no fallback at all: `project.ts:713` is
`name: thread.name` (pinned passthrough at `project.test.ts:1479,1490`).
`roster.ts:60` is `thread.name ?? thread.preview ?? ""` — different fallback,
different default, untested for that chain.

### Two further blockers even if a twin existed
- `navigationSummaryFor` (`threadTitle.ts:21-23`) calls
  `navigationStore.getState()`, the web zustand singleton — plan row D14.
- The other four exports of `liveness.ts` are React
  (`createContext`/`useContext`/`useEffect`/`useState`, lines 5,16,18,57) and
  cannot enter a zero-dependency package.

## Consequence
Web-only, one consumer tree, zero native importers
(`grep -rn 'session/liveness\|threadTitle' mobile mobile-native` is empty).
Plan rule 7 — "a PACKAGE CANDIDATE with one consumer does not move" — applies,
so B7 as written should be dropped or rewritten, not executed.

## Note on naming
There *is* a real "liveness line":
`panes/session/transcript/flow/liveness.ts` `describeLiveness` +
`flow/LivenessLine.tsx`. Native has no equivalent of that either (no
`describeLiveness`, no "may be stalled" text), so re-pointing B7 at it does not
rescue the row.
