import { DraftDocument } from "./draftDocument";
import type {
	DraftHandoffDelivery,
	DraftHandoffEdge,
	DraftHandoffLedger,
	DraftSubmissionObligation,
} from "./draftHandoffLedger";
import { orderHandoffGraph, readHandoffGraph, terminalDestination } from "./draftHandoffGraph";
import type { DraftDestination, DraftRecord, DraftRepository } from "./draftRepository";

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
				(terminal, record) => {
					this.hubs.get(terminal.hubId)?.get(terminal.sessionRef)?.adopt(record);
				},
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
		this.cleanFinished(ledger, load.graph);
		const ordering = orderHandoffGraph(load);
		if (ordering.state !== "ready") return;
		const blocked = new Set<string>();
		for (const edge of ordering.order) {
			if (edge.dependencies.some((dependency) => blocked.has(dependency))) {
				blocked.add(edge.edgeId);
				continue;
			}
			let accepted: boolean;
			try {
				accepted = this.applyEdge(ledger, edge);
			} catch {
				blocked.add(edge.edgeId);
				continue;
			}
			if (accepted) continue;
			const fresh = readHandoffGraph(ledger);
			if (fresh.state !== "ready") return;
			if (fresh.graph.edges.find((entry) => entry.edgeId === edge.edgeId)?.state === "pending")
				blocked.add(edge.edgeId);
		}
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
		const ordering = orderHandoffGraph(load);
		if (ordering.state !== "ready") return false;
		const blocked = ordering.blocked.find((entry) => entry.edgeId === edge.edgeId);
		// A dependency that is not settled stops the hop entirely. An open
		// obligation only defers the settle/refuse decision: the composition may
		// still move so an in-flight submission completes at the destination.
		if (blocked?.reason === "dependency") return false;
		const planned = ordering.order.some((entry) => entry.edgeId === edge.edgeId) || blocked?.reason === "obligation";
		if (!planned) return false;
		return this.deliver(ledger, edge, blocked === undefined);
	}

	private deliver(ledger: DraftHandoffLedger, edge: DraftHandoffEdge, mayDecide: boolean): boolean {
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
				return mayDecide ? this.refuse(ledger, edge, source) : false;
			}
			if (destinationNow === edge.deliveredHash)
				return mayDecide ? this.settle(ledger, edge, destination, current.record, edge.deliveredHash) : true;
			if (compositionFingerprint(snapshot.record) !== edge.deliveredHash)
				return mayDecide ? this.refuse(ledger, edge, source) : false;
			// The intended write never landed; fall through and compose again.
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

	private refuse(ledger: DraftHandoffLedger, edge: DraftHandoffEdge, source: DraftDocument): boolean {
		const record = source.getSnapshot().record;
		const completed = [...(record.completedCommandRecoveryIds ?? [])];
		const released = releasedSources(record);
		const destination = this.open(edge.to);
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
		try {
			ledger.settleEdge(edge.edgeId, deliveriesFor(edge, completed, released), deliveredHash);
		} catch {
			return false;
		}
		destination.adopt(record);
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
			if (
				!sameDestination(
					terminalDestination(graph.edges, obligation.origin, obligation.chainId ?? undefined),
					destination,
				)
			)
				continue;
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
			if (
				!sameDestination(
					terminalDestination(graph.edges, obligation.origin, obligation.chainId ?? undefined),
					destination,
				)
			)
				continue;
			this.applyCompletion(
				obligation,
				terminalDestination(graph.edges, obligation.origin, obligation.chainId ?? undefined),
			);
		}
	}

	/** The recovery pass: every resolved completion still waiting on a capture is
	 * applied to the record that owns it. */
	private applyCompletion(obligation: DraftSubmissionObligation, terminal: DraftDestination): void {
		const open = this.hubs.get(terminal.hubId)?.get(terminal.sessionRef);
		let current: DraftRecord;
		if (open?.getSnapshot().loaded) current = open.getSnapshot().record;
		else {
			try {
				current = this.repository().read(terminal);
			} catch {
				return;
			}
		}
		if (current.unconfirmedCaptureToken !== obligation.obligationId) return;
		const next = mergeCompletion(current, obligation);
		try {
			this.repository().write(terminal, next);
		} catch {
			return;
		}
		open?.adopt(next);
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

/** A completed submission, or a proven local refusal, owns the destination
 * capture. A plain failed operation leaves it recoverable. */
function clearsCapture(state: DraftSubmissionObligation["state"]): boolean {
	return state === "completed" || state === "not-sent";
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

/** Clear a resolved submission's capture and merge the ids it delivered. */
function mergeCompletion(record: DraftRecord, obligation: DraftSubmissionObligation): DraftRecord {
	const next: DraftRecord = {
		...record,
		unconfirmed: null,
		unconfirmedImages: undefined,
		unconfirmedComposer: undefined,
		unconfirmedCaptureToken: undefined,
	};
	if (obligation.completedCommandIds.length)
		next.completedCommandRecoveryIds = [
			...new Set([...(record.completedCommandRecoveryIds ?? []), ...obligation.completedCommandIds]),
		];
	if (obligation.releasedRecoveryIds.length)
		next.releasedRecoveryIds = [...new Set([...(record.releasedRecoveryIds ?? []), ...obligation.releasedRecoveryIds])];
	return next;
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
