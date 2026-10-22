// One app-owned conversation binding per (hub, ref), borrowed by every mounted
// view: the real native store, conversation service, activity store and durable
// mutation host, with the runtime and SQLite platform double underneath. Only
// the wire client and the platform edges are test doubles - no fake store,
// service or submitter stands in for the code under test.
import "../conversationPlatform.testkit";
import type { AppwireClientLike, Thread, ThreadReadResponse, TranscriptDisplayConfigV1 } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { StrictMode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { NativeMutationRuntime } from "../nativeMutationRuntime";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "../sqliteSync.testkit";
import { render, settle, unmountMountedTrees } from "../renderNative.testkit";
import { type NativeConversationAttachment, NativeConversationSessions } from "./nativeConversationSessions";
import {
	NativeConversationSessionsProvider,
	useNativeConversationAttachment,
	useNativeConversationSessions,
} from "./NativeConversationSessionsProvider";

const CAPABILITIES = {
	send: true,
	steer: true,
	interrupt: true,
	queue: true,
	compact: false,
	clear: false,
	forkFromTurn: false,
	shutdown: false,
	changeModel: false,
	changeVisionModel: false,
	sharedNotes: false,
	goal: false,
	rename: false,
};

function thread(ref: string): Thread {
	return {
		id: `thread-${ref}`,
		sessionId: `session-${ref}`,
		preview: "",
		ephemeral: false,
		modelProvider: "anthropic",
		createdAt: 0,
		updatedAt: 0,
		status: { type: "idle" },
		cwd: "",
		cliVersion: "",
		source: "",
		turns: [],
		evener: {
			ref,
			instanceId: `instance-${ref}`,
			capabilities: CAPABILITIES,
			queue: { revision: 0, depth: 0, preview: [] },
		},
	} as unknown as Thread;
}

interface Deferred<T> {
	readonly promise: Promise<T>;
	resolve(value: T): void;
	reject(error: unknown): void;
}

function deferred<T>(): Deferred<T> {
	let resolve!: (value: T) => void;
	let reject!: (error: unknown) => void;
	const promise = new Promise<T>((settleOk, settleError) => {
		resolve = settleOk;
		reject = settleError;
	});
	return { promise, resolve, reject };
}

/** A ready fake client that answers every thread read with `served`; a test
 * can hold that answer to observe ownership during a read. */
function hub(ref: string, read?: () => Promise<ThreadReadResponse>): FakeClient {
	const client = new FakeClient("ready");
	client.on("thread/read", (params) => {
		if (read) return read();
		return { thread: thread(String((params as { ref: string }).ref)) };
	});
	return client;
}

function reads(client: FakeClient): number {
	return client.calls.filter((call) => call.method === "thread/read").length;
}

let database: SqliteDoubleDatabase | undefined;
let runtime: NativeMutationRuntime;
let sessions: NativeConversationSessions;
let flushes: number;

// The provider constructs its own registry with the real default runtime; this
// seam points that default at the suite's real runtime over the SQLite double,
// so the provider path and the direct path exercise the same durable owner.
const runtimeHolder = vi.hoisted(() => ({ current: null as unknown }));
vi.mock("../nativeMutationRuntime", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../nativeMutationRuntime")>();
	return { ...actual, getNativeMutationRuntime: () => runtimeHolder.current as NativeMutationRuntime };
});

function connected(client: AppwireClientLike) {
	sessions.setConnection("hub-1", client);
}

beforeEach(() => {
	const opened = openSqliteSyncDouble();
	database = opened.database;
	let id = 0;
	runtime = new NativeMutationRuntime(opened.port, { createMutationId: () => `mutation-${++id}` });
	runtimeHolder.current = runtime;
	flushes = 0;
	sessions = new NativeConversationSessions({
		runtime: () => runtime,
		flushOutbox: () => {
			flushes += 1;
		},
	});
});

afterEach(async () => {
	unmountMountedTrees();
	sessions.dispose();
	await runtime.stop();
	database?.close();
	database = undefined;
	harness.connection = {};
	harness.preferences = { hubId: null, config: null };
});

describe("two attachments", () => {
	it("share one store, service, sink and target registration", async () => {
		const client = hub("ref-1");
		connected(client);

		const a = sessions.attach("hub-1", "ref-1");
		const b = sessions.attach("hub-1", "ref-1");

		expect(a.entry).toBe(b.entry);
		expect(a.entry.store).toBe(b.entry.store);
		expect(a.entry.activitySink).toBe(b.entry.activitySink);
		expect(a.entry.service).toBe(b.entry.service);
		expect(a.entry.service).not.toBeNull();
		expect(a.entry.attachmentCount).toBe(2);

		await vi.waitFor(() => expect(a.entry.store.getState().status).toBe("open"));
		expect(a.entry.store.getState().canonicalSession()).not.toBeNull();
		expect(reads(client)).toBe(1);
		expect(runtime.targetClient("hub-1", "ref-1")).toBe(client);
	});

	it("release one during a held authoritative read, and the survivor sees its publication", async () => {
		const held = deferred<ThreadReadResponse>();
		const client = hub("ref-1", () => held.promise);
		connected(client);

		const a = sessions.attach("hub-1", "ref-1");
		const b = sessions.attach("hub-1", "ref-1");
		await vi.waitFor(() => expect(reads(client)).toBe(1));

		a.release();
		expect(sessions.size).toBe(1);
		expect(b.entry.retired).toBe(false);
		expect(b.entry.liveHost).not.toBeNull();
		expect(b.entry.attachmentCount).toBe(1);

		held.resolve({ thread: thread("ref-1") });
		await b.entry.readSettled();

		expect(b.entry.store.getState().status).toBe("open");
		expect(b.entry.store.getState().canonicalSession()?.threadId).toBe("thread-ref-1");
		expect(b.entry.retired).toBe(false);
		expect(runtime.targetClient("hub-1", "ref-1")).toBe(client);
	});
});

describe("final release", () => {
	it("unregisters, closes and cannot be resurrected by a late reply", async () => {
		const held = deferred<ThreadReadResponse>();
		const client = hub("ref-1", () => held.promise);
		connected(client);

		const a = sessions.attach("hub-1", "ref-1");
		await vi.waitFor(() => expect(reads(client)).toBe(1));
		expect(client.listenerCount).toBeGreaterThan(0);

		a.release();

		expect(a.entry.retired).toBe(true);
		expect(sessions.size).toBe(0);
		expect(runtime.targetClient("hub-1", "ref-1")).toBeUndefined();
		expect(a.entry.store.getState().status).toBe("closed");
		expect(a.entry.store.getState().conversation).toBeNull();
		expect(a.entry.liveHost).toBeNull();
		expect(a.entry.service).toBeNull();
		expect(flushes).toBe(1);

		held.resolve({ thread: thread("ref-1") });
		await a.entry.readSettled();

		// The retired generation won the fence: no late publication, no rows.
		expect(a.entry.store.getState().status).toBe("closed");
		expect(a.entry.store.getState().conversation).toBeNull();
		expect(a.entry.store.getState().canonicalSession()).toBeNull();
		expect(flushes).toBe(1);
		// The in-flight subscription acquisition settles and only then releases
		// its connection listener; the released lease is not reopening anything.
		await vi.waitFor(() => expect(client.listenerCount).toBe(0));
	});

	it("is idempotent per handle", async () => {
		const client = hub("ref-1");
		connected(client);
		const a = sessions.attach("hub-1", "ref-1");
		const b = sessions.attach("hub-1", "ref-1");
		await vi.waitFor(() => expect(a.entry.store.getState().status).toBe("open"));

		a.release();
		a.release();
		expect(sessions.size).toBe(1);
		expect(a.entry.retired).toBe(false);

		b.release();
		b.release();
		expect(sessions.size).toBe(0);
		expect(flushes).toBe(1);
	});
});

describe("reopen", () => {
	it("starts a fresh store and a fresh authoritative read", async () => {
		const client = hub("ref-1");
		connected(client);

		const first = sessions.attach("hub-1", "ref-1");
		await vi.waitFor(() => expect(first.entry.store.getState().status).toBe("open"));
		const firstStore = first.entry.store;
		first.release();

		const second = sessions.attach("hub-1", "ref-1");
		expect(second.entry).not.toBe(first.entry);
		expect(second.entry.store).not.toBe(firstStore);
		expect(firstStore.getState().status).toBe("closed");

		await vi.waitFor(() => expect(second.entry.store.getState().status).toBe("open"));
		expect(second.entry.store.getState().canonicalSession()).not.toBeNull();
		expect(reads(client)).toBe(2);
	});
});

describe("hub isolation", () => {
	it("keeps the same ref on different hubs in separate entries", async () => {
		const one = hub("ref-1");
		const two = hub("ref-1");
		sessions.setConnection("hub-1", one);

		const onOne = sessions.attach("hub-1", "ref-1");
		const onTwo = sessions.attach("hub-2", "ref-1");

		expect(onOne.entry).not.toBe(onTwo.entry);
		expect(onOne.entry.store).not.toBe(onTwo.entry.store);
		await vi.waitFor(() => expect(onOne.entry.store.getState().status).toBe("open"));
		expect(onTwo.entry.service).toBeNull();
		expect(onTwo.entry.store.getState().status).toBe("idle");
		expect(runtime.targetClient("hub-2", "ref-1")).toBeUndefined();

		sessions.setConnection("hub-2", two);

		await vi.waitFor(() => expect(onTwo.entry.store.getState().status).toBe("open"));
		expect(two.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
		expect(runtime.targetClient("hub-2", "ref-1")).toBe(two);
		expect(runtime.targetClient("hub-1", "ref-1")).toBeUndefined();
		// The retired hub keeps readable display rows on its own entry, but no
		// live authority and no canonical session.
		expect(onOne.entry.store.getState().conversation).not.toBeNull();
		expect(onOne.entry.store.getState().canonicalSession()).toBeNull();
		expect(onOne.entry.liveHost).toBeNull();
		expect(onOne.entry.service).toBeNull();
	});
});

// The provider: exactly one registry, connection and matching hub preferences
// fed into it, and a committed attachment hook. Strict Mode mounts effects
// twice; the hook must settle on one usable attachment, not a leaked refcount.
const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
	preferences: { hubId: null as string | null, config: null as TranscriptDisplayConfigV1 | null },
}));

vi.mock("../ConnectionProvider", () => ({
	useConnection: () => harness.connection,
}));
vi.mock("../NativePreferencesProvider", () => ({
	useNativePreferences: () => harness.preferences,
}));

function Probe({
	seen,
}: {
	seen: Array<{ attachment: NativeConversationAttachment | null; registry: NativeConversationSessions }>;
}) {
	const registry = useNativeConversationSessions();
	const attachment = useNativeConversationAttachment("hub-1", "ref-1");
	seen.push({ attachment, registry });
	return null;
}

describe("provider", () => {
	it("settles Strict Mode setup/cleanup/setup on one attachment", async () => {
		const client = hub("ref-1");
		harness.connection = { activeProfile: { id: "hub-1" }, client, state: "ready" };
		harness.preferences = { hubId: "hub-1", config: null };
		const seen: Array<{ attachment: NativeConversationAttachment | null; registry: NativeConversationSessions }> = [];

		render(
			<StrictMode>
				<NativeConversationSessionsProvider>
					<Probe seen={seen} />
				</NativeConversationSessionsProvider>
			</StrictMode>,
		);
		await settle();

		const registry = seen.at(-1)!.registry;
		expect(registry).not.toBe(sessions);
		expect(registry.size).toBe(1);
		const attachment = seen.at(-1)!.attachment;
		expect(attachment).not.toBeNull();
		expect(attachment!.entry.attachmentCount).toBe(1);
		await vi.waitFor(() => expect(attachment!.entry.store.getState().status).toBe("open"));
		expect(attachment!.entry.store.getState().canonicalSession()).not.toBeNull();
	});
});
