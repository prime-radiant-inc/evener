import { View } from "react-native";
import { usageRows, type SessionAccounting } from "./transcriptPresentation";
import { Copy } from "./ui";

export function TranscriptUsage({ usage, cost }: SessionAccounting) {
	const rows = usageRows(usage);
	if (rows.length === 0 && cost === null) return null;
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
