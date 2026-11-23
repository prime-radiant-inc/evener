// Current jobs stay visible; terminal history
// unfolds without changing the shared collection's visible-page demand.

import { activityNodeID, type JobActivityJob } from "@evener/appwire-client";
import { useSessionActivity } from "../../stores/sessionActivity";
import { Disclosure } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { workspaceStore } from "../workspace";
import { ActivityCollectionGate } from "./ActivityCollectionGate";
import { useActivityScrollProgress } from "./ActivityViewport";
import styles from "./activitybar.module.css";
import { JobRow } from "./activityRows";
import type { ActivityTabBodyProps } from "./activityTabs";

const CLASS = {
  stack: requireClass(styles.stack, "activitybar.module.css", "stack"),
};

export function JobsTab({ scope, now }: ActivityTabBodyProps) {
  const { snapshot, loadMore } = useSessionActivity(scope.leaf.ref, "session", "jobs");
  const collection = snapshot?.jobs;
  const summary = snapshot?.summary?.jobs;
  const completedTotal = summary?.known ? summary.total - summary.active : null;
  useActivityScrollProgress(
    collection?.rows.map((job) => activityNodeID({ ...job, kind: "shell" })) ?? [],
    collection?.complete ?? false,
    collection?.hasMore ?? false,
  );
  const rows = collection?.rows ?? [];
  const running = rows.filter((job) => !job.terminal);
  const completed = rows.filter((job) => job.terminal);
  const renderJob = (job: JobActivityJob) => (
    <JobRow
      key={activityNodeID({ ...job, kind: "shell" })}
      job={job}
      now={now}
      onOpen={
        job.transcriptRef
          ? () =>
              workspaceStore
                .getState()
                .openPane("transcript", { ref: job.transcriptRef, parentRef: job.ownerRef }, { slot: "secondary" })
          : undefined
      }
    />
  );
  return (
    <ActivityCollectionGate noun="jobs" collection={collection} summary={summary} loadMore={loadMore}>
      {running.map(renderJob)}
      {completed.length > 0 ? (
        <Disclosure
          id={`${scope.leaf.ref}\0completed-jobs`}
          summary={
            <span>
              {completedTotal === null
                ? "Completed jobs"
                : `${completedTotal} completed ${completedTotal === 1 ? "job" : "jobs"}`}
            </span>
          }
        >
          <div className={CLASS.stack}>{completed.map(renderJob)}</div>
        </Disclosure>
      ) : null}
    </ActivityCollectionGate>
  );
}
