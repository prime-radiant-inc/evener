// The shell/exec_command/run_shell_command descriptor (parity checklist
// §2's shellRenderer). Ground truth on the failure signal (this
// descriptor's autoExpand is the one place the checklist specifically asks
// for it): ItemModel.status is ALWAYS "completed" for a finished tool call
// regardless of exit code (internal/appprojector/appwire_projection.go
// hard-codes Status: "completed" on EventToolCallEnd, never conditional on
// the call's own error), and ThreadItem.error - which DOES carry the
// failure text on the wire - is dropped by protocol/reducer.ts's
// wireItemToModel before it ever reaches a component. Neither field is a
// usable failure signal here. The only remaining signal is the shell
// tool's own output TEXT: agent/session_tools_shell.go's formatShellResult
// appends a trailing "[exit <N> · ...]" bracketed footer whenever the
// command wasn't backgrounded and an exit code was captured. This
// descriptor parses THAT footer - a text-pattern heuristic coupled to the
// Go formatter's current wording, not a structured wire field - documented
// here and in the wave-4 task-3 report rather than invented silently. It
// intentionally only looks inside the FINAL bracketed segment (never the
// command's own stdout/stderr body) to keep false positives unlikely.
import { registerToolRenderer } from "../toolRenderers";
import type { ToolRenderProps } from "../toolRenderers";
import { CodeBlock } from "../../../../widgets";
import { clip, rememberedArgs, str, tailFold, tailSlice, trailingBracketFooter } from "./helpers";
import type { ItemModel } from "../../../../protocol/model";
import styles from "./shelltool.module.css";
import { requireClass } from "../../../../widgets/internal/requireClass";

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
// path), falling back to the buffered-execenv trailer above - see this
// file's own header for why this is a text heuristic, not a structured
// field. Returns undefined for a backgrounded/still-running command (no
// trailer of either shape yet).
function parseShellExitCode(output: string): number | undefined {
  const footer = trailingBracketFooter(output);
  if (footer !== undefined) {
    const bracketed = /\bexit (-?\d+)\b/.exec(footer);
    if (bracketed) return Number(bracketed[1]);
  }
  const buffered = BUFFERED_EXIT_CODE_RE.exec(output);
  return buffered ? Number(buffered[1]) : undefined;
}

function ShellBody({ item, live }: ToolRenderProps) {
  const args = rememberedArgs(item);
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
    const args = rememberedArgs(item);
    const command = clip(shellCommand(args), COMMAND_CLIP);
    const exitCode = parseShellExitCode(item.output ?? "");
    return exitCode ? `Ran ${command} · exit ${exitCode}` : `Ran ${command}`;
  },
  body: ShellBody,
  autoExpand(item: ItemModel) {
    const exitCode = parseShellExitCode(item.output ?? "");
    return exitCode !== undefined && exitCode !== 0;
  },
});
