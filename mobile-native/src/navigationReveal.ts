import type { NavigationReadParams, NavigationSessionLocation } from "@evener/appwire-client";
import {
	decodeNavigationResponse,
	materializeSnapshot,
	type ResourceKey,
} from "@evener/appwire-client/state/navigation";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import type { PageSource } from "./navigationPages";

export interface SessionLocation {
	ref: string;
	/** The page row to scroll to and highlight. The location's own ref for a
	 * top-level session, and for any other (a subagent, a nested fork original)
	 * the top-level row that carries it: the hub answers their locations with
	 * top_level_ref = that row. */
	revealRef: string;
	title: string;
	params: Omit<NavigationReadParams, "representationVersion">;
}
export async function locateSession(
	client: ConversationClientLike,
	ref: string,
	signal?: AbortSignal,
): Promise<SessionLocation> {
	const response = await client.request("evener/navigation/read", {
		representationVersion: 3,
		resource: "location",
		ref,
	});
	if (signal?.aborted) throw new Error("The location request was cancelled when you left the session.");
	let location: NavigationSessionLocation | undefined;
	try {
		const key: ResourceKey = { kind: "location", ref };
		const decoded = decodeNavigationResponse(key, undefined, response);
		if (decoded.status === "snapshot")
			location = materializeSnapshot(key, decoded) as unknown as NavigationSessionLocation;
	} catch {
		location = undefined;
	}
	if (response.status !== "ok" || location?.ref !== ref || location.session?.ref !== ref)
		throw new Error("This session could not be located. Try again.");
	// Only a top-level session has a row of its own: navigation lists top-level
	// rows alone, and the archived list keeps a fork original inside its
	// continuation's row. The hub answers any other location (a subagent, a
	// nested fork original) with top_level_ref = the top-level row that
	// carries it, so reveal that row. (The web's rail does the same outside
	// the archived tier, which it renders with fork originals inline.)
	const revealRef = location.top_level ? ref : location.top_level_ref;
	if (location.project_key) {
		if (!["current", "recent", "archived"].includes(location.tier ?? ""))
			throw new Error("The hub returned an unknown project section.");
		return {
			ref,
			revealRef,
			title: location.session.project || "Project",
			// Name the location's catalog: the key may be in several. An older
			// hub sends none.
			params: {
				resource: "project_page",
				projectKey: location.project_key,
				...(location.catalog ? { catalog: location.catalog } : {}),
				tier: location.tier,
			},
		};
	}
	if (location.pin_section_id)
		return {
			ref,
			revealRef,
			title: "Pinned sessions",
			params: { resource: "pin_section", sectionId: location.pin_section_id },
		};
	const section = location.tier === "needs_you" ? "needs_you" : "live";
	return {
		ref,
		revealRef,
		title: section === "needs_you" ? "Needs you" : "Live sessions",
		params: { resource: "section", section },
	};
}

/** Follow the server's pages until the row appears, without crossing
 * revisions or retaining abandoned work. True once it's found; false once the
 * caller has moved on. */
export async function revealNavigationRow<T>(
	pages: PageSource<T>,
	ref: string,
	key: (row: T) => string,
	isCurrent: () => boolean,
): Promise<boolean> {
	await pages.refresh();
	while (isCurrent()) {
		const state = pages.getSnapshot();
		if (state.loading || !state.loaded) throw new Error("The list is refreshing. Try locating the session again.");
		if (state.error || state.stale)
			throw new Error(state.error || "The list changed while locating the session. Locate again.");
		if (state.rows.some((row) => key(row) === ref)) return true;
		if (!state.remaining) throw new Error("The session is not in the returned list. It may have moved.");
		await pages.more();
	}
	return false;
}
