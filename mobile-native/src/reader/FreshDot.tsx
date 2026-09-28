// The blue dot of a document that's new or changed since you last read it
// (spec 8.1, 8.2, 10.1): on the Files chip, a document chip and a Files row.
import { SymbolView } from "expo-symbols";
import { useColors } from "../ui";

export function FreshDot() {
	const { palette } = useColors();
	return <SymbolView name="circle.fill" size={8} tintColor={palette.accent} />;
}
