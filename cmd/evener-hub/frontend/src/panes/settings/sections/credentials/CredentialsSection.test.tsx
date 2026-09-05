import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { AuthTestResponse, InstanceEntry, InstanceListResponse } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { credentialsStore, resetCredentialsStoreForTests } from "../../../../stores/credentials";
import { Toast } from "../../../../widgets";
import { resetToastStoreForTests } from "../../../../widgets/toast/store";
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
});

afterEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  cleanup();
  vi.useRealTimers();
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
    expect((within(inspector).getByRole("button", { name: "Edit" }) as HTMLButtonElement).disabled).toBe(false);
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
    const oldInstance = instance({ name: "work", providerId: "anthropic", baseUrl: "https://old.example/v1" });
    const refreshedInstance = instance({ name: "work", providerId: "anthropic", baseUrl: "https://new.example/v1" });
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
    await within(inspector).findByText("openai-chat · base https://old.example/v1");
    await userEvent.setup().click(within(inspector).getByRole("button", { name: "Test credentials" }));
    expect(within(inspector).getByRole("button", { name: "Testing credentials…" })).toBeTruthy();

    await act(async () => {
      await credentialsStore.getState().fetch();
    });
    // The sheet reads the instance from the store, so the refreshed base URL
    // lands live; the stale pending state from the old configuration is gone.
    await within(inspector).findByText("openai-chat · base https://new.example/v1");
    const refreshedButton = within(inspector).getByRole("button", { name: /Test(?:ing credentials…)?/ });
    expect((refreshedButton as HTMLButtonElement).disabled).toBe(false);
    response.resolve({ provider: "work", status: "success", message: "Credentials verified." });
    await act(async () => {
      await response.promise;
    });
    expect(screen.queryByRole("status")).toBeNull();
  });
});

describe("single-open-editor invariant", () => {
  test("opening the Add form, then Edit from a row's sheet, replaces it (only one editor open at a time)", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("work");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "+ Add provider instance" }));
    expect(screen.getByRole("dialog", { name: "Add provider instance" })).toBeTruthy();
    const inspector = await openSheet(user, "work");
    await user.click(within(inspector).getByRole("button", { name: "Edit" }));
    expect(screen.queryByRole("dialog", { name: "Add provider instance" })).toBeNull();
    expect(screen.queryByRole("dialog", { name: "work" })).toBeNull();
    expect(screen.getByRole("dialog", { name: "Edit work" })).toBeTruthy();
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
    const openSpy = vi.spyOn(window, "open").mockReturnValue(null);
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("personal");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "personal");
    await user.click(within(inspector).getByRole("button", { name: "Sign in…" }));
    await screen.findByRole("dialog", { name: "Sign in to personal" });
    expect(screen.queryByRole("dialog", { name: "personal" })).toBeNull();
    expect(openSpy).toHaveBeenCalledWith("https://auth/start", "_blank", "noopener");
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
    fireEvent.click(within(inspector).getByRole("button", { name: "Sign in…" }));
    await vi.waitFor(() => expect(screen.getByText("AAAA-1111")).toBeTruthy());

    // Flow A expires.
    await advanceTime(1000);
    expect(screen.getByText(/Code expired/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Start again" }));

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

describe("Clear / Clear stored key / Remove confirm dialogs", () => {
  test("Clear opens a ConfirmDialog naming the instance; confirming calls authLogout then refreshes", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    fake.on("evener/auth/logout", (params) => {
      expect(params).toEqual({ provider: "work" });
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
      expect(params).toEqual({ provider: "shadowed" });
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
      expect(params).toEqual({ provider: "vertex" });
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

  test("writesRefused disables Add and each sheet's Edit/Remove/make default, but not Test credentials/Set key/Clear", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => ({
      instances: [WORK, PERSONAL],
      availableProviders: [],
      writesRefused: true,
    }));
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("work");
    const user = userEvent.setup();

    expect((screen.getByRole("button", { name: "+ Add provider instance" }) as HTMLButtonElement).disabled).toBe(true);

    const workInspector = await openSheet(user, "work");
    expect((within(workInspector).getByRole("button", { name: "Edit" }) as HTMLButtonElement).disabled).toBe(true);
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
