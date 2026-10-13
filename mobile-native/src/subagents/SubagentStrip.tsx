import { View } from "react-native";
import type { Palette } from "../design/tokens";
import { useColors } from "../ui";
import type { SubagentTally } from "../session/sessionState";
import { type StripSegment, stripSegments } from "./subagentModel";

const HEIGHT = 6;

/** Each state's color in the strip, and in the chips' swatches, its legend. */
export function stateColors(palette: Palette): Record<StripSegment["state"], string> {
	return { running: palette.alive, done: palette.edge };
}
const GAP = 1;

/** Active delegates followed by quiet terminal history. */
export function SubagentStrip({ tally, width }: { tally: SubagentTally; width: number }) {
	const { palette } = useColors();
	const segments = stripSegments(tally, width, GAP);
	if (segments.length === 0) return null;
	const color = stateColors(palette);
	return (
		<View
			accessible
			accessibilityLabel={`${tally.running} running, ${tally.failed + tally.done} done`}
			style={{ width, height: HEIGHT, borderRadius: HEIGHT / 2, overflow: "hidden", flexDirection: "row", gap: GAP }}
		>
			{segments.map((segment) => (
				<View
					key={segment.state}
					style={{ width: segment.width, height: HEIGHT, backgroundColor: color[segment.state] }}
				/>
			))}
		</View>
	);
}
