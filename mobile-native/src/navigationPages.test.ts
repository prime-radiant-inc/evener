import { describe, expect, it } from "vitest";
import type {
	AnyNotification,
	NavigationInvalidatedPayload,
	NavigationReadParams,
	NavigationReadResponse,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import { wireV2 } from "../../cmd/evener-hub/frontend/src/stores/navigation/testing";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { NavigationPages } from "./navigationPages";

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

function boundary(resource: "catalog" | "section" = "catalog") {
	const requests: {
		params: NavigationReadParams;
		resolve: (value: NavigationReadResponse) => void;
	}[] = [];
	const arrivals = new Map<number, () => void>();
	let notify: (event: AnyNotification) => void = () => {};
	const client: ConversationClientLike = {
		request: (_method, params) =>
			new Promise((resolve) => {
				requests.push({ params: params as NavigationReadParams, resolve });
				arrivals.get(requests.length)?.();
			}),
		onNotification: (listener) => {
			notify = listener;
			return () => {
				notify = () => {};
			};
		},
	};
	return {
		requests,
		requested: (count: number) =>
			requests.length >= count
				? Promise.resolve()
				: new Promise<void>((resolve) => arrivals.set(count, resolve)),
		invalidate: (payload: NavigationInvalidatedPayload) =>
			notify({ method: "evener/navigation/invalidated", params: payload }),
		pages: new NavigationPages<{ key: string; ref?: string }>(
			client,
			resource === "catalog"
				? { resource: "catalog", catalog: "projects" }
				: { resource: "section", section: "live" },
			resource === "catalog" ? "projects" : "sessions",
			(row) => row.ref ?? row.key,
			2,
		),
	};
}
function response(
	keys: string[],
	remaining = 0,
	revision = 1,
	offset = 0,
	generation = "hub-generation",
): NavigationReadResponse {
	return wireV2(
		{
			representationVersion: 2,
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
		expect(pages.getSnapshot().rows.map((row) => row.key)).toEqual([
			"a",
			"b",
			"c",
		]);
		expect(pages.getSnapshot().remaining).toBe(0);
	});
	it("refuses to combine revisions and requires refresh", async () => {
		const { requests, pages } = boundary();
		const first = pages.refresh();
		requests[0].resolve(response(["a"], 1));
		await first;
		const next = pages.more();
		requests[1].resolve(response(["b"], 0, 2, 1));
		await next;
		expect(pages.getSnapshot().rows.map((row) => row.key)).toEqual(["a"]);
		expect(pages.getSnapshot().stale).toBe(true);
		await pages.more();
		expect(requests).toHaveLength(2);
		const refresh = pages.refresh();
		requests[2].resolve(response(["b"], 0, 2));
		await refresh;
		expect(pages.getSnapshot().rows.map((row) => row.key)).toEqual(["b"]);
		expect(pages.getSnapshot().stale).toBe(false);
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

it("marks relevant updates stale without replacing visible rows", async () => {
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
	invalidate({
		generationId: "hub-generation",
		sequence: 2,
		targets: [{ kind: "catalog", catalog: "projects", revision: 2 }],
	});
	expect(pages.getSnapshot().stale).toBe(true);
	expect(pages.getSnapshot().rows).toMatchObject([{ key: "a" }]);
	await pages.more();
	expect(requests).toHaveLength(1);
	const refresh = pages.refresh();
	requests[1].resolve(response(["b"], 0, 2));
	await refresh;
	expect(pages.getSnapshot().stale).toBe(false);
	stop();
	invalidate({ generationId: "other", sequence: 1, targets: [] });
	expect(pages.getSnapshot().stale).toBe(false);
});
it("does not let an old in-flight read clear a newer invalidation", async () => {
	const { requests, pages, invalidate } = boundary();
	pages.watch();
	const first = pages.refresh();
	invalidate({
		generationId: "hub-generation",
		sequence: 1,
		targets: [{ kind: "catalog", catalog: "projects", revision: 3 }],
	});
	requests[0].resolve(response(["old"], 0, 2));
	await first;
	expect(pages.getSnapshot().stale).toBe(true);
	expect(pages.getSnapshot().rows).toEqual([]);
	const refresh = pages.refresh();
	requests[1].resolve(response(["fresh"], 0, 3));
	await refresh;
	expect(pages.getSnapshot().stale).toBe(false);
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
});
it("requires refresh after a sequence gap even with unrelated targets", async () => {
	const { requests, pages, invalidate } = boundary();
	pages.watch();
	const first = pages.refresh();
	requests[0].resolve(response(["a"]));
	await first;
	invalidate({ generationId: "hub-generation", sequence: 1, targets: [] });
	invalidate({ generationId: "hub-generation", sequence: 3, targets: [] });
	expect(pages.getSnapshot().stale).toBe(true);
});

it("establishes a restarted hub generation from an explicit refresh", async () => {
	const { requests, pages, invalidate } = boundary();
	pages.watch();
	const first = pages.refresh();
	requests[0].resolve(response(["a"], 0, 10));
	await first;
	invalidate({
		generationId: "hub-generation",
		sequence: 1,
		targets: [{ kind: "catalog", catalog: "projects", revision: 10 }],
	});
	const refresh = pages.refresh();
	requests[1].resolve({
		...response(["new"], 0, 1, 0, "restarted"),
	});
	await refresh;
	expect(pages.getSnapshot().rows).toMatchObject([{ key: "new" }]);
	expect(pages.getSnapshot().stale).toBe(false);
});
it("rejects a response from before a generation change during the request", async () => {
	const { requests, pages, invalidate } = boundary();
	pages.watch();
	const first = pages.refresh();
	invalidate({ generationId: "restarted", sequence: 1, targets: [] });
	requests[0].resolve(response(["old"]));
	await first;
	expect(pages.getSnapshot().rows).toEqual([]);
	expect(pages.getSnapshot().stale).toBe(true);
});

it("retains session truncation across pages and clears it on refresh", async () => {
	const { requests, pages } = boundary("section");
	const page = (
		ref: string,
		offset: number,
		remaining: number,
		truncated: boolean,
	) =>
		wireV2(
			{
				representationVersion: 2,
				resource: "section",
				section: "live",
				offset,
				limit: 2,
			},
			{ sessions: [{ ref }], remaining, truncated },
			`etag-${offset}`,
			1,
			"hub-generation",
		);
	const first = pages.refresh();
	requests[0].resolve(page("a", 0, 1, true));
	await first;
	expect(pages.getSnapshot().truncated).toBe(true);
	const next = pages.more();
	requests[1].resolve(page("b", 1, 0, false));
	await next;
	expect(pages.getSnapshot().truncated).toBe(true);
	const refresh = pages.refresh();
	requests[2].resolve(page("c", 0, 0, false));
	await refresh;
	expect(pages.getSnapshot().truncated).toBe(false);
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
		representationVersion: 2,
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
	expect(pages.getSnapshot().rows.map((row) => row.key)).toEqual(["a"]);
	expect(pages.getSnapshot().remaining).toBe(1);
	expect(pages.getSnapshot().error).toBeNull();
});

it("applies delta removals and container order before publishing refreshed rows", async () => {
	const { pages, requests } = boundary();
	const original = response(["a", "b"]);
	const graph =
		original.data as import("../../cmd/evener-hub/frontend/src/protocol/types.gen").NavigationSnapshot;
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
			upsertedContainers: [
				{ ...graph.containers[0], children: [graph.entities[1].key] },
			],
			removedContainerKeys: [],
		},
	});
	await refresh;
	expect(pages.getSnapshot().rows).toEqual([
		{ key: "b", name: "Renamed", session_count: 2 },
	]);
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
	const { pages, requests, invalidate } = boundary();
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
	await next;
	expect(pages.getSnapshot().stale).toBe(true);
});

it("resets paging offset when a conditional refresh retains the first page", async () => {
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
	const again = pages.more();
	expect(requests[3].params.offset).toBe(1);
	requests[3].resolve(response(["b"], 1, 1, 1));
	await again;
});

it("pages the pin catalog and invalidates it when a section changes", async () => {
	const requests: NavigationReadParams[] = [];
	let notify: (event: AnyNotification) => void = () => {};
	const client: ConversationClientLike = {
		onNotification: (listener) => {
			notify = listener;
			return () => {};
		},
		request: async (_method, params) => {
			const p = params as NavigationReadParams;
			requests.push(p);
			const offset = p.offset ?? 0;
			return wireV2(
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
	};
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
	await pages.more();
	expect(requests).toHaveLength(2);
});
