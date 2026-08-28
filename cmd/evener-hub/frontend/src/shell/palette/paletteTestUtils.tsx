import { type RenderResult, render } from "@testing-library/react";
import type { ReactElement } from "react";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type { SearchResponse } from "../../protocol/types.gen";
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
