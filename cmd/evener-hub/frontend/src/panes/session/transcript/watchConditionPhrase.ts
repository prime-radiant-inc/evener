import { humanizeInterval, humanizeSeconds, type parseConditionText } from "@evener/appwire-client";
import { watchEventLabel } from "./watchEventLabel";

// The trigger phrases of one parsed watch condition, in the wording the watch
// list has always used. Both the list row and the watch card render them, so the
// two word a condition identically: the card once drifted to "events *" where
// the list said "any event" (a review finding), and one composer is what keeps
// the vocabulary from splitting again.
export interface WatchTriggerPhrases {
  /** The humanized timer, when the condition is a bare after/repeat timer. */
  timer?: string;
  /** Pattern, events (with the throttle), event filter, heartbeat, in that order. */
  bits: string[];
}

export function watchTriggerPhrases(parsed: ReturnType<typeof parseConditionText>): WatchTriggerPhrases {
  const phrases: WatchTriggerPhrases = { bits: [] };
  // A timer condition is a cadence and nothing else: the list row reads
  // "in 5m · <source>", and a condition carrying both timer fields takes the
  // after_seconds reading, as the list always has.
  if (parsed.afterSeconds !== undefined) {
    phrases.timer = humanizeSeconds(parsed.afterSeconds);
  } else if (parsed.repeatSeconds !== undefined) {
    phrases.timer = `every ${humanizeInterval(parsed.repeatSeconds).replace(/^every /, "")}`;
  }
  if (parsed.outputMatch) phrases.bits.push(`“${parsed.outputMatch}”`);
  // The every throttle rides the events bit when one renders, else the filter
  // bit — it is one shared throttle ("events: […] every N where …"), so it must
  // never print twice. Parens match the create summary's "(every N)" shape
  // (RoboRev PR #954 review 3).
  const every = parsed.every !== undefined ? ` (every ${parsed.every})` : "";
  if (parsed.events.length > 0) {
    const names = parsed.events.map(watchEventLabel).join(", ");
    phrases.bits.push(`${names}${every}`);
  }
  if (parsed.filterToolName || parsed.filterStatus) {
    phrases.bits.push(`${filterSummaryPhrase(parsed)}${parsed.events.length === 0 ? every : ""}`);
  }
  if (parsed.progressIntervalMS !== undefined) {
    phrases.bits.push(humanizeInterval(parsed.progressIntervalMS / 1000));
  }
  return phrases;
}

// filterSummaryPhrase names an event-filter watch's shape in words, shared by
// summaries, list rows, and row details so the three never drift (RoboRev PR
// #954 review 3): error and ok both explicit with the tool named when
// present; a status-less filter still names the tool ("calls on …"), and a
// bare filter with neither reads "matching events".
interface FilterPhrase {
  filterToolName?: string;
  filterStatus?: string;
}

export function filterSummaryPhrase(condition: FilterPhrase): string {
  const tool = condition.filterToolName ? ` on ${condition.filterToolName}` : "";
  if (condition.filterStatus === "error") return `failed tool calls${tool}`;
  if (condition.filterStatus === "ok") return `successful tool calls${tool}`;
  return condition.filterToolName ? `calls on ${condition.filterToolName}` : "matching events";
}
