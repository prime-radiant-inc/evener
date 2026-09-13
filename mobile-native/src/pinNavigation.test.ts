import { describe, expect, it } from "vitest";
import type {
	AnyNotification,
	NavigationReadParams,
} from "../../appwire-client/typescript/types.gen";
import { wireV2 } from "../../cmd/evener-hub/frontend/src/stores/navigation/testing";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import type { NavigationActionCheckpoint } from "./navigationActionRepository";
import { NavigationPages } from "./navigationPages";
import { readPinLocation, refreshPinNavigation } from "./pinNavigation";

function boundary({
	locationGeneration = "g",
	catalogGeneration = "g",
	catalogRevision = 2,
	missing = false,
} = {}) {
	const calls: NavigationReadParams[] = [];
	const client = {
		onNotification: () => () => {},
		request: async (_method: string, p: NavigationReadParams) => {
			calls.push(p);
			if (p.resource === "location") {
				if (missing)
					return {
						status: "gone",
						generationId: locationGeneration,
						revision: 4,
						etag: "gone",
					};
				const response = wireV2(
					p,
					{
						ref: p.ref,
						top_level_ref: p.ref,
						top_level: true,
						pin_section_id: "focus",
						session: {
							ref: p.ref,
							host_id: "local",
							session_id: "s",
							title: "Session",
							project: "p",
							state: "idle",
							kind: "session",
							live: true,
						},
					},
					"location",
					4,
					locationGeneration,
				);
				(response.data as { metadata: Record<string, unknown> }).metadata.pin_section_id =
					"focus";
				return response;
			}
			const offset = p.offset ?? 0;
			return wireV2(
				p,
				{
					pin_sections: offset
						? [{ id: "focus", name: "Focus", count: 1 }]
						: [{ id: "later", name: "Later", count: 0 }],
					remaining: offset ? 0 : 1,
				},
				`catalog-${offset}`,
				catalogRevision,
				catalogGeneration,
			);
		},
	} as unknown as ConversationClientLike;
	const pages = new NavigationPages<{
		id: string;
		name: string;
		count: number;
	}>(client, { resource: "pin_catalog" }, "pin_sections", (s) => s.id, 1);
	return { client, pages, calls };
}
const checkpoint: NavigationActionCheckpoint = {
	id: "operation",
	operation: {
		kind: "assignPin",
		params: { sessionRef: "local:s", sectionId: "focus" },
	},
	receipt: {
		generation_id: "g",
		targets: [
			{ kind: "pin_catalog", revision: 2 },
			{ kind: "project", projectKey: "p", revision: 90 },
		],
	},
};
describe("native pin navigation readback", () => {
	it.each([false, true])(
		"bounds receipt readback when notification gaps continue: %s",
		async (repeatGap) => {
			let notify: (event: AnyNotification) => void = () => {};
			const calls: string[] = [];
			const client = {
				onNotification: (listener: typeof notify) => {
					notify = listener;
					return () => {
						notify = () => {};
					};
				},
				request: async (method: string, params: NavigationReadParams) => {
					calls.push(method);
					if (calls.length === 1 || repeatGap)
						notify({
							method: "evener/navigation/invalidated",
							params: {
								generationId: "g",
								sequence: calls.length * 3,
								targets: [{ kind: "pin_catalog", revision: 2 }],
							},
						});
					return wireV2(
						params,
						{
							pin_sections: [{ id: "focus", name: "Renamed", count: 1 }],
							remaining: 0,
						},
						"catalog",
						2,
						"g",
					);
				},
			} as unknown as ConversationClientLike;
			const pages = new NavigationPages<{
				id: string;
				name: string;
				count: number;
			}>(client, { resource: "pin_catalog" }, "pin_sections", (row) => row.id);
			const stop = pages.watch();
			try {
				const result = refreshPinNavigation(client, pages, {
					sectionId: "focus",
					current: () => true,
					confirmReceipt: true,
					checkpoint: {
						...checkpoint,
						operation: {
							kind: "renamePinSection",
							params: { sectionId: "focus", name: "Renamed" },
						},
					},
				});
				if (repeatGap) {
					await expect(result).rejects.toThrow();
					expect(pages.getSnapshot().stale).toBe(true);
				} else {
					await expect(result).resolves.toMatchObject({
						section: { id: "focus", name: "Renamed" },
					});
					expect(pages.getSnapshot()).toMatchObject({
						stale: false,
						loading: false,
						error: null,
					});
				}
				expect(calls).toEqual([
					"evener/navigation/read",
					"evener/navigation/read",
				]);
			} finally {
				stop();
			}
		},
	);
	it("looks up a section across pages without a session location", async () => {
		const { client, pages, calls } = boundary();
		const result = await refreshPinNavigation(client, pages, {
			sectionId: "focus",
			current: () => true,
		});
		expect(result.location).toBeNull();
		expect(result.section).toEqual({ id: "focus", name: "Focus", count: 1 });
		expect(calls.map((request) => [request.resource, request.offset])).toEqual([
			["pin_catalog", 0],
			["pin_catalog", 1],
		]);
	});
	it("recognizes an absent requested section only after catalog exhaustion", async () => {
		const { client, pages, calls } = boundary();
		const result = await refreshPinNavigation(client, pages, {
			sectionId: "missing",
			current: () => true,
		});
		expect(result.section).toBeNull();
		expect(calls.map((request) => request.offset)).toEqual([0, 1]);
	});
	it("stops after finding the requested section on the first page", async () => {
		const { client, pages, calls } = boundary();
		const result = await refreshPinNavigation(client, pages, {
			sectionId: "later",
			current: () => true,
		});
		expect(result.section?.id).toBe("later");
		expect(calls.map((request) => request.offset)).toEqual([0]);
	});
	it("rejects a section lookup abandoned during its second page", async () => {
		const { client, pages, calls } = boundary();
		await expect(
			refreshPinNavigation(client, pages, {
				sectionId: "focus",
				current: () => calls.length < 2,
			}),
		).rejects.toThrow();
		expect(calls.map((request) => request.offset)).toEqual([0, 1]);
	});
	it("reads the pending target and current session and finds assigned sections across catalog pages", async () => {
		const { client, pages, calls } = boundary();
		const result = await refreshPinNavigation(client, pages, {
			checkpoint,
			sessionRef: "local:other",
			current: () => true,
			confirmReceipt: true,
		});
		expect(
			calls.filter((p) => p.resource === "location").map((p) => p.ref),
		).toEqual(["local:s", "local:other"]);
		expect(
			calls.filter((p) => p.resource === "pin_catalog").map((p) => p.offset),
		).toEqual([0, 1]);
		expect(result.location?.ref).toBe("local:other");
		expect(result.section?.name).toBe("Focus");
	});
	it("rejects readback below the catalog receipt while ignoring unrelated resource revisions", async () => {
		const { client, pages } = boundary({ catalogRevision: 1 });
		await expect(
			refreshPinNavigation(client, pages, {
				checkpoint,
				current: () => true,
				confirmReceipt: true,
			}),
		).rejects.toThrow();
	});
	it("does not combine location and catalog from different generations", async () => {
		const { client, pages } = boundary({
			locationGeneration: "old",
			catalogGeneration: "new",
		});
		await expect(
			refreshPinNavigation(client, pages, {
				sessionRef: "local:s",
				current: () => true,
			}),
		).rejects.toThrow();
	});
	it("requires the acknowledged generation for immediate write confirmation but permits fresh restart reconciliation", async () => {
		const { client, pages } = boundary({
			locationGeneration: "new",
			catalogGeneration: "new",
			catalogRevision: 1,
		});
		await expect(
			refreshPinNavigation(client, pages, {
				checkpoint,
				current: () => true,
				confirmReceipt: true,
			}),
		).rejects.toThrow();
		await expect(
			refreshPinNavigation(client, pages, { checkpoint, current: () => true }),
		).resolves.toMatchObject({ generationId: "new" });
	});
	it("recognizes a disappeared target without inventing a pin assignment", async () => {
		const { client } = boundary({ missing: true });
		const result = await readPinLocation(client, "local:s");
		expect(result.location).toBeNull();
		expect(result.generationId).toBe("g");
	});
	it("does not clear another operation kind or accept an abandoned screen's read", async () => {
		const { client, pages, calls } = boundary();
		await expect(
			refreshPinNavigation(client, pages, {
				checkpoint: {
					...checkpoint,
					operation: {
						kind: "favorite",
						params: { kind: "project", id: "p", favorited: true },
					},
				},
				current: () => true,
			}),
		).rejects.toThrow();
		await expect(
			refreshPinNavigation(client, pages, {
				sessionRef: "local:s",
				current: () => false,
			}),
		).rejects.toThrow();
		expect(calls).toEqual([]);
	});
});
