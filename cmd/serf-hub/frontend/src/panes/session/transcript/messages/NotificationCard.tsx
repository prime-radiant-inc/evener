// In-transcript job-notification card (contracts §17). Renders one parsed
// <job-notification> / observer-callback block (steeringClassify.ts) as a card
// that RECEDES when nothing went wrong (color-is-attention: a completed job is
// the expected state, so success/neutral get no tint - only warning earns
// attention, only error earns danger, each via a Chip tone). The verbatim block
// is always kept inspectable in a raw disclosure, and the excerpt is
// entity-decoded then rendered as ESCAPED text (React's default), never as live
// HTML - a communicate message is the one thing rendered as markdown, through
// the sanitizing Markdown widget.
//
// Scope-out recorded for T8's sweep: the legacy card's full communicate FACTS
// list (status/commit_hashes/test_summary/artifacts as a <dl>) is not rebuilt -
// the message (markdown) and concerns carry the signal; the plumbing facts stay
// in the raw disclosure. The watch/observer glyph vocabulary (◌/↩) is replaced
// by the uniform tone treatment.
import { Card, Chip, Markdown } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./notificationcard.module.css";
import type { NotificationTone, ParsedNotification } from "./steeringClassify";

const CLASS = {
  head: requireClass(styles.head, "notificationcard.module.css", "head"),
  title: requireClass(styles.title, "notificationcard.module.css", "title"),
  secondary: requireClass(styles.secondary, "notificationcard.module.css", "secondary"),
  concerns: requireClass(styles.concerns, "notificationcard.module.css", "concerns"),
  excerpt: requireClass(styles.excerpt, "notificationcard.module.css", "excerpt"),
  raw: requireClass(styles.raw, "notificationcard.module.css", "raw"),
  rawBody: requireClass(styles.rawBody, "notificationcard.module.css", "rawBody"),
};

const EXCERPT_PREVIEW = 500;
const MESSAGE_MAX = 8000;

// Only warning/error earn colour (attention/danger); success + neutral recede
// with no chip at all (the done glyph is the same neutral as any other card).
function toneChip(tone: NotificationTone): { chipTone: "attention" | "danger"; label: string } | null {
  if (tone === "error") return { chipTone: "danger", label: "error" };
  if (tone === "warning") return { chipTone: "attention", label: "warning" };
  return null;
}

// decodeNotificationEntities unescapes one HTML-entity layer so the reader sees
// the job's real output text (the daemon escapes < & > in the excerpt to keep
// them from breaking the <job-notification> wrapper). &amp; is undone LAST so
// double-escaped content unwraps just one level. Safe: the result is only ever
// rendered as React text (escaped), never as HTML.
function decodeEntities(text: string): string {
  return text
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/&quot;/g, '"')
    .replace(/&#0*39;|&#x0*27;/gi, "'")
    .replace(/&amp;/g, "&");
}

function Excerpt({ text }: { text: string }) {
  const decoded = decodeEntities(text.trim());
  if (decoded === "") return null;
  if (decoded.length <= EXCERPT_PREVIEW) {
    return <div className={CLASS.excerpt}>{decoded}</div>;
  }
  // A very long unstructured excerpt is collapsed rather than rendered in full
  // (contracts §17): a bounded preview plus a details holding the whole thing.
  return (
    <>
      <div className={CLASS.excerpt}>{`${decoded.slice(0, EXCERPT_PREVIEW)}…`}</div>
      <details className={CLASS.raw}>
        <summary>Full excerpt</summary>
        <pre className={CLASS.rawBody}>{decoded}</pre>
      </details>
    </>
  );
}

export function NotificationCard({ notification }: { notification: ParsedNotification }) {
  const chip = toneChip(notification.tone);
  return (
    <Card>
      <div className={CLASS.head} data-testid="notification-card" data-tone={notification.tone}>
        {chip && <Chip tone={chip.chipTone}>{chip.label}</Chip>}
        <span className={CLASS.title}>{notification.title}</span>
        {notification.secondary && <span className={CLASS.secondary}>{notification.secondary}</span>}
      </div>
      {notification.message ? (
        <Markdown source={notification.message.slice(0, MESSAGE_MAX)} />
      ) : (
        <Excerpt text={notification.excerpt} />
      )}
      {notification.concerns.length > 0 && (
        <div className={CLASS.concerns}>Concerns: {notification.concerns.join("; ")}</div>
      )}
      <details className={CLASS.raw}>
        <summary>Raw notification</summary>
        <pre className={CLASS.rawBody} data-testid="notification-raw">
          {notification.rawText}
        </pre>
      </details>
    </Card>
  );
}
