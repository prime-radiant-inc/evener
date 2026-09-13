import { MAX_ATTACHMENTS } from "../../cmd/evener-hub/frontend/src/panes/session/composer/attachments/limits";
import {
	insertMarker,
	markerText,
	stripMarker,
} from "../../cmd/evener-hub/frontend/src/panes/session/composer/attachments/textareaMarkers";
import type { InputAttachment } from "../../appwire-client/typescript/composerInput";
import type { DraftImage, DraftImageData } from "./draftImages";
import { restoreUnconfirmedDraft } from "./draftRecovery";
import type {
	DraftDestination,
	DraftRecord,
	DraftRepository,
} from "./draftRepository";

interface DraftSnapshot {
	record: DraftRecord;
	loaded: boolean;
	submitting: boolean;
	error: string | null;
}

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
	private unsavedImages = new Map<string, DraftImageData>();

	imagePreviews(
		images: DraftImage[] = this.snapshot.record.images ?? [],
	): InputAttachment[] {
		return images.map((image) => {
			const pending = this.unsavedImages.get(image.id);
			if (pending) return pending;
			const input = this.repository().imageInputs(this.destination, [image])[0];
			if (!input) throw new Error("Saved image is unavailable.");
			return input;
		});
	}

	constructor(
		private repository: () => Pick<
			DraftRepository,
			"read" | "write" | "imageInputs"
		>,
		private destination: DraftDestination,
	) {
		this.retry();
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
		if (this.forgotten) return;
		try {
			const referenced = new Set(
				[...(record.images ?? []), ...(record.unconfirmedImages ?? [])].map(
					(image) => image.id,
				),
			);
			this.repository().write(
				this.destination,
				record,
				[...this.unsavedImages.values()].filter((image) =>
					referenced.has(image.id),
				),
			);
			this.unsavedImages.clear();
			this.update({ record, error: null });
		} catch (error) {
			this.update({
				...(retainOnFailure ? { record } : {}),
				error:
					"Your latest draft could not be saved on this device. Keep this screen open and retry saving.",
			});
			throw error;
		}
	}
	retry = () => {
		if (this.forgotten) return;
		try {
			if (this.snapshot.loaded) this.persist(this.snapshot.record, true);
			else
				this.update({
					record: this.repository().read(this.destination),
					loaded: true,
					error: null,
				});
		} catch {
			if (!this.snapshot.loaded)
				this.update({
					error:
						"Your saved draft could not be loaded. Retry before composing.",
				});
		}
	};
	forget() {
		this.forgotten = true;
		this.unsavedImages.clear();
		this.update({
			record: { draft: "", unconfirmed: null },
			loaded: false,
			error: null,
		});
	}
	edit(text: string) {
		if (!this.snapshot.loaded) return;
		try {
			this.persist({ ...this.snapshot.record, draft: text }, true);
		} catch {
			/* The snapshot retains unsaved input and its error. */
		}
	}

	replaceDraft(text: string) {
		if (
			!this.snapshot.loaded ||
			this.snapshot.submitting ||
			this.snapshot.record.unconfirmed !== null
		)
			return;
		try {
			this.persist({ draft: text, unconfirmed: null }, true);
		} catch {
			/* Keep the replacement visible with an explicit save retry. */
		}
	}

	addImage(image: DraftImageData, cursor?: number) {
		if (!this.snapshot.loaded) return;
		const { data: _data, ...reference } = image;
		const record = this.snapshot.record;
		const images = record.images ?? [];
		if (
			images.length >= MAX_ATTACHMENTS ||
			images.some((item) => item.marker === image.marker) ||
			[...images, ...(record.unconfirmedImages ?? [])].some(
				(item) => item.id === image.id,
			)
		)
			throw new Error("This image cannot be added to the draft.");
		const position = Math.max(
			0,
			Math.min(cursor ?? record.draft.length, record.draft.length),
		);
		const text = insertMarker(
			record.draft,
			position,
			position,
			markerText(image.marker),
		).value;
		this.unsavedImages.set(image.id, image);
		try {
			this.persist(
				{
					...record,
					draft: text,
					images: [...(record.images ?? []), reference],
				},
				true,
			);
		} catch {
			/* Retain the image bytes and reference for explicit save retry. */
		}
	}

	removeImage(id: string) {
		if (!this.snapshot.loaded) return;
		const record = this.snapshot.record;
		const image = record.images?.find((item) => item.id === id);
		if (!image) return;
		const images = record.images?.filter((item) => item.id !== id);
		try {
			this.persist(
				{
					...record,
					draft: stripMarker(record.draft, undefined, image.marker).value,
					images: images?.length ? images : undefined,
				},
				true,
			);
		} catch {
			/* The visible removal can be retried without deleting other images. */
		}
	}
	async submit(
		operation: (text: string, images: InputAttachment[]) => Promise<boolean>,
	) {
		return this.submitContent(this.snapshot.record.draft, false, operation);
	}
	async submitText(
		text: string,
		operation: (text: string, images: InputAttachment[]) => Promise<boolean>,
	) {
		return this.submitContent(text, true, operation);
	}
	async submitWithQueue(
		operation: (text: string, images: InputAttachment[]) => Promise<boolean>,
	) {
		return this.submitContent(
			this.snapshot.record.draft,
			false,
			operation,
			true,
		);
	}
	private async submitContent(
		text: string,
		preserveDraft: boolean,
		operation: (text: string, images: InputAttachment[]) => Promise<boolean>,
		queuedInput = false,
	) {
		const { record, loaded, submitting, error } = this.snapshot;
		const images = preserveDraft ? [] : (record.images ?? []);
		if (
			!loaded ||
			submitting ||
			error ||
			record.unconfirmed !== null ||
			(!text.trim() && images.length === 0 && !queuedInput)
		)
			return;
		// Persist before invoking any transport operation. A crash after this point
		// can only establish uncertainty, never that it is safe to replay the input.
		const inputImages = this.repository().imageInputs(this.destination, images);
		this.persist(
			{
				draft: preserveDraft ? record.draft : "",
				unconfirmed: text,
				...(preserveDraft && record.images?.length
					? { images: record.images }
					: {}),
				...(images.length ? { unconfirmedImages: images } : {}),
			},
			false,
		);
		this.update({ submitting: true });
		try {
			if (await operation(text, inputImages))
				this.persist(
					{
						...this.snapshot.record,
						unconfirmed: null,
						unconfirmedImages: undefined,
					},
					true,
				);
		} finally {
			this.update({ submitting: false });
		}
	}
	dismiss() {
		if (!this.snapshot.loaded || this.snapshot.submitting) return;
		try {
			this.persist(
				{
					...this.snapshot.record,
					unconfirmed: null,
					unconfirmedImages: undefined,
				},
				true,
			);
		} catch {
			/* Retry retains the requested recovery state. */
		}
	}
	restore() {
		const { record, loaded, submitting } = this.snapshot;
		if (
			!loaded ||
			submitting ||
			record.unconfirmed === null ||
			record.images?.length
		)
			return;
		const text = restoreUnconfirmedDraft(record.draft, record.unconfirmed);
		if (text === null) return;
		try {
			this.persist(
				{
					draft: text,
					unconfirmed: null,
					...(record.unconfirmedImages?.length
						? { images: record.unconfirmedImages }
						: {}),
				},
				true,
			);
		} catch {
			/* Unsaved recovered text stays visible. */
		}
	}
}
