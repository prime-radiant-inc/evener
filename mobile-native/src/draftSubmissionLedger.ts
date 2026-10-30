import type { DraftDestination } from "./draftRepository";
import type { SqliteSync } from "./sqliteSync";

/** The durable record that a submission began at an identity. A submission
 * writes one before any transport call and resolves it when it settles, so a
 * completion can find the identity that owns the record now without any
 * in-memory chain. The obligation id is the submission's capture token: the
 * one value both the uncertain record and the resolution name. */
export type DraftSubmissionState = "open" | "completed" | "not-sent" | "failed";

export interface DraftSubmissionObligation {
	obligationId: string;
	origin: DraftDestination;
	state: DraftSubmissionState;
	recoveryIds: string[];
	completedCommandIds: string[];
	releasedRecoveryIds: string[];
	appliedAt: number | null;
	createdAt: number;
}

/** The dispositions one settlement durably delivered, and where. One row per
 * (destination, recovery id): a completed disposition is stronger than a
 * released one, so a row that already says completed never regresses. */
export interface DraftDispositionDelivery {
	destination: DraftDestination;
	recoveryId: string;
	disposition: "completed" | "released";
	transferId: string;
}

/** Only a completed submission or a proven local refusal clears its capture:
 * a failed operation's input stays recoverable, so a failed obligation never
 * releases recovery suppression or delivers a capture-clearing completion. */
export function clearsCapture(state: DraftSubmissionState): boolean {
	return state === "completed" || state === "not-sent";
}

export interface DraftSubmissionLedgerOptions {
	now?: () => number;
}

export class DraftSubmissionLedgerSQLite {
	readonly #db: SqliteSync;
	readonly #now: () => number;

	constructor(db: SqliteSync, options: DraftSubmissionLedgerOptions = {}) {
		this.#db = db;
		this.#now = options.now ?? Date.now;
		// Greenfield: created once on first open, never dropped or rebuilt.
		this.#db.execSync(`CREATE TABLE IF NOT EXISTS draft_submission_obligations (
      obligation_id TEXT PRIMARY KEY,
      hub_id TEXT NOT NULL, session_ref TEXT NOT NULL,
      state TEXT NOT NULL,
      recovery_ids TEXT NOT NULL DEFAULT '[]',
      completed_command_ids TEXT NOT NULL DEFAULT '[]',
      released_recovery_ids TEXT NOT NULL DEFAULT '[]',
      applied_at INTEGER,
      created_at INTEGER NOT NULL,
      state_changed_at INTEGER NOT NULL
    )`);
		this.#db.execSync(
			`CREATE INDEX IF NOT EXISTS draft_submission_obligations_unresolved
      ON draft_submission_obligations (created_at) WHERE applied_at IS NULL`,
		);
		this.#db.execSync(`CREATE TABLE IF NOT EXISTS draft_handoff_deliveries (
      hub_id TEXT NOT NULL, session_ref TEXT NOT NULL, recovery_id TEXT NOT NULL,
      disposition TEXT NOT NULL, transfer_id TEXT NOT NULL,
      PRIMARY KEY (hub_id, session_ref, recovery_id)
    )`);
		this.#db.execSync(
			`CREATE INDEX IF NOT EXISTS draft_handoff_deliveries_recovery
      ON draft_handoff_deliveries (recovery_id)`,
		);
	}

	/** Record the submission before transport. Throws when the store cannot
	 * write: the caller falls back to keeping the input recoverable. */
	beginSubmission(obligationId: string, origin: DraftDestination, recoveryIds: readonly string[]): void {
		const at = this.#now();
		this.#db.runSync(
			`INSERT INTO draft_submission_obligations
      (obligation_id, hub_id, session_ref, state, recovery_ids, created_at, state_changed_at)
    VALUES (?, ?, ?, 'open', ?, ?, ?)`,
			obligationId,
			origin.hubId,
			origin.sessionRef,
			JSON.stringify([...recoveryIds]),
			at,
			at,
		);
	}

	/** Settle the obligation's outcome. Throws when the store cannot write or
	 * the obligation was never recorded: the caller queues the resolution. */
	resolveSubmission(
		obligationId: string,
		state: DraftSubmissionState,
		completedCommandIds: readonly string[],
		releasedRecoveryIds: readonly string[],
	): void {
		const changed = this.#db.runSync(
			`UPDATE draft_submission_obligations
     SET state = ?, completed_command_ids = ?, released_recovery_ids = ?,
         state_changed_at = ?, applied_at = CASE WHEN ? = 'failed' THEN ? ELSE applied_at END
   WHERE obligation_id = ? AND state = 'open'`,
			state,
			JSON.stringify([...completedCommandIds]),
			JSON.stringify([...releasedRecoveryIds]),
			this.#now(),
			state,
			this.#now(),
			obligationId,
		);
		if (changed === undefined || changed.changes === 0)
			throw new Error("a submission resolution must name an open obligation");
	}

	/** Every obligation still owing routine work: open ones block restoration
	 * and handoff settlement; resolved ones owe their completion's delivery.
	 * Acknowledged and failed history contributes nothing - the partial index
	 * prices the pass in unresolved work alone. */
	outstandingObligations(): DraftSubmissionObligation[] {
		return this.#db
			.getAllSync<ObligationRow>(
				`SELECT * FROM draft_submission_obligations
     WHERE applied_at IS NULL ORDER BY created_at, rowid`,
			)
			.map((row) => this.mapObligation(row));
	}

	/** The open obligations whose recovery ids this identity must not restore:
	 * a running command's input is owned by its submission until it settles. */
	openObligationsForOrigin(origin: DraftDestination): DraftSubmissionObligation[] {
		return this.#db
			.getAllSync<ObligationRow>(
				`SELECT * FROM draft_submission_obligations
     WHERE hub_id = ? AND session_ref = ? AND state = 'open' ORDER BY created_at, rowid`,
				origin.hubId,
				origin.sessionRef,
			)
			.map((row) => this.mapObligation(row));
	}

	obligationById(obligationId: string): DraftSubmissionObligation | undefined {
		const row = this.#db.getFirstSync<ObligationRow>(
			"SELECT * FROM draft_submission_obligations WHERE obligation_id = ?",
			obligationId,
		);
		return row === null ? undefined : this.mapObligation(row);
	}

	/** The completion durably applied (or provably needed nothing): the
	 * obligation leaves the routine pass forever. */
	acknowledgeApplied(obligationId: string): void {
		this.#db.runSync(
			"UPDATE draft_submission_obligations SET applied_at = ?, state_changed_at = ? WHERE obligation_id = ?",
			this.#now(),
			this.#now(),
			obligationId,
		);
	}

	/** Record what one settlement delivered, and where. Completed wins: the
	 * row is the durable evidence an executed command's input must not
	 * restore, so a released row never overwrites it. */
	writeDeliveries(deliveries: readonly DraftDispositionDelivery[]): void {
		for (const delivery of deliveries) {
			const existing = this.#db.getFirstSync<{ disposition: string }>(
				"SELECT disposition FROM draft_handoff_deliveries WHERE hub_id = ? AND session_ref = ? AND recovery_id = ?",
				delivery.destination.hubId,
				delivery.destination.sessionRef,
				delivery.recoveryId,
			);
			if (existing?.disposition === "completed") continue;
			this.#db.runSync(
				`INSERT INTO draft_handoff_deliveries
      (hub_id, session_ref, recovery_id, disposition, transfer_id)
    VALUES (?, ?, ?, ?, ?)
    ON CONFLICT (hub_id, session_ref, recovery_id) DO UPDATE SET
      disposition = excluded.disposition, transfer_id = excluded.transfer_id`,
				delivery.destination.hubId,
				delivery.destination.sessionRef,
				delivery.recoveryId,
				delivery.disposition,
				delivery.transferId,
			);
		}
	}

	/** The durable delivery evidence, for tests and diagnostics. */
	deliveries(): DraftDispositionDelivery[] {
		return this.#db
			.getAllSync<DeliveryRow>("SELECT * FROM draft_handoff_deliveries ORDER BY hub_id, session_ref, recovery_id")
			.map((row) => ({
				destination: { hubId: row.hub_id, sessionRef: row.session_ref },
				recoveryId: row.recovery_id,
				disposition: row.disposition as "completed" | "released",
				transferId: row.transfer_id,
			}));
	}

	/** Every destination that holds delivery evidence for a recovery id: the
	 * routing a refused handoff's rows follow when their submission resolves. */
	deliveryDestinations(recoveryId: string): { hubId: string; sessionRef: string }[] {
		return this.#db
			.getAllSync<{ hub_id: string; session_ref: string }>(
				"SELECT DISTINCT hub_id, session_ref FROM draft_handoff_deliveries WHERE recovery_id = ?",
				recoveryId,
			)
			.map((row) => ({ hubId: row.hub_id, sessionRef: row.session_ref }));
	}

	private mapObligation(row: ObligationRow): DraftSubmissionObligation {
		return {
			obligationId: row.obligation_id,
			origin: { hubId: row.hub_id, sessionRef: row.session_ref },
			state: row.state as DraftSubmissionState,
			recoveryIds: decodeIdList(row.recovery_ids),
			completedCommandIds: decodeIdList(row.completed_command_ids),
			releasedRecoveryIds: decodeIdList(row.released_recovery_ids),
			appliedAt: row.applied_at,
			createdAt: row.created_at,
		};
	}
}

interface ObligationRow {
	obligation_id: string;
	hub_id: string;
	session_ref: string;
	state: string;
	recovery_ids: string;
	completed_command_ids: string | null;
	released_recovery_ids: string | null;
	applied_at: number | null;
	created_at: number;
}

interface DeliveryRow {
	hub_id: string;
	session_ref: string;
	recovery_id: string;
	disposition: string;
	transfer_id: string;
}

function decodeIdList(raw: string | null): string[] {
	if (raw === null) return [];
	try {
		const parsed: unknown = JSON.parse(raw);
		return Array.isArray(parsed) ? parsed.filter((id): id is string => typeof id === "string") : [];
	} catch {
		return [];
	}
}
