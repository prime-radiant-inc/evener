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
	const targetKey = JSON.stringify([activeId, activeOrigin, foreground, attempt]);
	const [fatal, setFatal] = useState(false);
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
	}, [activeId, activeOrigin, foreground, attempt, store, repository, targetKey]);
	const client = connectedFor.current?.key === targetKey ? connectedFor.current.client : null;
	return {
		client,
		state: client
			? coreState.state
			: activeId && foreground
				? "connecting"
				: "idle",
		fatal,
	};
}
