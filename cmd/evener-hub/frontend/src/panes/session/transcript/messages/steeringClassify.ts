// Structured steering-notification parsing: <job-notification …> blocks and
// the fixed "Observer callback:\n" header are markup, not prose, so reading
// them is parsing rather than guessing.
//
// NOTHING EMITS THE "Observer callback:" HEADER ANY MORE. Its producer was
// deleted with agent.EntryWatchDelivery (kata z5fm), and a watch-origin
// observer's terminal communicate now reaches its parent as the ordinary
// <delegate-notification> frame. parseObserverCallback stays because
// transcripts are DURABLE: a thread recorded while the producer existed still
// replays that steering turn through here, and a reader must render it the way
// it was written. Treat it as a reader of history, not of anything live. SteeringItem.tsx routes daemon steering on
// ItemModel.steeringKind (the wire's events.SteeringKind*, named at the
// injection site) instead of inferring one from wording, so nothing here
// decides a "kind" any more - this file only extracts notification cards,
// which stay content-driven because structured markup can't false-positive
// the way a prose pattern could (see parseSteeringNotifications below).

export type NotificationTone = "success" | "warning" | "error" | "neutral";

export interface ParsedNotification {
  type: string; // delegate | job | watch | watch-send | observer-callback
  title: string;
  tone: NotificationTone;
  secondary: string; // job_type · exit N · reason (quiet plumbing stays in raw)
  jobId?: string;
  jobType?: string;
  delegateId?: string;
  watchId?: string;
  description?: string;
  status?: string;
  reason?: string;
  outputBytes?: number;
  exitCode?: number;
  transcriptRef?: string;
  excerpt: string;
  prose?: string; // body text before any excerpt marker (timers: sentence + note), raw entities
  message?: string; // a communicate envelope's message (rendered as markdown)
  concerns: string[];
  rawText: string; // the verbatim block, always kept inspectable
}

const REF_PART_PATTERN = /^[A-Za-z0-9._~-]+$/;

// isValidTranscriptRef mirrors appwire/refs.go's qualified source:thread
// grammar. Keeping the check at the parser boundary means malformed daemon
// metadata cannot become a dead navigation control in the card.
export function isValidTranscriptRef(value: string | undefined): value is string {
  if (value === undefined || value === "") return false;
  const separator = value.indexOf(":");
  if (separator <= 0 || separator === value.length - 1) return false;
  const source = value.slice(0, separator);
  const thread = value.slice(separator + 1);
  return REF_PART_PATTERN.test(source) && REF_PART_PATTERN.test(thread) && !thread.includes("..");
}

function stripSystemReminder(text: string): string {
  return text
    .replace(/^\s*<SYSTEM-REMINDER>\s*/i, "")
    .replace(/\s*<\/SYSTEM-REMINDER>\s*$/i, "")
    .trim();
}

function parseQuotedAttrs(src: string): Record<string, string> {
  const attrs: Record<string, string> = {};
  for (const m of src.matchAll(/([A-Za-z0-9_:-]+)="([^"]*)"/g)) {
    const key = m[1];
    const value = m[2];
    if (key !== undefined && value !== undefined) attrs[key] = value;
  }
  return attrs;
}

function optionalNonNegativeInteger(attrs: Record<string, string>, key: string): number | undefined {
  const raw = attrs[key]?.trim();
  if (raw === undefined || raw === "") return undefined;
  const value = Number(raw);
  return Number.isSafeInteger(value) && value >= 0 ? value : undefined;
}

type JobDisposition = "success" | "failure" | "cancelled" | "stopped" | "unknown";

interface JobNotificationAnalysis {
  disposition: JobDisposition;
  exitCode?: number;
}

function optionalSignedInteger(raw: string | undefined): number | undefined {
  const text = (raw ?? "").trim();
  if (!/^-?\d+$/.test(text)) return undefined;
  const value = Number(text);
  return Number.isSafeInteger(value) ? value : undefined;
}

function analyzeJobNotification(
  attrs: Record<string, string>,
  communicate: CommunicateEnvelope | null,
): JobNotificationAnalysis {
  const outerStatus = (attrs.status ?? "").trim().toLowerCase();
  const outerEvent = (attrs.event ?? "").trim().toLowerCase();
  const communicateStatus = (communicate?.status ?? "").trim().toLowerCase();
  const status = outerStatus || outerEvent || communicateStatus;
  const exitCode = optionalSignedInteger(attrs.exit_code);

  let disposition: JobDisposition = "unknown";
  if (status === "failed" || status === "error" || status === "exhausted" || status.includes("fail")) {
    disposition = "failure";
  } else if (status === "cancelled") {
    disposition = "cancelled";
  } else if (status === "stopped") {
    disposition = "stopped";
  } else if (status === "completed" || status === "done") {
    disposition = exitCode !== undefined && exitCode !== 0 ? "failure" : "success";
  } else if (exitCode !== undefined && exitCode !== 0) {
    disposition = "failure";
  }

  return exitCode === undefined ? { disposition } : { disposition, exitCode };
}

// A notification-text fragment in source order: either a raw
// <job|delegate-notification> block (still unparsed - the caller classifies
// it) or a trimmed span of text between/around blocks. Splitting into
// ordered fragments - instead of collecting every block and handing back one
// merged leftover string - is what lets a caller keep interstitial text
// pinned to its original position between two notification cards (issue #48)
// rather than collapsing it into a single trailing divider.
interface NotificationBlockFragment {
  kind: "block" | "text";
  text: string;
}

// splitJobNotificationBlocks extracts each individual <job-notification …>…
// </job-notification> block. The per-block match MUST be non-greedy: a single
// steering turn can carry several blocks joined by newlines, and a greedy match
// would span the first opening tag to the last closing tag and aggregate
// distinct notifications into one (contracts §17).
function splitNotificationBlocks(text: string): NotificationBlockFragment[] {
  const pattern = /<(job|delegate)-notification\s+[^>]*>[\s\S]*?<\/\1-notification>/g;
  const fragments: NotificationBlockFragment[] = [];
  let cursor = 0;
  for (const match of text.matchAll(pattern)) {
    const index = match.index ?? 0;
    const before = text.slice(cursor, index).trim();
    if (before) fragments.push({ kind: "text", text: before });
    fragments.push({ kind: "block", text: match[0] });
    cursor = index + match[0].length;
  }
  const after = text.slice(cursor).trim();
  if (after) fragments.push({ kind: "text", text: after });
  return fragments;
}

function parseDelegateNotification(block: string): ParsedNotification | null {
  const match = block.match(/^<delegate-notification\s+([^>]*)>([\s\S]*)<\/delegate-notification>$/);
  if (!match) return null;
  const attrs = parseQuotedAttrs(match[1] ?? "");
  const body = (match[2] ?? "").trim();
  const { excerpt } = splitNotificationExcerpt(body);
  const communicate = parseCommunicateEnvelope(decodeNotificationEntities(excerpt));
  const tone = notificationTone(attrs, communicate);
  const transcriptRef = isValidTranscriptRef(attrs.transcript_ref) ? attrs.transcript_ref : undefined;
  const description = decodeNotificationEntities(attrs.description ?? "").trim();
  const status = (attrs.status || attrs.event || "notification").trim();
  const reason = attrs.reason?.trim();
  const secondary = [description || attrs.delegate_id?.trim(), tone === "error" || tone === "warning" ? reason : ""]
    .filter(Boolean)
    .join(" · ");
  return {
    type: "delegate",
    title: status ? `Delegate ${status}` : "Delegate notification",
    tone,
    secondary,
    delegateId: attrs.delegate_id?.trim() || undefined,
    description: description || undefined,
    status: attrs.status?.trim() || undefined,
    reason: reason || undefined,
    transcriptRef,
    excerpt,
    message: communicate?.message || undefined,
    concerns: communicate?.concerns ?? [],
    rawText: block,
  };
}

function splitNotificationExcerpt(body: string): { prose: string; excerpt: string } {
  const marker = "\nexcerpt:\n";
  const idx = body.indexOf(marker);
  if (idx === -1) return { prose: body.trim(), excerpt: "" };
  return { prose: body.slice(0, idx).trim(), excerpt: body.slice(idx + marker.length).trim() };
}

function compactStringArray(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return value.map((v) => String(v ?? "").trim()).filter(Boolean);
}

// decodeNotificationEntities is the paired decoder for agent/job_notify.go's
// escapeNotificationText: the producer HTML-entity-escapes &, <, >, and "
// before interpolating job/watch-derived text into a <job-notification>
// wrapper (kata 77sf), so text extracted from that wrapper - a body excerpt,
// or (below) a delegate's communicate envelope, whose own JSON quotes are
// escaped the same way as any other body content - must be decoded back
// before use. &amp; is decoded LAST so double-escaped content only unwraps
// one level. Exported so NotificationCard.tsx shares this one decoder rather
// than keeping a second copy for its own excerpt display.
export function decodeNotificationEntities(text: string): string {
  return text
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/&quot;/g, '"')
    .replace(/&#0*39;|&#x0*27;/gi, "'")
    .replace(/&amp;/g, "&");
}

// escapeNotificationEntities is decodeNotificationEntities run backward:
// the producer-side escaping (agent/job_notify.go's escapeNotificationText)
// for text interpolated into a <job-notification> wrapper. watchProse builds
// synthesized card prose from an already-DECODED reason, so it re-escapes
// the synthesis: prose is stored escaped-form throughout (passthrough bodies
// arrive escaped), and NotificationCard decodes every prose exactly once.
// Without the re-escape a literal "&lt;" in a matched pattern would decode
// twice and display wrong (combined RoboRev review).
export function escapeNotificationEntities(text: string): string {
  return text.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

interface CommunicateEnvelope {
  message: string;
  status: string;
  concerns: string[];
}

// A communicate tool result rides the excerpt as a JSON envelope
// {message, data:{status, concerns, …}} (agent/session_tools_communicate.go).
// Only message/status/concerns are read here - the deeper facts list
// (commit_hashes/test_summary/artifacts) the legacy card rendered is a conscious
// scope-out for this stream (see w8-t3-report).
function parseCommunicateEnvelope(text: string): CommunicateEnvelope | null {
  const raw = text.trim();
  if (!raw.startsWith("{")) return null;
  try {
    const parsed = JSON.parse(raw);
    if (typeof parsed !== "object" || parsed === null) return null;
    const data = typeof parsed.data === "object" && parsed.data ? parsed.data : {};
    return {
      message: String(parsed.message ?? "").trim(),
      status: String(data.status ?? "").trim(),
      concerns: compactStringArray(data.concerns),
    };
  } catch {
    return null;
  }
}

function notificationTone(attrs: Record<string, string>, communicate: CommunicateEnvelope | null): NotificationTone {
  const outerStatus = (attrs.status ?? "").toLowerCase();
  const outerEvent = (attrs.event ?? "").toLowerCase();
  const communicateStatus = (communicate?.status ?? "").toLowerCase();
  const exitCode = (attrs.exit_code ?? "").trim();
  const concerns = (communicate?.concerns.length ?? 0) > 0;
  if (
    outerStatus.includes("fail") ||
    outerEvent.includes("fail") ||
    outerStatus === "error" ||
    outerEvent === "error" ||
    outerStatus === "exhausted" ||
    (exitCode !== "" && exitCode !== "0")
  ) {
    return "error";
  }
  // A failed delivery earns attention even on paths that short-circuit
  // watch frames below: the watcher missed something. Defense in depth —
  // watch diagnostics normally arrive as job notifications (which check
  // this in jobNotificationTone), but no watch-shaped failure must ever
  // read as an expected outcome (combined RoboRev review).
  if (isWatchDeliveryFailure(decodeNotificationEntities(attrs.reason ?? "").trim())) {
    return "warning";
  }
  // Mockups 23-job-watch §E: a fired watch is the expected outcome, never
  // something needing a human — no watch notification earns a tone chip,
  // ever (not the timer, not a match, not even the budget auto-clear: the
  // words carry it). Watch events short-circuit before the concerns/
  // cancelled/stopped warning arm below, which a watch must never reach.
  if (outerEvent === "watch" || outerEvent === "watch_send" || outerStatus === "watch") {
    return "neutral";
  }
  const status = communicateStatus || outerStatus || outerEvent;
  if (concerns || status === "cancelled" || status === "stopped") {
    return "warning";
  }
  if (status === "completed" || status === "done") return "success";
  return "neutral";
}

// isWatchDeliveryFailure reports whether a (decoded) watch reason names a
// failed delivery rather than a trigger: the send-rail diagnostics
// ("watch send failed:", "watch send dropped/pending/stable-enqueue state
// failed:", "watch send evicted:" — agent/job_watch.go's
// watchSendDiagnosticNotification sites), the feed guard
// ("output dropped:"), and the runtime fallback drop ("child unreachable:"
// — renderUnreachableChildPendingsWithLoaders emits the DiagnosticReason
// directly, outside the "watch send " family). The "watch send " prefix
// with its trailing space covers the send-rail family present and future. A
// failed delivery is not a firing — it titles and tones as a failure so it
// cannot present as a successful trigger (combined RoboRev review).
function isWatchDeliveryFailure(reason: string): boolean {
  return (
    reason.startsWith("watch send ") || reason.startsWith("output dropped:") || reason.startsWith("child unreachable:")
  );
}

function jobNotificationTone(
  attrs: Record<string, string>,
  communicate: CommunicateEnvelope | null,
  analysis: JobNotificationAnalysis,
): NotificationTone {
  if (analysis.disposition === "failure") return "error";
  // A failed delivery earns attention even though it rides a watch frame:
  // the watcher missed something. Checked before the watch short-circuit
  // below, which is for expected outcomes (fires, teardowns).
  if (isWatchDeliveryFailure(decodeNotificationEntities(attrs.reason ?? "").trim())) {
    return "warning";
  }
  // Same §E rule as notificationTone above: watch and watch-send deliveries
  // are expected outcomes. The budget auto-clear notice carries the same
  // watch event as a fire (agent/job_watch.go's watchNotification), so this
  // covers it too — its "matched 50 times" words carry the signal. The check
  // mirrors the parser's type detection (event OR status "watch"): a
  // status-only watch frame is still a watch delivery, never a chip
  // (combined RoboRev review).
  const event = (attrs.event ?? "").trim().toLowerCase();
  const status = (attrs.status ?? "").trim().toLowerCase();
  if (event === "watch" || event === "watch_send" || status === "watch") {
    return "neutral";
  }
  if ((communicate?.concerns.length ?? 0) > 0 || analysis.disposition === "stopped") {
    return "warning";
  }
  if (analysis.disposition === "success") return "success";
  return "neutral";
}

function titleForJobNotification(attrs: Record<string, string>, type: string, prose?: string): string {
  if (type === "watch-send") return "Watch delivered";
  if (type === "watch") {
    // Derive the title from the trigger, not from job presence (RoboRev PR
    // #954): only an output_match fire is an "Output matched on …". A timer
    // prose lead ("Timer fired …") keeps a timer title; an event fire
    // ("event: …" reason) names the event.
    // The reason is producer-escaped (escapeNotificationText) and
    // parseQuotedAttrs does not decode, so decode before matching — a
    // pattern containing & < > must title decoded (RoboRev PR #954 review 3).
    const reason = decodeNotificationEntities(attrs.reason ?? "").trim();
    // Teardown notices are endings, never firings (RoboRev PR #954 review 3):
    // watchEndedUnfiredMessage / watchLostAtRestartMessage start with
    // "watch ended:", watchBudgetClearedMessage with "watch cleared:". The
    // prose/reason carries which watch and why, so the title stays short.
    if (reason.startsWith("watch ended:")) return "Watch ended";
    if (reason.startsWith("watch cleared:")) return "Watch auto-cleared";
    // A failed delivery is not a firing — it titles as what it is, never
    // "Watch fired on <job>" (combined RoboRev review). Checked before the
    // trigger fallthroughs below.
    if (isWatchDeliveryFailure(reason)) return "Watch delivery failed";
    const jobId = (attrs.job_id ?? "").trim();
    const outputMatch = /^output_match:\s*(.+)$/.exec(reason)?.[1]?.trim();
    if (outputMatch && jobId) return `Output matched on ${jobId}`;
    if (/^timer fired/i.test(prose ?? "")) return "Timer fired";
    const eventFire = /^event:\s*(.+)$/.exec(reason)?.[1]?.trim();
    if (eventFire && jobId) return `Event on ${jobId}: ${eventFire}`;
    if (jobId) return `Watch fired on ${jobId}`;
    return "Watch triggered";
  }
  const status = (attrs.status || attrs.event || "notification").trim();
  if (!status) return "Job notification";
  return `Job ${status}`;
}

// Local duration humanizer for timer secondaries. jobWatch.tsx owns the
// canonical humanizeSeconds/humanizeInterval, but this file is a message
// parser and must not import a tool renderer for one of its descriptors —
// the parser stays independent of any single tool's rendering layer — so the
// tiny minutes math is duplicated here instead of cross-imported. The hour
// branch matches the renderer's grammar (whole hours "1h", else "1h05m") so
// a day-long timer never reads "after 1440m" (RoboRev PR #954 combined
// review: valid timers run to 86,400s).
function humanizeTimerAfter(totalSeconds: number): string {
  if (totalSeconds < 60) return `after ${Math.round(totalSeconds)}s`;
  const totalMinutes = Math.floor(totalSeconds / 60);
  const leftoverSeconds = Math.round(totalSeconds % 60);
  if (totalMinutes < 60) {
    return leftoverSeconds === 0
      ? `after ${totalMinutes}m`
      : `after ${totalMinutes}m${String(leftoverSeconds).padStart(2, "0")}s`;
  }
  const hours = Math.floor(totalMinutes / 60);
  const minutes = totalMinutes % 60;
  return minutes === 0 ? `after ${hours}h` : `after ${hours}h${String(minutes).padStart(2, "0")}m`;
}

function humanizeTimerEvery(totalSeconds: number): string {
  if (totalSeconds < 60) return `every ${Math.round(totalSeconds)}s`;
  const totalMinutes = Math.floor(totalSeconds / 60);
  const leftoverSeconds = Math.round(totalSeconds % 60);
  if (totalMinutes < 60) {
    return leftoverSeconds === 0
      ? `every ${totalMinutes}m`
      : `every ${totalMinutes}m${String(leftoverSeconds).padStart(2, "0")}s`;
  }
  const hours = Math.floor(totalMinutes / 60);
  const minutes = totalMinutes % 60;
  return minutes === 0 ? `every ${hours}h` : `every ${hours}h${String(minutes).padStart(2, "0")}m`;
}

// timerSecondaryFromProse humanizes a bare timer reason ("after"/"repeat")
// from the prose lead's own seconds ("Timer fired after 300s." /
// "Timer fired (every 300s).", agent/job_notify.go). Undefined when the prose
// does not match — the caller falls back to the raw reason.
function timerSecondaryFromProse(reason: string, prose: string | undefined): string | undefined {
  if (reason !== "after" && reason !== "repeat") return undefined;
  const seconds = /^Timer fired (?:after|\(every) (\d+)s/.exec((prose ?? "").trim())?.[1];
  if (seconds === undefined) return undefined;
  const totalSeconds = Number(seconds);
  if (!Number.isFinite(totalSeconds)) return undefined;
  return reason === "after" ? humanizeTimerAfter(totalSeconds) : humanizeTimerEvery(totalSeconds);
}

function notificationSecondary(
  attrs: Record<string, string>,
  tone: NotificationTone,
  description: string,
  analysis: JobNotificationAnalysis,
  notificationType?: string,
  prose?: string,
): string {
  // A watch card's secondary names the trigger (the output_match / event the
  // watch fired on) — the one producer field that says what happened. The
  // job_type/status/output echo attrs stay out (NotificationCard suppresses
  // them for watch type too).
  // The reason is producer-escaped (see titleForJobNotification above), so it
  // is decoded here too; a bare timer reason ("after"/"repeat") humanizes
  // from the prose lead's seconds instead ("after 5m" / "every 5m"), falling
  // back to the raw reason when the prose does not match (RoboRev PR #954
  // review 3).
  if (notificationType === "watch") {
    const reason = decodeNotificationEntities(attrs.reason ?? "").trim();
    return timerSecondaryFromProse(reason, prose) ?? reason;
  }
  const bits: string[] = [];
  const type = (attrs.job_type ?? "").trim();
  if (description) bits.push(description);
  else if (type && type !== "job") bits.push(type);
  if (analysis.disposition === "failure" && analysis.exitCode !== undefined && analysis.exitCode !== 0) {
    bits.push(`exit ${analysis.exitCode}`);
  }
  const reason = (attrs.reason ?? "").trim();
  if (reason && (tone === "error" || tone === "warning")) bits.push(reason);
  return bits.join(" · ");
}

// watchProse is a watch notification's card prose. Most watch bodies ARE
// their content (timer sentences, teardown notices, event bodies) and pass
// through verbatim. The one exception is a job-targeted CONDITION fire: the
// producer's non-empty-job_id path emits only the generic "Job <id> <event>."
// sentence (formatJobNotificationBlock's fallthrough — the excerpt is ignored
// for watch frames), keeping the trigger in the escaped reason attr. For that
// shape the card synthesizes prose from the reason so the expanded card shows
// what fired instead of the generic sentence (RoboRev PR #954 combined
// review). The synthesis names the trigger, never invented output context —
// the reason carries the matched pattern / event name, not surrounding
// output.
function watchProse(attrs: Record<string, string>, bodyText: string): string {
  const jobId = (attrs.job_id ?? "").trim();
  if (!jobId) return bodyText;
  const reason = decodeNotificationEntities(attrs.reason ?? "").trim();
  // Teardown notices ("watch ended:" / "watch cleared:") keep their own
  // prose when the producer emitted it — but a job-targeted teardown's body
  // is the same generic "Job <id> watch." sentence as a condition fire's
  // (formatJobNotificationBlock's non-empty-JobID fallthrough covers every
  // reason). Only a body that already carries the reason passes through; a
  // generic body falls to the reason below (combined RoboRev review). The
  // comparison decodes the body first: the body is escaped-form and the
  // reason decoded-form, so a raw includes() misses whenever either carries
  // an entity.
  if (reason.startsWith("watch ended:") || reason.startsWith("watch cleared:")) {
    if (decodeNotificationEntities(bodyText).includes(reason)) return bodyText;
    return escapeNotificationEntities(reason);
  }
  // Timer fires with a job_id are not a producer shape (timers emit watch_id
  // with an empty job_id), but if one ever arrives the body is already its
  // content — leave it alone.
  if (/^timer fired/i.test(bodyText)) return bodyText;
  const outputMatch = /^output_match:\s*(.+)$/.exec(reason)?.[1]?.trim();
  if (outputMatch) return escapeNotificationEntities(`Matched output_match: ${outputMatch} on ${jobId}.`);
  const eventFire = /^event:\s*(.+)$/.exec(reason)?.[1]?.trim();
  if (eventFire) return escapeNotificationEntities(`Watch event triggered: ${eventFire} on ${jobId}.`);
  if (/^progress_tick$/.test(reason)) return escapeNotificationEntities(`Progress tick on ${jobId}.`);
  // Not a recognized trigger reason — the body is whatever the producer
  // sent; only the exact generic sentence is worth replacing, with the
  // escaped reason as the honest fallback.
  const generic = new RegExp(`^Job ${escapeRegExp(jobId)} \\S+\\.`);
  if (generic.test(bodyText.trim())) return escapeNotificationEntities(reason) || bodyText;
  return bodyText;
}

function escapeRegExp(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function parseJobNotification(block: string): ParsedNotification | null {
  const m = block.match(/^<job-notification\s+([^>]*)>([\s\S]*)<\/job-notification>$/);
  if (!m) return null;
  const attrs = parseQuotedAttrs(m[1] ?? "");
  const bodyText = (m[2] ?? "").trim();
  let type = "job";
  // A watch fire names its watched job (watchNotificationFromWatch always
  // sets JobID — agent/job_watch.go), so event/status "watch" wins over the
  // job_id presence check: a job-targeted condition fire is still a watch
  // delivery, with prose worth keeping and no echo metadata worth showing.
  if (attrs.event === "watch" || attrs.status === "watch") type = "watch";
  if (attrs.event === "watch_send") type = "watch-send";
  // A job-targeted watch fire's body is the producer's generic "Job <id>
  // <event>." sentence (formatJobNotificationBlock's fallthrough: the excerpt
  // is ignored for watch frames, so there is no matched output to carry).
  // The reason attr holds the actual trigger ("output_match: …" /
  // "event: …"), so the card synthesizes its prose from the reason instead
  // of showing the generic sentence (RoboRev PR #954 combined review).
  // Teardown notices ("watch ended:" / "watch cleared:") and timer/event
  // bodies already carry their own prose and pass through untouched.
  const { prose, excerpt } =
    type === "watch" ? { prose: watchProse(attrs, bodyText), excerpt: "" } : splitNotificationExcerpt(bodyText);
  // A communicate envelope can only ride a delegate's report (the delegate
  // calls communicate to produce it - agent/session_tools_communicate.go).
  // Gate on the actual job type, not on whether the excerpt happens to parse
  // as JSON with message/data keys: shell stdout is literal output even when
  // it coincidentally looks like an envelope (kata 9cnq). The excerpt is
  // producer-escaped (kata 77sf) - decode before parsing, or the envelope's
  // own JSON quotes (now &quot;) are no longer valid JSON syntax. excerpt
  // itself stays raw/undecoded: NotificationCard's Excerpt decodes it
  // separately, only when there is no communicate message to show instead.
  const communicate =
    attrs.job_type === "delegate" ? parseCommunicateEnvelope(decodeNotificationEntities(excerpt)) : null;
  const transcriptRef = isValidTranscriptRef(attrs.transcript_ref) ? attrs.transcript_ref : undefined;
  const description = decodeNotificationEntities(attrs.description ?? "").trim();
  const analysis = analyzeJobNotification(attrs, communicate);
  const tone = jobNotificationTone(attrs, communicate, analysis);
  return {
    type,
    title: titleForJobNotification(attrs, type, type === "watch" ? bodyText : undefined),
    tone,
    secondary: notificationSecondary(attrs, tone, description, analysis, type, type === "watch" ? bodyText : undefined),
    jobId: attrs.job_id?.trim() || undefined,
    jobType: attrs.job_type?.trim() || undefined,
    watchId: attrs.watch_id?.trim() || undefined,
    description: description || undefined,
    status: attrs.status?.trim() || undefined,
    reason: attrs.reason?.trim() || undefined,
    outputBytes: optionalNonNegativeInteger(attrs, "output_bytes"),
    exitCode: analysis.exitCode,
    transcriptRef,
    excerpt,
    // A timer's body IS its content (the fired sentence plus the watch's
    // note); every other job's body is a redundant "Job j completed." line
    // the card's title already says, so only watch cards carry prose.
    prose: type === "watch" && prose ? prose : undefined,
    message: communicate?.message || undefined,
    concerns: communicate?.concerns ?? [],
    rawText: block,
  };
}

function parseObserverCallback(stripped: string): ParsedNotification | null {
  if (!/^Observer callback:\n/.test(stripped)) return null;
  const withoutHeader = stripped.replace(/^Observer callback:\n/, "");
  const marker = "\noutput: ";
  const idx = withoutHeader.indexOf(marker);
  const output = idx === -1 ? "" : withoutHeader.slice(idx + marker.length).trim();
  // The observer's own `message:` prose is the real signal (floor parity-m4
  // §8:239 "body = observer-callback prose"). With an `output:` envelope the
  // communicate message/excerpt carries the body; with NO output (the daemon's
  // historic `Observer callback:\nmessage: X` shape)
  // the prose is the ONLY content, so surface it rather than dropping it to the
  // raw disclosure alone. (Historic shape only — see the file header.)
  const proseOnly = idx === -1 ? withoutHeader.replace(/^message: /, "").trim() : "";
  const communicate = parseCommunicateEnvelope(output);
  // Observer callbacks are coerced from success to warning - a callback firing
  // at all is a thing the reader should notice (legacy renderer-format.js:392).
  const rawTone = notificationTone({ event: "observer_callback" }, communicate);
  return {
    type: "observer-callback",
    title: "Observer callback",
    tone: rawTone === "success" ? "warning" : rawTone,
    secondary: "",
    excerpt: output || proseOnly,
    message: communicate?.message || undefined,
    concerns: communicate?.concerns ?? [],
    rawText: stripped,
  };
}

// An ordered fragment of a parsed steer: either a notification card or a span
// of plain text between/around cards, in the position it appeared in the
// original text. SteeringItem.tsx renders these in order so interstitial
// text stays where it was written (issue #48) instead of collapsing into one
// trailing divider after every card.
export type SteeringFragment =
  | { kind: "notification"; notification: ParsedNotification }
  | { kind: "text"; text: string };

// Notification blocks are STRUCTURED markup (<job-notification …>) and a fixed
// "Observer callback:\n" header, so reading them is parsing, not guessing: they
// cannot false-positive the way a prose pattern like /completed all tasks/ can.
// This is why the card's trigger stayed content-driven while the kind moved to
// the wire, and why a pre-Kind transcript still renders its cards.
export function parseSteeringNotifications(text: string): SteeringFragment[] {
  const stripped = stripSystemReminder(text);
  const blockFragments = splitNotificationBlocks(stripped);
  const fragments: SteeringFragment[] = [];
  let sawNotification = false;
  for (const frag of blockFragments) {
    if (frag.kind === "text") {
      fragments.push({ kind: "text", text: frag.text });
      continue;
    }
    const notification = frag.text.startsWith("<delegate-notification")
      ? parseDelegateNotification(frag.text)
      : parseJobNotification(frag.text);
    if (notification) {
      fragments.push({ kind: "notification", notification });
      sawNotification = true;
    }
  }
  if (sawNotification) return fragments;
  const observer = parseObserverCallback(stripped);
  if (observer) return [{ kind: "notification", notification: observer }];
  return stripped ? [{ kind: "text", text: stripped }] : [];
}
