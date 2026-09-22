import { useCallback, useRef } from "react";
import type { AppwireClient, ConnectionState } from "@evener/appwire-client";

/** What a ready-only screen shows for its connection: nothing ("ready"), a
 * full-screen replacement ("wall" - there is nothing to show yet, or a
 * failure a retry can never clear), or a floating status over content that
 * stays mounted ("banner" - a flap the screen survives, auto-refreshing once
 * ready again through the store's own reconnect recovery rather than any
 * manual "refresh to see the latest" affordance). */
export type ConnectionDisplay = "wall" | "banner" | "none";

/** Whether an affordance that issues a request (a mutation, a sign-in, an
 * upgrade, a manual refresh) may run right now. AppWire rejects an ordinary
 * request unless the client is `"ready"`; every screen gates its own
 * mutation/refresh controls through this one predicate rather than each
 * hand-rolling `state === "ready"`. */
export function isReady(state: ConnectionState): boolean {
	return state === "ready";
}

export type LiveReadiness = () => boolean;

/** Keeps a deferred request tied to the render that opened it: the current
 * state must still be ready for the same hub and client before it may run. */
export function useLiveReadiness(
	scope: string,
	client: object | null,
	state: ConnectionState,
): LiveReadiness {
	const current = useRef({ scope, client, state });
	current.current = { scope, client, state };
	return useCallback(
		() =>
			current.current.scope === scope &&
			current.current.client === client &&
			current.current.state === "ready",
		[scope, client],
	);
}

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

/** Wraps a handler so it no-ops unless the live readiness predicate is true: the shared guard an
 * `onPress` that issues a request uses, in place of an ad hoc
 * `if (ready) ...` (or its inverse, `if (!ready) return`) copied at each call
 * site. Pairs with the `disabled` prop reading the same `isReady(state)`, so
 * a missed `disabled` or a programmatic press still cannot start the
 * request. */
export function whenReady<A extends unknown[]>(
	ready: LiveReadiness,
	handler: (...args: A) => void,
): (...args: A) => void {
	return (...args: A) => {
		if (ready()) handler(...args);
	};
}

/** The client a ready-only screen renders with while retained through the
 * brief client-null window of a manual retry (hubConnection.ts clears
 * `client` while it dials a fresh one; a passive flap never does - its own
 * generation guard keeps the same client object through the whole flap).
 * Shared by every ready-only screen that needs the fallback, in place of each
 * keeping its own identical ref. */
export function useRenderClient(
	client: AppwireClient | null,
): AppwireClient | null {
	const lastClient = useRef<AppwireClient | null>(null);
	if (client) lastClient.current = client;
	return client ?? lastClient.current;
}
