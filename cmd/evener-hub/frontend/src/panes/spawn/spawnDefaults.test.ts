import type { ModelDescriptor } from "@evener/appwire-client";
import { afterEach, beforeAll, beforeEach, describe, expect, test } from "vitest";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import {
  defaultsKeyFor,
  GLOBAL_LAST_WORKING_DIR_KEY,
  GLOBAL_MODEL_KEY,
  GLOBAL_WORKING_DIR_KEY,
  loadDefaultsBlob,
  modelValidityAgainstList,
  resolveInitialDefaults,
  saveDefaults,
  sweepStaleModels,
} from "./spawnDefaults";

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
});

beforeEach(() => localStorage.clear());
afterEach(() => localStorage.clear());

const MODELS: ModelDescriptor[] = [
  { provider: "anthropic", model: "claude-sonnet-4-5" },
  { provider: "openai", model: "gpt-5" },
];

describe("defaultsKeyFor", () => {
  test("keys a per-project blob by its working dir (floor §1.9, spawn.js:51-53)", () => {
    expect(defaultsKeyFor("/home/me/proj")).toBe("evener-hub.spawn-defaults./home/me/proj");
  });

  test("an empty working dir keys the shared 'global' blob", () => {
    expect(defaultsKeyFor("")).toBe("evener-hub.spawn-defaults.global");
    expect(defaultsKeyFor("   ")).toBe("evener-hub.spawn-defaults.global");
  });
});

describe("loadDefaultsBlob", () => {
  test("returns the parsed blob for a project", () => {
    localStorage.setItem(defaultsKeyFor("/p"), JSON.stringify({ harness: "evener", access_mode: "read-only" }));
    expect(loadDefaultsBlob("/p")).toEqual({ harness: "evener", access_mode: "read-only" });
  });

  test("returns an empty object when absent or malformed", () => {
    expect(loadDefaultsBlob("/missing")).toEqual({});
    localStorage.setItem(defaultsKeyFor("/bad"), "{not json");
    expect(loadDefaultsBlob("/bad")).toEqual({});
  });
});

describe("resolveInitialDefaults", () => {
  test("prefers the server-supplied ?dir= over the global last-resort (floor §1.9)", () => {
    localStorage.setItem(GLOBAL_WORKING_DIR_KEY, "/global/dir");
    expect(resolveInitialDefaults({ serverPrefillDir: "/server/dir" }).workingDir).toBe("/server/dir");
  });

  test("falls back to the global working_dir when no server prefill (floor §1.9, spawn.js:84-88)", () => {
    localStorage.setItem(GLOBAL_WORKING_DIR_KEY, "/global/dir");
    expect(resolveInitialDefaults({}).workingDir).toBe("/global/dir");
  });

  test("layers the global model UNDER a per-project model default (floor §1.9, spawn.js:81-83)", () => {
    localStorage.setItem(GLOBAL_WORKING_DIR_KEY, "/p");
    localStorage.setItem(GLOBAL_MODEL_KEY, "openai/gpt-5");
    localStorage.setItem(defaultsKeyFor("/p"), JSON.stringify({ model: "anthropic/claude-sonnet-4-5" }));
    expect(resolveInitialDefaults({}).model).toBe("anthropic/claude-sonnet-4-5");
  });

  test("uses the global model when the per-project blob has none", () => {
    localStorage.setItem(GLOBAL_WORKING_DIR_KEY, "/p");
    localStorage.setItem(GLOBAL_MODEL_KEY, "openai/gpt-5");
    expect(resolveInitialDefaults({}).model).toBe("openai/gpt-5");
  });

  test("surfaces harness/access/reasoning from the resolved project blob", () => {
    localStorage.setItem(
      defaultsKeyFor("/p"),
      JSON.stringify({ harness: "evener", access_mode: "read-only", reasoning_effort: "high" }),
    );
    expect(resolveInitialDefaults({ serverPrefillDir: "/p" })).toMatchObject({
      harness: "evener",
      accessMode: "read-only",
      reasoningEffort: "high",
      workingDir: "/p",
    });
  });
});

describe("saveDefaults", () => {
  test("writes a per-project blob plus the global working_dir on every submit (floor §1.9, spawn.js:100)", () => {
    saveDefaults({ cwd: "/p", harness: "evener", accessMode: "read-only", harnessUsesEvenerModels: true });
    expect(loadDefaultsBlob("/p")).toMatchObject({ harness: "evener", access_mode: "read-only" });
    expect(localStorage.getItem(GLOBAL_WORKING_DIR_KEY)).toBe("/p");
  });

  test("drops the model field for a non-evener-model harness (floor §1.9, spawn.js:92-96)", () => {
    saveDefaults({ cwd: "/p", model: "openai/gpt-5", harnessUsesEvenerModels: false });
    expect(loadDefaultsBlob("/p").model).toBeUndefined();
    expect(localStorage.getItem(GLOBAL_MODEL_KEY)).toBeNull();
  });

  test("writes the model globally only when the harness uses evener models AND a model was chosen (floor §1.9, spawn.js:98)", () => {
    saveDefaults({ cwd: "/p", model: "anthropic/claude-sonnet-4-5", harnessUsesEvenerModels: true });
    expect(localStorage.getItem(GLOBAL_MODEL_KEY)).toBe("anthropic/claude-sonnet-4-5");
    expect(loadDefaultsBlob("/p").model).toBe("anthropic/claude-sonnet-4-5");
  });

  test("does not write a global working_dir when the submit has no cwd", () => {
    saveDefaults({ cwd: "", harness: "evener", harnessUsesEvenerModels: true });
    expect(localStorage.getItem(GLOBAL_WORKING_DIR_KEY)).toBeNull();
  });

  test("omits the model from the cwd blob for a remote launch, but keeps its other fields (round eight)", () => {
    // The blob is keyed by the cwd alone, and that path is often also a local
    // checkout: a model chosen on the selected host would become this project's
    // LOCAL default - one this hub may not serve, and which the stale-model
    // sweep cannot even recognize as wrong (an unknown provider is left alone).
    saveDefaults({
      cwd: "/srv/app",
      harness: "evener",
      model: "host-only/gpt-5",
      accessMode: "read-only",
      harnessUsesEvenerModels: true,
      remoteLaunch: true,
    });
    expect(loadDefaultsBlob("/srv/app").model).toBeUndefined();
    expect(localStorage.getItem(GLOBAL_MODEL_KEY)).toBeNull();
    // The host-generic layer is still remembered for that project (round
    // seven's "the blob is still written").
    expect(loadDefaultsBlob("/srv/app")).toMatchObject({ harness: "evener", access_mode: "read-only" });

    // A later LOCAL submit of the same project persists its own model, so the
    // omission is scoped to the remote launch, not to the cwd.
    saveDefaults({ cwd: "/srv/app", model: "local/gpt-5", harnessUsesEvenerModels: true });
    expect(loadDefaultsBlob("/srv/app").model).toBe("local/gpt-5");
  });

  // The other half of the same rule (round nine): omitting the model must not
  // ERASE what a previous local launch stored for this path. The write replaces
  // the whole blob, so "no model from this submit" used to mean "no model for
  // this project" - and the next local spawn of the path silently lost its
  // sticky choice.
  test("a remote launch keeps the cwd blob's existing local model (round nine)", () => {
    saveDefaults({ cwd: "/srv/app", harness: "evener", model: "local/gpt-5", harnessUsesEvenerModels: true });
    expect(loadDefaultsBlob("/srv/app").model).toBe("local/gpt-5");

    saveDefaults({
      cwd: "/srv/app",
      harness: "evener",
      model: "host-only/gpt-5",
      accessMode: "read-only",
      harnessUsesEvenerModels: true,
      remoteLaunch: true,
    });

    // The local default survives, the host's own model is still never written -
    // not into the project blob and not into the controller's global scalar.
    expect(loadDefaultsBlob("/srv/app")).toMatchObject({
      harness: "evener",
      access_mode: "read-only",
      model: "local/gpt-5",
    });
    expect(loadDefaultsBlob("/srv/app").model).not.toBe("host-only/gpt-5");
    // The controller's global scalar is untouched by the remote submit too: it
    // keeps the local launch's own value, not the host's.
    expect(localStorage.getItem(GLOBAL_MODEL_KEY)).toBe("local/gpt-5");
    // A fresh local page reads the surviving project default back, layered over
    // whatever the controller-wide scalar says.
    localStorage.setItem(GLOBAL_MODEL_KEY, "other/global-model");
    expect(resolveInitialDefaults({ serverPrefillDir: "/srv/app" }).model).toBe("local/gpt-5");
  });

  test("a remote launch onto a path with no stored model still writes no model (round eight)", () => {
    saveDefaults({
      cwd: "/srv/fresh",
      harness: "evener",
      model: "host-only/gpt-5",
      harnessUsesEvenerModels: true,
      remoteLaunch: true,
    });
    expect(loadDefaultsBlob("/srv/fresh")).toEqual({ harness: "evener" });
  });

  // Round nine's carry-over, generalized to every field rather than the model
  // alone. A remote form names only the fields it re-supplied, so rebuilding
  // the blob from those fields deleted every stored field the remote form left
  // empty - and the model carry-over HID that: it was what kept the write
  // non-empty, so the erasure was silent whenever the path also had a stored
  // model (component 07b review, residual).
  test("a remote launch keeps the stored layer's fields the remote form did not name", () => {
    saveDefaults({
      cwd: "/srv/app",
      harness: "evener",
      accessMode: "full",
      reasoningEffort: "high",
      model: "local/gpt-5",
      harnessUsesEvenerModels: true,
    });
    expect(loadDefaultsBlob("/srv/app")).toEqual({
      harness: "evener",
      access_mode: "full",
      reasoning_effort: "high",
      model: "local/gpt-5",
    });

    // The remote form named its own harness and nothing else: no access mode,
    // no effort, and - by round eight - never a model of its own.
    saveDefaults({ cwd: "/srv/app", harness: "external", harnessUsesEvenerModels: true, remoteLaunch: true });

    // Everything it did not name is still the local project's stored layer.
    expect(loadDefaultsBlob("/srv/app")).toEqual({
      harness: "external",
      access_mode: "full",
      reasoning_effort: "high",
      model: "local/gpt-5",
    });
  });

  // The global scalars are controller-wide defaults, and a remote launch's cwd
  // and model belong to the selected HOST: written here, the next LOCAL spawn
  // would default to a path this machine usually does not have and to a model
  // this hub may not serve.
  test("a remote launch keeps the controller's global defaults out of it (component 07b review, round three)", () => {
    saveDefaults({ cwd: "/p", model: "anthropic/claude-sonnet-4-5", harnessUsesEvenerModels: true });
    expect(localStorage.getItem(GLOBAL_MODEL_KEY)).toBe("anthropic/claude-sonnet-4-5");
    expect(localStorage.getItem(GLOBAL_WORKING_DIR_KEY)).toBe("/p");

    saveDefaults({
      cwd: "/srv/remote-project",
      harness: "external",
      model: "openai/gpt-5",
      accessMode: "read-only",
      harnessUsesEvenerModels: true,
      remoteLaunch: true,
    });

    // Re-pinned to main's landed round-eight rule: the blob is keyed by the cwd
    // alone, and that path is often also a local checkout of the same path, so a
    // model read from the SELECTED host's catalog is never written into it. The
    // branch's own round-four "a remote launch writes nothing at all" is
    // superseded by main's round eight (which keeps the host-generic layer) -
    // with the round-four half that survives ported in as the no-delete guard.
    expect(loadDefaultsBlob("/srv/remote-project")).toMatchObject({
      harness: "external",
      access_mode: "read-only",
    });
    expect(loadDefaultsBlob("/srv/remote-project").model).toBeUndefined();
    expect(localStorage.getItem(GLOBAL_MODEL_KEY)).toBe("anthropic/claude-sonnet-4-5");
    expect(localStorage.getItem(GLOBAL_WORKING_DIR_KEY)).toBe("/p");
  });

  // Round-four refinement of the gate above, ported onto main's landed round
  // eight: the per-project blob is keyed by the cwd ALONE, and a cwd does not
  // identify a host - `/tmp/foo` or `$HOME` is as often a local checkout as a
  // remote path. A harness or model chosen on the remote host came from THAT
  // host's catalogs, so writing the MODEL here would prefill a later LOCAL spawn
  // of the same path with config this hub may not serve (and the stale-model
  // sweep cannot even recognize a foreign provider as wrong). The branch's own
  // round-four "a remote launch writes nothing at all" is re-pinned to main's
  // round eight, which keeps the host-generic layer; what this test keeps of
  // round four is the half below - the blob must not be DELETED either.
  test("a remote launch never erases a local project's per-cwd blob (component 07b review, round four)", () => {
    saveDefaults({
      cwd: "/srv/remote-project",
      harness: "external",
      model: "openai/gpt-5",
      accessMode: "read-only",
      harnessUsesEvenerModels: true,
      remoteLaunch: true,
    });
    // Re-pinned to main's round eight: the host-generic layer is written, the
    // selected host's own model is not.
    expect(loadDefaultsBlob("/srv/remote-project")).toMatchObject({
      harness: "external",
      access_mode: "read-only",
    });
    expect(loadDefaultsBlob("/srv/remote-project").model).toBeUndefined();

    // Nor may it DELETE a local project's stored layer for that path: a remote
    // form with fewer fields is not evidence about the local project.
    const localBlob = JSON.stringify({ harness: "evener", access_mode: "read-only" });
    localStorage.setItem(defaultsKeyFor("/p"), localBlob);
    saveDefaults({ cwd: "/p", harnessUsesEvenerModels: true, remoteLaunch: true });
    expect(localStorage.getItem(defaultsKeyFor("/p"))).toBe(localBlob);
  });
});

describe("modelValidityAgainstList (floor §1.10, spawn.js:154-175)", () => {
  test("a value with no '/' separator is malformed", () => {
    expect(modelValidityAgainstList("claude-sonnet-4-5", MODELS)).toBe("malformed");
  });

  test("an exact provider/model in the list is valid", () => {
    expect(modelValidityAgainstList("openai/gpt-5", MODELS)).toBe("valid");
  });

  test("a known provider whose model is gone is stale", () => {
    expect(modelValidityAgainstList("openai/gpt-4o", MODELS)).toBe("stale");
  });

  test("a provider not enumerated at all is unknown (left untouched)", () => {
    expect(modelValidityAgainstList("openrouter/anthropic-claude", MODELS)).toBe("unknown");
  });
});

describe("sweepStaleModels (floor §1.10)", () => {
  test("clears stale and malformed models across every blob, leaves unknown, and reports discards", () => {
    localStorage.setItem(defaultsKeyFor("/a"), JSON.stringify({ model: "openai/gpt-4o", harness: "evener" }));
    localStorage.setItem(defaultsKeyFor("/b"), JSON.stringify({ model: "legacybare" }));
    localStorage.setItem(defaultsKeyFor("/c"), JSON.stringify({ model: "openrouter/x-anthropic" }));
    localStorage.setItem(defaultsKeyFor("/d"), JSON.stringify({ model: "openai/gpt-5" }));

    const result = sweepStaleModels(MODELS);

    // stale model cleared, blob otherwise preserved
    expect(loadDefaultsBlob("/a")).toEqual({ harness: "evener" });
    // malformed model cleared AND the now-empty blob deleted outright
    expect(localStorage.getItem(defaultsKeyFor("/b"))).toBeNull();
    // unknown provider left untouched
    expect(loadDefaultsBlob("/c")).toEqual({ model: "openrouter/x-anthropic" });
    // valid model untouched
    expect(loadDefaultsBlob("/d")).toEqual({ model: "openai/gpt-5" });
    expect(result.discarded).toEqual(expect.arrayContaining(["openai/gpt-4o", "legacybare"]));
    expect(result.discarded).not.toContain("openrouter/x-anthropic");
    expect(result.discarded).not.toContain("openai/gpt-5");
  });

  test("sweeps the standalone global-model scalar key too (floor §1.10, spawn.js:177-246)", () => {
    localStorage.setItem(GLOBAL_MODEL_KEY, "openai/gpt-4o");
    const result = sweepStaleModels(MODELS);
    expect(localStorage.getItem(GLOBAL_MODEL_KEY)).toBeNull();
    expect(result.discarded).toContain("openai/gpt-4o");
  });

  test("never touches the working-dir scalar keys during the sweep", () => {
    localStorage.setItem(GLOBAL_WORKING_DIR_KEY, "/keep/me");
    localStorage.setItem(GLOBAL_LAST_WORKING_DIR_KEY, "/keep/me/too");
    sweepStaleModels(MODELS);
    expect(localStorage.getItem(GLOBAL_WORKING_DIR_KEY)).toBe("/keep/me");
    expect(localStorage.getItem(GLOBAL_LAST_WORKING_DIR_KEY)).toBe("/keep/me/too");
  });
});
