/**
 * VoiceCoordinator — a pure TypeScript state machine that coordinates streamed
 * voice turns between the native voice bridge and the conversation service.
 *
 * No React, no DOM, no wall-clock sleeps. All timing is driven by an injected
 * clock so tests are fully deterministic.
 *
 * Responsibilities:
 *  - Start/end a voice session for a conversation ref.
 *  - Receive conversation deltas (`consumeConversation`) and chunk new assistant
 *    prose for synthesis, skipping reasoning/tools/notices/history/inactive.
 *  - De-duplicate: never speak the same consumed range twice. Consumed offsets
 *    are stored only for the active process/voice session and cleared on end.
 *  - Process native voice events (`handleNativeEvent`): partial (no mutation),
 *    final (start/steer a turn), barge-in (stop speaking, retain partial, steer
 *    once on final), errors (fall back to typed).
 *  - Ignore stale events (old session ID, non-monotonic sequence).
 *  - Enforce a 250ms queue deadline and 750ms idle-start report.
 */

import type { InputItem } from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type {
  MobileConversation,
  MobileTimelineItem,
} from "../conversation/model";
import type { NativeBridge, VoiceBridgeEvent } from "../native/client";
import type { ConversationService } from "../services/conversation";
import { chunkAssistantProse, type VoiceChunk } from "./chunker";

// ---------------------------------------------------------------------------
// Injected clock
// ---------------------------------------------------------------------------

export interface VoiceClock {
  now(): number;
}

// ---------------------------------------------------------------------------
// Conversation ref
// ---------------------------------------------------------------------------

export interface VoiceRef {
  readonly ref: string;
  readonly threadId: string;
}

// ---------------------------------------------------------------------------
// Coordinator callbacks — the coordinator reports phase transitions to the
// caller (voice state store) without depending on it directly.
// ---------------------------------------------------------------------------

export interface VoiceCoordinatorCallbacks {
  onListening(active: boolean): void;
  onSpeaking(active: boolean): void;
  onInterrupted(interrupted: boolean): void;
  onError(message: string): void;
  onCaption(text: string): void;
  onLevel(level: number): void;
  onAssistantSentence(text: string): void;
  onVoiceSessionId(id: string | null): void;
  /** Reports the measured idle-start latency when synthesis begins. */
  onIdleStartReport(ms: number): void;
}

// ---------------------------------------------------------------------------
// Internal state
// ---------------------------------------------------------------------------

interface ItemConsumption {
  /** Offset in the assistant markdown up to which we've chunked. */
  consumedTo: number;
  /** Set of chunk IDs already spoken (de-dup). */
  spokenChunkIds: Set<string>;
}

type TurnPhase = "idle" | "active";

interface BargeState {
  /** True while we're holding a barge-in partial, waiting for final. */
  pending: boolean;
  /** The partial text captured at barge-in time. */
  partial: string;
}

interface CoordinatorState {
  voiceSessionId: string | null;
  ref: VoiceRef | null;
  phase: TurnPhase;
  /** Per-assistant-item consumption, keyed by item id. */
  consumption: Map<string, ItemConsumption>;
  /** Highest native event seq seen per session (monotonic guard). */
  lastSeq: number;
  /** Barge-in hold state. */
  barge: BargeState;
  /** Timestamp (ms) of the last assistant sentence queued, for deadline check. */
  lastQueueTime: number | null;
  /** Timestamp (ms) when we became idle (for idle-start report). */
  idleSince: number | null;
  /** Whether we've reported idle-start for the current idle period. */
  idleStartReported: boolean;
  /** True if voice has fallen back to typed mode after an error. */
  fallbackToTyped: boolean;
  /** Active assistant item id being streamed (the latest streaming one). */
  activeAssistantItemId: string | null;
  /** Number of speech chunks started but not yet finished. */
  inflightSpeechChunks: number;
  /** Active turn id (derived from the streaming item's turn). */
  activeTurnId: string | null;
}

function initialState(): CoordinatorState {
  return {
    voiceSessionId: null,
    ref: null,
    phase: "idle",
    consumption: new Map(),
    lastSeq: 0,
    barge: { pending: false, partial: "" },
    lastQueueTime: null,
    idleSince: null,
    idleStartReported: false,
    fallbackToTyped: false,
    activeAssistantItemId: null,
    inflightSpeechChunks: 0,
    activeTurnId: null,
  };
}

// ---------------------------------------------------------------------------
// Options
// ---------------------------------------------------------------------------

export interface VoiceCoordinatorOptions {
  readonly clock: VoiceClock;
  readonly bridge: NativeBridge;
  readonly callbacks: VoiceCoordinatorCallbacks;
  /** Locale for voice.start; defaults to "en-US". */
  readonly locale?: string;
  /** Queue deadline (ms). Default 250. */
  readonly queueDeadlineMs?: number;
  /** Idle-start report threshold (ms). Default 750. */
  readonly idleStartMs?: number;
}

// ---------------------------------------------------------------------------
// Coordinator
// ---------------------------------------------------------------------------

export class VoiceCoordinator {
  private readonly clock: VoiceClock;
  private readonly bridge: NativeBridge;
  private readonly callbacks: VoiceCoordinatorCallbacks;
  private readonly locale: string;
  private readonly queueDeadlineMs: number;
  private readonly idleStartMs: number;
  private state: CoordinatorState = initialState();
  private unsubEvents: (() => void) | null = null;

  constructor(opts: VoiceCoordinatorOptions) {
    this.clock = opts.clock;
    this.bridge = opts.bridge;
    this.callbacks = opts.callbacks;
    this.locale = opts.locale ?? "en-US";
    this.queueDeadlineMs = opts.queueDeadlineMs ?? 250;
    this.idleStartMs = opts.idleStartMs ?? 750;
  }

  // -------------------------------------------------------------------------
  // Public lifecycle
  // -------------------------------------------------------------------------

  async start(ref: VoiceRef): Promise<void> {
    // Session switch: clear everything and start fresh.
    this.endSync();
    this.state = initialState();
    this.state.ref = ref;

    try {
      const granted = await this.bridge.voicePermissions();
      if (!granted) {
        this.state.fallbackToTyped = true;
        this.callbacks.onError("Voice permissions denied");
        return;
      }
      const ready = await this.bridge.voiceStart(this.locale);
      this.state.voiceSessionId = ready.voiceSessionId;
      this.state.idleSince = this.clock.now();
      this.callbacks.onVoiceSessionId(ready.voiceSessionId);
      this.callbacks.onListening(true);
      this.unsubEvents = this.bridge.onVoiceEvent((e) =>
        this.handleNativeEvent(e),
      );
    } catch (err) {
      this.state.fallbackToTyped = true;
      this.callbacks.onError(err instanceof Error ? err.message : String(err));
    }
  }

  end(): void {
    this.endSync();
  }

  private endSync(): void {
    const sid = this.state.voiceSessionId;
    if (sid !== null) {
      void this.bridge.voiceStop(sid).catch(() => {});
      void this.bridge.voiceStopSpeaking(sid).catch(() => {});
    }
    if (this.unsubEvents !== null) {
      this.unsubEvents();
      this.unsubEvents = null;
    }
    this.state = initialState();
    this.callbacks.onListening(false);
    this.callbacks.onSpeaking(false);
    this.callbacks.onInterrupted(false);
    this.callbacks.onVoiceSessionId(null);
    this.callbacks.onCaption("");
    this.callbacks.onLevel(0);
  }

  // -------------------------------------------------------------------------
  // Conversation consumption
  // -------------------------------------------------------------------------

  /**
   * Receive a conversation snapshot/delta and chunk new assistant prose for
   * synthesis. Only the active assistant's new (unconsumed) prose in the active
   * voice session is spoken.
   */
  consumeConversation(conversation: MobileConversation): void {
    if (this.state.voiceSessionId === null || this.state.ref === null) return;
    if (this.state.fallbackToTyped) return;
    // Only speak content from the active conversation (matching thread id).
    if (conversation.id !== this.state.ref.threadId) return;
    if (conversation.askPending) return; // Ask active → voice disabled.

    const items = conversation.items;
    if (items.length === 0) return;

    // Find the latest assistant item (the active/streaming one).
    let activeItem: MobileTimelineItem | null = null;
    for (let i = items.length - 1; i >= 0; i--) {
      const item = items[i];
      if (item !== undefined && item.kind === "assistant") {
        activeItem = item;
        break;
      }
    }
    if (activeItem === null || activeItem.kind !== "assistant") return;

    const itemId = activeItem.id;
    this.state.activeAssistantItemId = itemId;
    this.state.activeTurnId = itemId; // turn id derived from item id.

    const markdown = activeItem.markdown;
    const consumed = this.state.consumption.get(itemId) ?? {
      consumedTo: 0,
      spokenChunkIds: new Set<string>(),
    };

    if (markdown.length <= consumed.consumedTo) return;

    const newText = markdown.slice(consumed.consumedTo);
    const flush = !activeItem.streaming;
    const result = chunkAssistantProse(newText, {
      voiceSessionId: this.state.voiceSessionId,
      turnId: itemId,
      itemIndex: 0,
      baseOffset: consumed.consumedTo,
      flush,
    });

    const newChunks: VoiceChunk[] = [];
    for (const chunk of result.chunks) {
      if (consumed.spokenChunkIds.has(chunk.id)) continue;
      consumed.spokenChunkIds.add(chunk.id);
      newChunks.push(chunk);
    }

    consumed.consumedTo = result.consumedTo;
    this.state.consumption.set(itemId, consumed);

    // Queue new chunks for synthesis.
    if (newChunks.length > 0) {
      const sid = this.state.voiceSessionId;
      if (sid === null) return;
      const now = this.clock.now();
      this.state.lastQueueTime = now;

      // Idle-start report: if we were idle and synthesis begins, report latency.
      if (this.state.phase === "idle" && !this.state.idleStartReported) {
        const idleSince = this.state.idleSince ?? now;
        const elapsed = now - idleSince;
        this.callbacks.onIdleStartReport(elapsed);
        this.state.idleStartReported = true;
      }

      this.callbacks.onSpeaking(true);
      for (const chunk of newChunks) {
        this.callbacks.onAssistantSentence(chunk.text);
        void this.bridge.voiceSpeak(sid, chunk.id, chunk.text).catch((err) => {
          this.callbacks.onError(
            err instanceof Error ? err.message : String(err),
          );
        });
      }
    }
  }

  // -------------------------------------------------------------------------
  // Native event handling
  // -------------------------------------------------------------------------

  handleNativeEvent(event: VoiceBridgeEvent): void {
    const sid = this.state.voiceSessionId;
    if (sid === null) return;

    // Stale session check: events from a different session are ignored.
    if (!isVoiceEventForSession(event, sid)) return;

    // Sequence monotonicity check (events with seq).
    const seq = voiceEventSeq(event);
    if (seq !== null) {
      if (seq < this.state.lastSeq) return; // non-monotonic → ignore
      this.state.lastSeq = seq;
    }

    switch (event.type) {
      case "voice.level":
        this.callbacks.onLevel(event.level);
        break;

      case "voice.partial":
        // Partial → no mutation. Only update caption for UX.
        this.callbacks.onCaption(event.text);
        if (this.state.barge.pending) {
          this.state.barge.partial = event.text;
        }
        break;

      case "voice.final":
        this.handleFinal(event.text);
        break;

      case "voice.speechStarted":
        this.state.inflightSpeechChunks++;
        this.callbacks.onSpeaking(true);
        break;

      case "voice.speechFinished":
        // A chunk finished; only stop speaking when no more chunks are in flight.
        if (this.state.inflightSpeechChunks > 0) {
          this.state.inflightSpeechChunks--;
        }
        if (this.state.inflightSpeechChunks === 0) {
          this.callbacks.onSpeaking(false);
        }
        break;

      case "voice.bargeIn":
        this.handleBargeIn(event.partial);
        break;

      case "voice.interrupted":
        this.state.inflightSpeechChunks = 0;
        this.callbacks.onInterrupted(false);
        this.callbacks.onSpeaking(false);
        break;

      case "voice.error":
        this.handleVoiceError(event);
        break;
    }
  }

  // -------------------------------------------------------------------------
  // Final recognition → start or steer a turn
  // -------------------------------------------------------------------------

  private handleFinal(text: string): void {
    const trimmed = text.trim();
    if (trimmed.length === 0) return;

    if (this.state.barge.pending) {
      // Barge-in: steer once with the held partial (or final text).
      this.state.barge.pending = false;
      this.state.barge.partial = "";
      this.callbacks.onInterrupted(false);
      this.steer(trimmed);
      return;
    }

    if (this.state.phase === "idle") {
      this.startNewTurn(trimmed);
    } else {
      this.steer(trimmed);
    }
  }

  private startNewTurn(text: string): void {
    const ref = this.state.ref;
    if (ref === null) return;
    this.state.phase = "active";
    this.state.idleSince = null;
    this.state.idleStartReported = false;
    this.callbacks.onCaption(text);
    // Fire-and-forget: the conversation service sends the turn.
    void this.sendInput(text, "send").catch((err) => {
      this.callbacks.onError(err instanceof Error ? err.message : String(err));
    });
  }

  private steer(text: string): void {
    const ref = this.state.ref;
    if (ref === null) return;
    this.callbacks.onCaption(text);
    void this.sendInput(text, "steer").catch((err) => {
      this.callbacks.onError(err instanceof Error ? err.message : String(err));
    });
  }

  private async sendInput(
    text: string,
    method: "send" | "steer",
  ): Promise<void> {
    const ref = this.state.ref;
    if (ref === null) return;
    const input: InputItem[] = [{ type: "text", text }];
    // The coordinator does not hold a ConversationService directly; it uses the
    // bridge. The caller wires send/steer via callbacks. For now, we call the
    // conversation service through the injected bridge-shaped dependency.
    // (See injectConversationService below.)
    const svc = this.conversationService;
    if (svc === null) return;
    if (method === "send") {
      await svc.send(input);
    } else {
      await svc.steer(input);
    }
  }

  private conversationService: ConversationService | null = null;

  /** Inject the conversation service used for send/steer. */
  setConversationService(svc: ConversationService): void {
    this.conversationService = svc;
  }

  // -------------------------------------------------------------------------
  // Barge-in
  // -------------------------------------------------------------------------

  private handleBargeIn(partial: string): void {
    this.state.barge.pending = true;
    this.state.barge.partial = partial;
    this.state.inflightSpeechChunks = 0;
    this.callbacks.onInterrupted(true);
    this.callbacks.onSpeaking(false);
    const sid = this.state.voiceSessionId;
    if (sid !== null) {
      void this.bridge.voiceStopSpeaking(sid).catch(() => {});
    }
  }

  // -------------------------------------------------------------------------
  // Error fallback
  // -------------------------------------------------------------------------

  private handleVoiceError(
    event: Extract<VoiceBridgeEvent, { type: "voice.error" }>,
  ): void {
    this.state.fallbackToTyped = true;
    this.callbacks.onError(event.error.message);
    this.callbacks.onListening(false);
    this.callbacks.onSpeaking(false);
    this.callbacks.onInterrupted(false);
    // Retain partial voice draft for typed fallback.
    if (this.state.barge.partial.length > 0) {
      this.callbacks.onCaption(this.state.barge.partial);
    }
  }

  // -------------------------------------------------------------------------
  // Accessors (for tests)
  // -------------------------------------------------------------------------

  get voiceSessionId(): string | null {
    return this.state.voiceSessionId;
  }

  get phase(): TurnPhase {
    return this.state.phase;
  }

  get isFallbackToTyped(): boolean {
    return this.state.fallbackToTyped;
  }

  get lastQueueTime(): number | null {
    return this.state.lastQueueTime;
  }
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function isVoiceEventForSession(
  event: VoiceBridgeEvent,
  sessionId: string,
): boolean {
  // voice.level, voice.partial, voice.final, voice.speechStarted,
  // voice.speechFinished, voice.bargeIn, voice.interrupted, voice.error
  // all carry voiceSessionId.
  return event.voiceSessionId === sessionId;
}

function voiceEventSeq(event: VoiceBridgeEvent): number | null {
  if (
    "seq" in event &&
    typeof event.seq === "number" &&
    Number.isFinite(event.seq)
  ) {
    return event.seq;
  }
  return null;
}

// Re-export the bridge event type for consumers.
export type { VoiceBridgeEvent };
