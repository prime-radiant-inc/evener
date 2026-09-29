// The composer's expanded editor (spec 8.5): the same draft in a field
// with room to read it, for a message that has outgrown six lines.
import { Platform, TextInput, useWindowDimensions } from "react-native";
import { ModalSheet } from "../sheet/ModalSheet";
import { allowFontScaling, useColors } from "../ui";

export function ExpandedEditor({
	visible,
	value,
	onChangeText,
	placeholder,
	editable,
	onDone,
}: {
	visible: boolean;
	value: string;
	onChangeText(text: string): void;
	placeholder: string;
	editable: boolean;
	onDone(): void;
}) {
	const { palette } = useColors();
	const { fontScale } = useWindowDimensions();
	const scale = Platform.OS === "ios" ? fontScale : 1;
	return (
		<ModalSheet visible={visible} done={{ onPress: onDone }} onRequestClose={onDone}>
			<TextInput
				accessibilityLabel="Message"
				allowFontScaling={allowFontScaling}
				multiline
				autoFocus
				editable={editable}
				value={value}
				onChangeText={onChangeText}
				placeholder={placeholder}
				placeholderTextColor={palette.inkMid}
				style={{
					flex: 1,
					paddingHorizontal: 16,
					paddingTop: 8,
					color: palette.inkHi,
					fontSize: 17 * scale,
					lineHeight: 24 * scale,
					textAlignVertical: "top",
				}}
			/>
		</ModalSheet>
	);
}
