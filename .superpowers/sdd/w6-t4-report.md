# Wave 6 — T4 (notifications engine) report

**Status:** DONE
**Branch:** `w6-notifications` (worktree `webui-w6-notifications`), based at `835c17e2b`.
**Commit range:** `1bc14dcde..d02d944aa` (5 commits).
**Manifest:** `notifications/**` only. No chokepoint touched (T1 landed `initNotifications()` in AppShell; this stream fills the body). No `tokens.css` touch (T5's exclusive). No CSS/widgets — the engine is pure TS side-effects.

**Gate at branch tip (honest, AND-chained, real exit codes):**
`npx tsc --noEmit` ✓ → `npx vitest run` ✓ **199 files / 2963 tests** (baseline 194 / 2898 → +5 test files, +65 tests) → `npm run lint` (biome ci) ✓ 571 files, 0 findings → `npm run build` ✓ → `git restore dist/PLACEHOLDER` → tree clean.

## What was built

Six modules under `cmd/serf-hub/frontend/src/notifications/` (each with a focused test):

| File | Role |
|---|---|
| `attention.ts` | PURE transition detection: `levelFromState`, `snapshotFromTree`, `detectFires` (+ loudScope). No DOM/stores. |
| `title.ts` | Title channel: `formatTitle` (count gating), `baseTitle` (focused pane), `applyTitle`. |
| `favicon.ts` | Favicon channel: `dotColorFor` (priority), `buildFaviconDataURI` (pinned colors/encoding), `applyFavicon`. |
| `channels.ts` | `fireOsNotification` + `playTone` (browser-API side effects, all failures swallowed). |
| `leader.ts` | Web-Locks-ONLY election: `electLeader`/`isLeader` (+ test setters). |
| `index.ts` | Orchestration: baseline/edge-fire/reconnect bookkeeping; wires treeStore + prefsStore + workspaceStore + connectionStore; `initNotifications`/`resetNotificationsForTests`. |

## Key wire-shape finding (traced Go source first, per the W5 discipline)

The plan says "per-thread transitions by diffing the needs_you tier + ask_pending". Tracing `internal/hubcore/attention.go` + `tree.go` + `web_api_tree.go` established the decisive fact:

- **`treeStore.tree.needs_you[]` IS the daemon's tier-eligible attention population** — uncapped, top-level, non-archived, non-subagent, exactly the sessions at level `needs_you`/`error` (`tree.go:792-852`), the same set `AttentionSummary` counts over (`attention.go:27-36`).
- Therefore **a ref newly present in `needs_you[]` is precisely the legacy's `into && !was` transition** (`notifications.js:264-267`) — reconstructed from snapshots instead of the per-broadcast `prevLevel` the old wire carried. This is both simpler and *more correct* than walking the whole tree, which would wrongly include subagents/children/archived rows.
- Per-node `state` is the already-normalized UI state (`errored`⇒error, `awaiting|warning`⇒needs_you), so `levelFromState` mirrors the daemon's `attentionLevel(NormalizeState(...))` exactly — needed to distinguish error from needs_you for loudScope.

Fixtures are wire-true: full `TreeNode` shape (every field `apiTreeNode`/`apiTreeNodeTier` stamps), real `state` vocabulary, `ask_pending`, `attentionSummary {needsYou,error,working}`.

## Pin compliance (the wave's most safety-critical)

- **All-OFF, no default layer (the top trap).** Every opt-in is read straight from `prefsStore.getState().notifications` + `.notificationsLoudScope`; `title.ts`/`favicon.ts` take the pref as a *parameter* and never `?? true`. The legacy `notifications.js:31` title/favicon-TRUE default is NOT ported. **Mutation-verified:** resurrecting the TRUE defaults fails `formatTitle` "title OFF: no prefix", `applyTitle` "OFF writes only the base", and `applyFavicon` "pref OFF draws the plain favicon". Also asserted end-to-end (`index.test.ts` "all-OFF defaults": high attention ⇒ `document.title === "serf hub"`, no error dot, no fire).
- **Favicon dot colors — pinned dark-theme literals** (`error=#f7768e`, `needs_you=#e0af68`, `working=#7aa2f7`; priority `error > needs_you > working`; no dot when none). Pinned byte-for-byte in `favicon.test.ts` (incl. the `#`→`%23`-only encoding). The one sanctioned non-token color site — literals live on the generated SVG, never in `tokens.css`.
- **Sound spec exact:** 800 Hz oscillator, gain `0.1`, stopped + context closed after 120 ms, every failure swallowed. Pinned in `channels.test.ts`.
- **Web-Locks-ONLY election** (`navigator.locks.request("serf-hub-os-leader",{ifAvailable:true})`); no-locks / throw / reject ⇒ every tab leader. **No BroadcastChannel** anywhere.
- **Edge-fire gating:** counts apply unconditionally; OS/sound fire only on a transition INTO needs_you/error, only unfocused, only on the leader; loudScope `"asks"` = askPending‖error, `"all"` = every qualifying transition; os/sound gate independently. Baseline = first snapshot (no re-alert on reload) — **mutation-verified** (diffing the first snapshot against empty fails both the baseline and reconnect tests).
- **Title base = focused pane** via `workspaceStore` + `paneRegistry.title()` with the same `threadName` ctx DockHost builds — the one honest divergence, shape `"<pane> · serf hub"`.
- **OS permission flow:** verified the shipped Wave-7 `panes/settings/sections/notifications.tsx` ALREADY performs `Notification.requestPermission()` on enabling the `os` toggle (lines 53-76, with revert-on-deny). Per the plan ("if it does not, add it here"), the engine does **not** duplicate it.

## Decisions / divergences a reviewer should note

1. **Engine drives `treeStore.refresh()` at init and on reconnect.** The tree store does NOT self-refetch on reconnect (it only reacts to notifications; `onReady` is unused by it). So the engine (a) fetches an initial baseline at init — mirroring the legacy `fetchBaseline`, and robust where the rail never mounts (mobile drawer); and (b) on a RE-connect (`connectionStore.state` reaches `"ready"` after a prior `"ready"`) sets a `rebaselinePending` flag and re-fetches, so counts re-sync and the post-gap snapshot re-baselines **silently** (matches floor §3.6 "reconnect re-syncs rather than trusting the gap stayed empty" without re-alerting). Both are public-API calls, not treeStore edits.
2. **Subscribes `workspaceStore`** (beyond the seam comment's "treeStore + connectionStore + prefsStore") so the tab title tracks the focused pane — required by the T4 body's base-title spec.
3. **`fireOsNotification` guards with `typeof window.Notification !== "function"`** instead of the floor's literal `"Notification" in window`. Behaviorally identical in real browsers (present ⇒ constructor; absent ⇒ property missing), but strictly more robust (also handles a present-but-`undefined` value, which is how a test stub models "no API"). OS click uses SPA `routing.navigate("/s/<ref>")` (qualified ref) — no full reload, unlike the legacy `location.href`.
4. **`prevSnapshot` seeded from an already-loaded tree at init** so the first *post-init* transition (not the first snapshot) is what fires, even when the rail loaded the tree before `initNotifications()` ran.

## Process note (transparency)

The title+favicon commit (`9971fff27`) was committed after `biome format` (which does NOT sort imports) but before `biome check` (which does). Its `title.ts` therefore failed `biome ci`'s `organizeImports` in isolation. Caught at the close build gate and folded in by the follow-up `d02d944aa`; **the branch tip passes all gates cleanly.** Interactive rebase is unavailable in this environment, so the fix is a separate commit rather than an amend — the controller may squash on merge if desired. (Root cause: my per-commit gate greps for the vitest line after a `;`, so a failing `npm run lint` in the `&&` chain didn't abort the visible output — corrected by running `biome check --write` before each subsequent commit.)

## Concerns

- None blocking. The reconnect re-baseline is the one behavior with no direct unit precedent in the legacy (which relied on missed-broadcast silence); it's covered by `index.test.ts` "reconnect re-baselines silently" and is conservative (errs toward not firing). Live proof of the full notification journey (grant permission, background tab, force a transition, observe title+favicon+OS+sound under each loudScope, confirm a fresh load does not re-alert) is **T6's** wave-close responsibility, not this stream's.
