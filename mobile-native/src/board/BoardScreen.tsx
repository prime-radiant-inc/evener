import {
	type NavigationPinSectionDescriptor,
	type NavigationProjectSummary,
	type NavigationSessionSummary,
	quietState,
	type SearchResult,
} from "@evener/appwire-client";
import { useHeaderHeight } from "@react-navigation/elements";
import { useFocusEffect, useIsFocused } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { SymbolView } from "expo-symbols";
import {
	type ReactNode,
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
	type LayoutChangeEvent,
	type NativeScrollEvent,
	type NativeSyntheticEvent,
	Platform,
	Pressable,
	ScrollView,
	Text,
	TextInput,
	useWindowDimensions,
	View,
} from "react-native";
import Animated from "react-native-reanimated";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { useConnection } from "../ConnectionProvider";
import type { NavigationActions } from "../navigationActions";
import { getNativeMutationRuntime } from "../nativeMutationRuntime";
import { drafts } from "../nativeDrafts";
import { useReduceMotion } from "../accessibilitySettings";
import { GlassHeaderPanel } from "../design/GlassHeaderPanel";
import { ChipStrip } from "../design/ChipStrip";
import { scaledType, uiType } from "../design/tokens";
import { navBarGlassOptions, reservedUnderGlass, useSystemGlass } from "../design/systemGlass";
import { useHeaderTextScale } from "../headerText";
import type { Routes } from "../screens";
import { sheetKey, useProvideSheetHost } from "../sheet/sheetHosts";
import { useScreenInFront } from "../sheet/useScreenInFront";
import { Toast, type ToastController, useToast } from "../Toast";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import {
	type Band,
	type ClassifiedRow,
	hostLabeler,
	type LiveSummary,
	liveBands,
	liveSummary,
	plural,
	rowClassifier,
	sectionLabel,
	summaryText,
	usualPlace,
} from "./attention";
import { type BoardItem, groupItems, liveItems, pinnedItems, projectItems } from "./boardItems";
import type { OrganizeBy, SeenMarkers } from "./boardMemory";
import { ROW_MOVE } from "./boardMotion";
import { createSearchController, projectResults, type SearchScope } from "./boardSearch";
import { FreshDot } from "../reader/FreshDot";
import { documentMemory } from "../reader/nativeDocumentMemory";
import { openDocumentInSession } from "../reader/openDocument";
import { BoardNotices, NoticeRow } from "./BoardNotices";
import { ContinueReadingRow } from "./ContinueReadingRow";
import { BandHeader, FoldChevron, Hairline, TITLE_INSET } from "./BoardRow";
import { BoardListRow, type RowContext } from "./BoardRows";
import { BoardToolbar, type ToolbarPlacement } from "./BoardToolbar";
import { type BoardController, type BoardSnapshot, createBoardController } from "./boardData";
import { useBoardReadRetry } from "./useBoardReadRetry";
import { useActivityPoll } from "./useActivityPoll";
import { type HeldAction, heldFor, heldProjectState, heldVerb, turnSeen, waitingLine } from "./boardHold";
import { onBoardJump } from "./boardJump";
import { BoardReplay } from "./boardReplay";
import { BoardStops, stopToast } from "./boardStops";
import { INCOMPATIBLE_VERSIONS } from "../connectionRecovery";
import { type HubSeenMarks, hubSeenMarks } from "./hubSeen";
import {
	boardHold,
	foldedSections,
	organizeByPreference,
	recentSearches,
	seenMarkers,
	useBoardSeen,
} from "./nativeBoardMemory";
import { notices } from "./notices";
import { PinnedEmptyHint, PinnedSection, useBoardFolds, useCategoryFolds } from "./PinnedSections";
import { journalHoldsProject, PROJECT_MENU_LABELS, type ProjectMenuAction, projectMenuActions } from "./projectMenu";
import {
	expandedProjectKeys,
	grouping,
	liveCountsByHost,
	type ProjectSection,
	type ProjectsView,
	type ProjectTreeItem,
	projectRevealTarget,
	projectTreeItems,
	SECTION_FOLDS,
} from "./projectTree";
import { projectName, ProjectSectionHeader, ProjectTreeRow } from "./ProjectTreeRow";
import { fleetMinutes } from "./pulse";
import { PulseMeter } from "./PulseMeter";
import {
	archiveTarget,
	archivingSessionId,
	journalOutcome,
	projectChange,
	type RowAction,
	type RowActionContext,
	renameSession,
	RENAMED,
	renameFailed,
	rowMenuActions,
	SHUT_DOWN_DONE,
	shutDownFailed,
	shutDownSession,
} from "./rowActions";
import { type RowMenuHost, rowMenuHosts } from "./RowMenu";
import { archiveRequest, archiveRow, rowSwipes, type SwipeRowAction } from "./rowSwipes";
import { SearchResults } from "./SearchResults";
import { SelectBar } from "./SelectBar";
import { selectionActions, toggleSelected } from "./selection";
import { listScrollHandlers } from "./settledList";
import { journalOperation, organizationOpen } from "./organizationCheck";
import { type BoardOrganization, useBoardOrganization } from "./useBoardOrganization";
import { PROJECT_SECTIONS, showExpanded, useProjectSections } from "./useProjectSections";
import { useSettledList } from "./useSettledList";
import { FLOAT_GAP, underBar, useBarHeight } from "../design/underBar";
import { destructiveButton, haptic } from "../haptics";
import { Button } from "../sheet/Grouped";

type Props = NativeStackScreenProps<Routes, "Sessions">;
type Navigation = Props["navigation"];

const MINUTE = 60_000;
const BAND_HEADERS: Record<Exclude<Band, "idle">, string> = {
	needsYou: "NEEDS YOU",
	working: "WORKING",
};
/** A row that reads a section's next page: a tier's sessions or the catalog's projects. */
type MoreItem = Extract<ProjectTreeItem, { kind: "more" | "moreProjects" }>;

/** Home (spec 7.1): every live session ordered by who needs you, then the
 * user's pinned categories, projects and archive. */
export function BoardScreen({ navigation, route }: Props) {
	const { activeProfile } = useConnection();
	const { palette } = useColors();
	if (!activeProfile) return <View style={{ flex: 1, backgroundColor: palette.page }} />;
	return (
		<Board
			key={activeProfile.id}
			hubId={activeProfile.id}
			hubName={activeProfile.name}
			navigation={navigation}
			routeKey={route.key}
		/>
	);
}

function Board({
	hubId,
	hubName,
	navigation,
	routeKey,
}: {
	hubId: string;
	hubName: string;
	navigation: Navigation;
	routeKey: string;
}) {
	const { client, state, fatal, activeProfile } = useConnection();
	const { palette } = useColors();
	const connected = state === "ready";
	// This Board's own hub is the connected one, so a hub write may go out.
	const actionsConnected = connected && activeProfile?.id === hubId;
	// The client a hub write goes out on, or null while none may.
	const actionsClient = actionsConnected ? client : null;
	const focused = useIsFocused();
	// Activity keeps polling while only a sheet covers the Board: the sheet
	// is part of the screen under it.
	const inFront = useScreenInFront(routeKey);
	const {
		activityOf,
		msSinceRead,
		revision: activityRevision,
		tick: activityTick,
	} = useActivityPoll(client, connected, inFront);
	const [board] = useState(createBoardController);
	useEffect(() => () => board.dispose(), [board]);
	const snapshot = useSyncExternalStore(board.subscribe, board.getSnapshot);
	const markers = seenMarkers(hubId);
	const hubMarks = hubSeenMarks(hubId);
	const seen = useBoardSeen(hubId);
	const [now, setNow] = useState(Date.now);
	const [draftRefs, setDraftRefs] = useState<Set<string>>(() => new Set());
	// Select mode (spec 7.1): on from Select until Done or one of its
	// actions completes (ruling 26), with the refs chosen so far.
	const [selecting, setSelecting] = useState(false);
	// How tall the toolbar (or the select bar) stands over the Board's end.
	const toolbar = useBarHeight();
	const toolbarHeight = toolbar.height ?? 0;
	const boardUnderBar = underBar(toolbarHeight);
	const toolbarPlacement: ToolbarPlacement = {
		testID: "board-toolbar",
		style: { position: "absolute", left: 0, right: 0, bottom: 0 },
		onLayout: toolbar.onLayout,
	};
	const [chosen, setChosen] = useState<ReadonlySet<string>>(() => new Set());
	const leaveSelect = () => {
		setSelecting(false);
		setChosen(new Set());
	};
	const choose = (row: NavigationSessionSummary) => {
		const next = toggleSelected(chosen, row.ref);
		setChosen(next);
		AccessibilityInfo.announceForAccessibility(`${next.size} selected`);
	};

	// Declared before the client binding so a first focus resumes an unbound
	// controller rather than one whose reads just went out.
	useFocusEffect(
		useCallback(() => {
			board.resume();
			setDraftRefs(readDraftRefs(hubId));
			setNow(Date.now());
			// Row ages are live labels: re-render them once a minute while the
			// Board is in view.
			const minutes = setInterval(() => setNow(Date.now()), MINUTE);
			return () => {
				clearInterval(minutes);
				board.pause();
			};
		}, [board, hubId]),
	);
	// A client rejects every request until it is ready, so the Board binds
	// only a ready one. A client ready again after reconnecting gets fresh
	// readers, which catch up on invalidations missed while it was down.
	useEffect(() => {
		board.setClient(state === "ready" ? client : null);
	}, [board, client, state]);
	const firstReadFailed = connected && !snapshot.loaded && snapshot.live.error !== null;
	// The retry rests while the Board is out of view: the controller is
	// paused then, and a paused read is cancelled, not answered.
	useBoardReadRetry(board, connected && focused ? client : null, snapshot);

	// Whether a session moved after the person last looked, from the activity
	// read's last-moved time against the hub's seen mark (the blue dot).
	const lastMovedAt = useCallback((row: NavigationSessionSummary) => activityOf(row.ref)?.lastMovedAt, [activityOf]);
	const movedSinceSeen = useCallback(
		(row: NavigationSessionSummary) => seen.movedSinceSeen(row, lastMovedAt(row)),
		[seen, lastMovedAt],
	);
	const bands = useMemo(
		() =>
			liveBands(
				snapshot.live.rows,
				snapshot.needsYou.rows,
				(row) => seen.isSeen(row),
				(row) => {
					const activity = activityOf(row.ref);
					return activity ? quietState(activity, msSinceRead ?? 0)?.state === "stuck" : false;
				},
				movedSinceSeen,
			),
		// seen re-runs isSeen after a mark, a pruned mark or first run.
		// activityRevision re-runs isStuck after each read, and activityOf
		// changes when the connection drops or returns, or the read goes
		// stale. Bare msSinceRead is left out on purpose: it changes on every
		// render, which would re-sort Working every render. activityTick
		// (useActivityPoll's own recheck, already ticking at ACTIVITY_POLL_MS
		// while a fresh read is on screen) stands in for it instead, so a row
		// that crosses into stuck purely from elapsed time - no new read
		// landing, quiet time alone reaching STUCK_AFTER_MS - still floats to
		// the top within one poll interval of its why-line saying so, instead
		// of waiting for the next successful read.
		[snapshot.live.rows, snapshot.needsYou.rows, seen, activityOf, activityRevision, activityTick, movedSinceSeen],
	);
	useFirstRun(board, markers, snapshot, focused);

	const usual = useMemo(() => usualPlace(snapshot.live.rows), [snapshot.live.rows]);
	const sources = snapshot.manifest?.sources;
	const hostLabel = useMemo(() => hostLabeler(sources), [sources]);

	// activityRevision re-classifies after each read, so a row's dot follows
	// its newest motion.
	const classify = useMemo(
		() => rowClassifier(snapshot.needsYou.rows, (row) => seen.isSeen(row), movedSinceSeen),
		[snapshot.needsYou.rows, seen, movedSinceSeen, activityRevision],
	);
	// A project section's session row: its approval comes from the row's own
	// flag alone, not from the needs_you section's membership.
	const projectRow = useMemo(
		() => rowClassifier([], (row) => seen.isSeen(row), movedSinceSeen),
		[seen, movedSinceSeen, activityRevision],
	);
	const folds = useCategoryFolds(hubId);
	const organization = useBoardOrganization(hubId);
	const toast = useToast();
	// Stop from the Board (ruling 17), one per hub. A new client lets go of
	// every Stop the old one was delivering; an interrupt not yet sent stays
	// in the outbox, delivered as any durable Stop is.
	const [stops] = useState(() => new BoardStops(getNativeMutationRuntime, hubId));
	useEffect(() => () => stops.dispose(), [stops]);
	useEffect(() => () => stops.releaseAll(), [stops, client]);
	// Board actions taken offline wait in the hold and go when the connection
	// returns (phase 6 ruling 18): Stop, Shut down and Rename at once,
	// wherever you are; organization changes through the journal once the
	// Board is focused and it's free.
	const [hold] = useState(() => boardHold(hubId));
	const held = useSyncExternalStore(hold.subscribe, hold.getSnapshot);
	const holdAction = (action: HeldAction) => hold.hold(action, Date.now());
	// A press answered later (an alert, a sheet) asks what's true then.
	const liveNow = useRef(actionsConnected);
	liveNow.current = actionsConnected;
	const clientNow = useRef(actionsClient);
	clientNow.current = actionsClient;
	const toastNow = useRef(toast);
	toastNow.current = toast;
	const [replay] = useState(
		() =>
			new BoardReplay(hold, {
				stop: (on, ref, guard) => stops.stop(on, ref, guard),
				toast: (text) => toastNow.current.show({ text }),
			}),
	);
	useEffect(() => () => replay.dispose(), [replay]);
	// Whether the connection a replay runs on is still live, asked of the
	// client itself: a request lost to a drop fails before any render could
	// say so, and must stay held rather than read as refused.
	const liveOn = (on: typeof actionsClient) => () => on?.state === "ready" && clientNow.current === on;
	useEffect(() => {
		if (actionsClient && held.length) void replay.sendImmediate(actionsClient, liveOn(actionsClient));
	}, [replay, actionsClient, held]);
	const organizationNow = useRef(organization);
	organizationNow.current = organization;
	useEffect(() => {
		if (focused && organization.ready && held.length)
			void replay.organize(organizationNow.current, liveOn(clientNow.current));
	}, [replay, focused, organization.ready, held]);
	const categoryMenu = pinnedCategoryMenu(organization, () => board.getSnapshot().pins.rows);
	const projectSections = useProjectSections(hubId);
	const [organizeBy, setOrganizeBy] = useState(() => organizeByPreference(hubId).get());
	// Every fold inside the project sections, by its ProjectTreeItem fold.
	const { isFolded, setFolded } = useBoardFolds(hubId);
	// Every session row the Board has loaded so far, from any section. A
	// project section's view keeps its identity until its reads change.
	const projectViews = PROJECT_SECTIONS.map((section) => projectSections[section].view);
	const loadedRows = useMemo(
		() => [
			...snapshot.live.rows,
			...snapshot.needsYou.rows,
			...Object.values(snapshot.pinSections).flatMap((page) => page.rows),
			...projectViews.flatMap(projectSessionRows),
		],
		[snapshot.live.rows, snapshot.needsYou.rows, snapshot.pinSections, ...projectViews],
	);
	useHubSeenMarks(hubMarks, actionsClient, loadedRows);
	// The document you left partway in the last two hours (spec 7.1). The
	// window is checked as the Board renders, and the Board's minute clock
	// re-renders it while in view, so the row goes within a minute of expiring.
	const documents = documentMemory(hubId);
	useSyncExternalStore(documents.subscribe, documents.getRevision);
	const continueReading = documents.continueReading();
	const hubNotices = useMemo(
		() => notices({ hubNotices: snapshot.notices, sources: sources ?? [] }),
		[snapshot.notices, sources],
	);

	const [idleFolded, setIdleFolded] = useState(() => foldedSections(hubId).isFolded("idle", true));
	const foldIdle = (folded: boolean) => {
		foldedSections(hubId).setFolded("idle", folded);
		setIdleFolded(folded);
	};

	const scroller = useRef<ScrollView>(null);
	// Where the device has Liquid Glass, one glass spans the nav bar and the
	// chips under it (spec 16.3), and the Board scrolls under both: its
	// content is inset by them (underGlass), and every scroll it makes itself
	// lands clear of them. Until the glass has measured, that's the bar alone.
	const headerHeight = useHeaderHeight();
	const navGlass = useSystemGlass();
	const [headerPanel, setHeaderPanel] = useState({ height: 0, onGlass: false });
	const underGlass = navGlass ? reservedUnderGlass(headerHeight, headerPanel, true) : 0;
	// Search is bound only while the Board is in view (the plugin poll's
	// rule): a reconnect while a pushed screen covers the Board must not send
	// one `evener/search` for the query the field still holds. A sheet over
	// the Board is still the Board (ruling 28), so it stays bound then.
	const search = useSearch(connected && inFront ? client : null);
	const searchInput = useRef<TextInput>(null);
	const [searchText, setSearchText] = useState("");
	// Searching from the moment the field takes focus until Cancel.
	const [searching, setSearching] = useState(false);
	const [scope, setScope] = useState<SearchScope>("all");
	const searchFieldHeight = searchFieldHeightAt(useTextScale());
	const reduceMotion = useReduceMotion();
	const { height: windowHeight } = useWindowDimensions();
	const recent = recentSearches(hubId);
	const [recentList, setRecentList] = useState(() => recent.list());
	const typeSearch = (text: string) => {
		setSearchText(text);
		search.controller.setQuery(text);
	};
	const leaveSearch = () => {
		typeSearch("");
		setSearching(false);
		searchInput.current?.blur?.();
	};
	const cancelSearch = () => {
		leaveSearch();
		// Tuck the field back out of view, where the Board keeps it.
		scrollBoardTo(searchFieldHeight);
	};

	const newSession = () => navigation.navigate("NewSession", { hubId, hubName });
	const openSession = (row: NavigationSessionSummary) => {
		seen.markRead(actionsClient, [row], lastMovedAt);
		navigation.navigate("Conversation", { hubId, ref: row.ref, title: row.title });
	};
	// A search result the Board has loaded opens like its row, so it's
	// marked seen the same way. Any other session marks itself seen when its
	// screen loads (useMarkSeenInFront).
	const openSearchResult = (result: SearchResult) => {
		rememberSearch();
		const row = loadedRows.find((loaded) => loaded.ref === result.ref);
		if (row) openSession(row);
		else navigation.navigate("Conversation", { hubId, ref: result.ref, title: result.title });
	};
	const clearRecent = () => {
		recent.clear();
		setRecentList(recent.list());
	};
	const rememberSearch = () => {
		recent.add(search.snapshot.query);
		setRecentList(recent.list());
	};

	// Where each section starts in the scroller, for the chips and the
	// summary line to jump to. Bands measure inside the Live block.
	const offsets = useRef<Record<string, number>>({});
	// Search's project hit waiting to be scrolled to: the item key while it
	// waits, and what has laid out since (revealProject).
	const [revealKey, setRevealKey] = useState<string | null>(null);
	const reveal = useRef<{ sectionTop: number | null; row: { y: number; height: number } | null } | null>(null);
	const liveEnd = useRef<number | null>(null);
	const measure = (key: string) => (event: LayoutChangeEvent) => {
		offsets.current[key] = event.nativeEvent.layout.y;
		jumpWhenLaidOut.current();
	};
	const scrollTo = (key: string, withinLive = false) =>
		scrollBoardTo((withinLive ? (offsets.current.live ?? 0) : 0) + (offsets.current[key] ?? 0));
	const jumpToBand = (band: Band) => {
		if (band === "idle") foldIdle(false);
		scrollTo(band, true);
	};
	// A tapped "3 sessions need you" banner asks for Needs you here, since the
	// Board's route takes no params (boardJump.ts). It can ask before the
	// Board has laid out (it popped to a Board just mounted), so the jump
	// waits until Live and the band have, then scrolls.
	const pendingJump = useRef<Band | null>(null);
	const jumpWhenLaidOut = useRef(() => {});
	jumpWhenLaidOut.current = () => {
		const band = pendingJump.current;
		if (band === null || offsets.current.live === undefined || offsets.current[band] === undefined) return;
		pendingJump.current = null;
		jumpToBand(band);
	};
	useEffect(
		() =>
			onBoardJump((section) => {
				pendingJump.current = section;
				jumpWhenLaidOut.current();
			}),
		[],
	);
	// Within about a screen of the end of Live, read its next page. Layout
	// checks too, so a first page too short to scroll keeps reading.
	const viewport = useRef({ offset: 0, height: 0 });
	// Where each of the project sections' "more" rows (a tier's sessions, or
	// the catalog's projects) was last laid out inside its section.
	const moreFrames = useRef(new Map<string, { y: number; height: number }>());
	const readMoreLiveIfNear = () => {
		const page = board.getSnapshot().live;
		// Search results fill the scroller in Live's place.
		if (searching || liveEnd.current === null || page.remaining === 0 || page.loading || page.stale || page.error)
			return;
		const { offset, height } = viewport.current;
		if (offset + 2 * height >= liveEnd.current) void board.loadMoreLive();
	};
	const onScroll = (event: NativeSyntheticEvent<NativeScrollEvent>) => {
		const { contentOffset, layoutMeasurement } = event.nativeEvent;
		// While the Board's own scroll is under way, scrollerOffset holds where
		// it's headed, which a glass change re-targets; its progress would
		// overwrite that.
		if (list.state !== "appScrolling") scrollerOffset.current = contentOffset.y;
		// What shows below the glass, in the Board's content.
		viewport.current = { offset: contentOffset.y + underGlass, height: layoutMeasurement.height - underGlass };
		scrolledFromTuck.current = true;
		readMoreLiveIfNear();
		readVisibleMore();
	};
	// iOS applies the scroller's initial content offset once, so a Dynamic
	// Type change would leave the field tucked at the old height. Re-apply the
	// offset for the new height, but only while the field is still tucked: a
	// revealed field, a search in progress or a scroll down the list is left
	// where the reader put it.
	const tuckedHeight = useRef(searchFieldHeight);
	const scrolledFromTuck = useRef(false);
	useEffect(() => {
		const was = tuckedHeight.current;
		if (was === searchFieldHeight) return;
		tuckedHeight.current = searchFieldHeight;
		if (searching) return;
		if (scrolledFromTuck.current && Math.abs(viewport.current.offset - was) > 1) return;
		scroller.current?.scrollTo?.({ y: searchFieldHeight - underGlass, animated: false });
	}, [searchFieldHeight, searching]);
	// The scroller's own offset, which the glass's inset shifts from the
	// content's: where it starts (contentOffset below), then each scroll, or
	// where a scroll the Board makes itself is headed.
	const scrollerOffset = useRef(searchFieldHeight - underGlass);

	const manifest = snapshot.manifest;
	const liveTotal = bands.needsYou.length + bands.working.length + bands.idle.length;
	// Every category keeps its section; only the chips hide empty ones.
	const pins = snapshot.pins.rows;
	const projects = manifest?.catalogs.projects.count ?? 0;
	// Projects shows while the manifest counts projects, until its catalog
	// loads empty; the chip follows the section.
	const projectsShown =
		projects > 0 && !(projectSections.projects.view.loaded && projectSections.projects.view.projects.length === 0);
	const archived = manifest?.catalogs.archived_projects.count ?? 0;
	const chips: ChipProps[] = [];
	const liveChipCount = manifest?.sections.live.count ?? liveTotal;
	if (liveChipCount > 0)
		chips.push({
			key: "live",
			name: "Live",
			count: liveChipCount,
			badge: bands.needsYou.length,
			label: `Live, ${plural(liveChipCount, "session")}${bands.needsYou.length ? `, ${summaryText("needsYou", bands.needsYou.length)}` : ""}`,
			onPress: () => scrollTo("live"),
		});
	for (const pin of pins.filter((category) => category.count > 0))
		chips.push({
			key: `pin:${pin.id}`,
			name: pin.name,
			count: pin.count,
			pinned: true,
			label: sectionLabel(pin.name, pin.count, "session"),
			// A chip that landed on a folded header would show nothing.
			onPress: () => {
				folds.setFolded(pin.id, false);
				scrollTo(`pin:${pin.id}`);
			},
		});
	if (projectsShown)
		chips.push({
			key: "projects",
			name: "Projects",
			count: projects,
			label: sectionLabel("Projects", projects, "project"),
			onPress: () => scrollTo("projects"),
		});
	if (archived > 0)
		chips.push({
			key: "archived",
			name: "Archived",
			count: archived,
			label: sectionLabel("Archived", archived, "project"),
			onPress: () => scrollTo("archived"),
		});

	// What a row's actions depend on: whether it sits in an archived tier.
	// Offline or with the journal busy the same actions are offered, and a
	// change that can't go now is held (holdsChange below).
	const rowContext = useCallback((archived: boolean): RowActionContext => ({ archived }), []);
	// The row menu, a sheet route that asks the Board's host below for the
	// row and its actions. It carries the tier it opened from, since a
	// session can show twice (Live and a project's Archived tier) and the two
	// copies offer different actions (Archive vs. Unarchive).
	// It holds the list while it's open (ruling 22), until its host hears
	// it close.
	const openRowMenu = (item: ClassifiedRow, archived: boolean) => {
		list.setInteraction("menu", true);
		navigation.navigate("RowMenuSheet", { hubId, ref: item.row.ref, archived });
	};
	/** Whether a change of this kind waits in the hold for this session. */
	const waitsFor = (ref: string, kind: HeldAction["kind"]) =>
		heldFor(hold.getSnapshot(), ref).some((record) => record.action.kind === kind);
	/** Whether a Board change goes into the hold rather than out now (phase 6
	 * ruling 18): offline; for an organization change (an archive, a pin, a
	 * project setting), while the journal can't take one; and while a change
	 * on the same subject waits in the hold, which the new one then replaces,
	 * undoes or queues behind rather than racing its replay. A held change
	 * goes when it can, so a Board change is never refused. */
	const holdsChange = (waiting: boolean, organization: boolean) =>
		waiting || !liveNow.current || (organization && !organizationOpen(organizationNow.current));
	const runRowAction = (item: ClassifiedRow, action: Exclude<SwipeRowAction, "more">) => {
		const { row } = item;
		const kind = action === "pin" ? "pin" : action === "stop" ? "stop" : "archive";
		if (holdsChange(waitsFor(row.ref, kind), kind !== "stop")) holdRowAction(row, action);
		else if (action === "pin") navigation.navigate("PinAssignment", { hubId, ref: row.ref, title: row.title });
		else if (action === "stop" && client)
			void stops.stop(client, row.ref).then((outcome) => toast.show({ text: stopToast(outcome, row.title) }));
		else if (action === "archive" || action === "unarchive") archiveOrHold(row, action === "archive");
	};
	/** Archive or Unarchive a row, and its Undo: through the journal, or held
	 * when it can't go now (holdsChange), including when the journal turns
	 * out not to take it. */
	const holdArchive = (archived: boolean) => (row: NavigationSessionSummary) =>
		holdRowAction(row, archived ? "archive" : "unarchive");
	const archiveOrHold = (row: NavigationSessionSummary, archived: boolean) => {
		const holdIt = () => holdArchive(archived)(row);
		const actions = organizationNow.current.actions;
		if (holdsChange(waitsFor(row.ref, "archive"), true) || !actions) holdIt();
		else
			void archiveRow(actions, row, archived, toast, () => archiveOrHold(row, !archived)).then((outcome) => {
				if (outcome === "notTaken") holdIt();
			});
	};
	/** A swipe's or the menu's action held until it can go: Pin asks for the
	 * category over the Board's loaded catalog, and a Stop names the turn you
	 * saw (phase 6 ruling 18). */
	const holdRowAction = (row: NavigationSessionSummary, action: Exclude<SwipeRowAction, "more">) => {
		if (action === "pin")
			chooseCategory(
				board.getSnapshot().pins.rows,
				toast,
				(section) => void holdAction({ kind: "pin", target: { sessionRef: row.ref, ...section } }),
			);
		else if (action === "stop") holdAction({ kind: "stop", ref: row.ref, title: row.title, seen: turnSeen(row) });
		else {
			const target = archiveTarget(row);
			if (target) holdAction({ kind: "archive", ref: row.ref, target, archived: action === "archive" });
		}
	};
	/** The menu's actions: Pin, Stop, Archive and Unarchive as the swipes do
	 * them, and Shut down and Rename as the Session sends them (rulings
	 * 18-21), held when they can't go now. */
	const actOnRow = (item: ClassifiedRow, action: RowAction) => {
		const { row } = item;
		if (action === "shutDown")
			confirmShutDown(row, () => {
				const on = holdsChange(waitsFor(row.ref, "shutDown"), false) ? null : clientNow.current;
				if (!on) holdAction({ kind: "shutDown", ref: row.ref, title: row.title, seen: turnSeen(row) });
				else
					shutDownSession(on, row.ref).then(
						() => toast.show({ text: SHUT_DOWN_DONE }),
						(error: unknown) => toast.show({ text: shutDownFailed(row.title, error) }),
					);
			});
		else if (action === "rename")
			promptRename(row, (name) => {
				const on = holdsChange(waitsFor(row.ref, "rename"), false) ? null : clientNow.current;
				if (!on) {
					if (name.trim()) holdAction({ kind: "rename", ref: row.ref, title: row.title, name: name.trim() });
					return;
				}
				renameSession(on, row.ref, name).then(
					(renamed) => {
						if (renamed) toast.show({ text: RENAMED });
					},
					(error: unknown) => toast.show({ text: renameFailed(row.title, error) }),
				);
			});
		else runRowAction(item, action);
	};
	const archivingId = archivingSessionId(organization.state);
	const listContext: RowContext = {
		connected,
		usual,
		hostLabel,
		now,
		// In select mode a press chooses the row instead of opening it.
		onOpen: selecting ? choose : openSession,
		draftRefs,
		activityOf,
		msSinceRead,
		waiting: (ref) => waitingLine(held, ref, actionsConnected),
		swipes: (item, archived) =>
			rowSwipes(item, rowContext(archived), archivingId, (action) =>
				action === "more" ? openRowMenu(item, archived) : runRowAction(item, action),
			),
		menu: (item, archived) => ({
			actions: menuActionsHere(item, rowContext(archived)),
			onOpenSession: () => openSession(item.row),
			onAction: (action) => actOnRow(item, action),
			onOpenSheet: () => openRowMenu(item, archived),
			onOpenChange: (open) => list.setInteraction("menu", open),
		}),
	};
	const summary = liveSummary(bands);
	// The fleet meter sums the working sessions the poll has read so far, and
	// stays still until it has read one.
	const workingMinutes = bands.working
		.map((item) => activityOf(item.row.ref)?.minutes)
		.filter((minutes): minutes is number[] => minutes !== undefined);
	const fleetPerMinute = workingMinutes.length ? fleetMinutes(workingMinutes) : undefined;

	// Projects (or Hosts), Test runs and Archived, after the pinned categories.
	const hostSources = sources ?? [];
	const projectGrouping = grouping(hostSources, organizeBy);
	const chooseOrganizeBy = (next: OrganizeBy) => {
		organizeByPreference(hubId).set(next);
		setOrganizeBy(next);
	};
	const testRuns = manifest?.catalogs.test_runs.count ?? 0;
	const projectHeaders: Record<ProjectSection, { title: string; label: string; shown: boolean }> = {
		projects: {
			title: projectGrouping === "host-project" ? "HOSTS" : "PROJECTS",
			label: projectGrouping === "host-project" ? "Hosts" : "Projects",
			shown: projectsShown,
		},
		"test-runs": {
			title: `Test runs · ${testRuns}`,
			label: sectionLabel("Test runs", testRuns, "project"),
			shown: testRuns > 0,
		},
		archived: {
			title: `ARCHIVED · ${archived}`,
			label: sectionLabel("Archived", archived, "project"),
			shown: archived > 0,
		},
	};
	const shownProjectSections = PROJECT_SECTIONS.filter((section) => projectHeaders[section].shown);
	const { live: livePage, needsYou: needsYouPage } = snapshot;
	// Memoized, with the Board's list below: a new list each render would
	// apply again each render.
	const shownSections = useMemo(() => {
		// A host's live count needs every Live and Needs you row (ruling 12).
		const hostLiveCount = liveCountsByHost(
			[...livePage.rows, ...needsYouPage.rows],
			livePage.loaded && needsYouPage.loaded && livePage.remaining === 0 && needsYouPage.remaining === 0,
		);
		return shownProjectSections.map((section) => {
			const folded = isFolded(SECTION_FOLDS[section].fold, SECTION_FOLDS[section].foldedByDefault);
			const { view } = projectSections[section];
			const items = folded
				? []
				: projectTreeItems({
						section,
						projects: view.projects,
						pages: view.pages,
						sources: sources ?? [],
						organizeBy,
						isFolded,
						hostLiveCount,
						remainingProjects: view.remaining,
					});
			return { section, folded, items };
		});
	}, [shownProjectSections.join(" "), isFolded, ...projectViews, sources, organizeBy, livePage, needsYouPage]);

	// The Board's list (spec 7.3, ruling 22): held still while touched, moving
	// or covered by an interaction, and applied at once when it settles.
	const boardItems = useMemo(
		() => [
			...liveItems(bands, idleFolded),
			...pinnedItems(pins, snapshot.pinSections, folds.isFolded, classify),
			...projectItems(shownSections, projectRow),
		],
		[bands, idleFolded, pins, snapshot.pinSections, folds.isFolded, classify, shownSections, projectRow],
	);
	// Nothing reaches the list before the first read lands, so a touch on the
	// skeleton holds no empty frame over the rows that read brings.
	const { list, snapshot: settled } = useSettledList(snapshot.loaded ? boardItems : null);
	const shownGroups = groupItems(settled.display);
	const rowMove = reduceMotion ? undefined : ROW_MOVE;
	const scrollHandlers = useMemo(() => listScrollHandlers((event) => list.send(event)), [list]);
	// Every animated scroll the Board starts holds the list until it ends.
	// Under Reduce Motion it jumps instead, and holds nothing. A test
	// renderer's host ScrollView has no instance to scroll.
	// `y` is where in the Board's content to show at its top, which on the
	// glass is the glass's lower edge.
	const scrollBoardTo = useCallback(
		(y: number) => {
			if (!reduceMotion) list.send("appScrollStart");
			scrollerOffset.current = y - underGlass;
			scroller.current?.scrollTo?.({ y: scrollerOffset.current, animated: !reduceMotion });
		},
		[list, reduceMotion, underGlass],
	);
	// When the glass comes or goes, or grows or shrinks (the chips appear, or
	// search hides them), the same content stays at the glass's lower edge.
	// A scroll the Board is making (Search's reveal, which hides the chips as
	// it starts) goes on to its content's new place instead of stopping.
	const shownUnderGlass = useRef(underGlass);
	useLayoutEffect(() => {
		const change = underGlass - shownUnderGlass.current;
		shownUnderGlass.current = underGlass;
		if (change === 0) return;
		scrollerOffset.current -= change;
		scroller.current?.scrollTo?.({ y: scrollerOffset.current, animated: list.state === "appScrolling" });
	}, [underGlass, list]);
	// The field sits above the Board, scrolled out of view, so Search brings
	// it down (spec 7.4).
	const revealSearch = useCallback(() => {
		scrollBoardTo(0);
		searchInput.current?.focus?.();
	}, [scrollBoardTo]);
	useHeader(navigation, hubId, hubName, revealSearch, navGlass, palette.page);
	// Leaving lets go (ruling 22): a screen pushed over the Board (its own
	// sheets are part of it, ruling 28), or the app leaving the foreground.
	useEffect(() => {
		if (!inFront) list.send("reset");
	}, [inFront, list]);
	// Leaving the app releases every hold; coming back while selecting takes
	// select mode's hold again, since it lasts until Done or an action.
	const [appActive, setAppActive] = useState(true);
	useEffect(() => {
		const subscription = AppState.addEventListener("change", (state) => {
			const away = state === "background" || state === "inactive";
			if (away) list.send("reset");
			setAppActive(!away);
		});
		return () => subscription.remove();
	}, [list]);
	// Select mode holds the list while the Board is in front (ruling 22).
	useEffect(
		() => list.setInteraction("select", selecting && inFront && appActive),
		[list, selecting, inFront, appActive],
	);
	// The session rows the Board's list shows now, a departed row a hold keeps
	// on screen included (ruling 22).
	const shownRowItems = settled.display.flatMap((item) =>
		item.kind === "row" ? [{ item: item.item, archived: item.archived }] : [],
	);
	// The rows the row menu sheet can be about: those, then Live's and the
	// categories' (a fold hides them, but they stay loaded) and the project
	// sessions in the shown tree, so the menu stays on whichever row it opened
	// from.
	const shownRows = useShownRows([
		...shownRowItems,
		...[...bands.needsYou, ...bands.working, ...bands.idle].map((item) => ({
			item,
			archived: false,
		})),
		...pins.flatMap((pin) => {
			const page = snapshot.pinSections[pin.id];
			return page?.loaded ? page.rows.map((row) => ({ item: classify(row), archived: false })) : [];
		}),
		...shownSections.flatMap(({ items }) =>
			items.flatMap((item) =>
				item.kind === "session" ? [{ item: projectRow(item.row), archived: item.archived }] : [],
			),
		),
	]);
	// The sheet reads the row live, so its actions follow the row while it's
	// open, and hands each answer back to the Board's own handlers.
	const menuHandlers = useRef({ actOnRow, openSession });
	// The sheet reads these at press time, so they are written after the commit
	// that made them current, not during render.
	useEffect(() => {
		menuHandlers.current = { actOnRow, openSession };
	});
	const rowMenuHost = useMemo<RowMenuHost>(
		() => ({
			item: (ref, archived) => shownRows.get(shownRowKey(ref, archived))?.item,
			actions: (item, archived) => menuActionsHere(item, rowContext(archived)),
			hostLabel,
			act: (item, action) => menuHandlers.current.actOnRow(item, action),
			// What is on its way can't be taken back, so it offers no Cancel.
			held: (item) =>
				heldFor(hold.getSnapshot(), item.row.ref)
					.filter((record) => !hold.isSending(record.id))
					.map((record) => ({ id: record.id, label: `Cancel ${heldVerb(record.action)}` })),
			cancel: (id) => hold.cancel(id),
			openSession: (item) => menuHandlers.current.openSession(item.row),
			closed: () => list.setInteraction("menu", false),
		}),
		// A new host whenever the hold changes, so an open menu re-reads its
		// Cancel entries.
		[shownRows, rowContext, hostLabel, list, hold, held],
	);
	useProvideSheetHost(rowMenuHosts, sheetKey(hubId), rowMenuHost);
	const itemsOf = (section: ProjectSection) => shownSections.find((shown) => shown.section === section)?.items ?? [];
	// A section's catalog is read when it's first shown unfolded, so a folded
	// Test runs or Archived costs no read.
	const opening = focused ? shownSections.filter((shown) => !shown.folded).map((shown) => shown.section) : [];
	const openingKey = opening.join(" ");
	// The sections' controllers change together, with the connection.
	const projectsController = projectSections.projects.controller;
	useEffect(() => {
		for (const section of opening) projectSections[section].open();
	}, [openingKey, projectsController]);
	// After each render, the browsers read exactly the projects on screen.
	useEffect(() => {
		if (!focused) return;
		for (const section of PROJECT_SECTIONS) {
			const controller = projectSections[section].controller;
			if (controller) showExpanded(controller, expandedProjectKeys(itemsOf(section)));
		}
	});
	// A "more" row reads its next page when pressed, and once at least half of
	// it is on screen (checked on scroll and on layout, as Live's paging is).
	const readMore = (section: ProjectSection, item: MoreItem) => {
		const controller = projectSections[section].controller;
		if (item.kind === "moreProjects") void controller?.loadMoreProjects();
		else void controller?.loadMoreSessions(item.projectKey, item.tier);
	};
	const readVisibleMore = () => {
		const { offset, height } = viewport.current;
		// Search results fill the scroller in the sections' place, so the rows'
		// last frames say nothing about what's on screen.
		if (searching || height === 0) return;
		for (const { section, items } of shownSections) {
			const top = offsets.current[section];
			if (top === undefined) continue;
			for (const item of items) {
				if (item.kind !== "more" && item.kind !== "moreProjects") continue;
				const frame = moreFrames.current.get(item.key);
				if (!frame) continue;
				const start = top + frame.y;
				const shown = Math.min(start + frame.height, offset + height) - Math.max(start, offset);
				if (shown >= frame.height / 2) readMore(section, item);
			}
		}
	};
	// Search's project hit (spec 7.4): unfold the way to the project, then,
	// once its row and the Projects section have both laid out (in either
	// order), scroll the row 30% of the way down the viewport, as a list's
	// scrollToItem with viewPosition 0.3 would. A reveal starts from search,
	// whose results replace the sections, so leaving search mounts them
	// afresh and both layouts always arrive, even for a project already
	// unfolded; the section's offset from before search could be stale.
	const revealProject = (projectKey: string): boolean => {
		const { view } = projectSections.projects;
		const project = view.projects.find((candidate) => candidate.key === projectKey);
		if (!project) return false;
		const target = projectRevealTarget({
			project,
			pages: view.pages.get(projectKey),
			sources: hostSources,
			organizeBy,
		});
		for (const fold of target.unfold) setFolded(fold, false);
		reveal.current = { sectionTop: null, row: null };
		setRevealKey(target.scrollTo);
		return true;
	};
	const finishReveal = () => {
		const pending = reveal.current;
		if (!pending?.row || pending.sectionTop === null) return;
		reveal.current = null;
		setRevealKey(null);
		const top = pending.sectionTop + pending.row.y;
		// A row taller than the viewport leaves no room to sit it a third of
		// the way down: scroll to its top instead of past it.
		const inset = 0.3 * Math.max(0, viewport.current.height - pending.row.height);
		scrollBoardTo(Math.max(0, top - inset));
	};
	const openProjectResult = (project: NavigationProjectSummary) => {
		// A stale catalog can lose the project between the result's render and
		// the tap: leave search only once the reveal has somewhere to land.
		if (!revealProject(project.key)) return;
		rememberSearch();
		leaveSearch();
	};
	/** A project's change: through the journal, or held when it can't go now
	 * (holdsChange), asked at the press since the journal may have moved
	 * while the menu was up. */
	const actOnProject = (project: NavigationProjectSummary, action: ProjectMenuAction) => {
		const target = { key: project.key, workingDir: project.working_dir };
		const pending = heldProjectState(hold.getSnapshot(), project.key);
		const waits =
			action === "pin" || action === "unpin" ? pending.favorite !== undefined : pending.archived !== undefined;
		const actions = organizationNow.current.actions;
		if (holdsChange(waits, true) || !actions) holdAction({ kind: "project", project: target, action });
		else void projectChange(actions, target, action);
	};
	const projectMenu = (section: ProjectSection, project: NavigationProjectSummary) => {
		// The menu offers what the project will be once what's held goes, so a
		// held Pin to top offers Unpin, which cancels it.
		const pending = heldProjectState(held, project.key);
		const actions = projectMenuActions(
			{
				...project,
				favorite: pending.favorite ?? project.favorite,
				is_archived: pending.archived ?? project.is_archived,
			},
			{ archived: pending.archived ?? section === "archived" },
		);
		return actions.length
			? () => openProjectMenu(project, actions, (action) => actOnProject(project, action))
			: undefined;
	};
	const treeItem = (section: ProjectSection, item: Exclude<ProjectTreeItem, { kind: "session" }>) => {
		if (item.kind === "more" || item.kind === "moreProjects")
			return (
				<View
					testID={item.kind === "more" ? "project-more" : "project-more-projects"}
					onLayout={(event) => {
						moreFrames.current.set(item.key, event.nativeEvent.layout);
						readVisibleMore();
					}}
				>
					<ProjectTreeRow item={item} onPress={() => readMore(section, item)} />
				</View>
			);
		const row = (
			<ProjectTreeRow
				item={item}
				onPress={() => {
					if ("fold" in item) setFolded(item.fold, !item.folded);
				}}
				onLongPress={item.kind === "project" ? projectMenu(section, item.project) : undefined}
				changing={
					item.kind === "project" &&
					(journalHoldsProject(organization, item.project.key) ||
						Object.keys(heldProjectState(held, item.project.key)).length > 0)
				}
			/>
		);
		if (item.key !== revealKey) return row;
		return (
			<View
				key={item.key}
				testID="project-reveal"
				onLayout={(event) => {
					if (!reveal.current) return;
					reveal.current.row = event.nativeEvent.layout;
					finishReveal();
				}}
			>
				{row}
			</View>
		);
	};
	/** One item of the Board's list, moving to its place with the spring. A
	 * row after another row in its section draws a hairline above it. */
	const entry = (item: BoardItem, previous: BoardItem | undefined) => {
		let content: ReactNode;
		let onLayout: ((event: LayoutChangeEvent) => void) | undefined;
		if (item.kind === "band") {
			content = <BandHeader text={`${BAND_HEADERS[item.band]} · ${item.count}`} />;
			onLayout = measure(item.band);
		} else if (item.kind === "idleFold") {
			content = (
				<IdleFold
					count={item.count}
					folded={item.folded}
					unseen={item.unseen}
					onToggle={() => foldIdle(!item.folded)}
				/>
			);
			onLayout = measure("idle");
		} else if (item.kind === "row")
			content = (
				<View style={{ marginLeft: 16 * item.depth }}>
					{item.separated && previous?.kind === "row" ? <Hairline inset={TITLE_INSET} /> : null}
					<BoardListRow
						item={item.item}
						variant={item.variant}
						moving={item.moving}
						archived={item.archived}
						selected={selecting ? chosen.has(item.item.row.ref) : undefined}
						wash={settled.washed.get(item.key) ?? 0}
						context={listContext}
						onSwipeActive={(active) => list.setInteraction(`swipe:${item.key}`, active)}
					/>
				</View>
			);
		else if (item.kind === "pinEmpty") content = <PinnedEmptyHint />;
		else if (item.kind === "tree") content = treeItem(item.section, item.tree);
		else return null;
		return (
			<Animated.View key={item.key} layout={rowMove} onLayout={onLayout}>
				{content}
			</Animated.View>
		);
	};
	const entries = (items: readonly BoardItem[]) => items.map((item, index) => entry(item, items[index - 1]));
	/** A pinned category or a project section, from its header item. */
	const sectionBlock = ([header, ...items]: BoardItem[]) => {
		if (header.kind === "pinHeader") {
			const pin = header.section;
			return (
				<PinnedSection
					key={header.key}
					section={pin}
					folded={header.folded}
					onToggle={() => folds.setFolded(pin.id, !folds.isFolded(pin.id))}
					onMenu={categoryMenu.menuFor(pin)}
					changing={categoryMenu.changing(pin.id)}
					onLayout={measure(`pin:${pin.id}`)}
				>
					{entries(items)}
				</PinnedSection>
			);
		}
		if (header.kind !== "projectHeader") return null;
		const { section: project, folded } = header;
		return (
			<View
				key={header.key}
				testID={`project-section:${project}`}
				onLayout={(event) => {
					measure(project)(event);
					readVisibleMore();
					if (project === "projects" && reveal.current) {
						reveal.current.sectionTop = event.nativeEvent.layout.y;
						finishReveal();
					}
				}}
				style={{ paddingTop: 10 }}
			>
				<ProjectSectionHeader
					title={projectHeaders[project].title}
					label={projectHeaders[project].label}
					folded={folded}
					onToggle={() => setFolded(SECTION_FOLDS[project].fold, !folded)}
					organize={
						project === "projects" && projectGrouping !== "flat" ? { by: organizeBy, onChange: chooseOrganizeBy } : null
					}
				/>
				{entries(items)}
			</View>
		);
	};
	const liveShown = shownGroups.get("live");
	const selection = selectionActions(shownRowItems.filter(({ item }) => chosen.has(item.row.ref)));
	/** Select mode's change to many rows, one at a time through the journal,
	 * reading the Board as it is now. A row whose change can't go now
	 * (holdsChange) is held, and so is every row after one the journal
	 * didn't take or couldn't confirm. Resolves the rows the hub confirmed,
	 * and how many it tried. */
	const changeEach = async (
		rows: readonly NavigationSessionSummary[],
		kind: "archive" | "pin",
		holdOne: (row: NavigationSessionSummary) => void,
		request: (actions: NavigationActions, row: NavigationSessionSummary) => Promise<void>,
	) => {
		const actions = organizationNow.current.actions;
		const sending: NavigationSessionSummary[] = [];
		for (const row of rows) {
			if (!actions || holdsChange(waitsFor(row.ref, kind), true)) holdOne(row);
			else sending.push(row);
		}
		const confirmed = actions ? await confirmEach(actions, sending, request, (rest) => rest.forEach(holdOne)) : [];
		return { confirmed, tried: sending.length };
	};
	const archiveEach = (rows: readonly NavigationSessionSummary[], archived: boolean) =>
		changeEach(rows, "archive", holdArchive(archived), (actions, row) => archiveRequest(actions, row, archived));
	const archiveChosen = async (rows: readonly NavigationSessionSummary[]) => {
		const { confirmed, tried } = await archiveEach(rows, true);
		leaveSelect();
		if (confirmed.length)
			toast.show({
				text: sessionCount("Archived", confirmed.length, tried),
				action: {
					label: "Undo",
					run: () =>
						void archiveEach(confirmed, false).then((undone) => {
							if (undone.confirmed.length)
								toast.show({ text: sessionCount("Unarchived", undone.confirmed.length, undone.tried) });
						}),
				},
			});
	};
	const pinChosen = async (rows: readonly NavigationSessionSummary[], section: PinTarget, name: string) => {
		const { confirmed, tried } = await changeEach(
			rows,
			"pin",
			(row) => void holdAction({ kind: "pin", target: { sessionRef: row.ref, ...section } }),
			(actions, row) => actions.assignPin({ sessionRef: row.ref, ...section }),
		);
		leaveSelect();
		if (confirmed.length) toast.show({ text: `${sessionCount("Pinned", confirmed.length, tried)} to ${name}` });
	};

	let live: ReactNode;
	// Update needed says everything there is to say until something loads.
	if (!snapshot.loaded && fatal) live = null;
	else if (firstReadFailed) live = <FirstReadFailed />;
	else if (!snapshot.loaded) live = <Skeleton />;
	else if (!liveShown) live = <EmptyBoard disabled={!connected} onNewSession={newSession} />;
	else
		live = (
			<>
				{summary ? (
					<SummaryLine summary={summary} connected={connected} perMinute={fleetPerMinute} onJump={jumpToBand} />
				) : null}
				{entries(liveShown)}
			</>
		);

	return (
		<View style={{ flex: 1, backgroundColor: palette.page }}>
			{/* Fixed under the header; their sections aren't there while
			    search results are. On the glass the Board runs under them, and
			    the panel's zIndex raises it over the scroller. */}
			<GlassHeaderPanel
				testID="board-header"
				style={navGlass ? GLASS_PANEL : undefined}
				glassTop={navGlass ? headerHeight : undefined}
				onLayout={(event) => setHeaderPanel({ height: event.nativeEvent.layout.height, onGlass: navGlass })}
			>
				{chips.length && !searching ? <Chips chips={chips} onGlass={navGlass} /> : null}
			</GlassHeaderPanel>
			<View style={{ flex: 1 }}>
				<Animated.ScrollView
					ref={scroller}
					style={{ flex: 1 }}
					// Starts just past the search field: pulling down reveals it
					// (spec 7.3). iOS applies this once, when the scroller mounts.
					contentOffset={{ x: 0, y: searchFieldHeight - underGlass }}
					keyboardShouldPersistTaps="handled"
					keyboardDismissMode="on-drag"
					// iOS keeps that offset only while the content is taller than the
					// viewport, so even a short Board (skeleton, empty, a few rows)
					// is tall enough to keep the field hidden.
					contentContainerStyle={{
						paddingBottom: 24 + boardUnderBar.endPadding,
						minHeight: windowHeight + searchFieldHeight,
					}}
					// The Board runs under its toolbar's glass, its end and its
					// scroll indicator clear of the toolbar.
					contentInset={navGlass ? { ...boardUnderBar.contentInset, top: underGlass } : boardUnderBar.contentInset}
					scrollIndicatorInsets={
						navGlass ? { ...boardUnderBar.scrollIndicatorInsets, top: underGlass } : boardUnderBar.scrollIndicatorInsets
					}
					onScroll={onScroll}
					onLayout={(event) => {
						viewport.current = { ...viewport.current, height: event.nativeEvent.layout.height - underGlass };
						readMoreLiveIfNear();
						readVisibleMore();
					}}
					onContentSizeChange={readMoreLiveIfNear}
					scrollEventThrottle={100}
					{...scrollHandlers}
				>
					<SearchField
						inputRef={searchInput}
						height={searchFieldHeight}
						text={searchText}
						searching={searching}
						onFocus={() => setSearching(true)}
						onChangeText={typeSearch}
						onCancel={cancelSearch}
					/>
					{searching ? (
						<SearchResults
							search={search.snapshot}
							scope={scope}
							onScope={setScope}
							connected={connected}
							recent={recentList}
							onOpen={openSearchResult}
							onRecent={typeSearch}
							onClearRecent={clearRecent}
							projects={projectResults(projectSections.projects.view.projects, search.snapshot.query)}
							onOpenProject={openProjectResult}
						/>
					) : (
						<>
							{fatal ? <NoticeRow text={INCOMPATIBLE_VERSIONS} /> : null}
							<BoardNotices hubId={hubId} notices={hubNotices} navigation={navigation} connected={connected} />
							{continueReading ? (
								<ContinueReadingRow
									trail={continueReading}
									onOpen={(trail) =>
										openDocumentInSession(navigation, {
											hubId,
											sessionRef: trail.sessionRef,
											path: trail.path,
											reference: trail.reference,
											sessionTitle: trail.sessionTitle,
											...(trail.updatedAt === undefined ? {} : { updatedAt: trail.updatedAt }),
										})
									}
								/>
							) : null}
							<View
								testID="live-block"
								onLayout={(event) => {
									const { y, height } = event.nativeEvent.layout;
									offsets.current.live = y;
									liveEnd.current = y + height;
									readMoreLiveIfNear();
									jumpWhenLaidOut.current();
								}}
							>
								{live}
							</View>
							{[...shownGroups.values()].filter((items) => items[0]?.group !== "live").map(sectionBlock)}
						</>
					)}
				</Animated.ScrollView>
				<View
					testID="board-toast"
					pointerEvents="box-none"
					style={{ position: "absolute", left: 0, right: 0, bottom: toolbarHeight + FLOAT_GAP }}
				>
					<Toast toast={toast.toast} dismiss={toast.dismiss} />
				</View>
			</View>
			{/* The toolbar lies over the Board's end, so the Board runs under it. */}
			{selecting ? (
				<SelectBar
					{...toolbarPlacement}
					counts={{
						archive: selection.archive.length,
						// Pin's sheet asks through ActionSheetIOS and Alert.prompt.
						pin: Platform.OS === "ios" ? selection.pin.length : 0,
					}}
					onDone={leaveSelect}
					onArchive={() => void archiveChosen(selection.archive)}
					onPin={() =>
						chooseCategory(board.getSnapshot().pins.rows, toast, (section, name) =>
							pinChosen(selection.pin, section, name),
						)
					}
				/>
			) : (
				<BoardToolbar
					{...toolbarPlacement}
					newSessionDisabled={!connected}
					onNewSession={newSession}
					onSelect={shownRowItems.length && !searching ? () => setSelecting(true) : undefined}
				/>
			)}
		</View>
	);
}

/** A category's name is at most 80 characters, the journal's own bound. */
const CATEGORY_NAME_TOO_LONG = "Category names can be up to 80 characters.";
function categoryNameTooLong(name: string): boolean {
	return Array.from(name).length > 80;
}

/** Rename and Delete for the pinned categories (spec 7.1), through the
 * Board's one organization journal. ⋯ shows only while a change can go out:
 * connected, and no change pending or unresolved. The journal's own error
 * text is never shown; a change on its way dims its category. */
function pinnedCategoryMenu(organization: BoardOrganization, catalog: () => readonly NavigationPinSectionDescriptor[]) {
	const listed = (sectionId: string) => catalog().some((section) => section.id === sectionId);
	const rename = (section: NavigationPinSectionDescriptor) =>
		Alert.prompt(
			"Rename category",
			undefined,
			[
				{ text: "Cancel", style: "cancel" },
				{
					text: "Rename",
					onPress: (value?: string) => {
						const name = (value ?? "").trim();
						if (!name || name === section.name) return;
						if (categoryNameTooLong(name)) {
							Alert.alert(CATEGORY_NAME_TOO_LONG);
							return;
						}
						if (!organizationOpen(organization) || !listed(section.id)) return;
						void organization.actions?.renamePinSection({ sectionId: section.id, name });
					},
				},
			],
			"plain-text",
			section.name,
		);
	const remove = (section: NavigationPinSectionDescriptor) =>
		Alert.alert(`Delete “${section.name}”?`, "Its sessions stay; they're only unpinned.", [
			{ text: "Cancel", style: "cancel" },
			destructiveButton("Delete", () => {
				if (!organizationOpen(organization) || !listed(section.id)) return;
				void organization.actions?.deletePinSection({ sectionId: section.id });
			}),
		]);
	const open = (section: NavigationPinSectionDescriptor) => {
		if (!organizationOpen(organization)) return;
		if (Platform.OS === "ios") {
			ActionSheetIOS.showActionSheetWithOptions(
				{
					title: section.name,
					options: ["Rename", "Delete", "Cancel"],
					destructiveButtonIndex: 1,
					cancelButtonIndex: 2,
				},
				(index) => {
					if (index === 0) rename(section);
					else if (index === 1) remove(section);
				},
			);
			return;
		}
		// Alert.prompt is iOS-only, so Rename stays off other platforms.
		Alert.alert(section.name, undefined, [
			{ text: "Delete", style: "destructive", onPress: () => remove(section) },
			{ text: "Cancel", style: "cancel" },
		]);
	};
	// One gate with the other readers (organizationCheck.journalOperation): a
	// change in flight or one the hub couldn't confirm holds the category, so it
	// stays dimmed until the journal settles.
	const operation = journalOperation(organization.state);
	return {
		menuFor: (section: NavigationPinSectionDescriptor) => (organization.ready ? () => open(section) : null),
		changing: (sectionId: string) =>
			(operation?.kind === "renamePinSection" || operation?.kind === "deletePinSection") &&
			operation.params.sectionId === sectionId,
	};
}

/** Where select mode pins: a category the catalog lists, or a new one by
 * name (the hub makes it for the first session and reuses it after). */
type PinTarget = { sectionId: string } | { sectionName: string };

/** "Archived 2 sessions", or "Archived 2 of 3 sessions" when some weren't. */
function sessionCount(verb: string, done: number, of: number): string {
	return done === of ? `${verb} ${plural(done, "session")}` : `${verb} ${done} of ${plural(of, "session")}`;
}

/** Makes one journaled change per session, in order, through the Board's
 * organization journal, stopping at the first the hub doesn't confirm: an
 * unconfirmed one is the journal's to settle, and `holdRest` gets the rows
 * after it, or that row and the rows after it when the journal didn't take
 * it. Resolves the sessions it confirmed. */
async function confirmEach(
	actions: NavigationActions,
	rows: readonly NavigationSessionSummary[],
	request: (actions: NavigationActions, row: NavigationSessionSummary) => Promise<void>,
	holdRest: (rows: readonly NavigationSessionSummary[]) => void,
): Promise<NavigationSessionSummary[]> {
	const confirmed: NavigationSessionSummary[] = [];
	for (const [index, row] of rows.entries()) {
		const outcome = await journalOutcome(actions, () => request(actions, row));
		if (outcome === "confirmed") {
			confirmed.push(row);
			continue;
		}
		if (outcome === "unconfirmed") void actions.reconcile();
		holdRest(rows.slice(outcome === "unconfirmed" ? index + 1 : index));
		break;
	}
	return confirmed;
}

/** Select mode's Pin (ruling 18): the pin catalog's categories, then a new
 * one, as an action sheet. A new category's name is 1-80 characters, the
 * journal's own bound. */
function chooseCategory(
	categories: readonly NavigationPinSectionDescriptor[],
	toast: Pick<ToastController, "show">,
	pin: (section: PinTarget, name: string) => void,
) {
	const createCategory = () =>
		Alert.prompt(
			"New category",
			undefined,
			[
				{ text: "Cancel", style: "cancel" },
				{
					text: "Create",
					onPress: (value?: string) => {
						const name = (value ?? "").trim();
						if (!name) return;
						if (categoryNameTooLong(name)) {
							toast.show({ text: CATEGORY_NAME_TOO_LONG });
							return;
						}
						pin({ sectionName: name }, name);
					},
				},
			],
			"plain-text",
		);
	ActionSheetIOS.showActionSheetWithOptions(
		{
			title: "Pin to category",
			options: [...categories.map((category) => category.name), "New category…", "Cancel"],
			cancelButtonIndex: categories.length + 1,
		},
		(index) => {
			const category = categories[index];
			if (category) pin({ sectionId: category.id }, category.name);
			else if (index === categories.length) createCategory();
		},
	);
}

/** A row's menu actions on this phone: Rename asks through Alert.prompt,
 * which only iOS has. */
function menuActionsHere(item: ClassifiedRow, context: RowActionContext): RowAction[] {
	const actions = rowMenuActions(item, context);
	return Platform.OS === "ios" ? actions : actions.filter((action) => action !== "rename");
}

/** Shut down from the row menu: asked first, online or offline, then
 * `shutDown` runs (the Session's own request, or a hold). */
function confirmShutDown(row: NavigationSessionSummary, shutDown: () => void) {
	Alert.alert(`Shut down “${row.title}”?`, "The agent stops. Send it a message to resume it.", [
		{ text: "Cancel", style: "cancel" },
		destructiveButton("Shut down", shutDown),
	]);
}

/** Rename from the row menu (iOS only: Alert.prompt), starting from the
 * row's title; `rename` gets the name typed. */
function promptRename(row: NavigationSessionSummary, rename: (name: string) => void) {
	Alert.prompt(
		"Rename session",
		undefined,
		[
			{ text: "Cancel", style: "cancel" },
			{ text: "Rename", onPress: (name?: string) => rename(name ?? "") },
		],
		"plain-text",
		row.title,
	);
}

/** A project row's long-press menu (ruling 15): Pin to top or Unpin, and
 * Archive or Unarchive, as an action sheet, or an alert off iOS. `act`
 * decides at the press whether the change is held, goes through the
 * journal, or can't go, since the connection or the journal may have moved
 * while the menu was up. */
function openProjectMenu(
	project: NavigationProjectSummary,
	actions: readonly ProjectMenuAction[],
	act: (action: ProjectMenuAction) => void,
) {
	const title = projectName(project);
	if (Platform.OS === "ios") {
		ActionSheetIOS.showActionSheetWithOptions(
			{
				title,
				options: [...actions.map((action) => PROJECT_MENU_LABELS[action]), "Cancel"],
				cancelButtonIndex: actions.length,
			},
			(index) => {
				const action = actions[index];
				if (action) act(action);
			},
		);
		return;
	}
	Alert.alert(title, undefined, [
		...actions.map((action) => ({ text: PROJECT_MENU_LABELS[action], onPress: () => act(action) })),
		{ text: "Cancel", style: "cancel" },
	]);
}

/** The Board's search controller, bound to the ready client or none. */
function useSearch(client: ConversationClientLike | null) {
	const [controller] = useState(createSearchController);
	useEffect(() => () => controller.dispose(), [controller]);
	useEffect(() => controller.setClient(client), [controller, client]);
	const snapshot = useSyncExternalStore(controller.subscribe, controller.getSnapshot);
	return { controller, snapshot };
}

/** The chips' panel on the glass: over the Board's top, which runs under it. */
const GLASS_PANEL = { position: "absolute", top: 0, left: 0, right: 0, zIndex: 1 } as const;

/** The search field's row: an 8pt margin around a field that grows with the
 * text size, never shorter than the 44pt touch target. */
const searchFieldHeightAt = (scale: number) => Math.max(44, 16 + Math.round(36 * scale));

/** Board search's field (spec 7.4), first in the Board's scroller. Cancel
 * shows while you search. */
function SearchField({
	inputRef,
	height,
	text,
	searching,
	onFocus,
	onChangeText,
	onCancel,
}: {
	inputRef: RefObject<TextInput | null>;
	height: number;
	text: string;
	searching: boolean;
	onFocus: () => void;
	onChangeText: (text: string) => void;
	onCancel: () => void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	// The field draws the stock iOS 36pt, but must offer a 44pt touch target:
	// the input's own row carries the target height, and the 36pt pill is a
	// background behind it, so the touch area is not clipped to the shorter
	// pill (React Native clips hitSlop to the parent's bounds).
	const pill = Math.round(36 * scale);
	const target = Math.max(44, pill);
	return (
		<View
			testID="search-field"
			style={{
				height,
				paddingHorizontal: 16,
				paddingVertical: (height - target) / 2,
				flexDirection: "row",
				alignItems: "center",
				columnGap: 12,
			}}
		>
			<View
				style={{
					flex: 1,
					height: target,
					flexDirection: "row",
					alignItems: "center",
					columnGap: 6,
					paddingHorizontal: 8,
				}}
			>
				<View
					style={{
						position: "absolute",
						left: 0,
						right: 0,
						top: (target - pill) / 2,
						height: pill,
						borderRadius: 10,
						backgroundColor: palette.inset,
					}}
				/>
				<SymbolView name="magnifyingglass" size={15 * scale} tintColor={palette.inkLow} />
				<TextInput
					ref={inputRef}
					accessibilityLabel="Search sessions"
					placeholder="Search sessions"
					placeholderTextColor={palette.inkLow}
					value={text}
					onFocus={onFocus}
					onChangeText={onChangeText}
					returnKeyType="search"
					autoCapitalize="none"
					autoCorrect={false}
					clearButtonMode="while-editing"
					allowFontScaling={allowFontScaling}
					style={{ flex: 1, alignSelf: "stretch", fontSize: 17 * scale, color: palette.inkHi }}
				/>
			</View>
			{searching ? (
				<Pressable
					accessibilityRole="button"
					accessibilityLabel="Cancel search"
					onPress={onCancel}
					style={({ pressed }) => ({ minHeight: 44, justifyContent: "center", opacity: pressed ? 0.6 : 1 })}
				>
					<Text allowFontScaling={allowFontScaling} style={{ fontSize: 17 * scale, color: palette.accentInk }}>
						Cancel
					</Text>
				</Pressable>
			) : null}
		</View>
	);
}

type ShownRow = { item: ClassifiedRow; archived: boolean };

/** A shown row's identity: its ref and the tier it sits in. A session shown
 * in both Live and a project's Archived tier is two different shown rows, so
 * the row menu opened from each reads its own copy and offers the right
 * action (Archive or Unarchive) instead of always the Live copy's. */
function shownRowKey(ref: string, archived: boolean): string {
	return `${ref}:${archived}`;
}

/** The Board's shown rows by ref and tier: each ref keeps its first
 * unarchived copy and its first archived copy, in screen order. The map
 * keeps its identity while no row, state or tier changes, so the row menu's
 * host (and an open menu) changes only when one does. */
function useShownRows(rows: readonly ShownRow[]): ReadonlyMap<string, ShownRow> {
	const byKey = new Map<string, ShownRow>();
	for (const shown of rows) {
		const key = shownRowKey(shown.item.row.ref, shown.archived);
		if (!byKey.has(key)) byKey.set(key, shown);
	}
	const kept = useRef(byKey);
	if (!sameShownRows(kept.current, byKey)) kept.current = byKey;
	return kept.current;
}

function sameShownRows(before: ReadonlyMap<string, ShownRow>, after: ReadonlyMap<string, ShownRow>): boolean {
	if (before.size !== after.size) return false;
	for (const [key, shown] of after) {
		const was = before.get(key);
		if (!was || was.item.row !== shown.item.row || was.item.state !== shown.item.state) return false;
	}
	return true;
}

/** Every session row a project section's view holds, across its projects'
 * tiers. */
function projectSessionRows(view: ProjectsView): NavigationSessionSummary[] {
	return [...view.pages.values()].flatMap((pages) => Object.values(pages).flatMap((page) => page.rows));
}

/** The drafts saved on this device for a hub's sessions, or none when the
 * draft store can't be read: a missing Draft tag is the only cost. */
function readDraftRefs(hubId: string): Set<string> {
	try {
		return drafts.refsWithDrafts(hubId);
	} catch {
		return new Set();
	}
}

/** First run on a device (spec 13.1): adopt the newest updated_at as the
 * seen epoch, but only from fresh, complete reads. Live is sorted by
 * attention, not time, so a newer row can sit on a later page: until the
 * epoch is adopted, keep reading Live's pages. Until then isSeen counts
 * every row as seen, so no row flashes a blue dot. */
function useFirstRun(board: BoardController, markers: SeenMarkers, snapshot: BoardSnapshot, focused: boolean) {
	useEffect(() => {
		// Out of view the Board is paused; coming back re-runs this and picks
		// the paging up where it stopped.
		if (!focused || markers.adopted) return;
		const { loaded, retained, live, needsYou } = snapshot;
		if (!loaded || retained || live.loading || live.stale) return;
		if (live.remaining > 0) {
			// A failed page waits for the next change rather than retrying hot.
			if (!live.error) void board.loadMoreLive();
			return;
		}
		// The controller pages Needs you to the end on its own.
		if (!needsYou.loaded || needsYou.loading || needsYou.stale || needsYou.remaining > 0) return;
		markers.adoptEpoch([...live.rows, ...needsYou.rows]);
	}, [board, markers, snapshot, focused]);
}

/** The hub's seen marks (S4): marks go out whenever the connection is ready,
 * which resends any a dropped connection lost and sends those made while
 * offline, and each pending mark is pruned once a row the Board has loaded,
 * from any section, shows it landed. */
function useHubSeenMarks(
	hubMarks: HubSeenMarks,
	client: ConversationClientLike | null,
	loadedRows: readonly NavigationSessionSummary[],
) {
	useEffect(() => {
		hubMarks.flush(client);
	}, [hubMarks, client]);
	useEffect(() => {
		hubMarks.prune(loadedRows);
	}, [hubMarks, loadedRows]);
}

function useHeader(
	navigation: Navigation,
	hubId: string,
	hubName: string,
	revealSearch: () => void,
	glass: boolean,
	page: string,
) {
	const { fontScale } = useWindowDimensions();
	useEffect(() => {
		const hubButton = (
			<HubButton
				hubName={hubName}
				onOpen={() => navigation.navigate("Hub", { screen: "HubHome", params: { hubId } })}
			/>
		);
		navigation.setOptions({
			...navBarGlassOptions(glass, page),
			title: "",
			unstable_headerLeftItems: () => [{ type: "custom", element: hubButton }],
			unstable_headerRightItems: () => [
				{
					type: "button",
					label: "Search",
					accessibilityLabel: "Search sessions",
					icon: { type: "sfSymbol", name: "magnifyingglass" },
					onPress: revealSearch,
				},
			],
			headerLeft: () => hubButton,
			headerRight: () => (
				<Button
					text
					label={fontScale > 1.4 ? "Find" : "Search"}
					accessibilityLabel="Search sessions"
					onPress={revealSearch}
				/>
			),
		});
	}, [navigation, hubId, hubName, revealSearch, fontScale, glass, page]);
}

/** The hub button (spec 7.1): the hub's name and a chevron as one control,
 * opening the Hub sheet (spec 12). A native bar item given both a label and an
 * icon draws only the icon, so this is a custom header view. The Hub opens
 * while the hub is out of reach too: it keeps its last data and says why its
 * controls wait. Its text follows Dynamic Type up to xxxLarge only, since the
 * bar's height is fixed, so a long press shows the whole name in the Large
 * Content Viewer, as Apple asks of text that stops growing. */
function HubButton({ hubName, onOpen }: { hubName: string; onOpen: () => void }) {
	const { palette } = useColors();
	const scale = useHeaderTextScale();
	const { width } = useWindowDimensions();
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={hubName}
			accessibilityHint="Opens the Hub"
			accessibilityShowsLargeContentViewer
			accessibilityLargeContentTitle={hubName}
			onPress={onOpen}
			style={{
				// A custom header view sizes itself, so a long hub name needs a
				// cap to truncate against instead of growing into Search.
				maxWidth: Math.round(width * 0.6),
				minHeight: 44,
				paddingHorizontal: 12,
				flexDirection: "row",
				alignItems: "center",
				columnGap: 6,
			}}
		>
			<Text
				allowFontScaling={allowFontScaling}
				numberOfLines={1}
				style={{ flexShrink: 1, fontSize: 17 * scale, color: palette.inkHi }}
			>
				{hubName}
			</Text>
			<SymbolView name="chevron.down" size={13 * scale} tintColor={palette.inkHi} />
		</Pressable>
	);
}

interface ChipProps {
	key: string;
	name: string;
	count: number;
	badge?: number;
	pinned?: boolean;
	label: string;
	onPress: () => void;
}

/** The section chips, fixed under the header (spec 7.1), in the shared chip
 * strip that owns the row's fill and its overflow fade. */
function Chips({ chips, onGlass }: { chips: ChipProps[]; onGlass: boolean }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<ChipStrip testID="chips" onGlass={onGlass}>
			{chips.map((chip) => (
				<Pressable
					key={chip.key}
					testID="chip"
					accessibilityRole="button"
					accessibilityLabel={chip.label}
					onPress={() => {
						haptic("selection");
						chip.onPress();
					}}
					// The chip draws 32pt tall; hit slop into the row's 8pt padding makes a 44pt target.
					hitSlop={{ top: 6, bottom: 6 }}
					style={({ pressed }) => ({
						minHeight: 32,
						paddingHorizontal: 12,
						borderRadius: 16,
						flexDirection: "row",
						alignItems: "center",
						columnGap: 6,
						backgroundColor: pressed ? palette.pressed : palette.surface,
						borderWidth: 0.5,
						borderColor: palette.edge,
					})}
				>
					{chip.pinned ? <SymbolView name="pin.fill" size={12 * scale} tintColor={palette.inkLow} /> : null}
					<Text
						allowFontScaling={allowFontScaling}
						style={{ fontSize: 14 * scale, fontWeight: "600", color: palette.inkHi }}
					>
						{chip.name}
					</Text>
					<Text
						allowFontScaling={allowFontScaling}
						style={{ fontSize: 14 * scale, fontWeight: "500", color: palette.inkLow, fontVariant: ["tabular-nums"] }}
					>
						{String(chip.count)}
					</Text>
					{chip.badge ? (
						<View
							style={{
								minWidth: 18 * scale,
								height: 18 * scale,
								paddingHorizontal: 5,
								borderRadius: 9 * scale,
								alignItems: "center",
								justifyContent: "center",
								backgroundColor: palette.attention,
							}}
						>
							<Text
								allowFontScaling={allowFontScaling}
								style={{
									fontSize: 11 * scale,
									fontWeight: "700",
									fontVariant: ["tabular-nums"],
									// Dark ink on amber in both themes: the light theme's ink,
									// the dark theme's page.
									color: palette.scheme === "dark" ? palette.page : palette.inkHi,
								}}
							>
								{String(chip.badge)}
							</Text>
						</View>
					) : null}
				</Pressable>
			))}
		</ChipStrip>
	);
}

/** The Live summary line (spec 7.1): each count jumps to its band. */
function SummaryLine({
	summary,
	connected,
	perMinute,
	onJump,
}: {
	summary: LiveSummary;
	connected: boolean;
	/** The fleet meter's per-minute counts; absent, it shows its still fallback. */
	perMinute?: readonly number[];
	onJump: (band: Band) => void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const entries = (["needsYou", "working", "idle"] as const).filter((band) => summary[band] > 0);
	return (
		<View
			testID="live-summary"
			style={{
				flexDirection: "row",
				flexWrap: "wrap",
				alignItems: "center",
				// Wrapped rows sit apart by both counts' slop, so a tap near the
				// wrap lands on the count it's over.
				rowGap: 14,
				paddingTop: 12,
				paddingHorizontal: 16,
			}}
		>
			{entries.map((band, index) => (
				// Each count carries the separator after it, so the line wraps
				// after a dot and no wrapped line starts with one.
				<View key={band} style={{ flexDirection: "row", alignItems: "center" }}>
					<Pressable
						accessibilityRole="button"
						onPress={() => {
							haptic("selection");
							onJump(band);
						}}
						// Each count draws 30pt tall; the slop makes a 44pt target.
						hitSlop={{ top: 7, bottom: 7 }}
						style={({ pressed }) => ({
							minHeight: 30,
							flexDirection: "row",
							alignItems: "center",
							columnGap: 4,
							opacity: pressed ? 0.6 : 1,
						})}
					>
						{band === "working" ? <PulseMeter tone={connected ? "alive" : "gray"} perMinute={perMinute} /> : null}
						<Text
							allowFontScaling={allowFontScaling}
							style={{
								...scaledType(uiType.statusLine, scale),
								fontWeight: band === "needsYou" ? "600" : "400",
								color: band === "needsYou" ? palette.attentionInk : palette.inkMid,
							}}
						>
							{summaryText(band, summary[band])}
						</Text>
					</Pressable>
					{index < entries.length - 1 ? (
						<Text allowFontScaling={allowFontScaling} style={{ fontSize: 14 * scale, color: palette.inkLow }}>
							{" · "}
						</Text>
					) : null}
				</View>
			))}
		</View>
	);
}

/** Idle's header, folded by default; its state persists per device. Folded,
 * it shows the blue dot while any session inside has updates you haven't
 * opened: no count, no words. */
function IdleFold({
	count,
	folded,
	unseen,
	onToggle,
}: {
	count: number;
	folded: boolean;
	unseen: boolean;
	onToggle: () => void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const dot = folded && unseen;
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={`Idle, ${plural(count, "session")}${dot ? ", unread sessions inside" : ""}`}
			accessibilityState={{ expanded: !folded }}
			onPress={onToggle}
			style={({ pressed }) => ({
				minHeight: 48,
				paddingHorizontal: 16,
				flexDirection: "row",
				alignItems: "center",
				justifyContent: "space-between",
				backgroundColor: pressed ? palette.pressed : palette.page,
			})}
		>
			<Text
				testID="band-header"
				allowFontScaling={allowFontScaling}
				style={{ fontSize: 15 * scale, color: palette.inkMid }}
			>
				{`Idle · ${count}`}
			</Text>
			<View style={{ flexDirection: "row", alignItems: "center", columnGap: 8 }}>
				{dot ? <FreshDot /> : null}
				<FoldChevron folded={folded} />
			</View>
		</Pressable>
	);
}

/** The very first load: three still placeholder rows. */
function Skeleton() {
	const { palette } = useColors();
	return (
		<View style={{ paddingTop: 12, paddingHorizontal: 16, rowGap: 8 }} accessibilityLabel="Loading sessions">
			{["first", "second", "third"].map((key) => (
				<View
					key={key}
					testID="skeleton-row"
					style={{ height: 64, borderRadius: 10, backgroundColor: palette.inset }}
				/>
			))}
		</View>
	);
}

function FirstReadFailed() {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View style={{ paddingHorizontal: 16, paddingVertical: 32 }}>
			<Text
				allowFontScaling={allowFontScaling}
				style={{ fontSize: 17 * scale, lineHeight: 22 * scale, color: palette.inkMid, textAlign: "center" }}
			>
				Couldn't load this hub's sessions. Trying again shortly.
			</Text>
		</View>
	);
}

function EmptyBoard({ disabled, onNewSession }: { disabled: boolean; onNewSession: () => void }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View style={{ paddingHorizontal: 16, paddingVertical: 32, alignItems: "center", rowGap: 16 }}>
			<Text
				allowFontScaling={allowFontScaling}
				style={{ fontSize: 17 * scale, lineHeight: 22 * scale, color: palette.inkMid, textAlign: "center" }}
			>
				Nothing's running. Start a session to put an agent to work.
			</Text>
			<Button primary compact label="New session" disabled={disabled} onPress={onNewSession} />
		</View>
	);
}
