// Persistence assertions inspect raw SQLite rows where the stored encoding is
// the contract; read assertions go through the storage port's public methods.
//
// The web IndexedDB adapter's conformance tests are the behavioral oracle;
// this suite exercises the same contracts against node:sqlite's real engine,
// since expo-sqlite is unavailable outside a device or simulator.
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { MutationIntent } from "@evener/appwire-client/state/mutation";
import { MutationOutboxSQLite, type Row } from "./mutationOutboxStorage";
import type { SqliteSync } from "./sqliteSync";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "./sqliteSync.testkit";

// The default id source is expo-crypto's synchronous randomUUID/getRandomValues,
// mocked so this suite proves the adapter never dereferences a bare Web Crypto
// global that React Native does not guarantee.
let expoCryptoCalls = 0;
vi.mock("expo-crypto", () => ({
	randomUUID: () => `expo-crypto-${++expoCryptoCalls}`,
	getRandomValues: (array: Uint8Array) => array,
}));

let directory: string;
let database: SqliteDoubleDatabase;
let port: SqliteSync;
let storage: MutationOutboxSQLite;

function openStorage() {
	let next = 0;
	storage = new MutationOutboxSQLite(port, {
		createMutationId: () => `mutation-${++next}`,
		now: () => 1234,
	});
}

function rawRow(table: string, clientMutationId: string): Row | undefined {
	return (database.prepare(`SELECT * FROM ${table} WHERE client_mutation_id = ?`).get(clientMutationId) as
		| Row
		| undefined) ?? undefined;
}

beforeEach(() => {
	directory = mkdtempSync(join(tmpdir(), "evener-mutation-outbox-"));
	const opened = openSqliteSyncDouble(join(directory, "outbox.sqlite"));
	database = opened.database;
	port = opened.port;
	openStorage();
});

afterEach(() => {
	database.close();
	rmSync(directory, { recursive: true, force: true });
});

const TARGET = "local:thread-1";

function intent(text: string, targetRef = TARGET): MutationIntent {
	return {
		targetRef,
		threadId: "thread-1",
		method: "turn/queue",
		payload: { ref: targetRef, input: [{ type: "text", text }] },
		attachments: [],
		optimisticDisplay: { text },
	};
}

function interruptIntent(targetRef = TARGET): MutationIntent {
	return {
		targetRef,
		method: "turn/interrupt",
		payload: { ref: targetRef },
		attachments: [],
		optimisticDisplay: { method: "turn/interrupt" },
	};
}

// Oracle: "reload restores the complete persisted intent" (mutationOutbox.test.ts:105).
test("enqueueIntent persists a submitting record with the full intent", async () => {
	const persisted = await storage.enqueueIntent(intent("survive reload"));
	expect(persisted).toEqual({
		version: 1,
		clientMutationId: "mutation-1",
		originClientId: undefined,
		targetRef: TARGET,
		threadId: "thread-1",
		intentSequence: 1,
		createdAt: 1234,
		method: "turn/queue",
		// The dispatcher sends this payload verbatim as the RPC params, and every
		// retry-safe method requires clientMutationId on it for the daemon's own
		// correlation. Oracle: "reload restores the complete persisted intent"
		// asserts the same payload.clientMutationId (mutationOutbox.test.ts:105).
		payload: { ref: TARGET, input: [{ type: "text", text: "survive reload" }], clientMutationId: "mutation-1" },
		attachments: [],
		optimisticDisplay: { text: "survive reload" },
		state: "submitting",
		attempted: false,
	});
	const row = rawRow("mutation_outbox", "mutation-1");
	expect(row).toMatchObject({ state: "submitting", attempted: 0, intent_sequence: 1 });
});

// RoboRev PR #1873 Medium, the fresh review: MutationIntent.instanceId - the
// fused fencing identity the web's identity fix (4059723ab4) added, where
// every durable row carries its enqueue-time instance and the cleanup fences
// instanceId ?? threadId - was dropped by the SQLite adapter: no column, no
// row conversion, no insert value. A reload (the row read back through
// fromRow) or a state transition lost the identity, so a native row fell
// back to its threadId even when the enqueue had captured the real instance.
test("instanceId round-trips through a reload and the recovery handoff", async () => {
	const record = await storage.enqueueIntent({ ...intent("fenced instance"), instanceId: "instance-at-click" });
	// The durable row carries the column the reload reads.
	expect(rawRow("mutation_outbox", record.clientMutationId)).toMatchObject({ instance_id: "instance-at-click" });

	// A fresh adapter over the same database - what an app restart is - reads
	// the identity back through the row conversion.
	openStorage();
	await expect(storage.getOutbox(record.clientMutationId)).resolves.toMatchObject({
		instanceId: "instance-at-click",
	});

	// The recovery handoff spreads the record it read, so the identity rides
	// the transition instead of falling back to the thread id.
	const recovery = await storage.transferToRecovery(record.clientMutationId, "rejected", "turn is not active");
	expect(recovery).toMatchObject({ instanceId: "instance-at-click" });
	expect(rawRow("mutation_recovery", record.clientMutationId)).toMatchObject({ instance_id: "instance-at-click" });
});

test("enqueueIntent persists an absent optimistic display as JSON null and reads it as undefined", async () => {
	const record = await storage.enqueueIntent({
		...intent("without an optimistic display"),
		optimisticDisplay: undefined,
	});

	expect(rawRow("mutation_outbox", record.clientMutationId)).toMatchObject({ optimistic_display: "null" });
	await expect(storage.getOutbox(record.clientMutationId)).resolves.toMatchObject({ optimisticDisplay: undefined });
	await expect(storage.settleReceipt(record.clientMutationId, "pending")).resolves.toBe(true);
	expect(rawRow("mutation_outbox", record.clientMutationId)).toBeUndefined();
	expect(rawRow("mutation_optimistic", record.clientMutationId)).toBeUndefined();
});

test("enqueueIntent persists attachments, composer text, and the submitting client identity", async () => {
	const identifiedStorage = new MutationOutboxSQLite(port, {
		createMutationId: () => "identified-mutation",
		now: () => 1234,
		getOwnClientId: () => "client-one",
	});
	const attachment = {
		presentationId: "presentation-1",
		marker: 1,
		name: "photo.png",
		mediaType: "image/png",
	};

	const record = await identifiedStorage.enqueueIntent({
		...intent("with attachment"),
		attachments: [attachment],
		composerText: "with attachment [image 1]",
	});

	expect(record).toMatchObject({
		attachments: [attachment],
		composerText: "with attachment [image 1]",
		originClientId: "client-one",
	});
	await expect(identifiedStorage.getOutbox(record.clientMutationId)).resolves.toMatchObject({
		attachments: [attachment],
		composerText: "with attachment [image 1]",
		originClientId: "client-one",
	});
});

test("enqueueIntent rejects an empty or whitespace targetRef before allocating a sequence", async () => {
	await expect(storage.enqueueIntent(intent("no target", "   "))).rejects.toThrow("targetRef is required");
	expect(database.prepare("SELECT * FROM mutation_sequence WHERE target_ref = ?").get("   ")).toBeUndefined();
	expect(database.prepare("SELECT * FROM mutation_outbox").all()).toEqual([]);
});

// Oracle: enqueueIntent uses IndexedDB's `add`, which throws on a duplicate
// key rather than silently keeping the old record. A collision here (a
// repeating id generator, the package's documented insecure fallback) must
// reject outright and roll back the sequence it just allocated - not
// advance the sequence while quietly leaving the first payload in place.
test("enqueueIntent rejects a duplicate clientMutationId and rolls back its sequence allocation", async () => {
	const collidingIdStorage = new MutationOutboxSQLite(port, {
		createMutationId: () => "mutation-collide",
		now: () => 1234,
	});
	const first = await collidingIdStorage.enqueueIntent(intent("first payload"));
	await expect(collidingIdStorage.enqueueIntent(intent("second payload", "local:b"))).rejects.toThrow();

	expect(database.prepare("SELECT * FROM mutation_sequence WHERE target_ref = ?").get("local:b")).toBeUndefined();
	expect(rawRow("mutation_outbox", first.clientMutationId)).toMatchObject({
		payload: JSON.stringify({
			ref: TARGET,
			input: [{ type: "text", text: "first payload" }],
			clientMutationId: "mutation-collide",
		}),
	});
});

// #1957: the uniqueness invariant is cross-store, not table-local. The outbox
// INSERT above rejects a collision within the outbox, but a record that has
// moved on to optimistic or recovery still owns its clientMutationId - a later
// enqueue that generates the same id must reject too, or the next settlement
// (settleReceipt/settleApplied keyed on the id) would treat the two active
// records as one mutation and retire the older one.
test("enqueueIntent rejects a clientMutationId already held by the optimistic store and rolls back its sequence allocation", async () => {
	const colliding = new MutationOutboxSQLite(port, {
		createMutationId: () => "mutation-shared",
		now: () => 1234,
	});
	const accepted = await colliding.enqueueIntent({
		...intent("accepted elsewhere"),
		optimisticDisplay: { input: [{ type: "text", text: "accepted elsewhere" }] },
	});
	await expect(colliding.settleReceipt(accepted.clientMutationId, "pending")).resolves.toBe(true);
	expect(rawRow("mutation_optimistic", "mutation-shared")).toBeDefined();

	await expect(colliding.enqueueIntent(intent("collides with the accepted record", "local:b"))).rejects.toThrow();

	// The older accepted record survives untouched, the colliding enqueue left
	// nothing behind, and the sequence it allocated rolled back with it.
	expect(rawRow("mutation_optimistic", "mutation-shared")).toBeDefined();
	expect(database.prepare("SELECT * FROM mutation_outbox WHERE target_ref = ?").all("local:b")).toHaveLength(0);
	expect(database.prepare("SELECT * FROM mutation_sequence WHERE target_ref = ?").get("local:b")).toBeUndefined();
});

test("enqueueIntent rejects a clientMutationId already held by the recovery store and rolls back its sequence allocation", async () => {
	const colliding = new MutationOutboxSQLite(port, {
		createMutationId: () => "mutation-shared",
		now: () => 1234,
	});
	const refused = await colliding.enqueueIntent(intent("refused elsewhere"));
	await colliding.transferToRecovery(refused.clientMutationId, "rejected", "turn is not active");
	expect(rawRow("mutation_recovery", "mutation-shared")).toBeDefined();

	await expect(colliding.enqueueIntent(intent("collides with the recovery record", "local:b"))).rejects.toThrow();

	expect(rawRow("mutation_recovery", "mutation-shared")).toMatchObject({ recovery_kind: "rejected" });
	expect(database.prepare("SELECT * FROM mutation_outbox WHERE target_ref = ?").all("local:b")).toHaveLength(0);
	expect(database.prepare("SELECT * FROM mutation_sequence WHERE target_ref = ?").get("local:b")).toBeUndefined();
});

test("enqueueInterruptAndCancel rejects a clientMutationId already active elsewhere and rolls back the whole Stop write", async () => {
	let nextId = "mutation-waiting";
	const store = new MutationOutboxSQLite(port, { createMutationId: () => nextId, now: () => 1234 });
	const waiting = await store.enqueueIntent(intent("still waiting"));
	nextId = "mutation-shared";
	const refused = await store.enqueueIntent(intent("refused elsewhere", "local:elsewhere"));
	await store.transferToRecovery(refused.clientMutationId, "rejected");

	// The Stop's interrupt would collide with the recovery record's id, so the
	// whole transaction - the cancel scan, the stop-epoch bump and the sequence
	// allocation - must roll back rather than half-apply.
	await expect(store.enqueueInterruptAndCancel(interruptIntent(TARGET))).rejects.toThrow();

	expect(rawRow("mutation_outbox", waiting.clientMutationId)).toMatchObject({ state: "submitting" });
	expect(database.prepare("SELECT * FROM mutation_outbox WHERE method = 'turn/interrupt'").all()).toHaveLength(0);
	expect(database.prepare("SELECT stop_epoch, last_sequence FROM mutation_sequence WHERE target_ref = ?").get(TARGET)).toMatchObject({
		stop_epoch: 0,
		last_sequence: 1,
	});
});

test("enqueueIntent defaults the mutation id through expo-crypto's SecureRandomSource, never a bare Web Crypto global", async () => {
	const originalCrypto = globalThis.crypto;
	// Simulate a host with no Web Crypto global at all - the case React Native
	// does not guarantee - to falsify a default that dereferences it directly.
	// @ts-expect-error - deliberately removing the global for this assertion.
	delete globalThis.crypto;
	try {
		const defaultIdStorage = new MutationOutboxSQLite(port, { now: () => 1234 });
		const persisted = await defaultIdStorage.enqueueIntent(intent("no bare crypto global", "local:no-crypto"));
		expect(persisted.clientMutationId).toMatch(/^expo-crypto-/);
	} finally {
		globalThis.crypto = originalCrypto;
	}
});

// Oracle: "concurrent tabs allocate one gap-free per-target sequence" (mutationOutbox.test.ts:176).
test("intentSequence is gap-free and per target ref", async () => {
	const first = await storage.enqueueIntent(intent("first", "local:a"));
	const second = await storage.enqueueIntent(intent("second", "local:a"));
	const other = await storage.enqueueIntent(intent("other", "local:b"));
	expect([first.intentSequence, second.intentSequence]).toEqual([1, 2]);
	expect(other.intentSequence).toBe(1);
});

// This deterministic reentrant call exercises the statement interleaving that
// could otherwise make two enqueue operations compute the same sequence.
test("enqueueIntent's sequence allocation never collides when enqueue operations interleave", async () => {
	let otherNext = 0;
	const storageB = new MutationOutboxSQLite(
		port,
		{ createMutationId: () => `other-${++otherNext}`, now: () => 5678 },
	);

	let sequenceAllocations = 0;
	let recordB: ReturnType<typeof storageB.enqueueIntent> | undefined;
	const racingAdapter: SqliteSync = {
		...port,
		getFirstSync: <T>(sql: string, ...params: (string | number)[]) => {
			const result = port.getFirstSync<T>(sql, ...params);
			sequenceAllocations += 1;
			// The old allocator's first getFirstSync is its standalone sequence
			// read. Re-enter with a second storage instance before that allocator
			// can persist its computed value. An atomic INSERT ... RETURNING
			// allocator has already committed the next sequence by this point.
			if (sequenceAllocations === 1) recordB = storageB.enqueueIntent(intent("handle B", TARGET));
			return result;
		},
	};
	const racingStorage = new MutationOutboxSQLite(racingAdapter, { createMutationId: () => "handle-a", now: () => 1234 });

	const recordA = await racingStorage.enqueueIntent(intent("handle A", TARGET));
	if (!recordB) throw new Error("reentrant enqueue did not run");
	const persistedB = await recordB;

	const sequences = database
		.prepare("SELECT intent_sequence FROM mutation_outbox WHERE target_ref = ? ORDER BY intent_sequence")
		.all(TARGET)
		.map((row) => (row as { intent_sequence: number }).intent_sequence);
	expect([recordA.intentSequence, persistedB.intentSequence].sort()).toEqual([1, 2]);
	expect(sequences).toEqual([1, 2]);
	expect(database.prepare("SELECT last_sequence FROM mutation_sequence WHERE target_ref = ?").get(TARGET)).toMatchObject({
		last_sequence: 2,
	});
});

test("enqueueIntent rolls back when target sequence uniqueness rejects a duplicate", async () => {
	const first = await storage.enqueueIntent(intent("keep the first sequence"));
	database.prepare("UPDATE mutation_sequence SET last_sequence = 0 WHERE target_ref = ?").run(TARGET);

	await expect(storage.enqueueIntent(intent("duplicate the first sequence"))).rejects.toThrow();

	expect(rawRow("mutation_outbox", first.clientMutationId)).toBeDefined();
	expect(database.prepare("SELECT * FROM mutation_outbox WHERE target_ref = ?").all(TARGET)).toHaveLength(1);
	expect(database.prepare("SELECT last_sequence FROM mutation_sequence WHERE target_ref = ?").get(TARGET)).toMatchObject({
		last_sequence: 0,
	});
});

test("markAttempted flips a submitting record's attempted flag and refuses a non-submitting one", async () => {
	const record = await storage.enqueueIntent(intent("attempt me"));
	await expect(storage.markAttempted(record.clientMutationId)).resolves.toBe(true);
	expect(rawRow("mutation_outbox", record.clientMutationId)).toMatchObject({ attempted: 1 });
	await expect(storage.markAttempted("missing")).resolves.toBe(false);

	const blocked = await storage.enqueueIntent(intent("already blocked", "local:blocked"));
	await storage.markUnknown(blocked.clientMutationId, "blockedUnknown");
	await expect(storage.markAttempted(blocked.clientMutationId)).resolves.toBe(false);
	expect(rawRow("mutation_outbox", blocked.clientMutationId)).toMatchObject({ attempted: 0 });
});

test("markUnknown sets the given state and its onlyAttempted guard refuses an un-attempted record", async () => {
	const record = await storage.enqueueIntent(intent("unknown outcome"));
	await expect(storage.markUnknown(record.clientMutationId, "blockedUnknown", { onlyAttempted: true })).resolves.toBe(
		false,
	);
	expect(rawRow("mutation_outbox", record.clientMutationId)).toMatchObject({ state: "submitting" });

	await storage.markAttempted(record.clientMutationId);
	await expect(storage.markUnknown(record.clientMutationId, "blockedUnknown", { onlyAttempted: true })).resolves.toBe(
		true,
	);
	expect(rawRow("mutation_outbox", record.clientMutationId)).toMatchObject({ state: "blockedUnknown" });
});

// Oracle: "markUnknown's state parameter names exactly blockedUnknown"
// (mutationOutboxIndexedDB.cancel.test.ts:519, the web's 79ecf2839
// narrowing) - the native adapter never received. The compile-time guard
// against writing "canceled" or "submitting" through the uncertain-outcome
// path: "canceled" is the user's durable decision (only an explicit user
// Retry releases it), and "submitting" is the settle/reopen paths' verdict,
// never this one's. The two misuse bindings are type-level only and never
// execute.
test("markUnknown's state parameter names exactly blockedUnknown", async () => {
	const record = await storage.enqueueIntent(intent("typed row"));
	await expect(storage.markUnknown(record.clientMutationId, "blockedUnknown")).resolves.toBe(true);

	const legal: Parameters<MutationOutboxSQLite["markUnknown"]>[1] = "blockedUnknown";
	// @ts-expect-error markUnknown cannot name "canceled"
	const misusedCanceled: Parameters<MutationOutboxSQLite["markUnknown"]>[1] = "canceled";
	// @ts-expect-error markUnknown cannot name "submitting"
	const misusedSubmitting: Parameters<MutationOutboxSQLite["markUnknown"]>[1] = "submitting";
	expect([legal, misusedCanceled, misusedSubmitting].filter((value) => value === "blockedUnknown")).toEqual([
		"blockedUnknown",
	]);
	// Nothing but the one legal call ever ran: the row is exactly where the
	// call above left it.
	await expect(storage.getOutbox(record.clientMutationId)).resolves.toMatchObject({ state: "blockedUnknown" });
});

// The runtime half of the same guard: a caller with no types at all (a JS
// bridge, a deserialized argument) must not be able to create a "canceled"
// row through the uncertain-outcome path either. The refusal is loud, the
// same contract-violation style as enqueueIntent's "targetRef is required".
test("markUnknown refuses a runtime state other than blockedUnknown", async () => {
	const record = await storage.enqueueIntent(intent("runtime guarded"));
	const untyped = storage.markUnknown.bind(storage) as unknown as (id: string, state: string) => Promise<boolean>;
	await expect(untyped(record.clientMutationId, "canceled")).rejects.toThrow();
	await expect(untyped(record.clientMutationId, "submitting")).rejects.toThrow();
	expect(rawRow("mutation_outbox", record.clientMutationId)).toMatchObject({ state: "submitting" });
});

// Oracle: "a pending receipt atomically hands input display from transport
// outbox to durable optimistic state" (mutationOutbox.test.ts:235).
test("settleReceipt moves a pending, input-carrying record into the optimistic table", async () => {
	const record = await storage.enqueueIntent({
		...intent("pending incorporation"),
		composerText: "composer-only source text",
		optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text: "pending incorporation" }] },
	});
	await expect(storage.settleReceipt(record.clientMutationId, "pending")).resolves.toBe(true);
	expect(rawRow("mutation_outbox", record.clientMutationId)).toBeUndefined();
	expect(rawRow("mutation_optimistic", record.clientMutationId)).toMatchObject({ state: "accepted", composer_text: null });
});

// RoboRev PR #1873 Medium: the accepted optimistic copy is built
// field-by-field (never a spread, so recovery evidence cannot leak into it),
// and it dropped the enqueue-time instance. The copy carries it now, the way
// every other record shape does - the identity survives the outbox ->
// optimistic transition a pending receipt makes.
test("settleReceipt's accepted optimistic copy keeps the enqueue-time instance", async () => {
	const record = await storage.enqueueIntent({
		...intent("accepted with identity"),
		instanceId: "instance-at-click",
		optimisticDisplay: { input: [{ type: "text", text: "accepted with identity" }] },
	});
	await expect(storage.settleReceipt(record.clientMutationId, "pending")).resolves.toBe(true);
	expect(rawRow("mutation_optimistic", record.clientMutationId)).toMatchObject({ instance_id: "instance-at-click" });

	openStorage();
	await expect(storage.getOptimistic(record.clientMutationId)).resolves.toMatchObject({
		state: "accepted",
		instanceId: "instance-at-click",
	});
});

test("settleReceipt clears attempt evidence from the accepted optimistic record", async () => {
	const record = await storage.enqueueIntent({
		...intent("attempted before receipt"),
		optimisticDisplay: { input: [{ type: "text", text: "attempted before receipt" }] },
	});
	await storage.markAttempted(record.clientMutationId);

	await expect(storage.settleReceipt(record.clientMutationId, "pending")).resolves.toBe(true);

	expect(rawRow("mutation_optimistic", record.clientMutationId)).toMatchObject({ attempted: 0 });
	const accepted = await storage.getOptimistic(record.clientMutationId);
	expect(accepted).toMatchObject({ state: "accepted" });
	expect(accepted).not.toHaveProperty("attempted");
});

// Oracle: "a pending receipt settles a receipt-only control without creating
// optimistic display" (mutationOutbox.test.ts:280).
test("settleReceipt drops a receipt-only control with no optimistic input to carry", async () => {
	const record = await storage.enqueueIntent({
		targetRef: TARGET,
		threadId: "thread-1",
		method: "turn/interrupt",
		payload: { ref: TARGET },
		attachments: [],
		optimisticDisplay: { method: "turn/interrupt" },
	});
	await expect(storage.settleReceipt(record.clientMutationId, "pending")).resolves.toBe(true);
	expect(rawRow("mutation_outbox", record.clientMutationId)).toBeUndefined();
	expect(rawRow("mutation_optimistic", record.clientMutationId)).toBeUndefined();
});

test("settleReceipt reports false for a record that is in none of the three tables", async () => {
	await expect(storage.settleReceipt("missing", "pending")).resolves.toBe(false);
});

// Oracle: settleReceipt resolves "outbox ?? recovery ?? optimistic" as its
// source (mutationOutboxIndexedDB.ts settleReceipt) so a receipt that arrives
// after the record already moved to recovery still retires it there, instead
// of returning false and leaving a stale recovery row behind.
test("settleReceipt consults recovery when the outbox no longer holds the record", async () => {
	const record = await storage.enqueueIntent({
		...intent("recovered then receipted"),
		optimisticDisplay: { input: [{ type: "text", text: "recovered then receipted" }] },
	});
	await storage.transferToRecovery(record.clientMutationId, "rejected", "turn is not active");
	await expect(storage.settleReceipt(record.clientMutationId, "pending")).resolves.toBe(true);
	expect(rawRow("mutation_recovery", record.clientMutationId)).toBeUndefined();
	// The recovery source carried recoveryKind/recoveryReason/attempted - the
	// promoted optimistic row must not inherit them (oracle: settleReceipt
	// builds its accepted record from an explicit field list, never a spread).
	expect(rawRow("mutation_optimistic", record.clientMutationId)).toMatchObject({
		state: "accepted",
		recovery_kind: null,
		recovery_reason: null,
	});
});

// A transition landing on an existing id must replace the complete row, so a
// stale payload/display cannot survive a receipt handoff. Seed a stale row
// directly because the port's own methods keep a record's id content fixed.
test("settleReceipt's optimistic insert fully replaces a stale row rather than only refreshing state", async () => {
	const record = await storage.enqueueIntent({
		...intent("fresh display"),
		optimisticDisplay: { input: [{ type: "text", text: "fresh display" }] },
	});
	database
		.prepare(
			`INSERT INTO mutation_optimistic (client_mutation_id, version, origin_client_id, target_ref, thread_id,
				method, payload, attachments, optimistic_display, composer_text, intent_sequence, created_at, state,
				attempted, recovery_kind, recovery_reason)
			 VALUES (?, 1, NULL, ?, ?, ?, ?, '[]', ?, NULL, 0, 0, 'accepted', 0, NULL, NULL)`,
		)
		.run(
			record.clientMutationId,
			TARGET,
			"thread-1",
			"turn/queue",
			JSON.stringify({ ref: TARGET, input: [{ type: "text", text: "stale" }], clientMutationId: record.clientMutationId }),
			JSON.stringify({ input: [{ type: "text", text: "stale" }] }),
		);

	await expect(storage.settleReceipt(record.clientMutationId, "pending")).resolves.toBe(true);
	expect(rawRow("mutation_optimistic", record.clientMutationId)).toMatchObject({
		payload: JSON.stringify({
			ref: TARGET,
			input: [{ type: "text", text: "fresh display" }],
			clientMutationId: record.clientMutationId,
		}),
		optimistic_display: JSON.stringify({ input: [{ type: "text", text: "fresh display" }] }),
	});
});

test("settleReceipt drops an existing optimistic record when a later receipt no longer carries a display to retain", async () => {
	const record = await storage.enqueueIntent({
		...intent("carried once"),
		optimisticDisplay: { input: [{ type: "text", text: "carried once" }] },
	});
	await storage.settleReceipt(record.clientMutationId, "pending");
	expect(rawRow("mutation_optimistic", record.clientMutationId)).toMatchObject({ state: "accepted" });
	await expect(storage.settleReceipt(record.clientMutationId, "applied")).resolves.toBe(true);
	expect(rawRow("mutation_optimistic", record.clientMutationId)).toBeUndefined();
});

// Falsifies the settleReceipt handoff's atomicity. The handoff writes the
// optimistic row BEFORE deleting the outbox row, so a fault has to land on
// the outbox delete (not the optimistic insert) to prove the insert itself
// gets rolled back rather than surviving as an orphaned duplicate - the same
// shape as the oracle's "an aborted pending receipt handoff retains the
// transport owner without an optimistic duplicate"
// (mutationOutbox.test.ts, beforeCommit("settleReceipt")).
test("a failed pending receipt handoff leaves the outbox record with no optimistic duplicate", async () => {
	const record = await storage.enqueueIntent({
		...intent("do not leave a display gap"),
		optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text: "do not leave a display gap" }] },
	});
	database.exec(
		"CREATE TRIGGER reject_outbox_delete BEFORE DELETE ON mutation_outbox BEGIN SELECT RAISE(ABORT, 'handoff failed'); END",
	);
	await expect(storage.settleReceipt(record.clientMutationId, "pending")).rejects.toThrow("handoff failed");
	expect(rawRow("mutation_outbox", record.clientMutationId)).toBeDefined();
	expect(rawRow("mutation_optimistic", record.clientMutationId)).toBeUndefined();
});

// Oracle: "applied settlement dominates unknown in either response order..." (mutationOutbox.test.ts:193).
test("settleApplied removes a record from whichever table currently holds it", async () => {
	const outboxRecord = await storage.enqueueIntent(intent("still in outbox"));
	await expect(storage.settleApplied(outboxRecord.clientMutationId)).resolves.toBe(true);
	expect(rawRow("mutation_outbox", outboxRecord.clientMutationId)).toBeUndefined();
	await expect(storage.settleApplied(outboxRecord.clientMutationId)).resolves.toBe(false);

	const optimisticRecord = await storage.enqueueIntent({
		...intent("already accepted"),
		optimisticDisplay: { input: [{ type: "text", text: "already accepted" }] },
	});
	await storage.settleReceipt(optimisticRecord.clientMutationId, "pending");
	await expect(storage.settleApplied(optimisticRecord.clientMutationId)).resolves.toBe(true);
	expect(rawRow("mutation_optimistic", optimisticRecord.clientMutationId)).toBeUndefined();

	const recoveryRecord = await storage.enqueueIntent(intent("in recovery"));
	await storage.transferToRecovery(recoveryRecord.clientMutationId, "rejected");
	await expect(storage.settleApplied(recoveryRecord.clientMutationId)).resolves.toBe(true);
	expect(rawRow("mutation_recovery", recoveryRecord.clientMutationId)).toBeUndefined();
});

// Oracle: "a rejection carries its reason into recovery" (mutationOutbox.test.ts:221).
test("transferToRecovery moves a record out of the outbox and carries the daemon's reason", async () => {
	const record = await storage.enqueueIntent(intent("steer that lost its turn"));
	const recovery = await storage.transferToRecovery(record.clientMutationId, "rejected", "turn is not active");
	expect(recovery).toMatchObject({
		clientMutationId: record.clientMutationId,
		recoveryKind: "rejected",
		recoveryReason: "turn is not active",
	});
	expect(rawRow("mutation_outbox", record.clientMutationId)).toBeUndefined();
	expect(rawRow("mutation_recovery", record.clientMutationId)).toMatchObject({ recovery_reason: "turn is not active" });
});

test("transferToRecovery reports undefined for a record that is not in the outbox", async () => {
	await expect(storage.transferToRecovery("missing", "orphaned")).resolves.toBeUndefined();
});

// Falsifies transferToRecovery's atomicity. The transfer writes the recovery
// row BEFORE deleting the outbox row, so a fault has to land on the outbox
// delete (not the recovery insert) to prove the insert itself gets rolled
// back rather than surviving as an orphaned duplicate - the same shape as
// the oracle's "an aborted rejection transfer leaves the outbox record
// durable and creates no recovery gap"
// (mutationOutbox.test.ts, beforeCommit("transferToRecovery")).
test("a failed recovery transfer leaves the outbox record durable with no recovery row", async () => {
	const record = await storage.enqueueIntent(intent("do not lose me"));
	database.exec(
		"CREATE TRIGGER reject_outbox_delete BEFORE DELETE ON mutation_outbox BEGIN SELECT RAISE(ABORT, 'transfer failed'); END",
	);
	await expect(storage.transferToRecovery(record.clientMutationId, "rejected")).rejects.toThrow("transfer failed");
	expect(rawRow("mutation_outbox", record.clientMutationId)).toBeDefined();
	expect(rawRow("mutation_recovery", record.clientMutationId)).toBeUndefined();
});

// --- Read path --------------------------------------------------------------

test("listTargetRefs reports every ref with a waiting outbox or optimistic record", async () => {
	await storage.enqueueIntent(intent("a", "local:a"));
	const b = await storage.enqueueIntent({
		...intent("b", "local:b"),
		optimisticDisplay: { input: [{ type: "text", text: "b" }] },
	});
	await storage.settleReceipt(b.clientMutationId, "pending");
	await expect(storage.listTargetRefs()).resolves.toEqual(["local:a", "local:b"]);
});

test("getOptimistic and getRecovery read back what settleReceipt and transferToRecovery wrote", async () => {
	const accepted = await storage.enqueueIntent({
		...intent("accepted"),
		optimisticDisplay: { input: [{ type: "text", text: "accepted" }] },
	});
	await storage.settleReceipt(accepted.clientMutationId, "pending");
	await expect(storage.getOptimistic(accepted.clientMutationId)).resolves.toMatchObject({ state: "accepted" });
	await expect(storage.getOptimistic("missing")).resolves.toBeUndefined();

	const recovered = await storage.enqueueIntent(intent("recovered"));
	await storage.transferToRecovery(recovered.clientMutationId, "rejected", "turn is not active");
	await expect(storage.getRecovery(recovered.clientMutationId)).resolves.toMatchObject({
		recoveryReason: "turn is not active",
	});
});

test("listRecovery scopes composite targets and discardRecovery requires the exact target", async () => {
	const targetA = JSON.stringify(["hub-a", "same-ref"]);
	const targetB = JSON.stringify(["hub-b", "same-ref"]);
	const first = await storage.enqueueIntent({
		...intent("first", targetA),
		composerText: "first source",
		attachments: [{ presentationId: "image-a", marker: 3, name: "a.png", mediaType: "image/png" }],
	});
	const second = await storage.enqueueIntent(intent("second", targetA));
	const other = await storage.enqueueIntent(intent("other hub", targetB));
	await storage.transferToRecovery(first.clientMutationId, "rejected", "refused");
	await storage.transferToRecovery(second.clientMutationId, "orphaned");
	await storage.transferToRecovery(other.clientMutationId, "rejected");

	const scoped = await storage.listRecovery(targetA);
	expect(scoped.map((record) => record.clientMutationId)).toEqual([
		first.clientMutationId,
		second.clientMutationId,
	]);
	expect(scoped[0]).toMatchObject({
		composerText: "first source",
		targetRef: targetA,
		attachments: [{ presentationId: "image-a", marker: 3 }],
	});
	await expect(storage.listRecovery(targetB)).resolves.toEqual([expect.objectContaining({ clientMutationId: other.clientMutationId })]);

	await expect(storage.discardRecovery(first.clientMutationId, targetB)).resolves.toBe(false);
	await expect(storage.getRecovery(first.clientMutationId)).resolves.toBeDefined();
	await expect(storage.discardRecovery(first.clientMutationId, targetA)).resolves.toBe(true);
	await expect(storage.getRecovery(first.clientMutationId)).resolves.toBeUndefined();
});

test("listOptimistic scopes to a target ref and sorts by intentSequence", async () => {
	const a = await storage.enqueueIntent({
		...intent("a"),
		optimisticDisplay: { input: [{ type: "text", text: "a" }] },
	});
	const b = await storage.enqueueIntent({
		...intent("b"),
		optimisticDisplay: { input: [{ type: "text", text: "b" }] },
	});
	const other = await storage.enqueueIntent({
		...intent("other target", "local:thread-2"),
		optimisticDisplay: { input: [{ type: "text", text: "other target" }] },
	});
	await storage.settleReceipt(a.clientMutationId, "pending");
	await storage.settleReceipt(b.clientMutationId, "pending");
	await storage.settleReceipt(other.clientMutationId, "pending");

	const scoped = await storage.listOptimistic(TARGET);
	expect(scoped.map((record) => record.clientMutationId)).toEqual([a.clientMutationId, b.clientMutationId]);
	await expect(storage.listOptimistic()).resolves.toHaveLength(3);
});

// Oracle: "a blocked lower sequence prevents later dispatch without blocking
// another target" (mutationOutbox.test.ts:540).
test("nextDispatchable returns the lowest-sequence submitting record, blocked by an earlier blockedUnknown on the same target only", async () => {
	const first = await storage.enqueueIntent(intent("first"));
	const second = await storage.enqueueIntent(intent("second"));
	const other = await storage.enqueueIntent(intent("other", "local:thread-2"));
	await storage.markAttempted(first.clientMutationId);
	await storage.markUnknown(first.clientMutationId, "blockedUnknown");

	await expect(storage.nextDispatchable(TARGET)).resolves.toBeUndefined();
	await expect(storage.nextDispatchable("local:thread-2")).resolves.toMatchObject({
		clientMutationId: other.clientMutationId,
	});

	await storage.settleApplied(first.clientMutationId);
	await expect(storage.nextDispatchable(TARGET)).resolves.toMatchObject({ clientMutationId: second.clientMutationId });
});

test("restoreProvenAbsent scopes reopening to the target and preserves authoritative records", async () => {
	const omitted = await storage.enqueueIntent(intent("omitted"));
	const named = await storage.enqueueIntent(intent("named"));
	const unrelated = await storage.enqueueIntent(intent("unrelated", "local:thread-2"));
	await storage.markUnknown(omitted.clientMutationId, "blockedUnknown");
	await storage.markUnknown(named.clientMutationId, "blockedUnknown");
	await storage.markUnknown(unrelated.clientMutationId, "blockedUnknown");

	await expect(storage.restoreProvenAbsent(TARGET, new Set([named.clientMutationId]))).resolves.toEqual([
		omitted.clientMutationId,
	]);
	await expect(storage.getOutbox(omitted.clientMutationId)).resolves.toMatchObject({ state: "submitting" });
	await expect(storage.getOutbox(named.clientMutationId)).resolves.toMatchObject({ state: "blockedUnknown" });
	await expect(storage.getOutbox(unrelated.clientMutationId)).resolves.toMatchObject({ state: "blockedUnknown" });
});

// Falsifies restoreProvenAbsent's atomicity: a trigger fails the UPDATE for
// the second of two blockedUnknown records on the same target, so the loop
// throws partway through. The savepoint must roll back the first record's
// reopen too, matching every other compound write in this file, instead of
// leaving a partial restore.
test("a failed restore leaves every blockedUnknown record on the target unreopened", async () => {
	const first = await storage.enqueueIntent(intent("first blocked"));
	const second = await storage.enqueueIntent(intent("second blocked"));
	await storage.markUnknown(first.clientMutationId, "blockedUnknown");
	await storage.markUnknown(second.clientMutationId, "blockedUnknown");

	database.exec(
		`CREATE TRIGGER reject_second_restore BEFORE UPDATE ON mutation_outbox
		 WHEN NEW.client_mutation_id = '${second.clientMutationId}'
		 BEGIN SELECT RAISE(ABORT, 'restore failed'); END`,
	);

	await expect(storage.restoreProvenAbsent(TARGET, new Set())).rejects.toThrow("restore failed");
	expect(rawRow("mutation_outbox", first.clientMutationId)).toMatchObject({ state: "blockedUnknown" });
	expect(rawRow("mutation_outbox", second.clientMutationId)).toMatchObject({ state: "blockedUnknown" });
});

// --- Stop's combined durable write (the port's enqueueInterruptAndCancel) ----
//
// Oracle: cmd/evener-hub/frontend/src/stores/mutationOutboxIndexedDB.cancel.test.ts
// - the same storage-level contracts against the real SQLite engine: the
// Stop's click is the cancel moment, the cancellations and the interrupt
// record land together or not at all, and a canceled row is terminal.

// Oracle: "enqueueInterruptAndCancel cancels every non-attempted row for the
// ref and commits the interrupt with them"
// (mutationOutboxIndexedDB.cancel.test.ts:96).
test("enqueueInterruptAndCancel cancels every non-attempted row for the ref and commits the interrupt with them", async () => {
	const queued = await storage.enqueueIntent(intent("queued"));
	const blocked = await storage.enqueueIntent(intent("blocked"));
	await storage.markUnknown(blocked.clientMutationId, "blockedUnknown");
	const inFlight = await storage.enqueueIntent(intent("in flight"));
	await storage.markAttempted(inFlight.clientMutationId);
	const attemptedBlocked = await storage.enqueueIntent(intent("attempted blocked"));
	await storage.markAttempted(attemptedBlocked.clientMutationId);
	await storage.markUnknown(attemptedBlocked.clientMutationId, "blockedUnknown");
	const otherRef = await storage.enqueueIntent(intent("other ref", "local:thread-2"));

	const interrupt = await storage.enqueueInterruptAndCancel(interruptIntent());

	expect(interrupt).toMatchObject({
		method: "turn/interrupt",
		state: "submitting",
		attempted: false,
		// The ref's sequence continues: the interrupt is the ref's fifth intent.
		intentSequence: 5,
	});
	expect((await storage.getOutbox(queued.clientMutationId))?.state).toBe("canceled");
	expect((await storage.getOutbox(blocked.clientMutationId))?.state).toBe("canceled");
	// Attempted rows may be on the wire; cancellation cannot unsend them.
	expect(await storage.getOutbox(inFlight.clientMutationId)).toMatchObject({ state: "submitting", attempted: true });
	expect(await storage.getOutbox(attemptedBlocked.clientMutationId)).toMatchObject({
		state: "blockedUnknown",
		attempted: true,
	});
	expect((await storage.getOutbox(otherRef.clientMutationId))?.state).toBe("submitting");
});

// Oracle: "an aborted enqueueInterruptAndCancel commit leaves no interrupt
// record and no cancellations" (mutationOutboxIndexedDB.cancel.test.ts:129).
// The SQLite fault lands where the write order puts it: the cancellations
// run first, so failing the interrupt INSERT proves they roll back too.
test("a failed enqueueInterruptAndCancel leaves no interrupt record and no cancellations", async () => {
	const queued = await storage.enqueueIntent(intent("queued"));
	database.exec(
		"CREATE TRIGGER reject_interrupt_insert BEFORE INSERT ON mutation_outbox WHEN NEW.method = 'turn/interrupt' \
BEGIN SELECT RAISE(ABORT, 'stop failed'); END",
	);

	await expect(storage.enqueueInterruptAndCancel(interruptIntent())).rejects.toThrow("stop failed");

	expect(rawRow("mutation_outbox", queued.clientMutationId)).toMatchObject({ state: "submitting" });
	const remaining = database
		.prepare("SELECT method FROM mutation_outbox WHERE target_ref = ?")
		.all(TARGET) as { method: string }[];
	expect(remaining.map((row) => row.method)).toEqual(["turn/queue"]);
	// The sequence the failed write allocated - and the stop-epoch bump it
	// made - roll back with everything else: a leftover bump would fence
	// every later enqueue against a Stop that never landed.
	expect(
		database.prepare("SELECT last_sequence, stop_epoch FROM mutation_sequence WHERE target_ref = ?").get(TARGET),
	).toMatchObject({ last_sequence: 1, stop_epoch: 0 });
});

test("enqueueInterruptAndCancel rejects an empty or whitespace targetRef before writing anything", async () => {
	await expect(storage.enqueueInterruptAndCancel(interruptIntent("   "))).rejects.toThrow("targetRef is required");
	expect(database.prepare("SELECT * FROM mutation_outbox").all()).toEqual([]);
	expect(database.prepare("SELECT * FROM mutation_sequence").all()).toEqual([]);
});

// Oracle: "a canceled row never reopens, even across a reload"
// (mutationOutboxIndexedDB.cancel.test.ts:146). The native Stop always carries
// its interrupt, so the reopen proof is restoreProvenAbsent leaving the
// canceled rows - and the interrupt's own submitting row - alone.
test("a canceled row never reopens, even across a fresh storage instance", async () => {
	const queued = await storage.enqueueIntent(intent("queued"));
	const blocked = await storage.enqueueIntent(intent("blocked"));
	await storage.markUnknown(blocked.clientMutationId, "blockedUnknown");
	await storage.enqueueInterruptAndCancel(interruptIntent());

	// Reconciliation proves nothing about these rows; restoreProvenAbsent is
	// the one reopen path, and it must leave them canceled.
	await expect(storage.restoreProvenAbsent(TARGET, new Set())).resolves.toEqual([]);

	// A fresh adapter over the same database - what an app restart is - agrees.
	openStorage();
	await expect(storage.restoreProvenAbsent(TARGET, new Set())).resolves.toEqual([]);
	expect((await storage.getOutbox(queued.clientMutationId))?.state).toBe("canceled");
	expect((await storage.getOutbox(blocked.clientMutationId))?.state).toBe("canceled");
});

// Oracle: "a canceled head row does not park the queue, but a blockedUnknown
// head still does" (mutationOutboxIndexedDB.cancel.test.ts:545).
test("nextDispatchable skips canceled rows without parking the queue, and the interrupt still dispatches", async () => {
	await storage.enqueueIntent(intent("canceled head"));
	const interrupt = await storage.enqueueInterruptAndCancel(interruptIntent());
	// A Stop whose own interrupt never went out would stop nothing: the
	// canceled rows ahead of it do not park the interrupt behind them.
	await expect(storage.nextDispatchable(TARGET)).resolves.toMatchObject({ clientMutationId: interrupt.clientMutationId });

	await storage.settleApplied(interrupt.clientMutationId);
	const later = await storage.enqueueIntent(intent("later send"));
	// The canceled row was provably never sent, so the later intent may go.
	await expect(storage.nextDispatchable(TARGET)).resolves.toMatchObject({ clientMutationId: later.clientMutationId });

	await storage.settleApplied(later.clientMutationId);
	// Delivery-uncertain rows keep the FIFO closed: skipping them could
	// reorder a send the daemon may already have applied.
	const blockedHead = await storage.enqueueIntent(intent("blocked head"));
	await storage.markUnknown(blockedHead.clientMutationId, "blockedUnknown");
	await storage.enqueueIntent(intent("queued behind"));
	await expect(storage.nextDispatchable(TARGET)).resolves.toBeUndefined();
});

// Oracle: "a canceled row cannot be marked attempted or reclassified back to
// blockedUnknown" (mutationOutboxIndexedDB.cancel.test.ts:506).
test("a canceled row cannot be marked attempted or reclassified back to blockedUnknown", async () => {
	const queued = await storage.enqueueIntent(intent("queued"));
	await storage.enqueueInterruptAndCancel(interruptIntent());

	// Cancellation is terminal: only an explicit Retry releases the row, and a
	// late uncertain-outcome write must not make it reopenable again.
	await expect(storage.markAttempted(queued.clientMutationId)).resolves.toBe(false);
	await expect(storage.markUnknown(queued.clientMutationId, "blockedUnknown")).resolves.toBe(false);
	expect(await storage.getOutbox(queued.clientMutationId)).toMatchObject({ state: "canceled", attempted: false });
});

// Oracle: "an in-flight enqueue whose barrier predates another tab's stop
// commits canceled, not submitting" (mutationOutboxIndexedDB.cancel.test.ts:186).
// The submitter reads the ref's stop epoch at its click, the Stop commits
// while the submission is still in flight, and the write that lands after
// that cancel must commit born-"canceled".
test("an in-flight enqueue whose barrier predates the stop commits canceled, not submitting", async () => {
	const capture = await storage.readStopEpoch(TARGET);
	expect(capture).toBe(0);

	const interrupt = await storage.enqueueInterruptAndCancel(interruptIntent());

	const raced = await storage.enqueueIntent(intent("raced the stop"), { stopEpoch: capture });
	expect(raced).toMatchObject({ state: "canceled", attempted: false });
	// The FIFO head is the stop's own interrupt, never the canceled row behind it.
	await expect(storage.nextDispatchable(TARGET)).resolves.toMatchObject({ clientMutationId: interrupt.clientMutationId });
});

// Oracle: "an enqueue after the stop captures the bumped epoch and still
// sends" (mutationOutboxIndexedDB.cancel.test.ts:207).
test("an enqueue after the stop captures the bumped epoch and still sends", async () => {
	await storage.enqueueInterruptAndCancel(interruptIntent());
	const capture = await storage.readStopEpoch(TARGET);
	expect(capture).toBe(1);

	// The user clicked send after the stop: the barrier the click captured is
	// the post-stop epoch, nothing intervenes, and the row goes live. A caller
	// passing no barrier at all (a host that never captured) is unchanged.
	const after = await storage.enqueueIntent(intent("sent after the stop"), { stopEpoch: capture });
	const barrierless = await storage.enqueueIntent(intent("no barrier"));
	expect(after.state).toBe("submitting");
	expect(barrierless.state).toBe("submitting");
});

// Oracle: "the stop epoch survives later enqueues, a reload, and both stop
// paths bump it" (mutationOutboxIndexedDB.cancel.test.ts:227). The port
// declares one stop path natively (the combined write); the epoch must ride
// the sequence row every allocation rewrites, not just the ones that bump it.
test("the stop epoch survives later enqueues and a fresh storage instance", async () => {
	// Two instances share one id space, the way every storage in this file
	// shares one database: distinct instances mint distinct ids.
	let next = 0;
	const instance = () =>
		new MutationOutboxSQLite(port, {
			createMutationId: () => `mutation-${++next}`,
			now: () => 1234,
		});

	const first = instance();
	await first.enqueueInterruptAndCancel(interruptIntent());
	await first.enqueueIntent(intent("after the stop"));
	await first.enqueueIntent(intent("and another"));
	await expect(first.readStopEpoch(TARGET)).resolves.toBe(1);

	const reloaded = instance();
	await expect(reloaded.readStopEpoch(TARGET)).resolves.toBe(1);
	await reloaded.enqueueInterruptAndCancel(interruptIntent());
	await expect(reloaded.readStopEpoch(TARGET)).resolves.toBe(2);
});

// SQLite-specific, no IndexedDB analog: a database the pre-barrier adapter
// created has no stop_epoch column on mutation_sequence. The constructor
// migrates it in place - the additive default-0 column is the whole
// migration - and the existing sequence counter survives it.
test("a pre-barrier mutation_sequence table migrates in place without losing its sequence", async () => {
	database.exec("DROP TABLE mutation_sequence");
	database.exec("CREATE TABLE mutation_sequence (target_ref TEXT PRIMARY KEY, last_sequence INTEGER NOT NULL)");
	database.prepare("INSERT INTO mutation_sequence (target_ref, last_sequence) VALUES (?, 7)").run(TARGET);

	openStorage();

	await expect(storage.readStopEpoch(TARGET)).resolves.toBe(0);
	const after = await storage.enqueueIntent(intent("after the migration"));
	expect(after.intentSequence).toBe(8);
	expect(
		database.prepare("SELECT last_sequence, stop_epoch FROM mutation_sequence WHERE target_ref = ?").get(TARGET),
	).toMatchObject({ last_sequence: 8, stop_epoch: 0 });
});

// RoboRev PR #1873 Medium, the same additive-migration style as stop_epoch's:
// the record tables predate the instance_id column (the adapter previously
// dropped the identity entirely), and ALTER TABLE has no IF NOT EXISTS, so
// the constructor checks each table's columns and adds the nullable column -
// the whole migration, no data rewrite. Rows written before the field existed
// read as instanceId-undefined, so their identity falls back to the
// threadId - exactly what a model with no instanceId presents.
test("pre-instanceId record tables migrate in place and their existing rows read as identity-less", async () => {
	database.exec("DROP TABLE mutation_outbox");
	database.exec(`CREATE TABLE mutation_outbox (client_mutation_id TEXT PRIMARY KEY, version INTEGER NOT NULL,
		origin_client_id TEXT, target_ref TEXT NOT NULL, thread_id TEXT, method TEXT NOT NULL,
		payload TEXT NOT NULL, attachments TEXT NOT NULL, optimistic_display TEXT NOT NULL,
		composer_text TEXT, intent_sequence INTEGER NOT NULL, created_at INTEGER NOT NULL,
		state TEXT NOT NULL, attempted INTEGER NOT NULL DEFAULT 0, recovery_kind TEXT, recovery_reason TEXT)`);
	database
		.prepare(
			`INSERT INTO mutation_outbox (client_mutation_id, version, origin_client_id, target_ref, thread_id, method,
				payload, attachments, optimistic_display, composer_text, intent_sequence, created_at, state, attempted)
			 VALUES ('mutation-pre-instance', 1, NULL, ?, 'thread-1', 'turn/queue', '{}', '[]', 'null', NULL, 1, 1234, 'submitting', 0)`,
		)
		.run(TARGET);
	// The seeded row occupies the ref's sequence 1, so the allocation the
	// post-migration enqueue makes must continue past it - the same
	// sequence-survival assertion the pre-barrier migration test makes.
	database.prepare("INSERT INTO mutation_sequence (target_ref, last_sequence) VALUES (?, 1)").run(TARGET);

	openStorage();

	const existing = await storage.getOutbox("mutation-pre-instance");
	expect(existing?.instanceId).toBeUndefined();
	expect(rawRow("mutation_outbox", "mutation-pre-instance")).toMatchObject({ instance_id: null });

	const after = await storage.enqueueIntent({ ...intent("after the migration"), instanceId: "instance-new" });
	expect(after.intentSequence).toBe(2);
	expect(rawRow("mutation_outbox", after.clientMutationId)).toMatchObject({ instance_id: "instance-new" });
});
