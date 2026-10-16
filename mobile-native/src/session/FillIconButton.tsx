// The composer's trailing slot draws one shape whichever control fills it
// (spec 8.5): a 36pt circle on a solid fill, a symbol centered in it, in a
// 44pt button whose VoiceOver label says what pressing it does. Send and Stop
// both use it, so the slot's geometry lives in one place.
import { type SFSymbol, SymbolView } from "expo-symbols";
import { View } from "react-native";
import { useColors } from "../ui";
import { SymbolButton } from "./SymbolButton";

export function FillIconButton({
	label,
	disabled,
	onPress,
	symbol,
	size,
	fill,
}: {
	label: string;
	disabled: boolean;
	onPress(): void;
	symbol: SFSymbol;
	/** The symbol's point size inside the 36pt circle. */
	size: number;
	/** Which palette color fills the circle; the symbol rides on `onFill`. */
	fill: "accentFill" | "danger";
}) {
	const { palette } = useColors();
	return (
		<SymbolButton label={label} disabled={disabled} onPress={onPress}>
			<View
				style={{
					width: 36,
					height: 36,
					borderRadius: 18,
					alignItems: "center",
					justifyContent: "center",
					backgroundColor: palette[fill],
				}}
			>
				<SymbolView name={symbol} tintColor={palette.onFill} size={size} />
			</View>
		</SymbolButton>
	);
}
