import type { AuthDevicePollResponse, AuthLoginCompleteResponse, InstanceListResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { captureNewTabs, NEW_TAB_POLICY, openedNewTab } from "../../../../shell/openInNewTab.testSupport";
import { connectionStore } from "../../../../stores/connection";
import { credentialsStore, resetCredentialsStoreForTests } from "../../../../stores/credentials";
import { setMutationClientIdentityForTests } from "../../../../stores/mutationClientIdentity";
import { Toast } from "../../../../widgets";
import { getToasts } from "../../../../widgets/toast/store";
import { DeviceCodeDialog, OAuthRedirectDialog } from "./oauthDialogs";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

async function advanceTime(milliseconds: number): Promise<void> {
  await act(() => vi.advanceTimersByTimeAsync(milliseconds));
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetCredentialsStoreForTests();
  // The auth mutations carry the page's identity on the wire now, so the
  // assertions that pin their exact params need one that cannot vary.
  setMutationClientIdentityForTests("test-tab");
});

afterEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  cleanup();
  vi.useRealTimers();
  // Leave no spy installed between tests: each test that watches a new tab
  // installs its own capture (openInNewTab.testSupport), and nothing should
  // still be watching after the test that installed it.
  vi.restoreAllMocks();
});

describe("OAuthRedirectDialog", () => {
  test("a dismissed redirect editor ignores a late completion", async () => {
    const fake = connectFakeClient();
    let complete!: (value: AuthLoginCompleteResponse) => void;
    fake.on(
      "evener/auth/login/complete",
      () =>
        new Promise<AuthLoginCompleteResponse>((resolve) => {
          complete = resolve;
        }),
    );
    const list = vi.fn(() => ({ instances: [], availableProviders: [] }));
    fake.on("evener/instance/list", list);
    const onSuccess = vi.fn();
    const { unmount } = render(
      <OAuthRedirectDialog name="work" flowId="flow" authUrl="https://x" onCancel={() => {}} onSuccess={onSuccess} />,
    );
    await userEvent.setup().type(screen.getByLabelText("Redirect URL"), "https://redirect?code=1");
    await act(async () => fireEvent.submit(screen.getByRole("button", { name: "Finish" }).closest("form")!));
    unmount();
    await act(async () =>
      complete({
        status: { provider: "work", supported: true, signedIn: true, activeSource: "oauth", hasStoredOAuth: true },
      }),
    );
    expect(onSuccess).not.toHaveBeenCalled();
    expect(list).not.toHaveBeenCalled();
  });

  // The store refuses a completion issued from the previous connection's
  // listing (stores/credentials.ts's requireWritableClient). The pasted URL is
  // not a secret, so it is kept for the retry; what must not happen is the
  // store's own words on screen, or a "Sign-in failed" toast over a sign-in
  // that was never sent.
  test("a submit refused while the held listing is stale reports the change, not a failed sign-in", async () => {
    const catalogue = [{ id: "work", protocol: "openai-chat", auth: "bearer", implicit: true }];
    const first = connectFakeClient();
    first.on("evener/instance/list", () => ({ instances: [], availableProviders: catalogue }));
    await act(async () => {
      await credentialsStore.getState().fetch();
    });
    const replacement = new FakeClient("ready");
    replacement.on("evener/instance/list", () => ({ instances: [], availableProviders: catalogue }));
    replacement.on("evener/auth/login/complete", () => ({
      status: { provider: "work", supported: true, signedIn: true, activeSource: "oauth", hasStoredOAuth: true },
    }));
    connectionStore.getState().connect(replacement);
    // The connection is replaced and this connection's own listing has not been
    // applied: the flow this editor is completing was started on the one that
    // is gone. (Wait out the read the connect itself starts, so setting the
    // marker is what the submit sees.)
    await waitFor(() => expect(credentialsStore.getState().listingFromPreviousConnection).toBe(false));
    await act(async () => credentialsStore.setState({ listingFromPreviousConnection: true }));

    const onSuccess = vi.fn();
    render(
      <>
        <OAuthRedirectDialog
          name="work"
          flowId="flow-1"
          authUrl="https://auth"
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );
    await userEvent.setup().type(screen.getByLabelText("Redirect URL"), "https://redirect?code=1");
    await act(async () => fireEvent.submit(screen.getByRole("button", { name: "Finish" }).closest("form")!));

    expect(replacement.calls.filter((call) => call.method === "evener/auth/login/complete")).toHaveLength(0);
    // The refusal is named for the change it is, never in the store's own words
    // and never as a failed sign-in.
    expect(screen.getByRole("alert").textContent).toContain("connection was replaced");
    expect(screen.getByRole("alert").textContent).not.toContain("credentials store");
    expect(screen.queryByText(/Sign-in failed/)).toBeNull();
    expect(onSuccess).not.toHaveBeenCalled();
    // The pasted URL survives for the retry.
    expect((screen.getByLabelText("Redirect URL") as HTMLInputElement).value).toBe("https://redirect?code=1");
  });

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
    fake.on("evener/auth/login/complete", complete);
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
    fake.on("evener/auth/login/complete", (params) => {
      expect(params).toEqual({
        provider: "work",
        flowId: "flow-1",
        redirectUrl: "https://redirect?code=1",
        originClientId: "test-tab",
      });
      return {
        status: { provider: "work", supported: true, signedIn: true, activeSource: "oauth", hasStoredOAuth: true },
      };
    });
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
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
    await waitFor(() => expect(onSuccess).toHaveBeenCalled());
    expect(screen.getAllByText("Signed in to work").length).toBeGreaterThan(0);
  });

  test("a completed sign-in whose listing read is lost is still reported as a sign-in", async () => {
    const fake = connectFakeClient();
    let complete!: (value: AuthLoginCompleteResponse) => void;
    fake.on(
      "evener/auth/login/complete",
      () =>
        new Promise<AuthLoginCompleteResponse>((resolve) => {
          complete = resolve;
        }),
    );
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
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
    await act(async () => fireEvent.submit(screen.getByRole("button", { name: "Finish" }).closest("form")!));
    // The connection drops while the completion is in flight. The follow-up
    // listing read then rejects (fetch's requireClient contract) - but the
    // sign-in itself already succeeded, so it must not be reported as failed.
    connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
    await act(async () =>
      complete({
        status: { provider: "work", supported: true, signedIn: true, activeSource: "oauth", hasStoredOAuth: true },
      }),
    );
    await waitFor(() => expect(onSuccess).toHaveBeenCalled());
    expect(within(screen.getByRole("dialog")).queryByText(/no client connected/)).toBeNull();
  });

  test("failure shows an inline error and a 'Sign-in failed' toast, without closing", async () => {
    const fake = connectFakeClient();
    fake.on("evener/auth/login/complete", () => {
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
    const anchors = captureNewTabs();
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
    // Showing the code sends the user nowhere: the button below is the only
    // thing that opens the verification URL.
    expect(anchors).toHaveLength(0);
  });

  test("'Send me to OpenAI' stays disabled until the code is copied, then opens the verification URL without an opener", async () => {
    connectFakeClient();
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
      configurable: true,
    });
    const anchors = captureNewTabs();
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
    expect(openedNewTab(anchors)).toEqual({ url: "https://verify", target: "_blank", rel: NEW_TAB_POLICY });
  });

  // A verification URL the opener refuses (a non-http(s) scheme, or one the URL
  // parser rejects) must still reach the user. openInNewTab throws loudly by
  // design, but an event-handler throw never reaches an error boundary, so
  // without the dialog's own catch the click would silently do nothing.
  test("a refused verification URL reports an error toast instead of throwing", async () => {
    connectFakeClient();
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
      configurable: true,
    });
    const anchors = captureNewTabs();
    render(
      <DeviceCodeDialog
        name="work"
        flowId="flow-2"
        userCode="ABCD-EFGH"
        verificationUrl="javascript:alert(1)"
        intervalSeconds={5}
        onCancel={() => {}}
        onSuccess={() => {}}
        onRestart={() => {}}
      />,
    );
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: /copy code/i }));
    const sendButton = await screen.findByRole("button", { name: /send me to openai/i });
    expect((sendButton as HTMLButtonElement).disabled).toBe(false);
    // React 19 routes an event-handler throw to reportGlobalError instead of
    // rethrowing it out of dispatch, so "does not throw" here describes this
    // handler's own contract: the refusal has to become a toast, not an
    // uncaught error, and nothing may be opened.
    expect(() => fireEvent.click(sendButton)).not.toThrow();
    expect(anchors).toHaveLength(0);
    expect(
      getToasts().some(
        (toast) =>
          toast.kind === "error" &&
          toast.text === "Couldn't open the verification page: refusing to open a URL with the javascript: scheme",
      ),
    ).toBe(true);
  });

  test("a dismissed device editor ignores success after its refresh completes", async () => {
    vi.useFakeTimers();
    const fake = connectFakeClient();
    fake.on("evener/auth/device/poll", () => ({ state: "authorized" }));
    let finishRead!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/list",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishRead = resolve;
        }),
    );
    const onSuccess = vi.fn();
    const { unmount } = render(
      <DeviceCodeDialog
        name="work"
        flowId="flow"
        userCode="CODE"
        verificationUrl="https://x"
        intervalSeconds={1}
        onCancel={() => {}}
        onSuccess={onSuccess}
        onRestart={() => {}}
      />,
    );
    await advanceTime(1000);
    unmount();
    await act(async () => finishRead({ instances: [], availableProviders: [] }));
    expect(onSuccess).not.toHaveBeenCalled();
  });

  test("polling: authorized stops polling, fetches, toasts, and calls onSuccess", async () => {
    vi.useFakeTimers();
    const fake = connectFakeClient();
    fake.on("evener/auth/device/poll", (params) => {
      expect(params).toEqual({ provider: "work", flowId: "flow-2", originClientId: "test-tab" });
      return { state: "authorized" };
    });
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
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
    await advanceTime(1000);
    // Asserting on onSuccess rather than toast DOM text - see the identical
    // comment on OAuthRedirectDialog's own success test for why.
    expect(onSuccess).toHaveBeenCalled();
    expect(screen.getAllByText("Signed in to work").length).toBeGreaterThan(0);
    // An authorized flow is finished: no later interval polls it again.
    const polls = fake.calls.filter((call) => call.method === "evener/auth/device/poll").length;
    await advanceTime(3000);
    expect(fake.calls.filter((call) => call.method === "evener/auth/device/poll")).toHaveLength(polls);
  });

  test("an authorized poll whose listing read is lost still completes the sign-in", async () => {
    vi.useFakeTimers();
    const fake = connectFakeClient();
    let poll!: (value: AuthDevicePollResponse) => void;
    fake.on(
      "evener/auth/device/poll",
      () =>
        new Promise<AuthDevicePollResponse>((resolve) => {
          poll = resolve;
        }),
    );
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
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
    await advanceTime(1000);
    // The connection drops while the poll is in flight: the authorization
    // itself succeeded, and the follow-up listing read must neither turn it
    // into a failure nor escape this timer-driven tick as an unhandled
    // rejection.
    connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
    await act(async () => poll({ state: "authorized" }));
    expect(onSuccess).toHaveBeenCalled();
    expect(screen.getAllByText("Signed in to work").length).toBeGreaterThan(0);
  });

  test("polling: expired stops polling and switches to a 'Start again' action", async () => {
    vi.useFakeTimers();
    const fake = connectFakeClient();
    fake.on("evener/auth/device/poll", () => ({ state: "expired" }));
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
    await advanceTime(1000);
    expect(screen.getByText(/Code expired/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Start again" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /copy code/i })).toBeNull();
  });

  test("polling: an unrecognized state just reschedules (still waiting)", async () => {
    vi.useFakeTimers();
    const fake = connectFakeClient();
    let calls = 0;
    fake.on("evener/auth/device/poll", () => {
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
    await advanceTime(1000);
    expect(calls).toBe(1);
    expect(screen.getByText(/Waiting for you to authorize/)).toBeTruthy();
  });

  // Pins the headline behavior verified directly against the legacy source
  // (templates/partials/credentials.html's own tick(), not the parity floor
  // doc's paraphrase - see oauthDialogs.tsx's own comment on this branch):
  // a poll REQUEST failure (as opposed to an "expired"/"authorized" response
  // body) attaches its message and does NOT reschedule - the poll loop
  // silently stops, identical to "expired".
  test("a devicePoll request failure stops polling without rescheduling - no further poll calls ever follow", async () => {
    vi.useFakeTimers();
    const fake = connectFakeClient();
    let calls = 0;
    fake.on("evener/auth/device/poll", () => {
      calls += 1;
      throw new Error("network error");
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
    await advanceTime(1000);
    expect(screen.getByText("network error")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Start again" })).toBeTruthy();
    const callsAtError = calls;
    await advanceTime(2200);
    expect(calls).toBe(callsAtError);
  });

  // The store refuses a poll issued from the previous connection's listing
  // (stores/credentials.ts's requireWritableClient): the flow belongs to the
  // hub, not to this client, and the refusal is the store holding the client
  // back until the replacement's listing lands. Treated as a poll failure it
  // would end the flow permanently - "Start again" over a flow that may still
  // be authorizable - for a connection that merely reconnected.
  test("a poll refused while the held listing is stale keeps polling and completes once that listing lands", async () => {
    vi.useFakeTimers();
    const catalogue = [{ id: "work", protocol: "openai-chat", auth: "bearer", implicit: true }];
    const first = connectFakeClient();
    first.on("evener/instance/list", () => ({ instances: [], availableProviders: catalogue }));
    await act(async () => {
      await credentialsStore.getState().fetch();
    });

    // The replacement's own listing is held open, so every poll is refused
    // while it is in flight.
    let finishRestore!: (value: InstanceListResponse) => void;
    const restore = new Promise<InstanceListResponse>((resolve) => {
      finishRestore = resolve;
    });
    const replacement = new FakeClient("ready");
    replacement.on("evener/instance/list", () => restore);
    replacement.on("evener/auth/device/poll", () => ({ state: "authorized" }));
    connectionStore.getState().connect(replacement);
    expect(credentialsStore.getState().listingFromPreviousConnection).toBe(true);

    const onSuccess = vi.fn();
    render(
      <DeviceCodeDialog
        name="work"
        flowId="flow-2"
        userCode="ABCD-EFGH"
        verificationUrl="https://verify"
        intervalSeconds={1}
        onCancel={() => {}}
        onSuccess={onSuccess}
        onRestart={() => {}}
      />,
    );

    // The refused tick is not a poll outcome: nothing reached the connection,
    // the flow is still waiting rather than offering "Start again", and the
    // status line says what it is waiting for.
    await advanceTime(1000);
    expect(replacement.calls.filter((call) => call.method === "evener/auth/device/poll")).toHaveLength(0);
    expect(screen.queryByRole("button", { name: "Start again" })).toBeNull();
    expect(screen.getByText("Waiting for the connection to be restored…")).toBeTruthy();

    // This connection's own listing lands; the next tick polls and the flow
    // completes against the connection that is actually there.
    await act(async () => finishRestore({ instances: [], availableProviders: catalogue }));
    await advanceTime(1000);
    await advanceTime(0);
    expect(replacement.calls.filter((call) => call.method === "evener/auth/device/poll")).toHaveLength(1);
    expect(onSuccess).toHaveBeenCalled();
  });

  // The refusal clears only when a listing this connection read is applied, and
  // the usual one is the reconnect's own restore read. If THAT read failed (the
  // connection dropped again while it was in flight), every later tick would be
  // refused in silence: "Waiting for you to authorize…" for as long as the
  // dialog stays open, with no error and no retry the user can reach. The poll
  // asks for the listing once itself, so the flow recovers on its own.
  test("a poll refused while the restore read failed asks for the listing and resumes once it lands", async () => {
    vi.useFakeTimers();
    const catalogue = [{ id: "work", protocol: "openai-chat", auth: "bearer", implicit: true }];
    const first = connectFakeClient();
    first.on("evener/instance/list", () => ({ instances: [], availableProviders: catalogue }));
    await act(async () => {
      await credentialsStore.getState().fetch();
    });

    // The replacement's own restore read fails, so the marker stays set.
    let reads = 0;
    const replacement = new FakeClient("ready");
    replacement.on("evener/instance/list", () => {
      reads += 1;
      if (reads === 1) throw new Error("transport down");
      return { instances: [], availableProviders: catalogue };
    });
    replacement.on("evener/auth/device/poll", () => ({ state: "authorized" }));
    connectionStore.getState().connect(replacement);
    expect(credentialsStore.getState().listingFromPreviousConnection).toBe(true);

    const onSuccess = vi.fn();
    render(
      <DeviceCodeDialog
        name="work"
        flowId="flow-3"
        userCode="ABCD-EFGH"
        verificationUrl="https://verify"
        intervalSeconds={1}
        onCancel={() => {}}
        onSuccess={onSuccess}
        onRestart={() => {}}
      />,
    );

    // The refused tick asks for this connection's listing itself...
    await advanceTime(1000);
    expect(replacement.calls.filter((call) => call.method === "evener/instance/list").length).toBeGreaterThan(1);
    expect(credentialsStore.getState().listingFromPreviousConnection).toBe(false);

    // ...and the next tick polls the connection that is actually there.
    await advanceTime(1000);
    await advanceTime(0);
    expect(replacement.calls.filter((call) => call.method === "evener/auth/device/poll")).toHaveLength(1);
    expect(onSuccess).toHaveBeenCalled();
  });

  // One ask is not enough: the read it starts can fail too (the connection
  // dropped again while it was in flight). A latch that never re-asks would
  // leave every later tick refused with nothing on screen to say so - "Waiting
  // for you to authorize..." forever. The poll keeps asking, and the status
  // line names the wait while it does.
  test("a poll refused repeatedly keeps asking for the listing and says it is waiting for the connection", async () => {
    vi.useFakeTimers();
    const catalogue = [{ id: "work", protocol: "openai-chat", auth: "bearer", implicit: true }];
    const first = connectFakeClient();
    first.on("evener/instance/list", () => ({ instances: [], availableProviders: catalogue }));
    await act(async () => {
      await credentialsStore.getState().fetch();
    });

    // The replacement's restore read keeps failing; the marker stays set.
    let reads = 0;
    const replacement = new FakeClient("ready");
    replacement.on("evener/instance/list", () => {
      reads += 1;
      if (reads <= 3) throw new Error("transport down");
      return { instances: [], availableProviders: catalogue };
    });
    replacement.on("evener/auth/device/poll", () => ({ state: "authorized" }));
    connectionStore.getState().connect(replacement);
    expect(credentialsStore.getState().listingFromPreviousConnection).toBe(true);

    const onSuccess = vi.fn();
    render(
      <DeviceCodeDialog
        name="work"
        flowId="flow-4"
        userCode="ABCD-EFGH"
        verificationUrl="https://verify"
        intervalSeconds={1}
        onCancel={() => {}}
        onSuccess={onSuccess}
        onRestart={() => {}}
      />,
    );

    // Each refused tick asks again (the first ask is the connect's own read,
    // then one per refusal) and the dialog says what it is waiting for.
    await advanceTime(1000);
    expect(screen.getByText("Waiting for the connection to be restored…")).toBeTruthy();
    const readsAfterFirstRefusal = replacement.calls.filter((call) => call.method === "evener/instance/list").length;
    await advanceTime(1000);
    expect(replacement.calls.filter((call) => call.method === "evener/instance/list").length).toBeGreaterThan(
      readsAfterFirstRefusal,
    );

    // The connection comes back (the fourth read succeeds), so the tick after
    // that polls the connection that is actually there and the wait is over.
    await advanceTime(1000);
    expect(credentialsStore.getState().listingFromPreviousConnection).toBe(false);
    await advanceTime(1000);
    await advanceTime(0);
    expect(screen.queryByText("Waiting for the connection to be restored…")).toBeNull();
    expect(replacement.calls.filter((call) => call.method === "evener/auth/device/poll")).toHaveLength(1);
    expect(onSuccess).toHaveBeenCalled();
  });

  test("clicking 'Start again' after expiry calls onRestart", async () => {
    vi.useFakeTimers();
    const fake = connectFakeClient();
    fake.on("evener/auth/device/poll", () => ({ state: "expired" }));
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
    await advanceTime(1000);
    fireEvent.click(screen.getByRole("button", { name: "Start again" }));
    expect(onRestart).toHaveBeenCalled();
  });

  test("unmounting the dialog stops polling (no further requests after unmount)", async () => {
    vi.useFakeTimers();
    const fake = connectFakeClient();
    let calls = 0;
    fake.on("evener/auth/device/poll", () => {
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
    await advanceTime(1000);
    expect(calls).toBe(1);
    unmount();
    const callsAtUnmount = calls;
    await advanceTime(1200);
    expect(calls).toBe(callsAtUnmount);
  });
});

// L2 (roborev round 6): the remote dialog and its success toast dropped the
// instance name - "Sign in on beta" / "Signed in on beta" - while the local
// variants carry it, so with several Codex-capable instances on one host the
// code on screen belonged to no instance in particular. Both name both now.
test("a remote device dialog names the instance AND the host", () => {
  connectFakeClient();
  render(
    <DeviceCodeDialog
      name="work"
      flowId="flow-2"
      userCode="ABCD-EFGH"
      verificationUrl="https://verify"
      intervalSeconds={5}
      host="beta"
      onCancel={() => {}}
      onSuccess={() => {}}
      onRestart={() => {}}
    />,
  );
  expect(screen.getByRole("dialog", { name: "Sign in to work on beta" })).toBeTruthy();
});

test("a completed remote sign-in names the instance AND the host in its toast", async () => {
  vi.useFakeTimers();
  const fake = connectFakeClient();
  fake.on("evener/host/request", (params) => {
    if (params.method === "evener/auth/device/poll") return { state: "authorized" };
    if (params.method === "evener/instance/list") return { instances: [], availableProviders: [] };
    throw new Error(`unexpected forwarded method ${params.method}`);
  });
  const onSuccess = vi.fn();
  render(
    <>
      <DeviceCodeDialog
        name="work"
        flowId="flow-2"
        userCode="ABCD-EFGH"
        verificationUrl="https://verify"
        intervalSeconds={1}
        host="beta"
        onCancel={() => {}}
        onSuccess={onSuccess}
        onRestart={() => {}}
      />
      <Toast />
    </>,
  );
  await advanceTime(1000);
  await advanceTime(0);
  expect(onSuccess).toHaveBeenCalled();
  expect(getToasts().some((toast) => toast.text === "Signed in to work on beta")).toBe(true);
});
