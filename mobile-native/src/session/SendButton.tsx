// The composer's one Send (spec 8.5): a paper airplane on the accent fill in
// a 44pt button, labeled for VoiceOver with what pressing it does. The
// composer and the Review sheet both send with it.
import { SymbolView } from "expo-symbols";
import { View } from "react-native";
import { useColors } from "../ui";
import { SymbolButton } from "./SymbolButton";

export function SendButton({ label, disabled, onPress }: { label: string; disabled: boolean; onPress(): void }) {
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
					backgroundColor: palette.accentFill,
				}}
			>
				<SymbolView name="paperplane.fill" tintColor={palette.onFill} size={17} />
			</View>
		</SymbolButton>
	);
}
