import { afterEach, describe, expect, test } from "vitest";
import type { DraftHandoffDelivery, DraftHandoffEndpoint } from "./draftHandoffLedger";
import { DraftHandoffLedgerSQLite } from "./draftHandoffLedgerSQLite";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "./sqliteSync.testkit";

const databases: SqliteDoubleDatabase[] = [];
afterEach(() => {
	for (const db of databases.splice(0)) db.close();
});

const a: DraftHandoffEndpoint = { hubId: "hub", sessionRef: "a" };
const b: DraftHandoffEndpoint = { hubId: "hub", sessionRef: "b" };
const c: DraftHandoffEndpoint = { hubId: "hub", sessionRef: "c" };

function setup() {
	const { database, port } = openSqliteSyncDouble();
	databases.push(database);
	let next = 0;
	const ledger = new DraftHandoffLedgerSQLite(port, {
		now: () => 1000,
		// Chains and edges draw from the same counter, so ids are stable and
		// unique without a host UUID.
		createId: () => `id-${++next}`,
	});
	return { database, port, ledger };
}

function delivery(edgeId: string, recoveryId: string, disposition: "released" | "completed"): DraftHandoffDelivery {
	return { chainId: "chain", destination: b, recoveryId, disposition, edgeId };
}

describe("draft handoff ledger", () => {
	test("creates the five tables idempotently and keeps their rows", () => {
		const { database, port, ledger } = setup();
		const tables = (database.prepare("SELECT name FROM sqlite_master WHERE type = 'table'").all() as { name: string }[])
			.map((row) => row.name)
			.filter((name) => name.startsWith("draft_"))
			.sort();
		expect(tables).toEqual([
			"draft_handoff_deliveries",
			"draft_handoff_dependencies",
			"draft_handoff_edge_obligations",
			"draft_handoff_edges",
			"draft_submission_obligations",
		]);
		ledger.beginSubmission("obligation-1", a);
		// A second construction on the same database creates nothing and keeps
		// every row.
		const again = new DraftHandoffLedgerSQLite(port, { now: () => 2000, createId: () => "unused" });
		expect(again.readGraph().obligations).toMatchObject([
			{ obligationId: "obligation-1", origin: a, state: "open", completedCommandIds: [] },
		]);
	});

	test("round-trips edges, dependencies, obligations, and deliveries", () => {
		const { ledger } = setup();
		ledger.beginSubmission("obligation-1", a);
		const first = ledger.createEdge({ chainId: "chain", from: a, to: b });
		const second = ledger.createEdge({
			chainId: "chain",
			from: b,
			to: c,
			dependencies: [first.edgeId],
			obligations: ["obligation-1"],
		});
		expect(second).toMatchObject({
			chainId: "chain",
			from: b,
			to: c,
			state: "pending",
			dependencies: [first.edgeId],
			obligations: ["obligation-1"],
			claimAt: null,
			deliveredHash: null,
		});
		expect(ledger.readGraph()).toMatchObject({
			edges: [{ edgeId: first.edgeId, state: "pending" }, { edgeId: second.edgeId }],
			obligations: [{ obligationId: "obligation-1", chainId: null, state: "open" }],
			deliveries: [],
		});
	});

	test("claims one destination per live edge and frees it on refusal", () => {
		const { ledger } = setup();
		const owner = ledger.createEdge({ chainId: "one", from: a, to: b });
		const rival = ledger.createEdge({ chainId: "two", from: c, to: b });
		expect(ledger.claimDestination(owner.edgeId)).toBe(true);
		// A retry of the same edge keeps its claim.
		expect(ledger.claimDestination(owner.edgeId)).toBe(true);
		expect(ledger.claimDestination(rival.edgeId)).toBe(false);
		// A refused edge keeps no ownership, so the destination is claimable.
		ledger.refuseEdge(owner.edgeId, []);
		expect(ledger.claimDestination(rival.edgeId)).toBe(true);
		// A settled edge does own it.
		const later = ledger.createEdge({ chainId: "three", from: a, to: b });
		ledger.settleEdge(rival.edgeId, [], "hash");
		expect(ledger.claimDestination(later.edgeId)).toBe(false);
	});

	test("settles only with deliveries and a resolved obligation set", () => {
		const { database, ledger } = setup();
		// The edge carries no disposition copies: the delivery rows are the one
		// authoritative set.
		const columns = (database.prepare("PRAGMA table_info(draft_handoff_edges)").all() as { name: string }[]).map(
			(column) => column.name,
		);
		expect(columns).not.toContain("released_recovery_ids");
		expect(columns).not.toContain("completed_command_ids");

		ledger.beginSubmission("obligation-1", a);
		const edge = ledger.createEdge({ chainId: "chain", from: a, to: b, obligations: ["obligation-1"] });
		expect(() => ledger.settleEdge(edge.edgeId, [], "hash")).toThrow(/open obligation/);
		expect(() => ledger.refuseEdge(edge.edgeId, [])).toThrow(/open obligation/);
		expect(ledger.readGraph().edges[0]).toMatchObject({ state: "pending", deliveredHash: null });

		ledger.resolveSubmission("obligation-1", "completed", ["recovered-1"]);
		expect(ledger.claimDestination(edge.edgeId)).toBe(true);
		ledger.settleEdge(edge.edgeId, [delivery(edge.edgeId, "recovered-1", "completed")], "hash");
		expect(ledger.readGraph()).toMatchObject({
			edges: [{ edgeId: edge.edgeId, state: "settled", deliveredHash: "hash", claimAt: null }],
			deliveries: [
				{ chainId: "chain", destination: b, recoveryId: "recovered-1", disposition: "completed", edgeId: edge.edgeId },
			],
		});
		expect(() => ledger.settleEdge(edge.edgeId, [], "again")).toThrow(/settled/);
	});

	test("rolls a settlement back when a delivery write fails", () => {
		const { database, ledger } = setup();
		const edge = ledger.createEdge({ chainId: "chain", from: a, to: b });
		database.exec(
			"CREATE TRIGGER reject_delivery BEFORE INSERT ON draft_handoff_deliveries BEGIN SELECT RAISE(ABORT, 'reject'); END",
		);
		expect(() => ledger.settleEdge(edge.edgeId, [delivery(edge.edgeId, "recovered-1", "released")], "hash")).toThrow(
			/reject/,
		);
		expect(ledger.readGraph()).toMatchObject({
			edges: [{ edgeId: edge.edgeId, state: "pending", deliveredHash: null }],
			deliveries: [],
		});
		database.exec("DROP TRIGGER reject_delivery");
		ledger.settleEdge(edge.edgeId, [delivery(edge.edgeId, "recovered-1", "released")], "hash");
		expect(ledger.readGraph().edges[0].state).toBe("settled");
	});

	test("records the delivered hash before settlement", () => {
		const { ledger } = setup();
		const edge = ledger.createEdge({ chainId: "chain", from: a, to: b });
		ledger.claimDestination(edge.edgeId);
		ledger.recordDelivered(edge.edgeId, "fingerprint");
		expect(ledger.readGraph().edges[0]).toMatchObject({ state: "pending", deliveredHash: "fingerprint" });
		ledger.settleEdge(edge.edgeId, [], "fingerprint");
		expect(ledger.readGraph().edges[0]).toMatchObject({ state: "settled", deliveredHash: "fingerprint" });
	});

	test("migrates a table created before the composed marker", () => {
		const { database, port } = openSqliteSyncDouble();
		databases.push(database);
		// The shape an earlier build of this branch created: no composed_at and
		// no obligation delivery columns.
		database.exec(`CREATE TABLE draft_handoff_edges (
			edge_id TEXT PRIMARY KEY, chain_id TEXT NOT NULL,
			from_hub_id TEXT NOT NULL, from_ref TEXT NOT NULL,
			to_hub_id TEXT NOT NULL, to_ref TEXT NOT NULL,
			state TEXT NOT NULL, claim_at INTEGER, delivered_hash TEXT, superseded_by TEXT,
			created_at INTEGER NOT NULL, state_changed_at INTEGER NOT NULL)`);
		database.exec(`CREATE TABLE draft_submission_obligations (
			obligation_id TEXT PRIMARY KEY, chain_id TEXT,
			origin_hub_id TEXT NOT NULL, origin_ref TEXT NOT NULL,
			state TEXT NOT NULL, completed_command_ids TEXT NOT NULL DEFAULT '[]',
			created_at INTEGER NOT NULL, resolved_at INTEGER)`);
		database
			.prepare(
				`INSERT INTO draft_handoff_edges
				 (edge_id, chain_id, from_hub_id, from_ref, to_hub_id, to_ref, state, created_at, state_changed_at)
				 VALUES ('old', 'chain', 'hub', 'a', 'hub', 'b', 'pending', 1, 1)`,
			)
			.run();

		const ledger = new DraftHandoffLedgerSQLite(port, { now: () => 1000, createId: () => "new" });
		expect(ledger.readGraph().edges[0]).toMatchObject({ edgeId: "old", composedAt: null });
		// Both the new column and the obligation delivery columns must be usable.
		ledger.recordComposed("old");
		ledger.beginSubmission("obligation-1", a, ["recovered-a"]);
		expect(ledger.readGraph().edges[0].composedAt).toBe(1000);
		expect(ledger.readGraph().obligations).toMatchObject([
			{ obligationId: "obligation-1", recoveryIds: ["recovered-a"], releasedRecoveryIds: [] },
		]);
	});

	test("sets the downstream edge before its predecessor", () => {
		const { ledger } = setup();
		const downstream = ledger.createEdge({ chainId: "chain", from: b, to: c });
		const upstream = ledger.createEdge({ chainId: "chain", from: a, to: b, dependencies: [downstream.edgeId] });
		expect(() => ledger.settleEdge(upstream.edgeId, [], "hash")).toThrow(/waits on/);
		expect(() => ledger.refuseEdge(upstream.edgeId, [])).toThrow(/waits on/);
		ledger.settleEdge(downstream.edgeId, [], "hash");
		ledger.settleEdge(upstream.edgeId, [], "hash");
		expect(ledger.readGraph().edges.map((edge) => edge.state)).toEqual(["settled", "settled"]);
	});

	test("cleans only settled edges without a pending dependent", () => {
		const { ledger } = setup();
		const downstream = ledger.createEdge({ chainId: "chain", from: b, to: c });
		const upstream = ledger.createEdge({ chainId: "chain", from: a, to: b, dependencies: [downstream.edgeId] });
		expect(() => ledger.cleanEdge(upstream.edgeId)).toThrow(/pending/);
		ledger.settleEdge(downstream.edgeId, [], "hash");
		expect(() => ledger.cleanEdge(downstream.edgeId)).toThrow(/pending dependent/);
		ledger.settleEdge(upstream.edgeId, [], "hash");
		ledger.cleanEdge(downstream.edgeId);
		ledger.cleanEdge(downstream.edgeId);
		expect(ledger.readGraph().edges[0].state).toBe("cleaned");
	});

	test("resolves an obligation once and keeps later completion ids", () => {
		const { ledger } = setup();
		ledger.beginSubmission("obligation-1", a);
		ledger.resolveSubmission("obligation-1", "completed", ["recovered-1"]);
		ledger.resolveSubmission("obligation-1", "completed", ["recovered-2"]);
		ledger.resolveSubmission("obligation-1", "failed");
		expect(ledger.readGraph().obligations).toMatchObject([
			{ obligationId: "obligation-1", state: "completed", completedCommandIds: ["recovered-1", "recovered-2"] },
		]);
		// An unknown obligation is not a crash: a document may run without one.
		expect(() => ledger.resolveSubmission("missing", "completed")).not.toThrow();
	});

	test("adopts an unclaimed submission at the first identity change", () => {
		const { ledger } = setup();
		ledger.beginSubmission("obligation-1", a);
		const first = ledger.recordIdentityChange(a, b);
		let graph = ledger.readGraph();
		expect(graph.edges).toMatchObject([
			{ edgeId: first.edgeId, chainId: first.chainId, from: a, to: b, obligations: ["obligation-1"] },
		]);
		expect(graph.obligations[0].chainId).toBe(first.chainId);
		// The stamped chain lets the next hop carry the same obligation.
		const second = ledger.recordIdentityChange(b, c);
		graph = ledger.readGraph();
		expect(second.chainId).toBe(first.chainId);
		expect(graph.edges.find((edge) => edge.edgeId === second.edgeId)?.obligations).toEqual(["obligation-1"]);
	});

	test("reuses the chain and idempotently records one identity change", () => {
		const { ledger } = setup();
		const first = ledger.recordIdentityChange(a, b);
		expect(ledger.recordIdentityChange(a, b)).toEqual(first);
		const second = ledger.recordIdentityChange(b, c);
		expect(second.chainId).toBe(first.chainId);
		const graph = ledger.readGraph();
		expect(graph.edges).toHaveLength(2);
		expect(graph.edges.find((edge) => edge.edgeId === second.edgeId)?.dependencies).toEqual([]);
		expect(graph.edges.find((edge) => edge.edgeId === first.edgeId)?.dependencies).toEqual([second.edgeId]);
	});

	test("stamps an obligation that starts after its edge was recorded", () => {
		const { ledger } = setup();
		const first = ledger.recordIdentityChange(a, b);
		// The submission starts after the edge exists, so its obligation has no
		// chain yet. The same identity change is recorded again.
		ledger.beginSubmission("obligation-1", a);
		expect(ledger.recordIdentityChange(a, b)).toEqual(first);
		const second = ledger.recordIdentityChange(b, c);
		const graph = ledger.readGraph();
		expect(graph.obligations[0].chainId).toBe(first.chainId);
		expect(graph.edges.find((edge) => edge.edgeId === second.edgeId)?.obligations).toEqual(["obligation-1"]);
	});

	test("re-records an identity change after its edge settled or was cleaned", () => {
		const { ledger } = setup();
		const first = ledger.recordIdentityChange(a, b);
		ledger.settleEdge(first.edgeId, [], "hash");
		const second = ledger.recordIdentityChange(a, b);
		expect(second.edgeId).not.toBe(first.edgeId);
		expect(ledger.readGraph().edges.find((edge) => edge.edgeId === second.edgeId)?.state).toBe("pending");
		ledger.refuseEdge(second.edgeId, []);
		ledger.cleanEdge(second.edgeId);
		ledger.cleanEdge(first.edgeId);
		const third = ledger.recordIdentityChange(a, b);
		expect(third.edgeId).not.toBe(first.edgeId);
		expect(() => ledger.settleEdge(third.edgeId, [], "hash")).not.toThrow();
	});

	test("merges dependencies and obligations passed for an existing edge", () => {
		const { ledger } = setup();
		ledger.beginSubmission("obligation-1", a);
		const edge = ledger.createEdge({ chainId: "chain", from: a, to: b });
		const merged = ledger.createEdge({
			chainId: "chain",
			from: a,
			to: b,
			obligations: ["obligation-1"],
		});
		expect(merged.edgeId).toBe(edge.edgeId);
		expect(merged.obligations).toEqual(["obligation-1"]);
	});
});
