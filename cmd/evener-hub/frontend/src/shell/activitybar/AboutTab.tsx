import { DetailsPanelBody } from "../../panes/session/chrome/DetailsPanel";
import { NOW_TICK_MS, useNowTick } from "../../panes/session/liveness";
import { useThreadModel } from "../../stores/useThreadModel";
import { Skeleton } from "../../widgets";
import type { ActivityScope } from "../statusbar/statusScope";

export function AboutTab({ scope }: { scope: ActivityScope }) {
  const model = useThreadModel(scope.leaf.ref);
  const now = useNowTick(NOW_TICK_MS);
  if (!model) return <Skeleton lines={2} />;
  return <DetailsPanelBody model={model} now={now} />;
}
