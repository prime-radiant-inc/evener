/** Finds a detail strip's meta line by its COMPLETE text. The delegate line's
 * id renders through the shared entity trigger (an element, not a bare run of
 * text within the line), so a text-node query would only ever see the
 * fragments around it; the whole-text comparison asserts the line reads
 * exactly as it always has. `root` is the caller's own render container when
 * it has one, and the whole document otherwise. */
export function detailLineByText(text: string, root: ParentNode = document): HTMLElement {
  const line = [...root.querySelectorAll<HTMLElement>("span")].find((element) => element.textContent === text);
  if (line === undefined) throw new Error(`no detail line reads ${text}`);
  return line;
}
