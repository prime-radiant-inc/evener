import { expect, test } from "vitest";
import {
  loadRailHost,
  type RailHostImporter,
  type RailHostModule,
  resetRailHostLoaderForTests,
  setRailHostImporterForTests,
} from "./railHostChunk";

const RAIL_HOST_MODULE = { RailHost: () => null } as unknown as RailHostModule;

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
  try {
    await expect(loadRailHost(true)).resolves.toBe(RAIL_HOST_MODULE);
    expect(evaluatedURLs).toEqual([expect.stringContaining("evener-rail-retry=1")]);
    expect(new URL(evaluatedURLs[0]!).pathname).toBe("/webassets/RailHost-a1b2c3.js");
  } finally {
    resetRailHostLoaderForTests();
  }
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
  try {
    await expect(loadRailHost(true)).resolves.toBe(RAIL_HOST_MODULE);
    expect(evaluatedURLs).toEqual([undefined]);
  } finally {
    resetRailHostLoaderForTests();
  }
});
