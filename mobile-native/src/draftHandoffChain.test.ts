import { afterEach, describe, expect, test, vi } from "vitest";
import { DraftLibrary } from "./draftLibrary";
import type { DraftHandoffEndpoint, DraftHandoffLedger, DraftSubmissionLedger } from "./draftHandoffLedger";
import type { DraftDocument } from "./draftDocument";
import { DraftRepository } from "./draftRepository";
import { MutationOutboxSQLite } from "./mutationOutboxStorage";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "./sqliteSync.testkit";
import type { MutationIntent } from "@evener/appwire-client/state/mutation";
import { MutationNotSentError } from "./conversation/state/conversationMutation";

// The ledger's default id source is expo-crypto through the storage adapter;
// the counter makes chain and edge ids stable across assertions.
let expoCryptoCalls = 0;
vi.mock("expo-crypto", () => ({
	randomUUID: () => `uuid-${++expoCryptoCalls}`,
	getRandomValues: (array: Uint8Array) => array,
}));

const databases: SqliteDoubleDatabase[] = [];
afterEach(() => {
	for (const db of databases.splice(0)) db.close();
});

const endpoint = (sessionRef: string): DraftHandoffEndpoint => ({ hubId: "hub", sessionRef });

/** The composite target key every native mutation row and retarget uses. */
const targetKey = (sessionRef: string): string => JSON.stringify(["hub", sessionRef]);

function setup() {
	const mutations = openSqliteSyncDouble();
	databases.push(mutations.database);
	let next = 0;
	const storage = new MutationOutboxSQLite(mutations.port, {
		createMutationId: () => `mutation-${++next}`,
		now: () => 1000,
	});
	const drafts = openSqliteSyncDouble();
	databases.push(drafts.database);
	const repository = new DraftRepository(drafts.port);
	const library = new DraftLibrary(
		() => repository,
		undefined,
		() => storage.handoffLedger,
	);
	return { storage, storageLedger: storage.handoffLedger, repository, library, mutations };
}

/** A resume records the identity change in the retarget savepoint even when no
 * mutation row moves. */
function resume(storage: MutationOutboxSQLite, from: string, to: string): void {
	storage.retarget(targetKey(from), {
		ref: targetKey(to),
		wireRef: to,
		threadId: `thread-${to}`,
		instanceId: `instance-${to}`,
	});
}

function pendingSubmission(document: DraftDocument) {
	let settle!: (sent: boolean) => void;
	const submitted = document.submit(
		() =>
			new Promise<boolean>((resolve) => {
				settle = resolve;
			}),
	);
	return { submitted, settle: (sent: boolean) => settle(sent) };
}

/** A ledger fake for document-only tests: handoff transitions are unreachable
 * here, so any accidental use fails loudly. */
function submissionLedger(
	submission: Pick<DraftSubmissionLedger, "beginSubmission" | "resolveSubmission">,
): DraftHandoffLedger {
	const unused = () => {
		throw new Error("unused handoff transition");
	};
	return {
		...submission,
		readGraph: unused,
		recordIdentityChange: unused,
		createEdge: unused,
		claimDestination: unused,
		recordDelivered: unused,
		recordComposed: unused,
		settleEdge: unused,
		refuseEdge: unused,
		cleanEdge: unused,
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

describe("handoff chain obligations", () => {
	test("records an identity change even when no mutation row moves", () => {
		const { storage, storageLedger } = setup();
		expect(resume(storage, "a", "b")).toBeUndefined();
		const graph = storageLedger.readGraph();
		expect(graph.edges).toMatchObject([{ from: endpoint("a"), to: endpoint("b"), state: "pending" }]);
	});

	test("one document records one submission obligation at a time", async () => {
		const { library, storageLedger } = setup();
		const document = library.open(endpoint("a"));
		document.edit("current");
		const first = pendingSubmission(document);
		expect(storageLedger.readGraph().obligations).toHaveLength(1);

		// A second send or a built-in command cannot start while the first is in
		// flight, so it records no second obligation.
		expect(await document.submitBuiltinCommand(async () => true)).toBeUndefined();
		expect(storageLedger.readGraph().obligations).toHaveLength(1);

		first.settle(true);
		await first.submitted;
		expect(storageLedger.readGraph().obligations).toMatchObject([{ state: "completed" }]);
	});

	test("an open obligation blocks settlement and refusal of its edge", async () => {
		const { library, repository, storage, storageLedger } = setup();
		const document = library.open(endpoint("a"));
		document.edit("current");
		const first = pendingSubmission(document);
		const token = repository.read(endpoint("a")).unconfirmedCaptureToken;
		if (token === undefined) throw new Error("missing capture token");
		resume(storage, "a", "b");
		const edge = storageLedger.readGraph().edges[0];
		expect(edge.obligations).toEqual([token]);
		expect(() => storageLedger.settleEdge(edge.edgeId, [], "hash")).toThrow(/open obligation/);
		expect(() => storageLedger.refuseEdge(edge.edgeId, [])).toThrow(/open obligation/);
		expect(storageLedger.readGraph().edges[0].state).toBe("pending");

		first.settle(true);
		await first.submitted;
		expect(storageLedger.readGraph().obligations[0].state).toBe("completed");
		storageLedger.settleEdge(edge.edgeId, [], "hash");
		expect(storageLedger.readGraph().edges[0].state).toBe("settled");
	});

	test("a new edge carries the chain's open obligations from more than one origin", async () => {
		const { library, repository, storage, storageLedger } = setup();
		const source = library.open(endpoint("a"));
		source.edit("one");
		const atA = pendingSubmission(source);
		const tokenA = repository.read(endpoint("a")).unconfirmedCaptureToken;
		if (tokenA === undefined) throw new Error("missing capture token at a");
		resume(storage, "a", "b");

		const middle = library.open(endpoint("b"));
		middle.edit("two");
		const atB = pendingSubmission(middle);
		const tokenB = repository.read(endpoint("b")).unconfirmedCaptureToken;
		if (tokenB === undefined) throw new Error("missing capture token at b");
		resume(storage, "b", "c");

		const graph = storageLedger.readGraph();
		const ab = graph.edges.find((edge) => edge.from.sessionRef === "a");
		const bc = graph.edges.find((edge) => edge.from.sessionRef === "b");
		if (ab === undefined || bc === undefined) throw new Error("missing chain edges");
		expect(bc.chainId).toBe(ab.chainId);
		expect([...bc.obligations].sort()).toEqual([tokenA, tokenB].sort());
		expect(
			graph.obligations
				.filter((obligation) => obligation.chainId === ab.chainId)
				.map((obligation) => obligation.obligationId)
				.sort(),
		).toEqual([tokenA, tokenB].sort());

		atA.settle(true);
		atB.settle(true);
		await Promise.all([atA.submitted, atB.submitted]);
		expect(storageLedger.readGraph().obligations.every((obligation) => obligation.state === "completed")).toBe(true);
	});

	test("a submission at a refused hop's destination rides the next hop", async () => {
		const { library, repository, storage, storageLedger } = setup();
		resume(storage, "a", "b");
		const ab = storageLedger.readGraph().edges[0];
		// The destination already owns work, so the hop is refused, not retried.
		storageLedger.refuseEdge(ab.edgeId, []);

		repository.write(endpoint("b"), {
			draft: "occupied",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-b"] },
		});
		const atB = library.open(endpoint("b"));
		let finish!: () => void;
		const submitted = atB.submitBuiltinCommand(
			() =>
				new Promise<boolean>((resolve) => {
					finish = () => resolve(true);
				}),
		);
		const token = repository.read(endpoint("b")).unconfirmedCaptureToken;
		if (token === undefined) throw new Error("missing capture token at b");

		resume(storage, "b", "c");
		const graph = storageLedger.readGraph();
		const bc = graph.edges.find((edge) => edge.from.sessionRef === "b");
		if (bc === undefined) throw new Error("missing redirected edge");
		expect(bc.chainId).toBe(ab.chainId);
		expect(bc.obligations).toEqual([token]);

		finish();
		await submitted;
		const completed = storageLedger.readGraph().obligations.find((obligation) => obligation.obligationId === token);
		expect(completed).toMatchObject({ state: "completed", completedCommandIds: ["recovered-b"] });

		storageLedger.refuseEdge(bc.edgeId, [
			{
				chainId: bc.chainId,
				destination: endpoint("c"),
				recoveryId: "recovered-b",
				disposition: "completed",
				edgeId: bc.edgeId,
			},
		]);
		expect(storageLedger.readGraph().deliveries).toMatchObject([
			{ destination: endpoint("c"), recoveryId: "recovered-b", disposition: "completed" },
		]);
	});

	test("one obligation reaches the terminal hop and is not reattached after it resolves", async () => {
		const { library, repository, storage, storageLedger } = setup();
		const source = library.open(endpoint("a"));
		source.edit("current");
		const first = pendingSubmission(source);
		const token = repository.read(endpoint("a")).unconfirmedCaptureToken;
		if (token === undefined) throw new Error("missing capture token");
		resume(storage, "a", "b");
		resume(storage, "b", "c");

		let graph = storageLedger.readGraph();
		expect(graph.edges.filter((edge) => edge.obligations.includes(token))).toHaveLength(2);
		first.settle(true);
		await first.submitted;
		storageLedger.resolveSubmission(token, "completed");
		storageLedger.resolveSubmission(token, "completed");

		resume(storage, "c", "d");
		graph = storageLedger.readGraph();
		expect(graph.obligations).toHaveLength(1);
		expect(graph.obligations[0]).toMatchObject({ obligationId: token, state: "completed" });
		const cd = graph.edges.find((edge) => edge.from.sessionRef === "c");
		expect(cd?.obligations).toEqual([]);
	});

	test("a predecessor waits for the edge that continues its chain", () => {
		const { storage, storageLedger } = setup();
		resume(storage, "a", "b");
		resume(storage, "b", "c");
		const [ab, bc] = storageLedger.readGraph().edges;
		expect(ab.dependencies).toEqual([bc.edgeId]);
		expect(() => storageLedger.settleEdge(ab.edgeId, [], "hash")).toThrow(/waits on/);
		storageLedger.settleEdge(bc.edgeId, [], "hash");
		storageLedger.settleEdge(ab.edgeId, [], "hash");
		expect(storageLedger.readGraph().edges.map((edge) => edge.state)).toEqual(["settled", "settled"]);
	});

	test("rolls the mutation row move back when recording the edge fails", async () => {
		const { storage, storageLedger, mutations } = setup();
		const intent: MutationIntent = {
			targetRef: targetKey("a"),
			threadId: "thread-a",
			method: "notes/human/set",
			payload: { ref: "a", expectedInstanceId: "instance-a", note: "waiting" },
			attachments: [],
			optimisticDisplay: null,
		};
		await storage.enqueueIntent(intent);
		mutations.database.exec(
			"CREATE TRIGGER reject_handoff_edge BEFORE INSERT ON draft_handoff_edges BEGIN SELECT RAISE(ABORT, 'reject'); END",
		);
		expect(() => resume(storage, "a", "b")).toThrow(/reject/);
		mutations.database.exec("DROP TRIGGER reject_handoff_edge");
		expect(await storage.listOutbox(targetKey("a"))).toHaveLength(1);
		expect(storageLedger.readGraph().edges).toEqual([]);
	});

	test("a local refusal after the capture is gone still resolves the obligation", async () => {
		const { library, repository, storageLedger } = setup();
		const document = library.open(endpoint("a"));
		document.edit("current");
		let reject!: (error: Error) => void;
		const submitted = document.submit(
			() =>
				new Promise<boolean>((_resolve, rejectPromise) => {
					reject = rejectPromise;
				}),
		);
		const token = repository.read(endpoint("a")).unconfirmedCaptureToken;
		if (token === undefined) throw new Error("missing capture token");
		// The hub is removed while the submission is in flight, so its capture
		// token no longer matches the document's snapshot.
		document.forget();
		reject(new MutationNotSentError("the session retired"));
		expect(await submitted).toBe("not-sent");
		expect(storageLedger.readGraph().obligations).toMatchObject([
			{ obligationId: token, origin: endpoint("a"), state: "not-sent" },
		]);
	});

	test("an unavailable obligation ledger does not stop an ordinary send", async () => {
		const { repository } = setup();
		let token: string | undefined;
		const resolved: [string, string][] = [];
		const library = new DraftLibrary(
			() => repository,
			undefined,
			() =>
				submissionLedger({
					beginSubmission: (obligationId: string) => {
						token = obligationId;
						throw new Error("ledger unavailable");
					},
					resolveSubmission: (obligationId: string, state: "completed" | "failed") => {
						resolved.push([obligationId, state]);
					},
				}),
		);
		const document = library.open(endpoint("a"));
		document.edit("current");
		let sent = false;
		// The send still reaches the transport's direct-request path: the
		// uncertainty capture is already durable, so an obligation store that
		// cannot record the submission does not stop it.
		expect(
			await document.submit(async () => {
				sent = true;
				return true;
			}),
		).toBe(true);
		expect(sent).toBe(true);
		expect(token).toBeDefined();
		// The resolution still records the outcome for the next ledger write.
		expect(resolved).toEqual([[token, "completed"]]);
		// The spent capture is cleared locally: the message went out.
		expect(repository.read(endpoint("a"))).toEqual({ draft: "", unconfirmed: null });
	});

	test("a failed obligation resolution is retried on the next document reload", async () => {
		const { repository } = setup();
		const resolved: [string, string, readonly string[]][] = [];
		let failResolve = true;
		const library = new DraftLibrary(
			() => repository,
			undefined,
			() =>
				submissionLedger({
					beginSubmission: () => {},
					resolveSubmission: (obligationId: string, state: "completed" | "failed", ids: readonly string[] = []) => {
						if (failResolve) {
							failResolve = false;
							throw new Error("write failed");
						}
						resolved.push([obligationId, state, ids]);
					},
				}),
		);
		const document = library.open(endpoint("a"));
		document.edit("current");
		expect(await document.submit(async () => true)).toBe(true);
		// The first resolution write failed and stayed queued.
		expect(resolved).toEqual([]);
		document.retry();
		expect(resolved).toHaveLength(1);
		expect(resolved[0][1]).toBe("completed");
	});

	test("a failed resolution after retirement is retried by the library's recovery pass", async () => {
		const { storageLedger, repository } = setup();
		repository.write(endpoint("a"), {
			draft: "current",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		const failing = flaky(storageLedger, "resolveSubmission", 1);
		const library = new DraftLibrary(
			() => repository,
			undefined,
			() => failing,
		);
		const source = library.open(endpoint("a"));
		let finish!: () => void;
		const submitted = source.submitBuiltinCommand(
			() =>
				new Promise<boolean>((resolve) => {
					finish = () => resolve(true);
				}),
		);
		expect(library.handoff(endpoint("a"), endpoint("b"))).toBe(true);

		finish();
		await submitted;
		// The first resolution write failed after retirement; the library retries
		// every retired document before restoring or settling.
		expect(storageLedger.readGraph().obligations).toMatchObject([{ state: "open" }]);
		library.retryPendingHandoffs();
		expect(storageLedger.readGraph().obligations).toMatchObject([
			{ state: "completed", completedCommandIds: ["recovered-a"] },
		]);
	});

	test("retrying a failed obligation resolution preserves the failed capture", async () => {
		const { storageLedger, repository } = setup();
		const failing = flaky(storageLedger, "resolveSubmission", 1);
		const library = new DraftLibrary(
			() => repository,
			undefined,
			() => failing,
		);
		const document = library.open(endpoint("a"));
		document.edit("current");
		document.addImage({ id: "photo", marker: 1, mediaType: "image/png", data: "AQID" });
		// The transport fails after the uncertain capture is durable, and the
		// resolution write fails too, so the failed resolution is queued.
		expect(await document.submit(async () => false)).toBe(false);
		const token = repository.read(endpoint("a")).unconfirmedCaptureToken;
		if (token === undefined) throw new Error("missing capture token");
		expect(storageLedger.readGraph().obligations).toMatchObject([{ state: "open" }]);

		document.retry();
		// The queued failed resolution commits...
		expect(storageLedger.readGraph().obligations).toMatchObject([{ state: "failed" }]);
		// ...but a failed operation's capture stays recoverable: the retry
		// must not clear the capture or delete its image references.
		const retained = repository.read(endpoint("a"));
		expect(retained).toMatchObject({ unconfirmed: "current[image 1]", unconfirmedCaptureToken: token });
		expect(retained.unconfirmedImages).toEqual([{ id: "photo", marker: 1, mediaType: "image/png" }]);
		expect(repository.imageInputs(endpoint("a"), [{ id: "photo", marker: 1, mediaType: "image/png" }])).toEqual([
			expect.objectContaining({ data: "AQID" }),
		]);
		expect(document.getSnapshot().record).toMatchObject({
			unconfirmed: "current[image 1]",
			unconfirmedCaptureToken: token,
		});
	});
});
