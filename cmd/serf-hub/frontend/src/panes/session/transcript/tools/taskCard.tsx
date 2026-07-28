// The task_list descriptor: renders a task-update card for a successful
// append/update mutation (parity-m4 §9:239 renderer.js:4769-4786,4966-5061;
// contracts-transcript §11). `action:"view"` and a malformed non-mutation
// render nothing (suppressed - the legacy "no card, no divider, no tool-call
// row"). A FAILED mutation renders no card either: its error is surfaced by
// ToolCallItem's generic failed-row treatment instead (the legacy card was
// appended only `if (!data.error)`).
//
// Wire truth: agent/session_tools_task.go's task_list executor returns
// tool.StateResult{State: store.View()} on every view/append/update call -
// the authoritative snapshot carrying every task's status, description, and
// minted timestamps. That State rides all the way to the client as
// item.raw (registry.go marshals it straight into ToolState; appprojector
// and apptranscript carry it onto ThreadItem.raw unchanged; reducer.ts's
// wireItemToModel keeps it as item.raw verbatim), and taskData.ts's
// parseTaskState narrows it - reusing chrome/taskData.ts's
// parseTaskListData, since it's the same agent/task/task_store.go Task[]
// shape the tasks side panel already parses from a different wire path. An
// update row's label prefers the matched task's description there
// (taskData.ts's taskLabel); a batch that completes a task without itself
// also starting another earns one extra row for whatever task the daemon
// auto-advanced to in_progress as a side effect (taskData.ts's
// autoStartedTask) - the "and now working on X" row docs/superpowers/plans/
// 2026-07-15-inline-task-update-cards.md required keeping ("authoritative
// auto-activation").
//
// raw is absent for an old daemon that predates StateResult.State and for a
// transcript replayed from before it existed - a real, ongoing case, not
// just a historical one - and the card then degrades to exactly its
// argument-only rendering: an update row falls back to "#<id>" for its
// label, and no auto-started row, because nothing beyond the caller's own
// args can be proven. Still absent regardless of raw: a full-list "show
// all" fold, surrounding-context rows, and aggregate done/up-next counts -
// the 2026-07-15 plan trimmed those from the legacy card deliberately (a
// changes-only card, not a full-plan disclosure; the sidebar remains the
// full-plan view), not because the data is unavailable.
import type { ItemModel } from "../../../../protocol/model";
import { Meter } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import { parseArgs, str } from "./helpers";
import styles from "./taskcard.module.css";
import { autoStartedTask, parseTaskState, taskLabel } from "./taskData";

const CLASS = {
  card: requireClass(styles.card, "taskcard.module.css", "card"),
  head: requireClass(styles.head, "taskcard.module.css", "head"),
  rows: requireClass(styles.rows, "taskcard.module.css", "rows"),
  row: requireClass(styles.row, "taskcard.module.css", "row"),
  flag: requireClass(styles.flag, "taskcard.module.css", "flag"),
  desc: requireClass(styles.desc, "taskcard.module.css", "desc"),
  note: requireClass(styles.note, "taskcard.module.css", "note"),
  progress: requireClass(styles.progress, "taskcard.module.css", "progress"),
};

interface TouchedRow {
  key: string;
  touch: string; // added | done | cancelled | started
  label: string; // description (append; update when state is known) or "#<id>" (update, state absent)
  note?: string;
}

interface Progress {
  done: number;
  total: number;
}

function asObjectArray(value: unknown): Record<string, unknown>[] {
  if (!Array.isArray(value)) return [];
  return value.filter((v): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v));
}

// The daemon applies duplicate IDs sequentially but returns the authoritative
// final state. Render one status touch per ID from the matching final argument;
// ordering by final occurrence keeps distinct IDs in the order the batch ends.
function finalUpdates(updates: Record<string, unknown>[]): Record<string, unknown>[] {
  const latestByID = new Map<number, { index: number; update: Record<string, unknown> }>();
  const unmarked: { index: number; update: Record<string, unknown> }[] = [];
  for (const [index, update] of updates.entries()) {
    const id = typeof update.id === "number" ? update.id : undefined;
    if (id === undefined) {
      unmarked.push({ index, update });
      continue;
    }
    latestByID.set(id, { index, update });
  }
  return [...latestByID.values(), ...unmarked].sort((a, b) => a.index - b.index).map(({ update }) => update);
}

// A valid mutation is exactly what the legacy validAppend/validUpdate gates
// accepted (renderer.js:4786-4788): append with a non-empty tasks array, or
// update with a non-empty updates array. Anything else (view, or a malformed
// call) is not a card.
function mutationRows(item: ItemModel): TouchedRow[] | undefined {
  const args = parseArgs(item.argumentsJSON);
  const action = str(args, "action") ?? "";
  if (action === "append") {
    const tasks = asObjectArray(args.tasks);
    if (tasks.length === 0) return undefined;
    return tasks.map((task, i) => ({
      key: `append_${i}`,
      touch: "added",
      label: str(task, "description") ?? str(task, "prompt") ?? "(untitled task)",
    }));
  }
  if (action === "update") {
    const updates = finalUpdates(asObjectArray(args.updates));
    if (updates.length === 0) return undefined;
    // Only a real status change earns a row - matching the legacy card, which
    // flags exactly done/cancelled/in_progress updates (renderer.js:5010) and
    // renders a note-only or reopened update as no per-row change at all.
    const state = parseTaskState(item.raw);
    const rows: TouchedRow[] = [];
    const touchedIds = new Set<number>();
    let completedAny = false;
    for (const [i, update] of updates.entries()) {
      const status = str(update, "status");
      const touch = TOUCH_BY_STATUS[status ?? ""];
      if (!touch) continue;
      const id = typeof update.id === "number" ? update.id : undefined;
      const stateTask = id === undefined ? undefined : state?.find((task) => task.id === id);
      // The Go task tool marks explicit in_progress updates from its pre-state.
      // A false marker is a status reassertion carrying notes, not a fresh
      // start. Unmarked historical state keeps the existing argument-only
      // rendering for transcripts written before this marker existed.
      if (touch === "started" && stateTask?.started === false) continue;
      if (id !== undefined) touchedIds.add(id);
      if (touch === "done" || touch === "cancelled") completedAny = true;
      rows.push({
        key: `update_${i}`,
        touch,
        label: taskLabel(state, id),
        note: str(update, "notes") || undefined,
      });
    }
    // The daemon may advance a DIFFERENT task to in_progress as a side
    // effect of this same call (session_tools_task.go's auto-advance); that
    // task never appears in the caller's own `updates` above.
    const started = autoStartedTask(state, touchedIds, completedAny);
    if (started) {
      rows.push({ key: `auto_started_${started.id}`, touch: "started", label: taskLabel(state, started.id) });
    }
    return rows;
  }
  return undefined;
}

// touchKind's status-to-flag mapping for the three statuses the card renders as
// a row (renderer-format.js:525-533's touchKind, gated by renderer.js:5010).
const TOUCH_BY_STATUS: Record<string, string> = {
  done: "done",
  cancelled: "cancelled",
  in_progress: "started",
};

const PROGRESS_RE = /Progress:\s*(\d+)\s*\/\s*(\d+)\s*tasks complete/;

function parseProgress(output: string | undefined): Progress | undefined {
  if (!output) return undefined;
  const m = PROGRESS_RE.exec(output);
  if (!m) return undefined;
  return { done: Number(m[1]), total: Number(m[2]) };
}

// isTaskMutation is the non-suppression predicate: a valid append/update with
// at least one row is the only thing that renders. It's re-derived (not cached)
// so suppress() and the body agree exactly.
function isTaskMutation(item: ItemModel): boolean {
  return mutationRows(item) !== undefined;
}

function TaskCardRow({ row }: { row: TouchedRow }) {
  return (
    <div className={CLASS.row} data-testid="task-card-row" data-touch={row.touch}>
      <span className={CLASS.flag}>{row.touch}</span>
      <span className={CLASS.desc}>{row.label}</span>
      {row.note && <span className={CLASS.note}>{row.note}</span>}
    </div>
  );
}

function TaskCardBody({ item }: ToolRenderProps) {
  // A failed mutation renders no card - ToolCallItem's generic failed-row
  // treatment already shows the error text (mirrors the legacy card being
  // appended only on success).
  if (item.error) return null;
  const rows = mutationRows(item) ?? [];
  const progress = parseProgress(item.output);
  return (
    <div className={CLASS.card} data-testid="task-card">
      {progress && (
        <div className={CLASS.head}>
          <span className={CLASS.progress} data-testid="task-card-progress">
            {progress.done} / {progress.total}
          </span>
          <Meter
            label={`Task progress: ${progress.done} of ${progress.total} complete`}
            value={progress.done}
            max={progress.total}
            tone="neutral"
          />
        </div>
      )}
      {rows.length > 0 && (
        <div className={CLASS.rows}>
          {rows.map((row) => (
            <TaskCardRow key={row.key} row={row} />
          ))}
        </div>
      )}
    </div>
  );
}

registerToolRenderer({
  match: "task_list",
  icon: "tasks",
  summary(item: ItemModel) {
    const progress = parseProgress(item.output);
    return progress ? `Tasks · ${progress.done} / ${progress.total}` : "Tasks";
  },
  body: TaskCardBody,
  // The card is a header, not a fold-to-open tool row - open it at settle so a
  // task change is visible without a click, the way the legacy always-visible
  // card was (a manual collapse afterward still sticks, ToolCallItem's own
  // userToggled guard).
  autoExpand: () => true,
  // A read (view) or a malformed non-mutation renders nothing; a failed call is
  // never suppressed so its error still surfaces (ToolCallItem generic path).
  suppress: (item) => !item.error && !isTaskMutation(item),
});
