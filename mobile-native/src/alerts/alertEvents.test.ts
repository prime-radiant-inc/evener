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

	it("alerts when a session starts needing you, and again when it moves to another needs-you state", () => {
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

	it("alerts once per needs-you state: a new question or approval target in it says nothing (question 3)", () => {
		const asking = row("a", { state: "awaiting", ask_pending: true, question: { text: "Keep the flag?", count: 1 } });
		const allowing = row("b", {
			state: "active",
			approval_pending: true,
			approval_tool: "write_file",
			approval_target: "~/a",
		});
		const start = detectSessionAlerts(null, bands([asking, allowing]), none).states;
		const next = [
			{ ...asking, question: { text: "Which branch?", count: 1 } },
			{ ...allowing, approval_target: "~/b" },
		];
		expect(detectSessionAlerts(start, bands(next), none).alerts).toEqual([]);
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

	it("diffs a session once even if it arrives in two bands, Needs you first", () => {
		const asking = row("d", { state: "awaiting", ask_pending: true });
		const start = detectSessionAlerts(null, bands([row("d", { state: "active" })]), none).states;
		const twice = {
			needsYou: [{ row: asking, state: "question" as const }],
			finished: [],
			working: [{ row: row("d", { state: "active" }), state: "working" as const }],
			idle: [],
		};
		const later = detectSessionAlerts(start, twice, none);
		expect(later.alerts.map((alert) => alert.kind)).toEqual(["question"]);
		expect(later.states.get("d")).toBe("question");
		expect(detectSessionAlerts(later.states, twice, none).alerts).toEqual([]);
	});

	it("alerts a finished turn only when the session was working", () => {
		const start = detectSessionAlerts(
			null,
			bands([row("a", { state: "active" }), row("b", { state: "awaiting", ask_pending: true })]),
			none,
		).states;
		const later = detectSessionAlerts(
			start,
			bands([row("a", { state: "awaiting" }), row("b", { state: "awaiting" })]),
			none,
		);
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

describe("Warning alerts while children run", () => {
	it.each([false, true])("waits until the last child settles, hub Needs you membership %s", (inNeedsYou) => {
		const start = detectSessionAlerts(null, bands([row("a", { state: "active" })]), none).states;
		const warning = row("a", { state: "warning", subagents: { running: 1, failed: 1, done: 0 } });
		const workingBands = bands([warning], inNeedsYou ? [warning] : []);
		const working = detectSessionAlerts(start, workingBands, none);
		expect(workingBands.needsYou).toEqual([]);
		expect(working.states.get("a")).toBe("working");
		expect(working.alerts).toEqual([]);
		expect(detectSessionAlerts(working.states, workingBands, none).alerts).toEqual([]);

		const settled = { ...warning, subagents: { running: 0, failed: 1, done: 1 } };
		const settledBands = bands([settled], inNeedsYou ? [settled] : []);
		const later = detectSessionAlerts(working.states, settledBands, none);
		expect(settledBands.needsYou.map((item) => item.row.ref)).toEqual(["a"]);
		expect(later.states.get("a")).toBe("warning");
		expect(later.alerts).toEqual([
			{
				kind: "warning",
				ref: "a",
				title: "Session a",
				why: { word: "Warning", hue: "attention", text: "open the session to see it" },
			},
		]);
		expect(detectSessionAlerts(later.states, settledBands, none).alerts).toEqual([]);
	});

	it("never alerts a Warning that clears before the last child settles", () => {
		const start = detectSessionAlerts(null, bands([row("a", { state: "active" })]), none).states;
		const warning = row("a", { state: "warning", subagents: { running: 1, failed: 1, done: 0 } });
		const deferred = detectSessionAlerts(start, bands([warning], [warning]), none);
		expect(deferred.alerts).toEqual([]);
		const cleared = { ...warning, state: "active" };
		const working = detectSessionAlerts(deferred.states, bands([cleared]), none);
		expect(working.alerts).toEqual([]);
		const settledBands = bands([{ ...cleared, state: "awaiting", subagents: { running: 0, failed: 1, done: 1 } }]);
		const settled = detectSessionAlerts(working.states, settledBands, none);
		// Clearing the warning keeps the ordinary finished-turn alert.
		expect(settled.alerts).toEqual([{ kind: "finished", ref: "a", title: "Session a", why: null }]);
		expect(detectSessionAlerts(settled.states, settledBands, none).alerts).toEqual([]);
	});

	it.each([{ ask_pending: true }, { approval_pending: true }])(
		"alerts a blocking Warning immediately, %o",
		(pending) => {
			const start = detectSessionAlerts(null, bands([row("a", { state: "active" })]), none).states;
			const warning = row("a", {
				state: "warning",
				subagents: { running: 1, failed: 1, done: 0 },
				...pending,
			});
			const warningBands = bands([warning], [warning]);
			const later = detectSessionAlerts(start, warningBands, none);
			expect(warningBands.needsYou.map((item) => item.row.ref)).toEqual(["a"]);
			expect(later.alerts.map((alert) => ({ kind: alert.kind, ref: alert.ref }))).toEqual([
				{ kind: "warning", ref: "a" },
			]);
			expect(detectSessionAlerts(later.states, warningBands, none).alerts).toEqual([]);
		},
	);

	it("does not alert a settled Warning on the connection's first read", () => {
		const warning = row("a", { state: "warning", subagents: { running: 0, failed: 1, done: 1 } });
		const warningBands = bands([warning], [warning]);
		const first = detectSessionAlerts(null, warningBands, none);
		expect(first.states.get("a")).toBe("warning");
		expect(first.alerts).toEqual([]);
		expect(detectSessionAlerts(first.states, warningBands, none).alerts).toEqual([]);
	});

	it("keeps a deferred Warning quiet through offline and recovery, then alerts at settlement", () => {
		const warning = row("a", { state: "warning", subagents: { running: 1, failed: 1, done: 0 } });
		const first = detectSessionAlerts(null, bands([warning], [warning]), none);
		expect(first.alerts).toEqual([]);
		const offline = { ...warning, offline: true };
		const away = detectSessionAlerts(first.states, bands([offline], [offline]), new Set(["a"]));
		expect(away.states.get("a")).toBe("working");
		expect(away.alerts).toEqual([]);
		const back = detectSessionAlerts(away.states, bands([warning], [warning]), none);
		expect(back.states.get("a")).toBe("working");
		expect(back.alerts).toEqual([]);
		const settled = { ...warning, subagents: { running: 0, failed: 1, done: 1 } };
		expect(
			detectSessionAlerts(back.states, bands([settled], [settled]), none).alerts.map((alert) => alert.kind),
		).toEqual(["warning"]);
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
const brokenPlugin: Notice = {
	kind: "plugin",
	key: "plugin:go",
	text: "go is broken",
	action: "Plugins",
	pluginId: "go",
	marketplace: "evener",
};

describe("notice alerts (ruling 5)", () => {
	it("alerts a sign-in or host notice when it appears, never a broken plugin, and nothing on the first read", () => {
		const first = detectNoticeAlerts(null, [hostDown]);
		expect(first.alerts).toEqual([]);
		const later = detectNoticeAlerts(first.keys, [hostDown, signIn, brokenPlugin]);
		expect(later.alerts).toEqual([
			{ kind: "notice", key: "signIn:codex", title: "codex-jesse-fsck.com sign-in expired" },
		]);
	});

	it("alerts again for a notice that went away and came back", () => {
		const first = detectNoticeAlerts(null, [hostDown]);
		const cleared = detectNoticeAlerts(first.keys, []);
		expect(detectNoticeAlerts(cleared.keys, [hostDown]).alerts.map((alert) => alert.key)).toEqual([
			"host:paradise-park",
		]);
	});
});

describe("the feed", () => {
	it("retracts a session's alert once it stops needing you", () => {
		const offered: Alert[] = [];
		const retracted: string[] = [];
		const feed = new AlertFeed({ offer: (alert) => offered.push(alert), retract: (ref) => retracted.push(ref) });
		feed.observeSessions(bands([row("a", { state: "active" })]), none);
		feed.observeSessions(bands([row("a", { state: "awaiting", ask_pending: true })]), none);
		expect(retracted).toEqual([]);
		// Answered elsewhere: it goes back to work.
		feed.observeSessions(bands([row("a", { state: "active" })]), none);
		expect(retracted).toEqual(["a"]);
		// A host going away keeps its last state, so it retracts nothing.
		feed.observeSessions(bands([row("a", { state: "awaiting", ask_pending: true })]), none);
		feed.observeSessions(bands([row("a", { state: "awaiting", ask_pending: true, offline: true })]), new Set(["a"]));
		expect(retracted).toEqual(["a"]);
	});

	it("keeps separate baselines for sessions and notices, and starts over for a new client", () => {
		const offered: Alert[] = [];
		const feed = new AlertFeed({ offer: (alert) => offered.push(alert), retract: () => {} });
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
