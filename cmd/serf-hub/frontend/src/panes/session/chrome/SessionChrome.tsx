// SessionChrome: the session pane's chrome surface - ONE quiet status-bar
// row (status dot, model switch, work time, location, tokens/cost, the
// goal chip when a goal is set) with Tasks and the session "⋯" menu pinned
// to the trailing edge - mounted by Session.tsx at PaneScaffold's footer
// slot. The locked contract stays exactly `{ ref: string }` - every real
// value (the ThreadModel, capabilities, ...) is read from the threads store
// internally via useThreadsStore, same as every other pane-level component
// in this app (mirrors Session.tsx's own model lookup).
//
// This is the nearest common parent of GoalControl and SessionActionsMenu,
// so it owns the one piece of state neither can own alone: whether the
// set-goal Dialog is open. GoalControl renders that dialog (and, once a
// goal exists, the goal chip + its clear popover); SessionActionsMenu's
// "Set goal…" menu item is the only way to OPEN it now that the row itself
// carries no permanent goal button.
import { useState } from "react";
import { useThreadsStore } from "../../../stores/threads";
import { requireClass } from "../../../widgets/internal/requireClass";
import { NOW_TICK_MS, useNowTick } from "../liveness";
import { GoalControl } from "./GoalControl";
import { SessionActionsMenu } from "./SessionActionsMenu";
import { StatusRow } from "./StatusRow";
import styles from "./sessionchrome.module.css";
import { TasksPanel } from "./TasksPanel";

export interface SessionChromeProps {
  ref: string;
}

const CLASS = {
  chrome: requireClass(styles.chrome, "sessionchrome.module.css", "chrome"),
  right: requireClass(styles.right, "sessionchrome.module.css", "right"),
};

export function SessionChrome({ ref: sessionRef }: SessionChromeProps) {
  const model = useThreadsStore((s) => s.threads.get(sessionRef));
  // Session.tsx already runs one useNowTick(NOW_TICK_MS) for the header's
  // own Cadence/LivenessLine; this is a second, independent instance for
  // the footer's work-time clock (widgets/cadence's own doc comment: "no
  // timers, no Date.now()" is a rule for the pure prop-driven widgets
  // downstream, not a ban on more than one clock owner upstream - see
  // liveness.ts's own useNowTick doc comment: "transient by design").
  const now = useNowTick(NOW_TICK_MS);
  const [goalDialogOpen, setGoalDialogOpen] = useState(false);
  if (!model) return null;

  return (
    <div className={CLASS.chrome} data-testid="session-chrome">
      <StatusRow sessionRef={sessionRef} model={model} now={now} />
      <GoalControl
        sessionRef={sessionRef}
        model={model}
        dialogOpen={goalDialogOpen}
        onDialogOpenChange={setGoalDialogOpen}
      />
      <div className={CLASS.right}>
        <TasksPanel sessionRef={sessionRef} model={model} />
        <SessionActionsMenu sessionRef={sessionRef} model={model} onSetGoal={() => setGoalDialogOpen(true)} />
      </div>
    </div>
  );
}
