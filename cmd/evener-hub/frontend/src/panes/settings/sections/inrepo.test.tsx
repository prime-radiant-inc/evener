import type { LaunchConfigResolved } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, describe, expect, test } from "vitest";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import { connectionStore } from "../../../stores/connection";
import { connectReadyFakeClient } from "../../../testing/connectionTestUtils";
import { RepositoryConfigSection } from "./inrepo";
import { shownReasons } from "./reasonTestUtils";

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
});

function connectFakeClient(): FakeClient {
  return connectReadyFakeClient((fake) => {
    fake.on("evener/path/validate", ({ path }) => ({ valid: true, path: path === "~" ? "/home" : path }));
    fake.on("evener/paths/complete", () => ({ data: [] }));
  });
}

function resolvedWithRepo(repo: LaunchConfigResolved["repo"]): LaunchConfigResolved {
  return { effective: {}, layers: {}, provenance: {}, repo };
}

/** What the status area shows: empty, loading, error, or the resolved
 * file's trust state (absent when there is no in-repo file). */
function inRepoState(): string | null {
  return screen.getByTestId("inrepo-status").getAttribute("data-state");
}

/** Waits for the absent repository-file state. */
async function noFileFound(): Promise<void> {
  await waitFor(() => expect(inRepoState()).toBe("absent"));
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
  test("resolves the contextual directory rather than lastCwd", async () => {
    localStorage.setItem("lastCwd", "/unrelated");
    const fake = connectFakeClient();
    fake.on("evener/launch/resolve", (params) => {
      expect(params).toEqual({ cwd: "/repo", launchOverrides: undefined });
      return resolvedWithRepo({ path: ".evener/launch.toml", trust: "absent" });
    });
    render(<RepositoryConfigSection host="local" cwd="/repo" />);
    expect(screen.queryByRole("button", { name: /working dir/i })).toBeNull();
    await noFileFound();
  });

  // FIX 2b: the loading phase gets the Loader widget instead of a static
  // "Loading…" string.
  test("shows a Loader while the resolve is in flight", async () => {
    localStorage.setItem("lastCwd", "/unrelated");
    const fake = connectFakeClient();
    let release!: () => void;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    fake.on("evener/launch/resolve", async () => {
      await gate;
      return resolvedWithRepo({ path: ".evener/launch.toml", trust: "absent" });
    });
    render(<RepositoryConfigSection host="local" cwd="/repo" />);
    expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
    expect(inRepoState()).toBe("loading");

    release();
    await noFileFound();
  });

  test("an empty contextual directory issues no RPC", () => {
    const fake = connectFakeClient();
    let called = false;
    fake.on("evener/launch/resolve", () => {
      called = true;
      return resolvedWithRepo(undefined);
    });
    render(<RepositoryConfigSection host="local" cwd="" />);
    expect(inRepoState()).toBe("empty");
    expect(called).toBe(false);
  });

  test("a directory change before connection readiness resolves only the current context", async () => {
    const fake = new FakeClient("idle");
    connectionStore.getState().connect(fake);
    const calls: string[] = [];
    fake.on("evener/launch/resolve", ({ cwd }) => {
      calls.push(cwd);
      return resolvedWithRepo(undefined);
    });
    const view = render(<RepositoryConfigSection host="local" cwd="/initial" />);
    view.rerender(<RepositoryConfigSection host="local" cwd="/current" />);
    expect(calls).toEqual([]);
    act(() => fake.emitReady());
    await waitFor(() => expect(calls).toEqual(["/current"]));
  });
});

describe("trust states", () => {
  test("absent: shows the no-file message and nothing else (no preview, no trust button)", async () => {
    localStorage.setItem("lastCwd", "/unrelated");
    const fake = connectFakeClient();
    fake.on("evener/launch/resolve", () => resolvedWithRepo({ path: ".evener/launch.toml", trust: "absent" }));
    render(<RepositoryConfigSection host="local" cwd="/repo" />);
    await noFileFound();
    expect(screen.queryByRole("button", { name: /trust this file/i })).toBeNull();
  });

  test("trusted: shows the content hash, the preview, and no trust button", async () => {
    localStorage.setItem("lastCwd", "/unrelated");
    const fake = connectFakeClient();
    fake.on("evener/launch/resolve", () =>
      resolvedWithRepo({ path: ".evener/launch.toml", trust: "trusted", hash: "abc123", preview: "model = 'x'" }),
    );
    render(<RepositoryConfigSection host="local" cwd="/repo" />);
    await waitFor(() => expect(inRepoState()).toBe("trusted"));
    expect(screen.getByText("abc123")).toBeTruthy();
    expect(screen.getByText("model = 'x'")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Copy" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /trust this file/i })).toBeNull();
  });

  test.each(["untrusted", "changed", "rejected"])("%s: shows the trust button", async (trust) => {
    localStorage.setItem("lastCwd", "/unrelated");
    const fake = connectFakeClient();
    fake.on("evener/launch/resolve", () => resolvedWithRepo({ path: ".evener/launch.toml", trust, hash: "abc123" }));
    render(<RepositoryConfigSection host="local" cwd="/repo" />);
    await waitFor(() => expect(inRepoState()).toBe(trust));
    expect(screen.getByRole("button", { name: /trust this file/i })).toBeTruthy();
  });
});

describe("trust action", () => {
  test("a pending trust failure belongs only to the directory that submitted it", async () => {
    const fake = connectFakeClient();
    let rejectTrust: (reason: Error) => void = () => {
      throw new Error("trust not started");
    };
    fake.on("evener/launch/resolve", ({ cwd }) =>
      resolvedWithRepo({
        path: `${cwd}/.evener/launch.toml`,
        trust: "untrusted",
        hash: cwd,
        preview: cwd,
      }),
    );
    fake.on(
      "evener/launch/trustRepo",
      () =>
        new Promise((_resolve, reject) => {
          rejectTrust = reject;
        }),
    );
    const view = render(<RepositoryConfigSection host="local" cwd="/old" />);
    await userEvent.setup().click(await screen.findByRole("button", { name: /trust this file/i }));
    view.rerender(<RepositoryConfigSection host="local" cwd="/new" />);
    await screen.findByText("/new");
    await act(async () => rejectTrust(new Error("old approval failed")));
    expect(shownReasons()).not.toContain("trust-failed");
    expect((screen.getByRole("button", { name: /trust this file/i }) as HTMLButtonElement).disabled).toBe(false);
    expect(screen.queryByText("/old")).toBeNull();
  });

  test("a late resolve cannot replace the current directory preview", async () => {
    const fake = connectFakeClient();
    let release: (value: LaunchConfigResolved) => void = () => {
      throw new Error("resolve not started");
    };
    fake.on("evener/launch/resolve", ({ cwd }) =>
      cwd === "/old"
        ? new Promise((resolve) => {
            release = resolve;
          })
        : resolvedWithRepo({ path: `${cwd}/.evener/launch.toml`, trust: "untrusted", hash: cwd, preview: cwd }),
    );
    const view = render(<RepositoryConfigSection host="local" cwd="/old" />);
    view.rerender(<RepositoryConfigSection host="local" cwd="/new" />);
    await screen.findByText("/new");
    await act(async () =>
      release(
        resolvedWithRepo({ path: "/old/.evener/launch.toml", trust: "trusted", hash: "old-hash", preview: "/old" }),
      ),
    );
    expect(inRepoState()).toBe("untrusted");
    expect(screen.queryByText("/old")).toBeNull();
    expect(screen.getByText("/new")).toBeTruthy();
  });

  test("clicking Trust calls trustRepo(cwd, hash) then re-resolves", async () => {
    localStorage.setItem("lastCwd", "/unrelated");
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
    render(<RepositoryConfigSection host="local" cwd="/repo" />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: /trust this file/i }));
    await waitFor(() => expect(inRepoState()).toBe("trusted"));
    expect(resolveCalls).toBe(2);
  });

  test("a trust failure appends an inline error while keeping the preview/note visible", async () => {
    localStorage.setItem("lastCwd", "/unrelated");
    const fake = connectFakeClient();
    fake.on("evener/launch/resolve", () =>
      resolvedWithRepo({ path: ".evener/launch.toml", trust: "untrusted", hash: "abc123", preview: "model = 'x'" }),
    );
    fake.on("evener/launch/trustRepo", () => {
      throw new Error("file changed since review");
    });
    render(<RepositoryConfigSection host="local" cwd="/repo" />);
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
    localStorage.setItem("lastCwd", "/unrelated");
    const fake = connectFakeClient();
    fake.on("evener/launch/resolve", () => {
      throw new Error("boom");
    });
    render(<RepositoryConfigSection host="local" cwd="/repo" />);
    // error is converted via friendlyErrorMessage: raw JS errors become the generic message
    await waitFor(() => expect(inRepoState()).toBe("error"));
    // Assert the raw string no longer appears
    expect(screen.queryByText(/boom/)).toBeNull();
  });
});
