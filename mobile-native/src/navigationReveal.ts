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
	 * top-level session, the owning row (its nearest non-subagent ancestor) for
	 * a subagent: the hub answers a subagent's location with ref = the subagent
	 * and top_level_ref = that row. */
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
	// A subagent has no row of its own: the hub answers its location with
	// top_level_ref = the row that owns it (D5). Every other ref -- a root, a
	// fork original or a cluster member -- has its own row, even when the hub
	// marks it non-top-level, so reveal it directly. The archived list is the
	// exception: it keeps a fork original inside its continuation's row, so a
	// non-top-level archived session is revealed through top_level_ref, as the
	// web's rail does.
	const carried = location.session?.kind === "subagent" || (location.tier === "archived" && !location.top_level);
	const revealRef = carried ? (location.top_level_ref ?? ref) : ref;
	if (location.project_key) {
		if (!["current", "recent", "archived"].includes(location.tier ?? ""))
			throw new Error("The hub returned an unknown project section.");
		return {
			ref,
			revealRef,
			title: location.session.project || "Project",
			params: {
				resource: "project_page",
				projectKey: location.project_key,
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
