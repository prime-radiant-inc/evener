// The chrome every redesign sheet shares (spec 6 and 16.2): a header with the
// title, Cancel and Done over the sheet's body. The native stack presents it
// as a formSheet route with the size in sheetRoutes.ts.
//
// Layout: react-native-screens sizes a sheet's scroll view to each detent only
// when the scroll view is the screen's first or second direct child, after a
// header view that isn't flattened away (RNSScreenContentWrapper.mm,
// coerceChildScrollViewComponentSizeToSize). So a sheet route renders <Sheet>
// as its root, and <Sheet> renders exactly two children: the header, and the
// body, which is one ScrollView, FlatList or SectionList. Anything pinned, such
// as a search field, a segmented control or a toast, goes in `accessory`,
// inside the header.
import { useNavigation, usePreventRemove } from "@react-navigation/native";
import { type ReactElement, type ReactNode, useEffect, useMemo, useRef } from "react";
import { Alert, Platform, Pressable, Text, useWindowDimensions, View } from "react-native";
import { useColors } from "../ui";
import { DISCARD_TITLE, discardAlert, sheetLeave } from "./sheetLeave";

export interface SheetController {
	/** Close as a person would: a sheet with unsaved input asks first. */
	close(): void;
	/** Leave on purpose, without asking: its Send or Add went through, or it
	 * leads somewhere else. Without `then`, the sheet goes back. With `then`,
	 * `then` removes the sheet itself: `navigation.goBack()` before a
	 * `navigate`, or `returnToSession`'s pop. A screen pushed while a sheet is
	 * still up lands under the sheet (react-native-screens pushes cards on the
	 * main stack and presents sheets over it), so the sheet always goes first. */
	finish(then?: () => void): void;
}

export interface SheetOptions {
	/** Unsaved input: closing asks "Keep editing" or "Discard". */
	dirty?: boolean;
	/** The question that alert asks, such as "Discard this comment?". */
	discardTitle?: string;
	/** Runs once when the sheet goes away, however it closed. */
	onClosed?: () => void;
}

export function useSheet({
	dirty = false,
	discardTitle = DISCARD_TITLE,
	onClosed,
}: SheetOptions = {}): SheetController {
	const navigation = useNavigation();
	const finishing = useRef(false);
	// A refused swipe down (native-stack's onNativeDismissCancelled), Cancel and
	// Android's back all arrive here while `dirty` holds the route.
	usePreventRemove(dirty, ({ data }) => {
		const leave = () => navigation.dispatch(data.action);
		if (sheetLeave(dirty, finishing.current) === "leave") {
			// Consumed: a bypass is good for the one removal it was set for, not
			// every later dismissal of this same mounted sheet.
			finishing.current = false;
			leave();
			return;
		}
		const alert = discardAlert(discardTitle, leave);
		Alert.alert(alert.title, undefined, alert.buttons);
	});
	const closed = useRef(onClosed);
	closed.current = onClosed;
	useEffect(() => () => closed.current?.(), []);
	return useMemo(
		() => ({
			close: () => navigation.goBack(),
			finish: (then?: () => void) => {
				finishing.current = true;
				if (then) then();
				else navigation.goBack();
			},
		}),
		[navigation],
	);
}

export interface SheetButton {
	/** "Done" unless the sheet names its own verb, such as "Add" or "Send". */
	label?: string;
	disabled?: boolean;
	onPress(): void;
}

export interface SheetProps {
	title?: string;
	/** A leading "Cancel". */
	onCancel?: () => void;
	/** The trailing button. */
	done?: SheetButton;
	/** Pinned under the title: a search field, a segmented control, a toast. */
	accessory?: ReactNode;
	/** The body: one ScrollView, FlatList or SectionList. */
	children: ReactElement;
}

export function Sheet({ title, onCancel, done, accessory, children }: SheetProps) {
	const { palette } = useColors();
	const { fontScale } = useWindowDimensions();
	const scale = Platform.OS === "ios" ? fontScale : 1;
	return (
		<>
			<View collapsable={false} style={{ backgroundColor: palette.canvas }}>
				<View style={{ minHeight: 56, flexDirection: "row", alignItems: "center", paddingHorizontal: 8 }}>
					<View style={{ flex: 1, alignItems: "flex-start" }}>
						{onCancel ? <HeaderButton label="Cancel" onPress={onCancel} /> : null}
					</View>
					{title ? (
						<Text
							accessibilityRole="header"
							allowFontScaling={Platform.OS !== "ios"}
							numberOfLines={1}
							style={{
								flexShrink: 1,
								textAlign: "center",
								color: palette.inkHi,
								fontSize: 17 * scale,
								lineHeight: 22 * scale,
								fontWeight: "600",
							}}
						>
							{title}
						</Text>
					) : null}
					<View style={{ flex: 1, alignItems: "flex-end" }}>
						{done ? (
							<HeaderButton label={done.label ?? "Done"} strong disabled={done.disabled} onPress={done.onPress} />
						) : null}
					</View>
				</View>
				{accessory}
			</View>
			{children}
		</>
	);
}

function HeaderButton({
	label,
	strong = false,
	disabled = false,
	onPress,
}: {
	label: string;
	strong?: boolean;
	disabled?: boolean;
	onPress(): void;
}) {
	const { palette } = useColors();
	const { fontScale } = useWindowDimensions();
	const scale = Platform.OS === "ios" ? fontScale : 1;
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={label}
			accessibilityState={{ disabled }}
			disabled={disabled}
			hitSlop={8}
			onPress={onPress}
			style={({ pressed }) => ({
				minHeight: 44,
				minWidth: 44,
				justifyContent: "center",
				paddingHorizontal: 8,
				opacity: disabled ? 0.4 : pressed ? 0.6 : 1,
			})}
		>
			<Text
				allowFontScaling={Platform.OS !== "ios"}
				style={{
					color: palette.accentInk,
					fontSize: 17 * scale,
					lineHeight: 24 * scale,
					fontWeight: strong ? "600" : "400",
				}}
			>
				{label}
			</Text>
		</Pressable>
	);
}
