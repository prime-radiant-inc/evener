import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, describe, expect, test } from "vitest";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { LaunchConfigResolved } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { InRepoSection } from "./inrepo";

// See shell/DockHost.test.tsx's identical comment: Node 26 shadows jsdom's
// real window.localStorage with its own (non-functional under vitest)
// global, so every test file that touches localStorage needs this same
// small in-memory stand-in. Scoped to this file only.
class MemoryStorage {
  private store = new Map<string, string>();
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) ?? null) : null;
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  clear(): void {
    this.store.clear();
  }
}

beforeAll(() => {
  // @ts-expect-error see MemoryStorage's own comment for why this is needed
  globalThis.localStorage = new MemoryStorage();
});

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

function resolvedWithRepo(repo: LaunchConfigResolved["repo"]): LaunchConfigResolved {
  return { effective: {}, layers: {}, provenance: {}, repo };
}

// The "No .serf/launch.toml in {cwd}." note is split across text + <code> +
// text + <code> + text nodes (see inrepo.tsx's ResolvedStatus) - a single
// regex/string getByText can't match text broken across sibling elements,
// so this matches on the containing <p>'s full textContent instead.
function findsNoFileMessage(cwd: string) {
  return (_: string, element: Element | null) =>
    element?.tagName === "P" && element.textContent === `No .serf/launch.toml in ${cwd}.`;
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
    fake.on("serf/launch/resolve", (params) => {
      expect(params).toEqual({ cwd: "/repo", launchOverrides: undefined });
      return resolvedWithRepo({ path: ".serf/launch.toml", trust: "absent" });
    });
    render(<InRepoSection sectionId="inrepo" />);
    expect(screen.getByLabelText(/working dir/i)).toHaveProperty("value", "/repo");
    await screen.findByText(findsNoFileMessage("/repo"));
  });

  test("no lastCwd in storage and an empty field: shows 'Enter a working directory.' with no RPC call", () => {
    const fake = connectFakeClient();
    let called = false;
    fake.on("serf/launch/resolve", () => {
      called = true;
      return resolvedWithRepo(undefined);
    });
    render(<InRepoSection sectionId="inrepo" />);
    expect(screen.getByText("Enter a working directory.")).toBeTruthy();
    expect(called).toBe(false);
  });
});

describe("blur/Enter-triggered re-resolve (not per-keystroke)", () => {
  test("typing alone does not refresh; blurring the field does", async () => {
    const fake = connectFakeClient();
    const calls: string[] = [];
    fake.on("serf/launch/resolve", (params) => {
      calls.push(params.cwd);
      return resolvedWithRepo({ path: ".serf/launch.toml", trust: "absent" });
    });
    render(<InRepoSection sectionId="inrepo" />);
    const user = userEvent.setup();
    const input = screen.getByLabelText(/working dir/i);
    await user.type(input, "/repo");
    expect(calls).toEqual([]); // no RPC per keystroke
    await user.tab(); // blur
    await waitFor(() => expect(calls).toEqual(["/repo"]));
  });
});

describe("trust states", () => {
  test("absent: shows the no-file message and nothing else (no preview, no trust button)", async () => {
    localStorage.setItem("lastCwd", "/repo");
    const fake = connectFakeClient();
    fake.on("serf/launch/resolve", () => resolvedWithRepo({ path: ".serf/launch.toml", trust: "absent" }));
    render(<InRepoSection sectionId="inrepo" />);
    await screen.findByText(findsNoFileMessage("/repo"));
    expect(screen.queryByRole("button", { name: /trust this file/i })).toBeNull();
  });

  test("trusted: shows the content hash, the preview, and no trust button", async () => {
    localStorage.setItem("lastCwd", "/repo");
    const fake = connectFakeClient();
    fake.on("serf/launch/resolve", () =>
      resolvedWithRepo({ path: ".serf/launch.toml", trust: "trusted", hash: "abc123", preview: "model = 'x'" }),
    );
    render(<InRepoSection sectionId="inrepo" />);
    await screen.findByText(/Trusted/);
    expect(screen.getByText("abc123")).toBeTruthy();
    expect(screen.getByText("model = 'x'")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /trust this file/i })).toBeNull();
  });

  test.each([
    ["untrusted", /Untrusted/],
    ["changed", /file has changed/],
    ["rejected", /Previously rejected/],
  ])("%s: shows the trust button", async (trust, copyPattern) => {
    localStorage.setItem("lastCwd", "/repo");
    const fake = connectFakeClient();
    fake.on("serf/launch/resolve", () => resolvedWithRepo({ path: ".serf/launch.toml", trust, hash: "abc123" }));
    render(<InRepoSection sectionId="inrepo" />);
    await screen.findByText(copyPattern);
    expect(screen.getByRole("button", { name: /trust this file/i })).toBeTruthy();
  });
});

describe("trust action", () => {
  test("clicking Trust calls trustRepo(cwd, hash) then re-resolves", async () => {
    localStorage.setItem("lastCwd", "/repo");
    const fake = connectFakeClient();
    let resolveCalls = 0;
    fake.on("serf/launch/resolve", () => {
      resolveCalls += 1;
      return resolveCalls === 1
        ? resolvedWithRepo({ path: ".serf/launch.toml", trust: "untrusted", hash: "abc123" })
        : resolvedWithRepo({ path: ".serf/launch.toml", trust: "trusted", hash: "abc123" });
    });
    fake.on("serf/launch/trustRepo", (params) => {
      expect(params).toEqual({ cwd: "/repo", hash: "abc123" });
      return resolvedWithRepo({ path: ".serf/launch.toml", trust: "trusted", hash: "abc123" });
    });
    render(<InRepoSection sectionId="inrepo" />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: /trust this file/i }));
    await screen.findByText(/Trusted/);
    expect(resolveCalls).toBe(2);
  });

  test("a trust failure appends an inline error while keeping the preview/note visible", async () => {
    localStorage.setItem("lastCwd", "/repo");
    const fake = connectFakeClient();
    fake.on("serf/launch/resolve", () =>
      resolvedWithRepo({ path: ".serf/launch.toml", trust: "untrusted", hash: "abc123", preview: "model = 'x'" }),
    );
    fake.on("serf/launch/trustRepo", () => {
      throw new Error("file changed since review");
    });
    render(<InRepoSection sectionId="inrepo" />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: /trust this file/i }));
    await screen.findByText(/Trust failed: file changed since review/);
    expect(screen.getByText("model = 'x'")).toBeTruthy();
    expect(screen.getByText(/Untrusted/)).toBeTruthy();
  });
});

describe("resolve failure", () => {
  test("shows 'Failed to load: {message}'", async () => {
    localStorage.setItem("lastCwd", "/repo");
    const fake = connectFakeClient();
    fake.on("serf/launch/resolve", () => {
      throw new Error("boom");
    });
    render(<InRepoSection sectionId="inrepo" />);
    await screen.findByText("Failed to load: boom");
  });
});
