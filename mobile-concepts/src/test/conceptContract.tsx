import {
  cleanup,
  fireEvent,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import type { ConceptModule } from "../concepts/contract";
import { formatUsage } from "../concepts/shared/format";
import type {
  Platform,
  PrototypeFixture,
  QuestionFixture,
  Route,
  ScenarioId,
} from "../core/model";
import { getPlatformPrimitives } from "../core/platform";
import {
  selectCurrentVoiceLevel,
  selectCurrentVoiceStep,
  selectGroupedSessions,
  selectSearchProjection,
} from "../core/selectors";
import type { PrototypeState } from "../core/state";
import { type RenderConceptResult, renderConcept } from "./renderConcept";

type OwnedSurface = "conversation" | "question" | "work" | "voice";

interface OpenConversationResult {
  result: RenderConceptResult;
  sessionId: string;
}

const rootLabels = {
  sessions: "Sessions",
  search: "Search",
  new: "New Session",
  settings: "Settings",
} as const;

function renderRoot(
  module: ConceptModule,
  tab: keyof typeof rootLabels,
  platform: Platform = "ios",
  scenario: ScenarioId = "baseline",
): RenderConceptResult {
  return renderConcept(module, {
    platform,
    route: { kind: "root", tab },
    scenario,
  });
}

function expectRouteSurface(route: string): HTMLElement {
  const mains = screen.getAllByRole("main");
  expect(mains).toHaveLength(1);
  const main = mains[0];
  if (!main) throw new Error("Concept must render one main element");
  expect(main).toHaveAttribute("data-route", route);
  return main;
}

function getAction(
  name: string,
  scope: ParentNode = document,
): HTMLButtonElement {
  const actions = scope.querySelectorAll(`[data-action="${name}"]`);
  expect(actions).toHaveLength(1);
  const action = actions[0];
  expect(action).toBeInstanceOf(HTMLButtonElement);
  return action as HTMLButtonElement;
}

function getDomainControl(scope: ParentNode, selector: string): HTMLElement {
  const domainElements = scope.querySelectorAll(selector);
  expect(domainElements).toHaveLength(1);
  const domainElement = domainElements[0];
  if (!(domainElement instanceof HTMLElement)) {
    throw new Error(`Domain element ${selector} must be an HTMLElement`);
  }
  if (domainElement.matches("button, a[href]")) {
    expect(domainElement).toHaveAccessibleName();
    return domainElement;
  }
  const buttons = within(domainElement).queryAllByRole("button");
  const links = within(domainElement).queryAllByRole("link");
  expect([...buttons, ...links]).toHaveLength(1);
  const control = buttons[0] ?? links[0];
  if (!control) throw new Error(`Domain element ${selector} needs a control`);
  expect(control).toHaveAccessibleName();
  return control;
}

function requireFixtureSession(
  fixture: PrototypeFixture,
  sessionId: string | null | undefined,
  relationship: string,
): string {
  expect(sessionId, `${relationship} must identify a session`).toBeTruthy();
  const session = fixture.sessions.find(({ id }) => id === sessionId);
  expect(session, `${relationship} session must exist`).toBeDefined();
  if (!session) throw new Error(`${relationship} has no canonical session`);
  return session.id;
}

function deriveOwnedSession(
  state: PrototypeState,
  surface: OwnedSurface,
): string {
  const { fixture } = state.projection;
  if (surface === "conversation") {
    const tool = fixture.transcript.find((item) => item.kind === "tool");
    expect(
      tool,
      "conversation fixture must own a tool transcript item",
    ).toBeDefined();
    return requireFixtureSession(fixture, tool?.sessionId, "tool transcript");
  }
  if (surface === "question") {
    const selectedSessionId = requireFixtureSession(
      fixture,
      state.projection.selectedSessionId,
      "question projection",
    );
    const questionItems = fixture.transcript.filter(
      (item) =>
        item.kind === "question" && item.sessionId === selectedSessionId,
    );
    expect(questionItems.length).toBeGreaterThan(0);
    for (const item of questionItems) {
      if (item.kind !== "question") continue;
      expect(
        fixture.questions.some(({ id }) => id === item.questionId),
        `question transcript ${item.id} must resolve to a question fixture`,
      ).toBe(true);
    }
    return selectedSessionId;
  }
  if (surface === "work") {
    const sessionIds = new Set(fixture.work.map(({ sessionId }) => sessionId));
    const workSessionId = [...sessionIds].find((candidate) => {
      const kinds = new Set(
        fixture.work
          .filter(({ sessionId }) => sessionId === candidate)
          .map(({ kind }) => kind),
      );
      return (["task", "subagent", "job"] as const).every((kind) =>
        kinds.has(kind),
      );
    });
    return requireFixtureSession(
      fixture,
      workSessionId,
      "complete Work hierarchy",
    );
  }
  return requireFixtureSession(
    fixture,
    state.projection.selectedSessionId,
    "voice projection",
  );
}

async function waitForExactRoute(
  result: RenderConceptResult,
  route: Route,
  surface: string,
): Promise<void> {
  await waitFor(() => {
    expect(result.store.getState().route).toEqual(route);
    expectRouteSurface(surface);
  });
}

async function openOwnedConversation(
  module: ConceptModule,
  surface: OwnedSurface,
  scenario: ScenarioId = "baseline",
): Promise<OpenConversationResult> {
  const result = renderRoot(module, "sessions", "ios", scenario);
  const sessionId = deriveOwnedSession(result.store.getState(), surface);
  const sessionsMain = expectRouteSurface("sessions");
  fireEvent.click(
    getDomainControl(sessionsMain, `[data-session-id="${sessionId}"]`),
  );
  await waitForExactRoute(
    result,
    { kind: "conversation", sessionId },
    "conversation",
  );
  expect(result.store.getState().selectedSessionId).toBe(sessionId);
  const selectedSessionId = result.store.getState().selectedSessionId;
  if (!selectedSessionId) throw new Error("Navigation must select its session");
  return { result, sessionId: selectedSessionId };
}

function ownedQuestions(
  state: PrototypeState,
  sessionId: string,
): readonly QuestionFixture[] {
  const questionIds = new Set(
    state.projection.fixture.transcript.flatMap((item) =>
      item.kind === "question" && item.sessionId === sessionId
        ? [item.questionId]
        : [],
    ),
  );
  const questions = state.projection.fixture.questions.filter(({ id }) =>
    questionIds.has(id),
  );
  expect(questions).toHaveLength(questionIds.size);
  expect(questions.length).toBeGreaterThan(0);
  return questions;
}

function resolutionButton(
  form: HTMLElement,
  resolution: "fallback" | "decide" | "skip",
): HTMLButtonElement | null {
  const names = {
    fallback: /fallback/i,
    decide: /decide/i,
    skip: /^skip$/i,
  } as const;
  const button = within(form).queryByRole("button", {
    name: names[resolution],
  });
  if (!button) return null;
  expect(button).toBeInstanceOf(HTMLButtonElement);
  expect(button).toHaveAttribute("data-question-resolution", resolution);
  if (!(button instanceof HTMLButtonElement)) {
    throw new Error(`${resolution} resolution must use a button`);
  }
  return button;
}

function fillValidNewSession(result: RenderConceptResult): void {
  const fixture = result.store.getState().projection.fixture;
  const project = fixture.recentProjects.at(-1);
  const model = fixture.models.at(-1);
  const effort = fixture.efforts.at(-1);
  expect(project).toBeDefined();
  expect(model).toBeDefined();
  expect(effort).toBeDefined();
  if (!project || !model || !effort) return;

  fireEvent.change(screen.getByRole("textbox", { name: "Project path" }), {
    target: { value: project.path },
  });
  fireEvent.change(screen.getByRole("textbox", { name: "Prompt" }), {
    target: { value: "Review the fixture" },
  });
  fireEvent.change(screen.getByRole("combobox", { name: "Model" }), {
    target: { value: model.id },
  });
  fireEvent.change(screen.getByRole("combobox", { name: "Effort" }), {
    target: { value: effort },
  });
}

export function runConceptContract(module: ConceptModule): void {
  describe(`${module.id} concept contract`, () => {
    afterEach(cleanup);

    it.each(["ios", "android"] as const)(
      "exposes %s platform chrome without adding root controls",
      (platform) => {
        renderRoot(module, "sessions", platform);
        const primitives = getPlatformPrimitives(platform);
        const root = document.querySelector("[data-concept-root]");
        expect(root).toHaveAttribute("data-platform", platform);
        expect(root).toHaveAttribute("data-navigation", primitives.navigation);
        expect(root).toHaveAttribute("data-title", primitives.title);
        expect(root).toHaveAttribute("data-sheet", primitives.sheet);
        expect(root).toHaveAttribute("data-dialog", primitives.dialog);
        expect(root).toHaveAttribute(
          "data-minimum-target",
          String(primitives.minimumTarget),
        );
        expect(root).toHaveAttribute("data-feedback", primitives.feedback);
        expect(root).toHaveAttribute("data-back", primitives.back);
        expect(root).toHaveAttribute("data-safe-area", primitives.safeArea);
        expectRouteSurface("sessions");

        const navigation = screen.getByRole("navigation", { name: "Primary" });
        const controls = within(navigation).getAllByRole("button");
        expect(controls).toHaveLength(4);
        for (const label of Object.values(rootLabels)) {
          expect(
            within(navigation).getByRole("button", { name: label }),
          ).toBeVisible();
        }
      },
    );

    it.each(Object.entries(rootLabels))(
      "navigates through the isolated %s root control",
      async (tab, label) => {
        const target = tab as keyof typeof rootLabels;
        const initial = target === "sessions" ? "search" : "sessions";
        const result = renderRoot(module, initial);
        const navigation = screen.getByRole("navigation", { name: "Primary" });
        const controls = within(navigation).getAllByRole("button");
        expect(controls).toHaveLength(4);
        fireEvent.click(
          within(navigation).getByRole("button", { name: label }),
        );
        await waitForExactRoute(result, { kind: "root", tab: target }, target);
      },
    );

    it.each([
      ["Switch concept", "concept-switcher"],
      ["Lab Controls", "lab-controls"],
    ] as const)("opens %s through its renderer action", (name, overlay) => {
      const result = renderRoot(module, "sessions");
      fireEvent.click(screen.getByRole("button", { name }));
      expect(result.store.getState().overlay).toBe(overlay);
    });

    it("uses canonical session groups, filtering, and refresh transitions", () => {
      const result = renderRoot(module, "sessions");
      const main = expectRouteSurface("sessions");
      const initial = result.store.getState();
      for (const group of selectGroupedSessions(initial)) {
        const regions = main.querySelectorAll(
          `[data-session-group-id="${group.id}"]`,
        );
        expect(regions).toHaveLength(1);
        const region = regions[0];
        expect(region).toBeVisible();
        for (const session of group.sessions) {
          expect(
            region?.querySelector(`[data-session-id="${session.id}"]`),
          ).toBeVisible();
        }
      }

      const query = initial.projection.fixture.sessions[0]?.project;
      expect(query).toBeDefined();
      fireEvent.change(
        screen.getByRole("searchbox", { name: "Filter sessions" }),
        {
          target: { value: query ?? "" },
        },
      );
      expect(result.store.getState().sessionQuery).toBe(query);
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
      fireEvent.click(getAction("complete-refresh", main));
      expect(result.store.getState().refreshState).toBe("complete");
      expect(
        main.querySelector('[data-refresh-state="complete"]'),
      ).toBeVisible();
    });

    it("renders selector-owned search guidance, results, no-results, and focus", async () => {
      const result = renderRoot(module, "search");
      const main = expectRouteSurface("search");
      expect(selectSearchProjection(result.store.getState()).kind).toBe(
        "prompt",
      );
      expect(main.querySelector('[data-search-state="prompt"]')).toBeVisible();

      const documentFixture = result.store
        .getState()
        .projection.fixture.search.find(({ itemId }) => itemId !== null);
      expect(documentFixture).toBeDefined();
      if (!documentFixture) return;
      fireEvent.change(screen.getByRole("searchbox", { name: "Search" }), {
        target: { value: documentFixture.title },
      });
      const projection = selectSearchProjection(result.store.getState());
      expect(projection.kind).toBe("results");
      if (projection.kind !== "results") return;
      for (const item of projection.results) {
        const links = main.querySelectorAll(
          `[data-search-result-id="${item.id}"]`,
        );
        expect(links).toHaveLength(1);
        const link = links[0];
        expect(link).toHaveAttribute("data-search-kind", item.kind);
        expect(link).toHaveAttribute("data-search-context", item.context);
        expect(link).toHaveAttribute("href");
      }
      fireEvent.click(
        getDomainControl(
          main,
          `[data-search-result-id="${documentFixture.id}"]`,
        ),
      );
      await waitForExactRoute(
        result,
        {
          kind: "conversation",
          sessionId: documentFixture.sessionId,
          ...(documentFixture.itemId
            ? { focusItemId: documentFixture.itemId }
            : {}),
        },
        "conversation",
      );
      expect(result.store.getState().selectedSessionId).toBe(
        documentFixture.sessionId,
      );
      expect(result.store.getState().focusedItemId).toBe(
        documentFixture.itemId,
      );
      expect(
        document.querySelector(
          `[data-transcript-item-id="${documentFixture.itemId}"][data-focused="true"]`,
        ),
      ).toBeVisible();

      cleanup();
      const noResults = renderRoot(module, "search");
      const noResultsMain = expectRouteSurface("search");
      fireEvent.change(screen.getByRole("searchbox", { name: "Search" }), {
        target: { value: "query-with-no-fixture-match" },
      });
      expect(selectSearchProjection(noResults.store.getState()).kind).toBe(
        "no-results",
      );
      expect(screen.getByRole("searchbox", { name: "Search" })).toHaveValue(
        "query-with-no-fixture-match",
      );
      expect(
        noResultsMain.querySelector('[data-search-state="no-results"]'),
      ).toBeVisible();
    });

    it("renders an owned conversation, disclosure, composer, Work, and Voice", async () => {
      const { result, sessionId } = await openOwnedConversation(
        module,
        "conversation",
      );
      const main = expectRouteSurface("conversation");
      const items = result.store
        .getState()
        .projection.fixture.transcript.filter(
          (item) => item.sessionId === sessionId,
        );
      for (const item of items) {
        expect(
          main.querySelector(`[data-transcript-item-id="${item.id}"]`),
        ).toBeVisible();
      }
      const tool = items.find((item) => item.kind === "tool");
      expect(tool).toBeDefined();
      if (tool?.kind !== "tool") return;
      const disclosure = within(main).getByRole("button", { name: tool.label });
      expect(disclosure).toHaveAttribute("aria-expanded", "false");
      fireEvent.click(disclosure);
      expect(result.store.getState().expandedToolIds).toContain(tool.id);
      expect(disclosure).toHaveAttribute("aria-expanded", "true");

      const composer = within(main).getByRole("textbox", { name: "Message" });
      fireEvent.change(composer, { target: { value: "Continue" } });
      expect(result.store.getState().draft).toBe("Continue");
      for (const mode of ["Send", "Steer", "Queue"]) {
        expect(within(main).getByRole("button", { name: mode })).toBeVisible();
      }
      expect(within(main).getByRole("button", { name: "Work" })).toBeVisible();
      expect(within(main).getByRole("button", { name: "Voice" })).toBeVisible();
    });

    it("renders owned Work hierarchy, formatted usage, and async Back", async () => {
      const { result, sessionId } = await openOwnedConversation(module, "work");
      fireEvent.click(screen.getByRole("button", { name: "Work" }));
      await waitForExactRoute(result, { kind: "work", sessionId }, "work");
      expect(result.store.getState().selectedSessionId).toBe(sessionId);
      const main = expectRouteSurface("work");
      const { fixture } = result.store.getState().projection;
      for (const kind of ["task", "subagent", "job"] as const) {
        const nodes = fixture.work.filter(
          (node) => node.sessionId === sessionId && node.kind === kind,
        );
        expect(nodes.length).toBeGreaterThan(0);
        for (const node of nodes) {
          const elements = main.querySelectorAll(
            `[data-work-node-id="${node.id}"][data-work-kind="${kind}"]`,
          );
          expect(elements).toHaveLength(1);
          const element = elements[0];
          expect(element).toBeVisible();
          fireEvent.click(
            getDomainControl(main, `[data-work-node-id="${node.id}"]`),
          );
          expect(result.store.getState().expandedWorkIds).toContain(node.id);
        }
      }
      const usageRegions = main.querySelectorAll("[data-work-usage]");
      expect(usageRegions).toHaveLength(1);
      const usage = usageRegions[0];
      expect(usage).toHaveTextContent(formatUsage(fixture.usage.tokens));
      expect(usage).toHaveTextContent(fixture.usage.costLabel);
      expect(usage).toHaveTextContent(fixture.usage.durationLabel);
      expect(usage).toHaveTextContent(String(fixture.usage.contextPercent));

      fireEvent.click(within(main).getByRole("button", { name: "Back" }));
      await waitForExactRoute(
        result,
        { kind: "conversation", sessionId },
        "conversation",
      );
    });

    it("uses canonical question ownership and exact answer state", async () => {
      const { result, sessionId } = await openOwnedConversation(
        module,
        "question",
        "question",
      );
      const main = expectRouteSurface("conversation");
      const questions = ownedQuestions(result.store.getState(), sessionId);
      const forms = main.querySelectorAll("form[data-question-id]");
      expect(forms).toHaveLength(questions.length);

      for (const question of questions) {
        const matchingForms = main.querySelectorAll(
          `form[data-question-id="${question.id}"]`,
        );
        expect(matchingForms).toHaveLength(1);
        const form = matchingForms[0];
        if (!(form instanceof HTMLElement)) continue;
        const submit = within(form).getByRole("button", {
          name: "Submit answer",
        });
        expect(submit).toBeDisabled();
        for (const [resolution, permitted] of [
          ["fallback", question.allowFallback],
          ["decide", question.allowDecide],
          ["skip", question.allowSkip],
        ] as const) {
          const button = resolutionButton(form, resolution);
          if (permitted) expect(button).toBeVisible();
          else expect(button).not.toBeInTheDocument();
        }

        const option = question.options[0];
        expect(option).toBeDefined();
        if (!option) continue;
        fireEvent.click(within(form).getByLabelText(option.label));
        expect(
          result.store.getState().answers[question.id]?.selectedOptionIds,
        ).toEqual([option.id]);
        if (question.allowNote) {
          fireEvent.change(
            within(form).getByRole("textbox", { name: "Note" }),
            {
              target: { value: "Fixture note" },
            },
          );
          expect(result.store.getState().answers[question.id]?.note).toBe(
            "Fixture note",
          );
        } else {
          expect(
            within(form).queryByRole("textbox", { name: "Note" }),
          ).not.toBeInTheDocument();
        }
        expect(submit).toBeEnabled();
        fireEvent.click(submit);
        expect(result.store.getState().answers[question.id]).toEqual({
          selectedOptionIds: [option.id],
          note: question.allowNote ? "Fixture note" : "",
          resolution: "answer",
          submitted: true,
        });
      }

      for (const resolution of ["fallback", "decide", "skip"] as const) {
        for (const originalQuestion of questions) {
          const permitted =
            resolution === "fallback"
              ? originalQuestion.allowFallback
              : resolution === "decide"
                ? originalQuestion.allowDecide
                : originalQuestion.allowSkip;
          if (!permitted) continue;
          cleanup();
          const opened = await openOwnedConversation(
            module,
            "question",
            "question",
          );
          const owned = ownedQuestions(
            opened.result.store.getState(),
            opened.sessionId,
          );
          const question = owned.find(({ id }) => id === originalQuestion.id);
          expect(question).toBeDefined();
          if (!question) continue;
          const form = document.querySelector(
            `form[data-question-id="${question.id}"]`,
          );
          expect(form).toBeInstanceOf(HTMLFormElement);
          if (!(form instanceof HTMLElement)) continue;
          const button = resolutionButton(form, resolution);
          expect(button).toBeVisible();
          if (!button) continue;
          fireEvent.click(button);
          expect(opened.result.store.getState().answers[question.id]).toEqual({
            selectedOptionIds: [],
            note: "",
            resolution,
            submitted: true,
          });
        }
      }
    });

    it("drives recent and typed project selection through all new-session outcomes", async () => {
      const result = renderRoot(module, "new");
      const main = expectRouteSurface("new");
      const { fixture } = result.store.getState().projection;
      const recent = fixture.recentProjects[0];
      expect(recent).toBeDefined();
      if (!recent) return;
      fireEvent.click(
        getDomainControl(main, `[data-project-id="${recent.id}"]`),
      );
      expect(result.store.getState().newSession.project).toBe(recent.path);
      fillValidNewSession(result);
      const submit = screen.getByRole("button", { name: "Start session" });
      expect(submit).toBeEnabled();
      fireEvent.click(submit);
      expect(result.store.getState().newSession.outcome).toBe("starting");
      expect(
        main.querySelector('[data-new-session-state="starting"]'),
      ).toBeVisible();
      fireEvent.click(getAction("complete-new-session-success", main));
      const startedSessionId = result.store.getState().selectedSessionId;
      expect(startedSessionId).toBeTruthy();
      if (!startedSessionId) return;
      await waitForExactRoute(
        result,
        { kind: "conversation", sessionId: startedSessionId },
        "conversation",
      );
      expect(result.store.getState().newSession.outcome).toBe("success");

      cleanup();
      const failure = renderRoot(module, "new");
      const failureMain = expectRouteSurface("new");
      fillValidNewSession(failure);
      fireEvent.click(screen.getByRole("button", { name: "Start session" }));
      fireEvent.click(getAction("complete-new-session-failure", failureMain));
      expect(failure.store.getState().newSession.outcome).toBe("failure");
      expect(
        failureMain.querySelector('[data-new-session-state="failure"]'),
      ).toBeVisible();
    });

    it("scopes exact fictional Hubs and settings controls to main", () => {
      const result = renderRoot(module, "settings");
      const main = expectRouteSurface("settings");
      fireEvent.change(
        within(main).getByRole("combobox", { name: "Appearance" }),
        {
          target: { value: "dark" },
        },
      );
      expect(result.store.getState().appearance).toBe("dark");
      fireEvent.click(
        within(main).getByRole("checkbox", { name: "Speak responses" }),
      );
      expect(result.store.getState().voicePreferences.speakResponses).toBe(
        false,
      );
      fireEvent.change(
        within(main).getByRole("combobox", { name: "Speech rate" }),
        {
          target: { value: "fast" },
        },
      );
      expect(result.store.getState().voicePreferences.rate).toBe("fast");

      const hubs = result.store.getState().projection.fixture.hubs;
      const rows = main.querySelectorAll("[data-hub-id]");
      expect(rows).toHaveLength(hubs.length);
      for (const hub of hubs) {
        const matchingRows = main.querySelectorAll(`[data-hub-id="${hub.id}"]`);
        expect(matchingRows).toHaveLength(1);
        const row = matchingRows[0];
        expect(row).toHaveAttribute("data-fictional", "true");
        expect(row).toHaveTextContent(hub.context);
      }
      expect(
        main.querySelector('[data-offline-prototype="true"]'),
      ).toBeVisible();
      expect(
        screen.getByRole("button", { name: "Switch concept" }),
      ).toBeVisible();
      expect(
        screen.getByRole("button", { name: "Lab Controls" }),
      ).toBeVisible();
    });

    it("keeps Voice Stop distinct from End and awaits End navigation", async () => {
      const { result, sessionId } = await openOwnedConversation(
        module,
        "voice",
        "voice",
      );
      fireEvent.click(screen.getByRole("button", { name: "Voice" }));
      await waitForExactRoute(result, { kind: "voice", sessionId }, "voice");
      expect(result.store.getState().selectedSessionId).toBe(sessionId);
      const main = expectRouteSurface("voice");
      const step = selectCurrentVoiceStep(result.store.getState());
      expect(step).not.toBeNull();
      if (!step) return;
      expect(
        main.querySelector(`[data-voice-state="${step.state}"]`),
      ).toBeVisible();
      expect(within(main).getByText(step.caption)).toBeVisible();
      const meter = within(main).getByRole("progressbar", {
        name: "Voice level",
      });
      expect(meter).toHaveAttribute(
        "aria-valuenow",
        String(selectCurrentVoiceLevel(result.store.getState())),
      );
      fireEvent.click(within(main).getByRole("button", { name: "Mute" }));
      expect(result.store.getState().voice.muted).toBe(true);
      fireEvent.click(within(main).getByRole("button", { name: "Stop" }));
      expect(result.store.getState().voice.stopped).toBe(true);
      expect(result.store.getState().voice.ended).toBe(false);
      expect(result.store.getState().route).toEqual({
        kind: "voice",
        sessionId,
      });
      fireEvent.click(within(main).getByRole("button", { name: "End" }));
      await waitForExactRoute(
        result,
        { kind: "conversation", sessionId },
        "conversation",
      );
      expect(result.store.getState().voice.ended).toBe(true);
    });

    it("awaits the accessible Voice Close control through popstate", async () => {
      const { result, sessionId } = await openOwnedConversation(
        module,
        "voice",
        "voice",
      );
      fireEvent.click(screen.getByRole("button", { name: "Voice" }));
      await waitForExactRoute(result, { kind: "voice", sessionId }, "voice");
      const main = expectRouteSurface("voice");
      fireEvent.click(within(main).getByRole("button", { name: "Close" }));
      await waitForExactRoute(
        result,
        { kind: "conversation", sessionId },
        "conversation",
      );
    });
  });
}
