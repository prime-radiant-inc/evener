// Durable-intent discovery: the half of a mutation outbox that is the same on
// every host.
//
// A submission is written to storage before it is dispatched, so the record —
// not the call stack — is what survives a reload, a lost connection or a
// closed app. Something then has to notice the records that are waiting and
// tell the consumer to dispatch them. That noticing is this class: it enqueues
// through a storage port, announces the commit to sibling clients, and
// re-scans on the moments a host can say "now would be a good time"
// (connection ready, the app came back, a timer).
//
// What it deliberately does NOT do: settle, reclassify or retry anything.
// Authoritative RPC outcomes are the only callers allowed to change a record's
// state; discovery only announces records the storage already holds.
//
// Every host-shaped capability is an option: the storage itself
// (MutationOutboxStorage — IndexedDB on the web, whatever native brings), the
// sibling-client channel, the lifecycle events, and the timer. None of them
// default to a browser global, so this module names no DOM type and a host
// that has no siblings or no lifecycle passes nothing and gets nothing.
//
// Native has no outbox and no durable record of its own yet. Its
// ConversationMutationState (mobile/src/state/conversation.ts) is a different
// thing entirely — UI ownership bookkeeping for the one mutation in flight:
// which action it was, whether it is pending or failed, the draft to restore,
// and two monotonic tokens the store uses to ignore stale completions. It
// carries no clientMutationId, no targetRef, no payload, no sequence and no
// persisted state, so nothing here aliases it; D25d is where native gains
// records and this class is what notices them.
import type {
  MutationAttachmentRef,
  MutationIntent,
  MutationOptimisticRecord,
  MutationOutboxRecord,
  MutationOutboxState,
  MutationRecoveryKind,
  MutationRecoveryRecord,
} from "./records";

// Why discovery ran, carried through to the consumer so a scan can be
// explained (and, in tests, asserted) rather than guessed at.
export type MutationDiscoveryReason =
  | "startup"
  | "enqueue"
  | "broadcast"
  | "ready"
  | "online"
  | "focus"
  | "visibility"
  | "interval";

// The storage this layer needs, as an interface rather than a class: the web's
// MutationOutboxIndexedDB implements it and stays where it is (it is IndexedDB
// through and through), and another host implements the same 13 calls over
// whatever it has. Two of them are this class's own (enqueueIntent,
// listTargetRefs); the rest are what the dispatcher calls, declared here so
// one port describes the contract rather than two halves of it.
//
// Every method is async because durable storage is: a host with a synchronous
// store returns resolved promises.
export interface MutationOutboxStorage<A extends MutationAttachmentRef = MutationAttachmentRef> {
  // --- used by MutationOutbox ---
  enqueueIntent(intent: MutationIntent<A>): Promise<MutationOutboxRecord<A>>;
  // Every ref with a record still waiting, for a full scan.
  listTargetRefs(): Promise<string[]>;
  // --- used by the dispatcher ---
  getOutbox(clientMutationId: string): Promise<MutationOutboxRecord<A> | undefined>;
  getOptimistic(clientMutationId: string): Promise<MutationOptimisticRecord<A> | undefined>;
  listOptimistic(targetRef?: string): Promise<MutationOptimisticRecord<A>[]>;
  getRecovery(clientMutationId: string): Promise<MutationRecoveryRecord<A> | undefined>;
  // The next record for one ref that is still waiting to be dispatched.
  nextDispatchable(targetRef: string): Promise<MutationOutboxRecord<A> | undefined>;
  markAttempted(clientMutationId: string): Promise<boolean>;
  markUnknown(
    clientMutationId: string,
    state: MutationOutboxState,
    options?: { onlyAttempted: boolean },
  ): Promise<boolean>;
  settleReceipt(clientMutationId: string, projectionState: string): Promise<boolean>;
  settleApplied(clientMutationId: string): Promise<boolean>;
  // Records the authoritative snapshot proves never landed, restored for
  // another dispatch attempt; returns the ids restored.
  restoreProvenAbsent(targetRef: string, authoritativeIds: ReadonlySet<string>): Promise<string[]>;
  transferToRecovery(
    clientMutationId: string,
    recoveryKind: MutationRecoveryKind,
    recoveryReason?: string,
  ): Promise<MutationRecoveryRecord<A> | undefined>;
}

// A channel to sibling clients of the same storage: one browser tab telling
// the others a record just landed. Structural, so a BroadcastChannel satisfies
// it and a host with no siblings passes nothing.
export interface MutationOutboxChannel {
  postMessage(message: unknown): void;
  close(): void;
  // The listener takes `unknown` so a real EventTarget (a BroadcastChannel, or
  // a test double extending EventTarget) satisfies this without the DOM's
  // Event type reaching this module; the handler narrows what it needs.
  addEventListener(type: string, listener: (event: unknown) => void): void;
  removeEventListener(type: string, listener: (event: unknown) => void): void;
}

// The "the app might have missed something" events: a browser window's online
// and focus, a phone's foreground. A host passes what it has.
export interface MutationLifecycleTarget {
  addEventListener(type: string, listener: () => void): void;
  removeEventListener(type: string, listener: () => void): void;
}

// The same, plus the readable flag a visibility event carries.
export interface MutationVisibilityTarget extends MutationLifecycleTarget {
  readonly visibilityState: string;
}

export interface MutationOutboxOptions {
  // Whether dispatching is possible at all right now (a live connection, this
  // runtime still current). Discovery announces nothing while it is false.
  isReady: () => boolean;
  onDiscover: (targetRefs: string[], reason: MutationDiscoveryReason) => void | Promise<void>;
  createBroadcastChannel?: (name: string) => MutationOutboxChannel;
  lifecycleWindow?: MutationLifecycleTarget;
  lifecycleDocument?: MutationVisibilityTarget;
  // A timer is a PAIR: scheduling what cannot be cancelled would keep scanning
  // an outbox that has stopped. Pass both or neither — one alone is no timer.
  setInterval?: (callback: () => void, milliseconds: number) => number;
  clearInterval?: (intervalId: number) => void;
  // How often a host with a timer re-scans. The web's 2s default is the
  // interval its oracle pins; a host that passes no timer never uses it.
  scanIntervalMs?: number;
}

const CHANNEL_NAME = "evener-mutation-outbox-v1";
const DEFAULT_SCAN_INTERVAL_MS = 2000;

interface MutationOutboxWakeup {
  version: 1;
  targetRef: string;
}

function isMutationOutboxWakeup(value: unknown): value is MutationOutboxWakeup {
  if (typeof value !== "object" || value === null) return false;
  const message = value as Partial<MutationOutboxWakeup>;
  return message.version === 1 && typeof message.targetRef === "string" && message.targetRef.length > 0;
}

// MutationOutbox owns durable-intent discovery. Its interval and lifecycle
// hooks only announce records already stored by the adapter; authoritative RPC
// outcomes are the only callers allowed to settle or reclassify them.
export class MutationOutbox<A extends MutationAttachmentRef = MutationAttachmentRef> {
  readonly #storage: MutationOutboxStorage<A>;
  readonly #isReady: () => boolean;
  readonly #onDiscover: MutationOutboxOptions["onDiscover"];
  readonly #createChannel: ((name: string) => MutationOutboxChannel) | undefined;
  readonly #lifecycleTarget: MutationLifecycleTarget | undefined;
  readonly #visibilityTarget: MutationVisibilityTarget | undefined;
  readonly #setInterval: ((callback: () => void, milliseconds: number) => number) | undefined;
  readonly #clearInterval: ((intervalId: number) => void) | undefined;
  readonly #scanIntervalMs: number;
  #channel: MutationOutboxChannel | undefined;
  #intervalId: number | undefined;
  #started = false;
  #pendingDiscovery: Promise<void> = Promise.resolve();
  readonly #scheduledReadyScans = new Set<MutationDiscoveryReason>();

  readonly #handleBroadcast = (event: unknown) => {
    if (!this.#isReady()) return;
    const message = (event as { data?: unknown } | null)?.data;
    if (!isMutationOutboxWakeup(message)) return;
    this.#scheduleDiscovery([message.targetRef], "broadcast");
  };

  readonly #handleOnline = () => {
    this.#scheduleReadyScan("online");
  };

  readonly #handleFocus = () => {
    this.#scheduleReadyScan("focus");
  };

  readonly #handleVisibility = () => {
    if (this.#visibilityTarget?.visibilityState === "visible") this.#scheduleReadyScan("visibility");
  };

  constructor(storage: MutationOutboxStorage<A>, options: MutationOutboxOptions) {
    this.#storage = storage;
    this.#isReady = options.isReady;
    this.#onDiscover = options.onDiscover;
    this.#createChannel = options.createBroadcastChannel;
    this.#lifecycleTarget = options.lifecycleWindow;
    this.#visibilityTarget = options.lifecycleDocument;
    const cancellableTimer = options.setInterval !== undefined && options.clearInterval !== undefined;
    this.#setInterval = cancellableTimer ? options.setInterval : undefined;
    this.#clearInterval = cancellableTimer ? options.clearInterval : undefined;
    this.#scanIntervalMs = options.scanIntervalMs ?? DEFAULT_SCAN_INTERVAL_MS;
  }

  async start(): Promise<void> {
    if (this.#started) return;
    this.#started = true;
    this.#channel = this.#createChannel?.(CHANNEL_NAME);
    this.#channel?.addEventListener("message", this.#handleBroadcast);
    this.#lifecycleTarget?.addEventListener("online", this.#handleOnline);
    this.#lifecycleTarget?.addEventListener("focus", this.#handleFocus);
    this.#visibilityTarget?.addEventListener("visibilitychange", this.#handleVisibility);
    this.#intervalId = this.#setInterval?.(() => this.#scheduleReadyScan("interval"), this.#scanIntervalMs);
    // Submissions need the runtime's listeners, not a scan of earlier work.
    // Queue startup discovery so a stalled read cannot delay their own commit.
    this.#schedule(() => this.#discoverAll("startup"));
  }

  async stop(): Promise<void> {
    if (!this.#started) return this.#pendingDiscovery;
    this.#started = false;
    this.#channel?.removeEventListener("message", this.#handleBroadcast);
    this.#channel?.close();
    this.#channel = undefined;
    this.#lifecycleTarget?.removeEventListener("online", this.#handleOnline);
    this.#lifecycleTarget?.removeEventListener("focus", this.#handleFocus);
    this.#visibilityTarget?.removeEventListener("visibilitychange", this.#handleVisibility);
    if (this.#intervalId !== undefined) this.#clearInterval?.(this.#intervalId);
    this.#intervalId = undefined;
    await this.#pendingDiscovery;
  }

  async enqueueIntent(
    intent: MutationIntent<A>,
    onCommitted?: (record: MutationOutboxRecord<A>) => void,
  ): Promise<MutationOutboxRecord<A>> {
    const record = await this.#storage.enqueueIntent(intent);
    onCommitted?.(record);
    try {
      this.#channel?.postMessage({
        version: 1,
        targetRef: record.targetRef,
      } satisfies MutationOutboxWakeup);
    } catch {
      // The commit owns the message. Lifecycle scans also discover it if a
      // closing client cannot broadcast; that cannot turn acceptance into
      // failure.
    }
    if (this.#isReady()) this.#scheduleDiscovery([record.targetRef], "enqueue");
    return record;
  }

  async connectionReady(): Promise<void> {
    if (!this.#isReady()) return;
    try {
      await this.#queueDiscovery(() => this.#discoverAll("ready"));
    } catch {
      // Readiness is a lifecycle notification, not a submission result.
      // Durable work remains available for the next discovery scan.
    }
  }

  #scheduleReadyScan(reason: MutationDiscoveryReason): void {
    // A host's timer callback can already be queued when clearInterval lands, and
    // a lifecycle event can arrive during shutdown: a stopped outbox scans
    // nothing. An enqueue is deliberately not gated — its record is durable and
    // announcing it costs nothing.
    if (!this.#started || !this.#isReady() || this.#scheduledReadyScans.has(reason)) return;
    this.#scheduledReadyScans.add(reason);
    this.#schedule(async () => {
      try {
        await this.#discoverAll(reason);
      } finally {
        this.#scheduledReadyScans.delete(reason);
      }
    });
  }

  #scheduleDiscovery(targetRefs: string[], reason: MutationDiscoveryReason): void {
    this.#schedule(() => this.#discover(targetRefs, reason));
  }

  #schedule(discovery: () => Promise<void>): void {
    void this.#queueDiscovery(discovery).catch(() => {
      // Durable work remains in storage and the next lifecycle scan retries discovery.
    });
  }

  #queueDiscovery(discovery: () => Promise<void>): Promise<void> {
    const current = this.#pendingDiscovery.then(discovery);
    this.#pendingDiscovery = current.catch(() => undefined);
    return current;
  }

  async #discoverAll(reason: MutationDiscoveryReason): Promise<void> {
    await this.#discover(await this.#storage.listTargetRefs(), reason);
  }

  async #discover(targetRefs: string[], reason: MutationDiscoveryReason): Promise<void> {
    // The consumer may still own failed reconciliation after its last durable record settled.
    await this.#onDiscover(targetRefs, reason);
  }
}
