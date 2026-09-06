import type {
  NavigationReadParams,
  NavigationSessionLocation,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import type { NavigationPages } from "./navigationPages";

export interface SessionLocation {
  ref: string;
  title: string;
  params: NavigationReadParams;
}
export async function locateSession(
  client: ConversationClientLike,
  ref: string,
  signal?: AbortSignal,
): Promise<SessionLocation> {
  const response = await client.request("evener/navigation/read", {
    resource: "location",
    ref,
  });
  if (signal?.aborted)
    throw new Error(
      "The location request was cancelled when you left the session.",
    );
  const location = response.data as NavigationSessionLocation | undefined;
  if (
    response.status !== "ok" ||
    location?.ref !== ref ||
    location.session?.ref !== ref
  )
    throw new Error(
      "This session could not be located. Refresh and try again.",
    );
  if (location.project_key) {
    if (!["current", "recent", "archived"].includes(location.tier ?? ""))
      throw new Error("The hub returned an unknown project section.");
    return {
      ref,
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
      title: "Pinned sessions",
      params: { resource: "pin_section", sectionId: location.pin_section_id },
    };
  const section = location.tier === "needs_you" ? "needs_you" : "live";
  return {
    ref,
    title: section === "needs_you" ? "Needs you" : "Live sessions",
    params: { resource: "section", section },
  };
}

function pathTo<T>(
  roots: readonly T[],
  target: string,
  key: (row: T) => string,
  children: ((row: T) => readonly T[]) | undefined,
): string[] | null {
  const seen = new Set<string>();
  function visit(rows: readonly T[], ancestors: string[]): string[] | null {
    for (const row of rows) {
      const ref = key(row);
      if (seen.has(ref)) continue;
      seen.add(ref);
      const path = [...ancestors, ref];
      if (ref === target) return path;
      const found = children && visit(children(row), path);
      if (found) return found;
    }
    return null;
  }
  return visit(roots, []);
}
/** Follow the server's pages without crossing revisions or retaining abandoned work. */
export async function revealNavigationRow<T>(
  pages: NavigationPages<T>,
  ref: string,
  key: (row: T) => string,
  children: ((row: T) => readonly T[]) | undefined,
  isCurrent: () => boolean,
) {
  await pages.refresh();
  while (isCurrent()) {
    const state = pages.getSnapshot();
    if (state.loading || !state.loaded)
      throw new Error(
        "The list is refreshing. Try locating the session again.",
      );
    if (state.error || state.stale)
      throw new Error(
        state.error || "This list changed. Refresh to locate the session.",
      );
    const path = pathTo(state.rows, ref, key, children);
    if (path) return path;
    if (!state.remaining)
      throw new Error(
        "The session is not in the returned list. It may have moved or the hub may have omitted part of its tree.",
      );
    await pages.more();
  }
  return null;
}
