import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
	describeQueuedMessageStorage,
	queuedIntent,
} from "@evener/appwire-client/testing/queuedMessageStorageConformance";
import { afterEach, expect, test, vi } from "vitest";
import { MutationOutboxSQLite } from "./mutationOutboxStorage";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "./sqliteSync.testkit";
import type { SqliteSync } from "./sqliteSync";
vi.mock("expo-crypto", () => ({ randomUUID: () => "unused", getRandomValues: (array: Uint8Array) => array }));
const databases: SqliteDoubleDatabase[] = [];
const directories: string[] = [];
afterEach(() => {
	for (const db of databases.splice(0)) db.close();
	for (const dir of directories.splice(0)) rmSync(dir, { recursive: true });
});
function fixture() {
	const dir = mkdtempSync(join(process.env.EVENER_SCRATCH_DIR ?? tmpdir(), "queued-sqlite-"));
	directories.push(dir);
	const path = join(dir, "outbox.sqlite");
	let id = 0;
	let failure: RegExp | undefined;
	const open = () => {
		const { database, port } = openSqliteSyncDouble(path);
		databases.push(database);
		const check = (sql: string) => {
			if (failure?.test(sql)) throw new Error("injected storage failure");
		};
		const guarded: SqliteSync = {
			...port,
			execSync: (sql) => {
				check(sql);
				return port.execSync(sql);
			},
			runSync: (sql, ...args) => {
				check(sql);
				return port.runSync(sql, ...args);
			},
			getFirstSync: (sql, ...args) => {
				check(sql);
				return port.getFirstSync(sql, ...args);
			},
		};
		return new MutationOutboxSQLite(guarded, { createMutationId: () => `mutation-${++id}` });
	};
	const storage = open();
	const peer = open();
	return {
		storage,
		peer,
		fail: (pattern?: RegExp) => {
			failure = pattern;
		},
		reopen: async () => {
			for (const db of databases.splice(0)) db.close();
			return open();
		},
		release: (record: { clientMutationId: string; targetRef: string }, barrier: { stopEpoch: number }) =>
			peer.releaseCanceled(record.clientMutationId, record.targetRef, barrier),
	};
}
describeQueuedMessageStorage("SQLite", fixture);

test("composer mentions persist across a database reopen", async () => {
	const f = fixture();
	const original = await f.storage.enqueueIntent(queuedIntent());
	expect(await (await f.reopen()).getOutbox(original.clientMutationId)).toMatchObject({
		composerMentions: [{ kind: "skill", name: "inspect", offset: 11 }],
	});
});

test.each([/INSERT INTO mutation_recovery/, /DELETE FROM mutation_outbox/, /^RELEASE/])(
	"edit rolls back at %s and retries",
	async (boundary) => {
		const f = fixture();
		const original = await f.storage.enqueueIntent(queuedIntent());
		f.fail(boundary);
		await expect(f.storage.editQueuedMessage(original, { stopEpoch: 0 })).rejects.toBeDefined();
		f.fail();
		const storage = await f.reopen();
		expect(await storage.getOutbox(original.clientMutationId)).toMatchObject(original);
		expect(await storage.getRecovery(original.clientMutationId)).toBeUndefined();
		expect(await storage.editQueuedMessage(original, { stopEpoch: 0 })).toMatchObject({ recoveryKind: "editing" });
	},
);

test("remove rollback retains source for successful retry", async () => {
	const f = fixture();
	const original = await f.storage.enqueueIntent(queuedIntent());
	f.fail(/^RELEASE/);
	await expect(f.storage.removeQueuedMessage(original, { stopEpoch: 0 })).rejects.toBeDefined();
	f.fail();
	expect(await f.peer.getOutbox(original.clientMutationId)).toMatchObject(original);
	expect(await f.peer.removeQueuedMessage(original, { stopEpoch: 0 })).toBe(true);
});

test.each([
	/INSERT INTO mutation_sequence/,
	/INSERT INTO mutation_outbox/,
	/DELETE FROM mutation_recovery/,
	/UPDATE mutation_sequence SET recovery_resends/,
	/^RELEASE/,
])("resend rolls back at %s and retries", async (boundary) => {
	const f = fixture();
	const ids = [];
	for (let i = 0; i < 2; i++) {
		const original = await f.storage.enqueueIntent(queuedIntent());
		ids.push(original.clientMutationId);
		await f.storage.transferToRecovery(original.clientMutationId, "rejected");
	}
	f.fail(boundary);
	await expect(f.storage.resendRecovery(ids, queuedIntent())).rejects.toBeDefined();
	f.fail();
	const storage = await f.reopen();
	expect(await storage.listOutbox()).toHaveLength(0);
	expect(await storage.readRecoveryResend("local:queued", ids)).toBeUndefined();
	for (const id of ids) expect(await storage.getRecovery(id)).toBeDefined();
	expect(await storage.resendRecovery(ids, queuedIntent())).toMatchObject({ intentSequence: 3 });
});

test("resendReceiptSurvivesSettlement", async () => {
	const f = fixture();
	const ids = [];
	for (let i = 0; i < 2; i++) {
		const original = await f.storage.enqueueIntent(queuedIntent());
		ids.push(original.clientMutationId);
		await f.storage.transferToRecovery(original.clientMutationId, "rejected");
	}
	const resend = await f.storage.resendRecovery(ids, queuedIntent());
	if (!resend) throw new Error("missing resend");
	await f.storage.settleReceipt(resend.clientMutationId, "pending");
	await f.storage.settleApplied(resend.clientMutationId);
	const storage = await f.reopen();
	const receipt = { targetRef: "local:queued", sourceIds: ids, clientMutationId: resend.clientMutationId };
	expect(await storage.getOutbox(resend.clientMutationId)).toBeUndefined();
	expect(await storage.getOptimistic(resend.clientMutationId)).toBeUndefined();
	expect(await storage.readRecoveryResend("local:queued", [...ids].reverse())).toEqual(receipt);
	expect(await storage.readRecoveryResend("other:target", ids)).toBeUndefined();
	expect(await storage.readRecoveryResend("local:queued", [ids[0] ?? ""])).toBeUndefined();
	expect(await storage.resendRecovery(ids, queuedIntent())).toBeUndefined();
	await storage.acknowledgeRecoveryResend("local:queued", "other-id");
	expect(await storage.readRecoveryResend("local:queued", ids)).toEqual(receipt);
	f.fail(/^RELEASE/);
	await expect(storage.acknowledgeRecoveryResend("local:queued", resend.clientMutationId)).rejects.toBeDefined();
	f.fail();
	expect(await storage.readRecoveryResend("local:queued", ids)).toEqual(receipt);
	await storage.acknowledgeRecoveryResend("local:queued", resend.clientMutationId);
	await storage.acknowledgeRecoveryResend("local:queued", resend.clientMutationId);
	expect(await (await f.reopen()).readRecoveryResend("local:queued", ids)).toBeUndefined();
});

test("acknowledgment retires only its receipt and a failed write can be retried", async () => {
	const f = fixture();
	const first = await f.storage.enqueueIntent(queuedIntent());
	await f.storage.transferToRecovery(first.clientMutationId, "rejected");
	const firstSend = await f.storage.resendRecovery(first.clientMutationId, queuedIntent());
	const second = await f.storage.enqueueIntent(queuedIntent());
	await f.storage.transferToRecovery(second.clientMutationId, "rejected");
	const secondSend = await f.storage.resendRecovery(second.clientMutationId, queuedIntent());
	if (!firstSend || !secondSend) throw new Error("missing sends");
	await f.storage.enqueueInterruptAndCancel({ ...queuedIntent(), method: "turn/interrupt" });
	await f.storage.acknowledgeRecoveryResend("other:target", firstSend.clientMutationId);
	expect(await f.storage.readRecoveryResend(first.targetRef, [first.clientMutationId])).toMatchObject({
		clientMutationId: firstSend.clientMutationId,
	});
	f.fail(/UPDATE mutation_sequence SET recovery_resends/);
	await expect(f.storage.acknowledgeRecoveryResend(first.targetRef, firstSend.clientMutationId)).rejects.toBeDefined();
	f.fail();
	expect(await f.storage.readRecoveryResend(first.targetRef, [first.clientMutationId])).toBeDefined();
	await f.storage.acknowledgeRecoveryResend(first.targetRef, firstSend.clientMutationId);
	const storage = await f.reopen();
	expect(await storage.readRecoveryResend(first.targetRef, [first.clientMutationId])).toBeUndefined();
	expect(await storage.readRecoveryResend(second.targetRef, [second.clientMutationId])).toEqual({
		targetRef: second.targetRef,
		sourceIds: [second.clientMutationId],
		clientMutationId: secondSend.clientMutationId,
	});
});

test("old-schema reopen migrates mentions and receipts without guessing image references", async () => {
	const f = fixture();
	const attachments = [
		{ presentationId: "a", marker: 4, name: "a", mediaType: "image/png", data: "AP8H" },
		{ presentationId: "b", marker: 9, name: "b", mediaType: "image/png", data: "AQID" },
	];
	const original = await f.storage.enqueueIntent({
		...queuedIntent(),
		attachments,
		composerText: undefined,
		composerMentions: undefined,
	});
	// Construct the baseline schema using real SQLite's additive-migration inverse.
	for (const table of ["mutation_outbox", "mutation_optimistic", "mutation_recovery"]) {
		const columns = f.storage.db.getAllSync<{ name: string }>(`PRAGMA table_info(${table})`);
		if (columns.some((column) => column.name === "composer_mentions"))
			f.storage.db.execSync(`ALTER TABLE ${table} DROP COLUMN composer_mentions`);
	}
	const sequenceColumns = f.storage.db.getAllSync<{ name: string }>("PRAGMA table_info(mutation_sequence)");
	if (sequenceColumns.some((column) => column.name === "recovery_resends"))
		f.storage.db.execSync("ALTER TABLE mutation_sequence DROP COLUMN recovery_resends");
	const storage = await f.reopen();
	const edit = await storage.editQueuedMessage(original, { stopEpoch: 0 });
	expect(edit).toMatchObject({
		recoveryKind: "editing",
		attachments,
		payload: {
			input: [
				{ type: "text", text: "same input" },
				{ type: "skill", name: "inspect" },
			],
		},
	});
	expect(edit?.composerText).toBeUndefined();
	expect(edit?.composerMentions).toBeUndefined();
	const fresh = await storage.enqueueIntent(queuedIntent());
	await storage.editQueuedMessage(fresh, { stopEpoch: 0 });
	const reopened = await f.reopen();
	expect(await reopened.getRecovery(fresh.clientMutationId)).toMatchObject({
		composerMentions: [{ kind: "skill", name: "inspect", offset: 11 }],
	});
	expect(await reopened.resendRecovery(original.clientMutationId, queuedIntent())).toBeDefined();
});
