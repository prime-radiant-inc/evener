// The recovery-hook contract, proven against the REAL NativeMutationRuntime
// over real MutationOutboxSQLite storage. The node:sqlite testkit double is
// the DB layer only (house test style - nativeMutationRuntime.test.ts); every
// read, storage-change notification and discard this suite observes goes
// through the landed slice-3 runtime surface: read(targetRef) on the
// composite target key, subscribeStorage, and discardRecovery. Nothing here
// reimplements the runtime - a green run is proof about the real one.

import type { MutationRecoveryRecord } from "@evener/appwire-client/state/mutation";
import { act, create, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, expect, test, vi } from "vitest";
import { TABLES } from "./mutationOutboxStorage";
import {
	NativeMutationRuntime,
	nativeMutationTargetKey,
} from "./nativeMutationRuntime";
import { renderHook } from "./renderNative.testkit";
import type { SqliteSync } from "./sqliteSync";
import {
	openSqliteSyncDouble,
	type SqliteDoubleDatabase,
} from "./sqliteSync.testkit";
import {
	type NativeMutationRecoveryProjection,
	useNativeMutationRecovery,
} from "./useNativeMutationRecovery";

const expoSQLite = vi.hoisted(() => ({ openDatabaseSync: vi.fn() }));
vi.mock("expo-sqlite", () => expoSQLite);
vi.mock("expo-crypto", () => ({
	randomUUID: () => "test-uuid",
	getRandomValues: (array: Uint8Array) => array,
}));

const TARGET = nativeMutationTargetKey("hub-1", "ref-1");
const OTHER_TARGET = nativeMutationTargetKey("hub-2", "ref-2");

const databases: SqliteDoubleDatabase[] = [];

afterEach(() => {
	for (const database of databases.splice(0)) database.close();
});

function openRuntime(label: string, port?: SqliteSync): NativeMutationRuntime {
	const opened = port ? { database: undefined, port } : openSqliteSyncDouble();
	if (opened.database) databases.push(opened.database);
	let next = 0;
	return new NativeMutationRuntime(opened.port, {
		createMutationId: () => `${label}-${++next}`,
		now: () => 1700000000000,
	});
}

async function seedRecovery(
	runtime: NativeMutationRuntime,
	targetKey: string,
	reason: string,
): Promise<MutationRecoveryRecord> {
	const record = await runtime.storage.enqueueIntent({
		targetRef: targetKey,
		method: "turn/queue",
		payload: {
			ref: "ref-1",
			expectedInstanceId: "instance-1",
			input: [{ type: "text", text: "hello" }],
		},
		attachments: [],
		optimisticDisplay: {
			method: "turn/queue",
			input: [{ type: "text", text: "hello" }],
		},
	});
	const recovery = await runtime.storage.transferToRecovery(
		record.clientMutationId,
		"rejected",
		reason,
	);
	if (!recovery) throw new Error("seeding recovery failed");
	return recovery;
}

function recoveryRows(projection: NativeMutationRecoveryProjection) {
	return (
		projection.snapshot?.recovery.map((row) => ({
			id: row.clientMutationId,
			kind: row.recoveryKind,
			reason: row.recoveryReason ?? null,
		})) ?? null
	);
}

async function flush() {
	await act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 0));
	});
}

// A gate at the DB layer, the one layer this suite is allowed to double:
// holding recovery-table SELECTs makes the real runtime's read() reject
// without touching any runtime or storage behavior.
function gatedPort(port: SqliteSync) {
	const gate = { closed: false };
	const wrapped: SqliteSync = {
		execSync: (sql) => port.execSync(sql),
		runSync: (sql, ...params) => port.runSync(sql, ...params),
		getFirstSync: (sql, ...params) => port.getFirstSync(sql, ...params),
		getAllSync: (sql, ...params) => {
			if (gate.closed && sql.includes(`FROM ${TABLES.recovery}`))
				throw new Error("recovery table unavailable");
			return port.getAllSync(sql, ...params);
		},
	};
	return {
		port: wrapped,
		hold: () => {
			gate.closed = true;
		},
		release: () => {
			gate.closed = false;
		},
	};
}

test("enumerates the target's durable recovery rows through the real runtime", async () => {
	const runtime = openRuntime("a");
	await seedRecovery(runtime, TARGET, "daemon refused a-1");
	await seedRecovery(runtime, TARGET, "daemon refused a-2");
	await seedRecovery(runtime, OTHER_TARGET, "another target's row");
	const outbox = await runtime.storage.enqueueIntent({
		targetRef: TARGET,
		method: "turn/queue",
		payload: { ref: "ref-1" },
		attachments: [],
		optimisticDisplay: { method: "turn/queue" },
	});

	const hook = renderHook(() => useNativeMutationRecovery(runtime, TARGET));
	await vi.waitFor(() => expect(hook.result.current.loading).toBe(false));

	expect(recoveryRows(hook.result.current)).toEqual([
		{ id: "a-1", kind: "rejected", reason: "daemon refused a-1" },
		{ id: "a-2", kind: "rejected", reason: "daemon refused a-2" },
	]);
	expect(hook.result.current.error).toBeNull();
	expect(hook.result.current.targetKey).toBe(TARGET);
	expect(
		hook.result.current.snapshot?.outbox.map((row) => row.clientMutationId),
	).toEqual([outbox.clientMutationId]);
});

test("refreshes only on the real runtime's storage changes for the exact target key", async () => {
	const runtime = openRuntime("a");
	await seedRecovery(runtime, TARGET, "daemon refused a-1");

	const hook = renderHook(() => useNativeMutationRecovery(runtime, TARGET));
	await vi.waitFor(() =>
		expect(recoveryRows(hook.result.current)).toEqual([
			{ id: "a-1", kind: "rejected", reason: "daemon refused a-1" },
		]),
	);

	await seedRecovery(runtime, TARGET, "daemon refused a-2");
	// A zero-row discard still publishes the storage change (the runtime's
	// zero-included rule); the other target's change must not re-read.
	await runtime.discardRecovery("missing", OTHER_TARGET);
	await flush();
	expect(recoveryRows(hook.result.current)).toEqual([
		{ id: "a-1", kind: "rejected", reason: "daemon refused a-1" },
	]);

	await runtime.discardRecovery("missing", TARGET);
	await vi.waitFor(() =>
		expect(recoveryRows(hook.result.current)).toEqual([
			{ id: "a-1", kind: "rejected", reason: "daemon refused a-1" },
			{ id: "a-2", kind: "rejected", reason: "daemon refused a-2" },
		]),
	);
});

test("discards a row durably through the real runtime and follows its own projection refresh", async () => {
	const runtime = openRuntime("a");
	const row = await seedRecovery(runtime, TARGET, "daemon refused a-1");
	const otherRow = await seedRecovery(
		runtime,
		OTHER_TARGET,
		"other target row",
	);

	const hook = renderHook(() => useNativeMutationRecovery(runtime, TARGET));
	await vi.waitFor(() =>
		expect(recoveryRows(hook.result.current)).toEqual([
			{ id: "a-1", kind: "rejected", reason: "daemon refused a-1" },
		]),
	);

	expect(await hook.result.current.discard(row.clientMutationId)).toBe(true);
	// The refresh the discard publishes leaves an empty recovery list, not a
	// null snapshot: the target's projection is present and settled.
	await vi.waitFor(() => expect(recoveryRows(hook.result.current)).toEqual([]));
	expect(await runtime.storage.listRecovery(TARGET)).toEqual([]);
	// The discard is idempotent and stays scoped to this projection's key:
	// another target's row is not reachable through it.
	expect(await hook.result.current.discard(row.clientMutationId)).toBe(false);
	expect(await hook.result.current.discard(otherRow.clientMutationId)).toBe(
		false,
	);
	expect(await runtime.storage.listRecovery(OTHER_TARGET)).toHaveLength(1);
});

test("clears the old snapshot when the runtime is replaced and drops the old generation's storage subscription", async () => {
	const runtimeA = openRuntime("a");
	const runtimeB = openRuntime("b");
	await seedRecovery(runtimeA, TARGET, "daemon refused a-1");
	await seedRecovery(runtimeB, TARGET, "daemon refused b-1");
	let runtime: NativeMutationRuntime | null = runtimeA;
	const hook = renderHook(() => useNativeMutationRecovery(runtime, TARGET));

	await vi.waitFor(() =>
		expect(recoveryRows(hook.result.current)).toEqual([
			{ id: "a-1", kind: "rejected", reason: "daemon refused a-1" },
		]),
	);

	runtime = runtimeB;
	hook.rerender();
	// The old snapshot is gone the moment the replacement mounts, before
	// the new generation's read has resolved.
	expect(hook.result.current).toMatchObject({
		targetKey: TARGET,
		loading: true,
		snapshot: null,
	});
	await vi.waitFor(() =>
		expect(recoveryRows(hook.result.current)).toEqual([
			{ id: "b-1", kind: "rejected", reason: "daemon refused b-1" },
		]),
	);

	// A late storage change on the OLD runtime must not repopulate the new
	// generation. A live stale subscription would re-read runtime A - which
	// now holds a second row - and show A's rows here, stably.
	await seedRecovery(runtimeA, TARGET, "daemon refused a-2");
	await runtimeA.discardRecovery("missing", TARGET);
	await flush();
	expect(recoveryRows(hook.result.current)).toEqual([
		{ id: "b-1", kind: "rejected", reason: "daemon refused b-1" },
	]);
});

test("drops a replaced generation's in-flight read completion", async () => {
	const runtimeA = openRuntime("a");
	const runtimeB = openRuntime("b");
	await seedRecovery(runtimeA, TARGET, "daemon refused a-1");
	await seedRecovery(runtimeB, TARGET, "daemon refused b-1");
	let runtime: NativeMutationRuntime | null = runtimeA;
	const hook = renderHook(() => useNativeMutationRecovery(runtime, TARGET));
	// No flush: the old generation's read is still in flight when the swap
	// happens, so its completion lands late - after the replacement.

	runtime = null;
	hook.rerender();
	expect(hook.result.current).toMatchObject({
		targetKey: TARGET,
		loading: false,
		snapshot: null,
		error: null,
	});

	// Resolving the old read now: if the fence were broken the stale snapshot
	// would repopulate this projection, and with no runtime mounted nothing
	// would overwrite it.
	await flush();
	expect(hook.result.current).toMatchObject({
		targetKey: TARGET,
		loading: false,
		snapshot: null,
		error: null,
	});

	// The surface recovers once a runtime is mounted again.
	runtime = runtimeB;
	hook.rerender();
	await vi.waitFor(() =>
		expect(recoveryRows(hook.result.current)).toEqual([
			{ id: "b-1", kind: "rejected", reason: "daemon refused b-1" },
		]),
	);
});

test("exposes none of the replaced runtime's rows on the first render after the swap", async () => {
	const runtimeA = openRuntime("a");
	const runtimeB = openRuntime("b");
	await seedRecovery(runtimeA, TARGET, "daemon refused a-1");
	await seedRecovery(runtimeB, TARGET, "daemon refused b-1");
	let runtime: NativeMutationRuntime | null = runtimeA;
	// renderHook's rerender flushes the clearing effect before an assertion
	// can run, so this probe records EVERY render's projection: the render
	// between the swap and the effect is the one the fence must hold.
	const observed: NativeMutationRecoveryProjection[] = [];
	function Probe() {
		observed.push(useNativeMutationRecovery(runtime, TARGET));
		return null;
	}
	let renderer!: ReactTestRenderer;
	act(() => {
		renderer = create(<Probe />);
	});
	await vi.waitFor(() => expect(observed.at(-1)?.loading).toBe(false));
	expect(
		observed.at(-1)?.snapshot?.recovery.map((row) => row.clientMutationId),
	).toEqual(["a-1"]);

	const beforeSwap = observed.length;
	runtime = runtimeB;
	act(() => {
		renderer.update(<Probe />);
	});
	// The first render after the swap - before the effect has cleared any
	// state - already shows the new pairing's empty projection, with the
	// new runtime's discard.
	expect(observed[beforeSwap]).toMatchObject({
		targetKey: TARGET,
		loading: true,
		snapshot: null,
	});
	for (const projection of observed.slice(beforeSwap)) {
		const ids =
			projection.snapshot?.recovery.map((row) => row.clientMutationId) ?? [];
		expect(ids).not.toContain("a-1");
	}
	await vi.waitFor(() =>
		expect(
			observed.at(-1)?.snapshot?.recovery.map((row) => row.clientMutationId),
		).toEqual(["b-1"]),
	);
});

test("switches the composite target key without leaking the old target's rows", async () => {
	const runtime = openRuntime("a");
	await seedRecovery(runtime, TARGET, "daemon refused a-1");
	const KEY_B = nativeMutationTargetKey("hub-1", "ref-2");
	await seedRecovery(runtime, KEY_B, "daemon refused b-1");
	let targetKey = TARGET;
	const hook = renderHook(() => useNativeMutationRecovery(runtime, targetKey));

	await vi.waitFor(() =>
		expect(recoveryRows(hook.result.current)).toEqual([
			{ id: "a-1", kind: "rejected", reason: "daemon refused a-1" },
		]),
	);

	targetKey = KEY_B;
	hook.rerender();
	expect(hook.result.current).toMatchObject({
		targetKey: KEY_B,
		loading: true,
		snapshot: null,
	});
	await vi.waitFor(() =>
		expect(recoveryRows(hook.result.current)).toEqual([
			{ id: "a-2", kind: "rejected", reason: "daemon refused b-1" },
		]),
	);

	// A storage change for the OLD key never refreshes the new one, even
	// though the runtime the subscription lives on is the same.
	await seedRecovery(runtime, TARGET, "daemon refused a-2");
	await runtime.discardRecovery("missing", TARGET);
	await flush();
	expect(recoveryRows(hook.result.current)).toEqual([
		{ id: "a-2", kind: "rejected", reason: "daemon refused b-1" },
	]);

	// The new key's own storage change refreshes it.
	await seedRecovery(runtime, KEY_B, "daemon refused b-2");
	await runtime.discardRecovery("missing", KEY_B);
	await vi.waitFor(() =>
		expect(recoveryRows(hook.result.current)).toEqual([
			{ id: "a-2", kind: "rejected", reason: "daemon refused b-1" },
			{ id: "a-4", kind: "rejected", reason: "daemon refused b-2" },
		]),
	);
});

test("reports a failed read and recovers on the next real storage change", async () => {
	const base = openSqliteSyncDouble();
	databases.push(base.database);
	const gate = gatedPort(base.port);
	const runtime = openRuntime("a", gate.port);
	await seedRecovery(runtime, TARGET, "daemon refused a-1");

	gate.hold();
	const hook = renderHook(() => useNativeMutationRecovery(runtime, TARGET));
	await vi.waitFor(() => expect(hook.result.current.loading).toBe(false));
	expect(hook.result.current.snapshot).toBeNull();
	expect(hook.result.current.error).toEqual(
		new Error("recovery table unavailable"),
	);

	gate.release();
	await runtime.discardRecovery("missing", TARGET);
	await vi.waitFor(() =>
		expect(recoveryRows(hook.result.current)).toEqual([
			{ id: "a-1", kind: "rejected", reason: "daemon refused a-1" },
		]),
	);
	expect(hook.result.current.error).toBeNull();
});

test("unsubscribes on unmount and the runtime stays usable for the next mount", async () => {
	const runtime = openRuntime("a");
	await seedRecovery(runtime, TARGET, "daemon refused a-1");
	// Wrap the real subscribeStorage purely to observe listener lifetime: the
	// registration and teardown still run through the runtime's own method.
	const subscribe = runtime.subscribeStorage.bind(runtime);
	let live = 0;
	let added = 0;
	let removed = 0;
	runtime.subscribeStorage = ((listener: Parameters<typeof subscribe>[0]) => {
		added += 1;
		live += 1;
		const stop = subscribe(listener);
		return () => {
			removed += 1;
			live -= 1;
			stop();
		};
	}) as typeof runtime.subscribeStorage;

	const hook = renderHook(() => useNativeMutationRecovery(runtime, TARGET));
	expect(added).toBe(1);
	hook.unmount();
	expect(live).toBe(0);
	expect(removed).toBe(1);
	await flush();

	const remounted = renderHook(() =>
		useNativeMutationRecovery(runtime, TARGET),
	);
	await vi.waitFor(() =>
		expect(recoveryRows(remounted.result.current)).toEqual([
			{ id: "a-1", kind: "rejected", reason: "daemon refused a-1" },
		]),
	);
});

test("exposes an empty projection with a no-op discard when no runtime is mounted", async () => {
	const runtime = openRuntime("a");
	await seedRecovery(runtime, TARGET, "daemon refused a-1");
	let mounted: NativeMutationRuntime | null = null;
	const hook = renderHook(() => useNativeMutationRecovery(mounted, TARGET));

	expect(hook.result.current).toMatchObject({
		targetKey: TARGET,
		loading: false,
		snapshot: null,
		error: null,
	});
	expect(await hook.result.current.discard("a-1")).toBe(false);

	mounted = runtime;
	hook.rerender();
	await vi.waitFor(() =>
		expect(recoveryRows(hook.result.current)).toEqual([
			{ id: "a-1", kind: "rejected", reason: "daemon refused a-1" },
		]),
	);
});
