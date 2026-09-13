import { MAX_ATTACHMENTS } from "../../cmd/evener-hub/frontend/src/panes/session/composer/attachments/limits";
import type { InputAttachment } from "../../cmd/evener-hub/frontend/src/protocol/composerInput";

/** Immutable local image identity plus its composer marker. Bytes live separately. */
export interface DraftImage {
	id: string;
	marker: number;
	mediaType: string;
	name?: string;
}

export interface DraftImageData extends DraftImage {
	data: string;
}

export function imageInput(image: DraftImage, data: string): InputAttachment {
	return {
		marker: image.marker,
		mediaType: image.mediaType,
		data,
		...(image.name === undefined ? {} : { name: image.name }),
	};
}

export function parseImages(raw: string): DraftImage[] {
	const images: unknown = JSON.parse(raw);
	if (!Array.isArray(images) || images.length > MAX_ATTACHMENTS)
		throw new Error("Invalid saved images.");
	const ids = new Set<string>();
	const markers = new Set<number>();
	for (const image of images) {
		if (
			!image ||
			typeof image.id !== "string" ||
			!image.id ||
			ids.has(image.id) ||
			!Number.isSafeInteger(image.marker) ||
			image.marker < 1 ||
			markers.has(image.marker) ||
			typeof image.mediaType !== "string" ||
			!image.mediaType.startsWith("image/") ||
			(image.name !== undefined && typeof image.name !== "string")
		)
			throw new Error("Invalid saved image.");
		ids.add(image.id);
		markers.add(image.marker);
	}
	return images.map((image) => ({
		id: image.id,
		marker: image.marker,
		mediaType: image.mediaType,
		...(image.name === undefined ? {} : { name: image.name }),
	}));
}
