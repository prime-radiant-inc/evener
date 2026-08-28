import { requireClass } from "../../../widgets/internal/requireClass";
import { GoalGlyph } from "../GoalGlyph";
import styles from "./currentwork.module.css";

export interface CurrentWorkProps {
  task?: string;
  goal?: string;
}

const CLASS = {
  currentWork: requireClass(styles.currentWork, "currentwork.module.css", "currentWork"),
  task: requireClass(styles.task, "currentwork.module.css", "task"),
  goal: requireClass(styles.goal, "currentwork.module.css", "goal"),
  dot: requireClass(styles.dot, "currentwork.module.css", "dot"),
  flag: requireClass(styles.flag, "currentwork.module.css", "flag"),
  label: requireClass(styles.label, "currentwork.module.css", "label"),
  value: requireClass(styles.value, "currentwork.module.css", "value"),
  divider: requireClass(styles.divider, "currentwork.module.css", "divider"),
};

export function CurrentWork({ task, goal }: CurrentWorkProps) {
  const currentTask = task?.trim() ?? "";
  const currentGoal = goal?.trim() ?? "";
  if (!currentTask && !currentGoal) return null;

  const ariaLabel = [
    ...(currentTask ? [`Working on: ${currentTask}`] : []),
    ...(currentGoal ? [`Goal: ${currentGoal}`] : []),
  ].join(". ");

  return (
    <div
      className={CLASS.currentWork}
      data-testid="current-work"
      role="status"
      aria-live="polite"
      aria-atomic="true"
      aria-label={ariaLabel}
    >
      {currentTask && (
        <div className={CLASS.task} data-testid="current-work-task">
          <span className={CLASS.dot} aria-hidden="true" />
          <span className={CLASS.label}>Working on</span>
          <span className={CLASS.value} data-testid="current-work-task-value" title={task}>
            {currentTask}
          </span>
        </div>
      )}
      {currentTask && currentGoal && (
        <span className={CLASS.divider} data-testid="current-work-divider" aria-hidden="true" />
      )}
      {currentGoal && (
        <div className={CLASS.goal} data-testid="current-work-goal">
          <GoalGlyph className={CLASS.flag} />
          <span className={CLASS.label}>Goal</span>
          <span className={CLASS.value} data-testid="current-work-goal-value" title={goal}>
            {currentGoal}
          </span>
        </div>
      )}
    </div>
  );
}
