import { expect, it } from "vitest";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import type { DraftStorage } from "../newSession";
import {
	bindCreation,
	creationService,
	creationStore as hubStore,
	forgetCreationForHub,
	releaseCreations,
} from "./creations";

const noDrafts: DraftStorage = () => ({ read: () => null, write: () => {}, clear: () => {} });
const creationStore = (hubId: string) => hubStore(hubId, noDrafts);

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
	expect(removed.getState().retired).toBe(true);
	expect(kept.getState().retired).toBe(false);
	expect(creationStore("hub-a")).not.toBe(removed);
	expect(creationStore("hub-b")).toBe(kept);
	forgetCreationForHub("hub-a");
	forgetCreationForHub("hub-b");
});

it("lets go of a client the connection has left, so a start on it doesn't wait forever (#3104)", async () => {
	const held = { request: () => new Promise(() => {}) } as unknown as ConversationClientLike;
	const store = creationStore("hub-a");
	bindCreation(store, held);
	await store.getState().setCwd("/project", false);
	store.getState().setPrompt("go");
	void store.getState().submit();
	await new Promise((resolve) => setTimeout(resolve, 0));
	expect(store.getState().submitting).toBe(true);
	// Still the live client: nothing changes.
	releaseCreations(held);
	expect(store.getState().submitting).toBe(true);
	// Disconnected (or another hub, or a new connection): the start is let go, uncertain.
	releaseCreations(null);
	expect(store.getState()).toMatchObject({ submitting: false, unconfirmedCreation: true });
	forgetCreationForHub("hub-a");
});
