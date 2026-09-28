import {
	humanizeState,
	type NavigationPinSectionDescriptor,
	type NavigationSessionSummary,
	quietState,
} from "@evener/appwire-client";
import { useFocusEffect, useIsFocused } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { SymbolView } from "expo-symbols";
import {
	type ReactNode,
	useCallback,
	useEffect,
	useMemo,
	useReducer,
	useRef,
	useState,
	useSyncExternalStore,
} from "react";
import {
	ActionSheetIOS,
	Alert,
	FlatList,
	Keyboard,
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
import { createRosterService } from "../../../mobile/src/services/roster";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { useConnection } from "../ConnectionProvider";
import { reconnectDelay } from "../hubConnection";
import { drafts } from "../nativeDrafts";
import { RosterSearch } from "../rosterSearch";
import type { Routes } from "../screens";
import { useScreenInFront } from "../sheet/useScreenInFront";
import { Action, Copy, styles, useColors, useTextScale } from "../ui";
import { usePinNavigation } from "../usePinNavigation";
import {
	type Band,
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
import { ACTIVITY_POLL_MS, ActivityPoll, isFreshRead } from "./activityPoll";
import type { SeenMarkers } from "./boardMemory";
import { bandHeaderText, BoardRows, FoldChevron, type RowContext } from "./BoardRow";
import { BoardToolbar } from "./BoardToolbar";
import { type BoardController, type BoardSnapshot, createBoardController } from "./boardData";
import { foldedSections, seenMarkers } from "./nativeBoardMemory";
import { PinnedSection, useCategoryFolds } from "./PinnedSections";
import { fleetMinutes } from "./pulse";
import { PulseMeter } from "./PulseMeter";

type Props = NativeStackScreenProps<Routes, "Sessions">;
type Navigation = Props["navigation"];

const SEARCH_PAGE_SIZE = 50;
const MINUTE = 60_000;
const INCOMPATIBLE =
	"This app and the hub need compatible versions. Update the app from TestFlight, or update Evener on the hub.";
const BAND_HEADERS: Record<Exclude<Band, "idle">, string> = {
	needsYou: "NEEDS YOU",
	finished: "FINISHED",
	working: "WORKING",
};

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
	const { client, state, fatal } = useConnection();
	const { palette } = useColors();
	const connected = state === "ready";
	const focused = useIsFocused();
	// Activity keeps polling while only a sheet covers the Board: the sheet
	// is part of the screen under it.
	const inFront = useScreenInFront(routeKey);
	const { activityOf, msSinceRead, revision: activityRevision } = useActivityPoll(client, connected, inFront);
	const [board] = useState(createBoardController);
	useEffect(() => () => board.dispose(), [board]);
	const snapshot = useSyncExternalStore(board.subscribe, board.getSnapshot);
	const markers = seenMarkers(hubId);
	const seenRevision = useSyncExternalStore(markers.subscribe, markers.getRevision);
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
		() =>
			liveBands(
				snapshot.live.rows,
				snapshot.needsYou.rows,
				(row) => markers.isSeen(row),
				(row) => {
					const activity = activityOf(row.ref);
					return activity ? quietState(activity, msSinceRead ?? 0)?.state === "stuck" : false;
				},
			),
		// seenRevision re-runs isSeen after a mark or first run. activityRevision
		// re-runs isStuck after each read, and activityOf changes when the
		// connection drops or returns, or the read goes stale. msSinceRead is
		// left out on purpose: it changes on every render, so it would re-sort
		// Working every time, and a row's place only needs to be as fresh as the
		// last read (its why line reads msSinceRead live).
		[snapshot.live.rows, snapshot.needsYou.rows, markers, seenRevision, activityOf, activityRevision],
	);
	useFirstRun(board, markers, snapshot, focused);

	const usual = useMemo(() => usualPlace(snapshot.live.rows), [snapshot.live.rows]);
	const sources = snapshot.manifest?.sources;
	const hostLabel = useMemo(() => {
		const labels = new Map((sources ?? []).map((source) => [source.id, source.label]));
		return (hostId: string) => labels.get(hostId) ?? hostId;
	}, [sources]);

	const classify = useMemo(
		() => rowClassifier(snapshot.needsYou.rows, (row) => markers.isSeen(row)),
		[snapshot.needsYou.rows, markers],
	);
	const folds = useCategoryFolds(hubId);
	const categoryMenu = useCategoryMenu(hubId, () => board.getSnapshot().pins.rows);

	const [idleFolded, setIdleFolded] = useState(() => foldedSections(hubId).isFolded("idle", true));
	const foldIdle = (folded: boolean) => {
		foldedSections(hubId).setFolded("idle", folded);
		setIdleFolded(folded);
	};

	const [searchOpen, setSearchOpen] = useState(false);
	const search = useBoardSearch(client, connected);
	const closeSearch = search.clear;
	const toggleSearch = useCallback(() => {
		if (searchOpen) closeSearch();
		setSearchOpen(!searchOpen);
	}, [searchOpen, closeSearch]);
	useHeader(navigation, hubId, hubName, connected, toggleSearch);

	const newSession = () => navigation.navigate("NewSession", { hubId, hubName });
	const openSession = (row: NavigationSessionSummary) => {
		markers.markSeen(row);
		navigation.navigate("Conversation", { hubId, ref: row.ref, title: row.title });
	};
	// A search result opens like its Board row when the Board lists it, so
	// it's marked seen with the row's own hub timestamp. A session the Board
	// doesn't list has no Finished state to clear.
	const openResult = (result: { ref: string; title: string }) => {
		const listed = (row: NavigationSessionSummary) => row.ref === result.ref;
		const row = snapshot.live.rows.find(listed) ?? snapshot.needsYou.rows.find(listed);
		if (row) openSession(row);
		else navigation.navigate("Conversation", { hubId, ref: result.ref, title: result.title });
	};

	// Where each section starts in the scroller, for the chips and the
	// summary line to jump to. Bands measure inside the Live block.
	const scroller = useRef<ScrollView>(null);
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
	const readMoreLiveIfNear = () => {
		const page = board.getSnapshot().live;
		if (liveEnd.current === null || page.remaining === 0 || page.loading || page.stale || page.error) return;
		const { offset, height } = viewport.current;
		if (offset + 2 * height >= liveEnd.current) void board.loadMoreLive();
	};
	const onScroll = (event: NativeSyntheticEvent<NativeScrollEvent>) => {
		const { contentOffset, layoutMeasurement } = event.nativeEvent;
		viewport.current = { offset: contentOffset.y, height: layoutMeasurement.height };
		readMoreLiveIfNear();
	};

	const manifest = snapshot.manifest;
	const liveTotal = bands.needsYou.length + bands.finished.length + bands.working.length + bands.idle.length;
	// Every category keeps its section; only the chips hide empty ones.
	const pins = snapshot.pins.rows;
	const projects = manifest?.catalogs.projects.count ?? 0;
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
	if (projects > 0)
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

	const rowContext: RowContext = {
		connected,
		usual,
		hostLabel,
		now,
		onOpen: openSession,
		draftRefs,
		activityOf,
		msSinceRead,
	};
	const rows = (items: ClassifiedRow[], variant: "signal" | "quiet", moving: boolean) => (
		<BoardRows items={items} variant={variant} moving={moving} context={rowContext} />
	);
	const band = (key: Exclude<Band, "idle">, moving: boolean) =>
		bands[key].length ? (
			<View key={key} onLayout={measure(key)}>
				<BandHeader text={`${BAND_HEADERS[key]} · ${bands[key].length}`} />
				{rows(bands[key], "signal", moving)}
			</View>
		) : null;
	const summary = liveSummary(bands);
	// The fleet meter sums the working sessions the poll has read so far, and
	// stays still until it has read one.
	const workingMinutes = bands.working
		.map((item) => activityOf(item.row.ref)?.minutes)
		.filter((minutes): minutes is number[] => minutes !== undefined);
	const fleetPerMinute = workingMinutes.length ? fleetMinutes(workingMinutes) : undefined;

	let live: ReactNode;
	// Update needed says everything there is to say until something loads.
	if (!snapshot.loaded && fatal) live = null;
	else if (firstReadFailed) live = <FirstReadFailed />;
	else if (!snapshot.loaded) live = <Skeleton />;
	else if (liveTotal === 0) live = <EmptyBoard disabled={!connected} onNewSession={newSession} />;
	else
		live = (
			<>
				{summary ? (
					<SummaryLine summary={summary} connected={connected} perMinute={fleetPerMinute} onJump={jumpToBand} />
				) : null}
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
			{searchOpen ? <SearchField search={search} connected={connected} /> : null}
			{search.active ? (
				<SearchResults search={search} connected={connected} onOpen={openResult} />
			) : (
				<>
					{chips.length ? <Chips chips={chips} /> : null}
					<ScrollView
						ref={scroller}
						style={{ flex: 1 }}
						contentContainerStyle={{ paddingBottom: 24 }}
						onScroll={onScroll}
						onLayout={(event) => {
							viewport.current = { ...viewport.current, height: event.nativeEvent.layout.height };
							readMoreLiveIfNear();
						}}
						onContentSizeChange={readMoreLiveIfNear}
						scrollEventThrottle={100}
					>
						{fatal ? <Notice text={INCOMPATIBLE} /> : null}
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
								context={rowContext}
								folded={folds.isFolded(pin.id)}
								onToggle={() => folds.setFolded(pin.id, !folds.isFolded(pin.id))}
								onMenu={categoryMenu.menuFor(pin)}
								changing={categoryMenu.changing(pin.id)}
								onLayout={measure(`pin:${pin.id}`)}
							/>
						))}
						{projects > 0 ? (
							<View onLayout={measure("projects")}>
								<SectionRow
									text={`Projects · ${projects}`}
									label={sectionLabel("Projects", projects, "project")}
									onPress={() => navigation.navigate("Projects", { hubId, archived: false })}
								/>
							</View>
						) : null}
						{archived > 0 ? (
							<View onLayout={measure("archived")}>
								<SectionRow
									text={`Archived · ${archived}`}
									label={sectionLabel("Archived", archived, "project")}
									onPress={() => navigation.navigate("Projects", { hubId, archived: true })}
								/>
							</View>
						) : null}
					</ScrollView>
				</>
			)}
			<BoardToolbar state={state} fatal={fatal} newSessionDisabled={!connected} onNewSession={newSession} />
		</View>
	);
}

/** Rename and Delete for the pinned categories (spec 7.1), through one
 * organization journal for the whole Board. ⋯ shows only while a change can
 * go out: connected, the journal confirmed against the hub, and no change
 * pending or unresolved. The journal's own error text is never shown; a
 * change on its way dims its category. */
function useCategoryMenu(hubId: string, catalog: () => readonly NavigationPinSectionDescriptor[]) {
	const pin = usePinNavigation(hubId);
	const free = (action: { pending: boolean; uncertain: boolean; storageUnavailable: boolean } | null) =>
		!!action && !action.pending && !action.uncertain && !action.storageUnavailable;
	const canChange = pin.ready && pin.confirmed && !!pin.actions && free(pin.action);
	// Checked again at every press: the connection or the journal may have
	// moved while the sheet or an alert was up.
	const canChangeNow = () => pin.isCurrent() && !!pin.actions && free(pin.actions.getSnapshot());
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
						if (!canChangeNow() || !listed(section.id)) return;
						void pin.actions?.renamePinSection({ sectionId: section.id, name });
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
					if (!canChangeNow() || !listed(section.id)) return;
					void pin.actions?.deletePinSection({ sectionId: section.id });
				},
			},
		]);
	const open = (section: NavigationPinSectionDescriptor) => {
		if (!canChangeNow()) return;
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
	const operation = pin.action?.pending ? pin.action.recovery?.operation : undefined;
	return {
		menuFor: (section: NavigationPinSectionDescriptor) => (canChange ? () => open(section) : null),
		changing: (sectionId: string) =>
			(operation?.kind === "renamePinSection" || operation?.kind === "deletePinSection") &&
			operation.params.sectionId === sectionId,
	};
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

const noSubscription = () => () => {};
const noRevision = () => 0;

/** S5's activity for every live session (activityPoll.ts), polled while
 * connected and in front. A poll is bound to the client it was made with, so
 * each client gets a fresh one, and there is none without a client. The
 * revision changes whenever the poll's report does: a read lands, or the hub
 * turns out to predate S5.
 *
 * The underlying client survives a reconnect (hubConnection.ts), so a poll
 * that stops on disconnect still holds its last read, and a hub that reports
 * ready but has stopped delivering reads leaves the same stale data behind
 * without ever disconnecting at all - reading either as current would let a
 * read merely aging past isFreshRead's threshold read as "stuck", a false
 * alarm about the connection or the hub rather than the session (Jesse's
 * ruling). Gating the RETURNED reading on `connected` AND freshness, and
 * never handing back the poll itself, means no caller can read around this:
 * every row, its meter and the Working order all fall back to their pre-S5
 * appearance the moment either one fails, and agree with each other since
 * there is only the one gate. Nothing re-renders the Board when a read merely
 * ages, so while a fresh read is on screen the recheck below re-renders at
 * the polling cadence, dropping the read within one interval of its going
 * stale, whatever becomes of the poll meanwhile. With no fresh read on screen
 * there is nothing to expire, so it doesn't run: not before the first read
 * lands, not while reads keep failing, and never on a hub that predates S5. */
function useActivityPoll(client: ConversationClientLike | null, connected: boolean, inFront: boolean) {
	const poll = useMemo(() => (client ? new ActivityPoll(client) : null), [client]);
	const revision = useSyncExternalStore(poll?.subscribe ?? noSubscription, poll?.getRevision ?? noRevision);
	useEffect(() => {
		if (!poll || !connected || !inFront) return;
		poll.start();
		return () => poll.stop();
	}, [poll, connected, inFront]);
	const msSinceRead = poll?.msSinceRead() ?? null;
	const reading = connected && isFreshRead(msSinceRead) ? poll : null;
	const [, recheck] = useReducer((n: number) => n + 1, 0);
	useEffect(() => {
		if (!reading || !inFront) return;
		const timer = setInterval(recheck, ACTIVITY_POLL_MS);
		return () => clearInterval(timer);
	}, [reading, inFront]);
	const activityOf = useCallback((ref: string) => reading?.activity(ref), [reading]);
	return { revision, activityOf, msSinceRead: reading ? msSinceRead : null };
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

function useHeader(navigation: Navigation, hubId: string, hubName: string, connected: boolean, toggleSearch: () => void) {
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
					onPress: toggleSearch,
				},
			],
			headerLeft: () => hubButton,
			headerRight: () => (
				<Action label="Search sessions" onPress={toggleSearch}>
					{fontScale > 1.4 ? "Find" : "Search"}
				</Action>
			),
		});
	}, [navigation, hubId, hubName, connected, toggleSearch, fontScale]);
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
		<View style={{ backgroundColor: palette.page }}>
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
						{band === "working" ? <PulseMeter tone={connected ? "alive" : "gray"} perMinute={perMinute} /> : null}
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

function BandHeader({ text }: { text: string }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Text
			testID="band-header"
			accessibilityRole="header"
			allowFontScaling={Platform.OS !== "ios"}
			style={{
				paddingTop: 22,
				paddingBottom: 6,
				paddingHorizontal: 16,
				...bandHeaderText(palette, scale),
			}}
		>
			{text}
		</Text>
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

/** Projects or Archived, after the pinned categories: a row that opens its
 * own screen until PR 3b brings it inline. */
function SectionRow({ text, label, onPress }: { text: string; label: string; onPress: () => void }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={label}
			onPress={onPress}
			style={({ pressed }) => ({
				minHeight: 48,
				paddingHorizontal: 16,
				flexDirection: "row",
				alignItems: "center",
				columnGap: 8,
				borderTopWidth: 0.5,
				borderColor: palette.edge,
				backgroundColor: pressed ? palette.pressed : palette.page,
			})}
		>
			<Text allowFontScaling={Platform.OS !== "ios"} style={{ flex: 1, fontSize: 17 * scale, color: palette.inkHi }}>
				{text}
			</Text>
			<SymbolView name="chevron.right" size={13 * scale} tintColor={palette.inkLow} />
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

/** A hub-level notice on the page, like a row: the mark says it needs you. */
function Notice({ text }: { text: string }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View
			style={{
				marginTop: 4,
				marginHorizontal: 16,
				paddingTop: 8,
				paddingBottom: 10,
				flexDirection: "row",
				alignItems: "center",
				columnGap: 10,
				borderBottomWidth: 0.5,
				borderColor: palette.edge,
			}}
		>
			<View style={{ width: 22, alignItems: "center" }}>
				<SymbolView name="exclamationmark.triangle.fill" size={17 * scale} tintColor={palette.attention} />
			</View>
			<Text
				allowFontScaling={Platform.OS !== "ios"}
				style={{ flex: 1, fontSize: 15 * scale, lineHeight: 20 * scale, color: palette.inkHi }}
			>
				{text}
			</Text>
		</View>
	);
}

type BoardSearch = ReturnType<typeof useBoardSearch>;

/** Today's roster search until PR 5 replaces it: thread/list matched on the
 * hub, results in place of the Board. */
function useBoardSearch(client: ConversationClientLike | null, connected: boolean) {
	const [text, setText] = useState("");
	const [active, setActive] = useState(false);
	const roster = useMemo(
		() => new RosterSearch(client ? createRosterService(client, SEARCH_PAGE_SIZE) : null),
		[client],
	);
	const results = useSyncExternalStore(roster.subscribe, roster.getSnapshot);
	useEffect(() => {
		setText("");
		setActive(false);
		return () => roster.cancel();
	}, [roster]);
	useFocusEffect(
		useCallback(
			() => (client && connected && active ? roster.watch(client) : () => {}),
			[roster, client, connected, active],
		),
	);
	const search = useCallback(
		(value: string) => {
			if (!connected) return;
			Keyboard.dismiss();
			if (!value.trim()) {
				setActive(false);
				roster.cancel();
				return;
			}
			setActive(true);
			void roster.load(value);
		},
		[connected, roster],
	);
	const clear = useCallback(() => {
		setText("");
		setActive(false);
		roster.cancel();
	}, [roster]);
	return { ...results, text, setText, active, search, clear };
}

function SearchField({ search, connected }: { search: BoardSearch; connected: boolean }) {
	const colors = useColors();
	const { fontScale } = useWindowDimensions();
	return (
		<View style={{ paddingHorizontal: 16, paddingVertical: 8, gap: 4 }}>
			<View style={[styles.row, { flexWrap: "wrap" }]}>
				<TextInput
					accessibilityLabel="Search sessions"
					placeholder="Search sessions"
					placeholderTextColor={colors.secondary}
					value={search.text}
					onChangeText={search.setText}
					onSubmitEditing={() => search.search(search.text)}
					returnKeyType="search"
					autoCapitalize="none"
					autoCorrect={false}
					autoFocus
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
				<Action disabled={!connected} onPress={() => search.search(search.text)}>
					Search
				</Action>
				{search.text || search.active ? <Action onPress={search.clear}>Clear</Action> : null}
			</View>
			{search.active && search.query ? <Copy muted>{`Results for “${search.query}”`}</Copy> : null}
		</View>
	);
}

function SearchResults({
	search,
	connected,
	onOpen,
}: {
	search: BoardSearch;
	connected: boolean;
	onOpen: (result: { ref: string; title: string }) => void;
}) {
	const colors = useColors();
	const { fontScale } = useWindowDimensions();
	return (
		<FlatList
			style={{ flex: 1 }}
			keyboardShouldPersistTaps="handled"
			data={search.rows}
			keyExtractor={(item) => item.ref}
			contentContainerStyle={{ paddingBottom: 16, gap: 12 }}
			ListEmptyComponent={
				<View style={{ paddingHorizontal: 16 }}>
					<Copy muted>
						{search.loading
							? "Loading sessions…"
							: search.error
								? "Could not search this hub's sessions."
								: search.query
									? "No sessions match your search."
									: ""}
					</Copy>
				</View>
			}
			ListFooterComponent={
				search.hasMore ? (
					<View style={{ paddingHorizontal: 16 }}>
						<Copy muted>
							Showing up to {SEARCH_PAGE_SIZE} sessions. Narrow your search to find others.
						</Copy>
					</View>
				) : null
			}
			renderItem={({ item }) => {
				const stateLabel = humanizeState(item.status, item.askPending === true);
				const signals = [
					["active", "awaiting", "warning", "errored"].includes(item.status) ? stateLabel : "",
					item.askPending && item.status !== "awaiting" ? humanizeState("awaiting", true) : "",
				].filter(Boolean);
				const metadataStyle = {
					fontSize: 13 * (Platform.OS === "ios" ? fontScale : 1),
					lineHeight: 19 * (Platform.OS === "ios" ? fontScale : 1),
				};
				return (
					<Pressable
						accessibilityRole="button"
						accessibilityLabel={`Open ${item.title || "Untitled session"}`}
						accessibilityHint={[...(signals.length ? signals : [stateLabel]), item.project].filter(Boolean).join(". ")}
						disabled={!connected}
						onPress={() => onOpen(item)}
						style={{
							marginHorizontal: 16,
							paddingVertical: 13,
							minHeight: Platform.OS === "android" ? 72 : 68,
							borderBottomWidth: 0.5,
							borderColor: colors.border,
							gap: 4,
						}}
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
								style={[metadataStyle, { color: item.attention === "needsYou" ? colors.accent : colors.secondary }]}
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
								{item.projectLabel}
							</Text>
						) : null}
					</Pressable>
				);
			}}
		/>
	);
}
