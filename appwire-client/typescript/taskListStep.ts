// What a task_list call did, in both clients: the tasks it added, started,
// completed or dropped, read from its arguments against the task list it
// returned, and the words a step's line says it with. The web's task card and
// the phone's step lines read the same rows.
//
// The call's result carries the authoritative task snapshot,
// end to end as item.raw: agent/session_tools_task.go's task_list executor
// returns tool.StateResult{State: store.View()} on every view/append/update
// call; agent/internal/tool/registry.go's ExecuteCall JSON-marshals State
// straight into ToolState (no wrapping key, no rename - the wire array IS
// item.raw); internal/appprojector and internal/apptranscript both carry it
// onto ThreadItem.raw unchanged; protocol/reducer.ts's wireItemToModel keeps
// it as item.raw verbatim. The shape is therefore the same
// agent/task/task_store.go Task[] the tasks side panel already parses from a
// different wire path (TaskListResponse.data) - the AppWire package's
// parseTaskListData is reused rather than reimplemented.
//
// Absent/malformed raw - an old daemon predating StateResult.State, or a
// transcript replayed from before it existed - parses to null, same
// contract as parseTaskListData itself: "we don't know the state", never
// "zero tasks". Callers must degrade to argument-only rendering in that
// case, never an empty checklist or a fabricated auto-start.
import type { ItemModel } from "./model";
import { parseTaskListData, type TaskRow } from "./taskListData";
import { parseArgs, str } from "./toolCallText";

/** The parts of a task_list step its rows read: the call's arguments and the
 * task list it returned. */
export type TaskListStep = Pick<ItemModel, "argumentsJSON" | "raw">;

// The label an update row shows for a task: its description when the
// authoritative state names one (mirrors the legacy card's
// buildTaskRowLine, which preferred task.description over a bare id -
// cmd/evener-hub/assets/renderer-format.js), falling back to "#<id>" when
// state is absent or the id isn't found there.
export function taskLabel(tasks: TaskRow[] | null, id: number | undefined): string {
  if (id === undefined) return "(task)";
  const description = tasks?.find((t) => t.id === id)?.description;
  return description || `#${id}`;
}

// The task the daemon auto-started as a side effect of THIS update call -
// the "and now working on X" row docs/superpowers/plans/2026-07-15-inline-
// task-update-cards.md required keeping ("authoritative auto-activation").
// agent/session_tools_task.go auto-advances (store.NextEligible + an
// in_progress transition) exactly when the batch completed something (a
// done or cancelled row - completedAny, matching that Go gate's own name)
// and did not ALSO explicitly start a task itself; either way that decision
// already happened server-side and rides in the SAME State this call
// returns, so it's found rather than re-derived here. Current daemon snapshots
// mark every in-progress row: only a true marker is an auto-start candidate.
// Historical markerless snapshots retain the legacy fallback of finding the
// untouched in-progress task. touchedIds is every id the caller's own updates
// already earned a row for (including an explicit in_progress one), so an
// explicit start is never double-counted.
export function autoStartedTask(
  tasks: TaskRow[] | null,
  touchedIds: ReadonlySet<number>,
  completedAny: boolean,
): TaskRow | undefined {
  if (!tasks || !completedAny) return undefined;
  const current = tasks.filter((t) => t.status === "in_progress");
  if (current.some((t) => t.started !== undefined)) {
    return current.find((t) => t.started === true && !touchedIds.has(t.id));
  }
  return tasks.find((t) => t.status === "in_progress" && !touchedIds.has(t.id));
}

// What a call did to a task: added it, completed it, dropped it or started
// it. A task still waiting is not something a call did, so it is not one.
export type MutationTouch = "added" | "done" | "cancelled" | "started";

export interface TouchedRow {
  key: string;
  touch: MutationTouch;
  // The task's description, or "#<id>" for an update when the call returned
  // no task list to look the id up in.
  label: string;
  note?: string;
  // The id of the task an update touched. An added task has no id a client
  // can see yet.
  id?: number;
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
// daemon appends notes in call order either way). A completion followed by
// a note cannot be erased into suppression. The fallback agrees with
// freshNotes for every note riding an ID's status word - the raw path also
// carries a notes-only touch for an ID whose batch has no status word at
// all, hung on the window's state-derived slot; the argument-only fallback
// has no such slot and no row to hang it on, so that one shape renders only
// on the raw path, the same degradation as a fresh note on an appended
// task. Whether a status is RENDERABLE stays updateRows' own filter.
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
    // the two. Notes are simply that touch's note, last-wins by batch
    // position - when the status word itself carries a note, lastNotes
    // already holds it at the entry's own index, so the lookup can never
    // return an earlier touch here. The value agrees with freshNotes'
    // order-independent derivation on the raw path.
    if (!note) {
      marked.push(entry);
      continue;
    }
    marked.push({
      index: Math.max(entry.index, note.index),
      update: { ...entry.update, notes: str(note.update, "notes") },
    });
  }
  return [...marked, ...unmarked].sort((a, b) => a.index - b.index).map(({ update }) => update);
}

// A valid mutation is a current add/update batch or its historical
// action:append/action:update equivalent that CHANGES at least one task
// status. Anything else - a view, a malformed call, a reopen, a notes-only
// touch - is not a card: with no touch to name, neither the folded line nor
// the recap has anything true to say.
function deriveMutationRows(item: TaskListStep): TouchedRow[] | undefined {
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
const mutationRowsCache = new WeakMap<TaskListStep, { rows: TouchedRow[] | undefined }>();
export function mutationRows(item: TaskListStep): TouchedRow[] | undefined {
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

function updateRows(item: TaskListStep, updates: Record<string, unknown>[]): TouchedRow[] {
  // Only a real status change earns a row - matching the legacy card, which
  // flags exactly done/cancelled/in_progress updates (renderer.js:5010) and
  // renders a note-only or reopened update as no per-row change at all.
  const state = parseTaskListData(item.raw);
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
export function taskMutationSummary(item: TaskListStep): string {
  const last = mutationRows(item)?.at(-1);
  return last ? `${SUMMARY_MARK[last.touch]} ${last.label}` : "";
}

// The expanded line: a real recap of this call's whole change, one clause
// per verb in first-occurrence order, same-verb labels comma-joined inside
// their quotes ('Completed "second", "first"'). Sentence case: only the
// recap's first letter capitalizes, so the second clause reads
// '...; started "fifth"', not a run of title-cased verbs.
export function taskMutationRecap(item: TaskListStep): string {
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

// The notes THIS call added, keyed by task id - the only notes a client
// shows with the call (a stale note from an earlier call never shows). Update-shaped calls
// carry them per update; append-shaped calls mint no client-visible ids, so
// a fresh note on an appended task has no key to ride and stays unrendered.
export function freshNotes(item: TaskListStep): ReadonlyMap<number, string> {
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
