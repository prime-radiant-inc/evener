import {
  cleanup,
  fireEvent,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import type { Platform, RootTab, ScenarioId } from "../../core/model";
import {
  selectCurrentVoiceLevel,
  selectGroupedSessions,
  selectSearchProjection,
} from "../../core/selectors";
import type { PrototypeState } from "../../core/state";
import { renderConcept } from "../../test/renderConcept";
import { stillwaterModule } from "./index";

afterEach(cleanup);

function renderRoot(
  tab: RootTab,
  platform: Platform = "ios",
  scenario: ScenarioId = "baseline",
) {
  return renderConcept(stillwaterModule, {
    platform,
    scenario,
    route: { kind: "root", tab },
  });
}

function mainFor(route: string): HTMLElement {
  const main = screen.getByRole("main");
  expect(main).toHaveAttribute("data-route", route);
  return main;
}

function sessionIdFor(
  state: PrototypeState,
  requirement: (sessionId: string) => boolean,
): string {
  const session = state.projection.fixture.sessions.find(({ id }) =>
    requirement(id),
  );
  if (!session)
    throw new Error("Fixture does not contain the required session");
  return session.id;
}

function openSession(result: ReturnType<typeof renderRoot>, sessionId: string) {
  const sessionRow = document.querySelector(`[data-session-id="${sessionId}"]`);
  if (!(sessionRow instanceof HTMLElement)) {
    throw new Error(`Missing session row ${sessionId}`);
  }
  fireEvent.click(within(sessionRow).getByRole("button"));
  expect(result.store.getState().route).toEqual({
    kind: "conversation",
    sessionId,
  });
  return mainFor("conversation");
}

function fillNewSession(
  result: ReturnType<typeof renderRoot>,
  projectPath: string,
) {
  const fixture = result.store.getState().projection.fixture;
  const model = fixture.models.at(-1);
  const effort = fixture.efforts.at(-1);
  if (!model || !effort)
    throw new Error("Fixture needs model and effort choices");
  fireEvent.change(screen.getByRole("textbox", { name: "Project path" }), {
    target: { value: projectPath },
  });
  fireEvent.change(screen.getByRole("textbox", { name: "Prompt" }), {
    target: { value: "Verify the complete mobile flow" },
  });
  fireEvent.change(screen.getByRole("combobox", { name: "Model" }), {
    target: { value: model.id },
  });
  fireEvent.change(screen.getByRole("combobox", { name: "Effort" }), {
    target: { value: effort },
  });
}

describe("Stillwater direct interactions", () => {
  it("groups attention, running, and recent sessions, filters locally, and explicitly completes refresh", () => {
    const result = renderRoot("sessions");
    const main = mainFor("sessions");
    const initial = result.store.getState();
    const groups = selectGroupedSessions(initial);
    expect(groups.map(({ id }) => id)).toEqual([
      "needs-you",
      "running",
      "recent",
    ]);
    for (const group of groups) {
      const region = main.querySelector(
        `[data-session-group-id="${group.id}"]`,
      );
      expect(region).toBeVisible();
      expect(
        within(region as HTMLElement).getByRole("heading", {
          name: group.label,
        }),
      ).toBeVisible();
    }

    const filterProject = initial.projection.fixture.sessions.at(-1)?.project;
    if (!filterProject) throw new Error("Fixture needs a filterable session");
    fireEvent.change(
      screen.getByRole("searchbox", { name: "Filter sessions" }),
      { target: { value: filterProject } },
    );
    expect(result.store.getState().sessionQuery).toBe(filterProject);
    expect(main.querySelectorAll("[data-session-id]")).toHaveLength(
      selectGroupedSessions(result.store.getState()).flatMap(
        ({ sessions }) => sessions,
      ).length,
    );

    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(result.store.getState().refreshState).toBe("refreshing");
    expect(
      main.querySelector('[data-refresh-state="refreshing"]'),
    ).toBeVisible();
    fireEvent.click(
      main.querySelector('[data-action="complete-refresh"]') as HTMLElement,
    );
    expect(result.store.getState().refreshState).toBe("complete");
    expect(main.querySelector('[data-refresh-state="complete"]')).toBeVisible();
  });

  it("shows search prompt, contextual results, unmatched state, focus, and back", async () => {
    const result = renderRoot("search");
    let main = mainFor("search");
    expect(selectSearchProjection(result.store.getState()).kind).toBe("prompt");
    expect(main.querySelector('[data-search-state="prompt"]')).toBeVisible();

    const fixtureResult = result.store
      .getState()
      .projection.fixture.search.find(({ itemId }) => itemId !== null);
    if (!fixtureResult)
      throw new Error("Fixture needs a focusable search result");
    fireEvent.change(screen.getByRole("searchbox", { name: "Search" }), {
      target: { value: fixtureResult.title },
    });
    const resultLink = main.querySelector(
      `[data-search-result-id="${fixtureResult.id}"]`,
    );
    expect(resultLink).toHaveTextContent(fixtureResult.body);
    fireEvent.click(resultLink as HTMLElement);
    await waitFor(() =>
      expect(result.store.getState().focusedItemId).toBe(fixtureResult.itemId),
    );
    main = mainFor("conversation");
    expect(
      main.querySelector(
        `[data-transcript-item-id="${fixtureResult.itemId}"][data-focused="true"]`,
      ),
    ).toBeVisible();
    fireEvent.click(within(main).getByRole("button", { name: "Back" }));
    await waitFor(() =>
      expect(result.store.getState().route).toEqual({
        kind: "root",
        tab: "search",
      }),
    );

    cleanup();
    const unmatched = renderRoot("search");
    main = mainFor("search");
    fireEvent.change(screen.getByRole("searchbox", { name: "Search" }), {
      target: { value: "unmatched-local-query" },
    });
    expect(selectSearchProjection(unmatched.store.getState()).kind).toBe(
      "no-results",
    );
    expect(
      main.querySelector('[data-search-state="no-results"]'),
    ).toBeVisible();
  });

  it("opens and backs out of a session, discloses tools, opens Work, and drives composer modes", async () => {
    const result = renderRoot("sessions");
    const state = result.store.getState();
    const tool = state.projection.fixture.transcript.find(
      (item) => item.kind === "tool",
    );
    if (tool?.kind !== "tool") throw new Error("Fixture needs a tool");
    const main = openSession(result, tool.sessionId);
    const disclosure = within(main).getByRole("button", { name: tool.label });
    expect(disclosure).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(disclosure);
    expect(result.store.getState().expandedToolIds).toContain(tool.id);
    expect(disclosure).toHaveAttribute("aria-expanded", "true");

    for (const mode of ["Steer", "Queue", "Send"] as const) {
      fireEvent.click(within(main).getByRole("button", { name: mode }));
      expect(result.store.getState().composerMode).toBe(mode.toLowerCase());
    }
    fireEvent.change(within(main).getByRole("textbox", { name: "Message" }), {
      target: { value: "Continue with the next check" },
    });
    fireEvent.click(
      within(main).getByRole("button", { name: "Submit message" }),
    );
    expect(result.store.getState().syntheticTurn).toBe("starting");
    fireEvent.click(
      within(main).getByRole("button", { name: "Complete response" }),
    );
    expect(result.store.getState().syntheticTurn).toBe("complete");

    fireEvent.click(within(main).getByRole("button", { name: "Work" }));
    await waitFor(() =>
      expect(result.store.getState().route.kind).toBe("work"),
    );
    const workMain = mainFor("work");
    fireEvent.click(within(workMain).getByRole("button", { name: "Back" }));
    await waitFor(() =>
      expect(result.store.getState().route.kind).toBe("conversation"),
    );
    fireEvent.click(
      within(mainFor("conversation")).getByRole("button", { name: "Back" }),
    );
    await waitFor(() =>
      expect(result.store.getState().route).toEqual({
        kind: "root",
        tab: "sessions",
      }),
    );
  });

  it("handles question choices, notes, answer validity, and every permitted alternate outcome", () => {
    const result = renderRoot("sessions", "ios", "question");
    const selected = result.store.getState().projection.selectedSessionId;
    if (!selected)
      throw new Error("Question scenario needs a selected session");
    let main = openSession(result, selected);
    const firstQuestion =
      result.store.getState().projection.fixture.questions[0];
    if (!firstQuestion) throw new Error("Fixture needs a question");
    const firstForm = main.querySelector(
      `form[data-question-id="${firstQuestion.id}"]`,
    );
    if (!(firstForm instanceof HTMLElement))
      throw new Error("Missing question form");
    const submit = within(firstForm).getByRole("button", {
      name: "Submit answer",
    });
    expect(submit).toBeDisabled();
    const option = firstQuestion.options[0];
    if (!option) throw new Error("Question needs an option");
    fireEvent.click(within(firstForm).getByLabelText(option.label));
    fireEvent.change(within(firstForm).getByRole("textbox", { name: "Note" }), {
      target: { value: "Prioritize native navigation" },
    });
    expect(submit).toBeEnabled();
    fireEvent.click(submit);
    expect(result.store.getState().answers[firstQuestion.id]).toMatchObject({
      resolution: "answer",
      submitted: true,
      note: "Prioritize native navigation",
    });

    for (const [resolution, name] of [
      ["fallback", /fallback/i],
      ["decide", /you decide/i],
      ["skip", /^skip$/i],
    ] as const) {
      cleanup();
      const alternate = renderRoot("sessions", "ios", "question");
      const alternateSession =
        alternate.store.getState().projection.selectedSessionId;
      if (!alternateSession)
        throw new Error("Question scenario needs ownership");
      main = openSession(alternate, alternateSession);
      const question = alternate.store
        .getState()
        .projection.fixture.questions.find((candidate) =>
          resolution === "skip"
            ? candidate.allowSkip
            : resolution === "decide"
              ? candidate.allowDecide
              : candidate.allowFallback,
        );
      if (!question) throw new Error(`Fixture must permit ${resolution}`);
      const form = main.querySelector(
        `form[data-question-id="${question.id}"]`,
      );
      if (!(form instanceof HTMLElement))
        throw new Error("Missing alternate form");
      fireEvent.click(within(form).getByRole("button", { name }));
      expect(alternate.store.getState().answers[question.id]).toMatchObject({
        resolution,
        submitted: true,
      });
    }
  });

  it("keeps recent selection and typed fixture paths separate and exposes starting, failure, and success", async () => {
    const failure = renderRoot("new");
    let main = mainFor("new");
    const projects = failure.store.getState().projection.fixture.recentProjects;
    const recent = projects[0];
    const typed = projects.at(-1);
    if (!recent || !typed)
      throw new Error("Fixture needs recent and typed projects");
    const recentRow = main.querySelector(`[data-project-id="${recent.id}"]`);
    fireEvent.click(within(recentRow as HTMLElement).getByRole("button"));
    expect(failure.store.getState().newSession.project).toBe(recent.path);
    fillNewSession(failure, typed.path);
    expect(failure.store.getState().newSession.project).toBe(typed.path);
    fireEvent.click(screen.getByRole("button", { name: "Start session" }));
    expect(failure.store.getState().newSession.outcome).toBe("starting");
    expect(
      main.querySelector('[data-new-session-state="starting"]'),
    ).toBeVisible();
    fireEvent.click(
      main.querySelector(
        '[data-action="complete-new-session-failure"]',
      ) as HTMLElement,
    );
    expect(failure.store.getState().newSession.outcome).toBe("failure");
    expect(
      main.querySelector('[data-new-session-state="failure"]'),
    ).toBeVisible();

    cleanup();
    const success = renderRoot("new");
    main = mainFor("new");
    fillNewSession(success, typed.path);
    fireEvent.click(screen.getByRole("button", { name: "Start session" }));
    fireEvent.click(
      main.querySelector(
        '[data-action="complete-new-session-success"]',
      ) as HTMLElement,
    );
    await waitFor(() =>
      expect(success.store.getState().newSession.outcome).toBe("success"),
    );
    expect(success.store.getState().route.kind).toBe("conversation");
  });

  it("advances deterministic voice levels, mutes, stops without ending, and ends", async () => {
    const result = renderRoot("sessions", "ios", "voice");
    const sessionId = sessionIdFor(result.store.getState(), (candidate) =>
      result.store
        .getState()
        .projection.fixture.sessions.some(({ id }) => id === candidate),
    );
    let main = openSession(result, sessionId);
    fireEvent.click(within(main).getByRole("button", { name: "Voice" }));
    await waitFor(() =>
      expect(result.store.getState().route.kind).toBe("voice"),
    );
    main = mainFor("voice");
    const initialLevel = selectCurrentVoiceLevel(result.store.getState());
    fireEvent.click(
      within(main).getByRole("button", { name: "Advance voice state" }),
    );
    expect(selectCurrentVoiceLevel(result.store.getState())).not.toBe(
      initialLevel,
    );
    fireEvent.click(within(main).getByRole("button", { name: "Mute" }));
    expect(result.store.getState().voice.muted).toBe(true);
    fireEvent.click(within(main).getByRole("button", { name: "Stop" }));
    expect(result.store.getState().voice.stopped).toBe(true);
    expect(result.store.getState().voice.ended).toBe(false);
    expect(result.store.getState().route.kind).toBe("voice");
    fireEvent.click(within(main).getByRole("button", { name: "End" }));
    await waitFor(() =>
      expect(result.store.getState().route.kind).toBe("conversation"),
    );
    expect(result.store.getState().voice.ended).toBe(true);
  });

  it("renders deterministic screen recovery and Settings controls without remote behavior", async () => {
    const error = renderRoot("sessions", "ios", "error");
    let main = mainFor("sessions");
    expect(main.querySelector('[data-screen-state="error"]')).toBeVisible();
    fireEvent.click(within(main).getByRole("button", { name: "Retry" }));
    await waitFor(() => {
      expect(error.store.getState()).toMatchObject({
        scenario: "baseline",
        route: { kind: "root", tab: "sessions" },
      });
      expect(main.querySelector('[data-screen-state="error"]')).toBeNull();
      expect(main.querySelector("[data-session-group-id]")).toBeVisible();
    });

    cleanup();
    const settings = renderRoot("settings");
    main = mainFor("settings");
    const hubs = settings.store.getState().projection.fixture.hubs;
    expect(main.querySelectorAll('[data-fictional="true"]')).toHaveLength(
      hubs.length,
    );
    fireEvent.change(
      within(main).getByRole("combobox", { name: "Appearance" }),
      {
        target: { value: "dark" },
      },
    );
    fireEvent.change(
      within(main).getByRole("combobox", { name: "Text size" }),
      {
        target: { value: "large" },
      },
    );
    fireEvent.click(
      within(main).getByRole("checkbox", { name: "Reduce motion" }),
    );
    expect(settings.store.getState()).toMatchObject({
      appearance: "dark",
      textScale: "large",
      reducedMotion: true,
    });
    expect(
      screen.getByRole("button", { name: "Switch concept" }),
    ).toBeVisible();
    expect(screen.getByRole("button", { name: "Lab Controls" })).toBeVisible();
  });
});
