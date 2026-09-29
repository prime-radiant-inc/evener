// A Sheet framed in a React Native modal: the Hub's detail and form sheets,
// which open over a page rather than as routes of their own. The page sheet
// slides up with the canvas behind the shared header.
import { SafeAreaView } from "react-native-safe-area-context";
import { HoldingModal } from "../alerts/HoldingModal";
import { useColors } from "../ui";
import { Sheet, type SheetProps } from "./Sheet";

export function ModalSheet({
	visible = true,
	onRequestClose,
	...sheet
}: SheetProps & {
	visible?: boolean;
	/** A swipe down or Android's back. */
	onRequestClose(): void;
}) {
	const { palette } = useColors();
	return (
		<HoldingModal visible={visible} animationType="slide" presentationStyle="pageSheet" onRequestClose={onRequestClose}>
			<SafeAreaView style={{ flex: 1, backgroundColor: palette.canvas }}>
				<Sheet {...sheet} />
			</SafeAreaView>
		</HoldingModal>
	);
}
