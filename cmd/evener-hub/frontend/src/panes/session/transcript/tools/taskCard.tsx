// The task_list descriptor: renders a task-update card for a successful
// append/update mutation (parity-m4 §9:239 renderer.js:4769-4786,4966-5061;
// contracts-transcript §11). `action:"view"` and a malformed non-mutation
// render nothing (suppressed - the legacy "no card, no divider, no tool-call
// row"). A FAILED mutation renders no card either: its error is surfaced by
// ToolCallItem's generic failed-row treatment instead (the legacy card was
// appended only `if (!data.error)`).
//
// The 2026-09 rework (Jesse's ruleset): the card is a WINDOW onto the plan,
// not a changelog. The folded summary line names only the most recent update
// (mark + label: "→ fifth"); opening the row swaps that line for a recap
// sentence of the whole call ('Completed "fourth"; started "fifth"') and the
// body shows at most three tasks - most-recently-settled, in-progress, next -
// joined by a hairline spine. A note renders only when THIS call added it,
// set in the prose face. The footer keeps the aggregate sentence + meter and
// adds an "Open task list" affordance (the same workspace toggle /tasks
// runs). The card settles FOLDED at every verbosity level (foldByDefault),
// so a run of updates reads as quiet one-liners; the tasks pane remains the
// full-plan view. The 2026-07-15 "changes-only card" trims are superseded.
//
// Wire truth: agent/session_tools_task.go's task_list executor returns
// tool.StateResult{State: store.View()} on every view/append/update call -
// the authoritative snapshot carrying every task's status, description, and
// minted timestamps. That State rides all the way to the client as
// item.raw (registry.go marshals it straight into ToolState; appprojector
// and apptranscript carry it onto ThreadItem.raw unchanged; reducer.ts's
// wireItemToModel keeps it as item.raw verbatim), and the AppWire package's
// parseTaskListData narrows it, since it's the same agent/task/task_store.go
// Task[] shape the tasks side panel already parses from a different wire
// path.
//
// raw is absent for an old daemon that predates StateResult.State and for a
// transcript replayed from before it existed - a real, ongoing case, not
// just a historical one - and the card then degrades to exactly its
// argument-only rendering: the window cannot be derived from state it
// doesn't have, so the body falls back to one row per touched task with
// "#<id>" labels, plus one row per status-less fresh note (Jesse's ruling on
// #2257: that note has no slot to ride and must not be lost), and no
// fabricated auto-start (the contract of the AppWire package's
// taskListStep.ts).
import type { ItemModel, TaskRow, TouchedRow } from "@evener/appwire-client";
import {
  freshNotes,
  mutationRows,
  parseArgs,
  parseTaskListData,
  str,
  taskAggregateLabel,
  taskLabel,
  taskMutationRecap,
  taskMutationSummary,
  taskSettledCount,
} from "@evener/appwire-client";
import { requestPaneFocus, workspaceStore } from "../../../../shell/workspace";
import { Meter, OpenButton, VisuallyHidden } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer, toolCallFailed } from "../toolRenderers";
import { TaskCheck, TOUCH_WORD, taskStatusTouch, taskStatusWord } from "./taskCheck";
import styles from "./taskcard.module.css";

const CLASS = {
  card: requireClass(styles.card, "taskcard.module.css", "card"),
  head: requireClass(styles.head, "taskcard.module.css", "head"),
  rows: requireClass(styles.rows, "taskcard.module.css", "rows"),
  row: requireClass(styles.row, "taskcard.module.css", "row"),
  rowText: requireClass(styles.rowText, "taskcard.module.css", "rowText"),
  desc: requireClass(styles.desc, "taskcard.module.css", "desc"),
  descStruck: requireClass(styles.descStruck, "taskcard.module.css", "descStruck"),
  descNow: requireClass(styles.descNow, "taskcard.module.css", "descNow"),
  descNext: requireClass(styles.descNext, "taskcard.module.css", "descNext"),
  note: requireClass(styles.note, "taskcard.module.css", "note"),
  noteMark: requireClass(styles.noteMark, "taskcard.module.css", "noteMark"),
  progress: requireClass(styles.progress, "taskcard.module.css", "progress"),
  spined: requireClass(styles.spined, "taskcard.module.css", "spined"),
};

export interface Progress {
  done: number;
  total: number;
  cancelled?: number;
  remaining?: number;
}

const PROGRESS_RE = /Progress:\s*(\d+)\s*\/\s*(\d+)\s*tasks complete/g;
const OUTCOME_PROGRESS_RE = /Progress:\s*(\d+)\s+done,\s*(\d+)\s+cancelled,\s*(\d+)\s+remaining\s*\((\d+)\s+total\)/g;

function lastProgressMatch(output: string, pattern: RegExp): RegExpMatchArray | undefined {
  // matchAll's iterator starts at its pattern's lastIndex. Clone the global
  // pattern for every parse so a later consumer cannot carry mutable state
  // into this helper.
  const matches = [...output.matchAll(new RegExp(pattern.source, pattern.flags))];
  return matches[matches.length - 1];
}

export function parseProgress(output: string | undefined): Progress | undefined {
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

// isTaskMutation is the non-suppression predicate: a valid append/update that
// actually changed at least one task status renders, and so does one that left
// a fresh note on a status-less task (Jesse's ruling on #2257: the note must
// not be lost, even when it is the call's only news).
function isTaskMutation(item: ItemModel): boolean {
  return (mutationRows(item) ?? []).length > 0 || statuslessNoteRows(item).length > 0;
}

// A note THIS call added to a task without giving that task any status word.
// A note riding a status word is already carried by that row (updateRows
// merges it and freshNotes hands it to the window slot), so only these can
// vanish. Update-shaped calls carry notes per update; append-shaped calls mint
// no visible ids, so the same degradation as before remains there.
interface NoteRow {
  id: number;
  note: string;
}

const statuslessNoteRowsCache = new WeakMap<ItemModel, NoteRow[]>();

// One derivation per item, like mutationRows: the folded summary, the recap,
// suppress(), and the body all ask for the same rows from one
// argumentsJSON/raw pair, and ItemModels are immutable snapshots.
function statuslessNoteRows(item: ItemModel): NoteRow[] {
  const cached = statuslessNoteRowsCache.get(item);
  if (cached) return cached;
  const args = parseArgs(item.argumentsJSON);
  const action = str(args, "action") ?? "";
  const updates = action === "update" ? args.updates : action === "" ? args.update : undefined;
  const statused = new Set<number>();
  for (const update of Array.isArray(updates) ? updates : []) {
    if (typeof update !== "object" || update === null || Array.isArray(update)) continue;
    const record = update as Record<string, unknown>;
    const id = typeof record.id === "number" ? record.id : undefined;
    if (id !== undefined && str(record, "status")) statused.add(id);
  }
  const rows = [...freshNotes(item)].filter(([id]) => !statused.has(id)).map(([id, note]) => ({ id, note }));
  statuslessNoteRowsCache.set(item, rows);
  return rows;
}

// The folded line for a call whose only news is a status-less note: the
// shared summary and recap are mutation-only, so name the noted task with the
// same label its row uses. The note mark keeps the card's mark + label
// grammar without inventing prose. Jesse's ruling on #2257.
const NOTE_MARK = "✎";

function taskNoteSummary(item: ItemModel): string {
  const row = statuslessNoteRows(item).at(-1);
  return row ? `${NOTE_MARK} ${taskLabel(parseTaskListData(item.raw), row.id)}` : "";
}

// ---- the window: settled, working, next -------------------------------

// The three slots the open body shows, from the authoritative state: the
// most recently SETTLED task (done or cancelled - a cancellation is the
// plan's most recent "finished" event and belongs in the first slot rather
// than vanishing), the in-progress task, and the first open task in list
// order. Timestamps order the settled slot; `prefer` (the terminal task
// THIS call settled) breaks ties first, so a stampless legacy snapshot
// names the task the folded line just named instead of a list-order
// stranger; remaining ties (including all-absent stamps, when the call
// settled nothing) fall back to the LAST settled row in list order, the
// same degradation the panel gives.
export interface TaskWindow {
  settled?: TaskRow;
  current?: TaskRow;
  next?: TaskRow;
}

export function stateWindow(tasks: TaskRow[] | null, prefer?: number): TaskWindow {
  if (!tasks) return {};
  const settledAll = tasks.filter((task) => task.status === "done" || task.status === "cancelled");
  // Only the terminal stamp orders settles: updatedAt advances on any later
  // edit (a note, an effort change), so it is not a settle moment - a
  // merely-annotated old cancellation must not outrank a newer completion.
  // The stamp itself is parsed, never compared as a string: RFC3339Nano
  // trims trailing zeros, so same-second stamps arrive in different lengths
  // (".5Z" sorts after ".55Z" byte-wise even though it is the earlier
  // instant), and mixed UTC offsets compare by wall-clock digits rather than
  // instants. The fraction is split off BEFORE Date.parse - engines are free
  // to differ on rounding more than three fractional digits, and one call
  // can settle two tasks in the same batch microseconds apart, so the
  // sub-millisecond digits must survive; the second-resolution base parses
  // identically everywhere, and the fraction folds back as whole
  // milliseconds plus a remainder. That fold keeps fidelity down to ~0.5us
  // (a double's ULP at epoch scale); finer distinctions collapse to a tie,
  // which the window resolves by preferring this call's last terminal row -
  // the batch's true latest settle, since the store stamps sequentially in
  // batch order. An absent or unparseable stamp reads as -Infinity: unknown
  // settles lose to any stamped one, and ties degrade to list order through
  // the comparison below.
  const settleKey = (task: TaskRow): number => {
    const raw = task.completedAt ?? "";
    const parts = raw.match(/^(.*T\d{2}:\d{2}:\d{2})(?:\.(\d+))?(\D.*)$/);
    const at = Date.parse(parts ? `${parts[1]}${parts[3]}` : raw);
    if (Number.isNaN(at)) return Number.NEGATIVE_INFINITY;
    const fraction = parts?.[2]?.padEnd(9, "0") ?? "000000000";
    return at + Number(fraction.slice(0, 3)) + Number(`0.${fraction.slice(3)}`);
  };
  // Ties (equal or absent timestamps) resolve first to the preferred task,
  // then to the later list entry - the most recent settle in list order
  // rather than the first.
  const winsTie = (candidate: TaskRow, current: TaskRow): boolean => {
    if (prefer === undefined) return true;
    if (candidate.id === prefer && current.id !== prefer) return true;
    if (current.id === prefer && candidate.id !== prefer) return false;
    return true;
  };
  let settled: TaskRow | undefined;
  for (const task of settledAll) {
    if (settled === undefined) {
      settled = task;
      continue;
    }
    const key = settleKey(task);
    const best = settleKey(settled);
    if (key > best || (key === best && winsTie(task, settled))) settled = task;
  }
  return {
    settled,
    current: tasks.find((task) => task.status === "in_progress"),
    next: tasks.find((task) => task.status === "open"),
  };
}

type SlotKind = "settled" | "current" | "next";

// The label ink per slot: settled reads struck through low ink, the working
// task carries the card's one semibold line, the next task stays quiet.
const SLOT_DESC_CLASS: Record<SlotKind, string> = {
  settled: CLASS.descStruck,
  current: CLASS.descNow,
  next: CLASS.descNext,
};

// One window slot: its state-named glyph, the label on the slot's ink, and
// this call's fresh note (if any) hanging under the label. The spined class
// joins the slots into one progression (see the stylesheet). Glyph and
// screen-reader word both key off the task's state through STATUS_TOUCH -
// the same map the tasks pane's rows use; stateWindow guarantees a slot's
// kind and its task's status agree.
function TaskWindowRow({ task, kind, note }: { task: TaskRow; kind: SlotKind; note?: string }) {
  const touch = taskStatusTouch(task.status);
  return (
    <div className={`${CLASS.row} ${CLASS.spined}`} data-testid="task-card-row" data-kind={kind}>
      <TaskCheck touch={touch} />
      <div className={CLASS.rowText}>
        <VisuallyHidden>{taskStatusWord(task.status)}</VisuallyHidden>
        <span className={SLOT_DESC_CLASS[kind]}>{task.description}</span>
        {note && <span className={CLASS.note}>{note}</span>}
      </div>
    </div>
  );
}

// The no-raw fallback row: one line per touched task, argument-only. Notes
// render bare (same treatment as the window's), never prefixed.
function TaskFallbackRow({ row }: { row: TouchedRow }) {
  const struck = row.touch === "done" || row.touch === "cancelled";
  return (
    <div className={CLASS.row} data-testid="task-card-row" data-touch={row.touch}>
      <TaskCheck touch={row.touch} />
      <div className={CLASS.rowText}>
        <VisuallyHidden>{TOUCH_WORD[row.touch]}</VisuallyHidden>
        <span className={struck ? CLASS.descStruck : CLASS.desc}>{row.label}</span>
        {row.note && <span className={CLASS.note}>{row.note}</span>}
      </div>
    </div>
  );
}

// A status-less note's own row: the task's label and the note. No TaskCheck
// glyph - a note is not a status touch - but the empty mark keeps the text
// column aligned with the mutation rows above it. Jesse's ruling on #2257.
function TaskNoteRow({ row, label }: { row: NoteRow; label: string }) {
  return (
    <div className={CLASS.row} data-testid="task-card-row" data-kind="note">
      <span className={CLASS.noteMark} aria-hidden="true" />
      <div className={CLASS.rowText}>
        <span className={CLASS.desc}>{label}</span>
        <span className={CLASS.note}>{row.note}</span>
      </div>
    </div>
  );
}

function TaskCardBody({ item, sessionRef }: ToolRenderProps) {
  // A failed mutation renders no card - ToolCallItem's generic failed-row
  // treatment already owns the failure (mirrors the legacy card being
  // appended only on success). The shared predicate, not error text alone:
  // a status-only or exit-code failure must suppress the card too.
  if (toolCallFailed(item)) return null;
  const touched = mutationRows(item) ?? [];
  const progress = parseProgress(item.output);
  // The parsed footer keeps the backend's own outcome shape (done/cancelled/
  // remaining/total, or legacy done/total); only the displayed sentence is
  // condensed, through the same helper the panel trigger uses.
  // Derived unconditionally: taskAggregateLabel always returns a non-empty
  // sentence, and the footer below only renders when progress parsed.
  const progressLabel = taskAggregateLabel({
    total: progress?.total ?? 0,
    done: progress?.done ?? 0,
    cancelled: progress?.cancelled,
    remaining: progress?.remaining,
  });
  const state = parseTaskListData(item.raw);
  // The terminal task this call settled wins the window's stampless tie -
  // the body must name the same task the folded line does, and the line
  // names the LAST row (taskMutationSummary's .at(-1)), so the LAST
  // terminal row is the preference, not the first.
  const settledTouch = touched.filter((row) => row.touch === "done" || row.touch === "cancelled").at(-1);
  const win = stateWindow(state, settledTouch?.id);
  const fresh = freshNotes(item);
  // A status-less note on a task the window already shows is not lost, so it
  // must not get a second row.
  const covered = new Set<number>();
  for (const task of [win.settled, win.current, win.next]) if (task) covered.add(task.id);
  const notes = statuslessNoteRows(item).filter((row) => !covered.has(row.id));
  return (
    <div className={CLASS.card} data-testid="task-card">
      {state !== null ? (
        <div className={CLASS.rows}>
          {/* The window is its own rows container so the spine ends at the last
              slot; a note row appended after it must not extend the hairline. */}
          <div className={CLASS.rows}>
            {win.settled && (
              <TaskWindowRow
                key={`settled_${win.settled.id}`}
                task={win.settled}
                kind="settled"
                note={fresh.get(win.settled.id)}
              />
            )}
            {win.current && (
              <TaskWindowRow
                key={`current_${win.current.id}`}
                task={win.current}
                kind="current"
                note={fresh.get(win.current.id)}
              />
            )}
            {win.next && (
              <TaskWindowRow key={`next_${win.next.id}`} task={win.next} kind="next" note={fresh.get(win.next.id)} />
            )}
          </div>
          {notes.map((row) => (
            <TaskNoteRow key={row.id} row={row} label={taskLabel(state, row.id)} />
          ))}
        </div>
      ) : (
        (touched.length > 0 || notes.length > 0) && (
          <div className={CLASS.rows}>
            {touched.map((row) => (
              <TaskFallbackRow key={row.key} row={row} />
            ))}
            {notes.map((row) => (
              <TaskNoteRow key={row.id} row={row} label={taskLabel(null, row.id)} />
            ))}
          </div>
        )
      )}
      {(progress || sessionRef !== undefined) && (
        <div className={CLASS.head}>
          {progress && (
            <>
              <span className={CLASS.progress} data-testid="task-card-progress">
                {progressLabel}
              </span>
              <Meter
                label={`Task progress: ${progressLabel}`}
                value={taskSettledCount(progress)}
                max={progress.total}
                tone="neutral"
              />
            </>
          )}
          {/* The whole-list affordance: the card hands off to the pane that
              owns the full plan. An OPEN, not the /tasks palette's toggle -
              the label promises opening, so the click focuses the pane when
              the reader already has it rather than closing it. The daemon
              appends a Progress footer to every successful mutation, so the
              aggregate and meter normally render beside it; a historical
              transcript without one still gets the button - the affordance
              needs no parsed progress. Hidden on surfaces with no owning
              session (a read-only transcript pane) - a control that cannot
              open anything must not render. */}
          {sessionRef !== undefined && (
            <OpenButton
              label="Open task list"
              onClick={() => {
                const paneId = workspaceStore
                  .getState()
                  .openPane("sessionTasks", { ref: sessionRef }, { slot: "secondary" });
                requestPaneFocus(paneId);
              }}
            />
          )}
        </div>
      )}
    </div>
  );
}

registerToolRenderer({
  match: "task_list",
  fold: "never", // the plan card stays visible
  icon: "tasks",
  // The shared summary and recap name mutation rows only. A call whose only
  // news is a status-less note falls back to the note's task label, so the
  // folded line is never blank and the reader has a cue to open the row.
  summary: (item) => taskMutationSummary(item) || taskNoteSummary(item),
  summaryWhenExpanded: (item) => taskMutationRecap(item) || taskNoteSummary(item),
  body: TaskCardBody,
  // Settle folded at EVERY verbosity level (activity/full force-expand every
  // other body through the level default): the folded line already carries
  // the news, so a run of updates reads as quiet one-liners and the reader's
  // own click opens the window. A manual open still sticks (ToolCallItem's
  // explicit store entry beats any fallback).
  foldByDefault: true,
  // A read (view), a malformed non-mutation, or a status-unchanged update
  // with no status-less note renders nothing; a failed call is never
  // suppressed so its failure still surfaces (ToolCallItem generic path) -
  // for every failure shape the shared predicate recognizes, not just error
  // text.
  suppress: (item) => !toolCallFailed(item) && !isTaskMutation(item),
});
