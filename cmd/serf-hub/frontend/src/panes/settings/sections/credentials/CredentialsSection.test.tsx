import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { AuthTestResponse, InstanceEntry, InstanceListResponse } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { resetCredentialsStoreForTests } from "../../../../stores/credentials";
import { Toast } from "../../../../widgets";
import { CredentialsSection } from "./CredentialsSection";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

function instance(overrides: Partial<InstanceEntry> & Pick<InstanceEntry, "name" | "type">): InstanceEntry {
  return {
    apiStyle: "",
    baseUrl: "",
    isDefault: false,
    activeSource: "absent",
    hasStoredOAuth: false,
    ...overrides,
  };
}

const WORK = instance({
  name: "work",
  type: "anthropic",
  authModes: ["apiKey"],
  isDefault: true,
  hasStoredFile: true,
  activeSource: "file",
});
const PERSONAL = instance({ name: "personal", type: "openai", authModes: ["apiKey", "oauth"] });
const LIST: InstanceListResponse = { instances: [WORK, PERSONAL], availableTypes: ["anthropic", "openai"] };

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

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetCredentialsStoreForTests();
});

afterEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  cleanup();
  vi.useRealTimers();
});

describe("initial load", () => {
  test("fetches serf/instance/list on mount and groups rows by type", async () => {
    const fake = connectFakeClient();
    fake.on("serf/instance/list", () => LIST);
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("work");
    expect(screen.getByText("anthropic")).toBeTruthy();
    expect(screen.getByText("openai")).toBeTruthy();
    expect(screen.getByText("personal")).toBeTruthy();
  });

  test("empty state", async () => {
    const fake = connectFakeClient();
    fake.on("serf/instance/list", () => ({ instances: [], availableTypes: [] }));
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("No provider instances configured.");
  });

  test("load failure shows an error message", async () => {
    const fake = connectFakeClient();
    fake.on("serf/instance/list", () => {
      throw new Error("network down");
    });
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText(/Failed to load: network down/);
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
    fake.on("serf/instance/list", () => {
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

describe("credential verification", () => {
  test("sends the exact custom instance name and shows local pending state until the deferred response arrives", async () => {
    const fake = connectFakeClient();
    const customName = "OpenAI / team-east:prod";
    const custom = instance({ name: customName, type: "openai", authModes: ["apiKey"] });
    const response = deferred<AuthTestResponse>();
    fake.on("serf/instance/list", () => ({ instances: [custom], availableTypes: ["openai"] }));
    fake.on("serf/auth/test", (params) => {
      expect(params).toEqual({ provider: customName });
      return response.promise;
    });
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText(customName);

    const row = screen.getByText(customName).closest("li");
    expect(row).not.toBeNull();
    const testButton = within(row!).getByRole("button", { name: "Test credentials" });
    await userEvent.setup().click(testButton);

    expect((within(row!).getByRole("button", { name: "Testing credentials…" }) as HTMLButtonElement).disabled).toBe(true);
    expect((within(row!).getByRole("button", { name: "Edit" }) as HTMLButtonElement).disabled).toBe(false);
    expect(fake.calls.filter((call) => call.method === "serf/auth/test")).toHaveLength(1);

    response.resolve({ provider: customName, status: "success", message: "Credentials verified." });
    expect((await screen.findByRole("status")).textContent).toContain("Credentials verified.");
    expect((within(row!).getByRole("button", { name: "Test credentials" }) as HTMLButtonElement).disabled).toBe(false);
  });

  test("suppresses duplicate clicks for one pending instance while another instance stays enabled", async () => {
    const fake = connectFakeClient();
    const workResponse = deferred<AuthTestResponse>();
    const personalResponse = deferred<AuthTestResponse>();
    fake.on("serf/instance/list", () => LIST);
    fake.on("serf/auth/test", (params) => {
      if (params.provider === WORK.name) return workResponse.promise;
      if (params.provider === PERSONAL.name) return personalResponse.promise;
      throw new Error(`unexpected provider ${params.provider}`);
    });
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText(WORK.name);
    const user = userEvent.setup();
    const workRow = screen.getByText(WORK.name).closest("li");
    const personalRow = screen.getByText(PERSONAL.name).closest("li");
    expect(workRow).not.toBeNull();
    expect(personalRow).not.toBeNull();

    const workButton = within(workRow!).getByRole("button", { name: "Test credentials" });
    await user.click(workButton);
    await user.click(workButton);
    expect(fake.calls.filter((call) => call.method === "serf/auth/test")).toHaveLength(1);
    expect((within(workRow!).getByRole("button", { name: "Testing credentials…" }) as HTMLButtonElement).disabled).toBe(true);
    expect((within(personalRow!).getByRole("button", { name: "Test credentials" }) as HTMLButtonElement).disabled).toBe(false);

    await user.click(within(personalRow!).getByRole("button", { name: "Test credentials" }));
    expect(fake.calls.filter((call) => call.method === "serf/auth/test")).toHaveLength(2);

    workResponse.resolve({ provider: WORK.name, status: "success", message: "Credentials verified." });
    personalResponse.resolve({ provider: PERSONAL.name, status: "success", message: "Credentials verified." });
    await waitFor(() => {
      expect((within(workRow!).getByRole("button", { name: "Test credentials" }) as HTMLButtonElement).disabled).toBe(false);
      expect((within(personalRow!).getByRole("button", { name: "Test credentials" }) as HTMLButtonElement).disabled).toBe(false);
    });
  });

  test.each([
    ["success", "Credentials verified."],
    ["missing", "No credentials are configured for this instance. Add a key or sign in first."],
    ["auth_rejected", "The provider rejected these credentials. Replace the key or sign in again."],
    ["endpoint_failure", "The provider endpoint could not be reached. Check the endpoint and network connection."],
    ["unsupported", "This provider does not support harmless credential verification."],
  ] as const)("renders the safe %s status and message", async (status, message) => {
    const fake = connectFakeClient();
    const response = deferred<AuthTestResponse>();
    fake.on("serf/instance/list", () => ({ instances: [WORK], availableTypes: [WORK.type] }));
    fake.on("serf/auth/test", () => response.promise);
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText(WORK.name);
    await userEvent.setup().click(screen.getByRole("button", { name: "Test credentials" }));
    response.resolve({ provider: WORK.name, status, message });

    const statusNode = await screen.findByRole("status");
    expect(statusNode.textContent).toBe(`${status}: ${message}`);
  });

  test("does not render a supplied secret from a response message", async () => {
    const fake = connectFakeClient();
    const secret = "sk-live-do-not-render";
    fake.on("serf/instance/list", () => ({ instances: [WORK], availableTypes: [WORK.type] }));
    fake.on("serf/auth/test", async () => ({ provider: WORK.name, status: "auth_rejected", message: secret }));
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText(WORK.name);
    await userEvent.setup().click(screen.getByRole("button", { name: "Test credentials" }));

    const status = await screen.findByRole("status");
    expect(status.textContent).toContain("The provider rejected these credentials. Replace the key or sign in again.");
    expect(document.body.textContent).not.toContain(secret);
  });

  test("does not render a raw RPC error string", async () => {
    const fake = connectFakeClient();
    const secret = "raw provider response containing sk-live-do-not-render";
    fake.on("serf/instance/list", () => ({ instances: [WORK], availableTypes: [WORK.type] }));
    fake.on("serf/auth/test", async () => {
      throw new Error(secret);
    });
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText(WORK.name);
    await userEvent.setup().click(screen.getByRole("button", { name: "Test credentials" }));

    const status = await screen.findByRole("status");
    expect(status.textContent).toContain("The provider endpoint could not be reached. Check the endpoint and network connection.");
    expect(document.body.textContent).not.toContain(secret);
  });
});

describe("single-open-editor invariant", () => {
  test("opening the Add form, then Edit on a row, replaces it (only one editor open at a time)", async () => {
    const fake = connectFakeClient();
    fake.on("serf/instance/list", () => LIST);
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("work");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "+ Add provider instance" }));
    expect(screen.getByRole("dialog", { name: "Add provider instance" })).toBeTruthy();
    await user.click(screen.getAllByRole("button", { name: "Edit" })[0]!);
    expect(screen.queryByRole("dialog", { name: "Add provider instance" })).toBeNull();
    expect(screen.getByRole("dialog", { name: "Edit work" })).toBeTruthy();
  });
});

describe("OAuth start branches", () => {
  test("fallback:true opens the redirect (paste-back) editor using loginStart's own flowId/url", async () => {
    const fake = connectFakeClient();
    fake.on("serf/instance/list", () => LIST);
    fake.on("serf/auth/device/start", () => ({
      provider: "personal",
      flowId: "device-flow",
      userCode: "X",
      verificationUrl: "https://verify",
      intervalSeconds: 5,
      fallback: true,
    }));
    fake.on("serf/auth/login/start", () => ({
      provider: "personal",
      flowId: "redirect-flow",
      url: "https://auth/start",
    }));
    const openSpy = vi.spyOn(window, "open").mockReturnValue(null);
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("personal");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Sign in…" }));
    await screen.findByRole("dialog", { name: "Sign in to personal" });
    expect(openSpy).toHaveBeenCalledWith("https://auth/start", "_blank", "noopener");
    expect(screen.getByRole("link", { name: /re-open authorize url/i })).toBeTruthy();
  });

  test("fallback:false/absent opens the device-code editor", async () => {
    const fake = connectFakeClient();
    fake.on("serf/instance/list", () => LIST);
    fake.on("serf/auth/device/start", () => ({
      provider: "personal",
      flowId: "device-flow",
      userCode: "ABCD-EFGH",
      verificationUrl: "https://verify",
      intervalSeconds: 5,
    }));
    fake.on("serf/auth/device/poll", () => ({ state: "pending" }));
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("personal");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Sign in…" }));
    await screen.findByText("ABCD-EFGH");
  });

  test("a deviceStart failure toasts 'Sign-in failed' and opens no editor", async () => {
    const fake = connectFakeClient();
    fake.on("serf/instance/list", () => LIST);
    fake.on("serf/auth/device/start", () => {
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
    await user.click(screen.getByRole("button", { name: "Sign in…" }));
    await screen.findByText("Sign-in failed: provider unavailable");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  // Proves the key={flowId} teardown DeviceCodeDialog's own doc comment
  // claims: expiring flow A, then "Start again" (a fresh serf/auth/device/
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
    fake.on("serf/instance/list", () => LIST);
    let deviceStartCalls = 0;
    fake.on("serf/auth/device/start", () => {
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
    fake.on("serf/auth/device/poll", (params) => {
      pollCalls.push(params.flowId);
      return params.flowId === "flow-A" ? { state: "expired" } : { state: "pending" };
    });
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("personal");
    vi.useFakeTimers();
    fireEvent.click(screen.getByRole("button", { name: "Sign in…" }));
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
    fake.on("serf/instance/list", () => LIST);
    fake.on("serf/instance/setDefault", (params) => {
      expect(params).toEqual({ name: "personal" });
      return { instances: [WORK, { ...PERSONAL, isDefault: true }], availableTypes: ["anthropic", "openai"] };
    });
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("personal");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: /make default/i }));
    await waitFor(() => expect(fake.calls.some((c) => c.method === "serf/instance/setDefault")).toBe(true));
    expect(screen.queryByRole("alert")).toBeNull();
  });

  test("a setDefault failure toasts 'Set default failed'", async () => {
    const fake = connectFakeClient();
    fake.on("serf/instance/list", () => LIST);
    fake.on("serf/instance/setDefault", () => {
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
    await user.click(screen.getByRole("button", { name: /make default/i }));
    await screen.findByText("Set default failed: boom");
  });
});

describe("Clear / Remove confirm dialogs", () => {
  test("Clear opens a ConfirmDialog naming the instance; confirming calls authLogout then refreshes", async () => {
    const fake = connectFakeClient();
    fake.on("serf/instance/list", () => LIST);
    fake.on("serf/auth/logout", (params) => {
      expect(params).toEqual({ provider: "work" });
      return {
        removed: true,
        status: { provider: "work", supported: true, signedIn: false, activeSource: "absent", hasStoredOAuth: false },
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
    // WORK carries hasStoredFile+activeSource:"file" in the shared fixture,
    // so its row already offers Clear.
    await user.click(screen.getByRole("button", { name: "Clear" }));
    const dialog = screen.getByRole("dialog", { name: /clear/i });
    expect(dialog).toBeTruthy();
    // The row's own Clear button is still present behind the dialog, so
    // scope this second click to the dialog's own Clear/confirm button.
    await user.click(within(dialog).getByRole("button", { name: "Clear" }));
    await screen.findByText("Credentials cleared for work");
  });

  test("Remove opens a ConfirmDialog; confirming calls instanceRemove", async () => {
    const fake = connectFakeClient();
    fake.on("serf/instance/list", () => LIST);
    fake.on("serf/instance/remove", (params) => {
      expect(params).toEqual({ name: "personal" });
      return { instances: [WORK], availableTypes: ["anthropic"] };
    });
    render(
      <>
        <CredentialsSection sectionId="credentials" />
        <Toast />
      </>,
    );
    await screen.findByText("personal");
    const user = userEvent.setup();
    const removeButtons = screen.getAllByRole("button", { name: "Remove" });
    await user.click(removeButtons[1]!); // personal's row
    const dialog = screen.getByRole("dialog", { name: /remove/i });
    expect(dialog).toBeTruthy();
    // The row's own Remove button is still present behind the dialog, so
    // scope this second click to the dialog's own Remove/confirm button.
    await user.click(within(dialog).getByRole("button", { name: "Remove" }));
    await screen.findByText("Removed instance personal");
  });

  test("cancelling a confirm dialog makes no RPC call", async () => {
    const fake = connectFakeClient();
    fake.on("serf/instance/list", () => LIST);
    const removeCalls: unknown[] = [];
    fake.on("serf/instance/remove", (params) => {
      removeCalls.push(params);
      return { instances: [], availableTypes: [] };
    });
    render(<CredentialsSection sectionId="credentials" />);
    await screen.findByText("personal");
    const user = userEvent.setup();
    const removeButtons = screen.getAllByRole("button", { name: "Remove" });
    await user.click(removeButtons[1]!);
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(removeCalls).toEqual([]);
  });
});
