// Five mock-up renderers for the inline task-list card rework, registered
// under throwaway tool names (mock_taskcard_a..e) so the production task_list
// descriptor in tools/taskCard.tsx stays untouched. taskcardmockups.html
// drives these through the REAL TranscriptBody + ToolCallItem pipeline, so
// the two-level disclosure, the fold interaction, the ToolRow grammar, and
// the theming are all production behavior - only the card's content is the
// proposal.
//
// The ruleset every variant obeys (Jesse's spec):
//   - folded: only the most recent update;
//   - unfolded: at most three tasks, ordered most-recently-settled,
//     in-progress, next;
//   - a note renders only when THIS call added it.
//
// Dev-support scaffolding for a design decision, not production code: the
// chosen variant gets implemented properly (and the derivation helpers here
// get either exported from taskCard.tsx or replaced by its own).

import type { ItemModel, TaskRow } from "@evener/appwire-client";
import { parseArgs, str, taskAggregateLabel } from "@evener/appwire-client";
import type { ReactNode } from "react";
import type { ToolRenderProps } from "../../panes/session/transcript/toolRenderers";
import { registerToolRenderer } from "../../panes/session/transcript/toolRenderers";
import { TaskCheck, type TaskTouch } from "../../panes/session/transcript/tools/taskCheck";
// The conservative variant renders inside today's card chrome unchanged.
import prodCard from "../../panes/session/transcript/tools/taskcard.module.css";
import { autoStartedTask, parseTaskState, taskLabel } from "../../panes/session/transcript/tools/taskData";
import { Meter } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./taskcardmockups.module.css";

const C = {
  card: requireClass(styles.card, "taskcardmockups.module.css", "card"),
  window: requireClass(styles.window, "taskcardmockups.module.css", "window"),
  winRow: requireClass(styles.winRow, "taskcardmockups.module.css", "winRow"),
  spined: requireClass(styles.spined, "taskcardmockups.module.css", "spined"),
  winRowText: requireClass(styles.winRowText, "taskcardmockups.module.css", "winRowText"),
  descNow: requireClass(styles.descNow, "taskcardmockups.module.css", "descNow"),
  descNext: requireClass(styles.descNext, "taskcardmockups.module.css", "descNext"),
  descStruck: requireClass(styles.descStruck, "taskcardmockups.module.css", "descStruck"),
  noteSerif: requireClass(styles.noteSerif, "taskcardmockups.module.css", "noteSerif"),
  noteSans: requireClass(styles.noteSans, "taskcardmockups.module.css", "noteSans"),
  pending: requireClass(styles.pending, "taskcardmockups.module.css", "pending"),
  foot: requireClass(styles.foot, "taskcardmockups.module.css", "foot"),
  footLabel: requireClass(styles.footLabel, "taskcardmockups.module.css", "footLabel"),
  footMeter: requireClass(styles.footMeter, "taskcardmockups.module.css", "footMeter"),
  pipe: requireClass(styles.pipe, "taskcardmockups.module.css", "pipe"),
  pipeArrow: requireClass(styles.pipeArrow, "taskcardmockups.module.css", "pipeArrow"),
  frag: requireClass(styles.frag, "taskcardmockups.module.css", "frag"),
  fragNow: requireClass(styles.fragNow, "taskcardmockups.module.css", "fragNow"),
  fragText: requireClass(styles.fragText, "taskcardmockups.module.css", "fragText"),
  fragDesc: requireClass(styles.fragDesc, "taskcardmockups.module.css", "fragDesc"),
  inset: requireClass(styles.inset, "taskcardmockups.module.css", "inset"),
  stripHead: requireClass(styles.stripHead, "taskcardmockups.module.css", "stripHead"),
  strip: requireClass(styles.strip, "taskcardmockups.module.css", "strip"),
  seg: requireClass(styles.seg, "taskcardmockups.module.css", "seg"),
  segDone: requireClass(styles.segDone, "taskcardmockups.module.css", "segDone"),
  segNow: requireClass(styles.segNow, "taskcardmockups.module.css", "segNow"),
  stripLabel: requireClass(styles.stripLabel, "taskcardmockups.module.css", "stripLabel"),
};

const P = {
  card: requireClass(prodCard.card, "taskcard.module.css", "card"),
  head: requireClass(prodCard.head, "taskcard.module.css", "head"),
  rows: requireClass(prodCard.rows, "taskcard.module.css", "rows"),
  row: requireClass(prodCard.row, "taskcard.module.css", "row"),
  rowText: requireClass(prodCard.rowText, "taskcard.module.css", "rowText"),
  descStruck: requireClass(prodCard.descStruck, "taskcard.module.css", "descStruck"),
  desc: requireClass(prodCard.desc, "taskcard.module.css", "desc"),
  note: requireClass(prodCard.note, "taskcard.module.css", "note"),
  progress: requireClass(prodCard.progress, "taskcard.module.css", "progress"),
};

// ---- shared derivation -------------------------------------------------

function asObjectArray(value: unknown): Record<string, unknown>[] {
  if (!Array.isArray(value)) return [];
  return value.filter((v): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v));
}

const TOUCH_BY_STATUS: Record<string, TaskTouch> = {
  done: "done",
  cancelled: "cancelled",
  in_progress: "started",
};

// The text mark the folded summary line carries per touch: the checkbox
// grammar in text form, matching the current card's touch summary marks
// except "started", whose arrow reads better than a hollow box.
const MARK: Record<TaskTouch, string> = { added: "☐", done: "☑", cancelled: "☒", started: "→" };

interface MutationRow {
  touch: TaskTouch;
  label: string;
  note?: string;
}

// A minimal re-derivation of taskCard.tsx's own mutationRows (module-private
// there; the production rework will own one shared derivation).
function mutationRows(item: ItemModel): MutationRow[] {
  const args = parseArgs(item.argumentsJSON);
  const action = str(args, "action") ?? "";
  const state = parseTaskState(item.raw);
  const rows: MutationRow[] = [];
  const touched = new Set<number>();
  let completedAny = false;
  const pushUpdate = (update: Record<string, unknown>) => {
    const touch = TOUCH_BY_STATUS[str(update, "status") ?? ""];
    if (!touch) return;
    const id = typeof update.id === "number" ? update.id : undefined;
    if (id !== undefined) touched.add(id);
    // Same reassertion guard as the production card: a false "started"
    // marker is a note-carrying restatement, not a fresh start.
    if (touch === "started" && state?.find((task) => task.id === id)?.started === false) return;
    if (touch === "done" || touch === "cancelled") completedAny = true;
    rows.push({ touch, label: taskLabel(state, id), note: str(update, "notes") || undefined });
  };
  if (action === "append") {
    for (const task of asObjectArray(args.tasks)) {
      rows.push({ touch: "added", label: str(task, "description") ?? "(untitled task)" });
    }
    return rows;
  }
  if (action === "") {
    for (const task of asObjectArray(args.add)) {
      rows.push({ touch: "added", label: str(task, "description") ?? "(untitled task)" });
    }
  }
  const updates = action === "update" ? asObjectArray(args.updates) : action === "" ? asObjectArray(args.update) : [];
  for (const update of updates) pushUpdate(update);
  const started = autoStartedTask(state, touched, completedAny);
  if (started) rows.push({ touch: "started", label: taskLabel(state, started.id) });
  return rows;
}

// The daemon's own Progress footer, parsed back out of the output text the
// same way taskCard.tsx does (its parseProgress is module-private; the mock
// fixtures all emit the current outcome form, so the legacy form is omitted).
const OUTCOME_PROGRESS_RE = /Progress:\s*(\d+)\s+done,\s*(\d+)\s+cancelled,\s*(\d+)\s+remaining\s*\((\d+)\s+total\)/g;

interface Progress {
  done: number;
  cancelled: number;
  remaining: number;
  total: number;
}

function parseProgress(output: string | undefined): Progress | undefined {
  if (!output) return undefined;
  const matches = [...output.matchAll(new RegExp(OUTCOME_PROGRESS_RE.source, OUTCOME_PROGRESS_RE.flags))];
  const match = matches.at(-1);
  return match
    ? { done: Number(match[1]), cancelled: Number(match[2]), remaining: Number(match[3]), total: Number(match[4]) }
    : undefined;
}

// The three-task window: most recently settled (done or cancelled - a
// cancellation is the plan's most recent "finished" event and belongs in the
// first slot rather than vanishing), the in-progress task, and the first
// open task in list order. Timestamps order the settled slot; a snapshot
// without any falls back to list order, the same degradation the production
// card gives a raw-less replay.
interface TaskWindow {
  settled?: TaskRow;
  current?: TaskRow;
  next?: TaskRow;
}

function stateWindow(state: TaskRow[] | null): TaskWindow {
  if (!state) return {};
  const settledAll = state.filter((task) => task.status === "done" || task.status === "cancelled");
  const settled =
    [...settledAll].sort((a, b) =>
      (b.completedAt ?? b.updatedAt ?? "").localeCompare(a.completedAt ?? a.updatedAt ?? ""),
    )[0] ?? settledAll.at(-1);
  return {
    settled,
    current: state.find((task) => task.status === "in_progress"),
    next: state.find((task) => task.status === "open"),
  };
}

// The notes THIS call added, keyed by task id - the only notes any variant
// may render. Update-shaped calls carry them per update; append-shaped calls
// mint no client-visible ids, so a fresh note on an appended task is a
// non-case this mock deliberately leaves unrendered.
function freshNotes(item: ItemModel): ReadonlyMap<number, string> {
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

// The proposed "pending" glyph: the TaskCheck grammar's square box with no
// inner mark, for the window's next slot (TaskCheck's marks are per-TOUCH -
// added/done/cancelled/started - and "not started yet" is a state, not a
// touch). Same 16x16 stroke grammar as TaskCheck so the family holds.
function PendingGlyph() {
  return (
    <svg
      viewBox="0 0 16 16"
      width={16}
      height={16}
      aria-hidden="true"
      focusable="false"
      className={C.pending}
      data-testid="task-pending"
      style={{ display: "block" }}
    >
      <path
        d="M2.5 2.5 H13.5 V13.5 H2.5 Z"
        stroke="currentColor"
        strokeWidth="1.75"
        strokeLinecap="round"
        strokeLinejoin="round"
        fill="none"
      />
    </svg>
  );
}

type SlotKind = "settled" | "current" | "next";

function glyphFor(task: TaskRow, kind: SlotKind): ReactNode {
  if (kind === "next") return <PendingGlyph />;
  if (kind === "current") return <TaskCheck touch="started" />;
  return <TaskCheck touch={task.status === "done" ? "done" : "cancelled"} />;
}

function descClassFor(kind: SlotKind): string {
  if (kind === "current") return C.descNow;
  if (kind === "next") return C.descNext;
  return C.descStruck;
}

// ---- the aggregate footer ----------------------------------------------

function ProgressFoot({ progress }: { progress: Progress | undefined }) {
  if (!progress) return null;
  const label = taskAggregateLabel(progress);
  return (
    <div className={C.foot} data-testid="mock-task-foot">
      <span className={C.footLabel}>{label}</span>
      <span className={C.footMeter}>
        <Meter
          label={`Task progress: ${label}`}
          value={progress.done + progress.cancelled}
          max={progress.total}
          tone="neutral"
        />
      </span>
    </div>
  );
}

// ---- shared window row (variants A and E) ------------------------------

function WindowRow({
  task,
  kind,
  note,
  noteStyle,
  spine,
}: {
  task: TaskRow;
  kind: SlotKind;
  note?: string;
  noteStyle: "serif" | "sans";
  spine: boolean;
}) {
  return (
    <div className={spine ? `${C.winRow} ${C.spined}` : C.winRow} data-kind={kind}>
      {glyphFor(task, kind)}
      <span className={C.winRowText}>
        <span className={descClassFor(kind)}>{task.description}</span>
        {note && <span className={noteStyle === "serif" ? C.noteSerif : C.noteSans}>{note}</span>}
      </span>
    </div>
  );
}

// ---- variant A: the ladder --------------------------------------------

// A vertical progression: settled, working, next, joined by a hairline
// spine through the glyph column. The fresh note sets in the prose face -
// the note is the agent's own sentence, and this variant lets it read like
// one. The aggregate sentence and meter sit in a footer.
function LadderBody({ item }: ToolRenderProps) {
  const win = stateWindow(parseTaskState(item.raw));
  const fresh = freshNotes(item);
  return (
    <div className={C.card} data-testid="mock-taskcard-a">
      <div className={C.window}>
        {win.settled && (
          <WindowRow task={win.settled} kind="settled" note={fresh.get(win.settled.id)} noteStyle="serif" spine />
        )}
        {win.current && (
          <WindowRow task={win.current} kind="current" note={fresh.get(win.current.id)} noteStyle="serif" spine />
        )}
        {win.next && <WindowRow task={win.next} kind="next" note={fresh.get(win.next.id)} noteStyle="sans" spine />}
      </div>
      <ProgressFoot progress={parseProgress(item.output)} />
    </div>
  );
}

// ---- variant B: the pipeline ------------------------------------------

// The whole window in one wrapping line: settled -> working -> next,
// arrow-separated, each fragment clamped to a single line. The order is
// the arrows' own semantics; the cost is truncation, shown honestly.
function PipelineFrag({ task, kind, note }: { task: TaskRow; kind: SlotKind; note?: string }) {
  return (
    <span className={kind === "current" ? `${C.frag} ${C.fragNow}` : C.frag}>
      {glyphFor(task, kind)}
      <span className={C.fragText}>
        <span className={`${C.fragDesc} ${descClassFor(kind)}`}>{task.description}</span>
        {note && <span className={C.noteSans}>{note}</span>}
      </span>
    </span>
  );
}

function PipelineBody({ item }: ToolRenderProps) {
  const win = stateWindow(parseTaskState(item.raw));
  const fresh = freshNotes(item);
  return (
    <div className={C.card} data-testid="mock-taskcard-b">
      <div className={C.pipe}>
        {win.settled && <PipelineFrag task={win.settled} kind="settled" note={fresh.get(win.settled.id)} />}
        {win.settled && (win.current || win.next) && (
          <span className={C.pipeArrow} aria-hidden="true">
            →
          </span>
        )}
        {win.current && <PipelineFrag task={win.current} kind="current" note={fresh.get(win.current.id)} />}
        {win.current && win.next && (
          <span className={C.pipeArrow} aria-hidden="true">
            →
          </span>
        )}
        {win.next && <PipelineFrag task={win.next} kind="next" note={fresh.get(win.next.id)} />}
      </div>
      <ProgressFoot progress={parseProgress(item.output)} />
    </div>
  );
}

// ---- variant C: the quiet ledger --------------------------------------

// The minimal-delta option: today's card chrome (head with the aggregate
// sentence + meter, then rows) with the changelog swapped for the window.
// Rendered inside the production taskcard.module.css classes unchanged, so
// the diff against today is exactly the rows' content.
function LedgerRow({ task, kind, note }: { task: TaskRow; kind: SlotKind; note?: string }) {
  return (
    <div className={P.row} data-kind={kind}>
      {glyphFor(task, kind)}
      <div className={P.rowText}>
        <span className={kind === "settled" ? P.descStruck : P.desc}>{task.description}</span>
        {note && <span className={P.note}>Notes: {note}</span>}
      </div>
    </div>
  );
}

function LedgerBody({ item }: ToolRenderProps) {
  const win = stateWindow(parseTaskState(item.raw));
  const fresh = freshNotes(item);
  const progress = parseProgress(item.output);
  const label = taskAggregateLabel(progress ?? { total: 0, done: 0 });
  return (
    <div className={P.card} data-testid="mock-taskcard-c">
      {progress && (
        <div className={P.head}>
          <span className={P.progress}>{label}</span>
          <Meter
            label={`Task progress: ${label}`}
            value={progress.done + progress.cancelled}
            max={progress.total}
            tone="neutral"
          />
        </div>
      )}
      <div className={P.rows}>
        {win.settled && <LedgerRow task={win.settled} kind="settled" note={fresh.get(win.settled.id)} />}
        {win.current && <LedgerRow task={win.current} kind="current" note={fresh.get(win.current.id)} />}
        {win.next && <LedgerRow task={win.next} kind="next" note={fresh.get(win.next.id)} />}
      </div>
    </div>
  );
}

// ---- variant D: the now inset -----------------------------------------

// The in-progress task inside an evidence-inset box - the transcript's own
// "here is the thing you should look at" grammar - with the settled task
// above and the next one below, so "you are here" is the middle of a
// sandwich rather than one flat list.
function InsetBody({ item }: ToolRenderProps) {
  const win = stateWindow(parseTaskState(item.raw));
  const fresh = freshNotes(item);
  return (
    <div className={C.card} data-testid="mock-taskcard-d">
      {win.settled && (
        <div className={C.winRow}>
          {glyphFor(win.settled, "settled")}
          <span className={C.winRowText}>
            <span className={C.descStruck}>{win.settled.description}</span>
            {fresh.get(win.settled.id) && <span className={C.noteSerif}>{fresh.get(win.settled.id)}</span>}
          </span>
        </div>
      )}
      {win.current && (
        <div className={C.inset}>
          <div className={C.winRow}>
            {glyphFor(win.current, "current")}
            <span className={C.winRowText}>
              <span className={C.descNow}>{win.current.description}</span>
              {fresh.get(win.current.id) && <span className={C.noteSerif}>{fresh.get(win.current.id)}</span>}
            </span>
          </div>
        </div>
      )}
      {win.next && (
        <div className={C.winRow}>
          {glyphFor(win.next, "next")}
          <span className={C.winRowText}>
            <span className={C.descNext}>{win.next.description}</span>
          </span>
        </div>
      )}
      <ProgressFoot progress={parseProgress(item.output)} />
    </div>
  );
}

// ---- variant E: the segment strip -------------------------------------

// Progress first: one segment per task (settled fills quiet ink, working
// carries --alive, open stays sunken), the aggregate sentence beneath it,
// then the window rows - the ladder without its spine, letting the strip
// carry the "where are we" load.
function StripBody({ item }: ToolRenderProps) {
  const state = parseTaskState(item.raw);
  const win = stateWindow(state);
  const fresh = freshNotes(item);
  const progress = parseProgress(item.output);
  const label = progress ? taskAggregateLabel(progress) : "";
  return (
    <div className={C.card} data-testid="mock-taskcard-e">
      {progress && (
        <div className={C.stripHead}>
          {/* biome-ignore lint/a11y/useSemanticElements: div+role mirrors the Meter widget's own deliberate themed-gauge escape hatch (see widgets/meter) */}
          <div
            className={C.strip}
            role="meter"
            aria-label={`Task progress: ${label}`}
            aria-valuenow={progress.done + progress.cancelled}
            aria-valuemin={0}
            aria-valuemax={progress.total}
          >
            {(state ?? []).map((task) => (
              <span
                key={task.id}
                className={task.status === "in_progress" ? C.segNow : task.status === "open" ? C.seg : C.segDone}
              />
            ))}
          </div>
          <span className={C.stripLabel}>{label}</span>
        </div>
      )}
      <div className={C.window}>
        {win.settled && (
          <WindowRow
            task={win.settled}
            kind="settled"
            note={fresh.get(win.settled.id)}
            noteStyle="sans"
            spine={false}
          />
        )}
        {win.current && (
          <WindowRow
            task={win.current}
            kind="current"
            note={fresh.get(win.current.id)}
            noteStyle="sans"
            spine={false}
          />
        )}
        {win.next && (
          <WindowRow task={win.next} kind="next" note={fresh.get(win.next.id)} noteStyle="sans" spine={false} />
        )}
      </div>
    </div>
  );
}

// ---- registration -----------------------------------------------------

// The folded line, strict form: the single most recent touch of this
// mutation, with its text mark. For the main fixture the most recent touch
// is the daemon's auto-start, so the folded line names what the agent is
// now working on - the completion that caused it stays in the open body.
function latestOnlySummary(item: ItemModel): string {
  const last = mutationRows(item).at(-1);
  return last ? `${MARK[last.touch]} ${last.label}` : "";
}

// The folded line, state form (variant B's voice): the same most-recent
// update - the auto-start - phrased as where the agent is now, with the
// strict mark form as the fallback when nothing is in progress.
function nowFirstSummary(item: ItemModel): string {
  const current = stateWindow(parseTaskState(item.raw)).current;
  return current ? `Now: ${current.description}` : latestOnlySummary(item);
}

// The harness drives folded vs open per ITEM: fixture ids containing
// "folded" settle collapsed (the mock's whole point), everything else opens
// at settle exactly like today's card does.
function autoOpenUnlessFolded(item: ItemModel): boolean {
  return !item.id.includes("folded");
}

registerToolRenderer({
  match: "mock_taskcard_a",
  icon: "tasks",
  fold: "never",
  summary: latestOnlySummary,
  summaryWhenExpanded: "Updated the task list",
  body: LadderBody,
  autoExpand: autoOpenUnlessFolded,
});

registerToolRenderer({
  match: "mock_taskcard_b",
  icon: "tasks",
  fold: "never",
  summary: nowFirstSummary,
  summaryWhenExpanded: "Updated the task list",
  body: PipelineBody,
  autoExpand: autoOpenUnlessFolded,
});

registerToolRenderer({
  match: "mock_taskcard_c",
  icon: "tasks",
  fold: "never",
  summary: latestOnlySummary,
  summaryWhenExpanded: "Updated the task list",
  body: LedgerBody,
  autoExpand: autoOpenUnlessFolded,
});

registerToolRenderer({
  match: "mock_taskcard_d",
  icon: "tasks",
  fold: "never",
  summary: latestOnlySummary,
  summaryWhenExpanded: "Updated the task list",
  body: InsetBody,
  autoExpand: autoOpenUnlessFolded,
});

registerToolRenderer({
  match: "mock_taskcard_e",
  icon: "tasks",
  fold: "never",
  summary: latestOnlySummary,
  summaryWhenExpanded: "Updated the task list",
  body: StripBody,
  autoExpand: autoOpenUnlessFolded,
});
