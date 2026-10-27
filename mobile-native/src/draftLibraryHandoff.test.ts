import { afterEach, describe, expect, test, vi } from "vitest";
import type { DraftHandoffEdge, DraftHandoffEndpoint, DraftHandoffLedger } from "./draftHandoffLedger";
import { DraftHandoffLedgerSQLite } from "./draftHandoffLedgerSQLite";
import { DraftLibrary } from "./draftLibrary";
import { terminalDestination } from "./draftHandoffGraph";
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

	test("a refusal defers while the destination cannot load its saved record", () => {
		const { ledger, repository, draftDatabase, library } = setup();
		// Another chain already owns the destination, so the hop is refused.
		const owner = ledger.createEdge({ chainId: "other", from: endpoint("x"), to: endpoint("b") });
		ledger.claimDestination(owner.edgeId);
		ledger.settleEdge(owner.edgeId, [], "owner");
		repository.write(destination("a"), {
			draft: "mine",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		// The destination holds work, but its record cannot load: a parked
		// composition references an image whose bytes are gone.
		draftDatabase.exec(
			`INSERT INTO drafts (hub_id, session_ref, draft, unconfirmed, parked_composition)
			 VALUES ('hub', 'b', 'theirs', NULL,
			 '[{"id":"park","text":"parked","images":[{"id":"missing","marker":1,"mediaType":"image/png"}]}]')`,
		);
		expect(() => repository.read(destination("b"))).toThrow(/Saved image is unavailable/);

		const draftLibrary = library();
		expect(draftLibrary.handoff(destination("a"), destination("b"))).toBe(false);
		// The refusal defers instead of merging dispositions through the
		// destination's unreadable record, so no work is replaced.
		expect(edgeFor(ledger, "a", "b")?.state).toBe("pending");
		expect(repository.read(destination("a")).draft).toBe("mine");

		// The record becomes readable again: the refusal proceeds.
		draftDatabase.exec("UPDATE drafts SET parked_composition = NULL WHERE hub_id = 'hub' AND session_ref = 'b'");
		draftLibrary.retryPendingHandoffs();
		expect(edgeFor(ledger, "a", "b")?.state).toBe("refused");
		expect(repository.read(destination("b"))).toMatchObject({
			draft: "theirs",
			releasedRecoveryIds: ["recovered-a"],
		});
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

	test("a retry does not settle an uncomposed edge whose capture never transferred", () => {
		const { ledger, repository, draftDatabase, library } = setup();
		// A source holding only an in-flight capture fingerprints exactly like
		// an empty destination: the composition fingerprint excludes unconfirmed
		// content and completion markers.
		repository.write(destination("a"), {
			draft: "",
			unconfirmed: "in flight",
			unconfirmedCaptureToken: "token-1",
		});
		draftDatabase.exec(
			"CREATE TRIGGER reject_b BEFORE INSERT ON drafts WHEN NEW.session_ref = 'b' BEGIN SELECT RAISE(ABORT, 'full'); END",
		);
		const draftLibrary = library();

		expect(draftLibrary.handoff(destination("a"), destination("b"))).toBe(false);
		expect(edgeFor(ledger, "a", "b")?.deliveredHash).not.toBeNull();
		draftDatabase.exec("DROP TRIGGER reject_b");

		draftLibrary.retryPendingHandoffs();
		const edge = edgeFor(ledger, "a", "b");
		expect(edge?.state).toBe("settled");
		expect(edge?.composedAt).not.toBeNull();
		expect(repository.read(destination("b"))).toMatchObject({
			unconfirmed: "in flight",
			unconfirmedCaptureToken: "token-1",
		});
		expect(repository.read(destination("a"))).toEqual({ draft: "", unconfirmed: null });
	});

	test("a retry re-delivers completion markers a failed transfer dropped", () => {
		const { ledger, repository, draftDatabase, library } = setup();
		repository.write(destination("a"), {
			draft: "",
			unconfirmed: null,
			composer: { skillNames: ["review"] },
			completedCommandRecoveryIds: ["done-1"],
			releasedRecoveryIds: ["rel-1"],
		});
		draftDatabase.exec(
			"CREATE TRIGGER reject_b BEFORE INSERT ON drafts WHEN NEW.session_ref = 'b' BEGIN SELECT RAISE(ABORT, 'full'); END",
		);
		const draftLibrary = library();

		expect(draftLibrary.handoff(destination("a"), destination("b"))).toBe(false);
		expect(edgeFor(ledger, "a", "b")?.deliveredHash).not.toBeNull();
		draftDatabase.exec("DROP TRIGGER reject_b");

		draftLibrary.retryPendingHandoffs();
		expect(edgeFor(ledger, "a", "b")?.state).toBe("settled");
		expect(repository.read(destination("b"))).toMatchObject({
			composer: { skillNames: ["review"] },
			completedCommandRecoveryIds: ["done-1"],
			releasedRecoveryIds: ["rel-1"],
		});
		expect(ledger.readGraph().deliveries).toEqual(
			expect.arrayContaining([
				expect.objectContaining({ destination: endpoint("b"), recoveryId: "done-1", disposition: "completed" }),
				expect.objectContaining({ destination: endpoint("b"), recoveryId: "rel-1", disposition: "released" }),
			]),
		);
		expect(repository.read(destination("a"))).toEqual({ draft: "", unconfirmed: null });
	});

	test("an uncomposed edge whose transfer landed settles from durable capture proof", () => {
		const { ledger, repository, library } = setup();
		repository.write(destination("a"), {
			draft: "",
			unconfirmed: "in flight",
			unconfirmedCaptureToken: "token-1",
		});
		const flakyLedger = flaky(flaky(ledger, "recordComposed", 1), "settleEdge", 1);
		const draftLibrary = library(() => flakyLedger);

		expect(draftLibrary.handoff(destination("a"), destination("b"))).toBe(false);
		expect(edgeFor(ledger, "a", "b")).toMatchObject({ state: "pending" });
		expect(repository.read(destination("b"))).toMatchObject({
			unconfirmed: "in flight",
			unconfirmedCaptureToken: "token-1",
		});

		const transfer = vi.spyOn(repository, "transfer");
		draftLibrary.retryPendingHandoffs();
		expect(edgeFor(ledger, "a", "b")?.state).toBe("settled");
		expect(transfer).not.toHaveBeenCalled();
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

	test("an open-obligation hop settles after its completion without resurrecting the source", async () => {
		const { ledger, repository, library } = setup();
		repository.write(destination("a"), {
			draft: "current",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		const draftLibrary = library();
		const source = draftLibrary.open(destination("a"));
		let finish!: () => void;
		const submitted = source.submitBuiltinCommand(
			() =>
				new Promise<boolean>((resolve) => {
					finish = () => resolve(true);
				}),
		);
		const token = repository.read(destination("a")).unconfirmedCaptureToken;
		if (token === undefined) throw new Error("missing capture token");

		// The composition moves while the obligation is still open, but the edge
		// must not settle yet.
		expect(draftLibrary.handoff(destination("a"), destination("b"))).toBe(true);
		expect(edgeFor(ledger, "a", "b")).toMatchObject({ state: "pending" });
		expect(repository.read(destination("b"))).toMatchObject({
			draft: "",
			unconfirmed: "current",
			unconfirmedCaptureToken: token,
		});

		finish();
		await submitted;
		// The completion clears the destination's capture and records the command.
		expect(repository.read(destination("b"))).toMatchObject({
			unconfirmed: null,
			completedCommandRecoveryIds: ["recovered-a"],
		});

		draftLibrary.retryPendingHandoffs();
		expect(edgeFor(ledger, "a", "b")).toMatchObject({ state: "settled" });
		expect(repository.read(destination("b"))).toMatchObject({
			draft: "",
			unconfirmed: null,
			completedCommandRecoveryIds: ["recovered-a"],
		});
		expect(ledger.readGraph().deliveries).toContainEqual(
			expect.objectContaining({ destination: endpoint("b"), recoveryId: "recovered-a", disposition: "completed" }),
		);
		expect(edgeFor(ledger, "a", "b")?.composedAt).not.toBeNull();
	});

	test("a repeated handoff after cleanup does not re-deliver the retired source", () => {
		const { ledger, repository, library } = setup();
		repository.write(destination("a"), { draft: "edited", unconfirmed: null });
		const draftLibrary = library();
		expect(draftLibrary.handoff(destination("a"), destination("b"))).toBe(true);
		draftLibrary.retryPendingHandoffs();
		expect(edgeFor(ledger, "a", "b")).toMatchObject({ state: "cleaned" });

		draftLibrary.open(destination("b")).edit("");
		expect(draftLibrary.handoff(destination("a"), destination("b"))).toBe(false);
		expect(repository.read(destination("b")).draft).toBe("");
		expect(repository.read(destination("a"))).toEqual({ draft: "", unconfirmed: null });
		// No second edge was recorded for the same pair.
		expect(
			ledger.readGraph().edges.filter((edge) => edge.from.sessionRef === "a" && edge.to.sessionRef === "b"),
		).toHaveLength(1);
	});

	test("a plain send's completion clears the destination capture and settles", async () => {
		const { ledger, repository, library } = setup();
		repository.write(destination("a"), { draft: "current", unconfirmed: null });
		const draftLibrary = library();
		const source = draftLibrary.open(destination("a"));
		let settle!: (sent: boolean) => void;
		const submitted = source.submit(
			() =>
				new Promise<boolean>((resolve) => {
					settle = () => resolve(true);
				}),
		);
		const token = repository.read(destination("a")).unconfirmedCaptureToken;
		if (token === undefined) throw new Error("missing capture token");

		expect(draftLibrary.handoff(destination("a"), destination("b"))).toBe(true);
		expect(repository.read(destination("b"))).toMatchObject({
			unconfirmed: "current",
			unconfirmedCaptureToken: token,
		});

		settle(true);
		await submitted;
		draftLibrary.retryPendingHandoffs();
		expect(edgeFor(ledger, "a", "b")).toMatchObject({ state: "settled" });
		expect(repository.read(destination("b"))).toMatchObject({ draft: "", unconfirmed: null });
		expect(repository.read(destination("b")).unconfirmedCaptureToken).toBeUndefined();
	});

	test("recovery does not restore a capture whose submission is still running", async () => {
		const { ledger, repository, library } = setup();
		repository.write(destination("a"), {
			draft: "current",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		const draftLibrary = library();
		const source = draftLibrary.open(destination("a"));
		let settle!: (sent: boolean) => void;
		const submitted = source.submit(
			() =>
				new Promise<boolean>((resolve) => {
					settle = () => resolve(true);
				}),
		);
		const token = repository.read(destination("a")).unconfirmedCaptureToken;
		if (token === undefined) throw new Error("missing capture token");
		expect(draftLibrary.handoff(destination("a"), destination("b"))).toBe(true);

		// The screen must skip this row while the obligation is open.
		expect(draftLibrary.openObligationRecoveryIds(destination("b"))).toEqual(new Set(["recovered-a"]));
		expect(edgeFor(ledger, "a", "b")?.state).toBe("pending");

		settle(true);
		await submitted;
		draftLibrary.retryPendingHandoffs();
		expect(edgeFor(ledger, "a", "b")?.state).toBe("settled");
		// The send resolved as a message admission: its recovered capture settles
		// through the resend receipt, not through the handoff graph, so it stays
		// recoverable input instead of being cleared here.
		expect(repository.read(destination("b"))).toMatchObject({
			unconfirmed: "current",
			unconfirmedComposer: { recoveryIds: ["recovered-a"] },
		});
		expect(draftLibrary.openObligationRecoveryIds(destination("b"))).toEqual(new Set());
	});

	test("a refusal write failure leaves the hop pending until the ledger accepts it", () => {
		const { ledger, repository, library } = setup();
		repository.write(destination("a"), {
			draft: "mine",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		repository.write(destination("b"), { draft: "theirs", unconfirmed: null });
		const flakyLedger = flaky(ledger, "refuseEdge", 1);
		const draftLibrary = library(() => flakyLedger);

		expect(draftLibrary.handoff(destination("a"), destination("b"))).toBe(false);
		expect(edgeFor(ledger, "a", "b")?.state).toBe("pending");
		draftLibrary.retryPendingHandoffs();
		expect(edgeFor(ledger, "a", "b")?.state).toBe("refused");
		expect(repository.read(destination("b")).draft).toBe("theirs");
		expect(repository.read(destination("a")).draft).toBe("mine");
	});

	test("a cleanup failure leaves the settled edge for the next retry", () => {
		const { ledger, repository, library } = setup();
		repository.write(destination("a"), { draft: "edited", unconfirmed: null });
		const flakyLedger = flaky(ledger, "cleanEdge", 1);
		const draftLibrary = library(() => flakyLedger);
		expect(draftLibrary.handoff(destination("a"), destination("b"))).toBe(true);

		draftLibrary.retryPendingHandoffs();
		expect(edgeFor(ledger, "a", "b")?.state).toBe("settled");
		draftLibrary.retryPendingHandoffs();
		expect(edgeFor(ledger, "a", "b")?.state).toBe("cleaned");
	});

	test("an inherited submission's completion reaches a two-hop terminal", async () => {
		const { ledger, repository, library } = setup();
		repository.write(destination("a"), {
			draft: "current",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		const draftLibrary = library();
		const source = draftLibrary.open(destination("a"));
		let finish!: () => void;
		const submitted = source.submitBuiltinCommand(
			() =>
				new Promise<boolean>((resolve) => {
					finish = () => resolve(true);
				}),
		);
		expect(draftLibrary.handoff(destination("a"), destination("b"))).toBe(true);
		expect(draftLibrary.handoff(destination("b"), destination("c"))).toBe(true);
		expect(edgeFor(ledger, "a", "b")?.state).toBe("pending");
		expect(edgeFor(ledger, "b", "c")?.state).toBe("pending");

		finish();
		await submitted;
		// The completion is delivered by the ledger to the terminal identity.
		expect(repository.read(destination("c"))).toMatchObject({
			draft: "",
			unconfirmed: null,
			completedCommandRecoveryIds: ["recovered-a"],
		});
		draftLibrary.retryPendingHandoffs();
		expect(edgeFor(ledger, "b", "c")?.state).toBe("settled");
		expect(edgeFor(ledger, "a", "b")?.state).toBe("settled");
		expect(repository.read(destination("a"))).toEqual({ draft: "", unconfirmed: null });
		expect(repository.read(destination("b"))).toEqual({ draft: "", unconfirmed: null });
		expect(repository.read(destination("c")).completedCommandRecoveryIds).toEqual(["recovered-a"]);
	});

	test("an obligation abandoned by a previous process resolves as failed and settles its edge", () => {
		const { ledger, repository, library } = setup();
		// Termination between submission start and resolution: the capture and
		// its open obligation are durable, and no live document owns the
		// submission in this process.
		repository.write(destination("a"), {
			draft: "",
			unconfirmed: "typed but never resolved",
			unconfirmedCaptureToken: "abandoned-token",
			unconfirmedComposer: { recoveryIds: ["recovered-a"] },
		});
		ledger.beginSubmission("abandoned-token", endpoint("a"), ["recovered-a"]);

		const draftLibrary = library();
		expect(draftLibrary.handoff(destination("a"), destination("b"))).toBe(true);
		// The open obligation owns the row until it completes: the edge waits
		// and destination recovery stays suppressed.
		expect(edgeFor(ledger, "a", "b")?.state).toBe("pending");
		expect(draftLibrary.openObligationRecoveryIds(destination("b"))).toEqual(new Set(["recovered-a"]));

		draftLibrary.retryPendingHandoffs();
		// The abandoned in-flight ownership is released as failed — the state
		// that leaves the capture recoverable instead of consuming it.
		expect(ledger.readGraph().obligations).toMatchObject([{ state: "failed" }]);
		expect(edgeFor(ledger, "a", "b")?.state).toBe("settled");
		expect(draftLibrary.openObligationRecoveryIds(destination("b"))).toEqual(new Set());
		expect(repository.read(destination("b"))).toMatchObject({
			unconfirmed: "typed but never resolved",
			unconfirmedCaptureToken: "abandoned-token",
		});
		expect(repository.read(destination("a"))).toEqual({ draft: "", unconfirmed: null });
	});

	test("a recovered first transfer reaches the terminal of a chain whose next hop exists", () => {
		const { ledger, repository, draftDatabase, library } = setup();
		repository.write(destination("a"), { draft: "delayed work", unconfirmed: null });
		const draftLibrary = library();
		// The first hop's transfer fails after its delivered hash is recorded.
		draftDatabase.exec(
			"CREATE TRIGGER reject_b BEFORE INSERT ON drafts WHEN NEW.session_ref = 'b' BEGIN SELECT RAISE(ABORT, 'full'); END",
		);
		expect(draftLibrary.handoff(destination("a"), destination("b"))).toBe(false);
		expect(edgeFor(ledger, "a", "b")?.deliveredHash).not.toBeNull();
		// The chain continues while the first hop is still pending: the second
		// hop must not move the source's not-yet-landed composition.
		expect(draftLibrary.handoff(destination("b"), destination("c"))).toBe(false);
		expect(edgeFor(ledger, "b", "c")).toMatchObject({ state: "pending" });

		draftDatabase.exec("DROP TRIGGER reject_b");
		draftLibrary.retryPendingHandoffs();
		// The recovered composition reaches the terminal identity...
		expect(repository.read(destination("c"))).toMatchObject({ draft: "delayed work" });
		expect(edgeFor(ledger, "a", "b")?.state).toBe("settled");
		expect(edgeFor(ledger, "b", "c")?.state).toBe("settled");
		// ...and the graph's terminal names the identity that holds it.
		const graph = ledger.readGraph();
		expect(terminalDestination(graph.edges, endpoint("a"))).toEqual(endpoint("c"));
		expect(repository.read(destination("a"))).toEqual({ draft: "", unconfirmed: null });
		expect(repository.read(destination("b"))).toEqual({ draft: "", unconfirmed: null });
	});
});
