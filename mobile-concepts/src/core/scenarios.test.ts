import { describe, expect, it } from "vitest";
import { canonicalFixture } from "./fixtures";
import { projectScenario, scenarioIds, scenarioProjectors } from "./scenarios";

const activeStates = new Set([
  "needs-answer",
  "needs-permission",
  "running",
  "waiting",
]);

describe("scenario projection", () => {
  it("exposes the exact Lab Controls order and a total projector map", () => {
    expect(scenarioIds).toEqual([
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
    ]);
    expect(Object.keys(scenarioProjectors)).toEqual(scenarioIds);
  });

  it.each(scenarioIds)(
    "projects %s without mutating canonical fixtures",
    (scenario) => {
      const before = structuredClone(canonicalFixture);
      const projected = projectScenario(canonicalFixture, scenario);
      expect(projected.id).toBe(scenario);
      expect(projected.fixture).not.toBe(canonicalFixture);
      expect(projected.fixture.sessions).not.toBe(canonicalFixture.sessions);
      expect(canonicalFixture).toEqual(before);
    },
  );

  it("projects exact structural scenario states", () => {
    expect(projectScenario(canonicalFixture, "loading").screenState).toBe(
      "loading",
    );
    expect(projectScenario(canonicalFixture, "empty").fixture.sessions).toEqual(
      [],
    );

    const offline = projectScenario(canonicalFixture, "offline");
    expect(offline.screenState).toBe("offline");
    expect(offline.fixture.sessions.length).toBeGreaterThan(0);
    expect(
      offline.fixture.sessions.every(({ updatedLabel }) =>
        updatedLabel.startsWith("stale · "),
      ),
    ).toBe(true);
    expect(offline.fixture.hubs.every(({ state }) => state === "offline")).toBe(
      true,
    );

    const error = projectScenario(canonicalFixture, "error");
    expect(error.screenState).toBe("error");
    expect(error.recoverable).toBe(true);

    expect(
      projectScenario(canonicalFixture, "needs-attention").selectedSessionId,
    ).toBe("session-mobile-release");
    expect(
      projectScenario(canonicalFixture, "question").selectedSessionId,
    ).toBe("session-mobile-release");
    expect(
      projectScenario(canonicalFixture, "multi-agent").fixture.work,
    ).toHaveLength(canonicalFixture.work.length);

    const completed = projectScenario(canonicalFixture, "completed");
    expect(completed.selectedSessionId).toBe("session-pairing-pr");
    expect(
      completed.fixture.work.some(({ state }) => activeStates.has(state)),
    ).toBe(false);

    expect(
      projectScenario(canonicalFixture, "voice").fixture.voiceSteps[0]?.state,
    ).toBe("ready");

    const longContent = projectScenario(canonicalFixture, "long-content");
    expect(longContent.selectedSessionId).toBe("session-native-client");
    expect(
      longContent.fixture.transcript.some((item) => item.kind === "attachment"),
    ).toBe(true);
    expect(
      longContent.fixture.transcript.some(
        (item) => "body" in item && item.body.length > 300,
      ),
    ).toBe(true);
  });
});
