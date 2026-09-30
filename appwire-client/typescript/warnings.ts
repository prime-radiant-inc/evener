// The informational-warning contract: which warning codes mark a notice as
// quiet detail (budget arithmetic succeeding, "no action needed") rather than
// an actionable failure. Two consumers gate on it, and both must agree by
// reading this one predicate instead of re-deriving from prose:
//   - transcriptProjector.ts hides an informational warning unless
//     transcriptDisplayConfig's informationalNoticesVisible says the level
//     is full;
//   - WarningItem.tsx renders it as one quiet line instead of the
//     attention-chip block, for the levels that do show it.

import type { ItemModel } from "./model";
import { isPlainObject } from "./plainObject";
import { hasWarningText } from "./warningText";

/**
 * The context-budget notices (an output-allocation clamp, a context-usage
 * heads-up, a predictive checkpoint that fell back to the deterministic one). Bound by test to the daemon's own constant in
 * agent/events/payloads.go, the way errors.ts binds its discriminants to
 * appwire/errors.go.
 */
export const WarningCodeContextBudget = "context_budget";

/**
 * A failed attempt to get a delegate's owed attention where it belongs:
 * restoring its cold runtime to deliver it, or escalating it to the root when
 * a closed ancestor fences it off. The daemon retries on its own and warns
 * once per failure episode, so it is detail for Full, not an alarm at every
 * level. Bound by test to agent/events/payloads.go.
 */
export const WarningCodeDelegateAttentionRestore = "delegate_attention_restore";

// The codes whose warnings show only at Full.
const INFORMATIONAL_WARNING_CODES: ReadonlySet<unknown> = new Set([
  WarningCodeContextBudget,
  WarningCodeDelegateAttentionRestore,
]);

/**
 * True when a warning is an informational notice rather than an actionable
 * failure: demote it (quiet line) and show it only at full. A warning reaches
 * a client two ways: a `warning` item (the hub's own warning notification,
 * its code on item.warning), and the daemon's overlay notice, a
 * systemMessage with eventKind "warning" and its code on raw.warning.code
 * (internal/appoverlay/notices.go warningAnnouncement). A warning with no
 * code, or a different code, keeps the always-critical treatment every
 * warning had before codes existed. A new informational warning extends
 * this predicate by naming its code, never by a consumer guessing from the
 * wording.
 */
export function isInformationalWarning(item: ItemModel): boolean {
  if (item.type === "warning") return INFORMATIONAL_WARNING_CODES.has(item.warning?.code);
  return (
    item.type === "systemMessage" &&
    item.eventKind === "warning" &&
    INFORMATIONAL_WARNING_CODES.has(noticeWarningCode(item.raw))
  );
}

// The code an overlay warning notice carries on its raw, or undefined. Raw is
// untyped wire JSON, so every step is checked.
function noticeWarningCode(raw: unknown): unknown {
  if (!isPlainObject(raw) || !isPlainObject(raw.warning)) return undefined;
  return raw.warning.code;
}

/** A warning's words as both clients show them, each once. */
export interface WarningWords {
  message: string;
  title?: string;
  hint?: string;
}

/**
 * A warning's message, title and hint, each only when it is a non-blank
 * string. The message is the warning's text, or its hint, then its title,
 * when it has none (a title is a label, a hint a sentence that can stand as
 * the line), and a title or hint that says what the message says is left
 * out, so no row repeats itself. Null when there is nothing to show.
 * Every warning row on the web and the phone reads its words from here.
 */
export function warningWords(text: unknown, title: unknown, hint: unknown): WarningWords | null {
  const message = [text, hint, title].find(hasWarningText);
  if (message === undefined) return null;
  return {
    message,
    ...(hasWarningText(title) && title !== message ? { title } : {}),
    ...(hasWarningText(hint) && hint !== message ? { hint } : {}),
  };
}

/**
 * A daemon warning notice (a systemMessage with eventKind "warning") that is
 * not informational: a failure both clients render with the warning
 * treatment rather than as a quiet system line (#3387). Returns its words
 * (warningWords) from its text and raw.warning's title and hint
 * (internal/appoverlay/notices.go warningAnnouncement); null for an
 * informational warning, one with nothing to show, or anything but a
 * warning notice.
 */
export function attentionWarningNotice(item: ItemModel): WarningWords | null {
  if (item.type !== "systemMessage" || item.eventKind !== "warning" || isInformationalWarning(item)) return null;
  const fields = isPlainObject(item.raw) && isPlainObject(item.raw.warning) ? item.raw.warning : {};
  return warningWords(item.text, fields.title, fields.hint);
}
