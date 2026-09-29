// The composer's full-screen editor (spec 8.5): the same draft in a field
// with room to read it, for a message that has outgrown six lines.
import { Platform, TextInput, useWindowDimensions, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { HoldingModal } from "../alerts/HoldingModal";
import { Action, allowFontScaling, styles, useColors } from "../ui";

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
		<HoldingModal visible={visible} animationType="slide" presentationStyle="fullScreen" onRequestClose={onDone}>
			<SafeAreaView style={[styles.fill, { backgroundColor: palette.page }]}>
				<View style={{ flexDirection: "row", justifyContent: "flex-end", paddingHorizontal: 8 }}>
					<Action onPress={onDone}>Done</Action>
				</View>
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
			</SafeAreaView>
		</HoldingModal>
	);
}
