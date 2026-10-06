import { DetailsPanelBody } from "../../panes/session/chrome/DetailsPanel";
import { NOW_TICK_MS, useNowTick } from "../../panes/session/liveness";
import { useThreadModel } from "../../stores/useThreadModel";
import type { ActivityScope } from "../statusbar/statusScope";

export function AboutTab({ scope }: { scope: ActivityScope }) {
  const model = useThreadModel(scope.leaf.ref);
  const now = useNowTick(NOW_TICK_MS);
  if (!model) return <p role="status">Loading session details…</p>;
  return <DetailsPanelBody model={model} now={now} />;
}
