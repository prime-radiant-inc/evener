/**
 * VoiceStore tests — state transitions, selectors, and cleanup on end.
 */

import { describe, expect, it } from "vitest";
import { createVoiceStore } from "./voice";

describe("voice store — state transitions through all phases", () => {
  it("transitions idle → listening → speaking → interrupted → error → idle", () => {
    const store = createVoiceStore();
    expect(store.getState().status).toBe("idle");

    store.getState().setVoiceSessionId("vs-1");
    expect(store.getState().voiceSessionId).toBe("vs-1");

    store.getState().setListening(true);
    expect(store.getState().status).toBe("listening");
    expect(store.getState().isListening).toBe(true);

    store.getState().setSpeaking(true);
    expect(store.getState().status).toBe("speaking");
    expect(store.getState().isSpeaking).toBe(true);

    store.getState().setInterrupted(true);
    expect(store.getState().status).toBe("interrupted");
    expect(store.getState().isInterrupted).toBe(true);

    store.getState().setError("mic failed");
    expect(store.getState().status).toBe("error");
    expect(store.getState().error).toBe("mic failed");

    store.getState().clearError();
    // After clearing error, returns to listening (still listening).
    expect(store.getState().status).toBe("listening");

    store.getState().setListening(false);
    store.getState().setSpeaking(false);
    store.getState().setInterrupted(false);
    expect(store.getState().status).toBe("idle");
  });

  it("caption and assistant sentence update independently", () => {
    const store = createVoiceStore();
    store.getState().setCaption("partial transcript");
    expect(store.getState().captions).toBe("partial transcript");
    expect(store.getState().caption).toBe("partial transcript");

    store.getState().setAssistantSentence("Spoken sentence.");
    expect(store.getState().latestAssistantSentence).toBe("Spoken sentence.");
  });

  it("level updates", () => {
    const store = createVoiceStore();
    store.getState().setLevel(0.5);
    expect(store.getState().level).toBe(0.5);
    store.getState().setLevel(0);
    expect(store.getState().level).toBe(0);
  });
});

describe("voice store — selectors return correct values", () => {
  it("isListening, isSpeaking, isInterrupted, hasError reflect state", () => {
    const store = createVoiceStore();

    expect(store.getState().isListening).toBe(false);
    expect(store.getState().isSpeaking).toBe(false);
    expect(store.getState().isInterrupted).toBe(false);
    expect(store.getState().hasError).toBe(false);
    expect(store.getState().captions).toBe("");
    expect(store.getState().level).toBe(0);
    expect(store.getState().voiceSessionId).toBeNull();
    expect(store.getState().latestAssistantSentence).toBe("");

    store.getState().setListening(true);
    expect(store.getState().isListening).toBe(true);

    store.getState().setSpeaking(true);
    expect(store.getState().isSpeaking).toBe(true);

    store.getState().setInterrupted(true);
    expect(store.getState().isInterrupted).toBe(true);

    store.getState().setError("err");
    expect(store.getState().hasError).toBe(true);
  });
});

describe("voice store — cleanup on end", () => {
  it("reset() clears all state", () => {
    const store = createVoiceStore();
    store.getState().setVoiceSessionId("vs-1");
    store.getState().setListening(true);
    store.getState().setSpeaking(true);
    store.getState().setCaption("transcript");
    store.getState().setAssistantSentence("sentence");
    store.getState().setLevel(0.8);
    store.getState().setError("err");

    store.getState().reset();

    const s = store.getState();
    expect(s.status).toBe("idle");
    expect(s.voiceSessionId).toBeNull();
    expect(s.caption).toBe("");
    expect(s.captions).toBe("");
    expect(s.latestAssistantSentence).toBe("");
    expect(s.level).toBe(0);
    expect(s.error).toBeNull();
    expect(s.isListening).toBe(false);
    expect(s.isSpeaking).toBe(false);
    expect(s.isInterrupted).toBe(false);
    expect(s.hasError).toBe(false);
  });
});
