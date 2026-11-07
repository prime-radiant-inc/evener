import type { NavigationState } from "@react-navigation/native";
import type { Routes } from "./screens";

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

/** The conversation in the split's detail stack, under any sheet or page
 * opened over it, or null when the pane is on its placeholder. */
export function activeConversation(state: NavigationState): Routes["Conversation"] | null {
	const focused = state.routes[state.index];
	const nested = (focused as { state?: NavigationState } | undefined)?.state;
	if (focused?.name !== "Sessions" || !nested) return null;
	for (let index = nested.index; index >= 0; index--) {
		const route = nested.routes[index];
		if (route?.name === "Conversation") return route.params as Routes["Conversation"];
	}
	return null;
}
