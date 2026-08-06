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
// goal happens through the command palette's /goal builtin - the unified
// session menu deliberately carries no slash-command actions (see
// SessionMenu.tsx's header comment), so the set-goal Dialog this component
// used to render is gone with it. Once a goal IS set, a quiet chip appears;
// clicking it opens a small popover with the goal's status and a clear
// action, using the setGoal(ref, "") wiring.
import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { sessionActionError } from "../../../protocol/errors";
import type { ThreadModel } from "../../../protocol/model";
import type { GoalState } from "../../../protocol/types.gen";
import { threadsStore } from "../../../stores/threads";
import { Button, Chip, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./goalcontrol.module.css";

export interface GoalControlProps {
  sessionRef: string;
  model: ThreadModel;
}

const CLASS = {
  anchor: requireClass(styles.anchor, "goalcontrol.module.css", "anchor"),
  chipButton: requireClass(styles.chipButton, "goalcontrol.module.css", "chipButton"),
  popover: requireClass(styles.popover, "goalcontrol.module.css", "popover"),
  status: requireClass(styles.status, "goalcontrol.module.css", "status"),
};

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
  const popoverRef = useRef<HTMLDivElement>(null);
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

  // Escape and an outside click dismiss the open popover - same containment
  // idiom widgets/menu and ModelSwitch's own popover use.
  useEffect(() => {
    if (!popoverOpen) return;
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") setPopoverOpen(false);
    }
    function onMouseDown(event: MouseEvent) {
      if (popoverRef.current && !popoverRef.current.contains(event.target as Node)) setPopoverOpen(false);
    }
    document.addEventListener("keydown", onKeyDown);
    document.addEventListener("mousedown", onMouseDown);
    return () => {
      document.removeEventListener("keydown", onKeyDown);
      document.removeEventListener("mousedown", onMouseDown);
    };
  }, [popoverOpen]);

  if (!goal) return null;

  return (
    <div className={CLASS.anchor} ref={popoverRef}>
      <button type="button" className={CLASS.chipButton} onClick={() => setPopoverOpen((v) => !v)}>
        <Chip>Goal: {goal.status}</Chip>
      </button>
      {popoverOpen && (
        <div className={CLASS.popover} data-testid="goal-popover">
          <p className={CLASS.status}>
            {goal.status} · {iterationsLabel(goal.iterations)}
          </p>
          <Button variant="quiet" size="sm" onClick={() => void handleClear()} disabled={!canSetGoal}>
            Clear goal
          </Button>
        </div>
      )}
    </div>
  );
}
