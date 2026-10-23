// One app-owned conversation binding per (hub, ref), borrowed by every mounted
// view: the real native store, conversation service, activity store and durable
// mutation host, with the runtime and SQLite platform double underneath. Only
// the wire client and the platform edges are test doubles - no fake store,
// service or submitter stands in for the code under test.
import "../conversationPlatform.testkit";
import type { AppwireClientLike, Thread, ThreadReadResponse, TranscriptDisplayConfigV1 } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { StrictMode, useLayoutEffect } from "react";
import { act } from "react-test-renderer";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { NativeMutationRuntime, nativeMutationTargetKey } from "../nativeMutationRuntime";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "../sqliteSync.testkit";
import { render, settle, unmountMountedTrees } from "../renderNative.testkit";
import { DetailLevels } from "./detailLevels";
import {
	type NativeConversationAttachment,
	NativeConversationSessions,
	type NativeConversationService,
} from "./nativeConversationSessions";
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

/** A thread with `count` recorded user rows in one completed turn, enough to
 * cross the store's 500-row live-end cap. */
function threadWithRows(ref: string, count: number): Thread {
	const served = thread(ref);
	(served as unknown as { evener: { capabilities: Record<string, unknown> } }).evener.capabilities = {
		...CAPABILITIES,
		pageBefore: true,
	};
	(served as unknown as { turns: unknown[] }).turns = [
		{
			id: "turn_1",
			status: "completed",
			itemsView: "default",
			items: Array.from({ length: count }, (_, index) => ({
				id: `u-${index}`,
				turnId: "turn_1",
				type: "userMessage",
				status: "completed",
				text: `message ${index}`,
				transcriptKey: `turn_1:${index}:0`,
				position: { entry: index, item: 0 },
			})),
		},
	];
	return served;
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

	it("trims only when every attached reader follows the live end", async () => {
		const client = hub("ref-1", () => Promise.resolve({ thread: threadWithRows("ref-1", 510) }));
		connected(client);

		const a = sessions.attach("hub-1", "ref-1");
		const b = sessions.attach("hub-1", "ref-1");
		await vi.waitFor(() => expect(a.entry.store.getState().status).toBe("open"));

		// A new reader starts conservatively as not following, so nothing trims
		// and the older rows a reader may be looking at stay.
		expect(a.entry.store.getState().conversation?.items.length).toBeGreaterThan(500);

		// One follower is not enough: the other reader is still above the end.
		a.setFollowingLiveEnd(true);
		await settle();
		expect(a.entry.store.getState().conversation?.items.length).toBeGreaterThan(500);

		b.setFollowingLiveEnd(true);
		await settle();
		expect(a.entry.store.getState().conversation?.items.length).toBe(500);
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
		expect(a.live).toBe(false);
		expect(b.live).toBe(true);

		b.release();
		b.release();
		expect(sessions.size).toBe(0);
		expect(flushes).toBe(1);
		expect(b.live).toBe(false);
	});
});

// The entry hands out the factory's complete service, so a migrated screen can
// keep its recovery/catalog/clear/fork controls without a per-screen adapter.
function requiresFullService(service: NativeConversationService | null): void {
	if (service === null) return;
	const actions: Array<keyof NativeConversationService> = [
		"adoptClear",
		"setGoal",
		"forkFromTurn",
		"forkAside",
		"models",
		"forceStop",
		"resume",
		"readProjection",
	];
	for (const action of actions) void service[action];
}

describe("service surface", () => {
	it("is the factory's full service, not only the base conversation verbs", async () => {
		const client = hub("ref-1");
		connected(client);
		const a = sessions.attach("hub-1", "ref-1");
		await vi.waitFor(() => expect(a.entry.store.getState().status).toBe("open"));

		const service = a.entry.service!;
		requiresFullService(service);
		for (const action of [
			"adoptClear",
			"setGoal",
			"forkFromTurn",
			"forkAside",
			"models",
			"forceStop",
			"resume",
		] as const) {
			expect(typeof service[action]).toBe("function");
		}
	});
});

describe("durable pending seam", () => {
	it("binds a real mutation, keeps its identity across entries, and disposes both subscriptions", async () => {
		// Real DetailLevels over an in-memory port, with counted subscribe/release.
		const values = new Map<string, string>();
		const levels = new DetailLevels(
			{
				getItemSync: (key) => values.get(key) ?? null,
				setItemSync: (key, value) => void values.set(key, value),
				removeItemSync: (key) => void values.delete(key),
			},
			"hub-1",
		);
		let settingsSubscriptions = 0;
		const subscribeLevels = levels.subscribe;
		levels.subscribe = (listener) => {
			settingsSubscriptions += 1;
			const release = subscribeLevels(listener);
			return () => {
				settingsSubscriptions -= 1;
				release();
			};
		};
		let storageSubscriptions = 0;
		const subscribeStorage = runtime.subscribeStorage.bind(runtime);
		runtime.subscribeStorage = (listener) => {
			storageSubscriptions += 1;
			const release = subscribeStorage(listener);
			return () => {
				storageSubscriptions -= 1;
				release();
			};
		};
		sessions = new NativeConversationSessions({
			runtime: () => runtime,
			flushOutbox: () => {
				flushes += 1;
			},
			levels: () => levels,
		});

		const client = hub("ref-1");
		const heldSend = deferred<unknown>();
		client.on("turn/start", () => heldSend.promise as never);
		connected(client);

		const a = sessions.attach("hub-1", "ref-1");
		const b = sessions.attach("hub-1", "ref-1");
		await vi.waitFor(() => expect(a.entry.store.getState().status).toBe("open"));
		await vi.waitFor(() => expect(a.entry.store.getState().pendingMutationsReady).toBe(true));
		// One entry owns one settings subscription and one pending seam, however
		// many views borrow it.
		expect(settingsSubscriptions).toBe(1);
		expect(storageSubscriptions).toBe(1);

		await runtime.submit({
			kind: "send",
			hubId: "hub-1",
			targetRef: "ref-1",
			threadId: "thread-ref-1",
			instanceId: "instance-ref-1",
			input: [{ type: "text", text: "hello" }],
		});
		await vi.waitFor(() => {
			const pending = b.entry.store.getState().pendingMutations;
			expect(pending).toHaveLength(1);
			expect(pending![0]).toMatchObject({ method: "send", text: "hello", fromThisClient: true });
		});
		expect(b.entry.store.getState().pendingMutationsReady).toBe(true);
		const identity = b.entry.store.getState().pendingMutations![0]!.id;
		const targetKey = nativeMutationTargetKey("hub-1", "ref-1");
		expect((await runtime.storage.listOutbox(targetKey)).map((record) => record.clientMutationId)).toEqual([identity]);

		a.release();
		b.release();
		expect(a.entry.retired).toBe(true);
		// Final release removes the entry-owned settings and pending
		// subscriptions; the durable record itself survives.
		expect(settingsSubscriptions).toBe(0);
		expect(storageSubscriptions).toBe(0);
		expect((await runtime.storage.listOutbox(targetKey)).map((record) => record.clientMutationId)).toEqual([identity]);

		const reopened = sessions.attach("hub-1", "ref-1");
		await vi.waitFor(() => expect(reopened.entry.store.getState().status).toBe("open"));
		await vi.waitFor(() => {
			const pending = reopened.entry.store.getState().pendingMutations;
			expect(pending).toHaveLength(1);
			expect(pending![0]!.id).toBe(identity);
		});
		expect(reopened.entry.store.getState().pendingMutationsReady).toBe(true);
		expect(settingsSubscriptions).toBe(1);
		expect(storageSubscriptions).toBe(1);
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

describe("client binding lifetime", () => {
	it("a replaced client's held read cannot publish or reconcile the new binding", async () => {
		const held = deferred<ThreadReadResponse>();
		let oldReads = 0;
		const oldClient = hub("ref-1", () => {
			oldReads += 1;
			if (oldReads === 1) return held.promise;
			return Promise.resolve({ thread: thread("ref-1") });
		});
		const newClient = hub("ref-1");
		sessions.setConnection("hub-1", oldClient);

		const a = sessions.attach("hub-1", "ref-1");
		await vi.waitFor(() => expect(reads(oldClient)).toBe(1));
		sessions.setConnection("hub-1", newClient);

		await vi.waitFor(() => expect(a.entry.store.getState().status).toBe("open"));
		expect(reads(newClient)).toBe(1);
		expect(runtime.targetClient("hub-1", "ref-1")).toBe(newClient);
		const generation = a.entry.store.getState().conversationGeneration;

		held.resolve({ thread: { ...thread("ref-1"), id: "thread-old" } as Thread });
		await a.entry.readSettled();

		expect(a.entry.store.getState().conversation?.threadId).toBe("thread-ref-1");
		expect(a.entry.store.getState().status).toBe("open");
		expect(a.entry.store.getState().conversationGeneration).toBe(generation);
		expect(runtime.targetClient("hub-1", "ref-1")).toBe(newClient);
	});

	it("disposal and a same-key reopen survive the old reply", async () => {
		const held = deferred<ThreadReadResponse>();
		let first = true;
		const client = hub("ref-1", () => {
			if (first) {
				first = false;
				return held.promise;
			}
			return Promise.resolve({ thread: thread("ref-1") });
		});
		connected(client);

		const a = sessions.attach("hub-1", "ref-1");
		await vi.waitFor(() => expect(reads(client)).toBe(1));
		sessions.dispose();
		expect(a.entry.retired).toBe(true);
		expect(sessions.size).toBe(0);

		const b = sessions.attach("hub-1", "ref-1");
		expect(b.entry).not.toBe(a.entry);
		expect(b.entry.store).not.toBe(a.entry.store);

		held.resolve({ thread: thread("ref-1") });
		await vi.waitFor(() => expect(b.entry.store.getState().status).toBe("open"));
		await a.entry.readSettled();

		expect(a.entry.store.getState().status).toBe("closed");
		expect(b.entry.store.getState().canonicalSession()).not.toBeNull();
		expect(b.entry.retired).toBe(false);
	});

	it("disconnect retains display rows but removes authority, and reconnect resumes once", async () => {
		const client = hub("ref-1");
		connected(client);
		const a = sessions.attach("hub-1", "ref-1");
		await vi.waitFor(() => expect(a.entry.store.getState().status).toBe("open"));
		const rows = a.entry.store.getState().conversation;
		expect(rows).not.toBeNull();

		sessions.setConnection("hub-1", null);

		expect(a.entry.store.getState().conversation).toBe(rows);
		expect(a.entry.store.getState().canonicalSession()).toBeNull();
		expect(a.entry.store.getState().status).toBe("opening");
		expect(a.entry.service).toBeNull();
		expect(a.entry.liveHost).toBeNull();
		expect(runtime.targetClient("hub-1", "ref-1")).toBeUndefined();
		expect(reads(client)).toBe(1);

		sessions.setConnection("hub-1", client);

		await vi.waitFor(() => expect(a.entry.store.getState().status).toBe("open"));
		expect(a.entry.store.getState().canonicalSession()).not.toBeNull();
		expect(a.entry.service).not.toBeNull();
		expect(reads(client)).toBe(2);
	});

	it("final release cancels a held pending refresh, and its late completion cannot reach a reopened entry", async () => {
		const client = hub("ref-1");
		connected(client);
		const a = sessions.attach("hub-1", "ref-1");
		await vi.waitFor(() => expect(a.entry.store.getState().status).toBe("open"));
		await vi.waitFor(() => expect(a.entry.store.getState().pendingMutationsReady).toBe(true));

		const heldPending = deferred<unknown>();
		const realRead = runtime.read.bind(runtime);
		let holdNext = false;
		runtime.read = async (targetRef) => {
			if (holdNext) {
				holdNext = false;
				await heldPending.promise;
			}
			return realRead(targetRef);
		};
		holdNext = true;
		const refresh = a.entry.store.getState().refreshPendingMutations();

		a.release();
		// Retirement settles the wait; the held durable read itself is still out.
		await refresh;
		expect(a.entry.store.getState().status).toBe("closed");
		expect(a.entry.store.getState().pendingMutations).toBeNull();

		const b = sessions.attach("hub-1", "ref-1");
		await vi.waitFor(() => expect(b.entry.store.getState().status).toBe("open"));
		const generation = b.entry.store.getState().conversationGeneration;

		heldPending.resolve(undefined);
		await settle();

		expect(a.entry.store.getState().status).toBe("closed");
		expect(a.entry.store.getState().pendingMutations).toBeNull();
		expect(b.entry.store.getState().conversationGeneration).toBe(generation);
		expect(b.entry.store.getState().status).toBe("open");
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

/** Records what a committed key-change layout effect actually receives: a
 * child's layout effect runs before the hook's parent effect attaches the new
 * key, so a stale handle would appear here as the old ref. */
function LayoutSeesAttachment({
	attachment,
	sessionRef,
	records,
}: {
	attachment: NativeConversationAttachment | null;
	sessionRef: string;
	records: string[];
}) {
	useLayoutEffect(() => {
		records.push(attachment === null ? "null" : attachment.entry.ref);
	}, [attachment, sessionRef, records]);
	return null;
}

function KeyProbe({ sessionRef, records }: { sessionRef: string; records: string[] }) {
	const attachment = useNativeConversationAttachment("hub-1", sessionRef);
	return <LayoutSeesAttachment attachment={attachment} sessionRef={sessionRef} records={records} />;
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

	it("never returns the previous key's attachment on a committed key change", async () => {
		const client = hub("ref-1");
		harness.connection = { activeProfile: { id: "hub-1" }, client, state: "ready" };
		harness.preferences = { hubId: "hub-1", config: null };
		const records: string[] = [];
		const tree = render(
			<NativeConversationSessionsProvider>
				<KeyProbe sessionRef="ref-1" records={records} />
			</NativeConversationSessionsProvider>,
		);
		await settle();
		await vi.waitFor(() => expect(records.at(-1)).toBe("ref-1"));

		records.length = 0;
		act(() => {
			tree.update(
				<NativeConversationSessionsProvider>
					<KeyProbe sessionRef="ref-2" records={records} />
				</NativeConversationSessionsProvider>,
			);
		});
		// The committed render for ref-2 ran the child's layout effect before the
		// hook's own key-change effect, so it saw no handle at all.
		expect(records[0]).toBe("null");
		await settle();
		expect(records).toContain("ref-2");
		expect(records).not.toContain("ref-1");
	});
});
