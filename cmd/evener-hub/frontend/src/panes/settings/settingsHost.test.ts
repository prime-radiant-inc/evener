import { expect, test } from "vitest";
import { LOCAL_HOST } from "../../stores/hostRouting";
import { settingsURL } from "./settingsHost";

// The default must leave today's URLs byte-for-byte unchanged, so `local`
// emits exactly what paneToURL already emitted.
test("local is exactly the URL paneToURL already emitted", () => {
  expect(settingsURL("credentials", LOCAL_HOST)).toBe("/settings/credentials");
  expect(settingsURL(undefined, LOCAL_HOST)).toBe("/settings");
});

test("a remote host is carried as the host query parameter", () => {
  expect(settingsURL("credentials", "beta")).toBe("/settings/credentials?host=beta");
  expect(settingsURL(undefined, "beta")).toBe("/settings?host=beta");
});

test("a host name is percent-encoded", () => {
  expect(settingsURL("credentials", "a b")).toBe("/settings/credentials?host=a%20b");
});
