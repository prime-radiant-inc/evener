import { describe, expect, it, vi } from "vitest";
import type { PendingMutation, ThreadCapabilities, ThreadReadResponse, Turn } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import { NativeMutationRuntime, nativeMutationTargetKey } from "../nativeMutationRuntime";
import { openSqliteSyncDouble } from "../sqliteSync.testkit";
import {
	readSendAction,
	SessionLink,
	type SessionState,
	type SessionTarget,
	stopRequestKind,
	submitSessionMessage,
} from "./sessionMessage";

vi.mock("expo-sqlite", () => ({ openDatabaseSync: vi.fn() }));
vi.mock("expo-crypto", () => ({ randomUUID: () => "test-uuid", getRandomValues: (array: Uint8Array) => array }));

const capabilities = (over: Partial<ThreadCapabilities> = {}): ThreadCapabilities => ({
	send: true,
	steer: true,
	interrupt: true,
	compact: false,
	clear: false,
	forkFromTurn: false,
	shutdown: false,
	changeModel: false,
	changeVisionModel: false,
	queue: true,
	goal: false,
	sharedNotes: false,
	rename: false,
	...over,
});
const session = (status: string, over: Partial<ThreadCapabilities> = {}): SessionState => ({
	threadId: "thread-1",
	instanceId: "instance-1",
	status,
	capabilities: capabilities(over),
	queueDepth: 0,
	model: "glm-5.3-vision",
});
const target: SessionTarget = { hubId: "hub-1", ref: "local:coord", threadId: "thread-1", instanceId: "instance-1" };

describe("the stop request's one Send (spec 9)", () => {
	it.each([
		["active", {}, "steer"],
		["active", { steer: false }, "queue"],
		["active", { steer: false, queue: false }, null],
		["idle", {}, "send"],
		["awaiting", {}, "send"],
		["ended", {}, "send"],
		["ended", { send: false }, null],
		["restartRequired", {}, null],
	] as const)("%s %o → %s", (status, over, expected) => {
		expect(stopRequestKind(session(status, over))).toBe(expected);
	});
});

describe("the composer's one Send, from a screen above the session (spec 8.5)", () => {
	const KEY = nativeMutationTargetKey("hub-1", "local:coord");
	const reply = (
		status: string,
		turns: Turn[] = [],
		over: Partial<ThreadCapabilities> = {},
		pendingMutations: PendingMutation[] = [],
	): ThreadReadResponse => ({
		thread: wireThread("local:coord", {
			id: "thread-1",
			status: { type: status },
			turns,
			evener: {
				ref: "local:coord",
				instanceId: "instance-1",
				capabilities: capabilities(over),
				queue: { revision: 1 },
				mutationStateAuthoritative: true,
				...(pendingMutations.length > 0 ? { pendingMutations } : {}),
			},
		}),
	});
	const shown = (clientMutationId: string): Turn => ({
		id: "turn-1",
		itemsView: "fragment",
		status: "completed",
		items: [{ type: "userMessage", id: "item-1", text: "first", clientMutationId }],
	});
	const firstSend = {
		kind: "send" as const,
		hubId: "hub-1",
		targetRef: "local:coord",
		threadId: "thread-1",
		instanceId: "instance-1",
		input: [{ type: "text" as const, text: "first" }],
	};
	const accepted = (params: unknown): never =>
		({
			receipt: {
				clientMutationId: (params as { clientMutationId: string }).clientMutationId,
				disposition: "applied",
				threadId: "thread-1",
				turnId: "turn-1",
				projectionState: "pending",
			},
			turn: { id: "turn-1" },
		}) as never;
	const runtimeFor = async (client: FakeClient) => {
		const runtime = new NativeMutationRuntime(openSqliteSyncDouble().port, { createMutationId: () => "mutation-1" });
		runtime.registerTarget("hub-1", "local:coord", client);
		await runtime.start();
		return runtime;
	};

	it.each([
		["idle", {}, "send"],
		["active", {}, "queue"],
		["ended", {}, "resume"],
		["restartRequired", {}, "none"],
		["active", { queue: false }, "none"],
	] as const)("a %s session with %o gets %s, read with the session's own window", async (status, over, expected) => {
		const client = new FakeClient("ready");
		client.on("thread/read", () => reply(status, [], over));
		const runtime = await runtimeFor(client);
		const { action, target } = await readSendAction(runtime, client, "hub-1", "local:coord");
		expect(action).toBe(expected);
		expect(target).toEqual({ hubId: "hub-1", ref: "local:coord", threadId: "thread-1", instanceId: "instance-1" });
		expect(client.calls.find((call) => call.method === "thread/read")?.params).toEqual({
			ref: "local:coord",
			includeTurns: true,
			itemsView: "fragment",
			itemLimit: 40,
		});
		await runtime.stop();
	});

	it("queues behind this client's own send that the outbox still holds", async () => {
		const client = new FakeClient("ready");
		client.on("thread/read", () => reply("idle"));
		const runtime = await runtimeFor(client);
		await runtime.submit(firstSend);
		expect((await runtime.read(KEY)).outbox).toHaveLength(1);
		expect((await readSendAction(runtime, client, "hub-1", "local:coord")).action).toBe("queue");
		await runtime.stop();
	});

	it("queues behind an accepted send the session doesn't show yet, and sends once it does", async () => {
		const client = new FakeClient("ready");
		let turns: Turn[] = [];
		client.on("thread/read", () => reply("idle", turns));
		client.on("turn/start", accepted);
		const runtime = await runtimeFor(client);
		const lease = runtime.beginAuthoritativeRead("hub-1", "local:coord", client);
		await runtime.reconcileAuthoritativeRead(lease!, reply("idle"));
		await runtime.submit(firstSend);
		await vi.waitFor(async () => expect((await runtime.read(KEY)).optimistic).toHaveLength(1));

		expect((await readSendAction(runtime, client, "hub-1", "local:coord")).action).toBe("queue");
		turns = [shown("mutation-1")];
		expect((await readSendAction(runtime, client, "hub-1", "local:coord")).action).toBe("send");
		await runtime.stop();
	});

	// A read that lists this phone's send as pending, before any turn shows
	// it, settles the send's durable record out of the outbox. The runtime
	// still knows the send was this phone's (Review Focus 4).
	it("queues behind an accepted send the daemon lists as pending after its durable record settled", async () => {
		const client = new FakeClient("ready");
		const pending: PendingMutation[] = [
			{ clientMutationId: "mutation-1", method: "turn/start", executionState: "accepted", projectionState: "pending" },
		];
		client.on("thread/read", () => reply("idle", [], {}, pending));
		client.on("turn/start", accepted);
		const runtime = await runtimeFor(client);
		const opening = runtime.beginAuthoritativeRead("hub-1", "local:coord", client);
		await runtime.reconcileAuthoritativeRead(opening!, reply("idle"));
		await runtime.submit(firstSend);
		await vi.waitFor(async () => expect((await runtime.read(KEY)).optimistic).toHaveLength(1));
		const listing = runtime.beginAuthoritativeRead("hub-1", "local:coord", client);
		await runtime.reconcileAuthoritativeRead(listing!, reply("idle", [], {}, pending));
		const held = await runtime.read(KEY);
		expect([...held.outbox, ...held.optimistic]).toEqual([]);

		expect((await readSendAction(runtime, client, "hub-1", "local:coord")).action).toBe("queue");
		await runtime.stop();
	});
});

describe("submitting a message to a session", () => {
	it("admits the message durably, then releases the session's target", async () => {
		const calls: unknown[] = [];
		const runtime = {
			submit: async (request: unknown) => {
				calls.push(["submit", request]);
				return undefined;
			},
			settleTarget: async (...args: unknown[]) => {
				calls.push(["settle", ...args]);
				return "reconciled" as const;
			},
		};
		const client = new FakeClient("ready");
		await submitSessionMessage(
			runtime,
			client,
			target,
			"steer",
			"Stop subagent “Fix race in tree settle”: it has failed.",
		);
		expect(calls).toEqual([
			[
				"submit",
				{
					kind: "steer",
					hubId: "hub-1",
					targetRef: "local:coord",
					threadId: "thread-1",
					instanceId: "instance-1",
					input: [{ type: "text", text: "Stop subagent “Fix race in tree settle”: it has failed." }],
				},
			],
			["settle", "hub-1", "local:coord", client],
		]);
	});

	it("keeps a message it admitted when the release can't run", async () => {
		const runtime = {
			submit: async () => undefined,
			settleTarget: async (): Promise<"blocked"> => {
				throw new Error("offline");
			},
		};
		await expect(submitSessionMessage(runtime, new FakeClient("ready"), target, "send", "hi")).resolves.toBeUndefined();
	});

	it("releases nothing when the admission itself fails", async () => {
		const settle = vi.fn();
		const runtime = {
			submit: async () => {
				throw new Error("The mutations database is unavailable");
			},
			settleTarget: settle,
		};
		await expect(submitSessionMessage(runtime, new FakeClient("ready"), target, "send", "hi")).rejects.toThrow(
			"The mutations database is unavailable",
		);
		expect(settle).not.toHaveBeenCalled();
	});
});

describe("following a session from a screen above it", () => {
	const read = (status: string, depth?: number): ThreadReadResponse =>
		({
			thread: {
				id: "thread-1",
				status: { type: status },
				modelProvider: "glm-5.3-vision",
				evener: {
					ref: "local:coord",
					instanceId: "instance-1",
					capabilities: capabilities(),
					queue: depth === undefined ? { revision: 1 } : { revision: 1, depth },
				},
			},
		}) as ThreadReadResponse;

	it("reads the session without moving the subscription unless asked to follow", async () => {
		const client = new FakeClient("ready");
		client.on("thread/read", () => read("active", 2));
		const link = new SessionLink(client, "local:coord");
		expect(await link.read({ follow: false })).toEqual({
			threadId: "thread-1",
			instanceId: "instance-1",
			status: "active",
			capabilities: capabilities(),
			queueDepth: 2,
			model: "glm-5.3-vision",
		});
		expect(client.calls[0]?.params).toEqual({ ref: "local:coord", includeTurns: false });
		await link.read({ follow: true });
		expect(client.calls[1]?.params).toEqual({
			ref: "local:coord",
			includeTurns: false,
			subscribe: true,
			replaceSubscription: false,
		});
	});

	it("tracks the followed session's status, capabilities and queue, and nothing else's, until disposed", async () => {
		const client = new FakeClient("ready");
		client.on("thread/read", () => read("active", 2));
		const link = new SessionLink(client, "local:coord");
		await link.read({ follow: true });
		client.emitNotification({
			method: "thread/status/changed",
			params: {
				threadId: "thread-1",
				ref: "local:coord",
				status: { type: "idle" },
				capabilities: capabilities({ steer: false }),
			},
		});
		client.emitNotification({
			method: "thread/status/changed",
			params: { threadId: "other", ref: "local:other", status: { type: "systemError" } },
		});
		client.emitNotification({
			method: "thread/queueChanged",
			params: { threadId: "thread-1", ref: "local:coord", queue: { revision: 2 } },
		});
		expect(link.getSnapshot()).toMatchObject({ status: "idle", queueDepth: 0, capabilities: { steer: false } });
		link.dispose();
		client.emitNotification({
			method: "thread/status/changed",
			params: { threadId: "thread-1", ref: "local:coord", status: { type: "active" } },
		});
		expect(link.getSnapshot()?.status).toBe("idle");
	});
});
