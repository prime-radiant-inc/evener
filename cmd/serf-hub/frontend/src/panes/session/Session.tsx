import { EmptyState, PaneScaffold } from "../../widgets";
import type { PaneProps } from "../../shell/paneRegistry";

export interface SessionPaneParams {
  ref: string;
}

/**
 * Placeholder for the real transcript pane (Wave 4). Shows the ref it was
 * opened with - proving routing and workspace mechanics (deep-link ->
 * openPane -> a dockview panel with the right params) work end to end -
 * without any of the real session content those come with. The dockview
 * tab's own title (shell/paneRegistry.ts's "session" descriptor, computed
 * by DockHost) separately tracks the live ThreadModel name via
 * PaneTitleCtx; this component stays a simple, literal proof rather than
 * duplicating that lookup for a view Wave 4 replaces outright.
 *
 * This placeholder has nothing to lose on a tab switch, but its real
 * replacement will: dockview unmounts a pane's whole tree when its tab
 * isn't active (see PaneHost's own comment in shell/DockHost.tsx), so the
 * real transcript view must keep anything durable (thread state, an
 * in-progress draft) in a store, not component-local state.
 */
export default function Session({ params }: PaneProps<SessionPaneParams>) {
  return (
    <PaneScaffold title={params.ref}>
      <EmptyState title="Transcript arrives in wave 4" />
    </PaneScaffold>
  );
}
