import { humanizeState, type NavigationSessionSummary } from "@evener/appwire-client";
import { useFocusEffect } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { type SFSymbol, SymbolView } from "expo-symbols";
import { type ReactNode, useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import {
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
import { Action, Copy, styles, useColors } from "../ui";
import { type Band, type ClassifiedRow, liveBands, liveSummary, summaryText, usualPlace } from "./attention";
import { BoardRow } from "./BoardRow";
import { BoardToolbar } from "./BoardToolbar";
import { type BoardController, type BoardSnapshot, createBoardController } from "./boardData";
import { foldedSections, seenMarkers } from "./nativeBoardMemory";
import { PulseMeter } from "./PulseMeter";

type Props = NativeStackScreenProps<Routes, "Sessions">;
type Navigation = Props["navigation"];

const SEARCH_PAGE_SIZE = 50;
const MINUTE = 60_000;
/** Rows are drawn without separators; the list draws hairlines inset to the
 * title (16 padding + 28 mark + 10 gap). */
const TITLE_INSET = 54;
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
	useLiveReadRetry(board, connected ? client : null, snapshot.live);

	const bands = useMemo(
		() => liveBands(snapshot.live.rows, snapshot.needsYou.rows, (row) => markers.isSeen(row)),
		// seenRevision re-runs isSeen after a mark or first run.
		[snapshot.live.rows, snapshot.needsYou.rows, markers, seenRevision],
	);
	useFirstRun(board, markers, snapshot);

	const usual = useMemo(() => usualPlace(snapshot.live.rows), [snapshot.live.rows]);
	const sources = snapshot.manifest?.sources;
	const hostLabel = useMemo(() => {
		const labels = new Map((sources ?? []).map((source) => [source.id, source.label]));
		return (hostId: string) => labels.get(hostId) ?? hostId;
	}, [sources]);

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
	const pins = snapshot.pins.rows.filter((pin) => pin.count > 0);
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
	for (const pin of pins)
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
				{index > 0 ? <Hairline inset={TITLE_INSET} /> : null}
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
			{searchOpen ? <SearchField search={search} connected={connected} /> : null}
			{search.active ? (
				<SearchResults search={search} connected={connected} hubId={hubId} navigation={navigation} />
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
function useFirstRun(
	board: BoardController,
	markers: ReturnType<typeof seenMarkers>,
	snapshot: ReturnType<BoardController["getSnapshot"]>,
) {
	useEffect(() => {
		if (markers.adopted) return;
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
	}, [board, markers, snapshot]);
}

/** While a Live read has failed on a ready connection, first page or later,
 * rebind the client after a backoff that grows with each failed attempt.
 * Nothing else would retry it: an idle fleet sends no invalidations, and
 * resuming re-reads only stale pages. Rebinding is the reconnect path: the
 * loaded rows stay on screen until the fresh reads land, and the screen's
 * load-more pages Live back out. A read that lands, or a new connection,
 * starts the count over. */
function useLiveReadRetry(
	board: BoardController,
	client: ConversationClientLike | null,
	live: Pick<BoardSnapshot["live"], "error" | "loading">,
) {
	const [retries, setRetries] = useState({ client, count: 0 });
	const count = retries.client === client ? retries.count : 0;
	const failed = live.error !== null;
	// A retry in flight shows no error while it loads (the controller gives
	// retained rows the fresh read's status), so only a read that finished
	// without one counts as success.
	const succeeded = !failed && !live.loading;
	useEffect(() => {
		if (succeeded && count > 0) setRetries({ client, count: 0 });
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
		const switchHub = () => navigation.navigate("Hubs");
		navigation.setOptions({
			title: "",
			unstable_headerLeftItems: () => [
				{
					type: "menu",
					label: hubName,
					accessibilityLabel: `${hubName}, hub menu`,
					icon: { type: "sfSymbol", name: "chevron.down" },
					menu: {
						items: [
							{
								type: "action",
								label: "Hub settings",
								disabled: !connected,
								onPress: () => navigation.navigate("HubSettings", { hubId }),
							},
							{ type: "action", label: "Switch hub", onPress: switchHub },
						],
					},
				},
			],
			unstable_headerRightItems: () => [
				{
					type: "button",
					label: "Search",
					accessibilityLabel: "Search sessions",
					icon: { type: "sfSymbol", name: "magnifyingglass" },
					onPress: toggleSearch,
				},
			],
			headerLeft: () => (
				<Action label={`${hubName}, switch hub`} onPress={switchHub}>
					{hubName}
				</Action>
			),
			headerRight: () => (
				<Action label="Search sessions" onPress={toggleSearch}>
					{fontScale > 1.4 ? "Find" : "Search"}
				</Action>
			),
		});
	}, [navigation, hubId, hubName, connected, toggleSearch, fontScale]);
}

function useTextScale() {
	const { fontScale } = useWindowDimensions();
	return Platform.OS === "ios" ? fontScale : 1;
}

function Hairline({ inset = 0 }: { inset?: number }) {
	const { palette } = useColors();
	return <View style={{ height: 0.5, marginLeft: inset, backgroundColor: palette.edge }} />;
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
	summary: NonNullable<ReturnType<typeof liveSummary>>;
	connected: boolean;
	onJump: (band: Band) => void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const entries = (["needsYou", "finished", "working", "idle"] as const).filter((band) => summary[band] > 0);
	return (
		<View
			style={{
				flexDirection: "row",
				flexWrap: "wrap",
				alignItems: "center",
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
				fontSize: 13 * scale,
				fontWeight: "600",
				letterSpacing: 0.4,
				color: palette.inkMid,
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
	hubId,
	navigation,
}: {
	search: BoardSearch;
	connected: boolean;
	hubId: string;
	navigation: Navigation;
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
						onPress={() => navigation.navigate("Conversation", { hubId, ref: item.ref, title: item.title })}
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
