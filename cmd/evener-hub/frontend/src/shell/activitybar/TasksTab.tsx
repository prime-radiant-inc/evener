// The Tasks tab: the navigation summary line (done/total and the current
// task) plus an Open affordance into the existing tasks pane. Embedding the
// full TasksPanelBody would force a thread subscription for a glance surface;
// the pane is one click away and already does it right.

import { Button } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import type { ActivityScope } from "../statusbar/statusScope";
import { workspaceStore } from "../workspace";
import styles from "./activitybar.module.css";

const CLASS = {
  stack: requireClass(styles.stack, "activitybar.module.css", "stack"),
  emptyNote: requireClass(styles.emptyNote, "activitybar.module.css", "emptyNote"),
  rowMeta: requireClass(styles.rowMeta, "activitybar.module.css", "rowMeta"),
};

export function TasksTab({ scope }: { scope: ActivityScope }) {
  const tasks = scope.leaf.tasks;
  if (tasks === undefined || tasks.total === 0) {
    return <span className={CLASS.emptyNote}>No task list for this session.</span>;
  }
  return (
    <div className={CLASS.stack}>
      <span className={CLASS.rowMeta}>
        {tasks.done} of {tasks.total} done{tasks.current ? ` · now: ${tasks.current}` : ""}
      </span>
      <div>
        <Button
          variant="quiet"
          size="sm"
          onClick={() =>
            workspaceStore.getState().openPane("sessionTasks", { ref: scope.leaf.ref }, { slot: "secondary" })
          }
        >
          Open tasks
        </Button>
      </div>
    </div>
  );
}
