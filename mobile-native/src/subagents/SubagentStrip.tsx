import { View } from "react-native";
import type { Palette } from "../design/tokens";
import { useColors } from "../ui";
import type { SubagentTally } from "../session/sessionState";
import { type SubagentState, stripSegments } from "./subagentModel";

const HEIGHT = 6;

/** Each state's color in the strip, and in the chips' swatches, its legend. */
export function stateColors(palette: Palette): Record<SubagentState, string> {
	return { failed: palette.danger, running: palette.alive, done: palette.edge };
}
const GAP = 1;

/** The Subagents list's full-width strip (spec 9): failures, then running,
 * then done, sized by count. Nothing once every subagent is done. */
export function SubagentStrip({ tally, width }: { tally: SubagentTally; width: number }) {
	const { palette } = useColors();
	const segments = stripSegments(tally, width, GAP);
	if (segments.length === 0) return null;
	const color = stateColors(palette);
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
