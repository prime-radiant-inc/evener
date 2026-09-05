import { DraftDocument } from "./draftDocument";
import type { DraftDestination, DraftRepository } from "./draftRepository";

export type DraftStorage = Pick<
	DraftRepository,
	"read" | "write" | "removeHub"
>;

/** One document per destination keeps pending work coherent across navigation. */
export class DraftLibrary {
	private hubs = new Map<string, Map<string, DraftDocument>>();
	constructor(private repository: () => DraftStorage) {}

	open(destination: DraftDestination): DraftDocument {
		let sessions = this.hubs.get(destination.hubId);
		if (!sessions) {
			sessions = new Map();
			this.hubs.set(destination.hubId, sessions);
		}
		let document = sessions.get(destination.sessionRef);
		if (!document) {
			document = new DraftDocument(this.repository, destination);
			sessions.set(destination.sessionRef, document);
		}
		return document;
	}

	removeHub(hubId: string) {
		try {
			this.repository().removeHub(hubId);
		} finally {
			for (const document of this.hubs.get(hubId)?.values() ?? [])
				document.forget();
			this.hubs.delete(hubId);
		}
	}
}
