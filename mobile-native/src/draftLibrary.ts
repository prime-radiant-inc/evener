import {
	type CompletionDeliveryOutcome,
	captureInFlight,
	type DraftCompletionDelivery,
	DraftDocument,
	mergeCompletion,
	messageAdmissionCapture,
} from "./draftDocument";
import type {
	DraftHandoffDelivery,
	DraftHandoffEdge,
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
	readHandoffGraph,
	terminalDestination,
} from "./draftHandoffGraph";
import type { DraftComposerMetadata, DraftDestination, DraftRecord, DraftRepository } from "./draftRepository";

export type DraftStorage = Pick<
	DraftRepository,
	"read" | "write" | "transfer" | "removeHub" | "imageInputs" | "refsWithDrafts"
>;

/** One document per destination keeps pending work coherent across navigation.
 * Handoffs themselves live in the durable ledger: this class reads the graph,
 * claims, composes, decides, and cleans, but keeps no pending handoff state of
 * its own. */
export class DraftLibrary {
	private hubs = new Map<string, Map<string, DraftDocument>>();
	/** Documents retired by a handoff, kept per hub so hub removal still
	 * invalidates a source the live document map no longer holds. */
	private retired = new Map<string, Set<DraftDocument>>();
	constructor(
		private repository: () => DraftStorage,
		private createImageId: () => string = () =>
			`reissue-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`,
		/** The mutations database's handoff graph. Without it the library cannot
		 * hand drafts across identities: it never keeps a private pending copy. */
		private handoffLedger?: () => DraftHandoffLedger,
	) {}

	open(destination: DraftDestination): DraftDocument {
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
			);
			sessions.set(destination.sessionRef, document);
		}
		return document;
	}

	/** Hand a destination's complete draft, image bytes and settlement markers
	 * to the identity a resume resolved to, before the replacement screen opens.
	 * An already-open destination adopts the record into its live snapshot, and
	 * an in-flight submission on the retired document settles through it. Only
	 * an empty destination, or one this edge already wrote, may take the handoff.
	 */
	handoff(from: DraftDestination, to: DraftDestination): boolean {
		if (sameDestination(from, to)) return true;
		const ledger = this.handoffLedger?.();
		if (ledger === undefined) return false;
		const pending = this.pendingEdge(ledger, from, to);
		if (pending !== undefined) return this.applyEdge(ledger, pending);
		// A source retired by an earlier hop has already given its composition
		// away; composing it again would resurrect the stale record.
		if (this.retiredSource(from) !== undefined) return false;
		const load = readHandoffGraph(ledger);
		if (load.state === "ready") {
			if (load.graph.edges.some((entry) => samePair(entry, from, to) && entry.state === "settled")) return true;
			if (
				load.graph.edges.some(
					(entry) => samePair(entry, from, to) && (entry.state === "refused" || entry.state === "cleaned"),
				)
			)
				return false;
		}
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
		const ledger = this.handoffLedger?.();
		if (ledger === undefined) return;
		// A retired document can still hold a resolution its ledger write missed.
		for (const documents of this.retired.values()) for (const document of documents) document.retry();
		const load = readHandoffGraph(ledger);
		if (load.state !== "ready") return;
		// A reconciled obligation unblocks the edges waiting on it, so the
		// ordering must read the graph that exists after the reconciliation.
		if (this.reconcileAbandonedObligations(ledger, load.graph)) {
			const fresh = readHandoffGraph(ledger);
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
				const load = readHandoffGraph(ledger);
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

	hasPendingHandoff(to: DraftDestination): boolean {
		const ledger = this.handoffLedger?.();
		if (ledger === undefined) return false;
		const load = readHandoffGraph(ledger);
		if (load.state !== "ready") return false;
		return load.graph.edges.some((edge) => edge.state === "pending" && sameDestination(edge.to, to));
	}

	private pendingEdge(
		ledger: DraftHandoffLedger,
		from: DraftDestination,
		to: DraftDestination,
	): DraftHandoffEdge | undefined {
		const load = readHandoffGraph(ledger);
		if (load.state !== "ready") return undefined;
		return load.graph.edges
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
		const load = readHandoffGraph(ledger);
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
		// source. The retired document still holds the record the edge wrote, so a
		// retry must read it, not a fresh empty document.
		const source = this.retiredSource(edge.from) ?? this.open(edge.from);
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
				if (this.transferProven(ledger, edge, snapshot.record))
					return mayDecide ? this.settle(ledger, edge, destination, current.record, edge.deliveredHash) : true;
			}
			if (compositionFingerprint(snapshot.record) !== edge.deliveredHash)
				return mayDecide ? this.refuse(ledger, edge, source) : false;
			// The intended write never landed (or cannot be proven); fall
			// through and compose again.
		}
		if (isOccupied(current.record)) return mayDecide ? this.refuse(ledger, edge, source) : false;

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
			this.repository().transfer(edge.from, edge.to, snapshot.record, additions);
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
		destination.adopt(snapshot.record);
		if (destination !== source) {
			source.retire();
			this.rememberRetired(edge.from.hubId, source);
			const sessions = this.hubs.get(edge.from.hubId);
			if (sessions?.get(edge.from.sessionRef) === source) sessions.delete(edge.from.sessionRef);
		}
		return mayDecide ? this.settle(ledger, edge, destination, snapshot.record, intended) : true;
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
			graph = ledger.readGraph();
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
		// A completion merges these ids into whichever record it clears, so the
		// source can carry them without the destination ever having received
		// them at delivery time.
		const carried = new Set([
			...(delivered.completedCommandRecoveryIds ?? []),
			...(delivered.releasedRecoveryIds ?? []),
			...(delivered.recoveryResendAcks ?? []),
			...obligations.flatMap((row) => [...row.completedCommandIds, ...row.releasedRecoveryIds]),
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

	/** Write completed and released markers at the identity the retargets moved
	 * the chain's mutation rows on to, when the rows traveled past this edge's
	 * own destination. False when the forwarding write cannot land, so
	 * settlement retries with the dispositions still recorded at the hop. */
	private forwardDispositions(
		ledger: DraftHandoffLedger,
		edge: DraftHandoffEdge,
		completed: readonly string[],
		released: readonly string[],
	): boolean {
		if (!completed.length && !released.length) return true;
		const load = readHandoffGraph(ledger);
		if (load.state !== "ready") return false;
		// The rows start from this edge's destination and follow the chain's
		// composed continuations onward: this edge delivered its dispositions at
		// its destination, so only a continuation that carried the identity
		// further can have carried the rows further too.
		const rowOwner = terminalDestination(load.graph.edges, edge.to, edge.chainId);
		if (sameDestination(rowOwner, edge.to)) return true;
		const document = this.open(rowOwner);
		let current = document.getSnapshot();
		if (!current.loaded) {
			document.retry();
			current = document.getSnapshot();
		}
		// An unloaded or unsaved row owner must not receive markers through an
		// empty snapshot; the edge stays pending and the next retry forwards.
		if (!current.loaded || current.error !== null) return false;
		try {
			if (completed.length && !document.adoptCompletedCommandRecovery(completed)) return false;
			if (released.length && !document.releaseRecoverySources(released)) return false;
		} catch {
			return false;
		}
		return true;
	}

	private cleanFinished(ledger: DraftHandoffLedger, graph: { edges: DraftHandoffEdge[] }): void {
		for (const edge of graph.edges)
			if (edge.state === "settled" || edge.state === "refused") this.tryClean(ledger, edge.edgeId);
	}

	/** Recovery sources of submissions still running on this destination's chain.
	 * The screen must not restore those rows while their obligation is open. */
	openObligationRecoveryIds(destination: DraftDestination): Set<string> {
		const ledger = this.handoffLedger?.();
		if (ledger === undefined) return new Set();
		let graph;
		try {
			graph = ledger.readGraph();
		} catch {
			return new Set();
		}
		const ids = new Set<string>();
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
					return undefined;
				}
			});
			if (!sameDestination(owner, destination)) continue;
			for (const id of obligation.recoveryIds) ids.add(id);
		}
		return ids;
	}

	/** Apply a resolved completion to the record of one destination before it is
	 * opened. Idempotent: the capture token is cleared once. */
	private applyObligationsFor(destination: DraftDestination): void {
		const ledger = this.handoffLedger?.();
		if (ledger === undefined) return;
		let graph;
		try {
			graph = ledger.readGraph();
		} catch {
			return;
		}
		for (const obligation of graph.obligations) {
			if (!clearsCapture(obligation.state)) continue;
			// A transfer can commit while its composed-marker write fails, so the
			// graph can name an origin whose capture durably lives elsewhere.
			const owner = captureTerminal(graph.edges, obligation, (endpoint) => {
				try {
					return this.repository().read(endpoint);
				} catch {
					return undefined;
				}
			});
			if (!sameDestination(owner, destination)) continue;
			this.applyCompletion(obligation, owner);
		}
	}

	/** The recovery pass: every resolved completion still waiting on a capture is
	 * applied to the record that owns it. */
	private applyCompletion(obligation: DraftSubmissionObligation, terminal: DraftDestination): void {
		this.deliverCompletionAt(terminal, {
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
			if (live.error !== null) return open.mergeCompletionThroughLiveState(completion);
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

	private tryClean(ledger: DraftHandoffLedger, edgeId: string): void {
		try {
			ledger.cleanEdge(edgeId);
		} catch {
			// A pending dependent or a failing store keeps the tombstone; the next
			// retry cleans it.
		}
	}

	private rememberRetired(hubId: string, document: DraftDocument): void {
		let documents = this.retired.get(hubId);
		if (!documents) {
			documents = new Set();
			this.retired.set(hubId, documents);
		}
		documents.add(document);
	}

	private retiredSource(destination: DraftDestination): DraftDocument | undefined {
		const targetKey = JSON.stringify([destination.hubId, destination.sessionRef]);
		for (const documents of this.retired.values())
			for (const document of documents) if (document.targetKey === targetKey) return document;
		return undefined;
	}

	refsWithDrafts(hubId: string): Set<string> {
		return this.repository().refsWithDrafts(hubId);
	}

	removeHub(hubId: string) {
		try {
			this.repository().removeHub(hubId);
		} finally {
			for (const document of this.retired.get(hubId) ?? []) document.forget();
			this.retired.delete(hubId);
			for (const document of this.hubs.get(hubId)?.values() ?? []) document.forget();
			this.hubs.delete(hubId);
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
