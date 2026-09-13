import { View } from "react-native";
import type { MobileUsage } from "../../mobile/src/conversation/model";
import { Copy } from "./ui";

export function TranscriptUsage({ usage }: { usage: MobileUsage }) {
	const tokens = [
		["Input", usage.inputTokens],
		["Output", usage.outputTokens],
		["Cached", usage.cacheReadTokens],
		["Total", usage.totalTokens],
	] as const;
	if (
		tokens.every(([, value]) => value === undefined) &&
		usage.cost === undefined
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
			{usage.cost !== undefined ? (
				<Copy muted>Estimated cost: {usage.cost}</Copy>
			) : null}
		</View>
	);
}
