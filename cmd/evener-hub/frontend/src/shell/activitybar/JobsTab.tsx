// The Jobs tab: running jobs first, then the completed, each opening its own
// transcript pane (the job log view) beside the session.

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
  const running = scope.leaf.running_jobs ?? [];
  const completed = scope.leaf.completed_jobs ?? [];
  if (running.length === 0 && completed.length === 0) {
    return <span className={CLASS.emptyNote}>No jobs at this level.</span>;
  }
  return (
    <div className={CLASS.stack}>
      {[...running, ...completed].map((job) => (
        <JobRow
          key={job.job_id}
          job={job}
          onOpen={() =>
            workspaceStore
              .getState()
              .openPane("transcript", { ref: `job:${job.job_id}`, parentRef: scope.leaf.ref }, { slot: "secondary" })
          }
        />
      ))}
    </div>
  );
}
