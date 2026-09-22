import { useFocusEffect } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import * as Crypto from "expo-crypto";
import {
	type ReactNode,
	useCallback,
	useEffect,
	useMemo,
	useRef,
	useState,
	useSyncExternalStore,
} from "react";
import {
	ActivityIndicator,
	Alert,
	RefreshControl,
	ScrollView,
	View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import {
	type AppwireClient,
	type ConnectionState,
	createHubOverviewStore,
	friendlyErrorMessage,
} from "@evener/appwire-client";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { useConnection } from "./ConnectionProvider";
import { ConnectionStatus } from "./ConnectionStatus";
import { useConnectionDisplay } from "./connectionDisplay";
import { HubUpgradeSection } from "./HubUpgradeSection";
import { createHubUpgradeController } from "./hubUpgrade";
import { nativeHubUpgradeStorage } from "./nativeHubUpgrade";
import type { Routes } from "./screens";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

type Props = NativeStackScreenProps<Routes, "HubSettings">;
// A mounted screen re-keyed to another hub is a fresh screen: the
// reconnect-retention state below - the banner's everReady, the last
// client a retry's gap renders through, the recovered-overview read - belongs
// to the hub it was built for, and none of it may survive a hub the route now
// names. React Navigation can update a mounted instance's params (setParams on
// a focused screen is this app's own idiom - see
// KeybindingPreferencesScreen), so the body is keyed to the hub id and a
// re-key remounts it whole.
export function HubSettingsScreen(props: Props) {
	return <HubSettingsScreenBody key={props.route.params.hubId} {...props} />;
}

function HubSettingsScreenBody({ route, navigation }: Props) {
	const { activeProfile, client, state, fatal, retry } = useConnection();
	const display = useConnectionDisplay(state, fatal);
	// See PluginsScreen.tsx's identical comment: a flap keeps `client` set
	// already; only a manual retry's own token refetch clears it briefly, and
	// the last client this screen had covers that gap too.
	const lastClient = useRef<AppwireClient | null>(null);
	if (client) lastClient.current = client;
	const renderClient = client ?? lastClient.current;
	if (activeProfile?.id !== route.params.hubId)
		return (
			<Copy>This hub is no longer selected. Return to Hubs to reconnect.</Copy>
		);
	if (display === "wall" || !renderClient)
		return (
			<View style={{ padding: 20 }}>
				<Copy>Connect to {activeProfile.name} to view hub settings.</Copy>
				<Action onPress={retry}>Reconnect</Action>
			</View>
		);
	return (
		<>
			{display === "banner" ? <ConnectionStatus /> : null}
			<HubSettings
				client={renderClient}
				connectionState={state}
				hubId={activeProfile.id}
				hubName={activeProfile.name}
				openTranscript={() =>
					navigation.navigate("TranscriptPreferences", {
						hubId: activeProfile.id,
					})
				}
				openKeybindings={() =>
					navigation.navigate("KeybindingPreferences", {
						hubId: activeProfile.id,
					})
				}
				openProviders={() =>
					navigation.navigate("Providers", { hubId: activeProfile.id })
				}
				openLaunchSettings={() =>
					navigation.navigate("LaunchSettings", { hubId: activeProfile.id })
				}
				openPlugins={() =>
					navigation.navigate("Plugins", { hubId: activeProfile.id })
				}
			/>
		</>
	);
}

function Detail({
	label,
	value,
}: {
	label: string;
	value: string | number | undefined;
}) {
	return (
		<View style={{ gap: 2, paddingVertical: 5 }}>
			<Copy muted>{label}</Copy>
			<Copy>
				{value === undefined || value === "" ? "Unavailable" : String(value)}
			</Copy>
		</View>
	);
}
function Section({ title, children }: { title: string; children: ReactNode }) {
	const [open, setOpen] = useState(false);
	const colors = useColors();
	return (
		<View style={{ borderTopWidth: 0.5, borderColor: colors.border }}>
			<Action expanded={open} onPress={() => setOpen(!open)}>
				{title}
			</Action>
			{open && <View style={{ paddingBottom: 14, gap: 8 }}>{children}</View>}
		</View>
	);
}
// The hub overview store keeps the failed request's own text in `error`;
// this screen shows the same copy for every failure, as the web's sections
// translate theirs at render.
const HUB_OVERVIEW_REFRESH_FAILED =
	"Could not refresh hub information. Try again when connected.";

function HubSettings({
	client,
	connectionState,
	hubId,
	hubName,
	openTranscript,
	openKeybindings,
	openProviders,
	openPlugins,
	openLaunchSettings,
}: {
	client: ConversationClientLike;
	connectionState: ConnectionState;
	hubId: string;
	hubName: string;
	openTranscript(): void;
	openKeybindings(): void;
	openProviders(): void;
	openPlugins(): void;
	openLaunchSettings(): void;
}) {
	const colors = useColors();
	const model = useMemo(() => createHubOverviewStore(client), [client]);
	const state = useSyncExternalStore(model.subscribe, model.getState);
	const upgrade = useMemo(
		() =>
			createHubUpgradeController(
				hubId,
				client,
				nativeHubUpgradeStorage,
				Crypto.randomUUID,
			),
		[hubId, client],
	);
	const upgradeState = useSyncExternalStore(
		upgrade.subscribe,
		upgrade.getSnapshot,
	);
	useEffect(() => () => upgrade.dispose(), [upgrade]);
	useEffect(() => () => model.dispose(), [model]);
	useFocusEffect(
		useCallback(() => {
			void model.getState().refresh();
			void upgrade.reconcileAfterReconnect();
		}, [model, upgrade]),
	);
	// useFocusEffect covers a screen the user comes back to; a passive
	// reconnect never refocuses it, and the client a manual retry replaces
	// this one with is still connecting when the focus effect re-runs, so
	// that read fails with nothing left to re-run it once the connection is
	// ready. The overview store keeps the last successful load through a
	// failed refresh (hubOverview.ts), so the banner over stale-but-shown
	// data stays usable meanwhile; this is the recovery read: one refresh and
	// one upgrade reconcile per transition back to ready. The seed counts a
	// mount that is already ready as refreshed: the focus read above is the
	// read it owes, and a ready "transition" that never happened must not
	// fire a second refresh and reconcile on top of it.
	const refreshedAtReady = useRef(connectionState === "ready");
	useEffect(() => {
		if (connectionState !== "ready") {
			refreshedAtReady.current = false;
			return;
		}
		if (refreshedAtReady.current) return;
		refreshedAtReady.current = true;
		void model.getState().refresh();
		void upgrade.reconcileAfterReconnect();
	}, [connectionState, model, upgrade]);
	const data = state.data;
	const hub = data?.hub;
	// The upgrade confirmation can outlive the connection it was opened
	// on, so the callback its alert captured must read readiness when it
	// fires, not through the render that captured it (the PluginsScreen
	// lastClient pattern: a ref mutated during render that never
	// re-renders on its own) - or confirming after a drop persists a
	// checkpoint for an RPC that cannot reach the hub (hubUpgrade.ts's
	// start). A replaced client needs no ref: the old callback reaches the
	// old controller, which the client swap's effect cleanup disposed.
	const readiness = useRef(connectionState);
	readiness.current = connectionState;
	return (
		<SafeAreaView
			edges={["bottom", "left", "right"]}
			style={[styles.fill, { backgroundColor: colors.background }]}
		>
			<ScrollView
				contentContainerStyle={{ padding: 20, gap: 12 }}
				refreshControl={
					<RefreshControl
						refreshing={state.loading && !!data}
						onRefresh={() => {
							void state.refresh();
						}}
					/>
				}
			>
				<Copy>{hubName}</Copy>
				<View style={[styles.row, { flexWrap: "wrap" }]}>
					<Action onPress={openProviders}>Providers</Action>
					<Action onPress={openPlugins}>Plugins</Action>
				</View>
				<Action onPress={openLaunchSettings}>Launch defaults</Action>
				<Action onPress={openTranscript}>Transcript display</Action>
				<Action onPress={openKeybindings}>Keyboard shortcuts</Action>
				<Section title="Hub update">
					<HubUpgradeSection
						state={upgradeState}
						hubName={hubName}
						runningIdentity={hub}
						// The start persists its checkpoint before the RPC leaves
						// (hubUpgrade.ts), so while the connection is away it must
						// not be pressable; the reads it leaves enabled are the
						// recovery path.
						disabled={connectionState !== "ready"}
						onStart={() => {
							if (readiness.current !== "ready") return;
							void upgrade.start();
						}}
						onRefresh={() => {
							void upgrade.reconcileAfterReconnect();
						}}
						onReviewAnother={() => {
							void upgrade.reviewAnotherUpdate().then((reviewed) => {
								if (!reviewed) return;
								Alert.alert(
									"Review another update?",
									"The running hub version was refreshed. Confirm to enable another upgrade attempt.",
									[
										{ text: "Cancel", style: "cancel" },
										{
											text: "Continue",
											onPress: () => upgrade.rearm(reviewed),
										},
									],
								);
							});
						}}
					/>
				</Section>
				{state.loading && !data && (
					<ActivityIndicator accessibilityLabel="Loading hub information" />
				)}
				<ErrorMessage
					message={state.error === null ? null : HUB_OVERVIEW_REFRESH_FAILED}
				/>
				{state.error && (
					<Action
						disabled={state.loading}
						onPress={() => {
							void state.refresh();
						}}
					>
						Retry hub information
					</Action>
				)}
				{data && (
					<>
						<Copy muted>
							{hub?.version ? `Evener ${hub.version}` : "Version unavailable"}
						</Copy>
						<Copy muted>{hub?.listenAddr || "Listen address unavailable"}</Copy>
						<Section title="Runtime and storage">
							<Detail label="Version" value={hub?.version} />
							<Detail label="Commit" value={hub?.commit} />
							<Detail label="Listen address" value={hub?.listenAddr} />
							<Detail label="Spawn timeout" value={hub?.spawnTimeout} />
							<Detail label="Bearer token age" value={hub?.bearerTokenAge} />
							<Detail label="Run directory" value={hub?.runDir} />
							<Detail label="State directory" value={data.storage?.stateDir} />
							<Copy muted>These locations are on the hub.</Copy>
							{hub?.pastIndex && (
								<>
									<Detail
										label="Past session index"
										value={hub.pastIndex.path}
									/>
									<Detail label="Index size" value={hub.pastIndex.size} />
									<Detail
										label="Indexed sessions"
										value={hub.pastIndex.count}
									/>
									<Detail
										label="Past results per page"
										value={hub.pastIndex.perPage}
									/>
								</>
							)}
						</Section>
						<Section title="Agents">
							{data.agents === undefined ? (
								<Copy muted>Agent information unavailable.</Copy>
							) : data.agents.length === 0 ? (
								<Copy muted>No agents discovered.</Copy>
							) : (
								data.agents.map((agent) => (
									<View key={agent.name} style={{ gap: 3 }}>
										<Copy>{agent.name}</Copy>
										<Copy muted>{agent.editPath || "Built-in"}</Copy>
									</View>
								))
							)}
						</Section>
						<Section title="Discovered MCP servers">
							{data.mcpDiscovered === undefined ? (
								<Copy muted>MCP discovery unavailable.</Copy>
							) : (
								<>
									<ErrorMessage
										message={
											data.mcpDiscovered.error
												? friendlyErrorMessage(data.mcpDiscovered.error)
												: null
										}
									/>
									{data.mcpDiscovered.servers?.map((server) => (
										<View key={server.name} style={{ gap: 3 }}>
											<Copy>{server.name}</Copy>
											<Detail label="Transport" value={server.transport} />
											<Detail label="Status" value={server.status} />
											<ErrorMessage
												message={
													server.error
														? friendlyErrorMessage(server.error)
														: null
												}
											/>
										</View>
									))}
									{!data.mcpDiscovered.error &&
										data.mcpDiscovered.servers?.length === 0 && (
											<Copy muted>No MCP servers discovered.</Copy>
										)}
								</>
							)}
						</Section>
					</>
				)}
			</ScrollView>
		</SafeAreaView>
	);
}
