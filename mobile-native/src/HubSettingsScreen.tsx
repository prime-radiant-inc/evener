import { useFocusEffect, useIsFocused } from "@react-navigation/native";
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
	type ConnectionState,
	createHubOverviewStore,
	friendlyErrorMessage,
} from "@evener/appwire-client";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { ConnectionStatus } from "./ConnectionStatus";
import { isReady, whenReady } from "./connectionDisplay";
import { HubUpgradeSection } from "./HubUpgradeSection";
import { createHubUpgradeController } from "./hubUpgrade";
import { nativeHubUpgradeStorage } from "./nativeHubUpgrade";
import {
	ConnectionWall,
	HUB_NO_LONGER_SELECTED,
	useRetainedScreenConnection,
} from "./retainedScreen";
import type { Routes } from "./screens";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

type Props = NativeStackScreenProps<Routes, "HubSettings">;
// A mounted screen re-keyed to another hub is a fresh screen: the
// reconnect-retention state below - the banner's everReady, the last
// client a retry's gap renders through, the recovered-overview read -
// belongs to the hub it was built for, and a re-key remounts the body
// whole (the keyed wrapper's own rationale: useRetainedScreenConnection's
// doc).
export function HubSettingsScreen(props: Props) {
	return <HubSettingsScreenBody key={props.route.params.hubId} {...props} />;
}

function HubSettingsScreenBody({ route, navigation }: Props) {
	const {
		activeProfile,
		state,
		retry,
		error,
		display,
		canUseConnection,
		renderClient,
	} = useRetainedScreenConnection(route.params.hubId);
	if (activeProfile?.id !== route.params.hubId)
		return <Copy>{HUB_NO_LONGER_SELECTED}</Copy>;
	if (display === "wall" || !renderClient)
		return (
			<ConnectionWall
				hubName={activeProfile.name}
				purpose="view hub settings"
				error={error}
				onReconnect={retry}
			/>
		);
	return (
		<>
			{display === "banner" ? <ConnectionStatus /> : null}
			<HubSettings
				client={renderClient}
				connectionState={state}
				canUseConnection={canUseConnection}
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
	canUseConnection,
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
	canUseConnection: () => boolean;
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
	const focused = useIsFocused();
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
	// The two recovery paths below coordinate through one client-generation
	// note. A replacement client that becomes ready while the screen is
	// focused re-runs the focus effect in the same commit the ready
	// transition recovers in - useFocusEffect's callback identity changes
	// with the client - so without the note every retry issued two read-sets:
	// two overview refreshes and two reconciles, the first invalidated by
	// the second (hubUpgrade.ts bumps its generation per reconcile and drops
	// the earlier answer). The transition effect runs first and leaves the
	// note; the focus effect reads it in that same commit and skips, so one
	// event recovers exactly once. A transition the screen is not focused
	// through leaves no note - no focus read is coming to read it - so the
	// next refocus still reads, the way it always has.
	const transitionRecovered = useRef<{
		client: ConversationClientLike;
	} | null>(null);
	const recoveredForClient = useRef<ConversationClientLike | null>(null);
	// useFocusEffect covers a screen the user comes back to; a passive
	// reconnect never refocuses it, and the client a manual retry replaces
	// this one with is still connecting when the focus effect re-runs, so
	// that read fails with nothing left to re-run it once the connection is
	// ready. The overview store keeps the last successful load through a
	// failed refresh (hubOverview.ts), so the banner over stale-but-shown
	// data stays usable meanwhile; this is the recovery read: one refresh and
	// one upgrade reconcile per transition back to ready. The focus read is
	// live-gated, and the live predicate settles in the parent's effect
	// AFTER this screen's own effects run: a mount that is already ready
	// under a pairing the predicate has not settled yet (a re-keyed body
	// remounting under the new hub's fresh client) sees its focus read
	// refused with no ready transition ever coming after it, so the
	// transition arm below reads exactly when the predicate it shares with
	// the focus read refuses - while the screen is focused, authorized here
	// means the focus read is coming in this same commit and refused here
	// means it refuses there too. An unfocused screen has no focus read
	// coming at all: an authorization it holds is not a read anyone owes, so
	// the arm reads for it (round 23). The render gates that let this screen
	// mount are the trust authority for that arm, not the deferred-request
	// predicate.
	useEffect(() => {
		if (connectionState !== "ready") {
			recoveredForClient.current = null;
			return;
		}
		// The gate keys on the client, not the readiness alone: a replacement
		// client can arrive while the state never leaves ready — a state-keyed
		// boolean would stay consumed for it — and its pairing's focus read is
		// refused while it settles, with no later event left to re-run it. Each
		// newly paired ready client is therefore its own recovery event
		// (round 39).
		if (recoveredForClient.current === client) return;
		recoveredForClient.current = client;
		// Only a focused screen's authorization means the focus effect is
		// about to read in this same commit; an unfocused screen's is a
		// pairing nothing else will use until the user comes back.
		if (canUseConnection() && focused) return;
		// A transition the screen is focused through owes the focus path's
		// read too: the client replacement that re-runs the focus effect
		// makes both effects fire in this one commit, and the note is what
		// keeps that to a single read-set.
		if (focused) transitionRecovered.current = { client };
		void model.getState().refresh();
		void upgrade.reconcileAfterReconnect();
	}, [client, connectionState, focused, model, upgrade, canUseConnection]);
	useFocusEffect(
		useCallback(() => {
			// The blur/dep-change cleanup runs before any later callback and
			// clears whatever note a previous commit left, so a note read
			// here is always one this commit's transition just set.
			const clearNote = () => {
				transitionRecovered.current = null;
			};
			if (!canUseConnection()) return clearNote;
			if (transitionRecovered.current?.client === client) {
				transitionRecovered.current = null;
				return clearNote;
			}
			void model.getState().refresh();
			void upgrade.reconcileAfterReconnect();
			return clearNote;
		}, [canUseConnection, client, model, upgrade]),
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
							if (canUseConnection()) void state.refresh();
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
						disabled={!ready}
						onStart={whenReady(canUseConnection, () => {
							void upgrade.start();
						})}
						onRefresh={() => {
							if (canUseConnection()) void upgrade.reconcileAfterReconnect();
						}}
						onReviewAnother={whenReady(canUseConnection, () => {
							void upgrade.reviewAnotherUpdate().then((reviewed) => {
								if (!reviewed) return;
								Alert.alert(
									"Review another update?",
									"The running hub version was refreshed. Confirm to enable another upgrade attempt.",
									[
										{ text: "Cancel", style: "cancel" },
										{
											text: "Continue",
											onPress: () => {
												if (canUseConnection()) upgrade.rearm(reviewed);
											},
										},
									],
								);
							});
						})}
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
						onPress={whenReady(canUseConnection, () => {
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
