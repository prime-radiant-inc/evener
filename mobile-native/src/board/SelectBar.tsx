// Select mode's bar, in the Board toolbar's place (spec 7.1): Done, then
// what can be done to the chosen sessions.
import { View } from "react-native";
import { BarButton, ToolbarFrame, type ToolbarPlacement } from "./BoardToolbar";

/** Each action is disabled while none of the chosen sessions can take it
 * (selection.ts selectionActions); Done always works. */
export function SelectBar({
	counts,
	onDone,
	onArchive,
	onPin,
	...placement
}: {
	counts: { archive: number; pin: number };
	onDone: () => void;
	onArchive: () => void;
	onPin: () => void;
} & ToolbarPlacement) {
	return (
		<ToolbarFrame {...placement}>
			<BarButton label="Done" onPress={onDone} />
			<View style={{ flex: 1, flexDirection: "row", justifyContent: "flex-end" }}>
				<BarButton label="Archive" disabled={counts.archive === 0} onPress={onArchive} />
				<BarButton label="Pin" disabled={counts.pin === 0} onPress={onPin} />
			</View>
		</ToolbarFrame>
	);
}
