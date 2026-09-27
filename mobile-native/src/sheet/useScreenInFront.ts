// Whether a screen is the one in front: focused, or covered only by sheets
// (sheetRoutes.ts, inFront). A sheet is part of the screen under it, so that
// screen keeps following its session, and its actions stay allowed, while its
// sheets are open.
import { useIsFocused, useNavigationState } from "@react-navigation/native";
import { inFront } from "./sheetRoutes";

export function useScreenInFront(routeKey: string): boolean {
	const focused = useIsFocused();
	const underSheets = useNavigationState((state) => inFront(state, routeKey));
	return focused || underSheets;
}

/** The same answer at the moment of a call, for a guard inside a callback. */
export function screenInFront(
	navigation: { isFocused(): boolean; getState(): Parameters<typeof inFront>[0] },
	routeKey: string,
): boolean {
	return navigation.isFocused() || inFront(navigation.getState(), routeKey);
}
