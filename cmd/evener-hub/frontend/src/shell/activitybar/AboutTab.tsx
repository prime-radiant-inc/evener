import { DetailsPanelBody } from "../../panes/session/chrome/DetailsPanel";
import { SessionLinksSection } from "../../panes/session/chrome/SessionLinksSection";
import { useNowTick, WORK_TICK_MS } from "../../panes/session/liveness";
import { useThreadModel } from "../../stores/useThreadModel";
import { Skeleton } from "../../widgets";
import { useLocalSessionBranch } from "../gitLocation";
import { refParam } from "../routing";
import type { ActivityScope } from "../statusbar/statusScope";
import { useWorkspaceStore } from "../workspace";

export function AboutTab({ scope }: { scope: ActivityScope }) {
  const model = useThreadModel(scope.leaf.ref);
  const now = useNowTick(WORK_TICK_MS);
  const resolvedBranch = useLocalSessionBranch(scope.leaf.ref, model);
  const sourcePaneId = useWorkspaceStore((state) => {
    const matches = (pane: (typeof state.panes)[number]) =>
      (pane.type === "session" || pane.type === "transcript") && refParam(pane.params) === scope.leaf.ref;
    const focused = state.panes.find((pane) => pane.id === state.focusedPaneId);
    if (focused && matches(focused)) return focused.id;
    return state.panes.find(matches)?.id;
  });
  if (!model) return <Skeleton lines={2} />;
  return (
    <div>
      <DetailsPanelBody model={model} now={now} resolvedBranch={resolvedBranch} />
      <SessionLinksSection sessionRef={scope.leaf.ref} model={model} sourcePaneId={sourcePaneId} />
    </div>
  );
}
