// What the stack means for alerts (spec 13.3). The screen in front decides
// what alerts at all: the Board lists notices, and a session answers its own
// alerts. A sheet or modal on top holds banners, since a banner can't show
// above one (ruling 7), and leaves the screen under it in front, as phase 2
// part 3's inFront does for sheets.
import { isSheetRoute } from "../sheet/sheetRoutes";
import type { AlertScreen } from "./alertCenter";

/** Phase 5's modal routes: the Hub and New session sheets. */
const MODAL_ROUTES: ReadonlySet<string> = new Set(["Hub", "NewSession"]);

type Route = { name: string; params?: object };

/** Whether this route, on top, covers the app, so banners wait. */
export function coversBanners(route: Route | undefined): boolean {
	return route !== undefined && (isSheetRoute(route.name) || MODAL_ROUTES.has(route.name));
}

/** The screen alerts care about, from the stack's routes up to the focused
 * one: the top route that isn't a sheet or a modal. A session screen counts
 * only for the hub it belongs to, since refs are per hub. */
export function alertScreenFor(routes: readonly Route[], hubId: string | null): AlertScreen {
	const screen = [...routes].reverse().find((route) => !coversBanners(route));
	if (screen?.name === "Sessions") return { kind: "board" };
	const params = screen?.params as { hubId?: unknown; ref?: unknown } | undefined;
	if (screen?.name === "Conversation" && params?.hubId === hubId && typeof params.ref === "string")
		return { kind: "session", ref: params.ref };
	return { kind: "other" };
}
