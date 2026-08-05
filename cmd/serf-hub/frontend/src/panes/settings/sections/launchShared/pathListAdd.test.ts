import { describe, expect, test, vi } from "vitest";
import type { LaunchOption } from "../../../../protocol/types.gen";
import { validatePathListAdd } from "./pathListAdd";

function pathListOption(pathKind: string | undefined, wireField = "skillsDirs"): LaunchOption {
  return {
    field: wireField,
    wireField,
    label: "Skill directories",
    group: "Resources",
    kind: "pathList",
    perLaunch: true,
    pathKind,
  };
}

describe("validatePathListAdd", () => {
  test("validates the add server-side with the option's own wire kind", async () => {
    const validatePath = vi.fn().mockResolvedValue({ valid: true });
    const outcome = await validatePathListAdd(pathListOption("dir"), [], "/opt/skills", validatePath);
    expect(validatePath).toHaveBeenCalledWith("/opt/skills", "dir");
    expect(outcome).toEqual({ ok: true, value: "/opt/skills" });
  });

  // schemaPathKind's whole reason for existing: the schema spells it
  // "outputFile", the RPC only accepts "output-file".
  test("an outputFile pathKind reaches the RPC as 'output-file'", async () => {
    const validatePath = vi.fn().mockResolvedValue({ valid: true });
    await validatePathListAdd(pathListOption("outputFile"), [], "/tmp/trace.json", validatePath);
    expect(validatePath).toHaveBeenCalledWith("/tmp/trace.json", "output-file");
  });

  test("a missing pathKind still calls validate, with the empty kind", async () => {
    const validatePath = vi.fn().mockResolvedValue({ valid: true });
    await validatePathListAdd(pathListOption(undefined), [], "/opt/skills", validatePath);
    expect(validatePath).toHaveBeenCalledWith("/opt/skills", "");
  });

  test("an invalid path is rejected with the server's own error", async () => {
    const validatePath = vi.fn().mockResolvedValue({ valid: false, error: "path does not exist" });
    const outcome = await validatePathListAdd(pathListOption("dir"), [], "/nope", validatePath);
    expect(outcome).toEqual({ ok: false, error: "path does not exist" });
  });

  test("an invalid path with no error message gets a generic one", async () => {
    const validatePath = vi.fn().mockResolvedValue({ valid: false });
    const outcome = await validatePathListAdd(pathListOption("dir"), [], "/nope", validatePath);
    expect(outcome).toEqual({ ok: false, error: "invalid path" });
  });

  test("the server-canonicalized path is what gets added", async () => {
    const validatePath = vi.fn().mockResolvedValue({ valid: true, path: "/opt/skills" });
    const outcome = await validatePathListAdd(pathListOption("dir"), [], "/opt/../opt/skills", validatePath);
    expect(outcome).toEqual({ ok: true, value: "/opt/skills" });
  });

  test("a duplicate is rejected without spending an RPC", async () => {
    const validatePath = vi.fn().mockResolvedValue({ valid: true });
    const outcome = await validatePathListAdd(pathListOption("dir"), ["/opt/skills"], "/opt/skills", validatePath);
    expect(outcome).toEqual({ ok: false, error: "Already added." });
    expect(validatePath).not.toHaveBeenCalled();
  });

  test("a path that canonicalizes onto an existing entry is rejected too", async () => {
    const validatePath = vi.fn().mockResolvedValue({ valid: true, path: "/opt/skills" });
    const outcome = await validatePathListAdd(
      pathListOption("dir"),
      ["/opt/skills"],
      "/opt/../opt/skills",
      validatePath,
    );
    expect(outcome).toEqual({ ok: false, error: "Already added." });
  });

  // A broken RPC must not wedge the add row - same fail-open rule the scalar
  // path fields follow.
  test("a validator that throws fails open and accepts the raw input", async () => {
    const validatePath = vi.fn().mockRejectedValue(new Error("socket closed"));
    const outcome = await validatePathListAdd(pathListOption("dir"), [], "/opt/skills", validatePath);
    expect(outcome).toEqual({ ok: true, value: "/opt/skills" });
  });
});
