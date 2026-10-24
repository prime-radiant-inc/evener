import { DraftDocument } from "./draftDocument";
import type { DraftDestination, DraftRepository } from "./draftRepository";

export type DraftStorage = Pick<
	DraftRepository,
	"read" | "write" | "transfer" | "removeHub" | "imageInputs" | "refsWithDrafts"
> &
	Partial<Pick<DraftRepository, "readPendingHandoffs" | "writePendingHandoff" | "removePendingHandoff">>;

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
			// Existing destination work is a permanent refusal, never a retry.
			if (this.releaseRefusedSources(source, destination)) this.clearPending(to);
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
		this.clearPending(to);
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
		for (const { from, to } of [...this.pending.values()]) this.handoff(from, to);
	}

	hasPendingHandoff(to: DraftDestination): boolean {
		this.loadPendingHandoffs();
		return this.pending.has(this.handoffKey(to));
	}

	private deferHandoff(from: DraftDestination, to: DraftDestination): void {
		this.pending.set(this.handoffKey(to), { from, to });
		try {
			this.repository().writePendingHandoff?.(from, to);
		} catch {
			// Memory still holds it for this launch; a restart cannot retry a
			// mapping the storage refused to write.
		}
	}

	private clearPending(to: DraftDestination): void {
		this.pending.delete(this.handoffKey(to));
		try {
			this.repository().removePendingHandoff?.(to);
		} catch {
			// A stale mapping retries on the next launch and refuses again.
		}
	}

	private loadPendingHandoffs(): void {
		if (this.pendingLoaded) return;
		try {
			const storage = this.repository();
			if (storage.readPendingHandoffs === undefined || storage.writePendingHandoff === undefined) {
				this.pendingLoaded = true;
				return;
			}
			for (const { from, to } of storage.readPendingHandoffs()) {
				const key = this.handoffKey(to);
				if (!this.pending.has(key)) this.pending.set(key, { from, to });
			}
			this.pendingLoaded = true;
		} catch {
			// Storage is failing; the next open or retry looks again.
		}
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

	private rememberRetired(hubId: string, document: DraftDocument): void {
		let documents = this.retired.get(hubId);
		if (!documents) {
			documents = new Set();
			this.retired.set(hubId, documents);
		}
		documents.add(document);
	}

	private handoffKey(destination: DraftDestination): string {
		return JSON.stringify([destination.hubId, destination.sessionRef]);
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
