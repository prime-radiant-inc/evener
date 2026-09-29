// What a tool step's output says, read from the shapes the tools print, so a
// step's evidence (spec 8.2: "Tapping a step shows its evidence") reads as
// the words and not the envelope: a command's output without its exit
// footer, a fetched page's answer, the skill an activation loaded, and JSON
// pretty-printed. Shared by the web's tool bodies and the phone's step
// evidence.

import { str } from "./toolCallText";

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

// The shell tool ends a command's output with "[exit N]"; the buffered
// environment, which cannot stream, with "exit_code=N duration_ms=N
// timed_out=bool".
const EXIT_FOOTER_RE = /(?:^|\n)(?:\[exit (-?\d+)\]|exit_code=(-?\d+) duration_ms=\d+ timed_out=(?:true|false))\s*$/;

/** A command's output without the footer the shell tool ends it with, and
 * the exit code that footer states. */
export function shellOutput(output: string): { text: string; exitCode?: number } {
  const match = EXIT_FOOTER_RE.exec(output);
  if (!match) return { text: output.replace(/\n+$/, "") };
  const code = match[1] ?? match[2];
  return { text: output.slice(0, match.index).replace(/\n+$/, ""), exitCode: Number(code) };
}

/** A fetched page as web_fetch reports it: the model's answer (or, when
 * both models refused, the page's raw content), where it came from, and how
 * big it was. Undefined for an output that isn't web_fetch's JSON. */
export function webFetchResult(output: string): { text: string; url?: string; bytes?: number } | undefined {
  let parsed: unknown;
  try {
    parsed = JSON.parse(output);
  } catch {
    return undefined;
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) return undefined;
  const record = parsed as Record<string, unknown>;
  const text = str(record, "answer") ?? str(record, "content");
  if (text === undefined) return undefined;
  const url = str(record, "url");
  const bytes = typeof record.size_bytes === "number" ? record.size_bytes : undefined;
  return { text, ...(url ? { url } : {}), ...(bytes === undefined ? {} : { bytes }) };
}

const SKILL_CONTEXT_RE = /^\s*<skill-context>\s*([\s\S]*?)\s*<\/skill-context>\s*$/;

/** The skill a use_skill step loaded, from the <skill-context> block the tool
 * returns: its name, description and instructions (markdown). Undefined for
 * any other output. */
export function skillContext(output: string): { name: string; description?: string; instructions: string } | undefined {
  const body = SKILL_CONTEXT_RE.exec(output)?.[1];
  if (body === undefined) return undefined;
  let parsed: unknown;
  try {
    parsed = JSON.parse(body);
  } catch {
    return undefined;
  }
  if (typeof parsed !== "object" || parsed === null) return undefined;
  const record = parsed as Record<string, unknown>;
  const name = str(record, "name");
  const instructions = str(record, "instructions");
  if (name === undefined || instructions === undefined) return undefined;
  const description = str(record, "description");
  return { name, ...(description ? { description } : {}), instructions };
}
