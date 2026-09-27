# iPhone redesign, Phase 6: Attention and resilience (Implementation Plan), part 2

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

This is part 2 of `docs/superpowers/plans/2026-09-26-iphone-redesign-phase6-attention-resilience.md` (part 1: PRs A, E and F, Tasks 1-2 and 12-14; part 3: PR I, Task 15). Part 1's Goal, Architecture, Tech Stack, Spec, Global Constraints, Rulings, Questions for Jesse, "Built on earlier phases" table and Review Focus bind every task here, and its task numbers continue here. PR B starts when the phase starts; PR G starts once PR D here and part 1's PR F have landed; PR H starts once phase 2 part 3 is on main.

## What part 1's review and Jesse's answers changed here

Part 1's review (#2511) left three findings against the alert center, and Jesse's answers of 2026-09-26 (`.superpowers/research/2026-09-26-jesse-answers.md`) changed two things. All are settled in the tasks below:

1. **The Hold switch turned off and on within 200ms** (RoboRev, round 5): `release()` returns while anything holds, so a release scheduled when the switch went off does nothing once it is back on. Task 3's test "keeps holding when the Hold switch goes off and back on within the release delay".
2. **`reset()` kept the screen** (RoboRev, round 5): it now forgets the screen, the provider reports the routes again, and a session screen counts only for its own hub (`alertScreenFor(routes, hubId)`). Tasks 3 and 8.
3. **A sheet held only when the Hold switch was on:** holds now have a kind. `"quiet"` (the Reader, typing) follows the switch; `"covered"` (a sheet or modal on top) always holds, since a banner can't show above one. Task 3's test "holds under a sheet whatever the Hold switch says, since nothing shows above one"; Tasks 8 and 10 pass the kinds.
4. **Sheets are native `formSheet` routes** (Jesse's answer 11), built on phase 2 part 3's sheet foundation (`src/sheet/sheetRoutes.ts`: `sheetOptions()`, `SHEET_ROUTES`, `isSheetRoute`, its Task 18.1). Hub and New session stay `"modal"` routes; only full-screen viewers and the Hub's inner detail sheets stay React Native `Modal`s. Task 8's `coversBanners` reads `isSheetRoute` plus the two modals, and the screen under a sheet stays in front, as `inFront` has it; Task 10's `HoldingModal` covers the `Modal`s that remain. No sheet of this part's own is a `Modal`: the Alerts page is a page inside the Hub sheet.
5. **Board rows show no subagent failures** (Jesse's answer 12). A banner's second line is phase 2's `whyLine`, which never names them, and the detectors alert on the coordinator's own state only.

Main also moved: Task 5's store takes the app's `SyncStringStorage` (`src/syncStringStorage.ts`, #2536).

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
  - `type BannerTarget = { kind: "session"; ref: string; title: string } | { kind: "needsYou" } | { kind: "notice"; key: string }`
  - `type Haptic = "warning" | "light"` and `type HoldKind = "quiet" | "covered"`: "quiet" is the Reader or typing, which the Hold switch governs; "covered" is a sheet or modal on top, which always holds
  - `interface AlertTimer { now(): number; setTimeout(callback: () => void, ms: number): unknown; clearTimeout(handle: unknown): void }`
  - `BANNER_MS`, `COALESCE_MS`, `RELEASE_MS`, `needsYou(alert): boolean`
  - `class AlertCenter`: constructor `(timer: AlertTimer, haptic?: (kind: Haptic) => void)`; `getSnapshot()`, `subscribe(listener)`, `offer(alert)`, `setScreen(screen)`, `setPreferences(preferences)`, `hold(kind: HoldKind): () => void`, `touch(down: boolean)`, `dismiss()`, `tap(): BannerTarget | null`, `nextUsed()`, `reset()`

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
		expect(alerts.tap()).toEqual({ kind: "session", ref: "a", title: "Session a" });
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
		const release = alerts.hold("quiet");
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
		// One of them failed, so the combined banner buzzes a warning.
		expect(haptics).toEqual(["warning"]);
	});

	it("joins what it held to a banner still up, so nothing on it drops out", () => {
		const { alerts, haptics } = center();
		alerts.offer(session("a"));
		const first = alerts.getSnapshot().banner?.id;
		const release = alerts.hold("quiet");
		vi.advanceTimersByTime(COALESCE_MS);
		alerts.offer(session("b"));
		alerts.offer(session("c"));
		release();
		vi.advanceTimersByTime(RELEASE_MS);
		expect(shown(alerts)).toEqual(["a", "b", "c"]);
		expect(alerts.getSnapshot().banner?.id).toBe(first);
		expect(haptics).toEqual(["light"]);
	});

	it("keeps a held notice back while a banner about a session is still up", () => {
		const { alerts } = center();
		alerts.offer(session("a"));
		const release = alerts.hold("quiet");
		alerts.offer(hostOffline);
		release();
		vi.advanceTimersByTime(RELEASE_MS);
		expect(shown(alerts)).toEqual(["a"]);
	});

	it("shows one held session as its own banner, and a held notice only when no session waits", () => {
		const { alerts } = center();
		let release = alerts.hold("quiet");
		alerts.offer(hostOffline);
		alerts.offer(session("a", "failed"));
		release();
		vi.advanceTimersByTime(RELEASE_MS);
		expect(shown(alerts)).toEqual(["a"]);
		alerts.dismiss();
		release = alerts.hold("quiet");
		alerts.offer(hostOffline);
		release();
		vi.advanceTimersByTime(RELEASE_MS);
		expect(shown(alerts)).toEqual(["host:paradise-park"]);
	});

	it("waits for every hold to end, and a quick return holds again", () => {
		const { alerts } = center();
		const reading = alerts.hold("quiet");
		const typing = alerts.hold("quiet");
		alerts.offer(session("a"));
		reading();
		vi.advanceTimersByTime(RELEASE_MS);
		expect(alerts.getSnapshot().banner).toBeNull();
		typing();
		vi.advanceTimersByTime(RELEASE_MS - 1);
		const back = alerts.hold("quiet");
		vi.advanceTimersByTime(RELEASE_MS);
		expect(alerts.getSnapshot()).toMatchObject({ banner: null, held: 1 });
		back();
		vi.advanceTimersByTime(RELEASE_MS);
		expect(shown(alerts)).toEqual(["a"]);
	});

	it("drops a finished result instead of holding it", () => {
		const { alerts } = center();
		alerts.setPreferences({ ...DEFAULT_ALERT_PREFERENCES, finished: true });
		const release = alerts.hold("quiet");
		alerts.offer(session("a", "finished"));
		expect(alerts.getSnapshot().held).toBe(0);
		release();
		vi.advanceTimersByTime(RELEASE_MS);
		expect(alerts.getSnapshot().banner).toBeNull();
	});

	it("lets go of held alerts about the session you land on", () => {
		const { alerts } = center();
		const release = alerts.hold("quiet");
		alerts.offer(session("a"));
		alerts.offer(session("b"));
		release();
		alerts.setScreen({ kind: "session", ref: "a" });
		vi.advanceTimersByTime(RELEASE_MS);
		expect(shown(alerts)).toEqual(["b"]);
	});

	it("shows what it held at once when holding is turned off", () => {
		const { alerts } = center();
		alerts.hold("quiet");
		alerts.offer(session("a"));
		alerts.setPreferences({ ...DEFAULT_ALERT_PREFERENCES, hold: false });
		vi.advanceTimersByTime(RELEASE_MS);
		expect(shown(alerts)).toEqual(["a"]);
		alerts.offer(session("b"));
		expect(shown(alerts)).toEqual(["a", "b"]);
	});
});

describe("what holds, and what the Hold switch governs (ruling 7)", () => {
	it("keeps holding when the Hold switch goes off and back on within the release delay", () => {
		const { alerts } = center();
		alerts.hold("quiet");
		alerts.offer(session("a"));
		alerts.setPreferences({ ...DEFAULT_ALERT_PREFERENCES, hold: false });
		vi.advanceTimersByTime(RELEASE_MS - 1);
		alerts.setPreferences(DEFAULT_ALERT_PREFERENCES);
		vi.advanceTimersByTime(RELEASE_MS);
		expect(alerts.getSnapshot()).toMatchObject({ banner: null, held: 1 });
	});

	it("holds under a sheet whatever the Hold switch says, since nothing shows above one", () => {
		const { alerts } = center();
		alerts.setPreferences({ ...DEFAULT_ALERT_PREFERENCES, hold: false });
		const closeSheet = alerts.hold("covered");
		alerts.offer(session("a"));
		expect(alerts.getSnapshot()).toMatchObject({ banner: null, held: 1 });
		const stopReading = alerts.hold("quiet");
		closeSheet();
		vi.advanceTimersByTime(RELEASE_MS);
		// With Hold off, reading holds nothing: the banner shows once the sheet goes.
		expect(shown(alerts)).toEqual(["a"]);
		stopReading();
	});

	it("forgets the screen with the hub, so the new hub's same ref alerts", () => {
		const { alerts } = center();
		alerts.setScreen({ kind: "session", ref: "a" });
		alerts.reset();
		alerts.offer(session("a"));
		expect(shown(alerts)).toEqual(["a"]);
	});
});

describe("the order Next serves first (spec 8.3)", () => {
	it("puts whatever alerted most recently first, shown or held", () => {
		const { alerts } = center();
		alerts.offer(session("a"));
		alerts.hold("quiet");
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
		const release = alerts.hold("quiet");
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
	const release = alerts.hold("quiet");
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
	| { kind: "session"; ref: string; title: string }
	| { kind: "needsYou" }
	| { kind: "notice"; key: string };

export type Haptic = "warning" | "light";

/** Why banners wait (ruling 7). "quiet" is the Reader or typing, which Hub >
 * In-app alerts' Hold switch governs; "covered" is a sheet or modal on top,
 * which always holds, since nothing shows above one. */
export type HoldKind = "quiet" | "covered";

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
	private holds = new Map<symbol, HoldKind>();
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

	/** Holds banners until the returned function runs: the Reader and typing
	 * each hold a "quiet" one, and a sheet a "covered" one (ruling 7). */
	hold(kind: HoldKind): () => void {
		const token = Symbol("hold");
		this.holds.set(token, kind);
		this.cancelRelease();
		return () => {
			if (this.holds.delete(token) && !this.holding()) this.scheduleRelease();
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
		return only.kind === "notice"
			? { kind: "notice", key: only.key }
			: { kind: "session", ref: only.ref, title: only.title };
	}

	/** Next took you on: it serves the held sessions itself now, so they
	 * don't drop in later (the prototype's goNext). */
	nextUsed(): void {
		if (this.held.length === 0) return;
		this.held = [];
		this.publish();
	}

	/** Another hub, or none: nothing carries over, not even what is on screen,
	 * whose ref named the old hub's session. The provider reports the screen
	 * again after a reset. */
	reset(): void {
		this.stopBanner();
		this.cancelRelease();
		this.held = [];
		this.recent = [];
		this.screen = { kind: "other" };
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
		for (const kind of this.holds.values()) if (kind === "covered" || this.preferences.hold) return true;
		return false;
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
		// A hold that began after the release was scheduled keeps it all back.
		if (this.holding()) return;
		const waiting = this.held.filter((alert) => this.wanted(alert));
		this.held = [];
		const current = this.banner;
		const showing = current?.alerts.every(needsYou) ? current.alerts : [];
		const sessions = waiting.filter(needsYou);
		if (sessions.length === 0) {
			// A held notice shows only when no session waits, a banner still up
			// included, and then only the latest; the Board lists every notice
			// either way (the prototype's releaseHeld).
			const notice = [...waiting].reverse().find((alert) => alert.kind === "notice");
			if (notice === undefined || showing.length > 0) this.publish();
			else this.show(notice);
			return;
		}
		// Held banners show when you leave, combined (spec 13.3). A banner about
		// sessions that need you that is still up takes them in, as a burst
		// does, so nothing on it drops out.
		const alerts = [
			...showing.filter((alert) => !sessions.some((held) => subject(held) === subject(alert))),
			...sessions,
		];
		const [only] = alerts;
		if (current !== null && showing.length > 0) {
			this.banner = { id: current.id, alerts };
		} else if (alerts.length === 1 && only !== undefined) {
			this.show(only);
			return;
		} else {
			this.stopBanner();
			this.banner = { id: this.nextId++, alerts };
			this.buzz(alerts.some((alert) => alert.kind === "failed") ? "warning" : "light");
		}
		this.shownAt = this.timer.now();
		this.armExpiry();
		this.publish();
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

	it("alerts once for an approval the hub promotes into Needs you, though it is in both lists", () => {
		const start = detectSessionAlerts(null, bands([row("c", { state: "active" })]), none).states;
		const both = bands([row("c", { state: "active" })], [row("c", { state: "active" })]);
		const later = detectSessionAlerts(start, both, none);
		expect(later.alerts.map((alert) => alert.kind)).toEqual(["approval"]);
		expect(detectSessionAlerts(later.states, both, none).alerts).toEqual([]);
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

const signIn: Notice = {
	kind: "signIn",
	key: "signIn:codex",
	text: "codex-jesse-fsck.com sign-in expired",
	action: "Sign in",
	providerId: "codex",
};
const hostDown: Notice = {
	kind: "host",
	key: "host:paradise-park",
	text: "paradise-park is offline · 3 sessions",
	action: "Details",
	sourceId: "paradise-park",
};
const brokenPlugin: Notice = { kind: "plugin", key: "plugin:go", text: "go is broken", action: "Plugins", pluginId: "go" };

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
	// liveBands holds each session once, from Live or the Needs you section,
	// and leaves offline rows out (boardState calls them shutDown), so each
	// ref is diffed once and an offline ref keeps the state seeded above.
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
  - `ALERT_PREFERENCES_KEY = "evener.native.alert-preferences"`
  - `class AlertPreferenceStore`: constructor `(storage: SyncStringStorage)`, over `src/syncStringStorage.ts`'s type; `getSnapshot(): AlertPreferences`, `subscribe(listener)`, `set(change: Partial<AlertPreferences>)`
  - `alertPreferences(): AlertPreferenceStore` from `nativeAlertPreferences.ts`, one store per process over `expo-sqlite/kv-store`

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/alerts/alertPreferences.test.ts
import { describe, expect, it } from "vitest";
import { DEFAULT_ALERT_PREFERENCES } from "./alertCenter";
import type { SyncStringStorage } from "../syncStringStorage";
import { ALERT_PREFERENCES_KEY, AlertPreferenceStore } from "./alertPreferences";

function memory(values = new Map<string, string>()): SyncStringStorage & { values: Map<string, string> } {
	return {
		values,
		getItemSync: (key) => values.get(key) ?? null,
		setItemSync: (key, value) => void values.set(key, value),
		removeItemSync: (key) => void values.delete(key),
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
		const broken: SyncStringStorage = {
			getItemSync: () => {
				throw new Error("disk");
			},
			setItemSync: () => {
				throw new Error("disk");
			},
			removeItemSync: () => {
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
import type { SyncStringStorage } from "../syncStringStorage";
import { type AlertPreferences, DEFAULT_ALERT_PREFERENCES } from "./alertCenter";

export const ALERT_PREFERENCES_KEY = "evener.native.alert-preferences";

function read(storage: SyncStringStorage): AlertPreferences {
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

	constructor(private readonly storage: SyncStringStorage) {
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
- `light`: Send (phase 3's `Composer` `onSend`, after the send is admitted), including part 1's offline admission (its Task 14).
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
		removeItemSync: (key) => void values.delete(key),
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
- Consumes: `AlertCenter` and its types (Task 3); `AlertFeed` (Task 4); `alertPreferences()` (Task 5); `haptic` (Task 6); `AlertBanner` and `requestBoardJump` (Task 7); phase 2's `createBoardController`, `liveBands`, `seenMarkers`, `notices` and `Notice`, and `openNotice` (extracted below); `isSheetRoute` from `src/sheet/sheetRoutes.ts` (phase 2 part 3's Task 18.1, the sheet foundation); `getDefaultHeaderHeight` from `@react-navigation/elements` (2.9.40: `(layout, modalPresentation, topInset) => number`); `createNavigationContainerRef` and `StackActions` from `@react-navigation/native`.
- Produces:
  - From `alertScreen.ts`: `coversBanners(route: { name: string; params?: object } | undefined): boolean` (a sheet route, or phase 5's `"Hub"` and `"NewSession"` modals) and `alertScreenFor(routes: readonly { name: string; params?: object }[], hubId: string | null): AlertScreen`.
  - From `AlertsProvider.tsx`: `<AlertsProvider>`, `useAlertCenter(): AlertCenter`, `useAlertSnapshot(): AlertSnapshot`, `useHoldAlerts(active: boolean, kind: HoldKind): void`, `useHeldAlertCount(): number`, `useReportRoutes(): (routes: readonly { name: string; params?: object }[]) => void` and `useNoticeFor(): (key: string) => Notice | undefined` (the notice the provider last read under that key).
  - `<AlertBannerHost navigation={NavigationContainerRef<Routes>} />`

**Requirements (spec 13.3; rulings 1-3, 5, 7):**
1. **One center, one feed.** `AlertsProvider` (inside `ConnectionProvider`) owns one `AlertCenter` on the real clock, whose haptic callback is `haptic(kind)`, and one `AlertFeed` over it. It follows `alertPreferences()` with `center.setPreferences` on mount and on every change.
2. **Its own reads.** It creates one `createBoardController()` per hub and calls `setClient(client)` whenever `useConnection().client` changes, as the Board does. It never pauses it: the Board and the Session pause theirs on blur (phase 3 ruling 33), and alerts must hear about sessions while you're anywhere. This costs a second set of navigation reads while the Board or a Session is in front; say so in the PR description.
3. **Feeding the center.**
   - Sessions: on each controller snapshot that is `loaded`, not `retained`, with the Needs you section complete (`needsYou.remaining === 0`, so the first observation is never a partial baseline whose later pages alert old news) and Live's first page loaded, call `feed.observeSessions(liveBands(live.rows, needsYou.rows, (row) => seen.isSeen(row)), offlineRefs)`, with `const seen = seenMarkers(hubId)`, where `offlineRefs` holds the refs of rows in either list with `offline: true`. `isSeen` is a `SeenMarkers` method that reads `this` (phase 2's `boardMemory.ts`), so it goes in a closure, never as `seenMarkers(hubId).isSeen`.
   - Notices: this controller reads auth as the Board's does (phase 2 Task 14), and never plugins, since a plugin notice never alerts (ruling 5). If phase 2's controller polls `evener/plugin/list` whenever it isn't paused, give `createBoardController` an option that turns those reads off, and use it here. Once the auth read has landed, call `feed.observeNotices(notices({ auth, sources, plugins: [], loadedRows }))` with the manifest's sources and the rows this controller has loaded (Live's first page and Needs you). Its host counts can be lower than the Board's, which loads more pages; phase 2 already calls that count a floor.
4. **Baselines.** A new client (a different non-null object: the app came back to the foreground, or a closed connection was replaced) calls `feed.rebaseline()`. Another hub calls `center.reset()` and `feed.rebaseline()`, and replaces the controller.
5. **What's on screen.** `App.tsx` gives `NavigationContainer` a ref from `createNavigationContainerRef<Routes>()` and passes the root state's routes up to the focused one (`routes.slice(0, index + 1)`) to `useReportRoutes()`'s function from `onReady` and from `onStateChange`. That function calls `center.setScreen(alertScreenFor(routes, hubId))` with the active hub's id, and holds one `"covered"` hold while `coversBanners` is true of the top route (ruling 7). After `center.reset()` it reports the current routes again, since the reset forgets the screen.
6. **The host.** `AlertBannerHost` renders inside `NavigationContainer`, after the navigator, as an absolutely positioned container with `pointerEvents="box-none"`, `left` and `right` 0, and `top = getDefaultHeaderHeight(frame, false, insets.top) + 4` from `useSafeAreaFrame()` and `useSafeAreaInsets()`: just below the nav bar, never over it (spec 13.3; round 1 problem 3). It renders `AlertBanner` keyed by the banner's `id`, or nothing.
7. **A tap.** `center.tap()`, then:
   - a session: `navigation.dispatch(StackActions.push("Conversation", { hubId, ref, title }))`, with the target's `ref` and `title` and the active hub's id from `useConnection()` (the center is reset for every hub), so Back returns to where you were (spec 6);
   - a coalesced banner: `navigation.dispatch(StackActions.popTo("Sessions"))`, then `requestBoardJump("needsYou")`;
   - a notice: the Board's own `openNotice(navigation, notice)` for `useNoticeFor()(key)`. A notice that resolved since the banner dropped in opens nothing.
8. **Swipe and touch** go to `center.dismiss()` and `center.touch(down)`.

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/alerts/alertScreen.test.ts
import { expect, it } from "vitest";
import { alertScreenFor, coversBanners } from "./alertScreen";

const board = { name: "Sessions" };
const session = (ref: string) => ({ name: "Conversation", params: { hubId: "hub-1", ref, title: ref } });
const tasks = { name: "TasksSheet", params: { hubId: "hub-1", ref: "local:a", threadId: "t", hasTasks: true } };
const hub = { name: "Hub", params: { screen: "HubHome" } };
const reader = { name: "Reader", params: { hubId: "hub-1", sessionRef: "local:a" } };

it.each([
	["the Board", [board], { kind: "board" }],
	["a session", [board, session("local:a")], { kind: "session", ref: "local:a" }],
	["a sheet over a session, which keeps it in front", [board, session("local:a"), tasks], { kind: "session", ref: "local:a" }],
	["the Hub over the Board", [board, hub], { kind: "board" }],
	["the Reader, which is its own screen", [board, session("local:a"), reader], { kind: "other" }],
	["a session route with no ref", [board, { name: "Conversation", params: {} }], { kind: "other" }],
	["nothing yet", [], { kind: "other" }],
] as const)("%s", (_name, routes, screen) => {
	expect(alertScreenFor(routes, "hub-1")).toEqual(screen);
});

it("counts a session screen only for its own hub, since refs are per hub", () => {
	expect(alertScreenFor([board, session("local:a")], "hub-2")).toEqual({ kind: "other" });
});

it("counts a sheet route and phase 5's modals as covering banners, and nothing else", () => {
	expect(coversBanners(tasks)).toBe(true);
	expect(coversBanners(hub)).toBe(true);
	expect(coversBanners({ name: "NewSession" })).toBe(true);
	expect(coversBanners(session("local:a"))).toBe(false);
	expect(coversBanners(reader)).toBe(false);
	expect(coversBanners(undefined)).toBe(false);
});
```

`AlertsProvider.test.tsx` drives the provider through a scripted hub, the `boundary()` fake of `src/projectBrowser.test.ts` answering `evener/navigation/read` with `wireV2` as phase 2's `boardData.test.ts` does. Mock `../ConnectionProvider` (a `useConnection` whose `client` the test swaps), `./nativeAlertPreferences` (a store over memory), `../haptics` and `../board/nativeBoardMemory`, whose `seenMarkers` returns a real `SeenMarkers` over a memory store, never an object literal, so a call that loses its receiver fails every case below. A probe component reads `useAlertSnapshot()`. Cover:
- the first reads (a session already failed) show no banner;
- an invalidation that re-reads Needs you with a new question shows that session's banner;
- a Needs you section with `remaining > 0` is not observed until its last page lands, so its second page alerts nothing;
- swapping in a new client whose first reads carry another new failure shows no banner (ruling 2);
- a row that goes `offline: true` and comes back alerts nothing (ruling 3);
- a provider sign-in that expires after the first auth read (an `evener/auth/updated` notification, then a read with `needsLogin`) alerts once, and the scripted hub never sees `evener/plugin/list` (ruling 5);
- `useHoldAlerts(true, "quiet")` in the probe holds the question's banner, and `useHeldAlertCount()` reads 1;
- reporting the routes `[Sessions, Hub]` holds a new question's banner, even with the Hold switch off, and reporting `[Sessions]` shows it 200ms later;
- a hub change resets the center and reports the routes again: a session screen of the old hub still on the stack doesn't quiet the new hub's session with the same ref.

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
// What the stack means for alerts (spec 13.3). The screen in front decides
// what alerts at all: the Board lists notices, and a session answers its own
// alerts. A sheet or modal on top holds banners, since a banner can't show
// above one (ruling 7), and leaves the screen under it in front, as phase 2
// part 3's inFront does for sheets.
import { isSheetRoute } from "../sheet/sheetRoutes";
import type { AlertScreen } from "./alertCenter";

/** Phase 5's modal routes: the Hub and New session sheets. */
const MODAL_ROUTES: ReadonlySet<string> = new Set(["Hub", "NewSession"]);

type Route = { name: string; params?: object };

/** Whether this route, on top, covers the app, so banners wait. */
export function coversBanners(route: Route | undefined): boolean {
	return route !== undefined && (isSheetRoute(route.name) || MODAL_ROUTES.has(route.name));
}

/** The screen alerts care about, from the stack's routes up to the focused
 * one: the top route that isn't a sheet or a modal. A session screen counts
 * only for the hub it belongs to, since refs are per hub. */
export function alertScreenFor(routes: readonly Route[], hubId: string | null): AlertScreen {
	const screen = [...routes].reverse().find((route) => !coversBanners(route));
	if (screen?.name === "Sessions") return { kind: "board" };
	const params = screen?.params as { hubId?: unknown; ref?: unknown } | undefined;
	if (screen?.name === "Conversation" && params?.hubId === hubId && typeof params.ref === "string")
		return { kind: "session", ref: params.ref };
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
1. **The Reader** calls `useHoldAlerts(useIsFocused(), "quiet")`. Phase 4 left its Back as the system back (its Task 14); it becomes a `headerLeft` of `` <BackButton count={held} label={held > 0 ? `Back, ${held} new while you read` : "Back"} onPress={navigation.goBack} /> ``, with `held = useHeldAlertCount()`: the amber count phase 3's Session Back uses, hidden at 0. The words are the prototype's (`panels.js`: "Back. 2 new while you read"), with phase 3's comma. Round 4 problem 6 is why it's a count and not a dot. The swipe-back gesture must still work: check it in the simulator.
2. **The composer** holds while its field is focused and its text isn't blank: `useHoldAlerts(focused && value.trim() !== "", "quiet")`. Sending empties the field, so it lets banners go, as the Hub row promises.
3. **Sheets.** The redesign's sheets are `formSheet` routes (Jesse's answer 11, phase 2 part 3's sheet foundation), which Task 8's route report holds. The React Native `Modal`s that remain (full-screen viewers and the Hub's inner detail sheets) go through `HoldingModal`, which renders `Modal` with the same props and calls `useHoldAlerts(props.visible ?? true, "covered")`: a sheet holds whatever the Hold switch says. Every production `Modal` becomes a `HoldingModal`.
4. **The guard.** `holdingModal.test.ts` fails when a production file other than `HoldingModal.tsx` imports `Modal` from `react-native`, or reaches it through a namespace import as `RN.Modal`, so a sheet added later can't let a banner over it.

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

/** Whether the module renders React Native's own Modal: a named import
 * (`import { Modal }`, renamed or not), or `X.Modal` through a namespace or
 * default import of react-native. */
function importsReactNativeModal(fileName: string, code: string): boolean {
	const source = ts.createSourceFile(fileName, code, ts.ScriptTarget.Latest, true);
	const namespaces = new Set<string>();
	for (const statement of source.statements) {
		if (!ts.isImportDeclaration(statement) || !ts.isStringLiteral(statement.moduleSpecifier)) continue;
		if (statement.moduleSpecifier.text !== "react-native") continue;
		const clause = statement.importClause;
		if (clause?.name) namespaces.add(clause.name.text);
		const bindings = clause?.namedBindings;
		if (bindings && ts.isNamespaceImport(bindings)) namespaces.add(bindings.name.text);
		if (bindings && ts.isNamedImports(bindings)) {
			if (bindings.elements.some((element) => (element.propertyName ?? element.name).text === "Modal")) return true;
		}
	}
	let found = false;
	const visit = (node: ts.Node) => {
		if (
			ts.isPropertyAccessExpression(node) &&
			ts.isIdentifier(node.expression) &&
			namespaces.has(node.expression.text) &&
			node.name.text === "Modal"
		)
			found = true;
		else ts.forEachChild(node, visit);
	};
	if (namespaces.size > 0) visit(source);
	return found;
}

it("finds React Native's Modal however it is imported, and nothing else", () => {
	expect(importsReactNativeModal("a.tsx", `import { Modal, View } from "react-native";`)).toBe(true);
	expect(importsReactNativeModal("b.tsx", `import { Modal as Sheet } from "react-native";`)).toBe(true);
	expect(importsReactNativeModal("c.tsx", `import * as RN from "react-native";\nconst c = <RN.Modal visible />;`)).toBe(true);
	expect(importsReactNativeModal("d.tsx", `import * as RN from "react-native";\nconst d = <RN.View />;`)).toBe(false);
	expect(importsReactNativeModal("e.tsx", `import { HoldingModal as Modal } from "./alerts/HoldingModal";`)).toBe(false);
});

it("every sheet holds alerts while it is up", () => {
	const offenders = productionFiles(SRC)
		.filter((file) => path.relative(SRC, file) !== path.join("alerts", "HoldingModal.tsx"))
		.filter((file) => importsReactNativeModal(file, readFileSync(file, "utf8")))
		.map((file) => path.relative(SRC, file));
	expect(offenders).toEqual([]);
});
```

Phase 4's Reader test: focused, it holds, and with one held alert Back's label reads "Back, 1 new while you read"; blurred, it lets go. Phase 3's `BackButton.test.tsx`: a `label` replaces the accessibility label, and without one it still reads "Back, 4 others need you". Phase 3's `Composer.test.tsx`: focusing an empty field holds nothing; typing holds; clearing the text or blurring lets go. Mock `../alerts/AlertsProvider` with a `useHoldAlerts` that records each call's `active` and kind (both call sites pass `"quiet"`) and a `useHeldAlertCount` the test sets.

- [ ] **Step 2: Run them and watch them fail**

Run: `cd mobile-native && npx vitest run src/alerts/holdingModal.test.ts <the Reader and Composer tests>`
Expected: FAIL. The self-check passes, and the guard lists every file that imports `Modal` from `react-native` (on main at `d0c0211be`, 22 files; phases 2 to 5 move most sheets to routes, so the list shrinks), and nothing holds yet.

- [ ] **Step 3: Implement**

```tsx
// mobile-native/src/alerts/HoldingModal.tsx
// React Native's Modal, holding in-app alerts while it is up (ruling 7): a
// banner over a sheet would cover the sheet's own controls.
import { Modal, type ModalProps } from "react-native";
import { useHoldAlerts } from "./AlertsProvider";

export function HoldingModal(props: ModalProps) {
	useHoldAlerts(props.visible ?? true, "covered");
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

## PR H: Board actions held offline

Jesse, 2026-09-26: "board actions while offline: hold em" (part 1's ruling 18). Spec 7.5 sends a Board action taken offline "to the outbox". Phase 2 part 3 hides those actions while offline (its ruling 21); this PR replaces the hiding with a hold. It builds on part 3's pieces and adds no second copy of them: the Board's one organization journal (`useBoardOrganization`, its Task 10.5), `BoardStops` (its Task 12.2), and `rowMenuActions`, `swipeActions`, `archiveSession`, `pinSession`, `shutDownSession` and `renameSession` (its Task 12.3), plus the project actions (its ruling 15).

Why a hold of its own, and not the mutation outbox: the outbox carries the four turn kinds with receipts and client mutation ids (`nativeMutationRuntime.ts:69-77`), while archive, pin and rename are set-style writes the hub answers without receipts, confirmed by the journal's read-back (`checkOrganizationChange`), and `thread/shutdown` and `evener/thread/name/set` take no client mutation id. Set-style writes replay safely; the hold's job is order, durability, and keeping a Stop or Shut down from landing on work nobody saw.

### Task 19: The hold

**Files:**
- Create: `mobile-native/src/board/boardHold.ts` (the policy and the store) and `mobile-native/src/board/nativeBoardHold.ts` (one store per hub over `expo-sqlite/kv-store`)
- Modify: `mobile-native/src/board/boardMemory.ts` (`forgetBoard` also removes the hold's key)
- Test: `mobile-native/src/board/boardHold.test.ts`

**Interfaces:**
- Consumes: `SyncStringStorage` (`src/syncStringStorage.ts`); `ArchiveParams` and `SessionPinAssignParams` from `@evener/appwire-client`; part 3's project action parameters (its ruling 15); `EvenerThread` (`activeTurnId`, `activeTurnStartedAt`).
- Produces:
  - `type HeldAction =`
    - `| { kind: "archive"; target: Omit<ArchiveParams, "archived">; archived: boolean }`
    - `| { kind: "pin"; target: SessionPinAssignParams }`
    - `| { kind: "project"; key: string; change: ProjectChange }`, where `ProjectChange` is part 3's project action and its parameters
    - `| { kind: "rename"; ref: string; name: string }`
    - `| { kind: "stop" | "shutDown"; ref: string; seen: TurnSeen }`
  - `interface TurnSeen { turnId?: string; updatedAt?: string }`: the running turn's id when the phone knows it, and the row's `updated_at` when the person pressed.
  - `interface HeldRecord { id: string; heldAt: number; action: HeldAction }`
  - `class BoardHold`: constructor `(storage: SyncStringStorage, hubId: string)`; `getSnapshot(): readonly HeldRecord[]` (in the order held), `subscribe(listener)`, `hold(action: HeldAction, now: number): HeldRecord`, `cancel(id: string): void`, `settled(id: string): void`
  - `heldFor(records: readonly HeldRecord[], ref: string): HeldRecord[]`
  - `turnStillSeen(seen: TurnSeen, thread: EvenerThread): boolean`
  - `boardHold(hubId: string): BoardHold` from `nativeBoardHold.ts`
  - Key: `evener.native.board-hold.${hubId}`

**Requirements:**
1. **Durable and per hub.** Every change writes the whole list to the hub's key, so a relaunch finds it. A list that won't parse reads as empty; a write that fails keeps the list for this launch, as `SeenMarkers` does.
2. **One per subject, last wins.** Holding an action replaces a held one on the same subject: archive or unarchive of the same session, a pin of the same session, a rename of the same session, a project change of the same project and kind, a Stop of the same session, a Shut down of the same session. An unarchive that replaces a held archive of the same session just drops it: the pair means "as it was". The replacement keeps the replaced record's place in the order, so replay order is the order of first intent.
3. **The turn guard.** `turnStillSeen(seen, thread)` is true only while `thread.activeTurnId` is set (a turn is running) and that turn is the one the person saw: `activeTurnId === seen.turnId` when the id is known, else `activeTurnStartedAt <= Date.parse(seen.updatedAt)`. Both times are the hub's clock, never the phone's. With neither the id nor a time, it is false. The exact check waits on a server item, S19: navigation rows carry the active turn id. Until S19 lands, `turnId` is known only when this launch has read the session's thread, and the time comparison decides; once it lands, the Board records the row's turn id at the press and the ids decide. If S19 is on main before this PR starts, use the row's turn id from the start. A Stop needs it true. A Shut down is dropped only when a turn is running and the guard is false: with no turn running, shutting down stops nothing the person didn't see.

**Test cases** (`boardHold.test.ts`, over a memory `SyncStringStorage`):
- the order held survives a new store over the same storage, and another hub's store sees none of it;
- a second archive of a session replaces the first in its place; an unarchive after a held archive leaves nothing held; two renames keep the last name;
- `cancel` and `settled` remove a record and notify once;
- unparseable storage reads as empty, and a failing write keeps the list for this launch;
- `turnStillSeen`, as a table: the same id, true; another id, false; no id, a turn started before `updatedAt`, true; started after, false; no turn running, false; neither id nor time, false;
- `forgetBoard` removes `evener.native.board-hold.${hubId}` with the other two keys.

- [ ] Steps: write the failing tests, watch them fail (`cd mobile-native && npx vitest run src/board/boardHold.test.ts src/board/boardMemory.test.ts`), implement, watch them pass with `npm run check`, then commit (`feat(native): a durable hold for Board actions taken offline`).

### Task 20: The Board holds its actions offline, and sends them when the connection returns

**Files:**
- Modify: `mobile-native/src/board/rowActions.ts` (the offline menus), `mobile-native/src/board/boardStops.ts` (a guard), `mobile-native/src/board/BoardScreen.tsx`, `BoardRow.tsx`, `SelectBar.tsx` and the project menu (the hold and its replay)
- Test: `mobile-native/src/board/rowActions.test.ts`, `mobile-native/src/board/boardStops.test.ts` and `mobile-native/src/board/BoardScreen.test.tsx`

**Interfaces:**
- Consumes: Task 19; part 3's `useBoardOrganization`, `organizationFree`, `BoardStops`, `archiveSession`, `pinSession`, `shutDownSession`, `renameSession` and the project actions; phase 3's `Toast`.
- Produces: `BoardStops.stop(client, ref, guard?: (thread: EvenerThread) => boolean): Promise<StopOutcome | "dropped">`: with a guard, it reads the thread as it does today and sends the interrupt only if the guard passes.

**Requirements:**
1. **Offline, the actions stay.** `rowMenuActions` and `swipeActions` offer the same actions offline as online, so part 3's ruling 21 is gone; select mode's actions and the project menu stay too. Offline, choosing one holds it (`boardHold(hubId).hold`) instead of sending it, after the same confirmation Shut down asks for online. A Pin opens part 3's category picker over the Board's loaded catalog, and a Rename its name field; both hold what you choose. A Stop or Shut down records `seen`: the running turn's id (the row's own once S19 lands, else from a thread read this launch made), and the row's `updated_at`.
2. **Held rows say so, where they are** (spec 14): a row with something held is dimmed, as an unconfirmed archive is today, and its second line reads what waits: "Archive waits for the connection", "Stop waits for the connection", and so on, one line for the latest. A held archive leaves the row in place until the hub confirms it. The row's menu adds "Cancel <action>" for each held action, which cancels it.
3. **Replay.** On each ready connection, and on relaunch once the connection is ready, the Board sends the active hub's held actions in the order held, one at a time:
   - Stop and Shut down go at once, wherever you are: `BoardStops.stop(client, ref, (thread) => turnStillSeen(seen, thread))`, and for Shut down a thread read first with the same guard's rule (requirement 3 of Task 19), then `shutDownSession`. A dropped Stop shows the toast "The turn you stopped ended before you were back online"; a dropped Shut down, "A newer turn started, so the session wasn't shut down".
   - Rename goes at once through `renameSession`.
   - Archive, pin and the project changes go through the Board's own journal once the Board is focused and `organizationFree` holds, one change at a time, each confirmed by the journal's check before the next. The journal holds one change per hub, and a second writer would break it (part 3's ruling 16).
   - A record is removed (`settled`) once its request is answered or its check confirms it. A request the hub refuses is removed with the toast the online action shows for it. A request the connection loses stays held for the next ready connection.
4. **Nothing else changes online.** With the connection ready, every action behaves as part 3 built it.

**Test cases:**
- `rowActions.test.ts`: offline, `rowMenuActions` and `swipeActions` return the online actions for each state (part 3's offline rows change from "hidden" to these).
- `boardStops.test.ts`, over the real runtime as part 3's tests run it: a guard that passes sends `turn/interrupt` once; a guard that fails sends nothing, returns `"dropped"`, and releases the target.
- `BoardScreen.test.tsx`, through a scripted hub and the mocked connection:
  - offline, archiving a row holds it: the row dims and reads "Archive waits for the connection", and nothing reaches the hub; back online with the Board focused, `evener/archive/set` goes once, the journal's check confirms it, and the hold is empty;
  - a held Stop whose turn is still the one seen sends `turn/interrupt` once on reconnect, even with the Board blurred;
  - a held Stop for a session now running a newer turn (`activeTurnStartedAt` after the held `updatedAt`) sends no interrupt and shows the toast;
  - actions held before a relaunch (a new store over the same storage) go out on the first ready connection, in the order held;
  - "Cancel Archive" removes the held archive, and the row is itself again.

- [ ] Steps: write the failing tests, watch them fail (`cd mobile-native && npx vitest run src/board`), implement, watch them pass with `npm run check`, then in a Release simulator build against the demo hub: turn the network off, archive and stop rows, turn it on, and watch them go. Commit (`feat(native): Board actions taken offline wait and go when the connection returns`). Open PR H: "feat(native): Board actions held offline (phase 6, PR H)".

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
