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
import { clip, rememberedArgs, str, tailFold, tailSlice } from "./helpers";
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

// parseShellExitCode reads the trailing "[... exit <N> ...]" footer
// formatShellResult appends - see this file's own header for why this is a
// text heuristic, not a structured field. Returns undefined for a
// backgrounded/still-running command (no footer yet) or any output that
// doesn't end in a bracketed segment.
function parseShellExitCode(output: string): number | undefined {
  const trimmed = output.trimEnd();
  if (!trimmed.endsWith("]")) return undefined;
  const openIdx = trimmed.lastIndexOf("[");
  if (openIdx === -1) return undefined;
  const footer = trimmed.slice(openIdx);
  const match = /\bexit (-?\d+)\b/.exec(footer);
  return match ? Number(match[1]) : undefined;
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
