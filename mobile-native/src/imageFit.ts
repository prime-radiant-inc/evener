// How the phone fits a photo under the attachment limit before it's staged
// (#3099). A camera photo is 12 to 48 megapixels; staged whole, even as JPEG
// it can pass 8 MB. So it's scaled down along its long edge and saved as
// JPEG, in steps that give up a little size and quality each time, stopping
// at the first that fits. A small image is never enlarged.
export interface FitAttempt {
	/** The long edge to scale to, or null to keep the image's own size. */
	resize: { width: number } | { height: number } | null;
	/** JPEG quality, 0 to 1. */
	compress: number;
}

const STEPS = [
	{ longEdge: 2048, compress: 0.85 },
	{ longEdge: 1536, compress: 0.75 },
	{ longEdge: 1024, compress: 0.7 },
] as const;

export function fitAttempts(width: number, height: number): FitAttempt[] {
	const longEdge = Math.max(width, height);
	return STEPS.map((step) => ({
		resize: longEdge > step.longEdge ? (width >= height ? { width: step.longEdge } : { height: step.longEdge }) : null,
		compress: step.compress,
	}));
}

/** Encodes each attempt in turn and keeps the first whose bytes fit;
 * when none does, the last and smallest, for the size check to name. */
export async function encodeToFit(
	width: number,
	height: number,
	maxBytes: number,
	bytes: (data: string) => number,
	encode: (attempt: FitAttempt) => Promise<string>,
): Promise<string> {
	let data = "";
	for (const attempt of fitAttempts(width, height)) {
		data = await encode(attempt);
		if (bytes(data) <= maxBytes) return data;
	}
	return data;
}
