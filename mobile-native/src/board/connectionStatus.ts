import type { ConnectionState } from "@evener/appwire-client";
import { relativeAge } from "@evener/appwire-client/state/navigation";

/** How long the connection can be down before the toolbar says so: a blip
 * shorter than this reconnects without a word. */
export const RECONNECTING_AFTER_MS = 2_000;
/** How long before the toolbar stops promising a reconnect and says how old
 * the rows are. */
export const OFFLINE_AFTER_MS = 30_000;

/** The Board toolbar's status (spec 14), or null when there is nothing to
 * say. A close no retry can fix says "Update needed" at once and never
 * Reconnecting. Otherwise the text depends on how long the connection has
 * been down (`downSince`) and when it was last live (`lastLiveAt`, null when
 * the hub was never reached this launch). */
export function connectionStatus(
	state: ConnectionState,
	fatal: boolean,
	downSince: number | null,
	lastLiveAt: number | null,
	now: number,
): string | null {
	if (fatal) return "Update needed";
	if (state === "ready" || downSince === null) return null;
	const down = now - downSince;
	if (down >= OFFLINE_AFTER_MS) {
		if (lastLiveAt === null) return "Offline";
		// Whole minutes, at least one: never "updated now ago".
		const age = relativeAge(new Date(lastLiveAt).toISOString(), now);
		return `Offline · updated ${age === undefined || age === "now" ? "1m" : age} ago`;
	}
	if (down >= RECONNECTING_AFTER_MS) return "Reconnecting…";
	return null;
}
