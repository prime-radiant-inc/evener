import { openDatabaseSync } from "expo-sqlite";
import { DraftLibrary } from "./draftLibrary";
import { DraftRepository } from "./draftRepository";

let repository: DraftRepository | undefined;
export function nativeDrafts(): DraftRepository {
	if (!repository) {
		const database = openDatabaseSync("evener-drafts.db");
		database.execSync("PRAGMA journal_mode = WAL");
		repository = new DraftRepository(database);
	}
	return repository;
}

export const drafts = new DraftLibrary(nativeDrafts);
