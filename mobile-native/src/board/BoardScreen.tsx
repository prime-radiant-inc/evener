import type {
	NavigationPinSectionDescriptor,
	NavigationProjectSummary,
	NavigationSessionSummary,
	SearchResult,
} from "@evener/appwire-client";
import { useFocusEffect, useIsFocused } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { SymbolView } from "expo-symbols";
import { type ReactNode, type RefObject, useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import {
	ActionSheetIOS,
	Alert,
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
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { useConnection } from "../ConnectionProvider";
import { reconnectDelay } from "../hubConnection";
import { getNativeMutationRuntime } from "../nativeMutationRuntime";
import { drafts } from "../nativeDrafts";
import type { Routes } from "../screens";
import { sheetKey, useProvideSheetHost } from "../sheet/sheetHosts";
import { Toast, type ToastController, useToast } from "../Toast";
import { Action, useColors, useTextScale } from "../ui";
import {
	type Band,
	boardState,
	type ClassifiedRow,
	type LiveSummary,
	liveBands,
	liveSummary,
	plural,
	rowClassifier,
	sectionLabel,
	summaryText,
	usualPlace,
} from "./attention";
import type { OrganizeBy, SeenMarkers } from "./boardMemory";
import { BoardNotices, NoticeRow } from "./BoardNotices";
import { BandHeader, FoldChevron } from "./BoardRow";
import { BoardRows, type RowContext } from "./BoardRows";
import { BoardToolbar } from "./BoardToolbar";
import { type BoardController, type BoardSnapshot, createBoardController } from "./boardData";
import { createSearchController, type SearchScope } from "./boardSearch";
import { BoardStops, stopToast } from "./boardStops";
import { BoardSeen, type HubSeenMarks, hubSeenMarks } from "./hubSeen";
import { foldedSections, organizeByPreference, recentSearches, seenMarkers } from "./nativeBoardMemory";
import { notices } from "./notices";
import { PinnedSection, useBoardFolds, useCategoryFolds } from "./PinnedSections";
import { journalHoldsProject, PROJECT_MENU_LABELS, type ProjectMenuAction, projectMenuActions } from "./projectMenu";
import {
	expandedProjectKeys,
	grouping,
	liveCountsByHost,
	type ProjectSection,
	type ProjectsView,
	type ProjectTreeItem,
	projectTreeItems,
	SECTION_FOLDS,
} from "./projectTree";
import { projectName, ProjectSectionHeader, ProjectTreeRow } from "./ProjectTreeRow";
import { PulseMeter } from "./PulseMeter";
import {
	archivingSessionId,
	type RowAction,
	type RowActionContext,
	renameSession,
	rowMenuActions,
	shutDownSession,
} from "./rowActions";
import { type RowMenuHost, rowMenuHosts } from "./RowMenu";
import { archiveRow, rowSwipes, type SwipeRowAction } from "./rowSwipes";
import { SearchResults } from "./SearchResults";
import { type BoardOrganization, organizationOpen, useBoardOrganization } from "./useBoardOrganization";
import { PROJECT_SECTIONS, showExpanded, useProjectSections } from "./useProjectSections";

type Props = NativeStackScreenProps<Routes, "Sessions">;
type Navigation = Props["navigation"];

const MINUTE = 60_000;
const INCOMPATIBLE =
	"This app and the hub need compatible versions. Update the app from TestFlight, or update Evener on the hub.";
const BAND_HEADERS: Record<Exclude<Band, "idle">, string> = {
	needsYou: "NEEDS YOU",
	finished: "FINISHED",
	working: "WORKING",
};
/** A row that reads a section's next page: a tier's sessions or the catalog's projects. */
type MoreItem = Extract<ProjectTreeItem, { kind: "more" | "moreProjects" }>;

/** Home (spec 7.1): every live session ordered by who needs you, then the
 * user's pinned categories, projects and archive. */
export function BoardScreen({ navigation }: Props) {
	const { activeProfile } = useConnection();
	const { palette } = useColors();
	if (!activeProfile) return <View style={{ flex: 1, backgroundColor: palette.page }} />;
	return <Board key={activeProfile.id} hubId={activeProfile.id} hubName={activeProfile.name} navigation={navigation} />;
}

function Board({ hubId, hubName, navigation }: { hubId: string; hubName: string; navigation: Navigation }) {
	const { client, state, fatal, activeProfile } = useConnection();
	const { palette } = useColors();
	const connected = state === "ready";
	const focused = useIsFocused();
	const [board] = useState(createBoardController);
	useEffect(() => () => board.dispose(), [board]);
	const snapshot = useSyncExternalStore(board.subscribe, board.getSnapshot);
	const markers = seenMarkers(hubId);
	const seenRevision = useSyncExternalStore(markers.subscribe, markers.getRevision);
	const hubMarks = hubSeenMarks(hubId);
	const hubSeenRevision = useSyncExternalStore(hubMarks.subscribe, hubMarks.getRevision);
	const seen = useMemo(() => new BoardSeen(markers, hubMarks), [markers, hubMarks]);
	const [now, setNow] = useState(Date.now);
	const [draftRefs, setDraftRefs] = useState<Set<string>>(() => new Set());

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
	useReadRetry(board, connected && focused ? client : null, snapshot);

	const bands = useMemo(
		() => liveBands(snapshot.live.rows, snapshot.needsYou.rows, (row) => seen.isSeen(row)),
		// The revisions re-run isSeen after a mark, a pruned mark or first run.
		[snapshot.live.rows, snapshot.needsYou.rows, seen, seenRevision, hubSeenRevision],
	);
	useFirstRun(board, markers, snapshot, focused);

	const usual = useMemo(() => usualPlace(snapshot.live.rows), [snapshot.live.rows]);
	const sources = snapshot.manifest?.sources;
	const hostLabel = useMemo(() => {
		const labels = new Map((sources ?? []).map((source) => [source.id, source.label]));
		return (hostId: string) => labels.get(hostId) ?? hostId;
	}, [sources]);

	const classify = useMemo(
		() => rowClassifier(snapshot.needsYou.rows, (row) => seen.isSeen(row)),
		[snapshot.needsYou.rows, seen, seenRevision, hubSeenRevision],
	);
	// A project section's session row: its approval comes from the row's own
	// flag alone, not from the needs_you section's membership.
	const projectRow = (row: NavigationSessionSummary): ClassifiedRow => ({
		row,
		state: boardState(row, false, seen.isSeen(row)),
	});
	const folds = useCategoryFolds(hubId);
	const organization = useBoardOrganization(hubId);
	const toast = useToast();
	// Stop from the Board (ruling 17), one per hub. A new client lets go of
	// every Stop the old one was delivering; an interrupt not yet sent stays
	// in the outbox, delivered as any durable Stop is.
	const [stops] = useState(() => new BoardStops(getNativeMutationRuntime, hubId));
	useEffect(() => () => stops.dispose(), [stops]);
	useEffect(() => () => stops.releaseAll(), [stops, client]);
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
	useHubSeenMarks(hubMarks, connected ? client : null, loadedRows);
	const hubNotices = useMemo(
		() => notices({ auth: snapshot.auth, sources: sources ?? [], plugins: snapshot.plugins, loadedRows }),
		[snapshot.auth, sources, snapshot.plugins, loadedRows],
	);

	const [idleFolded, setIdleFolded] = useState(() => foldedSections(hubId).isFolded("idle", true));
	const foldIdle = (folded: boolean) => {
		foldedSections(hubId).setFolded("idle", folded);
		setIdleFolded(folded);
	};

	const scroller = useRef<ScrollView>(null);
	const search = useSearch(connected ? client : null);
	const searchInput = useRef<TextInput>(null);
	const [searchText, setSearchText] = useState("");
	// Searching from the moment the field takes focus until Cancel.
	const [searching, setSearching] = useState(false);
	const [scope, setScope] = useState<SearchScope>("all");
	const searchFieldHeight = searchFieldHeightAt(useTextScale());
	const { height: windowHeight } = useWindowDimensions();
	const recent = recentSearches(hubId);
	const [recentList, setRecentList] = useState(() => recent.list());
	const typeSearch = (text: string) => {
		setSearchText(text);
		search.controller.setQuery(text);
	};
	const cancelSearch = () => {
		typeSearch("");
		setSearching(false);
		searchInput.current?.blur?.();
		// Tuck the field back out of view, where the Board keeps it.
		scroller.current?.scrollTo?.({ y: searchFieldHeight, animated: true });
	};
	// The field sits above the Board, scrolled out of view, so Search brings
	// it down (spec 7.4). A test renderer's host views have no instances.
	const revealSearch = useCallback(() => {
		scroller.current?.scrollTo?.({ y: 0, animated: true });
		searchInput.current?.focus?.();
	}, []);
	useHeader(navigation, hubId, hubName, connected, revealSearch);

	const newSession = () => navigation.navigate("NewSession", { hubId, hubName });
	const openSession = (row: NavigationSessionSummary) => {
		seen.markRead(connected ? client : null, [row]);
		navigation.navigate("Conversation", { hubId, ref: row.ref, title: row.title });
	};
	// A search result the Board has loaded opens like its row, so it's
	// marked seen the same way. Any other session marks itself seen when its
	// screen loads (useMarkSeenInFront).
	const openSearchResult = (result: SearchResult) => {
		recent.add(search.snapshot.query);
		setRecentList(recent.list());
		const row = loadedRows.find((loaded) => loaded.ref === result.ref);
		if (row) openSession(row);
		else navigation.navigate("Conversation", { hubId, ref: result.ref, title: result.title });
	};
	const clearRecent = () => {
		recent.clear();
		setRecentList(recent.list());
	};

	// Where each section starts in the scroller, for the chips and the
	// summary line to jump to. Bands measure inside the Live block.
	const offsets = useRef<Record<string, number>>({});
	const liveEnd = useRef<number | null>(null);
	const measure = (key: string) => (event: LayoutChangeEvent) => {
		offsets.current[key] = event.nativeEvent.layout.y;
	};
	const scrollTo = (key: string, withinLive = false) => {
		const y = (withinLive ? (offsets.current.live ?? 0) : 0) + (offsets.current[key] ?? 0);
		// A test renderer's host ScrollView has no instance to scroll.
		scroller.current?.scrollTo?.({ y, animated: true });
	};
	const jumpToBand = (band: Band) => {
		if (band === "idle") foldIdle(false);
		scrollTo(band, true);
	};
	// Within about a screen of the end of Live, read its next page. Layout
	// checks too, so a first page too short to scroll keeps reading.
	const viewport = useRef({ offset: 0, height: 0 });
	// Where each of the project sections' "more" rows (a tier's sessions, or
	// the catalog's projects) was last laid out inside its section.
	const moreFrames = useRef(new Map<string, { y: number; height: number }>());
	const readMoreLiveIfNear = () => {
		const page = board.getSnapshot().live;
		// Search results fill the scroller in Live's place.
		if (searching || liveEnd.current === null || page.remaining === 0 || page.loading || page.stale || page.error) return;
		const { offset, height } = viewport.current;
		if (offset + 2 * height >= liveEnd.current) void board.loadMoreLive();
	};
	const onScroll = (event: NativeSyntheticEvent<NativeScrollEvent>) => {
		const { contentOffset, layoutMeasurement } = event.nativeEvent;
		viewport.current = { offset: contentOffset.y, height: layoutMeasurement.height };
		readMoreLiveIfNear();
		readVisibleMore();
	};

	const manifest = snapshot.manifest;
	const liveTotal = bands.needsYou.length + bands.finished.length + bands.working.length + bands.idle.length;
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

	// What a row's actions may do now (rulings 16 and 21), by whether it sits
	// in an archived tier.
	const actionsConnected = connected && activeProfile?.id === hubId;
	const rowContext = (archived: boolean): RowActionContext => ({
		connected: actionsConnected,
		organizationReady: organization.ready,
		archived,
	});
	// The row menu, a sheet route that asks the Board's host below for the
	// row and its actions. It carries the tier it opened from, since a
	// session can show twice (Live and a project's Archived tier) and the two
	// copies offer different actions (Archive vs. Unarchive).
	const openRowMenu = (item: ClassifiedRow, archived: boolean) =>
		navigation.navigate("RowMenuSheet", { hubId, ref: item.row.ref, archived });
	// Rename asks through Alert.prompt, which only iOS has.
	const menuActions = (item: ClassifiedRow, archived: boolean) => {
		const actions = rowMenuActions(item, rowContext(archived));
		return Platform.OS === "ios" ? actions : actions.filter((action) => action !== "rename");
	};
	// `archived` only matters for "more" (it opens the sheet on the tier the
	// swipe came from); actOnRow's call never sends "more", so it's fine left
	// at its default there.
	const runRowAction = (item: ClassifiedRow, action: SwipeRowAction, archived = false) => {
		const { row } = item;
		if (action === "more") openRowMenu(item, archived);
		else if (action === "pin") navigation.navigate("PinAssignment", { hubId, ref: row.ref, title: row.title });
		else if (action === "stop" && client)
			void stops.stop(client, row.ref).then((outcome) => toast.show({ text: stopToast(outcome, row.title) }));
		else if (action === "archive" || action === "unarchive")
			void archiveRow(organization, row, action === "archive", toast);
	};
	/** The menu's actions: Pin, Stop, Archive and Unarchive as the swipes do
	 * them, the read marks on this phone, and Shut down and Rename as the
	 * Session sends them (rulings 18-21). */
	const actOnRow = (item: ClassifiedRow, action: RowAction) => {
		const { row } = item;
		const markClient = connected ? client : null;
		if (action === "markRead") seen.markRead(markClient, [row]);
		else if (action === "markUnread") seen.markUnread(markClient, [row]);
		else if (action === "shutDown") confirmShutDown(client, row, toast);
		else if (action === "rename") promptRename(client, row, toast);
		else runRowAction(item, action);
	};
	const archivingId = archivingSessionId(organization.state);
	const listContext: RowContext = {
		connected,
		usual,
		hostLabel,
		now,
		onOpen: openSession,
		draftRefs,
		swipes: (item, archived) =>
			rowSwipes(item, rowContext(archived), archivingId, (action) => runRowAction(item, action, archived)),
		menu: (item, archived) => ({
			actions: menuActions(item, archived),
			onOpenSession: () => openSession(item.row),
			onAction: (action) => actOnRow(item, action),
			onOpenSheet: () => openRowMenu(item, archived),
		}),
	};
	const rows = (items: ClassifiedRow[], variant: "signal" | "quiet", moving: boolean, archived = false) => (
		<BoardRows items={items} variant={variant} moving={moving} archived={archived} context={listContext} />
	);
	const band = (key: Exclude<Band, "idle">, moving: boolean) =>
		bands[key].length ? (
			<View key={key} onLayout={measure(key)}>
				<BandHeader text={`${BAND_HEADERS[key]} · ${bands[key].length}`} />
				{rows(bands[key], "signal", moving)}
			</View>
		) : null;
	const summary = liveSummary(bands);

	// Projects (or Hosts), Test runs and Archived, after the pinned categories.
	const hostSources = sources ?? [];
	const projectGrouping = grouping(hostSources, organizeBy);
	const chooseOrganizeBy = (next: OrganizeBy) => {
		organizeByPreference(hubId).set(next);
		setOrganizeBy(next);
	};
	// A host's live count needs every Live and Needs you row (ruling 12).
	const hostLiveCount = liveCountsByHost(
		[...snapshot.live.rows, ...snapshot.needsYou.rows],
		snapshot.live.loaded &&
			snapshot.needsYou.loaded &&
			snapshot.live.remaining === 0 &&
			snapshot.needsYou.remaining === 0,
	);
	const testRuns = manifest?.catalogs.test_runs.count ?? 0;
	const projectHeaders: Record<ProjectSection, { title: string; label: string; shown: boolean }> = {
		projects: {
			title: projectGrouping === "host-project" ? "HOSTS" : "PROJECTS",
			label: projectGrouping === "host-project" ? "Hosts" : "Projects",
			shown: projectsShown,
		},
		"test-runs": { title: `Test runs · ${testRuns}`, label: sectionLabel("Test runs", testRuns, "project"), shown: testRuns > 0 },
		archived: { title: `ARCHIVED · ${archived}`, label: sectionLabel("Archived", archived, "project"), shown: archived > 0 },
	};
	const shownSections = PROJECT_SECTIONS.filter((section) => projectHeaders[section].shown).map((section) => {
		const folded = isFolded(SECTION_FOLDS[section].fold, SECTION_FOLDS[section].foldedByDefault);
		const { view } = projectSections[section];
		const items = folded
			? []
			: projectTreeItems({
					section,
					projects: view.projects,
					pages: view.pages,
					sources: hostSources,
					organizeBy,
					isFolded,
					hostLiveCount,
					remainingProjects: view.remaining,
				});
		return { section, folded, items };
	});
	// The rows the row menu sheet can be about: Live's and the categories'
	// (a fold hides them, but they stay loaded) and the project sessions in
	// the shown tree.
	const shownRows = useShownRows([
		...[...bands.needsYou, ...bands.finished, ...bands.working, ...bands.idle].map((item) => ({ item, archived: false })),
		...pins.flatMap((pin) => {
			const page = snapshot.pinSections[pin.id];
			return page?.loaded ? page.rows.map((row) => ({ item: classify(row), archived: false })) : [];
		}),
		...shownSections.flatMap(({ items }) =>
			items.flatMap((item) => (item.kind === "session" ? [{ item: projectRow(item.row), archived: item.archived }] : [])),
		),
	]);
	// The sheet reads the row live, so its actions follow the row while it's
	// open, and hands each answer back to the Board's own handlers.
	const menuHandlers = useRef({ actOnRow, openSession });
	menuHandlers.current = { actOnRow, openSession };
	const rowMenuHost = useMemo<RowMenuHost>(
		() => ({
			item: (ref, archived) => shownRows.get(shownRowKey(ref, archived))?.item,
			actions: (item, archived) => menuActions(item, archived),
			hostLabel,
			act: (item, action) => menuHandlers.current.actOnRow(item, action),
			openSession: (item) => menuHandlers.current.openSession(item.row),
			// Nothing on the Board waits for the menu to close.
			closed: () => {},
		}),
		[shownRows, actionsConnected, organization.ready, hostLabel],
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
	const projectMenu = (section: ProjectSection, project: NavigationProjectSummary) => {
		const actions = projectMenuActions(project, {
			connected,
			organizationReady: organization.ready,
			archived: section === "archived",
		});
		return actions.length ? () => openProjectMenu(organization, project, actions) : undefined;
	};
	const projectItem = (section: ProjectSection, item: ProjectTreeItem) => {
		if (item.kind === "session")
			return (
				<View key={item.key} style={{ marginLeft: 16 * item.depth }}>
					{rows([projectRow(item.row)], "quiet", false, item.archived)}
				</View>
			);
		if (item.kind === "more" || item.kind === "moreProjects")
			return (
				<View
					key={item.key}
					testID={item.kind === "more" ? "project-more" : "project-more-projects"}
					onLayout={(event) => {
						moreFrames.current.set(item.key, event.nativeEvent.layout);
						readVisibleMore();
					}}
				>
					<ProjectTreeRow item={item} onPress={() => readMore(section, item)} />
				</View>
			);
		return (
			<ProjectTreeRow
				key={item.key}
				item={item}
				onPress={() => {
					if ("fold" in item) setFolded(item.fold, !item.folded);
				}}
				onLongPress={item.kind === "project" ? projectMenu(section, item.project) : undefined}
				changing={item.kind === "project" && journalHoldsProject(organization, item.project.key)}
			/>
		);
	};

	let live: ReactNode;
	// Update needed says everything there is to say until something loads.
	if (!snapshot.loaded && fatal) live = null;
	else if (firstReadFailed) live = <FirstReadFailed />;
	else if (!snapshot.loaded) live = <Skeleton />;
	else if (liveTotal === 0) live = <EmptyBoard disabled={!connected} onNewSession={newSession} />;
	else
		live = (
			<>
				{summary ? <SummaryLine summary={summary} connected={connected} onJump={jumpToBand} /> : null}
				{band("needsYou", false)}
				{band("finished", false)}
				{band("working", true)}
				{bands.idle.length ? (
					<View onLayout={measure("idle")}>
						<IdleFold count={bands.idle.length} folded={idleFolded} onToggle={() => foldIdle(!idleFolded)} />
						{idleFolded ? null : rows(bands.idle, "quiet", false)}
					</View>
				) : null}
			</>
		);

	return (
		<View style={{ flex: 1, backgroundColor: palette.page }}>
			{/* Fixed under the header; their sections aren't there while
			    search results are. */}
			{chips.length && !searching ? <Chips chips={chips} /> : null}
			<View style={{ flex: 1 }}>
				<ScrollView
					ref={scroller}
					style={{ flex: 1 }}
					// Starts just past the search field: pulling down reveals it
					// (spec 7.3). iOS applies this once, when the scroller mounts.
					contentOffset={{ x: 0, y: searchFieldHeight }}
					keyboardShouldPersistTaps="handled"
					keyboardDismissMode="on-drag"
					// iOS keeps that offset only while the content is taller than the
					// viewport, so even a short Board (skeleton, empty, a few rows)
					// is tall enough to keep the field hidden.
					contentContainerStyle={{ paddingBottom: 24, minHeight: windowHeight + searchFieldHeight }}
					onScroll={onScroll}
					onLayout={(event) => {
						viewport.current = { ...viewport.current, height: event.nativeEvent.layout.height };
						readMoreLiveIfNear();
						readVisibleMore();
					}}
					onContentSizeChange={readMoreLiveIfNear}
					scrollEventThrottle={100}
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
						/>
					) : (
						<>
							{fatal ? <NoticeRow text={INCOMPATIBLE} /> : null}
							<BoardNotices hubId={hubId} notices={hubNotices} navigation={navigation} />
							<View
								testID="live-block"
								onLayout={(event) => {
									const { y, height } = event.nativeEvent.layout;
									offsets.current.live = y;
									liveEnd.current = y + height;
									readMoreLiveIfNear();
								}}
							>
								{live}
							</View>
							{pins.map((pin) => (
								<PinnedSection
									key={pin.id}
									section={pin}
									page={snapshot.pinSections[pin.id]}
									classify={classify}
									context={listContext}
									folded={folds.isFolded(pin.id)}
									onToggle={() => folds.setFolded(pin.id, !folds.isFolded(pin.id))}
									onMenu={categoryMenu.menuFor(pin)}
									changing={categoryMenu.changing(pin.id)}
									onLayout={measure(`pin:${pin.id}`)}
								/>
							))}
							{shownSections.map(({ section, folded, items }) => (
								<View
									key={section}
									testID={`project-section:${section}`}
									onLayout={(event) => {
										measure(section)(event);
										readVisibleMore();
									}}
									style={{ paddingTop: 10 }}
								>
									<ProjectSectionHeader
										title={projectHeaders[section].title}
										label={projectHeaders[section].label}
										folded={folded}
										onToggle={() => setFolded(SECTION_FOLDS[section].fold, !folded)}
										organize={
											section === "projects" && projectGrouping !== "flat"
												? { by: organizeBy, onChange: chooseOrganizeBy }
												: null
										}
									/>
									{items.map((item) => projectItem(section, item))}
								</View>
							))}
						</>
					)}
				</ScrollView>
				{/* The toast floats 10pt above the toolbar. */}
				<View pointerEvents="box-none" style={{ position: "absolute", left: 0, right: 0, bottom: 10 }}>
					<Toast toast={toast.toast} dismiss={toast.dismiss} />
				</View>
			</View>
			<BoardToolbar state={state} fatal={fatal} newSessionDisabled={!connected} onNewSession={newSession} />
		</View>
	);
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
						if (Array.from(name).length > 80) {
							Alert.alert("Category names can be up to 80 characters.");
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
			{
				text: "Delete",
				style: "destructive",
				onPress: () => {
					if (!organizationOpen(organization) || !listed(section.id)) return;
					void organization.actions?.deletePinSection({ sectionId: section.id });
				},
			},
		]);
	const open = (section: NavigationPinSectionDescriptor) => {
		if (!organizationOpen(organization)) return;
		if (Platform.OS === "ios") {
			ActionSheetIOS.showActionSheetWithOptions(
				{ title: section.name, options: ["Rename", "Delete", "Cancel"], destructiveButtonIndex: 1, cancelButtonIndex: 2 },
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
	const operation = organization.state?.pending ? organization.state.recovery?.operation : undefined;
	return {
		menuFor: (section: NavigationPinSectionDescriptor) => (organization.ready ? () => open(section) : null),
		changing: (sectionId: string) =>
			(operation?.kind === "renamePinSection" || operation?.kind === "deletePinSection") &&
			operation.params.sectionId === sectionId,
	};
}

/** Shut down from the row menu: asked first, then the Session's own
 * request, then a toast either way. */
function confirmShutDown(
	client: ConversationClientLike | null,
	row: NavigationSessionSummary,
	toast: Pick<ToastController, "show">,
) {
	Alert.alert(`Shut down “${row.title}”?`, "The agent stops. Send it a message to resume it.", [
		{ text: "Cancel", style: "cancel" },
		{
			text: "Shut down",
			style: "destructive",
			onPress: () => {
				if (!client) return;
				shutDownSession(client, row.ref).then(
					() => toast.show({ text: "Session shut down" }),
					(error: unknown) => toast.show({ text: `Couldn't shut down “${row.title}”: ${hubMessage(error)}` }),
				);
			},
		},
	]);
}

/** Rename from the row menu (iOS only: Alert.prompt), starting from the
 * row's title. An empty name sends nothing. */
function promptRename(client: ConversationClientLike | null, row: NavigationSessionSummary, toast: Pick<ToastController, "show">) {
	Alert.prompt(
		"Rename session",
		undefined,
		[
			{ text: "Cancel", style: "cancel" },
			{
				text: "Rename",
				onPress: (name?: string) => {
					if (!client) return;
					renameSession(client, row.ref, name ?? "").then(
						(renamed) => {
							if (renamed) toast.show({ text: "Renamed" });
						},
						(error: unknown) => toast.show({ text: `Couldn't rename “${row.title}”: ${hubMessage(error)}` }),
					);
				},
			},
		],
		"plain-text",
		row.title,
	);
}

const hubMessage = (error: unknown) => (error instanceof Error ? error.message : String(error));

/** A project row's long-press menu (ruling 15): Pin to top or Unpin, and
 * Archive or Unarchive, as an action sheet, or an alert off iOS. */
function openProjectMenu(
	organization: BoardOrganization,
	project: NavigationProjectSummary,
	actions: readonly ProjectMenuAction[],
) {
	if (!organizationOpen(organization)) return;
	const act = (action: ProjectMenuAction) => {
		if (!organizationOpen(organization)) return;
		if (action === "pin" || action === "unpin") void organization.actions?.favorite(project.key, action === "pin");
		else
			void organization.actions?.archive(
				{ kind: "project", id: project.key, workingDir: project.working_dir },
				action === "archive",
			);
	};
	const title = projectName(project);
	if (Platform.OS === "ios") {
		ActionSheetIOS.showActionSheetWithOptions(
			{ title, options: [...actions.map((action) => PROJECT_MENU_LABELS[action]), "Cancel"], cancelButtonIndex: actions.length },
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

/** The search field's row: an 8pt margin around a field that grows with
 * the text size. */
const searchFieldHeightAt = (scale: number) => 16 + Math.round(36 * scale);

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
	return (
		<View
			testID="search-field"
			style={{ height, paddingHorizontal: 16, paddingVertical: 8, flexDirection: "row", alignItems: "center", columnGap: 12 }}
		>
			<View
				style={{
					flex: 1,
					alignSelf: "stretch",
					flexDirection: "row",
					alignItems: "center",
					columnGap: 6,
					paddingHorizontal: 8,
					borderRadius: 10,
					backgroundColor: palette.inset,
				}}
			>
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
					allowFontScaling={Platform.OS !== "ios"}
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
					<Text allowFontScaling={Platform.OS !== "ios"} style={{ fontSize: 17 * scale, color: palette.accentInk }}>
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
 * every row as seen, so nothing flashes Finished. */
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

/** While any of the Board's reads has failed on a ready connection (Live,
 * Needs you, the pin catalog, a category or the manifest; first read or
 * later), rebind the client after a backoff that grows with each failed
 * attempt. Nothing else would retry it while the Board stays in view: an
 * idle fleet sends no invalidations, and the controller retries failed
 * reads only when it resumes, on a focus change. The retry waits while
 * another read is still out, so a rebind never cancels a healthy read.
 * Rebinding is the reconnect path: the loaded rows stay on screen until the
 * fresh reads land, and the screen's load-more pages Live back out. A read
 * that lands, or a new connection, starts the count over; with no client
 * (disconnected or out of view) the hook holds its count and schedules
 * nothing. */
function useReadRetry(
	board: BoardController,
	client: ConversationClientLike | null,
	snapshot: Pick<BoardSnapshot, "loaded" | "retained" | "reading" | "error">,
) {
	const [retries, setRetries] = useState({ client, count: 0 });
	const count = retries.client === client ? retries.count : 0;
	const failed = snapshot.error !== null && !snapshot.reading;
	// Only fresh reads that settled count as success: the Board has loaded
	// and shows nothing retained, with no error and no read in flight. A read
	// a pause cancelled leaves no error and no loading flag, but it never
	// lands, so its page stays unloaded or retained.
	const succeeded = snapshot.loaded && !snapshot.retained && snapshot.error === null && !snapshot.reading;
	useEffect(() => {
		if (client && succeeded && count > 0) setRetries({ client, count: 0 });
	}, [client, count, succeeded]);
	useEffect(() => {
		if (!failed || !client) return;
		const timer = setTimeout(() => {
			board.setClient(client);
			setRetries({ client, count: count + 1 });
		}, reconnectDelay(count + 1));
		return () => clearTimeout(timer);
	}, [board, client, failed, count]);
}

function useHeader(navigation: Navigation, hubId: string, hubName: string, connected: boolean, revealSearch: () => void) {
	const { fontScale } = useWindowDimensions();
	useEffect(() => {
		const hubButton = (
			<HubButton
				hubName={hubName}
				connected={connected}
				onSettings={() => navigation.navigate("HubSettings", { hubId })}
				onSwitch={() => navigation.navigate("Hubs")}
			/>
		);
		navigation.setOptions({
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
				<Action label="Search sessions" onPress={revealSearch}>
					{fontScale > 1.4 ? "Find" : "Search"}
				</Action>
			),
		});
	}, [navigation, hubId, hubName, connected, revealSearch, fontScale]);
}

/** The hub button (spec 7.1): the hub's name and a chevron as one control,
 * opening the hub menu (ruling 9) until phase 5's Hub sheet. A native bar
 * item given both a label and an icon draws only the icon, so this is a
 * custom header view, and the menu is an action sheet. */
function HubButton({
	hubName,
	connected,
	onSettings,
	onSwitch,
}: {
	hubName: string;
	connected: boolean;
	onSettings: () => void;
	onSwitch: () => void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const { width } = useWindowDimensions();
	const open = () => {
		if (Platform.OS === "ios") {
			ActionSheetIOS.showActionSheetWithOptions(
				{
					title: hubName,
					options: ["Hub settings", "Switch hub", "Cancel"],
					cancelButtonIndex: 2,
					// Hub settings needs the hub; Switch hub doesn't.
					disabledButtonIndices: connected ? [] : [0],
				},
				(index) => {
					if (index === 0) onSettings();
					else if (index === 1) onSwitch();
				},
			);
			return;
		}
		// An alert has no disabled buttons, so Hub settings leaves the list
		// while the hub is out of reach.
		Alert.alert(hubName, undefined, [
			...(connected ? [{ text: "Hub settings", onPress: onSettings }] : []),
			{ text: "Switch hub", onPress: onSwitch },
			{ text: "Cancel", style: "cancel" },
		]);
	};
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={`${hubName}, hub menu`}
			onPress={open}
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
				allowFontScaling={Platform.OS !== "ios"}
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

/** The section chips, fixed under the header (spec 7.1). The row fades at its
 * trailing edge, so a cut-off chip reads as "there's more". */
function Chips({ chips }: { chips: ChipProps[] }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View testID="chips" style={{ backgroundColor: palette.page }}>
			<ScrollView
				horizontal
				showsHorizontalScrollIndicator={false}
				contentContainerStyle={{ columnGap: 8, paddingHorizontal: 16, paddingVertical: 8 }}
			>
				{chips.map((chip) => (
					<Pressable
						key={chip.key}
						testID="chip"
						accessibilityRole="button"
						accessibilityLabel={chip.label}
						onPress={chip.onPress}
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
						<Text allowFontScaling={Platform.OS !== "ios"} style={{ fontSize: 14 * scale, fontWeight: "600", color: palette.inkHi }}>
							{chip.name}
						</Text>
						<Text
							allowFontScaling={Platform.OS !== "ios"}
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
									allowFontScaling={Platform.OS !== "ios"}
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
			</ScrollView>
			<View
				pointerEvents="none"
				style={{
					position: "absolute",
					top: 0,
					bottom: 0,
					right: 0,
					width: 28,
					experimental_backgroundImage: `linear-gradient(to right, ${palette.page}00, ${palette.page})`,
				}}
			/>
		</View>
	);
}

/** The Live summary line (spec 7.1): each count jumps to its band. */
function SummaryLine({
	summary,
	connected,
	onJump,
}: {
	summary: LiveSummary;
	connected: boolean;
	onJump: (band: Band) => void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const entries = (["needsYou", "finished", "working", "idle"] as const).filter((band) => summary[band] > 0);
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
				<View key={band} style={{ flexDirection: "row", alignItems: "center" }}>
					{index > 0 ? (
						<Text allowFontScaling={Platform.OS !== "ios"} style={{ fontSize: 14 * scale, color: palette.inkLow }}>
							{" · "}
						</Text>
					) : null}
					<Pressable
						accessibilityRole="button"
						onPress={() => onJump(band)}
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
						{band === "working" ? <PulseMeter tone={connected ? "alive" : "gray"} /> : null}
						<Text
							allowFontScaling={Platform.OS !== "ios"}
							style={{
								fontSize: 14 * scale,
								lineHeight: 20 * scale,
								fontWeight: band === "needsYou" ? "600" : "400",
								color: band === "needsYou" ? palette.attentionInk : palette.inkMid,
							}}
						>
							{summaryText(band, summary[band])}
						</Text>
					</Pressable>
				</View>
			))}
		</View>
	);
}

/** Idle's header, folded by default; its state persists per device. */
function IdleFold({ count, folded, onToggle }: { count: number; folded: boolean; onToggle: () => void }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={`Idle, ${plural(count, "session")}`}
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
			<Text testID="band-header" allowFontScaling={Platform.OS !== "ios"} style={{ fontSize: 15 * scale, color: palette.inkMid }}>
				{`Idle · ${count}`}
			</Text>
			<FoldChevron folded={folded} />
		</Pressable>
	);
}

/** The very first load: three still placeholder rows. */
function Skeleton() {
	const { palette } = useColors();
	return (
		<View style={{ paddingTop: 12, paddingHorizontal: 16, rowGap: 8 }} accessibilityLabel="Loading sessions">
			{["first", "second", "third"].map((key) => (
				<View key={key} testID="skeleton-row" style={{ height: 64, borderRadius: 10, backgroundColor: palette.inset }} />
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
				allowFontScaling={Platform.OS !== "ios"}
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
				allowFontScaling={Platform.OS !== "ios"}
				style={{ fontSize: 17 * scale, lineHeight: 22 * scale, color: palette.inkMid, textAlign: "center" }}
			>
				Nothing's running. Start a session to put an agent to work.
			</Text>
			<Action tone="primary" disabled={disabled} onPress={onNewSession}>
				New session
			</Action>
		</View>
	);
}
