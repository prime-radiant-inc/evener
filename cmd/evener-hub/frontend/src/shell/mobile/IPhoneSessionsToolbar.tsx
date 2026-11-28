import { type RefObject, useEffectEvent, useId, useLayoutEffect } from "react";
import { FLOATING_WEB_LAYER, iPhoneCommand, tellIPhoneHost } from "../iPhoneHost";
import { useOpenRailDestination } from "../rail/RailNavigationActions";

export function IPhoneSessionsToolbar({
  mode,
  panelAnchor,
  onToggle,
  onBack,
  destination,
  needsYou = 0,
}: {
  mode: "list" | "launcher" | "navigation";
  panelAnchor?: RefObject<HTMLSpanElement | null>;
  destination?: "open" | "board" | null;
  needsYou?: number;
  onBack?: () => void;
  onToggle(): void;
}) {
  const id = useId();
  const toggle = useEffectEvent(onToggle);
  const back = useEffectEvent(() => onBack?.());
  const canGoBack = !!onBack;
  const openRailDestination = useEffectEvent(useOpenRailDestination());
  useLayoutEffect(() => {
    // Only the foreground sheet can act. Its row menus and confirmations
    // release the side buttons until their own focus layer closes.
    const ownsActions = () => {
      const layers = document.querySelectorAll(`${FLOATING_WEB_LAYER}:not([inert], [inert] *)`);
      const front = layers.item(layers.length - 1);
      return mode !== "list" ? !front : !!front && panelAnchor?.current?.closest(FLOATING_WEB_LAYER) === front;
    };
    // On entry the title announces the list. Otherwise its first web control,
    // Filter, would receive automatic focus and summon the iPhone keyboard.
    const panel = panelAnchor?.current?.closest<HTMLElement>('[role="dialog"]');
    if (mode === "list" && panel && ownsActions() && !panel.contains(document.activeElement)) {
      panel.focus({ preventScroll: true });
    }
    let visible: boolean | undefined;
    const publish = () => {
      const active = ownsActions();
      if (active === visible) return;
      visible = active;
      if (!active) tellIPhoneHost({ type: "sessionsToolbar", id, mode: null });
      else if (mode === "navigation")
        tellIPhoneHost({
          type: "sessionsToolbar",
          id,
          mode,
          back: canGoBack,
          destination: destination ?? null,
          needsYou,
        });
      else tellIPhoneHost({ type: "sessionsToolbar", id, mode });
    };
    const layers = new MutationObserver((records) => {
      if (
        records.some(
          (record) =>
            record.type === "attributes" ||
            [...record.addedNodes, ...record.removedNodes].some(
              (node) =>
                node instanceof Element && (node.matches(FLOATING_WEB_LAYER) || node.querySelector(FLOATING_WEB_LAYER)),
            ),
        )
      )
        publish();
    });
    layers.observe(document.body, {
      subtree: true,
      childList: true,
      attributes: true,
      attributeFilter: ["inert", "aria-modal"],
    });
    const receive = (event: MessageEvent) => {
      const command = iPhoneCommand(event);
      if (command?.type !== "sessionsAction" || command.id !== id || !ownsActions()) return;
      if (mode === "navigation") {
        if (command.action === "back" && canGoBack) back();
        else if (destination && command.action === destination) toggle();
      } else if (command.action === (mode === "list" ? "close" : "open")) toggle();
      else if (mode === "list" && (command.action === "hubs" || command.action === "settings")) {
        openRailDestination(command.action);
      }
    };
    window.addEventListener("message", receive);
    publish();
    return () => {
      layers.disconnect();
      window.removeEventListener("message", receive);
    };
  }, [id, mode, panelAnchor, canGoBack, destination, needsYou]);
  // A new badge or Board state updates the same native toolbar in place.
  useLayoutEffect(() => () => tellIPhoneHost({ type: "sessionsToolbar", id, mode: null }), [id]);
  return null;
}
