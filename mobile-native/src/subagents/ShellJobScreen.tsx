// A shell job's detail (Jesse's ruling on shell jobs): the job as its
// coordinator's activity tree carries it (its command, how it's doing or how
// it ended, its exit code when that isn't 0, who started it) over the tail of
// its output, drawn as the step output viewer draws terminal text. The tree
// is the Jobs list's own, so the job stays live, and the tail is read
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
import { flattenJobs, type ShellJobRow, shellJobMeta, shellJobOwner } from "./subagentModel";
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
				? flattenJobs(snapshot.tree, coordinator.title).find((job) => job.id === jobId && job.job.ownerRef === ownerRef)
				: undefined,
		[snapshot.tree, coordinator.title, jobId, ownerRef],
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
				? "partial"
				: "unlisted"
			: null
		: snapshot.failed
			? "failed"
			: null;
	const header = (
		<View style={{ paddingBottom: 12, gap: 12 }}>
			{row ? <JobFacts row={row} now={now} /> : null}
			{gone ? (
				<Text testID={`job-gone:${gone}`} allowFontScaling={allowFontScaling} style={quiet}>
					{GONE_WORDS[gone]}
				</Text>
			) : null}
			{row ? <OutputNote output={output} /> : null}
		</View>
	);
	return (
		<View style={{ flex: 1, backgroundColor: palette.page }}>
			<AnsiOutputList
				text={output.status === "read" ? output.text : ""}
				header={header}
				contentContainerStyle={{ padding: 16, paddingBottom: 24 }}
			/>
		</View>
	);
}

/** Why the job isn't shown: the tree couldn't all be read, it no longer
 * lists the job, or it couldn't be read at all. */
const GONE_WORDS = {
	partial: "This job can't be listed right now.",
	unlisted: "This job is no longer listed.",
	failed: "This job couldn't be read right now.",
} as const;

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
				<Text testID="job-exit-code" allowFontScaling={allowFontScaling} style={small}>
					{`Exited ${exitCode}`}
				</Text>
			) : null}
			<Text allowFontScaling={allowFontScaling} style={small}>
				{shellJobOwner(row)}
			</Text>
		</View>
	);
}

const OUTPUT_NOTE_WORDS = {
	failed: "The output couldn't be read right now.",
	empty: "No output.",
	tail: "Showing the end of the output.",
} as const;

/** What the output below is: nothing while it's read, why there's none, or
 * that it starts partway through the job's output. */
function OutputNote({ output }: { output: ShellJobOutput }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const note =
		output.status === "failed"
			? "failed"
			: output.status === "read" && output.text === ""
				? "empty"
				: output.status === "read" && output.page.offsetBytes > 0
					? "tail"
					: null;
	if (note === null) return null;
	return (
		<Text
			testID={`output-note:${note}`}
			allowFontScaling={allowFontScaling}
			style={{ fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkLow }}
		>
			{OUTPUT_NOTE_WORDS[note]}
		</Text>
	);
}
