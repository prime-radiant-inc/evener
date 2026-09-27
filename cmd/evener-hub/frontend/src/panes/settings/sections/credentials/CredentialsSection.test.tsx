import type { AuthLogoutResponse, AuthTestResponse, InstanceEntry, InstanceListResponse } from "@evener/appwire-client";
import {
  CONNECTION_REPLACED_ERROR,
  ENDPOINT_CHANGED_TEST_MESSAGE,
  ErrorEndpointConflict,
  ErrorInstanceRemoveApplied,
  ErrorInstanceRenamePersisted,
  FINGERPRINT_UNAVAILABLE_TEST_MESSAGE,
  WireError,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { captureNewTabs, NEW_TAB_POLICY, openedNewTab } from "../../../../shell/openInNewTab.testSupport";
import { connectionStore } from "../../../../stores/connection";
import { credentialsStore, resetCredentialsStoreForTests } from "../../../../stores/credentials";
import { setMutationClientIdentityForTests } from "../../../../stores/mutationClientIdentity";
import { Toast } from "../../../../widgets";
import { getToasts, resetToastStoreForTests } from "../../../../widgets/toast/store";
import { CredentialsSection } from "./CredentialsSection";

/** The refusal these controls use instead of the native attribute: a click that
 * starts work must not drop the keyboard, so the pending/busy state is
 * aria-disabled (Button/Switch swallow the activation themselves). */
const isRefused = (el: HTMLElement): boolean => el.getAttribute("aria-disabled") === "true";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

function instance(overrides: Partial<InstanceEntry> & Pick<InstanceEntry, "name" | "providerId">): InstanceEntry {
  return {
    protocol: "openai-chat",
    auth: "bearer",
    implicit: false,
    isDefault: false,
    activeSource: "none",
    hasStoredOAuth: false,
    credentialRequired: true,
    ...overrides,
  };
}

const WORK = instance({
  name: "work",
  providerId: "anthropic",
  authModes: ["apiKey"],
  isDefault: true,
  hasStoredFile: true,
  activeSource: "store",
  models: [{ id: "claude-opus-4-6" }, { id: "claude-sonnet-5", disabled: true }],
});
const PERSONAL = instance({
  name: "personal",
  providerId: "openai-codex",
  auth: "oauth-openai-codex",
  authModes: ["oauth"],
});
const LIST: InstanceListResponse = { instances: [WORK, PERSONAL], availableProviders: [] };

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

async function advanceTime(milliseconds: number): Promise<void> {
  await act(() => vi.advanceTimersByTimeAsync(milliseconds));
}

// The detail-sheet navigation path: every per-instance action lives in the
// inspector that opens from a row tap (design-system.md §10), so integration
// tests reach them through it. Returns the inspector dialog for scoping.
async function openSheet(user: ReturnType<typeof userEvent.setup>, name: string): Promise<HTMLElement> {
  await user.click(await screen.findByRole("button", { name: new RegExp(name) }));
  return screen.findByRole("dialog", { name });
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetCredentialsStoreForTests();
  resetToastStoreForTests();
  // The auth mutations carry the page's identity on the wire now, so the
  // assertions that pin their exact params need one that cannot vary.
  setMutationClientIdentityForTests("test-tab");
});

afterEach(() => {
  cleanup();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

test("Settings Connect provider opens discovery and retains management on cancel", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => LIST);
  render(<CredentialsSection sectionId="credentials" />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "Connect provider" }));
  expect(await screen.findByText("Show all providers")).toBeTruthy();
  await user.keyboard("{Escape}");
  expect(await screen.findByText("work")).toBeTruthy();
  expect(fake.calls.filter((call) => call.method === "evener/instance/setDefault")).toEqual([]);
});

describe("initial load", () => {
  test("fetches evener/instance/list on mount and groups rows by providerId", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("work");
    expect(screen.getByText("anthropic")).toBeTruthy();
    expect(screen.getByText("openai-codex")).toBeTruthy();
    expect(screen.getByText("personal")).toBeTruthy();
  });

  // The Add dialog labels a provider `name || id`; the list has to call it
  // the same thing, or ProviderDescriptor.name only ever appears in the form.
  test("a group header prints the provider's display name when the registry supplies one", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => ({
      instances: [WORK],
      availableProviders: [
        { id: "anthropic", name: "Anthropic", protocol: "anthropic", auth: "bearer", implicit: true },
      ],
    }));
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("work");
    expect(screen.getByText("Anthropic")).toBeTruthy();
    expect(screen.queryByText("anthropic")).toBeNull();
  });

  test("a group header falls back to the raw providerId when no descriptor names it", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => ({ instances: [WORK], availableProviders: [] }));
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("work");
    expect(screen.getByText("anthropic")).toBeTruthy();
  });

  test("empty state", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("No provider instances configured.");
  });

  test("load failure shows an error message", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => {
      throw new Error("network down");
    });
    render(<CredentialsSection sectionId="credentials" />);
    // error is converted via friendlyErrorMessage: raw JS errors become the generic message
    await screen.findByText(/Failed to load: Something went wrong/);
    // Assert the raw string no longer appears
    expect(screen.queryByText(/network down/)).toBeNull();
  });

  // The integration-level proof of useConnectedEffect: a direct deep link
  // to /credentials can mount this section before AppShell's own connect()
  // handshake finishes (see that hook's own doc comment) - the initial
  // fetch must defer until the connection is actually ready, then fire
  // exactly once, rather than throwing (unhandled) or never firing at all.
  test("mounting before the connection is ready defers the initial load, which then fires exactly once it becomes ready", async () => {
    const fake = new FakeClient("idle"); // NOT ready at mount
    connectionStore.getState().connect(fake);
    let calls = 0;
    fake.on("evener/instance/list", () => {
      calls += 1;
      return LIST;
    });
    render(<CredentialsSection sectionId="credentials" />);
    // Give any (wrongly) eager fetch attempt every chance to fire before
    // asserting it hasn't - a real bug here would throw synchronously into
    // an unhandled rejection, not silently pass this check.
    await act(() => Promise.resolve());
    expect(calls).toBe(0);

    act(() => {
      fake.emitReady();
    });

    await screen.findByText("work");
    expect(calls).toBe(1);
  });
});

// The pane keeps its rows mounted through a load, so the focused row survives a
// refresh; a listing that no longer CARRIES it - a removal from this pane or
// another client - unmounts the focused control instead. The browser drops
// focus to <body> and the pane is not inside a focus scope, so nothing brought
// it back: the next Tab started over at the top of the document.
test("a listing that removes the focused row hands the keyboard back to the pane", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => LIST);
  render(<CredentialsSection sectionId="credentials" />);
  const workRow = await screen.findByRole("button", { name: /work/ });
  workRow.focus();
  expect(document.activeElement).toBe(workRow);

  await act(async () => credentialsStore.setState({ instances: [PERSONAL] }));

  expect(screen.queryByRole("button", { name: /work/ })).toBeNull(); // the row really did go
  expect(document.activeElement).not.toBe(document.body);
  expect(screen.getByRole("button", { name: "Connect provider" })).toBe(document.activeElement);
});

// Focus recovery is for focus that was TAKEN away, not for focus that was never
// placed: a cold load (a deep link, a reload) has nothing focused, and moving the
// keyboard into the pane's first control on mount would be the same defect in
// reverse.
test("a cold load of the pane does not move the keyboard", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => LIST);
  render(<CredentialsSection sectionId="credentials" />);
  await screen.findByText("work");

  expect(document.activeElement).toBe(document.body);
});

// A read in flight must not take the rows away: the list IS what the user is
// reading, and swapping it for the skeleton on every background refresh makes it
// flicker and unmounts the row the keyboard is on. The skeleton is for the state
// it was written for - nothing to show yet - which is what the connection dialog
// already does with its own rows.
test("a background refresh keeps the rows mounted instead of swapping in the skeleton", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => LIST);
  render(<CredentialsSection sectionId="credentials" />);
  await screen.findByRole("button", { name: /work/ });

  const refresh = deferred<InstanceListResponse>();
  fake.on("evener/instance/list", () => refresh.promise);
  let inFlight!: Promise<boolean>;
  await act(async () => {
    inFlight = credentialsStore.getState().fetch();
    await Promise.resolve();
  });

  // The read is in flight and the rows are still the ones on screen.
  expect(credentialsStore.getState().loading).toBe(true);
  expect(screen.getByRole("button", { name: /work/ })).toBeTruthy();
  expect(screen.queryByRole("status", { name: "Loading" })).toBeNull();

  await act(async () => {
    refresh.resolve(LIST);
    await inFlight;
  });
  expect(screen.getByRole("button", { name: /work/ })).toBeTruthy();
});

// A failed refresh keeps the listing it already had (readListing works that way),
// so the rows it kept are still the user's - the banner explains them rather than
// replacing them.
test("a failed refresh keeps the rows and shows the banner", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => LIST);
  render(<CredentialsSection sectionId="credentials" />);
  const workRow = await screen.findByRole("button", { name: /work/ });
  workRow.focus();

  fake.on("evener/instance/list", () => {
    throw new WireError("listing unavailable", -32000);
  });
  await act(async () => {
    await credentialsStore.getState().fetch();
  });

  expect(screen.getByText(/Failed to load/)).toBeTruthy();
  expect(screen.getByRole("button", { name: /work/ })).toBe(workRow);
});

// ...and the keyboard stays where the user put it, since nothing unmounted under
// it.
test("a background refresh keeps the keyboard in the pane", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => LIST);
  render(<CredentialsSection sectionId="credentials" />);
  const workRow = await screen.findByRole("button", { name: /work/ });
  workRow.focus();
  expect(document.activeElement).toBe(workRow);

  const refresh = deferred<InstanceListResponse>();
  fake.on("evener/instance/list", () => refresh.promise);
  let inFlight!: Promise<boolean>;
  await act(async () => {
    inFlight = credentialsStore.getState().fetch();
    await Promise.resolve();
  });

  // The row stayed mounted through the read, so the keyboard never moved.
  expect(screen.getByRole("button", { name: /work/ })).toBe(workRow);
  expect(document.activeElement).toBe(workRow);

  await act(async () => {
    refresh.resolve(LIST);
    await inFlight;
  });
  expect(screen.getByRole("button", { name: /work/ })).toBe(workRow);
});

// Warnings describe the listing that produced them - a providers.toml load
// error, the user-layer note, a stray OAuth notice - so while the rows on
// screen belong to a connection that is gone they describe a listing this one
// never read. The management dialog suppresses them for exactly that reason.
test("a replaced connection's warnings stay hidden until its own listing lands", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => ({ ...LIST, diagnostics: ['providers.toml: unexpected key "type"'] }));
  render(<CredentialsSection sectionId="credentials" />);
  await screen.findByText("work");
  expect(screen.getByText("Warnings")).toBeTruthy();

  const restored = deferred<InstanceListResponse>();
  const replacement = new FakeClient("ready");
  replacement.on("evener/instance/list", () => restored.promise);
  await act(async () => connectionStore.getState().connect(replacement));
  expect(screen.queryByText("Warnings")).toBeNull();

  await act(async () => {
    restored.resolve({ ...LIST, diagnostics: ["user layer: /home/x/.config/evener/providers.toml"] });
    await restored.promise;
  });
  expect(await screen.findByText("Warnings")).toBeTruthy();
  expect(screen.getByText(/user layer/)).toBeTruthy();
});

// staleListingHeld answers "are there rows to act on", which is the wrong
// question for content: a warning describes the listing that produced it, and
// that is true of a replaced listing that carried no rows at all.
test("a replaced connection's warnings are hidden even when its listing held no rows", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => ({
    instances: [],
    availableProviders: [],
    diagnostics: ['providers.toml: unexpected key "type"'],
  }));
  render(<CredentialsSection sectionId="credentials" />);
  await screen.findByText("Warnings");

  const replacement = new FakeClient("ready");
  replacement.on("evener/instance/list", () => new Promise<InstanceListResponse>(() => {}));
  await act(async () => connectionStore.getState().connect(replacement));

  expect(credentialsStore.getState().listingFromPreviousConnection).toBe(true);
  expect(screen.queryByText("Warnings")).toBeNull();
});

// A refresh is a read, so it is allowed while the rows are a replaced
// connection's - but its ANSWER speaks about this connection, and merging it
// into the previous connection's rows (or clearing the listing error a failed
// full read left) would present those rows as current. The full listing read
// that lands next is what makes the row a refresh can speak about.
test("a model refresh while the rows are a replaced connection's does not apply its answer", async () => {
  const fake = connectFakeClient();
  const row = { ...WORK, models: [{ id: "claude-opus-4-6", disabled: false }] };
  fake.on("evener/instance/list", () => ({ instances: [row], availableProviders: [] }));
  render(
    <>
      <CredentialsSection sectionId="credentials" />
      <Toast />
    </>,
  );
  await screen.findByText("work");
  const user = userEvent.setup();
  const inspector = await openSheet(user, "work");

  // The replacement's own read is held open, and its refresh answer carries a
  // different inventory for the same name.
  const replacement = new FakeClient("ready");
  replacement.on("evener/instance/list", () => new Promise<InstanceListResponse>(() => {}));
  replacement.on("evener/instance/refreshModels", () => ({
    instances: [{ ...row, models: [{ id: "claude-sonnet-5", disabled: true }] }],
    availableProviders: [],
  }));
  await act(async () => connectionStore.getState().connect(replacement));
  expect(credentialsStore.getState().listingFromPreviousConnection).toBe(true);
  await act(async () => credentialsStore.setState({ error: "listing unavailable" }));

  await user.click(within(inspector).getByRole("button", { name: "Refresh live models" }));
  await act(async () => {
    await Promise.resolve();
  });

  // The refreshed inventory is not presented as current for rows that still
  // belong to the connection that is gone...
  expect(credentialsStore.getState().instances[0]?.models?.map((model) => model.id)).toEqual(["claude-opus-4-6"]);
  // ...and the failed full read's error is not cleared by it.
  expect(credentialsStore.getState().error).toBe("listing unavailable");
});

describe("the detail sheet", () => {
  test("clicking a row opens the inspector; its close button dismisses it", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    expect(within(inspector).getByText(/Configured via stored API key/)).toBeTruthy();
    await user.click(within(inspector).getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "work" })).toBeNull());
  });

  test("opening the API-key editor from the sheet closes the sheet", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.click(within(inspector).getByRole("button", { name: "Replace key" }));
    expect(screen.getByRole("dialog", { name: "Set API key for work" })).toBeTruthy();
    expect(screen.queryByRole("dialog", { name: "work" })).toBeNull();
  });

  test("a removed instance's inspector closes itself once the store updates", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    fake.on("evener/instance/remove", (params) => {
      expect(params).toEqual({ name: "personal", originClientId: "test-tab" });
      return { instances: [WORK], availableProviders: [] };
    });
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("personal");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "personal");
    await user.click(within(inspector).getByRole("button", { name: "Remove" }));
    const confirm = screen.getByRole("dialog", { name: "Remove instance" });
    await user.click(within(confirm).getByRole("button", { name: "Remove" }));
    await screen.findByText("Removed instance personal");
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "personal" })).toBeNull());
  });

  // The listing a removal answers with is only the truth if the store kept it,
  // so a removal must be confirmed against a listing the store actually
  // applied - never against a response a concurrent read threw away.
  test("a superseded removal reconciles the listing before it is reported", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    const listingsAtRemoval: InstanceEntry[][] = [];
    const onInstanceRemoved = vi.fn(() => {
      listingsAtRemoval.push(credentialsStore.getState().instances);
    });
    render(<CredentialsSection sectionId="credentials" onInstanceRemoved={onInstanceRemoved} />);
    await screen.findByText("personal");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "personal");
    let resolveRemoval!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/remove",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          resolveRemoval = resolve;
        }),
    );
    await user.click(within(inspector).getByRole("button", { name: "Remove" }));
    const confirm = screen.getByRole("dialog", { name: "Remove instance" });
    await user.click(within(confirm).getByRole("button", { name: "Remove" }));

    // A listing read issued after the removal wins the store race, so the
    // removal's own response - the only one without the row - is discarded.
    await act(async () => {
      await credentialsStore.getState().fetch();
    });
    const WITHOUT_PERSONAL: InstanceListResponse = { instances: [WORK], availableProviders: [] };
    fake.on("evener/instance/list", () => WITHOUT_PERSONAL);
    await act(async () => {
      resolveRemoval(WITHOUT_PERSONAL);
    });
    // The discarded response is not the confirmation: the removal is only
    // reported as done once the store applied a listing without the authored
    // row, which takes a read of its own (the mount read and the test's own
    // superseding read are the first two).
    await waitFor(() => expect(getToasts().some((toast) => toast.text === "Removed instance personal")).toBe(true));
    expect(fake.calls.filter((call) => call.method === "evener/instance/list").length).toBeGreaterThanOrEqual(3);
    expect(credentialsStore.getState().instances).toEqual([WORK]);
    expect(onInstanceRemoved).toHaveBeenCalledWith("personal");
    expect(listingsAtRemoval[0]).toEqual([WORK]);
  });

  // fetch() swallows a failed read into the store's error field instead of
  // rejecting, so a resolved reconcile promise is no confirmation. The
  // superseded removal is still reported: its RPC resolved (only its response
  // was discarded), so the entry left providers.toml, and the row the listing
  // still shows is one this client read before the removal landed. That is
  // reported as "could not be confirmed" rather than as a failure of the
  // removal itself.
  test("a removal whose reconcile read fails reports the removal it could not confirm", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    const onInstanceRemoved = vi.fn();
    render(
      <>
        <CredentialsSection sectionId="credentials" onInstanceRemoved={onInstanceRemoved} />
        <Toast />
      </>,
    );
    await screen.findByText("personal");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "personal");
    let resolveRemoval!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/remove",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          resolveRemoval = resolve;
        }),
    );
    await user.click(within(inspector).getByRole("button", { name: "Remove" }));
    const confirm = screen.getByRole("dialog", { name: "Remove instance" });
    await user.click(within(confirm).getByRole("button", { name: "Remove" }));

    // A listing read issued after the removal supersedes its response.
    await act(async () => {
      await credentialsStore.getState().fetch();
    });
    fake.on("evener/instance/list", () => {
      throw new Error("list denied");
    });
    await act(async () => {
      resolveRemoval({ instances: [WORK], availableProviders: [] });
      // The reconcile read fails outright; wait until the flow settles on
      // either the honest unconfirmed-removal error or (wrongly) a success.
      await vi.waitFor(() => {
        if (!screen.queryByText(/could not be confirmed/) && !screen.queryByText("Removed instance personal")) {
          throw new Error("no outcome toast yet");
        }
      });
    });
    expect(screen.getByText(/could not be confirmed for personal/)).toBeTruthy();
    expect(onInstanceRemoved).toHaveBeenCalledWith("personal");
  });

  // The applied path is not automatically a confirmation either: the store's
  // own listing is the applied response, and it must actually have lost the row.
  test("a removal whose applied listing still holds the row is not reported as removed", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    const onInstanceRemoved = vi.fn();
    render(
      <>
        <CredentialsSection sectionId="credentials" onInstanceRemoved={onInstanceRemoved} />
        <Toast />
      </>,
    );
    await screen.findByText("personal");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "personal");
    fake.on("evener/instance/remove", () => ({ instances: [WORK, PERSONAL], availableProviders: [] }));
    await user.click(within(inspector).getByRole("button", { name: "Remove" }));
    const confirm = screen.getByRole("dialog", { name: "Remove instance" });
    await user.click(within(confirm).getByRole("button", { name: "Remove" }));

    await waitFor(() => expect(screen.getByText(/could not be confirmed for personal/)).toBeTruthy());
    expect(screen.queryByText("Removed instance personal")).toBeNull();
    // The row is gone on the host: re-issuing the remove could only fail, so
    // the confirm dialog closes with the failure.
    expect(screen.queryByRole("dialog", { name: "Remove instance" })).toBeNull();
    expect(onInstanceRemoved).not.toHaveBeenCalled();
  });

  // The hub deletes the instance's credentials first and its config entry
  // after; a failure that cannot put the deleted credential back leaves the
  // removal standing. The hub marks that with its own discriminator, so the
  // section reconciles - closes the confirmation and the sheet, re-reads the
  // listing, and tells the guided owner the instance is gone - rather than
  // report a failed Remove whose retry targets a missing instance.
  test("an applied removal reported by the hub is reconciled, not failed", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    const HUB_MESSAGE =
      'removing "personal" failed: the instance is still configured, but its stored key could not be restored (restore refused)';
    fake.on("evener/instance/remove", () => {
      throw new WireError(HUB_MESSAGE, -32603, { evenerErrorInfo: ErrorInstanceRemoveApplied });
    });
    const onInstanceRemoved = vi.fn();
    render(
      <>
        <CredentialsSection sectionId="credentials" onInstanceRemoved={onInstanceRemoved} />
        <Toast />
      </>,
    );
    await screen.findByText("personal");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "personal");
    const listingsBefore = fake.calls.filter((call) => call.method === "evener/instance/list").length;
    await user.click(within(inspector).getByRole("button", { name: "Remove" }));
    const confirm = screen.getByRole("dialog", { name: "Remove instance" });
    await user.click(within(confirm).getByRole("button", { name: "Remove" }));

    // The owning editor hears the instance is gone...
    await waitFor(() => expect(onInstanceRemoved).toHaveBeenCalledWith("personal"));
    // ...the confirmation closes instead of hanging over a removed instance...
    expect(screen.queryByRole("dialog", { name: "Remove instance" })).toBeNull();
    // ...the sheet closes too: the listing here still holds the row (a stale or
    // lost refresh is exactly the hazard), and a sheet left open on it would
    // keep the removed instance's draft and let a save re-author the name...
    expect(screen.queryByRole("dialog", { name: "personal" })).toBeNull();
    // ...the listing is re-read...
    expect(fake.calls.filter((call) => call.method === "evener/instance/list").length).toBeGreaterThan(listingsBefore);
    // ...and the hub's own message is a warning naming what was left behind,
    // never a failed Remove.
    expect(await screen.findByText(HUB_MESSAGE)).toBeTruthy();
    expect(screen.queryByText(/Remove failed/)).toBeNull();
  });

  // `implicit` is not the same as "the environment supplies it": a stored key
  // and a signed-in Codex record are implicit rows the removal's credential
  // cleanup DOES delete. A stale listing that still holds one is not a
  // confirmed removal, and a surviving one is not environment access. Only the
  // source test (fromEnvironment) tells them apart.
  test("a superseded removal whose name survives only as a stored-key row is not confirmed", async () => {
    const STORED = instance({
      name: "groq",
      providerId: "groq",
      implicit: true,
      activeSource: "store",
      hasStoredFile: true,
      authModes: ["apiKey"],
    });
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => ({ instances: [STORED], availableProviders: [] }));
    const onInstanceRemoved = vi.fn();
    render(
      <>
        <CredentialsSection sectionId="credentials" onInstanceRemoved={onInstanceRemoved} />
        <Toast />
      </>,
    );
    // `groq` is both this row's name and its provider's group header, so scope
    // the wait to the row button.
    await screen.findByRole("button", { name: /groq/ });
    const user = userEvent.setup();
    const inspector = await openSheet(user, "groq");
    let resolveRemoval!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/remove",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          resolveRemoval = resolve;
        }),
    );
    await user.click(within(inspector).getByRole("button", { name: "Remove" }));
    const confirm = screen.getByRole("dialog", { name: "Remove instance" });
    await user.click(within(confirm).getByRole("button", { name: "Remove" }));

    // A listing read issued after the removal wins the store race, so the
    // removal's own response is discarded as superseded.
    await act(async () => {
      await credentialsStore.getState().fetch();
    });
    await act(async () => {
      resolveRemoval({ instances: [STORED], availableProviders: [] });
    });

    // The row still on the host is the user's own stored-key row, so the
    // removal is not confirmed and the success wording must not appear.
    await screen.findByText(/could not be confirmed for groq/);
    expect(screen.queryByText("Removed instance groq")).toBeNull();
    expect(screen.queryByText(/environment access for it is still active/)).toBeNull();
    // The removal's RPC did resolve: the owner still hears so it can drop what
    // it retained for the name.
    expect(onInstanceRemoved).toHaveBeenCalledWith("groq");
  });

  // The mirror of the stored-key case: a row the environment really does
  // supply keeps the wording that says access under the name is still active.
  test("a leftover environment-backed row produces the still-supplied wording", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    fake.on("evener/instance/remove", () => ({
      instances: [
        WORK,
        instance({
          name: "personal",
          providerId: "openai-codex",
          implicit: true,
          activeSource: "env:OPENAI_API_KEY",
        }),
      ],
      availableProviders: [],
    }));
    const onInstanceRemoved = vi.fn();
    render(
      <>
        <CredentialsSection sectionId="credentials" onInstanceRemoved={onInstanceRemoved} />
        <Toast />
      </>,
    );
    await screen.findByText("personal");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "personal");
    await user.click(within(inspector).getByRole("button", { name: "Remove" }));
    const confirm = screen.getByRole("dialog", { name: "Remove instance" });
    await user.click(within(confirm).getByRole("button", { name: "Remove" }));

    await screen.findByText("Removed instance personal; environment access for it is still active");
    expect(screen.queryByText(/could not be confirmed/)).toBeNull();
    expect(onInstanceRemoved).toHaveBeenCalledWith("personal");
  });
});

describe("credential verification", () => {
  test("sends the exact custom instance name and shows local pending state until the deferred response arrives", async () => {
    const fake = connectFakeClient();
    const customName = "OpenAI / team-east:prod";
    const custom = instance({ name: customName, providerId: "openai", authModes: ["apiKey"] });
    const response = deferred<AuthTestResponse>();
    fake.on("evener/instance/list", () => ({ instances: [custom], availableProviders: [] }));
    fake.on("evener/auth/test", (params) => {
      expect(params).toEqual({ provider: customName });
      return response.promise;
    });
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText(customName);

    const inspector = await openSheet(userEvent.setup(), customName);
    const testButton = within(inspector).getByRole("button", { name: "Test credentials" });
    await userEvent.setup().click(testButton);

    expect(isRefused(within(inspector).getByRole("button", { name: "Testing credentials…" }))).toBe(true);
    expect((within(inspector).getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(false);
    expect(fake.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(1);

    response.resolve({ provider: customName, status: "success", message: "Credentials verified." });
    expect((await screen.findByRole("status")).textContent).toContain("Credentials verified.");
    expect(isRefused(within(inspector).getByRole("button", { name: "Test credentials" }))).toBe(false);
  });

  test("suppresses duplicate clicks for one pending instance while another instance stays enabled", async () => {
    const fake = connectFakeClient();
    const workResponse = deferred<AuthTestResponse>();
    const personalResponse = deferred<AuthTestResponse>();
    fake.on("evener/instance/list", () => LIST);
    fake.on("evener/auth/test", (params) => {
      if (params.provider === WORK.name) return workResponse.promise;
      if (params.provider === PERSONAL.name) return personalResponse.promise;
      throw new Error(`unexpected provider ${params.provider}`);
    });
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText(WORK.name);
    const user = userEvent.setup();

    const workInspector = await openSheet(user, WORK.name);
    const workButton = within(workInspector).getByRole("button", { name: "Test credentials" });
    await user.click(workButton);
    await user.click(workButton);
    expect(fake.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(1);
    expect(isRefused(within(workInspector).getByRole("button", { name: "Testing credentials…" }))).toBe(true);

    // The other instance's sheet is independent: its Test stays enabled.
    await user.click(within(workInspector).getByRole("button", { name: "Close" }));
    const personalInspector = await openSheet(user, PERSONAL.name);
    const personalButton = within(personalInspector).getByRole("button", { name: "Test credentials" });
    expect(isRefused(personalButton)).toBe(false);
    await user.click(personalButton);
    expect(fake.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(2);

    workResponse.resolve({ provider: WORK.name, status: "success", message: "Credentials verified." });
    personalResponse.resolve({ provider: PERSONAL.name, status: "success", message: "Credentials verified." });
    await waitFor(() =>
      expect(isRefused(within(personalInspector).getByRole("button", { name: "Test credentials" }))).toBe(false),
    );
    await user.click(within(personalInspector).getByRole("button", { name: "Close" }));
    const workAgain = await openSheet(user, WORK.name);
    expect(isRefused(within(workAgain).getByRole("button", { name: "Test credentials" }))).toBe(false);
  });

  // The status-to-message table itself is pinned in credentialLabels.test.ts;
  // this is the wiring that renders its result.
  test("renders the safe status and message", async () => {
    const fake = connectFakeClient();
    const response = deferred<AuthTestResponse>();
    fake.on("evener/instance/list", () => ({ instances: [WORK], availableProviders: [] }));
    fake.on("evener/auth/test", () => response.promise);
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText(WORK.name);
    const inspector = await openSheet(userEvent.setup(), WORK.name);
    await userEvent.setup().click(within(inspector).getByRole("button", { name: "Test credentials" }));
    response.resolve({ provider: WORK.name, status: "missing", message: "raw provider message" });

    const statusNode = await screen.findByRole("status");
    expect(statusNode.textContent).toBe(
      "missing: No credentials are configured for this instance. Add a key or sign in first.",
    );
  });

  test("does not render a supplied secret from a response message", async () => {
    const fake = connectFakeClient();
    const secret = "sk-live-do-not-render";
    fake.on("evener/instance/list", () => ({ instances: [WORK], availableProviders: [] }));
    fake.on("evener/auth/test", async () => ({ provider: WORK.name, status: "auth_rejected", message: secret }));
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText(WORK.name);
    const inspector = await openSheet(userEvent.setup(), WORK.name);
    await userEvent.setup().click(within(inspector).getByRole("button", { name: "Test credentials" }));

    const status = await screen.findByRole("status");
    expect(status.textContent).toContain("The provider rejected these credentials. Replace the key or sign in again.");
    expect(document.body.textContent).not.toContain(secret);
  });

  test("does not render a raw RPC error string", async () => {
    const fake = connectFakeClient();
    const secret = "raw provider response containing sk-live-do-not-render";
    fake.on("evener/instance/list", () => ({ instances: [WORK], availableProviders: [] }));
    fake.on("evener/auth/test", async () => {
      throw new Error(secret);
    });
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText(WORK.name);
    const inspector = await openSheet(userEvent.setup(), WORK.name);
    await userEvent.setup().click(within(inspector).getByRole("button", { name: "Test credentials" }));

    const status = await screen.findByRole("status");
    expect(status.textContent).toContain(
      "The provider endpoint could not be reached. Check the endpoint and network connection.",
    );
    expect(document.body.textContent).not.toContain(secret);
  });

  test("resets pending state and ignores a late result after same-name instance refresh", async () => {
    const fake = connectFakeClient();
    // A destination carries a fingerprint in production unless the hub cannot
    // key one, and this test is about the late result, not a fingerprint
    // refusal: both rows assert their own destination so the probe is sent.
    const oldInstance = instance({
      name: "work",
      providerId: "anthropic",
      baseUrl: "https://old.example/v1",
      endpointFingerprint: "fp-old",
    });
    const refreshedInstance = instance({
      name: "work",
      providerId: "anthropic",
      baseUrl: "https://new.example/v1",
      endpointFingerprint: "fp-new",
    });
    const response = deferred<AuthTestResponse>();
    let listCalls = 0;
    fake.on("evener/instance/list", () => {
      listCalls += 1;
      return listCalls === 1
        ? { instances: [oldInstance], availableProviders: [] }
        : { instances: [refreshedInstance], availableProviders: [] };
    });
    fake.on("evener/auth/test", () => response.promise);
    render(<CredentialsSection sectionId="credentials" />);
    const inspector = await openSheet(userEvent.setup(), "work");
    await screen.findByText("Not configured · openai-chat · base https://old.example/v1");
    await userEvent.setup().click(within(inspector).getByRole("button", { name: "Test credentials" }));
    expect(within(inspector).getByRole("button", { name: "Testing credentials…" })).toBeTruthy();

    await act(async () => {
      await credentialsStore.getState().fetch();
    });
    // The row reads the instance from the store, so the refreshed base URL
    // lands live; the stale pending state from the old configuration is gone.
    await screen.findByText("Not configured · openai-chat · base https://new.example/v1");
    const refreshedButton = within(inspector).getByRole("button", { name: /Test(?:ing credentials…)?/ });
    expect(isRefused(refreshedButton)).toBe(false);
    response.resolve({ provider: "work", status: "success", message: "Credentials verified." });
    await act(async () => {
      await response.promise;
    });
    expect(screen.queryByRole("status")).toBeNull();
  });

  // A destination the hub cannot fingerprint has no assertion to send, and an
  // unasserted probe would dial whatever the name resolves to now: the test is
  // refused before any RPC is sent, with an error toast and no pending state.
  test("the test refuses a destination the hub cannot fingerprint", async () => {
    const fake = connectFakeClient();
    const unkeyed = instance({
      name: "work",
      providerId: "anthropic",
      baseUrl: "https://unkeyed.example/v1",
    });
    fake.on("evener/instance/list", () => ({ instances: [unkeyed], availableProviders: [] }));
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.click(within(inspector).getByRole("button", { name: "Test credentials" }));

    expect(fake.calls.filter((call) => call.method === "evener/auth/test")).toEqual([]);
    await screen.findByText(FINGERPRINT_UNAVAILABLE_TEST_MESSAGE);
    expect(isRefused(within(inspector).getByRole("button", { name: "Test credentials" }))).toBe(false);
  });

  // roborev PR #1136: the probe asserts the destination the row was read from,
  // so the hub can refuse a check whose name has since been re-pointed to an
  // unreviewed endpoint instead of sending the stored credential there.
  test("the test asserts the selected instance's fingerprint", async () => {
    const fake = connectFakeClient();
    const WORK_FP = { ...WORK, endpointFingerprint: "fp-work" };
    fake.on("evener/instance/list", () => ({ instances: [WORK_FP], availableProviders: [] }));
    fake.on("evener/auth/test", (params) => {
      expect(params).toEqual({ provider: "work", expectedEndpointFingerprint: "fp-work" });
      return { provider: "work", status: "success", message: "Credentials verified." };
    });
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.click(within(inspector).getByRole("button", { name: "Test credentials" }));

    await vi.waitFor(() => {
      const calls = fake.calls.filter((call) => call.method === "evener/auth/test");
      expect(calls).toHaveLength(1);
      expect(calls[0]?.params).toEqual({ provider: "work", expectedEndpointFingerprint: "fp-work" });
    });
  });

  // roborev PR #1136: the hub refusing the asserted destination is not an
  // endpoint failure - the name moved since this listing was read, so there is
  // no honest probe result to show. Report the changed connection, clear the
  // pending test, and re-read the listing so a retry asserts the destination
  // now on screen rather than the one the credential was aimed at.
  // A pending test was issued against the listing a replacement took away: its
  // answer describes rows of a connection that is gone, and until the new
  // listing lands nothing else clears it - the row's own "Testing credentials…"
  // state would sit there, and a late answer would be shown as a result for
  // rows this connection never read.
  test("a client replacement clears a pending credential test and drops its late answer", async () => {
    const fake = connectFakeClient();
    const pending = deferred<AuthTestResponse>();
    fake.on("evener/instance/list", () => LIST);
    fake.on("evener/auth/test", () => pending.promise);
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.click(within(inspector).getByRole("button", { name: "Test credentials" }));
    expect(isRefused(within(inspector).getByRole("button", { name: "Testing credentials…" }))).toBe(true);

    // The connection is replaced and its own listing is held open, so the rows
    // on screen are still the ones the pending test was issued against.
    let finishRestore!: (value: InstanceListResponse) => void;
    const replacement = new FakeClient("ready");
    replacement.on(
      "evener/instance/list",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishRestore = resolve;
        }),
    );
    await act(async () => connectionStore.getState().connect(replacement));
    expect(credentialsStore.getState().listingFromPreviousConnection).toBe(true);

    // The pending test is released rather than left as this row's state...
    await waitFor(() =>
      expect(isRefused(within(inspector).getByRole("button", { name: "Test credentials" }))).toBe(false),
    );

    // ...and its answer, which describes the connection that is gone, is not
    // shown as a result for the rows still on screen.
    await act(async () => pending.resolve({ provider: "work", status: "success", message: "Credentials verified." }));
    expect(screen.queryByText("Credentials verified.")).toBeNull();

    await act(async () => finishRestore(LIST));
    await waitFor(() => expect(credentialsStore.getState().listingFromPreviousConnection).toBe(false));
  });

  test("a refused assertion is reported as a changed connection and re-reads the listing", async () => {
    const fake = connectFakeClient();
    const WORK_FP = { ...WORK, endpointFingerprint: "fp-work" };
    let listCalls = 0;
    fake.on("evener/instance/list", () => {
      listCalls += 1;
      return { instances: [WORK_FP], availableProviders: [] };
    });
    fake.on("evener/auth/test", () => {
      throw new WireError("endpoint changed", -32013, { evenerErrorInfo: ErrorEndpointConflict });
    });
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("work");
    const callsBefore = listCalls;
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.click(within(inspector).getByRole("button", { name: "Test credentials" }));

    await screen.findByText(ENDPOINT_CHANGED_TEST_MESSAGE);
    expect(screen.queryByText(/provider endpoint could not be reached/)).toBeNull();
    // The refused assertion is not a result: the pending test clears and the
    // action returns to its idle label.
    await waitFor(() =>
      expect(isRefused(within(inspector).getByRole("button", { name: "Test credentials" }))).toBe(false),
    );
    // A retry has to assert the destination now on screen, so the listing is
    // re-read after the refusal.
    await waitFor(() => expect(listCalls).toBeGreaterThan(callsBefore));
  });
});

// The store refuses writes and probes issued from the previous connection's
// listing (stores/credentials.ts's requireWritableClient): the rows on screen
// name instances of a connection that is gone. Every section action that can
// hit that refusal reports the change it is and asks for this connection's
// listing - never as a failure of the action the user asked for, and never in
// the store's own words.
describe("actions refused while the held listing belongs to a replaced connection", () => {
  /** Renders the section with a listing on screen, then replaces the client the
   * way a reconnect does and holds its own read open: the rows on screen were
   * read by the connection that is gone, this one's listing has not been applied,
   * and the marker is set exactly as the store's own replacement path sets it
   * (stores/credentials.ts's connectionStore subscription). The rows stay mounted
   * through the read, so this is the real window rather than a seeded copy of it.
   */
  async function renderWithReplacedConnection(): Promise<{
    replacement: FakeClient;
    /** Answers the replacement's held listing read, the way a real reconnect's
     * read lands, and waits for the marker it clears. */
    release: () => Promise<void>;
  }> {
    const first = connectFakeClient();
    first.on("evener/instance/list", () => LIST);
    let finishRestore!: (value: InstanceListResponse) => void;
    const restore = new Promise<InstanceListResponse>((resolve) => {
      finishRestore = resolve;
    });
    const replacement = new FakeClient("ready");
    replacement.on("evener/instance/list", () => restore);
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("work");
    await act(async () => connectionStore.getState().connect(replacement));
    expect(credentialsStore.getState().listingFromPreviousConnection).toBe(true);
    expect(screen.getByText("work")).toBeTruthy();
    return {
      replacement,
      release: async () => {
        await act(async () => finishRestore(LIST));
        await waitFor(() => expect(credentialsStore.getState().listingFromPreviousConnection).toBe(false));
      },
    };
  }

  test("a credential test is refused with the change, clears its pending state, and re-reads the listing", async () => {
    const { replacement, release } = await renderWithReplacedConnection();
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.click(within(inspector).getByRole("button", { name: "Test credentials" }));

    // No probe reached the replacement connection - the test was refused, not
    // run against a destination this connection never read.
    expect(replacement.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(0);
    await screen.findByText(CONNECTION_REPLACED_ERROR);
    expect(screen.queryByText(/provider endpoint could not be reached/)).toBeNull();
    // The pending test clears, so the action does not sit in "Testing…".
    await waitFor(() =>
      expect(isRefused(within(inspector).getByRole("button", { name: "Test credentials" }))).toBe(false),
    );
    // The refusal asked for this connection's own listing, and that read is
    // what reopens the action.
    await release();

    // The same action now goes out and is answered by this connection.
    replacement.on("evener/auth/test", () => ({
      provider: "work",
      status: "success",
      message: "Credentials verified.",
    }));
    await user.click(within(inspector).getByRole("button", { name: "Test credentials" }));
    await waitFor(() => expect(replacement.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(1));
  });

  test("make default is refused with the change, not reported as a failed action", async () => {
    const { replacement } = await renderWithReplacedConnection();
    const user = userEvent.setup();
    const inspector = await openSheet(user, "personal");
    await user.click(within(inspector).getByRole("button", { name: /make default/i }));

    expect(replacement.calls.filter((call) => call.method === "evener/instance/setDefault")).toHaveLength(0);
    await screen.findByText(CONNECTION_REPLACED_ERROR);
    expect(screen.queryByText(/Set default failed/)).toBeNull();
  });

  test("starting a sign-in is refused with the change, not reported as a failed sign-in", async () => {
    const { replacement } = await renderWithReplacedConnection();
    const user = userEvent.setup();
    const inspector = await openSheet(user, "personal");
    await user.click(within(inspector).getByRole("button", { name: "Sign in…" }));

    expect(replacement.calls.filter((call) => call.method === "evener/auth/device/start")).toHaveLength(0);
    expect(replacement.calls.filter((call) => call.method === "evener/auth/login/start")).toHaveLength(0);
    await screen.findByText(CONNECTION_REPLACED_ERROR);
    expect(screen.queryByText(/Sign-in failed/)).toBeNull();
  });

  // Every instance-scoped write goes through the same gate, the model toggle
  // included: an open sheet over a replaced connection's rows could otherwise
  // flip a model by name against the hub that is there now.
  test("a model toggle from a replaced connection's listing is refused with the change", async () => {
    const fake = connectFakeClient();
    const row = { ...WORK, models: [{ id: "claude-opus-4-6", disabled: false }] };
    fake.on("evener/instance/list", () => ({ instances: [row], availableProviders: [] }));
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("work");

    // The replacement answers its own listing read (so the rows stay on screen
    // rather than the skeleton) and every write path a wrongly-ungated toggle
    // would reach; the stale state is then the one between a replacement and the
    // listing this connection would read.
    const replacement = new FakeClient("ready");
    replacement.on("evener/instance/list", () => ({ instances: [row], availableProviders: [] }));
    replacement.on("evener/instance/setModelDisabled", () => ({ instances: [row], availableProviders: [] }));
    await act(async () => connectionStore.getState().connect(replacement));
    await waitFor(() => expect(credentialsStore.getState().listingFromPreviousConnection).toBe(false));
    await act(async () => credentialsStore.setState({ listingFromPreviousConnection: true }));

    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.click(within(inspector).getByRole("switch", { name: "claude-opus-4-6" }));

    expect(replacement.calls.filter((call) => call.method === "evener/instance/setModelDisabled")).toHaveLength(0);
    await screen.findByText(CONNECTION_REPLACED_ERROR);
    expect(screen.queryByText(/Model toggle failed/)).toBeNull();
  });

  test("a confirm-gated removal is refused with the change, not reported as a failed removal", async () => {
    const { replacement } = await renderWithReplacedConnection();
    const user = userEvent.setup();
    const inspector = await openSheet(user, "personal");
    await user.click(within(inspector).getByRole("button", { name: "Remove" }));
    const dialog = screen.getByRole("dialog", { name: "Remove instance" });
    await user.click(within(dialog).getByRole("button", { name: "Remove" }));

    expect(replacement.calls.filter((call) => call.method === "evener/instance/remove")).toHaveLength(0);
    await screen.findByText(CONNECTION_REPLACED_ERROR);
    expect(screen.queryByText(/Remove failed/)).toBeNull();
    // The confirmation carried the fingerprint the row showed on the connection
    // that is gone, so it closes: the retry captures the fresh row instead of
    // retrying with a stale assertion.
    expect(screen.queryByRole("dialog", { name: "Remove instance" })).toBeNull();
  });
});

describe("single-open-editor invariant", () => {
  test("opening the Add form, then Replace key from a row's sheet, replaces it (only one editor open at a time)", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    render(<CredentialsSection sectionId="credentials" fullEditor />);
    await screen.findByText("work");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "+ Add provider instance" }));
    expect(screen.getByRole("dialog", { name: "Add provider instance" })).toBeTruthy();
    const inspector = await openSheet(user, "work");
    await user.click(within(inspector).getByRole("button", { name: "Replace key" }));
    expect(screen.queryByRole("dialog", { name: "Add provider instance" })).toBeNull();
    expect(screen.queryByRole("dialog", { name: "work" })).toBeNull();
    expect(screen.getByRole("dialog", { name: "Set API key for work" })).toBeTruthy();
  });

  // An open API-key editor stops rendering if its instance disappears.
  test("an API key dialog closes when a refreshed list removes its instance", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.click(within(inspector).getByRole("button", { name: /replace key/i }));
    await screen.findByRole("dialog", { name: "Set API key for work" });

    act(() => credentialsStore.setState({ instances: [PERSONAL] }));

    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Set API key for work" })).toBeNull());
  });
});

describe("OAuth start branches", () => {
  test("fallback:true opens the redirect (paste-back) editor using loginStart's own flowId/url", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    fake.on("evener/auth/device/start", () => ({
      provider: "personal",
      flowId: "device-flow",
      userCode: "X",
      verificationUrl: "https://verify",
      intervalSeconds: 5,
      fallback: true,
    }));
    fake.on("evener/auth/login/start", () => ({
      provider: "personal",
      flowId: "redirect-flow",
      url: "https://auth/start",
    }));
    const anchors = captureNewTabs();
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("personal");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "personal");
    await user.click(within(inspector).getByRole("button", { name: "Sign in…" }));
    await screen.findByRole("dialog", { name: "Sign in to personal" });
    expect(screen.queryByRole("dialog", { name: "personal" })).toBeNull();
    expect(openedNewTab(anchors)).toEqual({
      url: "https://auth/start",
      target: "_blank",
      rel: NEW_TAB_POLICY,
    });
    expect(screen.getByRole("link", { name: /re-open authorize url/i })).toBeTruthy();
  });

  test("fallback:false/absent opens the device-code editor", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    fake.on("evener/auth/device/start", () => ({
      provider: "personal",
      flowId: "device-flow",
      userCode: "ABCD-EFGH",
      verificationUrl: "https://verify",
      intervalSeconds: 5,
    }));
    fake.on("evener/auth/device/poll", () => ({ state: "pending" }));
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("personal");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "personal");
    await user.click(within(inspector).getByRole("button", { name: "Sign in…" }));
    await screen.findByText("ABCD-EFGH");
  });

  test("a deviceStart failure toasts 'Sign-in failed' and opens no editor", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    fake.on("evener/auth/device/start", () => {
      throw new Error("provider unavailable");
    });
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("personal");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "personal");
    await user.click(within(inspector).getByRole("button", { name: "Sign in…" }));
    // error is converted via friendlyErrorMessage: raw JS errors become the generic message
    await screen.findByText("Sign-in failed: Something went wrong.");
    // Assert the raw string no longer appears
    expect(screen.queryByText(/provider unavailable/)).toBeNull();
    // No dialog of any kind: the inspector closed on the click, and the
    // failed start must not open (or reopen) anything.
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  // Proves the key={flowId} teardown DeviceCodeDialog's own doc comment
  // claims: expiring flow A, then "Start again" (a fresh evener/auth/device/
  // start -> a NEW flowId, same openEditor.kind==="device" throughout) must
  // both (a) reset DeviceCodeDialog's own local UI state (copied/expired/
  // error) rather than leaking flow A's "expired" straight into flow B's
  // first render, and (b) leave flow A's poll timer genuinely dead. Neither
  // holds for free: DeviceCodeDialog's internal poll effect already
  // restarts on a bare flowId prop change (flowId is one of its own deps),
  // which is enough to make (b) true even WITHOUT the key - only (a)
  // actually depends on key forcing a real remount (a mere prop update
  // would keep the same component instance, and therefore its stale local
  // state, across the transition).
  test("abandoning an expired device flow and starting a new one resets to a fresh state, not flow A's leftover 'expired' UI - and flow A's timer stays dead", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    let deviceStartCalls = 0;
    fake.on("evener/auth/device/start", () => {
      deviceStartCalls += 1;
      return deviceStartCalls === 1
        ? {
            provider: "personal",
            flowId: "flow-A",
            userCode: "AAAA-1111",
            verificationUrl: "https://verify",
            intervalSeconds: 1,
          }
        : {
            provider: "personal",
            flowId: "flow-B",
            userCode: "BBBB-2222",
            verificationUrl: "https://verify",
            intervalSeconds: 1,
          };
    });
    const pollCalls: string[] = [];
    fake.on("evener/auth/device/poll", (params) => {
      pollCalls.push(params.flowId);
      return params.flowId === "flow-A" ? { state: "expired" } : { state: "pending" };
    });
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("personal");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "personal");
    // The stubbed `jest` global lets Testing Library's waitFor advance the fake
    // clock instead of polling on real time.
    vi.stubGlobal("jest", { advanceTimersByTime: vi.advanceTimersByTime });
    vi.useFakeTimers();
    const request = vi.spyOn(fake, "request");
    await act(async () => {
      const requestIndex = request.mock.calls.length;
      fireEvent.click(within(inspector).getByRole("button", { name: "Sign in…" }));
      const started = request.mock.results[requestIndex];
      if (started?.type !== "return") throw new Error("Sign in did not start the device flow request");
      expect(request.mock.calls[requestIndex]?.[0]).toBe("evener/auth/device/start");
      await started.value;
    });
    await waitFor(() => expect(screen.getByText("AAAA-1111")).toBeTruthy());

    // Flow A expires.
    await advanceTime(1000);
    expect(screen.getByText(/Code expired/)).toBeTruthy();
    await act(async () => {
      const requestIndex = request.mock.calls.length;
      fireEvent.click(screen.getByRole("button", { name: "Start again" }));
      const started = request.mock.results[requestIndex];
      if (started?.type !== "return") throw new Error("Start again did not start a new device flow request");
      expect(request.mock.calls[requestIndex]?.[0]).toBe("evener/auth/device/start");
      await started.value;
    });

    // Flow B starts fresh: its own code, NOT flow A's leftover expired state.
    await waitFor(() => expect(screen.getByText("BBBB-2222")).toBeTruthy());
    expect(screen.queryByText(/Code expired/)).toBeNull();
    expect(screen.getByRole("button", { name: /copy code/i })).toBeTruthy();

    // Flow B is genuinely polling under its own flowId.
    await advanceTime(1000);
    expect(pollCalls).toContain("flow-B");
    const flowACallsAtSwitch = pollCalls.filter((id) => id === "flow-A").length;

    await advanceTime(2200);
    expect(pollCalls.filter((id) => id === "flow-A").length).toBe(flowACallsAtSwitch);
  });
});

describe("set default", () => {
  test("calls instanceSetDefault directly with no confirm dialog and no success toast", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    fake.on("evener/instance/setDefault", (params) => {
      expect(params).toEqual({ name: "personal", originClientId: "test-tab" });
      return { instances: [WORK, { ...PERSONAL, isDefault: true }], availableProviders: [] };
    });
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("personal");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "personal");
    await user.click(within(inspector).getByRole("button", { name: /make default/i }));
    await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/instance/setDefault")).toBe(true));
    expect(screen.queryByRole("alert")).toBeNull();
  });

  test("a setDefault failure toasts 'Set default failed'", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    fake.on("evener/instance/setDefault", () => {
      throw new Error("boom");
    });
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("personal");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "personal");
    await user.click(within(inspector).getByRole("button", { name: /make default/i }));
    // error is converted via friendlyErrorMessage: raw JS errors become the generic message
    await screen.findByText("Set default failed: Something went wrong.");
    // Assert the raw string no longer appears
    expect(screen.queryByText(/boom/)).toBeNull();
  });
});

describe("model live refresh", () => {
  test("opening a sheet does not fetch; the Refresh button does and merges", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    fake.on("evener/instance/refreshModels", (params) => {
      expect(params).toEqual({ name: "work", originClientId: "test-tab" });
      return {
        instances: [{ ...WORK, models: [...(WORK.models ?? []), { id: "claude-live-new" }] }],
        availableProviders: [],
      };
    });
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    expect(fake.calls.some((c) => c.method === "evener/instance/refreshModels")).toBe(false);
    await user.click(within(inspector).getByRole("button", { name: "Refresh live models" }));
    await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/instance/refreshModels")).toBe(true));
    await within(inspector).findByRole("switch", { name: "claude-live-new" });
  });

  test("an instance with no inventory still offers the Refresh button", async () => {
    const fake = connectFakeClient();
    const bare = instance({ name: "work", providerId: "anthropic", authModes: ["apiKey"] });
    fake.on("evener/instance/list", () => ({ instances: [bare], availableProviders: [] }));
    fake.on("evener/instance/refreshModels", (params) => {
      expect(params).toEqual({ name: "work", originClientId: "test-tab" });
      return {
        instances: [{ ...bare, models: [{ id: "claude-live-new" }] }],
        availableProviders: [],
      };
    });
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.click(within(inspector).getByRole("button", { name: "Refresh live models" }));
    await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/instance/refreshModels")).toBe(true));
    await within(inspector).findByRole("switch", { name: "claude-live-new" });
  });

  test("two concurrent refreshes each track their own pending state", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    const gates = new Map<string, ReturnType<typeof deferred<InstanceListResponse>>>();
    fake.on("evener/instance/refreshModels", (params: { name: string }) => {
      const gate = deferred<InstanceListResponse>();
      gates.set(params.name, gate);
      return gate.promise;
    });
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    // Start work's refresh, then personal's while work's is still in
    // flight: both sheets must show pending independently.
    const workInspector = await openSheet(user, "work");
    await user.click(within(workInspector).getByRole("button", { name: "Refresh live models" }));
    await user.click(within(workInspector).getByRole("button", { name: "Close" }));
    const personalInspector = await openSheet(user, "personal");
    await user.click(within(personalInspector).getByRole("button", { name: "Refresh live models" }));
    await within(personalInspector).findByRole("button", { name: "Refreshing live models…" });
    // Settle personal's first: work's must still read pending.
    gates.get("personal")?.resolve({ instances: [WORK, PERSONAL], availableProviders: [] });
    await waitFor(() =>
      expect(within(personalInspector).queryByRole("button", { name: "Refreshing live models…" })).toBeNull(),
    );
    await user.click(within(personalInspector).getByRole("button", { name: "Close" }));
    const workAgain = await openSheet(user, "work");
    expect(within(workAgain).getByRole("button", { name: "Refreshing live models…" })).toBeTruthy();
    gates.get("work")?.resolve({ instances: [WORK, PERSONAL], availableProviders: [] });
    await within(workAgain).findByRole("button", { name: "Refresh live models" });
  });

  test("a refresh in flight for another instance does not disable this sheet's button", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    const gate = deferred<InstanceListResponse>();
    fake.on("evener/instance/refreshModels", () => gate.promise);
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    // Start a refresh on personal's sheet, then open work's sheet while it
    // is still in flight: work's button must stay enabled.
    const personalInspector = await openSheet(user, "personal");
    await user.click(within(personalInspector).getByRole("button", { name: "Refresh live models" }));
    await user.click(within(personalInspector).getByRole("button", { name: "Close" }));
    const workInspector = await openSheet(user, "work");
    expect(isRefused(within(workInspector).getByRole("button", { name: "Refresh live models" }))).toBe(false);
    gate.resolve({ instances: [WORK, PERSONAL], availableProviders: [] });
    await waitFor(() =>
      expect(isRefused(within(workInspector).getByRole("button", { name: "Refresh live models" }))).toBe(false),
    );
  });

  test("a refresh failure toasts and keeps the cached rows", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    fake.on("evener/instance/refreshModels", () => {
      throw new Error("boom");
    });
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.click(within(inspector).getByRole("button", { name: "Refresh live models" }));
    await screen.findByText("Live refresh failed: Something went wrong.");
    expect(screen.queryByText(/boom/)).toBeNull();
    // Catalog rows from the list fetch still render.
    within(inspector).getByRole("switch", { name: "claude-opus-4-6" });
  });
});

describe("model toggles", () => {
  test("flipping a switch calls setModelDisabled and applies the returned list", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    fake.on("evener/instance/setModelDisabled", (params) => {
      expect(params).toEqual({ name: "work", model: "claude-opus-4-6", disabled: true, originClientId: "test-tab" });
      return {
        instances: [
          {
            ...WORK,
            models: [
              { id: "claude-opus-4-6", disabled: true },
              { id: "claude-sonnet-5", disabled: true },
            ],
          },
        ],
        availableProviders: [],
      };
    });
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.click(within(inspector).getByRole("switch", { name: "claude-opus-4-6" }));
    await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/instance/setModelDisabled")).toBe(true));
    await screen.findByText("Disabled claude-opus-4-6");
  });

  test("a switch disables while its toggle is in flight", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    let release!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/setModelDisabled",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          release = resolve;
        }),
    );
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    const toggle = within(inspector).getByRole("switch", { name: "claude-opus-4-6" });
    await user.click(toggle);
    // While the write is out, the same switch is refused with aria-disabled,
    // never the native attribute: a control the click itself disables would drop
    // the keyboard to <body> (and the pane's focus recovery would then scroll
    // the sheet to wherever it put it back). A second rapid click still cannot
    // submit a duplicate write.
    await waitFor(() =>
      expect(isRefused(within(inspector).getByRole("switch", { name: "claude-opus-4-6" }))).toBe(true),
    );
    release({ instances: [{ ...WORK, models: [{ id: "claude-opus-4-6", disabled: true }] }], availableProviders: [] });
    await screen.findByText("Disabled claude-opus-4-6");
    await waitFor(() =>
      expect(isRefused(within(inspector).getByRole("switch", { name: "claude-opus-4-6" }))).toBe(false),
    );
  });

  // The click that starts a toggle is what disables the switch it was made on,
  // and a natively disabled control cannot hold the keyboard (Chrome drops it to
  // <body>): the pane's focus recovery then had to put focus back somewhere
  // else, scrolling the sheet to that control - the jump this pins away.
  test("a toggle in flight keeps the keyboard on its switch", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    let release!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/setModelDisabled",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          release = resolve;
        }),
    );
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    const toggle = within(inspector).getByRole("switch", { name: "claude-opus-4-6" });

    await user.click(toggle);
    await waitFor(() =>
      expect(within(inspector).getByRole("switch", { name: "claude-opus-4-6" }).getAttribute("aria-disabled")).toBe(
        "true",
      ),
    );
    expect(document.activeElement).toBe(within(inspector).getByRole("switch", { name: "claude-opus-4-6" }));

    release({ instances: [{ ...WORK, models: [{ id: "claude-opus-4-6", disabled: true }] }], availableProviders: [] });
    await screen.findByText("Disabled claude-opus-4-6");
  });

  // The same property for the two buttons whose own click starts their work:
  // the in-flight state is a refusal, so the button that was clicked keeps the
  // keyboard instead of dropping it to <body> (see widgets/switch).
  test("clicking Test credentials keeps the keyboard on it while its probe is out", async () => {
    const fake = connectFakeClient();
    const probe = deferred<AuthTestResponse>();
    fake.on("evener/instance/list", () => LIST);
    fake.on("evener/auth/test", () => probe.promise);
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.click(within(inspector).getByRole("button", { name: "Test credentials" }));

    await waitFor(() =>
      expect(isRefused(within(inspector).getByRole("button", { name: "Testing credentials…" }))).toBe(true),
    );
    expect(document.activeElement).toBe(within(inspector).getByRole("button", { name: "Testing credentials…" }));

    probe.resolve({ provider: "work", status: "success", message: "Credentials verified." });
    expect((await screen.findByRole("status")).textContent).toContain("Credentials verified.");
  });

  test("clicking Refresh live models keeps the keyboard on it while the read is out", async () => {
    const fake = connectFakeClient();
    const row = { ...WORK, models: [{ id: "claude-opus-4-6", disabled: false }] };
    const refresh = deferred<InstanceListResponse>();
    fake.on("evener/instance/list", () => ({ instances: [row], availableProviders: [] }));
    fake.on("evener/instance/refreshModels", () => refresh.promise);
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.click(within(inspector).getByRole("button", { name: "Refresh live models" }));

    await waitFor(() =>
      expect(isRefused(within(inspector).getByRole("button", { name: "Refreshing live models…" }))).toBe(true),
    );
    expect(document.activeElement).toBe(within(inspector).getByRole("button", { name: "Refreshing live models…" }));

    refresh.resolve({ instances: [row], availableProviders: [] });
    await act(async () => {
      await refresh.promise;
    });
  });

  test("two clicks in the same tick submit one toggle", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    let writes = 0;
    fake.on(
      "evener/instance/setModelDisabled",
      () =>
        new Promise<InstanceListResponse>(() => {
          writes += 1;
        }),
    );
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    const toggle = within(inspector).getByRole("switch", { name: "claude-opus-4-6" });
    // Two clicks with nothing awaited between them. The switch only renders
    // disabled on the next render, so the guard itself has to be
    // synchronous — a state updater inspected after the fact cannot stop
    // the second submission.
    await act(async () => {
      fireEvent.click(toggle);
      fireEvent.click(toggle);
    });
    expect(writes).toBe(1);
  });

  test("a toggle failure toasts 'Model toggle failed'", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    fake.on("evener/instance/setModelDisabled", () => {
      throw new Error("boom");
    });
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.click(within(inspector).getByRole("switch", { name: "claude-opus-4-6" }));
    // error is converted via friendlyErrorMessage: raw JS errors become the generic message
    await screen.findByText("Model toggle failed: Something went wrong.");
    // Assert the raw string no longer appears
    expect(screen.queryByText(/boom/)).toBeNull();
  });
});

describe("Clear / Clear stored key / Remove confirm dialogs", () => {
  test("Clear opens a ConfirmDialog naming the instance; confirming calls authLogout then refreshes", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    fake.on("evener/auth/logout", (params) => {
      expect(params).toEqual({ provider: "work", originClientId: "test-tab" });
      return {
        removed: true,
        status: { provider: "work", supported: true, signedIn: false, activeSource: "none", hasStoredOAuth: false },
      };
    });
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    // WORK carries hasStoredFile+activeSource:"store" in the shared fixture,
    // so its sheet already offers Clear.
    const inspector = await openSheet(user, "work");
    await user.click(within(inspector).getByRole("button", { name: "Clear" }));
    const dialog = screen.getByRole("dialog", { name: "Clear credentials" });
    expect(dialog).toBeTruthy();
    // The sheet's own Clear button is still present behind the confirm, so
    // scope this second click to the dialog's own Clear/confirm button.
    await user.click(within(dialog).getByRole("button", { name: "Clear" }));
    await screen.findByText("Credentials cleared for work");
  });

  test("a cleared credential whose listing read is lost is still reported as cleared", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    let logout!: (value: AuthLogoutResponse) => void;
    fake.on(
      "evener/auth/logout",
      () =>
        new Promise<AuthLogoutResponse>((resolve) => {
          logout = resolve;
        }),
    );
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.click(within(inspector).getByRole("button", { name: "Clear" }));
    const dialog = screen.getByRole("dialog", { name: "Clear credentials" });
    await user.click(within(dialog).getByRole("button", { name: "Clear" }));
    // Dropped connection during the clear: the logout succeeded, and the
    // follow-up listing read rejecting must not report "Clear failed".
    await act(async () => {
      connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
    });
    await act(async () => {
      logout({
        removed: true,
        status: { provider: "work", supported: true, signedIn: false, activeSource: "none", hasStoredOAuth: false },
      });
    });
    // The handler continues past the lost listing read over a few more
    // microtasks; flush them inside act before asserting.
    await act(async () => {});
    await screen.findByText("Credentials cleared for work");
    expect(screen.queryByText(/Clear failed/)).toBeNull();
  });

  // #713: a stray stored key shadowed behind an active OAuth login needs an
  // affordance that clears the key without dropping the login - distinct
  // from Clear (authLogout), which for a signed-in Codex row would remove
  // the OAuth record instead.
  test("Clear stored key opens a ConfirmDialog naming the instance; confirming calls clearStoredKey then refreshes", async () => {
    const fake = connectFakeClient();
    const SHADOWED = instance({
      name: "shadowed",
      providerId: "openai-codex",
      auth: "oauth-openai-codex",
      authModes: ["oauth"],
      activeSource: "oauth",
      hasStoredOAuth: true,
      hasStoredFile: true,
    });
    fake.on("evener/instance/list", () => ({ instances: [SHADOWED], availableProviders: [] }));
    fake.on("evener/auth/apiKey/clear", (params) => {
      expect(params).toEqual({ provider: "shadowed", originClientId: "test-tab" });
      return { provider: "shadowed", supported: true, signedIn: true, activeSource: "oauth", hasStoredOAuth: true };
    });
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("shadowed");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "shadowed");
    // This row is signed in via OAuth (showClear also true), so both Clear
    // and Clear stored key render - assert the narrower action reaches the
    // narrower RPC, leaving the login alone.
    await user.click(within(inspector).getByRole("button", { name: "Clear stored key" }));
    const dialog = screen.getByRole("dialog", { name: "Clear stored key" });
    expect(dialog).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "Clear" }));
    await screen.findByText("Stored key cleared for shadowed");
  });

  // roborev round 3, F3: a gcp-adc instance's stored credential is a JSON
  // document, not an API key - the confirm dialog and success toast must
  // call it that, mirroring the flow above.
  test("Clear stored credential JSON for a gcp-adc instance opens a ConfirmDialog naming the credential JSON; confirming calls clearStoredKey then refreshes", async () => {
    const fake = connectFakeClient();
    const VERTEX = instance({
      name: "vertex",
      providerId: "google-vertex",
      auth: "gcp-adc",
      authModes: ["adc", "credentialJson"],
      activeSource: "adc",
      hasStoredFile: true,
    });
    fake.on("evener/instance/list", () => ({ instances: [VERTEX], availableProviders: [] }));
    fake.on("evener/auth/apiKey/clear", (params) => {
      expect(params).toEqual({ provider: "vertex", originClientId: "test-tab" });
      return {
        provider: "vertex",
        supported: true,
        signedIn: true,
        activeSource: "adc",
        hasStoredOAuth: false,
        hasStoredFile: false,
      };
    });
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("vertex");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "vertex");
    await user.click(within(inspector).getByRole("button", { name: "Clear stored credential JSON" }));
    const dialog = screen.getByRole("dialog", { name: "Clear stored credential JSON" });
    expect(dialog).toBeTruthy();
    expect(within(dialog).getByText(/credential JSON for "vertex"/)).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "Clear" }));
    await screen.findByText("Stored credential JSON cleared for vertex");
  });

  test("Remove opens a ConfirmDialog; confirming calls instanceRemove", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    fake.on("evener/instance/remove", (params) => {
      expect(params).toEqual({ name: "personal", originClientId: "test-tab" });
      return { instances: [WORK], availableProviders: [] };
    });
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("personal");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "personal");
    await user.click(within(inspector).getByRole("button", { name: "Remove" }));
    const dialog = screen.getByRole("dialog", { name: "Remove instance" });
    expect(dialog).toBeTruthy();
    // The sheet's own Remove button is still present behind the confirm, so
    // scope this second click to the dialog's own Remove/confirm button.
    await user.click(within(dialog).getByRole("button", { name: "Remove" }));
    await screen.findByText("Removed instance personal");
  });

  // A confirmation is issued against the instance the user was looking at:
  // the row's endpoint fingerprint travels with the action so the hub refuses
  // a name that now resolves elsewhere instead of clearing or removing a
  // replacement's credentials/configuration.
  test("Clear sends the endpoint fingerprint of the row the confirm was opened for", async () => {
    const fake = connectFakeClient();
    const WORK_FP = { ...WORK, endpointFingerprint: "fp-work" };
    fake.on("evener/instance/list", () => ({ instances: [WORK_FP], availableProviders: [] }));
    fake.on("evener/auth/logout", (params) => {
      expect(params).toEqual({ provider: "work", expectedEndpointFingerprint: "fp-work", originClientId: "test-tab" });
      return {
        removed: true,
        status: { provider: "work", supported: true, signedIn: false, activeSource: "none", hasStoredOAuth: false },
      };
    });
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.click(within(inspector).getByRole("button", { name: "Clear" }));
    const dialog = screen.getByRole("dialog", { name: "Clear credentials" });
    await user.click(within(dialog).getByRole("button", { name: "Clear" }));
    await screen.findByText("Credentials cleared for work");
  });

  test("Clear stored key sends the endpoint fingerprint of the row the confirm was opened for", async () => {
    const fake = connectFakeClient();
    const SHADOWED = instance({
      name: "shadowed",
      providerId: "openai-codex",
      auth: "oauth-openai-codex",
      authModes: ["oauth"],
      activeSource: "oauth",
      hasStoredOAuth: true,
      hasStoredFile: true,
      endpointFingerprint: "fp-shadowed",
    });
    fake.on("evener/instance/list", () => ({ instances: [SHADOWED], availableProviders: [] }));
    fake.on("evener/auth/apiKey/clear", (params) => {
      expect(params).toEqual({
        provider: "shadowed",
        expectedEndpointFingerprint: "fp-shadowed",
        originClientId: "test-tab",
      });
      return { provider: "shadowed", supported: true, signedIn: true, activeSource: "oauth", hasStoredOAuth: true };
    });
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("shadowed");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "shadowed");
    await user.click(within(inspector).getByRole("button", { name: "Clear stored key" }));
    const dialog = screen.getByRole("dialog", { name: "Clear stored key" });
    await user.click(within(dialog).getByRole("button", { name: "Clear" }));
    await screen.findByText("Stored key cleared for shadowed");
  });

  test("Remove sends the endpoint fingerprint of the row the confirm was opened for", async () => {
    const fake = connectFakeClient();
    const PERSONAL_FP = { ...PERSONAL, endpointFingerprint: "fp-personal" };
    fake.on("evener/instance/list", () => ({ instances: [WORK, PERSONAL_FP], availableProviders: [] }));
    fake.on("evener/instance/remove", (params) => {
      expect(params).toEqual({
        name: "personal",
        expectedEndpointFingerprint: "fp-personal",
        originClientId: "test-tab",
      });
      return { instances: [WORK], availableProviders: [] };
    });
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("personal");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "personal");
    await user.click(within(inspector).getByRole("button", { name: "Remove" }));
    const dialog = screen.getByRole("dialog", { name: "Remove instance" });
    await user.click(within(dialog).getByRole("button", { name: "Remove" }));
    await screen.findByText("Removed instance personal");
  });

  // The confirm-gated actions assert the listed row's endpoint fingerprint, and
  // the hub refuses a stale one with the endpoint-conflict discriminant. The
  // confirmation holds the destination that moved, so the retry cannot succeed
  // unless the section closes it, clears the stale selection, re-reads the
  // listing, and lets the next confirmation capture the refreshed fingerprint -
  // the same recovery the mobile and TUI clients make.
  test("a removal refused for a moved endpoint closes the confirmation, re-reads, and warns", async () => {
    const fake = connectFakeClient();
    const PERSONAL_FP = { ...PERSONAL, endpointFingerprint: "fp-old" };
    const PERSONAL_MOVED = { ...PERSONAL, endpointFingerprint: "fp-new" };
    // The first listing is the row the confirmation is opened against; the read
    // the refusal asks for is the one that reports the row re-pointed.
    let refused = false;
    fake.on("evener/instance/list", () => ({
      instances: [refused ? PERSONAL_MOVED : PERSONAL_FP],
      availableProviders: [],
    }));
    fake.on("evener/instance/remove", () => {
      refused = true;
      throw new WireError("personal no longer resolves to the endpoint this confirmation was opened on", -32013, {
        evenerErrorInfo: ErrorEndpointConflict,
      });
    });
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("personal");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "personal");
    await user.click(within(inspector).getByRole("button", { name: "Remove" }));
    const dialog = screen.getByRole("dialog", { name: "Remove instance" });
    const listingsBefore = fake.calls.filter((call) => call.method === "evener/instance/list").length;
    await user.click(within(dialog).getByRole("button", { name: "Remove" }));

    // The confirmation and the sheet close, and the refusal is this client's own
    // warning - never a failed Remove, which would leave the stale assertion
    // holding the retry open.
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Remove instance" })).toBeNull());
    expect(screen.queryByRole("dialog", { name: "personal" })).toBeNull();
    await screen.findByText(/changed to a different endpoint/);
    expect(screen.queryByText(/Remove failed/)).toBeNull();
    // ...and the listing was re-read, so the retry can assert where the name
    // resolves now.
    expect(fake.calls.filter((call) => call.method === "evener/instance/list").length).toBeGreaterThan(listingsBefore);

    const reopened = await openSheet(user, "personal");
    await user.click(within(reopened).getByRole("button", { name: "Remove" }));
    const retryDialog = screen.getByRole("dialog", { name: "Remove instance" });
    await user.click(within(retryDialog).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(fake.calls.filter((call) => call.method === "evener/instance/remove")).toHaveLength(2));
    const removals = fake.calls.filter((call) => call.method === "evener/instance/remove");
    expect(removals[0]?.params).toEqual({
      name: "personal",
      expectedEndpointFingerprint: "fp-old",
      originClientId: "test-tab",
    });
    expect(removals[1]?.params).toEqual({
      name: "personal",
      expectedEndpointFingerprint: "fp-new",
      originClientId: "test-tab",
    });
  });

  test("a clear refused for a moved endpoint closes the confirmation, re-reads, and warns", async () => {
    const fake = connectFakeClient();
    const SHADOWED = instance({
      name: "shadowed",
      providerId: "openai-codex",
      auth: "oauth-openai-codex",
      authModes: ["oauth"],
      activeSource: "oauth",
      hasStoredOAuth: true,
      hasStoredFile: true,
      endpointFingerprint: "fp-old",
    });
    const SHADOWED_MOVED = { ...SHADOWED, endpointFingerprint: "fp-new" };
    let refused = false;
    fake.on("evener/instance/list", () => ({
      instances: [refused ? SHADOWED_MOVED : SHADOWED],
      availableProviders: [],
    }));
    fake.on("evener/auth/apiKey/clear", () => {
      refused = true;
      throw new WireError("shadowed no longer resolves to the endpoint this confirmation was opened on", -32013, {
        evenerErrorInfo: ErrorEndpointConflict,
      });
    });
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("shadowed");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "shadowed");
    await user.click(within(inspector).getByRole("button", { name: "Clear stored key" }));
    const dialog = screen.getByRole("dialog", { name: "Clear stored key" });
    const listingsBefore = fake.calls.filter((call) => call.method === "evener/instance/list").length;
    await user.click(within(dialog).getByRole("button", { name: "Clear" }));

    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Clear stored key" })).toBeNull());
    expect(screen.queryByRole("dialog", { name: "shadowed" })).toBeNull();
    await screen.findByText(/changed to a different endpoint/);
    expect(screen.queryByText(/Clear stored key failed/)).toBeNull();
    expect(fake.calls.filter((call) => call.method === "evener/instance/list").length).toBeGreaterThan(listingsBefore);

    const reopened = await openSheet(user, "shadowed");
    await user.click(within(reopened).getByRole("button", { name: "Clear stored key" }));
    const retryDialog = screen.getByRole("dialog", { name: "Clear stored key" });
    await user.click(within(retryDialog).getByRole("button", { name: "Clear" }));
    await waitFor(() =>
      expect(fake.calls.filter((call) => call.method === "evener/auth/apiKey/clear")).toHaveLength(2),
    );
    const clears = fake.calls.filter((call) => call.method === "evener/auth/apiKey/clear");
    expect(clears[1]?.params).toEqual({
      provider: "shadowed",
      expectedEndpointFingerprint: "fp-new",
      originClientId: "test-tab",
    });
  });

  // A name the environment also supplies keeps resolving after the authored
  // entry is removed: the hub re-lists it as an implicit instance, and the
  // access it resolves is the environment's, not the removed entry's. The
  // removal is confirmed by the authored row leaving - requiring the *name* to
  // vanish would report a removal that did happen as unconfirmed, leaving the
  // sheet open on stale authored state.
  test("removing an instance the environment also supplies confirms on the authored row leaving", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    fake.on("evener/instance/remove", (params) => {
      expect(params).toEqual({ name: "personal", originClientId: "test-tab" });
      return {
        instances: [
          WORK,
          instance({
            name: "personal",
            providerId: "openai-codex",
            implicit: true,
            activeSource: "env:OPENAI_API_KEY",
          }),
        ],
        availableProviders: [],
      };
    });
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("personal");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "personal");
    await user.click(within(inspector).getByRole("button", { name: "Remove" }));
    const confirm = screen.getByRole("dialog", { name: "Remove instance" });
    await user.click(within(confirm).getByRole("button", { name: "Remove" }));
    await screen.findByText(/Removed instance personal/);
    expect(screen.queryByText(/could not be confirmed/)).toBeNull();
  });

  // The authored entry's name can survive the removal as an environment-
  // supplied implicit row. A sheet left open on that name keeps the removed
  // instance's dirty draft and offers a Save that would author a new override
  // out of it, so a confirmed removal clears the selection: the replacement
  // row opens fresh.
  test("a confirmed removal closes the sheet even when the name survives as an environment-supplied row", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => ({ instances: [WORK], availableProviders: [] }));
    fake.on("evener/instance/remove", (params) => {
      expect(params).toEqual({ name: "work", originClientId: "test-tab" });
      return {
        instances: [
          instance({
            name: "work",
            providerId: "anthropic",
            implicit: true,
            activeSource: "env:ANTHROPIC_API_KEY",
          }),
        ],
        availableProviders: [],
      };
    });
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    // A dirty draft in the sheet's own editor lights its Save button - exactly
    // the control that must not survive onto the replacement implicit row.
    await user.type(within(inspector).getByLabelText("Base URL"), "https://edited.example");
    expect(isRefused(within(inspector).getByRole("button", { name: "Save" }))).toBe(false);
    await user.click(within(inspector).getByRole("button", { name: "Remove" }));
    const confirm = screen.getByRole("dialog", { name: "Remove instance" });
    await user.click(within(confirm).getByRole("button", { name: "Remove" }));

    await screen.findByText(/Removed instance work; environment access for it is still active/);
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "work" })).toBeNull());
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
    expect(
      fake.calls.filter((call) => call.method === "evener/instance/edit" || call.method === "evener/instance/create"),
    ).toEqual([]);
  });

  test("cancelling a confirm dialog makes no RPC call and keeps the sheet open", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    const removeCalls: unknown[] = [];
    fake.on("evener/instance/remove", (params) => {
      removeCalls.push(params);
      return { instances: [], availableProviders: [] };
    });
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("personal");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "personal");
    await user.click(within(inspector).getByRole("button", { name: "Remove" }));
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog", { name: "Remove instance" })).toBeNull();
    expect(removeCalls).toEqual([]);
    expect(screen.getByRole("dialog", { name: "personal" })).toBeTruthy();
  });

  test("clear failure shows error toast", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    fake.on("evener/auth/logout", () => {
      throw new Error("logout denied");
    });
    render(
      <>
        <Toast />
        <CredentialsSection sectionId="credentials" />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    // WORK has a stored key → its sheet offers Clear.
    const inspector = await openSheet(user, "work");
    await user.click(within(inspector).getByRole("button", { name: "Clear" }));
    const dialog = screen.getByRole("dialog", { name: "Clear credentials" });
    await user.click(within(dialog).getByRole("button", { name: "Clear" }));
    await screen.findByText("Clear failed: Something went wrong.");
    expect(screen.getByRole("dialog", { name: "Clear credentials" })).toBeTruthy();
  });

  test("clear stored key failure shows error toast", async () => {
    const fake = connectFakeClient();
    const SHADOWED = instance({
      name: "shadowed",
      providerId: "openai-codex",
      auth: "oauth-openai-codex",
      authModes: ["oauth"],
      activeSource: "oauth",
      hasStoredOAuth: true,
      hasStoredFile: true,
    });
    fake.on("evener/instance/list", () => ({ instances: [SHADOWED], availableProviders: [] }));
    fake.on("evener/auth/apiKey/clear", () => {
      throw new Error("clear denied");
    });
    render(
      <>
        <Toast />
        <CredentialsSection sectionId="credentials" />
      </>,
    );
    await screen.findByText("shadowed");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "shadowed");
    await user.click(within(inspector).getByRole("button", { name: "Clear stored key" }));
    const dialog = screen.getByRole("dialog", { name: "Clear stored key" });
    await user.click(within(dialog).getByRole("button", { name: "Clear" }));
    await screen.findByText("Clear stored key failed: Something went wrong.");
    expect(screen.getByRole("dialog", { name: "Clear stored key" })).toBeTruthy();
  });

  test("remove failure shows error toast", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    fake.on("evener/instance/remove", () => {
      throw new Error("remove denied");
    });
    render(
      <>
        <Toast />
        <CredentialsSection sectionId="credentials" />
      </>,
    );
    await screen.findByText("personal");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "personal");
    await user.click(within(inspector).getByRole("button", { name: "Remove" }));
    const dialog = screen.getByRole("dialog", { name: "Remove instance" });
    await user.click(within(dialog).getByRole("button", { name: "Remove" }));
    await screen.findByText("Remove failed: Something went wrong.");
    expect(screen.getByRole("dialog", { name: "Remove instance" })).toBeTruthy();
  });
});

describe("credential dialogs against a moving endpoint", () => {
  // The section captures the row's endpoint fingerprint when a credential
  // editor opens and passes it to the dialog, which submits that captured
  // value. A concurrent change that puts a different endpoint under the same
  // name updates the dialog's live row but not the captured fingerprint, so
  // the already-entered secret cannot be re-targeted to it.
  test("a name that resolves to a different endpoint refuses the save, clears the value, and shows an error", async () => {
    const fake = connectFakeClient();
    const WORK_FP = { ...WORK, endpointFingerprint: "fp-original" };
    fake.on("evener/instance/list", () => ({ instances: [WORK_FP], availableProviders: [] }));
    const setKey = vi.fn();
    fake.on("evener/auth/apiKey/set", setKey);
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.click(within(inspector).getByRole("button", { name: "Replace key" }));
    const dialog = screen.getByRole("dialog", { name: "Set API key for work" });
    await user.type(within(dialog).getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    // A concurrent client puts a different endpoint under the same name.
    const MOVED = { ...WORK, endpointFingerprint: "fp-changed" };
    fake.on("evener/instance/list", () => ({ instances: [MOVED], availableProviders: [] }));
    await act(async () => {
      await credentialsStore.getState().fetch();
    });
    await user.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(within(dialog).getByRole("alert").textContent).toContain("different endpoint"));
    expect(setKey).not.toHaveBeenCalled();
    expect((within(dialog).getByLabelText(/api key/i, { selector: "input" }) as HTMLInputElement).value).toBe("");
  });

  test("an unchanged destination submits the captured fingerprint", async () => {
    const fake = connectFakeClient();
    const WORK_FP = { ...WORK, endpointFingerprint: "fp-original" };
    fake.on("evener/instance/list", () => ({ instances: [WORK_FP], availableProviders: [] }));
    fake.on("evener/auth/apiKey/set", (params) => {
      expect(params).toEqual({
        provider: "work",
        value: "sk-secret",
        expectedEndpointFingerprint: "fp-original",
        originClientId: "test-tab",
      });
      return { provider: "work", supported: true, signedIn: true, activeSource: "store", hasStoredOAuth: false };
    });
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.click(within(inspector).getByRole("button", { name: "Replace key" }));
    const dialog = screen.getByRole("dialog", { name: "Set API key for work" });
    await user.type(within(dialog).getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    await user.click(within(dialog).getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(fake.calls.some((c) => c.method === "evener/auth/apiKey/set")).toBe(true));
  });
});

// The registry reports what it could not load (diagnostics) and whether the
// user layer can be written at all (writesRefused) on every instance list -
// spec §11.3.
describe("diagnostics and writesRefused", () => {
  test("renders every diagnostics entry from the list response", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => ({
      instances: [],
      availableProviders: [],
      diagnostics: [
        'providers.toml: unknown key "type" (instance writes are refused until the file is fixed)',
        "user layer: none (EVENER_PROVIDERS_CONFIG is empty)",
      ],
    }));
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText(/providers\.toml: unknown key "type"/);
    expect(screen.getByText("user layer: none (EVENER_PROVIDERS_CONFIG is empty)")).toBeTruthy();
  });

  test("no diagnostics banner when the list carries none", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("work");
    expect(screen.queryByText("Warnings")).toBeNull();
  });

  test("writesRefused gates the raw add-instance action, not the guided connector or credential-only actions", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => ({
      instances: [WORK, PERSONAL],
      availableProviders: [],
      writesRefused: true,
    }));
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("work");
    const user = userEvent.setup();

    // Credentials go to the credential store, not providers.toml, and the
    // registry still serves the curated/implicit set when the user layer fails
    // to load - so the guided entry point must stay usable while writes are
    // refused.
    expect((screen.getByRole("button", { name: "Connect provider" }) as HTMLButtonElement).disabled).toBe(false);

    const workInspector = await openSheet(user, "work");
    expect((within(workInspector).getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(true);
    // WORK has a stored key, so its sheet offers Clear - unaffected by writesRefused.
    expect((within(workInspector).getByRole("button", { name: "Clear" }) as HTMLButtonElement).disabled).toBe(false);
    expect((within(workInspector).getByRole("button", { name: "Replace key" }) as HTMLButtonElement).disabled).toBe(
      false,
    );
    expect(isRefused(within(workInspector).getByRole("button", { name: "Test credentials" }))).toBe(false);
    await user.click(within(workInspector).getByRole("button", { name: "Close" }));

    // Only PERSONAL is non-default, so it is the only sheet offering "make default".
    const personalInspector = await openSheet(user, "personal");
    expect(
      (within(personalInspector).getByRole("button", { name: /make default/i }) as HTMLButtonElement).disabled,
    ).toBe(true);
  });

  test("writesRefused disables the raw add-instance action that authors providers.toml", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => ({ instances: [WORK], availableProviders: [], writesRefused: true }));
    render(<CredentialsSection sectionId="credentials" fullEditor />);
    await screen.findByText("work");

    expect((screen.getByRole("button", { name: "+ Add provider instance" }) as HTMLButtonElement).disabled).toBe(true);
  });

  test("writesRefused leaves the guided connector and the credential-only actions usable", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => ({
      instances: [WORK, PERSONAL],
      availableProviders: [],
      writesRefused: true,
    }));
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("work");
    const user = userEvent.setup();

    // Credentials go to the credential store, not providers.toml, and the
    // registry still serves the curated/implicit set when the user layer fails
    // to load - so the guided entry point must stay usable while writes are
    // refused.
    expect((screen.getByRole("button", { name: "Connect provider" }) as HTMLButtonElement).disabled).toBe(false);

    const workInspector = await openSheet(user, "work");
    expect((within(workInspector).getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(true);
    // WORK has a stored key, so its sheet offers Clear - unaffected by writesRefused.
    expect((within(workInspector).getByRole("button", { name: "Clear" }) as HTMLButtonElement).disabled).toBe(false);
    expect((within(workInspector).getByRole("button", { name: "Replace key" }) as HTMLButtonElement).disabled).toBe(
      false,
    );
    expect(isRefused(within(workInspector).getByRole("button", { name: "Test credentials" }))).toBe(false);
    await user.click(within(workInspector).getByRole("button", { name: "Close" }));

    // Only PERSONAL is non-default, so it is the only sheet offering "make default".
    const personalInspector = await openSheet(user, "personal");
    expect(
      (within(personalInspector).getByRole("button", { name: /make default/i }) as HTMLButtonElement).disabled,
    ).toBe(true);
  });
});

describe("rename from the sheet", () => {
  test("re-selects the instance under its new name so the sheet stays open", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    fake.on("evener/instance/edit", (params) => ({
      instances: [{ ...WORK, name: params.newName ?? WORK.name }, PERSONAL],
      availableProviders: [],
    }));
    render(
      <>
        <Toast />
        <CredentialsSection sectionId="credentials" />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.type(within(inspector).getByLabelText("Name"), "2");
    await user.click(within(inspector).getByRole("button", { name: "Save" }));
    await screen.findByRole("dialog", { name: "work2" });
    expect(screen.queryByRole("dialog", { name: "work" })).toBeNull();
    expect(screen.getByRole("button", { name: /work2/ })).toBeTruthy();
  });

  // A rename that stood still comes back as an error when the hub could not
  // carry the instance's OAuth record to the new name. The hub discriminates
  // exactly that case with its own evenerErrorInfo value, so the client steers
  // to the renamed instance and surfaces the hub's own message as a warning
  // rather than a failed save: providers.toml already names the new instance.
  test("a rename error carrying the hub's persisted discriminator is reconciled and warned, not failed", async () => {
    const fake = connectFakeClient();
    const HUB_MESSAGE =
      "renamed work to work2, but: OAuth record not read: open /state/auth/work.json: permission denied";
    let renamed = false;
    fake.on("evener/instance/list", () =>
      renamed ? { instances: [{ ...WORK, name: "work2" }, PERSONAL], availableProviders: [] } : LIST,
    );
    fake.on("evener/instance/edit", () => {
      renamed = true;
      throw new WireError(HUB_MESSAGE, -32603, { evenerErrorInfo: ErrorInstanceRenamePersisted });
    });
    render(
      <>
        <Toast />
        <CredentialsSection sectionId="credentials" />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.type(within(inspector).getByLabelText("Name"), "2");
    await user.click(within(inspector).getByRole("button", { name: "Save" }));

    await screen.findByText(/OAuth record not read/);
    expect(screen.queryByText(/Save failed/)).toBeNull();
    // The rename stood: the sheet follows the instance to its new name.
    await screen.findByRole("dialog", { name: "work2" });
    expect(screen.queryByRole("dialog", { name: "work" })).toBeNull();
  });

  // The hub persisted the rename but could not finish it, and its registry is
  // on a fallback listing that does NOT carry the new row. The discriminator is
  // authoritative, so the sheet steers anyway - and must survive the gap: the
  // held entry is all that keeps `instance` defined until the listing catches
  // up. Clearing it as part of steering (or letting the `[name]` effect drop it
  // unconditionally) closes the sheet on itself the moment it moves, leaving
  // the user with only a toast and no editor.
  test("a persisted rename whose listing omits the new row keeps the sheet open until the listing catches up", async () => {
    const fake = connectFakeClient();
    const HUB_MESSAGE =
      "renamed work to work2, but: OAuth record not read: open /state/auth/work.json: permission denied";
    let caughtUp = false;
    fake.on("evener/instance/list", () =>
      caughtUp ? { instances: [{ ...WORK, name: "work2" }, PERSONAL], availableProviders: [] } : LIST,
    );
    fake.on("evener/instance/edit", () => {
      throw new WireError(HUB_MESSAGE, -32603, { evenerErrorInfo: ErrorInstanceRenamePersisted });
    });
    render(
      <>
        <Toast />
        <CredentialsSection sectionId="credentials" />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.type(within(inspector).getByLabelText("Name"), "2");
    await user.click(within(inspector).getByRole("button", { name: "Save" }));

    await screen.findByText(/OAuth record not read/);
    expect(screen.queryByText(/Save failed/)).toBeNull();
    // The listing has not caught up, so the held entry is keeping the sheet
    // alive: it is still open, not closed on the missing row.
    expect(screen.getAllByRole("dialog")).toHaveLength(1);

    // The registry catches up: the sheet follows to the new name.
    caughtUp = true;
    await act(async () => {
      await credentialsStore.getState().fetch();
    });
    await screen.findByRole("dialog", { name: "work2" });
    expect(screen.queryByRole("dialog", { name: "work" })).toBeNull();
  });

  // The rename frees the old name, and the destination name is not reserved:
  // another instance can wear it - an implicit curated row the environment
  // re-derived there, or a different authored instance that took the held
  // original's place. The held entry must stay the sheet's subject until the
  // listing's row at the destination is this rename's own, or edits typed for
  // the renamed instance land on a stranger's configuration.
  test("a persisted rename does not adopt an impostor row at the destination name", async () => {
    const fake = connectFakeClient();
    const HUB_MESSAGE =
      "renamed work to work2, but: OAuth record not read: open /state/auth/work.json: permission denied";
    // The impostor: an implicit row re-derived under the freed name, with a
    // different endpoint. It is not the row this rename authored.
    const IMPOSTOR = instance({
      name: "work2",
      providerId: "openai",
      baseUrl: "https://impostor.example.test",
      implicit: true,
      activeSource: "env:WORK2_KEY",
    });
    let listing: InstanceListResponse = LIST;
    fake.on("evener/instance/list", () => listing);
    fake.on("evener/instance/edit", () => {
      throw new WireError(HUB_MESSAGE, -32603, { evenerErrorInfo: ErrorInstanceRenamePersisted });
    });
    render(
      <>
        <Toast />
        <CredentialsSection sectionId="credentials" />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.type(within(inspector).getByLabelText("Name"), "2");
    await user.click(within(inspector).getByRole("button", { name: "Save" }));
    await screen.findByText(/OAuth record not read/);

    // An impostor appears at the destination. The sheet stays the held
    // original: it neither titles itself with the impostor nor adopts its
    // values.
    listing = { instances: [IMPOSTOR, PERSONAL], availableProviders: [] };
    await act(async () => {
      await credentialsStore.getState().fetch();
    });
    const held = screen.getByRole("dialog", { name: "work" });
    expect(screen.queryByRole("dialog", { name: "work2" })).toBeNull();
    expect((within(held).getByLabelText("Base URL") as HTMLInputElement).value).toBe("");
    expect(screen.queryByDisplayValue("https://impostor.example.test")).toBeNull();

    // The real renamed row appears: the sheet follows to the new name.
    listing = { instances: [{ ...WORK, name: "work2" }, PERSONAL], availableProviders: [] };
    await act(async () => {
      await credentialsStore.getState().fetch();
    });
    await screen.findByRole("dialog", { name: "work2" });
    expect(screen.queryByRole("dialog", { name: "work" })).toBeNull();
  });

  // The same hazard with an authored row: a different instance that took the
  // freed name (renamed onto it, or recreated under it). The identity checks
  // reject it on its own fields, not only on implicitness.
  test("a persisted rename does not adopt a different authored row at the destination name", async () => {
    const fake = connectFakeClient();
    const HUB_MESSAGE =
      "renamed work to work2, but: OAuth record not read: open /state/auth/work.json: permission denied";
    const IMPOSTOR = instance({
      name: "work2",
      providerId: "openai",
      baseUrl: "https://impostor.example.test",
      activeSource: "store",
    });
    let listing: InstanceListResponse = LIST;
    fake.on("evener/instance/list", () => listing);
    fake.on("evener/instance/edit", () => {
      throw new WireError(HUB_MESSAGE, -32603, { evenerErrorInfo: ErrorInstanceRenamePersisted });
    });
    render(
      <>
        <Toast />
        <CredentialsSection sectionId="credentials" />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.type(within(inspector).getByLabelText("Name"), "2");
    await user.click(within(inspector).getByRole("button", { name: "Save" }));
    await screen.findByText(/OAuth record not read/);

    listing = { instances: [IMPOSTOR, PERSONAL], availableProviders: [] };
    await act(async () => {
      await credentialsStore.getState().fetch();
    });
    const held = screen.getByRole("dialog", { name: "work" });
    expect(screen.queryByRole("dialog", { name: "work2" })).toBeNull();
    expect((within(held).getByLabelText("Base URL") as HTMLInputElement).value).toBe("");
    expect(screen.queryByDisplayValue("https://impostor.example.test")).toBeNull();

    listing = { instances: [{ ...WORK, name: "work2" }, PERSONAL], availableProviders: [] };
    await act(async () => {
      await credentialsStore.getState().fetch();
    });
    await screen.findByRole("dialog", { name: "work2" });
    expect(screen.queryByRole("dialog", { name: "work" })).toBeNull();
  });

  // A persisted rename whose listing omits the row leaves the user's dirty
  // rename draft in place. Save stays pressable (the file's pressable-refusal
  // precedent), but resubmitting now would send the completed rename against
  // the old name - gone from the config - so it must refuse before any write.
  test("a persisted rename in the listing gap refuses a second save without a request", async () => {
    const fake = connectFakeClient();
    const HUB_MESSAGE =
      "renamed work to work2, but: OAuth record not read: open /state/auth/work.json: permission denied";
    fake.on("evener/instance/list", () => LIST);
    fake.on("evener/instance/edit", () => {
      throw new WireError(HUB_MESSAGE, -32603, { evenerErrorInfo: ErrorInstanceRenamePersisted });
    });
    render(
      <>
        <Toast />
        <CredentialsSection sectionId="credentials" />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.type(within(inspector).getByLabelText("Name"), "2");
    await user.click(within(inspector).getByRole("button", { name: "Save" }));
    await screen.findByText(/OAuth record not read/);
    const edits = () => fake.calls.filter((call) => call.method === "evener/instance/edit");
    expect(edits()).toHaveLength(1);

    // The draft is still dirty and Save pressable; pressing must refuse.
    await user.click(within(inspector).getByRole("button", { name: "Save" }));
    expect(edits()).toHaveLength(1);
    expect(within(inspector).getByRole("alert").textContent).toMatch(/still being confirmed/);

    // The form's own submit door refuses too.
    fireEvent.submit(within(inspector).getByRole("form", { name: "Edit work" }));
    expect(edits()).toHaveLength(1);
  });

  // During the gap the section's selection names the destination, and every
  // action the sheet forwards targets that name - whatever occupies it, here an
  // impostor row. Remove and Clear must refuse rather than mutate the wrong
  // instance, and must work again the instant the real row arrives.
  test("a persisted rename in the listing gap refuses Remove and Clear, and they work once it reconciles", async () => {
    const fake = connectFakeClient();
    const HUB_MESSAGE =
      "renamed work to work2, but: OAuth record not read: open /state/auth/work.json: permission denied";
    const IMPOSTOR = instance({
      name: "work2",
      providerId: "openai",
      baseUrl: "https://impostor.example.test",
      implicit: true,
      activeSource: "env:WORK2_KEY",
    });
    let listing: InstanceListResponse = LIST;
    fake.on("evener/instance/list", () => listing);
    fake.on("evener/instance/edit", () => {
      listing = { instances: [IMPOSTOR, PERSONAL], availableProviders: [] };
      throw new WireError(HUB_MESSAGE, -32603, { evenerErrorInfo: ErrorInstanceRenamePersisted });
    });
    render(
      <>
        <Toast />
        <CredentialsSection sectionId="credentials" />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.type(within(inspector).getByLabelText("Name"), "2");
    await user.click(within(inspector).getByRole("button", { name: "Save" }));
    await screen.findByText(/OAuth record not read/);
    // The gap: the sheet shows the held original while the destination holds
    // the impostor.
    expect(screen.getByRole("dialog", { name: "work" })).toBeTruthy();

    await user.click(within(inspector).getByRole("button", { name: "Remove" }));
    expect(screen.queryByRole("dialog", { name: "Remove instance" })).toBeNull();
    expect(fake.calls.filter((call) => call.method === "evener/instance/remove")).toHaveLength(0);
    await user.click(within(inspector).getByRole("button", { name: "Clear" }));
    expect(screen.queryByRole("dialog", { name: "Clear credentials" })).toBeNull();
    expect(fake.calls.filter((call) => call.method === "evener/auth/logout")).toHaveLength(0);
    // The user is told why, and the impostor is untouched.
    expect((await screen.findAllByText(/still being confirmed/)).length).toBeGreaterThan(0);
    expect(credentialsStore.getState().instances.some((i) => i.name === "work2")).toBe(true);

    // The real row arrives: the actions work again.
    listing = { instances: [{ ...WORK, name: "work2" }, PERSONAL], availableProviders: [] };
    await act(async () => {
      await credentialsStore.getState().fetch();
    });
    await screen.findByRole("dialog", { name: "work2" });
    await user.click(within(inspector).getByRole("button", { name: "Remove" }));
    expect(await screen.findByRole("dialog", { name: "Remove instance" })).toBeTruthy();
  });

  // A refusal carries no such discriminator, so it stays the plain save failure
  // it was, with the sheet left where it was.
  test("a rename error without the persisted discriminator stays a plain failure", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    fake.on("evener/instance/edit", () => {
      throw new WireError("renaming work was refused: the new name is taken", -32013);
    });
    render(
      <>
        <Toast />
        <CredentialsSection sectionId="credentials" />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.type(within(inspector).getByLabelText("Name"), "2");
    await user.click(within(inspector).getByRole("button", { name: "Save" }));

    await screen.findByText(/Save failed/);
    expect(screen.queryByRole("dialog", { name: "work2" })).toBeNull();
    expect(screen.getByRole("dialog", { name: "work" })).toBeTruthy();
  });
});
