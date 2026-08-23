import { type JSX, useState } from "react";
import type { TaskGroup, TaskGroupStatus } from "../../services/activity";
import { SectionHeader } from "./ActivitySheet";

export interface TaskSectionProps {
  readonly tasks: TaskGroup[];
}

const STATUS_LABELS: Record<TaskGroupStatus, string> = {
  active: "Active",
  open: "Open",
  done: "Done",
};

/**
 * Tasks section — grouped by active, open, and done. The collapsed summary
 * shows the done count; expanded shows the full active/open/done breakdown.
 */
export function TaskSection({ tasks }: TaskSectionProps): JSX.Element {
  const [expanded, setExpanded] = useState(false);

  const doneGroup = tasks.find((g) => g.status === "done");
  const summary = doneGroup ? `${doneGroup.count} done` : "";

  return (
    <section className="evener-activity-section">
      <SectionHeader
        label="Tasks"
        expanded={expanded}
        onToggle={() => setExpanded((e) => !e)}
      >
        {summary}
      </SectionHeader>
      {expanded ? (
        <div className="evener-activity-section__detail">
          {tasks.length === 0 ? (
            <p className="evener-activity-section__empty">No task data</p>
          ) : (
            <ul className="evener-activity-task-list">
              {tasks.map((g) => (
                <li key={g.status} className="evener-activity-task-list__item">
                  <span className="evener-activity-task-list__status">
                    {STATUS_LABELS[g.status]}
                  </span>
                  <span className="evener-activity-task-list__count">
                    {g.status} {g.count}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </div>
      ) : null}
    </section>
  );
}
