// Shared arrow/Home/End navigation for the app's roving-tabindex groups:
// RadioGroup, SegmentedControl, and AskDock's question tablist. Each carried
// the same move-focus-and-select loop; only the disabled-option rules and the
// key set differ, so the hook takes the focused item's index from the caller
// and offers a horizontal-only tablist mode.
import { type KeyboardEvent as ReactKeyboardEvent, useCallback } from "react";

// Items come from several shapes (radio options, segmented options, ask
// questions); the helper reads only an optional `disabled` flag, so it accepts
// any object rather than forcing callers to share a type or an index
// signature.
export type RovingItem = object;

function isDisabled(item: RovingItem): boolean {
  return "disabled" in item && item.disabled === true;
}

export function firstEnabledIndex(items: readonly RovingItem[]): number {
  return items.findIndex((item) => !isDisabled(item));
}

export function lastEnabledIndex(items: readonly RovingItem[]): number {
  for (let index = items.length - 1; index >= 0; index--) {
    const item = items[index];
    if (item !== undefined && !isDisabled(item)) return index;
  }
  return -1;
}

// Next enabled index stepping by `delta`, wrapping at the ends; `from` itself
// when every item is disabled, so the keydown stays a no-op.
export function stepEnabledIndex(items: readonly RovingItem[], from: number, delta: 1 | -1): number {
  const count = items.length;
  if (count === 0) return -1;
  let index = from;
  for (let step = 0; step < count; step++) {
    index = (index + delta + count) % count;
    const item = items[index];
    if (item !== undefined && !isDisabled(item)) return index;
  }
  return from;
}

export type RovingMode = "radio" | "tablist";

export interface RovingRadioOptions {
  items: readonly RovingItem[];
  onMove(index: number): void;
  /** radio (the default) also answers ArrowUp/Down; tablist (AskDock's
   * question strip) is horizontal-only. Both wrap at the ends. */
  mode?: RovingMode;
}

export function useRovingRadio({ items, onMove, mode = "radio" }: RovingRadioOptions) {
  return useCallback(
    (event: ReactKeyboardEvent, from: number) => {
      let next: number;
      switch (event.key) {
        case "ArrowRight":
          next = stepEnabledIndex(items, from, 1);
          break;
        case "ArrowLeft":
          next = stepEnabledIndex(items, from, -1);
          break;
        case "ArrowDown":
          if (mode === "tablist") return;
          next = stepEnabledIndex(items, from, 1);
          break;
        case "ArrowUp":
          if (mode === "tablist") return;
          next = stepEnabledIndex(items, from, -1);
          break;
        case "Home":
          next = mode === "tablist" ? 0 : firstEnabledIndex(items);
          break;
        case "End":
          next = mode === "tablist" ? items.length - 1 : lastEnabledIndex(items);
          break;
        default:
          return;
      }
      event.preventDefault();
      if (next !== -1) onMove(next);
    },
    [items, mode, onMove],
  );
}
