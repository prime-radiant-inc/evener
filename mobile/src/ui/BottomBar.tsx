import type { JSX, ReactNode } from "react";

export interface BottomTab {
  readonly id: string;
  readonly label: string;
  readonly glyph: ReactNode;
}

export interface BottomBarProps {
  readonly tabs: readonly BottomTab[];
  readonly activeId: string;
  readonly onSelect: (id: string) => void;
  readonly panelId: string;
}

/**
 * Three-item bottom tab bar. The container uses `role="tablist"`, each tab
 * uses `role="tab"` with `aria-selected` and `aria-controls` pointing to the
 * associated panel. 44px minimum, never hover-dependent.
 *
 * A `<div role="tablist">` inside `<nav>` is used because `<nav>` has an
 * implicit `navigation` role that some ARIA implementations do not override
 * with an explicit `tablist` role.
 */
export function BottomBar({
  tabs,
  activeId,
  onSelect,
  panelId,
}: BottomBarProps): JSX.Element {
  return (
    <nav className="evener-bottombar" aria-label="Primary">
      <div
        role="tablist"
        aria-label="Primary"
        className="evener-bottombar__tablist"
      >
        {tabs.map((tab) => {
          const selected = tab.id === activeId;
          return (
            <button
              key={tab.id}
              type="button"
              role="tab"
              aria-selected={selected}
              aria-controls={panelId}
              id={`${panelId}-tab-${tab.id}`}
              className="evener-tab"
              onClick={() => onSelect(tab.id)}
            >
              {tab.glyph}
              <span>{tab.label}</span>
            </button>
          );
        })}
      </div>
    </nav>
  );
}
