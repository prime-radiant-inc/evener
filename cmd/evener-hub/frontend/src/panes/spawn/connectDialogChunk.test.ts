import { afterEach, beforeEach, expect, test } from "vitest";
import {
  type ConnectDialogImporter,
  type ConnectDialogModule,
  loadConnectDialog,
  resetConnectDialogLoaderForTests,
  setConnectDialogImporterForTests,
} from "./connectDialogChunk";

const CONNECT_DIALOG_MODULE = { ConnectProviderDialog: () => null } as unknown as ConnectDialogModule;

function appendViteConnectDialogAssets(): void {
  const modulepreload = document.createElement("link");
  modulepreload.rel = "modulepreload";
  modulepreload.href = "/webassets/ConnectProviderDialog-a1b2c3.js";
  modulepreload.crossOrigin = "";
  modulepreload.setAttribute("nonce", "dialog-nonce");

  const stylesheet = document.createElement("link");
  stylesheet.rel = "stylesheet";
  stylesheet.href = "/webassets/ConnectProviderDialog-d4e5f6.css";
  stylesheet.crossOrigin = "";
  stylesheet.setAttribute("nonce", "dialog-nonce");

  document.head.append(modulepreload, stylesheet);
}

function retryStylesheet(): HTMLLinkElement {
  const link = Array.from(document.querySelectorAll<HTMLLinkElement>('link[rel="stylesheet"]')).find((candidate) =>
    candidate.href.includes("evener-dialog-retry=1"),
  );
  if (!link) throw new Error("retry ConnectProviderDialog stylesheet was not appended");
  return link;
}

beforeEach(() => {
  document.head.replaceChildren();
  resetConnectDialogLoaderForTests();
});

afterEach(() => {
  document.head.replaceChildren();
  resetConnectDialogLoaderForTests();
});

test("a retry evaluates a cache-busted URL, not the failed one", async () => {
  const initialImporter: ConnectDialogImporter = () =>
    Promise.reject(
      new Error("Failed to fetch dynamically imported module: /webassets/ConnectProviderDialog-a1b2c3.js"),
    );
  setConnectDialogImporterForTests(initialImporter);
  await expect(loadConnectDialog()).rejects.toThrow("Failed to fetch dynamically imported module");

  const evaluatedURLs: string[] = [];
  const retryImporter: ConnectDialogImporter = (retryURL) => {
    evaluatedURLs.push(retryURL ?? "");
    return Promise.resolve(CONNECT_DIALOG_MODULE);
  };
  setConnectDialogImporterForTests(retryImporter);
  await expect(loadConnectDialog(true)).resolves.toBe(CONNECT_DIALOG_MODULE);
  expect(evaluatedURLs).toEqual([expect.stringContaining("evener-dialog-retry=1")]);
  expect(new URL(evaluatedURLs[0]!).pathname).toBe("/webassets/ConnectProviderDialog-a1b2c3.js");
});

test("without a remembered chunk URL the retry reuses the plain import", async () => {
  // No failure has been seen, so there is no hashed filename to cache-bust:
  // the retry must still reach the importer (with no URL) rather than throw
  // on a missing URL.
  resetConnectDialogLoaderForTests();
  const evaluatedURLs: Array<string | undefined> = [];
  setConnectDialogImporterForTests((retryURL) => {
    evaluatedURLs.push(retryURL);
    return Promise.resolve(CONNECT_DIALOG_MODULE);
  });
  await expect(loadConnectDialog(true)).resolves.toBe(CONNECT_DIALOG_MODULE);
  expect(evaluatedURLs).toEqual([undefined]);
});

test("retry waits for a cache-busted ConnectProviderDialog stylesheet before evaluating JS", async () => {
  const initialImporter: ConnectDialogImporter = () => {
    // This is the synchronous part of Vite's generated preload wrapper. The
    // CSS request fails before the first module can evaluate.
    appendViteConnectDialogAssets();
    return Promise.reject(new Error("Unable to preload CSS for /webassets/ConnectProviderDialog-d4e5f6.css"));
  };
  setConnectDialogImporterForTests(initialImporter);
  await expect(loadConnectDialog()).rejects.toThrow("Unable to preload CSS");

  const evaluatedURLs: string[] = [];
  const retryImporter: ConnectDialogImporter = (retryURL) => {
    evaluatedURLs.push(retryURL ?? "");
    return Promise.resolve(CONNECT_DIALOG_MODULE);
  };
  setConnectDialogImporterForTests(retryImporter);
  const retry = loadConnectDialog(true);

  expect(evaluatedURLs).toEqual([]);
  const stylesheet = retryStylesheet();
  const retryPreload = Array.from(document.querySelectorAll<HTMLLinkElement>('link[rel="modulepreload"]')).find(
    (link) => link.href.includes("evener-dialog-retry=1"),
  );
  expect(retryPreload).toBeTruthy();
  expect(stylesheet.href).toContain("evener-dialog-retry=1");
  expect(stylesheet.getAttribute("crossorigin")).toBe("");
  expect(stylesheet.getAttribute("nonce")).toBe("dialog-nonce");

  stylesheet.dispatchEvent(new Event("load"));
  await expect(retry).resolves.toBe(CONNECT_DIALOG_MODULE);
  expect(evaluatedURLs).toEqual([expect.stringContaining("evener-dialog-retry=1")]);
  expect(new URL(evaluatedURLs[0]!).searchParams.get("evener-dialog-retry")).toBe(
    new URL(stylesheet.href).searchParams.get("evener-dialog-retry"),
  );
});

test("a retry CSS error prevents cache-busted ConnectProviderDialog JS evaluation", async () => {
  setConnectDialogImporterForTests(() => {
    appendViteConnectDialogAssets();
    return Promise.reject(new Error("Unable to preload CSS for /webassets/ConnectProviderDialog-d4e5f6.css"));
  });
  await expect(loadConnectDialog()).rejects.toThrow("Unable to preload CSS");

  let evaluations = 0;
  setConnectDialogImporterForTests(() => {
    evaluations += 1;
    return Promise.resolve(CONNECT_DIALOG_MODULE);
  });
  const retry = loadConnectDialog(true);
  retryStylesheet().dispatchEvent(new Event("error"));

  await expect(retry).rejects.toThrow("Unable to preload ConnectDialog CSS");
  expect(evaluations).toBe(0);
});
