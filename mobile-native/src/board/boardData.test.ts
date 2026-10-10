import { describe, expect, it } from "vitest";
import { manifest } from "@evener/appwire-client/testing/navigation";
import { type BoardSnapshot, createBoardController } from "./boardData";
import {
	answer,
	answerNotices,
	boundary,
	fail,
	type Hub,
	hubNotice,
	invalidate,
	next,
	noticesChanged,
	noticesMethodNotFound,
	readerOf,
	requestsFor,
	response,
	session,
	sessions,
	tick,
} from "./navigationHubTestUtils";

const sources = [{ id: "laptop", label: "Laptop", kind: "local", online: true }];
async function answerAll(hub: Hub, { live = sessions("live-", 2), needsYou = [session("ask-0")] } = {}) {
	answer(hub, "live", { sessions: live, remaining: 0 });
	answer(hub, "needs_you", { sessions: needsYou, remaining: 0 });
	answer(hub, "pin_catalog", {
		pin_sections: [{ id: "pins-1", name: "Mine", count: 3 }],
		remaining: 0,
	});
	answer(hub, "manifest", manifest({ sources }));
	await tick();
	// The catalog's one category is read once the catalog lands.
	answer(hub, "pin_section:pins-1", { sessions: [session("pinned-0")], remaining: 0 });
	await tick();
}
const refs = (page: { rows: Array<{ ref: string }> }) => page.rows.map((row) => row.ref);

it("reads Live, Needs you, the pin catalog and the manifest when it gets a client", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	await Promise.resolve();
	const reads = hub.requests.filter((request) => request.method === "evener/navigation/read");
	expect(reads.map((request) => request.params.resource).sort()).toEqual([
		"manifest",
		"pin_catalog",
		"section",
		"section",
	]);
	expect(
		reads
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
	expect(board.getSnapshot().pins.rows.map((row) => row.id)).toEqual(["pins-1"]);
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

it("reads every page of the pin catalog, so every category has its row", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	const categories = (count: number, from = 0) =>
		Array.from({ length: count }, (_, index) => ({
			id: `pins-${from + index}`,
			name: `Pins ${from + index}`,
			count: 1,
		}));
	answer(hub, "pin_catalog", { pin_sections: categories(100), remaining: 20 });
	await tick();
	const second = next(hub, "pin_catalog");
	expect(second.params.offset).toBe(100);
	second.resolve(response(second.params, { pin_sections: categories(20, 100), remaining: 0 }));
	await tick();
	expect(board.getSnapshot().pins.rows).toHaveLength(120);
	expect(requestsFor(hub, "pin_catalog")).toHaveLength(2);
});

it("resumes paging the pin catalog where a pause interrupted it", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	const categories = (count: number, from = 0) =>
		Array.from({ length: count }, (_, index) => ({
			id: `pins-${from + index}`,
			name: `Pins ${from + index}`,
			count: 1,
		}));
	answer(hub, "pin_catalog", { pin_sections: categories(100), remaining: 20 });
	await tick();
	// The second page is out when the Board pauses, which cancels it.
	next(hub, "pin_catalog");
	board.pause();
	board.resume();
	await Promise.resolve();
	const retry = next(hub, "pin_catalog");
	expect(retry.params.offset).toBe(100);
	retry.resolve(response(retry.params, { pin_sections: categories(20, 100), remaining: 0 }));
	await tick();
	expect(board.getSnapshot().pins.rows).toHaveLength(120);
});

it("reads a Needs you page that failed again when the Board resumes", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	answer(hub, "needs_you", { sessions: sessions("ask-", 50), remaining: 30 });
	await tick();
	fail(hub, "needs_you", "The hub went away.");
	await tick();
	// A failed page isn't retried hot.
	expect(requestsFor(hub, "needs_you")).toHaveLength(2);
	board.pause();
	board.resume();
	const retry = next(hub, "needs_you");
	expect(retry.params.offset).toBe(50);
	retry.resolve(response(retry.params, { sessions: sessions("ask-", 30, 50), remaining: 0 }));
	await tick();
	expect(board.getSnapshot().needsYou.rows).toHaveLength(80);
	// The retried page is live again: the hub's next change is read.
	invalidate(hub, 1, [{ kind: "section", section: "needs_you", revision: 2 }]);
	await tick();
	expect(requestsFor(hub, "needs_you")).toHaveLength(4);
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

it("reads no more Live while paused", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	answer(hub, "live", { sessions: sessions("live-", 50), remaining: 20 });
	await tick();
	board.pause();
	await board.loadMoreLive();
	expect(requestsFor(hub, "live")).toHaveLength(1);
	board.resume();
	void board.loadMoreLive();
	expect(requestsFor(hub, "live")).toHaveLength(2);
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
	stranded.resolve(response(stranded.params, manifest({ sources: [] })));
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

it("says it's reading while any read is out, the manifest's included", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	expect(board.getSnapshot().reading).toBe(true);
	answer(hub, "live", { sessions: sessions("live-", 2), remaining: 0 });
	answer(hub, "needs_you", { sessions: [], remaining: 0 });
	answer(hub, "pin_catalog", { pin_sections: [], remaining: 0 });
	await tick();
	expect(board.getSnapshot().reading).toBe(true);
	answer(hub, "manifest", manifest({ sources }));
	await tick();
	expect(board.getSnapshot().reading).toBe(false);
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

it("reads a manifest whose re-read failed again when the Board resumes", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	await answerAll(hub);
	invalidate(hub, 1, [{ kind: "manifest", revision: 2 }]);
	fail(hub, "manifest", "The hub went away.");
	await tick();
	expect(board.getSnapshot().manifest?.sources).toEqual(sources);
	expect(requestsFor(hub, "manifest")).toHaveLength(2);
	board.pause();
	board.resume();
	expect(requestsFor(hub, "manifest")).toHaveLength(3);
	const moved = [...sources, { id: "studio", label: "Studio Mac", kind: "ssh", online: true }];
	answer(hub, "manifest", manifest({ sources: moved }), 2);
	await tick();
	expect(board.getSnapshot().manifest?.sources).toEqual(moved);
	expect(board.getSnapshot().error).toBeNull();
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
	expect(hub.requests.slice(before).map(readerOf).sort()).toEqual([
		"live",
		"manifest",
		"needs_you",
		"notices",
		"pin_catalog",
	]);
});

it("resume reads what a pause interrupted before its first answer", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	board.pause();
	const before = hub.requests.length;
	board.resume();
	await Promise.resolve();
	expect(hub.requests.slice(before).map(readerOf).sort()).toEqual([
		"live",
		"manifest",
		"needs_you",
		"notices",
		"pin_catalog",
	]);
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
	// Live, Needs you, the pin catalog, the manifest and the notices.
	expect(hub.requests).toHaveLength(5);
});

it("dispose leaves no listeners", async () => {
	const hub = boundary();
	const board = createBoardController();
	let notified = 0;
	board.subscribe(() => notified++);
	board.setClient(hub.client);
	await answerAll(hub);
	// Live, Needs you, the pin catalog, the manifest, the one category and
	// the sign-in updates.
	expect(hub.listeners.size).toBe(6);
	board.dispose();
	expect(hub.listeners.size).toBe(0);
	const after = notified;
	invalidate(hub, 1, [{ kind: "manifest", revision: 2 }]);
	await tick();
	expect(notified).toBe(after);
});

// The Board's notices come from the hub (S11): evener/notices/list when it
// binds a client and on resume, and each evener/notices/changed after. A hub
// without the method has no notices to show (Jesse, 2026-09-29: no
// client-side fallback).
const signIn = hubNotice("signInRequired", "openai", 2);
const offline = hubNotice("hostOffline", "studio", 3);

it("reads the hub's notices when it gets a client, and the answer becomes the snapshot", async () => {
	const hub = boundary();
	const board = createBoardController();
	expect(board.getSnapshot()).toMatchObject({ notices: [], noticesRead: false });
	board.setClient(hub.client);
	expect(requestsFor(hub, "notices")).toHaveLength(1);
	answerNotices(hub, [signIn, offline]);
	await tick();
	expect(board.getSnapshot()).toMatchObject({ notices: [signIn, offline], noticesRead: true });
});

it("reads a notice list the hub sends as null as an empty one", async () => {
	// Go's encoding/json sends a nil slice as null.
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	next(hub, "notices").resolve({ notices: null } as never);
	await tick();
	expect(board.getSnapshot()).toMatchObject({ notices: [], noticesRead: true });
});

it("takes each evener/notices/changed list as the notices, with no read", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	answerNotices(hub, [signIn]);
	await tick();
	noticesChanged(hub, [offline]);
	expect(board.getSnapshot().notices).toEqual([offline]);
	expect(requestsFor(hub, "notices")).toHaveLength(1);
});

// The hub orders a list response against a broadcast only while it derives,
// and never re-broadcasts a list it announced, so a read answered after a
// changed list is older than it.
it("drops a list read that answers after a changed list", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	noticesChanged(hub, [offline]);
	answerNotices(hub, [signIn]);
	await tick();
	expect(board.getSnapshot().notices).toEqual([offline]);
});

// A changed list is a baseline too, so the alerts stay live when the read
// was dropped or failed. A notice that first appears in that list counts as
// baseline and never alerts: the price of not waiting on a read that may
// never land.
it("takes a changed list as its first notice read when the read failed", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	fail(hub, "notices", "boom");
	await tick();
	expect(board.getSnapshot().noticesRead).toBe(false);
	noticesChanged(hub, [offline]);
	expect(board.getSnapshot()).toMatchObject({ notices: [offline], noticesRead: true });
});

it("shows no notices and no error on a hub without evener/notices/list", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	await answerAll(hub);
	noticesMethodNotFound(hub);
	await tick();
	expect(board.getSnapshot()).toMatchObject({ notices: [], noticesRead: true, error: null });
});

it("never reads the sign-ins or the plugins for notices", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	await answerAll(hub);
	board.pause();
	board.resume();
	await tick();
	expect(hub.requests.map((request) => request.method)).not.toContain("evener/auth/list");
	expect(hub.requests.map((request) => request.method)).not.toContain("evener/plugin/list");
});

it("a changed list the same as the current one notifies nobody", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	answerNotices(hub, [signIn]);
	await tick();
	const before = board.getSnapshot();
	let notified = 0;
	const stop = board.subscribe(() => {
		notified += 1;
	});
	noticesChanged(hub, [signIn]);
	expect(notified).toBe(0);
	expect(board.getSnapshot()).toBe(before);
	stop();
});

it("reads no notices while paused, and reads them again on resume", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.pause();
	board.setClient(hub.client);
	expect(requestsFor(hub, "notices")).toHaveLength(0);
	board.resume();
	expect(requestsFor(hub, "notices")).toHaveLength(1);
});

it("keeps a failed notice read out of the Board's error and keeps the last list", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	await answerAll(hub);
	answerNotices(hub, [signIn]);
	await tick();
	board.pause();
	board.resume();
	fail(hub, "notices", "The hub went away.");
	await tick();
	expect(board.getSnapshot()).toMatchObject({ notices: [signIn], error: null, reading: false, retained: false });
});

it("keeps the notices through a reconnect until the new connection's read lands, and drops the old one's", async () => {
	const first = boundary();
	const board = createBoardController();
	board.setClient(first.client);
	answerNotices(first, [signIn]);
	await tick();
	board.pause();
	board.resume();
	board.setClient(null);
	expect(board.getSnapshot().notices).toEqual([signIn]);
	const second = boundary();
	board.setClient(second.client);
	// The old connection's late answer, and its notifications, are dropped.
	answerNotices(first, []);
	noticesChanged(first, []);
	await tick();
	expect(board.getSnapshot().notices).toEqual([signIn]);
	answerNotices(second, [offline]);
	await tick();
	expect(board.getSnapshot().notices).toEqual([offline]);
});

const category = (id: string, count = 1) => ({ id, name: id, count });

it("reads each category's first page once the catalog lands, and its answers become pinSections", async () => {
	const hub = boundary();
	// Every category's reads, in the order they went out.
	const categoryReads = () => hub.requests.filter((request) => request.params.resource === "pin_section");
	const board = createBoardController();
	board.setClient(hub.client);
	await Promise.resolve();
	expect(categoryReads()).toHaveLength(0);
	answer(hub, "pin_catalog", { pin_sections: [category("release", 2), category("later", 0)], remaining: 0 });
	await tick();
	expect(categoryReads().map((request) => request.params)).toEqual([
		expect.objectContaining({ resource: "pin_section", sectionId: "release", limit: 50 }),
		expect.objectContaining({ resource: "pin_section", sectionId: "later", limit: 50 }),
	]);
	expect(board.getSnapshot().reading).toBe(true);
	answer(hub, "pin_section:release", { sessions: [session("r-0"), session("r-1")], remaining: 0 });
	answer(hub, "pin_section:later", { sessions: [], remaining: 0 });
	await tick();
	const { pinSections } = board.getSnapshot();
	expect(Object.keys(pinSections)).toEqual(["release", "later"]);
	expect(refs(pinSections.release)).toEqual(["r-0", "r-1"]);
	expect(pinSections.later).toMatchObject({ loaded: true, rows: [] });
});

it("reads a category of more than 50 sessions to completion", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	answer(hub, "pin_catalog", { pin_sections: [category("big", 120)], remaining: 0 });
	await tick();
	answer(hub, "pin_section:big", { sessions: sessions("big-", 50), remaining: 70 });
	await tick();
	const second = answer(hub, "pin_section:big", { sessions: sessions("big-", 50, 50), remaining: 20 });
	expect(second.params.offset).toBe(50);
	await tick();
	const third = answer(hub, "pin_section:big", { sessions: sessions("big-", 20, 100), remaining: 0 });
	expect(third.params.offset).toBe(100);
	await tick();
	expect(board.getSnapshot().pinSections.big.rows).toHaveLength(120);
	expect(requestsFor(hub, "pin_section:big")).toHaveLength(3);
});

it("drops a category that leaves the catalog: its reader, its entry and any late answer", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	await answerAll(hub);
	invalidate(hub, 1, [{ kind: "pin_catalog", revision: 2 }]);
	answer(hub, "pin_catalog", { pin_sections: [category("pins-1", 3), category("gone")], remaining: 0 }, 2);
	await tick();
	const late = next(hub, "pin_section:gone");
	expect(Object.keys(board.getSnapshot().pinSections)).toEqual(["pins-1", "gone"]);
	// Each reader's listener, one per category, and the sign-in updates'.
	expect(hub.listeners.size).toBe(7);
	invalidate(hub, 2, [{ kind: "pin_catalog", revision: 3 }]);
	answer(hub, "pin_catalog", { pin_sections: [category("pins-1", 3)], remaining: 0 }, 3);
	await tick();
	expect(Object.keys(board.getSnapshot().pinSections)).toEqual(["pins-1"]);
	expect(hub.listeners.size).toBe(6);
	expect(board.getSnapshot().reading).toBe(false);
	let notified = 0;
	board.subscribe(() => notified++);
	late.resolve(response(late.params, { sessions: [session("late-0")], remaining: 0 }));
	invalidate(hub, 3, [{ kind: "pin_section", sectionId: "gone", revision: 2 }]);
	await tick();
	expect(notified).toBe(0);
	expect(board.getSnapshot().pinSections.gone).toBeUndefined();
	expect(requestsFor(hub, "pin_section:gone")).toHaveLength(1);
});

it("re-reads a category when the hub invalidates it", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	await answerAll(hub);
	invalidate(hub, 1, [{ kind: "pin_section", sectionId: "pins-1", revision: 2 }]);
	answer(hub, "pin_section:pins-1", { sessions: [session("pinned-0"), session("pinned-1")], remaining: 0 }, 2);
	await tick();
	expect(refs(board.getSnapshot().pinSections["pins-1"])).toEqual(["pinned-0", "pinned-1"]);
});

it("keeps a category's rows across a reconnect, reading it at once, until its new read lands", async () => {
	const first = boundary();
	const board = createBoardController();
	board.setClient(first.client);
	await answerAll(first);
	board.setClient(null);
	expect(refs(board.getSnapshot().pinSections["pins-1"])).toEqual(["pinned-0"]);
	expect(board.getSnapshot().retained).toBe(true);

	const second = boundary();
	board.setClient(second.client);
	await Promise.resolve();
	// The retained catalog names the category, so it is read before the
	// catalog's own read lands.
	expect(requestsFor(second, "pin_section:pins-1")).toHaveLength(1);
	answer(second, "live", { sessions: sessions("live-", 2), remaining: 0 });
	answer(second, "needs_you", { sessions: [], remaining: 0 });
	answer(second, "manifest", manifest({ sources }));
	answer(second, "pin_catalog", { pin_sections: [category("pins-1", 3)], remaining: 0 });
	await tick();
	expect(refs(board.getSnapshot().pinSections["pins-1"])).toEqual(["pinned-0"]);
	expect(board.getSnapshot().pinSections["pins-1"].loading).toBe(true);
	expect(board.getSnapshot().retained).toBe(true);
	answer(second, "pin_section:pins-1", { sessions: [session("pinned-9")], remaining: 0 });
	await tick();
	expect(refs(board.getSnapshot().pinSections["pins-1"])).toEqual(["pinned-9"]);
	expect(board.getSnapshot().retained).toBe(false);
	expect(requestsFor(second, "pin_section:pins-1")).toHaveLength(1);
});

it("a retained category the new catalog no longer lists loses its rows", async () => {
	const first = boundary();
	const board = createBoardController();
	board.setClient(first.client);
	await answerAll(first);
	const second = boundary();
	board.setClient(second.client);
	answer(second, "live", { sessions: sessions("live-", 2), remaining: 0 });
	answer(second, "needs_you", { sessions: [], remaining: 0 });
	answer(second, "manifest", manifest({ sources }));
	answer(second, "pin_catalog", { pin_sections: [], remaining: 0 });
	await tick();
	expect(board.getSnapshot().pinSections).toEqual({});
	expect(board.getSnapshot().retained).toBe(false);
});

it("a failed category read reports its error, and resume reads it again", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	answer(hub, "pin_catalog", { pin_sections: [category("release", 2)], remaining: 0 });
	await tick();
	fail(hub, "pin_section:release", "The hub went away.");
	await tick();
	expect(board.getSnapshot().error).toBe("The hub went away.");
	board.pause();
	board.resume();
	answer(hub, "pin_section:release", { sessions: [session("r-0")], remaining: 0 });
	await tick();
	expect(refs(board.getSnapshot().pinSections.release)).toEqual(["r-0"]);
	expect(requestsFor(hub, "pin_section:release")).toHaveLength(2);
});

it("pause holds a category's reads, and resume catches up", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	answer(hub, "pin_catalog", { pin_sections: [category("big", 70)], remaining: 0 });
	await tick();
	answer(hub, "pin_section:big", { sessions: sessions("big-", 50), remaining: 20 });
	await tick();
	// The second page is out when the Board pauses, which cancels it.
	const cancelled = next(hub, "pin_section:big");
	board.pause();
	invalidate(hub, 1, [{ kind: "pin_section", sectionId: "big", revision: 2 }]);
	await tick();
	expect(requestsFor(hub, "pin_section:big")).toHaveLength(2);
	cancelled.resolve(response(cancelled.params, { sessions: sessions("big-", 20, 50), remaining: 0 }));
	await tick();
	expect(board.getSnapshot().pinSections.big.rows).toHaveLength(50);
	board.resume();
	await Promise.resolve();
	const reread = answer(hub, "pin_section:big", { sessions: sessions("big-", 50), remaining: 20 }, 2);
	expect(reread.params.offset ?? 0).toBe(0);
	await tick();
	answer(hub, "pin_section:big", { sessions: sessions("big-", 20, 50), remaining: 0 }, 2);
	await tick();
	expect(board.getSnapshot().pinSections.big.rows).toHaveLength(70);
});

it("a category the retained catalog lists waits for resume to read on a client given while paused", async () => {
	const first = boundary();
	const board = createBoardController();
	board.setClient(first.client);
	await answerAll(first);
	board.pause();
	const second = boundary();
	board.setClient(second.client);
	await tick();
	expect(second.requests).toHaveLength(0);
	expect(refs(board.getSnapshot().pinSections["pins-1"])).toEqual(["pinned-0"]);
	board.resume();
	await Promise.resolve();
	expect(requestsFor(second, "pin_section:pins-1")).toHaveLength(1);
});

it("dispose stops the category readers too", async () => {
	const hub = boundary();
	const board = createBoardController();
	board.setClient(hub.client);
	await answerAll(hub);
	board.dispose();
	expect(hub.listeners.size).toBe(0);
	invalidate(hub, 1, [{ kind: "pin_section", sectionId: "pins-1", revision: 2 }]);
	await tick();
	expect(requestsFor(hub, "pin_section:pins-1")).toHaveLength(1);
});

describe("the attention scope, the in-app alerts' own reads (phase 6)", () => {
	it("reads Live, Needs you, the manifest and the notices, and never the pins or a category", async () => {
		const hub = boundary();
		const board = createBoardController({ scope: "attention" });
		board.setClient(hub.client);
		answer(hub, "live", { sessions: sessions("live-", 2), remaining: 0 });
		answer(hub, "needs_you", { sessions: [session("ask-0")], remaining: 0 });
		answer(hub, "manifest", manifest({ sources }));
		answerNotices(hub, []);
		await tick();
		board.pause();
		board.resume();
		await tick();
		expect(new Set(hub.requests.map(readerOf))).toEqual(new Set(["live", "needs_you", "manifest", "notices"]));
		expect(board.getSnapshot()).toMatchObject({ loaded: true, pins: { rows: [] }, pinSections: {} });
		expect(refs(board.getSnapshot().needsYou)).toEqual(["ask-0"]);
	});

	it("says when its first notice read has landed, even when it finds no notices", async () => {
		const hub = boundary();
		const board = createBoardController({ scope: "attention" });
		board.setClient(hub.client);
		fail(hub, "notices", "boom");
		await tick();
		expect(board.getSnapshot().noticesRead).toBe(false);
		board.pause();
		board.resume();
		answerNotices(hub, []);
		await tick();
		expect(board.getSnapshot()).toMatchObject({ noticesRead: true, notices: [] });
	});
});
