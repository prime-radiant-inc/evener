import { View } from "react-native";
import { useColors } from "../ui";
import { PULSE_BARS, pulseBars } from "./pulse";

const WIDTH = 22;
const HEIGHT = 15;

/** The Board's one piece of motion (spec 16.4). It is still until the hub
 * reports per-minute activity (S5): motion is evidence. */
export function PulseMeter({
	perMinute,
	tone = "alive",
}: {
	perMinute?: readonly number[];
	tone?: "alive" | "attention" | "gray";
}) {
	const { palette } = useColors();
	const color = tone === "gray" ? palette.inkLow : palette[tone];
	return (
		<View
			style={{ width: WIDTH, height: HEIGHT, flexDirection: "row", alignItems: "flex-end", gap: 1 }}
			accessibilityElementsHidden
			importantForAccessibility="no-hide-descendants"
		>
			{pulseBars(perMinute).map((bar, index) => (
				<View
					key={`minutes-ago-${PULSE_BARS - 1 - index}`}
					style={{ flex: 1, height: Math.max(1, bar.height * HEIGHT), backgroundColor: color, opacity: bar.opacity }}
				/>
			))}
		</View>
	);
}
