import { describe, expect, it } from "vitest";
import type { NavigationPinSectionDescriptor } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import { wireV2 } from "../../cmd/evener-hub/frontend/src/stores/navigation/testing";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { nativeNavigationActions } from "./navigationActionRepository";
import { NavigationActions } from "./navigationActions";
import { NavigationPages } from "./navigationPages";
import { refreshPinNavigation } from "./pinNavigation";

function storage() {
	const values = new Map<string, unknown>();
	let id = 0;
	return {
		createId: () => `checkpoint-${++id}`,
		get: (key: string) => structuredClone(values.get(key)),
		set: (key: string, value: unknown) =>
			values.set(key, structuredClone(value)),
		deleteIf: (key: string, expected: unknown) => {
			if (JSON.stringify(values.get(key)) !== JSON.stringify(expected))
				return false;
			values.delete(key);
			return true;
		},
	};
}
function client(failLocations: number[] = []) {
	let locationReads = 0;
	let mutations = 0;
	const calls: string[] = [];
	const fake = {
		calls,
		get mutations() {
			return mutations;
		},
		request: async (method: string, params: Record<string, unknown>) => {
			calls.push(method);
			if (method === "evener/session-pin/assign") {
				mutations++;
				return {
					ok: true,
					navigation: {
						generation_id: "g",
						targets: [{ kind: "pin_catalog", revision: 2 }],
					},
				};
			}
			if (params.resource === "location") {
				locationReads++;
				if (failLocations.includes(locationReads))
					throw new Error("location unavailable");
				return wireV2(
					params as never,
					{
						top_level: true,
						...(mutations ? { pin_section_id: "focus" } : {}),
						session: {
							ref: "session",
							host_id: "local",
							session_id: "id",
							title: "Session",
							project: "p",
							state: "idle",
							kind: "session",
						},
					},
					'"location"',
					3,
					"g",
				);
			}
			return wireV2(
				params as never,
				{
					pin_sections: [{ id: "focus", name: "Focus", count: 1 }],
					remaining: 0,
				},
				'"catalog"',
				2,
				"g",
			);
		},
		onNotification: () => () => {},
	} as unknown as ConversationClientLike & {
		calls: string[];
		mutations: number;
	};
	return fake;
}
function pages(client: ConversationClientLike) {
	return new NavigationPages<NavigationPinSectionDescriptor>(
		client,
		{ resource: "pin_catalog" },
		"pin_sections",
		(row) => row.id,
	);
}
function controller(
	client: ConversationClientLike,
	journal: ReturnType<typeof nativeNavigationActions>,
	page: NavigationPages<{ id: string; name: string; count: number }>,
	current = () => true,
) {
	return new NavigationActions(
		client,
		(_receipt, checkpoint) =>
			refreshPinNavigation(client, page, {
				checkpoint,
				sessionRef: "session",
				current,
				confirmReceipt: true,
			}).then(() => undefined),
		current,
		(checkpoint) =>
			refreshPinNavigation(client, page, {
				checkpoint,
				sessionRef: "session",
				current,
			}).then(() => undefined),
		journal,
	);
}
describe("pin navigation recovery", () => {
	it("reconstructs after ACKed assign with failed readback and never replays", async () => {
		const fake = client([1]);
		const journal = nativeNavigationActions("hub", storage());
		const first = controller(fake, journal, pages(fake));
		await first.assignPin({ sessionRef: "session", sectionId: "focus" });
		expect(first.getSnapshot().uncertain).toBe(true);
		expect(journal.load()).toMatchObject({
			operation: {
				kind: "assignPin",
				params: { sessionRef: "session", sectionId: "focus" },
			},
			receipt: {
				generation_id: "g",
				targets: [{ kind: "pin_catalog", revision: 2 }],
			},
		});
		first.dispose();
		const afterWrite = fake.calls.length;
		const p2 = pages(fake);
		const second = controller(fake, journal, p2);
		await second.reconcile();
		expect(second.getSnapshot().uncertain).toBe(false);
		expect(journal.load()).toBeNull();
		expect(fake.mutations).toBe(1);
		expect(fake.calls.slice(afterWrite)).toEqual([
			"evener/navigation/read",
			"evener/navigation/read",
		]);
		expect(p2.getSnapshot().rows).toEqual([
			{ id: "focus", name: "Focus", count: 1 },
		]);
	});
	it("can recover a failed no-journal reconciliation without mutation", async () => {
		const fake = client([1]);
		const journal = nativeNavigationActions("hub", storage());
		const actions = controller(fake, journal, pages(fake));
		await actions.reconcile();
		expect(actions.getSnapshot().uncertain).toBe(true);
		await actions.reconcile();
		expect(actions.getSnapshot().uncertain).toBe(false);
		expect(fake.mutations).toBe(0);
	});
});
