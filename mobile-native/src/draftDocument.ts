import {
	insertMarker,
	markerText,
	MAX_ATTACHMENTS,
	mergeComposerCompositions,
	nextComposerImageMarker,
	reconcileComposerMentions,
	recoverMutationComposition,
	stripMarker,
} from "@evener/appwire-client";
import * as Crypto from "expo-crypto";
import type { InputAttachment } from "@evener/appwire-client";
import type { MutationRecoveryRecord } from "@evener/appwire-client/state/mutation";
import { type ConversationMutationContext, MutationNotSentError } from "../../mobile/src/state/conversationMutation";
import type { DraftImage, DraftImageData } from "./draftImages";
import { restoreUnconfirmedDraft } from "./draftRecovery";
import type {
	DraftComposerMetadata,
	DraftDestination,
	DraftParkedComposition,
	DraftRecord,
	DraftRepository,
} from "./draftRepository";
import type { RecoveryResendCommit } from "./mutationOutboxStorage";

function sameSources(owned: readonly string[] | undefined, supplied: readonly string[]): boolean {
	return (
		!!owned?.length &&
		owned.length === supplied.length &&
		new Set(supplied).size === supplied.length &&
		owned.every((id) => supplied.includes(id))
	);
}

function occupiedMetadata(metadata?: DraftComposerMetadata): boolean {
	return !!(metadata?.skillNames?.length || metadata?.commandNames?.length || metadata?.recoveryIds?.length);
}

function reconcileEditedComposer(
	record: DraftRecord,
	text: string,
	mentions: DraftComposerMetadata["mentions"],
): DraftComposerMetadata | undefined {
	const composer = record.composer;
	if (composer?.mentions === undefined) return composer;
	const retains = (kind: "skill" | "command", name: string) => {
		const hadLabel = composer.mentions?.some((mention) => mention.kind === kind && mention.name === name);
		return !hadLabel || !!mentions?.some((mention) => mention.kind === kind && mention.name === name);
	};
	return {
		...composer,
		mentions,
		skillNames: composer.skillNames?.filter((name) => retains("skill", name)),
		commandNames: composer.commandNames?.filter((name) => retains("command", name)),
	};
}

function releasedIds(record: DraftRecord): string[] {
	const legacy = (record.composer as (DraftComposerMetadata & { releasedRecoveryIds?: string[] }) | undefined)
		?.releasedRecoveryIds;
	return [...new Set([...(record.releasedRecoveryIds ?? []), ...(legacy ?? [])])];
}

/** An empty exclusion set is absent state, not a record field. */
function withReleasedRecoveryIds(record: DraftRecord, ids: string[]): DraftRecord {
	if (ids.length) return { ...record, releasedRecoveryIds: ids };
	if (record.releasedRecoveryIds === undefined) return record;
	const { releasedRecoveryIds: _omitted, ...withoutIds } = record;
	return withoutIds;
}

/** An empty park list is absent state, not a record field. */
function withParkedCompositions(record: DraftRecord, parks: DraftRecord["parkedCompositions"]): DraftRecord {
	if (parks?.length) return { ...record, parkedCompositions: parks };
	if (record.parkedCompositions === undefined) return record;
	const { parkedCompositions: _omitted, ...withoutParks } = record;
	return withoutParks;
}

function releaseEmptyComposerOwnership(record: DraftRecord): DraftRecord {
	const releasedRecoveryIds = releasedIds(record);
	const base = withReleasedRecoveryIds(record, releasedRecoveryIds);
	const composer = base.composer;
	const { releasedRecoveryIds: _legacy, ...composerMetadata } =
		(composer as (DraftComposerMetadata & { releasedRecoveryIds?: string[] }) | undefined) ?? {};
	const migrated =
		releasedRecoveryIds.length && base.releasedRecoveryIds === undefined ? { ...base, releasedRecoveryIds } : base;
	if (!composer) return migrated;
	const parkedIds = new Set(base.unconfirmedComposer?.recoveryIds ?? []);
	const claimedIds = (composer?.recoveryIds ?? []).filter((id) => !parkedIds.has(id));
	if (
		base.draft !== "" ||
		(base.images?.length ?? 0) > 0 ||
		composer?.skillNames?.length ||
		composer?.commandNames?.length ||
		!claimedIds.length
	)
		return migrated;
	return {
		...migrated,
		composer: {
			...composerMetadata,
			recoveryIds: composer.recoveryIds?.filter((id) => !claimedIds.includes(id)) ?? [],
		},
		releasedRecoveryIds: [...new Set([...releasedRecoveryIds, ...claimedIds])],
	};
}

function composition(text: string, images: DraftImage[] = [], metadata?: DraftComposerMetadata) {
	return {
		text,
		attachments: images,
		skillNames: metadata?.skillNames ?? [],
		commandNames: metadata?.commandNames ?? [],
		...(metadata?.mentions === undefined ? {} : { mentions: metadata.mentions }),
	};
}

function parkedRecoveryIds(parks: readonly DraftParkedComposition[] | undefined): string[] {
	return (parks ?? []).flatMap((parked) => parked.composer?.recoveryIds ?? []);
}

interface DraftSnapshot {
	record: DraftRecord;
	loaded: boolean;
	submitting: boolean;
	error: string | null;
}

/** Why the composer can't take a recovered message back right now. */
export type RecoveredRestoreBlock = "unavailable" | "loading" | "sending" | "unsaved" | "image" | "occupied";

const RECOVERED_RESTORE_HINTS: Record<RecoveredRestoreBlock, string> = {
	unavailable: "The draft is unavailable on this device.",
	loading: "Wait for the draft to load to restore this message.",
	sending: "Wait for the current draft to finish sending to restore this message.",
	unsaved: "Retry saving the draft to restore this message.",
	image: "Remove the draft's image to restore this message.",
	occupied: "Clear or send your current draft to restore this message.",
};

/** Composition survives the lifetime of a socket and a conversation store. */
export class DraftDocument {
	private snapshot: DraftSnapshot = {
		record: { draft: "", unconfirmed: null },
		loaded: false,
		submitting: false,
		error: null,
	};
	private listeners = new Set<() => void>();
	private forgotten = false;
	/** Retired after a handoff: ordinary edits must not recreate the source
	 * draft, while an already pending submission still settles onward. */
	private retired = false;
	private unsavedImages = new Map<string, DraftImageData>();
	// A resumed identity's document that this retired one settles through. A
	// submission already in flight outlives its screen, and its completion must
	// clear the capture at the destination that now owns the record.
	private successor: DraftDocument | undefined;

	imagePreviews(images: DraftImage[] = this.snapshot.record.images ?? []): InputAttachment[] {
		return images.map((image) => {
			const pending = this.unsavedImages.get(image.id);
			if (pending) return pending;
			const input = this.repository().imageInputs(this.destination, [image])[0];
			if (!input) throw new Error("Saved image is unavailable.");
			return input;
		});
	}

	constructor(
		private repository: () => Pick<DraftRepository, "read" | "write" | "imageInputs">,
		private destination: DraftDestination,
		/** Fresh identities for re-issued attachments. The default is unique
		 * across app restarts; production passes the app's own randomUUID. */
		private createImageId: () => string = () =>
			`reissue-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`,
		private createCaptureToken: () => string = Crypto.randomUUID,
		private createParkId: () => string = () =>
			`park-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`,
	) {
		this.retry();
	}

	get targetKey(): string {
		return JSON.stringify([this.destination.hubId, this.destination.sessionRef]);
	}

	getSnapshot = (): DraftSnapshot => this.snapshot;
	subscribe = (listener: () => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};
	private update(update: Partial<DraftSnapshot>) {
		this.snapshot = { ...this.snapshot, ...update };
		for (const listener of this.listeners) listener();
	}
	private persist(record: DraftRecord, retainOnFailure: boolean) {
		if (this.forgotten || this.retired) return;
		const persisted = releaseEmptyComposerOwnership(withParkedCompositions(record, record.parkedCompositions));
		try {
			const referenced = new Set(
				[
					...(persisted.images ?? []),
					...(persisted.unconfirmedImages ?? []),
					...(persisted.parkedCompositions ?? []).flatMap((parked) => parked.images ?? []),
				].map((image) => image.id),
			);
			this.repository().write(
				this.destination,
				persisted,
				[...this.unsavedImages.values()].filter((image) => referenced.has(image.id)),
			);
			this.unsavedImages.clear();
			this.update({ record: persisted, error: null });
		} catch (error) {
			this.update({
				...(retainOnFailure ? { record: this.failedRecord(record, persisted) } : {}),
				error: "Your latest draft could not be saved on this device. Keep this screen open and retry saving.",
			});
			throw error;
		}
	}
	/** A failed save has not released anything durably. Put back any ownership
	 * that releaseEmptyComposerOwnership removed, so the screen keeps hiding
	 * Discard for sources the saved draft still references. */
	private failedRecord(previous: DraftRecord, attempted: DraftRecord): DraftRecord {
		const previousOwned = previous.composer?.recoveryIds ?? [];
		const attemptedOwned = attempted.composer?.recoveryIds ?? [];
		const parked = new Set(attempted.unconfirmedComposer?.recoveryIds ?? []);
		const dropped = previousOwned.filter((id) => !attemptedOwned.includes(id) && !parked.has(id));
		if (!dropped.length) return attempted;
		return {
			...attempted,
			composer: {
				...previous.composer,
				...attempted.composer,
				recoveryIds: [...attemptedOwned, ...dropped],
			},
			releasedRecoveryIds: releasedIds(attempted).filter((id) => !dropped.includes(id)),
		};
	}
	retry = () => {
		if (this.forgotten || this.retired) return;
		try {
			if (this.snapshot.loaded) this.persist(this.snapshot.record, true);
			else {
				const record = this.repository().read(this.destination);
				this.update({ record, loaded: true, error: null });
				if (releasedIds(record).length && record.releasedRecoveryIds === undefined) this.persist(record, true);
			}
		} catch {
			if (!this.snapshot.loaded)
				this.update({
					error: "Your saved draft could not be loaded. Retry before composing.",
				});
		}
	};
	forget() {
		this.forgotten = true;
		this.retired = true;
		this.successor = undefined;
		this.unsavedImages.clear();
		this.update({
			record: { draft: "", unconfirmed: null },
			loaded: false,
			error: null,
		});
	}

	/** Adopt a record another destination already persisted for this document.
	 * The live snapshot replaces an empty one immediately, so an already-open
	 * screen renders the transferred composition without reopening storage. */
	adopt(record: DraftRecord): void {
		this.unsavedImages.clear();
		this.update({ record, loaded: true, submitting: false, error: null });
	}

	/** Retire this document after a handoff. Ordinary edits stop here, while any
	 * later settlement for its in-flight submission belongs to the document that
	 * inherited the record. */
	retireInto(successor: DraftDocument): void {
		if (successor === this) return;
		this.successor = successor;
		this.retired = true;
		this.unsavedImages.clear();
	}

	/** Keep recovered sources out of automatic restore while leaving them
	 * manually recoverable. A refused handoff uses this so migrated editing rows
	 * cannot append their stale original composition to an occupied draft. */
	releaseRecoverySources(ids: readonly string[]): boolean {
		if (this.forgotten || this.retired || !ids.length) return false;
		const current = this.snapshot.record;
		const released = [...new Set([...(current.releasedRecoveryIds ?? []), ...ids])];
		if (released.length === (current.releasedRecoveryIds?.length ?? 0)) return true;
		try {
			this.persist({ ...current, releasedRecoveryIds: released }, true);
			return true;
		} catch {
			return false;
		}
	}
	edit(text: string) {
		if (!this.snapshot.loaded || this.retired) return;
		const record = this.snapshot.record;
		const mentions =
			record.composer?.mentions === undefined
				? undefined
				: reconcileComposerMentions(record.draft, text, record.composer.mentions);
		const composer = reconcileEditedComposer(record, text, mentions);
		try {
			this.persist(
				{
					...record,
					draft: text,
					...(composer === undefined ? {} : { composer }),
				},
				true,
			);
		} catch {
			/* The snapshot retains unsaved input and its error. */
		}
	}

	replaceDraft(text: string) {
		if (!this.snapshot.loaded || this.retired || this.snapshot.submitting || this.snapshot.record.unconfirmed !== null)
			return;
		const record = this.snapshot.record;
		const owned = !!record.composer?.recoveryIds?.length;
		try {
			// A replacement is not an edit of the recovered message. Park the old
			// composition in its own record so reconciliation cannot append it to
			// the replacement, and keep its sources owned by the park until the
			// user explicitly restores it.
			this.persist(
				{
					...record,
					draft: text,
					images: undefined,
					composer: undefined,
					releasedRecoveryIds: releasedIds(record),
					parkedCompositions: owned
						? [
								...(record.parkedCompositions ?? []),
								{
									id: this.createParkId(),
									text: record.draft,
									images: record.images,
									composer: record.composer,
								},
							]
						: record.parkedCompositions,
				},
				true,
			);
		} catch {
			/* Keep the replacement visible with an explicit save retry. */
		}
	}

	appendEditedRecovery(source: MutationRecoveryRecord): "restored" | "already-restored" | "blocked" {
		const { record, loaded, submitting, error } = this.snapshot;
		if (this.forgotten || this.retired || !loaded || submitting || error || !this.isEditedSource(source))
			return "blocked";
		if (record.completedCommandRecoveryIds?.includes(source.clientMutationId)) return "already-restored";
		if (
			[...(record.composer?.recoveryIds ?? []), ...(record.unconfirmedComposer?.recoveryIds ?? [])].includes(
				source.clientMutationId,
			) ||
			parkedRecoveryIds(record.parkedCompositions).includes(source.clientMutationId)
		)
			return "already-restored";
		const recovered = recoverMutationComposition(source);
		if ((record.images?.length ?? 0) + recovered.attachments.length > MAX_ATTACHMENTS) return "blocked";
		const additions = recovered.attachments.map((image) => ({ ...image, id: this.createImageId() }));
		const merged = mergeComposerCompositions(composition(record.draft, record.images, record.composer), {
			...recovered,
			attachments: additions.map(({ data: _data, ...image }) => image),
		});
		for (const image of additions) this.unsavedImages.set(image.id, image);
		try {
			this.persist(
				{
					...record,
					draft: merged.text,
					images: merged.attachments,
					composer: {
						skillNames: merged.skillNames,
						commandNames: merged.commandNames,
						mentions: merged.mentions,
						recoveryIds: [...(record.composer?.recoveryIds ?? []), source.clientMutationId],
					},
					releasedRecoveryIds: releasedIds(record).filter((id) => id !== source.clientMutationId),
				},
				false,
			);
			return "restored";
		} catch {
			for (const image of additions) this.unsavedImages.delete(image.id);
			return "blocked";
		}
	}

	private isEditedSource(source: MutationRecoveryRecord): boolean {
		return source.recoveryKind === "editing" && source.targetRef === this.targetKey;
	}

	addImage(image: DraftImageData, cursor?: number) {
		if (!this.snapshot.loaded || this.retired) return;
		const { data: _data, ...reference } = image;
		const record = this.snapshot.record;
		const images = record.images ?? [];
		if (
			images.length >= MAX_ATTACHMENTS ||
			[...images, ...(record.unconfirmedImages ?? [])].some((item) => item.id === image.id)
		)
			throw new Error("This image cannot be added to the draft.");
		const attachments = [...images, ...(record.unconfirmedImages ?? [])];
		const marker =
			attachments.some((item) => item.marker === image.marker) || record.draft.includes(markerText(image.marker))
				? nextComposerImageMarker(record.draft, attachments, image.marker)
				: image.marker;
		const position = Math.max(0, Math.min(cursor ?? record.draft.length, record.draft.length));
		const markerValue = markerText(marker);
		const text = insertMarker(record.draft, position, position, markerValue).value;
		// The insertion can land inside a selected label. Reconcile against the
		// resulting text so a split label loses its mention and canonical
		// selection, while a surviving label keeps its shifted offset.
		const composer = reconcileEditedComposer(
			record,
			text,
			record.composer?.mentions === undefined
				? undefined
				: reconcileComposerMentions(record.draft, text, record.composer.mentions),
		);
		this.unsavedImages.set(image.id, { ...image, marker });
		try {
			this.persist(
				{
					...record,
					draft: text,
					images: [...(record.images ?? []), { ...reference, marker }],
					...(composer === undefined ? {} : { composer }),
				},
				true,
			);
		} catch {
			/* Retain the image bytes and reference for explicit save retry. */
		}
	}

	removeImage(id: string) {
		if (!this.snapshot.loaded || this.retired) return;
		const record = this.snapshot.record;
		const image = record.images?.find((item) => item.id === id);
		if (!image) return;
		const images = record.images?.filter((item) => item.id !== id);
		const text = stripMarker(record.draft, undefined, image.marker).value;
		// Offsets stay in the pre-removal coordinates here: reconciliation maps
		// each surviving mention through the removed marker exactly once.
		const composer = reconcileEditedComposer(
			record,
			text,
			record.composer?.mentions === undefined
				? undefined
				: reconcileComposerMentions(record.draft, text, record.composer.mentions),
		);
		try {
			this.persist(
				{
					...record,
					draft: text,
					images: images?.length ? images : undefined,
					...(composer === undefined ? {} : { composer }),
				},
				true,
			);
		} catch {
			/* The visible removal can be retried without deleting other images. */
		}
	}
	async submit(
		operation: (text: string, images: InputAttachment[], context: ConversationMutationContext) => Promise<boolean>,
	) {
		return this.submitContent(this.snapshot.record.draft, false, operation);
	}
	/** Explicit non-message command path. A true result here is command success,
	 * unlike submit(), whose true result is never proof of message admission. */
	async submitBuiltinCommand(
		operation: (text: string, images: InputAttachment[], context: ConversationMutationContext) => Promise<boolean>,
	) {
		return this.submitContent(this.snapshot.record.draft, false, operation, undefined, true);
	}
	/** Submit composer text without consuming the draft. `reissued` carries the
	 * attachments of an input being re-sent (a Retry): they travel with the text
	 * and the guard counts them, so an image-only re-issue can send. */
	async submitText(
		text: string,
		operation: (text: string, images: InputAttachment[], context: ConversationMutationContext) => Promise<boolean>,
		reissued?: InputAttachment[],
	) {
		return this.submitContent(text, true, operation, reissued);
	}
	private async submitContent(
		text: string,
		preserveDraft: boolean,
		operation: (text: string, images: InputAttachment[], context: ConversationMutationContext) => Promise<boolean>,
		reissued?: InputAttachment[],
		builtinCommand = false,
	): Promise<boolean | "not-sent" | undefined> {
		const { record, loaded, submitting, error } = this.snapshot;
		const images = preserveDraft ? [] : (record.images ?? []);
		const outbound = reissued ?? images;
		const metadata = preserveDraft ? undefined : record.composer;
		if (
			!loaded ||
			this.forgotten ||
			this.retired ||
			submitting ||
			error ||
			record.unconfirmed !== null ||
			(!text.trim() && outbound.length === 0 && !metadata?.skillNames?.length && !metadata?.commandNames?.length)
		)
			return;
		// A re-issued input (a Retry) carries its images as wire attachments:
		// register them as pending image data so the uncertain record keeps
		// both the references and the bytes, and recovery restores the whole
		// input instead of text with orphaned markers.
		const reissuedRecords: DraftImage[] = [];
		const draftCaptureToken = this.createCaptureToken();
		if (reissued) {
			for (const attachment of reissued) {
				const data: DraftImageData = {
					id: this.createImageId(),
					marker: attachment.marker,
					mediaType: attachment.mediaType,
					...(attachment.name === undefined ? {} : { name: attachment.name }),
					data: attachment.data,
				};
				this.unsavedImages.set(data.id, data);
				reissuedRecords.push(data);
			}
		}
		// Persist before invoking any transport operation. A crash after this point
		// can only establish uncertainty, never that it is safe to replay the input.
		const inputImages = reissued ?? this.repository().imageInputs(this.destination, images);
		this.persist(
			{
				...record,
				draft: preserveDraft ? record.draft : "",
				unconfirmed: text,
				images: preserveDraft ? record.images : undefined,
				composer: preserveDraft ? record.composer : undefined,
				unconfirmedComposer: metadata,
				unconfirmedCaptureToken: draftCaptureToken,
				unconfirmedImages: outbound.length ? [...reissuedRecords, ...images] : undefined,
				releasedRecoveryIds: builtinCommand
					? releasedIds(record).filter((id) => !metadata?.recoveryIds?.includes(id))
					: releasedIds(record),
			},
			false,
		);
		this.update({ submitting: true });
		try {
			const context: ConversationMutationContext = {
				composer: {
					text,
					attachments: inputImages,
					...(metadata?.skillNames === undefined ? {} : { skillNames: [...metadata.skillNames] }),
					...(metadata?.commandNames === undefined ? {} : { commandNames: [...metadata.commandNames] }),
					...(metadata?.mentions === undefined
						? {}
						: { mentions: metadata.mentions.map((mention) => ({ ...mention })) }),
				},
				...(metadata?.recoveryIds?.length ? { recoverySourceIds: [...metadata.recoveryIds] } : {}),
				draftCaptureToken,
			};
			if (!(await operation(text, inputImages, context))) return false;
			if (builtinCommand) {
				const current = this.snapshot.record;
				if (
					current.unconfirmed === text &&
					current.unconfirmedCaptureToken === draftCaptureToken &&
					(!metadata?.recoveryIds?.length ||
						sameSources(current.unconfirmedComposer?.recoveryIds, metadata.recoveryIds))
				) {
					const ids = metadata?.recoveryIds ?? [];
					this.completeSubmission(
						{
							...current,
							unconfirmed: null,
							unconfirmedImages: undefined,
							unconfirmedComposer: undefined,
							unconfirmedCaptureToken: undefined,
							completedCommandRecoveryIds: [...new Set([...(current.completedCommandRecoveryIds ?? []), ...ids])],
							releasedRecoveryIds: releasedIds(current).filter((id) => !ids.includes(id)),
						},
						draftCaptureToken,
					);
				}
			} else if (!metadata?.recoveryIds?.length && this.snapshot.record.unconfirmedCaptureToken === draftCaptureToken)
				this.completeSubmission(
					{
						...this.snapshot.record,
						unconfirmed: null,
						unconfirmedImages: undefined,
						unconfirmedComposer: undefined,
						unconfirmedCaptureToken: undefined,
					},
					draftCaptureToken,
				);
			return true;
		} catch (error) {
			// A proven local refusal made no durable row and no request. Clear only
			// this operation's uncertainty marker; the live draft, selections and
			// images stay, and no unconfirmed ghost is left behind. Ordinary throws
			// keep their existing uncertain recovery. Route through the handoff
			// chain so a submission that outlived a retirement clears the capture
			// where the record now lives.
			if (!(error instanceof MutationNotSentError)) throw error;
			if (this.snapshot.record.unconfirmedCaptureToken === draftCaptureToken)
				this.completeSubmission(
					{
						...this.snapshot.record,
						unconfirmed: null,
						unconfirmedImages: undefined,
						unconfirmedComposer: undefined,
						unconfirmedCaptureToken: undefined,
					},
					draftCaptureToken,
				);
			return "not-sent";
		} finally {
			this.update({ submitting: false });
		}
	}

	/** Route a completed submission to the document that inherited the record
	 * during a handoff. Only the capture this submission owns is cleared; the
	 * successor's current draft, images, selections and newer capture stay. */
	private completeSubmission(record: DraftRecord, captureToken: string): void {
		if (this.successor) this.successor.forwardCompletion(record, captureToken);
		else this.persist(record, true);
	}

	/** Follow the whole handoff chain: A's completion must reach the document
	 * that currently owns the record, not an intermediate retired destination. */
	private forwardCompletion(record: DraftRecord, captureToken: string): void {
		if (this.successor) this.successor.forwardCompletion(record, captureToken);
		else this.acceptSubmissionCompletion(record, captureToken);
	}

	private acceptSubmissionCompletion(settled: DraftRecord, captureToken: string): void {
		const current = this.snapshot.record;
		const next: DraftRecord = { ...current };
		if (current.unconfirmedCaptureToken === captureToken) {
			next.unconfirmed = null;
			next.unconfirmedImages = undefined;
			next.unconfirmedComposer = undefined;
			next.unconfirmedCaptureToken = undefined;
		}
		const completed = settled.completedCommandRecoveryIds ?? [];
		if (completed.length)
			next.completedCommandRecoveryIds = [...new Set([...(current.completedCommandRecoveryIds ?? []), ...completed])];
		const released = settled.releasedRecoveryIds ?? [];
		if (released.length) next.releasedRecoveryIds = [...new Set([...(current.releasedRecoveryIds ?? []), ...released])];
		try {
			this.persist(next, true);
		} catch {
			/* The destination keeps its work; the composer's Retry owns the failure. */
		}
	}

	/** Clear only the unconfirmed composition whose exact submission token a
	 * durable mutation row proved it owns. Other captures and current draft work
	 * remain untouched. A failed local write must stop the caller's action. */
	clearMatchingUnconfirmedCapture(token: string): boolean {
		if (this.retired) return true;
		let durable: DraftRecord;
		try {
			durable = this.repository().read(this.destination);
		} catch {
			return false;
		}
		if (durable.unconfirmed === null || durable.unconfirmedCaptureToken !== token) return true;

		const current = this.snapshot.record;
		const record =
			current.unconfirmed === null || current.unconfirmedCaptureToken === token
				? {
						...current,
						unconfirmed: null,
						unconfirmedImages: undefined,
						unconfirmedComposer: undefined,
						unconfirmedCaptureToken: undefined,
					}
				: current;
		try {
			this.persist(record, false);
			return true;
		} catch {
			return false;
		}
	}

	/** Remove only IDs whose exact-target deletion attempts completed. */
	finishCompletedCommandRecovery(ids: readonly string[]): boolean {
		const { record, loaded, error } = this.snapshot;
		if (this.forgotten || this.retired || !loaded || error) return false;
		const remaining = (record.completedCommandRecoveryIds ?? []).filter((id) => !ids.includes(id));
		if (remaining.length === (record.completedCommandRecoveryIds ?? []).length) return true;
		try {
			this.persist(
				{
					...record,
					completedCommandRecoveryIds: remaining.length ? remaining : undefined,
					releasedRecoveryIds: releasedIds(record).filter((id) => !ids.includes(id)),
				},
				false,
			);
			return true;
		} catch {
			return false;
		}
	}

	/** A runtime receipt, not transport success or an absent outbox row, releases ownership. */
	settleEditedResend(commit: RecoveryResendCommit): boolean {
		const { record, loaded, error } = this.snapshot;
		if (
			this.forgotten ||
			this.retired ||
			!loaded ||
			error ||
			commit.targetRef !== this.targetKey ||
			record.unconfirmed === null ||
			!sameSources(record.unconfirmedComposer?.recoveryIds, commit.sourceIds)
		)
			return false;
		try {
			this.persist(
				{
					...record,
					unconfirmed: null,
					unconfirmedImages: undefined,
					unconfirmedComposer: undefined,
					unconfirmedCaptureToken: undefined,
					recoveryResendAcks: [...new Set([...(record.recoveryResendAcks ?? []), commit.clientMutationId])],
				},
				false,
			);
			return true;
		} catch {
			return false;
		}
	}

	/** Call only after runtime acknowledgment succeeds. Failure retains the durable retry marker. */
	finishRecoveryResendAck(clientMutationId: string): boolean {
		const { record, loaded, error } = this.snapshot;
		if (this.forgotten || this.retired || !loaded || error) return false;
		if (!record.recoveryResendAcks?.includes(clientMutationId)) return true;
		const remaining = record.recoveryResendAcks.filter((id) => id !== clientMutationId);
		try {
			this.persist({ ...record, recoveryResendAcks: remaining.length ? remaining : undefined }, false);
			return true;
		} catch {
			return false;
		}
	}

	/** The coordinator must freshly read ALL retained editing rows before calling.
	 * Missing outbox rows are not proof. Restore our captured revision, not the
	 * older source payload, and never submit here. */
	restoreEditedRecovery(retainedSources: readonly MutationRecoveryRecord[]): boolean {
		const { record, loaded, submitting, error } = this.snapshot;
		if (
			this.forgotten ||
			this.retired ||
			!loaded ||
			submitting ||
			error ||
			record.unconfirmed === null ||
			!sameSources(
				record.unconfirmedComposer?.recoveryIds,
				retainedSources.map((source) => source.clientMutationId),
			) ||
			!retainedSources.every((source) => this.isEditedSource(source)) ||
			(record.images?.length ?? 0) + (record.unconfirmedImages?.length ?? 0) > MAX_ATTACHMENTS
		)
			return false;
		const merged = mergeComposerCompositions(composition(record.draft, record.images, record.composer), {
			...composition(record.unconfirmed, record.unconfirmedImages, record.unconfirmedComposer),
			// Legacy append already assigned disjoint markers without changing prose.
			imageReferences: "recorded",
		});
		try {
			this.persist(
				{
					...record,
					draft: merged.text,
					images: merged.attachments,
					composer: {
						skillNames: merged.skillNames,
						commandNames: merged.commandNames,
						mentions: merged.mentions,
						recoveryIds: [...(record.composer?.recoveryIds ?? []), ...(record.unconfirmedComposer?.recoveryIds ?? [])],
					},
					releasedRecoveryIds: releasedIds(record).filter(
						(id) => !record.unconfirmedComposer?.recoveryIds?.includes(id),
					),
					unconfirmed: null,
					unconfirmedImages: undefined,
					unconfirmedComposer: undefined,
					unconfirmedCaptureToken: undefined,
				},
				false,
			);
			return true;
		} catch {
			return false;
		}
	}

	dismiss() {
		if (
			!this.snapshot.loaded ||
			this.retired ||
			this.snapshot.submitting ||
			this.snapshot.record.unconfirmedComposer?.recoveryIds?.length
		)
			return;
		try {
			this.persist(
				{
					...this.snapshot.record,
					unconfirmed: null,
					unconfirmedImages: undefined,
					unconfirmedComposer: undefined,
					unconfirmedCaptureToken: undefined,
				},
				true,
			);
		} catch {
			/* Retry retains the requested recovery state. */
		}
	}
	/** Discard only the selected park, leaving other parks and any unconfirmed send. */
	dismissParked(id: string) {
		const { record, loaded, submitting } = this.snapshot;
		const parked = record.parkedCompositions?.find((candidate) => candidate.id === id);
		if (!loaded || submitting || parked === undefined) return;
		const parkedIds = parked.composer?.recoveryIds ?? [];
		try {
			this.persist(
				{
					...record,
					parkedCompositions: record.parkedCompositions?.filter((candidate) => candidate.id !== id),
					releasedRecoveryIds: [...new Set([...releasedIds(record), ...parkedIds])],
				},
				true,
			);
		} catch {
			// A failed save changed nothing durably: keep the prior park and
			// its source ownership visible so those sources stay protected.
			this.update({ record });
		}
	}
	/** Restore only the selected park, leaving other parks and any unconfirmed send. */
	restoreParked(id: string) {
		const { record, loaded, submitting } = this.snapshot;
		const parked = record.parkedCompositions?.find((candidate) => candidate.id === id);
		if (!loaded || submitting || parked === undefined) return;
		if (record.images?.length || occupiedMetadata(record.composer)) return;
		const text = restoreUnconfirmedDraft(record.draft, parked.text);
		if (text === null) return;
		try {
			this.persist(
				{
					...record,
					draft: text,
					images: parked.images,
					composer: parked.composer,
					parkedCompositions: record.parkedCompositions?.filter((candidate) => candidate.id !== id),
				},
				true,
			);
		} catch {
			/* Unsaved recovered text stays visible. */
		}
	}
	restore() {
		const { record, loaded, submitting } = this.snapshot;
		if (!loaded || this.retired || submitting) return;
		if (record.unconfirmed === null) return;
		if (record.images?.length || occupiedMetadata(record.composer) || record.unconfirmedComposer?.recoveryIds?.length)
			return;
		const text = restoreUnconfirmedDraft(record.draft, record.unconfirmed);
		if (text === null) return;
		try {
			this.persist(
				{
					...record,
					draft: text,
					unconfirmed: null,
					images: record.unconfirmedImages,
					composer: record.unconfirmedComposer,
					unconfirmedImages: undefined,
					unconfirmedComposer: undefined,
					unconfirmedCaptureToken: undefined,
				},
				true,
			);
		} catch {
			/* Unsaved recovered text stays visible. */
		}
	}

	// Whether the composer can accept a recovered restore right now. A recovery
	// surface reads this before offering the action, so a restore is disabled
	// with an explanation instead of silently doing nothing when the composer
	// already holds a draft or an image.
	canRestoreRecoveredDraft(): boolean {
		const { record, loaded, submitting, error } = this.snapshot;
		return (
			!this.forgotten &&
			!this.retired &&
			loaded &&
			!submitting &&
			error === null &&
			record.draft === "" &&
			!occupiedMetadata(record.composer) &&
			(record.images?.length ?? 0) === 0
		);
	}

	// Why a restore is currently blocked, or null when the composer can accept
	// one. It tells an occupied composer apart from a draft that is still
	// loading/sending or whose last save failed, so a disabled restore never
	// tells the user to clear a draft that isn't there.
	recoveredRestoreBlock(): RecoveredRestoreBlock | null {
		const { record, loaded, submitting, error } = this.snapshot;
		if (this.forgotten || this.retired) return "unavailable";
		if (!loaded) return "loading";
		if (submitting) return "sending";
		if (error !== null) return "unsaved";
		if ((record.images?.length ?? 0) > 0) return "image";
		if (record.draft !== "" || occupiedMetadata(record.composer)) return "occupied";
		return null;
	}

	// recoveredRestoreBlock, in the user's terms.
	recoveredRestoreHint(): string | null {
		const block = this.recoveredRestoreBlock();
		return block === null ? null : RECOVERED_RESTORE_HINTS[block];
	}

	// Restores a rejected mutation's recovered text into the composer in one
	// savepointed repository write (DraftRepository.write), so a failed restore
	// can never leave the draft half-written. It refuses to clobber a composer
	// that already holds a draft or an image, and preserves an uncertain
	// submission's own unconfirmed text and images while it restores. Returns
	// whether the text was written; a failed write leaves the stored draft
	// untouched and surfaces through the snapshot's error.
	restoreRecoveredDraft(text: string): boolean {
		if (text.length === 0 || !this.canRestoreRecoveredDraft()) return false;
		const { record } = this.snapshot;
		try {
			this.persist(
				{
					...record,
					draft: text,
				},
				false,
			);
			return true;
		} catch {
			return false;
		}
	}
}
