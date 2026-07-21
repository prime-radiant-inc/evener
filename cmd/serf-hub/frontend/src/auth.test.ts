import { afterEach, describe, expect, test, vi } from "vitest";
import { checkAuthStatus, SIGN_IN_PROMPT_MESSAGE } from "./auth";

afterEach(() => {
  vi.restoreAllMocks();
});

describe("checkAuthStatus", () => {
  test('returns "unauthenticated" on a 401 - hubedge.AuthGuard\'s own rejection status', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(new Response(null, { status: 401 }));
    await expect(checkAuthStatus(fetchImpl)).resolves.toBe("unauthenticated");
  });

  test('returns "authenticated" on a 200 - the request got past AuthGuard', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(new Response(null, { status: 200 }));
    await expect(checkAuthStatus(fetchImpl)).resolves.toBe("authenticated");
  });

  // AuthGuard wraps the server's entire mux and runs before any route's own
  // logic - so a 503 (the SEPARATE "web app not built" fallback,
  // shell/chrome/webNotBuilt.ts's own concern) can only ever be produced
  // for a request that already got past the auth check.
  test('returns "authenticated" on a 503 - past AuthGuard; not-built is a different, later concern', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(new Response(null, { status: 503 }));
    await expect(checkAuthStatus(fetchImpl)).resolves.toBe("authenticated");
  });

  test('returns "unknown" when fetch itself fails - a network/hub-down failure is not an auth signal', async () => {
    const fetchImpl = vi.fn().mockRejectedValue(new TypeError("Failed to fetch"));
    await expect(checkAuthStatus(fetchImpl)).resolves.toBe("unknown");
  });

  test('fetches "/" with same-origin credentials, so the auth cookie actually rides along', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(new Response(null, { status: 200 }));
    await checkAuthStatus(fetchImpl);
    expect(fetchImpl).toHaveBeenCalledWith("/", { credentials: "same-origin" });
  });

  test("defaults to the global fetch when no override is given", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(null, { status: 200 }));
    await expect(checkAuthStatus()).resolves.toBe("authenticated");
    expect(fetchSpy).toHaveBeenCalledWith("/", { credentials: "same-origin" });
  });
});

describe("SIGN_IN_PROMPT_MESSAGE", () => {
  test("is the quiet, sentence-case, actionable instruction this task specifies verbatim", () => {
    expect(SIGN_IN_PROMPT_MESSAGE).toBe("Open the authorization link from the hub's startup log.");
  });
});
