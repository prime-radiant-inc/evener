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

// The buffered environment's own block (runBufferedShell), whole and at the
// end: a cancel, or a timeout with its retry line. An "[ERROR: …]" line the
// command printed is its output.
const ENVIRONMENT_ERROR_RE =
  /\[ERROR: Command (?:was canceled before completion|timed out after \d+ms)\. Partial output is shown above\.(?:\n[^\]]*)?\]\s*$/;

// The body without the block the buffered environment wrote, when it ends in
// one.
function withoutTrailingError(body: string): string {
  return body.replace(ENVIRONMENT_ERROR_RE, "");
}

/** A text's last line. */
export function lastLine(text: string): string {
  return text.slice(text.lastIndexOf("\n") + 1);
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

/** How far from the end an intervention can start: the registry's are a
 * sentence or two. */
export const OUTPUT_TAIL_WINDOW = 8192;
/** At most this many cuts are tried. */
export const MAX_OUTPUT_TAIL_CUTS = 16;

/** Where the last OUTPUT_TAIL_WINDOW characters of a text begin: the only
 * part a reader searches for how the text ends. */
function tailStart(text: string): number {
  return Math.max(0, text.length - OUTPUT_TAIL_WINDOW);
}

/** Where a tool's own output may end: the whole output, then the output cut
 * at each blank line from the last back, within the output's last
 * OUTPUT_TAIL_WINDOW characters and at most MAX_OUTPUT_TAIL_CUTS of them.
 * The registry appends an intervention after a blank line
 * (agent/internal/tool/breaker.go's appendIntervention), and it may hold
 * blank lines of its own, so a reader takes the first of these that reads
 * as the tool's tail. Bounded, because a running command's output has no
 * footer yet and is read on every render. */
export function outputTails(output: string): string[] {
  const tails = [output];
  const floor = tailStart(output);
  for (
    let cut = output.lastIndexOf("\n\n");
    cut > 0 && cut >= floor && tails.length <= MAX_OUTPUT_TAIL_CUTS;
    cut = output.lastIndexOf("\n\n", cut - 1)
  ) {
    tails.push(output.slice(0, cut));
  }
  return tails;
}

function withoutTrailingNewlines(text: string): string {
  return text.replace(/\n+$/, "");
}

function readBracketed(text: string): ShellOutput | undefined {
  // The footer ends the text, so only its last OUTPUT_TAIL_WINDOW characters
  // are searched: a running command's output grows without one.
  const start = tailStart(text);
  const tail = text.slice(start);
  let footer: RegExpMatchArray | undefined;
  for (const match of tail.matchAll(BRACKETED_RE)) {
    const segments = (match[1] ?? "").split(" · ");
    const rest = tail.slice((match.index ?? 0) + match[0].length);
    if (FOOTER_FIRST_SEGMENT_RE.test(segments[0] ?? "") && rest.trim() === "") footer = match;
  }
  if (footer === undefined) return undefined;
  const segments = (footer[1] ?? "").split(" · ");
  const body = text.slice(0, start + (footer.index ?? 0)).replace(TRAILING_REMINDER_RE, "");
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
  const trailer = BUFFERED_TRAILER_RE.exec(lastLine(trimmed));
  if (!trailer) return undefined;
  const cut = trimmed.lastIndexOf("\n");
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
