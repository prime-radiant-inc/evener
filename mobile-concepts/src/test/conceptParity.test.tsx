import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { StoreApi } from "zustand/vanilla";
import { LabControls } from "../app/LabControls";
import { RootApp } from "../app/RootApp";
import type { ConceptModule } from "../concepts/contract";
import { conceptRegistry } from "../concepts/registry";
import { formatUsage } from "../concepts/shared/format";
import { canonicalFixture } from "../core/fixtures";
import {
  createNavigationController,
  type NavigationController,
} from "../core/history";
import type { ConceptId, Route, ScenarioId } from "../core/model";
import { defaultPreferences, preferenceStorageKey } from "../core/persistence";
import { getPlatformPrimitives } from "../core/platform";
import {
  selectCurrentVoiceLevel,
  selectCurrentVoiceStep,
  selectGroupedSessions,
  selectNewSessionValidity,
  selectQuestionSubmitValidity,
  selectSearchProjection,
} from "../core/selectors";
import {
  createPrototypeStore,
  PrototypeProvider,
  type PrototypeStore,
} from "../core/store";

interface Harness {
  controller: NavigationController;
  store: StoreApi<PrototypeStore>;
}

const controllers: NavigationController[] = [];
const pendingPopstateWaiters = new Set<() => void>();
const conceptLabels: Record<ConceptId, string> = {
  stillwater: "Stillwater",
  constellation: "Constellation",
  "field-notes": "Field Notes",
};
type AlternateQuestionResolution = "fallback" | "decide" | "skip";

function permitsResolution(
  question: (typeof canonicalFixture.questions)[number],
  resolution: AlternateQuestionResolution,
): boolean {
  if (resolution === "fallback") return question.allowFallback;
  if (resolution === "decide") return question.allowDecide;
  return question.allowSkip;
}

const canonicalQuestionResolutionPairs = canonicalFixture.questions.flatMap(
  (question) =>
    (["fallback", "decide", "skip"] as const).flatMap((resolution) =>
      permitsResolution(question, resolution) ? [{ question, resolution }] : [],
    ),
);
const questionResolutionCases = Object.values(conceptRegistry).flatMap(
  (module) =>
    canonicalQuestionResolutionPairs.map(({ question, resolution }) => ({
      module,
      question,
      resolution,
    })),
);

function memoryStorage(concept: ConceptId, scenario = "baseline") {
  const values = new Map<string, string>([
    [
      preferenceStorageKey,
      JSON.stringify({ ...defaultPreferences, concept, scenario }),
    ],
  ]);
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => values.set(key, value),
    removeItem: (key: string) => values.delete(key),
  };
}

function renderHarness(
  module: ConceptModule,
  scenario: ScenarioId = "baseline",
): Harness {
  const store = createPrototypeStore({
    platform: "ios",
    storage: memoryStorage(module.id, scenario),
    fixtureInput: canonicalFixture,
    diagnostics: { report: vi.fn() },
  });
  const controller = createNavigationController(store, window);
  controllers.push(controller);
  const primitives = getPlatformPrimitives("ios");
  render(
    <PrototypeProvider store={store}>
      <RootApp dispatch={controller.dispatch} primitives={primitives} />
      <LabControls dispatch={controller.dispatch} primitives={primitives} />
    </PrototypeProvider>,
  );
  return { controller, store };
}

function main(route: string): HTMLElement {
  const mains = screen.getAllByRole("main");
  expect(mains).toHaveLength(1);
  const current = mains[0];
  if (!current) throw new Error("expected one main landmark");
  expect(current).toHaveAttribute("data-route", route);
  return current;
}

function domainControl(scope: ParentNode, selector: string): HTMLElement {
  const matches = scope.querySelectorAll(selector);
  expect(matches).toHaveLength(1);
  const match = matches[0];
  if (!(match instanceof HTMLElement)) throw new Error(`missing ${selector}`);
  if (match.matches("button, a[href]")) return match;
  const controls = match.querySelectorAll("button, a[href]");
  expect(controls).toHaveLength(1);
  const control = controls[0];
  if (!(control instanceof HTMLElement))
    throw new Error(`missing ${selector} control`);
  return control;
}

function action(scope: ParentNode, name: string): HTMLButtonElement {
  const element = scope.querySelector(`[data-action="${name}"]`);
  expect(element).toBeInstanceOf(HTMLButtonElement);
  return element as HTMLButtonElement;
}

function nextPopState(): Promise<PopStateEvent> {
  return new Promise((resolve, reject) => {
    let settled = false;
    const cancel = () => {
      if (settled) return;
      settled = true;
      window.clearTimeout(timeout);
      window.removeEventListener("popstate", onPopState);
      pendingPopstateWaiters.delete(cancel);
    };
    const timeout = window.setTimeout(() => {
      cancel();
      reject(new Error("timed out awaiting real popstate"));
    }, 1_000);
    const onPopState = (event: PopStateEvent) => {
      cancel();
      resolve(event);
    };
    pendingPopstateWaiters.add(cancel);
    window.addEventListener("popstate", onPopState);
  });
}

async function clickBack(expected: Route): Promise<void> {
  const popped = nextPopState();
  fireEvent.click(screen.getByRole("button", { name: "Back" }));
  await popped;
  await waitFor(() =>
    expect(
      main(expected.kind === "root" ? expected.tab : expected.kind),
    ).toBeVisible(),
  );
}

async function setQuestionScenario(): Promise<void> {
  fireEvent.click(
    within(screen.getByRole("main")).getByRole("button", {
      name: "Lab Controls",
    }),
  );
  expect(screen.getByRole("dialog", { name: "Lab Controls" })).toBeVisible();
  const popped = nextPopState();
  fireEvent.click(screen.getByRole("radio", { name: "question" }));
  await popped;
  await waitFor(() => expect(main("sessions")).toBeVisible());
}

async function switchTo(concept: ConceptId): Promise<void> {
  const opener = screen.getByRole("button", { name: "Switch concept" });
  opener.focus();
  fireEvent.click(opener);
  const popped = nextPopState();
  fireEvent.click(
    screen.getByRole("button", {
      name: `Select ${conceptLabels[concept]}`,
    }),
  );
  await popped;
  await waitFor(() =>
    expect(document.querySelector(`.concept-${concept}`)).toBeInTheDocument(),
  );
}

async function resetThroughLab(): Promise<void> {
  fireEvent.click(
    within(screen.getByRole("main")).getByRole("button", {
      name: "Lab Controls",
    }),
  );
  const popped = nextPopState();
  fireEvent.click(screen.getByRole("button", { name: "Reset prototype" }));
  await popped;
  await waitFor(() =>
    expect(screen.getByTestId("concept-gallery")).toBeVisible(),
  );
}

function selectConcept(module: ConceptModule): void {
  fireEvent.click(
    screen.getByRole("button", { name: `Select ${conceptLabels[module.id]}` }),
  );
  expect(main("sessions")).toBeVisible();
}

function fillNewSession(projectPath: string, prompt: string): void {
  fireEvent.change(screen.getByRole("textbox", { name: "Project path" }), {
    target: { value: projectPath },
  });
  fireEvent.change(screen.getByRole("textbox", { name: "Prompt" }), {
    target: { value: prompt },
  });
  fireEvent.change(screen.getByRole("combobox", { name: "Model" }), {
    target: { value: canonicalFixture.models[2]?.id },
  });
  fireEvent.change(screen.getByRole("combobox", { name: "Effort" }), {
    target: { value: "high" },
  });
}

beforeEach(() => window.history.replaceState(null, "", "/"));
afterEach(() => {
  for (const cancel of [...pendingPopstateWaiters]) cancel();
  const errors: unknown[] = [];
  const attempt = (operation: () => void) => {
    try {
      operation();
    } catch (error) {
      errors.push(error);
    }
  };
  attempt(cleanup);
  for (const controller of controllers.splice(0)) {
    attempt(() => controller.dispose());
  }
  attempt(() => vi.restoreAllMocks());
  if (errors.length > 0) {
    throw new AggregateError(errors, "parity cleanup failed");
  }
});

describe("cross-concept behavioral parity", () => {
  it.each(Object.values(conceptRegistry))(
    "$id executes the complete shared behavior sequence",
    async (module) => {
      const { store } = renderHarness(module);

      // Sessions: shared selector projection, filtering, refresh, and opening.
      const sessionsMain = main("sessions");
      const session = canonicalFixture.sessions.find(
        ({ id }) => id === "session-native-client",
      );
      expect(session).toBeDefined();
      if (!session) return;
      fireEvent.change(
        screen.getByRole("searchbox", { name: "Filter sessions" }),
        {
          target: { value: session.title },
        },
      );
      expect(store.getState().sessionQuery).toBe(session.title);
      expect(
        selectGroupedSessions(store.getState()).flatMap(
          (group) => group.sessions,
        ),
      ).toEqual([session]);
      fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
      expect(store.getState().refreshState).toBe("refreshing");
      fireEvent.click(action(sessionsMain, "complete-refresh"));
      expect(store.getState().refreshState).toBe("complete");
      fireEvent.click(
        domainControl(sessionsMain, `[data-session-id="${session.id}"]`),
      );
      await waitFor(() => expect(main("conversation")).toBeVisible());
      expect(store.getState().route).toEqual({
        kind: "conversation",
        sessionId: session.id,
      });

      // Search prompt, contextual result/focus, and no-result context.
      await clickBack({ kind: "root", tab: "sessions" });
      fireEvent.click(screen.getByRole("button", { name: "Search" }));
      const searchMain = main("search");
      expect(selectSearchProjection(store.getState())).toEqual({
        kind: "prompt",
        query: "",
        results: [],
      });
      const transcriptResult = canonicalFixture.search.find(
        ({ kind }) => kind === "transcript",
      );
      expect(transcriptResult).toBeDefined();
      if (!transcriptResult?.itemId) return;
      fireEvent.change(screen.getByRole("searchbox", { name: "Search" }), {
        target: { value: transcriptResult.title },
      });
      const projection = selectSearchProjection(store.getState());
      expect(projection.kind).toBe("results");
      fireEvent.click(
        domainControl(
          searchMain,
          `[data-search-result-id="${transcriptResult.id}"]`,
        ),
      );
      await waitFor(() => {
        const focused = document.querySelector(
          `[data-transcript-item-id="${transcriptResult.itemId}"][data-focused="true"]`,
        );
        expect(focused).toHaveFocus();
      });
      await clickBack({ kind: "root", tab: "search" });
      fireEvent.change(screen.getByRole("searchbox", { name: "Search" }), {
        target: { value: "unmatched deterministic query" },
      });
      expect(selectSearchProjection(store.getState())).toEqual({
        kind: "no-results",
        query: "unmatched deterministic query",
        results: [],
      });
      expect(
        main("search").querySelector('[data-search-state="no-results"]'),
      ).toBeVisible();

      // Contextual tool result, disclosure, Work hierarchy, and all usage fields.
      const toolResult = canonicalFixture.search.find(
        ({ kind }) => kind === "tool",
      );
      expect(toolResult).toBeDefined();
      if (!toolResult?.itemId) return;
      fireEvent.change(screen.getByRole("searchbox", { name: "Search" }), {
        target: { value: toolResult.title },
      });
      fireEvent.click(
        domainControl(
          main("search"),
          `[data-search-result-id="${toolResult.id}"]`,
        ),
      );
      await waitFor(() => expect(main("conversation")).toBeVisible());
      const tool = canonicalFixture.transcript.find(
        (item) => item.id === toolResult.itemId && item.kind === "tool",
      );
      expect(tool?.kind).toBe("tool");
      if (tool?.kind !== "tool") return;
      const disclosure = within(main("conversation")).getByRole("button", {
        name: tool.label,
      });
      expect(disclosure).toHaveAttribute("aria-expanded", "false");
      fireEvent.click(disclosure);
      expect(store.getState().expandedToolIds).toContain(tool.id);
      expect(disclosure).toHaveAttribute("aria-expanded", "true");
      fireEvent.click(screen.getByRole("button", { name: "Work" }));
      await waitFor(() => expect(main("work")).toBeVisible());
      for (const nodeId of [
        "work-task-shell",
        "work-subagent-navigation",
        "work-job-route-matrix",
      ]) {
        const node = domainControl(
          main("work"),
          `[data-work-node-id="${nodeId}"]`,
        );
        fireEvent.click(node);
        expect(store.getState().expandedWorkIds).toContain(nodeId);
      }
      const usage = main("work").querySelector("[data-work-usage]");
      expect(usage).toHaveTextContent(
        formatUsage(canonicalFixture.usage.tokens),
      );
      expect(usage).toHaveTextContent(canonicalFixture.usage.costLabel);
      expect(usage).toHaveTextContent(canonicalFixture.usage.durationLabel);
      expect(usage).toHaveTextContent(
        String(canonicalFixture.usage.contextPercent),
      );
      await clickBack({
        kind: "conversation",
        sessionId: "session-native-client",
        focusItemId: tool.id,
      });

      // Question validity, single/multiple selection, notes, and exact reducer answers.
      await setQuestionScenario();
      const questionSession = canonicalFixture.sessions.find(
        ({ id }) => id === "session-mobile-release",
      );
      expect(questionSession).toBeDefined();
      if (!questionSession) return;
      fireEvent.click(
        domainControl(
          main("sessions"),
          `[data-session-id="${questionSession.id}"]`,
        ),
      );
      await waitFor(() => expect(main("conversation")).toBeVisible());
      for (const question of canonicalFixture.questions) {
        const form = main("conversation").querySelector(
          `form[data-question-id="${question.id}"]`,
        );
        expect(form).toBeInstanceOf(HTMLFormElement);
        if (!(form instanceof HTMLFormElement)) continue;
        const submit = within(form).getByRole("button", {
          name: "Submit answer",
        });
        expect(submit).toBeDisabled();
        const selected =
          question.mode === "multiple"
            ? question.options.slice(0, 2)
            : question.options.slice(0, 1);
        for (const option of selected) {
          const control = within(form).getByRole(
            question.mode === "multiple" ? "checkbox" : "radio",
            { name: option.label },
          );
          fireEvent.click(control);
          expect(control).toBeChecked();
        }
        fireEvent.change(within(form).getByRole("textbox", { name: "Note" }), {
          target: { value: `${question.mode} note` },
        });
        expect(
          selectQuestionSubmitValidity(store.getState(), question.id, "answer"),
        ).toBe(true);
        expect(submit).toBeEnabled();
        fireEvent.click(submit);
        expect(store.getState().answers[question.id]).toEqual({
          selectedOptionIds: selected.map(({ id }) => id),
          note: `${question.mode} note`,
          resolution: "answer",
          submitted: true,
        });
        expect(
          main("conversation").querySelector(
            `[data-question-id="${question.id}"][data-question-resolution="answer"]`,
          ),
        ).toHaveAttribute("role", "status");
      }

      // Return through actual history, focus a tool, then exercise every composer mode.
      await clickBack({ kind: "root", tab: "sessions" });
      fireEvent.click(screen.getByRole("button", { name: "Search" }));
      fireEvent.change(screen.getByRole("searchbox", { name: "Search" }), {
        target: { value: toolResult.title },
      });
      fireEvent.click(
        domainControl(
          main("search"),
          `[data-search-result-id="${toolResult.id}"]`,
        ),
      );
      await waitFor(() => expect(main("conversation")).toBeVisible());
      fireEvent.click(
        within(main("conversation")).getByRole("button", { name: tool.label }),
      );
      for (const mode of ["Send", "Steer", "Queue"] as const) {
        const button = within(main("conversation")).getByRole("button", {
          name: mode,
        });
        fireEvent.click(button);
        expect(button).toHaveAttribute("aria-pressed", "true");
        expect(store.getState().composerMode).toBe(mode.toLowerCase());
      }
      fireEvent.change(screen.getByRole("textbox", { name: "Message" }), {
        target: { value: "Queue this deterministic turn" },
      });
      fireEvent.click(screen.getByRole("button", { name: "Submit message" }));
      expect(store.getState().composerMode).toBe("running");
      expect(store.getState().syntheticTurn).toBe("starting");
      expect(screen.getByRole("status")).toHaveTextContent(
        "Response is running",
      );
      fireEvent.click(screen.getByRole("button", { name: "Stop response" }));
      expect(store.getState().composerMode).toBe("send");
      expect(store.getState().syntheticTurn).toBe("stopped");
      fireEvent.change(screen.getByRole("textbox", { name: "Message" }), {
        target: { value: "Preserve this cross-concept draft" },
      });

      const preservedWorkIds = [
        "work-task-shell",
        "work-subagent-navigation",
        "work-job-route-matrix",
      ] as const;
      fireEvent.click(screen.getByRole("button", { name: "Work" }));
      await waitFor(() => expect(main("work")).toBeVisible());
      for (const nodeId of preservedWorkIds) {
        fireEvent.click(
          domainControl(main("work"), `[data-work-node-id="${nodeId}"]`),
        );
        expect(store.getState().expandedWorkIds).toContain(nodeId);
      }
      await clickBack({
        kind: "conversation",
        sessionId: "session-native-client",
        focusItemId: tool.id,
      });

      // Switching out and back preserves all interaction state and both
      // disclosure sets, including the controls after Work is revisited.
      const conceptIds = Object.keys(conceptRegistry) as ConceptId[];
      const alternate = conceptIds.find((id) => id !== module.id);
      expect(alternate).toBeDefined();
      if (!alternate) return;
      const beforeSwitch = store.getState();
      await switchTo(alternate);
      await switchTo(module.id);
      const afterSwitch = store.getState();
      expect({
        route: afterSwitch.route,
        scenario: afterSwitch.scenario,
        focusedItemId: afterSwitch.focusedItemId,
        draft: afterSwitch.draft,
        answers: afterSwitch.answers,
        expandedToolIds: [...afterSwitch.expandedToolIds],
        expandedWorkIds: [...afterSwitch.expandedWorkIds],
      }).toEqual({
        route: beforeSwitch.route,
        scenario: beforeSwitch.scenario,
        focusedItemId: beforeSwitch.focusedItemId,
        draft: beforeSwitch.draft,
        answers: beforeSwitch.answers,
        expandedToolIds: [...beforeSwitch.expandedToolIds],
        expandedWorkIds: [...beforeSwitch.expandedWorkIds],
      });
      expect(
        document.querySelector(
          `[data-transcript-item-id="${tool.id}"][data-focused="true"]`,
        ),
      ).toBeVisible();
      await waitFor(() =>
        expect(
          screen.getByRole("button", { name: "Switch concept" }),
        ).toHaveFocus(),
      );
      fireEvent.click(screen.getByRole("button", { name: "Work" }));
      await waitFor(() => expect(main("work")).toBeVisible());
      for (const nodeId of preservedWorkIds) {
        const node = canonicalFixture.work.find(({ id }) => id === nodeId);
        expect(node).toBeDefined();
        if (!node) continue;
        const wrapper = main("work").querySelector(
          `[data-work-node-id="${nodeId}"]`,
        );
        expect(wrapper).toBeInstanceOf(HTMLElement);
        if (!(wrapper instanceof HTMLElement)) continue;
        expect(
          within(wrapper).getByRole("button", { name: node.title }),
        ).toHaveAttribute("aria-expanded", "true");
      }
      await clickBack({
        kind: "conversation",
        sessionId: "session-native-client",
        focusItemId: tool.id,
      });

      // Recent-project success, reset, then typed-project failure with form preservation.
      await clickBack({ kind: "root", tab: "search" });
      fireEvent.click(screen.getByRole("button", { name: "New Session" }));
      const recent = canonicalFixture.recentProjects[0];
      expect(recent).toBeDefined();
      if (!recent) return;
      fireEvent.click(
        domainControl(main("new"), `[data-project-id="${recent.id}"]`),
      );
      expect(store.getState().newSession.project).toBe(recent.path);
      fillNewSession(recent.path, "Successful local session");
      expect(selectNewSessionValidity(store.getState())).toBe(true);
      fireEvent.click(screen.getByRole("button", { name: "Start session" }));
      expect(store.getState().newSession.outcome).toBe("starting");
      fireEvent.click(action(main("new"), "complete-new-session-success"));
      await waitFor(() => expect(main("conversation")).toBeVisible());
      expect(store.getState().newSession.outcome).toBe("success");

      await resetThroughLab();
      expect(store.getState().concept).toBeNull();
      selectConcept(module);
      fireEvent.click(screen.getByRole("button", { name: "New Session" }));
      const typed = canonicalFixture.recentProjects.at(-1);
      expect(typed).toBeDefined();
      if (!typed) return;
      fillNewSession(typed.path, "Preserve failed session form");
      const failureDraft = { ...store.getState().newSession };
      fireEvent.click(screen.getByRole("button", { name: "Start session" }));
      fireEvent.click(action(main("new"), "complete-new-session-failure"));
      expect(store.getState().newSession).toEqual({
        ...failureDraft,
        outcome: "failure",
        errorCode: "synthetic-start-failed",
      });
      expect(
        main("new").querySelector('[data-new-session-state="failure"]'),
      ).toHaveAttribute("role", "alert");

      // Every deterministic Voice state/level, then Stop without End, then End.
      fireEvent.click(screen.getByRole("button", { name: "Sessions" }));
      fireEvent.click(
        domainControl(
          main("sessions"),
          '[data-session-id="session-native-client"]',
        ),
      );
      await waitFor(() => expect(main("conversation")).toBeVisible());
      fireEvent.click(screen.getByRole("button", { name: "Voice" }));
      await waitFor(() => expect(main("voice")).toBeVisible());
      for (const voiceStep of canonicalFixture.voiceSteps) {
        fireEvent.click(
          screen.getByRole("button", {
            name: `Set voice state: ${voiceStep.state}`,
          }),
        );
        expect(selectCurrentVoiceStep(store.getState())).toEqual(voiceStep);
        expect(selectCurrentVoiceLevel(store.getState())).toBe(voiceStep.level);
        expect(
          screen.getByRole("progressbar", { name: "Voice level" }),
        ).toHaveAttribute("aria-valuenow", String(voiceStep.level));
        expect(
          main("voice").querySelector(
            `[data-voice-state="${voiceStep.state}"]`,
          ),
        ).toBeVisible();
      }
      fireEvent.click(screen.getByRole("button", { name: "Stop" }));
      expect(store.getState().voice).toMatchObject({
        stopped: true,
        ended: false,
      });
      expect(store.getState().route).toEqual({
        kind: "voice",
        sessionId: "session-native-client",
      });
      fireEvent.click(screen.getByRole("button", { name: "End" }));
      await waitFor(() => expect(main("conversation")).toBeVisible());
      expect(store.getState().voice).toMatchObject({
        stopped: false,
        ended: true,
      });

      // Consume the Voice browser entry, then the Conversation entry, via real popstate.
      const consumedVoice = nextPopState();
      fireEvent.click(screen.getByRole("button", { name: "Back" }));
      await consumedVoice;
      expect(store.getState().route.kind).toBe("conversation");
      await clickBack({ kind: "root", tab: "sessions" });

      // Display, voice, reset, and exact defaults from Settings.
      fireEvent.click(screen.getByRole("button", { name: "Settings" }));
      fireEvent.change(screen.getByRole("combobox", { name: "Appearance" }), {
        target: { value: "dark" },
      });
      fireEvent.change(screen.getByRole("combobox", { name: "Text size" }), {
        target: { value: "accessibility" },
      });
      fireEvent.click(screen.getByRole("checkbox", { name: "Reduce motion" }));
      fireEvent.click(
        screen.getByRole("checkbox", { name: "Speak responses" }),
      );
      fireEvent.change(screen.getByRole("combobox", { name: "Speech rate" }), {
        target: { value: "fast" },
      });
      expect(store.getState()).toMatchObject({
        appearance: "dark",
        textScale: "accessibility",
        reducedMotion: true,
        voicePreferences: { speakResponses: false, rate: "fast" },
      });
      await resetThroughLab();
      expect(store.getState()).toMatchObject({
        concept: null,
        appearance: "system",
        textScale: "standard",
        reducedMotion: false,
        voicePreferences: { speakResponses: true, rate: "normal" },
      });
    },
    20_000,
  );

  it.each(Object.values(conceptRegistry))(
    "$id enforces the identical offline read-only flow and Retry recovery",
    async (module) => {
      const { controller, store } = renderHarness(module, "offline");
      const sessionsMain = main("sessions");
      expect(
        sessionsMain.querySelector('[data-offline-policy="read-only"]'),
      ).toHaveAttribute("role", "status");
      expect(screen.getByText("Native mobile client")).toBeVisible();

      fireEvent.click(
        domainControl(
          sessionsMain,
          '[data-session-id="session-native-client"]',
        ),
      );
      await waitFor(() => expect(main("conversation")).toBeVisible());
      expect(screen.getByText("Inspect fixture schema")).toBeVisible();
      const composer = screen.getByRole("textbox", { name: "Message" });
      const composerBefore = {
        draft: store.getState().draft,
        composerMode: store.getState().composerMode,
        syntheticTurn: store.getState().syntheticTurn,
      };
      expect(composer).toBeDisabled();
      const submitMessage = screen.getByRole("button", {
        name: "Submit message",
      });
      expect(submitMessage).toBeDisabled();
      controller.dispatch({ type: "submitComposer" });
      expect({
        draft: store.getState().draft,
        composerMode: store.getState().composerMode,
        syntheticTurn: store.getState().syntheticTurn,
      }).toEqual(composerBefore);
      await clickBack({ kind: "root", tab: "sessions" });

      fireEvent.click(
        domainControl(
          main("sessions"),
          '[data-session-id="session-mobile-release"]',
        ),
      );
      await waitFor(() => expect(main("conversation")).toBeVisible());
      const questionForm = main("conversation").querySelector(
        'form[data-question-id="question-release-focus"]',
      );
      expect(questionForm).toBeInstanceOf(HTMLFormElement);
      if (!(questionForm instanceof HTMLFormElement)) return;
      const option = within(questionForm).getByRole("radio", {
        name: "Navigation",
      });
      const fallback = within(questionForm).getByRole("button", {
        name: /fallback/i,
      });
      expect(option).toBeDisabled();
      expect(fallback).toBeDisabled();
      controller.dispatch({
        type: "resolveQuestion",
        questionId: "question-release-focus",
        resolution: "fallback",
      });
      expect(
        store.getState().answers["question-release-focus"],
      ).toBeUndefined();
      await clickBack({ kind: "root", tab: "sessions" });

      fireEvent.click(screen.getByRole("button", { name: "New Session" }));
      const project = canonicalFixture.recentProjects[0];
      expect(project).toBeDefined();
      if (!project) return;
      fillNewSession(project.path, "Offline draft remains local");
      expect(selectNewSessionValidity(store.getState())).toBe(true);
      const start = screen.getByRole("button", { name: "Start session" });
      expect(start).toBeDisabled();
      controller.dispatch({ type: "submitNewSession" });
      expect(store.getState().newSession.outcome).toBe("editing");

      fireEvent.click(screen.getByRole("button", { name: "Settings" }));
      fireEvent.change(screen.getByRole("combobox", { name: "Appearance" }), {
        target: { value: "dark" },
      });
      fireEvent.click(
        screen.getByRole("checkbox", { name: "Speak responses" }),
      );
      expect(store.getState()).toMatchObject({
        appearance: "dark",
        voicePreferences: { speakResponses: false },
      });

      fireEvent.click(screen.getByRole("button", { name: "Sessions" }));
      fireEvent.click(
        domainControl(
          main("sessions"),
          '[data-session-id="session-native-client"]',
        ),
      );
      await waitFor(() => expect(main("conversation")).toBeVisible());
      fireEvent.click(screen.getByRole("button", { name: "Voice" }));
      await waitFor(() => expect(main("voice")).toBeVisible());
      const voiceBefore = store.getState().voice;
      const listening = screen.getByRole("button", {
        name: "Set voice state: listening",
      });
      const stop = screen.getByRole("button", { name: "Stop" });
      expect(listening).toBeDisabled();
      expect(stop).toBeDisabled();
      controller.dispatch({ type: "setVoiceState", state: "listening" });
      controller.dispatch({ type: "stopVoice" });
      expect(store.getState().voice).toEqual(voiceBefore);

      const recovered = nextPopState();
      fireEvent.click(
        within(
          main("voice").querySelector(
            '[data-offline-policy="read-only"]',
          ) as HTMLElement,
        ).getByRole("button", { name: "Retry" }),
      );
      await recovered;
      await waitFor(() => expect(main("sessions")).toBeVisible());
      expect(store.getState().scenario).toBe("baseline");
      expect(
        main("sessions").querySelector('[data-offline-policy="read-only"]'),
      ).not.toBeInTheDocument();
      fireEvent.click(
        domainControl(
          main("sessions"),
          '[data-session-id="session-native-client"]',
        ),
      );
      await waitFor(() => expect(main("conversation")).toBeVisible());
      expect(screen.getByRole("textbox", { name: "Message" })).toBeEnabled();
    },
  );

  it.each(questionResolutionCases)(
    "$module.id reaches $question.id:$resolution",
    async ({ module, question, resolution }) => {
      const { store } = renderHarness(module, "question");
      fireEvent.click(
        domainControl(
          main("sessions"),
          '[data-session-id="session-mobile-release"]',
        ),
      );
      await waitFor(() => expect(main("conversation")).toBeVisible());
      const form = main("conversation").querySelector(
        `form[data-question-id="${question.id}"]`,
      );
      expect(form).toBeInstanceOf(HTMLFormElement);
      if (!(form instanceof HTMLFormElement)) return;
      const name =
        resolution === "fallback"
          ? /fallback/i
          : resolution === "decide"
            ? /decide/i
            : /^skip$/i;
      const button = within(form).getByRole("button", { name });
      expect(button).toHaveAttribute("data-question-resolution", resolution);
      expect(
        selectQuestionSubmitValidity(store.getState(), question.id, resolution),
      ).toBe(true);
      fireEvent.click(button);
      expect(store.getState().answers[question.id]).toEqual({
        selectedOptionIds: [],
        note: "",
        resolution,
        submitted: true,
      });
      expect(
        main("conversation").querySelector(
          `[data-question-id="${question.id}"][data-question-resolution="${resolution}"]`,
        ),
      ).toHaveAttribute("role", "status");
    },
  );

  it("locks all five permitted question-resolution pairs across three concepts", () => {
    expect(
      canonicalQuestionResolutionPairs.map(
        ({ question, resolution }) => `${question.id}:${resolution}`,
      ),
    ).toEqual([
      "question-release-focus:fallback",
      "question-release-focus:decide",
      "question-release-checks:fallback",
      "question-release-checks:decide",
      "question-release-checks:skip",
    ]);
    expect(canonicalQuestionResolutionPairs).toHaveLength(5);
    expect(questionResolutionCases).toHaveLength(15);
    expect(
      new Set(
        questionResolutionCases.map(
          ({ module, question, resolution }) =>
            `${module.id}:${question.id}:${resolution}`,
        ),
      ),
    ).toHaveLength(15);
  });
});
