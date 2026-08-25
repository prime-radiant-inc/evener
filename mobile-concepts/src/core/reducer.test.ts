import { describe, expect, it } from "vitest";
import { canonicalFixture } from "./fixtures";
import type { PersistedPreferencesV1 } from "./persistence";
import { createInitialState, reducePrototype } from "./reducer";
import { projectScenario } from "./scenarios";

const preferences: PersistedPreferencesV1 = {
  version: 1,
  concept: "stillwater",
  appearance: "dark",
  textScale: "large",
  reducedMotion: true,
  scenario: "baseline",
};

function initial() {
  return createInitialState({
    platform: "ios",
    preferences,
    projection: projectScenario(canonicalFixture, "baseline"),
  });
}

function dispatchAll(
  state: ReturnType<typeof initial>,
  ...actions: Parameters<typeof reducePrototype>[1][]
) {
  return actions.reduce(reducePrototype, state);
}

describe("prototype reducer", () => {
  it("creates canonical gallery or persisted-concept initial routes", () => {
    expect(initial()).toMatchObject({
      route: { kind: "root", tab: "sessions" },
      history: [],
      draft: "",
      resetGeneration: 0,
    });
    expect(
      createInitialState({
        platform: "android",
        preferences: { ...preferences, concept: null },
      }),
    ).toMatchObject({ platform: "android", route: { kind: "gallery" } });
  });

  it("selectConcept preserves every cross-concept interaction field", () => {
    const before = dispatchAll(
      initial(),
      { type: "openSession", sessionId: "session-native-client" },
      { type: "setSessionQuery", value: "native" },
      { type: "setGlobalQuery", value: "voice" },
      { type: "setDraft", value: "keep this" },
      {
        type: "setQuestionOption",
        questionId: "question-release-focus",
        optionId: "option-navigation",
        selected: true,
      },
      { type: "toggleTool", itemId: "item-tool-inspect" },
      { type: "toggleWork", nodeId: "work-task-shell" },
      { type: "openVoice", sessionId: "session-native-client" },
      { type: "advanceVoice" },
    );
    const after = reducePrototype(before, {
      type: "selectConcept",
      concept: "constellation",
    });

    expect(after.concept).toBe("constellation");
    for (const key of [
      "route",
      "scenario",
      "sessionQuery",
      "globalQuery",
      "draft",
      "answers",
      "expandedToolIds",
      "expandedWorkIds",
      "voice",
    ] as const) {
      expect(after[key]).toBe(before[key]);
    }
  });

  it("setScenario rebuilds interactions and preserves reviewer display preferences", () => {
    const dirty = dispatchAll(
      initial(),
      { type: "setDraft", value: "discard" },
      { type: "toggleTool", itemId: "item-tool-inspect" },
      { type: "setSessionQuery", value: "discard" },
    );
    const next = reducePrototype(dirty, {
      type: "setScenario",
      scenario: "empty",
    });
    expect(next).toMatchObject({
      concept: "stillwater",
      scenario: "empty",
      appearance: "dark",
      textScale: "large",
      reducedMotion: true,
      draft: "",
      sessionQuery: "",
    });
    expect(next.expandedToolIds.size).toBe(0);
    expect(next.projection.screenState).toBe("empty");
    expect(next.projection.fixture.sessions).toEqual([]);
  });

  it("always rebuilds scenarios and reset from the immutable unprojected source", () => {
    const baseline = initial();
    expect(Object.isFrozen(baseline.sourceFixture)).toBe(true);
    expect(Object.isFrozen(baseline.sourceFixture.sessions)).toBe(true);

    const empty = reducePrototype(baseline, {
      type: "setScenario",
      scenario: "empty",
    });
    const restored = reducePrototype(empty, {
      type: "setScenario",
      scenario: "baseline",
    });
    expect(restored.projection.fixture).toEqual(canonicalFixture);

    const offline = reducePrototype(restored, {
      type: "setScenario",
      scenario: "offline",
    });
    expect(offline.projection.fixture.sessions[0]?.updatedLabel).toBe(
      `stale · ${canonicalFixture.sessions[0]?.updatedLabel}`,
    );
    expect(
      reducePrototype(offline, { type: "setScenario", scenario: "offline" }),
    ).toBe(offline);

    const online = reducePrototype(offline, {
      type: "setScenario",
      scenario: "baseline",
    });
    expect(online.projection.fixture.sessions).toEqual(
      canonicalFixture.sessions,
    );
    expect(online.projection.fixture.hubs).toEqual(canonicalFixture.hubs);

    const reset = reducePrototype(empty, { type: "reset" });
    expect(reset.projection.fixture).toEqual(canonicalFixture);
    expect(reset.sourceFixture).toBe(baseline.sourceFixture);
  });

  it("treats root tabs as depth-zero replacement destinations", () => {
    const root = initial();
    expect(
      reducePrototype(root, { type: "navigateRoot", tab: "sessions" }),
    ).toBe(root);

    const conversation = reducePrototype(root, {
      type: "openSession",
      sessionId: "session-native-client",
      focusItemId: "item-assistant-plan",
    });
    const search = reducePrototype(conversation, {
      type: "navigateRoot",
      tab: "search",
    });
    expect(search).toMatchObject({
      route: { kind: "root", tab: "search" },
      history: [],
      selectedSessionId: null,
      focusedItemId: null,
    });
    expect(
      reducePrototype(search, { type: "navigateRoot", tab: "search" }),
    ).toBe(search);
  });

  it("keeps route, selected session, and focus synchronized through every push and pop", () => {
    const root = initial();
    const conversationA = reducePrototype(root, {
      type: "openSession",
      sessionId: "session-native-client",
      focusItemId: "item-assistant-plan",
    });
    expect(conversationA).toMatchObject({
      selectedSessionId: "session-native-client",
      focusedItemId: "item-assistant-plan",
    });

    const workB = reducePrototype(conversationA, {
      type: "openWork",
      sessionId: "session-pairing-review",
    });
    expect(workB).toMatchObject({
      route: { kind: "work", sessionId: "session-pairing-review" },
      selectedSessionId: "session-pairing-review",
      focusedItemId: null,
    });

    const voiceB = reducePrototype(workB, {
      type: "openVoice",
      sessionId: "session-pairing-review",
    });
    expect(voiceB).toMatchObject({
      route: { kind: "voice", sessionId: "session-pairing-review" },
      selectedSessionId: "session-pairing-review",
      focusedItemId: null,
    });

    const backToWork = reducePrototype(voiceB, { type: "goBack" });
    expect(backToWork).toMatchObject({
      route: { kind: "work", sessionId: "session-pairing-review" },
      selectedSessionId: "session-pairing-review",
      focusedItemId: null,
    });
    const backToConversation = reducePrototype(backToWork, { type: "goBack" });
    expect(backToConversation).toMatchObject({
      route: {
        kind: "conversation",
        sessionId: "session-native-client",
        focusItemId: "item-assistant-plan",
      },
      selectedSessionId: "session-native-client",
      focusedItemId: "item-assistant-plan",
    });
    const backToRoot = reducePrototype(backToConversation, { type: "goBack" });
    expect(backToRoot).toMatchObject({
      route: { kind: "root", tab: "sessions" },
      history: [],
      selectedSessionId: null,
      focusedItemId: null,
    });
  });

  it("rejects missing destination references and invalid conversation focus", () => {
    const state = initial();
    for (const action of [
      { type: "openSession", sessionId: "missing" },
      {
        type: "openSession",
        sessionId: "session-native-client",
        focusItemId: "missing",
      },
      { type: "openWork", sessionId: "missing" },
      { type: "openVoice", sessionId: "missing" },
      { type: "openSearchResult", resultId: "missing" },
    ] as const) {
      expect(reducePrototype(state, action)).toBe(state);
    }
  });

  it("pushes and pops routes in order and closes overlays first", () => {
    const root = initial();
    const conversation = reducePrototype(root, {
      type: "openSession",
      sessionId: "session-native-client",
    });
    const work = reducePrototype(conversation, {
      type: "openWork",
      sessionId: "session-native-client",
    });
    const covered = reducePrototype(work, {
      type: "openOverlay",
      overlay: "lab-controls",
    });
    const uncovered = reducePrototype(covered, { type: "goBack" });
    expect(uncovered.route).toEqual(work.route);
    expect(uncovered.overlay).toBeNull();
    const backConversation = reducePrototype(uncovered, { type: "goBack" });
    expect(backConversation.route).toEqual(conversation.route);
    expect(reducePrototype(backConversation, { type: "goBack" }).route).toEqual(
      root.route,
    );
  });

  it("uses the same goBack action on Android and for Voice", () => {
    const android = { ...initial(), platform: "android" as const };
    const voice = reducePrototype(android, {
      type: "openVoice",
      sessionId: "session-native-client",
    });
    expect(reducePrototype(voice, { type: "goBack" }).route).toEqual(
      android.route,
    );
  });

  it("refreshes only through explicit completion without changing fixtures", () => {
    const state = initial();
    const fixture = state.projection.fixture;
    const refreshing = reducePrototype(state, { type: "refreshSessions" });
    expect(refreshing.refreshState).toBe("refreshing");
    expect(refreshing.projection.fixture).toBe(fixture);
    expect(reducePrototype(state, { type: "completeRefresh" })).toBe(state);
    const complete = reducePrototype(refreshing, { type: "completeRefresh" });
    expect(complete.refreshState).toBe("complete");
    expect(complete.projection.fixture).toBe(fixture);
  });

  it("retains search result kind/context and routes a match with focus", () => {
    const state = reducePrototype(initial(), {
      type: "setGlobalQuery",
      value: "transition",
    });
    const matches = state.projection.fixture.search.filter((result) =>
      `${result.title} ${result.body}`
        .toLowerCase()
        .includes(state.globalQuery.toLowerCase()),
    );
    expect(matches.map(({ kind, body }) => ({ kind, body }))).toContainEqual({
      kind: "task",
      body: "The incomplete draft needs correction.",
    });
    const opened = reducePrototype(state, {
      type: "openSearchResult",
      resultId: "search-task",
    });
    expect(opened.route).toEqual({
      kind: "conversation",
      sessionId: "session-native-client",
      focusItemId: "item-tool-verify",
    });
    expect(opened.focusedItemId).toBe("item-tool-verify");
    expect(opened.selectedSessionId).toBe("session-native-client");
    expect(
      reducePrototype(state, { type: "openSearchResult", resultId: "missing" }),
    ).toBe(state);
  });

  it("represents empty-query prompt and unmatched query context without fixture mutation", () => {
    const empty = initial();
    expect(empty.globalQuery).toBe("");
    const unmatched = reducePrototype(empty, {
      type: "setGlobalQuery",
      value: "no such fixture result",
    });
    expect(unmatched.globalQuery).toBe("no such fixture result");
    expect(
      unmatched.projection.fixture.search.filter((result) =>
        `${result.title} ${result.body}`
          .toLowerCase()
          .includes(unmatched.globalQuery.toLowerCase()),
      ),
    ).toEqual([]);
    expect(unmatched.projection.fixture.search).toBe(
      empty.projection.fixture.search,
    );
  });

  it("toggles disclosure sets immutably and returns to the prior membership", () => {
    const state = initial();
    const toolOn = reducePrototype(state, {
      type: "toggleTool",
      itemId: "item-tool-inspect",
    });
    expect(toolOn.expandedToolIds).toEqual(new Set(["item-tool-inspect"]));
    expect(state.expandedToolIds.size).toBe(0);
    expect(
      reducePrototype(toolOn, {
        type: "toggleTool",
        itemId: "item-tool-inspect",
      }).expandedToolIds,
    ).toEqual(new Set());

    const workOn = reducePrototype(state, {
      type: "toggleWork",
      nodeId: "work-task-shell",
    });
    expect(workOn.expandedWorkIds).toEqual(new Set(["work-task-shell"]));
    expect(state.expandedWorkIds.size).toBe(0);
  });

  it("moves composer through explicit starting, complete, and running-only stop", () => {
    const state = dispatchAll(
      initial(),
      { type: "setDraft", value: "Ship it" },
      { type: "setComposerMode", mode: "queue" },
      { type: "submitComposer" },
    );
    expect(state).toMatchObject({
      composerMode: "running",
      syntheticTurn: "starting",
      draft: "",
    });
    const complete = reducePrototype(state, { type: "completeSyntheticTurn" });
    expect(complete.syntheticTurn).toBe("complete");
    expect(reducePrototype(complete, { type: "stopSyntheticTurn" })).toBe(
      complete,
    );
    const stopped = reducePrototype(state, { type: "stopSyntheticTurn" });
    expect(stopped.syntheticTurn).toBe("stopped");
  });

  it("validates question modes, option membership, notes, and allowed outcomes", () => {
    const state = initial();
    expect(
      reducePrototype(state, {
        type: "resolveQuestion",
        questionId: "question-release-focus",
        resolution: "answer",
      }),
    ).toBe(state);
    const one = reducePrototype(state, {
      type: "setQuestionOption",
      questionId: "question-release-focus",
      optionId: "option-navigation",
      selected: true,
    });
    const replacement = reducePrototype(one, {
      type: "setQuestionOption",
      questionId: "question-release-focus",
      optionId: "option-typography",
      selected: true,
    });
    expect(
      replacement.answers["question-release-focus"]?.selectedOptionIds,
    ).toEqual(["option-typography"]);
    expect(
      reducePrototype(replacement, {
        type: "resolveQuestion",
        questionId: "question-release-focus",
        resolution: "skip",
      }),
    ).toBe(replacement);
    expect(
      reducePrototype(replacement, {
        type: "resolveQuestion",
        questionId: "question-release-focus",
        resolution: "answer",
      }).answers["question-release-focus"],
    ).toMatchObject({ resolution: "answer", submitted: true });

    const noted = reducePrototype(state, {
      type: "setQuestionNote",
      questionId: "question-release-checks",
      note: "Reviewer context",
    });
    expect(noted.answers["question-release-checks"]?.note).toBe(
      "Reviewer context",
    );
    expect(
      reducePrototype(noted, {
        type: "resolveQuestion",
        questionId: "question-release-checks",
        resolution: "fallback",
      }).answers["question-release-checks"],
    ).toMatchObject({ resolution: "fallback", submitted: true });
  });

  it("keeps each required structured answer invalid until its selection is satisfied", () => {
    let state = initial();
    for (const questionId of [
      "question-release-focus",
      "question-release-checks",
    ]) {
      expect(
        reducePrototype(state, {
          type: "resolveQuestion",
          questionId,
          resolution: "answer",
        }),
      ).toBe(state);
    }
    state = dispatchAll(
      state,
      {
        type: "setQuestionOption",
        questionId: "question-release-focus",
        optionId: "option-navigation",
        selected: true,
      },
      {
        type: "setQuestionOption",
        questionId: "question-release-checks",
        optionId: "option-offline",
        selected: true,
      },
    );
    for (const questionId of [
      "question-release-focus",
      "question-release-checks",
    ]) {
      state = reducePrototype(state, {
        type: "resolveQuestion",
        questionId,
        resolution: "answer",
      });
      expect(state.answers[questionId]?.submitted).toBe(true);
    }
  });

  it("keeps typed and recent projects distinct while producing the same valid path", () => {
    const typed = reducePrototype(initial(), {
      type: "setNewSessionProject",
      value: "/workspace/aurora",
    });
    const recent = reducePrototype(initial(), {
      type: "selectRecentProject",
      projectId: "project-aurora",
    });
    expect(typed.newSession.project).toBe(recent.newSession.project);
    const unchanged = initial();
    expect(
      reducePrototype(unchanged, {
        type: "selectRecentProject",
        projectId: "missing",
      }),
    ).toBe(unchanged);
  });

  it("separates new-session submission from stable success/failure completion", () => {
    const ready = dispatchAll(
      initial(),
      { type: "setNewSessionProject", value: "/workspace/aurora" },
      { type: "setNewSessionPrompt", value: "Review this fixture" },
    );
    const starting = reducePrototype(ready, { type: "submitNewSession" });
    expect(starting.newSession.outcome).toBe("starting");

    const failed = reducePrototype(starting, {
      type: "completeNewSession",
      result: "failure",
    });
    expect(failed.newSession).toMatchObject({
      project: "/workspace/aurora",
      prompt: "Review this fixture",
      outcome: "failure",
    });
    expect(failed.route).toEqual(ready.route);

    const success = reducePrototype(starting, {
      type: "completeNewSession",
      result: "success",
    });
    const synthetic = success.projection.fixture.sessions.at(-1);
    expect(synthetic?.id).toBe(
      `session-synthetic-${starting.projection.fixture.sessions.length + 1}`,
    );
    expect(success.route).toEqual({
      kind: "conversation",
      sessionId: synthetic?.id,
    });
    expect(success.newSession.outcome).toBe("success");
    expect(
      reducePrototype(ready, { type: "completeNewSession", result: "success" }),
    ).toBe(ready);
  });

  it("updates process-local voice preferences and projects deterministic levels", () => {
    let state = dispatchAll(
      initial(),
      { type: "setSpeakResponses", enabled: false },
      { type: "setSpeechRate", rate: "fast" },
      { type: "openVoice", sessionId: "session-native-client" },
    );
    expect(state.voicePreferences).toEqual({
      speakResponses: false,
      rate: "fast",
    });
    const observed: number[] = [];
    for (
      let index = 0;
      index < state.projection.fixture.voiceSteps.length;
      index += 1
    ) {
      observed.push(
        state.projection.fixture.voiceSteps[state.voice.stepIndex]?.level ?? -1,
      );
      state = reducePrototype(state, { type: "advanceVoice" });
    }
    expect(observed).toEqual(
      state.projection.fixture.voiceSteps.map(({ level }) => level),
    );
    const denied = reducePrototype(state, {
      type: "setVoiceState",
      state: "denied",
    });
    expect(
      denied.projection.fixture.voiceSteps[denied.voice.stepIndex]?.state,
    ).toBe("denied");
    expect(
      reducePrototype(denied, {
        type: "setVoiceState",
        state: "error",
      }).projection.fixture.voiceSteps.find(
        ({ state: step }) => step === "error",
      )?.level,
    ).toBe(0);
  });

  it("distinguishes voice Stop from End", () => {
    const voice = dispatchAll(
      initial(),
      { type: "openVoice", sessionId: "session-native-client" },
      { type: "advanceVoice" },
    );
    const stopped = reducePrototype(voice, { type: "stopVoice" });
    expect(stopped.route.kind).toBe("voice");
    expect(stopped.voice).toMatchObject({ stopped: true, ended: false });
    expect(reducePrototype(stopped, { type: "advanceVoice" })).toBe(stopped);
    const ended = reducePrototype(voice, { type: "endVoice" });
    expect(ended.voice.ended).toBe(true);
    expect(ended.route).toEqual(initial().route);
  });

  it("reset clears interactions, returns to gallery, and increments generation", () => {
    const dirty = dispatchAll(
      initial(),
      { type: "setDraft", value: "discard" },
      { type: "openSession", sessionId: "session-native-client" },
      { type: "toggleTool", itemId: "item-tool-inspect" },
    );
    const reset = reducePrototype(dirty, { type: "reset" });
    expect(reset).toMatchObject({
      concept: null,
      route: { kind: "gallery" },
      draft: "",
      resetGeneration: dirty.resetGeneration + 1,
    });
    expect(reset.history).toEqual([]);
    expect(reset.expandedToolIds.size).toBe(0);
  });
});
