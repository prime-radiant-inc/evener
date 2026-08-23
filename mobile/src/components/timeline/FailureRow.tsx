import type { JSX } from "react";
import { PlainTextBlock } from "./PlainTextBlock";

export interface FailureRowProps {
  readonly title: string;
  readonly detail: string;
}

/**
 * Renders a turn failure inline as plain text. The title is emphasized and the
 * detail (message + hint + additionalDetails) follows. All text is escaped —
 * never raw HTML. The row is actionable (it surfaces the failure so the user
 * can retry or steer), but V1 does not embed interactive controls here.
 */
export function FailureRow({ title, detail }: FailureRowProps): JSX.Element {
  return (
    <div className="evener-failure-row" role="alert">
      <PlainTextBlock text={title} className="evener-failure-row__title" />
      <PlainTextBlock text={detail} className="evener-failure-row__detail" />
    </div>
  );
}
