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
import { TaskCheck, type TaskTouch } from "./taskCheck";
import styles from "./taskcard.module.css";
import { autoStartedTask, parseTaskState, taskLabel } from "./taskData";

const CLASS = {
  card: requireClass(styles.card, "taskcard.module.css", "card"),
  head: requireClass(styles.head, "taskcard.module.css", "head"),
  rows: requireClass(styles.rows, "taskcard.module.css", "rows"),
  row: requireClass(styles.row, "taskcard.module.css", "row"),
  rowText: requireClass(styles.rowText, "taskcard.module.css", "rowText"),
  desc: requireClass(styles.desc, "taskcard.module.css", "desc"),
  descStruck: requireClass(styles.descStruck, "taskcard.module.css", "descStruck"),
  note: requireClass(styles.note, "taskcard.module.css", "note"),
  progress: requireClass(styles.progress, "taskcard.module.css", "progress"),
  srOnly: requireClass(styles.srOnly, "taskcard.module.css", "srOnly"),
};

interface TouchedRow {
  key: string;
  touch: TaskTouch; // added | done | cancelled | started
  label: string; // description (append; update when state is known) or "#<id>" (update, state absent)
  note?: string;
}

interface Progress {
  done: number;
  total: number;
  cancelled?: number;
  remaining?: number;
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

// A valid mutation is a current add/update batch or its historical
// action:append/action:update equivalent. Anything else (view, or a malformed
// call) is not a card.
function mutationRows(item: ItemModel): TouchedRow[] | undefined {
  const args = parseArgs(item.argumentsJSON);
  const action = str(args, "action") ?? "";
  if (action === "append") {
    const tasks = asObjectArray(args.tasks);
    if (tasks.length === 0) return undefined;
    return appendRows(tasks, true);
  }
  if (action === "update") {
    const updates = finalUpdates(asObjectArray(args.updates));
    return updates.length > 0 ? updateRows(item, updates) : undefined;
  }
  if (action !== "") return undefined;

  const adds = asObjectArray(args.add);
  const updates = finalUpdates(asObjectArray(args.update));
  if (adds.length === 0 && updates.length === 0) return undefined;
  return [...appendRows(adds, false), ...updateRows(item, updates)];
}

function appendRows(tasks: Record<string, unknown>[], legacy: boolean): TouchedRow[] {
  return tasks.map((task, i) => ({
    key: `append_${i}`,
    touch: "added",
    label: str(task, "description") ?? (legacy ? str(task, "prompt") : undefined) ?? "(untitled task)",
  }));
}

function updateRows(item: ItemModel, updates: Record<string, unknown>[]): TouchedRow[] {
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
    // A suppressed status reassertion still belongs to this call. Record it
    // before filtering so it cannot be rediscovered as an auto-start below.
    if (id !== undefined) touchedIds.add(id);
    // The Go task tool marks every current task from this call's pre-state.
    // A false marker is a status reassertion carrying notes, not a fresh
    // start. Unmarked historical state keeps the existing argument-only
    // rendering for transcripts written before this marker existed.
    if (touch === "started" && stateTask?.started === false) continue;
    if (touch === "done" || touch === "cancelled") completedAny = true;
    rows.push({
      key: `update_${i}`,
      touch,
      label: taskLabel(state, id),
      note: str(update, "notes") || undefined,
    });
  }
  // The daemon may advance a DIFFERENT task to in_progress as a side effect
  // of this same call (session_tools_task.go's auto-advance); that task never
  // appears in the caller's own `updates` above.
  const started = autoStartedTask(state, touchedIds, completedAny);
  if (started) {
    rows.push({ key: `auto_started_${started.id}`, touch: "started", label: taskLabel(state, started.id) });
  }
  return rows;
}

// touchKind's status-to-flag mapping for the three statuses the card renders as
// a row (renderer-format.js:525-533's touchKind, gated by renderer.js:5010).
const TOUCH_BY_STATUS: Record<string, TaskTouch> = {
  done: "done",
  cancelled: "cancelled",
  in_progress: "started",
};

const PROGRESS_RE = /Progress:\s*(\d+)\s*\/\s*(\d+)\s*tasks complete/g;
const OUTCOME_PROGRESS_RE = /Progress:\s*(\d+)\s+done,\s*(\d+)\s+cancelled,\s*(\d+)\s+remaining\s*\((\d+)\s+total\)/g;

function lastProgressMatch(output: string, pattern: RegExp): RegExpMatchArray | undefined {
  // matchAll's iterator starts at its pattern's lastIndex. Clone the global
  // pattern for every parse so a later consumer cannot carry mutable state
  // into this helper.
  const matches = [...output.matchAll(new RegExp(pattern.source, pattern.flags))];
  return matches[matches.length - 1];
}

function parseProgress(output: string | undefined): Progress | undefined {
  if (!output) return undefined;
  const outcome = lastProgressMatch(output, OUTCOME_PROGRESS_RE);
  const legacy = lastProgressMatch(output, PROGRESS_RE);
  if (outcome && (!legacy || (outcome.index ?? -1) > (legacy.index ?? -1))) {
    return {
      done: Number(outcome[1]),
      cancelled: Number(outcome[2]),
      remaining: Number(outcome[3]),
      total: Number(outcome[4]),
    };
  }
  if (!legacy) return undefined;
  return { done: Number(legacy[1]), total: Number(legacy[2]) };
}

// isTaskMutation is the non-suppression predicate: a valid append/update with
// at least one row is the only thing that renders. It's re-derived (not cached)
// so suppress() and the body agree exactly.
function isTaskMutation(item: ItemModel): boolean {
  return mutationRows(item) !== undefined;
}

// The word assistive tech reads for each touch - the visible flag label is
// gone, so the status rides along visually-hidden beside the glyph.
const TOUCH_WORD: Record<TaskTouch, string> = {
  added: "added",
  done: "done",
  cancelled: "cancelled",
  started: "started",
};

const TOUCH_SUMMARY_MARK: Record<TaskTouch, string> = {
  added: "☐",
  done: "☑",
  cancelled: "☒",
  started: "☐",
};

function taskMutationSummary(item: ItemModel): string {
  const rows = mutationRows(item) ?? [];
  return rows.map((row) => `${TOUCH_SUMMARY_MARK[row.touch]} ${row.label}`).join(" · ");
}

function TaskCardRow({ row }: { row: TouchedRow }) {
  const struck = row.touch === "done" || row.touch === "cancelled";
  return (
    <div className={CLASS.row} data-testid="task-card-row" data-touch={row.touch}>
      <TaskCheck touch={row.touch} />
      <div className={CLASS.rowText}>
        <span className={CLASS.srOnly}>{TOUCH_WORD[row.touch]}</span>
        <span className={struck ? CLASS.descStruck : CLASS.desc}>{row.label}</span>
        {row.note && <span className={CLASS.note}>Notes: {row.note}</span>}
      </div>
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
            {progress.cancelled === undefined || progress.remaining === undefined
              ? `${progress.done} of ${progress.total} done`
              : `${progress.done} done, ${progress.cancelled} cancelled, ${progress.remaining} remaining (${progress.total} total)`}
          </span>
          <Meter
            label={
              progress.cancelled === undefined || progress.remaining === undefined
                ? `Task progress: ${progress.done} of ${progress.total} complete`
                : `Task progress: ${progress.done} done, ${progress.cancelled} cancelled, ${progress.remaining} remaining (${progress.total} total)`
            }
            value={progress.done + (progress.cancelled ?? 0)}
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
  fold: "never", // the plan card stays visible
  icon: "tasks",
  summary: taskMutationSummary,
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
