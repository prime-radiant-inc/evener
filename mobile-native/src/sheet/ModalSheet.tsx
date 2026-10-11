// A Sheet framed in a React Native modal: the Hub's detail and form sheets,
// which open over a page rather than as routes of their own. The page sheet
// slides up with the canvas behind the shared header.
import type { ReactNode } from "react";
import { View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { HoldingModal } from "../alerts/HoldingModal";
import { useColors } from "../ui";
import { Sheet, type SheetProps } from "./Sheet";

/** The modal a sheet opens in: a page sheet that slides up on the canvas.
 * ModalSheet puts the shared Sheet in it; a sheet that draws its own Sheet,
 * such as an editor whose header saves, goes in it directly. */
export function ModalFrame({
	visible = true,
	onRequestClose,
	privacyCover = false,
	children,
}: {
	visible?: boolean;
	/** A swipe down or Android's back. */
	onRequestClose(): void;
	/** An opaque cover over the frame's content while the app is inactive, so a
	 * secret on the sheet never reaches the app-switcher snapshot. */
	privacyCover?: boolean;
	children: ReactNode;
}) {
	const { palette } = useColors();
	return (
		<HoldingModal visible={visible} animationType="slide" presentationStyle="pageSheet" onRequestClose={onRequestClose}>
			<SafeAreaView style={{ flex: 1, backgroundColor: palette.canvas }}>
				{children}
				{privacyCover ? (
					<View
						testID="privacy-cover"
						style={{
							position: "absolute",
							top: 0,
							left: 0,
							right: 0,
							bottom: 0,
							backgroundColor: palette.canvas,
						}}
					/>
				) : null}
			</SafeAreaView>
		</HoldingModal>
	);
}

export function ModalSheet({
	visible = true,
	onRequestClose,
	privacyCover,
	...sheet
}: SheetProps & {
	visible?: boolean;
	/** A swipe down or Android's back. */
	onRequestClose(): void;
	/** Hide the sheet's content while the app is inactive (see ModalFrame). */
	privacyCover?: boolean;
}) {
	return (
		<ModalFrame visible={visible} onRequestClose={onRequestClose} privacyCover={privacyCover}>
			<Sheet {...sheet} />
		</ModalFrame>
	);
}
