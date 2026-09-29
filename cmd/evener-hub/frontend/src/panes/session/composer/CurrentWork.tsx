import { requireClass } from "../../../widgets/internal/requireClass";
import { GoalGlyph } from "../GoalGlyph";
import styles from "./currentwork.module.css";

export interface CurrentWorkProps {
  task?: string;
  goal?: string;
  goalStatus?: string;
  onOpenTasks(): void;
  onEditGoal(): void;
}

const CLASS = {
  currentWork: requireClass(styles.currentWork, "currentwork.module.css", "currentWork"),
  task: requireClass(styles.task, "currentwork.module.css", "task"),
  goal: requireClass(styles.goal, "currentwork.module.css", "goal"),
  dot: requireClass(styles.dot, "currentwork.module.css", "dot"),
  flag: requireClass(styles.flag, "currentwork.module.css", "flag"),
  label: requireClass(styles.label, "currentwork.module.css", "label"),
  value: requireClass(styles.value, "currentwork.module.css", "value"),
  status: requireClass(styles.status, "currentwork.module.css", "status"),
  statusBlocked: requireClass(styles.statusBlocked, "currentwork.module.css", "statusBlocked"),
  link: requireClass(styles.link, "currentwork.module.css", "link"),
  divider: requireClass(styles.divider, "currentwork.module.css", "divider"),
  visuallyHidden: requireClass(styles.visuallyHidden, "currentwork.module.css", "visuallyHidden"),
};

// The wire status is a free string (agent/internal/goal's enum does not
// survive the wire), so the display vocabulary lives at this seam: known
// statuses map to their display word, anything else renders as written.
const GOAL_STATUS_DISPLAY: Record<string, string> = {
  active: "Active",
  complete: "Complete",
  blocked: "Blocked",
};

export function CurrentWork({ task, goal, goalStatus, onOpenTasks, onEditGoal }: CurrentWorkProps) {
  const currentTask = task?.trim() ?? "";
  const currentGoal = goal?.trim() ?? "";
  const currentGoalStatus = goalStatus?.trim() ?? "";
  const statusKey = currentGoalStatus.toLowerCase();
  const statusDisplay = GOAL_STATUS_DISPLAY[statusKey] ?? currentGoalStatus;
  const statusSuffix = statusDisplay ? ` (${statusDisplay})` : "";

  const announcement = [
    ...(currentTask ? [`Task: ${currentTask}`] : []),
    // Announced here too, not only on the row: a visual-only status change is silent to screen readers.
    ...(currentGoal ? [`Goal: ${currentGoal}${statusSuffix}`] : []),
  ].join(". ");

  return (
    <>
      <span className={CLASS.visuallyHidden} role="status" aria-live="polite" aria-atomic="true">
        {announcement}
      </span>
      {(currentTask || currentGoal) && (
        <div className={CLASS.currentWork} data-testid="current-work">
          {currentTask && (
            <div className={CLASS.task} data-testid="current-work-task">
              <span className={CLASS.dot} aria-hidden="true" />
              <span className={CLASS.label}>Task</span>
              <button
                type="button"
                className={`${CLASS.value} ${CLASS.link}`}
                data-testid="current-work-task-value"
                title={task}
                aria-label={`Open tasks: ${currentTask}`}
                onClick={onOpenTasks}
              >
                {currentTask}
              </button>
            </div>
          )}
          {currentTask && currentGoal && (
            <span className={CLASS.divider} data-testid="current-work-divider" aria-hidden="true" />
          )}
          {currentGoal && (
            <div className={CLASS.goal} data-testid="current-work-goal">
              <GoalGlyph className={CLASS.flag} />
              <span className={CLASS.label}>Goal</span>
              <button
                type="button"
                className={`${CLASS.value} ${CLASS.link}`}
                data-testid="current-work-goal-value"
                title={goal}
                aria-label={`Edit goal: ${currentGoal}`}
                onClick={onEditGoal}
              >
                {currentGoal}
              </button>
              {currentGoalStatus && (
                <span
                  className={statusKey === "blocked" ? `${CLASS.status} ${CLASS.statusBlocked}` : CLASS.status}
                  data-testid="current-work-goal-status"
                >
                  {statusDisplay}
                </span>
              )}
            </div>
          )}
        </div>
      )}
    </>
  );
}
