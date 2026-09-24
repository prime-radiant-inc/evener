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
// wireItemToModel keeps it as item.raw verbatim), and taskData.ts's
// parseTaskState narrows it - reusing the AppWire package's
// parseTaskListData, since it's the same agent/task/task_store.go Task[]
// shape the tasks side panel already parses from a different wire path.
//
// raw is absent for an old daemon that predates StateResult.State and for a
// transcript replayed from before it existed - a real, ongoing case, not
// just a historical one - and the card then degrades to exactly its
// argument-only rendering: the window cannot be derived from state it
// doesn't have, so the body falls back to one row per touched task with
// "#<id>" labels, and no fabricated auto-start (taskData.ts's own contract).
import type { ItemModel, TaskRow } from "@evener/appwire-client";
import { parseArgs, str, taskAggregateLabel } from "@evener/appwire-client";
import { requestPaneFocus, workspaceStore } from "../../../../shell/workspace";
import { Meter, OpenButton } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer, toolCallFailed } from "../toolRenderers";
import { STATUS_TOUCH, TaskCheck, type TaskTouch, TOUCH_WORD } from "./taskCheck";
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
  descNow: requireClass(styles.descNow, "taskcard.module.css", "descNow"),
  descNext: requireClass(styles.descNext, "taskcard.module.css", "descNext"),
  note: requireClass(styles.note, "taskcard.module.css", "note"),
  progress: requireClass(styles.progress, "taskcard.module.css", "progress"),
  spined: requireClass(styles.spined, "taskcard.module.css", "spined"),
  srOnly: requireClass(styles.srOnly, "taskcard.module.css", "srOnly"),
};

// A mutation touch only: "pending" is a window-slot state (TaskCheck's
// empty box) and never something a call did to a task, so every record
// keyed by what a call DID excludes it once, here.
export type MutationTouch = Exclude<TaskTouch, "pending">;

export interface TouchedRow {
  key: string;
  touch: MutationTouch;
  label: string; // description (append; update when state is known) or "#<id>" (update, state absent)
  note?: string;
  // The wire task id an update row touched; append rows mint no
  // client-visible id. Drives the window's tie-break preference.
  id?: number;
}

export interface Progress {
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
// final state. Render one status touch per ID from its last update that
// carries ANY status: done followed by reopen must end at the reopen (the
// card names the batch's final word, and a touched-away completion is not
// it), while the ID's last note-bearing touch annotates that final word
// instead of displacing it - whether the note arrived on its own update or
// attached to an earlier status update, before or after the final word (the
// daemon appends notes in call order either way, and freshNotes on the raw
// path collects any update carrying notes, so the no-raw fallback agrees
// with it). A completion followed by a note cannot be erased into
// suppression. Whether a status is RENDERABLE stays updateRows' own filter.
// Ordering by each ID's final occurrence keeps distinct IDs in the order the
// batch ends.
function finalUpdates(updates: Record<string, unknown>[]): Record<string, unknown>[] {
  type Entry = { index: number; update: Record<string, unknown> };
  const latestByID = new Map<number, Entry>();
  const unmarked: { index: number; update: Record<string, unknown> }[] = [];
  const lastNotes = new Map<number, Entry>();
  for (const [index, update] of updates.entries()) {
    const id = typeof update.id === "number" ? update.id : undefined;
    if (id === undefined) {
      unmarked.push({ index, update });
      continue;
    }
    if (str(update, "status")) {
      latestByID.set(id, { index, update });
    }
    if (str(update, "notes")) {
      lastNotes.set(id, { index, update });
    }
  }
  const marked: Entry[] = [];
  for (const [id, entry] of latestByID) {
    const note = lastNotes.get(id);
    // The ID's last note-bearing touch rides the row it annotates
    // whichever update carried it, and the merged row ends at the later of
    // the two. Notes follow last-wins by batch position, agreeing with
    // freshNotes' order-independent derivation on the raw path: when the
    // status word itself carries a note and is the later touch, that note
    // stays instead of being clobbered by an earlier touch's note.
    if (!note) {
      marked.push(entry);
      continue;
    }
    const ownNote = str(entry.update, "notes");
    const notes = ownNote && note.index < entry.index ? ownNote : str(note.update, "notes");
    marked.push({ index: Math.max(entry.index, note.index), update: { ...entry.update, notes } });
  }
  return [...marked, ...unmarked].sort((a, b) => a.index - b.index).map(({ update }) => update);
}

// A valid mutation is a current add/update batch or its historical
// action:append/action:update equivalent that CHANGES at least one task
// status. Anything else - a view, a malformed call, a reopen, a notes-only
// touch - is not a card: with no touch to name, neither the folded line nor
// the recap has anything true to say.
function deriveMutationRows(item: ItemModel): TouchedRow[] | undefined {
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

// One parse per item: summary(), suppress(), the recap, and the body all
// derive the same rows from one argumentsJSON/raw pair, and ItemModels are
// immutable snapshots (rebuilt, never mutated in place, on each wire
// frame), so item identity implies content. Caching on that identity keeps
// every caller in exact agreement without re-parsing per call site.
const mutationRowsCache = new WeakMap<ItemModel, { rows: TouchedRow[] | undefined }>();
export function mutationRows(item: ItemModel): TouchedRow[] | undefined {
  const cached = mutationRowsCache.get(item);
  if (cached) return cached.rows;
  const rows = deriveMutationRows(item);
  mutationRowsCache.set(item, { rows });
  return rows;
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
    // The Go task tool marks every current task and every terminal task
    // from this call's pre-state. A false marker is a status reassertion
    // carrying notes - not a fresh start, and not a fresh settle: the store
    // keeps the task's original CompletedAt on re-assertions, so a re-sent
    // done or cancelled word is an annotation of an old settle, not news.
    // Unmarked historical state keeps the existing argument-only rendering
    // for transcripts written before this marker existed.
    if (touch === "started" && stateTask?.started === false) continue;
    if ((touch === "done" || touch === "cancelled") && stateTask?.settled === false) continue;
    if (touch === "done" || touch === "cancelled") completedAny = true;
    rows.push({
      key: `update_${i}`,
      touch,
      label: taskLabel(state, id),
      note: str(update, "notes") || undefined,
      id,
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
const TOUCH_BY_STATUS: Record<string, MutationTouch> = {
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
// actually changed at least one task status is the only thing that renders.
function isTaskMutation(item: ItemModel): boolean {
  return (mutationRows(item) ?? []).length > 0;
}

// The text mark the folded summary line carries per mutation touch: the
// checkbox grammar in text form. "started" reads best as the arrow it means.
export const SUMMARY_MARK: Record<MutationTouch, string> = {
  added: "☐",
  done: "☑",
  cancelled: "☒",
  started: "→",
};

// The recap verb per mutation touch, for the expanded summary line.
const RECAP_VERB: Record<MutationTouch, string> = {
  added: "Added",
  done: "Completed",
  cancelled: "Dropped",
  started: "Started",
};

// The folded line: only the most recent update this call made - the last
// touch in the batch, which for the common completion is the daemon's
// auto-advance (the completion itself stays in the recap and the window).
function taskMutationSummary(item: ItemModel): string {
  const last = mutationRows(item)?.at(-1);
  return last ? `${SUMMARY_MARK[last.touch]} ${last.label}` : "";
}

// The expanded line: a real recap of this call's whole change, one clause
// per verb in first-occurrence order, same-verb labels comma-joined inside
// their quotes ('Completed "second", "first"'). Sentence case: only the
// recap's first letter capitalizes, so the second clause reads
// '...; started "fifth"', not a run of title-cased verbs.
function taskMutationRecap(item: ItemModel): string {
  const rows = mutationRows(item) ?? [];
  const byVerb = new Map<string, string[]>();
  for (const row of rows) {
    const verb = RECAP_VERB[row.touch].toLowerCase();
    const labels = byVerb.get(verb) ?? [];
    labels.push(`"${row.label}"`);
    byVerb.set(verb, labels);
  }
  const sentence = [...byVerb].map(([verb, labels]) => `${verb} ${labels.join(", ")}`).join("; ");
  return sentence.charAt(0).toUpperCase() + sentence.slice(1);
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
  // milliseconds plus a remainder. An absent or unparseable stamp reads as
  // -Infinity: unknown settles lose to any stamped one, and ties degrade to
  // list order through the >= below.
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

// The notes THIS call added, keyed by task id - the only notes the card may
// render (a stale note from an earlier call never shows). Update-shaped calls
// carry them per update; append-shaped calls mint no client-visible ids, so
// a fresh note on an appended task has no key to ride and stays unrendered.
export function freshNotes(item: ItemModel): ReadonlyMap<number, string> {
  const args = parseArgs(item.argumentsJSON);
  const action = str(args, "action") ?? "";
  const map = new Map<number, string>();
  const collect = (list: unknown) => {
    for (const update of asObjectArray(list)) {
      const id = typeof update.id === "number" ? update.id : undefined;
      const note = str(update, "notes") ?? "";
      if (id !== undefined && note !== "") map.set(id, note);
    }
  };
  if (action === "update") collect(args.updates);
  else if (action === "") collect(args.update);
  return map;
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
  const touch = STATUS_TOUCH[task.status];
  return (
    <div className={`${CLASS.row} ${CLASS.spined}`} data-testid="task-card-row" data-kind={kind}>
      <TaskCheck touch={touch} />
      <div className={CLASS.rowText}>
        <span className={CLASS.srOnly}>{TOUCH_WORD[touch]}</span>
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
        <span className={CLASS.srOnly}>{TOUCH_WORD[row.touch]}</span>
        <span className={struck ? CLASS.descStruck : CLASS.desc}>{row.label}</span>
        {row.note && <span className={CLASS.note}>{row.note}</span>}
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
  const state = parseTaskState(item.raw);
  // The terminal task this call settled wins the window's stampless tie -
  // the body must name the same task the folded line does, and the line
  // names the LAST row (taskMutationSummary's .at(-1)), so the LAST
  // terminal row is the preference, not the first.
  const settledTouch = touched.filter((row) => row.touch === "done" || row.touch === "cancelled").at(-1);
  const win = stateWindow(state, settledTouch?.id);
  const fresh = freshNotes(item);
  return (
    <div className={CLASS.card} data-testid="task-card">
      {state !== null ? (
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
      ) : (
        touched.length > 0 && (
          <div className={CLASS.rows}>
            {touched.map((row) => (
              <TaskFallbackRow key={row.key} row={row} />
            ))}
          </div>
        )
      )}
      {progress && (
        <div className={CLASS.head}>
          <span className={CLASS.progress} data-testid="task-card-progress">
            {progressLabel}
          </span>
          <Meter
            label={`Task progress: ${progressLabel}`}
            value={progress.done + (progress.cancelled ?? 0)}
            max={progress.total}
            tone="neutral"
          />
          {/* The whole-list affordance: the card hands off to the pane that
              owns the full plan. An OPEN, not the /tasks palette's toggle -
              the label promises opening, so the click focuses the pane when
              the reader already has it rather than closing it. Hidden on
              surfaces with no owning session (a read-only transcript pane) -
              a control that cannot open anything must not render. */}
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
  summary: taskMutationSummary,
  summaryWhenExpanded: taskMutationRecap,
  body: TaskCardBody,
  // Settle folded at EVERY verbosity level (activity/full force-expand every
  // other body through the level default): the folded line already carries
  // the news, so a run of updates reads as quiet one-liners and the reader's
  // own click opens the window. A manual open still sticks (ToolCallItem's
  // explicit store entry beats any fallback).
  foldByDefault: true,
  // A read (view), a malformed non-mutation, or a status-unchanged update
  // renders nothing; a failed call is never suppressed so its failure still
  // surfaces (ToolCallItem generic path) - for every failure shape the
  // shared predicate recognizes, not just error text.
  suppress: (item) => !toolCallFailed(item) && !isTaskMutation(item),
});
