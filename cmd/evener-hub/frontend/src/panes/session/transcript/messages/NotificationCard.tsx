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
import { Fragment } from "react";
import {
  disclosureScopeForSession,
  expandDetailsByDefault,
  useTranscriptRenderContext,
} from "../../../../transcriptDisplay/renderContext";
import { Card, Chip, Markdown } from "../../../../widgets";
import { AnsiTailBuffer, parseAnsiLines } from "../../../../widgets/codeblock/ansi";
import { AnsiLineContent } from "../../../../widgets/codeblock/ansiLine";
import {
  disclosureDefault,
  isDisclosureOpen,
  scopedDisclosureId,
  toggleDisclosure,
} from "../../../../widgets/disclosure/disclosureStore";
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
  headingText: requireClass(styles.headingText, "notificationcard.module.css", "headingText"),
  title: requireClass(styles.title, "notificationcard.module.css", "title"),
  secondary: requireClass(styles.secondary, "notificationcard.module.css", "secondary"),
  secondaryTail: requireClass(styles.secondaryTail, "notificationcard.module.css", "secondaryTail"),
  secondaryTailText: requireClass(styles.secondaryTailText, "notificationcard.module.css", "secondaryTailText"),
  openTrailing: requireClass(styles.openTrailing, "notificationcard.module.css", "openTrailing"),
  metadata: requireClass(styles.metadata, "notificationcard.module.css", "metadata"),
  field: requireClass(styles.field, "notificationcard.module.css", "field"),
  fieldLabel: requireClass(styles.fieldLabel, "notificationcard.module.css", "fieldLabel"),
  concerns: requireClass(styles.concerns, "notificationcard.module.css", "concerns"),
  excerpt: requireClass(styles.excerpt, "notificationcard.module.css", "excerpt"),
  prose: requireClass(styles.prose, "notificationcard.module.css", "prose"),
  raw: requireClass(styles.raw, "notificationcard.module.css", "raw"),
  summary: requireClass(styles.summary, "notificationcard.module.css", "summary"),
  rawBody: requireClass(styles.rawBody, "notificationcard.module.css", "rawBody"),
};

const EXCERPT_PREVIEW = 500;
const MESSAGE_MAX = 8000;

function splitTrailingWord(text: string): [leading: string, trailing: string] {
  const match = /^(.*\s)(\S+)$/.exec(text);
  const leading = match?.[1];
  const trailing = match?.[2];
  return leading !== undefined && trailing !== undefined ? [leading, trailing] : ["", text];
}

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
  // Mockups 23-job-watch §E: a watch notification's title names what happened
  // and its note is the payload — the producer's echo attrs (watch id,
  // status, job type, output count, reason) are metadata soup that says
  // nothing, so the card shows none of them. The watch id stays reachable
  // in the raw disclosure. Job, delegate, watch-send, and observer-callback
  // paths are untouched.
  if (notification.type === "watch") return null;
  const fields = [
    notification.delegateId && (
      <Field
        key="delegate-id"
        label="Delegate id"
        value={notification.delegateId}
        testId="notification-field-delegate-id"
      />
    ),
    notification.jobId && (
      <Field key="job-id" label="Job id" value={notification.jobId} testId="notification-field-job-id" />
    ),
    notification.watchId && (
      <Field key="watch-id" label="Watch id" value={notification.watchId} testId="notification-field-watch-id" />
    ),
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
  const context = useTranscriptRenderContext();
  const { config } = context;
  const disclosureScope = disclosureScopeForSession(context, sessionRef);
  const notificationId = notification.delegateId ?? notification.jobId ?? notification.rawText;
  const scopedNotificationId = `notification:${sessionRef ?? "default"}:${notificationId}`;
  const disclosureKey = scopedDisclosureId(disclosureScope, scopedNotificationId);
  const disclosureFallback =
    expandDetailsByDefault(config) || disclosureDefault(disclosureScope, scopedNotificationId, false);
  const open = isDisclosureOpen(disclosureKey, disclosureFallback);
  const chip = toneChip(notification.tone);
  const transcriptRef = isValidTranscriptRef(notification.transcriptRef) ? notification.transcriptRef : undefined;
  const secondaryParts = notification.secondary ? splitTrailingWord(notification.secondary) : undefined;
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
          toggleDisclosure(disclosureKey, disclosureFallback);
        }}
      >
        {chip && <Chip tone={chip.chipTone}>{chip.label}</Chip>}
        <span className={CLASS.headingText}>
          {secondaryParts || !transcriptRef ? (
            <span className={CLASS.title}>{notification.title}</span>
          ) : (
            <span className={CLASS.secondaryTail}>
              <span className={`${CLASS.title} ${CLASS.secondaryTailText}`}>{notification.title}</span>
              <span className={CLASS.openTrailing}>
                <OpenTranscriptButton transcriptRef={transcriptRef} parentRef={sessionRef} label="Open subagent" />
              </span>
            </span>
          )}
          {secondaryParts ? (
            <span className={CLASS.secondary}>
              {transcriptRef ? (
                <>
                  {secondaryParts[0]}
                  <span className={CLASS.secondaryTail}>
                    <span className={CLASS.secondaryTailText}>{secondaryParts[1]}</span>
                    <span className={CLASS.openTrailing}>
                      <OpenTranscriptButton
                        transcriptRef={transcriptRef}
                        parentRef={sessionRef}
                        label="Open subagent"
                      />
                    </span>
                  </span>
                </>
              ) : (
                notification.secondary
              )}
            </span>
          ) : null}
        </span>
      </summary>
      {open && (
        <Card>
          <div className={CLASS.root} data-testid="notification-card-root">
            <NotificationMetadata notification={notification} />
            {notification.prose && (
              <pre className={CLASS.prose} data-testid="notification-prose">
                {decodeNotificationEntities(notification.prose)}
              </pre>
            )}
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
