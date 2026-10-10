// A hub's pairing link (Settings, then Mobile app, in Evener on a computer):
// the hub's /auth/<token> URL, as a QR code or as text (spec 15).
import { parsePairingURL } from "../connection";

export interface PairingTarget {
	origin: string;
	token: string;
}

/** A scanned or pasted pairing link, or null when the text isn't one. */
export function pairingFrom(text: string): PairingTarget | null {
	try {
		return parsePairingURL(text.trim());
	} catch {
		return null;
	}
}

/** A starting name for a newly paired hub: its host name without the port. */
export function suggestedHubName(origin: string): string {
	try {
		return new URL(origin).hostname;
	} catch {
		return origin;
	}
}
