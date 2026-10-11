// RawToolOutput is the tool-renderer registry's default body: the output of a
// tool with no descriptor of its own. It renders through CodeBlock so the
// fallback body carries the identical weight, wrapping, font size and inset
// copy control as every descriptor body that already uses it (A4) - a
// same-shaped block is the whole point of a fallback.

import { prettyJSON } from "@evener/appwire-client";
import { CodeBlock } from "../../../widgets";
import type { ToolRenderProps } from "./toolRenderers";

// A tool whose result IS JSON (job_status, job_list, most MCP tools) emits it
// as one compact line, which wraps into an unreadable wall in the block. When
// the whole output is a JSON object or array (@evener/appwire-client's
// prettyJSON, which the phone reads too), display it pretty-printed - the same
// display preparation the shell row's pretty-printed command gets - while the
// copy control still writes the tool's original bytes (copyText, as
// ShellCommandBlock does for the raw command).

export function RawToolOutput({ item }: ToolRenderProps) {
  const output = item.output ?? "";
  if (output === "") return null;
  const pretty = prettyJSON(output);
  if (pretty === undefined || pretty === output) {
    return <CodeBlock text={output} copyLabel="Copy output" />;
  }
  return <CodeBlock text={pretty} copyText={output} language="json" copyLabel="Copy output" />;
}
