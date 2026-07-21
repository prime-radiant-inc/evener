// ToolCallItem is the item renderer for tool-call items - the wire's
// "commandExecution" ThreadItem.type (internal/appprojector/
// appwire_projection.go; the same mapping reducer.ts's isAskUserItem cites).
// It dispatches into the tool-renderer registry (toolRenderers.ts) by
// ItemModel.toolName: T1 ships only the registry + the raw-output default
// descriptor (toolRenderers.ts's DEFAULT_DESCRIPTOR), T3 fills in the real
// per-tool descriptors.
import { registerItemRenderer, type ItemRenderProps } from "./types";
import { toolRendererFor } from "./toolRenderers";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./toolcallitem.module.css";

const CLASS = {
  call: requireClass(styles.call, "toolcallitem.module.css", "call"),
  summary: requireClass(styles.summary, "toolcallitem.module.css", "summary"),
};

export function ToolCallItem({ item, live }: ItemRenderProps) {
  const descriptor = toolRendererFor(item.toolName ?? "");
  const Body = descriptor.body;
  return (
    <div className={CLASS.call} data-testid="tool-call-item" data-tool-name={item.toolName ?? ""}>
      <span className={CLASS.summary}>{descriptor.summary(item)}</span>
      {Body && <Body item={item} live={live} />}
    </div>
  );
}

registerItemRenderer("commandExecution", ToolCallItem);
