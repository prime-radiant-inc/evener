import { type RenderResult, render } from "@testing-library/react";
import { type ReactNode, useEffect } from "react";
import type { StoreApi } from "zustand/vanilla";
import type { ConceptModule } from "../concepts/contract";
import { canonicalFixture } from "../core/fixtures";
import {
  createNavigationController,
  type NavigationController,
} from "../core/history";
import type { ConceptId, Platform, Route, ScenarioId } from "../core/model";
import { defaultPreferences, preferenceStorageKey } from "../core/persistence";
import { getPlatformPrimitives } from "../core/platform";
import {
  createPrototypeStore,
  PrototypeProvider,
  type PrototypeStore,
  usePrototypeState,
} from "../core/store";

export interface RenderConceptOptions {
  platform: Platform;
  scenario: ScenarioId;
  route: Route;
  concept?: ConceptId;
}

export interface RenderConceptResult extends RenderResult {
  store: StoreApi<PrototypeStore>;
}

function memoryStorage(initialValue: string | null) {
  const values = new Map<string, string>();
  if (initialValue !== null) values.set(preferenceStorageKey, initialValue);
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => values.set(key, value),
    removeItem: (key: string) => values.delete(key),
  };
}

function navigateTo(controller: NavigationController, route: Route): void {
  switch (route.kind) {
    case "gallery":
      return;
    case "root":
      controller.dispatch({ type: "navigateRoot", tab: route.tab });
      return;
    case "conversation":
      controller.dispatch({
        type: "openSession",
        sessionId: route.sessionId,
        focusItemId: route.focusItemId,
      });
      return;
    case "work":
      controller.dispatch({ type: "openWork", sessionId: route.sessionId });
      return;
    case "voice":
      controller.dispatch({ type: "openVoice", sessionId: route.sessionId });
  }
}

interface ConnectedRendererProps {
  module: ConceptModule;
  controller: NavigationController;
}

function ConnectedRenderer({ module, controller }: ConnectedRendererProps) {
  const state = usePrototypeState((current) => current);
  const primitives = getPlatformPrimitives(state.platform);
  const { Renderer } = module;
  return (
    <Renderer
      dispatch={controller.dispatch}
      onOpenConceptSwitcher={() =>
        controller.dispatch({
          type: "openOverlay",
          overlay: "concept-switcher",
        })
      }
      onOpenLabControls={() =>
        controller.dispatch({ type: "openOverlay", overlay: "lab-controls" })
      }
      platform={state.platform}
      primitives={primitives}
      state={state}
    />
  );
}

function HarnessLifecycle({
  children,
  controller,
}: {
  children: ReactNode;
  controller: NavigationController;
}) {
  useEffect(() => () => controller.dispose(), [controller]);
  return children;
}

export function renderConcept(
  module: ConceptModule,
  options: RenderConceptOptions,
): RenderConceptResult {
  const concept =
    options.route.kind === "gallery" ? null : (options.concept ?? module.id);
  const preferences = {
    ...defaultPreferences,
    concept,
    scenario: options.scenario,
  };
  const store = createPrototypeStore({
    platform: options.platform,
    storage: memoryStorage(JSON.stringify(preferences)),
    fixtureInput: canonicalFixture,
    diagnostics: {
      report(diagnostic) {
        throw new Error(
          `Contract fixture failed to decode: ${diagnostic.code} at ${diagnostic.path}`,
        );
      },
    },
  });
  const controller = createNavigationController(store, window);
  navigateTo(controller, options.route);

  const result = render(
    <PrototypeProvider store={store}>
      <HarnessLifecycle controller={controller}>
        <ConnectedRenderer module={module} controller={controller} />
      </HarnessLifecycle>
    </PrototypeProvider>,
  );
  return { ...result, store };
}
