// GoalControl: displays the session's /goal objective (model.goal) and
// clears it. goal/set has NO live push on the wire (appwire/protocol.go's
// Notifications catalog has no goal-changed entry, and GoalSetResponse
// carries only {started} - protocol/model.ts's own ThreadModel.goal doc
// comment) - a planning decision made this wave (T5 owns "snapshot +
// optimistic local update", per the wave plan's own Session-chrome bullet).
//
// The optimistic value can't live in component state: dockview unmounts an
// inactive pane's whole tree on a tab switch (binding constraint, every
// wave-5 task), and there is no threads-store action to patch
// ThreadModel.goal locally either (stores/threads.ts is T1's frozen
// chokepoint, outside this stream's manifest). So it lives in a tiny
// ref-keyed module cache instead - the same "module-private singleton
// alongside the store" shape stores/threads.ts itself uses for refCounts/
// inflightHydrates. Each entry also remembers the model.goal it was set
// against (`baseline`); resolveDisplayedGoal drops a stale entry the moment
// the store's own model.goal has independently moved on (a genuine fresh
// hydrate, e.g. after a reconnect, brought real server truth) - otherwise a
// stale optimistic value would mask every future update forever, not just
// bridge the gap until the next one.
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
import { useState, useSyncExternalStore } from "react";
import { sessionActionError } from "../../../protocol/errors";
import type { ThreadModel } from "../../../protocol/model";
import type { GoalState } from "../../../protocol/types.gen";
import { threadsStore } from "../../../stores/threads";
import { Button, Chip, Popover, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
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

// The compact trigger's glyph: evener has no dedicated goal icon (widgets/
// toolicon is a closed enum of tool-row kinds, not extended here per the
// approved design). A small flag on a pole reads as "goal"/milestone at
// 16px and follows widgets/toolicon's own grammar exactly (see that
// widget's header comment) - stroke currentColor, 1.75 width, round caps/
// joins, fill none, square 16x16 block box - so it reads as one icon
// family with the rest of the app even though it isn't drawn through that
// widget.
function GoalGlyph() {
  return (
    <svg viewBox="0 0 16 16" width={14} height={14} aria-hidden="true" focusable="false" style={{ display: "block" }}>
      <path
        d="M4 2 V14 M4 3 L11 5 L4 7"
        stroke="currentColor"
        strokeWidth="1.75"
        strokeLinecap="round"
        strokeLinejoin="round"
        fill="none"
      />
    </svg>
  );
}

function goalEquals(a: GoalState | null, b: GoalState | null): boolean {
  if (a === null || b === null) return a === b;
  return a.status === b.status && a.iterations === b.iterations;
}

interface GoalOverrideEntry {
  baseline: GoalState | null;
  override: GoalState | null;
}

const overrides = new Map<string, GoalOverrideEntry>();
const listeners = new Set<() => void>();

function notifyGoalOverrideListeners(): void {
  for (const listener of Array.from(listeners)) listener();
}

function subscribeGoalOverrides(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

// setGoalOverride records `override` as the locally-known truth for `ref`,
// remembering `baseline` (the model.goal it was computed against) so a
// later fresh hydrate can be detected and invalidate it - see this file's
// own header comment.
function setGoalOverride(ref: string, baseline: GoalState | null, override: GoalState | null): void {
  overrides.set(ref, { baseline, override });
  notifyGoalOverrideListeners();
}

// applyGoalSetOptimistically is the same optimistic bridge for the OTHER
// goal writer: the /goal built-in (shell/palette/commands.ts, run from the
// composer's command line). goal/set has no live push, so without this the
// chip would not appear until some unrelated rehydrate delivered fresh
// server truth - live-verified: the transcript showed the goal running
// while the status row still showed nothing.
export function applyGoalSetOptimistically(ref: string, baseline: GoalState | null, objective: string): void {
  setGoalOverride(ref, baseline, objective === "" ? null : { status: "active", iterations: 0 });
}

// resolveDisplayedGoal returns the override for `ref` only while it's still
// valid (the store's own model.goal hasn't moved since it was set); a
// stale entry is dropped as a side effect so the next call sees the fresh
// value directly, with no separate invalidation pass needed.
function resolveDisplayedGoal(ref: string, actual: GoalState | null): GoalState | null {
  const entry = overrides.get(ref);
  if (!entry) return actual;
  if (!goalEquals(entry.baseline, actual)) {
    overrides.delete(ref);
    return actual;
  }
  return entry.override;
}

function useDisplayedGoal(ref: string, actual: GoalState | null): GoalState | null {
  return useSyncExternalStore(subscribeGoalOverrides, () => resolveDisplayedGoal(ref, actual));
}

// resetGoalOverridesForTests clears the module-private override cache -
// this module is a singleton shared by the whole app, so GoalControl.test.tsx
// must reset it between tests to keep them isolated (mirrors stores/
// threads.ts's own resetThreadsStoreForTests precedent). No production code
// should ever call this.
export function resetGoalOverridesForTests(): void {
  overrides.clear();
  listeners.clear();
}

function iterationsLabel(iterations: number): string {
  return `${iterations} iteration${iterations === 1 ? "" : "s"}`;
}

export function GoalControl({ sessionRef, model }: GoalControlProps) {
  const toasts = useToasts();
  const goal = useDisplayedGoal(sessionRef, model.goal);
  const [popoverOpen, setPopoverOpen] = useState(false);
  const canSetGoal = model.capabilities.goal;

  async function handleClear() {
    try {
      await threadsStore.getState().setGoal(sessionRef, "");
      setGoalOverride(sessionRef, model.goal, null);
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
