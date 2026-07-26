// Consequence ranking for a single tool call - kata bc16. Tool-call
// clustering (kata d6fp) needs to name the ONE step that led a collapsed run
// ("edit cache.go" leads, "read cache.go" does not - mockup 06 Alt A), and no
// read-vs-mutate classification of a tool call existed anywhere in this repo
// before this file: agent/internal/tool/definitions.go has no such marker,
// and agent/transcript_render.go's per-tool switch is an argument formatter,
// not a classification. agent/internal/tool/definitions.go is the ground
// truth this whole module was derived from - every level below was decided
// by reading that file's descriptions and parameter schemas, never by a
// tool's name.
//
// The result is an ORDER, not a boolean, on purpose: clustering has to pick
// a winner among several calls in one run, and manage_worktree's force_dirty
// needs to outrank an ordinary write in that pick (see below) - a yes/no
// flag can't express that; three totally-ordered levels can.
//
// Two positions this module takes deliberately, both settled with Jesse
// before a line of it was written:
//
// - shell ranks "destructive" unconditionally, and the command text is never
//   parsed. `ls` and `rm -rf` are the same tool call, and a shell parser
//   written to tell them apart is a bad trade at any price. The two wrong
//   answers here are not symmetric: ranking shell low can hide a destructive
//   command behind an innocent read summary, while ranking it high only
//   costs a run of `ls` calls leading with a shell line instead of staying
//   quiet. So every shell call assumes the more consequential reading, full
//   stop - do not add command parsing here to "do better".
//
// - delegate/delegate_send are clusterable and ranked like every other tool
//   in this table (2026-07-26 ruling from Jesse). A delegate call does not
//   get a pass from ranking just because it also owns a subagent module
//   rendered beneath it in the transcript - keeping that module's provenance
//   legible when its spawning call is folded into a collapsed run is d6fp's
//   problem to solve, not a reason for this module to exempt the call.
//
// manage_worktree and task_list are ranked from PARSED ARGUMENTS, never from
// the tool name - see the two dedicated functions below. An unrecognized
// tool name, and an unrecognized operation/action on one of those two
// tools, both default to "destructive": silently defaulting to "harmless"
// for a call this table has never seen is exactly the failure mode this
// module exists to prevent - a summary that says "read cache.go" over a run
// that also deleted a file is worse than no summary at all.
import { parseArgs, str } from "./helpers";
import { parseWorktreeCallArgs } from "./worktreeTool";

export type ConsequenceLevel = "read-only" | "mutating" | "destructive";

// Totally ordered so a caller (d6fp) can pick the winner among a run of
// calls with a plain Math.max, rather than re-deriving an order from the
// level strings itself. The compiler enforces that every ConsequenceLevel
// has an entry here - the completeness net a hand-maintained switch can't
// give for free.
export const CONSEQUENCE_RANK: Record<ConsequenceLevel, number> = {
  "read-only": 0,
  mutating: 1,
  destructive: 2,
};

export interface ToolCall {
  toolName?: string;
  argumentsJSON?: string;
}

// FLAT_LEVEL covers every registered tool whose consequence does not depend
// on its arguments - every tool except manage_worktree and task_list, which
// parse an operation/action and are handled by their own functions below.
// Kept as a single object literal (not a switch) so the completeness test in
// consequenceRank.test.ts can enumerate its keys directly and catch this
// table silently drifting from agent/internal/tool/definitions.go's
// registry.
//
// "communicate" is deliberately absent, from this table and from the
// registry the completeness test checks against:
// internal/appprojector/appwire_projection.go's EventToolCallStart handler
// suppresses it before it ever becomes a commandExecution item (a
// communicate call is projected as an agentMessage instead), so it can never
// reach a tool-call cluster for this module to rank.
const FLAT_LEVEL: Record<string, ConsequenceLevel> = {
  // read-only: each of these only observes state; none can change a file, a
  // job, or a branch.
  read_file: "read-only",
  glob: "read-only",
  grep: "read-only",
  list_dir: "read-only",
  read_transcript: "read-only",
  read_session_transcript: "read-only",
  find_session_transcripts: "read-only",
  job_list: "read-only",
  job_status: "read-only",
  job_read_output: "read-only", // definitions.go's own words: "reads never consume or acknowledge anything"
  // job_watch's create/clear operations DO register or remove a standing
  // trigger, but that trigger is this session's own notification plumbing -
  // it never touches a file, job, or branch, so unlike manage_worktree/
  // task_list it stays one flat rank across every operation instead of
  // earning its own per-operation split.
  job_watch: "read-only",
  web_fetch: "read-only",
  web_search: "read-only",
  // Loads a skill's instructions into the calling model's own context; no
  // file, job, or branch is touched, so this is a read like any other
  // reference lookup.
  use_skill: "read-only",

  // mutating: ordinary, expected side effects. None of these carry a flag
  // that escalates them further (contrast manage_worktree's force_dirty).
  write_file: "mutating",
  edit_file: "mutating",
  apply_patch: "mutating",
  // Not a file/job/branch mutation, but not an observation either: it ends
  // the model's turn and blocks the whole session on a reply. Folding it
  // into a read-led cluster summary would hide that the run stopped to ask
  // something, so it sits with the tools that change what happens next
  // rather than the tools that only look.
  ask_user: "mutating",

  // destructive: assume the worst, either because the outcome is
  // unconditionally irreversible or because the blast radius cannot be
  // bounded without parsing content this module deliberately refuses to
  // parse (shell).
  shell: "destructive",
  // Always ends a running job unconditionally; whatever it hadn't finished
  // is now abandoned, no argument makes that any less true.
  job_stop: "destructive",
  // Spawns (delegate) or steers (delegate_send) an independently-acting
  // agent that can itself call any tool in this table, including shell - the
  // same unbounded-blast-radius reasoning as shell, one level removed.
  delegate: "destructive",
  delegate_send: "destructive",
};

// manage_worktree mutates only for create/remove/prune/dispose; list/switch/
// exit just move between or inspect existing worktrees. force_dirty is the
// single most consequential flag in the whole tool surface: definitions.go
// says outright it makes remove/dispose "proceed even if the worktree has
// uncommitted changes (they are discarded)" - the one truly unrecoverable
// loss anywhere in this registry - so it alone escalates remove/dispose to
// "destructive". Plain `force` overrides a DIFFERENT refusal (an unmanaged
// sidecar, or deleting an unmerged branch/lane) and definitions.go says
// explicitly it "does NOT discard uncommitted changes", so it stays
// "mutating" - conflating the two would be the same dishonesty
// worktreeSummary's own DISCARD_NOTE comment already refuses to commit in
// the renderer.
function manageWorktreeLevel(argumentsJSON: string | undefined): ConsequenceLevel {
  const { operation, forceDirty } = parseWorktreeCallArgs(argumentsJSON);
  switch (operation) {
    case "list":
    case "switch":
    case "exit":
      return "read-only";
    case "create":
    case "prune":
      return "mutating";
    case "remove":
    case "dispose":
      return forceDirty ? "destructive" : "mutating";
    default:
      // An operation this table has never heard of - the enum grew, or the
      // call is malformed - assumes the worst rather than silently
      // defaulting to harmless.
      return "destructive";
  }
}

// task_list mutates only for append/update; view only inspects the current
// task snapshot (taskCard.tsx's own mutationRows already treats view as "not
// a mutation" for its card-suppression logic - this is the same fact, read
// for ranking instead of rendering).
function taskListLevel(argumentsJSON: string | undefined): ConsequenceLevel {
  const action = str(parseArgs(argumentsJSON), "action");
  switch (action) {
    case "view":
      return "read-only";
    case "append":
    case "update":
      return "mutating";
    default:
      return "destructive";
  }
}

export function consequenceLevel(call: ToolCall): ConsequenceLevel {
  switch (call.toolName) {
    case "manage_worktree":
      return manageWorktreeLevel(call.argumentsJSON);
    case "task_list":
      return taskListLevel(call.argumentsJSON);
    default: {
      const known = call.toolName !== undefined ? FLAT_LEVEL[call.toolName] : undefined;
      // An unregistered tool name - a future addition to definitions.go this
      // table hasn't caught up with, an MCP/plugin tool, or a malformed
      // item - defaults to the same "assume the worst" rule as shell:
      // silently falling back to read-only is exactly the failure mode this
      // module exists to prevent.
      return known ?? "destructive";
    }
  }
}

export function consequenceRank(call: ToolCall): number {
  return CONSEQUENCE_RANK[consequenceLevel(call)];
}

// Every tool name this module explicitly classifies: FLAT_LEVEL's keys plus
// the two operation-dependent tools handled above. Exported so
// consequenceRank.test.ts can diff this against a literal transcription of
// agent/internal/tool/definitions.go's registry and fail loudly the moment
// the two drift.
export const RANKED_TOOL_NAMES: readonly string[] = [...Object.keys(FLAT_LEVEL), "manage_worktree", "task_list"];
