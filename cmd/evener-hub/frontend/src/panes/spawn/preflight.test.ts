// @vitest-environment node

import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { describe, expect, test } from "vitest";
import { preflightDir } from "./preflight";

describe("preflightDir", () => {
  test("a valid directory preflights ok", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/path/validate", () => ({ path: "/tmp/p", valid: true }));

    expect(await preflightDir(fake, "/tmp/p")).toEqual({ kind: "ok" });
  });

  test("validates via evener/path/validate with kind:dir", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/path/validate", () => ({ path: "/tmp/p", valid: true }));

    await preflightDir(fake, "/tmp/p");

    expect(fake.calls[0]?.method).toBe("evener/path/validate");
    expect(fake.calls[0]?.params).toEqual({ path: "/tmp/p", kind: "dir" });
  });

  test.each(["path is not a directory", "absolute path required", "path is required"])(
    "a deterministic non-fixable reason (%s) aborts rather than offering to create (floor §1.13)",
    async (reason) => {
      const fake = new FakeClient("ready");
      fake.on("evener/path/validate", () => ({ path: "/x", valid: false, error: reason }));

      expect(await preflightDir(fake, "/x")).toEqual({ kind: "abort", message: reason });
    },
  );

  test("a not-yet-existing directory offers to create it (any other invalid reason, floor §1.13)", async () => {
    const fake = new FakeClient("ready");
    // The dir kind's stat failure surfaces the raw os error verbatim
    // (fspaths ValidateLaunchPath returns err.Error() when os.Stat fails).
    fake.on("evener/path/validate", () => ({
      path: "/tmp/new",
      valid: false,
      error: "stat /tmp/new: no such file or directory",
    }));

    expect(await preflightDir(fake, "/tmp/new")).toEqual({ kind: "offer-create", path: "/tmp/new" });
  });

  test("an invalid result with no error message still offers to create (not a known abort string)", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/path/validate", () => ({ path: "/tmp/new", valid: false }));

    expect(await preflightDir(fake, "/tmp/new")).toEqual({ kind: "offer-create", path: "/tmp/new" });
  });

  test("fails OPEN when the validate check itself throws (spawn.js:573-580)", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/path/validate", () => {
      throw new Error("rpc down");
    });

    expect(await preflightDir(fake, "/tmp/p")).toEqual({ kind: "ok" });
  });
});

// Component 07b: a remote host's directory is validated and created on that
// host through evener/host/request, not on the controller.
describe("preflightDir (remote host routing)", () => {
  test("preflightDir wraps path validation for a remote host", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/host/request", () => ({ path: "/srv/app", valid: true }));

    expect(await preflightDir(fake, "/srv/app", "buildbox")).toEqual({ kind: "ok" });
    expect(fake.calls).toEqual([
      {
        method: "evener/host/request",
        params: { host: "buildbox", method: "evener/path/validate", params: { path: "/srv/app", kind: "dir" } },
      },
    ]);
  });
});
