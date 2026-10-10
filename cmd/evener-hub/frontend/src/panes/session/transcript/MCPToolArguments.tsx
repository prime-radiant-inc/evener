import { prettyJSON } from "@evener/appwire-client";
import { CodeBlock } from "../../../widgets";
import type { ToolRenderProps } from "./toolRenderers";

export function MCPToolArguments({ item }: ToolRenderProps) {
  const original = item.argumentsJSON;
  if (original === undefined || original.trim() === "") return null;

  const formatted = prettyJSON(original) ?? original;

  return (
    <section aria-label="Tool call arguments">
      <CodeBlock text={formatted} copyText={original} copyLabel="Copy arguments" fold={false} />
    </section>
  );
}
