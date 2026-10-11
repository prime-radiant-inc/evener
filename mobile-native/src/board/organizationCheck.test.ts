import { expect, it } from "vitest";
import type { NavigationPinSectionDescriptor } from "@evener/appwire-client";
import type { NavigationActionCheckpoint } from "../navigationActionRepository";
import { NavigationPages } from "../navigationPages";
import { organizationHub, SESSION_ID } from "./organizationTestUtils";
import { checkOrganizationChange, journalOperation, organizationFree } from "./organizationCheck";

function setup(options?: Parameters<typeof organizationHub>[0]) {
	const hub = organizationHub(options);
	const pinPages = new NavigationPages<NavigationPinSectionDescriptor>(
		hub.client,
		{ resource: "pin_catalog" },
		"pin_sections",
		(section) => section.id,
	);
	return { ...hub, pinPages };
}
const archive = (receipt: NavigationActionCheckpoint["receipt"]): NavigationActionCheckpoint => ({
	id: "archive",
	operation: { kind: "archive", params: { kind: "session", id: SESSION_ID, archived: true } },
	receipt,
});
const always = () => true;

it("checks an archive through the session's location, settled once the hub shows it", async () => {
	const { client, pinPages, reads } = setup();
	expect(
		await checkOrganizationChange(client, pinPages, archive({ generation_id: "g", targets: [] }), always, true),
	).toBe(true);
	expect(reads.map((read) => read.resource)).toEqual(["manifest", "location", "manifest"]);
	expect(reads[1]?.ref).toBe(`local:${SESSION_ID}`);
});

it("reports an archive the hub doesn't show as unsettled, without rejecting", async () => {
	const { client, pinPages } = setup({ tier: "recent" });
	expect(
		await checkOrganizationChange(client, pinPages, archive({ generation_id: "g", targets: [] }), always, true),
	).toBe(false);
});

it("reports an archive whose reply was lost as unsettled even when the hub shows it", async () => {
	const { client, pinPages } = setup();
	expect(await checkOrganizationChange(client, pinPages, archive(null), always, false)).toBe(false);
});

it("checks a pin change through the pin reads", async () => {
	const { client, pinPages, reads } = setup();
	const assign: NavigationActionCheckpoint = {
		id: "pin",
		operation: { kind: "assignPin", params: { sessionRef: "local:s", sectionId: "release" } },
		receipt: null,
	};
	expect(await checkOrganizationChange(client, pinPages, assign, always, false)).toBe(true);
	expect(reads.map((read) => read.resource)).toEqual(["location", "pin_catalog"]);
});

it("leaves a session deletion to the screen that made it, reading nothing", async () => {
	const { client, pinPages, reads } = setup();
	const deletion: NavigationActionCheckpoint = {
		id: "delete",
		operation: { kind: "deleteSession", params: { ref: `local:${SESSION_ID}` } },
		receipt: null,
	};
	await expect(checkOrganizationChange(client, pinPages, deletion, always, false)).rejects.toThrow(
		"checked on the screen that made it",
	);
	expect(reads).toEqual([]);
});

it("is free only with nothing pending, unresolved or unsaved", () => {
	const idle = { pending: false, uncertain: false, storageUnavailable: false };
	expect(organizationFree(idle)).toBe(true);
	expect(organizationFree(null)).toBe(false);
	expect(organizationFree({ ...idle, pending: true })).toBe(false);
	expect(organizationFree({ ...idle, uncertain: true })).toBe(false);
	expect(organizationFree({ ...idle, storageUnavailable: true })).toBe(false);
});

it("holds the journal's operation only while a change is pending or unresolved", () => {
	const held: NavigationActionCheckpoint = {
		id: "held",
		operation: { kind: "favorite", params: { kind: "favorite", id: "evener", favorited: true } },
		receipt: null,
	};
	expect(journalOperation(null)).toBeNull();
	expect(journalOperation({ pending: false, uncertain: false, recovery: null })).toBeNull();
	expect(journalOperation({ pending: false, uncertain: false, recovery: held })).toBeNull();
	expect(journalOperation({ pending: true, uncertain: false, recovery: held })).toEqual(held.operation);
	expect(journalOperation({ pending: false, uncertain: true, recovery: held })).toEqual(held.operation);
	expect(journalOperation({ pending: true, uncertain: false, recovery: null })).toBeNull();
});
