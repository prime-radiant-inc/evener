// The task card's per-touch checkbox glyph: a square box whose inner mark
// names what happened to the task (plus = added, check = done, x =
// cancelled, arrow = started). Drawn in the app's shared 16x16 line-art
// grammar (stroke currentColor, 1.75 width, round caps/joins, fill none,
// square box - the same contract widgets/toolicon makes), so the glyph
// reads as family next to the transcript's tool-kind icons and the row's
// own CSS class governs colour. Deliberately NOT part of the ToolIcon set:
// that set is per tool KIND, this is per task STATUS. The glyph is a
// picture of state, not a control - aria-hidden, never focusable; the
// row's visually-hidden status word (taskCard.tsx) is what assistive tech
// reads.
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./taskcheck.module.css";

export const TOUCHES = ["added", "done", "cancelled", "started"] as const;
export type TaskTouch = (typeof TOUCHES)[number];

const CLASS = {
  check: requireClass(styles.check, "taskcheck.module.css", "check"),
  added: requireClass(styles.added, "taskcheck.module.css", "added"),
  done: requireClass(styles.done, "taskcheck.module.css", "done"),
  cancelled: requireClass(styles.cancelled, "taskcheck.module.css", "cancelled"),
  started: requireClass(styles.started, "taskcheck.module.css", "started"),
};

// The box outline every touch shares; only the inner mark varies.
const BOX = "M2.5 2.5 H13.5 V13.5 H2.5 Z";
const MARKS: Record<TaskTouch, string> = {
  added: "M8 5.5 V10.5 M5.5 8 H10.5",
  done: "M4.8 8.4 L7.2 10.8 L11.4 5.6",
  cancelled: "M5.5 5.5 L10.5 10.5 M10.5 5.5 L5.5 10.5",
  started: "M5 8 H11 M8.8 5.8 L11 8 L8.8 10.2",
};

const DEFAULT_SIZE = 16;

export function TaskCheck({ touch, size = DEFAULT_SIZE }: { touch: TaskTouch; size?: number }) {
  return (
    <svg
      viewBox="0 0 16 16"
      width={size}
      height={size}
      aria-hidden="true"
      focusable="false"
      className={`${CLASS.check} ${CLASS[touch]}`}
      data-testid="task-check"
      data-touch={touch}
      // Inline rather than a class (same rationale as widgets/toolicon):
      // `display` here is correctness - an inline SVG would sit in a line
      // box taller than itself, undoing the square box.
      style={{ display: "block" }}
    >
      <path
        d={`${BOX} ${MARKS[touch]}`}
        stroke="currentColor"
        strokeWidth="1.75"
        strokeLinecap="round"
        strokeLinejoin="round"
        fill="none"
      />
    </svg>
  );
}
