import "../session/testing/editorGeometry";
import type {
  HostForwardedResult,
  HostRequestParams,
  LaunchOption,
  ModelDescriptor,
  ModelListResponse,
  NavigationManifest,
  ThreadStartParams,
  ThreadStartResponse,
} from "@evener/appwire-client";
import { HOST_DEPENDENT_DISCOVERY_METHODS, WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { connectionStore } from "../../stores/connection";
import { hostsStore } from "../../stores/hosts";
import { toastKinds } from "../../widgets/toast/toastTestUtils";
import { loadDefaultsBlob } from "./spawnDefaults";
import { resetSpawnDraftsForTests, selectSpawnDirectory, setDraftField } from "./spawnDrafts";
import {
  completionDraft,
  deferred,
  expectModelReadsDefault,
  expectWorkingDir,
  fillPrompt,
  installSpawnTestHooks,
  LAST_WORKING_DIR_KEY,
  modelListRequests,
  modelState,
  modelTrigger,
  modelValue,
  openDesktopPluginSelection,
  pickModel,
  promptField,
  readyClient,
  renderSpawn,
  routedDiscoveryDefault,
  SPAWN_PLUGIN_PREVIEW,
  seedSources,
  settled,
  setupUser,
  setWorkingDir,
  waitForPluginPreviewState,
  workingDir,
} from "./spawnTestUtils";

installSpawnTestHooks();

// A remote launch's cwd and model belong to the SELECTED HOST, not this
// controller, so they must not be written to the global scalar defaults a later
// LOCAL spawn reads - the remote cwd would default the next local Spawn to a
// path that usually does not exist here.
test("a remote launch does not persist its cwd as the controller's global working directory", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient();
  window.history.pushState({}, "", "/new?dir=/srv/app");
  renderSpawn(fake);
  await settled();

  // A model plus a cwd: both are host-scoped on a remote submit.
  await act(async () => {
    setDraftField(completionDraft("/srv/app"), "model", "openai/gpt-5");
  });
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await fillPrompt(user, "run remotely");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((c) => c.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  expect(params.cwd).toBe("/srv/app");
  // The controller's global defaults a later LOCAL spawn consults are untouched.
  expect(localStorage.getItem("evener-hub.spawn-defaults.global.working_dir")).toBeNull();
  expect(localStorage.getItem("evener-hub.spawn-defaults.global.model")).toBeNull();
});

// --- remote target: host-scoped project defaults (Component 06b, round eight) ---

// model/list's first entry: the model the uncredentialed-default fallback
// installs, and (in the cross-draft test) a legitimately sticky value that must
// not be confused with it.
const MODEL_A_FALLBACK = "anthropic/claude-sonnet-4-5";

// The per-project blob is keyed by the cwd ALONE, and that path is often also a
// local checkout: a model chosen on the selected host must not become this
// path's LOCAL default - one this hub may not serve. The remote project's
// harness/access layer is still remembered (round seven's blob disposition,
// minus the host-specific model).
test("a remote launch's model does not become the cwd's local project default", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient();
  window.history.pushState({}, "", "/new?dir=/srv/app");
  const mounted = renderSpawn(fake);
  await settled();

  // A host-only model, as the remote hub's own catalog would offer it.
  await act(async () => {
    setDraftField(completionDraft("/srv/app"), "model", "buildbox-only/gpt-5");
  });
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await fillPrompt(user, "run remotely");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  // The launch still forwards the model the person configured - the selected
  // host resolves it against its own catalog.
  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  expect(params.model).toBe("buildbox-only/gpt-5");
  // ...but it is not remembered as this path's local default.
  expect(loadDefaultsBlob("/srv/app").model).toBeUndefined();

  // A later LOCAL spawn of the same path (a fresh page, so fresh drafts) reads
  // exactly those persisted defaults: Model is untouched, not the host's.
  mounted.unmount();
  resetSpawnDraftsForTests();
  renderSpawn(fake);
  await settled();
  expectModelReadsDefault();
  expect(modelTrigger().textContent).not.toContain("gpt-5");
});

// The fallback marker is per DRAFT (round eight). SpawnForm is a singleton
// reused across drafts, so a form-local marker let draft A's provenance retire
// an identical model string draft B legitimately owned: B's sticky per-project
// default was cleared when B went remote even though the fallback never touched
// B's model.
test("a fallback installed for one draft does not retire another draft's identical sticky model", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  // Draft B's own sticky default - deliberately the exact string draft A's
  // fallback installs.
  localStorage.setItem("evener-hub.spawn-defaults./tmp/fallback-owner", JSON.stringify({ model: MODEL_A_FALLBACK }));
  const fake = readyClient((f) => {
    f.on("model/list", () => ({
      data: [{ provider: "anthropic", model: "claude-sonnet-4-5", displayName: MODEL_A_FALLBACK }],
    }));
    f.on("evener/launch/resolve", () => ({
      effective: { model: "openai/gpt-5.5" }, // openai is not launchable on THIS controller
      layers: {},
      provenance: {},
    }));
  });
  window.history.pushState({}, "", "/new?dir=/tmp/fallback-installer");
  renderSpawn(fake);
  await settled();

  // Draft A: the controller-catalog fallback installs models[0].
  await waitFor(() => expect(modelTrigger().textContent).toContain(MODEL_A_FALLBACK));

  // Draft B, reached through the cwd picker: same string, different provenance.
  await setWorkingDir(user, "/tmp/fallback-owner");
  expect(modelTrigger().textContent).toContain(MODEL_A_FALLBACK);

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  // B's model is B's own sticky default, so it survives the remote switch.
  expect(modelTrigger().textContent).toContain(MODEL_A_FALLBACK);
  expect(modelState()).toBe("chosen");

  await fillPrompt(user, "run remotely");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  expect(params.model).toBe(MODEL_A_FALLBACK);
});

// The marker lives WITH the draft, so it survives the pane unmounting and
// remounting (a single-pane/mobile host mounts only the active route, and the
// draft map outlives the form). A form-local ref lost it there, which left the
// controller's fallback model to ride a remote launch after all.
test("a controller-catalog fallback model is still retired after a pane remount", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient((f) => {
    f.on("model/list", () => ({
      data: [{ provider: "anthropic", model: "claude-sonnet-4-5", displayName: MODEL_A_FALLBACK }],
    }));
    f.on("evener/launch/resolve", () => ({
      effective: { model: "openai/gpt-5.5" },
      layers: {},
      provenance: {},
    }));
  });
  window.history.pushState({}, "", "/new?dir=/tmp/fallback-remount");
  const mounted = renderSpawn(fake);
  await settled();
  await waitFor(() => expect(modelTrigger().textContent).toContain(MODEL_A_FALLBACK));

  // A remount with no navigation keeps the same draft - and so its provenance.
  mounted.unmount();
  renderSpawn(fake);
  await settled();
  expect(modelTrigger().textContent).toContain(MODEL_A_FALLBACK);

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await settled();
  await waitFor(expectModelReadsDefault);
  expect(modelTrigger().textContent).not.toContain("claude-sonnet-4-5");

  await fillPrompt(user, "run remotely");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  expect(params.model).toBeUndefined();
  expect(params.modelProvider).toBeUndefined();
});

// The complement of the retire rule, and the invariant that keeps it honest: a
// model the person picked themselves is their own choice (handleModelChange
// drops the draft's fallback mark), so it is never retired on a remote switch -
// not even in a draft where the fallback had installed a different model.
test("a user's explicit model pick is preserved when the target becomes remote", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient((f) => {
    f.on("model/list", () => ({
      data: [
        { provider: "anthropic", model: "claude-sonnet-4-5", displayName: MODEL_A_FALLBACK },
        { provider: "openai", model: "gpt-5", displayName: "openai/gpt-5" },
      ],
    }));
    // mistral is not launchable on THIS controller, so the fallback fires even
    // though openai (the person's own pick below) is launchable.
    f.on("evener/launch/resolve", () => ({
      effective: { model: "mistral/large" },
      layers: {},
      provenance: {},
    }));
  });
  window.history.pushState({}, "", "/new?dir=/tmp/fallback-explicit");
  renderSpawn(fake);
  await settled();

  // The fallback installs the controller's first launchable model...
  await waitFor(() => expect(modelTrigger().textContent).toContain(MODEL_A_FALLBACK));
  // ...and then the person picks a different one from the picker.
  await pickModel(user, "gpt-5", "openai/gpt-5");
  expect(modelTrigger().textContent).toContain("openai/gpt-5");

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  // The explicit pick is not the fallback's value, so it is not retired.
  expect(modelTrigger().textContent).toContain("openai/gpt-5");
  expect(modelState()).toBe("chosen");

  await fillPrompt(user, "run remotely");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  expect(params.model).toBe("openai/gpt-5");
});

// The dir-picker's seed (GLOBAL_LAST_WORKING_DIR_KEY) is the CONTROLLER's own
// browse history: recording a cwd picked while a remote host is selected would
// open the next LOCAL picker at a path that usually does not exist here.
test("a cwd picked for a remote target is not recorded as the controller's picker seed", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  window.history.pushState({}, "", "/new?dir=/tmp/seed-local");
  // The path validator is host-routed (component 07b): the selected host
  // answers for its own filesystem, so the picker's acceptance of the remote
  // path below is the host's verdict.
  renderSpawn(
    readyClient((f) =>
      f.on("evener/host/request", (params) =>
        (params as HostRequestParams).method === "evener/path/validate"
          ? ({ path: "/srv/remote-only", valid: true } as HostForwardedResult)
          : routedDiscoveryDefault((params as HostRequestParams).method),
      ),
    ),
  );
  await settled();

  // A local pick still updates the controller's own browse history.
  await setWorkingDir(user, "/tmp/seed-local-next");
  expect(localStorage.getItem(LAST_WORKING_DIR_KEY)).toBe("/tmp/seed-local-next");

  // With a remote host selected, the path belongs to that host instead.
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  localStorage.removeItem(LAST_WORKING_DIR_KEY);
  await setWorkingDir(user, "/srv/remote-only");
  expect(localStorage.getItem(LAST_WORKING_DIR_KEY)).toBeNull();
});

// --- remote target: source-aware paths (Component 06b, round nine) -----------

// evener/path/validate answers for THIS controller's filesystem, so a directory
// that exists only on the selected host reads as invalid here - and the picker
// refuses to select what its validator rejects. A remote target's cwd belongs to
// that host, which validates it when the session starts (the authority the cwd
// preflight already defers to), so the controller must not make the judgment at
// all (round nine).
test("a remote target can select a directory this controller cannot see", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient((f) => {
    // The controller's own filesystem cannot see the path...
    f.on("evener/path/validate", ({ path }) => ({ path, valid: false, error: "no such file or directory" }));
    // ...but the selected host can, and it is the host that judges a remote
    // target's path (component 07b, through evener/host/request).
    f.on("evener/host/request", (params) =>
      (params as HostRequestParams).method === "evener/path/validate"
        ? ({ path: "/srv/remote-only", valid: true } as HostForwardedResult)
        : routedDiscoveryDefault((params as HostRequestParams).method),
    );
  });
  window.history.pushState({}, "", "/new?dir=/tmp/picker-base");
  renderSpawn(fake);
  await settled();

  // A LOCAL target keeps the controller's gate unchanged: the same path is
  // reported invalid and cannot be selected.
  await user.click(workingDir());
  const localInput = await screen.findByRole("textbox", { name: "Path" });
  await user.clear(localInput);
  await user.type(localInput, "/srv/remote-only{Enter}");
  expect((await screen.findByRole("alert")).textContent).toBe("no such file or directory");
  expect((screen.getByRole("button", { name: "Use this folder" }) as HTMLButtonElement).disabled).toBe(true);
  await user.click(screen.getByRole("button", { name: "Cancel" }));

  // The same path on a remote target is the host's business: it can be picked.
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await setWorkingDir(user, "/srv/remote-only");
  expectWorkingDir("/srv/remote-only");
  expect(screen.queryByText("no such file or directory")).toBeNull();
});

// The picker's "New folder" is a WRITE, and evener/dirs/create MkdirAll's the
// path on the filesystem of the hub it reaches. A remote target's path belongs
// to the selected host, so the request is forwarded there (component 07b)
// instead of materializing the folder on the controller.
test("the picker creates a folder on the selected host, never on the controller", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const forwarded: string[] = [];
  const fake = readyClient((f) =>
    f.on("evener/host/request", (params) => {
      const method = (params as HostRequestParams).method;
      forwarded.push(method);
      if (method === "evener/dirs/create")
        return { path: "/srv/remote-create/child", created: true } as HostForwardedResult;
      return routedDiscoveryDefault(method);
    }),
  );
  window.history.pushState({}, "", "/new?dir=/tmp/remote-create-base");
  renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await user.click(workingDir());
  const input = await screen.findByRole("textbox", { name: "Path" });
  await user.clear(input);
  await user.type(input, "/srv/remote-create{Enter}");
  await waitFor(() =>
    expect((screen.getByRole("button", { name: "Use this folder" }) as HTMLButtonElement).disabled).toBe(false),
  );
  await user.click(screen.getByRole("button", { name: "New folder" }));
  await user.type(screen.getByRole("textbox", { name: "Folder name" }), "child{Enter}");
  await settled();

  // Nothing was created on the controller...
  expect(fake.calls.some((call) => call.method === "evener/dirs/create")).toBe(false);
  // ...the creation went to the selected host, through the admin proxy.
  expect(forwarded).toContain("evener/dirs/create");
  for (const call of fake.calls.filter((call) => call.method === "evener/host/request")) {
    expect((call.params as HostRequestParams).host).toBe("buildbox");
  }
});

// The advanced panel validates a path-kind value through the same controller
// closure and marks the field invalid on failure - and
// collectAdvancedOverrides DROPS an invalid field, so a valid remote path
// silently vanished from launchOverrides. For a remote target the value is
// forwarded for the selected host to judge (round nine).
test("a remote target's advanced path value is not dropped for being invisible here", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const schema = {
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
  };
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => schema);
    // The controller cannot see the value...
    f.on("evener/path/validate", ({ path }) => ({ path, valid: false, error: "no such file or directory" }));
    // ...and the selected host judges its own filesystem (component 07b), where
    // the path kind accepts it. The schema is host-dependent too.
    f.on("evener/host/request", (params) => {
      const method = (params as HostRequestParams).method;
      if (method === "evener/launch/schema") return schema as HostForwardedResult;
      if (method === "evener/path/validate") return { path: "", valid: true } as HostForwardedResult;
      return routedDiscoveryDefault(method);
    });
  });
  window.history.pushState({}, "", "/new?dir=/tmp/remote-advanced-path");
  renderSpawn(fake);
  await settled();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));

  // Local: unchanged. The controller's verdict marks the field invalid, the
  // reason is shown, and the value is excluded from the launch.
  fireEvent.change(screen.getByLabelText("Agent"), { target: { value: "host-only-agent" } });
  expect(await screen.findByText("no such file or directory")).toBeTruthy();
  await waitFor(() =>
    expect(completionDraft("/tmp/remote-advanced-path").fields.getState().advancedOverrides).toEqual({}),
  );

  // Remote: the same kind of value rides thread/start for the host to resolve.
  // (A different string, not the one above: React suppresses a change event
  // whose value is unchanged, so re-typing the identical text would never reach
  // the field's own handler.)
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  // The target switch retires the previous host's schema until the selected
  // host's own answer lands, so the field reappears with the host's options.
  fireEvent.change(await screen.findByLabelText("Agent"), { target: { value: "remote-only-agent" } });
  expect(completionDraft("/tmp/remote-advanced-path").fields.getState().advancedOverrides).toEqual({
    agent: "remote-only-agent",
  });
  await fillPrompt(user, "run remotely");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  expect(params.launchOverrides).toMatchObject({ agent: "remote-only-agent" });
});

// Switching to a host the cached registry predates re-judges the stored path
// values on that host; the verdict must wait for the registry to list it rather
// than be refused and left standing as the previous host's "invalid".
test.each([false, true])(
  "a path judged invalid on the previous host is re-judged once the registry lists the new host (older poll in flight: %s)",
  async (pollInFlight) => {
    seedSources([
      { id: "local", label: "Local", kind: "local", online: true },
      { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
    ]);
    const schema = {
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
    };
    const hostRow = (name: string) => ({
      name,
      generation: 1,
      incarnationId: `inc-${name}`,
      origin: "hub.toml" as const,
      attached: true,
      midAttach: false,
      removed: false,
    });
    const registry = deferred<{ hosts: ReturnType<typeof hostRow>[] }>();
    const olderPoll = deferred<{ hosts: ReturnType<typeof hostRow>[] }>();
    let listReads = 0;
    const fake = readyClient((f) => {
      f.on("evener/launch/schema", () => schema);
      f.on("evener/path/validate", ({ path }) => ({ path, valid: false, error: "no such file or directory" }));
      f.on("evener/host/list", () => {
        listReads += 1;
        // The first read is a poll that started before the host was added.
        return pollInFlight && listReads === 1 ? olderPoll.promise : registry.promise;
      });
      f.on("evener/host/request", (params) => {
        const method = (params as HostRequestParams).method;
        if (method === "evener/launch/schema") return schema as HostForwardedResult;
        if (method === "evener/path/validate") return { path: "", valid: true } as HostForwardedResult;
        return routedDiscoveryDefault(method);
      });
    });
    hostsStore.setState({ load: { phase: "ready", hosts: [hostRow("other")] } });
    window.history.pushState({}, "", "/new?dir=/tmp/registry-lag-path");
    renderSpawn(fake);
    if (pollInFlight) await act(async () => void hostsStore.getState().refresh());
    await settled();
    fireEvent.click(screen.getByRole("button", { name: "Advanced options" }));
    fireEvent.change(screen.getByLabelText("Agent"), { target: { value: "host-only-agent" } });
    await waitFor(() =>
      expect(completionDraft("/tmp/registry-lag-path").fields.getState().advancedOverrides).toEqual({}),
    );

    fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
    await settled();
    await act(async () => {
      registry.resolve({ hosts: [hostRow("other"), hostRow("buildbox")] });
      olderPoll.resolve({ hosts: [hostRow("other")] });
    });

    await waitFor(() =>
      expect(completionDraft("/tmp/registry-lag-path").fields.getState().advancedOverrides).toEqual({
        agent: "host-only-agent",
      }),
    );
  },
);

// The host picker is a DISPLAY of what the manifest last said about the hosts.
// The store retains that snapshot while the fresh manifest is in flight, and
// reading the SETTLED-only list there hid the picker (or flipped the visible
// choice to Local) for the length of every revalidation - while the launch
// decision still has to come from the settled list, which is what the second
// half of this test pins (round nine).
test("the host picker keeps its host while the manifest revalidates", async () => {
  const user = setupUser();
  const sources: NavigationManifest["sources"] = [
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
    { id: "offline-host", label: "offline-host", kind: "ssh", online: false },
  ];
  seedSources(sources);
  const fake = readyClient();
  window.history.pushState({}, "", "/new?dir=/tmp/host-revalidate");
  renderSpawn(fake);
  await settled();
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });

  await act(async () => seedSources(sources, { loading: true, stale: true }));
  const picker = screen.getByLabelText("Host") as HTMLSelectElement;
  expect(picker.value).toBe("buildbox");
  const options = within(picker).getAllByRole("option") as HTMLOptionElement[];
  expect(options.map((option) => option.value)).toEqual(["local", "buildbox", "offline-host"]);
  // The retained snapshot keeps its own offline labelling: a withheld list
  // reads as "unknown", which would flip this one to online.
  const offline = options.find((option) => option.value === "offline-host") as HTMLOptionElement;
  expect(offline.disabled).toBe(true);
  expect(offline.textContent).toContain("offline");

  // Unsettled, the draft's host is still the launch target (only a settled
  // manifest may confirm a fallback), so what the picker shows is what submits.
  await fillPrompt(user, "run remotely");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));
  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
});

// --- remote target: target-mode changes (Component 06b, round ten) -----------

// The stale-model sweep judges a draft's model against THIS controller's
// model/list. A remote launch runs on the selected source's own hub, which
// resolves its own model, so a model this catalog does not list can be
// perfectly valid there - and the sweep must not erase it. The target mode is
// re-read when the response lands, so a draft that switches hosts while the
// request is in flight (the sequence below) is not judged by the run it
// started as local. The local half is unchanged and stays pinned by the
// existing sweep tests above.
test("a remote target keeps the draft's model this controller's catalog calls stale", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  window.history.pushState({}, "", "/new?dir=/tmp/remote-stale-model");
  localStorage.setItem(
    "evener-hub.spawn-defaults./tmp/remote-stale-model",
    JSON.stringify({ model: "openai/retired" }),
  );
  const global = deferred<ModelListResponse>();
  const fake = readyClient((f) => {
    f.on("model/list", ({ harness, cwd }) =>
      harness === "evener" && cwd === undefined ? global.promise : { data: [{ provider: "openai", model: "gpt-5" }] },
    );
    // The SELECTED HOST's catalog is what judges a remote draft's model
    // (component 07b), and it still offers the draft's own model.
    f.on("evener/host/request", (params) =>
      (params as HostRequestParams).method === "model/list"
        ? ({ data: [{ provider: "openai", model: "retired" }] } as HostForwardedResult)
        : routedDiscoveryDefault((params as HostRequestParams).method),
    );
  });
  renderSpawn(fake);
  await settled();
  // The sticky model is in the draft, and the controller's global catalog - the
  // one whose verdict sweeps a local draft - has not answered yet.
  expect(modelValue().textContent).toContain("openai/retired");

  // The draft moves to the remote host while that request is in flight.
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await act(async () => global.resolve({ data: [{ provider: "openai", model: "gpt-5" }] }));

  // The controller's verdict is a fact about THIS controller: the host it is
  // about to launch on still offers the model, so the draft keeps it and
  // nothing claims it was discarded.
  expect(modelValue().textContent).toContain("openai/retired");
  expect(screen.queryByText(/discarded last-used model/i)).toBeNull();

  // A remote launch forwards the model for the selected host to resolve.
  await fillPrompt(user, "run remotely");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));
  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  expect(params.model).toBe("openai/retired");
});

// The same skip applies to a draft that is ALREADY on a remote host when a
// fresh catalog lands (a remount, or a provider refresh under a remote draft):
// nothing is registered to sweep it at all. The first mount parks its own
// request so the model is still in the draft, and the remount's request - the
// registration the fix has to refuse - answers immediately.
test("a draft already on a remote host is never swept by the controller's catalog", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  window.history.pushState({}, "", "/new?dir=/tmp/remote-remount-model");
  localStorage.setItem(
    "evener-hub.spawn-defaults./tmp/remote-remount-model",
    JSON.stringify({ model: "openai/retired" }),
  );
  const parked = deferred<ModelListResponse>();
  let globalRequests = 0;
  let routedRequests = 0;
  const fake = readyClient((f) => {
    f.on("model/list", ({ harness, cwd }) => {
      if (harness !== "evener" || cwd !== undefined) return { data: [{ provider: "openai", model: "gpt-5" }] };
      globalRequests += 1;
      return globalRequests === 1 ? parked.promise : { data: [{ provider: "openai", model: "gpt-5" }] };
    });
    // A remote draft's catalog read is forwarded to the selected host
    // (component 07b), so the controller's own listing is not what a remote
    // mount consults.
    f.on("evener/host/request", (params) => {
      const forwarded = params as HostRequestParams;
      if (forwarded.method !== "model/list") return routedDiscoveryDefault(forwarded.method);
      const { harness, cwd } = forwarded.params as { harness?: string; cwd?: string };
      if (harness !== "evener" || cwd !== undefined) {
        return { data: [{ provider: "openai", model: "gpt-5" }] } as HostForwardedResult;
      }
      routedRequests += 1;
      // The host offers the draft's own model, so its verdict keeps it.
      return { data: [{ provider: "openai", model: "retired" }] } as HostForwardedResult;
    });
  });
  const mounted = renderSpawn(fake, true, { connectStore: false });
  await settled();
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await act(async () => parked.resolve({ data: [{ provider: "openai", model: "gpt-5" }] }));
  expect(modelValue().textContent).toContain("openai/retired");

  // Remount: the draft (and its host) persist, so the fresh mount's own
  // catalog request resolves under a remote target from its first render - and
  // that request is the HOST's (routed), never the controller's parked one.
  mounted.unmount();
  renderSpawn(fake, true, { connectStore: false });
  await settled();
  await act(async () => {});
  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox");
  expect(routedRequests).toBeGreaterThan(0);
  expect(globalRequests).toBe(1);
  expect(modelValue().textContent).toContain("openai/retired");
  expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
});

// A path-kind field's `invalid` flag (and its message) records what THIS
// controller could see at the moment the value changed, and
// collectAdvancedOverrides drops a flagged field from launchOverrides. Moving
// the Host picker to a remote source changes who judges the path - the host
// does, at start - but nothing re-validated the stored value, so a remote-only
// path typed while local stayed dropped and kept showing the controller's
// stale error. The mode change re-validates the stored value (round ten).
test("a path marked invalid while local reaches the launch after switching host without re-typing", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const schema = {
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
  };
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => schema);
    // The controller cannot see the path...
    f.on("evener/path/validate", ({ path }) => ({ path, valid: false, error: "no such file or directory" }));
    // ...and the selected host judges its own filesystem (component 07b), where
    // the value is fine. The schema is host-dependent too, so it is answered
    // here for the routed read.
    f.on("evener/host/request", (params) => {
      const method = (params as HostRequestParams).method;
      if (method === "evener/launch/schema") return schema as HostForwardedResult;
      if (method === "evener/path/validate") return { path: "", valid: true } as HostForwardedResult;
      return routedDiscoveryDefault(method);
    });
  });
  window.history.pushState({}, "", "/new?dir=/tmp/remote-switch-path");
  renderSpawn(fake);
  await settled();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));

  // Local: the controller's verdict marks the field invalid, shows the reason,
  // and excludes the value from the launch.
  fireEvent.change(await screen.findByLabelText("Agent"), { target: { value: "host-only-agent" } });
  expect(await screen.findByText("no such file or directory")).toBeTruthy();
  await waitFor(() =>
    expect(completionDraft("/tmp/remote-switch-path").fields.getState().advancedOverrides).toEqual({}),
  );

  // The Host picker moves to the remote source WITHOUT re-typing the value.
  // The host switch re-reads the launch schema, so the Advanced fields remount;
  // wait for the value to land in the draft before judging the field.
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await waitFor(() =>
    expect(completionDraft("/tmp/remote-switch-path").fields.getState().advancedOverrides).toEqual({
      agent: "host-only-agent",
    }),
  );
  expect(((await screen.findByLabelText("Agent")) as HTMLInputElement).value).toBe("host-only-agent");
  expect(screen.queryByText("no such file or directory")).toBeNull();

  // Back to local, the controller is the judge again: the same stored value is
  // re-marked and dropped rather than left silently flagged or silently kept.
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "local" } });
  expect(await screen.findByText("no such file or directory")).toBeTruthy();
  await waitFor(() =>
    expect(completionDraft("/tmp/remote-switch-path").fields.getState().advancedOverrides).toEqual({}),
  );
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await waitFor(() =>
    expect(completionDraft("/tmp/remote-switch-path").fields.getState().advancedOverrides).toEqual({
      agent: "host-only-agent",
    }),
  );

  await fillPrompt(user, "run remotely");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));
  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  expect(params.launchOverrides).toMatchObject({ agent: "host-only-agent" });
});

// --- host-routed discovery (Component 07b) ---------------------------------

// The discovery methods the spawn form's mount path issues for the selected
// host. The on-demand methods (path completion/validation, dirs/create, recent
// projects, the slash catalog, plugin preview) are covered by their own seams'
// tests; appwire-client/typescript/hostRouting.test.ts covers the whole spec set at the seam.
const MOUNT_DISCOVERY_METHODS = [
  "model/list",
  "evener/harnesses/list",
  "evener/launch/schema",
  "evener/launch/resolve",
  "evener/git/head",
  "evener/instance/list",
] as const;

test("a selected remote host routes every discovery call through evener/host/request", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = new FakeClient("ready");
  const forwarded: Record<string, unknown> = {
    "model/list": {
      data: [{ provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" }],
    },
    "evener/harnesses/list": { data: [{ id: "evener", label: "evener", kind: "evener" }] },
    "evener/launch/schema": { options: [] },
    "evener/launch/resolve": { effective: {}, layers: {}, provenance: {} },
    "evener/git/head": { head: "main" },
    "evener/instance/list": { instances: [], availableProviders: [] },
    "evener/projects/recent": { data: [] },
    "evener/paths/complete": { data: [] },
    "evener/path/validate": { path: "", valid: true },
    "evener/dirs/create": { path: "", created: true },
    "evener/plugin/preview": { plugins: [] },
    "evener/spawn/slashCatalog": { commands: [], skills: [] },
  };
  fake.on("evener/host/request", (params) => {
    const method = (params as HostRequestParams).method;
    return (forwarded[method] ?? {}) as HostForwardedResult;
  });
  connectionStore.getState().connect(fake);

  // Select the remote host before the form mounts, so the mount-time discovery
  // calls are issued against it from the first render.
  const draft = selectSpawnDirectory("/tmp/remote-routing");
  setDraftField(draft, "source", "buildbox");
  window.history.pushState({}, "", "/new?dir=/tmp/remote-routing");
  renderSpawn(fake);
  await settled();

  await waitFor(() => {
    const routed = new Set(
      fake.calls
        .filter((call) => call.method === "evener/host/request")
        .map((call) => (call.params as HostRequestParams).method),
    );
    for (const method of MOUNT_DISCOVERY_METHODS) expect(routed.has(method)).toBe(true);
  });

  // Every proxied call names the selected host...
  for (const call of fake.calls.filter((call) => call.method === "evener/host/request")) {
    expect((call.params as HostRequestParams).host).toBe("buildbox");
  }
  // ...and nothing was left controller-scoped.
  for (const method of HOST_DEPENDENT_DISCOVERY_METHODS) {
    expect(fake.calls.some((call) => call.method === method)).toBe(false);
  }
});

// The "buildbox" host's answers for every discovery call the spawn form can
// issue. `fail` names methods the remote host refuses (an offline host, a proxy
// rejection); every other answer is a well-formed empty/minimal response.
function answerRemoteHost(
  fake: FakeClient,
  options: { overrides?: Record<string, unknown>; fail?: readonly string[] } = {},
): void {
  const answers: Record<string, unknown> = {
    "model/list": { data: [{ provider: "openai", model: "gpt-4o", displayName: "openai/gpt-4o" }] },
    "evener/harnesses/list": { data: [{ id: "evener", label: "evener", kind: "evener" }] },
    "evener/launch/schema": { options: [] },
    "evener/launch/resolve": { effective: {}, layers: {}, provenance: {} },
    "evener/git/head": { head: "main" },
    "evener/instance/list": { instances: [], availableProviders: [] },
    "evener/projects/recent": { data: [] },
    "evener/paths/complete": { data: [] },
    "evener/path/validate": { path: "", valid: true },
    "evener/dirs/create": { path: "", created: true },
    "evener/plugin/preview": { plugins: [] },
    "evener/spawn/slashCatalog": { commands: [], skills: [] },
    ...options.overrides,
  };
  fake.on("evener/host/request", (params) => {
    const forwarded = params as HostRequestParams;
    if (forwarded.host !== "buildbox") throw new Error(`unexpected host "${forwarded.host}"`);
    if (options.fail?.includes(forwarded.method)) throw new Error(`remote ${forwarded.method} unavailable`);
    const answer = answers[forwarded.method];
    // A function answers per call: a host whose answer depends on what is asked
    // (whether it has a directory, vs the verdict on a field's value).
    if (typeof answer === "function") return (answer as (params: unknown) => HostForwardedResult)(forwarded.params);
    return (answer ?? {}) as HostForwardedResult;
  });
}

const REMOTE_SOURCES: NavigationManifest["sources"] = [
  { id: "local", label: "Local", kind: "local", online: true },
  { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
];

// buildbox's own credentialed provider instance, the same shape readyClient
// gives the controller. answerRemoteHost's default is an EMPTY registry (what the
// provider-setup tests want: "configure one on that host to use a model"), so a
// host that is launch-ready has to say so itself - otherwise providerRequired
// holds Start disabled and nothing can be submitted.
const REMOTE_CREDENTIALED_INSTANCE = {
  name: "anthropic",
  providerId: "anthropic",
  protocol: "anthropic",
  auth: "bearer",
  implicit: false,
  isDefault: true,
  activeSource: "store",
  hasStoredOAuth: false,
  credentialRequired: true,
};

function readyRemoteHost(fake: FakeClient, overrides: Record<string, unknown> = {}): void {
  answerRemoteHost(fake, {
    overrides: {
      "evener/instance/list": { instances: [REMOTE_CREDENTIALED_INSTANCE], availableProviders: [] },
      ...overrides,
    },
  });
}

// A remote host's catalog describes only that host. The controller's persisted
// spawn defaults are controller-scoped: sweeping them against another machine's
// model list would permanently delete models the controller still offers, and
// switching back to Local would leave those project defaults lost.
test("a remote host's catalog never sweeps the controller's saved model defaults", async () => {
  window.history.pushState({}, "", "/new?dir=/tmp/remote-sweep");
  const savedBlob = JSON.stringify({ harness: "evener", model: "openai/gpt-5" });
  localStorage.setItem("evener-hub.spawn-defaults./tmp/remote-sweep", savedBlob);
  localStorage.setItem("evener-hub.spawn-defaults.global.model", "openai/gpt-5");
  seedSources(REMOTE_SOURCES);
  // The remote catalog offers the openai provider but not the saved model, so
  // modelValidityAgainstList would classify "openai/gpt-5" as stale.
  const fake = readyClient((f) => answerRemoteHost(f));
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/remote-sweep");
  setDraftField(draft, "source", "buildbox");
  renderSpawn(fake);
  await settled();

  // The remote catalog has been consumed by the sweep effect and the draft
  // validator (which reports the in-memory draft's model as discarded).
  expect(await screen.findByText(/discarded last-used model openai\/gpt-5/i)).toBeTruthy();
  expect(localStorage.getItem("evener-hub.spawn-defaults./tmp/remote-sweep")).toBe(savedBlob);
  expect(localStorage.getItem("evener-hub.spawn-defaults.global.model")).toBe("openai/gpt-5");
});

// The same rule in the state an unsettled manifest produces: the settled list
// is withheld, so hostChoice is a provisional "local" while the draft still
// names its host - and every catalog this pane reads, the sweep's included, is
// issued against the DRAFT'S host (round nine's `submittedSource = source`).
// The controller's stored defaults must survive it exactly as they survive a
// settled remote target: the sweep's authority is the machine the form is
// reading, never the withheld fallback.
test("a revalidating manifest does not hand the controller's catalog the sweep", async () => {
  const cwd = "/tmp/remote-sweep-revalidate";
  window.history.pushState({}, "", `/new?dir=${cwd}`);
  const savedBlob = JSON.stringify({ harness: "evener", model: "openai/gpt-5" });
  localStorage.setItem(`evener-hub.spawn-defaults.${cwd}`, savedBlob);
  localStorage.setItem("evener-hub.spawn-defaults.global.model", "openai/gpt-5");
  seedSources(REMOTE_SOURCES, { loading: true, stale: true });
  // The host's catalog offers the openai provider but not the saved model, so a
  // sweep running against it would delete both stored values.
  const fake = readyClient((f) =>
    answerRemoteHost(f, {
      overrides: { "model/list": { data: [{ provider: "openai", model: "gpt-4o", displayName: "openai/gpt-4o" }] } },
    }),
  );
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory(cwd);
  setDraftField(draft, "source", "buildbox");
  renderSpawn(fake);
  await settled();

  // The pane's catalog is the host's: the routed model/list answers for it, and
  // the controller's own list is never asked at all.
  await waitFor(() =>
    expect(
      fake.calls.filter(
        (call) =>
          call.method === "evener/host/request" &&
          (call.params as HostRequestParams).method === "model/list" &&
          (call.params as HostRequestParams).host === "buildbox",
      ).length,
    ).toBeGreaterThan(0),
  );
  expect(modelListRequests(fake)).toHaveLength(0);
  // So the stored layer is exactly as it was stored.
  expect(localStorage.getItem(`evener-hub.spawn-defaults.${cwd}`)).toBe(savedBlob);
  expect(localStorage.getItem("evener-hub.spawn-defaults.global.model")).toBe("openai/gpt-5");
});

// A host change drops the previous host's harness/schema catalogs: they describe
// the machine that answered, and a failed load for the new host must show an
// empty catalog rather than the previous host's, which the user could otherwise
// select and only discover is missing at thread/start.
test("a failed remote catalog load shows an empty harness list, not the previous host's", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) => answerRemoteHost(f, { fail: ["evener/harnesses/list", "evener/launch/schema"] }));
  connectionStore.getState().connect(fake);
  window.history.pushState({}, "", "/new?dir=/tmp/host-catalog");
  renderSpawn(fake);
  await settled();

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const harness = screen.getByLabelText("Harness") as HTMLSelectElement;
  expect(within(harness).getByRole("option", { name: "external" })).toBeTruthy();

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");

  await waitFor(() => expect(within(harness).queryByRole("option", { name: "external" })).toBeNull());
  // The fallback the select renders with no host catalog, not the controller's.
  expect(
    within(harness)
      .queryAllByRole("option")
      .map((option) => option.textContent),
  ).toEqual(["evener"]);
});

// On-demand discovery for a remote host reads that host's filesystem too: the
// directory picker's recent-projects list and path completion are proxied, and
// the controller's own methods are never called.
test("a remote spawn reads recent projects and completes paths from the selected host", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) =>
    answerRemoteHost(f, {
      overrides: {
        "evener/projects/recent": { data: ["/srv/remote-project"] },
        "evener/paths/complete": { data: ["/srv/remote-project/src"] },
      },
    }),
  );
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/srv/remote-project");
  setDraftField(draft, "source", "buildbox");
  window.history.pushState({}, "", "/new?dir=/srv/remote-project");
  renderSpawn(fake);
  await settled();

  await user.click(workingDir());
  await screen.findByRole("textbox", { name: "Path" });
  expect(await screen.findByRole("button", { name: "Open /srv/remote-project/src" })).toBeTruthy();

  await waitFor(() => {
    const routed = fake.calls
      .filter((call) => call.method === "evener/host/request")
      .map((call) => call.params as HostRequestParams);
    expect(routed.filter((call) => call.method === "evener/projects/recent" && call.host === "buildbox")).toHaveLength(
      1,
    );
    expect(routed.some((call) => call.method === "evener/paths/complete" && call.host === "buildbox")).toBe(true);
  });
  // Neither on-demand call was left controller-scoped.
  expect(fake.calls.some((call) => call.method === "evener/projects/recent")).toBe(false);
  expect(fake.calls.some((call) => call.method === "evener/paths/complete")).toBe(false);
});

// A credential or model change made ON the selected remote host arrives wrapped
// in evener/host/notification tagged with that host (app_host_admin.go's
// fan-out). The pane's scoped model/list cache is keyed on the credential
// generation, so the wrapper must advance it exactly as the unwrapped
// evener/auth/updated does for the controller -- otherwise a remote spawn keeps
// validating against a catalog the host no longer serves.
test("a selected remote host's wrapped config notification reloads the pane model catalog", async () => {
  seedSources(REMOTE_SOURCES);
  // The host's instance refetch never lands, so nothing but the pane's own
  // generation key can invalidate the catalog: this pins the FORM's observation
  // of the wrapper, not the credentials store's debounced fetchHost.
  const fake = readyClient((f) =>
    answerRemoteHost(f, { overrides: { "evener/instance/list": new Promise(() => {}) } }),
  );
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/remote-notify");
  setDraftField(draft, "source", "buildbox");
  window.history.pushState({}, "", "/new?dir=/tmp/remote-notify");
  renderSpawn(fake);
  await settled();

  // Every model/list this form issues is forwarded to buildbox: the pane
  // catalog, the default-model preview, and the global sweep all share the
  // selected host.
  const paneLoads = () =>
    fake.calls.filter(
      (call) =>
        call.method === "evener/host/request" &&
        (call.params as HostRequestParams).host === "buildbox" &&
        (call.params as HostRequestParams).method === "model/list",
    ).length;
  await waitFor(() => expect(paneLoads()).toBeGreaterThan(0));
  const before = paneLoads();

  await act(async () =>
    fake.emitNotification({
      method: "evener/host/notification",
      params: { host: "buildbox", method: "evener/auth/updated", params: {} },
    }),
  );

  await waitFor(() => expect(paneLoads()).toBeGreaterThan(before));
});

// The same wrapper, in the same unsettled window the picker's revalidation test
// above pins: a withholding manifest makes hostChoice a provisional "local"
// while the pane's catalog is still scoped to the draft's host. Keying this
// listener on hostChoice dropped the host's own wrapper for the length of every
// revalidation, so a credential or model change made on the host during one
// never invalidated the catalog this form validates against (component 07b
// review, residual).
test("the draft's host wrapped config notification reloads the catalog while the manifest revalidates", async () => {
  seedSources(REMOTE_SOURCES, { loading: true, stale: true });
  const fake = readyClient((f) =>
    answerRemoteHost(f, {
      overrides: {
        // Both host reads stay in flight: the instance partition therefore
        // keeps one identity (nothing in the pane's catalog cache key moves
        // with it) and every catalog request is a fresh, recorded one rather
        // than a settled answer reused from it - the same isolation the
        // settled version of this test uses.
        "evener/instance/list": new Promise(() => {}),
        "model/list": new Promise(() => {}),
      },
    }),
  );
  connectionStore.getState().connect(fake);
  const cwd = "/tmp/host-notify-revalidate";
  const draft = selectSpawnDirectory(cwd);
  setDraftField(draft, "source", "buildbox");
  window.history.pushState({}, "", `/new?dir=${cwd}`);
  renderSpawn(fake);
  await settled();
  // Unsettled manifest: the settled list is withheld, so hostChoice is a
  // provisional "local" while the launch target and every discovery call stay
  // on the draft's host.
  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox");

  const catalogLoads = (params: unknown) =>
    fake.calls.filter(
      (call) =>
        call.method === "evener/host/request" &&
        (call.params as HostRequestParams).host === "buildbox" &&
        (call.params as HostRequestParams).method === "model/list" &&
        JSON.stringify((call.params as HostRequestParams).params) === JSON.stringify(params),
    ).length;
  // The pane passes through a harness value on its way to the settled draft
  // scope, and issues one catalog request per scope. Wait for the SETTLED
  // scope's own request: its promise stays in the pane's per-scope cache (the
  // host's answer never lands), so from here only a cache invalidation - the
  // credential generation this test is about - can issue another one.
  await waitFor(() => expect(catalogLoads({ cwd })).toBeGreaterThan(0));
  const before = catalogLoads({ cwd });

  act(() =>
    fake.emitNotification({
      method: "evener/host/notification",
      params: { host: "buildbox", method: "evener/auth/updated", params: {} },
    }),
  );

  await waitFor(() => expect(catalogLoads({ cwd })).toBeGreaterThan(before));
});

// The other half of the same question, in the state a SETTLED manifest produces
// when the draft's host is not launchable (offline here; a host removed from
// the list is the same). hostChoice falls back to "local" and the write-back
// puts that in the draft, so the machine this pane reads - and the machine it
// would launch on - is the controller. The wrapper from the host it no longer
// reads must therefore not move its catalog: the filter is the host that was
// actually asked, which is what lets the pane recover when the host it DOES
// read changes (the revalidation test above).
test("a wrapper from a draft host the offline fallback replaced does not reload the catalog", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: false },
  ]);
  const fake = readyClient();
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/host-offline-notify");
  setDraftField(draft, "source", "buildbox");
  window.history.pushState({}, "", "/new?dir=/tmp/host-offline-notify");
  renderSpawn(fake);
  await settled();

  // The offline fallback is written into the draft (round nine), so the pane's
  // own reads are controller-scoped from here on.
  await waitFor(() => expect(draft.fields.getState().source).toBe("local"));
  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("local");
  // Hydration: the controller's catalog has answered for the SETTLED scope (the
  // scoped load the fallback's write-back triggers). Everything before it is
  // the mount's own churn, and every consumer after it is a cache hit.
  const scopedCatalogLoads = () =>
    fake.calls.filter(
      (call) => call.method === "model/list" && (call.params as { cwd?: string }).cwd === "/tmp/host-offline-notify",
    ).length;
  await waitFor(() => expect(scopedCatalogLoads()).toBeGreaterThan(0));

  const hostLoadsFor = (host: string) =>
    fake.calls.filter(
      (call) =>
        call.method === "evener/host/request" &&
        (call.params as HostRequestParams).host === host &&
        (call.params as HostRequestParams).method === "model/list",
    ).length;
  const before = { controller: modelListRequests(fake).length, replacedHost: hostLoadsFor("buildbox") };
  act(() =>
    fake.emitNotification({
      method: "evener/host/notification",
      params: { host: "buildbox", method: "evener/auth/updated", params: {} },
    }),
  );
  // Nothing to await: the claim is that nothing happens. The window is a
  // tripwire past the pane's 250ms catalog debounce (a generation bump re-runs
  // the catalog effect and issues a request), not the mechanism.
  await act(async () => {
    await vi.advanceTimersByTimeAsync(400);
  });

  // Neither the replaced host's scope nor the controller's catalog moved: the
  // wrapper describes a machine this pane is not reading.
  expect(hostLoadsFor("buildbox")).toBe(before.replacedHost);
  expect(modelListRequests(fake).length).toBe(before.controller);
});

// The provider editor (ConnectProviderDialog) is controller-scoped: it reads and
// writes the credentialsStore's top-level fields on the plain connection. A
// remote host's provider verdict now comes from that host's own partition, so
// offering the controller editor for a remote host would "connect" the wrong
// machine and leave Start disabled. The controller's own path is unchanged.
test("a remote spawn never offers the controller's Connect provider editor", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) => {
    answerRemoteHost(f);
    f.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
  });
  connectionStore.getState().connect(fake);
  window.history.pushState({}, "", "/new?dir=/tmp/remote-connect");
  renderSpawn(fake);
  await settled();

  // Controller with no usable instance: the dialog action is still offered.
  await screen.findByRole("button", { name: "Connect provider" });
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox"));

  // The remote partition reports missing too, but the only offered remediation
  // is a message naming the host, never the controller's editor.
  await screen.findByText(/configure one on that host to use a model/i);
  expect(screen.queryByRole("button", { name: "Connect provider" })).toBeNull();
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);

  // Back on the controller, the editor returns.
  await user.selectOptions(screen.getByLabelText("Host"), "local");
  await screen.findByRole("button", { name: "Connect provider" });
});

// The branch readout describes the SELECTED host's working tree. Switching
// hosts with the same directory must drop the previous host's HEAD immediately,
// not display it until (or after) the new host's evener/git/head resolves.
test("switching hosts with the same directory drops the previous host's branch readout", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) => {
    f.on("evener/git/head", () => ({ head: "local-branch" }));
    // The remote host's HEAD never lands: the controller's readout must not
    // stand in for it.
    answerRemoteHost(f, { overrides: { "evener/git/head": new Promise(() => {}) } });
  });
  connectionStore.getState().connect(fake);
  window.history.pushState({}, "", "/new?dir=/tmp/host-branch");
  renderSpawn(fake);
  await settled();
  await waitFor(() => expect(screen.getByTestId("spawn-branch").textContent).toContain("local-branch"));

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox"));

  await waitFor(() => expect(screen.queryByTestId("spawn-branch")).toBeNull());
});

// ...and the readout must survive the manifest going UNSETTLED again. A
// revalidating manifest withholds its sources, so hostChoice is a provisional
// "local" while the draft's host is still the launch and discovery target
// (round nine) - the same window the picker above pins. Keying the readout on
// hostChoice instead blanked a readout the pane's own evener/git/head call had
// already answered for the draft's host, for the length of every revalidation
// (component 07b review, residual).
test("the branch readout keeps the draft's host while the manifest revalidates", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) => {
    f.on("evener/git/head", () => ({ head: "local-branch" }));
    answerRemoteHost(f, { overrides: { "evener/git/head": { head: "host-branch" } } });
  });
  connectionStore.getState().connect(fake);
  window.history.pushState({}, "", "/new?dir=/tmp/host-branch-revalidate");
  renderSpawn(fake);
  await settled();

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");
  await waitFor(() => expect(screen.getByTestId("spawn-branch").textContent).toBe("host-branch"));

  // The fresh manifest is in flight: the settled list is withheld, so the
  // launch target stays the draft's host (round nine) and so does the host the
  // readout describes.
  await act(async () => seedSources(REMOTE_SOURCES, { loading: true, stale: true }));
  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox");
  expect(screen.getByTestId("spawn-branch").textContent).toBe("host-branch");
});

// The resolved default launch config is a property of the selected host, not
// just the directory. Switching hosts with the same directory must not let the
// previous host's default satisfy -- or fail -- the Start model check while the
// new host's evener/launch/resolve is pending.
test("switching hosts with the same directory drops the previous host's default-model verdict", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) => {
    // The controller has no default model: Start reads as model-required.
    f.on("evener/launch/resolve", () => ({ effective: {}, layers: {}, provenance: {} }));
    // The remote host's resolve never lands, so only the host key can clear the
    // controller's stale "no default" verdict.
    answerRemoteHost(f, { overrides: { "evener/launch/resolve": new Promise(() => {}) } });
  });
  connectionStore.getState().connect(fake);
  window.history.pushState({}, "", "/new?dir=/tmp/host-default");
  renderSpawn(fake);
  await settled();
  await waitFor(() => expect(screen.getByRole("alert").dataset.reason).toBe("no-default-model"));

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox"));

  await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
});

// The draft's launch config is chosen from the SELECTED host's catalogs, and the
// draft store carries it across a host switch. Left alone, a harness that exists
// only on the host that produced it still rides thread/start - and the form
// cannot even show the mismatch, because an empty harness catalog renders the
// "evener" fallback label. buildbox offers only "evener".
test("switching to a host that does not offer the draft's harness drops it from the launch", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) => readyRemoteHost(f));
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/host-harness");
  setDraftField(draft, "harness", "external");
  setDraftField(draft, "model", "openai/gpt-4o");
  window.history.pushState({}, "", "/new?dir=/tmp/host-harness");
  renderSpawn(fake);
  await settled();

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox"));
  // The controller offered "external"; buildbox does not, so the draft's
  // selection drops back to the host's own default.
  await waitFor(() => expect(draft.fields.getState().harness).toBe(""));

  // And the launch carries no harness: the host resolves its own default rather
  // than being handed a harness only the controller has.
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  const started = fake.calls.find((entry) => entry.method === "thread/start");
  expect(started?.params).toMatchObject({ model: "openai/gpt-4o" });
  expect(started?.params).not.toHaveProperty("harness");
});

// The same defect class for Advanced options: the override map is keyed by the
// schema field names the selected host exposes, and an empty schema renders no
// Advanced fields at all - so a stale override would ride thread/start with
// nothing on screen to account for it.
test("switching to a host whose schema lacks the draft's advanced options drops them", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const controllerOption: LaunchOption = {
    field: "agent",
    wireField: "agent",
    label: "Agent",
    group: "general",
    kind: "text",
    perLaunch: true,
  };
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({ options: [controllerOption] }));
    // buildbox's own schema (answerRemoteHost) exposes nothing.
    readyRemoteHost(f);
  });
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/host-schema");
  setDraftField(draft, "advancedOverrides", { agent: "controller-agent" });
  setDraftField(draft, "advancedValues", { agent: { value: "controller-agent" } });
  window.history.pushState({}, "", "/new?dir=/tmp/host-schema");
  renderSpawn(fake);
  await settled();
  expect(draft.fields.getState().advancedOverrides).toEqual({ agent: "controller-agent" });

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox"));

  await waitFor(() => expect(draft.fields.getState().advancedOverrides).toEqual({}));
  expect(draft.fields.getState().advancedValues).toEqual({});
});

// Until the SELECTED host's catalogs answer, the form has no authority about
// what that host offers - and the draft's launch config has not been reconciled
// against them. A submit through that window could carry a value the host does
// not have, so Start is blocked until the answers land (either way: an empty
// catalog is an answer, so a host that refuses one can never hold Start
// hostage).
test("Start is blocked only until the selected host's catalog answers settle", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  let releaseHarnesses!: (result: HostForwardedResult) => void;
  const fake = readyClient((f) =>
    readyRemoteHost(f, {
      "evener/harnesses/list": new Promise<HostForwardedResult>((resolve) => {
        releaseHarnesses = resolve;
      }),
    }),
  );
  connectionStore.getState().connect(fake);
  // A chosen model keeps modelRequired out of the picture, so the only thing
  // that can disable Start here is the catalog block itself (buildbox's
  // evener/launch/resolve reports no default model).
  const draft = selectSpawnDirectory("/tmp/host-pending");
  setDraftField(draft, "model", "openai/gpt-4o");
  window.history.pushState({}, "", "/new?dir=/tmp/host-pending");
  renderSpawn(fake);
  await settled();
  // Nothing about the controller's own catalogs is pending.
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox"));
  await settled();
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);

  act(() => releaseHarnesses({ data: [{ id: "evener", label: "evener", kind: "evener" }] }));
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
});

// The persisted defaults a submit writes are controller-scoped localStorage. A
// remote launch's cwd, harness and model all belong to the selected host, so a
// later LOCAL spawn of the same path must not inherit any of them - and a remote
// submit must not delete a local project's stored layer for that path either.
test("a remote submit writes no controller spawn defaults", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) => readyRemoteHost(f));
  connectionStore.getState().connect(fake);
  // A layer stored for the same path by (or for) the controller - the remote
  // submit below has no authority over it.
  const localBlob = JSON.stringify({ harness: "evener", access_mode: "read-only" });
  localStorage.setItem("evener-hub.spawn-defaults./srv/remote-submit", localBlob);
  const draft = selectSpawnDirectory("/srv/remote-submit");
  setDraftField(draft, "source", "buildbox");
  setDraftField(draft, "model", "openai/gpt-4o");
  window.history.pushState({}, "", "/new?dir=/srv/remote-submit");
  renderSpawn(fake);
  await settled();

  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  const started = fake.calls.find((entry) => entry.method === "thread/start");
  expect(started?.params).toMatchObject({ source: "buildbox" });

  // The per-project blob a local spawn of the same path would read is untouched
  // (component 07b review, round four: the cwd key cannot distinguish the two;
  // this is the half of round four this rebase keeps - the "writes nothing at
  // all" half is re-pinned to main's round eight in the cases above).
  expect(localStorage.getItem("evener-hub.spawn-defaults./srv/remote-submit")).toBe(localBlob);
  // And the controller-wide scalars a local spawn reads are untouched.
  expect(localStorage.getItem("evener-hub.spawn-defaults.global.model")).toBeNull();
  expect(localStorage.getItem("evener-hub.spawn-defaults.global.working_dir")).toBeNull();
});

// --- host-switch hardening (Component 07b review, round four) ---------------

// The host selector must not stay interactive while a submission is awaiting
// preflight or thread/start: a change there moves the picker (and the draft's
// source) while the pending operation keeps using the hostChoice captured at
// submit time, so the launch - and the defaults it persists - can belong to a
// different host than the one on screen.
test("the host picker is locked while a submit is in flight", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) => readyRemoteHost(f));
  // The start never resolves, so the submit stays in flight and the picker's
  // lock is the only thing keeping it from moving off the captured host.
  fake.on("thread/start", () => new Promise<ThreadStartResponse>(() => {}));
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/host-lock");
  setDraftField(draft, "source", "buildbox");
  setDraftField(draft, "model", "openai/gpt-4o");
  window.history.pushState({}, "", "/new?dir=/tmp/host-lock");
  renderSpawn(fake);
  await settled();

  const picker = () => screen.getByLabelText("Host") as HTMLSelectElement;
  await waitFor(() => expect(picker().disabled).toBe(false));
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  expect(picker().disabled).toBe(true);
  expect(picker().value).toBe("buildbox");
  const startedOn = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(startedOn.source).toBe("buildbox");
});

// A plugin selection is a list of names resolved against the SELECTED host's own
// preview. A host switch whose preview fails can never be reconciled, so the
// previous host's explicit names would keep riding combinedOverrides into the
// new host's preview, slash catalog and thread/start - handing that host
// plugins it may not provide.
test("a host switch drops an explicit plugin selection the new host never previewed", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) => {
    f.on("evener/plugin/preview", () => SPAWN_PLUGIN_PREVIEW);
    // buildbox's own preview never answers: there is no list to reconcile the
    // controller's selection against on that host.
    answerRemoteHost(f, {
      fail: ["evener/plugin/preview"],
      overrides: {
        "evener/instance/list": { instances: [REMOTE_CREDENTIALED_INSTANCE], availableProviders: [] },
        // The controller's own catalog (readyClient) offers the same model, so
        // the model-validity effect keeps it across the switch.
        "model/list": { data: [{ provider: "openai", model: "gpt-5", displayName: "openai/gpt-5" }] },
      },
    });
  });
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/host-plugin-switch");
  setDraftField(draft, "model", "openai/gpt-5");
  window.history.pushState({}, "", "/new?dir=/tmp/host-plugin-switch");
  renderSpawn(fake);
  await settled();

  // An explicit selection on the controller.
  await openDesktopPluginSelection(user);
  await user.click(screen.getByRole("switch", { name: "beta" }));
  await waitFor(() => expect(screen.getByTestId("spawn-plugin-summary").textContent).toContain("alpha"));
  expect(draft.fields.getState().pluginSelection).toEqual({ mode: "explicit", names: ["alpha"] });

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");
  await waitForPluginPreviewState("error");

  // The controller's selection is gone with the switch...
  expect(draft.fields.getState().pluginSelection).toEqual({ mode: "default" });
  // ...so the launch hands buildbox no selection at all.
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  const started = fake.calls.find((entry) => entry.method === "thread/start");
  expect(started?.params).toMatchObject({ source: "buildbox" });
  const startedParams = started?.params as ThreadStartParams;
  expect(startedParams.launchOverrides?.enabledPlugins).toBeUndefined();
});

// Drafts persist at module scope, so a mount can start on a remote host whose
// catalogs have not answered yet. The draft's launch config came from that host
// (or another one) and has not been reconciled against the answers still in
// flight: Start must wait, exactly as it does for a host CHANGE.
test("a mount that starts on a remote draft waits for that host's catalogs", async () => {
  seedSources(REMOTE_SOURCES);
  let releaseHarnesses!: (result: HostForwardedResult) => void;
  const fake = readyClient((f) =>
    readyRemoteHost(f, {
      "evener/harnesses/list": new Promise<HostForwardedResult>((resolve) => {
        releaseHarnesses = resolve;
      }),
    }),
  );
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/remote-mount");
  setDraftField(draft, "source", "buildbox");
  setDraftField(draft, "model", "openai/gpt-4o");
  window.history.pushState({}, "", "/new?dir=/tmp/remote-mount");
  renderSpawn(fake);
  await settled();

  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);
  // The ⌘/Ctrl+Enter chord reaches the submit path directly, so the guard has to
  // hold there too: nothing may be preflighted while the catalogs are
  // outstanding.
  fireEvent.keyDown(promptField(), { key: "Enter", metaKey: true });
  await act(async () => {});
  expect(
    fake.calls.filter(
      (call) =>
        call.method === "evener/host/request" && (call.params as HostRequestParams).method === "evener/path/validate",
    ),
  ).toHaveLength(0);
  expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(0);

  act(() => releaseHarnesses({ data: [{ id: "evener", label: "evener", kind: "evener" }] }));
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
});

// A host change clears harnesses/schemaOptions, so the "settled" stamp for the
// host that answered before it must be invalidated with them: switching A→B→A
// before B answers otherwise reconciles the draft against the just-cleared EMPTY
// catalogs and silently wipes the harness and every Advanced-options value the
// restored host still offers - never restored once A's answers land.
test("a switch away and back before the middle host answers keeps the draft", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const controllerOption: LaunchOption = {
    field: "agent",
    wireField: "agent",
    label: "Agent",
    group: "general",
    kind: "text",
    perLaunch: true,
  };
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({ options: [controllerOption] }));
    // buildbox never answers the catalog probe, so only the switch back can
    // decide what the draft is reconciled against.
    answerRemoteHost(f, {
      overrides: {
        "evener/harnesses/list": new Promise<HostForwardedResult>(() => {}),
        "evener/launch/schema": new Promise<HostForwardedResult>(() => {}),
      },
    });
  });
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/host-rapid");
  setDraftField(draft, "harness", "external");
  setDraftField(draft, "model", "openai/gpt-4o");
  setDraftField(draft, "advancedOverrides", { agent: "controller-agent" });
  setDraftField(draft, "advancedValues", { agent: { value: "controller-agent" } });
  window.history.pushState({}, "", "/new?dir=/tmp/host-rapid");
  renderSpawn(fake);
  await settled();
  await waitFor(() => expect(draft.fields.getState().harness).toBe("external"));

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox"));
  await user.selectOptions(screen.getByLabelText("Host"), "local");
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("local"));

  // The controller's catalogs re-answer for the restored host, and the draft's
  // controller-offered config is still there to reconcile against them.
  await waitFor(() => expect(draft.fields.getState().harness).toBe("external"));
  expect(draft.fields.getState().advancedOverrides).toEqual({ agent: "controller-agent" });
  expect(draft.fields.getState().advancedValues).toEqual({ agent: { value: "controller-agent" } });
});

// The directory picker's "last accepted directory" seed is CONTROLLER-wide (the
// fallback a later local picker opens at): a remote host's path stored there
// would seed the next local spawn with a directory this hub usually does not
// have. The controller's own picks keep seeding it.
test("a remote directory pick never seeds the controller's last-working-dir", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) =>
    answerRemoteHost(f, { overrides: { "evener/projects/recent": { data: ["/srv/remote-pick"] } } }),
  );
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/remote-dir-pick");
  setDraftField(draft, "source", "buildbox");
  window.history.pushState({}, "", "/new?dir=/tmp/remote-dir-pick");
  renderSpawn(fake);
  await settled();

  await user.click(workingDir());
  await user.click(await screen.findByRole("button", { name: "Open recent /srv/remote-pick" }));
  const confirm = screen.getByRole("button", { name: "Use this folder" });
  await waitFor(() => expect((confirm as HTMLButtonElement).disabled).toBe(false));
  await user.click(confirm);

  expect(localStorage.getItem(LAST_WORKING_DIR_KEY)).toBeNull();
});

// Host reconciliation filters the Advanced-options maps to the fields the
// selected host offers. An error is state about a field, not about the host, so
// the error of a KEPT field must survive the filter - it explains a validation
// the user still has to fix - while a dropped field's error goes with it.
test("host reconciliation keeps the errors of the advanced fields it keeps", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const agentOption: LaunchOption = {
    field: "agent",
    wireField: "agent",
    label: "Agent",
    group: "general",
    kind: "text",
    perLaunch: true,
  };
  const keptOption: LaunchOption = {
    field: "maxRounds",
    wireField: "maxRounds",
    label: "Max rounds",
    group: "general",
    kind: "text",
    perLaunch: true,
  };
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({ options: [agentOption, keptOption] }));
    // buildbox offers maxRounds but not agent.
    readyRemoteHost(f, { "evener/launch/schema": { options: [keptOption] } });
  });
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/host-errors");
  setDraftField(draft, "model", "openai/gpt-4o");
  setDraftField(draft, "advancedOverrides", { agent: "controller-agent", maxRounds: 7 });
  setDraftField(draft, "advancedValues", {
    agent: { value: "controller-agent" },
    maxRounds: { value: "7" },
  });
  setDraftField(draft, "advancedErrors", { agent: "no agent here", maxRounds: "fix this value" });
  window.history.pushState({}, "", "/new?dir=/tmp/host-errors");
  renderSpawn(fake);
  await settled();

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox"));

  await waitFor(() => expect(draft.fields.getState().advancedOverrides).toEqual({ maxRounds: 7 }));
  expect(draft.fields.getState().advancedValues).toEqual({ maxRounds: { value: "7" } });
  expect(draft.fields.getState().advancedErrors).toEqual({ maxRounds: "fix this value" });
});

// --- round five ------------------------------------------------------------

/** The calls the pane forwarded to `host` through evener/host/request. */
function routedHostCalls(fake: FakeClient, method: string, host = "buildbox"): HostRequestParams[] {
  return fake.calls
    .filter((call) => call.method === "evener/host/request")
    .map((call) => call.params as HostRequestParams)
    .filter((call) => call.method === method && call.host === host);
}

// A failed catalog load is NOT an answer. The round-four rejection handlers
// cleared the lists and the Promise.all stamped the host "settled" whatever the
// outcome, so a single transient hub error reconciled a perfectly good draft
// against an empty catalog: the harness and every Advanced-options override
// vanished, and a later successful load had nothing left to restore. Start must
// still be released by a settled-but-unanswered load (an offline host must not
// hold the submit hostage) - only the RECONCILIATION waits for a real answer.
test("a failed catalog load leaves the draft's harness and advanced options alone", async () => {
  const fake = readyClient((f) => {
    f.on("evener/harnesses/list", () => {
      throw new Error("hub unavailable");
    });
    f.on("evener/launch/schema", () => {
      throw new Error("hub unavailable");
    });
  });
  const draft = selectSpawnDirectory("/tmp/catalog-failure");
  setDraftField(draft, "harness", "external");
  setDraftField(draft, "advancedOverrides", { agent: "controller-agent" });
  setDraftField(draft, "advancedValues", { agent: { value: "controller-agent" } });
  window.history.pushState({}, "", "/new?dir=/tmp/catalog-failure");
  renderSpawn(fake);
  await settled();

  await waitFor(() => expect(fake.calls.filter((call) => call.method === "evener/harnesses/list")).toHaveLength(1));
  // Let the rejection handlers and the reconciliation effect take their turn.
  await act(async () => {});
  expect(draft.fields.getState().harness).toBe("external");
  expect(draft.fields.getState().advancedOverrides).toEqual({ agent: "controller-agent" });
  expect(draft.fields.getState().advancedValues).toEqual({ agent: { value: "controller-agent" } });
});

// A refused catalog read (the hub's forwarded-read pool was full, or the host
// was briefly unreachable) is said, beside a Retry that reads both catalogs
// again: nothing else re-reads them until the host changes.
test("a refused host catalog read says so and Retry reads it again", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  let refuse = true;
  const fake = readyClient((f) =>
    f.on("evener/host/request", (params) => {
      const forwarded = params as HostRequestParams;
      if (refuse && forwarded.method === "evener/harnesses/list") {
        throw new WireError(
          "this connection already has 32 requests waiting on slow answers; try again shortly",
          -32014,
        );
      }
      return routedDiscoveryDefault(forwarded.method);
    }),
  );
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/srv/catalog-refused");
  setDraftField(draft, "source", "buildbox");
  window.history.pushState({}, "", "/new?dir=/srv/catalog-refused");
  renderSpawn(fake);
  await settled();

  const notice = await screen.findByTestId("spawn-host-catalog-error");
  expect(notice.textContent).toContain("try again shortly");

  refuse = false;
  await user.click(within(notice).getByRole("button", { name: "Retry" }));
  await waitFor(() => expect(routedHostCalls(fake, "evener/harnesses/list")).toHaveLength(2));
  await waitFor(() => expect(screen.queryByTestId("spawn-host-catalog-error")).toBeNull());
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const harness = screen.getByLabelText("Harness") as HTMLSelectElement;
  await waitFor(() => expect(within(harness).getByRole("option", { name: "external" })).toBeTruthy());
});

// Each catalog's failure is said on its own, so a host refusing both shows
// both reasons.
test("both refused host catalog reads are said", async () => {
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) =>
    f.on("evener/host/request", (params) => {
      const forwarded = params as HostRequestParams;
      if (forwarded.method === "evener/harnesses/list") throw new WireError("harness list down", -32014);
      if (forwarded.method === "evener/launch/schema") throw new WireError("schema down", -32014);
      return routedDiscoveryDefault(forwarded.method);
    }),
  );
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/srv/catalog-both-refused");
  setDraftField(draft, "source", "buildbox");
  window.history.pushState({}, "", "/new?dir=/srv/catalog-both-refused");
  renderSpawn(fake);
  await settled();

  const notice = await screen.findByTestId("spawn-host-catalog-error");
  await waitFor(() => {
    expect(notice.textContent).toContain("harness list down");
    expect(notice.textContent).toContain("schema down");
  });
});

// Retry reads the launch schema again too, and keeps what the host already
// answered: it is no host change, so the harness list it holds stays offered
// while the retried reads are out.
test("Retry re-reads the launch schema and keeps the answered harness list", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  let refuseSchema = true;
  let harnessReads = 0;
  const fake = readyClient((f) =>
    f.on("evener/host/request", (params) => {
      const forwarded = params as HostRequestParams;
      if (forwarded.method === "evener/launch/schema" && refuseSchema) throw new WireError("schema down", -32014);
      if (forwarded.method === "evener/harnesses/list" && ++harnessReads > 1) {
        return new Promise<HostForwardedResult>(() => {});
      }
      return routedDiscoveryDefault(forwarded.method);
    }),
  );
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/srv/catalog-schema-refused");
  setDraftField(draft, "source", "buildbox");
  window.history.pushState({}, "", "/new?dir=/srv/catalog-schema-refused");
  renderSpawn(fake);
  await settled();

  const notice = await screen.findByTestId("spawn-host-catalog-error");
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const harness = screen.getByLabelText("Harness") as HTMLSelectElement;
  await waitFor(() => expect(within(harness).getByRole("option", { name: "external" })).toBeTruthy());

  refuseSchema = false;
  await user.click(within(notice).getByRole("button", { name: "Retry" }));
  await waitFor(() => expect(routedHostCalls(fake, "evener/launch/schema")).toHaveLength(2));
  await waitFor(() => expect(routedHostCalls(fake, "evener/harnesses/list")).toHaveLength(2));
  await act(async () => {});
  expect(within(harness).getByRole("option", { name: "external" })).toBeTruthy();
  expect(screen.queryByTestId("spawn-host-catalog-error")).toBeNull();
});

// A previous host's catalog read that fails after the host changed says
// nothing about the host now selected.
test("a previous host's late catalog failure shows no notice", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  let failHarnesses!: (err: Error) => void;
  const fake = readyClient((f) =>
    f.on("evener/host/request", (params) => {
      const forwarded = params as HostRequestParams;
      if (forwarded.method === "evener/harnesses/list") {
        return new Promise<HostForwardedResult>((_, reject) => {
          failHarnesses = reject;
        });
      }
      return routedDiscoveryDefault(forwarded.method);
    }),
  );
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/srv/catalog-late-failure");
  setDraftField(draft, "source", "buildbox");
  window.history.pushState({}, "", "/new?dir=/srv/catalog-late-failure");
  renderSpawn(fake);
  await settled();
  await waitFor(() => expect(routedHostCalls(fake, "evener/harnesses/list")).toHaveLength(1));

  await user.selectOptions(screen.getByLabelText("Host"), "local");
  await act(async () => {
    failHarnesses(new WireError("buildbox went away", -32014));
  });
  expect(screen.queryByTestId("spawn-host-catalog-error")).toBeNull();
});

// The other half of the same rule, on a host that has no answers at all: the
// request SETTLED, so the submit gate opens - the host refuses what it cannot
// serve at start rather than leaving Start disabled forever.
test("a host whose catalogs all fail still releases Start", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) =>
    answerRemoteHost(f, {
      fail: ["evener/harnesses/list", "evener/launch/schema"],
      // A credentialed registry, so the only thing that could hold Start here is
      // the catalog gate itself.
      overrides: { "evener/instance/list": { instances: [REMOTE_CREDENTIALED_INSTANCE], availableProviders: [] } },
    }),
  );
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/srv/catalog-answerless");
  setDraftField(draft, "source", "buildbox");
  setDraftField(draft, "model", "openai/gpt-4o");
  window.history.pushState({}, "", "/new?dir=/srv/catalog-answerless");
  renderSpawn(fake);
  await settled();

  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  expect(fake.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({ source: "buildbox" });
});

// An unavailable manifest is not evidence that the draft's host is gone: it is
// empty both while it loads and if it never arrives, so reading that emptiness
// as "fall back to local" silently ran a remote draft's launch on the
// controller. The draft's own source is the last host that was confirmed, and
// it stays the launch AND discovery target until the manifest says otherwise.
test("a remote draft is not silently converted to local while the manifest is unavailable", async () => {
  const user = setupUser();
  const fake = readyClient((f) => readyRemoteHost(f));
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/srv/manifest-gap");
  setDraftField(draft, "source", "buildbox");
  setDraftField(draft, "model", "openai/gpt-4o");
  window.history.pushState({}, "", "/new?dir=/srv/manifest-gap");
  renderSpawn(fake);
  await settled();

  // Every host-scoped call went to the draft's own host...
  await waitFor(() => expect(routedHostCalls(fake, "evener/harnesses/list")).toHaveLength(1));
  // ...and the launch carries it, so it runs on buildbox rather than here.
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  expect(fake.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({ source: "buildbox" });
});

// The submit gate covered the harness/schema answers but not the model list, so
// after a host switch a model chosen from the PREVIOUS host's catalog could be
// submitted while the new host's model list was still in flight. A model is
// host-derived like the harness: the gate has to include its host's settlement.
test("a previous host's model cannot be submitted while the new host's model list is outstanding", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const remoteModelList = deferred<{ data: ModelDescriptor[] }>();
  const fake = readyClient((f) => readyRemoteHost(f, { "model/list": remoteModelList.promise }));
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/host-model-pending");
  // Persisted from, and still valid in, the CONTROLLER's own catalog - it stays
  // non-empty across the switch, so only the gate can hold the submit.
  setDraftField(draft, "model", "openai/gpt-5");
  window.history.pushState({}, "", "/new?dir=/tmp/host-model-pending");
  renderSpawn(fake);
  await settled();
  // A local mount is not host-derived, so it stays startable exactly as before.
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox"));
  // buildbox answered for harnesses/schema, so the catalog gate is open and the
  // model list is the only thing still outstanding.
  await waitFor(() => expect(routedHostCalls(fake, "evener/harnesses/list")).toHaveLength(1));
  expect(draft.fields.getState().model).toBe("openai/gpt-5");
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);
  fireEvent.keyDown(promptField(), { key: "Enter", metaKey: true });
  await act(async () => {});
  expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(0);

  act(() => remoteModelList.resolve({ data: [{ provider: "openai", model: "gpt-5", displayName: "openai/gpt-5" }] }));
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  expect(fake.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({ source: "buildbox" });
});

// The pane's injected validatePath closes over the host selected when the field
// was edited, and the panel accepts the answer on field identity alone - so a
// late controller answer marked (or re-added) an advanced path override after
// the user had switched hosts. The superseded answer must never land: the field
// is judged by the host that is selected NOW.
test("a late path validation from the previous host never marks the field on the new host", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const agentOption: LaunchOption = {
    field: "agent",
    wireField: "agent",
    label: "Agent",
    kind: "text",
    group: "general",
    pathKind: "command",
    perLaunch: true,
  };
  const localValidation = deferred<{ path: string; valid: boolean; error: string }>();
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({ options: [agentOption] }));
    f.on("evener/path/validate", ({ path }) =>
      path === "review-agent" ? localValidation.promise : { path, valid: true },
    );
    // buildbox offers the same field but rejects the value - and has the
    // working directory, so the switch's own resolution of that directory
    // keeps it (only the field's value is refused).
    readyRemoteHost(f, {
      "evener/launch/schema": { options: [agentOption] },
      "evener/path/validate": (params: { path?: string } | undefined) =>
        params?.path === "review-agent"
          ? { path: "review-agent", valid: false, error: "not on buildbox" }
          : { path: params?.path ?? "", valid: true },
    });
  });
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/host-path-validation");
  window.history.pushState({}, "", "/new?dir=/tmp/host-path-validation");
  renderSpawn(fake);
  await settled();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  fireEvent.change(screen.getByLabelText("Agent"), { target: { value: "review-agent" } });
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "evener/path/validate")).toHaveLength(1));

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox"));
  await waitFor(() => expect(routedHostCalls(fake, "evener/harnesses/list")).toHaveLength(1));

  // The controller finally accepts the path - after the user left it.
  await act(async () => localValidation.resolve({ path: "review-agent", valid: true, error: "" }));
  // The value is re-judged by buildbox instead of being marked valid by the
  // machine the user left. Re-pinned for main's landed target-change
  // re-validation (the pathValidationTarget effect): that effect already asks
  // the newly selected host about every stored path value, so the superseded
  // local answer's re-ask is not the only routed call - what this pins is that
  // every routed ask names this field and that the verdict that lands is
  // buildbox's.
  // The routed path questions are exactly two: whether the SELECTED host has
  // the draft's directory (the switch's own resolution of it), and the verdict
  // on the field's value. Any other ask would be a question for a machine the
  // form has left.
  const fieldAsks = routedHostCalls(fake, "evener/path/validate").filter(
    (call) => (call.params as { kind?: string }).kind !== "dir",
  );
  expect(fieldAsks.length).toBeGreaterThan(0);
  for (const call of routedHostCalls(fake, "evener/path/validate")) {
    const params = call.params as { kind?: string };
    expect(call.params).toMatchObject(
      params.kind === "dir" ? { path: draft.cwd, kind: "dir" } : { path: "review-agent", kind: "command" },
    );
  }
  await waitFor(() =>
    expect(draft.fields.getState().advancedValues).toEqual({ agent: { value: "review-agent", invalid: true } }),
  );
  // ...so it cannot ride the launch to a host that does not have it.
  expect(draft.fields.getState().advancedOverrides).toEqual({});
  expect(await screen.findByText("not on buildbox")).toBeTruthy();
});

// The same guard, through the other consumer the finding names: a pathList add
// goes on the validator's "ok" verdict, so a superseded host's late acceptance
// re-added an entry the selected host never accepted.
test("a late pathList validation from the previous host cannot re-add its entry on the new host", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const pathListOption: LaunchOption = {
    field: "pluginDirs",
    wireField: "pluginDirs",
    label: "Plugin dirs",
    kind: "pathList",
    group: "general",
    pathKind: "command",
    perLaunch: true,
    description: "Add a plugin dir",
  };
  const localValidation = deferred<{ path: string; valid: boolean; error: string }>();
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({ options: [pathListOption] }));
    f.on("evener/path/validate", ({ path }) =>
      path === "review-dir" ? localValidation.promise : { path, valid: true },
    );
    // buildbox offers the same field but refuses the entry - and has the
    // working directory, so the switch's own resolution of that directory
    // keeps it (only the entry is refused).
    readyRemoteHost(f, {
      "evener/launch/schema": { options: [pathListOption] },
      "evener/path/validate": (params: { path?: string } | undefined) =>
        params?.path === "review-dir"
          ? { path: "review-dir", valid: false, error: "no such dir on buildbox" }
          : { path: params?.path ?? "", valid: true },
    });
  });
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/host-pathlist");
  window.history.pushState({}, "", "/new?dir=/tmp/host-pathlist");
  renderSpawn(fake);
  await settled();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.type(screen.getByPlaceholderText("Add a plugin dir"), "review-dir");
  await user.click(screen.getByRole("button", { name: "Add" }));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "evener/path/validate")).toHaveLength(1));

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox"));
  await waitFor(() => expect(routedHostCalls(fake, "evener/harnesses/list")).toHaveLength(1));

  // The controller accepts it only after the user has left it.
  await act(async () => localValidation.resolve({ path: "review-dir", valid: true, error: "" }));
  await waitFor(() =>
    expect(
      routedHostCalls(fake, "evener/path/validate").filter(
        (call) => (call.params as { kind?: string }).kind === "command",
      ),
    ).toHaveLength(1),
  );
  // buildbox refused it, so the entry never joins the draft's list - and so
  // never rides the launch to a host that never accepted it. (Its message is
  // the add row's own transient feedback; the host switch repopulates the
  // panel's fields from the new host's schema, so only the draft state here is
  // durable.)
  expect(draft.fields.getState().advancedOverrides).toEqual({});
  expect(draft.fields.getState().advancedValues).toEqual({});
});

// The picker's last-working-dir seed is controller-wide: opening a REMOTE picker
// at a path the controller last accepted validates a directory that need not
// exist on the selected host. Only a local launch may seed from it.
test("a remote directory picker does not start from the controller's last working directory", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  localStorage.setItem(LAST_WORKING_DIR_KEY, "/home/controller/last");
  const fake = readyClient((f) =>
    readyRemoteHost(f, { "evener/path/validate": { path: "/home/buildbox", valid: true } }),
  );
  connectionStore.getState().connect(fake);
  // No working directory yet, so the fallback seed is what browsing starts from.
  const draft = selectSpawnDirectory("");
  setDraftField(draft, "source", "buildbox");
  window.history.pushState({}, "", "/new");
  renderSpawn(fake);
  await settled();

  await user.click(workingDir());
  await waitFor(() => expect(routedHostCalls(fake, "evener/path/validate")).toHaveLength(1));
  expect(routedHostCalls(fake, "evener/path/validate")[0]?.params).toMatchObject({ path: "~" });
});

// --- round six -------------------------------------------------------------

// A host's two catalogs answer and fail independently. Round five separated
// "the request settled" from "the answer arrived", but stamped one shared "host
// settled" value and only when BOTH answered - so a host that answered one
// catalog and refused the other reconciled NEITHER half, and the draft's
// harness rode thread/start to a host whose own harness list omits it
// (component 07b review, round six). Here buildbox answers
// evener/harnesses/list (with only "evener") and refuses evener/launch/schema:
// the answered harness half must drop "external", while the half whose request
// never answered leaves the Advanced-options maps alone (the round-five rule).
test("a host that answers its harness list reconciles the draft even when its schema rejects", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) =>
    answerRemoteHost(f, {
      // buildbox's own evener/harnesses/list answers with only "evener"; its
      // evener/launch/schema never answers at all.
      fail: ["evener/launch/schema"],
      // A credentialed registry, so nothing but the catalog gate can hold Start.
      overrides: { "evener/instance/list": { instances: [REMOTE_CREDENTIALED_INSTANCE], availableProviders: [] } },
    }),
  );
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/srv/partial-harness");
  setDraftField(draft, "source", "buildbox");
  setDraftField(draft, "harness", "external");
  setDraftField(draft, "model", "openai/gpt-4o");
  setDraftField(draft, "advancedOverrides", { agent: "controller-agent" });
  setDraftField(draft, "advancedValues", { agent: { value: "controller-agent" } });
  window.history.pushState({}, "", "/new?dir=/srv/partial-harness");
  renderSpawn(fake);
  await settled();

  // The answered catalog does not offer "external", so the draft drops it...
  await waitFor(() => expect(draft.fields.getState().harness).toBe(""));
  // ...while the half whose request never answered is untouched.
  expect(draft.fields.getState().advancedOverrides).toEqual({ agent: "controller-agent" });
  expect(draft.fields.getState().advancedValues).toEqual({ agent: { value: "controller-agent" } });

  // Start is released by the settlement (round five), and the launch now
  // carries no harness that only the controller offers.
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  const started = fake.calls.find((entry) => entry.method === "thread/start");
  expect(started?.params).toMatchObject({ source: "buildbox" });
  expect(started?.params).not.toHaveProperty("harness");
});

// The mirror case: the schema answers without the draft's Advanced-options
// field while the harness list refuses. The answered schema half reconciles
// (dropping "agent"), and the unanswered harness half keeps the draft's
// harness - a request that never answered is still not evidence about what the
// host offers.
test("a host that answers its schema reconciles the advanced options even when its harness list rejects", async () => {
  seedSources(REMOTE_SOURCES);
  const keptOption: LaunchOption = {
    field: "maxRounds",
    wireField: "maxRounds",
    label: "Max rounds",
    group: "general",
    kind: "text",
    perLaunch: true,
  };
  const fake = readyClient((f) =>
    answerRemoteHost(f, {
      // buildbox's own schema offers maxRounds but not agent; its harness list
      // never answers.
      fail: ["evener/harnesses/list"],
      overrides: { "evener/launch/schema": { options: [keptOption] } },
    }),
  );
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/srv/partial-schema");
  setDraftField(draft, "source", "buildbox");
  setDraftField(draft, "harness", "external");
  setDraftField(draft, "model", "openai/gpt-4o");
  setDraftField(draft, "advancedOverrides", { agent: "controller-agent", maxRounds: 7 });
  setDraftField(draft, "advancedValues", {
    agent: { value: "controller-agent" },
    maxRounds: { value: "7" },
  });
  window.history.pushState({}, "", "/new?dir=/srv/partial-schema");
  renderSpawn(fake);
  await settled();

  // The answered schema omits "agent", so its override and value drop...
  await waitFor(() => expect(draft.fields.getState().advancedOverrides).toEqual({ maxRounds: 7 }));
  expect(draft.fields.getState().advancedValues).toEqual({ maxRounds: { value: "7" } });
  // ...while the harness whose request never answered stays put.
  expect(draft.fields.getState().harness).toBe("external");
});

// --- round seven -----------------------------------------------------------

/** Every "create this directory" the pane issued, naming the host it was
 * addressed to: the plain call is the controller's, a forwarded one carries the
 * host it was scoped to. */
function createDirCallHosts(fake: FakeClient): string[] {
  const hosts: string[] = [];
  for (const call of fake.calls) {
    if (call.method === "evener/dirs/create") hosts.push("local");
    else if (
      call.method === "evener/host/request" &&
      (call.params as HostRequestParams).method === "evener/dirs/create"
    ) {
      hosts.push((call.params as HostRequestParams).host);
    }
  }
  return hosts;
}

// The "Create directory?" confirmation is an action against ONE host: buildbox
// validated the path and supplied the draft's launch config, so the pending
// create belongs to buildbox. The manifest's `online` flag is live, and an
// offline source makes hostChoice fall back to "local" (see its derivation)
// without touching createDialogPath - so before round seven the open dialog's
// "Create & start" created the path and started the session on the host that
// took over, with a draft that host never offered. A host switch now dismisses
// the pending create instead of re-homing it.
test("a host that drops offline while the create dialog is open never creates or starts on the host that took over", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) =>
    readyRemoteHost(f, { "evener/path/validate": { path: "/srv/taken-over", valid: false } }),
  );
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/srv/taken-over");
  setDraftField(draft, "source", "buildbox");
  setDraftField(draft, "model", "openai/gpt-4o");
  window.history.pushState({}, "", "/new?dir=/srv/taken-over");
  renderSpawn(fake);
  await settled();

  // buildbox says the directory does not exist, so the pane offers to create it
  // - on buildbox, which is also where the draft's model came from.
  await user.click(screen.getByTestId("spawn-submit"));
  expect(await screen.findByRole("dialog", { name: "Create directory?" })).toBeTruthy();

  await act(async () => {
    seedSources([
      { id: "local", label: "Local", kind: "local", online: true },
      { id: "buildbox", label: "buildbox", kind: "ssh", online: false },
    ]);
  });
  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("local");

  // The confirmation went with the host it was bound to...
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(draft.fields.getState()).toMatchObject({ createDialogPath: null, createDialogHost: null });
  // ...so nothing was created anywhere and nothing was started, on either host.
  expect(createDirCallHosts(fake)).toEqual([]);
  expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(0);
  // Re-pinned to main's landed offline-fallback write-back (component 06b
  // review, round nine - see "a host that returns online after its offline
  // fallback stays on local"): the picker's fallback is recorded in the draft so
  // the select and the launch target never diverge, which supersedes the branch's
  // round-five "the stale draft value stays until the picker changes it". The
  // draft's other config is untouched, and what round seven still guarantees -
  // no create and no start on the host that took over - is asserted above.
  expect(draft.fields.getState().source).toBe("local");
});

// The binding is re-checked at the confirm, not only by the switch effect: a
// dialog restored from the draft (a remount) can open onto a selection that has
// moved since it was preflighted, and that confirm must not fall back to the
// host selected now. Here buildbox went offline while the pane was unmounted,
// so the restored dialog is bound to buildbox while hostChoice is "local".
test("a restored create dialog whose preflight host is no longer selected aborts instead of creating the path", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: false },
  ]);
  const fake = readyClient();
  const draft = selectSpawnDirectory("/srv/stale-create");
  setDraftField(draft, "source", "buildbox");
  setDraftField(draft, "createDialogPath", "/srv/stale-create");
  setDraftField(draft, "createDialogHost", "buildbox");
  window.history.pushState({}, "", "/new?dir=/srv/stale-create");
  renderSpawn(fake);
  await settled();

  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("local");
  await user.click(await screen.findByRole("button", { name: "Create & start" }));

  expect(createDirCallHosts(fake)).toEqual([]);
  expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(0);
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(draft.fields.getState()).toMatchObject({ createDialogPath: null, createDialogHost: null });
  await waitFor(() => expect(toastKinds()).toEqual(["error"]));
});

// The other half of handleSpawn's gate: the host that preflighted the path is
// still selected, but its catalogs have not answered, so the draft's launch
// config has not been reconciled against them. The confirmation is held (the
// binding is intact, so the dialog stays for the next click) and releases on the
// answers - settlement, not success (round five), so it can never dead-end.
test("a create confirmation waits for the selected host's catalogs before creating and starting", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  let releaseHarnesses!: (result: HostForwardedResult) => void;
  const fake = readyClient((f) =>
    readyRemoteHost(f, {
      "evener/harnesses/list": new Promise<HostForwardedResult>((resolve) => {
        releaseHarnesses = resolve;
      }),
    }),
  );
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/srv/unsettled-create");
  setDraftField(draft, "source", "buildbox");
  setDraftField(draft, "model", "openai/gpt-4o");
  setDraftField(draft, "createDialogPath", "/srv/unsettled-create");
  setDraftField(draft, "createDialogHost", "buildbox");
  window.history.pushState({}, "", "/new?dir=/srv/unsettled-create");
  renderSpawn(fake);
  await settled();

  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox");
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);
  await user.click(screen.getByRole("button", { name: "Create & start" }));

  expect(createDirCallHosts(fake)).toEqual([]);
  expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(0);
  expect(screen.getByRole("dialog", { name: "Create directory?" })).toBeTruthy();

  act(() => releaseHarnesses({ data: [{ id: "evener", label: "evener", kind: "evener" }] }));
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));

  await user.click(screen.getByRole("button", { name: "Create & start" }));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  // The create and the launch both still name the host the path was preflighted
  // on, with the draft that host offered.
  expect(createDirCallHosts(fake)).toEqual(["buildbox"]);
  expect(fake.calls.find((entry) => entry.method === "thread/start")?.params).toMatchObject({
    source: "buildbox",
    cwd: "/srv/unsettled-create",
  });
  expect(screen.queryByRole("dialog")).toBeNull();
});
