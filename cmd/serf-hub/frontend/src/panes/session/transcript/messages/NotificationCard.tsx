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
import { Fragment, useState } from "react";
import { Card, Chip, Markdown } from "../../../../widgets";
import { AnsiTailBuffer, parseAnsiLines } from "../../../../widgets/codeblock/ansi";
import { AnsiLineContent } from "../../../../widgets/codeblock/ansiLine";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { OpenTranscriptButton } from "../openTranscript";
import styles from "./notificationcard.module.css";
import {
  decodeNotificationEntities,
  isValidTranscriptRef,
  type NotificationTone,
  type ParsedNotification,
} from "./steeringClassify";

const CLASS = {
  disclosure: requireClass(styles.disclosure, "notificationcard.module.css", "disclosure"),
  root: requireClass(styles.root, "notificationcard.module.css", "root"),
  head: requireClass(styles.head, "notificationcard.module.css", "head"),
  title: requireClass(styles.title, "notificationcard.module.css", "title"),
  secondary: requireClass(styles.secondary, "notificationcard.module.css", "secondary"),
  action: requireClass(styles.action, "notificationcard.module.css", "action"),
  metadata: requireClass(styles.metadata, "notificationcard.module.css", "metadata"),
  field: requireClass(styles.field, "notificationcard.module.css", "field"),
  fieldLabel: requireClass(styles.fieldLabel, "notificationcard.module.css", "fieldLabel"),
  concerns: requireClass(styles.concerns, "notificationcard.module.css", "concerns"),
  excerpt: requireClass(styles.excerpt, "notificationcard.module.css", "excerpt"),
  raw: requireClass(styles.raw, "notificationcard.module.css", "raw"),
  summary: requireClass(styles.summary, "notificationcard.module.css", "summary"),
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

function ExcerptText({ text, ansi }: { text: string; ansi: boolean }) {
  if (!ansi) return text;
  return parseAnsiLines(text).map((line, index) => (
    // biome-ignore lint/suspicious/noArrayIndexKey: index is the stable source line number
    <Fragment key={index}>
      {index > 0 ? "\n" : null}
      <AnsiLineContent line={line} />
    </Fragment>
  ));
}

// boundedShellTailPreview bounds a shell excerpt to its FINAL EXCERPT_PREVIEW
// characters, not its first: the producer's own excerpt is already a tail of
// retained output (agent/job_notify.go), so a head-cut on top of that hides
// the command's newest, usually most useful lines behind its oldest ones.
// AnsiTailBuffer is the shared ANSI tail-state machinery (also used live by
// shellTool.tsx's ShellBody) - reused here rather than a second control
// parser - so a cut landing inside an SGR sequence never leaks a raw
// fragment, and styling active at the kept boundary is reconstructed as a
// normalized SGR sequence right before the kept text.
function boundedShellTailPreview(decoded: string): string {
  const tail = new AnsiTailBuffer(EXCERPT_PREVIEW).update(decoded);
  return tail.truncated ? `…${tail.renderedText}` : tail.renderedText;
}

function Excerpt({ text, ansi }: { text: string; ansi: boolean }) {
  const decoded = decodeNotificationEntities(text.trim());
  if (decoded === "") return null;
  // Direction matches the parse mode: a shell excerpt (ansi) is bounded to
  // its tail, a delegate report head (non-ansi) keeps its existing
  // head-truncated preview.
  const preview = ansi
    ? boundedShellTailPreview(decoded)
    : decoded.length <= EXCERPT_PREVIEW
      ? decoded
      : `${decoded.slice(0, EXCERPT_PREVIEW)}…`;
  // Keep unstructured output bounded in the primary card. The complete
  // diagnostic payload remains available in the card's one raw disclosure.
  return (
    <div className={CLASS.excerpt} data-testid="notification-field-excerpt">
      <ExcerptText text={preview} ansi={ansi} />
    </div>
  );
}

function Field({ label, value, testId }: { label: string; value: string | number; testId: string }) {
  return (
    <span className={CLASS.field} data-testid={testId}>
      <span className={CLASS.fieldLabel}>{label}</span> {value}
    </span>
  );
}

function NotificationMetadata({ notification }: { notification: ParsedNotification }) {
  const fields = [
    notification.status && (
      <Field key="status" label="Status" value={notification.status} testId="notification-field-status" />
    ),
    notification.jobType && (
      <Field key="job-type" label="Job type" value={notification.jobType} testId="notification-field-job-type" />
    ),
    notification.outputBytes !== undefined && (
      <Field key="output" label="Output" value={notification.outputBytes} testId="notification-field-output" />
    ),
    notification.reason && (
      <Field key="reason" label="Reason" value={notification.reason} testId="notification-field-reason" />
    ),
    notification.exitCode !== undefined && (
      <Field key="exit" label="Exit code" value={notification.exitCode} testId="notification-field-exit" />
    ),
  ].filter(Boolean);
  if (fields.length === 0) return null;
  return <div className={CLASS.metadata}>{fields}</div>;
}

export function NotificationCard({
  notification,
  sessionRef,
}: {
  notification: ParsedNotification;
  sessionRef?: string;
}) {
  const [open, setOpen] = useState(false);
  const chip = toneChip(notification.tone);
  const transcriptRef = isValidTranscriptRef(notification.transcriptRef) ? notification.transcriptRef : undefined;
  return (
    <details className={CLASS.disclosure} open={open}>
      {/* biome-ignore lint/a11y/noStaticElementInteractions: <summary> is natively keyboard-operable; controlled for the same single-source-of-truth reason as ToolRow */}
      {/* biome-ignore lint/a11y/useAriaPropsSupportedByRole: summary's implicit role is button, which supports aria-expanded (same ruling as ToolRow.tsx) - and it can never disagree with the native details state, which the same `open` drives */}
      <summary
        className={CLASS.head}
        data-testid="notification-card"
        data-tone={notification.tone}
        aria-expanded={open}
        onClick={(e) => {
          e.preventDefault();
          setOpen((current) => !current);
        }}
      >
        {chip && <Chip tone={chip.chipTone}>{chip.label}</Chip>}
        <span className={CLASS.title}>{notification.title}</span>
        {notification.secondary && <span className={CLASS.secondary}>{notification.secondary}</span>}
        {transcriptRef && (
          <span className={CLASS.action}>
            <OpenTranscriptButton transcriptRef={transcriptRef} parentRef={sessionRef} label="Open subagent" />
          </span>
        )}
      </summary>
      {open && (
        <Card>
          <div className={CLASS.root} data-testid="notification-card-root">
            <NotificationMetadata notification={notification} />
            {notification.message ? (
              <div className={CLASS.excerpt} data-testid="notification-field-excerpt">
                <Markdown source={notification.message.slice(0, MESSAGE_MAX)} />
              </div>
            ) : (
              <Excerpt text={notification.excerpt} ansi={notification.jobType === "shell"} />
            )}
            {notification.concerns.length > 0 && (
              <div className={CLASS.concerns}>Concerns: {notification.concerns.join("; ")}</div>
            )}
            <details className={CLASS.raw} data-testid="notification-raw-disclosure">
              <summary className={CLASS.summary}>Raw notification</summary>
              <pre className={CLASS.rawBody} data-testid="notification-raw">
                {notification.rawText}
              </pre>
            </details>
          </div>
        </Card>
      )}
    </details>
  );
}
