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
});
