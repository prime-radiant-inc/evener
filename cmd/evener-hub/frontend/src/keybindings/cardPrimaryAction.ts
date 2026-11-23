// The keyboard primary action every interactive card shares: Enter with the
// platform modifier (Meta/Ctrl) invokes the card's one action. AskDock's ask
// batch and the sandbox escalation card both carry this chord; keeping it in
// one place keeps the guards identical - an IME composition's confirm Enter
// is text input, never activation, and a chord the card consumed must not
// also reach a window-level binding.
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
      if (!isPrimaryActionKey(event) && !(isBareEnterPrimary?.(event) ?? false)) return;
      event.preventDefault();
      event.stopPropagation();
      if (enabled) onPrimary();
    },
    [enabled, isBareEnterPrimary, onPrimary],
  );
}
