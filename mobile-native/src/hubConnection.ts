import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import type {
	AppwireClient,
	ConnectionState,
	WebSocketLike,
} from "@evener/appwire-client";
import { createConnectionStore } from "@evener/appwire-client/state/connection";
import { createHubClient } from "./connection";
import { connectionFailure } from "./connectionRecovery";

export interface HubConnection {
	client: AppwireClient | null;
	state: ConnectionState;
}

/** Every input the connecting effect depends on except `store` and
 * `repository`: those two are structurally invariant for the hook's whole
 * lifetime (`store` is created once via useState; `repository` is the
 * caller's own stable reference), so they can never distinguish one
 * generation of the connection from another the way these four can. */
interface ConnectionTarget {
	activeId: string | undefined;
	activeOrigin: string | undefined;
	foreground: boolean;
	attempt: number;
}

function sameTarget(a: ConnectionTarget | undefined, b: ConnectionTarget): boolean {
	return (
		a !== undefined &&
		a.activeId === b.activeId &&
		a.activeOrigin === b.activeOrigin &&
		a.foreground === b.foreground &&
		a.attempt === b.attempt
	);
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
	// The complete generation (every input the effect below depends on,
	// short of store/repository - see ConnectionTarget) the store's current
	// client was actually opened for, written in the same synchronous step
	// as store.setState({ client }) below - never on a later render. Passive
	// effects run after commit, so a render whose OWN inputs have already
	// moved on (a switched hub, a bumped retry) would otherwise read this
	// render's inputs against the PREVIOUS generation's still-live client:
	// comparing against the generation recorded alongside that exact client,
	// instead of trusting whatever the store holds, is what ConnectionProvider
	// did before this hook existed (`session?.profileId === selected`) and
	// still has to do here - just keyed on every input that opens a new
	// connection, not only the hub id.
	const connectedFor = useRef<ConnectionTarget | undefined>(undefined);
	// biome-ignore lint/correctness/useExhaustiveDependencies: The retry counter deliberately reopens the same hub connection.
	useEffect(() => {
		let cancelled = false;
		let connection: AppwireClient | null = null;
		let unsubscribe: (() => void) | undefined;
		store.setState({ client: null });
		connectedFor.current = undefined;
		setError(null);
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
				store.setState({ client: connection });
				connectedFor.current = { activeId, activeOrigin, foreground, attempt };
				const currentConnection = connection;
				unsubscribe = currentConnection.onStateChange((next) => {
					if (cancelled) return;
					if (next === "ready") setError(null);
					if (next === "closed")
						setError(
							connectionFailure(currentConnection.terminalReason).message,
						);
				});
				return connection.connect();
			})
			.catch(() => {
				if (!cancelled) {
					setError(
						connectionFailure(connection?.terminalReason ?? null).message,
					);
					store.setState({ state: "closed" });
					connection?.close();
				}
			});
		return () => {
			cancelled = true;
			unsubscribe?.();
			connection?.close();
			// Releases the core's own onStateChange listener too: nothing else
			// clears the client on a true unmount (the next run's own reset only
			// fires when there IS a next run), so without this the retired
			// client's swap-safety listener would outlive the component.
			store.setState({ client: null });
		};
	}, [activeId, activeOrigin, foreground, attempt, store, repository]);
	// The store only ever holds the AppwireClient instances created above.
	// sameTarget is what rejects a stale render: it fails during exactly the
	// window described above - a switched hub OR a bumped retry - before it
	// can ever hand a consumer a previous generation's client under the
	// current generation's identity.
	const target: ConnectionTarget = { activeId, activeOrigin, foreground, attempt };
	const client =
		foreground && sameTarget(connectedFor.current, target)
			? (coreState.client as AppwireClient | null)
			: null;
	return {
		client,
		state: client
			? coreState.state
			: activeId && foreground
				? "connecting"
				: "idle",
	};
}
