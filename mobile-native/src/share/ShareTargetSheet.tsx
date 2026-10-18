// Where a share goes (spec: share intent). One sheet, opened over whatever was
// in front: New session, the active hub's live sessions, or an older session
// found with the Board's own search. Choosing a session writes the shared text
// into its saved draft, so the composer holds it when the session opens; only
// after that write lands is the share dropped from the app group.
import { useEffect, useState, useSyncExternalStore } from "react";
import { Pressable, ScrollView, Text, TextInput, View } from "react-native";
import type { NavigationSessionSummary, SearchResult } from "@evener/appwire-client";
import { useConnection } from "../ConnectionProvider";
import type { DraftLibrary } from "../draftLibrary";
import { isReady } from "../connectionDisplay";
import { createBoardController } from "../board/boardData";
import { type SearchScope, createSearchController, searchResultMark, sessionResults } from "../board/boardSearch";
import { liveBands, stateWord } from "../board/attention";
import { BandHeader, Hairline } from "../board/BoardRow";
import { StateMark } from "../board/StateMark";
import { SymbolView } from "expo-symbols";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { placeSharedText } from "./placeShare";
import { type SharedPayload } from "./sharePayload";

export interface ShareTargetSheetProps {
	navigation: {
		navigate(name: string, params?: object): void;
		goBack(): void;
	};
	route: { params: { item: SharedPayload } };
	/** The app's one draft registry, so the sheet writes the same document the
	 * session screen will read. */
	drafts: DraftLibrary;
	/** The share is safely in a draft: the app drops its file and moves on. */
	onPlaced(item: SharedPayload): void;
	/** The share went with the New session sheet, which seeds the prompt and
	 * completes the share itself. The file stays until that seed lands. */
	onHandedOff?(item: SharedPayload): void;
}

/** The search field's height, as the Board's: never shorter than the 44pt
 * touch target. */
const FIELD_MIN_HEIGHT = 44;

export function ShareTargetSheet({ navigation, route, drafts, onPlaced, onHandedOff }: ShareTargetSheetProps) {
	const { item } = route.params;
	const { activeProfile, client, state, fatal } = useConnection();
	const { palette } = useColors();
	const scale = useTextScale();
	const [error, setError] = useState<string | null>(null);
	const connected = isReady(state) && activeProfile !== null;
	const hubId = activeProfile?.id ?? "";

	// The live sessions, read exactly as the Board reads them.
	const [board] = useState(() => createBoardController());
	useEffect(() => () => board.dispose(), [board]);
	useEffect(() => {
		board.setClient(connected ? client : null);
	}, [board, connected, client]);
	const boardSnapshot = useSyncExternalStore(board.subscribe, board.getSnapshot);

	const [search] = useState(createSearchController);
	useEffect(() => () => search.dispose(), [search]);
	useEffect(() => {
		search.setClient(connected ? client : null);
	}, [search, connected, client]);
	const searchSnapshot = useSyncExternalStore(search.subscribe, search.getSnapshot);
	const [scope, setScope] = useState<SearchScope>("all");

	const bands = liveBands(boardSnapshot.live.rows, boardSnapshot.needsYou.rows, () => true);
	const rows = [...bands.needsYou, ...bands.working, ...bands.idle];

	// A place that took the text: drop the share, then show the destination.
	function placed() {
		setError(null);
		onPlaced(item);
	}

	function openSession(ref: string, title: string) {
		const document = drafts.open({ hubId, sessionRef: ref });
		if (!placeSharedText(document, item.text)) {
			setError("Your draft couldn't be saved on this device, so the share is still here. Try again.");
			return;
		}
		placed();
		navigation.navigate("Conversation", { hubId, ref, title });
	}

	function startNew() {
		onHandedOff?.(item);
		navigation.navigate("NewSession", { hubId, hubName: activeProfile?.name ?? "", share: item });
	}

	function openResult(result: SearchResult) {
		openSession(result.ref, result.title);
	}

	return (
		<ScrollView
			style={{ flex: 1, backgroundColor: palette.canvas }}
			contentContainerStyle={{ paddingBottom: 24 }}
			keyboardShouldPersistTaps="handled"
		>
			<Text
				allowFontScaling={allowFontScaling}
				numberOfLines={4}
				style={{
					marginHorizontal: 16,
					marginTop: 16,
					fontSize: 15 * scale,
					lineHeight: 21 * scale,
					color: palette.inkMid,
				}}
			>
				{item.text}
			</Text>
			<Pressable
				testID="share-new-session"
				accessibilityRole="button"
				accessibilityLabel="New session"
				onPress={startNew}
				style={({ pressed }) => ({
					minHeight: 48,
					marginTop: 16,
					paddingHorizontal: 16,
					flexDirection: "row",
					alignItems: "center",
					columnGap: 10,
					backgroundColor: pressed ? palette.pressed : palette.page,
				})}
			>
				<View style={{ width: 28, alignItems: "center" }}>
					<SymbolView name="plus" size={16 * scale} tintColor={palette.accentInk} />
				</View>
				<Text
					allowFontScaling={allowFontScaling}
					style={{ fontSize: 17 * scale, fontWeight: "600", color: palette.accentInk }}
				>
					New session
				</Text>
			</Pressable>
			<Hairline />
			<View
				style={{
					minHeight: FIELD_MIN_HEIGHT,
					marginHorizontal: 16,
					marginVertical: 8,
					paddingHorizontal: 12,
					borderRadius: 10,
					justifyContent: "center",
					backgroundColor: palette.surface,
					borderWidth: 0.5,
					borderColor: palette.edge,
				}}
			>
				<TextInput
					accessibilityLabel="Search sessions"
					placeholder="Search sessions"
					autoCapitalize="none"
					allowFontScaling={allowFontScaling}
					onChangeText={search.setQuery}
					style={{ fontSize: 17 * scale, color: palette.inkHi }}
				/>
			</View>
			{error ? (
				<Text
					testID="share-error"
					allowFontScaling={allowFontScaling}
					style={{ marginHorizontal: 16, marginVertical: 8, fontSize: 14 * scale, color: palette.dangerInk }}
				>
					{error}
				</Text>
			) : null}
			{!connected && !fatal ? (
				<Text
					allowFontScaling={allowFontScaling}
					style={{ marginHorizontal: 16, marginVertical: 12, fontSize: 15 * scale, color: palette.inkMid }}
				>
					Connect to a hub to choose a session. The share waits until you do.
				</Text>
			) : null}
			{searchSnapshot.query ? (
				<SearchHits
					scope={scope}
					onScope={setScope}
					snapshot={searchSnapshot}
					connected={connected}
					onOpen={openResult}
				/>
			) : (
				<>
					{rows.length ? <BandHeader text="LIVE" /> : null}
					{rows.map((row) => (
						<LiveRow
							key={row.row.ref}
							row={row.row}
							state={row.state}
							onOpen={() => openSession(row.row.ref, row.row.title)}
						/>
					))}
				</>
			)}
		</ScrollView>
	);
}

function LiveRow({
	row,
	state,
	onOpen,
}: {
	row: NavigationSessionSummary;
	state: ReturnType<typeof liveBands>["needsYou"][number]["state"];
	onOpen(): void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={row.title}
			onPress={onOpen}
			style={({ pressed }) => ({
				minHeight: 64,
				paddingHorizontal: 16,
				paddingVertical: 8,
				flexDirection: "row",
				alignItems: "center",
				columnGap: 10,
				backgroundColor: pressed ? palette.pressed : palette.page,
			})}
		>
			<StateMark state={state} connected />
			<View style={{ flex: 1, minWidth: 0 }}>
				<Text
					allowFontScaling={allowFontScaling}
					numberOfLines={1}
					style={{ fontSize: 17 * scale, fontWeight: "600", color: palette.inkHi }}
				>
					{row.title || "Untitled session"}
				</Text>
				<Text
					allowFontScaling={allowFontScaling}
					numberOfLines={1}
					style={{ marginTop: 2, fontSize: 13 * scale, color: palette.inkLow }}
				>
					{[stateWord(state), row.project].filter(Boolean).join(" · ")}
				</Text>
			</View>
		</Pressable>
	);
}

function SearchHits({
	scope,
	onScope,
	snapshot,
	connected,
	onOpen,
}: {
	scope: SearchScope;
	onScope(scope: SearchScope): void;
	snapshot: ReturnType<ReturnType<typeof createSearchController>["getSnapshot"]>;
	connected: boolean;
	onOpen(result: SearchResult): void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	if (snapshot.failed)
		return (
			<Text style={{ marginHorizontal: 16, marginVertical: 12, fontSize: 15 * scale, color: palette.inkMid }}>
				Couldn't search this hub's sessions.
			</Text>
		);
	if (!snapshot.results) return null;
	const results = sessionResults(snapshot.results, scope);
	return (
		<>
			<View style={{ flexDirection: "row", columnGap: 8, paddingHorizontal: 16, paddingVertical: 8 }}>
				{(["all", "live"] as const).map((name) => (
					<Pressable
						key={name}
						accessibilityRole="button"
						accessibilityLabel={name === "all" ? "All" : "Live"}
						accessibilityState={{ selected: scope === name }}
						onPress={() => onScope(name)}
					>
						<Text
							style={{
								fontSize: 14 * scale,
								fontWeight: "600",
								color: scope === name ? palette.accentInk : palette.inkHi,
							}}
						>
							{name === "all" ? "All" : "Live"}
						</Text>
					</Pressable>
				))}
			</View>
			<BandHeader text={`SESSIONS · ${results.length}`} />
			{results.map((grouped) => (
				<SearchRow key={grouped.result.ref} result={grouped.result} connected={connected} onOpen={onOpen} />
			))}
		</>
	);
}

function SearchRow({
	result,
	connected,
	onOpen,
}: {
	result: SearchResult;
	connected: boolean;
	onOpen(result: SearchResult): void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={result.title}
			onPress={() => onOpen(result)}
			style={({ pressed }) => ({
				minHeight: 64,
				paddingHorizontal: 16,
				paddingVertical: 8,
				flexDirection: "row",
				alignItems: "center",
				columnGap: 10,
				backgroundColor: pressed ? palette.pressed : palette.page,
			})}
		>
			<StateMark state={searchResultMark(result)} connected={connected} />
			<View style={{ flex: 1, minWidth: 0 }}>
				<Text
					allowFontScaling={allowFontScaling}
					numberOfLines={1}
					style={{ fontSize: 17 * scale, fontWeight: "600", color: palette.inkHi }}
				>
					{result.title || "Untitled session"}
				</Text>
				{result.project ? (
					<Text
						allowFontScaling={allowFontScaling}
						numberOfLines={1}
						style={{ marginTop: 2, fontSize: 13 * scale, color: palette.inkLow }}
					>
						{result.project}
					</Text>
				) : null}
			</View>
		</Pressable>
	);
}
