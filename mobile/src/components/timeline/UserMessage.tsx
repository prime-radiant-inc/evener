import type { JSX } from "react";
import { PlainTextBlock } from "./PlainTextBlock";

export interface UserMessageProps {
  readonly text: string;
}

/**
 * Renders a user message as a trailing chat bubble with escaped plain text.
 * No `dangerouslySetInnerHTML` — React escapes the text node. The bubble sits
 * aligned to the trailing edge to distinguish it from assistant content.
 */
export function UserMessage({ text }: UserMessageProps): JSX.Element {
  return (
    <div className="evener-user-message">
      <PlainTextBlock text={text} className="evener-user-message__bubble" />
    </div>
  );
}
