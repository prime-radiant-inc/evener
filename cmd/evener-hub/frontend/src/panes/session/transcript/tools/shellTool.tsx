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

import { useRef } from "react";
import type { ItemModel } from "../../../../protocol/model";
import { useThreadsStore } from "../../../../stores/threads";
import { useOptionalTranscriptRenderContext } from "../../../../transcriptDisplay/renderContext";
import { CodeBlock, ShellCommandBlock } from "../../../../widgets";
import { AnsiTailBuffer } from "../../../../widgets/codeblock/ansi";
import type { ToolRenderProps, ToolSummaryContext } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import { parseArgs, str, trailingBracketFooter } from "./helpers";

const TAIL_MAX_CHARS = 8000;

function shellCommand(args: Record<string, unknown>): string {
  return str(args, "command") ?? str(args, "cmd") ?? "";
}

// stripRedundantCd removes the literal "cd <cwd> && " prefix models
// habitually prepend even though the daemon already runs every command in
// the session cwd. Literal match only — a cd anywhere else is information
// and stays. Display-only: argumentsJSON is never modified.
export function stripRedundantCd(command: string, cwd: string | undefined): string {
  if (cwd === undefined || cwd === "") return command;
  const prefix = `cd ${cwd} && `;
  if (!command.startsWith(prefix)) return command;
  const rest = command.slice(prefix.length);
  return rest === "" ? command : rest;
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

// The row summary owns collapsed command presentation. The expanded body owns
// a readable formatted command block and the output block independently.
function ShellBodyContent({ item, live, cwd, sessionRef }: ToolRenderProps) {
  const rawCommand = shellCommand(parseArgs(item.argumentsJSON));
  const command = stripRedundantCd(rawCommand, cwd);
  const output = item.output ?? "";
  const buffer = useRef<{ itemId: string; sessionRef?: string; live: boolean; tail: AnsiTailBuffer } | undefined>(
    undefined,
  );
  if (
    buffer.current?.itemId !== item.id ||
    buffer.current.sessionRef !== sessionRef ||
    (!live && buffer.current.live)
  ) {
    buffer.current = { itemId: item.id, sessionRef, live, tail: new AnsiTailBuffer(TAIL_MAX_CHARS) };
  }
  buffer.current.live = live;
  const tail = buffer.current.tail.update(output);
  if (command === "" && output === "") return null;
  const body =
    live || !tail.truncated
      ? tail.renderedText
      : `earlier output not retained — showing the last ${TAIL_MAX_CHARS.toLocaleString("en-US")} chars\n${tail.renderedText}`;
  return (
    <>
      {command !== "" && <ShellCommandBlock command={command} copyText={rawCommand} />}
      {output !== "" && <CodeBlock text={body} copyText={tail.copyText} copyLabel="Copy output" ansi />}
    </>
  );
}

function LegacyShellBody(props: ToolRenderProps) {
  const cwd = useThreadsStore((state) =>
    props.sessionRef === undefined ? undefined : state.threads.get(props.sessionRef)?.cwd,
  );
  return <ShellBodyContent {...props} cwd={cwd} />;
}

function ShellBody(props: ToolRenderProps) {
  const context = useOptionalTranscriptRenderContext();
  return context === null ? (
    <LegacyShellBody {...props} />
  ) : (
    <ShellBodyContent {...props} cwd={props.cwd ?? context.thread?.cwd} />
  );
}

// nonzeroExit is the "this command failed" predicate shared by failed() and
// autoExpand() so the glyph and the auto-open can never disagree.
function nonzeroExit(item: ItemModel): boolean {
  const exitCode = shellExitCode(item);
  return exitCode !== undefined && exitCode !== 0;
}

registerToolRenderer({
  // These three names are mirrored server-side by
  // internal/apptranscript's ShellToolNames, which the session-scale failure
  // count reads exit codes for. The two lists have to agree: a name here and
  // not there is a row wearing a failure glyph the count omits.
  match: (name) => name === "shell" || name === "exec_command" || name === "run_shell_command",
  icon: "terminal",
  monoSummary: true,
  fold: "consequential",
  // The exit code is NOT in the summary: a nonzero exit is announced by the
  // row's failure glyph instead (A2 - "exit 1" as the headline made every
  // failure look like a footnote). The number itself stays reachable via
  // detail() below, which the row shows both as a hover title and as real text
  // in the expanded body.
  summary(item: ItemModel, ctx?: ToolSummaryContext) {
    const command = stripRedundantCd(shellCommand(parseArgs(item.argumentsJSON)), ctx?.cwd);
    return `Ran ${command}`;
  },
  body: ShellBody,
  failed: nonzeroExit,
  // The exit code, and ONLY the exit code. It deliberately does not carry the
  // command as well: detail() renders as the row's hover title, and folding
  // the command in would put a second copy of the call under the row. The
  // expanded body already shows the command pretty-printed
  // (ShellCommandBlock), so nothing is lost.
  detail(item: ItemModel) {
    const exitCode = shellExitCode(item);
    return exitCode === undefined ? undefined : `exit ${exitCode}`;
  },
  // The row summary IS the raw one-line command; the expanded body renders
  // that same command pretty-printed. Showing both on an open row duplicated
  // the call, so the summary drops out while expanded (the collapsed row
  // keeps it - there it is the only glance at the command).
  summaryHiddenWhenExpanded: true,
  autoExpand: nonzeroExit,
});
