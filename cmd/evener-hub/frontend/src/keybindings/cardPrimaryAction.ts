// The keyboard primary action every interactive card shares: Enter with the
// platform modifier (Meta/Ctrl) invokes the card's one action - unless the
// event comes from a focused link, whose Enter the browser keeps
// (Ctrl/Cmd+Enter is the platform's open-in-new-tab chord). AskDock's ask
// batch and the sandbox escalation card both carry this chord; keeping it
// in one place keeps the guards identical - an IME composition's confirm
// Enter is text input, never activation, and a chord the card consumed
// must not also reach a window-level binding.
import { type KeyboardEvent as ReactKeyboardEvent, useCallback } from "react";
import { isIMECompositionKeydown } from "./dispatcher";

/** True for the card primary-action chord itself: Enter + Meta/Ctrl. */
export function isPrimaryActionKey(event: Pick<KeyboardEvent, "key" | "metaKey" | "ctrlKey">): boolean {
  return event.key === "Enter" && (event.metaKey || event.ctrlKey);
}

export interface CardPrimaryActionOptions {
  onPrimary(): void;
  /** False while the action is unavailable (a disabled footer button, a
   * resolved card). The chord is still consumed, but nothing runs. */
  enabled: boolean;
  /** A card with a single-line text field can also accept a bare Enter from
   * that field; the caller decides from its own affordance marker. */
  isBareEnterPrimary?(event: ReactKeyboardEvent): boolean;
}

export function useCardPrimaryAction({ onPrimary, enabled, isBareEnterPrimary }: CardPrimaryActionOptions) {
  return useCallback(
    (event: ReactKeyboardEvent) => {
      if (event.key !== "Enter" || isIMECompositionKeydown(event.nativeEvent)) return;
      // A focused link's Enter belongs to the browser: Ctrl/Cmd+Enter opens
      // the link in a new tab, and a card whose prose carries a link (ask
      // questions render markdown) must not convert that into its primary
      // action. Card prose can also put focus inside a link's subtree, so
      // the check walks ancestors, not just the target itself.
      if ((event.target as HTMLElement | null)?.closest?.("a") != null) return;
      if (!isPrimaryActionKey(event) && !(isBareEnterPrimary?.(event) ?? false)) return;
      event.preventDefault();
      event.stopPropagation();
      if (enabled) onPrimary();
    },
    [enabled, isBareEnterPrimary, onPrimary],
  );
}
