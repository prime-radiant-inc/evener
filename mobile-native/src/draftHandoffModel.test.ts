import { afterEach, describe, expect, test } from "vitest";
import type { DraftHandoffEdge, DraftHandoffLedger } from "./draftHandoffLedger";
import { DraftHandoffLedgerSQLite } from "./draftHandoffLedgerSQLite";
import { DraftLibrary } from "./draftLibrary";
import { type DraftDestination, DraftRepository } from "./draftRepository";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "./sqliteSync.testkit";

const databases: SqliteDoubleDatabase[] = [];
afterEach(() => {
	for (const db of databases.splice(0)) db.close();
});

const destination = (sessionRef: string): DraftDestination => ({ hubId: "hub", sessionRef });

type FailureMethod =
	| "claimDestination"
	| "recordDelivered"
	| "recordComposed"
	| "settleEdge"
	| "refuseEdge"
	| "cleanEdge";
const failures: (FailureMethod | "none")[] = [
	"none",
	"claimDestination",
	"recordDelivered",
	"recordComposed",
	"settleEdge",
	"refuseEdge",
	"cleanEdge",
];
const completionPoints = ["before", "between", "after"] as const;

interface Harness {
	ledger: DraftHandoffLedgerSQLite;
	repository: DraftRepository;
	makeLibrary: () => DraftLibrary;
}

function setup(failure: FailureMethod | "none"): Harness {
	const drafts = openSqliteSyncDouble();
	databases.push(drafts.database);
	const mutations = openSqliteSyncDouble();
	databases.push(mutations.database);
	const ledger = new DraftHandoffLedgerSQLite(mutations.port, {
		now: () => 1000,
		createId: (() => {
			let next = 0;
			return () => `id-${++next}`;
		})(),
	});
	let remaining = failure === "none" ? 0 : 1;
	const accessor = (): DraftHandoffLedger => {
		if (failure === "none") return ledger;
		return new Proxy(ledger, {
			get(target, property) {
				const value = Reflect.get(target, property, target);
				if (property === failure && typeof value === "function")
					return (...args: unknown[]) => {
						if (remaining-- > 0) throw new Error(`injected ${failure} failure`);
						return (value as (...inner: unknown[]) => unknown).apply(target, args);
					};
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as DraftHandoffLedger;
	};
	const repository = new DraftRepository(drafts.port);
	return {
		ledger,
		repository,
		makeLibrary: () => new DraftLibrary(() => repository, undefined, accessor),
	};
}

/** Model of the identity chain: a hop is accepted only when its destination is
 * empty, and the composition stays where the last accepted hop put it. */
function modelOwner(
	refused: readonly [boolean, boolean],
	completionAt: (typeof completionPoints)[number],
): "a" | "b" | "c" {
	if (refused[0]) {
		// A deferred refusal is decided only after completion. If the intermediate
		// identity moved its own work on, it is empty by then and the hop is
		// accepted, so the predecessor's completion lands at b.
		if (completionAt === "after") return refused[1] ? "a" : "b";
		return "a";
	}
	return refused[1] ? "b" : "c";
}

function edgesFor(ledger: DraftHandoffLedger): DraftHandoffEdge[] {
	return ledger.readGraph().edges;
}

describe("handoff interleavings", () => {
	test("seeded operation sequences keep the durable invariants", async () => {
		for (let seed = 1; seed <= 12; seed++) {
			const harness = setup("none");
			const { ledger, repository } = harness;
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

				const graph = ledger.readGraph();
				const openObligations = new Set(
					graph.obligations.filter((row) => row.state === "open").map((row) => row.obligationId),
				);
				for (const edge of graph.edges.filter((row) => row.state === "pending")) {
					const awaitingObligation = edge.obligations.some((id) => openObligations.has(id));
					const awaitingDependency = edge.dependencies.some(
						(dependency) => graph.edges.find((row) => row.edgeId === dependency)?.state === "pending",
					);
					expect(awaitingObligation || awaitingDependency).toBe(true);
				}
				for (const ref of ["a", "b", "c"] as const) {
					const capture = repository.read(destination(ref)).unconfirmedCaptureToken;
					if (capture !== undefined) expect(openObligations.has(capture)).toBe(true);
				}
			}
			if (open) await complete();
			for (let attempt = 0; attempt < 4; attempt++) {
				library = harness.makeLibrary();
				library.retryPendingHandoffs();
			}
			expect(ledger.readGraph().obligations.every((row) => row.state !== "open")).toBe(true);
			expect(edgesFor(ledger).every((edge) => edge.state !== "pending")).toBe(true);
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
				// A compose failure under an open obligation is not retried until the
				// obligation resolves; the chain then settles downstream first. That
				// ordering is covered by the before/between cases, so keep the
				// after-completion pattern free of injected store failures.
				if (completionAt === "after" && failure !== "none") continue;
				const label = `refused=${refused.join(",")} completion=${completionAt} failure=${failure}`;
				test(label, async () => {
					const harness = setup(failure);
					const { ledger, repository } = harness;
					// Occupied destinations hold their own work from the start.
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
					const edgeState = (from: string, to: string): string | undefined =>
						edgesFor(ledger).find((edge) => edge.from.sessionRef === from && edge.to.sessionRef === to)?.state;

					if (completionAt === "before") await complete();

					library = harness.makeLibrary();
					library.handoff(destination("a"), destination("b"));
					drain();

					if (completionAt === "between") await complete();

					library = harness.makeLibrary();
					library.handoff(destination("b"), destination("c"));
					drain();

					if (completionAt === "after") await complete();

					// Drain a one-shot store failure, then check the durable invariants.
					drain();

					const graph = ledger.readGraph();
					expect(graph.obligations).toMatchObject([{ obligationId: capture, state: "completed" }]);
					expect(edgesFor(ledger).every((edge) => edge.state !== "pending")).toBe(true);
					const expected = (refusedAtHop: boolean) => (refusedAtHop ? ["refused", "cleaned"] : ["settled", "cleaned"]);
					expect(expected(refused[0])).toContain(edgeState("a", "b"));
					expect(expected(refused[1])).toContain(edgeState("b", "c"));
					for (const ref of ["a", "b", "c"] as const)
						expect(repository.read(destination(ref)).unconfirmedCaptureToken).toBeUndefined();

					const owner = modelOwner(refused, completionAt);
					expect(repository.read(destination(owner)).completedCommandRecoveryIds).toContain("recovered-a");
					for (const other of ["a", "b", "c"] as const)
						if (other !== owner) expect(repository.read(destination(other)).unconfirmedCaptureToken).toBeUndefined();
					expect(
						graph.deliveries.filter(
							(delivery) => delivery.recoveryId === "recovered-a" && delivery.disposition === "completed",
						).length,
					).toBeGreaterThan(0);
					expect(ledger.readGraph().obligations.find((row) => row.obligationId === capture)?.chainId).toBeDefined();
				});
			}
});
