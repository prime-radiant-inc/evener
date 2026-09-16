// The text on either side of an inline element within its parent, walked
// over ALL siblings. ToolRow's tail glue renders a line's final word as its
// own text node (splitTrailingWord inside the TailUnit), so an assertion
// like "the control sits between the anchor prefix and the meta" spans the
// whole run of text around the control, never one sibling node.
export function textAround(el: Element): [before: string, after: string] {
  let before = "";
  for (let node = el.previousSibling; node; node = node.previousSibling) before = (node.textContent ?? "") + before;
  let after = "";
  for (let node = el.nextSibling; node; node = node.nextSibling) after += node.textContent ?? "";
  return [before, after];
}
