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
		// The resolution schedules the handoff pass, which settles the edge the
		// open obligation had blocked - no explicit transition or retry needed.
		expect(storageLedger.readGraph().edges[0].resolvedAs).toBe("settled");
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

		// The resolution's scheduled pass settles the next hop with the
		// completion's delivery recorded at its destination.
		expect(storageLedger.readGraph().edges.find((edge) => edge.edgeId === bc.edgeId)?.resolvedAs).toBe("settled");
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
		// The outcome is retained in the document, not discarded: its resolution
		// commits on the next ledger write instead of being lost with the
		// obligation that never got recorded.
		expect(resolved).toEqual([]);
		// The spent capture is cleared locally: the message went out.
		expect(repository.read(endpoint("a"))).toEqual({ draft: "", unconfirmed: null });
	});

	test("a failed obligation creation is retried once the ledger recovers", async () => {
		const { storageLedger, repository } = setup();
		let failBegin = true;
		const failing = new Proxy(storageLedger, {
			get(target, property) {
				const value = Reflect.get(target, property, target);
				if (property === "beginSubmission" && typeof value === "function")
					return (...args: unknown[]) => {
						if (failBegin) throw new Error("ledger unavailable");
						return (value as (...inner: unknown[]) => unknown).apply(target, args);
					};
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as DraftHandoffLedger;
		const library = new DraftLibrary(
			() => repository,
			undefined,
			() => failing,
		);
		repository.write(endpoint("a"), {
			draft: "current",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		const document = library.open(endpoint("a"));
		let finish!: () => void;
		const submitted = document.submitBuiltinCommand(
			() =>
				new Promise<boolean>((resolve) => {
					finish = () => resolve(true);
				}),
		);
		// The send went out; its obligation could not be recorded.
		expect(storageLedger.readGraph().obligations).toEqual([]);
		finish();
		await submitted;
		expect(storageLedger.readGraph().obligations).toEqual([]);

		// The ledger recovers: the retained begin adopts the pending capture, so
		// the queued resolution attaches its completion markers.
		failBegin = false;
		document.retry();
		expect(storageLedger.readGraph().obligations).toMatchObject([
			{ state: "completed", completedCommandIds: ["recovered-a"] },
		]);
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

	test("a completion lands where a transfer committed without its composed marker", async () => {
		const { storageLedger, repository } = setup();
		repository.write(endpoint("a"), {
			draft: "current",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		const failing = flaky(storageLedger, "recordComposed", 1);
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
		// The transfer committed, but its composed marker write failed: the
		// capture is durably at the destination while the graph still names the
		// origin as its owner.
		expect(repository.read(endpoint("b"))).toMatchObject({ unconfirmed: "current" });

		finish();
		await submitted;
		// The completion follows the durable capture evidence to the
		// destination instead of recreating a record at the retired origin.
		expect(repository.read(endpoint("b"))).toMatchObject({
			unconfirmed: null,
			completedCommandRecoveryIds: ["recovered-a"],
		});
		expect(repository.read(endpoint("a"))).toEqual({ draft: "", unconfirmed: null });

		library.retryPendingHandoffs();
		expect(storageLedger.readGraph().edges[0]).toMatchObject({ resolvedAs: "settled" });
	});

	test("a completion does not delete image bytes a live document's failed restore retains", async () => {
		const { storageLedger, repository } = setup();
		// The destination's first save fails once: the restored image stays in
		// the live document while the persisted record keeps the capture.
		let failWrite = true;
		const flakyRepository = new Proxy(repository, {
			get(target, property) {
				const value = Reflect.get(target, property, target);
				if (property === "write" && typeof value === "function")
					return (...args: unknown[]) => {
						if (failWrite && (args[0] as { sessionRef?: string } | undefined)?.sessionRef === "b") {
							failWrite = false;
							throw new Error("save failed");
						}
						return (value as (...inner: unknown[]) => unknown).apply(target, args);
					};
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as DraftRepository;
		const library = new DraftLibrary(
			() => flakyRepository,
			undefined,
			() => storageLedger,
		);
		repository.write(
			endpoint("a"),
			{
				draft: "current[image 1]",
				unconfirmed: null,
				images: [{ id: "photo", marker: 1, mediaType: "image/png" }],
			},
			[{ id: "photo", marker: 1, mediaType: "image/png", data: "AQID" }],
		);
		const source = library.open(endpoint("a"));
		let settle!: (sent: boolean) => void;
		const submitted = source.submit(
			() =>
				new Promise<boolean>((resolve) => {
					settle = () => resolve(true);
				}),
		);
		const token = repository.read(endpoint("a")).unconfirmedCaptureToken;
		if (token === undefined) throw new Error("missing capture token");
		expect(library.handoff(endpoint("a"), endpoint("b"))).toBe(true);
		const atB = library.open(endpoint("b"));
		atB.restore();
		// The failed restore kept the image in the live document's unsaved state
		// while the persisted record still holds the capture token.
		const live = atB.getSnapshot();
		expect(live.error).not.toBeNull();
		expect(live.record).toMatchObject({ draft: "current[image 1]" });
		expect(repository.read(endpoint("b")).unconfirmedCaptureToken).toBe(token);

		settle(true);
		await submitted;
		// The completion merges through the live document's own save path; the
		// persisted record behind it was never written, so the image bytes the
		// restored draft references survive and later saves keep working.
		expect(repository.read(endpoint("b"))).toMatchObject({ draft: "current[image 1]", unconfirmed: null });
		expect(repository.read(endpoint("b")).unconfirmedCaptureToken).toBeUndefined();
		expect(repository.read(endpoint("b")).images).toEqual([{ id: "photo", marker: 1, mediaType: "image/png" }]);
		expect(repository.imageInputs(endpoint("b"), [{ id: "photo", marker: 1, mediaType: "image/png" }])).toEqual([
			expect.objectContaining({ data: "AQID" }),
		]);
		expect(atB.getSnapshot().error).toBeNull();
		expect(() => atB.retry()).not.toThrow();
		library.retryPendingHandoffs();
		expect(storageLedger.readGraph().edges.every((edge) => edge.state !== "pending")).toBe(true);
	});

	test("a settled completion's markers reach the identity owning the retargeted rows", async () => {
		const { storage, storageLedger, repository, library } = setup();
		// A submitting outbox row lets each retarget move recovery rows with it.
		const intent = (note: string): MutationIntent => ({
			targetRef: targetKey("a"),
			threadId: "thread-a",
			method: "notes/human/set",
			payload: { ref: "a", expectedInstanceId: "instance-a", note },
			attachments: [],
			optimisticDisplay: null,
		});
		await storage.enqueueIntent(intent("drives the retarget"));
		const captured = await storage.enqueueIntent(intent("recovered command input"));
		await storage.transferToRecovery(captured.clientMutationId, "editing");
		repository.write(endpoint("a"), {
			draft: "current",
			unconfirmed: null,
			composer: { recoveryIds: [captured.clientMutationId] },
		});
		// An occupied intermediate identity with its own draft.
		repository.write(endpoint("b"), { draft: "b work", unconfirmed: null });
		const source = library.open(endpoint("a"));
		let finish!: () => void;
		const submitted = source.submitBuiltinCommand(
			() =>
				new Promise<boolean>((resolve) => {
					finish = () => resolve(true);
				}),
		);

		// Two retargets move the recovered row to the terminal identity.
		resume(storage, "a", "b");
		resume(storage, "b", "c");
		expect(await storage.getRecovery(captured.clientMutationId)).toMatchObject({ targetRef: targetKey("c") });

		// The occupied intermediate's own draft moves on first; the delayed
		// predecessor then lands its running capture at the intermediate.
		library.retryPendingHandoffs();
		expect(repository.read(endpoint("c")).draft).toBe("b work");
		expect(repository.read(endpoint("b")).unconfirmedCaptureToken).toBeDefined();

		// Late command completion: the markers land at the capture's owner.
		finish();
		await submitted;
		expect(repository.read(endpoint("b")).completedCommandRecoveryIds).toEqual([captured.clientMutationId]);

		library.retryPendingHandoffs();
		// The settled dispositions reach the identity that owns the row...
		expect(repository.read(endpoint("c")).completedCommandRecoveryIds).toEqual([captured.clientMutationId]);
		// ...so the already-executed command cannot be restored there.
		expect(
			library.open(endpoint("c")).appendEditedRecovery({
				version: 1,
				clientMutationId: captured.clientMutationId,
				targetRef: targetKey("c"),
				method: "turn/start",
				payload: { input: [{ type: "text", text: "encoded" }] },
				attachments: [],
				composerText: "encoded",
				optimisticDisplay: {},
				intentSequence: 1,
				createdAt: 1,
				state: "canceled",
				attempted: false,
				recoveryKind: "editing",
			}),
		).toBe("already-restored");
		expect(storageLedger.readGraph().edges.find((edge) => edge.from.sessionRef === "a")?.resolvedAs).toBe("settled");
	});

	test("a completion between a failed transfer and its retry reaches the destination", async () => {
		const { storage, storageLedger, repository } = setup();
		// Every transfer attempt fails through one full retry pass - the
		// delivered hash is durable, the drafts transfer is not.
		let failedTransfers = 0;
		const flakyRepository = new Proxy(repository, {
			get(target, property) {
				const value = Reflect.get(target, property, target);
				if (property === "transfer" && typeof value === "function")
					return (...args: unknown[]) => {
						if (failedTransfers < 2) {
							failedTransfers++;
							throw new Error("transfer failed");
						}
						return (value as (...inner: unknown[]) => unknown).apply(target, args);
					};
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as DraftRepository;
		const library = new DraftLibrary(
			() => flakyRepository,
			undefined,
			() => storageLedger,
		);
		const intent = (note: string): MutationIntent => ({
			targetRef: targetKey("a"),
			threadId: "thread-a",
			method: "notes/human/set",
			payload: { ref: "a", expectedInstanceId: "instance-a", note },
			attachments: [],
			optimisticDisplay: null,
		});
		await storage.enqueueIntent(intent("drives the retarget"));
		const captured = await storage.enqueueIntent(intent("recovered command input"));
		await storage.transferToRecovery(captured.clientMutationId, "editing");
		repository.write(endpoint("a"), {
			draft: "current",
			unconfirmed: null,
			composer: { recoveryIds: [captured.clientMutationId] },
		});
		const source = library.open(endpoint("a"));
		let finish!: () => void;
		const submitted = source.submitBuiltinCommand(
			() =>
				new Promise<boolean>((resolve) => {
					finish = () => resolve(true);
				}),
		);
		resume(storage, "a", "b");
		library.retryPendingHandoffs();
		// The transfer failed after its delivered hash: the destination is still
		// empty and the capture stays with the origin.
		expect(repository.read(endpoint("b"))).toEqual({ draft: "", unconfirmed: null });

		// The recovered command completes at the origin while the transfer is
		// missing; the resolution's scheduled pass then composes again and the
		// re-transfer carries the markers to the destination.
		finish();
		await submitted;
		expect(repository.read(endpoint("b")).completedCommandRecoveryIds).toEqual([captured.clientMutationId]);

		library.retryPendingHandoffs();
		// The obligation's own resolved ids prove nothing about the destination:
		// the retry composes again, and the re-transfer carries the completion
		// markers to the destination that owns the retargeted row.
		expect(repository.read(endpoint("b")).completedCommandRecoveryIds).toEqual([captured.clientMutationId]);
		expect(
			library.open(endpoint("b")).appendEditedRecovery({
				version: 1,
				clientMutationId: captured.clientMutationId,
				targetRef: targetKey("b"),
				method: "turn/start",
				payload: { input: [{ type: "text", text: "encoded" }] },
				attachments: [],
				composerText: "encoded",
				optimisticDisplay: {},
				intentSequence: 1,
				createdAt: 1,
				state: "canceled",
				attempted: false,
				recoveryKind: "editing",
			}),
		).toBe("already-restored");
		expect(storageLedger.readGraph().edges.find((edge) => edge.from.sessionRef === "a")?.resolvedAs).toBe("settled");
	});

	test("a refusal forwards dispositions to the identity owning the retargeted rows", async () => {
		const { storage, storageLedger, repository, library } = setup();
		const intent = (note: string): MutationIntent => ({
			targetRef: targetKey("a"),
			threadId: "thread-a",
			method: "notes/human/set",
			payload: { ref: "a", expectedInstanceId: "instance-a", note },
			attachments: [],
			optimisticDisplay: null,
		});
		await storage.enqueueIntent(intent("drives the retarget"));
		const captured = await storage.enqueueIntent(intent("recovered command input"));
		await storage.transferToRecovery(captured.clientMutationId, "editing");
		repository.write(endpoint("a"), {
			draft: "current",
			unconfirmed: null,
			composer: { recoveryIds: [captured.clientMutationId] },
		});
		// Both the intermediate and the terminal identities hold their own work,
		// so every handoff refuses while the retargets move the rows on.
		repository.write(endpoint("b"), { draft: "b work", unconfirmed: null });
		repository.write(endpoint("c"), { draft: "c work", unconfirmed: null });
		const source = library.open(endpoint("a"));
		let finish!: () => void;
		const submitted = source.submitBuiltinCommand(
			() =>
				new Promise<boolean>((resolve) => {
					finish = () => resolve(true);
				}),
		);
		resume(storage, "a", "b");
		resume(storage, "b", "c");
		expect(await storage.getRecovery(captured.clientMutationId)).toMatchObject({ targetRef: targetKey("c") });
		library.retryPendingHandoffs();

		// The recovered command completes at the origin; both hops then refuse.
		finish();
		await submitted;
		library.retryPendingHandoffs();
		// Both hops refused; cleanup may already have retired their tombstones,
		// so the durable decisions are asserted through resolvedAs.
		expect(
			storageLedger
				.readGraph()
				.edges.map((edge) => edge.resolvedAs)
				.sort(),
		).toEqual(["refused", "refused"]);
		// The immediate destination received its markers...
		expect(repository.read(endpoint("b")).completedCommandRecoveryIds).toEqual([captured.clientMutationId]);
		// ...and so did the identity the retargets moved the row to, although no
		// draft was ever accepted there.
		expect(repository.read(endpoint("c")).completedCommandRecoveryIds).toEqual([captured.clientMutationId]);
		expect(
			library.open(endpoint("c")).appendEditedRecovery({
				version: 1,
				clientMutationId: captured.clientMutationId,
				targetRef: targetKey("c"),
				method: "turn/start",
				payload: { input: [{ type: "text", text: "encoded" }] },
				attachments: [],
				composerText: "encoded",
				optimisticDisplay: {},
				intentSequence: 1,
				createdAt: 1,
				state: "canceled",
				attempted: false,
				recoveryKind: "editing",
			}),
		).toBe("already-restored");
	});

	test("a failed resolution on a live source is retried once storage recovers", async () => {
		const { storageLedger, repository } = setup();
		// The occupied destination keeps the source live: no transfer retires it.
		repository.write(endpoint("b"), { draft: "b work", unconfirmed: null });
		const failing = flaky(storageLedger, "resolveSubmission", 1);
		const library = new DraftLibrary(
			() => repository,
			undefined,
			() => failing,
		);
		repository.write(endpoint("a"), {
			draft: "current",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		const source = library.open(endpoint("a"));
		let finish!: () => void;
		const submitted = source.submitBuiltinCommand(
			() =>
				new Promise<boolean>((resolve) => {
					finish = () => resolve(true);
				}),
		);
		// The edge is recorded; the occupied destination turns the transfer
		// away, so the source stays live and its hop stays pending.
		expect(library.handoff(endpoint("a"), endpoint("b"))).toBe(false);
		expect(storageLedger.readGraph().edges).toMatchObject([{ state: "pending" }]);

		// The command completes while its resolution write fails: the obligation
		// stays open and the chain stays blocked.
		finish();
		await submitted;
		expect(storageLedger.readGraph().obligations).toMatchObject([{ state: "open" }]);

		// The retry pass flushes the live source too, so the recovered store
		// accepts the resolution and the handoff stops waiting on it.
		library.retryPendingHandoffs();
		expect(storageLedger.readGraph().obligations).toMatchObject([
			{ state: "completed", completedCommandIds: ["recovered-a"] },
		]);
		expect(storageLedger.readGraph().edges.every((edge) => edge.state !== "pending")).toBe(true);
		expect(repository.read(endpoint("b")).completedCommandRecoveryIds).toEqual(["recovered-a"]);
	});

	test("a failed forwarding write is retried once the row owner saves again", async () => {
		const { storage, storageLedger, repository } = setup();
		// The row owner's first save fails once.
		let failWrite = true;
		const flakyRepository = new Proxy(repository, {
			get(target, property) {
				const value = Reflect.get(target, property, target);
				if (property === "write" && typeof value === "function")
					return (...args: unknown[]) => {
						if (failWrite && (args[0] as { sessionRef?: string } | undefined)?.sessionRef === "c") {
							failWrite = false;
							throw new Error("save failed");
						}
						return (value as (...inner: unknown[]) => unknown).apply(target, args);
					};
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as DraftRepository;
		const library = new DraftLibrary(
			() => flakyRepository,
			undefined,
			() => storageLedger,
		);
		const intent = (note: string): MutationIntent => ({
			targetRef: targetKey("a"),
			threadId: "thread-a",
			method: "notes/human/set",
			payload: { ref: "a", expectedInstanceId: "instance-a", note },
			attachments: [],
			optimisticDisplay: null,
		});
		await storage.enqueueIntent(intent("drives the retarget"));
		const captured = await storage.enqueueIntent(intent("recovered command input"));
		await storage.transferToRecovery(captured.clientMutationId, "editing");
		repository.write(endpoint("a"), {
			draft: "current",
			unconfirmed: null,
			composer: { recoveryIds: [captured.clientMutationId] },
		});
		repository.write(endpoint("b"), { draft: "b work", unconfirmed: null });
		const source = library.open(endpoint("a"));
		let finish!: () => void;
		const submitted = source.submitBuiltinCommand(
			() =>
				new Promise<boolean>((resolve) => {
					finish = () => resolve(true);
				}),
		);
		resume(storage, "a", "b");
		resume(storage, "b", "c");
		library.retryPendingHandoffs();
		finish();
		await submitted;

		// The resolution's scheduled pass attempted the forwarding write and its
		// save failed once, so the settlement stays pending with the markers
		// still owed to the row owner.
		expect(storageLedger.readGraph().edges.find((edge) => edge.from.sessionRef === "a")?.state).toBe("pending");

		// The next pass retries the row owner's own save - preserving its live
		// record - and the forwarded markers land.
		library.retryPendingHandoffs();
		expect(repository.read(endpoint("c")).completedCommandRecoveryIds).toEqual([captured.clientMutationId]);
		expect(storageLedger.readGraph().edges.find((edge) => edge.from.sessionRef === "a")?.resolvedAs).toBe("settled");
	});

	test("a new edge carries a reopened source's current composition", () => {
		const { storage, storageLedger, repository, library } = setup();
		repository.write(endpoint("a"), { draft: "first", unconfirmed: null });
		expect(library.handoff(endpoint("a"), endpoint("b"))).toBe(true);
		library.retryPendingHandoffs();
		expect(repository.read(endpoint("a"))).toEqual({ draft: "", unconfirmed: null });

		// The identity reopens and composes new work after the first handoff.
		const reopened = library.open(endpoint("a"));
		reopened.edit("second");
		expect(repository.read(endpoint("a")).draft).toBe("second");

		// A later retarget creates a fresh edge from the same endpoint: the new
		// edge must carry the current composition, never the snapshot the first
		// handoff carried away, or the transfer would delete the newer work.
		resume(storage, "a", "c");
		library.retryPendingHandoffs();
		expect(repository.read(endpoint("c")).draft).toBe("second");
		expect(storageLedger.readGraph().edges.every((edge) => edge.state !== "pending")).toBe(true);
	});

	test("a committed continuation settles an edge without its composed marker", async () => {
		const { storageLedger, repository } = setup();
		const failing = flaky(storageLedger, "recordComposed", 1);
		const library = new DraftLibrary(
			() => repository,
			undefined,
			() => failing,
		);
		repository.write(
			endpoint("a"),
			{
				draft: "work[image 1]",
				unconfirmed: null,
				composer: { recoveryIds: ["recovered-a"] },
				images: [{ id: "photo", marker: 1, mediaType: "image/png" }],
			},
			[{ id: "photo", marker: 1, mediaType: "image/png", data: "AQID" }],
		);
		const source = library.open(endpoint("a"));
		let finish!: () => void;
		const submitted = source.submitBuiltinCommand(
			() =>
				new Promise<boolean>((resolve) => {
					finish = () => resolve(true);
				}),
		);
		// The first transfer commits; its composed-marker write fails.
		expect(library.handoff(endpoint("a"), endpoint("b"))).toBe(true);
		// The continuation carries the composition onward before any retry,
		// leaving the first edge's destination empty.
		expect(library.handoff(endpoint("b"), endpoint("c"))).toBe(true);
		expect(repository.read(endpoint("b"))).toEqual({ draft: "", unconfirmed: null });

		finish();
		await submitted;
		library.retryPendingHandoffs();
		// The first hop settles from the committed continuation instead of
		// re-transferring the retired source - whose image bytes the first
		// transfer already moved - and duplicating the composition.
		expect(storageLedger.readGraph().edges.every((edge) => edge.state !== "pending")).toBe(true);
		const firstHop = storageLedger.readGraph().edges.find((edge) => edge.from.sessionRef === "a");
		expect(firstHop?.resolvedAs).toBe("settled");
		expect(repository.read(endpoint("b"))).toEqual({ draft: "", unconfirmed: null });
		expect(repository.read(endpoint("c"))).toMatchObject({
			unconfirmed: null,
			completedCommandRecoveryIds: ["recovered-a"],
		});
	});

	test("a retried begin attaches to a recorded edge and reconciles its completion", async () => {
		const { storage, storageLedger, repository } = setup();
		const intent = (note: string): MutationIntent => ({
			targetRef: targetKey("a"),
			threadId: "thread-a",
			method: "notes/human/set",
			payload: { ref: "a", expectedInstanceId: "instance-a", note },
			attachments: [],
			optimisticDisplay: null,
		});
		await storage.enqueueIntent(intent("drives the retarget"));
		const captured = await storage.enqueueIntent(intent("recovered command input"));
		await storage.transferToRecovery(captured.clientMutationId, "editing");
		repository.write(endpoint("a"), {
			draft: "current",
			unconfirmed: null,
			composer: { recoveryIds: [captured.clientMutationId] },
		});
		// An occupied destination refuses the handoff.
		repository.write(endpoint("b"), { draft: "b work", unconfirmed: null });
		let failBegin = true;
		const failing = new Proxy(storageLedger, {
			get(target, property) {
				const value = Reflect.get(target, property, target);
				if (property === "beginSubmission" && typeof value === "function")
					return (...args: unknown[]) => {
						if (failBegin) throw new Error("ledger down");
						return (value as (...inner: unknown[]) => unknown).apply(target, args);
					};
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as DraftHandoffLedger;
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
		const token = repository.read(endpoint("a")).unconfirmedCaptureToken;
		if (token === undefined) throw new Error("missing capture token");

		// The edge and the retargeted row are recorded while the obligation's
		// begin is still failing: the hop refuses without waiting.
		resume(storage, "a", "b");
		library.retryPendingHandoffs();
		expect(storageLedger.readGraph().obligations).toEqual([]);
		expect(storageLedger.readGraph().edges[0]).toMatchObject({ state: "refused" });

		// Storage recovers; the command completes. The retried begin registers
		// the obligation, and its completion reconciles the dispositions with the
		// identity that owns the retargeted row.
		failBegin = false;
		finish();
		await submitted;
		library.retryPendingHandoffs();
		expect(storageLedger.readGraph().obligations).toMatchObject([{ state: "completed" }]);
		expect(repository.read(endpoint("b")).completedCommandRecoveryIds).toEqual([captured.clientMutationId]);
		expect(
			library.open(endpoint("b")).appendEditedRecovery({
				version: 1,
				clientMutationId: captured.clientMutationId,
				targetRef: targetKey("b"),
				method: "turn/start",
				payload: { input: [{ type: "text", text: "encoded" }] },
				attachments: [],
				composerText: "encoded",
				optimisticDisplay: {},
				intentSequence: 1,
				createdAt: 1,
				state: "canceled",
				attempted: false,
				recoveryKind: "editing",
			}),
		).toBe("already-restored");
	});

	test("a self completion saves through the normal path and clears a save error", async () => {
		const { storageLedger, repository } = setup();
		// The submission's own write succeeds; the mid-flight edit's write fails.
		let writes = 0;
		const flakyRepository = new Proxy(repository, {
			get(target, property) {
				const value = Reflect.get(target, property, target);
				if (property === "write" && typeof value === "function")
					return (...args: unknown[]) => {
						if ((args[0] as { sessionRef?: string } | undefined)?.sessionRef === "a" && ++writes === 2)
							throw new Error("save failed");
						return (value as (...inner: unknown[]) => unknown).apply(target, args);
					};
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as DraftRepository;
		const library = new DraftLibrary(
			() => flakyRepository,
			undefined,
			() => storageLedger,
		);
		repository.write(endpoint("a"), { draft: "current", unconfirmed: null });
		const document = library.open(endpoint("a"));
		let finish!: () => void;
		const submitted = document.submit(
			() =>
				new Promise<boolean>((resolve) => {
					finish = () => resolve(true);
				}),
		);
		// Typing during the submission hits a transient save failure.
		document.edit("typed mid-flight");
		expect(document.getSnapshot().error).not.toBeNull();

		finish();
		await submitted;
		// The completion saves through the document's normal path: storage
		// recovered, so the error clears and sending is available again.
		expect(document.getSnapshot().error).toBeNull();
		expect(repository.read(endpoint("a"))).toMatchObject({ draft: "typed mid-flight", unconfirmed: null });
	});

	test("a stale retired snapshot never deletes newer source work", () => {
		const { storageLedger, repository } = setup();
		// The transfer commits; its composed marker and its settlement both
		// fail, so the edge stays pending and retains the retired snapshot.
		const failing = flaky(flaky(storageLedger, "recordComposed", 1), "settleEdge", 1);
		const library = new DraftLibrary(
			() => repository,
			undefined,
			() => failing,
		);
		repository.write(endpoint("a"), { draft: "original work", unconfirmed: null });
		expect(library.handoff(endpoint("a"), endpoint("b"))).toBe(false);
		expect(storageLedger.readGraph().edges[0]).toMatchObject({ state: "pending", composedAt: null });

		// The destination clears its copy, and the reopened source composes
		// fresh work: the reviewer's exact recovery sequence.
		library.open(endpoint("b")).edit("");
		library.open(endpoint("a")).edit("newer work");

		library.retryPendingHandoffs();
		// The retry must not move the stale snapshot over the source's newer
		// durable work; the committed transfer's own evidence settles the
		// edge instead, and the newer work survives untouched.
		expect(repository.read(endpoint("a"))).toMatchObject({ draft: "newer work" });
		expect(repository.read(endpoint("b"))).toEqual({ draft: "", unconfirmed: null });
		expect(storageLedger.readGraph().edges[0].resolvedAs).toBe("settled");
	});

	test("a restart never re-transfers newer source work", () => {
		const { storageLedger, repository } = setup();
		// The transfer commits; its composed marker and its settlement both
		// fail, so the edge stays pending with only durable transfer evidence.
		const failing = flaky(flaky(storageLedger, "recordComposed", 1), "settleEdge", 1);
		const library = new DraftLibrary(
			() => repository,
			undefined,
			() => failing,
		);
		repository.write(endpoint("a"), { draft: "original work", unconfirmed: null });
		expect(library.handoff(endpoint("a"), endpoint("b"))).toBe(false);
		expect(storageLedger.readGraph().edges[0]).toMatchObject({ state: "pending", composedAt: null });

		// The destination clears its copy, and the reopened source composes
		// fresh work: the reviewer's exact recovery sequence.
		library.open(endpoint("b")).edit("");
		library.open(endpoint("a")).edit("newer work");

		// A restart loses the in-memory retired map that carried the
		// committed-transfer proof until now: only durable evidence may
		// protect the newer source work.
		const restarted = new DraftLibrary(
			() => repository,
			undefined,
			() => storageLedger,
		);
		restarted.retryPendingHandoffs();
		// The retry must not move the source's newer durable work to the
		// cleared destination; the committed transfer's durable evidence
		// settles the edge instead, and the newer work survives untouched.
		expect(repository.read(endpoint("a"))).toMatchObject({ draft: "newer work" });
		expect(repository.read(endpoint("b"))).toEqual({ draft: "", unconfirmed: null });
		expect(storageLedger.readGraph().edges[0].resolvedAs).toBe("settled");
	});

	test("a live source edited after a failed transfer moves its current record", () => {
		const { storageLedger, repository } = setup();
		// The delivery marker records, then the transfer savepoint fails once.
		let transfers = 0;
		const flakyRepository = new Proxy(repository, {
			get(target, property) {
				const value = Reflect.get(target, property, target);
				if (property === "transfer" && typeof value === "function")
					return (...args: unknown[]) => {
						if (++transfers === 1) throw new Error("transfer failed");
						return (value as (...inner: unknown[]) => unknown).apply(target, args);
					};
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as DraftRepository;
		const library = new DraftLibrary(
			() => flakyRepository,
			undefined,
			() => storageLedger,
		);
		repository.write(endpoint("a"), { draft: "one", unconfirmed: null });
		expect(library.handoff(endpoint("a"), endpoint("b"))).toBe(false);
		// The source is edited while the delivery marker still names the old
		// bytes, and the transfer never landed.
		library.open(endpoint("a")).edit("two");

		library.retryPendingHandoffs();
		// The retry moves the source's current record instead of refusing the
		// handoff and stranding the updated composition at the old identity.
		expect(repository.read(endpoint("b"))).toMatchObject({ draft: "two" });
		expect(repository.read(endpoint("a"))).toEqual({ draft: "", unconfirmed: null });
		expect(storageLedger.readGraph().edges[0].resolvedAs).toBe("settled");
	});

	test("a cleared destination is never resurrected by a stale re-transfer", () => {
		const { storageLedger, repository } = setup();
		const failing = flaky(flaky(storageLedger, "recordComposed", 1), "settleEdge", 1);
		const library = new DraftLibrary(
			() => repository,
			undefined,
			() => failing,
		);
		repository.write(endpoint("a"), { draft: "original work", unconfirmed: null });
		expect(library.handoff(endpoint("a"), endpoint("b"))).toBe(false);
		// The destination discards the transferred composition.
		library.open(endpoint("b")).edit("");

		library.retryPendingHandoffs();
		// The discarded composition does not come back: the retained snapshot
		// is evidence the transfer committed, so the edge settles from that
		// evidence and the destination keeps its own state.
		expect(repository.read(endpoint("b"))).toEqual({ draft: "", unconfirmed: null });
		expect(storageLedger.readGraph().edges[0].resolvedAs).toBe("settled");
	});

	test("a destination that moved on settles the committed handoff without overwrite", () => {
		const { storageLedger, repository } = setup();
		const failing = flaky(flaky(storageLedger, "recordComposed", 1), "settleEdge", 1);
		const library = new DraftLibrary(
			() => repository,
			undefined,
			() => failing,
		);
		repository.write(endpoint("a"), { draft: "original work", unconfirmed: null });
		expect(library.handoff(endpoint("a"), endpoint("b"))).toBe(false);
		// The destination discards the transferred composition and composes
		// its own work.
		library.open(endpoint("b")).edit("");
		library.open(endpoint("b")).edit("destination work");

		library.retryPendingHandoffs();
		// The destination's newer work survives untouched, and the committed
		// handoff settles instead of being refused as if it never happened.
		expect(repository.read(endpoint("b"))).toMatchObject({ draft: "destination work" });
		expect(storageLedger.readGraph().edges[0].resolvedAs).toBe("settled");
	});

	test("a converged capture stays findable across another chain's continuation", async () => {
		const { storage, storageLedger, repository } = setup();
		const library = new DraftLibrary(
			() => repository,
			undefined,
			() => storageLedger,
		);
		repository.write(endpoint("a"), {
			draft: "a work",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		// A recovered command runs while its draft hands off to B.
		const source = library.open(endpoint("a"));
		let finish!: () => void;
		const submitted = source.submitBuiltinCommand(
			() =>
				new Promise<boolean>((resolve) => {
					finish = () => resolve(true);
				}),
		);
		resume(storage, "a", "b");
		expect(library.handoff(endpoint("a"), endpoint("b"))).toBe(true);
		// A second origin converges on B, then B continues to C: the newest
		// inbound edge hands the continuation the other chain.
		resume(storage, "x", "b");
		resume(storage, "b", "c");
		library.retryPendingHandoffs();
		expect(repository.read(endpoint("b"))).toEqual({ draft: "", unconfirmed: null });
		const token = repository.read(endpoint("c")).unconfirmedCaptureToken;
		if (token === undefined) throw new Error("missing capture at c");

		// The command completes after the convergence: its capture must be
		// findable at C even though the graph routes B -> C through the other
		// chain, or the completed command stays restorable there.
		finish();
		await submitted;
		const settled = repository.read(endpoint("c"));
		expect(settled.unconfirmedCaptureToken).toBeUndefined();
		expect(settled).toMatchObject({ completedCommandRecoveryIds: ["recovered-a"] });
	});

	test("a removed hub never receives late completion writes", async () => {
		const { library, storage, storageLedger, repository } = setup();
		const intent = (ref: string): MutationIntent => ({
			targetRef: targetKey(ref),
			threadId: `thread-${ref}`,
			method: "notes/human/set",
			payload: { ref, expectedInstanceId: `instance-${ref}`, note: "recovered command input" },
			attachments: [],
			optimisticDisplay: null,
		});
		// The command's recovery row targets another ref of the hub.
		const captured = await storage.enqueueIntent(intent("b"));
		await storage.transferToRecovery(captured.clientMutationId, "editing");
		storageLedger.beginSubmission("token-late", endpoint("a"), [captured.clientMutationId]);
		repository.write(endpoint("a"), {
			draft: "current",
			unconfirmed: null,
			composer: { recoveryIds: [captured.clientMutationId] },
			unconfirmedCaptureToken: "token-late",
		});
		// The submission resolves durably, a handoff settles into the hub, and
		// then the hub goes away with the resolution unapplied.
		storageLedger.resolveSubmission("token-late", "completed", [captured.clientMutationId]);
		expect(library.handoff(endpoint("a"), endpoint("d"))).toBe(true);
		library.removeHub("hub");
		library.retryPendingHandoffs();
		// The late reconciliation must not recreate the removed hub's deleted
		// draft storage, and its ledger work leaves the graph.
		expect(repository.read(endpoint("b"))).toEqual({ draft: "", unconfirmed: null });
		expect(storageLedger.readGraph().edges).toEqual([]);
	});

	test("a converged continuation inherits the running command's obligation", async () => {
		const { storage, storageLedger, repository } = setup();
		const intent = (ref: string): MutationIntent => ({
			targetRef: targetKey(ref),
			threadId: `thread-${ref}`,
			method: "notes/human/set",
			payload: { ref, expectedInstanceId: `instance-${ref}`, note: "recovered command input" },
			attachments: [],
			optimisticDisplay: null,
		});
		const captured = await storage.enqueueIntent(intent("a"));
		// The retarget only moves rows while a live outbox row rides it, so a
		// driver intent keeps the resume real.
		await storage.enqueueIntent(intent("a"));
		await storage.transferToRecovery(captured.clientMutationId, "editing");
		const library = new DraftLibrary(
			() => repository,
			undefined,
			() => storageLedger,
		);
		repository.write(endpoint("a"), {
			draft: "a work",
			unconfirmed: null,
			composer: { recoveryIds: [captured.clientMutationId] },
		});
		// The recovered command runs while its draft converges on B.
		const source = library.open(endpoint("a"));
		let finish!: () => void;
		const submitted = source.submitBuiltinCommand(
			() =>
				new Promise<boolean>((resolve) => {
					finish = () => resolve(true);
				}),
		);
		const token = storageLedger.readGraph().obligations[0]?.obligationId;
		if (token === undefined) throw new Error("missing obligation");
		resume(storage, "a", "b");
		expect(library.handoff(endpoint("a"), endpoint("b"))).toBe(true);
		// A second origin converges on B, and the continuation selects its
		// chain - so the running command's obligation must ride it anyway.
		resume(storage, "x", "b");
		resume(storage, "b", "c");
		const bc = storageLedger.readGraph().edges.find((edge) => edge.from.sessionRef === "b");
		if (bc === undefined) throw new Error("missing continuation edge");
		expect(bc.obligations).toContain(token);

		library.retryPendingHandoffs();
		// The continuation waits for the running command: it cannot settle out
		// from under it, and the terminal's screen keeps the row excluded.
		expect(storageLedger.readGraph().edges.find((edge) => edge.edgeId === bc.edgeId)?.state).toBe("pending");
		expect([...(library.openObligationRecoveryIds(endpoint("c")) ?? new Set<string>())]).toContain(
			captured.clientMutationId,
		);

		finish();
		await submitted;
		library.retryPendingHandoffs();
		const settled = storageLedger.readGraph().edges.find((edge) => edge.edgeId === bc.edgeId);
		expect(settled?.resolvedAs).toBe("settled");
		const atC = repository.read(endpoint("c"));
		expect(atC.unconfirmedCaptureToken).toBeUndefined();
		expect(atC).toMatchObject({ completedCommandRecoveryIds: [captured.clientMutationId] });
	});

	test("an applied completion is never revisited by the routine pass", () => {
		const { storageLedger, repository } = setup();
		// The durable state a crash leaves behind: a resolved obligation whose
		// capture was never cleared.
		storageLedger.beginSubmission("token-done", endpoint("a"), []);
		repository.write(endpoint("a"), {
			draft: "work",
			unconfirmed: "work",
			unconfirmedCaptureToken: "token-done",
		});
		storageLedger.resolveSubmission("token-done", "completed", []);
		let reads = 0;
		const counting = new Proxy(repository, {
			get(target, property) {
				const value = Reflect.get(target, property, target);
				if (property === "read" && typeof value === "function")
					return (...args: unknown[]) => {
						reads++;
						return (value as (...inner: unknown[]) => unknown).apply(target, args);
					};
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as DraftRepository;
		const library = new DraftLibrary(
			() => counting,
			undefined,
			() => storageLedger,
		);
		library.retryPendingHandoffs();
		// The pass applied the completion at its capture owner.
		const settled = repository.read(endpoint("a"));
		expect(settled.unconfirmedCaptureToken).toBeUndefined();
		expect(settled).toMatchObject({ unconfirmed: null });

		// The acknowledged obligation is excluded from every later routine
		// pass: typing must not revisit the whole submission history.
		const afterFirst = reads;
		library.retryPendingHandoffs();
		library.retryPendingHandoffs();
		expect(reads).toBe(afterFirst);
	});

	test("unavailable ownership defers recovery restoration", () => {
		const { storageLedger, repository } = setup();
		const library = new DraftLibrary(
			() => repository,
			undefined,
			() => storageLedger,
		);
		// A running command's recovery row must stay excluded from restoration.
		storageLedger.beginSubmission("token-run", endpoint("a"), ["recovered-run"]);
		let failReads = true;
		const failing = new Proxy(storageLedger, {
			get(target, property) {
				const value = Reflect.get(target, property, target);
				if ((property === "readGraph" || property === "readOutstandingGraph") && typeof value === "function")
					return (...args: unknown[]) => {
						if (failReads) throw new Error("ledger down");
						return (value as (...inner: unknown[]) => unknown).apply(target, args);
					};
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as DraftHandoffLedger;
		const broken = new DraftLibrary(
			() => repository,
			undefined,
			() => failing,
		);
		// An unreadable ledger means ownership is unknown, not empty: the
		// restoration must defer instead of clearing a running command.
		expect(broken.openObligationRecoveryIds(endpoint("a"))).toBeUndefined();
		failReads = false;
		// With the ledger readable again the exclusion names the row.
		expect([...(library.openObligationRecoveryIds(endpoint("a")) ?? new Set<string>())]).toContain("recovered-run");
		// A record that cannot be read is unknown ownership too.
		let failRecord = true;
		const unreadable = new Proxy(repository, {
			get(target, property) {
				const value = Reflect.get(target, property, target);
				if (property === "read" && typeof value === "function")
					return (...args: unknown[]) => {
						if (failRecord) throw new Error("draft store down");
						return (value as (...inner: unknown[]) => unknown).apply(target, args);
					};
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as DraftRepository;
		const blind = new DraftLibrary(
			() => unreadable,
			undefined,
			() => storageLedger,
		);
		expect(blind.openObligationRecoveryIds(endpoint("a"))).toBeUndefined();
		failRecord = false;
		expect(blind.openObligationRecoveryIds(endpoint("a"))).toBeDefined();
	});

	test("a failed resolution write retries itself after storage recovers", async () => {
		const { storageLedger, repository } = setup();
		const failing = flaky(storageLedger, "resolveSubmission", 1);
		const library = new DraftLibrary(
			() => repository,
			undefined,
			() => failing,
		);
		repository.write(endpoint("a"), { draft: "current", unconfirmed: null });
		const document = library.open(endpoint("a"));
		let finish!: () => void;
		const submitted = document.submit(
			() =>
				new Promise<boolean>((resolve) => {
					finish = () => resolve(true);
				}),
		);
		vi.useFakeTimers();
		try {
			finish();
			await submitted;
			// The resolution write failed: the obligation stays open.
			expect(storageLedger.readGraph().obligations[0]?.state).toBe("open");
			// No user action and no explicit retry: the document retries the
			// owed write itself once the transient failure clears.
			await vi.advanceTimersByTimeAsync(1500);
		} finally {
			vi.useRealTimers();
		}
		expect(storageLedger.readGraph().obligations[0]?.state).toBe("completed");
	});

	test("routine reconciliation reads outstanding work, not history", () => {
		const { storageLedger, repository } = setup();
		let fullReads = 0;
		const counting = new Proxy(storageLedger, {
			get(target, property) {
				const value = Reflect.get(target, property, target);
				if (property === "readGraph" && typeof value === "function")
					return (...args: unknown[]) => {
						fullReads++;
						return (value as (...inner: unknown[]) => unknown).apply(target, args);
					};
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as DraftHandoffLedger;
		const library = new DraftLibrary(
			() => repository,
			undefined,
			() => counting,
		);
		// Settled history: an applied and acknowledged completion.
		storageLedger.beginSubmission("token-old", endpoint("a"), []);
		repository.write(endpoint("a"), {
			draft: "work",
			unconfirmed: "work",
			unconfirmedCaptureToken: "token-old",
		});
		storageLedger.resolveSubmission("token-old", "completed", []);
		library.retryPendingHandoffs();
		expect(repository.read(endpoint("a")).unconfirmedCaptureToken).toBeUndefined();
		// A keystroke's pass and exclusion read must not load the whole graph.
		const before = fullReads;
		library.retryPendingHandoffs();
		expect(library.openObligationRecoveryIds(endpoint("a"))?.size ?? 0).toBe(0);
		expect(fullReads).toBe(before);
	});

	test("a completed outcome survives a failed completion save", async () => {
		const { storageLedger, repository } = setup();
		// Both stores fail while the operation itself succeeds: the ledger
		// rejects the resolution twice, the draft save of the completion fails.
		const failingLedger = flaky(storageLedger, "resolveSubmission", 2);
		let writes = 0;
		const failingRepository = new Proxy(repository, {
			get(target, property) {
				const value = Reflect.get(target, property, target);
				if (property === "write" && typeof value === "function")
					return (...args: unknown[]) => {
						if ((args[0] as { sessionRef?: string } | undefined)?.sessionRef === "a" && ++writes === 2)
							throw new Error("save failed");
						return (value as (...inner: unknown[]) => unknown).apply(target, args);
					};
				return typeof value === "function" ? (value as (...inner: unknown[]) => unknown).bind(target) : value;
			},
		}) as DraftRepository;
		const library = new DraftLibrary(
			() => failingRepository,
			undefined,
			() => failingLedger,
		);
		repository.write(endpoint("a"), {
			draft: "current",
			unconfirmed: null,
			composer: { recoveryIds: ["recovered-a"] },
		});
		const document = library.open(endpoint("a"));
		let finish!: () => void;
		const submitted = document.submitBuiltinCommand(
			() =>
				new Promise<boolean>((resolve) => {
					finish = () => resolve(true);
				}),
		);
		vi.useFakeTimers();
		try {
			finish();
			// The operation completed, but its completion save failed: the
			// rejection must not demote the completed outcome.
			await expect(submitted).rejects.toThrow("save failed");
			expect(storageLedger.readGraph().obligations[0]?.state).toBe("open");
			// Storage recovers: the queued completed outcome resolves with its
			// recovery ids, so the executed command is not restorable.
			await vi.advanceTimersByTimeAsync(1500);
		} finally {
			vi.useRealTimers();
		}
		expect(storageLedger.readGraph().obligations[0]).toMatchObject({
			state: "completed",
			completedCommandIds: ["recovered-a"],
		});
		const settled = repository.read(endpoint("a"));
		expect(settled.unconfirmedCaptureToken).toBeUndefined();
		expect(settled).toMatchObject({ completedCommandRecoveryIds: ["recovered-a"] });
	});

	test("a handled failure stops contributing routine work", () => {
		const { storageLedger, repository } = setup();
		// A failed operation keeps its capture recoverable in the draft record.
		storageLedger.beginSubmission("token-fail", endpoint("a"), ["recovered-fail"]);
		repository.write(endpoint("a"), {
			draft: "current",
			unconfirmed: "current",
			unconfirmedComposer: { recoveryIds: ["recovered-fail"] },
			unconfirmedCaptureToken: "token-fail",
		});
		storageLedger.resolveSubmission("token-fail", "failed");
		// The failed obligation never loads for routine reconciliation again,
		// so a keystroke's pass does not traverse its historical handoffs.
		expect(storageLedger.readOutstandingGraph().obligations.map((row) => row.obligationId)).not.toContain("token-fail");
		// Its recoverable capture is preserved untouched in the draft record.
		expect(repository.read(endpoint("a")).unconfirmedCaptureToken).toBe("token-fail");
	});

	test("a recovery pass applies a resolved completion with no pending edge", () => {
		const { storageLedger, repository } = setup();
		// The durable state a crash leaves behind: the resolution committed to
		// the ledger, the capture it owns was never cleared, and no edge ever
		// waited for the obligation.
		storageLedger.beginSubmission("token-x", endpoint("a"), ["recovered-a"]);
		repository.write(endpoint("a"), {
			draft: "current",
			unconfirmed: "current",
			unconfirmedComposer: { recoveryIds: ["recovered-a"] },
			unconfirmedCaptureToken: "token-x",
		});
		storageLedger.resolveSubmission("token-x", "completed", ["recovered-a"]);

		// The ledger recovers on a restart: the recovery pass must apply the
		// completion at the capture owner even though no edge waited for it,
		// so the completed command cannot come back as editable input.
		const restarted = new DraftLibrary(
			() => repository,
			undefined,
			() => storageLedger,
		);
		restarted.retryPendingHandoffs();
		const settled = repository.read(endpoint("a"));
		expect(settled.unconfirmedCaptureToken).toBeUndefined();
		expect(settled).toMatchObject({
			unconfirmed: null,
			completedCommandRecoveryIds: ["recovered-a"],
		});
		expect(
			restarted.open(endpoint("a")).appendEditedRecovery({
				version: 1,
				clientMutationId: "recovered-a",
				targetRef: targetKey("a"),
				method: "turn/start",
				payload: { input: [{ type: "text", text: "encoded" }] },
				attachments: [],
				composerText: "encoded",
				optimisticDisplay: {},
				intentSequence: 1,
				createdAt: 1,
				state: "canceled",
				attempted: false,
				recoveryKind: "editing",
			}),
		).toBe("already-restored");
	});

	test("a reopened origin's durable record owns its new capture", () => {
		const { storageLedger, repository } = setup();
		// A historical handoff A -> B settles and cleans up.
		repository.write(endpoint("a"), { draft: "old work", unconfirmed: null });
		const first = new DraftLibrary(
			() => repository,
			undefined,
			() => storageLedger,
		);
		expect(first.handoff(endpoint("a"), endpoint("b"))).toBe(true);
		first.retryPendingHandoffs();
		expect(storageLedger.readGraph().edges[0]).toMatchObject({ resolvedAs: "settled", state: "cleaned" });

		// A crash leaves a resolved submission at the reopened origin: no edge
		// adopts it, and its capture sits in A's own durable record.
		storageLedger.beginSubmission("token-fresh", endpoint("a"), ["recovered-new"]);
		repository.write(endpoint("a"), {
			draft: "fresh work",
			unconfirmed: "fresh work",
			unconfirmedComposer: { recoveryIds: ["recovered-new"] },
			unconfirmedCaptureToken: "token-fresh",
		});
		storageLedger.resolveSubmission("token-fresh", "completed", ["recovered-new"]);

		// On restart the completion must apply at A - whose record durably
		// holds the token - not at B, the terminal of the unrelated
		// historical edge the graph would otherwise route it through.
		const restarted = new DraftLibrary(
			() => repository,
			undefined,
			() => storageLedger,
		);
		restarted.retryPendingHandoffs();
		const settled = repository.read(endpoint("a"));
		expect(settled.unconfirmedCaptureToken).toBeUndefined();
		expect(settled).toMatchObject({
			completedCommandRecoveryIds: ["recovered-new"],
		});
		expect(repository.read(endpoint("b"))).toEqual({ draft: "old work", unconfirmed: null });
		expect(
			restarted.open(endpoint("a")).appendEditedRecovery({
				version: 1,
				clientMutationId: "recovered-new",
				targetRef: targetKey("a"),
				method: "turn/start",
				payload: { input: [{ type: "text", text: "encoded" }] },
				attachments: [],
				composerText: "encoded",
				optimisticDisplay: {},
				intentSequence: 1,
				createdAt: 1,
				state: "canceled",
				attempted: false,
				recoveryKind: "editing",
			}),
		).toBe("already-restored");
	});
});
