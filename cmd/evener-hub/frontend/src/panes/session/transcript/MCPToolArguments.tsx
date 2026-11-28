import { JSONCodeBlock } from "./JSONCodeBlock";
import type { ToolRenderProps } from "./toolRenderers";

export function MCPToolArguments({ item }: ToolRenderProps) {
  const original = item.argumentsJSON;
  if (original === undefined || original.trim() === "") return null;

  return (
    <section aria-label="Tool call arguments">
      <JSONCodeBlock text={original} copyLabel="Copy arguments" fold={false} />
    </section>
  );
}
