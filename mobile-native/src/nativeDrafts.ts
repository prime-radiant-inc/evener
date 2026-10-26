import { openDatabaseSync } from "expo-sqlite";
import { randomUUID } from "expo-crypto";
import { DraftLibrary } from "./draftLibrary";
import { DraftRepository } from "./draftRepository";
import { getNativeMutationRuntime } from "./nativeMutationRuntime";
import { handoffResumeDraft } from "./resumeDraftHandoff";

let repository: DraftRepository | undefined;
export function nativeDrafts(): DraftRepository {
	if (!repository) {
		const database = openDatabaseSync("evener-drafts.db");
		database.execSync("PRAGMA journal_mode = WAL");
		repository = new DraftRepository(database);
	}
	return repository;
}

export const drafts = new DraftLibrary(nativeDrafts, randomUUID, () => getNativeMutationRuntime().storage);

/** Carry the app's edited draft to the identity a resume resolved to, for the
 * background flush and screens that submit above the session. */
export function handoffNativeResumeDraft(hubId: string, fromRef: string, toRef: string): void {
	handoffResumeDraft(drafts, hubId, fromRef, toRef);
}
