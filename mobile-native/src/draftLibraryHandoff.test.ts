import { afterEach, describe, expect, test, vi } from "vitest";
import type { DraftHandoffEdge, DraftHandoffEndpoint, DraftHandoffLedger } from "./draftHandoffLedger";
import { DraftHandoffLedgerSQLite } from "./draftHandoffLedgerSQLite";
import { DraftLibrary } from "./draftLibrary";
import { type DraftDestination, DraftRepository } from "./draftRepository";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "./sqliteSync.testkit";

const databases: SqliteDoubleDatabase[] = [];
afterEach(() => {
	for (const db of databases.splice(0)) db.close();
});

const endpoint = (sessionRef: string): DraftHandoffEndpoint => ({ hubId: "hub", sessionRef });
const destination = (sessionRef: string): DraftDestination => ({ hubId: "hub", sessionRef });

function setup(): {
	ledger: DraftHandoffLedgerSQLite;
	ledgerDatabase: SqliteDoubleDatabase;
	repository: DraftRepository;
	draftDatabase: SqliteDoubleDatabase;
	library: (accessor?: () => DraftHandoffLedger) => DraftLibrary;
} {
	const drafts = openSqliteSyncDouble();
	databases.push(drafts.database);
	const mutations = openSqliteSyncDouble();
	databases.push(mutations.database);
	let next = 0;
	const ledger = new DraftHandoffLedgerSQLite(mutations.port, {
		now: () => 1000,
		createId: () => `id-${++next}`,
	});
	const repository = new DraftRepository(drafts.port);
	return {
		ledger,
		ledgerDatabase: mutations.database,
		repository,
		draftDatabase: drafts.database,
		library: (accessor = () => ledger) => new DraftLibrary(() => repository, undefined, accessor),
	};
}

/** Fails the named ledger method a fixed number of times, then delegates. */
function flaky(ledger: DraftHandoffLedger, method: keyof DraftHandoffLedger, times: number): DraftHandoffLedger {
	let remaining = times;
	return new Proxy(ledger, {
		get(target, property) {
			const value = Reflect.get(target, property, target);
			if (property === method && typeof value === "function")
				return (...args: unknown[]) => {
					if (remaining-- > 0) throw new Error(`injected ${String(method)} failure`);
					return (value as (...inner: unknown[]) => unknown).apply(target, args);
				};
			return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
		},
	}) as DraftHandoffLedger;
}

function edgeFor(ledger: DraftHandoffLedger, from: string, to: string): DraftHandoffEdge | undefined {
	return ledger.readGraph().edges.find((edge) => edge.from.sessionRef === from && edge.to.sessionRef === to);
}

describe("durable handoff library", () => {
	test("settles an accepted hop and records its deliveries", () => {
		const { ledger, repository, library } = setup();
		repository.write(destination("a"), {
			draft: "edited",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
			completedCommandRecoveryIds: ["completed-a"],
			releasedRecoveryIds: ["released-a"],
		});
		expect(library().handoff(destination("a"), destination("b"))).toBe(true);
		const edge = edgeFor(ledger, "a", "b");
		expect(edge).toMatchObject({ state: "settled", claimAt: null });
		expect(edge?.deliveredHash).not.toBeNull();
		expect(ledger.readGraph().deliveries).toEqual(
			expect.arrayContaining([
				expect.objectContaining({ destination: endpoint("b"), recoveryId: "completed-a", disposition: "completed" }),
				expect.objectContaining({ destination: endpoint("b"), recoveryId: "recovered-a", disposition: "released" }),
				expect.objectContaining({ destination: endpoint("b"), recoveryId: "released-a", disposition: "released" }),
			]),
		);
		expect(repository.read(destination("a"))).toEqual({ draft: "", unconfirmed: null });
		expect(repository.read(destination("b"))).toMatchObject({ draft: "edited" });
	});

	test("refuses an occupied destination, delivers exclusions, and keeps both drafts", () => {
		const { ledger, repository, library } = setup();
		repository.write(destination("a"), {
			draft: "mine",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		repository.write(destination("b"), { draft: "theirs", unconfirmed: null });

		expect(library().handoff(destination("a"), destination("b"))).toBe(false);
		expect(edgeFor(ledger, "a", "b")).toMatchObject({ state: "refused" });
		expect(repository.read(destination("a")).draft).toBe("mine");
		expect(repository.read(destination("b"))).toMatchObject({
			draft: "theirs",
			releasedRecoveryIds: ["recovered-a"],
		});
		expect(ledger.readGraph().deliveries).toContainEqual(
			expect.objectContaining({ destination: endpoint("b"), recoveryId: "recovered-a", disposition: "released" }),
		);
	});

	test("refuses when another chain already owns the destination", () => {
		const { ledger, repository, library } = setup();
		const owner = ledger.createEdge({ chainId: "other", from: endpoint("x"), to: endpoint("b") });
		ledger.claimDestination(owner.edgeId);
		ledger.settleEdge(owner.edgeId, [], "owner");
		repository.write(destination("a"), { draft: "mine", unconfirmed: null });

		expect(library().handoff(destination("a"), destination("b"))).toBe(false);
		expect(edgeFor(ledger, "a", "b")).toMatchObject({ state: "refused" });
		expect(repository.read(destination("a")).draft).toBe("mine");
		expect(repository.read(destination("b")).draft).toBe("");
	});

	test("cleans settled and refused edges on retry, and a second retry is a no-op", () => {
		const { ledger, repository, library } = setup();
		repository.write(destination("a"), { draft: "edited", unconfirmed: null });
		const draftLibrary = library();
		expect(draftLibrary.handoff(destination("a"), destination("b"))).toBe(true);
		draftLibrary.retryPendingHandoffs();
		expect(edgeFor(ledger, "a", "b")).toMatchObject({ state: "cleaned" });
		const state = ledger.readGraph();
		draftLibrary.retryPendingHandoffs();
		expect(ledger.readGraph().edges).toEqual(state.edges);
		expect(ledger.readGraph().deliveries).toEqual(state.deliveries);
		expect(repository.read(destination("b"))).toMatchObject({ draft: "edited" });
	});

	test("a claim failure leaves the edge pending and a retry settles it", () => {
		const { ledger, repository, library } = setup();
		repository.write(destination("a"), { draft: "edited", unconfirmed: null });
		let failures = 1;
		const flakyLedger = new Proxy(ledger, {
			get(target, property) {
				const value = Reflect.get(target, property, target);
				if (property === "claimDestination" && typeof value === "function")
					return (...args: unknown[]) => {
						if (failures-- > 0) throw new Error("injected claim failure");
						return (value as (...inner: unknown[]) => unknown).apply(target, args);
					};
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as DraftHandoffLedger;
		const draftLibrary = library(() => flakyLedger);

		expect(draftLibrary.handoff(destination("a"), destination("b"))).toBe(false);
		expect(edgeFor(ledger, "a", "b")?.state).toBe("pending");
		expect(repository.read(destination("a")).draft).toBe("edited");
		draftLibrary.retryPendingHandoffs();
		expect(edgeFor(ledger, "a", "b")?.state).toBe("settled");
		expect(repository.read(destination("b"))).toMatchObject({ draft: "edited" });
	});

	test("a drafts write failure leaves the edge pending and the source draft intact", () => {
		const { ledger, repository, draftDatabase, library } = setup();
		repository.write(destination("a"), { draft: "edited", unconfirmed: null });
		draftDatabase.exec(
			"CREATE TRIGGER reject_b BEFORE INSERT ON drafts WHEN NEW.session_ref = 'b' BEGIN SELECT RAISE(ABORT, 'full'); END",
		);
		const draftLibrary = library();

		expect(draftLibrary.handoff(destination("a"), destination("b"))).toBe(false);
		// The delivered hash records the intended write before the compose; the
		// source still holds it, so a retry composes again.
		expect(edgeFor(ledger, "a", "b")?.state).toBe("pending");
		expect(edgeFor(ledger, "a", "b")?.deliveredHash).not.toBeNull();
		expect(repository.read(destination("a")).draft).toBe("edited");
		draftDatabase.exec("DROP TRIGGER reject_b");
		draftLibrary.retryPendingHandoffs();
		expect(edgeFor(ledger, "a", "b")?.state).toBe("settled");
	});

	test("a delivered-hash write failure leaves the source to retry the compose", () => {
		const { ledger, repository, library } = setup();
		repository.write(destination("a"), { draft: "edited", unconfirmed: null });
		const flakyLedger = flaky(ledger, "recordDelivered", 1);
		const draftLibrary = library(() => flakyLedger);

		expect(draftLibrary.handoff(destination("a"), destination("b"))).toBe(false);
		expect(edgeFor(ledger, "a", "b")).toMatchObject({ state: "pending", deliveredHash: null });
		expect(repository.read(destination("a")).draft).toBe("edited");
		expect(repository.read(destination("b")).draft).toBe("");
		// The next retry uses the real ledger and settles.
		draftLibrary.retryPendingHandoffs();
		expect(edgeFor(ledger, "a", "b")?.state).toBe("settled");
		expect(repository.read(destination("b"))).toMatchObject({ draft: "edited" });
	});

	test("a settlement failure is retried from the recorded hash without moving drafts again", () => {
		const { ledger, repository, library } = setup();
		repository.write(destination("a"), { draft: "edited", unconfirmed: null });
		const flakyLedger = flaky(ledger, "settleEdge", 1);
		const draftLibrary = library(() => flakyLedger);

		expect(draftLibrary.handoff(destination("a"), destination("b"))).toBe(false);
		expect(edgeFor(ledger, "a", "b")).toMatchObject({ state: "pending" });
		expect(edgeFor(ledger, "a", "b")?.deliveredHash).not.toBeNull();
		expect(repository.read(destination("a"))).toEqual({ draft: "", unconfirmed: null });
		const transfer = vi.spyOn(repository, "transfer");
		draftLibrary.retryPendingHandoffs();
		expect(edgeFor(ledger, "a", "b")?.state).toBe("settled");
		expect(transfer).not.toHaveBeenCalled();
		expect(repository.read(destination("b"))).toMatchObject({ draft: "edited" });
	});

	test("a destination edited after a failed settlement is refused on retry", () => {
		const { ledger, repository, library } = setup();
		repository.write(destination("a"), {
			draft: "edited",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		const flakyLedger = flaky(ledger, "settleEdge", 1);
		const draftLibrary = library(() => flakyLedger);
		expect(draftLibrary.handoff(destination("a"), destination("b"))).toBe(false);
		expect(edgeFor(ledger, "a", "b")).toMatchObject({ state: "pending" });

		// The destination gains work after this edge wrote its own record.
		draftLibrary.open(destination("b")).edit("typed later");
		draftLibrary.retryPendingHandoffs();
		expect(edgeFor(ledger, "a", "b")).toMatchObject({ state: "refused" });
		expect(repository.read(destination("b")).draft).toBe("typed later");
		expect(repository.read(destination("b")).releasedRecoveryIds).toEqual(["recovered-a"]);
	});
});
