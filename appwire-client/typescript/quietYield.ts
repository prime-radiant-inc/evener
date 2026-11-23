// Quiet communicate({}) yields render in the transcript as a muted one-line
// system notice. Any companion assistant prose emitted in the same response is
// demoted out of agentMessage chat bubbles into raw.commentary on the quiet
// yield item, keeping it inspectable without polluting chat.

import type { ItemModel } from "./model";
import { isPlainObject } from "./plainObject";

export const QUIET_YIELD_EVENT_KIND = "quiet_yield";

/** Trimmed companion assistant commentary demoted into a quiet_yield notice,
 * or undefined when the yield carried no companion prose. */
export function quietYieldCommentaryOf(item: Pick<ItemModel, "eventKind" | "raw">): string | undefined {
  if (item.eventKind !== QUIET_YIELD_EVENT_KIND || !isPlainObject(item.raw)) return undefined;
  const commentary = item.raw.commentary;
  if (typeof commentary !== "string") return undefined;
  const trimmed = commentary.trim();
  return trimmed === "" ? undefined : trimmed;
}
