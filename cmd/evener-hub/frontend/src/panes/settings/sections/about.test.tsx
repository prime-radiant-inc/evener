import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { SettingsOverviewResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, expect, test } from "vitest";
import { connectionStore } from "../../../stores/connection";
import { resetSettingsOverviewStoreForTests } from "../../../stores/settingsOverview";
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
import { AboutSection, MIT_LICENSE_TEXT } from "./about";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetSettingsOverviewStoreForTests();
  resetDisclosureStoreForTests();
});

// Renders About with no client connected and waits for the overview fetch it
// starts on mount to settle (it rejects, and the identity line degrades), so
// the credits tests end with no state update still in flight.
async function renderDisconnectedAbout(): Promise<void> {
  render(<AboutSection />);
  await screen.findByText("Version unavailable:", { exact: false });
}

test("renders the design-credit line naming Beautiful UI, its author, and the MIT License", async () => {
  await renderDisconnectedAbout();
  expect(
    screen.getByText(
      "The visual design language is adapted from Beautiful UI (https://www.beautifului.dev) by Shane Levine, used under the MIT License.",
    ),
  ).toBeTruthy();
});

test("renders the typeface credit and the third-party-notices pointer", async () => {
  await renderDisconnectedAbout();
  expect(screen.getByText("Inter and JetBrains Mono, used under the SIL Open Font License.")).toBeTruthy();
  expect(screen.getByText("Full third-party notices live in the repository.")).toBeTruthy();
});

test("the MIT license text is collapsed behind a disclosure and includes the copyright line", async () => {
  await renderDisconnectedAbout();
  expect(screen.queryByText("Copyright (c) 2026 Shane Levine", { exact: false })).toBeNull();

  fireEvent.click(screen.getByText("MIT License"));

  expect(screen.getByText("Copyright (c) 2026 Shane Levine", { exact: false })).toBeTruthy();
});

test("the embedded license const matches LICENSES/beautiful-ui.txt on disk, from the MIT License heading onward", () => {
  const licensePath = join(
    dirname(fileURLToPath(import.meta.url)),
    "..",
    "..",
    "..",
    "..",
    "LICENSES",
    "beautiful-ui.txt",
  );
  const fileContent = readFileSync(licensePath, "utf8");
  const headingIndex = fileContent.indexOf("MIT License");
  expect(headingIndex).toBeGreaterThan(-1);
  expect(MIT_LICENSE_TEXT).toBe(fileContent.slice(headingIndex));
});

test("shows the hub version (and commit) once connected and loaded", async () => {
  const fake = connectFakeClient();
  const response: SettingsOverviewResponse = {
    hub: { version: "1.2.3", commit: "abc1234", daemonIdleTimeoutMillis: 3600000 },
  };
  fake.on("evener/settings/overview", () => response);

  render(<AboutSection />);

  expect(await screen.findByText("1.2.3", { exact: false })).toBeTruthy();
  expect(screen.getByText("(abc1234)")).toBeTruthy();
});

test("omits version/commit gracefully, with no raw error text, when not connected", async () => {
  await renderDisconnectedAbout();
  expect(screen.getByText("evener hub")).toBeTruthy();
  expect(screen.queryByText("no client connected", { exact: false })).toBeNull();
});

test("a fetch failure shows a friendly message, never the raw error", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => {
    throw new Error("hub unreachable");
  });

  render(<AboutSection />);

  expect(await screen.findByText("Version unavailable:", { exact: false })).toBeTruthy();
  expect(screen.queryByText("hub unreachable", { exact: false })).toBeNull();
});
