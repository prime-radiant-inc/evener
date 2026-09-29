// What a manage_worktree step says, in both clients. The operations differ
// in consequence (a listing reads, a removal with force_dirty throws away
// uncommitted work), so the line leads with the operation and what it acted
// on, and says when uncommitted changes were discarded.
//
// Every operation returns a map with a `status` string
// (agent/session_tools_worktree.go), which the registry marshals to JSON, and
// the settled result is preferred over the arguments wherever they disagree:
// a `switch` that turned out to be a no-op reads "Already in", not "Switched
// to", and an `already_disposed` dispose never claims a dirty-discard,
// because nothing was torn down to discard.

import type { ItemModel } from "./model";
import { parseArgs, str } from "./toolCallText";
import { toolJSONResult } from "./toolEvidence";

/** The parts of a manage_worktree step its words read. */
export type WorktreeStep = Pick<ItemModel, "argumentsJSON" | "output">;

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
interface WorktreeCallArgs {
  operation: string;
  forceDirty: boolean;
}

function parseWorktreeCallArgs(argumentsJSON: string | undefined): WorktreeCallArgs {
  const args = parseArgs(argumentsJSON);
  return { operation: str(args, "operation") ?? "", forceDirty: args.force_dirty === true };
}

function countOf(result: Record<string, unknown> | undefined, key: string): number | undefined {
  const value = result?.[key];
  return Array.isArray(value) ? value.length : undefined;
}

/** "Created worktree settle-fix (from main)", "Already in worktree settle-fix". */
export function worktreeSummary(item: WorktreeStep): string {
  const args = parseArgs(item.argumentsJSON);
  const { operation, forceDirty } = parseWorktreeCallArgs(item.argumentsJSON);
  const result = toolJSONResult(item.output);
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
    case "adopt": {
      // The result names the managed worktree the adopted path became.
      const name = (result ? str(result, "name") : undefined) ?? str(args, "path") ?? "";
      return `Adopted worktree ${name}`;
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
      // it was, in words, never as the bare tool name.
      return `Used manage worktree: ${operation}`;
  }
}

/** What the operation says it did, in its own words (its result's
 * `message`); undefined for output that isn't the tool's JSON. */
export function worktreeMessage(output: string | undefined): string | undefined {
  const result = toolJSONResult(output);
  return result ? str(result, "message") : undefined;
}

/** What a running manage_worktree step is doing: "Creating worktree settle-fix". */
export function worktreeProgress(item: Pick<WorktreeStep, "argumentsJSON">): string {
  const args = parseArgs(item.argumentsJSON);
  const { operation } = parseWorktreeCallArgs(item.argumentsJSON);
  const target = str(args, "name") ?? str(args, "path") ?? "";
  switch (operation) {
    case "create":
      return `Creating worktree ${target}`;
    case "list":
      return "Listing worktrees";
    case "switch":
      return `Switching to worktree ${target}`;
    case "exit":
      return "Leaving the worktree";
    case "remove":
      return `Removing worktree ${target}`;
    case "prune":
      return "Pruning worktrees";
    case "adopt":
      return `Adopting worktree ${target}`;
    case "dispose":
      return `Disposing ${str(args, "id") ?? ""}`;
    default:
      return `Using manage worktree: ${operation}`;
  }
}
