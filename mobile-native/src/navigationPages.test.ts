import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type {
	AnyNotification,
	NavigationInvalidatedPayload,
	NavigationReadParams,
	NavigationReadResponse,
} from "@evener/appwire-client";
import { wireSnapshot } from "@evener/appwire-client/testing/navigation";
import type { ConversationClientLike } from "./conversation/services/conversation";
import { reconnectDelay } from "./hubConnection";
import { NavigationPages, updating } from "./navigationPages";

it("reconciles a restarted hub without carrying a prior generation's receipt floor", async () => {
	const { pages, requests } = boundary();
	const first = pages.refresh();
	requests[0].resolve(response(["old"], 0, 1));
	await first;
	const changed = pages.refreshAfter({
		generation_id: "hub-generation",
		targets: [{ kind: "catalog", catalog: "projects", revision: 8 }],
	});
	requests[1].resolve(response(["stale"], 0, 2));
	await expect(changed).rejects.toThrow();
	const reconnect = pages.refresh();
	requests[2].resolve(response(["restarted"], 0, 1, 0, "restarted-hub"));
	await reconnect;
	expect(pages.getSnapshot().stale).toBe(false);
	expect(pages.getSnapshot().rows.map((row) => row.key)).toEqual(["restarted"]);
});

it("uses only the current resource's mutation revision", async () => {
	const { pages, requests } = boundary();
	const read = pages.refreshAfter({
		generation_id: "hub-generation",
		targets: [
			{ kind: "manifest", revision: 90 },
			{ kind: "catalog", catalog: "projects", revision: 2 },
			{ kind: "pin_catalog", revision: 40 },
		],
	});
	requests[0].resolve(response(["current"], 0, 2));
	await expect(read).resolves.toBeUndefined();
	expect(pages.getSnapshot().stale).toBe(false);
	expect(pages.getSnapshot().rows.map((row) => row.key)).toEqual(["current"]);
});
it("does not accept a stale tombstone as mutation readback", async () => {
	const { pages, requests } = boundary();
	const first = pages.refresh();
	requests[0].resolve(response(["current"], 0, 1));
	await first;
	const read = pages.refreshAfter({
		generation_id: "hub-generation",
		targets: [{ kind: "catalog", catalog: "projects", revision: 3 }],
	});
	requests[1].resolve({
		status: "gone",
		generationId: "hub-generation",
		revision: 2,
		etag: "stale-gone",
	});
	await expect(read).rejects.toThrow();
	expect(pages.getSnapshot().rows.map((row) => row.key)).toEqual(["current"]);
	expect(pages.getSnapshot().stale).toBe(true);
});

function boundary(resource: "catalog" | "section" = "catalog", { readRetry = true } = {}) {
	const requests: {
		params: NavigationReadParams;
		resolve: (value: NavigationReadResponse) => void;
		reject: (error: Error) => void;
	}[] = [];
	const arrivals = new Map<number, () => void>();
	let notify: (event: AnyNotification) => void = () => {};
	const client: ConversationClientLike = Object.assign(new FakeClient("ready"), {
		request: (_method, params) =>
			new Promise((resolve, reject) => {
				requests.push({ params: params as NavigationReadParams, resolve, reject });
				arrivals.get(requests.length)?.();
			}),
		onNotification: (listener) => {
			notify = listener;
			return () => {
				notify = () => {};
			};
		},
	} as Omit<ConversationClientLike, "state" | "onReady" | "onStateChange">);
	return {
		requests,
		requested: (count: number) =>
			requests.length >= count ? Promise.resolve() : new Promise<void>((resolve) => arrivals.set(count, resolve)),
		invalidate: (payload: NavigationInvalidatedPayload) =>
			notify({ method: "evener/navigation/invalidated", params: payload }),
		pages: new NavigationPages<{ key: string; ref?: string }>(
			client,
			resource === "catalog" ? { resource: "catalog", catalog: "projects" } : { resource: "section", section: "live" },
			resource === "catalog" ? "projects" : "sessions",
			(row) => row.ref ?? row.key,
			2,
			{ readRetry },
		),
	};
}
function until<T>(pages: NavigationPages<T>, ready: (state: ReturnType<NavigationPages<T>["getSnapshot"]>) => boolean) {
	return new Promise<void>((resolve) => {
		let stop = () => {};
		const check = () => {
			if (!ready(pages.getSnapshot())) return;
			stop();
			resolve();
		};
		stop = pages.subscribe(check);
		check();
	});
}
const settled = <T>(pages: NavigationPages<T>) => until(pages, (state) => !state.loading && !state.stale);
const tick = () => new Promise((resolve) => setTimeout(resolve));
function response(
	keys: string[],
	remaining = 0,
	revision = 1,
	offset = 0,
	generation = "hub-generation",
): NavigationReadResponse {
	return wireSnapshot(
		{
			representationVersion: 3,
			resource: "catalog",
			catalog: "projects",
			offset,
			limit: 2,
		},
		{ projects: keys.map((key) => ({ key })), remaining },
		`etag-${offset}-${revision}`,
		revision,
		generation,
	);
}
describe("navigation pages", () => {
	it("advances by raw rows while deduplicating navigable identities", async () => {
		const { requests, pages } = boundary();
		const first = pages.refresh();
		requests[0].resolve(response(["a", "b"], 2));
		await first;
		const next = pages.more();
		void pages.more();
		expect(requests).toHaveLength(2);
		expect(requests[1].params.offset).toBe(2);
		requests[1].resolve(response(["b", "c"], 0, 1, 2));
		await next;
		expect(pages.getSnapshot().rows.map((row) => row.key)).toEqual(["a", "b", "c"]);
		expect(pages.getSnapshot().remaining).toBe(0);
	});
	it("refuses to combine revisions and re-reads from the first page", async () => {
		const { requests, pages } = boundary();
		const first = pages.refresh();
		requests[0].resolve(response(["a"], 1));
		await first;
		const next = pages.more();
		requests[1].resolve(response(["b"], 0, 2, 1));
		await next;
		expect(pages.getSnapshot().rows.map((row) => row.key)).toEqual(["a"]);
		expect(pages.getSnapshot().stale).toBe(true);
		// The revision mismatch hands the page to the recovery flight, which
		// re-reads from the first page on its own: the fresh read is already
		// in flight, and the press that arrives meanwhile joins it.
		const joined = pages.more();
		expect(requests).toHaveLength(3);
		expect(requests[2].params.offset).toBe(0);
		requests[2].resolve(response(["b"], 0, 2));
		await joined;
		await settled(pages);
		expect(pages.getSnapshot().rows.map((row) => row.key)).toEqual(["b"]);
	});
	it("ignores an in-flight completion after leaving the binding", async () => {
		const { requests, pages } = boundary();
		const first = pages.refresh();
		pages.cancel();
		requests[0].resolve(response(["old"]));
		await first;
		expect(pages.getSnapshot().rows).toEqual([]);
		expect(pages.getSnapshot().loading).toBe(false);
	});
	it("reports malformed progress instead of looping empty pages", async () => {
		const { requests, pages } = boundary();
		const first = pages.refresh();
		requests[0].resolve(response([], 3));
		await first;
		expect(pages.getSnapshot().error).toBeTruthy();
		expect(pages.getSnapshot().remaining).toBe(0);
	});
});

describe("a failed read's own retry", () => {
	beforeEach(() => vi.useFakeTimers());
	afterEach(() => vi.useRealTimers());

	it("re-reads after a growing backoff until a read lands, then retires", async () => {
		const { requests, pages } = boundary();
		const first = pages.refresh();
		requests[0].reject(new Error("request timed out"));
		await first;
		expect(pages.getSnapshot().error).toBeTruthy();
		// The first retry waits the backoff's first step, and keeps waiting
		// longer with each failure in a row.
		await vi.advanceTimersByTimeAsync(reconnectDelay(1) - 1);
		expect(requests).toHaveLength(1);
		await vi.advanceTimersByTimeAsync(1);
		expect(requests).toHaveLength(2);
		requests[1].reject(new Error("request timed out"));
		await settled(pages).catch(() => {});
		await vi.advanceTimersByTimeAsync(reconnectDelay(2) - 1);
		expect(requests).toHaveLength(2);
		await vi.advanceTimersByTimeAsync(1);
		expect(requests).toHaveLength(3);
		requests[2].resolve(response(["fresh"]));
		await settled(pages);
		expect(pages.getSnapshot()).toMatchObject({ rows: [{ key: "fresh" }], error: null });
		// Loaded and clean: no more retries arm.
		const reads = requests.length;
		await vi.advanceTimersByTimeAsync(reconnectDelay(3) * 2);
		expect(requests).toHaveLength(reads);
	});

	it("a failed refresh of an already-loaded page re-arms the re-read too", async () => {
		const { requests, pages } = boundary();
		const first = pages.refresh();
		requests[0].resolve(response(["a"]));
		await first;
		const refresh = pages.refresh();
		requests[1].reject(new Error("request timed out"));
		await refresh;
		expect(pages.getSnapshot().rows).toMatchObject([{ key: "a" }]);
		await vi.advanceTimersByTimeAsync(reconnectDelay(1));
		expect(requests).toHaveLength(3);
		requests[2].resolve(response(["b"]));
		await settled(pages);
		expect(pages.getSnapshot().rows).toMatchObject([{ key: "b" }]);
	});

	it("a retry restores the depth the reader had reached, not just the first page", async () => {
		const { requests, pages, requested } = boundary();
		// Two pages loaded: the reader reached a depth of four rows.
		const first = pages.refresh();
		requests[0].resolve(response(["a", "b"], 2));
		await first;
		const more = pages.more();
		requests[1].resolve(response(["c", "d"], 0, 1, 2));
		await more;
		expect(pages.getSnapshot().rows.map((r) => r.key)).toEqual(["a", "b", "c", "d"]);
		// A refresh replaces page one and fails on page two: the rows fall
		// back to one page while the retry still owes the second.
		const refresh = pages.refresh();
		requests[2].resolve(response(["a2", "b2"], 2));
		await refresh;
		const deeper = pages.more();
		requests[3].reject(new Error("request timed out"));
		await Promise.allSettled([deeper]);
		expect(pages.getSnapshot().rows.map((r) => r.key)).toEqual(["a2", "b2"]);
		// The retry re-reads to the depth the reader had (two pages) before it
		// clears the error: a first-page landing alone must not end recovery.
		await vi.advanceTimersByTimeAsync(reconnectDelay(1));
		expect(requests).toHaveLength(5);
		requests[4].resolve(response(["a3", "b3"], 2));
		await settled(pages);
		expect(pages.getSnapshot().error).toBeTruthy();
		// The depth loop continues on the same landing, and only a full
		// restore clears the error.
		await requested(6);
		requests[5].resolve(response(["c3", "d3"], 0, 1, 2));
		await settled(pages);
		expect(pages.getSnapshot().rows.map((r) => r.key)).toEqual(["a3", "b3", "c3", "d3"]);
		expect(pages.getSnapshot().error).toBeNull();
	});

	it("completes recovery when the results end below the depth it owed", async () => {
		const { requests, pages, requested } = boundary();
		const first = pages.refresh();
		requests[0].resolve(response(["a", "b"], 2));
		await first;
		const more = pages.more();
		requests[1].resolve(response(["c", "d"], 0, 1, 2));
		await more;
		// The catalog shrank: the refresh answers with a single page and
		// nothing remaining, though the reader had reached a depth of four.
		const refresh = pages.refresh();
		requests[2].resolve(response(["a2"], 0, 2));
		await refresh;
		// The old rows are gone from the hub: results exhausted, recovery is
		// complete, and no error stays latched for depth that cannot exist.
		expect(pages.getSnapshot().rows.map((r) => r.key)).toEqual(["a2"]);
		expect(pages.getSnapshot().error).toBeNull();
		// No retry arms for depth the list no longer has.
		const reads = requests.length;
		await vi.advanceTimersByTimeAsync(reconnectDelay(2) * 2);
		expect(requests).toHaveLength(reads);
		await Promise.resolve(requested);
	});

	it("cancel abandons the pending retry, and none runs while paused", async () => {
		const { requests, pages } = boundary();
		const first = pages.refresh();
		requests[0].reject(new Error("request timed out"));
		await first;
		pages.cancel();
		await vi.advanceTimersByTimeAsync(reconnectDelay(1) * 3);
		expect(requests).toHaveLength(1);
		// Resume re-arms the read the way it always has: the store's own
		// owed-read path, not a second retry owner.
		pages.resume();
		expect(requests).toHaveLength(2);
		requests[1].resolve(response(["back"]));
		await settled(pages);
		expect(pages.getSnapshot().rows).toMatchObject([{ key: "back" }]);
	});

	it("a failed recovery walk waits out the backoff instead of restarting at once", async () => {
		const { requests, pages, requested, invalidate } = boundary();
		pages.watch();
		// Two pages loaded: the reader reached a depth of four rows.
		const first = pages.refresh();
		requests[0].resolve(response(["a", "b"], 2));
		await first;
		const more = pages.more();
		requests[1].resolve(response(["c", "d"], 0, 1, 2));
		await more;
		// A hub change re-reads the loaded depth; the re-read's own walk fails
		// on page two.
		invalidate({
			generationId: "hub-generation",
			sequence: 2,
			targets: [{ kind: "catalog", catalog: "projects", revision: 2 }],
		});
		await requested(3);
		requests[2].resolve(response(["a2", "b2"], 2, 2));
		await requested(4);
		requests[3].reject(new Error("request timed out"));
		await settled(pages);
		expect(pages.getSnapshot().error).toBeTruthy();
		// The walk's own page-one landing must not leave a queued request that
		// restarts the recovery the moment the failed walk ends: the next read
		// waits for the backoff the failure armed.
		const reads = requests.length;
		await vi.advanceTimersByTimeAsync(reconnectDelay(1) - 1);
		expect(requests).toHaveLength(reads);
		await vi.advanceTimersByTimeAsync(1);
		expect(requests).toHaveLength(reads + 1);
	});

	it("a mutation readback landing short beside a superseded recovery still queues the walk", async () => {
		const { requests, pages, requested, invalidate } = boundary();
		pages.watch();
		// Three pages loaded: the reader reached a depth of six rows.
		const first = pages.refresh();
		requests[0].resolve(response(["a", "b"], 2));
		await first;
		const more = pages.more();
		requests[1].resolve(response(["c", "d"], 2, 1, 2));
		await more;
		const deepest = pages.more();
		requests[2].resolve(response(["e", "f"], 0, 1, 4));
		await deepest;
		// A hub change starts a recovery whose page two is slow to answer.
		invalidate({
			generationId: "hub-generation",
			sequence: 2,
			targets: [{ kind: "catalog", catalog: "projects", revision: 2 }],
		});
		await requested(4);
		requests[3].resolve(response(["a2", "b2"], 2, 2));
		await requested(5);
		// The reader archives a project: the mutation readback supersedes the
		// walk's in-flight page two and lands a first page short of the depth.
		// Its landing must still hand the rest of the depth to the flight,
		// superseded walk or not.
		const after = pages.refreshAfter({
			generation_id: "hub-generation",
			targets: [{ kind: "catalog", catalog: "projects", revision: 3 }],
		});
		await requested(6);
		requests[5].resolve(response(["a3", "b3"], 2, 3));
		await expect(after).resolves.toBeUndefined();
		// The superseded walk's late answer arrives and is discarded; the
		// flight still owes the depth and re-reads for it.
		requests[4].resolve(response(["c2", "d2"], 2, 2, 2));
		await requested(7);
		requests[6].resolve(response(["a3", "b3"], 2, 3));
		await requested(8);
		requests[7].resolve(response(["c3", "d3"], 2, 3, 2));
		await requested(9);
		requests[8].resolve(response(["e3", "f3"], 0, 3, 4));
		await settled(pages);
		expect(pages.getSnapshot().rows.map((r) => r.key)).toEqual(["a3", "b3", "c3", "d3", "e3", "f3"]);
		expect(pages.getSnapshot().error).toBeNull();
	});

	it("a stale resume that fails waits out the backoff, with no trailing run queued", async () => {
		const { requests, pages, requested, invalidate } = boundary();
		pages.watch();
		// Three pages loaded: the reader reached a depth of six rows.
		const first = pages.refresh();
		requests[0].resolve(response(["a", "b"], 2));
		await first;
		const more = pages.more();
		requests[1].resolve(response(["c", "d"], 2, 1, 2));
		await more;
		const deepest = pages.more();
		requests[2].resolve(response(["e", "f"], 0, 1, 4));
		await deepest;
		// A hub change starts a recovery; the reader leaves before its reset
		// lands, so the page is stale with the depth owed underneath.
		invalidate({
			generationId: "hub-generation",
			sequence: 2,
			targets: [{ kind: "catalog", catalog: "projects", revision: 2 }],
		});
		await requested(4);
		pages.cancel();
		pages.resume();
		// The resumed re-read starts and fails: the next read must wait for
		// the backoff the failure armed, not fire from a trailing run the
		// resume queued behind the stale re-read.
		await requested(5);
		requests[4].reject(new Error("request timed out"));
		// The failed read leaves the page stale: drain the microtasks so the
		// failure's retry timer arms before counting.
		await Promise.resolve();
		await Promise.resolve();
		const reads = requests.length;
		await vi.advanceTimersByTimeAsync(reconnectDelay(1) - 1);
		expect(requests).toHaveLength(reads);
		await vi.advanceTimersByTimeAsync(1);
		expect(requests).toHaveLength(reads + 1);
	});

	it("resume restores a cancelled recovery's depth without the retry opt-in", async () => {
		const { requests, pages, requested, invalidate } = boundary("catalog", { readRetry: false });
		pages.watch();
		// Three pages loaded: the reader reached a depth of six rows.
		const first = pages.refresh();
		requests[0].resolve(response(["a", "b"], 2));
		await first;
		const more = pages.more();
		requests[1].resolve(response(["c", "d"], 2, 1, 2));
		await more;
		const deepest = pages.more();
		requests[2].resolve(response(["e", "f"], 0, 1, 4));
		await deepest;
		// A hub change starts a recovery; its first page lands clean and the
		// reader leaves while the walk's deeper page is pending. Without the
		// automatic-retry opt-in, the depth is the only thing that can bring
		// the reader's pages back on resume.
		invalidate({
			generationId: "hub-generation",
			sequence: 2,
			targets: [{ kind: "catalog", catalog: "projects", revision: 2 }],
		});
		await requested(4);
		requests[3].resolve(response(["a2", "b2"], 2, 2));
		await requested(5);
		pages.cancel();
		pages.resume();
		// The recovery re-reads from the first page and completes the depth.
		await requested(6);
		requests[5].resolve(response(["a2", "b2"], 2, 2));
		await requested(7);
		requests[6].resolve(response(["c2", "d2"], 2, 2, 2));
		await requested(8);
		requests[7].resolve(response(["e2", "f2"], 0, 2, 4));
		await settled(pages);
		expect(pages.getSnapshot().rows.map((r) => r.key)).toEqual(["a2", "b2", "c2", "d2", "e2", "f2"]);
	});

	it("a not_modified landing with depth owed keeps the error for a resume to re-arm", async () => {
		const { requests, pages, requested, invalidate } = boundary();
		pages.watch();
		// Three pages loaded: the reader reached a depth of six rows.
		const first = pages.refresh();
		requests[0].resolve(response(["a", "b"], 2));
		await first;
		const more = pages.more();
		requests[1].resolve(response(["c", "d"], 2, 1, 2));
		await more;
		const deepest = pages.more();
		requests[2].resolve(response(["e", "f"], 0, 1, 4));
		await deepest;
		// A hub change starts a recovery whose page two fails.
		invalidate({
			generationId: "hub-generation",
			sequence: 2,
			targets: [{ kind: "catalog", catalog: "projects", revision: 2 }],
		});
		await requested(4);
		requests[3].resolve(response(["a2", "b2"], 2, 2));
		await requested(5);
		requests[4].reject(new Error("request timed out"));
		await settled(pages);
		expect(pages.getSnapshot().error).toBeTruthy();
		// The reader presses Try again: a conditional reset whose base is
		// the revision the store already holds. The hub answers not_modified
		// — the rows stand — but the depth is still owed, so the error must
		// hold through that landing: a cleared error would strand the
		// recovery with nothing for a resume to re-arm.
		const manual = pages.refresh();
		await requested(6);
		expect(requests[5].params.base).toMatchObject({
			generationId: "hub-generation",
			revision: 2,
			etag: "etag-0-2",
		});
		requests[5].resolve({
			status: "not_modified",
			generationId: "hub-generation",
			revision: 2,
			etag: "etag-0-2",
		});
		await Promise.resolve();
		await Promise.resolve();
		expect(pages.getSnapshot().error).toBeTruthy();
		// The walk's page two is pending; the reader leaves the screen. The
		// recovery is cancelled mid-walk with neither stale nor error able
		// to re-arm it — only the depth it still owes.
		await requested(7);
		pages.cancel();
		pages.resume();
		// The recovery re-reads from the first page, the hub answers
		// not_modified again, and the walk completes the depth.
		await requested(8);
		expect(requests[7].params.base).toMatchObject({
			generationId: "hub-generation",
			revision: 2,
			etag: "etag-0-2",
		});
		requests[7].resolve({
			status: "not_modified",
			generationId: "hub-generation",
			revision: 2,
			etag: "etag-0-2",
		});
		await requested(9);
		requests[8].resolve(response(["c2", "d2"], 2, 2, 2));
		await requested(10);
		requests[9].resolve(response(["e2", "f2"], 0, 2, 4));
		await manual;
		await settled(pages);
		expect(pages.getSnapshot().rows.map((r) => r.key)).toEqual(["a2", "b2", "c2", "d2", "e2", "f2"]);
		expect(pages.getSnapshot().error).toBeNull();
	});

	it("a refresh that joins a running recovery observes its completion, not a page", async () => {
		const { requests, pages, requested, invalidate } = boundary();
		pages.watch();
		// Three pages loaded: the reader reached a depth of six rows.
		const first = pages.refresh();
		requests[0].resolve(response(["a", "b"], 2));
		await first;
		const more = pages.more();
		requests[1].resolve(response(["c", "d"], 2, 1, 2));
		await more;
		const deepest = pages.more();
		requests[2].resolve(response(["e", "f"], 0, 1, 4));
		await deepest;
		// A hub change re-reads the loaded depth. The re-read's reset lands
		// the new revision, and its own walk fails on page two, so the
		// recovery still owes the full depth.
		invalidate({
			generationId: "hub-generation",
			sequence: 2,
			targets: [{ kind: "catalog", catalog: "projects", revision: 2 }],
		});
		await requested(4);
		requests[3].resolve(response(["a2", "b2"], 2, 2));
		await requested(5);
		requests[4].reject(new Error("request timed out"));
		await settled(pages);
		expect(pages.getSnapshot().error).toBeTruthy();
		// The reader presses Retry loading more: the press is a direct page
		// read at the offset it reached for. It lands short of the owed depth,
		// so its landing hands the rest of the recovery to the flight, whose
		// re-read walks from the first page.
		const manual = pages.more();
		await requested(6);
		// The refresh joins while the press's own read is still in flight.
		// The press lands short of the owed depth, so its landing must hand
		// the rest of the recovery to the flight without resolving the
		// joined refresh: the depth is not restored yet.
		const joined = pages.refresh();
		let resolved = false;
		void joined.then(() => {
			resolved = true;
		});
		requests[5].resolve(response(["c2", "d2"], 2, 2, 2));
		await Promise.resolve();
		await Promise.resolve();
		expect(resolved).toBe(false);
		await manual;
		expect(pages.getSnapshot().error).toBeTruthy();
		// The flight's re-read walks the rest of the depth. The reset page
		// stands (the revision has not changed again) while page three is
		// still owed; the joined refresh must stay pending through it too.
		await requested(7);
		requests[6].resolve({
			status: "not_modified",
			generationId: "hub-generation",
			revision: 2,
			etag: "etag-0-2",
		});
		await requested(8);
		requests[7].resolve(response(["e2", "f2"], 0, 2, 4));
		await Promise.resolve();
		await Promise.resolve();
		expect(resolved).toBe(false);
		// The not_modified reset landing must not have resolved the joined
		// refresh either; the walk's last page completes the recovery, and
		// the joined refresh resolves with it.
		await joined;
		expect(resolved).toBe(true);
		expect(pages.getSnapshot().rows.map((r) => r.key)).toEqual(["a2", "b2", "c2", "d2", "e2", "f2"]);
		expect(pages.getSnapshot().error).toBeNull();
	});
});

it("re-reads a relevant update without user action, keeping the visible rows until it lands", async () => {
	const { requests, pages, invalidate } = boundary();
	const stop = pages.watch();
	const first = pages.refresh();
	requests[0].resolve(response(["a"], 1));
	await first;
	invalidate({
		generationId: "hub-generation",
		sequence: 1,
		targets: [{ kind: "catalog", catalog: "archived_projects", revision: 2 }],
	});
	expect(pages.getSnapshot().stale).toBe(false);
	expect(requests).toHaveLength(1);
	invalidate({
		generationId: "hub-generation",
		sequence: 2,
		targets: [{ kind: "catalog", catalog: "projects", revision: 2 }],
	});
	expect(pages.getSnapshot()).toMatchObject({
		stale: true,
		loading: true,
		error: null,
		rows: [{ key: "a" }],
	});
	expect(requests).toHaveLength(2);
	expect(requests[1].params.offset).toBe(0);
	// The recovery flight owns the re-read; a press that arrives while it is
	// in flight joins it rather than racing a second read.
	const joined = pages.more();
	expect(requests).toHaveLength(2);
	requests[1].resolve(response(["b"], 0, 2));
	await joined;
	await settled(pages);
	expect(pages.getSnapshot().rows).toMatchObject([{ key: "b" }]);
	stop();
	invalidate({ generationId: "other", sequence: 1, targets: [] });
	expect(pages.getSnapshot().stale).toBe(false);
	expect(requests).toHaveLength(2);
});
it("re-reads every loaded page so the list keeps its length", async () => {
	const { requests, pages, invalidate, requested } = boundary();
	pages.watch();
	const first = pages.refresh();
	requests[0].resolve(response(["a", "b"], 4));
	await first;
	const more = pages.more();
	requests[1].resolve(response(["c", "d"], 2, 1, 2));
	await more;
	invalidate({
		generationId: "hub-generation",
		sequence: 1,
		targets: [{ kind: "catalog", catalog: "projects", revision: 2 }],
	});
	requests[2].resolve(response(["a", "b2"], 4, 2));
	await requested(4);
	expect(requests[3].params.offset).toBe(2);
	requests[3].resolve(response(["c2", "d"], 2, 2, 2));
	await settled(pages);
	expect(requests.map((r) => r.params.offset)).toEqual([0, 2, 0, 2]);
	expect(pages.getSnapshot()).toMatchObject({
		rows: [{ key: "a" }, { key: "b2" }, { key: "c2" }, { key: "d" }],
		remaining: 2,
		error: null,
	});
});
it("re-reads after a page read that lost a race with an invalidation", async () => {
	const { requests, pages, invalidate } = boundary();
	pages.watch();
	const first = pages.refresh();
	requests[0].resolve(response(["a", "b"], 2));
	await first;
	const more = pages.more();
	invalidate({
		generationId: "hub-generation",
		sequence: 1,
		targets: [{ kind: "catalog", catalog: "projects", revision: 2 }],
	});
	requests[1].resolve(response(["c", "d"], 0, 1, 2));
	await more;
	expect(pages.getSnapshot().rows).toMatchObject([{ key: "a" }, { key: "b" }]);
	expect(requests).toHaveLength(3);
	expect(requests[2].params.offset).toBe(0);
	requests[2].resolve(response(["a2", "b"], 2, 2));
	await settled(pages);
	expect(pages.getSnapshot().rows).toMatchObject([{ key: "a2" }, { key: "b" }]);
	expect(requests).toHaveLength(3);
});
it("retries an in-flight read that a newer invalidation overtook", async () => {
	const { requests, pages, invalidate, requested } = boundary();
	pages.watch();
	const first = pages.refresh();
	invalidate({
		generationId: "hub-generation",
		sequence: 1,
		targets: [{ kind: "catalog", catalog: "projects", revision: 3 }],
	});
	requests[0].resolve(response(["old"], 0, 2));
	await requested(2);
	expect(pages.getSnapshot().stale).toBe(true);
	expect(pages.getSnapshot().rows).toEqual([]);
	requests[1].resolve(response(["fresh"], 0, 3));
	await first;
	expect(pages.getSnapshot().stale).toBe(false);
	expect(pages.getSnapshot().rows).toMatchObject([{ key: "fresh" }]);
	expect(requests).toHaveLength(2);
});
it("a burst of invalidations during a re-read costs one trailing read", async () => {
	const { requests, pages, invalidate, requested } = boundary();
	pages.watch();
	const first = pages.refresh();
	requests[0].resolve(response(["a"], 0, 1));
	await first;
	invalidate({
		generationId: "hub-generation",
		sequence: 1,
		targets: [{ kind: "catalog", catalog: "projects", revision: 2 }],
	});
	invalidate({
		generationId: "hub-generation",
		sequence: 2,
		targets: [{ kind: "catalog", catalog: "projects", revision: 3 }],
	});
	expect(requests).toHaveLength(2);
	requests[1].resolve(response(["b"], 0, 2));
	await requested(3);
	requests[2].resolve(response(["c"], 0, 3));
	await settled(pages);
	expect(pages.getSnapshot().rows).toMatchObject([{ key: "c" }]);
	expect(requests).toHaveLength(3);
});
it("accepts a read that already incorporates the notified revision", async () => {
	const { requests, pages, invalidate } = boundary();
	pages.watch();
	const first = pages.refresh();
	invalidate({
		generationId: "hub-generation",
		sequence: 1,
		targets: [{ kind: "catalog", catalog: "projects", revision: 3 }],
	});
	requests[0].resolve(response(["fresh"], 0, 3));
	await first;
	expect(pages.getSnapshot().rows).toMatchObject([{ key: "fresh" }]);
	expect(pages.getSnapshot().stale).toBe(false);
	expect(requests).toHaveLength(1);
});
it("re-reads after a sequence gap even with unrelated targets", async () => {
	const { requests, pages, invalidate } = boundary();
	pages.watch();
	const first = pages.refresh();
	requests[0].resolve(response(["a"]));
	await first;
	invalidate({ generationId: "hub-generation", sequence: 1, targets: [] });
	expect(requests).toHaveLength(1);
	invalidate({ generationId: "hub-generation", sequence: 3, targets: [] });
	expect(pages.getSnapshot().stale).toBe(true);
	expect(requests).toHaveLength(2);
	requests[1].resolve(response(["a"], 0, 1));
	await settled(pages);
	expect(pages.getSnapshot().rows).toMatchObject([{ key: "a" }]);
});
it("hands the invalidation to an owner instead of reading when one is watching", async () => {
	const { requests, pages, invalidate } = boundary();
	let handed = 0;
	pages.watch(() => handed++);
	const first = pages.refresh();
	requests[0].resolve(response(["a"]));
	await first;
	invalidate({
		generationId: "hub-generation",
		sequence: 1,
		targets: [{ kind: "catalog", catalog: "projects", revision: 2 }],
	});
	expect(handed).toBe(1);
	expect(pages.getSnapshot().stale).toBe(true);
	expect(requests).toHaveLength(1);
	const refresh = pages.refresh();
	requests[1].resolve(response(["b"], 0, 2));
	await refresh;
	expect(pages.getSnapshot().stale).toBe(false);
});

it("establishes a restarted hub generation from the re-read", async () => {
	const { requests, pages, invalidate } = boundary();
	pages.watch();
	const first = pages.refresh();
	requests[0].resolve(response(["a"], 0, 10));
	await first;
	invalidate({
		generationId: "restarted",
		sequence: 1,
		targets: [{ kind: "catalog", catalog: "projects", revision: 1 }],
	});
	requests[1].resolve(response(["new"], 0, 1, 0, "restarted"));
	await settled(pages);
	expect(pages.getSnapshot().rows).toMatchObject([{ key: "new" }]);
});
it("does not re-read an update whose revision it already holds", async () => {
	const { requests, pages, invalidate } = boundary();
	pages.watch();
	const first = pages.refresh();
	requests[0].resolve(response(["a"], 0, 3));
	await first;
	invalidate({
		generationId: "hub-generation",
		sequence: 1,
		targets: [
			{ kind: "catalog", catalog: "projects", revision: 3 },
			{ kind: "catalog", catalog: "projects", revision: 2 },
		],
	});
	expect(pages.getSnapshot().stale).toBe(false);
	expect(requests).toHaveLength(1);
	invalidate({
		generationId: "hub-generation",
		sequence: 2,
		targets: [{ kind: "catalog", catalog: "projects", revision: 4 }],
	});
	expect(requests).toHaveLength(2);
});
it("holds an owed re-read while cancelled and runs it on resume", async () => {
	const { requests, pages, invalidate } = boundary();
	pages.watch();
	const first = pages.refresh();
	requests[0].resolve(response(["a"], 0, 1));
	await first;
	invalidate({
		generationId: "hub-generation",
		sequence: 1,
		targets: [{ kind: "catalog", catalog: "projects", revision: 2 }],
	});
	pages.cancel();
	requests[1].resolve(response(["b"], 0, 2));
	await tick();
	expect(pages.getSnapshot()).toMatchObject({ stale: true, rows: [{ key: "a" }] });
	invalidate({
		generationId: "hub-generation",
		sequence: 2,
		targets: [{ kind: "catalog", catalog: "projects", revision: 3 }],
	});
	expect(requests).toHaveLength(2);
	pages.resume();
	expect(requests).toHaveLength(3);
	requests[2].resolve(response(["c"], 0, 3));
	await settled(pages);
	expect(pages.getSnapshot().rows).toMatchObject([{ key: "c" }]);
});
it("keeps an owed re-read when the read it interrupted is cancelled", async () => {
	// Round 2 (#1462): the notification landed mid-read, so the store left it
	// to that read's checks; cancel() then dropped the read and the owed
	// re-read with it.
	const { requests, pages, invalidate } = boundary();
	pages.watch();
	const first = pages.refresh();
	requests[0].resolve(response(["a"], 0, 1));
	await first;
	const next = pages.refresh();
	invalidate({
		generationId: "hub-generation",
		sequence: 1,
		targets: [{ kind: "catalog", catalog: "projects", revision: 2 }],
	});
	pages.cancel();
	requests[1].resolve(response(["a"], 0, 1));
	await next;
	await tick();
	expect(requests).toHaveLength(2);
	pages.resume();
	expect(requests).toHaveLength(3);
	expect(requests[2].params.offset).toBe(0);
	requests[2].resolve(response(["b"], 0, 2));
	await settled(pages);
	expect(pages.getSnapshot().rows).toMatchObject([{ key: "b" }]);
	expect(requests).toHaveLength(3);
});
it("re-reads after cancel and resume when the cancelled re-read never settles", async () => {
	// #2466: cancel() dropped the re-read in flight, but a transport the app's
	// backgrounding silently abandoned may never settle. The single flight then
	// stayed running, so every later invalidation for the page was dropped.
	const { requests, pages, invalidate } = boundary();
	pages.watch();
	const first = pages.refresh();
	requests[0].resolve(response(["a"], 0, 1));
	await first;
	invalidate({
		generationId: "hub-generation",
		sequence: 1,
		targets: [{ kind: "catalog", catalog: "projects", revision: 2 }],
	});
	// The re-read is in flight and never answers.
	expect(requests).toHaveLength(2);
	pages.cancel();
	pages.resume();
	expect(requests).toHaveLength(3);
	requests[2].resolve(response(["b"], 0, 2));
	await settled(pages);
	expect(pages.getSnapshot().rows).toMatchObject([{ key: "b" }]);
	// A later invalidation still re-reads rather than sticking.
	await tick();
	invalidate({
		generationId: "hub-generation",
		sequence: 2,
		targets: [{ kind: "catalog", catalog: "projects", revision: 3 }],
	});
	expect(requests).toHaveLength(4);
	requests[3].resolve(response(["c"], 0, 3));
	await settled(pages);
	expect(pages.getSnapshot().rows).toMatchObject([{ key: "c" }]);
});
it("an explicit refresh resumes a cancelled store and satisfies the owed re-read", async () => {
	// A revealing list refreshes on focus instead of calling resume(); that
	// read must both lift the pause and stand in for the re-read owed.
	const { requests, pages, invalidate } = boundary();
	pages.watch();
	const first = pages.refresh();
	requests[0].resolve(response(["a"], 0, 1));
	await first;
	pages.cancel();
	invalidate({
		generationId: "hub-generation",
		sequence: 1,
		targets: [{ kind: "catalog", catalog: "projects", revision: 2 }],
	});
	expect(requests).toHaveLength(1);
	const refocus = pages.refresh();
	requests[1].resolve(response(["b"], 0, 2));
	await refocus;
	await tick();
	expect(requests).toHaveLength(2);
	invalidate({
		generationId: "hub-generation",
		sequence: 2,
		targets: [{ kind: "catalog", catalog: "projects", revision: 3 }],
	});
	expect(requests).toHaveLength(3);
	requests[2].resolve(response(["c"], 0, 3));
	await settled(pages);
	expect(pages.getSnapshot().rows).toMatchObject([{ key: "c" }]);
});
it("re-reads a page the hub under-served on the next notification, once per notification", async () => {
	// Round 3 (#1462): after the hub served an older revision than it
	// announced the page stayed stale with no trigger left to read it again.
	const { requests, pages, invalidate } = boundary();
	pages.watch();
	const first = pages.refresh();
	requests[0].resolve(response(["a"], 0, 1));
	await first;
	invalidate({
		generationId: "hub-generation",
		sequence: 1,
		targets: [{ kind: "catalog", catalog: "projects", revision: 2 }],
	});
	requests[1].resolve(response(["a"], 0, 1));
	await until(pages, (state) => !state.loading);
	await tick();
	expect(pages.getSnapshot().stale).toBe(true);
	expect(requests).toHaveLength(2);
	invalidate({
		generationId: "hub-generation",
		sequence: 2,
		targets: [{ kind: "catalog", catalog: "archived_projects", revision: 5 }],
	});
	expect(requests).toHaveLength(3);
	expect(requests[2].params.offset).toBe(0);
	requests[2].resolve(response(["a"], 0, 1));
	await until(pages, (state) => !state.loading);
	await tick();
	expect(requests).toHaveLength(3);
	invalidate({
		generationId: "hub-generation",
		sequence: 3,
		targets: [{ kind: "catalog", catalog: "archived_projects", revision: 6 }],
	});
	expect(requests).toHaveLength(4);
	requests[3].resolve(response(["b"], 0, 2));
	await settled(pages);
	expect(pages.getSnapshot().rows).toMatchObject([{ key: "b" }]);
});
it("re-reads a page the hub under-served when it is resumed", async () => {
	const { requests, pages, invalidate } = boundary();
	pages.watch();
	const first = pages.refresh();
	requests[0].resolve(response(["a"], 0, 1));
	await first;
	invalidate({
		generationId: "hub-generation",
		sequence: 1,
		targets: [{ kind: "catalog", catalog: "projects", revision: 2 }],
	});
	requests[1].resolve(response(["a"], 0, 1));
	await until(pages, (state) => !state.loading);
	await tick();
	pages.cancel();
	pages.resume();
	expect(requests).toHaveLength(3);
	requests[2].resolve(response(["b"], 0, 2));
	await settled(pages);
	expect(pages.getSnapshot().rows).toMatchObject([{ key: "b" }]);
});
it("loads more on a stale idle page by reading from the first page", async () => {
	const { requests, pages, invalidate } = boundary();
	pages.watch();
	const first = pages.refresh();
	requests[0].resolve(response(["a"], 1, 1));
	await first;
	invalidate({
		generationId: "hub-generation",
		sequence: 1,
		targets: [{ kind: "catalog", catalog: "projects", revision: 2 }],
	});
	requests[1].resolve(response(["a"], 1, 1));
	await until(pages, (state) => !state.loading);
	expect(pages.getSnapshot().stale).toBe(true);
	const more = pages.more();
	expect(requests[2].params.offset).toBe(0);
	requests[2].resolve(response(["b"], 0, 2));
	await more;
	expect(pages.getSnapshot()).toMatchObject({ stale: false, rows: [{ key: "b" }] });
});
it("retries a read that predates a generation change during the request", async () => {
	const { requests, pages, invalidate, requested } = boundary();
	pages.watch();
	const first = pages.refresh();
	invalidate({ generationId: "restarted", sequence: 1, targets: [] });
	requests[0].resolve(response(["old"]));
	await requested(2);
	expect(pages.getSnapshot().rows).toEqual([]);
	expect(pages.getSnapshot().stale).toBe(true);
	requests[1].resolve(response(["new"], 0, 1, 0, "restarted"));
	await first;
	expect(pages.getSnapshot().rows).toMatchObject([{ key: "new" }]);
	expect(pages.getSnapshot().stale).toBe(false);
});

it("requires a post-mutation read to satisfy its receipt even without notifications", async () => {
	const { requests, pages } = boundary();
	const first = pages.refresh();
	requests[0].resolve(response(["old"]));
	await first;
	const reload = pages.refreshAfter({
		generation_id: "hub-generation",
		targets: [{ kind: "catalog", catalog: "projects", revision: 3 }],
	});
	requests[1].resolve(response(["old"], 0, 2));
	await expect(reload).rejects.toThrow();
	expect(pages.getSnapshot().stale).toBe(true);
	expect(pages.getSnapshot().rows).toMatchObject([{ key: "old" }]);
	const retry = pages.refresh();
	requests[2].resolve(response(["new"], 0, 3));
	await retry;
	expect(pages.getSnapshot().rows).toMatchObject([{ key: "new" }]);
});
it("does not acknowledge a mutation when another read supersedes its verification", async () => {
	const { pages, requests } = boundary();
	const first = pages.refresh();
	requests[0].resolve(response(["old"]));
	await first;
	const mutation = pages.refreshAfter({
		generation_id: "hub-generation",
		targets: [{ kind: "catalog", catalog: "projects", revision: 3 }],
	});
	const refresh = pages.refresh();
	requests[2].resolve(response(["other"], 0, 1));
	await refresh;
	requests[1].resolve(response(["new"], 0, 3));
	await expect(mutation).rejects.toThrow();
});
it("revalidates after a notification races mutation verification, without repeating the mutation", async () => {
	const { pages, requests, invalidate } = boundary();
	pages.watch();
	const first = pages.refresh();
	requests[0].resolve(response(["old"]));
	await first;
	const mutation = pages.refreshAfter({
		generation_id: "hub-generation",
		targets: [{ kind: "catalog", catalog: "projects", revision: 3 }],
	});
	invalidate({
		generationId: "hub-generation",
		sequence: 12,
		targets: [{ kind: "catalog", catalog: "projects", revision: 3 }],
	});
	const retried = new Promise<void>((resolve) => {
		const stop = pages.subscribe(() => {
			if (pages.getSnapshot().loading && pages.getSnapshot().stale) {
				stop();
				resolve();
			}
		});
	});
	requests[1].resolve(response(["new"], 0, 3));
	await retried;
	expect(requests).toHaveLength(3);
	requests[2].resolve(response(["new"], 0, 3));
	await mutation;
	expect(pages.getSnapshot().stale).toBe(false);
	expect(pages.getSnapshot().error).toBeNull();
});

it("uses exact page bases for conditional refresh and preserves not-modified rows", async () => {
	const { pages, requests } = boundary();
	const first = pages.refresh();
	expect(requests[0].params).toEqual({
		representationVersion: 3,
		resource: "catalog",
		catalog: "projects",
		offset: 0,
		limit: 2,
	});
	requests[0].resolve(response(["a"], 1));
	await first;
	const more = pages.more();
	expect(requests[1].params.base).toBeUndefined();
	requests[1].resolve(response(["b"], 0, 1, 1));
	await more;
	const refresh = pages.refresh();
	expect(requests[2].params.base).toEqual({
		generationId: "hub-generation",
		revision: 1,
		etag: "etag-0-1",
	});
	requests[2].resolve({
		status: "not_modified",
		generationId: "hub-generation",
		revision: 1,
		etag: "etag-0-1",
	});
	await refresh;
	expect(pages.getSnapshot().rows.map((row) => row.key)).toEqual(["a", "b"]);
	expect(pages.getSnapshot().remaining).toBe(0);
	expect(pages.getSnapshot().error).toBeNull();
});

it("applies delta removals and container order before publishing refreshed rows", async () => {
	const { pages, requests } = boundary();
	const original = response(["a", "b"]);
	const graph = original.data as import("@evener/appwire-client").NavigationSnapshot;
	const first = pages.refresh();
	requests[0].resolve(original);
	await first;
	const refresh = pages.refresh();
	requests[1].resolve({
		status: "ok",
		representation: "delta",
		generationId: "hub-generation",
		revision: 2,
		etag: "changed",
		base: { generationId: "hub-generation", revision: 1, etag: "etag-0-1" },
		data: {
			metadata: { ...(graph.metadata as object), revision: 2 },
			upsertedEntities: [
				{
					...graph.entities[1],
					value: { key: "b", name: "Renamed", session_count: 2 },
				},
			],
			removedEntityKeys: [graph.entities[0].key],
			upsertedContainers: [{ ...graph.containers[0], children: [graph.entities[1].key] }],
			removedContainerKeys: [],
		},
	});
	await refresh;
	expect(pages.getSnapshot().rows).toEqual([{ key: "b", name: "Renamed", session_count: 2 }]);
	const next = pages.refresh();
	expect(requests[2].params.base?.etag).toBe("changed");
	requests[2].resolve(response(["b"], 0, 2));
	await next;
});

it("recovers a mismatched delta base with one unconditional read", async () => {
	const { pages, requests, requested } = boundary();
	const first = pages.refresh();
	requests[0].resolve(response(["old"]));
	await first;
	const refresh = pages.refresh();
	requests[1].resolve({
		status: "ok",
		representation: "delta",
		generationId: "hub-generation",
		revision: 2,
		etag: "next",
		base: { generationId: "other", revision: 1, etag: "wrong" },
		data: {
			upsertedEntities: [],
			removedEntityKeys: [],
			upsertedContainers: [],
			removedContainerKeys: [],
		},
	});
	await requested(3);
	expect(requests[2].params.base).toBeUndefined();
	expect(pages.getSnapshot().rows.map((row) => row.key)).toEqual(["old"]);
	requests[2].resolve(response(["fresh"], 0, 2));
	await refresh;
	expect(pages.getSnapshot().rows.map((row) => row.key)).toEqual(["fresh"]);
});

it("clears disappeared resources and never uses a tombstone as a delta base", async () => {
	const { pages, requests } = boundary();
	const first = pages.refresh();
	requests[0].resolve(response(["old"]));
	await first;
	const refresh = pages.refresh();
	requests[1].resolve({
		status: "gone",
		generationId: "hub-generation",
		revision: 2,
		etag: "gone",
	});
	await refresh;
	expect(pages.getSnapshot()).toMatchObject({
		rows: [],
		remaining: 0,
		stale: false,
		error: null,
	});
	const retry = pages.refresh();
	expect(requests[2].params.base).toBeUndefined();
	requests[2].resolve(response(["restored"], 0, 3));
	await retry;
	expect(pages.getSnapshot().rows.map((row) => row.key)).toEqual(["restored"]);
});

it("retains a mutation revision floor after a failed readback and manual retry", async () => {
	const { pages, requests } = boundary();
	const first = pages.refresh();
	requests[0].resolve(response(["old"]));
	await first;
	const mutation = pages.refreshAfter({
		generation_id: "hub-generation",
		targets: [{ kind: "catalog", catalog: "projects", revision: 3 }],
	});
	requests[1].resolve(response(["old"], 0, 2));
	await expect(mutation).rejects.toThrow();
	const retry = pages.refresh();
	requests[2].resolve(response(["still-old"], 0, 2));
	await retry;
	expect(pages.getSnapshot().stale).toBe(true);
	expect(pages.getSnapshot().rows.map((row) => row.key)).toEqual(["old"]);
});

it("does not satisfy a newer mutation receipt with a cached not-modified response", async () => {
	const { pages, requests } = boundary();
	const first = pages.refresh();
	requests[0].resolve(response(["a"], 0, 1));
	await first;
	const mutation = pages.refreshAfter({
		generation_id: "hub-generation",
		targets: [{ kind: "catalog", catalog: "projects", revision: 2 }],
	});
	requests[1].resolve({
		status: "not_modified",
		generationId: "hub-generation",
		revision: 1,
		etag: "etag-0-1",
	});
	await expect(mutation).rejects.toThrow();
	expect(pages.getSnapshot().stale).toBe(true);
});

it("does not let a conditional response clear a newer notification", async () => {
	const { pages, requests, invalidate, requested } = boundary();
	pages.watch();
	const first = pages.refresh();
	requests[0].resolve(response(["a"], 0, 1));
	await first;
	const next = pages.refresh();
	invalidate({
		generationId: "hub-generation",
		sequence: 1,
		targets: [{ kind: "catalog", catalog: "projects", revision: 2 }],
	});
	requests[1].resolve({
		status: "not_modified",
		generationId: "hub-generation",
		revision: 1,
		etag: "etag-0-1",
	});
	await requested(3);
	expect(pages.getSnapshot().stale).toBe(true);
	requests[2].resolve(response(["b"], 0, 2));
	await next;
	expect(pages.getSnapshot().stale).toBe(false);
	expect(pages.getSnapshot().rows).toMatchObject([{ key: "b" }]);
	expect(requests).toHaveLength(3);
});

it("keeps every loaded page when a conditional refresh finds the list unchanged", async () => {
	const { pages, requests } = boundary();
	const first = pages.refresh();
	requests[0].resolve(response(["a"], 2, 1));
	await first;
	const more = pages.more();
	requests[1].resolve(response(["b"], 1, 1, 1));
	await more;
	const refresh = pages.refresh();
	requests[2].resolve({
		status: "not_modified",
		generationId: "hub-generation",
		revision: 1,
		etag: "etag-0-1",
	});
	await refresh;
	expect(pages.getSnapshot().rows.map((row) => row.key)).toEqual(["a", "b"]);
	const again = pages.more();
	expect(requests[3].params.offset).toBe(2);
	requests[3].resolve(response(["c"], 0, 1, 2));
	await again;
	expect(pages.getSnapshot().rows.map((row) => row.key)).toEqual(["a", "b", "c"]);
});

it("re-reads the pin catalog when a section changes and does not spin on an older hub revision", async () => {
	const requests: NavigationReadParams[] = [];
	let notify: (event: AnyNotification) => void = () => {};
	const client: ConversationClientLike = Object.assign(new FakeClient("ready"), {
		onNotification: (listener) => {
			notify = listener;
			return () => {};
		},
		request: async (_method, params) => {
			const p = params as NavigationReadParams;
			requests.push(p);
			const offset = p.offset ?? 0;
			return wireSnapshot(
				p,
				{
					pin_sections: [
						{
							id: offset ? "two" : "one",
							name: offset ? "Later" : "First",
							count: 1,
						},
					],
					remaining: offset ? 0 : 1,
				},
				`pin-${offset}`,
				1,
				"hub-generation",
			) as never;
		},
	} as Omit<ConversationClientLike, "state" | "onReady" | "onStateChange">);
	const pages = new NavigationPages<{
		id: string;
		name: string;
		count: number;
	}>(client, { resource: "pin_catalog" }, "pin_sections", (r) => r.id, 1);
	pages.watch();
	await pages.refresh();
	await pages.more();
	expect(pages.getSnapshot().rows.map((r) => r.id)).toEqual(["one", "two"]);
	expect(requests.map((p) => p.offset)).toEqual([0, 1]);
	notify({
		method: "evener/navigation/invalidated",
		params: {
			generationId: "hub-generation",
			sequence: 1,
			targets: [{ kind: "pin_catalog", revision: 2 }],
		},
	});
	expect(pages.getSnapshot().stale).toBe(true);
	expect(requests.map((p) => p.offset)).toEqual([0, 1, 0]);
	// The hub keeps serving revision 1 below the notified 2: stay stale
	// without spinning on further reads. Loading more then catches up from
	// the first page instead of paging past stale rows.
	await until(pages, (state) => !state.loading);
	expect(pages.getSnapshot().stale).toBe(true);
	await pages.more();
	expect(requests.map((p) => p.offset)).toEqual([0, 1, 0, 0]);
	expect(pages.getSnapshot().stale).toBe(true);
});

describe("updating copy", () => {
	const page = (overrides: Partial<Parameters<typeof updating>[0]>) => ({
		loading: false,
		error: null,
		stale: false,
		remaining: 0,
		...overrides,
	});
	it("stays visible while a stale re-read is in flight", () => {
		// Follow-up to #1484: a stale re-read publishes stale, then loading,
		// without clearing stale; screens with no loading cue must keep saying so.
		expect(updating(page({ stale: true, loading: true }))).toBe(true);
	});
	it("shows for a stale page waiting on its next trigger", () => {
		expect(updating(page({ stale: true }))).toBe(true);
	});
	it("gives way to an error on screen", () => {
		expect(updating(page({ stale: true, error: "offline" }))).toBe(false);
		expect(updating(page({ stale: true, loading: true, error: "offline" }))).toBe(false);
	});
	it("says nothing about a fresh page, loading or not", () => {
		expect(updating(page({ loading: true }))).toBe(false);
		expect(updating(page({}))).toBe(false);
	});
});
