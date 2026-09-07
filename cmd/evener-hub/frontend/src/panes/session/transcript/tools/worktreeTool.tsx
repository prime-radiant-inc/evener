// Descriptors for manage_worktree and find_session_transcripts — the last two
// tools the agent runtime can emit that had no descriptor of their own and so
// fell through to toolRenderers.ts's DEFAULT_DESCRIPTOR, whose summary is
// `item.toolName`. The whole transcript row read `manage_worktree`.
//
// That is worse for this tool than for most. manage_worktree carries seven
// operations (create/list/switch/exit/remove/prune/dispose) plus `force` and
// `force_dirty`, and the Go docs describe force_dirty as overriding "the
// refusal to discard uncommitted changes" — so a read-only listing and a
// removal that throws away someone's work rendered as the same single word,
// distinguishable only by expanding the row. The row has to lead with the
// consequential step.
//
// Ground truth, verified against agent/session_tools_worktree.go: every
// operation returns a map with a `status` string, and the statuses are
// created/listed/switched/unchanged/exited/removed/pruned/already_disposed.
// Because Exec returns a plain map (not a tool.StateResult), the registry's
// toolValueToString json.MarshalIndents it, so item.output here is real
// parseable JSON — same situation web_fetch is in, and unlike the
// human-formatted text most tools in this directory return.
//
// The settled result is preferred over the arguments wherever it disagrees:
// a `switch` that turned out to be a no-op reads "Already in", not
// "Switched to", and an `already_disposed` dispose never claims a
// dirty-discard, because nothing was torn down to discard.
import { MCPToolArguments } from "../MCPToolArguments";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import { HeadClippedOutputBody } from "./bodies";
import { clip, parseArgs, parseJSONObject, str } from "./helpers";

// Only force_dirty earns the phrase. A plain `force` overrides merge-safety
// gating (an unmerged branch, an unmanaged sidecar) and explicitly "does NOT
// discard uncommitted changes" per the tool's own parameter description —
// claiming otherwise on the row would be the same dishonesty in the other
// direction.
const DISCARD_NOTE = " · discarded uncommitted changes";

// Parsed core of a manage_worktree call's arguments: which operation it
// requested, and whether force_dirty was set. This is deliberately narrower
// than the full args object worktreeSummary below needs (it also reads
// name/path/base_ref for display text) - it's exactly the "read-vs-mutate"
// shape a caller that only cares about consequence, not display, needs.
// Exported so callers that need the "read-vs-mutate" shape can reuse this
// instead of re-deriving the same two fields from item.argumentsJSON itself.
export interface WorktreeCallArgs {
  operation: string;
  forceDirty: boolean;
}

export function parseWorktreeCallArgs(argumentsJSON: string | undefined): WorktreeCallArgs {
  const args = parseArgs(argumentsJSON);
  return { operation: str(args, "operation") ?? "", forceDirty: args.force_dirty === true };
}

function countOf(result: Record<string, unknown> | undefined, key: string): number | undefined {
  const value = result?.[key];
  return Array.isArray(value) ? value.length : undefined;
}

function worktreeSummary(item: { argumentsJSON?: string; output?: string }): string {
  const args = parseArgs(item.argumentsJSON);
  const { operation, forceDirty } = parseWorktreeCallArgs(item.argumentsJSON);
  const result = parseJSONObject(item.output);
  const status = result ? str(result, "status") : undefined;
  // `name` is the handle for create/remove/switch; `path` is switch's other
  // accepted form (the schema takes exactly one of the two).
  const target = str(args, "name") ?? str(args, "path") ?? "";
  const dirty = forceDirty ? DISCARD_NOTE : "";

  switch (operation) {
    case "create": {
      const base = str(args, "base_ref");
      return `Created worktree ${target}${base ? ` (from ${base})` : ""}`;
    }
    case "list": {
      const found = countOf(result, "entries");
      return `Listed worktrees${found === undefined ? "" : ` · ${found} found`}`;
    }
    case "switch":
      // The daemon reports `unchanged` when the session was already there.
      return status === "unchanged" ? `Already in worktree ${target}` : `Switched to worktree ${target}`;
    case "exit": {
      const left = result ? str(result, "left_path") : undefined;
      return `Exited worktree${left ? ` at ${left}` : ""}`;
    }
    case "remove":
      return `Removed worktree ${target}${dirty}`;
    case "prune": {
      const removed = countOf(result, "removed");
      const skipped = countOf(result, "skipped");
      if (removed === undefined && skipped === undefined) return "Pruned worktrees";
      return `Pruned worktrees · ${removed ?? 0} removed, ${skipped ?? 0} skipped`;
    }
    case "dispose": {
      const id = str(args, "id") ?? "";
      // Idempotent no-op: the lane was already gone, so no work was discarded
      // however the call was flagged.
      if (status === "already_disposed") return `Already disposed ${id}`;
      return `Disposed ${id}${dirty}`;
    }
    default:
      // A future operation this build has never heard of still says which one
      // it was, rather than collapsing back to the bare tool name.
      return `manage_worktree: ${operation}`;
  }
}

registerToolRenderer({
  match: "manage_worktree",
  summary: worktreeSummary,
  fold: "consequential", // creates/switches/removes a tree: a mutation
  // The output really is parseable JSON here (see this file's header), but
  // whether each operation deserves its own structured body is a bigger
  // question than the row this fix is about - a head-clipped dump is honest
  // and complete in the meantime.
  body: HeadClippedOutputBody,
});

// find_session_transcripts is read-only, so its row carries far less risk —
// but a bare tool name told a reader nothing about what was searched for
// either. Its Exec returns human-formatted TEXT (not JSON), ending in either
// "N match (scope: …)" or "No matching sessions (scope: …)", which is where
// the count comes from.
//
// `children_of` leads when both are present, matching the tool's own
// precedence: asking for one session's children is a different question from
// a text search, and the parent ref is the more specific answer.
const MATCH_COUNT = /(\d+)\s+match/;
const NO_MATCHES = /No matching sessions/;

function findSessionsCount(output: string | undefined): number | undefined {
  if (output === undefined || output === "") return undefined;
  if (NO_MATCHES.test(output)) return 0;
  const found = output.match(MATCH_COUNT);
  return found?.[1] === undefined ? undefined : Number(found[1]);
}

function findSessionsSummary(item: { argumentsJSON?: string; output?: string }): string {
  const args = parseArgs(item.argumentsJSON);
  const childrenOf = str(args, "children_of");
  const query = str(args, "query");

  let lead: string;
  let noun: string;
  if (childrenOf !== undefined && childrenOf !== "") {
    lead = `Searched sessions spawned by ${childrenOf}`;
    noun = "matches";
  } else if (query !== undefined && query !== "") {
    lead = `Searched sessions for "${clip(query, 60)}"`;
    noun = "matches";
  } else {
    // No query and no parent: the tool's plain catalog listing, which reports
    // sessions rather than matches — there was nothing to match against.
    lead = "Listed recent sessions";
    noun = "sessions";
  }

  const count = findSessionsCount(item.output);
  return count === undefined ? lead : `${lead} · ${count} ${noun}`;
}

// The row's body composes the request arguments above the head-clipped
// output (matching defaultToolBody's own arrangement in toolRenderers.ts) so
// "what was searched for" is answerable from the expanded card, not just the
// summary line above it.
function FindSessionTranscriptsBody(props: ToolRenderProps) {
  return (
    <>
      <MCPToolArguments {...props} />
      <HeadClippedOutputBody {...props} />
    </>
  );
}

registerToolRenderer({
  match: "find_session_transcripts",
  // A read-only search: folds and only counts (toolFoldPolicy.test.ts pins
  // every registered tool's policy).
  fold: "quiet",
  summary: findSessionsSummary,
  body: FindSessionTranscriptsBody,
});
