import * as Crypto from "expo-crypto";
import * as SecureStore from "expo-secure-store";
import { Storage } from "expo-sqlite/kv-store";
import { createContext, type ReactNode, useCallback, useContext, useEffect, useMemo, useState } from "react";
import { AppState } from "react-native";
import type { AppwireClient, ConnectionState } from "@evener/appwire-client";
import { forgetBoardForHub } from "./board/nativeBoardMemory";
import { ConnectionClock, type ConnectionTimes } from "./connectionClock";
import { type HubInput, type HubProfile, HubProfiles, type HubUpdate } from "./connection";
import { runHubCleanups } from "./hubCleanups";
import { useHubConnection } from "./hubConnection";
import { HubSelection } from "./hubSelection";
import type { SavedLocation } from "./location";
import { drafts } from "./nativeDrafts";
import { locations } from "./nativeLocation";
import { removeOrganizationData } from "./nativeOrganization";
import { readerPositions } from "./nativeReaderPosition";
import { forgetDocumentSummaries } from "./reader/documentSummaries";
import { forgetDocumentsForHub } from "./reader/nativeDocumentMemory";
import { forgetCreationForHub, releaseCreations } from "./newSession/creations";
import { forgetLaunchMemoryForHub } from "./newSession/nativeLaunchMemory";
import { forgetStopRequestsForHub } from "./subagents/nativeStopRequests";
import { forgetSubagentTrees } from "./subagents/subagentTree";
import { forgetDetailLevelsForHub } from "./session/nativeDetailLevels";
import { forgetNoteDrafts } from "./session/sessionNotes";

const repository = new HubProfiles(SecureStore);
interface Connection {
	initialLocation: SavedLocation | null;
	restorationError: string | null;
	profiles: HubProfile[];
	activeProfile: HubProfile | null;
	client: AppwireClient | null;
	state: ConnectionState;
	fatal: boolean;
	/** When this stretch in front without a live connection began (spec 14's
	 * status clock); null while live or in the background. */
	downSince: number | null;
	/** When the connection's data was last live, background included; null
	 * until it has been since launch or since the hub was chosen. */
	lastLiveAt: number | null;
	error: string | null;
	loading: boolean;
	saveHub(input: HubInput): Promise<boolean>;
	updateHub(id: string, input: HubUpdate): Promise<void>;
	selectHub(id: string): void;
	removeHub(id: string): Promise<void>;
	disconnect(): void;
}
const Context = createContext<Connection | null>(null);

export function ConnectionProvider({ children }: { children: ReactNode }) {
	const [initialLocation, setInitialLocation] = useState<SavedLocation | null>(null);
	const [restorationError, setRestorationError] = useState<string | null>(null);
	const [profiles, setProfiles] = useState<HubProfile[]>([]);
	const [selected, setSelected] = useState<string | null>(null);
	const [error, setError] = useState<string | null>(null);
	const [loading, setLoading] = useState(true);
	const [attempt, setAttempt] = useState(0);
	const [foreground, setForeground] = useState(AppState.currentState === "active");
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
	const activeProfile = profiles.find((profile) => profile.id === selected) ?? null;
	const activeId = activeProfile?.id;
	const activeOrigin = activeProfile?.origin;
	const {
		client,
		state: visibleState,
		fatal,
	} = useHubConnection(repository, activeId, activeOrigin, foreground, attempt, setError);
	// A New session start bound to a client the connection has left (another
	// hub, a new connection, or none after disconnecting) is let go, so it
	// reads as uncertain rather than starting forever (#3104).
	useEffect(() => releaseCreations(client), [client]);
	// The status clock (spec 14): fed every change in hub, liveness and
	// foreground, read by useConnectionStatusText wherever a status shows.
	const [clock] = useState(() => new ConnectionClock());
	const [times, setTimes] = useState<ConnectionTimes>({ downSince: null, lastLiveAt: null });
	useEffect(() => {
		setTimes(clock.observe({ hubId: activeId ?? null, live: visibleState === "ready", foreground }, Date.now()));
	}, [clock, activeId, visibleState, foreground]);
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
						setRestorationError("Your last location could not be restored. Choose a saved hub.");
					}
				}
			})
			.catch(() => {
				if (!cancelled) setError("Saved hubs could not be loaded. Try reopening the app.");
			})
			.finally(() => {
				if (!cancelled) setLoading(false);
			});
		const subscription = AppState.addEventListener("change", (next) => setForeground(next === "active"));
		return () => {
			cancelled = true;
			subscription.remove();
		};
	}, [selection]);
	const selectHub = useCallback((id: string) => selection.select(id), [selection]);
	const disconnect = useCallback(() => selection.disconnect(), [selection]);
	const saveHub = useCallback((input: HubInput) => selection.save(input), [selection]);
	const updateHub = useCallback((id: string, input: HubUpdate) => selection.update(id, input), [selection]);
	const removeHub = useCallback(
		(id: string) =>
			selection.remove(id, {
				removeHub(hubId: string) {
					runHubCleanups(hubId, [
						(id) => drafts.removeHub(id),
						(id) => readerPositions.removeHub(id),
						removeOrganizationData,
						forgetBoardForHub,
						forgetDetailLevelsForHub,
						(id) => forgetNoteDrafts(Storage, id),
						forgetDocumentsForHub,
						forgetDocumentSummaries,
						forgetSubagentTrees,
						forgetLaunchMemoryForHub,
						forgetCreationForHub,
						forgetStopRequestsForHub,
					]);
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
			downSince: times.downSince,
			lastLiveAt: times.lastLiveAt,
			error,
			loading,
			saveHub,
			updateHub,
			selectHub,
			removeHub,
			disconnect,
		}),
		[
			initialLocation,
			restorationError,
			profiles,
			activeProfile,
			client,
			visibleState,
			fatal,
			times,
			error,
			loading,
			saveHub,
			updateHub,
			selectHub,
			removeHub,
			disconnect,
		],
	);
	return <Context.Provider value={value}>{children}</Context.Provider>;
}
export function useConnection(): Connection {
	const value = useContext(Context);
	if (!value) throw new Error("ConnectionProvider is required.");
	return value;
}
