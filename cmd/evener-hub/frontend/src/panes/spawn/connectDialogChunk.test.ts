import { expect, test } from "vitest";
import {
  type ConnectDialogImporter,
  type ConnectDialogModule,
  loadConnectDialog,
  resetConnectDialogLoaderForTests,
  setConnectDialogImporterForTests,
} from "./connectDialogChunk";

const CONNECT_DIALOG_MODULE = { ConnectProviderDialog: () => null } as unknown as ConnectDialogModule;

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
  try {
    await expect(loadConnectDialog(true)).resolves.toBe(CONNECT_DIALOG_MODULE);
    expect(evaluatedURLs).toEqual([expect.stringContaining("evener-dialog-retry=1")]);
    expect(new URL(evaluatedURLs[0]!).pathname).toBe("/webassets/ConnectProviderDialog-a1b2c3.js");
  } finally {
    resetConnectDialogLoaderForTests();
  }
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
  try {
    await expect(loadConnectDialog(true)).resolves.toBe(CONNECT_DIALOG_MODULE);
    expect(evaluatedURLs).toEqual([undefined]);
  } finally {
    resetConnectDialogLoaderForTests();
  }
});
