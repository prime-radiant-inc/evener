// TDD tests for the ConceptSwitcher modal component.
//
// Asserts one dialog, three concept choices, opener focus restoration,
// Escape/Back close without selection, selection dispatch then close,
// background inert, and selected concept announced without color-only
// meaning.

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  CONCEPT_CHOICES,
  ConceptSwitcher,
  type ConceptSwitcherProps,
} from "./ConceptSwitcher";

afterEach(() => {
  cleanup();
});

function renderSwitcher(
  overrides: Partial<ConceptSwitcherProps> = {},
): ReturnType<typeof render> & { opener: HTMLButtonElement } {
  const opener = document.createElement("button");
  opener.textContent = "Open switcher";
  document.body.appendChild(opener);
  opener.focus();
  const openerRef = { current: opener } as React.RefObject<HTMLElement | null>;

  const props: ConceptSwitcherProps = {
    open: true,
    selectedConcept: "stillwater",
    onSelect: vi.fn(),
    onClose: vi.fn(),
    openerRef,
    ...overrides,
  };

  const result = render(<ConceptSwitcher {...props} />);
  return { ...result, opener };
}

describe("ConceptSwitcher — dialog structure", () => {
  it("renders exactly one dialog when open", () => {
    renderSwitcher();
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
  });

  it("renders three concept choices", () => {
    renderSwitcher();
    const buttons = screen.getAllByRole("button", {
      name: /(Stillwater|Constellation|Field Notes)/,
    });
    expect(buttons).toHaveLength(3);
  });

  it("gives every choice an exact user-meaningful AX action label", () => {
    renderSwitcher();
    for (const name of ["Stillwater", "Constellation", "Field Notes"]) {
      expect(
        screen.getByRole("button", { name: `Switch to ${name}` }),
      ).toBeVisible();
    }
  });

  it("matches the expected concept IDs", () => {
    expect(CONCEPT_CHOICES.map((c) => c.id)).toEqual([
      "stillwater",
      "constellation",
      "field-notes",
    ]);
  });
});

describe("ConceptSwitcher — close without selection", () => {
  it("Escape closes without dispatching a selection", () => {
    const onSelect = vi.fn();
    const onClose = vi.fn();
    renderSwitcher({ onSelect, onClose });
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("Back button closes without dispatching a selection", () => {
    const onSelect = vi.fn();
    const onClose = vi.fn();
    renderSwitcher({ onSelect, onClose });
    fireEvent.click(screen.getByRole("button", { name: "Back" }));
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("overlay click closes without dispatching a selection", () => {
    const onSelect = vi.fn();
    const onClose = vi.fn();
    renderSwitcher({ onSelect, onClose });
    const overlay = document.body.querySelector(
      ".live-concept-switcher__overlay",
    );
    expect(overlay).not.toBeNull();
    fireEvent.click(overlay as Element);
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(onSelect).not.toHaveBeenCalled();
  });
});

describe("ConceptSwitcher — selection dispatch and close", () => {
  it("selecting a concept dispatches onSelect then onClose", () => {
    const onSelect = vi.fn();
    const onClose = vi.fn();
    renderSwitcher({ onSelect, onClose });
    fireEvent.click(screen.getByRole("button", { name: /Constellation/ }));
    expect(onSelect).toHaveBeenCalledWith("constellation");
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("dispatches the correct concept for each choice", () => {
    for (const choice of CONCEPT_CHOICES) {
      const onSelect = vi.fn();
      const onClose = vi.fn();
      renderSwitcher({ onSelect, onClose, selectedConcept: "stillwater" });
      fireEvent.click(
        screen.getByRole("button", { name: new RegExp(choice.label) }),
      );
      expect(onSelect).toHaveBeenCalledWith(choice.id);
      expect(onClose).toHaveBeenCalledTimes(1);
      cleanup();
    }
  });
});

describe("ConceptSwitcher — focus restoration", () => {
  it("restores focus to the opener element when closed", () => {
    const opener = document.createElement("button");
    opener.textContent = "Open switcher";
    document.body.appendChild(opener);
    opener.focus();
    const openerRef = {
      current: opener,
    } as React.RefObject<HTMLElement | null>;

    const { rerender } = render(
      <ConceptSwitcher
        open
        selectedConcept="stillwater"
        onSelect={vi.fn()}
        onClose={vi.fn()}
        openerRef={openerRef}
      />,
    );

    // Close the dialog
    rerender(
      <ConceptSwitcher
        open={false}
        selectedConcept="stillwater"
        onSelect={vi.fn()}
        onClose={vi.fn()}
        openerRef={openerRef}
      />,
    );

    expect(document.activeElement).toBe(opener);
    document.body.removeChild(opener);
  });
});

describe("ConceptSwitcher — semantic selected announcement", () => {
  it("announces the selected concept via text, not color alone", () => {
    renderSwitcher({ selectedConcept: "stillwater" });
    const stillwaterButton = screen
      .getByRole("button", { name: /Stillwater/ })
      .closest("[data-concept-choice]");
    expect(stillwaterButton).not.toBeNull();
    expect(stillwaterButton).toHaveAttribute("data-concept-selected", "true");
    expect(stillwaterButton).toHaveAttribute("aria-pressed", "true");
    // Visible "Selected" text label
    expect(
      (stillwaterButton as HTMLElement).querySelector(
        ".live-concept-switcher__choice-selected",
      ),
    ).not.toBeNull();
    expect(
      (stillwaterButton as HTMLElement).querySelector(
        ".live-concept-switcher__choice-selected",
      )?.textContent,
    ).toBe("Selected");
  });

  it("does not announce selected for non-selected concepts", () => {
    renderSwitcher({ selectedConcept: "stillwater" });
    const constellationButton = screen
      .getByRole("button", { name: /Constellation/ })
      .closest("[data-concept-choice]");
    expect(constellationButton).not.toBeNull();
    expect(constellationButton).toHaveAttribute(
      "data-concept-selected",
      "false",
    );
    expect(constellationButton).toHaveAttribute("aria-pressed", "false");
    expect(
      (constellationButton as HTMLElement).querySelector(
        ".live-concept-switcher__choice-selected",
      ),
    ).toBeNull();
  });
});

describe("ConceptSwitcher — background inertness", () => {
  it("marks the dialog as aria-modal to inert the background", () => {
    renderSwitcher();
    const dialog = screen.getByRole("dialog");
    expect(dialog).toHaveAttribute("aria-modal", "true");
  });

  it("does not render when closed", () => {
    render(
      <ConceptSwitcher
        open={false}
        selectedConcept="stillwater"
        onSelect={vi.fn()}
        onClose={vi.fn()}
      />,
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});
