import { MAX_ATTACHMENTS } from "@evener/appwire-client";
import type { MutationAttachmentRef, MutationRecoveryRecord } from "@evener/appwire-client/state/mutation";
import type { DraftImageData } from "./draftImages";

export interface RecoveredNativeDraft {
	draft: string;
	images: DraftImageData[];
}

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null;
}

interface RecoverableImage {
	data: string;
	mediaType?: string;
	name?: string;
}

function recoverableImages(input: unknown[]): RecoverableImage[] | undefined {
	const images: RecoverableImage[] = [];
	for (const item of input) {
		if (!isRecord(item) || typeof item.type !== "string") return undefined;
		if (item.type === "text") {
			if (typeof item.text !== "string") return undefined;
			continue;
		}
		if (item.type !== "image" || typeof item.data !== "string" || item.data.length === 0) return undefined;
		if (
			(item.mediaType !== undefined && typeof item.mediaType !== "string") ||
			(item.name !== undefined && typeof item.name !== "string")
		)
			return undefined;
		images.push({
			data: item.data,
			...(item.mediaType === undefined ? {} : { mediaType: item.mediaType }),
			...(item.name === undefined ? {} : { name: item.name }),
		});
	}
	return images;
}

function validAttachment(value: unknown): value is MutationAttachmentRef {
	if (!isRecord(value)) return false;
	return (
		typeof value.presentationId === "string" &&
		value.presentationId.length > 0 &&
		typeof value.marker === "number" &&
		Number.isSafeInteger(value.marker) &&
		value.marker > 0 &&
		typeof value.name === "string" &&
		typeof value.mediaType === "string" &&
		value.mediaType.startsWith("image/")
	);
}

// The payload has image bytes but no marker identity. Pair those bytes with
// durable attachment metadata by their producer-preserved order; a count
// mismatch is rejected instead of guessing which marker an image belonged to.
export function recoveryToNativeDraft(
	record: MutationRecoveryRecord<MutationAttachmentRef>,
): RecoveredNativeDraft | null {
	if (typeof record.composerText !== "string" || !Array.isArray(record.attachments)) return null;
	if (!isRecord(record.payload) || !Array.isArray(record.payload.input)) return null;
	if (record.attachments.length > MAX_ATTACHMENTS) return null;

	const images = recoverableImages(record.payload.input);
	if (images === undefined || images.length !== record.attachments.length) return null;

	const ids = new Set<string>();
	const markers = new Set<number>();
	const recoveredImages: DraftImageData[] = [];
	for (let index = 0; index < images.length; index += 1) {
		const attachment = record.attachments[index];
		const image = images[index];
		if (
			attachment === undefined ||
			!validAttachment(attachment) ||
			ids.has(attachment.presentationId) ||
			markers.has(attachment.marker) ||
			(image.mediaType !== undefined && image.mediaType !== attachment.mediaType) ||
			(image.name !== undefined && image.name !== attachment.name)
		)
			return null;
		ids.add(attachment.presentationId);
		markers.add(attachment.marker);
		recoveredImages.push({
			id: attachment.presentationId,
			marker: attachment.marker,
			mediaType: attachment.mediaType,
			name: attachment.name,
			data: image.data,
		});
	}

	return { draft: record.composerText, images: recoveredImages };
}
