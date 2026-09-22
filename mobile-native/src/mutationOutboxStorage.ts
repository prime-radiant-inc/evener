// Native implementation of the package's MutationOutboxStorage port
// (appwire-client/typescript/state/mutation/outbox.ts) over expo-sqlite. The
// adapter owns the SQLite handle because the package port names no host
// storage global.
//
// Oracle: cmd/evener-hub/frontend/src/stores/mutationOutbox.test.ts's
// describe("MutationOutboxIndexedDB", ...) block. This mirrors its
// contracts (gap-free per-target sequencing, settleReceipt's
// pending-input-carrying-record-becomes-optimistic rule, markUnknown's
// onlyAttempted guard, enqueueInterruptAndCancel's cancel-and-interrupt
// Stop write with the stop-epoch barrier enqueueIntent compares at commit,
// nextDispatchable blocked by an earlier blockedUnknown on the same target
// only while skipping canceled rows, restoreProvenAbsent reopening only
// what the authoritative snapshot omits) without the web's Blob handling,
// cross-tab identity or shared-notes recovery-superseding, none of which
// the port declares.
import * as Crypto from "expo-crypto";
import type {
	MutationAttachmentRef,
	MutationIntent,
	MutationOptimisticRecord,
	MutationOutboxRecord,
	MutationOutboxStorage,
	MutationOutboxState,
	MutationRecord,
	MutationRecoveryKind,
	MutationRecoveryRecord,
	MutationStopBarrier,
	SecureRandomSource,
} from "@evener/appwire-client/state/mutation";
import { createSecureUUID } from "@evener/appwire-client/state/mutation";
import { type SqliteSync, type SqliteSyncRunResult, withSavepoint } from "./sqliteSync";

// This app's SecureRandomSource: expo-crypto's synchronous randomUUID and
// getRandomValues, the native module every other id-generating call site in
// this app already uses (credentialStore.ts, nativeOrganization.ts) instead
// of a bare Web Crypto global React Native does not guarantee.
function nativeRandomSource(): SecureRandomSource {
	return { randomUUID: Crypto.randomUUID, getRandomValues: Crypto.getRandomValues };
}

function changedRows(result: SqliteSyncRunResult): number {
	return typeof result.changes === "bigint" ? Number(result.changes) : result.changes;
}

export interface MutationOutboxSQLiteOptions {
	createMutationId?: () => string;
	// The port every id-generating default in this adapter goes through
	// (createSecureUUID) rather than dereferencing a host global directly.
	// Defaults to expo-crypto; tests inject their own.
	randomSource?: SecureRandomSource;
	now?: () => number;
	// The submitting client's own id (this store's ClientIdentity), read fresh
	// per enqueue rather than captured at construction - a client swap must not
	// retroactively change who submitted an already-queued record.
	getOwnClientId?: () => string | undefined;
}

// The one row shape all three tables share: a record's current table IS its
// state (submitting/blockedUnknown -> outbox, accepted -> optimistic,
// rejected/orphaned -> recovery), so no separate state machine duplicates
// what the table membership already says.
export const TABLES = { outbox: "mutation_outbox", optimistic: "mutation_optimistic", recovery: "mutation_recovery" } as const;

const COLUMNS = [
	"client_mutation_id",
	"version",
	"origin_client_id",
	"target_ref",
	"thread_id",
	"instance_id",
	"method",
	"payload",
	"attachments",
	"optimistic_display",
	"composer_text",
	"intent_sequence",
	"created_at",
	"state",
	"attempted",
	"recovery_kind",
	"recovery_reason",
] as const;

const insertSQL = (table: string): string =>
	`INSERT INTO ${table} (${COLUMNS.join(", ")}) VALUES (${COLUMNS.map(() => "?").join(", ")})`;

const replaceSQL = (table: string): string =>
	`${insertSQL(table)} ON CONFLICT (client_mutation_id) DO UPDATE SET ${COLUMNS.slice(1)
		.map((column) => `${column} = excluded.${column}`)
		.join(", ")}`;

export interface Row {
	client_mutation_id: string;
	version: number;
	origin_client_id: string | null;
	target_ref: string;
	thread_id: string | null;
	instance_id: string | null;
	method: string;
	payload: string;
	attachments: string;
	optimistic_display: string;
	composer_text: string | null;
	intent_sequence: number;
	created_at: number;
	state: string;
	attempted: number;
	recovery_kind: string | null;
	recovery_reason: string | null;
}

export function fromRow<A extends MutationAttachmentRef, T extends MutationRecord<A>>(row: Row): T {
	const optimisticDisplay = JSON.parse(row.optimistic_display);
	return {
		version: row.version as 1,
		clientMutationId: row.client_mutation_id,
		originClientId: row.origin_client_id ?? undefined,
		targetRef: row.target_ref,
		threadId: row.thread_id ?? undefined,
		instanceId: row.instance_id ?? undefined,
		method: row.method,
		payload: JSON.parse(row.payload),
		attachments: JSON.parse(row.attachments),
		optimisticDisplay: optimisticDisplay === null ? undefined : optimisticDisplay,
		composerText: row.composer_text ?? undefined,
		intentSequence: row.intent_sequence,
		createdAt: row.created_at,
		state: row.state as MutationOutboxState,
		...(row.state === "accepted" ? {} : { attempted: row.attempted === 1 }),
		...(row.recovery_kind ? { recoveryKind: row.recovery_kind, recoveryReason: row.recovery_reason ?? undefined } : {}),
	} as unknown as T;
}

// MutationOutboxSQLite implements the package's MutationOutboxStorage port
// over one synchronous database handle, wrapped in resolved promises
// (outbox.ts's own comment: "a host with a synchronous store returns
// resolved promises").
export class MutationOutboxSQLite<A extends MutationAttachmentRef = MutationAttachmentRef>
	implements MutationOutboxStorage<A>
{
	readonly db: SqliteSync;
	readonly #createMutationId: () => string;
	readonly #now: () => number;
	readonly #getOwnClientId: () => string | undefined;

	constructor(db: SqliteSync, options: MutationOutboxSQLiteOptions = {}) {
		this.db = db;
		const randomSource = options.randomSource ?? nativeRandomSource();
		this.#createMutationId = options.createMutationId ?? (() => createSecureUUID(randomSource));
		this.#now = options.now ?? Date.now;
		this.#getOwnClientId = options.getOwnClientId ?? (() => undefined);
		const schema = (table: string) => `CREATE TABLE IF NOT EXISTS ${table} (
			client_mutation_id TEXT PRIMARY KEY, version INTEGER NOT NULL,
			origin_client_id TEXT, target_ref TEXT NOT NULL, thread_id TEXT, instance_id TEXT, method TEXT NOT NULL,
			payload TEXT NOT NULL, attachments TEXT NOT NULL, optimistic_display TEXT NOT NULL,
			composer_text TEXT, intent_sequence INTEGER NOT NULL, created_at INTEGER NOT NULL,
			state TEXT NOT NULL, attempted INTEGER NOT NULL DEFAULT 0,
			recovery_kind TEXT, recovery_reason TEXT)`;
		for (const table of Object.values(TABLES)) this.db.execSync(schema(table));
		this.db.execSync(
			`CREATE UNIQUE INDEX IF NOT EXISTS mutation_outbox_target_sequence
			 ON ${TABLES.outbox} (target_ref, intent_sequence)`,
		);
		this.db.execSync(
			`CREATE UNIQUE INDEX IF NOT EXISTS mutation_optimistic_target_sequence
			 ON ${TABLES.optimistic} (target_ref, intent_sequence)`,
		);
		this.db.execSync(
			`CREATE INDEX IF NOT EXISTS mutation_recovery_target_sequence
			 ON ${TABLES.recovery} (target_ref, intent_sequence)`,
		);
		this.db.execSync(
			`CREATE TABLE IF NOT EXISTS mutation_sequence (target_ref TEXT PRIMARY KEY, last_sequence INTEGER NOT NULL,
			 stop_epoch INTEGER NOT NULL DEFAULT 0)`,
		);
		// The ref's durable stop epoch (§4's stop barrier) rides the sequence
		// row - the same seat the web adapter's sequences store gives it. A
		// database created before the barrier lacks the column, and ALTER
		// TABLE has no IF NOT EXISTS, so check the table's columns first: the
		// additive default-0 column is the whole migration, no data rewrite,
		// and existing rows read as "never stopped".
		const sequenceColumns = this.db.getAllSync<{ name: string }>("PRAGMA table_info(mutation_sequence)");
		if (!sequenceColumns.some((column) => column.name === "stop_epoch")) {
			this.db.execSync("ALTER TABLE mutation_sequence ADD COLUMN stop_epoch INTEGER NOT NULL DEFAULT 0");
		}
		// The record tables' own additive column (the web's fused identity
		// fix, 4059723ab4): a durable row carries its enqueue-time instance so
		// cleanup and Retry can compare the identity the daemon actually
		// fences with (instanceId ?? threadId). A database created before the
		// field existed has no such column, and ALTER TABLE has no IF NOT
		// EXISTS, so check each table's columns first - the nullable column is
		// the whole migration, no data rewrite, and rows written before the
		// field existed read as instanceId-undefined, their identity falling
		// back to the threadId exactly the way the fused identity defines.
		for (const table of Object.values(TABLES)) {
			const columns = this.db.getAllSync<{ name: string }>(`PRAGMA table_info(${table})`);
			if (!columns.some((column) => column.name === "instance_id")) {
				this.db.execSync(`ALTER TABLE ${table} ADD COLUMN instance_id TEXT`);
			}
		}
	}

	async enqueueIntent(intent: MutationIntent<A>, barrier?: MutationStopBarrier): Promise<MutationOutboxRecord<A>> {
		if (!intent.targetRef.trim()) throw new Error("targetRef is required");
		return this.transaction("mutation_outbox_enqueue", () => {
			// §4's stop barrier: a Stop whose cancel transaction committed while
			// this submission was in flight - after the click, before this
			// write - left the ref's durable stop epoch past the click-time
			// capture. The row commits born-"canceled": never dispatched,
			// released only by an explicit Retry.
			const canceledByBarrier =
				barrier !== undefined && this.stopEpochOf(intent.targetRef) > barrier.stopEpoch;
			// Allocate and persist the next sequence in one write so a reentrant
			// enqueue cannot reuse a value read before another allocation.
			const intentSequence = this.allocateSequence(intent.targetRef);
			const clientMutationId = this.#createMutationId();
			this.assertMutationIdAvailable(clientMutationId);
			const record: MutationOutboxRecord<A> = {
				...intent,
				// The dispatcher sends this payload verbatim as the RPC params, and
				// every retry-safe method requires clientMutationId on it for the
				// daemon's own correlation - the record's top-level field is not
				// enough, the daemon never sees that.
				payload: { ...intent.payload, clientMutationId },
				version: 1,
				clientMutationId,
				originClientId: this.#getOwnClientId(),
				intentSequence,
				createdAt: this.#now(),
				state: canceledByBarrier ? "canceled" : "submitting",
				attempted: false,
			};
			this.insertNew(TABLES.outbox, record);
			return record;
		});
	}

	// Stop's combined durable write (the port's enqueueInterruptAndCancel):
	// the ref's cancelable rows turn "canceled" in the same savepoint that
	// enqueues the turn/interrupt record, so the user's click is the cancel
	// moment and both land or neither does. The cancellations are written
	// before the interrupt is added so the scan cannot cancel the interrupt
	// itself, and the same savepoint bumps the ref's stop epoch (§5) - the
	// fence a later enqueue's click-time capture compares against - so the
	// scan and the fence commit as one durable fact.
	async enqueueInterruptAndCancel(intent: MutationIntent<A>): Promise<MutationOutboxRecord<A>> {
		if (!intent.targetRef.trim()) throw new Error("targetRef is required");
		return this.transaction("mutation_outbox_enqueue_interrupt", () => {
			// The rows a Stop may honestly cancel: still waiting ("submitting"
			// or "blockedUnknown") and proven unattempted by the flag the
			// dispatcher's pre-transport write sets. An attempted row may
			// already be on the wire; it stays in-flight/uncertain, not canceled.
			this.db.runSync(
				`UPDATE ${TABLES.outbox} SET state = 'canceled'
				 WHERE target_ref = ? AND state IN ('submitting', 'blockedUnknown') AND attempted = 0`,
				intent.targetRef,
			);
			// One Stop, one bump, inside the Stop's own cancel transaction.
			this.db.runSync(
				`INSERT INTO mutation_sequence (target_ref, last_sequence, stop_epoch) VALUES (?, 0, 1)
				 ON CONFLICT (target_ref) DO UPDATE SET stop_epoch = stop_epoch + 1`,
				intent.targetRef,
			);
			const intentSequence = this.allocateSequence(intent.targetRef);
			const clientMutationId = this.#createMutationId();
			this.assertMutationIdAvailable(clientMutationId);
			const record: MutationOutboxRecord<A> = {
				...intent,
				payload: { ...intent.payload, clientMutationId },
				version: 1,
				clientMutationId,
				originClientId: this.#getOwnClientId(),
				intentSequence,
				createdAt: this.#now(),
				// The Stop's own interrupt passes no barrier: it IS the click
				// the epoch records.
				state: "submitting",
				attempted: false,
			};
			this.insertNew(TABLES.outbox, record);
			return record;
		});
	}

	// The click-time half of §4's stop barrier: what an enqueuing caller reads
	// at the user's click, before the durable write issues. Fresh from the
	// ref's sequence row, never cached - the comparison in enqueueIntent is
	// commit-order, so a capture that never left memory fences nothing.
	async readStopEpoch(targetRef: string): Promise<number> {
		return this.stopEpochOf(targetRef);
	}

	// Commits attempt evidence before transport so another dispatch pass or a
	// reload cannot mistake a possibly-delivered mutation for an unsent intent.
	async markAttempted(clientMutationId: string): Promise<boolean> {
		return (
			changedRows(
				this.db.runSync(
					`UPDATE ${TABLES.outbox} SET attempted = 1 WHERE client_mutation_id = ? AND state = 'submitting'`,
					clientMutationId,
				),
			) > 0
		);
	}

	async markUnknown(
		clientMutationId: string,
		// The literal type is the port's own contract (outbox.ts's
		// MutationOutboxStorage declares exactly "blockedUnknown"): the type
		// system rejects asking the uncertain-outcome write for any other
		// state, the same narrowing the web adapter's 79ecf2839 gave it.
		state: "blockedUnknown",
		options?: { onlyAttempted: boolean },
	): Promise<boolean> {
		// The runtime half of the same guard, for a caller with no types at
		// all (a JS bridge, a deserialized argument): "canceled" is the user's
		// durable decision only an explicit user Retry releases, and
		// "submitting" is the settle/reopen paths' verdict, never this one's,
		// so the request itself is a contract violation - the same loud
		// refusal enqueueIntent gives an empty targetRef.
		if (state !== "blockedUnknown") throw new Error('markUnknown only names "blockedUnknown"');
		return (
			changedRows(
				this.db.runSync(
					// A canceled row is the user's durable decision (only an
					// explicit user Retry releases it); an uncertain-outcome
					// write must not reclassify it into something
					// restoreProvenAbsent could reopen.
					`UPDATE ${TABLES.outbox} SET state = ? WHERE client_mutation_id = ? AND state != 'canceled'
					 AND (? = 0 OR attempted = 1)`,
					state,
					clientMutationId,
					options?.onlyAttempted ? 1 : 0,
				),
			) > 0
		);
	}

	// Moves a receipt-settled record out of whichever table currently holds it
	// (outbox, or - a receipt for an already-transferred record - recovery or
	// optimistic): a pending receipt whose optimisticDisplay still carries
	// composer input becomes (or stays) an accepted optimistic record (the
	// daemon has not reflected it into the thread yet); any other pending
	// receipt (a receipt-only control) or projection state has nothing worth
	// carrying forward, so the record is simply retired everywhere it lives.
	async settleReceipt(clientMutationId: string, projectionState: string): Promise<boolean> {
		return this.transaction("mutation_outbox_settle_receipt", () => {
			const outboxRecord = this.get<MutationOutboxRecord<A>>(TABLES.outbox, clientMutationId);
			const recoveryRecord = this.get<MutationRecoveryRecord<A>>(TABLES.recovery, clientMutationId);
			const optimisticRecord = this.get<MutationOptimisticRecord<A>>(TABLES.optimistic, clientMutationId);
			const source = outboxRecord ?? recoveryRecord ?? optimisticRecord;
			if (!source) return false;
			const display = source.optimisticDisplay;
			const retainsOptimisticDisplay =
				projectionState === "pending" &&
				typeof display === "object" &&
				display !== null &&
				Array.isArray((display as { input?: unknown }).input);
			if (retainsOptimisticDisplay) {
				// Built field-by-field, never spread, so a recovery-sourced receipt
				// (source.recoveryKind/recoveryReason) and attempted evidence do not
				// leak into the optimistic row - the oracle's settleReceipt builds
				// its accepted record the same explicit way.
				const accepted: MutationOptimisticRecord<A> = {
					version: source.version,
					clientMutationId: source.clientMutationId,
					originClientId: source.originClientId,
						// The enqueue-time instance rides the outbox ->
						// optimistic transition like provenance does: dropping it
						// would leave the accepted record identifying itself by
						// threadId alone, exactly the pre-instance shape a
						// replacement that retains the thread id is invisible to.
						instanceId: source.instanceId,
					targetRef: source.targetRef,
					threadId: source.threadId,
					method: source.method,
					payload: source.payload,
					attachments: source.attachments,
					optimisticDisplay: source.optimisticDisplay,
					intentSequence: source.intentSequence,
					createdAt: source.createdAt,
					state: "accepted",
				};
				this.replace(TABLES.optimistic, accepted);
			} else if (optimisticRecord) {
				this.delete(TABLES.optimistic, clientMutationId);
			}
			if (outboxRecord) this.delete(TABLES.outbox, clientMutationId);
			if (recoveryRecord) this.delete(TABLES.recovery, clientMutationId);
			return true;
		});
	}

	// Removes a record the daemon has authoritatively applied, from whichever
	// of the three tables currently holds it.
	async settleApplied(clientMutationId: string): Promise<boolean> {
		return this.transaction("mutation_outbox_settle_applied", () => {
			let removed = false;
			for (const table of Object.values(TABLES)) {
				if (this.delete(table, clientMutationId)) removed = true;
			}
			return removed;
		});
	}

	async transferToRecovery(
		clientMutationId: string,
		recoveryKind: MutationRecoveryKind,
		recoveryReason?: string,
	): Promise<MutationRecoveryRecord<A> | undefined> {
		return this.transaction("mutation_outbox_transfer_recovery", () => {
			const record = this.get<MutationOutboxRecord<A>>(TABLES.outbox, clientMutationId);
			if (!record) return undefined;
			const recovery: MutationRecoveryRecord<A> = { ...record, recoveryKind, recoveryReason };
			this.replace(TABLES.recovery, recovery);
			this.delete(TABLES.outbox, clientMutationId);
			return recovery;
		});
	}

	// Read methods required by MutationOutboxStorage.

	async listTargetRefs(): Promise<string[]> {
		const rows = this.db.getAllSync<{ target_ref: string }>(
			`SELECT DISTINCT target_ref FROM ${TABLES.outbox} UNION SELECT DISTINCT target_ref FROM ${TABLES.optimistic}`,
		);
		return rows.map((row) => row.target_ref).sort();
	}

	async listOutbox(targetRef?: string): Promise<MutationOutboxRecord<A>[]> {
		return this.list<MutationOutboxRecord<A>>(TABLES.outbox, targetRef);
	}

	async getOutbox(clientMutationId: string): Promise<MutationOutboxRecord<A> | undefined> {
		return this.get<MutationOutboxRecord<A>>(TABLES.outbox, clientMutationId);
	}

	async getOptimistic(clientMutationId: string): Promise<MutationOptimisticRecord<A> | undefined> {
		return this.get<MutationOptimisticRecord<A>>(TABLES.optimistic, clientMutationId);
	}

	async listOptimistic(targetRef?: string): Promise<MutationOptimisticRecord<A>[]> {
		return this.list<MutationOptimisticRecord<A>>(TABLES.optimistic, targetRef);
	}

	async getRecovery(clientMutationId: string): Promise<MutationRecoveryRecord<A> | undefined> {
		return this.get<MutationRecoveryRecord<A>>(TABLES.recovery, clientMutationId);
	}

	// Native-only recovery projection reads are always scoped to the composite
	// hub/conversation target. The shared storage port intentionally stays
	// unchanged; callers must provide the exact target key before seeing or
	// deleting a recovered record.
	async listRecovery(targetRef: string): Promise<MutationRecoveryRecord<A>[]> {
		return this.list<MutationRecoveryRecord<A>>(TABLES.recovery, targetRef);
	}

	async discardRecovery(clientMutationId: string, targetRef: string): Promise<boolean> {
		return (
			changedRows(
				this.db.runSync(
					`DELETE FROM ${TABLES.recovery} WHERE client_mutation_id = ? AND target_ref = ?`,
					clientMutationId,
					targetRef,
				),
			) > 0
		);
	}

	// The lowest-sequence submitting outbox record for this target. A
	// canceled row provably never left the client, so it cannot be reordered
	// against the daemon and must not park what follows it - including the
	// interrupt that canceled it. A blockedUnknown head is different: the
	// daemon may already have applied it, so the FIFO stays closed behind it
	// and every later one on the SAME target, never another target's.
	async nextDispatchable(targetRef: string): Promise<MutationOutboxRecord<A> | undefined> {
		for (const record of this.list<MutationOutboxRecord<A>>(TABLES.outbox, targetRef)) {
			if (record.state === "submitting") return record;
			if (record.state === "blockedUnknown") return undefined;
		}
		return undefined;
	}

	// Reopens every blockedUnknown record for this target the authoritative
	// read does NOT name - a missing id is not proof of non-delivery (a
	// bounded transcript can omit older work), so only an id the snapshot
	// explicitly confirms stays settled. Returns the ids restored. Wrapped in
	// the same savepoint as every other compound write here: a throw partway
	// through the loop must not leave some blockedUnknown records reopened
	// and others not.
	async restoreProvenAbsent(targetRef: string, authoritativeIds: ReadonlySet<string>): Promise<string[]> {
		return this.transaction("mutation_outbox_restore_absent", () => {
			const blocked = this.list<MutationOutboxRecord<A>>(TABLES.outbox, targetRef).filter(
				(record) => record.state === "blockedUnknown" && !authoritativeIds.has(record.clientMutationId),
			);
			for (const record of blocked) {
				this.db.runSync(
					`UPDATE ${TABLES.outbox} SET state = 'submitting' WHERE client_mutation_id = ?`,
					record.clientMutationId,
				);
			}
			return blocked.map((record) => record.clientMutationId);
		});
	}

	protected list<T extends MutationRecord<A>>(table: string, targetRef?: string): T[] {
		const rows =
			targetRef === undefined
				? this.db.getAllSync<Row>(`SELECT * FROM ${table} ORDER BY intent_sequence`)
				: this.db.getAllSync<Row>(`SELECT * FROM ${table} WHERE target_ref = ? ORDER BY intent_sequence`, targetRef);
		return rows.map((row) => fromRow<A, T>(row));
	}

	// Shared row plumbing used by the write and read methods above.
	// Allocates and persists the next sequence in one write so a reentrant
	// enqueue cannot reuse a value read before another allocation. The upsert
	// touches only last_sequence, so the ref's stop epoch survives every
	// allocation - the same preservation the web adapter's spread carries.
	protected allocateSequence(targetRef: string): number {
		const sequenceRow = this.db.getFirstSync<{ last_sequence: number }>(
			`INSERT INTO mutation_sequence (target_ref, last_sequence) VALUES (?, 1)
			 ON CONFLICT (target_ref) DO UPDATE SET last_sequence = last_sequence + 1
			 RETURNING last_sequence`,
			targetRef,
		);
		if (!sequenceRow) throw new Error("failed to allocate intent sequence");
		return sequenceRow.last_sequence;
	}

	// The ref's durable stop epoch as the caller's current transaction sees
	// it - a ref with no sequence row yet counts as never stopped.
	protected stopEpochOf(targetRef: string): number {
		const row = this.db.getFirstSync<{ stop_epoch: number }>(
			"SELECT stop_epoch FROM mutation_sequence WHERE target_ref = ?",
			targetRef,
		);
		return row?.stop_epoch ?? 0;
	}

	// A fresh clientMutationId has never been seen before, so a primary-key
	// collision (the id generator repeating, the documented insecure
	// fallback under adversarial conditions) means something is wrong with
	// the id, not that this record should overwrite whatever collided with
	// it - the same rejection the oracle's `add` gives a duplicate key.
	//
	// The uniqueness invariant is cross-store, not table-local (the
	// appwire-client MutationOutboxStorage port's enqueue contract): a
	// generated id must be absent from all three active stores, because the
	// INSERT below only fences the outbox. A record that has already moved to
	// optimistic or recovery still owns its id, and a second active record
	// holding it would let a later settlement keyed on the id retire the older
	// one. Checked before any insert, inside the enclosing savepoint, so a
	// collision rolls the sequence allocation back instead of half-applying;
	// the id is rejected, never silently regenerated.
	protected assertMutationIdAvailable(clientMutationId: string): void {
		for (const table of Object.values(TABLES)) {
			if (this.get(table, clientMutationId)) {
				throw new Error(`clientMutationId is already active in the mutation outbox: ${clientMutationId}`);
			}
		}
	}

	protected insertNew(table: string, record: MutationOutboxRecord<A> | MutationOptimisticRecord<A> | MutationRecoveryRecord<A>): void {
		this.db.runSync(insertSQL(table), ...this.insertValues(record));
	}

	// A transition (a receipt settling, a rejection transferring) writes a
	// record that may already occupy this id in this table - a retry of the
	// same transition, say - and every column must land as given, the same
	// full-row replace the oracle's `put` gives on a colliding key. Refreshing
	// only state/attempted would leave a stale payload/display/recovery
	// reason behind from whatever the row held before.
	protected replace(table: string, record: MutationOutboxRecord<A> | MutationOptimisticRecord<A> | MutationRecoveryRecord<A>): void {
		this.db.runSync(replaceSQL(table), ...this.insertValues(record));
	}

	protected insertValues(
		record: MutationOutboxRecord<A> | MutationOptimisticRecord<A> | MutationRecoveryRecord<A>,
	): (string | number | null)[] {
		const recovery = record as Partial<MutationRecoveryRecord<A>>;
		return [
			record.clientMutationId,
			record.version,
			record.originClientId ?? null,
			record.targetRef,
			record.threadId ?? null,
			record.instanceId ?? null,
			record.method,
			JSON.stringify(record.payload),
			JSON.stringify(record.attachments),
			JSON.stringify(record.optimisticDisplay ?? null),
			record.composerText ?? null,
			record.intentSequence,
			record.createdAt,
			record.state,
			"attempted" in record && record.attempted ? 1 : 0,
			recovery.recoveryKind ?? null,
			recovery.recoveryReason ?? null,
		];
	}

	protected get<T extends MutationRecord<A>>(table: string, clientMutationId: string): T | undefined {
		const row = this.db.getFirstSync<Row>(`SELECT * FROM ${table} WHERE client_mutation_id = ?`, clientMutationId);
		return row ? fromRow<A, T>(row) : undefined;
	}

	protected delete(table: string, clientMutationId: string): boolean {
		return changedRows(this.db.runSync(`DELETE FROM ${table} WHERE client_mutation_id = ?`, clientMutationId)) > 0;
	}

	// A compound operation (sequence allocation plus an insert, a multi-table
	// settlement, a recovery handoff) runs in one SQLite savepoint so a throw
	// partway through rolls back every statement already run - the one shared
	// helper draftRepository and creationDraftRepository also write through.
	protected transaction<T>(name: string, body: () => T): T {
		return withSavepoint(this.db, name, body);
	}
}
