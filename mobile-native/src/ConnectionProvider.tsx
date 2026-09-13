import * as Crypto from "expo-crypto";
import * as SecureStore from "expo-secure-store";
import {
	createContext,
	type ReactNode,
	useCallback,
	useContext,
	useEffect,
	useMemo,
	useState,
} from "react";
import { AppState } from "react-native";
import type {
	AppwireClient,
	ConnectionState,
} from "../../appwire-client/typescript/client";
import type { WebSocketLike } from "../../appwire-client/typescript/transport";
import {
	createHubClient,
	type HubInput,
	type HubProfile,
	HubProfiles,
	type HubUpdate,
} from "./connection";
import { connectionFailure } from "./connectionRecovery";
import { HubSelection } from "./hubSelection";
import type { SavedLocation } from "./location";
import { drafts } from "./nativeDrafts";
import { locations } from "./nativeLocation";
import { removeOrganizationData } from "./nativeOrganization";
import { readerPositions } from "./nativeReaderPosition";

const repository = new HubProfiles(SecureStore);
interface Connection {
	initialLocation: SavedLocation | null;
	restorationError: string | null;
	profiles: HubProfile[];
	activeProfile: HubProfile | null;
	client: AppwireClient | null;
	state: ConnectionState;
	error: string | null;
	loading: boolean;
	saveHub(input: HubInput): Promise<boolean>;
	updateHub(id: string, input: HubUpdate): Promise<void>;
	selectHub(id: string): void;
	removeHub(id: string): Promise<void>;
	disconnect(): void;
	retry(): void;
}
const Context = createContext<Connection | null>(null);

export function ConnectionProvider({ children }: { children: ReactNode }) {
	const [initialLocation, setInitialLocation] = useState<SavedLocation | null>(
		null,
	);
	const [restorationError, setRestorationError] = useState<string | null>(null);
	const [profiles, setProfiles] = useState<HubProfile[]>([]);
	const [selected, setSelected] = useState<string | null>(null);
	const [session, setSession] = useState<{
		profileId: string;
		client: AppwireClient;
	} | null>(null);
	const [state, setState] = useState<ConnectionState>("idle");
	const [error, setError] = useState<string | null>(null);
	const [loading, setLoading] = useState(true);
	const [attempt, setAttempt] = useState(0);
	const [foreground, setForeground] = useState(
		AppState.currentState === "active",
	);
	const [selection] = useState(
		() =>
			new HubSelection(
				repository,
				{
					onProfiles: setProfiles,
					onSelect: setSelected,
					onRetry: () => setAttempt((value) => value + 1),
				},
				Crypto.randomUUID,
			),
	);
	const activeProfile =
		profiles.find((profile) => profile.id === selected) ?? null;
	const activeId = activeProfile?.id;
	const activeOrigin = activeProfile?.origin;
	const client =
		foreground && session?.profileId === selected
			? (session?.client ?? null)
			: null;
	const visibleState = client
		? state
		: activeProfile && foreground
			? "connecting"
			: "idle";
	useEffect(() => {
		let cancelled = false;
		selection
			.load()
			.then((value) => {
				if (!cancelled) {
					try {
						const location = locations.read(value.map((profile) => profile.id));
						setInitialLocation(location);
						selection.restore(location?.hubId ?? null);
					} catch {
						setRestorationError(
							"Your last location could not be restored. Choose a saved hub.",
						);
					}
				}
			})
			.catch(() => {
				if (!cancelled)
					setError("Saved hubs could not be loaded. Try reopening the app.");
			})
			.finally(() => {
				if (!cancelled) setLoading(false);
			});
		const subscription = AppState.addEventListener("change", (next) =>
			setForeground(next === "active"),
		);
		return () => {
			cancelled = true;
			subscription.remove();
		};
	}, [selection]);
	// biome-ignore lint/correctness/useExhaustiveDependencies: The retry counter deliberately reopens the same hub connection.
	useEffect(() => {
		let cancelled = false;
		let connection: AppwireClient | null = null;
		let unsubscribe: (() => void) | undefined;
		setSession(null);
		setState("idle");
		setError(null);
		if (!activeId || !activeOrigin || !foreground) return;
		setState("connecting");
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
				const currentConnection = connection;
				unsubscribe = currentConnection.onStateChange((next) => {
					if (!cancelled) {
						setState(next);
						if (next === "ready") setError(null);
						if (next === "closed")
							setError(
								connectionFailure(currentConnection.terminalReason).message,
							);
					}
				});
				setSession({ profileId: activeId, client: connection });
				return connection.connect();
			})
			.catch(() => {
				if (!cancelled) {
					setError(
						connectionFailure(connection?.terminalReason ?? null).message,
					);
					setState("closed");
					connection?.close();
				}
			});
		return () => {
			cancelled = true;
			unsubscribe?.();
			connection?.close();
		};
	}, [activeId, activeOrigin, foreground, attempt]);
	const selectHub = useCallback(
		(id: string) => selection.select(id),
		[selection],
	);
	const disconnect = useCallback(() => selection.disconnect(), [selection]);
	const retry = useCallback(() => setAttempt((value) => value + 1), []);
	const saveHub = useCallback(
		(input: HubInput) => selection.save(input),
		[selection],
	);
	const updateHub = useCallback(
		(id: string, input: HubUpdate) => selection.update(id, input),
		[selection],
	);
	const removeHub = useCallback(
		(id: string) =>
			selection.remove(id, {
				removeHub(hubId: string) {
					drafts.removeHub(hubId);
					readerPositions.removeHub(hubId);
					removeOrganizationData(hubId);
				},
			}),
		[selection],
	);
	const value = useMemo(
		() => ({
			initialLocation,
			restorationError,
			profiles,
			activeProfile,
			client,
			state: visibleState,
			error,
			loading,
			saveHub,
			updateHub,
			selectHub,
			removeHub,
			disconnect,
			retry,
		}),
		[
			initialLocation,
			restorationError,
			profiles,
			activeProfile,
			client,
			visibleState,
			error,
			loading,
			saveHub,
			updateHub,
			selectHub,
			removeHub,
			disconnect,
			retry,
		],
	);
	return <Context.Provider value={value}>{children}</Context.Provider>;
}
export function useConnection(): Connection {
	const value = useContext(Context);
	if (!value) throw new Error("ConnectionProvider is required.");
	return value;
}
