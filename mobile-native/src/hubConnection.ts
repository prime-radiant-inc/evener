import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import type {
	AppwireClient,
	ConnectionState,
	WebSocketLike,
} from "@evener/appwire-client";
import { createConnectionStore } from "@evener/appwire-client/state/connection";
import { createHubClient } from "./connection";
import { recordClientReadyHub } from "./connectionIdentity";
import { connectionFailure } from "./connectionRecovery";

export interface HubConnection {
	client: AppwireClient | null;
	state: ConnectionState;
	/** True only for a close a retry can never fix (a protocol mismatch -
	 * connectionRecovery.ts's ConnectionFailureKind). Belongs to the CURRENT
	 * generation only: a fresh attempt starts with no verdict of its own,
	 * never carrying the previous generation's failure forward. */
	fatal: boolean;
}

/** How long the connection waits before trying again after `failures`
 * attempts in a row closed without reaching ready (spec 14): at once, then
 * 1, 2, 4, 8 and 16 seconds, then every 30 seconds. */
export function reconnectDelay(failures: number): number {
	return failures <= 0 ? 0 : Math.min(1000 * 2 ** (failures - 1), 30_000);
}

/** The one HubProfiles method this hook needs; HubProfiles itself satisfies
 * it, and a test can hand in a lighter fake without building a real one. */
export interface HubTokenSource {
	token(id: string): Promise<string>;
}

/**
 * The package's createConnectionStore - its client-swap safety and
 * state-following - wired to whichever hub profile the caller names,
 * reopening the same connection whenever activeId, activeOrigin, foreground
 * or attempt change. `setError` is the caller's own error slot (shared with
 * its other failure sources), reported exactly as ConnectionProvider always
 * has; this hook never keeps an error of its own.
 */
export function useHubConnection(
	repository: HubTokenSource,
	activeId: string | undefined,
	activeOrigin: string | undefined,
	foreground: boolean,
	attempt: number,
	setError: (message: string | null) => void,
): HubConnection {
	const [store] = useState(() => createConnectionStore());
	// The hook's own attempts, on top of the caller's `attempt`: each one
	// reopens the connection exactly as a bumped `attempt` does.
	const [retry, setRetry] = useState(0);
	// Attempts in a row that closed without reaching ready.
	const failures = useRef(0);
	const coreState = useSyncExternalStore(store.subscribe, store.getState);
	// The generation (every input the effect below depends on except
	// store/repository, which are structurally invariant for the hook's
	// whole life - store is created once via useState, repository is the
	// caller's own stable reference) the ref's client was actually opened
	// for, as one key rather than a field-by-field comparison - JSON.stringify
	// of the tuple, not a `|`-joined template, so no origin or id can forge a
	// collision with a neighboring field. Carrying the client here too, not
	// only in the store, means this render reads its own concretely-typed
	// AppwireClient instead of casting the store's AppwireClientLike.
	//
	// Written in the same synchronous step as store.setState({ client })
	// below - never on a later render. Passive effects run after commit, so
	// a render whose OWN inputs have already moved on (a switched hub, a
	// bumped retry) would otherwise read this render's inputs against the
	// PREVIOUS generation's still-live client: comparing against the
	// generation recorded alongside that exact client, instead of trusting
	// whatever the store holds, is what ConnectionProvider did before this
	// hook existed (`session?.profileId === selected`) and still has to do
	// here - just keyed on every input that opens a new connection, not only
	// the hub id.
	const connectedFor = useRef<{ key: string; client: AppwireClient } | undefined>(
		undefined,
	);
	const targetKey = JSON.stringify([activeId, activeOrigin, foreground, attempt, retry]);
	const [fatal, setFatal] = useState(false);
	useEffect(() => {
		failures.current = 0;
	}, [activeId, activeOrigin, foreground, attempt]);
	// biome-ignore lint/correctness/useExhaustiveDependencies: The retry counter deliberately reopens the same hub connection.
	useEffect(() => {
		let cancelled = false;
		let connection: AppwireClient | null = null;
		let unsubscribe: (() => void) | undefined;
		setError(null);
		setFatal(false);
		if (!activeId || !activeOrigin || !foreground) return;
		store.setState({ state: "connecting" });
		void repository
			.token(activeId)
			.then((token) => {
				if (cancelled) return;
				connection = createHubClient(activeOrigin, token, (url, options) => {
					// React Native adds native upgrade headers to the standard socket API.
					const NativeWebSocket = WebSocket as unknown as new (
						url: string,
						protocols: string[] | null,
						options: { headers: Record<string, string> },
					) => WebSocketLike;
					return new NativeWebSocket(url, null, options);
				});
				connectedFor.current = { key: targetKey, client: connection };
				// The core's swap path publishes `state: client.state`
				// (state/connection/core.ts), which for a freshly built,
				// not-yet-dialed client is "idle" - overwriting "connecting" until
				// connect() below transitions it back. The core lets a caller's own
				// partial win in the same write, so naming both here avoids that
				// pre-dial "idle" the store would otherwise briefly report.
				store.setState({ client: connection, state: "connecting" });
				const currentConnection = connection;
				unsubscribe = currentConnection.onStateChange((next) => {
					if (cancelled) return;
					if (next === "ready") {
						// The hub this client was dialed for is the one fact
						// the display hooks cannot see for themselves (a
						// route's key can outrun the connection's
						// re-point), so the connection layer records it —
						// and readiness is what proves it: a client still
						// connecting, or whose dial failed, has established
						// nothing, so the record is written here, on the
						// ready transition itself. The state-change dispatch
						// is synchronous: every listener runs before React
						// schedules the render that observes ready, so the
						// record lands before any ready consumer reads it
						// (connectionIdentity, round 61).
						recordClientReadyHub(currentConnection, activeId);
						setError(null);
						setFatal(false);
						failures.current = 0;
					}
					if (next === "closed") {
						const failure = connectionFailure(currentConnection.terminalReason);
						setError(failure.message);
						setFatal(failure.kind === "protocol");
					}
				});
				return connection.connect();
			})
			.catch(() => {
				if (!cancelled) {
					const failure = connectionFailure(connection?.terminalReason ?? null);
					setError(failure.message);
					setFatal(failure.kind === "protocol");
					store.setState({ state: "closed" });
					connection?.close();
				}
			});
		return () => {
			cancelled = true;
			unsubscribe?.();
			connection?.close();
			connectedFor.current = undefined;
			// Releases the core's own onStateChange listener too: nothing else
			// clears the client on a true unmount (a subsequent run's own next
			// cleanup only fires when there IS a subsequent run), so without this
			// the retired client's swap-safety listener would outlive the
			// component. Doubles as the reset a fresh attempt needs - React always
			// runs this cleanup before the next effect body, and on the very first
			// run there is nothing to reset (the store starts at client: null).
			store.setState({ client: null });
		};
	}, [activeId, activeOrigin, foreground, attempt, retry, store, repository, targetKey]);
	const client = connectedFor.current?.key === targetKey ? connectedFor.current.client : null;
	// Round 85's follow-up: a failed token fetch leaves no client and a
	// terminal "closed" verdict in the core (the catch above), so the
	// foregrounded no-client branch must expose that verdict instead of
	// flattening back to "connecting" — a failed reconnect is not a
	// still-connecting one, and reading it as connecting keeps the retry
	// affordance hidden behind a spinner forever. Scoped to the foregrounded
	// case so backgrounded and profile-less mounts keep their exact "idle"
	// report, and a fresh attempt's own effect body re-arms "connecting"
	// synchronously, so the verdict never outlives its generation.
	const closedWithoutClient =
		!client && activeId && foreground && coreState.state === "closed";
	const state: ConnectionState = client
		? coreState.state
		: closedWithoutClient
			? "closed"
			: activeId && foreground
				? "connecting"
				: "idle";
	// A connection that closed for a reason a retry can fix tries again on its
	// own while the app is in front (spec 14), so no screen needs a Reconnect
	// button. A protocol mismatch (fatal) is left alone: retrying can't fix it.
	useEffect(() => {
		if (state !== "closed" || fatal || !activeId || !activeOrigin || !foreground) return;
		const timer = setTimeout(() => {
			failures.current += 1;
			setRetry((value) => value + 1);
		}, reconnectDelay(failures.current));
		return () => clearTimeout(timer);
	}, [state, fatal, activeId, activeOrigin, foreground]);
	return { client, state, fatal };
}
