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

import type { ItemModel } from "@evener/appwire-client";
import { parseArgs, str, trailingBracketFooter } from "@evener/appwire-client";
import { useRef } from "react";
import { useThreadsStore } from "../../../../stores/threads";
import { useOptionalTranscriptRenderContext } from "../../../../transcriptDisplay/renderContext";
import { CodeBlock, ShellCommandBlock } from "../../../../widgets";
import { AnsiTailBuffer } from "../../../../widgets/codeblock/ansi";
import type { ToolRenderProps, ToolSummaryContext } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";

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

// The buffered-env trailer's exact full-line shape (agent/session_tools_
// shell.go's runBufferedShell ends the output in this bare line).
const BUFFERED_TRAILER_RE = /\bexit_code=(-?\d+) duration_ms=\d+ timed_out=(?:true|false)$/;

// exitTrailerCode reads the output's own TERMINAL exit trailer, shape-aware:
// the daemon's bracketed "[… exit N …]" final segment (trailingBracketFooter
// already returns only the last bracketed segment), or the buffered-env
// trailer as the output's final non-empty line. Never a bare "exit_code=N"
// token echoed mid-stream by the command's own stdout (a test runner printing
// that string is not a trailer), which parseShellExitCode's whole-output
// fallback scan accepts - this stricter read exists for the body's
// synthesized-footer gate below, where a false positive would suppress the
// number's only authoritative copy.
function exitTrailerCode(output: string): number | undefined {
  const footer = trailingBracketFooter(output);
  if (footer !== undefined) {
    const bracketed = /\bexit (-?\d+)\b/.exec(footer);
    if (bracketed) return Number(bracketed[1]);
  }
  const finalLine = output.trimEnd().split("\n").pop() ?? "";
  const buffered = BUFFERED_TRAILER_RE.exec(finalLine);
  return buffered ? Number(buffered[1]) : undefined;
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
  // The typed exit code's one home is the captured output's trailing footer,
  // but the wire carries the two independently: an output can exist with NO
  // exit trailer of either shape while the typed ItemModel.exitCode is present
  // (a stored transcript predating footer-baking replayed by a newer daemon, a
  // buffered path that lost its line). With the row's hover title retired, the
  // body is the number's only home, so it synthesizes the daemon's own footer
  // shape for exactly that gap - display-only, like the truncated-tail notice
  // below, so Copy output keeps the raw evidence. Two guards keep the
  // synthesis honest: the -1 sentinel of a signalled job (job-control.md:1012
  // - "not a shell code"; formatShellResult omits it from the footer for the
  // same reason) never fabricates an exit line, and an output trailer only
  // suppresses the synthesis when its code AGREES with the typed value, so a
  // trailer that disagrees leaves the authoritative typed footer standing
  // beside the verbatim raw text.
  const exitFooter =
    item.exitCode !== undefined && item.exitCode >= 0 && exitTrailerCode(output) !== item.exitCode
      ? `[exit ${item.exitCode}]`
      : undefined;
  const renderedOutput =
    tail.renderedText === "" && output === ""
      ? exitFooter
      : exitFooter === undefined
        ? tail.renderedText
        : `${tail.renderedText}\n${exitFooter}`;
  if (command === "" && renderedOutput === undefined) return null;
  return (
    <>
      {command !== "" && <ShellCommandBlock command={command} copyText={rawCommand} />}
      {renderedOutput !== undefined && (
        <CodeBlock
          text={
            live || !tail.truncated
              ? renderedOutput
              : `earlier output not retained — showing the last ${TAIL_MAX_CHARS.toLocaleString("en-US")} chars\n${renderedOutput}`
          }
          copyText={tail.copyText}
          copyLabel="Copy output"
          ansi
        />
      )}
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
  // failure look like a footnote). The number's home is the real text at the
  // tail of the expanded body: formatShellResult bakes "[exit N]" into the
  // captured output itself (agent/session_tools_shell.go) — and when the
  // output carries no trailer of either shape, the body synthesizes the typed
  // code's line instead (see ShellBodyContent's exitFooter).
  summary(item: ItemModel, ctx?: ToolSummaryContext) {
    const command = stripRedundantCd(shellCommand(parseArgs(item.argumentsJSON)), ctx?.cwd);
    return `Ran ${command}`;
  },
  body: ShellBody,
  failed: nonzeroExit,
  // The row summary IS the raw one-line command; the expanded body renders
  // that same command pretty-printed. Showing both on an open row duplicated
  // the call, so while expanded the summary text swaps to this placeholder:
  // the summary line stays (it is the line the disclosure chevron rides) and
  // the collapsed row keeps the command, where it is the only glance at it.
  summaryWhenExpanded: "Ran a shell command",
  autoExpand: nonzeroExit,
});
