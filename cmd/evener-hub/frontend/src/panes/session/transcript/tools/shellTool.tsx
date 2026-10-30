// The shell/exec_command/run_shell_command descriptor (parity checklist
// §2's shellRenderer). A nonzero EXIT is not a tool error (Jesse,
// 2026-10-16: "don't call out failed in lists of tool calls just for nonzero
// exits ... show a small x inline on the call"). The wire stamps an honest
// settled status — "failed" only when the tool RESULT carried an error,
// "completed" otherwise (apptranscript.SettledToolStatus,
// appwire_projection.go:438) — and a command that ran and returned nonzero is
// a clean tool result (empty ItemModel.error, status "completed"). So this
// descriptor does NOT declare the exit as a failure; it exposes the exit code
// through the exitCode hook, which ToolCallItem turns into the small inline
// exit mark via the shared isExitOnly rule. The daemon promotes the process
// exit code onto the item as a typed wire field (ItemModel.exitCode), and when
// that is absent (an old daemon that doesn't populate it) the descriptor falls
// back to the output-footer text heuristic: agent/session_tools_shell.go's
// formatShellResult appends a trailing "[exit <N> · ...]" bracketed footer
// whenever the command wasn't backgrounded and an exit code was captured. The
// heuristic looks only inside the FINAL bracketed segment (never the command's
// own stdout/stderr body) to keep false positives unlikely. ItemModel.error
// carries a denial/failure message when the call itself failed or was denied
// (mapped by reducer.ts's wireItemToModel); it drives the generic failed-row
// treatment in ToolCallItem, a distinct signal from a nonzero exit.

import {
  AnsiTailBuffer,
  parseArgs,
  shellCommand,
  shellOutput,
  shellSummary,
  stripRedundantCd,
  tailFoldNote,
} from "@evener/appwire-client";
import { useRef } from "react";
import { useThreadsStore } from "../../../../stores/threads";
import { useOptionalTranscriptRenderContext } from "../../../../transcriptDisplay/renderContext";
import { CodeBlock, ShellCommandBlock } from "../../../../widgets";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import { TOOL_BODY_MAX_CHARS } from "./bodies";

// shellCommand, stripRedundantCd and the summary: @evener/appwire-client's
// toolSummaries.

// The exit code a command's footer states comes from @evener/appwire-client's
// shellOutput, the one reader of the shell tool's tail (the bracketed
// "[… exit N …]" footer, or the buffered environment's exit_code= trailer),
// which the phone reads too. It reads only the tail, never an "exit_code="
// token a command echoed mid-stream.

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
    buffer.current = { itemId: item.id, sessionRef, live, tail: new AnsiTailBuffer(TOOL_BODY_MAX_CHARS) };
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
    item.exitCode !== undefined && item.exitCode >= 0 && shellOutput(output).exitCode !== item.exitCode
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
          text={live || !tail.truncated ? renderedOutput : `${tailFoldNote(TOOL_BODY_MAX_CHARS)}\n${renderedOutput}`}
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

registerToolRenderer({
  // These three names are mirrored by the shared toolFamily's shell family
  // (@evener/appwire-client's toolSummaries), which toolExitCode reads the exit
  // code under. The two lists have to agree: a name here and not there is a row
  // whose exit the mark would miss.
  match: (name) => name === "shell" || name === "exec_command" || name === "run_shell_command",
  icon: "terminal",
  monoSummary: true,
  fold: "consequential",
  // The exit code is NOT in the summary: a nonzero exit is announced by the
  // row's small inline mark instead (A2 - "exit 1" as the headline made every
  // failure look like a footnote). The number's home is the real text at the
  // tail of the expanded body: formatShellResult bakes "[exit N]" into the
  // captured output itself (agent/session_tools_shell.go) — and when the
  // output carries no trailer of either shape, the body synthesizes the typed
  // code's line instead (see ShellBodyContent's exitFooter).
  summary: shellSummary,
  body: ShellBody,
  // The row summary IS the raw one-line command; the expanded body renders
  // that same command pretty-printed. Showing both on an open row duplicated
  // the call, so while expanded the summary text swaps to this placeholder:
  // the summary line stays (it is the line the disclosure chevron rides) and
  // the collapsed row keeps the command, where it is the only glance at it.
  summaryWhenExpanded: "Ran a shell command",
});
