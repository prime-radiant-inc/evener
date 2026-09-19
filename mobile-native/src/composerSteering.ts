import { decideSteerRoute } from "@evener/appwire-client";
import type { InputItem } from "@evener/appwire-client";
import type { ConversationService } from "../../mobile/src/services/conversation";
import type { createConversationStore } from "../../mobile/src/state/conversation";

export async function steerComposer(
  store: ReturnType<typeof createConversationStore>,
  service: ConversationService,
  input: InputItem[],
) {
  const conversation = store.getState().conversation;
  if (!conversation) return false;
  const route = decideSteerRoute({
    hasText: input.some((item) => item.type === "text" && !!item.text?.trim()),
    hasAttachments: input.some((item) => item.type === "image"),
    queueDepth: conversation.queue?.depth ?? 0,
  });
  if (route === "none") return false;
  return store
    .getState()
    .steer(
      service,
      input,
      route === "drain" ? conversation.queue?.revision : undefined,
    );
}
