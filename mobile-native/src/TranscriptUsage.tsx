import { View } from "react-native";
import { hasUsageLines, type SessionAccounting, usageRows } from "./transcriptPresentation";
import { Copy } from "./ui";

export function TranscriptUsage({ derived, cumulative, cost }: SessionAccounting) {
	if (!hasUsageLines({ derived, cumulative, cost })) return null;
	const rows = usageRows({ derived, cumulative });
	return (
		<View style={{ gap: 4, paddingTop: 16 }}>
			{rows.map(({ label, value, unit }) => (
				<Copy key={label} muted>
					{label}: {value.toLocaleString()} {unit}
				</Copy>
			))}
			{cost !== null ? <Copy muted>Estimated cost: {cost}</Copy> : null}
		</View>
	);
}
