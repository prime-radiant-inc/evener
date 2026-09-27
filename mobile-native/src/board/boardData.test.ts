import { expect, it } from "vitest";
import type {
	AnyNotification,
	NavigationInvalidationTarget,
	NavigationReadParams,
	NavigationReadResponse,
} from "@evener/appwire-client";
import { manifest, wireV2 } from "@evener/appwire-client/testing/navigation";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { type BoardSnapshot, createBoardController } from "./boardData";

function boundary() {
	const requests: Array<{
		params: NavigationReadParams;
		resolve: (value: NavigationReadResponse) => void;
		reject: (error: Error) => void;
		answered: boolean;
	}> = [];
	const listeners = new Set<(event: AnyNotification) => void>();
	const client: ConversationClientLike = {
		request: (_method, params) =>
			new Promise((resolve, reject) => {
				requests.push({
					params: params as NavigationReadParams,
					resolve,
					reject,
					answered: false,
				});
			}),
		onNotification: (listener) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
	};
	return { client, requests, listeners };
}
type Hub = ReturnType<typeof boundary>;
function response(params: NavigationReadParams, data: unknown, revision = 1) {
	return wireV2(
		{
			...params,
			representationVersion: 2,
			offset: params.offset ?? 0,
			limit: params.limit ?? 50,
		},
		data,
		`etag-${params.offset ?? 0}-${revision}`,
		revision,
		"generation-test",
	);
}
const session = (ref: string) => ({
	ref,
	host_id: "local",
	session_id: ref,
	title: ref,
	project: "p",
	state: "idle",
	kind: "session",
	live: true,
	children: [],
});
const sessions = (prefix: string, count: number, from = 0) =>
	Array.from({ length: count }, (_, index) =>
		session(`${prefix}${from + index}`),
	);
const tick = () => new Promise((resolve) => setTimeout(resolve, 0));

type Reader = "live" | "needs_you" | "pin_catalog" | "manifest";
const readerOf = (params: NavigationReadParams): Reader =>
	params.resource === "section"
		? (params.section as Reader)
		: (params.resource as Reader);
/** The oldest unanswered request for one reader. */
function next(hub: Hub, reader: Reader) {
	const request = hub.requests.find(
		(candidate) => !candidate.answered && readerOf(candidate.params) === reader,
	);
	if (!request) throw new Error(`no pending ${reader} request`);
	request.answered = true;
	return request;
}
function answer(hub: Hub, reader: Reader, data: unknown, revision = 1) {
	const request = next(hub, reader);
	request.resolve(response(request.params, data, revision));
	return request;
}
function fail(hub: Hub, reader: Reader, message: string) {
	next(hub, reader).reject(new Error(message));
}
const requestsFor = (hub: Hub, reader: Reader) =>
	hub.requests.filter((request) => readerOf(request.params) === reader);
const sources = [
	{ id: "laptop", label: "Laptop", kind: "local", online: true },
];
async function answerAll(
	hub: Hub,
	{ live = sessions("live-", 2), needsYou = [session("ask-0")] } = {},
) {
	answer(hub, "live", { sessions: live, remaining: 0 });
	answer(hub, "needs_you", { sessions: needsYou, remaining: 0 });
	answer(hub, "pin_catalog", {
		pin_sections: [{ id: "pins-1", name: "Mine", count: 3 }],
		remaining: 0,
	});
	answer(hub, "manifest", manifest({ sources }));
	await tick();
}
function invalidate(
	hub: Hub,
	sequence: number,
	targets: NavigationInvalidationTarget[],
	generationId = "generation-test",
) {
	for (const listener of hub.listeners)
		listener({
			method: "evener/navigation/invalidated",
			params: { generationId, sequence, targets },
		});
}
const refs = (page: { rows: Array<{ ref: string }> }) =>
	page.rows.map((row) => row.ref);

it("reads Live, Needs you, the pin catalog and the manifest when it gets a client", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	await Promise.resolve();
	expect(hub.requests.map((request) => request.params.resource).sort()).toEqual(
		["manifest", "pin_catalog", "section", "section"],
	);
	expect(
		hub.requests
			.filter((request) => request.params.resource === "section")
			.map((request) => request.params.section)
			.sort(),
	).toEqual(["live", "needs_you"]);
});

it("answers become the snapshot", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	expect(board.getSnapshot().loaded).toBe(false);
	await answerAll(hub);
	const snapshot = board.getSnapshot();
	expect(snapshot.loaded).toBe(true);
	expect(snapshot.retained).toBe(false);
	expect(snapshot.error).toBeNull();
	expect(refs(snapshot.live)).toEqual(["live-0", "live-1"]);
	expect(refs(snapshot.needsYou)).toEqual(["ask-0"]);
	expect(snapshot.pins.rows.map((row) => row.id)).toEqual(["pins-1"]);
	expect(snapshot.manifest?.sources).toEqual(sources);
	expect(board.getSnapshot()).toBe(snapshot);
});

it("keeps showing its rows while disconnected and replaces them only when the new connection's reads land", async () => {
	const first = boundary();
	const board = createBoardController();
	board.setClient(first.client);
	await answerAll(first);

	board.setClient(null);
	const offline = board.getSnapshot();
	expect(refs(offline.live)).toEqual(["live-0", "live-1"]);
	expect(refs(offline.needsYou)).toEqual(["ask-0"]);
	expect(offline.manifest?.sources).toEqual(sources);
	expect(offline.loaded).toBe(true);
	expect(offline.retained).toBe(true);
	expect(first.listeners.size).toBe(0);

	const emitted: BoardSnapshot[] = [];
	board.subscribe(() => emitted.push(board.getSnapshot()));
	const second = boundary();
	board.setClient(second.client);
	expect(refs(board.getSnapshot().live)).toEqual(["live-0", "live-1"]);
	answer(second, "live", { sessions: [session("live-9")], remaining: 0 });
	await tick();
	expect(refs(board.getSnapshot().live)).toEqual(["live-9"]);
	expect(refs(board.getSnapshot().needsYou)).toEqual(["ask-0"]);
	expect(board.getSnapshot().retained).toBe(true);

	answer(second, "needs_you", { sessions: [], remaining: 0 });
	answer(second, "manifest", manifest({ sources }));
	await tick();
	expect(board.getSnapshot().retained).toBe(true);
	expect(board.getSnapshot().pins.rows.map((row) => row.id)).toEqual([
		"pins-1",
	]);
	answer(second, "pin_catalog", { pin_sections: [], remaining: 0 });
	await tick();
	expect(emitted.length).toBeGreaterThan(0);
	for (const snapshot of emitted) {
		expect(snapshot.live.rows.length).toBeGreaterThan(0);
		expect(snapshot.loaded).toBe(true);
	}
	const settled = board.getSnapshot();
	expect(settled.retained).toBe(false);
	expect(settled.needsYou.rows).toEqual([]);
	expect(settled.pins.rows).toEqual([]);
});

it("shows an empty Live when the new connection's read returns no rows", async () => {
	const first = boundary();
	const board = createBoardController();
	board.setClient(first.client);
	await answerAll(first);
	board.setClient(null);
	const second = boundary();
	board.setClient(second.client);
	answer(second, "live", { sessions: [], remaining: 0 });
	await tick();
	expect(board.getSnapshot().live.rows).toEqual([]);
});

it("swapping straight to a new client keeps the rows too", async () => {
	const first = boundary();
	const board = createBoardController();
	board.setClient(first.client);
	await answerAll(first);
	const second = boundary();
	board.setClient(second.client);
	expect(first.listeners.size).toBe(0);
	expect(refs(board.getSnapshot().live)).toEqual(["live-0", "live-1"]);
	expect(board.getSnapshot().retained).toBe(true);
});

it("retained rows say what the new connection's read of them is doing", async () => {
	const first = boundary();
	const board = createBoardController();
	board.setClient(first.client);
	await answerAll(first);
	const second = boundary();
	board.setClient(second.client);
	await tick();
	expect(refs(board.getSnapshot().live)).toEqual(["live-0", "live-1"]);
	expect(board.getSnapshot().live).toMatchObject({ loading: true, error: null });
	fail(second, "live", "The hub went away.");
	await tick();
	expect(refs(board.getSnapshot().live)).toEqual(["live-0", "live-1"]);
	expect(board.getSnapshot().live).toMatchObject({ loading: false, error: "The hub went away." });
	expect(board.getSnapshot().retained).toBe(true);
});

it("a disconnected board that never loaded has nothing retained", () => {
	const board = createBoardController();
	board.setClient(null);
	expect(board.getSnapshot().retained).toBe(false);
	expect(board.getSnapshot().loaded).toBe(false);
});

it("reads every Needs you page, up to 200 rows", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	answer(hub, "needs_you", { sessions: sessions("ask-", 50), remaining: 30 });
	await tick();
	const second = next(hub, "needs_you");
	expect(second.params.offset).toBe(50);
	second.resolve(
		response(second.params, {
			sessions: sessions("ask-", 30, 50),
			remaining: 0,
		}),
	);
	await tick();
	expect(board.getSnapshot().needsYou.rows).toHaveLength(80);
	expect(requestsFor(hub, "needs_you")).toHaveLength(2);
});

it("reads Needs you until the hub has no more rows, past 200", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	for (let page = 0; page < 5; page++) {
		const request = next(hub, "needs_you");
		expect(request.params.offset ?? 0).toBe(page * 50);
		request.resolve(
			response(request.params, {
				sessions: sessions("ask-", 50, page * 50),
				remaining: 250 - (page + 1) * 50,
			}),
		);
		await tick();
	}
	expect(board.getSnapshot().needsYou.rows).toHaveLength(250);
	expect(() => next(hub, "needs_you")).toThrow("no pending needs_you request");
});

it("loads the next Live page on request", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	answer(hub, "live", { sessions: sessions("live-", 50), remaining: 20 });
	await tick();
	expect(() => next(hub, "live")).toThrow("no pending live request");
	const loading = board.loadMoreLive();
	const more = next(hub, "live");
	expect(more.params.offset).toBe(50);
	more.resolve(
		response(more.params, {
			sessions: sessions("live-", 20, 50),
			remaining: 0,
		}),
	);
	await loading;
	expect(board.getSnapshot().live.rows).toHaveLength(70);
});

it("re-reads the manifest when the hub invalidates it", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	await answerAll(hub);
	expect(requestsFor(hub, "manifest")).toHaveLength(1);
	invalidate(hub, 1, [{ kind: "manifest", revision: 2 }]);
	expect(requestsFor(hub, "manifest")).toHaveLength(2);
	invalidate(hub, 2, [{ kind: "manifest", revision: 2 }]);
	expect(requestsFor(hub, "manifest")).toHaveLength(2);
	const offline = [{ ...sources[0], online: false }];
	answer(hub, "manifest", manifest({ sources: offline }), 2);
	await tick();
	expect(requestsFor(hub, "manifest")).toHaveLength(2);
	expect(board.getSnapshot().manifest?.sources).toEqual(offline);
});

it("re-reads the manifest again when an invalidation names a newer revision than the read returned", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	await answerAll(hub);
	invalidate(hub, 1, [{ kind: "manifest", revision: 2 }]);
	invalidate(hub, 2, [{ kind: "manifest", revision: 3 }]);
	answer(hub, "manifest", manifest({ sources }), 2);
	await tick();
	expect(requestsFor(hub, "manifest")).toHaveLength(3);
});

it("a revision-less manifest invalidation earns one follow-up read, and later numbered ones still coalesce", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	await answerAll(hub);
	invalidate(hub, 1, [{ kind: "manifest" }]);
	expect(requestsFor(hub, "manifest")).toHaveLength(2);
	answer(hub, "manifest", manifest({ sources }), 2);
	await tick();
	expect(requestsFor(hub, "manifest")).toHaveLength(2);
	invalidate(hub, 2, [{ kind: "manifest", revision: 3 }]);
	invalidate(hub, 3, [{ kind: "manifest", revision: 3 }]);
	expect(requestsFor(hub, "manifest")).toHaveLength(3);
	answer(hub, "manifest", manifest({ sources }), 3);
	await tick();
	expect(requestsFor(hub, "manifest")).toHaveLength(3);
});

it("a revision-less invalidation during a read earns a follow-up read", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	await answerAll(hub);
	invalidate(hub, 1, [{ kind: "manifest", revision: 2 }]);
	invalidate(hub, 2, [{ kind: "manifest" }]);
	answer(hub, "manifest", manifest({ sources }), 2);
	await tick();
	expect(requestsFor(hub, "manifest")).toHaveLength(3);
	// A numbered invalidation during the follow-up read is covered once that
	// read returns its revision: the revision-less one pinned nothing.
	invalidate(hub, 3, [{ kind: "manifest", revision: 3 }]);
	answer(hub, "manifest", manifest({ sources }), 3);
	await tick();
	expect(requestsFor(hub, "manifest")).toHaveLength(3);
});

it("a manifest read that never answers doesn't strand the Board after pause and resume", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	answer(hub, "live", { sessions: [session("live-0")], remaining: 0 });
	await tick();
	const stranded = next(hub, "manifest");
	board.pause();
	board.resume();
	await Promise.resolve();
	expect(requestsFor(hub, "manifest")).toHaveLength(2);
	answer(hub, "manifest", manifest({ sources }));
	await tick();
	expect(board.getSnapshot().manifest?.sources).toEqual(sources);
	stranded.resolve(
		response(stranded.params, manifest({ sources: [] })),
	);
	await tick();
	expect(board.getSnapshot().manifest?.sources).toEqual(sources);
});

it("pausing an idle, loaded Board notifies no listener", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	await answerAll(hub);
	let notified = 0;
	board.subscribe(() => notified++);
	const before = board.getSnapshot();
	board.pause();
	expect(notified).toBe(0);
	expect(board.getSnapshot()).toBe(before);
});

it("a client given while paused notifies nothing beyond the connection itself", () => {
	const hub = boundary();
	const board = createBoardController();
	board.pause();
	let notified = 0;
	board.subscribe(() => notified++);
	const before = board.getSnapshot();
	board.setClient(hub.client);
	expect(notified).toBe(0);
	expect(board.getSnapshot()).toBe(before);
});

it("ignores invalidations that don't name the manifest", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	await answerAll(hub);
	invalidate(hub, 1, [{ kind: "pin_catalog", revision: 2 }]);
	expect(requestsFor(hub, "manifest")).toHaveLength(1);
});

it("re-reads the manifest after a missed invalidation, whatever the next one names", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	await answerAll(hub);
	invalidate(hub, 1, [{ kind: "pin_catalog", revision: 2 }]);
	expect(requestsFor(hub, "manifest")).toHaveLength(1);
	// Sequence 2 never arrived, and it may have named the manifest.
	invalidate(hub, 3, [{ kind: "pin_catalog", revision: 3 }]);
	expect(requestsFor(hub, "manifest")).toHaveLength(2);
	answer(hub, "manifest", manifest({ sources }), 1);
	await tick();
	// A repeat of a sequence already seen is a duplicate, even one that
	// names the manifest.
	invalidate(hub, 3, [{ kind: "manifest", revision: 2 }]);
	expect(requestsFor(hub, "manifest")).toHaveLength(2);
});

it("a missed invalidation during a manifest read earns a follow-up read", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	await answerAll(hub);
	invalidate(hub, 1, [{ kind: "manifest", revision: 2 }]);
	expect(requestsFor(hub, "manifest")).toHaveLength(2);
	// Sequence 2 is lost while that read is out; the read can't cover it.
	invalidate(hub, 3, [{ kind: "pin_catalog", revision: 3 }]);
	answer(hub, "manifest", manifest({ sources }), 2);
	await tick();
	expect(requestsFor(hub, "manifest")).toHaveLength(3);
});

it("numbers sequences afresh when the hub's generation changes", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	await answerAll(hub);
	invalidate(hub, 1, [{ kind: "pin_catalog", revision: 2 }]);
	invalidate(hub, 2, [{ kind: "pin_catalog", revision: 3 }]);
	expect(requestsFor(hub, "manifest")).toHaveLength(1);
	// A restarted hub counts from 1 again: news, not a repeat.
	invalidate(hub, 1, [{ kind: "manifest", revision: 1 }], "generation-restarted");
	expect(requestsFor(hub, "manifest")).toHaveLength(2);
});

it("an approval that resolves leaves Needs you", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	await answerAll(hub, { needsYou: [session("ask-0"), session("ask-1")] });
	invalidate(hub, 1, [{ kind: "section", section: "needs_you", revision: 2 }]);
	answer(hub, "needs_you", { sessions: [session("ask-1")], remaining: 0 }, 2);
	await tick();
	expect(refs(board.getSnapshot().needsYou)).toEqual(["ask-1"]);
});

it("a failed first read reports its error and stays unloaded", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	fail(hub, "live", "The hub went away.");
	await tick();
	expect(board.getSnapshot().error).toBe("The hub went away.");
	expect(board.getSnapshot().loaded).toBe(false);
});

it("a failed manifest read reports its error", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	fail(hub, "manifest", "No manifest.");
	await tick();
	expect(board.getSnapshot().error).toBe("No manifest.");
	expect(board.getSnapshot().manifest).toBeNull();
});

it("a failed later read keeps the rows", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	await answerAll(hub);
	invalidate(hub, 1, [
		{ kind: "section", section: "live", revision: 2 },
		{ kind: "manifest", revision: 2 },
	]);
	fail(hub, "live", "The hub went away.");
	fail(hub, "manifest", "The hub went away.");
	await tick();
	const snapshot = board.getSnapshot();
	expect(snapshot.error).toBe("The hub went away.");
	expect(snapshot.loaded).toBe(true);
	expect(refs(snapshot.live)).toEqual(["live-0", "live-1"]);
	expect(snapshot.manifest?.sources).toEqual(sources);
});

it("pause holds re-reads, resume catches up", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	await answerAll(hub);
	const before = hub.requests.length;
	board.pause();
	invalidate(hub, 1, [
		{ kind: "section", section: "live", revision: 2 },
		{ kind: "section", section: "needs_you", revision: 2 },
		{ kind: "pin_catalog", revision: 2 },
		{ kind: "manifest", revision: 2 },
	]);
	await tick();
	expect(hub.requests.length).toBe(before);
	board.resume();
	await Promise.resolve();
	expect(
		hub.requests
			.slice(before)
			.map((request) => readerOf(request.params))
			.sort(),
	).toEqual(["live", "manifest", "needs_you", "pin_catalog"]);
});

it("resume reads what a pause interrupted before its first answer", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	board.pause();
	const before = hub.requests.length;
	board.resume();
	await Promise.resolve();
	expect(
		hub.requests
			.slice(before)
			.map((request) => readerOf(request.params))
			.sort(),
	).toEqual(["live", "manifest", "needs_you", "pin_catalog"]);
	const interrupted = hub.requests.slice(0, before);
	for (const request of interrupted) request.answered = true;
	answer(hub, "live", { sessions: [session("live-0")], remaining: 0 });
	await tick();
	expect(refs(board.getSnapshot().live)).toEqual(["live-0"]);
});

it("a client given while paused waits for resume to read", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.pause();
	board.setClient(hub.client);
	await Promise.resolve();
	expect(hub.requests).toHaveLength(0);
	board.resume();
	await Promise.resolve();
	expect(hub.requests).toHaveLength(4);
});

it("dispose leaves no listeners", async () => {
	const hub = boundary();
	const board = createBoardController();
	let notified = 0;
	board.subscribe(() => notified++);
	board.setClient(hub.client);
	await answerAll(hub);
	expect(hub.listeners.size).toBe(4);
	board.dispose();
	expect(hub.listeners.size).toBe(0);
	const after = notified;
	invalidate(hub, 1, [{ kind: "manifest", revision: 2 }]);
	await tick();
	expect(notified).toBe(after);
});
