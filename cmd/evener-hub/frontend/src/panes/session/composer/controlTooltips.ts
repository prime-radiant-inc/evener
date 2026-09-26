// The Send and Steer tooltips: where the composer's keyboard chords and the
// timing each verb applies are spelled out, since neither lives in the
// buttons themselves.

import { chordLabel } from "../../../widgets";

// Why a Steer press is refused while the local recovery fence stands: the
// explicit Resume action is the only thing that clears it, so the refusal
// names that path instead of a generic unavailability.
export const STEER_RECOVERY_FENCED_REASON = "Steer isn't available until this session is resumed";

// Send keeps ONE label in every state. While a turn runs it queues rather
// than sending now, but that is a change of TIMING, not of verb - a label
// that flips to "Queue" made the same button mean two different things
// depending on when you looked, and Steer beside it is what now carries
// "act on this turn immediately". The tooltip says which timing applies, and
// names the chord that submits under the current enterToSend preference.
export function submitTooltipLabel({ canQueue, enterToSend }: { canQueue: boolean; enterToSend: boolean }): string {
  const chord = chordLabel(enterToSend ? ["Enter"] : ["Mod", "Enter"]);
  return canQueue ? `Queue until the agent stops · ${chord}` : `Send now · ${chord}`;
}

// enterToSend makes Shift+Enter a literal newline rather than a steer, so the
// tooltip stops advertising a chord that no longer reaches Steer. While the
// recovery fence stands the tooltip says why the press is refused instead of
// describing an action the fence refuses (kata 2f41).
export function steerTooltipLabel({
  recoveryFenced,
  enterToSend,
}: {
  recoveryFenced: boolean;
  enterToSend: boolean;
}): string {
  if (recoveryFenced) return STEER_RECOVERY_FENCED_REASON;
  return enterToSend ? "Interrupt and redirect now" : `Interrupt and redirect now · ${chordLabel(["Shift", "Enter"])}`;
}
