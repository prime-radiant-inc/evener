// SessionChrome: the session pane's chrome surface - ONE quiet status-bar
// row (status dot, model switch, work time, location, tokens/cost, the
// goal chip when a goal is set) with Details, Tasks, Jobs and the session "⋯"
// menu pinned to the trailing edge - mounted by Session.tsx at PaneScaffold's
// footer
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
import { useEffect, useRef, useState } from "react";
import { useThreadsStore } from "../../../stores/threads";
import { Cadence, type MenuItem } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { cadenceStateForStatus, NOW_TICK_MS, useNowTick } from "../liveness";
import { DetailsPanel, type DetailsPanelHandle } from "./DetailsPanel";
import { GoalControl } from "./GoalControl";
import { JobsPanel, type JobsPanelHandle } from "./JobsPanel";
import { SessionActionsMenu } from "./SessionActionsMenu";
import { StatusRow } from "./StatusRow";
import styles from "./sessionchrome.module.css";
import { TasksPanel, type TasksPanelHandle } from "./TasksPanel";

export interface SessionChromeProps {
  ref: string;
}

const CLASS = {
  chrome: requireClass(styles.chrome, "sessionchrome.module.css", "chrome"),
  body: requireClass(styles.body, "sessionchrome.module.css", "body"),
  cadenceSlot: requireClass(styles.cadenceSlot, "sessionchrome.module.css", "cadenceSlot"),
  right: requireClass(styles.right, "sessionchrome.module.css", "right"),
};

// Module-level empty array so a ref with no tracked frames yet doesn't get a
// fresh [] identity every render (Session.tsx's own frameTimes lookup does
// the same for the header cadence).
const EMPTY_FRAME_TIMES: number[] = [];

// Below this measured width, Details, Tasks, and Jobs move INTO the "..."
// menu so .body has room to compress its status facts while the footer stays
// one line tall. Picked from live measurement in a real browser (jsdom reports
// zero for every layout dimension, so this number could never come from a
// unit test - see this task's report). The status row's container-query
// variants then compress progressively inside the remaining inline size.
const NARROW_CHROME_WIDTH_PX = 640;

// Measures `ref`'s own border-box width via ResizeObserver - a PANE's box,
// not the viewport, which is the trigger this kata's own report rules out a
// media query for. Mirrors the guard widgets/textarea and widgets/popover
// already use: jsdom ships no ResizeObserver, so this silently never fires
// under vitest (narrow stays false, matching every existing test's
// expectations) and a test that wants to exercise it stubs the observer the
// same way popover.test.tsx's own "re-measures placement" test does.
//
// The ref is a CALLBACK ref, not a plain useRef object, on purpose: this
// component's own caller (SessionChrome) can render null on its first pass
// while the thread model is still loading (see `if (!model) return null`
// below) and only mount this div once the model arrives. A plain useRef
// paired with a `useEffect(..., [thresholdPx])` runs its setup exactly once,
// against whatever `elementRef.current` holds at that first commit - null,
// in the loading case - and never runs again since threshold never changes,
// so the observer silently never attaches for the entire session. Storing
// the node in state instead makes React invoke the callback (and this hook
// re-render) at the moment the div actually mounts, however many renders
// that takes.
function useNarrowerThan(thresholdPx: number): [(el: HTMLDivElement | null) => void, boolean] {
  const [node, setNode] = useState<HTMLDivElement | null>(null);
  const [narrow, setNarrow] = useState(false);
  useEffect(() => {
    if (!node) return;
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver((entries) => {
      const entry = entries[0];
      if (entry) setNarrow(entry.contentRect.width < thresholdPx);
    });
    observer.observe(node);
    return () => observer.disconnect();
  }, [node, thresholdPx]);
  return [setNode, narrow];
}

export function SessionChrome({ ref: sessionRef }: SessionChromeProps) {
  const model = useThreadsStore((s) => s.threads.get(sessionRef));
  // The cadence that used to live in the pane header's cadence slot: the
  // header is hidden on mobile (2026-07-30-mobile-session-layout-design.md,
  // decision 3), so the liveness marker relocates here. Rendered always,
  // revealed only below the breakpoint by .cadenceSlot's own CSS - the same
  // "panes never ask am I mobile?" rule the header channel follows. Read
  // from the same store fields Session.tsx reads for the header copy.
  const frameTimes = useThreadsStore((s) => s.frameTimes.get(sessionRef) ?? EMPTY_FRAME_TIMES);
  // Session.tsx already runs one useNowTick(NOW_TICK_MS) for the header's
  // own Cadence/LivenessLine; this is a second, independent instance for
  // the footer's work-time clock (widgets/cadence's own doc comment: "no
  // timers, no Date.now()" is a rule for the pure prop-driven widgets
  // downstream, not a ban on more than one clock owner upstream - see
  // liveness.ts's own useNowTick doc comment: "transient by design").
  const now = useNowTick(NOW_TICK_MS);
  const [goalDialogOpen, setGoalDialogOpen] = useState(false);
  const [chromeRef, collapsed] = useNarrowerThan(NARROW_CHROME_WIDTH_PX);
  const detailsRef = useRef<DetailsPanelHandle>(null);
  const tasksRef = useRef<TasksPanelHandle>(null);
  const jobsRef = useRef<JobsPanelHandle>(null);
  if (!model) return null;

  // Details/Tasks/Jobs lead the "..." menu's own list when collapsed - see
  // SessionActionsMenu's extraItems doc comment for why that order, not
  // this one, is what actually matters (this array is just the three items).
  const overflowItems: MenuItem[] = collapsed
    ? [
        { id: "details", label: "Details", onSelect: () => detailsRef.current?.open() },
        { id: "tasks", label: "Tasks", onSelect: () => tasksRef.current?.open() },
        { id: "jobs", label: "Jobs", onSelect: () => jobsRef.current?.open() },
      ]
    : [];

  return (
    <div ref={chromeRef} className={CLASS.chrome} data-testid="session-chrome">
      {/* .body owns compression (sessionchrome.module.css says why): its
          inline-size container progressively simplifies status content, so
          .right - and with it the "..." menu - always shares this one line. */}
      <div className={CLASS.body} data-testid="session-chrome-body">
        <span className={CLASS.cadenceSlot} data-testid="session-chrome-cadence">
          <Cadence state={cadenceStateForStatus(model.status.type)} frameTimes={frameTimes} now={now} />
        </span>
        <StatusRow sessionRef={sessionRef} model={model} now={now} />
        <GoalControl
          sessionRef={sessionRef}
          model={model}
          dialogOpen={goalDialogOpen}
          onDialogOpenChange={setGoalDialogOpen}
        />
      </div>
      <div className={CLASS.right}>
        <DetailsPanel ref={detailsRef} model={model} now={now} hideTrigger={collapsed} />
        <TasksPanel ref={tasksRef} sessionRef={sessionRef} model={model} hideTrigger={collapsed} />
        <JobsPanel ref={jobsRef} sessionRef={sessionRef} model={model} now={now} hideTrigger={collapsed} />
        <SessionActionsMenu
          sessionRef={sessionRef}
          model={model}
          onSetGoal={() => setGoalDialogOpen(true)}
          extraItems={overflowItems}
        />
      </div>
    </div>
  );
}
