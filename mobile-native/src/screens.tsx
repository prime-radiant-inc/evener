import type { CellRendererProps } from "@react-native/virtualized-lists";
import { useHeaderHeight } from "@react-navigation/elements";
import { useFocusEffect, useIsFocused } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import * as Clipboard from "expo-clipboard";
import {
	Component,
	useCallback,
	useEffect,
	useMemo,
	useRef,
	useState,
	useSyncExternalStore,
} from "react";
import {
	AccessibilityInfo,
	ActivityIndicator,
	Alert,
	AppState,
	FlatList,
	Keyboard,
	KeyboardAvoidingView,
	Platform,
	Pressable,
	ScrollView,
	Text,
	TextInput,
	useWindowDimensions,
	View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import type { AskBatch } from "../../cmd/evener-hub/frontend/src/panes/session/composer/askDock/reconcileBatches";
import {
	parseSlashToken,
	spliceSlashCommand,
} from "../../cmd/evener-hub/frontend/src/panes/session/composer/slashCompletion";
import { humanizeState } from "../../cmd/evener-hub/frontend/src/shell/rail/sessionState";
import { buildComposerInput } from "../../cmd/evener-hub/frontend/src/stores/composerInput";
import { createConversationService } from "../../mobile/src/services/conversation";
import { createRosterService } from "../../mobile/src/services/roster";
import { createActivityStore } from "../../mobile/src/state/activity";
import { createConversationStore } from "../../mobile/src/state/conversation";
import { ActivitySheet } from "./ActivitySheet";
import { ApprovalSheet } from "./ApprovalSheet";
import { ApprovalControls } from "./approvalControls";
import { CommandCompletion } from "./CommandCompletion";
import { type ComposerSetting, ComposerSettings } from "./ComposerSettings";
import { ComposerSettingsSheet } from "./ComposerSettingsSheet";
import { useConnection } from "./ConnectionProvider";
import {
	CommandArgumentError,
	composerCommand,
	isLocalComposerCommand,
	submitComposerCommand,
} from "./composerCommand";
import { steerComposer } from "./composerSteering";
import type { HubProfile } from "./connection";
import { goalObjective, submitGoalCommand } from "./goalCommand";
import { HubEditor } from "./HubEditor";
import { ImageAttachments } from "./ImageAttachments";
import { ImageSelection } from "./imageSelection";
import { useNativePreferences } from "./NativePreferencesProvider";
import { drafts } from "./nativeDrafts";
import { nativeImagePicker } from "./nativeImagePicker";
import { readerPositions } from "./nativeReaderPosition";
import { locateSession, type SessionLocation } from "./navigationReveal";
import { QuestionSheet } from "./QuestionSheet";
import { QueueSheet } from "./QueueSheet";
import {
	composeQuestionAnswers,
	pendingQuestions,
	type QuestionSelections,
} from "./questionAnswers";
import { QuestionBatches } from "./questionBatches";
import {
	captureReaderAnchor,
	isReaderAnchorLoaded,
	type ReaderAnchor,
	type ReaderMeasurement,
	readerKey,
	resolveReaderAnchor,
	restoreReaderCommand,
	shouldApplyExactRestore,
} from "./readerPosition";
import { RosterSearch } from "./rosterSearch";
import { type SessionDestination, SessionMenu } from "./SessionMenu";
import { SessionSheet } from "./SessionSheet";
import { SessionControls } from "./sessionControls";
import { localSessionId } from "./sessionDeletionResult";
import { TasksSheet } from "./TasksSheet";
import { TimelineItem } from "./TimelineItem";
import { TranscriptUsage } from "./TranscriptUsage";
import { groupTimeline, type TimelineRow, timelineGap } from "./timeline";
import { projectNativeTranscript } from "./transcriptPresentation";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

const NATIVE_ROSTER_PAGE_SIZE = 50;
const noControls = () => null;
const noControlSubscription = () => () => {};

export type Routes = {
	SessionDeletion: { hubId: string; ref: string; title: string };
	Fork: {
		hubId: string;
		ref: string;
		title: string;
		instanceId: string;
		entryIndex: number;
		preview: string;
	};
	PinSections: { hubId: string };
	PinnedSection: { hubId: string; sectionId: string; title: string };
	PinSectionEditor: { hubId: string; sectionId: string; title: string };
	PinAssignment: { hubId: string; ref: string; title: string };
	SessionLocation: { hubId: string; location: SessionLocation };
	Projects: { hubId: string };
	Providers: { hubId: string };
	Plugins: { hubId: string };
	HubSettings: { hubId: string };
	TranscriptPreferences: { hubId: string };
	LaunchSettings: { hubId: string; projectCwd?: string };
	Project: { hubId: string; projectKey: string; title: string };
	Hubs: undefined;
	Sessions: undefined;
	NewSession: { hubId: string; hubName: string };
	Conversation: { hubId: string; ref: string; title: string };
};

function ConnectionStatus() {
	const { state, error, retry, activeProfile } = useConnection();
	return (
		<View style={{ paddingHorizontal: 16 }}>
			<View style={styles.row}>
				<View style={styles.fill}>
					<Copy muted>
						{activeProfile?.name ?? "No hub selected"} ·{" "}
						{state === "ready" ? "Connected" : state}
					</Copy>
				</View>
				{state !== "ready" ? <Action onPress={retry}>Reconnect</Action> : null}
			</View>
			<ErrorMessage message={error} />
		</View>
	);
}

export function HubsScreen({
	navigation,
}: NativeStackScreenProps<Routes, "Hubs">) {
	const {
		profiles,
		activeProfile,
		saveHub,
		updateHub,
		selectHub,
		removeHub,
		loading,
	} = useConnection();
	const colors = useColors();
	const headerHeight = useHeaderHeight();
	const [editing, setEditing] = useState<HubProfile | null>(null);
	const [name, setName] = useState("");
	const [origin, setOrigin] = useState("");
	const [token, setToken] = useState("");
	const [saving, setSaving] = useState(false);
	const [error, setError] = useState<string | null>(null);
	const inputStyle = [
		styles.input,
		{
			color: colors.text,
			borderColor: colors.border,
			backgroundColor: colors.surface,
		},
	];
	async function save() {
		if (saving) return;
		setSaving(true);
		setError(null);
		try {
			await saveHub({ name, origin, token });
			setName("");
			setOrigin("");
			setToken("");
			navigation.navigate("Sessions");
		} catch {
			setError(
				"Could not save this hub. Check the name and http(s) origin, and try again.",
			);
		} finally {
			setSaving(false);
		}
	}
	function remove(id: string, label: string) {
		Alert.alert(
			`Remove ${label}?`,
			"The saved hub, its credentials, and its local drafts will be removed from this device.",
			[
				{ text: "Cancel", style: "cancel" },
				{
					text: "Remove",
					style: "destructive",
					onPress: () => {
						void removeHub(id).catch((error: unknown) =>
							setError(
								error instanceof Error
									? error.message
									: "Could not remove this hub. Try again.",
							),
						);
					},
				},
			],
		);
	}
	return (
		<SafeAreaView
			edges={["bottom", "left", "right"]}
			style={[styles.fill, { backgroundColor: colors.background }]}
		>
			{editing ? (
				<HubEditor
					profile={editing}
					save={updateHub}
					close={() => setEditing(null)}
				/>
			) : null}
			<KeyboardAvoidingView
				style={styles.fill}
				behavior={Platform.OS === "ios" ? "padding" : "height"}
				keyboardVerticalOffset={headerHeight}
			>
				<ScrollView
					keyboardShouldPersistTaps="handled"
					contentContainerStyle={styles.padded}
				>
					<Text style={[styles.title, { color: colors.text }]}>Saved hubs</Text>
					{loading ? (
						<ActivityIndicator accessibilityLabel="Loading saved hubs" />
					) : profiles.length === 0 ? (
						<Copy muted>Add a hub to browse your sessions.</Copy>
					) : null}
					{profiles.map((profile) => (
						<View
							key={profile.id}
							style={[
								styles.card,
								{ borderColor: colors.border, backgroundColor: colors.surface },
							]}
						>
							<Pressable
								accessibilityRole="button"
								accessibilityLabel={`Open ${profile.name}`}
								onPress={() => {
									selectHub(profile.id);
									navigation.navigate("Sessions");
								}}
								style={{ gap: 8, minHeight: 44 }}
							>
								<Copy>
									{profile.name}
									{activeProfile?.id === profile.id ? " · Selected" : ""}
								</Copy>
								<Copy muted>{profile.origin}</Copy>
							</Pressable>
							<View
								style={[
									styles.row,
									{ justifyContent: "flex-end", flexWrap: "wrap" },
								]}
							>
								<Action
									onPress={() => setEditing(profile)}
									label={`Edit ${profile.name}`}
								>
									Edit
								</Action>
								<Action
									onPress={() => remove(profile.id, profile.name)}
									label={`Remove ${profile.name}`}
								>
									Remove
								</Action>
							</View>
						</View>
					))}
					<Text style={[styles.title, { color: colors.text, marginTop: 16 }]}>
						Add hub
					</Text>
					<Copy muted>
						Enter the hub’s origin, such as https://hub.example.com:9180.
					</Copy>
					<TextInput
						accessibilityLabel="Hub name"
						placeholder="Hub name"
						placeholderTextColor={colors.secondary}
						value={name}
						onChangeText={setName}
						style={inputStyle}
					/>
					<TextInput
						accessibilityLabel="Hub origin"
						placeholder="https://hub.example.com:9180"
						placeholderTextColor={colors.secondary}
						value={origin}
						onChangeText={setOrigin}
						autoCapitalize="none"
						autoCorrect={false}
						keyboardType="url"
						style={inputStyle}
					/>
					<TextInput
						accessibilityLabel="Bearer token, optional"
						placeholder="Bearer token (optional)"
						placeholderTextColor={colors.secondary}
						value={token}
						onChangeText={setToken}
						autoCapitalize="none"
						autoCorrect={false}
						secureTextEntry
						style={inputStyle}
					/>
					<ErrorMessage message={error} />
					<Action
						onPress={() => {
							void save();
						}}
						disabled={saving || loading || !name.trim() || !origin.trim()}
					>
						{saving ? "Saving…" : "Save and connect"}
					</Action>
				</ScrollView>
			</KeyboardAvoidingView>
		</SafeAreaView>
	);
}

export function SessionsScreen({
	navigation,
}: NativeStackScreenProps<Routes, "Sessions">) {
	const { activeProfile, client, state } = useConnection();
	const colors = useColors();
	const { fontScale } = useWindowDimensions();
	const [searchText, setSearchText] = useState("");
	const roster = useMemo(
		() =>
			new RosterSearch(
				client ? createRosterService(client, NATIVE_ROSTER_PAGE_SIZE) : null,
			),
		[client],
	);
	const {
		rows,
		loading: refreshing,
		error,
		hasMore,
		query,
	} = useSyncExternalStore(roster.subscribe, roster.getSnapshot);
	const refresh = useCallback(async () => {
		if (state === "ready") await roster.load();
	}, [roster, state]);
	// biome-ignore lint/correctness/useExhaustiveDependencies: A new connection starts with an unfiltered roster.
	useEffect(() => {
		setSearchText("");
	}, [roster]);
	useFocusEffect(
		useCallback(() => {
			void refresh();
			return () => roster.cancel();
		}, [refresh, roster]),
	);
	function search(value: string) {
		if (state !== "ready") return;
		Keyboard.dismiss();
		void roster.load(value);
	}
	useEffect(() => {
		const openHubs = () => navigation.popToTop();
		const openNew = () => {
			if (activeProfile)
				navigation.navigate("NewSession", {
					hubId: activeProfile.id,
					hubName: activeProfile.name,
				});
		};
		navigation.setOptions({
			unstable_headerLeftItems: () => [
				{ type: "button", label: "Hubs", onPress: openHubs },
			],
			unstable_headerRightItems: () => [
				{
					type: "button",
					label: "New session",
					disabled: !activeProfile || state !== "ready",
					onPress: openNew,
				},
			],
			headerLeft: () => <Action onPress={openHubs}>Hubs</Action>,
			headerRight: () => (
				<Action
					label="New session"
					disabled={!activeProfile || state !== "ready"}
					onPress={openNew}
				>
					{fontScale > 1.4 ? "New" : "New session"}
				</Action>
			),
		});
	}, [navigation, activeProfile, state, fontScale]);
	return (
		<SafeAreaView
			edges={["bottom", "left", "right"]}
			style={[styles.fill, { backgroundColor: colors.background }]}
		>
			<FlatList
				ListHeaderComponent={
					<View style={{ marginBottom: 4 }}>
						<ConnectionStatus />
						<View style={[styles.row, { flexWrap: "wrap" }]}>
							<Action
								disabled={!activeProfile || state !== "ready"}
								onPress={() => {
									if (activeProfile)
										navigation.navigate("HubSettings", {
											hubId: activeProfile.id,
										});
								}}
							>
								Hub settings
							</Action>
							<Action
								disabled={!activeProfile || state !== "ready"}
								onPress={() => {
									if (activeProfile)
										navigation.navigate("Projects", {
											hubId: activeProfile.id,
										});
								}}
							>
								Browse projects
							</Action>
							<Action
								disabled={!activeProfile}
								onPress={() => {
									if (activeProfile)
										navigation.navigate("PinSections", {
											hubId: activeProfile.id,
										});
								}}
							>
								Pinned sections
							</Action>
						</View>
						<View style={{ paddingHorizontal: 20, paddingBottom: 8, gap: 4 }}>
							<View style={[styles.row, { flexWrap: "wrap" }]}>
								<TextInput
									accessibilityLabel="Search sessions"
									placeholder="Search sessions"
									placeholderTextColor={colors.secondary}
									value={searchText}
									onChangeText={setSearchText}
									onSubmitEditing={() => search(searchText)}
									returnKeyType="search"
									autoCapitalize="none"
									autoCorrect={false}
									style={[
										styles.input,
										styles.fill,
										{
											minWidth: fontScale > 1.4 ? "100%" : "50%",
											color: colors.text,
											borderColor: colors.border,
											backgroundColor: colors.surface,
										},
									]}
								/>
								<Action
									disabled={state !== "ready"}
									onPress={() => search(searchText)}
								>
									Search
								</Action>
								{searchText || query ? (
									<Action
										disabled={state !== "ready"}
										onPress={() => {
											setSearchText("");
											search("");
										}}
									>
										Clear
									</Action>
								) : null}
							</View>
							{query ? <Copy muted>{`Results for “${query}”`}</Copy> : null}
						</View>
						<ErrorMessage message={error} />
						{error ? (
							<Action
								disabled={state !== "ready" || refreshing}
								onPress={() => {
									void refresh();
								}}
							>
								Retry sessions
							</Action>
						) : null}
					</View>
				}
				keyboardShouldPersistTaps="handled"
				data={rows}
				keyExtractor={(item) => item.ref}
				refreshing={refreshing}
				onRefresh={() => {
					void refresh();
				}}
				contentContainerStyle={{ paddingBottom: 16, gap: 12 }}
				ListEmptyComponent={
					<View style={{ paddingHorizontal: 16 }}>
						<Copy muted>
							{refreshing
								? "Loading sessions…"
								: state !== "ready"
									? "Connect to the hub to load sessions."
									: error
										? "Pull down to retry loading sessions."
										: query
											? "No sessions match your search."
											: "No sessions on this hub yet."}
						</Copy>
					</View>
				}
				ListFooterComponent={
					hasMore ? (
						<View style={{ paddingHorizontal: 16 }}>
							<Copy muted>
								Showing up to {NATIVE_ROSTER_PAGE_SIZE} sessions. Narrow your
								search to find others.
							</Copy>
						</View>
					) : null
				}
				renderItem={({ item }) => {
					const stateLabel = humanizeState(
						item.status,
						item.askPending === true,
					);
					const signals = [
						["active", "awaiting", "warning", "errored"].includes(item.status)
							? stateLabel
							: "",
						item.askPending && item.status !== "awaiting"
							? humanizeState("awaiting", true)
							: "",
					].filter(Boolean);
					const metadataStyle = {
						fontSize: 13 * (Platform.OS === "ios" ? fontScale : 1),
						lineHeight: 19 * (Platform.OS === "ios" ? fontScale : 1),
					};
					return (
						<Pressable
							accessibilityRole="button"
							accessibilityLabel={`Open ${item.title || "Untitled session"}`}
							accessibilityHint={[
								...(signals.length ? signals : [stateLabel]),
								item.project,
							]
								.filter(Boolean)
								.join(". ")}
							disabled={state !== "ready" || !activeProfile}
							onPress={() => {
								if (activeProfile)
									navigation.navigate("Conversation", {
										hubId: activeProfile.id,
										ref: item.ref,
										title: item.title,
									});
							}}
							style={[
								{
									marginHorizontal: 16,
									paddingVertical: 13,
									minHeight: Platform.OS === "android" ? 72 : 68,
									borderBottomWidth: 0.5,
									borderColor: colors.border,
									gap: 4,
								},
							]}
						>
							<Text
								allowFontScaling={Platform.OS !== "ios"}
								numberOfLines={2}
								style={{
									color: colors.text,
									fontSize: 17 * (Platform.OS === "ios" ? fontScale : 1),
									fontWeight: "500",
								}}
							>
								{item.title || "Untitled session"}
							</Text>
							{signals.length ? (
								<Text
									allowFontScaling={Platform.OS !== "ios"}
									style={[
										metadataStyle,
										{
											color:
												item.attention === "needsYou"
													? colors.accent
													: colors.secondary,
										},
									]}
								>
									{signals.join(" · ")}
								</Text>
							) : null}
							{item.project ? (
								<Text
									allowFontScaling={Platform.OS !== "ios"}
									numberOfLines={fontScale > 1.4 ? 2 : 1}
									ellipsizeMode={fontScale > 1.4 ? "tail" : "middle"}
									style={[metadataStyle, { color: colors.secondary }]}
								>
									{item.project}
								</Text>
							) : null}
						</Pressable>
					);
				}}
			/>
		</SafeAreaView>
	);
}

export function ConversationScreen({
	route,
	navigation,
}: NativeStackScreenProps<Routes, "Conversation">) {
	const { activeProfile, client, state: connectionState } = useConnection();
	const focused = useIsFocused();
	const colors = useColors();
	const { fontScale, height: windowHeight } = useWindowDimensions();
	const [viewportHeight, setViewportHeight] = useState(windowHeight);
	const [sessionMenuOpen, setSessionMenuOpen] = useState(false);
	const headerHeight = useHeaderHeight();
	// biome-ignore lint/correctness/useExhaustiveDependencies: Each route destination owns an independent conversation binding.
	const store = useMemo(
		() => createConversationStore(),
		[route.params.hubId, route.params.ref],
	);
	// biome-ignore lint/correctness/useExhaustiveDependencies: Activity lifetime follows its conversation binding.
	const activity = useMemo(() => createActivityStore(), [store]);
	// The conversation store validates the exact bound sink object on refresh.
	const activitySink = useMemo(() => activity.getState(), [activity]);
	const service = useMemo(
		() => (client ? createConversationService(client) : null),
		[client],
	);
	const snapshot = store();
	const timeline = useRef<FlatList>(null);
	const readerMeasurements = useRef(new Map<string, ReaderMeasurement>());
	const [layoutRevision, setLayoutRevision] = useState(0);
	const readerAnchor = useRef<ReaderAnchor | null>(null);
	const appliedReaderRestore = useRef<{
		key: string;
		y: number;
		height: number;
		offset: number;
	} | null>(null);
	const approximateReaderRestore = useRef<number | null>(null);
	const restoreRetries = useRef(0);
	const readerPageAttempts = useRef(new Set<string>());
	const readerHeader = useRef(false);
	const readerLatest = useRef(false);
	const captureSuppressed = useRef(false);
	const readerDragging = useRef(false);
	const readerMomentum = useRef(false);
	const restoreFrame = useRef<number | null>(null);
	const composerInput = useRef<TextInput>(null);
	const [composerSelection, setComposerSelection] = useState({
		start: 0,
		end: 0,
	});
	const [completionClosedAt, setCompletionClosedAt] = useState<string | null>(
		null,
	);
	const focusAfterModal = useRef(false);
	useEffect(() => {
		const subscription = AppState.addEventListener("focus", () => {
			if (focusAfterModal.current && navigation.isFocused()) {
				focusAfterModal.current = false;
				composerInput.current?.focus();
			}
		});
		return () => subscription.remove();
	}, [navigation]);
	const [refreshing, setRefreshing] = useState(false);
	const [queueOpen, setQueueOpen] = useState(false);
	const [sessionOpen, setSessionOpen] = useState(false);
	const [taskContext, setTaskContext] = useState<{
		hubId: string;
		ref: string;
		threadId: string;
		hasTasks: boolean;
		hubName: string;
		client: NonNullable<typeof client>;
	} | null>(null);
	const [activityContext, setActivityContext] = useState<{
		hubId: string;
		ref: string;
		threadId: string;
		hubName: string;
		client: NonNullable<typeof client>;
	} | null>(null);
	const [approvalsOpen, setApprovalsOpen] = useState(false);
	const [questionsOpen, setQuestionsOpen] = useState(false);
	// biome-ignore lint/correctness/useExhaustiveDependencies: Question ownership follows the destination store.
	const questionBatches = useMemo(() => new QuestionBatches(), [store]);
	const batches = useSyncExternalStore(
		questionBatches.subscribe,
		questionBatches.getSnapshot,
	);
	useEffect(() => {
		const reconcile = () =>
			questionBatches.reconcile(
				pendingQuestions(store.getState().conversation),
			);
		reconcile();
		return store.subscribe(reconcile);
	}, [store, questionBatches]);
	const [composerSetting, setComposerSetting] =
		useState<ComposerSetting | null>(null);
	useFocusEffect(
		useCallback(
			() => () => {
				setQueueOpen(false);
				setSessionOpen(false);
				setApprovalsOpen(false);
				setQuestionsOpen(false);
				setComposerSetting(null);
			},
			[],
		),
	);
	const [actionError, setActionError] = useState<string | null>(null);
	const [commandPending, setCommandPending] = useState(false);
	const commandBusy = useRef(false);
	const projectLookup = useRef<AbortController | null>(null);
	const document = useMemo(
		() =>
			drafts.open({ hubId: route.params.hubId, sessionRef: route.params.ref }),
		[route.params.hubId, route.params.ref],
	);
	const draft = useSyncExternalStore(document.subscribe, document.getSnapshot);
	const imageSelection = useMemo(
		() => new ImageSelection(document, nativeImagePicker),
		[document],
	);
	const imageState = useSyncExternalStore(
		imageSelection.subscribe,
		imageSelection.getSnapshot,
	);
	useFocusEffect(
		useCallback(
			() => () => {
				imageSelection.cancel();
				projectLookup.current?.abort();
			},
			[imageSelection],
		),
	);
	const unconfirmedSend = draft.submitting ? null : draft.record.unconfirmed;
	const connected =
		connectionState === "ready" && activeProfile?.id === route.params.hubId;
	// Thread reads replace the connection's subscription. Returning from a
	// child or editor must reacquire this screen's stream and current snapshot.
	useEffect(() => {
		if (!service || !connected || !focused) return;
		void store
			.getState()
			.openProjected(service, activitySink, route.params.ref);
		return () => {
			store.getState().close();
			service.close();
		};
	}, [service, store, activitySink, connected, focused, route.params.ref]);
	async function refresh() {
		if (!service || !connected || refreshing) return;
		setRefreshing(true);
		try {
			if (store.getState().status === "open")
				await store.getState().rehydrate(service, activitySink);
			else {
				await store
					.getState()
					.openProjected(service, activitySink, route.params.ref);
			}
		} finally {
			setRefreshing(false);
		}
	}
	const connectionReady = useRef(connected);
	connectionReady.current = connected;
	const bindingGeneration = snapshot.conversationGeneration;
	const bindingInstance = snapshot.conversation?.instanceId;
	function forkMessage(entryIndex: number, preview: string) {
		const current = store.getState();
		if (
			!connectionReady.current ||
			!navigation.isFocused() ||
			current.status !== "open" ||
			!current.conversation?.capabilities?.forkFromTurn ||
			!bindingInstance ||
			current.conversation.instanceId !== bindingInstance ||
			current.conversationGeneration !== bindingGeneration ||
			!Number.isSafeInteger(entryIndex) ||
			entryIndex <= 0
		)
			return;
		Keyboard.dismiss();
		navigation.navigate("Fork", {
			...route.params,
			instanceId: bindingInstance,
			entryIndex,
			preview,
		});
	}
	const controls = useMemo(
		() =>
			service && connected && focused
				? new SessionControls(
						service,
						async () => {
							await store.getState().rehydrate(service, activitySink);
							const current = store.getState();
							if (current.status !== "open" || current.error)
								throw new Error("Session refresh failed");
						},
						() => {
							store.getState().close();
							service.close();
							setSessionOpen(false);
							if (navigation.isFocused()) navigation.goBack();
						},
						() => {
							const current = store.getState();
							return (
								connectionReady.current &&
								navigation.isFocused() &&
								current.status === "open" &&
								current.conversationGeneration === bindingGeneration &&
								current.conversation?.instanceId === bindingInstance
							);
						},
						() => store.getState().conversation,
						() =>
							!commandBusy.current &&
							!document.getSnapshot().submitting &&
							store.getState().pendingMutation?.status !== "pending",
					)
				: null,
		[
			service,
			store,
			activitySink,
			navigation,
			connected,
			focused,
			bindingGeneration,
			bindingInstance,
			document,
		],
	);
	useEffect(() => () => controls?.dispose(), [controls]);
	const approvalControls = useMemo(
		() =>
			client && service && connected && focused
				? new ApprovalControls(
						client,
						route.params.ref,
						() => store.getState().conversation?.pendingApprovals ?? [],
						() =>
							connectionReady.current &&
							navigation.isFocused() &&
							store.getState().status === "open" &&
							store.getState().conversationGeneration === bindingGeneration &&
							store.getState().conversation?.instanceId === bindingInstance,
						async () => {
							await store.getState().rehydrate(service, activitySink);
							if (store.getState().error) throw new Error("Refresh failed");
						},
					)
				: null,
		[
			client,
			service,
			connected,
			focused,
			route.params.ref,
			store,
			navigation,
			bindingGeneration,
			bindingInstance,
			activitySink,
		],
	);
	useEffect(() => () => approvalControls?.dispose(), [approvalControls]);

	const hasConversation = snapshot.conversation !== null;
	const deletionAvailable =
		!!localSessionId(route.params.ref) &&
		snapshot.conversation?.status === "notLoaded";
	const openSessionDestination = useCallback(
		(destination: SessionDestination) => {
			setSessionMenuOpen(false);
			Keyboard.dismiss();
			const current = store.getState().conversation;
			if (!current) return;
			if (destination === "delete") {
				if (!localSessionId(route.params.ref) || current.status !== "notLoaded")
					return;
				navigation.navigate("SessionDeletion", {
					hubId: route.params.hubId,
					ref: route.params.ref,
					title: current.name ?? route.params.title,
				});
				return;
			}
			if (destination === "pin") {
				navigation.navigate("PinAssignment", {
					hubId: route.params.hubId,
					ref: route.params.ref,
					title: route.params.title,
				});
				return;
			}
			if (destination === "session") {
				setSessionOpen(true);
				return;
			}
			if (!client || !connected) return;
			const context = {
				hubId: route.params.hubId,
				ref: route.params.ref,
				threadId: current.id,
				hubName: activeProfile?.name ?? "Hub",
				client,
			};
			if (destination === "tasks")
				setTaskContext({ ...context, hasTasks: current.tasks != null });
			else setActivityContext(context);
		},
		[
			store,
			client,
			connected,
			route.params.hubId,
			route.params.ref,
			activeProfile?.name,
			navigation,
			route.params.title,
		],
	);
	useEffect(() => {
		navigation.setOptions({
			unstable_headerRightItems: () => [
				{
					type: "menu",
					label: "Session actions",
					accessibilityLabel: "Session actions",
					icon: { type: "sfSymbol", name: "ellipsis" },
					disabled: !hasConversation,
					menu: {
						items: [
							...(deletionAvailable
								? [
										{
											type: "action" as const,
											label: "Delete saved session",
											onPress: () => openSessionDestination("delete"),
										},
									]
								: []),
							{
								type: "action",
								label: "Session details",
								onPress: () => openSessionDestination("session"),
							},
							{
								type: "action",
								label: "Pin to section",
								onPress: () => openSessionDestination("pin"),
							},
							{
								type: "action",
								label: "Tasks",
								disabled: !connected,
								onPress: () => openSessionDestination("tasks"),
							},
							{
								type: "action",
								label: "Activity",
								disabled: !connected,
								onPress: () => openSessionDestination("activity"),
							},
						],
					},
				},
			],
			headerRight: () => (
				<Pressable
					accessibilityRole="button"
					accessibilityLabel="Session actions"
					accessibilityState={{ disabled: !hasConversation }}
					disabled={!hasConversation}
					onPress={() => {
						Keyboard.dismiss();
						setSessionMenuOpen(true);
					}}
					style={({ pressed }) => ({
						minWidth: 48,
						minHeight: 48,
						alignItems: "center",
						justifyContent: "center",
						opacity: !hasConversation ? 0.4 : pressed ? 0.6 : 1,
					})}
				>
					<Text
						allowFontScaling={false}
						style={{ fontSize: 24, color: colors.text }}
					>
						⋮
					</Text>
				</Pressable>
			),
		});
	}, [
		navigation,
		hasConversation,
		deletionAvailable,
		connected,
		openSessionDestination,
		colors.text,
	]);
	const currentName = snapshot.conversation?.name;
	useEffect(() => {
		if (currentName && currentName !== route.params.title)
			navigation.setParams({ title: currentName });
	}, [navigation, currentName, route.params.title]);
	const conversation = snapshot.conversation;
	const preferences = useNativePreferences();
	const presentation = useMemo(
		() =>
			projectNativeTranscript(
				conversation,
				preferences.hubId === route.params.hubId ? preferences.config : null,
			),
		[conversation, preferences.hubId, preferences.config, route.params.hubId],
	);
	const timelineRows = useMemo(
		() => groupTimeline(presentation.items),
		[presentation.items],
	);
	useEffect(() => {
		appliedReaderRestore.current = null;
		approximateReaderRestore.current = null;
		restoreRetries.current = 0;
		if (restoreFrame.current !== null)
			cancelAnimationFrame(restoreFrame.current);
		restoreFrame.current = null;
		readerLatest.current = false;
		readerHeader.current = false;
		captureSuppressed.current = false;
		readerPageAttempts.current.clear();
		readerMeasurements.current.clear();
		readerAnchor.current = readerPositions.read(
			route.params.hubId,
			route.params.ref,
		);
	}, [route.params.hubId, route.params.ref]);
	// biome-ignore lint/correctness/useExhaustiveDependencies: Cell layout revisions intentionally retrigger semantic restoration.
	useEffect(() => {
		const anchor = readerAnchor.current;
		if (
			!anchor ||
			timelineRows.length === 0 ||
			!focused ||
			readerHeader.current ||
			readerLatest.current ||
			readerDragging.current ||
			readerMomentum.current
		)
			return;
		if (anchor.conversationInstance && snapshot.status !== "open") return;
		if (
			anchor.conversationInstance &&
			bindingInstance &&
			anchor.conversationInstance !== bindingInstance
		) {
			readerAnchor.current = null;
			appliedReaderRestore.current = null;
			return;
		}
		if (resolveReaderAnchor(anchor, timelineRows) === null) {
			if (isReaderAnchorLoaded(anchor, conversation?.items ?? [])) return;
			const cursor = snapshot.olderCursor;
			if (
				service &&
				connected &&
				cursor &&
				!snapshot.loadingOlder &&
				!readerPageAttempts.current.has(cursor)
			) {
				readerPageAttempts.current.add(cursor);
				void store.getState().loadOlder(service);
			}
			return;
		}
		const command = restoreReaderCommand(
			anchor,
			timelineRows,
			[...readerMeasurements.current.values()],
			96,
			!readerDragging.current && !readerMomentum.current,
		);
		if (!command) return;
		if (command.kind === "approximate") {
			if (approximateReaderRestore.current === command.offset) return;
			approximateReaderRestore.current = command.offset;
			captureSuppressed.current = true;
			timeline.current?.scrollToIndex({
				index: resolveReaderAnchor(anchor, timelineRows) ?? 0,
				viewOffset: -anchor.withinItemOffset,
				animated: false,
			});
		} else {
			const measurement = readerMeasurements.current.get(anchor.itemKey);
			if (
				!measurement ||
				!shouldApplyExactRestore(
					appliedReaderRestore.current,
					measurement,
					appliedReaderRestore.current?.offset ?? null,
					anchor.withinItemOffset,
				)
			)
				return;
			appliedReaderRestore.current = {
				key: anchor.itemKey,
				y: measurement.y,
				height: measurement.height,
				offset: anchor.withinItemOffset,
			};
			captureSuppressed.current = true;
			timeline.current?.scrollToOffset({
				offset: Math.max(0, measurement.y - command.viewOffset),
				animated: false,
			});
		}
	}, [
		bindingInstance,
		layoutRevision,
		snapshot.status,
		snapshot.olderCursor,
		snapshot.loadingOlder,
		conversation?.items,
		timelineRows,
		service,
		store,
		connected,
		focused,
	]);
	useEffect(
		() => () => {
			if (readerAnchor.current) readerPositions.save(readerAnchor.current);
			if (restoreFrame.current !== null)
				cancelAnimationFrame(restoreFrame.current);
			restoreFrame.current = null;
		},
		[],
	);
	useFocusEffect(
		useCallback(() => {
			appliedReaderRestore.current = null;
			approximateReaderRestore.current = null;
			restoreRetries.current = 0;
			setLayoutRevision((revision) => revision + 1);
			return () => {
				readerPositions.save(readerAnchor.current);
				if (restoreFrame.current !== null)
					cancelAnimationFrame(restoreFrame.current);
				restoreFrame.current = null;
			};
		}, []),
	);
	useEffect(() => {
		const subscription = AppState.addEventListener("change", (state) => {
			if (state !== "active") readerPositions.save(readerAnchor.current);
		});
		return () => subscription.remove();
	}, []);
	const controlsState = useSyncExternalStore(
		controls?.subscribe ?? noControlSubscription,
		controls?.getSnapshot ?? noControls,
	);
	const settingsPending = controlsState?.pending != null || commandPending;
	const pending = snapshot.pendingMutation?.status === "pending";
	const ready =
		connected &&
		snapshot.status === "open" &&
		!refreshing &&
		!pending &&
		!settingsPending;
	const questions = batches.flatMap((batch) => batch.questions);
	const canCompose =
		!conversation ||
		(["send", "steer", "queue", "goal"] as const).some(
			(action) => conversation.capabilities[action],
		);
	const slashToken =
		composerSelection.start === composerSelection.end &&
		draft.record.draft !== completionClosedAt
			? parseSlashToken(draft.record.draft, composerSelection.start)
			: null;
	const goalCommand = goalObjective(
		draft.record.draft,
		draft.record.images?.length,
	);
	const command = composerCommand(
		draft.record.draft,
		draft.record.images?.length,
	);
	async function applyCommand(clear = false) {
		const action = clear ? "goal" : command?.command.id;
		const capability = clear ? "goal" : command?.command.capability;
		if (
			!service ||
			!ready ||
			!action ||
			commandBusy.current ||
			(capability != null && !conversation?.capabilities[capability]) ||
			draft.submitting ||
			unconfirmedSend !== null ||
			imageState.busy ||
			questions.length
		)
			return;
		setActionError(null);
		const currentBinding = () =>
			connectionReady.current &&
			navigation.isFocused() &&
			store.getState().conversationGeneration === bindingGeneration &&
			store.getState().conversation?.instanceId === bindingInstance;
		commandBusy.current = true;
		setCommandPending(true);
		try {
			const completed = clear
				? (await submitGoalCommand(document, service, true))
					? "goal"
					: null
				: await submitComposerCommand(document, service, {
						isCurrent: currentBinding,
						reasoning: () => store.getState().conversation,
						turn: () => store.getState().conversation,
						cleared: (response) => {
							const replacement = service.adoptClear(response);
							void store
								.getState()
								.openProjected(
									service,
									activitySink,
									route.params.ref,
									replacement,
								);
						},
						openAside: (ref, title) => {
							Keyboard.dismiss();
							navigation.push("Conversation", {
								hubId: route.params.hubId,
								ref,
								title,
							});
						},
						local: async (id) => {
							if (!currentBinding())
								throw new CommandArgumentError(
									"The session changed. Try again from the current session.",
								);
							if (id === "project") {
								if (!client)
									throw new CommandArgumentError(
										"Connect to the hub to locate this session.",
									);
								const lookup = new AbortController();
								projectLookup.current = lookup;
								let location: SessionLocation;
								try {
									location = await locateSession(
										client,
										route.params.ref,
										lookup.signal,
									);
								} catch (error) {
									throw new CommandArgumentError(
										error instanceof Error
											? error.message
											: "Could not locate this session.",
									);
								}
								if (!currentBinding() || lookup.signal.aborted)
									throw new CommandArgumentError(
										"The session changed before its location was loaded.",
									);
								Keyboard.dismiss();
								navigation.push("SessionLocation", {
									hubId: route.params.hubId,
									location,
								});
								return;
							}
							if (id === "copy-id") {
								try {
									const copied = await Clipboard.setStringAsync(
										route.params.ref,
									);
									if (!copied) throw new Error("Clipboard unavailable");
									AccessibilityInfo.announceForAccessibility(
										"Session reference copied",
									);
								} catch {
									throw new CommandArgumentError(
										"Could not copy the session reference. Try again.",
									);
								}
								return;
							}
							Keyboard.dismiss();
							if (id === "status") setSessionOpen((open) => !open);
							else {
								const current = store.getState().conversation;
								if (!current || !client)
									throw new CommandArgumentError(
										"Session tasks are unavailable.",
									);
								setTaskContext((open) =>
									open
										? null
										: {
												hubId: route.params.hubId,
												ref: route.params.ref,
												threadId: current.id,
												hasTasks: current.tasks != null,
												hubName: activeProfile?.name ?? "Hub",
												client,
											},
								);
							}
						},
					});
			if (!completed || !currentBinding()) return;
			if (isLocalComposerCommand(completed) || completed === "aside") return;
			if (completed === "shutdown") {
				store.getState().close();
				service.close();
				setSessionOpen(false);
				navigation.goBack();
			} else await store.getState().rehydrate(service, activitySink);
		} catch (error) {
			if (!currentBinding()) return;
			setActionError(
				error instanceof CommandArgumentError
					? error.message
					: "Could not confirm the command. Check the session before trying again.",
			);
		} finally {
			commandBusy.current = false;
			setCommandPending(false);
		}
	}
	function editGoal() {
		const replace = () => {
			if (
				!connectionReady.current ||
				!navigation.isFocused() ||
				store.getState().conversationGeneration !== bindingGeneration ||
				document.getSnapshot().submitting ||
				document.getSnapshot().record.unconfirmed !== null
			)
				return;
			imageSelection.cancel();
			document.replaceDraft(
				`/goal ${store.getState().conversation?.goal?.objective ?? ""}`,
			);
			// Android's dialog must release window focus before opening the keyboard.
			focusAfterModal.current = Platform.OS === "android";
			setSessionOpen(false);
			if (Platform.OS === "ios")
				requestAnimationFrame(() => composerInput.current?.focus());
		};
		if (draft.record.draft || draft.record.images?.length)
			Alert.alert(
				"Replace this draft?",
				"The current message and attachments will be replaced with an editable goal command.",
				[
					{ text: "Cancel", style: "cancel" },
					{ text: "Replace draft", onPress: replace },
				],
			);
		else replace();
	}
	async function sendAnswers(batch: AskBatch, selections: QuestionSelections) {
		const current = store.getState();
		questionBatches.reconcile(pendingQuestions(current.conversation));
		const text = composeQuestionAnswers(batch.questions, selections);
		if (
			!service ||
			!ready ||
			!connectionReady.current ||
			!navigation.isFocused() ||
			current.status !== "open" ||
			current.conversationGeneration !== bindingGeneration ||
			current.conversation?.instanceId !== bindingInstance ||
			controls?.getSnapshot().pending != null ||
			current.pendingMutation?.status === "pending" ||
			!current.conversation?.capabilities.send ||
			text === null ||
			!questionBatches.getSnapshot().includes(batch)
		)
			return;
		setActionError(null);
		let acceptedAnswers = false;
		try {
			await document.submitText(text, async (input) => {
				if (!questionBatches.begin(batch)) return false;
				const previous = store.getState().lastAcceptedMutation;
				await store.getState().send(service, [{ type: "text", text: input }]);
				const accepted = store.getState().lastAcceptedMutation;
				if (!accepted || accepted === previous || accepted.kind !== "send")
					return false;
				questionBatches.finish(batch.id, true);
				setQuestionsOpen(false);
				acceptedAnswers = true;
				return true;
			});
			if (
				acceptedAnswers &&
				connectionReady.current &&
				navigation.isFocused() &&
				store.getState().conversationGeneration === bindingGeneration &&
				store.getState().conversation?.instanceId === bindingInstance
			)
				await store.getState().rehydrate(service, activitySink);
		} catch {
			setActionError(
				"Could not confirm delivery. Your answers are retained; check delivery before trying again.",
			);
		} finally {
			questionBatches.finish(batch.id, false);
		}
	}
	async function mutate(kind: "send" | "steer" | "queue" | "interrupt") {
		if (
			!service ||
			!ready ||
			controls?.getSnapshot().pending != null ||
			(kind !== "interrupt" &&
				(imageSelection.getSnapshot().busy ||
					unconfirmedSend !== null ||
					pendingQuestions(store.getState().conversation).length > 0)) ||
			store.getState().pendingMutation?.status === "pending"
		)
			return;
		setActionError(null);
		try {
			if (kind !== "interrupt") {
				const submit = (
					kind === "steer" &&
					(store.getState().conversation?.queue.depth ?? 0) > 0
						? document.submitWithQueue
						: document.submit
				).bind(document);
				await submit(async (text, images) => {
					store.getState().setDraft(text);
					const previous = store.getState().lastAcceptedMutation;
					const input = buildComposerInput(text, images);
					if (kind === "steer") await steerComposer(store, service, input);
					else await store.getState()[kind](service, input);
					const accepted = store.getState().lastAcceptedMutation;
					return (
						accepted != null && accepted !== previous && accepted.kind === kind
					);
				});
			} else await store.getState().interrupt(service);
		} catch {
			setActionError(
				"This action is unavailable. Refresh the conversation and try again.",
			);
		}
	}
	const submissionActions = (kinds: readonly ("send" | "steer" | "queue")[]) =>
		command === null &&
		kinds.map((kind) =>
			canCompose &&
			questions.length === 0 &&
			conversation?.capabilities[kind] ? (
				<Action
					key={kind}
					tone={
						kind === "send" ||
						(kind === "steer" && !conversation.capabilities.send)
							? "primary"
							: "quiet"
					}
					disabled={
						!ready ||
						!draft.loaded ||
						!!draft.error ||
						draft.submitting ||
						unconfirmedSend !== null ||
						imageState.busy ||
						(!draft.record.draft.trim() &&
							!draft.record.images?.length &&
							!(kind === "steer" && conversation.queue.depth > 0))
					}
					onPress={() => {
						void mutate(kind);
					}}
				>
					{kind === "send" ? "Send" : kind === "steer" ? "Steer" : "Queue"}
				</Action>
			) : null,
		);

	const composerSettings =
		conversation && canCompose ? (
			<ComposerSettings
				conversation={conversation}
				disabled={!ready || !controls || draft.submitting}
				pending={settingsPending}
				open={(setting) => {
					Keyboard.dismiss();
					setComposerSetting(setting);
				}}
			/>
		) : null;
	const readerCellRenderer = useMemo(() => {
		return class ReaderCell extends Component<CellRendererProps<TimelineRow>> {
			componentWillUnmount() {
				readerMeasurements.current.delete(readerKey(this.props.item));
			}
			render() {
				const { children, item, onLayout, onFocusCapture, style } = this.props;
				return (
					<View
						style={style}
						{...{ onFocusCapture }}
						onLayout={(event) => {
							onLayout?.(event);
							if (!item) return;
							const key = readerKey(item);
							readerMeasurements.current.set(key, {
								key,
								y: event.nativeEvent.layout.y,
								height: event.nativeEvent.layout.height,
							});
							setLayoutRevision((revision) => revision + 1);
						}}
					>
						{children}
					</View>
				);
			}
		};
	}, []);

	return (
		<SafeAreaView
			edges={["bottom", "left", "right"]}
			style={[styles.fill, { backgroundColor: colors.background }]}
		>
			{sessionMenuOpen ? (
				<SessionMenu
					title={conversation?.name ?? route.params.title}
					hubName={activeProfile?.name ?? "Hub"}
					connected={connected}
					deletionAvailable={deletionAvailable}
					close={() => setSessionMenuOpen(false)}
					choose={openSessionDestination}
				/>
			) : null}
			{batches.map((batch, index) => (
				<QuestionSheet
					key={batch.id + JSON.stringify(batch.questions)}
					visible={questionsOpen && index === 0}
					destination={{
						hubId: route.params.hubId,
						sessionRef: route.params.ref,
					}}
					questions={batch.questions}
					hubName={activeProfile?.name ?? "Hub"}
					ready={
						ready &&
						!!conversation?.capabilities.send &&
						draft.loaded &&
						!draft.error &&
						unconfirmedSend === null &&
						!batch.sending
					}
					pending={draft.submitting}
					error={
						unconfirmedSend !== null
							? "Delivery is unconfirmed. Close this sheet and check delivery before sending again."
							: actionError
					}
					close={() => setQuestionsOpen(false)}
					send={(selections) => sendAnswers(batch, selections)}
				/>
			))}
			{approvalsOpen && conversation && approvalControls ? (
				<ApprovalSheet
					approvals={conversation.pendingApprovals}
					controls={approvalControls}
					hubName={activeProfile?.name ?? "Hub"}
					close={() => setApprovalsOpen(false)}
					refresh={refresh}
				/>
			) : null}
			{composerSetting && conversation && controls ? (
				<ComposerSettingsSheet
					setting={composerSetting}
					conversation={conversation}
					controls={controls}
					hubName={
						activeProfile?.id === route.params.hubId
							? activeProfile.name
							: "Disconnected hub"
					}
					ready={ready}
					close={() => setComposerSetting(null)}
				/>
			) : null}
			{activeProfile?.id === route.params.hubId &&
			taskContext?.hubId === route.params.hubId &&
			taskContext.ref === route.params.ref ? (
				<TasksSheet
					key={`${route.params.hubId}:${route.params.ref}`}
					client={client ?? taskContext.client}
					sessionRef={route.params.ref}
					threadId={conversation?.id ?? taskContext.threadId}
					hasTasks={
						conversation ? conversation.tasks != null : taskContext.hasTasks
					}
					connected={connected}
					hubName={taskContext.hubName}
					close={() => setTaskContext(null)}
				/>
			) : null}
			{activeProfile?.id === route.params.hubId &&
			activityContext?.hubId === route.params.hubId &&
			activityContext.ref === route.params.ref ? (
				<ActivitySheet
					key={`${route.params.hubId}:${route.params.ref}`}
					client={client ?? activityContext.client}
					sessionRef={route.params.ref}
					threadId={conversation?.id ?? activityContext.threadId}
					connected={connected}
					hubName={activityContext.hubName}
					close={() => setActivityContext(null)}
					openSession={(ref, title) => {
						setActivityContext(null);
						navigation.push("Conversation", {
							hubId: route.params.hubId,
							ref,
							title,
						});
					}}
				/>
			) : null}
			{sessionOpen && conversation && controls ? (
				<SessionSheet
					conversation={conversation}
					controls={controls}
					hubName={
						activeProfile?.id === route.params.hubId
							? activeProfile.name
							: "Disconnected hub"
					}
					ready={ready}
					close={() => setSessionOpen(false)}
					editGoal={editGoal}
					clearGoal={() => void applyCommand(true)}
					goalError={actionError ?? draft.error}
					goalDisabled={
						!ready ||
						draft.submitting ||
						unconfirmedSend !== null ||
						imageState.busy ||
						questions.length > 0
					}
				/>
			) : null}
			{queueOpen && conversation && service ? (
				<QueueSheet
					conversation={conversation}
					service={service}
					ready={ready}
					refresh={async () => {
						await refresh();
						const current = store.getState();
						if (current.status !== "open" || current.error)
							throw new Error("Queue refresh failed");
					}}
					close={() => setQueueOpen(false)}
				/>
			) : null}
			<KeyboardAvoidingView
				style={styles.fill}
				behavior={Platform.OS === "ios" ? "padding" : "height"}
				keyboardVerticalOffset={headerHeight}
			>
				<View
					style={styles.fill}
					onLayout={(event) =>
						setViewportHeight(event.nativeEvent.layout.height)
					}
				>
					<View style={{ flex: 1 }}>
						<FlatList
							ref={timeline}
							data={timelineRows}
							ListFooterComponent={
								presentation.usage ? (
									<TranscriptUsage usage={presentation.usage} />
								) : null
							}
							CellRendererComponent={readerCellRenderer}
							keyExtractor={(item) => item.id}
							renderItem={({ item, index }) => (
								<View
									style={{
										paddingBottom: timelineGap(item, timelineRows[index + 1]),
									}}
								>
									<TimelineItem
										item={item}
										hubId={route.params.hubId}
										sessionRef={route.params.ref}
										activityPresentation={presentation.activityPresentation.get(
											item.id,
										)}
										expandByDefault={presentation.expandByDefault}
										showDuration={presentation.showDuration}
										fork={
											connected &&
											focused &&
											snapshot.conversation?.capabilities?.forkFromTurn
												? forkMessage
												: undefined
										}
									/>
								</View>
							)}
							contentContainerStyle={{ padding: 16, paddingBottom: 72 }}
							onContentSizeChange={() => {
								if (readerLatest.current)
									timeline.current?.scrollToEnd({ animated: false });
							}}
							scrollEventThrottle={100}
							onScroll={(event) => {
								if (captureSuppressed.current) {
									return;
								}
								const y = event.nativeEvent.contentOffset.y;
								const visible = timelineRows.find((item) => {
									const measurement = readerMeasurements.current.get(
										readerKey(item),
									);
									return measurement && measurement.y + measurement.height > y;
								});
								if (visible) {
									readerAnchor.current = captureReaderAnchor(
										route.params.hubId,
										route.params.ref,
										visible,
										y,
										[...readerMeasurements.current.values()],
										Date.now(),
										bindingInstance,
									);
									const anchor = readerAnchor.current;
									const measurement = readerMeasurements.current.get(
										readerKey(visible),
									);
									if (anchor && measurement)
										appliedReaderRestore.current = {
											...measurement,
											offset: anchor.withinItemOffset,
										};
								}
							}}
							onScrollBeginDrag={() => {
								readerDragging.current = true;
								readerLatest.current = false;
								readerHeader.current = false;
								captureSuppressed.current = false;
								if (restoreFrame.current !== null)
									cancelAnimationFrame(restoreFrame.current);
								restoreFrame.current = null;
							}}
							onScrollEndDrag={() => {
								readerDragging.current = false;
								readerPositions.save(readerAnchor.current);
								setLayoutRevision((revision) => revision + 1);
							}}
							onMomentumScrollBegin={() => {
								readerMomentum.current = true;
							}}
							onMomentumScrollEnd={() => {
								readerMomentum.current = false;
								readerPositions.save(readerAnchor.current);
								setLayoutRevision((revision) => revision + 1);
							}}
							onScrollToIndexFailed={({ index, averageItemLength }) => {
								if (restoreRetries.current >= 3) return;
								restoreRetries.current += 1;
								appliedReaderRestore.current = null;
								approximateReaderRestore.current = null;
								restoreFrame.current = requestAnimationFrame(() => {
									restoreFrame.current = null;
									const anchor = readerAnchor.current;
									if (
										!anchor ||
										readerDragging.current ||
										readerMomentum.current
									)
										return;
									captureSuppressed.current = true;
									timeline.current?.scrollToOffset({
										offset: Math.max(
											0,
											index * Math.max(1, averageItemLength) +
												anchor.withinItemOffset,
										),
										animated: false,
									});
								});
							}}
							keyboardShouldPersistTaps="handled"
							refreshing={refreshing}
							onRefresh={() => {
								void refresh();
							}}
							ListHeaderComponent={
								<View style={{ gap: 12, paddingBottom: 16 }}>
									<ConnectionStatus />
									<ErrorMessage message={snapshot.error || actionError} />
									<ErrorMessage message={draft.error} />
									{unconfirmedSend !== null ? (
										<View
											style={[
												styles.card,
												{ marginHorizontal: 16, borderColor: colors.border },
											]}
										>
											<Copy>Delivery unconfirmed</Copy>
											<Copy muted>
												Check the transcript before sending again. This message
												may have reached the hub.
											</Copy>
											<ScrollView style={{ maxHeight: 100 }}>
												<Copy>{unconfirmedSend}</Copy>
												<ImageAttachments
													document={document}
													selection={imageSelection}
													uncertain
												/>
											</ScrollView>
											<View style={styles.row}>
												<Action
													disabled={draft.record.draft !== ""}
													onPress={() => document.restore()}
												>
													Restore to draft
												</Action>
												<Action onPress={() => document.dismiss()}>
													Dismiss
												</Action>
											</View>
											{draft.record.draft !== "" ? (
												<Copy muted>
													Your current draft is kept. Clear it to restore this
													message.
												</Copy>
											) : null}
										</View>
									) : null}
									{connected &&
									conversation &&
									!conversation.capabilities.send &&
									!conversation.capabilities.steer &&
									!conversation.capabilities.queue ? (
										<Copy muted>Sending is unavailable for this session.</Copy>
									) : null}
									{snapshot.olderCursor ? (
										<Action
											disabled={!ready || snapshot.loadingOlder}
											onPress={() => {
												if (service) void store.getState().loadOlder(service);
											}}
										>
											{snapshot.loadingOlder
												? "Loading…"
												: "Load older messages"}
										</Action>
									) : null}
								</View>
							}
							ListEmptyComponent={
								<Copy muted>
									{snapshot.status === "opening"
										? "Loading conversation…"
										: !connected
											? "Reconnect to load the conversation. Your draft is kept."
											: snapshot.status === "error"
												? "Pull down to retry."
												: "No messages yet."}
								</Copy>
							}
						/>
						{conversation?.items.length ? (
							<View
								style={{
									position: "absolute",
									right: 16,
									bottom: 8,
									backgroundColor: colors.surface,
									borderRadius: 24,
									borderWidth: 1,
									borderColor: colors.border,
								}}
							>
								<Pressable
									accessibilityRole="button"
									accessibilityLabel="Latest"
									onPress={() => {
										readerHeader.current = false;
										readerAnchor.current = null;
										readerLatest.current = true;
										captureSuppressed.current = false;
										timeline.current?.scrollToEnd({ animated: true });
									}}
									style={({ pressed }) => ({
										minWidth: Platform.OS === "ios" ? 44 : 48,
										minHeight: Platform.OS === "ios" ? 44 : 48,
										alignItems: "center",
										justifyContent: "center",
										opacity: pressed ? 0.6 : 1,
									})}
								>
									<Text
										allowFontScaling={false}
										style={{ fontSize: 24, color: colors.secondary }}
									>
										↓
									</Text>
								</Pressable>
							</View>
						) : null}
					</View>
					<View
						style={[
							{
								flexShrink: 1,
								maxHeight: "80%",
								marginHorizontal: 12,
								marginTop: 8,
								marginBottom: 8,
								padding: 8,
								gap: 4,
								borderWidth: canCompose ? 1 : 0,
								borderRadius: canCompose ? 22 : 0,
								borderColor: colors.border,
								backgroundColor: canCompose
									? colors.surface
									: colors.background,
							},
						]}
					>
						{canCompose &&
						questions.length === 0 &&
						connected &&
						client &&
						conversation &&
						slashToken ? (
							<CommandCompletion
								// Suggestions use at most 40% of the composer's 80% viewport cap.
								maxHeight={Math.min(160, viewportHeight * 0.32)}
								client={client}
								sessionRef={route.params.ref}
								capabilities={conversation.capabilities}
								query={slashToken.query}
								close={() => setCompletionClosedAt(draft.record.draft)}
								choose={(item) => {
									if (
										document.getSnapshot().record.draft !== draft.record.draft
									)
										return;
									const inserted = spliceSlashCommand(
										draft.record.draft,
										slashToken,
										item.invocation,
									);
									document.edit(inserted.text);
									setCompletionClosedAt(inserted.text);
									setComposerSelection({
										start: inserted.caret,
										end: inserted.caret,
									});
									requestAnimationFrame(() => {
										composerInput.current?.setNativeProps({
											selection: { start: inserted.caret, end: inserted.caret },
										});
										composerInput.current?.focus();
									});
								}}
							/>
						) : null}
						<ScrollView
							style={{ flexGrow: 0, flexShrink: 1 }}
							contentContainerStyle={{ gap: 4 }}
							keyboardShouldPersistTaps="handled"
							nestedScrollEnabled
						>
							{questions.length ? (
								<Action
									disabled={!ready}
									expanded={questionsOpen}
									onPress={() => {
										Keyboard.dismiss();
										setQuestionsOpen(true);
									}}
								>
									{`${questions.length} ${questions.length === 1 ? "question" : "questions"} to answer`}
								</Action>
							) : null}
							{conversation?.pendingApprovals.length ? (
								<Action
									disabled={!ready || !approvalControls}
									expanded={approvalsOpen}
									onPress={() => {
										Keyboard.dismiss();
										setApprovalsOpen(true);
									}}
								>{`${conversation.pendingApprovals.length} ${conversation.pendingApprovals.length === 1 ? "approval" : "approvals"} needed`}</Action>
							) : null}
							{conversation?.queue.depth ? (
								<Action
									tone="quiet"
									expanded={queueOpen}
									onPress={() => {
										Keyboard.dismiss();
										setQueueOpen(true);
									}}
								>
									{`${conversation.queue.depth} queued`}
								</Action>
							) : null}
							{conversation?.goal ? (
								<Action
									tone="quiet"
									onPress={() => {
										Keyboard.dismiss();
										setSessionOpen(true);
									}}
								>
									{`Goal · ${conversation.goal.status}`}
								</Action>
							) : null}
							{canCompose && questions.length === 0 ? (
								<ImageAttachments
									document={document}
									selection={imageSelection}
								/>
							) : null}
							<ErrorMessage message={imageState.error} />
							<View
								style={{
									flexDirection: "row",
									alignItems: "flex-end",
									flexWrap: "wrap",
									gap: 8,
								}}
							>
								{canCompose && questions.length === 0 ? (
									<TextInput
										ref={composerInput}
										accessibilityLabel="Message"
										multiline
										value={draft.record.draft}
										onChangeText={(text) => document.edit(text)}
										onSelectionChange={(event) =>
											setComposerSelection(event.nativeEvent.selection)
										}
										editable={draft.loaded}
										placeholder="Message"
										placeholderTextColor={colors.secondary}
										style={[
											styles.input,
											{
												color: colors.text,
												backgroundColor: colors.surface,
												borderWidth: 0,
												padding: 2,
												flex: 1,
												minWidth: "100%",
												minHeight: Platform.OS === "android" ? 48 : 44,
												maxHeight: fontScale > 1.6 ? 96 : 160,
												textAlignVertical: "top",
											},
										]}
									/>
								) : null}
							</View>
							{fontScale > 1.4 ? composerSettings : null}
							<View
								style={[
									styles.row,
									{
										flexWrap: "wrap",
										justifyContent:
											fontScale > 1.4 ? "space-between" : "flex-end",
										gap: 4,
									},
								]}
							>
								{canCompose && questions.length === 0 ? (
									<Action
										tone="quiet"
										label="Attach images"
										disabled={!draft.loaded || !!draft.error || imageState.busy}
										onPress={() => {
											Keyboard.dismiss();
											void imageSelection.choose();
										}}
									>
										{imageState.busy ? "Processing…" : "+"}
									</Action>
								) : null}
								{fontScale <= 1.4 ? composerSettings : null}
								{canCompose && questions.length === 0 && command !== null ? (
									<Action
										tone="primary"
										label={
											command.command.id === "compact"
												? "Compact transcript"
												: undefined
										}
										disabled={
											!ready ||
											!draft.loaded ||
											!!draft.error ||
											draft.submitting ||
											unconfirmedSend !== null ||
											imageState.busy ||
											(command.command.id === "goal" &&
												!goalCommand &&
												!conversation?.goal) ||
											(command.command.capability != null &&
												!conversation?.capabilities[command.command.capability])
										}
										onPress={() => void applyCommand()}
									>
										{command.command.id === "goal"
											? goalCommand || !conversation?.goal
												? "Set goal"
												: "Clear goal"
											: command.command.label}
									</Action>
								) : null}
								{submissionActions(["send", "steer"])}
								{controlsState?.error &&
								(controlsState.lastAction === "changeModel" ||
									controlsState.lastAction === "setReasoningEffort") ? (
									<Action
										tone="quiet"
										onPress={() => {
											Keyboard.dismiss();
											setComposerSetting(
												controlsState.lastAction === "changeModel"
													? "model"
													: "reasoning",
											);
										}}
									>
										Review settings error
									</Action>
								) : null}
								{draft.error ? (
									<Action tone="accent" onPress={document.retry}>
										{draft.loaded ? "Retry saving" : "Retry loading draft"}
									</Action>
								) : null}
								{!connected ||
								snapshot.error ||
								actionError ||
								unconfirmedSend !== null ? (
									<Action
										tone="quiet"
										onPress={() => {
											Keyboard.dismiss();
											// This explicit action reveals the header; it is not a
											// new semantic reader position.
											readerHeader.current = true;
											captureSuppressed.current = true;
											timeline.current?.scrollToOffset({
												offset: 0,
												animated: true,
											});
										}}
									>
										{unconfirmedSend !== null
											? "Check delivery"
											: !connected
												? "Connection"
												: "Review error"}
									</Action>
								) : null}
								{conversation?.capabilities.interrupt ? (
									<Action
										tone="quiet"
										disabled={!ready}
										onPress={() => {
											void mutate("interrupt");
										}}
									>
										Stop
									</Action>
								) : null}
								{submissionActions(["queue"])}
							</View>
						</ScrollView>
					</View>
				</View>
			</KeyboardAvoidingView>
		</SafeAreaView>
	);
}
