// The task_list descriptor: renders a task-update card for a successful
// append/update mutation (parity-m4 §9:239 renderer.js:4769-4786,4966-5061;
// contracts-transcript §11). `action:"view"` and a malformed non-mutation
// render nothing (suppressed - the legacy "no card, no divider, no tool-call
// row"). A FAILED mutation renders no card either: its error is surfaced by
// ToolCallItem's generic failed-row treatment instead (the legacy card was
// appended only `if (!data.error)`).
//
// Wire truth (why this card is scoped to the touched rows + a progress head):
// the authoritative task State - store.View(), which carries every task's
// status, description and minted timestamps - rides the tool result's
// StateResult.State (agent/session_tools_task.go), but the reducer drops
// ThreadItem.raw (protocol/reducer.ts's wireItemToModel), so the model
// preserves only two usable fields: argumentsJSON (the rows the caller named)
// and the output text (whose "Progress: <done>/<total> tasks complete." footer
// gives the progress head, agent/session_tools_task.go formatTaskList/the
// append+update acknowledgements). Conscious divergences from the legacy card,
// recorded for T8's sweep: an update row shows the task id + status + note but
// NOT a description (descriptions live only in the dropped State / the cache
// legacy kept); there is no full-list "show all" fold, no surrounding-context
// rows, and no inferred "and now working on X" auto-advance row - the card
// shows only what the caller's own args prove.
import type { ItemModel } from "../../../../protocol/model";
import { Meter } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import { parseArgs, str } from "./helpers";
import styles from "./taskcard.module.css";

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
  label: string; // description (append) or "#<id>" (update)
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

function taskAction(item: ItemModel): string {
  return str(parseArgs(item.argumentsJSON), "action") ?? "";
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
    const updates = asObjectArray(args.updates);
    if (updates.length === 0) return undefined;
    // Only a real status change earns a row - matching the legacy card, which
    // flags exactly done/cancelled/in_progress updates (renderer.js:5010) and
    // renders a note-only or reopened update as no per-row change at all.
    const rows: TouchedRow[] = [];
    for (const [i, update] of updates.entries()) {
      const status = str(update, "status");
      const touch = TOUCH_BY_STATUS[status ?? ""];
      if (!touch) continue;
      const id = typeof update.id === "number" ? update.id : undefined;
      rows.push({
        key: `update_${i}`,
        touch,
        label: id === undefined ? "(task)" : `#${id}`,
        note: str(update, "notes") || undefined,
      });
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
