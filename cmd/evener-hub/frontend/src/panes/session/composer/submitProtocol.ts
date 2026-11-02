// The decisions the Composer's submit and the QueueStrip's drain share, so the
// two surfaces cannot drift: whether the target can take the draft's
// selections, how a draft reaches the threads store, and what the user hears
// when a recovery row was changed or consumed elsewhere.

import type { ComposerMention } from "@evener/appwire-client";
import { type InputAttachment, threadsStore } from "../../../stores/threads";
import { pushToast } from "../../../widgets/toast/store";

export type DraftSelections = {
  skillNames?: readonly string[];
  commandNames?: readonly string[];
};

export type SelectionRefusal = "skill-unsupported" | "command-unsupported";

// What the target advertises. Absent capabilities (a thread model that has not
// loaded) support nothing, matching the store's own composerMutationIntent gate.
export type SelectionCapabilities = { skillInput?: boolean; commandInput?: boolean } | undefined;

export function selectionRefusal(
  selections: DraftSelections,
  capabilities: SelectionCapabilities,
): SelectionRefusal | undefined {
  if ((selections.skillNames?.length ?? 0) > 0 && capabilities?.skillInput !== true) return "skill-unsupported";
  if ((selections.commandNames?.length ?? 0) > 0 && capabilities?.commandInput !== true) return "command-unsupported";
  return undefined;
}

// Reports a refused selection; the draft is kept by the caller returning
// before anything durable is written.
export function pushSelectionRefusal(refusal: SelectionRefusal): void {
  if (refusal === "skill-unsupported")
    pushToast("error", "Skill selections aren't supported on this session yet; your draft is kept", refusal);
  else pushToast("error", "Command selections are not supported on this session; your draft is kept", refusal);
}

// A recovery row that another surface changed ("changed") or already sent
// ("consumed") under this one.
export type RecoveryRefusal = "changed" | "consumed";

export function pushRecoveryRefusal(refusal: RecoveryRefusal): void {
  if (refusal === "changed") pushToast("info", "This message changed in another tab. Refresh and try again.", refusal);
  else pushToast("info", "This message was already sent in another tab.", refusal);
}

export type DraftRoute = "send" | "queue" | "steer" | "drain";

export type DraftPayload = {
  text: string;
  attachments?: InputAttachment[];
  skillNames?: readonly string[];
  commandNames?: readonly string[];
  mentions?: readonly ComposerMention[];
};

// One store call for all four routes; "drain" is the store's drainAsSteer.
export function submitDraftToStore(
  route: DraftRoute,
  ref: string,
  draft: DraftPayload,
  compositionOwnerId?: string,
): Promise<void> {
  const store = threadsStore.getState();
  const args = [ref, draft.text, draft.attachments, draft.skillNames, draft.commandNames, draft.mentions] as const;
  const call = (method: (typeof store)["send"] | (typeof store)["drainAsSteer"]) => method(...args, compositionOwnerId);
  return route === "drain" ? call(store.drainAsSteer) : call(store[route]);
}
