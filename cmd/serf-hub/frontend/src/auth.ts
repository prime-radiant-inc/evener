// Detects whether THIS browser is authenticated with the hub, and holds the
// copy for the "you aren't" chrome. shell/ConnectionBanner.tsx is the only
// current renderer of that copy - a quiet banner strip is the only chrome
// surface available without touching AppShell.tsx (out of scope for this
// task), so there is no separate "view" component here; this file stays
// JSX-free (auth.ts, not auth.tsx) and exports logic + copy only.
//
// hubedge.AuthGuard (cmd/serf-hub/internal/hubedge/auth_token.go) rejects
// every unauthenticated request with 401 before it reaches its normal
// handler - INCLUDING the /rpc WebSocket upgrade itself, since AuthGuard
// wraps the server's entire mux (cmd/serf-hub/web.go: `auth(...)`), not
// individual routes. That rejection happens at the plain-HTTP layer, before
// the WebSocket handshake ever completes - and browsers do not expose the
// HTTP status code of a failed WebSocket handshake to JS. A rejected
// upgrade surfaces to AppwireClient only as a generic error/close event
// (protocol/client.ts's waitForOpen), indistinguishable from "hub
// unreachable," a bad network, or any other failure. A dropped or
// never-established /rpc connection can NOT, by itself, tell you "wrong or
// missing auth cookie" - the only reliable way to read back a real 401 is a
// plain fetch(), which DOES expose response.status.
export type AuthCheckResult = "authenticated" | "unauthenticated" | "unknown";

// checkAuthStatus fetches "/" - always registered (cmd/serf-hub/web.go),
// and auth-guarded unless the guard is disabled entirely (an empty token,
// a testing-only escape hatch AuthGuard's own comment documents; a live hub
// never runs with one) - and reads back exactly what AuthGuard produces:
// 401 means this browser has no valid cookie/bearer token. Any OTHER status
// means the request got PAST the guard, so this browser IS authenticated,
// regardless of what (if anything) went wrong afterward - including 503,
// the separate "web app not built" fallback (webnext.go's serveSPAIndex,
// checked independently by shell/chrome/webNotBuilt.ts): AuthGuard runs
// before that fallback logic ever sees the request, so 401 and 503 can
// never both describe the same response. A thrown fetch (network failure,
// hub unreachable) isn't an auth signal either way - "unknown" leaves that
// case to the generic closed-connection UI instead of a guess.
//
// /api/health was the obvious-looking alternative and is deliberately NOT
// used: hubedge.isAuthExempt() explicitly exempts it, so it returns 200
// unconditionally regardless of auth state - fetching it can never observe
// a 401 at all.
export async function checkAuthStatus(fetchImpl: typeof fetch = fetch): Promise<AuthCheckResult> {
  let response: Response;
  try {
    response = await fetchImpl("/", { credentials: "same-origin" });
  } catch {
    return "unknown";
  }
  return response.status === 401 ? "unauthenticated" : "authenticated";
}

// The quiet, sentence-case message shown instead of a dead spinner once
// checkAuthStatus resolves "unauthenticated" - shorthand for the fuller
// instructions AuthGuard's own 401 body gives a non-JS client
// (auth_token.go: read the auth URL from the hub's startup log, or the
// token file, then visit /auth?token=<value>).
export const SIGN_IN_PROMPT_MESSAGE = "Open the authorization link from the hub's startup log.";
