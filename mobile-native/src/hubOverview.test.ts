import type { SettingsOverviewResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { expect, it } from "vitest";
import { createNativeHubOverview } from "./hubOverview";

// The store's behavior is pinned by the package's own test; this pins what the
// native adapter adds - the copy the screen renders in place of the error.
it("retains the known overview on a failed refresh and shows the native copy instead of the error detail", async () => {
  const fake = new FakeClient("ready");
  const model = createNativeHubOverview(fake);
  fake.on("evener/settings/overview", (): SettingsOverviewResponse => ({ hub: { version: "fixture" } }));
  await model.getState().refresh();
  expect(model.getState().data?.storage).toBeUndefined();
  fake.on("evener/settings/overview", () => {
    throw Error("private internal detail");
  });
  await model.getState().refresh();
  expect(model.getState().data?.hub?.version).toBe("fixture");
  expect(model.getState().error).toBe("Could not refresh hub information. Try again when connected.");
  fake.on("evener/settings/overview", () => ({ agents: [] }));
  await model.getState().refresh();
  expect(model.getState().data).toEqual({ agents: [] });
  expect(model.getState().error).toBeNull();
  expect(fake.calls.map((call) => call.method)).toEqual(Array(3).fill("evener/settings/overview"));
});
