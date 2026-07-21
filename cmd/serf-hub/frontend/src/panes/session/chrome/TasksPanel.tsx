// TasksPanel: a trigger + Sheet for the session's task list.
//
// Two independent signals, per the plan's push-driven-plus-fetch-on-open
// model:
//   - The TRIGGER's badge is model.tasks (the {total, done} aggregate,
//     pushed live by serf/task/updated - protocol/reducer.ts's own case) -
//     cheap and already-live without opening anything, so it stays exactly
//     as before.
//   - The SHEET's row list is fetched fresh via threadsStore.listTasks(ref)
//     every time the panel opens (unblocked by the T1 addendum, commit
//     da1b43f85 on w5-interaction, cherry-picked here), and re-fetched
//     automatically if model.tasks changes again while the panel stays
//     open (a live update while the user is looking at the list - the
//     aggregate object reference only changes when the reducer's own
//     serf/task/updated case actually re-assigns it, so this never
//     refires on an unrelated model update). taskData.ts's
//     parseTaskListData (pinned wire-true against agent/task/task_store.go's
//     real Task shape) owns interpreting the raw `unknown` response.
//
// Failure handling: a Codex-source thread rejects the wire call outright
// (appwire.Unavailable, "actionUnavailable" - verified against
// CodexSource.ListTasks) - an expected capability gap, not a bug, so it
// gets an honest inline "not available" state and no toast. A resolved-
// but-uninterpretable response (parseTaskListData returning null - e.g. an
// old daemon with no tasksFn registered, which responds with null data
// rather than rejecting - server/appwire_runtime.go:713-721) is folded into
// the SAME "not available" state, for the same reason: nothing actually
// went wrong at the transport level. Any other rejection is a genuine
// failure: toast (the wave's failure-feedback convention) AND an inline
// error state, since there is nothing left to show in its place.
import { useEffect, useState } from "react";
import { WireError } from "../../../protocol/errors";
import type { ThreadModel } from "../../../protocol/model";
import { threadsStore } from "../../../stores/threads";
import { Button, Chip, type ChipTone, EmptyState, Sheet, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { parseTaskListData, type TaskRow, type TaskStatus } from "./taskData";
import styles from "./taskspanel.module.css";

export interface TasksPanelProps {
  sessionRef: string;
  model: ThreadModel;
}

const CLASS = {
  state: requireClass(styles.state, "taskspanel.module.css", "state"),
  list: requireClass(styles.list, "taskspanel.module.css", "list"),
  row: requireClass(styles.row, "taskspanel.module.css", "row"),
  description: requireClass(styles.description, "taskspanel.module.css", "description"),
};

// Mirrors the legacy sidebar/inline task-row grammar (cmd/serf-hub/assets/
// renderer-format.js: planGlyphForStatus/planStateClass) translated onto
// this app's own widget vocabulary (Chip tones) rather than the legacy's
// window.SerfIcons SVG fragments, which this client has no equivalent of -
// same semantic mapping: done recedes (neutral), in_progress reads as the
// live "agent working" hue (alive), cancelled reads the same as a failure
// (danger) per that file's own comment ("a plan item that will not happen
// reads the same as a failure"), open/pending stays neutral with a hollow
// glyph to stay visually distinct from done's checkmark.
const STATUS_GLYPH: Record<TaskStatus, string> = {
  open: "○",
  in_progress: "●",
  done: "✓",
  cancelled: "✕",
};

const STATUS_TONE: Record<TaskStatus, ChipTone> = {
  open: "neutral",
  in_progress: "alive",
  done: "neutral",
  cancelled: "danger",
};

function triggerLabel(tasks: ThreadModel["tasks"]): string {
  return tasks ? `Tasks ${tasks.done}/${tasks.total}` : "Tasks";
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

function isActionUnavailable(err: unknown): boolean {
  return err instanceof WireError && err.serfErrorInfo === "actionUnavailable";
}

function TaskRowView({ task }: { task: TaskRow }) {
  return (
    <li className={CLASS.row} data-testid="task-row">
      <Chip tone={STATUS_TONE[task.status]}>{STATUS_GLYPH[task.status]}</Chip>
      <span className={CLASS.description}>{task.description}</span>
    </li>
  );
}

export function TasksPanel({ sessionRef, model }: TasksPanelProps) {
  const toasts = useToasts();
  const [open, setOpen] = useState(false);
  const [rows, setRows] = useState<TaskRow[] | null>(null);
  const [unsupported, setUnsupported] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Re-fetches on every open, and again whenever model.tasks changes while
  // still open (a live serf/task/updated push while the user is looking) -
  // see this file's own header comment. `toasts` is deliberately not a
  // dependency: useToasts() returns a fresh wrapper object every render
  // (see widgets/toast/index.tsx), so depending on it would refire this
  // effect on every unrelated re-render; toasts.push itself is a stable,
  // module-level function underneath.
  // biome-ignore lint/correctness/useExhaustiveDependencies: toasts is a fresh wrapper object every render (see above) - toasts.push itself is stable
  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setError(null);
    setUnsupported(false);
    threadsStore
      .getState()
      .listTasks(sessionRef)
      .then((data) => {
        if (cancelled) return;
        const parsed = parseTaskListData(data);
        if (parsed === null) {
          setUnsupported(true);
          setRows(null);
        } else {
          setRows(parsed);
        }
      })
      .catch((err) => {
        if (cancelled) return;
        if (isActionUnavailable(err)) {
          setUnsupported(true);
          setRows(null);
          return;
        }
        setRows(null);
        const message = errorMessage(err);
        setError(message);
        toasts.push("error", `Couldn't load tasks: ${message}`);
      });
    return () => {
      cancelled = true;
    };
  }, [open, model.tasks, sessionRef]);

  function openPanel() {
    setOpen(true);
  }

  function closePanel() {
    setOpen(false);
  }

  function renderBody() {
    if (unsupported) {
      return (
        <EmptyState title="Task list isn't available" hint="This session's source doesn't support the task list." />
      );
    }
    if (error) {
      return <EmptyState title="Couldn't load tasks" hint={error} />;
    }
    if (rows === null) {
      return <p className={CLASS.state}>Loading tasks…</p>;
    }
    if (rows.length === 0) {
      return <EmptyState title="No tasks yet" hint="The agent's task list is empty for this session." />;
    }
    return (
      <ul className={CLASS.list}>
        {rows.map((row) => (
          <TaskRowView key={row.id} task={row} />
        ))}
      </ul>
    );
  }

  return (
    <>
      <Button variant="quiet" size="sm" onClick={openPanel}>
        {triggerLabel(model.tasks)}
      </Button>
      <Sheet open={open} onClose={closePanel} title="Tasks">
        {renderBody()}
      </Sheet>
    </>
  );
}
