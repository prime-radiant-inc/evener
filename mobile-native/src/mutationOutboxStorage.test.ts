// D25d-1a: the write half only (D25d-1b, stacked on this, adds the read
// methods - getOutbox/getOptimistic/listOptimistic/getRecovery/
// nextDispatchable/listTargetRefs/restoreProvenAbsent - so these tests
// verify each write's effect against the raw SQLite row rather than through
// a read method that does not exist yet).
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

test("enqueueIntent rejects an empty or whitespace targetRef before allocating a sequence", async () => {
	await expect(storage.enqueueIntent(intent("no target", "   "))).rejects.toThrow("targetRef is required");
	expect(database.prepare("SELECT * FROM mutation_sequence WHERE target_ref = ?").get("   ")).toBeUndefined();
	expect(database.prepare("SELECT * FROM mutation_outbox").all()).toEqual([]);
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

test("markAttempted flips a submitting record's attempted flag and refuses a non-submitting one", async () => {
	const record = await storage.enqueueIntent(intent("attempt me"));
	await expect(storage.markAttempted(record.clientMutationId)).resolves.toBe(true);
	expect(rawRow("mutation_outbox", record.clientMutationId)).toMatchObject({ attempted: 1 });
	await expect(storage.markAttempted("missing")).resolves.toBe(false);
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
		optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text: "pending incorporation" }] },
	});
	await expect(storage.settleReceipt(record.clientMutationId, "pending")).resolves.toBe(true);
	expect(rawRow("mutation_outbox", record.clientMutationId)).toBeUndefined();
	expect(rawRow("mutation_optimistic", record.clientMutationId)).toMatchObject({ state: "accepted" });
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
	expect(rawRow("mutation_optimistic", record.clientMutationId)).toMatchObject({ state: "accepted" });
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
