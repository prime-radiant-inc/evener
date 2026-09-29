import type { Palette } from "../design/tokens";

// The amber-edged card both docks sit in (spec 8.4), so the approval and
// question docks read as one kind of thing.
export function dockCard(palette: Palette) {
	return {
		marginHorizontal: 16,
		marginBottom: 8,
		borderWidth: 1,
		borderColor: palette.attentionEdge,
		borderRadius: 12,
		borderCurve: "continuous" as const,
		backgroundColor: palette.surface,
	};
}
