import { decideSteerRoute } from "../../cmd/evener-hub/frontend/src/panes/session/composer/submitRouting";
import type { InputItem } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationService } from "../../mobile/src/services/conversation";
import type { createConversationStore } from "../../mobile/src/state/conversation";

export async function steerComposer(
  store: ReturnType<typeof createConversationStore>,
  service: ConversationService,
  input: InputItem[],
) {
  const conversation = store.getState().conversation;
  if (!conversation) return;
  const route = decideSteerRoute({
    hasText: input.some((item) => item.type === "text" && !!item.text?.trim()),
    hasAttachments: input.some((item) => item.type === "image"),
    queueDepth: conversation.queue.depth,
  });
  if (route === "none") return;
  await store
    .getState()
    .steer(
      service,
      input,
      route === "drain" ? conversation.queue.revision : undefined,
    );
}
