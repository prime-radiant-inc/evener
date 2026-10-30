import { afterEach, describe, expect, test } from "vitest";
import { DraftLibrary } from "./draftLibrary";
import { type DraftDestination, DraftRepository } from "./draftRepository";
import { DraftSubmissionLedgerSQLite } from "./draftSubmissionLedger";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "./sqliteSync.testkit";

const databases: SqliteDoubleDatabase[] = [];
afterEach(() => {
	for (const db of databases.splice(0)) db.close();
});

const destination = (sessionRef: string): DraftDestination => ({ hubId: "hub", sessionRef });

/** One-shot failures at the durable write points a restart must recover from.
 * `beginTransfer` is excluded: an intent the drafts store could not record is
 * recoverable only within the process that holds it (the in-memory fallback),
 * which the library suite covers. `resolveSubmission` is excluded the same
 * way the predecessor graph excluded its resolution failures: the owed write
 * lives in the submitting document, so its own test below drives the in-process
 * retry that converges it. */
type FailureMethod = "transfer" | "markTransferSettled" | "resolveSubmission";
const failures: (FailureMethod | "none")[] = ["none", "transfer", "markTransferSettled"];
const completionPoints = ["before", "between", "after"] as const;

interface Harness {
	ledger: DraftSubmissionLedgerSQLite;
	repository: DraftRepository;
	makeLibrary: () => DraftLibrary;
}

function setup(failure: FailureMethod | "none"): Harness {
	const drafts = openSqliteSyncDouble();
	databases.push(drafts.database);
	const mutations = openSqliteSyncDouble();
	databases.push(mutations.database);
	const ledger = new DraftSubmissionLedgerSQLite(mutations.port, { now: () => 1000 });
	let ledgerRemaining = failure === "resolveSubmission" ? 1 : 0;
	const ledgerAccessor = () => {
		if (failure !== "resolveSubmission") return ledger;
		return new Proxy(ledger, {
			get(target, property) {
				const value = Reflect.get(target, property, target);
				if (property === "resolveSubmission" && typeof value === "function")
					return (...args: unknown[]) => {
						if (ledgerRemaining-- > 0) throw new Error("injected resolveSubmission failure");
						return (value as (...inner: unknown[]) => unknown).apply(target, args);
					};
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as DraftSubmissionLedgerSQLite;
	};
	const repository = new DraftRepository(drafts.port, () => 1000);
	let repositoryRemaining = failure === "transfer" || failure === "markTransferSettled" ? 1 : 0;
	// Transfer ids are process-global in production (random UUIDs); the
	// harness counter must outlive every library instance the same way.
	let next = 0;
	const repositoryAccessor = () => {
		if (failure !== "transfer" && failure !== "markTransferSettled") return repository;
		return new Proxy(repository, {
			get(target, property) {
				const value = Reflect.get(target, property, target);
				if (property === failure && typeof value === "function")
					return (...args: unknown[]) => {
						if (repositoryRemaining-- > 0) throw new Error(`injected ${failure} failure`);
						return (value as (...inner: unknown[]) => unknown).apply(target, args);
					};
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as DraftRepository;
	};
	return {
		ledger,
		repository,
		makeLibrary: () =>
			new DraftLibrary(
				() => repositoryAccessor(),
				() => `id-${++next}`,
				ledgerAccessor,
			),
	};
}

/** Model of the identity chain: a hop is accepted only when its destination is
 * empty, an occupied destination is a permanent refusal that keeps its own
 * work, and the composition stays where the last accepted hop put it. */
function modelOwner(refused: readonly [boolean, boolean]): "a" | "b" | "c" {
	if (refused[0]) return "a";
	return refused[1] ? "b" : "c";
}

describe("handoff interleavings", () => {
	test("seeded operation sequences keep the durable invariants", async () => {
		for (let seed = 1; seed <= 12; seed++) {
			const harness = setup("none");
			const { repository } = harness;
			repository.write(destination("a"), {
				draft: "work",
				unconfirmed: null,
				composer: { recoveryIds: ["recovered-seed"] },
			});
			let library = harness.makeLibrary();
			const source = library.open(destination("a"));
			let finish: (() => void) | undefined;
			let submitted: Promise<boolean | "not-sent" | undefined> | undefined;
			let open = false;
			let random = seed;
			const next = () => {
				random = (random * 1103515245 + 12345) % 2147483648;
				return random / 2147483648;
			};
			const startSubmission = () => {
				submitted = source.submitBuiltinCommand(
					() =>
						new Promise<boolean>((resolve) => {
							finish = () => resolve(true);
						}),
				);
				open = true;
			};
			const complete = async () => {
				finish?.();
				await submitted;
				open = false;
				library = harness.makeLibrary();
				library.retryPendingHandoffs();
			};

			for (let step = 0; step < 8; step++) {
				const operation = Math.floor(next() * 4);
				if (operation === 0 && !open) startSubmission();
				else if (operation === 1 && open) await complete();
				else {
					library = harness.makeLibrary();
					if (operation === 2) {
						const refs = ["a", "b", "c"] as const;
						const index = Math.min(Math.floor(next() * 2), 1);
						const to = refs[index + 1];
						if (to !== undefined) library.handoff(destination(refs[index]), destination(to));
					}
					library.retryPendingHandoffs();
				}
			}
			if (open) await complete();
			for (let attempt = 0; attempt < 4; attempt++) {
				library = harness.makeLibrary();
				library.retryPendingHandoffs();
			}
			// Every durably intended hop finished, and every obligation the
			// sequence opened is resolved and applied: nothing is stranded.
			expect(harness.repository.outstandingTransfers()).toHaveLength(0);
			expect(harness.ledger.outstandingObligations()).toHaveLength(0);
			// A capture token outlives its submission only as uncertain input:
			// once every obligation is applied, no record still holds one.
			for (const ref of ["a", "b", "c"] as const)
				expect(harness.repository.read(destination(ref)).unconfirmedCaptureToken).toBeUndefined();
		}
	});

	for (const refused of [
		[false, false],
		[true, false],
		[false, true],
		[true, true],
	] as const)
		for (const completionAt of completionPoints)
			for (const failure of failures) {
				const label = `refused=${refused.join(",")} completion=${completionAt} failure=${failure}`;
				test(label, async () => {
					const harness = setup(failure);
					const { repository } = harness;
					if (refused[0]) repository.write(destination("b"), { draft: "b work", unconfirmed: null });
					if (refused[1]) repository.write(destination("c"), { draft: "c work", unconfirmed: null });
					repository.write(destination("a"), {
						draft: "current",
						unconfirmed: null,
						composer: { recoveryIds: ["recovered-a"] },
					});

					let library = harness.makeLibrary();
					const source = library.open(destination("a"));
					let finish!: () => void;
					const submitted = source.submitBuiltinCommand(
						() =>
							new Promise<boolean>((resolve) => {
								finish = () => resolve(true);
							}),
					);
					const capture = repository.read(destination("a")).unconfirmedCaptureToken;
					if (capture === undefined) throw new Error("missing capture token");

					const drain = () => {
						for (let attempt = 0; attempt < 4; attempt++) {
							library = harness.makeLibrary();
							library.retryPendingHandoffs();
						}
					};
					const complete = async () => {
						finish();
						await submitted;
						drain();
					};

					if (completionAt === "before") await complete();

					library = harness.makeLibrary();
					library.handoff(destination("a"), destination("b"));
					drain();

					if (completionAt === "between") await complete();

					library = harness.makeLibrary();
					library.handoff(destination("b"), destination("c"));
					drain();

					if (completionAt === "after") await complete();

					drain();
					const owner = modelOwner(refused);
					expect(repository.read(destination(owner)).completedCommandRecoveryIds).toContain("recovered-a");
					for (const ref of ["a", "b", "c"] as const)
						expect(repository.read(destination(ref)).unconfirmedCaptureToken).toBeUndefined();
					expect(repository.outstandingTransfers()).toHaveLength(0);
					expect(harness.ledger.outstandingObligations()).toHaveLength(0);
					// An occupied destination keeps its own work only until its
					// own identity moves on: the second hop legitimately carries
					// it away, like any resume would.
					if (refused[1]) expect(repository.read(destination("c")).draft).toBe("c work");
				});
			}

	test("a lost resolution retries through the document that owes it", async () => {
		const harness = setup("resolveSubmission");
		const { repository } = harness;
		repository.write(destination("a"), {
			draft: "current",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		// The instance that holds the submitting document: its retry pass is
		// the one that can flush the resolution the failed write still owes.
		const library = harness.makeLibrary();
		const source = library.open(destination("a"));
		let finish!: () => void;
		const submitted = source.submitBuiltinCommand(
			() =>
				new Promise<boolean>((resolve) => {
					finish = () => resolve(true);
				}),
		);
		const capture = repository.read(destination("a")).unconfirmedCaptureToken;
		if (capture === undefined) throw new Error("missing capture token");
		expect(library.handoff(destination("a"), destination("b"))).toBe(true);
		finish();
		await submitted;
		// The resolution write failed after the retired origin lost its local
		// fallback: the owner still holds the capture, and the markers are
		// owed by the document whose submission went out.
		expect(repository.read(destination("b")).completedCommandRecoveryIds).toBeUndefined();
		expect(repository.read(destination("b")).unconfirmedCaptureToken).toBe(capture);
		// The document's own retry converges it: the resolution lands, and the
		// completion delivers at the identity that holds the record now.
		library.retryPendingHandoffs();
		expect(repository.read(destination("b"))).toMatchObject({
			unconfirmed: null,
			completedCommandRecoveryIds: ["recovered-a"],
		});
		expect(harness.ledger.outstandingObligations()).toHaveLength(0);
	});
});
