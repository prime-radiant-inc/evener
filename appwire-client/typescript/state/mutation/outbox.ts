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
import type { AppwireClientLike } from "../../clientLike";
import type {
  MutationAttachmentRef,
  MutationIntent,
  MutationOptimisticRecord,
  MutationOutboxRecord,
  MutationRecoveryKind,
  MutationRecoveryRecord,
} from "./records";
import { tryOrUndefined } from "./secureUUID";

// The one readiness notion this subpath owns, shared by the outbox's discovery
// gate and the dispatcher's per-ref loop. A host answers for a target ref, or
// ref-less for "the runtime's current client" — the same call it wires into
// both halves, so the two can no longer be derived from facts that drift.
export type MutationClientLookup = (targetRef?: string) => AppwireClientLike | null | undefined;

// What "ready" means for both: a live client whose state is "ready". A
// missing client (no connection, a retired runtime) is never ready.
export function isClientReady(client: AppwireClientLike | null | undefined): client is AppwireClientLike {
  return client?.state === "ready";
}

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

// The click-time half of a host's durable stop barrier: the ref's durable
// stop epoch as the submitting click observed it. A host whose Stop cancels
// rows durably bumps that epoch in the same transaction, and passes this
// capture to the enqueue so a Stop landing while the submission was in
// flight - after the click, before the commit - makes the record commit
// already canceled instead of live. The comparison is commit-order, not
// click-order: a Stop whose durable write lands after a later send's capture
// still cancels that send, which is the conservative, retryable direction.
export interface MutationStopBarrier {
  stopEpoch: number;
}

// The storage this layer needs, as an interface rather than a class: the web's
// MutationOutboxIndexedDB implements it and stays where it is (it is IndexedDB
// through and through), and another host implements the same 14 calls over
// whatever it has. Three of them are this class's own (enqueueIntent,
// enqueueInterruptAndCancel, listTargetRefs); the rest are what the dispatcher
// calls, declared here so one port describes the contract rather than two
// halves of it.
//
// Every method is async because durable storage is: a host with a synchronous
// store returns resolved promises.
export interface MutationOutboxStorage<A extends MutationAttachmentRef = MutationAttachmentRef> {
  // --- used by MutationOutbox ---
  // The optional barrier is the host's click-time stop-epoch capture (see
  // MutationStopBarrier): a storage that honors it commits the record
  // canceled when a Stop landed between the capture and this transaction.
  //
  // The generated clientMutationId must be unique across ALL THREE active
  // stores (outbox, optimistic, recovery), not merely within the outbox: a
  // record that has already moved on to optimistic or recovery still owns its
  // id, and a second active record holding it would let a settlement keyed on
  // the id overwrite or discard that older one. An enqueue that collides with
  // any of the three rejects in the same transaction - rolling the record's
  // sequence allocation back with it - and never silently regenerates the id
  // unless a host deliberately chooses that policy.
  enqueueIntent(intent: MutationIntent<A>, barrier?: MutationStopBarrier): Promise<MutationOutboxRecord<A>>;
  // Stop's combined durable write: cancel the ref's non-attempted rows and
  // enqueue the interrupt record in one transaction — both or neither.
  // The generated id obeys enqueueIntent's cross-store uniqueness invariant.
  enqueueInterruptAndCancel(intent: MutationIntent<A>): Promise<MutationOutboxRecord<A>>;
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
  // The state an uncertain-outcome write may move a record to is exactly
  // "blockedUnknown" - the literal type is deliberate, so the type system
  // rejects asking this method for any other state. "canceled" especially is
  // the user's durable decision (only an explicit user Retry releases it), and
  // "submitting" is the settle/reopen paths' verdict, never this one's.
  markUnknown(
    clientMutationId: string,
    state: "blockedUnknown",
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
  // The same client lookup the dispatcher takes. Asked ref-less, it answers
  // with the runtime's current client, and discovery announces nothing unless
  // that client is ready (a live connection, this runtime still current).
  getClient: MutationClientLookup;
  onDiscover: (targetRefs: string[], reason: MutationDiscoveryReason) => void | Promise<void>;
  createBroadcastChannel?: (name: string) => MutationOutboxChannel;
  lifecycleWindow?: MutationLifecycleTarget;
  lifecycleDocument?: MutationVisibilityTarget;
  // A timer is a PAIR: scheduling what cannot be cancelled would keep scanning
  // an outbox that has stopped. Pass both or neither — one alone is no timer.
  setInterval?: (callback: () => void, milliseconds: number) => number;
  clearInterval?: (intervalId: number) => void;
}

const CHANNEL_NAME = "evener-mutation-outbox-v1";
// How often a host with a timer re-scans — the interval the web's oracle pins.
const SCAN_INTERVAL_MS = 2000;

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
  readonly #getClient: MutationClientLookup;
  readonly #onDiscover: MutationOutboxOptions["onDiscover"];
  readonly #createChannel: ((name: string) => MutationOutboxChannel) | undefined;
  readonly #lifecycleTarget: MutationLifecycleTarget | undefined;
  readonly #visibilityTarget: MutationVisibilityTarget | undefined;
  readonly #setInterval: ((callback: () => void, milliseconds: number) => number) | undefined;
  readonly #clearInterval: ((intervalId: number) => void) | undefined;
  #channel: MutationOutboxChannel | undefined;
  #intervalId: number | undefined;
  #started = false;
  #pendingDiscovery: Promise<void> = Promise.resolve();
  readonly #scheduledReadyScans = new Set<MutationDiscoveryReason>();

  readonly #handleBroadcast = (event: unknown) => {
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
    this.#getClient = options.getClient;
    this.#onDiscover = options.onDiscover;
    this.#createChannel = options.createBroadcastChannel;
    this.#lifecycleTarget = options.lifecycleWindow;
    this.#visibilityTarget = options.lifecycleDocument;
    const cancellableTimer = options.setInterval !== undefined && options.clearInterval !== undefined;
    this.#setInterval = cancellableTimer ? options.setInterval : undefined;
    this.#clearInterval = cancellableTimer ? options.clearInterval : undefined;
  }

  // Startup is transactional. Every listener, channel and timer is acquired
  // first, the started state published only after all of them succeed, and a
  // setup step that throws - an injected timer port's first call, say - unwinds
  // whatever it acquired. Nothing stays live and nothing stays latched, so the
  // next start runs the whole setup again instead of returning early on a
  // half-initialized outbox.
  async start(): Promise<void> {
    if (this.#started) return;
    try {
      this.#channel = this.#createChannel?.(CHANNEL_NAME);
      this.#channel?.addEventListener("message", this.#handleBroadcast);
      this.#lifecycleTarget?.addEventListener("online", this.#handleOnline);
      this.#lifecycleTarget?.addEventListener("focus", this.#handleFocus);
      this.#visibilityTarget?.addEventListener("visibilitychange", this.#handleVisibility);
      this.#intervalId = this.#setInterval?.(() => this.#scheduleReadyScan("interval"), SCAN_INTERVAL_MS);
    } catch (error) {
      this.#releaseAcquired();
      throw error;
    }
    this.#started = true;
    // Submissions need the runtime's listeners, not a scan of earlier work.
    // Queue startup discovery so a stalled read cannot delay their own commit.
    this.#schedule(() => this.#discoverAll("startup"));
  }

  async stop(): Promise<void> {
    if (!this.#started) return this.#pendingDiscovery;
    this.#started = false;
    this.#releaseAcquired();
    await this.#pendingDiscovery;
  }

  // Releases every resource start() acquires, shared by stop() and start()'s
  // failure path so a newly acquired resource is torn down by both.
  #releaseAcquired(): void {
    this.#channel?.removeEventListener("message", this.#handleBroadcast);
    this.#channel?.close();
    this.#channel = undefined;
    this.#lifecycleTarget?.removeEventListener("online", this.#handleOnline);
    this.#lifecycleTarget?.removeEventListener("focus", this.#handleFocus);
    this.#visibilityTarget?.removeEventListener("visibilitychange", this.#handleVisibility);
    if (this.#intervalId !== undefined) this.#clearInterval?.(this.#intervalId);
    this.#intervalId = undefined;
  }

  async enqueueIntent(
    intent: MutationIntent<A>,
    onCommitted?: (record: MutationOutboxRecord<A>) => void,
    barrier?: MutationStopBarrier,
  ): Promise<MutationOutboxRecord<A>> {
    const record = await this.#storage.enqueueIntent(intent, barrier);
    this.#announceCommit(record, onCommitted);
    return record;
  }

  // The same announce-and-discover tail as enqueueIntent, over the storage's
  // combined Stop write: cancellation and the interrupt record commit together.
  async enqueueInterruptAndCancel(
    intent: MutationIntent<A>,
    onCommitted?: (record: MutationOutboxRecord<A>) => void,
  ): Promise<MutationOutboxRecord<A>> {
    const record = await this.#storage.enqueueInterruptAndCancel(intent);
    this.#announceCommit(record, onCommitted);
    return record;
  }

  #announceCommit(record: MutationOutboxRecord<A>, onCommitted?: (record: MutationOutboxRecord<A>) => void): void {
    onCommitted?.(record);
    // The commit owns the message. Lifecycle scans also discover it if a
    // closing client cannot broadcast; that cannot turn acceptance into failure.
    tryOrUndefined(() =>
      this.#channel?.postMessage({
        version: 1,
        targetRef: record.targetRef,
      } satisfies MutationOutboxWakeup),
    );
    if (this.#isReady()) this.#scheduleDiscovery([record.targetRef], "enqueue");
  }

  async connectionReady(): Promise<void> {
    if (!this.#mayDiscover("ready")) return;
    try {
      await this.#queueDiscovery(() => this.#discoverAll("ready"));
    } catch {
      // Readiness is a lifecycle notification, not a submission result.
      // Durable work remains available for the next discovery scan.
    }
  }

  // May this outbox still discover, right now? Discovery is serialized, so a scan
  // can be queued while everything is fine and RUN after stop() drained the queue
  // or after the connection dropped — which is why every entry asks this and the
  // execution path asks it again, rather than four ad-hoc checks that each cover
  // one moment. Two exceptions, both deliberate:
  //
  //   - "enqueue": a commit that just landed is durable and announcing it costs
  //     nothing, so it is allowed even as the outbox stops (enqueueIntent owns
  //     the readiness decision for it);
  //   - "startup": the first scan runs whether or not the host is ready, so an
  //     app that opens offline still learns what is waiting.
  #mayDiscover(reason: MutationDiscoveryReason): boolean {
    if (reason === "enqueue") return true;
    return this.#started && (reason === "startup" || this.#isReady());
  }

  // Whether dispatching is possible at all right now, read off the one client
  // lookup rather than a flag a host could wire independently of the
  // dispatcher's per-ref readiness.
  #isReady(): boolean {
    return isClientReady(this.#getClient());
  }

  #scheduleReadyScan(reason: MutationDiscoveryReason): void {
    if (!this.#mayDiscover(reason) || this.#scheduledReadyScans.has(reason)) return;
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
    if (!this.#mayDiscover(reason)) return;
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
    // Asked again here, at execution time: this call may have waited behind a
    // slower scan, and there is no point reading storage for an outbox that has
    // stopped or a connection that has dropped.
    if (!this.#mayDiscover(reason)) return;
    await this.#discover(await this.#storage.listTargetRefs(), reason);
  }

  async #discover(targetRefs: string[], reason: MutationDiscoveryReason): Promise<void> {
    // The one place onDiscover is called, so the one place the answer must hold:
    // the storage read above can itself take long enough for the outbox to stop.
    if (!this.#mayDiscover(reason)) return;
    // The consumer may still own failed reconciliation after its last durable record settled.
    await this.#onDiscover(targetRefs, reason);
  }
}
