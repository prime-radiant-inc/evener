// The Session's composer (spec 8.5): the field on top, and beneath it one
// controls row that looks the same whether or not the agent works: +, the
// model and effort controls, and the one Send. Stop lives in the status
// tray; steering is something you do to a queued message.
import { SymbolView } from "expo-symbols";
import { type ReactNode, type RefObject, useState } from "react";
import {
	ActionSheetIOS,
	Alert,
	Platform,
	TextInput,
	useWindowDimensions,
	View,
} from "react-native";
import { useColors } from "../ui";
import { ExpandedEditor } from "./ExpandedEditor";
import { SymbolButton } from "./SymbolButton";

/** The field grows to this many lines, then scrolls. */
const MAX_LINES = 6;

export interface ComposerProps {
	value: string;
	editable: boolean;
	onChangeText(text: string): void;
	onSelectionChange?(selection: { start: number; end: number }): void;
	inputRef?: RefObject<TextInput | null>;
	placeholder: string;
	sendLabel: string;
	sendEnabled: boolean;
	onSend(): void;
	onPhotoLibrary(): void;
	onCamera(): void;
	/** The model and effort controls: today's ComposerSettings until
	 * PR 6's chip replaces it. Null hides the slot. */
	settings: ReactNode;
	/** What sits above the field: attachments, today's inline command
	 * completion (until PR 10), and PR 2's ghosts. */
	above?: ReactNode;
}

export function Composer({
	value,
	editable,
	onChangeText,
	onSelectionChange,
	inputRef,
	placeholder,
	sendLabel,
	sendEnabled,
	onSend,
	onPhotoLibrary,
	onCamera,
	settings,
	above,
}: ComposerProps) {
	const { palette } = useColors();
	const { fontScale, height: windowHeight } = useWindowDimensions();
	const scale = Platform.OS === "ios" ? fontScale : 1;
	const lineHeight = 24 * scale;
	const [contentHeight, setContentHeight] = useState(0);
	const [expanded, setExpanded] = useState(false);
	// Typed line breaks count before layout has measured anything; the
	// measured height catches long lines that wrap. The half line of slack
	// keeps rounding in the measurement from offering the editor at six.
	const overflows =
		value.split("\n").length > MAX_LINES || contentHeight > (MAX_LINES + 0.5) * lineHeight;
	function openAddMenu() {
		if (Platform.OS === "ios")
			ActionSheetIOS.showActionSheetWithOptions(
				{ options: ["Photo library", "Camera", "Cancel"], cancelButtonIndex: 2 },
				(index) => {
					if (index === 0) onPhotoLibrary();
					else if (index === 1) onCamera();
				},
			);
		else
			Alert.alert("Add", undefined, [
				{ text: "Photo library", onPress: onPhotoLibrary },
				{ text: "Camera", onPress: onCamera },
				{ text: "Cancel", style: "cancel" },
			]);
	}
	return (
		<View
			style={{
				marginHorizontal: 16,
				marginBottom: 8,
				padding: 8,
				gap: 4,
				borderWidth: 1,
				borderRadius: 22,
				borderColor: palette.edge,
				backgroundColor: palette.surface,
			}}
		>
			{above}
			<View>
				<TextInput
					ref={inputRef}
					accessibilityLabel="Message"
					allowFontScaling={Platform.OS !== "ios"}
					multiline
					scrollEnabled
					value={value}
					onChangeText={onChangeText}
					onSelectionChange={
						onSelectionChange ? (event) => onSelectionChange(event.nativeEvent.selection) : undefined
					}
					onContentSizeChange={(event) => setContentHeight(event.nativeEvent.contentSize.height)}
					editable={editable}
					placeholder={placeholder}
					placeholderTextColor={palette.inkMid}
					style={{
						color: palette.inkHi,
						fontSize: 17 * scale,
						lineHeight,
						minHeight: 44,
						// At the largest text sizes six lines would crowd out the
						// transcript, so the field also stops at a third of the window.
						maxHeight: Math.min(MAX_LINES * lineHeight, windowHeight / 3),
						paddingHorizontal: 4,
						paddingVertical: 0,
						paddingRight: overflows ? 44 : 4,
						textAlignVertical: "top",
					}}
				/>
				{overflows ? (
					<SymbolButton
						label="Expand editor"
						onPress={() => setExpanded(true)}
						style={{ position: "absolute", top: -8, right: -8 }}
					>
						<SymbolView name="arrow.up.left.and.arrow.down.right" tintColor={palette.inkMid} size={15 * scale} />
					</SymbolButton>
				) : null}
			</View>
			<View style={{ flexDirection: "row", alignItems: "center", gap: 4 }}>
				<SymbolButton label="Add" disabled={!editable} onPress={openAddMenu}>
					<SymbolView name="plus" tintColor={palette.accentInk} size={20 * scale} />
				</SymbolButton>
				<View style={{ flex: 1, minWidth: 0 }}>{settings}</View>
				<SymbolButton label={sendLabel} disabled={!sendEnabled} onPress={onSend}>
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
			</View>
			<ExpandedEditor
				visible={expanded}
				value={value}
				onChangeText={onChangeText}
				placeholder={placeholder}
				editable={editable}
				onDone={() => setExpanded(false)}
			/>
		</View>
	);
}
