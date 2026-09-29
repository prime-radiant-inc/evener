// What a tool step says, in both clients: one intent-first line read from the
// step's arguments and output ("Read agent/tree.go · lines 1-40", "Ran go
// test ./agent/..."), and the family a run's summary counts it under. The
// web's tool descriptors call these summaries and the phone's step lines read
// them, so the two never word a step differently.
//
// Every tool no summary covers, an MCP tool among them, still reads as words
// ("Used github: create issue", "Used compact context"), never its raw name.

import { diffStats, editDiffText } from "./editDiff";
import type { ItemModel } from "./model";
import { composeStepWords, type StepWords, withDetail } from "./stepWords";
import { taskMutationSummary } from "./taskListStep";
import { clip, formatByteCount, lineCount, parseArgs, str } from "./toolCallText";
import { lastLine, outputTails, webFetchResult } from "./toolEvidence";
import {
  findSessionsProgress,
  findSessionsWords,
  readTranscriptProgress,
  readTranscriptWords,
} from "./transcriptSteps";
import { worktreeProgress, worktreeWords } from "./worktreeSteps";

/** What a step's summary reads besides the step: the session's directory,
 * which a shell command's leading `cd <cwd> && ` repeats. */
export interface ToolSummaryContext {
  cwd?: string;
}

export { composeStepWords, type StepWords } from "./stepWords";

/** The parts of a step its summary reads. */
export type ToolStep = Pick<ItemModel, "toolName" | "argumentsJSON" | "output" | "raw">;

/** The family a run's summary counts a step under. */
export type ToolFamily =
  | "read"
  | "edit"
  | "search"
  | "fetch"
  | "webSearch"
  | "shell"
  | "skill"
  | "tasks"
  | "transcript"
  | "sessions"
  | "worktree"
  | "mcp"
  | "tool";

const GREP_PATTERN_CLIP = 50;
const QUERY_CLIP = 120;

// --- files ------------------------------------------------------------------

// The line range a read covered: its offset and limit, or else as many lines
// as the output holds. Undefined when neither says: a read whose output hasn't
// arrived, with no limit of its own.
function readLineRange(args: Record<string, unknown>, output: string): string | undefined {
  const offsetArg = args.offset;
  const offset = typeof offsetArg === "number" && offsetArg > 0 ? offsetArg : 1;
  const limitArg = args.limit;
  const count = typeof limitArg === "number" && limitArg > 0 ? limitArg : (output.match(/\n/g) ?? []).length;
  if (count > 0) return `lines ${offset}-${offset + count - 1}`;
  return output === "" ? undefined : `lines ${offset}`;
}

// What an output counts ("2 hits"), only once there is output to count.
function outputCount(output: string | undefined, noun: string): string | undefined {
  return output ? `${lineCount(output)} ${noun}` : undefined;
}

/** The header read_file puts before an image or a document's base64 data. */
export const BINARY_PAYLOAD_HEADER = /^\[(image|document): [^\]]+, base64 data follows\]/;

/** The file parsed arguments name: file_path, or the older path alias. */
export function filePathOf(args: Record<string, unknown>): string | undefined {
  return str(args, "file_path") ?? str(args, "path");
}

/** The file a step read, wrote or edited. */
export function filePathArg(step: Pick<ToolStep, "argumentsJSON">): string | undefined {
  return filePathOf(parseArgs(step.argumentsJSON));
}

/** "Read agent/tree.go · lines 1-40", "Read <file>" for an image or a
 * document (base64 data, not lines), or "Read a file" when it names none. */
function readFileWords(step: ToolStep): StepWords {
  const target = filePathArg(step);
  if (!target) return { verb: "Read a file" };
  if (BINARY_PAYLOAD_HEADER.test(step.output ?? "")) return { verb: "Read", target };
  return withDetail({ verb: "Read", target }, readLineRange(parseArgs(step.argumentsJSON), step.output ?? ""));
}

// The pattern a grep looked for, quoted, and where it looked.
function grepTarget(args: Record<string, unknown>): { pattern: string; where: string } | undefined {
  const pattern = str(args, "pattern");
  if (!pattern) return undefined;
  const path = str(args, "path") ?? ".";
  const globFilter = str(args, "glob_filter");
  return {
    pattern: `"${clip(pattern, GREP_PATTERN_CLIP)}"`,
    where: `in ${path}${globFilter ? ` (${globFilter})` : ""}`,
  };
}

/** 'Searched "func settle" in agent (*.go) · 2 hits', or "Searched files". */
function grepWords(step: ToolStep): StepWords {
  const target = grepTarget(parseArgs(step.argumentsJSON));
  if (!target) return { verb: "Searched files" };
  return withDetail(
    { verb: "Searched", target: target.pattern, after: target.where },
    outputCount(step.output, "hits"),
  );
}

// The directory a listing named and the pattern it filtered by, if any.
function listTarget(args: Record<string, unknown>): { path: string; pattern?: string } | undefined {
  const path = str(args, "path");
  const pattern = str(args, "pattern");
  if (!path && !pattern) return undefined;
  return { path: path || ".", ...(pattern ? { pattern: `(${pattern})` } : {}) };
}

// list_dir ends its listing with the count it returned: "3 entries" after a
// blank line, the count alone for an empty directory, or "2 of 5 entries
// (offset 0) — more…" for a page of a longer one. A summary says how many it
// returned; an output without a count counts its lines. The footer is always
// the last line, so only the last line is read, and only the whole footer
// (agent/session_tools_shell.go's formatDirListing) counts: an entry named
// like a count ("2 entries.md", "2 entries (notes)") never reads as one.
const LIST_DIR_COUNT_RE = /^(\d+)(?: of \d+ entries \(offset \d+\)(?: — more with list_dir\(offset=\d+\))?| entries)$/;

function entries(n: number | string): string {
  return `${n} ${String(n) === "1" ? "entry" : "entries"}`;
}

function listDirCount(output: string | undefined): string | undefined {
  if (!output) return undefined;
  // The footer is the last line of the tool's own output, which may be
  // followed by an intervention the registry appended.
  const tails = outputTails(output);
  for (const tail of tails) {
    const stated = LIST_DIR_COUNT_RE.exec(lastLine(tail.trimEnd()))?.[1];
    if (stated !== undefined) return entries(stated);
  }
  // No footer: the listing's own lines, without an intervention after it.
  return entries(lineCount(tails.at(-1) ?? output));
}

/** "Listed agent/internal · 4 entries", or "Listed files". */
function listDirWords(step: ToolStep): StepWords {
  const target = listTarget(parseArgs(step.argumentsJSON));
  if (!target) return { verb: "Listed files" };
  const words: StepWords = { verb: "Listed", target: target.path };
  if (target.pattern) words.after = target.pattern;
  return withDetail(words, listDirCount(step.output));
}

function globPattern(args: Record<string, unknown>): string | undefined {
  return str(args, "pattern") || str(args, "glob") || undefined;
}

/** "Matched agent/**\/*_test.go · 3 matches", or "Searched files". */
function globWords(step: ToolStep): StepWords {
  const pattern = globPattern(parseArgs(step.argumentsJSON));
  if (!pattern) return { verb: "Searched files" };
  return withDetail({ verb: "Matched", target: pattern }, outputCount(step.output, "matches"));
}

// An edit's result: its diff's added and removed lines, or "ok" for none.
function diffResultText(text: string): string {
  const { added, removed } = diffStats(text);
  return added === 0 && removed === 0 ? "ok" : `+${added} -${removed}`;
}

/** "Edited agent/tree.go · +3 -2", or "Edited a file". */
function editFileWords(step: ToolStep): StepWords {
  const path = filePathArg(step);
  if (!path) return { verb: "Edited a file" };
  const args = parseArgs(step.argumentsJSON);
  const oldString = str(args, "old_string") ?? "";
  const newString = str(args, "new_string") ?? "";
  return { verb: "Edited", target: path, detail: diffResultText(editDiffText(path, oldString, newString)) };
}

/** "Wrote agent/tree_order.go", or "Wrote a file". */
function writeFileWords(step: ToolStep): StepWords {
  const path = filePathArg(step);
  return path ? { verb: "Wrote", target: path } : { verb: "Wrote a file" };
}

const PATCH_FILE_HEADER_RE = /^\*\*\* (?:Add|Update|Delete) File: (.+)$/;

// The files a v4a patch touches, in order, each once.
function patchTargets(patch: string): string[] {
  const seen = new Set<string>();
  const targets: string[] = [];
  for (const line of patch.split("\n")) {
    const match = PATCH_FILE_HEADER_RE.exec(line);
    if (match?.[1] !== undefined && !seen.has(match[1])) {
      seen.add(match[1]);
      targets.push(match[1]);
    }
  }
  return targets;
}

/** "Patched agent/tree.go, agent/tree_order.go · +3 -1", or "Patched files". */
function applyPatchWords(step: ToolStep): StepWords {
  const patch = str(parseArgs(step.argumentsJSON), "patch") ?? "";
  const targets = patchTargets(patch);
  if (targets.length === 0) return { verb: "Patched files" };
  return { verb: "Patched", target: targets.join(", "), detail: diffResultText(patch) };
}

// --- shell ------------------------------------------------------------------

/** A shell step's command, from whichever argument the tool names it by. */
export function shellCommand(args: Record<string, unknown>): string {
  return str(args, "command") ?? str(args, "cmd") ?? "";
}

/** The command without a leading `cd <cwd> && ` that only repeats the
 * session's own directory. */
export function stripRedundantCd(command: string, cwd: string | undefined): string {
  if (cwd === undefined || cwd === "") return command;
  const prefix = `cd ${cwd} && `;
  if (!command.startsWith(prefix)) return command;
  const rest = command.slice(prefix.length);
  return rest === "" ? command : rest;
}

function commandOf(step: Pick<ToolStep, "argumentsJSON">, ctx?: ToolSummaryContext): string {
  return stripRedundantCd(shellCommand(parseArgs(step.argumentsJSON)), ctx?.cwd).trim();
}

/** "Ran go test ./agent/...", or "Ran a command". The exit code stays out: a
 * failed step says so with its own mark. */
function shellWords(step: ToolStep, ctx?: ToolSummaryContext): StepWords {
  const command = commandOf(step, ctx);
  return command ? { verb: "Ran", target: command } : { verb: "Ran a command" };
}

// --- web ----------------------------------------------------------------------

/** How big a fetched page was: web_fetch's size_bytes (from the one parse,
 * webFetchResult), else its output. */
export function webFetchByteCount(output: string): number {
  return webFetchResult(output)?.bytes ?? output.length;
}

/** "Fetched https://example.com/release-notes · 48213 bytes", or "Fetched a
 * page". */
function webFetchWords(step: ToolStep): StepWords {
  const url = str(parseArgs(step.argumentsJSON), "url");
  if (!url) return { verb: "Fetched a page" };
  const output = step.output ?? "";
  return withDetail({ verb: "Fetched", target: url }, output ? formatByteCount(webFetchByteCount(output)) : undefined);
}

/** The lines of a web search's output that are results. */
export function webSearchResultLines(output: string): string[] {
  return output.split("\n").filter((line) => line.trim() !== "");
}

function searchQuery(args: Record<string, unknown>): string | undefined {
  const query = str(args, "query") || str(args, "q");
  return query ? clip(query, QUERY_CLIP) : undefined;
}

/** 'Searched the web for "go race detector" · 2 results', or "Searched the
 * web". */
function webSearchWords(step: ToolStep): StepWords {
  const query = searchQuery(parseArgs(step.argumentsJSON));
  if (!query) return { verb: "Searched the web" };
  const output = step.output ?? "";
  return withDetail(
    { verb: "Searched the web for", target: `"${query}"` },
    output ? `${webSearchResultLines(output).length} results` : undefined,
  );
}

// --- skills -------------------------------------------------------------------

/** The skill a use_skill step activated. */
export function skillName(step: Pick<ToolStep, "argumentsJSON">): string {
  const args = parseArgs(step.argumentsJSON);
  return str(args, "skill_name") ?? str(args, "name") ?? "";
}

/** "Activated skill: systematic-debugging", or "Activated a skill". */
function useSkillWords(step: ToolStep): StepWords {
  const name = skillName(step);
  return name ? { verb: "Activated skill:", target: name } : { verb: "Activated a skill" };
}

// --- tasks ----------------------------------------------------------------------

/** Whether a task_list call asked for a change: a bare call, an empty add and
 * update, or a historical action with nothing in it ("view", an empty
 * "append" or "update", an action this build doesn't know) only reads the
 * list. */
export function taskListChanges(step: Pick<ToolStep, "argumentsJSON">): boolean {
  const args = parseArgs(step.argumentsJSON);
  const nonEmpty = (list: unknown) => Array.isArray(list) && list.length > 0;
  switch (str(args, "action") ?? "") {
    case "":
      return nonEmpty(args.add) || nonEmpty(args.update);
    case "append":
      return nonEmpty(args.tasks);
    case "update":
      return nonEmpty(args.updates);
    default:
      return false;
  }
}

// The latest task the call touched ("☑ Reproduce the race", "→ Fix the
// drain"), as the web's task card folds it; a call that touched no task's
// status says whether it changed the list or only read it.
function taskListWords(step: ToolStep): StepWords {
  const touched = taskMutationSummary(step);
  if (touched) return { verb: touched };
  return { verb: taskListChanges(step) ? "Updated the task list" : "Checked the task list" };
}

// --- every other tool ---------------------------------------------------------

/** A tool name's words: its underscores and hyphens are spaces
 * ("create_issue" reads "create issue", "foo-bar" reads "foo bar"). */
export function words(name: string): string {
  return name.replace(/[_-]+/g, " ").trim();
}

/** An MCP tool's server and tool in words. The MCP manager names each tool
 * <server>__<tool>, a hyphen in either turned into an underscore
 * (agent/internal/mcp's sanitizeToolName). The name splits at its first
 * double underscore, so a server whose own name holds one ("my__srv") reads
 * as the part before it; nothing in the name marks the true boundary.
 * Undefined for any other name. */
export function mcpToolParts(toolName: string): { server: string; tool: string } | undefined {
  const at = toolName.indexOf("__");
  if (at <= 0 || at + 2 >= toolName.length) return undefined;
  return { server: words(toolName.slice(0, at)), tool: words(toolName.slice(at + 2)) };
}

/** A tool no summary covers, in words: "Used github: create issue" for an MCP
 * tool, "Used compact context" for any other. Never its raw name. */
export function fallbackToolSummary(step: Pick<ToolStep, "toolName">): string {
  return `Used ${toolInWords(step.toolName ?? "")}`;
}

function toolInWords(name: string): string {
  const mcp = mcpToolParts(name);
  if (mcp) return `${mcp.server}: ${mcp.tool}`;
  return words(name) || "a tool";
}

// --- a running step -------------------------------------------------------------

// What a running step is doing, for the phone's status tray: the summary's
// live form, without the counts its output hasn't given yet.
function progressFor(
  family: ToolFamily,
  step: Pick<ToolStep, "toolName" | "argumentsJSON">,
  ctx?: ToolSummaryContext,
): string {
  const args = parseArgs(step.argumentsJSON);
  const name = step.toolName ?? "";
  switch (family) {
    case "read": {
      const path = filePathArg(step);
      return path ? `Reading ${path}` : "Reading a file";
    }
    case "search": {
      if (name === "glob") {
        const pattern = globPattern(args);
        return pattern ? `Matching ${pattern}` : "Searching files";
      }
      if (name === "list_dir" || name === "list_directory") {
        const target = listTarget(args);
        return target ? `Listing ${target.path}${target.pattern ? ` ${target.pattern}` : ""}` : "Listing files";
      }
      const target = grepTarget(args);
      return target ? `Searching ${target.pattern} ${target.where}` : "Searching files";
    }
    case "edit": {
      if (name === "apply_patch") {
        const targets = patchTargets(str(args, "patch") ?? "");
        return targets.length > 0 ? `Patching ${targets.join(", ")}` : "Patching files";
      }
      const path = filePathArg(step);
      const verb = name === "write_file" ? "Writing" : "Editing";
      return path ? `${verb} ${path}` : `${verb} a file`;
    }
    case "shell": {
      const firstLine = commandOf(step, ctx).split("\n")[0]?.trim();
      return firstLine ? `Running ${firstLine}` : "Running a command";
    }
    case "fetch": {
      const url = str(args, "url");
      return url ? `Fetching ${url}` : "Fetching a page";
    }
    case "webSearch": {
      const query = searchQuery(args);
      return query ? `Searching the web for "${query}"` : "Searching the web";
    }
    case "skill": {
      const skill = skillName(step);
      return skill ? `Activating skill: ${skill}` : "Activating a skill";
    }
    case "tasks":
      return taskListChanges(step) ? "Updating the task list" : "Checking the task list";
    case "transcript":
      return readTranscriptProgress(step);
    case "sessions":
      return findSessionsProgress(step);
    case "worktree":
      return worktreeProgress(step);
    case "mcp":
    case "tool":
      return `Using ${toolInWords(name)}`;
  }
}

// --- the table ----------------------------------------------------------------

type WordsOf = (step: ToolStep, ctx?: ToolSummaryContext) => StepWords;

interface ToolEntry {
  family: ToolFamily;
  words: WordsOf;
}

const TOOLS: Record<string, ToolEntry> = {
  read_file: { family: "read", words: readFileWords },
  edit_file: { family: "edit", words: editFileWords },
  write_file: { family: "edit", words: writeFileWords },
  apply_patch: { family: "edit", words: applyPatchWords },
  grep: { family: "search", words: grepWords },
  grep_files: { family: "search", words: grepWords },
  grep_search: { family: "search", words: grepWords },
  glob: { family: "search", words: globWords },
  list_dir: { family: "search", words: listDirWords },
  list_directory: { family: "search", words: listDirWords },
  web_fetch: { family: "fetch", words: webFetchWords },
  web_search: { family: "webSearch", words: webSearchWords },
  shell: { family: "shell", words: shellWords },
  exec_command: { family: "shell", words: shellWords },
  run_shell_command: { family: "shell", words: shellWords },
  use_skill: { family: "skill", words: useSkillWords },
  task_list: { family: "tasks", words: taskListWords },
  read_transcript: { family: "transcript", words: readTranscriptWords },
  read_session_transcript: { family: "transcript", words: readTranscriptWords },
  find_session_transcripts: { family: "sessions", words: findSessionsWords },
  manage_worktree: { family: "worktree", words: worktreeWords },
};

// A tool's one-line summary: its words composed, as the web draws it.
const summaryOf =
  (words: WordsOf) =>
  (step: ToolStep, ctx?: ToolSummaryContext): string =>
    composeStepWords(words(step, ctx));

export const readFileSummary = summaryOf(readFileWords);
export const grepSummary = summaryOf(grepWords);
export const listDirSummary = summaryOf(listDirWords);
export const globSummary = summaryOf(globWords);
export const editFileSummary = summaryOf(editFileWords);
export const writeFileSummary = summaryOf(writeFileWords);
export const applyPatchSummary = summaryOf(applyPatchWords);
export const shellSummary = summaryOf(shellWords);
export const webFetchSummary = summaryOf(webFetchWords);
export const webSearchSummary = summaryOf(webSearchWords);
export const useSkillSummary = summaryOf(useSkillWords);

function entryFor(toolName: string): ToolEntry | undefined {
  return Object.hasOwn(TOOLS, toolName) ? TOOLS[toolName] : undefined;
}

/** The family a run's summary counts a step of this tool under. */
export function toolFamily(toolName: string): ToolFamily {
  return entryFor(toolName)?.family ?? (mcpToolParts(toolName) ? "mcp" : "tool");
}

/** A step's words in parts, for any tool: what it did, what it acted on
 * (the target a client can set apart), and what it found. */
export function toolStepWords(step: ToolStep, ctx?: ToolSummaryContext): StepWords {
  const entry = entryFor(step.toolName ?? "");
  return entry ? entry.words(step, ctx) : { verb: fallbackToolSummary(step) };
}

/** A step's one-line summary, for any tool: its words composed. */
export function toolStepSummary(step: ToolStep, ctx?: ToolSummaryContext): string {
  return composeStepWords(toolStepWords(step, ctx));
}

/** What a running step is doing, for any tool: "Reading agent/tree.go",
 * "Running go test ./...", "Using github: create issue". */
export function toolStepProgress(step: Pick<ToolStep, "toolName" | "argumentsJSON">, ctx?: ToolSummaryContext): string {
  return progressFor(toolFamily(step.toolName ?? ""), step, ctx);
}
