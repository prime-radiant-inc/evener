import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
// The hub's seen marker on the phone (S4): which path decides a row, the
// pending marks that show at once, and the one-at-a-time seen/set calls, all
// against a fake client that records every call and answers when told.
import { type NavigationSessionSummary, type SessionSeenMark, WireError } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { SeenMarkers } from "./boardMemory";
import { BoardSeen, forgetHubSeenMarks, HubSeenMarks, hubSeenMarks } from "./hubSeen";

const T = Date.UTC(2026, 8, 26, 12, 0);
const iso = (ms: number) => new Date(ms).toISOString();

const row = (ref: string, over: Partial<NavigationSessionSummary> = {}): NavigationSessionSummary => ({
	ref,
	host_id: "local",
	session_id: ref,
	title: ref,
	project: "evener",
	state: "idle",
	kind: "session",
	live: true,
	children: [],
	updated_at: iso(T),
	...over,
});
/** A row the hub decides: its turn ended at `endedAt`. */
const ended = (ref: string, endedAt: number, unseen: boolean) => row(ref, { turn_ended_at: iso(endedAt), unseen });

interface Call {
	client: string;
	sessions: SessionSeenMark[];
	answer: () => void;
	refuse: (code: number) => void;
	drop: () => void;
}
/** A client whose seen/set calls wait until the test answers each one. */
function fakeClient(name: string, calls: Call[]): ConversationClientLike {
	return Object.assign(new FakeClient("ready"), {
		request: (method, params) =>
			new Promise((resolve, reject) => {
				if (method !== "evener/session/seen/set") throw new Error(`unexpected ${method}`);
				calls.push({
					client: name,
					sessions: (params as { sessions: SessionSeenMark[] }).sessions,
					answer: () => resolve({ ok: true, changed: true, navigation: { generation_id: "g", targets: [] } } as never),
					refuse: (code) => reject(new WireError("refused", code)),
					drop: () => reject(new Error("connection closed")),
				});
			}),
		onNotification: () => () => {},
	} as Omit<ConversationClientLike, "state" | "onReady" | "onStateChange">);
}
async function settle() {
	for (let step = 0; step < 20; step++) await Promise.resolve();
}
function setup() {
	const calls: Call[] = [];
	return { calls, client: fakeClient("a", calls), marks: new HubSeenMarks() };
}
/** setup(), plus the device's own markers with an epoch a minute before T,
 * and the Board's seen state over both paths. */
function board() {
	const { calls, client, marks } = setup();
	const values = new Map<string, string>();
	const markers = new SeenMarkers(
		{
			getItemSync: (key) => values.get(key) ?? null,
			setItemSync: (key, value) => void values.set(key, value),
			removeItemSync: (key) => void values.delete(key),
		},
		"hub",
	);
	markers.adoptEpoch([{ updated_at: iso(T - 60_000) }]);
	return { markers, calls, client, seen: new BoardSeen(markers, marks) };
}

describe("which path decides a row", () => {
	it("leaves a row without turn_ended_at to the device, and follows the hub's unseen for one with it", () => {
		const { marks } = setup();
		expect(marks.isSeenOnHub(row("old"))).toBeNull();
		expect(marks.isSeenOnHub(row("old", { unseen: true }))).toBeNull();
		expect(marks.isSeenOnHub(ended("a", T, true))).toBe(false);
		expect(marks.isSeenOnHub(ended("a", T, false))).toBe(true);
		expect(marks.isSeenOnHub(row("a", { turn_ended_at: iso(T) }))).toBe(true);
	});

	it("leaves a row whose turn_ended_at doesn't parse to the device, pending mark or not", () => {
		const { marks, client } = setup();
		expect(marks.isSeenOnHub(row("bad", { turn_ended_at: "not a time", unseen: true }))).toBeNull();
		marks.markUnread(client, ["bad"]);
		expect(marks.isSeenOnHub(row("bad", { turn_ended_at: "not a time", unseen: false }))).toBeNull();
	});

	it("ignores the device's markers for a hub row, and uses them for any other", () => {
		const { markers, seen } = board();
		markers.markUnread("unseen-here");
		// The device says unread; the hub says seen, and wins.
		expect(seen.isSeen(ended("unseen-here", T, false))).toBe(true);
		// The device's epoch says seen; the hub says unseen, and wins.
		expect(
			seen.isSeen(row("fresh", { turn_ended_at: iso(T - 120_000), unseen: true, updated_at: iso(T - 120_000) })),
		).toBe(false);
		// Without turn_ended_at the device decides: updated after its epoch.
		expect(seen.isSeen(row("fresh"))).toBe(false);
		markers.markSeen(row("fresh"));
		expect(seen.isSeen(row("fresh"))).toBe(true);
		expect(seen.isSeen(row("unseen-here"))).toBe(false);
	});
});

describe("pending marks", () => {
	it("shows a seen mark at once, until a newer turn ends", async () => {
		const { marks, client } = setup();
		marks.markSeen(client, [{ ref: "a", seenThrough: T }]);
		expect(marks.isSeenOnHub(ended("a", T, true))).toBe(true);
		expect(marks.isSeenOnHub(ended("a", T - 1, true))).toBe(true);
		expect(marks.isSeenOnHub(ended("a", T + 1, true))).toBe(false);
		// Another ref is untouched.
		expect(marks.isSeenOnHub(ended("b", T, true))).toBe(false);
	});

	it("shows an unread mark at once", () => {
		const { marks, client } = setup();
		marks.markUnread(client, ["a"]);
		expect(marks.isSeenOnHub(ended("a", T, false))).toBe(false);
		// It never decides a row the hub doesn't.
		expect(marks.isSeenOnHub(row("a"))).toBeNull();
	});

	it("drops a seen mark once the hub reads seen, or once a newer turn ends, and keeps it otherwise", () => {
		const { marks, client } = setup();
		marks.markSeen(client, [
			{ ref: "landed", seenThrough: T },
			{ ref: "newer", seenThrough: T },
			{ ref: "waiting", seenThrough: T },
		]);
		const revision = marks.getRevision();
		marks.prune([ended("landed", T, false), ended("newer", T + 1, true), ended("waiting", T, true)]);
		expect(marks.getRevision()).toBeGreaterThan(revision);
		// Dropped: the hub's own state shows again.
		expect(marks.isSeenOnHub(ended("landed", T, true))).toBe(false);
		expect(marks.isSeenOnHub(ended("newer", T, true))).toBe(false);
		// Kept: the hub still reads unseen for the same turn.
		expect(marks.isSeenOnHub(ended("waiting", T, true))).toBe(true);
	});

	it("drops an unread mark once the hub reads unseen, and keeps it otherwise", () => {
		const { marks, client } = setup();
		marks.markUnread(client, ["landed", "waiting"]);
		marks.prune([ended("landed", T, true), ended("waiting", T, false)]);
		expect(marks.isSeenOnHub(ended("landed", T, false))).toBe(true);
		expect(marks.isSeenOnHub(ended("waiting", T, false))).toBe(false);
	});

	it("keeps a pending mark when the only rows for its ref have no readable turn end", () => {
		const { marks, client } = setup();
		marks.markSeen(client, [{ ref: "seen", seenThrough: T }]);
		marks.markUnread(client, ["unread"]);
		const revision = marks.getRevision();
		// Such a row is the device's to decide, so it can't show a hub mark landed.
		marks.prune([row("seen"), row("seen", { turn_ended_at: "not a time" }), row("unread", { unseen: true })]);
		expect(marks.getRevision()).toBe(revision);
		expect(marks.isSeenOnHub(ended("seen", T, true))).toBe(true);
		expect(marks.isSeenOnHub(ended("unread", T, false))).toBe(false);
	});

	it("changes nothing, and tells no one, when pruning drops nothing", () => {
		const { marks, client } = setup();
		marks.markSeen(client, [{ ref: "a", seenThrough: T }]);
		let told = 0;
		marks.subscribe(() => told++);
		const revision = marks.getRevision();
		marks.prune([ended("a", T, true), ended("other", T, false)]);
		expect(marks.getRevision()).toBe(revision);
		expect(told).toBe(0);
	});

	it("tells its subscribers about every mark", () => {
		const { marks, client } = setup();
		let told = 0;
		const stop = marks.subscribe(() => told++);
		marks.markSeen(client, [{ ref: "a", seenThrough: T }]);
		marks.markUnread(client, ["a"]);
		expect(told).toBe(2);
		stop();
		marks.markSeen(client, [{ ref: "a", seenThrough: T }]);
		expect(told).toBe(2);
	});
});

describe("sending", () => {
	it("sends one call with every mark, then marks it acknowledged without resending", async () => {
		const { marks, client, calls } = setup();
		marks.markSeen(client, [
			{ ref: "a", seenThrough: T },
			{ ref: "b", seenThrough: T - 5 },
		]);
		expect(calls.map((call) => call.sessions)).toEqual([
			[
				{ ref: "a", seenThrough: T },
				{ ref: "b", seenThrough: T - 5 },
			],
		]);
		calls[0].answer();
		await settle();
		// Acknowledged, the mark still shows until the hub's rows catch up.
		expect(marks.isSeenOnHub(ended("a", T, true))).toBe(true);
		marks.flush(client);
		await settle();
		expect(calls).toHaveLength(1);
	});

	it("sends one call at a time, in the order the marks were made", async () => {
		const { marks, client, calls } = setup();
		marks.markSeen(client, [{ ref: "a", seenThrough: T }]);
		marks.markUnread(client, ["a"]);
		marks.markUnread(client, ["b"]);
		expect(calls).toHaveLength(1);
		calls[0].answer();
		await settle();
		expect(calls.map((call) => call.sessions)).toEqual([
			[{ ref: "a", seenThrough: T }],
			[
				{ ref: "a", unread: true },
				{ ref: "b", unread: true },
			],
		]);
		calls[1].answer();
		await settle();
		expect(calls).toHaveLength(2);
		// The later mark stands.
		expect(marks.isSeenOnHub(ended("a", T, false))).toBe(false);
	});

	it("records and sends nothing for a seen mark that doesn't advance the one it has", async () => {
		const { marks, client, calls } = setup();
		let told = 0;
		marks.subscribe(() => told++);
		marks.markSeen(client, [{ ref: "a", seenThrough: T }]);
		marks.markSeen(client, [{ ref: "a", seenThrough: T }]);
		marks.markSeen(client, [{ ref: "a", seenThrough: T - 1 }]);
		calls[0].answer();
		await settle();
		marks.markSeen(client, [{ ref: "a", seenThrough: T }]);
		await settle();
		expect(calls).toHaveLength(1);
		expect(told).toBe(1);
		// An unread mark that repeats a pending one changes nothing either.
		marks.markUnread(client, ["b"]);
		marks.markUnread(client, ["b"]);
		expect(calls).toHaveLength(2);
		expect(told).toBe(2);
		// A later turn does advance it.
		calls[1].answer();
		await settle();
		marks.markSeen(client, [{ ref: "a", seenThrough: T + 1 }]);
		expect(calls.map((call) => call.sessions).at(-1)).toEqual([{ ref: "a", seenThrough: T + 1 }]);
	});

	it("lets a seen mark replace an unread one and an unread mark replace a seen one", async () => {
		const { marks, client, calls } = setup();
		marks.markUnread(client, ["a"]);
		calls[0].answer();
		await settle();
		marks.markSeen(client, [{ ref: "a", seenThrough: T }]);
		expect(marks.isSeenOnHub(ended("a", T, true))).toBe(true);
		calls[1].answer();
		await settle();
		marks.markUnread(client, ["a"]);
		expect(marks.isSeenOnHub(ended("a", T, false))).toBe(false);
		expect(calls.map((call) => call.sessions)).toEqual([
			[{ ref: "a", unread: true }],
			[{ ref: "a", seenThrough: T }],
			[{ ref: "a", unread: true }],
		]);
	});

	it("drops a refused call's marks, so the row shows the hub's own state again", async () => {
		const { marks, client, calls } = setup();
		marks.markSeen(client, [{ ref: "a", seenThrough: T }]);
		const revision = marks.getRevision();
		calls[0].refuse(-32602);
		await settle();
		expect(marks.getRevision()).toBeGreaterThan(revision);
		expect(marks.isSeenOnHub(ended("a", T, true))).toBe(false);
		marks.flush(client);
		await settle();
		expect(calls).toHaveLength(1);
		// The client still takes later marks.
		marks.markSeen(client, [{ ref: "b", seenThrough: T }]);
		expect(calls).toHaveLength(2);
	});

	it("stops sending on a client whose hub has no seen/set, and keeps sending on another", async () => {
		const { marks, client, calls } = setup();
		marks.markSeen(client, [{ ref: "a", seenThrough: T }]);
		calls[0].refuse(-32601);
		await settle();
		expect(marks.isSeenOnHub(ended("a", T, true))).toBe(false);
		marks.markSeen(client, [{ ref: "b", seenThrough: T }]);
		marks.flush(client);
		await settle();
		expect(calls).toHaveLength(1);
		// The mark still shows, and goes out over a new connection.
		expect(marks.isSeenOnHub(ended("b", T, true))).toBe(true);
		marks.flush(fakeClient("b", calls));
		expect(calls.map((call) => [call.client, call.sessions])).toEqual([
			["a", [{ ref: "a", seenThrough: T }]],
			["b", [{ ref: "b", seenThrough: T }]],
		]);
		// Remembered per client, across controllers.
		new HubSeenMarks().markSeen(client, [{ ref: "c", seenThrough: T }]);
		expect(calls).toHaveLength(2);
	});

	it("keeps a call's marks when the hub fails for now, and sends them again on the next flush", async () => {
		const { marks, client, calls } = setup();
		marks.markSeen(client, [{ ref: "a", seenThrough: T }]);
		// A store error (-32603) or an unavailable navigation service (-32014)
		// says nothing about the mark itself.
		calls[0].refuse(-32603);
		await settle();
		expect(marks.isSeenOnHub(ended("a", T, true))).toBe(true);
		expect(calls).toHaveLength(1);
		marks.flush(client);
		calls[1].refuse(-32014);
		await settle();
		expect(marks.isSeenOnHub(ended("a", T, true))).toBe(true);
		marks.flush(client);
		expect(calls.map((call) => call.sessions)).toEqual([
			[{ ref: "a", seenThrough: T }],
			[{ ref: "a", seenThrough: T }],
			[{ ref: "a", seenThrough: T }],
		]);
	});

	it("sends over the new connection the marks a replaced connection's hub refused", async () => {
		const { marks, client, calls } = setup();
		marks.markSeen(client, [{ ref: "a", seenThrough: T }]);
		// The connection is replaced while the call is out, and the old hub then
		// answers that it has no seen/set: the new connection's hub may.
		marks.flush(fakeClient("b", calls));
		calls[0].refuse(-32601);
		await settle();
		expect(calls.map((call) => [call.client, call.sessions])).toEqual([
			["a", [{ ref: "a", seenThrough: T }]],
			["b", [{ ref: "a", seenThrough: T }]],
		]);
		expect(marks.isSeenOnHub(ended("a", T, true))).toBe(true);
	});

	it("keeps a mark whose call failed in transit, and sends it again on the next flush", async () => {
		const { marks, client, calls } = setup();
		marks.markSeen(client, [{ ref: "a", seenThrough: T }]);
		calls[0].drop();
		await settle();
		expect(calls).toHaveLength(1);
		expect(marks.isSeenOnHub(ended("a", T, true))).toBe(true);
		const next = fakeClient("b", calls);
		marks.flush(next);
		expect(calls.map((call) => [call.client, call.sessions])).toEqual([
			["a", [{ ref: "a", seenThrough: T }]],
			["b", [{ ref: "a", seenThrough: T }]],
		]);
	});

	it("sends to the new connection a mark made while the old one's call was out", async () => {
		const { marks, client, calls } = setup();
		marks.markSeen(client, [{ ref: "a", seenThrough: T }]);
		// The connection drops and comes back while the call is out.
		marks.flush(null);
		marks.markSeen(null, [{ ref: "b", seenThrough: T }]);
		marks.flush(fakeClient("b", calls));
		calls[0].drop();
		await settle();
		expect(calls.map((call) => [call.client, call.sessions])).toEqual([
			["a", [{ ref: "a", seenThrough: T }]],
			[
				"b",
				[
					{ ref: "a", seenThrough: T },
					{ ref: "b", seenThrough: T },
				],
			],
		]);
	});

	it("sends a mark made right after a flush that found nothing to send", () => {
		const { marks, client, calls } = setup();
		marks.flush(client);
		marks.markSeen(client, [{ ref: "a", seenThrough: T }]);
		expect(calls.map((call) => call.sessions)).toEqual([[{ ref: "a", seenThrough: T }]]);
	});

	it("records marks made with no connection, and sends them once one is ready", () => {
		const { marks, client, calls } = setup();
		marks.markSeen(null, [{ ref: "a", seenThrough: T }]);
		marks.markUnread(null, ["b"]);
		expect(calls).toHaveLength(0);
		expect(marks.isSeenOnHub(ended("a", T, true))).toBe(true);
		marks.flush(client);
		expect(calls.map((call) => call.sessions)).toEqual([
			[
				{ ref: "a", seenThrough: T },
				{ ref: "b", unread: true },
			],
		]);
	});

	it("sends nothing after the connection goes away, even with a call out", async () => {
		const { marks, client, calls } = setup();
		marks.markSeen(client, [{ ref: "a", seenThrough: T }]);
		marks.flush(null);
		marks.markSeen(null, [{ ref: "b", seenThrough: T }]);
		calls[0].answer();
		await settle();
		expect(calls).toHaveLength(1);
	});

	it("puts at most 500 marks in a call, and sends the rest in the next", async () => {
		const { marks, client, calls } = setup();
		const many = Array.from({ length: 501 }, (_, index) => ({ ref: `s${index}`, seenThrough: T }));
		marks.markSeen(client, many);
		expect(calls.map((call) => call.sessions.length)).toEqual([500]);
		calls[0].answer();
		await settle();
		expect(calls.map((call) => call.sessions.length)).toEqual([500, 1]);
		expect(calls[1].sessions).toEqual([{ ref: "s500", seenThrough: T }]);
	});

	it("never sends a mark with no turn end to mark through", () => {
		const { marks, client, calls } = setup();
		marks.markSeen(client, [
			{ ref: "a", seenThrough: Number.NaN },
			{ ref: "b", seenThrough: 0 },
		]);
		expect(calls).toHaveLength(0);
	});
});

describe("one controller per hub", () => {
	it("keeps one controller per hub until the hub is forgotten", () => {
		const first = hubSeenMarks("hub-x");
		expect(hubSeenMarks("hub-x")).toBe(first);
		expect(hubSeenMarks("hub-y")).not.toBe(first);
		forgetHubSeenMarks("hub-x");
		expect(hubSeenMarks("hub-x")).not.toBe(first);
	});
});

describe("the Board's marks", () => {
	it("marks a row read the way opening it does: a hub row through its turn end, any other with the device's marker", () => {
		const { markers, calls, client, seen } = board();
		seen.markRead(client, [ended("hub", T, true)]);
		expect(calls.map((call) => call.sessions)).toEqual([[{ ref: "hub", seenThrough: T }]]);
		expect(seen.isSeen(ended("hub", T, true))).toBe(true);
		expect(markers.isSeen(row("hub"))).toBe(false);
		seen.markRead(client, [row("device")]);
		expect(calls).toHaveLength(1);
		expect(markers.isSeen(row("device"))).toBe(true);
	});

	it("marks rows read and unread, one call for all the hub rows", async () => {
		const { markers, calls, client, seen } = board();
		seen.markRead(client, [ended("h1", T, true), ended("h2", T - 9, true), row("d1")]);
		expect(calls.map((call) => call.sessions)).toEqual([
			[
				{ ref: "h1", seenThrough: T },
				{ ref: "h2", seenThrough: T - 9 },
			],
		]);
		expect(markers.isSeen(row("d1"))).toBe(true);
		calls[0].answer();
		await settle();
		seen.markUnread(client, [ended("h1", T, false), ended("h2", T - 9, false), row("d1")]);
		expect(calls.map((call) => call.sessions).at(-1)).toEqual([
			{ ref: "h1", unread: true },
			{ ref: "h2", unread: true },
		]);
		expect(markers.isSeen(row("d1"))).toBe(false);
		expect(seen.isSeen(ended("h1", T, false))).toBe(false);
	});

	it("marks a row whose turn_ended_at doesn't parse with the device's markers, read and unread", () => {
		const { markers, calls, client, seen } = board();
		const bad = row("bad", { turn_ended_at: "not a time", unseen: true });
		seen.markRead(client, [bad]);
		expect(markers.isSeen(bad)).toBe(true);
		expect(seen.isSeen(bad)).toBe(true);
		seen.markUnread(client, [bad]);
		expect(markers.isSeen(bad)).toBe(false);
		expect(seen.isSeen(bad)).toBe(false);
		expect(calls).toHaveLength(0);
	});

	it("sends nothing when no row is the hub's", () => {
		const { calls, client, seen } = board();
		seen.markRead(client, [row("d1")]);
		seen.markUnread(client, [row("d1")]);
		expect(calls).toHaveLength(0);
	});
});
