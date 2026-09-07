import { beforeEach, describe, expect, it, vi } from "vitest";

const storage = new Map<string, string>();
let uuid = 0;
vi.mock("expo-crypto", () => ({ randomUUID: () => `uuid-${++uuid}` }));
vi.mock("expo-sqlite/kv-store", () => ({
	Storage: {
		getItemSync: (key: string) => storage.get(key) ?? null,
		setItemSync: (key: string, value: string) => storage.set(key, value),
		removeItemSync: (key: string) => storage.delete(key),
		getAllKeysSync: () => [...storage.keys()],
	},
}));

import {
	organizationJournal,
	pinDrafts,
	removeOrganizationData,
} from "./nativeOrganization";

beforeEach(() => {
	storage.clear();
	uuid = 0;
});
const operation = {
	kind: "assignPin" as const,
	params: { sessionRef: "session", sectionId: "focus" },
};
describe("native organization persistence", () => {
	it("persists a draft and journal across recreation", () => {
		const draft = pinDrafts("hub", "session").save({
			kind: "new",
			name: "  New  ",
		});
		const journal = organizationJournal("hub");
		const checkpoint = journal.begin(operation);
		expect(pinDrafts("hub", "session").load()).toEqual(draft);
		expect(organizationJournal("hub").load()).toEqual(checkpoint);
	});
	it("fences stale conditional clear for both draft and journal", () => {
		const drafts = pinDrafts("hub", "session");
		const oldDraft = drafts.save({ kind: "new", name: "same" });
		const newDraft = drafts.save({ kind: "new", name: "same" });
		expect(drafts.removeIf(oldDraft)).toBe(false);
		expect(drafts.load()).toEqual(newDraft);
		const first = organizationJournal("hub").begin(operation);
		const journalKey = "evener.native.navigation-action.hub";
		storage.set(
			journalKey,
			JSON.stringify({ id: "new", operation, receipt: null }),
		);
		expect(organizationJournal("hub").finish(first)).toBe(false);
		expect(storage.has(journalKey)).toBe(true);
	});
	it("removes one hub's records while retaining other and unrelated storage", () => {
		pinDrafts("hub-a", "s-a").save({ kind: "existing", sectionId: "a" });
		pinDrafts("hub-b", "s-b").save({ kind: "existing", sectionId: "b" });
		organizationJournal("hub-a").begin(operation);
		organizationJournal("hub-b").begin({
			...operation,
			params: { ...operation.params, sessionRef: "s-b" },
		});
		storage.set('evener.native.pin-assignment.["hub-a","malformed', "keep");
		storage.set("unrelated", "keep");
		removeOrganizationData("hub-a");
		expect(pinDrafts("hub-a", "s-a").load()).toBeNull();
		expect(organizationJournal("hub-a").load()).toBeNull();
		expect(pinDrafts("hub-b", "s-b").load()).not.toBeNull();
		expect(organizationJournal("hub-b").load()).not.toBeNull();
		expect(
			storage.get('evener.native.pin-assignment.["hub-a","malformed'),
		).toBe("keep");
		expect(storage.get("unrelated")).toBe("keep");
	});
});
