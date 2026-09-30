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
  scopedDisclosureId,
} from "@evener/appwire-client";
import { Fragment, type ReactNode } from "react";
import { useEntityOwnerRef, useEntityViews } from "../../../../transcriptDisplay/entityViews";
import {
  disclosureScopeForSession,
  expandDetailsByDefault,
  useTranscriptRenderContext,
} from "../../../../transcriptDisplay/renderContext";
import { Card, Chevron, Chip, Markdown, ToolIcon, type ToolIconKind } from "../../../../widgets";
import { AnsiTailBuffer, parseAnsiLines } from "../../../../widgets/codeblock/ansi";
import { AnsiLineContent } from "../../../../widgets/codeblock/ansiLine";
import { disclosureDefault, isDisclosureOpen, toggleDisclosure } from "../../../../widgets/disclosure/disclosureStore";
import { FailureGlyph } from "../../../../widgets/failureglyph";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { EntityRef } from "../EntityRef";
import { OpenTranscriptButton } from "../openTranscript";
import { splitTrailingWord } from "../tailWord";
import styles from "./notificationcard.module.css";

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
  chevron: requireClass(styles.chevron, "notificationcard.module.css", "chevron"),
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
  messageBubble: requireClass(styles.messageBubble, "notificationcard.module.css", "messageBubble"),
  resultTable: requireClass(styles.resultTable, "notificationcard.module.css", "resultTable"),
  resultKey: requireClass(styles.resultKey, "notificationcard.module.css", "resultKey"),
  resultValue: requireClass(styles.resultValue, "notificationcard.module.css", "resultValue"),
  resultNone: requireClass(styles.resultNone, "notificationcard.module.css", "resultNone"),
  structuredNote: requireClass(styles.structuredNote, "notificationcard.module.css", "structuredNote"),
  staticHead: requireClass(styles.staticHead, "notificationcard.module.css", "staticHead"),
};

const EXCERPT_PREVIEW = 500;
const MESSAGE_MAX = 8000;
// The structured table's bounds, the message's MESSAGE_MAX for rows and
// cells: the daemon accepts results up to a megabyte
// (delegatestore.MaxTerminalStructuredResultBytes), and a many-keyed or
// long-valued result must render a bounded window, not thousands of rows in
// one mount. The full result stays in the daemon's frame and the transcript.
const RESULT_ROWS_MAX = 100;
const RESULT_VALUE_MAX = 2000;

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
function statusIconSeat(tone: NotificationTone): { className: string; glyph: ReactNode } {
  if (tone === "error") {
    return { className: CLASS.statusIconError, glyph: <FailureGlyph /> };
  }
  return { className: CLASS.statusIcon, glyph: <ToolIcon kind={STATUS_ICON_KIND[tone]} /> };
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
  // The parser hands the excerpt already decoded (issue #3086), so this slice
  // and the tail bound work on plain text.
  const body = text.trim();
  if (body === "") return null;
  // Direction matches the parse mode: a shell excerpt (ansi) is bounded to
  // its tail, a delegate report head (non-ansi) keeps its existing
  // head-truncated preview.
  const preview = ansi
    ? boundedShellTailPreview(body)
    : body.length <= EXCERPT_PREVIEW
      ? body
      : `${body.slice(0, EXCERPT_PREVIEW)}…`;
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

// The verdict reasons the daemon writes beside a structured result it refused
// (agent/jobs.go's structuredResultReason* codes), said plainly for the one
// reader who asked for a schema and needs to know their result did not
// survive. The verbatim frame keeps the details a code cannot carry.
const STRUCTURED_RESULT_NOTES: Record<string, string> = {
  schema_result_missing: "Structured result missing: the run ended without reporting one.",
  schema_result_too_large: "Structured result too large to show.",
  schema_validation_failed: "Structured result failed schema validation.",
  schema_capture_failed: "Structured result could not be captured.",
};

// A schema row's key in words (finding_verdict → "Finding verdict"), bounded
// as a value is - a schema key can be as long as its author likes; the raw
// keys stay in the daemon's frame, which is the verbatim record.
function structuredRowLabel(key: string): string {
  const words = key.replaceAll("_", " ");
  return `${words.charAt(0).toUpperCase()}${words.slice(1)}`;
}

// One schema value as readable text: scalars as they are, arrays as a joined
// list, nested objects as their JSON (schemas that nest are rare, and their
// shape is the caller's own). An absent value says so, quietly - an empty
// cell reads as a gap.
function boundedValue(text: string): string {
  return text.length <= RESULT_VALUE_MAX ? text : `${text.slice(0, RESULT_VALUE_MAX)}…`;
}

function structuredRowValue(value: unknown): ReactNode {
  if (value === null || value === undefined || (Array.isArray(value) && value.length === 0)) {
    return <span className={CLASS.resultNone}>(none)</span>;
  }
  if (Array.isArray(value)) {
    if (value.every((entry) => typeof entry !== "object")) return boundedValue(value.map(String).join(", "));
    return boundedValue(JSON.stringify(value));
  }
  if (typeof value === "object") return boundedValue(JSON.stringify(value));
  return boundedValue(String(value));
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
      <div className={CLASS.structuredNote} data-testid="notification-structured-note">
        {STRUCTURED_RESULT_NOTES[notification.structuredResultReason ?? ""] ?? "Structured result not shown."}
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
  const shown = rows.slice(0, RESULT_ROWS_MAX);
  return (
    <table className={CLASS.resultTable} data-testid="notification-structured-result">
      <tbody>
        {shown.map(([key, value]) => (
          <tr key={key}>
            <th scope="row" className={CLASS.resultKey}>
              {boundedValue(structuredRowLabel(key))}
            </th>
            <td className={CLASS.resultValue}>{structuredRowValue(value)}</td>
          </tr>
        ))}
        {rows.length > shown.length && (
          <tr>
            <td className={CLASS.resultNone} colSpan={2}>
              (+{rows.length - shown.length} more rows)
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
  const { config } = context;
  const disclosureScope = disclosureScopeForSession(context, sessionRef);
  // The disclosure identity prefers the watch id for watch cards: two
  // watches on the same job share a jobId, and keying by it couples their
  // expand/collapse state. Content joins identity — repeat firings of one
  // watch (timer repeats especially) are distinct deliveries that expand
  // independently; keying by watch id alone re-collapses them into one.
  // Legacy frames without a watch id fall back to the job id, then the raw
  // text, exactly as before. Non-watch cards keep delegate → job → raw
  // identity untouched.
  const notificationId =
    notification.type === "watch"
      ? notification.watchId
        ? `${notification.watchId}:${notification.rawText}`
        : (notification.jobId ?? notification.rawText)
      : (notification.delegateId ?? notification.jobId ?? notification.rawText);
  const scopedNotificationId = `notification:${sessionRef ?? "default"}:${notificationId}${disclosureId ? `:${disclosureId}` : ""}`;
  const disclosureKey = scopedDisclosureId(disclosureScope, scopedNotificationId);
  const disclosureFallback =
    expandDetailsByDefault(config) || disclosureDefault(disclosureScope, scopedNotificationId, false);
  const open = isDisclosureOpen(disclosureKey, disclosureFallback);
  // Only warning earns a Chip; success + neutral recede with no chip at all
  // (the done glyph is the same neutral as any other card). Error announces
  // itself with the red FailureGlyph in the head's rail seat instead - a
  // pill reading "error" beside a title that already says "Job failed"
  // carried nothing the glyph doesn't.
  const showWarningChip = notification.tone === "warning";
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
  // cannot answer, EntityRef falls back to plain text and the click guard
  // stays off, so an unresolved name keeps toggling the row like any other
  // head text.
  const delegateLabel =
    notification.type === "delegate"
      ? [notification.name, notification.description, notification.delegateId].find(
          (part) => part !== undefined && part !== "",
        )
      : undefined;
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
  // an affordance lie, so it renders as a static line. The terms below mirror
  // the body's blocks one for one (metadata, prose, message-or-excerpt,
  // structured result, concerns; the raw disclosure is delegate-suppressed) -
  // keep them in step. Non-delegate cards always render the raw disclosure, so
  // they always have a body.
  // A structured result's presence: a record says something by its keys, and
  // any other validated shape (a top-level array or scalar schema's result)
  // by existing. An empty record is not a report - the row stays static
  // rather than expanding to a body whose only block renders nothing.
  const hasStructuredResult =
    notification.structuredResult !== undefined &&
    (!isPlainObject(notification.structuredResult) || Object.keys(notification.structuredResult).length > 0);
  const delegateHasReport =
    notification.type !== "delegate" ||
    Boolean(
      notification.message ||
        notification.prose ||
        notification.excerpt ||
        hasStructuredResult ||
        notification.structuredResultValid === false ||
        notification.concerns.length > 0,
    );
  // A delegate head with a label carries its trigger composition in place of
  // the plain secondary string; every other shape splits that string.
  const secondaryParts =
    delegateLabel === undefined && notification.secondary ? splitTrailingWord(notification.secondary) : undefined;
  const delegateTrigger =
    delegateLabel !== undefined && delegateView ? (
      // biome-ignore lint/a11y/noStaticElementInteractions: this span is a click guard, not a control - it only swallows the click so the trigger never toggles the enclosing summary; the interaction is the EntityRef trigger inside, which owns its own focus and hover lifecycle
      // biome-ignore lint/a11y/useKeyWithClickEvents: keyboard events never reach this wrapper - focus lands on the EntityRef trigger inside, and the summary's disclosure toggle is click-only, so there is no key path to guard
      <span
        onClick={(event) => {
          // A click on the name opens the delegate's card, never the row's own
          // disclosure - the same ownership OpenButton's stopPropagation gives
          // its control, plus preventDefault against the summary's native
          // activation.
          event.stopPropagation();
          event.preventDefault();
        }}
      >
        <EntityRef view={delegateView} id={notification.delegateId ?? ""} display={delegateLabel} triggerOnly />
      </span>
    ) : undefined;
  // The unresolved-name fallback (plain text instead of the trigger) and the
  // delegate secondary as the head grammar's two pieces: the leading node
  // (trigger or label, plus the ending's lead) and the trailing word the
  // chevron hugs. A reported run has no ending, so its tail holds the controls
  // alone.
  const delegateName = delegateTrigger ?? delegateLabel;
  let delegateLead: ReactNode | undefined;
  let delegateTailWord: string | undefined;
  if (delegateLabel !== undefined) {
    if (delegateEnding === undefined) {
      delegateLead = delegateName;
      delegateTailWord = "";
    } else {
      const [endingLead, endingWord] = splitTrailingWord(delegateEnding);
      delegateLead = (
        <>
          {delegateName}
          {" · "}
          {endingLead}
        </>
      );
      delegateTailWord = endingWord;
    }
  }
  // The head grammar's inputs, generalized: a delegate head with a label
  // carries its trigger composition in place of the plain secondary string;
  // every other shape splits that string exactly as before.
  const secondaryLead: ReactNode | undefined = delegateLead ?? secondaryParts?.[0];
  const secondaryTailWord: string | undefined = delegateTailWord ?? secondaryParts?.[1];
  const hasSecondary = secondaryLead !== undefined;
  // The title-only branch (no secondary) splits the title the same way, so
  // its chevron rides the title's final word atomically. Computed eagerly: the
  // branch that reads it is the one where no secondary exists.
  const titleParts = splitTrailingWord(notification.title);
  // The head suppresses the native details marker (the old text glyphs were
  // font-dependent and overhung their boxes - see widgets/chevron), so this is
  // the reader's only expand affordance: the same trailing <Chevron> idiom
  // ThinkBlock's summary uses. EVERY branch renders it inside an atomic
  // secondaryTail unit with the words it opens - after the Open control where
  // one exists (the ToolRow grammar's "text, Open, chevron" order) - so a line
  // that runs out moves the whole unit and the glyph can never strand alone
  // on a wrapped line.
  const chevron = (
    <span
      className={CLASS.chevron}
      aria-hidden="true"
      data-open={open ? "true" : "false"}
      data-testid="notification-chevron"
    >
      <Chevron />
    </span>
  );
  // The head's Open control, hoisted like the chevron: every seat renders the
  // same wrapped control, so the label and wrapper cannot drift between them.
  const openControl = transcriptRef ? (
    <span className={CLASS.openTrailing}>
      <OpenTranscriptButton transcriptRef={transcriptRef} parentRef={sessionRef} label="Open subagent" />
    </span>
  ) : null;
  // Seated first in the summary, before the chip and title: the glyph rides
  // the transcript's icon rail (see notificationcard.module.css's .statusIcon;
  // error's full-strength variant is .statusIconError). Decorative - the
  // title's own words ("Job completed", "Job failed") already name the status
  // for assistive tech, so exposing the glyph would announce the same fact
  // twice (the same ruling as every other rail icon and ThinkBlock's bulb) -
  // which is also why FailureGlyph's own accessible name is silenced by the
  // aria-hidden seat here, unlike its inline use on failed tool rows where it
  // is the only failure signal.
  const seat = statusIconSeat(notification.tone);
  const statusIcon = (
    <span className={seat.className} data-testid="notification-status-icon" aria-hidden="true">
      {seat.glyph}
    </span>
  );
  if (!delegateHasReport) {
    return (
      <div className={CLASS.staticHead} data-testid="notification-card" data-tone={notification.tone}>
        {statusIcon}
        {showWarningChip && <Chip tone="attention">warning</Chip>}
        <span className={CLASS.headingText}>
          <span className={CLASS.title}>{notification.title}</span>
          {/* An unlabeled delegate keeps its ending and Open control: the
              secondary falls back to the parser's own composition (which
              already carries the ending) when no name/description/id exists,
              and the Open control renders wherever a transcript_ref does -
              neither may be gated on the label. */}
          {(delegateName !== undefined || notification.secondary !== "" || openControl !== null) && (
            <span className={CLASS.secondary}>
              {delegateName !== undefined ? delegateName : notification.secondary}
              {delegateName !== undefined && delegateEnding ? ` · ${delegateEnding}` : null}
              {openControl}
            </span>
          )}
        </span>
      </div>
    );
  }
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
        {statusIcon}
        {showWarningChip && <Chip tone="attention">warning</Chip>}
        <span className={CLASS.headingText}>
          {hasSecondary || !transcriptRef ? (
            hasSecondary ? (
              <span className={CLASS.title}>{notification.title}</span>
            ) : (
              // Title-only mirrors the secondary treatment: the FINAL word and
              // the chevron are one atomic unit, so the glyph hugs the title's
              // last line at any width (a whole-title unit parks it at the
              // unit's right edge) and never strands.
              <>
                <span className={CLASS.title}>{titleParts[0]}</span>
                <span className={CLASS.secondaryTail}>
                  <span className={`${CLASS.title} ${CLASS.secondaryTailText}`}>{titleParts[1]}</span>
                  {chevron}
                </span>
              </>
            )
          ) : (
            <span className={CLASS.secondaryTail}>
              <span className={`${CLASS.title} ${CLASS.secondaryTailText}`}>{notification.title}</span>
              {openControl}
              {chevron}
            </span>
          )}
          {hasSecondary ? (
            <span className={CLASS.secondary}>
              {transcriptRef ? (
                <>
                  {secondaryLead}
                  <span className={CLASS.secondaryTail}>
                    <span className={CLASS.secondaryTailText}>{secondaryTailWord}</span>
                    {openControl}
                    {chevron}
                  </span>
                </>
              ) : (
                <>
                  {secondaryLead}
                  <span className={CLASS.secondaryTail}>
                    <span className={CLASS.secondaryTailText}>{secondaryTailWord}</span>
                    {chevron}
                  </span>
                </>
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
                {notification.prose}
              </pre>
            )}
            {notification.message ? (
              // The message's seat by type, the statusIconSeat idiom: a
              // delegate's report renders in the sans bubble, every other
              // lane in the excerpt treatment - one Markdown call either way.
              <div
                className={notification.type === "delegate" ? CLASS.messageBubble : CLASS.excerpt}
                data-testid={notification.type === "delegate" ? "notification-message" : "notification-field-excerpt"}
              >
                <Markdown source={notification.message.slice(0, MESSAGE_MAX)} />
              </div>
            ) : (
              <Excerpt text={notification.excerpt} ansi={notification.jobType === "shell"} />
            )}
            <StructuredResult notification={notification} />
            {notification.concerns.length > 0 && (
              <div className={CLASS.concerns}>Concerns: {notification.concerns.join("; ")}</div>
            )}
            {notification.type !== "delegate" && (
              <details className={CLASS.raw} data-testid="notification-raw-disclosure">
                <summary className={CLASS.summary}>Raw notification</summary>
                <pre className={CLASS.rawBody} data-testid="notification-raw">
                  {notification.rawText}
                </pre>
              </details>
            )}
          </div>
        </Card>
      )}
    </details>
  );
}
