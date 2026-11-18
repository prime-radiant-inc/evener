// Whether a screen is the one in front: focused, or covered only by sheets
// (sheetRoutes.ts, inFront). A sheet is part of the screen under it, so that
// screen keeps following its session, and its actions stay allowed, while its
// sheets are open.
import { useNavigationState } from "@react-navigation/native";
import { inFront, isSheetRoute, type StackState } from "./sheetRoutes";

/** A navigator's state as these rules read it when it is not the screen's
 * own: the same shape, with the navigator nested in its front route. */
interface NestedState {
	index: number;
	routes: readonly { key: string; name: string; state?: unknown }[];
}

/** A stack a screen is nested in. React Navigation hands its state back as the
 * navigator's own, so the shape is checked before it is read. */
interface OwningStack {
	getState(): unknown;
	getParent?(): OwningStack | undefined;
}

/** A screen's navigation: its own stack's state, and the stacks it is nested
 * in. The Duo split puts a conversation's stack inside the root stack's
 * Sessions route, so the two are not always the same answer. */
export interface ScreenNavigation {
	getState(): StackState;
	getParent?(): OwningStack | undefined;
}

function nestedState(value: unknown): NestedState | null {
	if (typeof value !== "object" || value === null) return null;
	const { index, routes } = value as { index?: unknown; routes?: unknown };
	if (typeof index !== "number" || !Array.isArray(routes)) return null;
	return value as NestedState;
}

/** The route a person is looking at in a stack: the topmost one that is not a
 * sheet, since a sheet is a moment over the screen under it. */
function frontScreen(state: NestedState): NestedState["routes"][number] | undefined {
	for (let index = state.index; index >= 0; index--) {
		const route = state.routes[index];
		if (route && !isSheetRoute(route.name)) return route;
	}
	return undefined;
}

/** Whether a stack is showing this route: its front screen is the route, or
 * holds it in the navigator nested inside it. The route can be a sheet's own,
 * since a sheet is a route on the stack it is presented over: a stack that
 * holds it there is showing it, and `inFront` is what says so. */
function holds(state: NestedState, key: string): boolean {
	const front = frontScreen(state);
	if (!front) return false;
	if (front.key === key) return true;
	if (inFront(state as StackState, key)) return true;
	const inside = nestedState(front.state);
	return inside ? holds(inside, key) : false;
}

export function useScreenInFront(routeKey: string): boolean {
	// inFront already answers true for the focused route, so a useIsFocused
	// subscription would add nothing but a re-render on every sheet open and
	// close.
	return useNavigationState((state) => inFront(state, routeKey));
}

/** The same answer at the moment of a call, for a guard inside a callback.
 * A stack that still holds a screen and a reader still looking at it are two
 * questions: a screen pushed on the stack that owns this one leaves this
 * stack's routes untouched, so every stack above has to be showing it too. */
export function screenInFront(navigation: ScreenNavigation, routeKey: string): boolean {
	if (!inFront(navigation.getState(), routeKey)) return false;
	for (let holder = navigation.getParent?.(); holder; holder = holder.getParent?.()) {
		const state = nestedState(holder.getState());
		if (!state || !holds(state, routeKey)) return false;
	}
	return true;
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
