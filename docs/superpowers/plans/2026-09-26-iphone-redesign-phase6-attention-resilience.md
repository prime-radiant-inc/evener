# iPhone redesign, Phase 6: Attention and resilience (Implementation Plan)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** In-app alerts tell you when another session needs you without breaking into what you're reading or typing, and the phone is honest about its connection: it says when it's reconnecting or offline, keeps what you send until it can deliver it, and shows each message's delivery on the message itself, with no recovery screen.

**Architecture:**
- **Alerts** (part 2). A pure `AlertCenter` (`src/alerts/alertCenter.ts`) decides which banner shows, which alerts wait while you read or type, and which sessions alerted you most recently, with the prototype's tested timing. Pure detectors (`src/alerts/alertEvents.ts`) turn successive navigation reads into alerts. An `AlertsProvider` feeds the center from the app-wide attention data, and `AlertBannerHost` draws the banner just below the nav bar and opens what you tap.
- **Connection.** A `ConnectionClock` in `ConnectionProvider` measures how long the app has been in front without a live connection, so the Board's toolbar, the Session's connection bar and the sheets read one status (`useConnectionStatusText`), and no text asks you to reconnect or refresh.
- **Outbox.** The durable mutation runtime stays (roadmap: keep the data layer). Phase 3 put the outbox inline as ghost bubbles and removed the recovery screen; this phase adds what it left: Discard for a lost send, Send now for a message a Stop held, the offline caption, Send while offline, and an app-wide flush that sends what no open session will.

**Tech Stack:** Expo SDK 57, React Native 0.86.3, React 19, TypeScript, vitest 5 with react-test-renderer (`src/renderNative.testkit.tsx`), `expo-symbols` (phase 2), `expo-haptics`, `expo-sqlite/kv-store`, the durable mutation runtime (`src/nativeMutationRuntime.ts`, `src/mutationOutboxStorage.ts`) over `@evener/appwire-client/state/mutation`, and the `node:sqlite` double (`src/sqliteSync.testkit.ts`) for runtime tests. CocoaPods runs through Bundler 2.7.2 on Ruby 3.3.6.

**Spec:** `docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md`: principle 2 (Calm, section 4), 8.3 (Next), 8.5 (Send's offline row and the ghost bubbles), 13.3 (in-app alerts), 14 (states and resilience), 16.3 and 16.6 (a banner's elevation, motion and haptics), and Appendix A frame 6. The alert rules come from the prototype (`docs/design/mobile/redesign/prototype/core.js`, `EV.alert` and `releaseHeld`) and the usability record (`docs/design/mobile/redesign/usability/findings.md`: round 1 problems 2 and 3, round 2 problem 2, round 3 problem 1, round 4 problems 2, 3 and 6). The roadmap is `docs/superpowers/plans/2026-09-25-iphone-redesign-roadmap.md`.

## Global Constraints

- **Copy is the spec's, verbatim.**
  - Connection: "Reconnecting…" after 2 seconds without a connection, "Offline · updated 3m ago" after 30 seconds, and "Update needed" for a close no retry can fix, whose sentence is "This app and the hub need compatible versions. Update the app from TestFlight, or update Evener on the hub."
  - Alerts: the state words "Failed", "Question", "Approval", "Warning", "Restart needed", and a coalesced banner "3 sessions need you" (never shown for one session).
  - Delivery: "Sending…", "Couldn't confirm this was sent", and the actions "Check" and "Discard".
- **Timing** (spec 13.3, 14, 16.6): a banner stays 8 seconds and never goes away while a finger is on it; alerts within 5 seconds combine; a banner drops with a 300ms spring, and with Reduce Motion it fades in instead.
- **Haptics** (spec 13.3): a warning for a failure, a light impact for anything else, none for finished results, and nothing when Hub > In-app alerts turns haptics off.
- **Calm** (spec principle 2):
  - A control appears only when it can act: Check only while connected, and the Board's hub-writing actions only while connected.
  - No screen carries a Reconnect button or asks you to refresh, and no text a person can read says "Reconnect", "Refresh" or "Pull down to retry" (Task 2's audit enforces it).
  - Nothing moves unless its data moved: a banner moves only when it drops in or is swiped away.
- **Color:** only from `useColors().palette`. A banner's edge is `attentionEdge`, and a finished result's is `accentEdge`. Only the state word takes a hue.
- **Routes and storage:**
  - Route names and params are unchanged. The Board stays `"Sessions"` with no params, so a coalesced banner reaches Needs you through `src/board/boardJump.ts` (part 2's Task 7), never a route param.
  - Existing storage keys are unchanged. One new kv-store key, `evener.native.alert-preferences` (per device, part 2's Task 5).
  - The mutation database keeps its schema: Discard deletes a row, Send now moves one, and nothing adds a column.
- **Tests** meet the hub at the request boundary: `FakeClient` from `@evener/appwire-client/testing/fakeClient`, the `boundary()` fake of `src/projectBrowser.test.ts`, or `scriptedClient` from `src/renderNative.testkit.tsx`. The mutation runtime runs for real over `openSqliteSyncDouble()`. Never mock the module under test.
- **Repo rules:**
  - Never run Biome in `mobile-native/`.
  - Never run `npm ci` through a symlinked `node_modules`: check that `[ -L node_modules ]` prints nothing first.
  - Never `git add -A`.
  - iPhone only. Run the task's own tests and `npm run check` locally; CI runs `make test-native`.

## Rulings

Decisions this plan makes where the spec is silent or its data doesn't exist yet. Questions 1 to 3 below ask Jesse about the three that change what a person sees.

1. **Alerts come from the navigation rows the Board already reads,** diffed per session: the complete `needs_you` section and Live's first page, classified by phase 2's `boardState`, re-read on `evener/navigation/invalidated`. A finished alert needs the session's working row on Live's first page, which holds the sessions most likely to finish. Nothing here listens to `evener/attention/changed`, which never fires for sessions on other hosts (#2529). A remote session reaches navigation through the hub's remote-thread cache, whose publish invalidates navigation (`cmd/evener-hub/main.go:635`) on its 30-second refresh or a poke (`refreshHubRemoteThreads`, `main_background.go:113`), so a remote session alerts as soon as its Board row changes, and #2529 needs no fallback here.
2. **The first read on a new client is a baseline and alerts nothing.** A new client means the app came back to the foreground, the hub changed, or a closed connection was replaced. A drop the same client recovers from is diffed, so a flap on the train still tells you what changed meanwhile (Question 1).
3. **A row on an offline host keeps its last state,** so a host going away and coming back never alerts. The hub marks such rows `offline`, and phase 2 shows them as shut down.
4. **Which alerts have switches.** Failures (on), questions and approvals (on) and finished results (off) have switches (spec 12). Warnings, restart-needed and notices always alert, as in the prototype's `EV.alert`.
5. **Notice alerts.** A provider sign-in that expired and a host that went offline alert when they appear while the Board isn't on screen. A broken plugin never alerts: the Board checks plugins only while it is on screen (phase 2 ruling 8), where its notice row already shows.
6. **A coalesced banner counts each session once,** and a session that alerts again updates its own entry. (The prototype counted it twice.)
7. **Holds.** Banners wait while the Reader is open (and the artifact viewer, once it exists), while you type (the composer focused with text in it, so sending lets them go, as the prototype's Hub row says: "They show when you leave the document or send"), and while a sheet is up (Question 2): the `formSheet` routes behind phase 3's `sheetOptions()` and `<Sheet>` (Jesse's answer 11, 2026-09-26), phase 5's `"Hub"` and `"NewSession"` modal routes, and the React Native `Modal`s left (full-screen viewers and the Hub's inner detail sheets). A banner can't show above any of them: each presents over the app's root view, where the banner lives. They show 200ms after the last hold ends, the prototype's delay after the composer blurs, so the screen you land on counts first: landing on the session that alerted answers it. A hold keeps new banners back. One already showing when a hold starts stays, since it sits below the nav bar, never over the composer, and a sheet presents above it; what the hold kept back joins it when the hold ends, as a burst does, so nothing on it drops out.
8. **Haptics.** A banner buzzes once, when it drops in; alerts that join it don't. Every haptic in spec 16.6 answers to the one Haptics switch (phase 3 ruling 5).
9. **Alerts read the fleet with their own board controller, never paused.** The Board and the Session pause theirs on blur (phase 3 ruling 33), and alerts must hear about sessions while you're anywhere. That costs a second set of navigation reads while the Board or a Session is in front; sharing one controller is a later consolidation.
10. **The connection clock counts only time in front.** The app closes its connection in the background (`ConnectionProvider`), so returning never flashes "Offline". "Updated 3m ago" still counts from when the data was last live, background included.
11. **The offline age is whole minutes, at least 1m** ("updated 1m ago" from the 30-second mark), because the status changes once a minute. `relativeAge` says "now" under a minute, which would read "updated now ago".
12. **Offline Send is fenced to the session instance the phone last saw,** as an online send is. A session this phone hasn't read since launch has no instance to fence with, so there Send stays disabled offline and the draft stays. That is about the phone's read, not the session's status: a shut-down session (`notLoaded` on the hub) the phone has read resumes on a send, fenced as an online resume is.
13. **Offline, Send queues whenever the harness can.** By the time the message arrives another turn may have started, which refuses a `turn/start`; the daemon runs a queued message at once on an idle session and holds it behind a running one (`clientMutationQueue`, `agent/session_client_mutation_queue.go`). A shut-down session resumes on a send.
14. **What a waiting message says.** "Sending…" while the connection is live, "Will send when you're back online" while it isn't (the prototype's words, and principle 5: nothing is sending), and Send's label offline says the same.
15. **"Couldn't confirm this was sent" offers Check only while connected, and Discard always,** since Check needs the hub and Discard doesn't. This covers a lost send, the unconfirmed draft and a record the phone couldn't place.
16. **A message a Stop held before it left the phone comes back as held,** with "Send now" and "Cancel", like the queue a Stop parks (spec 8.5). Send now moves it to the end of its session's line, behind the Stop that held it, so that Stop can never stop it. The roadmap's phase 6 row, as phase 3 words it, calls these Retry and Discard; the ghost uses spec 8.5's words for a held message.
17. **The flush.** On each ready connection, and whenever a record lands for a session nobody has open, the app settles and sends every target of the active hub that no session screen holds, as the web's `handleReady` does, then lets each go once nothing on it can be sent. On a ready connection it also settles a target a session screen holds when something on it waits, because a screen under the Reader or a subagent doesn't read while covered; the screen keeps its target.
18. **The Board's actions that write to the hub wait for the connection** (Question 3). Mark as read and unread are this phone's own and stay.

## Questions for Jesse

1. **Should a banner tell you about changes that happened while the phone was disconnected?** My recommendation, and what ruling 2 builds: yes after a drop the connection recovers from while you're using the app (a flap on the train), and no after you return from the background, where the Board, Back's count and Next already show what needs you.
2. **Banners while a sheet is up:** hold them until the sheet closes (my recommendation, and what ruling 7 builds), or show them over the sheet as the prototype does? Over a sheet, the banner would cover the sheet's own Cancel and Done, and a React Native modal presents above the app's overlay, so showing it there needs a full-window overlay.
3. **Board actions taken offline.** Spec 7.5 says they "go to the outbox". The phone's outbox carries only the four turn kinds (`nativeMutationRuntime.ts:69-77`), and organization changes run through a one-slot journal that fails at once offline (`navigationActions.ts`), so an offline archive dims and silently comes back. A durable queue for Archive, Pin, Rename and Shut down is its own piece of work, about the size of this phase's outbox PRs, and a Stop queued offline would stop whatever turn runs when it lands. My recommendation, and what Task 16 builds: those actions don't show while offline, and a later PR adds the queue if you want it. Build the queue now instead?

## Built on earlier phases

Phases 2 to 5 are planned beside this one, so some names below are what those plans say they produce. Tasks 3-11, 17 and 18 are in part 2. Before a task that uses one, read the landed code. If it landed under another name or shape, use the landed one and say so in the PR.

| This plan uses | From | Used by |
|---|---|---|
| `boardState`, `liveBands`, `whyLine`, `WhyLine`, `LiveBands`, `BoardState` (`src/board/attention.ts`), `StateMark`, `seenMarkers` | phase 2 PR 1 | Tasks 3, 4, 7, 8 |
| `createBoardController` (`src/board/boardData.ts`), `connectionStatus` (`src/board/connectionStatus.ts`), `BoardScreen`, `BoardToolbar` | phase 2 PR 2 | Tasks 1, 7, 8 |
| `Notice` and `notices` (`src/board/notices.ts`), `Notices.tsx`; `rowActions.ts`, `SwipeRow.tsx`, `RowMenu.tsx`, `SelectBar.tsx` | phase 2 part 2 (its Tasks 12-14) | Tasks 4, 8, 16 |
| The demo fleet (`src/dev/demoFleet.ts`, `scripts/demo-hub.mts`) | phase 2 PR B | Task 17 |
| `useConnectionStatusText()` and the Session's connection bar (its Task 15); `compactDuration` (`src/session/format.ts`) and `sendAction` (its Task 3); `Composer` (its Task 5); `ghosts.ts`, whose parked queue reads the package's `isQueueParked`, and its wiring (its Tasks 7-8); `fleetOrder.ts` (`othersNeedingYou`, `nextSession`), `BackButton` and the Next capsule (its Tasks 32-33); `Toast` | phase 3 | Tasks 1, 2, 10, 11, 13, 14 |
| Phase 3 left here: Send while offline (ruling 4), every haptic (ruling 5), Next's recent order (ruling 11), the Board's outbox and a Stop-held message's retry (ruling 3) | phase 3 | Tasks 6, 11, 13, 14, 16 |
| `NativeMutationRuntime.settleTarget` (its Task 2); `ReaderScreen` (`src/reader/ReaderScreen.tsx`, its Task 14) on the `"Reader"` route | phase 4 | Tasks 10, 15 |
| The grouped-list pieces (`src/sheet/Grouped.tsx`, its Task 1); `useConnectionStatusLine`, `SheetStatus` and `INCOMPATIBLE_VERSIONS` (its Task 2); the Hub sheet (`src/hub/HubSheet.tsx`, `HubHome.tsx`, its Task 3) and its sheet routes `"Hub"` and `"NewSession"`; the "Reconnect" guard and the connection messages (its Task 27); phase 5 left the In-app alerts page here (its ruling 11) | phase 5 | Tasks 1, 2, 8, 9 |

## Review Focus

1. **A pile of banners when you come back.** Opening the app, switching hubs or returning from the background must not alert about sessions that already needed you; the Board, Back's count and Next already show them. Pinned by Task 4 (the first read is a baseline) and Task 8 (a new client starts a new baseline, and a Needs you section still loading is never the baseline), both in part 2.
2. **Old news alerting twice.** A host that goes offline and comes back, or a session that alerts twice within a burst, must never produce a second banner or count one session twice. Pinned by Task 4 (offline rows keep their state) and Task 3 (a coalesced banner counts each session once), both in part 2.
3. **Returning to the app flashes "Offline".** An hour in the background, then a foreground return whose connection takes a second, must show nothing, and "Reconnecting…" only after 2 seconds. Pinned by Task 1 (the clock never counts background time).
4. **A message sent offline is lost, sent twice, left waiting, or stopped by its own Stop.** Send while offline, then leave the session or open the Reader from it, relaunch, and come back online: the message goes out exactly once, with no trip back to its session. A message a Stop held, sent again, goes after that Stop. Pinned by Task 14 (the offline admission stores one record, sent once when the connection returns), Task 15 (a fresh process sends it once, and the flush settles for a session screen under the Reader) and Task 12 (Send now moves the row behind its Stop).
5. **A banner in the way.** A banner never covers the nav bar (Back, the title, the ask dock), never interrupts a sheet or your typing, and never goes away under your finger. Pinned by Task 8 (it sits below the header height, and a sheet route holds it), Task 10 (typing and every `Modal` hold it) and Task 3 (a finger keeps it), all in part 2.

---

## PRs and lanes

| PR | Tasks | Model | Starts when | Lane |
|---|---|---|---|---|
| A: one connection clock, and no reconnect words | 1-2 | Sonnet (1), Opus (2) | the phase starts (phase 5's PRs on main) | connection |
| B: the alert center and haptics (part 2) | 3-6 | Sonnet (3-5), Opus (6) | the phase starts | alerts |
| C: banners on every screen (part 2) | 7-8 | Opus | PR B lands | alerts |
| D: holds, the In-app alerts page and Next (part 2) | 9-11 | Opus (9-10), Sonnet (11) | PR C lands | alerts |
| E: your own undelivered messages | 12-13 | Sonnet (12), Opus (13) | the phase starts | outbox |
| F: Send while offline | 14-16 | Opus (14, 16), Sonnet (15) | PRs A and E land | connection |
| G: the demo and the screenshots (part 2) | 17-18 | Opus (17), then by hand | PRs D and F land | alerts |

- Three lanes start together: alerts (PR B), connection (PR A) and outbox (PR E). The outbox lane joins the connection lane at PR F, and that lane joins the alerts lane at PR G.
- Files two lanes share: `App.tsx` (PR C mounts the alerts, PR F binds the flush) and phase 3's session screen (PRs D, E and F). The second PR to land in either merges `origin/main` before its review.
- Every PR lands under the roadmap's rules: CI green, RoboRev with nothing Medium or higher, /simplify, admin squash merge, Lows in a fast-follow, and decompose after five rounds.
- PR G, the phase's last, carries Release-simulator screenshots of the alert and offline frames against the demo fleet (part 2's Task 18).

---

## PR A: one connection clock, and no reconnect words

### Task 1: One connection clock and one status for every screen

Phase 2 put the status on the Board's toolbar (`connectionStatus` in `src/board/connectionStatus.ts`, phase 2 Task 7). Phase 3 and phase 5 each plan to move its timing into a hook in the same file: `useConnectionStatusText()` for the Session's connection bar (phase 3 Task 15), and `useConnectionStatusLine()` for the sheets' status line (phase 5 Task 2, its ruling 21). If both landed, they are one hook under two names: keep `useConnectionStatusText`, delete `useConnectionStatusLine` and its test file, and point its callers (phase 5's `SheetStatus` and `HubHome`; `grep -rn useConnectionStatusLine mobile-native/src`) at the survivor. What none of them owns is the clock, and it has two traps:
- The time down is tracked where the hook is mounted, so a screen that appears while the phone is offline starts from zero, and a return from the background can count the background as time down and flash "Offline".
- `relativeAge` says "now" under a minute, so the status can read "updated now ago".

This task moves the clock into `ConnectionProvider`, where it sees the foreground, and makes the hook read it. The module stays in `src/board/`: a `src/connectionStatus.ts` would differ from the existing `src/ConnectionStatus.tsx` only in case, which macOS's file system and TypeScript both refuse (TS1149).

**Files:**
- Create: `mobile-native/src/connectionClock.ts`
- Modify: `mobile-native/src/board/connectionStatus.ts` (replace its content as below; `useConnectionStatusText` keeps its name, so phase 2's toolbar and phase 3's bar need no change), `mobile-native/src/sheet/SheetStatus.tsx` and `mobile-native/src/hub/HubHome.tsx` (phase 5 Tasks 2 and 3: call `useConnectionStatusText` where they called `useConnectionStatusLine`), `mobile-native/src/ConnectionProvider.tsx` (the clock, and `downSince` and `lastLiveAt` on the context) and `mobile-native/src/renderNative.testkit.tsx` (`screenConnection` adds `downSince: null` and `lastLiveAt: null`)
- Test: `mobile-native/src/connectionClock.test.ts` and `mobile-native/src/board/connectionStatus.test.ts` (replace the timing tests phase 3 moved there); delete phase 5's `src/board/connectionStatusLine.test.tsx` with its hook, since the new hook tests cover its cases; update any screen test that drove the clock through its own mounted hook to set `downSince` and `lastLiveAt` on its mocked connection instead, and phase 5's `SheetStatus.test.tsx` mock to name `useConnectionStatusText`

**Interfaces:**
- Consumes: phase 2's `connectionStatus(state, fatal, downSince, lastLiveAt, now)`, phase 3's `useConnectionStatusText()` (phase 3 Task 15), phase 5's `useConnectionStatusLine()` (phase 5 Task 2) and `compactDuration` from `src/session/format.ts` (phase 3 Task 3).
- Produces:
  - `interface ConnectionObservation { hubId: string | null; live: boolean; foreground: boolean }`
  - `interface ConnectionTimes { downSince: number | null; lastLiveAt: number | null }`
  - `class ConnectionClock { observe(next: ConnectionObservation, now: number): ConnectionTimes }`
  - `useConnection()` gains `downSince: number | null` and `lastLiveAt: number | null`.
  - From `src/board/connectionStatus.ts`: `RECONNECTING_AFTER_MS`, `OFFLINE_AFTER_MS`, `offlineAge(ms: number): string`, `connectionStatus(state, fatal, downSince, lastLiveAt, now): string | null` (phase 2's signature), `nextStatusChange(state, fatal, downSince, lastLiveAt, now): number | null` and `useConnectionStatusText(): string | null` (phase 3's name).

- [ ] **Step 1: Write the failing clock tests**

```ts
// mobile-native/src/connectionClock.test.ts
import { describe, expect, it } from "vitest";
import { ConnectionClock } from "./connectionClock";

const at = (seconds: number) => 1_000_000 + seconds * 1000;
const hub = (live: boolean, foreground = true, hubId: string | null = "hub-a") => ({ hubId, live, foreground });

describe("the connection clock (spec 14)", () => {
	it("starts down at launch, with nothing live yet", () => {
		const clock = new ConnectionClock();
		expect(clock.observe(hub(false), at(0))).toEqual({ downSince: at(0), lastLiveAt: null });
	});

	it("is not down while live, and remembers when the connection dropped", () => {
		const clock = new ConnectionClock();
		clock.observe(hub(false), at(0));
		expect(clock.observe(hub(true), at(1))).toEqual({ downSince: null, lastLiveAt: null });
		expect(clock.observe(hub(false), at(90))).toEqual({ downSince: at(90), lastLiveAt: at(90) });
	});

	it("keeps counting from the moment it went down", () => {
		const clock = new ConnectionClock();
		clock.observe(hub(true), at(0));
		clock.observe(hub(false), at(10));
		expect(clock.observe(hub(false), at(40))).toEqual({ downSince: at(10), lastLiveAt: at(10) });
	});

	it("never counts time in the background, but the data's age does", () => {
		const clock = new ConnectionClock();
		clock.observe(hub(true), at(0));
		expect(clock.observe(hub(false, false), at(5))).toEqual({ downSince: null, lastLiveAt: at(5) });
		expect(clock.observe(hub(false, true), at(3600))).toEqual({ downSince: at(3600), lastLiveAt: at(5) });
	});

	it("starts over for a different hub", () => {
		const clock = new ConnectionClock();
		clock.observe(hub(true), at(0));
		clock.observe(hub(false), at(10));
		expect(clock.observe(hub(false, true, "hub-b"), at(20))).toEqual({ downSince: at(20), lastLiveAt: null });
	});
});
```

- [ ] **Step 2: Write the failing status tests**

Replace `connectionStatus.test.ts` with this (it keeps phase 2's table and adds the age, the schedule and the hook's clock):

```ts
// mobile-native/src/board/connectionStatus.test.ts
import { act } from "react-test-renderer";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook } from "../renderNative.testkit";

const connection = vi.hoisted(() => ({
	value: {
		state: "ready" as string,
		fatal: false,
		downSince: null as number | null,
		lastLiveAt: null as number | null,
	},
}));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => connection.value }));

import { connectionStatus, nextStatusChange, offlineAge, useConnectionStatusText } from "./connectionStatus";

const T = 1_000_000;

describe("what the connection says (spec 14)", () => {
	it.each([
		["live", "ready", false, null, null, T, null],
		["down 1s", "reconnecting", false, T, T, T + 1_000, null],
		["down 2s", "reconnecting", false, T, T, T + 2_000, "Reconnecting…"],
		["closed 2s", "closed", false, T, T, T + 2_000, "Reconnecting…"],
		["down 30s", "reconnecting", false, T, T, T + 30_000, "Offline · updated 1m ago"],
		["down 3m", "closed", false, T, T, T + 180_000, "Offline · updated 3m ago"],
		["never live this launch", "connecting", false, T, null, T + 31_000, "Offline"],
		["down, then an hour away", "connecting", false, T, T - 3_600_000, T + 31_000, "Offline · updated 1h ago"],
		["no retry can fix it", "closed", true, T, T, T, "Update needed"],
		["no clock yet", "connecting", false, null, null, T, null],
	] as const)("%s", (_name, state, fatal, downSince, lastLiveAt, now, expected) => {
		expect(connectionStatus(state, fatal, downSince, lastLiveAt, now)).toBe(expected);
	});

	it("says the data's age in whole minutes, hours or days, never under 1m", () => {
		expect([0, 59_999, 60_000, 119_999, 3_599_999, 3_600_000, 86_399_999, 86_400_000 * 2].map(offlineAge)).toEqual([
			"1m", "1m", "1m", "1m", "59m", "1h", "23h", "2d",
		]);
	});

	it("changes on its own only at 2 seconds, 30 seconds, then each minute of the data's age", () => {
		expect(nextStatusChange("reconnecting", false, T, T, T + 500)).toBe(T + 2_000);
		expect(nextStatusChange("reconnecting", false, T, T, T + 2_000)).toBe(T + 30_000);
		expect(nextStatusChange("reconnecting", false, T, T - 10_000, T + 30_000)).toBe(T + 50_000);
		expect(nextStatusChange("reconnecting", false, T, null, T + 30_000)).toBeNull();
		expect(nextStatusChange("ready", false, null, null, T)).toBeNull();
		expect(nextStatusChange("closed", true, T, T, T)).toBeNull();
	});
});

describe("useConnectionStatusText", () => {
	beforeEach(() => {
		vi.useFakeTimers();
		vi.setSystemTime(T);
	});
	afterEach(() => vi.useRealTimers());

	it("re-renders exactly when its words change, and runs no clock once live", () => {
		connection.value = { state: "reconnecting", fatal: false, downSince: T, lastLiveAt: T };
		const hook = renderHook(() => useConnectionStatusText());
		act(() => {
			vi.advanceTimersByTime(1_000);
		});
		expect(hook.result.current).toBeNull();
		act(() => {
			vi.advanceTimersByTime(1_000);
		});
		expect(hook.result.current).toBe("Reconnecting…");
		act(() => {
			vi.advanceTimersByTime(28_000);
		});
		expect(hook.result.current).toBe("Offline · updated 1m ago");
		// One tick per act: React re-runs the effect that schedules the next
		// tick only when act flushes.
		act(() => {
			vi.advanceTimersByTime(30_000);
		});
		expect(hook.result.current).toBe("Offline · updated 1m ago");
		act(() => {
			vi.advanceTimersByTime(60_000);
		});
		expect(hook.result.current).toBe("Offline · updated 2m ago");
		connection.value = { state: "ready", fatal: false, downSince: null, lastLiveAt: T };
		hook.rerender();
		expect(hook.result.current).toBeNull();
		expect(vi.getTimerCount()).toBe(0);
		hook.unmount();
	});

	it("says Update needed at once for a close no retry can fix, and runs no clock", () => {
		connection.value = { state: "closed", fatal: true, downSince: T, lastLiveAt: T };
		const hook = renderHook(() => useConnectionStatusText());
		expect(hook.result.current).toBe("Update needed");
		expect(vi.getTimerCount()).toBe(0);
		hook.unmount();
	});
});
```

- [ ] **Step 3: Run them and watch them fail**

Run: `cd mobile-native && npx vitest run src/connectionClock.test.ts src/board/connectionStatus.test.ts`
Expected: FAIL. `./connectionClock` doesn't exist, `offlineAge` and `nextStatusChange` aren't exported, the hook ignores the provider's clock, and the age reads "now" under a minute.

- [ ] **Step 4: Implement the clock**

```ts
// mobile-native/src/connectionClock.ts
// Spec 14's clock for the connection status: how long the app has been in
// front without a live connection, and when its data was last live. Time in
// the background never counts as down: ConnectionProvider closes the
// connection there and dials again on the way back, so a return after an
// hour starts a fresh 2-second grace instead of flashing "Offline". The
// data's age does count the background: it is how old what you see is.

export interface ConnectionObservation {
	hubId: string | null;
	live: boolean;
	foreground: boolean;
}

export interface ConnectionTimes {
	/** When this stretch in front without a live connection began; null while
	 * live or in the background. */
	downSince: number | null;
	/** When this hub's connection last stopped being live; null until it has
	 * been live and dropped since the app launched or the hub was chosen. */
	lastLiveAt: number | null;
}

export class ConnectionClock {
	private last: ConnectionObservation | null = null;
	private times: ConnectionTimes = { downSince: null, lastLiveAt: null };

	observe(next: ConnectionObservation, now: number): ConnectionTimes {
		const last = this.last;
		this.last = next;
		const sameHub = last !== null && last.hubId === next.hubId;
		let { downSince, lastLiveAt } = this.times;
		if (!sameHub) lastLiveAt = null;
		else if (last.live && !next.live) lastLiveAt = now;
		if (next.live || !next.foreground) downSince = null;
		else if (!sameHub || !last.foreground || downSince === null) downSince = now;
		this.times = { downSince, lastLiveAt };
		return this.times;
	}
}
```

- [ ] **Step 5: Implement the status and its hook**

Replace `mobile-native/src/board/connectionStatus.ts` with:

```ts
// mobile-native/src/board/connectionStatus.ts
// What the connection says wherever a screen shows it (spec 14): nothing
// while live, "Reconnecting…" after 2 seconds without a connection,
// "Offline · updated 3m ago" after 30, and "Update needed" for a close no
// retry can fix. The Board's toolbar, the Session's connection bar and the
// sheets' status lines all read it.
import type { ConnectionState } from "@evener/appwire-client";
import { useEffect, useState } from "react";
import { useConnection } from "../ConnectionProvider";
import { compactDuration } from "../session/format";

export const RECONNECTING_AFTER_MS = 2_000;
export const OFFLINE_AFTER_MS = 30_000;
const MINUTE = 60_000;

/** How old the last live data is, in spec 5's compact durations, never under
 * 1m: the status first says it at 30 seconds and then changes once a minute,
 * so a count of seconds would either tick or lie (ruling 11). */
export function offlineAge(ms: number): string {
	return compactDuration(Math.max(MINUTE, ms));
}

export function connectionStatus(
	state: ConnectionState,
	fatal: boolean,
	downSince: number | null,
	lastLiveAt: number | null,
	now: number,
): string | null {
	if (fatal) return "Update needed";
	if (state === "ready" || downSince === null) return null;
	const down = now - downSince;
	if (down < RECONNECTING_AFTER_MS) return null;
	if (down < OFFLINE_AFTER_MS) return "Reconnecting…";
	return lastLiveAt === null ? "Offline" : `Offline · updated ${offlineAge(now - lastLiveAt)} ago`;
}

/** When the status next changes on its own, or null when it won't: at the
 * 2- and 30-second marks, then at each minute of the data's age. */
export function nextStatusChange(
	state: ConnectionState,
	fatal: boolean,
	downSince: number | null,
	lastLiveAt: number | null,
	now: number,
): number | null {
	if (fatal || state === "ready" || downSince === null) return null;
	const down = now - downSince;
	if (down < RECONNECTING_AFTER_MS) return downSince + RECONNECTING_AFTER_MS;
	if (down < OFFLINE_AFTER_MS) return downSince + OFFLINE_AFTER_MS;
	if (lastLiveAt === null) return null;
	return now + MINUTE - ((now - lastLiveAt) % MINUTE);
}

/** The status for the app's connection, re-rendered exactly when its words
 * change, on the provider's one clock (ConnectionClock). A live connection
 * runs no timer. */
export function useConnectionStatusText(): string | null {
	const { state, fatal, downSince, lastLiveAt } = useConnection();
	const [now, setNow] = useState(Date.now);
	// `now` is a dependency though the body doesn't read it: each tick re-runs
	// this effect, which schedules the next one.
	useEffect(() => {
		const current = Date.now();
		const next = nextStatusChange(state, fatal, downSince, lastLiveAt, current);
		if (next === null) return;
		const timer = setTimeout(() => setNow(Date.now()), Math.max(0, next - current));
		return () => clearTimeout(timer);
	}, [state, fatal, downSince, lastLiveAt, now]);
	return connectionStatus(state, fatal, downSince, lastLiveAt, now);
}
```

- [ ] **Step 6: Wire the clock into the provider, and delete the old tracking**

In `mobile-native/src/ConnectionProvider.tsx`:
1. Import `ConnectionClock` and `type ConnectionTimes` from `./connectionClock`.
2. Add `downSince: number | null;` and `lastLiveAt: number | null;` to the `Connection` interface, after `fatal`.
3. After the `useHubConnection(...)` call, add:

```tsx
	// The status clock (spec 14): fed every change in hub, liveness and
	// foreground, read by useConnectionStatusText wherever a status shows.
	const [clock] = useState(() => new ConnectionClock());
	const [times, setTimes] = useState<ConnectionTimes>({ downSince: null, lastLiveAt: null });
	useEffect(() => {
		setTimes(
			clock.observe(
				{ hubId: activeId ?? null, live: visibleState === "ready", foreground },
				Date.now(),
			),
		);
	}, [clock, activeId, visibleState, foreground]);
```

4. Add `downSince: times.downSince` and `lastLiveAt: times.lastLiveAt` to the `useMemo` value after `fatal`, and `times` to its dependency list.

Delete whatever `downSince` and `lastLiveAt` tracking the hook or `BoardToolbar` kept before: the provider's clock replaces it. In `src/renderNative.testkit.tsx`, add `downSince: null, lastLiveAt: null` to `screenConnection`'s literal. A screen test that drove the clock through a mounted hook now sets `downSince` and `lastLiveAt` on its mocked connection and advances fake timers exactly as before.

- [ ] **Step 7: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/connectionClock.test.ts src/board src/session src/sheet src/hub && npm run check`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add mobile-native/src/connectionClock.ts mobile-native/src/connectionClock.test.ts mobile-native/src/ConnectionProvider.tsx mobile-native/src/renderNative.testkit.tsx mobile-native/src/board/connectionStatus.ts mobile-native/src/board/connectionStatus.test.ts <phase 5's hook callers and the test files you updated or deleted>
git commit -m "feat(native): one connection clock for every screen, never counting the background"
```

### Task 2: No text asks you to reconnect or refresh

Principle 2: "Nothing asks you to do what the app can do itself: it reconnects, keeps every screen current and retries its reads on its own, so no screen carries a Reconnect button or asks you to refresh." Phase 5's last PR guards the word "Reconnect" (`src/noReconnect.test.ts`, phase 5 Task 27). Shared messages still ask for a refresh, though: `navigationPages.ts` says "Could not read this navigation page. Refresh to try again.", and `navigationActions.ts` says "Refresh before trying again". This task ends them and widens the guard so they stay ended.

Phase 5 also gives spec 14's version sentence one home, `INCOMPATIBLE_VERSIONS` in `src/connectionRecovery.ts` (its Task 2), and makes it and "Couldn't reach the hub. Check its address, its token and your network." the connection's two failure messages (its Task 27). Phase 2's Board notice and phase 3's connection bar hint still spell the sentence out; they import the constant instead.

**Files:**
- Create: `mobile-native/src/calmCopy.test.ts`
- Delete: `mobile-native/src/noReconnect.test.ts` (phase 5 Task 27), which `calmCopy.test.ts` replaces
- Modify: every production file the audit reports, and the tests that assert the strings you change
- Modify: phase 2's Board notice for a close no retry can fix, and phase 3's `src/session/SessionHeader.tsx` hint: both import `INCOMPATIBLE_VERSIONS`

**Interfaces:**
- Consumes: `INCOMPATIBLE_VERSIONS` from `src/connectionRecovery.ts` (phase 5 Task 2), and phase 5 Task 27's `connectionFailure` messages.

**Requirements:**
1. Spec 14's sentence has one copy: the Board's notice and the Session's bar hint import `INCOMPATIBLE_VERSIONS`. If phase 5's messages aren't on main, give `connectionFailure` them here, with its test.
2. Each string the audit reports is either removed with its control, because the app already does the thing (it reconnects on its own and re-reads on every hub invalidation), or rewritten per spec 5: what went wrong and what to do, where "what to do" is never to reconnect, refresh or pull. Where the app doesn't retry by itself (a save or a change it couldn't confirm), "Try again" is fine.
3. The audit allows one file, `nativeMutationHost.ts`, whose "Reconnect and try again." refusal Task 14 deletes along with the allowance.
4. A test that asserts a rewritten string changes in the same commit, and the commit body lists them.

- [ ] **Step 1: Write the failing test**

```ts
// mobile-native/src/calmCopy.test.ts
// Principle 2 (spec 4): nothing asks a person to do what the app does
// itself. The app reconnects on its own and keeps every screen current, so
// no text a person can read offers Reconnect or asks them to refresh.
//
// It reads string literals, template text and JSX text through the
// TypeScript parser, so comments, identifiers and import paths never trip
// it. Text handed to console.* is for developers, and is skipped.
import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";
import { expect, it } from "vitest";

const SRC = fileURLToPath(new URL(".", import.meta.url));
const FORBIDDEN = /\breconnect\b|\brefresh\b|\bpull down to retry\b/i;
// Task 14 deletes this refusal ("Reconnect and try again.") and this entry.
const ALLOWED = new Set(["nativeMutationHost.ts"]);

function productionFiles(dir: string): string[] {
	return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
		const full = path.join(dir, entry.name);
		if (entry.isDirectory()) return entry.name === "dev" ? [] : productionFiles(full);
		if (!/\.tsx?$/.test(entry.name) || /\.(test|testkit)\.tsx?$/.test(entry.name)) return [];
		return [full];
	});
}

// Whether the node sits anywhere inside a console.* call, nested calls
// included: text handed to console is for developers.
function inConsoleCall(node: ts.Node): boolean {
	for (let parent = node.parent; parent; parent = parent.parent) {
		if (!ts.isCallExpression(parent)) continue;
		const callee = parent.expression;
		if (ts.isPropertyAccessExpression(callee) && ts.isIdentifier(callee.expression) && callee.expression.text === "console")
			return true;
	}
	return false;
}

function readableText(fileName: string, code: string): string[] {
	const source = ts.createSourceFile(
		fileName,
		code,
		ts.ScriptTarget.Latest,
		true,
		fileName.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS,
	);
	const found: string[] = [];
	const visit = (node: ts.Node) => {
		if (ts.isImportDeclaration(node) || ts.isExportDeclaration(node)) return;
		if (inConsoleCall(node)) return;
		if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node) || ts.isJsxText(node)) found.push(node.text);
		else if (ts.isTemplateExpression(node))
			found.push(node.head.text, ...node.templateSpans.map((span) => span.literal.text));
		ts.forEachChild(node, visit);
	};
	visit(source);
	return found;
}

const asks = (fileName: string, code: string) => readableText(fileName, code).filter((text) => FORBIDDEN.test(text));

it("reads the text a person sees, and nothing else", () => {
	expect(asks("a.tsx", `const a = <Action onPress={retry}>Reconnect</Action>;`)).toEqual(["Reconnect"]);
	expect(asks("b.ts", "const b = `Could not load ${name}. Refresh to try again.`;")).toEqual([". Refresh to try again."]);
	expect(asks("c.ts", `const c = { hint: "Pull down to retry" };`)).toEqual(["Pull down to retry"]);
	expect(asks("d.ts", `const d = "Reconnecting…";`)).toEqual([]);
	expect(asks("e.ts", `// Reconnect later\nconst e = reconcileAfterReconnect(refresh);`)).toEqual([]);
	expect(asks("f.ts", `import { refresh } from "./refresh";\nconsole.warn("refresh failed");`)).toEqual([]);
	expect(asks("g.ts", `console.warn(format("refresh failed: %s", error));`)).toEqual([]);
});

it("no text a person can read asks them to reconnect or refresh", () => {
	const offenders = productionFiles(SRC)
		.filter((file) => !ALLOWED.has(path.relative(SRC, file)))
		.flatMap((file) =>
			asks(file, readFileSync(file, "utf8")).map((text) => `${path.relative(SRC, file)}: ${text.trim()}`),
		);
	expect(offenders).toEqual([]);
});
```

- [ ] **Step 2: Run it and read the list**

Run: `cd mobile-native && npx vitest run src/calmCopy.test.ts`
Expected: FAIL. The self-check passes, and the audit lists each offending string by file: this task's worklist, whose length goes in the PR description. On main at `d0c0211be` it held 101 strings in 43 files. Phases 2 to 5 replace the screens behind most of them (phase 3 rewrites `approvalControls.ts`'s). The shared navigation messages are the likeliest to remain: `navigationActions.ts` (6), `navigationPages.ts` (5), `navigationReadback.ts` (4), `pinNavigation.ts` (4), `organizationNavigation.ts` (2), and one each in `navigationReveal.ts` and `sessionDeletionNavigation.ts`.

- [ ] **Step 3: Implement** requirement 1, then work through the list under requirements 2 and 4, one file at a time, running that file's own tests after each.

- [ ] **Step 4: Run the audit, the touched tests and the type check**

Run: `cd mobile-native && npx vitest run src/calmCopy.test.ts <touched test files> && npm run check`
Expected: PASS, with the audit's offender list empty.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/calmCopy.test.ts <every file you changed or deleted>
git commit -m "fix(native): no text asks you to reconnect or refresh"
```

Open PR A: "feat(native): one connection clock, and no text asks you to reconnect (phase 6, PR A)". The description lists the audit's first-run offenders and what became of each.

---

## PRs B, C, D and G: in part 2

The alerts and the phase's screenshots, PRs B, C, D and G (Tasks 3-11, 17 and 18), are in part 2, `docs/superpowers/plans/2026-09-26-iphone-redesign-phase6-attention-resilience-part2.md`, which lands in its own PR. After five review rounds on this PR the alert center (Task 3) still had findings open, so it and everything built on it moved out; part 2 opens with those findings. This part's Goal, Architecture, Global Constraints, Rulings, Questions and Review Focus bind part 2's tasks, and the task numbers continue there.

---

## PR E: your own undelivered messages

Phase 3 puts the outbox inline (its ruling 3): "Sending…", "Couldn't confirm this was sent" with Check, refused messages, and the unconfirmed draft all become ghost bubbles (`src/session/ghosts.ts`). It leaves three things to this phase: a lost send has Check but no Discard, because native storage can't discard one; a message a Stop held before it left the phone doesn't show at all ("waits for phase 6's retry"); and "Sending…" shows whether or not anything can send. This PR closes all three.

### Task 12: Discard and Send now in the phone's outbox

**Files:**
- Modify: `mobile-native/src/mutationOutboxStorage.ts` (two methods after `discardRecovery`, `:459-469`)
- Modify: `mobile-native/src/nativeMutationRuntime.ts` (the `NativeStorage` type, `:41-46`, and two methods after `discardRecovery`, `:236-240`)
- Test: `mobile-native/src/mutationOutboxStorage.test.ts` and `mobile-native/src/nativeMutationRuntime.test.ts`

**Interfaces:**
- Produces:
  - `MutationOutboxSQLite.discardUndelivered(clientMutationId: string, targetRef: string): Promise<boolean>`
  - `MutationOutboxSQLite.releaseCanceled(clientMutationId: string, targetRef: string, barrier: MutationStopBarrier): Promise<boolean>`
  - `NativeMutationRuntime.discardUndelivered(clientMutationId: string, targetRef: string): Promise<boolean>`
  - `NativeMutationRuntime.releaseCanceled(clientMutationId: string, targetRef: string): Promise<boolean>`

  `targetRef` is always the composite key, `nativeMutationTargetKey(hubId, ref)`.

- [ ] **Step 1: Write the failing storage tests** (in `mutationOutboxStorage.test.ts`, after the `discardRecovery` test)

```ts
test("discardUndelivered removes only that target's blocked or Stop-canceled row", async () => {
	const blocked = await storage.enqueueIntent(intent("blocked"));
	const waiting = await storage.enqueueIntent(intent("waiting"));
	await storage.markAttempted(blocked.clientMutationId);
	await storage.markUnknown(blocked.clientMutationId, "blockedUnknown");
	const held = await storage.enqueueIntent(intent("held", "local:thread-2"));
	await storage.enqueueInterruptAndCancel(interruptIntent("local:thread-2"));

	await expect(storage.discardUndelivered(blocked.clientMutationId, "local:thread-2")).resolves.toBe(false);
	await expect(storage.discardUndelivered(waiting.clientMutationId, TARGET)).resolves.toBe(false);
	await expect(storage.discardUndelivered(blocked.clientMutationId, TARGET)).resolves.toBe(true);
	await expect(storage.discardUndelivered(held.clientMutationId, "local:thread-2")).resolves.toBe(true);

	expect(rawRow("mutation_outbox", blocked.clientMutationId)).toBeUndefined();
	expect(rawRow("mutation_outbox", held.clientMutationId)).toBeUndefined();
	expect(rawRow("mutation_outbox", waiting.clientMutationId)).toMatchObject({ state: "submitting" });
	await expect(storage.nextDispatchable(TARGET)).resolves.toMatchObject({ clientMutationId: waiting.clientMutationId });
});

test("releaseCanceled sends a held row after the Stop that held it, and never past a newer Stop", async () => {
	const held = await storage.enqueueIntent(intent("held"));
	const stop = await storage.enqueueInterruptAndCancel(interruptIntent());
	const barrier = { stopEpoch: await storage.readStopEpoch(TARGET) };

	await expect(storage.releaseCanceled(held.clientMutationId, "local:thread-2", barrier)).resolves.toBe(false);
	await expect(storage.releaseCanceled(stop.clientMutationId, TARGET, barrier)).resolves.toBe(false);
	await expect(storage.releaseCanceled(held.clientMutationId, TARGET, barrier)).resolves.toBe(true);

	const released = await storage.getOutbox(held.clientMutationId);
	expect(released).toMatchObject({ state: "submitting", attempted: false });
	expect(released?.intentSequence).toBeGreaterThan(stop.intentSequence);
	await expect(storage.nextDispatchable(TARGET)).resolves.toMatchObject({ clientMutationId: stop.clientMutationId });

	const again = await storage.enqueueIntent(intent("held again"));
	await storage.enqueueInterruptAndCancel(interruptIntent());
	await expect(storage.releaseCanceled(again.clientMutationId, TARGET, barrier)).resolves.toBe(false);
	expect((await storage.getOutbox(again.clientMutationId))?.state).toBe("canceled");
});
```

- [ ] **Step 2: Write the failing runtime tests** (in `nativeMutationRuntime.test.ts`, after "submit durably records while disconnected and dispatches after readiness")

```ts
test("Discard on a message whose delivery couldn't be confirmed lets the next one send", async () => {
	let nextId = 0;
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => `mutation-${++nextId}`,
	});
	const client = new FakeClient("ready");
	client.on("turn/start", appliedReceipt);
	await registerAndStart(runtime, client);
	const targetKey = nativeMutationTargetKey("hub-1", "ref-1");
	await runtime.submit(request("send"));
	await runtime.storage.markAttempted("mutation-1");
	const lease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	await runtime.reconcileAuthoritativeRead(lease!, readResponse("ref-1", { authoritative: false }));
	expect((await runtime.storage.getOutbox("mutation-1"))?.state).toBe("blockedUnknown");
	await runtime.submit(request("send"));
	expect(await runtime.storage.getOutbox("mutation-2")).toMatchObject({ state: "submitting", attempted: false });

	const changes: string[][] = [];
	runtime.subscribeStorage((targetRefs) => changes.push([...targetRefs]));
	await expect(runtime.discardUndelivered("mutation-1", targetKey)).resolves.toBe(true);
	expect(changes[0]).toEqual([targetKey]);
	await vi.waitFor(() =>
		expect(client.calls.map((call) => (call.params as { clientMutationId: string }).clientMutationId)).toEqual([
			"mutation-2",
		]),
	);
	await runtime.stop();
});

test("Send now on a message a Stop held sends it after that Stop", async () => {
	let nextId = 0;
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => `mutation-${++nextId}`,
	});
	const client = new FakeClient("connecting");
	client.on("turn/start", appliedReceipt);
	client.on("turn/interrupt", appliedReceipt);
	await registerAndStart(runtime, client);
	const targetKey = nativeMutationTargetKey("hub-1", "ref-1");
	await runtime.submit(request("send"));
	await runtime.submit(request("interrupt"));
	expect((await runtime.storage.getOutbox("mutation-1"))?.state).toBe("canceled");

	await expect(runtime.releaseCanceled("mutation-1", targetKey)).resolves.toBe(true);
	client.emitStateChange("ready");
	const lease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	await runtime.reconcileAuthoritativeRead(lease!, readResponse("ref-1"));

	await vi.waitFor(() =>
		expect(
			client.calls.map((call) => [call.method, (call.params as { clientMutationId: string }).clientMutationId]),
		).toEqual([
			["turn/interrupt", "mutation-2"],
			["turn/start", "mutation-1"],
		]),
	);
	await runtime.stop();
});
```

- [ ] **Step 3: Run them and watch them fail**

Run: `cd mobile-native && npx vitest run src/mutationOutboxStorage.test.ts src/nativeMutationRuntime.test.ts`
Expected: FAIL: `storage.discardUndelivered is not a function` (and the same for `releaseCanceled` and the runtime's methods).

- [ ] **Step 4: Implement the storage methods** (in `mutationOutboxStorage.ts`, after `discardRecovery`)

```ts
	// The user's Discard on their own message that a Stop held before it left
	// the phone ("canceled") or whose delivery couldn't be confirmed
	// ("blockedUnknown"): exactly that row, for exactly that composite target.
	// A submitting row is still owed to the daemon and is never discarded.
	async discardUndelivered(clientMutationId: string, targetRef: string): Promise<boolean> {
		return (
			changedRows(
				this.db.runSync(
					`DELETE FROM ${TABLES.outbox}
					 WHERE client_mutation_id = ? AND target_ref = ? AND state IN ('blockedUnknown', 'canceled')`,
					clientMutationId,
					targetRef,
				),
			) > 0
		);
	}

	// The one release of a canceled row: the user's Send now. It moves the row
	// to the end of its target's line, with a fresh sequence, so it can never
	// dispatch ahead of the interrupt whose Stop canceled it (the row never
	// left the phone, so moving it is safe). The barrier is the press's
	// stop-epoch capture: a Stop that committed after it outranks the press,
	// and the row stays canceled. The web adapter's releaseCanceled, plus the
	// move.
	async releaseCanceled(clientMutationId: string, targetRef: string, barrier: MutationStopBarrier): Promise<boolean> {
		return this.transaction("mutation_outbox_release_canceled", () => {
			const record = this.get<MutationOutboxRecord<A>>(TABLES.outbox, clientMutationId);
			if (record?.state !== "canceled" || record.targetRef !== targetRef) return false;
			if (this.stopEpochOf(targetRef) > barrier.stopEpoch) return false;
			this.db.runSync(
				`UPDATE ${TABLES.outbox} SET state = 'submitting', intent_sequence = ? WHERE client_mutation_id = ?`,
				this.allocateSequence(targetRef),
				clientMutationId,
			);
			return true;
		});
	}
```

- [ ] **Step 5: Implement the runtime methods**

In `nativeMutationRuntime.ts`, add to the `NativeStorage` type:

```ts
	discardUndelivered(clientMutationId: string, targetRef: string): Promise<boolean>;
	releaseCanceled(clientMutationId: string, targetRef: string, barrier: MutationStopBarrier): Promise<boolean>;
```

and after `discardRecovery`:

```ts
	// Discard on your own message a Stop held or whose delivery couldn't be
	// confirmed. Removing a blocked head lets the target's next message go,
	// so the target is dispatched again (the dispatcher still checks its gate).
	async discardUndelivered(clientMutationId: string, targetRef: string): Promise<boolean> {
		const discarded = await this.storage.discardUndelivered(clientMutationId, targetRef);
		this.#notifyStorageChange([targetRef]);
		if (discarded) void this.#dispatcher.dispatchTargets([targetRef]).catch(() => undefined);
		return discarded;
	}

	// Send now on your own message a Stop held before it left the phone. The
	// stop epoch is read at the press, before any other await, like submit's
	// barrier: a Stop landing after the press keeps the row held.
	async releaseCanceled(clientMutationId: string, targetRef: string): Promise<boolean> {
		const barrier: MutationStopBarrier = { stopEpoch: await this.storage.readStopEpoch(targetRef) };
		await this.start();
		const released = await this.storage.releaseCanceled(clientMutationId, targetRef, barrier);
		this.#notifyStorageChange([targetRef]);
		if (released) void this.#dispatcher.dispatchTargets([targetRef]).catch(() => undefined);
		return released;
	}
```

- [ ] **Step 6: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/mutationOutboxStorage.test.ts src/nativeMutationRuntime.test.ts && npm run check`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add mobile-native/src/mutationOutboxStorage.ts mobile-native/src/mutationOutboxStorage.test.ts mobile-native/src/nativeMutationRuntime.ts mobile-native/src/nativeMutationRuntime.test.ts
git commit -m "feat(native): Discard and Send now for your own undelivered messages"
```

### Task 13: The ghosts know the connection, a lost send can be discarded, and a held message comes back

**Files:**
- Modify: `mobile-native/src/session/ghosts.ts` (phase 3 Task 7) and its test
- Modify: phase 3 Task 8's ghost wiring: the session screen's ghost action handler and every `ghosts(...)` call (the screen and `QueueSheet.tsx`)
- Test: `mobile-native/src/session/ghosts.test.ts` and phase 3's `mobile-native/src/ConversationScreen.recovery.test.tsx`

**Interfaces:**
- Consumes: `NativeMutationRuntime.discardUndelivered` and `releaseCanceled` (Task 12); `nativeMutationTargetKey`.
- Produces: `ghosts(session, pending, unconfirmedDraft, recovery, connected: boolean): Ghost[]`. Every other export of `ghosts.ts` is unchanged.

**Requirements (spec 8.5, 14; rulings 14 to 16):**
1. A message this phone hasn't handed to the hub yet (`state: "submitting"`, and an accepted send) reads "Sending…" while connected and "Will send when you're back online" while not.
2. "Couldn't confirm this was sent" offers Check only while connected, and always Discard: on a lost send (`blockedUnknown`), on the unconfirmed draft and on a record the phone couldn't place (`orphaned`).
3. A message a Stop held before it left the phone (`state: "canceled"`) shows as a held ghost: "Held · you stopped this turn", with "Send now" and "Cancel" as buttons. Another client's rows stay out. This is a record on the phone, which the hub never saw; the queue a Stop parks on the hub stays phase 3's (`queueGhosts`, through `isQueueParked`).
4. In the action handler, a ghost whose origin is `{ kind: "pending" }`:
   - Discard and Cancel call `getNativeMutationRuntime().discardUndelivered(clientMutationId, nativeMutationTargetKey(hubId, ref))`.
   - Send now calls `getNativeMutationRuntime().releaseCanceled(clientMutationId, nativeMutationTargetKey(hubId, ref))`.
   - A `false` result shows no error: the row changed underneath the press, and the ghosts re-render from storage.

- [ ] **Step 1: Write the failing tests**

Update every `ghosts(...)` call in phase 3's `ghosts.test.ts` to pass `true` as the new last argument, and change the expected buttons in its test "asks you to check a send whose answer was lost" from `["check"]` to `["check", "discard"]`. Replace its test "leaves out another client's rows and a row Stop canceled before it left the phone" with the second test below, and add the rest:

```ts
describe("the phone's own outbox (phase 6)", () => {
	it("says a message will send when you're back online while offline", () => {
		const [ghost] = ghosts(session("idle"), [pending()], null, [], false);
		expect(ghost).toMatchObject({ state: "sending", caption: "Will send when you're back online", buttons: [] });
		expect(ghosts(session("idle"), [pending()], null, [], true)[0]?.caption).toBe("Sending…");
	});

	it("leaves out another client's rows, and holds a message a Stop kept on the phone", () => {
		const list = ghosts(
			session("idle"),
			[pending({ id: "a", fromThisClient: false }), pending({ id: "b", state: "canceled", text: "held back" })],
			null,
			[],
			false,
		);
		expect(list).toEqual([
			{
				key: "pending:b",
				state: "held",
				text: "held back",
				caption: "Held · you stopped this turn",
				buttons: ["sendNow", "cancel"],
				menu: [],
				origin: { kind: "pending", clientMutationId: "b" },
			},
		]);
	});

	it("offers Check only while connected, and Discard on everything it couldn't confirm", () => {
		const lost = pending({ state: "blockedUnknown" });
		expect(ghosts(session("idle"), [lost], null, [], true)[0]?.buttons).toEqual(["check", "discard"]);
		expect(ghosts(session("idle"), [lost], null, [], false)[0]?.buttons).toEqual(["discard"]);
		expect(ghosts(session("idle"), [], "maybe sent", [], false)[0]?.buttons).toEqual(["discard"]);
		expect(ghosts(session("idle"), [], null, [row({ status: "orphaned" })], false)[0]?.buttons).toEqual(["discard"]);
	});
});
```

In `ConversationScreen.recovery.test.tsx`, add a case on its harness: a durable `canceled` row for the screen's target renders "Held · you stopped this turn"; pressing "Cancel" removes the row from storage; a second canceled row's "Send now" makes the client receive its `turn/start` once the session reads idle.

- [ ] **Step 2: Run them and watch them fail**

Run: `cd mobile-native && npx vitest run src/session/ghosts.test.ts src/ConversationScreen.recovery.test.tsx`
Expected: FAIL: offline sends still read "Sending…", Check shows offline, and the canceled row is missing.

- [ ] **Step 3: Implement**

In `ghosts.ts`, add the offline caption beside `CAPTIONS`:

```ts
/** A message the phone holds until it can send it (ruling 14). */
const WAITING_TO_SEND = "Will send when you're back online";
```

and replace `ghosts` with:

```ts
export function ghosts(
	session: GhostSource,
	pending: readonly PendingTurnEntry[] | null | undefined,
	unconfirmedDraft: string | null,
	recovery: readonly RecoveryGhostRow[],
	connected: boolean,
): Ghost[] {
	// Another client's rows aren't yours to watch.
	const own = (pending ?? []).filter((entry) => entry.fromThisClient);
	const steering = (entry: PendingTurnEntry) =>
		STEERS.has(entry.method) && (entry.state === "accepted" || entry.state === "claimed");
	// Check needs the hub; Discard never does (spec principle 2: a control
	// shows only when it can act).
	const confirmButtons: GhostAction[] = connected ? ["check", "discard"] : ["discard"];
	const out: Ghost[] = own.filter(steering).map((entry) => pendingGhost(entry, "steering", []));
	out.push(...queueGhosts(session));
	for (const entry of own) {
		if (steering(entry)) continue;
		if (entry.state === "canceled") out.push(pendingGhost(entry, "held", ["sendNow", "cancel"]));
		else if (entry.state === "blockedUnknown") out.push(pendingGhost(entry, "unconfirmed", confirmButtons));
		else {
			const ghost = pendingGhost(entry, "sending", []);
			out.push(connected ? ghost : { ...ghost, caption: WAITING_TO_SEND });
		}
	}
	if (unconfirmedDraft !== null)
		out.push({
			key: "draft:unconfirmed",
			state: "unconfirmed",
			text: unconfirmedDraft,
			caption: CAPTIONS.unconfirmed,
			buttons: confirmButtons,
			menu: ["edit"],
			origin: { kind: "draft" },
		});
	for (const row of recovery) out.push(recoveryGhost(row, confirmButtons));
	return out;
}
```

and give `recoveryGhost` the buttons for a record it couldn't place:

```ts
function recoveryGhost(row: RecoveryGhostRow, confirmButtons: GhostAction[]): Ghost {
	const refused = row.status === "rejected";
	const canEdit = row.actions.includes("restore");
	const buttons: GhostAction[] = refused ? (canEdit ? ["edit", "discard"] : ["discard"]) : confirmButtons;
```

keeping the rest of `recoveryGhost` as it is. Then pass `connected` at every call of `ghosts`, and add requirement 4's branches to the action handler.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/session src/ConversationScreen.recovery.test.tsx src/ConversationScreen.send.test.tsx && npm run check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/session/ghosts.ts mobile-native/src/session/ghosts.test.ts mobile-native/src/ConversationScreen.recovery.test.tsx <the screen and QueueSheet files>
git commit -m "feat(native): ghosts say when a message waits for the connection, and held messages come back"
```

Open PR E: "feat(native): Discard and Send now for your own undelivered messages (phase 6, PR E)".

---

## PR F: Send while offline

Phase 3 disables Send while offline (its ruling 4): the durable submitter refuses without a live host (`createDurableSubmitter`, `nativeMutationHost.ts:44-55`), and the store only admits a message through a bound service (`captureOperationBinding`, `mobile/src/state/conversation.ts:1177-1195`). Spec 8.5 says Send "holds the message in the outbox and sends it when the connection returns". The runtime already keeps a message admitted while disconnected and sends it once its target is registered and read (the test "submit durably records while disconnected and dispatches after readiness", `nativeMutationRuntime.test.ts:800-828`). This PR admits it, and sends what you left in sessions you've since closed.

### Task 14: Send while offline

**Files:**
- Modify: `mobile-native/src/session/sendAction.ts` and `sendAction.test.ts` (phase 3 Task 3)
- Create: `mobile-native/src/outbox/offlineSend.ts` and `mobile-native/src/outbox/offlineSend.test.ts`
- Modify: the session screen's Send and `sendAnswers` wiring (phase 3 Tasks 5 and 10, in `mobile-native/src/screens.tsx`)
- Modify: `mobile-native/src/nativeMutationHost.ts` (`NATIVE_MUTATION_HOST_UNAVAILABLE` loses "Reconnect") and `mobile-native/src/calmCopy.test.ts` (the allowance for it goes)
- Test: `mobile-native/src/ConversationScreen.offline.test.tsx` (create, on the harness of `ConversationScreen.send.test.tsx`)

**Interfaces:**
- Consumes: phase 3's `sendAction`, `SendSource`, `SendAction`, `composerPlaceholder` and `sendLabel`; `getNativeMutationRuntime().submit` and `nativeMutationTargetKey`; `buildComposerInput` from `@evener/appwire-client`.
- Produces:
  - `sendAction(conversation, pendingMutations, connected)` keeps its signature. Offline it returns the action the message will take when it arrives, instead of `"none"`.
  - `sendLabel(action, questionPending, connected = true)`.
  - `interface OfflineTarget { hubId: string; ref: string; threadId: string; instanceId?: string | null }` and `offlineRequest(target: OfflineTarget, action: "send" | "queue" | "resume", input: InputItem[]): ConversationMutationRequest`, from `src/outbox/offlineSend.ts`.

**Requirements (spec 8.5 and 14; rulings 12 to 14):**
1. **Routing offline.** A message waits in the outbox, and by the time it arrives another turn may have started, where `turn/start` is refused. So offline, Send queues whenever the harness can: the daemon runs a queued message at once on an idle session (`clientMutationQueue` wakes it, `agent/session_client_mutation_queue.go:107-190`) and holds it behind a running one. A shut-down session resumes on a send. A paused session, one that needs a restart, and one whose harness takes neither stay `"none"`.
2. **What Send admits offline.** When the screen isn't connected and the phone has read the session since launch (`store.getState().conversation` is set), Send is enabled on the same draft conditions as online, minus `ready`. It runs `document.submit(async (text, images) => …)`, which submits `offlineRequest({ hubId, ref, threadId, instanceId }, action, buildComposerInput(text, images))` to `getNativeMutationRuntime()`, and returns `true`. Its haptic comes with part 2's Task 6. The ghost shows at once through the durable pending rows the screen already follows (phase 3 Task 8).
3. **Never read.** A session this phone hasn't read since launch (`store.getState().conversation` is unset) has no instance to fence with (ruling 12), so its Send stays disabled offline, and the draft stays. `sendAction` is never asked there. A thread whose hub status is `notLoaded` is a shut-down session the phone did read, and routes to `"resume"`.
4. **Words.** The placeholder is `composerPlaceholder` of the connected routing, so it describes the session rather than the outbox. Send's accessibility label offline is "Send when you're back online", or "Send answer when you're back online" while a question is pending.
5. **Answers.** The dock's "Send answer" admits offline through the same `offlineRequest` path, with the composed text as one text input, and marks the batch sent as `sendAnswers` does online.
6. **The refusal left online.** `NATIVE_MUTATION_HOST_UNAVAILABLE` becomes "Couldn't keep this message on the phone. Try again." With offline sends going straight to the runtime, it shows only when the screen has no live host while connected: the host couldn't be created (the mutations database or the client binding, `screens.tsx:1038-1052`), or the connection dropped in the same instant. Trying again covers both. Keep the host guard in `createDurableSubmitter`, and remove the constant's allowance from `calmCopy.test.ts`.

- [ ] **Step 1: Write the failing tests**

In phase 3's `sendAction.test.ts`, delete the first line of "does nothing offline, while paused, or where the harness can't take it" (`sendAction(session("idle"), [], false)`), rename the test "does nothing while paused, or where the harness can't take it", and add:

```ts
describe("Send while offline (phase 6)", () => {
	it("queues whenever the harness can, so a turn that started meanwhile never refuses it", () => {
		expect(sendAction(session("idle"), [], false)).toBe("queue");
		expect(sendAction(session("active"), [], false)).toBe("queue");
		expect(sendAction(session("awaiting"), [], false)).toBe("queue");
	});

	it("sends to resume a shut-down session, or where the harness can't queue", () => {
		expect(sendAction(session("notLoaded"), [], false)).toBe("resume");
		expect(sendAction(session("idle", { capabilities: caps({ queue: false }) }), [], false)).toBe("send");
	});

	it("does nothing for a paused session, one that needs a restart, or one that takes nothing", () => {
		expect(sendAction(session("idle", { resumeRequired: true }), [], false)).toBe("none");
		expect(sendAction(session("restartRequired"), [], false)).toBe("none");
		expect(sendAction(session("ended", { capabilities: caps({ send: false }) }), [], false)).toBe("none");
		expect(sendAction(session("idle", { capabilities: caps({ queue: false, send: false }) }), [], false)).toBe("none");
	});

	it("says Send waits for the connection", () => {
		expect(sendLabel("queue", false, false)).toBe("Send when you're back online");
		expect(sendLabel("queue", true, false)).toBe("Send answer when you're back online");
		expect(sendLabel("queue", false)).toBe("Queue message");
	});
});
```

```ts
// mobile-native/src/outbox/offlineSend.test.ts
import { expect, it } from "vitest";
import { offlineRequest } from "./offlineSend";

const target = { hubId: "hub-1", ref: "local:thread-1", threadId: "thread-1", instanceId: "instance-7" };
const input = [{ type: "text" as const, text: "sent on the train" }];

it("fences a message to the session instance the phone last saw", () => {
	expect(offlineRequest(target, "queue", input)).toEqual({
		kind: "queue",
		hubId: "hub-1",
		targetRef: "local:thread-1",
		threadId: "thread-1",
		instanceId: "instance-7",
		input,
	});
});

it("sends to start or resume, and falls back to the thread id for a session with no instance", () => {
	expect(offlineRequest(target, "send", input).kind).toBe("send");
	expect(offlineRequest({ ...target, instanceId: undefined }, "resume", input)).toMatchObject({
		kind: "send",
		instanceId: "thread-1",
	});
});
```

`ConversationScreen.offline.test.tsx` mounts the real screen on the harness of phase 3's `ConversationScreen.send.test.tsx`, with the real durable runtime over `openSqliteSyncDouble()` (mock `expo-sqlite`'s `openDatabaseSync` to return that port). It covers:
- the session loads connected; the connection drops (the mocked connection reports `"reconnecting"`); typing "sent on the train" and pressing Send ("Send when you're back online") leaves the composer empty, stores one `turn/queue` record for `nativeMutationTargetKey("hub-1", ref)`, and shows a ghost reading "Will send when you're back online";
- the connection returns (`"ready"`, the same client): the client receives that `turn/queue` exactly once, and the ghost goes when the read reflects it;
- a screen that never read its session keeps Send disabled while offline, and the typed draft stays.

- [ ] **Step 2: Run them and watch them fail**

Run: `cd mobile-native && npx vitest run src/session/sendAction.test.ts src/outbox/offlineSend.test.ts src/ConversationScreen.offline.test.tsx`
Expected: FAIL: offline routing is `"none"`, `./offlineSend` doesn't exist, and Send is disabled offline.

- [ ] **Step 3: Implement the routing and the request**

In `sendAction.ts`, replace the first line of `sendAction` and add the offline rule below it:

```ts
export function sendAction(
	conversation: SendSource,
	pendingMutations: readonly PendingTurnEntry[] | null | undefined,
	connected: boolean,
): SendAction {
	if (conversation.resumeRequired) return "none";
	if (!connected) return offlineAction(conversation);
	const status = conversation.status.type;
	// … the rest of phase 3's body, unchanged …
}

/** Offline, a message waits in the phone's outbox (spec 8.5), and by the
 * time it arrives another turn may have started, which refuses a turn/start.
 * So it queues whenever the harness can: the daemon runs a queued message at
 * once on an idle session and holds it behind a running one
 * (agent/session_client_mutation_queue.go, clientMutationQueue). A shut-down
 * session resumes on a send. */
function offlineAction(conversation: SendSource): SendAction {
	const status = conversation.status.type;
	if (status === "restartRequired") return "none";
	if (ENDED.has(status)) return conversation.capabilities.send ? "resume" : "none";
	if (conversation.capabilities.queue) return "queue";
	return conversation.capabilities.send ? "send" : "none";
}
```

and give `sendLabel` the connection:

```ts
export function sendLabel(action: SendAction, questionPending: boolean, connected = true): string {
	if (!connected) return questionPending ? "Send answer when you're back online" : "Send when you're back online";
	if (questionPending) return "Send answer";
	if (action === "queue") return "Queue message";
	if (action === "resume") return "Send and resume";
	return "Send";
}
```

```ts
// mobile-native/src/outbox/offlineSend.ts
// The durable request for a message sent while offline (spec 8.5, ruling
// 12). It is fenced to the session instance the phone last saw, the same
// fence an online send carries (conversation.ts submitMutation), so a session
// that restarted meanwhile refuses it and its ghost says so. A thread with no
// instance id is fenced with its thread id, as the online send is and as the
// hub stamps a daemon's thread (appsource/local_daemon.go).
import type { InputItem } from "@evener/appwire-client";
import type { ConversationMutationRequest } from "../../../mobile/src/state/conversationMutation";

export interface OfflineTarget {
	hubId: string;
	ref: string;
	threadId: string;
	instanceId?: string | null;
}

export function offlineRequest(
	target: OfflineTarget,
	action: "send" | "queue" | "resume",
	input: InputItem[],
): ConversationMutationRequest {
	return {
		kind: action === "queue" ? "queue" : "send",
		hubId: target.hubId,
		targetRef: target.ref,
		threadId: target.threadId,
		instanceId: target.instanceId ?? target.threadId,
		input,
	};
}
```

- [ ] **Step 4: Wire the screen** to requirements 2 to 6: the offline branch of Send and of `sendAnswers`, the enabled rule, the placeholder and the label. Keep the online path exactly as phase 3 built it.

- [ ] **Step 5: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/session src/outbox src/ConversationScreen.offline.test.tsx src/ConversationScreen.send.test.tsx src/calmCopy.test.ts && npm run check`
Expected: PASS. Build Release in the simulator, turn the Mac's network off, send, turn it on, and watch the ghost go from "Will send when you're back online" to the transcript.

- [ ] **Step 6: Commit**

```bash
git add mobile-native/src/session/sendAction.ts mobile-native/src/session/sendAction.test.ts mobile-native/src/outbox/offlineSend.ts mobile-native/src/outbox/offlineSend.test.ts mobile-native/src/ConversationScreen.offline.test.tsx mobile-native/src/screens.tsx mobile-native/src/nativeMutationHost.ts mobile-native/src/calmCopy.test.ts
git commit -m "feat(native): Send while offline keeps the message and sends it when the connection returns"
```

### Task 15: What you left behind sends itself

A message sent offline in a session you then left waits in the outbox with no screen to send it: only an open session screen registers its target (`createNativeMutationHost`, `nativeMutationHost.ts:57-108`). So does anything admitted for a session no screen has open, such as phase 2's Board Stop (`stopSession`, which submits through the runtime). The web sends every target with waiting records on each ready connection (`handleReady`, `cmd/evener-hub/frontend/src/stores/threads.ts:2702-2767`). This task does the same, and also looks again whenever a record lands for a target nobody holds. It reuses phase 4's `settleTarget` (phase 4 Task 2), which releases a registered target with a read that leaves the connection's subscription alone. That also covers a session screen under the Reader or a subagent: it keeps its target but doesn't read while covered (phase 4 Task 2), so on a ready connection the flush settles its target for it when something on it waits to be sent.

A target nobody holds is settled whatever its records hold, as `handleReady` reads every stored target. The read is how the phone confirms a send whose answer was lost: a record the hub's read reflects is settled (`reconcileIdentities`), and one the hub proves it never received goes back to the outbox to send (`restoreProvenAbsent`, `mutationOutboxStorage.ts:492`). Spec 14 shows "Couldn't confirm this was sent" only when "delivery can't be confirmed after reconnecting".

**Files:**
- Create: `mobile-native/src/outbox/outboxFlush.ts`, `mobile-native/src/outbox/nativeOutboxFlush.ts` and `mobile-native/src/outbox/outboxFlush.test.ts`
- Modify: `mobile-native/src/nativeMutationRuntime.ts` (`hasTarget`, beside `registerTarget`)
- Modify: `mobile-native/App.tsx` (bind the flush to the connection) and the session screen's durable-host effect (`screens.tsx:1035-1059`: flush after its host lets go)

**Interfaces:**
- Consumes: `NativeMutationRuntime.settleTarget(hubId, targetRef, client)` (phase 4 Task 2), `registerTarget`, `start`, `subscribeStorage`, `storage.listTargetRefs` and `storage.listOutbox`.
- Produces:
  - `NativeMutationRuntime.hasTarget(hubId: string, targetRef: string): boolean`
  - `parseTargetKey(key: string): { hubId: string; ref: string } | null`
  - `class OutboxFlush`: constructor `(runtime: () => FlushRuntime)`; `bind(hubId: string | null, client: AppwireClientLike | null)`, `flush(): Promise<void>`, `dispose()`
  - `outboxFlush`, the app's one instance, from `nativeOutboxFlush.ts`

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/outbox/outboxFlush.test.ts
import type { ThreadReadResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { afterEach, expect, it, vi } from "vitest";
import { NativeMutationRuntime, type NativeMutationRequest } from "../nativeMutationRuntime";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "../sqliteSync.testkit";
import { OutboxFlush, parseTargetKey } from "./outboxFlush";

vi.mock("expo-sqlite", () => ({ openDatabaseSync: vi.fn() }));
vi.mock("expo-crypto", () => ({ randomUUID: () => "test-uuid", getRandomValues: (array: Uint8Array) => array }));

let database: SqliteDoubleDatabase | undefined;
afterEach(() => {
	database?.close();
	database = undefined;
});

function runtime(): NativeMutationRuntime {
	const opened = openSqliteSyncDouble();
	database = opened.database;
	let next = 0;
	return new NativeMutationRuntime(opened.port, { createMutationId: () => `mutation-${++next}` });
}

const message = (over: Partial<NativeMutationRequest> = {}): NativeMutationRequest => ({
	kind: "send",
	hubId: "hub-1",
	targetRef: "ref-1",
	threadId: "thread-1",
	instanceId: "instance-1",
	input: [{ type: "text", text: "sent on the train" }],
	...over,
});

function applied(params: unknown): never {
	const { clientMutationId } = params as { clientMutationId: string };
	return {
		receipt: { clientMutationId, disposition: "applied", threadId: "thread-1", turnId: "turn-1", projectionState: "pending" },
		turn: { id: "turn-1" },
	} as never;
}

function read(ref: string): ThreadReadResponse {
	return {
		thread: {
			id: "thread-1",
			status: { type: "idle" },
			evener: { ref, capabilities: {}, queue: { clientMutationIds: [] }, mutationStateAuthoritative: true },
		},
	} as unknown as ThreadReadResponse;
}

function methods(client: FakeClient): string[] {
	return client.calls.map((call) => call.method);
}

it("sends a message kept while offline once the connection returns, even after a relaunch", async () => {
	const opened = openSqliteSyncDouble();
	database = opened.database;
	const beforeRelaunch = new NativeMutationRuntime(opened.port, { createMutationId: () => "mutation-1" });
	await beforeRelaunch.submit(message());
	await beforeRelaunch.stop();
	// A fresh process: the runtime is new and not started; the message is on disk.
	const outbox = new NativeMutationRuntime(opened.port, { createMutationId: () => "mutation-2" });
	const client = new FakeClient("ready");
	client.on("thread/read", () => read("ref-1"));
	client.on("turn/start", applied);
	const flush = new OutboxFlush(() => outbox);

	flush.bind("hub-1", client);

	await vi.waitFor(() => expect(methods(client)).toEqual(["thread/read", "turn/start"]));
	expect(client.calls[0]?.params).toEqual({ ref: "ref-1", includeTurns: true, itemsView: "fragment", itemLimit: 40 });
	expect((client.calls[1]?.params as { clientMutationId: string }).clientMutationId).toBe("mutation-1");
	await vi.waitFor(() => expect(outbox.hasTarget("hub-1", "ref-1")).toBe(false));
	flush.dispose();
	await outbox.stop();
});

it("never takes a session a screen holds", async () => {
	const outbox = runtime();
	const screen = new FakeClient("ready");
	outbox.registerTarget("hub-1", "ref-1", screen);
	await outbox.submit(message());
	const client = new FakeClient("ready");
	const flush = new OutboxFlush(() => outbox);

	flush.bind("hub-1", client);
	await flush.flush();

	expect(methods(client)).toEqual([]);
	flush.dispose();
	await outbox.stop();
});

it("settles for a session screen that isn't reading, and leaves the target the screen's", async () => {
	const outbox = runtime();
	const client = new FakeClient("ready");
	client.on("thread/read", () => read("ref-1"));
	client.on("turn/start", applied);
	// A session screen under the Reader: registered with the connection's
	// client, blocked since the reconnect, and not reading while blurred.
	const unregister = outbox.registerTarget("hub-1", "ref-1", client);
	await outbox.submit(message());
	const flush = new OutboxFlush(() => outbox);

	flush.bind("hub-1", client);

	await vi.waitFor(() => expect(methods(client)).toEqual(["thread/read", "turn/start"]));
	flush.dispose();
	expect(outbox.hasTarget("hub-1", "ref-1")).toBe(true);
	unregister();
	await outbox.stop();
});

it("reads nothing for a session screen with nothing waiting to send", async () => {
	const outbox = runtime();
	const client = new FakeClient("ready");
	const unregister = outbox.registerTarget("hub-1", "ref-1", client);
	await outbox.submit(message());
	// Its one message couldn't be confirmed, so it waits for you in its session.
	await outbox.storage.markUnknown("mutation-1", "blockedUnknown");
	const flush = new OutboxFlush(() => outbox);

	flush.bind("hub-1", client);
	await flush.flush();

	expect(methods(client)).toEqual([]);
	flush.dispose();
	unregister();
	await outbox.stop();
});

it("sends what another screen admits for a session no screen has open", async () => {
	const outbox = runtime();
	const client = new FakeClient("ready");
	client.on("thread/read", () => read("ref-1"));
	client.on("turn/start", applied);
	const flush = new OutboxFlush(() => outbox);
	flush.bind("hub-1", client);
	await flush.flush();
	expect(methods(client)).toEqual([]);

	await outbox.submit(message());

	await vi.waitFor(() => expect(methods(client)).toEqual(["thread/read", "turn/start"]));
	flush.dispose();
	await outbox.stop();
});

it("keeps a message it can't settle, and lets its session go", async () => {
	const outbox = runtime();
	await outbox.submit(message());
	const client = new FakeClient("ready");
	client.on("thread/read", () => Promise.reject(new Error("offline")));
	client.on("turn/start", applied);
	const flush = new OutboxFlush(() => outbox);

	flush.bind("hub-1", client);
	await flush.flush();

	await vi.waitFor(() => expect(outbox.hasTarget("hub-1", "ref-1")).toBe(false));
	expect(methods(client)).not.toContain("turn/start");
	expect((await outbox.storage.getOutbox("mutation-1"))?.state).toBe("submitting");
	flush.dispose();
	await outbox.stop();
});

it("lets go of a target it can't settle, and still sends the others", async () => {
	const outbox = runtime();
	await outbox.submit(message({ targetRef: "ref-1" }));
	await outbox.submit(message({ targetRef: "ref-2" }));
	const client = new FakeClient("ready");
	// ref-1's answer is malformed, so settling it throws.
	client.on("thread/read", (params) => ((params as { ref: string }).ref === "ref-1" ? ({} as never) : read("ref-2")));
	client.on("turn/start", applied);
	const flush = new OutboxFlush(() => outbox);

	flush.bind("hub-1", client);

	await vi.waitFor(() => expect(methods(client)).toContain("turn/start"));
	expect((client.calls.find((call) => call.method === "turn/start")?.params as { ref: string }).ref).toBe("ref-2");
	expect(outbox.hasTarget("hub-1", "ref-1")).toBe(false);
	flush.dispose();
	await outbox.stop();
});

it("looks again for a record that landed while a settle it then lost was in flight", async () => {
	const outbox = runtime();
	await outbox.submit(message());
	const client = new FakeClient("ready");
	const firstRead: { reject?: (error: Error) => void } = {};
	let reads = 0;
	client.on("thread/read", () => {
		reads += 1;
		if (reads > 1) return read("ref-1");
		return new Promise<never>((_resolve, reject) => {
			firstRead.reject = reject;
		});
	});
	client.on("turn/start", applied);
	const flush = new OutboxFlush(() => outbox);
	flush.bind("hub-1", client);
	await vi.waitFor(() => expect(firstRead.reject).toBeDefined());

	await outbox.submit(message({ input: [{ type: "text", text: "and this one" }] }));
	firstRead.reject?.(new Error("the connection dropped the answer"));

	await vi.waitFor(() => expect(methods(client).filter((method) => method === "turn/start")).toHaveLength(2));
	flush.dispose();
	await outbox.stop();
});

it("sends only the active hub's messages, and nothing while the connection isn't ready", async () => {
	const outbox = runtime();
	await outbox.submit(message({ hubId: "hub-2" }));
	const client = new FakeClient("ready");
	const flush = new OutboxFlush(() => outbox);
	flush.bind("hub-1", client);
	await flush.flush();
	expect(methods(client)).toEqual([]);

	const connecting = new FakeClient("connecting");
	flush.bind("hub-2", connecting);
	await flush.flush();
	expect(methods(connecting)).toEqual([]);
	flush.dispose();
	await outbox.stop();
});

it("lets every session go when the connection drops", async () => {
	const outbox = runtime();
	await outbox.submit(message());
	const client = new FakeClient("ready");
	client.on("thread/read", () => new Promise<never>(() => {}));
	const flush = new OutboxFlush(() => outbox);
	flush.bind("hub-1", client);
	await vi.waitFor(() => expect(outbox.hasTarget("hub-1", "ref-1")).toBe(true));

	flush.bind("hub-1", null);

	expect(outbox.hasTarget("hub-1", "ref-1")).toBe(false);
	flush.dispose();
	await outbox.stop();
});

it("reads the hub and ref out of a composite target key", () => {
	expect(parseTargetKey(JSON.stringify(["hub-1", "local:thread-1"]))).toEqual({ hubId: "hub-1", ref: "local:thread-1" });
	expect(parseTargetKey("local:thread-1")).toBeNull();
	expect(parseTargetKey(JSON.stringify(["hub-1"]))).toBeNull();
});
```

The first test fails without the flush's `runtime.start()`: a fresh runtime dispatches nothing until started (`#getClient`, `nativeMutationRuntime.ts:140-149`). The second fails without the `hasTarget` check, the third without settling a covered screen's target, the fourth when that settling ignores whether anything waits, the fifth without the storage watch, the seventh when one target's failure stops the rest, and the eighth when a record that landed during a settle that then failed is left behind.

- [ ] **Step 2: Run them and watch them fail**

Run: `cd mobile-native && npx vitest run src/outbox/outboxFlush.test.ts`
Expected: FAIL: `Cannot find module './outboxFlush'`.

- [ ] **Step 3: Implement**

In `nativeMutationRuntime.ts`, before `beginAuthoritativeRead`:

```ts
	/** Whether a session screen, or the flush, has registered this target. */
	hasTarget(hubId: string, targetRef: string): boolean {
		return this.#targets.has(nativeMutationTargetKey(hubId, targetRef));
	}
```

```ts
// mobile-native/src/outbox/outboxFlush.ts
// Sends what no open session is sending (spec 8.5's offline row, ruling 17):
// a message the phone kept while offline, in a session you have since left,
// goes out when the connection returns, as the web's handleReady does
// (cmd/evener-hub/frontend/src/stores/threads.ts); so does anything admitted
// for a session no screen has open, such as a Stop from the Board. The flush
// takes only targets nobody has registered, settles each with a read that
// leaves the connection's subscription alone (NativeMutationRuntime.
// settleTarget), and lets each go once nothing on it is waiting to be sent.
// A session screen owns its own target; the flush only settles one that has
// something waiting, for a screen under the Reader or a subagent, which
// doesn't read while it's covered.
import type { AppwireClientLike } from "@evener/appwire-client";

export type SettleResult = "open" | "reconciled" | "blocked" | "stale" | "unregistered";

/** The slice of NativeMutationRuntime the flush uses. */
export interface FlushRuntime {
	readonly storage: {
		listTargetRefs(): Promise<string[]>;
		listOutbox(targetRef: string): Promise<readonly { state: string }[]>;
	};
	start(): Promise<void>;
	hasTarget(hubId: string, targetRef: string): boolean;
	registerTarget(hubId: string, targetRef: string, client: AppwireClientLike | null): () => void;
	settleTarget(hubId: string, targetRef: string, client: AppwireClientLike): Promise<SettleResult>;
	subscribeStorage(listener: (targetRefs: readonly string[]) => void): () => void;
}

/** The hub and ref a composite target key names (nativeMutationTargetKey),
 * or null for a key of any other shape. */
export function parseTargetKey(key: string): { hubId: string; ref: string } | null {
	try {
		const value: unknown = JSON.parse(key);
		if (Array.isArray(value) && value.length === 2 && typeof value[0] === "string" && typeof value[1] === "string")
			return { hubId: value[0], ref: value[1] };
	} catch {
		// Not a composite key: not the flush's to send.
	}
	return null;
}

export class OutboxFlush {
	private hubId: string | null = null;
	private client: AppwireClientLike | null = null;
	private generation = 0;
	private readonly owned = new Map<string, () => void>();
	/** Targets the flush holds whose records changed while it held them. */
	private readonly touched = new Set<string>();
	private unsubscribe: (() => void) | null = null;

	/** `runtime` is read at the first ready connection: a message kept from an
	 * earlier launch can only be found in the mutations database. */
	constructor(private readonly runtime: () => FlushRuntime) {}

	/** The active hub and its client while it is ready, else nulls. A new
	 * client flushes, and watches for work no screen will send; losing it lets
	 * every target go. */
	bind(hubId: string | null, client: AppwireClientLike | null): void {
		if (hubId === this.hubId && client === this.client) return;
		this.releaseAll();
		this.touched.clear();
		this.unsubscribe?.();
		this.unsubscribe = null;
		this.hubId = hubId;
		this.client = client;
		this.generation += 1;
		if (hubId === null || client === null) return;
		const runtime = this.runtime();
		this.unsubscribe = runtime.subscribeStorage((keys) => this.changed(runtime, keys));
		void this.flush().catch(() => undefined);
	}

	/** Looks again, for a session screen that let go of its target while the
	 * connection was live. */
	async flush(): Promise<void> {
		const { hubId, client, generation } = this;
		if (hubId === null || client === null || client.state !== "ready") return;
		const runtime = this.runtime();
		// A fresh runtime dispatches nothing until started.
		await runtime.start();
		for (const key of await runtime.storage.listTargetRefs()) {
			if (generation !== this.generation) return;
			const target = parseTargetKey(key);
			// settle() claims a target before its first await, so a second flush
			// running beside this one finds it owned and skips it.
			if (target === null || target.hubId !== hubId || this.owned.has(key)) continue;
			try {
				await this.settle(runtime, key, hubId, target.ref, client, generation);
			} catch {
				// A read or storage failure leaves this target's records where
				// they are, for the next ready connection or the next record,
				// and the other targets still go.
				this.letGo(key);
			}
		}
	}

	dispose(): void {
		this.bind(null, null);
	}

	private async settle(
		runtime: FlushRuntime,
		key: string,
		hubId: string,
		ref: string,
		client: AppwireClientLike,
		generation: number,
	): Promise<void> {
		if (runtime.hasTarget(hubId, ref)) {
			// A session screen holds this target. Under the Reader or a subagent
			// it doesn't read, so after a reconnect its waiting message would
			// wait for a trip back: settle it for the screen, which keeps its
			// registration. settleTarget reads only for the client the target
			// is registered to, so a screen bound to another client is left
			// alone.
			if (await this.waiting(runtime, key)) await runtime.settleTarget(hubId, ref, client);
			return;
		}
		this.owned.set(key, runtime.registerTarget(hubId, ref, client));
		this.touched.delete(key);
		const settled = await runtime.settleTarget(hubId, ref, client);
		if (generation !== this.generation) return;
		if (settled === "reconciled" || settled === "open") await this.releaseIfDone(runtime, key);
		else this.letGo(key);
	}

	/** Lets go of a target the flush couldn't settle. A record that landed on
	 * it meanwhile was left to that settle, so look again for it; a failure
	 * with nothing new waits for the next record or connection, so a failing
	 * read never spins. */
	private letGo(key: string): void {
		this.release(key);
		if (this.touched.delete(key)) void this.flush().catch(() => undefined);
	}

	/** A target the flush holds may be done; a record for one nobody holds is
	 * work no screen will send. A target a session screen holds stays that
	 * screen's here: its records come from the screen, or bring their own
	 * settle (a message sent from a screen above it), and settling it on every
	 * change would re-read after each unknown outcome and could resend in a
	 * loop. */
	private changed(runtime: FlushRuntime, keys: readonly string[]): void {
		let unclaimed = false;
		for (const key of keys) {
			if (this.owned.has(key)) {
				this.touched.add(key);
				void this.releaseIfDone(runtime, key).catch(() => undefined);
				continue;
			}
			const target = parseTargetKey(key);
			if (target !== null && target.hubId === this.hubId && !runtime.hasTarget(target.hubId, target.ref)) unclaimed = true;
		}
		if (unclaimed) void this.flush().catch(() => undefined);
	}

	/** A target is done once nothing on it is waiting to be sent: what's left
	 * is settled, or waits for you in its session (a message it couldn't
	 * confirm, or one a Stop held). */
	private async releaseIfDone(runtime: FlushRuntime, key: string): Promise<void> {
		if (!(await this.waiting(runtime, key))) this.release(key);
	}

	private async waiting(runtime: FlushRuntime, key: string): Promise<boolean> {
		const records = await runtime.storage.listOutbox(key);
		return records.some((record) => record.state === "submitting");
	}

	private release(key: string): void {
		const release = this.owned.get(key);
		if (release === undefined) return;
		this.owned.delete(key);
		release();
	}

	private releaseAll(): void {
		for (const key of [...this.owned.keys()]) this.release(key);
	}
}
```

```ts
// mobile-native/src/outbox/nativeOutboxFlush.ts
import { getNativeMutationRuntime } from "../nativeMutationRuntime";
import { OutboxFlush } from "./outboxFlush";

/** The app's one flush. App.tsx binds it to the connection; a session screen
 * asks it to look again when it lets go of its target. */
export const outboxFlush = new OutboxFlush(getNativeMutationRuntime);
```

- [ ] **Step 4: Wire it**
  - In `App.tsx`'s `Navigation`, bind the flush to the connection: `useEffect(() => { outboxFlush.bind(activeProfile?.id ?? null, state === "ready" ? client : null); }, [activeProfile?.id, state, client]);`, with `client` and `state` from `useConnection()`.
  - In the session screen's durable-host effect cleanup (`screens.tsx:1055-1058`), after `host.dispose()`, call `void outboxFlush.flush()`: letting go of a target writes nothing to storage, so without it a message still waiting when you leave a session would wait for the next connection.
  - Screen tests that import `screens.tsx` mock `./outbox/nativeOutboxFlush` to `{ outboxFlush: { flush: async () => {}, bind: () => {} } }`, the way they mock other native singletons.

- [ ] **Step 5: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/outbox src/nativeMutationRuntime.test.ts src/ConversationScreen.offline.test.tsx && npm run check`
Expected: PASS. In the simulator: turn the network off, send in one session, go back to the Board, turn the network on, and see the session's turn start without opening it.

- [ ] **Step 6: Commit**

```bash
git add mobile-native/src/outbox/outboxFlush.ts mobile-native/src/outbox/outboxFlush.test.ts mobile-native/src/outbox/nativeOutboxFlush.ts mobile-native/src/nativeMutationRuntime.ts mobile-native/App.tsx mobile-native/src/screens.tsx
git commit -m "feat(native): messages you left behind send themselves when the connection returns"
```

### Task 16: The Board's actions wait for the connection

Spec 7.5 sends a Board action taken offline "to the outbox", and phase 3 left the Board's outbox to this phase (its ruling 3). Today an organization change taken offline fails at once and leaves its journal uncertain (`NavigationActions.run`, `navigationActions.ts:210-301`), so an archived row dims and then comes back with nothing said, and a Stop taken offline would reach the hub minutes later and stop whatever turn is running then. A durable queue for Board actions is its own piece of work (Question 3). Until then, a control appears only when it can act (principle 2).

**Files:**
- Modify: phase 2's `src/board/rowActions.ts`, `SwipeRow.tsx`, `RowMenu.tsx` and `SelectBar.tsx` (phase 2 Tasks 12 and 13), and the function there that lists a row's menu actions per state
- Modify: phase 2's `src/board/BoardScreen.tsx`, which holds the `NavigationActions` its row actions use (`archiveSession`, `pinSession`): the reconcile on the ready transition
- Test: phase 2's `rowActions.test.ts`, `RowMenu.test.tsx` and `BoardScreen.test.tsx`

**Interfaces:**
- Consumes: `useConnection().state`.
- Produces: the menu-actions function takes `connected: boolean`.

**Requirements:**
1. While the connection isn't ready, the actions that write to the hub don't show: the leading swipe reveals nothing (a full swipe does nothing), the trailing swipe shows only More, and the long-press menu and select mode keep Mark as read and Mark as unread, which are this phone's own (`seenMarkers`), and drop Pin to category…, Stop, Shut down, Archive and Rename.
2. They come back the moment the connection is ready again, with no refresh.
3. An organization change whose answer the connection lost before it arrived settles itself when the connection returns: the Board calls its `NavigationActions`' `reconcile()` when the connection turns ready while it is focused, as `usePinNavigation` does (`usePinNavigation.ts:109-111`). The row stays dimmed until then, and nothing asks you to refresh.

- [ ] **Step 1: Write the failing tests.**
  - The menu function returns only the read marks offline and every action online, per state.
  - Rendered offline, a row's trailing swipe shows only More, and its leading swipe has no Archive.
  - In `BoardScreen.test.tsx`: archive a row while connected, then drop the connection before the hub answers, so the journal holds an uncertain archive. When the mocked connection goes from `"reconnecting"` to `"ready"` while the Board is focused, the Board's `NavigationActions.reconcile()` runs once (its read-back of the journal's checkpoint, `navigationActions.ts:164-209`), and the row's dimming follows the hub's answer. The same transition while the Board is blurred reads nothing.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/board`
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and watch them pass**, then `npm run check`.
- [ ] **Step 5: Commit** (`feat(native): Board actions that need the hub wait for the connection`).

Open PR F: "feat(native): Send while offline (phase 6, PR F)".
