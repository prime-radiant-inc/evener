// Composer: the session pane's input surface, mounted by Session.tsx below
// the transcript (T1 carves this slot; Session.tsx is FROZEN for the wave
// once T1 lands — every stream below edits only inside this subtree).
//
// T1 ships this as an empty placeholder. Later streams fill it in without
// ever touching Session.tsx again:
//   - T2 (composer core, panes/session/composer/** minus queue/ and
//     askDock/): the Textarea, send-vs-steer-vs-queue routing via
//     protocol/sendQueueAvailability's deriveSendQueueAvailability, drafts,
//     attachments, interrupt affordance.
//   - T3 (panes/session/composer/queue/**): the queue strip + optimistic
//     pending, rendered inside this component's own tree.
//   - T4 (panes/session/composer/askDock/**): the ask_user answering dock,
//     also inside this component's own tree.
export interface ComposerProps {
  ref: string;
}

// _ref: unused until a later stream reads it (threadsStore lookups,
// send/steer/queue calls, ...) - the leading underscore satisfies Biome's
// noUnusedFunctionParameters without a suppression comment.
export function Composer({ ref: _ref }: ComposerProps) {
  return null;
}
