// The chrome every redesign sheet shares (spec 6 and 16.2): a header with the
// title, Cancel and Done over the sheet's body. A sheet route (a formSheet in
// the native stack, sized in sheetRoutes.ts) renders it as its root, and
// ModalSheet frames it inside a React Native modal; closing a route sheet as
// a person would is useSheet's job (useSheet.ts).
//
// Layout for a sheet route: react-native-screens sizes a sheet's scroll view
// to each detent only when the scroll view is the screen's first or second
// direct child, after a header view that isn't flattened away
// (RNSScreenContentWrapper.mm, coerceChildScrollViewComponentSizeToSize). So
// <Sheet> renders exactly two children: the header, and the body, which is one
// ScrollView, FlatList or SectionList. Anything pinned, such as a search
// field, a segmented control or a toast, goes in `accessory`, inside the
// header. A modal has no detents, but keeps the same shape.
import type { ReactElement, ReactNode } from "react";
import { Platform, Text, useWindowDimensions, View } from "react-native";
import { allowFontScaling, useColors } from "../ui";
import { HeaderButton } from "./HeaderButton";

export interface SheetButton {
	/** "Done" unless the sheet names its own verb, such as "Add" or "Send". */
	label?: string;
	disabled?: boolean;
	/** Its action is running: VoiceOver hears it's busy. */
	busy?: boolean;
	onPress(): void;
}

export interface SheetProps {
	title?: string;
	/** A leading "Cancel". */
	onCancel?: () => void;
	/** Cancel can't run just now, such as while a save is in flight. */
	cancelDisabled?: boolean;
	/** The trailing button. */
	done?: SheetButton;
	/** Pinned under the title: a search field, a segmented control, a toast. */
	accessory?: ReactNode;
	/** The body: one ScrollView, FlatList or SectionList. */
	children: ReactElement;
}

export function Sheet({ title, onCancel, cancelDisabled = false, done, accessory, children }: SheetProps) {
	const { palette } = useColors();
	const { fontScale } = useWindowDimensions();
	const scale = Platform.OS === "ios" ? fontScale : 1;
	return (
		<>
			<View collapsable={false} style={{ backgroundColor: palette.canvas }}>
				<View style={{ minHeight: 56, flexDirection: "row", alignItems: "center", paddingHorizontal: 8 }}>
					<View style={{ flex: 1, alignItems: "flex-start" }}>
						{onCancel ? <HeaderButton label="Cancel" disabled={cancelDisabled} onPress={onCancel} /> : null}
					</View>
					{title ? (
						<Text
							accessibilityRole="header"
							allowFontScaling={allowFontScaling}
							numberOfLines={1}
							style={{
								flexShrink: 1,
								// The prototype's 220 of 390pt: the side slots keep room
								// for Cancel and Done, and a long title truncates.
								maxWidth: "56%",
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
							<HeaderButton
								label={done.label ?? "Done"}
								strong
								disabled={done.disabled}
								busy={done.busy}
								onPress={done.onPress}
							/>
						) : null}
					</View>
				</View>
				{accessory}
			</View>
			{children}
		</>
	);
}
