import type { CellRendererProps } from "@react-native/virtualized-lists";
import { useHeaderHeight } from "@react-navigation/elements";
import type { NavigatorScreenParams } from "@react-navigation/native";
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
	ActionSheetIOS,
	Alert,
	AppState,
	FlatList,
	Keyboard,
	Platform,
	Pressable,
	ScrollView,
	Text,
	TextInput,
	View,
} from "react-native";
import { KeyboardAvoidingView } from "react-native-keyboard-controller";
import { SafeAreaView } from "react-native-safe-area-context";
import {
	type AskBatch,
	type AskQuestionRef,
	buildComposerInput,
	formatQuoteBlock,
	mergeDraftText,
	type ModelListResponse,
	type NavigationSessionSummary,
	type TranscriptDisplayConfigV1,
	translateAttachmentMarkers,
} from "@evener/appwire-client";
import { createConversationService } from "../../mobile/src/services/conversation";
import { createActivityStore } from "../../mobile/src/state/activity";
import { type ConversationState, createConversationStore, olderPageKey } from "../../mobile/src/state/conversation";
import {
	createConversationMutationPendingPort,
	type ConversationMutationSubmitter,
} from "../../mobile/src/state/conversationMutation";
import { useAlertedRecently, useNextUsed } from "./alerts/alertsContext";
import { ApprovalControls } from "./approvalControls";
import { useMarkSeenInFront } from "./board/sessionSeen";
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
import { goalObjective, submitGoalCommand } from "./goalCommand";
import type { HubRoutes } from "./hub/hubSheetContext";
import { ImageAttachments } from "./ImageAttachments";
import { ImageSelection } from "./imageSelection";
import { projectNativeMutationRecovery, RecoveryFailure, useRecoveryPanel } from "./MutationRecoveryPanel";
import { useNativePreferences } from "./NativePreferencesProvider";
import { drafts } from "./nativeDrafts";
import { nativeImagePicker } from "./nativeImagePicker";
import { createNativeMutationHost, createDurableSubmitter, type NativeMutationHost } from "./nativeMutationHost";
import { getNativeMutationRuntime, nativeMutationTargetKey } from "./nativeMutationRuntime";
import { type SessionSeed, seedFromSession } from "./newSession/launchSetup";
import { readerPositions } from "./nativeReaderPosition";
import { MessageDocuments } from "./reader/DocumentChip";
import { documentReferences, fileWrites, writtenPaths } from "./reader/documentReferences";
import { documentMemory } from "./reader/nativeDocumentMemory";
import { documentFreshness, type SessionDocument, sessionDocuments } from "./reader/sessionDocuments";
import { locateSession, type SessionLocation } from "./navigationReveal";
import { queueHosts, type QueueHost } from "./QueueSheet";
import {
	composeQuestionAnswers,
	pendingQuestions,
	type QuestionSelections,
	questionsIdentity,
} from "./questionAnswers";
import { BarFrame } from "./design/BarFrame";
import { navBarGlassOptions, reservedUnderGlass, useSystemGlass } from "./design/systemGlass";
import { listContentMinHeight, underBar, useBarHeight } from "./design/underBar";
import { ApprovalDock } from "./session/ApprovalDock";
import { shrinkingScroller } from "./session/dockCard";
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
	readerAnchorRow,
	readerKey,
	resolveReaderAnchor,
	restoreReaderCommand,
	type AppliedRestore,
	exactRestoreDue,
} from "./readerPosition";
import { type SessionDestination, SessionMenu } from "./SessionMenu";
import { type ErrorAction, errorAction, RETRY_MESSAGE } from "./session/errorAction";
import {
	type Ghost,
	type GhostAction,
	ghostActionTarget,
	ghosts,
	type QueueEntryRef,
	whatCanActNow,
} from "./session/ghosts";
import { FloatingStack, transcriptEndRoomAt } from "./session/FloatingStack";
import { nativeDisclosureStore } from "./nativeDisclosure";
import { sessionDisclosureScope } from "./session/disclosureKeys";
import { atEnd, pagesOlder, useLiveEndFollow } from "./session/liveEndFollow";
import { NewContentPill } from "./session/NewContentPill";
import { BackButton } from "./session/BackButton";
import { liveOrder, neighbor, nextNavigation, nextQueue, othersNeedingYou } from "./session/fleetOrder";
import { NextCapsule } from "./session/NextCapsule";
import { useFleet } from "./session/useFleet";
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
import { SessionControls, useControlsState } from "./sessionControls";
import { useConnectionStatusText } from "./board/connectionStatus";
import { Composer, ModelChip } from "./session/Composer";
import { ComposerFocus } from "./session/composerFocus";
import { type ModelHost, modelHosts } from "./session/ModelSheet";
import { type CommandsHost, commandHosts, insertInvocation } from "./session/CommandsSheet";
import { FindBar } from "./session/FindBar";
import { findMatches, matchLabel, stepMatch } from "./session/findInSession";
import { currentLevel, displayForLevel, levelToast } from "./session/detailLevels";
import { detailLevels } from "./session/nativeDetailLevels";
import { outboxFlush } from "./outbox/nativeOutboxFlush";
import { type OfflineTarget, offlineRequest } from "./outbox/offlineSend";
import { composerPlaceholder, sendAction, sendLabel } from "./session/sendAction";
import { NotesBar } from "./session/NotesBar";
import { type NotesHost, notesHosts } from "./session/NotesSheet";
import { SessionHeader, useHeaderHiding } from "./session/SessionHeader";
import { type SessionMenuAction, sessionMenu } from "./session/sessionMenu";
import {
	confirmShutDown,
	type SessionInfoAction,
	type SessionInfoHost,
	sessionInfoHosts,
} from "./session/SessionInfoSheet";
import { SessionNotice } from "./session/SessionNotice";
import { useSessionRestart } from "./session/sessionRestart";
import {
	canDeleteSavedSession,
	canOpenModelSheet,
	latestForkPoint,
	modelChipLabel,
	sessionHosts,
} from "./session/sessionFacts";
import { type ChipKind, contextChips, SHUT_DOWN, sessionStateLine } from "./session/sessionState";
import { canWriteHumanNote, NotesController, notesBarPreview, type SaveOutcome } from "./session/sessionNotes";
import { SessionTitle } from "./session/SessionTitle";
import { LiveStatusTray, useFrameCounter } from "./session/StatusTray";
import { sheetKey, useProvideSheetHost } from "./sheet/sheetHosts";
import { screenInFront, useScreenInFront } from "./sheet/useScreenInFront";
import { takeQuote } from "./session/pendingQuote";
import { type Coordinator, SubagentPanel } from "./subagents/SubagentPanel";
import { type SubagentRow, timeInState } from "./subagents/subagentModel";
import { TimelineItem } from "./TimelineItem";
import { Toast, type ToastMessage, useToast } from "./Toast";
import { TranscriptUsage } from "./TranscriptUsage";
import { groupTimeline, type TimelineRow, timelineGap } from "./timeline";
import { projectNativeTranscript } from "./transcriptPresentation";
import { Action, Copy, ErrorMessage, styles, useColors, useTextScale } from "./ui";
import { haptic } from "./haptics";

const NO_QUESTIONS: AskQuestionRef[] = [];
const STEER_FAILED = { text: "Couldn't steer with this message now." };
/** The most of its room the Session's bottom bar may take (spec 8.1). */
const BAR_MAX_SHARE = 0.8;
const STEER_ALL_FAILED = { text: "Couldn't steer with these messages now." };

/** Find in session while it's open: what you typed, the current match's
 * reader key, whether older history is being searched, and whether that
 * search reached the start of history with nothing older. */
interface FindState {
	query: string;
	key: string | null;
	seeking: boolean;
	exhausted: boolean;
}

/** A new query starts a new search: no current match yet, and older history
 * is searched only when there is something to look for. */
function newFind(query: string): FindState {
	return { query, key: null, seeking: query.trim() !== "", exhausted: false };
}

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
	Project: {
		hubId: string;
		projectKey: string;
		title: string;
		archived?: boolean;
		tier?: "current" | "recent" | "archived";
	};
	Hubs: undefined;
	Sessions: undefined;
	/** The Hub sheet (spec 12), a modal holding its own stack of pages. */
	Hub: NavigatorScreenParams<HubRoutes>;
	/** New session (spec 11), a modal holding its own stack of pages. `like`
	 * opens it on a session's setup ("New session like this"). */
	NewSession: { hubId: string; hubName: string; like?: SessionSeed };
	/** openedBy says Next opened this session (ruling 2), so Next from it
	 * replaces it. slideFrom says the title's swipe opened it as the
	 * previous ("left") or next ("right") session in Live order, the side it
	 * slides in from (spec 6). location.ts persists neither. */
	Conversation: {
		hubId: string;
		ref: string;
		title: string;
		openedBy?: "next";
		slideFrom?: "left" | "right";
	};
	TasksSheet: { hubId: string; ref: string; threadId: string; hasTasks: boolean };
	NotesSheet: { hubId: string; ref: string; focusEditor?: boolean };
	QueueSheet: { hubId: string; ref: string };
	SessionInfoSheet: { hubId: string; ref: string };
	ModelSheet: { hubId: string; ref: string; setting: "model" | "vision" };
	CommandsSheet: { hubId: string; ref: string };
	RowMenuSheet: { hubId: string; ref: string; archived: boolean };
	Reader: {
		hubId: string;
		/** The document's session, whose folder holds the file; Open session,
		 * Quote in reply and the review go to it too (ruling 16). */
		sessionRef: string;
		path: string;
		/** That session's title, for Open session, Quote in reply and the review. */
		sessionTitle: string;
		/** When the file was last written, as its opener reported it. */
		updatedAt?: string;
	};
	OutlineSheet: { hubId: string; sessionRef: string; path: string };
	CommentSheet: {
		hubId: string;
		sessionRef: string;
		path: string;
		blockIndex: number;
		blockHash: string;
		/** The words the comment is on: a selection, or its block's words. */
		quote: string;
	};
	CommentsSheet: ReviewSheetParams;
	ReviewSheet: ReviewSheetParams;
	/** A coordinator's subagents (spec 9). */
	Subagents: { hubId: string; ref: string; threadId: string; title: string };
	/** Ask a subagent's coordinator to stop it (spec 9, ruling 10). */
	StopSubagentSheet: { hubId: string; coordinator: Coordinator; ref: string };
	/** A subagent's own session, over its coordinator's (ruling 30). */
	Subagent: { hubId: string; ref: string; title: string; coordinator: Coordinator };
	/** The session's documents as they were when the sheet opened (ruling 26). */
	FilesSheet: { hubId: string; ref: string; title: string; documents: SessionDocument[] };
};

/** A document's comments and its review: the document, and its session's title,
 * which the review goes to (ruling 16). */
type ReviewSheetParams = { hubId: string; sessionRef: string; path: string; sessionTitle: string };

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
	subagents: "subagents",
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
	subagentOf,
}: NativeStackScreenProps<Routes, "Conversation"> & {
	/** This session is a subagent's, opened from its coordinator (the
	 * "Subagent" route): the coordinator whose tree it sits in. */
	subagentOf?: Coordinator;
}) {
	const { activeProfile, client, state: connectionState } = useConnection();
	const focused = useScreenInFront(route.key);
	const colors = useColors();
	const [sessionMenuOpen, setSessionMenuOpen] = useState(false);
	// Find in session (ruling 29), open while non-null. The current match is
	// remembered by its row's reader key, since older pages prepend rows.
	const [find, setFind] = useState<FindState | null>(null);
	const headerHeight = useHeaderHeight();
	// Where the device has Liquid Glass, the nav bar is the system's glass
	// over the transcript (spec 16.3): the screen starts under it, and the
	// header's own glass spans the bar and the rows under it.
	const navGlass = useSystemGlass();
	const underNavBar = navGlass ? headerHeight : 0;
	// Before paint, so a session never opens with an opaque bar that turns to
	// glass; Reduce Transparency's last known value (accessibilitySettings)
	// is there on the first render of every screen after the first.
	useLayoutEffect(() => {
		navigation.setOptions(navBarGlassOptions(navGlass, colors.background));
	}, [navigation, navGlass, colors.background]);
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
	const hubDisplayConfig = preferences.hubId === route.params.hubId ? preferences.config : null;
	// The detail level chosen for this session on this device replaces the
	// hub config's content (spec 8.7); nothing chosen leaves the hub's.
	const levels = detailLevels(route.params.hubId);
	useSyncExternalStore(levels.subscribe, levels.getRevision);
	const chosenLevel = levels.get(route.params.ref);
	const display = useMemo(() => displayForLevel(chosenLevel, hubDisplayConfig), [chosenLevel, hubDisplayConfig]);
	const displayConfig = display.config;
	const displayConfigRef = useRef<TranscriptDisplayConfigV1 | null>(displayConfig);
	displayConfigRef.current = displayConfig;
	const resolveDisplayConfig = useCallback(() => displayConfigRef.current, []);
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
						onReadStart: (ref, expectedThreadId) => mutationHostRef.current?.beginRead(ref, expectedThreadId),
						onReadComplete: (lease, response) => mutationHostRef.current?.reconcileRead(lease, response),
					})
				: null,
		[client, resolveDisplayConfig],
	);
	const currentDestination = useRef({ store, client });
	currentDestination.current = { store, client };
	const snapshot = store();
	// The next older page, or null: past the cursor, or above rows the cap
	// trimmed (the store names the oldest row kept).
	const olderPage = olderPageKey(snapshot);
	const timeline = useRef<FlatList>(null);
	const readerMeasurements = useRef(new Map<string, ReaderMeasurement>());
	const readerContentHeight = useRef(0);
	const readerViewportHeight = useRef(0);
	const [layoutRevision, setLayoutRevision] = useState(0);
	const readerAnchor = useRef<ReaderAnchor | null>(null);
	const appliedReaderRestore = useRef<AppliedRestore | null>(null);
	const readerRestoreAttempts = useRef(new ReaderRestoreAttempts());
	const readerPageAttempts = useRef(new Set<string>());
	const readerHeader = useRef(false);
	// The latest settled turn while the list sat at its end (ruling 31). Every
	// anchor carries it, so opening the session later can tell a newer reply
	// finished since.
	const turnsSeen = useRef<string | undefined>(undefined);
	// Where the session opened is decided once per route (spec 7.3).
	const openedFor = useRef<string | null>(null);
	// Following the live end, what moves the list, and the rows it held when
	// you left the end, which "↓ 3 new" counts against (session/liveEndFollow).
	// The store trims the 500-row cap only while the reader follows the end.
	// It hears a reader leave the end at once, and a return (the "↓ new" pill,
	// a drag or a coast to the end) only once the list is there: trimming the
	// top while the list still travels would move what it passes.
	const returningToEnd = useRef(false);
	const follow = useLiveEndFollow((following) => {
		returningToEnd.current = following;
		if (!following) store.getState().setFollowingLiveEnd(false);
	});
	function settleAtEnd(end: boolean) {
		if (!end || !returningToEnd.current) return;
		returningToEnd.current = false;
		store.getState().setFollowingLiveEnd(true);
	}
	// Rows keep their open state in one app-wide store, scoped by session, so a
	// row the list remounts keeps it. Leaving the session drops its scope, which
	// keeps the store bounded.
	useEffect(() => {
		const scope = sessionDisclosureScope(route.params.hubId, route.params.ref);
		return () => nativeDisclosureStore.clearScope(scope);
	}, [route.params.hubId, route.params.ref]);
	const captureSuppressed = useRef(false);
	const restoreFrame = useRef<number | null>(null);
	const composerInput = useRef<TextInput>(null);
	// The composer field's focus, for what steps aside while you type in it
	// (useComposerTyping); the find bar's or a sheet's field raising the
	// keyboard isn't typing in it.
	const [composerFocus] = useState(() => new ComposerFocus());
	// Puts the caret at `caret` in the composer and focuses it, on the next
	// frame so the field has the text the caret is placed in.
	const focusComposerAt = useCallback((caret: number) => {
		requestAnimationFrame(() => {
			composerInput.current?.setNativeProps({
				selection: { start: caret, end: caret },
			});
			composerInput.current?.focus();
		});
	}, []);
	const focusAfterModal = useRef(false);
	useFocusAfterModal(navigation, focusAfterModal, composerInput);
	// biome-ignore lint/correctness/useExhaustiveDependencies: Question ownership follows the destination store.
	const questionBatches = useMemo(() => new QuestionBatches(), [store]);
	const batches = useSyncExternalStore(questionBatches.subscribe, questionBatches.getSnapshot);
	useEffect(() => {
		const reconcile = () => questionBatches.reconcile(pendingQuestions(store.getState().conversation));
		reconcile();
		return store.subscribe(reconcile);
	}, [store, questionBatches]);
	const [actionError, setActionError] = useState<string | null>(null);
	const [commandPending, setCommandPending] = useState(false);
	const commandBusy = useRef(false);
	const projectLookup = useRef<AbortController | null>(null);
	const document = useMemo(
		() => drafts.open({ hubId: route.params.hubId, sessionRef: route.params.ref }),
		[route.params.hubId, route.params.ref],
	);
	const draft = useSyncExternalStore(document.subscribe, document.getSnapshot);
	const imageSelection = useMemo(() => new ImageSelection(document, nativeImagePicker), [document]);
	const imageState = useSyncExternalStore(imageSelection.subscribe, imageSelection.getSnapshot);
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
	const connected = connectionState === "ready" && activeProfile?.id === route.params.hubId;
	const connectionText = useConnectionStatusText();
	// The same debounced signal the connection bar itself waits on (spec 14:
	// "a blip shorter than this reconnects without a word"), so the chips
	// never flicker through a hide-and-show the bar stays silent for, and a
	// visible chip's tap (below, in openSessionDestination) never silently
	// does nothing during that same window (Calm).
	const chipsConnected = activeProfile?.id === route.params.hubId && connectionText === null;
	// Who else needs you (spec 13.2), for Back's count and Next.
	const fleet = useFleet(route.params.hubId, connected ? client : null, focused);
	const live = useMemo(() => liveOrder(fleet.bands), [fleet.bands]);
	// This session's own row, which the fleet re-reads when a turn ends.
	const fleetRow = useMemo(() => live.find((row) => row.ref === route.params.ref), [live, route.params.ref]);
	useMarkSeenInFront(
		route.params,
		focused,
		connected ? client : null,
		snapshot.status === "open" ? snapshot.conversation : null,
		fleetRow,
		fleet.seen,
	);
	const othersWaiting = useMemo(() => othersNeedingYou(fleet.bands, route.params.ref), [fleet.bands, route.params.ref]);
	const othersWaitingCount = othersWaiting.length;
	// Next serves whichever session alerted you most recently first (spec 8.3).
	const alertedRecently = useAlertedRecently();
	const queue = useMemo(
		() => nextQueue(fleet.bands, route.params.ref, alertedRecently),
		[fleet.bands, route.params.ref, alertedRecently],
	);
	const nextUsed = useNextUsed();
	useEffect(() => {
		// iPhone only: Android keeps its own back arrow.
		if (Platform.OS !== "ios") return;
		navigation.setOptions({
			headerLeft: () => <BackButton count={othersWaitingCount} onPress={() => navigation.goBack()} />,
		});
	}, [navigation, othersWaitingCount]);
	// Leaving for another session marks it seen, the way the Board marks a
	// row it opens (spec 8.3).
	// Next and a title-bar swipe both move to another session (spec 16.6's
	// lateral move).
	function leaveFor(target: NavigationSessionSummary) {
		haptic("selection");
		Keyboard.dismiss();
		fleet.seen.markRead(connected ? client : null, [target]);
	}
	function openNext(target: NavigationSessionSummary) {
		leaveFor(target);
		nextUsed();
		const params = {
			hubId: route.params.hubId,
			ref: target.ref,
			title: target.title,
			openedBy: "next" as const,
		};
		if (nextNavigation(route.params.openedBy) === "replace") navigation.replace("Conversation", params);
		else navigation.push("Conversation", params);
	}
	// A pan on the title replaces this session with its neighbor in Live
	// order (spec 6), keeping Next's mark so Next from there still replaces.
	const previous = neighbor(live, route.params.ref, -1);
	const next = neighbor(live, route.params.ref, 1);
	const hasPrevious = previous !== null;
	const hasNext = next !== null;
	function swipeToSession(direction: 1 | -1) {
		const target = direction === -1 ? previous : next;
		if (!target) return;
		leaveFor(target);
		navigation.replace("Conversation", {
			hubId: route.params.hubId,
			ref: target.ref,
			title: target.title,
			openedBy: route.params.openedBy,
			slideFrom: direction === -1 ? "left" : "right",
		});
	}
	// Touch and hold on Next lists who needs you, first eight (spec 8.3).
	function chooseNext() {
		if (Platform.OS !== "ios") return;
		const choices = queue.slice(0, 8);
		ActionSheetIOS.showActionSheetWithOptions(
			{
				options: [...choices.map((row) => row.title), "Cancel"],
				cancelButtonIndex: choices.length,
			},
			(index) => {
				const chosen = choices[index];
				if (chosen) openNext(chosen);
			},
		);
	}
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
			host = createNativeMutationHost(getNativeMutationRuntime(), route.params.hubId, route.params.ref, client);
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
			// Letting go writes nothing to storage, so ask the flush to look: a
			// message still waiting here would otherwise wait for the next
			// connection.
			void outboxFlush.flush();
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
		() => (service ? store.getState().resumeProjected(service, activitySink, route.params.ref) : Promise.resolve()),
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
			store
				.getState()
				.bindPendingMutationsIfUnbound(
					createConversationMutationPendingPort(
						getNativeMutationRuntime(),
						nativeMutationTargetKey(route.params.hubId, route.params.ref),
					),
				);
		} catch (error) {
			// The mutations database could not be opened. The conversation stays
			// usable; a later reconnect or remount retries, and the failure is
			// logged so it is not silent.
			console.error("ConversationScreen: durable pending seam bind failed", error);
		}
	}, [store, client, connected, focused, route.params.hubId, route.params.ref, snapshot.conversationGeneration]);
	const connectionReady = useRef(connected);
	connectionReady.current = connected;
	const bindingGeneration = snapshot.conversationGeneration;
	const bindingInstance = snapshot.conversation?.instanceId;
	// Stable across renders, so the row it is handed to does not rebuild when
	// the screen re-renders for a reason no fork can see (the reader keying,
	// the composer, a sheet opening).
	const forkMessage = useCallback(
		(entryIndex: number, preview: string) => {
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
		},
		[store, navigation, route.key, route.params, bindingInstance, bindingGeneration],
	);
	// The model catalog the screen last knew, handed to each new controls so
	// the model keeps its name across a screen pushed over this one and a
	// rebinding, while the catalog reads again (audit N6).
	const knownCatalog = useRef<ModelListResponse | null>(null);
	const controls = useMemo(() => {
		if (!service || !connected || !focused) return null;
		const refreshSession = async () => {
			if (store.getState().status === "open") await store.getState().rehydrate(service, activitySink);
			else await store.getState().resumeProjected(service, activitySink, route.params.ref);
			const current = store.getState();
			if (current.status !== "open" || current.error) throw new Error("Could not read the session.");
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
				if (scope === "destination") return current.ref === route.params.ref;
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
			knownCatalog.current,
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
							if (store.getState().error) throw new Error("Could not read the session.");
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
	const deletionAvailable = snapshot.conversation !== null && canDeleteSavedSession(snapshot.conversation);
	const openSessionDestination = useCallback(
		(destination: SessionDestination) => {
			setSessionMenuOpen(false);
			Keyboard.dismiss();
			const current = store.getState().conversation;
			if (!current) return;
			if (destination === "find") {
				// The one place a find opens, whatever menu asked for it.
				chooseSessionActionRef.current({ kind: "find" });
				return;
			}
			if (destination === "delete") {
				if (!canDeleteSavedSession(current)) return;
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
				navigation.navigate("SessionInfoSheet", {
					hubId: route.params.hubId,
					ref: route.params.ref,
				});
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
			// The Subagents list (spec 9), in place of the Activity sheet. A
			// subagent's screen opens its coordinator's, which lists every depth.
			navigation.navigate(
				"Subagents",
				subagentOf
					? { hubId: route.params.hubId, ...subagentOf }
					: {
							hubId: route.params.hubId,
							ref: route.params.ref,
							threadId: current.threadId,
							title: route.params.title,
						},
			);
		},
		[store, client, chipsConnected, route.params.hubId, route.params.ref, navigation, route.params.title],
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
	// A subagent's screen times its run as its Subagents row does, from the
	// row its panel reads (one number per subagent everywhere). An ended row
	// times how long ago it ended, which is no Working time, so a session still
	// winding down times its turn instead.
	const [subagentRow, setSubagentRow] = useState<SubagentRow | null>(null);
	const runMs = useCallback(
		(now: number) => (subagentRow?.state === "running" ? timeInState(subagentRow, now) : null),
		[subagentRow],
	);
	// The room the transcript keeps at its end for what floats over it.
	const transcriptEnd = transcriptEndRoomAt(useTextScale());
	// How tall the bottom bar stands over the transcript's end, null until it
	// lays out: the transcript runs under its glass (design/underBar).
	const bottomBar = useBarHeight();
	// The room the bottom bar sits over (useBarHeight measures any view's
	// height). The bar is capped at BAR_MAX_SHARE of it in points, so the cap
	// follows every re-layout of the room: a percentage cap could keep a height
	// from before a push's header inset landed (#3248).
	const bottomBarRoom = useBarHeight();
	const barHeight = bottomBar.height ?? 0;
	const listUnderBar = underBar(barHeight);
	const listLaidOut = bottomBar.height !== null && readerViewportHeight.current > 0;
	// The bar growing or shrinking (a dock, the tray, the keyboard) keeps a
	// follower at the end in the same frame; a reader anywhere else stays put,
	// since on iOS the bar is an inset the content size of a transcript taller
	// than its viewport doesn't depend on (a short one rests above the bar).
	// biome-ignore lint/correctness/useExhaustiveDependencies: only a new bar height re-pins.
	useLayoutEffect(() => {
		if (follow.state.current.following)
			(timeline.current?.getScrollResponder() as ScrollView | null)?.scrollToEnd({ animated: false });
	}, [barHeight]);
	const now = Date.now();
	const stateLine = conversation ? sessionStateLine(conversation, now, runMs(now)) : null;
	// Files & artifacts (spec 10.1): what the session wrote or linked, and
	// whether any of it is new or changed since you last opened it.
	const documents = useMemo(() => {
		const cwd = conversation?.cwd ?? "";
		return sessionDocuments(documentReferences(conversation?.turns ?? [], cwd), conversation?.sessionUrls ?? [], cwd);
	}, [conversation?.turns, conversation?.sessionUrls, conversation?.cwd]);
	const memory = documentMemory(route.params.hubId);
	useSyncExternalStore(memory.subscribe, memory.getRevision);
	const freshDocuments = documents.some(
		({ path, updatedAt }) =>
			documentFreshness(memory.lastRead({ sessionRef: route.params.ref, path }), updatedAt) !== "read",
	);
	const chips = conversation
		? contextChips(conversation, chipsConnected, { count: documents.length, fresh: freshDocuments })
		: [];
	const headerHiding = useHeaderHiding();
	// The header block floats over the list; the list reserves its height.
	// The block's measured height, and whether it was measured on the glass,
	// where it includes the nav bar's room.
	const [sessionHeader, setSessionHeader] = useState({ height: 0, onGlass: false });
	// What the list's top keeps clear: the bar where the screen runs under it,
	// and the rows.
	const reservedTop = reservedUnderGlass(headerHeight, sessionHeader, navGlass);
	// The block's rows (the connection line, the chips, the note).
	const headerRows = reservedTop - underNavBar;
	const listOffset = useRef(0);
	const reservedRows = useRef(0);
	// When the rows grow or shrink (the connection line comes or goes), the
	// list's top padding moves by the same amount; scrolling the list by it
	// too keeps every row where it was on screen. At the top the list stays
	// at the top, and the rows make room. The bar turning glass or opaque
	// asks for no scroll: it moves the list's frame by the bar's height as
	// the padding moves by the same, so the rows stay where they are.
	useLayoutEffect(() => {
		const change = headerRows - reservedRows.current;
		reservedRows.current = headerRows;
		if (change === 0 || listOffset.current <= 0) return;
		const target = Math.max(0, listOffset.current + change);
		// Set optimistically: the list's own onScroll is throttled
		// (scrollEventThrottle), so a second height change in the same window
		// must compose with where this scroll is already taking the list, not
		// with the last offset the list actually reported.
		listOffset.current = target;
		timeline.current?.scrollToOffset({ offset: target, animated: false });
	}, [headerRows]);
	function openChip(kind: ChipKind) {
		if (kind === "queue") openQueue();
		else if (kind === "files") openFiles();
		else openSessionDestination(SESSION_DESTINATIONS[kind]);
	}
	function openFiles() {
		Keyboard.dismiss();
		navigation.navigate("FilesSheet", {
			hubId: route.params.hubId,
			ref: route.params.ref,
			title: route.params.title,
			documents,
		});
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
	const canAside = connected && service !== null && !!conversation?.capabilities.forkFromTurn;
	const canShutDown =
		controls !== null && !!conversation?.capabilities.shutdown && !SHUT_DOWN.has(conversation.status.type);
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
	// What each session action does, for the ⋯ menu and the Session sheet. It
	// returns its toast rather than showing it: the menu shows it on the
	// session, and the sheet shows it where the person is (ruling 37).
	async function runSessionAction(action: SessionInfoAction): Promise<ToastMessage | null> {
		switch (action) {
			case "aside":
				if (!service) return null;
				try {
					const aside = await startAside(service);
					if (screenInFront(navigation, route.key)) openAside(aside.ref, aside.title);
					return null;
				} catch {
					return { text: "Couldn't start an aside." };
				}
			case "fork": {
				const point = latestForkPoint(store.getState().conversation?.items ?? []);
				if (point) forkMessage(point.entryIndex, point.preview);
				return null;
			}
			case "compact":
				// The hub can go away while the sheet asks; say so rather than
				// do nothing.
				if (!controls) return { text: "Couldn't compact the context: the hub isn't connected." };
				// A refusal shows as the controls' error line in the sheet.
				return (await controls.compact()) ? { text: "Compacting context" } : null;
			case "pin":
			case "delete":
				openSessionDestination(action);
				return null;
			case "archive":
				try {
					await archive(true);
				} catch {
					return { text: "Couldn't archive this session." };
				}
				return {
					text: "Session archived",
					action: {
						label: "Undo",
						run: () => void archive(false).catch(() => toaster.show({ text: "Couldn't undo the archive." })),
					},
				};
			case "shutDown": {
				if (!controls)
					return {
						text: "Couldn't shut down this session: the hub isn't connected.",
					};
				const stopped = await controls.shutdown();
				return {
					text: stopped ? "Session shut down" : "Couldn't shut down this session.",
				};
			}
		}
	}
	const showSessionToast = (message: ToastMessage | null) => {
		if (message) toaster.show(message);
	};
	function chooseSessionAction(action: SessionMenuAction) {
		switch (action.kind) {
			case "level":
				haptic("selection");
				levels.set(route.params.ref, action.level);
				toaster.show({ text: levelToast(action.level) });
				return;
			case "find":
				setFind(newFind(""));
				return;
			case "files":
				openFiles();
				return;
			case "subagents":
			case "tasks":
			case "notes":
			case "info":
			case "pin":
				openSessionDestination(SESSION_DESTINATIONS[action.kind]);
				return;
			case "aside":
			case "archive":
				void runSessionAction(action.kind).then(showSessionToast);
				return;
			case "like":
				// Ruling 24: the sheet opens on this session's host, folder, model
				// and effort; the menu shows only once the session has loaded.
				if (!conversation) return;
				navigation.navigate("NewSession", {
					hubId: route.params.hubId,
					hubName: activeProfile?.name ?? "Hub",
					like: seedFromSession(route.params.ref, conversation),
				});
				return;
			case "shutDown":
				if (!controls) return;
				confirmShutDown(() => void runSessionAction("shutDown").then(showSessionToast));
				return;
		}
	}
	// The header items outlive this render; they reach the latest choices
	// through the ref, so the header is not reset on every render. The ref is
	// written after commit, in its own effect with no deps, not during
	// render, which React's own rules reserve for effects.
	const chooseSessionActionRef = useRef(chooseSessionAction);
	const runSessionActionRef = useRef(runSessionAction);
	const swipeToSessionRef = useRef(swipeToSession);
	useEffect(() => {
		chooseSessionActionRef.current = chooseSessionAction;
		runSessionActionRef.current = runSessionAction;
		swipeToSessionRef.current = swipeToSession;
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
								backCount={othersWaitingCount}
								onPress={() => openSessionDestination("session")}
								onSwipe={(direction) => swipeToSessionRef.current(direction)}
								neighbors={{ previous: hasPrevious, next: hasNext }}
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
							hasDocuments: documents.length > 0,
							connected,
							sharedNotes: !!conversation?.capabilities.sharedNotes,
							canAside,
							canShutDown,
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
					<Text allowFontScaling={false} style={{ fontSize: 24, color: colors.text }}>
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
		hasPrevious,
		hasNext,
		othersWaitingCount,
		menuLevel,
		hasSubagents,
		documents.length,
		conversation?.capabilities.sharedNotes,
		canAside,
		canShutDown,
	]);
	const currentName = snapshot.conversation?.name;
	useEffect(() => {
		if (currentName && currentName !== route.params.title) navigation.setParams({ title: currentName });
	}, [navigation, currentName, route.params.title]);
	// The conversation's rows are already level-correct: the store projected
	// them at displayConfig (D24-6's seam routing), so the presentation layer
	// only reshapes (member unrolling, attachment adjacency) and computes the
	// footer's accounting — no second, screen-level projection.
	const presentation = useMemo(
		() => projectNativeTranscript(conversation, displayConfig, { justTheConversation: display.justTheConversation }),
		[conversation, displayConfig, display.justTheConversation],
	);
	const timelineRows = useMemo(
		() =>
			// Your answers to a question show beneath the question itself.
			hideAnswerMessages(
				sessionRows(groupTimeline(presentation.items), conversation?.turns ?? [], {
					olderToLoad: olderPage !== null,
				}),
			),
		[presentation.items, conversation?.turns, olderPage],
	);
	const liveRun = liveRunId(timelineRows, conversation?.activeTurnId);
	// A subagent row opens the subagent's own session, under this session as
	// its coordinator, or on a subagent's screen, under the same coordinator.
	const openSubagent = useCallback(
		(ref: string, title: string) => {
			// A row shows only in a loaded transcript, which names its thread.
			const threadId = store.getState().conversation?.threadId;
			const coordinator =
				subagentOf ?? (threadId ? { ref: route.params.ref, threadId, title: route.params.title } : null);
			if (coordinator) navigation.push("Subagent", { hubId: route.params.hubId, ref, title, coordinator });
		},
		[navigation, route.params.hubId, route.params.ref, route.params.title, subagentOf, store],
	);
	const answerFor = useCallback((itemId: string) => answerTo(conversation, itemId), [conversation]);
	// Stable across renders, so a settled agent message keeps its memoized
	// markdown view (TimelineItem's AgentMessage) while the list re-renders.
	const quote = useCallback(
		(text: string) => {
			const quoted = formatQuoteBlock(text);
			if (quoted === "") return;
			const merged = mergeDraftText(document.getSnapshot().record.draft, quoted);
			document.edit(merged);
			focusComposerAt(merged.length);
		},
		[document, focusComposerAt],
	);
	// The documents the agent names become chips under its messages (spec
	// 8.2), aged by the session's own writes. Every publish hands the screen
	// new turns, even while the agent only streams text, so the writes are
	// keyed by their content: a publish that changed no write keeps the same
	// Map, and each message's chips skip re-reading its markdown.
	const documentCwd = conversation?.cwd ?? "";
	const turns = conversation?.turns;
	const writesKey = useMemo(() => JSON.stringify([...fileWrites(turns ?? [], documentCwd)]), [turns, documentCwd]);
	const writes = useMemo(() => new Map<string, string>(JSON.parse(writesKey) as [string, string][]), [writesKey]);
	// The paths the session wrote, with or without a time, so a bare file name
	// it wrote becomes a chip even when the write carried no time.
	const writtenKey = useMemo(() => JSON.stringify([...writtenPaths(turns ?? [], documentCwd)]), [turns, documentCwd]);
	const written = useMemo(() => new Set<string>(JSON.parse(writtenKey) as string[]), [writtenKey]);
	const openDocument = useCallback(
		(path: string, updatedAt: string | undefined) =>
			navigation.navigate("Reader", {
				hubId: route.params.hubId,
				sessionRef: route.params.ref,
				path,
				sessionTitle: route.params.title,
				...(updatedAt === undefined ? {} : { updatedAt }),
			}),
		[navigation, route.params.hubId, route.params.ref, route.params.title],
	);
	// A message still streaming shows its chips once it settles.
	const documentChips = useCallback(
		(message: { id: string; markdown: string; streaming: boolean }) =>
			message.streaming ? null : (
				<MessageDocuments
					hubId={route.params.hubId}
					sessionRef={route.params.ref}
					markdown={message.markdown}
					cwd={documentCwd}
					writes={writes}
					written={written}
					open={openDocument}
				/>
			),
		[route.params.hubId, route.params.ref, documentCwd, writes, written, openDocument],
	);
	// Quote in reply from a screen above this session (the Reader) holds the
	// words until this session is in front again.
	useEffect(() => {
		if (!focused) return;
		const words = takeQuote(route.params.hubId, route.params.ref);
		if (words !== null) quote(words);
	}, [focused, route.params.hubId, route.params.ref, quote]);
	useEffect(() => {
		appliedReaderRestore.current = null;
		readerRestoreAttempts.current.reset();
		if (restoreFrame.current !== null) cancelAnimationFrame(restoreFrame.current);
		restoreFrame.current = null;
		readerHeader.current = false;
		captureSuppressed.current = false;
		readerPageAttempts.current = new Set<string>();
		readerMeasurements.current.clear();
		readerAnchor.current = readerPositions.read(route.params.hubId, route.params.ref);
		follow.dispatch({ type: "reset", following: readerAnchor.current === null });
		turnsSeen.current = readerAnchor.current?.turnsSeen;
		openedFor.current = null;
	}, [route.params.hubId, route.params.ref, follow.dispatch]);
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
			pendingQuestions(conversation).length > 0 || conversation.pendingEscalations.length > 0,
		);
		if (target.kind === "live") {
			readerAnchor.current = null;
			follow.dispatch({ type: "follow" });
			(timeline.current?.getScrollResponder() as ScrollView | null)?.scrollToEnd({ animated: false });
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
			follow.dispatch({ type: "unfollow" });
			appliedReaderRestore.current = null;
			readerRestoreAttempts.current.reset();
		}
	}, [conversation, snapshot.status, timelineRows, focused, bindingInstance, route.params.hubId, route.params.ref]);
	// Loads the page above the loaded history once per cursor: a page that
	// failed, or brought nothing new, stays guarded until the binding or route
	// resets, so a failing page never loops. Both a reading position restored
	// above the loaded rows and a scroll near the top ask for it.
	function loadOlderPage() {
		// Which page this is, for guarding repeat attempts; the store asks for it
		// from its own cursor and trim boundary.
		const pageKey = olderPage;
		const pageAttempts = readerPageAttempts.current;
		if (!service || !connected || !pageKey || snapshot.loadingOlder || pageAttempts.has(pageKey)) return;
		pageAttempts.add(pageKey);
		void store
			.getState()
			.loadOlder(service)
			.then((result) => {
				if (result.status === "ignored" || (result.status === "loaded" && result.itemKeys.length > 0))
					pageAttempts.delete(pageKey);
			})
			.catch(() => {
				// Keep failed page attempts guarded until a binding or route reset.
			});
	}
	const findQuery = find?.query ?? "";
	const findHits = useMemo(
		() => findMatches(timelineRows, findQuery, conversation?.delegates),
		[timelineRows, findQuery, conversation?.delegates],
	);
	const findKey = find?.key ?? null;
	const findIndex = findKey === null ? -1 : timelineRows.findIndex((row) => readerKey(row) === findKey);
	const findCurrent = findIndex < 0 ? null : findIndex;
	function stepFind(direction: 1 | -1) {
		if (!find) return;
		const next = stepMatch(findHits, findCurrent, direction);
		if (next !== null)
			setFind({
				...find,
				key: readerKey(timelineRows[next]),
				seeking: false,
				exhausted: false,
			});
		// Nothing older is loaded: look in older history.
		else if (direction === -1) setFind({ ...find, seeking: true, exhausted: false });
	}
	// While seeking, find the newest match older than the current one (or
	// the newest of all), loading one older page at a time until a match
	// appears or history ends.
	useEffect(() => {
		if (!find?.seeking) return;
		const next = stepMatch(findHits, findCurrent, -1);
		if (next !== null) {
			setFind({ ...find, key: readerKey(timelineRows[next]), seeking: false });
			return;
		}
		if (snapshot.loadingOlder) return;
		const pageKey = olderPage;
		if (!pageKey) {
			// Stepped past the oldest match: say so. With none at all, the
			// label already reads "No matches".
			setFind({ ...find, seeking: false, exhausted: findHits.length > 0 });
			return;
		}
		// Offline, or this page already failed: stop, and the next step asks again.
		if (!service || !connected || readerPageAttempts.current.has(pageKey)) {
			setFind({ ...find, seeking: false });
			return;
		}
		// Paging older history reads away from the live end, so new rows stop
		// pulling the list down while find looks.
		if (follow.state.current.following) {
			findLeftTheEnd.current = true;
			follow.dispatch({ type: "unfollow" });
		}
		loadOlderPage();
	});
	// A search that ends with no match, or find closing, gives the end back to
	// a list find unfollowed, when the list is still there. A jump to a match
	// is reading, and keeps it.
	const findLeftTheEnd = useRef(false);
	useEffect(() => {
		if (!findLeftTheEnd.current || (find !== null && (find.seeking || find.key !== null))) return;
		findLeftTheEnd.current = false;
		const stillAtEnd = atEnd({
			contentOffset: { y: listOffset.current },
			contentSize: { height: readerContentHeight.current },
			layoutMeasurement: { height: readerViewportHeight.current },
			contentInset: listUnderBar.contentInset,
		});
		if (stillAtEnd) follow.dispatch({ type: "follow" });
	});
	// The current match comes into view, 30% down the list. A row the list
	// hasn't measured fails the jump (onScrollToIndexFailed, while
	// findJumping holds); the list then moves near it and tries again, for as
	// long as each try measures rows closer to it, as the reader's restore
	// does (ReaderRestoreAttempts.retryUnmeasured). A retry belongs to the
	// match it started for: a new match or closing find cancels it, and it
	// finds its row again when it runs, since older pages may have prepended.
	const findJumping = useRef(false);
	const findAttempts = useRef(new ReaderRestoreAttempts());
	const findRetryFrame = useRef<number | null>(null);
	const findCurrentNow = useRef(findCurrent);
	useEffect(() => {
		findCurrentNow.current = findCurrent;
	});
	function cancelFindRetry() {
		if (findRetryFrame.current !== null) cancelAnimationFrame(findRetryFrame.current);
		findRetryFrame.current = null;
	}
	function retryFindMatch() {
		findRetryFrame.current = null;
		if (findCurrentNow.current !== null) scrollToFindMatch(findCurrentNow.current);
	}
	function scrollToFindMatch(index: number) {
		findLeftTheEnd.current = false;
		follow.dispatch({ type: "unfollow" });
		readerHeader.current = false;
		// The reading position follows the jump, so nothing pulls the list back.
		captureSuppressed.current = false;
		findJumping.current = true;
		timeline.current?.scrollToIndex({ index, viewPosition: 0.3, animated: true });
		findJumping.current = false;
	}
	// biome-ignore lint/correctness/useExhaustiveDependencies: only a new current match scrolls; rows prepending above it keep it in view.
	useEffect(() => {
		cancelFindRetry();
		findAttempts.current.reset();
		if (findCurrent !== null) scrollToFindMatch(findCurrent);
		return cancelFindRetry;
	}, [findKey]);
	// Leaving the screen closes find.
	useEffect(() => {
		if (!focused) setFind(null);
	}, [focused]);
	// biome-ignore lint/correctness/useExhaustiveDependencies: Cell layout revisions intentionally retrigger semantic restoration.
	useEffect(() => {
		const anchor = readerAnchor.current;
		if (
			!anchor ||
			timelineRows.length === 0 ||
			!focused ||
			readerHeader.current ||
			follow.state.current.following ||
			follow.state.current.touch !== "none" ||
			// Where the list can reach depends on the bar: restore once it has
			// laid out, so a first restore is never clamped short of it.
			bottomBar.height === null
		)
			return;
		if (anchor.conversationInstance && snapshot.status !== "open") return;
		if (anchor.conversationInstance && bindingInstance && anchor.conversationInstance !== bindingInstance) {
			readerAnchor.current = null;
			appliedReaderRestore.current = null;
			follow.dispatch({ type: "follow" });
			(timeline.current?.getScrollResponder() as ScrollView | null)?.scrollToEnd({ animated: false });
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
			follow.state.current.touch === "none",
		);
		const targetIndex = resolveReaderAnchor(anchor, timelineRows);
		const measurementProgress =
			targetIndex === null
				? -1
				: furthestMeasuredRowBeforeTarget(timelineRows, targetIndex, [...readerMeasurements.current.values()]);
		if (!command || !readerRestoreAttempts.current.begin(command, measurementProgress)) return;
		if (command.kind === "approximate") {
			captureSuppressed.current = true;
			timeline.current?.scrollToIndex({
				index: resolveReaderAnchor(anchor, timelineRows) ?? 0,
				viewOffset: -anchor.withinItemOffset,
				animated: false,
			});
		} else {
			if (restoreFrame.current !== null) cancelAnimationFrame(restoreFrame.current);
			restoreFrame.current = null;
			const currentKey = readerKey(timelineRows[command.index]);
			const measurement = readerMeasurements.current.get(currentKey);
			if (!measurement) return;
			const desired = measurement.y - command.viewOffset;
			// On iOS the bar's inset extends how far the list can scroll.
			const scrollOffset = reachableReaderOffset(
				desired,
				readerContentHeight.current,
				listContentMinHeight(readerViewportHeight.current, listUnderBar),
			);
			if (!exactRestoreDue(appliedReaderRestore.current, measurement, scrollOffset)) return;
			appliedReaderRestore.current = {
				key: currentKey,
				height: measurement.height,
				offset: scrollOffset,
				clamped: scrollOffset !== desired,
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
		olderPage,
		snapshot.loadingOlder,
		conversation?.items,
		timelineRows,
		service,
		store,
		connected,
		focused,
		bottomBar.height,
	]);
	useEffect(
		() => () => {
			if (readerAnchor.current) readerPositions.save(readerAnchor.current);
			if (restoreFrame.current !== null) cancelAnimationFrame(restoreFrame.current);
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
			if (restoreFrame.current !== null) cancelAnimationFrame(restoreFrame.current);
			restoreFrame.current = null;
		};
	}, [focused]);
	useEffect(() => {
		const subscription = AppState.addEventListener("change", (state) => {
			if (state !== "active") readerPositions.save(readerAnchor.current);
		});
		return () => subscription.remove();
	}, []);
	const controlsState = useControlsState(controls);
	const settingsPending = controlsState?.pending != null || commandPending;
	const pending = snapshot.pendingMutation?.status === "pending";
	const ready = connected && focused && snapshot.status === "open" && !pending && !settingsPending;
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
	const dockBatch = questionBatch ? questionBatch.id + questionsIdentity(questionBatch.questions) : null;
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
		(!conversation || (!conversation.resumeRequired && conversation.status.type !== "restartRequired")) &&
		(!conversation || canComposeFor(conversation));
	const goalCommand = goalObjective(draft.record.draft, draft.record.images?.length);
	const command = composerCommand(draft.record.draft, draft.record.images?.length);
	async function applyCommand(clear = false) {
		const action = clear ? "goal" : command?.command.id;
		if (
			!service ||
			!ready ||
			!action ||
			commandBusy.current ||
			(clear
				? !conversation?.capabilities.goal
				: !command || !conversation || !composerCommandAvailable(command.command, conversation)) ||
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
							void store.getState().openProjected(service, activitySink, route.params.ref, replacement);
						},
						openAside,
						local: async (id) => {
							if (!currentBinding())
								throw new CommandArgumentError("The session changed. Try again from the current session.");
							if (id === "project") {
								if (!client) throw new CommandArgumentError("Connect to the hub to locate this session.");
								const lookup = new AbortController();
								projectLookup.current = lookup;
								let location: SessionLocation;
								try {
									location = await locateSession(client, route.params.ref, lookup.signal);
								} catch (error) {
									throw new CommandArgumentError(
										error instanceof Error ? error.message : "Could not locate this session.",
									);
								}
								if (!currentBinding() || lookup.signal.aborted)
									throw new CommandArgumentError("The session changed before its location was loaded.");
								Keyboard.dismiss();
								navigation.push("SessionLocation", {
									hubId: route.params.hubId,
									location,
								});
								return;
							}
							if (id === "copy-id") {
								try {
									const copied = await Clipboard.setStringAsync(route.params.ref);
									if (!copied) throw new Error("Clipboard unavailable");
									AccessibilityInfo.announceForAccessibility("Session reference copied");
								} catch {
									throw new CommandArgumentError("Could not copy the session reference. Try again.");
								}
								return;
							}
							Keyboard.dismiss();
							if (id === "status")
								navigation.navigate("SessionInfoSheet", {
									hubId: route.params.hubId,
									ref: route.params.ref,
								});
							else {
								const current = store.getState().conversation;
								if (!current || !client) throw new CommandArgumentError("Session tasks are unavailable.");
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
			// A shut-down session stays open on its history, like the ⋯ menu's
			// own Shut down (ruling 19): the store's error surfaces a failed read.
			await store.getState().rehydrate(service, activitySink);
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
			document.replaceDraft(`/goal ${store.getState().conversation?.goal?.objective ?? ""}`);
			// Android's dialog must release window focus before opening the keyboard.
			focusAfterModal.current = Platform.OS === "android";
			if (Platform.OS === "ios") requestAnimationFrame(() => composerInput.current?.focus());
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
	async function sendAnswers(batch: AskBatch, selections: QuestionSelections): Promise<boolean> {
		const current = store.getState();
		questionBatches.reconcile(pendingQuestions(current.conversation));
		const text = composeQuestionAnswers(batch.questions, selections);
		// Offline, the answers wait in the phone's outbox as a message would
		// (spec 8.5), fenced to the instance this phone last read.
		const offline = !connectionReady.current;
		const online = service;
		const target = offline ? offlineTarget(current) : null;
		const offlineAction = offline ? offlineSendAction(current) : "none";
		if (
			(offline
				? target === null || offlineAction === "none"
				: !service ||
					!ready ||
					current.status !== "open" ||
					current.conversationGeneration !== bindingGeneration ||
					current.conversation?.instanceId !== bindingInstance) ||
			!screenInFront(navigation, route.key) ||
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
				if (target !== null && offlineAction !== "none") {
					await getNativeMutationRuntime().submit(
						offlineRequest(target, offlineAction, [{ type: "text", text: input }]),
					);
				} else if (online) {
					const previous = store.getState().lastAcceptedMutation;
					await store.getState().send(online, [{ type: "text", text: input }]);
					const accepted = store.getState().lastAcceptedMutation;
					if (!accepted || accepted === previous || accepted.kind !== "send") return false;
				} else return false;
				questionBatches.finish(batch.id, true);
				acceptedAnswers = true;
				return true;
			});
			// Kept offline isn't sent yet: its ghost says it waits, and the
			// haptic is Send's.
			if (acceptedAnswers && offline) haptic("light");
			else if (acceptedAnswers) {
				haptic("success");
				toaster.show({
					text: batch.questions.length > 1 ? "Answers sent" : "Answer sent",
				});
			}
			if (
				acceptedAnswers &&
				online &&
				!offline &&
				connectionReady.current &&
				screenInFront(navigation, route.key) &&
				store.getState().conversationGeneration === bindingGeneration &&
				store.getState().conversation?.instanceId === bindingInstance
			)
				await store.getState().rehydrate(online, activitySink);
		} catch {
			setAnswerError("Could not confirm delivery. Your answers are retained; check delivery before trying again.");
		} finally {
			questionBatches.finish(batch.id, false);
		}
		return acceptedAnswers;
	}
	const frames = useFrameCounter(store);
	const toaster = useToast();
	const showToast = toaster.show;
	const showSubagentToast = useCallback((text: string) => showToast({ text }), [showToast]);
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
						const live = connectionReady.current ? currentDestination.current.client : null;
						return live ? live.request(method, params) : Promise.reject(new Error("Not connected"));
					},
				},
				hubId: route.params.hubId,
				ref: route.params.ref,
				instanceId: () => store.getState().conversation?.instanceId,
				savedNote: () => store.getState().conversation?.humanNote ?? "",
				writable: () => {
					const live = store.getState().conversation;
					return live ? canWriteHumanNote(live) : false;
				},
				working: () => store.getState().conversation?.status.type === "active",
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
					text: outcome.woke ? "Note saved. The agent is reading it." : "Note saved",
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
						cwd: conversation.cwd,
						title: route.params.title,
					}
				: undefined,
		[
			conversation?.cwd,
			route.params.title,
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
	useProvideSheetHost(notesHosts, sheetKey(route.params.hubId, route.params.ref), notesHost);
	// The Session sheet's host (ruling 37). Its callbacks reach the latest
	// render through refs, so the host changes only when what it shows does.
	const goalActionsRef = useRef({ editGoal, clearGoal: () => void applyCommand(true) });
	useEffect(() => {
		goalActionsRef.current = { editGoal, clearGoal: () => void applyCommand(true) };
	});
	// The hub's own machine goes by the hub's name, as Hub > Hosts names it;
	// any other host by the manifest's label, or its id until the manifest
	// has loaded (sessionHosts).
	const hubName = activeProfile?.id === route.params.hubId ? activeProfile.name : null;
	const host = useMemo(() => sessionHosts(fleet.sources, hubName, connected), [fleet.sources, hubName, connected]);
	// The Board row names the model too (S17), for while the catalog is away.
	const modelLabel = conversation
		? modelChipLabel(conversation, controlsState?.catalog?.data, fleetRow?.model_name)
		: "";
	const sessionInfoHost = useMemo<SessionInfoHost | undefined>(
		() =>
			// Provided while the screen lives, with or without controls, so a
			// connection blip never closes an open sheet or loses its input.
			conversation
				? {
						session: conversation,
						controls,
						host,
						modelLabel,
						runMs,
						ready,
						editGoal: () => goalActionsRef.current.editGoal(),
						clearGoal: () => goalActionsRef.current.clearGoal(),
						act: (action) => runSessionActionRef.current(action),
						toast: toaster.show,
					}
				: undefined,
		[conversation, controls, host, modelLabel, runMs, ready, toaster.show],
	);
	useProvideSheetHost(sessionInfoHosts, sheetKey(route.params.hubId, route.params.ref), sessionInfoHost);
	// The model sheet's host (ruling 37).
	const modelHost = useMemo<ModelHost | undefined>(
		() => (conversation ? { session: conversation, controls, ready, toast: toaster.show } : undefined),
		[conversation, controls, ready, toaster.show],
	);
	useProvideSheetHost(modelHosts, sheetKey(route.params.hubId, route.params.ref), modelHost);
	// The Commands and skills sheet's host (ruling 37). A choice lands at the
	// start of the draft, with the caret after it for the command's argument.
	const commandsHost = useMemo<CommandsHost | undefined>(
		() =>
			conversation
				? {
						session: conversation,
						choose: (invocation) => {
							const inserted = insertInvocation(document.getSnapshot().record.draft, invocation);
							document.edit(inserted.text);
							focusComposerAt(inserted.caret);
						},
					}
				: undefined,
		[conversation, document, focusComposerAt],
	);
	useProvideSheetHost(commandHosts, sheetKey(route.params.hubId, route.params.ref), commandsHost);
	// The chip names the model the way the catalog does, so the screen loads
	// the catalog once for each binding it opens connected. Controls made
	// while the session was still reopening can't read yet, so the read waits
	// for the session to be open.
	const sessionOpen = snapshot.status === "open";
	useEffect(() => {
		if (controls && hasConversation && sessionOpen) void controls.loadModels();
	}, [controls, hasConversation, sessionOpen]);
	const catalog = controlsState?.catalog;
	useEffect(() => {
		if (catalog) knownCatalog.current = catalog;
	}, [catalog]);
	// What takes the composer's place when the session can't take a message
	// yet (ruling 20).
	const notice =
		conversation?.status.type === "restartRequired" ? "restartNeeded" : conversation?.resumeRequired ? "paused" : null;
	const restart = useSessionRestart(controls, snapshot.status);
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
			if (accepted && accepted !== previous && accepted.kind === "interrupt") toaster.show({ text: "Stopped" });
		} catch {
			// Stop only acts while a turn runs; a turn that ended first has
			// nothing left to stop, so a refusal says nothing.
		} finally {
			stopBusy.current = false;
			setStopping(false);
		}
	}
	/** Loads the page above when the list at `y` is near its top and you have
	 * moved it yourself (session/liveEndFollow pagesOlder). */
	function pageOlderNear(y: number | undefined) {
		if (y !== undefined && pagesOlder(follow.state.current, y)) loadOlderPage();
	}
	function jumpToLive() {
		readerHeader.current = false;
		readerAnchor.current = null;
		follow.dispatch({ type: "follow" });
		captureSuppressed.current = false;
		(timeline.current?.getScrollResponder() as ScrollView | null)?.scrollToEnd({ animated: true });
	}
	// The render-time action drives the placeholder, the label and whether
	// Send is enabled; a press routes on the live state instead.
	const action = conversation ? sendAction(conversation, snapshot.pendingMutations, connected) : "none";
	// What Send does once the connection is there, which the placeholder
	// describes offline too: it describes the session, not the outbox.
	const onlineAction = connected
		? action
		: conversation
			? sendAction(conversation, snapshot.pendingMutations, true)
			: "none";
	// Offline, Send keeps the message in the phone's outbox, for a session
	// this phone has read since launch (ruling 12).
	const offlineAdmits =
		!connected && focused && offlineTarget(snapshot) !== null && offlineSendAction(snapshot) !== "none";
	const composerReady =
		(ready || offlineAdmits) &&
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
			? // A command asks the hub, so it waits for the connection.
				ready &&
				composerReady &&
				!(command.command.id === "goal" && !goalCommand && !conversation?.goal) &&
				!!conversation &&
				composerCommandAvailable(command.command, conversation)
			: composerReady && (!!draft.record.draft.trim() || !!draft.record.images?.length) && action !== "none";
	const composerSendLabel =
		answering || command === null
			? sendLabel(action, answering, connected)
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
		if (!connectionReady.current) {
			await sendOffline();
			return;
		}
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
	// The session a message sent offline is fenced to: the instance this
	// phone last read (ruling 12), exactly as an online send's durable request
	// carries it. A session not read since launch has none, so Send waits.
	function offlineTarget(state: ConversationState): OfflineTarget | null {
		const read = state.conversation;
		if (!read || state.ref !== route.params.ref) return null;
		return {
			hubId: route.params.hubId,
			ref: route.params.ref,
			threadId: read.threadId,
			instanceId: read.instanceId ?? read.threadId,
		};
	}
	function offlineSendAction(state: ConversationState) {
		return state.conversation ? sendAction(state.conversation, state.pendingMutations, false) : "none";
	}
	// Send while offline (spec 8.5): the message goes straight into the
	// phone's outbox, which sends it once the connection returns and this
	// session reads again.
	async function sendOffline() {
		const live = store.getState();
		const target = offlineTarget(live);
		const kind = offlineSendAction(live);
		if (target === null || kind === "none") return;
		setActionError(null);
		try {
			await document.submit(async (text, images) => {
				await getNativeMutationRuntime().submit(offlineRequest(target, kind, buildComposerInput(text, images)));
				haptic("light");
				return true;
			});
		} catch {
			// As Send's: the draft stays, and its ghost says what happened.
		}
	}
	// Your text as the free answer to the question the dock is on. When that
	// completes the ask, every answer goes out as one message; otherwise the
	// dock returns at the next unanswered question and the composer steps
	// aside again (ruling 14).
	async function answerWithComposer(batch: AskBatch) {
		if (!questionDraft.loaded) return;
		const text = document.getSnapshot().record.draft;
		const result = answerWithText(batch.questions, questionDraft.selections, questionDraft.activeIndex, text);
		if (result.message === null) {
			questionDraft.setSelections(() => result.selections);
			if (result.nextIndex !== undefined) questionDraft.setActiveIndex(result.nextIndex);
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
	const liveSendKind = useCallback((): "send" | "queue" | null => {
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
		const liveAction = sendAction(live.conversation, live.pendingMutations, connectionReady.current);
		if (liveAction === "none") return null;
		return liveAction === "queue" ? "queue" : "send";
	}, [service, ready, controls, unconfirmedSend, store]);
	// Sends or queues one message the way Send does, and says whether the hub
	// took it.
	const deliver = useCallback(
		async (
			through: NonNullable<typeof service>,
			kind: "send" | "queue",
			text: string,
			images: Parameters<typeof buildComposerInput>[1],
		) => {
			const previous = store.getState().lastAcceptedMutation;
			await store.getState()[kind](through, buildComposerInput(text, images));
			const accepted = store.getState().lastAcceptedMutation;
			const admitted = accepted != null && accepted !== previous && accepted.kind === kind;
			// Spec 16.6: a light impact on send, Send's and an error row's Retry's.
			if (admitted) haptic("light");
			return admitted;
		},
		[store],
	);
	// An error row's Retry sends Jesse's sentence as your message through
	// Send's own path (ruling 26), leaving whatever you were typing alone.
	const retryFailedTurn = useCallback(async () => {
		const kind = liveSendKind();
		if (!service || kind === null) return;
		setActionError(null);
		try {
			await document.submitText(RETRY_MESSAGE, (text, images) => deliver(service, kind, text, images));
		} catch {
			// As with Send, a refusal leaves the unconfirmed ghost to say so.
		}
	}, [liveSendKind, service, document, deliver]);
	const runErrorAction = useCallback(
		(errorAction: ErrorAction) => {
			if (errorAction === "resume") void controls?.resume();
			else if (errorAction === "signIn")
				// The error doesn't name the provider, so the Hub opens at Providers.
				navigation.navigate("Hub", { screen: "Providers", params: { hubId: route.params.hubId }, initial: false });
			else void retryFailedTurn();
		},
		[controls, navigation, route.params.hubId, retryFailedTurn],
	);
	function openCommands() {
		Keyboard.dismiss();
		navigation.navigate("CommandsSheet", {
			hubId: route.params.hubId,
			ref: route.params.ref,
		});
	}
	function openModelSheet(setting: "model" | "vision") {
		Keyboard.dismiss();
		navigation.navigate("ModelSheet", {
			hubId: route.params.hubId,
			ref: route.params.ref,
			setting,
		});
	}
	// The chip opens the model sheet when there is something there to change:
	// the model, or its effort.
	const composerSettings =
		conversation && canCompose ? (
			<ModelChip
				label={modelLabel}
				onPress={controls && canOpenModelSheet(conversation) ? () => openModelSheet("model") : undefined}
			/>
		) : null;
	// Everything waiting to reach the agent, as ghosts above the composer
	// (spec 8.5 and 14). A refused row keeps Edit whenever its record can come
	// back; whether the composer can take it right now is the bubble's
	// canEdit, so an occupied composer shows Edit disabled with the reason.
	const recoveryRows = projectNativeMutationRecovery(recovery.targetKey, recovery.snapshot);
	const allGhosts = whatCanActNow(
		ghosts(
			conversation,
			snapshot.pendingMutations,
			unconfirmedSend === null
				? null
				: {
						text: unconfirmedSend,
						sentText: translateAttachmentMarkers(unconfirmedSend, draft.record.unconfirmedImages),
					},
			recoveryRows,
			connected,
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
	async function oneGhostAction(run: () => Promise<ToastMessage | null>): Promise<ToastMessage | null> {
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
	async function ghostAction(ghost: Ghost, action: GhostAction): Promise<ToastMessage | null> {
		const origin = ghost.origin;
		if (origin.kind === "queue") return queuedGhostAction(origin.entry, action);
		if (action === "check") {
			await checkDelivery();
			return null;
		}
		if (origin.kind === "pending") {
			// The phone's own message: Discard or Cancel drops it from the
			// outbox, Send now releases one a Stop held. A press that finds the
			// row already changed does nothing; the ghosts re-render from storage.
			const runtime = getNativeMutationRuntime();
			const targetKey = nativeMutationTargetKey(route.params.hubId, route.params.ref);
			if (action === "discard" || action === "cancel")
				await runtime.discardUndelivered(origin.clientMutationId, targetKey);
			else if (action === "sendNow") await runtime.releaseCanceled(origin.clientMutationId, targetKey);
			return null;
		}
		if (origin.kind === "draft") {
			if (action === "discard") {
				document.dismiss();
				// The ghost stands in for a lost send the outbox still holds (the
				// draft's `sameSend` match): Discard clears that row too, or it
				// returns as its own "Couldn't confirm this was sent" ghost.
				if (origin.clientMutationId !== undefined)
					await getNativeMutationRuntime().discardUndelivered(
						origin.clientMutationId,
						nativeMutationTargetKey(route.params.hubId, route.params.ref),
					);
			} else if (action === "edit") document.restore();
			return null;
		}
		if (origin.kind === "recovery") {
			const row = recoveryRows.find((candidate) => candidate.clientMutationId === origin.row.clientMutationId);
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
	async function queueChange(live: NonNullable<typeof service>, change: () => Promise<unknown>): Promise<boolean> {
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
	async function queuedGhostAction(entry: QueueEntryRef, action: GhostAction): Promise<ToastMessage | null> {
		const live = store.getState().conversation;
		const instanceId = live?.instanceId;
		if (!service || !connectionReady.current || !live || !instanceId) return null;
		// The message you saw, wherever it sits now; nothing once it has left
		// the queue (Review Focus 2).
		const target = ghostActionTarget(live.queue, entry);
		if (!target) return null;
		const cancel = () => queueChange(service, () => service.cancelQueued(target.index, target.id, instanceId));
		if (action === "steerNow" || action === "sendNow") {
			if (queueActionRefusal(live, "promote") !== null) return STEER_FAILED;
			const promoted = await queueChange(service, () =>
				service.promoteQueuedAsSteer(target.index, target.id, instanceId),
			);
			return promoted ? null : STEER_FAILED;
		}
		if (action === "cancel") return (await cancel()) ? null : { text: "Couldn't take this message out of the queue." };
		if (action === "edit") {
			const text = live.queue?.texts?.[target.index];
			if (!text?.trim() || !document.getSnapshot().loaded) return null;
			// The text goes into the composer before the message leaves the
			// queue, so a failed cancel never loses it (web QueueStrip).
			const merged = mergeDraftText(document.getSnapshot().record.draft, text);
			document.edit(merged);
			const cancelled = await cancel();
			focusComposerAt(merged.length);
			return cancelled ? null : { text: "Moved to your message, but it's still queued." };
		}
		return null;
	}
	// Steer all now: the whole live queue, whatever it holds at the press.
	async function steerWithQueue(): Promise<ToastMessage | null> {
		const live = store.getState().conversation;
		const instanceId = live?.instanceId;
		if (!service || !connectionReady.current || !live?.queue || !instanceId) return null;
		if (queueActionRefusal(live, "drainAll") !== null) return STEER_ALL_FAILED;
		const revision = live.queue.revision;
		const drained = await queueChange(service, () => service.drainAsSteer(revision, instanceId));
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
	const queuedGhosts = allGhosts.filter((ghost) => ghost.origin.kind === "queue");
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
			...(canSteerAll ? { steerAll: () => ghostActions.current.steerAll() } : {}),
		}),
		[queuedKey, ghostBusy, canSteerAll, toaster.show],
	);
	useProvideSheetHost(queueHosts, sheetKey(route.params.hubId, route.params.ref), queueHost);
	// The docks, the tray and the composer, by one rule (bottomStack.ts).
	const approval = conversation?.pendingEscalations[0] ?? null;
	const bottom = bottomStack({
		approvalPending: approval !== null,
		questionPending: questionBatch !== null,
		folded: questionFolded,
		composerBack,
	});
	// A subagent the hub takes no message for (a running one, ruling 30) holds
	// its bar where the composer would be.
	const subagentBar =
		subagentOf !== undefined &&
		conversation !== null &&
		conversation !== undefined &&
		!conversation.capabilities.send &&
		!conversation.capabilities.queue;
	const composerShown = canCompose && bottom.composer && !subagentBar;
	// "↓ 3 new": rows that arrived below while you read above the end.
	const newCount = follow.away ? newRowCount(timelineRows, follow.away) : 0;
	// Next shows while someone else needs you, unless this session asks you
	// something, or you are finding in it (spec 8.3); FloatingStack steps it
	// aside while you type.
	const nextTarget = approval === null && questionBatch === null && find === null ? (queue[0] ?? null) : null;
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
			{recovery.failed ? <RecoveryFailure error={recovery.error} onRetry={recovery.retry} /> : null}
			<QueuedMessages
				ghosts={allGhosts}
				disabled={ghostBusy}
				canEdit={canEditGhost}
				editHint={ghostEditHint}
				// Only one of the two places waitingForAgent shows is mounted.
				backdrop={composerShown ? "surface" : "page"}
				draftAttachments={<ImageAttachments document={document} selection={imageSelection} uncertain />}
				composerFocus={composerFocus}
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

	// One render function for the list's lifetime: FlatList sees the same
	// reference across a re-render that changes nothing a row reads (the
	// reader keying, a sheet opening), so it does not rebuild every visible
	// transcript cell for them.
	const renderItem = useCallback(
		({ item, index }: { item: TimelineRow; index: number }) => (
			<View
				style={{
					paddingBottom: timelineGap(item, timelineRows[index + 1]),
				}}
			>
				<View
					style={
						findKey !== null && readerKey(item) === findKey
							? {
									// The current match (ruling 29): blue means selected.
									backgroundColor: colors.palette.accentBg,
									borderRadius: 12,
									marginHorizontal: -8,
									paddingHorizontal: 8,
								}
							: undefined
					}
				>
					<TimelineItem
						item={item}
						hubId={route.params.hubId}
						sessionRef={route.params.ref}
						activityPresentation={presentation.activityPresentation.get(item.id)}
						expandByDefault={presentation.expandByDefault}
						showDuration={presentation.showDuration}
						fork={snapshot.conversation?.capabilities?.forkFromTurn ? forkMessage : undefined}
						forkDisabled={!connected || !focused || snapshot.status !== "open"}
						quote={quote}
						live={item.id === liveRun}
						liveRunsOpen={presentation.liveRunsOpen}
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
						documentChips={documentChips}
					/>
				</View>
			</View>
		),
		[
			findKey,
			colors.palette.accentBg,
			timelineRows,
			route.params.hubId,
			route.params.ref,
			presentation,
			snapshot,
			forkMessage,
			connected,
			focused,
			quote,
			liveRun,
			conversation,
			openSubagent,
			answerFor,
			liveSendKind,
			runErrorAction,
			documentChips,
		],
	);

	return (
		<SafeAreaView edges={["left", "right"]} style={[styles.fill, { backgroundColor: colors.background }]}>
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
			<KeyboardAvoidingView
				style={styles.fill}
				behavior={Platform.OS === "ios" ? "padding" : "height"}
				keyboardVerticalOffset={headerHeight - underNavBar}
			>
				<View testID="session-bottom-bar-room" style={styles.fill} onLayout={bottomBarRoom.onLayout}>
					<View style={{ flex: 1 }}>
						<FlatList
							ref={timeline}
							// Where the transcript rests depends on its viewport and the
							// bar, so it shows once both have laid out and never draws a
							// frame at a place it then leaves.
							style={{ opacity: listLaidOut ? 1 : 0 }}
							onLayout={(event) => {
								readerViewportHeight.current = event.nativeEvent.layout.height;
								setLayoutRevision((revision) => revision + 1);
								// The viewport changed (the keyboard, a dock): while following,
								// the end stays in view.
								if (follow.state.current.following)
									(timeline.current?.getScrollResponder() as ScrollView | null)?.scrollToEnd({ animated: false });
							}}
							data={timelineRows}
							// Cells re-render only for a new renderItem or new rows, and
							// renderItem changes with everything a row reads (the live run
							// included): a screen render that changes nothing a row reads
							// (the bottom bar re-laying out as the keyboard folds the queue)
							// leaves them alone, where FlatList otherwise rebuilds its
							// renderer, and so every visible cell, on every render (#3247).
							strictMode
							ListFooterComponent={presentation.usage ? <TranscriptUsage {...presentation.usage} /> : null}
							CellRendererComponent={readerCellRenderer}
							// A row keeps its reader key when history records it, so the
							// list keeps its cell (a streamed reply's wire id changes).
							keyExtractor={readerKey}
							renderItem={renderItem}
							// The end keeps a fixed room for what floats over it (spec 8.3),
							// so Next never sits on the last line and nothing coming or
							// going there moves the list.
							contentContainerStyle={{
								// A short transcript rests just above the composer (spec 8.5):
								// it fills the viewport above the bar's inset, so at rest it is
								// at its end with nothing under the bar. The viewport is a ref;
								// its onLayout bumps layoutRevision, which renders this again.
								minHeight: listContentMinHeight(readerViewportHeight.current, listUnderBar),
								justifyContent: "flex-end",
								padding: 16,
								paddingTop: 16 + reservedTop,
								paddingBottom: listUnderBar.endPadding + transcriptEnd,
							}}
							contentInset={listUnderBar.contentInset}
							// Dragging the transcript lowers the keyboard: following the finger
							// as in Messages on iOS, and at the drag's start on Android, which
							// has no interactive dismissal.
							keyboardDismissMode={Platform.OS === "ios" ? "interactive" : "on-drag"}
							scrollIndicatorInsets={listUnderBar.scrollIndicatorInsets}
							// Older history loading above never moves what you read.
							maintainVisibleContentPosition={{ minIndexForVisible: 0 }}
							onContentSizeChange={(_width, height) => {
								readerContentHeight.current = height;
								setLayoutRevision((revision) => revision + 1);
								if (follow.state.current.following)
									(timeline.current?.getScrollResponder() as ScrollView | null)?.scrollToEnd({ animated: false });
							}}
							scrollEventThrottle={100}
							onScroll={(event) => {
								const y = event.nativeEvent.contentOffset.y;
								listOffset.current = y;
								headerHiding.onScroll(y, follow.state.current.touch !== "none");
								if (!focused) return;
								const end = atEnd(event.nativeEvent);
								if (end) turnsSeen.current = latestSettledTurn(conversation) ?? turnsSeen.current;
								follow.dispatch({ type: "scroll", atEnd: end, keys: () => new Set(timelineRows.map(readerKey)) });
								settleAtEnd(end && follow.state.current.touch === "none");
								if (captureSuppressed.current) return;
								// Older history loads as you near the top (spec 8.2) once you
								// move the list. With no finger on it, and the app neither
								// following the end nor restoring (which returns above), the
								// list moved for an assistive scroll. The drag and coast events
								// check too, since a short flick or an overscroll may report no
								// scroll between them.
								if (follow.state.current.touch === "none" && !follow.state.current.following)
									follow.dispatch({ type: "assistiveScroll" });
								if (!follow.state.current.following) pageOlderNear(y);
								const visible = readerAnchorRow(timelineRows, readerMeasurements.current, y);
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
									const measurement = readerMeasurements.current.get(readerKey(visible));
									// Where you scrolled is where the anchor is, so there is nothing
									// to restore until the anchor changes or its row reflows. The
									// trade-off: every scroll re-captures the anchor and so arms a
									// restore only for that exact row; one that reflows (a text-size
									// change) restores, one whose y merely moves never does.
									if (anchor && measurement)
										appliedReaderRestore.current = {
											key: measurement.key,
											height: measurement.height,
											offset: y,
											clamped: false,
										};
								}
							}}
							onScrollBeginDrag={(event) => {
								follow.dispatch({ type: "dragBegin" });
								pageOlderNear(event?.nativeEvent.contentOffset.y);
								readerHeader.current = false;
								captureSuppressed.current = false;
								if (restoreFrame.current !== null) cancelAnimationFrame(restoreFrame.current);
								restoreFrame.current = null;
							}}
							// Letting go at the end, or a flick settling there, follows it
							// again (spec 8.2); a row landing mid-drag never moves the list
							// under the finger.
							onScrollEndDrag={(event) => {
								follow.dispatch({ type: "dragEnd", atEnd: atEnd(event.nativeEvent) });
								settleAtEnd(atEnd(event.nativeEvent));
								pageOlderNear(event.nativeEvent.contentOffset.y);
								readerPositions.save(readerAnchor.current);
								setLayoutRevision((revision) => revision + 1);
							}}
							onMomentumScrollBegin={(event) => {
								follow.dispatch({ type: "momentumBegin" });
								pageOlderNear(event?.nativeEvent.contentOffset.y);
							}}
							onMomentumScrollEnd={(event) => {
								follow.dispatch({ type: "momentumEnd", atEnd: atEnd(event.nativeEvent) });
								settleAtEnd(atEnd(event.nativeEvent));
								pageOlderNear(event.nativeEvent.contentOffset.y);
								readerPositions.save(readerAnchor.current);
								setLayoutRevision((revision) => revision + 1);
							}}
							onScrollToIndexFailed={({ index, averageItemLength }) => {
								// A match beyond the measured rows: move near it, so the
								// rows on the way render, then try again.
								if (findJumping.current) {
									const progress = furthestMeasuredRowBeforeTarget(timelineRows, index, [
										...readerMeasurements.current.values(),
									]);
									if (!findAttempts.current.retryUnmeasured(progress)) return;
									timeline.current?.scrollToOffset({
										offset: index * Math.max(1, averageItemLength),
										animated: false,
									});
									findRetryFrame.current = requestAnimationFrame(retryFindMatch);
									return;
								}
								const anchor = readerAnchor.current;
								const targetIndex = anchor ? resolveReaderAnchor(anchor, timelineRows) : null;
								if (targetIndex !== null && readerMeasurements.current.has(readerKey(timelineRows[targetIndex])))
									return;
								const measurementProgress =
									targetIndex === null
										? -1
										: furthestMeasuredRowBeforeTarget(timelineRows, targetIndex, [
												...readerMeasurements.current.values(),
											]);
								if (!readerRestoreAttempts.current.retryUnmeasured(measurementProgress)) return;
								appliedReaderRestore.current = null;
								restoreFrame.current = requestAnimationFrame(() => {
									restoreFrame.current = null;
									const anchor = readerAnchor.current;
									if (!anchor || follow.state.current.touch !== "none") return;
									captureSuppressed.current = true;
									timeline.current?.scrollToOffset({
										offset: Math.max(0, index * Math.max(1, averageItemLength) + anchor.withinItemOffset),
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
									<ErrorMessage message={snapshot.status === "error" ? null : snapshot.error} />
									{/* A subagent's bar already says what you can do instead. */}
									{connected && permitted && !subagentBar && !permitted.send && !permitted.steer && !permitted.queue ? (
										<Copy muted>Sending is unavailable for this session.</Copy>
									) : null}
								</View>
							}
							// Until the conversation first loads, three quiet blocks stand
							// in for it. A loaded conversation with no rows shows nothing:
							// the composer's placeholder invites.
							ListEmptyComponent={conversation ? null : <TranscriptSkeleton />}
						/>
						<SessionHeader
							glassTop={navGlass ? headerHeight : undefined}
							onLayout={(event) => setSessionHeader({ height: event.nativeEvent.layout.height, onGlass: navGlass })}
							status={connectionText}
							chips={chips}
							find={
								find ? (
									<FindBar
										query={find.query}
										label={
											find.exhausted ? "No older matches" : find.query.trim() ? matchLabel(findHits, findCurrent) : ""
										}
										searchingOlder={find.seeking && snapshot.loadingOlder}
										settled={!find.seeking}
										onQuery={(query) => setFind(newFind(query))}
										onStep={stepFind}
										onDone={() => {
											Keyboard.dismiss();
											setFind(null);
										}}
										onGlass={navGlass}
									/>
								) : undefined
							}
							hidden={headerHiding.hidden}
							composerFocus={composerFocus}
							onChip={openChip}
							notes={
								notesPreview ? (
									<NotesBar
										onGlass={navGlass}
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
						<FloatingStack
							toast={toaster.toast ? <Toast toast={toaster.toast} dismiss={toaster.dismiss} /> : null}
							next={
								nextTarget ? (
									<NextCapsule target={nextTarget} onOpen={() => openNext(nextTarget)} onHold={chooseNext} />
								) : null
							}
							pill={newCount > 0 ? <NewContentPill count={newCount} onPress={jumpToLive} /> : null}
							barHeight={barHeight}
							composerFocus={composerFocus}
						/>
					</View>
					{/* The bottom bar (spec 8.1): the tray or a dock and the composer,
					    over the transcript's end so the transcript runs under its glass,
					    and never taller than four fifths of its room. */}
					<BarFrame
						testID="session-bottom-bar"
						style={{
							position: "absolute",
							left: 0,
							right: 0,
							bottom: 0,
							maxHeight:
								bottomBarRoom.height === null
									? (`${BAR_MAX_SHARE * 100}%` as const)
									: bottomBarRoom.height * BAR_MAX_SHARE,
							paddingTop: 8,
						}}
						onLayout={bottomBar.onLayout}
					>
						<ScrollView
							style={{ ...shrinkingScroller, marginBottom: 4 }}
							contentContainerStyle={{ gap: 4, paddingHorizontal: 12 }}
							keyboardShouldPersistTaps="handled"
							nestedScrollEnabled
						>
							{composerShown ? null : waitingForAgent}
							<View style={{ flexDirection: "row", flexWrap: "wrap", gap: 4 }}>
								{controlsState?.error &&
								(controlsState.lastAction === "changeModel" ||
									controlsState.lastAction === "setVisionModel" ||
									controlsState.lastAction === "setReasoningEffort") ? (
									<Action
										tone="quiet"
										onPress={() => openModelSheet(controlsState.lastAction === "setVisionModel" ? "vision" : "model")}
									>
										Review settings error
									</Action>
								) : null}
							</View>
						</ScrollView>
						{/* Only the dock's slot gives up height, so a dock taller than the
						    room left scrolls its body and keeps its answer controls on
						    screen (spec 8.4); the tray and the composer keep theirs. */}
						<View style={{ flexShrink: 1 }}>
							{bottom.dock === "approval" && approval ? (
								<ApprovalDock
									// A new approval starts with nothing decided.
									key={approval.escalationId}
									request={approval}
									// Null while the hub is away: the dock still says what
									// waits, without Allow or Deny.
									controls={approvalControls}
									waiting={(conversation?.pendingEscalations.length ?? 1) - 1}
									onDecided={(allowed) => {
										if (allowed) haptic("success");
										toaster.show({ text: allowed ? "Allowed once" : "Denied" });
									}}
								/>
							) : null}
							{(bottom.dock === "question" || bottom.dock === "foldedQuestion") && questionBatch ? (
								<QuestionDock
									questions={questionBatch.questions}
									draft={questionDraft}
									ready={
										(ready || offlineAdmits) &&
										!!permitted?.send &&
										draft.loaded &&
										!draft.error &&
										unconfirmedSend === null
									}
									waitsForConnection={!connected}
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
									composerFocus={composerFocus}
								/>
							) : null}
						</View>
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
						{subagentOf ? (
							<SubagentPanel
								hubId={route.params.hubId}
								ref={route.params.ref}
								coordinator={subagentOf}
								inFront={focused}
								barShown={subagentBar}
								showToast={showSubagentToast}
								onRow={setSubagentRow}
								navigation={navigation as never}
							/>
						) : null}
						{composerShown ? (
							<Composer
								value={draft.record.draft}
								editable={draft.loaded}
								onChangeText={(text) => {
									// A "/" that starts an empty draft opens Commands and
									// skills in its place (spec 8.5).
									if (text === "/" && draft.record.draft === "" && commandsHost) {
										openCommands();
										return;
									}
									document.edit(text);
								}}
								inputRef={composerInput}
								focus={composerFocus}
								placeholder={composerPlaceholder(onlineAction, answering)}
								// Under an open dock, whose own button reads "Send answer",
								// this Send says it sends what you typed.
								sendLabel={bottom.dock === "question" ? "Send your answer" : composerSendLabel}
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
								onCommands={commandsHost ? openCommands : undefined}
								settings={bottom.modelChip ? composerSettings : null}
								above={
									<>
										{waitingForAgent}
										<ImageAttachments document={document} selection={imageSelection} />
										<ErrorMessage message={imageState.error} />
									</>
								}
							/>
						) : notice ? (
							<SessionNotice
								kind={notice}
								busy={restart.busy || controlsState?.pending === "forceStop" || controlsState?.pending === "resume"}
								disabled={!controls}
								error={notice === "restartNeeded" ? restart.error : null}
								onPress={() => {
									if (notice === "paused") void controls?.resume();
									else void restart.restart();
								}}
							/>
						) : null}
					</BarFrame>
				</View>
			</KeyboardAvoidingView>
		</SafeAreaView>
	);
}
