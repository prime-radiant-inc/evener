import { useCallback, useEffect, useRef, useState } from "react";
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

/** The pairing snapshot (`current`, `bornScope`) is settled by a passive
 * effect, so between a moved render's commit and that effect a callback from
 * the previous render still validated against the previous settle — the exact
 * window where a deferred request could fire on a hub the render has already
 * left. A generation counter closed it: the render that observes the identity
 * (scope or client) move bumps the counter synchronously, so every callback a
 * previous render captured refuses from that instant — the effect's settle
 * then makes the NEW callback usable, and the window never authorizes the old
 * one. The bump is the one render-phase write this hook allows, and it is
 * fail-closed by construction: a render React abandons midway leaves the
 * counter ahead of the settled snapshot, so every callback refuses (nothing
 * stale is ever authorized) until the next committed render's effect catches
 * up. The r21 ban on settling the STATE snapshot during render is untouched —
 * a generation bump carries no authorization data at all. */
/** Keeps a deferred request tied to the render that opened it: the current
 * state must still be ready for the same hub and client before it may run —
 * and in the re-key window, where the hub has moved while the connection
 * still reports the previous hub's client as ready, that pairing is the
 * previous hub's say-so and authorizes nothing. A client object's first
 * observation is the scope it was born under: a moved scope carrying a client
 * born under the previous hub is refused however often it re-renders, until
 * the connection reports a client that genuinely arrived — and proved ready —
 * under the scope it is asked to authorize. */
export function useLiveReadiness(
	scope: string,
	client: object | null,
	state: ConnectionState,
): LiveReadiness {
	// The pairing is settled by the effect below, never during render: a
	// render React abandons midway must not leave ref writes behind.
	const current = useRef({ scope, client, state });
	const bornScope = useRef({ client, scope });
	const lastIdentity = useRef({ scope, client });
	const generation = useRef(0);
	// Synchronous on the identity move, before any effect runs: this is what
	// closes the commit-to-effect window.
	if (lastIdentity.current.scope !== scope || lastIdentity.current.client !== client) {
		generation.current += 1;
	}
	const myGeneration = generation.current;
	useEffect(() => {
		lastIdentity.current = { scope, client };
		if (client !== null && bornScope.current.client !== client) {
			bornScope.current = { client, scope };
		}
		current.current = { scope, client, state };
	}, [scope, client, state]);
	return useCallback(
		() =>
			generation.current === myGeneration &&
			bornScope.current.client === client &&
			bornScope.current.scope === scope &&
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
 * state: it never causes its own re-render, is updated in an effect rather
 * than during render (a render React abandons midway must not leave ref
 * writes behind), and only ever needs to be read alongside the state that
 * already re-renders the screen when it changes.
 *
 * `hubId` is the hub whose connection `state` reports - the ACTIVE
 * profile's hub, not the one the route names. The two disagree in the
 * window where a navigator has already re-keyed a mounted screen to
 * another hub while the connection still reports the previous one (the
 * screen's own `activeProfile?.id !== route.params.hubId` early return is
 * what hides that window's data), and retention recorded for the previous
 * hub says nothing about the next one: both refs are reset as soon as
 * `hubId` moves, and the new hub's first render already reads them as void,
 * so a hub the profile is still moving toward inherits neither the banner
 * history nor the fatal wall of the one before it. That first moved render
 * walls outright — the state it reports still belongs to the previous hub —
 * and the window does not close when the effects settle: a ready state only
 * earns its screen for the hub its readiness was earned under
 * (`trustedScope`, re-recorded only on a transition into ready), so every
 * ready render under the new hub walls until the connection itself
 * transitions into ready for it. A return to a hub whose trust still stands
 * shows immediately.
 *
 * `trustedScope` is state, not a ref, because it gates what the ready
 * renders show: the transition into ready that re-records it is the same
 * commit the ready renders arrive in, so without its own re-render a screen
 * whose connection went ready under a new hub would read the stale trust
 * until something else happened to render. The `everReady` and fatal
 * recovery refs stay refs - they are only ever read alongside a state that
 * already re-renders. */
export function useConnectionDisplay(
	hubId: string | undefined,
	state: ConnectionState,
	fatal: boolean,
): ConnectionDisplay {
	const everReady = useRef(false);
	const fatalRecovery = useRef(false);
	const scope = useRef<string | undefined>(hubId);
	// The hub whose connection the current ready state vouches for: set at
	// mount and on every transition INTO ready, read on every ready render. A
	// hub change alone — the re-key window's only event — never re-records
	// it, so however long the connection keeps reporting the previous hub's
	// client as ready under the new hub, the trust stays with the hub that
	// earned it.
	const [trustedScope, setTrustedScope] = useState<string | undefined>(hubId);
	const lastState = useRef<ConnectionState | "unmounted">("unmounted");
	// The hub the refs still record retention for. The reset itself runs in
	// the effect below, so this render computes with voided values instead of
	// mutating the refs mid-render.
	const scopeMoved = scope.current !== hubId;
	useEffect(() => {
		if (scope.current !== hubId) {
			scope.current = hubId;
			everReady.current = false;
			fatalRecovery.current = false;
		}
		if (state !== lastState.current) {
			if (state === "ready") {
				// A transition INTO ready is the one event that vouches for
				// the hub it happened under; the mount counts as one through
				// the "unmounted" sentinel.
				setTrustedScope(hubId);
			}
			lastState.current = state;
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
	}, [state, fatal, hubId]);
	// The connection reports the PREVIOUS hub in the re-key window (`hubId`
	// has moved while the connection still reports the old one), and this
	// hook cannot see which client that state vouches for. Void the moved
	// render's non-ready states outright — the wall is the fail-closed
	// display — and read retention only on the renders after the effect
	// settles the new scope; its ready states take the trust gate below.
	if (scopeMoved && state !== "ready") {
		return "wall";
	}
	// A ready state only earns its screen for the hub it vouches for: through
	// the whole window that is the previous hub, so the ready renders under
	// the new one wall until the connection transitions into ready for it.
	if (state === "ready" && trustedScope !== hubId) {
		return "wall";
	}
	return connectionDisplay(state, everReady.current, fatal || fatalRecovery.current);
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
 * The adoption records the hub the new client proved ready under, and a
 * client is only served for the hub that adopted it: a hub change alone
 * re-adopts nothing, so in the re-key window — where the connection still
 * reports the previous hub's client as ready — the carried client is refused
 * on every render, the way `useConnectionDisplay` walls them, until the
 * connection reports a client that proved ready under the new hub (withheld
 * for the one render before its settling effect runs). A return to a hub
 * whose adoption still stands serves immediately.
 *
 * The adoption is state, not a ref, because it gates what the ready renders
 * hand out: a connection can deliver a new hub's client and its readiness in
 * one commit, and without the adoption's own re-render the stores that read
 * through the rendered client would wait on a render that never comes. */
export function useRenderClient(
	client: AppwireClient | null,
	state: ConnectionState,
	hubId: string | undefined,
): AppwireClient | null {
	const [adoption, setAdoption] = useState<{ client: AppwireClient | null; scope: string | undefined }>(() => ({
		client: null,
		scope: hubId,
	}));
	useEffect(() => {
		if (state === "ready" && client !== null && client !== adoption.client) {
			setAdoption({ client, scope: hubId });
		}
	}, [state, hubId, client, adoption.client]);
	if (state === "ready") {
		return adoption.scope === hubId ? client : null;
	}
	return adoption.scope === hubId ? adoption.client : null;
}
