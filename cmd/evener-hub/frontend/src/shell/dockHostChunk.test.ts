import { afterEach, beforeEach, expect, test } from "vitest";
import {
  type DockHostImporter,
  type DockHostModule,
  isStaleDockHostChunkError,
  loadDockHost,
  resetDockHostLoaderForTests,
  setDockHostImporterForTests,
} from "./dockHostChunk";

const DOCK_HOST_MODULE = { DockHost: () => null } as unknown as DockHostModule;

function appendViteDockHostAssets(): void {
  const modulepreload = document.createElement("link");
  modulepreload.rel = "modulepreload";
  modulepreload.href = "/webassets/DockHost-a1b2c3.js";
  modulepreload.crossOrigin = "";
  modulepreload.setAttribute("nonce", "dock-nonce");

  const stylesheet = document.createElement("link");
  stylesheet.rel = "stylesheet";
  stylesheet.href = "/webassets/DockHost-d4e5f6.css";
  stylesheet.crossOrigin = "";
  stylesheet.setAttribute("nonce", "dock-nonce");

  document.head.append(modulepreload, stylesheet);
}

function retryStylesheet(): HTMLLinkElement {
  const link = Array.from(document.querySelectorAll<HTMLLinkElement>('link[rel="stylesheet"]')).find((candidate) =>
    candidate.href.includes("serf-dock-retry=1"),
  );
  if (!link) throw new Error("retry DockHost stylesheet was not appended");
  return link;
}

beforeEach(() => {
  document.head.replaceChildren();
  resetDockHostLoaderForTests();
});

afterEach(() => {
  document.head.replaceChildren();
  resetDockHostLoaderForTests();
});

test("retry waits for a cache-busted DockHost stylesheet before evaluating JS", async () => {
  const initialImporter: DockHostImporter = () => {
    // This is the synchronous part of Vite's generated preload wrapper. The
    // CSS request fails before the first module can evaluate.
    appendViteDockHostAssets();
    return Promise.reject(new Error("Unable to preload CSS for /webassets/DockHost-d4e5f6.css"));
  };
  setDockHostImporterForTests(initialImporter);
  await expect(loadDockHost()).rejects.toThrow("Unable to preload CSS");

  const evaluatedURLs: string[] = [];
  const retryImporter: DockHostImporter = (retryURL) => {
    evaluatedURLs.push(retryURL ?? "");
    return Promise.resolve(DOCK_HOST_MODULE);
  };
  setDockHostImporterForTests(retryImporter);
  const retry = loadDockHost(true);

  expect(evaluatedURLs).toEqual([]);
  const stylesheet = retryStylesheet();
  const retryPreload = Array.from(document.querySelectorAll<HTMLLinkElement>('link[rel="modulepreload"]')).find(
    (link) => link.href.includes("serf-dock-retry=1"),
  );
  expect(retryPreload).toBeTruthy();
  expect(stylesheet.href).toContain("serf-dock-retry=1");
  expect(stylesheet.getAttribute("crossorigin")).toBe("");
  expect(stylesheet.getAttribute("nonce")).toBe("dock-nonce");

  stylesheet.dispatchEvent(new Event("load"));
  await expect(retry).resolves.toBe(DOCK_HOST_MODULE);
  expect(evaluatedURLs).toEqual([expect.stringContaining("serf-dock-retry=1")]);
  expect(new URL(evaluatedURLs[0]!).searchParams.get("serf-dock-retry")).toBe(
    new URL(stylesheet.href).searchParams.get("serf-dock-retry"),
  );
});

test("a retry CSS error prevents cache-busted DockHost JS evaluation", async () => {
  setDockHostImporterForTests(() => {
    appendViteDockHostAssets();
    return Promise.reject(new Error("Unable to preload CSS for /webassets/DockHost-d4e5f6.css"));
  });
  await expect(loadDockHost()).rejects.toThrow("Unable to preload CSS");

  let evaluations = 0;
  setDockHostImporterForTests(() => {
    evaluations += 1;
    return Promise.resolve(DOCK_HOST_MODULE);
  });
  const retry = loadDockHost(true);
  retryStylesheet().dispatchEvent(new Event("error"));

  await expect(retry).rejects.toThrow("Unable to preload DockHost CSS");
  expect(evaluations).toBe(0);
});

test("a stale DockHost stylesheet error offers the page-reload fallback", () => {
  expect(
    isStaleDockHostChunkError(new Error("Failed to fetch dynamically imported module: /webassets/DockHost-d4e5f6.css")),
  ).toBe(true);
});
