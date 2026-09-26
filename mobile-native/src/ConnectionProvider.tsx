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
import type { AppwireClient, ConnectionState } from "@evener/appwire-client";
import {
	type HubInput,
	type HubProfile,
	HubProfiles,
	type HubUpdate,
} from "./connection";
import { useHubConnection } from "./hubConnection";
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
	fatal: boolean;
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
	const { client, state: visibleState, fatal } = useHubConnection(
		repository,
		activeId,
		activeOrigin,
		foreground,
		attempt,
		setError,
	);
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
			fatal,
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
			fatal,
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
