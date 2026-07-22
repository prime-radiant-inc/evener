import { afterEach, beforeAll, beforeEach, describe, expect, test } from "vitest";
import type { ModelDescriptor } from "../../protocol/types.gen";
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

// See stores/prefs.test.ts's identical comment: Node 26 shadows jsdom's real
// window.localStorage with its own (non-functional under vitest) global, so
// every test file touching localStorage needs this in-memory stand-in. This
// one additionally implements length/key(i) because sweepStaleModels enumerates
// keys via the Web Storage index API. Scoped to this file only.
class MemoryStorage {
  private store = new Map<string, string>();
  get length(): number {
    return this.store.size;
  }
  key(index: number): string | null {
    return Array.from(this.store.keys())[index] ?? null;
  }
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) ?? null) : null;
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  clear(): void {
    this.store.clear();
  }
}

beforeAll(() => {
  globalThis.localStorage = new MemoryStorage() as unknown as Storage;
});

beforeEach(() => localStorage.clear());
afterEach(() => localStorage.clear());

const MODELS: ModelDescriptor[] = [
  { provider: "anthropic", model: "claude-sonnet-4-5" },
  { provider: "openai", model: "gpt-5" },
];

describe("defaultsKeyFor", () => {
  test("keys a per-project blob by its working dir (floor §1.9, spawn.js:51-53)", () => {
    expect(defaultsKeyFor("/home/me/proj")).toBe("serf-hub.spawn-defaults./home/me/proj");
  });

  test("an empty working dir keys the shared 'global' blob", () => {
    expect(defaultsKeyFor("")).toBe("serf-hub.spawn-defaults.global");
    expect(defaultsKeyFor("   ")).toBe("serf-hub.spawn-defaults.global");
  });
});

describe("loadDefaultsBlob", () => {
  test("returns the parsed blob for a project", () => {
    localStorage.setItem(defaultsKeyFor("/p"), JSON.stringify({ harness: "serf", branch: "main" }));
    expect(loadDefaultsBlob("/p")).toEqual({ harness: "serf", branch: "main" });
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

  test("surfaces harness/branch/access/reasoning from the resolved project blob", () => {
    localStorage.setItem(
      defaultsKeyFor("/p"),
      JSON.stringify({ harness: "serf", branch: "dev", access_mode: "read-only", reasoning_effort: "high" }),
    );
    expect(resolveInitialDefaults({ serverPrefillDir: "/p" })).toMatchObject({
      harness: "serf",
      branch: "dev",
      accessMode: "read-only",
      reasoningEffort: "high",
      workingDir: "/p",
    });
  });
});

describe("saveDefaults", () => {
  test("writes a per-project blob plus the global working_dir on every submit (floor §1.9, spawn.js:100)", () => {
    saveDefaults({ cwd: "/p", harness: "serf", branch: "main", harnessUsesSerfModels: true });
    expect(loadDefaultsBlob("/p")).toMatchObject({ harness: "serf", branch: "main" });
    expect(localStorage.getItem(GLOBAL_WORKING_DIR_KEY)).toBe("/p");
  });

  test("drops the model field for a non-serf-model harness (floor §1.9, spawn.js:92-96)", () => {
    saveDefaults({ cwd: "/p", model: "openai/gpt-5", harnessUsesSerfModels: false });
    expect(loadDefaultsBlob("/p").model).toBeUndefined();
    expect(localStorage.getItem(GLOBAL_MODEL_KEY)).toBeNull();
  });

  test("writes the model globally only when the harness uses serf models AND a model was chosen (floor §1.9, spawn.js:98)", () => {
    saveDefaults({ cwd: "/p", model: "anthropic/claude-sonnet-4-5", harnessUsesSerfModels: true });
    expect(localStorage.getItem(GLOBAL_MODEL_KEY)).toBe("anthropic/claude-sonnet-4-5");
    expect(loadDefaultsBlob("/p").model).toBe("anthropic/claude-sonnet-4-5");
  });

  test("does not write a global working_dir when the submit has no cwd", () => {
    saveDefaults({ cwd: "", harness: "serf", harnessUsesSerfModels: true });
    expect(localStorage.getItem(GLOBAL_WORKING_DIR_KEY)).toBeNull();
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
    localStorage.setItem(defaultsKeyFor("/a"), JSON.stringify({ model: "openai/gpt-4o", branch: "main" }));
    localStorage.setItem(defaultsKeyFor("/b"), JSON.stringify({ model: "legacybare" }));
    localStorage.setItem(defaultsKeyFor("/c"), JSON.stringify({ model: "openrouter/x-anthropic" }));
    localStorage.setItem(defaultsKeyFor("/d"), JSON.stringify({ model: "openai/gpt-5" }));

    const result = sweepStaleModels(MODELS);

    // stale model cleared, blob otherwise preserved
    expect(loadDefaultsBlob("/a")).toEqual({ branch: "main" });
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
