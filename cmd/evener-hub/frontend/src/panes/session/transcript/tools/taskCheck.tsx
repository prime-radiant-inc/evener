// The task surfaces' checkbox glyph: a square box whose inner mark names
// the task's state (plus = added, check = done, x = cancelled, arrow =
// started, empty = pending/not started yet). Drawn in the app's shared
// 16x16 line-art grammar (stroke currentColor, 1.75 width, round
// caps/joins, fill none, square box - the same contract widgets/toolicon
// makes), so the glyph reads as family next to the transcript's tool-kind
// icons and the row's own CSS class governs colour. Deliberately NOT part
// of the ToolIcon set: that set is per tool KIND, this is per task STATE.
// The glyph is a picture of state, not a control - aria-hidden, never
// focusable; the row's visually-hidden status word (taskCard.tsx) is what
// assistive tech reads. Both task surfaces share it: the transcript's
// inline card and the tasks pane.
import type { TaskStatus } from "@evener/appwire-client";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./taskcheck.module.css";

export const TOUCHES = ["added", "done", "cancelled", "started", "pending"] as const;
export type TaskTouch = (typeof TOUCHES)[number];

// Both task surfaces (the pane's rows, the card's window slots) key their
// glyph off the task's STATE through this one map - the 2026-09 rendering
// unification's "one box grammar across both surfaces" rule, spelled once at
// the glyph family's home. The cancellation rule is the one the legacy chain
// already settled (renderer-format.js:496-506 planStateClass +
// style.css:3324-3329): a cancelled task's glyph stays the dim neutral
// pending uses, never danger - in this design system's
// color-is-attention rule, danger-tinting a routine cancellation would make
// reprioritized work indistinguishable from a genuine failure. The ✕ mark
// alone carries the "won't happen" distinction.
export const STATUS_TOUCH: Record<TaskStatus, TaskTouch> = {
  open: "pending",
  in_progress: "started",
  done: "done",
  cancelled: "cancelled",
};

// The word assistive tech reads for each touch - the visible flag label is
// gone on both task surfaces, so the status rides along visually-hidden
// beside the glyph. "pending" belongs to a slot state (the card window's
// next task, the pane's open row), never to a mutation touch.
export const TOUCH_WORD: Record<TaskTouch, string> = {
  added: "added",
  done: "done",
  cancelled: "cancelled",
  started: "started",
  pending: "pending",
};

const CLASS = {
  check: requireClass(styles.check, "taskcheck.module.css", "check"),
  added: requireClass(styles.added, "taskcheck.module.css", "added"),
  done: requireClass(styles.done, "taskcheck.module.css", "done"),
  cancelled: requireClass(styles.cancelled, "taskcheck.module.css", "cancelled"),
  started: requireClass(styles.started, "taskcheck.module.css", "started"),
  pending: requireClass(styles.pending, "taskcheck.module.css", "pending"),
};

// The box outline every touch shares; only the inner mark varies.
const BOX = "M2.5 2.5 H13.5 V13.5 H2.5 Z";
const MARKS: Record<TaskTouch, string> = {
  added: "M8 5.5 V10.5 M5.5 8 H10.5",
  done: "M4.8 8.4 L7.2 10.8 L11.4 5.6",
  cancelled: "M5.5 5.5 L10.5 10.5 M10.5 5.5 L5.5 10.5",
  started: "M5 8 H11 M8.8 5.8 L11 8 L8.8 10.2",
  // Not started yet: the empty box IS the mark.
  pending: "",
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
