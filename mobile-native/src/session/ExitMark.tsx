// The small x that marks a call whose only problem was a nonzero exit
// (the shared isExitOnly predicate): the process exited nonzero, but the call
// itself did not fail - no error text, no failed/interrupted status. Jesse,
// 2026-10-16: "don't call out failed in lists of tool calls just for nonzero
// exits ... show a small x inline on the call." So this is a plain x in
// attention ink - visible, not the danger octagon that means a real failure.
//
// Decorative: the call's own line names the exit (its state word), so a
// second accessible name here would only repeat it.
import { SymbolView } from "expo-symbols";
import { useColors, useTextScale } from "../ui";

export function ExitMark({ size }: { size?: number }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return <SymbolView name="xmark" tintColor={palette.attentionInk} size={size ?? 13 * scale} />;
}
