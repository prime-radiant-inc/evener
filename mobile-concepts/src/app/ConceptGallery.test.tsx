import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { canonicalFixture } from "../core/fixtures";
import { getPlatformPrimitives } from "../core/platform";
import {
  createPrototypeStore,
  type PreferenceStorage,
  PrototypeProvider,
} from "../core/store";
import { ConceptGallery } from "./ConceptGallery";

function renderGallery(dispatch = vi.fn()) {
  const storage: PreferenceStorage = {
    getItem: () => null,
    setItem: vi.fn(),
    removeItem: vi.fn(),
  };
  const store = createPrototypeStore({
    platform: "ios",
    storage,
    fixtureInput: canonicalFixture,
    diagnostics: { report: vi.fn() },
  });
  return {
    dispatch,
    store,
    ...render(
      <PrototypeProvider store={store}>
        <ConceptGallery
          dispatch={dispatch}
          primitives={getPlatformPrimitives("ios")}
        />
      </PrototypeProvider>,
    ),
  };
}

describe("ConceptGallery", () => {
  it("exposes three selectable semantic articles with headings and theses", () => {
    renderGallery();
    const articles = screen.getAllByRole("article");
    expect(articles).toHaveLength(3);
    for (const [index, name, thesis] of [
      [0, "Stillwater", "A calm, exact tool that disappears behind the work."],
      [1, "Constellation", "See the shape of the work, not just its log."],
      [
        2,
        "Field Notes",
        "Treat every session as a durable, readable record of work.",
      ],
    ] as const) {
      expect(
        within(articles[index] as HTMLElement).getByRole("heading", {
          name,
        }),
      ).toBeVisible();
      expect(
        within(articles[index] as HTMLElement).getByText(thesis),
      ).toBeVisible();
      expect(
        within(articles[index] as HTMLElement).getByRole("button", {
          name: `Select ${name}`,
        }),
      ).toBeVisible();
    }
    expect(document.querySelector("[class*='phone']")).toBeNull();
  });

  it("dispatches the exact concept and conveys selection", () => {
    const { dispatch, store, rerender } = renderGallery();
    fireEvent.click(
      screen.getByRole("button", { name: "Select Constellation" }),
    );
    expect(dispatch).toHaveBeenCalledWith({
      type: "selectConcept",
      concept: "constellation",
    });

    store.getState().dispatch({
      type: "selectConcept",
      concept: "constellation",
    });
    rerender(
      <PrototypeProvider store={store}>
        <ConceptGallery
          dispatch={dispatch}
          primitives={getPlatformPrimitives("ios")}
        />
      </PrototypeProvider>,
    );
    expect(
      screen.getByRole("button", { name: "Select Constellation" }),
    ).toHaveAttribute("aria-pressed", "true");
  });

  it("consumes the resolved platform target and semantic families", () => {
    renderGallery();
    const gallery = screen.getByTestId("concept-gallery");
    expect(gallery).toHaveAttribute("data-minimum-target", "44");
    expect(gallery).toHaveAttribute("data-navigation", "ios-tab-bar");
    expect(gallery).toHaveAttribute("data-title", "large-or-inline");
    expect(gallery).toHaveAttribute("data-feedback", "ios-highlight");
  });
});
