// SessionChrome: the session pane's chrome surface - ONE quiet status-bar
// row (cadence where needed, model · effort, context, live work, queue depth,
// and the goal chip when a goal is set) with Details, Tasks, Activity and the session "⋯"
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
import { useIsMobile } from "../../../shell/useIsMobile";
import { isPaneOpen, useWorkspaceStore, workspaceStore } from "../../../shell/workspace";
import { useActivitySummaryStore } from "../../../stores/activitySummary";
import { useThreadsStore } from "../../../stores/threads";
import { Button, Cadence, type MenuItem } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { cadenceStateForStatus, NOW_TICK_MS, useNowTick } from "../liveness";
import { ActivityPanel, type ActivityPanelHandle } from "./ActivityPanel";
import { DetailsPanel, type DetailsPanelHandle } from "./DetailsPanel";
import { GoalControl } from "./GoalControl";
import { SessionActionsMenu } from "./SessionActionsMenu";
import { StatusRow } from "./StatusRow";
import styles from "./sessionchrome.module.css";
import { TasksPanel, type TasksPanelHandle } from "./TasksPanel";
import "../../sessionPanels";

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

// Below this measured width, Details, Tasks, and Activity move INTO the "..."
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
// `narrow` is null until the observer's FIRST report: before that the width
// is unknown, not known-wide, and behavior that collapse must suppress (the
// hidden Activity badge refresh below) has to wait for the measurement
// rather than assume "not collapsed".
function useNarrowerThan(thresholdPx: number): [(el: HTMLDivElement | null) => void, boolean | null] {
  const [node, setNode] = useState<HTMLDivElement | null>(null);
  const [narrow, setNarrow] = useState<boolean | null>(null);
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
  const isMobile = useIsMobile();
  const detailsOpen = useWorkspaceStore((s) => isPaneOpen(s, "sessionDetails", { ref: sessionRef }));
  const tasksOpen = useWorkspaceStore((s) => isPaneOpen(s, "sessionTasks", { ref: sessionRef }));
  const activityOpen = useWorkspaceStore((s) => isPaneOpen(s, "sessionActivity", { ref: sessionRef }));
  const activitySummary = useActivitySummaryStore((s) => s.entries.get(sessionRef));
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
  const [chromeRef, narrow] = useNarrowerThan(NARROW_CHROME_WIDTH_PX);
  // Unmeasured (null) renders like not-collapsed - the triggers show until
  // the first observation lands, avoiding a mount flash - but is NOT treated
  // as measured-wide where that matters (see refreshWhenHidden below).
  const collapsed = narrow === true;
  const detailsRef = useRef<DetailsPanelHandle>(null);
  const tasksRef = useRef<TasksPanelHandle>(null);
  const activityRef = useRef<ActivityPanelHandle>(null);
  if (!model) return null;

  const openDetails = () => {
    if (isMobile) detailsRef.current?.open();
    else workspaceStore.getState().togglePane("sessionDetails", { ref: sessionRef });
  };
  const openTasks = () => {
    if (isMobile) tasksRef.current?.open();
    else workspaceStore.getState().togglePane("sessionTasks", { ref: sessionRef });
  };
  const openActivity = () => {
    if (isMobile) activityRef.current?.open();
    else workspaceStore.getState().togglePane("sessionActivity", { ref: sessionRef });
  };
  const activityLabel = activitySummary?.counts?.complete ? `Activity · ${activitySummary.counts.active}` : "Activity";
  const checkedLabel = (label: string, open: boolean) => (open ? `${label} ✓` : label);

  // Details/Tasks/Activity lead the "..." menu's own list when collapsed - see
  // SessionActionsMenu's extraItems doc comment for why that order, not
  // this one, is what actually matters (this array is just the three items).
  // Collapse is not desktop-only: a phone-width pane collapses too, and its
  // items open the Sheets (openX branches on isMobile), never workspace panes.
  const overflowItems: MenuItem[] = collapsed
    ? [
        { id: "details", label: checkedLabel("Details", detailsOpen), onSelect: openDetails },
        { id: "tasks", label: checkedLabel("Tasks", tasksOpen), onSelect: openTasks },
        { id: "activity", label: checkedLabel("Activity", activityOpen), onSelect: openActivity },
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
        <DetailsPanel ref={detailsRef} model={model} now={now} hideTrigger={!isMobile || collapsed} />
        <TasksPanel ref={tasksRef} sessionRef={sessionRef} model={model} hideTrigger={!isMobile || collapsed} />
        <ActivityPanel
          ref={activityRef}
          sessionRef={sessionRef}
          model={model}
          now={now}
          hideTrigger={!isMobile || collapsed}
          refreshWhenHidden={!isMobile && narrow === false}
        />
        {!isMobile && !collapsed && (
          <>
            <Button variant="quiet" size="sm" onClick={openDetails} aria-pressed={detailsOpen} data-details-trigger="">
              Details
            </Button>
            <Button variant="quiet" size="sm" onClick={openTasks} aria-pressed={tasksOpen} data-tasks-trigger="">
              {model.tasks ? `Tasks ${model.tasks.done}/${model.tasks.total}` : "Tasks"}
            </Button>
            <Button variant="quiet" size="sm" onClick={openActivity} aria-pressed={activityOpen}>
              {activityLabel}
            </Button>
          </>
        )}
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
