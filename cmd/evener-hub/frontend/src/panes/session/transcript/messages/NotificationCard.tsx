// In-transcript job-notification card (contracts §17). Renders one parsed
// <job-notification> / observer-callback block (steeringNotifications.ts) as a card
// that RECEDES when nothing went wrong (color-is-attention: a completed job is
// the expected state, so success/neutral get no tint - warning earns an
// attention Chip, and error announces itself with the red FailureGlyph cross
// seated at full strength in the head's rail: an "error" pill beside a title
// that already says "Job failed" restated the fact without adding anything
// the glyph doesn't show). The excerpt arrives from the parser already decoded
// to plain text (issue #3086), then rendered as ESCAPED text (React's
// default), never as live HTML - a communicate message is the one thing
// rendered as markdown, through the sanitizing Markdown widget.
//
// The delegate card is the report redesign (docs/web-ui/history/mockups/
// 24-delegate-complete): the head names the delegate - its name is the shared
// entity-card trigger - and the outcome; the body is the report, the message
// as a sans-serif bubble and the structured result as a table, with no echo
// metadata and no raw disclosure. A stopped run or a failure whose whole
// error is its ending renders a static head with nothing to expand. Job and
// watch cards keep their metadata fields and raw disclosure: a job's exit
// code and reason are facts their heads do not carry, and a watch's raw frame
// stays the one place its id is inspectable.
//
// Scope-out recorded for T8's sweep: the legacy card's full communicate FACTS
// list (status/commit_hashes/test_summary/artifacts as a <dl>) is not rebuilt -
// the message (markdown) and concerns carry the signal; the plumbing facts stay
// in the raw disclosure. The watch/observer glyph vocabulary (◌/↩) is replaced
// by the uniform tone treatment.
import {
  entityOpenTarget,
  findEntityView,
  isPlainObject,
  isValidTranscriptRef,
  type NotificationTone,
  type ParsedNotification,
  parseAnsiLines,
} from "@evener/appwire-client";
import { Fragment, type ReactNode } from "react";
import { useEntityOwnerRef, useEntityViews } from "../../../../transcriptDisplay/entityViews";
import {
  disclosureScopeForSession,
  expandDetailsByDefault,
  useTranscriptRenderContext,
} from "../../../../transcriptDisplay/renderContext";
import { Card, Chip, Disclosure, Markdown, MarkdownBubble, ToolIcon, type ToolIconKind } from "../../../../widgets";
import { AnsiLineContent } from "../../../../widgets/codeblock/ansiLine";
import { setDisclosureOpen } from "../../../../widgets/disclosure/disclosureStore";
import { FailureGlyph } from "../../../../widgets/failureglyph";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { EntityRef } from "../EntityRef";
import { OpenTranscriptButton } from "../openTranscript";
import styles from "./notificationcard.module.css";
import {
  boundedMessage,
  boundedValue,
  delegateCardLabel,
  excerptPreview,
  hiddenResultRows,
  notificationDisclosureId,
  notificationRendersBody,
  rawDisclosureId,
  STRUCTURED_NONE,
  shownResultRows,
  structuredResultNote,
  structuredRowLabel,
  structuredValueText,
} from "./notificationText";
import { useTranscriptDisclosure } from "./useTranscriptDisclosure";

const CLASS = {
  collapsible: requireClass(styles.collapsible, "notificationcard.module.css", "collapsible"),
  disclosure: requireClass(styles.disclosure, "notificationcard.module.css", "disclosure"),
  besideOpen: requireClass(styles.besideOpen, "notificationcard.module.css", "besideOpen"),
  delegateRef: requireClass(styles.delegateRef, "notificationcard.module.css", "delegateRef"),
  expanded: requireClass(styles.expanded, "notificationcard.module.css", "expanded"),
  root: requireClass(styles.root, "notificationcard.module.css", "root"),
  headingText: requireClass(styles.headingText, "notificationcard.module.css", "headingText"),
  title: requireClass(styles.title, "notificationcard.module.css", "title"),
  secondary: requireClass(styles.secondary, "notificationcard.module.css", "secondary"),
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
  statusIcon: requireClass(styles.statusIcon, "notificationcard.module.css", "statusIcon"),
  statusIconError: requireClass(styles.statusIconError, "notificationcard.module.css", "statusIconError"),
  summaryIcon: requireClass(styles.summaryIcon, "notificationcard.module.css", "summaryIcon"),
  summaryIconError: requireClass(styles.summaryIconError, "notificationcard.module.css", "summaryIconError"),
  resultTable: requireClass(styles.resultTable, "notificationcard.module.css", "resultTable"),
  resultKey: requireClass(styles.resultKey, "notificationcard.module.css", "resultKey"),
  resultValue: requireClass(styles.resultValue, "notificationcard.module.css", "resultValue"),
  resultNone: requireClass(styles.resultNone, "notificationcard.module.css", "resultNone"),
  structuredNote: requireClass(styles.structuredNote, "notificationcard.module.css", "structuredNote"),
  staticHead: requireClass(styles.staticHead, "notificationcard.module.css", "staticHead"),
};

// The head's status glyph for every tone except error: one line-art shape per
// parsed tone, receding in neutral ink at the rail's ambient 50% opacity like
// every other row icon. Error is deliberately absent - it renders
// FailureGlyph (see statusIconSeat below), the red cross that carries the
// failure's hue as the card's one attention signal.
const STATUS_ICON_KIND: Record<Exclude<NotificationTone, "error">, ToolIconKind> = {
  success: "check",
  warning: "alert",
  neutral: "info",
};

// The error tone's presentation decision in one place: the FailureGlyph cross
// seated at full strength, never the quiet rail's kind icon - the red cross
// IS the failure signal. Every other tone draws its kind from the table above.
function statusIconSeat(tone: NotificationTone): { className: string; summaryClassName: string; glyph: ReactNode } {
  if (tone === "error") {
    return { className: CLASS.statusIconError, summaryClassName: CLASS.summaryIconError, glyph: <FailureGlyph /> };
  }
  return {
    className: CLASS.statusIcon,
    summaryClassName: CLASS.summaryIcon,
    glyph: <ToolIcon kind={STATUS_ICON_KIND[tone]} />,
  };
}

// The card's one head: status glyph, the warning chip a warning tone earns,
// title and secondary line. The static line wraps it, every other card passes
// it as the Disclosure summary; `inSummary` selects the glyph's seat and
// nothing else differs.
function NotificationHead({
  tone,
  inSummary = false,
  title,
  secondary,
}: {
  tone: NotificationTone;
  inSummary?: boolean;
  title: string;
  secondary?: ReactNode;
}) {
  const seat = statusIconSeat(tone);
  const showWarningChip = tone === "warning";
  return (
    <>
      <span
        className={inSummary ? seat.summaryClassName : seat.className}
        data-testid="notification-status-icon"
        aria-hidden="true"
      >
        {seat.glyph}
      </span>
      {showWarningChip && (
        <Chip tone="attention" kind="warning">
          warning
        </Chip>
      )}
      <span className={CLASS.headingText}>
        <span className={CLASS.title}>{title}</span>
        {secondary !== undefined && <span className={CLASS.secondary}>{secondary}</span>}
      </span>
    </>
  );
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

function Excerpt({ text, ansi }: { text: string; ansi: boolean }) {
  const preview = excerptPreview(text, ansi);
  if (preview === "") return null;
  // Keep unstructured output bounded in the primary card. The complete
  // diagnostic payload remains available in the card's one raw disclosure.
  return (
    <div className={CLASS.excerpt} data-testid="notification-field-excerpt">
      <ExcerptText text={preview} ansi={ansi} />
    </div>
  );
}

function Field({ label, value, testId }: { label: string; value: ReactNode; testId: string }) {
  return (
    <span className={CLASS.field} data-testid={testId}>
      <span className={CLASS.fieldLabel}>{label}</span> {value}
    </span>
  );
}

// The verbatim frame, behind the card's one raw disclosure. It stays the
// native <details>/<summary> pair the card has always shown (the marker row is
// part of that design), but its open state lives in the shared disclosure store
// so Find can open it: a match only the frame holds opens this fold and
// highlights the word. A closed fold is aria-hidden, which is what keeps Find's
// marking out of words a reader cannot see (<details>'s closed body is hidden
// to assistive tech and to marking alike).
function RawNotification({ scope, id, text }: { scope: string; id: string; text: string }) {
  const { key, open } = useTranscriptDisclosure({ scope, id, defaultOpen: false });
  return (
    <details
      className={CLASS.raw}
      data-testid="notification-raw-disclosure"
      open={open}
      onToggle={(event) => {
        if (event.currentTarget.open !== open) setDisclosureOpen(key, event.currentTarget.open);
      }}
    >
      <summary className={CLASS.summary}>Raw notification</summary>
      <pre className={CLASS.rawBody} data-testid="notification-raw" aria-hidden={open ? undefined : "true"}>
        {text}
      </pre>
    </details>
  );
}

// An identity field's value IS an entity id, so it renders as the transcript's
// shared card trigger - the same one a tool summary's id renders as, hovered or
// focused for the card. Resolving is the map's business: an id the session's
// entity map cannot answer for falls back to the plain text it is today
// (EntityRef's own contract), so no field ever shows a dead trigger.
function EntityIdField({ label, id, testId }: { label: string; id: string; testId: string }) {
  return <Field label={label} value={<EntityRef id={id} />} testId={testId} />;
}

function NotificationMetadata({ notification }: { notification: ParsedNotification }) {
  // A delegate card carries no echo metadata (mockups 24-delegate-complete):
  // the status restates the title's outcome word for word, and the delegate id
  // was only ever a handle for the entity card - which the head's name trigger
  // opens. Job and watch cards keep their fields.
  if (notification.type === "delegate") return null;
  // Mockups 23-job-watch §E: a watch notification's title names what happened
  // and its note is the payload — the producer's echo attrs (status, job
  // type, output count, reason) are metadata soup that says nothing, so the
  // card shows none of them. Identity is the exception: the originating
  // watch id (the producer stamps every watch frame — timers, job-targeted
  // fires, teardown notices, send-rail diagnostics) names which watch to
  // inspect or clear, and a job-targeted fire additionally names its watched
  // job. Both render as what they are; the raw disclosure keeps the full
  // frame. Job, delegate, watch-send, and observer-callback paths are
  // untouched.
  // A job-targeted watch fire carries NO watch_id attr on older frames
  // (formatJobNotificationBlock emitted watch_id only when JobID == ""), so
  // the watched job id is the only recoverable identity there — it renders
  // as the card's one identity line, labelled as what it is (never a watch
  // id). A job-less watch names nothing.
  if (notification.type === "watch") {
    const fields = [
      notification.watchId && (
        <EntityIdField key="watch-id" label="Watch id" id={notification.watchId} testId="notification-field-watch-id" />
      ),
      notification.jobId && notification.jobId !== "self" && (
        <EntityIdField key="job-id" label="Job id" id={notification.jobId} testId="notification-field-job-id" />
      ),
    ].filter(Boolean);
    if (fields.length === 0) return null;
    return <div className={CLASS.metadata}>{fields}</div>;
  }
  const fields = [
    notification.jobId && (
      <EntityIdField key="job-id" label="Job id" id={notification.jobId} testId="notification-field-job-id" />
    ),
    notification.watchId && (
      <EntityIdField key="watch-id" label="Watch id" id={notification.watchId} testId="notification-field-watch-id" />
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

// One schema value as the table paints it. An absent value says so, quietly -
// an empty cell reads as a gap.
function structuredRowValue(value: unknown): ReactNode {
  const text = structuredValueText(value);
  return text === STRUCTURED_NONE ? (
    <span className={CLASS.resultNone} data-empty="true">
      {text}
    </span>
  ) : (
    text
  );
}

// The structured output as a table (mockups 24-delegate-complete §A): one row
// per schema key, headerless - the labels are the keys in words, and the
// message above already names what the result is about. A result that failed
// its verdict renders as a quiet note instead, never as rows that failed
// their schema, and a result with no record shape (a top-level array or
// scalar schema's output) renders through the value grammar - the same
// bounded text a table cell gives a value - rather than vanishing.
function StructuredResult({ notification }: { notification: ParsedNotification }) {
  if (notification.structuredResultValid === false) {
    return (
      <div
        className={CLASS.structuredNote}
        data-testid="notification-structured-note"
        data-reason={notification.structuredResultReason}
      >
        {structuredResultNote(notification.structuredResultReason)}
      </div>
    );
  }
  const result = notification.structuredResult;
  if (result === undefined) return null;
  if (!isPlainObject(result)) {
    return (
      <div className={CLASS.excerpt} data-testid="notification-structured-json">
        {structuredRowValue(result)}
      </div>
    );
  }
  const rows = Object.entries(result);
  if (rows.length === 0) return null;
  const shown = shownResultRows(rows);
  const hidden = hiddenResultRows(rows.length);
  return (
    <table className={CLASS.resultTable} data-testid="notification-structured-result">
      <tbody>
        {shown.map(([key, value]) => (
          <tr key={key} data-key={key}>
            <th scope="row" className={CLASS.resultKey}>
              {boundedValue(structuredRowLabel(key))}
            </th>
            <td className={CLASS.resultValue}>{structuredRowValue(value)}</td>
          </tr>
        ))}
        {hidden > 0 && (
          <tr>
            <td className={CLASS.resultNone} colSpan={2}>
              (+{hidden} more rows)
            </td>
          </tr>
        )}
      </tbody>
    </table>
  );
}

export function NotificationCard({
  notification,
  sessionRef,
  disclosureId,
}: {
  notification: ParsedNotification;
  sessionRef?: string;
  // Stable per-delivery discriminator (transcript item id + fragment index,
  // threaded from SteeringItem). Byte-identical repeat deliveries share
  // watch id and raw text; without this they share one disclosure key and
  // toggle together. Optional for backward compatibility — existing callers
  // render without it and keep today's keys.
  disclosureId?: string;
}) {
  const context = useTranscriptRenderContext();
  const entities = useEntityViews();
  const contextOwner = useEntityOwnerRef();
  const entityOwner = sessionRef ?? contextOwner;
  const disclosureScope = disclosureScopeForSession(context, sessionRef);
  // The card's own identity and its raw frame's, both scoped to this view:
  // Find opens the same keys (notificationText's identity rule).
  const scopedNotificationId = notificationDisclosureId(notification, sessionRef, disclosureId);
  const rawNotificationId = rawDisclosureId(scopedNotificationId);
  const disclosure = useTranscriptDisclosure({
    scope: disclosureScope,
    id: scopedNotificationId,
    defaultOpen: expandDetailsByDefault(context.config),
  });
  // The head's open affordance is the SUBAGENT control, so only a delegate
  // notification earns it: its transcript_ref, when a frame carries one,
  // names a child session's thread. A job notification is not a subagent
  // report - its transcript_ref is the read_transcript ref for retained
  // output ("job:<id>", agent/job_notify.go's jobTranscriptRef), which opens
  // the job-log surface rather than a subagent transcript - so job
  // notifications of any type never show the control. The job log stays
  // reachable through the card's job-id trigger and the activity tree.
  // The frame's own transcript_ref, when it carries one. A REAL daemon frame
  // does not (agent/delegate_delivery.go's delegateNotificationContent stamps
  // delegate_id and name only), so on a live report the ref below is absent and
  // the entity map answers the delegate id with its transcript — the same
  // resolution the phone makes by delegate id (#3075). Unresolved leaves the
  // control off, never a dead one.
  const delegateView =
    notification.type === "delegate" && notification.delegateId && entities && entityOwner
      ? findEntityView(entities, "delegate", notification.delegateId, entityOwner)
      : undefined;
  const resolvedRef = delegateView?.kind === "delegate" ? entityOpenTarget(delegateView)?.ref : undefined;
  const transcriptRef =
    notification.type === "delegate" ? [notification.transcriptRef, resolvedRef].find(isValidTranscriptRef) : undefined;
  // The delegate head's identity (mockups 24-delegate-complete §A): the name
  // is the shared entity-card trigger, with the ending a failed or stopped run
  // appends riding after it - the same composition the parser's `secondary`
  // carries, the trigger standing in for the plain label. When the entity map
  // cannot answer, EntityRef falls back to plain text.
  const delegateLabel = delegateCardLabel(notification);
  // The ending composed onto the head is the packet frame's display prose
  // (`ending`, delegateEndingText); a legacy attribute frame's `reason` is a
  // raw producer code and never reaches the head.
  const delegateEnding =
    delegateLabel !== undefined && (notification.tone === "error" || notification.tone === "warning")
      ? notification.ending
      : undefined;
  // A delegate head whose body would render nothing - a machinery stop, a
  // failure whose whole error is its ending (mockups 24-delegate-complete §C)
  // - is the whole row: a disclosure that expanded to an empty body would be
  // an affordance lie, so it renders as a static line. The predicate is shared
  // with Find, which must not offer to open a disclosure that is not there.
  const delegateHasReport = notificationRendersBody(notification);
  // The delegate's name, in two forms. On the static head (a plain row) it is
  // the shared entity-card trigger. In a disclosure summary it is the same
  // card on hover but never a focus stop or click target of its own: the
  // summary stays one control, the Disclosure's. When the entity map cannot
  // answer, EntityRef falls back to plain text either way.
  const delegateTrigger = (embedded: boolean) =>
    delegateLabel !== undefined && delegateView ? (
      <EntityRef
        view={delegateView}
        id={notification.delegateId ?? ""}
        display={delegateLabel}
        triggerOnly
        embedded={embedded}
      />
    ) : (
      delegateLabel
    );
  // The Open control. It is interactive, so it never rides inside a
  // disclosure summary: the collapsible card seats it right after the
  // Disclosure, on the summary's line.
  const openControl = transcriptRef ? (
    <span className={CLASS.openTrailing}>
      <OpenTranscriptButton transcriptRef={transcriptRef} parentRef={sessionRef} label="Open subagent" />
    </span>
  ) : null;
  // The head's identity for a reader of the DOM: the notification's type,
  // tone and settled outcome, and whether it is a running update.
  const headData = {
    "data-testid": "notification-card",
    "data-tone": notification.tone,
    "data-type": notification.type,
    "data-outcome": notification.outcome,
    "data-update": notification.update ? "true" : undefined,
  };
  if (!delegateHasReport) {
    const delegateName = delegateTrigger(false);
    // Neither the ending nor the Open control may be gated on the label.
    const secondary =
      delegateName !== undefined || notification.secondary !== "" || openControl !== null ? (
        <>
          {delegateName !== undefined ? delegateName : notification.secondary}
          {delegateName !== undefined && delegateEnding ? ` · ${delegateEnding}` : null}
          {openControl}
        </>
      ) : undefined;
    return (
      <div className={CLASS.staticHead} {...headData}>
        <NotificationHead tone={notification.tone} title={notification.title} secondary={secondary} />
      </div>
    );
  }
  const delegateName = delegateTrigger(true);
  const secondary =
    delegateName !== undefined ? (
      <>
        {delegateName}
        {delegateEnding ? ` · ${delegateEnding}` : null}
      </>
    ) : (
      notification.secondary || undefined
    );
  return (
    <div className={CLASS.collapsible} {...headData}>
      <div className={CLASS.disclosure}>
        <Disclosure
          variant="quiet"
          id={disclosure.key}
          defaultOpen={disclosure.fallback}
          summary={
            <NotificationHead tone={notification.tone} inSummary title={notification.title} secondary={secondary} />
          }
        >
          <div className={CLASS.expanded}>
            <Card>
              <div className={CLASS.root} data-testid="notification-card-root">
                {/* The head's name is hover-only; here it is the same entity card
                    as a keyboard focus stop, so a keyboard reader reaches what a
                    pointer reader gets. */}
                {delegateView && delegateLabel !== undefined && (
                  <div className={CLASS.delegateRef} data-testid="notification-delegate">
                    {delegateTrigger(false)}
                  </div>
                )}
                <NotificationMetadata notification={notification} />
                {notification.prose && (
                  <pre className={CLASS.prose} data-testid="notification-prose">
                    {notification.prose}
                  </pre>
                )}
                {notification.message ? (
                  notification.type === "delegate" ? (
                    <MarkdownBubble source={boundedMessage(notification.message)} dataTestId="notification-message" />
                  ) : (
                    <div className={CLASS.excerpt} data-testid="notification-field-excerpt">
                      <Markdown source={boundedMessage(notification.message)} />
                    </div>
                  )
                ) : (
                  <Excerpt text={notification.excerpt} ansi={notification.jobType === "shell"} />
                )}
                <StructuredResult notification={notification} />
                {notification.concerns.length > 0 && (
                  <div className={CLASS.concerns}>Concerns: {notification.concerns.join("; ")}</div>
                )}
                {notification.type !== "delegate" && (
                  <RawNotification scope={disclosureScope} id={rawNotificationId} text={notification.rawText} />
                )}
              </div>
            </Card>
          </div>
        </Disclosure>
      </div>
      {openControl && <span className={CLASS.besideOpen}>{openControl}</span>}
    </div>
  );
}
