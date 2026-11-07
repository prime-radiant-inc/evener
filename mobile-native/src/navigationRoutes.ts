import type { NavigationState } from "@react-navigation/native";

/** The root stack's routes up to the focused one, as alerts read them. The
 * focused Sessions route's split appends the detail stack, so an alert knows
 * which conversation and sheet are actually on screen. */
export function stackRoutes(state: NavigationState) {
	const routes = state.routes.slice(0, state.index + 1).map((route) => ({ name: route.name, params: route.params }));
	const focused = state.routes[state.index];
	const nested = (focused as { state?: NavigationState } | undefined)?.state;
	if (focused?.name === "Sessions" && nested) {
		routes.push(
			...nested.routes.slice(0, nested.index + 1).map((route) => ({ name: route.name, params: route.params })),
		);
	}
	return routes;
}
