// A sheet's search field: a rounded inset field that filters the list below it,
// as typed (no capitals or corrections), with a magnifying glass and a clear
// button. `label` is both its VoiceOver label and its placeholder. It is the
// one field the Hub and New session share; a page insets it, so it carries no
// margin of its own.
import { SymbolView } from "expo-symbols";
import { Pressable, TextInput, View } from "react-native";
import { uiType } from "../design/tokens";
import { allowFontScaling, useColors, useTextScale } from "../ui";

export function SearchField({
	label,
	value,
	onChangeText,
}: {
	label: string;
	value: string;
	onChangeText(text: string): void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View
			style={{
				minHeight: 44,
				flexDirection: "row",
				alignItems: "center",
				gap: 6,
				paddingLeft: 10,
				paddingRight: value ? 0 : 10,
			}}
		>
			<View
				pointerEvents="none"
				style={{
					position: "absolute",
					left: 0,
					right: 0,
					top: 4,
					bottom: 4,
					borderRadius: 10,
					backgroundColor: palette.inset,
				}}
			/>
			<SymbolView name="magnifyingglass" size={15} tintColor={palette.inkLow} />
			<TextInput
				accessibilityLabel={label}
				placeholder={label}
				placeholderTextColor={palette.inkLow}
				value={value}
				onChangeText={onChangeText}
				autoCapitalize="none"
				autoCorrect={false}
				allowFontScaling={allowFontScaling}
				style={{
					flex: 1,
					minHeight: 44,
					color: palette.inkHi,
					fontSize: uiType.listRow.fontSize * scale,
					paddingVertical: 8,
				}}
			/>
			{value ? (
				<Pressable
					accessibilityRole="button"
					accessibilityLabel="Clear filter"
					onPress={() => onChangeText("")}
					style={{ minWidth: 44, minHeight: 44, alignItems: "center", justifyContent: "center" }}
				>
					<SymbolView name="xmark.circle.fill" size={15} tintColor={palette.inkLow} />
				</Pressable>
			) : null}
		</View>
	);
}
