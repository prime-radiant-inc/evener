import type { JSX } from "react";
import type { NoticeTone } from "../../conversation/model";
import { PlainTextBlock } from "./PlainTextBlock";

export interface NoticeRowProps {
  readonly tone: NoticeTone;
  readonly text: string;
}

/**
 * Renders a steering or system notice as tone-colored plain text. Tones map
 * to semantic data attributes (info/warning/system) so color reinforces but
 * never carries meaning alone. Text is escaped — never raw HTML.
 */
export function NoticeRow({ tone, text }: NoticeRowProps): JSX.Element {
  return (
    <div className="evener-notice-row" data-tone={tone}>
      <PlainTextBlock text={text} />
    </div>
  );
}
