// Pure logic behind SelectionQuote.tsx, kept free of the DOM-event/selection
// glue (jsdom's own selection APIs are too thin to drive honestly - see that
// file's header comment) so quote formatting, message-content containment,
// and floating-bar clamping are each testable directly.

/**
 * Turns a raw text-selection string into a markdown blockquote: outer
 * whitespace-only lines are dropped, every remaining line gets its own
 * "> " prefix (an internal blank line becomes a bare "> "), and the block
 * ends with one blank line so it reads as its own paragraph once inserted
 * ahead of whatever the composer already holds. An empty/whitespace-only
 * selection formats to "" - callers never insert a lone blank block.
 */
export function formatQuoteBlock(selectedText: string): string {
  const trimmed = selectedText.trim();
  if (trimmed === "") return "";
  const lines = trimmed.split(/\r\n|\n/);
  return `${lines.map((line) => `> ${line}`).join("\n")}\n\n`;
}

/**
 * Walks from `node` up toward (but never including) `container`, returning
 * the nearest ancestor element marked `data-view-anchor-message="true"` -
 * the same attribute TurnBlock.tsx and Session.tsx already stamp on every
 * message item's wrapper (both view modes) so this file never needs its own
 * marker convention or a change to transcript/messages/'s renderers. Returns
 * null when the walk reaches `container` (or leaves the tree entirely)
 * without finding one - selections in transcript chrome (dividers, the
 * load-older row, action-group summaries) or in a message wrapper stamped
 * "false" (a non-message item, e.g. a tool call) correctly report "not
 * message content".
 */
export function messageContentElement(node: Node | null, container: HTMLElement): HTMLElement | null {
  if (!node || node === container || !container.contains(node)) return null;
  let current: Node | null = node;
  while (current && current !== container) {
    if (current instanceof HTMLElement && current.getAttribute("data-view-anchor-message") === "true") {
      return current;
    }
    current = current.parentNode;
  }
  return null;
}

export interface ClampSize {
  width: number;
  height: number;
}

/**
 * Clamps a proposed top-left position so an element of `size` stays fully
 * within a `bounds`-sized box anchored at (0, 0) - the caller translates
 * both `pos` and `bounds` into the same coordinate space first (SelectionQuote.tsx
 * uses the pane's own viewport rect). `padding` keeps the element off the
 * exact edge; when `size` alone exceeds `bounds` (a narrow pane, a wide
 * bar), the floor wins over a negative max, same as a real CSS clamp would
 * read once one side of the range no longer fits.
 */
export function clampToBounds(
  pos: { x: number; y: number },
  size: ClampSize,
  bounds: ClampSize,
  padding = 8,
): { x: number; y: number } {
  const maxX = Math.max(padding, bounds.width - size.width - padding);
  const maxY = Math.max(padding, bounds.height - size.height - padding);
  return {
    x: Math.min(Math.max(pos.x, padding), maxX),
    y: Math.min(Math.max(pos.y, padding), maxY),
  };
}
