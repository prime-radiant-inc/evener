// TasksPanel: a trigger + Sheet showing the session's task-list aggregate
// (model.tasks: {total, done}). That aggregate is push-driven ONLY
// (serf/task/updated, protocol/reducer.ts's own case) and seeded null at
// hydrate - never populated from the thread/read snapshot itself
// (hydrateThread: `tasks: null`) - so a session that hasn't had a live
// task-count update yet has genuinely nothing to show beyond an honest
// empty state, not a false "zero tasks" claim.
//
// The per-task row list (id/description/status/...) has no live data
// source this wave: serf/tasks/list's real response shape was investigated
// and pinned wire-true (see taskData.ts's parseTaskListData + its own
// citations), but no threads-store action fetches it (stores/threads.ts is
// T1's frozen chokepoint, outside this stream's manifest, and the wave's
// own binding constraint requires every wire call to go through a
// threads-store action) - see this stream's report for the exact
// NEEDS_CONTEXT gap. parseTaskListData ships ready to wire in once that
// action exists.
import { useState } from "react";
import type { ThreadModel } from "../../../protocol/model";
import { Button, EmptyState, Sheet } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./taskspanel.module.css";

export interface TasksPanelProps {
  model: ThreadModel;
}

const CLASS = {
  summary: requireClass(styles.summary, "taskspanel.module.css", "summary"),
};

function triggerLabel(tasks: ThreadModel["tasks"]): string {
  return tasks ? `Tasks ${tasks.done}/${tasks.total}` : "Tasks";
}

export function TasksPanel({ model }: TasksPanelProps) {
  const [open, setOpen] = useState(false);
  const tasks = model.tasks;

  return (
    <>
      <Button variant="quiet" size="sm" onClick={() => setOpen(true)}>
        {triggerLabel(tasks)}
      </Button>
      <Sheet open={open} onClose={() => setOpen(false)} title="Tasks">
        {tasks ? (
          <p className={CLASS.summary}>
            {tasks.done} of {tasks.total} done
          </p>
        ) : (
          <EmptyState
            title="No task activity yet"
            hint="Task counts appear here once the agent starts using its task list."
          />
        )}
      </Sheet>
    </>
  );
}
