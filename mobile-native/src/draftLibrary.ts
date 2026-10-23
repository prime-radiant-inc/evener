import { DraftDocument } from "./draftDocument";
import type { DraftDestination, DraftRepository } from "./draftRepository";

export type DraftStorage = Pick<
	DraftRepository,
	"read" | "write" | "remove" | "removeHub" | "imageInputs" | "refsWithDrafts"
>;

/** One document per destination keeps pending work coherent across navigation. */
export class DraftLibrary {
	private hubs = new Map<string, Map<string, DraftDocument>>();
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

	/** Hand a destination's complete draft, including recovery ownership, to a
	 * resumed identity before the new screen can open it. The source is removed
	 * only after the destination write commits. */
	handoff(from: DraftDestination, to: DraftDestination): boolean {
		if (from.hubId === to.hubId && from.sessionRef === to.sessionRef) return true;
		const source = this.open(from);
		const snapshot = source.getSnapshot();
		if (!snapshot.loaded) return false;
		const record = snapshot.record;
		const destination = this.repository().read(to);
		if (
			destination.draft !== "" ||
			(destination.images?.length ?? 0) > 0 ||
			(destination.unconfirmed !== null && destination.unconfirmed !== undefined) ||
			(destination.composer?.recoveryIds?.length ?? 0) > 0
		)
			return false;
		const images = [...(record.images ?? []), ...(record.unconfirmedImages ?? [])];
		let additions;
		try {
			additions = images.map((image) => ({
				...image,
				data: source.imagePreviews([image])[0]?.data ?? "",
			}));
		} catch {
			return false;
		}
		try {
			this.repository().write(to, record, additions);
		} catch {
			return false;
		}
		this.repository().remove(from);
		source.forget();
		return true;
	}

	refsWithDrafts(hubId: string): Set<string> {
		return this.repository().refsWithDrafts(hubId);
	}

	removeHub(hubId: string) {
		try {
			this.repository().removeHub(hubId);
		} finally {
			for (const document of this.hubs.get(hubId)?.values() ?? []) document.forget();
			this.hubs.delete(hubId);
		}
	}
}
