import { CodeBlock } from "../../../widgets";
import type { ToolRenderProps } from "./toolRenderers";

function prettyPrintArguments(original: string): string {
  try {
    return JSON.stringify(JSON.parse(original), null, 2);
  } catch {
    return original;
  }
}

export function MCPToolArguments({ item }: ToolRenderProps) {
  const original = item.argumentsJSON;
  const raw = original?.trim();
  if (!raw) return null;

  const formatted = prettyPrintArguments(original ?? raw);

  return (
    <section aria-label="Tool call arguments">
      <CodeBlock text={formatted} copyText={original ?? raw} copyLabel="Copy arguments" fold={false} />
    </section>
  );
}
