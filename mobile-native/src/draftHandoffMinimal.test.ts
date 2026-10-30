import { afterEach, describe, expect, it } from "vitest";
import { DraftLibrary } from "./draftLibrary";
import { type DraftDestination, DraftRepository } from "./draftRepository";
import { DraftSubmissionLedgerSQLite } from "./draftSubmissionLedger";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "./sqliteSync.testkit";

const databases: SqliteDoubleDatabase[] = [];
afterEach(() => {
	for (const database of databases.splice(0)) database.close();
});

const destination = (sessionRef: string): DraftDestination => ({ hubId: "hub", sessionRef });

function setup() {
	const { database: db, port } = openSqliteSyncDouble();
	databases.push(db);
	const repository = new DraftRepository(port, () => 1000);
	const { database: ledgerDb, port: ledgerPort } = openSqliteSyncDouble();
	databases.push(ledgerDb);
	const ledger = new DraftSubmissionLedgerSQLite(ledgerPort, { now: () => 1000 });
	let next = 0;
	const createId = () => `id-${++next}`;
	return {
		repository,
		db,
		ledger,
		library: () =>
			new DraftLibrary(
				() => repository,
				createId,
				() => ledger,
			),
	};
}

/** A storage double whose `transfer` fails while `failures` says so. */
function failingTransfers(repository: DraftRepository, failures: number) {
	let remaining = failures;
	return new Proxy(repository, {
		get(target, property) {
			const value = Reflect.get(target, property, target);
			if (property === "transfer" && typeof value === "function")
				return (...args: unknown[]) => {
					if (remaining > 0) {
						remaining -= 1;
						throw new Error("transfer failed");
					}
					return (value as (...inner: unknown[]) => unknown).apply(target, args);
				};
			return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
		},
	}) as DraftRepository;
}

describe("durable minimal handoffs", () => {
	it("a restart finishes a handoff whose transfer failed", () => {
		const { repository, ledger, library } = setup();
		const a = destination("a");
		const b = destination("b");
		repository.write(a, { draft: "edited recovery", unconfirmed: null });
		const failing = failingTransfers(repository, 1);
		const live = new DraftLibrary(
			() => failing,
			() => "id-live",
			() => ledger,
		);
		expect(live.handoff(a, b)).toBe(false);
		// The hop is durable pending state now, not an in-memory map entry.
		const restarted = library();
		expect(restarted.hasPendingHandoff(b)).toBe(true);
		restarted.retryPendingHandoffs();
		expect(repository.read(b)).toEqual({ draft: "edited recovery", unconfirmed: null });
		expect(repository.read(a)).toEqual({ draft: "", unconfirmed: null });
		expect(restarted.hasPendingHandoff(b)).toBe(false);
	});

	it("a restarted handoff refuses an occupied destination without stranding its cited sources", () => {
		const { repository, ledger, library } = setup();
		const a = destination("a");
		const b = destination("b");
		repository.write(a, {
			draft: "work",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		const failing = failingTransfers(repository, 1);
		const live = new DraftLibrary(
			() => failing,
			() => "id-live",
			() => ledger,
		);
		expect(live.handoff(a, b)).toBe(false);
		// The destination gains its own work while the process restarts.
		repository.write(b, { draft: "b work", unconfirmed: null });
		const restarted = library();
		restarted.retryPendingHandoffs();
		// The refusal is permanent and its released sources reached the
		// occupied destination durably - nothing moved, nothing stranded.
		expect(restarted.hasPendingHandoff(b)).toBe(false);
		expect(repository.read(b).releasedRecoveryIds).toEqual(["recovered-a"]);
		expect(repository.read(a)).toMatchObject({ draft: "work", composer: { recoveryIds: ["recovered-a"] } });
		expect(repository.read(b).draft).toBe("b work");
	});

	it("unknown recovery ownership defers instead of reading as empty", async () => {
		const { repository, ledger, library } = setup();
		const a = destination("a");
		repository.write(a, {
			draft: "current",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		const live = library();
		const source = live.open(a);
		let finish!: () => void;
		const submitted = source.submitBuiltinCommand(
			() =>
				new Promise<boolean>((resolve) => {
					finish = () => resolve(true);
				}),
		);
		// While the command runs, its input is owned: the ledger's open
		// obligation suppresses restoration at the origin.
		expect(live.openObligationRecoveryIds(a)).toEqual(new Set(["recovered-a"]));
		// A ledger that cannot be read is unknown ownership, never empty.
		const unreadable = new Proxy(ledger, {
			get(target, property) {
				if (property === "outstandingObligations") throw new Error("ledger unavailable");
				const value = Reflect.get(target, property, target);
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as DraftSubmissionLedgerSQLite;
		const degraded = new DraftLibrary(
			() => repository,
			() => "id-degraded",
			() => unreadable,
		);
		expect(degraded.openObligationRecoveryIds(a)).toBeUndefined();
		finish();
		await submitted;
		// The resolution released the ownership: nothing is suppressed.
		expect(live.openObligationRecoveryIds(a)?.size).toBe(0);
	});

	it("a completion reaches the record's owner across two hops and a restart", async () => {
		const { repository, db, library } = setup();
		const a = destination("a");
		const b = destination("b");
		const c = destination("c");
		repository.write(a, {
			draft: "current",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		const live = library();
		const source = live.open(a);
		let finish!: () => void;
		const submitted = source.submitBuiltinCommand(
			() =>
				new Promise<boolean>((resolve) => {
					finish = () => resolve(true);
				}),
		);
		const token = repository.read(a).unconfirmedCaptureToken;
		if (token === undefined) throw new Error("missing capture token");
		expect(live.handoff(a, b)).toBe(true);
		expect(live.handoff(b, c)).toBe(true);
		expect(repository.read(c)).toMatchObject({
			unconfirmed: "current",
			unconfirmedCaptureToken: token,
		});
		// The command completes, but the marker write at the owner fails: the
		// durable resolution outlives the process that produced it.
		db.exec(
			"CREATE TRIGGER reject_c BEFORE UPDATE ON drafts WHEN NEW.session_ref = 'c' BEGIN SELECT RAISE(ABORT, 'full'); END",
		);
		finish();
		await submitted;
		expect(repository.read(c).completedCommandRecoveryIds).toBeUndefined();
		db.exec("DROP TRIGGER reject_c");
		// A fresh process owns nothing in memory: the obligation and the
		// transfer pointers are the whole completion route.
		const restarted = library();
		restarted.retryPendingHandoffs();
		expect(repository.read(c)).toMatchObject({
			unconfirmed: null,
			completedCommandRecoveryIds: ["recovered-a"],
		});
		expect(restarted.open(c).getSnapshot().record.unconfirmed).toBeNull();
	});

	it("a completion finds its capture after the origin moved other work onward", async () => {
		const { repository, ledger, library } = setup();
		const a = destination("a");
		const b = destination("b");
		const c = destination("c");
		repository.write(a, {
			draft: "current",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		const live = library();
		const source = live.open(a);
		let finish!: () => void;
		const submitted = source.submitBuiltinCommand(
			() =>
				new Promise<boolean>((resolve) => {
					finish = () => resolve(true);
				}),
		);
		const token = repository.read(a).unconfirmedCaptureToken;
		if (token === undefined) throw new Error("missing capture token");
		expect(live.handoff(a, b)).toBe(true);
		// The origin is reopened with different work and resumes to C: the
		// latest hop out of A now carries other work, not the capture.
		repository.write(a, { draft: "other work", unconfirmed: null });
		expect(live.handoff(a, c)).toBe(true);
		finish();
		await submitted;
		// The completion still reaches the identity holding its capture.
		expect(repository.read(b)).toMatchObject({
			unconfirmed: null,
			completedCommandRecoveryIds: ["recovered-a"],
		});
		expect(ledger.outstandingObligations()).toHaveLength(0);
	});

	it("a repeated handoff settles its committed hop instead of refusing it", () => {
		const { repository, ledger } = setup();
		const a = destination("a");
		const b = destination("b");
		repository.write(a, {
			draft: "work",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		let deliveries = 0;
		const failingLedger = new Proxy(ledger, {
			get(target, property) {
				const value = Reflect.get(target, property, target);
				if (property === "writeDeliveries" && typeof value === "function")
					return (...args: unknown[]) => {
						if (deliveries++ === 0) throw new Error("deliveries write failed");
						return (value as (...inner: unknown[]) => unknown).apply(target, args);
					};
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as typeof ledger;
		const live = new DraftLibrary(
			() => repository,
			() => "id-live",
			() => failingLedger,
		);
		expect(live.handoff(a, b)).toBe(true);
		// The hop committed; its settlement owes only the delivery records.
		const committed = repository.outstandingTransfers();
		expect(committed).toHaveLength(1);
		expect(committed[0].deliveredHash).not.toBeNull();
		// The same handoff again must settle the committed hop - never refuse
		// against the destination its own transfer occupied.
		expect(live.handoff(a, b)).toBe(true);
		expect(repository.outstandingTransfers()).toHaveLength(0);
		const pointer = repository.latestCommittedTransferFrom(a);
		expect(pointer?.to).toMatchObject({ hubId: "hub", sessionRef: "b" });
		expect(ledger.deliveries().length).toBeGreaterThan(0);
	});

	it("a settlement whose marker write fails stays pending and converges", () => {
		const { repository, ledger, library } = setup();
		const a = destination("a");
		const b = destination("b");
		repository.write(a, {
			draft: "work",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		// The hop commits, but its delivery records cannot write yet, so its
		// settlement stays owed.
		let deliveries = 0;
		const failingLedger = new Proxy(ledger, {
			get(target, property) {
				const value = Reflect.get(target, property, target);
				if (property === "writeDeliveries" && typeof value === "function")
					return (...args: unknown[]) => {
						if (deliveries++ === 0) throw new Error("deliveries write failed");
						return (value as (...inner: unknown[]) => unknown).apply(target, args);
					};
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as typeof ledger;
		const live = new DraftLibrary(
			() => repository,
			() => "id-live",
			() => failingLedger,
		);
		expect(live.handoff(a, b)).toBe(true);
		expect(repository.outstandingTransfers()).toHaveLength(1);
		// The destination discards the transferred composition: its record no
		// longer carries the hop's dispositions inline, so the owed settlement
		// must write them as markers.
		repository.write(b, { draft: "", unconfirmed: null });
		let writesToB = 0;
		const failing = new Proxy(repository, {
			get(target, property) {
				const value = Reflect.get(target, property, target);
				if (property === "write" && typeof value === "function")
					return (...args: unknown[]) => {
						const to = args[0] as DraftDestination;
						if (to.sessionRef === "b" && writesToB++ === 0) throw new Error("marker write failed");
						return (value as (...inner: unknown[]) => unknown).apply(target, args);
					};
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as typeof repository;
		const retried = new DraftLibrary(
			() => failing,
			() => "id-retry",
			() => ledger,
		);
		retried.retryPendingHandoffs();
		// The marker write at the destination failed: the hop stays pending
		// instead of settling over dispositions that never persisted.
		expect(repository.outstandingTransfers()).toHaveLength(1);
		expect(writesToB).toBe(1);
		// A clean pass delivers the dispositions and settles the hop.
		const restarted = library();
		restarted.retryPendingHandoffs();
		expect(repository.outstandingTransfers()).toHaveLength(0);
		expect(repository.read(b).releasedRecoveryIds).toEqual(["recovered-a"]);
	});

	it("a refusal delivers completed and released markers to the occupied destination", () => {
		const { repository, library } = setup();
		const a = destination("a");
		const b = destination("b");
		repository.write(b, { draft: "b work", unconfirmed: null });
		repository.write(a, {
			draft: "work",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
			completedCommandRecoveryIds: ["recovered-a"],
		});
		const live = library();
		expect(live.handoff(a, b)).toBe(false);
		// The executed command's marker reaches the occupied destination along
		// with the release, so its retargeted rows cannot restore the input.
		expect(repository.read(b)).toMatchObject({
			draft: "b work",
			completedCommandRecoveryIds: ["recovered-a"],
			releasedRecoveryIds: ["recovered-a"],
		});
		expect(repository.read(a)).toMatchObject({ draft: "work" });
	});
});
