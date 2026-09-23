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
// chosen variant is now the production card, so every derivation below
// (mutation rows, the window, fresh notes, progress) is the production
// card's own, imported from taskCard.tsx - only the presentation is each
// variant's proposal.

import type { ItemModel, TaskRow } from "@evener/appwire-client";
import { taskAggregateLabel } from "@evener/appwire-client";
import type { ComponentType, ReactNode } from "react";
import type { ToolRenderProps } from "../../panes/session/transcript/toolRenderers";
import { registerToolRenderer } from "../../panes/session/transcript/toolRenderers";
import {
  freshNotes,
  mutationRows,
  type Progress,
  parseProgress,
  SUMMARY_MARK,
  stateWindow,
} from "../../panes/session/transcript/tools/taskCard";
import { STATUS_TOUCH, TaskCheck } from "../../panes/session/transcript/tools/taskCheck";
// The conservative variant renders inside today's card chrome unchanged.
import prodCard from "../../panes/session/transcript/tools/taskcard.module.css";
import { parseTaskState } from "../../panes/session/transcript/tools/taskData";
import { Meter } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./taskcardmockups.module.css";

const C = {
  card: requireClass(styles.card, "taskcardmockups.module.css", "card"),
  window: requireClass(styles.window, "taskcardmockups.module.css", "window"),
  winRow: requireClass(styles.winRow, "taskcardmockups.module.css", "winRow"),
  winRowText: requireClass(styles.winRowText, "taskcardmockups.module.css", "winRowText"),
  noteSans: requireClass(styles.noteSans, "taskcardmockups.module.css", "noteSans"),
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
  descNow: requireClass(prodCard.descNow, "taskcard.module.css", "descNow"),
  descNext: requireClass(prodCard.descNext, "taskcard.module.css", "descNext"),
  desc: requireClass(prodCard.desc, "taskcard.module.css", "desc"),
  note: requireClass(prodCard.note, "taskcard.module.css", "note"),
  progress: requireClass(prodCard.progress, "taskcard.module.css", "progress"),
  spined: requireClass(prodCard.spined, "taskcard.module.css", "spined"),
};

type SlotKind = "settled" | "current" | "next";

// The window slot's glyph keys off the task's state through the same map the
// pane and the production card use - one box grammar across every surface.
function glyphFor(task: TaskRow): ReactNode {
  return <TaskCheck touch={STATUS_TOUCH[task.status]} />;
}

// The label ink per slot: settled reads struck, the working task carries the
// one semibold line, the next task stays quiet - the production card's own
// classes, so a token tweak to the shipped window reaches these variants too.
function descClassFor(kind: SlotKind): string {
  if (kind === "current") return P.descNow;
  if (kind === "next") return P.descNext;
  return P.descStruck;
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
          value={progress.done + (progress.cancelled ?? 0)}
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
    <div className={spine ? `${C.winRow} ${P.spined}` : C.winRow} data-kind={kind}>
      {glyphFor(task)}
      <span className={C.winRowText}>
        <span className={descClassFor(kind)}>{task.description}</span>
        {note && <span className={noteStyle === "serif" ? P.note : C.noteSans}>{note}</span>}
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
      {glyphFor(task)}
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
      {glyphFor(task)}
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
            value={progress.done + (progress.cancelled ?? 0)}
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
          {glyphFor(win.settled)}
          <span className={C.winRowText}>
            <span className={P.descStruck}>{win.settled.description}</span>
            {fresh.get(win.settled.id) && <span className={P.note}>{fresh.get(win.settled.id)}</span>}
          </span>
        </div>
      )}
      {win.current && (
        <div className={C.inset}>
          <div className={C.winRow}>
            {glyphFor(win.current)}
            <span className={C.winRowText}>
              <span className={P.descNow}>{win.current.description}</span>
              {fresh.get(win.current.id) && <span className={P.note}>{fresh.get(win.current.id)}</span>}
            </span>
          </div>
        </div>
      )}
      {win.next && (
        <div className={C.winRow}>
          {glyphFor(win.next)}
          <span className={C.winRowText}>
            <span className={P.descNext}>{win.next.description}</span>
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
            aria-valuenow={progress.done + (progress.cancelled ?? 0)}
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
  const last = mutationRows(item)?.at(-1);
  return last ? `${SUMMARY_MARK[last.touch]} ${last.label}` : "";
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

// One registration per variant; the summary voice and the body are the only
// per-variant fields, so the rest is shared.
const VARIANTS: { match: string; summary: (item: ItemModel) => string; body: ComponentType<ToolRenderProps> }[] = [
  { match: "mock_taskcard_a", summary: latestOnlySummary, body: LadderBody },
  { match: "mock_taskcard_b", summary: nowFirstSummary, body: PipelineBody },
  { match: "mock_taskcard_c", summary: latestOnlySummary, body: LedgerBody },
  { match: "mock_taskcard_d", summary: latestOnlySummary, body: InsetBody },
  { match: "mock_taskcard_e", summary: latestOnlySummary, body: StripBody },
];

for (const variant of VARIANTS) {
  registerToolRenderer({
    match: variant.match,
    icon: "tasks",
    fold: "never",
    summary: variant.summary,
    summaryWhenExpanded: "Updated the task list",
    body: variant.body,
    autoExpand: autoOpenUnlessFolded,
  });
}
