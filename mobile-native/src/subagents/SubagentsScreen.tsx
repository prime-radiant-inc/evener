// Native activity renders loaded subtree pages and authoritative summary counts.
// The shared activity store owns reads, notifications and recovery.
import { useIsFocused } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { SymbolView } from "expo-symbols";
import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { FlatList, Pressable, Text, useWindowDimensions, View } from "react-native";
import { BandHeader } from "../board/BoardRow";
import { useConnection } from "../ConnectionProvider";
import { ChipStrip } from "../design/ChipStrip";
import { space } from "../design/tokens";
import { HubModels } from "../hubModels";
import type { Routes } from "../screens";
import { SearchField } from "../sheet/SearchField";
import { Toast, useToast } from "../Toast";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { type ActivityFilter, type ActivityListItem, activityListItems, activityListKey } from "./activityList";
import {
	flattenActivity,
	SEARCH_AFTER,
	type ShellJobRow,
	type SubagentRow,
	isSubagentRow,
	sameModel,
	summaryTally,
} from "./subagentModel";
import { stopRequests } from "./nativeStopRequests";
import { ShellJobRowView } from "./ShellJobRowView";
import { SubagentRowView } from "./SubagentRowView";
import { SubagentStrip, stateColors } from "./SubagentStrip";
import type { SubagentTree } from "./subagentTree";
import { useFollowedSubagentTree } from "./useSubagentTree";
import { haptic } from "../haptics";

export function SubagentsScreen({ route, navigation }: NativeStackScreenProps<Routes, "Subagents">) {
	const { hubId, ref, threadId, title } = route.params;
	const { palette } = useColors();
	const scale = useTextScale();
	const { width } = useWindowDimensions();
	const { tree, snapshot } = useFollowedSubagentTree(hubId, ref, threadId);

	const activity = useMemo(() => (snapshot.tree ? flattenActivity(snapshot.tree, title) : []), [snapshot.tree, title]);
	// The strip, stops and subagent screens are the subagents' own; the list,
	// its chips and its count hold the shell jobs too.
	const rows = useMemo(() => activity.filter(isSubagentRow), [activity]);
	const subagentTally = summaryTally(snapshot.summary?.delegates);
	const activityTally = summaryTally(snapshot.summary?.delegates, snapshot.summary?.jobs);
	// Taken when the tree changes, so the list runs no clock (ruling 7).
	// biome-ignore lint/correctness/useExhaustiveDependencies: a new snapshot is what moves the clock
	const now = useMemo(() => Date.now(), [snapshot]);

	const [filter, setFilter] = useState<ActivityFilter>("all");
	const [query, setQuery] = useState("");
	const [doneOpen, setDoneOpen] = useState(false);
	const [completedJobsOpen, setCompletedJobsOpen] = useState(false);
	// The hub's own ref for the coordinator, once a tree carries it, names
	// the coordinator's branch in what couldn't be listed.
	const coordinatorRef = snapshot.tree?.root.ref ?? ref;
	const items = useMemo(
		() =>
			activityListItems(activity, {
				filter,
				query,
				doneOpen,
				completedJobsOpen,
				missing: snapshot.missing,
				coordinator: { ref: coordinatorRef, title },
			}),
		[activity, filter, query, doneOpen, completedJobsOpen, snapshot.missing, coordinatorRef, title],
	);

	// The stops you asked for, on their rows, and settled against each new
	// tree while this list is in front, with a toast for each that stopped.
	// A subagent's screen pushed over the list settles them instead, so the
	// toast shows where you are, and once.
	const requests = stopRequests(hubId);
	const stopRevision = useSyncExternalStore(requests.subscribe, requests.getRevision);
	const focused = useIsFocused();
	const boundaryKeys = JSON.stringify(items.map(activityListKey));
	const [boundary, setBoundary] = useState<{ tree: SubagentTree; keys: string } | null>(null);
	const contentSize = useRef<{ width: number; height: number } | null>(null);
	const delegatePage = snapshot.pages?.delegates;
	const jobPage = snapshot.pages?.jobs;
	useEffect(() => {
		if (!focused || boundary?.tree !== tree || boundary.keys !== boundaryKeys) return;
		// A closed fold can consume a page without changing the native list's
		// height. Keep its observed edge demand, not a view-owned retry loop.
		for (const [resource, page] of [
			["delegates", delegatePage],
			["jobs", jobPage],
		] as const) {
			if (page?.hasMore && !page.loading && !page.error && !page.permanent) void tree.loadMore(resource);
		}
	}, [focused, boundary, boundaryKeys, tree, delegatePage, jobPage]);
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

	// A shell job's detail is its own screen, over this list.
	const openJob = useCallback(
		(row: ShellJobRow) =>
			navigation.push("ShellJob", {
				hubId,
				jobId: row.id,
				ownerRef: row.job.ownerRef,
				title: row.title,
				coordinator: { ref, threadId, title },
			}),
		[navigation, hubId, ref, threadId, title],
	);

	const count = activityTally ? String(activityTally.total) : "…";
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
		? activity.length === 0 && !snapshot.partial && snapshot.missing.length === 0
			? "No subagents or shell jobs yet."
			: null
		: snapshot.failed
			? "The activity couldn't be listed right now."
			: snapshot.unsupported
				? "This session can't list its activity."
				: snapshot.ended
					? "This session is shut down, so its activity can't be listed."
					: null;

	const renderItem = useCallback(
		({ item }: { item: ActivityListItem }) => {
			switch (item.kind) {
				case "section":
					return <BandHeader text={`${item.state.toUpperCase()} · ${item.count}`} />;
				case "doneFold":
				case "completedJobsFold": {
					const delegates = item.kind === "doneFold";
					const setOpen = delegates ? setDoneOpen : setCompletedJobsOpen;
					return (
						<HistoryFold
							label={delegates ? "Done" : "Completed"}
							count={item.count}
							open={item.open}
							onToggle={() => setOpen((open) => !open)}
						/>
					);
				}
				case "missing":
					return <MissingLine title={item.title} />;
				case "row":
					if (item.row.kind === "job") return <ShellJobRowView row={item.row} now={now} onOpen={openJob} />;
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
		[now, snapshot.coordinatorModel, modelName, openRow, openJob, noteFor],
	);

	const header = (
		<View style={{ paddingTop: 12, gap: 12 }}>
			<View style={{ paddingHorizontal: 16 }}>
				{subagentTally ? (
					<SubagentStrip tally={subagentTally} width={width - 32} />
				) : (
					<Text accessibilityLabel="Subagent counts unknown" style={{ color: palette.inkMid }}>
						Subagents …
					</Text>
				)}
			</View>
			{!activityTally || activityTally.total > 0 ? (
				// One row, as spec 9 draws it: past the phone's width it scrolls
				// sideways and fades at its trailing edge, as the Board's and the
				// Session's chip rows do.
				<ChipStrip testID="subagent-filters" onGlass={false}>
					<FilterChip
						label="All"
						count={activityTally?.total ?? "…"}
						selected={filter === "all"}
						onPress={() => setFilter("all")}
					/>
					{[
						{ id: "running" as const, label: "Running", count: activityTally?.running },
						{
							id: "done" as const,
							label: "Done",
							count: activityTally ? activityTally.failed + activityTally.done : undefined,
						},
					]
						.filter((state) => state.count === undefined || state.count > 0)
						.map((state) => (
							<FilterChip
								key={state.id}
								state={state.id}
								label={state.label}
								count={state.count ?? "…"}
								selected={filter === state.id}
								onPress={() => setFilter(state.id)}
							/>
						))}
				</ChipStrip>
			) : null}
			{/* Kept while it has words, so a list that shrinks never stays filtered with no way to clear it. */}
			{(activityTally?.total ?? activity.length) > SEARCH_AFTER || query !== "" ? (
				<View style={{ marginHorizontal: space.margin }}>
					<SearchField label="Filter activity" value={query} onChangeText={setQuery} />
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
				keyboardDismissMode="on-drag"
				onEndReached={() => setBoundary({ tree, keys: boundaryKeys })}
				onScroll={({ nativeEvent: { contentSize, contentOffset, layoutMeasurement } }) => {
					if (contentSize.height - contentOffset.y - layoutMeasurement.height > 2) setBoundary(null);
				}}
				onContentSizeChange={(width, height) => {
					const previous = contentSize.current;
					contentSize.current = { width, height };
					if (previous && (previous.width !== width || previous.height !== height)) setBoundary(null);
				}}
				keyExtractor={activityListKey}
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
				{`Activity · ${count}`}
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
	state?: "running" | "done";
	label: string;
	count: number | string;
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

function HistoryFold({
	label,
	count,
	open,
	onToggle,
}: {
	label: "Done" | "Completed";
	count: number;
	open: boolean;
	onToggle(): void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const text = `${label} · ${count}`;
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={text}
			accessibilityState={{ expanded: open }}
			onPress={onToggle}
			style={{ minHeight: 44, flexDirection: "row", alignItems: "center", gap: 6, paddingHorizontal: 16 }}
		>
			<Text
				allowFontScaling={allowFontScaling}
				style={{ fontSize: 15 * scale, lineHeight: 20 * scale, fontWeight: "600", color: palette.inkMid }}
			>
				{text}
			</Text>
			<SymbolView name={open ? "chevron.down" : "chevron.right"} size={13} tintColor={palette.inkLow} />
		</Pressable>
	);
}

function MissingLine({ title }: { title?: string }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Text
			allowFontScaling={allowFontScaling}
			style={{ padding: 16, fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkLow }}
		>
			{title === undefined ? "Some activity isn't listed." : `Some activity under “${title}” isn't listed.`}
		</Text>
	);
}

/** Three quiet rows until the first read lands: no spinner, no shimmer. */
function Skeleton() {
	const { palette } = useColors();
	return (
		<View accessible accessibilityLabel="Loading activity" style={{ gap: 8, paddingTop: 8 }}>
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
