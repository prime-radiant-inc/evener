// The Subagents list (spec 9): a coordinator's subagents, read whole from its
// activity tree. A strip and chips say how many are failed, running and done;
// the list shows failures first, then what's running, then what's done folded
// away. It reads again on its own (on focus, on reconnect, on the tree's
// notifications) and never offers Retry, Refresh or Reconnect.
import { useFocusEffect } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { SymbolView } from "expo-symbols";
import { useCallback, useEffect, useMemo, useState, useSyncExternalStore } from "react";
import { FlatList, Pressable, Text, TextInput, useWindowDimensions, View } from "react-native";
import { BandHeader } from "../board/BoardRow";
import { useConnection } from "../ConnectionProvider";
import { HubModels } from "../hubModels";
import type { Routes } from "../screens";
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
import { SubagentRowView } from "./SubagentRowView";
import { SubagentStrip, stateColors } from "./SubagentStrip";
import { useSubagentTree } from "./useSubagentTree";

export function SubagentsScreen({ route, navigation }: NativeStackScreenProps<Routes, "Subagents">) {
	const { hubId, ref, threadId, title } = route.params;
	const { palette } = useColors();
	const scale = useTextScale();
	const { width } = useWindowDimensions();
	const { tree, snapshot } = useSubagentTree(hubId, ref, threadId);

	// Following the coordinator each time this screen comes into focus keeps
	// its tree notifications coming (ruling 9). A reconnect loses the
	// connection's subscription, so the screen in front follows again then too.
	const { state } = useConnection();
	const ready = state === "ready";
	useFocusEffect(
		useCallback(() => {
			if (ready) void tree.follow();
		}, [tree, ready]),
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

	const modelName = useModelNames(hubId);
	// A subagent's screen is its session (ruling 30): the session route until
	// PR 3's "Subagent" screen, which adds the coordinator's stop controls.
	const openRow = useCallback(
		(row: SubagentRow) => navigation.navigate("Conversation", { hubId, ref: row.ref, title: row.title }),
		[navigation, hubId],
	);

	const count = countLabel(tally.total, snapshot.partial);
	useEffect(() => {
		navigation.setOptions({ headerTitle: () => <HeaderTitle count={count} title={title} /> });
	}, [navigation, count, title]);

	const quiet = { color: palette.inkMid, fontSize: 15 * scale, lineHeight: 20 * scale, paddingHorizontal: 16, paddingTop: 16 };
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
							onOpen={openRow}
						/>
					);
			}
		},
		[now, snapshot.coordinatorModel, modelName, openRow],
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
			{tally.total > SEARCH_AFTER ? <SearchField query={query} onChange={setQuery} /> : null}
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
			onPress={onPress}
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
			{state ? <View style={{ width: 8, height: 8, borderRadius: 2, backgroundColor: stateColors(palette)[state] }} /> : null}
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

function SearchField({ query, onChange }: { query: string; onChange(query: string): void }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View
			style={{
				marginHorizontal: 16,
				minHeight: 36,
				flexDirection: "row",
				alignItems: "center",
				gap: 6,
				paddingHorizontal: 10,
				borderRadius: 10,
				backgroundColor: palette.inset,
			}}
		>
			<SymbolView name="magnifyingglass" size={15} tintColor={palette.inkLow} />
			<TextInput
				accessibilityLabel="Filter subagents"
				placeholder="Filter subagents"
				placeholderTextColor={palette.inkLow}
				value={query}
				onChangeText={onChange}
				autoCorrect={false}
				allowFontScaling={allowFontScaling}
				style={{ flex: 1, color: palette.inkHi, fontSize: 17 * scale, paddingVertical: 8 }}
			/>
			{query ? (
				<Pressable
					accessibilityRole="button"
					accessibilityLabel="Clear filter"
					onPress={() => onChange("")}
					hitSlop={10}
				>
					<SymbolView name="xmark.circle.fill" size={15} tintColor={palette.inkLow} />
				</Pressable>
			) : null}
		</View>
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
