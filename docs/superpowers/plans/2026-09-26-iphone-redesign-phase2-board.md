# iPhone redesign, Phase 2: the Board — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The Board replaces the project-first Sessions screen as the iPhone app's home: every live session ordered by who needs you, then the user's pinned categories, projects, test runs and archive, built on the fallbacks so it works before any server addition lands, and a connection that recovers on its own.

**Architecture:**
- **Pure core.** A pure attention model (`src/board/attention.ts`) turns `NavigationSessionSummary` rows into Board states, bands, counts and copy. A small device memory (`src/board/boardMemory.ts`) records what you've seen and which sections you folded.
- **Data.** A `BoardController` (`src/board/boardData.ts`) reads the navigation resources the hub already serves. It keeps its rows on screen through reconnects.
- **Screen.** `BoardScreen` renders all of this under the existing route name `"Sessions"`, so restore, pop targets and `location.test.ts` stay untouched.
- **Row actions.** Swipes, the long-press menu and select mode arrive last, on `react-native-gesture-handler` and `react-native-reanimated`.

**Tech Stack:** Expo SDK 57, React Native 0.86.3, React 19, TypeScript, vitest 5 with react-test-renderer (`src/renderNative.testkit.tsx`), `expo-symbols` 57.0.3 (`SymbolView`), `expo-sqlite/kv-store` for device memory, `@evener/appwire-client/state/navigation` for decoding. PR 4 adds `react-native-gesture-handler`, `react-native-reanimated` and `react-native-worklets`. CocoaPods runs through Bundler 2.7.2 on Ruby 3.3.6.

**Spec:** `docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md`: principles (4), the Board (7), attention (13.1-13.2), states and resilience (14), the pulse meter (16.4), iconography (16.5), data sources (17) and server additions (18). The roadmap is `docs/superpowers/plans/2026-09-25-iphone-redesign-roadmap.md`.

## Global Constraints

- **Copy is the spec's, verbatim.**
  - State words: "Failed", "Question", "Approval", "Warning", "Restart needed".
  - Band headers: "NEEDS YOU · 4", "FINISHED · 4", "WORKING · 9", "Idle · 3".
  - The summary line: "4 need you · 4 finished · 9 working · 3 idle" ("1 needs you" when singular).
  - The empty Board: "Nothing's running. Start a session to put an agent to work."
  - Toolbar status: "Reconnecting…" and "Offline · updated 3m ago".
- **Calm** (spec principle 2):
  - A control appears only when it can act.
  - No screen carries a Reconnect button or asks you to refresh; no pull-to-refresh.
  - Nothing moves unless its data moved.
- **Color:** only from `useColors().palette`. Four hues, one job each:
  - amber (`attention`/`attentionInk`) means a human is needed;
  - green (`alive`) means working;
  - red (`danger`/`dangerInk`) means failed;
  - blue (`accent`/`accentInk`) means tappable, selected or unread.
  - Only the state word takes a hue; the reason is ink.
- **Marks are SF Symbols through `SymbolView` from `expo-symbols`:**
  - `xmark.octagon.fill` (Failed), `questionmark.circle.fill` (Question), `hand.raised.circle.fill` (Approval), `exclamationmark.triangle.fill` (Warning), `arrow.triangle.2.circlepath.circle.fill` (Restart needed), `circle.fill` (unread and still-working dots);
  - `pin.fill`, `magnifyingglass`, `square.and.pencil`, `ellipsis.circle`, `archivebox`, `stop.fill`, `folder`, `server.rack`.
- **Type:** SF Pro for everything on the Board.
  - Title: semibold 17/22 (two lines for Needs you).
  - Why line: 15/20. Last line: 13/18.
  - Age: 13pt tabular (`fontVariant: ["tabular-nums"]`) in `inkLow`.
  - The Finished excerpt (Source Serif 15/21) waits for S1.
- **Layout:**
  - 28pt mark column; 16pt horizontal padding.
  - Signal rows (Needs you, unseen Finished, Working) are 64-88pt; quiet rows are 48pt.
  - Hairline separators inset to the title; 44pt minimum touch targets.
- **Routes and storage:**
  - Route names and params are unchanged: home stays `"Sessions"` with no params, and a session opens with `navigation.navigate("Conversation", { hubId, ref, title })`.
  - Existing storage keys are unchanged.
  - New kv-store keys: `evener.native.seen.${hubId}`, `evener.native.board-sections.${hubId}` and `evener.native.recent-searches.${hubId}`. All are cleared by `ConnectionProvider.removeHub`.
- **Fallbacks, not fakes.** Every row fact comes from `NavigationSessionSummary`, the manifest, the pin catalog, the catalogs, `evener/search`, `evener/auth/list` or `evener/plugin/list`. Where the spec wants data the hub doesn't send yet, the Board uses section 18's fallback, and the server lane swaps it later (S1, S2, S3, S4, S5, S11, S13, S14).
- **Tests** meet the hub at the request boundary: the `boundary()` fake `ConversationClientLike` plus `wireV2` from `@evener/appwire-client/testing/navigation`, as in `src/projectBrowser.test.ts`. Never mock the module under test.
- **Repo rules:**
  - Never run Biome in `mobile-native/`.
  - Never run `npm ci` through a symlinked `node_modules`.
  - Never `git add -A`.
  - iPhone only.
  - Run the native gate with `make test-native`.

## Rulings

Decisions this plan makes where the spec is silent or its data doesn't exist yet. The spec and roadmap edits that go with them are in this plan's PR.

1. **The Needs you count is computed on the phone from rows.** The hub's `AttentionSummary.needsYou` counts every `awaiting` session, including the spec's Finished, and it counts failures separately (`hubcore/attention.go`). Spec section 17 is corrected to match.
2. **Approvals are inferred until S2.** A row in the hub's `needs_you` section whose state is none of `awaiting`, `warning`, `restartRequired` or `errored` is there for a pending approval, because the hub promotes an escalation into the section but leaves the row `active` (`promotedAttentionLevel`, `hubcore/attention.go`).
3. **Quiet, May be stuck, and the meter's minutes wait for S5.**
   - `updated_at` moves only when the session's metadata is written (turn boundaries, renames), so it can't tell a quiet session from a busy one.
   - Until S5, working rows say "Waiting on N subagents", "Running <command>" or "Working", and the meter shows the spec's one-bar fallback without motion.
4. **Finished versus Idle is a phone-local seen marker (S4's fallback).**
   - The marker stores the row's `updated_at` from when you opened it, so only hub timestamps are compared.
   - A rename moves `updated_at` and brings a seen session back to Finished until S4. That's a known, harmless fallback artifact.
5. **Continue reading moves to phase 4, with the Reader.** It needs document reading positions that only the Reader records. The roadmap's phase 2 and 4 rows change in this PR.
6. **The model on rows waits** for a model on summaries and the "Show model on Board rows" setting (phase 5). Summaries carry no model today.
7. **Search ships with Sessions and Projects groups and the All and Live scopes.**
   - `evener/search` matches live session titles and ids and searches the past index. It returns no message-text hits and no archived flag.
   - "In sessions" and the Archived scope need S14 (message-text search with snippets and an archived flag). This PR adds S14 to spec section 18 and to the server lane.
8. **Notices.**
   - A provider notice appears when `evener/auth/list` reports `needsLogin`. `needsRefresh` (within five minutes of expiry, OAuth only) is left to the hub, and "expiring within a day" isn't knowable.
   - A plugin notice appears when `evener/plugin/list` reports `broken`. It's polled when the Board comes into view and every 5 minutes while it's shown, because `evener/plugin/updated` fires only on mutations.
   - A host notice comes from the manifest's `sources[].online`, with its action "Details" rather than "Reconnect": the hub already retries a dropped host with backoff (`cmd/evener-hub/internal/sshconn`), so Details opens the host with its last error. #2460 amends spec 7.1 to say the same.
9. **The hub button opens a menu until phase 5's Hub sheet exists.** It lists the destinations the old home header offered: "Hub settings" (`HubSettings`) and "Switch hub" (`Hubs`). "Browse projects" and "Pinned sections" become Board sections.
10. **The Working band keeps the hub's Live order.** The hub sorts Live by attention rank, then `updated_at` newest first, which for working rows is newest turn first: stable, and the closest thing to start time the phone has until S5.

## Review Focus

1. **Reconnect or background without an empty flash.**
   - Today, backgrounding closes the client. The old home then unmounted its list and reloaded from scratch with one project expanded.
   - The Board must keep its last rows on screen through a background and foreground cycle, a dropped connection and a hub restart, with the meters grayed, and replace them only when fresh reads land.
   - Pinned by Task 5 (the controller keeps rows across `setClient(null)` and a new client) and Task 7 (the screen never renders an empty Live while rows are retained).
2. **More than 50 live sessions.** A Live page holds 50 rows. A session that needs you but sits past the loaded pages must still appear in Needs you, and scrolling loads the next Live page. Pinned by Task 2 (bands union the `needs_you` section) and Task 5 (`loadMoreLive`).
3. **A session with a question and an approval at once shows Question, and an approval that resolves leaves Needs you on the next invalidation.** Pinned by Task 2 (precedence) and Task 5 (an invalidated `needs_you` re-read drops the approval).
4. **Seen markers survive clock skew and first run.** The first launch must not flood Finished with every past session. A phone clock minutes off changes nothing, because only hub timestamps are compared. Pinned by Task 3.
5. **Offline host and offline hub.**
   - A session on an offline host never shows as Working or Finished: its row is `ended` and leaves Live.
   - When the phone loses the hub, rows keep their last state, meters gray, the toolbar says "Reconnecting…", and nothing asks you to reconnect.
   - Pinned by Task 2 (`ended` means shut down), Task 1 (automatic retry) and Task 7 (toolbar status and gray meters).

---

## PRs and lanes

| PR | Tasks | Model | Starts when | Lane |
|---|---|---|---|---|
| A: the connection recovers on its own | 1 | Sonnet (the plan carries the code) | now | A |
| 1: attention model, device memory, marks | 2-4 | Sonnet | now | B |
| B: the demo fleet | 16 | Sonnet | when PR A lands | A |
| 2: the Board, Live first | 5-8 | Opus (medium) | when PR 1 lands | B |
| 3: pinned categories, projects and hosts, test runs, archived | 9-10 | Opus (medium) | when PR 2 lands | B |
| 5: notices and search | 14-15 | Opus (medium) | when PR 2 lands | A |
| 4: row actions, select mode, list stability | 11-13 | Opus (medium) | when PRs 3 and 5 land | B |

- The server lane (phase 7) runs beside these as the third lane.
- Every PR lands under the roadmap's rules: CI green, RoboRev with nothing Medium or higher, /simplify, admin squash merge, Lows in a fast-follow, and decompose after five rounds.
- The phase's last PR carries Release-simulator screenshots of Appendix A frames 1-7 against the demo fleet (Task 17).

---

## PR A: the connection recovers on its own

### Task 1: Automatic reconnection

Today a fresh client whose first handshake fails closes for good. The app opens a fresh client on every return to the foreground, so a flaky network or a restarting hub leaves every screen on a Reconnect button. The `AppwireClient` already retries a connection that drops after reaching ready (250ms doubling to 5s, `appwire-client/typescript/client.ts`). This task retries the first handshake too.

**Files:**
- Modify: `mobile-native/src/hubConnection.ts`
- Test: `mobile-native/src/hubConnection.test.tsx`

**Interfaces:**
- Produces:
  - `reconnectDelay(failures: number): number` (exported from `hubConnection.ts`).
  - `useHubConnection` keeps its signature and return shape.

- [ ] **Step 1: Write the failing tests**

Add to `mobile-native/src/hubConnection.test.tsx` (it already has `FakeHubClient`, `harness`, `mount` and `act`). Add `reconnectDelay` to the import from `./hubConnection`.

```tsx
it("waits at once, then 1, 2, 4, 8 and 16 seconds, then every 30 seconds", () => {
	expect([0, 1, 2, 3, 4, 5, 6, 12].map(reconnectDelay)).toEqual([
		0, 1000, 2000, 4000, 8000, 16000, 30000, 30000,
	]);
});

it("tries a closed connection again on its own, backing off between attempts", async () => {
	vi.useFakeTimers();
	try {
		const first = new FakeHubClient();
		harness.client = first;
		const { hook } = mount();
		await act(async () => {});
		const second = new FakeHubClient();
		harness.client = second;
		await act(async () => {
			first.fail();
		});
		expect(hook.result.current.state).toBe("closed");
		await act(async () => {
			vi.advanceTimersByTime(0);
		});
		await act(async () => {});
		expect(second.state).toBe("connecting");
		const third = new FakeHubClient();
		harness.client = third;
		await act(async () => {
			second.fail();
		});
		await act(async () => {
			vi.advanceTimersByTime(999);
		});
		await act(async () => {});
		expect(third.state).toBe("idle");
		await act(async () => {
			vi.advanceTimersByTime(1);
		});
		await act(async () => {});
		expect(third.state).toBe("connecting");
		await act(async () => {
			third.succeed();
		});
		expect(hook.result.current.state).toBe("ready");
	} finally {
		vi.useRealTimers();
	}
});

it("starts the backoff over once a connection reaches ready", async () => {
	vi.useFakeTimers();
	try {
		const first = new FakeHubClient();
		harness.client = first;
		mount();
		await act(async () => {});
		const second = new FakeHubClient();
		harness.client = second;
		await act(async () => {
			first.fail();
		});
		await act(async () => {
			vi.advanceTimersByTime(0);
		});
		await act(async () => {});
		await act(async () => {
			second.succeed();
		});
		const third = new FakeHubClient();
		harness.client = third;
		await act(async () => {
			second.fail();
		});
		await act(async () => {
			vi.advanceTimersByTime(0);
		});
		await act(async () => {});
		expect(third.state).toBe("connecting");
	} finally {
		vi.useRealTimers();
	}
});

it("never retries a connection no retry can fix", async () => {
	vi.useFakeTimers();
	try {
		const first = new FakeHubClient();
		harness.client = first;
		const { hook } = mount();
		await act(async () => {});
		const second = new FakeHubClient();
		harness.client = second;
		await act(async () => {
			first.fail("protocol");
		});
		expect(hook.result.current.fatal).toBe(true);
		await act(async () => {
			vi.advanceTimersByTime(60_000);
		});
		await act(async () => {});
		expect(second.state).toBe("idle");
	} finally {
		vi.useRealTimers();
	}
});

it("leaves a backgrounded app alone and tries at once on returning", async () => {
	vi.useFakeTimers();
	try {
		const first = new FakeHubClient();
		harness.client = first;
		const { hook, input } = mount();
		await act(async () => {});
		const second = new FakeHubClient();
		harness.client = second;
		await act(async () => {
			first.fail();
		});
		// rerender() runs its own act(), so it stays outside the async ones.
		input.foreground = false;
		hook.rerender();
		await act(async () => {
			vi.advanceTimersByTime(60_000);
		});
		await act(async () => {});
		expect(second.state).toBe("idle");
		input.foreground = true;
		hook.rerender();
		await act(async () => {});
		expect(second.state).toBe("connecting");
	} finally {
		vi.useRealTimers();
	}
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/hubConnection.test.tsx`
Expected: FAIL. `reconnectDelay` is not exported, and the retry tests time out waiting for a second attempt that never starts.

- [ ] **Step 3: Implement**

In `mobile-native/src/hubConnection.ts`:

1. Export the delay beside `HubConnection`:

```ts
/** How long the connection waits before trying again after `failures`
 * attempts in a row closed without reaching ready (spec 14): at once, then
 * 1, 2, 4, 8 and 16 seconds, then every 30 seconds. */
export function reconnectDelay(failures: number): number {
	return failures <= 0 ? 0 : Math.min(1000 * 2 ** (failures - 1), 30_000);
}
```

2. Inside `useHubConnection`, after `const [store] = useState(...)`:

```ts
	// The hook's own attempts, on top of the caller's `attempt`: each one
	// reopens the connection exactly as a bumped `attempt` does.
	const [retry, setRetry] = useState(0);
	// Attempts in a row that closed without reaching ready.
	const failures = useRef(0);
```

3. Add `retry` to `targetKey` and to the main effect's dependency list:

```ts
	const targetKey = JSON.stringify([activeId, activeOrigin, foreground, attempt, retry]);
```

   Add, before the main effect, a reset for a new hub, a return to the foreground or a manual retry:

```ts
	useEffect(() => {
		failures.current = 0;
	}, [activeId, activeOrigin, foreground, attempt]);
```

4. In the `onStateChange` handler's `next === "ready"` branch, add `failures.current = 0;`.

5. Replace the final `return { ... }` with a computed state, the retry effect, and a return:

```ts
	const state: ConnectionState = client
		? coreState.state
		: closedWithoutClient
			? "closed"
			: activeId && foreground
				? "connecting"
				: "idle";
	// A connection that closed for a reason a retry can fix tries again on its
	// own while the app is in front (spec 14), so no screen needs a Reconnect
	// button. A protocol mismatch (fatal) is left alone: retrying can't fix it.
	useEffect(() => {
		if (state !== "closed" || fatal || !activeId || !activeOrigin || !foreground) return;
		const timer = setTimeout(() => {
			failures.current += 1;
			setRetry((value) => value + 1);
		}, reconnectDelay(failures.current));
		return () => clearTimeout(timer);
	}, [state, fatal, activeId, activeOrigin, foreground]);
	return { client, state, fatal };
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/hubConnection.test.tsx`
Expected: PASS, including every existing test in the file.

- [ ] **Step 5: Run the native gate**

Run: `make test-native`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add mobile-native/src/hubConnection.ts mobile-native/src/hubConnection.test.tsx
git commit -m "feat(native): the hub connection retries a failed first handshake on its own"
```

Open PR A: "feat(native): the connection recovers on its own (phase 2, PR A)". The description names the old behavior (a failed first handshake closed for good, and every return to the foreground opens a fresh client) and the backoff.

---

## PR 1: attention model, device memory, marks

PR 1 lands the pure core and the marks. The Board screen in PR 2 is their first consumer; say so in the PR description.

### Task 2: The attention model

**Files:**
- Create: `mobile-native/src/board/attention.ts`
- Test: `mobile-native/src/board/attention.test.ts`

**Interfaces:**
- Produces (all exported from `attention.ts`):
  - `type BoardState = "failed" | "question" | "approval" | "warning" | "restartNeeded" | "working" | "finished" | "idle" | "shutDown"`
  - `type Band = "needsYou" | "finished" | "working" | "idle"`
  - `interface ClassifiedRow { row: NavigationSessionSummary; state: BoardState }`
  - `interface LiveBands { needsYou: ClassifiedRow[]; finished: ClassifiedRow[]; working: ClassifiedRow[]; idle: ClassifiedRow[] }`
  - `approvalRefs(needsYouSection: readonly NavigationSessionSummary[]): Set<string>`
  - `boardState(row: NavigationSessionSummary, approval: boolean, seen: boolean): BoardState`
  - `bandOf(state: BoardState): Band | null`
  - `liveBands(live: readonly NavigationSessionSummary[], needsYouSection: readonly NavigationSessionSummary[], isSeen: (row: NavigationSessionSummary) => boolean): LiveBands`
  - `interface LiveSummary { needsYou: number; finished: number; working: number; idle: number }`
  - `liveSummary(bands: LiveBands): LiveSummary | null`
  - `summaryText(band: Band, count: number): string`
  - `type Hue = "danger" | "attention"`
  - `interface WhyLine { word?: string; hue?: Hue; text: string }`
  - `whyLine(item: ClassifiedRow): WhyLine | null`
  - `workingActivity(row: NavigationSessionSummary): string`
  - `interface Usual { project?: string; host?: string }`
  - `usualPlace(rows: readonly NavigationSessionSummary[]): Usual`
  - `interface LastLine { project?: string; host?: string }`
  - `lastLine(row: NavigationSessionSummary, usual: Usual, hostLabel: (hostId: string) => string): LastLine | null`
  - `stateWord(state: BoardState): string`

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/board/attention.test.ts
import { describe, expect, it } from "vitest";
import type { NavigationSessionSummary } from "@evener/appwire-client";
import {
	approvalRefs,
	boardState,
	lastLine,
	liveBands,
	liveSummary,
	summaryText,
	usualPlace,
	whyLine,
	workingActivity,
} from "./attention";

const row = (
	ref: string,
	over: Partial<NavigationSessionSummary> = {},
): NavigationSessionSummary => ({
	ref,
	host_id: "local",
	session_id: ref,
	title: ref,
	project: "evener",
	state: "idle",
	kind: "session",
	live: true,
	children: [],
	...over,
});
const at = (minutes: number) => new Date(Date.UTC(2026, 8, 26, 12, minutes)).toISOString();
const never = () => false;

describe("a row's Board state (spec 13.1)", () => {
	it.each([
		[{ state: "errored" }, false, false, "failed"],
		[{ state: "restartRequired" }, false, false, "restartNeeded"],
		[{ state: "warning" }, false, false, "warning"],
		[{ state: "awaiting", ask_pending: true }, false, false, "question"],
		[{ state: "awaiting", ask_pending: true }, true, false, "question"],
		[{ state: "active" }, true, false, "approval"],
		[{ state: "active" }, false, false, "working"],
		[{ state: "awaiting" }, false, false, "finished"],
		[{ state: "awaiting" }, false, true, "idle"],
		[{ state: "idle" }, false, false, "finished"],
		[{ state: "idle", dormant: true }, false, false, "idle"],
		[{ state: "ended" }, false, false, "shutDown"],
		[{ state: "notLoaded" }, false, false, "shutDown"],
		[{ state: "ended", offline: true }, false, false, "shutDown"],
		[{ state: "active", offline: true }, false, false, "shutDown"],
		[{ state: "awaiting", ask_pending: true, offline: true }, false, false, "shutDown"],
	] as const)("%o, approval %s, seen %s → %s", (over, approval, seen, expected) => {
		expect(boardState(row("s", over), approval, seen)).toBe(expected);
	});
});

describe("approvals inferred from the needs_you section (until S2)", () => {
	it("is every row there for no reason its state gives", () => {
		const section = [
			row("a", { state: "awaiting" }),
			row("b", { state: "warning" }),
			row("c", { state: "restartRequired" }),
			row("d", { state: "errored" }),
			row("e", { state: "active" }),
			row("f", { state: "idle" }),
		];
		expect([...approvalRefs(section)].sort()).toEqual(["e", "f"]);
	});
});

describe("Live bands (spec 7.1)", () => {
	it("puts failures first, oldest first, then the rest of Needs you oldest waiting first", () => {
		const live = [
			row("q-new", { state: "awaiting", ask_pending: true, updated_at: at(30) }),
			row("f-new", { state: "errored", updated_at: at(20) }),
			row("w-old", { state: "warning", updated_at: at(5) }),
			row("f-old", { state: "errored", updated_at: at(10) }),
		];
		const bands = liveBands(live, [], never);
		expect(bands.needsYou.map((item) => item.row.ref)).toEqual(["f-old", "f-new", "w-old", "q-new"]);
	});

	it("orders Finished and Idle newest first and keeps the hub's order for Working", () => {
		const live = [
			row("work-b", { state: "active", updated_at: at(1) }),
			row("done-old", { state: "awaiting", updated_at: at(2) }),
			row("work-a", { state: "active", updated_at: at(40) }),
			row("done-new", { state: "awaiting", updated_at: at(50) }),
			row("seen-old", { state: "idle", updated_at: at(3) }),
			row("seen-new", { state: "idle", updated_at: at(4) }),
		];
		const seen = (r: NavigationSessionSummary) => r.ref.startsWith("seen");
		const bands = liveBands(live, [], seen);
		expect(bands.working.map((item) => item.row.ref)).toEqual(["work-b", "work-a"]);
		expect(bands.finished.map((item) => item.row.ref)).toEqual(["done-new", "done-old"]);
		expect(bands.idle.map((item) => item.row.ref)).toEqual(["seen-new", "seen-old"]);
	});

	it("shows a session that needs you even when it sits past the Live pages loaded so far", () => {
		const bands = liveBands(
			[row("loaded", { state: "active" })],
			[row("later", { state: "awaiting", ask_pending: true })],
			never,
		);
		expect(bands.needsYou.map((item) => item.row.ref)).toEqual(["later"]);
	});

	it("marks an approval from the section on the Live copy of the row", () => {
		const bands = liveBands(
			[row("x", { state: "active", children: [row("child", { state: "active" })] })],
			[row("x", { state: "active" })],
			never,
		);
		expect(bands.needsYou).toHaveLength(1);
		expect(bands.needsYou[0]?.state).toBe("approval");
		expect(bands.needsYou[0]?.row.children).toHaveLength(1);
	});

	it("leaves shut-down rows to Projects", () => {
		const bands = liveBands([row("gone", { state: "ended" })], [], never);
		expect(Object.values(bands).flat()).toEqual([]);
	});
});

describe("the Live summary line", () => {
	it("shows only when at least two bands have sessions", () => {
		expect(liveSummary(liveBands([row("a", { state: "active" })], [], never))).toBeNull();
		expect(
			liveSummary(liveBands([row("a", { state: "active" }), row("b", { state: "errored" })], [], never)),
		).toEqual({ needsYou: 1, finished: 0, working: 1, idle: 0 });
	});

	it("says each count the spec's way", () => {
		expect(summaryText("needsYou", 1)).toBe("1 needs you");
		expect(summaryText("needsYou", 4)).toBe("4 need you");
		expect(summaryText("finished", 4)).toBe("4 finished");
		expect(summaryText("working", 9)).toBe("9 working");
		expect(summaryText("idle", 3)).toBe("3 idle");
	});
});

describe("why lines on the fallbacks (spec 7.2, 18)", () => {
	it.each([
		["failed", { word: "Failed", hue: "danger", text: "open the session to see what went wrong" }],
		["question", { word: "Question", hue: "attention", text: "waiting for your answer" }],
		["approval", { word: "Approval", hue: "attention", text: "waiting for your permission" }],
		["warning", { word: "Warning", hue: "attention", text: "open the session to see it" }],
		[
			"restartNeeded",
			{ word: "Restart needed", hue: "attention", text: "restart this session to pick up the hub's update" },
		],
	] as const)("%s", (state, expected) => {
		expect(whyLine({ row: row("s"), state })).toEqual(expected);
	});

	it("has none for Finished (the excerpt is S1) or Idle", () => {
		expect(whyLine({ row: row("s"), state: "finished" })).toBeNull();
		expect(whyLine({ row: row("s"), state: "idle" })).toBeNull();
	});

	it("says what a working session is doing with what the row carries", () => {
		const one = row("s", { state: "active", children: [row("c", { state: "active" })] });
		expect(workingActivity(one)).toBe("Waiting on 1 subagent");
		const three = row("s", {
			state: "active",
			children: [
				row("c1", { state: "active" }),
				row("c2", { state: "active" }),
				row("c3", { state: "active" }),
				row("c4", { state: "idle" }),
			],
		});
		expect(workingActivity(three)).toBe("Waiting on 3 subagents");
		expect(workingActivity({ ...three, more_subagents: 12 })).toBe("Waiting on 3 subagents (+12 more)");
		const running = row("s", {
			state: "active",
			running_jobs: [{ job_id: "j", job_type: "shell", status: "running", command: "go test ./agent/..." }],
		});
		expect(workingActivity(running)).toBe("Running go test ./agent/...");
		expect(workingActivity(row("s", { state: "active" }))).toBe("Working");
		expect(whyLine({ row: running, state: "working" })).toEqual({ text: "Running go test ./agent/..." });
	});
});

describe("the last line prints project and host only when unusual", () => {
	const fleet = [
		row("a", { project: "evener", host_id: "local" }),
		row("b", { project: "evener", host_id: "local" }),
		row("c", { project: "docs", host_id: "paradise-park" }),
	];
	const label = (id: string) => (id === "paradise-park" ? "paradise-park" : "this host");

	it("finds the fleet's usual project and host", () => {
		expect(usualPlace(fleet)).toEqual({ project: "evener", host: "local" });
	});

	it("prints what differs and nothing when nothing does", () => {
		const usual = usualPlace(fleet);
		expect(lastLine(fleet[0] as NavigationSessionSummary, usual, label)).toBeNull();
		expect(lastLine(fleet[2] as NavigationSessionSummary, usual, label)).toEqual({
			project: "docs",
			host: "paradise-park",
		});
	});
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/board/attention.test.ts`
Expected: FAIL: `Cannot find module './attention'`.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/board/attention.ts
// The Board's attention model (spec 13.1-13.2) as pure functions over the
// navigation rows the hub already sends. Where the spec wants a fact the rows
// don't carry yet, the fallback from spec 18 lives here, and each server
// addition replaces its fallback in this file: S1 (why text), S2 (approval
// flag), S3 (subagent failures), S4 (seen marker), S5 (activity), S13 (tasks).
import type { NavigationSessionSummary } from "@evener/appwire-client";

export type BoardState =
	| "failed"
	| "question"
	| "approval"
	| "warning"
	| "restartNeeded"
	| "working"
	| "finished"
	| "idle"
	| "shutDown";

export type Band = "needsYou" | "finished" | "working" | "idle";

export interface ClassifiedRow {
	row: NavigationSessionSummary;
	state: BoardState;
}

export interface LiveBands {
	needsYou: ClassifiedRow[];
	finished: ClassifiedRow[];
	working: ClassifiedRow[];
	idle: ClassifiedRow[];
}

const WORDS: Record<BoardState, string> = {
	failed: "Failed",
	question: "Question",
	approval: "Approval",
	warning: "Warning",
	restartNeeded: "Restart needed",
	working: "Working",
	finished: "Finished",
	idle: "Idle",
	shutDown: "Shut down",
};

export function stateWord(state: BoardState): string {
	return WORDS[state];
}

// The states that put a session in the hub's needs_you section on their own.
const EXPLAINED = new Set(["awaiting", "warning", "restartRequired", "errored"]);

/** The hub promotes a session with a pending sandbox escalation into its
 * needs_you section but leaves the row "active" (promotedAttentionLevel in
 * cmd/evener-hub/internal/hubcore/attention.go), so a row there whose state
 * explains nothing else is there for an approval. S2 puts the flag on rows. */
export function approvalRefs(needsYouSection: readonly NavigationSessionSummary[]): Set<string> {
	const refs = new Set<string>();
	for (const row of needsYouSection) if (!EXPLAINED.has(row.state)) refs.add(row.ref);
	return refs;
}

export function boardState(
	row: NavigationSessionSummary,
	approval: boolean,
	seen: boolean,
): BoardState {
	// A row from an offline source can't be reached, whatever state it last
	// reported: it is never Working, Finished or Needs you.
	if (row.offline) return "shutDown";
	switch (row.state) {
		case "errored":
			return "failed";
		case "restartRequired":
			return "restartNeeded";
		case "warning":
			return "warning";
		case "ended":
		case "notLoaded":
			return "shutDown";
	}
	if (row.state === "awaiting" && row.ask_pending) return "question";
	if (approval) return "approval";
	if (row.state === "active") return "working";
	if (row.dormant || seen) return "idle";
	return "finished";
}

const NEEDS_YOU = new Set<BoardState>(["failed", "question", "approval", "warning", "restartNeeded"]);

export function bandOf(state: BoardState): Band | null {
	if (NEEDS_YOU.has(state)) return "needsYou";
	if (state === "working" || state === "finished" || state === "idle") return state;
	return null;
}

function time(row: NavigationSessionSummary): number {
	const value = row.updated_at ? Date.parse(row.updated_at) : Number.NaN;
	return Number.isFinite(value) ? value : 0;
}
function byRef(a: ClassifiedRow, b: ClassifiedRow): number {
	return a.row.ref < b.row.ref ? -1 : a.row.ref > b.row.ref ? 1 : 0;
}
function oldestFirst(a: ClassifiedRow, b: ClassifiedRow): number {
	return time(a.row) - time(b.row) || byRef(a, b);
}
function newestFirst(a: ClassifiedRow, b: ClassifiedRow): number {
	return time(b.row) - time(a.row) || byRef(a, b);
}
function needsYouOrder(a: ClassifiedRow, b: ClassifiedRow): number {
	const rank = (item: ClassifiedRow) => (item.state === "failed" ? 0 : 1);
	return rank(a) - rank(b) || oldestFirst(a, b);
}

/** Splits Live into the spec's four bands. Rows from the needs_you section
 * join when Live's loaded pages don't hold them yet, so a session that needs
 * you is never hidden behind "load more"; a row in both keeps its Live copy,
 * which carries children. Working keeps the hub's Live order (ruling 10). */
export function liveBands(
	live: readonly NavigationSessionSummary[],
	needsYouSection: readonly NavigationSessionSummary[],
	isSeen: (row: NavigationSessionSummary) => boolean,
): LiveBands {
	const approvals = approvalRefs(needsYouSection);
	const rows = new Map<string, NavigationSessionSummary>();
	for (const row of live) rows.set(row.ref, row);
	for (const row of needsYouSection) if (!rows.has(row.ref)) rows.set(row.ref, row);
	const bands: LiveBands = { needsYou: [], finished: [], working: [], idle: [] };
	for (const row of rows.values()) {
		const state = boardState(row, approvals.has(row.ref), isSeen(row));
		const band = bandOf(state);
		if (band) bands[band].push({ row, state });
	}
	bands.needsYou.sort(needsYouOrder);
	bands.finished.sort(newestFirst);
	bands.idle.sort(newestFirst);
	return bands;
}

export interface LiveSummary {
	needsYou: number;
	finished: number;
	working: number;
	idle: number;
}

/** The Live summary line's counts, or null when fewer than two bands have
 * sessions and the band headers already say it all (spec 7.1). */
export function liveSummary(bands: LiveBands): LiveSummary | null {
	const counts: LiveSummary = {
		needsYou: bands.needsYou.length,
		finished: bands.finished.length,
		working: bands.working.length,
		idle: bands.idle.length,
	};
	return Object.values(counts).filter((count) => count > 0).length >= 2 ? counts : null;
}

export function summaryText(band: Band, count: number): string {
	if (band === "needsYou") return `${count} ${count === 1 ? "needs you" : "need you"}`;
	return `${count} ${band}`;
}

export type Hue = "danger" | "attention";

export interface WhyLine {
	word?: string;
	hue?: Hue;
	text: string;
}

// Until S1 carries the question, the approval's target and the error, the
// reason says what the person can do next.
const REASONS: Partial<Record<BoardState, { hue: Hue; text: string }>> = {
	failed: { hue: "danger", text: "open the session to see what went wrong" },
	question: { hue: "attention", text: "waiting for your answer" },
	approval: { hue: "attention", text: "waiting for your permission" },
	warning: { hue: "attention", text: "open the session to see it" },
	restartNeeded: { hue: "attention", text: "restart this session to pick up the hub's update" },
};

export function whyLine(item: ClassifiedRow): WhyLine | null {
	if (item.state === "working") return { text: workingActivity(item.row) };
	const reason = REASONS[item.state];
	return reason ? { word: WORDS[item.state], ...reason } : null;
}

/** What a working session is doing, from what its row carries (S5 adds the
 * current step and quiet spells). */
export function workingActivity(row: NavigationSessionSummary): string {
	const subagents = row.children.filter((child) => child.state === "active").length;
	// The hub caps a row's children; until S3 tallies the whole tree, say how
	// many more there are rather than undercounting (spec 18, S3's fallback).
	const more = row.more_subagents ?? 0;
	if (subagents > 0)
		return `Waiting on ${subagents} ${subagents === 1 ? "subagent" : "subagents"}${more > 0 ? ` (+${more} more)` : ""}`;
	const command = row.running_jobs?.find((job) => job.command)?.command;
	if (command) return `Running ${command}`;
	return "Working";
}

export interface Usual {
	project?: string;
	host?: string;
}

function mostCommon(values: readonly string[]): string | undefined {
	const counts = new Map<string, number>();
	for (const value of values) if (value) counts.set(value, (counts.get(value) ?? 0) + 1);
	let best: string | undefined;
	let bestCount = 0;
	for (const [value, count] of counts)
		if (count > bestCount || (count === bestCount && best !== undefined && value < best)) {
			best = value;
			bestCount = count;
		}
	return best;
}

/** The fleet's usual project and host: the most common among Live rows. A row
 * prints either only when it differs (spec 7.2). */
export function usualPlace(rows: readonly NavigationSessionSummary[]): Usual {
	return {
		project: mostCommon(rows.map((row) => row.project)),
		host: mostCommon(rows.map((row) => row.host_id)),
	};
}

export interface LastLine {
	project?: string;
	host?: string;
}

/** The row's last line on the fallbacks: task progress waits for S13 and
 * subagent failures for S3, so today it is the unusual project and host. */
export function lastLine(
	row: NavigationSessionSummary,
	usual: Usual,
	hostLabel: (hostId: string) => string,
): LastLine | null {
	const line: LastLine = {};
	if (row.project && row.project !== usual.project) line.project = row.project;
	if (row.host_id !== usual.host) line.host = hostLabel(row.host_id);
	return line.project || line.host ? line : null;
}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/board/attention.test.ts`
Expected: PASS.

If `offline` is missing from `NavigationSessionSummary` in `appwire-client/typescript/types.gen.ts`, main predates #2453: rebase onto `origin/main` first.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/board/attention.ts mobile-native/src/board/attention.test.ts
git commit -m "feat(native): the Board's attention model on the navigation fallbacks"
```

### Task 3: The Board's device memory (seen markers, folded sections)

**Files:**
- Create: `mobile-native/src/board/boardMemory.ts`
- Create: `mobile-native/src/board/nativeBoardMemory.ts`
- Modify: `mobile-native/src/ConnectionProvider.tsx` (`removeHub` forgets the Board too)
- Test: `mobile-native/src/board/boardMemory.test.ts`

**Interfaces:**
- Produces:
  - `interface BoardStorage { getItemSync(key: string): string | null; setItemSync(key: string, value: string): void; removeItemSync(key: string): void }`
  - `class SeenMarkers`:
    - constructor `(storage: BoardStorage, hubId: string)`
    - `isSeen(row: { ref: string; updated_at?: string }): boolean`
    - `adoptEpoch(rows: readonly { updated_at?: string }[]): void`
    - `markSeen(row: { ref: string; updated_at?: string }): void`
    - `markUnread(ref: string): void`
    - `subscribe(listener: () => void): () => void`
    - `getRevision(): number`
  - `class FoldedSections`:
    - constructor `(storage: BoardStorage, hubId: string)`
    - `isFolded(section: string, byDefault: boolean): boolean`
    - `setFolded(section: string, folded: boolean): void`
  - `forgetBoard(storage: BoardStorage, hubId: string): void`
  - From `nativeBoardMemory.ts`: `seenMarkers(hubId: string): SeenMarkers`, `foldedSections(hubId: string): FoldedSections` and `forgetBoardForHub(hubId: string): void`. These are per-hub singletons over `expo-sqlite/kv-store`.

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/board/boardMemory.test.ts
import { describe, expect, it } from "vitest";
import { type BoardStorage, FoldedSections, forgetBoard, SeenMarkers } from "./boardMemory";

function memoryStorage(values = new Map<string, string>()): BoardStorage & { values: Map<string, string> } {
	return {
		values,
		getItemSync: (key) => values.get(key) ?? null,
		setItemSync: (key, value) => void values.set(key, value),
		removeItemSync: (key) => void values.delete(key),
	};
}
const at = (minutes: number) => new Date(Date.UTC(2026, 8, 26, 12, minutes)).toISOString();

describe("seen markers", () => {
	it("treats everything as seen until the first load sets the epoch", () => {
		const seen = new SeenMarkers(memoryStorage(), "hub-a");
		expect(seen.isSeen({ ref: "a", updated_at: at(5) })).toBe(true);
	});

	it("on first run, adopts the newest row as the epoch so nothing past floods Finished", () => {
		const seen = new SeenMarkers(memoryStorage(), "hub-a");
		seen.adoptEpoch([{ updated_at: at(5) }, { updated_at: at(9) }, {}]);
		expect(seen.isSeen({ ref: "old", updated_at: at(9) })).toBe(true);
		expect(seen.isSeen({ ref: "new", updated_at: at(10) })).toBe(false);
	});

	it("never moves the epoch once set", () => {
		const seen = new SeenMarkers(memoryStorage(), "hub-a");
		seen.adoptEpoch([{ updated_at: at(9) }]);
		seen.adoptEpoch([{ updated_at: at(30) }]);
		expect(seen.isSeen({ ref: "x", updated_at: at(20) })).toBe(false);
	});

	it("compares hub timestamps only, so a later turn is unseen again", () => {
		const seen = new SeenMarkers(memoryStorage(), "hub-a");
		seen.adoptEpoch([{ updated_at: at(0) }]);
		seen.markSeen({ ref: "a", updated_at: at(10) });
		expect(seen.isSeen({ ref: "a", updated_at: at(10) })).toBe(true);
		expect(seen.isSeen({ ref: "a", updated_at: at(11) })).toBe(false);
	});

	it("keeps Mark as unread until the session is opened", () => {
		const seen = new SeenMarkers(memoryStorage(), "hub-a");
		seen.adoptEpoch([{ updated_at: at(30) }]);
		seen.markUnread("a");
		expect(seen.isSeen({ ref: "a", updated_at: at(1) })).toBe(false);
		seen.markSeen({ ref: "a", updated_at: at(1) });
		expect(seen.isSeen({ ref: "a", updated_at: at(1) })).toBe(true);
	});

	it("survives a relaunch and keeps hubs apart", () => {
		const storage = memoryStorage();
		const first = new SeenMarkers(storage, "hub-a");
		first.adoptEpoch([{ updated_at: at(0) }]);
		first.markSeen({ ref: "a", updated_at: at(10) });
		expect(new SeenMarkers(storage, "hub-a").isSeen({ ref: "a", updated_at: at(10) })).toBe(true);
		expect(storage.values.has("evener.native.seen.hub-a")).toBe(true);
		const other = new SeenMarkers(storage, "hub-b");
		other.adoptEpoch([{ updated_at: at(0) }]);
		expect(other.isSeen({ ref: "a", updated_at: at(10) })).toBe(false);
	});

	it("reads corrupt storage as empty", () => {
		const storage = memoryStorage(new Map([["evener.native.seen.hub-a", "{not json"]]));
		const seen = new SeenMarkers(storage, "hub-a");
		expect(seen.isSeen({ ref: "a", updated_at: at(1) })).toBe(true);
	});

	it("keeps working in memory when storage throws", () => {
		const broken: BoardStorage = {
			getItemSync: () => {
				throw new Error("disk");
			},
			setItemSync: () => {
				throw new Error("disk");
			},
			removeItemSync: () => {},
		};
		const seen = new SeenMarkers(broken, "hub-a");
		seen.adoptEpoch([{ updated_at: at(0) }]);
		seen.markSeen({ ref: "a", updated_at: at(3) });
		expect(seen.isSeen({ ref: "a", updated_at: at(3) })).toBe(true);
	});

	it("keeps unread marks and the newest 500 seen marks", () => {
		const storage = memoryStorage();
		const seen = new SeenMarkers(storage, "hub-a");
		seen.adoptEpoch([{ updated_at: at(0) }]);
		seen.markUnread("keep-unread");
		for (let i = 1; i <= 505; i++)
			seen.markSeen({ ref: `s${i}`, updated_at: new Date(Date.UTC(2026, 8, 26, 13, 0, i)).toISOString() });
		const stored = JSON.parse(storage.values.get("evener.native.seen.hub-a") as string);
		expect(Object.keys(stored.sessions)).toHaveLength(500);
		expect(stored.sessions["keep-unread"]).toEqual({ unread: true });
		expect(stored.sessions.s505).toBeDefined();
		expect(stored.sessions.s1).toBeUndefined();
	});

	it("tells subscribers when something changes", () => {
		const seen = new SeenMarkers(memoryStorage(), "hub-a");
		let calls = 0;
		const stop = seen.subscribe(() => calls++);
		const before = seen.getRevision();
		seen.markUnread("a");
		expect(calls).toBe(1);
		expect(seen.getRevision()).toBe(before + 1);
		stop();
		seen.markUnread("b");
		expect(calls).toBe(1);
	});
});

describe("folded sections", () => {
	it("falls back to the section's default, then remembers per hub", () => {
		const storage = memoryStorage();
		const folded = new FoldedSections(storage, "hub-a");
		expect(folded.isFolded("idle", true)).toBe(true);
		folded.setFolded("idle", false);
		expect(new FoldedSections(storage, "hub-a").isFolded("idle", true)).toBe(false);
		expect(new FoldedSections(storage, "hub-b").isFolded("idle", true)).toBe(true);
	});
});

it("forgetting a hub removes both of its keys and nothing else", () => {
	const storage = memoryStorage(
		new Map([
			["evener.native.seen.hub-a", "{}"],
			["evener.native.board-sections.hub-a", "{}"],
			["evener.native.seen.hub-b", "{}"],
		]),
	);
	forgetBoard(storage, "hub-a");
	expect([...storage.values.keys()]).toEqual(["evener.native.seen.hub-b"]);
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/board/boardMemory.test.ts`
Expected: FAIL: `Cannot find module './boardMemory'`.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/board/boardMemory.ts
// What this device remembers about one hub's Board: which sessions you have
// seen and which sections you folded. Kept in expo-sqlite's kv-store under
// per-hub keys that ConnectionProvider.removeHub clears.

export interface BoardStorage {
	getItemSync(key: string): string | null;
	setItemSync(key: string, value: string): void;
	removeItemSync(key: string): void;
}

const seenKey = (hubId: string) => `evener.native.seen.${hubId}`;
const foldedKey = (hubId: string) => `evener.native.board-sections.${hubId}`;
const SEEN_LIMIT = 500;

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}
function readJson(storage: BoardStorage, key: string): unknown {
	try {
		const raw = storage.getItemSync(key);
		return raw ? JSON.parse(raw) : null;
	} catch {
		return null;
	}
}
function writeJson(storage: BoardStorage, key: string, value: unknown): void {
	try {
		storage.setItemSync(key, JSON.stringify(value));
	} catch {
		// The in-memory copy still serves this launch.
	}
}
function timeOf(value: string | null | undefined): number | null {
	if (!value) return null;
	const time = Date.parse(value);
	return Number.isFinite(time) ? time : null;
}

interface SeenRecord {
	through?: string;
	unread?: true;
}
interface SeenState {
	epoch: string | null;
	sessions: Record<string, SeenRecord>;
}

function parseSeen(value: unknown): SeenState {
	const state: SeenState = { epoch: null, sessions: {} };
	if (!isRecord(value)) return state;
	if (typeof value.epoch === "string") state.epoch = value.epoch;
	if (isRecord(value.sessions))
		for (const [ref, record] of Object.entries(value.sessions)) {
			if (!isRecord(record)) continue;
			if (record.unread === true) state.sessions[ref] = { unread: true };
			else if (typeof record.through === "string") state.sessions[ref] = { through: record.through };
		}
	return state;
}

/** Whether you have opened each session since its last turn ended: Finished
 * until seen, then Idle (spec 13.1). Every comparison is between the hub's
 * own timestamps (a row's updated_at against the updated_at stored when you
 * opened it), so the phone's clock never matters. The first load on a device
 * adopts the newest updated_at it sees as an epoch, so sessions that ended
 * before this device ever showed the Board don't all arrive as unseen. S4
 * replaces this with a marker on the hub. */
export class SeenMarkers {
	private state: SeenState;
	private revision = 0;
	private listeners = new Set<() => void>();

	constructor(
		private readonly storage: BoardStorage,
		private readonly hubId: string,
	) {
		this.state = parseSeen(readJson(storage, seenKey(hubId)));
	}

	isSeen(row: { ref: string; updated_at?: string }): boolean {
		const record = this.state.sessions[row.ref];
		if (record?.unread) return false;
		if (this.state.epoch === null) return true;
		const updated = timeOf(row.updated_at);
		if (updated === null) return true;
		const through = Math.max(
			timeOf(record?.through) ?? Number.NEGATIVE_INFINITY,
			timeOf(this.state.epoch) ?? Number.NEGATIVE_INFINITY,
		);
		return updated <= through;
	}

	adoptEpoch(rows: readonly { updated_at?: string }[]): void {
		if (this.state.epoch !== null) return;
		let newest: { value: string; time: number } | null = null;
		for (const row of rows) {
			const time = timeOf(row.updated_at);
			if (time !== null && row.updated_at && (newest === null || time > newest.time))
				newest = { value: row.updated_at, time };
		}
		if (!newest) return;
		this.state.epoch = newest.value;
		this.save();
	}

	markSeen(row: { ref: string; updated_at?: string }): void {
		if (row.updated_at) this.state.sessions[row.ref] = { through: row.updated_at };
		else delete this.state.sessions[row.ref];
		this.save();
	}

	markUnread(ref: string): void {
		this.state.sessions[ref] = { unread: true };
		this.save();
	}

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => this.listeners.delete(listener);
	};

	getRevision = (): number => this.revision;

	private save(): void {
		const entries = Object.entries(this.state.sessions);
		if (entries.length > SEEN_LIMIT) {
			// Unread marks are choices you made; past the limit, the oldest seen
			// marks go first (they matter least: the epoch covers old sessions).
			entries.sort(
				([, a], [, b]) =>
					(a.unread ? 0 : 1) - (b.unread ? 0 : 1) ||
					(timeOf(b.through) ?? 0) - (timeOf(a.through) ?? 0),
			);
			this.state.sessions = Object.fromEntries(entries.slice(0, SEEN_LIMIT));
		}
		writeJson(this.storage, seenKey(this.hubId), this.state);
		this.revision++;
		for (const listener of [...this.listeners]) listener();
	}
}

/** Which Board sections you folded, per device and hub (spec 7.1). */
export class FoldedSections {
	private folded: Record<string, boolean> = {};

	constructor(
		private readonly storage: BoardStorage,
		private readonly hubId: string,
	) {
		const value = readJson(storage, foldedKey(hubId));
		if (isRecord(value))
			for (const [section, folded] of Object.entries(value))
				if (typeof folded === "boolean") this.folded[section] = folded;
	}

	isFolded(section: string, byDefault: boolean): boolean {
		return this.folded[section] ?? byDefault;
	}

	setFolded(section: string, folded: boolean): void {
		this.folded[section] = folded;
		writeJson(this.storage, foldedKey(this.hubId), this.folded);
	}
}

export function forgetBoard(storage: BoardStorage, hubId: string): void {
	for (const key of [seenKey(hubId), foldedKey(hubId)])
		try {
			storage.removeItemSync(key);
		} catch {
			// Nothing stored to forget.
		}
}
```

```ts
// mobile-native/src/board/nativeBoardMemory.ts
import { Storage } from "expo-sqlite/kv-store";
import { FoldedSections, forgetBoard, SeenMarkers } from "./boardMemory";

// One instance per hub, so every screen reading the Board's memory sees the
// same in-memory state and the same subscribers.
const seen = new Map<string, SeenMarkers>();
const folded = new Map<string, FoldedSections>();

export function seenMarkers(hubId: string): SeenMarkers {
	let markers = seen.get(hubId);
	if (!markers) {
		markers = new SeenMarkers(Storage, hubId);
		seen.set(hubId, markers);
	}
	return markers;
}

export function foldedSections(hubId: string): FoldedSections {
	let sections = folded.get(hubId);
	if (!sections) {
		sections = new FoldedSections(Storage, hubId);
		folded.set(hubId, sections);
	}
	return sections;
}

export function forgetBoardForHub(hubId: string): void {
	seen.delete(hubId);
	folded.delete(hubId);
	forgetBoard(Storage, hubId);
}
```

In `mobile-native/src/ConnectionProvider.tsx`, import `forgetBoardForHub` from `./board/nativeBoardMemory` and call it inside `removeHub`'s callback beside the other clean-ups:

```ts
				removeHub(hubId: string) {
					drafts.removeHub(hubId);
					readerPositions.removeHub(hubId);
					removeOrganizationData(hubId);
					forgetBoardForHub(hubId);
				},
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/board/boardMemory.test.ts`
Expected: PASS.

- [ ] **Step 5: Run the native gate**

Run: `make test-native`
Expected: PASS. `ConnectionProvider` already imports `expo-sqlite/kv-store` through `nativeReaderPosition.ts`, so no test mock changes.

- [ ] **Step 6: Commit**

```bash
git add mobile-native/src/board/boardMemory.ts mobile-native/src/board/nativeBoardMemory.ts mobile-native/src/board/boardMemory.test.ts mobile-native/src/ConnectionProvider.tsx
git commit -m "feat(native): the Board remembers what you've seen and what you folded"
```

### Task 4: State marks and the pulse meter

**Files:**
- Modify: `mobile-native/package.json` and `package-lock.json` (`expo-symbols`), `mobile-native/Podfile.lock` (regenerated, never hand-edited)
- Create: `mobile-native/src/board/pulse.ts`, `mobile-native/src/board/PulseMeter.tsx` and `mobile-native/src/board/StateMark.tsx`
- Test: `mobile-native/src/board/pulse.test.ts` and `mobile-native/src/board/StateMark.test.tsx`

**Interfaces:**
- Consumes: `BoardState` from Task 2.
- Produces:
  - `PULSE_FULL_SCALE = 64`, `PULSE_BARS = 7` and `WORKING_WITHOUT_ACTIVITY: readonly number[]`
  - `pulseBars(perMinute?: readonly number[]): { height: number; opacity: number }[]`
  - `<PulseMeter perMinute?: readonly number[] tone?: "alive" | "attention" | "gray" />`
  - `markFor(state: BoardState, moving: boolean): { name: SFSymbol; tint: "danger" | "attention" | "accent" | "alive"; size: number } | "meter" | null`
  - `<StateMark state: BoardState moving?: boolean connected?: boolean />`

- [ ] **Step 1: Add expo-symbols**

From `mobile-native`, with the install a real directory (check `[ -L node_modules ]` prints nothing first):

```bash
npx expo install expo-symbols
```

Expected: `package.json` gains `"expo-symbols": "~57.0.3"`.

- [ ] **Step 2: Write the failing tests**

```ts
// mobile-native/src/board/pulse.test.ts
import { expect, it } from "vitest";
import { PULSE_BARS, pulseBars, WORKING_WITHOUT_ACTIVITY } from "./pulse";

it("uses one fixed log scale, so a trickle never looks like a flood", () => {
	const bars = pulseBars([0, 1, 8, 64, 500, -3, 0]);
	expect(bars.map((bar) => Number(bar.height.toFixed(3)))).toEqual([0, 0.166, 0.526, 1, 1, 0, 0]);
});

it("fades older minutes so time reads left to right", () => {
	const opacities = pulseBars([1, 1, 1, 1, 1, 1, 1]).map((bar) => bar.opacity);
	expect(opacities[0]).toBeCloseTo(0.35);
	expect(opacities[PULSE_BARS - 1]).toBeCloseTo(1);
	expect([...opacities].sort((a, b) => a - b)).toEqual(opacities);
});

it("pads a short history on the left and keeps only the newest seven minutes", () => {
	expect(pulseBars([64]).map((bar) => bar.height)).toEqual([0, 0, 0, 0, 0, 0, 1]);
	expect(pulseBars([64, 0, 0, 0, 0, 0, 0, 0]).map((bar) => bar.height)).toEqual([0, 0, 0, 0, 0, 0, 0]);
});

it("draws the one-bar fallback until the hub reports activity (S5)", () => {
	expect(pulseBars()).toEqual(pulseBars(WORKING_WITHOUT_ACTIVITY));
	expect(pulseBars().filter((bar) => bar.height > 0)).toHaveLength(1);
});
```

```tsx
// mobile-native/src/board/StateMark.test.tsx
import { describe, expect, it, vi } from "vitest";
import { render } from "../renderNative.testkit";
import { markFor, StateMark } from "./StateMark";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

describe("state marks pair shape with color (spec 13.1)", () => {
	it.each([
		["failed", "xmark.octagon.fill", "danger"],
		["question", "questionmark.circle.fill", "attention"],
		["approval", "hand.raised.circle.fill", "attention"],
		["warning", "exclamationmark.triangle.fill", "attention"],
		["restartNeeded", "arrow.triangle.2.circlepath.circle.fill", "attention"],
		["finished", "circle.fill", "accent"],
	] as const)("%s", (state, name, tint) => {
		expect(markFor(state, false)).toMatchObject({ name, tint });
	});

	it("moves only on Live's working rows; elsewhere working is a still green dot", () => {
		expect(markFor("working", true)).toBe("meter");
		expect(markFor("working", false)).toMatchObject({ name: "circle.fill", tint: "alive", size: 8 });
	});

	it("draws nothing for idle and shut-down rows", () => {
		expect(markFor("idle", false)).toBeNull();
		expect(markFor("shutDown", false)).toBeNull();
	});

	it("names the state for VoiceOver and tints from the palette", () => {
		const tree = render(<StateMark state="failed" />);
		const symbol = tree.root.findByType("SymbolView" as never);
		expect(symbol.props.name).toBe("xmark.octagon.fill");
		expect(symbol.props.tintColor).toMatch(/^#/);
		expect(tree.root.findAll((node) => node.props.accessibilityLabel === "Failed").length).toBeGreaterThan(0);
	});
});
```

- [ ] **Step 3: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/board/pulse.test.ts src/board/StateMark.test.tsx`
Expected: FAIL: the modules don't exist.

- [ ] **Step 4: Implement**

```ts
// mobile-native/src/board/pulse.ts
// The pulse meter's geometry (spec 16.4): seven one-minute bars, newest on
// the right, on one fixed fleet-wide log scale so two meters compare and a
// trickle never looks like a flood.

/** Events per minute at which a bar reaches full height. */
export const PULSE_FULL_SCALE = 64;
export const PULSE_BARS = 7;

/** Until the hub reports per-minute activity (S5), a working session shows a
 * single bar in its newest minute: the spec's fallback. */
export const WORKING_WITHOUT_ACTIVITY: readonly number[] = [0, 0, 0, 0, 0, 0, 8];

export interface PulseBar {
	/** 0 to 1 of the meter's height; the meter always draws a 1pt baseline. */
	height: number;
	opacity: number;
}

export function pulseBars(perMinute: readonly number[] = WORKING_WITHOUT_ACTIVITY): PulseBar[] {
	const recent = perMinute.slice(-PULSE_BARS);
	const minutes = [...Array<number>(PULSE_BARS - recent.length).fill(0), ...recent];
	return minutes.map((events, index) => ({
		height: events <= 0 ? 0 : Math.min(1, Math.log2(1 + events) / Math.log2(1 + PULSE_FULL_SCALE)),
		opacity: 0.35 + (0.65 * index) / (PULSE_BARS - 1),
	}));
}
```

```tsx
// mobile-native/src/board/PulseMeter.tsx
import { View } from "react-native";
import { useColors } from "../ui";
import { PULSE_BARS, pulseBars } from "./pulse";

const WIDTH = 22;
const HEIGHT = 15;

/** The Board's one piece of motion (spec 16.4). It is still until the hub
 * reports per-minute activity (S5): motion is evidence. */
export function PulseMeter({
	perMinute,
	tone = "alive",
}: {
	perMinute?: readonly number[];
	tone?: "alive" | "attention" | "gray";
}) {
	const { palette } = useColors();
	const color = tone === "gray" ? palette.inkLow : palette[tone];
	return (
		<View
			style={{ width: WIDTH, height: HEIGHT, flexDirection: "row", alignItems: "flex-end", gap: 1 }}
			accessibilityElementsHidden
			importantForAccessibility="no-hide-descendants"
		>
			{pulseBars(perMinute).map((bar, index) => (
				<View
					key={`minutes-ago-${PULSE_BARS - 1 - index}`}
					style={{ flex: 1, height: Math.max(1, bar.height * HEIGHT), backgroundColor: color, opacity: bar.opacity }}
				/>
			))}
		</View>
	);
}
```

```tsx
// mobile-native/src/board/StateMark.tsx
import { type SFSymbol, SymbolView } from "expo-symbols";
import { View } from "react-native";
import { useColors } from "../ui";
import { type BoardState, stateWord } from "./attention";
import { PulseMeter } from "./PulseMeter";

type Tint = "danger" | "attention" | "accent" | "alive";
export type Glyph = { name: SFSymbol; tint: Tint; size: number };

/** A row's leading mark (spec 13.1). `moving` is true only on Live's working
 * rows: one meter per view, and a pinned or project copy of the same session
 * gets a still green dot. */
export function markFor(state: BoardState, moving: boolean): Glyph | "meter" | null {
	switch (state) {
		case "failed":
			return { name: "xmark.octagon.fill", tint: "danger", size: 20 };
		case "question":
			return { name: "questionmark.circle.fill", tint: "attention", size: 20 };
		case "approval":
			return { name: "hand.raised.circle.fill", tint: "attention", size: 20 };
		case "warning":
			return { name: "exclamationmark.triangle.fill", tint: "attention", size: 20 };
		case "restartNeeded":
			return { name: "arrow.triangle.2.circlepath.circle.fill", tint: "attention", size: 20 };
		case "finished":
			return { name: "circle.fill", tint: "accent", size: 8 };
		case "working":
			return moving ? "meter" : { name: "circle.fill", tint: "alive", size: 8 };
		default:
			return null;
	}
}

export function StateMark({
	state,
	moving = false,
	connected = true,
}: {
	state: BoardState;
	moving?: boolean;
	connected?: boolean;
}) {
	const { palette } = useColors();
	const mark = markFor(state, moving);
	return (
		<View
			style={{ width: 28, alignItems: "center", justifyContent: "center" }}
			accessible={mark !== null}
			accessibilityLabel={mark ? stateWord(state) : undefined}
		>
			{mark === "meter" ? (
				<PulseMeter tone={connected ? "alive" : "gray"} />
			) : mark ? (
				<SymbolView name={mark.name} tintColor={palette[mark.tint]} size={mark.size} />
			) : null}
		</View>
	);
}
```

- [ ] **Step 5: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/board/pulse.test.ts src/board/StateMark.test.tsx`
Expected: PASS. The first test's heights are `log2(1 + n) / log2(65)`: 0.166 for 1 event a minute and 0.526 for 8.

- [ ] **Step 6: Regenerate the CocoaPods lock the documented way**

From `mobile-native`, exactly as phase 1 did (Ruby 3.3.6, Bundler 2.7.2):

```bash
rbenv shell 3.3.6
bundle _2.7.2_ install
npx expo prebuild --platform ios --no-install
cp Podfile.lock ios/Podfile.lock
bundle _2.7.2_ exec pod install --project-directory=ios
cp ios/Podfile.lock Podfile.lock
git diff --stat Podfile.lock
bundle _2.7.2_ exec pod install --deployment --project-directory=ios
```

Expected: the diff adds the `ExpoSymbols` pod and changes nothing unrelated, and the `--deployment` install succeeds. If anything else in the lock changes, stop and diagnose (`docs/design/mobile/ios-build-distribution.md`). `ios/` stays untracked.

- [ ] **Step 7: Build and look**

Run `make test-native` (it includes the Metro bundle gate), then build the Evener scheme in Release for an iOS simulator and confirm the app launches. The marks have no screen until PR 2.

- [ ] **Step 8: Commit**

```bash
git add mobile-native/package.json mobile-native/package-lock.json mobile-native/Podfile.lock mobile-native/src/board/pulse.ts mobile-native/src/board/pulse.test.ts mobile-native/src/board/PulseMeter.tsx mobile-native/src/board/StateMark.tsx mobile-native/src/board/StateMark.test.tsx
git commit -m "feat(native): SF Symbol state marks and the pulse meter"
```

Open PR 1: "feat(native): the Board's attention model, memory and marks (phase 2, PR 1)". The description says the Board screen (PR 2) is their first consumer.

---

## PR 2: the Board, Live first

PR 2 makes the Board home. Pinned categories, Projects and Archived appear as section rows that open today's screens until PR 3 brings them inline, and search keeps today's `thread/list` search until PR 5. Nothing the old home reached becomes unreachable.

### Task 5: The Board's data

**Files:**
- Create: `mobile-native/src/board/boardData.ts`
- Test: `mobile-native/src/board/boardData.test.ts`

**Interfaces:**
- Consumes:
  - `NavigationPages<T>` (`src/navigationPages.ts`): constructor `(client, params, field, key, limit)`, `getSnapshot`, `subscribe`, `watch`, `more`, `refresh`, `cancel`, `resume`.
  - `decodeNavigationResponse`, `materializeSnapshot` and `navigationParamsToResourceKey` from `@evener/appwire-client/state/navigation`, used the way `src/navigationReadback.ts` uses them.
- Produces:

```ts
type Page<T> = ReturnType<NavigationPages<T>["getSnapshot"]>;
export interface BoardSnapshot {
	/** Sources (hosts, online or not) and section and catalog counts. */
	manifest: NavigationManifest | null;
	live: Page<NavigationSessionSummary>;
	needsYou: Page<NavigationSessionSummary>;
	pins: Page<NavigationPinSectionDescriptor>;
	/** True from the first time Live loaded for this hub, and never false again. */
	loaded: boolean;
	/** True while the rows shown were read over an earlier connection and the
	 * current one hasn't replaced them yet. */
	retained: boolean;
	error: string | null;
}
export interface BoardController {
	getSnapshot(): BoardSnapshot;
	subscribe(listener: () => void): () => void;
	/** Bind to the hub's current client, or null while disconnected or in the
	 * background. Rows already shown stay until the new client's reads land. */
	setClient(client: ConversationClientLike | null): void;
	loadMoreLive(): Promise<void>;
	pause(): void;
	resume(): void;
	dispose(): void;
}
export function createBoardController(): BoardController;
```

**Requirements:**
1. `setClient(client)` with a client creates four readers:
   - Live: `new NavigationPages<NavigationSessionSummary>(client, { resource: "section", section: "live" }, "sessions", (row) => row.ref, 50)`.
   - Needs you: the same with `section: "needs_you"`. Keep reading its pages with `more()` until `remaining` is 0: the spec's Needs you must be complete, and the hub caps a page at 50.
   - The pin catalog: `{ resource: "pin_catalog" }`, field `"pin_sections"`, key `(row) => row.id`, limit 100.
   - The manifest: read with `evener/navigation/read { resource: "manifest", representationVersion: 2 }`, decoded and materialized as in `navigationReadback.ts`. It is re-read on any `evener/navigation/invalidated` whose targets include `{ kind: "manifest" }`, coalesced with `singleFlight` from `src/singleFlight.ts`. The reader remembers the highest revision announced. An invalidation that arrives while a read is in flight schedules one follow-up read, unless the in-flight answer already reaches that revision. An invalidation with no revision always reads again. So no announced change is lost, and two announcements during one read cost one follow-up, not two.

   Each paged reader is `watch()`ed so invalidations re-read it (`NavigationPages` does the work).
2. `setClient(null)` cancels the readers (`cancel()`) and keeps the last snapshot's rows with `retained: true`. A later `setClient(next)` builds fresh readers on `next`. Until each fresh reader's first read lands, the snapshot keeps that reader's retained rows. When it lands, its rows replace the retained ones and `retained` turns false once all four readers (Live, Needs you, the pin catalog and the manifest) have landed. No snapshot emitted after `loaded` first turns true may have an empty Live whose retained copy had rows, unless the fresh read itself returned no rows.
3. A failed first read sets `error` to the reader's message and leaves `loaded` false. A failed read after `loaded` keeps the rows and sets `error`. The screen doesn't show it, because the Board's toolbar status and the retry in Task 1 cover connection loss (spec 14: errors inline, specific, one action; a navigation read has no action of its own).
4. `loadMoreLive()` calls `more()` on Live. `pause()` and `resume()` call `cancel()` and `resume()` on every reader; the screen pauses while it isn't focused. `dispose()` unsubscribes and unwatches everything.
5. `getSnapshot()` returns the same object until something changes, because the screen reads it through `useSyncExternalStore`.

- [ ] **Step 1: Write the failing tests**

Model the harness on `src/projectBrowser.test.ts`: `boundary()` returns `{ client, requests, listeners }`, a `response(params, data, revision)` helper wraps `wireV2`, and requests are answered in order. Write these tests:

```ts
// mobile-native/src/board/boardData.test.ts (the first test, in full; the rest follow its pattern)
import { expect, it } from "vitest";
import type { AnyNotification, NavigationReadParams, NavigationReadResponse } from "@evener/appwire-client";
import { wireV2 } from "@evener/appwire-client/testing/navigation";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { createBoardController } from "./boardData";

// boundary() and response() exactly as in src/projectBrowser.test.ts.

it("reads Live, Needs you, the pin catalog and the manifest when it gets a client", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	await Promise.resolve();
	expect(hub.requests.map((request) => request.params.resource).sort()).toEqual([
		"manifest",
		"pin_catalog",
		"section",
		"section",
	]);
	expect(
		hub.requests
			.filter((request) => request.params.resource === "section")
			.map((request) => request.params.section)
			.sort(),
	).toEqual(["live", "needs_you"]);
});
```

Then:
- "answers become the snapshot": Live 2 rows, Needs you 1, pin catalog 1 and the manifest arrive; `loaded` is true, `retained` false, and the rows and manifest sources are exposed.
- "keeps showing its rows while disconnected and replaces them only when the new connection's reads land": load, then `setClient(null)`; the snapshot keeps its 2 Live rows with `retained: true`. Then `setClient(second)`; answer the second client's Live read only. Record every snapshot emitted with `subscribe`: none has an empty Live, and after all four reads land `retained` is false.
- "reads every Needs you page": the first needs_you page has `remaining: 30`, the controller requests `offset: 50`, and a fifth page (past 200 rows) is still read.
- "loads the next Live page on request": `loadMoreLive()` sends `offset: 50`.
- "re-reads the manifest when the hub invalidates it": deliver an `evener/navigation/invalidated` notification to `hub.listeners` with targets `[{ kind: "manifest", revision: 2 }]`; a second manifest request goes out, and two invalidations before it answers cause one request.
- "an approval that resolves leaves Needs you": invalidate `{ kind: "section", section: "needs_you", revision: 2 }` and answer the re-read without the row; the snapshot's `needsYou.rows` no longer has it.
- "a failed first read reports its error and stays unloaded"; "a failed later read keeps the rows".
- "pause holds re-reads, resume catches up"; "dispose leaves no listeners" (`hub.listeners.size` is 0).

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/board/boardData.test.ts`
Expected: FAIL: `Cannot find module './boardData'`.

- [ ] **Step 3: Implement `boardData.ts` to the requirements above**

Keep each reader's retained rows in the controller: store the last non-empty `Page` per reader. Build the snapshot from each reader's live page once it has loaded, otherwise from the retained copy. The manifest reader is a small private class in the same file, about 50 lines: `read()`, invalidation-driven re-read through `singleFlight`, and `dispose()`.

- [ ] **Step 4: Run the tests and watch them pass, then run `make test-native`**

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/board/boardData.ts mobile-native/src/board/boardData.test.ts
git commit -m "feat(native): the Board's data keeps its rows through reconnects"
```

### Task 6: Board rows

**Files:**
- Create: `mobile-native/src/board/BoardRow.tsx`
- Test: `mobile-native/src/board/BoardRow.test.tsx`

**Interfaces:**
- Consumes: `ClassifiedRow`, `whyLine`, `lastLine`, `Usual` and `stateWord` (Task 2); `StateMark` (Task 4); `relativeAge` from `@evener/appwire-client/state/navigation`.
- Produces:

```tsx
export interface BoardRowProps {
	item: ClassifiedRow;
	/** Signal rows (Needs you, unseen Finished, Working in Live) have up to
	 * three lines; quiet rows (Idle, and rows in pinned categories, Projects
	 * and Archived) have one. */
	variant: "signal" | "quiet";
	/** Live's working rows move; every other copy of a session is still. */
	moving: boolean;
	connected: boolean;
	usual: Usual;
	hostLabel: (hostId: string) => string;
	hasDraft: boolean;
	now: number;
	onOpen: (row: NavigationSessionSummary) => void;
}
export function BoardRow(props: BoardRowProps): ReactElement;
```

**Requirements (spec 7.2):**
- The leading 28pt column is `StateMark` (moving only when `moving`; gray meter when `!connected`).
- The title is SF Pro semibold 17/22 in `inkHi`, tail-truncated: two lines for Needs you rows, one otherwise.
- The age is trailing on line 1: `relativeAge(row.updated_at, now)` at 13pt tabular in `inkLow`, hidden when undefined.
- The Draft tag is a small blue "Draft" (`accentInk` text on `accentBg`, 11/13 semibold, 4pt radius) before the age when `hasDraft`.
- The why line (signal rows only) is 15/20, up to two lines for Needs you and one for Working. The state word is semibold in `dangerInk` or `attentionInk` (from `WhyLine.hue`); then " · "; then the reason in `inkHi`. Working's text is in `inkMid` with no word.
- The last line (signal rows only) is 13/18 `inkLow`, one line, never wrapping: a `folder` glyph plus the project, then a `server.rack` glyph plus the host, from `lastLine`. It is omitted when null.
- Quiet rows are 48pt, with the mark, title (one line) and age only.
- Padding is 16pt; signal rows run 64-88pt by content. The whole row is one pressable (44pt+) that calls `onOpen(row)`, with a `pressed` background from `palette.pressed`.
- VoiceOver reads one label: title, state word, reason, age ("Fix Endless Provider Retry Loop, Failed, open the session to see what went wrong, 2 minutes").

- [ ] **Step 1: Write the failing tests** (render with `render` from `../renderNative.testkit`; mock `react-native` and `expo-symbols` as in Task 4). One test per requirement:
  - a Failed signal row shows the title, the "Failed" word in the danger ink, the reason in ink, and "2m";
  - a Needs you title allows two lines and a Working title one;
  - a quiet row has no why line and no last line;
  - the Draft tag appears only with `hasDraft`;
  - the last line shows only the unusual project and host;
  - a working row in Live renders `PulseMeter`, and with `moving: false` a still dot;
  - `!connected` grays the meter;
  - the accessibility label reads in order;
  - pressing calls `onOpen` with the row.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/board/BoardRow.test.tsx`
- [ ] **Step 3: Implement `BoardRow.tsx`.**
- [ ] **Step 4: Run them and watch them pass, then run `make test-native`.**
- [ ] **Step 5: Commit** (`feat(native): Board rows`).

### Task 7: The Board screen

**Files:**
- Create: `mobile-native/src/board/BoardScreen.tsx`, `mobile-native/src/board/BoardToolbar.tsx` and `mobile-native/src/board/connectionStatus.ts`
- Modify:
  - `mobile-native/App.tsx`: route `"Sessions"` renders `BoardScreen`.
  - `mobile-native/src/draftRepository.ts` and `mobile-native/src/draftLibrary.ts`: add `refsWithDrafts(hubId: string): Set<string>`.
- Test: `mobile-native/src/board/connectionStatus.test.ts`, `mobile-native/src/board/BoardScreen.test.tsx` and `mobile-native/src/draftRepository.test.ts`

**Interfaces:**
- Consumes: Tasks 2-6 and `useConnection()` (`client`, `state`, `activeProfile`).
- Produces:
  - `BoardScreen` (a route component for `"Sessions"`).
  - `connectionStatus(state: ConnectionState, fatal: boolean, downSince: number | null, lastLiveAt: number | null, now: number): string | null`, which returns "Update needed" whenever `fatal` is true (a close no retry can fix never says Reconnecting), `null` when live, "Reconnecting…" after 2 seconds down, and "Offline · updated 3m ago" after 30 seconds (the age comes from `relativeAge` on `lastLiveAt`; with no `lastLiveAt`, because the hub was never reached this launch, it says "Offline").
  - `DraftRepository.refsWithDrafts(hubId: string): Set<string>` (`SELECT session_ref FROM drafts WHERE hub_id = ? AND (draft != '' OR unconfirmed IS NOT NULL) UNION SELECT session_ref FROM draft_image_sets WHERE hub_id = ?`: an image-only draft lives only in `draft_image_sets`, since `write` deletes the empty `drafts` row) and `DraftLibrary.refsWithDrafts(hubId)` passing it through (add `"refsWithDrafts"` to the `DraftStorage` `Pick` in `draftLibrary.ts`).

**Requirements (spec 7.1, 7.5, 14; layout from top to bottom):**
1. **Header** (native stack header, no large title):
   - Leading: the hub button, the active hub's name plus `chevron.down`, opening a menu with "Hub settings" (`navigation.navigate("HubSettings", { hubId })`) and "Switch hub" (`navigation.navigate("Hubs")`). Ruling 9.
   - Trailing: Search (`magnifyingglass`), which toggles an inline search field above the list. That field uses today's `RosterSearch` exactly as the old `SessionsScreen` does (`screens.tsx` lines 722-808) until PR 5.
2. **Section chips** (sticky under the header): Live (count of Live rows; amber badge with the Needs you count when above zero), one chip per pinned category (`pin.fill` + name + count), Projects (`manifest.catalogs.projects.count`) and Archived (`manifest.catalogs.archived_projects.count`).
   - Chips for empty sections are hidden, and the chip row fades at its trailing edge.
   - Tapping a chip scrolls to its section.
3. **Live summary line** (`liveSummary`): each count is tappable and jumps to its band, and Idle unfolds. It shows "4 need you" in `attentionInk`, and the fleet `PulseMeter` (gray when not connected) before the working count.
4. **Bands:**
   - "NEEDS YOU · n", "FINISHED · n" and "WORKING · n" (headers 13pt semibold uppercase, `inkMid`, 0.4 letter-spacing), with signal rows.
   - Then "Idle · n ›", folded by default. Its fold state lives in `foldedSections(hubId)` under the key `"idle"`. It holds quiet rows.
   - Empty bands are omitted.
5. **Section rows** after Live, each opening today's screen until PR 3:
   - one "📌 name · count ›" row per pinned category (`navigation.navigate("PinnedSection", { hubId, sectionId, title })`);
   - "Projects · n ›" (`Projects` route with `archived: false`);
   - "Archived · n ›" (`Projects` route with `archived: true`).

   Read the route params in `screens.tsx`'s param list before wiring them.
6. **Bottom toolbar** (`BoardToolbar`):
   - Center: `connectionStatus(...)` in `inkMid`, or nothing when live. The status depends on elapsed time, so while the connection isn't live the toolbar re-renders at the 2-second and 30-second marks and then once a minute (for "updated 3m ago"). The timers are cleared when the connection is live again, so a live Board runs no clock.
   - Trailing: New session (`square.and.pencil` in `accentInk`), opening today's `NewSession` route the way the old header did.
   - Select arrives in PR 4.
7. **States:**
   - First load with nothing retained: three skeleton rows, 64pt, `inset` fill, no shimmer.
   - Loaded and every band empty: "Nothing's running. Start a session to put an agent to work." with a New session button, then the section rows.
   - Disconnected: rows stay, meters gray, the toolbar status shows. No Reconnect control exists anywhere on the screen.
   - A close no retry can fix (`useConnection().fatal`): the toolbar says "Update needed", and a notice row at the top of the Board gives spec 14's sentence, "This app and the hub need compatible versions. Update the app from TestFlight, or update Evener on the hub." It has no action; the app tries again each time it returns to the foreground (Task 1). Test it with `fatal: true`.
8. **Opening a session:**
   - Call `seenMarkers(hubId).markSeen(row)`, then `navigation.navigate("Conversation", { hubId, ref: row.ref, title: row.title })`.
   - The Board re-renders through `seenMarkers(hubId).subscribe` and `getRevision`.
   - `adoptEpoch` runs with Live's rows once `loaded` first turns true.
9. **Focus:** pause the controller when the screen blurs (`useFocusEffect`), and resume when it's focused. The controller lives in a `useState` per hub and gets `setClient(client)` whenever `useConnection().client` changes.
10. **Drafts:** `drafts.refsWithDrafts(hubId)` is read on focus. Rows whose ref is in it show the Draft tag.

- [ ] **Step 1: Write the failing tests.**
  - `BoardToolbar` under fake timers: disconnected with nothing else changing, it shows nothing at 1 second, "Reconnecting…" at 2 seconds and "Offline · updated …" at 30 seconds, and it schedules no timer once live.
  - `connectionStatus.test.ts` is a table: live → null; down 1s → null; down 2s → "Reconnecting…"; down 31s with `lastLiveAt` 3 minutes ago → "Offline · updated 3m ago"; down 31s with no `lastLiveAt` → "Offline"; closed → the same timeline; fatal at any age → "Update needed".
  - `draftRepository.test.ts`: `refsWithDrafts` returns refs with non-empty text, unconfirmed text or only images, for that hub only.
  - `BoardScreen.test.tsx`: mock `react-native`, `expo-symbols`, `@react-navigation/native` (`useFocusEffect`, `useIsFocused`, `useNavigation`) and `../ConnectionProvider` (a `useConnection` returning `screenConnection(...)` from `renderNative.testkit`). Drive the hub through `scriptedClient` from `renderNative.testkit` answering navigation reads with `wireV2`. Cover:
    - the fixture fleet renders bands in order with counts;
    - Idle starts folded;
    - opening a row marks it seen and navigates with `{ hubId, ref, title }`;
    - with the client removed, rows stay and the text "Reconnecting…" appears after 2 seconds (fake timers);
    - no element's text is "Reconnect" or "Refresh";
    - the empty state's copy.

  Read `ConversationScreen.recovery.test.tsx` for how a screen test mocks navigation and the connection.
- [ ] **Step 2: Run them and watch them fail.**
- [ ] **Step 3: Implement.** Map `"Sessions"` to `BoardScreen` in `App.tsx`. Keep `SessionsScreen` in `screens.tsx` for one more task.
- [ ] **Step 4: Run them and watch them pass. Run `make test-native`. Build Release in the simulator and look at the Board against a real hub** (the demo fleet arrives with PR B; until then, a local `evener-hub` with a few sessions).
- [ ] **Step 5: Commit** (`feat(native): the Board replaces the project list as home`).

### Task 8: Remove the old home and describe the Board

**Files:**
- Modify: `mobile-native/src/screens.tsx` (delete `SessionsScreen` and its now-unused helpers)
- Delete: `mobile-native/src/ProjectSessionsList.tsx`
- Modify: `mobile-native/DESIGN.md` (replace "Design System: Evener Native Project Browser" with the Board: its bands, rows, marks and meter, and the calm rules) and `mobile-native/.impeccable/design.json` (replace `ds-project-header` and `ds-search` with the Board's components)
- Modify: `appwire-client/typescript/railSessionState.ts:3` (its comment cites `screens.tsx`; point it at `src/board/attention.ts` if it still describes the phone's use)

- [ ] **Step 1:** Delete the code. Then run `cd mobile-native && npx tsc --noEmit -p tsconfig.check.json` and remove every import it reports as unused or missing, with no other edits.
- [ ] **Step 2:** Run `make test-native` and `make test-web` (the `railSessionState.ts` comment is in the shared package). Expected: PASS.
- [ ] **Step 3:** Update `DESIGN.md` and `design.json` to describe what shipped.
- [ ] **Step 4: Commit** (`refactor(native): remove the project-first home`), then open PR 2: "feat(native): the Board, Live first (phase 2, PR 2)".

---

## PR 3: pinned categories, projects and hosts, test runs, archived

### Task 9: Pinned categories inline

**Files:**
- Create: `mobile-native/src/board/PinnedSections.tsx`
- Modify: `mobile-native/src/board/boardData.ts` (per-category pages) and `mobile-native/src/board/BoardScreen.tsx`
- Test: `mobile-native/src/board/boardData.test.ts` and `mobile-native/src/board/PinnedSections.test.tsx`

**Interfaces:**
- Produces: `BoardSnapshot.pinSections: Record<string, Page<NavigationSessionSummary>>`, one `NavigationPages` per category (`{ resource: "pin_section", sectionId }`, field `"sessions"`, limit 50), created for each category in the catalog and dropped when a category leaves it.

**Requirements (spec 7.1):**
- Each category is its own section, in the hub's order. The header shows `pin.fill`, the name, the count, a fold toggle (`foldedSections` key `pin:${id}`, unfolded by default), and ⋯ with Rename and Delete.
  - Rename and Delete use `NavigationActions.renamePinSection` and `deletePinSection` through the existing `usePinNavigation` flow, or open `PinSectionEditor` with its existing params.
  - Delete confirms: "Delete "<name>"? Its sessions stay; they're only unpinned."
- Rows are quiet one-line `BoardRow`s with a still mark. A live session also stays in Live.
- An empty category says "Touch and hold a session and choose Pin to category."
- The pinned-category row links from PR 2 are removed.

- [ ] Steps follow Task 5's pattern: failing tests (pages per category, and fold state persists), implement, `make test-native`, then commit (`feat(native): pinned categories live on the Board`).

### Task 10: Projects and hosts, test runs, archived

**Files:**
- Create: `mobile-native/src/board/ProjectsSection.tsx`
- Modify: `mobile-native/src/board/BoardScreen.tsx` and `mobile-native/src/projectBrowser.ts` (reuse it, adding a catalog parameter so it can read `projects`, `test_runs` and `archived_projects`)
- Test: `mobile-native/src/projectBrowser.test.ts` and `mobile-native/src/board/ProjectsSection.test.tsx`

**Requirements (spec 7.1):**
- **Projects.**
  - Projects come from `createProjectBrowserController(client, "projects")`. Keep today's default (`"projects"`) so existing callers and tests don't change.
  - Pinned projects (`favorite`) float to the top with `pin.fill`. Each project row shows its name and live count (`rollup_live`).
  - Inside an unfolded project, sessions split into today (`current`), recent, and a folded archived group, as quiet rows.
- **"Organize by."**
  - It appears only when the manifest has more than one source. The toggle in the section header flips between "Project, then host" (default) and "Host, then project", and the section title between "Projects" and "Hosts".
  - Its choice persists in `foldedSections` under the key `organize-by-host`.
  - Hosts group projects by `NavigationProjectSummary.sources`, labelled with the manifest's source label. A host whose source is offline shows an amber "Offline" and nothing when connected.
- **Test runs** (folded by default) reads the `test_runs` catalog; **Archived** (folded by default) reads `archived_projects`, with each project's archived tier inside.
- Rows in Projects, Test runs and Archived are quiet `BoardRow`s. A session on an offline host is `ended`, so it shows as shut down.
- The Projects and Archived link rows from PR 2 are removed.

- [ ] Steps: failing tests (the catalog parameter reads each catalog; the host grouping and offline label; fold defaults), implement, `make test-native`, then commit (`feat(native): projects, hosts, test runs and archive on the Board`). Open PR 3: "feat(native): the Board's own sections (phase 2, PR 3)".

---

## PR 5: notices and search

### Task 14: Notices

**Files:**
- Create: `mobile-native/src/board/notices.ts` (pure) and `mobile-native/src/board/Notices.tsx`
- Modify: `mobile-native/src/board/boardData.ts` (auth and plugin reads) and `mobile-native/src/board/BoardScreen.tsx`
- Test: `mobile-native/src/board/notices.test.ts`, `mobile-native/src/board/boardData.test.ts` and `mobile-native/src/board/Notices.test.tsx`

**Interfaces:**
- Produces: `type Notice = { kind: "signIn" | "host" | "plugin"; key: string; text: string; action: "Sign in" | "Details" | "Plugins" }` and `notices(input: { auth: AuthStatusResponse[]; sources: Source[]; plugins: PluginEntry[]; liveRows: readonly NavigationSessionSummary[]; projectRows: readonly NavigationSessionSummary[] }): Notice[]`.

**Requirements (spec 7.1, ruling 8):**
- **Sign-in:** one notice per provider with `needsLogin`: "<provider> sign-in expired". Its action "Sign in" opens today's provider sign-in flow for that provider.
- **Host:** one notice per offline source: "<label> is offline · 3 sessions", where the count is the number of loaded rows whose `host_id` is that source. The action "Details" opens `HubSettings` until phase 5.
- **Plugin:** one notice per broken plugin: "<plugin> is broken". Its action "Plugins" opens today's `Plugins` screen. That route takes only `{ hubId }`, so opening at that plugin's row (spec 7.1) waits for phase 5's Hub; the notice already names the plugin.
- **Placement and style:** notices sit under the chips as rows: `exclamationmark.triangle.fill` in amber, the sentence in `inkHi`, the action in `accentInk`. No tinted box. They disappear when resolved.
- **Reads:**
  - `evener/auth/list` on focus and on `evener/auth/updated`.
  - `evener/plugin/list` on focus and every 5 minutes while the Board is focused.
  - The sources come from the manifest.

- [ ] Steps: failing tests (the pure `notices` table, the reads and polling with fake timers, and rendering), implement, `make test-native`, then commit (`feat(native): Board notices`).

### Task 15: Search

**Files:**
- Create: `mobile-native/src/board/boardSearch.ts` (the controller and recent searches) and `mobile-native/src/board/SearchResults.tsx`
- Modify: `mobile-native/src/board/BoardScreen.tsx` (replace the `RosterSearch` field from Task 7) and `mobile-native/src/board/boardMemory.ts` (`forgetBoard` removes `evener.native.recent-searches.${hubId}` too)
- Test: `mobile-native/src/board/boardSearch.test.ts`, `mobile-native/src/board/SearchResults.test.tsx` and `mobile-native/src/board/boardMemory.test.ts` (the forget test covers all three keys)

**Requirements (spec 7.4, ruling 7):**
- **Showing search:**
  - Search shows from the header's Search button or by pulling the list down. Spike `headerSearchBarOptions` on the native stack first: with `hideWhenScrolling`, iOS reveals it by pulling down, which is exactly the spec.
  - If the spike fails, use a search field in the list header, hidden at an initial content offset.
  - Record the result in the PR description.
- **Scopes:** chips for All and Live. Archived waits for S14.
- **Results:**
  - `evener/search { query }` is debounced 250ms. A newer query wins, and a query change clears stale results.
  - **Sessions** lists `live` and then `past` results, each with a state mark from `boardState` over `{ state }` and the age.
  - **Projects** lists the loaded projects catalog filtered by name or `working_dir`, case-insensitive.
  - Tapping a session opens it; tapping a project scrolls to it in the Projects section and unfolds it.
- **Recent searches** show when the field is empty: the last 8 queries submitted with a tap on a result, kept in kv-store under `evener.native.recent-searches.${hubId}` and cleared by `forgetBoardForHub`, with a "Clear" action.
- **Retire today's search:** `RosterSearch` and `rosterSearch.ts` are deleted if nothing else uses them. Check first with `grep -rn "rosterSearch\|RosterSearch" mobile-native/src`.

- [ ] Steps: failing tests (debounce and newest wins, scopes, grouping, recent searches bounded and per hub), implement, `make test-native`, then commit (`feat(native): Board search`). Open PR 5: "feat(native): Board notices and search (phase 2, PR 5)".

---

## PR 4: row actions, select mode, list stability

### Task 11: Gesture and animation foundations

**Files:**
- Modify: `mobile-native/package.json`, `package-lock.json`, `babel.config.js` (the worklets plugin, if Expo 57's preset doesn't add it), `App.tsx` (`GestureHandlerRootView` at the root) and `Podfile.lock` (regenerated as in Task 4, Step 6)
- Modify: `mobile-native/src/renderNative.testkit.tsx` only if screen tests need a gesture-handler mock

- [ ] **Step 1:** Run `npx expo install react-native-gesture-handler react-native-reanimated react-native-worklets` and let Expo pick SDK 57's versions. Read the installed `react-native-reanimated` README's Expo section for the Babel plugin requirement, and follow it.
- [ ] **Step 2:** Wrap the app root in `GestureHandlerRootView style={{ flex: 1 }}`.
- [ ] **Step 3:** Regenerate the lock (Task 4, Step 6's commands). Expected: the diff adds the three pods only.
- [ ] **Step 4:** Run `make test-native`. The ConversationScreen tests import all of `screens.tsx`; if a new native import breaks them, mock it in that test's `vi.mock` list, as the test already does for other native modules.
- [ ] **Step 5:** Build Release in the simulator and launch. Commit (`build(native): gesture handler and reanimated`).

### Task 12: Swipes and the long-press menu

**Files:**
- Create: `mobile-native/src/board/SwipeRow.tsx`, `mobile-native/src/board/rowActions.ts` and `mobile-native/src/board/RowMenu.tsx`
- Modify: `mobile-native/src/board/BoardRow.tsx` and `mobile-native/src/board/BoardScreen.tsx`
- Test: `mobile-native/src/board/rowActions.test.ts` and `mobile-native/src/board/RowMenu.test.tsx`

**Interfaces:**
- Produces `rowActions.ts`. Each function is at the request boundary, takes the client, and returns the hub's result:
  - `archiveSession(actions: NavigationActions, row, archived: boolean)`, using `evener/archive/set` through `NavigationActions.archive` so the recovery journal covers it;
  - `stopSession(client, row)`, calling `turn/interrupt`. Read `TurnInterruptParams` in `appwire/types.go`. If it needs the instance id, read it with `thread/read { ref }` first, and say so in a comment;
  - `shutDownSession(client, row)` (`thread/shutdown`);
  - `renameSession(client, row, name)` (`evener/thread/name/set`);
  - `pinSession(actions, row, target)` (`NavigationActions.assignPin`).

**Requirements (spec 7.3):**
- **Swipe right** (leading) is Archive, in blue-gray (`inkMid` fill, white label). A full swipe archives, and the toast "Archived · Undo" shows for 8 seconds; Undo unarchives.
- **Swipes that begin within 24pt of the screen's left edge never act on a row.** Use `ReanimatedSwipeable` with a `hitSlop` of `{ left: -24 }`, or check the gesture's start x. Test the start-x rule as a pure function.
- **Swipe left** (trailing) shows Stop (only when the row is working), Pin and More (which opens the long-press menu).
- **Long-press:**
  - Spike `@expo/ui`'s SwiftUI `ContextMenu` with a preview first, then `@react-native-menu/menu`, and if neither gives a preview card, a sheet that shows the preview card on top with the actions below.
  - The preview card holds the title, state, why line, project and host (the task progress, subagent failures, model and excerpt come with S13, S3 and S1).
  - Tapping the card opens the session.
  - The actions are: Pin to category… (the categories plus "New category…"), Mark as read or Mark as unread (`seenMarkers`), Stop (when working), Shut down (destructive, confirmed), Archive, and Rename. Copy link waits for a session deep link, which the app doesn't have; say so in the PR.
  - Record the spike's outcome in the PR description.
- Every action shows its state where it was taken (spec 14's outbox): an archived row dims until the hub confirms.

- [ ] Steps: failing tests (each `rowActions` function sends the right request; the edge-zone rule; the menu's actions per state), implement, `make test-native`, look in the simulator, then commit (`feat(native): swipe and long-press actions on Board rows`).

### Task 13: Select mode and list stability

**Files:**
- Create: `mobile-native/src/board/listStability.ts` (pure) and `mobile-native/src/board/SelectBar.tsx`
- Modify: `mobile-native/src/board/BoardScreen.tsx` and `mobile-native/src/board/BoardToolbar.tsx`
- Test: `mobile-native/src/board/listStability.test.ts`

**Requirements (spec 7.1, 7.3):**
- **Select** leads the toolbar. It puts rows into multi-select, with a checkbox in the mark column and a bottom bar: Archive, Pin, and Mark as read. Done leaves.
- **The list never reorders while a finger is on it or it is scrolling.**
  - Hold the band ordering: `listStability.ts` exports `class HeldOrder` with `hold()`, `release()` and `order(next: LiveBands): LiveBands`. While held, `order` returns the previous order with row contents updated, and rows that left are removed.
  - Changes apply when the list settles (`onScrollEndDrag`, `onMomentumScrollEnd`, touch end).
  - Rows move with a 250ms spring (Reanimated layout transitions).
  - A row entering Needs you gets a brief amber wash: `attentionBg` fading out over 1.2s.
  - With Reduce Motion on, rows change places without animation and the wash is skipped.

- [ ] Steps: failing tests (`HeldOrder` keeps order while held, updates contents, drops departed rows and applies on release), implement, `make test-native`, look in the simulator, then commit (`feat(native): select mode and a Board that holds still under your finger`). Open PR 4: "feat(native): Board row actions and select mode (phase 2, PR 4)".

---

## PR B: the demo fleet

### Task 16: The demo hub serves the Board

The demo hub (`mobile-native/scripts/demo-hub.mts`) answers no navigation reads, so the Board can't be seen or screenshotted without a real fleet. Spec Appendix B names the prototype's `docs/design/mobile/redesign/prototype/data.js` as the canonical fixture.

**Files:**
- Create: `mobile-native/scripts/demoFleet.mts` (the fixture as navigation rows)
- Modify: `mobile-native/scripts/demo-hub.mts` (route the new methods)
- Test: `mobile-native/scripts/demoFleet.test.ts`, run by `npm run check:scripts` or `vitest` as the other scripts' tests are (read `package.json`'s scripts and follow them)

**Requirements:**
1. `demoFleet.mts` turns `data.js`'s sessions into `NavigationSessionSummary` rows:
   - state from the prototype's session state (failed → `errored`, question → `awaiting` + `ask_pending`, approval → `active`, and so on);
   - `updated_at` relative to startup;
   - `host_id` from its host (magic-kingdom local, paradise-park remote);
   - project, children and running jobs.

   It also builds projects, pin categories, test runs and archived projects, plus a manifest with two sources and paradise-park online.
2. With `EVENER_DEMO_FLEET=1`, the demo hub answers:
   - `evener/navigation/read` for `manifest`, `section` (`live`, `needs_you`: approval rows are the `active` rows the prototype marks as approvals), `pin_catalog`, `pin_section`, `catalog` (`projects`, `archived_projects`, `test_runs`), `project` and `project_page`. Every response uses `wireV2` from `@evener/appwire-client/testing/navigation` with offsets, limits and `remaining`.
   - `evener/search`, `evener/auth/list` (one provider with `needsLogin` so the sign-in notice shows) and `evener/plugin/list`.

   Without the variable, it behaves exactly as today.
3. `EVENER_DEMO_FLEET_OFFLINE_HOST=1` marks paradise-park offline, for the offline frames.
4. The tests decode every served resource with the package codec (`decodeNavigationResponse`), so the fixture can't drift from the wire. They also check that the Live section holds the 17 live top-level sessions Appendix B describes.

- [ ] Steps: failing tests, implement, run the demo hub (`EVENER_DEMO_FLEET=1 npx tsx scripts/demo-hub.mts`), point a Release simulator build at port 9196 and see the Board, then commit (`feat(native): the demo hub serves the redesign's fleet`). Open PR B: "feat(native): a demo fleet for the Board (phase 2, PR B)".

### Task 17: Screenshots for the phase's last PR

With the demo fleet, capture Release-simulator screenshots (iPhone 17 Pro) of Appendix A frames 1-7 in light, plus frame 1 in dark and at the largest standard Dynamic Type size. Save them under `docs/design/mobile/assets/2026-09-2x-redesign-phase2-board-*.png`, named by frame, and attach them to the phase's last PR, with the demo hub's command in the description. The frames are:

1. Board with the fleet;
2. Board with nothing live;
3. offline and reconnecting;
4. search;
5. row actions;
6. the long-press menu;
7. select mode.

---

## Self-review against the spec

- **7.1:**
  - header: Task 7;
  - chips: Task 7;
  - notices: Task 14;
  - Continue reading: ruling 5 (phase 4);
  - summary line: Tasks 2 and 7;
  - Live bands: Tasks 2 and 7;
  - pinned categories: Task 9;
  - Projects and Hosts: Task 10;
  - Test runs and Archived: Task 10;
  - toolbar: Tasks 7 and 13;
  - fold persistence: Tasks 3, 7, 9 and 10.
- **7.2:** row anatomy is Task 6. Attachments and the excerpt wait for S1; task progress for S13; subagent failures for S3; the model for ruling 6.
- **7.3:** tap is Task 7; swipes and long-press are Task 12; pull-down search is Task 15; stability and motion are Task 13.
- **7.4:** search is Task 15 (ruling 7).
- **7.5:** Board states are Task 7.
- **13.1-13.2:** Task 2 (Quiet and May be stuck: ruling 3).
- **14:** connection is Tasks 1 and 7; the outbox presentation for row actions is Task 12; drafts are Task 7.
- **16.4:** Task 4. **16.5:** Tasks 4, 6 and 7.
