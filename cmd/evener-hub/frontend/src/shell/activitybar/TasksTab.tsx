// The Tasks tab: the task list itself, unfolded in the sidebar - the same
// TasksPanelBody the tasks pane renders, fed by the shared thread-model
// subscription (stores/useThreadModel.ts). Counts live in the tab label;
// the body starts with the task groups.

import { TaskActivityTime } from "../../panes/session/chrome/activityTime";
import { TasksPanelBody, taskDisclosureId } from "../../panes/session/chrome/TasksPanel";
import { useTasksPanelStore } from "../../stores/tasksPanel";
import { useThreadModel } from "../../stores/useThreadModel";
import { Skeleton } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { useActivityScrollProgress } from "./ActivityViewport";
import styles from "./activitybar.module.css";
import type { ActivityTabBodyProps } from "./activityTabs";

const CLASS = {
  stack: requireClass(styles.stack, "activitybar.module.css", "stack"),
};

export function TasksTab({ scope, now }: ActivityTabBodyProps) {
  const ref = scope.leaf.ref;
  const model = useThreadModel(ref);
  const entry = useTasksPanelStore((state) => state.entries.get(ref));
  useActivityScrollProgress(
    entry?.rows?.map((task) => taskDisclosureId(ref, task.id)) ?? [],
    !!entry && entry.rows !== null && !entry.loading && !entry.failure && !entry.daemonGone && !entry.unsupported,
  );
  if (!model) {
    return <Skeleton lines={2} />;
  }
  return (
    <div className={CLASS.stack}>
      <TasksPanelBody
        sessionRef={ref}
        model={model}
        showSummary={false}
        renderTime={(task) => <TaskActivityTime task={task} now={now} />}
      />
    </div>
  );
}
