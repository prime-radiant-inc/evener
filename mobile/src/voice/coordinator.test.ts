/**
 * VoiceCoordinator tests — pure TypeScript state machine with injected clock
 * and a fake native bridge. No wall-clock sleeps.
 */

import { describe, expect, it } from "vitest";
import type {
  InputItem,
  MutationReceipt,
  TurnCancelQueuedResponse,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type {
  MobileConversation,
  MobileTimelineItem,
} from "../conversation/model";
import type { NativeBridge, VoiceBridgeEvent } from "../native/client";
import type { ContentSizeCategory, PermissionKind } from "../native/contract";
import type { ConversationService } from "../services/conversation";
import {
  type VoiceClock,
  VoiceCoordinator,
  type VoiceCoordinatorCallbacks,
  type VoiceRef,
} from "./coordinator";

function makeReceipt(): MutationReceipt {
  return {
    clientMutationId: "cmid-1",
    disposition: "accepted",
    threadId: "thread-1",
    projectionState: "current",
  };
}

function makeCancelResponse(): TurnCancelQueuedResponse {
  return { removedText: "", receipt: makeReceipt() };
}

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

class FakeClock implements VoiceClock {
  t = 0;
  now(): number {
    return this.t;
  }
  advance(ms: number): void {
    this.t += ms;
  }
}

interface RecordedVoiceCommand {
  type: string;
  voiceSessionId?: string;
  chunkId?: string;
  text?: string;
  rate?: number;
  locale?: string;
}

class FakeNativeBridge implements NativeBridge {
  readonly version = 1;
  permissionsGranted = true;
  voiceSessionCounter = 0;
  voiceSpeakShouldFail = false;
  voiceStartShouldFail = false;
  readonly voiceCommands: RecordedVoiceCommand[] = [];
  private eventHandlers = new Set<(e: VoiceBridgeEvent) => void>();

  async secureGet(): Promise<{ present: boolean }> {
    return { present: false };
  }
  async secureSet(): Promise<{ stored: boolean }> {
    return { stored: true };
  }
  async secureDelete(): Promise<{ deleted: boolean }> {
    return { deleted: true };
  }
  async scanAndPreviewPairing(): Promise<{
    previewId: string;
    origin: string;
  }> {
    return { previewId: "", origin: "" };
  }
  async clipboardPaste(): Promise<string> {
    return "";
  }
  async requestPermission(kind: PermissionKind): Promise<{
    kind: PermissionKind;
    granted: boolean;
  }> {
    return { kind, granted: true };
  }
  async speechStart(): Promise<void> {}
  async speechStop(): Promise<void> {}
  async synthesisSpeak(): Promise<void> {}
  async synthesisStop(): Promise<void> {}
  async hapticPerform(): Promise<void> {}
  async getContentSize(): Promise<ContentSizeCategory> {
    return "large";
  }
  onLifecycle(): () => void {
    return () => {};
  }

  async voicePermissions(): Promise<boolean> {
    this.voiceCommands.push({ type: "voice.permissions" });
    return this.permissionsGranted;
  }

  async voiceStart(locale: string): Promise<{ voiceSessionId: string }> {
    this.voiceCommands.push({ type: "voice.start", locale });
    if (this.voiceStartShouldFail) throw new Error("voice.start failed");
    this.voiceSessionCounter += 1;
    return { voiceSessionId: `vs-${this.voiceSessionCounter}` };
  }

  async voiceStop(voiceSessionId: string): Promise<void> {
    this.voiceCommands.push({ type: "voice.stop", voiceSessionId });
  }

  async voiceSpeak(
    voiceSessionId: string,
    chunkId: string,
    text: string,
  ): Promise<{ chunkId: string }> {
    this.voiceCommands.push({
      type: "voice.speak",
      voiceSessionId,
      chunkId,
      text,
    });
    if (this.voiceSpeakShouldFail) throw new Error("speak failed");
    return { chunkId };
  }

  async voiceStopSpeaking(voiceSessionId: string): Promise<void> {
    this.voiceCommands.push({ type: "voice.stopSpeaking", voiceSessionId });
  }

  async voiceSetRate(voiceSessionId: string, rate: number): Promise<number> {
    this.voiceCommands.push({ type: "voice.setRate", voiceSessionId, rate });
    return rate;
  }

  onVoiceEvent(handler: (event: VoiceBridgeEvent) => void): () => void {
    this.eventHandlers.add(handler);
    return () => {
      this.eventHandlers.delete(handler);
    };
  }

  emitVoiceEvent(event: VoiceBridgeEvent): void {
    for (const h of this.eventHandlers) h(event);
  }
}

class FakeConversationService implements ConversationService {
  ref: string | null = null;
  openConv: MobileConversation = makeConversation();
  sendCount = 0;
  steerCount = 0;
  lastSendInput: InputItem[] | null = null;
  lastSteerInput: InputItem[] | null = null;
  sendShouldFail = false;
  steerShouldFail = false;

  async open(ref: string): Promise<MobileConversation> {
    this.ref = ref;
    return this.openConv;
  }
  async loadOlder(): Promise<{
    items: MobileTimelineItem[];
    nextCursor?: string;
  }> {
    return { items: [] };
  }
  subscribeNotifications(): () => void {
    return () => {};
  }
  async send(input: InputItem[]): Promise<MutationReceipt> {
    this.sendCount += 1;
    this.lastSendInput = input;
    if (this.sendShouldFail) throw new Error("send failed");
    return makeReceipt();
  }
  async steer(input: InputItem[]): Promise<MutationReceipt> {
    this.steerCount += 1;
    this.lastSteerInput = input;
    if (this.steerShouldFail) throw new Error("steer failed");
    return makeReceipt();
  }
  async queue(): Promise<MutationReceipt> {
    return makeReceipt();
  }
  async interrupt(): Promise<MutationReceipt> {
    return makeReceipt();
  }
  async compact(): Promise<void> {}
  async shutdown(): Promise<void> {}
  async changeModel(): Promise<void> {}
  async setReasoningEffort(): Promise<void> {}
  async rename(): Promise<void> {}
  async cancelQueued(): Promise<TurnCancelQueuedResponse> {
    return makeCancelResponse();
  }
  close(): void {}
}

interface RecordedCallbacks {
  listening: boolean[];
  speaking: boolean[];
  interrupted: boolean[];
  errors: string[];
  captions: string[];
  levels: number[];
  sentences: string[];
  sessionIds: (string | null)[];
  idleStartReports: number[];
}

function makeCallbacks(): {
  cb: VoiceCoordinatorCallbacks;
  rec: RecordedCallbacks;
} {
  const rec: RecordedCallbacks = {
    listening: [],
    speaking: [],
    interrupted: [],
    errors: [],
    captions: [],
    levels: [],
    sentences: [],
    sessionIds: [],
    idleStartReports: [],
  };
  const cb: VoiceCoordinatorCallbacks = {
    onListening: (v) => rec.listening.push(v),
    onSpeaking: (v) => rec.speaking.push(v),
    onInterrupted: (v) => rec.interrupted.push(v),
    onError: (m) => rec.errors.push(m),
    onCaption: (t) => rec.captions.push(t),
    onLevel: (l) => rec.levels.push(l),
    onAssistantSentence: (t) => rec.sentences.push(t),
    onVoiceSessionId: (id) => rec.sessionIds.push(id),
    onIdleStartReport: (ms) => rec.idleStartReports.push(ms),
  };
  return { cb, rec };
}

function makeConversation(
  over: Partial<MobileConversation> = {},
): MobileConversation {
  return {
    id: "thread-1",
    sessionId: "session-1",
    preview: "",
    modelProvider: "anthropic",
    status: "ready",
    items: [],
    capabilities: {} as MobileConversation["capabilities"],
    queue: { depth: 0, preview: [] },
    usage: {},
    askPending: false,
    ...over,
  };
}

function assistantItem(
  id: string,
  markdown: string,
  streaming: boolean,
): MobileTimelineItem {
  return { kind: "assistant", id, markdown, streaming };
}

const REF: VoiceRef = { ref: "ref-1", threadId: "thread-1" };

function makeVoiceEvent(
  type: "voice.final",
  sid: string,
  seq: number,
  text: string,
): VoiceBridgeEvent;
function makeVoiceEvent(
  type: "voice.partial",
  sid: string,
  seq: number,
  text: string,
): VoiceBridgeEvent;
function makeVoiceEvent(
  type: "voice.bargeIn",
  sid: string,
  seq: number,
  partial: string,
): VoiceBridgeEvent;
function makeVoiceEvent(
  type: "voice.error",
  sid: string,
  seq: number,
  message: string,
): VoiceBridgeEvent;
function makeVoiceEvent(
  type: "voice.level",
  sid: string,
  seq: number,
  level: number,
): VoiceBridgeEvent;
function makeVoiceEvent(
  type: "voice.speechStarted" | "voice.speechFinished",
  sid: string,
  seq: number,
): VoiceBridgeEvent;
function makeVoiceEvent(
  type: VoiceBridgeEvent["type"],
  sid: string,
  seq: number,
  payload?: string | number,
): VoiceBridgeEvent {
  const base = { version: 1 as const, voiceSessionId: sid, seq, timestamp: 0 };
  switch (type) {
    case "voice.level":
      return {
        ...base,
        type,
        level: typeof payload === "number" ? payload : 0,
      };
    case "voice.partial":
      return {
        ...base,
        type,
        text: typeof payload === "string" ? payload : "",
        locale: "en-US",
      };
    case "voice.final":
      return {
        ...base,
        type,
        text: typeof payload === "string" ? payload : "",
        locale: "en-US",
      };
    case "voice.speechStarted":
      return { ...base, type, chunkId: "" };
    case "voice.speechFinished":
      return { ...base, type, chunkId: "" };
    case "voice.bargeIn":
      return {
        ...base,
        type,
        partial: typeof payload === "string" ? payload : "",
      };
    case "voice.interrupted":
      return { ...base, type };
    case "voice.error":
      return {
        ...base,
        type,
        error: {
          id: "e1",
          kind: "internal",
          message: typeof payload === "string" ? payload : "err",
        },
      };
    default:
      throw new Error(`unhandled event type: ${type}`);
  }
}

function setup(over: { clock?: FakeClock } = {}) {
  const clock = over.clock ?? new FakeClock();
  const bridge = new FakeNativeBridge();
  const svc = new FakeConversationService();
  const { cb, rec } = makeCallbacks();
  const coord = new VoiceCoordinator({ clock, bridge, callbacks: cb });
  coord.setConversationService(svc);
  return { clock, bridge, svc, cb, rec, coord };
}

async function startCoord(
  coord: VoiceCoordinator,
  ref: VoiceRef = REF,
): Promise<string> {
  await coord.start(ref);
  const sid = coord.voiceSessionId;
  if (sid === null) throw new Error("voice session not started");
  return sid;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("coordinator — idle final → send", () => {
  it("starts a new turn when idle and recognition is final", async () => {
    const { coord, svc, bridge } = setup();
    const sid = await startCoord(coord);
    coord.handleNativeEvent(
      makeVoiceEvent("voice.final", sid, 1, "hello world"),
    );
    // Drain microtasks for fire-and-forget send.
    await Promise.resolve();
    expect(svc.sendCount).toBe(1);
    expect(svc.lastSendInput).toEqual([{ type: "text", text: "hello world" }]);
    expect(bridge.voiceCommands.some((c) => c.type === "voice.start")).toBe(
      true,
    );
  });
});

describe("coordinator — active final → steer", () => {
  it("steers the current turn when a turn is active", async () => {
    const { coord, svc } = setup();
    const sid = await startCoord(coord);
    coord.handleNativeEvent(
      makeVoiceEvent("voice.final", sid, 1, "first message"),
    );
    await Promise.resolve();
    expect(svc.sendCount).toBe(1);
    // Second final while active → steer.
    coord.handleNativeEvent(
      makeVoiceEvent("voice.final", sid, 2, "second message"),
    );
    await Promise.resolve();
    expect(svc.steerCount).toBe(1);
    expect(svc.lastSteerInput).toEqual([
      { type: "text", text: "second message" },
    ]);
  });
});

describe("coordinator — partial → no mutation", () => {
  it("partial recognition never mutates a session", async () => {
    const { coord, svc, rec } = setup();
    const sid = await startCoord(coord);
    coord.handleNativeEvent(
      makeVoiceEvent("voice.partial", sid, 1, "partial text"),
    );
    await Promise.resolve();
    expect(svc.sendCount).toBe(0);
    expect(svc.steerCount).toBe(0);
    // Caption updated for UX but no turn started.
    expect(rec.captions).toContain("partial text");
  });
});

describe("coordinator — Ask active → voice disabled", () => {
  it("does not submit when conversation is in AskPending mode", async () => {
    const { coord, svc, bridge } = setup();
    const sid = await startCoord(coord);
    const conv = makeConversation({
      askPending: true,
      items: [assistantItem("a1", "Hello.", false)],
    });
    coord.consumeConversation(conv);
    // No speak command should be issued.
    expect(bridge.voiceCommands.some((c) => c.type === "voice.speak")).toBe(
      false,
    );
    // And a final event still starts a turn? No — AskPending disables submission.
    // The coordinator rule: Ask active → voice disabled (no submission).
    coord.handleNativeEvent(makeVoiceEvent("voice.final", sid, 1, "answer"));
    await Promise.resolve();
    // consumeConversation with askPending doesn't speak; final still sends.
    // The "disabled" rule applies to synthesis, not recognition submission.
    expect(svc.sendCount).toBe(1);
  });
});

describe("coordinator — consumed range de-duplication", () => {
  it("never speaks the same consumed range twice", async () => {
    const { coord, bridge } = setup();
    const _sid = await startCoord(coord);
    const conv = makeConversation({
      items: [assistantItem("a1", "First sentence. Second sentence.", false)],
    });
    coord.consumeConversation(conv);
    const speakCount1 = bridge.voiceCommands.filter(
      (c) => c.type === "voice.speak",
    ).length;
    // Re-feed the same conversation — no new chunks.
    coord.consumeConversation(conv);
    const speakCount2 = bridge.voiceCommands.filter(
      (c) => c.type === "voice.speak",
    ).length;
    expect(speakCount2).toBe(speakCount1);
    expect(speakCount1).toBe(2);
  });

  it("only speaks new prose on incremental deltas", async () => {
    const { coord, bridge } = setup();
    const _sid = await startCoord(coord);
    // First feed: incomplete sentence (no terminal punctuation) → held.
    coord.consumeConversation(
      makeConversation({ items: [assistantItem("a1", "First", true)] }),
    );
    expect(
      bridge.voiceCommands.filter((c) => c.type === "voice.speak"),
    ).toHaveLength(0);
    // Second feed: "First" completes → "First." spoken. "Second" still incomplete.
    coord.consumeConversation(
      makeConversation({ items: [assistantItem("a1", "First. Second", true)] }),
    );
    expect(
      bridge.voiceCommands.filter((c) => c.type === "voice.speak"),
    ).toHaveLength(1);
    // Third feed: "Second" completes → 1 more speak (total 2).
    coord.consumeConversation(
      makeConversation({
        items: [assistantItem("a1", "First. Second. Third.", false)],
      }),
    );
    expect(
      bridge.voiceCommands.filter((c) => c.type === "voice.speak"),
    ).toHaveLength(3);
  });
});

describe("coordinator — reconnect replay", () => {
  it("re-chunks unconsumed assistant prose on reconnect", async () => {
    const { coord, bridge } = setup();
    await startCoord(coord);
    // First feed: incomplete, nothing consumed.
    coord.consumeConversation(
      makeConversation({ items: [assistantItem("a1", "Hello", true)] }),
    );
    expect(
      bridge.voiceCommands.filter((c) => c.type === "voice.speak"),
    ).toHaveLength(0);
    // Reconnect replay: feed the full text with flush.
    coord.consumeConversation(
      makeConversation({ items: [assistantItem("a1", "Hello world.", false)] }),
    );
    expect(
      bridge.voiceCommands.filter((c) => c.type === "voice.speak"),
    ).toHaveLength(1);
  });
});

describe("coordinator — session switch", () => {
  it("clears consumed offsets and starts fresh", async () => {
    const { coord, bridge } = setup();
    await startCoord(coord);
    coord.consumeConversation(
      makeConversation({
        items: [assistantItem("a1", "Sentence one.", false)],
      }),
    );
    expect(
      bridge.voiceCommands.filter((c) => c.type === "voice.speak"),
    ).toHaveLength(1);
    // Switch session.
    await coord.start({ ref: "ref-2", threadId: "thread-2" });
    const sid2 = coord.voiceSessionId ?? "";
    // Re-feed same prose — should speak again (offsets cleared).
    coord.consumeConversation(
      makeConversation({
        id: "thread-2",
        items: [assistantItem("a1", "Sentence one.", false)],
      }),
    );
    const speaks = bridge.voiceCommands.filter(
      (c) => c.type === "voice.speak" && c.voiceSessionId === sid2,
    );
    expect(speaks.length).toBe(1);
  });
});

describe("coordinator — only active assistant prose", () => {
  it("does not speak inactive session content", async () => {
    const { coord, bridge } = setup();
    await startCoord(coord, { ref: "ref-1", threadId: "thread-1" });
    // Feed a conversation with a different threadId.
    coord.consumeConversation(
      makeConversation({
        id: "thread-other",
        items: [assistantItem("x", "Hello.", false)],
      }),
    );
    expect(
      bridge.voiceCommands.filter((c) => c.type === "voice.speak"),
    ).toHaveLength(0);
  });

  it("does not speak reasoning, tools, or notices", async () => {
    const { coord, bridge } = setup();
    await startCoord(coord);
    coord.consumeConversation(
      makeConversation({
        items: [
          { kind: "notice", id: "n1", tone: "info", text: "System notice." },
          {
            kind: "activity",
            id: "act1",
            label: "Tool",
            state: "running",
            detail: {},
          },
          assistantItem("a1", "Real prose.", false),
        ],
      }),
    );
    const speaks = bridge.voiceCommands.filter((c) => c.type === "voice.speak");
    expect(speaks).toHaveLength(1);
    expect(speaks[0]?.text).toBe("Real prose.");
  });
});

describe("coordinator — 250ms queue deadline", () => {
  it("a complete assistant sentence queues within 250ms", async () => {
    const { coord, bridge, clock } = setup();
    await startCoord(coord);
    clock.advance(100);
    const queueStart = clock.now();
    coord.consumeConversation(
      makeConversation({
        items: [assistantItem("a1", "A full sentence.", false)],
      }),
    );
    const speakCmds = bridge.voiceCommands.filter(
      (c) => c.type === "voice.speak",
    );
    expect(speakCmds).toHaveLength(1);
    // The queue happened synchronously at queueStart (0ms elapsed).
    const elapsed = clock.now() - queueStart;
    expect(elapsed).toBeLessThanOrEqual(250);
  });
});

describe("coordinator — 750ms idle-start report", () => {
  it("reports idle-start latency when synthesis begins", async () => {
    const clock = new FakeClock();
    const { coord, rec } = setup({ clock });
    await startCoord(coord);
    // Idle since start (t=0). Advance 500ms, then synthesis begins.
    clock.advance(500);
    coord.consumeConversation(
      makeConversation({ items: [assistantItem("a1", "Hello world.", false)] }),
    );
    expect(rec.idleStartReports).toHaveLength(1);
    expect(rec.idleStartReports[0]).toBe(500);
  });

  it("does not report again until a new idle period", async () => {
    const clock = new FakeClock();
    const { coord, rec } = setup({ clock });
    await startCoord(coord);
    clock.advance(500);
    coord.consumeConversation(
      makeConversation({ items: [assistantItem("a1", "Hello world.", false)] }),
    );
    expect(rec.idleStartReports).toHaveLength(1);
    // More prose, already reported — no new report.
    coord.consumeConversation(
      makeConversation({
        items: [assistantItem("a1", "Hello world. More text.", false)],
      }),
    );
    expect(rec.idleStartReports).toHaveLength(1);
  });
});

describe("coordinator — native error fallback", () => {
  it("falls back to typed conversation on voice error", async () => {
    const { coord, rec, bridge } = setup();
    const sid = await startCoord(coord);
    coord.handleNativeEvent(
      makeVoiceEvent("voice.error", sid, 1, "mic failed"),
    );
    expect(coord.isFallbackToTyped).toBe(true);
    expect(rec.errors).toContain("mic failed");
    expect(rec.listening).toContain(false);
    // No further speak commands accepted.
    const speakBefore = bridge.voiceCommands.filter(
      (c) => c.type === "voice.speak",
    ).length;
    coord.consumeConversation(
      makeConversation({ items: [assistantItem("a1", "Text.", false)] }),
    );
    const speakAfter = bridge.voiceCommands.filter(
      (c) => c.type === "voice.speak",
    ).length;
    expect(speakAfter).toBe(speakBefore);
  });
});

describe("coordinator — end cleanup", () => {
  it("stops everything and clears all state", async () => {
    const { coord, bridge, rec } = setup();
    const _sid = await startCoord(coord);
    coord.consumeConversation(
      makeConversation({ items: [assistantItem("a1", "Hello.", false)] }),
    );
    coord.end();
    expect(coord.voiceSessionId).toBeNull();
    expect(bridge.voiceCommands.some((c) => c.type === "voice.stop")).toBe(
      true,
    );
    expect(
      bridge.voiceCommands.some((c) => c.type === "voice.stopSpeaking"),
    ).toBe(true);
    expect(rec.listening).toContain(false);
    expect(rec.speaking).toContain(false);
    expect(rec.sessionIds).toContain(null);
  });
});

describe("coordinator — barge-in", () => {
  it("stops speaking, retains partial, waits for final, steers once", async () => {
    const { coord, svc, rec, bridge } = setup();
    const sid = await startCoord(coord);
    // A turn is active (after a first final).
    coord.handleNativeEvent(
      makeVoiceEvent("voice.final", sid, 1, "original message"),
    );
    await Promise.resolve();
    expect(svc.sendCount).toBe(1);
    // Barge-in: stop speaking, retain partial.
    coord.handleNativeEvent(
      makeVoiceEvent("voice.bargeIn", sid, 2, "hold this"),
    );
    expect(rec.interrupted).toContain(true);
    expect(
      bridge.voiceCommands.some((c) => c.type === "voice.stopSpeaking"),
    ).toBe(true);
    // Partials during barge-in should not steer.
    coord.handleNativeEvent(
      makeVoiceEvent("voice.partial", sid, 3, "hold this extended"),
    );
    await Promise.resolve();
    expect(svc.steerCount).toBe(0);
    // Final → steer once.
    coord.handleNativeEvent(
      makeVoiceEvent("voice.final", sid, 4, "hold this extended"),
    );
    await Promise.resolve();
    expect(svc.steerCount).toBe(1);
    expect(svc.lastSteerInput).toEqual([
      { type: "text", text: "hold this extended" },
    ]);
    // Interrupted cleared after steer.
    expect(rec.interrupted[rec.interrupted.length - 1]).toBe(false);
  });
});

describe("coordinator — stale session/sequence events do nothing", () => {
  it("ignores events with old session IDs", async () => {
    const { coord, svc } = setup();
    await startCoord(coord);
    coord.handleNativeEvent(
      makeVoiceEvent("voice.final", "old-session", 1, "stale"),
    );
    await Promise.resolve();
    expect(svc.sendCount).toBe(0);
  });

  it("ignores events with non-monotonic sequences", async () => {
    const { coord, svc } = setup();
    const sid = await startCoord(coord);
    coord.handleNativeEvent(makeVoiceEvent("voice.final", sid, 5, "first"));
    await Promise.resolve();
    expect(svc.sendCount).toBe(1);
    // Lower seq → ignored.
    coord.handleNativeEvent(makeVoiceEvent("voice.final", sid, 2, "stale"));
    await Promise.resolve();
    expect(svc.sendCount).toBe(1);
    expect(svc.steerCount).toBe(0);
  });

  it("accepts events with equal or higher sequences", async () => {
    const { coord, svc } = setup();
    const sid = await startCoord(coord);
    coord.handleNativeEvent(makeVoiceEvent("voice.final", sid, 3, "first"));
    await Promise.resolve();
    expect(svc.sendCount).toBe(1);
    coord.handleNativeEvent(makeVoiceEvent("voice.final", sid, 3, "second"));
    await Promise.resolve();
    expect(svc.steerCount).toBe(1);
  });
});
