// Pure presentation helpers for the doc pane: a human-readable byte size, and
// (from the package, shared with the phone) a file's display name and whether
// it renders as markdown. Kept in their own
// leaf module so they unit-test without mounting the pane (and so the tiny
// registration module can compute a tab title without importing the pane).

export { filenameOf, isMarkdownPath } from "@evener/appwire-client/docContent";

// formatDocBytes renders a byte count the way the Go handler's formatDocBytes
// does (cmd/evener-hub/doc_serve.go:223): floored binary units, B / KiB / MiB.
export function formatDocBytes(n: number): string {
  if (n >= 1 << 20) return `${Math.floor(n / (1 << 20))} MiB`;
  if (n >= 1 << 10) return `${Math.floor(n / (1 << 10))} KiB`;
  return `${n} B`;
}
