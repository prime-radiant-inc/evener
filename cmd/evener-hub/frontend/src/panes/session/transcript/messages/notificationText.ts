// What a notification card puts on screen, and the bounds it puts on each
// field. The card paints these strings and the session's Find reads the same
// ones, so a counted match is always a word a reader can see. Free of the
// card's renderers: Find and the palette read it without mounting a row.
import { AnsiTailBuffer, isPlainObject, type ParsedNotification, parseAnsiLines } from "@evener/appwire-client";

const EXCERPT_PREVIEW = 500;
const MESSAGE_MAX = 8000;
// The structured table's bounds, the message's MESSAGE_MAX for rows and
// cells: the daemon accepts results up to a megabyte
// (delegatestore.MaxTerminalStructuredResultBytes), and a many-keyed or
// long-valued result must render a bounded window, not thousands of rows in
// one mount. The full result stays in the daemon's frame and the transcript.
const RESULT_ROWS_MAX = 100;
const RESULT_VALUE_MAX = 2000;

/** The words a delegate report's message is rendered from: bounded as the
 * card's Markdown bubble bounds it. */
export function boundedMessage(message: string): string {
  return message.slice(0, MESSAGE_MAX);
}

/** The name a delegate card's head shows, in the card's own preference order:
 * nothing for another kind of card. */
export function delegateCardLabel(notification: ParsedNotification): string | undefined {
  if (notification.type !== "delegate") return undefined;
  return [notification.name, notification.description, notification.delegateId].find(
    (part) => part !== undefined && part !== "",
  );
}

/** Whether the card renders an expandable body at all. A delegate head whose
 * body would say nothing - a machinery stop, a failure whose whole error is
 * its ending - is the whole row: a disclosure that expanded to an empty body
 * would be an affordance lie, so it renders as a static line. Job and watch
 * cards always keep their raw disclosure, so they always have a body. */
export function notificationRendersBody(notification: ParsedNotification): boolean {
  if (notification.type !== "delegate") return true;
  // A structured result's presence: a record says something by its keys, and
  // any other validated shape (a top-level array or scalar schema's result) by
  // existing. An empty record is not a report.
  const result = notification.structuredResult;
  const hasStructuredResult = result !== undefined && (!isPlainObject(result) || Object.keys(result).length > 0);
  return Boolean(
    notification.message ||
      notification.prose ||
      notification.excerpt ||
      hasStructuredResult ||
      notification.structuredResultValid === false ||
      notification.concerns.length > 0,
  );
}

/** The structured table's visible rows, bounded as the card bounds them. */
export function shownResultRows<T>(rows: readonly T[]): readonly T[] {
  return rows.slice(0, RESULT_ROWS_MAX);
}

/** How many rows a full result holds beyond the table's window. */
export function hiddenResultRows(total: number): number {
  return Math.max(0, total - RESULT_ROWS_MAX);
}

/** An empty cell's word, as the card shows it. */
export const STRUCTURED_NONE = "(none)";

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

/** An excerpt as its card previews it: empty where the card renders nothing,
 * a shell excerpt's ANSI tail, anything else's head-truncated preview. */
export function excerptPreview(decoded: string, ansi: boolean): string {
  // The parser hands the excerpt already decoded (issue #3086), so this slice
  // and the tail bound work on plain text. Direction matches the parse mode: a
  // shell excerpt (ansi) is bounded to its tail, a delegate report head
  // (non-ansi) keeps its existing head-truncated preview.
  const body = decoded.trim();
  if (body === "") return "";
  if (ansi) return boundedShellTailPreview(body);
  return body.length <= EXCERPT_PREVIEW ? body : `${body.slice(0, EXCERPT_PREVIEW)}…`;
}

/** The preview's words as they are painted: one reading per parsed ANSI line,
 * the lines separated as the card separates them. */
export function excerptText(decoded: string, ansi: boolean): string {
  const preview = excerptPreview(decoded, ansi);
  if (!ansi || preview === "") return preview;
  return parseAnsiLines(preview)
    .map((line) => line.map((run) => run.text).join(""))
    .join("\n");
}

// A schema row's key in words (finding_verdict → "Finding verdict"), bounded
// as a value is - a schema key can be as long as its author likes; the raw
// keys stay in the daemon's frame, which is the verbatim record.
export function structuredRowLabel(key: string): string {
  const words = key.replaceAll("_", " ");
  return `${words.charAt(0).toUpperCase()}${words.slice(1)}`;
}

/** A value bounded to the table cell's window. */
export function boundedValue(text: string): string {
  return text.length <= RESULT_VALUE_MAX ? text : `${text.slice(0, RESULT_VALUE_MAX)}…`;
}

/** A schema value as the table's cell says it: scalars as they are, arrays as
 * a joined list, nested objects as their JSON, an absent value in words. */
export function structuredValueText(value: unknown): string {
  if (value === null || value === undefined || (Array.isArray(value) && value.length === 0)) return STRUCTURED_NONE;
  if (Array.isArray(value)) {
    return boundedValue(
      value.every((entry) => typeof entry !== "object") ? value.map(String).join(", ") : JSON.stringify(value),
    );
  }
  if (typeof value === "object") return boundedValue(JSON.stringify(value));
  return boundedValue(String(value));
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

/** Why a refused structured result is not shown, in the words the card uses. */
export function structuredResultNote(reason: string | undefined): string {
  return STRUCTURED_RESULT_NOTES[reason ?? ""] ?? "Structured result not shown.";
}

/** The disclosure identity NotificationCard gives one delivery: the session,
 * the card's own identity (a watch's, a delegate's, a job's, else the frame),
 * and the delivery's stable discriminator. Find opens the same key. */
export function notificationDisclosureId(
  notification: ParsedNotification,
  sessionRef: string | undefined,
  deliveryId?: string,
): string {
  // The disclosure identity prefers the watch id for watch cards: two watches
  // on the same job share a jobId, and keying by it couples their
  // expand/collapse state. Content joins identity - repeat firings of one watch
  // (timer repeats especially) are distinct deliveries that expand
  // independently. Legacy frames without a watch id fall back to the job id,
  // then the raw text. Non-watch cards keep delegate → job → raw identity.
  const notificationId =
    notification.type === "watch"
      ? notification.watchId
        ? `${notification.watchId}:${notification.rawText}`
        : (notification.jobId ?? notification.rawText)
      : (notification.delegateId ?? notification.jobId ?? notification.rawText);
  return `notification:${sessionRef ?? "default"}:${notificationId}${deliveryId ? `:${deliveryId}` : ""}`;
}

/** The raw frame's disclosure key, beside the card's own. */
export function rawDisclosureId(disclosure: string): string {
  return `${disclosure}:raw`;
}
