import type { SearchResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { type RenderResult, render, screen } from "@testing-library/react";
import type { ReactElement } from "react";
import { connectionStore } from "../../stores/connection";
import { ClientProvider } from "../clientContext";

export function renderPalette(children: ReactElement): RenderResult {
  const client = connectionStore.getState().client ?? new FakeClient("idle");
  return render(<ClientProvider client={client}>{children}</ClientProvider>);
}

export function scriptSearch(response: SearchResponse): void {
  const client = new FakeClient();
  client.on("evener/search", () => response);
  connectionStore.getState().connect(client);
}

export function scriptSearchFailure(): void {
  const client = new FakeClient();
  client.on("evener/search", () => {
    throw new Error("search failed");
  });
  connectionStore.getState().connect(client);
}

// The palette's results carry structured markers so tests can read what the
// palette shows without reading its copy: section headers carry data-section,
// rows carry data-kind, and the keyboard-shortcut legend is a ShortcutList
// whose chords are its terms. A failed search is an alert and loading
// options a status, so those placeholders are found by role.

export function paletteSection(section: string): Element | null {
  return document.querySelector(`[data-section="${section}"]`);
}

export function paletteRowsOfKind(kind: string): HTMLElement[] {
  return screen.queryAllByRole("option").filter((option) => option.getAttribute("data-kind") === kind);
}

// Grouped search rows: a matching subagent nested under its group's anchor,
// and an anchor greyed because only its subagents matched.
export function paletteNestedRows(): HTMLElement[] {
  return screen.queryAllByRole("option").filter((option) => option.hasAttribute("data-nested"));
}

export function paletteIndirectRows(): HTMLElement[] {
  return screen.queryAllByRole("option").filter((option) => option.hasAttribute("data-indirect"));
}

// Each legend row's chord as KeyHint renders it, e.g. "Ctrl+K".
export function helpChords(): string[] {
  return screen
    .queryAllByRole("term")
    .map((term) => [...term.querySelectorAll("kbd")].map((kbd) => kbd.textContent).join("+"));
}
