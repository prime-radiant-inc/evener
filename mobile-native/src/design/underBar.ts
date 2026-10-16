// A list that runs under something floating at its end (the session's bottom
// bar, the Board's compose button), so that thing has content under it
// (spec 16.3).
import { useCallback, useState } from "react";
import { type LayoutChangeEvent, Platform, type ViewStyle } from "react-native";

/** The gap between a bar and what floats above it (a toast, Next, "↓ new"). */
export const FLOAT_GAP = 10;

/** What a floating element wears so it reads as lifted off the page
 * (spec 16.3): the shadow* props iOS draws, and the elevation Android draws
 * instead. A round control (the Board's compose button) and a surface (a
 * toast, a notice banner) take the same lift. */
export const FLOATING_SHADOW: ViewStyle = {
	shadowColor: "#000000",
	shadowOpacity: 0.12,
	shadowRadius: 12,
	shadowOffset: { width: 0, height: 4 },
	elevation: 3,
};

/** How a list keeps its end clear of a bar `barHeight` tall. On iOS the bar
 * is a content inset: the list's content size never depends on it, so the
 * bar growing or shrinking moves nothing, and UIKit counts the rows under it
 * as on screen (VoiceOver included). Android has no content inset, so the
 * list pads its end by the bar instead. */
export function underBar(barHeight: number): {
	contentInset?: { bottom: number };
	scrollIndicatorInsets: { bottom: number };
	endPadding: number;
} {
	if (Platform.OS === "ios")
		return { contentInset: { bottom: barHeight }, scrollIndicatorInsets: { bottom: barHeight }, endPadding: 0 };
	return { scrollIndicatorInsets: { bottom: barHeight }, endPadding: barHeight };
}

/** How much of a list's viewport a bar leaves clear, as a content height: a
 * list's content fills it to rest at its end with nothing under the bar, and
 * scrolls by however much taller than it the content is. On Android the
 * content pads its own end by the bar, so that is the whole viewport. */
export function listContentMinHeight(viewportHeight: number, list: ReturnType<typeof underBar>): number {
	return Math.max(0, viewportHeight - (list.contentInset?.bottom ?? 0));
}

/** A bar's height from its onLayout, null until it has laid out. */
export function useBarHeight(): { height: number | null; onLayout: (event: LayoutChangeEvent) => void } {
	const [height, setHeight] = useState<number | null>(null);
	const onLayout = useCallback((event: LayoutChangeEvent) => setHeight(event.nativeEvent.layout.height), []);
	return { height, onLayout };
}
