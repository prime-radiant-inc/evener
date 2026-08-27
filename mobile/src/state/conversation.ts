// ConversationStore — the active session's Zustand state. Wraps a
// ConversationService (which wraps an AppwireClient) and owns the conversation
// projection, draft, paging cursor, and mutation lifecycle.
//
// Generation safety (CRITICAL):
// - conversationGeneration increments on every open, close, and reset; late
//   frames from an older generation cannot overwrite a newer conversation's
//   state.
// - applyNotification checks threadId/ref against the current conversation and
//   silently drops mismatches.
// - Profile switching calls reset() before opening a new conversation.
// - The store never auto-retries a user mutation. On conflict, it restores the
//   draft (only if no new text was typed) and sets error.
//
// The store holds the MobileConversation projection (never the raw wire
// Thread). Notifications update the projection in place; a re-read via
// thread/read after evener/thread/resync is the authoritative refresh path
// (triggered by the injected coalescer, not timers).

import { create } from "zustand";
import type {
  AnyNotification,
  InputItem,
  ThreadCapabilities,
  ThreadItem,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type {
  MobileCapabilities,
  MobileConversation,
  MobileTimelineItem,
  MobileUsage,
} from "../conversation/model";
import type {
  ConversationService,
  LiveConversationService,
} from "../services/conversation";
import type { ActivityState } from "./activity";

export type ConversationStatus =
  | "idle"
  | "opening"
  | "open"
  | "error"
  | "closed";

// Mutation lifecycle state for send/steer/queue/interrupt. The store tracks
// the kind, pending/failed status, the exact draft snapshot at submission,
// and the conversation generation that initiated it. On failure, the failed
// state PERSISTS until a subsequent mutation or open clears it.
export interface ConversationMutationState {
  kind: "send" | "steer" | "queue" | "interrupt";
  status: "pending" | "failed";
  draftSnapshot: string | null;
  generation: number;
}

// An injected coalescer that batches rehydrate requests (from resync and
// unsupported item transitions) into a single rehydrate() call. The store
// calls requestRehydrate(ref) — the coalescer decides when to actually fire.
export interface RehydrateCoalescer {
  requestRehydrate(ref: string): void;
}

// --- limits and truncation helpers (centralized) ----------------------------

export const MAX_ITEM_BYTES = 64 * 1024; // 64 KiB in UTF-8 bytes
export const TRUNCATION_MARKER = "… truncated";
export const RETAINED_ITEM_CAP = 500;

// Truncate a string to maxBytes in UTF-8 + marker, ending with "… truncated"
// exactly once. Uses TextEncoder for byte-accurate measurement and ensures the
// result is valid Unicode (no split surrogate pairs).
const textEncoder = new TextEncoder();
const markerBytes = textEncoder.encode(TRUNCATION_MARKER);

export function truncateText(text: string, maxBytes: number): string {
  const encoded = textEncoder.encode(text);
  if (encoded.length <= maxBytes) return text;
  const targetBytes = maxBytes - markerBytes.length;
  // Decode a subarray up to targetBytes, then re-encode to verify the actual
  // byte length (the decoder may add replacement chars at a boundary).
  const decoder = new TextDecoder("utf-8", { fatal: false });
  let truncated = decoder.decode(encoded.subarray(0, targetBytes));
  // If the re-encoded truncated text + marker exceeds maxBytes (due to
  // replacement chars at the boundary), trim further.
  let truncatedBytes = textEncoder.encode(truncated);
  while (
    truncatedBytes.length + markerBytes.length > maxBytes &&
    truncated.length > 0
  ) {
    truncated = truncated.slice(0, -1);
    truncatedBytes = textEncoder.encode(truncated);
  }
  return truncated + TRUNCATION_MARKER;
}

// Check if text exceeds the byte limit (for setting truncated flag in projections).
export function exceedsByteLimit(text: string, maxBytes: number): boolean {
  return textEncoder.encode(text).length > maxBytes;
}

// Apply truncation to an item's text-bearing fields (arguments, output, error,
// markdown). Returns a new item with truncated fields.
function truncateItem(item: MobileTimelineItem): MobileTimelineItem {
  switch (item.kind) {
    case "assistant":
      return { ...item, markdown: truncateText(item.markdown, MAX_ITEM_BYTES) };
    case "activity":
      return {
        ...item,
        detail: {
          ...item.detail,
          arguments: item.detail.arguments
            ? truncateText(item.detail.arguments, MAX_ITEM_BYTES)
            : item.detail.arguments,
          output: item.detail.output
            ? truncateText(item.detail.output, MAX_ITEM_BYTES)
            : item.detail.output,
          error: item.detail.error
            ? truncateText(item.detail.error, MAX_ITEM_BYTES)
            : item.detail.error,
        },
      };
    default:
      return item;
  }
}

// Enforce the 500-item retained cap. When prepending older items, trims the
// NEWEST items (end of array) so the oldest rows are retained for paging.
// When appending, trims the oldest (front of array).
function capItems(
  items: MobileTimelineItem[],
  mode: "append" | "prepend" = "append",
): MobileTimelineItem[] {
  if (items.length <= RETAINED_ITEM_CAP) return items;
  if (mode === "prepend") {
    // Keep the oldest RETAINED_ITEM_CAP items (front of the merged array).
    return items.slice(0, RETAINED_ITEM_CAP);
  }
  // Default: keep the newest RETAINED_ITEM_CAP items.
  return items.slice(items.length - RETAINED_ITEM_CAP);
}

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
  readonly pendingMutation?: ConversationMutationState | null;

  open(service: ConversationService, ref: string): Promise<void>;
  openProjected?(
    service: LiveConversationService,
    activityStore: { getState: () => ActivityState },
    ref: string,
  ): Promise<void>;
  rehydrate?(
    service: LiveConversationService,
    activityStore: { getState: () => ActivityState },
  ): Promise<void>;
  setCoalescer?(coalescer: RehydrateCoalescer): void;
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

// Check if an error carries the actionUnavailable evenerErrorInfo.
function isActionUnavailableError(err: unknown): boolean {
  if (err === null || err === undefined || typeof err !== "object")
    return false;
  const obj = err as Record<string, unknown>;
  return obj.evenerErrorInfo === "actionUnavailable";
}

// Check a capability on the current conversation and throw if false. This
// mirrors the service's requireCap but runs in the store so the fake
// service (which has no capability gating) still respects capabilities.
function requireCap(
  conv: MobileConversation,
  cap: keyof MobileCapabilities,
  action: string,
): void {
  if (!conv.capabilities[cap]) {
    throw new Error(`Action "${action}" is not available for this thread`);
  }
}

// Project a wire ThreadItem into a mobile timeline item for insertion from
// item/started and item/completed notifications. This reuses the same field
// mapping as the full projection but handles a single item in isolation.
function projectSingleItem(item: ThreadItem): MobileTimelineItem | null {
  if (item.type === "userMessage") {
    return { kind: "user", id: item.id, text: item.text ?? "" };
  }
  if (item.type === "agentMessage") {
    return {
      kind: "assistant",
      id: item.id,
      markdown: `${item.text ?? ""}${item.delta ?? ""}`,
      streaming: item.status === "inProgress",
    };
  }
  if (item.type === "commandExecution") {
    return {
      kind: "activity",
      id: item.id,
      label: item.toolName ?? item.description?.trim() ?? "Tool",
      state:
        item.error !== undefined && item.error !== ""
          ? "failed"
          : item.status === "inProgress"
            ? "running"
            : "completed",
      detail: {
        arguments: item.argumentsJson,
        output: item.output,
        error: item.error,
        exitCode: item.exitCode,
        durationMs: item.durationMs,
        callId: item.callId,
      },
    };
  }
  if (item.type === "reasoning") {
    return {
      kind: "activity",
      id: item.id,
      label: "Reasoning",
      state: item.status === "inProgress" ? "running" : "completed",
      detail: { output: item.text },
    };
  }
  // Unknown item types — return null to signal an unsupported transition
  // that should trigger a coalesced rehydrate.
  return null;
}

// Known notification methods that we handle explicitly. The default branch
// only resyncs for unsupported item/* transitions, not for all unknown
// notifications, to avoid reread storms from unrelated notification families.
const ITEM_NOTIFICATION_METHODS = new Set([
  "item/started",
  "item/completed",
  "item/agentMessage/delta",
  "item/agentMessage/reset",
  "item/reasoning/summaryTextDelta",
  "item/toolOutput/delta",
]);

export function createConversationStore() {
  let conversationGen = 0;
  let coalescer: RehydrateCoalescer | null = null;

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
    pendingMutation: null,

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
        pendingMutation: null,
        conversationGeneration: gen,
      });
      try {
        const conv = await service.open(ref);
        // Reject if a newer conversation generation was opened during the await.
        if (gen !== conversationGen) return;
        set({
          conversation: {
            ...conv,
            items: capItems(conv.items.map(truncateItem)),
          },
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

    async openProjected(service, activityStore, ref) {
      const gen = ++conversationGen;
      // Reset thread-scoped state (draft, pending mutation) — presentation state
      // now lives outside the store (in live-ui-store).
      set({
        status: "opening",
        ref,
        error: null,
        conversation: null,
        olderCursor: null,
        loadingOlder: false,
        draft: "",
        pendingSend: null,
        pendingMutation: null,
        conversationGeneration: gen,
      });
      try {
        const { conversation, activity, olderCursor } =
          await service.readProjection(ref);
        if (gen !== conversationGen) return;
        set({
          conversation: {
            ...conversation,
            items: capItems(conversation.items.map(truncateItem)),
          },
          status: "open",
          olderCursor,
        });
        activityStore.getState().setView(activity);
        service.subscribeNotifications((n) => {
          if (gen !== conversationGen) return;
          // Route notifications to BOTH stores — conversation and activity.
          get().applyNotification(n);
          activityStore.getState().applyNotification(n);
        });
      } catch (err) {
        if (gen !== conversationGen) return;
        set({
          status: "error",
          error: err instanceof Error ? err.message : String(err),
        });
      }
    },

    async rehydrate(service, activityStore) {
      // Rehydrate uses readProjection to refresh the conversation without
      // calling destructive open(). Preserves draft.
      const state = get();
      if (state.ref === null) return;
      const ref = state.ref;
      const currentDraft = state.draft;
      const gen = state.conversationGeneration;
      try {
        const { conversation, activity, olderCursor } =
          await service.readProjection(ref);
        // Guard: a newer generation may have opened during the await.
        if (get().conversationGeneration !== gen) {
          return;
        }
        set({
          conversation: {
            ...conversation,
            items: capItems(conversation.items.map(truncateItem)),
          },
          olderCursor,
          // Preserve draft
          draft: currentDraft,
          error: null,
        });
        activityStore.getState().setView(activity);
      } catch (err) {
        // Stale safety: only set error if the generation hasn't changed.
        if (get().conversationGeneration === gen) {
          set({
            error: err instanceof Error ? err.message : String(err),
          });
        }
      }
    },

    setCoalescer(c) {
      coalescer = c;
    },

    async loadOlder(service) {
      const state = get();
      if (state.loadingOlder || state.conversation === null) return;
      const cursor = state.olderCursor ?? "";
      const gen = state.conversationGeneration;
      set({ loadingOlder: true });
      try {
        const result = await service.loadOlder(cursor);
        // Guard: the conversation generation may have changed during the await.
        if (get().conversationGeneration !== gen) {
          set({ loadingOlder: false });
          return;
        }
        const currentConv = get().conversation;
        if (currentConv !== null) {
          // Prepend older items, then trim from the newest (end) so the oldest
          // rows are retained for continued paging utility.
          const merged = capItems(
            [...result.items.map(truncateItem), ...currentConv.items],
            "prepend",
          );
          set({
            conversation: { ...currentConv, items: merged },
            olderCursor: result.nextCursor ?? null,
            loadingOlder: false,
          });
        }
      } catch (err) {
        // Stale safety: only set error if generation hasn't changed.
        if (get().conversationGeneration === gen) {
          set({
            loadingOlder: false,
            error: err instanceof Error ? err.message : String(err),
          });
        }
      }
    },

    setDraft(text) {
      set({ draft: text });
    },

    async send(service, input) {
      const state = get();
      if (state.conversation === null) return;
      requireCap(state.conversation, "send", "send");
      const draftText = state.draft;
      const gen = state.conversationGeneration;
      const mutation: ConversationMutationState = {
        kind: "send",
        status: "pending",
        draftSnapshot: draftText,
        generation: gen,
      };
      set({ draft: "", pendingSend: "pending", pendingMutation: mutation });
      try {
        await service.send(input);
        if (get().pendingMutation?.generation === gen) {
          set({ pendingSend: null, pendingMutation: null, error: null });
        }
      } catch (err) {
        await handleMutationError(
          err,
          service,
          state.ref,
          gen,
          mutation,
          draftText,
          set,
          get,
        );
      }
    },

    async steer(service, input) {
      const state = get();
      if (state.conversation === null) return;
      requireCap(state.conversation, "steer", "steer");
      const draftText = state.draft;
      const gen = state.conversationGeneration;
      const mutation: ConversationMutationState = {
        kind: "steer",
        status: "pending",
        draftSnapshot: draftText,
        generation: gen,
      };
      // Steer/queue clear the draft on submit like send.
      set({ draft: "", pendingMutation: mutation });
      try {
        await service.steer(input);
        if (get().pendingMutation?.generation === gen) {
          set({ pendingMutation: null, error: null });
        }
      } catch (err) {
        await handleMutationError(
          err,
          service,
          state.ref,
          gen,
          mutation,
          draftText,
          set,
          get,
        );
      }
    },

    async queue(service, input) {
      const state = get();
      if (state.conversation === null) return;
      requireCap(state.conversation, "queue", "queue");
      const draftText = state.draft;
      const gen = state.conversationGeneration;
      const mutation: ConversationMutationState = {
        kind: "queue",
        status: "pending",
        draftSnapshot: draftText,
        generation: gen,
      };
      set({ draft: "", pendingMutation: mutation });
      try {
        await service.queue(input);
        if (get().pendingMutation?.generation === gen) {
          set({ pendingMutation: null, error: null });
        }
      } catch (err) {
        await handleMutationError(
          err,
          service,
          state.ref,
          gen,
          mutation,
          draftText,
          set,
          get,
        );
      }
    },

    async interrupt(service) {
      const state = get();
      if (state.conversation === null) return;
      requireCap(state.conversation, "interrupt", "interrupt");
      const gen = state.conversationGeneration;
      const mutation: ConversationMutationState = {
        kind: "interrupt",
        status: "pending",
        // Interrupt does NOT snapshot the draft — it should remain as-is.
        draftSnapshot: null,
        generation: gen,
      };
      // Interrupt does NOT clear the draft.
      set({ pendingMutation: mutation });
      try {
        await service.interrupt();
        if (get().pendingMutation?.generation === gen) {
          set({ pendingMutation: null, error: null });
        }
      } catch (err) {
        await handleMutationError(
          err,
          service,
          state.ref,
          gen,
          mutation,
          null,
          set,
          get,
        );
      }
    },

    close() {
      // Increment generation so late frames from the closed conversation
      // cannot repopulate the store.
      ++conversationGen;
      set({
        status: "closed",
        conversation: null,
        ref: null,
        draft: "",
        pendingSend: null,
        pendingMutation: null,
        olderCursor: null,
        loadingOlder: false,
        conversationGeneration: conversationGen,
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

        case "item/started": {
          const params = n.params as { item: ThreadItem };
          const projected = projectSingleItem(params.item);
          if (projected !== null) {
            const truncated = truncateItem(projected);
            const existingIdx = conv.items.findIndex(
              (i) => i.id === params.item.id,
            );
            if (existingIdx >= 0) {
              set({
                conversation: {
                  ...conv,
                  items: conv.items.map((i, idx) =>
                    idx === existingIdx ? truncated : i,
                  ),
                },
              });
            } else {
              set({
                conversation: {
                  ...conv,
                  items: capItems([...conv.items, truncated]),
                },
              });
            }
          } else {
            // Unsupported item transition — coalesce to one rehydrate.
            if (coalescer !== null && state.ref !== null) {
              coalescer.requestRehydrate(state.ref);
            }
          }
          break;
        }

        case "item/completed": {
          const params = n.params as { item: ThreadItem };
          const projected = projectSingleItem(params.item);
          if (projected !== null) {
            const truncated = truncateItem(projected);
            const existingIdx = conv.items.findIndex(
              (i) => i.id === params.item.id,
            );
            if (existingIdx >= 0) {
              // Replace existing item.
              set({
                conversation: {
                  ...conv,
                  items: conv.items.map((i, idx) =>
                    idx === existingIdx ? truncated : i,
                  ),
                },
              });
            } else {
              // UPSERT: insert the authoritative completed item even if the
              // start notification was missed.
              set({
                conversation: {
                  ...conv,
                  items: capItems([...conv.items, truncated]),
                },
              });
            }
          } else {
            if (coalescer !== null && state.ref !== null) {
              coalescer.requestRehydrate(state.ref);
            }
          }
          break;
        }

        case "item/agentMessage/delta": {
          const params = n.params as { itemId: string; delta: string };
          const existing = conv.items.find(
            (i) => i.id === params.itemId && i.kind === "assistant",
          );
          if (existing) {
            set({
              conversation: {
                ...conv,
                items: conv.items.map((item) =>
                  item.kind === "assistant" && item.id === params.itemId
                    ? {
                        ...item,
                        markdown: truncateText(
                          item.markdown + params.delta,
                          MAX_ITEM_BYTES,
                        ),
                      }
                    : item,
                ),
              },
            });
          } else {
            // Delta targeting missing item — trigger resync.
            if (coalescer !== null && state.ref !== null) {
              coalescer.requestRehydrate(state.ref);
            }
          }
          break;
        }

        case "item/agentMessage/reset": {
          const params = n.params as { itemId: string };
          const existing = conv.items.find(
            (i) => i.id === params.itemId && i.kind === "assistant",
          );
          if (existing) {
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
          } else {
            if (coalescer !== null && state.ref !== null) {
              coalescer.requestRehydrate(state.ref);
            }
          }
          break;
        }

        case "item/reasoning/summaryTextDelta": {
          const params = n.params as { itemId: string; delta: string };
          const existing = conv.items.find(
            (i) => i.id === params.itemId && i.kind === "activity",
          );
          if (existing) {
            set({
              conversation: {
                ...conv,
                items: conv.items.map((item) =>
                  item.kind === "activity" && item.id === params.itemId
                    ? {
                        ...item,
                        detail: {
                          ...item.detail,
                          output: truncateText(
                            (item.detail.output ?? "") + params.delta,
                            MAX_ITEM_BYTES,
                          ),
                        },
                      }
                    : item,
                ),
              },
            });
          } else {
            // Delta targeting missing or wrong-kind item — trigger resync.
            if (coalescer !== null && state.ref !== null) {
              coalescer.requestRehydrate(state.ref);
            }
          }
          break;
        }

        case "item/toolOutput/delta": {
          const params = n.params as { itemId: string; delta: string };
          const existing = conv.items.find(
            (i) => i.id === params.itemId && i.kind === "activity",
          );
          if (existing) {
            set({
              conversation: {
                ...conv,
                items: conv.items.map((item) =>
                  item.kind === "activity" && item.id === params.itemId
                    ? {
                        ...item,
                        detail: {
                          ...item.detail,
                          output: truncateText(
                            (item.detail.output ?? "") + params.delta,
                            MAX_ITEM_BYTES,
                          ),
                        },
                      }
                    : item,
                ),
              },
            });
          } else {
            if (coalescer !== null && state.ref !== null) {
              coalescer.requestRehydrate(state.ref);
            }
          }
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
            conversation: {
              ...conv,
              items: capItems([...conv.items, failureItem]),
            },
          });
          break;
        }

        // evener/thread/resync triggers a coalesced rehydrate via the injected
        // coalescer. The store does not re-read on its own.
        case "evener/thread/resync": {
          if (coalescer !== null && state.ref !== null) {
            coalescer.requestRehydrate(state.ref);
          }
          break;
        }

        // Default: only resync for unsupported item/* transitions, not for
        // all unknown notifications — to avoid reread storms from unrelated
        // notification families.
        default: {
          if (
            typeof n.method === "string" &&
            n.method.startsWith("item/") &&
            !ITEM_NOTIFICATION_METHODS.has(n.method)
          ) {
            if (coalescer !== null && state.ref !== null) {
              coalescer.requestRehydrate(state.ref);
            }
          }
          break;
        }
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
        pendingMutation: null,
        conversationGeneration: conversationGen,
      });
    },
  }));
}

// Shared mutation error handler: on actionUnavailable, uses the non-subscribing
// refreshCapabilities (never open()) to publish refreshed caps before
// surfacing the error. On failure, the failed mutation state PERSISTS (not
// cleared to null). The draft is only restored if no new text was typed
// during the in-flight mutation.
async function handleMutationError(
  err: unknown,
  service: ConversationService,
  ref: string | null,
  gen: number,
  mutation: ConversationMutationState,
  draftSnapshot: string | null,
  set: (partial: Partial<ConversationState>) => void,
  get: () => ConversationState,
): Promise<void> {
  if (isActionUnavailableError(err) && ref !== null) {
    // Use the non-subscribing capability refresh — never open().
    // Only available on LiveConversationService; check for the method.
    const liveService = service as LiveConversationService;
    if (typeof liveService.refreshCapabilities === "function") {
      try {
        const refreshed = await liveService.refreshCapabilities();
        if (get().conversationGeneration === gen && refreshed !== null) {
          const currentConv = get().conversation;
          if (currentConv !== null) {
            set({
              conversation: {
                ...currentConv,
                capabilities: { ...refreshed },
              },
            });
          }
        }
      } catch {
        // If refresh fails, continue to surface the original error.
      }
    }
  }
  if (get().pendingMutation?.generation === gen) {
    // The failed mutation state PERSISTS — do NOT clear pendingMutation.
    const currentDraft = get().draft;
    // Only restore the draft if no new text was typed during the mutation.
    const shouldRestore = currentDraft === "" && draftSnapshot !== null;
    set({
      pendingMutation: { ...mutation, status: "failed" },
      pendingSend: null,
      ...(shouldRestore ? { draft: draftSnapshot } : {}),
      error: err instanceof Error ? err.message : String(err),
    });
  }
}

// Re-export the MobileCapabilities type for consumers that import from the
// store module.
export type { MobileCapabilities, MobileConversation, MobileTimelineItem };
