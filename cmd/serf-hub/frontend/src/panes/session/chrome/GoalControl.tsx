// GoalControl: displays and sets the session's /goal objective
// (model.goal). goal/set has NO live push on the wire (appwire/protocol.go's
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
import { type ChangeEvent, useState, useSyncExternalStore } from "react";
import type { ThreadModel } from "../../../protocol/model";
import type { GoalState } from "../../../protocol/types.gen";
import { threadsStore } from "../../../stores/threads";
import { Button, Dialog, Textarea, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./goalcontrol.module.css";

export interface GoalControlProps {
  sessionRef: string;
  model: ThreadModel;
}

const CLASS = {
  row: requireClass(styles.row, "goalcontrol.module.css", "row"),
  field: requireClass(styles.field, "goalcontrol.module.css", "field"),
  label: requireClass(styles.label, "goalcontrol.module.css", "label"),
  footer: requireClass(styles.footer, "goalcontrol.module.css", "footer"),
};

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
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
  const [dialogOpen, setDialogOpen] = useState(false);
  const [objective, setObjective] = useState("");
  const [busy, setBusy] = useState(false);
  const canSetGoal = model.capabilities.goal;

  function openDialog() {
    setObjective("");
    setDialogOpen(true);
  }

  async function handleSave() {
    const trimmed = objective.trim();
    if (!trimmed) return;
    setBusy(true);
    try {
      await threadsStore.getState().setGoal(sessionRef, trimmed);
      setGoalOverride(sessionRef, model.goal, { status: "active", iterations: 0 });
      setDialogOpen(false);
    } catch (err) {
      toasts.push("error", `Couldn't set goal: ${errorMessage(err)}`);
    } finally {
      setBusy(false);
    }
  }

  async function handleClear() {
    try {
      await threadsStore.getState().setGoal(sessionRef, "");
      setGoalOverride(sessionRef, model.goal, null);
    } catch (err) {
      toasts.push("error", `Couldn't clear goal: ${errorMessage(err)}`);
    }
  }

  return (
    <div className={CLASS.row} data-testid="goal-control">
      <span>{goal ? `Goal: ${goal.status} · ${iterationsLabel(goal.iterations)}` : "No goal set"}</span>
      <Button variant="quiet" size="sm" onClick={openDialog} disabled={!canSetGoal}>
        {goal ? "Change goal" : "Set goal"}
      </Button>
      {goal && (
        <Button variant="quiet" size="sm" onClick={() => void handleClear()} disabled={!canSetGoal}>
          Clear goal
        </Button>
      )}

      <Dialog
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
        title="Set goal"
        footer={
          <div className={CLASS.footer}>
            <Button variant="quiet" onClick={() => setDialogOpen(false)}>
              Cancel
            </Button>
            <Button variant="primary" onClick={() => void handleSave()} disabled={busy || !objective.trim()}>
              Save
            </Button>
          </div>
        }
      >
        <div className={CLASS.field}>
          <label className={CLASS.label} htmlFor="goal-control-objective">
            Objective
          </label>
          <Textarea
            id="goal-control-objective"
            autoGrow
            placeholder="What should the agent aim for?"
            value={objective}
            onChange={(e: ChangeEvent<HTMLTextAreaElement>) => setObjective(e.target.value)}
          />
        </div>
      </Dialog>
    </div>
  );
}
