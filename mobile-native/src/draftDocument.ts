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
import { type ConversationMutationContext, MutationNotSentError } from "./conversation/state/conversationMutation";
import type { DraftImage, DraftImageData } from "./draftImages";
import { restoreUnconfirmedDraft } from "./draftRecovery";
import { clearsCapture } from "./draftHandoffLedger";
import type {
	DraftComposerMetadata,
	DraftDestination,
	DraftParkedComposition,
	DraftRecord,
	DraftRepository,
} from "./draftRepository";
import type { DraftHandoffLedger } from "./draftHandoffLedger";
import { captureTerminal } from "./draftHandoffGraph";
import type { RecoveryResendCommit } from "./mutationOutboxStorage";

/** A resolved submission's completion: the capture it clears and the ids it
 * delivered at the identity that owns the record now. */
export interface DraftCompletionDelivery {
	captureToken: string;
	/** The obligation's durable resolution; `not-sent` is a proven local
	 * refusal that consumes its capture like a completion does. */
	state: "completed" | "not-sent";
	completedCommandIds: readonly string[];
	releasedRecoveryIds: readonly string[];
}

/** The outcome of delivering a completion at a terminal identity. */
export type CompletionDeliveryOutcome =
	/** The completion landed durably, or its capture settles through the
	 * resend receipt the receipt-acknowledging coordinator owns. */
	| "delivered"
	/** A live document at the terminal owns the completion through its own
	 * save path: the persisted record must not be written behind the unsaved
	 * images and edits it holds. The completion is retried with that document. */
	| "owned-live"
	/** Nothing at this terminal owns the capture; the caller may keep looking. */
	| "unowned";

/** Capture tokens of submissions this process started and has not yet
 * resolved durably. The set is process state, never durable: a process that
 * dies takes its in-flight submissions with it, so a durable open obligation
 * whose token is not registered here was abandoned by a previous process. */
const inFlightCaptures = new Set<string>();

export function captureInFlight(token: string): boolean {
	return inFlightCaptures.has(token);
}

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

/** Clear a resolved submission's capture and merge the ids it delivered. */
export function mergeCompletion(record: DraftRecord, completion: DraftCompletionDelivery): DraftRecord {
	const next: DraftRecord = {
		...record,
		unconfirmed: null,
		unconfirmedImages: undefined,
		unconfirmedComposer: undefined,
		unconfirmedCaptureToken: undefined,
	};
	if (completion.completedCommandIds.length)
		next.completedCommandRecoveryIds = [
			...new Set([...(record.completedCommandRecoveryIds ?? []), ...completion.completedCommandIds]),
		];
	if (completion.releasedRecoveryIds.length)
		next.releasedRecoveryIds = [...new Set([...(record.releasedRecoveryIds ?? []), ...completion.releasedRecoveryIds])];
	return next;
}

/** A completed message admission, not a built-in command: its capture holds
 * recovered sources whose resend receipt must be acknowledged before the
 * capture clears, so the receipt-acknowledging coordinator owns it and the
 * completion must not clear it here. */
export function messageAdmissionCapture(completion: DraftCompletionDelivery, record: DraftRecord): boolean {
	return (
		completion.state === "completed" &&
		!completion.completedCommandIds.length &&
		(record.unconfirmedComposer?.recoveryIds?.length ?? 0) > 0
	);
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

/** How long a document waits before retrying a ledger write it still owes. */
const OBLIGATION_RETRY_MS = 1000;

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
	/** Obligation resolutions a failing ledger write did not commit. They are
	 * retried on the next submission or document reload in this session; until
	 * then the open obligation blocks settlement, the safe direction. */
	private unresolvedResolutions = new Map<
		string,
		{ state: "completed" | "not-sent" | "failed"; completedCommandIds: string[]; releasedRecoveryIds: string[] }
	>();
	/** Submissions whose obligation could not be recorded before transport.
	 * The send still went out; the begin is retried so a recovered ledger adopts
	 * the pending capture instead of losing the obligation entirely. */
	private unrecordedCaptures = new Map<string, readonly string[]>();
	/** A pending self-retry of the owed ledger writes above, so a transient
	 * store failure recovers without user action. */
	private obligationRetryTimer: ReturnType<typeof setTimeout> | null = null;
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
		/** The durable submission obligations a handoff inherits. A submission
		 * records one before transport and resolves it when it settles, so a
		 * chain can wait for its completion without in-memory forwarding. */
		private submissionLedger?: () => DraftHandoffLedger,
		/** Deliver a resolved submission's completion at the terminal identity.
		 * The library merges it into the live document it holds there, so the
		 * destination's unsaved edits and pending image data survive. */
		private deliverCompletionTo?: (
			terminal: DraftDestination,
			completion: DraftCompletionDelivery,
		) => CompletionDeliveryOutcome,
		/** Called after an obligation durably resolves - completed, not-sent, or
		 * failed - so the library can publish the transition and schedule the
		 * handoff pass the resolution unblocks. */
		private onObligationResolved?: () => void,
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
		// A retired document may still hold resolutions the successor must know
		// about; flush before the retired check so a late retry is not a no-op.
		this.flushObligationResolutions();
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
		this.unsavedImages.clear();
		if (this.obligationRetryTimer !== null) {
			clearTimeout(this.obligationRetryTimer);
			this.obligationRetryTimer = null;
		}
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

	/** Retire this document after a handoff. Ordinary edits stop here; a later
	 * completion reaches the terminal destination through the ledger. */
	retire(): void {
		this.flushObligationResolutions();
		this.retired = true;
		this.unsavedImages.clear();
	}

	/** Merge a delivered completion into this live document. The merge starts
	 * from the live record and persists through the normal save path, so
	 * unsaved edits survive and pending image data is written with the
	 * completion instead of being cleared. False when this document is not the
	 * capture's owner, or the save fails and the completion must be retried. */
	adoptCompletion(completion: DraftCompletionDelivery): boolean {
		const { record, loaded } = this.snapshot;
		if (this.forgotten || this.retired || !loaded) return false;
		if (record.unconfirmedCaptureToken !== completion.captureToken) return false;
		// A recovered message's capture settles through its resend receipt,
		// which the receipt-acknowledging coordinator clears.
		if (messageAdmissionCapture(completion, record)) return true;
		try {
			this.persist(mergeCompletion(record, completion), true);
			return true;
		} catch {
			return false;
		}
	}

	/** The persisted record holds a capture this document's live state has
	 * already consumed or never saw, while a failed save left the document
	 * holding newer state - a restored image, unsaved edits - that the
	 * persisted record cannot see. The completion merges through this
	 * document's own save path, because writing the persisted record behind it
	 * would delete image bytes the live draft still references and make every
	 * later save retry fail. The document owns the capture; the completion
	 * retries here until its save succeeds. */
	mergeCompletionThroughLiveState(completion: DraftCompletionDelivery): CompletionDeliveryOutcome {
		const { record, loaded } = this.snapshot;
		if (this.forgotten || this.retired || !loaded) return "unowned";
		// A different submission owns this document's live capture; the
		// persisted record this completion names cannot be written through it.
		if (record.unconfirmedCaptureToken !== undefined && record.unconfirmedCaptureToken !== completion.captureToken)
			return "unowned";
		let durable: DraftRecord;
		try {
			durable = this.repository().read(this.destination);
		} catch {
			// Ownership cannot be proven either way; never touch the persisted
			// record while this document holds unsaved state.
			return "owned-live";
		}
		if (durable.unconfirmedCaptureToken !== completion.captureToken) return "unowned";
		// A recovered message's capture settles through its resend receipt,
		// which the receipt-acknowledging coordinator clears.
		if (messageAdmissionCapture(completion, record) || messageAdmissionCapture(completion, durable)) return "delivered";
		try {
			this.persist(mergeCompletion(record, completion), true);
			return "delivered";
		} catch {
			return "owned-live";
		}
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
	/** Keep a completed built-in command's sources out of automatic restore and
	 * replay after their rows moved to this destination, even when this
	 * composition refuses the draft itself. */
	adoptCompletedCommandRecovery(ids: readonly string[]): boolean {
		if (this.forgotten || this.retired || !ids.length) return true;
		const current = this.snapshot.record;
		const completed = [...new Set([...(current.completedCommandRecoveryIds ?? []), ...ids])];
		if (completed.length === (current.completedCommandRecoveryIds?.length ?? 0)) return true;
		try {
			this.persist({ ...current, completedCommandRecoveryIds: completed }, true);
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
		this.flushObligationResolutions();
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
			// Durable before any transport call: a handoff edge that inherits this
			// submission must wait for its completion instead of settling without
			// the completion markers. A failure resolves as failed and keeps its
			// original error.
			try {
				this.submissionLedger?.().beginSubmission(draftCaptureToken, this.destination, metadata?.recoveryIds ?? []);
				inFlightCaptures.add(draftCaptureToken);
			} catch {
				// An unavailable obligation store must not stop an ordinary send:
				// the uncertainty capture is already durable, so the input stays
				// recoverable while the message goes out. No handoff can wait for
				// this submission, because the ledger that would carry the
				// obligation is what is failing.
				this.unrecordedCaptures.set(draftCaptureToken, [...(metadata?.recoveryIds ?? [])]);
				this.scheduleObligationRetry();
			}
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
			if (!(await operation(text, inputImages, context))) {
				this.resolveObligation(draftCaptureToken, "failed");
				return false;
			}
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
				} else this.resolveObligation(draftCaptureToken, "completed", metadata?.recoveryIds ?? []);
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
			else this.resolveObligation(draftCaptureToken, "completed");
			return true;
		} catch (error) {
			// A proven local refusal made no durable row and no request. Clear only
			// this operation's uncertainty marker; the live draft, selections and
			// images stay, and no unconfirmed ghost is left behind. Ordinary throws
			// keep their existing uncertain recovery. Route through the handoff
			// chain so a submission that outlived a retirement clears the capture
			// where the record now lives.
			if (!(error instanceof MutationNotSentError)) {
				this.resolveObligation(draftCaptureToken, "failed");
				throw error;
			}
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
					"not-sent",
				);
			else this.resolveObligation(draftCaptureToken, "not-sent");
			return "not-sent";
		} finally {
			this.update({ submitting: false });
		}
	}

	/** Resolve the submission's durable obligation, then clear its capture at the
	 * terminal identity. The ledger, not a document chain, carries the completion. */
	private completeSubmission(
		record: DraftRecord,
		captureToken: string,
		state: "completed" | "not-sent" = "completed",
	): void {
		const resolved = this.resolveObligation(
			captureToken,
			state,
			state === "completed" ? (record.completedCommandRecoveryIds ?? []) : [],
			state === "completed" ? (record.releasedRecoveryIds ?? []) : [],
		);
		const delivered =
			resolved &&
			this.deliverCompletion(
				captureToken,
				state,
				state === "completed" ? (record.completedCommandRecoveryIds ?? []) : [],
				state === "completed" ? (record.releasedRecoveryIds ?? []) : [],
			);
		if (!delivered && !this.retired) this.persist(record, true);
	}

	/** A failed obligation write must not mask the submission's own outcome. The
	 * open obligation blocks handoff settlement until a later attempt resolves it. */
	private resolveObligation(
		captureToken: string,
		state: "completed" | "not-sent" | "failed",
		completedCommandIds: readonly string[] = [],
		releasedRecoveryIds: readonly string[] = [],
	): boolean {
		// A resolution for an obligation that was never recorded commits
		// nothing: queue it until the retried begin creates the row.
		if (this.unrecordedCaptures.has(captureToken)) {
			this.unresolvedResolutions.set(captureToken, {
				state,
				completedCommandIds: [...completedCommandIds],
				releasedRecoveryIds: [...releasedRecoveryIds],
			});
			return false;
		}
		try {
			this.submissionLedger?.().resolveSubmission(captureToken, state, completedCommandIds, releasedRecoveryIds);
			this.unresolvedResolutions.delete(captureToken);
			inFlightCaptures.delete(captureToken);
			this.onObligationResolved?.();
			return true;
		} catch {
			this.unresolvedResolutions.set(captureToken, {
				state,
				completedCommandIds: [...completedCommandIds],
				releasedRecoveryIds: [...releasedRecoveryIds],
			});
			this.scheduleObligationRetry();
			return false;
		}
	}

	/** Retry every obligation write this document still owes the ledger: a
	 * failed begin or resolution from a submission that already went out. The
	 * library calls this on live and retired documents before planning
	 * handoffs, so a transient store failure does not leave an open obligation
	 * blocking its chain after storage recovers. */
	flushObligationResolutions(): void {
		for (const [captureToken, recoveryIds] of [...this.unrecordedCaptures]) {
			try {
				this.submissionLedger?.().beginSubmission(captureToken, this.destination, recoveryIds);
				this.unrecordedCaptures.delete(captureToken);
				inFlightCaptures.add(captureToken);
			} catch {
				// Still failing; the capture stays unrecorded and its resolution queued.
				this.scheduleObligationRetry();
			}
		}
		for (const [captureToken, resolution] of [...this.unresolvedResolutions]) {
			if (this.unrecordedCaptures.has(captureToken)) continue;
			try {
				this.submissionLedger?.().resolveSubmission(
					captureToken,
					resolution.state,
					resolution.completedCommandIds,
					resolution.releasedRecoveryIds,
				);
				this.unresolvedResolutions.delete(captureToken);
				inFlightCaptures.delete(captureToken);
				this.onObligationResolved?.();
				if (resolution.state !== "failed")
					this.deliverCompletion(
						captureToken,
						resolution.state,
						resolution.completedCommandIds,
						resolution.releasedRecoveryIds,
					);
			} catch {
				// Still failing; keep it queued and leave the obligation open.
				this.scheduleObligationRetry();
			}
		}
	}

	/** Retry owed ledger writes after a transient failure, so an obligation
	 * blocks its chain only until storage recovers - including on a retired
	 * document nobody is typing into, whose unmounted screen would otherwise
	 * wait for user action to flush it. */
	private scheduleObligationRetry(): void {
		if (this.forgotten || this.obligationRetryTimer !== null) return;
		this.obligationRetryTimer = setTimeout(() => {
			this.obligationRetryTimer = null;
			this.flushObligationResolutions();
		}, OBLIGATION_RETRY_MS);
	}

	/** Clear this submission's capture at the identity that owns the record now.
	 * Only a completed submission or a proven local refusal owns the capture: a
	 * failed operation leaves its capture recoverable, so delivery never clears
	 * it. A missing graph or a failed write leaves the resolved obligation for
	 * the library's reconciliation pass; the completion is never dropped. */
	private deliverCompletion(
		captureToken: string,
		state: "completed" | "not-sent",
		completedCommandIds: readonly string[],
		releasedRecoveryIds: readonly string[],
	): boolean {
		const ledger = this.submissionLedger?.();
		if (ledger === undefined) return false;
		let graph;
		try {
			graph = ledger.readGraph();
		} catch {
			return false;
		}
		const obligation = graph.obligations.find((entry) => entry.obligationId === captureToken);
		if (obligation === undefined || !clearsCapture(obligation.state)) return false;
		// A transfer can commit while its composed-marker write fails, leaving
		// the graph naming the origin; durable capture evidence at a hop's
		// destination wins over the lagging graph.
		const terminal = captureTerminal(graph.edges, obligation, (endpoint) => {
			try {
				return this.repository().read(endpoint);
			} catch {
				return undefined;
			}
		});
		const completion: DraftCompletionDelivery = {
			captureToken,
			state,
			completedCommandIds,
			releasedRecoveryIds,
		};
		const self = terminal.hubId === this.destination.hubId && terminal.sessionRef === this.destination.sessionRef;
		// Never write a completion through a retired origin: its snapshot can
		// be stale, and the transfer that retired it removed its durable row, so
		// the evidence above names the identity that really holds the capture.
		if (self && this.retired) return false;
		if (self && this.snapshot.loaded) {
			const current = this.snapshot.record;
			if (current.unconfirmedCaptureToken !== captureToken) return false;
			// A recovered message's capture settles through its resend receipt,
			// which the receipt-acknowledging coordinator clears.
			if (messageAdmissionCapture(completion, current)) return true;
			const next = mergeCompletion(current, completion);
			try {
				// The normal save path: pending image data flushes with the
				// completion and a save error the submission outlived clears
				// when this write succeeds, instead of leaving the document
				// sending-disabled after storage recovered.
				this.persist(next, true);
			} catch {
				return false;
			}
			return true;
		}
		// Never write a completion through a retired origin: its snapshot can be
		// stale and its durable row is gone after the transfer that retired it.
		if (self && this.retired) return false;
		// The completion belongs to another identity. Its live document owns any
		// unsaved edits and pending image bytes the persisted record cannot see,
		// so the library merges the completion through that document's own
		// persistence path.
		const outcome = this.deliverCompletionTo?.(terminal, completion);
		if (outcome === "delivered") {
			this.syncOriginCapture(completion);
			return true;
		}
		// A live document at the terminal owns the completion through its own
		// save path: writing the persisted record behind the unsaved images and
		// edits it holds would delete bytes its later save retries still need.
		// The library's reconciliation pass retries the completion there.
		if (outcome === "owned-live") return false;
		if (self) return false;
		// Without a live document the persisted record at the terminal is
		// authoritative.
		let current: DraftRecord;
		try {
			current = this.repository().read(terminal);
		} catch {
			return false;
		}
		if (current.unconfirmedCaptureToken !== captureToken) return false;
		if (messageAdmissionCapture(completion, current)) return true;
		try {
			this.repository().write(terminal, mergeCompletion(current, completion));
		} catch {
			return false;
		}
		this.syncOriginCapture(completion);
		return true;
	}

	/** The origin document keeps the record its edge transferred away; keep its
	 * view coherent with the delivered completion so a later retry proves the
	 * capture consumed instead of refusing a finished hop. */
	private syncOriginCapture(completion: DraftCompletionDelivery): void {
		const { record, loaded } = this.snapshot;
		if (loaded && record.unconfirmedCaptureToken === completion.captureToken)
			this.update({ record: mergeCompletion(record, completion) });
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
