import type { NavigationSessionSummary, SearchResult } from "@evener/appwire-client";
import { useFocusEffect, useIsFocused } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { type SFSymbol, SymbolView } from "expo-symbols";
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
import { drafts } from "../nativeDrafts";
import type { Routes } from "../screens";
import { Action, useColors, useTextScale } from "../ui";
import {
	type Band,
	type ClassifiedRow,
	type LiveSummary,
	liveBands,
	liveSummary,
	summaryText,
	usualPlace,
} from "./attention";
import type { SeenMarkers } from "./boardMemory";
import { BoardNotices, NoticeRow } from "./BoardNotices";
import { BandHeader, BoardRow, Hairline } from "./BoardRow";
import { BoardToolbar } from "./BoardToolbar";
import { type BoardController, type BoardSnapshot, createBoardController } from "./boardData";
import { createSearchController, type SearchScope } from "./boardSearch";
import { foldedSections, recentSearches, seenMarkers } from "./nativeBoardMemory";
import { notices } from "./notices";
import { PulseMeter } from "./PulseMeter";
import { SearchResults } from "./SearchResults";

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

const plural = (count: number, noun: string) => `${count} ${noun}${count === 1 ? "" : "s"}`;
/** A section's VoiceOver label, shared by its chip and its row. */
const sectionLabel = (name: string, count: number, noun: string) => `${name}, ${plural(count, noun)}`;

/** Home (spec 7.1): every live session ordered by who needs you, then the
 * user's pinned categories, projects and archive. */
export function BoardScreen({ navigation }: Props) {
	const { activeProfile } = useConnection();
	const { palette } = useColors();
	if (!activeProfile) return <View style={{ flex: 1, backgroundColor: palette.page }} />;
	return <Board key={activeProfile.id} hubId={activeProfile.id} hubName={activeProfile.name} navigation={navigation} />;
}

function Board({ hubId, hubName, navigation }: { hubId: string; hubName: string; navigation: Navigation }) {
	const { client, state, fatal } = useConnection();
	const { palette } = useColors();
	const connected = state === "ready";
	const focused = useIsFocused();
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
		() => liveBands(snapshot.live.rows, snapshot.needsYou.rows, (row) => markers.isSeen(row)),
		// seenRevision re-runs isSeen after a mark or first run.
		[snapshot.live.rows, snapshot.needsYou.rows, markers, seenRevision],
	);
	useFirstRun(board, markers, snapshot, focused);

	const usual = useMemo(() => usualPlace(snapshot.live.rows), [snapshot.live.rows]);
	const sources = snapshot.manifest?.sources;
	// An offline host counts its sessions on the Live and Needs you pages
	// loaded so far; pinned categories join when the Board loads them.
	const hubNotices = useMemo(
		() =>
			notices({
				auth: snapshot.auth,
				sources: sources ?? [],
				plugins: snapshot.plugins,
				loadedRows: [...snapshot.live.rows, ...snapshot.needsYou.rows],
			}),
		[snapshot.auth, sources, snapshot.plugins, snapshot.live.rows, snapshot.needsYou.rows],
	);
	const hostLabel = useMemo(() => {
		const labels = new Map((sources ?? []).map((source) => [source.id, source.label]));
		return (hostId: string) => labels.get(hostId) ?? hostId;
	}, [sources]);

	const [idleFolded, setIdleFolded] = useState(() => foldedSections(hubId).isFolded("idle", true));
	const foldIdle = (folded: boolean) => {
		foldedSections(hubId).setFolded("idle", folded);
		setIdleFolded(folded);
	};

	// Where each section starts in the scroller, for the chips and the
	// summary line to jump to. Bands measure inside the Live block.
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
	const openSearchResult = (result: SearchResult) => {
		recent.add(search.snapshot.query);
		setRecentList(recent.list());
		openResult(result);
	};
	const clearRecent = () => {
		recent.clear();
		setRecentList(recent.list());
	};

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
		// Search results fill the scroller in Live's place.
		if (searching || liveEnd.current === null || page.remaining === 0 || page.loading || page.stale || page.error) return;
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
	// Every category keeps its row; only the chips hide empty ones.
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
			onPress: () => scrollTo(`pin:${pin.id}`),
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

	const rows = (items: ClassifiedRow[], variant: "signal" | "quiet", moving: boolean) =>
		items.map((item, index) => (
			<View key={item.row.ref}>
				{index > 0 ? <Hairline /> : null}
				<BoardRow
					item={item}
					variant={variant}
					moving={moving}
					connected={connected}
					usual={usual}
					hostLabel={hostLabel}
					hasDraft={draftRefs.has(item.row.ref)}
					now={now}
					onOpen={openSession}
				/>
			</View>
		));
	const band = (key: Exclude<Band, "idle">, moving: boolean) =>
		bands[key].length ? (
			<View key={key} onLayout={measure(key)}>
				<BandHeader text={`${BAND_HEADERS[key]} · ${bands[key].length}`} />
				{rows(bands[key], "signal", moving)}
			</View>
		) : null;
	const summary = liveSummary(bands);

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
							<View key={pin.id} onLayout={measure(`pin:${pin.id}`)}>
								<SectionRow
									glyph="pin.fill"
									text={`${pin.name} · ${pin.count}`}
									label={sectionLabel(pin.name, pin.count, "session")}
									onPress={() =>
										navigation.navigate("PinnedSection", { hubId, sectionId: pin.id, title: pin.name })
									}
								/>
							</View>
						))}
						{projects > 0 ? (
							<View testID="projects-section" onLayout={measure("projects")}>
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
					</>
				)}
			</ScrollView>
			<BoardToolbar state={state} fatal={fatal} newSessionDisabled={!connected} onNewSession={newSession} />
		</View>
	);
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

/** While any of the Board's reads has failed on a ready connection (Live,
 * Needs you, the pin catalog or the manifest; first read or later), rebind
 * the client after a backoff that grows with each failed attempt. Nothing
 * else would retry it while the Board stays in view: an idle fleet sends no
 * invalidations, and the controller retries failed reads only when it
 * resumes, on a focus change. The retry waits while another read is still
 * out, so a rebind never cancels a healthy read. Rebinding is the reconnect
 * path: the loaded rows stay on screen until the fresh reads land, and the
 * screen's load-more pages Live back out. A read that lands, or a new
 * connection, starts the count over; with no client (disconnected or out of
 * view) the hook holds its count and schedules nothing. */
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
			<View style={{ transform: [{ rotate: folded ? "0deg" : "90deg" }] }}>
				<SymbolView name="chevron.right" size={13 * scale} tintColor={palette.inkLow} />
			</View>
		</Pressable>
	);
}

/** A section after Live that opens its own screen until PR 3 brings it
 * inline. */
function SectionRow({ glyph, text, label, onPress }: { glyph?: SFSymbol; text: string; label: string; onPress: () => void }) {
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
			{glyph ? <SymbolView name={glyph} size={13 * scale} tintColor={palette.inkLow} /> : null}
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
