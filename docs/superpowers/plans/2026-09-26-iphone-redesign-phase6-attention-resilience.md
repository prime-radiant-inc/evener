# iPhone redesign, Phase 6: Attention and resilience (Implementation Plan)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** In-app alerts tell you when another session needs you without breaking into what you're reading or typing, and the phone is honest about its connection: it says when it's reconnecting or offline, keeps what you send until it can deliver it, and shows each message's delivery on the message itself, with no recovery screen.

**Architecture:**
- **Alerts** (part 2). A pure `AlertCenter` (`src/alerts/alertCenter.ts`) decides which banner shows, which alerts wait while you read or type, and which sessions alerted you most recently, with the prototype's tested timing. Pure detectors (`src/alerts/alertEvents.ts`) turn successive navigation reads into alerts. An `AlertsProvider` feeds the center from the app-wide attention data, and `AlertBannerHost` draws the banner just below the nav bar and opens what you tap.
- **Connection.** A `ConnectionClock` in `ConnectionProvider` measures how long the app has been in front without a live connection, so the Board's toolbar, the Session's connection bar and the sheets read one status (`useConnectionStatusText`), and no text asks you to reconnect or refresh.
- **Outbox.** The durable mutation runtime stays (roadmap: keep the data layer). Phase 3 put the outbox inline as ghost bubbles and removed the recovery screen; this phase adds what it left: Discard for a lost send, Send now for a message a Stop held, the offline caption, Send while offline, an app-wide flush that sends what no open session will (part 3), and a hold for Board actions taken offline (part 2).

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
  - A control appears only when it can act: Check only while connected. The Board's hub-writing actions are held while offline (ruling 18, part 2's PR H); until then phase 2 part 3 hides them (its ruling 21).
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

Decisions this plan makes where the spec is silent or its data doesn't exist yet. Questions 1 and 2 below ask Jesse about the two that change what a person sees; ruling 18 records his answer to a third.

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
12. **Offline Send is fenced to the session instance the phone last saw,** as an online send is: the conversation's `instanceId`, which the service sets on every read (the read's instance, or the thread id when the read names none, `mobile/src/services/conversation.ts:729-736`). Phase 6 adds no fallback of its own. A session this phone hasn't read since launch has no instance to fence with, so there Send stays disabled offline and the draft stays. That is about the phone's read, not the session's status: a shut-down session (`notLoaded` on the hub) the phone has read resumes on a send, fenced as an online resume is.
13. **Offline, Send queues whenever the harness can.** By the time the message arrives another turn may have started, which refuses a `turn/start`; the daemon runs a queued message at once on an idle session and holds it behind a running one (`clientMutationQueue`, `agent/session_client_mutation_queue.go`). So offline routing is the package's table as if a send of this phone's were already pending (`deriveSendQueueAvailability`'s tier 6), which also queues a second message behind a first this phone still holds. A harness that can't queue waits for the connection. A shut-down session resumes on its first message.
14. **What a waiting message says.** "Sending…" while the connection is live, "Will send when you're back online" while it isn't (the prototype's words, and principle 5: nothing is sending), and Send's label offline says the same.
15. **"Couldn't confirm this was sent" offers Check only while connected, and Discard always,** since Check needs the hub and Discard doesn't. This covers a lost send, the unconfirmed draft and a record the phone couldn't place.
16. **A message a Stop held before it left the phone comes back as held,** with "Send now" and "Cancel", like the queue a Stop parks (spec 8.5). Send now moves it to the end of its session's line, behind the Stop that held it, so that Stop can never stop it. The roadmap's phase 6 row, as phase 3 words it, calls these Retry and Discard; the ghost uses spec 8.5's words for a held message.
17. **The flush.** On each ready connection, and whenever a record lands for a session nobody has open, the app settles and sends every target of the active hub that no session screen holds, as the web's `handleReady` does, then lets each go once nothing on it can be sent. On a ready connection it also settles a target a session screen holds when something on it waits, because a screen under the Reader or a subagent doesn't read while covered; the screen keeps its target.
18. **Board actions taken offline are held, and sent when the connection returns.** Jesse, 2026-09-26: "board actions while offline: hold em" (this plan's former Question 3, and phase 2 part 3's Question 2). Spec 7.5 says they "go to the outbox". Archive and unarchive, pin, rename, shut down, the project actions and Stop wait in a durable hold on the phone and go out in order on the next ready connection. A held Stop or Shut down names the turn the person saw and is dropped, never applied, if a newer turn is running, so it can't stop work nobody saw. Part 2's PR H designs and builds the hold on phase 2 part 3's journal, `BoardStops` and row actions; until it lands, part 3's ruling 21 (those actions hidden while offline) is the interim. Mark as read and unread are this phone's own and never wait.

## Questions for Jesse

1. **Should a banner tell you about changes that happened while the phone was disconnected?** My recommendation, and what ruling 2 builds: yes after a drop the connection recovers from while you're using the app (a flap on the train), and no after you return from the background, where the Board, Back's count and Next already show what needs you.
2. **Banners while a sheet is up:** hold them until the sheet closes (my recommendation, and what ruling 7 builds), or show them over the sheet as the prototype does? Over a sheet, the banner would cover the sheet's own Cancel and Done, and a React Native modal presents above the app's overlay, so showing it there needs a full-window overlay.

## Built on earlier phases

Phases 2 to 5 are planned beside this one, so some names below are what those plans say they produce. Tasks 3-11 and 17-20 are in part 2, and Task 15 in part 3. Before a task that uses one, read the landed code. If it landed under another name or shape, use the landed one and say so in the PR.

| This plan uses | From | Used by |
|---|---|---|
| `boardState`, `liveBands`, `whyLine`, `WhyLine`, `LiveBands`, `BoardState` (`src/board/attention.ts`), `StateMark`, `seenMarkers` | phase 2 PR 1 | Tasks 3, 4, 7, 8 |
| `createBoardController` (`src/board/boardData.ts`), `connectionStatus` (`src/board/connectionStatus.ts`), `BoardScreen`, `BoardToolbar` | phase 2 PR 2 | Tasks 1, 7, 8 |
| `Notice` and `notices` (`src/board/notices.ts`), `Notices.tsx` | phase 2 part 2 (its Task 14) | Tasks 4, 8 |
| The Board's offline rule (`rowMenuActions`, its ruling 21), `useBoardOrganization` (its ruling 16) and `BoardStops` (its ruling 17, Task 12.2) | phase 2 part 3 | ruling 18; part 2's PR H; part 3's Task 15 |
| The demo fleet (`src/dev/demoFleet.ts`, `scripts/demo-hub.mts`) | phase 2 PR B | Task 17 |
| `useConnectionStatusText()` and the Session's connection bar (its Task 15); `compactDuration` (`src/session/format.ts`) and `sendAction` (its Task 3); `Composer` (its Task 5); `ghosts.ts`, whose parked queue reads the package's `isQueueParked`, and its wiring (its Tasks 7-8); `fleetOrder.ts` (`othersNeedingYou`, `nextSession`), `BackButton` and the Next capsule (its Tasks 32-33); `Toast` | phase 3 | Tasks 1, 2, 10, 11, 13, 14 |
| Phase 3 left here: Send while offline (ruling 4), every haptic (ruling 5), Next's recent order (ruling 11), the Board's outbox and a Stop-held message's retry (ruling 3) | phase 3 | Tasks 6, 11, 13, 14; part 2's PR H |
| `NativeMutationRuntime.settleTarget` (its Task 2); `ReaderScreen` (`src/reader/ReaderScreen.tsx`, its Task 14) on the `"Reader"` route | phase 4 | Tasks 10 (part 2) and 15 (part 3) |
| The grouped-list pieces (`src/sheet/Grouped.tsx`, its Task 1); `useConnectionStatusLine`, `SheetStatus` and `INCOMPATIBLE_VERSIONS` (its Task 2); the Hub sheet (`src/hub/HubSheet.tsx`, `HubHome.tsx`, its Task 3) and its sheet routes `"Hub"` and `"NewSession"`; the "Reconnect" guard and the connection messages (its Task 27); phase 5 left the In-app alerts page here (its ruling 11) | phase 5 | Tasks 1, 2, 8, 9 |

## Review Focus

1. **A pile of banners when you come back.** Opening the app, switching hubs or returning from the background must not alert about sessions that already needed you; the Board, Back's count and Next already show them. Pinned by Task 4 (the first read is a baseline) and Task 8 (a new client starts a new baseline, and a Needs you section still loading is never the baseline), both in part 2.
2. **Old news alerting twice.** A host that goes offline and comes back, or a session that alerts twice within a burst, must never produce a second banner or count one session twice. Pinned by Task 4 (offline rows keep their state) and Task 3 (a coalesced banner counts each session once), both in part 2.
3. **Returning to the app flashes "Offline".** An hour in the background, then a foreground return whose connection takes a second, must show nothing, and "Reconnecting…" only after 2 seconds. Pinned by Task 1 (the clock never counts background time).
4. **A message sent offline is lost, sent twice, left waiting, or stopped by its own Stop.** Send while offline, then leave the session or open the Reader from it, relaunch, and come back online: the message goes out exactly once, with no trip back to its session. A message a Stop held, sent again, goes after that Stop. Pinned by Task 14 (the offline admission stores one record, sent once when the connection returns), Task 12 (Send now moves the row behind its Stop), and part 3's Task 15 (a fresh process sends it once, and the flush settles for a session screen under the Reader).
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
| F: Send while offline | 14 | Opus | PRs A and E land | connection |
| G: the demo and the screenshots (part 2) | 17-18 | Opus (17), then by hand | PRs D and F land | alerts |
| H: Board actions held offline (part 2) | 19-20 | Opus | phase 2 part 3's PRs are on main | board |
| I: what you left behind sends itself (part 3) | 15 | Sonnet | PR F lands | connection |

- Three lanes start together: alerts (PR B), connection (PR A) and outbox (PR E). The outbox lane joins the connection lane at PR F, and that lane joins the alerts lane at PR G. PR H, the Board's hold, runs on its own once phase 2 part 3 is on main.
- Files two lanes share: `App.tsx` (PR C mounts the alerts, PR I binds the flush) and phase 3's session screen (PRs D, E, F and I). The second PR to land in either merges `origin/main` before its review.
- Every PR lands under the roadmap's rules: CI green, RoboRev with nothing Medium or higher, /simplify, admin squash merge, Lows in a fast-follow, and decompose after five rounds.
- PR G, the phase's last, carries Release-simulator screenshots of the alert and offline frames against the demo fleet (part 2's Task 18).

---

## PR A: one connection clock, and no reconnect words

### Task 1: One connection clock and one status for every screen

Phase 2 put the status on the Board's toolbar (`connectionStatus` in `src/board/connectionStatus.ts`, phase 2 Task 7). Phase 3 and phase 5 each plan to move its timing into a hook in the same file: `useConnectionStatusText()` for the Session's connection bar (phase 3 Task 15), and `useConnectionStatusLine()` for the sheets' status line (phase 5 Task 2, its ruling 21). If both landed, they are one hook under two names: keep `useConnectionStatusText`, delete `useConnectionStatusLine` and its test file, and point its callers (phase 5's `SheetStatus`, `HubHome` and `BoardToolbar`, which its Task 2 rewired; `grep -rn useConnectionStatusLine mobile-native/src`) at the survivor. What none of them owns is the clock, and it has two traps:
- The time down is tracked where the hook is mounted, so a screen that appears while the phone is offline starts from zero, and a return from the background can count the background as time down and flash "Offline".
- `relativeAge` says "now" under a minute, so the status can read "updated now ago".

This task moves the clock into `ConnectionProvider`, where it sees the foreground, and makes the hook read it. The module stays in `src/board/`: a `src/connectionStatus.ts` would differ from the existing `src/ConnectionStatus.tsx` only in case, which macOS's file system and TypeScript both refuse (TS1149).

**Files:**
- Create: `mobile-native/src/connectionClock.ts`
- Modify: `mobile-native/src/board/connectionStatus.ts` (replace its content as below; `useConnectionStatusText` keeps its name, so phase 3's bar needs no change), `mobile-native/src/board/BoardToolbar.tsx`, `mobile-native/src/sheet/SheetStatus.tsx` and `mobile-native/src/hub/HubHome.tsx` (phase 5 Tasks 2 and 3: call `useConnectionStatusText` where they called `useConnectionStatusLine`), `mobile-native/src/ConnectionProvider.tsx` (the clock, and `downSince` and `lastLiveAt` on the context) and `mobile-native/src/renderNative.testkit.tsx` (`screenConnection` adds `downSince: null` and `lastLiveAt: null`)
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

	it("dates the data from leaving the front, even if the connection closes later", () => {
		const clock = new ConnectionClock();
		clock.observe(hub(true), at(0));
		expect(clock.observe(hub(true, false), at(5))).toEqual({ downSince: null, lastLiveAt: at(5) });
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
	/** When this hub's connection last stopped being live in front (it
	 * dropped, or the app left the front); null until that has happened since
	 * the app launched or the hub was chosen. */
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
		// The data stops being live when the connection drops or the app
		// leaves the front, whichever the provider reports first: it closes
		// the connection in the background.
		const wasLive = sameHub && last.live && last.foreground;
		if (!sameHub) lastLiveAt = null;
		else if (wasLive && !(next.live && next.foreground)) lastLiveAt = now;
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
// it. Text handed to console.* is for developers, and is skipped. It covers
// the phone's own code and the shared mobile code it runs (mobile/src), whose
// errors the phone shows.
import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";
import { expect, it } from "vitest";

const REPO = fileURLToPath(new URL("../..", import.meta.url));
const ROOTS = ["mobile-native/src", "mobile/src"].map((root) => path.join(REPO, root));
const FORBIDDEN = /\breconnect\b|\brefresh\b|\bpull down to retry\b/i;
// Task 14 deletes this refusal ("Reconnect and try again.") and this entry.
const ALLOWED = new Set(["mobile-native/src/nativeMutationHost.ts"]);

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
	expect(asks("h.ts", `export const MESSAGE = "Refresh to try again.";\nexport function hint() { return "Reconnect first"; }`)).toEqual([
		"Refresh to try again.",
		"Reconnect first",
	]);
	expect(asks("i.ts", `export { refresh } from "./refresh";`)).toEqual([]);
});

it("no text a person can read asks them to reconnect or refresh", () => {
	const offenders = ROOTS.flatMap(productionFiles)
		.filter((file) => !ALLOWED.has(path.relative(REPO, file)))
		.flatMap((file) =>
			asks(file, readFileSync(file, "utf8")).map((text) => `${path.relative(REPO, file)}: ${text.trim()}`),
		);
	expect(offenders).toEqual([]);
});
```

- [ ] **Step 2: Run it and read the list**

Run: `cd mobile-native && npx vitest run src/calmCopy.test.ts`
Expected: FAIL. The self-check passes, and the audit lists each offending string by file: this task's worklist, whose length goes in the PR description. On main at `c074010bb` it held 103 strings in 45 files, two of them in `mobile/src`. Phases 2 to 5 replace the screens behind most of them (phase 3 rewrites `approvalControls.ts`'s). The shared navigation messages are the likeliest to remain: `navigationActions.ts` (6), `navigationPages.ts` (5), `navigationReadback.ts` (4), `pinNavigation.ts` (4), `organizationNavigation.ts` (2), and one each in `navigationReveal.ts` and `sessionDeletionNavigation.ts`.

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

## PRs B, C, D, G, H and I: in parts 2 and 3

The alerts, the phase's screenshots and the Board's hold, PRs B, C, D, G and H (Tasks 3-11 and 17-20), are in part 2, `docs/superpowers/plans/2026-09-26-iphone-redesign-phase6-attention-resilience-part2.md`. After five review rounds on this PR the alert center (Task 3) still had findings open, so it and everything built on it moved out; the Board's hold joined it when Jesse answered the offline question (ruling 18). The outbox's flush, PR I (Task 15), is in part 3, `docs/superpowers/plans/2026-09-26-iphone-redesign-phase6-attention-resilience-part3.md`, after its own review rounds here. Neither file is in this PR's branch; each lands in its own PR and opens with the findings it carries. This part's Goal, Architecture, Global Constraints, Rulings, Questions and Review Focus bind both, and the task numbers continue there.

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
		// The press-time half of the stop barrier, captured as submit's is:
		// readStopEpoch's body is synchronous (mutationOutboxStorage.ts:290-292),
		// so the epoch is read inside this call, before any other event runs. A
		// Stop that lands during start() then moves the epoch past it, and
		// storage refuses the release.
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

Phase 3 disables Send while offline (its ruling 4): the durable submitter refuses without a live host (`createDurableSubmitter`, `nativeMutationHost.ts:44-55`), and the store only admits a message through a bound service (`captureOperationBinding`, `mobile/src/state/conversation.ts:1177-1195`). Spec 8.5 says Send "holds the message in the outbox and sends it when the connection returns". The runtime already keeps a message admitted while disconnected and sends it once its target is registered and read (the test "submit durably records while disconnected and dispatches after readiness", `nativeMutationRuntime.test.ts:800-828`). This PR admits it. A message left in a session you have since closed goes when you open it again, or with part 3's flush (PR I) as soon as the connection returns.

### Task 14: Send while offline

**Files:**
- Modify: `mobile-native/src/session/sendAction.ts` and `sendAction.test.ts` (phase 3 Task 3)
- Create: `mobile-native/src/outbox/offlineSend.ts` and `mobile-native/src/outbox/offlineSend.test.ts`
- Modify: the session screen's Send and `sendAnswers` wiring (phase 3 Tasks 5 and 10, in `mobile-native/src/screens.tsx`)
- Modify: `mobile-native/src/nativeMutationHost.ts` (`NATIVE_MUTATION_HOST_UNAVAILABLE` loses "Reconnect") and `mobile-native/src/calmCopy.test.ts` (the allowance for it goes)
- Test: `mobile-native/src/ConversationScreen.offline.test.tsx` (create, on the harness of `ConversationScreen.send.test.tsx`)

**Interfaces:**
- Consumes: phase 3's `sendAction`, `SendSource`, `SendAction`, `composerPlaceholder` and `sendLabel`; `deriveSendQueueAvailability` and `buildComposerInput` from `@evener/appwire-client`, and `ownPendingSend` from `@evener/appwire-client/state/mutation` (phase 3 Task 2); `getNativeMutationRuntime().submit` and `nativeMutationTargetKey`.
- Produces:
  - `sendAction(conversation, pendingMutations, connected)` keeps its signature. Offline it returns the action the message will take when it arrives, instead of `"none"`.
  - `sendLabel(action, questionPending, connected = true)`.
  - `interface OfflineTarget { hubId: string; ref: string; threadId: string; instanceId: string }` and `offlineRequest(target: OfflineTarget, action: "send" | "queue" | "resume", input: InputItem[]): ConversationMutationRequest`, from `src/outbox/offlineSend.ts`.

**Requirements (spec 8.5 and 14; rulings 12 to 14):**
1. **Routing offline.** A message waits in the outbox, and by the time it arrives another turn may have started, where `turn/start` is refused. So offline, Send routes by the package's table as if a send of this phone's were already pending (`deriveSendQueueAvailability` with `hasPendingSend: true`, its tier 6): it queues whenever the harness can, and the daemon runs a queued message at once on an idle session (`clientMutationQueue` wakes it, `agent/session_client_mutation_queue.go:107-190`) and holds it behind a running one. A message after one this phone still holds queues behind it, as online. The first message to a shut-down session resumes it. A paused session, one that needs a restart, and one whose harness can't queue stay `"none"`, and the draft stays.
2. **What Send admits offline.** When the screen isn't connected and the phone has read the session since launch (`store.getState().conversation` is set, with the `instanceId` every read sets), Send is enabled on the same draft conditions as online, minus `ready`. It runs `document.submit(async (text, images) => …)`, which submits `offlineRequest({ hubId, ref, threadId, instanceId }, action, buildComposerInput(text, images))` to `getNativeMutationRuntime()`, and returns `true`. Its haptic comes with part 2's Task 6. The ghost shows at once through the durable pending rows the screen already follows (phase 3 Task 8).
3. **Never read.** A session this phone hasn't read since launch (`store.getState().conversation` is unset) has no instance to fence with (ruling 12), so its Send stays disabled offline, and the draft stays. `sendAction` is never asked there. A thread whose hub status is `notLoaded` is a shut-down session the phone did read, and routes to `"resume"`.
4. **Words.** The placeholder is `composerPlaceholder` of the connected routing, so it describes the session rather than the outbox. Send's accessibility label offline is "Send when you're back online", or "Send answer when you're back online" while a question is pending.
5. **Answers.** The dock's "Send answer" admits offline through the same `offlineRequest` path, with the composed text as one text input, and marks the batch sent as `sendAnswers` does online.
6. **The refusal left online.** `NATIVE_MUTATION_HOST_UNAVAILABLE` becomes "Couldn't keep this message on the phone. Try again." With offline sends going straight to the runtime, it shows only when the screen has no live host while connected: the host couldn't be created (the mutations database or the client binding, `screens.tsx:1038-1052`), or the connection dropped in the same instant. Trying again covers both. Keep the host guard in `createDurableSubmitter`, and remove the constant's allowance from `calmCopy.test.ts`.

- [ ] **Step 1: Write the failing tests**

In phase 3's `sendAction.test.ts`, delete the first line of "does nothing offline, while paused, or where the harness can't take it" (`sendAction(session("idle"), [], false)`), rename the test "does nothing while paused, or where the harness can't take it", and add (`pendingSend`, `caps` and `session` are that file's helpers):

```ts
describe("Send while offline (phase 6)", () => {
	it.each([
		["idle", "queue"],
		["awaiting", "queue"],
		["systemError", "queue"],
		["active", "queue"],
		["notLoaded", "resume"],
		["ended", "resume"],
		["closed", "resume"],
		["restartRequired", "none"],
	] as const)("%s → %s: it queues where a turn may be running by the time it arrives", (type, expected) => {
		expect(sendAction(session(type), [], false)).toBe(expected);
	});

	it("queues a message behind one this phone still holds, a shut-down session's first included", () => {
		expect(sendAction(session("ended"), [pendingSend()], false)).toBe("queue");
		expect(sendAction(session("notLoaded"), [pendingSend({ state: "blockedUnknown" })], false)).toBe("queue");
		expect(sendAction(session("idle"), [pendingSend()], false)).toBe("queue");
	});

	it("waits for the connection where the harness can't queue, and while paused", () => {
		expect(sendAction(session("idle", { capabilities: caps({ queue: false }) }), [], false)).toBe("none");
		expect(sendAction(session("active", { capabilities: caps({ queue: false }) }), [], false)).toBe("none");
		expect(sendAction(session("idle", { resumeRequired: true }), [], false)).toBe("none");
		expect(sendAction(session("ended", { capabilities: caps({ send: false }) }), [], false)).toBe("none");
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

it("starts or resumes a session with a send, fenced the same way", () => {
	expect(offlineRequest(target, "send", input)).toMatchObject({ kind: "send", instanceId: "instance-7" });
	expect(offlineRequest(target, "resume", input)).toMatchObject({ kind: "send", instanceId: "instance-7" });
});
```

`ConversationScreen.offline.test.tsx` mounts the real screen on the harness of phase 3's `ConversationScreen.send.test.tsx`, with the real durable runtime over `openSqliteSyncDouble()` (mock `expo-sqlite`'s `openDatabaseSync` to return that port). It covers:
- the session loads connected; the connection drops (the mocked connection reports `"reconnecting"`); typing "sent on the train" and pressing Send ("Send when you're back online") leaves the composer empty, stores one `turn/queue` record for `nativeMutationTargetKey("hub-1", ref)`, and shows a ghost reading "Will send when you're back online";
- the connection returns (`"ready"`, the same client): the client receives that `turn/queue` exactly once, and the ghost goes when the read reflects it;
- a screen that never read its session keeps Send disabled while offline, and the typed draft stays;
- with a question pending and the connection down, the dock's Send reads "Send answer when you're back online", pressing it stores one record carrying the composed answer and marks the question's batch sent, and back online the client receives it exactly once.

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
	if (!connected) return offlineAction(conversation, ownPendingSend(pendingMutations));
	const status = conversation.status.type;
	// … the rest of phase 3's body, unchanged …
}

/** Offline, a message waits in the phone's outbox (spec 8.5, ruling 13).
 * By the time it arrives a turn may be running, so it goes as if a send of
 * this phone's were already pending, the package's tier 6: it queues where
 * the harness can, and waits for the connection where it can't. The one
 * exception is the first message to a shut-down session, which resumes it;
 * a message after it queues behind it. */
function offlineAction(conversation: SendSource, pendingSend: boolean): SendAction {
	const status = conversation.status.type;
	if (ENDED.has(status) && !pendingSend) return conversation.capabilities.send ? "resume" : "none";
	const availability = deriveSendQueueAvailability({
		statusType: status,
		capabilities: conversation.capabilities,
		hasPendingSend: true,
	});
	return availability.canQueue ? "queue" : "none";
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
// 12). It is fenced to the session instance the phone last saw: the fence the
// conversation service computed on its last read of the session
// (mobile/src/services/conversation.ts:729-736), the same value an online
// send carries, so a session that restarted meanwhile refuses it and its
// ghost says so. This module adds no fallback of its own.
import type { InputItem } from "@evener/appwire-client";
import type { ConversationMutationRequest } from "../../../mobile/src/state/conversationMutation";

export interface OfflineTarget {
	hubId: string;
	ref: string;
	threadId: string;
	/** The conversation's instanceId, which every read sets. */
	instanceId: string;
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
		instanceId: target.instanceId,
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

### The Board's side: part 2 holds it

The Board's half of spec 14's outbox has no task in part 1, so Task 16 is gone and the numbers skip it. Phase 2 part 3 hides the Board's hub-writing actions while offline (its ruling 21, through `rowMenuActions`' `connected` context, its Task 12.3), reconciles its one organization journal on focus and whenever the connection turns ready (its ruling 16, `useBoardOrganization`, its Task 10.5), and sends a Board Stop through the durable runtime, holding its target until the interrupt leaves (its ruling 17, `BoardStops`, its Task 12.2). Jesse's answer (ruling 18) turns the hiding into a hold, which part 2's PR H builds on those same pieces. Phase 6 writes no second copy of any of them.

Open PR F: "feat(native): Send while offline (phase 6, PR F)".
