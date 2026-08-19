// needsYouCycle.ts: the shared logic behind Mod+J (AppShell.tsx) and the
// palette's "Go to next session needing you" command/empty-query view
// (shell/palette/commands.ts, CommandPalette.tsx) - one place decides which
// session is "next", so a keyboard-only chord and its palette-visible
// counterpart can never disagree.
//
// needs_you is already the server's own ordered list of sessions currently
// wanting the user (stores/tree.ts's TreeResponse.needs_you) - tree order,
// not rediscovered here. Opening a hit goes through the exact seam the
// rail's own row activation uses (Rail.tsx's openSession: navigate() to the
// session's /s/{ref} URL) rather than poking workspaceStore directly - that
// lets AppShell's existing route-placement effect do the top-level/nested
// resolution (topLevelAncestorRef) it already owns, and keeps the address
// bar honest, which a direct openTopLevelSession/openNestedSessionWithOwner
// call would not: AppShell reconciles the CURRENT pathname against the
// workspace on every pane change, and a pane change with no matching URL
// change is exactly what that reconciliation exists to undo.
import type { TreeResponse } from "../../stores/tree";
import { navigate, paneToURL } from "../routing";

/** The needs-you sessions' refs, in the server's own tree order. Empty
 * before the first tree load. */
export function needsYouRefs(tree: TreeResponse | null): string[] {
  return tree ? tree.needs_you.map((n) => n.ref) : [];
}

/** The ref to cycle to next: the one after `currentRef` in `refs`, wrapping
 * past the end back to the first. Starts at the first ref when nothing is
 * currently focused, or when the focused session isn't itself in the
 * needs-you list (it doesn't need you, so there's no "next from here" to
 * resume). Null only when there's nothing to cycle to at all. */
export function nextNeedsYouRef(refs: string[], currentRef: string | null): string | null {
  if (refs.length === 0) return null;
  const idx = currentRef === null ? -1 : refs.indexOf(currentRef);
  if (idx === -1) return refs[0] ?? null;
  return refs[(idx + 1) % refs.length] ?? null;
}

/** Opens `ref` the same way clicking its rail row would: navigate to its
 * /s/{ref} URL. AppShell's own route-placement effect (openRouteAsPane)
 * resolves nested-vs-top-level from there, exactly as it does for every
 * other /s/{ref} navigation (a search result, a rail row, a share link). */
export function openNeedsYouSession(ref: string): void {
  const url = paneToURL("session", { ref });
  if (url !== null) navigate(url);
}
