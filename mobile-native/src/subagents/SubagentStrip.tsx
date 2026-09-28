import { View } from "react-native";
import { useColors } from "../ui";
import type { SubagentTally } from "../session/sessionState";
import { stripSegments } from "./subagentModel";

const HEIGHT = 6;
const GAP = 1;

/** The Subagents list's full-width strip (spec 9): failures, then running,
 * then done, sized by count. Nothing once every subagent is done. */
export function SubagentStrip({ tally, width }: { tally: SubagentTally; width: number }) {
	const { palette } = useColors();
	const segments = stripSegments(tally, width, GAP);
	if (segments.length === 0) return null;
	const color = { failed: palette.danger, running: palette.alive, done: palette.edge };
	return (
		<View
			accessible
			accessibilityLabel={`${tally.failed} failed, ${tally.running} running, ${tally.done} done`}
			style={{ width, height: HEIGHT, borderRadius: HEIGHT / 2, overflow: "hidden", flexDirection: "row", gap: GAP }}
		>
			{segments.map((segment) => (
				<View key={segment.state} style={{ width: segment.width, height: HEIGHT, backgroundColor: color[segment.state] }} />
			))}
		</View>
	);
}
