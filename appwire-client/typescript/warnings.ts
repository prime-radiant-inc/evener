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

/**
 * The context-budget notices (an output-allocation clamp, a context-usage
 * heads-up). Bound by test to the daemon's own constant in
 * agent/events/payloads.go, the way errors.ts binds its discriminants to
 * appwire/errors.go.
 */
export const WarningCodeContextBudget = "context_budget";

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
  if (item.type === "warning") return item.warning?.code === WarningCodeContextBudget;
  return (
    item.type === "systemMessage" &&
    item.eventKind === "warning" &&
    noticeWarningCode(item.raw) === WarningCodeContextBudget
  );
}

// The code an overlay warning notice carries on its raw, or undefined. Raw is
// untyped wire JSON, so every step is checked.
function noticeWarningCode(raw: unknown): unknown {
  if (typeof raw !== "object" || raw === null) return undefined;
  const warning = (raw as { warning?: unknown }).warning;
  if (typeof warning !== "object" || warning === null) return undefined;
  return (warning as { code?: unknown }).code;
}
