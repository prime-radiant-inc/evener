import type { ThreadModel } from "@evener/appwire-client";
import { DetailsPanelBody } from "../../panes/session/chrome/DetailsPanel";
import { NOW_TICK_MS, useNowTick } from "../../panes/session/liveness";
import { useThreadModel } from "../../stores/useThreadModel";
import { Skeleton } from "../../widgets";
import { useClient } from "../clientContext";
import { useHeadBranch } from "../gitLocation";
import type { ActivityScope } from "../statusbar/statusScope";

// A local session's About is the phone's only branch surface: the footer's
// RepoLocation line is a desktop line, and nothing populates the wire's
// thread.gitInfo on the hub's local read paths, so the model alone cannot
// answer "what branch is this on". Resolve the branch at view time from the
// session's cwd on this hub - the same evener/git/head lookup the session
// location line and the Spawn pane's branch chip use - for local refs only:
// a source-backed session's cwd belongs to another host, whose repositories
// this hub cannot speak for. A wire-carried gitBranch wins and asks nothing.
function useLocalSessionBranch(ref: string, model: ThreadModel | undefined): string {
  const client = useClient();
  const cwd = model?.cwd ?? "";
  return useHeadBranch(client, cwd, ref.startsWith("local:") && (model?.gitBranch ?? "") === "");
}

export function AboutTab({ scope }: { scope: ActivityScope }) {
  const model = useThreadModel(scope.leaf.ref);
  const now = useNowTick(NOW_TICK_MS);
  const resolvedBranch = useLocalSessionBranch(scope.leaf.ref, model);
  if (!model) return <Skeleton lines={2} />;
  return <DetailsPanelBody model={model} now={now} resolvedBranch={resolvedBranch} />;
}
