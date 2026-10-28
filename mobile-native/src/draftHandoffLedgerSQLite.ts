import type { SqliteSync } from "./sqliteSync";
import { withSavepoint } from "./sqliteSync";
import { decodeDraftEndpoint } from "./draftHandoffLedger";
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
	composed_at: number | null;
	resolved_as: "settled" | "refused" | null;
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
	recovery_ids: string;
	completed_command_ids: string;
	released_recovery_ids: string;
	created_at: number;
	resolved_at: number | null;
	applied_at: number | null;
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
			 composed_at INTEGER,
			 resolved_as TEXT,
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
			 recovery_ids TEXT NOT NULL DEFAULT '[]',
			 completed_command_ids TEXT NOT NULL DEFAULT '[]',
			 released_recovery_ids TEXT NOT NULL DEFAULT '[]',
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
		// Additive columns for tables an earlier build of this branch created.
		this.addColumnIfMissing("draft_handoff_edges", "composed_at", "INTEGER");
		this.addColumnIfMissing("draft_handoff_edges", "resolved_as", "TEXT");
		this.addColumnIfMissing("draft_submission_obligations", "recovery_ids", "TEXT NOT NULL DEFAULT '[]'");
		this.addColumnIfMissing("draft_submission_obligations", "released_recovery_ids", "TEXT NOT NULL DEFAULT '[]'");
		this.addColumnIfMissing("draft_submission_obligations", "applied_at", "INTEGER");
		// The routine pass prices in unresolved work: a partial index keeps the
		// outstanding scan and its ordering off acknowledged and failed
		// history, which grows for the life of the device. It comes after the
		// migrations because it names applied_at.
		this.db.execSync(`CREATE INDEX IF NOT EXISTS draft_submission_obligations_outstanding
			 ON draft_submission_obligations (created_at)
			 WHERE state = 'open' OR (applied_at IS NULL AND state != 'failed')`);
		// The edge side of the same contract: the outstanding scan rides a
		// partial index in order, the reachability walk reaches edges by their
		// from-endpoint, and a dependency's dependents are named by an index
		// the dependency table's primary key does not lead with. The
		// destination index above already carries the to-endpoint lookups.
		this.db.execSync(`CREATE INDEX IF NOT EXISTS draft_handoff_edges_outstanding
			 ON draft_handoff_edges (created_at)
			 WHERE state != 'cleaned'`);
		this.db.execSync(`CREATE INDEX IF NOT EXISTS draft_handoff_edges_from
			 ON draft_handoff_edges (from_hub_id, from_ref)`);
		this.db.execSync(`CREATE INDEX IF NOT EXISTS draft_handoff_dependencies_dependent
			 ON draft_handoff_dependencies (depends_on_edge_id)`);
	}

	private addColumnIfMissing(table: string, column: string, definition: string): void {
		const columns = this.db.getAllSync<{ name: string }>(`PRAGMA table_info(${table})`);
		if (columns.some((existing) => existing.name === column)) return;
		withSavepoint(this.db, `draft_handoff_add_${table}_${column}`, () => {
			this.db.execSync(`ALTER TABLE ${table} ADD COLUMN ${column} ${definition}`);
		});
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
				for (const obligation of this.continuationObligations(recorded.chain_id, from))
					this.attachObligation(recorded.edge_id, obligation);
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
			const obligations = this.continuationObligations(chainId, from);
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
				if (existing.state !== "pending")
					throw new Error(`handoff edge ${existing.edgeId} is already ${existing.state}`);
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

	recoveryRowOwners(recoveryIds: readonly string[]): DraftHandoffEndpoint[] | undefined {
		if (!recoveryIds.length) return [];
		// The mutationOutboxStorage tables share this database but cannot be
		// imported here without a construction cycle, so the names are spelled
		// out; that module's TABLES constants are their single source of truth.
		let rows: { target_ref: string }[];
		try {
			const placeholders = recoveryIds.map(() => "?").join(", ");
			rows = this.db.getAllSync<{ target_ref: string }>(
				`SELECT DISTINCT target_ref FROM mutation_outbox WHERE client_mutation_id IN (${placeholders})
				 UNION SELECT DISTINCT target_ref FROM mutation_recovery WHERE client_mutation_id IN (${placeholders})`,
				...recoveryIds,
				...recoveryIds,
			);
		} catch {
			// A ledger constructed on a database without the mutation tables has
			// no row ownership to read; the caller falls back to the graph.
			return undefined;
		}
		const owners = new Map<string, DraftHandoffEndpoint>();
		for (const row of rows) {
			const endpoint = decodeDraftEndpoint(row.target_ref);
			if (endpoint !== undefined) owners.set(`${endpoint.hubId}\u0000${endpoint.sessionRef}`, endpoint);
		}
		return [...owners.values()];
	}

	/** The graph's outstanding work, bounded: open and unacknowledged
	 * obligations, the edges pending or still awaiting cleanup, and the edges
	 * reachable from every outstanding obligation's origin. Capture routing
	 * walks the whole path a composition took - including settled and cleaned
	 * edges - while history with nothing outstanding never loads, so routine
	 * composer reconciliation costs what is unresolved, not the device's whole
	 * submission history. A failed resolution retires its obligation from the
	 * load: its capture stays recoverable in the draft record, which routine
	 * work never touches, so it owes no reconciliation - unless an edge still
	 * pending cites it, because an obligation the graph does not know blocks
	 * that edge's planning. The dependencies a pending edge cites load with
	 * it for the same reason. Settled history's deliveries are not included:
	 * nothing in the routine pass reads them. */
	readOutstandingGraph(): DraftHandoffGraph {
		const obligations = this.db
			.getAllSync<ObligationRow>(
				`SELECT * FROM draft_submission_obligations
				 WHERE state = 'open' OR (applied_at IS NULL AND state != 'failed')
				 ORDER BY created_at, rowid`,
			)
			.map((row) => this.mapObligation(row));
		const edges = new Map<string, EdgeRow>();
		for (const row of this.db.getAllSync<EdgeRow>(
			`SELECT * FROM draft_handoff_edges WHERE state != 'cleaned' ORDER BY created_at, rowid`,
		))
			edges.set(row.edge_id, row);
		// Every identity an outstanding obligation reached, through edges of
		// any state: a capture can sit behind an edge that settled and cleaned
		// long before its obligation's completion applied.
		const visited = new Set<string>();
		let frontier = [
			...new Map(
				obligations.map((obligation) => [
					`${obligation.origin.hubId}\u0000${obligation.origin.sessionRef}`,
					obligation.origin,
				]),
			).values(),
		];
		for (const endpoint of frontier) visited.add(`${endpoint.hubId}\u0000${endpoint.sessionRef}`);
		while (frontier.length !== 0) {
			const where: string[] = [];
			const params: string[] = [];
			for (const endpoint of frontier) {
				where.push("(from_hub_id = ? AND from_ref = ?)");
				params.push(endpoint.hubId, endpoint.sessionRef);
			}
			const next: DraftHandoffEndpoint[] = [];
			for (const row of this.db.getAllSync<EdgeRow>(
				`SELECT * FROM draft_handoff_edges WHERE ${where.join(" OR ")} ORDER BY created_at, rowid`,
				...params,
			)) {
				if (!edges.has(row.edge_id)) edges.set(row.edge_id, row);
				const to: DraftHandoffEndpoint = { hubId: row.to_hub_id, sessionRef: row.to_ref };
				const key = `${to.hubId}\u0000${to.sessionRef}`;
				if (!visited.has(key)) {
					visited.add(key);
					next.push(to);
				}
			}
			frontier = next;
		}
		const outstandingEdges = [...edges.values()].map((row) => this.mapEdge(row));
		// Edges still pending or awaiting cleanup can name history the
		// filters above exclude: dependencies that cleaned, whose ids must
		// exist to plan the edges that cite them, and applied or failed
		// obligations they carry - an obligation the ledger never heard of
		// blocks planning, so one that merely resolved must not vanish from
		// the known set. Both loads stay bounded by the edges already
		// included, never by the device's history.
		const edgeIds = new Set(outstandingEdges.map((edge) => edge.edgeId));
		const missingEdgeIds = [
			...new Set(outstandingEdges.flatMap((edge) => edge.dependencies).filter((id) => !edgeIds.has(id))),
		];
		if (missingEdgeIds.length !== 0) {
			const placeholders = missingEdgeIds.map(() => "?").join(", ");
			for (const row of this.db.getAllSync<EdgeRow>(
				`SELECT * FROM draft_handoff_edges WHERE edge_id IN (${placeholders}) ORDER BY created_at, rowid`,
				...missingEdgeIds,
			))
				if (!edgeIds.has(row.edge_id)) {
					edgeIds.add(row.edge_id);
					outstandingEdges.push(this.mapEdge(row));
				}
		}
		const knownObligationIds = new Set(obligations.map((obligation) => obligation.obligationId));
		const missingObligationIds = [
			...new Set(outstandingEdges.flatMap((edge) => edge.obligations).filter((id) => !knownObligationIds.has(id))),
		];
		if (missingObligationIds.length !== 0) {
			const placeholders = missingObligationIds.map(() => "?").join(", ");
			for (const row of this.db.getAllSync<ObligationRow>(
				`SELECT * FROM draft_submission_obligations WHERE obligation_id IN (${placeholders}) ORDER BY created_at, rowid`,
				...missingObligationIds,
			))
				if (!knownObligationIds.has(row.obligation_id)) {
					knownObligationIds.add(row.obligation_id);
					obligations.push(this.mapObligation(row));
				}
		}
		return { edges: outstandingEdges, obligations, deliveries: [] };
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
				 SET state = 'settled', resolved_as = 'settled', claim_at = NULL, delivered_hash = ?, state_changed_at = ?
				 WHERE edge_id = ?`,
				deliveredHash,
				this.#now(),
				edgeId,
			);
		});
	}

	recordDelivered(edgeId: string, deliveredHash: string): void {
		withSavepoint(this.db, "draft_handoff_delivered", () => {
			const edge = this.requirePending(edgeId);
			this.db.runSync(
				`UPDATE draft_handoff_edges SET delivered_hash = ?, state_changed_at = ? WHERE edge_id = ?`,
				deliveredHash,
				this.#now(),
				edge.edgeId,
			);
		});
	}

	recordComposed(edgeId: string): void {
		withSavepoint(this.db, "draft_handoff_composed", () => {
			const edge = this.requirePending(edgeId);
			this.db.runSync(
				`UPDATE draft_handoff_edges SET composed_at = ?, state_changed_at = ? WHERE edge_id = ?`,
				this.#now(),
				this.#now(),
				edge.edgeId,
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
				 SET state = 'refused', resolved_as = 'refused', claim_at = NULL, state_changed_at = ?
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

	/** A removed hub's drafts are gone, so its edges can never deliver, settle,
	 * or refuse. Their deliveries and dependency rows leave with them;
	 * obligations survive, because their dispositions can still be owed to
	 * identities in other hubs. */
	removeHub(hubId: string): void {
		withSavepoint(this.db, "draft_handoff_remove_hub", () => {
			this.db.runSync(
				`DELETE FROM draft_handoff_deliveries WHERE hub_id = ? OR edge_id IN
				 (SELECT edge_id FROM draft_handoff_edges WHERE from_hub_id = ? OR to_hub_id = ?)`,
				hubId,
				hubId,
				hubId,
			);
			this.db.runSync(
				`DELETE FROM draft_handoff_dependencies WHERE edge_id IN
				 (SELECT edge_id FROM draft_handoff_edges WHERE from_hub_id = ? OR to_hub_id = ?)
				 OR depends_on_edge_id IN
				 (SELECT edge_id FROM draft_handoff_edges WHERE from_hub_id = ? OR to_hub_id = ?)`,
				hubId,
				hubId,
				hubId,
				hubId,
			);
			this.db.runSync(
				`DELETE FROM draft_handoff_edge_obligations WHERE edge_id IN
				 (SELECT edge_id FROM draft_handoff_edges WHERE from_hub_id = ? OR to_hub_id = ?)`,
				hubId,
				hubId,
			);
			this.db.runSync(`DELETE FROM draft_handoff_edges WHERE from_hub_id = ? OR to_hub_id = ?`, hubId, hubId);
		});
	}

	/** Durably acknowledge a resolved obligation whose completion has been
	 * applied, so routine reconciliation stops revisiting it. Only a resolved
	 * obligation can be acknowledged: an open one still owes its decision. */
	acknowledgeAppliedCompletion(obligationId: string): void {
		withSavepoint(this.db, "draft_handoff_ack_completion", () => {
			this.db.runSync(
				`UPDATE draft_submission_obligations SET applied_at = ?
				 WHERE obligation_id = ? AND state != 'open' AND applied_at IS NULL`,
				this.#now(),
				obligationId,
			);
		});
	}

	beginSubmission(obligationId: string, origin: DraftHandoffEndpoint, recoveryIds: readonly string[] = []): void {
		withSavepoint(this.db, "draft_handoff_begin_submission", () => {
			this.db.runSync(
				`INSERT OR IGNORE INTO draft_submission_obligations
				 (obligation_id, origin_hub_id, origin_ref, state, recovery_ids, completed_command_ids, created_at)
				 VALUES (?, ?, ?, 'open', ?, '[]', ?)`,
				obligationId,
				origin.hubId,
				origin.sessionRef,
				JSON.stringify([...new Set(recoveryIds)]),
				this.#now(),
			);
			// A begin the document retried after storage recovered can arrive
			// once its origin already recorded a handoff: the pending edges from
			// this origin would carry the submission's composition, so they must
			// wait for its resolution like an edge created after the begin would.
			this.db.runSync(
				`INSERT OR IGNORE INTO draft_handoff_edge_obligations (edge_id, obligation_id)
				 SELECT edge_id, ? FROM draft_handoff_edges
				 WHERE from_hub_id = ? AND from_ref = ? AND state = 'pending'`,
				obligationId,
				origin.hubId,
				origin.sessionRef,
			);
			// Ride the chain of the edge that carries it, so later continuations
			// adopt the obligation too.
			this.db.runSync(
				`UPDATE draft_submission_obligations
				 SET chain_id = COALESCE((
					 SELECT edge.chain_id FROM draft_handoff_edges edge
					 JOIN draft_handoff_edge_obligations link ON link.edge_id = edge.edge_id
					 WHERE link.obligation_id = ? LIMIT 1), chain_id)
				 WHERE obligation_id = ? AND chain_id IS NULL`,
				obligationId,
				obligationId,
			);
		});
	}

	resolveSubmission(
		obligationId: string,
		state: Exclude<DraftSubmissionState, "open">,
		completedCommandIds: readonly string[] = [],
		releasedRecoveryIds: readonly string[] = [],
	): void {
		withSavepoint(this.db, "draft_handoff_resolve_submission", () => {
			const existing = this.obligation(obligationId);
			if (existing === undefined) return;
			const completed = [...new Set([...existing.completedCommandIds, ...completedCommandIds])];
			const released = [...new Set([...existing.releasedRecoveryIds, ...releasedRecoveryIds])];
			if (existing.state === "open") {
				this.db.runSync(
					`UPDATE draft_submission_obligations
					 SET state = ?, completed_command_ids = ?, released_recovery_ids = ?, resolved_at = ?
					 WHERE obligation_id = ?`,
					state,
					JSON.stringify(completed),
					JSON.stringify(released),
					this.#now(),
					obligationId,
				);
				return;
			}
			// A retried completion may carry ids the first write missed; the state
			// and resolution time stay the first one's.
			if (
				existing.state === state &&
				(completed.length !== existing.completedCommandIds.length ||
					released.length !== existing.releasedRecoveryIds.length)
			)
				this.db.runSync(
					`UPDATE draft_submission_obligations SET completed_command_ids = ?, released_recovery_ids = ? WHERE obligation_id = ?`,
					JSON.stringify(completed),
					JSON.stringify(released),
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

	/** Open obligations a continuation from `from` must inherit: its own
	 * chain's and its origin's, plus one from every inbound edge whose
	 * recovery rows durably continue through `from`. Converging chains can
	 * deliver a running command's composition here while the continuation
	 * selects another chain, and without the inherited obligation the
	 * continuation could settle and clean while the command still runs,
	 * leaving the terminal without the pending handoff that keeps its row
	 * from restoring. Inherited obligations keep their own chain: the
	 * attachment waits for them, the chain still routes them. */
	private continuationObligations(chainId: string, from: DraftHandoffEndpoint): string[] {
		const own = this.openObligations(chainId, from);
		for (const obligation of own) this.stampChain(obligation, chainId);
		const inherited = new Set(own);
		for (const inbound of this.db.getAllSync<{ edge_id: string }>(
			`SELECT edge_id FROM draft_handoff_edges
			 WHERE to_hub_id = ? AND to_ref = ? AND state != 'cleaned' ORDER BY created_at, rowid`,
			from.hubId,
			from.sessionRef,
		)) {
			for (const row of this.db.getAllSync<{ obligation_id: string }>(
				`SELECT obligation_id FROM draft_handoff_edge_obligations WHERE edge_id = ? ORDER BY rowid`,
				inbound.edge_id,
			)) {
				if (inherited.has(row.obligation_id)) continue;
				const obligation = this.obligation(row.obligation_id);
				if (obligation === undefined || obligation.state !== "open") continue;
				// Only rows that durably continue through this destination ride
				// the continuation: the command's recovery rows follow the
				// retargets, and the terminal they target is the one at risk.
				const owners = this.recoveryRowOwners(obligation.recoveryIds);
				if (owners === undefined) continue;
				if (!owners.some((owner) => owner.hubId === from.hubId && owner.sessionRef === from.sessionRef)) continue;
				inherited.add(row.obligation_id);
			}
		}
		return [...inherited];
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
			composedAt: row.composed_at ?? null,
			resolvedAs: row.resolved_as ?? null,
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
			recoveryIds: JSON.parse(row.recovery_ids) as string[],
			completedCommandIds: JSON.parse(row.completed_command_ids) as string[],
			releasedRecoveryIds: JSON.parse(row.released_recovery_ids) as string[],
			createdAt: row.created_at,
			resolvedAt: row.resolved_at,
			appliedAt: row.applied_at ?? null,
		};
	}
}
