// The session screen marks itself seen (S4): once per visit to the front,
// through the loaded snapshot's turn end, on the hub's own controller.
import type { SessionSeenMark } from "@evener/appwire-client";
import { expect, it } from "vitest";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { renderHook } from "../renderNative.testkit";
import { hubSeenMarks } from "./hubSeen";
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
	const view = {
		inFront: true,
		client: client as ConversationClientLike | null,
		conversation: null as { lastTurnEndedAt?: string } | null,
	};
	const hook = renderHook(() =>
		useMarkSeenInFront({ hubId, ref: "local:s" }, view.inFront, view.client, view.conversation),
	);
	return { hubId, sent, view, hook };
}

it("marks the session seen through its turn end once it has loaded in front", () => {
	const { sent, view, hook } = setup();
	expect(sent).toEqual([]);
	view.conversation = { lastTurnEndedAt: iso(T) };
	hook.rerender();
	expect(sent).toEqual([[{ ref: "local:s", seenThrough: T }]]);
	hook.unmount();
});

it("doesn't mark a newer turn that ends while the screen stays in front", () => {
	const { sent, view, hook } = setup();
	view.conversation = { lastTurnEndedAt: iso(T) };
	hook.rerender();
	view.conversation = { lastTurnEndedAt: iso(T + 5_000) };
	hook.rerender();
	expect(sent).toHaveLength(1);
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

it("marks nothing without a turn end, and counts the visit as marked", () => {
	const { sent, view, hook } = setup();
	view.conversation = {};
	hook.rerender();
	expect(sent).toEqual([]);
	// A turn that ends while the screen stays in front is not "opened since".
	view.conversation = { lastTurnEndedAt: iso(T) };
	hook.rerender();
	expect(sent).toEqual([]);
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
