// A shell job's detail (Jesse's ruling on shell jobs): the job as its
// coordinator's activity tree carries it (its command, how it's doing or how
// it ended, its exit code when that isn't 0, who started it) over the tail of
// its output, drawn as the step output viewer draws terminal text. The tree
// is the Activity list's own, so the job stays live, and the tail is read
// again when the tree shows the job changed and on a pace while a running
// job's detail is in front. Like the list, it never offers Refresh, and it
// offers no Stop.
import { useIsFocused } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useMemo } from "react";
import { Text, View } from "react-native";
import { AnsiOutputList } from "../AnsiOutputList";
import { fonts } from "../design/tokens";
import type { Routes } from "../screens";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { flattenJobs, type ShellJobRow, shellJobMeta } from "./subagentModel";
import { type ShellJobOutput, useShellJobOutput } from "./useShellJobOutput";
import { useFollowedSubagentTree } from "./useSubagentTree";

export function ShellJobScreen({ route }: NativeStackScreenProps<Routes, "ShellJob">) {
	const { hubId, jobId, ownerRef, coordinator } = route.params;
	const { palette } = useColors();
	const scale = useTextScale();
	const { snapshot, client } = useFollowedSubagentTree(hubId, coordinator.ref, coordinator.threadId);

	const row = useMemo(
		() =>
			snapshot.tree
				? flattenJobs(snapshot.tree).find((job) => job.id === jobId && job.job.ownerRef === ownerRef)
				: undefined,
		[snapshot.tree, jobId, ownerRef],
	);
	// Taken when the tree changes, so the screen runs no clock (ruling 7).
	// biome-ignore lint/correctness/useExhaustiveDependencies: a new snapshot is what moves the clock
	const now = useMemo(() => Date.now(), [snapshot]);
	const output = useShellJobOutput(client, row?.job ?? null, useIsFocused());

	const quiet = { fontSize: 15 * scale, lineHeight: 20 * scale, color: palette.inkMid };
	const gone = snapshot.tree
		? row === undefined
			? // A partial tree may hold the job in the part it couldn't read.
				snapshot.partial
				? "This job can't be listed right now."
				: "This job is no longer listed."
			: null
		: snapshot.failed
			? "This job couldn't be read right now."
			: null;
	const header = (
		<View style={{ paddingBottom: 12, gap: 12 }}>
			{row ? <JobFacts row={row} now={now} /> : null}
			{gone ? (
				<Text allowFontScaling={allowFontScaling} style={quiet}>
					{gone}
				</Text>
			) : null}
			{row ? <OutputNote output={output} /> : null}
		</View>
	);
	return (
		<View style={{ flex: 1, backgroundColor: palette.page }}>
			<AnsiOutputList
				text={output.status === "read" ? output.tail.tail : ""}
				header={header}
				contentContainerStyle={{ padding: 16, paddingBottom: 24 }}
			/>
		</View>
	);
}

/** The command, how it's doing, its exit code when that isn't 0, and who
 * started it. */
function JobFacts({ row, now }: { row: ShellJobRow; now: number }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const small = { fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkMid };
	const { command, exitCode } = row.job;
	return (
		<View style={{ gap: 4 }}>
			<Text
				allowFontScaling={allowFontScaling}
				selectable
				style={{ fontFamily: fonts.mono, fontSize: 15 * scale, lineHeight: 20 * scale, color: palette.inkHi }}
			>
				{command || row.title}
			</Text>
			<Text allowFontScaling={allowFontScaling} style={small}>
				{shellJobMeta(row, now)}
			</Text>
			{exitCode !== undefined && exitCode !== 0 ? (
				<Text allowFontScaling={allowFontScaling} style={small}>
					{`Exited ${exitCode}`}
				</Text>
			) : null}
			<Text allowFontScaling={allowFontScaling} style={small}>
				{`under ${row.owner}`}
			</Text>
		</View>
	);
}

/** What the output below is: nothing while it's read, why there's none, or
 * that it starts partway through the job's output. */
function OutputNote({ output }: { output: ShellJobOutput }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const note =
		output.status === "failed"
			? "The output couldn't be read right now."
			: output.status === "read" && output.tail.tail === ""
				? "No output."
				: output.status === "read" && output.tail.retainedStart > 0
					? "Showing the end of the output."
					: null;
	if (note === null) return null;
	return (
		<Text
			allowFontScaling={allowFontScaling}
			style={{ fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkLow }}
		>
			{note}
		</Text>
	);
}
