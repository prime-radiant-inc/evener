// Shared ask_user question/option parsing (wave-5 T4 extraction). Ground
// truth: agent/internal/tool/definitions.go's DefAskUser gives the exact
// argumentsJson shape - {questions:[{header?(<=12 chars), question,
// options:[{label,detail,recommended?}], multi_select?, why?,
// if_unanswered?}]}, 1-4 questions. This used to live private to
// transcript/tools/askUser.tsx (the wave-4 read-only tool-call renderer);
// wave 5's composer/askDock/** needs the identical shape check to build
// its interactive answering dock, so it moved here - a leaf module outside
// both transcript/tools/** and composer/** so neither wave's manifest
// entangles with the other. askUser.tsx imports these back and is
// otherwise unchanged (its own rendering/tests stay byte-equivalent).
//
// Parsing is defensive throughout: a malformed argumentsJSON, a missing
// `questions` array, or an individual malformed question/option all
// degrade to a fallback rather than throwing, since this is untrusted wire
// JSON, not a value this file controls the shape of.

import type { ItemModel, ThreadModel } from "../../protocol/model";
import { parseArgs } from "./transcript/tools/helpers";

export interface AskUserOption {
  label: string;
  detail: string;
  recommended?: boolean;
}

export interface AskUserQuestion {
  header: string;
  question: string;
  options: AskUserOption[];
  multiSelect?: boolean;
  why?: string;
  ifUnanswered?: string;
}

function parseOption(raw: unknown): AskUserOption | undefined {
  if (typeof raw !== "object" || raw === null) return undefined;
  const obj = raw as Record<string, unknown>;
  if (typeof obj.label !== "string" || typeof obj.detail !== "string") return undefined;
  return { label: obj.label, detail: obj.detail, recommended: obj.recommended === true };
}

function parseQuestion(raw: unknown, index: number): AskUserQuestion | undefined {
  if (typeof raw !== "object" || raw === null) return undefined;
  const obj = raw as Record<string, unknown>;
  if (
    (obj.header !== undefined && typeof obj.header !== "string") ||
    typeof obj.question !== "string" ||
    !Array.isArray(obj.options)
  ) {
    return undefined;
  }
  const options = obj.options.map(parseOption).filter((o): o is AskUserOption => o !== undefined);
  if (options.length === 0) return undefined;
  return {
    header: typeof obj.header === "string" ? obj.header : `Question ${index + 1}`,
    question: obj.question,
    options,
    multiSelect: obj.multi_select === true,
    why: typeof obj.why === "string" ? obj.why : undefined,
    ifUnanswered: typeof obj.if_unanswered === "string" ? obj.if_unanswered : undefined,
  };
}

// parseAskUserQuestions returns undefined whenever there's nothing usable to
// render - argumentsJSON absent entirely, a JSON syntax error, valid JSON
// with no questions array, or every individual question in that array
// failing its own shape check - without distinguishing WHY here. An empty
// array can't legitimately occur (DefAskUser requires minItems:1) but is
// treated the same as undefined defensively either way. Callers that need
// to draw their own absent-vs-malformed distinction (e.g. AskUserBody's
// fallback wording) do so with their own check alongside this one.
export function parseAskUserQuestions(item: ItemModel): AskUserQuestion[] | undefined {
  const args = parseArgs(item.argumentsJSON);
  const raw = args.questions;
  if (!Array.isArray(raw)) return undefined;
  const questions = raw
    .map((question, index) => parseQuestion(question, index))
    .filter((q): q is AskUserQuestion => q !== undefined);
  return questions.length > 0 ? questions : undefined;
}

// One line of a composed [answers] reply (askCompose.ts's composeAskAnswers,
// byte-exact format): "N. [Header] → resolution text[ — note: "..."]". The
// header is captured raw (never escaped by composeAskAnswers, unlike the
// resolution's quoted strings), so this matches on brackets rather than
// trying to unescape anything.
const ASK_ANSWER_LINE_RE = /^\d+\.\s\[([^\]]*)\]\s→\s(.*)$/;

// parseAskAnswerLines reads a composed [answers] reply's text back into a
// header -> resolution-text map. The optional trailing " — note: ..." is
// stripped: a note is supplementary context for the model, not part of the
// answer itself, and this map only ever feeds a one-line recap (kata h70z).
function parseAskAnswerLines(text: string): Map<string, string> {
  const map = new Map<string, string>();
  for (const line of text.split("\n")) {
    const m = ASK_ANSWER_LINE_RE.exec(line);
    if (!m) continue;
    const header = m[1] ?? "";
    const rest = m[2] ?? "";
    const noteAt = rest.indexOf(" — note: ");
    map.set(header, noteAt === -1 ? rest : rest.slice(0, noteAt));
  }
  return map;
}

// answeredAskUserSuffix builds the "— answered: ..." recap for a SETTLED,
// answered ask_user call - kata h70z's fix. Old build precedent: "asked
// [Direction] — answered: 'Celsius to Fahrenheit'" as a single collapsed
// line. The current transcript already collapses a clean ask_user row by
// default (ToolCallItem's generic disclosure contract), but its summary
// only ever showed "Asked: [Header]" - the answer lives in a SEPARATE,
// later userMessage item (the composed [answers] reply) that this item
// alone can't see, forcing the reader to cross-reference a second,
// unstyled message to reconstruct "what did I answer". This reads the
// FULL thread model to find that reply and pull the answer back in.
//
// Returns undefined - meaning "show nothing extra, keep the bare
// 'Asked: [Header]' summary" - whenever there's nothing to append: the call
// errored (its own row already gets the failure treatment), it carries no
// parseable questions, or no later [answers] reply exists yet (still
// pending in the composer/dock - a LIVE thing, which per the same
// principle must stay looking unresolved).
export function answeredAskUserSuffix(model: ThreadModel, item: ItemModel): string | undefined {
  if (item.error !== undefined) return undefined;
  const questions = parseAskUserQuestions(item);
  if (!questions) return undefined;
  const items = model.turns.flatMap((turn) => turn.items);
  const at = items.findIndex((i) => i.id === item.id);
  if (at === -1) return undefined;
  const reply = items.slice(at + 1).find((i) => i.type === "userMessage" && i.text.startsWith("[answers]"));
  if (!reply) return undefined;
  const answers = parseAskAnswerLines(reply.text);
  const multiple = questions.length > 1;
  const parts = questions
    .map((q) => {
      const a = answers.get(q.header);
      if (a === undefined) return undefined;
      return multiple ? `${q.header}: ${a}` : a;
    })
    .filter((p): p is string => p !== undefined);
  return parts.length > 0 ? ` — answered: ${parts.join(", ")}` : undefined;
}
