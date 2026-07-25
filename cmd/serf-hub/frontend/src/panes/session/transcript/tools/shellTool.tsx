// The shell/exec_command/run_shell_command descriptor (parity checklist
// §2's shellRenderer). Failure signal for a settled call: the daemon promotes
// the process exit code onto the item as a typed wire field
// (ItemModel.exitCode), so that structured number is the PRIMARY source for
// both the exit-code summary suffix and autoExpand. One fact still shapes the
// logic: a nonzero EXIT is not a tool error. The wire stamps an honest settled
// status — "failed" only when the tool RESULT carried an error, "completed"
// otherwise (apptranscript.SettledToolStatus, appwire_projection.go:438) — and
// a command that ran and returned nonzero is a clean tool result (empty
// ItemModel.error, status "completed") that this descriptor still flags via
// exitCode alone. ItemModel.error carries a denial/failure message when the
// call itself failed or was denied (mapped by reducer.ts's wireItemToModel); it
// drives the generic failed-row treatment in ToolCallItem and is a distinct
// signal from a nonzero exit, handled there, not here. When exitCode is absent (an
// old daemon that doesn't populate it), the descriptor falls back to the
// output-footer text heuristic below: agent/session_tools_shell.go's
// formatShellResult appends a trailing "[exit <N> · ...]" bracketed footer
// whenever the command wasn't backgrounded and an exit code was captured. The
// heuristic looks only inside the FINAL bracketed segment (never the command's
// own stdout/stderr body) to keep false positives unlikely.

import type { ItemModel } from "../../../../protocol/model";
import { CodeBlock } from "../../../../widgets";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import { clip, parseArgs, str, tailFold, tailSlice, trailingBracketFooter } from "./helpers";

const COMMAND_CLIP = 80;
const TAIL_MAX_CHARS = 8000;

function shellCommand(args: Record<string, unknown>): string {
  return str(args, "command") ?? str(args, "cmd") ?? "";
}

// A second, differently-shaped trailer for the "buffered" execution
// environment fallback (used when the env doesn't support streaming,
// agent/session_tools_shell.go's runBufferedShell): no StateResult/
// brackets at all, just a bare "exit_code=N duration_ms=N timed_out=bool"
// line.
const BUFFERED_EXIT_CODE_RE = /\bexit_code=(-?\d+)\b/;

// parseShellExitCode reads "exit <N>" out of the trailing "[... exit <N>
// ...]" footer formatShellResult appends (the common, streaming-execenv
// path), falling back to the buffered-execenv trailer above. This is the
// old-daemon fallback used only when the typed ItemModel.exitCode is absent —
// see this file's own header. Returns undefined for a backgrounded/still-
// running command (no trailer of either shape yet).
function parseShellExitCode(output: string): number | undefined {
  const footer = trailingBracketFooter(output);
  if (footer !== undefined) {
    const bracketed = /\bexit (-?\d+)\b/.exec(footer);
    if (bracketed) return Number(bracketed[1]);
  }
  const buffered = BUFFERED_EXIT_CODE_RE.exec(output);
  return buffered ? Number(buffered[1]) : undefined;
}

// shellExitCode is the descriptor's single exit-code source: the typed wire
// field (ItemModel.exitCode) first, the output-footer text heuristic only as
// the old-daemon fallback. `??` (not `||`) so a real typed 0 stays 0 rather
// than falling through to the text scan.
function shellExitCode(item: ItemModel): number | undefined {
  return item.exitCode ?? parseShellExitCode(item.output ?? "");
}

// The body is the OUTPUT, nothing else. It does not repeat the command: the
// collapsed row above already names it (A4 - "repeats the tool call but not
// truncated"). The row's own summary is what clips a long command, and a reader
// who wants the untruncated form gets it from the row's hover title, not from a
// second copy of the call under it.
function ShellBody({ item, live }: ToolRenderProps) {
  const output = item.output ?? "";
  if (output === "") return null;
  const body = live ? tailSlice(output, TAIL_MAX_CHARS) : tailFold(output, TAIL_MAX_CHARS);
  return <CodeBlock text={body} copyLabel="Copy output" />;
}

// nonzeroExit is the "this command failed" predicate shared by failed() and
// autoExpand() so the glyph and the auto-open can never disagree.
function nonzeroExit(item: ItemModel): boolean {
  const exitCode = shellExitCode(item);
  return exitCode !== undefined && exitCode !== 0;
}

registerToolRenderer({
  match: (name) => name === "shell" || name === "exec_command" || name === "run_shell_command",
  // The exit code is NOT in the summary: a nonzero exit is announced by the
  // row's failure glyph instead (A2 - "exit 1" as the headline made every
  // failure look like a footnote). The number itself stays reachable via
  // detail() below, which the row hangs off its hover title.
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    return `Ran ${clip(shellCommand(args), COMMAND_CLIP)}`;
  },
  body: ShellBody,
  failed: nonzeroExit,
  // The hover title carries the two facts the row itself deliberately doesn't
  // shout: the UNTRUNCATED command (the summary clips at COMMAND_CLIP) and the
  // exit code.
  detail(item: ItemModel) {
    const command = shellCommand(parseArgs(item.argumentsJSON));
    const exitCode = shellExitCode(item);
    const exit = exitCode === undefined ? "" : ` · exit ${exitCode}`;
    return command === "" && exit === "" ? undefined : `$ ${command}${exit}`;
  },
  autoExpand: nonzeroExit,
});
