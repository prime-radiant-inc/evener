import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import { connectionStore } from "../../../../stores/connection";
import { resetCredentialsStoreForTests } from "../../../../stores/credentials";
import { Toast } from "../../../../widgets";
import { DeviceCodeDialog, OAuthRedirectDialog } from "./oauthDialogs";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
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

describe("OAuthRedirectDialog", () => {
  test("shows a re-open link to the authorize URL", () => {
    connectFakeClient();
    render(
      <OAuthRedirectDialog
        name="work"
        flowId="flow-1"
        authUrl="https://auth.example.com/start"
        onCancel={() => {}}
        onSuccess={() => {}}
      />,
    );
    const link = screen.getByRole("link", { name: /re-open authorize url/i }) as HTMLAnchorElement;
    expect(link.href).toBe("https://auth.example.com/start");
    expect(link.target).toBe("_blank");
  });

  test("empty (trimmed) submit silently cancels with no RPC call", async () => {
    const fake = connectFakeClient();
    const complete = vi.fn();
    fake.on("serf/auth/login/complete", complete);
    const onCancel = vi.fn();
    render(
      <OAuthRedirectDialog name="work" flowId="flow-1" authUrl="https://x" onCancel={onCancel} onSuccess={() => {}} />,
    );
    fireEvent.submit(screen.getByRole("button", { name: "Finish" }).closest("form")!);
    expect(complete).not.toHaveBeenCalled();
    expect(onCancel).toHaveBeenCalled();
  });

  test("submitting a redirect URL calls authLoginComplete and, on success, fetches + calls onSuccess", async () => {
    const fake = connectFakeClient();
    fake.on("serf/auth/login/complete", (params) => {
      expect(params).toEqual({ provider: "work", flowId: "flow-1", redirectUrl: "https://redirect?code=1" });
      return {
        status: { provider: "work", supported: true, signedIn: true, activeSource: "oauth", hasStoredOAuth: true },
      };
    });
    fake.on("serf/instance/list", () => ({ instances: [], availableTypes: [] }));
    const onSuccess = vi.fn();
    render(
      <>
        <OAuthRedirectDialog
          name="work"
          flowId="flow-1"
          authUrl="https://x"
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Redirect URL"), "https://redirect?code=1");
    await user.click(screen.getByRole("button", { name: "Finish" }));
    // Asserting on onSuccess rather than toast DOM text: the toast queue is
    // a module-singleton that outlives cleanup() between tests (see
    // shell/rail/Rail.test.tsx's identical convention), so a same-text toast
    // from an earlier test could otherwise satisfy findByText before this
    // test's own flow has actually completed.
    await vi.waitFor(() => expect(onSuccess).toHaveBeenCalled());
    expect(screen.getAllByText("Signed in to work").length).toBeGreaterThan(0);
  });

  test("failure shows an inline error and a 'Sign-in failed' toast, without closing", async () => {
    const fake = connectFakeClient();
    fake.on("serf/auth/login/complete", () => {
      throw new Error("expired flow");
    });
    const onSuccess = vi.fn();
    render(
      <>
        <OAuthRedirectDialog
          name="work"
          flowId="flow-1"
          authUrl="https://x"
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Redirect URL"), "https://redirect");
    await user.click(screen.getByRole("button", { name: "Finish" }));
    await screen.findByText("expired flow");
    expect(screen.getByText("Sign-in failed: expired flow")).toBeTruthy();
    expect(onSuccess).not.toHaveBeenCalled();
  });
});

describe("DeviceCodeDialog", () => {
  test("shows the user code without auto-opening the verification URL", () => {
    connectFakeClient();
    const openSpy = vi.spyOn(window, "open").mockReturnValue(null);
    render(
      <DeviceCodeDialog
        name="work"
        flowId="flow-2"
        userCode="ABCD-EFGH"
        verificationUrl="https://verify"
        intervalSeconds={5}
        onCancel={() => {}}
        onSuccess={() => {}}
        onRestart={() => {}}
      />,
    );
    expect(screen.getByText("ABCD-EFGH")).toBeTruthy();
    expect(openSpy).not.toHaveBeenCalled();
  });

  test("'Send me to OpenAI' stays disabled until the code is copied, then opens the verification URL", async () => {
    connectFakeClient();
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
      configurable: true,
    });
    const openSpy = vi.spyOn(window, "open").mockReturnValue(null);
    render(
      <DeviceCodeDialog
        name="work"
        flowId="flow-2"
        userCode="ABCD-EFGH"
        verificationUrl="https://verify"
        intervalSeconds={5}
        onCancel={() => {}}
        onSuccess={() => {}}
        onRestart={() => {}}
      />,
    );
    const sendButton = screen.getByRole("button", { name: /send me to openai/i }) as HTMLButtonElement;
    expect(sendButton.disabled).toBe(true);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: /copy code/i }));
    await screen.findByRole("button", { name: /copied/i });
    expect(sendButton.disabled).toBe(false);
    await user.click(sendButton);
    expect(openSpy).toHaveBeenCalledWith("https://verify", "_blank", "noopener");
  });

  test("polling: authorized stops polling, fetches, toasts, and calls onSuccess", async () => {
    const fake = connectFakeClient();
    fake.on("serf/auth/device/poll", (params) => {
      expect(params).toEqual({ provider: "work", flowId: "flow-2" });
      return { state: "authorized" };
    });
    fake.on("serf/instance/list", () => ({ instances: [], availableTypes: [] }));
    const onSuccess = vi.fn();
    render(
      <>
        <DeviceCodeDialog
          name="work"
          flowId="flow-2"
          userCode="ABCD-EFGH"
          verificationUrl="https://verify"
          intervalSeconds={1}
          onCancel={() => {}}
          onSuccess={onSuccess}
          onRestart={() => {}}
        />
        <Toast />
      </>,
    );
    // Asserting on onSuccess rather than toast DOM text - see the identical
    // comment on OAuthRedirectDialog's own success test for why.
    await vi.waitFor(() => expect(onSuccess).toHaveBeenCalled(), { timeout: 3000 });
    expect(screen.getAllByText("Signed in to work").length).toBeGreaterThan(0);
  });

  // intervalSeconds is clamped to a 1-real-second floor by the verified
  // legacy formula (Math.max(1, intervalSeconds || 5) * 1000 - see
  // startDevicePolling), so every polling assertion below needs headroom
  // past the default 1000ms waitFor/findBy timeout to avoid racing it.
  const POLL_TIMEOUT = { timeout: 3000 };

  test("polling: expired stops polling and switches to a 'Start again' action", async () => {
    const fake = connectFakeClient();
    fake.on("serf/auth/device/poll", () => ({ state: "expired" }));
    render(
      <DeviceCodeDialog
        name="work"
        flowId="flow-2"
        userCode="ABCD-EFGH"
        verificationUrl="https://verify"
        intervalSeconds={1}
        onCancel={() => {}}
        onSuccess={() => {}}
        onRestart={() => {}}
      />,
    );
    await screen.findByText(/Code expired/, {}, POLL_TIMEOUT);
    expect(screen.getByRole("button", { name: "Start again" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /copy code/i })).toBeNull();
  });

  test("polling: an unrecognized state just reschedules (still waiting)", async () => {
    const fake = connectFakeClient();
    let calls = 0;
    fake.on("serf/auth/device/poll", () => {
      calls += 1;
      return { state: "pending" };
    });
    render(
      <DeviceCodeDialog
        name="work"
        flowId="flow-2"
        userCode="ABCD-EFGH"
        verificationUrl="https://verify"
        intervalSeconds={1}
        onCancel={() => {}}
        onSuccess={() => {}}
        onRestart={() => {}}
      />,
    );
    await vi.waitFor(() => expect(calls).toBeGreaterThanOrEqual(1), POLL_TIMEOUT);
    expect(screen.getByText(/Waiting for you to authorize/)).toBeTruthy();
  });

  test("clicking 'Start again' after expiry calls onRestart", async () => {
    const fake = connectFakeClient();
    fake.on("serf/auth/device/poll", () => ({ state: "expired" }));
    const onRestart = vi.fn();
    render(
      <DeviceCodeDialog
        name="work"
        flowId="flow-2"
        userCode="ABCD-EFGH"
        verificationUrl="https://verify"
        intervalSeconds={1}
        onCancel={() => {}}
        onSuccess={() => {}}
        onRestart={onRestart}
      />,
    );
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Start again" }, POLL_TIMEOUT));
    expect(onRestart).toHaveBeenCalled();
  });

  test("unmounting the dialog stops polling (no further requests after unmount)", async () => {
    const fake = connectFakeClient();
    let calls = 0;
    fake.on("serf/auth/device/poll", () => {
      calls += 1;
      return { state: "pending" };
    });
    const { unmount } = render(
      <DeviceCodeDialog
        name="work"
        flowId="flow-2"
        userCode="ABCD-EFGH"
        verificationUrl="https://verify"
        intervalSeconds={1}
        onCancel={() => {}}
        onSuccess={() => {}}
        onRestart={() => {}}
      />,
    );
    await vi.waitFor(() => expect(calls).toBeGreaterThanOrEqual(1), POLL_TIMEOUT);
    unmount();
    const callsAtUnmount = calls;
    await act(() => new Promise((resolve) => setTimeout(resolve, 1200)));
    expect(calls).toBe(callsAtUnmount);
  });
});
