// What floats 10pt above the transcript's end, which sits on the tray, the
// composer or the dock: a toast, then Next at the trailing edge, then
// "↓ 3 new". They share one column so none can cover another, and each is
// left out while it has nothing to show, so what does show keeps its 10pt.
// The stack reports the room it occludes, its own height plus its 10pt lift,
// so the transcript can keep its newest message clear of all of it (#2988).
// The screen lets that room only grow while it is mounted, so controls coming
// and going never move the list back; the first, taller stack lifts a
// follower's end once.
import type { ReactNode } from "react";
import { View } from "react-native";
import type { Palette } from "../design/tokens";
import { FLOAT_GAP } from "../design/underBar";
import { useComposerTyping } from "../useKeyboardShown";
import type { ComposerFocus } from "./composerFocus";

/** The capsule every floating control wears, so Next and "↓ 3 new" read as
 * one family and restyle together. */
export function floatingCapsule(palette: Palette) {
	return {
		minHeight: 44,
		paddingHorizontal: 16,
		borderRadius: 999,
		borderWidth: 1,
		borderColor: palette.edgeStrong,
		backgroundColor: palette.surface,
		shadowColor: "#000",
		shadowOpacity: 0.12,
		shadowRadius: 8,
		shadowOffset: { width: 0, height: 2 },
		elevation: 3,
	} as const;
}

/** The floor of the room the transcript keeps at its end (spec 8.3: 60pt),
 * grown with the text size and never less, so one Next (44pt with its 10pt
 * lift) never sits on the last line. A taller stack raises the room through
 * `onRoom`; the screen keeps the tallest it has seen while mounted. */
export function transcriptEndRoomAt(scale: number): number {
	return Math.max(60, Math.round(60 * scale));
}

export function FloatingStack({
	toast,
	next,
	pill,
	barHeight = 0,
	composerFocus,
	onRoom,
}: {
	toast: ReactNode;
	next: ReactNode;
	pill: ReactNode;
	/** The composer's focus: while you type in it (useComposerTyping), Next
	 * steps aside. */
	composerFocus: ComposerFocus;
	/** How tall the bar under the transcript's end stands; the stack floats
	 * FLOAT_GAP above it. */
	barHeight?: number;
	/** The room the stack occludes above the bar: its height plus its 10pt
	 * lift. The transcript keeps its end that far clear. */
	onRoom?(room: number): void;
}) {
	const typing = useComposerTyping(composerFocus);
	return (
		<View
			pointerEvents="box-none"
			style={{ position: "absolute", left: 0, right: 0, bottom: barHeight + FLOAT_GAP, gap: 8 }}
			onLayout={(event) => onRoom?.(event.nativeEvent.layout.height + FLOAT_GAP)}
		>
			{toast ? (
				<View pointerEvents="box-none" style={{ alignItems: "center" }}>
					{toast}
				</View>
			) : null}
			{next && !typing ? (
				<View pointerEvents="box-none" style={{ alignItems: "flex-end", paddingHorizontal: 16 }}>
					{next}
				</View>
			) : null}
			{pill ? (
				<View pointerEvents="box-none" style={{ alignItems: "center" }}>
					{pill}
				</View>
			) : null}
		</View>
	);
}
