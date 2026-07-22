// ToolCallItem is the item renderer for tool-call items - the wire's
// "commandExecution" ThreadItem.type (internal/appprojector/
// appwire_projection.go). It dispatches into the tool-renderer registry
// (toolRenderers.ts) by ItemModel.toolName, which pairs a raw-output default
// descriptor (toolRenderers.ts's DEFAULT_DESCRIPTOR) with the real per-tool
// descriptors registered under tools/.
import { memo, useLayoutEffect, useRef, useState } from "react";
import { requireClass } from "../../../widgets/internal/requireClass";
import { ImageGallery } from "./flow/ImageGallery";
import styles from "./toolcallitem.module.css";
import { toolRendererFor } from "./toolRenderers";
import { type ItemRenderProps, ignoringTurn, registerItemRenderer } from "./types";

const CLASS = {
  call: requireClass(styles.call, "toolcallitem.module.css", "call"),
  summary: requireClass(styles.summary, "toolcallitem.module.css", "summary"),
};

// Memoized ignoring `turn` identity (types.ts's ignoringTurn): this
// component never reads `turn` at all (only `item`/`live`, destructured
// below - the descriptor's Body only ever gets `item`/`live` too, see
// toolRenderers.ts's ToolRenderProps), so a fresh turn object on every
// streaming delta targeting a DIFFERENT item must not re-render an
// already-settled tool call.
export const ToolCallItem = memo(function ToolCallItem({ item, live }: ItemRenderProps) {
  const descriptor = toolRendererFor(item.toolName ?? "");
  const Body = descriptor.body;
  // outputImages is a generic ItemModel field any tool call can carry (the
  // wire's ToolCallEndData.OutputImages, agent/events/payloads.go), not
  // owned by any one descriptor - rendered here, once, so every current and
  // future tool gets it for free rather than each body wiring it in itself.
  const hasOutputImages = (item.outputImages?.length ?? 0) > 0;

  // Every row with a body starts collapsed (parity-m4-transcript.md's own
  // Highlights: "every tool row, including diffs, starts collapsed" - the
  // only default-expanded state anywhere is a failed shell call once it
  // settles, via descriptor.autoExpand). autoExpand only means anything
  // once the call has actually finished (e.g. shell's own exit-code
  // heuristic can't resolve mid-stream), so this is applied exactly once,
  // at the live -> settled transition - never on every render, which is
  // also what lets a user's own manual toggle afterward stick rather than
  // being fought back open/closed.
  const [expanded, setExpanded] = useState(false);
  const userToggledRef = useRef(false);

  // biome-ignore lint/correctness/useExhaustiveDependencies: deliberately edge-triggered on live only, see the comment inside
  useLayoutEffect(() => {
    if (live || userToggledRef.current) return;
    setExpanded(descriptor.autoExpand?.(item) ?? false);
    // Edge-triggered on the live -> settled transition (and on an
    // already-settled initial mount) - deliberately NOT depending on
    // `item`/`descriptor` too, so a settled row's later re-renders never
    // re-run this and re-fight a manual toggle.
  }, [live]);

  if (!Body && !hasOutputImages) {
    return (
      <div className={CLASS.call} data-testid="tool-call-item" data-tool-name={item.toolName ?? ""}>
        <span className={CLASS.summary}>{descriptor.summary(item)}</span>
      </div>
    );
  }

  return (
    <details className={CLASS.call} data-testid="tool-call-item" data-tool-name={item.toolName ?? ""} open={expanded}>
      {/* biome-ignore lint/a11y/noStaticElementInteractions: <summary> is natively keyboard-operable (implicit role="button", Enter/Space already synthesize this same click) */}
      <summary
        className={CLASS.summary}
        onClick={(e) => {
          // Fully controlled rather than relying on <details>' own native
          // toggle: preventDefault stops the browser from also flipping
          // `open` itself, so `expanded` state stays the single source of
          // truth. The user's own choice, once made, always wins over
          // autoExpand from here on (see the effect above).
          e.preventDefault();
          userToggledRef.current = true;
          setExpanded((prev) => !prev);
        }}
      >
        {descriptor.summary(item)}
      </summary>
      {Body && <Body item={item} live={live} />}
      <ImageGallery images={item.outputImages} />
    </details>
  );
}, ignoringTurn);

registerItemRenderer("commandExecution", ToolCallItem);
