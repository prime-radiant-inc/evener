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
	// The activeId the store's current client was actually opened for,
	// written in the same synchronous step as store.setState({ client })
	// below - never on a later render. Passive effects run after commit, so
	// a render that already has a NEW activeId (its own effect has not run
	// yet) would otherwise read this render's activeId against the PREVIOUS
	// hub's still-live client: comparing against the id recorded alongside
	// that exact client, instead of trusting whatever the store holds, is
	// what ConnectionProvider did before this hook existed (`session
	// ?.profileId === selected`) and still has to do here.
	const connectedFor = useRef<string | undefined>(undefined);
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
				connectedFor.current = activeId;
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
	// connectedFor.current === activeId is what rejects a stale render: it
	// fails during exactly the window described above, before it can ever
	// hand a consumer the previous hub's client under the new hub's identity.
	const client =
		foreground && connectedFor.current === activeId
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
