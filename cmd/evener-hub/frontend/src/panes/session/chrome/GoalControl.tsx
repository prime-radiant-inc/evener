// GoalControl displays and clears the session's /goal state. model.goal is the
// only UI source of truth: the threads store commits successful local writes,
// and evener/goal/updated folds remote and engine-driven transitions into the
// same model.
//
// Row presence: with no goal set, this renders nothing at all. SETTING a
// goal happens through the session's own composer (its inline /goal built-in
// - 2026-08-14, "the composer is where you act on this session"; the command
// palette used to run it and now only hands off to the composer instead,
// design-system.md §9) - the unified session menu deliberately carries no
// slash-command actions either (see SessionMenu.tsx's header comment), so
// the set-goal Dialog this component used to render is gone with it. Once a
// goal IS set, a quiet chip appears;
// clicking it opens a small popover with the goal's status and a clear
// action, using the setGoal(ref, "") wiring.
//
// The popover is the shared widgets/popover, same as this row's sibling
// ModelSwitch - not a hand-rolled `position: absolute` panel. This chip
// lives inside sessionchrome.module.css's `.body` (overflow: hidden, for
// inline-size compression), and a panel positioned relative to an anchor
// inside that row is clipped there entirely: correct geometry, opacity 1,
// never painted (live-verified). Popover portals the panel to document.body
// at a position: fixed coordinate computed off the trigger's own
// getBoundingClientRect(), so the clipping ancestor can't cut it off.
import { useState } from "react";
import { sessionActionError } from "../../../protocol/errors";
import type { ThreadModel } from "../../../protocol/model";
import { threadsStore } from "../../../stores/threads";
import { Button, Chip, Popover, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { GoalGlyph } from "../GoalGlyph";
import styles from "./goalcontrol.module.css";

export interface GoalControlProps {
  sessionRef: string;
  model: ThreadModel;
}

const CLASS = {
  anchor: requireClass(styles.anchor, "goalcontrol.module.css", "anchor"),
  chipButton: requireClass(styles.chipButton, "goalcontrol.module.css", "chipButton"),
  compactTrigger: requireClass(styles.compactTrigger, "goalcontrol.module.css", "compactTrigger"),
  compactTriggerActive: requireClass(styles.compactTriggerActive, "goalcontrol.module.css", "compactTriggerActive"),
  popover: requireClass(styles.popover, "goalcontrol.module.css", "popover"),
  status: requireClass(styles.status, "goalcontrol.module.css", "status"),
};

function iterationsLabel(iterations: number): string {
  return `${iterations} iteration${iterations === 1 ? "" : "s"}`;
}

export function GoalControl({ sessionRef, model }: GoalControlProps) {
  const toasts = useToasts();
  const goal = model.goal;
  const [popoverOpen, setPopoverOpen] = useState(false);
  const canSetGoal = model.capabilities.goal;

  async function handleClear() {
    try {
      await threadsStore.getState().setGoal(sessionRef, "");
      setPopoverOpen(false);
    } catch (err) {
      toasts.push("error", sessionActionError("Couldn't clear goal", err));
    }
  }

  if (!goal) return null;

  return (
    <div className={CLASS.anchor}>
      <Popover
        open={popoverOpen}
        onClose={() => setPopoverOpen(false)}
        data-testid="goal-popover"
        trigger={
          // Two triggers, one Popover: the full text chip stays the trigger
          // at full width, and a compact glyph-only button takes over below
          // 560px (goalcontrol.module.css's @container rule shows exactly
          // one of the two - see that file's own header comment). Both are
          // direct children of the same Fragment, which Popover's own
          // .trigger wrapper (an inline-flex span) renders in-flow with no
          // DOM node of its own for the Fragment - so the wrapper's
          // measured rect is always just the one visible child's rect,
          // whichever that is.
          <>
            <button
              type="button"
              className={CLASS.chipButton}
              data-testid="goal-chip-trigger"
              onClick={() => setPopoverOpen((v) => !v)}
            >
              <Chip>Goal: {goal.status}</Chip>
            </button>
            <button
              type="button"
              className={`${CLASS.compactTrigger} ${goal.status === "active" ? CLASS.compactTriggerActive : ""}`}
              data-testid="goal-compact-trigger"
              aria-label={`Goal: ${goal.status}`}
              onClick={() => setPopoverOpen((v) => !v)}
            >
              <GoalGlyph />
            </button>
          </>
        }
      >
        <div className={CLASS.popover}>
          <p className={CLASS.status}>
            {goal.status} · {iterationsLabel(goal.iterations)}
          </p>
          <Button variant="quiet" size="sm" onClick={() => void handleClear()} disabled={!canSetGoal}>
            Clear goal
          </Button>
        </div>
      </Popover>
    </div>
  );
}
