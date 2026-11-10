import type * as appwire from "@evener/appwire-client";
import { entryDisplayKey, isTurnError, makeTranscriptDisplayConfig, projectThread } from "@evener/appwire-client";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import { backgroundHolds, foregroundHolds } from "./threadOwners";
import { threadsStore } from "./threads";

export const ref = "local:claim-handoff";
export const sourceItems = [
  { id: "source-a", text: "CLAIM_BODY_A" },
  { id: "source-b", text: "CLAIM_BODY_B" },
];
export const sourceIds = [...sourceItems.map((item) => item.id), "turn:itemless-error:error"];

export function response(requestedRef = ref): appwire.ThreadReadResponse {
  return {
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-claim", length: 3 },
    thread: wireThread(requestedRef, {
      turns: [
        ...sourceItems.map((item, index) => ({
          id: `turn-${item.id}`,
          status: "completed" as const,
          itemsView: "full" as const,
          version: 1,
          items: [
            {
              ...item,
              type: "agentMessage",
              transcriptKey: item.id,
              turnId: `turn-${item.id}`,
              version: 1,
              position: { entry: index + 1, item: 0 },
            },
          ],
        })),
        {
          id: "itemless-error",
          status: "failed",
          itemsView: "full",
          version: 1,
          items: [],
          error: { message: "CLAIM_ERROR" },
        },
      ],
    }),
  };
}

const HANDOFF_TRANSCRIPT_CONFIG = makeTranscriptDisplayConfig({ kind: "preset", level: "full" });

// Ordered conversation identities: the canonical projection's content
// entries by display key, then any turn errors, so a failed turn survives
// the snapshot.
export function snapshot(model = threadsStore.getState().threads.get(ref)) {
  const projected = model ? projectThread(model as appwire.ThreadModel, HANDOFF_TRANSCRIPT_CONFIG) : undefined;
  const content = projected
    ? projected.turns.flatMap((turn) =>
        turn.entries.flatMap((entry) =>
          entry.kind === "item" || entry.kind === "critical"
            ? [{ key: entryDisplayKey(entry), index: entry.sourceIndex }]
            : [],
        ),
      )
    : [];
  content.sort((a, b) => a.index - b.index);
  return {
    foreground: foregroundHolds(ref),
    background: backgroundHolds(ref),
    items: model?.turns.flatMap((turn) => turn.items.map((item) => item.id)) ?? [],
    sources: [
      ...content.map((entry) => entry.key),
      ...(projected?.turns.flatMap((turn) =>
        isTurnError(turn.source.error) ? [`turn:${turn.source.id}:error`] : [],
      ) ?? []),
    ],
  };
}
