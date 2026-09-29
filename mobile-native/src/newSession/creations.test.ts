import { expect, it, vi } from "vitest";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { creationService, creationStore, forgetCreationForHub } from "./creations";
import { creationRetired } from "./retiredCreations";

vi.mock("../nativeDrafts", () => ({
	nativeDrafts: () => ({ creation: { read: () => null, write: () => {}, clear: () => {} } }),
}));

const client = () => ({ request: async () => ({ data: [] }) }) as unknown as ConversationClientLike;

it("keeps one creation store per hub, whichever sheet asks", () => {
	expect(creationStore("hub-a")).toBe(creationStore("hub-a"));
	expect(creationStore("hub-a")).not.toBe(creationStore("hub-b"));
	forgetCreationForHub("hub-a");
	forgetCreationForHub("hub-b");
});

it("keeps one service per client, so a reopened sheet rebinds nothing", () => {
	const one = client();
	expect(creationService(one)).toBe(creationService(one));
	expect(creationService(one)).not.toBe(creationService(client()));
});

it("forgets a removed hub's store, unbound, and leaves the others", async () => {
	const removed = creationStore("hub-a");
	const kept = creationStore("hub-b");
	removed.getState().bind(creationService(client()));
	await removed.getState().setCwd("/project", false);
	removed.getState().setPrompt("go");
	const start = removed.getState().submit();
	forgetCreationForHub("hub-a");
	expect(await start).toEqual({ status: "obsolete" });
	expect(creationRetired(removed)).toBe(true);
	expect(creationRetired(kept)).toBe(false);
	expect(creationStore("hub-a")).not.toBe(removed);
	expect(creationStore("hub-b")).toBe(kept);
	forgetCreationForHub("hub-a");
	forgetCreationForHub("hub-b");
});
