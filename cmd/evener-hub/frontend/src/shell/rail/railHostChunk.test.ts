import { afterEach, beforeEach, expect, test } from "vitest";
import {
  loadRailHost,
  type RailHostImporter,
  type RailHostModule,
  resetRailHostLoaderForTests,
  setRailHostImporterForTests,
} from "./railHostChunk";

const RAIL_HOST_MODULE = { RailHost: () => null } as unknown as RailHostModule;

function appendViteRailHostAssets(): void {
  const modulepreload = document.createElement("link");
  modulepreload.rel = "modulepreload";
  modulepreload.href = "/webassets/RailHost-a1b2c3.js";
  modulepreload.crossOrigin = "";
  modulepreload.setAttribute("nonce", "rail-nonce");

  const stylesheet = document.createElement("link");
  stylesheet.rel = "stylesheet";
  stylesheet.href = "/webassets/RailHost-d4e5f6.css";
  stylesheet.crossOrigin = "";
  stylesheet.setAttribute("nonce", "rail-nonce");

  document.head.append(modulepreload, stylesheet);
}

function retryStylesheet(): HTMLLinkElement {
  const link = Array.from(document.querySelectorAll<HTMLLinkElement>('link[rel="stylesheet"]')).find((candidate) =>
    candidate.href.includes("evener-rail-retry=1"),
  );
  if (!link) throw new Error("retry RailHost stylesheet was not appended");
  return link;
}

beforeEach(() => {
  document.head.replaceChildren();
  resetRailHostLoaderForTests();
});

afterEach(() => {
  document.head.replaceChildren();
  resetRailHostLoaderForTests();
});

test("a retry evaluates a cache-busted URL, not the failed one", async () => {
  const initialImporter: RailHostImporter = () =>
    Promise.reject(new Error("Failed to fetch dynamically imported module: /webassets/RailHost-a1b2c3.js"));
  setRailHostImporterForTests(initialImporter);
  await expect(loadRailHost()).rejects.toThrow("Failed to fetch dynamically imported module");

  const evaluatedURLs: string[] = [];
  const retryImporter: RailHostImporter = (retryURL) => {
    evaluatedURLs.push(retryURL ?? "");
    return Promise.resolve(RAIL_HOST_MODULE);
  };
  setRailHostImporterForTests(retryImporter);
  await expect(loadRailHost(true)).resolves.toBe(RAIL_HOST_MODULE);
  expect(evaluatedURLs).toEqual([expect.stringContaining("evener-rail-retry=1")]);
  expect(new URL(evaluatedURLs[0]!).pathname).toBe("/webassets/RailHost-a1b2c3.js");
});

test("without a remembered chunk URL the retry reuses the plain import", async () => {
  // No failure has been seen, so there is no hashed filename to cache-bust:
  // the retry must still reach the importer (with no URL) rather than throw
  // on a missing URL.
  resetRailHostLoaderForTests();
  const evaluatedURLs: Array<string | undefined> = [];
  setRailHostImporterForTests((retryURL) => {
    evaluatedURLs.push(retryURL);
    return Promise.resolve(RAIL_HOST_MODULE);
  });
  await expect(loadRailHost(true)).resolves.toBe(RAIL_HOST_MODULE);
  expect(evaluatedURLs).toEqual([undefined]);
});

test("retry waits for a cache-busted RailHost stylesheet before evaluating JS", async () => {
  const initialImporter: RailHostImporter = () => {
    // This is the synchronous part of Vite's generated preload wrapper. The
    // CSS request fails before the first module can evaluate.
    appendViteRailHostAssets();
    return Promise.reject(new Error("Unable to preload CSS for /webassets/RailHost-d4e5f6.css"));
  };
  setRailHostImporterForTests(initialImporter);
  await expect(loadRailHost()).rejects.toThrow("Unable to preload CSS");

  const evaluatedURLs: string[] = [];
  const retryImporter: RailHostImporter = (retryURL) => {
    evaluatedURLs.push(retryURL ?? "");
    return Promise.resolve(RAIL_HOST_MODULE);
  };
  setRailHostImporterForTests(retryImporter);
  const retry = loadRailHost(true);

  expect(evaluatedURLs).toEqual([]);
  const stylesheet = retryStylesheet();
  const retryPreload = Array.from(document.querySelectorAll<HTMLLinkElement>('link[rel="modulepreload"]')).find(
    (link) => link.href.includes("evener-rail-retry=1"),
  );
  expect(retryPreload).toBeTruthy();
  expect(stylesheet.href).toContain("evener-rail-retry=1");
  expect(stylesheet.getAttribute("crossorigin")).toBe("");
  expect(stylesheet.getAttribute("nonce")).toBe("rail-nonce");

  stylesheet.dispatchEvent(new Event("load"));
  await expect(retry).resolves.toBe(RAIL_HOST_MODULE);
  expect(evaluatedURLs).toEqual([expect.stringContaining("evener-rail-retry=1")]);
  expect(new URL(evaluatedURLs[0]!).searchParams.get("evener-rail-retry")).toBe(
    new URL(stylesheet.href).searchParams.get("evener-rail-retry"),
  );
});

test("a retry CSS error prevents cache-busted RailHost JS evaluation", async () => {
  setRailHostImporterForTests(() => {
    appendViteRailHostAssets();
    return Promise.reject(new Error("Unable to preload CSS for /webassets/RailHost-d4e5f6.css"));
  });
  await expect(loadRailHost()).rejects.toThrow("Unable to preload CSS");

  let evaluations = 0;
  setRailHostImporterForTests(() => {
    evaluations += 1;
    return Promise.resolve(RAIL_HOST_MODULE);
  });
  const retry = loadRailHost(true);
  retryStylesheet().dispatchEvent(new Event("error"));

  await expect(retry).rejects.toThrow("Unable to preload RailHost CSS");
  expect(evaluations).toBe(0);
});
