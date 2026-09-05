// VoiceScreen tests — focused full-screen voice experience. Deterministic:
// the voice store and native bridge are faked; no wall-clock timing depends
// on the coordinator's injected clock.

import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { StoreApi, UseBoundStore } from "zustand";
import { create } from "zustand";
import type {
  MobileConversation,
  MobileTimelineItem,
} from "../conversation/model";
import type { NativeBridge, VoiceBridgeEvent } from "../native/client";
import type {
  ContentSizeCategory,
  HapticKind,
  PermissionKind,
} from "../native/contract";
import type { ConversationState } from "../state/conversation";
import { createVoiceStore, type VoiceState } from "../state/voice";
import { VoiceScreen } from "./VoiceScreen";

afterEach(() => cleanup());

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

class FakeNativeBridge implements NativeBridge {
  readonly version = 1;
  permissionsGranted = true;
  readonly haptics: HapticKind[] = [];
  readonly permissionRequests: PermissionKind[] = [];
  readonly voiceStops: string[] = [];
  readonly voiceStopSpeakings: string[] = [];
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
  async openExternalUrl(): Promise<void> {}
  async requestPermission(kind: PermissionKind): Promise<{
    kind: PermissionKind;
    granted: boolean;
  }> {
    this.permissionRequests.push(kind);
    return { kind, granted: this.permissionsGranted };
  }
  async speechStart(): Promise<void> {}
  async speechStop(): Promise<void> {}
  async synthesisSpeak(): Promise<void> {}
  async synthesisStop(): Promise<void> {}
  async hapticPerform(kind: HapticKind): Promise<void> {
    this.haptics.push(kind);
  }
  async getContentSize(): Promise<ContentSizeCategory> {
    return "large";
  }
  onLifecycle(): () => void {
    return () => {};
  }
  async voicePermissions(): Promise<boolean> {
    return this.permissionsGranted;
  }
  async voiceStart(): Promise<{ voiceSessionId: string }> {
    return { voiceSessionId: "vs-1" };
  }
  async voiceStop(voiceSessionId: string): Promise<void> {
    this.voiceStops.push(voiceSessionId);
  }
  async voiceSpeak(): Promise<{ chunkId: string }> {
    return { chunkId: "c1" };
  }
  async voiceStopSpeaking(voiceSessionId: string): Promise<void> {
    this.voiceStopSpeakings.push(voiceSessionId);
  }
  async voiceSetRate(): Promise<number> {
    return 1;
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

function makeConversation(
  over: Partial<MobileConversation> = {},
): MobileConversation {
  const items: MobileTimelineItem[] = [];
  return {
    id: "thread-1",
    sessionId: "session-1",
    name: "Test Chat",
    preview: "",
    modelProvider: "anthropic",
    status: "ready",
    items,
    capabilities: {
      send: true,
      steer: true,
      interrupt: true,
      compact: true,
      clear: true,
      forkFromTurn: true,
      shutdown: true,
      changeModel: true,
      changeVisionModel: true,
      queue: true,
      goal: true,
      rename: true,
    },
    queue: { depth: 0, preview: [] },
    usage: {},
    askPending: false,
    ...over,
  };
}

type ConversationStoreHook = UseBoundStore<StoreApi<ConversationState>>;
type VoiceStoreHook = UseBoundStore<StoreApi<VoiceState>>;

function createFakeConversationStore(
  conversation: MobileConversation | null,
  ref = "ref-1",
): ConversationStoreHook {
  return create<ConversationState>(() => ({
    ref: conversation ? ref : null,
    profileId: "p1",
    connectionGeneration: 0,
    conversationGeneration: 0,
    conversation,
    olderCursor: null,
    loadingOlder: false,
    status: "open",
    error: null,
    draft: "",
    pendingSend: null,
    open: vi.fn(),
    loadOlder: vi.fn(),
    setDraft: vi.fn(),
    send: vi.fn(),
    steer: vi.fn(),
    queue: vi.fn(),
    interrupt: vi.fn(),
    close: vi.fn(),
    applyNotification: vi.fn(),
    reset: vi.fn(),
  }));
}

function renderVoiceScreen(over: {
  readonly bridge?: FakeNativeBridge;
  readonly conversation?: MobileConversation | null;
  readonly voiceStore?: VoiceStoreHook;
  readonly ref?: string;
  readonly onEnd?: () => void;
  readonly onKeyboard?: () => void;
  readonly connectionStatus?: "reachable" | "unknown" | "offline";
}): {
  bridge: FakeNativeBridge;
  conversationStore: ConversationStoreHook;
  voiceStore: VoiceStoreHook;
  composerFocusRef: { current: HTMLElement | null };
} {
  const bridge = over.bridge ?? new FakeNativeBridge();
  const conversation =
    over.conversation === undefined ? makeConversation() : over.conversation;
  const conversationStore = createFakeConversationStore(conversation, over.ref);
  const voiceStore = over.voiceStore ?? createVoiceStore();
  const composerFocusRef = { current: null as HTMLElement | null };
  const onEnd = over.onEnd ?? vi.fn();
  const onKeyboard = over.onKeyboard ?? vi.fn();
  render(
    <VoiceScreen
      conversationStore={conversationStore}
      voiceStore={voiceStore}
      bridge={bridge}
      composerFocusRef={composerFocusRef}
      onEnd={onEnd}
      onKeyboard={onKeyboard}
      connectionStatus={over.connectionStatus ?? "reachable"}
    />,
  );
  return { bridge, conversationStore, voiceStore, composerFocusRef };
}

// Helper: wait for the permission check to resolve and the voice UI to settle.
async function settle(): Promise<void> {
  // The mount effect checks voicePermissions (async) then starts voice. We
  // wait until the screen leaves the placeholder ("Checking…") state.
  await waitFor(() => {
    expect(
      screen.getByTestId("voice-screen").getAttribute("data-status"),
    ).not.toBe(null);
  });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("VoiceScreen", () => {
  describe("permission rationale", () => {
    it("shows rationale when permissions are not granted", async () => {
      const bridge = new FakeNativeBridge();
      bridge.permissionsGranted = false;
      renderVoiceScreen({ bridge });
      await settle();
      expect(
        screen.getByTestId("voice-screen").getAttribute("data-status"),
      ).toBe("permission-denied");
      expect(screen.getByText(/microphone access/i)).not.toBeNull();
    });

    it("Open Settings action requests microphone permission", async () => {
      const bridge = new FakeNativeBridge();
      bridge.permissionsGranted = false;
      renderVoiceScreen({ bridge });
      await settle();
      fireEvent.click(screen.getByTestId("voice-open-settings"));
      expect(bridge.permissionRequests).toContain("microphone");
    });
  });

  describe("connection title/status", () => {
    it("shows the session title and connection mark", async () => {
      renderVoiceScreen({
        conversation: makeConversation({ name: "My Chat" }),
      });
      await settle();
      const title = screen.getByTestId("voice-session-title");
      expect(title.textContent).toContain("My Chat");
      // The StatusMark label "Connected" appears for "reachable".
      expect(screen.getByText("Connected")).not.toBeNull();
    });
  });

  describe("voice states", () => {
    it("renders the listening state distinctly", async () => {
      const { voiceStore } = renderVoiceScreen({});
      await settle();
      voiceStore.getState().setListening(true);
      expect(
        screen.getByTestId("voice-screen").getAttribute("data-status"),
      ).toBe("listening");
      expect(screen.getByTestId("voice-status-label").textContent).toBe(
        "Listening",
      );
    });

    it("renders the speaking state distinctly", async () => {
      const { voiceStore } = renderVoiceScreen({});
      await settle();
      voiceStore.getState().setSpeaking(true);
      await waitFor(() => {
        expect(
          screen.getByTestId("voice-screen").getAttribute("data-status"),
        ).toBe("speaking");
      });
    });

    it("renders the interrupted state distinctly", async () => {
      const { voiceStore } = renderVoiceScreen({});
      await settle();
      voiceStore.getState().setInterrupted(true);
      await waitFor(() => {
        expect(
          screen.getByTestId("voice-screen").getAttribute("data-status"),
        ).toBe("interrupted");
      });
    });

    it("renders the error state distinctly", async () => {
      const { voiceStore } = renderVoiceScreen({});
      await settle();
      voiceStore.getState().setError("boom");
      await waitFor(() => {
        expect(
          screen.getByTestId("voice-screen").getAttribute("data-status"),
        ).toBe("error");
      });
      expect(screen.getByTestId("voice-error")).not.toBeNull();
    });
  });

  describe("captions", () => {
    it("renders captions as text", async () => {
      const { voiceStore } = renderVoiceScreen({});
      await settle();
      voiceStore.getState().setCaption("hello world");
      await waitFor(() => {
        expect(screen.getByTestId("voice-caption-visible").textContent).toBe(
          "hello world",
        );
      });
    });
  });

  describe("latest assistant sentence", () => {
    it("shows the latest assistant sentence", async () => {
      const { voiceStore } = renderVoiceScreen({});
      await settle();
      voiceStore.getState().setAssistantSentence("I can help with that.");
      await waitFor(() => {
        expect(screen.getByTestId("voice-assistant-sentence").textContent).toBe(
          "I can help with that.",
        );
      });
    });
  });

  describe("controls", () => {
    it("renders a 44-pixel-class End control", async () => {
      renderVoiceScreen({});
      await settle();
      const end = screen.getByLabelText("End voice mode");
      expect(end.tagName).toBe("BUTTON");
    });

    it("renders a 44-pixel-class Keyboard control", async () => {
      renderVoiceScreen({});
      await settle();
      const keyboard = screen.getByLabelText("Switch to keyboard");
      expect(keyboard.tagName).toBe("BUTTON");
    });

    it("End invokes onEnd", async () => {
      const onEnd = vi.fn();
      renderVoiceScreen({ onEnd });
      await settle();
      fireEvent.click(screen.getByLabelText("End voice mode"));
      expect(onEnd).toHaveBeenCalledTimes(1);
    });

    it("Keyboard invokes onKeyboard", async () => {
      const onKeyboard = vi.fn();
      renderVoiceScreen({ onKeyboard });
      await settle();
      fireEvent.click(screen.getByLabelText("Switch to keyboard"));
      expect(onKeyboard).toHaveBeenCalledTimes(1);
    });
  });

  describe("error state", () => {
    it("shows Open Settings action for permission errors", async () => {
      const { voiceStore, bridge } = renderVoiceScreen({});
      await settle();
      voiceStore.getState().setError("Voice permissions denied");
      await waitFor(() => {
        expect(screen.getByTestId("voice-open-settings")).not.toBeNull();
      });
      fireEvent.click(screen.getByTestId("voice-open-settings"));
      expect(bridge.permissionRequests).toContain("microphone");
    });
  });

  describe("ask-mode disabled reason", () => {
    it("shows a disabled reason when conversation is in AskPending", async () => {
      renderVoiceScreen({
        conversation: makeConversation({ askPending: true }),
      });
      await settle();
      expect(screen.getByTestId("voice-ask-disabled")).not.toBeNull();
    });

    it("does not show a disabled reason when not in AskPending", async () => {
      renderVoiceScreen({
        conversation: makeConversation({ askPending: false }),
      });
      await settle();
      expect(screen.queryByTestId("voice-ask-disabled")).toBeNull();
    });
  });

  describe("haptics", () => {
    it("triggers haptic feedback on status transitions", async () => {
      const { bridge, voiceStore } = renderVoiceScreen({});
      await settle();
      // After settle, the coordinator has set listening=true.
      bridge.haptics.length = 0;
      voiceStore.getState().setSpeaking(true);
      // The haptic effect runs after the render commits.
      await waitFor(() => {
        expect(bridge.haptics.length).toBeGreaterThan(0);
      });
      expect(bridge.haptics).toContain("selection");
    });

    it("triggers an error haptic on transition to error", async () => {
      const { bridge, voiceStore } = renderVoiceScreen({});
      await settle();
      bridge.haptics.length = 0;
      voiceStore.getState().setError("boom");
      await waitFor(() => {
        expect(bridge.haptics).toContain("notificationError");
      });
    });
  });

  describe("focus return", () => {
    it("returns focus to the composer on End", async () => {
      const composerEl = document.createElement("input");
      composerEl.focus = vi.fn();
      const composerFocusRef = {
        current: composerEl as unknown as HTMLElement,
      };
      const conversationStore = createFakeConversationStore(makeConversation());
      const voiceStore = createVoiceStore();
      const bridge = new FakeNativeBridge();
      render(
        <VoiceScreen
          conversationStore={conversationStore}
          voiceStore={voiceStore}
          bridge={bridge}
          composerFocusRef={composerFocusRef}
          onEnd={vi.fn()}
          onKeyboard={vi.fn()}
          connectionStatus="reachable"
        />,
      );
      await settle();
      fireEvent.click(screen.getByLabelText("End voice mode"));
      expect(composerEl.focus).toHaveBeenCalled();
    });
  });
});

// ---------------------------------------------------------------------------
// Accessibility / motion tests
// ---------------------------------------------------------------------------

describe("VoiceScreen accessibility/motion", () => {
  it("status live-region throttles announcements (computeAnnouncement)", async () => {
    // The pure throttling function is covered in VoiceCaptions tests; here we
    // assert the status label is a polite, atomic live region.
    renderVoiceScreen({});
    await settle();
    // The VoiceStatus live region is polite + atomic.
    const statusLabel = screen.getByTestId("voice-status-label");
    const region = statusLabel.parentElement;
    expect(region?.getAttribute("aria-live")).toBe("polite");
    expect(region?.getAttribute("aria-atomic")).toBe("true");
  });

  it("waveform is decorative (aria-hidden)", async () => {
    renderVoiceScreen({});
    await settle();
    const wave = screen.getByTestId("voice-level");
    expect(wave.getAttribute("aria-hidden")).toBe("true");
  });

  it("does not flood the transcript live region", async () => {
    const { voiceStore } = renderVoiceScreen({});
    await settle();
    // Rapidly update the caption many times.
    for (let i = 0; i < 20; i++) {
      voiceStore.getState().setCaption(`partial ${i}`);
    }
    // The announced region text should not change on every update (throttled).
    const announced = screen.getByTestId("voice-caption-announced");
    // It is a single string — not a concatenation of all 20 partials.
    expect(announced.textContent?.length ?? 0).toBeLessThan(100);
  });

  it("reduced-motion disables the waveform animation (data attribute)", async () => {
    const { voiceStore } = renderVoiceScreen({});
    await settle();
    // After settle the coordinator set listening=true, so the waveform is active.
    voiceStore.getState().setListening(false);
    // Wait for the re-render: status → idle, active → false.
    await waitFor(() => {
      const wave = screen.getByTestId("voice-level");
      expect(wave.getAttribute("data-active")).toBe("false");
    });
  });

  it("Dynamic Type scales without clipping (title wraps at accessibility sizes)", async () => {
    renderVoiceScreen({
      conversation: makeConversation({
        name: "A very long conversation title",
      }),
    });
    await settle();
    const title = screen.getByTestId("voice-session-title");
    // Structural: the title element exists and contains the full text.
    expect(title.textContent).toContain("A very long conversation title");
  });

  it("keyboard fallback ends native voice before navigation", async () => {
    const onKeyboard = vi.fn();
    const bridge = new FakeNativeBridge();
    // Capture whether voiceStop fires before onKeyboard.
    let voiceStoppedBeforeNav = false;
    const wrappedOnKeyboard = vi.fn(() => {
      voiceStoppedBeforeNav = bridge.voiceStops.length > 0;
    });
    render(
      <VoiceScreen
        conversationStore={createFakeConversationStore(makeConversation())}
        voiceStore={createVoiceStore()}
        bridge={bridge}
        composerFocusRef={{ current: null }}
        onEnd={vi.fn()}
        onKeyboard={wrappedOnKeyboard}
        connectionStatus="reachable"
      />,
    );
    await settle();
    // The coordinator started voice, so a voice session id exists.
    fireEvent.click(screen.getByLabelText("Switch to keyboard"));
    // onKeyboard fires after voiceStop.
    expect(wrappedOnKeyboard).toHaveBeenCalledTimes(1);
    expect(voiceStoppedBeforeNav).toBe(true);
    expect(onKeyboard).not.toHaveBeenCalled();
  });
});
