/**
 * VoiceStore — app-facing voice state.
 *
 * A Zustand store following the pattern in conversation.ts. It exposes
 * selectors the React layer reads (captions/status/latest assistant
 * sentence/level) and is driven by VoiceCoordinatorCallbacks. The store holds
 * no native bridge or conversation service references — it is a pure state
 * projection updated by the coordinator via callbacks.
 */

import { create } from "zustand";

export type VoiceStatus =
  | "idle"
  | "listening"
  | "speaking"
  | "interrupted"
  | "error"
  | "disabled";

export interface VoiceState {
  readonly status: VoiceStatus;
  readonly voiceSessionId: string | null;
  readonly caption: string;
  readonly latestAssistantSentence: string;
  readonly level: number;
  readonly error: string | null;

  // Selectors
  readonly isListening: boolean;
  readonly isSpeaking: boolean;
  readonly isInterrupted: boolean;
  readonly hasError: boolean;
  readonly captions: string;

  // Mutators (called by VoiceCoordinator callbacks)
  setListening(active: boolean): void;
  setSpeaking(active: boolean): void;
  setInterrupted(interrupted: boolean): void;
  setError(message: string): void;
  clearError(): void;
  setCaption(text: string): void;
  setLevel(level: number): void;
  setAssistantSentence(text: string): void;
  setVoiceSessionId(id: string | null): void;
  reset(): void;
}

export function createVoiceStore() {
  return create<VoiceState>((set) => ({
    status: "idle",
    voiceSessionId: null,
    caption: "",
    latestAssistantSentence: "",
    level: 0,
    error: null,

    isListening: false,
    isSpeaking: false,
    isInterrupted: false,
    hasError: false,
    captions: "",

    setListening(active) {
      set((s) => ({
        isListening: active,
        status: active
          ? "listening"
          : s.isSpeaking
            ? "speaking"
            : s.isInterrupted
              ? "interrupted"
              : s.error !== null
                ? "error"
                : "idle",
      }));
    },

    setSpeaking(active) {
      set((s) => ({
        isSpeaking: active,
        status: active
          ? "speaking"
          : s.isListening
            ? "listening"
            : s.isInterrupted
              ? "interrupted"
              : s.error !== null
                ? "error"
                : "idle",
      }));
    },

    setInterrupted(interrupted) {
      set((s) => ({
        isInterrupted: interrupted,
        status: interrupted
          ? "interrupted"
          : s.isListening
            ? "listening"
            : s.isSpeaking
              ? "speaking"
              : s.error !== null
                ? "error"
                : "idle",
      }));
    },

    setError(message) {
      set({
        error: message,
        hasError: true,
        status: "error",
      });
    },

    clearError() {
      set((s) => ({
        error: null,
        hasError: false,
        status: s.isListening
          ? "listening"
          : s.isSpeaking
            ? "speaking"
            : s.isInterrupted
              ? "interrupted"
              : "idle",
      }));
    },

    setCaption(text) {
      set({ caption: text, captions: text });
    },

    setLevel(level) {
      set({ level });
    },

    setAssistantSentence(text) {
      set({ latestAssistantSentence: text });
    },

    setVoiceSessionId(id) {
      set({ voiceSessionId: id });
    },

    reset() {
      set({
        status: "idle",
        voiceSessionId: null,
        caption: "",
        latestAssistantSentence: "",
        level: 0,
        error: null,
        isListening: false,
        isSpeaking: false,
        isInterrupted: false,
        hasError: false,
        captions: "",
      });
    },
  }));
}
