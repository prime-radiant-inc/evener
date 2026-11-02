import {
	type AttachmentRejection,
	admissionRejection,
	MAX_ATTACHMENTS,
	nextComposerImageMarker,
	sizeRejection,
} from "@evener/appwire-client";
import type { DraftDocument } from "./draftDocument";

export interface PickedImage {
	uri: string;
	name: string;
	type: string;
	size: number;
	/** The pixel size the picker reports; absent or 0 when it doesn't know. */
	width?: number;
	height?: number;
}
interface PendingImage extends PickedImage {
	id: string;
	marker: number;
}
/** Why a picked image, or the whole pick, didn't attach. */
export type ImageRefusalReason = AttachmentRejection | "sizeUnread" | "notPrepared" | "cameraDenied" | "unreadable";

/** One thing a pick refused: the image it names, or null for the pick as a
 * whole (the camera was off, or the selection couldn't be read). */
export interface ImageRefusal {
	name: string | null;
	reason: ImageRefusalReason;
}

interface SelectionSnapshot {
	busy: boolean;
	pending: PendingImage[];
	/** What the last pick refused, in order. */
	refusals: ImageRefusal[];
	/** The refusals in the person's terms, one line each; null with none. */
	error: string | null;
}
export interface ImagePicker {
	pick(limit: number): Promise<PickedImage[]>;
	/** One photo from the camera; rejects with CameraAccessDenied when the
	 * person has turned camera access off. */
	capture(): Promise<PickedImage[]>;
	/** The image as the phone sends it: scaled down to fit the attachment
	 * limit, or as it is when it already fits (nativeImagePicker.ts). */
	encode(image: PickedImage): Promise<EncodedImage>;
	id(): string;
}

const CAMERA_OFF = "Camera access is off. Turn it on in Settings to take photos here.";

/** The camera permission is off, so the picker could not open the camera. */
export class CameraAccessDenied extends Error {
	constructor() {
		super(CAMERA_OFF);
		this.name = "CameraAccessDenied";
	}
}

/** An image as base64, and the type it is in: always an image, so only its
 * size is checked once it's encoded. */
export interface EncodedImage {
	data: string;
	mediaType: "image/jpeg" | "image/png";
}

/** The largest picked file the phone decodes to scale it: far above a camera
 * HEIC or JPEG, and a bound on the memory decoding takes. */
export const MAX_SOURCE_BYTES = 25 * 1024 * 1024;

/** A full frame from a 48 megapixel iPhone camera, the most pixels the phone
 * decodes. */
const FULL_FRAME_WIDTH = 8064;
const FULL_FRAME_HEIGHT = 6048;
const MAX_SOURCE_PIXELS = FULL_FRAME_WIDTH * FULL_FRAME_HEIGHT;

/** Why an image can't be attached, after its name: the reason, then what to
 * do (#3166). Too large to decode and too large to send read the same. */
const REFUSED: Record<ImageRefusalReason, string> = {
	notImage: "This isn't an image. Choose an image to attach.",
	tooMany: `You can attach up to ${MAX_ATTACHMENTS} images. Remove one to add another.`,
	tooLarge: "This image is too large to attach. Try a smaller image or a screenshot.",
	sizeUnread: "This image's size couldn't be read. Try another image.",
	notPrepared: "This image couldn't be prepared to attach. Try another image.",
	cameraDenied: CAMERA_OFF,
	unreadable: "Could not open or read the image selection.",
};

function refused(image: PickedImage, reason: ImageRefusalReason): ImageRefusal {
	return { name: image.name, reason };
}

function refusalText({ name, reason }: ImageRefusal): string {
	return name === null ? REFUSED[reason] : `${name}: ${REFUSED[reason]}`;
}

function refusalUpdate(refusals: ImageRefusal[]): Pick<SelectionSnapshot, "refusals" | "error"> {
	return { refusals, error: refusals.length ? refusals.map(refusalText).join("\n") : null };
}

/** Why a picked image can't be staged, said before it's decoded, or undefined.
 * Its own size isn't the attachment limit: the phone scales it down and the
 * encoding is measured after. A picker that doesn't say its pixel size leaves
 * only the file-size cap to bound the decode. */
function sourceRejection(image: PickedImage, reserved: number): ImageRefusal | undefined {
	if (!Number.isFinite(image.size) || image.size < 0) return refused(image, "sizeUnread");
	if (image.size > MAX_SOURCE_BYTES || (image.width ?? 0) * (image.height ?? 0) > MAX_SOURCE_PIXELS)
		return refused(image, "tooLarge");
	const rejection = admissionRejection(image, reserved);
	return rejection && refused(image, rejection);
}

function base64ByteLength(data: string): number {
	const padding = data.endsWith("==") ? 2 : data.endsWith("=") ? 1 : 0;
	return (data.length * 3) / 4 - padding;
}

/** Owns selection and encoding for one draft, including late native results. */
export class ImageSelection {
	private snapshot: SelectionSnapshot = {
		busy: false,
		pending: [],
		refusals: [],
		error: null,
	};
	private listeners = new Set<() => void>();
	private generation = 0;
	private nextMarker = 0;
	constructor(
		private document: Pick<DraftDocument, "getSnapshot" | "addImage">,
		private picker: ImagePicker,
	) {}
	getSnapshot = () => this.snapshot;
	subscribe = (listener: () => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};
	private update(update: Partial<SelectionSnapshot>) {
		this.snapshot = { ...this.snapshot, ...update };
		for (const listener of this.listeners) listener();
	}
	cancel() {
		this.generation++;
		this.update({ busy: false, pending: [], ...refusalUpdate([]) });
	}
	remove(id: string) {
		this.update({
			pending: this.snapshot.pending.filter((image) => image.id !== id),
		});
	}
	async choose(source: "library" | "camera" = "library") {
		const draft = this.document.getSnapshot();
		if (this.snapshot.busy || !draft.loaded || draft.error) return;
		const count = draft.record.images?.length ?? 0;
		if (count >= MAX_ATTACHMENTS) {
			this.update(refusalUpdate([{ name: null, reason: "tooMany" }]));
			return;
		}
		const generation = ++this.generation;
		this.update({ busy: true, ...refusalUpdate([]) });
		const refusals: ImageRefusal[] = [];
		try {
			const picked =
				source === "camera" ? await this.picker.capture() : await this.picker.pick(MAX_ATTACHMENTS - count);
			if (generation !== this.generation) return;
			const current = this.document.getSnapshot().record;
			const attachments = [...(current.images ?? []), ...(current.unconfirmedImages ?? [])];
			let reserved = current.images?.length ?? 0;
			const pending: PendingImage[] = [];
			for (const image of picked) {
				const refusal = sourceRejection(image, reserved);
				if (refusal) {
					refusals.push(refusal);
					continue;
				}
				reserved++;
				const marker = nextComposerImageMarker(current.draft, [...attachments, ...pending], this.nextMarker);
				this.nextMarker = marker;
				pending.push({
					...image,
					id: this.picker.id(),
					marker,
				});
			}
			this.update({ pending });
			// Decode one full-resolution image at a time to bound native memory use.
			for (const image of pending) {
				if (generation !== this.generation) return;
				if (!this.snapshot.pending.some((item) => item.id === image.id)) continue;
				try {
					const { data, mediaType } = await this.picker.encode(image);
					if (generation !== this.generation) return;
					if (!this.snapshot.pending.some((item) => item.id === image.id)) continue;
					const rejection = sizeRejection(base64ByteLength(data));
					if (rejection) refusals.push(refused(image, rejection));
					else
						this.document.addImage({
							id: image.id,
							marker: image.marker,
							mediaType,
							name: image.name,
							data,
						});
				} catch {
					if (generation === this.generation && this.snapshot.pending.some((item) => item.id === image.id))
						refusals.push(refused(image, "notPrepared"));
				}
				if (generation === this.generation) this.remove(image.id);
			}
		} catch (error) {
			refusals.push({ name: null, reason: error instanceof CameraAccessDenied ? "cameraDenied" : "unreadable" });
		} finally {
			if (generation === this.generation) this.update({ busy: false, pending: [], ...refusalUpdate(refusals) });
		}
	}
}
