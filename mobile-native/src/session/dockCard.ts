import type { Palette } from "../design/tokens";

// The amber-edged card both docks sit in (spec 8.4), so the approval and
// question docks read as one kind of thing. It gives up height when the
// screen has less room than the dock needs; its body scrolls (DockBody).
export function dockCard(palette: Palette) {
	return {
		marginHorizontal: 16,
		marginBottom: 8,
		borderWidth: 1,
		borderColor: palette.attentionEdge,
		borderRadius: 12,
		borderCurve: "continuous" as const,
		backgroundColor: palette.surface,
		flexShrink: 1,
	};
}

/** A scroller that takes its content's height until its column runs out of
 * room, then shrinks and scrolls. */
export const shrinkingScroller = { flexGrow: 0, flexShrink: 1 } as const;
