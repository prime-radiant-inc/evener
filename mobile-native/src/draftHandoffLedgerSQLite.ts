import type { SqliteSync } from "./sqliteSync";
import { withSavepoint } from "./sqliteSync";
import type {
	CreateDraftHandoffEdgeInput,
	DraftHandoffDelivery,
	DraftHandoffDisposition,
	DraftHandoffEdge,
	DraftHandoffEdgeState,
	DraftHandoffEndpoint,
	DraftHandoffGraph,
	DraftHandoffLedger,
	DraftSubmissionObligation,
	DraftSubmissionState,
} from "./draftHandoffLedger";

interface EdgeRow {
	edge_id: string;
	chain_id: string;
	from_hub_id: string;
	from_ref: string;
	to_hub_id: string;
	to_ref: string;
	state: DraftHandoffEdgeState;
	claim_at: number | null;
	delivered_hash: string | null;
	superseded_by: string | null;
	created_at: number;
	state_changed_at: number;
}

interface ObligationRow {
	obligation_id: string;
	chain_id: string | null;
	origin_hub_id: string;
	origin_ref: string;
	state: DraftSubmissionState;
	completed_command_ids: string;
	created_at: number;
	resolved_at: number | null;
}

interface DeliveryRow {
	chain_id: string;
	hub_id: string;
	session_ref: string;
	recovery_id: string;
	disposition: DraftHandoffDisposition;
	edge_id: string;
}

export interface DraftHandoffLedgerOptions {
	now?: () => number;
	/** Stable ids for chains and edges; tests inject a counter. */
	createId?: () => string;
}

/** The ledger's durable home. Every transition is one savepoint on the
 * mutations database, so an edge's state, its delivery rows, and the
 * obligation checks that authorize the transition commit together or not at
 * all. */
export class DraftHandoffLedgerSQLite implements DraftHandoffLedger {
	readonly #now: () => number;
	readonly #createId: () => string;

	constructor(
		private db: SqliteSync,
		options: DraftHandoffLedgerOptions = {},
	) {
		this.#now = options.now ?? Date.now;
		this.#createId = options.createId ?? (() => `edge-${Math.random().toString(36).slice(2, 10)}`);
		this.createSchema();
	}

	/** Idempotent: a second construction on the same database creates nothing. */
	createSchema(): void {
		this.db.execSync(
			`CREATE TABLE IF NOT EXISTS draft_handoff_edges (
			 edge_id TEXT PRIMARY KEY,
			 chain_id TEXT NOT NULL,
			 from_hub_id TEXT NOT NULL, from_ref TEXT NOT NULL,
			 to_hub_id TEXT NOT NULL, to_ref TEXT NOT NULL,
			 state TEXT NOT NULL,
			 claim_at INTEGER,
			 delivered_hash TEXT,
			 superseded_by TEXT,
			 created_at INTEGER NOT NULL,
			 state_changed_at INTEGER NOT NULL)`,
		);
		// Only live edges hold the identity: a cleaned tombstone must never block
		// a later attempt for the same pair.
		this.db.execSync("DROP INDEX IF EXISTS draft_handoff_edges_identity");
		this.db.execSync(
			`CREATE UNIQUE INDEX IF NOT EXISTS draft_handoff_edges_identity
			 ON draft_handoff_edges (chain_id, from_hub_id, from_ref, to_hub_id, to_ref)
			 WHERE state != 'cleaned'`,
		);
		this.db.execSync(
			`CREATE INDEX IF NOT EXISTS draft_handoff_edges_destination
			 ON draft_handoff_edges (to_hub_id, to_ref)`,
		);
		this.db.execSync(
			`CREATE TABLE IF NOT EXISTS draft_handoff_dependencies (
			 edge_id TEXT NOT NULL,
			 depends_on_edge_id TEXT NOT NULL,
			 PRIMARY KEY (edge_id, depends_on_edge_id))`,
		);
		this.db.execSync(
			`CREATE TABLE IF NOT EXISTS draft_handoff_edge_obligations (
			 edge_id TEXT NOT NULL,
			 obligation_id TEXT NOT NULL,
			 PRIMARY KEY (edge_id, obligation_id))`,
		);
		this.db.execSync(
			`CREATE TABLE IF NOT EXISTS draft_submission_obligations (
			 obligation_id TEXT PRIMARY KEY,
			 chain_id TEXT,
			 origin_hub_id TEXT NOT NULL,
			 origin_ref TEXT NOT NULL,
			 state TEXT NOT NULL,
			 completed_command_ids TEXT NOT NULL DEFAULT '[]',
			 created_at INTEGER NOT NULL,
			 resolved_at INTEGER)`,
		);
		this.db.execSync(
			`CREATE TABLE IF NOT EXISTS draft_handoff_deliveries (
			 chain_id TEXT NOT NULL,
			 hub_id TEXT NOT NULL,
			 session_ref TEXT NOT NULL,
			 recovery_id TEXT NOT NULL,
			 disposition TEXT NOT NULL,
			 edge_id TEXT NOT NULL,
			 PRIMARY KEY (chain_id, hub_id, session_ref, recovery_id))`,
		);
	}

	/** Create the chain's edge inside the retarget savepoint. The inbound edge
	 * makes the new one settle after it, and every open obligation of the chain
	 * plus every open submission at the source rides the new edge. */
	recordIdentityChange(from: DraftHandoffEndpoint, to: DraftHandoffEndpoint): { edgeId: string; chainId: string } {
		return withSavepoint(this.db, "draft_handoff_record_identity", () => {
			const recorded = this.db.getFirstSync<EdgeRow>(
				`SELECT * FROM draft_handoff_edges
				 WHERE from_hub_id = ? AND from_ref = ? AND to_hub_id = ? AND to_ref = ? AND state = 'pending'
				 ORDER BY created_at DESC, rowid DESC LIMIT 1`,
				from.hubId,
				from.sessionRef,
				to.hubId,
				to.sessionRef,
			);
			if (recorded !== null) {
				for (const obligation of this.openObligations(recorded.chain_id, from)) {
					this.stampChain(obligation, recorded.chain_id);
					this.attachObligation(recorded.edge_id, obligation);
				}
				for (const predecessor of this.db.getAllSync<EdgeRow>(
					`SELECT * FROM draft_handoff_edges
					 WHERE to_hub_id = ? AND to_ref = ? AND state != 'cleaned' ORDER BY created_at, rowid`,
					from.hubId,
					from.sessionRef,
				))
					this.db.runSync(
						`INSERT OR IGNORE INTO draft_handoff_dependencies (edge_id, depends_on_edge_id) VALUES (?, ?)`,
						predecessor.edge_id,
						recorded.edge_id,
					);
				return { edgeId: recorded.edge_id, chainId: recorded.chain_id };
			}
			const inbound = this.db.getFirstSync<EdgeRow>(
				`SELECT * FROM draft_handoff_edges
				 WHERE to_hub_id = ? AND to_ref = ? AND state != 'cleaned'
				 ORDER BY created_at DESC, rowid DESC LIMIT 1`,
				from.hubId,
				from.sessionRef,
			);
			const inboundEdges = this.db.getAllSync<EdgeRow>(
				`SELECT * FROM draft_handoff_edges
				 WHERE to_hub_id = ? AND to_ref = ? AND state != 'cleaned'
				 ORDER BY created_at, rowid`,
				from.hubId,
				from.sessionRef,
			);
			// A settled or refused edge with the same identity is a finished
			// attempt, not something to revive: record a fresh chain instead.
			const finished = this.db.getFirstSync<EdgeRow>(
				`SELECT * FROM draft_handoff_edges
				 WHERE from_hub_id = ? AND from_ref = ? AND to_hub_id = ? AND to_ref = ? AND state != 'cleaned'
				 ORDER BY created_at DESC, rowid DESC LIMIT 1`,
				from.hubId,
				from.sessionRef,
				to.hubId,
				to.sessionRef,
			);
			const reusedChain = inbound?.chain_id ?? this.#createId();
			const chainId = finished !== null && finished.chain_id === reusedChain ? this.#createId() : reusedChain;
			const obligations = this.openObligations(chainId, from);
			for (const obligation of obligations) this.stampChain(obligation, chainId);
			const edge = this.createEdge({ chainId, from, to, obligations });
			// A predecessor settles after the edge that continues the chain, so
			// its older composition cannot occupy the terminal destination first.
			for (const predecessor of inboundEdges)
				this.db.runSync(
					`INSERT OR IGNORE INTO draft_handoff_dependencies (edge_id, depends_on_edge_id) VALUES (?, ?)`,
					predecessor.edge_id,
					edge.edgeId,
				);
			return { edgeId: edge.edgeId, chainId };
		});
	}

	createEdge(input: CreateDraftHandoffEdgeInput): DraftHandoffEdge {
		return withSavepoint(this.db, "draft_handoff_edge_insert", () => {
			const existing = this.edgeByIdentity(input.chainId, input.from, input.to);
			if (existing !== undefined) {
				// Merge rather than silently ignore what the caller carried.
				for (const dependency of input.dependencies ?? []) {
					if (this.edge(dependency) === undefined) throw new Error(`unknown handoff dependency ${dependency}`);
					this.db.runSync(
						`INSERT OR IGNORE INTO draft_handoff_dependencies (edge_id, depends_on_edge_id) VALUES (?, ?)`,
						existing.edgeId,
						dependency,
					);
				}
				for (const obligation of input.obligations ?? []) {
					if (this.obligation(obligation) === undefined) throw new Error(`unknown handoff obligation ${obligation}`);
					this.attachObligation(existing.edgeId, obligation);
				}
				const merged = this.edge(existing.edgeId);
				if (merged === undefined) throw new Error("handoff edge disappeared");
				return merged;
			}
			const edgeId = this.#createId();
			const now = this.#now();
			this.db.runSync(
				`INSERT INTO draft_handoff_edges
				 (edge_id, chain_id, from_hub_id, from_ref, to_hub_id, to_ref, state, created_at, state_changed_at)
				 VALUES (?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
				edgeId,
				input.chainId,
				input.from.hubId,
				input.from.sessionRef,
				input.to.hubId,
				input.to.sessionRef,
				now,
				now,
			);
			for (const dependency of input.dependencies ?? []) {
				if (this.edge(dependency) === undefined) throw new Error(`unknown handoff dependency ${dependency}`);
				this.db.runSync(
					`INSERT OR IGNORE INTO draft_handoff_dependencies (edge_id, depends_on_edge_id) VALUES (?, ?)`,
					edgeId,
					dependency,
				);
			}
			for (const obligation of input.obligations ?? []) {
				if (this.obligation(obligation) === undefined) throw new Error(`unknown handoff obligation ${obligation}`);
				this.attachObligation(edgeId, obligation);
			}
			const created = this.edge(edgeId);
			if (created === undefined) throw new Error("handoff edge insert did not commit");
			return created;
		});
	}

	readGraph(): DraftHandoffGraph {
		return {
			edges: this.db
				.getAllSync<EdgeRow>(`SELECT * FROM draft_handoff_edges ORDER BY created_at, rowid`)
				.map((row) => this.mapEdge(row)),
			obligations: this.db
				.getAllSync<ObligationRow>(`SELECT * FROM draft_submission_obligations ORDER BY created_at, rowid`)
				.map((row) => this.mapObligation(row)),
			deliveries: this.db
				.getAllSync<DeliveryRow>(
					`SELECT * FROM draft_handoff_deliveries ORDER BY chain_id, hub_id, session_ref, recovery_id`,
				)
				.map((row) => ({
					chainId: row.chain_id,
					destination: { hubId: row.hub_id, sessionRef: row.session_ref },
					recoveryId: row.recovery_id,
					disposition: row.disposition,
					edgeId: row.edge_id,
				})),
		};
	}

	claimDestination(edgeId: string): boolean {
		return withSavepoint(this.db, "draft_handoff_claim", () => {
			const edge = this.requirePending(edgeId);
			if (edge.claimAt !== null) return true;
			const busy = this.db.getFirstSync<{ edge_id: string }>(
				`SELECT edge_id FROM draft_handoff_edges
				 WHERE edge_id != ? AND to_hub_id = ? AND to_ref = ? AND state != 'cleaned'
				   AND (claim_at IS NOT NULL OR state = 'settled')
				 LIMIT 1`,
				edge.edgeId,
				edge.to.hubId,
				edge.to.sessionRef,
			);
			if (busy !== null) return false;
			this.db.runSync(
				`UPDATE draft_handoff_edges SET claim_at = ?, state_changed_at = ? WHERE edge_id = ?`,
				this.#now(),
				this.#now(),
				edge.edgeId,
			);
			return true;
		});
	}

	settleEdge(edgeId: string, deliveries: readonly DraftHandoffDelivery[], deliveredHash: string): void {
		withSavepoint(this.db, "draft_handoff_settle", () => {
			const edge = this.requirePending(edgeId);
			this.requireNoOpenObligations(edgeId);
			this.requireNoPendingDependencies(edgeId);
			this.writeDeliveries(edge, deliveries);
			this.db.runSync(
				`UPDATE draft_handoff_edges
				 SET state = 'settled', claim_at = NULL, delivered_hash = ?, state_changed_at = ?
				 WHERE edge_id = ?`,
				deliveredHash,
				this.#now(),
				edgeId,
			);
		});
	}

	refuseEdge(edgeId: string, deliveries: readonly DraftHandoffDelivery[]): void {
		withSavepoint(this.db, "draft_handoff_refuse", () => {
			const edge = this.requirePending(edgeId);
			this.requireNoOpenObligations(edgeId);
			this.requireNoPendingDependencies(edgeId);
			this.writeDeliveries(edge, deliveries);
			this.db.runSync(
				`UPDATE draft_handoff_edges
				 SET state = 'refused', claim_at = NULL, state_changed_at = ?
				 WHERE edge_id = ?`,
				this.#now(),
				edgeId,
			);
		});
	}

	cleanEdge(edgeId: string): void {
		withSavepoint(this.db, "draft_handoff_clean", () => {
			const edge = this.edge(edgeId);
			if (edge === undefined) throw new Error(`unknown handoff edge ${edgeId}`);
			if (edge.state === "cleaned") return;
			if (edge.state === "pending") throw new Error("a pending handoff cannot be cleaned");
			this.requireNoOpenObligations(edgeId);
			const pendingDependent = this.db.getFirstSync<{ edge_id: string }>(
				`SELECT d.edge_id FROM draft_handoff_dependencies d
				 JOIN draft_handoff_edges e ON e.edge_id = d.edge_id
				 WHERE d.depends_on_edge_id = ? AND e.state = 'pending' LIMIT 1`,
				edgeId,
			);
			if (pendingDependent !== null) throw new Error("a handoff with a pending dependent cannot be cleaned");
			this.db.runSync(
				`UPDATE draft_handoff_edges SET state = 'cleaned', claim_at = NULL, state_changed_at = ? WHERE edge_id = ?`,
				this.#now(),
				edgeId,
			);
		});
	}

	beginSubmission(obligationId: string, origin: DraftHandoffEndpoint): void {
		this.db.runSync(
			`INSERT OR IGNORE INTO draft_submission_obligations
			 (obligation_id, origin_hub_id, origin_ref, state, completed_command_ids, created_at)
			 VALUES (?, ?, ?, 'open', '[]', ?)`,
			obligationId,
			origin.hubId,
			origin.sessionRef,
			this.#now(),
		);
	}

	resolveSubmission(
		obligationId: string,
		state: Exclude<DraftSubmissionState, "open">,
		completedCommandIds: readonly string[] = [],
	): void {
		withSavepoint(this.db, "draft_handoff_resolve_submission", () => {
			const existing = this.obligation(obligationId);
			if (existing === undefined) return;
			const completed = [...new Set([...existing.completedCommandIds, ...completedCommandIds])];
			if (existing.state === "open") {
				this.db.runSync(
					`UPDATE draft_submission_obligations
					 SET state = ?, completed_command_ids = ?, resolved_at = ?
					 WHERE obligation_id = ?`,
					state,
					JSON.stringify(completed),
					this.#now(),
					obligationId,
				);
				return;
			}
			// A retried completion may carry ids the first write missed; the state
			// and resolution time stay the first one's.
			if (existing.state === state && completed.length !== existing.completedCommandIds.length)
				this.db.runSync(
					`UPDATE draft_submission_obligations SET completed_command_ids = ? WHERE obligation_id = ?`,
					JSON.stringify(completed),
					obligationId,
				);
		});
	}

	private openObligations(chainId: string, from: DraftHandoffEndpoint): string[] {
		const rows = this.db.getAllSync<{ obligation_id: string }>(
			`SELECT obligation_id FROM draft_submission_obligations
			 WHERE state = 'open' AND (chain_id = ? OR chain_id IS NULL)
			   AND (chain_id = ? OR (origin_hub_id = ? AND origin_ref = ?))
			 ORDER BY created_at, rowid`,
			chainId,
			chainId,
			from.hubId,
			from.sessionRef,
		);
		return rows.map((row) => row.obligation_id);
	}

	private stampChain(obligationId: string, chainId: string): void {
		this.db.runSync(
			`UPDATE draft_submission_obligations SET chain_id = ? WHERE obligation_id = ? AND chain_id IS NULL`,
			chainId,
			obligationId,
		);
	}

	private attachObligation(edgeId: string, obligationId: string): void {
		this.db.runSync(
			`INSERT OR IGNORE INTO draft_handoff_edge_obligations (edge_id, obligation_id) VALUES (?, ?)`,
			edgeId,
			obligationId,
		);
	}

	private writeDeliveries(edge: DraftHandoffEdge, deliveries: readonly DraftHandoffDelivery[]): void {
		for (const delivery of deliveries) {
			if (delivery.destination.hubId !== edge.to.hubId || delivery.destination.sessionRef !== edge.to.sessionRef)
				throw new Error("a handoff delivery must name the edge's destination");
			this.db.runSync(
				`INSERT INTO draft_handoff_deliveries
				 (chain_id, hub_id, session_ref, recovery_id, disposition, edge_id)
				 VALUES (?, ?, ?, ?, ?, ?)
				 ON CONFLICT (chain_id, hub_id, session_ref, recovery_id) DO UPDATE SET
				 disposition = excluded.disposition, edge_id = excluded.edge_id`,
				edge.chainId,
				delivery.destination.hubId,
				delivery.destination.sessionRef,
				delivery.recoveryId,
				delivery.disposition,
				edge.edgeId,
			);
		}
	}

	private requireNoOpenObligations(edgeId: string): void {
		const open = this.db.getFirstSync<{ obligation_id: string }>(
			`SELECT o.obligation_id FROM draft_handoff_edge_obligations eo
			 JOIN draft_submission_obligations o ON o.obligation_id = eo.obligation_id
			 WHERE eo.edge_id = ? AND o.state = 'open' LIMIT 1`,
			edgeId,
		);
		if (open !== null) throw new Error(`handoff edge ${edgeId} still has an open obligation`);
	}

	private requireNoPendingDependencies(edgeId: string): void {
		const pending = this.db.getFirstSync<{ edge_id: string }>(
			`SELECT d.depends_on_edge_id AS edge_id FROM draft_handoff_dependencies d
			 JOIN draft_handoff_edges e ON e.edge_id = d.depends_on_edge_id
			 WHERE d.edge_id = ? AND e.state = 'pending' LIMIT 1`,
			edgeId,
		);
		if (pending !== null) throw new Error(`handoff edge ${edgeId} still waits on ${pending.edge_id}`);
	}

	private requirePending(edgeId: string): DraftHandoffEdge {
		const edge = this.edge(edgeId);
		if (edge === undefined) throw new Error(`unknown handoff edge ${edgeId}`);
		if (edge.state !== "pending") throw new Error(`handoff edge ${edgeId} is ${edge.state}`);
		return edge;
	}

	private edge(edgeId: string): DraftHandoffEdge | undefined {
		const row = this.db.getFirstSync<EdgeRow>(`SELECT * FROM draft_handoff_edges WHERE edge_id = ?`, edgeId);
		return row === null ? undefined : this.mapEdge(row);
	}

	private edgeByIdentity(
		chainId: string,
		from: DraftHandoffEndpoint,
		to: DraftHandoffEndpoint,
	): DraftHandoffEdge | undefined {
		const row = this.db.getFirstSync<EdgeRow>(
			`SELECT * FROM draft_handoff_edges
			 WHERE chain_id = ? AND from_hub_id = ? AND from_ref = ? AND to_hub_id = ? AND to_ref = ?
			   AND state != 'cleaned'`,
			chainId,
			from.hubId,
			from.sessionRef,
			to.hubId,
			to.sessionRef,
		);
		return row === null ? undefined : this.mapEdge(row);
	}

	private obligation(obligationId: string): DraftSubmissionObligation | undefined {
		const row = this.db.getFirstSync<ObligationRow>(
			`SELECT * FROM draft_submission_obligations WHERE obligation_id = ?`,
			obligationId,
		);
		return row === null ? undefined : this.mapObligation(row);
	}

	private mapEdge(row: EdgeRow): DraftHandoffEdge {
		const dependencies = this.db
			.getAllSync<{ depends_on_edge_id: string }>(
				`SELECT depends_on_edge_id FROM draft_handoff_dependencies WHERE edge_id = ? ORDER BY rowid`,
				row.edge_id,
			)
			.map((dependency) => dependency.depends_on_edge_id);
		const obligations = this.db
			.getAllSync<{ obligation_id: string }>(
				`SELECT obligation_id FROM draft_handoff_edge_obligations WHERE edge_id = ? ORDER BY rowid`,
				row.edge_id,
			)
			.map((obligation) => obligation.obligation_id);
		return {
			edgeId: row.edge_id,
			chainId: row.chain_id,
			from: { hubId: row.from_hub_id, sessionRef: row.from_ref },
			to: { hubId: row.to_hub_id, sessionRef: row.to_ref },
			state: row.state,
			dependencies,
			obligations,
			claimAt: row.claim_at,
			deliveredHash: row.delivered_hash,
			supersededBy: row.superseded_by,
			createdAt: row.created_at,
			stateChangedAt: row.state_changed_at,
		};
	}

	private mapObligation(row: ObligationRow): DraftSubmissionObligation {
		return {
			obligationId: row.obligation_id,
			chainId: row.chain_id,
			origin: { hubId: row.origin_hub_id, sessionRef: row.origin_ref },
			state: row.state,
			completedCommandIds: JSON.parse(row.completed_command_ids) as string[],
			createdAt: row.created_at,
			resolvedAt: row.resolved_at,
		};
	}
}
