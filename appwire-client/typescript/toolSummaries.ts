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
import { clip, formatByteCount, lineCount, parseArgs, parseJSONObject, str } from "./toolCallText";

/** What a step's summary reads besides the step: the session's directory,
 * which a shell command's leading `cd <cwd> && ` repeats. */
export interface ToolSummaryContext {
  cwd?: string;
}

/** The parts of a step its summary reads. */
export type ToolStep = Pick<ItemModel, "toolName" | "argumentsJSON" | "output">;

/** The family a run's summary counts a step under. */
export type ToolFamily = "read" | "edit" | "search" | "fetch" | "webSearch" | "shell" | "skill" | "mcp" | "tool";

const GREP_PATTERN_CLIP = 50;
const QUERY_CLIP = 120;

// --- files ------------------------------------------------------------------

// The line range a read covered: its offset and limit, or else as many lines
// as the output holds.
function readLineRange(args: Record<string, unknown>, output: string): string {
  const offsetArg = args.offset;
  const offset = typeof offsetArg === "number" && offsetArg > 0 ? offsetArg : 1;
  const limitArg = args.limit;
  const count = typeof limitArg === "number" && limitArg > 0 ? limitArg : (output.match(/\n/g) ?? []).length;
  return count > 0 ? `lines ${offset}-${offset + count - 1}` : `lines ${offset}`;
}

/** The header read_file puts before an image or a document's base64 data. */
export const BINARY_PAYLOAD_HEADER = /^\[(image|document): [^\]]+, base64 data follows\]/;

/** The file a step read, wrote or edited. */
export function filePathArg(step: Pick<ToolStep, "argumentsJSON">): string | undefined {
  const args = parseArgs(step.argumentsJSON);
  return str(args, "file_path") ?? str(args, "path");
}

/** "Read agent/tree.go · lines 1-40", or just "Read <file>" for an image or a
 * document, whose output is base64 data rather than lines. */
export function readFileSummary(step: ToolStep): string {
  const target = filePathArg(step) ?? "";
  if (BINARY_PAYLOAD_HEADER.test(step.output ?? "")) return `Read ${target}`;
  return `Read ${target} · ${readLineRange(parseArgs(step.argumentsJSON), step.output ?? "")}`;
}

function grepTarget(args: Record<string, unknown>): string {
  const pattern = clip(str(args, "pattern") ?? "", GREP_PATTERN_CLIP);
  const path = str(args, "path") ?? ".";
  const globFilter = str(args, "glob_filter");
  return `"${pattern}" in ${path}${globFilter ? ` (${globFilter})` : ""}`;
}

/** 'Searched "func settle" in agent (*.go) · 2 hits'. */
export function grepSummary(step: ToolStep): string {
  return `Searched ${grepTarget(parseArgs(step.argumentsJSON))} · ${lineCount(step.output ?? "")} hits`;
}

/** "Listed agent/internal · 4 entries". */
export function listDirSummary(step: ToolStep): string {
  const args = parseArgs(step.argumentsJSON);
  const path = str(args, "path") ?? ".";
  const pattern = str(args, "pattern");
  return `Listed ${path}${pattern ? ` (${pattern})` : ""} · ${lineCount(step.output ?? "")} entries`;
}

/** "Matched agent/**\/*_test.go · 3 matches". */
export function globSummary(step: ToolStep): string {
  const args = parseArgs(step.argumentsJSON);
  const pattern = str(args, "pattern") ?? str(args, "glob") ?? "";
  return `Matched ${pattern} · ${lineCount(step.output ?? "")} matches`;
}

// An edit's result: its diff's added and removed lines, or "ok" for none.
function diffResultText(text: string): string {
  const { added, removed } = diffStats(text);
  return added === 0 && removed === 0 ? "ok" : `+${added} -${removed}`;
}

/** "Edited agent/tree.go · +3 -2". */
export function editFileSummary(step: ToolStep): string {
  const args = parseArgs(step.argumentsJSON);
  const path = str(args, "file_path") ?? str(args, "path") ?? "";
  const oldString = str(args, "old_string") ?? "";
  const newString = str(args, "new_string") ?? "";
  return `Edited ${path} · ${diffResultText(editDiffText(path, oldString, newString))}`;
}

/** "Wrote agent/tree_order.go". */
export function writeFileSummary(step: ToolStep): string {
  return `Wrote ${filePathArg(step) ?? ""}`;
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

/** "Patched agent/tree.go, agent/tree_order.go · +3 -1". */
export function applyPatchSummary(step: ToolStep): string {
  const patch = str(parseArgs(step.argumentsJSON), "patch") ?? "";
  return `Patched ${patchTargets(patch).join(", ")} · ${diffResultText(patch)}`;
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

/** "Ran go test ./agent/...". The exit code stays out: a failed step says so
 * with its own mark. */
export function shellSummary(step: ToolStep, ctx?: ToolSummaryContext): string {
  return `Ran ${stripRedundantCd(shellCommand(parseArgs(step.argumentsJSON)), ctx?.cwd)}`;
}

// --- web ----------------------------------------------------------------------

/** How big a fetched page was: web_fetch's size_bytes, else its output. */
export function webFetchByteCount(output: string): number {
  const sizeBytes = parseJSONObject(output)?.size_bytes;
  return typeof sizeBytes === "number" ? sizeBytes : output.length;
}

/** "Fetched https://example.com/release-notes · 48213 bytes". */
export function webFetchSummary(step: ToolStep): string {
  const url = str(parseArgs(step.argumentsJSON), "url") ?? "";
  return `Fetched ${url} · ${formatByteCount(webFetchByteCount(step.output ?? ""))}`;
}

/** The lines of a web search's output that are results. */
export function webSearchResultLines(output: string): string[] {
  return output.split("\n").filter((line) => line.trim() !== "");
}

/** 'Searched the web for "go race detector" · 2 results'. */
export function webSearchSummary(step: ToolStep): string {
  const args = parseArgs(step.argumentsJSON);
  const query = clip(str(args, "query") ?? str(args, "q") ?? "", QUERY_CLIP);
  return `Searched the web for "${query}" · ${webSearchResultLines(step.output ?? "").length} results`;
}

// --- skills -------------------------------------------------------------------

/** The skill a use_skill step activated. */
export function skillName(step: Pick<ToolStep, "argumentsJSON">): string {
  const args = parseArgs(step.argumentsJSON);
  return str(args, "skill_name") ?? str(args, "name") ?? "";
}

/** "Activated skill: systematic-debugging". */
export function useSkillSummary(step: ToolStep): string {
  return `Activated skill: ${skillName(step)}`;
}

// --- every other tool ---------------------------------------------------------

// A tool name's words: its underscores are spaces ("create_issue" reads
// "create issue").
function words(name: string): string {
  return name.replaceAll("_", " ").trim();
}

/** An MCP tool's server and tool in words. The MCP manager names each tool
 * <server>__<tool>, a hyphen in either turned into an underscore
 * (agent/internal/mcp's sanitizeToolName). Undefined for any other name. */
export function mcpToolParts(toolName: string): { server: string; tool: string } | undefined {
  const at = toolName.indexOf("__");
  if (at <= 0 || at + 2 >= toolName.length) return undefined;
  return { server: words(toolName.slice(0, at)), tool: words(toolName.slice(at + 2)) };
}

/** A tool no summary covers, in words: "Used github: create issue" for an MCP
 * tool, "Used compact context" for any other. Never its raw name. */
export function fallbackToolSummary(step: Pick<ToolStep, "toolName">): string {
  const name = step.toolName ?? "";
  const mcp = mcpToolParts(name);
  if (mcp) return `Used ${mcp.server}: ${mcp.tool}`;
  return name ? `Used ${words(name)}` : "Used a tool";
}

// --- the table ----------------------------------------------------------------

interface ToolEntry {
  family: ToolFamily;
  summary: (step: ToolStep, ctx?: ToolSummaryContext) => string;
}

const TOOLS: Record<string, ToolEntry> = {
  read_file: { family: "read", summary: readFileSummary },
  edit_file: { family: "edit", summary: editFileSummary },
  write_file: { family: "edit", summary: writeFileSummary },
  apply_patch: { family: "edit", summary: applyPatchSummary },
  grep: { family: "search", summary: grepSummary },
  grep_files: { family: "search", summary: grepSummary },
  grep_search: { family: "search", summary: grepSummary },
  glob: { family: "search", summary: globSummary },
  list_dir: { family: "search", summary: listDirSummary },
  list_directory: { family: "search", summary: listDirSummary },
  web_fetch: { family: "fetch", summary: webFetchSummary },
  web_search: { family: "webSearch", summary: webSearchSummary },
  shell: { family: "shell", summary: shellSummary },
  exec_command: { family: "shell", summary: shellSummary },
  run_shell_command: { family: "shell", summary: shellSummary },
  use_skill: { family: "skill", summary: useSkillSummary },
};

function entryFor(toolName: string): ToolEntry | undefined {
  return Object.hasOwn(TOOLS, toolName) ? TOOLS[toolName] : undefined;
}

/** The family a run's summary counts a step of this tool under. */
export function toolFamily(toolName: string): ToolFamily {
  return entryFor(toolName)?.family ?? (mcpToolParts(toolName) ? "mcp" : "tool");
}

/** A step's one-line summary, for any tool. */
export function toolStepSummary(step: ToolStep, ctx?: ToolSummaryContext): string {
  const entry = entryFor(step.toolName ?? "");
  return entry ? entry.summary(step, ctx) : fallbackToolSummary(step);
}
