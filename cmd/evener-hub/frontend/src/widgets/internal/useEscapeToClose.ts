import { type KeyboardEvent, useCallback } from "react";

/**
 * A keydown handler that closes the layer on Escape. Attach it to the
 * layer's own element, so only an Escape that happens inside the layer
 * closes it. The press is consumed: an enclosing overlay that also closes on
 * Escape (a Dialog this widget is nested in) must not close on the same key.
 * Returns whether this press was the Escape it handled.
 */
export function useEscapeToClose(onClose: () => void): (event: KeyboardEvent<HTMLElement>) => boolean {
  return useCallback(
    (event: KeyboardEvent<HTMLElement>) => {
      if (event.key !== "Escape") return false;
      event.preventDefault();
      event.stopPropagation();
      onClose();
      return true;
    },
    [onClose],
  );
}
