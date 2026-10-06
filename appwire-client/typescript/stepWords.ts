// A step's line in parts, so each client can draw the thing it acted on (a
// path, a command, a query, a ref) as that: the phone sets the target in
// Menlo (spec § Activity run: "intent sentence, target in Menlo, and a status
// mark"), and the web draws the parts composed into its one line.

import { diffStats } from "./editDiff";
import { clip, lineCount } from "./toolCallText";

/** A step's words: what it did, what it acted on, the rest of the sentence,
 * and what it found ("Read" "agent/tree.go" · "lines 1-4"). */
export interface StepWords {
  verb: string;
  target?: string;
  /** Words after the target, still part of the sentence ("in agent (*.go)"). */
  after?: string;
  /** What the step found or changed, after a " · " ("2 hits", "+3 -1"). */
  detail?: string;
}

/** The words as one line: "Searched "func settle" in agent (*.go) · 2 hits". */
export function composeStepWords(words: StepWords): string {
  let line = words.verb;
  if (words.target) line += ` ${words.target}`;
  if (words.after) line += ` ${words.after}`;
  if (words.detail) line += ` · ${words.detail}`;
  return line;
}

/** A tool's one-line summary from its words: the words composed, as the web
 * draws them. */
export function summaryOf<Step, Ctx = undefined>(
  words: (step: Step, ctx?: Ctx) => StepWords,
): (step: Step, ctx?: Ctx) => string {
  return (step, ctx) => composeStepWords(words(step, ctx));
}

/** The words with a detail added, when there is one. */
export function withDetail(words: StepWords, detail: string | undefined): StepWords {
  return detail === undefined ? words : { ...words, detail };
}

/** A read's line range from its requested bounds or the output it returned. */
/** The header read_file puts before an image or a document's base64 data. */
export const BINARY_PAYLOAD_HEADER = /^\[(image|document): [^\]]+, base64 data follows\]/;

export function readLineRange(args: Record<string, unknown>, output: string): string | undefined {
  const offsetArg = args.offset;
  const offset = typeof offsetArg === "number" && offsetArg > 0 ? offsetArg : 1;
  const limitArg = args.limit;
  const count = typeof limitArg === "number" && limitArg > 0 ? limitArg : lineCount(output);
  if (count > 0) return `lines ${offset}-${offset + count - 1}`;
  return output === "" ? undefined : `lines ${offset}`;
}

/** Count an output only once there is text to count. */
export function outputCount(output: string | undefined, noun: string): string | undefined {
  return output ? `${lineCount(output)} ${noun}` : undefined;
}

/** The quoted, bounded pattern shared by grep-like summaries. */
export function quotedSearchPattern(pattern: string): string {
  return `"${clip(pattern, 50)}"`;
}

/** An edit's result: its diff's added and removed lines, or "ok" for none. */
export function diffResultText(text: string): string {
  const { added, removed } = diffStats(text);
  return added === 0 && removed === 0 ? "ok" : `+${added} -${removed}`;
}
