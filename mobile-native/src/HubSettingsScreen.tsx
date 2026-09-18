import { useFocusEffect } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import * as Crypto from "expo-crypto";
import {
	type ReactNode,
	useCallback,
	useEffect,
	useMemo,
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
	type ConnectionState,
	createHubOverviewStore,
	friendlyErrorMessage,
} from "@evener/appwire-client";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { useConnection } from "./ConnectionProvider";
import { ConnectionStatus } from "./ConnectionStatus";
import { isReady, useConnectionDisplay, useRenderClient, whenReady } from "./connectionDisplay";
import { HubUpgradeSection } from "./HubUpgradeSection";
import { createHubUpgradeController } from "./hubUpgrade";
import { nativeHubUpgradeStorage } from "./nativeHubUpgrade";
import type { Routes } from "./screens";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

type Props = NativeStackScreenProps<Routes, "HubSettings">;
export function HubSettingsScreen({ route, navigation }: Props) {
	const { activeProfile, client, state, fatal, retry } = useConnection();
	const display = useConnectionDisplay(state, fatal);
	// See PluginsScreen.tsx's identical comment: a flap keeps `client` set
	// already; only a manual retry's own token refetch clears it briefly, and
	// the last client this screen had covers that gap too.
	const renderClient = useRenderClient(client);
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
	const ready = isReady(connectionState);
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
	const data = state.data;
	const hub = data?.hub;
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
							if (ready) void state.refresh();
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
						disabled={!ready}
						onStart={whenReady(ready, () => {
							void upgrade.start();
						})}
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
						disabled={state.loading || !ready}
						onPress={whenReady(ready, () => {
							void state.refresh();
						})}
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
