// Until a session's conversation first loads, three quiet blocks stand in for
// it (spec 14): no spinner, no shimmer, no sentence about connecting.
import { View } from "react-native";
import { useColors } from "../ui";

const HEIGHTS = [64, 96, 48] as const;

export function TranscriptSkeleton() {
	const { palette } = useColors();
	return (
		<View accessible accessibilityLabel="Loading conversation" style={{ gap: 12 }}>
			{HEIGHTS.map((height) => (
				<View key={height} style={{ height, borderRadius: 12, backgroundColor: palette.inset }} />
			))}
		</View>
	);
}
