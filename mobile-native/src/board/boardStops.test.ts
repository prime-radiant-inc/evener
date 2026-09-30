// Stop from the Board over the real runtime and SQLite storage, with only the
// wire faked (the composition nativeMutationDispatch.test.ts uses).
import { afterEach, expect, it, vi } from "vitest";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { NativeMutationRuntime, nativeMutationTargetKey } from "../nativeMutationRuntime";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "../sqliteSync.testkit";
import { BoardStops, stopToast } from "./boardStops";

vi.mock("expo-sqlite", () => ({ openDatabaseSync: vi.fn() }));
vi.mock("expo-crypto", () => ({
	randomUUID: () => "test-uuid",
	getRandomValues: (array: Uint8Array) => array,
}));

const CAPS: ThreadCapabilities = {
	send: true,
	steer: true,
	interrupt: true,
	compact: true,
	clear: true,
	forkFromTurn: true,
	shutdown: true,
	changeModel: true,
	changeVisionModel: true,
	sharedNotes: true,
	queue: true,
	goal: true,
	rename: true,
};
function thread(status: string, evener: Partial<Thread["evener"]>): Thread {
	return {
		id: "thread-1",
		sessionId: "session-1",
		preview: "",
		ephemeral: false,
		modelProvider: "anthropic",
		createdAt: 1,
		updatedAt: 1,
		status: { type: status },
		cwd: "/tmp",
		cliVersion: "1.0.0",
		source: "local",
		turns: [],
		evener: { ref: "ref-1", capabilities: CAPS, queue: { revision: 0, depth: 0, preview: [] }, ...evener },
	};
}
const applied = (params: unknown) =>
	({
		receipt: {
			clientMutationId: (params as { clientMutationId: string }).clientMutationId,
			disposition: "applied",
			threadId: "thread-1",
			projectionState: "pending",
		},
	}) as never;

let databases: SqliteDoubleDatabase[] = [];
afterEach(() => {
	for (const database of databases) database.close();
	databases = [];
});
function setup(status = "active", evener: Partial<Thread["evener"]> = { instanceId: "instance-1" }) {
	const opened = openSqliteSyncDouble();
	databases.push(opened.database);
	let next = 0;
	const runtime = new NativeMutationRuntime(opened.port, {
		createMutationId: () => `mutation-${++next}`,
		now: () => 1,
		getOwnClientId: () => "origin-a",
	});
	const client = new FakeClient("ready");
	client.on("thread/read", () => ({ thread: thread(status, evener) }) as ThreadReadResponse);
	const stops = new BoardStops(() => runtime, "hub-1");
	return { runtime, client, stops, targetKey: nativeMutationTargetKey("hub-1", "ref-1") };
}
const calls = (client: FakeClient, method: string) => client.calls.filter((call) => call.method === method);
/** No registration is left: a ready client can't open a read for the target. */
const unregistered = (runtime: NativeMutationRuntime, client: FakeClient) =>
	runtime.beginAuthoritativeRead("hub-1", "ref-1", client) === undefined;

it("sends the interrupt now, fenced by a fresh read, and lets go once it has landed", async () => {
	const { runtime, client, stops, targetKey } = setup();
	client.on("turn/interrupt", applied);
	expect(await stops.stop(client, "ref-1")).toBe("stopped");
	await vi.waitFor(() => expect(calls(client, "turn/interrupt")).toHaveLength(1));
	expect(calls(client, "thread/read")[0]?.params).toEqual({
		ref: "ref-1",
		includeTurns: true,
		itemsView: "fragment",
		itemLimit: 40,
	});
	expect(calls(client, "turn/interrupt")[0]?.params).toEqual({
		ref: "ref-1",
		expectedInstanceId: "instance-1",
		clientMutationId: "mutation-1",
	});
	await vi.waitFor(() => expect(stops.stopping("ref-1")).toBe(false));
	expect(unregistered(runtime, client)).toBe(true);
	expect((await runtime.read(targetKey)).outbox).toEqual([]);
	await runtime.stop();
});

it("fences the interrupt with the thread id when the read names no instance", async () => {
	const { runtime, client, stops } = setup("active", {});
	client.on("turn/interrupt", applied);
	await stops.stop(client, "ref-1");
	await vi.waitFor(() => expect(calls(client, "turn/interrupt")).toHaveLength(1));
	expect(calls(client, "turn/interrupt")[0]?.params).toMatchObject({ expectedInstanceId: "thread-1" });
	await runtime.stop();
});

it("cancels the session's never-sent messages before anything can send, as the Session's Stop does", async () => {
	const { runtime, client, stops, targetKey } = setup();
	client.on("turn/interrupt", applied);
	client.on("turn/start", applied);
	// A message admitted while no screen held this session: durable, never sent.
	await runtime.submit({
		kind: "send",
		hubId: "hub-1",
		targetRef: "ref-1",
		threadId: "thread-1",
		instanceId: "instance-1",
		input: [{ type: "text", text: "sent before the Stop" }],
	});
	expect((await runtime.read(targetKey)).outbox).toMatchObject([{ method: "turn/start", state: "submitting" }]);
	expect(await stops.stop(client, "ref-1")).toBe("stopped");
	await vi.waitFor(() => expect(calls(client, "turn/interrupt")).toHaveLength(1));
	expect(calls(client, "turn/start")).toEqual([]);
	expect((await runtime.read(targetKey)).outbox).toMatchObject([{ method: "turn/start", state: "canceled" }]);
	await runtime.stop();
});

it("sends nothing to a session whose turn already ended", async () => {
	const { runtime, client, stops, targetKey } = setup("idle");
	expect(await stops.stop(client, "ref-1")).toBe("notWorking");
	expect(calls(client, "turn/interrupt")).toEqual([]);
	expect(stops.stopping("ref-1")).toBe(false);
	expect(unregistered(runtime, client)).toBe(true);
	expect((await runtime.read(targetKey)).outbox).toEqual([]);
	await runtime.stop();
});

it("sends nothing to a session the hub says must be resumed first", async () => {
	const { runtime, client, stops } = setup("active", { instanceId: "instance-1", resumeRequired: true });
	expect(await stops.stop(client, "ref-1")).toBe("unavailable");
	expect(calls(client, "turn/interrupt")).toEqual([]);
	expect(unregistered(runtime, client)).toBe(true);
	await runtime.stop();
});

it("leaves nothing registered when the read fails", async () => {
	const { runtime, client, stops } = setup();
	client.on("thread/read", () => {
		throw new Error("gone");
	});
	expect(await stops.stop(client, "ref-1")).toBe("unavailable");
	expect(unregistered(runtime, client)).toBe(true);
	await runtime.stop();
});

it("never opens a client that isn't connected", async () => {
	const { runtime, stops } = setup();
	const reconnecting = new FakeClient("reconnecting");
	expect(await stops.stop(reconnecting, "ref-1")).toBe("unavailable");
	expect(reconnecting.calls).toEqual([]);
	await runtime.stop();
});

it("lets go when the connection drops before the interrupt lands", async () => {
	const { runtime, client, stops } = setup();
	client.on("turn/interrupt", () => new Promise(() => {}) as never);
	expect(await stops.stop(client, "ref-1")).toBe("stopped");
	await vi.waitFor(() => expect(calls(client, "turn/interrupt")).toHaveLength(1));
	expect(stops.stopping("ref-1")).toBe(true);
	client.emitStateChange("reconnecting");
	expect(stops.stopping("ref-1")).toBe(false);
	client.emitStateChange("ready");
	expect(unregistered(runtime, client)).toBe(true);
	await runtime.stop();
});

it("keeps a Stop whose read fence fails: the interrupt is already durable, and the fence failure is logged, not thrown", async () => {
	const { runtime, client, stops } = setup();
	client.on("turn/interrupt", applied);
	const fault = new Error("storage fault");
	const fence = vi.spyOn(runtime, "reconcileAuthoritativeRead").mockRejectedValue(fault);
	const logged = vi.spyOn(console, "error").mockImplementation(() => {});
	try {
		await expect(stops.stop(client, "ref-1")).resolves.toBe("stopped");
		expect(fence).toHaveBeenCalled();
		expect(logged).toHaveBeenCalledWith(expect.stringContaining("read fence failed"), fault);
	} finally {
		fence.mockRestore();
		logged.mockRestore();
	}
	await runtime.stop();
});

it("sends one interrupt for two Stops at once", async () => {
	const { runtime, client, stops } = setup();
	client.on("turn/interrupt", applied);
	expect(await Promise.all([stops.stop(client, "ref-1"), stops.stop(client, "ref-1")])).toEqual(["stopped", "stopped"]);
	await vi.waitFor(() => expect(stops.stopping("ref-1")).toBe(false));
	expect(calls(client, "turn/interrupt")).toHaveLength(1);
	await runtime.stop();
});

it("dispose lets go of every Stop still being delivered and refuses new ones", async () => {
	const { runtime, client, stops } = setup();
	client.on("turn/interrupt", () => new Promise(() => {}) as never);
	await stops.stop(client, "ref-1");
	stops.dispose();
	expect(stops.stopping("ref-1")).toBe(false);
	expect(unregistered(runtime, client)).toBe(true);
	expect(await stops.stop(client, "ref-1")).toBe("unavailable");
	await runtime.stop();
});

it("sends a held Stop's interrupt once while its guard passes (phase 6 ruling 18)", async () => {
	const { runtime, client, stops } = setup("active", { instanceId: "instance-1", activeTurnId: "turn-2" });
	client.on("turn/interrupt", applied);
	const seen: unknown[] = [];
	expect(
		await stops.stop(client, "ref-1", (thread) => {
			seen.push(thread.activeTurnId);
			return true;
		}),
	).toBe("stopped");
	await vi.waitFor(() => expect(calls(client, "turn/interrupt")).toHaveLength(1));
	expect(seen).toEqual(["turn-2"]);
	await runtime.stop();
});

it("drops a held Stop whose guard fails: nothing sent, and nothing left registered", async () => {
	const { runtime, client, stops, targetKey } = setup();
	expect(await stops.stop(client, "ref-1", () => false)).toBe("dropped");
	expect(calls(client, "turn/interrupt")).toEqual([]);
	expect(stops.stopping("ref-1")).toBe(false);
	expect(unregistered(runtime, client)).toBe(true);
	expect((await runtime.read(targetKey)).outbox).toEqual([]);
	await runtime.stop();
});

it("says in the toast what a Stop from the Board did", () => {
	expect(stopToast("dropped", "Build docs")).toBe("The turn you stopped ended before you were back online");
	expect(stopToast("stopped", "Build docs")).toBe("Stopped");
	expect(stopToast("notWorking", "Build docs")).toBe("Nothing to stop: its turn had already ended.");
	expect(stopToast("unavailable", "Build docs")).toBe("Couldn't stop “Build docs”. Open it to stop it there.");
});
