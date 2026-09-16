import { View } from "react-native";
import type { SessionAccounting } from "./transcriptPresentation";
import { Copy } from "./ui";

export function TranscriptUsage({ usage }: { usage: SessionAccounting }) {
	const tokens = [
		["Input", usage.usage?.inputTokens],
		["Output", usage.usage?.outputTokens],
		["Cached", usage.usage?.cacheReadTokens],
		["Total", usage.usage?.totalTokens],
	] as const;
	if (
		tokens.every(([, value]) => value === undefined) &&
		usage.cost === null
	)
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
			{usage.cost !== null ? (
				<Copy muted>Estimated cost: {usage.cost}</Copy>
			) : null}
		</View>
	);
}
