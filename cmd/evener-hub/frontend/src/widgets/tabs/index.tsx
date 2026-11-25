// Tabs: horizontal underline tabs with roving keyboard focus. Each tab is one
// flush rectangular button anchored to the strip's bottom hairline: no pill or
// button chrome on pointer/touch selection, only the active bottom underline,
// and an inset rectangular :focus-visible ring for keyboard navigation so the
// focus ring sits cleanly inside the cell without rounded corners colliding
// with the straight underline.

import { type KeyboardEvent as ReactKeyboardEvent, useEffect, useRef } from "react";
import { requireClass } from "../internal/requireClass";
import { useRovingRadio } from "../internal/rovingRadio";
import styles from "./tabs.module.css";

export interface TabOption<T extends string = string> {
  value: T;
  label: string;
  /** Optional second line under the label (counts, fractions). Omitted or
   * blank renders a single centered label. */
  detail?: string;
  /** Accessible name when the visible label+detail need spoken context. */
  accessibleLabel?: string;
}

export interface TabsProps<T extends string = string> {
  label: string;
  value: T;
  options: readonly TabOption<T>[];
  onChange: (value: T) => void;
  /** Controlled tabpanel id wired onto each tab's aria-controls. */
  panelId?: string;
  /** Optional id builder wired onto each tab's DOM id. */
  tabDomId?: (value: T) => string;
  /** Move focus to the selected tab when armed (e.g. phone modal Overview). */
  autoFocusSelected?: boolean;
  /** Value change that re-arms autoFocusSelected (such as a surface reopening). */
  focusTrigger?: unknown;
}

const BASE_CLASS = {
  tablist: requireClass(styles.tablist, "tabs.module.css", "tablist"),
  tab: requireClass(styles.tab, "tabs.module.css", "tab"),
  stack: requireClass(styles.stack, "tabs.module.css", "stack"),
  label: requireClass(styles.label, "tabs.module.css", "label"),
  detail: requireClass(styles.detail, "tabs.module.css", "detail"),
};

export function Tabs<T extends string = string>({
  label,
  value,
  options,
  onChange,
  panelId,
  tabDomId,
  autoFocusSelected = false,
  focusTrigger,
}: TabsProps<T>) {
  const tabRefs = useRef<(HTMLButtonElement | null)[]>([]);
  const selectedIndex = options.findIndex((option) => option.value === value);

  function reveal(tab: HTMLButtonElement | null) {
    tab?.scrollIntoView?.({ block: "nearest", inline: "nearest" });
  }

  function choose(index: number, moveFocus: boolean) {
    const option = options[index];
    if (!option) return;
    onChange(option.value);
    const tab = tabRefs.current[index] ?? null;
    if (moveFocus) tab?.focus();
    reveal(tab);
  }

  const move = useRovingRadio({
    items: options,
    mode: "tablist",
    onMove: (index) => choose(index, true),
  });

  // biome-ignore lint/correctness/useExhaustiveDependencies: selectedIndex is read once when entry focus is armed
  useEffect(() => {
    if (!autoFocusSelected) return;
    const frame = requestAnimationFrame(() => {
      const tab = tabRefs.current[selectedIndex] ?? null;
      tab?.focus();
      reveal(tab);
    });
    return () => cancelAnimationFrame(frame);
  }, [autoFocusSelected, focusTrigger]);

  function handleKeyDown(event: ReactKeyboardEvent<HTMLDivElement>) {
    // Modifier chords belong to the global dispatcher; claim only bare keys.
    if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
    move(event, selectedIndex >= 0 ? selectedIndex : 0);
  }

  return (
    <div
      className={BASE_CLASS.tablist}
      role="tablist"
      aria-label={label}
      aria-orientation="horizontal"
      onKeyDown={handleKeyDown}
    >
      {options.map((option, index) => {
        const selected = option.value === value;
        const hasDetail = option.detail !== undefined && option.detail.trim() !== "";
        return (
          <button
            key={option.value}
            ref={(node) => {
              tabRefs.current[index] = node;
            }}
            type="button"
            className={BASE_CLASS.tab}
            role="tab"
            id={tabDomId?.(option.value)}
            aria-selected={selected}
            aria-controls={panelId}
            aria-label={option.accessibleLabel ?? option.label}
            tabIndex={selected ? 0 : -1}
            onClick={() => choose(index, false)}
          >
            <span className={BASE_CLASS.stack}>
              <span className={BASE_CLASS.label}>{option.label}</span>
              {hasDetail ? <span className={BASE_CLASS.detail}>{option.detail}</span> : null}
            </span>
          </button>
        );
      })}
    </div>
  );
}
