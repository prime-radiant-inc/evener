import type { CellRendererProps } from "@react-native/virtualized-lists";
import { useHeaderHeight } from "@react-navigation/elements";
import { useFocusEffect } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import * as Clipboard from "expo-clipboard";
import { randomUUID } from "expo-crypto";
import { Storage } from "expo-sqlite/kv-store";
import {
	Component,
	type RefObject,
	useCallback,
	useEffect,
	useLayoutEffect,
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
import {
	type AskBatch,
	type AskQuestionRef,
	buildComposerInput,
	formatQuoteBlock,
	mergeDraftText,
	parseSlashToken,
	spliceSlashCommand,
	type TranscriptDisplayConfigV1,
	translateAttachmentMarkers,
} from "@evener/appwire-client";
import { createConversationService } from "../../mobile/src/services/conversation";
import { createActivityStore } from "../../mobile/src/state/activity";
import { createConversationStore } from "../../mobile/src/state/conversation";
import {
	createConversationMutationPendingPort,
	type ConversationMutationSubmitter,
} from "../../mobile/src/state/conversationMutation";
import { ActivitySheet } from "./ActivitySheet";
import { ApprovalControls } from "./approvalControls";
import { useMarkSeenInFront } from "./board/sessionSeen";
import { CommandCompletion } from "./CommandCompletion";
import { type ComposerSetting, ComposerSettings } from "./ComposerSettings";
import { ComposerSettingsSheet } from "./ComposerSettingsSheet";
import { useConnection } from "./ConnectionProvider";
import {
	CommandArgumentError,
	composerCommand,
	composerCommandAvailable,
	isLocalComposerCommand,
	startAside,
	submitComposerCommand,
} from "./composerCommand";
import { canComposeFor, conversationControls, queueActionRefusal } from "./conversationControls";
import type { HubProfile } from "./connection";
import { goalObjective, submitGoalCommand } from "./goalCommand";
import { HubEditor } from "./HubEditor";
import { ImageAttachments } from "./ImageAttachments";
import { ImageSelection } from "./imageSelection";
import {
	projectNativeMutationRecovery,
	RecoveryFailure,
	useRecoveryPanel,
} from "./MutationRecoveryPanel";
import { useNativePreferences } from "./NativePreferencesProvider";
import { drafts } from "./nativeDrafts";
import { nativeImagePicker } from "./nativeImagePicker";
import {
	createNativeMutationHost,
	createDurableSubmitter,
	type NativeMutationHost,
} from "./nativeMutationHost";
import {
	getNativeMutationRuntime,
	nativeMutationTargetKey,
} from "./nativeMutationRuntime";
import { readerPositions } from "./nativeReaderPosition";
import { locateSession, type SessionLocation } from "./navigationReveal";
import {
	editPairingInput,
	importPairing as importReviewedPairing,
	reviewPairingInput,
} from "./pairingImport";
import { queueHosts, type QueueHost } from "./QueueSheet";
import {
	composeQuestionAnswers,
	pendingQuestions,
	type QuestionSelections,
	questionsIdentity,
} from "./questionAnswers";
import { ApprovalDock } from "./session/ApprovalDock";
import { answerWithText } from "./session/askDockCopy";
import { bottomStack } from "./session/bottomStack";
import { QuestionDock } from "./session/QuestionDock";
import { useQuestionDraft } from "./session/useQuestionDraft";
import { QuestionBatches } from "./questionBatches";
import {
	captureReaderAnchor,
	furthestMeasuredRowBeforeTarget,
	isReaderAnchorLoaded,
	openingTarget,
	type ReaderAnchor,
	type ReaderMeasurement,
	ReaderRestoreAttempts,
	reachableReaderOffset,
	readerAnchorAt,
	readerKey,
	resolveReaderAnchor,
	restoreReaderCommand,
	shouldApplyExactRestore,
} from "./readerPosition";
import { type SessionDestination, SessionMenu } from "./SessionMenu";
import { SessionSheet } from "./SessionSheet";
import { type ErrorAction, errorAction, RETRY_MESSAGE } from "./session/errorAction";
import {
	type Ghost,
	type GhostAction,
	ghostActionTarget,
	ghosts,
	type QueueEntryRef,
	whatCanActNow,
} from "./session/ghosts";
import { NewContentPill } from "./session/NewContentPill";
import { QueuedMessages } from "./session/QueuedMessages";
import { TranscriptSkeleton } from "./session/TranscriptSkeleton";
import { useReadRetry } from "./session/useReadRetry";
import {
	answerTo,
	hideAnswerMessages,
	latestSettledTurn,
	liveRunId,
	newRowCount,
	sessionRows,
} from "./session/transcriptRows";
import { SessionControls } from "./sessionControls";
import { useConnectionStatusText } from "./board/connectionStatus";
import { Composer } from "./session/Composer";
import {
	configForLevel,
	currentLevel,
	levelToast,
} from "./session/detailLevels";
import { detailLevels } from "./session/nativeDetailLevels";
import {
	composerPlaceholder,
	sendAction,
	sendLabel,
} from "./session/sendAction";
import { NotesBar } from "./session/NotesBar";
import { canWriteHumanNote, type NotesHost, notesHosts } from "./session/NotesSheet";
import { SessionHeader, useHeaderHiding } from "./session/SessionHeader";
import { type SessionMenuAction, sessionMenu } from "./session/sessionMenu";
import {
	type ChipKind,
	contextChips,
	SHUT_DOWN,
	sessionStateLine,
} from "./session/sessionState";
import {
	NotesController,
	notesBarPreview,
	type SaveOutcome,
} from "./session/sessionNotes";
import { SessionTitle } from "./session/SessionTitle";
import { LiveStatusTray, useFrameCounter } from "./session/StatusTray";
import { localSessionId } from "./sessionDeletionResult";
import { sheetKey, useProvideSheetHost } from "./sheet/sheetHosts";
import { leaveScreen, screenInFront, useScreenInFront } from "./sheet/useScreenInFront";
import { TimelineItem } from "./TimelineItem";
import { Toast, type ToastMessage, useToast } from "./Toast";
import { TranscriptUsage } from "./TranscriptUsage";
import { groupTimeline, type TimelineRow, timelineGap } from "./timeline";
import { projectNativeTranscript } from "./transcriptPresentation";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

const noControls = () => null;
const noControlSubscription = () => () => {};
const NO_QUESTIONS: AskQuestionRef[] = [];
const STEER_FAILED = { text: "Couldn't steer with this message now." };
const STEER_ALL_FAILED = { text: "Couldn't steer with these messages now." };

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
	Projects: { hubId: string; archived?: boolean };
	Providers: { hubId: string };
	Plugins: { hubId: string };
	HubSettings: { hubId: string };
	TranscriptPreferences: { hubId: string };
	KeybindingPreferences: {
		hubId: string;
		editor?: { actionId: string; chord: string };
	};
	LaunchSettings: { hubId: string; projectCwd?: string };
	Project: {
		hubId: string;
		projectKey: string;
		title: string;
		archived?: boolean;
		tier?: "current" | "recent" | "archived";
	};
	Hubs: undefined;
	Sessions: undefined;
	NewSession: { hubId: string; hubName: string };
	Conversation: { hubId: string; ref: string; title: string };
	TasksSheet: { hubId: string; ref: string; threadId: string; hasTasks: boolean };
	NotesSheet: { hubId: string; ref: string; focusEditor?: boolean };
	QueueSheet: { hubId: string; ref: string };
	RowMenuSheet: { hubId: string; ref: string; archived: boolean };
	Reader: {
		hubId: string;
		/** The session whose folder holds the file. */
		sessionRef: string;
		path: string;
		/** The session the Reader sits over, where Open session and reviews go. */
		reviewRef: string;
		reviewTitle: string;
		/** When the file was last written, as its opener reported it. */
		updatedAt?: string;
	};
	OutlineSheet: { hubId: string; sessionRef: string; path: string };
};

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
	const { fontScale } = useWindowDimensions();
	const textScale = Platform.OS === "ios" ? fontScale : 1;
	const [editing, setEditing] = useState<HubProfile | null>(null);
	const [name, setName] = useState("");
	const [origin, setOrigin] = useState("");
	const [token, setToken] = useState("");
	const [pairingReview, setPairingReview] = useState(editPairingInput(""));
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
			const selected = await saveHub({ name, origin, token });
			setName("");
			setOrigin("");
			setToken("");
			setPairingReview(editPairingInput(""));
			if (selected) navigation.navigate("Sessions");
		} catch {
			setError(
				"Could not save this hub. Check the name and http(s) origin, and try again.",
			);
		} finally {
			setSaving(false);
		}
	}
	function reviewPairingURL() {
		const review = reviewPairingInput(pairingReview.input);
		setPairingReview(review);
		setError(review.error);
	}
	function importPairing() {
		const imported = importReviewedPairing(pairingReview);
		if (!imported) return;
		setOrigin(imported.origin);
		setToken(imported.token);
		setPairingReview(imported.state);
		setError(null);
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
					<Text
						accessibilityRole="header"
						allowFontScaling={Platform.OS !== "ios"}
						style={[
							styles.title,
							{
								color: colors.text,
								fontSize: 22 * textScale,
								lineHeight: 28 * textScale,
							},
						]}
					>
						Saved hubs
					</Text>
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
					<Text
						accessibilityRole="header"
						allowFontScaling={Platform.OS !== "ios"}
						style={[
							styles.title,
							{
								color: colors.text,
								fontSize: 22 * textScale,
								lineHeight: 28 * textScale,
								marginTop: 16,
							},
						]}
					>
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
						accessibilityLabel="Pairing URL"
						placeholder="Paste pairing URL"
						placeholderTextColor={colors.secondary}
						value={pairingReview.input}
						onChangeText={(value) => {
							setPairingReview(editPairingInput(value));
							setError(null);
						}}
						autoCapitalize="none"
						autoCorrect={false}
						keyboardType="url"
						secureTextEntry
						style={inputStyle}
					/>
					{pairingReview.preview ? (
						<View style={{ gap: 8 }}>
							<Copy muted>Pairing target: {pairingReview.preview.origin}</Copy>
							<Action disabled={saving} onPress={importPairing}>
								Import pairing link
							</Action>
						</View>
					) : (
						<Action
							disabled={saving || !pairingReview.input.trim()}
							onPress={reviewPairingURL}
						>
							Review pairing link
						</Action>
					)}
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

// Refocuses the composer after a modal closes, on AppState's "focus" event.
// That event is Android-only (react-native's AppState "focus"/"blur" pair
// never fires on iOS, and subscribing there raises a dev-mode red box), so
// the subscription itself is Android-only. Extracted so this is testable
// without mounting the whole screen.
//
// It asks for real focus, not whether the screen is in front
// (useScreenInFront): the keyboard belongs to the screen the person is looking
// at, so a session under one of its own sheets never pulls focus into its
// composer.
export function useFocusAfterModal(
	navigation: { isFocused: () => boolean },
	focusAfterModal: RefObject<boolean>,
	composerInput: RefObject<TextInput | null>,
): void {
	useEffect(() => {
		if (Platform.OS !== "android") return;
		const subscription = AppState.addEventListener("focus", () => {
			if (focusAfterModal.current && navigation.isFocused()) {
				focusAfterModal.current = false;
				composerInput.current?.focus();
			}
		});
		return () => subscription.remove();
	}, [navigation, focusAfterModal, composerInput]);
}

/** The sheet or screen each context chip and ⋯ menu item opens. */
const SESSION_DESTINATIONS = {
	subagents: "activity",
	tasks: "tasks",
	notes: "notes",
	goal: "session",
	info: "session",
	pin: "pin",
	delete: "delete",
} as const satisfies Record<string, SessionDestination>;

export function ConversationScreen({
	route,
	navigation,
}: NativeStackScreenProps<Routes, "Conversation">) {
	const {
		activeProfile,
		client,
		state: connectionState,
		fatal,
	} = useConnection();
	const focused = useScreenInFront(route.key);
	const colors = useColors();
	const { height: windowHeight } = useWindowDimensions();
	const [viewportHeight, setViewportHeight] = useState(windowHeight);
	const [sessionMenuOpen, setSessionMenuOpen] = useState(false);
	const headerHeight = useHeaderHeight();
	// The durable-mutation wiring: the store admits every mutation through a
	// lazily-acquired process runtime (a screen that never sends never opens the
	// mutations database), and a connected host effect binds this screen's
	// client and target to that same runtime. The host owns the registration and
	// the read fence; the runtime owns dispatch and the recovery row a
	// rejection produces.
	const mutationHostRef = useRef<NativeMutationHost | null>(null);
	// The submitter refuses while no host is live, so a mutation is never
	// durably accepted (and its draft cleared) while this screen has no
	// registered client to dispatch it.
	const mutationSubmitter = useMemo<ConversationMutationSubmitter>(
		() => createDurableSubmitter(() => mutationHostRef.current),
		[],
	);
	// The transcript display config the conversation projects at (the hub's
	// evener/settings/transcriptDisplay settings; the shipped mobile default
	// — intent — when the hub stores none). The store owns the level it
	// projects at and re-projects through its display boundary when it
	// changes; the service reads the CURRENT value at each read it projects,
	// so the ref (not the render value) is what it closes over — the store is
	// not recreated per config change.
	const preferences = useNativePreferences();
	const hubDisplayConfig =
		preferences.hubId === route.params.hubId ? preferences.config : null;
	// The detail level chosen for this session on this device replaces the
	// hub config's content (spec 8.7); nothing chosen leaves the hub's.
	const levels = detailLevels(route.params.hubId);
	useSyncExternalStore(levels.subscribe, levels.getRevision);
	const chosenLevel = levels.get(route.params.ref);
	const displayConfig = useMemo(
		() => configForLevel(chosenLevel, hubDisplayConfig),
		[chosenLevel, hubDisplayConfig],
	);
	const displayConfigRef = useRef<TranscriptDisplayConfigV1 | null>(displayConfig);
	displayConfigRef.current = displayConfig;
	const resolveDisplayConfig = useCallback(
		() => displayConfigRef.current,
		[],
	);
	// biome-ignore lint/correctness/useExhaustiveDependencies: Each route destination owns an independent conversation binding.
	const store = useMemo(
		() =>
			createConversationStore({
				mutationHubId: route.params.hubId,
				mutationSubmitter,
				displayConfig,
			}),
		[mutationSubmitter, route.params.hubId, route.params.ref],
	);
	// A config change reaches the live store as a level change, not a
	// rebinding: setDisplayConfig re-projects the conversation at the new
	// level through the same display boundary (an equal value is a no-op).
	useEffect(() => {
		store.getState().setDisplayConfig(displayConfig);
	}, [store, displayConfig]);
	// biome-ignore lint/correctness/useExhaustiveDependencies: Activity lifetime follows its conversation binding.
	const activity = useMemo(() => createActivityStore(), [store]);
	// The conversation store validates the exact bound sink object on refresh.
	const activitySink = useMemo(() => activity.getState(), [activity]);
	const service = useMemo(
		() =>
			client
				? createConversationService(client, {
						resolveDisplayConfig,
						onReadStart: (ref, expectedThreadId) =>
							mutationHostRef.current?.beginRead(ref, expectedThreadId),
						onReadComplete: (lease, response) =>
							mutationHostRef.current?.reconcileRead(lease, response),
					})
				: null,
		[client, resolveDisplayConfig],
	);
	const currentDestination = useRef({ store, client });
	currentDestination.current = { store, client };
	const snapshot = store();
	const timeline = useRef<FlatList>(null);
	const readerMeasurements = useRef(new Map<string, ReaderMeasurement>());
	const readerContentHeight = useRef(0);
	const readerViewportHeight = useRef(0);
	const [layoutRevision, setLayoutRevision] = useState(0);
	const readerAnchor = useRef<ReaderAnchor | null>(null);
	const appliedReaderRestore = useRef<{
		key: string;
		y: number;
		height: number;
		scrollOffset: number;
	} | null>(null);
	const readerRestoreAttempts = useRef(new ReaderRestoreAttempts());
	const readerPageAttempts = useRef(new Set<string>());
	const readerHeader = useRef(false);
	const readerLatest = useRef(false);
	// The latest settled turn while the list sat at its end (ruling 31). Every
	// anchor carries it, so opening the session later can tell a newer reply
	// finished since.
	const turnsSeen = useRef<string | undefined>(undefined);
	// Where the session opened is decided once per route (spec 7.3).
	const openedFor = useRef<string | null>(null);
	// The reader keys the list held when you left its end; null at the end.
	// Rows that arrive below it make "↓ 3 new".
	const [awayKeys, setAwayKeys] = useState<ReadonlySet<string> | null>(null);
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
	useFocusAfterModal(navigation, focusAfterModal, composerInput);
	const [sessionOpen, setSessionOpen] = useState(false);
	const [activityContext, setActivityContext] = useState<{
		hubId: string;
		ref: string;
		threadId: string;
		hubName: string;
		client: NonNullable<typeof client>;
	} | null>(null);
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
				setSessionOpen(false);
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
	// Follows the screen in front rather than focus, so a photo being attached
	// survives one of the screen's own sheets opening.
	useEffect(() => {
		if (!focused) return;
		return () => {
			imageSelection.cancel();
			projectLookup.current?.abort();
		};
	}, [focused, imageSelection]);
	const unconfirmedSend = draft.submitting ? null : draft.record.unconfirmed;
	const connected =
		connectionState === "ready" && activeProfile?.id === route.params.hubId;
	const connectionText = useConnectionStatusText(connectionState, fatal);
	// The same debounced signal the connection bar itself waits on (spec 14:
	// "a blip shorter than this reconnects without a word"), so the chips
	// never flicker through a hide-and-show the bar stays silent for, and a
	// visible chip's tap (below, in openSessionDestination) never silently
	// does nothing during that same window (Calm).
	const chipsConnected =
		activeProfile?.id === route.params.hubId && connectionText === null;
	useMarkSeenInFront(
		route.params,
		focused,
		connected ? client : null,
		snapshot.status === "open" ? snapshot.conversation : null,
	);
	// The recovery surface: this exact hub/conversation target's durable
	// recovery rows, shown as ghosts above the composer. useRecoveryPanel
	// acquires the runtime only once the conversation is connected (the
	// singleton opens the mutations database), so a screen that never reaches a
	// live conversation never constructs one - what the landed render fence
	// (useNativeMutationRecovery.render.test.tsx) requires of this screen.
	const recovery = useRecoveryPanel({
		connected,
		hubId: route.params.hubId,
		targetRef: route.params.ref,
	});
	// Bind this screen's client and target to the runtime for the connected
	// lifetime: the service's read fence calls through mutationHostRef, and the
	// runtime's dispatch gate opens on this screen's own authoritative read and
	// retires with the mount. A failed startup keeps the registration: the
	// runtime retries its own start on the next submission, and a durable
	// admission must always have this screen's client bound.
	useEffect(() => {
		if (!client || !connected) return;
		let host: NativeMutationHost;
		try {
			host = createNativeMutationHost(
				getNativeMutationRuntime(),
				route.params.hubId,
				route.params.ref,
				client,
			);
		} catch {
			// The mutations database or the client binding could not be created.
			// Leave the screen mounted with no host rather than crashing the
			// conversation; a later reconnect or remount retries, and a mutation
			// submitted meanwhile surfaces its own failure through the store.
			mutationHostRef.current = null;
			return;
		}
		mutationHostRef.current = host;
		void host.start().catch(() => undefined);
		return () => {
			if (mutationHostRef.current === host) mutationHostRef.current = null;
			host.dispose();
		};
	}, [client, connected, route.params.hubId, route.params.ref]);
	useEffect(() => () => store.getState().close(), [store]);
	// Thread reads replace the connection's subscription. Returning from a
	// child or editor must reacquire this screen's stream and current snapshot.
	useEffect(() => {
		if (!service || !connected || !focused) return;
		void store
			.getState()
			.resumeProjected(service, activitySink, route.params.ref)
			.catch((error) => {
				// The store surfaces a failed resume in its own state; this only
				// keeps the rejection from going unobserved.
				console.error("ConversationScreen: resume failed", error);
			});
		return () => {
			store.getState().suspendProjected();
			service.close();
		};
	}, [service, store, activitySink, connected, focused, route.params.ref]);
	// A read that failed while connected tries again on its own (spec 14);
	// from the third failure in a row the transcript says so.
	const readStatus = useCallback(() => store.getState().status, [store]);
	const retryRead = useCallback(
		() =>
			service
				? store.getState().resumeProjected(service, activitySink, route.params.ref)
				: Promise.resolve(),
		[service, store, activitySink, route.params.ref],
	);
	const readFailures = useReadRetry({
		status: snapshot.status,
		active: Boolean(service) && connected && focused,
		resetKey: `${route.params.hubId}\u0000${route.params.ref}`,
		readStatus,
		resume: retryRead,
	});
	// The durable pending-row seam. The store retires it on EVERY thread open,
	// and `openProjected` runs from more than the resume effect: the /clear
	// command's cleared callback, the refresh paths, and resumeProjected's own
	// fall-through all reopen the thread. Key the rebind on the conversation
	// generation so any host-initiated (re)open re-establishes the seam once the
	// store has retired it, while a suspend/rehydrate generation bump that did
	// not retire it leaves the live subscription untouched (the store's
	// bindPendingMutationsIfUnbound is idempotent). The store's own close/reset
	// retires the seam on unmount, so this effect needs no cleanup.
	useEffect(() => {
		if (!client || !connected || !focused) return;
		try {
			store.getState().bindPendingMutationsIfUnbound(
				createConversationMutationPendingPort(
					getNativeMutationRuntime(),
					nativeMutationTargetKey(route.params.hubId, route.params.ref),
				),
			);
		} catch (error) {
			// The mutations database could not be opened. The conversation stays
			// usable; a later reconnect or remount retries, and the failure is
			// logged so it is not silent.
			console.error(
				"ConversationScreen: durable pending seam bind failed",
				error,
			);
		}
	}, [
		store,
		client,
		connected,
		focused,
		route.params.hubId,
		route.params.ref,
		snapshot.conversationGeneration,
	]);
	const connectionReady = useRef(connected);
	connectionReady.current = connected;
	const bindingGeneration = snapshot.conversationGeneration;
	const bindingInstance = snapshot.conversation?.instanceId;
	function forkMessage(entryIndex: number, preview: string) {
		const current = store.getState();
		if (
			!connectionReady.current ||
			!screenInFront(navigation, route.key) ||
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
	const controls = useMemo(() => {
		if (!service || !connected || !focused) return null;
		const refreshSession = async () => {
			if (store.getState().status === "open")
				await store.getState().rehydrate(service, activitySink);
			else
				await store
					.getState()
					.resumeProjected(service, activitySink, route.params.ref);
			const current = store.getState();
			if (current.status !== "open" || current.error)
				throw new Error("Session refresh failed");
		};
		return new SessionControls(
			service,
			refreshSession,
			// A shut-down session stays open on its history (ruling 19); a
			// failed read surfaces through the store's own error.
			() => {
				void refreshSession().catch(() => undefined);
			},
			(scope) => {
				const current = store.getState();
				if (
					currentDestination.current.store !== store ||
					currentDestination.current.client !== client ||
					!connectionReady.current ||
					!screenInFront(navigation, route.key)
				)
					return false;
				if (scope === "destination")
					return current.ref === route.params.ref;
				return (
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
		);
	}, [
		service,
		store,
		client,
		route.params.ref,
		activitySink,
		navigation,
		connected,
		focused,
		bindingGeneration,
		bindingInstance,
		document,
	]);
	useEffect(() => () => controls?.dispose(), [controls]);
	const approvalControls = useMemo(
		() =>
			client && service && connected && focused
				? new ApprovalControls(
						client,
						route.params.ref,
						() => store.getState().conversation?.pendingEscalations ?? [],
						() =>
							connectionReady.current &&
							screenInFront(navigation, route.key) &&
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
		snapshot.conversation?.status.type === "notLoaded";
	const openSessionDestination = useCallback(
		(destination: SessionDestination) => {
			setSessionMenuOpen(false);
			Keyboard.dismiss();
			const current = store.getState().conversation;
			if (!current) return;
			if (destination === "delete") {
				if (!localSessionId(route.params.ref) || current.status.type !== "notLoaded")
					return;
				navigation.navigate("SessionDeletion", {
					hubId: route.params.hubId,
					ref: route.params.ref,
					title: current.name || route.params.title,
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
			// Shared notes read without a connection.
			if (destination === "notes") {
				navigation.navigate("NotesSheet", {
					hubId: route.params.hubId,
					ref: route.params.ref,
				});
				return;
			}
			// The same debounced signal the chips (and the connection bar) use:
			// a blip shorter than the bar's own grace period must not make an
			// already-visible chip's tap silently do nothing (Calm).
			if (!client || !chipsConnected) return;
			if (destination === "tasks") {
				navigation.navigate("TasksSheet", {
					hubId: route.params.hubId,
					ref: route.params.ref,
					threadId: current.threadId,
					hasTasks: current.tasks != null,
				});
				return;
			}
			setActivityContext({
				hubId: route.params.hubId,
				ref: route.params.ref,
				threadId: current.threadId,
				hubName: activeProfile?.name ?? "Hub",
				client,
			});
		},
		[
			store,
			client,
			chipsConnected,
			route.params.hubId,
			route.params.ref,
			activeProfile?.name,
			navigation,
			route.params.title,
		],
	);
	// The title's state line reads live ages ("Working · 38m", "Finished ·
	// 1h ago"): re-render twice a minute while the session is in front.
	const [, setClock] = useState(0);
	useEffect(() => {
		if (!focused) return;
		const clock = setInterval(() => setClock((tick) => tick + 1), 30_000);
		return () => clearInterval(clock);
	}, [focused]);
	const conversation = snapshot.conversation;
	const stateLine = conversation
		? sessionStateLine(conversation, Date.now())
		: null;
	const chips = conversation ? contextChips(conversation, chipsConnected) : [];
	const headerHiding = useHeaderHiding();
	// The header block floats over the list; the list reserves its height.
	const [sessionHeaderHeight, setSessionHeaderHeight] = useState(0);
	const listOffset = useRef(0);
	const reservedHeaderHeight = useRef(0);
	// When the block grows or shrinks (the connection bar comes or goes), the
	// list's top padding moves by the same amount; scrolling the list by it
	// too keeps every row where it was on screen. At the top the list stays
	// at the top, and the rows make room for the block.
	useLayoutEffect(() => {
		const change = sessionHeaderHeight - reservedHeaderHeight.current;
		reservedHeaderHeight.current = sessionHeaderHeight;
		if (change === 0 || listOffset.current <= 0) return;
		const target = Math.max(0, listOffset.current + change);
		// Set optimistically: the list's own onScroll is throttled
		// (scrollEventThrottle), so a second height change in the same window
		// must compose with where this scroll is already taking the list, not
		// with the last offset the list actually reported.
		listOffset.current = target;
		timeline.current?.scrollToOffset({ offset: target, animated: false });
	}, [sessionHeaderHeight]);
	function openChip(kind: ChipKind) {
		if (kind !== "queue") {
			openSessionDestination(SESSION_DESTINATIONS[kind]);
			return;
		}
		openQueue();
	}
	function openQueue() {
		Keyboard.dismiss();
		navigation.navigate("QueueSheet", {
			hubId: route.params.hubId,
			ref: route.params.ref,
		});
	}
	const menuLevel = currentLevel(chosenLevel, hubDisplayConfig);
	// The menu offers Subagents exactly when its chip shows.
	const hasSubagents = chips.some((chip) => chip.kind === "subagents");
	const canAside =
		connected &&
		service !== null &&
		!!conversation?.capabilities.forkFromTurn;
	const canShutDown =
		controls !== null &&
		!!conversation?.capabilities.shutdown &&
		!SHUT_DOWN.has(conversation.status.type);
	function openAside(ref: string, title: string) {
		Keyboard.dismiss();
		navigation.push("Conversation", {
			hubId: route.params.hubId,
			ref,
			title,
		});
	}
	function archive(archived: boolean) {
		if (!client) return Promise.reject(new Error("Not connected"));
		return client.request("evener/archive/set", {
			kind: "session",
			id: route.params.ref,
			archived,
		});
	}
	function chooseSessionAction(action: SessionMenuAction) {
		switch (action.kind) {
			case "level":
				levels.set(route.params.ref, action.level);
				toaster.show({ text: levelToast(action.level) });
				return;
			case "subagents":
			case "tasks":
			case "notes":
			case "info":
			case "pin":
			case "delete":
				openSessionDestination(SESSION_DESTINATIONS[action.kind]);
				return;
			case "aside":
				if (!service) return;
				startAside(service).then(
					(aside) => {
						if (screenInFront(navigation, route.key))
							openAside(aside.ref, aside.title);
					},
					() => toaster.show({ text: "Couldn't start an aside." }),
				);
				return;
			case "archive":
				archive(true).then(
					() =>
						toaster.show({
							text: "Session archived",
							action: {
								label: "Undo",
								run: () =>
									void archive(false).catch(() =>
										toaster.show({ text: "Couldn't undo the archive." }),
									),
							},
						}),
					() => toaster.show({ text: "Couldn't archive this session." }),
				);
				return;
			case "shutDown":
				if (!controls) return;
				Alert.alert(
					"Shut down this session?",
					"It stops now and keeps its history. Sending a message resumes it.",
					[
						{ text: "Cancel", style: "cancel" },
						{
							text: "Shut down",
							style: "destructive",
							onPress: () => {
								void controls.shutdown().then((stopped) => {
									toaster.show({
										text: stopped
											? "Session shut down"
											: "Couldn't shut down this session.",
									});
								});
							},
						},
					],
				);
				return;
		}
	}
	// The header items outlive this render; they reach the latest choices
	// through the ref, so the header is not reset on every render. The ref is
	// written after commit, in its own effect with no deps, not during
	// render, which React's own rules reserve for effects.
	const chooseSessionActionRef = useRef(chooseSessionAction);
	useEffect(() => {
		chooseSessionActionRef.current = chooseSessionAction;
	});
	useEffect(() => {
		navigation.setOptions({
			// iPhone only: the pressable title with the session's state (spec
			// 8.1). Android keeps the plain title.
			headerTitle:
				Platform.OS === "ios" && stateLine
					? ({ children }) => (
							<SessionTitle
								title={children}
								line={stateLine}
								onPress={() => openSessionDestination("session")}
							/>
						)
					: undefined,
			// iOS draws these as a native menu and ignores headerRight; Android
			// ignores them and keeps headerRight's SessionMenu.
			unstable_headerRightItems: () =>
				hasConversation
					? sessionMenu({
							current: menuLevel,
							hasSubagents,
							connected,
							sharedNotes: !!conversation?.capabilities.sharedNotes,
							canAside,
							canShutDown,
							deletable: deletionAvailable,
							choose: (action) => chooseSessionActionRef.current(action),
						})
					: [],
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
		stateLine?.state,
		stateLine?.text,
		menuLevel,
		hasSubagents,
		conversation?.capabilities.sharedNotes,
		canAside,
		canShutDown,
	]);
	const currentName = snapshot.conversation?.name;
	useEffect(() => {
		if (currentName && currentName !== route.params.title)
			navigation.setParams({ title: currentName });
	}, [navigation, currentName, route.params.title]);
	// The conversation's rows are already level-correct: the store projected
	// them at displayConfig (D24-6's seam routing), so the presentation layer
	// only reshapes (member unrolling, attachment adjacency) and computes the
	// footer's accounting — no second, screen-level projection.
	const presentation = useMemo(
		() => projectNativeTranscript(conversation, displayConfig),
		[conversation, displayConfig],
	);
	const timelineRows = useMemo(
		() =>
			// Your answers to a question show beneath the question itself.
			hideAnswerMessages(
				sessionRows(groupTimeline(presentation.items), conversation?.turns ?? []),
			),
		[presentation.items, conversation?.turns],
	);
	const liveRun = liveRunId(timelineRows, conversation?.activeTurnId);
	// A subagent row opens the subagent's own transcript, as the Activity
	// sheet does, until phase 4's subagent screen.
	const openSubagent = useCallback(
		(ref: string, title: string) =>
			navigation.push("Conversation", { hubId: route.params.hubId, ref, title }),
		[navigation, route.params.hubId],
	);
	const answerFor = useCallback(
		(itemId: string) => answerTo(conversation, itemId),
		[conversation],
	);
	// Stable across renders, so a settled agent message keeps its memoized
	// markdown view (TimelineItem's AgentMessage) while the list re-renders.
	const quote = useCallback(
		(text: string) => {
			const quoted = formatQuoteBlock(text);
			if (quoted === "") return;
			const merged = mergeDraftText(document.getSnapshot().record.draft, quoted);
			document.edit(merged);
			setComposerSelection({ start: merged.length, end: merged.length });
			requestAnimationFrame(() => {
				composerInput.current?.setNativeProps({
					selection: { start: merged.length, end: merged.length },
				});
				composerInput.current?.focus();
			});
		},
		[document],
	);
	useEffect(() => {
		appliedReaderRestore.current = null;
		readerRestoreAttempts.current.reset();
		if (restoreFrame.current !== null)
			cancelAnimationFrame(restoreFrame.current);
		restoreFrame.current = null;
		readerHeader.current = false;
		captureSuppressed.current = false;
		readerPageAttempts.current = new Set<string>();
		readerMeasurements.current.clear();
		readerAnchor.current = readerPositions.read(
			route.params.hubId,
			route.params.ref,
		);
		readerLatest.current = readerAnchor.current === null;
		turnsSeen.current = readerAnchor.current?.turnsSeen;
		openedFor.current = null;
		setAwayKeys(null);
	}, [route.params.hubId, route.params.ref]);
	// Where the session opens (spec 7.3, ruling 31), decided once per route on
	// the first layout with rows: the live end while a question or approval
	// waits, the start of a reply that finished since you last reached the
	// end, or where you left off. It only sets the reading position; the
	// restore effect below moves the list there.
	useEffect(() => {
		const routeKey = `${route.params.hubId}\u0000${route.params.ref}`;
		if (
			openedFor.current === routeKey ||
			!conversation ||
			snapshot.status !== "open" ||
			timelineRows.length === 0 ||
			!focused
		)
			return;
		openedFor.current = routeKey;
		const target = openingTarget(
			readerAnchor.current,
			timelineRows,
			conversation.turns.map((turn) => turn.id),
			pendingQuestions(conversation).length > 0 ||
				conversation.pendingEscalations.length > 0,
		);
		if (target.kind === "live") {
			readerAnchor.current = null;
			readerLatest.current = true;
			(
				timeline.current?.getScrollResponder() as ScrollView | null
			)?.scrollToEnd({ animated: false });
		} else if (target.kind === "row") {
			readerAnchor.current = readerAnchorAt(
				route.params.hubId,
				route.params.ref,
				timelineRows[target.index],
				0,
				Date.now(),
				bindingInstance,
				turnsSeen.current,
			);
			readerLatest.current = false;
			appliedReaderRestore.current = null;
			readerRestoreAttempts.current.reset();
		}
	}, [
		conversation,
		snapshot.status,
		timelineRows,
		focused,
		bindingInstance,
		route.params.hubId,
		route.params.ref,
	]);
	// Loads the page above the loaded history once per cursor: a page that
	// failed, or brought nothing new, stays guarded until the binding or route
	// resets, so a failing page never loops. Both a reading position restored
	// above the loaded rows and a scroll near the top ask for it.
	function loadOlderPage() {
		const cursor = snapshot.olderCursor;
		const pageAttempts = readerPageAttempts.current;
		if (
			!service ||
			!connected ||
			!cursor ||
			snapshot.loadingOlder ||
			pageAttempts.has(cursor)
		)
			return;
		pageAttempts.add(cursor);
		void store
			.getState()
			.loadOlder(service)
			.then((result) => {
				if (
					result.status === "ignored" ||
					(result.status === "loaded" && result.itemKeys.length > 0)
				)
					pageAttempts.delete(cursor);
			})
			.catch(() => {
				// Keep failed page attempts guarded until a binding or route reset.
			});
	}
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
			readerLatest.current = true;
			(
				timeline.current?.getScrollResponder() as ScrollView | null
			)?.scrollToEnd({ animated: false });
			return;
		}
		if (resolveReaderAnchor(anchor, timelineRows) === null) {
			if (isReaderAnchorLoaded(anchor, conversation?.items ?? [])) return;
			loadOlderPage();
			return;
		}
		const command = restoreReaderCommand(
			anchor,
			timelineRows,
			[...readerMeasurements.current.values()],
			96,
			!readerDragging.current && !readerMomentum.current,
		);
		const targetIndex = resolveReaderAnchor(anchor, timelineRows);
		const measurementProgress =
			targetIndex === null
				? -1
				: furthestMeasuredRowBeforeTarget(timelineRows, targetIndex, [
						...readerMeasurements.current.values(),
					]);
		if (
			!command ||
			!readerRestoreAttempts.current.begin(command, measurementProgress)
		)
			return;
		if (command.kind === "approximate") {
			captureSuppressed.current = true;
			timeline.current?.scrollToIndex({
				index: resolveReaderAnchor(anchor, timelineRows) ?? 0,
				viewOffset: -anchor.withinItemOffset,
				animated: false,
			});
		} else {
			if (restoreFrame.current !== null)
				cancelAnimationFrame(restoreFrame.current);
			restoreFrame.current = null;
			const currentKey = readerKey(timelineRows[command.index]);
			const measurement = readerMeasurements.current.get(currentKey);
			if (!measurement) return;
			const scrollOffset = reachableReaderOffset(
				measurement.y - command.viewOffset,
				readerContentHeight.current,
				readerViewportHeight.current,
			);
			if (
				!shouldApplyExactRestore(
					appliedReaderRestore.current,
					measurement,
					appliedReaderRestore.current?.scrollOffset ?? null,
					scrollOffset,
				)
			)
				return;
			appliedReaderRestore.current = {
				key: currentKey,
				y: measurement.y,
				height: measurement.height,
				scrollOffset,
			};
			captureSuppressed.current = true;
			timeline.current?.scrollToOffset({
				offset: scrollOffset,
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
	// Follows the screen in front rather than focus, so opening one of the
	// screen's own sheets neither saves nor re-applies the reading position.
	useEffect(() => {
		if (!focused) return;
		appliedReaderRestore.current = null;
		readerRestoreAttempts.current.reset();
		setLayoutRevision((revision) => revision + 1);
		return () => {
			readerPositions.save(readerAnchor.current);
			if (restoreFrame.current !== null)
				cancelAnimationFrame(restoreFrame.current);
			restoreFrame.current = null;
		};
	}, [focused]);
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
		focused &&
		snapshot.status === "open" &&
		!pending &&
		!settingsPending;
	const questions = batches.flatMap((batch) => batch.questions);
	// The ask dock shows the first batch only; the rest wait behind it.
	const questionBatch = batches[0] ?? null;
	const questionDraft = useQuestionDraft(
		{ hubId: route.params.hubId, sessionRef: route.params.ref },
		questionBatch?.questions ?? NO_QUESTIONS,
	);
	// Saved answers that couldn't be read get another try on their own when
	// the hub comes back and when the session comes back to front: there is
	// no Retry to press (Calm).
	// biome-ignore lint/correctness/useExhaustiveDependencies: reload is a new function each render; it runs on these transitions only.
	useEffect(() => {
		if (connected && focused) questionDraft.reload();
	}, [connected, focused]);
	// Whether the dock is folded, and whether "Other answer…" brought the
	// composer back: both start over for each new batch of questions.
	const [questionFolded, setQuestionFolded] = useState(false);
	const [composerBack, setComposerBack] = useState(false);
	// Why the answers didn't go: the dock's own line, open or folded. Every
	// other failure keeps the screen's error area.
	const [answerError, setAnswerError] = useState<string | null>(null);
	const dockBatch = questionBatch
		? questionBatch.id + questionsIdentity(questionBatch.questions)
		: null;
	const [dockFor, setDockFor] = useState(dockBatch);
	if (dockFor !== dockBatch) {
		setDockFor(dockBatch);
		setQuestionFolded(false);
		setComposerBack(false);
		setAnswerError(null);
	}
	// What this conversation may be asked to do now (conversationControls.ts):
	// every affordance and submission below reads it, never a raw capability.
	const permitted = conversation ? conversationControls(conversation) : null;
	const canCompose =
		(!conversation ||
			(!conversation.resumeRequired &&
				conversation.status.type !== "restartRequired")) &&
		(!conversation || canComposeFor(conversation));
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
		if (
			!service ||
			!ready ||
			!action ||
			commandBusy.current ||
			(clear
				? !conversation?.capabilities.goal
				: !command ||
					!conversation ||
					!composerCommandAvailable(command.command, conversation)) ||
			draft.submitting ||
			unconfirmedSend !== null ||
			imageState.busy ||
			questions.length
		)
			return;
		setActionError(null);
		const currentBinding = () =>
			connectionReady.current &&
			screenInFront(navigation, route.key) &&
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
						openAside,
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
								navigation.navigate("TasksSheet", {
									hubId: route.params.hubId,
									ref: route.params.ref,
									threadId: current.threadId,
									hasTasks: current.tasks != null,
								});
							}
						},
					});
			if (!completed || !currentBinding()) return;
			if (isLocalComposerCommand(completed) || completed === "aside") return;
			if (completed === "shutdown") {
				store.getState().close();
				service.close();
				setSessionOpen(false);
				leaveScreen(navigation, route.key);
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
				!screenInFront(navigation, route.key) ||
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
	// Sends a batch's answers as one message, and says whether the hub took
	// them.
	async function sendAnswers(
		batch: AskBatch,
		selections: QuestionSelections,
	): Promise<boolean> {
		const current = store.getState();
		questionBatches.reconcile(pendingQuestions(current.conversation));
		const text = composeQuestionAnswers(batch.questions, selections);
		if (
			!service ||
			!ready ||
			!connectionReady.current ||
			!screenInFront(navigation, route.key) ||
			current.status !== "open" ||
			current.conversationGeneration !== bindingGeneration ||
			current.conversation?.instanceId !== bindingInstance ||
			controls?.getSnapshot().pending != null ||
			current.pendingMutation?.status === "pending" ||
			!(current.conversation && conversationControls(current.conversation).send) ||
			text === null ||
			!questionBatches.getSnapshot().includes(batch)
		)
			return false;
		setAnswerError(null);
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
				acceptedAnswers = true;
				return true;
			});
			if (acceptedAnswers)
				toaster.show({
					text: batch.questions.length > 1 ? "Answers sent" : "Answer sent",
				});
			if (
				acceptedAnswers &&
				connectionReady.current &&
				screenInFront(navigation, route.key) &&
				store.getState().conversationGeneration === bindingGeneration &&
				store.getState().conversation?.instanceId === bindingInstance
			)
				await store.getState().rehydrate(service, activitySink);
		} catch {
			setAnswerError(
				"Could not confirm delivery. Your answers are retained; check delivery before trying again.",
			);
		} finally {
			questionBatches.finish(batch.id, false);
		}
		return acceptedAnswers;
	}
	const frames = useFrameCounter(store);
	const toaster = useToast();
	// The session's shared notes (spec 8.8). The controller lives here, not in
	// the sheet, so a save the sheet starts as it closes outlives it. store
	// itself is already rebuilt exactly when route.params.hubId/ref change
	// (its own useMemo above), so this would rebuild on a session switch
	// through [store] alone - hubId/ref are listed too anyway, so the
	// rebuild condition doesn't rest on that indirection (RoboRev #2769
	// round 3).
	// biome-ignore lint/correctness/useExhaustiveDependencies: One controller per conversation binding.
	const notes = useMemo(
		() =>
			new NotesController({
				client: {
					request: (method, params) => {
						const live = connectionReady.current
							? currentDestination.current.client
							: null;
						return live
							? live.request(method, params)
							: Promise.reject(new Error("Not connected"));
					},
				},
				hubId: route.params.hubId,
				ref: route.params.ref,
				instanceId: () => store.getState().conversation?.instanceId,
				savedNote: () => store.getState().conversation?.humanNote ?? "",
				working: () =>
					store.getState().conversation?.status.type === "active",
				storage: Storage,
				uuid: randomUUID,
			}),
		[store, route.params.hubId, route.params.ref],
	);
	useEffect(() => () => notes.dispose(), [notes]);
	// Follow the hub's note (evener/notes/updated) as it changes: sync() itself
	// is a no-op unless the text actually differs.
	useEffect(() => {
		notes.sync();
	}, [conversation?.humanNote, notes]);
	const writable = conversation ? canWriteHumanNote(conversation) : false;
	// A note kept on this phone because its save failed sends once the
	// session is open, connected, in front and still takes notes; otherwise
	// it stays on this phone. The controller is watched, not
	// only the deps, so a save that fails while already connected is tried
	// again without waiting for a reconnect. One retry per failure: the
	// retry's own "failed" publish must not start another.
	useEffect(() => {
		let retrying = false;
		let timer: ReturnType<typeof setTimeout> | null = null;
		function retryIfNeeded() {
			if (retrying || !connected || !focused || !bindingInstance || !writable) return;
			if (notes.getSnapshot().phase !== "failed") return;
			retrying = true;
			// Next tick: the "failed" publish can come from inside a flush whose
			// shared saving promise hasn't cleared, and a flush in the same tick
			// would join that failing promise instead of trying again.
			timer = setTimeout(() => {
				timer = null;
				void notes.flush().finally(() => {
					retrying = false;
				});
			}, 0);
		}
		retryIfNeeded();
		const unsubscribe = notes.subscribe(retryIfNeeded);
		return () => {
			unsubscribe();
			if (timer !== null) clearTimeout(timer);
		};
	}, [connected, focused, bindingInstance, notes, writable]);
	const notesSaved = useCallback(
		(outcome: SaveOutcome) => {
			if (outcome.saved)
				toaster.show({
					text: outcome.woke
						? "Note saved. The agent is reading it."
						: "Note saved",
				});
		},
		[toaster.show],
	);
	const notesHost = useMemo<NotesHost | undefined>(
		() =>
			conversation
				? {
						session: {
							humanNote: conversation.humanNote,
							agentNote: conversation.agentNote,
							sessionUrls: conversation.sessionUrls,
							status: conversation.status,
							resumeRequired: conversation.resumeRequired,
							capabilities: conversation.capabilities,
						},
						notes,
						saved: notesSaved,
					}
				: undefined,
		[
			conversation?.humanNote,
			conversation?.agentNote,
			conversation?.sessionUrls,
			conversation?.status,
			conversation?.resumeRequired,
			conversation?.capabilities,
			notes,
			notesSaved,
		],
	);
	useProvideSheetHost(
		notesHosts,
		sheetKey(route.params.hubId, route.params.ref),
		notesHost,
	);
	const notesPreview = conversation ? notesBarPreview(conversation) : null;
	const [stopping, setStopping] = useState(false);
	const stopBusy = useRef(false);
	async function stop() {
		if (
			!service ||
			!connectionReady.current ||
			stopBusy.current ||
			store.getState().pendingMutation?.status === "pending"
		)
			return;
		stopBusy.current = true;
		setStopping(true);
		try {
			const previous = store.getState().lastAcceptedMutation;
			await store.getState().interrupt(service);
			const accepted = store.getState().lastAcceptedMutation;
			if (accepted && accepted !== previous && accepted.kind === "interrupt")
				toaster.show({ text: "Stopped" });
		} catch {
			// Stop only acts while a turn runs; a turn that ended first has
			// nothing left to stop, so a refusal says nothing.
		} finally {
			stopBusy.current = false;
			setStopping(false);
		}
	}
	function jumpToLive() {
		readerHeader.current = false;
		readerAnchor.current = null;
		readerLatest.current = true;
		captureSuppressed.current = false;
		setAwayKeys(null);
		(
			timeline.current?.getScrollResponder() as ScrollView | null
		)?.scrollToEnd({ animated: true });
	}
	// The render-time action drives the placeholder, the label and whether
	// Send is enabled; a press routes on the live state instead.
	const action = conversation
		? sendAction(conversation, snapshot.pendingMutations, connected)
		: "none";
	const composerReady =
		ready &&
		draft.loaded &&
		!draft.error &&
		!draft.submitting &&
		unconfirmedSend === null &&
		!imageState.busy;
	// While a question waits, Send answers it with your text (ruling 14).
	const answering = questionBatch !== null;
	const sendEnabled = answering
		? composerReady &&
			!!draft.record.draft.trim() &&
			!!permitted?.send &&
			!questionBatch.sending &&
			// Your text answers against the saved answers, so it waits for them.
			questionDraft.loaded
		: command !== null
			? composerReady &&
				!(command.command.id === "goal" && !goalCommand && !conversation?.goal) &&
				!!conversation &&
				composerCommandAvailable(command.command, conversation)
			: composerReady &&
				(!!draft.record.draft.trim() || !!draft.record.images?.length) &&
				action !== "none";
	const composerSendLabel =
		answering || command === null
			? sendLabel(action, answering)
			: command.command.id === "compact"
				? "Compact transcript"
				: command.command.id === "goal"
					? goalCommand || !conversation?.goal
						? "Set goal"
						: "Clear goal"
					: command.command.label;
	async function send() {
		if (questionBatch) {
			await answerWithComposer(questionBatch);
			return;
		}
		if (command !== null) {
			void applyCommand();
			return;
		}
		if (imageSelection.getSnapshot().busy) return;
		const kind = liveSendKind();
		if (!service || kind === null) return;
		setActionError(null);
		try {
			await document.submit(async (text, images) => {
				store.getState().setDraft(text);
				return deliver(service, kind, text, images);
			});
		} catch {
			// A refused Send adds no text of its own: the draft stays, and the
			// unconfirmed ghost document.submit leaves says what happened and
			// what to do.
		}
	}
	// Your text as the free answer to the question the dock is on. When that
	// completes the ask, every answer goes out as one message; otherwise the
	// dock returns at the next unanswered question and the composer steps
	// aside again (ruling 14).
	async function answerWithComposer(batch: AskBatch) {
		if (!questionDraft.loaded) return;
		const text = document.getSnapshot().record.draft;
		const result = answerWithText(
			batch.questions,
			questionDraft.selections,
			questionDraft.activeIndex,
			text,
		);
		if (result.message === null) {
			questionDraft.setSelections(() => result.selections);
			if (result.nextIndex !== undefined)
				questionDraft.setActiveIndex(result.nextIndex);
			document.edit("");
			setComposerBack(false);
			return;
		}
		if (await sendAnswers(batch, result.selections)) {
			// Only the text you typed is cleared: an edit made while the answer
			// was on its way stays.
			if (document.getSnapshot().record.draft === text) document.edit("");
		}
	}
	// Whether a message can go out right now, and whether it sends or queues:
	// routed on what is true at the press, the way the web composer re-derives
	// at submit, since a turn may have started or ended since render. Send and
	// an error row's Retry both ask.
	function liveSendKind(): "send" | "queue" | null {
		const live = store.getState();
		if (
			!service ||
			!ready ||
			controls?.getSnapshot().pending != null ||
			unconfirmedSend !== null ||
			live.pendingMutation?.status === "pending" ||
			!live.conversation ||
			// A waiting question takes Send's text as its answer instead.
			pendingQuestions(live.conversation).length > 0
		)
			return null;
		const liveAction = sendAction(
			live.conversation,
			live.pendingMutations,
			connectionReady.current,
		);
		if (liveAction === "none") return null;
		return liveAction === "queue" ? "queue" : "send";
	}
	// Sends or queues one message the way Send does, and says whether the hub
	// took it.
	async function deliver(
		through: NonNullable<typeof service>,
		kind: "send" | "queue",
		text: string,
		images: Parameters<typeof buildComposerInput>[1],
	) {
		const previous = store.getState().lastAcceptedMutation;
		await store.getState()[kind](through, buildComposerInput(text, images));
		const accepted = store.getState().lastAcceptedMutation;
		return accepted != null && accepted !== previous && accepted.kind === kind;
	}
	// An error row's Retry sends Jesse's sentence as your message through
	// Send's own path (ruling 26), leaving whatever you were typing alone.
	async function retryFailedTurn() {
		const kind = liveSendKind();
		if (!service || kind === null) return;
		setActionError(null);
		try {
			await document.submitText(RETRY_MESSAGE, (text, images) =>
				deliver(service, kind, text, images),
			);
		} catch {
			// As with Send, a refusal leaves the unconfirmed ghost to say so.
		}
	}
	function runErrorAction(errorAction: ErrorAction) {
		if (errorAction === "resume") void controls?.resume();
		else if (errorAction === "signIn")
			navigation.navigate("Providers", { hubId: route.params.hubId });
		else void retryFailedTurn();
	}
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
	// Everything waiting to reach the agent, as ghosts above the composer
	// (spec 8.5 and 14). A refused row keeps Edit whenever its record can come
	// back; whether the composer can take it right now is the bubble's
	// canEdit, so an occupied composer shows Edit disabled with the reason.
	const recoveryRows = projectNativeMutationRecovery(
		recovery.targetKey,
		recovery.snapshot,
		() => true,
	);
	const allGhosts = whatCanActNow(
		ghosts(
			conversation,
			snapshot.pendingMutations,
			unconfirmedSend === null
				? null
				: {
						text: unconfirmedSend,
						sentText: translateAttachmentMarkers(
							unconfirmedSend,
							draft.record.unconfirmedImages,
						),
					},
			recoveryRows,
		),
		{ connected, composerLoaded: draft.loaded },
	);
	const canEditGhost = document.canRestoreRecoveredDraft();
	const ghostEditHint = document.recoveredRestoreHint();
	const [ghostBusy, setGhostBusy] = useState(false);
	const ghostBusyRef = useRef(false);
	// One ghost action at a time, each reading the live session at the press.
	// It returns its toast rather than showing it, so the caller shows it
	// where you are: the session's toast, or the Queue sheet's own.
	async function oneGhostAction(
		run: () => Promise<ToastMessage | null>,
	): Promise<ToastMessage | null> {
		if (ghostBusyRef.current) return null;
		ghostBusyRef.current = true;
		setGhostBusy(true);
		try {
			return await run();
		} finally {
			ghostBusyRef.current = false;
			setGhostBusy(false);
		}
	}
	function runGhostAction(ghost: Ghost, action: GhostAction) {
		return oneGhostAction(() => ghostAction(ghost, action));
	}
	async function ghostAction(
		ghost: Ghost,
		action: GhostAction,
	): Promise<ToastMessage | null> {
		const origin = ghost.origin;
		if (origin.kind === "queue") return queuedGhostAction(origin.entry, action);
		if (action === "check") {
			await checkDelivery();
			return null;
		}
		if (origin.kind === "draft") {
			if (action === "discard") document.dismiss();
			else if (action === "edit") document.restore();
			return null;
		}
		if (origin.kind === "recovery") {
			const row = recoveryRows.find(
				(candidate) =>
					candidate.clientMutationId === origin.row.clientMutationId,
			);
			if (!row) return null;
			if (action === "discard") recovery.discard(row);
			else if (action === "edit") {
				setActionError(null);
				if (!document.restoreRecoveredDraft(row.text))
					setActionError("This message could not be restored to the draft.");
			}
		}
		return null;
	}
	// Reads the session again after a ghost action; a failed read leaves the
	// session as it was, which the action's own outcome already reports.
	function rehydrateQuietly(live: NonNullable<typeof service>) {
		return store
			.getState()
			.rehydrate(live, activitySink)
			.catch(() => undefined);
	}
	// A change to the hub's queue: whether the hub took it. The session is
	// read again either way, so the ghosts show the queue as it now is.
	async function queueChange(
		live: NonNullable<typeof service>,
		change: () => Promise<unknown>,
	): Promise<boolean> {
		try {
			await change();
			return true;
		} catch {
			return false;
		} finally {
			await rehydrateQuietly(live);
		}
	}
	// Check: read the session again, then show its live end, where the
	// message is if it arrived.
	async function checkDelivery() {
		if (service && connectionReady.current) await rehydrateQuietly(service);
		jumpToLive();
	}
	async function queuedGhostAction(
		entry: QueueEntryRef,
		action: GhostAction,
	): Promise<ToastMessage | null> {
		const live = store.getState().conversation;
		const instanceId = live?.instanceId;
		if (!service || !connectionReady.current || !live || !instanceId)
			return null;
		// The message you saw, wherever it sits now; nothing once it has left
		// the queue (Review Focus 2).
		const target = ghostActionTarget(live.queue, entry);
		if (!target) return null;
		const cancel = () =>
			queueChange(service, () =>
				service.cancelQueued(target.index, target.id, instanceId),
			);
		if (action === "steerNow" || action === "sendNow") {
			if (queueActionRefusal(live, "promote") !== null) return STEER_FAILED;
			const promoted = await queueChange(service, () =>
				service.promoteQueuedAsSteer(target.index, target.id, instanceId),
			);
			return promoted ? null : STEER_FAILED;
		}
		if (action === "cancel")
			return (await cancel())
				? null
				: { text: "Couldn't take this message out of the queue." };
		if (action === "edit") {
			const text = live.queue?.texts?.[target.index];
			if (!text?.trim() || !document.getSnapshot().loaded) return null;
			// The text goes into the composer before the message leaves the
			// queue, so a failed cancel never loses it (web QueueStrip).
			const merged = mergeDraftText(document.getSnapshot().record.draft, text);
			document.edit(merged);
			const cancelled = await cancel();
			requestAnimationFrame(() => {
				composerInput.current?.setNativeProps({
					selection: { start: merged.length, end: merged.length },
				});
				composerInput.current?.focus();
			});
			return cancelled
				? null
				: { text: "Moved to your message, but it's still queued." };
		}
		return null;
	}
	// Steer all now: the whole live queue, whatever it holds at the press.
	async function steerWithQueue(): Promise<ToastMessage | null> {
		const live = store.getState().conversation;
		const instanceId = live?.instanceId;
		if (!service || !connectionReady.current || !live?.queue || !instanceId)
			return null;
		if (queueActionRefusal(live, "drainAll") !== null) return STEER_ALL_FAILED;
		const revision = live.queue.revision;
		const drained = await queueChange(service, () =>
			service.drainAsSteer(revision, instanceId),
		);
		return drained ? null : STEER_ALL_FAILED;
	}
	// The Queue sheet's host is memoized on what it shows, so its actions
	// reach this render's functions through a ref.
	const liveGhostActions = {
		act: runGhostAction,
		steerAll: () => oneGhostAction(steerWithQueue),
	};
	const ghostActions = useRef(liveGhostActions);
	ghostActions.current = liveGhostActions;
	const queuedGhosts = allGhosts.filter(
		(ghost) => ghost.origin.kind === "queue",
	);
	const queuedKey = JSON.stringify(queuedGhosts);
	// Like the per-message actions, it goes to the hub, so it isn't offered
	// while the hub is away.
	const canSteerAll =
		connected &&
		!!conversation &&
		(conversation.queue?.depth ?? 0) > 1 &&
		conversationControls(conversation).drainQueue;
	// biome-ignore lint/correctness/useExhaustiveDependencies: queuedKey stands in for queuedGhosts, a new array each render
	const queueHost = useMemo<QueueHost>(
		() => ({
			ghosts: queuedGhosts,
			disabled: ghostBusy,
			act: (ghost, action) => ghostActions.current.act(ghost, action),
			showOnSession: toaster.show,
			...(canSteerAll
				? { steerAll: () => ghostActions.current.steerAll() }
				: {}),
		}),
		[queuedKey, ghostBusy, canSteerAll, toaster.show],
	);
	useProvideSheetHost(
		queueHosts,
		sheetKey(route.params.hubId, route.params.ref),
		queueHost,
	);
	// The docks, the tray and the composer, by one rule (bottomStack.ts).
	const approval = conversation?.pendingEscalations[0] ?? null;
	const bottom = bottomStack({
		approvalPending: approval !== null,
		questionPending: questionBatch !== null,
		folded: questionFolded,
		composerBack,
	});
	const composerShown = canCompose && bottom.composer;
	// What sits above the composer: failures only you can act on, then
	// everything waiting to reach the agent. While the composer is hidden
	// (the dock is open) it sits in the composer's place, so a queued
	// message never drops out of sight.
	const waitingForAgent = (
		<>
			<ErrorMessage message={actionError} />
			{draft.error ? (
				<View style={{ alignItems: "flex-start" }}>
					<ErrorMessage message={draft.error} />
					<Action tone="accent" onPress={document.retry}>
						{draft.loaded ? "Retry saving" : "Retry loading draft"}
					</Action>
				</View>
			) : null}
			{recovery.failed ? (
				<RecoveryFailure error={recovery.error} onRetry={recovery.retry} />
			) : null}
			<QueuedMessages
				ghosts={allGhosts}
				disabled={ghostBusy}
				canEdit={canEditGhost}
				editHint={ghostEditHint}
				draftAttachments={
					<ImageAttachments
						document={document}
						selection={imageSelection}
						uncertain
					/>
				}
				onAction={(ghost, action) => {
					void runGhostAction(ghost, action).then((message) => {
						if (message) toaster.show(message);
					});
				}}
				onMore={openQueue}
			/>
		</>
	);
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
					title={conversation?.name || route.params.title}
					hubName={activeProfile?.name ?? "Hub"}
					connected={connected}
					deletionAvailable={deletionAvailable}
					close={() => setSessionMenuOpen(false)}
					choose={openSessionDestination}
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
			activityContext?.hubId === route.params.hubId &&
			activityContext.ref === route.params.ref ? (
				<ActivitySheet
					key={`${route.params.hubId}:${route.params.ref}`}
					client={client ?? activityContext.client}
					sessionRef={route.params.ref}
					threadId={conversation?.threadId ?? activityContext.threadId}
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
							onLayout={(event) => {
								readerViewportHeight.current = event.nativeEvent.layout.height;
								setLayoutRevision((revision) => revision + 1);
							}}
							data={timelineRows}
							// The live run changes when a turn starts or ends, without the
							// rows changing; its row must re-render to fold or unfold.
							extraData={liveRun}
							ListFooterComponent={
								presentation.usage ? (
									<TranscriptUsage {...presentation.usage} />
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
											snapshot.conversation?.capabilities?.forkFromTurn
												? forkMessage
												: undefined
										}
										forkDisabled={
											!connected || !focused || snapshot.status !== "open"
										}
										quote={quote}
										live={item.id === liveRun}
										delegates={conversation?.delegates}
										openSubagent={openSubagent}
										answerFor={answerFor}
										errorActionFor={(row) =>
											conversation
												? // Retry shows only when a press would send.
													errorAction(row, conversation, liveSendKind() !== null)
												: null
										}
										onErrorAction={runErrorAction}
									/>
								</View>
							)}
							// Room at the end for the Next capsule (spec 8.3).
							contentContainerStyle={{
								padding: 16,
								paddingTop: 16 + sessionHeaderHeight,
								paddingBottom: 60,
							}}
							// Older history loading above never moves what you read.
							maintainVisibleContentPosition={{ minIndexForVisible: 0 }}
							onContentSizeChange={(_width, height) => {
								readerContentHeight.current = height;
								setLayoutRevision((revision) => revision + 1);
								if (readerLatest.current)
									(
										timeline.current?.getScrollResponder() as ScrollView | null
									)?.scrollToEnd({ animated: false });
							}}
							scrollEventThrottle={100}
							onScroll={(event) => {
								const { contentOffset, contentSize, layoutMeasurement } =
									event.nativeEvent;
								const y = contentOffset.y;
								listOffset.current = y;
								headerHiding.onScroll(
									y,
									readerDragging.current || readerMomentum.current,
								);
								if (!focused) return;
								if (y + layoutMeasurement.height >= contentSize.height - 48) {
									if (awayKeys !== null) setAwayKeys(null);
									turnsSeen.current = latestSettledTurn(conversation) ?? turnsSeen.current;
								} else if (awayKeys === null) {
									setAwayKeys(new Set(timelineRows.map(readerKey)));
								}
								if (captureSuppressed.current) return;
								// Older history loads as you near the top (spec 8.2).
								if (y < 800) loadOlderPage();
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
										turnsSeen.current,
									);
									const anchor = readerAnchor.current;
									const measurement = readerMeasurements.current.get(
										readerKey(visible),
									);
									if (anchor && measurement)
										appliedReaderRestore.current = {
											...measurement,
											scrollOffset: y,
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
								const anchor = readerAnchor.current;
								const targetIndex = anchor
									? resolveReaderAnchor(anchor, timelineRows)
									: null;
								if (
									targetIndex !== null &&
									readerMeasurements.current.has(
										readerKey(timelineRows[targetIndex]),
									)
								)
									return;
								const measurementProgress =
									targetIndex === null
										? -1
										: furthestMeasuredRowBeforeTarget(
												timelineRows,
												targetIndex,
												[...readerMeasurements.current.values()],
											);
								if (
									!readerRestoreAttempts.current.retryUnmeasured(
										measurementProgress,
									)
								)
									return;
								appliedReaderRestore.current = null;
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
							ListHeaderComponent={
								<View style={{ gap: 12, paddingBottom: 16 }}>
									{readFailures >= 3 ? (
										<Text
											style={{
												fontSize: 13,
												lineHeight: 18,
												color: colors.palette.inkLow,
											}}
										>
											Couldn't load this session. Trying again on its own.
										</Text>
									) : null}
									{/* A failed read says so above, once, and retries on its own. */}
									<ErrorMessage
										message={snapshot.status === "error" ? null : snapshot.error}
									/>
									{connected &&
									permitted &&
									!permitted.send &&
									!permitted.steer &&
									!permitted.queue ? (
										<Copy muted>Sending is unavailable for this session.</Copy>
									) : null}
								</View>
							}
							// Until the conversation first loads, three quiet blocks stand
							// in for it. A loaded conversation with no rows shows nothing:
							// the composer's placeholder invites.
							ListEmptyComponent={conversation ? null : <TranscriptSkeleton />}
						/>
						<View
							pointerEvents="box-none"
							style={{ position: "absolute", top: 0, left: 0, right: 0 }}
							onLayout={(event) =>
								setSessionHeaderHeight(event.nativeEvent.layout.height)
							}
						>
							<SessionHeader
								status={connectionText}
								chips={chips}
								hidden={headerHiding.hidden}
								onChip={openChip}
								notes={
									notesPreview ? (
										<NotesBar
											preview={notesPreview}
											onPress={() => {
												Keyboard.dismiss();
												// Showing your note, the editor opens with the caret at its end.
												navigation.navigate("NotesSheet", {
													hubId: route.params.hubId,
													ref: route.params.ref,
													focusEditor: notesPreview.glyph === "person",
												});
											}}
										/>
									) : undefined
								}
							/>
						</View>
						<View
							pointerEvents="box-none"
							style={{
								position: "absolute",
								left: 0,
								right: 0,
								bottom: 10,
								alignItems: "center",
							}}
						>
							<NewContentPill
								count={awayKeys ? newRowCount(timelineRows, awayKeys) : 0}
								onPress={jumpToLive}
							/>
						</View>
					</View>
					<View style={{ flexShrink: 1, maxHeight: "80%", marginTop: 8, gap: 4 }}>
						<ScrollView
							style={{ flexGrow: 0, flexShrink: 1 }}
							contentContainerStyle={{ gap: 4, paddingHorizontal: 12 }}
							keyboardShouldPersistTaps="handled"
							nestedScrollEnabled
						>
							{composerShown ? null : waitingForAgent}
							{conversation?.goal ? (
								<View
									style={{ flexDirection: "row", flexWrap: "wrap", gap: 4 }}
								>
									<Action
										tone="quiet"
										onPress={() => {
											Keyboard.dismiss();
											setSessionOpen(true);
										}}
									>
										{`Goal · ${conversation.goal.status}`}
									</Action>
								</View>
							) : null}
							<View style={{ flexDirection: "row", flexWrap: "wrap", gap: 4 }}>
								{controlsState?.error &&
								(controlsState.lastAction === "changeModel" ||
									controlsState.lastAction === "setVisionModel" ||
									controlsState.lastAction === "setReasoningEffort") ? (
									<Action
										tone="quiet"
										onPress={() => {
											Keyboard.dismiss();
											setComposerSetting(
												controlsState.lastAction === "changeModel"
													? "model"
													: controlsState.lastAction === "setVisionModel"
														? "vision"
														: "reasoning",
											);
										}}
									>
										Review settings error
									</Action>
								) : null}
							</View>
						</ScrollView>
						<View>
							{/* The toast floats 10pt above the tray, or above the
							    composer when there is no tray. */}
							<View
								pointerEvents="box-none"
								style={{
									position: "absolute",
									left: 0,
									right: 0,
									bottom: "100%",
									paddingBottom: 10,
									alignItems: "center",
								}}
							>
								<Toast toast={toaster.toast} dismiss={toaster.dismiss} />
							</View>
							{bottom.dock === "approval" && approval ? (
								<ApprovalDock
									// A new approval starts with nothing decided.
									key={approval.escalationId}
									request={approval}
									// Null while the hub is away: the dock still says what
									// waits, without Allow or Deny.
									controls={approvalControls}
									waiting={(conversation?.pendingEscalations.length ?? 1) - 1}
									onDecided={(allowed) =>
										toaster.show({ text: allowed ? "Allowed once" : "Denied" })
									}
								/>
							) : null}
							{(bottom.dock === "question" ||
								bottom.dock === "foldedQuestion") &&
							questionBatch ? (
								<QuestionDock
									questions={questionBatch.questions}
									draft={questionDraft}
									ready={
										ready &&
										!!permitted?.send &&
										draft.loaded &&
										!draft.error &&
										unconfirmedSend === null
									}
									sending={draft.submitting || questionBatch.sending}
									folded={bottom.dock === "foldedQuestion"}
									onFold={setQuestionFolded}
									onOtherAnswer={() => {
										setComposerBack(true);
										requestAnimationFrame(() => composerInput.current?.focus());
									}}
									onSend={(selections) => {
										void sendAnswers(questionBatch, selections);
									}}
									error={answerError}
								/>
							) : null}
							{bottom.tray ? (
								<LiveStatusTray
									session={conversation}
									frames={frames}
									connected={connected}
									canStop={!!permitted?.stop}
									stopping={stopping || pending}
									onStop={() => {
										void stop();
									}}
									onJumpToLive={jumpToLive}
								/>
							) : null}
							{composerShown ? (
								<Composer
									value={draft.record.draft}
									editable={draft.loaded}
									onChangeText={(text) => document.edit(text)}
									onSelectionChange={setComposerSelection}
									inputRef={composerInput}
									placeholder={composerPlaceholder(action, answering)}
									// Under an open dock, whose own button reads "Send answer",
									// this Send says it sends what you typed.
									sendLabel={
										bottom.dock === "question"
											? "Send your answer"
											: composerSendLabel
									}
									sendEnabled={sendEnabled}
									onSend={() => {
										void send();
									}}
									onPhotoLibrary={() => {
										Keyboard.dismiss();
										void imageSelection.choose();
									}}
									onCamera={() => {
										Keyboard.dismiss();
										void imageSelection.choose("camera");
									}}
									settings={bottom.modelChip ? composerSettings : null}
									above={
										<>
											{waitingForAgent}
											{connected && client && conversation && slashToken ? (
												<CommandCompletion
													// Suggestions use at most 40% of the composer's 80% viewport cap.
													maxHeight={Math.min(160, viewportHeight * 0.32)}
													client={client}
													sessionRef={route.params.ref}
													session={conversation}
													query={slashToken.query}
													close={() => setCompletionClosedAt(draft.record.draft)}
													choose={(item) => {
														if (
															document.getSnapshot().record.draft !==
															draft.record.draft
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
																selection: {
																	start: inserted.caret,
																	end: inserted.caret,
																},
															});
															composerInput.current?.focus();
														});
													}}
												/>
											) : null}
											<ImageAttachments
												document={document}
												selection={imageSelection}
											/>
											<ErrorMessage message={imageState.error} />
										</>
									}
								/>
							) : null}
						</View>
					</View>
				</View>
			</KeyboardAvoidingView>
		</SafeAreaView>
	);
}
