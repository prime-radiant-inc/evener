// A Sheet framed in a React Native modal: the Hub's detail and form sheets,
// which open over a page rather than as routes of their own. The page sheet
// slides up with the canvas behind the shared header.
import type { ReactNode } from "react";
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
	children,
}: {
	visible?: boolean;
	/** A swipe down or Android's back. */
	onRequestClose(): void;
	children: ReactNode;
}) {
	const { palette } = useColors();
	return (
		<HoldingModal visible={visible} animationType="slide" presentationStyle="pageSheet" onRequestClose={onRequestClose}>
			<SafeAreaView style={{ flex: 1, backgroundColor: palette.canvas }}>{children}</SafeAreaView>
		</HoldingModal>
	);
}

export function ModalSheet({
	visible = true,
	onRequestClose,
	...sheet
}: SheetProps & {
	visible?: boolean;
	/** A swipe down or Android's back. */
	onRequestClose(): void;
}) {
	return (
		<ModalFrame visible={visible} onRequestClose={onRequestClose}>
			<Sheet {...sheet} />
		</ModalFrame>
	);
}
