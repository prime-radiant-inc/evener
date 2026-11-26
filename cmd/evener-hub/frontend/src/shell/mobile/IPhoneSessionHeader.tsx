import { type RefObject, useEffectEvent, useLayoutEffect, useState } from "react";
import { type IPhoneHeader, type IPhoneHeaderAction, iPhoneCommand, tellIPhoneHost } from "../iPhoneHost";

// Floating layers cover the toolbar. Inline Find docks beneath it.
const WEB_LAYER = '[data-focus-layer], [role="listbox"], [aria-modal="true"]';
const TOOLBAR_SURFACES = `${WEB_LAYER}, [data-find-bar]`;

function containsLayer(node: Node): boolean {
  return node instanceof Element && (node.matches(TOOLBAR_SURFACES) || node.querySelector(TOOLBAR_SURFACES) !== null);
}

type Props = Pick<IPhoneHeader, "id" | "title" | "leading" | "collapsed" | "needsYou"> & {
  bar: RefObject<HTMLDivElement | null>;
  edgeToolbar: boolean;
  onFindChange(open: boolean): void;
  onAction(action: IPhoneHeaderAction): void;
};

/** The web bar stays in layout so both renderers use the same height and
 * the reading scroller keeps its usual collapse compensation. */
export function IPhoneSessionHeader({
  bar,
  id,
  title,
  leading,
  needsYou,
  collapsed,
  edgeToolbar,
  onAction,
  onFindChange,
}: Props) {
  const [height, setHeight] = useState(0);
  useLayoutEffect(() => {
    const element = bar.current;
    if (!element) return;
    const measure = () => {
      onFindChange(document.querySelector("[data-find-bar]") !== null);
      setHeight(document.querySelector(WEB_LAYER) ? 0 : element.offsetHeight);
    };
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
      onFindChange(false);
    };
  }, [bar, onFindChange]);

  const receive = useEffectEvent((event: MessageEvent) => {
    const command = iPhoneCommand(event);
    if (command?.type !== "headerAction" || command.id !== id) return;
    const action = command.action;
    if (leading && action === leading) onAction(leading);
    else if (action === "find" || action === "overview") onAction(action);
  });
  const shown = height > 0;
  useLayoutEffect(() => {
    const element = bar.current;
    if (!element || !shown) return;
    element.toggleAttribute("inert", !edgeToolbar);
    element.toggleAttribute("data-system-edge-actions", edgeToolbar);
    return () => {
      element.removeAttribute("inert");
      element.removeAttribute("data-system-edge-actions");
    };
  }, [bar, shown, edgeToolbar]);
  useLayoutEffect(() => {
    if (!shown) return;
    window.addEventListener("message", receive);
    return () => {
      window.removeEventListener("message", receive);
      tellIPhoneHost({ type: "sessionHeader", header: null });
    };
  }, [shown]);
  useLayoutEffect(() => {
    if (height) tellIPhoneHost({ type: "sessionHeader", header: { id, title, leading, needsYou, collapsed, height } });
  }, [id, title, leading, needsYou, collapsed, height]);
  return null;
}
