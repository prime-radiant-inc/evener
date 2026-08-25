import { canonicalFixture } from "./fixtures";
import type { PrototypeFixture, QuestionFixture, Route } from "./model";
import { projectScenario } from "./scenarios";
import {
  selectNewSessionValidity,
  selectQuestionSubmitValidity,
} from "./selectors";
import type {
  InitialStateOptions,
  PrototypeAction,
  PrototypeState,
  QuestionAnswerState,
} from "./state";

const emptyAnswer: QuestionAnswerState = {
  selectedOptionIds: [],
  note: "",
  resolution: null,
  submitted: false,
};

function initialProjection(
  options: InitialStateOptions,
  sourceFixture: PrototypeFixture,
) {
  return (
    options.projection ??
    projectScenario(sourceFixture, options.preferences.scenario)
  );
}

function immutableSourceFixture(options: InitialStateOptions) {
  return projectScenario(
    options.sourceFixture ?? options.projection?.fixture ?? canonicalFixture,
    "baseline",
  ).fixture;
}

export function createInitialState(
  options: InitialStateOptions,
): PrototypeState {
  const sourceFixture = immutableSourceFixture(options);
  const projection = initialProjection(options, sourceFixture);
  return {
    concept: options.preferences.concept,
    platform: options.platform,
    scenario: options.preferences.scenario,
    appearance: options.preferences.appearance,
    textScale: options.preferences.textScale,
    reducedMotion: options.preferences.reducedMotion,
    route:
      options.preferences.concept === null
        ? { kind: "gallery" }
        : { kind: "root", tab: "sessions" },
    history: [],
    overlay: null,
    refreshState: "idle",
    sessionQuery: "",
    globalQuery: "",
    selectedSessionId: null,
    focusedItemId: null,
    expandedToolIds: new Set(),
    expandedWorkIds: new Set(),
    draft: "",
    composerMode: "send",
    syntheticTurn: "none",
    answers: {},
    newSession: {
      project: "",
      prompt: "",
      modelId: projection.fixture.models[0]?.id ?? "",
      effort: projection.fixture.efforts[0] ?? "medium",
      outcome: "editing",
      errorCode: null,
    },
    voicePreferences: { speakResponses: true, rate: "normal" },
    voice: { stepIndex: 0, muted: false, stopped: false, ended: false },
    sourceFixture,
    projection,
    resetGeneration: 0,
  };
}

function hasSession(state: PrototypeState, sessionId: string): boolean {
  return state.projection.fixture.sessions.some(({ id }) => id === sessionId);
}

function hasFocusItem(
  state: PrototypeState,
  sessionId: string,
  itemId: string,
): boolean {
  return state.projection.fixture.transcript.some(
    (item) => item.id === itemId && item.sessionId === sessionId,
  );
}

function routeSelection(
  route: Route,
): Pick<PrototypeState, "selectedSessionId" | "focusedItemId"> {
  switch (route.kind) {
    case "conversation":
      return {
        selectedSessionId: route.sessionId,
        focusedItemId: route.focusItemId ?? null,
      };
    case "work":
    case "voice":
      return { selectedSessionId: route.sessionId, focusedItemId: null };
    case "gallery":
    case "root":
      return { selectedSessionId: null, focusedItemId: null };
  }
}

function pushRoute(state: PrototypeState, route: Route): PrototypeState {
  return {
    ...state,
    route,
    history: [...state.history, state.route],
    overlay: null,
    ...routeSelection(route),
  };
}

function popRoute(state: PrototypeState): PrototypeState {
  const route = state.history.at(-1);
  if (!route) return state;
  return {
    ...state,
    route,
    history: state.history.slice(0, -1),
    ...routeSelection(route),
  };
}

function toggleSet(
  values: ReadonlySet<string>,
  value: string,
): ReadonlySet<string> {
  const next = new Set(values);
  if (next.has(value)) next.delete(value);
  else next.add(value);
  return next;
}

function answerFor(
  state: PrototypeState,
  questionId: string,
): QuestionAnswerState {
  return state.answers[questionId] ?? emptyAnswer;
}

function updateAnswer(
  state: PrototypeState,
  questionId: string,
  answer: QuestionAnswerState,
): PrototypeState {
  return { ...state, answers: { ...state.answers, [questionId]: answer } };
}

function findQuestion(
  state: PrototypeState,
  questionId: string,
): QuestionFixture | undefined {
  return state.projection.fixture.questions.find(({ id }) => id === questionId);
}

function editNewSession(
  state: PrototypeState,
  change: Partial<PrototypeState["newSession"]>,
): PrototypeState {
  return {
    ...state,
    newSession: {
      ...state.newSession,
      ...change,
      outcome: "editing",
      errorCode: null,
    },
  };
}

function appendSyntheticSession(state: PrototypeState): PrototypeState {
  const { fixture } = state.projection;
  const id = `session-synthetic-${fixture.sessions.length + 1}`;
  const syntheticSession = {
    id,
    title: state.newSession.prompt.trim(),
    project: state.newSession.project,
    state: "running" as const,
    summary: "Synthetic prototype session",
    updatedLabel: "synthetic",
  };
  const nextFixture: PrototypeFixture = {
    ...fixture,
    sessions: [...fixture.sessions, syntheticSession],
  };
  return {
    ...state,
    route: { kind: "conversation", sessionId: id },
    history: [...state.history, state.route],
    selectedSessionId: id,
    focusedItemId: null,
    projection: {
      ...state.projection,
      fixture: nextFixture,
      selectedSessionId: id,
    },
    newSession: {
      ...state.newSession,
      outcome: "success",
      errorCode: null,
    },
  };
}

function assertNever(value: never): never {
  throw new Error(`unhandled prototype action: ${JSON.stringify(value)}`);
}

export function reducePrototype(
  state: PrototypeState,
  action: PrototypeAction,
): PrototypeState {
  switch (action.type) {
    case "selectConcept":
      return state.concept === action.concept
        ? state
        : { ...state, concept: action.concept };
    case "setScenario": {
      if (state.scenario === action.scenario) return state;
      const preferences = {
        version: 1 as const,
        concept: state.concept,
        scenario: action.scenario,
        appearance: state.appearance,
        textScale: state.textScale,
        reducedMotion: state.reducedMotion,
      };
      const rebuilt = createInitialState({
        platform: state.platform,
        preferences,
        sourceFixture: state.sourceFixture,
        projection: projectScenario(state.sourceFixture, action.scenario),
      });
      return {
        ...rebuilt,
        sourceFixture: state.sourceFixture,
        resetGeneration: state.resetGeneration,
      };
    }
    case "setAppearance":
      return state.appearance === action.appearance
        ? state
        : { ...state, appearance: action.appearance };
    case "setTextScale":
      return state.textScale === action.textScale
        ? state
        : { ...state, textScale: action.textScale };
    case "setReducedMotion":
      return state.reducedMotion === action.reducedMotion
        ? state
        : { ...state, reducedMotion: action.reducedMotion };
    case "navigateRoot":
      return state.route.kind === "root" && state.route.tab === action.tab
        ? state
        : {
            ...state,
            route: { kind: "root", tab: action.tab },
            history: [],
            overlay: null,
            selectedSessionId: null,
            focusedItemId: null,
          };
    case "openSession": {
      if (
        !hasSession(state, action.sessionId) ||
        (action.focusItemId !== undefined &&
          !hasFocusItem(state, action.sessionId, action.focusItemId))
      ) {
        return state;
      }
      const route: Route = {
        kind: "conversation",
        sessionId: action.sessionId,
        ...(action.focusItemId ? { focusItemId: action.focusItemId } : {}),
      };
      return pushRoute(state, route);
    }
    case "openWork":
      return hasSession(state, action.sessionId)
        ? pushRoute(state, { kind: "work", sessionId: action.sessionId })
        : state;
    case "openVoice":
      return hasSession(state, action.sessionId)
        ? {
            ...pushRoute(state, { kind: "voice", sessionId: action.sessionId }),
            voice: {
              stepIndex: 0,
              muted: state.voice.muted,
              stopped: false,
              ended: false,
            },
          }
        : state;
    case "openOverlay":
      return state.overlay === action.overlay
        ? state
        : { ...state, overlay: action.overlay };
    case "goBack":
      return state.overlay === null
        ? popRoute(state)
        : { ...state, overlay: null };
    case "refreshSessions":
      return state.refreshState === "refreshing"
        ? state
        : { ...state, refreshState: "refreshing" };
    case "completeRefresh":
      return state.refreshState === "refreshing"
        ? { ...state, refreshState: "complete" }
        : state;
    case "setSessionQuery":
      return state.sessionQuery === action.value
        ? state
        : { ...state, sessionQuery: action.value };
    case "setGlobalQuery":
      return state.globalQuery === action.value
        ? state
        : { ...state, globalQuery: action.value };
    case "openSearchResult": {
      const result = state.projection.fixture.search.find(
        ({ id }) => id === action.resultId,
      );
      if (
        !result ||
        !hasSession(state, result.sessionId) ||
        (result.itemId !== null &&
          !hasFocusItem(state, result.sessionId, result.itemId))
      ) {
        return state;
      }
      const route: Route = {
        kind: "conversation",
        sessionId: result.sessionId,
        ...(result.itemId ? { focusItemId: result.itemId } : {}),
      };
      return pushRoute(state, route);
    }
    case "toggleTool":
      return state.projection.fixture.transcript.some(
        (item) => item.id === action.itemId && item.kind === "tool",
      )
        ? {
            ...state,
            expandedToolIds: toggleSet(state.expandedToolIds, action.itemId),
          }
        : state;
    case "toggleWork":
      return state.projection.fixture.work.some(
        ({ id }) => id === action.nodeId,
      )
        ? {
            ...state,
            expandedWorkIds: toggleSet(state.expandedWorkIds, action.nodeId),
          }
        : state;
    case "setDraft":
      return state.draft === action.value
        ? state
        : { ...state, draft: action.value };
    case "setComposerMode":
      return state.composerMode === "running" ||
        state.composerMode === action.mode
        ? state
        : { ...state, composerMode: action.mode };
    case "submitComposer":
      return state.draft.trim().length === 0 ||
        state.syntheticTurn === "starting"
        ? state
        : {
            ...state,
            draft: "",
            composerMode: "running",
            syntheticTurn: "starting",
          };
    case "completeSyntheticTurn":
      return state.syntheticTurn === "starting"
        ? { ...state, composerMode: "send", syntheticTurn: "complete" }
        : state;
    case "stopSyntheticTurn":
      return state.syntheticTurn === "starting"
        ? { ...state, composerMode: "send", syntheticTurn: "stopped" }
        : state;
    case "setQuestionOption": {
      const question = findQuestion(state, action.questionId);
      if (!question?.options.some(({ id }) => id === action.optionId)) {
        return state;
      }
      const answer = answerFor(state, action.questionId);
      if (answer.submitted) return state;
      const contains = answer.selectedOptionIds.includes(action.optionId);
      if (contains === action.selected) return state;
      const selectedOptionIds = action.selected
        ? question.mode === "single"
          ? [action.optionId]
          : [...answer.selectedOptionIds, action.optionId]
        : answer.selectedOptionIds.filter((id) => id !== action.optionId);
      return updateAnswer(state, action.questionId, {
        ...answer,
        selectedOptionIds,
      });
    }
    case "setQuestionNote": {
      const question = findQuestion(state, action.questionId);
      if (!question?.allowNote) return state;
      const answer = answerFor(state, action.questionId);
      if (answer.submitted || answer.note === action.note) return state;
      return updateAnswer(state, action.questionId, {
        ...answer,
        note: action.note,
      });
    }
    case "resolveQuestion": {
      const question = findQuestion(state, action.questionId);
      const answer = answerFor(state, action.questionId);
      if (
        !question ||
        !selectQuestionSubmitValidity(
          state,
          action.questionId,
          action.resolution,
        )
      ) {
        return state;
      }
      return updateAnswer(state, action.questionId, {
        ...answer,
        resolution: action.resolution,
        submitted: true,
      });
    }
    case "setNewSessionProject":
      return state.newSession.project === action.value
        ? state
        : editNewSession(state, { project: action.value });
    case "selectRecentProject": {
      const project = state.projection.fixture.recentProjects.find(
        ({ id }) => id === action.projectId,
      );
      return project ? editNewSession(state, { project: project.path }) : state;
    }
    case "setNewSessionPrompt":
      return state.newSession.prompt === action.value
        ? state
        : editNewSession(state, { prompt: action.value });
    case "setNewSessionModel":
      return state.projection.fixture.models.some(
        ({ id }) => id === action.modelId,
      )
        ? editNewSession(state, { modelId: action.modelId })
        : state;
    case "setNewSessionEffort":
      return state.projection.fixture.efforts.includes(action.effort)
        ? editNewSession(state, { effort: action.effort })
        : state;
    case "submitNewSession":
      return selectNewSessionValidity(state)
        ? {
            ...state,
            newSession: {
              ...state.newSession,
              outcome: "starting",
              errorCode: null,
            },
          }
        : state;
    case "completeNewSession":
      if (state.newSession.outcome !== "starting") return state;
      return action.result === "success"
        ? appendSyntheticSession(state)
        : {
            ...state,
            newSession: {
              ...state.newSession,
              outcome: "failure",
              errorCode: "synthetic-start-failed",
            },
          };
    case "setSpeakResponses":
      return state.voicePreferences.speakResponses === action.enabled
        ? state
        : {
            ...state,
            voicePreferences: {
              ...state.voicePreferences,
              speakResponses: action.enabled,
            },
          };
    case "setSpeechRate":
      return state.voicePreferences.rate === action.rate
        ? state
        : {
            ...state,
            voicePreferences: {
              ...state.voicePreferences,
              rate: action.rate,
            },
          };
    case "advanceVoice":
      return state.voice.stopped ||
        state.voice.ended ||
        state.voice.stepIndex >= state.projection.fixture.voiceSteps.length - 1
        ? state
        : {
            ...state,
            voice: { ...state.voice, stepIndex: state.voice.stepIndex + 1 },
          };
    case "setVoiceState": {
      const stepIndex = state.projection.fixture.voiceSteps.findIndex(
        ({ state: voiceState }) => voiceState === action.state,
      );
      return stepIndex < 0 || stepIndex === state.voice.stepIndex
        ? state
        : {
            ...state,
            voice: {
              ...state.voice,
              stepIndex,
              stopped: false,
              ended: false,
            },
          };
    }
    case "toggleVoiceMute":
      return { ...state, voice: { ...state.voice, muted: !state.voice.muted } };
    case "stopVoice":
      return state.route.kind !== "voice" || state.voice.stopped
        ? state
        : { ...state, voice: { ...state.voice, stopped: true } };
    case "endVoice":
      if (state.route.kind !== "voice") return state;
      return {
        ...popRoute(state),
        voice: { ...state.voice, stopped: false, ended: true },
      };
    case "reset": {
      const reset = createInitialState({
        platform: state.platform,
        preferences: {
          version: 1,
          concept: null,
          appearance: "system",
          textScale: "standard",
          reducedMotion: false,
          scenario: "baseline",
        },
        sourceFixture: state.sourceFixture,
        projection: projectScenario(state.sourceFixture, "baseline"),
      });
      return {
        ...reset,
        sourceFixture: state.sourceFixture,
        resetGeneration: state.resetGeneration + 1,
      };
    }
    default:
      return assertNever(action);
  }
}
