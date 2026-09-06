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

	private questionRow(destination: DraftDestination) {
		return this.db.getFirstSync<{ signature: string; selections: string }>(
			"SELECT signature, selections FROM question_drafts WHERE hub_id = ? AND session_ref = ?",
			destination.hubId,
			destination.sessionRef,
		);
	}
	readQuestions(
		destination: DraftDestination,
		signature: string,
	): QuestionSelections {
		const row = this.questionRow(destination);
		if (!row) return {};
		const definitions = questionDefinitions(row.signature);
		const selections = decodeQuestionSelections(row.selections);
		const result: QuestionSelections = {};
		for (const [key, definition] of questionDefinitions(signature)) {
			if (definitions.get(key) === definition && selections[key])
				result[key] = selections[key];
		}
		return result;
	}
	writeQuestions(
		destination: DraftDestination,
		signature: string,
		selections: QuestionSelections,
	): void {
		const row = this.questionRow(destination);
		const definitions = row
			? questionDefinitions(row.signature)
			: new Map<string, string>();
		const merged = row ? decodeQuestionSelections(row.selections) : {};
		for (const [key, definition] of questionDefinitions(signature)) {
			definitions.set(key, definition);
			if (selections[key]) merged[key] = selections[key];
			else delete merged[key];
		}
		this.db.runSync(
			`INSERT INTO question_drafts (hub_id, session_ref, signature, selections)
       VALUES (?, ?, ?, ?) ON CONFLICT (hub_id, session_ref) DO UPDATE SET
       signature = excluded.signature, selections = excluded.selections`,
			destination.hubId,
			destination.sessionRef,
			`[${[...definitions.values()].join(",")}]`,
			JSON.stringify(merged),
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

/** Definitions are compared per question so another batch cannot erase edits. */
function questionDefinitions(signature: string): Map<string, string> {
	const value: unknown = JSON.parse(signature);
	if (!Array.isArray(value)) throw new Error("Invalid question definitions");
	const definitions = new Map<string, string>();
	for (const question of value) {
		if (
			!question ||
			typeof question !== "object" ||
			typeof question.key !== "string" ||
			definitions.has(question.key)
		)
			throw new Error("Invalid question definitions");
		definitions.set(question.key, JSON.stringify(question));
	}
	return definitions;
}
