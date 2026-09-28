// What the connection says wherever a screen shows it (spec 14): nothing
// while live, "Reconnecting…" after 2 seconds without a connection,
// "Offline · updated 3m ago" after 30, and "Update needed" for a close no
// retry can fix. The Board's toolbar, the Session's connection bar and the
// sheets' status lines all read it, on ConnectionProvider's one clock.
import type { ConnectionState } from "@evener/appwire-client";
import { useEffect, useState } from "react";
import { useConnection } from "../ConnectionProvider";
import { compactDuration } from "../session/format";

/** How long the connection can be down before the status says so: a blip
 * shorter than this reconnects without a word. */
export const RECONNECTING_AFTER_MS = 2_000;
/** How long before the status stops promising a reconnect and says how old
 * what's on screen is. */
export const OFFLINE_AFTER_MS = 30_000;
const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;
/** The status for a close no retry can fix; INCOMPATIBLE_VERSIONS says what
 * it means. */
export const UPDATE_NEEDED = "Update needed";

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
	if (fatal) return UPDATE_NEEDED;
	if (state === "ready" || downSince === null) return null;
	const down = now - downSince;
	if (down < RECONNECTING_AFTER_MS) return null;
	if (down < OFFLINE_AFTER_MS) return "Reconnecting…";
	return lastLiveAt === null ? "Offline" : `Offline · updated ${offlineAge(now - lastLiveAt)} ago`;
}

/** How old the last live data is, in spec 5's compact durations, never under
 * 1m: the status first says it at 30 seconds, where a count of seconds would
 * tick every second and "now" would read "updated now ago". */
export function offlineAge(ms: number): string {
	return compactDuration(Math.max(MINUTE, ms));
}

/** When the status next changes on its own, or null when it won't: at the
 * 2- and 30-second marks, then when the age it says next changes, in the unit
 * it says it in (offlineAge: minutes, never under 1m, then hours, then
 * days). */
export function nextStatusChange(
	state: ConnectionState,
	fatal: boolean,
	downSince: number | null,
	lastLiveAt: number | null,
	now: number,
): number | null {
	if (fatal || state === "ready" || downSince === null) return null;
	const down = now - downSince;
	if (down < RECONNECTING_AFTER_MS) return downSince + RECONNECTING_AFTER_MS;
	if (down < OFFLINE_AFTER_MS) return downSince + OFFLINE_AFTER_MS;
	if (lastLiveAt === null) return null;
	const age = now - lastLiveAt;
	const unit = age < HOUR ? MINUTE : age < DAY ? HOUR : DAY;
	return lastLiveAt + Math.max(2 * MINUTE, (Math.floor(age / unit) + 1) * unit);
}

/** The status for the app's connection, re-rendered exactly when its words
 * change, on the provider's one clock (ConnectionClock), so every screen
 * says the same thing at the same moment however recently it mounted. A
 * live connection runs no timer. */
export function useConnectionStatusText(): string | null {
	const { state, fatal, downSince, lastLiveAt } = useConnection();
	const [now, setNow] = useState(Date.now);
	// `now` is a dependency though the body doesn't read it: each tick re-runs
	// this effect, which schedules the next one.
	useEffect(() => {
		const current = Date.now();
		const next = nextStatusChange(state, fatal, downSince, lastLiveAt, current);
		if (next === null) return;
		const timer = setTimeout(() => setNow(Date.now()), Math.max(0, next - current));
		return () => clearTimeout(timer);
	}, [state, fatal, downSince, lastLiveAt, now]);
	return connectionStatus(state, fatal, downSince, lastLiveAt, now);
}
