// Steering classification, ported from the legacy renderer-format.js:269-494
// (classifySteering + its notification helpers) and driven the same way
// renderer.js:4706-4756 drove it. This is a PURE, CONTENT-pattern-based
// decision off the steering text alone - the legacy classifier never read a
// wire "kind" field, so nothing here needs one and nothing escalates to the
// reducer. SteeringItem.tsx routes each kind to one of three treatments
// (suppress / divider / notification card) via steeringTreatment below.

export type NotificationTone = "success" | "warning" | "error" | "neutral";

export interface ParsedNotification {
  type: string; // job | watch | watch-send | observer-callback
  title: string;
  tone: NotificationTone;
  secondary: string; // job_type · exit N · reason (quiet plumbing stays in raw)
  excerpt: string;
  message?: string; // a communicate envelope's message (rendered as markdown)
  concerns: string[];
  rawText: string; // the verbatim block, always kept inspectable
}

export type SteeringKind =
  | "current-task"
  | "full-list"
  | "task-nudge"
  | "tasks-done"
  | "loop"
  | "read-only"
  | "transcript"
  | "notification"
  | "unknown";

export interface SteeringClass {
  kind: SteeringKind;
  label: string;
  detail: string;
  // The SYSTEM-REMINDER-stripped text, shown verbatim in the divider body
  // (parity-m4 §8: the divider body is the classified text, not re-rendered).
  cleanText: string;
  notifications?: ParsedNotification[];
  leftover?: string;
}

export type SteeringTreatment = "suppress" | "divider" | "card";

// current-task / full-list / task-nudge are task bookkeeping the tasks panel
// (and the task-update card) already own, so they render nothing inline
// (parity-m4 §8:209-217). notification renders one card per block. Everything
// else keeps the collapsible steering divider, now with its own classified
// label instead of a single generic "Steering injected".
export function steeringTreatment(kind: SteeringKind): SteeringTreatment {
  if (kind === "current-task" || kind === "full-list" || kind === "task-nudge") return "suppress";
  if (kind === "notification") return "card";
  return "divider";
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
  const communicate = parseCommunicateEnvelope(excerpt);
  let type = "job";
  if ((attrs.event === "watch" || attrs.status === "watch") && !attrs.job_id) type = "watch";
  if (attrs.event === "watch_send") type = "watch-send";
  const tone = notificationTone(attrs, communicate);
  return {
    type,
    title: titleForJobNotification(attrs, type),
    tone,
    secondary: notificationSecondary(attrs, tone),
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

export function classifySteering(text: string): SteeringClass {
  const stripped = stripSystemReminder(text);
  return { ...classifyStripped(stripped), cleanText: stripped };
}

function classifyStripped(stripped: string): Omit<SteeringClass, "cleanText"> {
  const taskMatch = stripped.match(/<CURRENT-TASK\s+id="(\d+)">([\s\S]*?)<\/CURRENT-TASK>/);
  if (taskMatch) {
    const title = (taskMatch[2] ?? "").match(/<TITLE>([\s\S]*?)<\/TITLE>/)?.[1]?.trim() ?? "";
    return { kind: "current-task", label: "current task", detail: `#${taskMatch[1]} ${title}`.trim() };
  }
  if (/^Task list:/m.test(stripped)) {
    return { kind: "full-list", label: "task list", detail: "" };
  }
  if (/completed all tasks/.test(stripped)) {
    return { kind: "tasks-done", label: "tasks done", detail: "" };
  }
  if (/task_list tool available/.test(stripped)) {
    return { kind: "task-nudge", label: "task_list nudge", detail: "" };
  }
  if (/stuck in a loop|still stuck|stuck for a long time/.test(stripped)) {
    return { kind: "loop", label: "loop detection", detail: "" };
  }
  if (/reading without writing|reading for \d+ turns/.test(stripped)) {
    return { kind: "read-only", label: "read-only nudge", detail: "" };
  }
  if (/pre-compaction transcript/.test(stripped)) {
    return { kind: "transcript", label: "transcript pointer", detail: "" };
  }

  const { blocks, leftover } = splitJobNotificationBlocks(stripped);
  if (blocks.length > 0) {
    const notifications = blocks.map(parseJobNotification).filter((n): n is ParsedNotification => n !== null);
    const [first] = notifications;
    if (first) {
      return { kind: "notification", label: first.title, detail: "", notifications, leftover };
    }
  }
  const observer = parseObserverCallback(stripped);
  if (observer) {
    return { kind: "notification", label: observer.title, detail: "", notifications: [observer], leftover: "" };
  }

  return { kind: "unknown", label: "steering injected", detail: "" };
}
