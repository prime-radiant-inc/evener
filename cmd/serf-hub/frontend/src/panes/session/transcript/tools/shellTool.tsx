// The shell/exec_command/run_shell_command descriptor (parity checklist
// §2's shellRenderer). Failure signal for a settled call: the daemon promotes
// the process exit code onto the item as a typed wire field
// (ItemModel.exitCode), so that structured number is the PRIMARY source for
// both the exit-code summary suffix and autoExpand. Two facts still shape the
// logic: ItemModel.status is ALWAYS "completed" for a finished tool call
// regardless of exit code (internal/appprojector/appwire_projection.go
// hard-codes Status: "completed" on EventToolCallEnd), and ItemModel.error
// carries a denial/failure message when the call itself failed or was denied
// (mapped by reducer.ts's wireItemToModel) — a distinct signal from a nonzero
// exit, which is a command that ran and returned. When exitCode is absent (an
// old daemon that doesn't populate it), the descriptor falls back to the
// output-footer text heuristic below: agent/session_tools_shell.go's
// formatShellResult appends a trailing "[exit <N> · ...]" bracketed footer
// whenever the command wasn't backgrounded and an exit code was captured. The
// heuristic looks only inside the FINAL bracketed segment (never the command's
// own stdout/stderr body) to keep false positives unlikely.

import type { ItemModel } from "../../../../protocol/model";
import { CodeBlock } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import { clip, parseArgs, str, tailFold, tailSlice, trailingBracketFooter } from "./helpers";
import styles from "./shelltool.module.css";

const COMMAND_CLIP = 80;
const TAIL_MAX_CHARS = 8000;

const CLASS = {
  header: requireClass(styles.header, "shelltool.module.css", "header"),
};

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

function ShellBody({ item, live }: ToolRenderProps) {
  const args = parseArgs(item.argumentsJSON);
  const output = item.output ?? "";
  const body = live ? tailSlice(output, TAIL_MAX_CHARS) : tailFold(output, TAIL_MAX_CHARS);
  return (
    <div>
      <div className={CLASS.header}>$ {shellCommand(args)}</div>
      {body !== "" && <CodeBlock text={body} />}
    </div>
  );
}

registerToolRenderer({
  match: (name) => name === "shell" || name === "exec_command" || name === "run_shell_command",
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    const command = clip(shellCommand(args), COMMAND_CLIP);
    const exitCode = shellExitCode(item);
    return exitCode ? `Ran ${command} · exit ${exitCode}` : `Ran ${command}`;
  },
  body: ShellBody,
  autoExpand(item: ItemModel) {
    const exitCode = shellExitCode(item);
    return exitCode !== undefined && exitCode !== 0;
  },
});
