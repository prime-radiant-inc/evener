// The informational-warning contract: which warning codes mark a notice as
// quiet detail (budget arithmetic succeeding, "no action needed") rather than
// an actionable failure. Two consumers gate on it, and both must agree by
// reading this one predicate instead of re-deriving from prose:
//   - transcriptProjector.ts hides an informational warning unless
//     transcriptDisplayConfig's informationalNoticesVisible says the level
//     is high verbosity;
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
 * True when a warning item is an informational notice rather than an
 * actionable failure: demote it (quiet line) and gate it on high verbosity.
 * A warning with no code, or a different code, keeps the always-critical
 * treatment every warning had before codes existed. A new informational
 * warning extends this predicate by naming its code, never by a consumer
 * guessing from the wording.
 */
export function isInformationalWarning(item: ItemModel): boolean {
  return item.type === "warning" && item.warning?.code === WarningCodeContextBudget;
}
