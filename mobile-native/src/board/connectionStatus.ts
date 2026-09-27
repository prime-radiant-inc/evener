import type { ConnectionState } from "@evener/appwire-client";
import { relativeAge } from "@evener/appwire-client/state/navigation";
import { useEffect, useState } from "react";

/** How long the connection can be down before the toolbar says so: a blip
 * shorter than this reconnects without a word. */
export const RECONNECTING_AFTER_MS = 2_000;
/** How long before the toolbar stops promising a reconnect and says how old
 * the rows are. */
export const OFFLINE_AFTER_MS = 30_000;
const MINUTE = 60_000;

/** The connection status the Board toolbar and the Session's connection bar
 * show (spec 14), or null when there is nothing to
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

interface Down {
	since: number;
	/** When the connection was last seen live, or null when it never has
	 * been this launch. */
	lastLiveAt: number | null;
}

/** connectionStatus over the connection as it changes: tracks when it went
 * down and when it was last live, and re-renders at the 2-second and
 * 30-second marks and then once a minute for the age. A live connection runs
 * no clock. */
export function useConnectionStatusText(state: ConnectionState, fatal: boolean): string | null {
	const live = state === "ready";
	const [down, setDown] = useState<Down | null>(() => (live ? null : { since: Date.now(), lastLiveAt: null }));
	const [now, setNow] = useState(Date.now);
	useEffect(() => {
		if (live) {
			setDown(null);
			return () => {
				const at = Date.now();
				setDown({ since: at, lastLiveAt: at });
				setNow(at);
			};
		}
		const tick = () => setNow(Date.now());
		let minutes: ReturnType<typeof setInterval> | undefined;
		const timers = [
			setTimeout(tick, RECONNECTING_AFTER_MS),
			setTimeout(() => {
				tick();
				minutes = setInterval(tick, MINUTE);
			}, OFFLINE_AFTER_MS),
		];
		return () => {
			for (const timer of timers) clearTimeout(timer);
			if (minutes !== undefined) clearInterval(minutes);
		};
	}, [live]);
	return connectionStatus(state, fatal, down?.since ?? null, down?.lastLiveAt ?? null, now);
}
