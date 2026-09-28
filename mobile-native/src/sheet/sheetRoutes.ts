// The redesign's sheets are native-stack formSheet routes (spec 6: medium and
// large detents, and a swipe down closes them). This module is their one list
// and their sizes, and the rule the app follows because a sheet is a route:
// a screen covered only by sheets is still the screen in front.
import type { NativeStackNavigationOptions } from "@react-navigation/native-stack";
import type { Routes } from "../screens";

/** The heights a sheet rests at: medium, about half the screen, and large,
 * the full sheet. */
export type Detent = "medium" | "large";

/** Each detent as the fraction of the tallest sheet that native-stack's
 * `sheetAllowedDetents` takes. iOS 16 and later turn fractions into custom
 * detents (react-native-screens, RNSScreen.mm). */
const FRACTION: Record<Detent, number> = { medium: 0.5, large: 1 };

/** The native-stack options for a sheet that rests at `detents` and opens at
 * `initial`. The sheet draws its own header (`Sheet.tsx`), so the native one
 * is off, and a sheet that can change size shows the grabber. */
export function sheetOptions(detents: readonly Detent[], initial: Detent): NativeStackNavigationOptions {
	const ordered = (["medium", "large"] as const).filter((detent) => detents.includes(detent));
	const index = ordered.indexOf(initial);
	if (index === -1)
		throw new Error(`A sheet can't open at ${initial}: it rests only at ${ordered.join(" and ") || "no size"}.`);
	return {
		presentation: "formSheet",
		headerShown: false,
		sheetAllowedDetents: ordered.map((detent) => FRACTION[detent]),
		sheetInitialDetentIndex: index,
		sheetGrabberVisible: ordered.length > 1,
	};
}

/** Every route presented as a sheet, with its size. Each sheet's spec section
 * names its size, and pickers open at medium. A sheet joins by adding its line
 * here and its `Stack.Screen`, with these options, in App.tsx's sheet group. */
export const SHEET_ROUTES = {
	TasksSheet: sheetOptions(["medium", "large"], "medium"),
	NotesSheet: sheetOptions(["medium", "large"], "large"),
	// The Board row's menu (phase 2 Task 12.6) opens at half height.
	RowMenuSheet: sheetOptions(["medium", "large"], "medium"),
	// The whole queue, when more wait than the composer shows (ruling 18).
	QueueSheet: sheetOptions(["medium", "large"], "medium"),
	// The Reader's headings, to jump to one (ruling 26).
	OutlineSheet: sheetOptions(["medium", "large"], "medium"),
	// You type in it, so it opens at full height (ruling 26).
	CommentSheet: sheetOptions(["medium", "large"], "large"),
	CommentsSheet: sheetOptions(["medium", "large"], "medium"),
	// You type the review's note in it (ruling 26).
	ReviewSheet: sheetOptions(["medium", "large"], "large"),
	// The Session sheet opens at full height (spec 8.6).
	SessionInfoSheet: sheetOptions(["medium", "large"], "large"),
	// A picker, so it opens at half height (spec 8.5).
	ModelSheet: sheetOptions(["medium", "large"], "medium"),
	// A picker, so it opens at half height (spec 8.5).
	CommandsSheet: sheetOptions(["medium", "large"], "medium"),
} satisfies { [Name in keyof Routes]?: NativeStackNavigationOptions };

export function isSheetRoute(name: string): boolean {
	return Object.hasOwn(SHEET_ROUTES, name);
}

/** The part of a stack navigator's state these rules read. */
export interface StackState {
	index: number;
	routes: readonly { key: string; name: string }[];
}

/** Whether the route is the screen in front: the focused route, or one that
 * only sheets cover. A sheet is part of the screen under it, so that screen
 * keeps following its data and keeps its actions allowed; a screen pushed over
 * it still takes it out of the front, as focus does. */
export function inFront(state: StackState, key: string): boolean {
	const position = state.routes.findIndex((route) => route.key === key);
	if (position === -1 || position > state.index) return false;
	return state.routes.slice(position + 1, state.index + 1).every((route) => isSheetRoute(route.name));
}
