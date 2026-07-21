// SessionChrome: the session pane's chrome surface (status row, model
// switch, session actions, goal, tasks panel), mounted by Session.tsx at
// PaneScaffold's footer slot. T1 shipped this as an empty placeholder and
// carved the slot; this is T5's fill-in. The locked contract stays exactly
// what T1 fixed (`{ ref: string }`, nothing else) - every real value (the
// ThreadModel, capabilities, ...) is read from the threads store
// internally via useThreadsStore, same as every other pane-level component
// in this app (mirrors Session.tsx's own model lookup) - never passed in as
// a prop, since Session.tsx (frozen for the wave) never changes what it
// hands this component.
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
  left: requireClass(styles.left, "sessionchrome.module.css", "left"),
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
  if (!model) return null;

  return (
    <div className={CLASS.chrome} data-testid="session-chrome">
      <div className={CLASS.left}>
        <StatusRow sessionRef={sessionRef} model={model} now={now} />
        <GoalControl sessionRef={sessionRef} model={model} />
      </div>
      <div className={CLASS.right}>
        <TasksPanel sessionRef={sessionRef} model={model} />
        <SessionActionsMenu sessionRef={sessionRef} model={model} />
      </div>
    </div>
  );
}
