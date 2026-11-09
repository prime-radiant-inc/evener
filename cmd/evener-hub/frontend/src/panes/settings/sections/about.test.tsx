import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { SettingsOverviewResponse } from "@evener/appwire-client";
import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, expect, test } from "vitest";
import { resetConnectionStoreForTests } from "../../../stores/connection";
import { resetSettingsOverviewStoreForTests, settingsOverviewStore } from "../../../stores/settingsOverview";
import { connectReadyFakeClient } from "../../../testing/connectionTestUtils";
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
import { AboutSection, MIT_LICENSE_TEXT } from "./about";

beforeEach(() => {
  resetConnectionStoreForTests();
  resetSettingsOverviewStoreForTests();
  resetDisclosureStoreForTests();
});

// Renders About with no client connected and waits for the overview fetch it
// starts on mount to settle (it rejects, and the identity line degrades), so
// the credits tests end with no state update still in flight.
async function renderDisconnectedAbout(): Promise<void> {
  render(<AboutSection />);
  await screen.findByTestId("about-version-unavailable");
}

// Attribution the borrowed design language requires: its presence is the
// contract, its wording is not.
test("renders the design-credit line", async () => {
  await renderDisconnectedAbout();
  expect(screen.getByTestId("design-credit").textContent).toMatch(/\S/);
});

test("the MIT license text is collapsed behind a disclosure until opened", async () => {
  await renderDisconnectedAbout();
  const license = screen.getByTestId("about-mit-license");
  expect(license.hasAttribute("open")).toBe(false);

  const summary = license.querySelector("summary");
  if (summary === null) throw new Error("license disclosure has no summary");
  fireEvent.click(summary);

  expect(license.hasAttribute("open")).toBe(true);
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
  const fake = connectReadyFakeClient();
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
  // The identity line stands alone: no commit note. The caption carries the
  // store's error, which the store already keeps in its friendly form
  // (hubOverview.test.ts pins that contract).
  expect(screen.getByTestId("about-identity").querySelector("span")).toBeNull();
  const storedError = settingsOverviewStore.getState().error;
  expect(storedError).not.toBeNull();
  expect(screen.getByTestId("about-version-unavailable").textContent).toContain(storedError);
});

test("a fetch failure shows a friendly message, never the raw error", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/settings/overview", () => {
    throw new Error("raw-overview-failure");
  });

  render(<AboutSection />);

  expect(await screen.findByTestId("about-version-unavailable")).toBeTruthy();
  expect(screen.queryByText("raw-overview-failure", { exact: false })).toBeNull();
});
