import { deferred } from "@evener/appwire-client/testing/deferred";
import "../session/testing/editorGeometry";
import type {
  HostRequestParams,
  InstanceListResponse,
  LaunchConfigResolved,
  LaunchOption,
  ModelListResponse,
  ThreadStartParams,
} from "@evener/appwire-client";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { ClientProvider } from "../../shell/clientContext";
import { connectionStore } from "../../stores/connection";
import { credentialsStore } from "../../stores/credentials";
import { readModuleCss } from "../../styles/cssBlock";
import { Toast } from "../../widgets";
import Spawn from "./Spawn";
import {
  effortControl,
  effortOptionValues,
  effortSelect,
  expectModelReadsDefault,
  expectResolvedDefaultModel,
  fillPrompt,
  installSpawnTestHooks,
  modelListRequests,
  modelListScript,
  modelState,
  modelTrigger,
  modelValue,
  pickModel,
  readyClient,
  renderSpawn,
  routedDiscoveryDefault,
  scriptModelList,
  seedSources,
  settled,
  setupUser,
  setWorkingDir,
} from "./spawnTestUtils";

/** The visible effort readout (aria-hidden: the select speaks the value). */
function effortReadout(): HTMLElement {
  const trigger = screen.getByTestId("spawn-effort");
  const readout = trigger.querySelector("[data-testid='spawn-effort-value']");
  if (!readout) throw new Error("the card's effort control has no visible readout");
  return readout as HTMLElement;
}

/** Effort is unset and its readout names the resolved default `level`. */
function expectEffortDefaultNames(level: string): void {
  expect(effortSelect().value).toBe("");
  expect(effortOptionLabels()[0]).toContain(level);
  expect(effortReadout().textContent).toBe(effortOptionLabels()[0]);
}
installSpawnTestHooks();

// --- kata xgk8: Model's "(default)" must not claim an answer the daemon --
// --- will refuse -----------------------------------------------------------
//
// The daemon's thread/start resolves Model from the SAME layered launch
// config evener/launch/resolve previews (app_threadlifecycle.go: overrides.Model
// wins when set, otherwise the resolved Effective.Model - empty is refused
// with "model is required"). Leaving Model untouched sends no model
// override, so an empty resolve preview means the daemon WILL refuse the
// submit - "(default)" next to a working Effort default is a lie in that
// state. The preview is fail-open (an unmocked/failing resolve never blocks
// Spawn) - only a CONFIRMED empty default does.

test("Model keeps reading '(default)' and Spawn stays untouched when the hub resolves a real default (kata xgk8, happy path)", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/resolve", () => ({
      effective: { model: "anthropic/claude-sonnet-4-5" },
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await settled();

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  expectModelReadsDefault();
  expect(screen.queryByRole("alert")).toBeNull();
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);

  await fillPrompt(user, "do the thing");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
});

test("kata xgk8: Model reads as required (not '(default)') and Spawn is disabled when the hub has no default model", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/resolve", () => ({ effective: {}, layers: {}, provenance: {} }));
  });
  renderSpawn(fake);
  await settled();

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  await waitFor(() => expect(modelState()).toBe("required"));
  expect(screen.getByRole("alert").dataset.reason).toBe("no-default-model");
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);

  // Defense in depth: the ⌘+Enter submit chord must not bypass the disabled
  // button either (handleSpawn's own guard, not just the button's attribute).
  await fillPrompt(user, "do the thing");
  await user.keyboard("{Meta>}{Enter}{/Meta}");
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
});

test("kata xgk8: choosing a model clears the required state and lets Start proceed", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/resolve", () => ({ effective: {}, layers: {}, provenance: {} }));
  });
  renderSpawn(fake);
  await settled();

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true));

  await user.click(modelTrigger());
  const combo = await screen.findByRole("combobox", { name: "Model" });
  await user.type(combo, "gpt-5");
  await user.click(await screen.findByText("openai/gpt-5"));

  expect(screen.queryByRole("alert")).toBeNull();
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);

  await fillPrompt(user, "do the thing");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  const start = fake.calls.find((c) => c.method === "thread/start");
  expect(start?.params).toMatchObject({ model: "openai/gpt-5" });
});

// The daemon's own launch-config schema exposes a SECOND "model" wireField
// inside Advanced options (perLaunchEvenerOptions - schema.go's real "model"
// LaunchOption, kind modelPicker) alongside the top-level Model chip; floor
// §1.11 has the Advanced field's override win at submit time. The preview
// here must agree - an override set ONLY through Advanced options satisfies
// the requirement without the top-level chip ever leaving "(default)".
test("kata xgk8: an Advanced-options model override satisfies the requirement without touching the top-level Model field", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({
      options: [
        {
          field: "model",
          wireField: "model",
          label: "Model",
          group: "general",
          kind: "modelPicker",
          perLaunch: true,
        },
      ],
    }));
    f.on("evener/launch/resolve", (params) => ({
      effective: { model: params.launchOverrides?.model ?? "" },
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await settled();

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true));

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const modelPickers = screen.getAllByRole("button", { name: /change model/i });
  // The Advanced-panel picker is the last one added: the card's trigger and
  // the top-level chip both render above it.
  await user.click(modelPickers[modelPickers.length - 1]!);
  const combo = await screen.findByRole("combobox", { name: "Model" });
  await user.type(combo, "gpt-5");
  await user.click(await screen.findByText("openai/gpt-5"));

  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
  expectModelReadsDefault(); // top-level chip untouched
});

// roborev: the Advanced-options model override used to win at submit even
// after the user touched the visible top-level Model control, so the chip
// displayed one model while thread/start launched another. Touching the
// top-level control is the user's newest intent and must clear the stale
// Advanced override (and its displayed value).
test("changing the top-level Model clears a standing Advanced-options model override (roborev)", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({
      options: [
        { field: "model", wireField: "model", label: "Model", group: "general", kind: "modelPicker", perLaunch: true },
      ],
    }));
    f.on("evener/launch/resolve", () => ({ effective: {}, layers: {}, provenance: {} }));
  });
  renderSpawn(fake);
  await settled();
  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  // A standing Advanced-options override: openai/gpt-5.
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const advancedPickers = screen.getAllByRole("button", { name: /change model/i });
  await user.click(advancedPickers[advancedPickers.length - 1]!);
  const advancedCombo = await screen.findByRole("combobox", { name: "Model" });
  await user.type(advancedCombo, "gpt-5");
  await user.click(await screen.findByText("openai/gpt-5"));

  // Then the user changes the visible top-level Model control.
  await pickModel(user, "claude-sonnet-4-5", "anthropic/claude-sonnet-4-5");
  expect(modelValue().textContent).toBe("anthropic/claude-sonnet-4-5");
  // The Advanced panel must not keep displaying the discarded value.
  const advancedPickersAfter = screen.getAllByRole("button", { name: /change model/i });
  expect(advancedPickersAfter[advancedPickersAfter.length - 1]!.textContent).not.toContain("openai/gpt-5");

  await fillPrompt(user, "model precedence");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((c) => c.method === "thread/start")).toBe(true));
  const params = fake.calls.find((c) => c.method === "thread/start")?.params as ThreadStartParams;
  // The launched request carries what the top-level control displays, not the
  // stale override.
  expect(params.model).toBe("anthropic/claude-sonnet-4-5");
  expect(params.launchOverrides?.model).toBeUndefined();
});

// The same mismatch for reasoning effort: the visible top-level Effort select
// was inert while an Advanced-options reasoning_effort override stood.
test("changing the top-level Effort clears a standing Advanced-options effort override (roborev)", async () => {
  const user = setupUser();
  const fake = readyClient((f) =>
    f.on("evener/launch/schema", () => ({
      options: [
        {
          field: "reasoning_effort",
          wireField: "reasoningEffort",
          label: "Reasoning effort",
          group: "model",
          kind: "select",
          perLaunch: true,
          choices: [
            { value: "low", label: "low" },
            { value: "high", label: "high" },
          ],
        },
      ],
    })),
  );
  renderSpawn(fake);
  await settled();
  await setWorkingDir(user, "/tmp/project");

  // A standing Advanced-options override: high.
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.selectOptions(screen.getByLabelText("Reasoning effort"), "high");

  // Then the user changes the visible top-level Effort control.
  fireEvent.change(effortControl(), { target: { value: "low" } });
  expect((effortControl() as HTMLSelectElement).value).toBe("low");
  // The Advanced field is cleared too, so it cannot show the discarded value.
  expect((screen.getByLabelText("Reasoning effort") as HTMLSelectElement).value).toBe("");

  await fillPrompt(user, "effort precedence");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((c) => c.method === "thread/start")).toBe(true));
  const params = fake.calls.find((c) => c.method === "thread/start")?.params as ThreadStartParams;
  expect(params.reasoningEffort).toBe("low");
  expect(params.launchOverrides?.reasoningEffort).toBeUndefined();
});

// The reverse order stays intact: an Advanced override set AFTER the
// top-level control is the user's newest intent and still wins at submit.
test("an Advanced-options model override set after the top-level Model still wins (roborev)", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({
      options: [
        { field: "model", wireField: "model", label: "Model", group: "general", kind: "modelPicker", perLaunch: true },
      ],
    }));
    f.on("evener/launch/resolve", () => ({ effective: {}, layers: {}, provenance: {} }));
  });
  renderSpawn(fake);
  await settled();
  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  await pickModel(user, "claude-sonnet-4-5", "anthropic/claude-sonnet-4-5");

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const advancedPickers = screen.getAllByRole("button", { name: /change model/i });
  await user.click(advancedPickers[advancedPickers.length - 1]!);
  const advancedCombo = await screen.findByRole("combobox", { name: "Model" });
  await user.type(advancedCombo, "gpt-5");
  await user.click(await screen.findByText("openai/gpt-5"));

  await fillPrompt(user, "override wins");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((c) => c.method === "thread/start")).toBe(true));
  const params = fake.calls.find((c) => c.method === "thread/start")?.params as ThreadStartParams;
  expect(params.model).toBe("openai/gpt-5");
});

test("an Advanced-options effort override set after the top-level Effort still wins (roborev)", async () => {
  const user = setupUser();
  const fake = readyClient((f) =>
    f.on("evener/launch/schema", () => ({
      options: [
        {
          field: "reasoning_effort",
          wireField: "reasoningEffort",
          label: "Reasoning effort",
          group: "model",
          kind: "select",
          perLaunch: true,
          choices: [
            { value: "low", label: "low" },
            { value: "high", label: "high" },
          ],
        },
      ],
    })),
  );
  renderSpawn(fake);
  await settled();
  await setWorkingDir(user, "/tmp/project");

  fireEvent.change(effortControl(), { target: { value: "low" } });
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.selectOptions(screen.getByLabelText("Reasoning effort"), "high");

  await fillPrompt(user, "effort override wins");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((c) => c.method === "thread/start")).toBe(true));
  const params = fake.calls.find((c) => c.method === "thread/start")?.params as ThreadStartParams;
  expect(params.reasoningEffort).toBe("high");
});

// --- resolved-default labels -------------------------------------------------
//
// A launch-config control whose unset state reads "(default)" names the value
// a session started now would inherit instead: the field's entry in the
// effective layer of evener/launch/resolve for the current working directory.
// Until that resolve lands - or if it fails - the label stays plain
// "(default)": an unresolved answer must never be dressed up as a known one.

function effortOptionLabels(): (string | null)[] {
  const select = screen.getByLabelText("Prompt reasoning effort") as HTMLSelectElement;
  return Array.from(select.options).map((o) => o.textContent);
}

test("Effort, Model, and the mobile rows name the resolved default once launch/resolve lands", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/resolve", () => ({
      effective: { model: "anthropic/claude-sonnet-4-5", reasoningEffort: "high" },
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await settled();

  // No working directory yet, so no resolve has run: the empty option names
  // no effort.
  expect(effortOptionLabels()[0]).not.toContain("high");
  expect(modelState()).toBe("default");

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  // Effort's empty option names the inherited effort.
  await waitFor(() => expectEffortDefaultNames("high"));
  // The card's Model trigger names the inherited model.
  expect(modelTrigger().textContent).toContain("anthropic/claude-sonnet-4-5");
  expectResolvedDefaultModel("anthropic/claude-sonnet-4-5");
});

// The visible effort readout is the selected option's own label - including
// the resolved default's ("high (default)"), not the bare "(default)" the
// empty value renders before the resolve lands.
test("the card's effort readout names the resolved default once launch/resolve lands", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/resolve", () => ({
      effective: { model: "anthropic/claude-sonnet-4-5", reasoningEffort: "high" },
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await settled();

  expect(effortReadout().textContent).not.toBe("");
  expect(effortReadout().textContent).not.toContain("high");

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  await waitFor(() => expectEffortDefaultNames("high"));
});

// Access mode is the chip-level face of the launch-config sandbox field
// (floor §1.8), so it follows the same resolved-default rule: its empty
// option names the inherited sandbox in the chip's own friendly wording.
test("Access mode names the resolved sandbox default once launch/resolve lands", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/resolve", () => ({
      effective: { sandbox: "workspace-write" },
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await settled();

  // The desktop Access mode select lives inside the Advanced panel.
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const accessOptionLabels = () => {
    const select = screen.getByLabelText("Access mode") as HTMLSelectElement;
    return Array.from(select.options).map((o) => o.textContent);
  };

  const workspaceWrite = () =>
    Array.from((screen.getByLabelText("Access mode") as HTMLSelectElement).options).find(
      (o) => o.value === "workspace-write",
    )?.textContent ?? "";
  expect(workspaceWrite()).not.toBe("");

  // No working directory yet, so no resolve has run: the empty option names no
  // sandbox.
  expect(accessOptionLabels()[0]).not.toContain(workspaceWrite());

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  // The desktop Access mode select's empty option names the inherited sandbox.
  await waitFor(() => expect(accessOptionLabels()[0]).toContain(workspaceWrite()));
  // The mobile Access mode row derives its resting label from the same
  // options list, so it inherits the resolved wording too.
  const mobileConfig = screen.getByTestId("spawn-mobile-config");
  const accessRow = mobileConfig.querySelector('[data-label="Access mode"]');
  expect(accessRow?.textContent).toContain(accessOptionLabels()[0]);
});

test.each(["desktop", "mobile"])("%s access inheritance survives an explicit mode round trip", async (surface) => {
  const user = setupUser();
  const inheritedSandbox = "off";
  const fake = readyClient((client) => {
    client.on("evener/launch/schema", () => ({
      options: [
        {
          field: "sandbox",
          wireField: "sandbox",
          label: "Sandbox",
          group: "sandbox",
          kind: "select",
          perLaunch: true,
          choices: ["", "off", "read-only", "workspace-write", "restricted"].map((value) => ({ value, label: value })),
        },
      ],
    }));
    client.on("evener/launch/resolve", ({ launchOverrides }) => ({
      effective: {
        model: "anthropic/claude-sonnet-4-5",
        sandbox: (launchOverrides as { sandbox?: string } | undefined)?.sandbox ?? inheritedSandbox,
      },
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await settled();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const select = screen.getByLabelText("Access mode") as HTMLSelectElement;
  const option = (value: string) => Array.from(select.options).find((entry) => entry.value === value);
  const unqualifiedDefault = option("")?.textContent;
  const sandboxSelect = screen.getByLabelText("Sandbox") as HTMLSelectElement;
  const sandboxDefault = () => Array.from(sandboxSelect.options).find((entry) => entry.value === "")?.textContent;
  const unqualifiedSandboxDefault = sandboxDefault();
  const mobileOptions = async () => {
    const row = screen.getByTestId("spawn-mobile-config").querySelector('[data-label="Access mode"] button');
    if (!(row instanceof HTMLElement)) throw new Error("missing access row");
    await user.click(row);
    return within(screen.getByRole("dialog"))
      .getAllByRole("button")
      .filter((button) => button.hasAttribute("aria-pressed"));
  };
  await setWorkingDir(user, "/tmp/inherited-access");
  const summary = screen.getByTestId("spawn-effective-access");
  await waitFor(() => expect(summary.getAttribute("data-sandbox")).toBe(inheritedSandbox));
  expect(option("")?.textContent).toContain(option("full")?.textContent);
  if (surface === "desktop") {
    await user.selectOptions(select, "read-only");
  } else {
    const index = Array.from(select.options).findIndex((entry) => entry.value === "read-only");
    const target = (await mobileOptions())[index];
    if (!target) throw new Error("missing explicit access option");
    await user.click(target);
  }
  await waitFor(() => expect(summary.getAttribute("data-sandbox")).toBe("read-only"));
  expect(option("")?.textContent).toBe(unqualifiedDefault);
  expect(sandboxDefault()).toBe(unqualifiedSandboxDefault);
  if (surface === "desktop") {
    await user.selectOptions(select, "");
  } else {
    const options = await mobileOptions();
    const inherit = options[0];
    if (!inherit) throw new Error("missing inherited access option");
    expect(inherit.textContent).toBe(unqualifiedDefault);
    await user.click(inherit);
  }
  await waitFor(() => expect(summary.getAttribute("data-sandbox")).toBe(inheritedSandbox));
  const resolve = fake.calls.filter((call) => call.method === "evener/launch/resolve").at(-1);
  expect(resolve?.params).not.toHaveProperty("launchOverrides.sandbox");
  expect(select.value).toBe("");
  expect(option("")?.textContent).toContain(option("full")?.textContent);
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));
  expect(fake.calls.find((call) => call.method === "thread/start")?.params).not.toHaveProperty(
    "launchOverrides.sandbox",
  );
});

test("missing provider prerequisite precedes the editable composer and suppresses the later model prerequisite", async () => {
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
    fake.on("model/list", () => ({ data: [] }));
    fake.on("evener/launch/resolve", () => ({ effective: { model: "" }, layers: {}, provenance: {} }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);

  const prerequisite = await screen.findByTestId("spawn-provider-prerequisite");
  const composer = screen.getByTestId("spawn-prompt-card");
  expect(prerequisite.compareDocumentPosition(composer) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
  expect(screen.queryByTestId("spawn-provider-prerequisite")?.parentElement).not.toBeNull();
  expect(document.querySelector('[data-reason="no-default-model"], [data-reason="provider-model"]')).toBeNull();
  expect(screen.getByRole("textbox", { name: "Prompt" }).hasAttribute("disabled")).toBe(false);
});

test.each([
  ["off", "full"],
  ["read-only", "read-only"],
  ["workspace-write", "workspace-write"],
  ["restricted", "restricted"],
  ["custom-sandbox", "custom-sandbox"],
])("effective access renders resolver sandbox %s as machine access %s", async (sandbox, access) => {
  const user = setupUser();
  const fake = readyClient((client) => {
    client.on("evener/launch/resolve", () => ({
      effective: { sandbox },
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await settled();
  await setWorkingDir(user, "/tmp/effective-access");

  const summary = await screen.findByTestId("spawn-effective-access");
  await waitFor(() => expect(summary.getAttribute("data-state")).toBe("ready"));
  expect(summary.getAttribute("data-sandbox")).toBe(sandbox);
  expect(summary.getAttribute("data-access")).toBe(access);
  expect(summary.getAttribute("data-source")).toBe("access");
});

test.each([false, true])(
  "Change leaves an earlier unrelated disclosure closed and focuses this Spawn control, Advanced initially open=%s",
  async (initiallyOpen) => {
    const user = setupUser();
    const unrelated = vi.fn();
    const fake = readyClient((client) => {
      client.on("evener/launch/resolve", () => ({ effective: { sandbox: "read-only" }, layers: {}, provenance: {} }));
    });
    connectionStore.getState().connect(fake);
    render(
      <>
        <button type="button" aria-controls="unrelated-panel" aria-expanded="false" onClick={unrelated}>
          Unrelated disclosure
        </button>
        <ClientProvider client={fake}>
          <Spawn params={{}} paneId="spawn-1" focused />
          <Toast />
        </ClientProvider>
      </>,
    );
    await settled();
    await setWorkingDir(user, "/tmp/scoped-effective-access");
    await waitFor(() => expect(screen.getByTestId("spawn-effective-access").getAttribute("data-state")).toBe("ready"));
    if (initiallyOpen) await user.click(screen.getByRole("button", { name: "Advanced options" }));

    await user.click(within(screen.getByTestId("spawn-effective-access")).getByRole("button", { name: "Change" }));

    expect(unrelated).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Advanced options" }).getAttribute("aria-expanded")).toBe("true");
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText("Access mode")));
  },
);

test("effective access clears stale resolution while the next directory is pending without changing Start eligibility", async () => {
  const user = setupUser();
  const first = deferred<LaunchConfigResolved>();
  const second = deferred<LaunchConfigResolved>();
  const fake = readyClient((client) => {
    client.on("evener/launch/resolve", ({ cwd }) => (cwd === "/tmp/access-a" ? first.promise : second.promise));
  });
  renderSpawn(fake);
  await settled();
  const start = screen.getByTestId("spawn-submit") as HTMLButtonElement;

  await setWorkingDir(user, "/tmp/access-a");
  const summary = screen.getByTestId("spawn-effective-access");
  expect(summary.getAttribute("data-state")).toBe("pending");
  expect(summary.hasAttribute("data-sandbox")).toBe(false);
  await act(async () =>
    first.resolve({
      effective: { sandbox: "read-only", model: "anthropic/claude-sonnet-4-5" },
      layers: {},
      provenance: {},
    }),
  );
  await waitFor(() => expect(summary.getAttribute("data-sandbox")).toBe("read-only"));
  const eligibility = start.disabled;

  await setWorkingDir(user, "/tmp/access-b");
  await waitFor(() => expect(summary.getAttribute("data-state")).toBe("pending"));
  expect(summary.hasAttribute("data-sandbox")).toBe(false);
  expect(start.disabled).toBe(eligibility);
  await act(async () =>
    second.resolve({
      effective: { sandbox: "workspace-write", model: "anthropic/claude-sonnet-4-5" },
      layers: {},
      provenance: {},
    }),
  );
  await waitFor(() => expect(summary.getAttribute("data-sandbox")).toBe("workspace-write"));
  expect(start.disabled).toBe(eligibility);
});

test("effective access Change focuses Access mode", async () => {
  const user = setupUser();
  const fake = readyClient((client) => {
    client.on("evener/launch/resolve", () => ({ effective: { sandbox: "read-only" }, layers: {}, provenance: {} }));
  });
  renderSpawn(fake);
  await settled();
  await setWorkingDir(user, "/tmp/effective-access-focus");
  const summary = screen.getByTestId("spawn-effective-access");
  await waitFor(() => expect(summary.getAttribute("data-state")).toBe("ready"));
  await user.click(within(summary).getByRole("button", { name: "Change" }));
  await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText("Access mode")));
});

test.each([false, true])("Advanced sandbox select owns Change focus, initially open=%s", async (initiallyOpen) => {
  const user = setupUser();
  // launchconfig/schema.go: sandbox is a select, including an inherit choice.
  const sandboxOption: LaunchOption = {
    field: "sandbox",
    wireField: "sandbox",
    label: "Sandbox",
    group: "sandbox",
    kind: "select",
    perLaunch: true,
    choices: [
      { value: "", label: "(inherit)" },
      { value: "off", label: "off" },
      { value: "read-only", label: "read-only" },
      { value: "workspace-write", label: "workspace-write" },
      { value: "restricted", label: "restricted" },
    ],
  };
  const fake = readyClient((client) => {
    client.on("evener/launch/schema", () => ({ options: [sandboxOption] }));
    client.on("evener/launch/resolve", ({ launchOverrides }) => ({
      effective: { sandbox: (launchOverrides as { sandbox?: string } | undefined)?.sandbox ?? "workspace-write" },
      layers: {},
      provenance: {},
    }));
  });
  const unrelated = vi.fn();
  connectionStore.getState().connect(fake);
  render(
    <>
      <button type="button" aria-controls="unrelated-panel" aria-expanded="false" onClick={unrelated}>
        Unrelated disclosure
      </button>
      <ClientProvider client={fake}>
        <Spawn params={{}} paneId="spawn-1" focused />
        <Toast />
      </ClientProvider>
    </>,
  );
  await settled();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.selectOptions(screen.getByLabelText("Access mode"), "full");
  await user.selectOptions(screen.getByLabelText("Sandbox"), "restricted");
  await setWorkingDir(user, "/tmp/advanced-effective-access");

  const summary = screen.getByTestId("spawn-effective-access");
  await waitFor(() => expect(summary.getAttribute("data-sandbox")).toBe("restricted"));
  expect(summary.getAttribute("data-access")).toBe("restricted");
  expect(summary.getAttribute("data-source")).toBe("advanced");
  const resolveCall = fake.calls.find(
    (call) =>
      call.method === "evener/launch/resolve" &&
      (call.params as { cwd?: string }).cwd === "/tmp/advanced-effective-access",
  );
  expect(resolveCall?.params).toMatchObject({ launchOverrides: { sandbox: "restricted" } });
  expect((screen.getByLabelText("Access mode") as HTMLSelectElement).value).toBe("full");
  if (!initiallyOpen) await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.click(within(summary).getByRole("button", { name: "Change" }));
  expect(unrelated).not.toHaveBeenCalled();
  expect(screen.getByRole("button", { name: "Advanced options" }).getAttribute("aria-expanded")).toBe("true");
  await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("combobox", { name: "Sandbox" })));
});

test("Advanced sandbox override wins in effective access and Change focuses its control", async () => {
  const user = setupUser();
  const sandboxOption: LaunchOption = {
    field: "sandbox",
    wireField: "sandbox",
    label: "Sandbox mode",
    group: "general",
    kind: "radio",
    perLaunch: true,
    choices: [
      { value: "off", label: "Off" },
      { value: "restricted", label: "Restricted" },
    ],
  };
  const fake = readyClient((client) => {
    client.on("evener/launch/schema", () => ({ options: [sandboxOption] }));
    client.on("evener/launch/resolve", ({ launchOverrides }) => ({
      effective: { sandbox: (launchOverrides as { sandbox?: string } | undefined)?.sandbox ?? "workspace-write" },
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await settled();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.selectOptions(screen.getByLabelText("Access mode"), "full");
  await user.click(screen.getByRole("radio", { name: "Restricted" }));
  await setWorkingDir(user, "/tmp/advanced-effective-access");

  const summary = screen.getByTestId("spawn-effective-access");
  await waitFor(() => expect(summary.getAttribute("data-sandbox")).toBe("restricted"));
  expect(summary.getAttribute("data-access")).toBe("restricted");
  expect(summary.getAttribute("data-source")).toBe("advanced");
  const resolveCall = fake.calls.find(
    (call) =>
      call.method === "evener/launch/resolve" &&
      (call.params as { cwd?: string }).cwd === "/tmp/advanced-effective-access",
  );
  expect(resolveCall?.params).toMatchObject({ launchOverrides: { sandbox: "restricted" } });
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.click(within(summary).getByRole("button", { name: "Change" }));
  await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("radio", { name: "Restricted" })));
});

test("the (default) labels stay plain when the resolve fails", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/resolve", () => {
      throw new Error("resolve down");
    });
  });
  renderSpawn(fake);
  await settled();
  const plainEffort = effortOptionLabels()[0];

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));
  await act(async () => {}); // let the rejection's state writes land

  expect(effortOptionLabels()[0]).toBe(plainEffort);
  expect(modelTrigger().textContent).not.toContain("claude");
  expect(modelState()).toBe("default");
  expect(screen.getByTestId("spawn-effective-access").getAttribute("data-state")).toBe("error");
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);
});

// The Advanced panel's own unset labels resolve the same way, off the same
// resolve the pane already runs: a boolean field reads "On (default)"/"Off
// (default)" per the effective value.
test("an Advanced-options boolean names the resolved default (On/Off)", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({
      options: [
        {
          field: "api_log",
          wireField: "apiLog",
          label: "Log API requests",
          group: "general",
          kind: "boolean",
          perLaunch: true,
        },
      ],
    }));
    f.on("evener/launch/resolve", () => ({
      effective: { model: "anthropic/claude-sonnet-4-5", apiLog: true },
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await settled();

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const select = screen.getByLabelText("Log API requests") as HTMLSelectElement;
  const [unset, on, off] = Array.from(select.options);
  expect(unset?.textContent).toContain(on?.textContent);
  expect(unset?.textContent).not.toContain(off?.textContent);
});

// --- uncredentialed-default fallback ---------------------------------------
//
// A resolved default whose provider has no credentials is a guaranteed
// thread/start failure (spawn.go's "provider credentials missing for
// <provider>..."). model/list only enumerates providers it could actually
// construct (launchcheck.go), so a default provider missing from that SET is
// the honest signal this form has for "not credentialed" - see the effect's
// own comment in Spawn.tsx. "(default)" is never an explicit row (only the
// closed trigger's text for value === ""), so hiding it IS preselecting a
// real model instead of leaving Model blank.

test("preselects the first launchable model when the resolved default's provider isn't in the launchable set", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("model/list", () => ({
      data: [
        { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" },
        { provider: "anthropic", model: "claude-opus-4", displayName: "anthropic/claude-opus-4" },
      ],
    }));
    f.on("evener/launch/resolve", () => ({
      effective: { model: "openai/gpt-5.5" }, // launch.toml's default; openai has no credentials here
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await settled();

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  // Falls back to the FIRST launchable model, not merely "some" model -
  // model/list's own order, which scopedCatalog.ts preserves into the picker.
  await waitFor(() => expect(modelTrigger().textContent).toContain("anthropic/claude-sonnet-4-5"));
  expect(modelState()).toBe("chosen");
  expect(screen.queryByRole("alert")).toBeNull();
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);

  await fillPrompt(user, "do the thing");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  const start = fake.calls.find((c) => c.method === "thread/start");
  expect(start?.params).toMatchObject({ model: "anthropic/claude-sonnet-4-5" });
});

// An Advanced-options model override wins over the top-level chip at submit
// (floor §1.11, spawnSchema's resolveScalars), so while one is set the chip is
// not what launches: rewriting it in the fallback's name would display a
// model that does not launch. Here the override's provider is launchable
// while the user configures it and drops out of model/list afterwards - the
// state a removed credential leaves behind - and the untouched chip must not
// be rewritten.
test("an advanced model override stops the uncredentialed-default fallback from rewriting the chip", async () => {
  const user = setupUser();
  let openaiLaunchable = true;
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({
      options: [
        { field: "model", wireField: "model", label: "Model", group: "general", kind: "modelPicker", perLaunch: true },
      ],
    }));
    f.on("model/list", () => ({
      data: [
        { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" },
        ...(openaiLaunchable ? [{ provider: "openai", model: "gpt-5", displayName: "openai/gpt-5" }] : []),
      ],
    }));
    f.on("evener/launch/resolve", (params) => ({
      // The hub applies the override first (resolveScalars), so the effective
      // model IS the override whose provider stops being launchable below.
      effective: {
        model: params.launchOverrides?.model ?? "anthropic/claude-opus-4",
        reasoningEffort: openaiLaunchable ? "high" : "low",
      },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();

  await setWorkingDir(user, "/tmp/project");
  // A credentialed default lands first: the untouched chip reads its resolved
  // default and no fallback substitution fires.
  await waitFor(() => expectResolvedDefaultModel("anthropic/claude-opus-4"));

  // The override is configured while openai is still in model/list.
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const modelPickers = screen.getAllByRole("button", { name: /change model/i });
  await user.click(modelPickers[modelPickers.length - 1]!);
  const combo = await screen.findByRole("combobox", { name: "Model" });
  await user.type(combo, "gpt-5");
  await user.click(await screen.findByText("openai/gpt-5"));

  // Wait for that pass to land (the override, not the chip, is the resolved
  // effective model now) before the provider disappears, so the drop below is
  // the only pending catalog pass.
  await waitFor(() => expectResolvedDefaultModel("openai/gpt-5"));

  // openai drops out of model/list while the override stands, and the catalog
  // scope refreshes so the fallback effect re-reads the listing and the
  // resolve.
  openaiLaunchable = false;
  await act(async () => credentialsStore.getState().fetch());

  // "low (default)" marks the post-drop resolve landing (it replaced the
  // "high (default)" of the pass above), so the fallback has run its course
  // by the time the chip is judged.
  await waitFor(() => expectEffortDefaultNames("low"));
  // The chip still names the model that will launch - the override - and the
  // Advanced field still shows it.
  expectResolvedDefaultModel("openai/gpt-5");
  expect(modelTrigger().textContent).not.toContain("anthropic/claude-sonnet-4-5");
  const advancedPickersAfter = screen.getAllByRole("button", { name: /change model/i });
  expect(advancedPickersAfter[advancedPickersAfter.length - 1]!.textContent).toContain("openai/gpt-5");
});

test("entering onboarding for one draft scope does not suppress the fallback for the next", async () => {
  const user = setupUser();
  // The first scope resolves a credentialed default, so no fallback runs and
  // Model stays empty. The working-directory switch starts a new catalog scope
  // whose default is uncredentialed - exactly when the fallback must apply.
  let defaultModel = "anthropic/claude-opus-4";
  const fake = readyClient((f) => {
    // Nothing stored, so the pane offers the Connect provider entry point - the
    // path that marks this draft as having entered onboarding.
    f.on("evener/instance/list", () => ({
      instances: [],
      availableProviders: [
        {
          id: "openai",
          name: "OpenAI",
          protocol: "openai-chat",
          auth: "bearer",
          implicit: true,
          authModes: ["apiKey"],
          setup: {
            name: "openai",
            providerId: "openai",
            protocol: "openai-chat",
            auth: "bearer",
            implicit: true,
            isDefault: false,
            activeSource: "none",
            hasStoredOAuth: false,
            credentialRequired: true,
            authModes: ["apiKey"],
            baseUrl: "https://provider.example/v1",
            endpointFingerprint: "fp-provider",
          },
        },
      ],
    }));
    f.on("model/list", () => ({
      data: [
        { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" },
        { provider: "anthropic", model: "claude-opus-4", displayName: "anthropic/claude-opus-4" },
      ],
    }));
    f.on("evener/launch/resolve", () => ({
      effective: { model: defaultModel },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();

  await user.click(await screen.findByRole("button", { name: "Connect provider" }));
  await act(async () => {
    await vi.dynamicImportSettled();
  });
  await user.keyboard("{Escape}");

  defaultModel = "openai/gpt-5.5"; // openai has no credentials in this fixture
  await setWorkingDir(user, "/tmp/later-draft");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  await waitFor(() => expect(modelTrigger().textContent).toContain("anthropic/claude-sonnet-4-5"));
});

test("onboarding a second draft scope does not forget the first scope's explicit choice", async () => {
  const user = setupUser();
  // Both scopes start with a credentialed default so nothing auto-fills while
  // onboarding is opened; the first scope's default turns uncredentialed only
  // after both have been onboarded, which is when the fallback would replace
  // its (still explicit) choice.
  const uncredentialedDirs = new Set<string>();
  const fake = readyClient((f) => {
    f.on("evener/instance/list", () => ({
      instances: [],
      availableProviders: [
        {
          id: "openai",
          name: "OpenAI",
          protocol: "openai-chat",
          auth: "bearer",
          implicit: true,
          authModes: ["apiKey"],
          setup: {
            name: "openai",
            providerId: "openai",
            protocol: "openai-chat",
            auth: "bearer",
            implicit: true,
            isDefault: false,
            activeSource: "none",
            hasStoredOAuth: false,
            credentialRequired: true,
            authModes: ["apiKey"],
            baseUrl: "https://provider.example/v1",
            endpointFingerprint: "fp-provider",
          },
        },
      ],
    }));
    f.on("model/list", () => ({
      data: [{ provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" }],
    }));
    f.on("evener/launch/resolve", ({ cwd }) => ({
      effective: { model: uncredentialedDirs.has(cwd) ? "openai/gpt-5.5" : "anthropic/claude-opus-4" },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();

  for (const dir of ["/tmp/first-draft", "/tmp/second-draft"]) {
    await setWorkingDir(user, dir);
    await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));
    await user.click(screen.getByRole("button", { name: "Connect provider" }));
    await act(async () => {
      await vi.dynamicImportSettled();
    });
    await user.keyboard("{Escape}");
  }

  uncredentialedDirs.add("/tmp/first-draft");
  await setWorkingDir(user, "/tmp/first-draft");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));
  // The first draft's onboarding choice still stands, so the substitute
  // fallback must not fill its model in: the pane asks for an explicit choice.
  expect(modelState()).toBe("required");
  expect(modelTrigger().textContent).not.toContain("anthropic/claude-sonnet-4-5");
});

test("an unmanaged harness with a resolved default model does not demand a model choice", async () => {
  window.history.pushState({}, "", "/new?dir=/tmp/unmanaged-draft");
  localStorage.setItem("evener-hub.spawn-defaults./tmp/unmanaged-draft", JSON.stringify({ harness: "external" }));
  const fake = readyClient((f) => {
    f.on("evener/launch/resolve", () => ({
      effective: { model: "anthropic/claude-sonnet-4-5" },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));
  // The external harness carries its own model through unmanaged, so the
  // resolved default keeps the chip off the required state and Start live.
  expect(modelState()).not.toBe("required");
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);
});

test("an unmanaged harness whose hub resolves no default model still starts", async () => {
  window.history.pushState({}, "", "/new?dir=/tmp/unmanaged-no-default");
  localStorage.setItem("evener-hub.spawn-defaults./tmp/unmanaged-no-default", JSON.stringify({ harness: "external" }));
  const fake = readyClient((f) => {
    f.on("evener/launch/resolve", () => ({
      // No Evener default model, but a resolved effort so the readout settling
      // on "high (default)" proves the resolve response has been applied.
      effective: { model: "", reasoningEffort: "high" },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();

  await waitFor(() => expectEffortDefaultNames("high"));

  // The unmanaged harness carries its own model, so the hub's missing Evener
  // default must not become a requirement of this pane: no "Choose a model",
  // no requirement note, Start live.
  expect(modelState()).not.toBe("required");
  expect(modelState()).toBe("default");
  expect(screen.queryByRole("alert")).toBeNull();
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);
});

test("an unmanaged harness is never auto-filled by the uncredentialed-default fallback", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/unmanaged-fallback");
  localStorage.setItem("evener-hub.spawn-defaults./tmp/unmanaged-fallback", JSON.stringify({ harness: "external" }));
  const fake = readyClient((f) => {
    f.on("model/list", () => ({
      data: [{ provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" }],
    }));
    f.on("evener/launch/resolve", () => ({
      // openai is absent from model/list, so the fallback would otherwise
      // replace the untouched Model with the first launchable Evener model.
      effective: { model: "openai/gpt-5.5" },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  // The fallback must not run for an unmanaged harness: Model stays untouched
  // and the chip keeps naming the resolved default instead of the first
  // launchable Evener model.
  await waitFor(() => expectResolvedDefaultModel("openai/gpt-5.5"));
  expect(modelTrigger().textContent).not.toContain("anthropic/claude-sonnet-4-5");

  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  const start = fake.calls.find((c) => c.method === "thread/start");
  if (!start) throw new Error("start was not issued");
  expect((start.params as ThreadStartParams).model).not.toBe("anthropic/claude-sonnet-4-5");
});

test("an Advanced-options model override after onboarding satisfies the requirement", async () => {
  const user = setupUser();
  let saved = false;
  const setup = {
    name: "openai",
    providerId: "openai",
    protocol: "openai-chat",
    auth: "bearer",
    implicit: true,
    isDefault: false,
    activeSource: "none",
    hasStoredOAuth: false,
    credentialRequired: true,
    authModes: ["apiKey"],
    baseUrl: "https://provider.example/v1",
    endpointFingerprint: "fp-provider",
  };
  const client = readyClient((fake) => {
    fake.on("evener/launch/schema", () => ({
      options: [
        { field: "model", wireField: "model", label: "Model", group: "general", kind: "modelPicker", perLaunch: true },
      ],
    }));
    fake.on("evener/instance/list", () => {
      const row = { ...setup, activeSource: saved ? "store" : "none", hasStoredFile: saved };
      return {
        instances: saved ? [row] : [],
        availableProviders: [
          {
            id: "openai",
            name: "OpenAI",
            protocol: row.protocol,
            auth: row.auth,
            implicit: true,
            authModes: ["apiKey"],
            setup: row,
          },
        ],
      };
    });
    fake.on("model/list", () => ({
      data: saved ? [{ provider: "openai", model: "from-server", displayName: "Server choice" }] : [],
    }));
    fake.on("evener/auth/apiKey/set", ({ provider }) => {
      saved = true;
      return {
        provider,
        supported: true,
        signedIn: true,
        activeSource: "store",
        hasStoredOAuth: false,
      };
    });
    fake.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
    // The resolved default is non-empty and uncredentialed, and it does NOT
    // follow the Advanced override: the requirement must be satisfied by the
    // override itself rather than by waiting for a re-resolve.
    fake.on("evener/launch/resolve", () => ({
      effective: { model: "missing/old-default" },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await setWorkingDir(user, "/tmp/guided-override");
  await user.click(screen.getByRole("button", { name: "Connect provider" }));
  await act(async () => {
    await vi.dynamicImportSettled();
  });
  await user.click(await screen.findByRole("button", { name: "OpenAI" }));
  await user.type(screen.getByLabelText("API key"), "fixture-only-key");
  fireEvent.click(screen.getByRole("button", { name: "Save and test" }));
  await settled();
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const modelPickers = screen.getAllByRole("button", { name: /change model/i });
  await user.click(modelPickers[modelPickers.length - 1]!);
  const combo = await screen.findByRole("combobox", { name: "Model" });
  await user.type(combo, "from");
  await user.click((await screen.findAllByRole("option", { name: /Server choice/ }))[0]!);

  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);
  expect(client.calls.filter((call) => call.method === "thread/start")).toEqual([]);
});

test("keeps the form usable and leaves Model at '(default)' when no provider is credentialed at all", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("model/list", () => ({ data: [] })); // nothing launchable to fall back to
    f.on("evener/launch/resolve", () => ({
      effective: { model: "openai/gpt-5.5" },
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await settled();

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  // No new dead-end UI: the server's own "provider credentials missing"
  // message on submit is what speaks here, not a form the picker can't
  // resolve on its own - the field stays exactly as it always has.
  expectModelReadsDefault();
  expect(screen.queryByRole("alert")).toBeNull();
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);
});

test("a sticky per-project model default is never clobbered by the uncredentialed-default fallback", async () => {
  // The sticky pref names a provider that IS launchable, but not the list's
  // first entry - if the fallback logic ignored modelRef and ran anyway, it
  // would silently overwrite this with models[0] ("anthropic/claude-opus-4").
  localStorage.setItem("evener-hub.spawn-defaults.global.working_dir", "/p");
  localStorage.setItem("evener-hub.spawn-defaults./p", JSON.stringify({ model: "anthropic/claude-sonnet-4-5" }));
  const fake = readyClient((f) => {
    f.on("model/list", () => ({
      data: [
        { provider: "anthropic", model: "claude-opus-4", displayName: "anthropic/claude-opus-4" },
        { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" },
      ],
    }));
    f.on("evener/launch/resolve", () => ({
      effective: { model: "openai/gpt-5.5" }, // openai uncredentialed
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await settled();

  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  expect(modelTrigger().textContent).toContain("anthropic/claude-sonnet-4-5");
  expect(modelTrigger().textContent).not.toContain("claude-opus-4");
});

test("auth notification retires global cleanup before the instance refresh completes", async () => {
  window.history.pushState({}, "", "/new?dir=/tmp/auth-generation");
  const saved = JSON.stringify({ model: "openai/newly-visible" });
  localStorage.setItem("evener-hub.spawn-defaults./tmp/auth-generation", saved);
  localStorage.setItem("evener-hub.spawn-defaults.global.model", "openai/newly-visible");
  const oldCatalog = deferred<ModelListResponse>();
  const instanceRefresh = deferred<InstanceListResponse>();
  const refreshStarted = deferred<void>();
  let notified = false;
  const client = readyClient((fake) => {
    fake.on("model/list", ({ harness, cwd }) =>
      harness === "evener" && cwd === undefined && !notified
        ? oldCatalog.promise
        : { data: [{ provider: "openai", model: "newly-visible" }] },
    );
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await settled();
  await act(async () => credentialsStore.getState().fetch());
  const refreshedInstances = { instances: credentialsStore.getState().instances, availableProviders: [] };
  client.on("evener/instance/list", () => {
    refreshStarted.resolve();
    return instanceRefresh.promise;
  });
  expect(modelValue().textContent).toBe("openai/newly-visible");
  // The obsolete global catalog must already be in flight (parked on
  // oldCatalog) before the notification, or resolving it later is a no-op.
  await waitFor(() =>
    expect(modelListRequests(client).some((params) => params.harness === "evener" && params.cwd === undefined)).toBe(
      true,
    ),
  );
  notified = true;
  await act(async () => client.emitNotification({ method: "evener/auth/updated", params: {} }));
  // The instance refetch is coalesced behind a timer; fire it, then await the
  // request it issues.
  await act(async () => {
    await vi.runOnlyPendingTimersAsync();
    await refreshStarted.promise;
  });
  try {
    await act(async () => oldCatalog.resolve({ data: [{ provider: "openai", model: "gpt-5" }] }));
    // Assert before releasing instance/list: its completion cannot repair
    // anything an obsolete global catalog has already removed.
    expect({
      model: modelValue().textContent,
      saved: localStorage.getItem("evener-hub.spawn-defaults./tmp/auth-generation"),
      global: localStorage.getItem("evener-hub.spawn-defaults.global.model"),
    }).toEqual({ model: "openai/newly-visible", saved, global: "openai/newly-visible" });
    expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
  } finally {
    await act(async () => instanceRefresh.resolve(refreshedInstances));
  }
  expect(modelValue().textContent).toBe("openai/newly-visible");
});

test("a model response from before a credential refresh cannot discard the saved selection", async () => {
  const saved = JSON.stringify({ model: "openai/gpt-5" });
  localStorage.setItem("evener-hub.spawn-defaults.global.working_dir", "/p");
  localStorage.setItem("evener-hub.spawn-defaults./p", saved);
  let refreshed = false;
  const pending: Array<(response: ModelListResponse) => void> = [];
  const client = readyClient((fake) => {
    fake.on("model/list", () =>
      refreshed
        ? { data: [{ provider: "openai", model: "gpt-5" }] }
        : new Promise<ModelListResponse>((resolve) => pending.push(resolve)),
    );
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await settled();
  await waitFor(() => expect(modelTrigger().textContent).toContain("openai/gpt-5"));
  expect(pending.length).toBeGreaterThan(0);
  refreshed = true;
  await act(async () => credentialsStore.getState().fetch());
  await act(async () => {
    for (const resolve of pending) resolve({ data: [{ provider: "openai", model: "gpt-4o" }] });
  });
  expect(modelTrigger().textContent).toContain("openai/gpt-5");
  expect(localStorage.getItem("evener-hub.spawn-defaults./p")).toBe(saved);
  expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
});

test("surfaces the discard notice when a prefilled model is no longer offered (floor §1.10)", async () => {
  localStorage.setItem("evener-hub.spawn-defaults.global.working_dir", "/p");
  localStorage.setItem("evener-hub.spawn-defaults./p", JSON.stringify({ model: "openai/gpt-4o" }));
  renderSpawn(readyClient());

  expect(await screen.findByText(/discarded last-used model openai\/gpt-4o/i)).toBeTruthy();
  // The stale blob was pruned by the sweep.
  await waitFor(() => expect(localStorage.getItem("evener-hub.spawn-defaults./p")).toBeNull());
});

test("the Effort select offers the selected model's own ladder and re-derives it on a model switch", async () => {
  const user = setupUser();
  scriptModelList([
    {
      provider: "anthropic",
      model: "claude-sonnet-4-5",
      displayName: "anthropic/claude-sonnet-4-5",
      supportsReasoning: true,
      reasoningEffortLevels: ["low", "medium", "high"],
    },
    {
      provider: "openai",
      model: "gpt-5",
      displayName: "openai/gpt-5",
      supportsReasoning: true,
      reasoningEffortLevels: ["minimal", "low", "medium", "high", "xhigh", "max"],
    },
  ]);
  renderSpawn(readyClient());
  await settled();

  await pickModel(user, "gpt-5", "openai/gpt-5");
  await waitFor(() =>
    expect(effortOptionValues()).toEqual(["", "minimal", "low", "medium", "high", "xhigh", "max", "none"]),
  );

  // A chosen level the next model's ladder doesn't name can't stay selected -
  // the select must never display a value it doesn't offer.
  await user.selectOptions(effortSelect(), "xhigh");
  await pickModel(user, "sonnet", "anthropic/claude-sonnet-4-5");
  await waitFor(() => expect(effortOptionValues()).toEqual(["", "low", "medium", "high", "none"]));
  expect(effortSelect().value).toBe("");
});

test("a model the catalog says cannot reason disables the Effort select and clears a chosen effort", async () => {
  const user = setupUser();
  scriptModelList([
    {
      provider: "anthropic",
      model: "claude-sonnet-4-5",
      displayName: "anthropic/claude-sonnet-4-5",
      supportsReasoning: true,
      reasoningEffortLevels: ["low", "medium", "high"],
    },
    {
      provider: "openai",
      model: "gpt-5",
      displayName: "openai/gpt-5",
      supportsReasoning: false,
      reasoningEffortLevels: [],
    },
  ]);
  renderSpawn(readyClient());
  await settled();

  await pickModel(user, "sonnet", "anthropic/claude-sonnet-4-5");
  await waitFor(() => expect(effortOptionValues()).toEqual(["", "low", "medium", "high", "none"]));
  await user.selectOptions(effortSelect(), "high");

  await pickModel(user, "gpt-5", "openai/gpt-5");
  await waitFor(() => expect(effortSelect().disabled).toBe(true));
  expect(effortSelect().value).toBe("");
});

// A disabled effort control must LOOK disabled: the visible wrapper carries
// the state (not just the transparent select inside it), so it drops its
// hover face and pointer cursor like every other disabled control.
test("a disabled effort control renders its disabled state on the visible wrapper", async () => {
  const user = setupUser();
  scriptModelList([
    {
      provider: "openai",
      model: "gpt-5",
      displayName: "openai/gpt-5",
      supportsReasoning: false,
      reasoningEffortLevels: [],
    },
  ]);
  renderSpawn(readyClient());
  await settled();

  await pickModel(user, "gpt-5", "openai/gpt-5");
  await waitFor(() => expect(effortSelect().disabled).toBe(true));

  const trigger = screen.getByTestId("spawn-effort");
  expect(trigger.getAttribute("data-disabled")).toBe("true");
});

// The transparent overlay select is the topmost hittable layer, so it must
// inherit the wrapper's cursor - its own `pointer` would otherwise win over
// the wrapper's `not-allowed` on a disabled control. CSS gate: jsdom
// evaluates no cascade, so the computed cursor is asserted on the source.
test("the effort overlay select inherits the wrapper cursor", () => {
  const spawnCss = readModuleCss(import.meta.url, "spawn.module.css");

  expect(spawnCss).toMatch(/\.effortSelect\s*\{[^}]*cursor:\s*inherit/);
  expect(spawnCss).toContain(".effortTrigger:not([data-disabled");
});

test("with Model left at '(default)', the Effort select follows the hub's resolved default model", async () => {
  const user = setupUser();
  scriptModelList([
    {
      provider: "anthropic",
      model: "claude-sonnet-4-5",
      displayName: "anthropic/claude-sonnet-4-5",
      supportsReasoning: true,
      reasoningEffortLevels: ["low", "medium", "high"],
    },
    {
      provider: "openai",
      model: "gpt-5",
      displayName: "openai/gpt-5",
      supportsReasoning: true,
      reasoningEffortLevels: ["low", "high"],
    },
  ]);
  const fake = readyClient((f) => {
    f.on("evener/launch/resolve", () => ({
      effective: { model: "openai/gpt-5" },
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await settled();

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  // gpt-5's provider IS in readyClient's model/list, so the uncredentialed
  // fallback doesn't preselect it - Model stays "(default)" and the ladder
  // still has to be gpt-5's own.
  expectModelReadsDefault();
  await waitFor(() => expect(effortOptionValues()).toEqual(["", "low", "high", "none"]));
});

test("the classic ladder remains when the hub can't enumerate the model's own levels", async () => {
  const user = setupUser();
  // The default model/list fixture has no reasoning metadata, so the catalog
  // degrades to label-only entries - the select must keep working.
  renderSpawn(readyClient());
  await settled();

  await pickModel(user, "gpt-5", "openai/gpt-5");
  await waitFor(() => expect(effortOptionValues()).toEqual(["", "minimal", "low", "medium", "high", "none"]));
});

// The pane-level Effort preview and the picker share one harness/cwd-scoped
// model/list promise. A rich response therefore reaches both consumers
// without the old REST enrichment request or a two-source merge race.
test("the Effort select and picker share one scoped model/list response", async () => {
  const user = setupUser();
  let resolve: ((response: ModelListResponse) => void) | undefined;
  const fake = readyClient((f) => {
    f.on("model/list", ({ harness }) =>
      harness === "evener"
        ? { data: [] }
        : new Promise<ModelListResponse>((done) => {
            resolve = done;
          }),
    );
  });
  renderSpawn(fake, true, { connectStore: false });
  await settled();

  await waitFor(() => expect(resolve).toBeDefined());
  if (!resolve) throw new Error("model/list test response resolver was not installed");
  const resolveModelList = resolve;
  await user.click(modelTrigger());
  expect(screen.getByRole("combobox", { name: "Model" })).toBeTruthy();
  expect(modelListRequests(fake).filter((params) => params.harness === undefined)).toHaveLength(1);

  await act(async () => {
    resolveModelList({
      data: [
        {
          provider: "openai",
          model: "gpt-5",
          displayName: "openai/gpt-5",
          supportsReasoning: true,
          reasoningEffortLevels: ["minimal", "low", "medium", "high", "xhigh", "max"],
        },
      ],
    });
  });

  expect(
    modelListRequests(fake).filter((params) => params.harness === "evener" && params.cwd === undefined),
  ).toHaveLength(1);
  await user.click(await screen.findByText("openai/gpt-5"));
  await waitFor(() =>
    expect(effortOptionValues()).toEqual(["", "minimal", "low", "medium", "high", "xhigh", "max", "none"]),
  );
  expect(modelListRequests(fake).filter((params) => params.harness === undefined)).toHaveLength(1);
});

// A credential change can make models discoverable (a stored Vertex credential
// JSON enables the publisher-model listing), so the pane's own scoped
// model/list cache must not outlive evener/auth/updated: the catalog reloads
// and the picker sees the new listing without a remount.
test("evener/auth/updated drops the pane's model/list cache so the catalog and picker reload", async () => {
  const user = setupUser();
  const fake = readyClient();
  renderSpawn(fake, true, { connectStore: false });
  await settled();
  await waitFor(() => expect(modelListRequests(fake).filter((params) => params.harness === undefined)).toHaveLength(1));
  expect(
    modelListRequests(fake).filter((params) => params.harness === "evener" && params.cwd === undefined),
  ).toHaveLength(1);

  modelListScript.override = [
    { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" },
    { provider: "google-vertex", model: "gemini-3.8-flash", displayName: "google-vertex/gemini-3.8-flash" },
  ];
  act(() => fake.emitNotification({ method: "evener/auth/updated", params: { provider: "google-vertex" } }));
  await waitFor(() => expect(modelListRequests(fake).filter((params) => params.harness === undefined)).toHaveLength(2));
  expect(
    modelListRequests(fake).filter((params) => params.harness === "evener" && params.cwd === undefined),
  ).toHaveLength(2);

  await user.click(modelTrigger());
  const combo = await screen.findByRole("combobox", { name: "Model" });
  await user.clear(combo);
  await user.type(combo, "gemini");
  await screen.findByText("google-vertex/gemini-3.8-flash");
  // The picker shares the reloaded promise rather than issuing a third call.
  expect(modelListRequests(fake).filter((params) => params.harness === undefined)).toHaveLength(2);
  expect(
    modelListRequests(fake).filter((params) => params.harness === "evener" && params.cwd === undefined),
  ).toHaveLength(2);
});

// The hub announces a refreshed model list on evener/auth/updated (#3539). An
// open picker updates in place: the list it shows stays up, with no loading
// state in between, and the new model appears without reopening it.
test("an open model picker updates in place when the hub announces a refreshed list", async () => {
  const user = setupUser();
  const fake = readyClient();
  renderSpawn(fake);
  await settled();
  await user.click(modelTrigger());
  await screen.findByRole("combobox", { name: "Model" });
  await screen.findByRole("listbox", { name: "Model" });

  // The refreshed list is held back until the test releases it, so the
  // picker's state while it loads can be seen.
  let release: () => void = () => {};
  const refreshed = new Promise<void>((resolve) => {
    release = resolve;
  });
  fake.on("model/list", async () => {
    await refreshed;
    return {
      data: [
        { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" },
        { provider: "google-vertex", model: "gemini-3.8-flash", displayName: "google-vertex/gemini-3.8-flash" },
      ],
    };
  });
  act(() => fake.emitNotification({ method: "evener/auth/updated", params: {} }));
  await act(async () => {});
  // Still showing the list it had while the refresh loads.
  expect(screen.queryByRole("listbox", { name: "Model" })).not.toBeNull();
  await act(async () => release());
  await screen.findByText("google-vertex/gemini-3.8-flash");
  expect(screen.getByRole("combobox", { name: "Model" })).toBeTruthy();
});

// A host switch is a new scope, not newer data: the picker drops the old
// host's list and loads the new host's from scratch, even though the switch
// also moves the instance list the in-place reload follows.
test("switching host under an open model picker reloads it from scratch", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  // The remote host's model list waits for the test, so the picker's state
  // while it loads can be seen.
  let release: () => void = () => {};
  const remoteList = new Promise<void>((resolve) => {
    release = resolve;
  });
  const fake = readyClient((f) => {
    f.on("evener/host/request", async (params) => {
      const forwarded = params as HostRequestParams;
      if (forwarded.method === "model/list") {
        await remoteList;
        return { data: [{ provider: "buildbox-llm", model: "local-7b", displayName: "buildbox-llm/local-7b" }] };
      }
      return routedDiscoveryDefault(forwarded.method);
    });
  });
  renderSpawn(fake);
  await settled();
  await user.click(modelTrigger());
  await screen.findByRole("listbox", { name: "Model" });

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await act(async () => {});
  expect(screen.queryByRole("listbox", { name: "Model" })).toBeNull();
  await act(async () => release());
  await screen.findByText("buildbox-llm/local-7b");
});
