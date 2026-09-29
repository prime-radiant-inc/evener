// A sheet's search field: a rounded inset field that filters the list below
// it, as typed (no capitals or corrections), with iOS's clear button.
import { TextInput } from "react-native";
import { allowFontScaling, useColors, useTextScale } from "../ui";

export function SearchField({
	label,
	value,
	onChangeText,
}: {
	/** VoiceOver's name for the field, and its placeholder. */
	label: string;
	value: string;
	onChangeText(text: string): void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<TextInput
			accessibilityLabel={label}
			value={value}
			onChangeText={onChangeText}
			placeholder={label}
			placeholderTextColor={palette.inkLow}
			autoCapitalize="none"
			autoCorrect={false}
			clearButtonMode="while-editing"
			allowFontScaling={allowFontScaling}
			style={{
				minHeight: 36,
				paddingHorizontal: 12,
				borderRadius: 10,
				backgroundColor: palette.inset,
				color: palette.inkHi,
				fontSize: 17 * scale,
			}}
		/>
	);
}
