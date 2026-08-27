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
// (triggered by the store-owned drain scheduler, not timers).

import { create } from "zustand";
import { WireError } from "../../../cmd/evener-hub/frontend/src/protocol/errors";
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
import type { ActivityView } from "../services/activity";
import type {
  ConversationService,
  LiveConversationService,
} from "../services/conversation";
import type { ActivityIdentity, NotificationOutcome } from "./activity";

export type ConversationStatus =
  | "idle"
  | "opening"
  | "open"
  | "error"
  | "closed";

// Mutation lifecycle state for send/steer/queue/interrupt. The store tracks
// the kind, pending/failed status, the exact draft snapshot at submission,
// and the conversation generation that initiated it. On failure, the failed
// state PERSISTS until a subsequent mutation or open clears it. The
// mutationId is a monotonically increasing private counter (F4) so
// out-of-order completion cannot change a newer mutation, error, or draft.
export interface ConversationMutationState {
  kind: "send" | "steer" | "queue" | "interrupt";
  status: "pending" | "failed";
  draftSnapshot: string | null;
  generation: number;
  /** @internal — monotonic token for stale-check; not for external consumption. */
  mutationId: number;
}

interface DrainScheduler {
  request(key: string, effect: () => Promise<void>): Promise<void>;
}

// A structural activity sink accepted by openProjected/rehydrate. Uses the
// approved LiveActivityState contract from state/activity.ts directly via an
// exact structural Pick so argument order cannot drift from the real store.
// The applyLiveNotification return value drives the shared drain scheduler.
export interface LiveActivitySink {
  setLiveView(view: ActivityView, identity: ActivityIdentity): boolean;
  applyLiveNotification(
    n: AnyNotification,
    identity: ActivityIdentity,
  ): NotificationOutcome;
  reset(): void;
}

// I1: Binding snapshot captured at request time. Every request through the
// drain scheduler captures the current bindingEpoch + ref + generation +
// service + sink object identities. The effect verifies ALL fields are still
// current BEFORE any read, suppressing stale work at the boundary — a request
// queued for conversation A can never call serviceA with refB after the store
// switched to B. This is an internal exact tuple; it is never exported.
/** @internal — binding snapshot for scheduler stale-work suppression. */
interface RequestBinding {
  readonly epoch: number;
  readonly ref: string;
  readonly generation: number;
  readonly service: LiveConversationService;
  readonly sink: LiveActivitySink;
}

// Production-owned drain scheduler with per-key completion promises. Each
// request returns a promise that resolves when that key's actual effect
// completes/skips/errors — even while unrelated rereads remain or hang. Same
// logical key coalesces (overwrites effect, merges waiter lists); distinct keys
// are preserved so heterogeneous outcomes both run. One effect at a time;
// recursively drains all pending. Catches effect errors without unhandled
// rejections and remains usable.
function createDrainScheduler(): DrainScheduler {
  let scheduled = false;
  let busy = false;
  let inFlight: Promise<void> | null = null;
  // Per-key pending queue. Each entry holds the coalesced effect AND a list of
  // waiters that resolve when this key's effect completes/skips/errors.
  interface PendingEntry {
    effect: () => Promise<void>;
    waiters: Array<() => void>;
  }
  const pending: Map<string, PendingEntry> = new Map();
  // Waiters for the currently in-flight effect (not yet in pending Map).
  let currentWaiters: Array<() => void> = [];

  function runOne(effect: () => Promise<void>): Promise<void> {
    return Promise.resolve()
      .then(effect)
      .catch(() => {
        // Effect errors are the effect's responsibility. Swallow to prevent
        // unhandled rejection; remain usable.
      })
      .then(() => {
        // Resolve this key's waiters — the effect completed/skipped/errored.
        const waiters = currentWaiters;
        currentWaiters = [];
        for (const w of waiters) w();
        if (pending.size > 0) {
          // Drain the next pending effect in insertion order. One at a time.
          const entry = pending.entries().next().value;
          if (entry === undefined) {
            inFlight = null;
            busy = false;
            return undefined;
          }
          const nextKey = entry[0] as string;
          const nextEntry = entry[1] as PendingEntry;
          pending.delete(nextKey);
          currentWaiters = nextEntry.waiters;
          inFlight = runOne(nextEntry.effect);
          return inFlight;
        }
        inFlight = null;
        busy = false;
        return undefined;
      });
  }

  function request(key: string, effect: () => Promise<void>): Promise<void> {
    // Per-key completion promise. Resolves when this key's actual effect
    // completes/skips/errors — even while unrelated rereads remain or hang.
    const waiters: Array<() => void> = [];
    const completion = new Promise<void>((resolve) => {
      waiters.push(resolve);
    });

    if (busy) {
      // An effect is in flight — coalesce same key (merge waiters, overwrite
      // effect); distinct keys are preserved for trailing drain.
      const existing = pending.get(key);
      if (existing !== undefined) {
        existing.effect = effect;
        for (const w of waiters) existing.waiters.push(w);
      } else {
        pending.set(key, { effect, waiters });
      }
      return completion;
    }
    if (scheduled) {
      // A microtask is pending but hasn't started — coalesce same key only.
      const existing = pending.get(key);
      if (existing !== undefined) {
        existing.effect = effect;
        for (const w of waiters) existing.waiters.push(w);
      } else {
        pending.set(key, { effect, waiters });
      }
      return completion;
    }
    // First request in a quiescent drain — defer to a microtask so a
    // synchronous burst coalesces into one effect.
    scheduled = true;
    pending.set(key, { effect, waiters });
    inFlight = Promise.resolve().then(() => {
      scheduled = false;
      busy = true;
      const entry = pending.entries().next().value;
      if (entry === undefined) {
        inFlight = null;
        busy = false;
        return;
      }
      const keyToUse = entry[0] as string;
      const entryValue = entry[1] as PendingEntry;
      pending.delete(keyToUse);
      currentWaiters = entryValue.waiters;
      return runOne(entryValue.effect);
    });
    return completion;
  }

  return {
    request,
  };
}

// F4: No legacy createActivitySink adapter — the activity store's own
// LiveActivityState (Coordinate B) implements LiveActivitySink directly.
// Do not fabricate "applied" — use the real applyLiveNotification outcome.

// --- limits and truncation helpers (centralized) ----------------------------

export const MAX_ITEM_BYTES = 64 * 1024; // 64 KiB in UTF-8 bytes
export const TRUNCATION_MARKER = "… truncated";
export const RETAINED_ITEM_CAP = 500;

// Truncate a string to maxBytes in UTF-8 + marker, ending with "… truncated"
// exactly once. Iterates Unicode scalar values (not UTF-16 code units) so
// no surrogate pairs are split and no U+FFFD replacement chars are produced.
const textEncoder = new TextEncoder();
const markerBytes = textEncoder.encode(TRUNCATION_MARKER);

export function truncateText(text: string, maxBytes: number): string {
  const encoded = textEncoder.encode(text);
  if (encoded.length <= maxBytes) return text;
  const targetBytes = maxBytes - markerBytes.length;
  // Iterate code points (for...of iterates Unicode scalar values) to find
  // the longest prefix whose UTF-8 encoding fits within targetBytes. This
  // avoids splitting surrogate pairs and never produces U+FFFD.
  let byteLen = 0;
  let cutIdx = 0;
  for (const cp of text) {
    const cpBytes = textEncoder.encode(cp).length;
    if (byteLen + cpBytes > targetBytes) break;
    byteLen += cpBytes;
    cutIdx += cp.length;
  }
  // Trim code points until the result + marker fits within maxBytes.
  // (May need to trim if a multibyte code point straddles the boundary.)
  let truncated = text.slice(0, cutIdx);
  let truncatedBytes = textEncoder.encode(truncated);
  while (
    truncatedBytes.length + markerBytes.length > maxBytes &&
    truncated.length > 0
  ) {
    // Remove one code point (may be 2 UTF-16 units for surrogate pairs).
    const codePoints = [...truncated];
    codePoints.pop();
    truncated = codePoints.join("");
    truncatedBytes = textEncoder.encode(truncated);
  }
  return truncated + TRUNCATION_MARKER;
}

// Check if text exceeds the byte limit (for setting truncated flag in projections).
export function exceedsByteLimit(text: string, maxBytes: number): boolean {
  return textEncoder.encode(text).length > maxBytes;
}

// F12: Per-item truncation ownership. Instead of checking if the text ends
// with the marker (which would freeze if genuine content ends with "…
// truncated"), the store tracks which item IDs have been truncated in a
// private set. This allows genuine marker suffixes in content without
// freezing delta appends.

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

// Enforce the 500-item retained cap. Always retains the NEWEST items (end
// of array) so the live tail is preserved for interactive scrolling.
function capItems(items: MobileTimelineItem[]): MobileTimelineItem[] {
  if (items.length <= RETAINED_ITEM_CAP) return items;
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

// Required live state interface (F3): the production store always implements
// these live-only methods. They are NOT optional-fallback to old open().
// Base ConversationState is preserved for screen test mocks that only need
// the basic open/send/steer/queue/interrupt/close surface.
// F2: setCoalescer is removed from the public interface — openProjected
// creates and binds the coalescer internally.
export interface LiveConversationState extends ConversationState {
  openProjected(
    service: LiveConversationService,
    activitySink: LiveActivitySink,
    ref: string,
  ): Promise<void>;
  rehydrate(
    service: LiveConversationService,
    activitySink: LiveActivitySink,
  ): Promise<void>;
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

// Check if an error is a WireError carrying the actionUnavailable
// evenerErrorInfo. F11: uses actual WireError identity (instanceof), not a
// structural property check, to match the canonical error discrimination
// pattern used by isHubLaunchError.
function isActionUnavailableError(err: unknown): boolean {
  return (
    err instanceof WireError && err.evenerErrorInfo === "actionUnavailable"
  );
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
// F6: ask_user items (commandExecution with toolName "ask_user") are NOT
// projected as generic activity — they return null to signal a reread, since
// the canonical projector needs the full turn context (pendingAsks set) to
// project them as question items. F7: precise subtype checks — wrong subtype
// or missing context returns null to schedule a reread.
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
  // F6: ask_user is a commandExecution with toolName "ask_user". The canonical
  // projector handles ask_user with full turn context (pendingAsks, question
  // parsing). We cannot replicate that from a single item notification, so
  // return null to schedule an authoritative reread — never project as generic
  // activity.
  if (item.type === "commandExecution") {
    if (item.toolName === "ask_user") {
      return null; // F6: schedule reread for ask_user
    }
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
  let mutationIdCounter = 0;
  // The store owns ONE drain scheduler for its entire lifetime. Lifecycle,
  // activity rehydrate, structural notification gaps, and mutation capability
  // recovery all request through it rather than owning timers/coalescers.
  const scheduler = createDrainScheduler();
  let activitySink: LiveActivitySink | null = null;
  // The service+sink bound by the last openProjected/rehydrate, used by
  // requestRehydrate so applyNotification can request through the scheduler
  // without holding its own service reference.
  let boundService: LiveConversationService | null = null;
  let boundSink: LiveActivitySink | null = null;
  // I1: Binding epoch — incremented on every openProjected/open/close/reset so
  // a request queued for an older binding (serviceA+refA) can never run after
  // the store switched to a newer binding (serviceB+refB). Every request
  // captures epoch+ref+generation+service+sink; the effect verifies all still
  // current before any read.
  let bindingEpoch = 0;
  // Late-bound store getter — assigned inside create() so requestRehydrate
  // (called from applyNotification) can access get().rehydrate.
  let storeGet: (() => LiveConversationState) | null = null;

  // I1: Capture the current binding snapshot at request time. The effect
  // verifies ALL fields (epoch+ref+generation+service+sink object identities)
  // are still current BEFORE any read — stale work is suppressed at the
  // boundary. Object identity comparison means a rebind with different service
  // or sink objects (even if same ref) produces a different binding and the
  // old effect is suppressed.
  function captureBinding(): RequestBinding | null {
    if (boundService === null || boundSink === null) return null;
    const state = storeGet?.();
    if (state === undefined || state.ref === null) return null;
    return {
      epoch: bindingEpoch,
      ref: state.ref,
      generation: state.conversationGeneration,
      service: boundService,
      sink: boundSink,
    };
  }

  // I1: Verify a captured binding is still current. If the epoch, ref,
  // generation, OR service/sink object identity changed (rebind with
  // different objects), the request is stale and must be suppressed — never
  // call serviceA with refB or sinkA after rebind to B.
  function isBindingCurrent(binding: RequestBinding): boolean {
    const state = storeGet?.();
    if (state === undefined) return false;
    return (
      binding.epoch === bindingEpoch &&
      binding.ref === state.ref &&
      binding.generation === state.conversationGeneration &&
      boundService === binding.service &&
      boundSink === binding.sink
    );
  }

  // I1: Cap-specific binding validation. For openProjected, checks the full
  // tuple (epoch/service/sink/ref/gen). For plain open(), boundService/
  // boundSink are null — checks epoch/ref/gen and service identity only
  // (sink is null for both the binding and the store, so null === null).
  // A rebind to serviceB/sinkB with same gen/mutation suppresses A's caps.
  function isCapBindingCurrent(binding: RequestBinding): boolean {
    const state = storeGet?.();
    if (state === undefined) return false;
    if (binding.epoch !== bindingEpoch) return false;
    if (binding.ref !== state.ref) return false;
    if (binding.generation !== state.conversationGeneration) return false;
    // For openProjected: boundService === binding.service (both set).
    // For plain open: boundService is null, binding.service is the passed
    // service. They won't match — but that's OK because for plain open
    // there's no rebind concern. We check service identity via the passed
    // service object directly.
    if (boundService !== null) {
      // openProjected path — full service identity check.
      if (boundService !== binding.service) return false;
      if (boundSink !== binding.sink) return false;
    }
    return true;
  }

  // Request one authoritative reread through the store-owned drain scheduler.
  // I1: captures the binding at request time; the effect verifies it is still
  // current before calling rehydrate with the expected service+sink.
  function requestRehydrate(ref: string): void {
    const binding = captureBinding();
    const service = boundService;
    const sink = boundSink;
    if (binding === null || service === null || sink === null) return;
    scheduler.request(ref, async () => {
      // I1: Suppress stale work BEFORE any read — if the binding changed,
      // do not call rehydrate (serviceA must never be called with refB).
      if (!isBindingCurrent(binding)) return;
      await storeGet?.().rehydrate(service, sink);
    });
  }

  // I2: Request a mutation capability refresh through the store-owned drain
  // scheduler — no direct await bypass. This serializes/coalesces with rereads.
  // I1: captures the exact binding tuple (epoch/service/sink/ref/gen) at
  // request time. For openProjected, captureBinding() provides the full tuple.
  // For plain open(), boundService/boundSink are null, so we capture a
  // service-specific tuple from the passed service argument. The effect
  // validates isBindingCurrent BEFORE the refresh, AFTER the refresh await,
  // and immediately before publication. A rebind to serviceB/sinkB with same
  // gen/mutation during the await suppresses A's capabilities.
  // I2: The capability key includes mutation identity (cap:ref:mutationId)
  // so distinct mutations do not coalesce with each other or with rereads.
  // handleMutationError schedules the cap refresh through the scheduler AND
  // awaits its per-key completion before surfacing error — an unrelated
  // replenishing reread cannot delay the error.
  function requestCapabilityRefresh(
    service: ConversationService,
    ref: string,
    gen: number,
    mutationId: number,
  ): Promise<void> {
    // I1: Capture the binding tuple. For openProjected, captureBinding()
    // provides epoch+ref+gen+service+sink. For plain open(), captureBinding()
    // returns null (boundService/boundSink are null), so capture a
    // service-specific tuple from the passed service.
    const projectedBinding = captureBinding();
    const binding: RequestBinding = projectedBinding ?? {
      epoch: bindingEpoch,
      ref,
      generation: gen,
      service: service as LiveConversationService,
      sink: boundSink as LiveActivitySink,
    };
    const capKey = `cap:${ref}:${mutationId}`;
    // I2: scheduler.request returns a per-key completion promise — resolves
    // when this key's effect completes/skips/errors, even while unrelated
    // rereads remain or hang. handleMutationError awaits this exact promise.
    return scheduler.request(capKey, async () => {
      const g = storeGet;
      if (g === null) return;
      // I1: Suppress stale work BEFORE any read — validate the full binding
      // tuple (epoch/service/sink/ref/gen). A rebind with different objects
      // suppresses this effect.
      if (!isCapBindingCurrent(binding)) return;
      const liveService = service as LiveConversationService;
      if (typeof liveService.refreshCapabilities !== "function") return;
      // I2: Check active mutation before the refresh — stale recovery bail.
      if (g().pendingMutation?.mutationId !== mutationId) return;
      let refreshed: ThreadCapabilities | null;
      try {
        refreshed = await liveService.refreshCapabilities(ref);
      } catch {
        // If refresh fails, the mutation error handler surfaces the error.
        return;
      }
      // I1: Validate the full binding tuple AGAIN after the await — a rebind
      // to serviceB/sinkB during the refresh must suppress A's capabilities.
      if (!isCapBindingCurrent(binding)) return;
      // I2: Check active mutation AFTER the refresh too.
      if (g().pendingMutation?.mutationId !== mutationId) return;
      // I1: Validate generation immediately before publication.
      if (g().conversationGeneration === gen && refreshed !== null) {
        const currentConv = g().conversation;
        if (currentConv !== null) {
          storeSet?.({
            conversation: {
              ...currentConv,
              capabilities: { ...refreshed },
            },
          });
        }
      }
    });
  }

  // Late-bound store setter — assigned inside create() so capability refresh
  // effects can call set() outside the create() callback scope.
  let storeSet: ((partial: Partial<ConversationState>) => void) | null = null;
  // F9: Rehydrate operation token — incremented on each rehydrate call so
  // a stale rehydrate (from an older operation) cannot overwrite a newer
  // rehydrate's state within the same generation.
  let rehydrateToken = 0;
  // F12: Per-item truncation ownership — tracks which item IDs have been
  // truncated to their byte limit. Once an item is truncated, later deltas
  // cannot append (the marker appears exactly once). This is tracked by
  // item ID, not by checking the text suffix, so genuine content that
  // happens to end with "… truncated" does not freeze delta appends.
  const truncatedItemIds = new Set<string>();

  // Truncate items and record which item IDs were truncated (F12).
  // Called from open/openProjected/rehydrate to seed the truncation set.
  function truncateAndRecord(
    items: MobileTimelineItem[],
  ): MobileTimelineItem[] {
    return items.map((item) => {
      // Check if any text-bearing field exceeds the byte limit.
      let needsTruncation = false;
      if (item.kind === "assistant") {
        needsTruncation = exceedsByteLimit(item.markdown, MAX_ITEM_BYTES);
      } else if (item.kind === "activity") {
        needsTruncation =
          (item.detail.arguments !== undefined &&
            exceedsByteLimit(item.detail.arguments, MAX_ITEM_BYTES)) ||
          (item.detail.output !== undefined &&
            exceedsByteLimit(item.detail.output, MAX_ITEM_BYTES)) ||
          (item.detail.error !== undefined &&
            exceedsByteLimit(item.detail.error, MAX_ITEM_BYTES));
      }
      if (needsTruncation) {
        truncatedItemIds.add(item.id);
      }
      return truncateItem(item);
    });
  }

  return create<LiveConversationState>((set, get) => {
    storeGet = get;
    storeSet = set;
    return {
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
        // I1: plain open CANNOT retain projected bindings — clear them and
        // increment the binding epoch so any queued projected rehydrate/cap
        // refresh is suppressed at the boundary.
        bindingEpoch += 1;
        boundService = null;
        boundSink = null;
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
              items: capItems(truncateAndRecord(conv.items)),
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

      async openProjected(service, sink, ref) {
        const gen = ++conversationGen;
        // I1: increment the binding epoch and bind service+sink so queued
        // requests from an older binding are suppressed at the boundary.
        bindingEpoch += 1;
        activitySink = sink;
        boundService = service;
        boundSink = sink;
        // Reset thread-scoped state (draft, pending mutation) — presentation state
        // now lives outside the store (in live-ui-store).
        // F4: reset the activity sink on thread change.
        sink.reset();
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
          const identity: ActivityIdentity = {
            threadId: conversation.id,
            ref,
            generation: gen,
          };
          // Strict activity sink: call setLiveView BEFORE committing the paired
          // conversation projection. If it returns false (stale/invalid identity),
          // do not commit the conversation projection — the two stores stay
          // atomically consistent under the same exact identity tuple.
          const accepted = sink.setLiveView(activity, identity);
          if (!accepted) return;
          set({
            conversation: {
              ...conversation,
              items: capItems(truncateAndRecord(conversation.items)),
            },
            status: "open",
            olderCursor,
          });
          service.subscribeNotifications((n) => {
            if (gen !== conversationGen) return;
            // Route notifications to BOTH stores — conversation and activity.
            // Propagate every returned outcome; rehydrate requests the one shared
            // drain scheduler. Notification-first, identity-second.
            const outcome = sink.applyLiveNotification(n, identity);
            if (outcome === "rehydrate") {
              requestRehydrate(ref);
            }
            // Also process the conversation store's own notification handler.
            // Only call the conversation's applyNotification if the activity
            // sink did not say "ignored" (ignored means the activity store
            // rejected it on identity grounds — but conversation still needs
            // its own processing for conversation-specific notifications).
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

      async rehydrate(service, sink) {
        // Rehydrate uses readProjection to refresh the conversation without
        // calling destructive open(). Preserves draft.
        // F3: rehydrate triggers the same shared reread.
        // F9: Operation token prevents stale rehydrate from overwriting
        // newer state within the same generation.
        // I1: rehydrate accepts the expected binding (ref+gen from entry state)
        // rather than resnapshotting current state mid-flight. It can never
        // call serviceA with refB — the scheduler effect verifies the binding
        // epoch before calling this, and rehydrate itself re-checks.
        // I1: Any rehydrate(service, sink) with different objects than the
        // current binding increments bindingEpoch BEFORE assignment, so queued
        // effects captured with the old service/sink are suppressed.
        const state = get();
        if (state.ref === null) return;
        const ref = state.ref;
        const currentDraft = state.draft;
        const gen = state.conversationGeneration;
        // I1: If the service or sink objects differ from the current binding,
        // increment bindingEpoch BEFORE assignment so queued effects captured
        // with the old service/sink are suppressed.
        if (service !== boundService || sink !== boundSink) {
          bindingEpoch += 1;
        }
        // I1: Capture the binding epoch at entry — if it changed during the
        // await (open/close/reset/openProjected/openProjected/rehydrate), this
        // rehydrate is stale.
        const entryEpoch = bindingEpoch;
        const token = ++rehydrateToken;
        activitySink = sink;
        boundService = service;
        boundSink = sink;
        try {
          const { conversation, activity, olderCursor } =
            await service.readProjection(ref);
          // I1: Suppress stale work — if the binding epoch changed, the store
          // switched to a different service/sink/ref. Do not commit.
          if (entryEpoch !== bindingEpoch) return;
          // Guard: a newer generation may have opened during the await.
          if (get().conversationGeneration !== gen) {
            return;
          }
          // F9: Stale rehydrate — a newer rehydrate started in the same gen.
          if (token !== rehydrateToken) {
            return;
          }
          const identity: ActivityIdentity = {
            threadId: conversation.id,
            ref,
            generation: gen,
          };
          // Strict activity sink: call setLiveView BEFORE committing the paired
          // conversation projection. If it returns false, do not commit.
          const accepted = sink.setLiveView(activity, identity);
          if (!accepted) return;
          set({
            conversation: {
              ...conversation,
              items: capItems(truncateAndRecord(conversation.items)),
            },
            olderCursor,
            // Preserve draft
            draft: currentDraft,
            error: null,
          });
        } catch (err) {
          // Stale safety: only set error if the binding epoch, generation, and
          // operation token are all still current.
          if (
            entryEpoch === bindingEpoch &&
            get().conversationGeneration === gen &&
            token === rehydrateToken
          ) {
            set({
              error: err instanceof Error ? err.message : String(err),
            });
          }
        }
      },

      async loadOlder(service) {
        const state = get();
        if (state.loadingOlder || state.conversation === null) return;
        // F8: Never request with a null/empty cursor — no more older pages.
        if (state.olderCursor === null) return;
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
            // F10: Dedupe by source item identity — items from older pages
            // that already exist in the current conversation (same id) are
            // dropped, keeping the newer (live tail) version.
            const existingIds = new Set(currentConv.items.map((i) => i.id));
            const deduped = result.items.filter((i) => !existingIds.has(i.id));
            // Prepend older (deduped) items, then trim from the oldest (front)
            // so the newest live tail is retained (finding 8).
            const merged = capItems([
              ...deduped.map(truncateItem),
              ...currentConv.items,
            ]);
            // F8: If we're at the cap and the merge trimmed older items,
            // disable further paging honestly — set cursor to null so
            // we don't repeatedly load rows that will be discarded.
            const atCap = merged.length >= RETAINED_ITEM_CAP;
            const nextCursor =
              atCap && deduped.length < result.items.length
                ? null // Some items were deduped — cap prevents useful paging
                : atCap
                  ? null // At cap — further paging would just discard rows
                  : (result.nextCursor ?? null);
            set({
              conversation: { ...currentConv, items: merged },
              olderCursor: nextCursor,
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
        const mutationId = ++mutationIdCounter;
        const mutation: ConversationMutationState = {
          kind: "send",
          status: "pending",
          draftSnapshot: draftText,
          generation: gen,
          mutationId,
        };
        set({ draft: "", pendingSend: "pending", pendingMutation: mutation });
        try {
          await service.send(input);
          // F4: Check mutationId — out-of-order completion cannot clear a
          // newer mutation's state.
          if (get().pendingMutation?.mutationId === mutationId) {
            set({ pendingSend: null, pendingMutation: null, error: null });
          }
        } catch (err) {
          await handleMutationError(
            err,
            service,
            state.ref,
            gen,
            mutationId,
            mutation,
            draftText,
            set,
            get,
            requestCapabilityRefresh,
          );
        }
      },

      async steer(service, input) {
        const state = get();
        if (state.conversation === null) return;
        requireCap(state.conversation, "steer", "steer");
        const draftText = state.draft;
        const gen = state.conversationGeneration;
        const mutationId = ++mutationIdCounter;
        const mutation: ConversationMutationState = {
          kind: "steer",
          status: "pending",
          draftSnapshot: draftText,
          generation: gen,
          mutationId,
        };
        // Steer/queue clear the draft on submit like send.
        // F10: any new mutation clears legacy pendingSend.
        set({ draft: "", pendingSend: null, pendingMutation: mutation });
        try {
          await service.steer(input);
          if (get().pendingMutation?.mutationId === mutationId) {
            set({ pendingMutation: null, error: null });
          }
        } catch (err) {
          await handleMutationError(
            err,
            service,
            state.ref,
            gen,
            mutationId,
            mutation,
            draftText,
            set,
            get,
            requestCapabilityRefresh,
          );
        }
      },

      async queue(service, input) {
        const state = get();
        if (state.conversation === null) return;
        requireCap(state.conversation, "queue", "queue");
        const draftText = state.draft;
        const gen = state.conversationGeneration;
        const mutationId = ++mutationIdCounter;
        const mutation: ConversationMutationState = {
          kind: "queue",
          status: "pending",
          draftSnapshot: draftText,
          generation: gen,
          mutationId,
        };
        // F10: any new mutation clears legacy pendingSend.
        set({ draft: "", pendingSend: null, pendingMutation: mutation });
        try {
          await service.queue(input);
          if (get().pendingMutation?.mutationId === mutationId) {
            set({ pendingMutation: null, error: null });
          }
        } catch (err) {
          await handleMutationError(
            err,
            service,
            state.ref,
            gen,
            mutationId,
            mutation,
            draftText,
            set,
            get,
            requestCapabilityRefresh,
          );
        }
      },

      async interrupt(service) {
        const state = get();
        if (state.conversation === null) return;
        requireCap(state.conversation, "interrupt", "interrupt");
        const gen = state.conversationGeneration;
        const mutationId = ++mutationIdCounter;
        const mutation: ConversationMutationState = {
          kind: "interrupt",
          status: "pending",
          // Interrupt does NOT snapshot the draft — it should remain as-is.
          draftSnapshot: null,
          generation: gen,
          mutationId,
        };
        // Interrupt does NOT clear the draft.
        // F10: any new mutation clears legacy pendingSend.
        set({ pendingSend: null, pendingMutation: mutation });
        try {
          await service.interrupt();
          if (get().pendingMutation?.mutationId === mutationId) {
            set({ pendingMutation: null, error: null });
          }
        } catch (err) {
          await handleMutationError(
            err,
            service,
            state.ref,
            gen,
            mutationId,
            mutation,
            null,
            set,
            get,
            requestCapabilityRefresh,
          );
        }
      },

      close() {
        // Increment generation so late frames from the closed conversation
        // cannot repopulate the store.
        ++conversationGen;
        // I1: increment the binding epoch and clear bindings so queued
        // requests from the closed conversation are suppressed.
        bindingEpoch += 1;
        truncatedItemIds.clear();
        // F4: reset the activity sink on close.
        if (activitySink !== null) {
          activitySink.reset();
          activitySink = null;
        }
        boundService = null;
        boundSink = null;
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
              conversation: {
                ...conv,
                reasoningEffort: params.reasoningEffort,
              },
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
              if (state.ref !== null) {
                requestRehydrate(state.ref);
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
              if (state.ref !== null) {
                requestRehydrate(state.ref);
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
              // F12: Per-item truncation ownership — once an item is
              // truncated, later deltas cannot append. Tracked by item ID,
              // not by text suffix, so genuine content ending with the
              // marker doesn't freeze.
              if (truncatedItemIds.has(params.itemId)) {
                break;
              }
              const combined =
                (existing.kind === "assistant" ? existing.markdown : "") +
                params.delta;
              const truncated = truncateText(combined, MAX_ITEM_BYTES);
              if (truncated !== combined) {
                truncatedItemIds.add(params.itemId);
              }
              set({
                conversation: {
                  ...conv,
                  items: conv.items.map((item) =>
                    item.kind === "assistant" && item.id === params.itemId
                      ? { ...item, markdown: truncated }
                      : item,
                  ),
                },
              });
            } else {
              // Delta targeting missing item — trigger resync.
              if (state.ref !== null) {
                requestRehydrate(state.ref);
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
              if (state.ref !== null) {
                requestRehydrate(state.ref);
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
              // F12: Per-item truncation ownership.
              if (truncatedItemIds.has(params.itemId)) {
                break;
              }
              const combined =
                (existing.kind === "activity"
                  ? (existing.detail.output ?? "")
                  : "") + params.delta;
              const truncated = truncateText(combined, MAX_ITEM_BYTES);
              if (truncated !== combined) {
                truncatedItemIds.add(params.itemId);
              }
              set({
                conversation: {
                  ...conv,
                  items: conv.items.map((item) =>
                    item.kind === "activity" && item.id === params.itemId
                      ? {
                          ...item,
                          detail: { ...item.detail, output: truncated },
                        }
                      : item,
                  ),
                },
              });
            } else {
              // Delta targeting missing or wrong-kind item — trigger resync.
              if (state.ref !== null) {
                requestRehydrate(state.ref);
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
              // F12: Per-item truncation ownership.
              if (truncatedItemIds.has(params.itemId)) {
                break;
              }
              const combined =
                (existing.kind === "activity"
                  ? (existing.detail.output ?? "")
                  : "") + params.delta;
              const truncated = truncateText(combined, MAX_ITEM_BYTES);
              if (truncated !== combined) {
                truncatedItemIds.add(params.itemId);
              }
              set({
                conversation: {
                  ...conv,
                  items: conv.items.map((item) =>
                    item.kind === "activity" && item.id === params.itemId
                      ? {
                          ...item,
                          detail: { ...item.detail, output: truncated },
                        }
                      : item,
                  ),
                },
              });
            } else {
              if (state.ref !== null) {
                requestRehydrate(state.ref);
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

          // evener/thread/resync triggers a coalesced rehydrate via the store-owned
          // drain scheduler. The store does not re-read on its own.
          case "evener/thread/resync": {
            if (state.ref !== null) {
              requestRehydrate(state.ref);
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
              if (state.ref !== null) {
                requestRehydrate(state.ref);
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
        // I1: increment the binding epoch and clear bindings so queued
        // requests from the prior conversation are suppressed.
        bindingEpoch += 1;
        truncatedItemIds.clear();
        // F4: reset the activity sink on thread change.
        if (activitySink !== null) {
          activitySink.reset();
          activitySink = null;
        }
        boundService = null;
        boundSink = null;
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
    };
  });
}

// Shared mutation error handler: on actionUnavailable, requests the
// non-subscribing refreshCapabilities (never open()) through the store-owned
// drain scheduler (I2 — no direct await bypass) to publish refreshed caps
// before surfacing the error. On failure, the failed mutation state PERSISTS
// (not cleared to null). The draft is only restored if no new text was typed
// during the in-flight mutation. F4/F10: uses mutationId (not generation alone)
// so out-of-order failure cannot overwrite a newer mutation's error.
// F10: checks active mutation before the capability refresh — a newer
// mutation may have started.
// I2: the capability refresh is requested through the store-owned scheduler
// so it serializes/coalesces with rereads; stale mutation recovery is
// suppressed by the mutationId guard inside the scheduler effect.
async function handleMutationError(
  err: unknown,
  service: ConversationService,
  ref: string | null,
  gen: number,
  mutationId: number,
  mutation: ConversationMutationState,
  draftSnapshot: string | null,
  set: (partial: Partial<ConversationState>) => void,
  get: () => ConversationState,
  requestCapabilityRefresh: (
    service: ConversationService,
    ref: string,
    gen: number,
    mutationId: number,
  ) => Promise<void>,
): Promise<void> {
  // F10: Check active mutation BEFORE the capability refresh. If a newer
  // mutation has already started, this error is stale — bail out.
  if (get().pendingMutation?.mutationId !== mutationId) return;

  // I2: on actionUnavailable, schedule the capability refresh through the
  // store-owned drain scheduler AND await its completion before publishing
  // capabilities/surfacing error. This preserves prior observable ordering and
  // mutation guards — caps are published before the error is visible.
  if (isActionUnavailableError(err) && ref !== null) {
    await requestCapabilityRefresh(service, ref, gen, mutationId);
  }

  // F10: Re-check mutationId — out-of-order failure cannot change a newer
  // mutation, error, or draft. The capability refresh has completed (awaited);
  // now surface the error.
  if (get().pendingMutation?.mutationId === mutationId) {
    // The failed mutation state PERSISTS — do NOT clear pendingMutation.
    const currentDraft = get().draft;
    // F10: Draft revision prevents type-delete restore — only restore if
    // the draft is still empty (no new text was typed during the mutation).
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
