// What a tool call shows, and where: the texts Find searches for one call, and
// the stretch to show when a body folds its words away.
//
// The invariant (#6336) is that every counted match is one Find can land on
// with its words on screen, so the texts here are only the ones a row paints:
//   - the intent line, on screen at every level;
//   - the summary line, once its own disclosure opens;
//   - the body - error, output, and (for a call rendered by the generic body)
//     its arguments. A landing opens the body; whatever the body's own fold or
//     clip keeps off screen is shown by ToolCallItem's excerpt.
import { type ItemModel, parseAnsiLines, parseArgs, prettyJSON, textMatchesFind } from "@evener/appwire-client";
import { defaultToolBody, toolRendererFor } from "../transcript/toolRenderers";

export interface ToolCallTexts {
  /** The row's intent line: always on screen, empty where it shows none. */
  readonly intent: string;
  /** The descriptor's summary line: on screen once its disclosure opens. */
  readonly summary: string;
  /** What only the body holds. Each text is its own string, so a match never
   * spans two. */
  readonly body: readonly string[];
}

export interface ToolCallContext {
  /** The intent line ToolCallItem paints (displayedIntentOf), read by that
   * renderer's own rule - a delegate's clipped brief included. */
  readonly intent?: string;
  /** The session's working directory: a shell summary reads at it. */
  readonly cwd?: string;
}

/** How much of the folded text a landing shows around the match. */
export const FIND_EXCERPT_MAX_CHARS = 400;

/** The summary words the row paints: a delegate row renders none at all
 * (subagentModule owns its presentation) even though its descriptor's own
 * summary() returns text. Both the renderer and Find read this one rule. */
export function displayedSummaryOf(item: ItemModel, summary: string): string {
  return item.toolName === "delegate" ? "" : summary;
}

/** Tool output as a reader sees it: terminal control codes interpreted away,
 * so words split by a colour change still read as one phrase. */
function outputAsShown(text: string): string {
  if (!text.includes("\u001b")) return text;
  return parseAnsiLines(text)
    .map((line) => line.map((run) => run.text).join(""))
    .join("\n");
}

// The string values inside a call's arguments, however nested, as the
// arguments body paints them: JSON-escaped, without the quotes around them.
// Field names are plumbing and are never collected.
function collectStrings(value: unknown, into: string[]): void {
  if (typeof value === "string") {
    if (value !== "") into.push(JSON.stringify(value).slice(1, -1));
  } else if (Array.isArray(value)) {
    for (const each of value) collectStrings(each, into);
  } else if (value !== null && typeof value === "object") {
    for (const each of Object.values(value)) collectStrings(each, into);
  }
}

/** The texts one tool call shows, or null where its descriptor suppresses the
 * whole row: nothing it holds could be landed on. */
export function toolCallTexts(item: ItemModel, context: ToolCallContext = {}): ToolCallTexts | null {
  if (item.type !== "commandExecution") return null;
  const descriptor = toolRendererFor(item.toolName ?? "");
  if (descriptor.suppress?.(item)) return null;
  const body: string[] = [];
  const error = item.error ?? "";
  if (error.trim() !== "") body.push(outputAsShown(error));
  const output = item.output ?? "";
  // The generic body (an unregistered or MCP tool) is the one that renders the
  // arguments, and it pretty-prints JSON output; every other descriptor paints
  // its own view of the output (a tail fold, a line clip, a preview).
  if (descriptor.body === defaultToolBody) {
    const pretty = prettyJSON(output);
    if (pretty !== undefined) body.push(pretty);
    else if (output !== "") body.push(output);
    collectStrings(parseArgs(item.argumentsJSON), body);
  } else if (output !== "") {
    body.push(outputAsShown(output));
  }
  const summary = displayedSummaryOf(item, descriptor.summary(item, { cwd: context.cwd }));
  return { intent: context.intent ?? "", summary, body };
}

// The needle is whitespace-collapsed and lowercased (findNeedle); in the text
// its words may be separated by any run of whitespace. Only the excerpt's
// window uses this, never the decision that a text holds the needle.
function pattern(needle: string): RegExp {
  const words = needle.split(" ").map((word) => word.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
  return new RegExp(words.join("\\s+"), "i");
}

/** Whether `text` holds the needle, by the shared rule Find counts with. */
export function holdsNeedle(text: string, needle: string): boolean {
  return textMatchesFind(text, needle);
}

/** At most `budget` characters of `text` around the needle, under a line
 * saying the rest is not shown; the whole text when it fits. Null when the
 * text does not hold the needle. */
export function excerptAround(text: string, needle: string, budget: number): string | null {
  if (!holdsNeedle(text, needle)) return null;
  if (text.length <= budget) return text;
  // Cut from the raw text, so lines keep their shape. The match's offset only
  // chooses the window; the decision above never depends on it, and a text
  // whose words the regex cannot place shows from its top instead.
  const match = pattern(needle).exec(text);
  const start = match === null ? 0 : Math.max(0, Math.min(match.index - budget / 2, text.length - budget));
  const note = `showing ${budget} chars around the match — the rest is not shown`;
  return `${note}\n${text.slice(start, start + budget)}`;
}
