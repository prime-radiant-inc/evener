import { requireClass } from "../../../widgets/internal/requireClass";
import { GoalGlyph } from "../GoalGlyph";
import styles from "./currentwork.module.css";

export interface CurrentWorkProps {
  task?: string;
  goal?: string;
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
  link: requireClass(styles.link, "currentwork.module.css", "link"),
  divider: requireClass(styles.divider, "currentwork.module.css", "divider"),
  visuallyHidden: requireClass(styles.visuallyHidden, "currentwork.module.css", "visuallyHidden"),
};

export function CurrentWork({ task, goal, onOpenTasks, onEditGoal }: CurrentWorkProps) {
  const currentTask = task?.trim() ?? "";
  const currentGoal = goal?.trim() ?? "";
  if (!currentTask && !currentGoal) return null;

  const ariaLabel = [
    ...(currentTask ? [`Task: ${currentTask}`] : []),
    ...(currentGoal ? [`Goal: ${currentGoal}`] : []),
  ].join(". ");

  return (
    <>
      <span
        className={CLASS.visuallyHidden}
        role="status"
        aria-live="polite"
        aria-atomic="true"
        aria-label={ariaLabel}
      />
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
          </div>
        )}
      </div>
    </>
  );
}
