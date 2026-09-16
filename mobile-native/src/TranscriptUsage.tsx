import { View } from "react-native";
import type { SessionAccounting } from "./transcriptPresentation";
import { Copy } from "./ui";

export function TranscriptUsage({ usage, cost }: SessionAccounting) {
	const tokens = [
		["Input", usage?.inputTokens],
		["Output", usage?.outputTokens],
		["Cached", usage?.cacheReadTokens],
		["Total", usage?.totalTokens],
	] as const;
	if (tokens.every(([, value]) => value === undefined) && cost === null)
		return null;
	return (
		<View style={{ gap: 4, paddingTop: 16 }}>
			{tokens
				.filter(([, value]) => value !== undefined)
				.map(([label, value]) => (
					<Copy key={label} muted>
						{label}: {value?.toLocaleString()} tokens
					</Copy>
				))}
			{cost !== null ? <Copy muted>Estimated cost: {cost}</Copy> : null}
		</View>
	);
}
