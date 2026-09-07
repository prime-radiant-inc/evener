import { expect, it } from "vitest";
import { wireV2 } from "../../cmd/evener-hub/frontend/src/stores/navigation/testing";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { NavigationPages } from "./navigationPages";
import { locateSession, revealNavigationRow } from "./navigationReveal";

it.each([
	[
		{ project_key: "p", tier: "archived" },
		{ resource: "project_page", projectKey: "p", tier: "archived" },
	],
	[{ pin_section_id: "pin" }, { resource: "pin_section", sectionId: "pin" }],
	[{ tier: "needs_you" }, { resource: "section", section: "needs_you" }],
	[{ tier: "live" }, { resource: "section", section: "live" }],
])("locates the actual session container: %j", async (fields, params) => {
	const client = {
		request: async (method: string, args: unknown) => {
			expect(method).toBe("evener/navigation/read");
			expect(args).toEqual({
				representationVersion: 2,
				resource: "location",
				ref: "child",
			});
			const v2 = wireV2(args as never, {
				ref: "child",
				session: { ref: "child", project: "Project" },
				...fields,
			});
			return {
				...v2,
				data: {
					...(v2.data as object),
					metadata: {
						...(v2.data as { metadata: object }).metadata,
						...fields,
					},
				},
			};
		},
	} as ConversationClientLike;
	expect(await locateSession(client, "child")).toMatchObject({
		params,
		ref: "child",
	});
});
it.each([
	{ status: "missing" },
	{ status: "ok", data: { ref: "other", session: { ref: "other" } } },
	{ status: "ok", data: { ref: "child" } },
])("rejects missing or mismatched locations", async (response) => {
	const client = {
		request: async () => response,
		onNotification: () => () => {},
	} as ConversationClientLike;
	await expect(locateSession(client, "child")).rejects.toThrow();
});
interface Row {
	ref: string;
	children?: Row[];
}
function pages(
	read: (offset: number) => unknown,
	params: {
		resource: "section" | "pin_section";
		section?: "live";
		sectionId?: string;
	} = { resource: "section", section: "live" },
) {
	const client = {
		request: async (_method: string, p: { offset: number }) => read(p.offset),
		onNotification: () => () => {},
	} as ConversationClientLike;
	return new NavigationPages<Row>(client, params, "sessions", (r) => r.ref, 1);
}
const response = (
	sessions: Row[],
	remaining: number,
	revision = 1,
	offset = 0,
	resource: "section" | "pin_section" = "section",
) =>
	wireV2(
		{
			representationVersion: 2,
			resource,
			...(resource === "section" ? { section: "live" } : { sectionId: "pin" }),
			offset,
			limit: 1,
		},
		{ sessions, remaining },
		`etag-${revision}`,
		revision,
		"g",
	);
it("loads later pages and expands the nested destination's ancestors", async () => {
	const offsets: number[] = [];
	const list = pages((offset) => {
		offsets.push(offset);
		return response(
			offset === 0
				? [{ ref: "other" }]
				: [{ ref: "root", children: [{ ref: "child" }] }],
			offset === 0 ? 1 : 0,
			1,
			offset,
		);
	});
	expect(
		await revealNavigationRow(
			list,
			"child",
			(r) => r.ref,
			(r) => r.children ?? [],
			() => true,
		),
	).toEqual(["root", "child"]);
	expect(offsets).toEqual([0, 1]);
});
it("does not continue paging after leaving", async () => {
	let current = true,
		calls = 0;
	const list = pages(() => {
		calls++;
		current = false;
		return response([{ ref: "other" }], 1);
	});
	expect(
		await revealNavigationRow(
			list,
			"child",
			(r) => r.ref,
			undefined,
			() => current,
		),
	).toBeNull();
	expect(calls).toBe(1);
});
it("rejects a revision change rather than mixing a false path", async () => {
	let calls = 0;
	const list = pages(() => response([{ ref: String(++calls) }], 1, calls));
	await expect(
		revealNavigationRow(
			list,
			"child",
			(r) => r.ref,
			undefined,
			() => true,
		),
	).rejects.toThrow();
	expect(calls).toBe(2);
});
it.each([
	[
		{ resource: "section", section: "live" },
		{ kind: "section", section: "live" },
	],
	[
		{ resource: "pin_section", sectionId: "pin" },
		{ kind: "pin_section", sectionId: "pin" },
	],
] as const)(
	"invalidates the located container when its revision changes",
	async (params, target) => {
		let notify: Parameters<ConversationClientLike["onNotification"]>[0] =
			() => {};
		const client = {
			request: async () =>
				response([{ ref: "child" }], 0, 1, 0, params.resource),
			onNotification: (listener: typeof notify) => {
				notify = listener;
				return () => {};
			},
		} as ConversationClientLike;
		const list = new NavigationPages<Row>(
			client,
			params,
			"sessions",
			(r) => r.ref,
		);
		list.watch();
		await list.refresh();
		notify({
			method: "evener/navigation/invalidated",
			params: {
				generationId: "g",
				sequence: 1,
				targets: [{ ...target, revision: 2 }],
			},
		});
		expect(list.getSnapshot().stale).toBe(true);
	},
);
it("rejects a delayed location after its screen was left", async () => {
	let release!: (value: unknown) => void;
	const client = {
		request: () =>
			new Promise<unknown>((resolve) => {
				release = resolve;
			}),
		onNotification: () => () => {},
	} as ConversationClientLike;
	const request = new AbortController();
	const lookup = locateSession(client, "child", request.signal);
	request.abort();
	release({
		status: "ok",
		data: { ref: "child", tier: "live", session: { ref: "child" } },
	});
	await expect(lookup).rejects.toThrow();
});
