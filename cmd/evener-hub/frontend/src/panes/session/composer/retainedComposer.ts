import type { PaneLifetime } from "../../../shell/paneLifetime";
import { liveComposerSeats, workspaceStore } from "../../../shell/workspace";
import { readRefComposerSeat, writeRefComposerSeat } from "./draft";
import { type ComposerSourceState, createComposerSourceState } from "./sourceState";

// Composer retention mirrors retainedTranscriptReadView: a conversation
// rendered in a cascade column keeps its composer state — draft, staged
// attachments, edit state — across the column collapsing to a spine and
// re-expanding, because the pane lifetime, not the column, owns it. A dead
// lifetime retains nothing.
export function retainedConversationComposer(lifetime: PaneLifetime, ref: string): ComposerSourceState | undefined {
  const existing = lifetime.composers.get(ref);
  if (existing?.alive) return existing;
  if (!lifetime.alive) return undefined;
  const composer = createComposerSourceState(ref, lifetime.compositionOwnerId);
  lifetime.composers.set(ref, composer);
  // The column's composer writes under this lifetime's seat; its conversation
  // mapping must name that seat, or a reload reconstructing the conversation
  // hands the returning pane a fresh one and restores nothing. Columns the
  // authoritative ancestry reveals only after a restore are created here, so
  // this is where their mappings reconcile: adopt this seat when the stored
  // one is absent or held by no live pane (displaced or closed); a mapping a
  // live pane still holds is that owner's.
  const stored = readRefComposerSeat(ref);
  if (
    stored === undefined ||
    (stored !== lifetime.compositionOwnerId && !liveComposerSeats(workspaceStore.getState().panes).has(stored))
  ) {
    writeRefComposerSeat(ref, lifetime.compositionOwnerId);
  }
  return composer;
}
