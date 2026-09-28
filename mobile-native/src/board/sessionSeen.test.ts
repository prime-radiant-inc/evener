// The session screen marks itself seen (S4) while it is in front: through
// the loaded snapshot's turn end, and through the fleet's row for it when a
// turn ends while you watch (Jesse's ruling, 2026-09-29).
import type { NavigationSessionSummary, SessionSeenMark } from "@evener/appwire-client";
import { expect, it } from "vitest";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { renderHook } from "../renderNative.testkit";
import { SeenMarkers } from "./boardMemory";
import { BoardSeen, hubSeenMarks } from "./hubSeen";
import { useMarkSeenInFront } from "./sessionSeen";

const T = Date.UTC(2026, 8, 26, 12, 0);
const iso = (ms: number) => new Date(ms).toISOString();

let hubCount = 0;
function setup() {
	hubCount += 1;
	const hubId = `session-seen-hub-${hubCount}`;
	const sent: SessionSeenMark[][] = [];
	const client: ConversationClientLike = {
		request: (method, params) => {
			if (method !== "evener/session/seen/set") throw new Error(`unexpected ${method}`);
			sent.push((params as { sessions: SessionSeenMark[] }).sessions);
			return Promise.resolve({ ok: true, changed: true, navigation: { generation_id: "g", targets: [] } } as never);
		},
		onNotification: () => () => {},
	};
	// The device's own markers, first run a minute before T.
	const values = new Map<string, string>();
	const markers = new SeenMarkers(
		{
			getItemSync: (key) => values.get(key) ?? null,
			setItemSync: (key, value) => void values.set(key, value),
			removeItemSync: (key) => void values.delete(key),
		},
		hubId,
	);
	markers.adoptEpoch([{ updated_at: iso(T - 60_000) }]);
	const seen = new BoardSeen(markers, hubSeenMarks(hubId));
	const view = {
		inFront: true,
		client: client as ConversationClientLike | null,
		conversation: null as { lastTurnEndedAt?: string } | null,
		/** The fleet's row for this session, once the fleet has read it. */
		row: undefined as NavigationSessionSummary | undefined,
	};
	const hook = renderHook(() =>
		useMarkSeenInFront({ hubId, ref: "local:s" }, view.inFront, view.client, view.conversation, view.row, seen),
	);
	return { hubId, sent, view, hook, client, markers };
}

const fleetRow = (over: Partial<NavigationSessionSummary>): NavigationSessionSummary => ({
	ref: "local:s",
	host_id: "local",
	session_id: "s",
	title: "s",
	project: "evener",
	state: "idle",
	kind: "session",
	live: true,
	children: [],
	...over,
});

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
	for (let step = 0; step < 10; step++) await Promise.resolve();
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
	for (let step = 0; step < 10; step++) await Promise.resolve();
	view.conversation = { lastTurnEndedAt: iso(T + 5_000) };
	hook.rerender();
	expect(sent).toEqual([[{ ref: "local:s", seenThrough: T }], [{ ref: "local:s", seenThrough: T + 5_000 }]]);
	hook.unmount();
});

it("doesn't mark a turn that ends while another screen is in front", async () => {
	const { sent, view, hook } = setup();
	view.conversation = { lastTurnEndedAt: iso(T) };
	view.row = fleetRow({ turn_ended_at: iso(T), updated_at: iso(T), unseen: false });
	hook.rerender();
	for (let step = 0; step < 10; step++) await Promise.resolve();
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
	for (let step = 0; step < 10; step++) await Promise.resolve();
	view.conversation = { lastTurnEndedAt: iso(T + 5_000) };
	view.inFront = false;
	hook.rerender();
	expect(sent).toHaveLength(1);
	view.inFront = true;
	hook.rerender();
	expect(sent).toEqual([[{ ref: "local:s", seenThrough: T }], [{ ref: "local:s", seenThrough: T + 5_000 }]]);
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
