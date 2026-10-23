import type { PaneLifetime } from "../../../shell/paneLifetime";
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
  return composer;
}
