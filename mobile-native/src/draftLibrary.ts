import { DraftDocument } from "./draftDocument";
import type { DraftDestination, DraftRepository } from "./draftRepository";
import type { ResumeIdentityLedger } from "./resumeIdentity";

export type DraftStorage = Pick<
	DraftRepository,
	"read" | "write" | "transfer" | "removeHub" | "imageInputs" | "refsWithDrafts"
> &
	Partial<
		Pick<
			DraftRepository,
			| "readPendingHandoffs"
			| "writePendingHandoff"
			| "removePendingHandoff"
			| "redirectPendingHandoff"
			| "readSettledHandoffs"
			| "markPendingHandoffSettled"
			| "removeSettledHandoff"
		>
	>;

interface PendingHandoff {
	from: DraftDestination;
	to: DraftDestination;
	/** Loaded edges between the source and the destination. A predecessor has
	 * a higher rank and settles after its downstream edge. */
	rank: number;
	/** Intermediate destinations whose handoffs must settle first. */
	dependsOn?: DraftDestination[];
}

/** One document per destination keeps pending work coherent across navigation. */
export class DraftLibrary {
	private hubs = new Map<string, Map<string, DraftDocument>>();
	/** A handoff whose destination write has not committed yet. Retrying after
	 * storage recovers keeps the moved recovery rows and their draft together. */
	private pending = new Map<string, PendingHandoff>();
	/** Pending mappings are durable, so they are read once from the drafts
	 * database on first use. A restart finds the handoff a failed transfer left
	 * behind before the resumed screen restores the source's recovery text. */
	private pendingLoaded = false;
	/** A load could not read every store. Settling on the partial graph could
	 * put a predecessor ahead of a downstream node the missing edges order. */
	private graphIncomplete = false;
	/** Handoffs whose transfer settled but whose durable cleanup failed. They
	 * are not retried as transfers; a later resume still redirects their stale
	 * mapping away from a retired ref. */
	private stale = new Map<string, { from: DraftDestination; to: DraftDestination }>();
	/** Documents retired by a handoff, kept per hub so hub removal still
	 * invalidates a source the live document map no longer holds. */
	private retired = new Map<string, Set<DraftDocument>>();
	constructor(
		private repository: () => DraftStorage,
		private createImageId: () => string = () =>
			`reissue-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`,
		/** The mutations database's record of identity-changing resumes. It is
		 * written in the retarget savepoint, so it can reconstruct a handoff
		 * the drafts database refused to record. */
		private resumeIdentities?: () => ResumeIdentityLedger,
	) {}

	open(destination: DraftDestination): DraftDocument {
		this.loadPendingHandoffs();
		let sessions = this.hubs.get(destination.hubId);
		if (!sessions) {
			sessions = new Map();
			this.hubs.set(destination.hubId, sessions);
		}
		let document = sessions.get(destination.sessionRef);
		if (!document) {
			document = new DraftDocument(this.repository, destination, this.createImageId);
			sessions.set(destination.sessionRef, document);
		}
		return document;
	}

	/** Hand a destination's complete draft, image bytes and settlement markers
	 * to the identity a resume resolved to, before the replacement screen opens.
	 * An already-open destination adopts the record into its live snapshot, and
	 * an in-flight submission on the retired document settles through it. Only
	 * an empty destination may take the handoff; existing work is left alone. */
	handoff(from: DraftDestination, to: DraftDestination): boolean {
		if (from.hubId === to.hubId && from.sessionRef === to.sessionRef) return true;
		this.loadPendingHandoffs();
		const source = this.open(from);
		// The session identity changed whether or not the composition can move:
		// a mounted screen must follow the resumed ref even while the graph is
		// incomplete and the transfer stays deferred.
		source.resolveIdentity(to);
		if (this.graphIncomplete) {
			this.deferHandoff(from, to);
			return false;
		}
		// Establish the chain dependency before any early return. A predecessor
		// that is not redirected here retries first while this edge is deferred,
		// moving its draft into the retired intermediate identity.
		if (!this.redirectPredecessors(from, to)) {
			this.deferHandoff(from, to);
			return false;
		}
		let snapshot = source.getSnapshot();
		if (!snapshot.loaded) {
			source.retry();
			snapshot = source.getSnapshot();
		}
		if (!snapshot.loaded) {
			this.deferHandoff(from, to);
			return false;
		}
		const destination = this.open(to);
		let current = destination.getSnapshot();
		if (!current.loaded || current.error !== null) {
			// A failed load is transient: retry it before deciding, and keep the
			// pending handoff so a later retry can still transfer the composition.
			destination.retry();
			current = destination.getSnapshot();
		}
		if (!current.loaded || current.error !== null || current.submitting) {
			this.deferHandoff(from, to);
			return false;
		}
		const existing = current.record;
		if (
			existing.draft !== "" ||
			(existing.images?.length ?? 0) > 0 ||
			(existing.unconfirmedImages?.length ?? 0) > 0 ||
			existing.unconfirmed !== null ||
			(existing.composer?.recoveryIds?.length ?? 0) > 0 ||
			(existing.unconfirmedComposer?.recoveryIds?.length ?? 0) > 0 ||
			(existing.parkedCompositions?.length ?? 0) > 0 ||
			(existing.composer?.skillNames?.length ?? 0) > 0 ||
			(existing.unconfirmedComposer?.skillNames?.length ?? 0) > 0 ||
			(existing.composer?.commandNames?.length ?? 0) > 0 ||
			(existing.unconfirmedComposer?.commandNames?.length ?? 0) > 0 ||
			existing.recoveryResendAcks?.length ||
			existing.completedCommandRecoveryIds?.length ||
			existing.releasedRecoveryIds?.length
		) {
			// A built-in command still submitting writes its completion marker
			// when it finishes. Finalizing the refusal now would copy only the
			// markers already on the source and leave the late one stranded for
			// the destination to restore and run again.
			if (source.getSnapshot().submitting) {
				this.deferHandoff(from, to);
				return false;
			}
			// Existing destination work is a permanent refusal, never a retry.
			if (this.releaseRefusedSources(source, destination)) this.clearPending(from, to);
			else this.deferHandoff(from, to);
			return false;
		}
		const images = [
			...(snapshot.record.images ?? []),
			...(snapshot.record.unconfirmedImages ?? []),
			...(snapshot.record.parkedCompositions ?? []).flatMap((parked) => parked.images ?? []),
		];
		let additions;
		try {
			additions = images.map((image) => {
				const data = source.imagePreviews([image])[0]?.data;
				if (!data) throw new Error("Saved image is unavailable.");
				return { ...image, data };
			});
			this.repository().transfer(from, to, snapshot.record, additions);
		} catch {
			this.deferHandoff(from, to);
			return false;
		}
		this.clearPending(from, to);
		destination.adopt(snapshot.record);
		if (destination !== source) {
			source.retireInto(destination);
			this.rememberRetired(from.hubId, source);
			const sessions = this.hubs.get(from.hubId);
			if (sessions?.get(from.sessionRef) === source) sessions.delete(from.sessionRef);
		}
		return true;
	}

	/** Retry a handoff whose storage write failed. A destination that gained
	 * work in the meantime owns it, so its pending retry is dropped. */
	retryPendingHandoffs(): void {
		this.loadPendingHandoffs();
		if (this.graphIncomplete) return;
		this.retryStaleCleanup();
		// Downstream first: a chain's intermediate must settle before its
		// predecessor, or the predecessor's older draft occupies the destination
		// and the intermediate's composition is refused. A deferred downstream
		// edge also blocks its predecessors, including when a transfer fails
		// transiently and the intermediate remains pending.
		const attempted = new Set<string>();
		const blocked = new Set<string>();
		for (;;) {
			// A predecessor of a blocked node is blocked too, so a deferred B->C
			// holds O->C behind A->C.
			let changed = true;
			while (changed) {
				changed = false;
				for (const candidate of this.pending.values()) {
					const fromKey = destinationKey(candidate.from);
					if (blocked.has(fromKey)) continue;
					const blockedDependency =
						candidate.dependsOn?.some((dependsOn) => blocked.has(destinationKey(dependsOn))) ?? false;
					if (!blockedDependency && !blocked.has(destinationKey(candidate.to))) continue;
					blocked.add(fromKey);
					changed = true;
				}
			}
			const next = [...this.pending.entries()]
				.filter(([candidate]) => !attempted.has(candidate))
				.filter(([, edge]) => !blocked.has(destinationKey(edge.to)))
				.filter(
					([, edge]) =>
						edge.dependsOn === undefined || !edge.dependsOn.some((dependsOn) => blocked.has(destinationKey(dependsOn))),
				)
				.sort((a, b) => a[1].rank - b[1].rank)[0];
			if (next === undefined) return;
			const [key, edge] = next;
			attempted.add(key);
			const settled = this.handoff(edge.from, edge.to);
			if (!settled && this.pending.has(key)) blocked.add(destinationKey(edge.from));
		}
	}

	hasPendingHandoff(to: DraftDestination): boolean {
		this.loadPendingHandoffs();
		return this.pendingTo(to);
	}

	private deferHandoff(from: DraftDestination, to: DraftDestination): void {
		const key = this.handoffKey(from, to);
		const existing = this.pending.get(key);
		let rank = existing?.rank;
		if (rank === undefined) {
			// Order a new edge against the edges already loaded. An edge whose
			// source is this destination is downstream and settles first; an edge
			// pointing at this source must settle after it.
			let downstream = Number.POSITIVE_INFINITY;
			let predecessor = Number.NEGATIVE_INFINITY;
			for (const edge of this.pending.values()) {
				if (sameDestination(edge.from, to)) downstream = Math.min(downstream, edge.rank);
				if (sameDestination(edge.to, from)) predecessor = Math.max(predecessor, edge.rank);
			}
			if (downstream !== Number.POSITIVE_INFINITY) rank = downstream + 1;
			else if (predecessor !== Number.NEGATIVE_INFINITY) rank = predecessor - 1;
			else rank = 0;
		}
		this.pending.set(key, {
			from,
			to,
			rank,
			...(existing?.dependsOn === undefined ? {} : { dependsOn: [...existing.dependsOn] }),
		});
		try {
			this.repository().writePendingHandoff?.(from, to);
		} catch {
			// Memory still holds it for this launch; a restart cannot retry a
			// mapping the storage refused to write.
		}
	}

	private clearPending(from: DraftDestination, to: DraftDestination): void {
		const key = this.handoffKey(from, to);
		this.pending.delete(key);
		// The mutations database is a second durable settlement store. It alone
		// must keep a restart from retrying the transfer when the drafts database
		// rejects every write.
		let ledgerSettled = false;
		try {
			this.resumeIdentities?.()?.settleResumeIdentity(from, to);
			ledgerSettled = true;
		} catch {
			// The drafts database can still record the settlement below.
		}
		let draftsSettled = false;
		try {
			this.repository().markPendingHandoffSettled?.(from, to);
			draftsSettled = true;
		} catch {
			// Retried by retryStaleCleanup while the mutations marker stays put.
		}
		// The drafts settlement is the only marker left when the ledger still
		// has to be cleaned; keep it until the ledger side committed.
		let draftsCleared = false;
		if (ledgerSettled && draftsSettled) {
			try {
				this.repository().removeSettledHandoff?.(from, to);
				draftsCleared = true;
			} catch {
				// retryStaleCleanup clears both markers.
			}
		}
		let ledgerCleared = false;
		if (draftsCleared) {
			try {
				this.resumeIdentities?.()?.removeResumeIdentitySettlement?.(from, to);
				ledgerCleared = true;
			} catch {
				// The marker is harmless; the next load retries the cleanup.
			}
		}
		if (ledgerSettled && draftsCleared && ledgerCleared) this.stale.delete(key);
		else this.stale.set(key, { from, to });
	}

	private retryStaleCleanup(): void {
		for (const [key, edge] of [...this.stale]) {
			let cleaned = true;
			try {
				this.resumeIdentities?.()?.settleResumeIdentity(edge.from, edge.to);
			} catch {
				cleaned = false;
			}
			if (!cleaned) continue;
			try {
				const storage = this.repository();
				storage.markPendingHandoffSettled?.(edge.from, edge.to);
				if (storage.removeSettledHandoff) storage.removeSettledHandoff(edge.from, edge.to);
				else if (!this.pendingTo(edge.to)) storage.removePendingHandoff?.(edge.to);
			} catch {
				cleaned = false;
			}
			// Drop the mutations marker only after the drafts mapping is gone, or
			// a restart would reconstruct the transfer from the leftover row.
			try {
				this.resumeIdentities?.()?.removeResumeIdentitySettlement?.(edge.from, edge.to);
			} catch {
				cleaned = false;
			}
			if (cleaned) this.stale.delete(key);
		}
	}

	private loadPendingHandoffs(): void {
		if (this.pendingLoaded) return;
		let loaded = true;
		const raw: { from: DraftDestination; to: DraftDestination }[] = [];
		const settledKeys = new Set(this.stale.keys());
		const settledSources = new Set<string>();
		for (const edge of this.stale.values()) settledSources.add(destinationKey(edge.from));
		try {
			const storage = this.repository();
			if (storage.readPendingHandoffs !== undefined && storage.writePendingHandoff !== undefined) {
				const pendingRows = storage.readPendingHandoffs();
				for (const edge of storage.readSettledHandoffs?.() ?? []) {
					const key = this.handoffKey(edge.from, edge.to);
					settledKeys.add(key);
					settledSources.add(destinationKey(edge.from));
					if (!this.stale.has(key)) this.stale.set(key, edge);
				}
				for (const edge of pendingRows)
					if (!settledKeys.has(this.handoffKey(edge.from, edge.to)) && !settledSources.has(destinationKey(edge.to)))
						raw.push(edge);
			}
		} catch {
			// Storage is failing; the next open or retry looks again.
			loaded = false;
		}
		try {
			const ledger = this.resumeIdentities?.();
			for (const edge of ledger?.readSettledResumeIdentities?.() ?? []) {
				const key = this.handoffKey(edge.from, edge.to);
				settledKeys.add(key);
				settledSources.add(destinationKey(edge.from));
				if (!this.stale.has(key)) this.stale.set(key, edge);
			}
			for (const edge of ledger?.readResumeIdentities() ?? []) {
				if (settledKeys.has(this.handoffKey(edge.from, edge.to))) continue;
				// A settled edge retires its source: edges pointing at that source
				// are provenance and must not become standalone transfers.
				if (settledSources.has(destinationKey(edge.to))) continue;
				raw.push(edge);
			}
		} catch {
			// The mutations database is failing too; a later open looks again.
			loaded = false;
		}
		// A settlement the drafts store did not know about still retires its
		// edge and every provenance edge pointing at its source.
		for (let index = raw.length - 1; index >= 0; index--) {
			const edge = raw[index];
			if (settledKeys.has(this.handoffKey(edge.from, edge.to)) || settledSources.has(destinationKey(edge.to)))
				raw.splice(index, 1);
		}
		// A partial load may have added a ledger edge before the drafts store
		// revealed its settled marker. Settlement wins: never keep a transfer.
		for (const [key, edge] of [...this.pending])
			if (settledKeys.has(key) || settledSources.has(destinationKey(edge.to))) this.pending.delete(key);
		// A redirect accepted by only one store leaves both the old and new
		// mappings on disk. Attempt the redirects first: a failure keeps the raw
		// intermediate edge and suppresses the redirected mapping from that
		// source, so the intermediate cannot settle until both stores accept it.
		const adjacency = handoffAdjacency(raw);
		const failedSources = new Set<string>();
		for (const edge of raw) {
			const followed = followHandoffs(edge.to, raw);
			if (sameDestination(followed, edge.to)) continue;
			let draftsDurable = true;
			try {
				const storage = this.repository();
				if (storage.redirectPendingHandoff) storage.redirectPendingHandoff(edge.from, edge.to, followed);
				else if (storage.writePendingHandoff) storage.writePendingHandoff(edge.from, followed);
			} catch {
				draftsDurable = false;
			}
			let identitiesDurable = true;
			try {
				this.resumeIdentities?.()?.redirectResumeIdentity(edge.from, edge.to, followed);
			} catch {
				identitiesDurable = false;
			}
			if (!draftsDurable || !identitiesDurable) failedSources.add(destinationKey(edge.from));
		}
		const reconciled = new Map<string, PendingHandoff>();
		for (const edge of raw) {
			const followed = followHandoffs(edge.to, raw);
			if (failedSources.has(destinationKey(edge.from))) {
				// Keep only the predecessor that still needs its redirect; an
				// already-redirected mapping from the same source must not settle
				// ahead of the intermediate.
				if (sameDestination(followed, edge.to)) continue;
				const key = this.handoffKey(edge.from, edge.to);
				const rank = handoffRank(edge.from, followed, adjacency);
				const existing = reconciled.get(key);
				if (!existing || existing.rank < rank) reconciled.set(key, { from: edge.from, to: edge.to, rank });
				continue;
			}
			if (sameDestination(edge.from, followed)) continue;
			const key = this.handoffKey(edge.from, followed);
			const rank = handoffRank(edge.from, followed, adjacency);
			const dependsOn = immediateDependencies(edge.from, followed, adjacency);
			const existing = reconciled.get(key);
			if (!existing || existing.rank < rank)
				reconciled.set(key, {
					from: edge.from,
					to: followed,
					rank,
					...(dependsOn.length ? { dependsOn } : {}),
				});
			else if (existing.rank === rank && dependsOn.length)
				existing.dependsOn = uniqueDestinations([...(existing.dependsOn ?? []), ...dependsOn]);
		}
		// A recovered graph can reveal that an entry loaded earlier now belongs
		// to a longer chain. Move the obsolete key onto its canonical terminal
		// rather than leaving both the old and new transfer pending.
		for (const [key, edge] of [...this.pending]) {
			const terminal = followHandoffs(edge.to, raw);
			if (sameDestination(terminal, edge.to)) continue;
			const canonicalKey = this.handoffKey(edge.from, terminal);
			const canonical = reconciled.get(canonicalKey);
			const dependsOn = uniqueDestinations([
				...(canonical?.dependsOn ?? []),
				...(edge.dependsOn ?? []),
				...immediateDependencies(edge.from, terminal, adjacency),
			]);
			reconciled.set(canonicalKey, {
				from: edge.from,
				to: terminal,
				rank: Math.max(canonical?.rank ?? 0, edge.rank, handoffRank(edge.from, terminal, adjacency)),
				...(dependsOn.length ? { dependsOn } : {}),
			});
			this.pending.delete(key);
		}
		// Downstream first, so an intermediate's composition wins the shared
		// destination and the predecessor falls under the occupied-destination
		// rule instead of the other way round.
		for (const edge of [...reconciled.values()].sort((a, b) => a.rank - b.rank)) {
			const key = this.handoffKey(edge.from, edge.to);
			const existing = this.pending.get(key);
			if (!existing) {
				this.pending.set(key, edge);
				continue;
			}
			// A load with a temporarily unreadable ledger can have added this
			// entry without its chain. A later load must upgrade its rank and
			// dependencies, not leave the stale order in place.
			const dependsOn = uniqueDestinations([...(existing.dependsOn ?? []), ...(edge.dependsOn ?? [])]);
			this.pending.set(key, {
				from: existing.from,
				to: existing.to,
				rank: Math.max(existing.rank, edge.rank),
				...(dependsOn.length ? { dependsOn } : {}),
			});
		}
		if (loaded) {
			this.pendingLoaded = true;
			this.graphIncomplete = false;
		} else this.graphIncomplete = true;
	}

	private releaseRefusedSources(source: DraftDocument, destination: DraftDocument): boolean {
		const record = source.getSnapshot().record;
		// The rows moved even though the composition did not. A completed
		// built-in command's marker must move too, or the occupied destination
		// re-appends and re-runs the command on a later Send.
		const completed = record.completedCommandRecoveryIds ?? [];
		const markersMoved = !completed.length || destination.adoptCompletedCommandRecovery(completed);
		const ids = [
			...(record.composer?.recoveryIds ?? []),
			...(record.unconfirmedComposer?.recoveryIds ?? []),
			...(record.parkedCompositions ?? []).flatMap((parked) => parked.composer?.recoveryIds ?? []),
		];
		const sourcesReleased = !ids.length || destination.releaseRecoverySources(ids);
		return markersMoved && sourcesReleased;
	}

	/** An intermediate identity can resolve again before its predecessor's
	 * handoff retries. Point every pending predecessor at the newest ref before
	 * the intermediate edge is cleared, so the composition and its completion
	 * markers continue toward the ref the rows actually moved to. */
	private redirectPredecessors(settlingFrom: DraftDestination, settlingTo: DraftDestination): boolean {
		let durable = true;
		for (const [key, edge] of [...this.pending]) {
			if (!sameDestination(edge.to, settlingFrom)) continue;
			let draftsDurable = true;
			try {
				const storage = this.repository();
				if (storage.redirectPendingHandoff) storage.redirectPendingHandoff(edge.from, settlingFrom, settlingTo);
				else storage.writePendingHandoff?.(edge.from, settlingTo);
			} catch {
				draftsDurable = false;
			}
			let identitiesDurable = true;
			try {
				this.resumeIdentities?.()?.redirectResumeIdentity(edge.from, settlingFrom, settlingTo);
			} catch {
				identitiesDurable = false;
			}
			if (!draftsDurable || !identitiesDurable) {
				durable = false;
				continue;
			}
			this.pending.delete(key);
			const redirectedKey = this.handoffKey(edge.from, settlingTo);
			const existing = this.pending.get(redirectedKey);
			const redirected: PendingHandoff = {
				from: edge.from,
				to: settlingTo,
				rank: Math.max(edge.rank + 1, existing?.rank ?? 0),
				dependsOn: uniqueDestinations([...(edge.dependsOn ?? []), ...(existing?.dependsOn ?? []), settlingFrom]),
			};
			this.pending.set(redirectedKey, redirected);
		}
		for (const [key, edge] of [...this.stale]) {
			if (!sameDestination(edge.to, settlingFrom)) continue;
			let cleaned = true;
			try {
				this.resumeIdentities?.()?.settleResumeIdentity(edge.from, edge.to);
			} catch {
				cleaned = false;
			}
			if (!cleaned) {
				durable = false;
				continue;
			}
			try {
				const storage = this.repository();
				storage.markPendingHandoffSettled?.(edge.from, edge.to);
				if (storage.removeSettledHandoff) storage.removeSettledHandoff(edge.from, edge.to);
				else if (!this.pendingTo(edge.to)) storage.removePendingHandoff?.(edge.to);
			} catch {
				cleaned = false;
			}
			// Keep the mutations settlement marker until the drafts mapping is
			// removed, so a restart cannot replay the settled edge.
			try {
				this.resumeIdentities?.()?.removeResumeIdentitySettlement?.(edge.from, edge.to);
			} catch {
				cleaned = false;
			}
			if (!cleaned) {
				durable = false;
				continue;
			}
			this.stale.delete(key);
		}
		return durable;
	}

	private pendingTo(to: DraftDestination): boolean {
		for (const edge of this.pending.values()) if (sameDestination(edge.to, to)) return true;
		return false;
	}

	private rememberRetired(hubId: string, document: DraftDocument): void {
		let documents = this.retired.get(hubId);
		if (!documents) {
			documents = new Set();
			this.retired.set(hubId, documents);
		}
		documents.add(document);
	}

	private handoffKey(from: DraftDestination, to: DraftDestination): string {
		return JSON.stringify([from.hubId, from.sessionRef, to.hubId, to.sessionRef]);
	}

	refsWithDrafts(hubId: string): Set<string> {
		return this.repository().refsWithDrafts(hubId);
	}

	removeHub(hubId: string) {
		try {
			this.repository().removeHub(hubId);
		} finally {
			for (const [key, { from, to }] of this.pending)
				if (from.hubId === hubId || to.hubId === hubId) this.pending.delete(key);
			for (const [key, { from, to }] of this.stale)
				if (from.hubId === hubId || to.hubId === hubId) this.stale.delete(key);
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

function destinationKey(destination: DraftDestination): string {
	return JSON.stringify([destination.hubId, destination.sessionRef]);
}

/** Follow the loaded handoff graph from one destination to its terminal ref.
 * A visited set stops a malformed cycle from spinning. */
function followHandoffs(
	destination: DraftDestination,
	edges: readonly { from: DraftDestination; to: DraftDestination }[],
): DraftDestination {
	const seen = new Set<string>();
	let current = destination;
	for (;;) {
		const key = destinationKey(current);
		if (seen.has(key)) return current;
		seen.add(key);
		const next = edges.find((edge) => sameDestination(edge.from, current));
		if (next === undefined) return current;
		current = next.to;
	}
}

function handoffAdjacency(
	edges: readonly { from: DraftDestination; to: DraftDestination }[],
): Map<string, DraftDestination[]> {
	const adjacency = new Map<string, DraftDestination[]>();
	for (const edge of edges) {
		if (sameDestination(edge.from, edge.to)) continue;
		const key = destinationKey(edge.from);
		const next = adjacency.get(key) ?? [];
		if (!next.some((destination) => sameDestination(destination, edge.to))) next.push(edge.to);
		adjacency.set(key, next);
	}
	return adjacency;
}

/** Longest provenance path from a source to its terminal. A shortcut must not
 * hide a longer chain that has to settle first. */
function handoffRank(from: DraftDestination, to: DraftDestination, adjacency: Map<string, DraftDestination[]>): number {
	const length = longestHandoffPath(from, to, adjacency, new Set<string>());
	return Number.isFinite(length) ? length + 1 : 1;
}

function longestHandoffPath(
	from: DraftDestination,
	to: DraftDestination,
	adjacency: Map<string, DraftDestination[]>,
	visiting: Set<string>,
): number {
	if (sameDestination(from, to)) return 0;
	const key = destinationKey(from);
	if (visiting.has(key)) return Number.NEGATIVE_INFINITY;
	visiting.add(key);
	let best = Number.NEGATIVE_INFINITY;
	for (const next of adjacency.get(key) ?? []) {
		const length = 1 + longestHandoffPath(next, to, adjacency, visiting);
		if (length > best) best = length;
	}
	visiting.delete(key);
	return best;
}

/** The nodes a source must wait for directly: every immediate next node except
 * the terminal itself. Their own pending edges carry the rest of the chain. */
function immediateDependencies(
	from: DraftDestination,
	to: DraftDestination,
	adjacency: Map<string, DraftDestination[]>,
): DraftDestination[] {
	return uniqueDestinations(
		(adjacency.get(destinationKey(from)) ?? []).filter(
			(next) => !sameDestination(next, to) && !sameDestination(next, from),
		),
	);
}

function uniqueDestinations(destinations: DraftDestination[]): DraftDestination[] {
	const byKey = new Map<string, DraftDestination>();
	for (const destination of destinations) {
		const key = destinationKey(destination);
		if (!byKey.has(key)) byKey.set(key, destination);
	}
	return [...byKey.values()];
}
