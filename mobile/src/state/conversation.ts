// ConversationStore — the active session's Zustand state. Wraps a
// ConversationService (which wraps an AppwireClient) and owns the conversation
// projection, draft, paging cursor, and mutation lifecycle.
//
// Generation safety (CRITICAL):
// - conversationGeneration increments on every open; late frames from an older
//   generation cannot overwrite a newer conversation's state.
// - applyNotification checks threadId/ref against the current conversation and
//   silently drops mismatches.
// - Profile switching calls reset() before opening a new conversation.
// - The store never auto-retries a user mutation. On conflict, it restores the
//   draft and sets error.
//
// The store holds the MobileConversation projection (never the raw wire
// Thread). Notifications update the projection in place; a re-read via
// thread/read after evener/thread/resync is the authoritative refresh path
// (triggered by the screen layer, not the store).

import { create } from "zustand";
import type {
  AnyNotification,
  InputItem,
  MutationReceipt,
  ThreadCapabilities,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type {
  MobileCapabilities,
  MobileConversation,
  MobileTimelineItem,
  MobileUsage,
} from "../conversation/model";
import type { ConversationService } from "../services/conversation";

export type ConversationStatus =
  | "idle"
  | "opening"
  | "open"
  | "error"
  | "closed";

export interface ConversationState {
  readonly ref: string | null;
  readonly profileId: string | null;
  readonly connectionGeneration: number;
  readonly conversationGeneration: number;

  readonly conversation: MobileConversation | null;
  readonly olderCursor: string | null;
  readonly loadingOlder: boolean;
  readonly status: ConversationStatus;
  readonly error: string | null;

  readonly draft: string;
  readonly pendingSend: string | null;

  open(service: ConversationService, ref: string): Promise<void>;
  loadOlder(service: ConversationService): Promise<void>;
  setDraft(text: string): void;
  send(service: ConversationService, input: InputItem[]): Promise<void>;
  steer(service: ConversationService, input: InputItem[]): Promise<void>;
  queue(service: ConversationService, input: InputItem[]): Promise<void>;
  interrupt(service: ConversationService): Promise<void>;
  close(): void;
  applyNotification(n: AnyNotification): void;
  reset(): void;
}

// Extract threadId/ref from a notification's params, returning null if the
// notification has neither (some global notifications don't).
function notificationRef(
  n: AnyNotification,
): { threadId?: string; ref?: string } | null {
  const params = n.params as Record<string, unknown> | undefined;
  if (params === undefined || params === null) return null;
  const threadId =
    typeof params.threadId === "string" ? params.threadId : undefined;
  const ref = typeof params.ref === "string" ? params.ref : undefined;
  if (threadId === undefined && ref === undefined) return null;
  return { threadId, ref };
}

export function createConversationStore() {
  let conversationGen = 0;

  return create<ConversationState>((set, get) => ({
    ref: null,
    profileId: null,
    connectionGeneration: 0,
    conversationGeneration: 0,

    conversation: null,
    olderCursor: null,
    loadingOlder: false,
    status: "idle",
    error: null,

    draft: "",
    pendingSend: null,

    async open(service, ref) {
      // Increment conversation generation so late frames from a previous
      // conversation are rejected.
      const gen = ++conversationGen;
      set({
        status: "opening",
        ref,
        error: null,
        conversation: null,
        olderCursor: null,
        loadingOlder: false,
        draft: "",
        pendingSend: null,
        conversationGeneration: gen,
      });
      try {
        const conv = await service.open(ref);
        // Reject if a newer conversation generation was opened during the await.
        if (gen !== conversationGen) return;
        set({
          conversation: conv,
          status: "open",
          olderCursor: null,
        });
        // Subscribe to notifications for this thread.
        service.subscribeNotifications((n) => {
          if (gen !== conversationGen) return;
          get().applyNotification(n);
        });
      } catch (err) {
        if (gen !== conversationGen) return;
        set({
          status: "error",
          error: err instanceof Error ? err.message : String(err),
        });
      }
    },

    async loadOlder(service) {
      const state = get();
      if (state.loadingOlder || state.conversation === null) return;
      const cursor = state.olderCursor ?? "";
      set({ loadingOlder: true });
      try {
        const result = await service.loadOlder(cursor);
        // Guard: the conversation may have changed during the await.
        if (get().conversation === null || get().ref !== state.ref) {
          set({ loadingOlder: false });
          return;
        }
        const currentConv = get().conversation;
        if (currentConv !== null) {
          set({
            conversation: {
              ...currentConv,
              items: [...result.items, ...currentConv.items],
            },
            olderCursor: result.nextCursor ?? null,
            loadingOlder: false,
          });
        }
      } catch (err) {
        set({
          loadingOlder: false,
          error: err instanceof Error ? err.message : String(err),
        });
      }
    },

    setDraft(text) {
      set({ draft: text });
    },

    async send(service, input) {
      const state = get();
      if (state.conversation === null) return;
      const draftText = state.draft;
      set({ draft: "", pendingSend: "pending" });
      try {
        const receipt: MutationReceipt = await service.send(input);
        // Only clear pendingSend if it's still ours. A newer conversation would
        // have reset pendingSend to null already.
        if (get().pendingSend !== null) {
          set({ pendingSend: null, error: null });
        }
        void receipt;
      } catch (err) {
        // On conflict, restore the draft and show the error. Never auto-retry.
        set({
          pendingSend: null,
          draft: draftText,
          error: err instanceof Error ? err.message : String(err),
        });
      }
    },

    async steer(service, input) {
      const state = get();
      if (state.conversation === null) return;
      try {
        await service.steer(input);
        set({ error: null });
      } catch (err) {
        set({ error: err instanceof Error ? err.message : String(err) });
      }
    },

    async queue(service, input) {
      const state = get();
      if (state.conversation === null) return;
      try {
        await service.queue(input);
        set({ error: null });
      } catch (err) {
        set({ error: err instanceof Error ? err.message : String(err) });
      }
    },

    async interrupt(service) {
      const state = get();
      if (state.conversation === null) return;
      try {
        await service.interrupt();
        set({ error: null });
      } catch (err) {
        set({ error: err instanceof Error ? err.message : String(err) });
      }
    },

    close() {
      set({
        status: "closed",
        conversation: null,
        ref: null,
        draft: "",
        pendingSend: null,
        olderCursor: null,
        loadingOlder: false,
      });
    },

    applyNotification(n) {
      const state = get();
      if (state.conversation === null) return;

      // Check threadId/ref against the current conversation and silently drop
      // mismatches.
      const nref = notificationRef(n);
      if (nref !== null) {
        const currentId = state.conversation.id;
        const currentRef = state.ref;
        const idMatch =
          nref.threadId === undefined || nref.threadId === currentId;
        const refMatch = nref.ref === undefined || nref.ref === currentRef;
        if (!idMatch || !refMatch) return;
      }

      const conv = state.conversation;
      switch (n.method) {
        case "thread/status/changed": {
          const params = n.params as {
            status: { type: string };
            capabilities?: ThreadCapabilities;
          };
          set({
            conversation: {
              ...conv,
              status: params.status.type,
              capabilities: params.capabilities
                ? { ...params.capabilities }
                : conv.capabilities,
            },
          });
          break;
        }

        case "thread/queueChanged": {
          const params = n.params as {
            queue: { depth?: number; preview?: string[]; texts?: string[] };
          };
          set({
            conversation: {
              ...conv,
              queue: {
                depth: params.queue.depth ?? 0,
                preview: params.queue.preview ?? params.queue.texts ?? [],
              },
            },
          });
          break;
        }

        case "evener/thread/name/changed": {
          const params = n.params as { name: string };
          set({
            conversation: { ...conv, name: params.name },
          });
          break;
        }

        case "thread/model/changed": {
          const params = n.params as {
            modelProvider: string;
            reasoningEffortLevels?: string[];
            supportsReasoning?: boolean;
          };
          set({
            conversation: {
              ...conv,
              modelProvider: params.modelProvider,
              reasoningEffortLevels: params.reasoningEffortLevels,
              supportsReasoning: params.supportsReasoning,
            },
          });
          break;
        }

        case "thread/reasoning-effort/changed": {
          const params = n.params as { reasoningEffort?: string };
          set({
            conversation: { ...conv, reasoningEffort: params.reasoningEffort },
          });
          break;
        }

        case "turn/started": {
          set({
            conversation: { ...conv, status: "running" },
          });
          break;
        }

        case "turn/completed": {
          const params = n.params as {
            turn: { usage?: MobileUsage; status: string };
          };
          set({
            conversation: {
              ...conv,
              status: conv.status === "running" ? "ready" : conv.status,
              usage: params.turn.usage
                ? { ...conv.usage, ...params.turn.usage }
                : conv.usage,
            },
          });
          break;
        }

        case "item/agentMessage/delta": {
          const params = n.params as { itemId: string; delta: string };
          set({
            conversation: {
              ...conv,
              items: conv.items.map((item) =>
                item.kind === "assistant" && item.id === params.itemId
                  ? { ...item, markdown: item.markdown + params.delta }
                  : item,
              ),
            },
          });
          break;
        }

        case "item/agentMessage/reset": {
          const params = n.params as { itemId: string };
          set({
            conversation: {
              ...conv,
              items: conv.items.map((item) =>
                item.kind === "assistant" && item.id === params.itemId
                  ? { ...item, markdown: "" }
                  : item,
              ),
            },
          });
          break;
        }

        case "warning": {
          const params = n.params as { message?: string; title?: string };
          const id = `warning:${params.title ?? params.message ?? Date.now()}`;
          const failureItem: MobileTimelineItem = {
            kind: "failure",
            id,
            title: params.title ?? "Warning",
            detail: params.message ?? "",
          };
          set({
            conversation: { ...conv, items: [...conv.items, failureItem] },
          });
          break;
        }

        // evener/thread/resync should trigger a re-read. The store cannot
        // re-read on its own (it has no service reference in applyNotification),
        // so it sets a flag the screen layer can watch. For now, we do nothing
        // — the screen layer calls service.open() again on resync.
        case "evener/thread/resync":
          break;

        // item/started, item/completed, item/reasoning/summaryTextDelta,
        // item/toolOutput/delta, and other notifications update item-level
        // state. The simplest correct approach is to update the projection;
        // but without the full Thread we cannot re-project. These are handled
        // by the screen layer triggering a re-read when needed.
        default:
          break;
      }
    },

    reset() {
      // Invalidate the current conversation generation so late frames are
      // rejected, then return to idle.
      ++conversationGen;
      set({
        ref: null,
        profileId: null,
        conversation: null,
        olderCursor: null,
        loadingOlder: false,
        status: "idle",
        error: null,
        draft: "",
        pendingSend: null,
        conversationGeneration: conversationGen,
      });
    },
  }));
}

// Re-export the MobileCapabilities type for consumers that import from the
// store module.
export type { MobileCapabilities, MobileConversation, MobileTimelineItem };
