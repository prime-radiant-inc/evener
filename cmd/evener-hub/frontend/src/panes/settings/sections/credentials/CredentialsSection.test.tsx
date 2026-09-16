import type { AuthLogoutResponse, AuthTestResponse, InstanceEntry, InstanceListResponse } from "@evener/appwire-client";
import {
  CONNECTION_REPLACED_ERROR,
  ENDPOINT_CHANGED_TEST_MESSAGE,
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

// The pane swaps its rows for the skeleton while a read is in flight, so a
// refresh - not only a listing that loses a row - unmounts the control holding
// the keyboard. That transition has to hand focus back to the pane as well.
test("a refresh that swaps the rows for the skeleton keeps the keyboard in the pane", async () => {
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

  // The row really is gone for the duration of the read; the keyboard is not on
  // <body>.
  expect(screen.queryByRole("button", { name: /work/ })).toBeNull();
  expect(document.activeElement).not.toBe(document.body);
  expect(screen.getByRole("button", { name: "Connect provider" })).toBe(document.activeElement);

  await act(async () => {
    refresh.resolve(LIST);
    await inFlight;
  });
  expect(await screen.findByRole("button", { name: /work/ })).toBeTruthy();
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
      expect(params).toEqual({ name: "personal" });
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
    render(<CredentialsSection sectionId="credentials" />);
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
    render(
      <>
        <CredentialsSection sectionId="credentials" />
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
  });

  // The applied path is not automatically a confirmation either: the store's
  // own listing is the applied response, and it must actually have lost the row.
  test("a removal whose applied listing still holds the row is not reported as removed", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    render(
      <>
        <CredentialsSection sectionId="credentials" />
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

    expect(
      (within(inspector).getByRole("button", { name: "Testing credentials…" }) as HTMLButtonElement).disabled,
    ).toBe(true);
    expect((within(inspector).getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(false);
    expect(fake.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(1);

    response.resolve({ provider: customName, status: "success", message: "Credentials verified." });
    expect((await screen.findByRole("status")).textContent).toContain("Credentials verified.");
    expect((within(inspector).getByRole("button", { name: "Test credentials" }) as HTMLButtonElement).disabled).toBe(
      false,
    );
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
    expect(
      (within(workInspector).getByRole("button", { name: "Testing credentials…" }) as HTMLButtonElement).disabled,
    ).toBe(true);

    // The other instance's sheet is independent: its Test stays enabled.
    await user.click(within(workInspector).getByRole("button", { name: "Close" }));
    const personalInspector = await openSheet(user, PERSONAL.name);
    const personalButton = within(personalInspector).getByRole("button", { name: "Test credentials" });
    expect((personalButton as HTMLButtonElement).disabled).toBe(false);
    await user.click(personalButton);
    expect(fake.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(2);

    workResponse.resolve({ provider: WORK.name, status: "success", message: "Credentials verified." });
    personalResponse.resolve({ provider: PERSONAL.name, status: "success", message: "Credentials verified." });
    await waitFor(() =>
      expect(
        (within(personalInspector).getByRole("button", { name: "Test credentials" }) as HTMLButtonElement).disabled,
      ).toBe(false),
    );
    await user.click(within(personalInspector).getByRole("button", { name: "Close" }));
    const workAgain = await openSheet(user, WORK.name);
    expect((within(workAgain).getByRole("button", { name: "Test credentials" }) as HTMLButtonElement).disabled).toBe(
      false,
    );
  });

  test.each([
    ["success", "Credentials verified."],
    ["missing", "No credentials are configured for this instance. Add a key or sign in first."],
    ["auth_rejected", "The provider rejected these credentials. Replace the key or sign in again."],
    ["endpoint_failure", "The provider endpoint could not be reached. Check the endpoint and network connection."],
    ["configuration_failure", "Provider configuration could not be loaded. Check the instance settings."],
    ["unsupported", "This provider does not support harmless credential verification."],
  ] as const)("renders the safe %s status and message", async (status, message) => {
    const fake = connectFakeClient();
    const response = deferred<AuthTestResponse>();
    fake.on("evener/instance/list", () => ({ instances: [WORK], availableProviders: [] }));
    fake.on("evener/auth/test", () => response.promise);
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText(WORK.name);
    const inspector = await openSheet(userEvent.setup(), WORK.name);
    await userEvent.setup().click(within(inspector).getByRole("button", { name: "Test credentials" }));
    response.resolve({ provider: WORK.name, status, message });

    const statusNode = await screen.findByRole("status");
    expect(statusNode.textContent).toBe(`${status}: ${message}`);
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
    expect((refreshedButton as HTMLButtonElement).disabled).toBe(false);
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
    expect((within(inspector).getByRole("button", { name: "Test credentials" }) as HTMLButtonElement).disabled).toBe(
      false,
    );
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
    expect(
      (within(inspector).getByRole("button", { name: "Testing credentials…" }) as HTMLButtonElement).disabled,
    ).toBe(true);

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
      expect((within(inspector).getByRole("button", { name: "Test credentials" }) as HTMLButtonElement).disabled).toBe(
        false,
      ),
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
      throw new WireError("endpoint changed", -32013, { evenerErrorInfo: "conflict" });
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
      expect((within(inspector).getByRole("button", { name: "Test credentials" }) as HTMLButtonElement).disabled).toBe(
        false,
      ),
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
  /** Renders the section with a listing on screen, replaces the client (as a
   * reconnect does), and leaves the store in the window the guard refuses in:
   * the rows on screen were read by the connection that is gone and this one's
   * listing has not been applied. The marker is set the way the store sets it
   * on a replacement (stores/credentials.ts's connectionStore subscription);
   * holding the read open cannot express this state here, because a read in
   * flight swaps the section's rows for its skeleton. */
  async function renderWithReplacedConnection(): Promise<{ replacement: FakeClient }> {
    const first = connectFakeClient();
    first.on("evener/instance/list", () => LIST);
    const replacement = new FakeClient("ready");
    replacement.on("evener/instance/list", () => LIST);
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("work");
    await act(async () => connectionStore.getState().connect(replacement));
    await waitFor(() => expect(credentialsStore.getState().listingFromPreviousConnection).toBe(false));
    await act(async () => credentialsStore.setState({ listingFromPreviousConnection: true }));
    expect(screen.getByText("work")).toBeTruthy();
    return { replacement };
  }

  test("a credential test is refused with the change, clears its pending state, and re-reads the listing", async () => {
    const { replacement } = await renderWithReplacedConnection();
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
      expect((within(inspector).getByRole("button", { name: "Test credentials" }) as HTMLButtonElement).disabled).toBe(
        false,
      ),
    );
    // The refusal asked for this connection's own listing, and that read is
    // what reopens the action.
    await waitFor(() => expect(credentialsStore.getState().listingFromPreviousConnection).toBe(false));

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
    await vi.waitFor(() => expect(screen.getByText("AAAA-1111")).toBeTruthy());

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
    await vi.waitFor(() => expect(screen.getByText("BBBB-2222")).toBeTruthy());
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
      expect(params).toEqual({ name: "personal" });
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
      expect(params).toEqual({ name: "work" });
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
      expect(params).toEqual({ name: "work" });
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
    expect(
      (within(workInspector).getByRole("button", { name: "Refresh live models" }) as HTMLButtonElement).disabled,
    ).toBe(false);
    gate.resolve({ instances: [WORK, PERSONAL], availableProviders: [] });
    await waitFor(() =>
      expect(
        (within(workInspector).getByRole("button", { name: "Refresh live models" }) as HTMLButtonElement).disabled,
      ).toBe(false),
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
      expect(params).toEqual({ name: "work", model: "claude-opus-4-6", disabled: true });
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
    // While the write is out, the same switch is disabled (plain DOM
    // property — this tree has no jest-dom matchers): a second rapid
    // click cannot submit a duplicate write.
    const isDisabled = (el: HTMLElement): boolean => (el as HTMLButtonElement).disabled;
    await waitFor(() =>
      expect(isDisabled(within(inspector).getByRole("switch", { name: "claude-opus-4-6" }))).toBe(true),
    );
    release({ instances: [{ ...WORK, models: [{ id: "claude-opus-4-6", disabled: true }] }], availableProviders: [] });
    await screen.findByText("Disabled claude-opus-4-6");
    await waitFor(() =>
      expect(isDisabled(within(inspector).getByRole("switch", { name: "claude-opus-4-6" }))).toBe(false),
    );
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
      expect(params).toEqual({ name: "personal" });
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
      expect(params).toEqual({ name: "personal", expectedEndpointFingerprint: "fp-personal" });
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
      expect(params).toEqual({ name: "personal" });
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
      expect(params).toEqual({ name: "work" });
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
    expect((within(inspector).getByRole("button", { name: "Save" }) as HTMLButtonElement).disabled).toBe(false);
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
    expect(
      (within(workInspector).getByRole("button", { name: "Test credentials" }) as HTMLButtonElement).disabled,
    ).toBe(false);
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
});
