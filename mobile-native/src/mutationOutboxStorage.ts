// D25d-1a: the write half of the phone's implementation of the package's
// MutationOutboxStorage port (appwire-client/typescript/state/mutation/
// outbox.ts) - the 13 calls the outbox's discovery and the dispatcher make -
// over expo-sqlite, the same storage draftRepository.ts already persists
// drafts through. No host global is named in the package; this adapter is
// where the sqlite handle lives.
//
// Stacked on this: D25d-1b adds the read half (getOutbox, getOptimistic,
// listOptimistic, getRecovery, nextDispatchable, listTargetRefs,
// restoreProvenAbsent) to this same class, which is what makes it satisfy
// the full port. Until then this class is real, tested and unused - dead
// code by construction, not a WIP method left unfinished.
//
// Oracle: cmd/evener-hub/frontend/src/stores/mutationOutbox.test.ts's
// describe("MutationOutboxIndexedDB", ...) block. This mirrors its
// write-side contracts (gap-free per-target sequencing, settleReceipt's
// pending-input-carrying-record-becomes-optimistic rule, markUnknown's
// onlyAttempted guard) without the web's Blob handling, cross-tab identity
// or shared-notes recovery-superseding, none of which the port declares.
import * as Crypto from "expo-crypto";
import type {
	MutationAttachmentRef,
	MutationIntent,
	MutationOptimisticRecord,
	MutationOutboxRecord,
	MutationOutboxState,
	MutationRecord,
	MutationRecoveryKind,
	MutationRecoveryRecord,
	SecureRandomSource,
} from "@evener/appwire-client/state/mutation";
import { createSecureUUID } from "@evener/appwire-client/state/mutation";

// This app's SecureRandomSource: expo-crypto's synchronous randomUUID and
// getRandomValues, the native module every other id-generating call site in
// this app already uses (credentialStore.ts, nativeOrganization.ts) instead
// of a bare Web Crypto global React Native does not guarantee.
function nativeRandomSource(): SecureRandomSource {
	return { randomUUID: Crypto.randomUUID, getRandomValues: Crypto.getRandomValues };
}

// The affected-row count expo-sqlite's runSync and node:sqlite's run() both
// report (sqlite3_changes64()), used to decide a write's boolean result from
// the statement itself instead of a preceding SELECT.
export interface MutationOutboxRunResult {
	changes: number | bigint;
}

function changedRows(result: MutationOutboxRunResult): number {
	return typeof result.changes === "bigint" ? Number(result.changes) : result.changes;
}

// The synchronous SQLite surface this adapter needs: expo-sqlite's
// openDatabaseSync in production, node:sqlite's DatabaseSync in tests
// (mirroring draftRepository.ts's DraftDatabase port), extended with
// getAllSync for the multi-row scans D25d-1b's read methods need.
export interface MutationOutboxDatabase {
	execSync(sql: string): void;
	runSync(sql: string, ...params: (string | number | null)[]): MutationOutboxRunResult;
	getFirstSync<T>(sql: string, ...params: (string | number)[]): T | null;
	getAllSync<T>(sql: string, ...params: (string | number)[]): T[];
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

export interface Row {
	client_mutation_id: string;
	version: number;
	origin_client_id: string | null;
	target_ref: string;
	thread_id: string | null;
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
	return {
		version: row.version as 1,
		clientMutationId: row.client_mutation_id,
		originClientId: row.origin_client_id ?? undefined,
		targetRef: row.target_ref,
		threadId: row.thread_id ?? undefined,
		method: row.method,
		payload: JSON.parse(row.payload),
		attachments: JSON.parse(row.attachments),
		optimisticDisplay: JSON.parse(row.optimistic_display),
		composerText: row.composer_text ?? undefined,
		intentSequence: row.intent_sequence,
		createdAt: row.created_at,
		state: row.state as MutationOutboxState,
		attempted: row.attempted === 1,
		...(row.recovery_kind ? { recoveryKind: row.recovery_kind, recoveryReason: row.recovery_reason ?? undefined } : {}),
	} as unknown as T;
}

// MutationOutboxSQLite implements the package's MutationOutboxStorage port
// over one synchronous database handle, wrapped in resolved promises
// (outbox.ts's own comment: "a host with a synchronous store returns
// resolved promises").
export class MutationOutboxSQLite<A extends MutationAttachmentRef = MutationAttachmentRef> {
	readonly db: MutationOutboxDatabase;
	readonly #createMutationId: () => string;
	readonly #now: () => number;
	readonly #getOwnClientId: () => string | undefined;

	constructor(db: MutationOutboxDatabase, options: MutationOutboxSQLiteOptions = {}) {
		this.db = db;
		const randomSource = options.randomSource ?? nativeRandomSource();
		this.#createMutationId = options.createMutationId ?? (() => createSecureUUID(randomSource));
		this.#now = options.now ?? Date.now;
		this.#getOwnClientId = options.getOwnClientId ?? (() => undefined);
		const schema = (table: string) => `CREATE TABLE IF NOT EXISTS ${table} (
			client_mutation_id TEXT PRIMARY KEY, version INTEGER NOT NULL,
			origin_client_id TEXT, target_ref TEXT NOT NULL, thread_id TEXT, method TEXT NOT NULL,
			payload TEXT NOT NULL, attachments TEXT NOT NULL, optimistic_display TEXT NOT NULL,
			composer_text TEXT, intent_sequence INTEGER NOT NULL, created_at INTEGER NOT NULL,
			state TEXT NOT NULL, attempted INTEGER NOT NULL DEFAULT 0,
			recovery_kind TEXT, recovery_reason TEXT)`;
		for (const table of Object.values(TABLES)) this.db.execSync(schema(table));
		this.db.execSync(
			"CREATE TABLE IF NOT EXISTS mutation_sequence (target_ref TEXT PRIMARY KEY, last_sequence INTEGER NOT NULL)",
		);
	}

	async enqueueIntent(intent: MutationIntent<A>): Promise<MutationOutboxRecord<A>> {
		if (!intent.targetRef.trim()) throw new Error("targetRef is required");
		return this.transaction("mutation_outbox_enqueue", () => {
			const sequenceRow = this.db.getFirstSync<{ last_sequence: number }>(
				"SELECT last_sequence FROM mutation_sequence WHERE target_ref = ?",
				intent.targetRef,
			);
			const intentSequence = (sequenceRow?.last_sequence ?? 0) + 1;
			this.db.runSync(
				`INSERT INTO mutation_sequence (target_ref, last_sequence) VALUES (?, ?)
				 ON CONFLICT (target_ref) DO UPDATE SET last_sequence = excluded.last_sequence`,
				intent.targetRef,
				intentSequence,
			);
			const clientMutationId = this.#createMutationId();
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
				state: "submitting",
				attempted: false,
			};
			this.insert(TABLES.outbox, record);
			return record;
		});
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
		state: MutationOutboxState,
		options?: { onlyAttempted: boolean },
	): Promise<boolean> {
		return (
			changedRows(
				this.db.runSync(
					`UPDATE ${TABLES.outbox} SET state = ? WHERE client_mutation_id = ? AND (? = 0 OR attempted = 1)`,
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
				this.insert(TABLES.optimistic, { ...source, state: "accepted" } satisfies MutationOptimisticRecord<A>);
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
			this.insert(TABLES.recovery, recovery);
			this.delete(TABLES.outbox, clientMutationId);
			return recovery;
		});
	}

	// Below: shared plumbing D25d-1b's read methods reuse rather than
	// re-deriving their own row (de)serialization. Not private (`#`) because
	// 1b's methods are added to this same class in a later commit, not a
	// subclass - visible only within this module either way, since neither is
	// exported past the class itself.
	protected insert(table: string, record: MutationOutboxRecord<A> | MutationOptimisticRecord<A> | MutationRecoveryRecord<A>): void {
		const recovery = record as Partial<MutationRecoveryRecord<A>>;
		this.db.runSync(
			`INSERT INTO ${table} (client_mutation_id, version, origin_client_id, target_ref, thread_id, method, payload,
				attachments, optimistic_display, composer_text, intent_sequence, created_at, state, attempted,
				recovery_kind, recovery_reason)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (client_mutation_id) DO UPDATE SET state = excluded.state, attempted = excluded.attempted`,
			record.clientMutationId,
			record.version,
			record.originClientId ?? null,
			record.targetRef,
			record.threadId ?? null,
			record.method,
			JSON.stringify(record.payload),
			JSON.stringify(record.attachments),
			JSON.stringify(record.optimisticDisplay),
			record.composerText ?? null,
			record.intentSequence,
			record.createdAt,
			"state" in record ? record.state : "accepted",
			"attempted" in record && record.attempted ? 1 : 0,
			recovery.recoveryKind ?? null,
			recovery.recoveryReason ?? null,
		);
	}

	protected get<T extends MutationRecord<A>>(table: string, clientMutationId: string): T | undefined {
		const row = this.db.getFirstSync<Row>(`SELECT * FROM ${table} WHERE client_mutation_id = ?`, clientMutationId);
		return row ? fromRow<A, T>(row) : undefined;
	}

	protected delete(table: string, clientMutationId: string): boolean {
		return changedRows(this.db.runSync(`DELETE FROM ${table} WHERE client_mutation_id = ?`, clientMutationId)) > 0;
	}

	// Wraps a compound operation (sequence allocation plus an insert, a
	// multi-table settlement, a recovery handoff) in one SQLite savepoint so a
	// throw partway through rolls back every statement already run - the same
	// SAVEPOINT/ROLLBACK TO/RELEASE pattern draftRepository.ts's write() uses
	// for its own compound writes over this same synchronous database port.
	protected transaction<T>(name: string, body: () => T): T {
		this.db.execSync(`SAVEPOINT ${name}`);
		try {
			const result = body();
			this.db.execSync(`RELEASE ${name}`);
			return result;
		} catch (error) {
			this.db.execSync(`ROLLBACK TO ${name}; RELEASE ${name}`);
			throw error;
		}
	}
}
