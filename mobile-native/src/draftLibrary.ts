import { DraftDocument } from "./draftDocument";
import type { DraftDestination, DraftRepository } from "./draftRepository";

export type DraftStorage = Pick<
	DraftRepository,
	"read" | "write" | "transfer" | "removeHub" | "imageInputs" | "refsWithDrafts"
>;

/** One document per destination keeps pending work coherent across navigation. */
export class DraftLibrary {
	private hubs = new Map<string, Map<string, DraftDocument>>();
	/** A handoff whose destination write has not committed yet. Retrying after
	 * storage recovers keeps the moved recovery rows and their draft together. */
	private pending = new Map<string, { from: DraftDestination; to: DraftDestination }>();
	constructor(
		private repository: () => DraftStorage,
		private createImageId: () => string = () =>
			`reissue-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`,
	) {}

	open(destination: DraftDestination): DraftDocument {
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
		const snapshot = source.getSnapshot();
		if (!snapshot.loaded) {
			this.deferHandoff(from, to);
			return false;
		}
		const destination = this.open(to);
		const current = destination.getSnapshot();
		const existing = current.record;
		if (
			!current.loaded ||
			current.submitting ||
			current.error !== null ||
			existing.draft !== "" ||
			(existing.images?.length ?? 0) > 0 ||
			(existing.unconfirmedImages?.length ?? 0) > 0 ||
			existing.unconfirmed !== null ||
			(existing.composer?.recoveryIds?.length ?? 0) > 0 ||
			(existing.unconfirmedComposer?.recoveryIds?.length ?? 0) > 0 ||
			(existing.composer?.skillNames?.length ?? 0) > 0 ||
			(existing.unconfirmedComposer?.skillNames?.length ?? 0) > 0 ||
			(existing.composer?.commandNames?.length ?? 0) > 0 ||
			(existing.unconfirmedComposer?.commandNames?.length ?? 0) > 0 ||
			existing.recoveryResendAcks?.length ||
			existing.completedCommandRecoveryIds?.length ||
			existing.releasedRecoveryIds?.length
		) {
			// Existing destination work is a permanent refusal, never a retry.
			this.pending.delete(this.handoffKey(to));
			return false;
		}
		const images = [...(snapshot.record.images ?? []), ...(snapshot.record.unconfirmedImages ?? [])];
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
		this.pending.delete(this.handoffKey(to));
		destination.adopt(snapshot.record);
		if (destination !== source) {
			source.transferSettlementTo(destination);
			const sessions = this.hubs.get(from.hubId);
			if (sessions?.get(from.sessionRef) === source) sessions.delete(from.sessionRef);
		}
		return true;
	}

	/** Retry a handoff whose storage write failed. A destination that gained
	 * work in the meantime owns it, so its pending retry is dropped. */
	retryPendingHandoffs(): void {
		for (const { from, to } of [...this.pending.values()]) this.handoff(from, to);
	}

	hasPendingHandoff(to: DraftDestination): boolean {
		return this.pending.has(this.handoffKey(to));
	}

	private deferHandoff(from: DraftDestination, to: DraftDestination): void {
		this.pending.set(this.handoffKey(to), { from, to });
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
			for (const document of this.hubs.get(hubId)?.values() ?? []) document.forget();
			this.hubs.delete(hubId);
		}
	}
}
