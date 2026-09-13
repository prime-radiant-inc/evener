import { CreationDraftRepository } from "./creationDraftRepository";
import {
	type DraftImage,
	type DraftImageData,
	imageInput,
	parseImages,
} from "./draftImages";
import {
	decodeQuestionSelections,
	type QuestionSelections,
} from "./questionAnswers";
export interface DraftRecord {
	draft: string;
	unconfirmed: string | null;
	images?: DraftImage[];
	unconfirmedImages?: DraftImage[];
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
	readonly creation: CreationDraftRepository;
	constructor(private readonly db: DraftDatabase) {
		this.creation = new CreationDraftRepository(db);
		db.execSync(`CREATE TABLE IF NOT EXISTS draft_images (
      hub_id TEXT NOT NULL, session_ref TEXT NOT NULL, id TEXT NOT NULL,
      media_type TEXT NOT NULL, data TEXT NOT NULL,
      PRIMARY KEY (hub_id, session_ref, id)
    )`);
		db.execSync(`CREATE TABLE IF NOT EXISTS draft_image_sets (
      hub_id TEXT NOT NULL, session_ref TEXT NOT NULL, images TEXT NOT NULL,
      unconfirmed_images TEXT NOT NULL, PRIMARY KEY (hub_id, session_ref)
    )`);
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
		db.execSync(`CREATE TABLE IF NOT EXISTS question_positions (
      hub_id TEXT NOT NULL, session_ref TEXT NOT NULL, question_key TEXT NOT NULL,
      PRIMARY KEY (hub_id, session_ref)
    )`);
	}

	readQuestionPosition(
		destination: DraftDestination,
		keys: string[],
	): string | undefined {
		const row = this.db.getFirstSync<{ question_key: string }>(
			"SELECT question_key FROM question_positions WHERE hub_id = ? AND session_ref = ?",
			destination.hubId,
			destination.sessionRef,
		);
		return row && keys.includes(row.question_key) ? row.question_key : keys[0];
	}
	writeQuestionPosition(destination: DraftDestination, key: string): void {
		this.db.runSync(
			`INSERT INTO question_positions (hub_id, session_ref, question_key)
      VALUES (?, ?, ?) ON CONFLICT (hub_id, session_ref) DO UPDATE SET question_key = excluded.question_key`,
			destination.hubId,
			destination.sessionRef,
			key,
		);
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
		const record = this.db.getFirstSync<DraftRecord>(
			"SELECT draft, unconfirmed FROM drafts WHERE hub_id = ? AND session_ref = ?",
			destination.hubId,
			destination.sessionRef,
		) ?? { draft: "", unconfirmed: null };
		const row = this.db.getFirstSync<{
			images: string;
			unconfirmed_images: string;
		}>(
			"SELECT images, unconfirmed_images FROM draft_image_sets WHERE hub_id = ? AND session_ref = ?",
			destination.hubId,
			destination.sessionRef,
		);
		if (row) {
			const images = parseImages(row.images);
			const unconfirmedImages = parseImages(row.unconfirmed_images);
			for (const image of [...images, ...unconfirmedImages])
				this.requireImage(destination, image);
			if (images.length) record.images = images;
			if (unconfirmedImages.length)
				record.unconfirmedImages = unconfirmedImages;
		}
		return record;
	}

	private requireImage(destination: DraftDestination, image: DraftImage) {
		const row = this.db.getFirstSync<{ media_type: string }>(
			"SELECT media_type FROM draft_images WHERE hub_id = ? AND session_ref = ? AND id = ?",
			destination.hubId,
			destination.sessionRef,
			image.id,
		);
		if (!row || row.media_type !== image.mediaType)
			throw new Error("Saved image is unavailable.");
		return row;
	}

	imageInputs(destination: DraftDestination, images: DraftImage[]) {
		return images.map((image) => {
			this.requireImage(destination, image);
			const row = this.db.getFirstSync<{ data: string }>(
				"SELECT data FROM draft_images WHERE hub_id = ? AND session_ref = ? AND id = ?",
				destination.hubId,
				destination.sessionRef,
				image.id,
			);
			if (!row) throw new Error("Saved image is unavailable.");
			return imageInput(image, row.data);
		});
	}

	write(
		destination: DraftDestination,
		record: DraftRecord,
		additions: DraftImageData[] = [],
	): void {
		const images = parseImages(JSON.stringify(record.images ?? []));
		const unconfirmedImages = parseImages(
			JSON.stringify(record.unconfirmedImages ?? []),
		);
		const referenced = [...images, ...unconfirmedImages];
		this.db.execSync("SAVEPOINT draft_write");
		try {
			for (const image of additions) {
				if (
					!referenced.some(
						(item) =>
							item.id === image.id && item.mediaType === image.mediaType,
					) ||
					!image.data
				)
					throw new Error("Image must belong to this draft.");
				const existing = this.db.getFirstSync<{
					data: string;
					media_type: string;
				}>(
					"SELECT data, media_type FROM draft_images WHERE hub_id = ? AND session_ref = ? AND id = ?",
					destination.hubId,
					destination.sessionRef,
					image.id,
				);
				if (
					existing &&
					(existing.data !== image.data ||
						existing.media_type !== image.mediaType)
				)
					throw new Error("Saved image identity cannot be replaced.");
				if (!existing)
					this.db.runSync(
						"INSERT INTO draft_images (hub_id, session_ref, id, media_type, data) VALUES (?, ?, ?, ?, ?)",
						destination.hubId,
						destination.sessionRef,
						image.id,
						image.mediaType,
						image.data,
					);
			}
			for (const image of referenced) this.requireImage(destination, image);
			if (referenced.length)
				this.db.runSync(
					`INSERT INTO draft_image_sets (hub_id, session_ref, images, unconfirmed_images) VALUES (?, ?, ?, ?)
        ON CONFLICT (hub_id, session_ref) DO UPDATE SET images = excluded.images, unconfirmed_images = excluded.unconfirmed_images`,
					destination.hubId,
					destination.sessionRef,
					JSON.stringify(images),
					JSON.stringify(unconfirmedImages),
				);
			else
				this.db.runSync(
					"DELETE FROM draft_image_sets WHERE hub_id = ? AND session_ref = ?",
					destination.hubId,
					destination.sessionRef,
				);
			this.writeText(destination, record);
			const ids = [...new Set(referenced.map((image) => image.id))];
			this.db.runSync(
				`DELETE FROM draft_images WHERE hub_id = ? AND session_ref = ?${ids.length ? ` AND id NOT IN (${ids.map(() => "?").join(",")})` : ""}`,
				destination.hubId,
				destination.sessionRef,
				...ids,
			);
			this.db.execSync("RELEASE draft_write");
		} catch (error) {
			this.db.execSync("ROLLBACK TO draft_write; RELEASE draft_write");
			throw error;
		}
	}

	private writeText(destination: DraftDestination, record: DraftRecord): void {
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
		this.creation.clear(hubId);
		this.db.runSync("DELETE FROM draft_image_sets WHERE hub_id = ?", hubId);
		this.db.runSync("DELETE FROM draft_images WHERE hub_id = ?", hubId);
		this.db.runSync("DELETE FROM drafts WHERE hub_id = ?", hubId);
		this.db.runSync("DELETE FROM question_drafts WHERE hub_id = ?", hubId);
		this.db.runSync("DELETE FROM question_positions WHERE hub_id = ?", hubId);
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
