// Shared by every place in the app that needs a session's human-readable
// title before its transcript has necessarily hydrated: DockHost's own
// dockview tab title (shell/DockHost.tsx) and this pane's own in-pane header
// (Session.tsx's PaneScaffold title). Both draw on the SAME two data
// sources, checked in the SAME order, so the two titles never disagree -
// the live ThreadModel's name wins the moment it's hydrated, falling back
// to the rail's already-loaded tree/session-index snapshot (findSessionNode)
// for a pane opened before hydration completes. Neither source having a
// name yet resolves to undefined, leaving the raw ref as each caller's own
// last resort.
import type { ThreadModel } from "../../protocol/model";
import { findSessionNode, type TreeResponse } from "../../stores/tree";

export function resolveThreadName(
  threads: Map<string, ThreadModel>,
  tree: TreeResponse | null,
  ref: string,
): string | undefined {
  const live = threads.get(ref)?.name;
  if (live !== undefined) return live;
  return tree ? findSessionNode(tree, ref)?.title : undefined;
}
