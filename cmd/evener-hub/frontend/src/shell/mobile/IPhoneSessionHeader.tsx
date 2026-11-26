import { type RefObject, useEffectEvent, useLayoutEffect, useState } from "react";
import { type IPhoneHeader, type IPhoneHeaderAction, iPhoneCommand, tellIPhoneHost } from "../iPhoneHost";

// Floating sheets, menus and popovers have a focus layer. Find and inline
// composer completions keep focus in a text field instead.
const WEB_LAYER = '[data-focus-layer], [data-find-bar], [role="listbox"], [aria-modal="true"]';

function containsLayer(node: Node): boolean {
  return node instanceof Element && (node.matches(WEB_LAYER) || node.querySelector(WEB_LAYER) !== null);
}

type Props = Pick<IPhoneHeader, "id" | "title" | "leading" | "collapsed" | "needsYou"> & {
  bar: RefObject<HTMLDivElement | null>;
  onAction(action: IPhoneHeaderAction): void;
};

/** The web bar stays in layout so both renderers use the same height and
 * the reading scroller keeps its usual collapse compensation. */
export function IPhoneSessionHeader({ bar, id, title, leading, needsYou, collapsed, onAction }: Props) {
  const [height, setHeight] = useState(0);
  useLayoutEffect(() => {
    const element = bar.current;
    if (!element) return;
    const measure = () => setHeight(document.querySelector(WEB_LAYER) ? 0 : element.offsetHeight);
    measure();
    const resize = new ResizeObserver(measure);
    resize.observe(element);
    const layers = new MutationObserver((records) => {
      if (records.some((record) => [...record.addedNodes, ...record.removedNodes].some(containsLayer))) measure();
    });
    layers.observe(document.body, { childList: true, subtree: true });
    return () => {
      resize.disconnect();
      layers.disconnect();
    };
  }, [bar]);

  const receive = useEffectEvent((event: MessageEvent) => {
    const command = iPhoneCommand(event);
    if (command?.type !== "headerAction" || command.id !== id) return;
    const action = command.action;
    if (action === leading) onAction(leading);
    else if (action === "find" || action === "overview") onAction(action);
  });
  const shown = height > 0;
  useLayoutEffect(() => {
    const element = bar.current;
    if (!element || !shown) return;
    element.toggleAttribute("inert", true);
    window.addEventListener("message", receive);
    return () => {
      element.removeAttribute("inert");
      window.removeEventListener("message", receive);
      tellIPhoneHost({ type: "sessionHeader", header: null });
    };
  }, [bar, shown]);
  useLayoutEffect(() => {
    if (height) tellIPhoneHost({ type: "sessionHeader", header: { id, title, leading, needsYou, collapsed, height } });
  }, [id, title, leading, needsYou, collapsed, height]);
  return null;
}
