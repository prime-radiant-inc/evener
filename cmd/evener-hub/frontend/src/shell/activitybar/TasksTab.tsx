// The Tasks tab: the task list itself, unfolded in the sidebar - the same
// TasksPanelBody the tasks pane renders, fed by the shared thread-model
// subscription (stores/useThreadModel.ts). The navigation summary line rides
// on top when the wire carries one. No pane affordance: the tab IS where
// tasks live now.

import { TasksPanelBody, taskDisclosureId } from "../../panes/session/chrome/TasksPanel";
import { useTasksPanelStore } from "../../stores/tasksPanel";
import { useThreadModel } from "../../stores/useThreadModel";
import { requireClass } from "../../widgets/internal/requireClass";
import type { ActivityScope } from "../statusbar/statusScope";
import { useActivityScrollProgress } from "./ActivityViewport";
import styles from "./activitybar.module.css";

const CLASS = {
  stack: requireClass(styles.stack, "activitybar.module.css", "stack"),
  emptyNote: requireClass(styles.emptyNote, "activitybar.module.css", "emptyNote"),
  rowMeta: requireClass(styles.rowMeta, "activitybar.module.css", "rowMeta"),
};

export function TasksTab({ scope }: { scope: ActivityScope }) {
  const ref = scope.leaf.ref;
  const model = useThreadModel(ref);
  const tasks = scope.leaf.tasks;
  const entry = useTasksPanelStore((state) => state.entries.get(ref));
  useActivityScrollProgress(
    entry?.rows?.map((task) => taskDisclosureId(ref, task.id)) ?? [],
    !!entry && entry.rows !== null && !entry.loading && !entry.failure && !entry.daemonGone && !entry.unsupported,
  );
  if (!model) {
    return <span className={CLASS.emptyNote}>Loading tasks…</span>;
  }
  return (
    <div className={CLASS.stack}>
      {tasks !== undefined && tasks.total > 0 ? (
        <span className={CLASS.rowMeta}>
          {tasks.done} of {tasks.total} done{tasks.current ? ` · now: ${tasks.current}` : ""}
        </span>
      ) : null}
      <TasksPanelBody sessionRef={ref} model={model} />
    </div>
  );
}
