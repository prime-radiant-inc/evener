import { forwardRef, type HTMLAttributes, type ReactNode } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./inlinenotice.module.css";

/** neutral: a status fact or a hint ("Saved", "Enter a working directory").
 * danger: something failed ("Could not save"). */
export type InlineNoticeTone = "neutral" | "danger";

/** How the notice is announced when it appears: "status" is polite, "alert"
 * interrupts, "off" is silent (for a notice inside a region that already
 * announces, or one present from first render that is not news). */
export type InlineNoticeLive = "status" | "alert" | "off";

export interface InlineNoticeProps
  extends Omit<HTMLAttributes<HTMLDivElement>, "className" | "role" | "title" | "children"> {
  tone?: InlineNoticeTone;
  /** Defaults to "alert" for danger and "status" for neutral. */
  live?: InlineNoticeLive;
  /** A short heading over the message, for a notice whose body is a list. */
  title?: ReactNode;
  /** Trailing control(s) that resolve the notice: Retry, Connect, Dismiss. */
  action?: ReactNode;
  children: ReactNode;
}

const BASE_CLASS = {
  notice: requireClass(styles.notice, "inlinenotice.module.css", "notice"),
  body: requireClass(styles.body, "inlinenotice.module.css", "body"),
  title: requireClass(styles.title, "inlinenotice.module.css", "title"),
  action: requireClass(styles.action, "inlinenotice.module.css", "action"),
};

const TONE_CLASS: Record<InlineNoticeTone, string> = {
  neutral: requireClass(styles.neutral, "inlinenotice.module.css", "neutral"),
  danger: requireClass(styles.danger, "inlinenotice.module.css", "danger"),
};

const DEFAULT_LIVE: Record<InlineNoticeTone, InlineNoticeLive> = {
  neutral: "status",
  danger: "alert",
};

/**
 * An inline message that sits in the flow of a form or section: a status
 * fact, a hint, or an error, with an optional action that resolves it.
 * Banner is the overlay strip for app-level connection state; this is the
 * in-place sibling.
 *
 * Forwards its ref and spreads native attributes (aria-label, data-testid,
 * tabIndex, ...) the way Button does, so a caller can name the notice or
 * move focus onto an error it just raised. {...rest} lands before role and
 * className so neither is caller-overridable.
 */
export const InlineNotice = forwardRef<HTMLDivElement, InlineNoticeProps>(function InlineNotice(
  { tone = "neutral", live, title, action, children, ...rest },
  ref,
) {
  const announce = live ?? DEFAULT_LIVE[tone];
  return (
    <div
      ref={ref}
      {...rest}
      role={announce === "off" ? undefined : announce}
      className={`${BASE_CLASS.notice} ${TONE_CLASS[tone]}`}
    >
      <div className={BASE_CLASS.body}>
        {title !== undefined && <p className={BASE_CLASS.title}>{title}</p>}
        {children}
      </div>
      {action !== undefined && <div className={BASE_CLASS.action}>{action}</div>}
    </div>
  );
});
