import { SessionLinksSection } from "../../panes/session/chrome/SessionLinksSection";
import { useThreadModel } from "../../stores/useThreadModel";
import { Skeleton } from "../../widgets";
import { refParam } from "../routing";
import type { ActivityScope } from "../statusbar/statusScope";
import { useWorkspaceStore } from "../workspace";

export function LinksTab({ scope, now }: { scope: ActivityScope; now: number }) {
  const model = useThreadModel(scope.leaf.ref);
  const sourcePaneId = useWorkspaceStore((state) => {
    const matches = (pane: (typeof state.panes)[number]) =>
      (pane.type === "session" || pane.type === "transcript") && refParam(pane.params) === scope.leaf.ref;
    const focused = state.panes.find((pane) => pane.id === state.focusedPaneId);
    if (focused && matches(focused)) return focused.id;
    return state.panes.find(matches)?.id;
  });
  if (!model) return <Skeleton lines={2} />;
  return <SessionLinksSection sessionRef={scope.leaf.ref} model={model} sourcePaneId={sourcePaneId} now={now} />;
}
