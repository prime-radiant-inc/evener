import type { AnyNotification, SearchParams, SearchResponse, SearchResult } from "@evener/appwire-client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { createSearchController, searchResultMark, sessionResults } from "./boardSearch";

/** A hub that holds every evener/search until the test answers it. */
function boundary() {
	const requests: Array<{
		method: string;
		params: SearchParams;
		resolve: (value: SearchResponse) => void;
		reject: (error: Error) => void;
	}> = [];
	const client: ConversationClientLike = {
		request: (method, params) =>
			new Promise((resolve, reject) => {
				requests.push({ method, params: params as SearchParams, resolve: resolve as never, reject });
			}),
		onNotification: (_listener: (event: AnyNotification) => void) => () => {},
	};
	return { client, requests };
}
const result = (id: string, over: Partial<SearchResult> = {}): SearchResult => ({
	id,
	title: id,
	project: "evener",
	state: "idle",
	age: "2m",
	ref: `local:${id}`,
	...over,
});
async function settle() {
	for (let step = 0; step < 10; step++) await Promise.resolve();
}

describe("the search controller", () => {
	beforeEach(() => vi.useFakeTimers());
	afterEach(() => vi.useRealTimers());

	it("asks the hub 250ms after the last keystroke, once", async () => {
		const hub = boundary();
		const search = createSearchController();
		search.setClient(hub.client);
		search.setQuery("f");
		vi.advanceTimersByTime(200);
		search.setQuery("fix");
		vi.advanceTimersByTime(249);
		expect(hub.requests).toHaveLength(0);
		vi.advanceTimersByTime(1);
		expect(hub.requests.map((request) => [request.method, request.params])).toEqual([
			["evener/search", { query: "fix" }],
		]);
		expect(search.getSnapshot()).toMatchObject({ query: "fix", searching: true, results: null });
		const answer = { live: [result("a")], past: [result("b")] };
		hub.requests[0].resolve(answer);
		await settle();
		expect(search.getSnapshot()).toEqual({ query: "fix", results: answer, searching: false, failed: false });
	});

	it("keeps the newest query's answer when an older one lands late", async () => {
		const hub = boundary();
		const search = createSearchController();
		search.setClient(hub.client);
		search.setQuery("old");
		vi.advanceTimersByTime(250);
		search.setQuery("new");
		vi.advanceTimersByTime(250);
		expect(hub.requests.map((request) => request.params.query)).toEqual(["old", "new"]);
		const newest = { live: [result("new")], past: [] };
		hub.requests[1].resolve(newest);
		await settle();
		hub.requests[0].resolve({ live: [result("old")], past: [] });
		await settle();
		expect(search.getSnapshot()).toMatchObject({ query: "new", results: newest, failed: false });
	});

	it("clears stale results the moment the query changes", async () => {
		const hub = boundary();
		const search = createSearchController();
		search.setClient(hub.client);
		search.setQuery("fix");
		vi.advanceTimersByTime(250);
		hub.requests[0].resolve({ live: [result("a")], past: [] });
		await settle();
		search.setQuery("fixes");
		expect(search.getSnapshot()).toEqual({ query: "fixes", results: null, searching: false, failed: false });
	});

	it("asks nothing for an empty query, or for the same query with spaces around it", async () => {
		const hub = boundary();
		const search = createSearchController();
		search.setClient(hub.client);
		search.setQuery("   ");
		vi.advanceTimersByTime(250);
		expect(hub.requests).toHaveLength(0);
		search.setQuery("fix");
		vi.advanceTimersByTime(250);
		const answer = { live: [result("a")], past: [] };
		hub.requests[0].resolve(answer);
		await settle();
		search.setQuery("fix ");
		vi.advanceTimersByTime(250);
		expect(hub.requests).toHaveLength(1);
		expect(search.getSnapshot().results).toBe(answer);
		// Emptying the field drops the query and its results.
		search.setQuery("");
		expect(search.getSnapshot()).toEqual({ query: "", results: null, searching: false, failed: false });
	});

	it("says a search failed, and tries again when the query changes", async () => {
		const hub = boundary();
		const search = createSearchController();
		search.setClient(hub.client);
		search.setQuery("fix");
		vi.advanceTimersByTime(250);
		hub.requests[0].reject(new Error("request timed out"));
		await settle();
		expect(search.getSnapshot()).toEqual({ query: "fix", results: null, searching: false, failed: true });
		search.setQuery("fixes");
		expect(search.getSnapshot().failed).toBe(false);
		vi.advanceTimersByTime(250);
		expect(hub.requests).toHaveLength(2);
	});

	it("drops an answer that lands after the query changed, before the new query is asked", async () => {
		const hub = boundary();
		const search = createSearchController();
		search.setClient(hub.client);
		search.setQuery("fix");
		vi.advanceTimersByTime(250);
		search.setQuery("fixes");
		hub.requests[0].resolve({ live: [result("fix")], past: [] });
		await settle();
		expect(search.getSnapshot()).toEqual({ query: "fixes", results: null, searching: false, failed: false });
		expect(hub.requests).toHaveLength(1);
	});

	it("stops saying a search failed once the client goes", async () => {
		const hub = boundary();
		const search = createSearchController();
		search.setClient(hub.client);
		search.setQuery("fix");
		vi.advanceTimersByTime(250);
		hub.requests[0].reject(new Error("request timed out"));
		await settle();
		expect(search.getSnapshot().failed).toBe(true);
		search.setClient(null);
		expect(search.getSnapshot()).toEqual({ query: "fix", results: null, searching: false, failed: false });
	});

	it("waits for a client, drops a search when the client goes, and asks again on a new one", async () => {
		const hub = boundary();
		const search = createSearchController();
		search.setQuery("fix");
		vi.advanceTimersByTime(250);
		expect(search.getSnapshot()).toMatchObject({ query: "fix", searching: false });
		// A client arriving with a query waiting asks at once.
		search.setClient(hub.client);
		expect(hub.requests).toHaveLength(1);
		search.setClient(null);
		expect(search.getSnapshot().searching).toBe(false);
		hub.requests[0].resolve({ live: [result("stale")], past: [] });
		await settle();
		expect(search.getSnapshot().results).toBeNull();
		const next = boundary();
		search.setClient(next.client);
		expect(next.requests.map((request) => request.params.query)).toEqual(["fix"]);
	});

	it("tells subscribers about each change and stops after dispose", async () => {
		const hub = boundary();
		const search = createSearchController();
		const heard = vi.fn();
		search.subscribe(heard);
		search.setClient(hub.client);
		search.setQuery("fix");
		expect(heard).toHaveBeenCalledTimes(1);
		search.dispose();
		vi.advanceTimersByTime(250);
		expect(hub.requests).toHaveLength(0);
	});
});

describe("session results", () => {
	const live = [result("live-1"), result("live-2")];
	const past = [result("past-1")];

	it("lists live results, then past ones, in All", () => {
		expect(sessionResults({ live, past }, "all").map((row) => row.id)).toEqual(["live-1", "live-2", "past-1"]);
	});

	it("lists only live results in Live", () => {
		expect(sessionResults({ live, past }, "live").map((row) => row.id)).toEqual(["live-1", "live-2"]);
	});

	it("lists a live session once, when the past index has it too", () => {
		// The hub's past index holds live sessions' records, and evener/search
		// doesn't leave them out of its past results.
		const livePast = { ...result("live-2"), state: "ended", age: "5m" };
		const rows = sessionResults({ live, past: [livePast, ...past] }, "all");
		expect(rows.map((row) => row.id)).toEqual(["live-1", "live-2", "past-1"]);
		expect(rows[1]).toBe(live[1]);
	});
});

describe("a search result's mark", () => {
	const mark = (over: Partial<SearchResult>) => searchResultMark(result("r", over));

	it("marks the states that explain themselves first, whatever flags ride along", () => {
		const flags = { askPending: true, approvalPending: true };
		expect(mark({ state: "errored", ...flags })).toBe("failed");
		expect(mark({ state: "restartRequired", ...flags })).toBe("restartNeeded");
		expect(mark({ state: "warning", ...flags })).toBe("warning");
		expect(mark({ state: "ended", ...flags })).toBe("shutDown");
		expect(mark({ state: "notLoaded", ...flags })).toBe("shutDown");
	});

	it("marks a pending question before a pending approval", () => {
		expect(mark({ state: "awaiting", askPending: true, approvalPending: true })).toBe("question");
		expect(mark({ state: "active", askPending: true })).toBe("question");
	});

	it("marks a pending approval, which leaves the state active", () => {
		expect(mark({ state: "active", approvalPending: true })).toBe("approval");
		expect(mark({ state: "idle", approvalPending: true })).toBe("approval");
	});

	it("marks an active session working", () => {
		expect(mark({ state: "active" })).toBe("working");
	});

	it("leaves everything else unmarked, never Finished: a result has no seen state", () => {
		expect(mark({ state: "idle" })).toBe("idle");
		expect(mark({ state: "awaiting" })).toBe("idle");
		expect(mark({ state: "somethingNew" })).toBe("idle");
	});
});
