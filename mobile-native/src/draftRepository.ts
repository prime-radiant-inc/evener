import {
	decodeQuestionSelections,
	type QuestionSelections,
} from "./questionAnswers";
export interface DraftRecord {
	draft: string;
	unconfirmed: string | null;
}

export interface DraftDestination {
	hubId: string;
	sessionRef: string;
}

export interface DraftDatabase {
	execSync(sql: string): void;
	runSync(sql: string, ...params: (string | null)[]): unknown;
	getFirstSync<T>(sql: string, ...params: string[]): T | null;
}

export class DraftRepository {
	constructor(private readonly db: DraftDatabase) {
		db.execSync(`CREATE TABLE IF NOT EXISTS drafts (
      hub_id TEXT NOT NULL,
      session_ref TEXT NOT NULL,
      draft TEXT NOT NULL,
      unconfirmed TEXT,
      PRIMARY KEY (hub_id, session_ref)
    )`);
		db.execSync(`CREATE TABLE IF NOT EXISTS question_drafts (
          hub_id TEXT NOT NULL, session_ref TEXT NOT NULL, signature TEXT NOT NULL,
          selections TEXT NOT NULL, PRIMARY KEY (hub_id, session_ref)
        )`);
	}

	readQuestions(
		destination: DraftDestination,
		signature: string,
	): QuestionSelections {
		const row = this.db.getFirstSync<{ selections: string }>(
			"SELECT selections FROM question_drafts WHERE hub_id = ? AND session_ref = ? AND signature = ?",
			destination.hubId,
			destination.sessionRef,
			signature,
		);
		return row ? decodeQuestionSelections(row.selections) : {};
	}
	writeQuestions(
		destination: DraftDestination,
		signature: string,
		selections: QuestionSelections,
	): void {
		this.db.runSync(
			`INSERT INTO question_drafts (hub_id, session_ref, signature, selections)
          VALUES (?, ?, ?, ?) ON CONFLICT (hub_id, session_ref) DO UPDATE SET
          signature = excluded.signature, selections = excluded.selections`,
			destination.hubId,
			destination.sessionRef,
			signature,
			JSON.stringify(selections),
		);
	}
	read(destination: DraftDestination): DraftRecord {
		return (
			this.db.getFirstSync<DraftRecord>(
				"SELECT draft, unconfirmed FROM drafts WHERE hub_id = ? AND session_ref = ?",
				destination.hubId,
				destination.sessionRef,
			) ?? { draft: "", unconfirmed: null }
		);
	}

	write(destination: DraftDestination, record: DraftRecord): void {
		if (record.draft === "" && record.unconfirmed === null) {
			this.db.runSync(
				"DELETE FROM drafts WHERE hub_id = ? AND session_ref = ?",
				destination.hubId,
				destination.sessionRef,
			);
			return;
		}
		this.db.runSync(
			`INSERT INTO drafts (hub_id, session_ref, draft, unconfirmed)
       VALUES (?, ?, ?, ?)
       ON CONFLICT (hub_id, session_ref) DO UPDATE SET
         draft = excluded.draft, unconfirmed = excluded.unconfirmed`,
			destination.hubId,
			destination.sessionRef,
			record.draft,
			record.unconfirmed,
		);
	}

	removeHub(hubId: string): void {
		this.db.runSync("DELETE FROM drafts WHERE hub_id = ?", hubId);
		this.db.runSync("DELETE FROM question_drafts WHERE hub_id = ?", hubId);
	}
}
