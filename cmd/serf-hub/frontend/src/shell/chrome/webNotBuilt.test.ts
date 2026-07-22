// @vitest-environment node
import { afterEach, describe, expect, test, vi } from "vitest";
import { checkWebNotBuilt, NOT_BUILT_MESSAGE } from "./webNotBuilt";

afterEach(() => {
  vi.restoreAllMocks();
});

describe("checkWebNotBuilt", () => {
  // cmd/serf-hub/webnext.go's serveSPAIndex returns exactly this status
  // when dist/index.html is missing - the one and only handler registered
  // for "/" (cmd/serf-hub/web.go), so no other code path can produce a 503
  // there for an unrelated reason.
  test('returns "not-built" on a 503 - serveSPAIndex\'s own missing-dist signature', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(new Response(null, { status: 503 }));
    await expect(checkWebNotBuilt(fetchImpl)).resolves.toBe("not-built");
  });

  test('returns "ok" on a 200 - the built app shell', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(new Response(null, { status: 200 }));
    await expect(checkWebNotBuilt(fetchImpl)).resolves.toBe("ok");
  });

  // A 401 means AuthGuard rejected the request BEFORE serveSPAIndex ever
  // ran (see ../../auth.ts) - this check genuinely can't tell whether
  // dist/ exists from that response. "ok" here means only "not detected as
  // not-built," never a positive claim the build is fine - so a 401 still
  // resolves "ok" (never a false "not-built"), same as a genuine 200 does,
  // even though the two cases know very different amounts.
  test('returns "ok" (never a false "not-built") on a 401 - AuthGuard ran first, so build status can\'t be read from this response either way', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(new Response(null, { status: 401 }));
    await expect(checkWebNotBuilt(fetchImpl)).resolves.toBe("ok");
  });

  test('returns "unknown" when fetch itself fails - a network/hub-down failure, not a build-status signal', async () => {
    const fetchImpl = vi.fn().mockRejectedValue(new TypeError("Failed to fetch"));
    await expect(checkWebNotBuilt(fetchImpl)).resolves.toBe("unknown");
  });

  test('fetches "/" with same-origin credentials', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(new Response(null, { status: 200 }));
    await checkWebNotBuilt(fetchImpl);
    expect(fetchImpl).toHaveBeenCalledWith("/", { credentials: "same-origin" });
  });

  test("defaults to the global fetch when no override is given", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(null, { status: 503 }));
    await expect(checkWebNotBuilt()).resolves.toBe("not-built");
    expect(fetchSpy).toHaveBeenCalledWith("/", { credentials: "same-origin" });
  });
});

describe("NOT_BUILT_MESSAGE", () => {
  test("is a quiet, sentence-case, actionable instruction", () => {
    expect(NOT_BUILT_MESSAGE).toBe("The hub's web app isn't built. Ask the operator to run the build, then retry.");
  });
});
