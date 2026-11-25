import { type KeyboardEvent, useCallback } from "react";

export interface EscapeToCloseOptions {
  /** Stop the event from also reaching an enclosing overlay that closes on
   * Escape (a Dialog this widget is nested in). */
  stopPropagation?: boolean;
}

/**
 * A keydown handler that closes the layer on Escape. Attach it to the
 * layer's own element, so only an Escape that happens inside the layer
 * closes it. Returns whether this press was the Escape it handled.
 */
export function useEscapeToClose(
  onClose: () => void,
  { stopPropagation = false }: EscapeToCloseOptions = {},
): (event: KeyboardEvent<HTMLElement>) => boolean {
  return useCallback(
    (event: KeyboardEvent<HTMLElement>) => {
      if (event.key !== "Escape") return false;
      event.preventDefault();
      if (stopPropagation) event.stopPropagation();
      onClose();
      return true;
    },
    [onClose, stopPropagation],
  );
}
