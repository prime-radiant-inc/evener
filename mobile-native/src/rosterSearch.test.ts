import { describe, expect, it } from "vitest";
import type {
	ThreadListParams,
	ThreadListResponse,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import type { RosterEntry } from "../../mobile/src/services/roster";
import { createRosterService } from "../../mobile/src/services/roster";
import { RosterSearch, searchProjectLabels } from "./rosterSearch";

function boundary() {
	const listeners = new Set<
		Parameters<ConversationClientLike["onNotification"]>[0]
	>();
	const waiting = new Map<number, () => void>();
	const requests: {
		params: ThreadListParams;
		resolve: (result: ThreadListResponse) => void;
		reject: (cause: Error) => void;
	}[] = [];
	const client: ConversationClientLike = {
		request: (_method, params) =>
			new Promise((resolve, reject) => {
				requests.push({ params: params as ThreadListParams, resolve, reject });
				waiting.get(requests.length - 1)?.();
			}),
		onNotification: (listener) => {
			listeners.add(listener);
			return () => {
				listeners.delete(listener);
			};
		},
	};
	return {
		requests,
		client,
		notify: () => {
			for (const listener of listeners)
				listener({
					method: "evener/navigation/invalidated",
					params: { generationId: "g", sequence: 1, targets: [] },
				} as never);
		},
		requestAt: async (index: number) => {
			if (!requests[index])
				await new Promise<void>((resolve) => {
					waiting.set(index, resolve);
				});
			return requests[index];
		},
		search: new RosterSearch(createRosterService(client, 50)),
	};
}
describe("roster search", () => {
	const row = (ref: string, project: string): RosterEntry => ({
		ref,
		title: ref,
		project,
		status: "idle",
		updatedAt: 0,
		attention: "recent",
	});
	it("adds enough path context only when search destinations share a basename", () => {
		const labels = searchProjectLabels([
			row("one", "/Users/jesse/client-a/app"),
			row("two", "/Users/jesse/client-b/app"),
			row("three", "/Users/jesse/other/notes"),
		]);
		expect(labels.get("one")).toBe("client-a / app");
		expect(labels.get("two")).toBe("client-b / app");
		expect(labels.get("three")).toBe("notes");
	});
	it("keeps ordinary sessions quiet when only their project repeats", () => {
		const labels = searchProjectLabels([
			row("one", "/Users/jesse/client/app"),
			row("two", "/Users/jesse/client/app"),
		]);
		expect(labels.get("one")).toBe("app");
		expect(labels.get("two")).toBe("app");
	});
	it("distinguishes duplicate projects while preserving repeated rows", () => {
		const labels = searchProjectLabels([
			row("one", "/Users/jesse/team-a/app"),
			row("two", "/Users/jesse/team-a/app"),
			row("three", "/Users/jesse/team-b/app"),
			row("four", "/Users/jesse/team-b/app"),
		]);
		expect(labels.get("one")).toBe("team-a / app");
		expect(labels.get("two")).toBe("team-a / app");
		expect(labels.get("three")).toBe("team-b / app");
		expect(labels.get("four")).toBe("team-b / app");
	});
	it("leaves an empty project path empty", () => {
		expect(searchProjectLabels([row("empty", "")]).get("empty")).toBe("");
	});
	it("preserves raw paths through RosterSearch while adding display labels", async () => {
		const raw = row("one", "/Users/jesse/team-a/app");
		const search = new RosterSearch({
			list: async () => ({ threads: [raw], hasMore: false }),
			refresh: async () => {},
		});
		await search.load();
		expect(search.getSnapshot().rows[0]).toMatchObject({
			project: "/Users/jesse/team-a/app",
			projectLabel: "app",
		});
	});
	it("keeps deeper context when duplicate paths share their immediate parent", () => {
		const labels = searchProjectLabels([
			row("one", "/Users/jesse/team-a/client/app"),
			row("two", "/Users/jesse/team-b/client/app"),
		]);
		expect(labels.get("one")).toBe("team-a / client / app");
		expect(labels.get("two")).toBe("team-b / client / app");
	});
	it("refreshes the current query after invalidations during an initial load and coalesces in-flight changes", async () => {
		const f = boundary();
		const stop = f.search.watch(f.client);
		const initial = f.search.load(" needle ");
		f.notify();
		f.notify();
		expect(f.requests).toHaveLength(1);
		f.requests[0].resolve({ data: [] });
		await initial;
		const refresh = await f.requestAt(1);
		expect(refresh.params.searchTerm).toBe("needle");
		f.notify();
		f.notify();
		expect(f.requests).toHaveLength(2);
		refresh.resolve({ data: [] });
		const followup = await f.requestAt(2);
		expect(f.requests).toHaveLength(3);
		expect(followup.params.searchTerm).toBe("needle");
		const settled = new Promise<void>((resolve) => {
			const off = f.search.subscribe(() => {
				if (!f.search.getSnapshot().loading) {
					off();
					resolve();
				}
			});
		});
		followup.resolve({ data: [] });
		await settled;
		stop();
	});
	it("preserves current rows while refreshing and applies changed server names", async () => {
		const f = boundary();
		const stop = f.search.watch(f.client);
		const thread = (name: string) =>
			({
				id: "id",
				evener: { ref: "local:id" },
				name,
				status: { type: "notLoaded" },
			}) as ThreadListResponse["data"][number];
		const initial = f.search.load();
		f.requests[0].resolve({ data: [thread("Before")] });
		await initial;
		f.notify();
		const update = await f.requestAt(1);
		expect(f.search.getSnapshot().rows[0].title).toBe("Before");
		const changed = new Promise<void>((resolve) => {
			const off = f.search.subscribe(() => {
				if (f.search.getSnapshot().rows[0]?.title === "After") {
					off();
					resolve();
				}
			});
		});
		update.resolve({ data: [thread("After")] });
		await changed;
		expect(f.search.getSnapshot().rows[0].title).toBe("After");
		stop();
	});
	it("unsubscribes on blur and rejects the late watched result", async () => {
		const f = boundary();
		const stop = f.search.watch(f.client);
		const initial = f.search.load("active");
		f.notify();
		stop();
		f.notify();
		f.requests[0].resolve({ data: [] });
		await initial;
		expect(f.requests).toHaveLength(1);
		expect(f.search.getSnapshot()).toMatchObject({
			loading: false,
			error: null,
		});
	});
	it("ignores an older query completing after a newer one", async () => {
		const { requests, search } = boundary();
		const first = search.load("old");
		const second = search.load("new");
		expect(requests.map((r) => r.params.searchTerm)).toEqual(["old", "new"]);
		requests[1].resolve({ data: [] });
		await second;
		requests[0].reject(new Error("old failed"));
		await first;
		expect(search.getSnapshot()).toMatchObject({
			query: "new",
			loading: false,
			error: null,
		});
	});
	it("invalidates in-flight work when leaving a hub", async () => {
		const { requests, search } = boundary();
		const pending = search.load("query");
		search.cancel();
		requests[0].reject(new Error("connection closed"));
		await pending;
		expect(search.getSnapshot()).toMatchObject({ loading: false, error: null });
	});
	it("retries the same query explicitly after failure", async () => {
		const { requests, search } = boundary();
		const pending = search.load(" query ");
		requests[0].reject(new Error("offline"));
		await pending;
		expect(search.getSnapshot().error).toBeTruthy();
		const retry = search.load();
		expect(requests[1].params.searchTerm).toBe("query");
		requests[1].resolve({ data: [] });
		await retry;
		expect(search.getSnapshot().error).toBeNull();
	});
});
