// The Session's composer (spec 8.5): the field on top, and beneath it one
// controls row that looks the same whether or not the agent works: +, the
// model and effort controls, and the one Send. Stop lives in the status
// tray; steering is something you do to a queued message.
import { SymbolView } from "expo-symbols";
import { type ReactNode, type RefObject, useEffect, useState } from "react";
import { ActionSheetIOS, Alert, Platform, Pressable, Text, TextInput, useWindowDimensions, View } from "react-native";
import { useHoldAlerts } from "../alerts/alertsContext";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import type { ComposerFocus } from "./composerFocus";
import { ExpandedEditor } from "./ExpandedEditor";
import { SendButton } from "./SendButton";
import { SymbolButton } from "./SymbolButton";

/** The field grows to this many lines, then scrolls. */
const MAX_LINES = 6;

export interface ComposerProps {
	value: string;
	editable: boolean;
	onChangeText(text: string): void;
	inputRef?: RefObject<TextInput | null>;
	/** Where the field reports its focus, for what steps aside while you type
	 * (useComposerTyping). */
	focus?: ComposerFocus;
	placeholder: string;
	sendLabel: string;
	sendEnabled: boolean;
	onSend(): void;
	onPhotoLibrary(): void;
	onCamera(): void;
	/** Opens Commands and skills. Absent, + doesn't offer it. */
	onCommands?(): void;
	/** The model chip. Null hides the slot. */
	settings: ReactNode;
	/** What sits above the field: attachments and the queued ghosts. */
	above?: ReactNode;
}

export function Composer({
	value,
	editable,
	onChangeText,
	inputRef,
	focus,
	placeholder,
	sendLabel,
	sendEnabled,
	onSend,
	onPhotoLibrary,
	onCamera,
	onCommands,
	settings,
	above,
}: ComposerProps) {
	const { palette } = useColors();
	const { fontScale, height: windowHeight } = useWindowDimensions();
	const scale = Platform.OS === "ios" ? fontScale : 1;
	const lineHeight = 24 * scale;
	const [contentHeight, setContentHeight] = useState(0);
	const [expanded, setExpanded] = useState(false);
	// Banners wait while you type (spec 13.3): the field focused with text in
	// it. Sending empties it, and that lets them go.
	const [focused, setFocused] = useState(false);
	useHoldAlerts(focused && value.trim() !== "", "quiet");
	// A composer that leaves the screen while focused reports no blur.
	useEffect(() => () => focus?.set(false), [focus]);
	// Typed line breaks count before layout has measured anything; the
	// measured height catches long lines that wrap. The half line of slack
	// keeps rounding in the measurement from offering the editor at six.
	const overflows = value.split("\n").length > MAX_LINES || contentHeight > (MAX_LINES + 0.5) * lineHeight;
	function openAddMenu() {
		const choices = [
			{ text: "Photo library", onPress: onPhotoLibrary },
			{ text: "Camera", onPress: onCamera },
			...(onCommands ? [{ text: "Commands and skills", onPress: onCommands }] : []),
		];
		if (Platform.OS === "ios")
			ActionSheetIOS.showActionSheetWithOptions(
				{ options: [...choices.map((choice) => choice.text), "Cancel"], cancelButtonIndex: choices.length },
				(index) => choices[index]?.onPress(),
			);
		else Alert.alert("Add", undefined, [...choices, { text: "Cancel", style: "cancel" }]);
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
					allowFontScaling={allowFontScaling}
					multiline
					scrollEnabled
					value={value}
					onChangeText={onChangeText}
					onFocus={() => {
						setFocused(true);
						focus?.set(true);
					}}
					onBlur={() => {
						setFocused(false);
						focus?.set(false);
					}}
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
						onPress={() => {
							// The editor's own field takes the keyboard; the composer's
							// blur isn't something to wait on.
							focus?.set(false);
							setExpanded(true);
						}}
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
				<SendButton label={sendLabel} disabled={!sendEnabled} onPress={onSend} />
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

/** The model and its effort, "GLM 5.3 Vision · XHigh" (spec 8.5). It opens
 * the model sheet; with nothing it could change, it only names the model. */
export function ModelChip({ label, onPress }: { label: string; onPress?: () => void }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const text = (
		<Text
			allowFontScaling={allowFontScaling}
			numberOfLines={1}
			style={{ flexShrink: 1, color: palette.inkMid, fontSize: 15 * scale, lineHeight: 20 * scale }}
		>
			{label}
		</Text>
	);
	if (!onPress) return <View style={{ minHeight: 44, flexDirection: "row", alignItems: "center" }}>{text}</View>;
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={`Model: ${label}. Change model or effort`}
			onPress={onPress}
			style={({ pressed }) => ({
				minHeight: 44,
				flexDirection: "row",
				alignItems: "center",
				gap: 4,
				opacity: pressed ? 0.6 : 1,
			})}
		>
			{text}
			<SymbolView name="chevron.down" tintColor={palette.inkMid} size={11 * scale} />
		</Pressable>
	);
}
