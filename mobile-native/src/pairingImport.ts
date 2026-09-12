import { parsePairingURL } from "./connection";

export interface PairingReview {
	input: string;
	preview: { origin: string; token: string } | null;
	error: string | null;
}

export function editPairingInput(input: string): PairingReview {
	return { input, preview: null, error: null };
}

export function reviewPairingInput(input: string): PairingReview {
	try {
		return { input, preview: parsePairingURL(input), error: null };
	} catch {
		return { input, preview: null, error: "Invalid pairing URL." };
	}
}

export function importPairing(
	review: PairingReview,
): { origin: string; token: string; state: PairingReview } | null {
	if (!review.preview) return null;
	return {
		...review.preview,
		state: { input: "", preview: null, error: null },
	};
}
