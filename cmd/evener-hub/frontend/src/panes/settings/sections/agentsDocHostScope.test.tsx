import type { AgentsDocResponse, HostRow } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import { resetAgentsDocHostInstancesForTests, resetAgentsDocStoreForTests } from "../../../stores/agentsDoc";
import { connectionStore } from "../../../stores/connection";
import { hostsStore } from "../../../stores/hosts";
import { resetSettingsHostForTests, setSettingsHost } from "../../../stores/settingsHost";
import { Toast } from "../../../widgets";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { AgentsDocHostScope } from "./agentsDoc";

// The AGENTS.md settings surface's host scope (component 07b): a remote
// selection shows AND changes that host's own file through evener/host/request,
// never this hub's. Local stays the plain-call section.

function hostRow(overrides: Partial<HostRow> & Pick<HostRow, "name">): HostRow {
  return { origin: "sidecar", attached: false, midAttach: false, removed: false, ...overrides };
}

const BETA_DOC: AgentsDocResponse = { path: "/home/b/.config/evener/AGENTS.md", exists: true, content: "# beta\n" };
const CONTROLLER_DOC: AgentsDocResponse = {
  path: "/home/c/.config/evener/AGENTS.md",
  exists: true,
  content: "# controller\n",
};

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

function editor(): HTMLTextAreaElement {
  return screen.getByRole("textbox", { name: "AGENTS.md contents" }) as HTMLTextAreaElement;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetAgentsDocStoreForTests();
  resetAgentsDocHostInstancesForTests();
  resetToastStoreForTests();
  hostsStore.getState().resetForTests();
  resetSettingsHostForTests();
  window.history.pushState({}, "", "/settings/agents-md");
  setSettingsHost("local");
});

afterEach(() => {
  cleanup();
  resetAgentsDocHostInstancesForTests();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  window.history.pushState({}, "", "/");
});

test("with this hub selected the section issues the plain agentsDoc calls and never the proxy", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/settings/agentsDoc/get", () => CONTROLLER_DOC);
  fake.on("evener/host/request", () => {
    throw new Error("the local hub must never route through the proxy");
  });

  render(
    <>
      <Toast />
      <AgentsDocHostScope sectionId="agents-md" />
    </>,
  );

  await waitFor(() => expect(editor().value).toBe("# controller\n"));
  expect(fake.calls.some((call) => call.method === "evener/host/request")).toBe(false);
});

test("a remote selection shows AND changes that host's own file, never this hub's", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/settings/agentsDoc/get", () => {
    throw new Error("a remote selection must not read this hub's AGENTS.md");
  });
  fake.on("evener/settings/agentsDoc/set", () => {
    throw new Error("a remote selection must not write this hub's AGENTS.md");
  });
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { host: string; method: string; params: { content?: string } };
    expect(forwarded.host).toBe("beta");
    if (forwarded.method === "evener/settings/agentsDoc/get") return BETA_DOC as never;
    if (forwarded.method === "evener/settings/agentsDoc/set")
      return { ...BETA_DOC, content: forwarded.params.content } as never;
    throw new Error(`unexpected forwarded method ${forwarded.method}`);
  });

  setSettingsHost("beta");
  render(
    <>
      <Toast />
      <AgentsDocHostScope sectionId="agents-md" />
    </>,
  );

  await waitFor(() => expect(editor().value).toBe("# beta\n"));

  const user = userEvent.setup();
  await user.clear(editor());
  await user.type(editor(), "# beta edited\n");
  await user.click(screen.getByRole("button", { name: "Save" }));

  await waitFor(() =>
    expect(
      fake.calls.some(
        (call) =>
          call.method === "evener/host/request" &&
          (call.params as { method: string }).method === "evener/settings/agentsDoc/set",
      ),
    ).toBe(true),
  );
  // Every call reached the proxy; none named the controller's own methods.
  const methods = fake.calls.map((call) => call.method);
  expect(methods.filter((method) => method.startsWith("evener/settings/agentsDoc/"))).toEqual([]);
});
