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
}

/**
 * Three-item bottom tab bar. Tabs are `role="tab"` with `aria-selected`.
 * 44px minimum, never hover-dependent.
 */
export function BottomBar({
  tabs,
  activeId,
  onSelect,
}: BottomBarProps): JSX.Element {
  return (
    <nav className="evener-bottombar" aria-label="Primary">
      {tabs.map((tab) => {
        const selected = tab.id === activeId;
        return (
          <button
            key={tab.id}
            type="button"
            role="tab"
            aria-selected={selected}
            className="evener-tab"
            onClick={() => onSelect(tab.id)}
          >
            {tab.glyph}
            <span>{tab.label}</span>
          </button>
        );
      })}
    </nav>
  );
}
