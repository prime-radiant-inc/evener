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
} from "../../cmd/evener-hub/frontend/src/protocol/client";
import type { WebSocketLike } from "../../cmd/evener-hub/frontend/src/protocol/transport";
import {
	createHubClient,
	type HubInput,
	type HubProfile,
	HubProfiles,
	type HubUpdate,
} from "./connection";
import { connectionFailure } from "./connectionRecovery";
import type { SavedLocation } from "./location";
import { drafts } from "./nativeDrafts";
import { locations } from "./nativeLocation";
import { removeOrganizationData } from "./nativeOrganization";
import { removeSavedHub } from "./removeHub";

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
	saveHub(input: HubInput): Promise<void>;
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
		repository
			.list()
			.then((value) => {
				if (!cancelled) {
					setProfiles(value);
					try {
						const location = locations.read(value.map((profile) => profile.id));
						setInitialLocation(location);
						setSelected(location?.hubId ?? null);
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
	}, []);
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
	const selectHub = useCallback((id: string) => {
		setSelected(id);
		setAttempt((value) => value + 1);
	}, []);
	const disconnect = useCallback(() => {
		setSelected(null);
	}, []);
	const retry = useCallback(() => setAttempt((value) => value + 1), []);
	const saveHub = useCallback(async (input: HubInput) => {
		const profile = await repository.save({
			...input,
			id: Crypto.randomUUID(),
		});
		setProfiles(await repository.list());
		setSelected(profile.id);
	}, []);
	const updateHub = useCallback(
		async (id: string, input: HubUpdate) => {
			const profile = await repository.update(id, input);
			setProfiles((current) =>
				current.map((item) => (item.id === id ? profile : item)),
			);
			if (input.token !== undefined && selected === id)
				setAttempt((value) => value + 1);
		},
		[selected],
	);
	const removeHub = useCallback(async (id: string) => {
		const result = await removeSavedHub(
			repository,
			{
				removeHub(hubId: string) {
					drafts.removeHub(hubId);
					removeOrganizationData(hubId);
				},
			},
			id,
		);
		setProfiles(
			(current) =>
				result.profiles ??
				(result.removed
					? current.filter((profile) => profile.id !== id)
					: current),
		);
		if (result.removed)
			setSelected((current) => (current === id ? null : current));
		if (result.error) throw new Error(result.error);
	}, []);
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
