import { CreationDraftRepository } from "./creationDraftRepository";
import { type DraftImage, type DraftImageData, imageInput, parseImages } from "./draftImages";
import {
	decodeQuestionSelections,
	questionDefinition,
	sameQuestion,
	type QuestionDefinition,
	type QuestionSelections,
} from "./questionAnswers";
import { type SqliteSync, withSavepoint } from "./sqliteSync";
import type { ComposerMention } from "@evener/appwire-client";

export interface DraftComposerMetadata {
	skillNames?: string[];
	commandNames?: string[];
	mentions?: ComposerMention[];
	recoveryIds?: string[];
}
export interface DraftRecord {
	draft: string;
	unconfirmed: string | null;
	images?: DraftImage[];
	unconfirmedImages?: DraftImage[];
	/** Work deliberate replacements (the goal editor) set aside, each kept
	 * whole with its own recovery sources and never merged into a replacement. */
	parkedCompositions?: DraftParkedComposition[];
	unconfirmedCaptureToken?: string;
	composer?: DraftComposerMetadata;
	unconfirmedComposer?: DraftComposerMetadata;
	recoveryResendAcks?: string[];
	completedCommandRecoveryIds?: string[];
	releasedRecoveryIds?: string[];
}

export interface DraftParkedComposition {
	id: string;
	text: string;
	images?: DraftImage[];
	composer?: DraftComposerMetadata;
}

export interface DraftDestination {
	hubId: string;
	sessionRef: string;
}

export class DraftRepository {
	readonly creation: CreationDraftRepository;
	constructor(private readonly db: SqliteSync) {
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
		db.execSync(`CREATE TABLE IF NOT EXISTS pending_draft_handoffs (
      from_hub_id TEXT NOT NULL,
      from_session_ref TEXT NOT NULL,
      to_hub_id TEXT NOT NULL,
      to_session_ref TEXT NOT NULL,
      PRIMARY KEY (to_hub_id, to_session_ref)
    )`);
		const draftColumns = new Set(
			db.getAllSync<{ name: string }>("PRAGMA table_info(drafts)").map((column) => column.name),
		);
		for (const [name, type] of [
			["composer", "TEXT"],
			["unconfirmed_composer", "TEXT"],
			["parked_composition", "TEXT"],
			["unconfirmed_capture_token", "TEXT"],
			["recovery_resend_acks", "TEXT"],
			["completed_command_recovery_ids", "TEXT"],
			["released_recovery_ids", "TEXT"],
		] as const) {
			if (!draftColumns.has(name)) db.execSync(`ALTER TABLE drafts ADD COLUMN ${name} ${type}`);
		}
		db.execSync(`CREATE TABLE IF NOT EXISTS question_drafts (
          hub_id TEXT NOT NULL, session_ref TEXT NOT NULL, signature TEXT NOT NULL,
          selections TEXT NOT NULL, PRIMARY KEY (hub_id, session_ref)
        )`);
		db.execSync(`CREATE TABLE IF NOT EXISTS question_positions (
      hub_id TEXT NOT NULL, session_ref TEXT NOT NULL, question_key TEXT NOT NULL,
      PRIMARY KEY (hub_id, session_ref)
    )`);
	}

	readQuestionPosition(destination: DraftDestination, keys: string[]): string | undefined {
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
	readQuestions(destination: DraftDestination, signature: string): QuestionSelections {
		const row = this.questionRow(destination);
		if (!row) return {};
		const definitions = questionDefinitions(row.signature);
		const selections = decodeQuestionSelections(row.selections);
		const result: QuestionSelections = {};
		for (const current of questionDefinitions(signature).values()) {
			if (sameQuestion(definitions.get(current.key), current) && selections[current.key])
				result[current.key] = selections[current.key];
		}
		return result;
	}
	writeQuestions(destination: DraftDestination, signature: string, selections: QuestionSelections): void {
		const row = this.questionRow(destination);
		const definitions = row ? questionDefinitions(row.signature) : new Map<string, QuestionDefinition>();
		const merged = row ? decodeQuestionSelections(row.selections) : {};
		for (const current of questionDefinitions(signature).values()) {
			definitions.set(current.key, current);
			if (selections[current.key]) merged[current.key] = selections[current.key];
			else delete merged[current.key];
		}
		this.db.runSync(
			`INSERT INTO question_drafts (hub_id, session_ref, signature, selections)
       VALUES (?, ?, ?, ?) ON CONFLICT (hub_id, session_ref) DO UPDATE SET
       signature = excluded.signature, selections = excluded.selections`,
			destination.hubId,
			destination.sessionRef,
			`[${[...definitions.values()].map((definition) => JSON.stringify(definition)).join(",")}]`,
			JSON.stringify(merged),
		);
	}
	read(destination: DraftDestination): DraftRecord {
		const draftRow = this.db.getFirstSync<{
			draft: string;
			unconfirmed: string | null;
			composer: string | null;
			unconfirmed_composer: string | null;
			parked_composition: string | null;
			unconfirmed_capture_token: string | null;
			recovery_resend_acks: string | null;
			completed_command_recovery_ids: string | null;
			released_recovery_ids: string | null;
		}>(
			"SELECT draft, unconfirmed, composer, unconfirmed_composer, parked_composition, unconfirmed_capture_token, recovery_resend_acks, completed_command_recovery_ids, released_recovery_ids FROM drafts WHERE hub_id = ? AND session_ref = ?",
			destination.hubId,
			destination.sessionRef,
		);
		const record: DraftRecord = { draft: draftRow?.draft ?? "", unconfirmed: draftRow?.unconfirmed ?? null };
		if (draftRow?.composer) record.composer = JSON.parse(draftRow.composer) as DraftComposerMetadata;
		if (draftRow?.unconfirmed_composer)
			record.unconfirmedComposer = JSON.parse(draftRow.unconfirmed_composer) as DraftComposerMetadata;
		if (draftRow?.parked_composition) {
			const parks = this.readParks(destination, draftRow.parked_composition);
			if (parks.length) record.parkedCompositions = parks;
		}
		if (draftRow?.unconfirmed_capture_token) record.unconfirmedCaptureToken = draftRow.unconfirmed_capture_token;
		if (draftRow?.recovery_resend_acks)
			record.recoveryResendAcks = JSON.parse(draftRow.recovery_resend_acks) as string[];
		if (draftRow?.completed_command_recovery_ids)
			record.completedCommandRecoveryIds = JSON.parse(draftRow.completed_command_recovery_ids) as string[];
		if (draftRow?.released_recovery_ids)
			record.releasedRecoveryIds = JSON.parse(draftRow.released_recovery_ids) as string[];
		const imageRow = this.db.getFirstSync<{
			images: string;
			unconfirmed_images: string;
		}>(
			"SELECT images, unconfirmed_images FROM draft_image_sets WHERE hub_id = ? AND session_ref = ?",
			destination.hubId,
			destination.sessionRef,
		);
		if (imageRow) {
			const images = parseImages(imageRow.images);
			const unconfirmedImages = parseImages(imageRow.unconfirmed_images);
			for (const image of [...images, ...unconfirmedImages]) this.requireImage(destination, image);
			if (images.length) record.images = images;
			if (unconfirmedImages.length) record.unconfirmedImages = unconfirmedImages;
		}
		return record;
	}

	private readParks(destination: DraftDestination, value: string): DraftParkedComposition[] {
		const parsed = JSON.parse(value) as unknown;
		const entries = Array.isArray(parsed) ? parsed : [parsed];
		return entries.flatMap((entry, index) => {
			if (!entry || typeof entry !== "object") return [];
			const parked = entry as { id?: unknown; text?: unknown; images?: unknown; composer?: unknown };
			const images = parseImages(JSON.stringify(parked.images ?? []));
			for (const image of images) this.requireImage(destination, image);
			return [
				{
					id: typeof parked.id === "string" && parked.id ? parked.id : `parked-legacy-${index}`,
					text: typeof parked.text === "string" ? parked.text : "",
					...(images.length ? { images } : {}),
					...(parked.composer ? { composer: parked.composer as DraftComposerMetadata } : {}),
				},
			];
		});
	}

	private requireImage(destination: DraftDestination, image: DraftImage) {
		const row = this.db.getFirstSync<{ media_type: string }>(
			"SELECT media_type FROM draft_images WHERE hub_id = ? AND session_ref = ? AND id = ?",
			destination.hubId,
			destination.sessionRef,
			image.id,
		);
		if (!row || row.media_type !== image.mediaType) throw new Error("Saved image is unavailable.");
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

	write(destination: DraftDestination, record: DraftRecord, additions: DraftImageData[] = []): void {
		const images = parseImages(JSON.stringify(record.images ?? []));
		const unconfirmedImages = parseImages(JSON.stringify(record.unconfirmedImages ?? []));
		const parkedImages = (record.parkedCompositions ?? []).flatMap((parked) =>
			parseImages(JSON.stringify(parked.images ?? [])),
		);
		const referenced = [...images, ...unconfirmedImages, ...parkedImages];
		withSavepoint(this.db, "draft_write", () => {
			for (const image of additions) {
				if (!referenced.some((item) => item.id === image.id && item.mediaType === image.mediaType) || !image.data)
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
				if (existing && (existing.data !== image.data || existing.media_type !== image.mediaType))
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
		});
	}

	/** Move one destination's complete draft, image bytes and settlement
	 * markers to another in one savepoint. The source is removed only after the
	 * destination write commits, so a failed handoff leaves both recoverable. */
	transfer(from: DraftDestination, to: DraftDestination, record: DraftRecord, additions: DraftImageData[] = []): void {
		withSavepoint(this.db, "draft_transfer", () => {
			this.write(to, record, additions);
			this.remove(from);
		});
	}

	/** A handoff whose destination write has not committed yet. The mapping is
	 * durable so a restart can retry the transfer before the resumed screen
	 * restores the source's original recovery text. */
	readPendingHandoffs(): { from: DraftDestination; to: DraftDestination }[] {
		const rows = this.db.getAllSync<{
			from_hub_id: string;
			from_session_ref: string;
			to_hub_id: string;
			to_session_ref: string;
		}>("SELECT from_hub_id, from_session_ref, to_hub_id, to_session_ref FROM pending_draft_handoffs");
		return rows.map((row) => ({
			from: { hubId: row.from_hub_id, sessionRef: row.from_session_ref },
			to: { hubId: row.to_hub_id, sessionRef: row.to_session_ref },
		}));
	}

	writePendingHandoff(from: DraftDestination, to: DraftDestination): void {
		this.db.runSync(
			`INSERT INTO pending_draft_handoffs (from_hub_id, from_session_ref, to_hub_id, to_session_ref)
       VALUES (?, ?, ?, ?) ON CONFLICT (to_hub_id, to_session_ref) DO UPDATE SET
       from_hub_id = excluded.from_hub_id, from_session_ref = excluded.from_session_ref`,
			from.hubId,
			from.sessionRef,
			to.hubId,
			to.sessionRef,
		);
	}

	/** Replace one pending handoff's destination atomically. A redirected
	 * chain must not leave the predecessor's old mapping for a restart to
	 * replay into the retired ref. */
	redirectPendingHandoff(from: DraftDestination, previousTo: DraftDestination, to: DraftDestination): void {
		if (previousTo.hubId === to.hubId && previousTo.sessionRef === to.sessionRef) return;
		withSavepoint(this.db, "draft_handoff_redirect", () => {
			this.writePendingHandoff(from, to);
			this.db.runSync(
				`DELETE FROM pending_draft_handoffs
				 WHERE from_hub_id = ? AND from_session_ref = ? AND to_hub_id = ? AND to_session_ref = ?`,
				from.hubId,
				from.sessionRef,
				previousTo.hubId,
				previousTo.sessionRef,
			);
		});
	}

	removePendingHandoff(to: DraftDestination): void {
		this.db.runSync(
			"DELETE FROM pending_draft_handoffs WHERE to_hub_id = ? AND to_session_ref = ?",
			to.hubId,
			to.sessionRef,
		);
	}

	remove(destination: DraftDestination): void {
		withSavepoint(this.db, "draft_remove", () => {
			this.db.runSync(
				"DELETE FROM draft_image_sets WHERE hub_id = ? AND session_ref = ?",
				destination.hubId,
				destination.sessionRef,
			);
			this.db.runSync(
				"DELETE FROM draft_images WHERE hub_id = ? AND session_ref = ?",
				destination.hubId,
				destination.sessionRef,
			);
			this.db.runSync(
				"DELETE FROM drafts WHERE hub_id = ? AND session_ref = ?",
				destination.hubId,
				destination.sessionRef,
			);
		});
	}

	private writeText(destination: DraftDestination, record: DraftRecord): void {
		if (
			record.draft === "" &&
			record.unconfirmed === null &&
			!record.composer &&
			!record.unconfirmedComposer &&
			!record.parkedCompositions?.length &&
			!record.unconfirmedCaptureToken &&
			!record.recoveryResendAcks?.length &&
			!record.completedCommandRecoveryIds?.length &&
			!record.releasedRecoveryIds?.length
		) {
			this.db.runSync(
				"DELETE FROM drafts WHERE hub_id = ? AND session_ref = ?",
				destination.hubId,
				destination.sessionRef,
			);
			return;
		}
		this.db.runSync(
			`INSERT INTO drafts (hub_id, session_ref, draft, unconfirmed, composer, unconfirmed_composer, parked_composition, unconfirmed_capture_token, recovery_resend_acks, completed_command_recovery_ids, released_recovery_ids)
				       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
       ON CONFLICT (hub_id, session_ref) DO UPDATE SET
	         draft = excluded.draft, unconfirmed = excluded.unconfirmed,
	         composer = excluded.composer, unconfirmed_composer = excluded.unconfirmed_composer,
			         parked_composition = excluded.parked_composition,
		         unconfirmed_capture_token = excluded.unconfirmed_capture_token,
		         recovery_resend_acks = excluded.recovery_resend_acks,
		         completed_command_recovery_ids = excluded.completed_command_recovery_ids,
		         released_recovery_ids = excluded.released_recovery_ids`,
			destination.hubId,
			destination.sessionRef,
			record.draft,
			record.unconfirmed,
			record.composer ? JSON.stringify(record.composer) : null,
			record.unconfirmedComposer ? JSON.stringify(record.unconfirmedComposer) : null,
			record.parkedCompositions?.length ? JSON.stringify(record.parkedCompositions) : null,
			record.unconfirmedCaptureToken ?? null,
			record.recoveryResendAcks?.length ? JSON.stringify(record.recoveryResendAcks) : null,
			record.completedCommandRecoveryIds?.length ? JSON.stringify(record.completedCommandRecoveryIds) : null,
			record.releasedRecoveryIds?.length ? JSON.stringify(record.releasedRecoveryIds) : null,
		);
	}

	/** User composition and recovery ownership are discoverable. Acknowledgment
	 * housekeeping and empty metadata alone are not draft indicators. */
	refsWithDrafts(hubId: string): Set<string> {
		const rows = this.db.getAllSync<{ session_ref: string }>(
			`SELECT session_ref FROM drafts WHERE hub_id = ? AND (
           draft != '' OR unconfirmed IS NOT NULL
           OR parked_composition IS NOT NULL
           OR json_array_length(composer, '$.skillNames') > 0
           OR json_array_length(composer, '$.commandNames') > 0
           OR json_array_length(composer, '$.recoveryIds') > 0
           OR json_array_length(unconfirmed_composer, '$.skillNames') > 0
           OR json_array_length(unconfirmed_composer, '$.commandNames') > 0
           OR json_array_length(unconfirmed_composer, '$.recoveryIds') > 0)
       UNION SELECT session_ref FROM draft_image_sets WHERE hub_id = ?`,
			hubId,
			hubId,
		);
		return new Set(rows.map((row) => row.session_ref));
	}

	removeHub(hubId: string): void {
		this.creation.clear(hubId);
		this.db.runSync("DELETE FROM pending_draft_handoffs WHERE from_hub_id = ? OR to_hub_id = ?", hubId, hubId);
		this.db.runSync("DELETE FROM draft_image_sets WHERE hub_id = ?", hubId);
		this.db.runSync("DELETE FROM draft_images WHERE hub_id = ?", hubId);
		this.db.runSync("DELETE FROM drafts WHERE hub_id = ?", hubId);
		this.db.runSync("DELETE FROM question_drafts WHERE hub_id = ?", hubId);
		this.db.runSync("DELETE FROM question_positions WHERE hub_id = ?", hubId);
	}
}

/** Definitions are compared per question so another batch cannot erase edits.
 * Each definition normalizes through questionAnswers.ts's questionDefinition,
 * so a row saved under the pre-identity signature — the full canonical
 * question, or the display bound's truncated copy — compares equal to the
 * identity's signature of the same question (questionAnswers.ts's
 * sameQuestion). */
function questionDefinitions(signature: string): Map<string, QuestionDefinition> {
	const value: unknown = JSON.parse(signature);
	if (!Array.isArray(value)) throw new Error("Invalid question definitions");
	const definitions = new Map<string, QuestionDefinition>();
	for (const question of value) {
		if (!question || typeof question !== "object" || typeof question.key !== "string" || definitions.has(question.key))
			throw new Error("Invalid question definitions");
		definitions.set(question.key, questionDefinition(question));
	}
	return definitions;
}
