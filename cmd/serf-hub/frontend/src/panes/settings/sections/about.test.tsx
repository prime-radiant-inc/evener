import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test } from "vitest";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { SettingsOverviewResponse } from "../../../protocol/types.gen";
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

afterEach(cleanup);

test("renders the design-credit line naming Beautiful UI, its author, and the MIT License", () => {
  render(<AboutSection />);
  expect(
    screen.getByText(
      "The visual design language is adapted from Beautiful UI (https://www.beautifului.dev) by Shane Levine, used under the MIT License.",
    ),
  ).toBeTruthy();
});

test("renders the typeface credit and the third-party-notices pointer", () => {
  render(<AboutSection />);
  expect(screen.getByText("Inter and JetBrains Mono, used under the SIL Open Font License.")).toBeTruthy();
  expect(screen.getByText("Full third-party notices live in the repository.")).toBeTruthy();
});

test("the MIT license text is collapsed behind a disclosure and includes the copyright line", () => {
  render(<AboutSection />);
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
  const response: SettingsOverviewResponse = { hub: { version: "1.2.3", commit: "abc1234" } };
  fake.on("serf/settings/overview", () => response);

  render(<AboutSection />);

  expect(await screen.findByText("1.2.3", { exact: false })).toBeTruthy();
  expect(screen.getByText("(abc1234)")).toBeTruthy();
});

test("omits version/commit gracefully, with no raw error text, when not connected", () => {
  render(<AboutSection />);
  expect(screen.getByText("serf hub")).toBeTruthy();
});

test("a fetch failure shows a friendly message, never the raw error", async () => {
  const fake = connectFakeClient();
  fake.on("serf/settings/overview", () => {
    throw new Error("hub unreachable");
  });

  render(<AboutSection />);

  expect(await screen.findByText("Version unavailable:", { exact: false })).toBeTruthy();
  expect(screen.queryByText("hub unreachable", { exact: false })).toBeNull();
});
