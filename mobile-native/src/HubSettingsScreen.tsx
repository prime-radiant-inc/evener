import { useFocusEffect } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
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
	RefreshControl,
	ScrollView,
	View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { friendlyErrorMessage } from "../../cmd/evener-hub/frontend/src/protocol/errors";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { useConnection } from "./ConnectionProvider";
import { HubUpgradeSection } from "./HubUpgradeSection";
import { HubOverview } from "./hubOverview";
import { createHubUpgradeController } from "./hubUpgrade";
import { nativeHubUpgradeStorage } from "./nativeHubUpgrade";
import type { Routes } from "./screens";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

type Props = NativeStackScreenProps<Routes, "HubSettings">;
export function HubSettingsScreen({ route, navigation }: Props) {
	const { activeProfile, client, state, retry } = useConnection();
	if (activeProfile?.id !== route.params.hubId)
		return (
			<Copy>This hub is no longer selected. Return to Hubs to reconnect.</Copy>
		);
	if (!client || state !== "ready")
		return (
			<View style={{ padding: 20 }}>
				<Copy>Connect to {activeProfile.name} to view hub settings.</Copy>
				<Action onPress={retry}>Reconnect</Action>
			</View>
		);
	return (
		<HubSettings
			client={client}
			hubId={activeProfile.id}
			hubName={activeProfile.name}
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
function HubSettings({
	client,
	hubId,
	hubName,
	openProviders,
	openPlugins,
	openLaunchSettings,
}: {
	client: ConversationClientLike;
	hubId: string;
	hubName: string;
	openProviders(): void;
	openPlugins(): void;
	openLaunchSettings(): void;
}) {
	const colors = useColors();
	const model = useMemo(() => new HubOverview(client), [client]);
	const state = useSyncExternalStore(model.subscribe, model.getSnapshot);
	const upgrade = useMemo(
		() => createHubUpgradeController(hubId, client, nativeHubUpgradeStorage),
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
			void model.refresh();
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
							void model.refresh();
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
				<Section title="Hub update">
					<HubUpgradeSection
						state={upgradeState}
						hubName={hubName}
						runningIdentity={hub}
						onStart={() => {
							void upgrade.start();
						}}
						onRefresh={() => {
							void upgrade.reconcileAfterReconnect();
						}}
					/>
				</Section>
				{state.loading && !data && (
					<ActivityIndicator accessibilityLabel="Loading hub information" />
				)}
				<ErrorMessage message={state.error} />
				{state.error && (
					<Action
						disabled={state.loading}
						onPress={() => {
							void model.refresh();
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
							<Detail
								label="Default hub configuration location"
								value="~/.config/evener/hub.toml"
							/>
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
