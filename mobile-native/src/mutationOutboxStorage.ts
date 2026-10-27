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
// what the authoritative snapshot omits, and the settlement note-supersede
// discardSupersededNoteRecovery runs inside both settleReceipt and
// settleApplied) without the web's Blob handling or cross-tab identity,
// neither of which the port declares.
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
	QueuedMessageStorage,
	SecureRandomSource,
} from "@evener/appwire-client/state/mutation";
import {
	acceptedRecord,
	canEditQueuedMessage,
	canRemoveQueuedMessage,
	createSecureUUID,
	matchesQueuedMessageSnapshot,
	PENDING_TURN_METHODS,
	retainsAcceptedCopy,
	settlementRetiresRecovery,
} from "@evener/appwire-client/state/mutation";
import { type SqliteSync, type SqliteSyncRunResult, withSavepoint } from "./sqliteSync";
import { decodeDraftEndpoint } from "./draftHandoffLedger";
import { DraftHandoffLedgerSQLite } from "./draftHandoffLedgerSQLite";

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

export interface RecoveryResendCommit {
	targetRef: string;
	sourceIds: readonly string[];
	clientMutationId: string;
}

// The one row shape all three tables share: a record's current table IS its
// state (submitting/blockedUnknown -> outbox, accepted -> optimistic,
// rejected/orphaned -> recovery), so no separate state machine duplicates
// what the table membership already says.
export const TABLES = {
	outbox: "mutation_outbox",
	optimistic: "mutation_optimistic",
	recovery: "mutation_recovery",
} as const;

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
	"composer_mentions",
	"draft_capture_token",
	"intent_sequence",
	"created_at",
	"state",
	"attempted",
	"stop_epoch_at_enqueue",
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
	composer_mentions: string | null;
	draft_capture_token: string | null;
	intent_sequence: number;
	created_at: number;
	state: string;
	attempted: number;
	stop_epoch_at_enqueue: number;
	unresolved_attempts: number;
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
		composerMentions: row.composer_mentions == null ? undefined : JSON.parse(row.composer_mentions),
		draftCaptureToken: row.draft_capture_token ?? undefined,
		intentSequence: row.intent_sequence,
		createdAt: row.created_at,
		state: row.state as MutationOutboxState,
		...(row.state === "accepted"
			? {}
			: {
					attempted: row.attempted === 1,
					unresolvedAttempts: row.unresolved_attempts,
					stopEpochAtEnqueue: row.stop_epoch_at_enqueue,
				}),
		...(row.recovery_kind ? { recoveryKind: row.recovery_kind, recoveryReason: row.recovery_reason ?? undefined } : {}),
	} as unknown as T;
}

// MutationOutboxSQLite implements the package's MutationOutboxStorage port
// over one synchronous database handle, wrapped in resolved promises
// (outbox.ts's own comment: "a host with a synchronous store returns
// resolved promises").
export class MutationOutboxSQLite<A extends MutationAttachmentRef = MutationAttachmentRef>
	implements MutationOutboxStorage<A>, QueuedMessageStorage<A>
{
	readonly db: SqliteSync;
	/** The durable identity-resume handoff graph. It shares this database
	 * because retarget records an edge in the same savepoint that moves the
	 * mutation rows to the resolved identity. */
	readonly handoffLedger: DraftHandoffLedgerSQLite;
	readonly #createMutationId: () => string;
	readonly #now: () => number;
	readonly #getOwnClientId: () => string | undefined;

	constructor(db: SqliteSync, options: MutationOutboxSQLiteOptions = {}) {
		this.db = db;
		const randomSource = options.randomSource ?? nativeRandomSource();
		this.#createMutationId = options.createMutationId ?? (() => createSecureUUID(randomSource));
		this.#now = options.now ?? Date.now;
		this.#getOwnClientId = options.getOwnClientId ?? (() => undefined);
		this.handoffLedger = new DraftHandoffLedgerSQLite(db, {
			now: this.#now,
			createId: () => createSecureUUID(randomSource),
		});
		const schema = (table: string) => `CREATE TABLE IF NOT EXISTS ${table} (
			client_mutation_id TEXT PRIMARY KEY, version INTEGER NOT NULL,
			origin_client_id TEXT, target_ref TEXT NOT NULL, thread_id TEXT, instance_id TEXT, method TEXT NOT NULL,
			payload TEXT NOT NULL, attachments TEXT NOT NULL, optimistic_display TEXT NOT NULL,
			composer_text TEXT, composer_mentions TEXT, draft_capture_token TEXT, intent_sequence INTEGER NOT NULL, created_at INTEGER NOT NULL,
			state TEXT NOT NULL, attempted INTEGER NOT NULL DEFAULT 0,
			unresolved_attempts INTEGER NOT NULL DEFAULT 0,
			stop_epoch_at_enqueue INTEGER NOT NULL DEFAULT 0,
			accepted_at INTEGER NOT NULL DEFAULT 0,
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
			 stop_epoch INTEGER NOT NULL DEFAULT 0, recovery_resends TEXT NOT NULL DEFAULT '[]')`,
		);
		// Each additive column below is the whole migration for databases from
		// before it existed: ALTER TABLE has no IF NOT EXISTS, so
		// addColumnIfMissing checks the table's columns first, and rows written
		// earlier read as the column's default.
		//
		// The ref's durable stop epoch (§4's stop barrier) rides the sequence
		// row - the same seat the web adapter's sequences store gives it;
		// existing rows read as "never stopped".
		this.addColumnIfMissing("mutation_sequence", "stop_epoch", "INTEGER NOT NULL DEFAULT 0");
		this.addColumnIfMissing("mutation_sequence", "recovery_resends", "TEXT NOT NULL DEFAULT '[]'");
		for (const table of Object.values(TABLES)) {
			// The enqueue-time instance (the web's fused identity fix,
			// 4059723ab4): cleanup and Retry compare the identity the daemon
			// actually fences with (instanceId ?? threadId). Rows written before
			// it read as instanceId-undefined, falling back to the threadId.
			this.addColumnIfMissing(table, "instance_id", "TEXT");
			this.addColumnIfMissing(table, "composer_mentions", "TEXT");
			this.addColumnIfMissing(table, "draft_capture_token", "TEXT");
			// #4342: the ref's stop epoch at enqueue time - the barrier's own
			// read, stored, so a refusal can fence its rollback: a Stop whose
			// cancel scan skipped the attempted row must not be outrun by the
			// refusal's redelivery. Part of the shared insert shape, so every
			// record table needs it. Rows written before it read as
			// captured-at-zero, the conservative direction.
			this.addColumnIfMissing(table, "stop_epoch_at_enqueue", "INTEGER NOT NULL DEFAULT 0");
			// When a record was accepted. The prune asks "accepted before this
			// read began", which enqueue time cannot answer: a replacement
			// client's read can be out when an earlier request's receipt lands.
			// A row written before the column existed is backfilled from its
			// enqueue time (it was accepted before the upgrade, and leaving it
			// at 0 would exclude exactly the records the prune exists to heal).
			// The ALTER and its backfill commit together: a process that died
			// between them would leave the column present and the rows
			// unstamped, and no later start would retry the backfill.
			this.addColumnIfMissing(
				table,
				"accepted_at",
				"INTEGER NOT NULL DEFAULT 0",
				`UPDATE ${table} SET accepted_at = created_at WHERE state = 'accepted'`,
			);
		}
		// #4342: unresolved attempt counts live beside the attempted flag on
		// the outbox table; rows written before it read as zero unresolved
		// attempts.
		this.addColumnIfMissing(TABLES.outbox, "unresolved_attempts", "INTEGER NOT NULL DEFAULT 0");
	}

	// Adds `column` to `table` when a database from before it existed lacks it.
	// A backfill statement commits together with the ALTER.
	private addColumnIfMissing(table: string, column: string, definition: string, backfill?: string): void {
		const columns = this.db.getAllSync<{ name: string }>(`PRAGMA table_info(${table})`);
		if (columns.some((existing) => existing.name === column)) return;
		withSavepoint(this.db, `mutation_add_${table}_${column}`, () => {
			this.db.execSync(`ALTER TABLE ${table} ADD COLUMN ${column} ${definition}`);
			if (backfill) this.db.execSync(backfill);
		});
	}

	async enqueueIntent(intent: MutationIntent<A>, barrier?: MutationStopBarrier): Promise<MutationOutboxRecord<A>> {
		if (!intent.targetRef.trim()) throw new Error("targetRef is required");
		return this.transaction("mutation_outbox_enqueue", () => this.enqueueRecord(intent, barrier));
	}

	// Synchronous body shared by enqueue and recovery resend. Its caller owns
	// the savepoint, so no async continuation can escape the transaction.
	private enqueueRecord(intent: MutationIntent<A>, barrier?: MutationStopBarrier): MutationOutboxRecord<A> {
		// §4's stop barrier: a Stop whose cancel transaction committed while
		// this submission was in flight - after the click, before this
		// write - left the ref's durable stop epoch past the click-time
		// capture. The row commits born-"canceled": never dispatched,
		// released only by an explicit Retry.
		const stopEpoch = this.stopEpochOf(intent.targetRef);
		const canceledByBarrier = barrier !== undefined && stopEpoch > barrier.stopEpoch;
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
			stopEpochAtEnqueue: stopEpoch,
			attempted: false,
		};
		this.insertNew(TABLES.outbox, record);
		return record;
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
			// The Stop's own interrupt passes no barrier - it IS the click the
			// epoch records - so its capture is the epoch its Stop just bumped:
			// a refusal of the interrupt itself redelivers like any transient
			// refusal instead of fence-canceling the Stop's own row.
			const stopEpoch = this.stopEpochOf(intent.targetRef);
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
				stopEpochAtEnqueue: stopEpoch,
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
		this.#seedLegacyAttemptCount(clientMutationId);
		// A row from before the capture column existed reads the migration's
		// DEFAULT 0. Its first mark proves by survival that no Stop's cancel
		// scan has run since its birth - any bump would have canceled it
		// while unattempted - so the epoch as it stands IS the epoch it was
		// born at: initialize the capture from the sequence here, and the
		// refusal's fence compares against the birth epoch instead of a
		// zero a historical Stop trips. A modern row born at epoch zero is
		// written back unchanged.
		this.db.runSync(
			`UPDATE ${TABLES.outbox}
			 SET stop_epoch_at_enqueue = COALESCE((SELECT stop_epoch FROM mutation_sequence WHERE target_ref = ${TABLES.outbox}.target_ref), 0)
			 WHERE client_mutation_id = ? AND state = 'submitting' AND stop_epoch_at_enqueue = 0`,
			clientMutationId,
		);
		return (
			changedRows(
				this.db.runSync(
					`UPDATE ${TABLES.outbox} SET attempted = 1, unresolved_attempts = unresolved_attempts + 1
						WHERE client_mutation_id = ? AND state = 'submitting'`,
					clientMutationId,
				),
			) > 0
		);
	}

	// Rolls the pre-transport attempt evidence back after the hub
	// authoritatively refused the attempt in band (#4342's
	// connectionBackpressure): the row is what it was before the dispatch,
	// so the reconnect reconcile's onlyAttempted-guarded blockedUnknown park
	// skips it and the ordinary flush rediscovers it - unless a Stop
	// interrupted the attempt: the refusal proves the attempt never landed,
	// and §4's conservative direction holds the row canceled (released only
	// by an explicit Retry) instead of returning it dispatchable ahead of
	// the interrupt the Stop already queued. Authoritative outcomes own
	// every other state, so only submitting and blockedUnknown rows roll
	// back.
	async unmarkAttempted(clientMutationId: string): Promise<boolean> {
		this.#seedLegacyAttemptCount(clientMutationId);
		const row = this.db.getFirstSync<{
			state: string;
			unresolved_attempts: number;
			target_ref: string;
			stop_epoch_at_enqueue: number;
		}>(
			`SELECT state, unresolved_attempts, target_ref, stop_epoch_at_enqueue FROM ${TABLES.outbox} WHERE client_mutation_id = ?`,
			clientMutationId,
		);
		if (row?.state === "blockedUnknown" && row.unresolved_attempts > 1) {
			this.db.runSync(
				`UPDATE ${TABLES.outbox} SET unresolved_attempts = unresolved_attempts - 1
					WHERE client_mutation_id = ? AND state = 'blockedUnknown'`,
				clientMutationId,
			);
			return false;
		}
		// A row parked while its only attempt was in flight reopens: the
		// in-band refusal is authoritative proof that park's uncertainty
		// resolves - unless a Stop interrupted the attempt, in which case the
		// fence holds it canceled.
		if (row?.state === "blockedUnknown") {
			const stopInterrupted = this.stopEpochOf(row.target_ref) > row.stop_epoch_at_enqueue;
			this.db.runSync(
				`UPDATE ${TABLES.outbox} SET state = ?, attempted = 0, unresolved_attempts = 0
					WHERE client_mutation_id = ? AND state = 'blockedUnknown'`,
				stopInterrupted ? "canceled" : "submitting",
				clientMutationId,
			);
			return !stopInterrupted;
		}
		if (row?.state === "submitting") {
			const unresolvedAttempts = Math.max(0, row.unresolved_attempts - 1);
			if (unresolvedAttempts === 0 && this.stopEpochOf(row.target_ref) > row.stop_epoch_at_enqueue) {
				this.db.runSync(
					`UPDATE ${TABLES.outbox} SET state = 'canceled', attempted = 0, unresolved_attempts = 0
						WHERE client_mutation_id = ? AND state = 'submitting'`,
					clientMutationId,
				);
				return false;
			}
			this.db.runSync(
				`UPDATE ${TABLES.outbox} SET attempted = ?, unresolved_attempts = ?
					WHERE client_mutation_id = ? AND state = 'submitting'`,
				unresolvedAttempts > 0 ? 1 : 0,
				unresolvedAttempts,
				clientMutationId,
			);
			return unresolvedAttempts === 0;
		}
		return false;
	}

	// A row written before the unresolved_attempts column existed carries its
	// evidence as the bare attempted flag over a zero count. Both count
	// writes seed that legacy shape first, so the refused retry's rollback
	// decrements real evidence instead of erasing it. Post-field rows never
	// hold the (attempted, zero-count) shape, so the seed is idempotent.
	#seedLegacyAttemptCount(clientMutationId: string): void {
		this.db.runSync(
			`UPDATE ${TABLES.outbox} SET unresolved_attempts = 1
				WHERE client_mutation_id = ? AND attempted = 1 AND unresolved_attempts = 0`,
			clientMutationId,
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
	// composer input, or a note save, becomes (or stays) an accepted optimistic
	// record (the daemon has not reflected it yet); any other pending receipt
	// (a receipt-only control) or projection state has nothing worth carrying
	// forward, so the record is simply retired everywhere it lives.
	async settleReceipt(clientMutationId: string, projectionState: string): Promise<boolean> {
		return this.transaction("mutation_outbox_settle_receipt", () => {
			const outboxRecord = this.get<MutationOutboxRecord<A>>(TABLES.outbox, clientMutationId);
			const recoveryRecord = this.get<MutationRecoveryRecord<A>>(TABLES.recovery, clientMutationId);
			const optimisticRecord = this.get<MutationOptimisticRecord<A>>(TABLES.optimistic, clientMutationId);
			const source = outboxRecord ?? recoveryRecord ?? optimisticRecord;
			if (!source) return false;
			this.discardSupersededNoteRecovery(source);
			if (retainsAcceptedCopy(source, projectionState)) {
				// The package's acceptedRecord is the one whitelist both adapters
				// build: it never spreads, so a recovery-sourced receipt
				// (source.recoveryKind/recoveryReason) and attempted evidence do
				// not leak into the optimistic row.
				this.replace(TABLES.optimistic, acceptedRecord(source));
				// Acceptance is its own moment. A read already in flight when this
				// receipt lands never saw the record, so its absence must not retire
				// it; the prune compares against this stamp, never enqueue time.
				this.db.runSync(
					`UPDATE ${TABLES.optimistic} SET accepted_at = ? WHERE client_mutation_id = ?`,
					this.#now(),
					clientMutationId,
				);
			} else if (optimisticRecord) {
				this.delete(TABLES.optimistic, clientMutationId);
			}
			if (outboxRecord) this.delete(TABLES.outbox, clientMutationId);
			if (recoveryRecord && settlementRetiresRecovery(recoveryRecord)) this.delete(TABLES.recovery, clientMutationId);
			return true;
		});
	}

	// Removes a record the daemon has authoritatively applied, from whichever
	// of the three tables currently holds it.
	async settleApplied(clientMutationId: string): Promise<boolean> {
		return this.transaction("mutation_outbox_settle_applied", () => {
			const outboxRecord = this.get<MutationOutboxRecord<A>>(TABLES.outbox, clientMutationId);
			const optimisticRecord = this.get<MutationOptimisticRecord<A>>(TABLES.optimistic, clientMutationId);
			const recoveryRecord = this.get<MutationRecoveryRecord<A>>(TABLES.recovery, clientMutationId);
			const source = outboxRecord ?? optimisticRecord ?? recoveryRecord;
			if (!source) return false;
			this.discardSupersededNoteRecovery(source);
			let removed = false;
			for (const table of Object.values(TABLES)) {
				if (this.delete(table, clientMutationId)) removed = true;
			}
			return removed;
		});
	}

	// The shared settlement contract's note supersede, run inside the same
	// settlement transaction (the web adapter's #discardSupersededNoteRecovery):
	// a settled notes/human/set supersedes the SAME ref's earlier
	// notes/human/set recovery rows whatever their recoveryKind - a later save
	// is the note editor's only retry - and nothing else. Every other method,
	// every other target, and every newer row (including a note saved after
	// this one) stays; the port declares no separate discard method for it,
	// because it is the settlement's own side effect.
	protected discardSupersededNoteRecovery(source: MutationRecord<A>): void {
		if (source.method !== "notes/human/set") return;
		this.db.runSync(
			`DELETE FROM ${TABLES.recovery}
			 WHERE method = 'notes/human/set' AND target_ref = ? AND intent_sequence < ?`,
			source.targetRef,
			source.intentSequence,
		);
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

	/** Moves every waiting row from one ref to the ref a resume returned, in
	 * one savepoint, keeping each clientMutationId (the daemon's reconciliation
	 * keys on it) and rewriting the thread/instance fence the row dispatches
	 * with. Returns the ids that moved. A resume that returns a new identity
	 * makes the old ref useless: a row left there would dispatch against an
	 * instance the daemon no longer serves. */
	retarget(ref: string, to: { ref: string; wireRef: string; threadId?: string; instanceId?: string }): string[] {
		if (!to.ref.trim()) throw new Error("retarget ref is required");
		if (ref === to.ref) return [];
		return this.transaction("mutation_outbox_retarget", () => {
			// The identity changed whether or not a row moved: the draft handoff
			// obligation is recorded in this same savepoint, so a resume of a
			// session with no parked rows still has a durable edge to settle.
			const fromEndpoint = decodeDraftEndpoint(ref);
			const toEndpoint = decodeDraftEndpoint(to.ref);
			if (fromEndpoint && toEndpoint) this.handoffLedger.recordIdentityChange(fromEndpoint, toEndpoint);
			// Source order matters: the moved rows keep their relative order at the
			// destination, so the FIFO the daemon saw is the FIFO it keeps seeing.
			const rows = this.db.getAllSync<{ client_mutation_id: string; payload: string }>(
				`SELECT client_mutation_id, payload FROM ${TABLES.outbox}
				 WHERE target_ref = ? AND state IN ('submitting', 'blockedUnknown')
				 ORDER BY intent_sequence`,
				ref,
			);
			if (rows.length === 0) return [];
			const ids = rows.map((row) => row.client_mutation_id);
			// A recovery resend's receipt is the draft owner's proof that its
			// captured sources were consumed. Its source rows move below, so the
			// receipt must move with them in this same savepoint: a receipt left
			// on the retired ref can never be read by the resumed draft, and the
			// moved sources could never reconcile.
			const sourceReceipts = this.recoveryResendsOf(ref);
			if (sourceReceipts.length) {
				const destinationReceipts = this.recoveryResendsOf(to.ref);
				const merged = new Map(destinationReceipts.map((receipt) => [receipt.clientMutationId, receipt]));
				for (const receipt of sourceReceipts)
					if (!merged.has(receipt.clientMutationId))
						merged.set(receipt.clientMutationId, { ...receipt, targetRef: to.ref });
				this.writeRecoveryResends(to.ref, [...merged.values()]);
				this.writeRecoveryResends(ref, []);
			}
			// Each row's target and sequence move in ONE write. Moving the target
			// first would keep the source sequence, and both refs can already hold
			// that number: the unique (target_ref, intent_sequence) index would
			// reject the move and roll the whole retarget back.
			//
			// Only the rows this move knows how to finish take part. A canceled row
			// left behind is harmless - it can never dispatch - while one carried
			// over without the rewrite below would reopen under the new target still
			// naming the retired identity.
			//
			// The optimistic table has the same unique index and the same risk, so
			// it moves the same way.
			//
			// The OUTBOX move is restricted to `ids` - exactly the rows this method
			// rewrites below. Moving a canceled row here would give it the new target
			// while its payload still named the retired identity, and its "Send now"
			// action would release a request addressing a session that no longer
			// exists. A canceled row left on the old ref can never dispatch, which is
			// the safe resting place for it.
			const destinationEpoch = this.stopEpochOf(to.ref);
			const optimistic = this.db.getAllSync<{ client_mutation_id: string }>(
				`SELECT client_mutation_id FROM ${TABLES.optimistic} WHERE target_ref = ? ORDER BY intent_sequence`,
				ref,
			);
			for (const row of optimistic) {
				this.db.runSync(
					`UPDATE ${TABLES.optimistic} SET target_ref = ?, intent_sequence = ? WHERE client_mutation_id = ?`,
					to.ref,
					this.allocateSequence(to.ref),
					row.client_mutation_id,
				);
			}
			// Recovery rows carry no unique sequence index, but they do carry the same
			// wire identity as the outbox rows they came from: a resumed screen looks
			// one up by ref, thread and instance, and an interrupted Edit whose columns
			// still name the retired identity is discarded as a lost capture. Move the
			// routing fields with the target while leaving composition and ownership
			// untouched.
			const recoveryRows = this.db.getAllSync<{ client_mutation_id: string; payload: string }>(
				`SELECT client_mutation_id, payload FROM ${TABLES.recovery} WHERE target_ref = ?`,
				ref,
			);
			// The destination's stop counter is its own. A row carrying the source's
			// epoch would be compared against an unrelated counter - falsely canceled
			// by a lower one, or un-fenced by a higher one. Starting at the
			// destination's current epoch (read above, before any move) keeps every
			// existing stop effective.
			// A resume that names no instance still changed identity: the daemon's
			// fence token is then the thread id, the same fused rule every other
			// caller derives it by.
			const fence = to.instanceId ?? to.threadId;
			for (const row of recoveryRows) {
				const payload = JSON.parse(row.payload) as Record<string, unknown>;
				if (typeof payload.ref === "string") payload.ref = to.wireRef;
				if (fence !== undefined) payload.expectedInstanceId = fence;
				this.db.runSync(
					`UPDATE ${TABLES.recovery}
						 SET target_ref = ?, payload = ?, thread_id = COALESCE(?, thread_id),
						     instance_id = COALESCE(?, instance_id)
						 WHERE client_mutation_id = ?`,
					to.ref,
					JSON.stringify(payload),
					to.threadId ?? null,
					fence ?? null,
					row.client_mutation_id,
				);
			}
			for (const row of rows) {
				// The dispatcher sends the payload verbatim as the RPC params, so the
				// row's own wire ref and instance fence must move with it: a payload
				// still naming the old identity is refused as stale however the
				// columns read. Sequences are allocated per destination so a later
				// send cannot take a lower number and overtake a moved row.
				const payload = JSON.parse(row.payload) as Record<string, unknown>;
				if (typeof payload.ref === "string") payload.ref = to.wireRef;
				if (fence !== undefined) payload.expectedInstanceId = fence;
				this.db.runSync(
					`UPDATE ${TABLES.outbox}
					 SET target_ref = ?, payload = ?, intent_sequence = ?, stop_epoch_at_enqueue = ?,
					     thread_id = COALESCE(?, thread_id), instance_id = COALESCE(?, instance_id)
					 WHERE client_mutation_id = ?`,
					to.ref,
					JSON.stringify(payload),
					this.allocateSequence(to.ref),
					destinationEpoch,
					to.threadId ?? null,
					fence ?? null,
					row.client_mutation_id,
				);
			}
			return ids;
		});
	}

	async listTargetRefs(): Promise<string[]> {
		// UNION (not UNION ALL) already removes duplicates, so a DISTINCT under
		// each arm would only repeat what the merge already guarantees. The
		// ordering stays in JS: the web adapter sorts by the default UTF-16
		// comparison, which a SQL ORDER BY's BINARY collation does not always
		// match for non-ASCII refs, and the two hosts must agree exactly.
		const rows = this.db.getAllSync<{ target_ref: string }>(
			`SELECT target_ref FROM ${TABLES.outbox} UNION SELECT target_ref FROM ${TABLES.optimistic}`,
		);
		return rows.map((row) => row.target_ref).sort();
	}

	async listOutbox(targetRef?: string): Promise<MutationOutboxRecord<A>[]> {
		return this.list<MutationOutboxRecord<A>>(TABLES.outbox, targetRef);
	}

	async getOutbox(clientMutationId: string): Promise<MutationOutboxRecord<A> | undefined> {
		return this.get<MutationOutboxRecord<A>>(TABLES.outbox, clientMutationId);
	}

	async getOutboxWithStopEpoch(
		clientMutationId: string,
	): Promise<{ record: MutationOutboxRecord<A> | undefined; stopEpoch: number }> {
		return this.transaction("mutation_outbox_snapshot", () => {
			const record = this.get<MutationOutboxRecord<A>>(TABLES.outbox, clientMutationId);
			return { record, stopEpoch: record ? this.stopEpochOf(record.targetRef) : 0 };
		});
	}

	async editQueuedMessage(
		expected: MutationOutboxRecord<A>,
		barrier: MutationStopBarrier,
	): Promise<MutationRecoveryRecord<A> | undefined> {
		return this.transaction("mutation_outbox_edit", () => {
			const record = this.get<MutationOutboxRecord<A>>(TABLES.outbox, expected.clientMutationId);
			if (!record || !matchesQueuedMessageSnapshot(record, expected) || !canEditQueuedMessage(record)) return undefined;
			if (this.stopEpochOf(record.targetRef) !== barrier.stopEpoch) return undefined;
			const recovery: MutationRecoveryRecord<A> = { ...record, recoveryKind: "editing" };
			this.replace(TABLES.recovery, recovery);
			this.delete(TABLES.outbox, record.clientMutationId);
			return recovery;
		});
	}

	async removeQueuedMessage(expected: MutationOutboxRecord<A>, barrier: MutationStopBarrier): Promise<boolean> {
		return this.transaction("mutation_outbox_remove", () => {
			const record = this.get<MutationOutboxRecord<A>>(TABLES.outbox, expected.clientMutationId);
			if (!record || !matchesQueuedMessageSnapshot(record, expected) || !canRemoveQueuedMessage(record)) return false;
			if (this.stopEpochOf(record.targetRef) !== barrier.stopEpoch) return false;
			return this.delete(TABLES.outbox, record.clientMutationId);
		});
	}

	async resendRecovery(
		sourceIds: string | readonly string[],
		intent: MutationIntent<A>,
		barrier?: MutationStopBarrier,
	): Promise<MutationOutboxRecord<A> | undefined> {
		if (!intent.targetRef.trim()) throw new Error("targetRef is required");
		return this.transaction("mutation_recovery_resend", () => {
			const ids = [...new Set(typeof sourceIds === "string" ? [sourceIds] : sourceIds)];
			if (ids.length === 0) return undefined;
			for (const id of ids) {
				const source = this.get<MutationRecoveryRecord<A>>(TABLES.recovery, id);
				if (!source || source.targetRef !== intent.targetRef) return undefined;
			}
			const record = this.enqueueRecord(intent, barrier);
			for (const id of ids) this.delete(TABLES.recovery, id);
			const receipts = this.recoveryResendsOf(intent.targetRef);
			receipts.push({ targetRef: intent.targetRef, sourceIds: ids, clientMutationId: record.clientMutationId });
			this.writeRecoveryResends(intent.targetRef, receipts);
			return record;
		});
	}

	// Receipt ownership is independent of outbox/optimistic settlement. Only
	// the draft owner's explicit acknowledgment may retire it after a restart.
	async readRecoveryResend(targetRef: string, sourceIds: readonly string[]): Promise<RecoveryResendCommit | undefined> {
		const ids = new Set(sourceIds);
		return this.recoveryResendsOf(targetRef).find(
			(receipt) => receipt.sourceIds.length === ids.size && receipt.sourceIds.every((id) => ids.has(id)),
		);
	}

	async acknowledgeRecoveryResend(targetRef: string, clientMutationId: string): Promise<void> {
		this.transaction("mutation_recovery_acknowledge", () => {
			const receipts = this.recoveryResendsOf(targetRef);
			this.writeRecoveryResends(
				targetRef,
				receipts.filter((receipt) => receipt.clientMutationId !== clientMutationId),
			);
		});
	}

	private recoveryResendsOf(targetRef: string): RecoveryResendCommit[] {
		const row = this.db.getFirstSync<{ recovery_resends: string }>(
			"SELECT recovery_resends FROM mutation_sequence WHERE target_ref = ?",
			targetRef,
		);
		return row ? JSON.parse(row.recovery_resends) : [];
	}

	private writeRecoveryResends(targetRef: string, receipts: RecoveryResendCommit[]): void {
		const updated = this.db.runSync(
			"UPDATE mutation_sequence SET recovery_resends = ? WHERE target_ref = ?",
			JSON.stringify(receipts),
			targetRef,
		);
		// A destination ref can have no sequence row yet; its receipt still has
		// to land. The UPDATE first keeps the resend rollback boundary truthful.
		if (changedRows(updated) === 0)
			this.db.runSync(
				"INSERT INTO mutation_sequence (target_ref, last_sequence, recovery_resends) VALUES (?, 0, ?)",
				targetRef,
				JSON.stringify(receipts),
			);
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

	// The user's Discard on their own message that a Stop held before it left
	// the phone ("canceled") or whose delivery couldn't be confirmed
	// ("blockedUnknown"): exactly that row, for exactly that composite target.
	// A submitting row is still owed to the daemon and is never discarded.
	async discardUndelivered(clientMutationId: string, targetRef: string): Promise<boolean> {
		return (
			changedRows(
				this.db.runSync(
					`DELETE FROM ${TABLES.outbox}
					 WHERE client_mutation_id = ? AND target_ref = ? AND state IN ('blockedUnknown', 'canceled')`,
					clientMutationId,
					targetRef,
				),
			) > 0
		);
	}

	// The one release of a canceled row: the user's Send now. It moves the row
	// to the end of its target's line, with a fresh sequence, so it can never
	// dispatch ahead of the interrupt whose Stop canceled it (the row never
	// left the phone, so moving it is safe). The barrier is the press's
	// stop-epoch capture: a Stop that committed after it outranks the press,
	// and the row stays canceled. A canceled row was never attempted (a Stop
	// cancels only rows with attempted = 0, enqueueInterruptAndCancel), so it
	// goes back exactly as it was. The web adapter's releaseCanceled, plus the
	// move.
	async releaseCanceled(clientMutationId: string, targetRef: string, barrier: MutationStopBarrier): Promise<boolean> {
		return this.transaction("mutation_outbox_release_canceled", () => {
			const record = this.get<MutationOutboxRecord<A>>(TABLES.outbox, clientMutationId);
			if (record?.state !== "canceled" || record.targetRef !== targetRef) return false;
			if (this.stopEpochOf(targetRef) > barrier.stopEpoch) return false;
			// The release is the user deliberately sending after a Stop (§4's
			// exemption), so it re-authorizes the row past the Stop its fence
			// captured: the capture refreshes to the current epoch, and the
			// row's next refusal redelivers instead of re-canceling.
			this.db.runSync(
				`UPDATE ${TABLES.outbox} SET state = 'submitting', intent_sequence = ?, stop_epoch_at_enqueue = ?
					WHERE client_mutation_id = ?`,
				this.allocateSequence(targetRef),
				this.stopEpochOf(targetRef),
				clientMutationId,
			);
			return true;
		});
	}

	// The lowest-sequence submitting outbox record for this target. A
	// canceled row provably never left the client, so it cannot be reordered
	// against the daemon and must not park what follows it - including the
	// interrupt that canceled it. A blockedUnknown head is different: the
	// daemon may already have applied it, so the FIFO stays closed behind it
	// and every later one on the SAME target, never another target's.
	async nextDispatchable(targetRef: string): Promise<MutationOutboxRecord<A> | undefined> {
		// The FIFO head is the lowest-sequence row still in play; a canceled row
		// above it provably never left the client and never parks the queue. Ask
		// SQLite for that one row directly instead of decoding the target's whole
		// queue only to discard it. The state filter mirrors the loop it replaces:
		// the first submitting/blockedUnknown row decides, and anything else is
		// skipped.
		const row = this.db.getFirstSync<Row>(
			`SELECT * FROM ${TABLES.outbox} WHERE target_ref = ? AND state IN ('submitting', 'blockedUnknown')
			 ORDER BY intent_sequence LIMIT 1`,
			targetRef,
		);
		if (!row || row.state === "blockedUnknown") return undefined;
		return fromRow<A, MutationOutboxRecord<A>>(row);
	}

	// Reopens every blockedUnknown record for this target the authoritative
	// read does NOT name - a missing id is not proof of non-delivery (a
	// bounded transcript can omit older work), so only an id the snapshot
	// explicitly confirms stays settled. Returns the ids restored. Wrapped in
	// the same savepoint as every other compound write here: a throw must not
	// leave some blockedUnknown records reopened and others not.
	async restoreProvenAbsent(targetRef: string, authoritativeIds: ReadonlySet<string>): Promise<string[]> {
		return this.transaction("mutation_outbox_restore_absent", () => {
			// One statement reopens the whole omitted set and reports the ids it
			// changed, instead of decoding every row on the target and updating the
			// blocked ones one at a time. It stays inside the savepoint: a fault on
			// any row aborts the single UPDATE and rolls the entire restore back,
			// the same all-or-nothing the loop's savepoint gave. RETURNING promises
			// no row order, so carry intent_sequence out and sort here to keep the
			// ascending order callers saw when this listed the target first.
			const named = [...authoritativeIds];
			const namedClause = named.length > 0 ? ` AND client_mutation_id NOT IN (${named.map(() => "?").join(", ")})` : "";
			const restored = this.db.getAllSync<{ client_mutation_id: string; intent_sequence: number }>(
				`UPDATE ${TABLES.outbox} SET state = 'submitting'
				 WHERE target_ref = ? AND state = 'blockedUnknown'${namedClause}
				 RETURNING client_mutation_id, intent_sequence`,
				targetRef,
				...named,
			);
			return restored
				.sort((left, right) => left.intent_sequence - right.intent_sequence)
				.map((row) => row.client_mutation_id);
		});
	}

	// Retires accepted records the daemon's complete mutation projection no
	// longer lists. An accepted record is already applied (its receipt said so)
	// and is kept only until a read shows it reflected; the daemon drops a
	// start or queue from that projection the moment its turn is incorporated,
	// so once the transcript item leaves a bounded read's window nothing would
	// ever name the id again and the composer would show it as a send still on
	// its way, with no action to take. Absence from a mutation-authoritative
	// projection is the proof, exactly as it is for a parked outbox row.
	// `acceptedBefore` is the read's own start: acceptance is stamped separately
	// from enqueue, so a receipt landing while that read was out keeps its record
	// out of the prune. `authoritativeIds` is what that read DID name - a named
	// record is proof it is still pending, so it stays even if settling it failed.
	// Only the turn verbs the daemon's pending projection can name take part - a
	// note or a clear is never listed there, and its absence proves nothing.
	async pruneUnlistedAccepted(
		targetRef: string,
		acceptedBefore: number,
		authoritativeIds: ReadonlySet<string>,
	): Promise<string[]> {
		return this.transaction("mutation_optimistic_prune_unlisted", () => {
			const methods = [...PENDING_TURN_METHODS];
			const prunable = this.db.getAllSync<{ client_mutation_id: string; intent_sequence: number }>(
				`SELECT client_mutation_id, intent_sequence FROM ${TABLES.optimistic}
				 WHERE target_ref = ? AND state = 'accepted' AND accepted_at > 0 AND accepted_at <= ?
				   AND method IN (${methods.map(() => "?").join(", ")})`,
				targetRef,
				acceptedBefore,
				...methods,
			);
			const unlisted = prunable.filter((row) => !authoritativeIds.has(row.client_mutation_id));
			if (unlisted.length === 0) return [];
			const removed = this.db.getAllSync<{ client_mutation_id: string; intent_sequence: number }>(
				`DELETE FROM ${TABLES.optimistic}
				 WHERE client_mutation_id IN (${unlisted.map(() => "?").join(", ")})
				 RETURNING client_mutation_id, intent_sequence`,
				...unlisted.map((row) => row.client_mutation_id),
			);
			return removed
				.sort((left, right) => left.intent_sequence - right.intent_sequence)
				.map((row) => row.client_mutation_id);
		});
	}

	protected list<T extends MutationRecord<A>>(table: string, targetRef?: string): T[] {
		// One statement template, scoped or not, so the query string lives in one
		// place instead of two near-identical copies.
		const rows = this.db.getAllSync<Row>(
			`SELECT * FROM ${table}${targetRef === undefined ? "" : " WHERE target_ref = ?"} ORDER BY intent_sequence`,
			...(targetRef === undefined ? [] : [targetRef]),
		);
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

	protected insertNew(
		table: string,
		record: MutationOutboxRecord<A> | MutationOptimisticRecord<A> | MutationRecoveryRecord<A>,
	): void {
		this.db.runSync(insertSQL(table), ...this.insertValues(record));
	}

	// A transition (a receipt settling, a rejection transferring) writes a
	// record that may already occupy this id in this table - a retry of the
	// same transition, say - and every column must land as given, the same
	// full-row replace the oracle's `put` gives on a colliding key. Refreshing
	// only state/attempted would leave a stale payload/display/recovery
	// reason behind from whatever the row held before.
	protected replace(
		table: string,
		record: MutationOutboxRecord<A> | MutationOptimisticRecord<A> | MutationRecoveryRecord<A>,
	): void {
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
			record.composerMentions === undefined ? null : JSON.stringify(record.composerMentions),
			record.draftCaptureToken ?? null,
			record.intentSequence,
			record.createdAt,
			record.state,
			"attempted" in record && record.attempted ? 1 : 0,
			"stopEpochAtEnqueue" in record ? (record.stopEpochAtEnqueue ?? 0) : 0,
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
