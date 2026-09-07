import type { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";

export type MutationOutboxState = "submitting" | "blockedUnknown";
export type MutationRecoveryKind = "rejected" | "orphaned";
export type MutationDiscoveryReason =
  | "startup"
  | "enqueue"
  | "broadcast"
  | "ready"
  | "online"
  | "focus"
  | "visibility"
  | "interval";

// marker is the composer marker number this attachment was staged under. It
// is what pairs the attachment back to its "[image N]" anchor in composerText
// when a failed record is restored into a composer, so it is recorded rather
// than re-derived from array position at restore time.
export interface MutationAttachment {
  presentationId: string;
  marker: number;
  name: string;
  mediaType: string;
  blob: Blob;
}

export interface MutationIntent {
  targetRef: string;
  threadId?: string;
  method: string;
  payload: Record<string, unknown>;
  attachments: MutationAttachment[];
  optimisticDisplay: unknown;
  // The composer's text exactly as typed, "[image N]" anchors intact. The
  // payload's own text is not a substitute: composerMutationIntent translates
  // every marker to prose at the submit boundary, so a payload restored
  // straight into a composer would carry sentences about images in place of
  // the anchors its tiles remove. Absent on intents no composer authored.
  composerText?: string;
}

export interface MutationRecord extends MutationIntent {
  version: 1;
  clientMutationId: string;
  intentSequence: number;
  createdAt: number;
}

export interface MutationOutboxRecord extends MutationRecord {
  state: MutationOutboxState;
}

export interface MutationOptimisticRecord extends MutationRecord {
  state: "accepted";
}

export interface MutationRecoveryRecord extends MutationOutboxRecord {
  recoveryKind: MutationRecoveryKind;
  // Why the daemon refused, in its own words. Without it a recovery row can
  // only say that something did not happen, which is kata 2f41: a Steer or
  // Stop refused with nothing on screen explaining why. Optional because
  // records written before this existed carry no reason, and because some
  // recovery kinds (orphaned) have no daemon message to carry.
  recoveryReason?: string;
}

interface BroadcastChannelLike extends EventTarget {
  postMessage(message: unknown): void;
  close(): void;
}

interface LifecycleTarget {
  addEventListener(type: string, listener: EventListener): void;
  removeEventListener(type: string, listener: EventListener): void;
}

interface VisibilityTarget extends LifecycleTarget {
  readonly visibilityState: string;
}

export interface MutationOutboxOptions {
  isReady: () => boolean;
  onDiscover: (targetRefs: string[], reason: MutationDiscoveryReason) => void | Promise<void>;
  createBroadcastChannel?: (name: string) => BroadcastChannelLike;
  lifecycleWindow?: LifecycleTarget;
  lifecycleDocument?: VisibilityTarget;
  setInterval?: (callback: () => void, milliseconds: number) => number;
  clearInterval?: (intervalId: number) => void;
}

// MutationOutbox owns durable-intent discovery. Its interval and lifecycle
// hooks only announce records already stored by the adapter; authoritative RPC
// outcomes are the only callers allowed to settle or reclassify them.
export class MutationOutbox {
  readonly #storage: MutationOutboxIndexedDB;
  readonly #isReady: () => boolean;
  readonly #onDiscover: MutationOutboxOptions["onDiscover"];
  readonly #createBroadcastChannel: (name: string) => BroadcastChannelLike;
  readonly #lifecycleWindow: LifecycleTarget | undefined;
  readonly #lifecycleDocument: VisibilityTarget | undefined;
  readonly #setInterval: (callback: () => void, milliseconds: number) => number;
  readonly #clearInterval: (intervalId: number) => void;
  #broadcastChannel: BroadcastChannelLike | undefined;
  #intervalId: number | undefined;
  #started = false;
  #pendingDiscovery: Promise<void> = Promise.resolve();
  readonly #scheduledReadyScans = new Set<MutationDiscoveryReason>();

  readonly #handleBroadcast = (event: Event) => {
    if (!this.#isReady()) return;
    const message = (event as MessageEvent<unknown>).data;
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
    if (this.#lifecycleDocument?.visibilityState === "visible") this.#scheduleReadyScan("visibility");
  };

  constructor(storage: MutationOutboxIndexedDB, options: MutationOutboxOptions) {
    this.#storage = storage;
    this.#isReady = options.isReady;
    this.#onDiscover = options.onDiscover;
    this.#createBroadcastChannel = options.createBroadcastChannel ?? ((name) => new BroadcastChannel(name));
    this.#lifecycleWindow = options.lifecycleWindow ?? (typeof window === "undefined" ? undefined : window);
    this.#lifecycleDocument = options.lifecycleDocument ?? (typeof document === "undefined" ? undefined : document);
    this.#setInterval =
      options.setInterval ?? ((callback, milliseconds) => globalThis.setInterval(callback, milliseconds));
    this.#clearInterval = options.clearInterval ?? ((intervalId) => globalThis.clearInterval(intervalId));
  }

  async start(): Promise<void> {
    if (this.#started) return;
    this.#started = true;
    this.#broadcastChannel = this.#createBroadcastChannel("evener-mutation-outbox-v1");
    this.#broadcastChannel.addEventListener("message", this.#handleBroadcast);
    this.#lifecycleWindow?.addEventListener("online", this.#handleOnline);
    this.#lifecycleWindow?.addEventListener("focus", this.#handleFocus);
    this.#lifecycleDocument?.addEventListener("visibilitychange", this.#handleVisibility);
    this.#intervalId = this.#setInterval(() => this.#scheduleReadyScan("interval"), 2000);
    // Submissions need the runtime's listeners, not a scan of earlier work.
    // Queue startup discovery so a stalled read cannot delay their own commit.
    this.#schedule(() => this.#discoverAll("startup"));
  }

  async stop(): Promise<void> {
    if (!this.#started) return this.#pendingDiscovery;
    this.#started = false;
    this.#broadcastChannel?.removeEventListener("message", this.#handleBroadcast);
    this.#broadcastChannel?.close();
    this.#broadcastChannel = undefined;
    this.#lifecycleWindow?.removeEventListener("online", this.#handleOnline);
    this.#lifecycleWindow?.removeEventListener("focus", this.#handleFocus);
    this.#lifecycleDocument?.removeEventListener("visibilitychange", this.#handleVisibility);
    if (this.#intervalId !== undefined) this.#clearInterval(this.#intervalId);
    this.#intervalId = undefined;
    await this.#pendingDiscovery;
  }

  async enqueueIntent(intent: MutationIntent): Promise<MutationOutboxRecord> {
    const record = await this.#storage.enqueueIntent(intent);
    try {
      this.#broadcastChannel?.postMessage({
        version: 1,
        targetRef: record.targetRef,
      } satisfies MutationOutboxWakeup);
    } catch {
      // The commit owns the message. Lifecycle scans also discover it if a
      // closing tab cannot broadcast; that cannot turn acceptance into failure.
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
    if (!this.#isReady() || this.#scheduledReadyScans.has(reason)) return;
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
      // Durable work remains in IndexedDB and the next lifecycle scan retries discovery.
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

interface MutationOutboxWakeup {
  version: 1;
  targetRef: string;
}

function isMutationOutboxWakeup(value: unknown): value is MutationOutboxWakeup {
  if (typeof value !== "object" || value === null) return false;
  const message = value as Partial<MutationOutboxWakeup>;
  return message.version === 1 && typeof message.targetRef === "string" && message.targetRef.length > 0;
}
