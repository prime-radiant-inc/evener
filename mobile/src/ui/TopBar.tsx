import type { JSX, ReactNode } from "react";

export interface TopBarProps {
  readonly title: ReactNode;
  readonly leading?: ReactNode;
  readonly trailing?: ReactNode;
}

/**
 * Navigation top bar. One row, 44px minimum, title centered/trailing.
 * Never desktop rail/panes.
 */
export function TopBar({ title, leading, trailing }: TopBarProps): JSX.Element {
  return (
    <header className="evener-topbar">
      {leading}
      <span className="evener-topbar__title">{title}</span>
      {trailing}
    </header>
  );
}
