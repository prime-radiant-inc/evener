// The write-path tests below verify each write's effect against the raw
// SQLite row directly (they predate the read methods this storage now also
// has, landed as a separate PR); the read-path tests verify through the
// port's own getOutbox/getOptimistic/listOptimistic/getRecovery/
// nextDispatchable/listTargetRefs/restoreProvenAbsent instead.
//
// Oracle: cmd/evener-hub/frontend/src/stores/mutationOutbox.test.ts's
// describe("MutationOutboxIndexedDB", ...) block - these assertions mirror
// its per-method contracts against a real (non-mock) SQLite engine, the way
// draftRepository.test.ts already runs the native draft storage against
// node:sqlite's DatabaseSync rather than expo-sqlite (unavailable outside a
// device/simulator).
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { DatabaseSync } from "node:sqlite";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { MutationIntent } from "@evener/appwire-client/state/mutation";
import { MutationOutboxSQLite, type MutationOutboxDatabase, type Row } from "./mutationOutboxStorage";

// The default id source (finding 4, round 2): expo-crypto's synchronous
// randomUUID/getRandomValues, mocked the way nativeOrganization.test.ts mocks
// the same module, so a test can prove the adapter never dereferences a bare
// Web Crypto global React Native does not guarantee.
let expoCryptoCalls = 0;
vi.mock("expo-crypto", () => ({
	randomUUID: () => `expo-crypto-${++expoCryptoCalls}`,
	getRandomValues: (array: Uint8Array) => array,
}));

let directory: string;
let database: DatabaseSync;
let storage: MutationOutboxSQLite;

function databaseAdapter(): MutationOutboxDatabase {
	return {
		execSync: (sql) => database.exec(sql),
		runSync: (sql, ...params) => database.prepare(sql).run(...params),
		getFirstSync: <T>(sql: string, ...params: (string | number)[]) =>
			(database.prepare(sql).get(...params) as T | undefined) ?? null,
		getAllSync: <T>(sql: string, ...params: (string | number)[]) => database.prepare(sql).all(...params) as T[],
	};
}

function openStorage() {
	let next = 0;
	storage = new MutationOutboxSQLite(databaseAdapter(), {
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
	database = new DatabaseSync(join(directory, "outbox.sqlite"));
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

test("enqueueIntent persists an absent optimistic display as JSON null so settlement can retire it", async () => {
	const record = await storage.enqueueIntent({
		...intent("without an optimistic display"),
		optimisticDisplay: undefined,
	});

	expect(rawRow("mutation_outbox", record.clientMutationId)).toMatchObject({ optimistic_display: "null" });
	await expect(storage.settleReceipt(record.clientMutationId, "pending")).resolves.toBe(true);
	expect(rawRow("mutation_outbox", record.clientMutationId)).toBeUndefined();
	expect(rawRow("mutation_optimistic", record.clientMutationId)).toBeUndefined();
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
	const collidingIdStorage = new MutationOutboxSQLite(databaseAdapter(), {
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

test("enqueueIntent defaults the mutation id through expo-crypto's SecureRandomSource, never a bare Web Crypto global", async () => {
	const originalCrypto = globalThis.crypto;
	// Simulate a host with no Web Crypto global at all - the case React Native
	// does not guarantee - to falsify a default that dereferences it directly.
	// @ts-expect-error - deliberately removing the global for this assertion.
	delete globalThis.crypto;
	try {
		const defaultIdStorage = new MutationOutboxSQLite(databaseAdapter(), { now: () => 1234 });
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

// Round 4 Medium (mutationOutboxStorage.ts:152-163): the original allocation
// read last_sequence on its own statement, separate from the write that
// persists the next value, so two enqueue operations could both compute the
// same intentSequence. SQLite serializes actual writers on separate handles
// (the direct two-handle attempt reports `database is locked` while the first
// savepoint still owns its read), so this deterministic reentrant call forces
// the same statement interleaving on one real SQLite connection.
test("enqueueIntent's sequence allocation never collides when enqueue operations interleave", async () => {
	let otherNext = 0;
	const storageB = new MutationOutboxSQLite(
		databaseAdapter(),
		{ createMutationId: () => `other-${++otherNext}`, now: () => 5678 },
	);

	let sequenceAllocations = 0;
	let recordB: ReturnType<typeof storageB.enqueueIntent> | undefined;
	const racingAdapter: MutationOutboxDatabase = {
		...databaseAdapter(),
		getFirstSync: <T>(sql: string, ...params: (string | number)[]) => {
			const result = databaseAdapter().getFirstSync<T>(sql, ...params);
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

// insert() used to update only state/attempted on a primary-key conflict, so
// a transition landing on a row that already occupies that id would keep the
// old payload/display. Seeds a stale row directly (this table's id space
// never legitimately repeats through the port's own methods today, since a
// record's stored content is fixed at enqueueIntent) to prove the write this
// call makes replaces every column, the way the oracle's `put` does.
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

// --- D25d-1b: the read path -------------------------------------------------

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

test("restoreProvenAbsent reopens a blockedUnknown record the authoritative snapshot omits, and leaves one it names alone", async () => {
	const omitted = await storage.enqueueIntent(intent("omitted"));
	const named = await storage.enqueueIntent(intent("named", "local:thread-2"));
	await storage.markUnknown(omitted.clientMutationId, "blockedUnknown");
	await storage.markUnknown(named.clientMutationId, "blockedUnknown");

	await expect(storage.restoreProvenAbsent(TARGET, new Set([named.clientMutationId]))).resolves.toEqual([
		omitted.clientMutationId,
	]);
	await expect(storage.getOutbox(omitted.clientMutationId)).resolves.toMatchObject({ state: "submitting" });

	await expect(storage.restoreProvenAbsent("local:thread-2", new Set([named.clientMutationId]))).resolves.toEqual([]);
	await expect(storage.getOutbox(named.clientMutationId)).resolves.toMatchObject({ state: "blockedUnknown" });
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
