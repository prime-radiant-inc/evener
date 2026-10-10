import { ActivityPageBoundary } from "./ActivityPageBoundary";
// Current jobs stay visible; terminal history
// unfolds without changing the shared collection's visible-page demand.

import { activityNodeID, type JobActivityJob } from "@evener/appwire-client";
import { useSessionActivity } from "../../stores/sessionActivity";
import { Disclosure } from "../../widgets/disclosure";
import { requireClass } from "../../widgets/internal/requireClass";
import type { ActivityScope } from "../statusbar/statusScope";
import { workspaceStore } from "../workspace";
import { useActivityScrollProgress } from "./ActivityViewport";
import styles from "./activitybar.module.css";
import { JobRow } from "./activityRows";

const CLASS = {
  stack: requireClass(styles.stack, "activitybar.module.css", "stack"),
  emptyNote: requireClass(styles.emptyNote, "activitybar.module.css", "emptyNote"),
};

export function JobsTab({ scope }: { scope: ActivityScope }) {
  const { snapshot, loadMore } = useSessionActivity(scope.leaf.ref, "session", "jobs");
  const collection = snapshot?.jobs;
  useActivityScrollProgress(
    collection?.rows.map((job) => activityNodeID({ ...job, kind: "shell" })) ?? [],
    collection?.complete ?? false,
    collection?.hasMore ?? false,
  );
  if (collection?.permanent && collection.rows.length === 0)
    return <span className={CLASS.emptyNote}>Jobs unavailable for this session.</span>;
  if (!collection || (collection.rows.length === 0 && !collection.complete))
    return <span className={CLASS.emptyNote}>Loading jobs…</span>;
  const running = collection.rows.filter((job) => !job.terminal);
  const completed = collection.rows.filter((job) => job.terminal);
  if (collection.rows.length === 0 && collection.complete)
    return <span className={CLASS.emptyNote}>No jobs at this level.</span>;
  const renderJob = (job: JobActivityJob) => (
    <JobRow
      key={activityNodeID({ ...job, kind: "shell" })}
      job={job}
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
    <div className={CLASS.stack}>
      {running.map(renderJob)}
      {completed.length > 0 ? (
        <Disclosure
          id={`${scope.leaf.ref}\0completed-jobs`}
          summary={
            <span>
              {completed.length} completed {completed.length === 1 ? "job" : "jobs"}
            </span>
          }
        >
          <div className={CLASS.stack}>{completed.map(renderJob)}</div>
        </Disclosure>
      ) : null}
      <ActivityPageBoundary
        resource="jobs"
        label="jobs"
        rows={collection.rows}
        hasMore={collection.hasMore}
        loading={collection.loading}
        error={collection.error}
        permanent={collection.permanent}
        loadMore={loadMore}
      />
      {collection.error && !collection.permanent ? <span className={CLASS.emptyNote}>Jobs are updating…</span> : null}
    </div>
  );
}
