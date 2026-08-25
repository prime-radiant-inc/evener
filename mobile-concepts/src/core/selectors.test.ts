import { describe, expect, it } from "vitest";
import type { PersistedPreferencesV1 } from "./persistence";
import { createInitialState, reducePrototype } from "./reducer";
import {
  selectCurrentVoiceLevel,
  selectCurrentVoiceStep,
  selectGroupedSessions,
  selectNewSessionValidity,
  selectQuestionSubmitValidity,
  selectSearchProjection,
} from "./selectors";
import type { PrototypeAction, PrototypeState } from "./state";

const preferences: PersistedPreferencesV1 = {
  version: 1,
  concept: "stillwater",
  appearance: "system",
  textScale: "standard",
  reducedMotion: false,
  scenario: "baseline",
};

function initial(): PrototypeState {
  return createInitialState({ platform: "ios", preferences });
}

function dispatchAll(
  state: PrototypeState,
  ...actions: PrototypeAction[]
): PrototypeState {
  return actions.reduce(reducePrototype, state);
}

describe("shared prototype selectors", () => {
  it("groups and filters sessions by title and project without empty groups", () => {
    expect(
      selectGroupedSessions(initial()).map(({ id, label, sessions }) => ({
        id,
        label,
        sessions: sessions.map(({ id: sessionId }) => sessionId),
      })),
    ).toEqual([
      {
        id: "needs-you",
        label: "Needs You",
        sessions: ["session-mobile-release", "session-pairing-review"],
      },
      {
        id: "running",
        label: "Running",
        sessions: ["session-native-client", "session-roster-latency"],
      },
      {
        id: "recent",
        label: "Recent",
        sessions: ["session-pairing-pr"],
      },
    ]);

    const filtered = reducePrototype(initial(), {
      type: "setSessionQuery",
      value: "  PRIME-RADIANT  ",
    });
    expect(
      selectGroupedSessions(filtered).map(({ id, sessions }) => ({
        id,
        sessions: sessions.map(({ id: sessionId }) => sessionId),
      })),
    ).toEqual([
      { id: "needs-you", sessions: ["session-pairing-review"] },
      { id: "recent", sessions: ["session-pairing-pr"] },
    ]);
  });

  it("projects Search prompt, contextual results with focus data, and no-results", () => {
    const base = initial();
    expect(selectSearchProjection(base)).toEqual({
      kind: "prompt",
      query: "",
      results: [],
    });

    const matching = reducePrototype(base, {
      type: "setGlobalQuery",
      value: "transition",
    });
    expect(selectSearchProjection(matching)).toEqual({
      kind: "results",
      query: "transition",
      results: [
        {
          id: "search-task",
          kind: "task",
          title: "Verify transition table",
          context: "The incomplete draft needs correction.",
          sessionId: "session-native-client",
          focusItemId: "item-tool-verify",
        },
      ],
    });

    const unmatched = reducePrototype(base, {
      type: "setGlobalQuery",
      value: "no such fixture result",
    });
    expect(selectSearchProjection(unmatched)).toEqual({
      kind: "no-results",
      query: "no such fixture result",
      results: [],
    });
    expect(unmatched.projection.fixture.search).toBe(
      base.projection.fixture.search,
    );
  });

  it("exposes the exact question submit validity consumed by the reducer", () => {
    const state = initial();
    expect(
      selectQuestionSubmitValidity(state, "question-release-focus", "answer"),
    ).toBe(false);
    expect(
      selectQuestionSubmitValidity(state, "question-release-focus", "fallback"),
    ).toBe(true);
    expect(
      selectQuestionSubmitValidity(state, "question-release-focus", "skip"),
    ).toBe(false);

    const selected = reducePrototype(state, {
      type: "setQuestionOption",
      questionId: "question-release-focus",
      optionId: "option-navigation",
      selected: true,
    });
    expect(
      selectQuestionSubmitValidity(
        selected,
        "question-release-focus",
        "answer",
      ),
    ).toBe(true);
    const submitted = reducePrototype(selected, {
      type: "resolveQuestion",
      questionId: "question-release-focus",
      resolution: "answer",
    });
    expect(
      selectQuestionSubmitValidity(
        submitted,
        "question-release-focus",
        "answer",
      ),
    ).toBe(false);
  });

  it("exposes New Session validity across project, prompt, model, and effort", () => {
    expect(selectNewSessionValidity(initial())).toBe(false);
    const valid = dispatchAll(
      initial(),
      { type: "setNewSessionProject", value: "/workspace/aurora" },
      { type: "setNewSessionPrompt", value: "  Review this fixture  " },
    );
    expect(selectNewSessionValidity(valid)).toBe(true);
    expect(
      selectNewSessionValidity({
        ...valid,
        newSession: { ...valid.newSession, modelId: "missing" },
      }),
    ).toBe(false);
    expect(
      selectNewSessionValidity({
        ...valid,
        newSession: {
          ...valid.newSession,
          effort: "future" as PrototypeState["newSession"]["effort"],
        },
      }),
    ).toBe(false);
  });

  it("selects the current deterministic voice step and level", () => {
    const state = dispatchAll(
      initial(),
      { type: "openVoice", sessionId: "session-native-client" },
      { type: "advanceVoice" },
      { type: "advanceVoice" },
    );
    expect(selectCurrentVoiceStep(state)).toEqual({
      id: "voice-listening",
      state: "listening",
      caption: "Listening",
      level: 62,
    });
    expect(selectCurrentVoiceLevel(state)).toBe(62);
    expect(
      selectCurrentVoiceLevel({
        ...state,
        voice: { ...state.voice, stepIndex: 999 },
      }),
    ).toBe(0);
  });
});
