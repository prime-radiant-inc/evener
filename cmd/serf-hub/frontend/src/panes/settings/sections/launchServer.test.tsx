import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { LaunchConfigLayer, LaunchConfigResolved, LaunchOptionSchemaResponse } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { resetLaunchConfigStoreForTests } from "../../../stores/launchConfig";
import { LaunchServerSection } from "./launchServer";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

const SCHEMA: LaunchOptionSchemaResponse = {
  options: [
    {
      field: "agent",
      wireField: "agent",
      label: "Agent",
      group: "Agent",
      kind: "text",
      perLaunch: true,
      defaultableLayers: ["global", "project"],
    },
  ],
};

const LAYER: LaunchConfigLayer = { agent: "serf" };
const RESOLVED: LaunchConfigResolved = { effective: LAYER, layers: { global: LAYER }, provenance: { agent: "global" } };

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetLaunchConfigStoreForTests();
});

afterEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  cleanup();
});

describe("load sequence", () => {
  test("shows a loading placeholder, then fetches schema before getLayer (sequential, not parallel)", async () => {
    const fake = connectFakeClient();
    const order: string[] = [];
    fake.on("serf/launch/schema", () => {
      order.push("schema");
      return SCHEMA;
    });
    fake.on("serf/launch/getLayer", (params) => {
      order.push("getLayer");
      expect(params).toEqual({ cwd: "/", layer: "global" });
      return LAYER;
    });
    fake.on("serf/launch/resolve", () => RESOLVED);
    render(<LaunchServerSection sectionId="launch-serf" />);
    expect(screen.getByText(/Loading launch settings/i)).toBeTruthy();
    await screen.findByLabelText("Agent");
    expect(order).toEqual(["schema", "getLayer"]);
  });

  test("on load failure, shows a failure message forever (2-state contract - no retry)", async () => {
    const fake = connectFakeClient();
    fake.on("serf/launch/schema", () => {
      throw new Error("network down");
    });
    render(<LaunchServerSection sectionId="launch-serf" />);
    await screen.findByText(/Failed to load launch settings/i);
    expect(screen.getByText(/network down/)).toBeTruthy();
  });

  test("a best-effort resolve('/') populates the diagnostics panel after load; its own failure is non-fatal", async () => {
    const fake = connectFakeClient();
    fake.on("serf/launch/schema", () => SCHEMA);
    fake.on("serf/launch/getLayer", () => LAYER);
    fake.on("serf/launch/resolve", () => {
      throw new Error("resolve failed");
    });
    render(<LaunchServerSection sectionId="launch-serf" />);
    await screen.findByLabelText("Agent");
    // The form is fully usable even though resolve() failed - "non-fatal".
    expect(screen.queryByText(/Warnings/)).toBeNull();
  });

  test("renders warnings from the initial resolve()'s diagnostics", async () => {
    const fake = connectFakeClient();
    fake.on("serf/launch/schema", () => SCHEMA);
    fake.on("serf/launch/getLayer", () => LAYER);
    fake.on("serf/launch/resolve", () => ({
      ...RESOLVED,
      diagnostics: [{ layer: "global", field: "sandbox", message: "sandbox_net has no effect without a sandbox mode" }],
    }));
    render(<LaunchServerSection sectionId="launch-serf" />);
    await screen.findByText("Warnings");
    expect(screen.getByText("sandbox: sandbox_net has no effect without a sandbox mode")).toBeTruthy();
  });
});

describe("save", () => {
  test("saving calls serf/launch/setLayer(/, global, collected) and refreshes diagnostics from ITS OWN returned resolved config", async () => {
    const fake = connectFakeClient();
    fake.on("serf/launch/schema", () => SCHEMA);
    fake.on("serf/launch/getLayer", () => LAYER);
    fake.on("serf/launch/resolve", () => RESOLVED); // no diagnostics initially
    fake.on("serf/launch/setLayer", (params) => {
      expect(params.cwd).toBe("/");
      expect(params.layer).toBe("global");
      return { ...RESOLVED, diagnostics: [{ layer: "global", field: "", message: "post-save warning" }] };
    });
    render(<LaunchServerSection sectionId="launch-serf" />);
    await screen.findByLabelText("Agent");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Save launch defaults" }));
    await waitFor(() => expect(screen.getByText("post-save warning")).toBeTruthy());
  });
});
