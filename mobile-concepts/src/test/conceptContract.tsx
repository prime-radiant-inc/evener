import {
  act,
  cleanup,
  fireEvent,
  screen,
  within,
} from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import type { ConceptModule } from "../concepts/contract";
import type { Platform, Route } from "../core/model";
import { getPlatformPrimitives } from "../core/platform";
import {
  selectCurrentVoiceLevel,
  selectCurrentVoiceStep,
  selectGroupedSessions,
  selectSearchProjection,
} from "../core/selectors";
import { type RenderConceptResult, renderConcept } from "./renderConcept";

const sessionId = "session-native-client";

function renderRoute(
  module: ConceptModule,
  route: Route,
  platform: Platform = "ios",
  scenario: Parameters<typeof renderConcept>[1]["scenario"] = "baseline",
): RenderConceptResult {
  return renderConcept(module, { platform, route, scenario });
}

function expectRoute(route: string): HTMLElement {
  const mains = screen.getAllByRole("main");
  expect(mains).toHaveLength(1);
  const main = mains[0];
  if (!main) throw new Error("Concept must render one main element");
  expect(main).toHaveAttribute("data-route", route);
  return main;
}

function getAction(name: string): HTMLElement {
  const action = document.querySelector(`[data-action="${name}"]`);
  expect(action).toBeInstanceOf(HTMLButtonElement);
  return action as HTMLElement;
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
      "exposes %s platform chrome and global navigation actions",
      (platform) => {
        const result = renderRoute(
          module,
          { kind: "root", tab: "sessions" },
          platform,
        );
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
        expectRoute("sessions");

        const navigation = screen.getByRole("navigation", { name: "Primary" });
        for (const name of ["Sessions", "Search", "New Session", "Settings"]) {
          expect(
            within(navigation).getByRole("button", { name }),
          ).toBeVisible();
        }
        fireEvent.click(screen.getByRole("button", { name: "Switch concept" }));
        expect(result.store.getState().overlay).toBe("concept-switcher");
        act(() => result.store.getState().dispatch({ type: "goBack" }));
        fireEvent.click(screen.getByRole("button", { name: "Lab Controls" }));
        expect(result.store.getState().overlay).toBe("lab-controls");
      },
    );

    it("uses canonical session groups, filtering, and refresh transitions", () => {
      const result = renderRoute(module, { kind: "root", tab: "sessions" });
      const initial = result.store.getState();
      for (const group of selectGroupedSessions(initial)) {
        const region = document.querySelector(
          `[data-session-group-id="${group.id}"]`,
        );
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
      expect(document.querySelectorAll("[data-session-id]")).toHaveLength(
        selectGroupedSessions(result.store.getState()).flatMap(
          ({ sessions }) => sessions,
        ).length,
      );

      fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
      expect(result.store.getState().refreshState).toBe("refreshing");
      expect(
        document.querySelector('[data-refresh-state="refreshing"]'),
      ).toBeVisible();
      fireEvent.click(getAction("complete-refresh"));
      expect(result.store.getState().refreshState).toBe("complete");
      expect(
        document.querySelector('[data-refresh-state="complete"]'),
      ).toBeVisible();
    });

    it("renders selector-owned search guidance, results, no-results, and focus", () => {
      const result = renderRoute(module, { kind: "root", tab: "search" });
      expectRoute("search");
      expect(selectSearchProjection(result.store.getState()).kind).toBe(
        "prompt",
      );
      expect(
        document.querySelector('[data-search-state="prompt"]'),
      ).toBeVisible();

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
        const link = document.querySelector(
          `[data-search-result-id="${item.id}"]`,
        );
        expect(link).toHaveAttribute("data-search-kind", item.kind);
        expect(link).toHaveAttribute("data-search-context", item.context);
        expect(link).toHaveAttribute("href");
      }
      fireEvent.click(
        document.querySelector(
          `[data-search-result-id="${documentFixture.id}"]`,
        ) as HTMLElement,
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
      const noResults = renderRoute(module, { kind: "root", tab: "search" });
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
        document.querySelector('[data-search-state="no-results"]'),
      ).toBeVisible();
    });

    it("renders conversation transcript, disclosure, composer, Work, and Voice", () => {
      const result = renderRoute(module, { kind: "conversation", sessionId });
      expectRoute("conversation");
      const state = result.store.getState();
      const items = state.projection.fixture.transcript.filter(
        (item) => item.sessionId === sessionId,
      );
      for (const item of items) {
        expect(
          document.querySelector(`[data-transcript-item-id="${item.id}"]`),
        ).toBeVisible();
      }
      const tool = items.find((item) => item.kind === "tool");
      expect(tool).toBeDefined();
      if (tool?.kind !== "tool") return;
      const disclosure = screen.getByRole("button", { name: tool.label });
      expect(disclosure).toHaveAttribute("aria-expanded", "false");
      fireEvent.click(disclosure);
      expect(result.store.getState().expandedToolIds).toContain(tool.id);
      expect(disclosure).toHaveAttribute("aria-expanded", "true");

      const composer = screen.getByRole("textbox", { name: "Message" });
      fireEvent.change(composer, { target: { value: "Continue" } });
      expect(result.store.getState().draft).toBe("Continue");
      for (const mode of ["Send", "Steer", "Queue"]) {
        expect(screen.getByRole("button", { name: mode })).toBeVisible();
      }
      fireEvent.click(screen.getByRole("button", { name: "Work" }));
      expect(result.store.getState().route.kind).toBe("work");
      act(() => result.store.getState().dispatch({ type: "goBack" }));
      fireEvent.click(screen.getByRole("button", { name: "Voice" }));
      expect(result.store.getState().route.kind).toBe("voice");
    });

    it("renders typed Work hierarchy and every usage field", () => {
      const result = renderRoute(module, { kind: "work", sessionId });
      expectRoute("work");
      const { fixture } = result.store.getState().projection;
      for (const kind of ["task", "subagent", "job"] as const) {
        const nodes = fixture.work.filter(
          (node) => node.sessionId === sessionId && node.kind === kind,
        );
        expect(nodes.length).toBeGreaterThan(0);
        for (const node of nodes) {
          const element = document.querySelector(
            `[data-work-node-id="${node.id}"][data-work-kind="${kind}"]`,
          );
          expect(element).toBeVisible();
          fireEvent.click(within(element as HTMLElement).getByRole("button"));
          expect(result.store.getState().expandedWorkIds).toContain(node.id);
        }
      }
      const usage = document.querySelector("[data-work-usage]");
      expect(usage).toHaveTextContent(String(fixture.usage.tokens));
      expect(usage).toHaveTextContent(fixture.usage.costLabel);
      expect(usage).toHaveTextContent(fixture.usage.durationLabel);
      expect(usage).toHaveTextContent(String(fixture.usage.contextPercent));
    });

    it("uses canonical question ownership for options, notes, and resolutions", () => {
      const result = renderRoute(
        module,
        { kind: "conversation", sessionId },
        "ios",
        "question",
      );
      const questions = result.store.getState().projection.fixture.questions;
      for (const question of questions) {
        const form = document.querySelector(
          `form[data-question-id="${question.id}"]`,
        ) as HTMLFormElement | null;
        expect(form).toBeVisible();
        if (!form) continue;
        const submit = within(form).getByRole("button", {
          name: "Submit answer",
        });
        expect(submit).toBeDisabled();
        const option = question.options[0];
        expect(option).toBeDefined();
        if (!option) continue;
        fireEvent.click(within(form).getByLabelText(option.label));
        if (question.allowNote) {
          fireEvent.change(
            within(form).getByRole("textbox", { name: "Note" }),
            {
              target: { value: "Fixture note" },
            },
          );
        }
        expect(submit).toBeEnabled();
        fireEvent.click(submit);
        expect(result.store.getState().answers[question.id]).toMatchObject({
          resolution: "answer",
          submitted: true,
        });
      }

      for (const resolution of ["fallback", "decide", "skip"] as const) {
        cleanup();
        const resolutionResult = renderRoute(
          module,
          { kind: "conversation", sessionId },
          "ios",
          "question",
        );
        const question = resolutionResult.store
          .getState()
          .projection.fixture.questions.find((candidate) =>
            resolution === "fallback"
              ? candidate.allowFallback
              : resolution === "decide"
                ? candidate.allowDecide
                : candidate.allowSkip,
          );
        expect(question).toBeDefined();
        if (!question) continue;
        fireEvent.click(
          document.querySelector(
            `[data-question-id="${question.id}"] [data-question-resolution="${resolution}"]`,
          ) as HTMLElement,
        );
        expect(
          resolutionResult.store.getState().answers[question.id],
        ).toMatchObject({
          resolution,
          submitted: true,
        });
      }
    });

    it("drives recent and typed project selection through all new-session outcomes", () => {
      const result = renderRoute(module, { kind: "root", tab: "new" });
      expectRoute("new");
      const { fixture } = result.store.getState().projection;
      const recent = fixture.recentProjects[0];
      expect(recent).toBeDefined();
      if (!recent) return;
      fireEvent.click(
        document.querySelector(
          `[data-project-id="${recent.id}"]`,
        ) as HTMLElement,
      );
      expect(result.store.getState().newSession.project).toBe(recent.path);
      fillValidNewSession(result);
      const submit = screen.getByRole("button", { name: "Start session" });
      expect(submit).toBeEnabled();
      fireEvent.click(submit);
      expect(result.store.getState().newSession.outcome).toBe("starting");
      expect(
        document.querySelector('[data-new-session-state="starting"]'),
      ).toBeVisible();
      fireEvent.click(getAction("complete-new-session-success"));
      expect(result.store.getState().newSession.outcome).toBe("success");
      expect(result.store.getState().route.kind).toBe("conversation");

      cleanup();
      const failure = renderRoute(module, { kind: "root", tab: "new" });
      fillValidNewSession(failure);
      fireEvent.click(screen.getByRole("button", { name: "Start session" }));
      fireEvent.click(getAction("complete-new-session-failure"));
      expect(failure.store.getState().newSession.outcome).toBe("failure");
      expect(
        document.querySelector('[data-new-session-state="failure"]'),
      ).toBeVisible();
    });

    it("renders settings preferences, fictional Hubs, and offline explanation", () => {
      const result = renderRoute(module, { kind: "root", tab: "settings" });
      expectRoute("settings");
      fireEvent.change(screen.getByRole("combobox", { name: "Appearance" }), {
        target: { value: "dark" },
      });
      expect(result.store.getState().appearance).toBe("dark");
      fireEvent.click(
        screen.getByRole("checkbox", { name: "Speak responses" }),
      );
      expect(result.store.getState().voicePreferences.speakResponses).toBe(
        false,
      );
      fireEvent.change(screen.getByRole("combobox", { name: "Speech rate" }), {
        target: { value: "fast" },
      });
      expect(result.store.getState().voicePreferences.rate).toBe("fast");
      for (const hub of result.store.getState().projection.fixture.hubs) {
        const row = document.querySelector(`[data-hub-id="${hub.id}"]`);
        expect(row).toHaveAttribute("data-fictional", "true");
        expect(row).toHaveTextContent(hub.context);
      }
      expect(
        document.querySelector('[data-offline-prototype="true"]'),
      ).toBeVisible();
      expect(
        screen.getByRole("button", { name: "Switch concept" }),
      ).toBeVisible();
      expect(
        screen.getByRole("button", { name: "Lab Controls" }),
      ).toBeVisible();
    });

    it("renders deterministic Voice state and keeps Stop distinct from End and Close", () => {
      const result = renderRoute(module, { kind: "voice", sessionId });
      expectRoute("voice");
      const step = selectCurrentVoiceStep(result.store.getState());
      expect(step).not.toBeNull();
      if (!step) return;
      expect(
        document.querySelector(`[data-voice-state="${step.state}"]`),
      ).toBeVisible();
      expect(screen.getByText(step.caption)).toBeVisible();
      const meter = screen.getByRole("progressbar", { name: "Voice level" });
      expect(meter).toHaveAttribute(
        "aria-valuenow",
        String(selectCurrentVoiceLevel(result.store.getState())),
      );
      fireEvent.click(screen.getByRole("button", { name: "Mute" }));
      expect(result.store.getState().voice.muted).toBe(true);
      fireEvent.click(screen.getByRole("button", { name: "Stop" }));
      expect(result.store.getState().voice.stopped).toBe(true);
      expect(result.store.getState().voice.ended).toBe(false);
      expect(result.store.getState().route.kind).toBe("voice");
      fireEvent.click(screen.getByRole("button", { name: "End" }));
      expect(result.store.getState().voice.ended).toBe(true);
      expect(result.store.getState().route.kind).not.toBe("voice");

      cleanup();
      const close = renderRoute(module, { kind: "voice", sessionId });
      fireEvent.click(screen.getByRole("button", { name: "Close" }));
      expect(close.store.getState().route.kind).not.toBe("voice");
    });
  });
}
