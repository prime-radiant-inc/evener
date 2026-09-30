import { expect, test, vi } from "vitest";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import { createConversationService } from "../../../mobile/src/services/conversation";
import { SessionLink } from "./sessionMessage";
import { SubagentTree } from "../subagents/subagentTree";
vi.mock("expo-sqlite", () => ({ openDatabaseSync: vi.fn() }));
vi.mock("expo-crypto", () => ({ randomUUID: () => "test-uuid", getRandomValues: (array: Uint8Array) => array }));
const ref = "remote:root";
const context = {
	ref,
	sessionId: "root",
	rootRef: ref,
	ancestors: [],
	ancestryKnown: true,
	epoch: "one",
	availability: "live",
} as const;
function hub() {
	const client = new FakeClient("ready");
	client.on("thread/read", () => ({ thread: wireThread(ref, { id: "root" }) }));
	client.on("thread/unsubscribe", () => ({}));
	const count = { known: true, total: 0, active: 0, failed: 0, completed: 0 };
	client.on("evener/thread/activity/read", ({ scope }) => ({
		context: { ...context, ancestors: [] },
		scope: scope ?? "session",
		delegates: count,
		jobs: count,
		watches: count,
	}));
	client.on("evener/thread/delegates/list", ({ scope }) => ({
		context: { ...context, ancestors: [] },
		scope: scope ?? "session",
		delegates: [],
		page: { complete: true, issues: [] },
	}));
	client.on("evener/thread/jobs/list", ({ scope }) => ({
		context: { ...context, ancestors: [] },
		scope: scope ?? "session",
		jobs: [],
		page: { complete: true, issues: [] },
	}));
	return client;
}
const tick = () => new Promise((resolve) => setTimeout(resolve, 0));
test.each(["activity", "transcript"] as const)("last owner alone unsubscribes when %s leaves first", async (first) => {
	const client = hub();
	const service = createConversationService(client);
	await service.readProjection(ref);
	const tree = new SubagentTree(ref, "root");
	tree.observeActivity();
	await tree.setClient(client);
	await tree.follow();
	const followed = new SessionLink(client, ref);
	await followed.read({ follow: true });
	const reads = client.calls.filter((c) => c.method === "thread/read").map((c) => c.params);
	expect(reads[0]).toMatchObject({
		ref,
		subscribe: true,
		replaceSubscription: false,
		includeTurns: true,
		itemsView: "fragment",
		itemLimit: 40,
	});
	expect(reads.slice(1).every((p) => (p as { subscribe: boolean }).subscribe === false)).toBe(true);
	if (first === "activity") await tree.setClient(null);
	else service.close();
	await tick();
	expect(client.calls.filter((c) => c.method === "thread/unsubscribe")).toHaveLength(0);
	followed.dispose();
	await tick();
	expect(client.calls.filter((c) => c.method === "thread/unsubscribe")).toHaveLength(0);
	if (first === "activity") service.close();
	else await tree.setClient(null);
	await tick();
	expect(client.calls.filter((c) => c.method === "thread/unsubscribe").map((c) => c.params)).toEqual([{ ref }]);
});

test("a rich transcript read joining pending activity acquisition keeps its projection fields", async () => {
	const client = hub();
	let enter: () => void = () => {};
	const entered = new Promise<void>((resolve) => {
		enter = resolve;
	});
	let answer: (value: { thread: ReturnType<typeof wireThread> }) => void = () => {};
	let first = true;
	client.on("thread/read", () => {
		if (first) {
			first = false;
			enter();
			return new Promise((resolve) => {
				answer = resolve;
			});
		}
		return { thread: wireThread(ref, { id: "root" }) };
	});
	const tree = new SubagentTree(ref, "root");
	tree.observeActivity();
	const activity = tree.setClient(client);
	await entered;
	const service = createConversationService(client);
	const transcript = service.readProjection(ref);
	answer({ thread: wireThread(ref, { id: "root" }) });
	await Promise.all([activity, transcript]);
	const reads = client.calls.filter((call) => call.method === "thread/read").map((call) => call.params);
	expect(reads).toHaveLength(2);
	expect(reads[0]).toMatchObject({ includeTurns: false, subscribe: true, replaceSubscription: false });
	expect(reads[1]).toMatchObject({
		ref,
		includeTurns: true,
		itemsView: "fragment",
		itemLimit: 40,
		subscribe: false,
		replaceSubscription: false,
	});
	service.close();
	await tree.setClient(null);
	await tick();
	expect(client.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(1);
});
