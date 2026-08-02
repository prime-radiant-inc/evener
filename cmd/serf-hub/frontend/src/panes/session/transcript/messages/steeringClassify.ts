// Structured steering-notification parsing: <job-notification …> blocks and
// the fixed "Observer callback:\n" header a daemon tool result emits
// (agent/session_tools_communicate.go) are markup, not prose, so reading them
// is parsing rather than guessing. SteeringItem.tsx routes daemon steering on
// ItemModel.steeringKind (the wire's events.SteeringKind*, named at the
// injection site) instead of inferring one from wording, so nothing here
// decides a "kind" any more - this file only extracts notification cards,
// which stay content-driven because structured markup can't false-positive
// the way a prose pattern could (see parseSteeringNotifications below).

export type NotificationTone = "success" | "warning" | "error" | "neutral";

export interface ParsedNotification {
  type: string; // job | watch | watch-send | observer-callback
  title: string;
  tone: NotificationTone;
  secondary: string; // job_type · exit N · reason (quiet plumbing stays in raw)
  jobId?: string;
  jobType?: string;
  description?: string;
  status?: string;
  reason?: string;
  outputBytes?: number;
  exitCode?: number;
  transcriptRef?: string;
  excerpt: string;
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

// splitJobNotificationBlocks extracts each individual <job-notification …>…
// </job-notification> block. The per-block match MUST be non-greedy: a single
// steering turn can carry several blocks joined by newlines, and a greedy match
// would span the first opening tag to the last closing tag and aggregate
// distinct notifications into one (contracts §17).
function splitJobNotificationBlocks(text: string): { blocks: string[]; leftover: string } {
  const blocks: string[] = [];
  const leftover = text
    .replace(/<job-notification\s+[^>]*>[\s\S]*?<\/job-notification>/g, (block) => {
      blocks.push(block);
      return "";
    })
    .trim();
  return { blocks, leftover };
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
  const status = communicateStatus || outerStatus || outerEvent;
  if (
    concerns ||
    status === "cancelled" ||
    status === "stopped" ||
    attrs.event === "watch_send" ||
    attrs.event === "watch"
  ) {
    return "warning";
  }
  if (status === "completed" || status === "done") return "success";
  return "neutral";
}

function titleForJobNotification(attrs: Record<string, string>, type: string): string {
  if (type === "watch-send") return "Watch delivered";
  if (type === "watch") return "Watch triggered";
  const status = (attrs.status || attrs.event || "notification").trim();
  if (!status) return "Job notification";
  return `Job ${status}`;
}

function notificationSecondary(attrs: Record<string, string>, tone: NotificationTone): string {
  const bits: string[] = [];
  const type = (attrs.job_type ?? "").trim();
  if (type && type !== "job") bits.push(type);
  const exit = (attrs.exit_code ?? "").trim();
  if (exit && exit !== "0") bits.push(`exit ${exit}`);
  const reason = (attrs.reason ?? "").trim();
  if (reason && (tone === "error" || tone === "warning")) bits.push(reason);
  return bits.join(" · ");
}

function parseJobNotification(block: string): ParsedNotification | null {
  const m = block.match(/^<job-notification\s+([^>]*)>([\s\S]*)<\/job-notification>$/);
  if (!m) return null;
  const attrs = parseQuotedAttrs(m[1] ?? "");
  const bodyText = (m[2] ?? "").trim();
  const { excerpt } = splitNotificationExcerpt(bodyText);
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
  let type = "job";
  if ((attrs.event === "watch" || attrs.status === "watch") && !attrs.job_id) type = "watch";
  if (attrs.event === "watch_send") type = "watch-send";
  const tone = notificationTone(attrs, communicate);
  const transcriptRef = isValidTranscriptRef(attrs.transcript_ref) ? attrs.transcript_ref : undefined;
  return {
    type,
    title: titleForJobNotification(attrs, type),
    tone,
    secondary: decodeNotificationEntities(attrs.description ?? "").trim() || notificationSecondary(attrs, tone),
    jobId: attrs.job_id?.trim() || undefined,
    jobType: attrs.job_type?.trim() || undefined,
    description: decodeNotificationEntities(attrs.description ?? "").trim() || undefined,
    status: attrs.status?.trim() || undefined,
    reason: attrs.reason?.trim() || undefined,
    outputBytes: optionalNonNegativeInteger(attrs, "output_bytes"),
    exitCode: optionalNonNegativeInteger(attrs, "exit_code"),
    transcriptRef,
    excerpt,
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
  // `Observer callback:\nmessage: X` shape, session_tools_communicate.go:117)
  // the prose is the ONLY content, so surface it rather than dropping it to the
  // raw disclosure alone.
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

// Notification blocks are STRUCTURED markup (<job-notification …>) and a fixed
// "Observer callback:\n" header, so reading them is parsing, not guessing: they
// cannot false-positive the way a prose pattern like /completed all tasks/ can.
// This is why the card's trigger stayed content-driven while the kind moved to
// the wire, and why a pre-Kind transcript still renders its cards.
export function parseSteeringNotifications(text: string): {
  notifications: ParsedNotification[];
  leftover: string;
} {
  const stripped = stripSystemReminder(text);
  const { blocks, leftover } = splitJobNotificationBlocks(stripped);
  const notifications = blocks.map(parseJobNotification).filter((n): n is ParsedNotification => n !== null);
  if (notifications.length > 0) return { notifications, leftover };
  const observer = parseObserverCallback(stripped);
  if (observer) return { notifications: [observer], leftover: "" };
  return { notifications: [], leftover: stripped };
}
