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
 * alongside the state that already re-renders the screen when it changes.
 *
 * `hubId` is the hub whose connection `state` reports - the ACTIVE
 * profile's hub, not the one the route names. The two disagree in the
 * window where a navigator has already re-keyed a mounted screen to
 * another hub while the connection still reports the previous one (the
 * screen's own `activeProfile?.id !== route.params.hubId` early return is
 * what hides that window's data), and retention recorded for the previous
 * hub says nothing about the next one: both refs are reset the moment
 * `hubId` moves, so a hub the profile is still moving toward inherits
 * neither the banner history nor the fatal wall of the one before it. */
export function useConnectionDisplay(
	hubId: string | undefined,
	state: ConnectionState,
	fatal: boolean,
): ConnectionDisplay {
	const everReady = useRef(false);
	const fatalRecovery = useRef(false);
	const scope = useRef<string | undefined>(hubId);
	if (scope.current !== hubId) {
		scope.current = hubId;
		everReady.current = false;
		fatalRecovery.current = false;
	}
	if (state === "ready") {
		everReady.current = true;
		fatalRecovery.current = false;
	} else if (fatal) {
		// A fatal close unmounts ready-only children. Keep that wall in place
		// until the replacement is ready; clearing the fatal flag while it is
		// still dialing must not remount a child against the closed client.
		fatalRecovery.current = true;
	}
	return connectionDisplay(
		state,
		everReady.current,
		fatal || fatalRecovery.current,
	);
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

/** The client a ready-only screen renders with while retained through a
 * manual retry: hubConnection.ts clears `client` the instant it starts
 * dialing a fresh one, then reports the REPLACEMENT while it is still
 * `"connecting"` - not yet safe to hand to a store, whose mount effect would
 * issue a request AppWire rejects before the client is `"ready"` (a passive
 * flap never does either of this - its own generation guard keeps the same
 * client object, already ready, through the whole flap). A new client is
 * therefore adopted only once `state` is `"ready"` for it; every other
 * transition - the null gap AND the whole `"connecting"` window before it -
 * keeps returning whatever was last adopted, the way the web's
 * ConnectionBanner (cmd/evener-hub/frontend/src/shell/ConnectionBanner.tsx)
 * only calls its own `onClientReplaced` - which swaps AppShell's
 * ClientProvider slot - AFTER `await fresh.connect()` resolves, never on
 * construction.
 *
 * Scoped to `hubId` - the ACTIVE profile's hub, not the route's - for the
 * same reason `useConnectionDisplay` is: once the connection's hub moves,
 * whatever client the previous hub adopted is dropped, so a screen re-keyed
 * ahead of its profile never renders the previous hub's retained client
 * under the new hub's banner. */
export function useRenderClient(
	client: AppwireClient | null,
	state: ConnectionState,
	hubId: string | undefined,
): AppwireClient | null {
	const lastClient = useRef<AppwireClient | null>(null);
	const scope = useRef<string | undefined>(hubId);
	if (scope.current !== hubId) {
		scope.current = hubId;
		lastClient.current = null;
	}
	if (state === "ready") lastClient.current = client;
	return state === "ready" ? client : lastClient.current;
}

