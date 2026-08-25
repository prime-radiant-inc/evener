import type { PrototypeFixture, ScenarioId, SessionRecord } from "./model";

export interface ScenarioProjection {
  id: ScenarioId;
  screenState: "ready" | "loading" | "empty" | "offline" | "error";
  fixture: PrototypeFixture;
  selectedSessionId: string | null;
  recoverable: boolean;
}

export type Projector = (fixture: PrototypeFixture) => ScenarioProjection;

export const scenarioIds = Object.freeze([
  "baseline",
  "loading",
  "empty",
  "offline",
  "error",
  "needs-attention",
  "multi-agent",
  "question",
  "completed",
  "voice",
  "long-content",
] as const satisfies readonly ScenarioId[]);

function deepFreeze<T>(value: T): T {
  if (value !== null && typeof value === "object" && !Object.isFrozen(value)) {
    for (const child of Object.values(value)) deepFreeze(child);
    Object.freeze(value);
  }
  return value;
}

function cloneFixture(fixture: PrototypeFixture): PrototypeFixture {
  return {
    version: 1,
    sessions: fixture.sessions.map((session) => ({ ...session })),
    transcript: fixture.transcript.map((item) => ({ ...item })),
    questions: fixture.questions.map((question) => ({
      ...question,
      options: question.options.map((option) => ({ ...option })),
    })),
    work: fixture.work.map((node) => ({ ...node })),
    search: fixture.search.map((document) => ({ ...document })),
    recentProjects: fixture.recentProjects.map((project) => ({ ...project })),
    models: fixture.models.map((model) => ({ ...model })),
    efforts: [...fixture.efforts],
    voiceSteps: fixture.voiceSteps.map((step) => ({ ...step })),
    hubs: fixture.hubs.map((hub) => ({ ...hub })),
    usage: { ...fixture.usage },
  };
}

function projection(
  id: ScenarioId,
  fixture: PrototypeFixture,
  screenState: ScenarioProjection["screenState"] = "ready",
  selectedSessionId: string | null = "session-native-client",
  recoverable = false,
): ScenarioProjection {
  return deepFreeze({
    id,
    screenState,
    fixture,
    selectedSessionId,
    recoverable,
  });
}

function baselineProjector(fixture: PrototypeFixture): ScenarioProjection {
  return projection("baseline", cloneFixture(fixture));
}

function loadingProjector(fixture: PrototypeFixture): ScenarioProjection {
  return projection("loading", cloneFixture(fixture), "loading", null);
}

function emptyProjector(fixture: PrototypeFixture): ScenarioProjection {
  const next = cloneFixture(fixture);
  return projection(
    "empty",
    {
      ...next,
      sessions: [],
      transcript: [],
      work: [],
      search: [],
    },
    "empty",
    null,
  );
}

function offlineProjector(fixture: PrototypeFixture): ScenarioProjection {
  const next = cloneFixture(fixture);
  const staleSessions: readonly SessionRecord[] = next.sessions.map(
    (session) => ({
      ...session,
      updatedLabel: `stale · ${session.updatedLabel}`,
    }),
  );
  return projection(
    "offline",
    {
      ...next,
      sessions: staleSessions,
      hubs: next.hubs.map((hub) => ({ ...hub, state: "offline" as const })),
    },
    "offline",
  );
}

function errorProjector(fixture: PrototypeFixture): ScenarioProjection {
  return projection("error", cloneFixture(fixture), "error", null, true);
}

function needsAttentionProjector(
  fixture: PrototypeFixture,
): ScenarioProjection {
  return projection(
    "needs-attention",
    cloneFixture(fixture),
    "ready",
    "session-mobile-release",
  );
}

function multiAgentProjector(fixture: PrototypeFixture): ScenarioProjection {
  return projection(
    "multi-agent",
    cloneFixture(fixture),
    "ready",
    "session-native-client",
  );
}

function questionProjector(fixture: PrototypeFixture): ScenarioProjection {
  return projection(
    "question",
    cloneFixture(fixture),
    "ready",
    "session-mobile-release",
  );
}

function completedProjector(fixture: PrototypeFixture): ScenarioProjection {
  const next = cloneFixture(fixture);
  return projection(
    "completed",
    { ...next, work: [] },
    "ready",
    "session-pairing-pr",
  );
}

function voiceProjector(fixture: PrototypeFixture): ScenarioProjection {
  const next = cloneFixture(fixture);
  const readyIndex = next.voiceSteps.findIndex(
    ({ state }) => state === "ready",
  );
  return projection("voice", {
    ...next,
    voiceSteps:
      readyIndex < 0
        ? next.voiceSteps
        : [
            ...next.voiceSteps.slice(readyIndex),
            ...next.voiceSteps.slice(0, readyIndex),
          ],
  });
}

function longContentProjector(fixture: PrototypeFixture): ScenarioProjection {
  return projection(
    "long-content",
    cloneFixture(fixture),
    "ready",
    "session-native-client",
  );
}

export const scenarioProjectors: Record<ScenarioId, Projector> = Object.freeze({
  baseline: baselineProjector,
  loading: loadingProjector,
  empty: emptyProjector,
  offline: offlineProjector,
  error: errorProjector,
  "needs-attention": needsAttentionProjector,
  "multi-agent": multiAgentProjector,
  question: questionProjector,
  completed: completedProjector,
  voice: voiceProjector,
  "long-content": longContentProjector,
});

export function projectScenario(
  fixture: PrototypeFixture,
  scenario: ScenarioId,
): ScenarioProjection {
  return scenarioProjectors[scenario](fixture);
}
