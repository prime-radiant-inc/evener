import { deferred } from "@evener/appwire-client/testing/deferred";
import "../session/testing/editorGeometry";
import type { HostForwardedResult, HostRequestParams, ThreadStartParams } from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import type { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { connectionStore } from "../../stores/connection";
import { navigationStore } from "../../stores/navigation/store";
import { getToasts } from "../../widgets/toast/store";
import { CONNECT_ATTACH_TIMEOUT_MS } from "./Spawn";
import { selectSpawnDirectory, setDraftField, spawnDraftsStore } from "./spawnDrafts";
import {
  completionDraft,
  expectModelReadsDefault,
  expectWorkingDir,
  fillPrompt,
  installSpawnTestHooks,
  modelTrigger,
  promptField,
  readyClient,
  renderSpawn,
  routedDiscoveryDefault,
  SPAWN_PLUGIN_PREVIEW,
  seedSources,
  settled,
  setupUser,
  setWorkingDir,
  workingDir,
} from "./spawnTestUtils";

/** Every evener/host/attach the pane issued, for the picker's attach assertions. */
function attachCalls(fake: FakeClient): { host: string }[] {
  return fake.calls
    .filter((call) => call.method === "evener/host/attach")
    .map((call) => call.params as { host: string });
}
installSpawnTestHooks();

// --- host picker (Component 06b) -------------------------------------------

test("host picker lists sources, preselects local, and disables offline hosts", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
    { id: "offline-host", label: "offline-host", kind: "ssh", online: false },
  ]);
  renderSpawn(readyClient());
  await settled();

  const picker = screen.getByLabelText("Host") as HTMLSelectElement;
  expect(picker.value).toBe("local");
  const options = within(picker).getAllByRole("option") as HTMLOptionElement[];
  expect(options.map((option) => option.value)).toEqual(["local", "buildbox", "offline-host"]);
  expect((options.find((option) => option.value === "buildbox") as HTMLOptionElement).disabled).toBe(false);
  const offline = options.find((option) => option.value === "offline-host") as HTMLOptionElement;
  expect(offline.disabled).toBe(true);
  // The reason is in the option's own accessible text, not only a tooltip.
  expect(offline.textContent).toContain("offline");
});

// The Host row sits above the working directory: the folder list, recents,
// and validation all come from the selected machine (hostRequest), so the
// form reads pick-the-machine first, then the folder on it. Asserted in DOM
// order, not visually, so keyboard focus follows the same path.
test("the host row renders above the working directory", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  renderSpawn(readyClient());
  await settled();

  const host = screen.getByLabelText("Host");
  const dir = workingDir();
  expect(host.compareDocumentPosition(dir) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
});

// Selecting an already-online remote host is NOT an attach request: the
// manifest reports the host online (its channel is attached), so the picker must
// not spend a redundant evener/host/attach on it. Only a host the manifest
// reports offline needs the dial, and that row's option is disabled, so the
// Connect affordance below is the path that reaches it. Before the fix every
// picker selection dialed, online or not.
test("selecting an already-online remote host issues no attach call", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await settled();

  // The selection still moves the draft - and so the launch target - while
  // issuing no attach call at all.
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox"));
  expect(attachCalls(fake)).toEqual([]);
});

// Two rapid activations of one Connect must dial once. The in-flight check
// cannot read `connectingHosts` state: setConnectingHosts is asynchronous, so
// two activations in the same batch both read the pre-update set and dial
// twice. The guard is a ref, mutated synchronously before the request, so the
// second activation sees the first one still in flight.
test("a rapid double Connect dials the host once", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "offline-host", label: "offline-host", kind: "ssh", online: false },
  ]);
  // A never-resolving first dial keeps it in flight while the second activation
  // is dispatched.
  let release!: () => void;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  const fake = readyClient((f) => {
    f.on("evener/host/attach", async () => {
      await gate;
      return { attached: true };
    });
  });
  renderSpawn(fake);
  await settled();

  const connect = screen.getByRole("button", { name: "Connect offline-host" }) as HTMLButtonElement;
  act(() => {
    connect.click();
    connect.click();
  });

  expect(attachCalls(fake)).toHaveLength(1);
  release();
  await waitFor(() => expect(screen.queryByRole("button", { name: /Connecting offline-host/ })).toBeNull());
});

// Changing the host select never dials: an online row is already attached (a
// dial would only be redundant), and an offline row's option is disabled, so a
// select event cannot name it. The Connect button below is the single attach
// path. Before the fix the onChange branch dialed any selected offline row, a
// latent double-attach if the disabled rendering were ever relaxed.
test("changing the host select issues no attach call", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
    { id: "offline-host", label: "offline-host", kind: "ssh", online: false },
  ]);
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  // An online selection moves the draft without an attach.
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await settled();
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox"));
  expect(attachCalls(fake)).toEqual([]);

  // Even a programmatic change to an offline row — unreachable through the
  // disabled option in the real UI — still issues no attach: the Connect
  // button owns that path.
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "offline-host" } });
  await settled();
  expect(attachCalls(fake)).toEqual([]);

  // The Connect button still attaches the offline host.
  fireEvent.click(screen.getByRole("button", { name: "Connect offline-host" }));
  await settled();
  await waitFor(() =>
    expect(
      fake.calls.some(
        (call) => call.method === "evener/host/attach" && (call.params as { host: string }).host === "offline-host",
      ),
    ).toBe(true),
  );
});

// A never-attached host is listed offline with a disabled spawn option
// (component 06b), so the picker must offer a concrete, enabled Connect
// affordance for it — otherwise a configured [[hosts]] entry is dead UI. The
// Connect action issues the same evener/host/attach call.
test("an offline host offers an enabled Connect action that attaches it", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "offline-host", label: "offline-host", kind: "ssh", online: false },
  ]);
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  const picker = screen.getByLabelText("Host") as HTMLSelectElement;
  const offline = within(picker).getByRole("option", { name: /offline-host/ }) as HTMLOptionElement;
  expect(offline.disabled).toBe(true);

  const connect = screen.getByRole("button", { name: "Connect offline-host" }) as HTMLButtonElement;
  expect(connect.disabled).toBe(false);
  fireEvent.click(connect);
  await settled();

  await waitFor(() =>
    expect(
      fake.calls.some(
        (call) => call.method === "evener/host/attach" && (call.params as { host: string }).host === "offline-host",
      ),
    ).toBe(true),
  );
});

// A Connect failure is surfaced, never a silent no-op: the row stays offline
// and the user sees the manager's error.
test("a failed Connect surfaces the attach error", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "offline-host", label: "offline-host", kind: "ssh", online: false },
  ]);
  const fake = readyClient((f) => {
    f.on("evener/host/attach", () => {
      throw new Error("host is unreachable");
    });
  });
  renderSpawn(fake);
  await settled();

  fireEvent.click(screen.getByRole("button", { name: "Connect offline-host" }));
  expect(await screen.findByText(/Connect offline-host failed/i)).toBeTruthy();
});

// The Connect action must survive a slow server-side attach: the Ensure seam
// behind `evener/host/attach` spends sequential bounded phases (deploy,
// restart, launch-contract refresh — 10 minutes each) that the AppWire
// client's 30s default request timeout does not cover, so a deploy that would
// succeed arrives after the client already rejected and the user sees a failed
// toast for a host that is online. The Connect call therefore carries an
// explicit attach timeout derived from the manager bound, and a slow attach
// resolving inside it succeeds with no failure toast. Before the fix the call
// passed no timeout option, so the default applied.
test("a slow Connect attach resolves inside the attach timeout with no failure toast", async () => {
  expect(CONNECT_ATTACH_TIMEOUT_MS).toBeGreaterThan(30_000);
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "offline-host", label: "offline-host", kind: "ssh", online: false },
  ]);
  let release!: () => void;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  const fake = readyClient((f) => {
    f.on("evener/host/attach", async () => {
      // Gated past the assertion below, so on the old code — where the call
      // carried no timeout option — the waitFor already failed before the
      // server answers; on the fixed code the call carries the attach timeout
      // and the late success clears the pending marker with no failure toast.
      await gate;
      return { attached: true };
    });
  });
  renderSpawn(fake);
  await settled();

  fireEvent.click(screen.getByRole("button", { name: "Connect offline-host" }));
  // The attach RPC carries the explicit attach timeout, not the 30s default.
  await waitFor(() =>
    expect(
      fake.calls.some(
        (call) => call.method === "evener/host/attach" && call.opts?.timeoutMs === CONNECT_ATTACH_TIMEOUT_MS,
      ),
    ).toBe(true),
  );
  release();
  await waitFor(() => expect(screen.queryByRole("button", { name: /Connecting offline-host/ })).toBeNull());
  expect(screen.queryByText(/Connect offline-host failed/i)).toBeNull();
});

test("no host picker renders when the manifest has only local", async () => {
  seedSources([{ id: "local", label: "Local", kind: "local", online: true }]);
  renderSpawn(readyClient());
  await settled();

  expect(screen.queryByLabelText("Host")).toBeNull();
});

test("choosing a non-local host sends source and persists it in the draft", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient();
  window.history.pushState({}, "", "/new?dir=/tmp/host-target");
  renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await fillPrompt(user, "run on buildbox");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  expect(completionDraft("/tmp/host-target").fields.getState().source).toBe("buildbox");
});

// --- host picker: the working directory is resolved over the selected host ---

// The working directory a new session is seeded with belongs to the machine the
// session starts on. Switching the Host picker re-resolves it over the SELECTED
// host alone: that host is asked whether it has the directory, and a refusal
// re-seeds it from that host's own default - its home, resolved over THAT host
// through evener/path/validate's "~" expansion - so neither the controller's
// default nor the previous host's value can ride to a machine that does not
// have it.
/** A host-routed path/validate call: the resolution a switch is made of. */
function routedPathValidations(fake: FakeClient, host: string): { path?: string }[] {
  return fake.calls
    .filter(
      (call) =>
        call.method === "evener/host/request" &&
        (call.params as HostRequestParams).host === host &&
        (call.params as HostRequestParams).method === "evener/path/validate",
    )
    .map((call) => (call.params as HostRequestParams).params as { path?: string });
}

/** readyClient for a host that has only its own home: its path/validate answers
 * "~" with `home` and refuses every other path, the way a real host answers for
 * a directory that belongs to another machine. */
function readyClientWithRemoteHome(home: string): FakeClient {
  return readyClient((f) => {
    f.on("evener/host/request", (params) => {
      const forwarded = params as HostRequestParams;
      if (forwarded.method === "evener/path/validate") {
        const path = (forwarded.params as { path?: string } | undefined)?.path;
        return path === "~"
          ? { path: home, valid: true }
          : { path: path ?? "", valid: false, error: "no such file or directory" };
      }
      return routedDiscoveryDefault(forwarded.method);
    });
  });
}

test("switching the host re-seeds the working directory from the selected host", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  // The controller's default working directory (spawnDefaults'
  // GLOBAL_WORKING_DIR_KEY): what a fresh /new seeds when the URL names no dir.
  localStorage.setItem("evener-hub.spawn-defaults.global.working_dir", "/tmp/controller-default");
  const fake = readyClientWithRemoteHome("/home/buildbox");
  window.history.pushState({}, "", "/new");
  renderSpawn(fake);
  await settled();
  expectWorkingDir("/tmp/controller-default");

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await settled();

  // The switch asks the SELECTED host about the controller's directory...
  expect(routedPathValidations(fake, "buildbox").some((params) => params.path === "/tmp/controller-default")).toBe(
    true,
  );
  // ...and, refused, resolves that host's own home over that host. The
  // directory shown and seeded for the new session is the host's.
  expect(routedPathValidations(fake, "buildbox").some((params) => params.path === "~")).toBe(true);
  expectWorkingDir("/home/buildbox");
  expect(workingDir().textContent).not.toContain("/tmp/controller-default");
});

test("switching back to local restores the controller's own default directory", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  localStorage.setItem("evener-hub.spawn-defaults.global.working_dir", "/tmp/controller-default");
  const fake = readyClientWithRemoteHome("/home/buildbox");
  // ...and that home is not a directory on the controller either.
  fake.on("evener/path/validate", ({ path }) =>
    path === "/home/buildbox" ? { path, valid: false, error: "no such file or directory" } : { path: "", valid: true },
  );
  window.history.pushState({}, "", "/new");
  renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await settled();
  expectWorkingDir("/home/buildbox");

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "local" } });
  await settled();

  // The reverse switch resolves the directory over the local host too: refused
  // here, it re-seeds the controller's own default - the same value a fresh
  // local mount seeds - and never keeps the previous host's path.
  expectWorkingDir("/tmp/controller-default");
  expect(workingDir().textContent).not.toContain("/home/buildbox");
  // Local keeps its current behavior: its own resolution is the plain call,
  // never a forwarded one.
  expect(routedPathValidations(fake, "local")).toEqual([]);
});

test("a host switch moves the draft's typed state to the selected host's directory", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClientWithRemoteHome("/home/buildbox");
  window.history.pushState({}, "", "/new?dir=/tmp/typed-before-switch");
  renderSpawn(fake);
  await settled();
  await fillPrompt(user, "typed before the switch");

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await settled();
  expectWorkingDir("/home/buildbox");

  // The re-seed moves the draft to the selected host's directory: the person's
  // typed state travels with it (a reconnect/host move is not a discard).
  expect(promptField().textContent).toBe("typed before the switch");
  expect(completionDraft("/home/buildbox").fields.getState().prompt).toBe("typed before the switch");
  expect(spawnDraftsStore.getState().drafts.has("/tmp/typed-before-switch")).toBe(false);
});

test("a same-host re-read neither re-resolves nor discards the working directory", async () => {
  const sources = [
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ] as const;
  seedSources([...sources]);
  const fake = readyClientWithRemoteHome("/home/buildbox");
  window.history.pushState({}, "", "/new?dir=/tmp/reconnect-a");
  const mounted = renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await settled();
  expectWorkingDir("/home/buildbox");
  const resolutions = routedPathValidations(fake, "buildbox").filter((params) => params.path === "~").length;
  expect(resolutions).toBe(1);

  // A reconnect re-seeds the navigation store and the manifest revalidates;
  // the host itself does not change. That is not a switch: nothing re-resolves
  // the default, and the directory stands.
  await act(async () => {
    seedSources([...sources], { loading: true });
  });
  await act(async () => {
    seedSources([...sources]);
  });
  await settled();
  expect(routedPathValidations(fake, "buildbox").filter((params) => params.path === "~")).toHaveLength(resolutions);
  expectWorkingDir("/home/buildbox");

  // A pane remount (the same draft restored) is not a switch either.
  mounted.unmount();
  renderSpawn(fake);
  await settled();
  expectWorkingDir("/home/buildbox");
  expect(routedPathValidations(fake, "buildbox").filter((params) => params.path === "~")).toHaveLength(resolutions);
});

// The other half of "a reconnect is not a switch": a re-VISIT to the same host
// IS a switch, and a new question. An answer to an earlier visit's question -
// issued for this same host and this same draft - describes a moment the form
// has left, so it must not land: only the newest issuance's answer decides.
test("a stale answer from an earlier visit to the same host cannot move the directory", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  // The FIRST buildbox question about this directory hangs; every later one
  // answers the way a host that has the directory does. The hanging answer is a
  // refusal, which late would re-seed the form from buildbox's home.
  const firstVisit = deferred<{ path: string; valid: boolean; error?: string }>();
  let buildboxDirectoryAsks = 0;
  const fake = readyClient((f) => {
    f.on("evener/host/request", (params) => {
      const forwarded = params as HostRequestParams;
      if (forwarded.method !== "evener/path/validate") return routedDiscoveryDefault(forwarded.method);
      const path = (forwarded.params as { path?: string } | undefined)?.path;
      if (path === "/tmp/visit-a") {
        buildboxDirectoryAsks++;
        return buildboxDirectoryAsks === 1 ? firstVisit.promise : { path, valid: true };
      }
      if (path === "~") return { path: "/home/buildbox", valid: true };
      return { path: path ?? "", valid: false, error: "no such file or directory" };
    });
  });
  window.history.pushState({}, "", "/new?dir=/tmp/visit-a");
  renderSpawn(fake);
  await settled();

  // local -> buildbox: this first question hangs.
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await settled();
  expect(buildboxDirectoryAsks).toBe(1);
  // buildbox -> local: the local host has the directory, so it stays.
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "local" } });
  await settled();
  // local -> buildbox again: the second question answers "this host has it".
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await settled();
  expect(buildboxDirectoryAsks).toBe(2);
  expectWorkingDir("/tmp/visit-a");

  // The first visit's refusal finally lands. It is not this switch's answer.
  await act(async () => {
    firstVisit.resolve({ path: "/tmp/visit-a", valid: false, error: "no such file or directory" });
  });
  await settled();
  expectWorkingDir("/tmp/visit-a");
  // Nothing re-seeded: only the latest issuance's answer landed, so the host's
  // own home was never resolved over it.
  expect(routedPathValidations(fake, "buildbox").filter((params) => params.path === "~")).toEqual([]);
});

// The guard belongs to the form INSTANCE, not to the draft store: the store is
// module scope and outlives the pane, so a validation still in flight when the
// pane goes away (a route change) must not seed the form a later mount shows
// for the same draft.
test("an answer still in flight when the pane unmounts cannot seed the remount", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const held = deferred<{ path: string; valid: boolean; error?: string }>();
  let heldOnce = false;
  const fake = readyClient((f) => {
    f.on("evener/host/request", (params) => {
      const forwarded = params as HostRequestParams;
      if (forwarded.method !== "evener/path/validate") return routedDiscoveryDefault(forwarded.method);
      const path = (forwarded.params as { path?: string } | undefined)?.path;
      if (path === "/tmp/unmount-a" && !heldOnce) {
        heldOnce = true;
        return held.promise;
      }
      if (path === "~") return { path: "/home/buildbox", valid: true };
      return { path: path ?? "", valid: false, error: "no such file or directory" };
    });
  });
  window.history.pushState({}, "", "/new?dir=/tmp/unmount-a");
  const mounted = renderSpawn(fake);
  await settled();

  // local -> buildbox: this question hangs.
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await settled();

  // The pane goes away while the answer is out, and comes back on the same
  // draft: a fresh mount's first run only records, so a host+draft check alone
  // would let the dead instance's answer through.
  mounted.unmount();
  renderSpawn(fake);
  await settled();
  expectWorkingDir("/tmp/unmount-a");

  // The answer finally lands. It belongs to a form that no longer exists.
  await act(async () => {
    held.resolve({ path: "/tmp/unmount-a", valid: false, error: "no such file or directory" });
  });
  await settled();
  expectWorkingDir("/tmp/unmount-a");
  expect(routedPathValidations(fake, "buildbox").filter((params) => params.path === "~")).toEqual([]);
});

// Drafts are keyed by directory and outlive a draft transition: picking another
// directory and then back returns the SAME draft object. An answer issued
// before that round trip must not pass for the draft that came back - only the
// question asked for the visit it belongs to may decide.
test("an answer issued before a directory round trip cannot seed the returned draft", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const held = deferred<{ path: string; valid: boolean; error?: string }>();
  let heldOnce = false;
  const fake = readyClient((f) => {
    f.on("evener/host/request", (params) => {
      const forwarded = params as HostRequestParams;
      if (forwarded.method !== "evener/path/validate") return routedDiscoveryDefault(forwarded.method);
      const path = (forwarded.params as { path?: string } | undefined)?.path;
      if (path === "/tmp/trip-a" && !heldOnce) {
        heldOnce = true;
        return held.promise;
      }
      if (path === "~") return { path: "/home/buildbox", valid: true };
      if (path === "/tmp/trip-a" || path === "/tmp/trip-b") return { path, valid: true };
      return { path: path ?? "", valid: false, error: "no such file or directory" };
    });
  });
  window.history.pushState({}, "", "/new?dir=/tmp/trip-a");
  renderSpawn(fake);
  await settled();

  // local -> buildbox: this question about trip-a hangs.
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await settled();
  expectWorkingDir("/tmp/trip-a");

  // The person visits another directory and comes back to the first one, which
  // returns the original draft.
  await setWorkingDir(user, "/tmp/trip-b");
  await settled();
  await setWorkingDir(user, "/tmp/trip-a");
  await settled();
  expectWorkingDir("/tmp/trip-a");

  // The trip's question finally answers - with a refusal, which late would
  // re-seed the returned draft from buildbox's home.
  await act(async () => {
    held.resolve({ path: "/tmp/trip-a", valid: false, error: "no such file or directory" });
  });
  await settled();
  expectWorkingDir("/tmp/trip-a");
  expect(routedPathValidations(fake, "buildbox").filter((params) => params.path === "~")).toEqual([]);
});

// The re-seed moves the live draft onto a key another draft may already occupy
// (a directory visited earlier by a draft that has since moved on). The live
// draft wins the key - the person's current work must not be dropped for stale
// state - and the displaced draft becomes unreachable: drafts are keyed by the
// directory, and nothing else holds a reference to it. Pinned as the
// deliberate tradeoff rather than inventing storage the pane does not have.
test("a re-seed onto an occupied directory keeps the live draft", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClientWithRemoteHome("/home/buildbox");
  // An earlier visit's draft for the host's home directory...
  const displaced = selectSpawnDirectory("/home/buildbox");
  setDraftField(displaced, "prompt", "stale home draft");
  window.history.pushState({}, "", "/new?dir=/tmp/collide-a");
  renderSpawn(fake);
  await settled();
  await fillPrompt(user, "live work");

  // ...and the live draft, which the switch re-seeds onto that same key.
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await settled();

  expectWorkingDir("/home/buildbox");
  expect(promptField().textContent).toBe("live work");
  expect(completionDraft("/home/buildbox").fields.getState().prompt).toBe("live work");
  expect(spawnDraftsStore.getState().drafts.get("/home/buildbox")).toBe(spawnDraftsStore.getState().current);
  // The displaced draft is unreachable: no key resolves to it any more.
  expect([...spawnDraftsStore.getState().drafts.values()]).not.toContain(displaced);
});

// A picked directory is the person's own choice, and it opens that directory's
// own draft (drafts are keyed by the exact directory). Selecting another draft
// is not a switch of the selected host, so the picked directory's draft keeps
// the directory the person just chose - the re-seed belongs to a host change
// WITHIN one draft, never to a draft change.
test("a directory pick does not re-seed the picked directory's own draft", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient((f) => {
    f.on("evener/host/request", (params) => {
      const forwarded = params as HostRequestParams;
      if (forwarded.method === "evener/path/validate") {
        const path = (forwarded.params as { path?: string } | undefined)?.path;
        if (path === "~") return { path: "/home/buildbox", valid: true };
        // The prefill's directory is not on this host; the one the person
        // picks is.
        if (path === "/tmp/pick-a") return { path, valid: false, error: "no such file or directory" };
        return { path: path ?? "", valid: true };
      }
      return routedDiscoveryDefault(forwarded.method);
    });
  });
  window.history.pushState({}, "", "/new?dir=/tmp/pick-a");
  renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await settled();
  expectWorkingDir("/home/buildbox");

  await setWorkingDir(user, "/srv/app");
  await settled();
  expectWorkingDir("/srv/app");
});

// The other half of the same rule: a directory the SELECTED host itself
// resolves as one it has is kept. It is no longer the previous host's value -
// it is the machine the session starts on saying it can start there - and no
// home is resolved over the host.
test("a switch keeps a directory the selected host also has", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient((f) => {
    f.on("evener/host/request", (params) => {
      const forwarded = params as HostRequestParams;
      if (forwarded.method === "evener/path/validate") {
        const path = (forwarded.params as { path?: string } | undefined)?.path;
        return path === "/tmp/shared-repo"
          ? { path, valid: true }
          : { path: path ?? "", valid: false, error: "no such file or directory" };
      }
      return routedDiscoveryDefault(forwarded.method);
    });
  });
  window.history.pushState({}, "", "/new?dir=/tmp/shared-repo");
  renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await settled();

  expect(routedPathValidations(fake, "buildbox").some((params) => params.path === "/tmp/shared-repo")).toBe(true);
  expect(routedPathValidations(fake, "buildbox").some((params) => params.path === "~")).toBe(false);
  expectWorkingDir("/tmp/shared-repo");
});

// A host that cannot answer keeps the directory: unknown is not absent (the
// same rule the phone's New session follows when it moves between hosts), and
// the submit's own host-routed preflight is the authority on the path then.
test("a host that cannot answer keeps the working directory", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient((f) => {
    f.on("evener/host/request", (params) => {
      const forwarded = params as HostRequestParams;
      if (forwarded.method === "evener/path/validate") throw new Error("host went away");
      return routedDiscoveryDefault(forwarded.method);
    });
  });
  window.history.pushState({}, "", "/new?dir=/tmp/away-a");
  renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await settled();

  expectWorkingDir("/tmp/away-a");
});

// The last resort of the re-seed: the host refused the directory, and its own
// home cannot be resolved either - refused, or a request that throws. The form
// must not keep a path that belongs to another machine, so it seeds NO
// directory: the picker already opens at the selected host's home there, and
// no launch carries a foreign path.
test.each([
  ["refuses it", () => ({ path: "", valid: false, error: "no such file or directory" })],
  [
    "cannot answer for it",
    () => {
      throw new Error("host went away");
    },
  ],
])("an unresolvable host default seeds no directory (%s)", async (_case, answerHome) => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient((f) => {
    f.on("evener/host/request", (params) => {
      const forwarded = params as HostRequestParams;
      if (forwarded.method !== "evener/path/validate") return routedDiscoveryDefault(forwarded.method);
      const path = (forwarded.params as { path?: string } | undefined)?.path;
      if (path === "~") return answerHome() as HostForwardedResult;
      return { path: path ?? "", valid: false, error: "no such file or directory" };
    });
  });
  window.history.pushState({}, "", "/new?dir=/tmp/foreign-a");
  renderSpawn(fake);
  await settled();
  expectWorkingDir("/tmp/foreign-a");

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await settled();

  // Nothing of the previous host's path is kept, and the form says so.
  expect(workingDir().textContent).not.toContain("/tmp/foreign-a");
  expect(workingDir().dataset.state).toBe("unset");
  expect(spawnDraftsStore.getState().current?.cwd).toBe("");
});

// `path` is typed non-optional, but evener/host/request hands a remote host's
// answer back through a cast, so an answer that is valid and names no directory
// (an older host, a malformed one) reaches this code as undefined. It must seed
// no directory - never a non-string key in the string-keyed drafts map.
test("a host answering valid with no path seeds no directory", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient((f) => {
    f.on("evener/host/request", (params) => {
      const forwarded = params as HostRequestParams;
      if (forwarded.method !== "evener/path/validate") return routedDiscoveryDefault(forwarded.method);
      const path = (forwarded.params as { path?: string } | undefined)?.path;
      if (path === "~") return { valid: true } as HostForwardedResult; // no path at all
      return { path: path ?? "", valid: false, error: "no such file or directory" };
    });
  });
  window.history.pushState({}, "", "/new?dir=/tmp/nopath-a");
  renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await settled();

  // Every draft key stays a directory string...
  expect([...spawnDraftsStore.getState().drafts.keys()].every((key) => typeof key === "string")).toBe(true);
  // ...and the form seeds no directory rather than a nameless one.
  expect(spawnDraftsStore.getState().current?.cwd).toBe("");
  expect(workingDir().dataset.state).toBe("unset");
});

test("a local host choice omits source from the thread/start request", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient();
  window.history.pushState({}, "", "/new?dir=/tmp/local-target");
  renderSpawn(fake);
  await settled();

  await fillPrompt(user, "stay local");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params).not.toHaveProperty("source");
});

// A draft can name a remote host while the manifest is not (yet) readable: a
// reconnect reseeds the navigation store and the manifest is back in flight,
// and the draft - which outlives the pane - still holds the chosen host. The
// empty `sources` in that window is not evidence the host is gone, so the
// submission must carry the draft's own source; reading the empty list as a
// fallback would start the session locally with no indication (Component 06b).
test("a draft naming a remote host submits it while the manifest is loading", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient();
  window.history.pushState({}, "", "/new?dir=/tmp/loading-manifest");
  renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox");

  // The manifest drops back to in-flight (a reconnect reseeds the store): the
  // draft keeps the host, only the manifest's sources are unavailable.
  await act(async () => {
    navigationStore.setState({
      manifest: {
        key: { kind: "manifest" },
        data: null,
        loadedRevision: null,
        targetRevision: null,
        forceToken: 0,
        etag: null,
        loading: true,
        stale: false,
        error: null,
        generationID: "generation_test",
      },
    });
  });

  await fillPrompt(user, "keep the host");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
});

// The hub keeps offline sources in the manifest (only the online flag flips),
// so a host chosen while online stays present in the draft but must no longer
// be submittable once it goes offline - otherwise thread/start is rejected
// with "spawn source is not available".
test("a host that goes offline while a draft names it falls back to local", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient();
  window.history.pushState({}, "", "/new?dir=/tmp/offline-target");
  renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox");

  await act(async () => {
    seedSources([
      { id: "local", label: "Local", kind: "local", online: true },
      { id: "buildbox", label: "buildbox", kind: "ssh", online: false },
    ]);
  });

  // The option is still listed (disabled) but the effective choice is local.
  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("local");

  await fillPrompt(user, "no longer reachable");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params).not.toHaveProperty("source");
});

// The offline fallback is a real choice, not a render-time convenience: if the
// host returns while the form is still mounted it must not silently re-select
// itself. Selecting "Local" to affirmatively settle on local fires no change
// event when the select already reads local, so a draft left holding the stale
// host id would be the only thing that flips both the picker and the submitted
// source back to the remote host.
test("a host that returns online after its offline fallback stays on local", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient();
  window.history.pushState({}, "", "/new?dir=/tmp/reonline-target");
  renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox");

  await act(async () => {
    seedSources([
      { id: "local", label: "Local", kind: "local", online: true },
      { id: "buildbox", label: "buildbox", kind: "ssh", online: false },
    ]);
  });
  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("local");
  // The fallback is recorded in the draft, so it is not merely a render value.
  expect(completionDraft("/tmp/reonline-target").fields.getState().source).toBe("local");

  await act(async () => {
    seedSources([
      { id: "local", label: "Local", kind: "local", online: true },
      { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
    ]);
  });
  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("local");

  await fillPrompt(user, "still local");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params).not.toHaveProperty("source");
});

// The other fallback: a draft naming a host that has left the manifest
// entirely (removed while the draft lived). The picker shows local and the
// wire omits source rather than launching the unknown host.
test("a draft naming a host removed from the manifest shows local and omits source", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient();
  window.history.pushState({}, "", "/new?dir=/tmp/removed-target");
  renderSpawn(fake);
  await settled();

  const draft = completionDraft("/tmp/removed-target");
  await act(async () => {
    setDraftField(draft, "source", "decommissioned");
  });

  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("local");

  await fillPrompt(user, "host is gone");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params).not.toHaveProperty("source");
});

// --- remote target: discovery ownership (Component 06b, round four) --------

// The working-directory preflight (evener/path/validate + evener/dirs/create) is
// the CONTROLLER's own filesystem check. A remote launch's cwd belongs to the
// selected source instead, so the local answer has no authority: a path that
// exists only on the remote reads as missing here, and confirming "Create &
// start" would create the directory on THIS host before the remote launch still
// failed on its own cwd. The remote hub validates its own cwd via thread/start.
test("a remote host submission skips the controller-local directory preflight", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  // A cwd the local check calls a creatable missing directory: left ungated, the
  // preflight opens the Create & start dialog and never reaches thread/start.
  const fake = readyClient((f) =>
    f.on("evener/path/validate", ({ path }) => ({ path, valid: false, error: "no such file or directory" })),
  );
  window.history.pushState({}, "", "/new?dir=/remote/only/path");
  renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  // No local "doesn't exist yet" offer, and nothing created on the controller.
  expect(screen.queryByRole("button", { name: "Create & start" })).toBeNull();
  expect(fake.calls.some((call) => call.method === "evener/dirs/create")).toBe(false);
});

// The gate is the TARGET, not the mere presence of a picker: a local submission
// keeps the controller-local preflight and its Create & start offer.
test("a local submission keeps the controller-local directory preflight", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient((f) =>
    f.on("evener/path/validate", ({ path }) => ({ path, valid: false, error: "no such file or directory" })),
  );
  window.history.pushState({}, "", "/new?dir=/local/only/path");
  renderSpawn(fake);
  await settled();

  await user.click(screen.getByTestId("spawn-submit"));
  expect(await screen.findByRole("button", { name: "Create & start" })).toBeTruthy();
  expect(fake.calls.some((call) => call.method === "thread/start")).toBe(false);
});

// Provider readiness is read from THIS controller (evener/instance/list). A
// remote launch runs on the selected source's own hub, which resolves its own
// credentials, so a controller-local "missing provider" must not disable Start
// for a remote target - the remote hub's own thread/start error is the
// authority until source-aware discovery lands.
test("a remote host submission is not blocked by missing controller-local providers", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient((f) => f.on("evener/instance/list", () => ({ instances: [], availableProviders: [] })));
  connectionStore.getState().connect(fake);
  window.history.pushState({}, "", "/new?dir=/tmp/remote-providers");
  renderSpawn(fake);
  await settled();

  // Local: the controller's missing provider disables Start and explains why.
  await screen.findByRole("button", { name: "Connect provider" });
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);

  // Remote: that same controller-local state must not block the launch.
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await settled();
  await waitFor(() => expect(screen.queryByRole("button", { name: "Connect provider" })).toBeNull());
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));

  // Switching back restores the local block.
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "local" } });
  await settled();
  await screen.findByRole("button", { name: "Connect provider" });
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);
});

// The remote-target fix must not trade a WRONG early judgment for no feedback at
// all. With a remote host selected the launch really reaches thread/start, and
// the selected host's own validation is what reports: its WireError surfaces
// verbatim through friendlyLaunchErrorMessage, and nothing is created on the
// controller (the remote hub runs its own hubThreadStart, which canonicalizes
// and rejects a cwd it cannot see).
test("a remote launch rejected by the selected host surfaces that host's own error", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient((f) => {
    // The controller would call this cwd a creatable missing directory...
    f.on("evener/path/validate", ({ path }) => ({ path, valid: false, error: "no such file or directory" }));
    // ...and the selected host rejects it as its own cwd failure.
    f.on("thread/start", () => {
      throw new WireError("cwd: no such file or directory on buildbox", -32602);
    });
  });
  window.history.pushState({}, "", "/new?dir=/remote/only/path");
  renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await user.click(screen.getByTestId("spawn-submit"));

  await screen.findByText("Start failed: cwd: no such file or directory on buildbox");
  expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true);
  expect(fake.calls.some((call) => call.method === "evener/dirs/create")).toBe(false);
  expect(screen.queryByRole("button", { name: "Create & start" })).toBeNull();
});

// Relaxing the controller-local provider gate must not mean a doomed remote
// launch is SILENTLY accepted: when the selected host cannot serve it either,
// that host's own credential failure is what the person sees - so "allowed to
// submit" and "actually succeeded" stay distinguishable, and no session is
// navigated to on the failure path.
test("a remote launch the selected host cannot serve reports that host's own failure", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient((f) => {
    f.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
    f.on("thread/start", () => {
      throw new WireError("provider credentials missing for anthropic", -32014, { evenerErrorInfo: "hubLaunch" });
    });
  });
  connectionStore.getState().connect(fake);
  window.history.pushState({}, "", "/new?dir=/tmp/remote-providers-fail");
  renderSpawn(fake);
  await settled();

  // Starts blocked by the CONTROLLER's missing provider check (local target).
  await screen.findByRole("button", { name: "Connect provider" });
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await settled();
  await waitFor(() => expect(screen.queryByRole("button", { name: "Connect provider" })).toBeNull());

  await fillPrompt(user, "run remotely");
  await user.click(screen.getByTestId("spawn-submit"));

  await screen.findByText("Start failed: provider credentials missing for anthropic");
  expect(window.location.pathname).not.toContain("/s/");
});

// The plugin preview is the CONTROLLER's own inspection (evener/plugin/preview
// answers for this hub's host). A remote launch runs on the selected source's
// own hub, which resolves its own plugins, so a controller-local "this plugin
// is unavailable" must not disable Start or refuse the submit for a remote
// target - the selection is forwarded and the selected hub validates it at
// start, the same authority model the launch already follows for cwd,
// providers and models.
test("a remote host submission is not blocked by controller-local plugin issues", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient((f) =>
    f.on("evener/plugin/preview", ({ launchOverrides }) =>
      launchOverrides?.enabledPlugins?.includes("alpha")
        ? { ...SPAWN_PLUGIN_PREVIEW, selectionErrors: [{ name: "alpha", reason: "plugin is unavailable" }] }
        : SPAWN_PLUGIN_PREVIEW,
    ),
  );
  window.history.pushState({}, "", "/new?dir=/tmp/remote-plugins");
  renderSpawn(fake);
  await settled();

  // An explicit selection the CONTROLLER's preview reports as unavailable:
  // local Start is disabled by that controller-local reading.
  await act(async () => {
    setDraftField(completionDraft("/tmp/remote-plugins"), "pluginSelection", {
      mode: "explicit",
      names: ["alpha"],
    });
  });
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true));

  // The same controller-local issue must not gate a remote launch. Re-pinned to
  // component 07b's host-catalog gate: a host switch first asks the new host for
  // its harnesses/schema and Start stays blocked until those answers settle, so
  // each verdict below is read once the selected host's discovery has landed
  // (that window is pinned by the sibling "Start is blocked only until the
  // selected host's catalog answers settle" test - not by these assertions).
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "local" } });
  // Drain the local host's own catalog round-trip first: while it is in flight
  // Start is blocked by that window, not by the controller-local plugin issue
  // this assertion is about.
  await settled();
  // The switch back also reset the plugin selection to the host's own default
  // (component 07b review, round four - pinned by the sibling "a host switch
  // drops an explicit plugin selection the new host never previewed"), so
  // re-establish the controller-local selection before reading the local verdict
  // this assertion is about.
  await act(async () => {
    setDraftField(completionDraft("/tmp/remote-plugins"), "pluginSelection", { mode: "explicit", names: ["alpha"] });
  });
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true));
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
  // Re-establish the selection for the host now selected: component 07b's
  // round-four reset means an explicit selection only ever belongs to the host
  // it was made on (the sibling "a host switch drops an explicit plugin
  // selection..." test pins the drop), so the verbatim-forwarding assertion
  // below is about a selection the REMOTE host's own form is carrying.
  await act(async () => {
    setDraftField(completionDraft("/tmp/remote-plugins"), "pluginSelection", { mode: "explicit", names: ["alpha"] });
  });

  await fillPrompt(user, "run remotely");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  // The selection is forwarded verbatim for the selected hub to validate.
  expect(params.launchOverrides).toMatchObject({ enabledPlugins: ["alpha"] });
});

// The model and effort catalogs the form validates against (model/list, the
// model ladder) belong to THIS controller. A value supported only by the
// selected host must not be refused locally - the value rides thread/start and
// the remote hub validates it.
test("a remote host does not pre-validate /model against the controller catalog", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient();
  window.history.pushState({}, "", "/new?dir=/tmp/remote-model");
  renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  // A model absent from the controller's catalog: a local target would toast
  // `unknown value` and never reach thread/start.
  await user.type(promptField(), "/model buildbox-only/model-x");
  await user.keyboard("{Escape}");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  expect(params.modelProvider).toBe("buildbox-only");
  expect(params.model).toBe("model-x");
  // The builtin is consumed, not forwarded as a literal first turn.
  expect(params.input).toEqual([]);
  expect(getToasts()).toEqual([]);
});

test("a remote host does not pre-validate /reasoning-effort against the controller ladder", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient();
  window.history.pushState({}, "", "/new?dir=/tmp/remote-effort");
  renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  // A level outside the controller's ladder: a local target would toast
  // `unknown value` and never reach thread/start.
  await user.type(promptField(), "/reasoning-effort ultra");
  await user.keyboard("{Escape}");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  expect(params.reasoningEffort).toBe("ultra");
  expect(params.input).toEqual([]);
  expect(getToasts()).toEqual([]);
});

// --- remote target: model and cwd ownership (Component 06b, round seven) -----

// The uncredentialed-default fallback reads THIS controller's model/list and
// installs models[0] when launch/resolve names a default whose provider is not
// launchable here. A remote target resolves its own default model from its own
// host's credentials and catalog, so injecting the controller's pick would ride
// thread/start and stop the selected host from resolving its own default.
test("a remote host does not inherit the controller's uncredentialed-default fallback model", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient((f) => {
    f.on("model/list", () => ({
      data: [{ provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" }],
    }));
    f.on("evener/launch/resolve", () => ({
      effective: { model: "openai/gpt-5.5" }, // openai is not launchable on THIS controller
      layers: {},
      provenance: {},
    }));
    // The selected host answers its own catalog and launch defaults (component
    // 07b): those reads are forwarded, so the proxy answers them too.
    f.on("evener/host/request", (params) => {
      const method = (params as HostRequestParams).method;
      if (method === "model/list") {
        return {
          data: [{ provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" }],
        } as HostForwardedResult;
      }
      if (method === "evener/launch/resolve") {
        return { effective: { model: "openai/gpt-5.5" }, layers: {}, provenance: {} } as HostForwardedResult;
      }
      return routedDiscoveryDefault(method);
    });
  });
  window.history.pushState({}, "", "/new?dir=/tmp/remote-fallback");
  renderSpawn(fake);
  // Select the remote host before the fallback's CATALOG_SETTLE_MS window can
  // elapse, so this pins the remote-at-mount path.
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await waitFor(() =>
    expect(
      fake.calls.some(
        (c) =>
          c.method === "evener/launch/resolve" ||
          (c.method === "evener/host/request" && (c.params as HostRequestParams).method === "evener/launch/resolve"),
      ),
    ).toBe(true),
  );
  // Outlast the settle window: a still-armed fallback would fire in here.
  await act(async () => {
    await vi.advanceTimersByTimeAsync(400);
  });

  expectModelReadsDefault();
  expect(modelTrigger().textContent).not.toContain("claude-sonnet-4-5");

  await fillPrompt(user, "run remotely");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((c) => c.method === "thread/start")).toBe(true));

  const params = fake.calls.find((c) => c.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  // No controller-derived model is forwarded; the host resolves its own default.
  expect(params.model).toBeUndefined();
  expect(params.modelProvider).toBeUndefined();
});

// The same defect from the other direction: a fallback installed while the
// target was local must be retired when the person switches to a remote host.
// Only a still-fallback-derived value is cleared - a person's own pick is not.
test("a controller-catalog fallback model is retired when the target becomes remote", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient((f) => {
    f.on("model/list", () => ({
      data: [{ provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" }],
    }));
    f.on("evener/launch/resolve", () => ({
      effective: { model: "openai/gpt-5.5" },
      layers: {},
      provenance: {},
    }));
  });
  window.history.pushState({}, "", "/new?dir=/tmp/remote-fallback-flip");
  renderSpawn(fake);
  await settled();

  // Local target: the fallback installs the controller's first launchable model.
  await waitFor(() => expect(modelTrigger().textContent).toContain("anthropic/claude-sonnet-4-5"));

  // Switching to a remote host retires it: it is the controller's model, not
  // the selected host's.
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await settled();
  await waitFor(expectModelReadsDefault);
  expect(modelTrigger().textContent).not.toContain("claude-sonnet-4-5");

  await fillPrompt(user, "run remotely");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((c) => c.method === "thread/start")).toBe(true));

  const params = fake.calls.find((c) => c.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  expect(params.model).toBeUndefined();
});

// /model takes RAW user text, unlike every other splitModelId caller (which
// splits a provider/model catalog id that always contains a slash). "foo"
// splits to provider "foo" with an empty model, which the selected host ignores
// (its own model field stays empty) - the requested model was silently dropped.
// It is refused pre-launch with the same unknown-value message the local path
// uses.
test("a remote /model value without a provider is refused before launch", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient();
  window.history.pushState({}, "", "/new?dir=/tmp/remote-bare-model");
  renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await user.type(promptField(), "/model foo");
  await user.keyboard("{Escape}");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(screen.getByText(/\/model: unknown value "foo"/)).toBeTruthy());
  expect(fake.calls.some((call) => call.method === "thread/start")).toBe(false);
});
