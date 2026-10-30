import type {
  LaunchConfigResolved,
  ModelListResponse,
  ThreadStartParams,
  ThreadStartResponse,
} from "@evener/appwire-client";
import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { expect, test } from "vitest";
import { ClientProvider } from "../../shell/clientContext";
import { connectionStore } from "../../stores/connection";
import { credentialsStore } from "../../stores/credentials";
import { Toast } from "../../widgets";
import { selectEditorText } from "../session/testing/editor";
import Spawn from "./Spawn";
import {
  completionDraft,
  completionNavigate,
  deferred,
  effortControl,
  effortOptionValues,
  expectWorkingDir,
  fillPrompt,
  installCanvasStubs,
  installSpawnTestHooks,
  LAST_WORKING_DIR_KEY,
  modelListRequests,
  modelState,
  modelTrigger,
  modelValue,
  pastePngInto,
  pickModel,
  promptField,
  readyClient,
  renderSpawn,
  settled,
  setupUser,
  setWorkingDir,
  slashOptions,
  startResponse,
  visitSpawnURL,
} from "./spawnTestUtils";

test("draft survives unmount and bare /new return before any successful start", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/draft-a");
  const client = readyClient();
  const mounted = renderSpawn(client);
  await settled();
  await fillPrompt(user, "draft-a-sentinel");
  fireEvent.change(effortControl(), { target: { value: "high" } });
  mounted.unmount();
  await visitSpawnURL("/settings");
  await visitSpawnURL("/new");
  renderSpawn(client);
  await settled();
  expectWorkingDir("/tmp/draft-a");
  expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLDivElement).textContent).toBe("draft-a-sentinel");
  expect((effortControl() as HTMLSelectElement).value).toBe("high");
  expect(localStorage.getItem(LAST_WORKING_DIR_KEY)).toBeNull();
});

test("project navigation isolates drafts and ignores non-new URL prefill", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/draft-a");
  renderSpawn(readyClient());
  await settled();
  await fillPrompt(user, "draft-a-sentinel");
  fireEvent.change(effortControl(), { target: { value: "high" } });
  await visitSpawnURL("/settings?dir=/tmp/foreign&prompt=foreign");
  expectWorkingDir("/tmp/draft-a");
  expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLDivElement).textContent).toBe("draft-a-sentinel");
  await visitSpawnURL("/new?dir=/tmp/draft-b");
  expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLDivElement).textContent).toBe("");
  await fillPrompt(user, "draft-b-sentinel");
  await visitSpawnURL("/new?dir=/tmp/draft-a");
  expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLDivElement).textContent).toBe("draft-a-sentinel");
  expect((effortControl() as HTMLSelectElement).value).toBe("high");
});

test("re-entering the same /new URL after leaving /new re-applies its explicit prefill", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/reentry-a");
  const client = readyClient();
  const mounted = renderSpawn(client);
  await settled();
  await fillPrompt(user, "sentinel-a");
  // The picker switches drafts without touching the URL, so the prefill
  // marker still names this same URL.
  await setWorkingDir(user, "/tmp/reentry-b");
  await fillPrompt(user, "sentinel-b");
  expectWorkingDir("/tmp/reentry-b");
  expect(window.location.search).toBe("?dir=/tmp/reentry-a");
  // Leaving /new can unmount the pane (mobile StackHost mounts only the
  // active route). Returning to the identical explicit URL is a fresh
  // request: it selects draft A again, and B's draft persists in the map.
  mounted.unmount();
  await visitSpawnURL("/settings");
  await visitSpawnURL("/new?dir=/tmp/reentry-a");
  renderSpawn(client);
  await settled();
  expectWorkingDir("/tmp/reentry-a");
  expect(promptField().textContent).toBe("sentinel-a");
  expect(completionDraft("/tmp/reentry-b").fields.getState().prompt).toBe("sentinel-b");
});

test("remounting the same /new URL without leaving /new keeps the picker-selected draft", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/reentry-a");
  const client = readyClient();
  const mounted = renderSpawn(client);
  await settled();
  await setWorkingDir(user, "/tmp/reentry-b");
  await fillPrompt(user, "sentinel-b");
  expectWorkingDir("/tmp/reentry-b");
  // A remount with no intervening departure must not clobber the current
  // draft back to the URL's prefill directory.
  mounted.unmount();
  renderSpawn(client);
  await settled();
  expectWorkingDir("/tmp/reentry-b");
  expect(promptField().textContent).toBe("sentinel-b");
});

// Removing the launch's view-ownership check must break these, even when
// picker changes leave the URL unchanged or the user returns to the same view.
test.each(["unchanged", "picker", "picker return", "away", "route return", "remount", "focus return"])(
  "completion ownership: %s while thread/start is pending",
  async (scenario) => {
    const user = setupUser();
    window.history.pushState({}, "", "/new?dir=/tmp/completion-a");
    const started = deferred<ThreadStartResponse>();
    const fake = readyClient((f) => f.on("thread/start", () => started.promise));
    const mounted = renderSpawn(fake);
    await fillPrompt(user, "submitted-a");
    await user.click(screen.getByTestId("spawn-submit"));
    await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
    const origin = completionDraft("/tmp/completion-a");
    if (scenario.startsWith("picker")) {
      await setWorkingDir(user, "/tmp/completion-b");
      await fillPrompt(user, "unsent-b");
      expect(window.location.search).toBe("?dir=/tmp/completion-a");
      if (scenario === "picker return") await setWorkingDir(user, "/tmp/completion-a");
    } else if (scenario === "away" || scenario === "route return") {
      await completionNavigate("/settings");
      if (scenario === "route return") await completionNavigate("/new?dir=/tmp/completion-a");
    } else if (scenario === "remount") {
      mounted.unmount();
      await completionNavigate("/settings");
      await completionNavigate("/new?dir=/tmp/completion-a");
      renderSpawn(fake);
    } else if (scenario === "focus return") {
      for (const focused of [false, true]) {
        mounted.rerender(
          <ClientProvider client={fake}>
            <Spawn params={{}} paneId="spawn-1" focused={focused} />
            <Toast />
          </ClientProvider>,
        );
      }
    }
    await act(async () => started.resolve(startResponse("local:completion-a")));
    await waitFor(() => expect(origin.fields.getState().busy).toBe(false));
    expect(origin.fields.getState().prompt).toBe("");
    expect(origin.fields.getState().busyStartedAt).toBeNull();
    expect(origin.busyRef.current).toBe(false);
    if (scenario.startsWith("picker"))
      expect(completionDraft("/tmp/completion-b").fields.getState().prompt).toBe("unsent-b");
    expect(window.location.pathname).toBe(
      scenario === "unchanged" ? "/s/local%3Acompletion-a" : scenario === "away" ? "/settings" : "/new",
    );
  },
);

test.each(["A first", "B first"])("completion ownership: concurrent launches finish %s", async (order) => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/completion-a");
  const startA = deferred<ThreadStartResponse>();
  const startB = deferred<ThreadStartResponse>();
  const fake = readyClient((f) =>
    f.on("thread/start", ({ cwd }) => (cwd === "/tmp/completion-a" ? startA.promise : startB.promise)),
  );
  renderSpawn(fake);
  await fillPrompt(user, "submitted-a");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  await setWorkingDir(user, "/tmp/completion-b");
  await fillPrompt(user, "submitted-b");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(2));
  expect(fake.calls.filter((call) => call.method === "thread/start").map((call) => call.params)).toMatchObject([
    { cwd: "/tmp/completion-a", input: [{ type: "text", text: "submitted-a" }] },
    { cwd: "/tmp/completion-b", input: [{ type: "text", text: "submitted-b" }] },
  ]);
  if (order === "A first") {
    await act(async () => startA.resolve(startResponse("local:completion-a")));
    expect(window.location.pathname).toBe("/new");
    expect(promptField().textContent).toBe("submitted-b");
    await act(async () => startB.resolve(startResponse("local:completion-b")));
  } else {
    await act(async () => startB.resolve(startResponse("local:completion-b")));
    expect(window.location.pathname).toBe("/s/local%3Acompletion-b");
    await act(async () => startA.resolve(startResponse("local:completion-a")));
  }
  expect(window.location.pathname).toBe("/s/local%3Acompletion-b");
  for (const cwd of ["/tmp/completion-a", "/tmp/completion-b"]) {
    expect(completionDraft(cwd).fields.getState()).toMatchObject({ prompt: "", busy: false, busyStartedAt: null });
  }
});

// Stamping only in doSpawn would incorrectly regain authority after these awaits.
test.each(["preflight", "create confirmation"])(
  "completion ownership: departure during %s cannot reacquire navigation on return",
  async (boundary) => {
    const user = setupUser();
    window.history.pushState({}, "", "/new?dir=/tmp/completion-a");
    const validation = deferred<{ path: string; valid: boolean }>();
    const creation = deferred<{ path: string; created: boolean }>();
    const fake = readyClient((f) => {
      f.on("evener/path/validate", () =>
        boundary === "preflight" ? validation.promise : { path: "/tmp/completion-a", valid: false },
      );
      f.on("evener/dirs/create", () => creation.promise);
    });
    renderSpawn(fake);
    await fillPrompt(user, "submitted-a");
    await user.click(screen.getByTestId("spawn-submit"));
    if (boundary === "create confirmation")
      await user.click(await screen.findByRole("button", { name: "Create & start" }));
    await waitFor(() =>
      expect(
        fake.calls.some(
          (call) => call.method === (boundary === "preflight" ? "evener/path/validate" : "evener/dirs/create"),
        ),
      ).toBe(true),
    );
    await completionNavigate("/settings");
    await completionNavigate("/new?dir=/tmp/completion-a");
    await act(async () => {
      if (boundary === "preflight") validation.resolve({ path: "/tmp/completion-a", valid: true });
      else creation.resolve({ path: "/tmp/completion-a", created: true });
    });
    await waitFor(() => expect(completionDraft("/tmp/completion-a").fields.getState().busy).toBe(false));
    expect(fake.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({
      cwd: "/tmp/completion-a",
      input: [{ type: "text", text: "submitted-a" }],
    });
    expect(promptField().textContent).toBe("");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(window.location.pathname).toBe("/new");
  },
);

test("completion ownership: late missing-directory preflight cannot open a dialog over settings", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/completion-a");
  const validation = deferred<{ path: string; valid: boolean }>();
  const fake = readyClient((f) => f.on("evener/path/validate", () => validation.promise));
  renderSpawn(fake);
  await fillPrompt(user, "submitted-a");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "evener/path/validate")).toBe(true));
  await completionNavigate("/settings");
  await act(async () => validation.resolve({ path: "/tmp/completion-a", valid: false }));
  expect(completionDraft("/tmp/completion-a").fields.getState()).toMatchObject({
    busy: false,
    createDialogPath: "/tmp/completion-a",
    prompt: "submitted-a",
  });
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(window.location.pathname).toBe("/settings");
  await completionNavigate("/new");
  await user.click(await screen.findByRole("button", { name: "Create & start" }));
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
});

test("completion ownership: a query-only navigation retires a pending launch's claim to the view", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/completion-a");
  const started = deferred<ThreadStartResponse>();
  const fake = readyClient((f) => f.on("thread/start", () => started.promise));
  renderSpawn(fake);
  await fillPrompt(user, "submitted-a");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  // Same draft, same pathname: only the query changes. The user's newest
  // navigation still wins over the older in-flight launch.
  await completionNavigate("/new?dir=/tmp/completion-a&prompt=updated");
  await act(async () => started.resolve(startResponse("local:completion-a")));
  await waitFor(() => expect(completionDraft("/tmp/completion-a").fields.getState().busy).toBe(false));
  expect(window.location.pathname).toBe("/new");
  expect(window.location.search).toBe("?dir=/tmp/completion-a&prompt=updated");
});

test("completion menu ownership: a remounted prompt's cleared snapshot retires its menu", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/completion-a");
  const started = deferred<ThreadStartResponse>();
  const fake = readyClient((f) => {
    f.on("thread/start", () => started.promise);
    f.on("evener/spawn/slashCatalog", () => ({ commands: [{ name: "review", description: "Review" }], skills: [] }));
  });
  const mounted = renderSpawn(fake, true, { connectStore: false });
  await user.type(promptField(), "/re");
  await screen.findByTestId("composer-slash-menu");
  await user.keyboard("{Meta>}{Enter}{/Meta}");
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  mounted.unmount();
  renderSpawn(fake, true, { connectStore: false });
  await screen.findByTestId("composer-slash-menu");
  await act(async () => started.resolve(startResponse("local:completion-a")));
  expect(promptField().textContent).toBe("");
  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
  expect(window.location.pathname).toBe("/new");
});

test.each(["unchanged", "newer prompt", "other draft"])("completion menu ownership: %s", async (scenario) => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/completion-a");
  const started = deferred<ThreadStartResponse>();
  const fake = readyClient((f) => {
    f.on("thread/start", () => started.promise);
    f.on("evener/spawn/slashCatalog", () => ({ commands: [{ name: "review", description: "Review" }], skills: [] }));
  });
  renderSpawn(fake);
  await user.type(promptField(), "/re");
  await screen.findByTestId("composer-slash-menu");
  await user.keyboard("{Meta>}{Enter}{/Meta}");
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  if (scenario === "other draft") await setWorkingDir(user, "/tmp/completion-b");
  if (scenario !== "unchanged") {
    await user.clear(promptField());
    await user.type(promptField(), "/rev");
    await screen.findByTestId("composer-slash-menu");
  }
  await act(async () => started.resolve(startResponse("local:completion-a")));
  await waitFor(() => expect(completionDraft("/tmp/completion-a").fields.getState().busy).toBe(false));
  expect(promptField().textContent).toBe(scenario === "unchanged" ? "" : "/rev");
  if (scenario === "unchanged") expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
  else {
    expect(screen.queryByTestId("composer-slash-menu")).not.toBeNull();
    expect(document.activeElement).toBe(promptField());
    expect(slashOptions().map((option) => option.textContent)).toContainEqual(expect.stringContaining("/review"));
  }
});

test("entering an in-memory draft validates its model after another draft swept its saved default", async () => {
  window.history.pushState({}, "", "/new?dir=/tmp/lifecycle-a");
  localStorage.setItem("evener-hub.spawn-defaults./tmp/lifecycle-a", JSON.stringify({ model: "openai/gpt-5" }));
  localStorage.setItem("evener-hub.spawn-defaults./tmp/lifecycle-b", JSON.stringify({ model: "openai/retired-b" }));
  const catalog = deferred<ModelListResponse>();
  renderSpawn(readyClient((f) => f.on("model/list", () => catalog.promise)));
  await settled();
  await visitSpawnURL("/new?dir=/tmp/lifecycle-b");
  expect(modelValue().textContent).toBe("openai/retired-b");
  await visitSpawnURL("/new?dir=/tmp/lifecycle-a");
  await act(async () => catalog.resolve({ data: [{ provider: "openai", model: "gpt-5" }] }));
  expect(localStorage.getItem("evener-hub.spawn-defaults./tmp/lifecycle-b")).toBeNull();
  expect(modelValue().textContent).toBe("openai/gpt-5");
  await visitSpawnURL("/new?dir=/tmp/lifecycle-b");
  expect(modelValue().textContent).not.toContain("retired-b");
  expect(screen.getByText(/discarded last-used model openai\/retired-b/i)).toBeTruthy();
  await visitSpawnURL("/new?dir=/tmp/lifecycle-a");
  expect(modelValue().textContent).toBe("openai/gpt-5");
  expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
});

test("entering a new draft validates defaults saved after the global sweep", async () => {
  window.history.pushState({}, "", "/new?dir=/tmp/lifecycle-a");
  const catalog = deferred<ModelListResponse>();
  renderSpawn(readyClient((f) => f.on("model/list", () => catalog.promise)));
  await settled();
  await act(async () => catalog.resolve({ data: [{ provider: "openai", model: "gpt-5" }] }));
  localStorage.setItem("evener-hub.spawn-defaults./tmp/lifecycle-b", JSON.stringify({ model: "openai/retired-b" }));
  await visitSpawnURL("/new?dir=/tmp/lifecycle-b");
  expect(modelValue().textContent).not.toContain("retired-b");
  expect(screen.getByText(/discarded last-used model openai\/retired-b/i)).toBeTruthy();
});

test.each(["unknown", "empty", "error"])("global cleanup fails open for an %s catalog", async (kind) => {
  window.history.pushState({}, "", "/new?dir=/tmp/lifecycle-a");
  const saved = JSON.stringify({ model: "openai/retained" });
  localStorage.setItem("evener-hub.spawn-defaults./tmp/lifecycle-a", saved);
  localStorage.setItem("evener-hub.spawn-defaults.global.model", "openai/retained");
  const catalog = deferred<ModelListResponse>();
  renderSpawn(readyClient((f) => f.on("model/list", () => catalog.promise)));
  await settled();
  await act(async () => {
    if (kind === "error") catalog.reject(new Error("catalog unavailable"));
    else catalog.resolve({ data: kind === "unknown" ? [{ provider: "private", model: "other" }] : [] });
  });
  expect(modelValue().textContent).toBe("openai/retained");
  expect(localStorage.getItem("evener-hub.spawn-defaults./tmp/lifecycle-a")).toBe(saved);
  expect(localStorage.getItem("evener-hub.spawn-defaults.global.model")).toBe("openai/retained");
  expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
});

test("a draft's discard notice survives another draft selecting a model and clears on its own selection", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/lifecycle-a");
  localStorage.setItem("evener-hub.spawn-defaults./tmp/lifecycle-a", JSON.stringify({ model: "openai/retired-a" }));
  renderSpawn(readyClient());
  expect(await screen.findByText(/discarded last-used model openai\/retired-a/i)).toBeTruthy();
  await visitSpawnURL("/new?dir=/tmp/lifecycle-b");
  await user.click(modelTrigger());
  await user.click(await screen.findByRole("option", { name: /gpt-5/ }));
  expect(modelValue().textContent).toBe("openai/gpt-5");
  expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
  await visitSpawnURL("/new?dir=/tmp/lifecycle-a");
  expect(screen.getByText(/discarded last-used model openai\/retired-a/i)).toBeTruthy();
  await user.click(modelTrigger());
  await user.click(await screen.findByRole("option", { name: /gpt-5/ }));
  expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
});

test("two draft discard notices coexist across provider refresh and pane remount", async () => {
  window.history.pushState({}, "", "/new?dir=/tmp/lifecycle-a");
  localStorage.setItem("evener-hub.spawn-defaults./tmp/lifecycle-a", JSON.stringify({ model: "openai/retired-a" }));
  localStorage.setItem("evener-hub.spawn-defaults./tmp/lifecycle-b", JSON.stringify({ model: "openai/retired-b" }));
  let refreshed = false;
  const fake = readyClient((f) =>
    f.on("model/list", () => ({
      data: [
        { provider: "openai", model: "gpt-5" },
        ...(refreshed ? [] : [{ provider: "openai", model: "retired-b" }]),
      ],
    })),
  );
  connectionStore.getState().connect(fake);
  const mounted = renderSpawn(fake);
  expect(await screen.findByText(/discarded last-used model openai\/retired-a/i)).toBeTruthy();
  await visitSpawnURL("/new?dir=/tmp/lifecycle-b");
  expect(modelValue().textContent).toBe("openai/retired-b");
  refreshed = true;
  await act(async () => credentialsStore.getState().fetch());
  expect(await screen.findByText(/discarded last-used model openai\/retired-b/i)).toBeTruthy();
  await visitSpawnURL("/new?dir=/tmp/lifecycle-a");
  expect(screen.getByText(/discarded last-used model openai\/retired-a/i)).toBeTruthy();
  mounted.unmount();
  renderSpawn(fake);
  await settled();
  expect(screen.getByText(/discarded last-used model openai\/retired-a/i)).toBeTruthy();
  await visitSpawnURL("/new?dir=/tmp/lifecycle-b");
  expect(screen.getByText(/discarded last-used model openai\/retired-b/i)).toBeTruthy();
});

test.each(["evener", "external"])("%s scoped catalogs cannot sweep unrelated Evener defaults", async (harness) => {
  window.history.pushState({}, "", "/new?dir=/tmp/lifecycle-a");
  localStorage.setItem("evener-hub.spawn-defaults./tmp/lifecycle-a", JSON.stringify({ harness }));
  const valid = JSON.stringify({ model: "openai/gpt-5", access_mode: "plan" });
  const unknown = JSON.stringify({ model: "private/secret", reasoning_effort: "high" });
  localStorage.setItem("evener-hub.spawn-defaults./tmp/unrelated", valid);
  localStorage.setItem("evener-hub.spawn-defaults./tmp/unknown", unknown);
  localStorage.setItem(
    "evener-hub.spawn-defaults./tmp/stale",
    JSON.stringify({ model: "openai/retired", access_mode: "plan" }),
  );
  localStorage.setItem("evener-hub.spawn-defaults./tmp/malformed", JSON.stringify({ model: "unqualified" }));
  localStorage.setItem("evener-hub.spawn-defaults.global.model", "openai/gpt-5");
  const fake = readyClient((f) =>
    f.on("model/list", (params) => ({
      data:
        params.harness === "evener" && params.cwd === undefined
          ? [{ provider: "openai", model: "gpt-5" }]
          : [{ provider: "openai", model: "scoped-only" }],
    })),
  );
  renderSpawn(fake);
  await settled();
  expect(localStorage.getItem("evener-hub.spawn-defaults./tmp/unrelated")).toBe(valid);
  expect(localStorage.getItem("evener-hub.spawn-defaults.global.model")).toBe("openai/gpt-5");
  expect(localStorage.getItem("evener-hub.spawn-defaults./tmp/unknown")).toBe(unknown);
  expect(JSON.parse(localStorage.getItem("evener-hub.spawn-defaults./tmp/stale") ?? "null")).toEqual({
    access_mode: "plan",
  });
  expect(localStorage.getItem("evener-hub.spawn-defaults./tmp/malformed")).toBeNull();
  expect(modelListRequests(fake).some((params) => params.harness === "evener" && params.cwd === undefined)).toBe(true);
});

test.each(["native-model", "openai/native-model"])(
  "Evener cleanup preserves an external draft's live %s model",
  async (model) => {
    window.history.pushState({}, "", "/new?dir=/tmp/lifecycle-a");
    localStorage.setItem("evener-hub.spawn-defaults./tmp/lifecycle-a", JSON.stringify({ harness: "external", model }));
    renderSpawn(readyClient((f) => f.on("model/list", () => ({ data: [{ provider: "openai", model: "gpt-5" }] }))));
    await settled();
    expect(modelValue().textContent).toBe(model);
    expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
  },
);

test.each([false, true])("project navigation isolates stale-model notices (late catalog: %s)", async (late) => {
  window.history.pushState({}, "", "/new?dir=/tmp/review-a");
  localStorage.setItem("evener-hub.spawn-defaults./tmp/review-a", JSON.stringify({ model: "openai/retired" }));
  const catalog = deferred<ModelListResponse>();
  const fake = readyClient((f) =>
    f.on("model/list", ({ harness, cwd }) =>
      harness === "evener" && cwd === undefined ? catalog.promise : { data: [{ provider: "openai", model: "gpt-5" }] },
    ),
  );
  renderSpawn(fake, true, { connectStore: false });
  await waitFor(() => expect(fake.calls.some((c) => c.method === "model/list")).toBe(true));
  if (late) await visitSpawnURL("/new?dir=/tmp/review-b");
  await act(async () => catalog.resolve({ data: [{ provider: "openai", model: "gpt-5" }] }));
  if (!late) {
    expect(await screen.findByText(/discarded last-used model openai\/retired/i)).toBeTruthy();
    await visitSpawnURL("/new?dir=/tmp/review-b");
  }
  expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
  expect(modelValue().textContent).not.toContain("retired");
});

test.each([false, true])("stale-model sweep retires its originating draft (navigate: %s)", async (navigate) => {
  window.history.pushState({}, "", "/new?dir=/tmp/review-a");
  localStorage.setItem("evener-hub.spawn-defaults./tmp/review-a", JSON.stringify({ model: "openai/retired" }));
  localStorage.setItem("evener-hub.spawn-defaults./tmp/review-b", JSON.stringify({ model: "openai/gpt-5" }));
  const catalog = deferred<ModelListResponse>();
  const fake = readyClient((f) =>
    f.on("model/list", ({ harness, cwd }) =>
      harness === "evener" && cwd === undefined ? catalog.promise : { data: [{ provider: "openai", model: "gpt-5" }] },
    ),
  );
  renderSpawn(fake, true, { connectStore: false });
  await waitFor(() => expect(fake.calls.some((c) => c.method === "model/list")).toBe(true));
  expect(modelValue().textContent).toBe("openai/retired");
  if (navigate) await visitSpawnURL("/new?dir=/tmp/review-b");
  await act(async () => catalog.resolve({ data: [{ provider: "openai", model: "gpt-5" }] }));
  if (navigate) {
    expect(modelValue().textContent).toBe("openai/gpt-5");
    expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
    await visitSpawnURL("/new?dir=/tmp/review-a");
  }
  expect(modelValue().textContent).not.toContain("retired");
  expect(screen.getByText(/discarded last-used model openai\/retired/i)).toBeTruthy();
});

test("stale-model sweep preserves a newer user selection in its originating draft and isolates B", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/review-a");
  localStorage.setItem("evener-hub.spawn-defaults./tmp/review-a", JSON.stringify({ model: "openai/retired" }));
  localStorage.setItem("evener-hub.spawn-defaults./tmp/review-b", JSON.stringify({ model: "openai/gpt-5" }));
  const catalog = deferred<ModelListResponse>();
  let refreshing = false;
  let refreshRequested = false;
  const fake = readyClient((f) =>
    f.on("model/list", ({ harness, cwd }) => {
      if (refreshing && harness === "evener" && cwd === undefined) {
        refreshRequested = true;
        return catalog.promise;
      }
      return {
        data: [
          { provider: "openai", model: "retired" },
          { provider: "openai", model: "gpt-5" },
        ],
      };
    }),
  );
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();
  refreshing = true;
  await act(async () => credentialsStore.getState().fetch());
  await waitFor(() => expect(refreshRequested).toBe(true));
  await user.click(modelTrigger());
  await user.clear(await screen.findByRole("combobox", { name: "Model" }));
  await user.click(await screen.findByRole("option", { name: /gpt-5/ }));
  expect(modelValue().textContent).toBe("openai/gpt-5");
  await visitSpawnURL("/new?dir=/tmp/review-b");
  expect(modelValue().textContent).toBe("openai/gpt-5");
  await act(async () => catalog.resolve({ data: [{ provider: "openai", model: "gpt-5" }] }));
  expect(modelValue().textContent).toBe("openai/gpt-5");
  expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
  await visitSpawnURL("/new?dir=/tmp/review-a");
  expect(modelValue().textContent).toBe("openai/gpt-5");
  expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
});

test("stale-model sweep snapshots the current draft on provider refresh after navigation", async () => {
  window.history.pushState({}, "", "/new?dir=/tmp/review-a");
  localStorage.setItem("evener-hub.spawn-defaults./tmp/review-a", JSON.stringify({ model: "openai/gpt-5" }));
  localStorage.setItem("evener-hub.spawn-defaults./tmp/review-b", JSON.stringify({ model: "openai/retired" }));
  const catalog = deferred<ModelListResponse>();
  let refreshing = false;
  let refreshRequested = false;
  const fake = readyClient((f) =>
    f.on("model/list", ({ harness, cwd }) => {
      if (refreshing && harness === "evener" && cwd === undefined) {
        refreshRequested = true;
        return catalog.promise;
      }
      return {
        data: [
          { provider: "openai", model: "retired" },
          { provider: "openai", model: "gpt-5" },
        ],
      };
    }),
  );
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();
  await waitFor(() => expect(fake.calls.some((c) => c.method === "model/list")).toBe(true));
  await visitSpawnURL("/new?dir=/tmp/review-b");
  expect(modelValue().textContent).toBe("openai/retired");
  refreshing = true;
  await act(async () => credentialsStore.getState().fetch());
  await waitFor(() => expect(refreshRequested).toBe(true));
  await visitSpawnURL("/new?dir=/tmp/review-a");
  await act(async () => catalog.resolve({ data: [{ provider: "openai", model: "gpt-5" }] }));
  expect(modelValue().textContent).toBe("openai/gpt-5");
  expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
  await visitSpawnURL("/new?dir=/tmp/review-b");
  expect(modelValue().textContent).not.toContain("retired");
  expect(screen.getByText(/discarded last-used model openai\/retired/i)).toBeTruthy();
});

test("project navigation clears the old default-model gate while the new resolve is pending", async () => {
  window.history.pushState({}, "", "/new?dir=/tmp/review-a");
  const next = deferred<LaunchConfigResolved>();
  const fake = readyClient((f) =>
    f.on("evener/launch/resolve", ({ cwd }) =>
      cwd === "/tmp/review-a" ? { effective: {}, layers: {}, provenance: {} } : next.promise,
    ),
  );
  renderSpawn(fake);
  await waitFor(() => expect(modelState()).toBe("required"));
  await visitSpawnURL("/new?dir=/tmp/review-b");
  await waitFor(() =>
    expect(
      fake.calls.some(
        (c) => c.method === "evener/launch/resolve" && (c.params as { cwd?: string }).cwd === "/tmp/review-b",
      ),
    ).toBe(true),
  );
  expect(modelState()).not.toBe("required");
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);
  await act(async () =>
    next.resolve({ effective: { model: "anthropic/claude-sonnet-4-5" }, layers: {}, provenance: {} }),
  );
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);
});

test.each(["settled error", "late error"])(
  "project navigation isolates advanced path validation: %s",
  async (scenario) => {
    const user = setupUser();
    window.history.pushState({}, "", "/new?dir=/tmp/review-a");
    const validation = deferred<{ path: string; valid: boolean; error: string }>();
    const fake = readyClient((f) => {
      f.on("evener/launch/schema", () => ({
        options: [
          {
            field: "agent",
            wireField: "agent",
            label: "Agent",
            kind: "text",
            group: "general",
            pathKind: "command",
            perLaunch: true,
          },
        ],
      }));
      f.on("evener/path/validate", ({ path }) =>
        path === "review-agent" ? validation.promise : { path, valid: true },
      );
    });
    renderSpawn(fake);
    await user.click(screen.getByRole("button", { name: "Advanced options" }));
    fireEvent.change(screen.getByLabelText("Agent"), { target: { value: "review-agent" } });
    const finish = async () =>
      act(async () => validation.resolve({ path: "review-agent", valid: false, error: "review-a-invalid" }));
    if (scenario === "settled error") {
      await finish();
      expect(screen.getByText("review-a-invalid")).toBeTruthy();
    }
    await visitSpawnURL("/new?dir=/tmp/review-b");
    if (scenario === "late error") await finish();
    expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("");
    expect(screen.queryByText("review-a-invalid")).toBeNull();
    // Navigation must not collapse the intentionally mounted advanced controls.
    expect(screen.getByRole("button", { name: "Show resolved config" })).toBeTruthy();
  },
);

test.each(["settled success", "settled error", "late success", "late error"])(
  "project navigation isolates advanced config preview: %s",
  async (scenario) => {
    const user = setupUser();
    window.history.pushState({}, "", "/new?dir=/tmp/review-a");
    const preview = deferred<LaunchConfigResolved>();
    let previewRequested = false;
    const fake = readyClient((f) =>
      f.on("evener/launch/resolve", ({ cwd }) =>
        previewRequested && cwd === "/tmp/review-a"
          ? preview.promise
          : { effective: { model: "anthropic/claude-sonnet-4-5" }, layers: {}, provenance: {} },
      ),
    );
    renderSpawn(fake);
    await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));
    await user.click(screen.getByRole("button", { name: "Advanced options" }));
    previewRequested = true;
    await user.click(screen.getByRole("button", { name: "Show resolved config" }));
    const finish = async () =>
      act(async () => {
        if (scenario.endsWith("error")) preview.reject(new Error("review-a-resolve-error"));
        else preview.resolve({ effective: { model: "review-a-resolved-model" }, layers: {}, provenance: {} });
      });
    if (scenario.startsWith("settled")) {
      await finish();
      if (scenario.endsWith("error")) expect(screen.getByText(/review-a-resolve-error/)).toBeTruthy();
      else
        expect(screen.getByRole("group", { name: "Resolved config" }).textContent).toContain("review-a-resolved-model");
    }
    await visitSpawnURL("/new?dir=/tmp/review-b");
    if (scenario.startsWith("late")) await finish();
    expect(screen.queryByRole("group", { name: "Resolved config" })).toBeNull();
    expect(screen.queryByText(/review-a-resolve-error/)).toBeNull();
    await user.click(screen.getByRole("button", { name: "Show resolved config" }));
    expect((await screen.findByRole("group", { name: "Resolved config" })).textContent).toContain(
      "anthropic/claude-sonnet-4-5",
    );
  },
);

test.each(["%20/tmp/review-a%20", "%20%20"])(
  "URL directory normalization preserves draft and launch identity: %s",
  async (dir) => {
    const user = setupUser();
    window.history.pushState({}, "", "/new?dir=/tmp/review-a");
    const fake = readyClient();
    renderSpawn(fake);
    await fillPrompt(user, "normalized-draft");
    await visitSpawnURL(`/new?dir=${dir}`);
    expect(promptField().textContent).toBe("normalized-draft");
    await user.click(screen.getByTestId("spawn-submit"));
    await waitFor(() => expect(fake.calls.some((c) => c.method === "thread/start")).toBe(true));
    expect(fake.calls.find((c) => c.method === "thread/start")?.params).toMatchObject({
      cwd: "/tmp/review-a",
      input: [{ type: "text", text: "normalized-draft" }],
    });
  },
);

test("directory picker assigns the unscoped draft and restores each project's launch settings", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new");
  const fake = readyClient();
  const mounted = renderSpawn(fake);
  await fillPrompt(user, "unscoped-sentinel");
  fireEvent.change(effortControl(), { target: { value: "high" } });
  mounted.unmount();
  renderSpawn(fake);
  expect(promptField().textContent).toBe("unscoped-sentinel");
  await setWorkingDir(user, "/tmp/draft-a");
  await pickModel(user, "gpt-5", "openai/gpt-5");
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.selectOptions(screen.getByLabelText("Access mode"), "Read-only");
  await user.selectOptions(screen.getByLabelText("Harness"), "evener");
  await setWorkingDir(user, "/tmp/draft-b");
  expect(promptField().textContent).toBe("");
  expect((effortControl() as HTMLSelectElement).value).toBe("");
  await fillPrompt(user, "draft-b-sentinel");
  await setWorkingDir(user, "/tmp/draft-a");
  expect(promptField().textContent).toBe("unscoped-sentinel");
  expect((effortControl() as HTMLSelectElement).value).toBe("high");
  expect(modelValue().textContent).toBe("openai/gpt-5");
  expect((screen.getByLabelText("Access mode") as HTMLSelectElement).value).toBe("read-only");
  expect((screen.getByLabelText("Harness") as HTMLSelectElement).value).toBe("evener");
  await setWorkingDir(user, "/tmp/draft-b");
  expect(promptField().textContent).toBe("draft-b-sentinel");
});

test("URL prefill is applied to its project but not replayed over edits on remount", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/draft-a&prompt=seed");
  const fake = readyClient();
  const mounted = renderSpawn(fake);
  await user.type(promptField(), "-edited");
  mounted.unmount();
  renderSpawn(fake);
  expect(promptField().textContent).toBe("seed-edited");
  await visitSpawnURL("/new?dir=/tmp/draft-b&prompt=other-seed");
  expect(promptField().textContent).toBe("other-seed");
  await visitSpawnURL("/new?dir=/tmp/draft-a");
  expect(promptField().textContent).toBe("seed-edited");
  await visitSpawnURL("/new?prompt=replacement");
  expect(promptField().textContent).toBe("replacement");
});

test("unchanged URL directory prefill does not replace a picker-selected draft on remount", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/review-a");
  const fake = readyClient();
  const mounted = renderSpawn(fake);
  await setWorkingDir(user, "/tmp/review-b");
  await fillPrompt(user, "picker-draft");
  mounted.unmount();
  renderSpawn(fake);
  expectWorkingDir("/tmp/review-b");
  expect(promptField().textContent).toBe("picker-draft");
  await visitSpawnURL("/new?dir=/tmp/review-a");
  expectWorkingDir("/tmp/review-a");
  expect(promptField().textContent).toBe("");
});

test.each(["unrelated field", "newer same field", "other project"])(
  "late advanced path validation preserves %s edits after remount",
  async (scenario) => {
    const user = setupUser();
    window.history.pushState({}, "", "/new?dir=/tmp/review-a");
    const validation = deferred<{ path: string; valid: boolean }>();
    const fake = readyClient((f) => {
      f.on("evener/launch/schema", () => ({
        options: [
          {
            field: "agent",
            wireField: "agent",
            label: "Agent",
            kind: "text",
            group: "general",
            pathKind: "command",
            perLaunch: true,
          },
          {
            field: "maxRounds",
            wireField: "maxRounds",
            label: "Max rounds",
            kind: "integer",
            group: "general",
            perLaunch: true,
          },
        ],
      }));
      f.on("evener/path/validate", ({ path }) =>
        path === "review-agent" ? validation.promise : { path, valid: true },
      );
    });
    const mounted = renderSpawn(fake);
    await user.click(screen.getByRole("button", { name: "Advanced options" }));
    fireEvent.change(screen.getByLabelText("Agent"), { target: { value: "review-agent" } });
    mounted.unmount();
    renderSpawn(fake);
    await user.click(screen.getByRole("button", { name: "Advanced options" }));
    fireEvent.change(screen.getByLabelText("Max rounds"), { target: { value: "7" } });
    if (scenario === "newer same field") {
      fireEvent.change(screen.getByLabelText("Agent"), { target: { value: "review-new" } });
    }
    if (scenario === "other project") {
      await visitSpawnURL("/new?dir=/tmp/review-b");
      fireEvent.change(screen.getByLabelText("Max rounds"), { target: { value: "9" } });
    }
    await act(async () => validation.resolve({ path: "review-agent", valid: scenario !== "newer same field" }));
    if (scenario === "other project") {
      expect((screen.getByLabelText("Max rounds") as HTMLInputElement).value).toBe("9");
      expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("");
      await visitSpawnURL("/new?dir=/tmp/review-a");
    }
    expect((screen.getByLabelText("Max rounds") as HTMLInputElement).value).toBe("7");
    const agent = scenario === "newer same field" ? "review-new" : "review-agent";
    expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe(agent);
    await user.click(screen.getByTestId("spawn-submit"));
    await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));
    expect(fake.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({
      cwd: "/tmp/review-a",
      launchOverrides: { agent, maxRounds: 7 },
    });
  },
);

test("successful creation preserves edits made while directory preflight was pending", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/draft-a");
  const validation = deferred<{ path: string; valid: boolean }>();
  const fake = readyClient((f) => f.on("evener/path/validate", () => validation.promise));
  renderSpawn(fake);
  await fillPrompt(user, "submitted-sentinel");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "evener/path/validate")).toBe(true));
  selectEditorText(promptField(), promptField().textContent?.length ?? 0);
  await user.type(promptField(), "-newer", { skipClick: true });
  await act(async () => validation.resolve({ path: "/tmp/draft-a", valid: true }));
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  expect(fake.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({
    input: [{ type: "text", text: "submitted-sentinel" }],
  });
  expect(promptField().textContent).toBe("submitted-sentinel-newer");
});

test("late missing-directory preflight keeps Create and start with its originating draft", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/draft-a");
  const validation = deferred<{ path: string; valid: boolean }>();
  const fake = readyClient((f) => f.on("evener/path/validate", () => validation.promise));
  renderSpawn(fake);
  await fillPrompt(user, "draft-a-sentinel");
  await user.click(screen.getByTestId("spawn-submit"));
  await visitSpawnURL("/new?dir=/tmp/draft-b");
  await fillPrompt(user, "draft-b-sentinel");
  await act(async () => validation.resolve({ path: "/tmp/draft-a", valid: false }));
  expect(screen.queryByRole("dialog")).toBeNull();
  await visitSpawnURL("/new?dir=/tmp/draft-a");
  await user.click(await screen.findByRole("button", { name: "Create & start" }));
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  expect(fake.calls.find((call) => call.method === "evener/dirs/create")?.params).toEqual({ path: "/tmp/draft-a" });
  expect(fake.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({
    cwd: "/tmp/draft-a",
    input: [{ type: "text", text: "draft-a-sentinel" }],
  });
  await visitSpawnURL("/new?dir=/tmp/draft-b");
  expect(promptField().textContent).toBe("draft-b-sentinel");
});

test("late successful creation clears only the originating draft across remount and project navigation", async () => {
  installCanvasStubs();
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/draft-a");
  const started = deferred<ThreadStartResponse>();
  const fake = readyClient((f) => f.on("thread/start", () => started.promise));
  const mounted = renderSpawn(fake);
  await fillPrompt(user, "submitted-sentinel");
  act(() => pastePngInto(promptField(), "submitted.png"));
  await screen.findByRole("button", { name: "View submitted.png" });
  fireEvent.change(effortControl(), { target: { value: "high" } });
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  mounted.unmount();
  renderSpawn(fake);
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);
  await visitSpawnURL("/new?dir=/tmp/draft-b");
  await fillPrompt(user, "other-project-sentinel");
  act(() => pastePngInto(promptField(), "other.png"));
  await screen.findByRole("button", { name: "View other.png" });
  await act(async () => started.resolve(startResponse("local:abc123")));
  expect(promptField().textContent).toBe("other-project-sentinel[image 1]");
  expect(screen.getByRole("button", { name: "View other.png" })).toBeTruthy();
  await visitSpawnURL("/new?dir=/tmp/draft-a");
  expect(promptField().textContent).toBe("");
  expect(screen.queryByTestId("attachment-tile")).toBeNull();
  expect((effortControl() as HTMLSelectElement).value).toBe("high");
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);
  expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1);
  expect(localStorage.getItem("evener-hub.spawn-defaults.global.working_dir")).toBe("/tmp/draft-a");
});

test("restoring a project's effort never clamps it against the previous project's catalog", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/draft-a");
  const fake = readyClient((f) => {
    f.on("model/list", ({ cwd }) => ({
      data: [
        {
          provider: "anthropic",
          model: "claude-sonnet-4-5",
          displayName: "anthropic/claude-sonnet-4-5",
          reasoningEffortLevels: cwd === "/tmp/draft-b" ? ["low"] : ["high"],
        },
      ],
    }));
    f.on("evener/launch/resolve", () => ({
      effective: { model: "anthropic/claude-sonnet-4-5" },
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await waitFor(() => expect(effortOptionValues()).toEqual(["", "high", "none"]));
  await user.selectOptions(effortControl(), "high");
  await visitSpawnURL("/new?dir=/tmp/draft-b");
  await waitFor(() => expect(effortOptionValues()).toEqual(["", "low", "none"]));
  await visitSpawnURL("/new?dir=/tmp/draft-a");
  expect((effortControl() as HTMLSelectElement).value).toBe("high");
  await waitFor(() => expect(effortOptionValues()).toEqual(["", "high", "none"]));
  expect((effortControl() as HTMLSelectElement).value).toBe("high");
});
installSpawnTestHooks();

// RoboRev PR1131 finding 3: model/list can serialize an empty Go slice as
// `data: null` (appwire's ModelListResponse.Data carries no omitempty); the
// sweep and validation callbacks passed r.data straight into helpers that
// iterate it, so a null payload rejected the callback and skipped its work.
test("a null model/list payload does not reject the defaults sweep", async () => {
  window.history.pushState({}, "", "/new?dir=/tmp/null-catalog-sweep");
  localStorage.setItem(
    "evener-hub.spawn-defaults./tmp/null-catalog-sweep",
    JSON.stringify({ model: "openai/gpt-5", access_mode: "plan" }),
  );
  const fake = readyClient((f) => {
    f.on("model/list", () => ({ data: null }) as unknown as ModelListResponse);
  });
  renderSpawn(fake);
  await settled();
  // The sweep ran to completion instead of throwing: an unenumerated provider's
  // model is left alone (verdict "unknown"), so the blob is untouched.
  const raw = localStorage.getItem("evener-hub.spawn-defaults./tmp/null-catalog-sweep");
  expect(raw).not.toBeNull();
  expect(JSON.parse(raw as string)).toEqual({ model: "openai/gpt-5", access_mode: "plan" });
});

// RoboRev PR1131 finding 8: `branch` stayed component-local while SpawnForm
// persists across draft switches, so project B showed project A's branch until
// B's evener/git/head completed - and indefinitely whenever it failed.
test("the branch readout never shows the previous project's branch after a draft switch", async () => {
  const headA = deferred<{ head: string }>();
  const headB = deferred<{ head: string }>();
  const fake = readyClient((f) => {
    f.on("evener/git/head", ({ cwd }) => (cwd === "/tmp/branch-project-a" ? headA.promise : headB.promise));
  });
  window.history.pushState({}, "", "/new?dir=/tmp/branch-project-a");
  renderSpawn(fake);
  await settled();
  await act(async () => headA.resolve({ head: "feature-a" }));
  await waitFor(() => expect(screen.getByTestId("spawn-branch").textContent).toBe("feature-a"));

  await visitSpawnURL("/new?dir=/tmp/branch-project-b");
  // B's HEAD is still in flight: A's branch must not linger under B.
  expect(screen.queryByTestId("spawn-branch")).toBeNull();

  await act(async () => headB.resolve({ head: "feature-b" }));
  await waitFor(() => expect(screen.getByTestId("spawn-branch").textContent).toBe("feature-b"));
});

// RoboRev PR1131 finding 5: draft ownership was keyed on the readValues callback
// identity, which Spawn recreates via useCallback(..., [draft]) on every draft
// change. Returning to a draft after visiting another (A -> B -> A) yields a NEW
// callback identity for the SAME draft, so an in-flight path validation's error
// was dropped even though its field value still lives in A's own draft (the
// validation still wrote its `invalid` flag, silently hiding the message).
test("a draft re-entered after another keeps its late path-validation error", async () => {
  const user = setupUser();
  const validation = deferred<{ path: string; valid: boolean; error: string }>();
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({
      options: [
        {
          field: "agent",
          wireField: "agent",
          label: "Agent",
          kind: "text",
          group: "general",
          pathKind: "command",
          perLaunch: true,
        },
      ],
    }));
    f.on("evener/path/validate", ({ path }) => (path === "review-agent" ? validation.promise : { path, valid: true }));
  });
  window.history.pushState({}, "", "/new?dir=/tmp/reentry-validation-a");
  renderSpawn(fake);
  await settled();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  fireEvent.change(screen.getByLabelText("Agent"), { target: { value: "review-agent" } });

  // Visit another draft and come back: A's draft is the SAME object, but Spawn's
  // readValues useCallback produces a new identity for it.
  await visitSpawnURL("/new?dir=/tmp/reentry-validation-b");
  await visitSpawnURL("/new?dir=/tmp/reentry-validation-a");
  expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("review-agent");

  await act(async () => validation.resolve({ path: "review-agent", valid: false, error: "reentry-a-invalid" }));
  expect(await screen.findByText("reentry-a-invalid")).toBeTruthy();
});

// RoboRev PR1131 finding: the validation MESSAGE lived only in component-local
// `errors` while the `invalid` flag is draft-persisted in advancedValues. The
// sibling regression above only covers a validation that settles AFTER its
// draft is active again; a result that lands while the draft is INACTIVE still
// writes the flag (so collectAdvancedOverrides keeps dropping the field) but has
// no error to show when the draft is revisited. The field must not be silently
// excluded with no message.
test("a path-validation error that settles while its draft is inactive appears when the draft is revisited", async () => {
  const user = setupUser();
  const validation = deferred<{ path: string; valid: boolean; error: string }>();
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({
      options: [
        {
          field: "agent",
          wireField: "agent",
          label: "Agent",
          kind: "text",
          group: "general",
          pathKind: "command",
          perLaunch: true,
        },
      ],
    }));
    f.on("evener/path/validate", ({ path }) => (path === "review-agent" ? validation.promise : { path, valid: true }));
  });
  window.history.pushState({}, "", "/new?dir=/tmp/inactive-validation-a");
  renderSpawn(fake);
  await settled();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  fireEvent.change(screen.getByLabelText("Agent"), { target: { value: "review-agent" } });
  await waitFor(() => expect(fake.calls.some((call) => call.method === "evener/path/validate")).toBe(true));

  // Leave A before the validator settles, so its rejection lands while B is active.
  await visitSpawnURL("/new?dir=/tmp/inactive-validation-b");
  await act(async () => validation.resolve({ path: "review-agent", valid: false, error: "inactive-a-invalid" }));

  // Returning to A finds the value and its draft-persisted invalid flag intact:
  // the field is still excluded from A's launch overrides...
  await visitSpawnURL("/new?dir=/tmp/inactive-validation-a");
  expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("review-agent");
  expect(completionDraft("/tmp/inactive-validation-a").fields.getState().advancedOverrides).toEqual({});
  // ...so the message explaining the exclusion must be available again.
  expect(await screen.findByText("inactive-a-invalid")).toBeTruthy();

  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));
  const startParams = fake.calls.find((call) => call.method === "thread/start")?.params as
    | ThreadStartParams
    | undefined;
  expect(startParams?.cwd).toBe("/tmp/inactive-validation-a");
  // startThread omits an empty layer entirely, so accept either encoding: the
  // point is that the invalid field never reaches the server.
  expect(startParams?.launchOverrides ?? {}).toEqual({});
});

// The same loss through the other door the finding names: a pane remount
// recreates the component, so local `errors` starts empty even though the
// draft still holds the invalid field.
test("a remounted draft still shows the path-validation error stored with its invalid field", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({
      options: [
        {
          field: "agent",
          wireField: "agent",
          label: "Agent",
          kind: "text",
          group: "general",
          pathKind: "command",
          perLaunch: true,
        },
      ],
    }));
    f.on("evener/path/validate", ({ path }) =>
      path === "review-agent" ? { path, valid: false, error: "remount-a-invalid" } : { path, valid: true },
    );
  });
  window.history.pushState({}, "", "/new?dir=/tmp/remount-validation-a");
  const mounted = renderSpawn(fake);
  await settled();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  fireEvent.change(screen.getByLabelText("Agent"), { target: { value: "review-agent" } });
  expect(await screen.findByText("remount-a-invalid")).toBeTruthy();

  mounted.unmount();
  renderSpawn(fake);
  await settled();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));

  expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("review-agent");
  expect(completionDraft("/tmp/remount-validation-a").fields.getState().advancedOverrides).toEqual({});
  expect(await screen.findByText("remount-a-invalid")).toBeTruthy();
});
