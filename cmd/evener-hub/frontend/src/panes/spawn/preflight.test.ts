// @vitest-environment node

import { describe, expect, test } from "vitest";
import { WireError } from "../../protocol/errors";
import { FakeClient } from "../../protocol/testing/fakeClient";
import { createDir, preflightDir } from "./preflight";

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

describe("createDir", () => {
  test("requests directory creation over AppWire", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/dirs/create", () => ({ path: "/tmp/new", created: true }));

    await createDir(fake, "/tmp/new");

    expect(fake.calls[0]).toEqual({ method: "evener/dirs/create", params: { path: "/tmp/new" } });
  });

  test("throws the server's error message on failure", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/dirs/create", () => {
      throw new WireError("a file already exists at that path", -32013);
    });

    await expect(createDir(fake, "/tmp/file")).rejects.toThrow("a file already exists at that path");
  });
});
