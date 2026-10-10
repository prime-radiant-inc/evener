import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
// The session screen marks itself seen (S4) while it is in front: through
// the loaded snapshot's turn end, and through the fleet's row for it when a
// turn ends while you watch (Jesse's ruling, 2026-09-29).
import { type NavigationSessionSummary, type SessionSeenMark, WireError } from "@evener/appwire-client";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { renderHook, settleMicrotasks } from "../renderNative.testkit";
import { fleetSession } from "../session/fleetTestUtils";
import { hubSeenMarks } from "./hubSeen";
import { seenMarkers, useBoardSeen } from "./nativeBoardMemory";
import { useMarkSeenInFront } from "./sessionSeen";

// The device's own markers, in memory.
const kv = vi.hoisted(() => new Map<string, string>());
vi.mock("expo-sqlite/kv-store", () => ({
	Storage: {
		getItemSync: (key: string) => kv.get(key) ?? null,
		setItemSync: (key: string, value: string) => kv.set(key, value),
		removeItemSync: (key: string) => kv.delete(key),
	},
}));

const T = Date.UTC(2026, 8, 26, 12, 0);
const iso = (ms: number) => new Date(ms).toISOString();

let hubCount = 0;
/** `refuse` makes the hub answer every mark with invalid params, a refusal
 * for good. `fail` makes the next call fail for now, as a dropped connection
 * does. */
function setup({ refuse = false, fail = false } = {}) {
	let failNext = fail;
	hubCount += 1;
	const hubId = `session-seen-hub-${hubCount}`;
	const sent: SessionSeenMark[][] = [];
	const client: ConversationClientLike = Object.assign(new FakeClient("ready"), {
		request: (method, params) => {
			if (method !== "evener/session/seen/set") throw new Error(`unexpected ${method}`);
			sent.push((params as { sessions: SessionSeenMark[] }).sessions);
			if (refuse) return Promise.reject(new WireError("invalid params", -32602));
			if (failNext) {
				failNext = false;
				return Promise.reject(new Error("connection closed"));
			}
			return Promise.resolve({ ok: true, changed: true, navigation: { generation_id: "g", targets: [] } } as never);
		},
		onNotification: () => () => {},
	} as Omit<ConversationClientLike, "state" | "onReady" | "onStateChange">);
	// The device's own markers, first run a minute before T.
	const markers = seenMarkers(hubId);
	markers.adoptEpoch([{ updated_at: iso(T - 60_000) }]);
	const view = {
		hubId,
		inFront: true,
		client: client as ConversationClientLike | null,
		conversation: null as { lastTurnEndedAt?: string } | null,
		/** The fleet's row for this session, once the fleet has read it. */
		row: undefined as NavigationSessionSummary | undefined,
		/** The session's last motion, from the screen's activity read. */
		lastMovedAt: undefined as number | undefined,
	};
	const hook = renderHook(() =>
		useMarkSeenInFront(
			{ hubId: view.hubId, ref: "local:s" },
			view.inFront,
			view.client,
			view.conversation,
			view.row,
			useBoardSeen(view.hubId),
			view.lastMovedAt,
		),
	);
	return { hubId, sent, view, hook, client, markers };
}

const fleetRow = (over: Partial<NavigationSessionSummary>) => fleetSession("local:s", over);

it("marks the session seen through its turn end once it has loaded in front", () => {
	const { sent, view, hook } = setup();
	expect(sent).toEqual([]);
	view.conversation = { lastTurnEndedAt: iso(T) };
	hook.rerender();
	expect(sent).toEqual([[{ ref: "local:s", seenThrough: T }]]);
	hook.unmount();
});

it("marks a turn that ends while the screen stays in front through the hub row's new turn end", async () => {
	const { sent, view, hook } = setup();
	view.conversation = { lastTurnEndedAt: iso(T) };
	view.row = fleetRow({ turn_ended_at: iso(T), updated_at: iso(T), unseen: false });
	hook.rerender();
	await settleMicrotasks();
	// The turn ends, and the fleet's next read shows the hub's stamp for it.
	view.row = fleetRow({ turn_ended_at: iso(T + 5_000), updated_at: iso(T + 5_000), unseen: true });
	hook.rerender();
	expect(sent).toEqual([[{ ref: "local:s", seenThrough: T }], [{ ref: "local:s", seenThrough: T + 5_000 }]]);
	hook.unmount();
});

it("marks through a newer turn end a re-read of the session brings while in front", async () => {
	const { sent, view, hook } = setup();
	view.conversation = { lastTurnEndedAt: iso(T) };
	hook.rerender();
	await settleMicrotasks();
	view.conversation = { lastTurnEndedAt: iso(T + 5_000) };
	hook.rerender();
	expect(sent).toEqual([[{ ref: "local:s", seenThrough: T }], [{ ref: "local:s", seenThrough: T + 5_000 }]]);
	hook.unmount();
});

it("sends a refused mark once, however often the hub's refusal re-renders the screen", async () => {
	const { sent, view, hook } = setup({ refuse: true });
	view.conversation = {};
	view.row = fleetRow({ turn_ended_at: iso(T), updated_at: iso(T), unseen: true });
	hook.rerender();
	await settleMicrotasks();
	hook.rerender();
	expect(sent).toEqual([[{ ref: "local:s", seenThrough: T }]]);
	hook.unmount();
});

it("sends a mark once across rerenders with the same row and snapshot", async () => {
	const { hubId, sent, view, hook } = setup();
	view.conversation = { lastTurnEndedAt: iso(T) };
	view.row = fleetRow({ turn_ended_at: iso(T), updated_at: iso(T), unseen: true });
	hook.rerender();
	await settleMicrotasks();
	// The hub's rows show the mark landed, so nothing is pending any more.
	act(() => hubSeenMarks(hubId).prune([fleetRow({ turn_ended_at: iso(T), unseen: false })]));
	hook.rerender();
	hook.rerender();
	expect(sent).toEqual([[{ ref: "local:s", seenThrough: T }]]);
	hook.unmount();
});

it("leaves an unread another device marked at the turn end it already marked", async () => {
	const { hubId, sent, view, hook } = setup();
	view.conversation = {};
	view.row = fleetRow({ turn_ended_at: iso(T), updated_at: iso(T), unseen: true });
	hook.rerender();
	await settleMicrotasks();
	act(() => hubSeenMarks(hubId).prune([fleetRow({ turn_ended_at: iso(T), unseen: false })]));
	// Mark as unread elsewhere: the hub reads unseen at the same turn end.
	view.row = fleetRow({ turn_ended_at: iso(T), updated_at: iso(T), unseen: true });
	hook.rerender();
	expect(sent).toEqual([[{ ref: "local:s", seenThrough: T }]]);
	// A turn that then ends while you watch is marked.
	view.row = fleetRow({ turn_ended_at: iso(T + 5_000), updated_at: iso(T + 5_000), unseen: true });
	hook.rerender();
	expect(sent).toEqual([[{ ref: "local:s", seenThrough: T }], [{ ref: "local:s", seenThrough: T + 5_000 }]]);
	hook.unmount();
});

it("doesn't mark a turn that ends while another screen is in front", async () => {
	const { sent, view, hook } = setup();
	view.conversation = { lastTurnEndedAt: iso(T) };
	view.row = fleetRow({ turn_ended_at: iso(T), updated_at: iso(T), unseen: false });
	hook.rerender();
	await settleMicrotasks();
	view.inFront = false;
	hook.rerender();
	view.row = fleetRow({ turn_ended_at: iso(T + 5_000), updated_at: iso(T + 5_000), unseen: true });
	hook.rerender();
	expect(sent).toEqual([[{ ref: "local:s", seenThrough: T }]]);
	hook.unmount();
});

it("marks a row the device decides on the device when it changes in front", () => {
	const { sent, view, hook, markers } = setup();
	view.conversation = {};
	const row = fleetRow({ updated_at: iso(T + 5_000) });
	expect(markers.isSeen(row)).toBe(false);
	view.row = row;
	hook.rerender();
	expect(markers.isSeen(row)).toBe(true);
	expect(sent).toEqual([]);
	hook.unmount();
});

it("marks a row with an unreadable turn end again when its updated_at moves in front", () => {
	const { view, hook, markers } = setup();
	view.conversation = {};
	view.row = fleetRow({ turn_ended_at: "not a time", updated_at: iso(T + 5_000) });
	hook.rerender();
	expect(markers.isSeen(view.row)).toBe(true);
	// The device decides this row by its updated_at, so a newer one is a new
	// thing to mark even though the unreadable stamp didn't change.
	const newer = fleetRow({ turn_ended_at: "not a time", updated_at: iso(T + 10_000) });
	expect(markers.isSeen(newer)).toBe(false);
	view.row = newer;
	hook.rerender();
	expect(markers.isSeen(newer)).toBe(true);
	hook.unmount();
});

it("leaves a row the device decides unmarked while another screen is in front", () => {
	const { view, hook, markers } = setup();
	view.inFront = false;
	const row = fleetRow({ updated_at: iso(T + 5_000) });
	view.row = row;
	hook.rerender();
	expect(markers.isSeen(row)).toBe(false);
	hook.unmount();
});

it("marks again after the screen leaves the front and comes back", async () => {
	const { sent, view, hook } = setup();
	view.conversation = { lastTurnEndedAt: iso(T) };
	hook.rerender();
	// The first call is answered before the next visit.
	await settleMicrotasks();
	view.conversation = { lastTurnEndedAt: iso(T + 5_000) };
	view.inFront = false;
	hook.rerender();
	expect(sent).toHaveLength(1);
	view.inFront = true;
	hook.rerender();
	expect(sent).toEqual([[{ ref: "local:s", seenThrough: T }], [{ ref: "local:s", seenThrough: T + 5_000 }]]);
	hook.unmount();
});

it("marks a row again at the same turn end on a new visit to the front", async () => {
	const { hubId, sent, view, hook } = setup();
	view.conversation = {};
	view.row = fleetRow({ turn_ended_at: iso(T), updated_at: iso(T), unseen: true });
	hook.rerender();
	await settleMicrotasks();
	act(() => hubSeenMarks(hubId).prune([fleetRow({ turn_ended_at: iso(T), unseen: false })]));
	view.inFront = false;
	hook.rerender();
	// Mark as unread elsewhere while another screen is in front.
	view.row = fleetRow({ turn_ended_at: iso(T), updated_at: iso(T), unseen: true });
	hook.rerender();
	expect(sent).toHaveLength(1);
	view.inFront = true;
	hook.rerender();
	expect(sent).toEqual([[{ ref: "local:s", seenThrough: T }], [{ ref: "local:s", seenThrough: T }]]);
	hook.unmount();
});

it("marks nothing without a turn end, then marks the first turn that ends in front", () => {
	const { sent, view, hook } = setup();
	view.conversation = {};
	hook.rerender();
	expect(sent).toEqual([]);
	view.conversation = { lastTurnEndedAt: iso(T) };
	hook.rerender();
	expect(sent).toEqual([[{ ref: "local:s", seenThrough: T }]]);
	hook.unmount();
});

it("marks nothing out of the front", () => {
	const { sent, view, hook } = setup();
	view.inFront = false;
	view.conversation = { lastTurnEndedAt: iso(T) };
	hook.rerender();
	expect(sent).toEqual([]);
	hook.unmount();
});

it("keeps the mark for the next ready connection while there is none", () => {
	const { hubId, sent, view, hook } = setup();
	view.client = null;
	view.conversation = { lastTurnEndedAt: iso(T) };
	hook.rerender();
	expect(sent).toEqual([]);
	expect(hubSeenMarks(hubId).isSeenOnHub({ ref: "local:s", turn_ended_at: iso(T), unseen: true })).toBe(true);
	hook.unmount();
});

it("keeps a mark whose call failed for now pending, and sends it on the next flush", async () => {
	const { hubId, sent, view, hook, client } = setup({ fail: true });
	view.conversation = { lastTurnEndedAt: iso(T) };
	hook.rerender();
	await settleMicrotasks();
	expect(sent).toEqual([[{ ref: "local:s", seenThrough: T }]]);
	expect(hubSeenMarks(hubId).isSeenOnHub({ ref: "local:s", turn_ended_at: iso(T), unseen: true })).toBe(true);
	// The connection comes back: the screen's next ready client flushes.
	view.client = null;
	hook.rerender();
	view.client = client;
	hook.rerender();
	expect(sent).toEqual([[{ ref: "local:s", seenThrough: T }], [{ ref: "local:s", seenThrough: T }]]);
	hook.unmount();
});

it("sends a mark it queued without a client once one arrives, with no Board to flush it", () => {
	const { sent, view, hook, client } = setup();
	view.client = null;
	view.conversation = { lastTurnEndedAt: iso(T) };
	hook.rerender();
	expect(sent).toEqual([]);
	view.client = client;
	hook.rerender();
	expect(sent).toEqual([[{ ref: "local:s", seenThrough: T }]]);
	hook.unmount();
});

it("marks the same session ref and turn end again when the screen's hub changes", async () => {
	const { sent, view, hook } = setup();
	view.row = fleetRow({ turn_ended_at: iso(T), unseen: true });
	hook.rerender();
	await settleMicrotasks();
	expect(sent).toEqual([[{ ref: "local:s", seenThrough: T }]]);
	hubCount += 1;
	view.hubId = `session-seen-hub-${hubCount}`;
	hook.rerender();
	await settleMicrotasks();
	expect(sent).toEqual([[{ ref: "local:s", seenThrough: T }], [{ ref: "local:s", seenThrough: T }]]);
	hook.unmount();
});

it("doesn't mark the fleet row again for a turn end the snapshot already marked in this stay", async () => {
	const { sent, view, hook, hubId } = setup();
	view.conversation = { lastTurnEndedAt: iso(T) };
	hook.rerender();
	await settleMicrotasks();
	expect(sent).toHaveLength(1);
	// The hub took the mark, and its row caught up.
	act(() => hubSeenMarks(hubId).prune([fleetRow({ turn_ended_at: iso(T), unseen: false })]));
	// Another device then marks it unread, and only now does the fleet row arrive.
	view.row = fleetRow({ turn_ended_at: iso(T), unseen: true });
	hook.rerender();
	await settleMicrotasks();
	expect(sent).toHaveLength(1);
	hook.unmount();
});

// Output streaming while you watch is seen too: the in-front mark follows the
// session's last motion as each activity read moves it, sending only marks
// that advance, and only to a hub that tracks seen-through marks.
it("marks a session in front seen through its last motion as it moves", async () => {
	const { sent, view, hook } = setup();
	view.row = fleetRow({ state: "active", seen_through: iso(T - 60_000) });
	view.lastMovedAt = T;
	hook.rerender();
	await settleMicrotasks();
	view.lastMovedAt = T + 10_000;
	hook.rerender();
	await settleMicrotasks();
	hook.rerender();
	await settleMicrotasks();
	expect(sent).toEqual([[{ ref: "local:s", seenThrough: T }], [{ ref: "local:s", seenThrough: T + 10_000 }]]);
	hook.unmount();
});

it("sends no motion mark to a hub without seen-through marks", async () => {
	const { sent, view, hook } = setup();
	view.row = fleetRow({ state: "active" });
	view.lastMovedAt = T;
	hook.rerender();
	await settleMicrotasks();
	expect(sent).toEqual([]);
	hook.unmount();
});
