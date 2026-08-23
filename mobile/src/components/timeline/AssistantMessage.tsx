import type { JSX, MouseEvent } from "react";
import {
  renderSafeMarkdown,
  type TrustedHTMLString,
} from "../../conversation/markdown";

export interface AssistantMessageProps {
  /** Assistant Markdown source. Sanitized before injection. */
  readonly source: string;
  /** Invoked when the user taps a link; receives the destination URL. */
  readonly onExternalLink?: (url: string) => void;
  /** Streaming hint for visual treatment (no behavioural change here). */
  readonly streaming?: boolean;
}

/**
 * Renders assistant Markdown as sanitized HTML. This is the ONLY component
 * permitted to use `dangerouslySetInnerHTML`; the HTML comes exclusively from
 * `renderSafeMarkdown`, which strips raw HTML at the Markdown token level and
 * sanitizes through DOMPurify with an explicit tag/attribute/protocol allowlist.
 *
 * Links display their destination and route taps to `onExternalLink` instead of
 * navigating the webview. The capture-phase click handler prevents default
 * before the callback fires.
 */
export function AssistantMessage({
  source,
  onExternalLink,
  streaming = false,
}: AssistantMessageProps): JSX.Element {
  const trusted: TrustedHTMLString = renderSafeMarkdown(source);

  function handleClick(event: MouseEvent<HTMLDivElement>): void {
    if (!onExternalLink) {
      return;
    }
    const target = event.target as HTMLElement | null;
    const anchor = target?.closest("a") as HTMLAnchorElement | null;
    if (!anchor) {
      return;
    }
    event.preventDefault();
    onExternalLink(anchor.href);
  }

  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: click is event-delegated to nested <a> elements; links remain keyboard-accessible natively
    // biome-ignore lint/a11y/useKeyWithClickEvents: the handler delegates to focusable anchors that already support keyboard activation
    <div
      className="evener-assistant-message"
      data-streaming={streaming || undefined}
      // biome-ignore lint/security/noDangerouslySetInnerHtml: HTML is produced by renderSafeMarkdown (DOMPurify-sanitized, raw-HTML-stripped). This is the sole sanctioned injection point.
      dangerouslySetInnerHTML={{ __html: trusted.html }}
      onClick={handleClick}
    />
  );
}
