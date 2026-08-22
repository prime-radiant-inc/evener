import type { JSX, KeyboardEvent, ReactNode } from "react";

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
 *
 * Keyboard: roving tabindex — the active tab is the only tabbable tab
 * (`tabindex=0`); the rest are `tabindex=-1`. ArrowLeft/ArrowRight (and
 * ArrowUp/ArrowDown) move selection and focus between tabs, wrapping at the
 * ends. Home/End select and focus the first/last tab. Handled keys call
 * `preventDefault`; unhandled keys are left alone. Click behavior is
 * unchanged. The glyph is hidden from assistive tech so the visible label is
 * the accessible name.
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
              tabIndex={selected ? 0 : -1}
              className="evener-tab"
              onClick={() => onSelect(tab.id)}
              onKeyDown={(event) => {
                handleTabKeyDown(event, tabs, activeId, onSelect, panelId);
              }}
            >
              <span aria-hidden="true" className="evener-tab__glyph">
                {tab.glyph}
              </span>
              <span>{tab.label}</span>
            </button>
          );
        })}
      </div>
    </nav>
  );
}

/**
 * Roving-tab keydown handler. Selects and focuses the target tab for handled
 * navigation keys; calls `preventDefault` only for those keys and otherwise
 * leaves the event untouched.
 */
function handleTabKeyDown(
  event: KeyboardEvent<HTMLButtonElement>,
  tabs: readonly BottomTab[],
  activeId: string,
  onSelect: (id: string) => void,
  panelId: string,
): void {
  const count = tabs.length;
  if (count === 0) {
    return;
  }
  const activeIndex = tabs.findIndex((t) => t.id === activeId);
  const index = activeIndex === -1 ? 0 : activeIndex;

  let nextIndex: number | null = null;
  switch (event.key) {
    case "ArrowRight":
    case "ArrowDown":
      nextIndex = (index + 1) % count;
      break;
    case "ArrowLeft":
    case "ArrowUp":
      nextIndex = (index - 1 + count) % count;
      break;
    case "Home":
      nextIndex = 0;
      break;
    case "End":
      nextIndex = count - 1;
      break;
    default:
      return;
  }

  if (nextIndex === null) {
    return;
  }
  event.preventDefault();
  const nextTab = tabs[nextIndex];
  if (nextTab === undefined) {
    return;
  }
  const targetId = nextTab.id;
  if (targetId !== activeId) {
    onSelect(targetId);
  }
  // Move focus to the target tab. It becomes the tabbable tab once the
  // controlled activeId prop updates and re-renders with tabindex=0.
  const root = event.currentTarget.ownerDocument;
  const node = root.getElementById(`${panelId}-tab-${targetId}`);
  node?.focus();
}
