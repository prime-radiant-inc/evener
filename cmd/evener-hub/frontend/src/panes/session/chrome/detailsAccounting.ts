// Pure derivations behind the session-details panel's accounting rows. Kept
// dependency-free of React (same convention as statusFormat.ts) so each is
// trivially unit-testable.

// TokenPair/turnUsageTokens/SessionTokens/sessionTokens/tokenUnitLabel are
// the platform-independent session-usage derivation, shared with native; the
// package is their home and this module re-exports them.
export type { SessionTokens, TokenPair } from "@evener/appwire-client";
export { sessionTokens, tokenUnitLabel, turnUsageTokens } from "@evener/appwire-client";

// formatTimestamp renders an instant in the reader's own locale and timezone,
// which is what someone reading "when did this session start" wants. An
// unparseable instant reports absent rather than letting the platform's
// "Invalid Date" string reach the panel.
export function formatTimestamp(iso: string | undefined): string | undefined {
  if (!iso) return undefined;
  const ms = Date.parse(iso);
  if (!Number.isFinite(ms)) return undefined;
  return new Date(ms).toLocaleString();
}
