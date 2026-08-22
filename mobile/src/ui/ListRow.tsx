import type { JSX, ReactNode } from "react";

export interface ListRowProps {
  readonly title: ReactNode;
  readonly subtitle?: ReactNode;
  readonly trailing?: ReactNode;
  readonly onClick?: () => void;
  readonly ariaLabel?: string;
}

/**
 * Inset grouped-list row (iOS style), not a floating card. 44px minimum,
 * hairline separators, title/subtitle/trailing slot.
 */
export function ListRow({
  title,
  subtitle,
  trailing,
  onClick,
  ariaLabel,
}: ListRowProps): JSX.Element {
  return (
    <button
      type="button"
      className="evener-list-row"
      onClick={onClick}
      aria-label={ariaLabel}
    >
      <span className="evener-list-row__main">
        <span className="evener-list-row__title">{title}</span>
        {subtitle ? (
          <span className="evener-list-row__subtitle">{subtitle}</span>
        ) : null}
      </span>
      {trailing}
    </button>
  );
}
