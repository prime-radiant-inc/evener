import { prettyJSON } from "@evener/appwire-client";
import { CodeBlock } from "../../../widgets";
import type { ToolRenderProps } from "./toolRenderers";

export function MCPToolArguments({ item }: ToolRenderProps) {
  const original = item.argumentsJSON;
  const raw = original?.trim();
  if (!raw) return null;

  const formatted = prettyJSON(original ?? raw) ?? original ?? raw;

  return (
    <section aria-label="Tool call arguments">
      <CodeBlock text={formatted} copyText={original ?? raw} copyLabel="Copy arguments" fold={false} />
    </section>
  );
}
