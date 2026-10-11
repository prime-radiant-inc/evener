// Whether a screen is the one in front: focused, or covered only by sheets
// (sheetRoutes.ts, inFront). A sheet is part of the screen under it, so that
// screen keeps following its session, and its actions stay allowed, while its
// sheets are open.
import { useNavigationState } from "@react-navigation/native";
import { inFront, type StackState } from "./sheetRoutes";

export function useScreenInFront(routeKey: string): boolean {
	// inFront already answers true for the focused route, so a useIsFocused
	// subscription would add nothing but a re-render on every sheet open and
	// close.
	return useNavigationState((state) => inFront(state, routeKey));
}

/** The same answer at the moment of a call, for a guard inside a callback. */
export function screenInFront(navigation: { getState(): StackState }, routeKey: string): boolean {
	return inFront(navigation.getState(), routeKey);
}

/** Leaves the screen along with every route over it. A covered screen's own
 * `goBack()` pops only the top route (StackRouter's POP counts from the
 * stack's index, not from the screen that asked), so a session under its
 * sheet would pop the sheet and stay. Does nothing once the screen is no
 * longer in the stack at or below its index. */
export function leaveScreen(navigation: { getState(): StackState; pop(count: number): void }, routeKey: string): void {
	const state = navigation.getState();
	const position = state.routes.findIndex((route) => route.key === routeKey);
	if (position === -1 || position > state.index) return;
	navigation.pop(state.index - position + 1);
}
