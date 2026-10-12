import type { LaunchConfigResolved } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, describe, expect, test } from "vitest";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import { connectionStore } from "../../../stores/connection";
import { InRepoSection } from "./inrepo";
import { shownReasons } from "./reasonTestUtils";

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
});

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  fake.on("evener/path/validate", ({ path }) => ({ valid: true, path: path === "~" ? "/home" : path }));
  fake.on("evener/paths/complete", () => ({ data: [] }));
  connectionStore.getState().connect(fake);
  return fake;
}

function resolvedWithRepo(repo: LaunchConfigResolved["repo"]): LaunchConfigResolved {
  return { effective: {}, layers: {}, provenance: {}, repo };
}

/** What the status area shows: empty, loading, error, or the resolved
 * file's trust state (absent when there is no in-repo file). */
function inRepoState(): string | null {
  return screen.getByTestId("inrepo-status").getAttribute("data-state");
}

/** Waits for the "no in-repo file" state for `cwd`. */
async function noFileFound(cwd: string): Promise<void> {
  await waitFor(() => expect(inRepoState()).toBe("absent"));
  expect(screen.getByTestId("inrepo-status").textContent).toContain(cwd);
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  localStorage.clear();
});

afterEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  cleanup();
});

describe("initial load", () => {
  test("pre-fills the cwd input from localStorage's lastCwd and resolves it immediately", async () => {
    localStorage.setItem("lastCwd", "/repo");
    const fake = connectFakeClient();
    fake.on("evener/launch/resolve", (params) => {
      expect(params).toEqual({ cwd: "/repo", launchOverrides: undefined });
      return resolvedWithRepo({ path: ".evener/launch.toml", trust: "absent" });
    });
    render(<InRepoSection sectionId="inrepo" />);
    expect(screen.getByRole("button", { name: "Working dir: /repo — browse" })).toBeTruthy();
    await noFileFound("/repo");
  });

  // FIX 2b: the loading phase gets the Loader widget instead of a static
  // "Loading…" string.
  test("shows a Loader while the resolve is in flight", async () => {
    localStorage.setItem("lastCwd", "/repo");
    const fake = connectFakeClient();
    let release!: () => void;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    fake.on("evener/launch/resolve", async () => {
      await gate;
      return resolvedWithRepo({ path: ".evener/launch.toml", trust: "absent" });
    });
    render(<InRepoSection sectionId="inrepo" />);
    expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
    expect(inRepoState()).toBe("loading");

    release();
    await noFileFound("/repo");
  });

  test("no lastCwd in storage and an empty field: shows 'Enter a working directory.' with no RPC call", () => {
    const fake = connectFakeClient();
    let called = false;
    fake.on("evener/launch/resolve", () => {
      called = true;
      return resolvedWithRepo(undefined);
    });
    render(<InRepoSection sectionId="inrepo" />);
    expect(inRepoState()).toBe("empty");
    expect(called).toBe(false);
  });

  // Pins the stale-closure bug useConnectedEffect's deferred-fire design can
  // introduce for any caller closing over mutable local state at mount time:
  // this section mounts before the connection is ready (a direct deep link
  // racing AppShell's own connect() handshake - useConnectedEffect's own
  // rationale), the user edits the field DURING that wait, and the
  // eventually-deferred initial resolve must reflect what they're now
  // looking at, not the value the effect closed over at mount.
  test("a directory draft does not replace the initial directory when the connection becomes ready", async () => {
    localStorage.setItem("lastCwd", "/initial");
    const fake = new FakeClient("idle"); // NOT ready yet - the deferred-fire path
    fake.on("evener/path/validate", ({ path }) => ({ valid: true, path: path === "~" ? "/home" : path }));
    fake.on("evener/paths/complete", () => ({ data: [] }));
    connectionStore.getState().connect(fake);
    const resolvedCwds: string[] = [];
    fake.on("evener/launch/resolve", (params) => {
      resolvedCwds.push(params.cwd);
      return resolvedWithRepo({ path: ".evener/launch.toml", trust: "absent" });
    });
    render(<InRepoSection sectionId="inrepo" />);
    expect(screen.getByLabelText(/working dir/i).textContent).toContain("/initial");

    // The user edits the field WHILE still waiting for the handshake.
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: /working dir/i }));
    const input = await screen.findByRole("textbox", { name: "Path" });
    await user.clear(input);
    await user.type(input, "/edited");
    expect(resolvedCwds).toEqual([]); // not ready yet - nothing has fired

    // The connection becomes ready - the deferred initial resolve fires now.
    act(() => {
      fake.emitReady();
    });

    await waitFor(() => expect(resolvedCwds).toEqual(["/initial"]));
    await user.type(input, "{Enter}");
    await user.click(screen.getByRole("button", { name: "Use this folder" }));
    await waitFor(() => expect(resolvedCwds).toEqual(["/initial", "/edited"]));
  });
});

describe("confirmed-directory re-resolve", () => {
  test("browsing and blur do not refresh; confirmation does", async () => {
    const fake = connectFakeClient();
    const calls: string[] = [];
    fake.on("evener/launch/resolve", (params) => {
      calls.push(params.cwd);
      return resolvedWithRepo({ path: ".evener/launch.toml", trust: "absent" });
    });
    render(<InRepoSection sectionId="inrepo" />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: /working dir/i }));
    const input = await screen.findByRole("textbox", { name: "Path" });
    await user.clear(input);
    await user.type(input, "/repo{Enter}");
    expect(calls).toEqual([]); // no RPC per keystroke
    await user.tab();
    expect(calls).toEqual([]);
    await user.click(screen.getByRole("button", { name: "Use this folder" }));
    await waitFor(() => expect(calls).toEqual(["/repo"]));
  });
});

describe("trust states", () => {
  test("absent: shows the no-file message and nothing else (no preview, no trust button)", async () => {
    localStorage.setItem("lastCwd", "/repo");
    const fake = connectFakeClient();
    fake.on("evener/launch/resolve", () => resolvedWithRepo({ path: ".evener/launch.toml", trust: "absent" }));
    render(<InRepoSection sectionId="inrepo" />);
    await noFileFound("/repo");
    expect(screen.queryByRole("button", { name: /trust this file/i })).toBeNull();
  });

  test("trusted: shows the content hash, the preview, and no trust button", async () => {
    localStorage.setItem("lastCwd", "/repo");
    const fake = connectFakeClient();
    fake.on("evener/launch/resolve", () =>
      resolvedWithRepo({ path: ".evener/launch.toml", trust: "trusted", hash: "abc123", preview: "model = 'x'" }),
    );
    render(<InRepoSection sectionId="inrepo" />);
    await waitFor(() => expect(inRepoState()).toBe("trusted"));
    expect(screen.getByText("abc123")).toBeTruthy();
    expect(screen.getByText("model = 'x'")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Copy" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /trust this file/i })).toBeNull();
  });

  test.each(["untrusted", "changed", "rejected"])("%s: shows the trust button", async (trust) => {
    localStorage.setItem("lastCwd", "/repo");
    const fake = connectFakeClient();
    fake.on("evener/launch/resolve", () => resolvedWithRepo({ path: ".evener/launch.toml", trust, hash: "abc123" }));
    render(<InRepoSection sectionId="inrepo" />);
    await waitFor(() => expect(inRepoState()).toBe(trust));
    expect(screen.getByRole("button", { name: /trust this file/i })).toBeTruthy();
  });
});

describe("trust action", () => {
  test("clicking Trust calls trustRepo(cwd, hash) then re-resolves", async () => {
    localStorage.setItem("lastCwd", "/repo");
    const fake = connectFakeClient();
    let resolveCalls = 0;
    fake.on("evener/launch/resolve", () => {
      resolveCalls += 1;
      return resolveCalls === 1
        ? resolvedWithRepo({ path: ".evener/launch.toml", trust: "untrusted", hash: "abc123" })
        : resolvedWithRepo({ path: ".evener/launch.toml", trust: "trusted", hash: "abc123" });
    });
    fake.on("evener/launch/trustRepo", (params) => {
      expect(params).toEqual({ cwd: "/repo", hash: "abc123" });
      return resolvedWithRepo({ path: ".evener/launch.toml", trust: "trusted", hash: "abc123" });
    });
    render(<InRepoSection sectionId="inrepo" />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: /trust this file/i }));
    await waitFor(() => expect(inRepoState()).toBe("trusted"));
    expect(resolveCalls).toBe(2);
  });

  test("a trust failure appends an inline error while keeping the preview/note visible", async () => {
    localStorage.setItem("lastCwd", "/repo");
    const fake = connectFakeClient();
    fake.on("evener/launch/resolve", () =>
      resolvedWithRepo({ path: ".evener/launch.toml", trust: "untrusted", hash: "abc123", preview: "model = 'x'" }),
    );
    fake.on("evener/launch/trustRepo", () => {
      throw new Error("file changed since review");
    });
    render(<InRepoSection sectionId="inrepo" />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: /trust this file/i }));
    // error is converted via friendlyErrorMessage: raw JS errors become the generic message
    await waitFor(() => expect(shownReasons()).toContain("trust-failed"));
    // Assert the raw string no longer appears
    expect(screen.queryByText(/file changed since review/)).toBeNull();
    expect(screen.getByText("model = 'x'")).toBeTruthy();
    expect(inRepoState()).toBe("untrusted");
  });
});

describe("resolve failure", () => {
  test("shows the load-failed state without the raw error", async () => {
    localStorage.setItem("lastCwd", "/repo");
    const fake = connectFakeClient();
    fake.on("evener/launch/resolve", () => {
      throw new Error("boom");
    });
    render(<InRepoSection sectionId="inrepo" />);
    // error is converted via friendlyErrorMessage: raw JS errors become the generic message
    await waitFor(() => expect(inRepoState()).toBe("error"));
    // Assert the raw string no longer appears
    expect(screen.queryByText(/boom/)).toBeNull();
  });
});
