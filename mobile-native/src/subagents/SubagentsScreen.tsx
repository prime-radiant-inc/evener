// The Subagents list (spec 9): a coordinator's subagents, read whole from its
// activity tree. A strip and chips say how many are failed, running and done;
// the list shows failures first, then what's running, then what's done folded
// away. It reads again on its own (on focus, on reconnect, on the tree's
// notifications) and never offers Retry, Refresh or Reconnect.
import { useFocusEffect, useIsFocused } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { SymbolView } from "expo-symbols";
import { useCallback, useEffect, useMemo, useState, useSyncExternalStore } from "react";
import { FlatList, Pressable, Text, useWindowDimensions, View } from "react-native";
import { BandHeader } from "../board/BoardRow";
import { useConnection } from "../ConnectionProvider";
import { space } from "../design/tokens";
import { HubModels } from "../hubModels";
import type { Routes } from "../screens";
import { SearchField } from "../sheet/SearchField";
import { Toast, useToast } from "../Toast";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { type SubagentFilter, type SubagentListItem, subagentListItems, subagentListKey } from "./subagentList";
import {
	countLabel,
	flattenSubagents,
	SEARCH_AFTER,
	STATE_ORDER,
	type SubagentRow,
	type SubagentState,
	sameModel,
	subagentStateWord,
	tallySubagents,
} from "./subagentModel";
import { stopRequests } from "./nativeStopRequests";
import { SubagentRowView } from "./SubagentRowView";
import { SubagentStrip, stateColors } from "./SubagentStrip";
import { useSubagentTree } from "./useSubagentTree";
import { haptic } from "../haptics";

export function SubagentsScreen({ route, navigation }: NativeStackScreenProps<Routes, "Subagents">) {
	const { hubId, ref, threadId, title } = route.params;
	const { palette } = useColors();
	const scale = useTextScale();
	const { width } = useWindowDimensions();
	const { tree, snapshot } = useSubagentTree(hubId, ref, threadId);

	// Following the coordinator each time this screen comes into focus keeps
	// its tree notifications coming (ruling 9). A new client (a reconnect, or
	// a hub switch that never leaves ready) has no subscription, so the screen
	// in front follows again on each one. useSubagentTree's effect, declared
	// above, has already handed the tree that client.
	const { state, client, activeProfile } = useConnection();
	const followed = state === "ready" && activeProfile?.id === hubId ? client : null;
	useFocusEffect(
		useCallback(() => {
			if (followed) void tree.follow();
		}, [tree, followed]),
	);

	const rows = useMemo(() => (snapshot.tree ? flattenSubagents(snapshot.tree) : []), [snapshot.tree]);
	const tally = useMemo(() => tallySubagents(rows), [rows]);
	// Taken when the tree changes, so the list runs no clock (ruling 7).
	// biome-ignore lint/correctness/useExhaustiveDependencies: a new snapshot is what moves the clock
	const now = useMemo(() => Date.now(), [snapshot]);

	const [filter, setFilter] = useState<SubagentFilter>("all");
	const [query, setQuery] = useState("");
	const [doneOpen, setDoneOpen] = useState(false);
	const items = useMemo(
		() => subagentListItems(rows, { filter, query, doneOpen, missing: snapshot.missing }),
		[rows, filter, query, doneOpen, snapshot.missing],
	);

	// The stops you asked for, on their rows, and settled against each new
	// tree while this list is in front, with a toast for each that stopped.
	// A subagent's screen pushed over the list settles them instead, so the
	// toast shows where you are, and once.
	const requests = stopRequests(hubId);
	const stopRevision = useSyncExternalStore(requests.subscribe, requests.getRevision);
	const focused = useIsFocused();
	const toast = useToast();
	const showToast = toast.show;
	useEffect(() => {
		if (!focused || rows.length === 0) return;
		for (const stopped of requests.reconcile(ref, rows)) showToast({ text: `“${stopped.title}” stopped` });
		// A request recorded after the tree already shows the stop settles too.
	}, [focused, rows, requests, ref, showToast, stopRevision]);
	const noteFor = useCallback(
		(row: SubagentRow) => {
			const view = requests.view(row);
			if (view === "stopped") return "Stopped at your request";
			if (view === "requested") return requests.direct(row) ? "Stop requested" : "Stop requested from the coordinator";
			return undefined;
		},
		// biome-ignore lint/correctness/useExhaustiveDependencies: the revision says the requests changed
		[requests, stopRevision],
	);

	const modelName = useModelNames(hubId);
	// A subagent's screen is its own session (ruling 30), over this list.
	const openRow = useCallback(
		(row: SubagentRow) =>
			navigation.push("Subagent", { hubId, ref: row.ref, title: row.title, coordinator: { ref, threadId, title } }),
		[navigation, hubId, ref, threadId, title],
	);

	const count = countLabel(tally.total, snapshot.partial);
	useEffect(() => {
		navigation.setOptions({ headerTitle: () => <HeaderTitle count={count} title={title} /> });
	}, [navigation, count, title]);

	const quiet = {
		color: palette.inkMid,
		fontSize: 15 * scale,
		lineHeight: 20 * scale,
		paddingHorizontal: 16,
		paddingTop: 16,
	};
	const notice = snapshot.tree
		? null
		: snapshot.failed
			? "The subagents couldn't be listed right now."
			: snapshot.unsupported
				? "This session can't list its subagents."
				: snapshot.ended
					? "This session is shut down, so its subagents can't be listed."
					: null;

	const renderItem = useCallback(
		({ item }: { item: SubagentListItem }) => {
			switch (item.kind) {
				case "section":
					return <BandHeader text={`${subagentStateWord(item.state).toUpperCase()} · ${item.count}`} />;
				case "doneFold":
					return <DoneFold count={item.count} open={item.open} onToggle={() => setDoneOpen((open) => !open)} />;
				case "missing":
					return <MissingLine title={item.title} />;
				case "row":
					return (
						<SubagentRowView
							row={item.row}
							now={now}
							coordinatorModel={snapshot.coordinatorModel}
							modelName={modelName}
							note={noteFor(item.row)}
							onOpen={openRow}
						/>
					);
			}
		},
		[now, snapshot.coordinatorModel, modelName, openRow, noteFor],
	);

	const header = (
		<View style={{ paddingTop: 12, gap: 12 }}>
			<View style={{ paddingHorizontal: 16 }}>
				<SubagentStrip tally={tally} width={width - 32} />
			</View>
			{tally.total > 0 ? (
				<View style={{ flexDirection: "row", flexWrap: "wrap", gap: 8, paddingHorizontal: 16 }}>
					<FilterChip label="All" count={tally.total} selected={filter === "all"} onPress={() => setFilter("all")} />
					{STATE_ORDER.filter((state) => tally[state] > 0).map((state) => (
						<FilterChip
							key={state}
							state={state}
							label={subagentStateWord(state)}
							count={tally[state]}
							selected={filter === state}
							onPress={() => setFilter(state)}
						/>
					))}
				</View>
			) : null}
			{/* Kept while it has words, so a list that shrinks never stays filtered with no way to clear it. */}
			{tally.total > SEARCH_AFTER || query !== "" ? (
				<View style={{ marginHorizontal: space.margin }}>
					<SearchField label="Filter subagents" value={query} onChangeText={setQuery} />
				</View>
			) : null}
			{notice ? (
				<Text allowFontScaling={allowFontScaling} style={quiet}>
					{notice}
				</Text>
			) : null}
			{!snapshot.tree && !notice ? <Skeleton /> : null}
		</View>
	);

	return (
		<View style={{ flex: 1, backgroundColor: palette.page }}>
			<FlatList
				data={items}
				keyExtractor={subagentListKey}
				renderItem={renderItem}
				ListHeaderComponent={header}
				initialNumToRender={20}
				windowSize={7}
				contentContainerStyle={{ paddingBottom: 24 }}
			/>
			<View pointerEvents="box-none" style={{ position: "absolute", left: 0, right: 0, bottom: 16 }}>
				<Toast toast={toast.toast} dismiss={toast.dismiss} />
			</View>
		</View>
	);
}

/** Display names from the hub's model catalog, read once while connected. */
function useModelNames(hubId: string): (model: string) => string {
	const { client, state, activeProfile } = useConnection();
	const connected = state === "ready" && activeProfile?.id === hubId;
	const models = useMemo(() => new HubModels(connected ? client : null), [connected, client]);
	useEffect(() => {
		void models.refresh();
		return () => models.dispose();
	}, [models]);
	const { catalog } = useSyncExternalStore(models.subscribe, models.getSnapshot);
	return useCallback(
		(model: string) => catalog?.models.find((entry) => sameModel(entry.model, model))?.displayName ?? model,
		[catalog],
	);
}

function HeaderTitle({ count, title }: { count: string; title: string }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View style={{ alignItems: "center" }}>
			<Text
				allowFontScaling={allowFontScaling}
				numberOfLines={1}
				style={{ color: palette.inkHi, fontSize: 15 * scale, lineHeight: 20 * scale, fontWeight: "600" }}
			>
				{`Subagents · ${count}`}
			</Text>
			<Text
				allowFontScaling={allowFontScaling}
				numberOfLines={1}
				ellipsizeMode="tail"
				style={{ color: palette.inkMid, fontSize: 13 * scale, lineHeight: 18 * scale }}
			>
				{title}
			</Text>
		</View>
	);
}

/** A filter chip, and the strip's legend: its state's swatch, its word and
 * its count. */
function FilterChip({
	state,
	label,
	count,
	selected,
	onPress,
}: {
	state?: SubagentState;
	label: string;
	count: number;
	selected: boolean;
	onPress(): void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={`${label}, ${count}`}
			accessibilityState={{ selected }}
			onPress={() => {
				haptic("selection");
				onPress();
			}}
			hitSlop={{ top: 6, bottom: 6 }}
			style={{
				height: 32,
				flexDirection: "row",
				alignItems: "center",
				gap: 6,
				paddingHorizontal: 12,
				borderRadius: 16,
				backgroundColor: selected ? palette.accentBg : palette.canvas,
			}}
		>
			{state ? (
				<View style={{ width: 8, height: 8, borderRadius: 2, backgroundColor: stateColors(palette)[state] }} />
			) : null}
			<Text
				allowFontScaling={allowFontScaling}
				style={{ fontSize: 15 * scale, lineHeight: 20 * scale, color: selected ? palette.accentInk : palette.inkHi }}
			>
				{`${label} `}
				<Text style={{ color: palette.inkMid, fontVariant: ["tabular-nums"] }}>{count}</Text>
			</Text>
		</Pressable>
	);
}

function DoneFold({ count, open, onToggle }: { count: number; open: boolean; onToggle(): void }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const label = `Done · ${count}`;
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={label}
			accessibilityState={{ expanded: open }}
			onPress={onToggle}
			style={{ minHeight: 44, flexDirection: "row", alignItems: "center", gap: 6, paddingHorizontal: 16 }}
		>
			<Text
				allowFontScaling={allowFontScaling}
				style={{ fontSize: 15 * scale, lineHeight: 20 * scale, fontWeight: "600", color: palette.inkMid }}
			>
				{label}
			</Text>
			<SymbolView name={open ? "chevron.down" : "chevron.right"} size={13} tintColor={palette.inkLow} />
		</Pressable>
	);
}

function MissingLine({ title }: { title: string }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Text
			allowFontScaling={allowFontScaling}
			style={{ padding: 16, fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkLow }}
		>
			{`Some subagents under “${title}” aren't listed.`}
		</Text>
	);
}

/** Three quiet rows until the first read lands: no spinner, no shimmer. */
function Skeleton() {
	const { palette } = useColors();
	return (
		<View accessible accessibilityLabel="Loading subagents" style={{ gap: 8, paddingTop: 8 }}>
			{[0, 1, 2].map((index) => (
				<View
					key={index}
					testID="subagent-skeleton"
					style={{ height: 64, marginHorizontal: 16, borderRadius: 12, backgroundColor: palette.inset }}
				/>
			))}
		</View>
	);
}
