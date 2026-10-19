// The Session's composer (spec 8.5): the field on top, and beneath it one
// controls row: +, the model and effort controls, and the slot at the end.
// That slot is Stop while the agent works and the field is empty, and Send
// otherwise: with an empty field the immediate action is stopping, with text
// it is sending. Steering is something you do to a queued message.
import { SymbolView } from "expo-symbols";
import { type ReactNode, type RefObject, useEffect, useState } from "react";
import { ActionSheetIOS, Alert, Platform, Pressable, Text, TextInput, useWindowDimensions, View } from "react-native";
import { useHoldAlerts } from "../alerts/alertsContext";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import type { ComposerFocus } from "./composerFocus";
import { ExpandedEditor } from "./ExpandedEditor";
import { SendButton } from "./SendButton";
import { StopButton } from "./StopButton";
import { SymbolButton } from "./SymbolButton";

/** The field grows to this many lines, then scrolls. */
const MAX_LINES = 6;

export interface ComposerProps {
	value: string;
	editable: boolean;
	/** Whether the empty draft may rest one line tall. The screen decides this
	 * (`composerRests`), since only it sees the images alongside the text and
	 * whether the field can take focus; the composer adds its own focus bit. */
	mayRest: boolean;
	onChangeText(text: string): void;
	inputRef?: RefObject<TextInput | null>;
	/** Where the field reports its focus, for what steps aside while you type
	 * (useComposerTyping). */
	focus?: ComposerFocus;
	placeholder: string;
	sendLabel: string;
	sendEnabled: boolean;
	onSend(): void;
	/** Stop, while the agent works: it takes the slot where Send sits. The
	 * caller decides when, since only it knows the whole draft (the text here
	 * and any images above); absent while the turn cannot stop. `disabled`
	 * covers a stop already in flight. */
	stop?: { onStop(): void; disabled: boolean };
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
	mayRest,
	onChangeText,
	inputRef,
	focus,
	placeholder,
	sendLabel,
	sendEnabled,
	onSend,
	stop,
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
	const [fieldHeight, setFieldHeight] = useState(0);
	const [expanded, setExpanded] = useState(false);
	// Banners wait while you type (spec 13.3): the field focused with text in
	// it. Sending empties it, and that lets them go.
	const [focused, setFocused] = useState(false);
	useHoldAlerts(focused && value.trim() !== "", "quiet");
	// A composer that leaves the screen while focused reports no blur.
	useEffect(() => () => focus?.set(false), [focus]);
	// An empty field you aren't in rests one line tall, so the composer stays
	// small until you reach for it: focusing it, or any character at all
	// (whitespace included, which the field visibly holds), keeps it open.
	// Whether the draft may rest at all is the screen's call (`mayRest`: an
	// attached image or a read-only field keeps it open); focus is the composer's.
	const resting = mayRest && !focused;
	const paddingVertical = resting ? 10 : 0;
	const minHeight = Math.max(44, lineHeight + paddingVertical * 2);
	// The viewport cap can hide text before six lines at large text sizes.
	// Explicit line breaks count before measurement; one point absorbs native
	// layout rounding without hiding the expansion action for a scrolled line.
	const maxHeight = Math.max(minHeight, Math.min(MAX_LINES * lineHeight, windowHeight / 3));
	// At the cap, iOS can stop emitting content-size changes. Offer expansion
	// from the measured frame as well, before more text scrolls inside it.
	const overflows =
		value.split("\n").length * lineHeight > maxHeight || contentHeight > maxHeight + 1 || fieldHeight >= maxHeight - 1;
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
	const add = (
		<SymbolButton label="Add" disabled={!editable} onPress={openAddMenu}>
			<SymbolView name="plus" tintColor={palette.accentInk} size={20} />
		</SymbolButton>
	);
	const slot = stop ? (
		<StopButton disabled={stop.disabled} onPress={stop.onStop} />
	) : (
		<SendButton label={sendLabel} disabled={!sendEnabled} onPress={onSend} />
	);
	const field = (
		<>
			<TextInput
				ref={inputRef}
				accessibilityLabel="Message"
				// Native scaling updates the font and its measurement together. A
				// later JS font-size change can measure an existing draft's old font.
				allowFontScaling
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
				onLayout={(event) => setFieldHeight(event.nativeEvent.layout.height)}
				editable={editable}
				placeholder={placeholder}
				placeholderTextColor={palette.inkMid}
				style={{
					color: palette.inkHi,
					fontSize: 17,
					lineHeight: 24,
					minHeight,
					// Native intrinsic layout owns growth and shrinkage. Fixing height
					// to its last measurement prevents iOS from emitting the next one.
					// At the largest text sizes six lines would crowd out the
					// transcript, so the field also stops at a third of the window.
					maxHeight,
					paddingHorizontal: 4,
					paddingVertical,
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
					<SymbolView name="arrow.up.left.and.arrow.down.right" tintColor={palette.inkMid} size={15} />
				</SymbolButton>
			) : null}
		</>
	);
	return (
		<View
			testID="composer-card"
			style={{
				marginHorizontal: 16,
				marginBottom: 4,
				padding: 8,
				// Collapsed there is one row, so no gap between rows.
				gap: resting ? 0 : 4,
				borderWidth: 1,
				borderRadius: 22,
				borderColor: palette.edge,
				backgroundColor: palette.surface,
			}}
		>
			{above}
			{/* One stable parent for the field, whose shape changes rather than
			    which branch renders: collapsed, the +, the field and Send share a
			    row and the model chip steps aside; open, the field takes the first
			    line and the controls sit under it (Jesse). The field stays in this
			    same parent either way, so focusing it does not remount it and drop
			    the keyboard. */}
			<View
				style={{
					flexDirection: resting ? "row" : "column",
					alignItems: resting ? "center" : "stretch",
					gap: 4,
				}}
			>
				{resting ? add : null}
				<View key="field" style={resting ? { flex: 1, minWidth: 0 } : { minWidth: 0 }}>
					{field}
				</View>
				{resting ? (
					slot
				) : (
					<View style={{ flexDirection: "row", alignItems: "center", gap: 4 }}>
						{add}
						<View style={{ flex: 1, minWidth: 0 }}>{settings}</View>
						{slot}
					</View>
				)}
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
