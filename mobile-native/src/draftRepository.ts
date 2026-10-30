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

export type DraftTransferState = "pending" | "settled" | "refused";

/** One handoff hop's durable evidence, written in the transfer's own savepoint.
 * The row exists from the moment the handoff is intended: `deliveredHash` is
 * null while the transfer has not committed, and carries the composition
 * fingerprint once it has, so a restart can tell "never landed" from "landed,
 * settlement owed". The from→to pair is also the forward pointer a completion
 * follows to find the identity that owns the record now. */
export interface DraftTransfer {
	transferId: string;
	from: DraftDestination;
	to: DraftDestination;
	state: DraftTransferState;
	deliveredHash: string | null;
	/** The unresolved capture the transferred record carried, if any. */
	carriedCaptureToken: string | null;
	/** The record generation the transferred record belongs to: stamped when
	 * a record first moves and carried by every later hop of the same record,
	 * so settlement follows the record, not just any hop out of an identity. */
	carriedGeneration: string | null;
	completedCommandRecoveryIds: string[];
	releasedRecoveryIds: string[];
	createdAt: number;
}

export class DraftRepository {
	readonly creation: CreationDraftRepository;
	constructor(
		private readonly db: SqliteSync,
		private readonly now: () => number = Date.now,
	) {
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
			["record_generation", "TEXT"],
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
		db.execSync(`CREATE TABLE IF NOT EXISTS draft_transfer_log (
      transfer_id TEXT PRIMARY KEY,
      from_hub_id TEXT NOT NULL, from_ref TEXT NOT NULL,
      to_hub_id TEXT NOT NULL, to_ref TEXT NOT NULL,
      state TEXT NOT NULL,
      delivered_hash TEXT,
	      carried_capture_token TEXT,
	      commit_sequence INTEGER,
	      carried_generation TEXT,
      completed_command_recovery_ids TEXT NOT NULL DEFAULT '[]',
      released_recovery_ids TEXT NOT NULL DEFAULT '[]',
      created_at INTEGER NOT NULL,
      state_changed_at INTEGER NOT NULL
    )`);
		// Created once; nothing later drops or rebuilds them. The outstanding
		// pass prices in pending hops only, and the pointer walk and the
		// pending-destination check read by identity, never by scan.
		db.execSync(
			`CREATE INDEX IF NOT EXISTS draft_transfer_log_outstanding
      ON draft_transfer_log (created_at) WHERE state = 'pending'`,
		);
		db.execSync(
			`CREATE INDEX IF NOT EXISTS draft_transfer_log_from
      ON draft_transfer_log (from_hub_id, from_ref, created_at)`,
		);
		db.execSync(
			`CREATE INDEX IF NOT EXISTS draft_transfer_log_to
      ON draft_transfer_log (to_hub_id, to_ref, created_at)`,
		);
		// Branch-created table: an earlier build of this branch created it
		// without the carried token or the commit sequence, so add them.
		const transferColumns = new Set(
			this.db.getAllSync<{ name: string }>("PRAGMA table_info(draft_transfer_log)").map((column) => column.name),
		);
		for (const name of ["carried_capture_token", "commit_sequence", "carried_generation"] as const)
			if (!transferColumns.has(name)) this.db.execSync(`ALTER TABLE draft_transfer_log ADD COLUMN ${name}`);
		db.execSync(
			`CREATE INDEX IF NOT EXISTS draft_transfer_log_carried
      ON draft_transfer_log (carried_capture_token, commit_sequence) WHERE carried_capture_token IS NOT NULL`,
		);
		db.execSync(
			`CREATE INDEX IF NOT EXISTS draft_transfer_log_generation
      ON draft_transfer_log (from_hub_id, from_ref, carried_generation, commit_sequence)
      WHERE carried_generation IS NOT NULL`,
		);
		// An earlier build recorded neither commit order nor carried evidence.
		// Recover what the durable rows still prove: commit sequences in
		// created order, and - for the latest committed arrival at a
		// destination - the capture token its record still holds plus a
		// generation that record keeps carrying. Rows whose record a later
		// arrival replaced are beyond proof; they keep null and never route.
		const unsequenced = db.getAllSync<{ transfer_id: string }>(
			`SELECT transfer_id FROM draft_transfer_log
      WHERE commit_sequence IS NULL AND delivered_hash IS NOT NULL ORDER BY created_at, rowid`,
		);
		if (unsequenced.length > 0) {
			let next =
				db.getFirstSync<{ top: number }>("SELECT COALESCE(MAX(commit_sequence), 0) AS top FROM draft_transfer_log")
					?.top ?? 0;
			for (const row of unsequenced)
				db.runSync(
					"UPDATE draft_transfer_log SET commit_sequence = ? WHERE transfer_id = ?",
					(next = next + 1),
					row.transfer_id,
				);
		}
		const unrouted = db.getAllSync<{
			transfer_id: string;
			to_hub_id: string;
			to_ref: string;
			commit_sequence: number;
		}>(
			`SELECT transfer_id, to_hub_id, to_ref, commit_sequence FROM draft_transfer_log
      WHERE carried_generation IS NULL AND delivered_hash IS NOT NULL ORDER BY commit_sequence, rowid`,
		);
		for (const row of unrouted) {
			const later = db.getFirstSync<{ found: number }>(
				`SELECT COUNT(*) AS found FROM draft_transfer_log
      WHERE to_hub_id = ? AND to_ref = ? AND delivered_hash IS NOT NULL AND commit_sequence > ?`,
				row.to_hub_id,
				row.to_ref,
				row.commit_sequence,
			);
			if (later === null || later.found > 0) continue;
			const endpoint = db.getFirstSync<{
				generation: string | null;
				unconfirmed_capture_token: string | null;
			}>(
				"SELECT record_generation AS generation, unconfirmed_capture_token FROM drafts WHERE hub_id = ? AND session_ref = ?",
				row.to_hub_id,
				row.to_ref,
			);
			if (endpoint === null) continue;
			const generation = endpoint.generation ?? row.transfer_id;
			db.runSync(
				`UPDATE draft_transfer_log SET carried_generation = ?,
      carried_capture_token = COALESCE(carried_capture_token, ?) WHERE transfer_id = ?`,
				generation,
				endpoint.unconfirmed_capture_token,
				row.transfer_id,
			);
			db.runSync(
				"UPDATE drafts SET record_generation = ? WHERE hub_id = ? AND session_ref = ?",
				generation,
				row.to_hub_id,
				row.to_ref,
			);
		}
	}

	/** Record a handoff hop as intended, before any bytes move. Until the
	 * transfer commits it is the durable pending handoff a restart retries. */
	beginTransfer(from: DraftDestination, to: DraftDestination, transferId: string): DraftTransfer {
		const at = this.now();
		this.db.runSync(
			`INSERT INTO draft_transfer_log
      (transfer_id, from_hub_id, from_ref, to_hub_id, to_ref, state, created_at, state_changed_at)
    VALUES (?, ?, ?, ?, ?, 'pending', ?, ?)`,
			transferId,
			from.hubId,
			from.sessionRef,
			to.hubId,
			to.sessionRef,
			at,
			at,
		);
		return {
			transferId,
			from,
			to,
			state: "pending",
			deliveredHash: null,
			carriedCaptureToken: null,
			carriedGeneration: null,
			completedCommandRecoveryIds: [],
			releasedRecoveryIds: [],
			createdAt: at,
		};
	}

	/** The pending handoff hops, oldest first: the retry pass's whole diet. */
	outstandingTransfers(): DraftTransfer[] {
		return this.db
			.getAllSync<TransferRow>(`SELECT * FROM draft_transfer_log WHERE state = 'pending' ORDER BY created_at, rowid`)
			.map((row) => this.mapTransfer(row));
	}

	/** The hops still on their way into a destination: what a hop out of
	 * that destination must reroute before it retires the identity. */
	pendingTransfersTo(to: DraftDestination): DraftTransfer[] {
		return this.db
			.getAllSync<TransferRow>(
				`SELECT * FROM draft_transfer_log
      WHERE to_hub_id = ? AND to_ref = ? AND state = 'pending' AND delivered_hash IS NULL
      ORDER BY created_at, rowid`,
				to.hubId,
				to.sessionRef,
			)
			.map((row) => this.mapTransfer(row));
	}

	/** The latest committed hop out of an identity: the forward pointer a
	 * completion follows toward the record's current owner. A row whose
	 * transfer never committed moved nothing, so it is not a pointer. */
	latestCommittedTransferFrom(from: DraftDestination): DraftTransfer | undefined {
		const row = this.db.getFirstSync<TransferRow>(
			`SELECT * FROM draft_transfer_log
      WHERE from_hub_id = ? AND from_ref = ? AND delivered_hash IS NOT NULL
      ORDER BY commit_sequence DESC LIMIT 1`,
			from.hubId,
			from.sessionRef,
		);
		return row === null ? undefined : this.mapTransfer(row);
	}

	/** The latest committed hop whose transferred record carried this capture
	 * token: while a submission is unresolved its token travels inside the
	 * record, so this row names where the record went last - regardless of
	 * what other work later moved through the identities along the way. */
	latestTransferCarrying(captureToken: string): DraftTransfer | undefined {
		const row = this.db.getFirstSync<TransferRow>(
			`SELECT * FROM draft_transfer_log
      WHERE carried_capture_token = ? AND delivered_hash IS NOT NULL
      ORDER BY commit_sequence DESC LIMIT 1`,
			captureToken,
		);
		return row === null ? undefined : this.mapTransfer(row);
	}

	/** The latest committed hop out of an identity that carried this record
	 * generation: the pointer a settlement follows toward the identity that
	 * owns that record now. A hop that carried other work - however recent -
	 * moved a different record and is not this one's pointer. */
	latestCommittedTransferFromCarrying(from: DraftDestination, generation: string): DraftTransfer | undefined {
		const row = this.db.getFirstSync<TransferRow>(
			`SELECT * FROM draft_transfer_log
      WHERE from_hub_id = ? AND from_ref = ? AND carried_generation = ? AND delivered_hash IS NOT NULL
      ORDER BY commit_sequence DESC LIMIT 1`,
			from.hubId,
			from.sessionRef,
			generation,
		);
		return row === null ? undefined : this.mapTransfer(row);
	}

	/** Whether a transfer is still on its way into this destination. */
	hasPendingTransferTo(to: DraftDestination): boolean {
		const row = this.db.getFirstSync<{ found: number }>(
			`SELECT 1 AS found FROM draft_transfer_log
      WHERE to_hub_id = ? AND to_ref = ? AND state = 'pending' LIMIT 1`,
			to.hubId,
			to.sessionRef,
		);
		return row !== null;
	}

	/** A hop whose destination proved occupied never moved anything: its row
	 * goes away so no later walk or retry treats it as evidence. */
	forgetTransfer(transferId: string): void {
		this.db.runSync("DELETE FROM draft_transfer_log WHERE transfer_id = ?", transferId);
	}

	/** A hop is settled once its dispositions are durably delivered. */
	markTransferSettled(transferId: string): void {
		this.db.runSync(
			`UPDATE draft_transfer_log SET state = 'settled', state_changed_at = ?
      WHERE transfer_id = ?`,
			this.now(),
			transferId,
		);
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
	 * markers to another in one savepoint, recording the hop's evidence in the
	 * same savepoint so it can never lag the move. The source is removed only
	 * after the destination write commits, so a failed handoff leaves both
	 * recoverable. */
	transfer(
		from: DraftDestination,
		to: DraftDestination,
		record: DraftRecord,
		additions: DraftImageData[] = [],
		evidence?: {
			transferId: string;
			deliveredHash: string;
			carriedCaptureToken?: string;
			completedCommandRecoveryIds?: readonly string[];
			releasedRecoveryIds?: readonly string[];
		},
		reroutes: readonly { transferId: string; to: DraftDestination }[] = [],
	): void {
		withSavepoint(this.db, "draft_transfer", () => {
			if (evidence !== undefined)
				this.db.runSync(
					`UPDATE draft_transfer_log SET delivered_hash = ?,
      carried_capture_token = ?, completed_command_recovery_ids = ?, released_recovery_ids = ?, state_changed_at = ?
    WHERE transfer_id = ?`,
					evidence.deliveredHash,
					evidence.carriedCaptureToken ?? null,
					JSON.stringify(evidence.completedCommandRecoveryIds ?? []),
					JSON.stringify(evidence.releasedRecoveryIds ?? []),
					this.now(),
					evidence.transferId,
				);
			// Written inside the same savepoint as the move: a hop intended
			// earlier can commit later, and capture routing must follow commit
			// order, never intent order.
			this.db.runSync(
				`UPDATE draft_transfer_log SET commit_sequence =
      (SELECT COALESCE(MAX(commit_sequence), 0) + 1 FROM draft_transfer_log)
    WHERE transfer_id = ?`,
				evidence?.transferId ?? "",
			);
			// Written inside the same savepoint as the move: a hop still on its
			// way into the source this hop retires must follow the work onward,
			// atomically, or it would later deliver into the retired identity.
			for (const reroute of reroutes)
				this.db.runSync(
					`UPDATE draft_transfer_log SET to_hub_id = ?, to_ref = ?, state_changed_at = ?
      WHERE transfer_id = ? AND state = 'pending' AND delivered_hash IS NULL`,
					reroute.to.hubId,
					reroute.to.sessionRef,
					this.now(),
					reroute.transferId,
				);
			// The record's generation, written in the same savepoint: stamped
			// when a record first moves, preserved by every later save of that
			// record and carried by each later hop, so settlement follows the
			// record itself and never other work through the same identity.
			let generation: string | null = null;
			if (evidence !== undefined) {
				const fromRow = this.db.getFirstSync<{ generation: string | null }>(
					"SELECT record_generation AS generation FROM drafts WHERE hub_id = ? AND session_ref = ?",
					from.hubId,
					from.sessionRef,
				);
				generation = fromRow?.generation ?? evidence.transferId;
				this.db.runSync(
					"UPDATE draft_transfer_log SET carried_generation = ? WHERE transfer_id = ?",
					generation,
					evidence.transferId,
				);
			}
			this.write(to, record, additions);
			if (evidence !== undefined)
				this.db.runSync(
					"UPDATE drafts SET record_generation = ? WHERE hub_id = ? AND session_ref = ?",
					generation,
					to.hubId,
					to.sessionRef,
				);
			this.remove(from);
		});
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
		this.db.runSync("DELETE FROM draft_transfer_log WHERE from_hub_id = ? OR to_hub_id = ?", hubId, hubId);
		this.db.runSync("DELETE FROM draft_image_sets WHERE hub_id = ?", hubId);
		this.db.runSync("DELETE FROM draft_images WHERE hub_id = ?", hubId);
		this.db.runSync("DELETE FROM drafts WHERE hub_id = ?", hubId);
		this.db.runSync("DELETE FROM question_drafts WHERE hub_id = ?", hubId);
		this.db.runSync("DELETE FROM question_positions WHERE hub_id = ?", hubId);
	}

	private mapTransfer(row: TransferRow): DraftTransfer {
		return {
			transferId: row.transfer_id,
			from: { hubId: row.from_hub_id, sessionRef: row.from_ref },
			to: { hubId: row.to_hub_id, sessionRef: row.to_ref },
			state: row.state as DraftTransferState,
			deliveredHash: row.delivered_hash,
			carriedCaptureToken: row.carried_capture_token,
			carriedGeneration: row.carried_generation,
			completedCommandRecoveryIds: decodeIdList(row.completed_command_recovery_ids),
			releasedRecoveryIds: decodeIdList(row.released_recovery_ids),
			createdAt: row.created_at,
		};
	}
}

interface TransferRow {
	transfer_id: string;
	from_hub_id: string;
	from_ref: string;
	to_hub_id: string;
	to_ref: string;
	state: string;
	delivered_hash: string | null;
	carried_capture_token: string | null;
	carried_generation: string | null;
	completed_command_recovery_ids: string | null;
	released_recovery_ids: string | null;
	created_at: number;
}

function decodeIdList(raw: string | null): string[] {
	if (raw === null) return [];
	try {
		const parsed: unknown = JSON.parse(raw);
		return Array.isArray(parsed) ? parsed.filter((id): id is string => typeof id === "string") : [];
	} catch {
		return [];
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
