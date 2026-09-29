// What a tool step's output says, read from the shapes the tools print, so a
// step's evidence (spec 8.2: "Tapping a step shows its evidence") reads as
// the words and not the envelope: a command's output without its exit
// footer, a fetched page's answer, the skill an activation loaded, and JSON
// pretty-printed. Shared by the web's tool bodies and the phone's step
// evidence.

import { parseJSONObject, str } from "./toolCallText";

/** Text pretty-printed when the whole of it is a JSON object or array;
 * undefined for anything else, a bare number or string included. */
export function prettyJSON(text: string): string | undefined {
  try {
    const value: unknown = JSON.parse(text);
    if (typeof value !== "object" || value === null) return undefined;
    return JSON.stringify(value, null, 2);
  } catch {
    return undefined;
  }
}

// The shell tool's footer is one bracketed line of " · "-joined segments
// (agent/session_tools_shell.go's formatShellResult): the exit code, a
// timeout, a windowed output's pointer, a job still running. Its first
// segment is always one of these. A system reminder may precede it on the
// same line, and the registry may append an intervention after it, set off
// by a blank line (agent/internal/tool/breaker.go's appendIntervention).
const FOOTER_FIRST_SEGMENT_RE =
  /^(?:exit -?\d+|timed out|the foreground wait ended|stopped by evener's runtime limit|no output before the limit|output windowed|output accumulates durably|completion arrives by notification|\d+ bytes dropped|still running as|running in background as)/;
const BRACKETED_RE = /\[([^[\]]*)\]/g;
const TRAILING_REMINDER_RE = /<system-reminder>[\s\S]*?<\/system-reminder>\s*$/;

// The environment that cannot stream ends its output with a bare trailer
// line (runBufferedShell), after an "[ERROR: …]" block when it timed out or
// was cancelled.
const BUFFERED_TRAILER_RE = /^exit_code=(-?\d+) duration_ms=\d+ timed_out=(true|false)$/;

// The buffered environment's own blocks (runBufferedShell): a timeout or a
// cancel. An "[ERROR: …]" line the command printed is its output.
const ENVIRONMENT_ERROR_STARTS = [
  "[ERROR: Command timed out after ",
  "[ERROR: Command was canceled before completion.",
];

// The body without the final block the buffered environment wrote, when it
// ends in one.
function withoutTrailingError(body: string): string {
  const start = Math.max(...ENVIRONMENT_ERROR_STARTS.map((marker) => body.lastIndexOf(marker)));
  if (start === -1 || !body.trimEnd().endsWith("]")) return body;
  return body.slice(0, start);
}

/** A command's output, read from the tail the shell tool ends it with. */
export interface ShellOutput {
  /** The command's own output, without the footer or anything after it. */
  text: string;
  exitCode?: number;
  /** The command, or its foreground wait, ran out of time. */
  timedOut?: boolean;
  /** The command kept running as a background job. */
  stillRunning?: boolean;
  /** The output was long, so only its start and end are here. */
  windowed?: boolean;
}

/** Where a tool's own output may end: the whole output, then the output cut
 * at each blank line from the last to the first. The registry appends an
 * intervention after a blank line (agent/internal/tool/breaker.go's
 * appendIntervention), and the intervention may hold blank lines of its own,
 * so a reader takes the first of these that reads as the tool's tail. */
export function outputTails(output: string): string[] {
  const tails = [output];
  for (let cut = output.lastIndexOf("\n\n"); cut !== -1; cut = output.lastIndexOf("\n\n", cut - 1)) {
    tails.push(output.slice(0, cut));
    if (cut === 0) break;
  }
  return tails;
}

function withoutTrailingNewlines(text: string): string {
  return text.replace(/\n+$/, "");
}

function readBracketed(text: string): ShellOutput | undefined {
  let footer: RegExpMatchArray | undefined;
  for (const match of text.matchAll(BRACKETED_RE)) {
    const segments = (match[1] ?? "").split(" · ");
    const rest = text.slice((match.index ?? 0) + match[0].length);
    if (FOOTER_FIRST_SEGMENT_RE.test(segments[0] ?? "") && rest.trim() === "") footer = match;
  }
  if (footer === undefined) return undefined;
  const segments = (footer[1] ?? "").split(" · ");
  const body = text.slice(0, footer.index).replace(TRAILING_REMINDER_RE, "");
  const result: ShellOutput = { text: withoutTrailingNewlines(body) };
  for (const segment of segments) {
    const exit = /^exit (-?\d+)$/.exec(segment);
    if (exit) result.exitCode = Number(exit[1]);
    if (
      /^(?:timed out|the foreground wait ended|stopped by evener's runtime limit|no output before the limit)/.test(
        segment,
      )
    )
      result.timedOut = true;
    if (/^(?:still running as|running in background as)/.test(segment)) result.stillRunning = true;
    if (segment.startsWith("output windowed")) result.windowed = true;
  }
  return result;
}

function readBuffered(text: string): ShellOutput | undefined {
  const trimmed = withoutTrailingNewlines(text);
  const cut = trimmed.lastIndexOf("\n");
  const trailer = BUFFERED_TRAILER_RE.exec(trimmed.slice(cut + 1));
  if (!trailer) return undefined;
  const body = cut === -1 ? "" : withoutTrailingError(trimmed.slice(0, cut));
  return {
    text: withoutTrailingNewlines(body),
    exitCode: Number(trailer[1]),
    ...(trailer[2] === "true" ? { timedOut: true } : {}),
  };
}

/** A command's output without the footer the shell tool ends it with (or
 * the buffered environment's trailer), without anything the registry
 * appended after that, and what the footer says. Reads only the tail, so a
 * bracket or an "exit_code=" line the command printed itself stays output. */
export function shellOutput(output: string): ShellOutput {
  const text = output.replace(/\r\n/g, "\n");
  for (const search of outputTails(text)) {
    const read = readBracketed(search) ?? readBuffered(search);
    if (read) return read;
  }
  return { text: withoutTrailingNewlines(text) };
}

/** A fetched page as web_fetch reports it: the model's answer (or, when
 * both models refused, the page's raw content), where it came from, and how
 * big it was. Undefined for an output that isn't web_fetch's JSON. */
export function webFetchResult(output: string): { text?: string; url?: string; bytes?: number } | undefined {
  const record = parseJSONObject(output);
  if (record === undefined) return undefined;
  const text = str(record, "answer") ?? str(record, "content");
  const url = str(record, "url");
  const bytes = typeof record.size_bytes === "number" ? record.size_bytes : undefined;
  // JSON with none of these isn't web_fetch's result.
  if (text === undefined && url === undefined && bytes === undefined) return undefined;
  return {
    ...(text === undefined ? {} : { text }),
    ...(url ? { url } : {}),
    ...(bytes === undefined ? {} : { bytes }),
  };
}

const SKILL_CONTEXT_RE = /^\s*<skill-context>\s*([\s\S]*?)\s*<\/skill-context>\s*$/;

/** The skill a use_skill step loaded, from the <skill-context> block the tool
 * returns: its name, description and instructions (markdown). Undefined for
 * any other output. */
export function skillContext(output: string): { name: string; description?: string; instructions: string } | undefined {
  // An intervention the registry appended after a blank line is not the skill.
  const body = outputTails(output)
    .map((search) => SKILL_CONTEXT_RE.exec(search)?.[1])
    .find((found) => found !== undefined);
  if (body === undefined) return undefined;
  const record = parseJSONObject(body);
  if (record === undefined) return undefined;
  const name = str(record, "name");
  const instructions = str(record, "instructions");
  if (name === undefined || instructions === undefined) return undefined;
  const description = str(record, "description");
  return { name, ...(description ? { description } : {}), instructions };
}
