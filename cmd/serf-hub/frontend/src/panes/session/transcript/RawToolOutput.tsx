// RawToolOutput is the tool-renderer registry's default body: the output of a
// tool with no descriptor of its own. It renders through CodeBlock so the
// fallback body carries the identical weight, wrapping, font size and inset
// copy control as every descriptor body that already uses it (A4) - a
// same-shaped block is the whole point of a fallback.

import { CodeBlock } from "../../../widgets";
import type { ToolRenderProps } from "./toolRenderers";

export function RawToolOutput({ item }: ToolRenderProps) {
  const output = item.output ?? "";
  if (output === "") return null;
  return <CodeBlock text={output} copyLabel="Copy output" />;
}
