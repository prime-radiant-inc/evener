import { ActivityPageBoundary } from "./ActivityPageBoundary";
// The Jobs tab: running jobs first, then the completed, each opening its own
// transcript pane (the job log view) beside the session.

import { activityNodeID } from "@evener/appwire-client";
import { useSessionActivity } from "../../stores/sessionActivity";
import { requireClass } from "../../widgets/internal/requireClass";
import type { ActivityScope } from "../statusbar/statusScope";
import { workspaceStore } from "../workspace";
import styles from "./activitybar.module.css";
import { JobRow } from "./activityRows";

const CLASS = {
  stack: requireClass(styles.stack, "activitybar.module.css", "stack"),
  emptyNote: requireClass(styles.emptyNote, "activitybar.module.css", "emptyNote"),
};

export function JobsTab({ scope }: { scope: ActivityScope }) {
  const { snapshot, loadMore } = useSessionActivity(scope.leaf.ref, "session", "jobs");
  const collection = snapshot?.jobs;
  if (collection?.permanent && collection.rows.length === 0)
    return <span className={CLASS.emptyNote}>Jobs unavailable for this session.</span>;
  if (!collection || (collection.rows.length === 0 && !collection.complete))
    return <span className={CLASS.emptyNote}>Loading jobs…</span>;
  const running = collection.rows.filter((job) => !job.terminal);
  const completed = collection.rows.filter((job) => job.terminal);
  if (collection.rows.length === 0 && collection.complete)
    return <span className={CLASS.emptyNote}>No jobs at this level.</span>;
  return (
    <div className={CLASS.stack}>
      {[...running, ...completed].map((job) => (
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
      ))}
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
