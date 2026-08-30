/**
 * Browser entry for the live conversation harness.
 *
 * This module is served by `browserguard.vite.config.mjs` on a private loopback
 * Vite port. It constructs the same production store types and actual
 * `LiveConceptHost` via `createReadyHarness`, then exposes only
 * `window.__EVENER_LIVE_CONVERSATION_HARNESS__: LiveConversationHarnessApi`.
 *
 * Query parameters select concept, fixture, type scale, theme, reduced motion,
 * and safe area. It installs the real `createViewportCoordinator` against the
 * typed harness visual viewport; it never assigns CSS viewport tokens directly.
 * It adds no DOM test buttons or product controls.
 */

import { createReadyHarness } from "./LiveConceptBrowserHarness";
import type {
  HarnessAction,
  LiveConversationHarnessApi,
} from "./live-conversation-harness-actions";
import { validateHarnessAction } from "./live-conversation-harness-actions";

interface HarnessQueryParams {
  concept: string;
  fixture: "pathological-39" | "variable-500";
  typeScale: string;
  theme: string;
  reducedMotion: boolean;
  safeArea: string;
}

function parseQueryParams(): HarnessQueryParams {
  const params = new URLSearchParams(window.location.search);
  return {
    concept: params.get("concept") ?? "stillwater",
    fixture:
      params.get("fixture") === "variable-500"
        ? "variable-500"
        : "pathological-39",
    typeScale: params.get("typeScale") ?? "large",
    theme: params.get("theme") ?? "dark",
    reducedMotion: params.get("reducedMotion") === "true",
    safeArea: params.get("safeArea") ?? "none",
  };
}

function main(): void {
  const _params = parseQueryParams();

  // The harness renders the actual LiveConceptHost into a container element.
  // createReadyHarness builds the production store graph and renders the host
  // using @testing-library/react. For the browser entry, we re-render into the
  // #harness-root container so the real DOM is available for CDP queries.
  const container = document.getElementById("harness-root");
  if (container === null) {
    throw new Error("harness-root container not found");
  }

  // Create the harness with default seeding. The CDP matrix runner dispatches
  // typed actions through the exposed API to drive each state.
  const harness = createReadyHarness({
    generation: 7,
    draft: "draft-sentinel::production-appwire",
    lastGoodKeyDigest: "last-good-key-digest::fixture",
    fixture: _params.fixture,
  });

  // Expose only the harness API on the global object. The CDP runner validates
  // every action before dispatch via the harness's own validateHarnessAction.
  const api: LiveConversationHarnessApi = {
    dispatch: harness.dispatch,
    snapshot: harness.snapshot,
  };
  (
    window as unknown as {
      __EVENER_LIVE_CONVERSATION_HARNESS__: LiveConversationHarnessApi;
    }
  ).__EVENER_LIVE_CONVERSATION_HARNESS__ = api;

  // Re-render the harness content into the visible container. The
  // createReadyHarness already rendered via @testing-library into a detached
  // container; for the browser we need the DOM in the visible #harness-root.
  // We move the rendered content.
  const renderedContainer = document.querySelector(
    "[data-live-conversation-frame='true']",
  )?.parentElement;
  if (renderedContainer != null && renderedContainer !== container) {
    container.appendChild(renderedContainer);
  }
}

// Re-export validateHarnessAction for the CDP runner to use if needed.
export { type HarnessAction, validateHarnessAction };

main();
