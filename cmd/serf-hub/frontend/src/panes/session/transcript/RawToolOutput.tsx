// RawToolOutput is the tool-renderer registry's default body: the output of a
// tool with no descriptor of its own. It renders through CodeBlock so the
// fallback body carries the identical weight, wrapping, font size and inset
// copy control as every descriptor body that already uses it (A4) - a
// same-shaped block is the whole point of a fallback.

import { CodeBlock } from "../../../widgets";
import type { ToolRenderProps } from "./toolRenderers";

// A tool whose result IS JSON (job_status, job_list, most MCP tools) emits it
// as one compact line, which wraps into an unreadable wall in the block. When
// the whole output parses as JSON, display it pretty-printed - the same
// display preparation the shell row's pretty-printed command gets - while the
// copy control still writes the tool's original bytes (copyText, as
// ShellCommandBlock does for the raw command).
function prettyPrintJson(output: string): string | undefined {
  try {
    return JSON.stringify(JSON.parse(output), null, 2);
  } catch {
    return undefined;
  }
}

export function RawToolOutput({ item }: ToolRenderProps) {
  const output = item.output ?? "";
  if (output === "") return null;
  const pretty = prettyPrintJson(output);
  if (pretty === undefined || pretty === output) {
    return <CodeBlock text={output} copyLabel="Copy output" />;
  }
  return <CodeBlock text={pretty} copyText={output} language="json" copyLabel="Copy output" />;
}
