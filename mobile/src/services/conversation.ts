// ConversationService wraps an AppwireClient to provide a typed, capability-gated
// API for opening, reading, paging, and mutating a single session thread. It is
// the protocol/native service layer between the Zustand ConversationStore and
// the wire: the store calls the service; the service calls the client.
//
// The service does NOT create or own the socket — it wraps an existing
// AppwireClient (or any structurally compatible client, e.g. FakeClient in
// tests). It projects the wire Thread to a MobileConversation via projectThread,
// subscribes to notifications, and enforces ThreadCapabilities before every
// mutation: a false capability blocks the request entirely, never reaching the
// wire. A server-reported actionUnavailable triggers a non-subscribing
// capability re-read so the store's cached capabilities stay fresh.
//
// Generation safety is enforced by the store, not the service: the service is
// stateless across opens (it holds only the current ref and last-read
// capabilities). The store owns profile/connection/conversation generations.

import type { AppwireClient } from "../../../cmd/evener-hub/frontend/src/protocol/client";
import type {
  AnyNotification,
  InputItem,
  MethodName,
  MethodTypes,
  MutationReceipt,
  Thread,
  ThreadCapabilities,
  ThreadReadResponse,
  ThreadTurnsListResponse,
  TurnCancelQueuedResponse,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type {
  MobileConversation,
  MobileTimelineItem,
} from "../conversation/model";
import { projectThread } from "../conversation/project";
import type { ActivityView } from "./activity";
import { createActivityService } from "./activity";

// The bounded read page limit and retained item cap, centralized so every
// caller uses the same constant.
export const READ_TURN_LIMIT = 50;
export const RETAINED_ITEM_CAP = 500;

// The narrow client surface the service depends on. Structurally compatible
// with AppwireClient and FakeClient, so tests inject a FakeClient without
// pulling the real class's reconnect/heartbeat machinery.
export interface ConversationClientLike {
  request<M extends MethodName>(
    method: M,
    params: MethodTypes[M]["params"],
    opts?: { timeoutMs?: number },
  ): Promise<MethodTypes[M]["result"]>;
  onNotification(cb: (n: AnyNotification) => void): () => void;
}

// A function that generates a clientMutationId. Default uses crypto.randomUUID
// when available, falling back to a counter; tests inject a deterministic one.
export type IdFactory = () => string;

export interface ConversationServiceOptions {
  readonly idFactory?: IdFactory;
}

export interface ConversationReadProjection {
  conversation: MobileConversation;
  activity: ActivityView;
  olderCursor: string | null;
}

// The canonical service interface — preserved for screen test mocks that only
// need the basic open/send/steer/queue/interrupt/close surface.
export interface ConversationService {
  open(ref: string, cursor?: string): Promise<MobileConversation>;
  loadOlder(cursor: string): Promise<{
    items: MobileTimelineItem[];
    nextCursor?: string;
  }>;
  subscribeNotifications(handler: (n: AnyNotification) => void): () => void;
  send(input: InputItem[]): Promise<MutationReceipt>;
  steer(input: InputItem[]): Promise<MutationReceipt>;
  queue(input: InputItem[]): Promise<MutationReceipt>;
  interrupt(): Promise<MutationReceipt>;
  compact(): Promise<void>;
  shutdown(): Promise<void>;
  changeModel(modelProvider: string, model: string): Promise<void>;
  setReasoningEffort(effort: string): Promise<void>;
  rename(name: string): Promise<void>;
  cancelQueued(
    index: number,
    expectedEntryId: string,
  ): Promise<TurnCancelQueuedResponse>;
  close(): void;
}

// Required live behavior interface for the live read/projection path. This
// must be implemented by any service that supports the live conversation
// features (readProjection, openProjected, rehydrate, coalescer, mutation
// state). It extends the canonical ConversationService with the live-only
// methods that must not be optional-fallback to the old open() path.
export interface LiveConversationService extends ConversationService {
  readProjection(ref: string): Promise<ConversationReadProjection>;
  refreshCapabilities(ref: string): Promise<ThreadCapabilities | null>;
}

let defaultIdCounter = 0;
function defaultIdFactory(): string {
  if (
    typeof crypto !== "undefined" &&
    typeof crypto.randomUUID === "function"
  ) {
    return crypto.randomUUID();
  }
  defaultIdCounter += 1;
  return `cmid-${Date.now()}-${defaultIdCounter}`;
}

export function createConversationService(
  client: ConversationClientLike | AppwireClient,
  options: ConversationServiceOptions = {},
): LiveConversationService {
  const idFactory: IdFactory = options.idFactory ?? defaultIdFactory;
  const activityService = createActivityService();

  // Current thread identity and capabilities, set by open(). Mutations check
  // these before reaching the wire; a re-read on actionUnavailable refreshes
  // them.
  let ref: string | null = null;
  let capabilities: ThreadCapabilities | null = null;
  let notificationUnsub: (() => void) | null = null;

  function requireRef(): string {
    if (ref === null) throw new Error("ConversationService: no thread open");
    return ref;
  }

  function requireCap(cap: keyof ThreadCapabilities, action: string): void {
    if (capabilities === null || !capabilities[cap]) {
      throw new Error(`Action "${action}" is not available for this thread`);
    }
  }

  // Non-subscribing capability refresh: reads the thread metadata WITHOUT
  // subscribing or replacing the subscription, and WITHOUT loading all turns.
  // Returns the refreshed capabilities. This is the only path the store should
  // use for actionUnavailable recovery — it never disturbs the active
  // subscription.
  async function refreshCapabilities(
    threadRef: string,
  ): Promise<ThreadCapabilities | null> {
    const response: ThreadReadResponse = await client.request("thread/read", {
      ref: threadRef,
      includeTurns: false,
      subscribe: false,
    });
    capabilities = response.thread.evener.capabilities;
    return capabilities;
  }

  // withCapabilityRefresh wraps a mutation: if the server rejects with
  // actionUnavailable, the service just re-throws — it does NOT auto-refresh
  // capabilities. Exactly one non-subscribing thread/read occurs, and it is
  // driven by the store's handleMutationError (F2). The store is the sole
  // caller of refreshCapabilities; the service never duplicates the read.
  async function withCapabilityRefresh<T>(
    _action: string,
    fn: () => Promise<T>,
  ): Promise<T> {
    return fn();
  }

  return {
    async open(threadRef, cursor) {
      ref = threadRef;
      const response: ThreadReadResponse = await client.request("thread/read", {
        ref: threadRef,
        includeTurns: true,
        subscribe: true,
        replaceSubscription: true,
        ...(cursor !== undefined ? { turnLimit: READ_TURN_LIMIT } : {}),
      });
      capabilities = response.thread.evener.capabilities;
      return projectThread(response.thread);
    },

    async readProjection(threadRef) {
      ref = threadRef;
      const response: ThreadReadResponse = await client.request("thread/read", {
        ref: threadRef,
        includeTurns: true,
        subscribe: true,
        replaceSubscription: true,
        turnLimit: READ_TURN_LIMIT,
      });
      capabilities = response.thread.evener.capabilities;
      const conversation = projectThread(response.thread);
      const activity = activityService.projectActivity(response.thread);
      return {
        conversation,
        activity,
        olderCursor: response.olderCursor ?? null,
      };
    },

    async loadOlder(cursor) {
      const threadRef = requireRef();
      const response: ThreadTurnsListResponse = await client.request(
        "thread/turns/list",
        { ref: threadRef, cursor, limit: READ_TURN_LIMIT },
      );
      // Project the older turns into mobile items by projecting a minimal
      // Thread containing just these turns. projectThread handles empty/missing
      // turns gracefully; we only need the item projection, not the full
      // conversation metadata.
      const items = projectOlderTurns(response.data);
      return { items, nextCursor: response.nextCursor };
    },

    refreshCapabilities,

    subscribeNotifications(handler) {
      if (notificationUnsub !== null) {
        notificationUnsub();
      }
      notificationUnsub = client.onNotification(handler);
      return () => {
        if (notificationUnsub !== null) {
          notificationUnsub();
          notificationUnsub = null;
        }
      };
    },

    async send(input) {
      requireCap("send", "send");
      const threadRef = requireRef();
      const clientMutationId = idFactory();
      return withCapabilityRefresh("send", () =>
        client.request("turn/start", {
          ref: threadRef,
          clientMutationId,
          input,
        }),
      ).then((r) => (r as { receipt: MutationReceipt }).receipt);
    },

    async steer(input) {
      requireCap("steer", "steer");
      const threadRef = requireRef();
      const clientMutationId = idFactory();
      return withCapabilityRefresh("steer", () =>
        client.request("turn/steer", {
          ref: threadRef,
          clientMutationId,
          input,
        }),
      ).then((r) => (r as { receipt: MutationReceipt }).receipt);
    },

    async queue(input) {
      requireCap("queue", "queue");
      const threadRef = requireRef();
      const clientMutationId = idFactory();
      return withCapabilityRefresh("queue", () =>
        client.request("turn/queue", {
          ref: threadRef,
          clientMutationId,
          input,
        }),
      ).then((r) => (r as { receipt: MutationReceipt }).receipt);
    },

    async interrupt() {
      requireCap("interrupt", "interrupt");
      const threadRef = requireRef();
      const clientMutationId = idFactory();
      return withCapabilityRefresh("interrupt", () =>
        client.request("turn/interrupt", {
          ref: threadRef,
          clientMutationId,
        }),
      ).then((r) => (r as { receipt: MutationReceipt }).receipt);
    },

    async compact() {
      requireCap("compact", "compact");
      const threadRef = requireRef();
      await withCapabilityRefresh("compact", () =>
        client.request("thread/compact/start", { ref: threadRef }),
      );
    },

    async shutdown() {
      requireCap("shutdown", "shutdown");
      const threadRef = requireRef();
      await withCapabilityRefresh("shutdown", () =>
        client.request("thread/shutdown", { ref: threadRef }),
      );
    },

    async changeModel(modelProvider, model) {
      requireCap("changeModel", "changeModel");
      const threadRef = requireRef();
      await withCapabilityRefresh("changeModel", () =>
        client.request("thread/model/set", {
          ref: threadRef,
          modelProvider,
          model,
        }),
      );
    },

    async setReasoningEffort(effort) {
      const threadRef = requireRef();
      await withCapabilityRefresh("setReasoningEffort", () =>
        client.request("thread/reasoning-effort/set", {
          ref: threadRef,
          reasoningEffort: effort,
        }),
      );
    },

    async rename(name) {
      requireCap("rename", "rename");
      const threadRef = requireRef();
      await withCapabilityRefresh("rename", () =>
        client.request("evener/thread/name/set", { ref: threadRef, name }),
      );
    },

    async cancelQueued(index, expectedEntryId) {
      const threadRef = requireRef();
      const clientMutationId = idFactory();
      return withCapabilityRefresh("cancelQueued", () =>
        client.request("turn/cancelQueued", {
          ref: threadRef,
          index,
          clientMutationId,
          expectedEntryId,
        }),
      );
    },

    close() {
      if (notificationUnsub !== null) {
        notificationUnsub();
        notificationUnsub = null;
      }
      ref = null;
      capabilities = null;
    },
  };
}

// Project older Turn[] into mobile timeline items. We build a minimal Thread
// with just these turns and project it, extracting only the items. This reuses
// the same projection logic (clustering, forward-compat) as open().
function projectOlderTurns(
  turns: ThreadTurnsListResponse["data"],
): MobileTimelineItem[] {
  if (turns.length === 0) return [];
  const thread: Thread = {
    id: "older",
    sessionId: "older",
    preview: "",
    ephemeral: false,
    modelProvider: "",
    createdAt: 0,
    updatedAt: 0,
    status: { type: "ready" },
    cwd: "",
    cliVersion: "",
    source: "",
    turns,
    evener: {
      ref: "older",
      capabilities: {
        send: false,
        steer: false,
        interrupt: false,
        compact: false,
        clear: false,
        forkFromTurn: false,
        shutdown: false,
        changeModel: false,
        queue: false,
        goal: false,
        rename: false,
      },
      queue: { revision: 0 },
    },
  };
  return projectThread(thread).items;
}
