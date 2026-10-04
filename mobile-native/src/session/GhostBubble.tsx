// One dashed ghost bubble (spec 8.5 and 14): a message on its way to the
// agent, or one that needs a look. It sits where your message will land,
// right-aligned, dashed and unfilled, with what it is waiting for beneath it
// and the actions it offers as text buttons. Tapping the bubble opens the
// rest of its actions, and a queued or held message swipes left to cancel.
import type { ReactNode } from "react";
import { ActionSheetIOS, Alert, Platform, Pressable, Text, View } from "react-native";
import { SwipeRow } from "../board/SwipeRow";
import { useReadingType } from "../display/displayContext";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import type { Ghost, GhostAction } from "./ghosts";
import { destructiveButton, haptic } from "../haptics";

const BUTTON_LABELS: Record<GhostAction, string> = {
	steerNow: "Steer now",
	sendNow: "Send now",
	edit: "Edit",
	cancel: "Cancel",
	check: "Check",
	discard: "Discard",
};

// In a menu, "Cancel" is how you close it, so taking a message out of the
// queue says so in full there.
const MENU_LABELS: Record<GhostAction, string> = { ...BUTTON_LABELS, cancel: "Cancel message" };

type GhostBackdrop = "page" | "canvas";

export interface GhostBubbleProps {
	ghost: Ghost;
	/** Another ghost action is running: nothing here fires. */
	disabled: boolean;
	/** Whether the composer can take a draft or refused message back now.
	 * A queued message's Edit merges into whatever is typed, so it ignores
	 * this. */
	canEdit: boolean;
	/** Why Edit can't bring the message back right now. */
	editHint: string | null;
	/** What the message carried besides its text, such as images. */
	attachments?: ReactNode;
	/** What the bubble sits on: the transcript's page, or a sheet's canvas.
	 * A swipe paints it under the unfilled bubble. */
	backdrop: GhostBackdrop;
	onAction(action: GhostAction): void;
}

export function GhostBubble({ ghost, disabled, canEdit, editHint, attachments, backdrop, onAction }: GhostBubbleProps) {
	const { palette } = useColors();
	const scale = useTextScale();
	const reading = useReadingType();
	const editBlocked = ghost.origin.kind !== "queue" && !canEdit;
	const offersEdit = ghost.buttons.includes("edit") || ghost.menu.includes("edit");
	const menu = ghost.menu.filter((action) => !(action === "edit" && editBlocked));
	// The bubble is a control only while tapping it opens something.
	const opensMenu = !disabled && menu.length > 0;
	function openMenu() {
		if (!opensMenu) return;
		const labels = menu.map((action) => MENU_LABELS[action]);
		const destructive = menu.indexOf("cancel");
		if (Platform.OS === "ios")
			ActionSheetIOS.showActionSheetWithOptions(
				{
					options: [...labels, "Cancel"],
					cancelButtonIndex: labels.length,
					...(destructive === -1 ? {} : { destructiveButtonIndex: destructive }),
				},
				(index) => {
					const action = menu[index];
					// Spec 16.6: the destructive choice is its own confirmation.
					if (index === destructive) haptic("rigid");
					if (action) onAction(action);
				},
			);
		// The caption titles it; the message itself, however long, reads
		// beneath.
		else
			Alert.alert(ghost.caption, ghost.text, [
				...menu.map((action) =>
					action === "cancel"
						? destructiveButton(MENU_LABELS[action], () => onAction(action))
						: { text: MENU_LABELS[action], onPress: () => onAction(action) },
				),
				{ text: "Cancel", style: "cancel" as const },
			]);
	}
	const caption = {
		allowFontScaling,
		style: {
			fontSize: 13 * scale,
			lineHeight: 18 * scale,
			color: ghost.state === "refused" ? palette.dangerInk : palette.inkLow,
		},
	};
	const bubble = (
		<View style={{ alignItems: "flex-end", gap: 2 }}>
			<Pressable
				accessibilityLabel={`${ghost.text}. ${ghost.caption}`}
				{...(opensMenu ? { accessibilityRole: "button" as const } : {})}
				accessibilityState={{ disabled: !opensMenu }}
				disabled={!opensMenu}
				onPress={openMenu}
				style={{
					alignSelf: "flex-end",
					maxWidth: "85%",
					borderWidth: 1,
					borderStyle: "dashed",
					borderColor: palette.edgeStrong,
					borderRadius: 18,
					borderCurve: "continuous",
					paddingHorizontal: 14,
					paddingVertical: 10,
				}}
			>
				<Text
					allowFontScaling={allowFontScaling}
					numberOfLines={3}
					ellipsizeMode="tail"
					style={{
						fontFamily: reading.yourMessage.fontFamily,
						fontSize: reading.yourMessage.fontSize * scale,
						lineHeight: reading.yourMessage.lineHeight * scale,
						color: palette.inkMid,
					}}
				>
					{ghost.text}
				</Text>
				{attachments}
			</Pressable>
			<Text {...caption}>{ghost.caption}</Text>
			{ghost.note ? <Text {...caption}>{ghost.note}</Text> : null}
			{offersEdit && editBlocked && editHint ? <Text {...caption}>{editHint}</Text> : null}
			{ghost.buttons.length > 0 ? (
				<View style={{ flexDirection: "row", gap: 4 }}>
					{ghost.buttons.map((action) => (
						<GhostButton
							key={action}
							action={action}
							disabled={disabled || (action === "edit" && editBlocked)}
							onPress={() => onAction(action)}
						/>
					))}
				</View>
			) : null}
		</View>
	);
	// Cancel takes the same path as the menu's, which checks at the moment
	// it runs that the message is still the one you swiped. While another
	// action runs the row holds still, and stays mounted so the bubble
	// never remounts.
	const cancels = ghost.buttons.includes("cancel") || ghost.menu.includes("cancel");
	return cancels ? (
		<SwipeRow
			destructive={{ key: "cancel", label: BUTTON_LABELS.cancel, run: () => onAction("cancel") }}
			backdrop={palette[backdrop]}
			enabled={!disabled}
		>
			{bubble}
		</SwipeRow>
	) : (
		bubble
	);
}

/** One of a ghost's text buttons: accent, or quiet when it takes the message
 * away. Disabled, it dims and does nothing. */
function GhostButton({ action, disabled, onPress }: { action: GhostAction; disabled: boolean; onPress(): void }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const quiet = action === "discard" || action === "cancel";
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={BUTTON_LABELS[action]}
			accessibilityState={{ disabled }}
			disabled={disabled}
			onPress={() => {
				if (!disabled) onPress();
			}}
			style={({ pressed }) => ({
				minHeight: 44,
				minWidth: 44,
				paddingHorizontal: 8,
				alignItems: "center",
				justifyContent: "center",
				opacity: disabled ? 0.4 : pressed ? 0.6 : 1,
			})}
		>
			<Text
				allowFontScaling={allowFontScaling}
				style={{
					fontSize: 15 * scale,
					lineHeight: 20 * scale,
					color: quiet ? palette.inkHi : palette.accentInk,
				}}
			>
				{BUTTON_LABELS[action]}
			</Text>
		</Pressable>
	);
}
