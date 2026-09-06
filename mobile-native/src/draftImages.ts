import type { InputAttachment } from "../../cmd/evener-hub/frontend/src/stores/composerInput";

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
