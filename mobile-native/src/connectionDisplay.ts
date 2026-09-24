import { useCallback, useEffect, useInsertionEffect, useRef, useState } from "react";
import type { AppwireClient, ConnectionState } from "@evener/appwire-client";
import { clientServesHub } from "./connectionIdentity";

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
 * left. A generation counter closed it: the render that observes the pairing
 * move — the scope or client identity, or a connection-state drop that leaves
 * the settled snapshot reporting "ready" through the window — bumps the
 * counter synchronously, so every callback a
 * previous render captured refuses from that instant — the effect's settle
 * then makes the NEW callback usable, and the window never authorizes the old
 * one. The bump is the one render-phase write this hook allows, and it is
 * fail-closed by construction: a render React abandons midway leaves the
 * counter ahead of every callback memoized before it, so each of those
 * refuses (nothing stale is ever authorized). Recovery is memoization, not
 * the settle effect: the callback's dependency array carries the generation
 * it was born at, so the first committed render after an abandoned bump —
 * whose identity matches the last committed one, so no new bump happens —
 * rebuilds the callback from the current counter instead of returning the
 * stale one (round 29). The r21 ban on settling the STATE snapshot during
 * render is untouched — a generation bump carries no authorization data at
 * all. */
/** The hub each client object last proved ready under, kept above the keyed
 * remount boundary that resets every hook instance's own state. A remount can
 * land on the previous hub's still-ready client — the store's selection
 * moves before the connection re-points — and a mount cannot tell from its
 * props whether the pairing it lands on is coherent; this record is the
 * memory that can (connectionIdentity's record, round 56). */

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
	// Seeded empty: the props a remount was born with are not a birth — a
	// re-keyed remount can land on the previous hub's still-ready pairing —
	// so the settling effect records the first birth, and the render before
	// it refuses the pairing the mount arrived with (round 32).
	const bornScope = useRef<{ client: object | null; scope: string }>({ client: null, scope });
	// The observed-pairing generation is advanced by this component's
	// insertion effect, a commit-phase write point — never a render-phase
	// ref write. The refs it writes are frozen between commits, so every
	// render pass, committed or abandoned, reads the same values, and an
	// abandoned pass leaves nothing behind: its callback may capture a
	// generation that never lands, but the pass never commits, so nothing
	// captures it and the committed pairing's callback stays identity-stable
	// (round 73). The insertion effect runs before the layout effects of this
	// tree's children, so a ready-captured callback refuses from the moment
	// a moved render commits, not one settle later (round 27). The state is
	// part of the observed pairing for the same reason: a connection drop
	// must refuse a ready-captured callback the moment the dropped render
	// commits (round 30). The render reads the frozen refs only to compute
	// the generation its own commit would land on — the insertion effect
	// re-derives the comparison and does the writing.
	const generation = useRef(0);
	const lastObserved = useRef({ scope, client, state });
	const moved =
		lastObserved.current.scope !== scope ||
		lastObserved.current.client !== client ||
		lastObserved.current.state !== state;
	const myGeneration = generation.current + (moved ? 1 : 0);
	useInsertionEffect(() => {
		if (
			lastObserved.current.scope !== scope ||
			lastObserved.current.client !== client ||
			lastObserved.current.state !== state
		) {
			lastObserved.current = { scope, client, state };
			generation.current += 1;
		}
	});
	useEffect(() => {
		if (client !== null && bornScope.current.client !== client) {
			// The persistent record refuses the re-birth of a client that
			// proved ready under another hub — the remount residual round 56
			// closes; unknown and native clients birth exactly as before, and
			// the settle bookkeeping below still runs for every client. The
			// record's writer is the connection layer, which alone knows
			// which hub a client was dialed for; this hook only consults it
			// (round 59) — writing here would attribute the client to whatever
			// route this mount happens to render under, before readiness and
			// across the re-key window.
			if (clientServesHub(client, scope)) {
				bornScope.current = { client, scope };
			}
		}
		current.current = { scope, client, state };
	}, [scope, client, state]);
	return useCallback(
		() =>
			generation.current === myGeneration &&
			// The render that built this callback must itself have observed a
			// ready connection: the settled snapshot lags one effect, so a
			// callback born in a dropped render would otherwise authorize
			// against the not-yet-settled "ready" through the window.
			state === "ready" &&
			bornScope.current.client === client &&
			bornScope.current.scope === scope &&
			current.current.scope === scope &&
			current.current.client === client &&
			current.current.state === "ready",
		[scope, client, state, myGeneration],
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
 * earns its screen for the hub its readiness was earned under (`trust`,
 * whose `{trusted, scope}` shape re-records scope only on a transition into
 * ready), so every
 * ready render under the new hub walls until the connection itself
 * transitions into ready for it. A return to a hub whose trust still stands
 * shows immediately.
 *
 * `trust` is state, not a ref, because it gates what the ready
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
	client: AppwireClient | null,
): ConnectionDisplay {
	const everReady = useRef(false);
	const fatalRecovery = useRef(false);
	const scope = useRef<string | undefined>(hubId);
	// The hub whose connection the current ready state vouches for: trust is
	// not born until the settling effect records it, and it is carried as an
	// explicit boolean beside the scope so the empty state can never compare
	// equal to a real scope — a mount whose hubId is also undefined must not
	// pass the gate by collision (round 50). Trust is recorded on the first
	// transition INTO ready the settling effect observes and on every later
	// one, and read on every ready render. A hub change alone — the re-key
	// window's only event — never re-records it, so however long the
	// connection keeps reporting the previous hub's client as ready under the
	// new hub, the trust stays with the hub that earned it. The mount's own
	// ready state earns the trust only through the settling effect, never on
	// the render it arrives in: a re-keyed remount can land on the previous
	// hub's still-ready pairing, and the props a mount was born with are not
	// evidence it vouches for this hub (round 32).
	const [trust, setTrust] = useState<{ trusted: boolean; scope: string | undefined }>({
		trusted: false,
		scope: undefined,
	});
	const lastState = useRef<ConnectionState | "unmounted">("unmounted");
	// The hub the refs still record retention for. The reset itself runs in
	// the effect below, so this render computes with voided values instead of
	// mutating the refs mid-render.
	const scopeMoved = scope.current !== hubId;
	// The pairing verdict the trust arm below keys on: the connection's
	// state alone reports whichever client it still holds — in the re-key
	// window, the previous hub's — so the identity record (the connection
	// layer's memory of which hub a client PROVED ready under, written at
	// its dial) is what says whether that pairing vouches for this hub at
	// all. An unknown client passes exactly as before: the record only ever
	// withholds a client it knows under a different hub (round 63).
	const pairingServesHub = client !== null && clientServesHub(client, hubId);
	useEffect(() => {
		if (scope.current !== hubId) {
			scope.current = hubId;
			everReady.current = false;
			fatalRecovery.current = false;
		}
		const transitionedIntoReady = state === "ready" && lastState.current !== state;
		if (state !== lastState.current) {
			if (state === "ready" && pairingServesHub) {
				// A transition INTO ready is the one event that vouches for
				// the hub it happened under; the mount counts as one through
				// the "unmounted" sentinel. But the state alone reports the
				// CONNECTION, and the connection still reports the previous
				// hub's client through the re-key window — so the transition
				// earns trust only when the pairing passes the identity
				// record. A client the record knows under another hub
				// vouches for nothing here, and the display walls until the
				// re-point's own dial delivers a client this hub can claim.
				setTrust({ trusted: true, scope: hubId });
			}
			lastState.current = state;
		}
		if (
			state === "ready" &&
			((transitionedIntoReady && pairingServesHub) ||
				(trust.trusted && trust.scope === hubId))
		) {
			// The retention arm keys on readiness this hub actually earned.
			// A post-settle ready render alone is not evidence: the scope
			// ref settles while the connection still reports the previous
			// hub's ready state, so arming on the settled scope would retain
			// content for a hub that has never been ready — the reset above
			// would be undone by a later effect run (round 32; round 52
			// closes the post-settle window the bare `!scopeMoved` term left
			// open, where a fatal update while the stale state stayed ready
			// re-armed retention for the new hub and the dialing
			// replacement showed a banner). A transition INTO ready arriving
			// in the moved commit itself is the new hub's own readiness —
			// the screen it shows must retain through the next flap like
			// any other genuinely-ready hub (round 33). A return to a
			// still-trusted scope arms on that trust: the hub's ready state
			// still vouches for it, so the content it shows retains exactly
			// as it would had the move never happened (round 41).
			everReady.current = true;
			fatalRecovery.current = false;
		} else if (fatal) {
			// A fatal close unmounts ready-only children. Keep that wall in place
			// until the replacement is ready; clearing the fatal flag while it is
			// still dialing must not remount a child against the closed client.
			fatalRecovery.current = true;
		}
	}, [state, fatal, hubId, client]);
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
	// The unborn trust walls an undefined hub's mount the same way — the
	// boolean cannot collide with any hubId (round 50). The verdict joins the
	// gate because trust is EARNED state, not live state: a same-state client
	// replacement triggers no transition, so nothing re-earns trust for the
	// new client — a replacement the record refuses walls here even while
	// the trust earned under the old client stands (round 67).
	if (state === "ready" && !(trust.trusted && trust.scope === hubId && pairingServesHub)) {
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
 * The adoption is seeded empty — scope undefined — so a fresh mount serves
 * nothing on its first render however ready the props claim: a remount of
 * this wiring (the screens key their bodies to the hub id) can land on props
 * that are still the previous hub's pairing — the store's selection moves
 * before the connection re-points — and the props a mount was born with are
 * never evidence the pairing earned anything. The settling effect performs
 * the first adoption exactly like every later one, and the empty scope seed
 * is what withholds the mount's first render; a ready render beyond it still
 * serves the live client prop, so a replacement that arrives ready is never
 * delayed a render behind the stores keyed on it (round 32). The adoption is
 * refused for a client the persistent record knows proved ready under
 * ANOTHER hub — the remount residual round 56 closes; a client no mount
 * ever adopted still adopts, because the genuinely new client arrives
 * exactly as unknown (round 56).
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
		scope: undefined,
	}));
	useEffect(() => {
		if (state === "ready" && client !== null && client !== adoption.client) {
			// The same persistent record as the birth: a client that proved
			// ready under another hub is refused here too, so a remount that
			// lands on the previous hub's still-ready pairing cannot
			// re-authorize it under the new hub (round 56). The record is
			// written where the pairing is real — dial success in the
			// connection layer — and this hook only consults it (round 59).
			if (clientServesHub(client, hubId)) {
				setAdoption({ client, scope: hubId });
			}
		}
	}, [state, hubId, client, adoption.client]);
	if (state === "ready") {
		// The adopted client is required, not just the scope: the empty seed
		// scopes to undefined, so a mount whose hubId is also undefined would
		// pass the bare scope comparison on its very first render and serve
		// the live client prop no settling effect has vouched for — the exact
		// mount-borne pairing the seed exists to withhold (round 49). The
		// verdict on the LIVE client is required too (round 67): a same-state
		// replacement swaps the client under an unchanged hubId with no
		// transition the settle could key on, so the render itself asks the
		// identity record — written at dial success — whether the client it
		// hands out serves this hub. A replacement the record vouches for
		// serves on the very render it arrives, never delayed behind the
		// stores keyed on it (round 32's contract, now keyed on identity); a
		// client the record knows under another hub is withheld until the
		// connection re-points, exactly as the settle's own adoption gate
		// refuses it (round 56).
		if (client !== null && adoption.client !== null && adoption.scope === hubId && clientServesHub(client, hubId)) {
			return client;
		}
		return null;
	}
	return adoption.scope === hubId ? adoption.client : null;
}
