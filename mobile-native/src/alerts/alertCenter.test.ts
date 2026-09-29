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
	sessionRef,
} from "./alertCenter";

const timer = {
	now: () => Date.now(),
	setTimeout: (callback: () => void, ms: number) => setTimeout(callback, ms),
	clearTimeout: (handle: unknown) => clearTimeout(handle as ReturnType<typeof setTimeout>),
};

function session(ref: string, kind: SessionAlert["kind"] = "question"): SessionAlert {
	return { kind, ref, title: `Session ${ref}`, why: null };
}
const hostOffline: Alert = {
	kind: "notice",
	key: "host:paradise-park",
	title: "paradise-park is offline · 3 sessions",
};

function center() {
	const haptics: Haptic[] = [];
	return { alerts: new AlertCenter(timer, (kind) => haptics.push(kind)), haptics };
}
function shown(alerts: AlertCenter): string[] | undefined {
	return alerts
		.getSnapshot()
		.banner?.alerts.map((alert) => sessionRef(alert) ?? (alert.kind === "notice" ? alert.key : alert.kind));
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

	it("tells you a session you started opened elsewhere, whatever the finished setting, and opens it on a tap", () => {
		const { alerts, haptics } = center();
		alerts.offer({ kind: "started", ref: "a", title: "Fix the flaky test", why: null });
		expect(shown(alerts)).toEqual(["a"]);
		expect(haptics).toEqual([]);
		expect(alerts.tap()).toEqual({ kind: "session", ref: "a", title: "Fix the flaky test" });
	});

	it("keeps a started session waiting while you read or type, and shows it when you're done", () => {
		const { alerts } = center();
		const release = alerts.hold("quiet");
		alerts.offer({ kind: "started", ref: "a", title: "Fix the flaky test", why: null });
		expect(alerts.getSnapshot()).toMatchObject({ banner: null, held: 1 });
		release();
		vi.advanceTimersByTime(RELEASE_MS);
		expect(shown(alerts)).toEqual(["a"]);
	});

	it.each(["expires", "is swiped away"] as const)(
		"shows a started session that waited with a session needing you once that banner %s",
		(end) => {
			const { alerts } = center();
			const release = alerts.hold("quiet");
			alerts.offer({ kind: "started", ref: "s", title: "Fix the flaky test", why: null });
			alerts.offer(session("q"));
			release();
			vi.advanceTimersByTime(RELEASE_MS);
			expect(shown(alerts)).toEqual(["q"]);
			if (end === "expires") vi.advanceTimersByTime(BANNER_MS);
			else alerts.dismiss();
			expect(shown(alerts)).toEqual(["s"]);
		},
	);

	it("shows a started session that waited once the banner ahead of it is answered by looking at it", () => {
		const { alerts } = center();
		const release = alerts.hold("quiet");
		alerts.offer({ kind: "started", ref: "s", title: "Fix the flaky test", why: null });
		alerts.offer(session("q"));
		release();
		vi.advanceTimersByTime(RELEASE_MS);
		expect(shown(alerts)).toEqual(["q"]);
		// Opening q answers its banner; the started session is next.
		alerts.setScreen({ kind: "session", ref: "q" });
		expect(shown(alerts)).toEqual(["s"]);
	});

	it("brings a started session back after a banner that replaced it", () => {
		const { alerts } = center();
		alerts.offer({ kind: "started", ref: "s", title: "Fix the flaky test", why: null });
		expect(shown(alerts)).toEqual(["s"]);
		alerts.offer(session("q"));
		expect(shown(alerts)).toEqual(["q"]);
		alerts.dismiss();
		expect(shown(alerts)).toEqual(["s"]);
		alerts.offer(hostOffline);
		expect(shown(alerts)).toEqual(["host:paradise-park"]);
		vi.advanceTimersByTime(BANNER_MS);
		expect(shown(alerts)).toEqual(["s"]);
	});

	it("brings a started session back after sessions that waited replace it together", () => {
		const { alerts } = center();
		alerts.offer({ kind: "started", ref: "s", title: "Fix the flaky test", why: null });
		const release = alerts.hold("quiet");
		alerts.offer(session("q1"));
		alerts.offer(session("q2"));
		release();
		vi.advanceTimersByTime(RELEASE_MS);
		expect(shown(alerts)).toEqual(["q1", "q2"]);
		alerts.dismiss();
		expect(shown(alerts)).toEqual(["s"]);
	});

	it("lets a banner that isn't about sessions needing you finish before a started session that waited", () => {
		const { alerts } = center();
		alerts.offer(hostOffline);
		const release = alerts.hold("quiet");
		alerts.offer({ kind: "started", ref: "s", title: "Fix the flaky test", why: null });
		release();
		vi.advanceTimersByTime(RELEASE_MS);
		expect(shown(alerts)).toEqual(["host:paradise-park"]);
		alerts.dismiss();
		expect(shown(alerts)).toEqual(["s"]);
	});

	it("keeps every started session that waits, and shows them in turn", () => {
		const { alerts } = center();
		let release = alerts.hold("quiet");
		alerts.offer({ kind: "started", ref: "s1", title: "One", why: null });
		alerts.offer(session("q"));
		release();
		vi.advanceTimersByTime(RELEASE_MS);
		release = alerts.hold("quiet");
		alerts.offer({ kind: "started", ref: "s2", title: "Two", why: null });
		release();
		vi.advanceTimersByTime(RELEASE_MS);
		expect(shown(alerts)).toEqual(["q"]);
		alerts.dismiss();
		expect(shown(alerts)).toEqual(["s1"]);
		alerts.dismiss();
		expect(shown(alerts)).toEqual(["s2"]);
	});

	it("drops a started session that waited once you look at it", () => {
		const { alerts } = center();
		const release = alerts.hold("quiet");
		alerts.offer({ kind: "started", ref: "s", title: "Fix the flaky test", why: null });
		alerts.offer(session("q"));
		release();
		vi.advanceTimersByTime(RELEASE_MS);
		alerts.setScreen({ kind: "session", ref: "s" });
		vi.advanceTimersByTime(BANNER_MS);
		expect(alerts.getSnapshot().banner).toBeNull();
	});

	it("shows a waiting started session before a waiting notice, and the notice after it", () => {
		const { alerts } = center();
		const release = alerts.hold("covered");
		alerts.offer({ kind: "started", ref: "a", title: "Fix the flaky test", why: null });
		alerts.offer(hostOffline);
		release();
		vi.advanceTimersByTime(RELEASE_MS);
		expect(shown(alerts)).toEqual(["a"]);
		alerts.dismiss();
		expect(shown(alerts)).toEqual(["host:paradise-park"]);
	});

	it("keeps a started session that waited when Next takes you on", () => {
		const { alerts } = center();
		alerts.hold("quiet");
		alerts.offer({ kind: "started", ref: "s", title: "Fix the flaky test", why: null });
		alerts.offer(session("q"));
		alerts.nextUsed();
		expect(alerts.getSnapshot().held).toBe(1);
	});

	it("never lets a started session join or replace a banner that is up, and shows it after", () => {
		const { alerts } = center();
		alerts.offer(session("a"));
		alerts.offer({ kind: "started", ref: "b", title: "Session b", why: null });
		expect(shown(alerts)).toEqual(["a"]);
		vi.advanceTimersByTime(BANNER_MS);
		expect(shown(alerts)).toEqual(["b"]);
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

	it("takes notices away when you go to the Board, which lists them", () => {
		const { alerts } = center();
		alerts.offer(hostOffline);
		expect(shown(alerts)).toEqual(["host:paradise-park"]);
		alerts.setScreen({ kind: "board" });
		expect(alerts.getSnapshot().banner).toBeNull();
		const release = alerts.hold("quiet");
		alerts.setScreen({ kind: "other" });
		alerts.offer(hostOffline);
		alerts.offer(session("a"));
		alerts.setScreen({ kind: "board" });
		release();
		vi.advanceTimersByTime(RELEASE_MS);
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

describe("a session that stops needing you", () => {
	it("drops its held alert, so nothing stale drops in when you leave", () => {
		const { alerts } = center();
		const release = alerts.hold("quiet");
		alerts.offer(session("a"));
		alerts.offer(session("b"));
		alerts.retract("a");
		expect(alerts.getSnapshot().held).toBe(1);
		release();
		vi.advanceTimersByTime(RELEASE_MS);
		expect(shown(alerts)).toEqual(["b"]);
	});

	it("leaves a banner still up, or takes it down when it was the only one", () => {
		const { alerts } = center();
		alerts.offer(session("a"));
		alerts.offer(session("b"));
		const id = alerts.getSnapshot().banner?.id;
		alerts.retract("a");
		expect(shown(alerts)).toEqual(["b"]);
		expect(alerts.getSnapshot().banner?.id).toBe(id);
		alerts.retract("b");
		expect(alerts.getSnapshot().banner).toBeNull();
	});

	it("says nothing when it had nothing about the session", () => {
		const { alerts } = center();
		let calls = 0;
		alerts.subscribe(() => calls++);
		alerts.retract("a");
		expect(calls).toBe(0);
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

	it("lets a release go when a quiet hold starts with the Hold switch off", () => {
		const { alerts } = center();
		alerts.hold("quiet");
		alerts.offer(session("a"));
		alerts.setPreferences({ ...DEFAULT_ALERT_PREFERENCES, hold: false });
		const typing = alerts.hold("quiet");
		vi.advanceTimersByTime(RELEASE_MS);
		// With Hold off, typing holds nothing, so it can't keep "a" back.
		expect(shown(alerts)).toEqual(["a"]);
		typing();
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

describe("a New session start that failed after its sheet closed (#3104)", () => {
	const failed: Alert = { kind: "startFailed", hubId: "hub-a", hubName: "magic-kingdom", uncertain: false };

	it("shows, buzzes as a failure, and opens New session on a tap", () => {
		const { alerts, haptics } = center();
		alerts.offer(failed);
		expect(shown(alerts)).toEqual(["startFailed"]);
		expect(haptics).toEqual(["warning"]);
		expect(alerts.tap()).toEqual({ kind: "newSession", hubId: "hub-a", hubName: "magic-kingdom" });
	});

	it("shows whatever the failures setting, since it's about what you just did", () => {
		const { alerts } = center();
		alerts.setPreferences({ ...DEFAULT_ALERT_PREFERENCES, failures: false });
		alerts.offer(failed);
		expect(shown(alerts)).toEqual(["startFailed"]);
	});

	it("is never lost: it waits behind a banner that is up, and out a hold, and keeps waiting when Next is used", () => {
		const { alerts } = center();
		alerts.offer(session("a"));
		alerts.offer(failed);
		expect(shown(alerts)).toEqual(["a"]);
		alerts.dismiss();
		expect(shown(alerts)).toEqual(["startFailed"]);
		alerts.dismiss();
		const release = alerts.hold("covered");
		alerts.offer(failed);
		alerts.offer(session("q"));
		alerts.nextUsed();
		expect(alerts.getSnapshot().held).toBe(1);
		release();
		vi.advanceTimersByTime(RELEASE_MS);
		expect(shown(alerts)).toEqual(["startFailed"]);
	});

	it("keeps a failed start on each hub, one after the other", () => {
		const { alerts } = center();
		const release = alerts.hold("covered");
		alerts.offer(failed);
		alerts.offer({ kind: "startFailed", hubId: "hub-b", hubName: "paradise-park", uncertain: true });
		expect(alerts.getSnapshot().held).toBe(2);
		release();
		vi.advanceTimersByTime(RELEASE_MS);
		expect(alerts.tap()).toEqual({ kind: "newSession", hubId: "hub-a", hubName: "magic-kingdom" });
		expect(alerts.tap()).toEqual({ kind: "newSession", hubId: "hub-b", hubName: "paradise-park" });
	});

	it("survives switching hubs, shown or waiting, since it names its own hub", () => {
		const { alerts } = center();
		alerts.offer(session("a"));
		alerts.offer(failed);
		alerts.reset();
		expect(shown(alerts)).toEqual(["startFailed"]);
		expect(alerts.tap()).toEqual({ kind: "newSession", hubId: "hub-a", hubName: "magic-kingdom" });
		const release = alerts.hold("covered");
		alerts.offer(failed);
		alerts.offer(session("q"));
		alerts.reset();
		expect(alerts.getSnapshot()).toMatchObject({ banner: null, held: 1 });
		release();
		vi.advanceTimersByTime(RELEASE_MS);
		expect(shown(alerts)).toEqual(["startFailed"]);
	});

	it("tells its listeners about a hub switch, with the failed start still on the banner", () => {
		const { alerts } = center();
		alerts.offer(session("a"));
		alerts.offer(failed);
		const snapshots: (string[] | undefined)[] = [];
		alerts.subscribe(() => snapshots.push(shown(alerts)));
		alerts.reset();
		expect(snapshots.length).toBeGreaterThan(0);
		expect(snapshots.at(-1)).toEqual(["startFailed"]);
	});

	it("goes only for the hub whose New session is opened", () => {
		const { alerts } = center();
		alerts.offer(failed);
		alerts.offer({ kind: "startFailed", hubId: "hub-b", hubName: "paradise-park", uncertain: false });
		alerts.startFailureSeen("hub-b");
		expect(alerts.tap()).toEqual({ kind: "newSession", hubId: "hub-a", hubName: "magic-kingdom" });
		expect(alerts.getSnapshot().banner).toBeNull();
	});

	it("goes once New session is opened, which shows the same reason", () => {
		const { alerts } = center();
		alerts.offer(failed);
		alerts.startFailureSeen("hub-a");
		expect(alerts.getSnapshot().banner).toBeNull();
		const release = alerts.hold("covered");
		alerts.offer(failed);
		alerts.startFailureSeen("hub-a");
		release();
		vi.advanceTimersByTime(RELEASE_MS);
		expect(alerts.getSnapshot()).toMatchObject({ banner: null, held: 0 });
	});
});

it("buzzes a failed start once, not again when a hub switch shows it again (#3104)", () => {
	const { alerts, haptics } = center();
	alerts.offer({ kind: "startFailed", hubId: "hub-1", hubName: "magic-kingdom", uncertain: false });
	expect(haptics).toEqual(["warning"]);
	alerts.reset();
	expect(alerts.getSnapshot().banner?.alerts).toEqual([
		{ kind: "startFailed", hubId: "hub-1", hubName: "magic-kingdom", uncertain: false },
	]);
	expect(haptics).toEqual(["warning"]);
});
