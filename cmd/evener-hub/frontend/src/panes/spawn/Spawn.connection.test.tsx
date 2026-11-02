import { deferred } from "@evener/appwire-client/testing/deferred";
import "../session/testing/editorGeometry";
import type { HostRequestParams, ModelDescriptor, ModelListResponse, ThreadStartParams } from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import type userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import { connectionStore } from "../../stores/connection";
import { credentialsStore } from "../../stores/credentials";
import { hostsStore } from "../../stores/hosts";
import { ownClientId } from "../../stores/mutationClientIdentity";
import { setDraftField, spawnDraftsStore } from "./spawnDrafts";
import {
  completionDraft,
  expectWorkingDir,
  fillPrompt,
  installCanvasStubs,
  installSpawnTestHooks,
  modelListRequests,
  modelState,
  modelTrigger,
  modelValue,
  pastePngInto,
  pickModel,
  readyClient,
  renderSpawn,
  routedDiscoveryDefault,
  seedSources,
  settled,
  setupUser,
  setWorkingDir,
  startResponse,
  visitSpawnURL,
} from "./spawnTestUtils";

installSpawnTestHooks();

test("missing credentials surface setup in the composer without opening a dialog or losing its draft", async () => {
  const user = setupUser();
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  const connect = await screen.findByRole("button", { name: "Connect provider" });
  expect(screen.queryByRole("dialog")).toBeNull();
  await fillPrompt(user, "draft-sentinel");
  await setWorkingDir(user, "/tmp/my-project");
  expect((screen.getByRole("button", { name: "Start" }) as HTMLButtonElement).disabled).toBe(true);
  // userEvent owns its async act environment. Await it before opening the
  // separate act scope for lazy-import completion; nesting races their flags.
  await user.click(connect);
  await act(async () => {
    await vi.dynamicImportSettled();
  });
  expect(screen.getByRole("dialog")).toBeTruthy();
  expect(screen.queryByTestId("connect-dialog-load-failure")).toBeNull();
  await user.keyboard("{Escape}");
  expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLDivElement).textContent).toBe("draft-sentinel");
  expectWorkingDir("/tmp/my-project");
});

test("local model catalogue repair opens the existing connection dialog without saving credentials", async () => {
  const user = setupUser();
  const client = readyClient((fake) => {
    fake.on("model/list", () => {
      throw new WireError("catalog unavailable", -32000);
    });
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await settled();
  await fillPrompt(user, "repair-draft");
  await setWorkingDir(user, "/tmp/repair-project");

  await user.click(modelTrigger());
  await screen.findByRole("alert");
  await user.click(screen.getByRole("button", { name: "Connection settings" }));
  await act(async () => {
    await vi.dynamicImportSettled();
  });

  expect(screen.getByRole("dialog")).toBeTruthy();
  expect(screen.getByRole("textbox", { name: "Prompt" })).toHaveProperty("textContent", "repair-draft");
  expectWorkingDir("/tmp/repair-project");
  expect(screen.getByRole("dialog").contains(document.activeElement)).toBe(true);
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toEqual([]);
});

test("remote model catalogue recovery retries the remote read without offering local connection settings", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  let modelReads = 0;
  const modelRequests: HostRequestParams[] = [];
  const client = readyClient((fake) => {
    fake.on("evener/host/request", (params) => {
      const request = params as HostRequestParams;
      if (request.method === "model/list") {
        modelRequests.push(request);
        modelReads += 1;
        throw new WireError("remote catalog unavailable", -32000);
      }
      return routedDiscoveryDefault(request.method);
    });
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await settled();
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await settled();

  await user.click(modelTrigger());
  await screen.findByRole("alert");
  expect(screen.queryByRole("button", { name: "Connection settings" })).toBeNull();
  const beforeRetry = modelReads;
  await user.click(screen.getByRole("button", { name: "Retry models" }));
  await waitFor(() => expect(modelReads).toBeGreaterThan(beforeRetry));
  expect(modelRequests.at(-1)).toMatchObject({ host: "buildbox", params: { refresh: true } });
  expect(client.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toEqual([]);
});

const TEAM_PROVIDER_ROW = {
  name: "team-local",
  providerId: "team-local",
  protocol: "openai-chat",
  auth: "bearer",
  implicit: false,
  isDefault: false,
  activeSource: "none",
  hasStoredOAuth: false,
  credentialRequired: true,
  authModes: ["apiKey"],
  baseUrl: "https://team.example/v1",
  endpointFingerprint: "fp-team",
};

const TEAM_PROVIDER_OPTION = {
  id: TEAM_PROVIDER_ROW.providerId,
  name: "Team local",
  protocol: TEAM_PROVIDER_ROW.protocol,
  auth: TEAM_PROVIDER_ROW.auth,
  implicit: TEAM_PROVIDER_ROW.implicit,
  authModes: TEAM_PROVIDER_ROW.authModes,
  setup: TEAM_PROVIDER_ROW,
};

function teamInstanceList(saved: boolean) {
  return {
    instances: saved ? [{ ...TEAM_PROVIDER_ROW, activeSource: "store", hasStoredFile: true }] : [],
    availableProviders: [TEAM_PROVIDER_OPTION],
  };
}

// The connected instance's marked row is deliberately NOT the first row and
// not the global default: another provider leads, and the launch resolver names
// that other provider as the global default.
const TEAM_SAVED_MODELS: ModelDescriptor[] = [
  { provider: "other", model: "global-default", displayName: "other/global-default" },
  { provider: "team-local", model: "first", displayName: "team-local/first" },
  { provider: "team-local", model: "chosen", displayName: "team-local/chosen", instanceDefault: true },
];

async function openTeamProvider(user: ReturnType<typeof userEvent.setup>): Promise<void> {
  await user.click(await screen.findByRole("button", { name: "Connect provider" }));
  await act(async () => {
    await vi.dynamicImportSettled();
  });
  // team-local is not one of the curated providers the picker leads with.
  await user.click(await screen.findByText("Show all providers"));
  await user.click(await screen.findByRole("button", { name: "Team local" }));
  await user.type(screen.getByLabelText("API key"), "fixture-only-key");
}

async function saveAndCheckProvider(): Promise<void> {
  fireEvent.click(screen.getByRole("button", { name: "Save and test" }));
  await settled();
}

function savedApiKeyResult(provider: string) {
  return { provider, supported: true, signedIn: true, activeSource: "store", hasStoredOAuth: false };
}

test("automatic connection completion adopts the connected instance's marked default without a picker", async () => {
  installCanvasStubs();
  const user = setupUser();
  let saved = false;
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => teamInstanceList(saved));
    fake.on("model/list", () => ({
      data: saved ? TEAM_SAVED_MODELS : [{ provider: "anthropic", model: "claude-sonnet-4-5" }],
    }));
    fake.on("evener/auth/apiKey/set", ({ provider }) => {
      saved = true;
      return savedApiKeyResult(provider);
    });
    fake.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
    fake.on("evener/launch/resolve", () => ({
      effective: { model: "other/global-default" },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await settled();

  await fillPrompt(user, "auto-draft");
  await setWorkingDir(user, "/tmp/auto-project");
  const prompt = screen.getByRole("textbox", { name: "Prompt" });
  await act(async () => pastePngInto(prompt, "shot.png"));
  await waitFor(() => expect(screen.getByRole("button", { name: "View shot.png" })).toBeTruthy());
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.selectOptions(screen.getByLabelText("Access mode"), "read-only");

  await openTeamProvider(user);
  await saveAndCheckProvider();

  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(screen.queryByRole("listbox")).toBeNull();
  await waitFor(() => expect(screen.getByTestId("spawn-model-value").textContent).toBe("team-local/chosen"));
  // The setup flow owned focus when it closed, so the composer gets it back.
  await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("textbox", { name: "Prompt" })));
  expect(client.calls.filter((call) => call.method === "thread/start")).toEqual([]);
  expect(client.calls.filter((call) => call.method === "evener/instance/setDefault")).toEqual([]);

  // The originating draft is preserved: prompt, attachment, directory, source,
  // harness and access settings.
  const draft = completionDraft("/tmp/auto-project");
  expect(draft.fields.getState().prompt).toContain("auto-draft");
  expect(draft.fields.getState().source).toBe("local");
  expect(screen.getByRole("button", { name: "View shot.png" })).toBeTruthy();
  expectWorkingDir("/tmp/auto-project");
  expect((screen.getByLabelText("Access mode") as HTMLSelectElement).value).toBe("read-only");
  // Adoption is automatic, not an explicit choice: the revision still says
  // "untouched", so a later completion refresh can still protect it.
  expect(draft.fields.getState().modelChoiceRevision).toBe(0);
  // Fresh discovery for this exact scope is the only authority for the marker.
  const fresh = modelListRequests(client).filter((params) => params.refresh === true);
  expect(fresh.at(-1)).toMatchObject({ cwd: "/tmp/auto-project", refresh: true });

  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(client.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  const params = client.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params).toMatchObject({
    cwd: "/tmp/auto-project",
    model: "team-local/chosen",
    launchOverrides: { sandbox: "read-only" },
  });
});

const UNMARKED_MODEL_CASES: Array<{
  name: string;
  models: ModelDescriptor[];
  diagnostics?: ModelListResponse["diagnostics"];
}> = [
  { name: "no marker", models: TEAM_SAVED_MODELS.map(({ instanceDefault: _marker, ...model }) => model) },
  {
    name: "an ambiguous double marker",
    models: [
      ...TEAM_SAVED_MODELS,
      { provider: "team-local", model: "second", displayName: "team-local/second", instanceDefault: true },
    ],
  },
  {
    name: "a marker for another instance",
    models: [
      { provider: "other", model: "global-default", instanceDefault: true },
      { provider: "team-local", model: "first" },
      { provider: "team-local", model: "chosen" },
    ],
  },
  { name: "an empty catalogue", models: [] },
  {
    name: "an unsupported catalogue",
    models: [],
    diagnostics: [{ provider: "team-local", message: "fixture unsupported" }],
  },
];

test.each(UNMARKED_MODEL_CASES)(
  "automatic connection completion leaves inline recovery in charge for $name",
  async ({ models, diagnostics }) => {
    const user = setupUser();
    let saved = false;
    const client = readyClient((fake) => {
      fake.on("evener/instance/list", () => teamInstanceList(saved));
      fake.on("model/list", () => ({ data: saved ? models : [], diagnostics }));
      fake.on("evener/auth/apiKey/set", ({ provider }) => {
        saved = true;
        return savedApiKeyResult(provider);
      });
      fake.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
      fake.on("evener/launch/resolve", () => ({
        effective: { model: "other/global-default" },
        layers: {},
        provenance: {},
      }));
    });
    connectionStore.getState().connect(client);
    renderSpawn(client);
    await settled();
    await setWorkingDir(user, "/tmp/auto-inline");

    await openTeamProvider(user);
    await saveAndCheckProvider();

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(modelState()).toBe("required");
    expect(modelValue().textContent).not.toContain("team-local/chosen");
    expect(client.calls.filter((call) => call.method === "thread/start")).toEqual([]);
    expect(client.calls.filter((call) => call.method === "evener/instance/setDefault")).toEqual([]);
  },
);

test("automatic connection completion leaves a failed fresh catalogue as inline recovery", async () => {
  const user = setupUser();
  let saved = false;
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => teamInstanceList(saved));
    fake.on("model/list", (params) => {
      if (saved && params.refresh) throw new WireError("fixture refresh failure", -32000);
      return {
        data: saved
          ? [
              {
                provider: "team-local",
                model: "stale-display",
                displayName: "team-local/stale-display",
                // Marked as the instance default so an implementation that
                // adopted a cached marked row after a failed fresh read would
                // be caught here, not excused by the marker being absent.
                instanceDefault: true,
              },
            ]
          : [],
      };
    });
    fake.on("evener/auth/apiKey/set", ({ provider }) => {
      saved = true;
      return savedApiKeyResult(provider);
    });
    fake.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
    fake.on("evener/launch/resolve", () => ({ effective: {}, layers: {}, provenance: {} }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await settled();
  await setWorkingDir(user, "/tmp/auto-failed");

  await openTeamProvider(user);
  await saveAndCheckProvider();

  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(modelState()).toBe("required");
  expect(modelValue().textContent).not.toContain("team-local/chosen");
  // The marked cached row is display data only: a failed fresh discovery is
  // not evidence, so it must not be adopted automatically.
  expect(modelValue().textContent).not.toContain("stale-display");
  // Display rows survive the failed fresh read, but they are not evidence.
  await user.click(modelTrigger());
  expect(await screen.findByRole("option", { name: /stale-display/ })).toBeTruthy();
});

test("automatic connection completion with no marker can still reach a manual choice and Start", async () => {
  const user = setupUser();
  let saved = false;
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => teamInstanceList(saved));
    fake.on("model/list", () => ({
      data: saved ? TEAM_SAVED_MODELS.map(({ instanceDefault: _marker, ...model }) => model) : [],
    }));
    fake.on("evener/auth/apiKey/set", ({ provider }) => {
      saved = true;
      return savedApiKeyResult(provider);
    });
    fake.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
    fake.on("evener/launch/resolve", () => ({ effective: {}, layers: {}, provenance: {} }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await settled();
  await setWorkingDir(user, "/tmp/auto-manual");
  await fillPrompt(user, "manual-draft");

  await openTeamProvider(user);
  await saveAndCheckProvider();
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(modelState()).toBe("required");

  // The ordinary user-opened picker is intact.
  await user.click(modelTrigger());
  await user.click(await screen.findByRole("option", { name: /team-local\/chosen/ }));
  expect(modelValue().textContent).toBe("team-local/chosen");
  expect(completionDraft("/tmp/auto-manual").fields.getState().modelChoiceRevision).toBe(1);

  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(client.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  expect(client.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({
    model: "team-local/chosen",
  });
});

test("automatic connection completion preserves a saved model the fresh catalogue calls stale", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/auto-saved");
  localStorage.setItem(
    "evener-hub.spawn-defaults./tmp/auto-saved",
    JSON.stringify({ model: "openai/gpt-5", access_mode: "read-only" }),
  );
  localStorage.setItem("evener-hub.spawn-defaults.global.model", "openai/gpt-5");
  let saved = false;
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => teamInstanceList(saved));
    fake.on("model/list", () => ({
      data: saved
        ? [
            { provider: "openai", model: "gpt-5.5" },
            { provider: "other", model: "global-default" },
            { provider: "team-local", model: "chosen", instanceDefault: true },
          ]
        : [{ provider: "openai", model: "gpt-5" }],
    }));
    fake.on("evener/auth/apiKey/set", ({ provider }) => {
      saved = true;
      return savedApiKeyResult(provider);
    });
    fake.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
    fake.on("evener/launch/resolve", () => ({ effective: {}, layers: {}, provenance: {} }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await settled();
  expect(modelValue().textContent).toBe("openai/gpt-5");

  await openTeamProvider(user);
  await saveAndCheckProvider();
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

  // The explicit saved choice survives even though this fresh listing no
  // longer offers it, and the completion does not rewrite saved defaults.
  expect(modelValue().textContent).toBe("openai/gpt-5");
  expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
  expect(localStorage.getItem("evener-hub.spawn-defaults.global.model")).toBe("openai/gpt-5");
  expect(JSON.parse(localStorage.getItem("evener-hub.spawn-defaults./tmp/auto-saved") ?? "null")).toEqual({
    model: "openai/gpt-5",
    access_mode: "read-only",
  });
  expect(client.calls.filter((call) => call.method === "evener/instance/setDefault")).toEqual([]);

  // The successful credential write queues the store's own listing refresh for
  // 250ms later. Completion finishes before that timer in this fixture, so the
  // delivery that can erase the choice is exactly the one this drains - not a
  // later user action or an unrelated configuration change.
  const readsAtDismissal = client.calls.filter((call) => call.method === "evener/instance/list").length;
  await act(async () => {
    await vi.advanceTimersByTimeAsync(300);
  });
  await settled();
  // The completion's awaited reconciliation consumed the queued refresh: the
  // timer leaves no second, unowned listing update behind. Before the fix this
  // advanced clock produced another listing read and the assertions below
  // failed.
  expect(client.calls.filter((call) => call.method === "evener/instance/list").length).toBe(readsAtDismissal);

  // The acquisition's own delayed refresh must carry the completion's
  // ownership: both the draft's explicit unavailable choice and the two saved
  // defaults survive its settlement.
  expect(modelValue().textContent).toBe("openai/gpt-5");
  expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
  expect(localStorage.getItem("evener-hub.spawn-defaults.global.model")).toBe("openai/gpt-5");
  expect(JSON.parse(localStorage.getItem("evener-hub.spawn-defaults./tmp/auto-saved") ?? "null")).toEqual({
    model: "openai/gpt-5",
    access_mode: "read-only",
  });
});

test("automatic connection completion keeps a same-value chip choice as explicit intent", async () => {
  const user = setupUser();
  let saved = false;
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => teamInstanceList(saved));
    fake.on("model/list", () => ({
      data: saved
        ? TEAM_SAVED_MODELS
        : [{ provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" }],
    }));
    fake.on("evener/auth/apiKey/set", ({ provider }) => {
      saved = true;
      return savedApiKeyResult(provider);
    });
    fake.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
    fake.on("evener/launch/resolve", () => ({ effective: {}, layers: {}, provenance: {} }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await settled();
  await setWorkingDir(user, "/tmp/auto-same");

  // Choosing the same row twice still records two explicit choices.
  await pickModel(user, "claude-sonnet-4-5", "anthropic/claude-sonnet-4-5");
  await user.click(modelTrigger());
  const sameRow = await screen.findByRole("listbox");
  await user.click(within(sameRow).getByRole("option", { name: /claude-sonnet-4-5/ }));
  expect(completionDraft("/tmp/auto-same").fields.getState().modelChoiceRevision).toBe(2);

  await openTeamProvider(user);
  await saveAndCheckProvider();
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

  expect(modelValue().textContent).toBe("anthropic/claude-sonnet-4-5");
  expect(completionDraft("/tmp/auto-same").fields.getState().modelChoiceRevision).toBe(2);
});

test("automatic connection completion preserves an Advanced-options model choice", async () => {
  const user = setupUser();
  let saved = false;
  const client = readyClient((fake) => {
    fake.on("evener/launch/schema", () => ({
      options: [
        { field: "model", wireField: "model", label: "Model", group: "general", kind: "modelPicker", perLaunch: true },
      ],
    }));
    fake.on("evener/instance/list", () => teamInstanceList(saved));
    fake.on("model/list", () => ({
      data: saved ? TEAM_SAVED_MODELS : [{ provider: "openai", model: "gpt-5", displayName: "openai/gpt-5" }],
    }));
    fake.on("evener/auth/apiKey/set", ({ provider }) => {
      saved = true;
      return savedApiKeyResult(provider);
    });
    fake.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
    fake.on("evener/launch/resolve", () => ({ effective: {}, layers: {}, provenance: {} }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await settled();
  await setWorkingDir(user, "/tmp/auto-advanced");

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const pickers = screen.getAllByRole("button", { name: /change model/i });
  await user.click(pickers[pickers.length - 1]!);
  const combo = await screen.findByRole("combobox", { name: "Model" });
  await user.type(combo, "gpt-5");
  await user.click(await screen.findByText("openai/gpt-5"));
  expect(completionDraft("/tmp/auto-advanced").fields.getState().modelChoiceRevision).toBe(1);

  await openTeamProvider(user);
  await saveAndCheckProvider();
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

  expect(modelState()).not.toBe("chosen");
  const after = screen.getAllByRole("button", { name: /change model/i });
  expect(after[after.length - 1]!.textContent).toContain("openai/gpt-5");
  expect(screen.getByTestId("spawn-model-value").textContent).not.toBe("team-local/chosen");
});

test("automatic connection completion does not adopt over a /model choice made while the catalogue is pending", async () => {
  const user = setupUser();
  let saved = false;
  const pendingCatalog = deferred<ModelListResponse>();
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => teamInstanceList(saved));
    fake.on("model/list", (params) => {
      if (saved && params.refresh) return pendingCatalog.promise;
      return { data: saved ? TEAM_SAVED_MODELS : [{ provider: "team-local", model: "later" }] };
    });
    fake.on("evener/auth/apiKey/set", ({ provider }) => {
      saved = true;
      return savedApiKeyResult(provider);
    });
    fake.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
    fake.on("evener/launch/resolve", () => ({
      effective: { model: "other/global-default" },
      layers: {},
      provenance: {},
    }));
    fake.on("thread/start", () => startResponse("local:abc123"));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await settled();
  await setWorkingDir(user, "/tmp/auto-slash");

  await openTeamProvider(user);
  await saveAndCheckProvider();
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

  // Typed while the completion's fresh catalogue is still in flight.
  await fillPrompt(user, "/model team-local/later");
  await act(async () => pendingCatalog.resolve({ data: TEAM_SAVED_MODELS }));
  await settled();

  expect(screen.getByTestId("spawn-model-value").textContent).not.toBe("team-local/chosen");
  expect(modelState()).toBe("required");

  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(client.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  expect(client.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({
    modelProvider: "team-local",
    model: "later",
  });
});

test("automatic connection completion retires on a same-page credential change", async () => {
  const user = setupUser();
  let saved = false;
  const pendingCatalog = deferred<ModelListResponse>();
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => teamInstanceList(saved));
    fake.on("model/list", (params) => {
      if (saved && params.refresh) return pendingCatalog.promise;
      return { data: saved ? TEAM_SAVED_MODELS : [] };
    });
    fake.on("evener/auth/apiKey/set", ({ provider }) => {
      saved = true;
      return savedApiKeyResult(provider);
    });
    fake.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
    fake.on("evener/launch/resolve", () => ({
      effective: { model: "other/global-default" },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await settled();
  await fillPrompt(user, "own-echo");
  await setWorkingDir(user, "/tmp/own-echo");

  await openTeamProvider(user);
  await saveAndCheckProvider();
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

  // Another credential write from this same page while the completion's fresh
  // catalogue is in flight is still a credential generation change: page
  // identity is not operation identity, so it retires the completion.
  act(() =>
    client.emitNotification({
      method: "evener/auth/updated",
      params: { originClientId: ownClientId(), provider: "team-local", activeSource: "store" },
    }),
  );
  await act(async () => pendingCatalog.resolve({ data: TEAM_SAVED_MODELS }));
  await settled();

  expect(screen.getByTestId("spawn-model-value").textContent).not.toBe("team-local/chosen");
  expect(modelState()).toBe("required");
});

test("automatic connection completion still refuses a foreign credential generation", async () => {
  const user = setupUser();
  let saved = false;
  const pendingCatalog = deferred<ModelListResponse>();
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => teamInstanceList(saved));
    fake.on("model/list", (params) => {
      if (saved && params.refresh) return pendingCatalog.promise;
      return { data: saved ? TEAM_SAVED_MODELS : [] };
    });
    fake.on("evener/auth/apiKey/set", ({ provider }) => {
      saved = true;
      return savedApiKeyResult(provider);
    });
    fake.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
    fake.on("evener/launch/resolve", () => ({
      effective: { model: "other/global-default" },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await settled();
  await fillPrompt(user, "foreign-generation");
  await setWorkingDir(user, "/tmp/foreign-generation");

  await openTeamProvider(user);
  await saveAndCheckProvider();
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

  act(() =>
    client.emitNotification({
      method: "evener/auth/updated",
      params: { originClientId: "another-page", provider: "team-local", activeSource: "store" },
    }),
  );
  await act(async () => pendingCatalog.resolve({ data: TEAM_SAVED_MODELS }));
  await settled();

  expect(screen.getByTestId("spawn-model-value").textContent).not.toBe("team-local/chosen");
  expect(modelState()).toBe("required");
});

test("automatic connection completion accepts its own fresh catalogue's launch-model announcement", async () => {
  const user = setupUser();
  let saved = false;
  const pendingCatalog = deferred<ModelListResponse>();
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => teamInstanceList(saved));
    fake.on("model/list", (params) => {
      if (saved && params.refresh) return pendingCatalog.promise;
      return { data: saved ? TEAM_SAVED_MODELS : [] };
    });
    fake.on("evener/auth/apiKey/set", ({ provider }) => {
      saved = true;
      return savedApiKeyResult(provider);
    });
    fake.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
    fake.on("evener/launch/resolve", () => ({
      effective: { model: "other/global-default" },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await settled();
  await fillPrompt(user, "catalogue-announcement");
  await setWorkingDir(user, "/tmp/catalogue-announcement");

  await openTeamProvider(user);
  await saveAndCheckProvider();
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

  // launchModelsChanged broadcasts the catalog-only form: no provider/origin,
  // plus an explicit reason. It was caused by this completion's own fresh
  // listing, not a retired credential generation.
  act(() =>
    client.emitNotification({
      method: "evener/auth/updated",
      params: { reason: "models_changed" },
    }),
  );
  await act(async () => pendingCatalog.resolve({ data: TEAM_SAVED_MODELS }));
  await settled();

  expect(screen.getByTestId("spawn-model-value").textContent).toBe("team-local/chosen");
  expect(modelState()).toBe("chosen");
  expect(completionDraft("/tmp/catalogue-announcement").fields.getState().modelChoiceRevision).toBe(0);
});

test("automatic connection completion restores the composer after its own busy blur", async () => {
  const user = setupUser();
  let saved = false;
  const probe = deferred<{ provider: string; status: string; message: string }>();
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => teamInstanceList(saved));
    fake.on("model/list", () => ({ data: saved ? TEAM_SAVED_MODELS : [] }));
    fake.on("evener/auth/apiKey/set", ({ provider }) => {
      saved = true;
      return savedApiKeyResult(provider);
    });
    fake.on("evener/auth/test", () => probe.promise);
    fake.on("evener/launch/resolve", () => ({ effective: {}, layers: {}, provenance: {} }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await settled();
  await fillPrompt(user, "busy-blur");
  await setWorkingDir(user, "/tmp/busy-blur");

  await openTeamProvider(user);
  fireEvent.click(screen.getByRole("button", { name: "Save and test" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Testing…" })).toBeTruthy());
  // Disabling the submit blurs it to the document body. That internal busy
  // transition is not the person moving focus away from setup.
  await act(async () => {
    document.body.tabIndex = -1;
    document.body.focus();
  });
  expect(document.activeElement).toBe(document.body);

  await act(async () => probe.resolve({ provider: "team-local", status: "success", message: "" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("textbox", { name: "Prompt" })));
  expect(screen.getByTestId("spawn-model-value").textContent).toBe("team-local/chosen");
});

test("automatic connection completion preserves an explicit empty /model choice made while pending", async () => {
  const user = setupUser();
  let saved = false;
  const pendingCatalog = deferred<ModelListResponse>();
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => teamInstanceList(saved));
    fake.on("model/list", (params) => {
      if (saved && params.refresh) return pendingCatalog.promise;
      return { data: saved ? TEAM_SAVED_MODELS : [] };
    });
    fake.on("evener/auth/apiKey/set", ({ provider }) => {
      saved = true;
      return savedApiKeyResult(provider);
    });
    fake.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
    fake.on("evener/launch/resolve", () => ({ effective: {}, layers: {}, provenance: {} }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await settled();
  await setWorkingDir(user, "/tmp/auto-empty-slash");

  await openTeamProvider(user);
  await saveAndCheckProvider();
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

  // Bare /model means "the default", which is an explicit empty choice.
  await fillPrompt(user, "/model");
  await act(async () => pendingCatalog.resolve({ data: TEAM_SAVED_MODELS }));
  await settled();
  expect(screen.getByTestId("spawn-model-value").textContent).not.toContain("team-local/");

  // Inline recovery still works from here: choose a model manually and start.
  await user.click(modelTrigger());
  await user.click(await screen.findByRole("option", { name: /team-local\/chosen/ }));
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(client.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  expect(client.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({
    model: "team-local/chosen",
  });
});

test.each(["draft", "harness", "host"])(
  "automatic connection completion is retired by an A-to-B-to-A %s round trip",
  async (kind) => {
    const user = setupUser();
    window.history.pushState({}, "", "/new?dir=/tmp/auto-round-a");
    const probe = deferred<{ provider: string; status: string; message: string }>();
    let saved = false;
    const client = readyClient((fake) => {
      fake.on("evener/instance/list", () => teamInstanceList(saved));
      fake.on("model/list", () => ({ data: saved ? TEAM_SAVED_MODELS : [] }));
      fake.on("evener/auth/apiKey/set", ({ provider }) => {
        saved = true;
        return savedApiKeyResult(provider);
      });
      fake.on("evener/auth/test", () => probe.promise);
    });
    connectionStore.getState().connect(client);
    renderSpawn(client);
    await settled();
    await fillPrompt(user, "round-a");
    const origin = completionDraft("/tmp/auto-round-a");

    await openTeamProvider(user);
    fireEvent.click(screen.getByRole("button", { name: "Save and test" }));

    if (kind === "draft") {
      await visitSpawnURL("/new?dir=/tmp/auto-round-b");
      await act(async () => setDraftField(completionDraft("/tmp/auto-round-b"), "prompt", "round-b"));
      await visitSpawnURL("/new?dir=/tmp/auto-round-a");
    } else if (kind === "harness") {
      await act(async () => setDraftField(origin, "harness", "external"));
      await act(async () => setDraftField(origin, "harness", ""));
    } else {
      await act(async () => setDraftField(origin, "source", "buildbox"));
      await act(async () => setDraftField(origin, "source", "local"));
    }

    // The revoked check's dialog is dismissed with its ownership, rather than
    // left mounted over the draft the person navigated back to.
    expect(screen.queryByRole("dialog")).toBeNull();

    await act(async () => probe.resolve({ provider: "team-local", status: "success", message: "" }));
    await settled();

    // A -> B -> A cannot revive the old attempt: the draft keeps its prompt and
    // an empty model, and nothing launched.
    expect(origin.fields.getState().prompt).toBe("round-a");
    expect(origin.fields.getState().model).toBe("");
    expect(document.activeElement).not.toBe(screen.getByRole("textbox", { name: "Prompt" }));
    expect(spawnDraftsStore.getState().current).toBe(origin);
    expect(client.calls.filter((call) => call.method === "thread/start")).toEqual([]);
    expect(client.calls.filter((call) => call.method === "evener/instance/setDefault")).toEqual([]);
  },
);

test("navigation during a pending connection check dismisses its dialog and a fresh setup still completes", async () => {
  installCanvasStubs();
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/revoke-a");
  const probe = deferred<{ provider: string; status: string; message: string }>();
  let saved = false;
  let released = false;
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => teamInstanceList(saved));
    // No usable catalogue until the retired check settles, so the model
    // control still offers its connection recovery as a fresh setup entry.
    fake.on("model/list", () => ({ data: saved && released ? TEAM_SAVED_MODELS : [] }));
    fake.on("evener/auth/apiKey/set", ({ provider }) => {
      saved = true;
      return savedApiKeyResult(provider);
    });
    fake.on("evener/auth/test", () => probe.promise);
    fake.on("evener/launch/resolve", () => ({ effective: {}, layers: {}, provenance: {} }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await settled();
  await fillPrompt(user, "revoke-a");
  await setWorkingDir(user, "/tmp/revoke-a");
  const origin = completionDraft("/tmp/revoke-a");

  await openTeamProvider(user);
  fireEvent.click(screen.getByRole("button", { name: "Save and test" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Testing…" })).toBeTruthy());

  // The check is still pending: the person visits another draft and returns.
  await visitSpawnURL("/new?dir=/tmp/revoke-b");
  await act(async () => setDraftField(completionDraft("/tmp/revoke-b"), "prompt", "revoke-b"));
  await visitSpawnURL("/new?dir=/tmp/revoke-a");

  // The originating dialog is dismissed with the revoked ownership.
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

  // A replacement setup is a fresh attempt. The retired check's late success
  // must neither close it nor write the origin draft.
  await user.click(modelTrigger());
  await user.click(await screen.findByRole("button", { name: "Connection settings" }));
  await act(async () => {
    await vi.dynamicImportSettled();
  });
  await user.click(await screen.findByText("Show all providers"));
  await user.click(await screen.findByRole("button", { name: "Team local" }));
  expect(screen.getByRole("dialog")).toBeTruthy();
  released = true;
  await act(async () => probe.resolve({ provider: "team-local", status: "success", message: "" }));
  await settled();
  expect(screen.getByRole("dialog")).toBeTruthy();
  expect(origin.fields.getState().prompt).toBe("revoke-a");
  expect(origin.fields.getState().model).toBe("");
  expect(client.calls.filter((call) => call.method === "thread/start")).toEqual([]);
  expect(client.calls.filter((call) => call.method === "evener/instance/setDefault")).toEqual([]);

  // The fresh dialog completes through the real connector.
  const freshSubmit = screen.getByRole("dialog").querySelector('button[type="submit"]');
  if (!freshSubmit) throw new Error("fresh setup has no submit control");
  fireEvent.click(freshSubmit);
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  await waitFor(() => expect(screen.getByTestId("spawn-model-value").textContent).toBe("team-local/chosen"));
  expect(completionDraft("/tmp/revoke-a").fields.getState().modelChoiceRevision).toBe(0);
});

test("automatic connection completion is retired by a pane remount", async () => {
  const user = setupUser();
  const pendingCatalog = deferred<ModelListResponse>();
  let saved = false;
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => teamInstanceList(saved));
    fake.on("model/list", (params) => {
      if (saved && params.refresh) return pendingCatalog.promise;
      return { data: saved ? TEAM_SAVED_MODELS : [] };
    });
    fake.on("evener/auth/apiKey/set", ({ provider }) => {
      saved = true;
      return savedApiKeyResult(provider);
    });
    fake.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  });
  connectionStore.getState().connect(client);
  const mounted = renderSpawn(client);
  await settled();
  await setWorkingDir(user, "/tmp/auto-remount");

  await openTeamProvider(user);
  await saveAndCheckProvider();
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

  mounted.unmount();
  renderSpawn(client);
  await settled();
  await act(async () => pendingCatalog.resolve({ data: TEAM_SAVED_MODELS }));
  await settled();

  const draft = completionDraft("/tmp/auto-remount");
  expect(draft.fields.getState().model).toBe("");
  expect(modelState()).toBe("required");
  expect(client.calls.filter((call) => call.method === "thread/start")).toEqual([]);
});

test("automatic connection completion drops a late answer from a retired credential generation", async () => {
  const user = setupUser();
  const pendingCatalog = deferred<ModelListResponse>();
  let saved = false;
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => teamInstanceList(saved));
    fake.on("model/list", (params) => {
      if (saved && params.refresh) return pendingCatalog.promise;
      return { data: saved ? TEAM_SAVED_MODELS : [] };
    });
    fake.on("evener/auth/apiKey/set", ({ provider }) => {
      saved = true;
      return savedApiKeyResult(provider);
    });
    fake.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await settled();
  await setWorkingDir(user, "/tmp/auto-generation");

  await openTeamProvider(user);
  await saveAndCheckProvider();
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

  // The credentials changed while the completion's read was in flight.
  act(() => client.emitNotification({ method: "evener/auth/updated", params: {} }));
  await act(async () => pendingCatalog.resolve({ data: TEAM_SAVED_MODELS }));
  await settled();

  const draft = completionDraft("/tmp/auto-generation");
  expect(draft.fields.getState().model).toBe("");
  expect(modelState()).toBe("required");
  expect(client.calls.filter((call) => call.method === "evener/instance/setDefault")).toEqual([]);
});

test("automatic connection completion preserves a chip choice made while the instance refresh is pending", async () => {
  const user = setupUser();
  let saved = false;
  let released = false;
  let holdInstanceRead = false;
  const pendingInstances = deferred<ReturnType<typeof teamInstanceList>>();
  const postCatalog: ModelDescriptor[] = [
    { provider: "openai", model: "gpt-5.5", displayName: "openai/gpt-5.5" },
    { provider: "other", model: "global-default", displayName: "other/global-default" },
    { provider: "team-local", model: "chosen", displayName: "team-local/chosen", instanceDefault: true },
  ];
  // Another project's saved model is also stale against the refreshed catalog;
  // the completion's owned read must not sweep it away.
  localStorage.setItem("evener-hub.spawn-defaults./tmp/untouched-project", JSON.stringify({ model: "openai/gpt-5" }));
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => {
      if (saved && holdInstanceRead) {
        holdInstanceRead = false;
        return pendingInstances.promise;
      }
      return teamInstanceList(saved);
    });
    fake.on("model/list", () => ({
      data: saved && released ? postCatalog : [{ provider: "openai", model: "gpt-5", displayName: "openai/gpt-5" }],
    }));
    fake.on("evener/auth/apiKey/set", ({ provider }) => {
      saved = true;
      return savedApiKeyResult(provider);
    });
    fake.on("evener/auth/test", ({ provider }) => {
      // The connector's own listing read already happened; the next instance
      // read is the spawn retry the completion awaits.
      holdInstanceRead = true;
      return { provider, status: "success", message: "" };
    });
    fake.on("evener/launch/resolve", () => ({
      effective: { model: "other/global-default" },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await settled();
  await setWorkingDir(user, "/tmp/auto-late-choice");

  await openTeamProvider(user);
  await saveAndCheckProvider();
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

  // The completion is waiting on the held instance read. The person explicitly
  // chooses an available model in the meantime.
  await user.click(modelTrigger());
  await user.click(await screen.findByRole("option", { name: /openai\/gpt-5/ }));
  expect(completionDraft("/tmp/auto-late-choice").fields.getState().modelChoiceRevision).toBe(1);
  expect(modelValue().textContent).toBe("openai/gpt-5");

  // Release the instance read: the refreshed catalog omits the choice.
  await act(async () => {
    released = true;
    pendingInstances.resolve(teamInstanceList(true));
  });
  await settled();

  await waitFor(() => expect(modelValue().textContent).toBe("openai/gpt-5"));
  expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
  expect(JSON.parse(localStorage.getItem("evener-hub.spawn-defaults./tmp/untouched-project") ?? "null")).toEqual({
    model: "openai/gpt-5",
  });
  expect(client.calls.filter((call) => call.method === "evener/instance/setDefault")).toEqual([]);
});

test("automatic connection completion does not shield a same-host draft from its own stale-model validation", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/protect-a");
  localStorage.setItem("evener-hub.spawn-defaults./tmp/protect-b", JSON.stringify({ model: "openai/retired" }));
  let saved = false;
  const instancesBefore = teamInstanceList(false);
  const instancesAfter = teamInstanceList(true);
  const postCatalog: ModelDescriptor[] = [
    { provider: "openai", model: "gpt-5.5", displayName: "openai/gpt-5.5" },
    ...TEAM_SAVED_MODELS,
  ];
  const client = readyClient((fake) => {
    // Stable instance identity after the connection: navigation alone must not
    // manufacture a fresh global request that masks the ownership question.
    fake.on("evener/instance/list", () => (saved ? instancesAfter : instancesBefore));
    fake.on("model/list", () => ({
      data: saved ? postCatalog : [{ provider: "openai", model: "retired", displayName: "openai/retired" }],
    }));
    fake.on("evener/auth/apiKey/set", ({ provider }) => {
      saved = true;
      return savedApiKeyResult(provider);
    });
    fake.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
    fake.on("evener/launch/resolve", () => ({ effective: {}, layers: {}, provenance: {} }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await settled();
  // Draft B already exists with its saved model before A's completion runs, so
  // the question is whether B's own live draft is validated, not whether the
  // completion's refresh already swept B's storage.
  await visitSpawnURL("/new?dir=/tmp/protect-b");
  await settled();
  expect(completionDraft("/tmp/protect-b").fields.getState().model).toBe("openai/retired");
  await visitSpawnURL("/new?dir=/tmp/protect-a");
  await settled();

  await openTeamProvider(user);
  await saveAndCheckProvider();
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  await waitFor(() => expect(modelValue().textContent).toBe("team-local/chosen"));
  // Let every owner's follow-up store update and its global request land while
  // A is still the live draft.
  await settled();
  await settled();

  // Draft B is on the same host; A's completion owns no validation for it.
  await visitSpawnURL("/new?dir=/tmp/protect-b");
  await settled();
  await waitFor(() => expect(completionDraft("/tmp/protect-b").fields.getState().model).toBe(""));
  expect(screen.getByText(/discarded last-used model openai\/retired/i)).toBeTruthy();
});

test("an ordinary refresh after an automatic completion still validates the model", async () => {
  const user = setupUser();
  let saved = false;
  let stale = false;
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => teamInstanceList(saved));
    fake.on("model/list", () => ({
      data: stale
        ? [{ provider: "team-local", model: "other-model", displayName: "team-local/other-model" }]
        : saved
          ? TEAM_SAVED_MODELS
          : [],
    }));
    fake.on("evener/auth/apiKey/set", ({ provider }) => {
      saved = true;
      return savedApiKeyResult(provider);
    });
    fake.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
    fake.on("evener/launch/resolve", () => ({ effective: {}, layers: {}, provenance: {} }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await settled();
  await setWorkingDir(user, "/tmp/auto-after-refresh");

  await openTeamProvider(user);
  await saveAndCheckProvider();
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  await waitFor(() => expect(modelValue().textContent).toBe("team-local/chosen"));

  // The completion's owned work is over. A later ordinary provider refresh
  // validates this draft exactly as it always did.
  stale = true;
  await act(async () => credentialsStore.getState().fetch());
  await settled();

  await waitFor(() => expect(completionDraft("/tmp/auto-after-refresh").fields.getState().model).toBe(""));
  expect(screen.getByText(/discarded last-used model team-local\/chosen/i)).toBeTruthy();
});

test("retrying missing provider setup discovers a local server started afterward", async () => {
  const user = setupUser();
  let available = false;
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => ({
      instances: [
        {
          name: "ollama",
          providerId: "ollama",
          protocol: "openai-chat",
          auth: "none",
          implicit: true,
          isDefault: true,
          activeSource: "none",
          hasStoredOAuth: false,
          credentialRequired: false,
        },
      ],
      availableProviders: [],
    }));
    fake.on("model/list", () => ({ data: available ? [{ provider: "ollama", model: "local-model" }] : [] }));
    fake.on("evener/auth/test", () => ({ provider: "ollama", status: "success", message: "" }));
    fake.on("evener/launch/resolve", () => ({
      effective: { model: "ollama/local-model" },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await screen.findByRole("button", { name: "Connect provider" });
  const retry = screen.getByRole("button", { name: "Retry provider check" });
  available = true;
  await user.click(retry);
  await waitFor(() => expect(screen.queryByRole("button", { name: "Connect provider" })).toBeNull());
  await user.click(modelTrigger());
  expect(await screen.findByRole("option", { name: /local-model/ })).toBeTruthy();
});

// #2227: the spawn form's retry is the other entry point into retryHostRead.
// After the registry's own read failed, the retry re-reads the registry, and the
// host's listing follows from the recovery effect in useHostInstances - exactly
// once, never twice.
test("a spawn retry that recovers the registry reads the selected host's listing exactly once", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  let registryDown = true;
  const fake = readyClient((f) => {
    f.on("evener/host/list", () => {
      if (registryDown) throw new WireError("registry unavailable", -32000);
      return {
        hosts: [
          {
            name: "buildbox",
            generation: 1,
            incarnationId: "inc-1",
            origin: "hub.toml",
            attached: true,
            midAttach: false,
            removed: false,
          },
        ],
      };
    });
    f.on("evener/host/request", (params) => routedDiscoveryDefault((params as HostRequestParams).method));
  });
  // The registry's own read failed (a settings visit, say): the spawn form's
  // provider check reads that failure and offers the registry's read to retry.
  hostsStore.setState({ load: { phase: "error", message: "registry unavailable" } });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await settled();

  // The read is held while the registry has failed: nothing went out yet.
  const listingReads = () =>
    fake.calls.filter(
      (call) =>
        call.method === "evener/host/request" && (call.params as HostRequestParams).method === "evener/instance/list",
    );
  expect(listingReads()).toHaveLength(0);

  registryDown = false;
  await user.click(screen.getByRole("button", { name: "Retry provider check" }));
  await settled();

  // Exactly one read across the one recovery: not two, and not zero.
  expect(listingReads()).toHaveLength(1);
});

// The host picker reads the navigation manifest, and the host registry behind
// the launch-config store is only re-read by Settings. A registry snapshot that
// predates a host added from another client must not turn that host's path and
// resolve reads into refusals.
test("a selected host the cached registry predates is still resolved on that host", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const hostRow = (name: string) => ({
    name,
    generation: 1,
    incarnationId: `inc-${name}`,
    origin: "hub.toml" as const,
    attached: true,
    midAttach: false,
    removed: false,
  });
  const fake = readyClient((f) => {
    f.on("evener/host/list", () => ({ hosts: [hostRow("other"), hostRow("buildbox")] }));
    f.on("evener/host/request", (params) => routedDiscoveryDefault((params as HostRequestParams).method));
  });
  hostsStore.setState({ load: { phase: "ready", hosts: [hostRow("other")] } });
  renderSpawn(fake);
  await settled();
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await settled();
  await setWorkingDir(user, "/srv/app");
  await settled();

  await waitFor(() =>
    expect(
      fake.calls.some(
        (call) =>
          call.method === "evener/host/request" &&
          (call.params as HostRequestParams).host === "buildbox" &&
          (call.params as HostRequestParams).method === "evener/launch/resolve",
      ),
    ).toBe(true),
  );
});

test("successful keyless testing refreshes availability without an auth notification", async () => {
  const user = setupUser();
  let available = false;
  const keyless = {
    name: "ollama",
    providerId: "ollama",
    protocol: "openai-chat",
    auth: "none",
    implicit: true,
    isDefault: true,
    activeSource: "none",
    hasStoredOAuth: false,
    credentialRequired: false,
    baseUrl: "http://localhost:11434/v1",
    endpointFingerprint: "fp-ollama",
  };
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => ({
      instances: [keyless],
      availableProviders: [
        {
          id: "ollama",
          name: "Local endpoint",
          protocol: "openai-chat",
          auth: "none",
          implicit: true,
          authModes: [],
          setup: keyless,
        },
      ],
    }));
    fake.on("model/list", () => ({ data: available ? [{ provider: "ollama", model: "local-model" }] : [] }));
    fake.on("evener/auth/test", () => ({ provider: "ollama", status: "success", message: "" }));
    fake.on("evener/launch/resolve", () => ({
      effective: { model: "ollama/local-model" },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  const connectProvider = await screen.findByRole("button", { name: "Connect provider" });
  // Finish the lazy dialog's mount and catalog refresh before retaining a button
  // reference: the refresh replaces the initially cached instance rows.
  await user.click(connectProvider);
  await act(async () => {
    await vi.dynamicImportSettled();
  });
  await user.click(await screen.findByText("Show all providers"));
  await user.click(screen.getByRole("button", { name: "Local endpoint" }));
  const testConnection = await screen.findByRole("button", { name: "Test connection" });
  available = true;
  fireEvent.click(testConnection);
  await settled();
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toEqual([
    { method: "evener/auth/test", params: { provider: "ollama", expectedEndpointFingerprint: "fp-ollama" } },
  ]);
  await waitFor(() => {
    expect(screen.queryByRole("button", { name: "Connect provider" })).toBeNull();
  });
  // The successful check returns to the draft without opening a picker; the
  // refreshed catalogue is available from the ordinary model control.
  expect(screen.queryByRole("listbox")).toBeNull();
  await user.click(modelTrigger());
  expect(await screen.findByRole("option", { name: /local-model/ })).toBeTruthy();
  await settled();
});

test("credential changes reload the cached model catalog and re-enter setup after removal", async () => {
  const user = setupUser();
  let configured = false;
  let modelRequests = 0;
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => ({
      instances: configured
        ? [
            {
              name: "work",
              providerId: "openai",
              protocol: "openai-responses",
              auth: "bearer",
              implicit: false,
              isDefault: true,
              activeSource: "store",
              hasStoredOAuth: false,
              credentialRequired: true,
            },
          ]
        : [],
      availableProviders: [],
    }));
    fake.on("model/list", () => {
      modelRequests++;
      return { data: configured ? [{ provider: "work", model: "test-model" }] : [] };
    });
    fake.on("evener/launch/resolve", () => ({ effective: { model: "work/test-model" }, layers: {}, provenance: {} }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await screen.findByRole("button", { name: "Connect provider" });
  await setWorkingDir(user, "/tmp/my-project");
  await fillPrompt(user, "draft-sentinel");
  const requestsBefore = modelRequests;
  configured = true;
  await act(async () => credentialsStore.getState().fetch());
  await waitFor(() => expect(modelRequests).toBeGreaterThan(requestsBefore));
  expect(screen.queryByRole("button", { name: "Connect provider" })).toBeNull();
  expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLDivElement).textContent).toBe("draft-sentinel");
  configured = false;
  await act(async () => credentialsStore.getState().fetch());
  await screen.findByRole("button", { name: "Connect provider" });
});
