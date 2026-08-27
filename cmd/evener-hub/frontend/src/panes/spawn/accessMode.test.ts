// @vitest-environment node
import { describe, expect, test } from "vitest";
import {
  ACCESS_MODE_OPTIONS,
  accessModeDefaultLabel,
  accessModeForSandbox,
  mergeAccessModeSandbox,
  sandboxForAccessMode,
} from "./accessMode";

describe("ACCESS_MODE_OPTIONS", () => {
  test("is exactly four fixed rows in order full/read-only/workspace-write/restricted (floor §1.8)", () => {
    expect(ACCESS_MODE_OPTIONS.map((o) => o.value)).toEqual(["full", "read-only", "workspace-write", "restricted"]);
  });

  test("every row has a non-empty sentence-case label", () => {
    for (const option of ACCESS_MODE_OPTIONS) {
      expect(option.label.length).toBeGreaterThan(0);
    }
  });
});

describe("sandboxForAccessMode", () => {
  test("maps each access mode 1:1 to its sandbox value (mirrors web_spawn.go)", () => {
    expect(sandboxForAccessMode("full")).toBe("off");
    expect(sandboxForAccessMode("read-only")).toBe("read-only");
    expect(sandboxForAccessMode("workspace-write")).toBe("workspace-write");
    expect(sandboxForAccessMode("restricted")).toBe("restricted");
  });

  test("an unrecognized or empty mode maps to no sandbox", () => {
    expect(sandboxForAccessMode("")).toBe("");
    expect(sandboxForAccessMode("nonsense")).toBe("");
  });
});

describe("accessModeForSandbox", () => {
  test("is the exact inverse of sandboxForAccessMode for every fixed row", () => {
    for (const option of ACCESS_MODE_OPTIONS) {
      expect(accessModeForSandbox(sandboxForAccessMode(option.value))).toEqual(option);
    }
  });

  test('maps sandbox "off" back to the Full access row', () => {
    expect(accessModeForSandbox("off")).toEqual({ value: "full", label: "Full access" });
  });

  test("an unrecognized or empty sandbox has no row", () => {
    expect(accessModeForSandbox("")).toBeUndefined();
    expect(accessModeForSandbox("nonsense")).toBeUndefined();
  });
});

describe("accessModeDefaultLabel", () => {
  test("names the resolved sandbox in the chip's own friendly wording", () => {
    expect(accessModeDefaultLabel("off")).toBe("Full access (default)");
    expect(accessModeDefaultLabel("read-only")).toBe("Read-only (default)");
    expect(accessModeDefaultLabel("workspace-write")).toBe("Workspace write (default)");
    expect(accessModeDefaultLabel("restricted")).toBe("Restricted (default)");
  });

  test('stays plain "(default)" when nothing resolves (unset everywhere or resolve not landed)', () => {
    expect(accessModeDefaultLabel("")).toBe("(default)");
    expect(accessModeDefaultLabel("   ")).toBe("(default)");
  });

  test("a sandbox value outside the fixed rows is named raw rather than dropped", () => {
    expect(accessModeDefaultLabel("custom-mode")).toBe("custom-mode (default)");
  });
});

describe("mergeAccessModeSandbox", () => {
  test("returns the overrides untouched when the mode has no sandbox mapping", () => {
    expect(mergeAccessModeSandbox(undefined, "")).toBeUndefined();
    const overrides = { maxRounds: 3 };
    expect(mergeAccessModeSandbox(overrides, "")).toBe(overrides);
  });

  test("creates a sandbox-only layer when there are no overrides", () => {
    expect(mergeAccessModeSandbox(undefined, "full")).toEqual({ sandbox: "off" });
  });

  test("fills sandbox into existing overrides that have not set it", () => {
    expect(mergeAccessModeSandbox({ maxRounds: 3 }, "read-only")).toEqual({
      maxRounds: 3,
      sandbox: "read-only",
    });
  });

  test("never clobbers a sandbox the advanced schema already set (floor §1.8: schema wins)", () => {
    const overrides = { sandbox: "workspace-write", maxRounds: 3 };
    expect(mergeAccessModeSandbox(overrides, "full")).toBe(overrides);
  });

  test("does not mutate the caller's overrides object", () => {
    const overrides = { maxRounds: 3 };
    mergeAccessModeSandbox(overrides, "restricted");
    expect(overrides).toEqual({ maxRounds: 3 });
  });
});
