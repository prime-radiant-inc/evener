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
			"readPendingHandoffs" | "writePendingHandoff" | "removePendingHandoff" | "redirectPendingHandoff"
		>
	>;

/** One document per destination keeps pending work coherent across navigation. */
export class DraftLibrary {
	private hubs = new Map<string, Map<string, DraftDocument>>();
	/** A handoff whose destination write has not committed yet. Retrying after
	 * storage recovers keeps the moved recovery rows and their draft together. */
	private pending = new Map<string, { from: DraftDestination; to: DraftDestination }>();
	/** Pending mappings are durable, so they are read once from the drafts
	 * database on first use. A restart finds the handoff a failed transfer left
	 * behind before the resumed screen restores the source's recovery text. */
	private pendingLoaded = false;
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
		const source = this.open(from);
		// The session identity changed whether or not the composition can move:
		// a mounted screen must follow the resumed ref even while the handoff is
		// deferred or refused, or its sends keep addressing the retired one.
		source.resolveIdentity(to);
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
		// An intermediate identity can resolve again before its predecessor's
		// handoff retries. Keep this edge pending until every predecessor's
		// redirect is durable in both stores, or a restart can replay the old
		// mapping into the retired ref.
		if (!this.redirectPredecessors(from, to)) {
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
		// A handoff can redirect a predecessor while this pass runs, creating a
		// new pending edge. Walk keys until every edge present has had one
		// attempt; a deferred edge stays for the next storage or screen event.
		const attempted = new Set<string>();
		for (;;) {
			const key = [...this.pending.keys()].find((candidate) => !attempted.has(candidate));
			if (key === undefined) return;
			attempted.add(key);
			const edge = this.pending.get(key);
			if (edge) this.handoff(edge.from, edge.to);
		}
	}

	hasPendingHandoff(to: DraftDestination): boolean {
		this.loadPendingHandoffs();
		return this.pendingTo(to);
	}

	private deferHandoff(from: DraftDestination, to: DraftDestination): void {
		this.pending.set(this.handoffKey(from, to), { from, to });
		try {
			this.repository().writePendingHandoff?.(from, to);
		} catch {
			// Memory still holds it for this launch; a restart cannot retry a
			// mapping the storage refused to write.
		}
	}

	private clearPending(from: DraftDestination, to: DraftDestination): void {
		this.pending.delete(this.handoffKey(from, to));
		if (!this.pendingTo(to)) {
			try {
				this.repository().removePendingHandoff?.(to);
			} catch {
				// A stale mapping retries on the next launch and refuses again.
			}
		}
		try {
			this.resumeIdentities?.()?.removeResumeIdentity(from, to);
		} catch {
			// A stale edge retries on the next launch; the transfer is durable.
		}
	}

	private loadPendingHandoffs(): void {
		if (this.pendingLoaded) return;
		let loaded = true;
		const raw: { from: DraftDestination; to: DraftDestination }[] = [];
		try {
			const storage = this.repository();
			if (storage.readPendingHandoffs !== undefined && storage.writePendingHandoff !== undefined) {
				raw.push(...storage.readPendingHandoffs());
			}
		} catch {
			// Storage is failing; the next open or retry looks again.
			loaded = false;
		}
		try {
			raw.push(...(this.resumeIdentities?.()?.readResumeIdentities() ?? []));
		} catch {
			// The mutations database is failing too; a later open looks again.
			loaded = false;
		}
		// A redirect accepted by only one store leaves both the old and new
		// mappings on disk. Canonicalize the loaded graph through any
		// intermediate edges before transfers, so A->B and A->C collapse onto
		// the final C instead of retrying A->B first.
		for (const edge of canonicalHandoffs(raw)) {
			const key = this.handoffKey(edge.from, edge.to);
			if (!this.pending.has(key)) this.pending.set(key, edge);
		}
		for (const edge of raw) {
			const finalTo = followHandoffs(edge.to, raw);
			if (sameDestination(finalTo, edge.to)) continue;
			try {
				const storage = this.repository();
				storage.redirectPendingHandoff?.(edge.from, edge.to, finalTo);
			} catch {
				// The canonical in-memory graph still prevents the bad retry.
			}
			try {
				this.resumeIdentities?.()?.redirectResumeIdentity(edge.from, edge.to, finalTo);
			} catch {
				// The canonical in-memory graph still prevents the bad retry.
			}
		}
		if (loaded) this.pendingLoaded = true;
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
			this.pending.set(this.handoffKey(edge.from, settlingTo), { from: edge.from, to: settlingTo });
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

/** Collapse the loaded mappings onto their final destinations. A redirect one
 * store rejected can leave A->B beside A->C; a retained B->C edge makes both
 * resolve to A->C, so no retry can replay the retired intermediate. */
function canonicalHandoffs(
	edges: readonly { from: DraftDestination; to: DraftDestination }[],
): { from: DraftDestination; to: DraftDestination }[] {
	const byDestination = new Map<string, { from: DraftDestination; to: DraftDestination }>();
	for (const edge of edges) {
		const to = followHandoffs(edge.to, edges);
		if (sameDestination(edge.from, to)) continue;
		const key = `${destinationKey(edge.from)}->${destinationKey(to)}`;
		if (!byDestination.has(key)) byDestination.set(key, { from: edge.from, to });
	}
	return [...byDestination.values()];
}
