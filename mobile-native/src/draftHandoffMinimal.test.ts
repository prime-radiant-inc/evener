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

	it("a settlement defers while its owner cannot load", () => {
		const { repository, ledger, library } = setup();
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
		// The destination keeps real work the marker write must not destroy.
		repository.write(b, { draft: "kept work", unconfirmed: null });
		let failedReads = 3;
		const unreadable = new Proxy(repository, {
			get(target, property) {
				const value = Reflect.get(target, property, target);
				if (property === "read" && typeof value === "function")
					return (...args: unknown[]) => {
						const at = args[0] as DraftDestination;
						if (at.sessionRef === "b" && failedReads-- > 0) throw new Error("database is locked");
						return (value as (...inner: unknown[]) => unknown).apply(target, args);
					};
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as typeof repository;
		const deferred = new DraftLibrary(
			() => unreadable,
			() => "id-defer",
			() => ledger,
		);
		deferred.retryPendingHandoffs();
		// The owner could not load: the settlement defers and the saved draft
		// survives untouched.
		expect(repository.outstandingTransfers()).toHaveLength(1);
		expect(repository.read(b)).toEqual({ draft: "kept work", unconfirmed: null });
		failedReads = 0;
		const clean = library();
		clean.retryPendingHandoffs();
		expect(repository.outstandingTransfers()).toHaveLength(0);
		expect(repository.read(b)).toMatchObject({ draft: "kept work", releasedRecoveryIds: ["recovered-a"] });
	});

	it("a marker save failure retries its durable write past the in-memory copy", () => {
		const { repository, ledger } = setup();
		const a = destination("a");
		const b = destination("b");
		repository.write(a, {
			draft: "work",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		let markerWrites = 0;
		const failing = new Proxy(repository, {
			get(target, property) {
				const value = Reflect.get(target, property, target);
				if (property === "write" && typeof value === "function")
					return (...args: unknown[]) => {
						const to = args[0] as DraftDestination;
						if (to.sessionRef === "b" && markerWrites++ === 0) throw new Error("marker write failed");
						return (value as (...inner: unknown[]) => unknown).apply(target, args);
					};
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as typeof repository;
		const live = new DraftLibrary(
			() => failing,
			() => "id-live",
			() => ledger,
		);
		expect(live.handoff(a, b)).toBe(true);
		expect(repository.outstandingTransfers()).toHaveLength(1);
		// The same library retries: the destination's live snapshot already
		// holds the ids from the failed attempt, but they never persisted, so
		// the settlement must write them durably before settling.
		live.retryPendingHandoffs();
		expect(repository.outstandingTransfers()).toHaveLength(0);
		expect(repository.read(b).releasedRecoveryIds).toEqual(["recovered-a"]);
	});

	it("capture routing follows commit order, not intent order", async () => {
		const { repository, ledger, library } = setup();
		const a = destination("a");
		const b = destination("b");
		const c = destination("c");
		// An older hop out of B is intended first but commits last.
		repository.write(b, { draft: "b work", unconfirmed: null });
		const failing = failingTransfers(repository, 1);
		const first = new DraftLibrary(
			() => failing,
			() => "id-first",
			() => ledger,
		);
		expect(first.handoff(b, c)).toBe(false);
		repository.write(b, { draft: "", unconfirmed: null });
		// A capture rides a newer hop into B and onward with the older hop's
		// retry, which commits after it.
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
		const restarted = library();
		restarted.retryPendingHandoffs();
		finish();
		await submitted;
		// The completion follows the last-committed hop carrying the capture.
		expect(repository.read(c)).toMatchObject({
			unconfirmed: null,
			completedCommandRecoveryIds: ["recovered-a"],
		});
		expect(ledger.outstandingObligations()).toHaveLength(0);
	});

	it("an undelivered completion keeps its capture suppressed until delivery lands", async () => {
		const { repository, ledger, library } = setup();
		const a = destination("a");
		const b = destination("b");
		repository.write(a, {
			draft: "current",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		let failLog = false;
		const failing = new Proxy(repository, {
			get(target, property) {
				const value = Reflect.get(target, property, target);
				if (property === "latestTransferCarrying" && typeof value === "function")
					return (...args: unknown[]) => {
						if (failLog) throw new Error("transfer log unavailable");
						return (value as (...inner: unknown[]) => unknown).apply(target, args);
					};
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as DraftRepository;
		const live = new DraftLibrary(
			() => failing,
			() => "id-live",
			() => ledger,
		);
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
		failLog = true;
		finish();
		await submitted;
		// The transfer log is unreadable, so the resolution is durable but its
		// completion has not delivered. The executed command's sources stay
		// suppressed at the capture's owner: a resolved obligation is not a
		// delivered one, and restoration must not run ahead of delivery.
		const restarted = library();
		expect(restarted.openObligationRecoveryIds(b)).toEqual(new Set(["recovered-a"]));
		failLog = false;
		restarted.retryPendingHandoffs();
		expect(repository.read(b)).toMatchObject({
			unconfirmed: null,
			completedCommandRecoveryIds: ["recovered-a"],
		});
		expect(ledger.outstandingObligations()).toHaveLength(0);
		expect(restarted.openObligationRecoveryIds(b)?.size).toBe(0);
	});

	it("a settlement follows only the hops that carried its own record", () => {
		const { repository, library } = setup();
		const a = destination("a");
		const b = destination("b");
		const c = destination("c");
		// B's own work moves on first: the committed B -> C hop carried B's
		// record, long before A's ever arrives at B.
		repository.write(b, {
			draft: "b work",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-b"] },
		});
		expect(library().handoff(b, c)).toBe(true);
		repository.write(a, {
			draft: "a work",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		const live = library();
		expect(live.handoff(a, b)).toBe(true);
		// The A -> B hop settles at B, the identity holding its record, and
		// never routes through the earlier B -> C hop, which carried
		// different work: C keeps only the markers of what it received.
		expect(repository.read(b)).toMatchObject({
			draft: "a work",
			releasedRecoveryIds: ["recovered-a"],
		});
		expect(repository.read(c).releasedRecoveryIds).toEqual(["recovered-b"]);
		expect(live.hasPendingHandoff(b)).toBe(false);
	});

	it("a migrated transfer log recovers capture ownership for undelivered completions", () => {
		// An earlier build's schema: no carried token, no commit sequence, no
		// generation routing. Its committed hop moved a capture A -> B and
		// never delivered the completion it owes.
		const legacy = openSqliteSyncDouble();
		databases.push(legacy.database);
		legacy.database.exec(`CREATE TABLE drafts (
      hub_id TEXT NOT NULL, session_ref TEXT NOT NULL, draft TEXT NOT NULL, unconfirmed TEXT,
      PRIMARY KEY (hub_id, session_ref))`);
		for (const column of [
			"composer",
			"unconfirmed_composer",
			"parked_composition",
			"unconfirmed_capture_token",
			"recovery_resend_acks",
			"completed_command_recovery_ids",
			"released_recovery_ids",
		])
			legacy.database.exec(`ALTER TABLE drafts ADD COLUMN ${column} TEXT`);
		legacy.database.exec(`CREATE TABLE draft_transfer_log (
      transfer_id TEXT PRIMARY KEY, from_hub_id TEXT NOT NULL, from_ref TEXT NOT NULL,
      to_hub_id TEXT NOT NULL, to_ref TEXT NOT NULL, state TEXT NOT NULL,
      delivered_hash TEXT, completed_command_recovery_ids TEXT NOT NULL DEFAULT '[]',
      released_recovery_ids TEXT NOT NULL DEFAULT '[]',
      created_at INTEGER NOT NULL, state_changed_at INTEGER NOT NULL)`);
		legacy.database.exec(`INSERT INTO draft_transfer_log
      (transfer_id, from_hub_id, from_ref, to_hub_id, to_ref, state, delivered_hash, created_at, state_changed_at)
    VALUES ('transfer-legacy', 'hub', 'a', 'hub', 'b', 'pending', 'hash-legacy', 900, 900)`);
		legacy.database.exec(`INSERT INTO drafts (hub_id, session_ref, draft, unconfirmed, unconfirmed_capture_token)
    VALUES ('hub', 'b', 'b work', 'captured', 'token-legacy')`);
		const repository = new DraftRepository(legacy.port, () => 1000);
		const { database: ledgerDb, port: ledgerPort } = openSqliteSyncDouble();
		databases.push(ledgerDb);
		const ledger = new DraftSubmissionLedgerSQLite(ledgerPort, { now: () => 1000 });
		ledger.beginSubmission("token-legacy", destination("a"), ["recovered-a"]);
		ledger.resolveSubmission("token-legacy", "completed", ["recovered-a"], []);
		const live = new DraftLibrary(
			() => repository,
			() => "id-live",
			() => ledger,
		);
		live.retryPendingHandoffs();
		// The migration recovered the hop's evidence from its endpoint, so
		// delivery reaches B and applies the completion - instead of reading
		// the emptied origin as proof the capture is gone and acknowledging
		// without delivering.
		expect(repository.read(destination("b"))).toMatchObject({
			unconfirmed: null,
			completedCommandRecoveryIds: ["recovered-a"],
		});
		expect(repository.read(destination("b")).unconfirmedCaptureToken).toBeUndefined();
		expect(ledger.outstandingObligations()).toHaveLength(0);
		expect(repository.outstandingTransfers()).toHaveLength(0);
	});

	it("a completion after a refusal reaches the destination that took its rows", async () => {
		const { repository, ledger, library } = setup();
		const a = destination("a");
		const b = destination("b");
		repository.write(a, {
			draft: "current",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		repository.write(b, { draft: "b work", unconfirmed: null });
		const live = library();
		const source = live.open(a);
		let finish!: () => void;
		const submitted = source.submitBuiltinCommand(
			() =>
				new Promise<boolean>((resolve) => {
					finish = () => resolve(true);
				}),
		);
		// The handoff refuses against the occupied B: the composition's cited
		// sources are released where its rows went, and the hop is forgotten.
		expect(live.handoff(a, b)).toBe(false);
		expect(repository.read(b).releasedRecoveryIds).toEqual(["recovered-a"]);
		finish();
		await submitted;
		// The command consumed its input, and the rows live at B: the
		// completed markers must reach B before the acknowledgment - released
		// rows stay editable, completed ones never restore.
		expect(repository.read(b).completedCommandRecoveryIds).toEqual(["recovered-a"]);
		expect(
			ledger.deliveries().find((row) => row.destination.sessionRef === "b" && row.recoveryId === "recovered-a")
				?.disposition,
		).toBe("completed");
		expect(ledger.outstandingObligations()).toHaveLength(0);
	});

	it("a hop retires its source only after rerouting what is still on its way in", () => {
		const { repository, ledger, library } = setup();
		const a = destination("a");
		const b = destination("b");
		const c = destination("c");
		repository.write(a, {
			draft: "a work",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		repository.write(b, { draft: "b work", unconfirmed: null });
		let failA = false;
		const failing = new Proxy(repository, {
			get(target, property) {
				const value = Reflect.get(target, property, target);
				if (property === "read" && typeof value === "function")
					return (...args: unknown[]) => {
						if (failA) throw new Error("drafts unavailable");
						return (value as (...inner: unknown[]) => unknown).apply(target, args);
					};
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as DraftRepository;
		const live = new DraftLibrary(
			() => failing,
			() => "id-live",
			() => ledger,
		);
		failA = true;
		expect(live.handoff(a, b)).toBe(false);
		failA = false;
		// B moves on while A -> B is still on its way in: A's hop must follow
		// the work onward, never deliver into the identity B just left.
		const restarted = library();
		expect(restarted.handoff(b, c)).toBe(true);
		restarted.retryPendingHandoffs();
		expect(repository.read(c)).toMatchObject({ draft: "b work" });
		// A's work stays with A - refused at the occupied C, released there,
		// nothing stranded at the retired B.
		expect(repository.read(b).draft).toBe("");
		expect(repository.read(a)).toMatchObject({
			draft: "a work",
			composer: { recoveryIds: ["recovered-a"] },
		});
		expect(repository.read(c).releasedRecoveryIds).toEqual(["recovered-a"]);
	});

	it("a retry batch honors a reroute an earlier hop in the same batch committed", () => {
		const { repository, ledger } = setup();
		const a = destination("a");
		const b = destination("b");
		const c = destination("c");
		repository.write(a, {
			draft: "a work",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		repository.write(b, { draft: "b work", unconfirmed: null });
		let failingRef: string | null = null;
		const failing = new Proxy(repository, {
			get(target, property) {
				const value = Reflect.get(target, property, target);
				if (property === "read" && typeof value === "function")
					return (...args: unknown[]) => {
						const destination_ = args[0] as DraftDestination;
						if (destination_.sessionRef === failingRef) throw new Error("drafts unavailable");
						return (value as (...inner: unknown[]) => unknown).apply(target, args);
					};
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as DraftRepository;
		let batchIds = 0;
		const live = new DraftLibrary(
			() => failing,
			() => `id-batch-${++batchIds}`,
			() => ledger,
		);
		// B -> C is intended first but cannot commit; A -> B is intended next
		// and cannot load its source. The batch runs in creation order.
		failingRef = "c";
		expect(live.handoff(b, c)).toBe(false);
		failingRef = "a";
		expect(live.handoff(a, b)).toBe(false);
		failingRef = null;
		live.retryPendingHandoffs();
		// B -> C commits and reroutes A's hop onward; the batch must execute
		// the rerouted row, never its stale A -> B object - A's work stays
		// with A, refused at the occupied C, never landed at the retired B.
		expect(repository.read(c)).toMatchObject({ draft: "b work" });
		expect(repository.read(b).draft).toBe("");
		expect(repository.read(a)).toMatchObject({
			draft: "a work",
			composer: { recoveryIds: ["recovered-a"] },
		});
		expect(repository.read(c).releasedRecoveryIds).toEqual(["recovered-a"]);
	});

	it("a rerouted unrecorded intent survives the hop that rerouted it", () => {
		const { repository, ledger } = setup();
		const a = destination("a");
		const b = destination("b");
		const c = destination("c");
		repository.write(a, {
			draft: "a work",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		repository.write(b, { draft: "b work", unconfirmed: null });
		let failStore = false;
		const failing = new Proxy(repository, {
			get(target, property) {
				const value = Reflect.get(target, property, target);
				if (property === "beginTransfer" && typeof value === "function" && failStore)
					return () => {
						throw new Error("drafts unavailable");
					};
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as DraftRepository;
		let intentIds = 0;
		const live = new DraftLibrary(
			() => failing,
			() => `id-intent-${++intentIds}`,
			() => ledger,
		);
		// A -> B cannot record: the intent lives only in memory.
		failStore = true;
		expect(live.handoff(a, b)).toBe(false);
		failStore = false;
		// B -> C commits and reroutes that intent onward: the redirected
		// intent must outlive the committing hop's own cleanup.
		expect(live.handoff(b, c)).toBe(true);
		expect(live.hasPendingHandoff(c)).toBe(true);
		live.retryPendingHandoffs();
		expect(live.hasPendingHandoff(c)).toBe(false);
		expect(repository.read(a)).toMatchObject({
			draft: "a work",
			composer: { recoveryIds: ["recovered-a"] },
		});
		expect(repository.read(c).releasedRecoveryIds).toEqual(["recovered-a"]);
	});

	it("an undelivered completion reaches owners of transfers still carrying its input", async () => {
		const { repository, ledger, library } = setup();
		const a = destination("a");
		const b = destination("b");
		const c = destination("c");
		repository.write(a, {
			draft: "current",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		repository.write(b, { draft: "b work", unconfirmed: null });
		const live = library();
		const source = live.open(a);
		let finish!: () => void;
		const submitted = source.submitBuiltinCommand(
			() =>
				new Promise<boolean>((resolve) => {
					finish = () => resolve(true);
				}),
		);
		// The handoff into occupied B refuses: the cited sources are released
		// at B, and the delivery rows name B.
		expect(live.handoff(a, b)).toBe(false);
		// B's record moves on, but the settlement's delivery records cannot
		// write: the committed hop stays owed with no rows naming C.
		let failDeliveries = true;
		const failingLedger = new Proxy(ledger, {
			get(target, property) {
				const value = Reflect.get(target, property, target);
				if (property === "writeDeliveries" && typeof value === "function" && failDeliveries)
					return () => {
						throw new Error("deliveries unavailable");
					};
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as typeof ledger;
		const mover = new DraftLibrary(
			() => repository,
			() => "id-mover",
			() => failingLedger,
		);
		expect(mover.handoff(b, c)).toBe(true);
		expect(mover.hasPendingHandoff(c)).toBe(true);
		// The command completes before the settlement retries: the executed
		// input's completed markers must reach C - the identity whose record
		// carries them - before the obligation is acknowledged.
		finish();
		await submitted;
		expect(repository.read(c).completedCommandRecoveryIds).toEqual(["recovered-a"]);
		failDeliveries = false;
		const restarted = library();
		restarted.retryPendingHandoffs();
		expect(ledger.outstandingObligations()).toHaveLength(0);
		expect(restarted.hasPendingHandoff(c)).toBe(false);
		expect(repository.read(c).completedCommandRecoveryIds).toEqual(["recovered-a"]);
		expect(repository.read(b).completedCommandRecoveryIds).toEqual(["recovered-a"]);
	});

	it("a rerouted fallback intent is never executed at its old destination", () => {
		const { repository, ledger } = setup();
		const a = destination("a");
		const b = destination("b");
		const c = destination("c");
		repository.write(a, {
			draft: "a work",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		repository.write(b, { draft: "b work", unconfirmed: null });
		let failStore = false;
		const failing = new Proxy(repository, {
			get(target, property) {
				const value = Reflect.get(target, property, target);
				if (property === "beginTransfer" && typeof value === "function" && failStore)
					return () => {
						throw new Error("drafts unavailable");
					};
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as DraftRepository;
		let intentIds = 0;
		const live = new DraftLibrary(
			() => failing,
			() => `id-fallback-${++intentIds}`,
			() => ledger,
		);
		// Neither hop can record: both intents live only in memory, and the
		// retry loop sees them in creation order.
		failStore = true;
		expect(live.handoff(b, c)).toBe(false);
		expect(live.handoff(a, b)).toBe(false);
		failStore = false;
		live.retryPendingHandoffs();
		// B -> C commits and reroutes the A -> B intent onward: the pass must
		// never execute the stale A -> B entry and strand A's work at the
		// identity B just left.
		expect(repository.read(b).draft).toBe("");
		expect(repository.read(a)).toMatchObject({
			draft: "a work",
			composer: { recoveryIds: ["recovered-a"] },
		});
		expect(live.hasPendingHandoff(c)).toBe(true);
		// The rerouted intent runs on the next pass, refused at the occupied
		// C, its cited sources released there.
		live.retryPendingHandoffs();
		expect(live.hasPendingHandoff(c)).toBe(false);
		expect(repository.read(c)).toMatchObject({ draft: "b work" });
		expect(repository.read(c).releasedRecoveryIds).toEqual(["recovered-a"]);
	});
});
