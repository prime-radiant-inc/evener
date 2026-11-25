import type { HostRequestParams } from "@evener/appwire-client";
import { FILE_MENTION_DEBOUNCE_MS } from "@evener/appwire-client";
import { act, screen, waitFor, within } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { readModuleCss, topRuleBlock } from "../../styles/cssBlock";
import { getToasts } from "../../widgets/toast/store";
import "../session/testing/editorGeometry";
import { selectEditorText } from "../session/testing/editor";
import {
  chooseHost,
  fillPrompt,
  installCanvasStubs,
  installSpawnTestHooks,
  pastePngInto,
  promptField,
  readyClient,
  renderSpawn,
  routedDiscoveryDefault,
  seedSources,
  settled,
  setupUser,
  setWorkingDir,
  visitSpawnURL,
} from "./spawnTestUtils";

installSpawnTestHooks();

function fileMenu() {
  return screen.getByTestId("composer-file-menu");
}

function fileOptions() {
  return within(fileMenu()).getAllByRole("option");
}

function queryFileMenu() {
  return screen.queryByTestId("composer-file-menu");
}

test("typing @ queries evener/files/search directly on local host and through evener/host/request on online remote host", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", online: true, kind: "local" },
    { id: "buildbox", label: "buildbox", online: true, kind: "remote" },
  ]);
  window.history.pushState({}, "", "/new?dir=/srv/local-repo");

  const fake = readyClient((f) => {
    f.on("evener/files/search", () => ({
      items: [{ path: "local/app.go", kind: "file" }],
    }));
    f.on("evener/host/request", (raw) => {
      const p = raw as HostRequestParams;
      if (p.method === "evener/files/search") {
        return { items: [{ path: "remote/worker.rs", kind: "file" }] };
      }
      return routedDiscoveryDefault(p.method);
    });
  });

  renderSpawn(fake);
  await settled();

  await user.type(promptField(), "edit @");
  await act(async () => {
    vi.advanceTimersByTime(FILE_MENTION_DEBOUNCE_MS);
  });
  await waitFor(() => expect(fileOptions()).toHaveLength(1));
  expect(fileOptions()[0]?.textContent).toContain("app.go");
  expect(fileMenu().getAttribute("data-placement")).toBe("below");
  expect(fake.calls.some((c) => c.method === "evener/files/search")).toBe(true);

  // Switch host to online remote host "buildbox": menu resets and subsequent @ queries go through hostRequest.
  await chooseHost("buildbox");
  expect(queryFileMenu()).toBeNull();

  await user.type(promptField(), "app");
  await act(async () => {
    vi.advanceTimersByTime(FILE_MENTION_DEBOUNCE_MS);
  });
  await waitFor(() => expect(fileOptions()).toHaveLength(1));
  expect(fileOptions()[0]?.textContent).toContain("worker.rs");

  const hostSearchCall = fake.calls.find(
    (c) => c.method === "evener/host/request" && (c.params as HostRequestParams).method === "evener/files/search",
  );
  expect(hostSearchCall?.params).toMatchObject({
    host: "buildbox",
    method: "evener/files/search",
    params: { cwd: "/srv/local-repo", query: "app" },
  });
});

test("switching cwd or draft resets file menu and clears optimistic rows; missing cwd returns empty quietly", async () => {
  const user = setupUser();
  seedSources([{ id: "local", label: "Local", online: true, kind: "local" }]);
  window.history.pushState({}, "", "/new?dir=/srv/repo-a");

  const fake = readyClient((f) => {
    f.on("evener/files/search", (raw) => {
      const p = raw as { cwd?: string };
      if (p.cwd === "/srv/missing-dir") return { items: [] };
      if (p.cwd === "/srv/repo-b") return { items: [{ path: "repo_b.ts", kind: "file" }] };
      return { items: [{ path: "repo_a.ts", kind: "file" }] };
    });
  });

  renderSpawn(fake);
  await settled();

  await user.type(promptField(), "open @rep");
  await act(async () => {
    vi.advanceTimersByTime(FILE_MENTION_DEBOUNCE_MS);
  });
  await waitFor(() => expect(fileOptions()[0]?.textContent).toContain("repo_a.ts"));

  // Switching cwd resets the menu so repo_a.ts never bleeds into repo-b.
  await setWorkingDir(user, "/srv/repo-b");
  expect(queryFileMenu()).toBeNull();

  // Switching draft via URL also resets the menu.
  await user.type(promptField(), "open @rep");
  await act(async () => {
    vi.advanceTimersByTime(FILE_MENTION_DEBOUNCE_MS);
  });
  await waitFor(() => expect(fileOptions()[0]?.textContent).toContain("repo_b.ts"));

  await visitSpawnURL("/new?dir=/srv/missing-dir");
  expect(queryFileMenu()).toBeNull();

  // Missing directory returning [] keeps menu closed with zero toasts.
  await user.type(promptField(), "check @none");
  await act(async () => {
    vi.advanceTimersByTime(FILE_MENTION_DEBOUNCE_MS);
  });
  await act(async () => {});
  expect(queryFileMenu()).toBeNull();
  expect(getToasts()).toEqual([]);
});

test("file completion stylesheet anchors upward by default and opens downward for below placement", () => {
  const css = readModuleCss(import.meta.url, "../session/composer/filecompletionmenu.module.css");
  const listboxRule = topRuleBlock(css, ".listbox");
  expect(listboxRule).toContain("bottom: calc(100% + var(--space-1));");

  const belowRule = topRuleBlock(css, ".below");
  expect(belowRule).toContain("top: calc(100% + var(--space-half));");
  expect(belowRule).toContain("bottom: auto;");
});

test("pasting an image while spawn file menu is open dismisses completion and prevents stale-offset corruption of [image 1]", async () => {
  installCanvasStubs();
  const user = setupUser();
  seedSources([{ id: "local", label: "Local", online: true, kind: "local" }]);
  window.history.pushState({}, "", "/new?dir=/srv/local-repo");

  const fake = readyClient((f) => {
    f.on("evener/files/search", () => ({
      items: [{ path: "src/main.go", kind: "file" }],
    }));
  });

  renderSpawn(fake);
  await settled();

  await fillPrompt(user, "see @src/main.go");
  const editor = promptField();
  selectEditorText(editor, "see @src/ma".length);
  await act(async () => {
    vi.advanceTimersByTime(FILE_MENTION_DEBOUNCE_MS);
  });
  await waitFor(() => expect(fileOptions()).toHaveLength(1));

  await act(async () => {
    pastePngInto(editor, "shot.png");
  });
  await waitFor(() => expect(editor.textContent).toBe("see @src/ma[image 1]in.go"));
  await waitFor(() => expect(screen.getByRole("button", { name: "Remove shot.png" })).toBeTruthy());
  expect(queryFileMenu()).toBeNull();

  await user.keyboard("{Tab}");
  expect(editor.textContent).toBe("see @src/ma[image 1]in.go");
  expect(screen.getByRole("button", { name: "Remove shot.png" })).toBeTruthy();
});
