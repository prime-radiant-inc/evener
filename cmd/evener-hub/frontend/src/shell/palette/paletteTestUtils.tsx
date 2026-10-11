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
// rows carry data-kind, the no-rows placeholder carries data-reason, and
// each keyboard-shortcut legend row carries data-help-row.

export function paletteSection(section: string): Element | null {
  return document.querySelector(`[data-section="${section}"]`);
}

export function paletteRowsOfKind(kind: string): HTMLElement[] {
  return screen.queryAllByRole("option").filter((option) => option.getAttribute("data-kind") === kind);
}

export function paletteEmptyReason(): string | null {
  return document.querySelector("[data-reason]")?.getAttribute("data-reason") ?? null;
}

// Each legend row's chord as KeyHint renders it, e.g. "Ctrl+K".
export function helpChords(): string[] {
  return [...document.querySelectorAll("[data-help-row]")].map((row) =>
    [...row.querySelectorAll("kbd")].map((kbd) => kbd.textContent).join("+"),
  );
}
