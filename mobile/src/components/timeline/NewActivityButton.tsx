import type { JSX } from "react";

export interface NewActivityButtonProps {
  readonly unseen: number;
  readonly onTap: () => void;
}

/**
 * The "N new" pill that appears when new items arrive while the user is not
 * following (scrolled up). Tapping it re-enables follow mode and clears the
 * unseen count. The pill is a 44px-tappable button with an accessible label.
 */
export function NewActivityButton({
  unseen,
  onTap,
}: NewActivityButtonProps): JSX.Element {
  const label = `${unseen} new`;
  return (
    <div className="evener-new-activity">
      <button
        type="button"
        className="evener-new-activity__button"
        aria-label={label}
        onClick={onTap}
      >
        {label}
      </button>
    </div>
  );
}
