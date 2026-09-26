# iPhone redesign, Phase 6: Attention and resilience (Implementation Plan)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** In-app alerts tell you when another session needs you without breaking into what you're reading or typing, and the phone is honest about its connection: it says when it's reconnecting or offline, keeps what you send until it can deliver it, and shows each message's delivery on the message itself, with no recovery screen.

**Architecture:**
- **Alerts.** A pure `AlertCenter` (`src/alerts/alertCenter.ts`) decides which banner shows, which alerts wait while you read or type, and which sessions alerted you most recently, with the prototype's tested timing. Pure detectors (`src/alerts/alertEvents.ts`) turn successive navigation reads into alerts. An `AlertsProvider` feeds the center from the app-wide attention data, and `AlertBannerHost` draws the banner just below the nav bar and opens what you tap.
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
  - Route names and params are unchanged. The Board stays `"Sessions"` with no params, so a coalesced banner reaches Needs you through `src/board/boardJump.ts` (Task 7), never a route param.
  - Existing storage keys are unchanged. One new kv-store key, `evener.native.alert-preferences` (per device, Task 5).
  - The mutation database keeps its schema: Discard deletes a row, Send now moves one, and nothing adds a column.
- **Tests** meet the hub at the request boundary: `FakeClient` from `@evener/appwire-client/testing/fakeClient`, the `boundary()` fake of `src/projectBrowser.test.ts`, or `scriptedClient` from `src/renderNative.testkit.tsx`. The mutation runtime runs for real over `openSqliteSyncDouble()`. Never mock the module under test.
- **Repo rules:**
  - Never run Biome in `mobile-native/`.
  - Never run `npm ci` through a symlinked `node_modules`: check that `[ -L node_modules ]` prints nothing first.
  - Never `git add -A`.
  - iPhone only. Run the task's own tests and `npm run check` locally; CI runs `make test-native`.

## Rulings

Decisions this plan makes where the spec is silent or its data doesn't exist yet. Questions 1 to 3 below ask Jesse about the three that change what a person sees.

1. **Alerts come from the navigation rows the Board already reads,** diffed per session: the complete `needs_you` section and Live's first page, classified by phase 2's `boardState`. A finished alert needs the session's working row on Live's first page, which holds the sessions most likely to finish.
2. **The first read on a new client is a baseline and alerts nothing.** A new client means the app came back to the foreground, the hub changed, or a closed connection was replaced. A drop the same client recovers from is diffed, so a flap on the train still tells you what changed meanwhile (Question 1).
3. **A row on an offline host keeps its last state,** so a host going away and coming back never alerts. The hub marks such rows `offline`, and phase 2 shows them as shut down.
4. **Which alerts have switches.** Failures (on), questions and approvals (on) and finished results (off) have switches (spec 12). Warnings, restart-needed and notices always alert, as in the prototype's `EV.alert`.
5. **Notice alerts.** A provider sign-in that expired and a host that went offline alert when they appear while the Board isn't on screen. A broken plugin never alerts: the Board checks plugins only while it is on screen (phase 2 ruling 8), where its notice row already shows.
6. **A coalesced banner counts each session once,** and a session that alerts again updates its own entry. (The prototype counted it twice.)
7. **Holds.** Banners wait while the Reader is open (and the artifact viewer, once it exists), while you type (the composer focused with text in it, so sending lets them go, as the prototype's Hub row says: "They show when you leave the document or send"), and while a sheet is up: phase 5's sheet routes and every React Native `Modal` (Question 2). They show 200ms after the last hold ends, the prototype's delay after the composer blurs, so the screen you land on counts first: landing on the session that alerted answers it.
8. **Haptics.** A banner buzzes once, when it drops in; alerts that join it don't. Every haptic in spec 16.6 answers to the one Haptics switch (phase 3 ruling 5).
9. **Alerts read the fleet with their own board controller, never paused.** The Board and the Session pause theirs on blur (phase 3 ruling 33), and alerts must hear about sessions while you're anywhere. That costs a second set of navigation reads while the Board or a Session is in front; sharing one controller is a later consolidation.
10. **The connection clock counts only time in front.** The app closes its connection in the background (`ConnectionProvider`), so returning never flashes "Offline". "Updated 3m ago" still counts from when the data was last live, background included.
11. **The offline age is whole minutes, at least 1m** ("updated 1m ago" from the 30-second mark), because the status changes once a minute. `relativeAge` says "now" under a minute, which would read "updated now ago".
12. **Offline Send is fenced to the session instance the phone last saw,** as an online send is. A session never loaded since launch has no instance to fence with, so there Send stays disabled offline and the draft stays.
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

Phases 2 to 5 are planned beside this one, so some names below are what those plans say they produce. Before a task that uses one, read the landed code. If it landed under another name or shape, use the landed one and say so in the PR.

| This plan uses | From | Used by |
|---|---|---|
| `boardState`, `liveBands`, `whyLine`, `WhyLine`, `LiveBands`, `BoardState` (`src/board/attention.ts`), `StateMark`, `seenMarkers` | phase 2 PR 1 | Tasks 3, 4, 7, 8 |
| `createBoardController` (`src/board/boardData.ts`), `connectionStatus` (`src/board/connectionStatus.ts`), `BoardScreen`, `BoardToolbar` | phase 2 PR 2 | Tasks 1, 7, 8 |
| `Notice` and `notices` (`src/board/notices.ts`), `Notices.tsx`; `rowActions.ts`, `SwipeRow.tsx`, `RowMenu.tsx`, `SelectBar.tsx` | phase 2 part 2 (its Tasks 12-14) | Tasks 4, 8, 16 |
| The demo fleet (`src/dev/demoFleet.ts`, `scripts/demo-hub.mts`) | phase 2 PR B | Task 17 |
| `useConnectionStatusText()` and the Session's connection bar (its Task 15); `compactDuration` (`src/session/format.ts`) and `sendAction` (its Task 3); `Composer` (its Task 5); `ghosts.ts` and its wiring (its Tasks 7-8); `fleetOrder.ts` (`othersNeedingYou`, `nextSession`), `BackButton` and the Next capsule (its Tasks 32-33); `Toast` | phase 3 | Tasks 1, 2, 10, 11, 13, 14 |
| Phase 3 left here: Send while offline (ruling 4), every haptic (ruling 5), Next's recent order (ruling 11), the Board's outbox and a Stop-held message's retry (ruling 3) | phase 3 | Tasks 6, 11, 13, 14, 16 |
| `NativeMutationRuntime.settleTarget` (its Task 2); `ReaderScreen` (`src/reader/ReaderScreen.tsx`, its Task 14) on the `"Reader"` route | phase 4 | Tasks 10, 15 |
| The grouped-list pieces (`src/sheet/Grouped.tsx`, its Task 1); `useConnectionStatusLine`, `SheetStatus` and `INCOMPATIBLE_VERSIONS` (its Task 2); the Hub sheet (`src/hub/HubSheet.tsx`, `HubHome.tsx`, its Task 3) and its sheet routes `"Hub"` and `"NewSession"`; the "Reconnect" guard and the connection messages (its Task 27); phase 5 left the In-app alerts page here (its ruling 11) | phase 5 | Tasks 1, 2, 8, 9 |

## Review Focus

1. **A pile of banners when you come back.** Opening the app, switching hubs or returning from the background must not alert about sessions that already needed you; the Board, Back's count and Next already show them. Pinned by Task 4 (the first read is a baseline) and Task 8 (a new client starts a new baseline, and a Needs you section still loading is never the baseline).
2. **Old news alerting twice.** A host that goes offline and comes back, or a session that alerts twice within a burst, must never produce a second banner or count one session twice. Pinned by Task 4 (offline rows keep their state) and Task 3 (a coalesced banner counts each session once).
3. **Returning to the app flashes "Offline".** An hour in the background, then a foreground return whose connection takes a second, must show nothing, and "Reconnecting…" only after 2 seconds. Pinned by Task 1 (the clock never counts background time).
4. **A message sent offline is lost, sent twice, left waiting, or stopped by its own Stop.** Send while offline, then leave the session or open the Reader from it, relaunch, and come back online: the message goes out exactly once, with no trip back to its session. A message a Stop held, sent again, goes after that Stop. Pinned by Task 14 (the offline admission stores one record, sent once when the connection returns), Task 15 (a fresh process sends it once, and the flush settles for a session screen under the Reader) and Task 12 (Send now moves the row behind its Stop).
5. **A banner in the way.** A banner never covers the nav bar (Back, the title, the ask dock), never interrupts a sheet or your typing, and never goes away under your finger. Pinned by Task 8 (it sits below the header height, and a sheet route holds it), Task 10 (typing and every `Modal` hold it) and Task 3 (a finger keeps it).

---

## PRs and lanes

| PR | Tasks | Model | Starts when | Lane |
|---|---|---|---|---|
| A: one connection clock, and no reconnect words | 1-2 | Sonnet (1), Opus (2) | the phase starts (phase 5's PRs on main) | connection |
| B: the alert center and haptics | 3-6 | Sonnet (3-5), Opus (6) | the phase starts | alerts |
| C: banners on every screen | 7-8 | Opus | PR B lands | alerts |
| D: holds, the In-app alerts page and Next | 9-11 | Opus (9-10), Sonnet (11) | PR C lands | alerts |
| E: your own undelivered messages | 12-13 | Sonnet (12), Opus (13) | the phase starts | outbox |
| F: Send while offline | 14-16 | Opus (14, 16), Sonnet (15) | PRs A, B and E land | connection |
| G: the demo and the screenshots | 17-18 | Opus (17), then by hand | PRs D and F land | alerts |

- Three lanes start together: alerts (PR B), connection (PR A) and outbox (PR E). The outbox lane joins the connection lane at PR F, and that lane joins the alerts lane at PR G.
- Files two lanes share: `App.tsx` (PR C mounts the alerts, PR F binds the flush) and phase 3's session screen (PRs D, E and F). The second PR to land in either merges `origin/main` before its review.
- Every PR lands under the roadmap's rules: CI green, RoboRev with nothing Medium or higher, /simplify, admin squash merge, Lows in a fast-follow, and decompose after five rounds.
- PR G, the phase's last, carries Release-simulator screenshots of the alert and offline frames against the demo fleet (Task 18).

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

Spec 14's version sentence also lives in three places: phase 2's Board notice, phase 3's connection bar hint, and `INCOMPATIBLE_VERSIONS` in phase 5's `src/sheet/SheetStatus.tsx`, which phase 5 Task 27 imports into `connectionRecovery.ts`. That import runs in a circle (`ConnectionProvider` → `hubConnection` → `connectionRecovery` → `SheetStatus` → `ConnectionProvider`). The constant moves down into `connectionRecovery.ts`, and the other three import it from there.

**Files:**
- Modify: `mobile-native/src/connectionRecovery.ts` (`INCOMPATIBLE_VERSIONS` declared here), `mobile-native/src/sheet/SheetStatus.tsx` and its test (import it from `../connectionRecovery`), phase 2's Board notice and phase 3's `SessionHeader.tsx` hint (import it instead of repeating the sentence)
- Create: `mobile-native/src/connectionRecovery.test.ts` and `mobile-native/src/calmCopy.test.ts`
- Delete: `mobile-native/src/noReconnect.test.ts` (phase 5 Task 27), which `calmCopy.test.ts` replaces
- Modify: every production file the audit reports, and the tests that assert the strings you change

**Interfaces:**
- Consumes: phase 5's `INCOMPATIBLE_VERSIONS` (its Task 2) and its `connectionRecovery.ts` messages (its Task 27).
- Produces: `INCOMPATIBLE_VERSIONS: string` from `src/connectionRecovery.ts`. `connectionFailure(terminalReason)` keeps its signature.

**Requirements:**
1. `connectionRecovery.ts` declares `INCOMPATIBLE_VERSIONS`, spec 14's sentence, and returns it for a protocol close. A transport close returns "Couldn't reach the hub. Check its address, its token and your network." Phase 5 Task 27 writes both messages; if they aren't on main, write them here. They show while pairing and in Hub settings; during use, the status line speaks instead.
2. Each string the audit reports is either removed with its control, because the app already does the thing (it reconnects on its own and re-reads on every hub invalidation), or rewritten per spec 5: what went wrong and what to do, where "what to do" is never to reconnect, refresh or pull. Where the app doesn't retry by itself (a save or a change it couldn't confirm), "Try again" is fine.
3. The audit allows one file, `nativeMutationHost.ts`, whose "Reconnect and try again." refusal Task 14 deletes along with the allowance.
4. A test that asserts a rewritten string changes in the same commit, and the commit body lists them.

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/connectionRecovery.test.ts
import { expect, it } from "vitest";
import { connectionFailure, INCOMPATIBLE_VERSIONS } from "./connectionRecovery";

it("says what to do when the app and the hub speak different versions (spec 14)", () => {
	expect(INCOMPATIBLE_VERSIONS).toBe(
		"This app and the hub need compatible versions. Update the app from TestFlight, or update Evener on the hub.",
	);
	expect(connectionFailure("protocol")).toEqual({ kind: "protocol", message: INCOMPATIBLE_VERSIONS });
});

it("says what to check when the hub can't be reached, and never to retry", () => {
	expect(connectionFailure(null)).toEqual({
		kind: "transport",
		message: "Couldn't reach the hub. Check its address, its token and your network.",
	});
});
```

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

function inConsoleCall(node: ts.Node): boolean {
	for (let parent = node.parent; parent; parent = parent.parent) {
		if (!ts.isCallExpression(parent)) continue;
		const callee = parent.expression;
		return ts.isPropertyAccessExpression(callee) && ts.isIdentifier(callee.expression) && callee.expression.text === "console";
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

- [ ] **Step 2: Run them and read the list**

Run: `cd mobile-native && npx vitest run src/connectionRecovery.test.ts src/calmCopy.test.ts`
Expected: FAIL. The self-check passes, and the audit lists each offending string by file: this task's worklist, whose length goes in the PR description. On main at `d0c0211be` it held 101 strings in 43 files. Phases 2 to 5 replace the screens behind most of them (phase 3 rewrites `approvalControls.ts`'s). The shared navigation messages are the likeliest to remain: `navigationActions.ts` (6), `navigationPages.ts` (5), `navigationReadback.ts` (4), `pinNavigation.ts` (4), `organizationNavigation.ts` (2), and one each in `navigationReveal.ts` and `sessionDeletionNavigation.ts`.

- [ ] **Step 3: Implement** requirement 1 and the constant's move, then work through the list under requirements 2 and 4, one file at a time, running that file's own tests after each.

- [ ] **Step 4: Run the audit, the touched tests and the type check**

Run: `cd mobile-native && npx vitest run src/connectionRecovery.test.ts src/calmCopy.test.ts <touched test files> && npm run check`
Expected: PASS, with the audit's offender list empty.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/connectionRecovery.ts mobile-native/src/connectionRecovery.test.ts mobile-native/src/calmCopy.test.ts <every file you changed or deleted>
git commit -m "fix(native): no text asks you to reconnect or refresh"
```

Open PR A: "feat(native): one connection clock, and no text asks you to reconnect (phase 6, PR A)". The description lists the audit's first-run offenders and what became of each.

---

## PR B: the alert center and haptics

PR B's first three tasks are pure; Task 6 adds the haptics every later task plays. PR C is the center's first consumer; say so in the PR description.

### Task 3: The alert center

**Files:**
- Create: `mobile-native/src/alerts/alertCenter.ts`
- Test: `mobile-native/src/alerts/alertCenter.test.ts`

**Interfaces:**
- Consumes: `WhyLine` from `src/board/attention.ts` (phase 2).
- Produces (all exported from `alertCenter.ts`):
  - `type NeedsYouKind = "failed" | "question" | "approval" | "warning" | "restartNeeded"`
  - `interface SessionAlert { kind: NeedsYouKind | "finished"; ref: string; title: string; why: WhyLine | null }`
  - `interface NoticeAlert { kind: "notice"; key: string; title: string }`
  - `type Alert = SessionAlert | NoticeAlert`
  - `interface AlertPreferences { failures; questions; finished; hold; haptics }` (all `boolean`) and `DEFAULT_ALERT_PREFERENCES`
  - `interface Banner { id: number; alerts: readonly Alert[] }`
  - `interface AlertSnapshot { banner: Banner | null; held: number; recent: readonly string[] }`
  - `type AlertScreen = { kind: "board" } | { kind: "session"; ref: string } | { kind: "other" }`
  - `type BannerTarget = { kind: "session"; ref: string } | { kind: "needsYou" } | { kind: "notice"; key: string }`
  - `type Haptic = "warning" | "light"`
  - `interface AlertTimer { now(): number; setTimeout(callback: () => void, ms: number): unknown; clearTimeout(handle: unknown): void }`
  - `BANNER_MS`, `COALESCE_MS`, `RELEASE_MS`, `needsYou(alert): boolean`
  - `class AlertCenter`: constructor `(timer: AlertTimer, haptic?: (kind: Haptic) => void)`; `getSnapshot()`, `subscribe(listener)`, `offer(alert)`, `setScreen(screen)`, `setPreferences(preferences)`, `hold(): () => void`, `touch(down: boolean)`, `dismiss()`, `tap(): BannerTarget | null`, `nextUsed()`, `reset()`

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/alerts/alertCenter.test.ts
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	type Alert,
	AlertCenter,
	BANNER_MS,
	COALESCE_MS,
	DEFAULT_ALERT_PREFERENCES,
	type Haptic,
	RELEASE_MS,
	type SessionAlert,
} from "./alertCenter";

const timer = {
	now: () => Date.now(),
	setTimeout: (callback: () => void, ms: number) => setTimeout(callback, ms),
	clearTimeout: (handle: unknown) => clearTimeout(handle as ReturnType<typeof setTimeout>),
};

function session(ref: string, kind: SessionAlert["kind"] = "question"): SessionAlert {
	return { kind, ref, title: `Session ${ref}`, why: null };
}
const hostOffline: Alert = { kind: "notice", key: "host:paradise-park", title: "paradise-park is offline · 3 sessions" };

function center() {
	const haptics: Haptic[] = [];
	return { alerts: new AlertCenter(timer, (kind) => haptics.push(kind)), haptics };
}
function shown(alerts: AlertCenter): string[] | undefined {
	return alerts.getSnapshot().banner?.alerts.map((alert) => (alert.kind === "notice" ? alert.key : alert.ref));
}

beforeEach(() => {
	vi.useFakeTimers();
});
afterEach(() => {
	vi.useRealTimers();
});

describe("a banner (spec 13.3)", () => {
	it("stays 8 seconds", () => {
		const { alerts } = center();
		alerts.offer(session("a"));
		expect(shown(alerts)).toEqual(["a"]);
		vi.advanceTimersByTime(BANNER_MS - 1);
		expect(shown(alerts)).toEqual(["a"]);
		vi.advanceTimersByTime(1);
		expect(alerts.getSnapshot().banner).toBeNull();
	});

	it("never goes away while a finger is on it", () => {
		const { alerts } = center();
		alerts.offer(session("a"));
		alerts.touch(true);
		vi.advanceTimersByTime(BANNER_MS * 3);
		expect(shown(alerts)).toEqual(["a"]);
		alerts.touch(false);
		vi.advanceTimersByTime(BANNER_MS);
		expect(alerts.getSnapshot().banner).toBeNull();
	});

	it("goes when swiped up", () => {
		const { alerts } = center();
		alerts.offer(session("a"));
		alerts.dismiss();
		expect(alerts.getSnapshot().banner).toBeNull();
	});

	it("says where a tap goes, and goes", () => {
		const { alerts } = center();
		alerts.offer(session("a"));
		expect(alerts.tap()).toEqual({ kind: "session", ref: "a" });
		expect(alerts.getSnapshot().banner).toBeNull();
		alerts.offer(hostOffline);
		expect(alerts.tap()).toEqual({ kind: "notice", key: "host:paradise-park" });
		expect(alerts.tap()).toBeNull();
	});
});

describe("coalescing (spec 13.3)", () => {
	it("combines alerts about sessions that need you within 5 seconds, each session once", () => {
		const { alerts, haptics } = center();
		alerts.offer(session("a"));
		vi.advanceTimersByTime(COALESCE_MS - 1);
		alerts.offer(session("b", "failed"));
		alerts.offer(session("a", "failed"));
		expect(shown(alerts)).toEqual(["a", "b"]);
		expect(alerts.getSnapshot().banner?.alerts[0]).toMatchObject({ ref: "a", kind: "failed" });
		expect(haptics).toEqual(["light"]);
		expect(alerts.tap()).toEqual({ kind: "needsYou" });
	});

	it("starts a fresh banner once 5 seconds have passed", () => {
		const { alerts, haptics } = center();
		alerts.offer(session("a"));
		const first = alerts.getSnapshot().banner?.id;
		vi.advanceTimersByTime(COALESCE_MS);
		alerts.offer(session("b"));
		expect(shown(alerts)).toEqual(["b"]);
		expect(alerts.getSnapshot().banner?.id).not.toBe(first);
		expect(haptics).toEqual(["light", "light"]);
	});

	it("keeps a burst together and gives it 8 seconds from its last alert", () => {
		const { alerts } = center();
		alerts.offer(session("a"));
		vi.advanceTimersByTime(4_000);
		alerts.offer(session("b"));
		vi.advanceTimersByTime(4_000);
		alerts.offer(session("c"));
		expect(shown(alerts)).toEqual(["a", "b", "c"]);
		vi.advanceTimersByTime(BANNER_MS - 1);
		expect(shown(alerts)).toEqual(["a", "b", "c"]);
		vi.advanceTimersByTime(1);
		expect(alerts.getSnapshot().banner).toBeNull();
	});

	it("never combines a notice or a finished result with sessions that need you", () => {
		const { alerts } = center();
		alerts.setPreferences({ ...DEFAULT_ALERT_PREFERENCES, finished: true });
		alerts.offer(session("a"));
		alerts.offer(hostOffline);
		expect(shown(alerts)).toEqual(["host:paradise-park"]);
		alerts.offer(session("b"));
		expect(shown(alerts)).toEqual(["b"]);
		alerts.offer(session("c", "finished"));
		expect(shown(alerts)).toEqual(["b"]);
	});
});

describe("what alerts at all", () => {
	it("follows Hub > In-app alerts, where warnings and restarts have no switch", () => {
		const { alerts } = center();
		alerts.setPreferences({ ...DEFAULT_ALERT_PREFERENCES, failures: false, questions: false });
		alerts.offer(session("a", "failed"));
		alerts.offer(session("b", "question"));
		alerts.offer(session("c", "approval"));
		alerts.offer(session("d", "finished"));
		expect(alerts.getSnapshot().banner).toBeNull();
		alerts.offer(session("e", "warning"));
		alerts.offer(session("f", "restartNeeded"));
		expect(shown(alerts)).toEqual(["e", "f"]);
	});

	it("shows a finished result only when turned on, and never buzzes for it", () => {
		const { alerts, haptics } = center();
		alerts.offer(session("a", "finished"));
		expect(alerts.getSnapshot().banner).toBeNull();
		alerts.setPreferences({ ...DEFAULT_ALERT_PREFERENCES, finished: true });
		alerts.offer(session("a", "finished"));
		expect(shown(alerts)).toEqual(["a"]);
		expect(haptics).toEqual([]);
	});

	it("says nothing about the session on screen, or about a notice while the Board lists it", () => {
		const { alerts } = center();
		alerts.setScreen({ kind: "session", ref: "a" });
		alerts.offer(session("a"));
		expect(alerts.getSnapshot()).toMatchObject({ banner: null, recent: [] });
		alerts.setScreen({ kind: "board" });
		alerts.offer(hostOffline);
		expect(alerts.getSnapshot().banner).toBeNull();
		alerts.offer(session("a"));
		expect(shown(alerts)).toEqual(["a"]);
	});

	it("takes a session's alerts away when you open it", () => {
		const { alerts } = center();
		alerts.offer(session("a"));
		alerts.offer(session("b"));
		alerts.setScreen({ kind: "session", ref: "a" });
		expect(shown(alerts)).toEqual(["b"]);
		expect(alerts.getSnapshot().recent).toEqual(["b"]);
		alerts.setScreen({ kind: "session", ref: "b" });
		expect(alerts.getSnapshot().banner).toBeNull();
	});
});

describe("haptics (spec 13.3)", () => {
	it("buzz once per banner: a warning for a failure, light for the rest", () => {
		const { alerts, haptics } = center();
		alerts.offer(session("a", "failed"));
		alerts.offer(session("b"));
		vi.advanceTimersByTime(COALESCE_MS);
		alerts.offer(hostOffline);
		expect(haptics).toEqual(["warning", "light"]);
	});

	it("stay still when turned off", () => {
		const { alerts, haptics } = center();
		alerts.setPreferences({ ...DEFAULT_ALERT_PREFERENCES, haptics: false });
		alerts.offer(session("a", "failed"));
		expect(haptics).toEqual([]);
	});
});

describe("held while you read or type (spec 13.3)", () => {
	it("waits, counting each session once, and shows them combined once you leave", () => {
		const { alerts, haptics } = center();
		const release = alerts.hold();
		alerts.offer(session("a"));
		alerts.offer(session("a", "failed"));
		alerts.offer(session("b"));
		expect(alerts.getSnapshot()).toMatchObject({ banner: null, held: 2 });
		release();
		vi.advanceTimersByTime(RELEASE_MS - 1);
		expect(alerts.getSnapshot().banner).toBeNull();
		vi.advanceTimersByTime(1);
		expect(shown(alerts)).toEqual(["a", "b"]);
		expect(alerts.getSnapshot().held).toBe(0);
		expect(haptics).toEqual(["light"]);
	});

	it("shows one held session as its own banner, and a held notice only when no session waits", () => {
		const { alerts } = center();
		let release = alerts.hold();
		alerts.offer(hostOffline);
		alerts.offer(session("a", "failed"));
		release();
		vi.advanceTimersByTime(RELEASE_MS);
		expect(shown(alerts)).toEqual(["a"]);
		alerts.dismiss();
		release = alerts.hold();
		alerts.offer(hostOffline);
		release();
		vi.advanceTimersByTime(RELEASE_MS);
		expect(shown(alerts)).toEqual(["host:paradise-park"]);
	});

	it("waits for every hold to end, and a quick return holds again", () => {
		const { alerts } = center();
		const reading = alerts.hold();
		const typing = alerts.hold();
		alerts.offer(session("a"));
		reading();
		vi.advanceTimersByTime(RELEASE_MS);
		expect(alerts.getSnapshot().banner).toBeNull();
		typing();
		vi.advanceTimersByTime(RELEASE_MS - 1);
		const back = alerts.hold();
		vi.advanceTimersByTime(RELEASE_MS);
		expect(alerts.getSnapshot()).toMatchObject({ banner: null, held: 1 });
		back();
		vi.advanceTimersByTime(RELEASE_MS);
		expect(shown(alerts)).toEqual(["a"]);
	});

	it("drops a finished result instead of holding it", () => {
		const { alerts } = center();
		alerts.setPreferences({ ...DEFAULT_ALERT_PREFERENCES, finished: true });
		const release = alerts.hold();
		alerts.offer(session("a", "finished"));
		expect(alerts.getSnapshot().held).toBe(0);
		release();
		vi.advanceTimersByTime(RELEASE_MS);
		expect(alerts.getSnapshot().banner).toBeNull();
	});

	it("lets go of held alerts about the session you land on", () => {
		const { alerts } = center();
		const release = alerts.hold();
		alerts.offer(session("a"));
		alerts.offer(session("b"));
		release();
		alerts.setScreen({ kind: "session", ref: "a" });
		vi.advanceTimersByTime(RELEASE_MS);
		expect(shown(alerts)).toEqual(["b"]);
	});

	it("shows what it held at once when holding is turned off", () => {
		const { alerts } = center();
		alerts.hold();
		alerts.offer(session("a"));
		alerts.setPreferences({ ...DEFAULT_ALERT_PREFERENCES, hold: false });
		vi.advanceTimersByTime(RELEASE_MS);
		expect(shown(alerts)).toEqual(["a"]);
		alerts.offer(session("b"));
		expect(shown(alerts)).toEqual(["a", "b"]);
	});
});

describe("the order Next serves first (spec 8.3)", () => {
	it("puts whatever alerted most recently first, shown or held", () => {
		const { alerts } = center();
		alerts.offer(session("a"));
		alerts.hold();
		alerts.offer(session("b"));
		alerts.offer(session("c"));
		alerts.offer(session("b", "failed"));
		expect(alerts.getSnapshot().recent).toEqual(["b", "c", "a"]);
	});

	it("never counts a finished result", () => {
		const { alerts } = center();
		alerts.setPreferences({ ...DEFAULT_ALERT_PREFERENCES, finished: true });
		alerts.offer(session("a", "finished"));
		expect(alerts.getSnapshot().recent).toEqual([]);
	});

	it("drops held banners once Next takes you on, and keeps the order", () => {
		const { alerts } = center();
		const release = alerts.hold();
		alerts.offer(session("a"));
		alerts.offer(session("b"));
		alerts.nextUsed();
		expect(alerts.getSnapshot()).toMatchObject({ held: 0, recent: ["b", "a"] });
		release();
		vi.advanceTimersByTime(RELEASE_MS);
		expect(alerts.getSnapshot().banner).toBeNull();
	});
});

it("forgets everything for another hub", () => {
	const { alerts } = center();
	alerts.offer(session("a"));
	const release = alerts.hold();
	alerts.offer(session("b"));
	alerts.reset();
	expect(alerts.getSnapshot()).toEqual({ banner: null, held: 0, recent: [] });
	release();
	vi.advanceTimersByTime(BANNER_MS);
	expect(alerts.getSnapshot().banner).toBeNull();
});

it("tells subscribers when something changes", () => {
	const { alerts } = center();
	let calls = 0;
	const stop = alerts.subscribe(() => calls++);
	const before = alerts.getSnapshot();
	alerts.offer(session("a"));
	expect(calls).toBe(1);
	expect(alerts.getSnapshot()).not.toBe(before);
	stop();
	alerts.dismiss();
	expect(calls).toBe(1);
});
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd mobile-native && npx vitest run src/alerts/alertCenter.test.ts`
Expected: FAIL: `Cannot find module './alertCenter'`.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/alerts/alertCenter.ts
// In-app alerts (spec 13.3) as a small state machine with no React and no
// native modules: which banner shows, which alerts wait while you read or
// type, and which sessions alerted you most recently, the order Next serves
// first (spec 8.3). The timing and coalescing are the prototype's
// (docs/design/mobile/redesign/prototype/core.js, EV.alert and releaseHeld),
// which the usability rounds tested (findings.md: round 1 problems 2 and 3,
// round 3 problem 1, round 4 problem 2).
import type { WhyLine } from "../board/attention";

/** The Board states that put a session in Needs you (spec 13.2). */
export type NeedsYouKind = "failed" | "question" | "approval" | "warning" | "restartNeeded";

export interface SessionAlert {
	kind: NeedsYouKind | "finished";
	ref: string;
	title: string;
	why: WhyLine | null;
}

export interface NoticeAlert {
	kind: "notice";
	/** The notice's own key (board/notices.ts), which says what it opens. */
	key: string;
	title: string;
}

export type Alert = SessionAlert | NoticeAlert;

/** Hub > In-app alerts (spec 12). */
export interface AlertPreferences {
	failures: boolean;
	/** Questions and approvals. */
	questions: boolean;
	finished: boolean;
	/** Hold alerts while reading or typing. */
	hold: boolean;
	haptics: boolean;
}

export const DEFAULT_ALERT_PREFERENCES: AlertPreferences = {
	failures: true,
	questions: true,
	finished: false,
	hold: true,
	haptics: true,
};

export interface Banner {
	/** New for each banner that drops in; kept while a banner updates in place. */
	id: number;
	/** One alert, or two or more about sessions that need you ("3 sessions need you"). */
	alerts: readonly Alert[];
}

export interface AlertSnapshot {
	banner: Banner | null;
	/** Alerts waiting while you read or type: one per session or notice. */
	held: number;
	/** Sessions that alerted you, most recent first. */
	recent: readonly string[];
}

/** What is on screen, as far as alerts care. */
export type AlertScreen = { kind: "board" } | { kind: "session"; ref: string } | { kind: "other" };

/** Where a tapped banner goes. */
export type BannerTarget =
	| { kind: "session"; ref: string }
	| { kind: "needsYou" }
	| { kind: "notice"; key: string };

export type Haptic = "warning" | "light";

/** The clock the center runs on: the real one in the app, a fake in tests. */
export interface AlertTimer {
	now(): number;
	setTimeout(callback: () => void, ms: number): unknown;
	clearTimeout(handle: unknown): void;
}

/** A banner stays 8 seconds (spec 13.3). */
export const BANNER_MS = 8_000;
/** Alerts within 5 seconds combine (spec 13.3). */
export const COALESCE_MS = 5_000;
/** Held banners show this long after the last hold ends, so a quick return
 * holds again and the screen you land on counts first (ruling 7). */
export const RELEASE_MS = 200;
const RECENT_LIMIT = 20;

export function needsYou(alert: Alert): alert is SessionAlert & { kind: NeedsYouKind } {
	return alert.kind !== "notice" && alert.kind !== "finished";
}

function subject(alert: Alert): string {
	return alert.kind === "notice" ? `notice:${alert.key}` : `session:${alert.ref}`;
}

export class AlertCenter {
	private banner: Banner | null = null;
	private shownAt = 0;
	private expiry: unknown = null;
	private releasing: unknown = null;
	private touching = false;
	private held: Alert[] = [];
	private recent: string[] = [];
	private holds = new Set<symbol>();
	private screen: AlertScreen = { kind: "other" };
	private preferences: AlertPreferences = DEFAULT_ALERT_PREFERENCES;
	private nextId = 1;
	private snapshot: AlertSnapshot = { banner: null, held: 0, recent: [] };
	private listeners = new Set<() => void>();

	constructor(
		private readonly timer: AlertTimer,
		private readonly haptic: (kind: Haptic) => void = () => {},
	) {}

	getSnapshot = (): AlertSnapshot => this.snapshot;

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};

	offer(alert: Alert): void {
		if (!this.wanted(alert)) return;
		if (needsYou(alert)) this.remember(alert.ref);
		// A finished result never joins, replaces or waits with alerts about
		// sessions that need you (spec 13.3).
		if (alert.kind === "finished" && (this.banner !== null || this.holding())) return;
		if (this.holding()) {
			this.held = [...this.held.filter((waiting) => subject(waiting) !== subject(alert)), alert];
			this.publish();
			return;
		}
		this.show(alert);
	}

	/** What is on screen. Looking at a session answers its alerts: it leaves
	 * the banner, the held alerts and the recent order. */
	setScreen(screen: AlertScreen): void {
		this.screen = screen;
		if (screen.kind !== "session") return;
		const about = (alert: Alert) => alert.kind !== "notice" && alert.ref === screen.ref;
		const recent = this.recent.filter((ref) => ref !== screen.ref);
		const held = this.held.filter((alert) => !about(alert));
		const shown = this.banner?.alerts ?? [];
		const kept = shown.filter((alert) => !about(alert));
		if (recent.length === this.recent.length && held.length === this.held.length && kept.length === shown.length)
			return;
		this.recent = recent;
		this.held = held;
		if (this.banner !== null) {
			if (kept.length === 0) this.stopBanner();
			else this.banner = { id: this.banner.id, alerts: kept };
		}
		this.publish();
	}

	setPreferences(preferences: AlertPreferences): void {
		const wasHolding = this.holding();
		this.preferences = preferences;
		if (wasHolding && !this.holding()) this.scheduleRelease();
	}

	/** Holds banners until the returned function runs: the Reader, typing and
	 * a sheet each hold one (ruling 7). */
	hold(): () => void {
		const token = Symbol("hold");
		this.holds.add(token);
		this.cancelRelease();
		return () => {
			if (this.holds.delete(token) && this.holds.size === 0) this.scheduleRelease();
		};
	}

	/** A finger on the banner keeps it up (spec 13.3). */
	touch(down: boolean): void {
		this.touching = down;
	}

	/** Swiped up. */
	dismiss(): void {
		if (this.banner === null) return;
		this.stopBanner();
		this.publish();
	}

	/** Tapped: the banner goes, and the caller opens where it points. */
	tap(): BannerTarget | null {
		const banner = this.banner;
		if (banner === null) return null;
		this.stopBanner();
		this.publish();
		const [only] = banner.alerts;
		if (only === undefined || banner.alerts.length > 1) return { kind: "needsYou" };
		return only.kind === "notice" ? { kind: "notice", key: only.key } : { kind: "session", ref: only.ref };
	}

	/** Next took you on: it serves the held sessions itself now, so they
	 * don't drop in later (the prototype's goNext). */
	nextUsed(): void {
		if (this.held.length === 0) return;
		this.held = [];
		this.publish();
	}

	/** Another hub, or none: nothing carries over. */
	reset(): void {
		this.stopBanner();
		this.cancelRelease();
		this.held = [];
		this.recent = [];
		this.publish();
	}

	private wanted(alert: Alert): boolean {
		const { failures, questions, finished } = this.preferences;
		if (alert.kind === "failed" && !failures) return false;
		if ((alert.kind === "question" || alert.kind === "approval") && !questions) return false;
		if (alert.kind === "finished" && !finished) return false;
		// Nothing alerts about what is on screen: the session you're looking
		// at, or a notice while the Board, which lists it, is up.
		if (alert.kind === "notice") return this.screen.kind !== "board";
		return !(this.screen.kind === "session" && this.screen.ref === alert.ref);
	}

	private holding(): boolean {
		return this.preferences.hold && this.holds.size > 0;
	}

	private remember(ref: string): void {
		this.recent = [ref, ...this.recent.filter((other) => other !== ref)].slice(0, RECENT_LIMIT);
	}

	private show(alert: Alert): void {
		const now = this.timer.now();
		const current = this.banner;
		if (current !== null && needsYou(alert) && current.alerts.every(needsYou) && now - this.shownAt < COALESCE_MS) {
			// Within 5 seconds, alerts about sessions that need you combine into
			// one banner, each session once (ruling 6).
			const joined = current.alerts.some((other) => subject(other) === subject(alert));
			this.banner = {
				id: current.id,
				alerts: joined
					? current.alerts.map((other) => (subject(other) === subject(alert) ? alert : other))
					: [...current.alerts, alert],
			};
		} else {
			this.stopBanner();
			this.banner = { id: this.nextId++, alerts: [alert] };
			if (alert.kind !== "finished") this.buzz(alert.kind === "failed" ? "warning" : "light");
		}
		this.shownAt = now;
		this.armExpiry();
		this.publish();
	}

	private release(): void {
		this.releasing = null;
		const waiting = this.held.filter((alert) => this.wanted(alert));
		this.held = [];
		const sessions = waiting.filter(needsYou);
		if (sessions.length > 1) {
			// Held banners show when you leave, combined (spec 13.3).
			this.stopBanner();
			this.banner = { id: this.nextId++, alerts: sessions };
			this.shownAt = this.timer.now();
			this.buzz("light");
			this.armExpiry();
			this.publish();
			return;
		}
		// A held notice shows only when no session waits; the Board lists it
		// either way (the prototype's releaseHeld).
		const next = sessions[0] ?? [...waiting].reverse().find((alert) => alert.kind === "notice");
		if (next === undefined) this.publish();
		else this.show(next);
	}

	private scheduleRelease(): void {
		this.cancelRelease();
		if (this.held.length > 0) this.releasing = this.timer.setTimeout(() => this.release(), RELEASE_MS);
	}

	private cancelRelease(): void {
		if (this.releasing !== null) this.timer.clearTimeout(this.releasing);
		this.releasing = null;
	}

	private armExpiry(): void {
		if (this.expiry !== null) this.timer.clearTimeout(this.expiry);
		this.expiry = this.timer.setTimeout(() => this.expire(), BANNER_MS);
	}

	private expire(): void {
		this.expiry = null;
		// A banner never goes away while a finger is on it.
		if (this.touching) {
			this.armExpiry();
			return;
		}
		this.banner = null;
		this.publish();
	}

	private stopBanner(): void {
		if (this.expiry !== null) this.timer.clearTimeout(this.expiry);
		this.expiry = null;
		this.touching = false;
		this.banner = null;
	}

	private buzz(kind: Haptic): void {
		if (this.preferences.haptics) this.haptic(kind);
	}

	private publish(): void {
		this.snapshot = { banner: this.banner, held: this.held.length, recent: this.recent };
		for (const listener of [...this.listeners]) listener();
	}
}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/alerts/alertCenter.test.ts && npm run check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/alerts/alertCenter.ts mobile-native/src/alerts/alertCenter.test.ts
git commit -m "feat(native): the in-app alert center: banners, coalescing, holds and Next's order"
```

### Task 4: Alerts from what the hub says

**Files:**
- Create: `mobile-native/src/alerts/alertEvents.ts`
- Test: `mobile-native/src/alerts/alertEvents.test.ts`

**Interfaces:**
- Consumes: `BoardState`, `LiveBands`, `liveBands` and `whyLine` from `src/board/attention.ts` (phase 2 Task 2); `Notice` from `src/board/notices.ts` (phase 2 Task 14: `{ kind: "signIn" | "host" | "plugin"; key: string; text: string; action: … }`); `AlertCenter`, `NeedsYouKind`, `NoticeAlert` and `SessionAlert` (Task 3).
- Produces:
  - `type SessionStates = ReadonlyMap<string, BoardState>`
  - `detectSessionAlerts(previous: SessionStates | null, bands: LiveBands, offlineRefs: ReadonlySet<string>): { alerts: SessionAlert[]; states: Map<string, BoardState> }`
  - `detectNoticeAlerts(previous: ReadonlySet<string> | null, notices: readonly Notice[]): { alerts: NoticeAlert[]; keys: Set<string> }`
  - `class AlertFeed`: constructor `(center: Pick<AlertCenter, "offer">)`; `rebaseline()`, `observeSessions(bands, offlineRefs)`, `observeNotices(notices)`

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/alerts/alertEvents.test.ts
import type { NavigationSessionSummary } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import { liveBands, whyLine } from "../board/attention";
import type { Notice } from "../board/notices";
import type { Alert } from "./alertCenter";
import { AlertFeed, detectNoticeAlerts, detectSessionAlerts } from "./alertEvents";

const row = (ref: string, over: Partial<NavigationSessionSummary> = {}): NavigationSessionSummary => ({
	ref,
	host_id: "local",
	session_id: ref,
	title: `Session ${ref}`,
	project: "evener",
	state: "idle",
	kind: "session",
	live: true,
	children: [],
	...over,
});
const never = () => false;
const bands = (live: NavigationSessionSummary[], needsYou: NavigationSessionSummary[] = []) =>
	liveBands(live, needsYou, never);
const none = new Set<string>();

describe("session alerts (spec 13.3)", () => {
	it("says nothing about the first read on a connection", () => {
		const first = detectSessionAlerts(null, bands([row("a", { state: "errored" })]), none);
		expect(first.alerts).toEqual([]);
		expect(first.states.get("a")).toBe("failed");
	});

	it("alerts when a session starts needing you, and again when the reason changes", () => {
		const start = detectSessionAlerts(null, bands([row("a", { state: "active" })]), none).states;
		const asking = row("a", { state: "awaiting", ask_pending: true });
		const asked = detectSessionAlerts(start, bands([asking]), none);
		expect(asked.alerts).toEqual([
			{ kind: "question", ref: "a", title: "Session a", why: whyLine({ row: asking, state: "question" }) },
		]);
		const failed = detectSessionAlerts(asked.states, bands([row("a", { state: "errored" })]), none);
		expect(failed.alerts.map((alert) => alert.kind)).toEqual(["failed"]);
		expect(detectSessionAlerts(failed.states, bands([row("a", { state: "errored" })]), none).alerts).toEqual([]);
	});

	it("alerts for a session that shows up already needing you", () => {
		const start = detectSessionAlerts(null, bands([]), none).states;
		const later = detectSessionAlerts(start, bands([], [row("b", { state: "awaiting", ask_pending: true })]), none);
		expect(later.alerts.map((alert) => alert.ref)).toEqual(["b"]);
	});

	it("alerts for an approval the hub promotes into Needs you", () => {
		const start = detectSessionAlerts(null, bands([row("c", { state: "active" })]), none).states;
		const later = detectSessionAlerts(start, bands([row("c", { state: "active" })], [row("c", { state: "active" })]), none);
		expect(later.alerts.map((alert) => alert.kind)).toEqual(["approval"]);
	});

	it("alerts a finished turn only when the session was working", () => {
		const start = detectSessionAlerts(
			null,
			bands([row("a", { state: "active" }), row("b", { state: "awaiting", ask_pending: true })]),
			none,
		).states;
		const later = detectSessionAlerts(start, bands([row("a", { state: "awaiting" }), row("b", { state: "awaiting" })]), none);
		expect(later.alerts).toEqual([{ kind: "finished", ref: "a", title: "Session a", why: null }]);
	});

	it("stays quiet when a host goes offline and comes back", () => {
		const asking = row("p", { state: "awaiting", ask_pending: true, host_id: "paradise-park" });
		const start = detectSessionAlerts(null, bands([asking]), none).states;
		const away = detectSessionAlerts(start, bands([{ ...asking, offline: true }]), new Set(["p"]));
		expect(away.alerts).toEqual([]);
		expect(detectSessionAlerts(away.states, bands([asking]), none).alerts).toEqual([]);
	});
});

const signIn: Notice = { kind: "signIn", key: "signIn:codex", text: "codex-jesse-fsck.com sign-in expired", action: "Sign in" };
const hostDown: Notice = { kind: "host", key: "host:paradise-park", text: "paradise-park is offline · 3 sessions", action: "Details" };
const brokenPlugin: Notice = { kind: "plugin", key: "plugin:go", text: "go is broken", action: "Plugins" };

describe("notice alerts (ruling 5)", () => {
	it("alerts a sign-in or host notice when it appears, never a broken plugin, and nothing on the first read", () => {
		const first = detectNoticeAlerts(null, [hostDown]);
		expect(first.alerts).toEqual([]);
		const later = detectNoticeAlerts(first.keys, [hostDown, signIn, brokenPlugin]);
		expect(later.alerts).toEqual([{ kind: "notice", key: "signIn:codex", title: "codex-jesse-fsck.com sign-in expired" }]);
	});

	it("alerts again for a notice that went away and came back", () => {
		const first = detectNoticeAlerts(null, [hostDown]);
		const cleared = detectNoticeAlerts(first.keys, []);
		expect(detectNoticeAlerts(cleared.keys, [hostDown]).alerts.map((alert) => alert.key)).toEqual(["host:paradise-park"]);
	});
});

describe("the feed", () => {
	it("keeps separate baselines for sessions and notices, and starts over for a new client", () => {
		const offered: Alert[] = [];
		const feed = new AlertFeed({ offer: (alert) => offered.push(alert) });
		feed.observeSessions(bands([row("a", { state: "active" })]), none);
		feed.observeNotices([hostDown]);
		feed.observeSessions(bands([row("a", { state: "errored" })]), none);
		feed.observeNotices([hostDown, signIn]);
		expect(offered.map((alert) => alert.kind)).toEqual(["failed", "notice"]);
		feed.rebaseline();
		feed.observeSessions(bands([row("a", { state: "awaiting", ask_pending: true })]), none);
		feed.observeNotices([brokenPlugin]);
		expect(offered).toHaveLength(2);
	});
});
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd mobile-native && npx vitest run src/alerts/alertEvents.test.ts`
Expected: FAIL: `Cannot find module './alertEvents'`.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/alerts/alertEvents.ts
// Turns what the hub says into alerts (spec 13.3): a session that starts
// needing you or needs you for a new reason, a working session that finishes
// its turn, and a hub notice that appears. Each source's first read on a
// connection is its baseline and alerts nothing (ruling 2), so opening the
// app never drops a pile of banners on what the Board already shows.
import { type BoardState, type LiveBands, whyLine } from "../board/attention";
import type { Notice } from "../board/notices";
import type { AlertCenter, NeedsYouKind, NoticeAlert, SessionAlert } from "./alertCenter";

export type SessionStates = ReadonlyMap<string, BoardState>;

const NEEDS_YOU = new Set<BoardState>(["failed", "question", "approval", "warning", "restartNeeded"]);

export function detectSessionAlerts(
	previous: SessionStates | null,
	bands: LiveBands,
	offlineRefs: ReadonlySet<string>,
): { alerts: SessionAlert[]; states: Map<string, BoardState> } {
	const states = new Map<string, BoardState>();
	// A row on an unreachable host keeps what it last said, so the host coming
	// back is not news about the session (ruling 3).
	for (const ref of offlineRefs) {
		const state = previous?.get(ref);
		if (state !== undefined) states.set(ref, state);
	}
	const alerts: SessionAlert[] = [];
	for (const item of [...bands.needsYou, ...bands.finished, ...bands.working, ...bands.idle]) {
		const { ref, title } = item.row;
		const before = previous?.get(ref);
		states.set(ref, item.state);
		if (previous === null) continue;
		if (NEEDS_YOU.has(item.state) && item.state !== before)
			alerts.push({ kind: item.state as NeedsYouKind, ref, title, why: whyLine(item) });
		else if (item.state === "finished" && before === "working")
			alerts.push({ kind: "finished", ref, title, why: null });
	}
	return { alerts, states };
}

// A broken plugin never alerts: the Board checks plugins only while it is on
// screen, where the notice row already shows it (ruling 5).
const ALERTING_NOTICES: ReadonlySet<Notice["kind"]> = new Set(["signIn", "host"]);

export function detectNoticeAlerts(
	previous: ReadonlySet<string> | null,
	notices: readonly Notice[],
): { alerts: NoticeAlert[]; keys: Set<string> } {
	const keys = new Set(notices.map((notice) => notice.key));
	if (previous === null) return { alerts: [], keys };
	const alerts = notices
		.filter((notice) => ALERTING_NOTICES.has(notice.kind) && !previous.has(notice.key))
		.map((notice): NoticeAlert => ({ kind: "notice", key: notice.key, title: notice.text }));
	return { alerts, keys };
}

/** Feeds the alert center from successive reads. Sessions and notices keep
 * separate baselines because they load separately. */
export class AlertFeed {
	private sessions: SessionStates | null = null;
	private notices: ReadonlySet<string> | null = null;

	constructor(private readonly center: Pick<AlertCenter, "offer">) {}

	/** A new client (ruling 2): its first reads are baselines again. */
	rebaseline(): void {
		this.sessions = null;
		this.notices = null;
	}

	observeSessions(bands: LiveBands, offlineRefs: ReadonlySet<string>): void {
		const detected = detectSessionAlerts(this.sessions, bands, offlineRefs);
		this.sessions = detected.states;
		for (const alert of detected.alerts) this.center.offer(alert);
	}

	observeNotices(notices: readonly Notice[]): void {
		const detected = detectNoticeAlerts(this.notices, notices);
		this.notices = detected.keys;
		for (const alert of detected.alerts) this.center.offer(alert);
	}
}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/alerts/alertEvents.test.ts && npm run check`
Expected: PASS. If `Notice` in `src/board/notices.ts` landed with a different shape, keep its field names and change this file's three uses (`kind`, `key`, `text`) to match.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/alerts/alertEvents.ts mobile-native/src/alerts/alertEvents.test.ts
git commit -m "feat(native): alerts from successive navigation and notice reads"
```

### Task 5: The In-app alerts preferences

Spec 12's Hub > In-app alerts: banners for failures (on), questions and approvals (on), finished results (off); hold alerts while reading (on); haptics (on). They describe how this phone interrupts you, so they're kept per device, not per hub.

**Files:**
- Create: `mobile-native/src/alerts/alertPreferences.ts` and `mobile-native/src/alerts/nativeAlertPreferences.ts`
- Test: `mobile-native/src/alerts/alertPreferences.test.ts`

**Interfaces:**
- Consumes: `AlertPreferences` and `DEFAULT_ALERT_PREFERENCES` (Task 3).
- Produces:
  - `interface PreferenceStorage { getItemSync(key: string): string | null; setItemSync(key: string, value: string): void }`
  - `ALERT_PREFERENCES_KEY = "evener.native.alert-preferences"`
  - `class AlertPreferenceStore`: constructor `(storage: PreferenceStorage)`; `getSnapshot(): AlertPreferences`, `subscribe(listener)`, `set(change: Partial<AlertPreferences>)`
  - `alertPreferences(): AlertPreferenceStore` from `nativeAlertPreferences.ts`, one store per process over `expo-sqlite/kv-store`

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/alerts/alertPreferences.test.ts
import { describe, expect, it } from "vitest";
import { DEFAULT_ALERT_PREFERENCES } from "./alertCenter";
import { ALERT_PREFERENCES_KEY, AlertPreferenceStore, type PreferenceStorage } from "./alertPreferences";

function memory(values = new Map<string, string>()): PreferenceStorage & { values: Map<string, string> } {
	return {
		values,
		getItemSync: (key) => values.get(key) ?? null,
		setItemSync: (key, value) => void values.set(key, value),
	};
}

describe("In-app alerts preferences (spec 12)", () => {
	it("start at the spec's defaults", () => {
		expect(new AlertPreferenceStore(memory()).getSnapshot()).toEqual({
			failures: true,
			questions: true,
			finished: false,
			hold: true,
			haptics: true,
		});
	});

	it("remember a change across launches", () => {
		const storage = memory();
		new AlertPreferenceStore(storage).set({ finished: true, haptics: false });
		expect(new AlertPreferenceStore(storage).getSnapshot()).toEqual({
			...DEFAULT_ALERT_PREFERENCES,
			finished: true,
			haptics: false,
		});
		expect(storage.values.has(ALERT_PREFERENCES_KEY)).toBe(true);
	});

	it("read damaged or foreign values as the defaults, field by field", () => {
		expect(new AlertPreferenceStore(memory(new Map([[ALERT_PREFERENCES_KEY, "{not json"]]))).getSnapshot()).toEqual(
			DEFAULT_ALERT_PREFERENCES,
		);
		const stored = JSON.stringify({ failures: false, finished: "yes", extra: true });
		expect(new AlertPreferenceStore(memory(new Map([[ALERT_PREFERENCES_KEY, stored]]))).getSnapshot()).toEqual({
			...DEFAULT_ALERT_PREFERENCES,
			failures: false,
		});
	});

	it("keep working in memory when storage fails", () => {
		const broken: PreferenceStorage = {
			getItemSync: () => {
				throw new Error("disk");
			},
			setItemSync: () => {
				throw new Error("disk");
			},
		};
		const store = new AlertPreferenceStore(broken);
		store.set({ hold: false });
		expect(store.getSnapshot().hold).toBe(false);
	});

	it("tell subscribers about each change", () => {
		const store = new AlertPreferenceStore(memory());
		let calls = 0;
		const stop = store.subscribe(() => calls++);
		const before = store.getSnapshot();
		store.set({ questions: false });
		expect(calls).toBe(1);
		expect(store.getSnapshot()).not.toBe(before);
		stop();
		store.set({ questions: true });
		expect(calls).toBe(1);
	});
});
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd mobile-native && npx vitest run src/alerts/alertPreferences.test.ts`
Expected: FAIL: `Cannot find module './alertPreferences'`.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/alerts/alertPreferences.ts
// Hub > In-app alerts (spec 12), kept per device: the switches describe how
// this phone interrupts you, whichever hub it is connected to.
import { type AlertPreferences, DEFAULT_ALERT_PREFERENCES } from "./alertCenter";

export interface PreferenceStorage {
	getItemSync(key: string): string | null;
	setItemSync(key: string, value: string): void;
}

export const ALERT_PREFERENCES_KEY = "evener.native.alert-preferences";

function read(storage: PreferenceStorage): AlertPreferences {
	let stored: unknown;
	try {
		const raw = storage.getItemSync(ALERT_PREFERENCES_KEY);
		stored = raw ? JSON.parse(raw) : null;
	} catch {
		return DEFAULT_ALERT_PREFERENCES;
	}
	if (typeof stored !== "object" || stored === null || Array.isArray(stored)) return DEFAULT_ALERT_PREFERENCES;
	const preferences = { ...DEFAULT_ALERT_PREFERENCES };
	for (const key of Object.keys(DEFAULT_ALERT_PREFERENCES) as (keyof AlertPreferences)[]) {
		const value = (stored as Record<string, unknown>)[key];
		if (typeof value === "boolean") preferences[key] = value;
	}
	return preferences;
}

export class AlertPreferenceStore {
	private value: AlertPreferences;
	private listeners = new Set<() => void>();

	constructor(private readonly storage: PreferenceStorage) {
		this.value = read(storage);
	}

	getSnapshot = (): AlertPreferences => this.value;

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};

	set(change: Partial<AlertPreferences>): void {
		this.value = { ...this.value, ...change };
		try {
			this.storage.setItemSync(ALERT_PREFERENCES_KEY, JSON.stringify(this.value));
		} catch {
			// The choice still holds for this launch.
		}
		for (const listener of [...this.listeners]) listener();
	}
}
```

```ts
// mobile-native/src/alerts/nativeAlertPreferences.ts
import { Storage } from "expo-sqlite/kv-store";
import { AlertPreferenceStore } from "./alertPreferences";

let store: AlertPreferenceStore | undefined;

/** The one store the banners and Hub > In-app alerts share, so a switch
 * flipped in Hub reaches the next banner at once. */
export function alertPreferences(): AlertPreferenceStore {
	store ??= new AlertPreferenceStore(Storage);
	return store;
}
```

Task 6's haptics and Task 8's provider read the store, and Task 9's page reads and writes it.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/alerts && npm run check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/alerts/alertPreferences.ts mobile-native/src/alerts/nativeAlertPreferences.ts mobile-native/src/alerts/alertPreferences.test.ts <phase 5's alerts section file>
git commit -m "feat(native): In-app alerts preferences, kept per device"
```


### Task 6: Haptics, behind one switch

Phase 3 held every haptic for this phase (its ruling 5), so that all of them answer to Hub > In-app alerts' Haptics switch. Spec 16.6 names them: "selection tick on chips, segments and lateral session moves; light impact on send; success on answer sent and approval allowed; warning on a failure banner; rigid on destructive confirmations."

**Files:**
- Modify: `mobile-native/package.json`, `package-lock.json` and `Podfile.lock` (`expo-haptics`, unless an earlier phase added it)
- Create: `mobile-native/src/haptics.ts` and `mobile-native/src/haptics.test.ts`
- Modify: each call site below

**Interfaces:**
- Consumes: `alertPreferences()` (Task 5).
- Produces: `type HapticKind = "selection" | "light" | "success" | "warning" | "rigid"` and `haptic(kind: HapticKind): void`.

**Call sites** (grep for each handler before editing; a site whose feature isn't on main yet is skipped and named in the PR description):
- `selection`: tapping a Board section chip or a Live summary count (phase 2 `BoardScreen`), choosing an Effort segment (phase 3's model sheet and phase 5's New session), choosing a detail level (phase 3's ⋯ menu), a Subagents filter chip (phase 4), and moving to another session with Next or a title-bar swipe (phase 3 PRs 11 and 12).
- `light`: Send (phase 3's `Composer` `onSend`, after the send is admitted).
- `success`: "Answer sent" / "Answers sent" and "Allowed once" (phase 3's dock, where it shows those toasts).
- `warning` and `light` for banners come from the alert center (Task 8 wires them).
- `rigid`: the confirmation of Shut down, Delete (a saved session, a category, a host, a plugin) and "Delete draft", at the moment the destructive button is pressed.

- [ ] **Step 1: Add the dependency**

From `mobile-native`, with `[ -L node_modules ]` printing nothing: `npx expo install expo-haptics`. Expected: `package.json` gains `expo-haptics` at the version Expo SDK 57 pins. Then read `node_modules/expo-haptics/build/Haptics.d.ts` and confirm the five calls below exist under these names: `selectionAsync`, `impactAsync` with `ImpactFeedbackStyle.Light` and `ImpactFeedbackStyle.Rigid`, and `notificationAsync` with `NotificationFeedbackType.Success` and `NotificationFeedbackType.Warning`. If a name differs, use the installed one and say so in the commit. Regenerate the CocoaPods lock exactly as phase 2's Task 4 Step 6 does; the diff adds the `ExpoHaptics` pod only.

- [ ] **Step 2: Write the failing test**

```ts
// mobile-native/src/haptics.test.ts
import { beforeEach, expect, it, vi } from "vitest";
import { AlertPreferenceStore } from "./alerts/alertPreferences";

const played = vi.hoisted(() => [] as string[]);
vi.mock("expo-haptics", () => ({
	ImpactFeedbackStyle: { Light: "Light", Rigid: "Rigid" },
	NotificationFeedbackType: { Success: "Success", Warning: "Warning" },
	selectionAsync: async () => void played.push("selection"),
	impactAsync: async (style: string) => void played.push(`impact:${style}`),
	notificationAsync: async (type: string) => void played.push(`notification:${type}`),
}));
const store = vi.hoisted(() => ({ current: null as AlertPreferenceStore | null }));
vi.mock("./alerts/nativeAlertPreferences", () => ({ alertPreferences: () => store.current }));

import { haptic } from "./haptics";

beforeEach(() => {
	played.length = 0;
	const values = new Map<string, string>();
	store.current = new AlertPreferenceStore({
		getItemSync: (key) => values.get(key) ?? null,
		setItemSync: (key, value) => void values.set(key, value),
	});
});

it("plays spec 16.6's feedback for each kind", () => {
	for (const kind of ["selection", "light", "success", "warning", "rigid"] as const) haptic(kind);
	expect(played).toEqual([
		"selection",
		"impact:Light",
		"notification:Success",
		"notification:Warning",
		"impact:Rigid",
	]);
});

it("plays nothing when Hub > In-app alerts turns haptics off", () => {
	store.current?.set({ haptics: false });
	haptic("warning");
	expect(played).toEqual([]);
});
```

- [ ] **Step 3: Run it and watch it fail**

Run: `cd mobile-native && npx vitest run src/haptics.test.ts`
Expected: FAIL: `Cannot find module './haptics'`.

- [ ] **Step 4: Implement**

```ts
// mobile-native/src/haptics.ts
// Spec 16.6's haptics, all behind Hub > In-app alerts' one Haptics switch
// (phase 3's ruling 5). A haptic is feedback, never information, so a
// failure to play one is ignored.
import * as Haptics from "expo-haptics";
import { alertPreferences } from "./alerts/nativeAlertPreferences";

export type HapticKind = "selection" | "light" | "success" | "warning" | "rigid";

export function haptic(kind: HapticKind): void {
	if (!alertPreferences().getSnapshot().haptics) return;
	const playing =
		kind === "selection"
			? Haptics.selectionAsync()
			: kind === "light"
				? Haptics.impactAsync(Haptics.ImpactFeedbackStyle.Light)
				: kind === "rigid"
					? Haptics.impactAsync(Haptics.ImpactFeedbackStyle.Rigid)
					: Haptics.notificationAsync(
							kind === "success"
								? Haptics.NotificationFeedbackType.Success
								: Haptics.NotificationFeedbackType.Warning,
						);
	void playing.catch(() => undefined);
}
```

Then add `haptic(...)` at each call site above. A screen test that renders a site with a haptic mocks `expo-haptics` the way this test does, or mocks `../haptics` to a no-op.

- [ ] **Step 5: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/haptics.test.ts <the touched screens' tests> && npm run check && make test-native-bundle`
Expected: PASS. Build Release in the simulator and feel Send, a chip and a destructive confirmation on a device when one is at hand; the simulator plays none.

- [ ] **Step 6: Commit**

```bash
git add mobile-native/package.json mobile-native/package-lock.json mobile-native/Podfile.lock mobile-native/src/haptics.ts mobile-native/src/haptics.test.ts <the call sites>
git commit -m "feat(native): haptics for spec 16.6, behind the one Haptics switch"
```

Open PR B: "feat(native): the in-app alert center and haptics (phase 6, PR B)". The description says PR C is the center's first consumer.

---

## PR C: banners on every screen

PR C puts the alert center on screen: the banner, and the provider that feeds it from the hub and opens what you tap.

### Task 7: The banner

**Files:**
- Create: `mobile-native/src/alerts/AlertBanner.tsx`, `mobile-native/src/alerts/bannerGesture.ts` and `mobile-native/src/board/boardJump.ts`
- Test: `mobile-native/src/alerts/AlertBanner.test.tsx`, `mobile-native/src/alerts/bannerGesture.test.ts` and `mobile-native/src/board/boardJump.test.ts`
- Modify: `mobile-native/src/board/BoardScreen.tsx` (it scrolls to Needs you when asked)

**Interfaces:**
- Consumes: `Banner`, `Alert` and `needsYou` (Task 3); `StateMark` and `BoardState` (phase 2); `WhyLine` (phase 2).
- Produces:
  - `<AlertBanner banner={Banner} onTap={() => void} onDismiss={() => void} onTouch={(down: boolean) => void} />`
  - `bannerLabel(banner: Banner): string`, the one sentence VoiceOver reads
  - `swipeDismisses(dy: number, vy: number): boolean`
  - `type BoardSection = "needsYou"`, `requestBoardJump(section: BoardSection): void` and `onBoardJump(listener: (section: BoardSection) => void): () => void`

**Requirements (spec 13.3, 16.3, 16.6):**
1. **The card.** 8pt from each screen edge (the prototype's inset, so it reads as a notification rather than a row), an 18pt continuous radius (`borderCurve: "continuous"`), `palette.surface`, a 1pt border in `attentionEdge` (`accentEdge` for a finished result), the floating shadow the phase 3 toast uses, and 11pt by 14pt padding. A 24pt mark column, then the text.
2. **The mark.** One session: `StateMark` for its state (the alert kinds map one to one onto phase 2's `BoardState`). A notice: `exclamationmark.triangle.fill` in `attention`. A coalesced banner: a 22pt `attentionBg` disc holding the count in `attentionInk`, semibold 13 with tabular figures.
3. **The words.**
   - Line 1: the title in SF Pro semibold 15/20, `inkHi`, one line with tail truncation, and a trailing "now" in 12pt `inkLow`. A coalesced banner's title is "N sessions need you".
   - Line 2, 14/19: for a session, its `WhyLine`: the word semibold in `dangerInk` or `attentionInk`, then " · " and the reason in `inkMid`. A coalesced banner says "Tap to see them on the Board." (the prototype's line: the title can't tell you a tap goes to the Board rather than a session). A notice and a finished result have no line 2 until S11 and S1 give them one.
4. **Touch.** Press-in calls `onTouch(true)` and press-out `onTouch(false)`, so a finger keeps the banner (the center re-arms its timer). A tap calls `onTap`. An upward drag follows the finger (never downward) and, on release, `swipeDismisses(dy, vy)` decides between `onDismiss` and a spring back. Use React Native's `PanResponder` claiming only upward moves, so `Pressable` keeps taps; no new gesture dependency.
5. **Motion.** A new banner (a new `id`) drops in from 24pt above with a spring that settles in about 300ms, fading from 0 to 1. The same banner updating in place (a session joining it) doesn't move. With Reduce Motion (`AccessibilityInfo.isReduceMotionEnabled()` and its `reduceMotionChanged` event), it only fades in, over 200ms.
6. **VoiceOver.** The card is one button whose label is `bannerLabel`: the title, then the why line's word and reason ("Fix Endless Provider Retry Loop, Failed, open the session to see what went wrong"). It carries `accessibilityActions` `activate` (tap) and `escape` (Dismiss). When a banner drops in or its label changes, `AccessibilityInfo.announceForAccessibility(label)` runs once.
7. **The Board jump.** `boardJump.ts` below. `BoardScreen` subscribes with `onBoardJump` while mounted and scrolls to the Needs you band with the same scroll its Live summary's "need you" count uses.

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/alerts/bannerGesture.test.ts
import { expect, it } from "vitest";
import { swipeDismisses } from "./bannerGesture";

it.each([
	[-31, 0, true],
	[-30, 0, false],
	[-10, -0.6, true],
	[-10, -0.4, false],
	[20, -2, false],
	[0, 0, false],
])("a drag of %ipt at %f pt/ms dismisses: %s", (dy, vy, expected) => {
	expect(swipeDismisses(dy, vy)).toBe(expected);
});
```

```ts
// mobile-native/src/board/boardJump.test.ts
import { expect, it } from "vitest";
import { type BoardSection, onBoardJump, requestBoardJump } from "./boardJump";

it("reaches the Board, and a request made before it listens waits for it once", () => {
	requestBoardJump("needsYou");
	const heard: BoardSection[] = [];
	const stop = onBoardJump((section) => heard.push(section));
	expect(heard).toEqual(["needsYou"]);
	requestBoardJump("needsYou");
	expect(heard).toEqual(["needsYou", "needsYou"]);
	stop();
	requestBoardJump("needsYou");
	const later: BoardSection[] = [];
	onBoardJump((section) => later.push(section))();
	expect(heard).toHaveLength(2);
	expect(later).toEqual(["needsYou"]);
});
```

`AlertBanner.test.tsx` renders through `render` from `../renderNative.testkit`, with `react-native` from `nativeModuleMock()` plus `AccessibilityInfo` (`announceForAccessibility`, `isReduceMotionEnabled`, `addEventListener`), `Animated` and `PanResponder` stubs, and `expo-symbols` as `{ SymbolView: "SymbolView" }`. It covers:
- a question banner shows the title, "now", "Question" in `attentionInk` and the reason; a failure's word is in `dangerInk`;
- a coalesced banner of three shows "3 sessions need you", "Tap to see them on the Board." and a disc reading "3";
- a notice shows its title with the triangle mark and no second line; a finished banner has the `accentEdge` border;
- press-in and press-out call `onTouch(true)` and `onTouch(false)`; pressing calls `onTap`;
- the `escape` accessibility action calls `onDismiss`, and `bannerLabel` reads "Session a, Question, waiting for your answer";
- the announcement runs once per banner, and again only when the label changes.

- [ ] **Step 2: Run them and watch them fail**

Run: `cd mobile-native && npx vitest run src/alerts/bannerGesture.test.ts src/board/boardJump.test.ts src/alerts/AlertBanner.test.tsx`
Expected: FAIL: the modules don't exist.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/alerts/bannerGesture.ts
/** Whether an upward drag, released, dismisses the banner: past 30pt (the
 * prototype's threshold) or flicked upward. */
export function swipeDismisses(dy: number, vy: number): boolean {
	return dy < -30 || (dy < 0 && vy < -0.5);
}
```

```ts
// mobile-native/src/board/boardJump.ts
// Asks the Board to scroll to a section. The Board's route takes no params
// (restoring the app depends on that), so a coalesced banner's "3 sessions
// need you" reaches Needs you through here. A request made while no Board
// listens waits for the first one.
export type BoardSection = "needsYou";
type Listener = (section: BoardSection) => void;

const listeners = new Set<Listener>();
let waiting: BoardSection | null = null;

export function requestBoardJump(section: BoardSection): void {
	if (listeners.size === 0) {
		waiting = section;
		return;
	}
	for (const listener of [...listeners]) listener(section);
}

export function onBoardJump(listener: Listener): () => void {
	listeners.add(listener);
	if (waiting !== null) {
		const section = waiting;
		waiting = null;
		listener(section);
	}
	return () => {
		listeners.delete(listener);
	};
}
```

Then build `AlertBanner.tsx` to the requirements, and subscribe `BoardScreen` to `onBoardJump`.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/alerts src/board && npm run check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/alerts/AlertBanner.tsx mobile-native/src/alerts/AlertBanner.test.tsx mobile-native/src/alerts/bannerGesture.ts mobile-native/src/alerts/bannerGesture.test.ts mobile-native/src/board/boardJump.ts mobile-native/src/board/boardJump.test.ts mobile-native/src/board/BoardScreen.tsx
git commit -m "feat(native): the in-app alert banner"
```

### Task 8: Alerts on every screen

**Files:**
- Create: `mobile-native/src/alerts/alertScreen.ts` (pure), `mobile-native/src/alerts/AlertsProvider.tsx` and `mobile-native/src/alerts/AlertBannerHost.tsx`
- Modify: `mobile-native/App.tsx` (the provider around `Navigation`, a navigation container ref, the host inside `NavigationContainer`, and the screen it reports)
- Modify: phase 2's `src/board/Notices.tsx` if the way a notice opens its destination is inline there: export it as `openNotice(navigation, notice)` so a tapped notice banner opens the same place
- Test: `mobile-native/src/alerts/alertScreen.test.ts`, `mobile-native/src/alerts/AlertsProvider.test.tsx` and `mobile-native/src/alerts/AlertBannerHost.test.tsx`

**Interfaces:**
- Consumes: `AlertCenter` and its types (Task 3); `AlertFeed` (Task 4); `alertPreferences()` (Task 5); `haptic` (Task 6); `AlertBanner` and `requestBoardJump` (Task 7); phase 2's `createBoardController`, `liveBands`, `seenMarkers`, `notices` and `Notice`, and `openNotice` (extracted below); `getDefaultHeaderHeight` from `@react-navigation/elements` (2.9.40: `(layout, modalPresentation, topInset) => number`); `createNavigationContainerRef` and `StackActions` from `@react-navigation/native`.
- Produces:
  - From `alertScreen.ts`: `SHEET_ROUTES: ReadonlySet<string>` (`"Hub"` and `"NewSession"`, the routes phase 5 presents as sheets) and `alertScreenFor(route: { name: string; params?: object } | undefined): AlertScreen`.
  - From `AlertsProvider.tsx`: `<AlertsProvider>`, `useAlertCenter(): AlertCenter`, `useAlertSnapshot(): AlertSnapshot`, `useHoldAlerts(active: boolean): void`, `useHeldAlertCount(): number`, `useReportRoute(): (route: { name: string; params?: object } | undefined) => void` and `useNoticeFor(): (key: string) => Notice | undefined` (the notice the provider last read under that key).
  - `<AlertBannerHost navigation={NavigationContainerRef<Routes>} />`

**Requirements (spec 13.3; rulings 1-3, 5, 7):**
1. **One center, one feed.** `AlertsProvider` (inside `ConnectionProvider`) owns one `AlertCenter` on the real clock, whose haptic callback is `haptic(kind)`, and one `AlertFeed` over it. It follows `alertPreferences()` with `center.setPreferences` on mount and on every change.
2. **Its own reads.** It creates one `createBoardController()` per hub and calls `setClient(client)` whenever `useConnection().client` changes, as the Board does. It never pauses it: the Board and the Session pause theirs on blur (phase 3 ruling 33), and alerts must hear about sessions while you're anywhere. This costs a second set of navigation reads while the Board or a Session is in front; say so in the PR description.
3. **Feeding the center.**
   - Sessions: on each controller snapshot that is `loaded`, not `retained`, with the Needs you section complete (`needsYou.remaining === 0`, so the first observation is never a partial baseline whose later pages alert old news) and Live's first page loaded, call `feed.observeSessions(liveBands(live.rows, needsYou.rows, seenMarkers(hubId).isSeen), offlineRefs)`, where `offlineRefs` holds the refs of rows in either list with `offline: true`.
   - Notices: this controller reads auth as the Board's does (phase 2 Task 14), and never plugins, since a plugin notice never alerts (ruling 5). If phase 2's controller polls `evener/plugin/list` whenever it isn't paused, give `createBoardController` an option that turns those reads off, and use it here. Once the auth read has landed, call `feed.observeNotices(notices({ auth, sources, plugins: [], loadedRows }))` with the manifest's sources and the rows this controller has loaded (Live's first page and Needs you). Its host counts can be lower than the Board's, which loads more pages; phase 2 already calls that count a floor.
4. **Baselines.** A new client (a different non-null object: the app came back to the foreground, or a closed connection was replaced) calls `feed.rebaseline()`. Another hub calls `center.reset()` and `feed.rebaseline()`, and replaces the controller.
5. **What's on screen.** `App.tsx` gives `NavigationContainer` a ref from `createNavigationContainerRef<Routes>()` and passes the top route to `useReportRoute()`'s function from `onReady` and from `onStateChange`. That function calls `center.setScreen(alertScreenFor(route))`, and holds one hold token while the route is in `SHEET_ROUTES` (ruling 7).
6. **The host.** `AlertBannerHost` renders inside `NavigationContainer`, after the navigator, as an absolutely positioned container with `pointerEvents="box-none"`, `left` and `right` 0, and `top = getDefaultHeaderHeight(frame, false, insets.top) + 4` from `useSafeAreaFrame()` and `useSafeAreaInsets()`: just below the nav bar, never over it (spec 13.3; round 1 problem 3). It renders `AlertBanner` keyed by the banner's `id`, or nothing.
7. **A tap.** `center.tap()`, then:
   - a session: `navigation.dispatch(StackActions.push("Conversation", { hubId, ref, title }))`, with the alert's title, so Back returns to where you were (spec 6);
   - a coalesced banner: `navigation.dispatch(StackActions.popTo("Sessions"))`, then `requestBoardJump("needsYou")`;
   - a notice: the Board's own `openNotice(navigation, notice)` for `useNoticeFor()(key)`. A notice that resolved since the banner dropped in opens nothing.
8. **Swipe and touch** go to `center.dismiss()` and `center.touch(down)`.

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/alerts/alertScreen.test.ts
import { expect, it } from "vitest";
import { alertScreenFor, SHEET_ROUTES } from "./alertScreen";

it.each([
	[{ name: "Sessions" }, { kind: "board" }],
	[{ name: "Conversation", params: { hubId: "hub-1", ref: "local:a", title: "A" } }, { kind: "session", ref: "local:a" }],
	[{ name: "Conversation", params: {} }, { kind: "other" }],
	[{ name: "Reader", params: { hubId: "hub-1", sessionRef: "local:a" } }, { kind: "other" }],
	[undefined, { kind: "other" }],
] as const)("%o is %o", (route, screen) => {
	expect(alertScreenFor(route)).toEqual(screen);
});

it("counts phase 5's sheets as sheets", () => {
	expect([...SHEET_ROUTES].sort()).toEqual(["Hub", "NewSession"]);
});
```

`AlertsProvider.test.tsx` drives the provider through a scripted hub, the `boundary()` fake of `src/projectBrowser.test.ts` answering `evener/navigation/read` with `wireV2` as phase 2's `boardData.test.ts` does. Mock `../ConnectionProvider` (a `useConnection` whose `client` the test swaps), `./nativeAlertPreferences` (a store over memory), `../haptics` and `../board/nativeBoardMemory`. A probe component reads `useAlertSnapshot()`. Cover:
- the first reads (a session already failed) show no banner;
- an invalidation that re-reads Needs you with a new question shows that session's banner;
- a Needs you section with `remaining > 0` is not observed until its last page lands, so its second page alerts nothing;
- swapping in a new client whose first reads carry another new failure shows no banner (ruling 2);
- a row that goes `offline: true` and comes back alerts nothing (ruling 3);
- a provider sign-in that expires after the first auth read (an `evener/auth/updated` notification, then a read with `needsLogin`) alerts once, and the scripted hub never sees `evener/plugin/list` (ruling 5);
- `useHoldAlerts(true)` in the probe holds the question's banner, and `useHeldAlertCount()` reads 1;
- reporting the route `{ name: "Hub" }` holds a new question's banner, and reporting `{ name: "Sessions" }` shows it 200ms later.

`AlertBannerHost.test.tsx` mocks `react-native-safe-area-context` (`useSafeAreaInsets` returns `{ top: 47, bottom: 34, left: 0, right: 0 }`, `useSafeAreaFrame` returns 393 by 852) and passes a navigation ref stub that records `dispatch` calls. Cover:
- the container's `top` is 95 (47 + 44 + 4) and it renders nothing without a banner;
- tapping a session banner dispatches a push of `"Conversation"` with that ref and title;
- tapping a coalesced banner dispatches `popTo("Sessions")` and a `requestBoardJump("needsYou")` reaches a listener;
- tapping a notice banner calls `openNotice` (mock `../board/Notices`) with the notice `useNoticeFor` holds under its key, and calls nothing once that notice is gone.

- [ ] **Step 2: Run them and watch them fail**

Run: `cd mobile-native && npx vitest run src/alerts/alertScreen.test.ts src/alerts/AlertsProvider.test.tsx src/alerts/AlertBannerHost.test.tsx`
Expected: FAIL: the modules don't exist.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/alerts/alertScreen.ts
// What the top route means for alerts (spec 13.3): the Board lists notices,
// a session answers its own alerts, and a sheet holds banners (ruling 7).
import type { AlertScreen } from "./alertCenter";

/** Routes phase 5 presents as sheets (presentation: "modal"): a banner there
 * would cover the sheet's own Cancel and Done. */
export const SHEET_ROUTES: ReadonlySet<string> = new Set(["Hub", "NewSession"]);

export function alertScreenFor(route: { name: string; params?: object } | undefined): AlertScreen {
	if (route?.name === "Sessions") return { kind: "board" };
	const ref = (route?.params as { ref?: unknown } | undefined)?.ref;
	if (route?.name === "Conversation" && typeof ref === "string") return { kind: "session", ref };
	return { kind: "other" };
}
```

Then build the provider, the host and the `App.tsx` wiring to the requirements.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/alerts && npm run check && make test-native-bundle`
Expected: PASS. Build Release in the simulator against the demo fleet (Task 17's alert script, or a real hub): open a session, have another session ask a question, and see the banner drop in below the nav bar; tap it and come Back.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/alerts/alertScreen.ts mobile-native/src/alerts/alertScreen.test.ts mobile-native/src/alerts/AlertsProvider.tsx mobile-native/src/alerts/AlertsProvider.test.tsx mobile-native/src/alerts/AlertBannerHost.tsx mobile-native/src/alerts/AlertBannerHost.test.tsx mobile-native/App.tsx <src/board/Notices.tsx if changed>
git commit -m "feat(native): in-app alerts on every screen"
```

Open PR C: "feat(native): in-app alert banners (phase 6, PR C)".

---

## PR D: holds, the In-app alerts page and Next

PR D finishes the alerts: their switches, their quiet while you read, type or use a sheet, and the order Next serves.

### Task 9: The In-app alerts page

Phase 5 left the page to this phase (its ruling 11), so its switches arrive with what they control.

**Files:**
- Create: `mobile-native/src/hub/AlertsPage.tsx` and `mobile-native/src/hub/AlertsPage.test.tsx`
- Modify: `mobile-native/src/hub/HubSheet.tsx` (one route) and `mobile-native/src/hub/HubHome.tsx` (one row), following how phase 5 adds its other pages

**Interfaces:**
- Consumes: `alertPreferences()` (Task 5); from phase 5, `GroupedPage`, `GroupLabel`, `Group`, `Row`, `SwitchRow` and `GroupFooter` (`src/sheet/Grouped.tsx`, its Task 1) and `HubRoutes` (`src/hub/HubSheet.tsx`, its Task 3).
- Produces: `HubRoutes` gains `Alerts: undefined`, whose component is `AlertsPage`.

**Requirements (spec 12; the prototype's `hub.js`, `EV.sheets.alerts`):**
1. The Hub home's row sits in "This phone" between Display and Hubs, where the prototype puts it: `<Row icon="bubble.left" label="In-app alerts" chevron onPress={() => navigation.navigate("Alerts")} />`. The prototype's glyph is a speech bubble.
2. The page is titled "In-app alerts" in the Hub stack's header, like phase 5's other pages, and is a `GroupedPage`:
   - `GroupLabel` "Show a banner when", then a `Group` of three `SwitchRow`s: "A session fails" (`failures`), "A session asks a question or needs approval" (`questions`), and "A session finishes" (`finished`) with the `sub` "Finished results always land in Finished on the Board";
   - a second `Group`: "Hold alerts while reading or typing" (`hold`), with the `sub` "They show when you leave the document or send", and "Haptics" (`haptics`);
   - the `GroupFooter` "Lock-screen notifications are coming later. Until then, alerts show while Evener is open."
3. Each switch reads the store through `useSyncExternalStore(alertPreferences().subscribe, alertPreferences().getSnapshot)` and writes with `set`.
4. The page needs no connection, so nothing on it disables and it shows no `SheetStatus`.

- [ ] **Step 1: Write the failing test** (`AlertsPage.test.tsx`): it renders the five labels, the two second lines and the footer; each switch shows its stored value; toggling "A session finishes" stores `finished: true`; a change made elsewhere re-renders the switch. Mock `../alerts/nativeAlertPreferences` with a store over memory.
- [ ] **Step 2: Run it and watch it fail.** Run: `cd mobile-native && npx vitest run src/hub/AlertsPage.test.tsx`
- [ ] **Step 3: Implement** the page, its route and its row.
- [ ] **Step 4: Run it and watch it pass**, with phase 5's Hub tests, then `npm run check`.
- [ ] **Step 5: Commit** (`feat(native): the In-app alerts page`).

### Task 10: Quiet while you read, type or use a sheet

Spec 13.3: "in the Reader, the Artifact viewer, or while typing in the composer, banners are held; the Back button shows how many are waiting as an amber count." Ruling 7 adds sheets.

**Files:**
- Create: `mobile-native/src/alerts/HoldingModal.tsx` and `mobile-native/src/alerts/holdingModal.test.ts`
- Modify: phase 4's `src/reader/ReaderScreen.tsx` (a hold while focused, and the held count on Back), phase 3's `src/session/BackButton.tsx` (an optional label), phase 3's `src/session/Composer.tsx` (a hold while you type), and every production file that renders React Native's `Modal`
- Test: phase 4's `src/reader/ReaderScreen.test.tsx`, phase 3's `src/session/BackButton.test.tsx` and `Composer.test.tsx`

**Interfaces:**
- Consumes: `useHoldAlerts` and `useHeldAlertCount` (Task 8); phase 3's `<BackButton count onPress />` (its Task 33), whose accessibility label reads "Back, 4 others need you".
- Produces: `<HoldingModal {...ModalProps} />`, React Native's `Modal` holding alerts while `visible`; `BackButton` gains `label?: string`, which replaces its accessibility label.

**Requirements:**
1. **The Reader** calls `useHoldAlerts(useIsFocused())`. Phase 4 left its Back as the system back (its Task 14); it becomes a `headerLeft` of `` <BackButton count={held} label={held > 0 ? `Back, ${held} new while you read` : "Back"} onPress={navigation.goBack} /> ``, with `held = useHeldAlertCount()`: the amber count phase 3's Session Back uses, hidden at 0. The words are the prototype's (`panels.js`: "Back. 2 new while you read"), with phase 3's comma. Round 4 problem 6 is why it's a count and not a dot. The swipe-back gesture must still work: check it in the simulator.
2. **The composer** holds while its field is focused and its text isn't blank: `useHoldAlerts(focused && value.trim() !== "")`. Sending empties the field, so it lets banners go, as the Hub row promises.
3. **Sheets.** `HoldingModal` renders React Native's `Modal` with the same props and calls `useHoldAlerts(props.visible ?? true)`. Every production `Modal` becomes a `HoldingModal`.
4. **The guard.** `holdingModal.test.ts` fails when a production file other than `HoldingModal.tsx` imports `Modal` from `react-native`, so a sheet added later can't let a banner over it.

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/alerts/holdingModal.test.ts
// Ruling 7: a sheet holds banners. Every React Native Modal goes through
// HoldingModal, so a sheet added later can't let a banner over it.
import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";
import { expect, it } from "vitest";

const SRC = fileURLToPath(new URL("..", import.meta.url));

function productionFiles(dir: string): string[] {
	return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
		const full = path.join(dir, entry.name);
		if (entry.isDirectory()) return productionFiles(full);
		if (!/\.tsx?$/.test(entry.name) || /\.(test|testkit)\.tsx?$/.test(entry.name)) return [];
		return [full];
	});
}

function importsReactNativeModal(file: string): boolean {
	const source = ts.createSourceFile(file, readFileSync(file, "utf8"), ts.ScriptTarget.Latest, true);
	return source.statements.some(
		(statement) =>
			ts.isImportDeclaration(statement) &&
			ts.isStringLiteral(statement.moduleSpecifier) &&
			statement.moduleSpecifier.text === "react-native" &&
			statement.importClause?.namedBindings !== undefined &&
			ts.isNamedImports(statement.importClause.namedBindings) &&
			statement.importClause.namedBindings.elements.some(
				(element) => (element.propertyName ?? element.name).text === "Modal",
			),
	);
}

it("every sheet holds alerts while it is up", () => {
	const offenders = productionFiles(SRC)
		.filter((file) => path.relative(SRC, file) !== path.join("alerts", "HoldingModal.tsx"))
		.filter(importsReactNativeModal)
		.map((file) => path.relative(SRC, file));
	expect(offenders).toEqual([]);
});
```

Phase 4's Reader test: focused, it holds, and with one held alert Back's label reads "Back, 1 new while you read"; blurred, it lets go. Phase 3's `BackButton.test.tsx`: a `label` replaces the accessibility label, and without one it still reads "Back, 4 others need you". Phase 3's `Composer.test.tsx`: focusing an empty field holds nothing; typing holds; clearing the text or blurring lets go. Mock `../alerts/AlertsProvider` with a recording `useHoldAlerts` and a `useHeldAlertCount` the test sets.

- [ ] **Step 2: Run them and watch them fail**

Run: `cd mobile-native && npx vitest run src/alerts/holdingModal.test.ts <the Reader and Composer tests>`
Expected: FAIL. The guard lists every file that imports `Modal` from `react-native` (on main at `d0c0211be`, 22 files; phases 3 to 5 change the list), and nothing holds yet.

- [ ] **Step 3: Implement**

```tsx
// mobile-native/src/alerts/HoldingModal.tsx
// React Native's Modal, holding in-app alerts while it is up (ruling 7): a
// banner over a sheet would cover the sheet's own controls.
import { Modal, type ModalProps } from "react-native";
import { useHoldAlerts } from "./AlertsProvider";

export function HoldingModal(props: ModalProps) {
	useHoldAlerts(props.visible ?? true);
	return <Modal {...props} />;
}
```

Then swap each production `Modal` for `HoldingModal`, and add the Reader's and the composer's holds and the Reader's count.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/alerts <the Reader, Composer and touched sheets' tests> && npm run check`
Expected: PASS. In the simulator: open a plan in the Reader, have two sessions ask questions, see Back's amber 2 and no banner, go Back, and see "2 sessions need you".

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/alerts/HoldingModal.tsx mobile-native/src/alerts/holdingModal.test.ts <the Reader, Composer and sheet files>
git commit -m "feat(native): banners wait while you read, type or use a sheet"
```

### Task 11: Next serves what alerted you first

Phase 3's Next goes by Needs you order until this phase (its ruling 11). Spec 8.3: "The order: whichever session alerted you most recently (shown or held), then Needs you order." Round 4 problem 2 is why: a participant's question alert vanished, and Next went to a failed session instead.

**Files:**
- Modify: `mobile-native/src/session/fleetOrder.ts` (phase 3 Task 32): add `servedFirst`, and `nextSession` takes the alert center's order
- Modify: the session screen's Next wiring (phase 3 Task 33, in `screens.tsx`): pass the order, sort the touch-and-hold list the same way, and call `center.nextUsed()` when either moves you on
- Test: `mobile-native/src/session/fleetOrder.test.ts` and phase 3's Next cases in `mobile-native/src/ConversationScreen.send.test.tsx`

**Interfaces:**
- Consumes: `useAlertSnapshot().recent` and `useAlertCenter()` (Task 8); phase 3's `othersNeedingYou` and `nextSession` (its Task 32).
- Produces, from `fleetOrder.ts`:
  - `servedFirst<T extends { ref: string }>(needsYou: readonly T[], recent: readonly string[]): T[]`
  - `nextSession(bands: LiveBands, currentRef: string, recent: readonly string[]): NavigationSessionSummary | null`, which gains its third parameter

**Requirements:**
1. Next's destination is `nextSession(bands, ref, useAlertSnapshot().recent)`, and the touch-and-hold list shows the first eight of `servedFirst(othersNeedingYou(bands, ref), recent)`, the same order. Back's count still counts `othersNeedingYou`.
2. Moving on with Next or picking from the list calls `center.nextUsed()`: the held banners are Next's to serve now, so they don't drop in later (the prototype's `goNext`). Opening a session already drops it from `recent` (Task 3).

- [ ] **Step 1: Write the failing tests**

In `fleetOrder.test.ts`, import `servedFirst` beside the others, pass `[]` as the new last argument in phase 3's three `nextSession` cases, and add:

```ts
describe("Next serves what alerted you first (spec 8.3)", () => {
	const refs = (list: readonly { ref: string }[]) => list.map((entry) => entry.ref);

	it("puts whatever alerted most recently first, then keeps Needs you order", () => {
		const needsYou = [{ ref: "failed-old" }, { ref: "failed-new" }, { ref: "question" }, { ref: "approval" }];
		expect(refs(servedFirst(needsYou, ["approval", "question"]))).toEqual([
			"approval",
			"question",
			"failed-old",
			"failed-new",
		]);
	});

	it("ignores alerts about sessions that no longer need you, and changes nothing without alerts", () => {
		const needsYou = [{ ref: "a" }, { ref: "b" }];
		expect(refs(servedFirst(needsYou, ["gone", "b"]))).toEqual(["b", "a"]);
		expect(refs(servedFirst(needsYou, []))).toEqual(["a", "b"]);
	});

	it("sends Next to the session that alerted most recently, never the one on screen", () => {
		expect(nextSession(bands, "working", ["question"])?.ref).toBe("question");
		expect(nextSession(bands, "question", ["question"])?.ref).toBe("failed");
	});
});
```

Phase 3's Next cases in `ConversationScreen.send.test.tsx` gain one: with Needs you holding a failure then a question, and the question in `recent` (mock `../alerts/AlertsProvider` so `useAlertSnapshot` returns it and `useAlertCenter` returns a recording `nextUsed`), the capsule names the question's session, and pressing it calls `nextUsed` once.

- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/session/fleetOrder.test.ts src/ConversationScreen.send.test.tsx`. Expected: FAIL: `servedFirst` isn't exported, and Next still names the failure.

- [ ] **Step 3: Implement.** In `fleetOrder.ts`, replace phase 3's `nextSession` with:

```ts
/** The order Next serves (spec 8.3): whichever session alerted you most
 * recently, shown or held, then Needs you order. The prototype's nextQueue.
 * `recent` is the alert center's, most recent first. */
export function servedFirst<T extends { ref: string }>(needsYou: readonly T[], recent: readonly string[]): T[] {
	const rank = (row: T) => {
		const index = recent.indexOf(row.ref);
		return index === -1 ? recent.length : index;
	};
	return needsYou
		.map((row, order) => ({ row, order }))
		.sort((a, b) => rank(a.row) - rank(b.row) || a.order - b.order)
		.map(({ row }) => row);
}

/** Next's destination. */
export function nextSession(
	bands: LiveBands,
	currentRef: string,
	recent: readonly string[],
): NavigationSessionSummary | null {
	return servedFirst(othersNeedingYou(bands, currentRef), recent)[0] ?? null;
}
```

Then pass `useAlertSnapshot().recent` where the screen calls `nextSession`, sort the list with `servedFirst`, and call `useAlertCenter().nextUsed()` in both open paths.

- [ ] **Step 4: Run them and watch them pass**, then `npm run check`.
- [ ] **Step 5: Commit** (`feat(native): Next serves what alerted you first`). Open PR D: "feat(native): alerts wait while you read, and Next serves them first (phase 6, PR D)".

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
3. A message a Stop held before it left the phone (`state: "canceled"`) shows as a held ghost: "Held · you stopped this turn", with "Send now" and "Cancel" as buttons. Another client's rows stay out.
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
- Consumes: phase 3's `sendAction`, `SendSource`, `SendAction`, `composerPlaceholder` and `sendLabel`; `getNativeMutationRuntime().submit` and `nativeMutationTargetKey`; `buildComposerInput` from `@evener/appwire-client`; `haptic` (Task 6).
- Produces:
  - `sendAction(conversation, pendingMutations, connected)` keeps its signature. Offline it returns the action the message will take when it arrives, instead of `"none"`.
  - `sendLabel(action, questionPending, connected = true)`.
  - `interface OfflineTarget { hubId: string; ref: string; threadId: string; instanceId?: string | null }` and `offlineRequest(target: OfflineTarget, action: "send" | "queue" | "resume", input: InputItem[]): ConversationMutationRequest`, from `src/outbox/offlineSend.ts`.

**Requirements (spec 8.5 and 14; rulings 12 to 14):**
1. **Routing offline.** A message waits in the outbox, and by the time it arrives another turn may have started, where `turn/start` is refused. So offline, Send queues whenever the harness can: the daemon runs a queued message at once on an idle session (`clientMutationQueue` wakes it, `agent/session_client_mutation_queue.go:107-190`) and holds it behind a running one. A shut-down session resumes on a send. A paused session, one that needs a restart, and one whose harness takes neither stay `"none"`.
2. **What Send admits offline.** When the screen isn't connected and the session loaded since launch (`store.getState().conversation` is set), Send is enabled on the same draft conditions as online, minus `ready`. It runs `document.submit(async (text, images) => …)`, which submits `offlineRequest({ hubId, ref, threadId, instanceId }, action, buildComposerInput(text, images))` to `getNativeMutationRuntime()`, plays `haptic("light")`, and returns `true`. The ghost shows at once through the durable pending rows the screen already follows (phase 3 Task 8).
3. **Never loaded.** A session that hasn't loaded since launch has no instance to fence with (ruling 12), so its Send stays disabled offline, and the draft stays.
4. **Words.** The placeholder is `composerPlaceholder` of the connected routing, so it describes the session rather than the outbox. Send's accessibility label offline is "Send when you're back online", or "Send answer when you're back online" while a question is pending.
5. **Answers.** The dock's "Send answer" admits offline through the same `offlineRequest` path, with the composed text as one text input, and marks the batch sent as `sendAnswers` does online.
6. **The refusal left online.** `NATIVE_MUTATION_HOST_UNAVAILABLE` becomes "Couldn't keep this message on the phone. Try again." It now means only that the mutations database couldn't open. Remove its allowance from `calmCopy.test.ts`.

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
- a screen whose session never loaded keeps Send disabled while offline, and the typed draft stays.

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
// that restarted meanwhile refuses it and its ghost says so.
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

A message sent offline in a session you then left waits in the outbox with no screen to send it: only an open session screen registers its target (`createNativeMutationHost`, `nativeMutationHost.ts:57-108`). So does anything admitted for a session no screen has open, such as phase 2's Board Stop (`stopSession`, which submits through the runtime). The web sends every target with waiting records on each ready connection (`handleReady`, `cmd/evener-hub/frontend/src/stores/threads.ts:2702-2767`). This task does the same, and also looks again whenever a record lands for a target nobody holds. It reuses phase 4's `settleTarget` (phase 4 Task 2), which releases a registered target with a read that leaves the connection's subscription alone. That also covers a session screen under the Reader or a subagent: it keeps its target but doesn't read while covered (phase 4 ruling 11), so on a ready connection the flush settles its target for it when something on it waits to be sent.

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

The first test fails without the flush's `runtime.start()`: a fresh runtime dispatches nothing until started (`#getClient`, `nativeMutationRuntime.ts:140-149`). The second fails without the `hasTarget` check, the third without settling a covered screen's target, the fourth when that settling ignores whether anything waits, and the fifth without the storage watch.

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
			if (target === null || target.hubId !== hubId || this.owned.has(key)) continue;
			if (runtime.hasTarget(hubId, target.ref)) {
				// A session screen holds this target. Under the Reader or a
				// subagent it doesn't read, so after a reconnect its waiting
				// message would wait for a trip back: settle it for the screen,
				// which keeps its registration.
				if (await this.waiting(runtime, key)) await runtime.settleTarget(hubId, target.ref, client);
				continue;
			}
			this.owned.set(key, runtime.registerTarget(hubId, target.ref, client));
			const settled = await runtime.settleTarget(hubId, target.ref, client);
			if (generation !== this.generation) return;
			if (settled === "reconciled" || settled === "open") await this.releaseIfDone(runtime, key);
			else this.release(key);
		}
	}

	dispose(): void {
		this.bind(null, null);
	}

	/** A target the flush holds may be done; a record for one nobody holds is
	 * work no screen will send. */
	private changed(runtime: FlushRuntime, keys: readonly string[]): void {
		let unclaimed = false;
		for (const key of keys) {
			if (this.owned.has(key)) {
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
- Test: phase 2's `rowActions.test.ts`, `RowMenu.test.tsx` and `BoardScreen.test.tsx`

**Interfaces:**
- Consumes: `useConnection().state`.
- Produces: the menu-actions function takes `connected: boolean`.

**Requirements:**
1. While the connection isn't ready, the actions that write to the hub don't show: the leading swipe reveals nothing (a full swipe does nothing), the trailing swipe shows only More, and the long-press menu and select mode keep Mark as read and Mark as unread, which are this phone's own (`seenMarkers`), and drop Pin to category…, Stop, Shut down, Archive and Rename.
2. They come back the moment the connection is ready again, with no refresh.
3. An organization change whose answer the connection lost before it arrived settles itself when the connection returns: the Board calls its `NavigationActions`' `reconcile()` when the connection turns ready while it is focused, as `usePinNavigation` does (`usePinNavigation.ts:109-111`). The row stays dimmed until then, and nothing asks you to refresh.

- [ ] **Step 1: Write the failing tests.** The menu function returns only the read marks offline and every action online, per state. Rendered offline, a row's trailing swipe shows only More, and its leading swipe has no Archive. The Board's `NavigationActions` reconciles on the ready transition while focused, and not while blurred.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/board`
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and watch them pass**, then `npm run check`.
- [ ] **Step 5: Commit** (`feat(native): Board actions that need the hub wait for the connection`).

Open PR F: "feat(native): Send while offline (phase 6, PR F)".

---

## PR G: the demo and the screenshots

### Task 17: The demo hub stages alerts, a lost send and a version mismatch

The demo fleet (phase 2's PR B, #2471: `src/dev/demoFleet.ts` and `scripts/demo-hub.mts`) serves a still fleet at one revision, so nothing on it ever alerts, and nothing can be lost or refused. The phase's screenshots need all three.

**Files:**
- Modify: `mobile-native/src/dev/demoFleet.ts` (a revision that moves, and named steps) and `mobile-native/scripts/demo-hub.mts` (stdin commands, the broadcast, and two env modes)
- Test: `mobile-native/src/dev/demoFleet.test.ts` and `mobile-native/src/demo-hub.test.ts`

**Interfaces:**
- Produces: the `DemoFleet` interface gains `step(name: DemoStep): NavigationInvalidatedPayload["targets"]`, where `type DemoStep = "question" | "failure" | "approval" | "finish" | "host-offline" | "host-online"`. Its rows, sources and revision become state the steps change; today `createDemoFleet` computes them once.

**Requirements:**
1. **Steps** follow the prototype's scripted events (`docs/design/mobile/redesign/prototype/sim.js`, `events`):
   - `question`: "Design Gateway Token Command MVP" (`s-gateway`) goes `awaiting` with `ask_pending`;
   - `failure`: `s-readintent` goes `errored`;
   - `approval`: `s-landing` joins the `needs_you` section while staying `active`, the way the hub promotes an escalation;
   - `finish`: `s-resume` goes `awaiting` without a question;
   - `host-offline` and `host-online`: paradise-park's source and every one of its rows go offline and back.
   Each step bumps the fleet's revision, so every read after it carries the new revision and a new etag, and returns the targets it changed: `{ kind: "section", section: "live" }`, `{ kind: "section", section: "needs_you" }` and `{ kind: "manifest" }`, each with the new revision.
2. **The broadcast.** With `EVENER_DEMO_FLEET=1`, the demo hub reads commands from stdin, one per line: a step's name, or `burst` (question, failure and approval within one second, for the coalesced banner). After each step it sends every connected socket `evener/navigation/invalidated` with the fleet's generation id, a sequence that rises by one per broadcast, and the step's targets. An unknown command prints the list of commands.
3. **`EVENER_DEMO_UNCONFIRMED=1`:** `turn/start` and `turn/queue` answer with the error a daemon gives when it can't record the outcome: code -32603 with `data: { clientMutationId, mutationOutcome: "unknown", retryDisposition: "blocked", cause: "persistenceUnavailable" }` (`MutationUnknown`, `appwire/errors.go`). A send then shows "Couldn't confirm this was sent".
4. **`EVENER_DEMO_PROTOCOL=<version>`:** the initialize response's `protocolVersion` (`scripts/demo-hub.mts:84`) becomes that value, so the phone's handshake fails as a protocol mismatch and shows "Update needed".
5. Without these variables the demo hub behaves exactly as before.

- [ ] **Step 1: Write the failing tests.**
  - `demoFleet.test.ts`: `step("question")` moves `s-gateway` into the `needs_you` section as `awaiting` with `ask_pending`, raises the revision the next read reports, and returns the three targets with that revision; `step("approval")` puts `s-landing` in `needs_you` while its Live row stays `active`; `host-offline` marks paradise-park's source and rows offline and `host-online` restores them. Every read still decodes with `decodeNavigationResponse`.
  - `demo-hub.test.ts`: with the fleet on, writing `question\n` to the hub's command input (inject the stream) makes a connected test socket receive `evener/navigation/invalidated` with sequence 1, and `burst\n` sends three; with `EVENER_DEMO_UNCONFIRMED=1`, a `turn/start` gets the -32603 error with `mutationOutcome: "unknown"`; with `EVENER_DEMO_PROTOCOL=evener-appwire-v0`, `initialize` answers with that version.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/dev/demoFleet.test.ts src/demo-hub.test.ts`
- [ ] **Step 3: Implement** the steps, the command input, the broadcast and the two modes. Keep `createDemoHub`'s signature compatible with its existing callers: take the command stream as an optional parameter that `import.meta` startup wires to `process.stdin`.
- [ ] **Step 4: Run them and watch them pass**, then `npm run check:scripts` and `npm run check`.
- [ ] **Step 5: Commit** (`feat(native): the demo hub stages alerts, a lost send and a version mismatch`).

### Task 18: Screenshots for the phase's last PR

With the demo hub (`EVENER_DEMO_FLEET=1 npx tsx scripts/demo-hub.mts`, plus the variables below), capture Release-simulator screenshots (iPhone 17 Pro) in light, and frames 1 and 2 in dark too. Save them as `docs/design/mobile/assets/2026-09-2x-redesign-phase6-*.png`, named by frame, and attach them to PR G with the commands that staged each:
1. Board, a banner arriving (Appendix A frame 6): type `question`.
2. Board, the coalesced banner (frame 6): type `burst`; "3 sessions need you".
3. A session, a banner below its nav bar while another session asks: open a working session, then type `question`.
4. The Reader with held alerts: open a plan, type `burst`, and capture Back with its amber 3; then go Back and capture the combined banner.
5. The Board toolbar reading "Reconnecting…": stop the demo hub and capture within 30 seconds.
6. The Board toolbar reading "Offline · updated 1m ago", and a session's connection bar: keep the hub stopped past 30 seconds.
7. A message sent while offline: with the hub stopped, send from a session; the ghost reads "Will send when you're back online". Start the hub again and capture the message in the transcript.
8. "Couldn't confirm this was sent" with Check and Discard: `EVENER_DEMO_UNCONFIRMED=1`, send, then capture the ghost.
9. "Update needed": `EVENER_DEMO_PROTOCOL=evener-appwire-v0`; the Board's toolbar and its notice with spec 14's sentence.
10. A notice banner: open a session and type `host-offline` ("paradise-park is offline · N sessions").
11. The In-app alerts page.

Open PR G: "feat(native): the demo stages alerts and the offline states, with the phase's screenshots (phase 6, PR G)".
