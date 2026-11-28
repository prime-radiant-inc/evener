// Shared arrow/Home/End navigation for the app's roving-tabindex groups:
// RadioGroup, SegmentedControl, Menu, and AskDock's question tablist. Each
// carried the same move-focus-and-select loop; only the skip rules and the key
// set differ, so the hook takes the focused item's index from the caller and
// offers a horizontal-only tablist mode and a vertical-only menu mode.
import { type KeyboardEvent as ReactKeyboardEvent, useCallback } from "react";

// Items come from several shapes (radio options, segmented options, ask
// questions, menu entries); the helpers read only an optional `disabled` flag
// by default, so they accept any object rather than forcing callers to share a
// type or an index signature.
export type RovingItem = object;

/** Which items a key may land on. RadioGroup and SegmentedControl skip only
 * disabled options; Menu also skips separators. */
export type ItemIsEnabled<T extends RovingItem> = (item: T) => boolean;

function isEnabledByDefault<T extends RovingItem>(item: T): boolean {
  return !("disabled" in item && item.disabled === true);
}

export function firstEnabledIndex<T extends RovingItem>(
  items: readonly T[],
  isEnabled: ItemIsEnabled<T> = isEnabledByDefault,
): number {
  return items.findIndex((item) => isEnabled(item));
}

export function lastEnabledIndex<T extends RovingItem>(
  items: readonly T[],
  isEnabled: ItemIsEnabled<T> = isEnabledByDefault,
): number {
  for (let index = items.length - 1; index >= 0; index--) {
    const item = items[index];
    if (item !== undefined && isEnabled(item)) return index;
  }
  return -1;
}

// Next enabled index stepping by `delta`, wrapping at the ends; `from` itself
// when every item is disabled, so the keydown stays a no-op.
export function stepEnabledIndex<T extends RovingItem>(
  items: readonly T[],
  from: number,
  delta: 1 | -1,
  isEnabled: ItemIsEnabled<T> = isEnabledByDefault,
): number {
  const count = items.length;
  if (count === 0) return -1;
  let index = from;
  for (let step = 0; step < count; step++) {
    index = (index + delta + count) % count;
    const item = items[index];
    if (item !== undefined && isEnabled(item)) return index;
  }
  return from;
}

export type RovingMode = "radio" | "tablist" | "menu";

export interface RovingRadioOptions<T extends RovingItem> {
  items: readonly T[];
  onMove(index: number): void;
  /** radio (the default) also answers ArrowUp/Down; tablist (AskDock's
   * question strip) is horizontal-only; menu is vertical-only. All wrap at
   * the ends. */
  mode?: RovingMode;
  /** Defaults to skipping items whose `disabled` is true. Menu passes its own
   * predicate so separators are skipped too. */
  isEnabled?: ItemIsEnabled<T>;
}

export function useRovingRadio<T extends RovingItem>({
  items,
  onMove,
  mode = "radio",
  isEnabled = isEnabledByDefault,
}: RovingRadioOptions<T>) {
  return useCallback(
    (event: ReactKeyboardEvent, from: number) => {
      // From "no item focused" - Menu's popup after the focused row is
      // removed - a forward key goes to the first enabled item and a backward
      // one to the last, rather than stepping away from an index that is not
      // the reader's place.
      const step = (delta: 1 | -1) =>
        from < 0
          ? delta === 1
            ? firstEnabledIndex(items, isEnabled)
            : lastEnabledIndex(items, isEnabled)
          : stepEnabledIndex(items, from, delta, isEnabled);
      let next: number;
      switch (event.key) {
        case "ArrowRight":
          if (mode === "menu") return;
          next = step(1);
          break;
        case "ArrowLeft":
          if (mode === "menu") return;
          next = step(-1);
          break;
        case "ArrowDown":
          if (mode === "tablist") return;
          next = step(1);
          break;
        case "ArrowUp":
          if (mode === "tablist") return;
          next = step(-1);
          break;
        case "Home":
          next = mode === "tablist" ? 0 : firstEnabledIndex(items, isEnabled);
          break;
        case "End":
          next = mode === "tablist" ? items.length - 1 : lastEnabledIndex(items, isEnabled);
          break;
        default:
          return;
      }
      event.preventDefault();
      if (next !== -1) onMove(next);
    },
    [items, isEnabled, mode, onMove],
  );
}
