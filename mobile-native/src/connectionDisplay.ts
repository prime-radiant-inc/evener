import { useRef } from "react";
import type { ConnectionState } from "@evener/appwire-client";

/** What a ready-only screen shows for its connection: nothing ("ready"), a
 * full-screen replacement ("wall" - there is nothing to show yet, or a
 * failure a retry can never clear), or a floating status over content that
 * stays mounted ("banner" - a flap the screen survives, auto-refreshing once
 * ready again through the store's own reconnect recovery rather than any
 * manual "refresh to see the latest" affordance). */
export type ConnectionDisplay = "wall" | "banner" | "none";

/** `everReady` is per-screen-instance state (has THIS mount ever seen
 * `state === "ready"` for the hub it is showing) - the wall is for a screen
 * that has never had anything to show, not for a screen recovering from a
 * flap. `fatal` names a close a retry cannot fix (a protocol mismatch,
 * connectionRecovery.ts's ConnectionFailureKind), which stays a wall even
 * once something has been shown, since nothing behind the banner can help. */
export function connectionDisplay(
	state: ConnectionState,
	everReady: boolean,
	fatal: boolean,
): ConnectionDisplay {
	if (state === "ready") return "none";
	if (!everReady || fatal) return "wall";
	return "banner";
}

/** Tracks `everReady` across a screen's own connection transitions and
 * derives its display from the current state. `everReady` is a ref, not
 * state: it mutates during render (the ForkScreen.tsx `owner.current`
 * pattern), never causes its own re-render, and only ever needs to be read
 * alongside the state that already re-renders the screen when it changes. */
export function useConnectionDisplay(
	state: ConnectionState,
	fatal: boolean,
): ConnectionDisplay {
	const everReady = useRef(false);
	if (state === "ready") everReady.current = true;
	return connectionDisplay(state, everReady.current, fatal);
}
