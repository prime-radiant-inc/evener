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
//
// Row presence: with no goal set, this renders nothing at all - "Set goal…"
// lives in the session ⋯ menu (SessionActionsMenu) instead of a permanent
// pair of buttons in the row. The set-goal Dialog's open state is therefore
// a controlled prop (dialogOpen/onDialogOpenChange) owned by SessionChrome,
// the nearest common parent of this component and SessionActionsMenu, so
// either can drive it; everything else (the objective draft, save/clear
// wiring, the optimistic-override cache above) stays exactly as before. Once
// a goal IS set, a quiet chip appears; clicking it opens a small popover
// with the goal's status and a clear action, using this same setGoal(ref,
// "") wiring.
import { type ChangeEvent, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { sessionActionError } from "../../../protocol/errors";
import type { ThreadModel } from "../../../protocol/model";
import type { GoalState } from "../../../protocol/types.gen";
import { threadsStore } from "../../../stores/threads";
import { Button, Chip, Dialog, Textarea, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./goalcontrol.module.css";

export interface GoalControlProps {
  sessionRef: string;
  model: ThreadModel;
  // Optional so a caller that only needs the goal chip + clear popover (no
  // "Set goal…" entry point of its own to wire up) can render this without
  // a dialog-open seam to plumb through. SessionChrome, the real caller,
  // always passes both - it's the nearest common parent that can trigger
  // this dialog from SessionActionsMenu's "Set goal…" item.
  dialogOpen?: boolean;
  onDialogOpenChange?: (open: boolean) => void;
}

const CLASS = {
  anchor: requireClass(styles.anchor, "goalcontrol.module.css", "anchor"),
  chipButton: requireClass(styles.chipButton, "goalcontrol.module.css", "chipButton"),
  popover: requireClass(styles.popover, "goalcontrol.module.css", "popover"),
  status: requireClass(styles.status, "goalcontrol.module.css", "status"),
  field: requireClass(styles.field, "goalcontrol.module.css", "field"),
  label: requireClass(styles.label, "goalcontrol.module.css", "label"),
  footer: requireClass(styles.footer, "goalcontrol.module.css", "footer"),
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

export function GoalControl({
  sessionRef,
  model,
  dialogOpen = false,
  onDialogOpenChange = () => undefined,
}: GoalControlProps) {
  const toasts = useToasts();
  const goal = useDisplayedGoal(sessionRef, model.goal);
  const [objective, setObjective] = useState("");
  const [busy, setBusy] = useState(false);
  const [popoverOpen, setPopoverOpen] = useState(false);
  const popoverRef = useRef<HTMLDivElement>(null);
  const canSetGoal = model.capabilities.goal;

  // The dialog always opens onto a blank objective (mirrors the previous
  // local openDialog()'s own reset) - there's nothing to prefill anyway,
  // since GoalState carries only status/iterations, never the objective
  // text, on the wire.
  useEffect(() => {
    if (dialogOpen) setObjective("");
  }, [dialogOpen]);

  async function handleSave() {
    const trimmed = objective.trim();
    if (!trimmed) return;
    setBusy(true);
    try {
      await threadsStore.getState().setGoal(sessionRef, trimmed);
      setGoalOverride(sessionRef, model.goal, { status: "active", iterations: 0 });
      onDialogOpenChange(false);
    } catch (err) {
      toasts.push("error", sessionActionError("Couldn't set goal", err));
    } finally {
      setBusy(false);
    }
  }

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

  return (
    <>
      {goal && (
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
      )}

      <Dialog
        open={dialogOpen}
        onClose={() => onDialogOpenChange(false)}
        title="Set goal"
        footer={
          <div className={CLASS.footer}>
            <Button variant="quiet" onClick={() => onDialogOpenChange(false)}>
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
    </>
  );
}
