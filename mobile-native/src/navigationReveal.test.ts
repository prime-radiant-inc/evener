import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { expect, it } from "vitest";
import { wireSnapshot } from "@evener/appwire-client/testing/navigation";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { NavigationPages } from "./navigationPages";
import { locateSession, revealNavigationRow } from "./navigationReveal";

it.each([
	[
		{ project_key: "p", tier: "archived" },
		{ resource: "project_page", projectKey: "p", tier: "archived" },
	],
	// A key held by several catalogs reads the first by key alone; the
	// location names the catalog whose project holds the session (#3799).
	[
		{ project_key: "no-project", catalog: "test_runs", tier: "current" },
		{ resource: "project_page", projectKey: "no-project", catalog: "test_runs", tier: "current" },
	],
	[{ pin_section_id: "pin" }, { resource: "pin_section", sectionId: "pin" }],
	[{ tier: "needs_you" }, { resource: "section", section: "needs_you" }],
	[{ tier: "live" }, { resource: "section", section: "live" }],
])("locates the actual session container: %j", async (fields, params) => {
	const client = Object.assign(new FakeClient("ready"), {
		request: async (method: string, args: unknown) => {
			expect(method).toBe("evener/navigation/read");
			expect(args).toEqual({
				representationVersion: 3,
				resource: "location",
				ref: "child",
			});
			const snapshot = wireSnapshot(args as never, {
				ref: "child",
				session: { ref: "child", project: "Project" },
				...fields,
			});
			return {
				...snapshot,
				data: {
					...(snapshot.data as object),
					metadata: {
						...(snapshot.data as { metadata: object }).metadata,
						...fields,
					},
				},
			};
		},
	} as Omit<ConversationClientLike, "state" | "onReady" | "onStateChange">) as ConversationClientLike;
	expect(await locateSession(client, "child")).toMatchObject({
		params,
		ref: "child",
		revealRef: "child",
	});
});
/** A hub answering one location, for a session of the given kind. */
const locationClient = (fields: Record<string, unknown>, kind = "session") =>
	Object.assign(new FakeClient("ready"), {
		request: async () => {
			const snapshot = wireSnapshot({ representationVersion: 3, resource: "location", ref: "child" } as never, {
				ref: "child",
				session: { ref: "child", project: "Project", kind },
				...fields,
			});
			return {
				...snapshot,
				data: {
					...(snapshot.data as object),
					metadata: { ...(snapshot.data as { metadata: object }).metadata, ...fields },
				},
			};
		},
		onNotification: () => () => {},
	} as Omit<ConversationClientLike, "state" | "onReady" | "onStateChange">) as ConversationClientLike;
it.each([
	["its root", { top_level: false, top_level_ref: "root", project_key: "p", tier: "current" }, "root"],
	[
		"a cluster row",
		{ top_level: false, top_level_ref: "cluster-row", project_key: "p", tier: "recent" },
		"cluster-row",
	],
])("reveals a subagent by %s, not by the subagent itself", async (_name, fields, revealRef) => {
	expect(await locateSession(locationClient(fields, "subagent"), "child")).toMatchObject({
		ref: "child",
		revealRef,
	});
});
// Navigation v3 lists only top-level rows, and the archived list keeps a fork
// original inside its continuation's row, so a nested fork original has no
// row of its own in any tier: /project lands on the top-level row carrying it.
it.each(["current", "recent", "archived"])(
	"reveals a nested fork original through the top-level row that carries it (%s)",
	async (tier) => {
		const fields = { top_level: false, top_level_ref: "root", project_key: "p", tier };
		expect(await locateSession(locationClient(fields, "fork"), "child")).toMatchObject({
			ref: "child",
			revealRef: "root",
		});
	},
);
it("reveals a top-level fork original by its own row", async () => {
	const fields = { top_level: true, top_level_ref: "child", project_key: "p", tier: "current" };
	expect(await locateSession(locationClient(fields, "fork"), "child")).toMatchObject({
		ref: "child",
		revealRef: "child",
	});
});
// The hub names the row carrying every location; one that names none is
// refused like any other malformed location, so a reveal never lacks a row.
it("refuses a location that names no top-level row", async () => {
	const fields = { top_level: false, top_level_ref: undefined, project_key: "p", tier: "current" };
	await expect(locateSession(locationClient(fields, "fork"), "child")).rejects.toThrow(
		"This session could not be located. Try again.",
	);
});
it.each(["local:orphan", "host:remote-subagent"])(
	"shows the existing could-not-be-located message for a gone ref: %s",
	async (ref) => {
		const client = Object.assign(new FakeClient("ready"), {
			request: async () => ({ status: "gone", generationId: "g", revision: 1, etag: '"one"' }),
			onNotification: () => () => {},
		} as Omit<ConversationClientLike, "state" | "onReady" | "onStateChange">) as ConversationClientLike;
		await expect(locateSession(client, ref)).rejects.toThrow("This session could not be located. Try again.");
	},
);
it.each([
	{ status: "missing" },
	{ status: "ok", data: { ref: "other", session: { ref: "other" } } },
	{ status: "ok", data: { ref: "child" } },
])("rejects missing or mismatched locations", async (response) => {
	const client = Object.assign(new FakeClient("ready"), {
		request: async () => response,
		onNotification: () => () => {},
	} as Omit<ConversationClientLike, "state" | "onReady" | "onStateChange">) as ConversationClientLike;
	await expect(locateSession(client, "child")).rejects.toThrow();
});
interface Row {
	ref: string;
}
function pages(
	read: (offset: number) => unknown,
	params: {
		resource: "section" | "pin_section";
		section?: "live";
		sectionId?: string;
	} = { resource: "section", section: "live" },
) {
	const client = Object.assign(new FakeClient("ready"), {
		request: async (_method: string, p: { offset: number }) => read(p.offset),
		onNotification: () => () => {},
	} as Omit<ConversationClientLike, "state" | "onReady" | "onStateChange">) as ConversationClientLike;
	return new NavigationPages<Row>(client, params, "sessions", (r) => r.ref, 1);
}
const response = (
	sessions: Row[],
	remaining: number,
	revision = 1,
	offset = 0,
	resource: "section" | "pin_section" = "section",
) =>
	wireSnapshot(
		{
			representationVersion: 3,
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
it("loads later pages and reveals the flat destination", async () => {
	const offsets: number[] = [];
	const list = pages((offset) => {
		offsets.push(offset);
		return response(offset === 0 ? [{ ref: "other" }] : [{ ref: "child" }], offset === 0 ? 1 : 0, 1, offset);
	});
	expect(
		await revealNavigationRow(
			list,
			"child",
			(r) => r.ref,
			() => true,
		),
	).toBe(true);
	expect(offsets).toEqual([0, 1]);
});
it("says so once every page is read and the row isn't there", async () => {
	const list = pages(() => response([{ ref: "other" }], 0));
	await expect(
		revealNavigationRow(
			list,
			"child",
			(r) => r.ref,
			() => true,
		),
	).rejects.toThrow("The session is not in the returned list. It may have moved.");
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
			() => current,
		),
	).toBe(false);
	expect(calls).toBe(1);
});
it("rejects a revision change while paging rather than mixing pages", async () => {
	let calls = 0;
	const list = pages(() => response([{ ref: String(++calls) }], 1, calls));
	await expect(
		revealNavigationRow(
			list,
			"child",
			(r) => r.ref,
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
] as const)("invalidates the located container when its revision changes", async (params, target) => {
	let notify: Parameters<ConversationClientLike["onNotification"]>[0] = () => {};
	const client = Object.assign(new FakeClient("ready"), {
		request: async () => response([{ ref: "child" }], 0, 1, 0, params.resource),
		onNotification: (listener: typeof notify) => {
			notify = listener;
			return () => {};
		},
	} as Omit<ConversationClientLike, "state" | "onReady" | "onStateChange">) as ConversationClientLike;
	const list = new NavigationPages<Row>(client, params, "sessions", (r) => r.ref);
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
});
it("rejects a delayed location after its screen was left", async () => {
	let release!: (value: unknown) => void;
	const client = Object.assign(new FakeClient("ready"), {
		request: () =>
			new Promise<unknown>((resolve) => {
				release = resolve;
			}),
		onNotification: () => () => {},
	} as Omit<ConversationClientLike, "state" | "onReady" | "onStateChange">) as ConversationClientLike;
	const request = new AbortController();
	const lookup = locateSession(client, "child", request.signal);
	request.abort();
	release({
		status: "ok",
		data: { ref: "child", tier: "live", session: { ref: "child" } },
	});
	await expect(lookup).rejects.toThrow();
});
