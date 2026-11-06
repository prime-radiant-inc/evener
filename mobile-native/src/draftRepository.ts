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
		// Durable evidence of committed handoff transfers: written inside the
		// transfer's own savepoint, so a restart cannot lose the proof the
		// way the library's in-memory retired map can. The disposition lists
		// ride beside the delivered hash for the same reason: they are the
		// delivery's own exclusions, and after a restart nothing else carries
		// what the transferred composition had completed or released.
		db.execSync(`CREATE TABLE IF NOT EXISTS draft_transfer_log (
      edge_id TEXT PRIMARY KEY, hub_id TEXT NOT NULL, session_ref TEXT NOT NULL, delivered_hash TEXT NOT NULL,
      completed_command_recovery_ids TEXT NOT NULL DEFAULT '[]', released_recovery_ids TEXT NOT NULL DEFAULT '[]'
    )`);
		const transferLogColumns = new Set(
			db.getAllSync<{ name: string }>("PRAGMA table_info(draft_transfer_log)").map((column) => column.name),
		);
		for (const name of ["completed_command_recovery_ids", "released_recovery_ids"] as const) {
			if (!transferLogColumns.has(name))
				db.execSync(`ALTER TABLE draft_transfer_log ADD COLUMN ${name} TEXT NOT NULL DEFAULT '[]'`);
		}
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
	 * destination write commits, so a failed handoff leaves both recoverable.
	 * `evidence` records the handoff edge this move serves inside the same
	 * savepoint, so the committed transfer survives a restart as durable
	 * evidence. */
	transfer(
		from: DraftDestination,
		to: DraftDestination,
		record: DraftRecord,
		additions: DraftImageData[] = [],
		evidence?: {
			edgeId: string;
			deliveredHash: string;
			completedCommandRecoveryIds?: readonly string[];
			releasedRecoveryIds?: readonly string[];
		},
	): void {
		withSavepoint(this.db, "draft_transfer", () => {
			if (evidence !== undefined) {
				this.db.runSync(
					`INSERT INTO draft_transfer_log (edge_id, hub_id, session_ref, delivered_hash,
					     completed_command_recovery_ids, released_recovery_ids)
					 VALUES (?, ?, ?, ?, ?, ?)
					 ON CONFLICT (edge_id) DO UPDATE SET hub_id = excluded.hub_id,
						 session_ref = excluded.session_ref, delivered_hash = excluded.delivered_hash,
						 completed_command_recovery_ids = excluded.completed_command_recovery_ids,
						 released_recovery_ids = excluded.released_recovery_ids`,
					evidence.edgeId,
					to.hubId,
					to.sessionRef,
					evidence.deliveredHash,
					JSON.stringify(evidence.completedCommandRecoveryIds ?? []),
					JSON.stringify(evidence.releasedRecoveryIds ?? []),
				);
			}
			this.write(to, record, additions);
			this.remove(from);
		});
	}

	/** Durable evidence that a handoff edge's transfer committed: written in
	 * the transfer's own savepoint, so it survives restarts and the
	 * destination later clearing or recomposing its copy. The disposition
	 * lists are the delivery's own exclusions as they were at transfer time -
	 * after a restart they are the only durable record of what the
	 * transferred composition had completed or released. */
	readTransferEvidence(edgeId: string):
		| {
				hubId: string;
				sessionRef: string;
				deliveredHash: string;
				completedCommandRecoveryIds: string[];
				releasedRecoveryIds: string[];
		  }
		| undefined {
		const row = this.db.getFirstSync<{
			hubId: string;
			sessionRef: string;
			deliveredHash: string;
			completedCommandRecoveryIds: string | null;
			releasedRecoveryIds: string | null;
		}>(
			`SELECT hub_id AS "hubId", session_ref AS "sessionRef", delivered_hash AS "deliveredHash",
			     completed_command_recovery_ids AS "completedCommandRecoveryIds",
			     released_recovery_ids AS "releasedRecoveryIds"
			 FROM draft_transfer_log WHERE edge_id = ?`,
			edgeId,
		);
		if (row === null) return undefined;
		// A stored list is JSON; a missing one predates the column and reads
		// as empty, never as an unknown the caller must defer on.
		const list = (raw: string | null): string[] => {
			if (raw === null) return [];
			try {
				const parsed: unknown = JSON.parse(raw);
				return Array.isArray(parsed) ? parsed.filter((id): id is string => typeof id === "string") : [];
			} catch {
				return [];
			}
		};
		return {
			hubId: row.hubId,
			sessionRef: row.sessionRef,
			deliveredHash: row.deliveredHash,
			completedCommandRecoveryIds: list(row.completedCommandRecoveryIds),
			releasedRecoveryIds: list(row.releasedRecoveryIds),
		};
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
