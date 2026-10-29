import {
	type CompletionDeliveryOutcome,
	captureInFlight,
	type DraftCompletionDelivery,
	DraftDocument,
	mergeCompletion,
	messageAdmissionCapture,
	submissionBeginUnrecorded,
	submissionBeginsUnrecorded,
} from "./draftDocument";
import type {
	DraftHandoffDelivery,
	DraftHandoffEdge,
	DraftHandoffEndpoint,
	DraftHandoffLedger,
	DraftHandoffGraph,
	DraftSubmissionObligation,
} from "./draftHandoffLedger";
import { clearsCapture } from "./draftHandoffLedger";
import {
	captureTerminal,
	compareEdges,
	type HandoffOrdering,
	orderHandoffGraph,
	readOutstandingHandoffGraph,
	readHandoffGraph,
	terminalDestination,
} from "./draftHandoffGraph";
import type { DraftComposerMetadata, DraftDestination, DraftRecord, DraftRepository } from "./draftRepository";

export type DraftStorage = Pick<
	DraftRepository,
	"read" | "write" | "transfer" | "removeHub" | "imageInputs" | "refsWithDrafts"
> & {
	/** Durable evidence of a handoff edge's committed transfer, when the
	 * drafts store keeps one. */
	readTransferEvidence?: (edgeId: string) => { hubId: string; sessionRef: string; deliveredHash: string } | undefined;
};

/** One document per destination keeps pending work coherent across navigation.
 * Handoffs themselves live in the durable ledger: this class reads the graph,
 * claims, composes, decides, and cleans, but keeps no pending handoff state of
 * its own. */
export class DraftLibrary {
	private hubs = new Map<string, Map<string, DraftDocument>>();
	/** Documents retired by a handoff, keyed by the edge that transferred them:
	 * only that edge's own retry reads the snapshot it wrote back, and the
	 * snapshot is durable evidence the transfer committed - rememberRetired
	 * runs only after the repository write - so that edge never re-transfers
	 * it. A newer edge from the same endpoint reads the endpoint's current
	 * document instead. The entry keeps the hub so hub removal still
	 * invalidates retired sources. */
	private retired = new Map<string, { hubId: string; document: DraftDocument }>();
	/** Hubs whose storage was removed: every later open maps to one inert,
	 * never-loading document, so a late obligation resolution cannot recreate
	 * the hub's deleted drafts. */
	private removedHubs = new Map<string, DraftDocument>();
	/** Consumers that must hear a durable obligation transition, so a mounted
	 * destination recomputes its recovery exclusions and re-runs reconciliation
	 * without waiting for a remount. */
	private obligationListeners = new Set<() => void>();
	constructor(
		private repository: () => DraftStorage,
		private createImageId: () => string = () =>
			`reissue-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`,
		/** The mutations database's handoff graph. Without it the library cannot
		 * hand drafts across identities: it never keeps a private pending copy. */
		private handoffLedger?: () => DraftHandoffLedger,
	) {}

	open(destination: DraftDestination): DraftDocument {
		// A removed hub never reopens: a fresh document here would persist into
		// the storage the removal just deleted.
		const removed = this.removedHubs.get(destination.hubId);
		if (removed !== undefined) return removed;
		// A resolved completion is applied before a screen reads the document, so
		// it never renders a stale capture.
		this.applyObligationsFor(destination);
		let sessions = this.hubs.get(destination.hubId);
		if (!sessions) {
			sessions = new Map();
			this.hubs.set(destination.hubId, sessions);
		}
		let document = sessions.get(destination.sessionRef);
		if (!document) {
			document = new DraftDocument(
				this.repository,
				destination,
				this.createImageId,
				undefined,
				undefined,
				this.handoffLedger,
				(terminal, completion) => this.deliverCompletionAt(terminal, completion),
				() => this.onObligationResolved(),
			);
			sessions.set(destination.sessionRef, document);
		}
		return document;
	}

	/** Subscribe to durable obligation transitions. Every resolution publishes -
	 * completed, not-sent, and failed alike - because a failed outcome lifts
	 * the destination's suppression just as much as a successful one. Returns
	 * an unsubscribe. */
	onObligationTransition(listener: () => void): () => void {
		this.obligationListeners.add(listener);
		return () => {
			this.obligationListeners.delete(listener);
		};
	}

	/** One obligation durably resolved: publish the transition and schedule the
	 * handoff pass, which every edge waiting on the obligation just unblocked.
	 * Deferring keeps the pass off the resolving submission's own stack. */
	private onObligationResolved(): void {
		queueMicrotask(() => {
			for (const listener of [...this.obligationListeners]) listener();
			this.retryPendingHandoffs();
		});
	}

	/** Hand a destination's complete draft, image bytes and settlement markers
	 * to the identity a resume resolved to, before the replacement screen opens.
	 * An already-open destination adopts the record into its live snapshot, and
	 * an in-flight submission on the retired document settles through it. Only
	 * an empty destination, or one this edge already wrote, may take the handoff.
	 */
	handoff(from: DraftDestination, to: DraftDestination): boolean {
		if (sameDestination(from, to)) return true;
		const ledger = this.availableHandoffLedger();
		if (ledger === undefined) return false;
		// A begin the source document still owes the ledger rides this edge:
		// flush it first, so the edge is recorded with the open obligation
		// attached and its decision waits for the running submission.
		this.open(from).flushObligationResolutions();
		const pending = this.pendingEdge(ledger, from, to);
		if (pending !== undefined) return this.applyEdge(ledger, pending);
		// A source that gave its composition away and composed nothing since
		// would hand a new identity an empty snapshot; a reopened source with
		// fresh work hands that work over instead.
		if (this.sourceRetiredIdle(from)) return false;
		const history = this.pairHistory(ledger, from, to);
		// A read that fails is incomplete, never empty: the identity-change
		// action defers its decision instead of throwing through the resume
		// flow, and a durable edge stays for a later retry.
		if (history === undefined) return false;
		if (history.some((entry) => samePair(entry, from, to) && entry.state === "settled")) return true;
		if (history.some((entry) => samePair(entry, from, to) && (entry.state === "refused" || entry.state === "cleaned")))
			return false;
		const edge = this.createEdge(ledger, from, to);
		if (edge === undefined) {
			return false;
		}
		return this.applyEdge(ledger, edge);
	}

	/** Retry every eligible edge in deterministic order: clean what finished,
	 * then attempt the graph's order. An edge that stays pending blocks the
	 * edges that depend on it. */
	retryPendingHandoffs(): void {
		const ledger = this.availableHandoffLedger();
		if (ledger === undefined) return;
		// A failed resolution write leaves its obligation open and the whole
		// chain blocked; a live source whose transfer an occupied destination
		// turned away needs the flush just as much as a retired one.
		for (const sessions of this.hubs.values())
			for (const document of sessions.values()) document.flushObligationResolutions();
		// A retired document can still hold a resolution its ledger write missed.
		for (const entry of this.retired.values()) entry.document.retry();
		const load = readOutstandingHandoffGraph(ledger);
		if (load.state !== "ready") return;
		// Resolved obligations still owe routine work until their dispositions
		// reach the identities owning their recovery rows and their completion
		// applies at the identity that owns their capture - an unrecorded begin
		// retried after its handoff decided, or a completion stranded by a
		// crash between its resolution and its capture clear. An obligation
		// whose completion is durably applied is acknowledged and never
		// revisited, so the pass costs what is still unresolved, not the
		// device's whole submission history.
		this.reconcileResolvedObligations(ledger, load.graph);
		// A reconciled obligation unblocks the edges waiting on it, so the
		// ordering must read the graph that exists after the reconciliation.
		if (this.reconcileAbandonedObligations(ledger, load.graph)) {
			const fresh = readOutstandingHandoffGraph(ledger);
			if (fresh.state !== "ready") return;
			this.cleanFinished(ledger, fresh.graph);
			this.attemptOrderedHandoffs(ledger, fresh.graph, orderHandoffGraph(fresh));
			return;
		}
		this.cleanFinished(ledger, load.graph);
		const ordering = orderHandoffGraph(load);
		this.attemptOrderedHandoffs(ledger, load.graph, ordering);
	}

	/** Transfers compose upstream first — a composition lands at its destination
	 * before a continuation moves it onward. Settlement then decides downstream
	 * first: a predecessor settles only after the edge that continues its
	 * chain, which the ledger itself enforces. */
	private attemptOrderedHandoffs(
		ledger: DraftHandoffLedger,
		graph: DraftHandoffGraph,
		ordering: HandoffOrdering,
	): void {
		if (ordering.state !== "ready") return;
		const byId = new Map(graph.edges.map((edge) => [edge.edgeId, edge]));
		const predecessors = ordering.blocked
			.map((entry) => byId.get(entry.edgeId))
			.filter((candidate): candidate is DraftHandoffEdge => candidate !== undefined)
			.sort(compareEdges);
		for (const edge of [...predecessors, ...[...ordering.order].reverse()]) {
			try {
				// Transfers only: the fresh graph reflects the composition the
				// previous attempt in this pass just committed.
				const load = readOutstandingHandoffGraph(ledger);
				if (load.state !== "ready") continue;
				const fresh = load.graph.edges.find((e) => e.edgeId === edge.edgeId);
				if (fresh !== undefined) this.deliver(ledger, fresh, load.graph, false);
			} catch {
				// A failing transfer stays pending for the next retry.
			}
		}
		for (const edge of [...ordering.order, ...predecessors]) {
			try {
				this.applyEdge(ledger, edge);
			} catch {
				// A failing decision stays pending for the next retry.
			}
		}
	}

	/** An obligation a previous process left open has no live submission to
	 * resolve it: the promise died with that process, and nothing in this one
	 * holds its capture in flight. Resolve each as failed — the state that
	 * releases the chain's in-flight ownership while leaving the capture's
	 * uncertain input recoverable — so edges stop waiting for a completion that
	 * cannot arrive and destination recovery stops suppressing its sources. */
	private reconcileAbandonedObligations(ledger: DraftHandoffLedger, graph: DraftHandoffGraph): boolean {
		let reconciled = false;
		for (const obligation of graph.obligations) {
			if (obligation.state !== "open" || captureInFlight(obligation.obligationId)) continue;
			try {
				ledger.resolveSubmission(obligation.obligationId, "failed");
				reconciled = true;
			} catch {
				// The store that cannot accept the resolution keeps the obligation
				// open; the next retry reconciles it again.
			}
		}
		return reconciled;
	}

	/** Whether a handoff is still trying to reach this destination. False only
	 * when the ledger proves no pending edge exists: an unavailable ledger or a
	 * failed graph read leaves pendingness unknown - undefined - and the caller
	 * must defer whatever a pending transfer would own, the same fail-closed
	 * rule as openObligationRecoveryIds. */
	hasPendingHandoff(to: DraftDestination): boolean | undefined {
		const ledger = this.availableHandoffLedger();
		if (ledger === undefined) return undefined;
		const load = readOutstandingHandoffGraph(ledger);
		if (load.state !== "ready") return undefined;
		return load.graph.edges.some((edge) => edge.state === "pending" && sameDestination(edge.to, to));
	}

	private pendingEdge(
		ledger: DraftHandoffLedger,
		from: DraftDestination,
		to: DraftDestination,
	): DraftHandoffEdge | undefined {
		const history = this.pairHistory(ledger, from, to);
		if (history === undefined) return undefined;
		return history
			.filter((edge) => edge.state === "pending" && samePair(edge, from, to))
			.sort((a, b) =>
				a.createdAt === b.createdAt
					? a.edgeId < b.edgeId
						? -1
						: a.edgeId > b.edgeId
							? 1
							: 0
					: a.createdAt - b.createdAt,
			)[0];
	}

	/** The edges one identity pair ever carried: the identity-change action
	 * is pair-scoped, so it reads the pair's own history - never the device's
	 * whole handoff graph - and ledgers without the pair read fall back to
	 * the whole graph. */
	private pairHistory(
		ledger: DraftHandoffLedger,
		from: DraftDestination,
		to: DraftDestination,
	): DraftHandoffEdge[] | undefined {
		try {
			const between = ledger.edgesBetween?.(from, to);
			if (between !== undefined) return between;
			const load = readHandoffGraph(ledger);
			return load.state === "ready" ? load.graph.edges : undefined;
		} catch {
			return undefined;
		}
	}

	/** A handoff without a recorded retarget (tests, or a resume path that did
	 * not pass through retarget) still gets its durable edge. */
	private createEdge(
		ledger: DraftHandoffLedger,
		from: DraftDestination,
		to: DraftDestination,
	): DraftHandoffEdge | undefined {
		try {
			ledger.recordIdentityChange(from, to);
		} catch {
			return undefined;
		}
		return this.pendingEdge(ledger, from, to);
	}

	private applyEdge(ledger: DraftHandoffLedger, edge: DraftHandoffEdge): boolean {
		// The resolution-reconciliation path prices in unresolved work only:
		// it runs after every submission resolves, so the whole-history read
		// here would grow every ordinary send's cost with the device's
		// history. The outstanding graph holds every non-cleaned edge plus
		// the cleaned paths outstanding obligations reached, the dependencies
		// its edges cite, and every obligation those edges carry.
		const load = readOutstandingHandoffGraph(ledger);
		if (load.state !== "ready") return false;
		// The caller's edge object can predate this call's own writes (a
		// transfer pass in the same retry); only the freshly read edge carries
		// the delivered hash and composed marker those writes committed.
		const fresh = load.graph.edges.find((candidate) => candidate.edgeId === edge.edgeId);
		if (fresh === undefined) return false;
		const ordering = orderHandoffGraph(load);
		if (ordering.state !== "ready") return false;
		const blocked = ordering.blocked.find((entry) => entry.edgeId === fresh.edgeId);
		// A dependency that is not settled stops the settle/refuse decision, not
		// the transfer: a predecessor's composition must still land at its
		// destination so the continuation can carry it onward. An open obligation
		// defers the decision the same way.
		const planned =
			ordering.order.some((entry) => entry.edgeId === fresh.edgeId) ||
			blocked?.reason === "obligation" ||
			blocked?.reason === "dependency";
		if (!planned) return false;
		return this.deliver(ledger, fresh, load.graph, blocked === undefined);
	}

	private deliver(
		ledger: DraftHandoffLedger,
		edge: DraftHandoffEdge,
		graph: DraftHandoffGraph,
		mayDecide: boolean,
	): boolean {
		// A failed settlement leaves the edge pending after this edge retired the
		// source. This edge's own retired document still holds the record it
		// wrote, so its retry reads that; a different edge from the same endpoint
		// reads the endpoint's current document, never the snapshot an earlier
		// handoff carried away.
		const source = this.retired.get(edge.edgeId)?.document ?? this.open(edge.from);
		let snapshot = source.getSnapshot();
		if (!snapshot.loaded) {
			source.retry();
			snapshot = source.getSnapshot();
		}
		if (!snapshot.loaded) return false;
		// A hop whose empty source is still waiting on a pending, uncomposed
		// inbound transfer would move a half-finished composition: defer until
		// the predecessor lands, settles, or refuses. An occupied source is
		// different: its inbound can never compose, only refuse, so the source's
		// own work may move on.
		if (
			!isOccupied(snapshot.record) &&
			graph.edges.some(
				(inbound) =>
					inbound.edgeId !== edge.edgeId &&
					inbound.state === "pending" &&
					inbound.composedAt === null &&
					sameDestination(inbound.to, edge.from),
			)
		)
			return false;

		let claimed: boolean;
		try {
			claimed = ledger.claimDestination(edge.edgeId);
		} catch {
			return false;
		}
		if (!claimed) return mayDecide ? this.refuse(ledger, edge, source) : false;

		const destination = this.open(edge.to);
		let current = destination.getSnapshot();
		if (!current.loaded || current.error !== null) {
			destination.retry();
			current = destination.getSnapshot();
		}
		if (!current.loaded || current.error !== null || current.submitting) return false;

		const intended = compositionFingerprint(snapshot.record);
		if (edge.deliveredHash !== null) {
			const destinationNow = compositionFingerprint(current.record);
			if (edge.composedAt !== null) {
				// The transfer committed. This edge's own completion may have changed
				// the destination's submission fields since; the composition
				// fingerprint ignores those. Any composition change is someone else's
				// work, so refuse instead of resurrecting the stale source.
				if (destinationNow === edge.deliveredHash)
					return mayDecide ? this.settle(ledger, edge, destination, current.record, edge.deliveredHash) : true;
				// Unless the change is this chain's own doing: a continuation
				// accepted after this edge wrote its record can have carried
				// exactly this composition onward, and its delivered hash proves it.
				if (
					graph.edges.some(
						(continuation) =>
							continuation.edgeId !== edge.edgeId &&
							continuation.chainId === edge.chainId &&
							sameDestination(continuation.from, edge.to) &&
							continuation.deliveredHash === edge.deliveredHash &&
							(continuation.resolvedAs === "settled" || continuation.composedAt !== null),
					)
				)
					return mayDecide ? this.settle(ledger, edge, destination, current.record, edge.deliveredHash) : true;
				return mayDecide ? this.refuse(ledger, edge, source) : false;
			}
			// A matching composition fingerprint alone is not proof the transfer
			// landed: the fingerprint excludes unconfirmed content, selections, and
			// completion markers, so a source holding only those fingerprints
			// exactly like a destination that never received anything. Demand
			// the destination's durable record prove the capture and metadata
			// identity too.
			if (destinationNow === edge.deliveredHash) {
				if (this.transferProven(ledger, edge, snapshot.record)) {
					try {
						// The transfer committed without its composed marker; repair
						// it so the graph routes this chain to the identity that
						// holds the composition.
						ledger.recordComposed(edge.edgeId);
					} catch {
						// The marker stays missing; the next retry repairs it.
					}
					return mayDecide;
				}
			}
			// A committed continuation can have carried exactly this composition
			// onward from the destination while this edge's composed-marker write
			// failed: composing again from the retired source would duplicate the
			// composition at the destination, and an image-bearing snapshot has no
			// bytes left at the source to re-transfer at all.
			if (
				graph.edges.some(
					(continuation) =>
						continuation.edgeId !== edge.edgeId &&
						continuation.chainId === edge.chainId &&
						sameDestination(continuation.from, edge.to) &&
						continuation.deliveredHash === edge.deliveredHash &&
						(continuation.resolvedAs === "settled" || continuation.composedAt !== null),
				)
			)
				return mayDecide ? this.settle(ledger, edge, destination, current.record, edge.deliveredHash) : true;
			// A committed transfer is durable evidence, never memory: the
			// drafts store recorded it in the transfer's own savepoint, so a
			// restart cannot lose it the way the in-memory retired map can.
			// The source endpoint's current record is not this edge's
			// transfer material anymore - the reopened endpoint can hold
			// newer work - so it is never re-transferred and never deleted. A
			// destination that moved on from the delivered evidence, cleared
			// or recomposed, is never overwritten either: the mirror of the
			// rule that newer source work survives. The edge settles from the
			// delivered evidence and both sides keep what they hold now.
			const retained = this.retired.get(edge.edgeId);
			const committed = retained !== undefined || this.transferCommitted(edge.edgeId);
			if (committed === undefined) return false;
			if (committed)
				return mayDecide ? this.settleDelivered(ledger, edge, retained?.document.getSnapshot().record) : true;
			// A live source whose composition no longer matches the recorded
			// delivery hash was edited after a transfer that never landed: no
			// retirement exists, so nothing reached the destination. Refusing
			// would strand the updated composition at the old identity; the
			// fall-through re-records the current fingerprint and moves the
			// current record instead.
		}
		// A destination holding nothing but disposition markers still accepts
		// its claimed transfer: the markers are a competing refusal's
		// bookkeeping, not work, and refusing here would strand the claimed
		// transfer forever. The transfer merges them so the refusal's
		// dispositions survive the composition landing.
		let deliveredRecord = snapshot.record;
		if (isOccupied(current.record)) {
			if (!dispositionOnly(current.record)) return mayDecide ? this.refuse(ledger, edge, source) : false;
			deliveredRecord = {
				...snapshot.record,
				completedCommandRecoveryIds: [
					...new Set([
						...(current.record.completedCommandRecoveryIds ?? []),
						...(snapshot.record.completedCommandRecoveryIds ?? []),
					]),
				],
				releasedRecoveryIds: [
					...new Set([...(current.record.releasedRecoveryIds ?? []), ...(snapshot.record.releasedRecoveryIds ?? [])]),
				],
				recoveryResendAcks: [
					...new Set([...(current.record.recoveryResendAcks ?? []), ...(snapshot.record.recoveryResendAcks ?? [])]),
				],
			};
		}

		try {
			ledger.recordDelivered(edge.edgeId, intended);
		} catch {
			return false;
		}
		const images = [
			...(snapshot.record.images ?? []),
			...(snapshot.record.unconfirmedImages ?? []),
			...(snapshot.record.parkedCompositions ?? []).flatMap((parked) => parked.images ?? []),
		];
		try {
			const additions = images.map((image) => {
				const data = source.imagePreviews([image])[0]?.data;
				if (!data) throw new Error("Saved image is unavailable.");
				return { ...image, data };
			});
			this.repository().transfer(edge.from, edge.to, deliveredRecord, additions, {
				edgeId: edge.edgeId,
				deliveredHash: intended,
			});
		} catch {
			return false;
		}
		try {
			// Durable proof the transfer committed, so a retry never composes from
			// the retired source again.
			ledger.recordComposed(edge.edgeId);
		} catch {
			// The destination still shows the composed record; the next retry settles
			// from that comparison.
		}
		// The live document adopts the delivered record: the merged
		// dispositions must survive its next edit, or the storage record the
		// transfer just wrote would lose them on the next persist.
		destination.adopt(deliveredRecord);
		if (destination !== source) {
			source.retire();
			this.rememberRetired(edge, source);
			const sessions = this.hubs.get(edge.from.hubId);
			if (sessions?.get(edge.from.sessionRef) === source) sessions.delete(edge.from.sessionRef);
		}
		return mayDecide ? this.settle(ledger, edge, destination, deliveredRecord, intended) : true;
	}

	/** Durable evidence that an uncomposed edge's transfer actually landed. The
	 * composition fingerprint excludes captures, selections, and completion
	 * markers, and the source record can still mutate after delivery — the
	 * completion clears its capture and merges ids wherever the record then
	 * lived — so proof comes from the destination's durable record read against
	 * the edge's own obligations, never from the source alone. */
	private transferProven(ledger: DraftHandoffLedger, edge: DraftHandoffEdge, source: DraftRecord): boolean {
		let graph: DraftHandoffGraph;
		try {
			graph = ledger.readOutstandingGraph?.() ?? ledger.readGraph();
		} catch {
			return false;
		}
		let delivered: DraftRecord;
		try {
			delivered = this.repository().read(edge.to);
		} catch {
			return false;
		}
		const obligations = edge.obligations
			.map((obligationId) => graph.obligations.find((row) => row.obligationId === obligationId))
			.filter((row): row is DraftSubmissionObligation => row !== undefined);
		// Delivered dispositions must be proven by the destination's own durable
		// record. An obligation's resolution names the ids its completion merged
		// at the capture's owner - which can still be the source when a transfer
		// failed - so counting them as carried would settle an empty destination
		// and strand the markers behind the retargeted rows. The hop instead
		// composes again and the re-transfer carries them.
		const carried = new Set([
			...(delivered.completedCommandRecoveryIds ?? []),
			...(delivered.releasedRecoveryIds ?? []),
			...(delivered.recoveryResendAcks ?? []),
		]);
		return (
			sameSelections(source.composer, delivered.composer) &&
			(source.completedCommandRecoveryIds ?? []).every((id) => carried.has(id)) &&
			(source.releasedRecoveryIds ?? []).every((id) => carried.has(id)) &&
			(source.recoveryResendAcks ?? []).every((id) => carried.has(id)) &&
			captureProven(source, delivered, obligations)
		);
	}

	private refuse(ledger: DraftHandoffLedger, edge: DraftHandoffEdge, source: DraftDocument): boolean {
		const record = source.getSnapshot().record;
		const completed = [...(record.completedCommandRecoveryIds ?? [])];
		const released = releasedSources(record);
		const destination = this.open(edge.to);
		let current = destination.getSnapshot();
		if (!current.loaded || current.error !== null) {
			destination.retry();
			current = destination.getSnapshot();
		}
		// An unloaded or unsaved destination has an empty snapshot; merging
		// dispositions through it would replace its existing text and images
		// with exclusion markers, so leave the edge pending for the next retry.
		if (!current.loaded || current.error !== null) return false;
		try {
			if (completed.length && !destination.adoptCompletedCommandRecovery(completed)) return false;
			if (released.length && !destination.releaseRecoverySources(released)) return false;
		} catch {
			return false;
		}
		// The retargets can have moved the rows past the immediate destination
		// whether or not it accepted the draft; a refusal must leave the
		// markers with the rows too, or the executed command stays restorable at
		// the row owner.
		if (!this.forwardDispositions(ledger, edge, completed, released)) return false;
		try {
			ledger.refuseEdge(edge.edgeId, deliveriesFor(edge, completed, released));
		} catch {
			return false;
		}
		// Existing destination work wins; this is a settled disposition, not retry.
		return false;
	}

	private settle(
		ledger: DraftHandoffLedger,
		edge: DraftHandoffEdge,
		destination: DraftDocument,
		record: DraftRecord,
		deliveredHash: string,
	): boolean {
		const completed = [...(record.completedCommandRecoveryIds ?? [])];
		const released = releasedSources(record);
		// Resolved dispositions must reach the identity that owns the mutation
		// rows, wherever the capture settled. Retargets move a chain's rows on
		// with the identity while the composition - and its completion markers -
		// can stay at this hop, and a marker written only here cannot clean a row
		// that targets another identity: the executed command would stay
		// restorable there.
		if (!this.forwardDispositions(ledger, edge, completed, released)) return false;
		try {
			ledger.settleEdge(edge.edgeId, deliveriesFor(edge, completed, released), deliveredHash);
		} catch {
			return false;
		}
		destination.adopt(record);
		return true;
	}

	/** Settle an edge whose transfer already committed, from the delivered
	 * evidence. The destination keeps whatever it holds now - its own newer
	 * work, or its own clearing - while the delivery's dispositions still
	 * reach the identities owning their rows. After a restart only the
	 * durable evidence remains: the transferred record is wherever the
	 * destination left it, so no disposition of the moved record survives
	 * to forward. */
	private settleDelivered(
		ledger: DraftHandoffLedger,
		edge: DraftHandoffEdge,
		delivered: DraftRecord | undefined,
	): boolean {
		const completed = [...(delivered?.completedCommandRecoveryIds ?? [])];
		const released = delivered === undefined ? [] : releasedSources(delivered);
		if (!this.forwardDispositions(ledger, edge, completed, released)) return false;
		try {
			ledger.settleEdge(edge.edgeId, deliveriesFor(edge, completed, released), edge.deliveredHash ?? "");
		} catch {
			return false;
		}
		return true;
	}

	/** Whether the drafts store holds durable evidence this edge's transfer
	 * committed: written in the transfer's own savepoint. Undefined when the
	 * store cannot answer - unknown evidence never authorizes a destructive
	 * re-transfer. */
	private transferCommitted(edgeId: string): boolean | undefined {
		try {
			const evidence = this.repository().readTransferEvidence?.(edgeId);
			return evidence !== undefined && evidence !== null;
		} catch {
			return undefined;
		}
	}

	/** Write completed and released markers at every identity that durably owns
	 * the dispositions' mutation rows, when the rows traveled past this edge's
	 * own destination. Row ownership follows the retargets, which move rows
	 * with the identity regardless of whether the destination accepted the
	 * draft, so both settlements and refusals forward. False when a forwarding
	 * write cannot land, so the caller retries with the dispositions still
	 * recorded. */
	private forwardDispositions(
		ledger: DraftHandoffLedger,
		edge: DraftHandoffEdge,
		completed: readonly string[],
		released: readonly string[],
	): boolean {
		if (!completed.length && !released.length) return true;
		return this.writeDispositionsAtRowOwners(ledger, edge.to, edge.chainId, completed, released);
	}

	/** The row-owner half of disposition forwarding, shared by settlement,
	 * refusal, and the late reconciliation of obligations no edge waited for.
	 * `skip` is the identity that already received the dispositions. */
	private writeDispositionsAtRowOwners(
		ledger: DraftHandoffLedger,
		skip: DraftHandoffEndpoint,
		chainId: string,
		completed: readonly string[],
		released: readonly string[],
	): boolean {
		// Durable row ownership first: the mutation tables name the identity
		// each row currently targets, across refused handoffs and uncomposed
		// edges alike. Ledgers without that query fall back to the graph's
		// continuation trace from this edge's destination.
		let owners: DraftHandoffEndpoint[] | undefined;
		try {
			owners = ledger.recoveryRowOwners?.([...completed, ...released]);
		} catch {
			// A failed ownership read is unknown, never absent: the
			// dispositions defer - the obligation stays for the next pass
			// instead of trusting the graph's older trace and acknowledging
			// on it.
			return false;
		}
		const rowOwners: DraftHandoffEndpoint[] = [];
		if (owners !== undefined) {
			for (const owner of owners) if (!sameDestination(owner, skip)) rowOwners.push(owner);
		} else {
			const load = readOutstandingHandoffGraph(ledger);
			if (load.state !== "ready") return false;
			const traced = terminalDestination(load.graph.edges, skip, chainId);
			if (!sameDestination(traced, skip)) rowOwners.push(traced);
		}
		for (const rowOwner of rowOwners) {
			const document = this.open(rowOwner);
			let current = document.getSnapshot();
			// An unloaded row owner, or one whose last save failed, retries its
			// own persistence first - the retry preserves the live record's
			// edits and images - so a single failed forwarding write does not
			// leave the predecessor pending forever when nothing else repairs
			// this identity.
			if (!current.loaded || current.error !== null) {
				document.retry();
				current = document.getSnapshot();
			}
			// An unloaded or unsaved row owner must not receive markers through
			// an empty snapshot; the caller retries and forwards again.
			if (!current.loaded || current.error !== null) return false;
			try {
				if (completed.length && !document.adoptCompletedCommandRecovery(completed)) return false;
				if (released.length && !document.releaseRecoverySources(released)) return false;
			} catch {
				return false;
			}
		}
		return true;
	}

	/** Resolved obligations still owed routine work: their dispositions must
	 * reach the identities owning their recovery rows, and their completion
	 * must apply at the identity that owns their capture - an unrecorded begin
	 * retried after its handoff decided, or a completion stranded by a crash
	 * between its resolution and its capture clear. An obligation whose
	 * completion durably applied - or proved durably absent, with every read
	 * successful - is acknowledged in the ledger and excluded from later
	 * passes, so routine reconciliation costs what is still unresolved, not
	 * the device's whole submission history. */
	private reconcileResolvedObligations(ledger: DraftHandoffLedger, graph: DraftHandoffGraph): void {
		for (const obligation of graph.obligations) {
			if (!clearsCapture(obligation.state)) continue;
			if (obligation.appliedAt != null) continue;
			const completed = obligation.state === "completed" ? obligation.completedCommandIds : [];
			const released = obligation.releasedRecoveryIds;
			let readsFailed = false;
			let tokenSeen = false;
			const owner = captureTerminal(graph.edges, obligation, (endpoint) => {
				try {
					const record = this.repository().read(endpoint);
					if (record?.unconfirmedCaptureToken === obligation.obligationId) tokenSeen = true;
					return record;
				} catch {
					readsFailed = true;
					return undefined;
				}
			});
			if (
				(completed.length !== 0 || released.length !== 0) &&
				!this.writeDispositionsAtRowOwners(ledger, owner, obligation.chainId ?? "", completed, released)
			)
				continue;
			const outcome = this.applyCompletion(obligation, owner);
			// A delivered completion just landed; a token no durable record
			// holds, with every read successful, is durably absent. Either way
			// the obligation owes no further routine work; a failed read or
			// write retries on the next pass.
			if (!readsFailed && (outcome === "delivered" || !tokenSeen)) {
				try {
					ledger.acknowledgeAppliedCompletion?.(obligation.obligationId);
				} catch {
					// The next pass retries the acknowledgment.
				}
			}
		}
	}

	private cleanFinished(ledger: DraftHandoffLedger, graph: { edges: DraftHandoffEdge[] }): void {
		for (const edge of graph.edges)
			if (edge.state === "settled" || edge.state === "refused") this.tryClean(ledger, edge.edgeId);
	}

	/** Recovery sources of submissions still running on this destination's chain.
	 * The screen must not restore those rows while their obligation is open.
	 * Undefined means ownership could not be read - the ledger or a record was
	 * unavailable - which is unknown, not empty: the caller must defer
	 * restoration until ownership reads successfully, or a running command's
	 * sources could be restored and its capture cleared. */
	openObligationRecoveryIds(destination: DraftDestination): Set<string> | undefined {
		const ledger = this.availableHandoffLedger();
		if (ledger === undefined) return undefined;
		let graph;
		try {
			graph = ledger.readOutstandingGraph?.() ?? ledger.readGraph();
		} catch {
			return undefined;
		}
		const ids = new Set<string>();
		let readsFailed = false;
		for (const obligation of graph.obligations) {
			if (obligation.state !== "open") continue;
			// Exclusions follow durable capture ownership, not the graph's routing:
			// a transfer whose composed-marker write failed leaves the capture at
			// its destination while the graph still names the origin, and
			// restore-capable reconciliation at the capture's owner is what could
			// resurrect the still-running command and clear its capture token.
			const owner = captureTerminal(graph.edges, obligation, (endpoint) => {
				try {
					return this.repository().read(endpoint);
				} catch {
					readsFailed = true;
					return undefined;
				}
			});
			if (!sameDestination(owner, destination)) continue;
			for (const id of obligation.recoveryIds) ids.add(id);
		}
		// A begin that never recorded is ownership the graph cannot show: the
		// submission is running - or queued to resolve - with no obligation
		// row, so nothing above suppresses its capture. The record holding
		// the token owns the suppression, wherever a transfer moved it: an
		// unrecorded capture's sources must not be restored while its
		// submission is unresolved.
		if (submissionBeginsUnrecorded()) {
			let record: DraftRecord | undefined;
			try {
				record = this.repository().read(destination);
			} catch {
				return undefined;
			}
			const token = record?.unconfirmedCaptureToken;
			if (token !== undefined && submissionBeginUnrecorded(token))
				for (const id of record?.unconfirmedComposer?.recoveryIds ?? []) ids.add(id);
		}
		if (readsFailed) return undefined;
		return ids;
	}

	/** Apply a resolved completion to the record of one destination before it is
	 * opened. Idempotent: the capture token is cleared once. */
	private applyObligationsFor(destination: DraftDestination): void {
		const ledger = this.availableHandoffLedger();
		if (ledger === undefined) return;
		let graph;
		try {
			graph = ledger.readOutstandingGraph?.() ?? ledger.readGraph();
		} catch {
			return;
		}
		for (const obligation of graph.obligations) {
			if (!clearsCapture(obligation.state)) continue;
			// An acknowledged obligation's completion already applied; nothing
			// of it can render as a stale capture.
			if (obligation.appliedAt != null) continue;
			let readsFailed = false;
			// A transfer can commit while its composed-marker write fails, so the
			// graph can name an origin whose capture durably lives elsewhere.
			const owner = captureTerminal(graph.edges, obligation, (endpoint) => {
				try {
					return this.repository().read(endpoint);
				} catch {
					readsFailed = true;
					return undefined;
				}
			});
			// A record that cannot be read may be the one holding the capture:
			// unknown ownership defers to the retry pass, which re-reads.
			if (readsFailed) continue;
			if (!sameDestination(owner, destination)) continue;
			this.applyCompletion(obligation, owner);
		}
	}

	/** The recovery pass: every resolved completion still waiting on a capture is
	 * applied to the record that owns it. */
	private applyCompletion(
		obligation: DraftSubmissionObligation,
		terminal: DraftDestination,
	): CompletionDeliveryOutcome {
		return this.deliverCompletionAt(terminal, {
			captureToken: obligation.obligationId,
			state: obligation.state === "completed" ? "completed" : "not-sent",
			completedCommandIds: obligation.completedCommandIds,
			releasedRecoveryIds: obligation.releasedRecoveryIds,
		});
	}

	/** Merge a delivered completion into the terminal's live document when one
	 * is open, so its unsaved edits and pending image data survive through the
	 * document's own persistence path. A clean live document that cannot adopt
	 * the completion never held the capture - its snapshot is stale against a
	 * durable write another instance committed - so the persisted record at the
	 * terminal decides. A live document with a pending save error owns newer
	 * state instead: the completion merges through its own save path, never
	 * through the persisted record behind it. */
	private deliverCompletionAt(
		terminal: DraftDestination,
		completion: DraftCompletionDelivery,
	): CompletionDeliveryOutcome {
		const open = this.hubs.get(terminal.hubId)?.get(terminal.sessionRef);
		const live = open?.getSnapshot();
		if (open !== undefined && live?.loaded) {
			if (open.adoptCompletion(completion)) return "delivered";
			// The adoption's own save can fail after the in-memory merge, setting
			// the document's error. The snapshot taken before the attempt cannot
			// see that new error: re-read it, so the errored live document
			// retries the completion through its own persistence path instead
			// of a direct repository write that strands the document with its
			// error while the obligation acknowledges on the write's success.
			if (open.getSnapshot().error !== null) return open.mergeCompletionThroughLiveState(completion);
		}
		let current: DraftRecord;
		try {
			current = this.repository().read(terminal);
		} catch {
			return "unowned";
		}
		if (current.unconfirmedCaptureToken !== completion.captureToken) return "unowned";
		// A recovered message's capture settles through its resend receipt,
		// which the receipt-acknowledging coordinator clears.
		if (messageAdmissionCapture(completion, current)) return "delivered";
		try {
			this.repository().write(terminal, mergeCompletion(current, completion));
		} catch {
			return "unowned";
		}
		return "delivered";
	}

	/** The handoff ledger, when its store can be reached. Acquiring it can
	 * initialize the mutation runtime and open its database, and an unavailable
	 * mutation store must not take draft access down with it: a failure reads as
	 * no ledger - an unavailable graph - so ordinary draft access continues. */
	private availableHandoffLedger(): DraftHandoffLedger | undefined {
		try {
			return this.handoffLedger?.();
		} catch {
			return undefined;
		}
	}

	private tryClean(ledger: DraftHandoffLedger, edgeId: string): void {
		try {
			ledger.cleanEdge(edgeId);
		} catch {
			// A pending dependent or a failing store keeps the tombstone; the next
			// retry cleans it.
		}
	}

	private rememberRetired(edge: DraftHandoffEdge, document: DraftDocument): void {
		this.retired.set(edge.edgeId, { hubId: edge.from.hubId, document });
	}

	/** True when a handoff already retired this endpoint's document and the
	 * identity has composed nothing since: its durable record is empty, so a
	 * new edge would move an empty snapshot. Fresh work reopens the identity -
	 * the current document is what a new edge must carry. */
	private sourceRetiredIdle(from: DraftDestination): boolean {
		const targetKey = JSON.stringify([from.hubId, from.sessionRef]);
		let retired = false;
		for (const entry of this.retired.values()) if (entry.document.targetKey === targetKey) retired = true;
		if (!retired) return false;
		try {
			return !isOccupied(this.repository().read(from));
		} catch {
			// An unreadable source has nothing a new edge could carry.
			return true;
		}
	}

	refsWithDrafts(hubId: string): Set<string> {
		return this.repository().refsWithDrafts(hubId);
	}

	removeHub(hubId: string) {
		try {
			this.repository().removeHub(hubId);
		} finally {
			for (const [edgeId, entry] of this.retired)
				if (entry.hubId === hubId) {
					entry.document.forget();
					this.retired.delete(edgeId);
				}
			for (const document of this.hubs.get(hubId)?.values() ?? []) document.forget();
			this.hubs.delete(hubId);
			// The hub's ledger work can never progress - its drafts are gone -
			// so its edges and their deliveries leave the graph. Obligations
			// survive: their dispositions can still be owed to identities in
			// other hubs.
			try {
				this.availableHandoffLedger()?.removeHub?.(hubId);
			} catch {
				// A ledger that cannot clean up keeps inert rows; the inert
				// document below still blocks every write into the hub.
			}
			if (!this.removedHubs.has(hubId)) {
				const inert = new DraftDocument(this.repository, { hubId, sessionRef: "" }, this.createImageId);
				inert.forget();
				this.removedHubs.set(hubId, inert);
			}
		}
	}
}

function sameDestination(a: DraftDestination, b: DraftDestination): boolean {
	return a.hubId === b.hubId && a.sessionRef === b.sessionRef;
}

function samePair(edge: DraftHandoffEdge, from: DraftDestination, to: DraftDestination): boolean {
	return sameDestination(edge.from, from) && sameDestination(edge.to, to);
}

/** Everything the record currently references or has deliberately released.
 * A refused hop hands these exclusions to the destination with the rows. */
function releasedSources(record: DraftRecord): string[] {
	return [
		...new Set([
			...(record.releasedRecoveryIds ?? []),
			...(record.composer?.recoveryIds ?? []),
			...(record.unconfirmedComposer?.recoveryIds ?? []),
			...(record.parkedCompositions ?? []).flatMap((parked) => parked.composer?.recoveryIds ?? []),
		]),
	];
}

function deliveriesFor(
	edge: DraftHandoffEdge,
	completed: readonly string[],
	released: readonly string[],
): DraftHandoffDelivery[] {
	return [
		...completed.map((recoveryId) => ({
			chainId: edge.chainId,
			destination: edge.to,
			recoveryId,
			disposition: "completed" as const,
			edgeId: edge.edgeId,
		})),
		...released.map((recoveryId) => ({
			chainId: edge.chainId,
			destination: edge.to,
			recoveryId,
			disposition: "released" as const,
			edgeId: edge.edgeId,
		})),
	];
}

/** A destination that holds any composition, marker, or parked work refuses a
 * handoff, exactly as before. */
function isOccupied(record: DraftRecord): boolean {
	return Boolean(
		record.draft !== "" ||
			(record.images?.length ?? 0) > 0 ||
			(record.unconfirmedImages?.length ?? 0) > 0 ||
			record.unconfirmed !== null ||
			(record.composer?.recoveryIds?.length ?? 0) > 0 ||
			(record.unconfirmedComposer?.recoveryIds?.length ?? 0) > 0 ||
			(record.parkedCompositions?.length ?? 0) > 0 ||
			(record.composer?.skillNames?.length ?? 0) > 0 ||
			(record.unconfirmedComposer?.skillNames?.length ?? 0) > 0 ||
			(record.composer?.commandNames?.length ?? 0) > 0 ||
			(record.unconfirmedComposer?.commandNames?.length ?? 0) > 0 ||
			record.recoveryResendAcks?.length ||
			record.completedCommandRecoveryIds?.length ||
			record.releasedRecoveryIds?.length,
	);
}

/** A record holding nothing but disposition markers: no composition, no
 * capture, no images - only the bookkeeping a refusal's dispositions leave
 * behind. A claimed transfer may land over it, preserving the markers. */
function dispositionOnly(record: DraftRecord): boolean {
	return (
		record.draft === "" &&
		(record.images?.length ?? 0) === 0 &&
		(record.unconfirmedImages?.length ?? 0) === 0 &&
		record.unconfirmed === null &&
		!record.composer &&
		!record.unconfirmedComposer &&
		(record.parkedCompositions?.length ?? 0) === 0
	);
}

/** Stable fingerprint of the composition a handoff transfers, so a retry can
 * tell its own write from work added at the destination. Submission lifecycle
 * fields are deliberately excluded: this edge's own completion changes them. */
function compositionFingerprint(record: DraftRecord): string {
	const canonical = JSON.stringify({
		draft: record.draft,
		images: (record.images ?? []).map((image) => [image.id, image.marker, image.mediaType]),
		composer: record.composer?.recoveryIds ?? [],
		parked: (record.parkedCompositions ?? []).map((parked) => [
			parked.text,
			parked.composer?.recoveryIds ?? [],
			(parked.images ?? []).map((image) => image.id),
		]),
	});
	let hash = 2166136261;
	for (let index = 0; index < canonical.length; index++) {
		hash ^= canonical.charCodeAt(index);
		hash = Math.imul(hash, 16777619);
	}
	return `${(hash >>> 0).toString(16)}-${canonical.length}`;
}

/** Selections the composition fingerprint leaves out. */
function sameSelections(
	source: DraftComposerMetadata | undefined,
	delivered: DraftComposerMetadata | undefined,
): boolean {
	return (
		JSON.stringify(source?.skillNames ?? []) === JSON.stringify(delivered?.skillNames ?? []) &&
		JSON.stringify(source?.commandNames ?? []) === JSON.stringify(delivered?.commandNames ?? []) &&
		JSON.stringify(source?.mentions ?? []) === JSON.stringify(delivered?.mentions ?? [])
	);
}

function capturePresent(record: DraftRecord): boolean {
	return (
		record.unconfirmed !== null ||
		record.unconfirmedCaptureToken !== undefined ||
		(record.unconfirmedImages?.length ?? 0) > 0 ||
		record.unconfirmedComposer !== undefined
	);
}

/** Proof that the destination's durable record accounts for every capture the
 * edge carried. The capture's token is unique, so the destination holding one
 * of this edge's tokens proves the transfer. A capture absent from both sides
 * is proven only when its own obligation durably resolved as completed or
 * not-sent, the only states that consume one; a capture the source still holds
 * is not consumed, so an empty destination proves nothing and the hop composes
 * again. */
function captureProven(
	source: DraftRecord,
	delivered: DraftRecord,
	obligations: readonly DraftSubmissionObligation[],
): boolean {
	if (capturePresent(delivered)) {
		const token = delivered.unconfirmedCaptureToken;
		// The destination holds a capture: proof only when it is one this edge
		// carried — the source's own token, or an obligation of this edge.
		if (token === undefined) return sameCaptureRest(source, delivered);
		return (
			(token === source.unconfirmedCaptureToken && sameCaptureRest(source, delivered)) ||
			obligations.some((row) => row.obligationId === token)
		);
	}
	if (capturePresent(source)) return false;
	// Neither side shows a capture: every capture this edge carried must have
	// been consumed by a durable resolution.
	return obligations.every((row) => clearsCapture(row.state));
}

/** The unconfirmed content of two records, compared without their tokens. */
function sameCaptureRest(source: DraftRecord, delivered: DraftRecord): boolean {
	return (
		source.unconfirmed === delivered.unconfirmed &&
		JSON.stringify(source.unconfirmedImages ?? []) === JSON.stringify(delivered.unconfirmedImages ?? []) &&
		JSON.stringify(source.unconfirmedComposer ?? null) === JSON.stringify(delivered.unconfirmedComposer ?? null)
	);
}
