// Shared ask_user question/option parsing (wave-5 T4 extraction). Ground
// truth: agent/internal/tool/definitions.go's DefAskUser gives the exact
// argumentsJson shape - {questions:[{header(<=12 chars), question,
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

import type { ItemModel } from "../../protocol/model";
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

function parseQuestion(raw: unknown): AskUserQuestion | undefined {
  if (typeof raw !== "object" || raw === null) return undefined;
  const obj = raw as Record<string, unknown>;
  if (typeof obj.header !== "string" || typeof obj.question !== "string" || !Array.isArray(obj.options)) {
    return undefined;
  }
  const options = obj.options.map(parseOption).filter((o): o is AskUserOption => o !== undefined);
  if (options.length === 0) return undefined;
  return {
    header: obj.header,
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
  const questions = raw.map(parseQuestion).filter((q): q is AskUserQuestion => q !== undefined);
  return questions.length > 0 ? questions : undefined;
}
