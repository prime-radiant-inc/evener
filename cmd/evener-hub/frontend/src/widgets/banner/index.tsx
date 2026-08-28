// Banner: a reusable overlay status strip carrying a tone-mapped warning hue
// and an optional action button, for "the connection needs a human" moments
// that should overlay the chrome instead of reserving layout space for what
// is usually a transient state.
//
// Reusable by design: ConnectionBanner composes it today, and any future
// shell-level status that needs an overlay strip + warning tint + optional
// action can compose it the same way. tone selects the semantic family
// (--attention "a human is needed" / --danger "something failed") via the
// tinted companions (-bg/-edge/-ink), never a bare-hue flood, per the
// token-contract allowlist this widget earns.
import { Button } from "../button";
import { requireClass } from "../internal/requireClass";
import styles from "./banner.module.css";

export type BannerTone = "attention" | "danger";

export interface BannerAction {
  label: string;
  onClick: () => void;
  // When true the button renders disabled and appends "…" to the label
  // (e.g. "Retry" → "Retry…") - a generic async-action affordance.
  inFlight?: boolean;
}

export interface BannerProps {
  tone: BannerTone;
  message: string;
  action?: BannerAction;
}

const BASE_CLASS = {
  banner: requireClass(styles.banner, "banner.module.css", "banner"),
};

const TONE_CLASS: Record<BannerTone, string> = {
  attention: requireClass(styles.attention, "banner.module.css", "attention"),
  danger: requireClass(styles.danger, "banner.module.css", "danger"),
};

/**
 * Overlay status strip pinned to the top of its positioning ancestor.
 * Absolutely positioned full-width: it floats over the content below it
 * instead of occupying flow height and shifting the layout. The ancestor
 * must establish a positioning context (position: relative/absolute/fixed);
 * in production that is AppShell's `.shell`.
 */
export function Banner({ tone, message, action }: BannerProps) {
  return (
    <div className={`${BASE_CLASS.banner} ${TONE_CLASS[tone]}`} role="status" aria-live="polite">
      <span className={styles.message}>{message}</span>
      {action && (
        <Button variant="quiet" size="sm" onClick={action.onClick} disabled={action.inFlight}>
          {action.inFlight ? `${action.label}…` : action.label}
        </Button>
      )}
    </div>
  );
}
