// Folding a run of finished, uneventful tool calls into one row (critique
// R9, principle 2 / decisions.md topic 06 Alt A): once a turn has settled,
// the reader does not need four separate rows saying a file was read - they
// need one row naming what the run amounted to, which opens to the rows when
// they do.
//
// Three rules keep the fold honest, and every one of them is about not
// hiding something the reader would want:
//
//   - a LIVE turn never folds. While the agent is still working, each call
//     appearing is the progress signal;
//   - anything that is not a plain, completed, unremarkable call BREAKS the
//     run and stays visible on its own: a failure, a call still in flight, a
//     descriptor that auto-expands, a `fold: "never"` card (a delegate, an
//     ask, a task list), any descriptor that has not opted in at all (an
//     unregistered or MCP tool may have side effects the reader must see,
//     so only `fold: "quiet"` and `fold: "consequential"` join a run), and
//     any non-tool entry (prose, reasoning, a system notice). A break
//     flushes the run rather than spanning it, so a folded row can never
//     gather calls the reader saw separated by an answer;
//   - a run has to be worth folding. Two rows are not clutter; three are.
import type { ProjectedEntry, ProjectedTurn } from "../../../transcriptDisplay/projector";
import { type ToolRendererDescriptor, type ToolSummaryContext, toolRendererFor } from "./toolRenderers";

export type ToolItemEntry = Extract<ProjectedEntry, { kind: "item" }>;

export interface ToolRun {
  kind: "run";
  /** Derived from the first folded entry, so the run's disclosure state is
   * stable across re-renders of the same turn. */
  id: string;
  entries: ToolItemEntry[];
}

export interface FoldOptions {
  /** False while the turn is still running - see the header. */
  turnSettled: boolean;
  descriptorFor: (toolName: string) => Pick<ToolRendererDescriptor, "summary" | "fold" | "failed" | "autoExpand">;
}

// Two folded rows save one row and cost a click; three start to read as
// clutter. The threshold is the whole reason a pair stays expanded.
const MIN_RUN = 3;

function foldable(entry: ProjectedEntry, opts: FoldOptions): entry is ToolItemEntry {
  if (entry.kind !== "item" || entry.item.type !== "commandExecution") return false;
  const { item } = entry;
  if (item.status !== "completed") return false;
  if (item.error !== undefined && item.error !== "") return false;
  const descriptor = opts.descriptorFor(item.toolName ?? "");
  if (descriptor.fold !== "quiet" && descriptor.fold !== "consequential") return false;
  if (descriptor.failed?.(item) || descriptor.autoExpand?.(item)) return false;
  return true;
}

export function foldToolRuns(entries: readonly ProjectedEntry[], opts: FoldOptions): (ProjectedEntry | ToolRun)[] {
  if (!opts.turnSettled) return [...entries];
  const out: (ProjectedEntry | ToolRun)[] = [];
  let run: ToolItemEntry[] = [];
  const flush = () => {
    const first = run[0];
    if (first && run.length >= MIN_RUN) out.push({ kind: "run", id: `run:${first.id}`, entries: run });
    else out.push(...run);
    run = [];
  };
  for (const entry of entries) {
    if (foldable(entry, opts)) {
      run.push(entry);
      continue;
    }
    flush();
    out.push(entry);
  }
  flush();
  return out;
}

// The summary a folded run wears: how many steps, and what the run amounted
// to. The named step is the LAST consequential one (a mutation - an edit, a
// shell command, a worktree change), because that is what the run did; with
// none, the last call, because that is where the run ended up.
// ctx is the same ToolSummaryContext an expanded row hands its descriptor
// (ToolCallItem passes the thread's cwd), so a shell step's label drops the
// redundant "cd <cwd> && " prefix exactly as the row beneath it does.
export function runLabel(run: ToolRun, descriptorFor: FoldOptions["descriptorFor"], ctx?: ToolSummaryContext): string {
  const newestFirst = [...run.entries].reverse();
  const named =
    newestFirst.find((entry) => descriptorFor(entry.item.toolName ?? "").fold === "consequential") ?? newestFirst[0];
  const steps = `${run.entries.length} steps`;
  // foldToolRuns never mints a run below MIN_RUN, so there is always a step
  // to name; the bare count keeps the row honest rather than throwing if a
  // future caller ever hands one over empty.
  if (!named) return steps;
  return `${steps} · ${descriptorFor(named.item.toolName ?? "").summary(named.item, ctx)}`;
}

/**
 * The one fold every consumer of a turn's entries must agree on. TurnBlock
 * renders from it and TranscriptBody registers scroll/focus anchors from it,
 * so a folded run is ONE anchor in both places (its id, the first entry's
 * source index) and never a set of entry ids that no rendered element carries
 * while the run is closed. "inProgress" is TurnModel.status's live literal
 * (the projector reads the same one).
 */
export function foldTurnEntries(turn: ProjectedTurn): (ProjectedEntry | ToolRun)[] {
  return foldToolRuns(turn.entries, {
    turnSettled: turn.source.status !== "inProgress",
    descriptorFor: toolRendererFor,
  });
}
